# ADR-0005: AuthSphere OIDC 接入与按用户隔离

## 状态

已由 ADR-0009（身份和归属）及 ADR-0010（访问边界）取代；本文保留为历史记录。

## 背景

平台需要登录门与会话数据的按用户隔离（票 #7）。AuthSphere 是既有认证层（auth.ss-vip.cn），其 Go SDK 基于 coreos/go-oidc，端点为 `{issuer}/oauth/authorize`、`{issuer}/oauth/token`、`{issuer}/.well-known/jwks.json`，不依赖 discovery 文档。本地开发不能强依赖 AuthSphere 实例。

## 决策

1. **验签用 coreos/go-oidc/v3**：`RemoteKeySet` 拉取 JWKS 并自动缓存轮换，`Verifier` 校验签名、exp、iss、aud。不引入 AuthSphere SDK 的本地路径依赖。
2. **会话数据按属主分区**：`workspace/<owner>/<id>/`，owner = JWT `sub`（经 `ValidOwner` 白名单校验，防目录穿越）。Store 全链路（Load/Save/Append*/BeginTurn/RecordPublish）带 owner 参数。
3. **`/app/<id>/` 保持公开**：被分享者零门槛访问是硬需求。公开路由没有用户上下文，通过全局 `by-id` 索引定位属主工作区；随机 12-hex ID 不可猜测，分享链接即能力。
4. **未认证响应 401 + 前端跳转**：`/api/*`（除 `/api/auth/*`）未认证返回 401 JSON，SPA 探测 `/api/me` 失败后整页跳 `/api/auth/login`；授权码回调在服务端换 token、种 HttpOnly Cookie、跳回 `/`。
5. **dev 旁路**：`AUTH_MODE=dev`（默认）时中间件注入固定开发用户，无需 AuthSphere 即可本地开发；`AUTH_MODE=oidc` 走真实认证。

## 后果

- 隔离语义是"他人 404"而非 403，不泄露会话存在性。
- Publish 记录、事件流、聊天全部继承 owner 维度，无遗漏面。
- 索引文件使磁盘布局多一层；删除会话时需同时清索引（后续管理后台切片处理）。
