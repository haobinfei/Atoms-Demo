# 生成异步化 Stage 2：Postgres 事实源 + Redis Streams 多副本

Stage 1 的「同进程 channel + goroutine Worker 池」不再作为实现路线。生成任务直接进入
Stage 2：Postgres 保存会话、事件和任务事实，Redis Streams 承担队列与实时扇出，
server 无状态化，多副本之间通过外部存储协调。

关键决策：

- **事件事实源是 Postgres**：事件先 append 到 `events` 表，获得单调递增的 `seq`，
  再 best-effort 写入 Redis Stream。Redis 丢失只影响实时性，不影响事实与重放。
- **SSE 双通道**：连接建立时先订阅 Redis Stream，再按 `after=seq` 从 Postgres 重放；
  实时事件按 `seq` 去重，发现空洞时回 Postgres 补齐。Redis 不可用时退化为轮询 Postgres。
- **队列使用 Redis Streams**：`turn` / `contest` / `publish` 作为 Job 入队，消费者组
  提供 Worker 池语义与 at-least-once；消费前仍以会话状态 CAS 作为幂等/认领兜底。
- **崩溃恢复基于状态扫描**：启动或周期性 sweeper 扫描 `status=building` 且无有效租约的
  会话重新入队。重跑前追加恢复事件，竞赛半成品整场重跑。
- **构建产物迁至 COS，活动版本由不可变对象指针选择**：`internal/artifact` 把每次构建
  写成 `sessions/<id>/builds/<buildID>/...`，再用 `sessions/<id>/current.json` 原子切换
  活动版本；`/app/<id>/` 每次请求解析指针，因此不同副本立即看到同一预览/发布版本。
  发布先 stage 新版本，再 activate，最后写 Postgres 发布记录；记录失败会恢复旧指针。
  COS 通过 S3 兼容 SDK 接入，bucket 与临时/永久凭据全部来自 Secret。
- **本地 `dist` 仅作为未配置 COS 时的兼容模式**：所有 `COS_*` 配置为空时保持单副本
  文件存储；出现任一 COS 配置但必填项不完整时启动失败，不静默回退到副本本地。
- **多副本边界**：COS 已解除构建产物对 Pod 本地 `dist` 的依赖，但 app 源码工作区仍由
  `workspace/<owner>/<id>/app` 承担。跨节点多副本必须给源码使用共享 RWX 存储，或后续
  把源码也迁入对象存储；不能把 RWO local-path PV 误当成跨节点共享盘。
- **API 不因 Redis 故障阻塞**：Redis publish/subscribe 失败只降级实时性，不能让生成、
  发布或会话状态转换失败；健康检查可另报 Redis 状态。

这取代 ADR-0006 中「Stage 1 同进程队列、SSE broker 零改动、Redis 属于 Out of Scope」
的边界。ADR-0006 保留为历史决策记录。

_Avoid_：把 Redis 当事实源；Redis 故障时静默丢任务；为了多副本把会话状态继续放在
Pod 本地文件；用截断事件流来掩盖重复投递；把构建产物写回 Pod 本地 `dist`；在 COS
配置不完整时静默回退。
