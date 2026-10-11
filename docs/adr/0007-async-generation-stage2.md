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
- **构建产物最终迁至 COS**：workspace 的 RWO 约束不能支撑 API 多副本，`dist` 产物
  迁移对象存储后，API 层才具备完整多副本条件。
- **API 不因 Redis 故障阻塞**：Redis publish/subscribe 失败只降级实时性，不能让生成、
  发布或会话状态转换失败；健康检查可另报 Redis 状态。

这取代 ADR-0006 中「Stage 1 同进程队列、SSE broker 零改动、Redis 属于 Out of Scope」
的边界。ADR-0006 保留为历史决策记录。

_Avoid_：把 Redis 当事实源；Redis 故障时静默丢任务；为了多副本把会话状态继续放在
Pod 本地文件；用截断事件流来掩盖重复投递。
