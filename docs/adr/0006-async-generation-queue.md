# 生成异步化：进程内队列 + Worker 池（Stage 1）

散养 goroutine 升级为有纪律的队列系统：turn/contest/publish 三种任务进入有界队列（容量 = Worker 数 ×4，满则 503 拒绝），同进程 Worker 池消费（默认 3，即 LLM 并发闸门）。Job 只携带会话指针，Worker 消费前做原子认领（与 HTTP 线程的 BeginTurn 构成双层防线），交付语义 at-least-once。崩溃恢复靠启动扫描所有 building 会话重新入队，竞赛半成品整场重跑，重跑前写恢复事件。

三个被明确否决的选项：不引入 queued 状态（排队以秒计，多一个状态不值，Stage 2 外置队列后再议）；不拆 per-contestant 任务（要等全家到齐的协调状态机复杂度不值）；队列满不阻塞等待、不静默降级回 goroutine（过载时诚实拒绝优于伪装健康）。

Stage 1 刻意限定为同进程 goroutine 池：broker/SSE 扇出机制零改动，Worker 只是「有纪律的执行者」而非跨进程执行者。外置队列、跨副本扇出、KEDA 是 Stage 2，接口（Enqueue/Start、Job 指针化）已为此预留。
_Avoid_: 为追求「纯队列」而放弃 HTTP 线程的同步 409 反馈（双击发送立即报错优于几秒后在时间线看到错误事件）；为消重而截断事件流（append-only 与「done 后所有写入完成」的约定会被破坏）。
