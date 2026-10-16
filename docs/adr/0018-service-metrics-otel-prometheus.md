# 0018. 服务指标采集：OTel 语义约定 + Prometheus 文本契约

日期：2026-10-16
状态：已接受

## 背景

服务此前零采集端点、零指标：排障只能看 `slog` 日志与临时 SQL，"当前积压多少任务""是
Redis 满了还是 Postgres 慢""模型链是不是在减员"都只能靠人猜。集群侧其实已具备完整
能力（kube-prometheus-stack 抓取、VictoriaMetrics 存储、Grafana 展示、Tempo 追踪），
缺的只是应用侧的埋点。

指标一旦被采集就是长期契约：指标名被看板和告警按名字引用，改名的代价真实存在且不会
报错——只会静默出一片空白图。因此下面九条决策都要钉死，并把关键几条写成测试。

## 决策

1. **用 OpenTelemetry Go SDK 统一装配，不另起一套 `prometheus/client_golang` 埋点。**
   metrics 恒开（`otelprom` exporter 以 Prometheus 文本格式暴露在 `/metrics`，仍是
   pull 模式）；traces 由 `OTEL_EXPORTER_OTLP_ENDPOINT` 开关，留空即 noop。
   理由有三：本仓库的姊妹项目 AuthSphere 已在 ADR-0001 里明确否决"两套并行"，理由是
   每个 handler 挂两层中间件、埋点维护两份，而 OTel 的 Prometheus exporter 并不损失
   pull 生态；`otelhttp`、`runtime` instrumentation 与 observable callback 恰好覆盖了
   本服务最需要的三类指标（HTTP 服务端、Go 运行时、依赖池与队列积压）；以及 traces
   将来开启时只是加环境变量，不用再改代码。

2. **`/metrics` 挂在业务同一端口 4173，不另开管理端口。**
   已核实 web 侧 nginx 只代理 `^/(api|app|pay)/`，因此该路径不经公网暴露，server 的
   Service 也是 ClusterIP；与 AuthSphere 的 ServiceMonitor 写法（`port: http` +
   `path: /metrics`）一致，chart 改动最小。代价是 `identityMiddleware` 必须把
   `/metrics` 列为公开路径（Prometheus 不带会话 Cookie）——这条由
   `internal/server/metrics_test.go` 钉住。

3. **保留 semconv 原名，但显式压掉常量标签。**
   HTTP 指标保留 `http.server.request.duration`（导出为
   `http_server_request_duration_seconds`），不做 view 重命名——重命名等于放弃 semconv
   的可移植性还要多维护一层 view。同时用 `WithoutScopeInfo()` / `WithoutTargetInfo()`
   与 view 的 `AttributeFilter` 把 `otel_scope_*`、`server.address`、`url.scheme`、
   `network.protocol.*` 这些常量全部去掉：它们对每条序列都重复一遍，纯浪费。
   已知代价：经典 `rate(http_requests_total[5m])` 那套社区看板与告警模板不适用，速率
   要从直方图的 `_count` 算。

4. **路由标签取 `net/http` 的路由模板（`r.Pattern`），且禁止任何租户维度标签。**
   标签集合被 view 收敛为 `http_request_method`、`http_response_status_code`、
   `http.route`、`error.type`；未匹配路由的请求归入 `unmatched`，绝不让原始路径进标签。
   `session_id` / `workspace_id` / `account_id` / `owner` / `email` 一律禁止——
   `TestNoTenantLabelsAnywhere` 是总闸。

5. **事件流（SSE）路由不计入 HTTP 耗时直方图，改用 `atoms.sse.clients.active` 表达。**
   `/api/sessions/{id}/events` 的耗时是"用户看了多久"而不是"服务用了多久"，混进请求
   分位数会同时污染两个查询。它由活跃连接数与回放次数单独表达。

6. **直方图分桶显式配置。**
   SDK 的默认边界是 `[0, 5, …, 10000]`（毫秒尺度的数字），用在以秒为单位的直方图上会让
   亚秒延迟全部落进 `le=0` 而尾部到 10000 秒，分位数不可用（AuthSphere 线上就是这个
   形状）。因此分三档显式给：HTTP 走秒级语义约定值，生成类任务走 0.5s–1800s，构建走
   10ms–30s。

7. **用专用 registry，且只保留一套 Go 运行时指标。**
   显式注册 client_golang 的 `go_*` / `process_*` collector（社区看板与告警认这些名字），
   **不**再叠加 OTel runtime instrumentation：AuthSphere 的 `/metrics` 上两套并存
   （`go_goroutines` 与 `go_goroutine_count` 同时存在），语义重叠只会让人在看板里选错。

8. **队列积压用 observable callback 在抓取时拉取，并给 `jobqueue.Queue` 接口加一个
   只读的 `Stats`。**
   抓取间隔就是采样间隔，不需要后台轮询 goroutine；Redis 实现取消费者组的 `lag`
   （还没投递出去）与 `pending`（已投递未确认），两者对应完全不同的故障。依赖不可用时
   这一轮不上报——把 0 当成"没有积压"比缺数据更危险。这是 Go 接口扩展，**不涉及任何
   表结构**，符合 `docs/design/atoms-dev-gap-and-optimization.md` §8.1 的边界。

9. **本轮不开 traces、不做告警、不改数据模型，也不往 `CONTEXT.md` 加词。**
   traces 留空即 noop，等 metrics 链路验证通过后单独一轮开。集群**没有部署
   Alertmanager**，现在写 PrometheusRule 只会产出没有接收方的死信；夜莺刚上线不久，
   接线方式需要另行确认。监控是基础设施词汇而非领域词汇，不进术语表——但指标命名必须
   沿用领域术语的英文（`turn` 回合、`job` 任务、`build` 构建），不使用被 `_Avoid_`
   的 `task` / `version`。

## 后果

- 埋点落在 `internal/observability`（装配 + 指标句柄 + 依赖适配）、`internal/jobqueue`、
  `internal/server`、`internal/pipeline`、`internal/provider`、`internal/builder`、
  `internal/eventstream`、`internal/artifact`、`internal/server/pay.go` 与
  `identity_http.go`。指标句柄必须在 `Setup()` 之后经 `observability.Obs()` 取得。
- 契约由测试钉住，改动会让 CI 红而不是静默打断看板：
  `TestOwnedMetricNames`（指标名清单）、`TestHTTPRouteLabelIsBounded` 与
  `TestNoTenantLabelsAnywhere`（基数）、`TestHTTPMetricLabelsAreCompressed`（常量标签）、
  `TestHistogramBucketsAreSecondsScale`（分桶）、`TestMetricsEndpointIsNotInstrumented`、
  `TestStreamingRouteIsExcludedFromHTTPMetrics`、
  `TestMetricsEndpointIsScrapable`（启用身份中间件时仍可匿名抓取）。
- 采集侧的清单在平级仓库 `atoms-demo-gitops`：`templates/servicemonitor.yaml`
  （**必须带 `release: monitoring` 标签**，平台的 `serviceMonitorSelector` 就是它，
  漏了则端点通了也没数据）与 `templates/grafana-dashboard.yaml`（看板 JSON 在
  `dashboards/atoms-demo.json`），两者都带 values 开关。
- Prometheus 抓到的指标 remoteWrite 进 `vminsert`，Grafana 的默认数据源是
  VictoriaMetrics（uid `victoriametrics`），看板查询按此编写。
- 队列与事件扇出现在共用一个 Redis 客户端（`RedisQueue.Close` 本就是 no-op，正是为了
  让调用方持有连接）：少一份连接，也少一个连接池要监控。
