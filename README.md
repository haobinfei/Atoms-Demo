# Atoms Demo

把一句话变成可运行的网页应用：描述想法，一支 AI 团队（生成引擎 + 构建器）
生成 React 代码、编译成静态产物并在 iframe 里实时预览；不满意就继续对话迭代，
还能让多个模型竞赛、发布公开链接、在生成的电商应用里走一遍模拟支付。

Go 模块化单体（HTTP API + SSE 事件流 + 文件工作区）+ React SPA，
前后端独立部署。设计共识见 [CONTEXT.md](CONTEXT.md) 与 [docs/adr/](docs/adr)。

## 功能一览

- **对话式生成**：输入想法 → 计划/规格/架构/写文件 → esbuild 构建 → iframe 预览
- **迭代回合**：在会话里继续描述变化，基于当前文件集增量再生成
- **竞赛模式**：勾选「竞赛模式」，多位 AI 参赛者并行生成，评审 Nina 三维度打分
  （设计/功能/代码质量 + 点评），你点选胜者；败者版本可随时回看
- **发布与扫码**：发布公开链接 `/app/{id}/`，版本历史 + 二维码 + 复制链接
- **模拟收银台**：生成的电商应用内置 AtomsPay SDK，加购 → 下单 → 平台托管
  收银台 → 测试卡号支付 → 回调应用内订单列表（明示模拟，不产生真实交易）
- **多项目管理**：列表 / 搜索 / 重命名 / 置顶 / 复制 / 删除
- **账户与工作区**：邮箱注册、验证和密码登录；团队工作区按所有者／编辑者角色协作

## 本地开发

要求：Docker Compose；单独运行服务端时还需 Go 1.25+（见 go.mod）、Node 20+。

```bash
docker compose up --build
```

打开 http://localhost:8080；注册、邀请与密码重置邮件可在 http://localhost:8025 查看。
本地 Postgres 在新数据卷首次创建时自动加载现有会话表与身份表；保留数据卷重启不会重放
初始化 SQL，后续结构演进应走迁移。Postgres 和 Mailpit 仅绑定本机回环地址，可通过
`POSTGRES_PORT`、`MAILPIT_SMTP_PORT`、`MAILPIT_WEB_PORT` 调整端口。本地数据库
凭据只供开发使用，不得用于生产。

手动运行 Go 与 Vite 时，先启动本地依赖，再把邮件链接指向 Vite 端口：

```bash
docker compose up -d postgres mailpit
DATABASE_DSN='postgres://atoms:local-dev-only@localhost:5432/atoms?sslmode=disable' SMTP_HOST=localhost SMTP_PORT=1025 SMTP_SECURITY=none SMTP_FROM=noreply@localhost APP_BASE_URL=http://localhost:5173 COOKIE_SECURE=0 go run ./cmd/server

# 前端（:5173，/api、/app、/pay 反代到 4173）
cd web && npm install && npm run dev
```

### 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` | `4173` | 后端监听端口 |
| `WORKSPACE_DIR` | `workspace` | 会话工作区根目录 |
| `DATABASE_DSN` | Compose 内置 Postgres | 账户、成员、邀请和登录会话的 Postgres DSN；生产由 Secret 注入 |
| `TRUSTED_PROXY_CIDRS` | 空 | 认证限流只信任这些直连代理覆盖的 `X-Real-IP`；应配置实际入口代理 CIDR，勿笼统信任公网或整个私网 |
| `APP_BASE_URL` | Compose 为 `http://localhost:8080` | 注册、邀请及重置邮件中的外部访问地址；本地使用 web 端口，生产设置为公开 HTTPS 域名 |
| `COOKIE_SECURE` | 生产为开启，Compose 为 `0` | 仅本地 HTTP 开发环境禁用登录 Cookie 的 Secure 属性 |
| `PLATFORM_ADMIN_EMAILS` | 空 | 逗号分隔的受控管理员邮箱；必须正常注册、验证，管理员身份不授予项目内容权限 |
| `SMTP_HOST` / `SMTP_PORT` | Compose 内置 Mailpit | SMTP 主机和端口；163 邮箱使用 `smtp.163.com:465` |
| `SMTP_SECURITY` | `tls`；Compose 为 `none` | `tls` 为隐式 TLS（163 推荐）；也支持 `starttls`；`none` 仅供本地 Mailpit |
| `SMTP_FROM` / `SMTP_USERNAME` / `SMTP_PASSWORD` | Compose 使用本地发件地址、无认证 | 生产由 Secret 注入发件邮箱和轮换后的 SMTP 授权码；不得提交真实值或写入日志 |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_USERNAME` / `REDIS_PASSWORD` | 空 | Redis Streams 同时承担实时扇出与任务队列（ADR-0007）。`REDIS_HOST` 留空退回单进程 MemoryBus + 有界 MemoryQueue；配置 Redis 后队列不可用（满/断连）时入队返回 503，**不静默回退内存队列**、不丢任务；事件扇出失败仍降级为 Postgres 重放 |
| `QUEUE_WORKERS` | `3` | Worker 池大小，也是 LLM 并发闸门；内存队列容量为其 ×4 |
| `COS_BUCKET` / `COS_ENDPOINT` / `COS_SECRET_ID` / `COS_SECRET_KEY` / `COS_SESSION_TOKEN` | 空 | 构建产物 Artifact Store；全部留空时 `/app` 读本地 `dist`。配置后构建产物写入腾讯 COS，多副本通过不可变 build + `current.json` 指针共享预览/发布。凭据由 `atoms-demo-env` Secret 注入；当前临时 session token 上线前换 CAM 子账号永久密钥 |
| `DEEPSEEK_API_KEY` | 空 | 主生成引擎；未配置且没有其他可用模型时，生成回合显式失败 |
| `DEEPSEEK_BASE_URL` / `DEEPSEEK_MODEL` | 官方默认值 | DeepSeek 兼容端点覆写 |
| `KIMI_API_KEY` / `KIMI_BASE_URL` / `KIMI_MODEL` | 空 / 官方默认值 | Kimi 生成引擎（可选） |
| `LOCAL_LLM_BASE_URL` | `http://192.168.1.101:8090/v1` | 本地 gemma（llama.cpp OpenAI 兼容） |
| `LOCAL_LLM_MODEL` | 空 | 本地模型名 |
| `ATOMS_API`（前端） | `http://localhost:4173` | vite 代理目标 |

公开认证端点按客户端 IP 和邮箱（仅保存摘要）双重限流，计数写入 Postgres，多个 API 副本共享。
数据库不可用时返回 503，不会绕过限制。生产部署前需在 gitops 所管理的数据库中
迁移完整身份表结构（见 `internal/identity/schema.sql`，包含 `identity_auth_limits`）；
本地仅新建数据卷时自动建表。保留旧数据卷时也须显式迁移，不能只重启服务。
`TRUSTED_PROXY_CIDRS` 默认为空，此时经同一 Nginx 转发的请求共享代理 IP 限额。
需要区分客户端时，将实际代理地址配置为最窄 CIDR（单个 IPv4 地址使用 `/32`，IPv6 使用 `/128`）；
代理必须覆盖 `X-Real-IP`，并限制后端仅允许该代理访问。

生成引擎在已配置的真实模型之间按顺序容错：DeepSeek → Kimi → 本地 gemma。模型全部
不可用、输出无法解析或文件校验失败时，回合直接标记失败并把错误写入事件流，不再回退
到固定模板应用。

构建产物存储有两种模式：未配置 COS 时在 `workspace/<workspaceID>/<id>/` 保存产物（本地开发与单副本）；配置 COS 后每次成功构建写入不可变对象目录，`current.json` 原子切换活动版本。成员预览使用当前草稿，匿名访问使用独立发布快照，未发布的修改不会改变公开版本。发布先激活快照，再写 Postgres 发布记录；记录失败恢复旧快照。API 多副本的 `/app` 因此不依赖各 Pod 本地 `dist`。

## 测试

```bash
docker compose -f e2e/compose.yaml up -d --wait
IDENTITY_TEST_DSN='postgres://atoms_e2e:local-e2e-only@127.0.0.1:55432/atoms_e2e?sslmode=disable' go test -race ./...  # 含真实 Postgres 身份与 HTTP 集成测试

cd e2e && npm install && npx playwright install chromium
npx playwright test            # 端到端：桌面 + 移动视口
```

E2E 依赖独立 Postgres 和 Mailpit；Playwright 启动测试专用 Go 服务（生成引擎
使用确定性模板）及构建后的 SPA，覆盖注册、邮箱验证、登录、退出、团队邀请与角色管理、创作、公开访问及取消公开。
启动和清理步骤见 [e2e/README.md](e2e/README.md)。生产部署还需在独立应用域
隔离生成代码，并完成对应的 DNS/TLS/Ingress、私有预览访问与支付联调；当前同源
`/app/` 路由不能作为已完成的生产权限边界。

## 演示脚本（一条链）

1. 首页输入「一家咖啡店的电商网站」，点 **开始构建** —— 看时间线推进，
   预览出现可购物的咖啡店页面
2. 点「加入购物车」→「去结算」→ 平台收银台（顶部橙色横幅：模拟支付，
   不产生真实交易）→ 卡号已预填 `4242 4242 4242 4242`，点支付 →
   跳回应用，绿色横幅「支付成功」，「我的订单」里出现已支付订单
3. 回到首页，勾选 **竞赛模式** 输入「一个数据运营分析仪表盘」点 **开赛** ——
   多位参赛者并行生成，Nina 逐个打分点评，点 **选为胜者**，败者版本可在
   版本栏回看
4. 点 **发布** 弹出公开链接 + 二维码，手机扫码直接访问

## 部署

Docker 与 GitOps（Argo CD + Helm，push 后自动拉起）见平级仓库
`atoms-demo-gitops` 与其 README；本仓库提供 `docker-compose.yml` 一键起全栈、
Dockerfile（server / web）与 `.env.example`。

生产多副本前需满足两点：`atoms-demo-env` Secret 包含全部 `COS_*` 键；app 源码工作区必须是所有副本可共同访问的存储（共享 RWX PV，或后续把源码也迁入对象存储）。COS 迁移只解除构建产物对 Pod 本地 `dist` 的依赖。
