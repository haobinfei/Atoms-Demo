# 模块化单体 + 前后端双部署单元

server 内部不拆微服务，按 `internal/{session,pipeline,provider,builder,pay,auth}` 严格分包的模块化单体；平台自身的交付物拆为两个可独立部署的单元：`server`（Go 单二进制）与 `web`（Vite+React 构建产物，nginx 伺服并反代 `/api`、`/events`），完全复刻 AuthSphere 仓库的 web/server 范式。

考虑过完整微服务化（api/worker/llm-gateway 多进程），否决理由：Demo 的考察维度是开发期可扩展性与可运行性，负载上 LLM 调用为 IO 等待型、构建为毫秒级 CPU 突发，不存在独立伸缩需求；多进程只会引入编排成本与分布式状态一致性难题。server 内部各包保持无内部状态、接口隔离，将来若需拆分为服务可沿包边界低成本切出。
_Avoid_: 在本项目内引入服务网格、消息队列等多进程协调机制。
