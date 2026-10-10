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
- **AuthSphere 登录**：OIDC 接入，会话按用户隔离；未登录可访问被分享的应用
  及其收银台

## 本地开发

要求：Go 1.26+、Node 26+。

```bash
# 后端（:4173，默认 dev 模式，免登录）
go run ./cmd/server

# 前端（:5173，/api、/app、/pay 反代到 4173）
cd web && npm install && npm run dev
```

打开 http://localhost:5173 即可。

### 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` | `4173` | 后端监听端口 |
| `WORKSPACE_DIR` | `workspace` | 会话工作区根目录 |
| `AUTH_MODE` | `dev` | `dev` 免登录（`DEV_USER`），`oidc` 走 AuthSphere |
| `DEEPSEEK_API_KEY` | 空 | 主生成引擎（留空则降级到本地模型/模板） |
| `DEEPSEEK_BASE_URL` / `DEEPSEEK_MODEL` | 官方默认值 | DeepSeek 兼容端点覆写 |
| `LOCAL_LLM_BASE_URL` | `http://192.168.1.101:8090/v1` | 本地 gemma（llama.cpp OpenAI 兼容） |
| `LOCAL_LLM_MODEL` | 空 | 本地模型名 |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | — | AuthSphere 接入（`AUTH_MODE=oidc` 时必填） |
| `OIDC_REDIRECT_URL` | `http://localhost:4173/api/auth/callback` | 回调地址 |
| `ATOMS_API`（前端） | `http://localhost:4173` | vite 代理目标 |

生成引擎是三级兜底链：DeepSeek → 本地 gemma → 内置模板（意图识别 + 七类应用
骨架），任何一级不可用自动降级，绝不卡死。

## 测试

```bash
go test -race ./...          # 单元 + HTTP 集成测试

cd e2e && npm install && npx playwright install chromium
npx playwright test            # 端到端：桌面 + 移动视口各跑一遍 happy path
```

E2E 自包含一条命令：Playwright 自动拉起真实 Go 服务（生成引擎打桩为确定性
模板）+ 构建后的 SPA，浏览器里完成 输入 prompt → 时间线渲染 → iframe 非空白
且可交互（加购）→ 发起修改回合 → 预览刷新 全链路。

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
