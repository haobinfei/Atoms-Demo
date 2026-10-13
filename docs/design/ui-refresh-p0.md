# UI 刷新 P0 方案：设计系统重构 + 项目缩略图

- 状态：设计系统及核心页面视觉改造已实现；项目卡片使用实时 iframe 预览，截图上传及缩略图 API 待实现
- 范围：`web/src/styles.css`、`web/src/*.tsx`（视觉层），以及缩略图所需的后端最小改动
- 目标：把「页面美观度」从 2/5 提升到可感知的 4/5；把项目列表从「文字卡片」变成「可视化作品墙」
- 非目标：本轮不做浅色主题切换、不做可视化编辑器、不做 GitHub 同步

## 1. 现状诊断（均来自当前代码）

| # | 问题 | 证据 | 影响 |
| --- | --- | --- | --- |
| 1 | 设计 token 极少 | `web/src/styles.css:1-8` 只有 6 个变量 | 任何全局调整都要全文件搜改 |
| 2 | 存在失效变量 | `var(--dim)` 被引用 3 次（`fd-head button`、`fd-list button`、`fd-empty`），但 `:root` 未定义 | 这些元素颜色回退为继承色，视觉错乱 |
| 3 | 双主色混用 | 品牌橙 `#f97316` 与青绿 `#43b7aa`/`#65c7bd`（认证页、管理页）并存 | 前台与后台像两个产品 |
| 4 | 硬编码颜色散落 | `#1d2b1f`、`#1f2430`、`#0a0c10`、`#222831`、`#171a20`、`#3a4350` 等 | 主题无法统一调整 |
| 5 | 无字阶 | 字号出现 12/13/14/15/16/18/20/24/32 | 层级不清、正文显小 |
| 6 | 圆角无规范 | 出现 4/5/6/8/10/12/16/24/999 | 观感零碎 |
| 7 | 无阴影与动效体系 | 全文件仅 1 处 `transition`、1 个 `@keyframes` | 界面「平」，缺交互反馈 |
| 8 | 项目卡片无缩略图 | `web/src/api.ts:158-166` 的 `SessionSummary` 只有 id/name/category/status/pinned/prompt/updatedAt | 列表不可视化，无法「一眼看到作品」 |

## 2. 设计系统 v2

### 2.1 Token 层（替换 `:root`）

```css
:root {
  /* 中性色阶：surface 分 3 级制造层次 */
  --c-bg: #0b0d12;
  --c-surface-1: #12151c;
  --c-surface-2: #171b24;
  --c-surface-3: #1e2330;
  --c-border: #262c3a;
  --c-border-strong: #333b4d;

  /* 文本 */
  --c-text: #e8eaf0;
  --c-text-dim: #9aa3b2;
  --c-text-muted: #6b7385;

  /* 品牌（统一为橙色单一主色） */
  --c-accent: #f97316;
  --c-accent-hover: #fb8c3c;
  --c-accent-soft: rgba(249, 115, 22, 0.14);

  /* 语义色 */
  --c-success: #22c55e;
  --c-danger: #ef4444;
  --c-info: #43b7aa;

  /* 字阶 */
  --fs-xs: 12px;
  --fs-sm: 13px;
  --fs-md: 14px;
  --fs-lg: 16px;
  --fs-xl: 20px;
  --fs-2xl: 28px;
  --fs-3xl: 36px;

  /* 间距（4 的倍数） */
  --sp-1: 4px;
  --sp-2: 8px;
  --sp-3: 12px;
  --sp-4: 16px;
  --sp-5: 20px;
  --sp-6: 24px;
  --sp-8: 32px;
  --sp-10: 40px;

  /* 圆角 */
  --r-sm: 6px;
  --r-md: 10px;
  --r-lg: 14px;
  --r-xl: 20px;
  --r-full: 999px;

  /* 阴影 */
  --sh-1: 0 1px 2px rgba(0, 0, 0, 0.3);
  --sh-2: 0 6px 20px rgba(0, 0, 0, 0.35);
  --sh-pop: 0 12px 40px rgba(0, 0, 0, 0.5);

  /* 动效 */
  --dur-fast: 120ms;
  --dur: 200ms;
  --ease: cubic-bezier(0.2, 0.7, 0.3, 1);

  /* 布局 */
  --header-h: 56px;
}
```

**兼容策略**：先加新 token，同时把旧名保留为别名（`--bg: var(--c-bg)`、`--panel: var(--c-surface-2)`、`--border: var(--c-border)`、`--text: var(--c-text)`、`--text-dim: var(--c-text-dim)`、`--accent: var(--c-accent)`），并补上缺失的 `--dim: var(--c-text-dim)`。这样第一步就能修掉第 2 条 bug，且不产生大范围破坏。

### 2.2 组件层（新增基础类）

- 按钮：`.btn`、`.btn--primary`、`.btn--ghost`、`.btn--danger`
- 表单：`.field`（统一 input/textarea 的 44px 高、边框、聚焦描边）
- 卡片：`.card`（surface-2 + border + r-md + hover 抬升）
- 徽章：`.badge`、`.badge--success`、`.badge--danger`、`.badge--muted`
- 标签页：`.tabs`、`.tab`、`.tab.is-active`
- 胶囊：`.chip`
- 交互三态统一：所有可点元素补齐 `:hover` / `:focus-visible`（`outline: 2px solid var(--c-accent-soft)`）/ `:disabled`，并统一 `transition: var(--dur) var(--ease)`

### 2.3 主题策略

默认保留深色，但把 surface 分成 3 级制造层次——**问题不在深浅，而在层次与留白比例**。预留 `[data-theme='light']` 覆盖层，后续可一键切换，不阻塞本轮。

## 3. 页面改造清单

| 页面 | 文件 | 改造要点 |
| --- | --- | --- |
| 顶部导航 | `App.tsx` | 高度统一 56px；导航 active 改为「胶囊底 + 主色文字」，替换现有青色下划线；右侧用户区改头像 + 下拉（邮箱/账户/退出） |
| 认证页 | `Auth.tsx` | 左栏升级为品牌区（渐变 + 细网格 + 3 条价值点：生成/预览/发布）；右栏表单卡片化（surface-2 + r-lg + sh-2）；统一主色为橙 |
| 首页 | `Home.tsx` | Hero 标题升到 `--fs-2xl`；Composer 卡片用 `--r-xl` + `--sh-2`，聚焦描边发光；示例 prompt 用 `.chip` |
| 项目列表 | `Dashboard.tsx` | 卡片改「封面 + 信息 + 操作」三段式；封面用真实缩略图，无图回退分类渐变；状态/分类用 `.badge`；hover `translateY(-2px)` + `--sh-2`；空态加插画位与主 CTA |
| 工作台 | `Workspace.tsx`、`Timeline.tsx`、`Preview.tsx` | 时间线事件卡片化（头像 + 角色 + 动作一张小卡）；chat 输入区聚焦描边；预览舞台去掉纯黑底改 surface-1 + 内阴影，frame 加 `--sh-2` |
| 文件抽屉 | `FilesDrawer.tsx` | 修复 `--dim`；选中态用 `--c-surface-3`；代码区加行高与等宽字体规范 |
| 管理页 | `Management.tsx` | 列表表格化（行高 56px）；危险操作统一 `.btn--danger` |

## 4. 项目缩略图方案

### 4.1 选型对比

| 方案 | 真实度 | 成本 | 依赖 | 结论 |
| --- | --- | --- | --- | --- |
| A 后端 headless 截图 | 高 | 高（需 chromium，k3s 单节点吃内存） | chromedp | 长期备选 |
| B 前端同源截屏上传 | 高 | 中 | html2canvas（~50KB） | **推荐** |
| C 纯 CSS 生成封面 | 低（非真实） | 零 | 无 | 立即兜底 |

### 4.2 推荐路径：C 兜底 + B 真实截图

**阶段 S3（0.5 天）— C 兜底**

卡片封面用「分类映射渐变 + 项目名首字 + 轻噪点」，零后端改动，立刻把列表从文字变成可视化。

**阶段 S4（2–3 天）— B 真实缩略图**

- 生成时机：项目在 workspace 打开且 `status === 'ready'` 后，用隐藏 iframe 加载 `/app/{id}/`，`load` 事件后延迟约 500ms，取 `iframe.contentDocument.body` 交给 html2canvas（同源可读，`/app` 与 SPA 同源），`canvas.toBlob('image/jpeg', 0.82)`
- 尺寸：显示 640×400，按 2x 取 1280×800，JPEG q80，约 40–80KB
- 后端接口：
  - `POST /api/sessions/{id}/thumbnail`，`Content-Type: image/jpeg`，body 为二进制，落 COS `sessions/{id}/thumbnail.jpg`
  - `GET /api/sessions/{id}/thumbnail`，返回图片或 302 到 COS 签名地址
- 列表响应：`SessionSummary` 增加 `thumbnailUrl?: string`
- 前端：卡片 `<img loading="lazy">`，`onError` 回退到 C 的 CSS 封面，永不出现破图

### 4.3 数据结构变更

- `internal/server/server.go:924` 的 session summary 结构新增 `ThumbnailURL string \`json:"thumbnailUrl,omitempty"\``
- 存储用 COS 对象键即可，可不改表结构；若要可查询，再加一列 `thumbnail_key`

## 5. 实施计划

| 阶段 | 内容 | 工作量 | 产出 |
| --- | --- | --- | --- |
| S1 | Token 化 + 修 `--dim` + 统一色板 + 基础组件类 | 1–2 天 | 一致性到位，后续改造有地基 |
| S2 | 核心页面重构（导航/认证/首页/列表/工作台/管理） | 2–3 天 | 6 个页面视觉升级 |
| S3 | CSS 封面兜底 | 0.5 天 | 项目列表可视化 |
| S4 | 真实缩略图链路（截屏 + 接口 + COS） | 2–3 天 | 作品墙效果 |

## 6. 验收标准

- 全站无未定义 CSS 变量（CI 增加一条静态校验）
- 除 token 定义处外，无硬编码颜色
- 字号只取自字阶，圆角只取自圆角 token
- 所有可点元素具备 hover / focus-visible / disabled 三态
- 缩略图失败有兜底，不出现破图
- 断点（820px / 560px）下视觉一致

## 7. 风险与取舍

- 深色改浅色是定位选择而非必须：建议保留深色，优先补层次、留白与动效
- html2canvas 对 `backdrop-filter`、web font、跨域图片支持有限；若生成效果差，退回方案 A（后端 chromium）
- 缩略图存储：单张 <100KB，1000 个项目约 100MB，COS 成本可忽略
- 改动集中在 CSS，TSX 仅少量类名替换，回归风险低，但仍需一轮全页面走查
