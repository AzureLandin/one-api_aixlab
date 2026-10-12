# One API 现代化改造实施文档

> 面向对象：执行改造的 Agent / 开发者  
> 文档状态：实施规范  
> 编制日期：2026-10-07  
> 关联计划：[one-api-modernization-plan.md](./one-api-modernization-plan.md)

## 1. 执行约束

1. 本文档中的顺序优先于“边改边重构”的做法。
2. 每个阶段完成后必须运行对应测试，并检查 `git diff`。
3. 不要在一次提交中同时改变数据库、协议转换和大范围 UI；按阶段提交，便于回滚。
4. 不要删除旧数据库列，除非已经完成数据迁移、灰度观察和明确的清理审批。
5. 不要将历史 `used_quota` 直接换算为 Token。
6. 不要把未报告的缓存字段填成 `0`。
7. 不要为了“兼容”而静默丢弃工具、图片、缓存或流式事件。**唯一记在案的例外**：目标协议无法表达的托管工具（hosted tool）在默认的 `tool_loss_policy: allow` 下丢弃，但每一次丢弃都写一条具名诊断（`logHostedLosses`），`safe`/`strict` 下改为具名 400（`hosted_tool_loss`）；响应阶段与流式阶段在任何策略下都不拒绝。见 §6.7。
8. 代码注释、错误信息和新增 API 文档优先使用项目已有语言风格；数据字段名使用稳定的英文命名。
9. 数据库迁移必须先备份并在副本上验证；不允许以“删库重建”代替迁移。

## 2. 当前代码入口与责任边界

实施前先阅读这些文件，并以当前分支实际代码为准：

| 领域 | 重点位置 | 任务 |
| --- | --- | --- |
| 启动 | `main.go` | 确认数据库初始化、路由注册和启动顺序 |
| 管理 API | `router/api.go` | 增加统计接口，清理充值/倍率相关接口暴露 |
| Relay API | `router/relay.go` | 增加 `/v1/responses`、`/v1/messages` |
| 鉴权 | `middleware/auth.go` | 确认 Bearer、`x-api-key` 和用户/令牌解析边界 |
| 用户/令牌 | `model/user.go`、`model/token.go`、`controller/user.go` | 保留身份、权限、有效期，移除额度语义 |
| 日志 | `model/log.go`、`controller/log.go` | 兼容旧日志，新增 usage 查询 |
| Relay helper | `relay/controller/helper.go` | 删除预扣/退款/额度判定的调用点 |
| 文本/图片/音频 | `relay/controller/text.go`、`image.go`、`audio.go` | 确保所有生成路径都调用统一 recorder |
| 结算 | `relay/billing/billing.go` | 逐步替换为 usage 记录，最终停用 |
| 通用 usage | `relay/model/misc.go` | 扩展标准 Usage 和缓存明细 |
| OpenAI 适配 | `relay/adaptor/openai/main.go` | 解析 Chat/Responses usage 和流式收尾 |
| Anthropic 适配 | `relay/adaptor/anthropic/model.go`、`main.go` | 解析 Messages、SSE 和缓存字段 |
| 协议入口 | `relay/protocol/`（`messages.go`、`responses.go`、`stream_*.go`） | 三种客户端协议的解析与渲染；只依赖 `relay/model` + `relay/sse` |
| 协议路由 | `relay/controller/protocol.go`、`protocol_passthrough.go`、`protocol_codec.go` | 能力检查 → 直通 → codec 三路分支；`protocolStream` 的 `Hosted`/`LossOf` |
| 托管工具 | `relay/model/hosted.go`（中立类型 + kind 表 + 策略，无 adaptor 依赖）；`relay/controller/hosted_attach.go`、`hosted_bridge.go`（请求挂载、流式桥） | 见 §6.7 |
| 前端 Dashboard | `web/default/src/pages/Dashboard/index.js` | 新增 Token/缓存指标和趋势图 |
| 前端基础 | `web/default/src/index.css`、`Header.js` | 全局布局、状态和响应式样式 |
| 前端业务页 | `OperationSetting.js`、`UsersTable.js`、`TokensTable.js`、`LogsTable.js` | 删除额度/倍率文案，接入新字段 |

如果实际分支中的路径或符号与表格不同，以当前代码为准，并在实现记录中补充映射。

### 2.1 当前数据库迁移基线

当前仓库的数据库启动流程位于 `model/main.go`：

- 根据 `SQL_DSN` 选择 SQLite、MySQL 或 PostgreSQL。
- 主库通过 GORM `AutoMigrate` 初始化 `channels`、`tokens`、`users`、`options`、`abilities` 和 `logs`。
- 如果配置 `LOG_SQL_DSN`，日志表会在独立数据库中通过 `migrateLOGDB()` 初始化。
- `bin/migration_lab.sql` 是面向已有部署的人工一次性清理脚本，不能作为新改造的唯一迁移机制。

因此，本次改造必须新增显式版本化迁移层。`AutoMigrate` 只负责安全的基础建表/加列，不能承担以下操作：

- 旧列到新列的数据转换。
- 额度历史与 Token 统计口径的切换。
- 跨 SQLite/MySQL/PostgreSQL 的复杂 DDL。
- 主库和独立日志库的迁移版本管理。
- 迁移前检查、迁移后校验和失败恢复。

## 3. 阶段 0：建立基线

### 3.1 代码盘点

使用全仓搜索列出以下符号和文案的所有读写位置：

```text
Quota
quota
UsedQuota
used_quota
PreConsume
PostConsume
ReturnPreConsumed
Ratio
model_ratio
group_ratio
completion_ratio
充值
余额
倍率
费用
```

逐个标记为：

- 必须删除：新请求路径中的额度放行、预扣、退款和倍率计费。
- 兼容保留：旧数据库字段、历史日志读取、旧客户端暂时依赖的响应字段。
- 仍然需要：与权限、限流、有效期无关的普通配置。

### 3.2 基线测试

在改代码前记录：

- `go test ./...` 结果。
- 前端现有 lint/build 命令及结果。
- 现有 `/v1/chat/completions` 非流式/流式行为。
- 现有 OpenAI 和 Anthropic 渠道的 usage 结构。
- 数据库类型和迁移机制。

如果仓库当前基线测试本身失败，先记录失败原因，不要把与本改造无关的问题混入修复提交。

### 3.3 迁移兼容性基线

迁移目标不是把旧库“改造成一张全新的表”，而是在保留旧数据的基础上增加新能力。必须遵循以下策略：

1. **先增量、后清理**：首次升级只新增 `usage_records`、迁移版本记录、必要索引和兼容字段；不删除 `users.quota`、`users.used_quota`、`tokens.remain_quota`、`tokens.used_quota`、`channels.used_quota` 或日志中的历史 `quota`。
2. **不做伪造回填**：旧 `used_quota` 没有可靠倍率和 Token 口径，不能回填为 `usage_records.input_tokens/output_tokens`。
3. **保留历史可读性**：原有 `logs` 表和历史日志继续可查，新页面将其标记为 legacy 或与新 usage 分开展示。
4. **新旧版本可回滚**：在旧列仍存在、旧表结构仍可读取的阶段，允许回滚到上一版本应用。
5. **独立日志库同等处理**：若 `LOG_SQL_DSN` 非空，主库迁移成功不代表升级完成，必须单独迁移并校验日志库。
6. **兼容旧脚本但不重复执行破坏性操作**：保留 `bin/migration_lab.sql` 的历史用途；新迁移必须有自己的版本标记和幂等判断，不能再次无条件 `DROP COLUMN`。

### 3.4 迁移版本与工具

实现时新增一个显式迁移包或迁移目录，例如：

```text
model/migration/
  runner.go
  versions.go
  v001_add_usage_records.go
  v002_add_usage_indexes.go
```

实际目录可按仓库风格调整，但必须具备：

- `schema_migrations`（或等价表），记录版本、执行时间和校验信息。
- `Up`/`Down` 或等价的前进/回滚定义。
- 按数据库方言生成 DDL，不能把 SQLite 语句直接用于 MySQL/PostgreSQL。
- 每个版本可安全重试；已完成版本不会重复执行。
- 迁移前打印数据库类型、当前版本和目标版本。
- 迁移后运行数据校验并输出结果。

如果现有 GORM 初始化顺序必须保留，则在 `AutoMigrate` 前后接入版本化迁移，但要保证：

1. 新表和字段由迁移版本创建，避免 GORM 自动创建出与目标不一致的类型。
2. `AutoMigrate` 只作为兼容旧部署的兜底，不得偷偷删除列。
3. 主库和日志库使用不同连接分别执行自己的迁移集合。

## 4. 阶段 1：实现 Usage 事实层

### 4.1 统一结构

在 `relay/model/misc.go` 或独立 usage 包中定义归一化结构。字段语义建议如下：

```go
type Usage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`

    PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
    InputTokensDetails      *InputTokensDetails      `json:"input_tokens_details,omitempty"`
    CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`

    CacheReadInputTokens  *int64 `json:"cache_read_input_tokens,omitempty"`
    CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
    Source                string `json:"usage_source"`
    CacheStatus           string `json:"cache_usage_status"`
}
```

实际类型可以按项目风格调整，但必须保留“指针/可选值”语义，不能用普通 `int` 把未报告和零混淆。

校验规则：

- `input_tokens >= 0`、`output_tokens >= 0`。
- 若 `total_tokens` 被报告，则通常应等于输入加输出；不一致时保存原始值并标记异常，不要静默改写。
- 缓存读取和写入不能为负数。
- 缓存读取 + 缓存写入大于输入 Token 时标记 `invalid`，不要强行裁剪。
- `reported`、`estimated`、`legacy` 只能由对应路径设置。

### 4.2 usage_records 表

迁移应创建独立表。SQL 方言以项目现有数据库层为准，不要直接假设只支持 MySQL。

逻辑字段：

```text
id
request_id                 unique/index
user_id                    index
token_id                   nullable/index
channel_id                 index
model_name                 index
input_tokens
output_tokens
total_tokens
cache_read_input_tokens    nullable
cache_write_input_tokens   nullable
usage_source
cache_usage_status
stream
status
created_at                 index
```

建议索引：

- `(user_id, created_at)`
- `(token_id, created_at)`
- `(model_name, created_at)`
- `(channel_id, created_at)`
- `request_id` 唯一索引

如果现有 ORM 不支持部分唯一索引，则由应用层加唯一约束检查，并在数据库层保留普通索引。

### 4.2.1 旧库迁移映射

旧库到新库的处理规则如下：

| 旧数据 | 新版本处理 |
| --- | --- |
| `users` | 原表保留，用户主键、账号、角色、状态、分组和访问令牌不变 |
| `users.quota` | 保留为 legacy，不参与放行或新统计 |
| `users.used_quota` | 保留为 legacy，不换算为 Token |
| `users.request_count` | 可作为历史请求数展示，但不伪造为 `usage_records` 明细 |
| `tokens` | 原表保留，令牌 Key、用户关联、模型限制、网段和有效期不变 |
| `tokens.remain_quota/used_quota` | 保留为 legacy，不再更新 |
| `channels` | 原表保留，Key、BaseURL、模型映射、分组和配置不变 |
| `channels.used_quota` | 保留为 legacy，不再更新 |
| `abilities` | 原有模型/渠道权限不变 |
| `options` | 保留未知配置；新代码忽略已废弃的额度/倍率 key |
| `logs` | 原历史记录不删除；新 usage 记录写入新表 |
| `redemptions` | 如存在，先保留或按现有部署策略归档；不能因新版本启动失败而无备份删除 |

迁移校验至少比较迁移前后的：

- 用户、令牌、渠道、能力、选项和日志行数。
- 每张表的最小/最大主键。
- 用户与令牌、渠道与能力的关联数量。
- 非空 Key、用户名、模型映射、渠道配置和令牌有效期。
- 日志最大时间、最小时间和历史日志总量。

### 4.2.2 迁移后新统计的起始点

定义一次明确的 cutover 时间 `usage_cutover_at`：

- cutover 之前的历史额度和旧日志不进入新的 Token 聚合。
- cutover 之后的生成请求必须写入 `usage_records`。
- 如果旧日志中本来就有可靠的 `prompt_tokens`/`completion_tokens`，可以在独立、可审计的 backfill 命令中导入；导入记录必须标记 `usage_source=legacy`，并且默认不纳入实时 Dashboard。
- 默认方案是不回填，避免把旧日志中的不完整或估算字段误当成统一事实。

### 4.3 UsageRecorder 接口

建议定义类似接口：

```go
type UsageRecordInput struct {
    RequestID string
    UserID    int
    TokenID   int
    ChannelID int
    ModelName string
    Usage     Usage
    Stream    bool
    Status    string
    CreatedAt time.Time
}

type UsageRecorder interface {
    Record(ctx context.Context, input UsageRecordInput) error
    RecordOnce(ctx context.Context, input UsageRecordInput) error
}
```

实施要求：

1. `RecordOnce` 对同一 `RequestID` 幂等。
2. 记录失败必须写结构化错误日志，但不能覆盖上游响应错误。
3. recorder 不负责额度，不调用 `PostConsumeTokenQuota`。
4. recorder 不根据倍率计算任何金额。
5. 成功响应、上游错误响应和客户端中断要有明确状态。
6. 流式请求只能在收到最终 usage 或完成估算后收尾一次。

### 4.4 聚合查询

新增管理端和用户端所需的查询服务，至少支持：

```text
GET /api/usage/summary
GET /api/usage/trend
GET /api/usage/by-user
GET /api/usage/by-model
GET /api/usage/by-channel
```

具体权限建议：

- 管理员：全局、用户、令牌、模型、渠道和时间维度。
- 普通用户：只允许自己的数据。

返回结构至少包括：

```json
{
  "input_tokens": 0,
  "output_tokens": 0,
  "total_tokens": 0,
  "request_count": 0,
  "cache_read_input_tokens": 0,
  "cache_write_input_tokens": 0,
  "cache_hit_rate": null,
  "call_hit_rate": null,
  "cache_coverage": 0
}
```

命中率应由聚合后的分子/分母计算，禁止先计算每次调用的百分比再做简单平均。

## 5. 阶段 2：移除额度与倍率

### 5.1 后端改造顺序

按下面顺序修改，避免先删字段导致请求链路失效：

1. 在所有 relay 路径接入 `UsageRecorder`。
2. 让新统计在旧额度逻辑旁边运行，验证记录数量和 request ID。
3. 移除预扣、额度耗尽检查和退款。
4. 将原 `billing.PostConsumeQuota` 的统计职责迁移到 recorder。
5. 删除模型倍率、分组倍率和输出倍率参与的计算。
6. 停止更新旧额度字段；必要时保留读取以兼容旧管理页面。
7. 删除充值接口或改为明确的 deprecated/不可用响应。
8. 更新 API 和前端，不再返回费用和倍率字段。

### 5.2 禁止保留的行为

- 请求前检查用户余额。
- 请求前预扣令牌额度。
- 请求失败退款。
- 用模型倍率/分组倍率计算日志的 `Quota`。
- 因额度不足拒绝合法请求。
- 把 Token 统计塞进旧 `PromptTokens` 但继续以额度解释。

### 5.3 兼容策略

旧 `users.quota`、`users.used_quota`、`tokens.used_quota` 或相关列如仍被历史代码读取，可暂时保留，但必须：

- 在代码注释中标记为 legacy。
- 不再作为放行条件。
- 不再被新请求写入。
- 不出现在新前端页面。
- 迁移文档说明后续清理条件。

旧日志允许继续展示历史额度字段，但新日志/usage 页面必须使用 `usage_records`。

## 6. 阶段 3：多 API 格式路由

### 6.1 分层结构

建议把 relay 处理拆成以下层次：

```text
HTTP middleware
  -> client protocol parser
  -> normalized request
  -> model/channel distributor
  -> upstream adaptor
  -> normalized response/event
  -> client protocol renderer
  -> usage recorder
```

不要让 OpenAI 或 Anthropic 的 JSON 结构直接渗透到所有 controller。

### 6.2 规范化请求

至少统一以下概念：

```text
model
messages / input items
system instructions
text parts
image parts
tool definitions
tool calls
tool results
stream
temperature / max tokens 等通用参数
request id
```

保留协议特有字段用于回写，但不要在转换时丢失调用 ID、工具名称和参数字符串。

### 6.3 Chat Completions

必须验证：

- 普通文本和多轮消息。
- 图片 URL/图片内容。
- assistant tool calls。
- tool role 结果。
- 非流式 usage。
- 流式 delta、结束事件和 usage。
- 上游错误转换。

### 6.4 Responses

- 使用稳定的 `response.id` 和 `output` 结构。
- 流式事件必须有明确事件类型，不能把所有片段拼成一条非标准字符串。

输入可以支持文本、图片、工具调用和工具结果；每个不支持的 input item 类型必须被识别并返回结构化错误。托管 input item（`web_search_call`/`mcp_call`）是被识别的一类：它们抽进托管历史随请求回放（§6.7）；`reasoning`/`file_search_call`/`computer_call`/`image_generation_call`/`code_interpreter_call`/`local_shell`/`mcp_list_tools`/`mcp_approval_*` 仍是 skip + debug 记录；其余未识别类型返回结构化错误。

**跨协议时**（Responses 客户端 → 非 Responses 上游，请求须经 `GeneralOpenAIRequest` 中间结构）：

- `store` 默认按 `false` 处理；`store=true` 忽略并 debug 记录（`relay/protocol/responses.go:103`；客户端如 Codex 默认会带，硬拒会打断它），不做服务端响应持久化。
- 收到 `previous_response_id` 时返回不支持错误。
- 收到后台任务参数（`background=true`）时返回不支持错误。
- `tools` 中的内置（hosted）工具类型**不再丢弃**：`web_search*` 抽成托管声明并按目标协议映射（chat 目标折成 `web_search_options`，Anthropic 目标折成 `web_search_20250305`），`mcp` 与其余 `native` 类按渠道策略处置而非无声丢弃。见 §6.7。`{"type":"custom"}` 的自由格式工具也不丢弃，而是降级成单 `input` 字符串的函数，见 §6.8。

**同协议时**（Responses 客户端 → Responses 上游）走直通，以上限制**全部不适用**：原生 body 原样转发，`store`、`previous_response_id`、`include`、`reasoning`、`text.format`、hosted 工具声明与未知字段逐字节到达上游；网关只定点补丁顶层 `model` 与强制 prompt（并入 `instructions`）。

### 6.5 Anthropic Messages

鉴权顺序：

1. 优先读取 `x-api-key`。
2. 没有时兼容 `Authorization: Bearer ...`。
3. 两者冲突时按项目安全策略拒绝或明确规定优先级，不能随机选择。

请求和响应要覆盖：

- system。
- content 数组。
- text/image block。
- tool_use / tool_result。
- 托管块 `server_tool_use` + `web_search_tool_result`、`mcp_tool_use` + `mcp_tool_result`（§6.7）。
- messages 的 `role`：**不做白名单校验，原样透传**（含 `system`）。Anthropic 自己的 API 只允许 user/assistant，但网关不是 Anthropic——chat 上游接受更多角色，另两个转换器也各自把 system 折进自己唯一的那个槽位（`relay/adaptor/anthropic/main.go` 折进顶层 `system`，`relay/adaptor/openai_responses/request.go` 折进 `instructions`），参考实现同样原样透传。**这里不需要校验**：拒绝它等于拒绝每个下游都已经准备好服务的请求（曾经就是具名 400，线上有客户端因此被打回）。针对 content block 的规则（`tool_use` 只能在 assistant 消息里、未知块具名拒绝等）不受影响，那是内容形状而非角色白名单。
- `stop_reason`。
- `stop_sequence`。
- usage。

**跨协议时**（Messages 客户端 → 非 Messages 上游）：`thinking` 与 `cache_control` 在 `GeneralOpenAIRequest` 里没有槽位，会丢失——已知有损，须在文档与冒烟里钉住，不能无声消失。`server_tool_use`/`web_search_tool_result`/`mcp_tool_use`/`mcp_tool_result` **已不再属于这一类**：它们抽进托管声明与托管历史，按目标协议映射，无法映射的按渠道策略具名处置（§6.7）。

**同协议时**（Messages 客户端 → Messages 上游）走直通：`cache_control`、`thinking`、`anthropic-beta` 请求头、未知 content block 类型全部原样往返。

### 6.6 上游协议选择

渠道配置需要能表达上游协议类型：

```text
openai-chat
openai-responses
anthropic-messages
compatible
```

这组字符串一旦由管理员保存进 `channels.config.upstream_protocol`，就成为事实上的公开接口：钉成 `relay/upstream` 里的常量，保存时白名单校验，拼错必须报错。**不能静默退回 `openai-chat`**——那会把渠道悄悄改道到翻译路径上，从而改变 hosted tools 的去向（见 §6.7），而失败是静默的，正是这个声明要防的事。该字段落在既有的 `channels.config` JSON blob 里，无 schema 变更、无迁移条目。

同一个 blob 里的 `tool_loss_policy`（`allow`/`safe`/`strict`，空即 `allow`）同样在保存时校验：取值不在三者之内则保存失败（`未知的托管工具丢损策略 %q，可选值：allow / safe / strict`）。理由与上一段同构——一个拼错的策略名若静默退回 `allow`，运维会以为自己设了 `strict` 而实际没有。它同样落在既有 blob 内，无 schema 变更、无迁移条目。

客户端协议和上游协议不能混为一谈。一个 Anthropic 客户端请求可以发送给 OpenAI 兼容上游，反之亦然，但必须经过统一中间结构和能力检查。三种客户端协议 × 三种上游协议构成矩阵，每格落点如下：

| ↓ 上游 / → 客户端 | chat/completions | responses | messages |
| --- | --- | --- | --- |
| openai-chat | chat 路径（raw 快路径） | codec 翻译 | codec 翻译 |
| anthropic-messages | chat 路径（adaptor 转换） | codec 翻译 | 直通 |
| openai-responses | chat 路径（`text_responses.go`） | 直通 | codec 翻译 |

- **上游协议解析**：显式配置 > 由 `apitype` 推导 > `openai-chat`。因此既有渠道无需任何配置即保持原行为。
- **能力检查取代硬编码闸门**：`CanServe(client, up)` 取代 `meta.APIType == apitype.OpenAI`。未翻译的上游家族（Gemini、Baidu、Bedrock 等）解析为 `unsupported`，仍然服务 `/v1/chat/completions`（走它自己的 adaptor），只有两个需要改写请求体的协议入口不可达，返回具名 `upstream_protocol_unsupported`。
- **同协议即直通**：`client == up` 时不解析、不重渲染，原生字节双向转发。这是保住 `cache_control`、`thinking`、hosted tools 与未知字段的唯一办法，也是"六个跨协议格子必然有损、三个同协议格子必然无损"这一取舍的来源。阶段 9 之后 hosted tools 不再"只有直通才保得住"——跨协议格子现在按 §6.7 映射，或按渠道策略具名处置。直通仍然是无损的那条路，但不再是唯一的路。
- **跨协议必经 codec**：codec 的输入输出都是既有渲染器已经在吃的形态（`*openai.TextResponse` / `sse.Chunk`），所以 `entry.Parse` / `entry.Render` / `entry.NewStream` 不需要按上游协议长分支。
- **选渠道时按能力过滤**：上游协议进入渠道选择（内存缓存与库路径都先按客户端协议过滤再算优先级档位）。分组里混用协议时，不能服务该客户端协议的渠道在选中前就被排除，而不是发出去等 400——`Relay` 的重试对 400 不重试（`shouldRetry`），发出去就已经失败了。

### 6.7 托管工具（hosted tools）

**托管工具**是**由上游自己执行**的工具：Anthropic 的 `web_search_20250305` 与 MCP server，Responses 的 `web_search` / `mcp`。客户端只声明它，网关从不执行它；上游把执行结果按自己的块类型（`server_tool_use` + `web_search_tool_result`、`mcp_tool_use` + `mcp_tool_result`）或 output item（`web_search_call`、`mcp_call`）报回来。Chat Completions 对这一切没有词汇表，只有 `web_search_options` 这一个开关——这正是它成为跨协议问题而不是字段映射问题的原因。

**包归属。** 纯类型、kind 表与策略在 `relay/model/hosted.go`，不依赖任何 adaptor：`relay/protocol` 只依赖 `relay/model` + `relay/sse`，若把 kind 表挪进一个依赖 adaptor 的新包，就会为了让 `relay/protocol` 查表而把它间接拖进 adaptor 依赖。需要 adaptor 的两件事分居两处——请求方向的挂载在 `relay/controller/hosted_attach.go`（chat 目标）、`relay/adaptor/openai_responses/request.go`（responses 目标）、`relay/adaptor/anthropic/main.go`（messages 目标）；流式响应方向的桥接在 `relay/controller/hosted_bridge.go`。

**载体。** 中立数据挂在既有的归一化类型上，用 `json:"-"` 字段：`GeneralOpenAIRequest.Hosted`、`openai.TextResponse.Hosted`、`sse.Chunk.Hosted`；custom 工具（§6.8）另加 `GeneralOpenAIRequest.CustomTools` / `openai.TextResponse.CustomTools`。这样不会泄漏进上游报文，`Parse` / `RequestBody` / `Render` / `protocolStream` 的签名也都不变。**一处例外**：`streamFactory` 增加了 custom 工具集合参数——流式渲染器必须在第一个 chunk 之前就知道它，而那时还没有任何响应载体可以嫁接。守卫是 `sse.ParseChunk` 永不填充 `Hosted`、`StreamState.Absorb` 忽略它——托管流事件只能由 codec 写，不可能经 JSON 往返意外产生。

**只有两种 kind 跨协议映射**：`web_search` 与 `mcp`。其余（computer use、bash、text editor、code execution、file search、image generation、web fetch、url context）一律是 `native`，走策略而不做映射。这张窄表是照参考实现的：`code_execution` 在 Responses 里没有 `container_id` 就无法表达，发明一个映射比具名记一次丢损更糟。

**策略。** 按渠道配置，落在既有 `channels.config` JSON blob 的 `tool_loss_policy`（`allow`/`safe`/`strict`，空即 `allow`；零 schema 变更、无迁移条目），保存时白名单校验（见 §6.6）。语义与 new-api 的 `convmeta.Options.ToolLossPolicy` 一致：

- `allow`（默认）：目标表达不了的产物被**丢弃、记一条具名诊断、请求照常成功**，客户端不被告知。
- `safe`：同一批诊断中凡 severity 为 `error` 者，变成 **400**（`hosted_tool_loss`）。
- `strict`：任何诊断都变成 400。

**`safe`/`strict` 只作用于请求阶段；响应阶段与流式阶段在任何策略下都不拒绝。** 到那时响应已经在流式写出、上游也真的执行完了搜索，此处拒绝等于把一次已完成的搜索丢掉，而不是保护谁。

这条例外只放宽 §1 的第 7 条，不放宽任何其它约定，且两端都有界：请求阶段的丢损在 `safe`/`strict` 下会变成具名 400；而**每一个阶段**（`allow` 下同样）都为每次丢损写一行面向运维的日志，由 `logHostedLosses` 经 `logger.SysWarnf` 落进 `oneapi.log`：

```text
channel %d hosted_tool_loss: phase=%s code=%s path=%s %s
```

`phase` 取 `request` / `response` / `stream`。“客户端不知道”是照参考实现有意为之；“运维不知道”不是。

**诊断码**是 `relay/model/hosted.go` 里的常量，三个阶段共用：`unsupported_hosted_tool`、`unsupported_hosted_history`、`unsupported_search_limit`、`unsupported_domain_filter`、`duplicate_hosted_web_search`、`forced_tool_choice_downgraded`、`web_search_result_continuation_lost`、`web_search_response_unrepresentable`、`web_search_result_omitted`、`mcp_identity_missing`、`mcp_result_unrepresentable`、`mcp_result_missing`，以及 §6.8 的 `custom_tool_as_function`、`custom_tool_name_conflict`。

其中两个容易混为一谈，且语义相反：

- **`web_search_result_continuation_lost` 是请求阶段（历史回放）的丢损。** 第二轮会把上一轮的托管历史回放过去，而两种协议对"一条搜索记录"的定义不一致：Responses 的 `web_search_call` 只有 action、没有结果位，而 Anthropic 拒绝没有配对结果的 `server_tool_use`（于是用 `web_search_tool_result_error(unavailable)` 占位）；反向时 Claude 的 `web_search_tool_result` 的结果在 Responses 的 item 里无处安放。两者都在回放点具名。
- **`web_search_response_unrepresentable` 是响应阶段的丢损。** 上游的 `web_search_call` 对 Claude 客户端整件无法渲染——Claude 的结果块必须携带只有 Anthropic 能签发的加密搜索结果，而没有结果的调用块又不是 API 接受的形状——所以整件丢弃并具名。参考实现在这里同样是丢弃。

**逐格落点**（请求方向 / 响应方向）：

| 客户端 | 上游 | 请求方向 | 响应方向 |
| --- | --- | --- | --- |
| `messages` | `openai-chat` | `web_search_20250305` → `web_search_options` | 无（chat 无法产出托管事件） |
| `responses` | `openai-chat` | `web_search` → `web_search_options` | 无 |
| `messages` | `openai-responses` | → `{"type":"web_search"}` | `web_search_call` → **丢弃并具名** `web_search_response_unrepresentable`；`mcp_call` → `mcp_tool_use` + `mcp_tool_result` |
| `responses` | `anthropic-messages` | → `{"type":"web_search_20250305","name":"web_search"}` | `server_tool_use` + `web_search_tool_result` → `web_search_call`；MCP 同理 |

三个 identity 格子仍走直通，零新代码。**注意上表与阶段 8 计划稿的差异**：计划稿把 `messages` ← `openai-responses` 的响应方向写成 `web_search_call` → `server_tool_use` + `web_search_tool_result`，那是错的。Claude 的结果块必须携带 provider 加密结果，网关重建不出来，所以这条搜索只能整件丢弃并具名——参考实现也是这么做的（`web_search_response_unrepresentable`）。MCP 才是两个方向都能活下来的那一种。

流式：`relay/controller/protocol.go` 的 `protocolStream` 增加 `Hosted(ev)`，`relayProtocolStream` 在分派 `Chunk` 之前先分派 `chunk.Hosted`。两座桥都是"坐在转换器前面的纯观察者"：`claudeHostedBridge` 按 index 闩住 `server_tool_use`/`mcp_tool_use` 的 `content_block_start`、吞掉其 `input_json_delta`、在 `content_block_stop` 时发出事件（此刻 input 才完整），并把结果块按 `tool_use_id` 折回同一件产物；`responsesHostedBridge` 把 `output_item.added`/`done` 与 `web_search_call.*`/`mcp_call.*` 进度事件折成 start/update/done 三段。**被认领的帧不再进转换器**，否则 `input_json_delta` 会漏成一个客户端从未声明的函数调用。终态上 `ResponsesStream.Finish` 把未闭合的 hosted item 补成 `incomplete`，`MessagesStream` 则因为每个托管块写完即闭而天然没有悬空块——两个方向都绝不伪造一个结果块。

### 6.8 自由格式工具（Responses custom tools）

**custom 工具**是 Responses 独有的一种声明：`{"type":"custom","name":"apply_patch","format":{...}}`，输入是自由文本而不是参数对象。Codex 用它声明 `apply_patch` 等工具，而**其它两种协议都没有自由格式工具**。

**它不丢弃，而是降级。** `relay/protocol/responses.go` 的 `parseResponsesTools` 把它转成一个**只带一个必填字符串参数 `input` 的函数**送上游（`required:["input"]`、`additionalProperties:false`——既然承载形式只剩一个槽，就不该让模型在这之外编造参数）。客户端的描述与它带的语法都保留进这个函数的 description：`format.type=="grammar"` 时按 `syntax` 分别写成 Lark / 正则 / 其它语法的提示。JSON Schema 只能说"是个字符串"，说不出"必须是这个语法"，所以语法只能降级成提示——但丢掉它，模型就不知道该产什么形状。

**回程按名字还原。** 请求侧把哪些名字原本是 custom 记在 `GeneralOpenAIRequest.CustomTools`（`json:"-"`）上，relay 在 `codec.NonStreamResponse` 与 `entry.Render` 之间把它嫁接到 `openai.TextResponse.CustomTools`（与 codec 嫁接 `Hosted` 同一手法），流式则经 `streamFactory` 的参数传给渲染器。渲染时命中该名字的调用**出 `custom_tool_call` 项**：`input` 是裸文本，**不带 `arguments` 键**。解包不出来时原样交回整串 arguments——模型若没按约定形状作答，那也仍是它的回答，给一个空 input 等于把它丢了。

**两种严重级别，不可互换：**

- `custom_tool_as_function`（**warning**，presentation）：降级没有丢东西，工具仍可调用，所以它**不该在 `safe` 下拦住请求**，只有 `strict` 会拒绝。
- `custom_tool_name_conflict`（**error**）：custom 工具与**另一件**工具重名时被丢弃。目标协议只靠名字区分调用，同名的两件对它来说是同一件，于是会有一件永远调不到——这与"换了承载形式"不同，确实改变了模型能做什么。**函数无论声明在前在后都赢**，所以这个检查放在整个 `tools` 数组读完**之后**，不能放进单趟循环。被丢的名字不进留档集合，于是强制它的 `tool_choice` 自然落回既有分支、降级为 auto 并记 `forced_tool_choice_downgraded`——诚实地表示"我们没送这个工具，就强制不了它"。

**流式不是改名。** custom 的输入是 `{"input":"..."}` 这个 JSON 对象在逐片段拼，而 `{"input":"*** Beg` 这样的片段**没法解包**成客户端要的裸文本。所以逐片段的 `response.function_call_arguments.delta` 对 custom **不发**（参数照旧累积），整个 input 在 item 关闭时**一次性**发出：`response.custom_tool_call_input.delta` + `.done`。`output_index` 的记账不受影响——custom item 照样占一个槽，编号仍稠密。

**逐格落点**（Responses 客户端）：

| 上游 | 请求方向 | 响应方向 |
| --- | --- | --- |
| `openai-chat` / `compatible` | custom → 单 `input` 字符串的函数 | 该名字的函数调用 → `custom_tool_call` |
| `anthropic-messages` | 同上（Anthropic 也没有自由格式工具） | 同上 |
| `openai-responses` | 直通，声明原样到达上游 | 直通 |

**范围之外**（有意不做，见进度日志）：不实现 tool `namespace`（继续完全忽略该字段）；不修 `relay/adaptor/openai_responses/response.go` 的 `decode()` 对**上游返回**的 `custom_tool_call` 的处理（那是 chat 客户端 ← Responses 上游的方向）。



## 7. 阶段 4：流式响应与 Usage 归一化

### 7.1 SSE 读取器

实现可复用的 SSE reader/writer，处理：

- 多行 `data:`。
- 空行分隔事件。
- `event:` 类型。
- `[DONE]` 或协议等价结束标记。
- 网络中断。
- 上游 error event。
- JSON 分片跨事件边界。

不能假设一个 `Read` 就对应一个完整 JSON。工具参数增量必须按调用 ID 分组累加，结束时再解析/记录完整参数。

### 7.2 流式事件状态

每个请求维护：

```text
request_id
response_id
message_id
tool_call_id -> accumulated arguments
text accumulator
usage accumulator
upstream error
terminal status
```

收到最终 usage 时以报告值为准；没有最终 usage 时：

- 如果允许估算，记录 `usage_source=estimated`。
- 如果无法估算，只记录请求状态和空 usage。
- 不把空 usage 改成零缓存命中。

### 7.3 客户端断开

客户端断开后应停止继续写响应，但要尽可能关闭上游连接并完成一次 usage 收尾。该记录状态应为 `partial` 或项目定义的等价值。

## 8. 阶段 5：缓存字段归一化

### 8.1 字段映射

| 协议 | 上游字段 | 归一化字段 |
| --- | --- | --- |
| Chat Completions | `usage.prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` |
| Responses | `usage.input_tokens_details.cached_tokens` | `cache_read_input_tokens` |
| Responses | 上游写入明细（若存在） | `cache_write_input_tokens` |
| Anthropic | `usage.cache_read_input_tokens` | `cache_read_input_tokens` |
| Anthropic | `usage.cache_creation_input_tokens` | `cache_write_input_tokens` |

不同适配器可以保留原始 usage JSON，便于审计和未来支持新字段。

### 8.2 状态规则

```text
有合法缓存字段       -> cache_usage_status=reported
明确没有缓存字段     -> cache_usage_status=not_reported
字段存在但值不合法   -> cache_usage_status=invalid
```

不要根据模型名称、渠道名称或请求是否重复来猜测缓存是否命中。

### 8.3 计算规则

```text
token_hit_rate =
sum(cache_read_input_tokens)
/ sum(input_tokens)
```

只有 `cache_usage_status=reported` 且缓存读取字段有效的记录进入可计算分子/分母。缓存写入只展示数量，不加入命中分子。

异常比例可以暂时保留原始计算结果并标记异常；前端显示“数据异常”，不要无提示地限制为 100%。

## 9. 阶段 6：前端实施顺序

### 9.1 基础布局

先修改：

- `web/default/src/index.css`
- `web/default/src/components/Header.js`
- 主布局和导航组件

完成：

- 左侧导航/移动端抽屉。
- 顶部栏和面包屑。
- 统一页面最大宽度、间距和卡片。
- 表格容器横向滚动。
- 统一按钮、标签、确认弹窗和 Toast。

### 9.2 Dashboard

在 `web/default/src/pages/Dashboard/index.js` 中按顺序实现：

1. 时间范围和刷新控制。
2. Token 总览卡片。
3. 缓存三指标卡片。
4. 按日期趋势。
5. 按用户/模型/渠道排行。
6. 加载、空数据、错误和无权限状态。

指标卡片必须显示数据覆盖率或“未报告缓存数据”的提示。

### 9.3 业务页面

按以下顺序处理：

1. `UsersTable.js`：删除余额、额度、倍率，增加 Token 汇总入口。
2. `TokensTable.js`：保留令牌身份、有效期、状态和使用 Token。
3. `LogsTable.js`：区分历史日志和新 usage 记录。
4. `OperationSetting.js`：移除费用、倍率和充值相关设置。
5. 模型广场：只显示模型可用性、渠道状态和能力，不显示价格。

前端不能通过 `undefined` 字段做隐式降级；接口字段变更后应同步调整类型/默认值和空状态。

## 10. 数据迁移、灰度与回滚

### 10.1 迁移前检查

迁移命令必须支持 `check`/`dry-run` 模式，至少检查：

1. 数据库连接和数据库类型。
2. 当前 schema migration 版本。
3. 是否存在未完成的旧迁移。
4. 所需表是否存在：`users`、`tokens`、`channels`、`abilities`、`options`、`logs`。
5. 是否存在重复用户名、重复令牌 Key、孤儿令牌或孤儿能力记录。
6. 数据库磁盘空间、连接权限和事务能力。
7. `LOG_SQL_DSN` 是否配置，以及日志库是否可连接。
8. 待迁移表中是否存在无法兼容的字段类型或超长数据。

检查失败时只输出报告，不修改数据库。

### 10.2 备份

正式迁移前必须产生可恢复备份：

- SQLite：复制主库文件，并在迁移前执行一致性检查；独立日志库文件也要单独备份。
- MySQL：使用一致性导出或等价快照，包含表结构、数据、触发器和索引。
- PostgreSQL：使用一致性 dump/快照，主库和日志库分别保存。

备份文件、数据库类型、版本、时间和校验和必须写入迁移记录。没有可验证备份时不得执行生产迁移。

### 10.3 正式迁移顺序

1. 停止写入或进入维护模式，避免迁移期间产生未覆盖的新数据。
2. 执行迁移前检查和备份。
3. 在主库创建迁移版本表。
4. 创建 `usage_records` 及其索引。
5. 写入 `usage_cutover_at` 和迁移版本。
6. 对主库执行结构和数据校验。
7. 对 `LOG_SQL_DSN` 日志库执行日志表迁移和校验。
8. 部署只读兼容版本，确认用户、令牌和渠道可以登录/鉴权。
9. 启用 recorder，同时保留旧字段但停止新请求更新额度。
10. 观察一段时间后启用新统计和前端。

迁移步骤要么处于已提交的完整版本，要么可以安全重试；不能依赖手工记忆“上次执行到哪一条 SQL”。

### 10.4 迁移后校验

自动生成校验报告，至少包含：

```text
database_type
from_version
to_version
main_db_status
log_db_status
users_before/after
tokens_before/after
channels_before/after
abilities_before/after
options_before/after
logs_before/after
usage_records_created
orphan_records
warnings
```

核心表行数发生非预期减少时，迁移必须失败或进入人工确认状态。仅允许新增迁移表和预期索引/版本记录造成行数变化。

### 10.5 回滚步骤

如果新协议或统计层异常：

1. 关闭新 API 路由开关或恢复旧路由处理。
2. 保留 `usage_records` 和迁移版本表，不删除新数据。
3. 恢复上一版本应用；由于旧额度列仍保留，旧版本应能读取原表。
4. 如果迁移只新增了表/索引，优先回滚应用而不是回滚数据库。
5. 只有在新增结构阻塞旧版本启动时，才使用经过验证的 `Down` 或备份恢复。
6. 检查旧字段是否仍可读，并记录新旧请求数量差异和失败样本。

如果数据库迁移失败，优先修复迁移脚本并从检查点重试；不要手工删除生产表，也不要直接执行 `DROP COLUMN` 修复。

## 11. Agent 分工与文件所有权

建议拆成以下互不重叠的工作包：

| Agent | 主要文件/目录 | 交付物 |
| --- | --- | --- |
| A：数据与统计 | `model/`、usage service、迁移 | usage_records、recorder、聚合 API |
| B：额度清理 | `relay/controller/`、`relay/billing/`、用户/令牌 controller | 移除结算和倍率依赖 |
| C：协议层 | `router/relay.go`、`relay/adaptor/`、`relay/protocol/`、`relay/controller/protocol*.go` | Responses、Anthropic、SSE、跨协议矩阵 |
| C2：托管工具 | `relay/model/hosted.go`、`relay/controller/hosted_attach.go`、`hosted_bridge.go` | 托管声明/历史的映射与丢损策略（§6.7） |
| D：前端 | `web/default/src/` | 布局、Dashboard、页面清理 |
| E：测试与文档 | `docs/`、测试目录 | 协议矩阵、迁移说明、验收报告 |

共享结构先由 A 定义并提交接口说明，其他 Agent 不要各自复制一套 Usage 类型。冲突较大的文件（例如 `relay/model/misc.go`、`router/relay.go`）应指定单一 owner。

## 12. 测试计划

### 12.1 单元测试

- usage 字段解析和校验。
- `0`/`null`/`invalid` 缓存状态。
- 三种协议的请求转换。
- 三种协议的响应转换。
- 工具调用参数增量合并。
- SSE 多行和跨 chunk 解析。
- request_id 幂等写入。
- 聚合命中率计算。

### 12.2 集成测试

至少覆盖：

| 客户端 | 上游 | 流式 | 场景 |
| --- | --- | --- | --- |
| Chat | OpenAI-compatible | 否 | 文本+usage |
| Chat | OpenAI-compatible | 是 | delta+usage |
| Responses | OpenAI-compatible | 否 | output+usage |
| Responses | OpenAI-compatible | 是 | typed events |
| Anthropic | Anthropic | 否 | content+cache usage |
| Anthropic | Anthropic | 是 | content_block_delta |
| Anthropic | OpenAI-compatible | 否 | 协议转换 |
| Chat | Anthropic | 否 | 协议转换 |

每一项都要加入工具调用、工具结果、图片或明确记录“不支持并正确报错”的测试。

### 12.3 回归检查

- 用户、令牌、渠道、分组权限仍然有效。
- 有效期和网段限制仍然有效。
- 限流和错误码没有被额度清理误删。
- 老日志仍可查看。
- 前端刷新、分页、删除确认和移动端布局正常。

## 13. 验收清单

提交前逐项确认：

- [ ] 新请求路径没有额度预扣、扣除、退款和额度不足判断。
- [ ] 新请求路径没有模型/分组倍率计费。
- [ ] 所有成功生成请求都有 usage 收尾路径。
- [ ] usage 写入按 `request_id` 幂等。
- [ ] 用户 Token 汇总可查询。
- [ ] `/v1/chat/completions` 正常。
- [ ] `/v1/responses` 正常。
- [ ] `/v1/messages` 正常。
- [ ] `/v1/models` 正常。
- [ ] 三种协议的流式错误不会被吞掉。
- [ ] 工具参数增量不会丢失或串台。
- [ ] Prompt Caching 字段被正确归一化。
- [ ] 缓存命中率按 Token 加权。
- [ ] 未报告缓存不会显示为 0% 命中。
- [ ] Dashboard 显示命中率和覆盖率。
- [ ] 前端无余额、充值、倍率和费用文案。
- [ ] 数据库迁移可重复执行或有明确幂等保护。
- [ ] 回滚步骤已在测试环境验证。
- [ ] 旧版 SQLite、MySQL、PostgreSQL 主库和独立日志库迁移均已验证。
- [ ] 迁移中断重试、重复执行和备份恢复均已验证。
- [ ] 迁移报告确认用户、令牌、渠道、权限、配置和历史日志未丢失。

## 14. 交付物

最终实现至少应包含：

1. 数据库迁移文件。
2. UsageRecorder 和 usage 聚合 API。
3. 多协议路由、转换器和 SSE 测试。
4. 缓存字段归一化及指标查询。
5. 前端现代化页面和响应式样式。
6. API 字段说明及迁移/回滚说明。
7. 测试报告，列出通过项、已知限制和未支持的协议能力。
8. 可重复执行的数据库迁移器、预检查命令、校验报告和恢复说明。
