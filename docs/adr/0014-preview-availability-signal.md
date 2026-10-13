# ADR-0014: 预览可用性由服务端派生标志判定

`/app/<id>/` 是否可预览，由**服务端派生**得出，并以 `SessionSummary` 上的只读字段
`previewReady` 暴露给前端；前端只在 `previewReady` 为真时渲染预览 iframe，并叠加一个
加载超时兜底。**不再把 `iframe.onError` 作为判据。**

原因是 `iframe.onError` 不可靠：跨文档的 HTTP 错误响应（例如 404、502 返回的 HTML 页面）
属于"加载成功"，不会触发 `onError`，因此降级封面会漏判，用户看到的是一个错误页而不是
降级封面。前端主动探测（对 `/app/<id>/` 发 HEAD 请求）是次优方案：它为每张卡片增加一次
额外请求，而且本地 `dist` 模式与 COS artifact 模式下的失败表现并不一致，需要逐一核对
才能可靠判断。服务端本来就是"产物是否存在"的唯一权威——它同时掌握会话 `status` 与
Artifact Store 的 `current.json` 指针。

`previewReady` 是**派生字段，不落库**：它由现有事实计算得出，因此不引入新的持久状态，
也不产生第二份事实源。

`previewReady` 只是**渲染提示，不是权限判据**。`/app/` 当前与 SPA 同源，README 已明确
该路由尚不能作为已验收的生产权限边界（另见 ADR-0010）；预览的访问控制必须由服务端在
请求处理时独立判定，不得依赖前端是否渲染了 iframe。

_Avoid_：用 `iframe.onError` 作为唯一可用性判据；把 `previewReady` 落库造成双份事实；
把前端是否渲染预览当作访问控制；在 `/app` 权限边界尚未验收的情况下让 `previewReady`
承担授权语义。
