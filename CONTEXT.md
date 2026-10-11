# Atoms Demo

一个 Atoms 风格的 AI 应用生成平台原型：用户用自然语言描述想法，由多智能体团队驱动 LLM 生成可运行的 React 工程，并以可视化网页实时预览。本项目为单上下文仓库。

## 生成域

**会话（Session）**:
一次"想法 → 应用"的完整工作区，包含消息历史、智能体事件日志和生成的文件，磁盘持久化于 `workspace/<owner>/<id>/`，按属主隔离。
_Avoid_: 项目（见下）

**属主（Owner）**:
会话的拥有者，即 AuthSphere JWT 的 `sub`。所有会话数据按属主隔离；他人访问一律 404，不泄露存在性。

**ID 索引（by-id Index）**:
`workspace/by-id/<id> → owner` 的全局映射。`/app/<id>/` 公开可分享且不带属主信息，靠随机会话 ID（不可猜测）定位工作区——分享链接本身就是访问能力。

**项目（Project）**:
多项目后台的列表项展示层：一个项目即一个会话的卡片（名称、品类、状态、置顶、修改时间）。对用户的可见单位。

**品类（Category）**:
项目卡片的类型标签，取生成引擎意图识别的结果（todo/dashboard/ecommerce/site），纯展示用途。

**回合（Turn）**:
会话中一次用户输入加智能体团队的一次完整响应。首建是一个回合，每次修改也是一个回合。

**AI 团队（AI Team）**:
平台上的多智能体人设集合——Mike（团队负责人）、Emma（产品经理）、Bob（架构师）、Alex（工程师）、Sarah（SEO 专家）、Nina（评审）。

**流水线（Pipeline）**:
单个回合内智能体按序执行的编排过程：拆需求 → 出规格 → 定架构 → 生成代码 → 补元信息。

**事件流（Event Stream）**:
流水线执行中经 SSE 推给前端的按序事件序列，是时间线 UI 的唯一数据源。

**规格（Spec）**:
Emma 产出的结构化产品描述。每个回合结合完整历史重新推导，是全量重生成的输入。

**Provider（生成引擎）**:
把"用户需求"变成"文件集合"的可插拔后端。三个实现：DeepSeekProvider、LocalProvider（本地 gemma）、TemplateProvider（模板兜底）。

**构建器（Builder）**:
服务端 esbuild 封装，把会话的文件集编成可预览的静态产物（`dist/index.html` + bundle.js）。

**供应商目录（vendor/）**:
预置的 React ESM 构建，是 esbuild resolve `react`/`react-dom` 的唯一来源，保证构建零网络依赖。

**任务（Job）**:
一次「会话独占执行」的最小调度单位，粒度与 BeginTurn 的认领语义一一对应。三种：回合（turn，首建与后续 chat 共用）、竞赛（contest，整场一个 job，内部多参赛者并行）、发布（publish）。Job 只携带 `{属主, 会话ID, 种类}` 指针，全部上下文从会话存储现取——同一指针重复消费天然幂等。
_Avoid_: 任务（指 Job 时请说「任务」，勿与「回合」混用；回合是领域过程，任务是调度对象）

**队列（Queue）**:
任务的入口缓冲，有界（容量 = Worker 数 ×4），满了拒绝入队（调用方收到 503）而不是无限排队。接口形态（Enqueue/Start）为 Stage 2 外置队列（NATS/Redis）预留，进程内 channel 只是当前实现。

**Worker**:
队列的消费者——同进程内的有界 goroutine 池（默认 3，QUEUE_WORKERS 可调）。Worker 数即 LLM 并发闸门。消费任务前必须再次原子认领会话（status CAS），认不上则丢弃该任务——与 HTTP 线程的 BeginTurn 构成「快速 409 + 正确性兜底」双层防线，交付语义 at-least-once。

**崩溃恢复（Recovery）**:
服务启动时扫描所有处于 building 状态的会话重新入队的机制。竞赛半成品整场重跑（覆盖半成品目录，语义干净）；重跑前追加一条恢复事件，不静默重花 LLM 调用。

## 业务域

**竞赛（Competition）**:
同一 prompt 由多个 Provider 并行生成不同版本，由 Nina 调 LLM 打分、用户点选胜者的模式。胜者成为会话的正式应用，败者归档可回看。

**模拟收银台（Mock Checkout）**:
平台托管的支付确认页。生成的应用经 `AtomsPay.js` SDK 调用，走"下单 → 收银台 → 回调"闭环，订单服务端持久化，不产生真实交易。
_Avoid_: 假支付、伪支付

**测试卡号**:
模拟收银台接受的固定假卡号（`4242 4242 4242 4242`），明示交易的模拟性质。

**发布（Publish）**:
把会话的构建产物开放为固定公网 URL（`/app/<id>/`）并生成二维码与发布记录的动作。

## 部署域

**部署单元（Deployable）**:
一个可独立构建、发布、扩缩的进程级交付物。本设计恰有两个：`server`（Go API）与 `web`（SPA 静态资源 + nginx）。
