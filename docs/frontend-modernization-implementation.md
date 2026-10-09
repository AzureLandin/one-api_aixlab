# One API AixLab 前端现代化实施文档

> 面向对象：执行本次前端改造的 Agent / 开发者
> 参考计划：`docs/frontend-modernization-plan.md`
> 参考风格：[SHUAI API](https://api.shuaiapi.com/)
> 文档状态：实施规范，尚未代表代码已完成
> 日期：2026-10-09

## 1. 实施目标

在不迁移前端框架、不恢复额度/倍率/费用业务、不破坏旧路由和数据库兼容性的前提下，将 `web/default` 改造成一个深色、克制、现代的开发者控制台。

本次交付必须同时满足：

1. 视觉风格接近 SHUAI API 的深色开发者门户：固定轻量顶栏、深色画布、细边框、白/灰文字层级、低圆角、少阴影、内容优先。
2. Dashboard 和 Usage 真实消费现有 usage API，不自行推导费用、倍率或缓存命中。
3. 修复当前已知的用量入口死链、错误传播、路由权限和刷新深链接问题。
4. 业务页面保留现有 CRUD、权限、模型限制、网段限制、有效期和渠道配置能力。
5. 通过构建、单元/组件测试、角色回归、响应式检查和视觉检查后再交付。

## 2. 禁止事项

- 不要删除或改写后端数据库迁移器、Relay、选渠逻辑或额度兼容列。
- 不要恢复余额、充值、模型倍率、分组倍率、费用或货币展示。
- 不要从旧 `quota`、`used_quota` 或费用接口推导 Token、费用或缓存节省金额。
- 不要伪造未注册的后端 API；当前没有 `/api/usage/by-token` 和 self 排行接口。
- 不要静默把 `null`、缺字段、未上报缓存或异常缓存数据转成 `0`。
- 不要把新 UI 仅做成“换颜色”；必须完成布局、状态、错误、权限和响应式处理。
- 不要引入新的 UI 框架、状态管理库、图标库或网络字体依赖，除非单独获得批准。
- 不要批量重写与本任务无关的业务逻辑或格式化整个仓库。
- 不要修改 `web/berry`、`web/air`；本任务只针对 `web/default`。
- 不要将 API Key、密码、验证码或渠道密钥写入 localStorage、日志或截图。

## 3. 当前实现基线

### 3.1 前端技术栈

- React 18
- React Router 6
- Semantic UI React / Semantic UI CSS
- Recharts
- Axios
- react-i18next
- react-toastify
- `react-scripts build` 构建

前端工作目录：

```text
/home/azure/Projects/one-api_aixlab/web/default
```

### 3.2 当前关键入口

| 文件 | 当前作用 | 实施注意 |
| --- | --- | --- |
| `src/index.js` | 全局 Header、Container、Footer、Provider | 改为按布局承载公共页、鉴权页和控制台页 |
| `src/App.js` | 路由注册 | 增加 `/usage`，处理 `/console/usage` 兼容入口，统一路由元数据 |
| `src/index.css` | 全局样式、响应式覆盖 | 移除 body 顶部占位/隐藏滚动条，建立主题变量 |
| `src/components/Header.js` | 当前顶部导航和移动菜单 | 拆分为 Topbar/Sidebar，保留语言、用户和退出 |
| `src/components/PrivateRoute.js` | localStorage 登录检查 | 补充初始化态、角色守卫和目标地址保留 |
| `src/helpers/api.js` | Axios 实例和错误拦截 | 错误拦截后必须继续 reject，避免页面丢失错误上下文 |
| `src/pages/Dashboard/index.js` | Token/缓存 Dashboard | 抽取请求和指标组件，保持后端口径 |
| `src/pages/Dashboard/Dashboard.css` | Dashboard 及部分全局样式 | 页面专属化，不能继续被 Plaza/Setting 依赖 |
| `src/pages/Plaza/index.js` | 模型广场 | 使用新的 PageHeader、TableShell 和深色表格 |
| `src/pages/Setting/index.js` | 设置 Tab | 改为统一设置布局和 Tab 样式 |
| `src/components/UsersTable.js` | 用户管理 | 修复 usage 深链接，避免 N+1 请求 |
| `src/components/TokensTable.js` | 令牌列表 | 保留令牌权限/有效期，敏感 Key 默认遮盖 |
| `src/components/ChannelsTable.js` | 渠道列表 | 保持测试、编辑、禁用和删除能力 |
| `src/components/LogsTable.js` | 日志列表 | 保持历史日志语义，不冒充 usage 明细 |
| `src/locales/zh-CN.json`、`en.json` | 国际化 | 新增布局、状态、缓存解释和错误文案 |

### 3.3 当前后端可用接口

当前 `router/api.go` 已注册：

```text
GET /api/usage/summary
GET /api/usage/trend
GET /api/usage/by-user
GET /api/usage/by-model
GET /api/usage/by-channel
GET /api/usage/self/summary
GET /api/usage/self/trend
```

管理员接口使用 `AdminAuth`；self 接口使用 `UserAuth`。响应格式为：

```json
{
  "success": true,
  "message": "",
  "data": {}
}
```

汇总数据至少包含：

```text
input_tokens
output_tokens
total_tokens
request_count
cache_read_input_tokens
cache_write_input_tokens
cache_hit_rate
call_hit_rate
cache_coverage
generated_calls
error_calls
not_reported_cache_calls
invalid_cache_calls
```

`cache_hit_rate`、`call_hit_rate`、`cache_coverage` 为可空指标：`null` 表示不可计算，必须展示为“—”，不能展示为 `0%`。

## 4. 交付顺序

不要把所有 UI 变化堆在一个提交中。建议按以下顺序提交：

```text
F0 fix: 基线、路由、错误和权限正确性
F1 feat: 主题变量和 Console/Public/Auth 布局
F2 feat: Dashboard 与 Usage 统计体验
F3 refactor: 用户/令牌/渠道/日志/模型广场页面统一
F4 refactor: 设置页、登录注册页、i18n 和无障碍
F5 test: 视觉回归、角色回归和发布检查
```

每个阶段结束后必须运行：

```bash
cd /home/azure/Projects/one-api_aixlab/web/default
CI=true npm test -- --watchAll=false
npm run build
```

`npm run build` 会把产物移动到 `web/build/default`。运行前检查该目录是否存在用户需要保留的文件，运行后确认工作区变更只包含预期文件。

## 5. F0：正确性基线与请求层

### 5.1 基线记录

执行前记录：

- `git status --short`。
- `npm --version`、`node --version`。
- `CI=true npm test -- --watchAll=false` 的存量结果。
- `npm run build` 的存量结果和 warning 数量。
- 当前页面在 390、768、1280、1440 px 的截图或人工记录。
- 访客、普通用户、管理员和超级管理员的可访问路由。

若依赖未安装，先记录为环境前置条件；不要为了构建临时升级 package-lock 或引入依赖。

### 5.2 API 错误传播

当前 `src/helpers/api.js` 的 Axios response interceptor 在 `showError(error)` 后必须 `return Promise.reject(error)`。同时约定：

```js
API.interceptors.response.use(
  (response) => response,
  (error) => {
    showError(error);
    return Promise.reject(error);
  }
);
```

页面层不应再次对同一个错误自动 Toast。推荐规则：

- API 层：只处理全局网络错误或未被页面消费的认证错误。
- 页面层：处理该页面的业务提示、空状态和重试按钮。
- `401`：清理失效会话并导航登录，保留 `return_to`。
- `403`：显示无权限状态，不当作网络错误。
- HTTP 200 + `success: false`：页面检查 `success` 并展示 `message`。

如果现有全局 Toast 无法区分 owner，先添加错误标记或页面消费约定，不要简单地关闭所有全局错误提示。

### 5.3 路由正确性

在 `App.js` 中：

1. 新增受保护的 `/usage` 页面。
2. 将 `/console/usage` 作为兼容入口，读取 `location.search` 后导航到 `/usage`，或渲染同一页面并保持查询参数。
3. 确保 UsersTable 当前的 `/console/usage?user_id=...` 链接可用。
4. 管理员筛选 `user_id` 时允许使用；普通用户进入 `/usage?user_id=other` 时忽略该参数或显示本人范围，不能调用管理员 API。
5. 编辑渠道、编辑用户、编辑令牌等受保护路由统一包裹 `PrivateRoute`，并在页面内部保留后端权限错误处理。
6. 未登录访问受保护路由时回到登录页，登录成功后返回原目标路径。

建议新增 `src/routes/routeMeta.js`，集中维护标题、导航分组、权限要求和面包屑；不要在每个页面重复定义导航名称。

### 5.4 请求服务和查询参数

新增 `src/services/usage.js` 或等价模块，职责仅限：

- 生成管理员/self 对应 endpoint。
- 生成和解析时间范围、user_id、token_id、channel_id、model_name。
- 统一检查 `{success,message,data}`。
- 不计算命中率，不改变后端数字。

建议接口：

```js
export function getUsageSummary(filters, scope) {}
export function getUsageTrend(filters, scope) {}
export function getUsageRanking(dimension, filters) {}
export function serializeUsageFilters(filters) {}
export function parseUsageFilters(searchParams) {}
```

筛选变化后取消旧请求，或使用递增 request sequence；旧请求返回不能覆盖新筛选结果。刷新时保留上次成功数据，并标记 `isRefreshing`。

## 6. F1：主题和布局

### 6.1 主题变量

在 `src/styles/theme.css` 或等价文件定义命名空间变量，避免继续使用散落的行内颜色：

```css
:root,
[data-theme='dark'] {
  --app-bg: #1c1c1c;
  --app-surface: #232323;
  --app-surface-raised: #2a2a2a;
  --app-text: #f5f5f5;
  --app-text-muted: #afafaf;
  --app-text-subtle: #808080;
  --app-border: rgba(255, 255, 255, 0.14);
  --app-border-strong: rgba(255, 255, 255, 0.22);
  --app-accent: #6ea8fe;
  --app-success: #63d391;
  --app-warning: #f2c66d;
  --app-danger: #ff8585;
  --app-radius-sm: 4px;
  --app-radius-md: 6px;
  --app-radius-lg: 8px;
  --app-sidebar-width: 232px;
  --app-topbar-height: 64px;
}
```

变量值是起始值，必须经过对比度检查。不要将颜色直接复制到几十个组件中。图表颜色要兼顾暗背景和色盲可辨识度。

### 6.2 布局组件

建议新增：

```text
src/layouts/ConsoleLayout.js
src/layouts/PublicLayout.js
src/layouts/AuthLayout.js
src/components/layout/Sidebar.js
src/components/layout/Topbar.js
src/components/layout/PageHeader.js
src/components/layout/Breadcrumbs.js
```

职责：

- `ConsoleLayout`：固定 Topbar、桌面 Sidebar、移动抽屉、主滚动区域。
- `PublicLayout`：首页/关于等公共页面只保留轻量顶栏和页脚。
- `AuthLayout`：登录/注册/重置密码使用窄内容列，不显示管理侧栏。
- `Sidebar`：按角色过滤导航，当前路由高亮，移动端点击后关闭。
- `Topbar`：品牌、面包屑、语言、主题预留、用户菜单和退出。
- `PageHeader`：标题、描述、筛选摘要、主操作。

布局要求：

- 不再通过 `body { padding-top: 55px; }` 假定 Header 高度。
- 不使用 Dashboard 的负 margin 修正位置。
- 主内容采用 `min-height: calc(100dvh - var(--app-topbar-height))`。
- `body` 恢复可见滚动条；隐藏滚动条不得作为视觉方案。
- 移动端 Sidebar 变为带遮罩的抽屉，Esc 可关闭，焦点返回菜单按钮。
- 表格只在明确的 `.table-scroll` 容器内横向滚动，页面本身不得产生意外横向溢出。

### 6.3 Semantic UI 适配

继续使用 Semantic UI 组件，但通过包装类覆盖外观：

- `ui card` 不再默认白底；使用 `.app-panel`。
- `ui table` 统一暗色背景、细边框、hover 行和 sticky header（仅在容器内）。
- `ui button` 区分 primary、secondary、ghost、danger；避免所有按钮都用同一视觉等级。
- `ui input`、`ui dropdown`、`ui form` 统一焦点边框和错误状态。
- `ui message` 映射为 info/success/warning/error，颜色之外必须有标题或图标。
- 不新增无命名空间的 `.ui.card`、`.ui.header` 全局覆盖。

## 7. F2：统计页面实施

### 7.1 共享组件

建议新增：

```text
src/components/analytics/MetricCard.js
src/components/analytics/RateMetric.js
src/components/analytics/ChartPanel.js
src/components/analytics/FilterBar.js
src/components/analytics/DataQualityNotice.js
src/components/analytics/UsageTable.js
src/components/states/PageState.js
```

组件约定：

- MetricCard 接收 label、value、unit、description、status，不读取 API。
- RateMetric 接收 nullable rate；`null` 显示“—”和不可计算说明。
- ChartPanel 接收 data、loading、error、empty 和 render function。
- FilterBar 只管理控件和 URL 同步，不决定权限。
- PageState 统一 loading、empty、error、forbidden；error 必须提供重试。
- DataQualityNotice 展示 `not_reported_cache_calls`、`invalid_cache_calls`，不改变统计值。

### 7.2 Dashboard 改造步骤

在 `src/pages/Dashboard/index.js`：

1. 迁移请求到 `services/usage.js`。
2. 把当前内联卡片、图表、提示拆到共享组件。
3. 保留当前 7/30/90 天/全部选项。
4. 顶部增加 PageHeader、范围摘要、最后成功更新时间和刷新按钮。
5. 首屏顺序为 Token 概览、缓存指标、数据质量提示、趋势、排行。
6. 管理员展示 by-model/by-user/by-channel；普通用户只请求 self summary/trend。
7. 输入 Token、输出 Token、总 Token 和缓存读取分系列展示；缓存读取属于输入的一部分，图表旁写清楚。
8. `invalid_cache_calls > 0` 显示警告，不裁剪异常率。
9. 初次加载显示骨架或 PageState；刷新保留旧数据并显示刷新状态。
10. 空数据不能渲染空图表坐标轴假装有数据。

不要在 Dashboard 内实现每个排行项单独请求 summary；若需要排行百分比，使用后端返回的聚合字段。

### 7.3 Usage 页面

新增 `src/pages/Usage/index.js`，优先实现聚合分析：

- FilterBar：时间范围、用户、令牌、渠道、模型。
- Summary：复用 Dashboard MetricCard。
- Trend：复用 ChartPanel。
- Ranking：管理员显示用户/模型/渠道 Tab 或三块面板。
- URL：筛选可复制、刷新和返回后恢复。
- 普通用户：隐藏管理员排行，不调用管理员 endpoint。
- 第一版不显示请求级明细、缓存逐请求明细或导出按钮，除非后端已提供稳定接口。

## 8. F3：业务页面实施

### 8.1 DataTableShell

新增 `src/components/data/DataTableShell.js`，统一处理：

```text
loading
empty
error + retry
forbidden
table-scroll
pagination
selection/action toolbar
```

业务表只负责列和单元格，不再各自实现重复 loading/empty/error。保持现有分页参数和 API，不改变后端排序字段。

### 8.2 用户表

文件：`src/components/UsersTable.js`

- 保留用户名、邮箱、角色、状态、分组和历史请求数语义。
- 修复 usage 链接为 `/usage?user_id=<id>` 或通过兼容路由正确跳转。
- 不逐行调用 usage summary，避免 N+1。
- 管理员点击用量入口时保留用户筛选；普通用户不出现管理操作。
- 删除/禁用/角色调整使用 ConfirmDialog；确认文本包含用户身份和影响。
- 长邮箱、显示名和分组在窄屏截断并提供 title/可访问名称。

### 8.3 令牌表和编辑页

文件：`src/components/TokensTable.js`、`src/pages/Token/EditToken.js`

- 删除额度、剩余额度、无限额度 UI，不新增费用字段。
- 保留状态、名称、创建时间、过期时间、模型限制、网段限制。
- Key 默认遮盖；复制按钮明确反馈；不把明文 Key 放 localStorage。
- 删除为不可逆或软删除动作时，确认框明确说明；禁用单独确认。
- 表单提交期间禁用重复提交，失败保留输入。
- 如果后端没有令牌 usage 聚合接口，不显示伪造的 Token 统计列；可提供跳转到全局 Usage 的入口并带 token_id，前提是后端支持该筛选。

### 8.4 渠道表和编辑页

文件：`src/components/ChannelsTable.js`、`src/pages/Channel/EditChannel.js`

- 保留渠道测试、编辑、删除禁用渠道和状态变化。
- 密钥输入使用 password 类控件或遮盖显示；编辑已有渠道时不回显不必要的明文。
- 渠道编辑按“基础、连接、模型/分组、高级配置”分区。
- `config` JSON 校验错误显示具体字段；不要只显示“请求失败”。
- 模型长列表使用折叠/搜索；不在移动端强行撑宽。
- 批量操作的 loading 只表示真实请求状态，不伪造进度百分比。

### 8.5 日志表

文件：`src/components/LogsTable.js`、`src/pages/Log/index.js`

- 保留历史日志查询接口和分页。
- 重点列：时间、用户/令牌、模型、渠道、输入/输出 Token、流式、请求 ID、状态。
- 旧日志 `quota` 字段不重新解释为费用或新 usage；如仍存在，隐藏或标注 legacy。
- 长内容按需展开；请求 ID提供复制。
- 没有缓存字段时显示“未报告”，不显示“未命中”。
- 筛选条件写入 URL 时，刷新/返回可恢复。

### 8.6 模型广场、设置和公共页

- `Plaza`：保留搜索、模型复制、当前分组和渠道展开；移除价格/倍率；改为暗色表格和可折叠面板。
- `Setting`：保留当前 root 权限和选项白名单；使用统一设置导航/表单区块；多项保存失败显示具体项。
- `Home` / `About`：使用轻量公共顶栏，正文采用参考站点式内容分区和分隔线，不强行套用管理侧栏。
- `LoginForm`、`RegisterForm`、`PasswordResetForm`：使用 AuthLayout，保留邮箱验证码、倒计时、字段校验和失败重试。
- Footer：深色背景中使用低对比度辅助文字，但链接仍需达到可读对比度。

## 9. F4：权限、错误和状态细节

### 9.1 状态矩阵

每个主要页面至少覆盖：

| 状态 | 展示 | 操作 |
| --- | --- | --- |
| 首次加载 | 骨架或 spinner，不显示空表误导 | 无或取消 |
| 刷新中 | 保留旧数据，显示刷新中 | 可再次刷新需防抖 |
| 空数据 | 说明没有数据及筛选范围 | 清除筛选/刷新 |
| 业务失败 | `message` + 重试 | 重试 |
| 网络失败 | 网络提示 + 重试 | 重试 |
| 401 | 会话过期提示/跳登录 | 登录 |
| 403 | 无权限说明 | 返回可访问页 |
| 部分区块失败 | 仅失败区块显示错误 | 单块重试 |
| 数据异常 | 说明 invalid/未上报 | 查看口径说明 |

### 9.2 角色矩阵

实施中使用真实账号或测试账号验证：

| 页面/操作 | 访客 | 普通用户 | 管理员 | 超级管理员 |
| --- | --- | --- | --- | --- |
| 公共首页/关于/登录 | 允许 | 允许 | 允许 | 允许 |
| 本人 Dashboard/Usage | 禁止 | 允许 | 允许 | 允许 |
| 全局用户/模型/渠道排行 | 禁止 | 禁止 | 允许 | 允许 |
| 令牌管理 | 禁止 | 本人 | 按现有后端权限 | 按现有后端权限 |
| 用户/渠道管理 | 禁止 | 禁止 | 按现有后端权限 | 允许 |
| 系统设置 | 禁止 | 禁止 | 禁止或按现有规则 | 按现有 root 规则 |

前端隐藏按钮只是体验层；后端权限响应必须作为最终结果处理。

## 10. F5：国际化与无障碍

### 10.1 i18n

- 所有新增可见文案进入 `zh-CN.json` 和 `en.json`。
- 不在 JSX 中新增业务中文硬编码，错误字段允许展示后端 message，但应有通用 fallback。
- 统计术语统一：输入 Token、输出 Token、总 Token、缓存读取、缓存写入、命中率、调用命中率、覆盖率、未上报、数据异常。
- 不把 `null` 翻译成“0”；“—”的解释文本必须可翻译。
- 导航和 PageHeader 使用同一组 key，避免标题和菜单不一致。

### 10.2 无障碍

- 图标按钮添加 `aria-label`。
- 抽屉和弹窗支持 Esc，关闭后焦点返回触发按钮。
- 输入框 label 与控件关联，错误文本通过 `aria-describedby` 关联。
- 状态不能只靠颜色区分，同时显示文字/图标。
- 图表下方提供摘要或表格，不能只依赖鼠标 Tooltip。
- 390 px 下不依赖 hover 显示操作。
- 检查 `prefers-reduced-motion`，避免无必要动画。

## 11. 测试实施

### 11.1 代码级测试

至少添加/更新以下测试：

- `serializeUsageFilters` / `parseUsageFilters`：空值、中文模型名、多个 ID、时间范围。
- nullable rate formatter：`null`、`0`、`1`、异常值、缺字段。
- API service：success=false、401、403、网络错误。
- PrivateRoute：未登录、初始化中、普通用户访问管理员路径。
- Dashboard：管理员/self endpoint 分流，未上报和 invalid 提示，刷新不清空旧数据。
- Usage：URL 深链接、清除筛选、普通用户不请求管理员接口。
- 表单：重复提交锁定、失败保留输入、危险操作取消不发请求。

若项目当前没有稳定的 React 测试基建，先使用现有 Jest/react-scripts 能力，不为本轮引入大规模测试框架。

### 11.2 构建和静态检查

```bash
cd /home/azure/Projects/one-api_aixlab/web/default
CI=true npm test -- --watchAll=false
npm run build
```

检查：

- 构建无新增 warning。
- 无未使用 import、无明显 React key 警告。
- 无 `undefined`、`NaN`、`Infinity` 进入可见统计。
- 无 quota/ratio/cost 旧文案或无效字段依赖。
- `git diff --check` 通过。

### 11.3 浏览器手工测试

尺寸：390、768、1280、1440 px。

流程：

1. 访客打开首页、登录、注册、重置密码。
2. 普通用户打开 Dashboard、Usage、Token、Log、Plaza、Setting。
3. 管理员筛选用户并从 UsersTable 进入 Usage，刷新后筛选仍保留。
4. 管理员打开用户、渠道和日志管理，执行编辑/禁用/测试/取消确认。
5. 断开 API 或返回 401/403，验证状态和恢复路径。
6. 用无数据、长模型名、中文/英文和未上报缓存数据验证布局。
7. 键盘完成导航、打开/关闭抽屉、填写表单和取消弹窗。

记录每个尺寸、角色、浏览器、页面和结果；失败项带截图和复现步骤。

### 11.4 视觉验收

重点检查：

- 页面背景不是默认白色 Semantic UI 卡片堆叠。
- 顶栏轻量、固定、导航层级清晰。
- 页面主要依靠边框、留白和文字层级，不使用重阴影/大渐变。
- 主按钮与次按钮权重清楚，危险操作不会伪装成普通按钮。
- 表格在暗色背景下仍清晰，数字对齐，状态不是只靠颜色。
- Dashboard 首屏能读出 Token 总量和三项缓存指标。
- 公共页、登录页和控制台页视觉统一但信息密度不同。

不要把参考网站的品牌名、运营文案、邀请码、价格政策或第三方报告直接复制到 One API。

## 12. 发布与回滚

### 12.1 发布前

- 确认后端 usage API 与前端字段契约一致。
- 确认数据库迁移后的测试环境可登录、可鉴权、可读取历史日志和新 usage。
- 备份当前 `web/build/default` 或记录其发布版本。
- 构建新产物并确认静态资源 hash 与 HTML 匹配。
- 完成角色、尺寸、浏览器和关键操作矩阵。

### 12.2 回滚

- 纯 CSS/UI 问题优先回滚前端静态产物，不回滚数据库。
- 若新增只读接口与前端强绑定，前端和后端按兼容版本一起回滚。
- 保留 `usage_records`、迁移版本和用户数据，不执行删表/清空配置。
- 回滚后重新验证旧路由、登录、令牌、渠道、历史日志和 Dashboard。

## 13. 最终验收清单

- [ ] 风格明确参考 SHUAI API，但没有复制其品牌、文案和业务政策。
- [ ] 默认深色主题、固定轻量顶栏、细边框、低圆角和少阴影生效。
- [ ] 公共页、鉴权页、控制台页有独立布局。
- [ ] `/usage` 可直达、刷新、返回，并支持合法筛选。
- [ ] `/console/usage?user_id=...` 不再 404。
- [ ] 普通用户不会通过 URL 或前端请求看到他人统计。
- [ ] API 错误会继续 reject，页面能区分 401、403、业务失败和网络失败。
- [ ] Dashboard/Usage 不从旧额度、倍率或费用字段推导数据。
- [ ] null、0、未上报和 invalid 缓存状态展示正确。
- [ ] Token、渠道密钥和密码不会进入 localStorage、日志或截图。
- [ ] 用户、令牌、渠道、日志、模型广场、设置和鉴权流程无功能回归。
- [ ] 390/768/1280/1440 px 布局通过，页面无意外横向溢出。
- [ ] 键盘、焦点、弹窗、抽屉和图表替代摘要通过检查。
- [ ] 中英文文案完整，无新增硬编码业务文案。
- [ ] `CI=true npm test -- --watchAll=false` 通过或已记录存量失败。
- [ ] `npm run build` 通过，且无新增 warning。
- [ ] 发布和回滚记录齐全。
