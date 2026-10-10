# One API 现代化改造实施记录

> 面向：改造执行者与审查者
> 依据：[one-api-modernization-implementation.md](./one-api-modernization-implementation.md) §3
> 状态：持续更新

## 阶段 0：基线与契约冻结

### 0.1 基线提交

| 项 | 值 |
| --- | --- |
| 改造前 HEAD | `14c4e2c`（feat: 增加模型广场页面与聚合接口） |
| 文档基线提交 | `6ff88f8`（docs: 提交现代化改造计划与实施文档） |
| 分支 | `main` |

### 0.2 代码盘点：额度/倍率符号读写位置（2026-10-07）

Go 后端命中分布（次数为符号出现行数）：

- 额度主链路：`model/token.go`(30)、`relay/controller/helper.go`(24)、`model/user.go`(22)、`relay/controller/audio.go`(15)、`relay/billing/billing.go`(14)、`model/cache.go`(13)、`model/utils.go`(12)、`model/option.go`(12)、`controller/billing.go`(12)、`relay/controller/text.go`(5)、`relay/controller/image.go`(7)
- 倍率：`relay/billing/ratio/model.go`(71)、`controller/plaza.go`(27)、`relay/billing/ratio/group.go`(17)、`relay/billing/ratio/image.go`(3)
- 管理/兼容面：`controller/token.go`(7)、`controller/user.go`(6)、`model/channel.go`(5)、`model/log.go`(4)、`controller/log.go`(2)、`common/config/config.go`(4)、`controller/misc.go`(1)

前端命中文件：`App.js`、`components/LogsTable.js`、`components/OperationSetting.js`、`components/TokensTable.js`、`components/UsersTable.js`、`helpers/render.js`、`pages/Dashboard/index.js`、`pages/Plaza/index.js`、`pages/Token/EditToken.js`、`pages/User/EditUser.js`

分类结论：

| 类别 | 位置 | 说明 |
| --- | --- | --- |
| 必须删除 | `relay/controller/helper.go` 的 `getPreConsumedQuota`/`preConsumeQuota`/`postConsumeQuota` 中额度判定与扣减；`relay/billing/billing.go` 的 `ReturnPreConsumedQuota`/`PostConsumeQuota`；`relay/controller/text.go`、`image.go`、`audio.go` 中对以上函数的调用；`model/cache.go`、`model/token.go`、`model/user.go` 中放行前的额度检查路径 | 新请求路径不再以额度放行、预扣、退款、结算 |
| 必须删除 | `relay/billing/ratio/*` 参与业务计算的调用点（`modelRatio`/`groupRatio`/`completionRatio` 进入 quota 计算） | 倍率不再参与任何计算；比率表文件本体在阶段 2 末尾停用删除 |
| 兼容保留 | `users.quota`、`users.used_quota`、`tokens.remain_quota`、`tokens.used_quota`、`channels.used_quota`、`logs.quota` 数据库列；`model/utils.go` 中 `UpdateUserUsedQuotaAndRequestCount` 等函数（标记 legacy、停止新写入） | 只读兼容，旧版本回滚可用 |
| 兼容保留 | `controller/billing.go` 充值接口（阶段 2 第 7 步改为 deprecated 响应）、`model/option.go` 中倍率类 option key（读取忽略、不再生效） | 避免旧前端/旧部署瞬断 |
| 仍然需要 | `users.request_count`（历史展示）、`middleware/rate-limit.go` 与 `common/rate-limit.go` 中的限流（与额度无关）、`model/token.go` 的 `UnlimitedQuota` 字段列（仅保留数据） | 权限、限流、有效期逻辑保留 |

### 0.3 基线测试结果

- `go build ./...`：通过（仅 `go-sqlite3` 依赖的无害 C 编译警告）。
- `go test ./...`：**基线非全绿**。已知问题（与本改造无关，按 §3.2 约定不混入修复提交）：
  - `common/image` `TestDecode/Decode:jpeg` 失败：`image: unknown format`，且该包测试随后 panic（nil pointer），中断该测试二进制。
  - 其余包结果见 0.3a 全量记录。
- 前端：`web/default/node_modules` 未安装依赖，无法执行 `react-scripts build`。已记录为阶段 6 前置项：先 `npm install` 再取 lint/build 基线。

### 0.3a 全量 go test 记录（2026-10-07）

```text
FAIL  github.com/songquanpeng/one-api/common/image   (TestDecode/Decode:jpeg: image: unknown format; 随后 nil pointer panic 中断)
ok    github.com/songquanpeng/one-api/common/network
ok    github.com/songquanpeng/one-api/relay
ok    github.com/songquanpeng/one-api/relay/adaptor/aws/llama3
ok    github.com/songquanpeng/one-api/relay/channeltype
（其余包无测试文件）
```

结论：除 `common/image` 外基线可用。`common/image` 的 jpeg 解码失败与 panic 为存量问题，与本改造无关，不纳入修复提交，仅在此记录。

### 0.4 现有 /v1/chat/completions 行为（静态基线）

- 路由：`router/relay.go` → `controller.Relay` → `relaymode` 判定 → `RelayTextHelper`（`relay/controller/text.go`）。
- 链路：解析校验 → 模型映射 → 系统提示词注入 → `getPromptTokens`（本地 tiktoken 估算）→ `preConsumeQuota`（用户额度检查+令牌预扣）→ `adaptor.DoRequest` → `adaptor.DoResponse`（写客户端响应并返回 usage）→ 异步 `postConsumeQuota`（结算+写 `logs`+更新 `used_quota`）。
- 错误路径：上游错误或响应错误时 `billing.ReturnPreConsumedQuota` 退款。
- usage 现状：`relay/model/misc.go` 的 `Usage` 只有 `prompt/completion/total + completion_tokens_details`；上游未报告 usage 时由 `relay/adaptor/openai/main.go` Handler 用本地估算补齐（无法区分报告值与估算值——阶段 1 要解决的核心点之一）。
- 流式现状：`openai.StreamHandler` 只在流尾捕获 `streamResponse.Usage`，无 usage 时为 nil；无跨 chunk 工具参数聚合归因。
- `request_id`：已有基础设施 `common/helper` 的 `SetRequestID/GetRequestID`（ctx 传递），`logs.request_id` 已写入；`usage_records` 将复用该 ID 做幂等键。

### 0.5 当前数据库迁移机制（阶段 1 输入）

- `model/main.go` `InitDB`/`migrateDB`：`AutoMigrate` 七次调用覆盖 `channels/tokens/users/options/abilities/logs`。
- `InitLogDB`/`migrateLOGDB`：`LOG_SQL_DSN` 非空时独立初始化 `logs`。
- 无版本化迁移记录表；`bin/migration_lab.sql` 为人工一次性脚本（含 DROP COLUMN），不作为新机制。

### 0.6 x-api-key 现状

- 仅有出向：`relay/adaptor/anthropic/adaptor.go:29` 设置上游 header。
- 入向鉴权在 `middleware/auth.go` `TokenAuth`：只解析 `Authorization: Bearer sk-...`。阶段 3 需扩展支持 `x-api-key`。

### 0.7 契约冻结（新 API/数据结构字段名）

- `usage_records` 列：`id, request_id(unique), user_id, token_id, channel_id, model_name, input_tokens, output_tokens, total_tokens, cache_read_input_tokens(nullable), cache_write_input_tokens(nullable), request_count, usage_source, cache_usage_status, stream, status, created_at`。
- `usage_source ∈ {reported, estimated, legacy}`；`cache_usage_status ∈ {reported, not_reported, invalid}`；`status ∈ {success, error, partial}`。
- 管理端聚合 API：`GET /api/usage/summary|trend|by-user|by-model|by-channel`，响应字段：`input_tokens, output_tokens, total_tokens, request_count, cache_read_input_tokens, cache_write_input_tokens, cache_hit_rate(null 允许), call_hit_rate(null 允许), cache_coverage`。
- 命中率公式（禁止逐调用百分比平均）：`cache_hit_rate = Σ cache_read_input_tokens / Σ input_tokens`（仅 `cache_usage_status=reported` 记录参与）；`call_hit_rate = cache_read>0 的有效调用数 / 已报告缓存数据的有效调用数`；`cache_coverage = 可计算缓存指标的调用数 / 已产生模型响应的生成调用数`。
- `usage_cutover_at` 写入 `options` 表（key：`usage_cutover_at`），cutover 前历史数据不进入新聚合。

## 阶段 1：Usage 事实层

完成内容（2026-10-08，提交见 git log `feat(usage):`）：

1. **版本化迁移包 `model/migration/`**
   - `runner.go`：`schema_migrations` 记账表；`Migrate()` 按版本顺序执行、已应用版本跳过、每步可安全重试；`Check()` 提供 dry-run 预检（方言、当前/目标版本、pending 列表、旧表缺失、未注册版本告警）。
   - `v001_add_usage_records.go`：三种方言各自 DDL 创建 `usage_records`（`request_id` UNIQUE、缓存列可空），并在 `options` 写入 `usage_cutover_at`（幂等）。
   - `v002_add_usage_indexes.go`：`(user/token/model/channel, created_at)` 复合索引 + `created_at` 索引，`HasIndex` 判断实现幂等。
   - 接入点：`model/main.go` `InitDB` 在 `migrateDB()`（AutoMigrate 兜底）之后执行预检 + `migration.Migrate(DB, "main")`。`usage_records` 属主库；日志库暂不挂主库迁移集合（代码注释已说明扩展方式）。
2. **统一 Usage 结构 `relay/model/misc.go`**：指针语义区分未报告/零值；`NormalizeUsage` 派生 `reported/not_reported/invalid`，不合法值保留原样只打标记；total 不一致 → `Abnormal`，不改写原值。JSON 解析自动捕获 `prompt_tokens_details.cached_tokens` / `input_tokens_details.cached_tokens`。
3. **UsageRecorder `relay/usage/recorder.go`**：`Record/RecordOnce`；`request_id` 幂等（存在检查 + 唯一索引双保险）；未标注来源保守记 `estimated`；`error` 状态行 `request_count=0`；不触碰额度。
4. **全路径接入**：`relay/controller/text.go`（成功 + 上游错误 + 响应错误）、`image.go`（成功/非 2xx/解析错误）、`audio.go`（成功 + 非 200；转写输出 token 为本地估算）。与旧额度逻辑并行运行，旧结算路径本阶段未删除。
5. **OpenAI 适配器来源标注**：流式捕获 usage → `reported`；非流式上游缺 usage 时的本地补齐 → `estimated`，有值 → `reported`。
6. **聚合 API `controller/usage.go` + `model/usage.go`**：
   - `GET /api/usage/summary|trend|by-user|by-model|by-channel`（AdminAuth，支持 user_id/token_id/channel_id/model_name/时间过滤、limit）
   - `GET /api/usage/self/summary|trend`（UserAuth，强制本人范围）
   - 所有查询默认 `AfterCutover=true` 且排除 legacy；三率（命中率/调用命中率/覆盖率）由聚合分子分母计算，分母为 0 → `null`。
7. **测试**：`relay/model/misc_test.go`（6 例：not_reported、reported 零值、invalid 不裁剪、Anthropic 双缓存字段、abnormal 保留原值、details 回退映射）；`model/usage_test.go`（6 例：迁移幂等、request_id 幂等、聚合三率与 legacy 排除、cutover 过滤、error 行不计数）；`relay/usage/recorder_test.go`（3 例：来源保守标注、error 归零、幂等与 fallback id）。
8. **验证**：`go build ./...` 通过；`go test ./...` 除阶段 0 记录的存量 `common/image` 外全部通过。

并行运行观察点（阶段 2 前置）：新表与旧 `logs` 并存；上线灰度后对比 `usage_records` 行数与成功请求数、Token 总量与旧日志 `prompt_tokens+completion_tokens` 差异，确认后再进入阶段 2 删除结算调用。

## 阶段 2：移除额度与倍率

完成内容（2026-10-08，提交见 git log `refactor(quota):`）：

按实施文档 §5.1 顺序执行（接入 recorder → 并行验证（阶段1已做）→ 删除预扣/耗尽/退款 → 统计迁移 → 倍率计算删除 → 停止写旧列 → 充值 deprecated → API/前端不返回费用倍率）：

1. **relay 请求路径额度全删除**
   - `relay/controller/helper.go`：`getPreConsumedQuota/preConsumeQuota/postConsumeQuota` 删除；新增 `recordConsumeLog`（旧 `logs` 表继续写行以保持日志页可用，`Quota=0`，内容改为“用量：输入 X / 输出 Y tokens”）。
   - `relay/controller/text.go`：modelRatio/groupRatio/completionRatio 计算、预扣、退款、结算调用全部移除；错误路径同步 `recordUsage(status=error)`，成功路径异步 recorder + 消费日志。
   - `relay/controller/image.go`：costRatio/ratio/额度检查/`PostConsumeTokenQuota`/`CacheUpdateUserQuota`/`UpdateUserUsedQuota`/`UpdateChannelUsedQuota` 移除；消费日志改为“生成 N 张图片”。
   - `relay/controller/audio.go`：TTS 预扣、退款 defer、`billing.PostConsumeQuota` 移除；转写输出 token 仅作本地估算入 `usage_records`；消费日志 quota=0。
   - `relay/billing/billing.go` 整文件删除（无任何调用者）。
2. **放行条件去额度**
   - `model/token.go` `ValidateUserToken`：删除 `RemainQuota<=0` 拒绝与 `TokenStatusExhausted` 写入；历史 Exhausted 令牌直接放行；`PreConsumeTokenQuota/PostConsumeTokenQuota` 删除（含额度提醒邮件）。
   - `controller/token.go`：启用令牌时不再因额度耗尽拒绝；`AddToken/UpdateToken` 忽略请求中的额度输入（新令牌固定 `remain_quota=0 + unlimited_quota=true` 占位，更新保留原值）。
3. **旧列停止写入（§5.3 兼容读取）**：`UpdateUserUsedQuotaAndRequestCount`、`UpdateChannelUsedQuota`、`Increase/DecreaseUserQuota`、`Increase/DecreaseTokenQuota`、`Cache*UserQuota` 全部无调用者，标注 `LEGACY(阶段2)`；新用户注册不再赠送额度（quota 固定 0），`AdminTopUp` 改 410 Gone 明确 deprecated；`UpdateUser` 忽略 quota 输入（保留原值），quota 修改日志删除。
4. **费用/倍率展示移除**
   - `controller/plaza.go`：API 不再返回 `model_ratio/completion_ratio/group_ratio/input_quota_per_1k/output_quota_per_1k/quota_per_unit/display_in_currency/ratio_configured/ratio_varies`；仅保留可用性（模型 → 渠道 id/名称/类型/状态/优先级）。
   - `controller/billing.go`：`/v1/dashboard/billing/subscription|usage` 退化为纯协议兼容占位（固定“不限额”结构 + `total_usage:0`），不再读 quota 列；注释明确“不代表计量数据”。
   - `web/default/src/pages/Plaza/index.js` + i18n（zh/en）：删除费用/倍率列与文案（旧前端阶段 6 整体重写，此处仅消除对已删字段的依赖，防止 undefined 破坏页面）。
5. **保留项（有意不删）**：`relay/billing/ratio` 包保留——`controller/group.go` 用 `GroupRatio` 键枚举分组名、`openai/token.go` 枚举模型名、`image.go` 用 `ImageOriginModelName` 做模型映射（均为名称/映射用途，不参与计费）；`users.quota/used_quota`、`tokens.remain_quota/used_quota`、`channels.used_quota` 列与 `/api/user/self`、`/api/log/stat` 的 quota 读取保留给旧管理页（阶段 6 前端移除展示）；`options` 中倍率/额度配置键仍可读写但仅影响已停用的枚举表。
6. **端到端验收**：`scripts/smoke/run_smoke.sh` + `scripts/smoke/mock_upstream.py`（SQLite + mock 上游，17 项断言全过）：
   - quota=0 用户 + quota=0 令牌调用 `/v1/chat/completions` → 200 且响应正常（不再有任何余额/预扣拒绝）。
   - `usage_records` 写入 `success|reported|7|5|12`；上游 401 场景写入 `estimated|error`（阶段 1 行为复验）。
   - 旧 `logs` 消费行 `quota=0`，内容“用量：输入 7 / 输出 5 tokens”；`users(0|0|0)`、`tokens.used_quota(0)`、`channels.used_quota(0)` 均未被新请求写入。
   - `POST /api/topup` → 410；`/v1/dashboard/billing/*` 返回占位；`/api/plaza/models` 无费用字段。
7. **测试**：`go build ./...` 通过；`go test`（除存量红的 `common/image`）全部通过；`gofmt` 已格式化。

## 阶段 3：多 API 格式路由

### 完成时间
2026-10-08（提交于阶段 3 commit）

### 实施内容
1. **入向鉴权扩展** `middleware/auth.go` `TokenAuth`：新增 `extractTokenKey`，按 §6.5 顺序解析客户端令牌——
   - 优先 `x-api-key`；无则回退 `Authorization: Bearer ...`；
   - 两者同时存在时，用 `normalizeTokenRef`（去 `sk-` 前缀、截到首个 `-`）归一后比较，**指向不同令牌则确定性 401 拒绝**，不随机选择；等价写法（仅 Bearer 前缀/渠道后缀不同）不算冲突。
   - 出站上游密钥不受影响：`Distribute` 仍用所选渠道 key 覆盖 `Authorization`，`meta.APIKey` 取的是渠道 key，客户端 `x-api-key` 仅在 `TokenAuth` 消费。
2. **新增两个客户端协议入口** `router/relay.go`：`POST /v1/responses`、`POST /v1/messages`，复用既有中间件链（`RelayPanicRecover → TokenAuth → Distribute`）。`relaymode` 新增 `Responses`、`AnthropicMessages` 两个模式常量与 `GetByPath` 前缀映射；`controller/relay.go` `relayHelper` 增加分发分支。
3. **统一中间结构 + 分层** `relay/protocol/`（纯转换、无 gin、可单测）：
   - `errors.go`：`ProtocolError` 携带参数名/类型/码/状态，落地 §6.4/§6.5 “不支持项必须点名拒绝，不静默降级”。
     - **（阶段 9 部分作废）** 托管工具是该条约定**唯一**记在案的例外：目标协议表达不了时在默认 `allow` 策略下丢弃并写具名诊断，`safe`/`strict` 下才是具名 400。其余全部照旧。见实施文档 §6.7。
   - `responses.go`：`ParseResponses`（OpenAI Responses → `GeneralOpenAIRequest`）+ `RenderResponses`（`openai.TextResponse` → Responses 信封）。
   - `messages.go`：`ParseMessages`（Anthropic Messages → `GeneralOpenAIRequest`）+ `RenderMessages`（`openai.TextResponse` → Anthropic 信封），含 `finish_reason ↔ stop_reason` 反向映射（现有 anthropic 适配器只有“Claude 上游→OpenAI 客户端”方向）。
   - `util.go`：`json.RawMessage` 防御式读取小工具。
4. **共享 relay 内核** `relay/controller/protocol.go`：`relayProtocolHelper` 走 §6.1 管线——解析→模型映射/强制 system prompt/prompt 估算（与 chat 路径一致）→**上游协议能力检查**→用 OpenAI 兼容上游发起 `/v1/chat/completions`→读取并归一 `openai.TextResponse`+usage（reported/estimated 判定同 `openai.Handler`）→按客户端协议渲染→收尾 `recordConsumeLog`+`recordUsage`（复用阶段 1/2 的幂等 recorder，`request_id` 不变）。
5. **薄入口** `relay/controller/protocol_responses.go`、`protocol_messages.go`：各自 `protocolEntry`（Parse/Stream/Render）+ `RelayResponsesHelper`/`RelayMessagesHelper`。

### §6.4 Responses 第一版边界（均返回点名结构化错误）
- `store`：默认按 false；`store=true` → 拒绝（不做服务端持久化）。
  - **（后续修正，渠道配置提交 `a2d2281`）** 实际落地改为**忽略 + debug 记录**而非拒绝（`relay/protocol/responses.go:103`）：Codex 等客户端默认会带 `store=true`，硬拒会打断它们，而不做服务端持久化本身无害——响应总是内联返回。
- `previous_response_id` → 拒绝；`background=true` → 拒绝。
- `input` 支持裸字符串、message 项（含 text/image content part）、`function_call`、`function_call_output`；`reasoning`/`item_reference`/`computer_call`/未识别项类型 → 点名 `input[i].type` 拒绝。
  - **（阶段 9 修正）** `reasoning`/`file_search_call`/`computer_call`/`image_generation_call`/`code_interpreter_call`/`local_shell`/`mcp_list_tools`/`mcp_approval_*` 改为 skip + debug 记录（不再 4xx）；`web_search_call`/`mcp_call` 抽进托管历史随请求回放。其余未识别类型仍点名拒绝。
- `tools` 仅 function（内置 web_search/file_search 等拒绝）；`tool_choice` 支持 auto/none/required 与 function 对象。
  - **（阶段 9 作废）** 内置工具不再拒绝：`web_search*` 抽成托管声明并按目标协议映射，`mcp` 与其余 `native` 类按渠道策略处置。非 function 的 `tool_choice` 仍降级为 auto，但被强制的 kind 记进 `ForcedChoice` 并写 `forced_tool_choice_downgraded` 诊断。见实施文档 §6.7。
- 稳定 `response.id`（`resp_` + 上游 chatcmpl 后缀）、`output[]`（message + function_call 项）、`output_text` 由 content 派生；`finish_reason=length` → `status:"incomplete"`+`incomplete_details`。
- usage 指针语义：`input_tokens_details.cached_tokens`/`output_tokens_details.reasoning_tokens` **仅在上游上报时出现**，绝不伪造 0。

### §6.5 Messages 覆盖
- 请求：`system`（string 或 text block 数组）、`messages`（string 或 block 数组）、`max_tokens`（必填正整数）、`stop_sequences`、`temperature/top_p/top_k`、`tools`（input_schema）、`tool_choice`（auto/any→required/tool/none）。
- content block：`text`、`image`（base64→data URI、url→image_url）、`assistant` 的 `tool_use`（归一为 OpenAI tool_calls，arguments 存 JSON 字符串）、`tool_result`（归一为独立 `role:"tool"` 消息，带 `tool_call_id`）。`document`/未知块 → 点名拒绝；`tool_use` 出现在 user 消息 → 拒绝。
  - **（阶段 9 部分作废）** `server_tool_use`/`mcp_tool_use` 与 `web_search_tool_result`/`mcp_tool_result` 不再落进“未知块 → 点名拒绝”：它们抽成托管历史随请求回放（`HostedHistoryItem`），无法回放的目标按渠道策略具名处置。`document`、真正未识别的块类型、以及 user 消息里的 `tool_use` 仍然点名拒绝。见实施文档 §6.7。
- 响应：`id/msg_*`、`type:"message"`、`role:"assistant"`、`content`（text + tool_use 块，input 由 arguments 反解析）、`stop_reason`（stop→end_turn、length→max_tokens、tool_calls→tool_use、空→null）、`usage`（input/output_tokens）。
  - **（阶段 9 追加）** `content` 还能承载托管块：`mcp_tool_use` + `mcp_tool_result` 双向都渲染；`server_tool_use`/`web_search_tool_result` 只在请求方向（历史回放）成立，响应方向整件丢弃并具名 `web_search_response_unrepresentable`（Claude 的结果块必须携带 provider 加密结果，网关重建不出来）。

### §6.6 上游协议选择
- 阶段 3 两个入口仅支持 **OpenAI 兼容上游**（`meta.APIType==apitype.OpenAI`）；其他渠道类型返回明确 `upstream_protocol_unsupported`，不猜测、不静默降级。多上游协议矩阵在阶段 4+ 扩展。
  - **（阶段 8 作废）** 该硬编码闸门已被 `upstream.CanServe` 取代，三种客户端协议 × 三种上游协议全部可达；见「阶段 8：跨协议矩阵」。
- 客户端协议 ≠ 上游协议：入口只负责“客户端 JSON ↔ 统一中间结构 ↔ 规范 OpenAI 响应 ↔ 客户端渲染”，中间结构 `GeneralOpenAIRequest` 与既有分发/适配层解耦。

### 明确限制（阶段 4 收口）
- **流式（typed SSE）在阶段 4 实现**：`/v1/responses` 与 `/v1/messages` 收到 `stream=true` 时返回点名 400（`unsupported_parameter`，param=`stream`），非“拼成一条非标字符串”的降级。§6.4 “流式事件必须有明确事件类型” 由阶段 4 交付。
- 预检/协议级拒绝（缺 model、非法块、store/previous_response_id/background、上游类型不符、stream 延后）**不写 error usage 行**——未真正发起生成，与 chat 路径的校验错误行为一致；只有真实 relay 尝试（do_request 失败、上游错误、读取失败）才记 error 行。

### 阶段 2 遗留项修复
- `model/user.go` `Insert`：注册**不再赠送** `config.QuotaForNewUser`，新用户 `Quota=0` 起步，默认令牌 `RemainQuota:0,UnlimitedQuota:true`（此前检查点标记的未落地编辑，已补齐）。

### 测试与验收
1. 协议矩阵单测 `relay/protocol/{responses,messages}_test.go`：
   - Responses：裸串/instructions+content parts/tool_call+output 项映射；拒绝 previous_response_id/background/store=true/未知项/缺字段；渲染 id、completed/incomplete、function_call 项、usage 指针语义（无上报不造 details）。
   - Messages：system/text block、string content、image base64→data URI、tool_use+tool_result 往返、tools/tool_choice(any→required)；拒绝缺 max_tokens/坏 role/未知块/tool_use 在 user；渲染 text/tool_use、stop_reason 映射、length→max_tokens、空 finish→nil。
2. 鉴权单测 `middleware/auth_test.go`：x-api-key 优先、Bearer 回退、仅 x-api-key、**冲突确定性拒绝**、等价写法不算冲突、无头返回空。
3. 端到端冒烟 `scripts/smoke/run_smoke.sh` 扩至 **30 项全过**：新增 `/v1/responses` 200+信封+usage、拒绝 previous_response_id/store=true/stream(400)；`/v1/messages` 经 `x-api-key` 200+信封+usage、冲突 401、缺 max_tokens 400；三入口成功生成各落 1 条 `usage_records success`。
4. `go build ./...`、`gofmt`、`go vet`、`go test`（除存量红的 `common/image`）全部通过。

## 阶段 4：流式与 Usage 收尾

### 完成时间
2026-10-08（提交于阶段 4 commit）

### 实施内容
1. **可复用 SSE 读取器 `relay/sse/reader.go`**：按 SSE 帧法解析（`field: value`、空行分隔事件、多行 `data:` 以 `\n` 合并、`event:` 类型、`id:`、注释/keep-alive 行忽略、`[DONE]` 终止标记）。用 `bufio.Scanner` + 可增长缓冲（上限 1MB），**不假设一次 Read 对应一个完整 JSON**——只在空行/EOF 边界产出完整事件；EOF 未换行结尾的尾事件也会被冲刷。`ErrUpstreamEOF` 用于区分“网络中断（未见终止标记）”与正常结束。
2. **可复用 SSE 写出器 `relay/sse/writer.go`**：`Writer.Event(type, data)` 产出 `event: <type>\ndata: <json>\n\n`（§6.4 “流式事件必须有明确事件类型，不能拼成一条非标字符串”）；多行 data 逐行加 `data:` 前缀；实现者若为 `http.Flusher`（gin Writer 即是）则每个事件即时 flush。`Raw`/`Done` 供 OpenAI 风格透传；`SetEventStreamHeaders` 统一 SSE 响应头。
3. **每请求流式状态 `relay/sse/state.go`**：`StreamState` 在规范化 OpenAI `chat.completion.chunk`（导出的 `Chunk` 视图，含每工具调用 `index`）上累加——`request_id/response_id/text/usage/finish_reason/upstream error` 各归其位；**工具参数增量按 `index` 分组累加**（OpenAI 仅在首个分片带 `id`/`name`，后续只带 `index`+参数片段，按 index 归组避免串台），结束时可得完整参数串。`TerminalStatus(aborted)`：有上游 error event→`error`；未见 `[DONE]` 或被中断→`partial`；否则 `success`。`FinalizeUsage`：reported 上报值优先→`NormalizeUsage`；否则按 `counter(累计文本+全部工具参数)` 估算→`usage_source=estimated`；无 counter 且无上报→返回 nil（只记状态+空 usage，**绝不把空 usage 伪造成零缓存命中**）。
4. **两种协议 typed SSE 渲染器**：`relay/protocol/stream_responses.go`（`response.created/in_progress/output_item.added/content_part.added/output_text.delta/output_text.done/content_part.done/output_item.done/function_call_arguments.delta/done/completed|failed`，`length`→`incomplete` 状态）与 `relay/protocol/stream_messages.go`（`message_start/content_block_start/content_block_delta(text_delta|input_json_delta)/content_block_stop/message_delta(stop_reason+usage)/message_stop/error`，finish_reason→Anthropic stop_reason 逆映射）。`Error(e)` 为终止事件，后续 `Finish` 不再追加（错误不被吞掉且互斥）。
5. **入口接线 `relay/controller/protocol.go`**：移除阶段 3 的 `stream=true→点名 400` 拒绝（`ResponsesStreamUnsupported`/`MessagesStreamUnsupported` 死代码删除）。`relayProtocolHelper` 在 `normalized.Stream` 时走新 `relayProtocolStream`：置 `meta.IsStream`、补 `stream_options.include_usage` 让 OpenAI 兼容上游发 usage 尾块；`reader.Next()` 循环内先查 `ctx.Err()`（客户端断开）→`aborted`；写失败也判 `aborted`；malformed chunk 跳过重发但继续累加（不污染状态、不吞）；上游 error event→`stream.Error` 并记 `error` 行。收尾**恰好一次**：`FinalizeUsage`→`recordUsage(status)`+`go recordConsumeLog`；断流按 `partial` 落库并尽力关闭上游。响应 ID 用 `resp_<request_id>`（稳定），messages 工厂改写为 `msg_` 命名空间。

### §7 收口对照
- §7.1 读取器：多行 data/空行分隔/event 类型/`[DONE]`/网络中断/上游 error/跨事件 JSON 分片，均由 `sse.Reader` + `StreamState` 覆盖并有单测。
- §7.2 事件状态：`StreamState` 持有 response_id/工具调用 ID 分组累加/文本累加/usage 累加/上游 error/终止状态；上报优先、否则估算、再否则空 usage 不伪造零命中——单测逐条覆盖（`state_test.go`）。
- §7.3 客户端断开：`ctx.Err()`/写失败→停止写、关闭上游、`recordUsage` 以 `partial` 收尾一次。

### 明确限制（后续阶段）
- **仅 OpenAI 兼容上游流式**：与阶段 3 一致，非 OpenAI 上游仍返回 `upstream_protocol_unsupported`；Anthropic/AWS 等原生上游协议矩阵在后续扩展。**（阶段 8 作废）** 矩阵已补齐，Anthropic 与 Responses 上游的流式各有自己的累积器与直通镜像路径。
- **`/v1/chat/completions` 既有透传未改动**：仍走 `openai.StreamHandler` 原样透传（含 reported/estimated usage 收尾），避免破坏大量渠道；新 `sse` 层与 typed 渲染当前只服务 `/v1/responses`、`/v1/messages`。chat 路径的 `partial`/错误不吞如需统一到 `sse` 层，留待评估后再迁移（改动面大）。
- 缓存字段归一化与三指标贯通 adaptor 属阶段 5。

### 测试与验收
- 新增单测：`relay/sse/reader_test.go`（多行 data/event 类型/注释/尾事件冲刷/跨分片）、`relay/sse/writer_test`（并入 reader_test，帧法与多行前缀）、`relay/sse/state_test.go`（文本+finish、**工具参数按 index 分组不串台**、reported 优先、estimated 含工具参数、无估算返回 nil、上游 error 不吞、abort/无 DONE→partial、malformed 报错）、`relay/protocol/stream_test.go`（Responses/Messages typed 事件序列、delta 不拼接、工具参数不串台、error 事件终止性、stop_reason 映射、message_start 提示种子）。
- 冒烟（`scripts/smoke/`，mock 上游升级为 `stream=true` 时发真实 OpenAI SSE：两段文本 delta + usage 尾块 + `[DONE]`）：新增 responses 流式 content-type/typed 事件/delta 不拼接/reported usage，messages 流式 content-type/typed 事件/finish_reason→stop_reason；收尾计数改为 5 条 success（chat + responses 非流式/流式 + messages 非流式/流式）、全部 `usage_source=reported`、上游未报缓存时 `cache_usage_status=not_reported`。`RESULT pass=38 fail=0`。
- `go build ./...`、`gofmt -l`（含全仓 relay/model/middleware/controller/router）、`go vet`、`go test`（排除存量红 `common/image`）全部通过。

## 阶段 5：缓存归一化

### 完成时间
2026-10-08（提交于阶段 5 commit）

### 实施内容
1. **§8.1 字段映射**：
   - Chat Completions `usage.prompt_tokens_details.cached_tokens` / Responses `usage.input_tokens_details.cached_tokens` → `cache_read_input_tokens`：由 `relay/model.NormalizeUsage` 统一处理（读 `PromptTokensDetails`，缺失再回退 `InputTokensDetails`）。OpenAI 适配器非流式/流式均以值拷贝透传 details，`recordUsage` 前 `NormalizeUsage` 落库。
   - Anthropic `usage.cache_read_input_tokens` → `cache_read_input_tokens`、`usage.cache_creation_input_tokens` → `cache_write_input_tokens`：扩展 `relay/adaptor/anthropic/model.go` 的 `Usage`（指针字段，nil=未上报）；`Handler` 非流式映射 + `Source=reported`；`StreamHandler` 在 `message_start`(输入侧缓存)/`message_delta` 上“取最新非 nil”而非累加，避免缓存读被重复计数。新增 `i64Ptr` 保持 nil 语义。
   - 缓存写入仅 OpenAI 兼容上游“若存在”才映射（本实现下写侧通常为 nil→未上报，符合 §8.1“若存在”）。
2. **§8.2 状态规则**：合法缓存字段→`reported`；明确缺失→`not_reported`；值非法(负数/读+写>输入)→`invalid`；全部由 `NormalizeUsage` 判定，绝不按模型名/渠道名/是否重复猜测命中。上报的 0 缓存（ptr-to-0）仍是 `reported`，与“未上报”区分。
3. **§8.3 计算规则**：三指标沿用阶段 1 的聚合 SQL + `(*UsageAggRow).Summary()`——`cache_hit_rate=Σcache_read/Σinput`（分子分母仅含 `cache_usage_status=reported` 行）、`call_hit_rate=hit_calls/reported_cache_calls`、`cache_coverage=reported_cache_calls/generated_calls(success)`；分母为 0 → null（前端显示“—”，非 0%）。缓存写入只入展示、不进命中分子。异常(total≠prompt+completion)由 `normalizeTotalCheck` 置 `Abnormal` 并**保留原始值**（不钳制到 100%），recorder 记 warn。
4. **贯通 adaptor**：补齐此前阶段 3/4 未打 `Source` 的 Anthropic `Handler`/`StreamHandler`；OpenAI 侧阶段 2/4 已置 reported/estimated，本阶段仅核对 details 透传无丢失。

### 测试与验收
- 新增 `relay/adaptor/anthropic/usage_test.go`：缓存字段映射(read/write)+`Source=reported`；缓存缺失→两指针 nil→`NormalizeUsage` 判 `not_reported`（非伪造 0）；上报 0 缓存→ptr-to-0 且 `reported`。
- 复用阶段 1 单测：`relay/model/misc_test.go`（not_reported / reported-zero / invalid-不钳制 / 双字段 / abnormal 保留原值 / input_details 回退）、`model/usage_test.go`（`TestUsageAggregationRates`：仅 reported 行进分子分母、命中率 40/150、call 0.5、coverage 2/4、无 reported 缓存→null）。
- 冒烟（`scripts/smoke/mock_upstream.py` 现按 `prompt` 含 `usecache` 时经 `prompt_tokens_details.cached_tokens=3` 上报；并新增 chunked body 解码——Go 客户端在无已知长度时用 chunked，旧 mock 只读 Content-Length 会丢 body 且串扰连接）：`cached_tokens→cache_read_input_tokens=3`、恰 1 条 `reported` 缓存行、5 条 `not_reported`、`/api/usage/summary` 三指标 `cache_hit_rate=3/7`、`call_hit_rate=1`、`cache_coverage=1/6`、`cache_write=0`。`RESULT pass=42 fail=0`。
- `go build ./...`、`gofmt -l`、`go vet`、`go test`（排除存量红 `common/image`）全部通过。

### 遗留（阶段 6）
- `invalid`/`Abnormal` 前端“数据异常”展示（API 已回 `invalid_cache_calls` 计数与原始值），与旧额度/倍率展示移除一并在阶段 6 前端处理。

## 阶段 6：前端现代化

### 完成时间
2026-10-08（提交于阶段 6 commit）

### §9.2 Dashboard 重写
1. **数据源切换**：`pages/Dashboard/index.js` 弃用旧 `/api/user/dashboard`（按额度聚合），改用阶段 5 的 `/api/usage/*` 事实层接口——`summary`、`trend`（按天）、`by-model`（排行）。请求走统一 `API` 实例（带 `baseURL` 与响应错误处理），不再用裸 `axios`。
2. **权限分流**：`isAdmin()` 为真走管理端 `/api/usage/{summary,trend,by-model}`，普通用户走 `/api/usage/self/{summary,trend}`（后端无 self 维度排行，非管理员隐藏排行，不做隐式降级）。
3. **控件**：时间范围下拉（近 7/30/90 天、全部）+ 刷新按钮，随范围变化自动重取（`useCallback` 依赖 `days`）。
4. **Token 概览卡**：总/输入/输出 Token 与调用次数四张统计卡。
5. **缓存三指标卡（§9.2 硬要求）**：命中率 `cache_hit_rate`、调用命中率 `call_hit_rate`、覆盖率 `cache_coverage` 各一张，附公式说明。**可空指标为 null 时显示 “—”，绝不显示 0%**（`fmtRate` 对 null/undefined 返回 “—”）；未上报缓存的行数 >0 时，卡片挂 “未上报缓存数据” 标签并另给一条 info 提示“有 N 次调用未上报缓存数据”，同时**覆盖率始终展示**以体现数据可信范围。
6. **趋势/排行**：Token 与缓存读取按天折线；按模型 Token 排行（柱图 + 明细表，含每模型命中率/覆盖率，null 命中挂 “无数据” 标签）。
7. **状态**：loading（骨架卡）、error（负面提示）、no-permission（401/403 单独文案）、empty（无数据）。错误分类依据 `e?.response?.status`。

### §9.3 业务页移除余额/充值/倍率/费用
- **UsersTable**：删除“额度/剩余额度/已用额度”展示列与按额度排序项，改为“请求次数”列 + 每行“用量”入口链接（`/console/usage?user_id=`）；移除 `renderQuota` 依赖。
- **TokensTable**：删除“已用额度/剩余额度/无限额度”列与按额度排序项，保留令牌身份、状态、创建/到期时间，新增“请求次数”列（`renderNumber`）；`renderQuota`→`renderNumber`。
- **LogsTable**：删除“额度”列与表头、总消耗额度改为 **Token 总数**；移除“发放额度(topup)”日志类型筛选项；`renderQuota`→`renderNumber`。
- **OperationSetting**：整体重写，移除额度设置（新用户初始额度、预扣费、额度提醒阈值、单位美元额度、货币显示）与模型/补全/分组倍率设置；仅保留日志清理、渠道监控（响应超时/自动启停）、通用（聊天链接、重试、按令牌统计、近似 token）等非计费项；`getOptions` 改为只回写白名单键。
- **EditToken**：删除额度输入框、无限额度按钮与额度提示；`remain_quota/unlimited_quota` 不再由用户配置（后端新路径本就强制无限、忽略额度），移除相关 state/import。
- **EditUser**：删除额度输入框、“发放额度”卡片（旧 `POST /api/topup` 已下线）、当前额度展示与 `grantQuota` 逻辑及未用 import。
- **helpers/render.js**：删除已无引用的 `renderQuota`/`renderQuotaWithPrompt`（费用展示函数）。

### §9.1 布局基础
- `index.css` 新增 `.table-scroll`（横向滚动容器 + `-webkit-overflow-scrolling`），Users/Tokens/Logs 三张大表包裹入内，宽表在窄屏横向滚动而不撑破页面。
- 修正 `colSpan`（去列后 Users=7、Tokens=6、Logs=9）。

### i18n 文案清理（§13）
- 重写 `dashboard.*`（zh/en，嵌套对象）。新增按用量口径的键：`token.table.request_count`、`token.sort.by_request_count`、`user.table.usage_entry`、`log.total_tokens`。
- 脚本化删除 **46** 个既无代码引用、又含“额度/倍率/充值/余额/费用/价格/货币/金额/$”的费用键；对其余仍被引用的 5 处文案改写去费用词（渠道详情、分组添加、消费日志、按令牌统计）。复核：引用键中已无费用文案命中。

### 测试与验收
- `CI=false npm run build`（`web/default`）：exit 0，Dashboard 新代码 0 告警；仅存量 react-hooks/no-unused-vars 提示（非本阶段引入，`Header/Segment` 于旧代码即未用）。
- 后端不受本阶段影响仍全绿：`go build ./...`、`go vet`、`gofmt -l`（空）、`go test`（排除存量红 `common/image`）12 包 ok（新增 `model/migration` 集成测试包）；`scripts/smoke/run_smoke.sh` `pass=45 fail=0`（本阶段补 `/v1/models` 列表 + `/v1/models/:model` 检索 3 项）。
- i18n JSON 双份 `json.load` 校验通过，嵌套键解析正确。


---

## §13 终局验收清单

逐项对照实施文档 §13。**证据键**：单测=Go 单元测试；冒烟=`scripts/smoke/run_smoke.sh`(151/0，阶段 10 口径)；迁移矩阵=`scripts/smoke/migration_matrix.sh`(真实二进制启动路径)；迁移集成=`model/migration/integration_test.go`。

- [x] 新请求路径没有额度预扣/扣除/退款/额度不足判断 — 阶段 2（`relay/billing/billing.go` 删除、预扣/退款/不足拒绝全部移除）；冒烟 “relay 200 with zero quota”、“user/token/channel 旧列 untouched”。
- [x] 新请求路径没有模型/分组倍率计费 — 阶段 2（`billingratio` 仅保留名称枚举用途，不参与计费）。
- [x] 所有成功生成请求都有 usage 收尾路径 — 阶段 3/4（recorder 落 `usage_records`）；冒烟 34 条 success 行、全部 `usage_source=reported`、`cache_usage_status` 计数 **9 reported / 25 not_reported**（9+25=34；阶段 10 口径；直通与跨协议格子各自走自己的收尾阶梯）。34 = 阶段 8 的 22 + 16h 的 8 格（16h-1..6、16h-8、16h-9）+ 16i 的 2 格（16i-1、16i-2）+ 16j 的 2 格（custom 工具，非流式 + 流式）；16h-7 是 `safe` 策略下的具名 400，codec 层 4xx 不写用量行，因此对计数中性——这个精确的等号就是"被拒绝的请求没有偷偷落一行"的证据。
- [x] usage 写入按 `request_id` 幂等 — `InsertUsageRecordOnce` + `usage_records.request_id UNIQUE`；迁移集成测试对三种方言断言“同 request_id 二次写入被拒”。阶段 8 起唯一例外：已有 `error` 行可被后到的 `success`/`partial` 覆盖（反之不可），使重试成功的请求不会留下首次尝试的失败为终态；单测覆盖两向与并发。
- [x] 渠道选择不把请求发给不能服务该客户端协议的渠道 — 阶段 8（决策 ②）：上游协议进入内存缓存与库路径，先按能力过滤候选再算优先级档位；`ErrNoChannelForProtocol` 与 `ErrChannelNotFound` 可区分。`model/channel_select_test.go` 4 例。
- [x] 用户 Token 汇总可查询 — `/api/usage/{summary,trend,by-user,by-model,by-channel,by-token}` + `/self` 变体；冒烟 `/api/usage/summary` 三指标校验；Dashboard 消费（阶段 6）。
- [x] `/v1/chat/completions` 正常 — 冒烟 relay 200 + body（旧流式路径不回归）。
- [x] `/v1/responses` 正常 — 冒烟 responses 非流式/拒绝项/流式 typed SSE/reported usage。
- [x] `/v1/messages` 正常 — 冒烟 messages 200/信封/usage/流式/stop_reason 映射/x-api-key 鉴权/冲突 401/缺 max_tokens 400。
- [x] `/v1/models` 正常 — 本阶段新增冒烟 “/v1/models 200 + list envelope + /v1/models/:model retrieve”。
- [x] 三种协议流式错误不被吞掉 — `relay/sse` 状态机 `UpstreamErrorNotSwallowed`、`MalformedChunkReturnsError`、`PartialOnAbortAndNoDone`；controller 错误分支 `stream.Error` 终局。
- [x] 工具参数增量不丢失或串台 — `TestState_ToolArgsGroupedByIndex` + `stream_test` 按 index 分组、不拼接。
- [x] Prompt Caching 字段被正确归一化 — 阶段 5（`NormalizeUsage`：read/write、nil→not_reported、ptr-0→reported、非法→invalid 保原值）。
- [x] 缓存命中率按 Token 加权 — `usageAggSelect` `Σcache_read/Σreported input`（仅 reported 行进分子分母）；冒烟 `cache_hit_rate=249/321`、覆盖率 `9/34`（阶段 10 口径）。
- [x] 未报告缓存不显示为 0% 命中 — 聚合分母 0→null，前端 `fmtRate(null)=“—”`；冒烟 25 条 not_reported + Dashboard “未上报缓存数据” 提示 + 覆盖率始终展示。同协议直通格子里的 `input_tokens_details.cached_tokens` 与跨协议格子走同一条 `NormalizeUsage`，不存在“直通路径绕过缓存归一化”的旁路。阶段 9 的两格分工正好是这条不变量的双向证据：16i-1 落 Anthropic 上游（其回答总带 `cache_read_input_tokens`），同时抬高命中分子与 reported 分母；16i-2 落 Responses 上游且未带缓存标记，只进 not_reported——同样"走上游并成功"，缓存归类却由上游报了什么决定，与走哪条协议路径无关。阶段 9 的 reported 分母由 7 抬到 9：多出的两格是 16h-6（非流式）与 16i-1（流式），二者都落 Anthropic 上游、各报 40，分子因此 +80、分母 +100。阶段 10 的 16j 两格仍走 chat 上游、不带缓存标记，因此只把 not_reported 由 23 抬到 25，命中分子与 reported 分母都不动。
- [x] Dashboard 显示命中率和覆盖率 — 阶段 6 三指标卡（命中率/调用命中率/覆盖率）。
- [x] 前端无余额、充值、倍率和费用文案 — 阶段 6 删除 46 个无引用费用键、改写 5 处残留文案、移除 `renderQuota*`；脚本复测引用键中费用词命中为 0。
- [x] 数据库迁移可重复执行 / 有幂等保护 — `migration.Migrate` 跳过已应用版本；迁移集成测试连跑 3 次 bookkeeping 恒为 {1,2} 不重复；矩阵重启后 `schema_migrations=1,2`。
- [x] 回滚步骤在测试环境验证 — 迁移矩阵：drop 主/日志库 → 从备份 restore → 重启二进制成功、计数复原（PG、MySQL 均 11/11）。
- [x] 旧版 SQLite / MySQL / PostgreSQL 主库与独立日志库迁移均验证 — 矩阵对 **PostgreSQL 主库 + 独立日志库** 与 **MySQL 主库 + 独立日志库** 各 11/11；SQLite 由 `TestMigrationSuite_SQLite`（默认随 `go test` 执行）与 42→45 项端到端冒烟覆盖其主库升级。（说明：one-api 中 `LOG_SQL_DSN` 为空即与主库同文件、非 URL DSN 一律走 MySQL，故“独立日志库”仅在 MySQL/PostgreSQL 拓扑下成立并已在该两引擎验证。）
- [x] 迁移中断重试、重复执行、备份恢复均验证 — 集成测试重复 Migrate x3（中断重试等价）；矩阵备份(drop+restore)恢复；重启幂等。
- [x] 迁移报告确认用户/令牌/渠道/权限(角色)/配置/历史日志未丢失 — `reportMigrationCheck` 预检上报 current/target/pending/missing/warnings；矩阵对 users/tokens/channels/logs/options 计数在“升级后→重启→回滚恢复”三个阶段逐一相等。

**总门禁**：`go build ./...` ✅ · `go vet ./...` ✅ 空（须用干净的 `GOCACHE`，见下）· `gofmt -l $(git ls-files '*.go')` ✅ 空 · `go test $(go list ./... | grep -v common/image)` ✅ · 端到端冒烟 `pass=151 fail=0` ✅（阶段 10 口径；检查清单建立时为 45/0）· 迁移矩阵 PostgreSQL `11/11` ✅ / MySQL `11/11` ✅ · 前端 `npm run build` exit 0（新代码 0 告警）✅。

**`go vet` 的一处存量红（阶段 9 发现，已修复）**：本机 Go 1.26.8 工具链下 `go vet ./...` 报一条 `common/message/email.go:67:44: address format "%s:%d" does not work with IPv6`（`hostport` 分析器）。该文件最后一次改动是 `ececfe5`（2025-02-02，上游），早于本分支全部提交，因此这是工具链版本带来的存量发现，而不是阶段 9 的回归。修复单独成提交：三处 `fmt.Sprintf("%s:%d", host, port)` 收成一处 `net.JoinHostPort(host, strconv.Itoa(port))`。原写法在 IPv6 字面量主机上会拼出非法地址（`::1:25`），`JoinHostPort` 会补方括号；对普通主机名输出逐字相同，故对现有部署无行为变更。修复后全仓 `go vet ./...` 为空。

**附带发现，影响验证方法本身**：`go vet` 的结果按包缓存在 `GOCACHE` 里，而本仓约定的 `GOCACHE=$PWD/.gocache` 中存着一份**过期的"干净"结果**——直接跑 `go vet ./...` 会重放它而**不报**这个真实缺陷；同样的源、同样的工具链，换一个空缓存目录或加 `-a` 才复现。所以"用仓内缓存跑 vet 得到空输出"不能当作证据，本阶段此前的 `go vet` 检查正是这样被掩盖的（`relay/model/controller` 三个包在干净缓存下确认仍为空，结论不变）。要点名验证 vet 时应另用一个干净的 `GOCACHE` 或加 `-a`。

**修订说明**：更早一版此条曾无条件写 `go vet ✅`，与工具链实际输出不符；阶段 9 先改为如实记录并标注"本次不修"，随后按用户要求单独提交修复，并补上上面那条缓存陷阱。

---

## 阶段 7：前端第二轮现代化改造（参考 SHUAI API 深色开发者控制台）

依据 `docs/frontend-modernization-plan.md` 与 `docs/frontend-modernization-implementation.md` 执行前端体验现代化提升。

### 核心实施内容

1. **F0（基线与请求安全）**：
   - 记录 `git status` 与 `npm test` 存量状态。
   - `web/default/src/helpers/api.js`：Axios response interceptor 在 `showError` 后补充 `return Promise.reject(error)`，使页面能准确捕获 401、403 与网络中断。
   - 权限守卫：`PrivateRoute.js` 扩展支持 `requiredRole`，普通用户访问受限路由自动回退并保留 `state.from`。
   - 路由元数据：新建 `src/routes/routeMeta.js`，集中维护标题、权限分级和导航归属。
   - 事实层服务：新建 `src/services/usage.js`，严格区分管理员与 self 范围，保留可空指标 `null` 规范。

2. **F1（设计变量与三套布局）**：
   - 建立 `src/styles/theme.css`：定义 `--app-bg` (#141414)、`--app-canvas`、`--app-surface`、细边框体系与低圆角规范。
   - 布局解耦：
     - `ConsoleLayout`（固定 Topbar + 桌面侧边栏 / 移动端抽屉 + 主滚动区域）。
     - `PublicLayout`（首页/关于页等公共页，保留轻量页脚）。
     - `AuthLayout`（登录/注册/重置密码窄屏居中容器）。
   - 样式治理：清理 `index.css` 侵入式 `body padding-top` 与隐藏滚动条代码，恢复可见深色滚动条。

3. **F2（统计分析与深链接）**：
   - 抽取统计共享组件：`MetricCard`、`RateMetric`（null 恒为“—”）、`ChartPanel`、`FilterBar`、`DataQualityNotice`、`PageState`。
   - 重构 `Dashboard/index.js`，清理全局外溢样式 `Dashboard.css`。
   - 新建 `src/pages/Usage/index.js`：支持多时间范围过滤、`user_id` 管理员精准筛选、URL 深链接自动同步，以及模型/用户/渠道三维度细分排行 Tab。

4. **F3 / F4（页面规范与 i18n/a11y）**：
   - `UsersTable`：修复 `/console/usage?user_id=...` 死链，更新为指向 `/usage?user_id=...`，并在 `App.js` 中保留兼容重定向路由。
   - 清理 Plaza 等页面对 `Dashboard.css` 的非预期样式依赖。
   - 完善中英文翻译字典，更新 Topbar/Sidebar 导航术语。

5. **F5（验证与验收）**：
   - 前端构建验证：`npm run build` 成功通过（exit 0），无新增 warning。
   - 容器热更新验证：打包最新 Docker 镜像并部署，验证主站根路径、`/usage` 路由与 API 正常加载。

---

## 阶段 8：跨协议矩阵（对齐 new-api）

### 完成时间
2026-10-10。分支 `feat/cross-protocol-matrix` 共 12 个提交：4 个前置（`254ddcf`/`6bfdbe2` 工具调用修复、`29f27bf` 冒烟断言、`b587d14` CLAUDE.md）+ 本阶段的 8 个（A1 `0bc836a` … A7 `12a4772`）。

### 立项缘由
一次工具调用缺陷修复（`254ddcf`、`6bfdbe2`）验收后，核对参考实现 new-api 时列出四处**有意偏离**，用户要求「全部都按 new-api 的来」。其中两处是独立子系统：① `/v1/responses` 与 `/v1/messages` 只接受 OpenAI 兼容上游（`upstream_protocol_unsupported` 在 new-api 全仓零命中）；② 服务端工具（hosted tools）硬拒，而 new-api 有完整支持。本阶段交付 ①，②（hosted tools）留作阶段 9——**阶段 9 已交付，见下**。

### 前提修正
立项时的三个假设被逐条推翻，记在此处以免重蹈：

1. **闸门不是唯一障碍。** `DoRequestHelper` 只做 URL + 头 + 发请求，不做转换；协议路径是 `json.Marshal(normalized)` 后直接交给 `adaptor.DoRequest`。所以拆掉闸门得到的是「把 chat 请求体 POST 到 Anthropic 的 `/v1/messages`」，是坏请求而非可用转换。响应侧同样硬编码 OpenAI（`readOpenAIUsage` 只解 `openai.TextResponse`，`relayProtocolStream` 只调 `sse.ParseChunk`），原生 SSE 会解析成空 chunk，文本/finish_reason/usage 全部静默丢失。
2. **`InsertUsageRecordOnce` 是首写生效。** 配合 `Relay` 的重试循环，第 1 次尝试写的 error 行会挡掉第 2 次成功写的 success 行——请求成功却记为 error。既存缺陷，矩阵会放大它（见 A2）。
3. **400 不重试。** `shouldRetry` 对 400 返回 false，所以「渠道不支持该客户端协议」的 400 不会触发换渠道。这正是决策 ② 必须在**选渠道阶段**过滤而非靠重试兜底的原因。

### 已确定的范围与决策

| 维度 | 结论 |
| --- | --- |
| 上游协议 | `openai-chat`（已支持）、`anthropic-messages`（adaptor 有双向转换，但从协议入口够不着）、`openai-responses`（全新） |
| 客户端协议 | 不变，三种 |
| 同协议配对 | **直通**：不解析、不重渲染，原生字节转发 |
| hosted tools | 全部类型（阶段 9；**已于阶段 9 交付**） |
| ① 重试与用量记录 | **终态可覆盖 error 行**：同一 `request_id` 已有 error 行时，允许 success/partial 覆盖 |
| ② 分组混用协议 | **选择渠道时按能力过滤** |
| ③ `upstream_protocol` 界面 | **先只做后端**，用 API/配置设置；`web/default` 界面后续阶段 |

### 矩阵落点（交付后）

| ↓ 上游 / → 客户端 | chat/completions | responses | messages |
| --- | --- | --- | --- |
| openai-chat | chat 路径（raw 快路径） | codec 翻译 | codec 翻译 |
| anthropic-messages | chat 路径（adaptor 转换） | codec 翻译 | 直通 |
| openai-responses | chat 路径（`text_responses.go`） | 直通 | codec 翻译 |

直通格子的取舍是这一阶段的支点：三个同协议配对**零新代码**地带过 `cache_control`、`thinking`、hosted tools 与未知字段；代价是六个跨协议格子必然有损，于是唯一剩下的问题变成「有损是否具名」。阶段 9 给出了这个问题的答案：hosted tools 的每一处丢损都有具名诊断码，且每次丢弃都写一行面向运维的日志（见「阶段 9」）。`cache_control` 与 `thinking` 仍只在直通格子里无损。

### 逐提交记录

| 提交 | 内容 |
| --- | --- |
| `0bc836a` | **A1** 上游协议概念：`relay/upstream`（`Protocol` + `Resolve`/`CanServe`/`Passthrough`/`Valid`）；`ChannelConfig.UpstreamProtocol`（在既有 JSON blob 内，**零 schema 变更、无迁移条目**）；保存时白名单校验。无消费者，纯地基。 |
| `1babfcc` | **A2** 决策 ①：`InsertUsageRecordOnce` 允许 success/partial 覆盖既有 error 行，反向不覆盖；事务内 UPDATE，其余仍幂等跳过。 |
| `1033380` | **A3** 决策 ②：协议进入内存缓存（`ChannelEntry{Channel, Protocol}`）与库路径；**先过滤后算优先级档位**——顺序反过来会让被过滤空的档位走到 `rand.Intn(0)` panic。顺带修正 A1 的缺陷：原矩阵对 `unsupported` 上游全列 false，会把 Gemini/Baidu 渠道从 `/v1/chat/completions` 上也一并摘掉。 |
| `b3488f7` | **A4** 同协议直通 + 闸门替换：`relay/controller/protocol_passthrough.go`，`relayProtocolHelper` 中插在 `entry.Parse` 之前。请求投影是**定点补丁**（`map[string]json.RawMessage`，只换 `model` 并合并强制 prompt），`sse.Reader`/`Writer.Raw` 镜像。顺带修 `anthropic-beta` 头由覆盖改为合并。 |
| `55ff412` | **A5** 编解码器泛化：`relay/controller/protocol_codec.go`，`openaiChatCodec` 保持逐字节等价（有差分测试），`anthropicChatCodec` 复用 adaptor 的转换函数而不重写。顺带把 `Resolve` 对 `AwsClaude` 的判定从 `anthropic-messages` 改为 `unsupported`（见「已知修正」）。 |
| `a384d0b` | **A6a** `relay/adaptor/openai_responses/`：与 sink 无关的 chat ⇄ Responses 转换库（请求、非流式、流式），无调用方。 |
| `e0ff956` | **A6b** 接线：chat 路径分支 + `openaiResponsesCodec`；两条路共用同一个读流器，客户端看到的流与落库的事实同源。 |
| `12a4772` | **A7** Responses 同协议直通：矩阵最后一格。`passthroughProtocols` 表取代硬编码 switch 与 `upstream_protocol_not_implemented`；`projectNativeRequest`、`mirrorUpstreamStream`、`readUpstreamBody`/`writeUpstreamBody`/`closeUpstreamBody` 抽成两份协议共用；`responsesMirror` 从终态信封的 `response.usage` 取用量。 |

提交拆分沿用「地基/接线分离」：A1 与 A6 各自可独立回滚（A6 拆成 a/b，与计划的单提交有别，理由与 A1 同）。迁移/协议/UI 改动各自独立提交。

### 已知修正与已知有损

- **`AwsClaude` 判定修正（`55ff412`）**：Bedrock 的 body 形状像 Messages，但走 SDK `InvokeModel` 加自己的 envelope。判成 `anthropic-messages` 会让它通过 `CanServe` 与 A3 的渠道过滤，然后在 `aws.DoRequest` 返回 `(nil, nil)` 上 nil 解引用——500 而不是具名 400。现在落 `unsupported`，它照旧在 `/v1/chat/completions` 上工作。这也是「apitype 推导」需要显式例外表的证据。
- **`thinking` 在跨协议格子丢失**：OpenAI 归一化形态里没有槽位（与既有 chat pivot 同因）。冒烟用「输出只含 text 块内容」把这条损失钉住，不让它无声消失。`cache_control` 同理——所以直通才是需求的大头。
- **`validateChannelConfig` 只管名字**：它校验 `upstream_protocol` 是不是已知名字，不管它与渠道族是否相容。管理员可以在 Anthropic/Gemini/Bedrock 渠道上声明 `openai-responses`，然后收到一个按另一套协议拼出来的 URL 和目标不认识的请求体（静默失败）。`e0ff956` 新增 `responsesUpstreamSupported`，在 chat 分支与 `upstreamCodecFor` 两处按名字拒绝（400 `upstream_protocol_unsupported`）；**相容性检查仍是逐处判断，不是一张表**——阶段 9 应把它收成一处。
- **`stream_options` 不下发 Responses 上游**：Responses 无此字段且总是报 usage，拒绝反而会打断默认设置它的客户端。

### 用量不变量（直通路径同样适用）

- 直通流与大水箱路径**落同一套 reported / estimated / not_reported 三态**：上游报了就用（`reported`），什么都没报但流出过文本就本地估算（`estimated`），既没报又没流出任何东西则返回 nil 只记状态——上游发 `response.failed` 而没有任何 output 时，不该伪造一行「prompt+0」的估算。
- Anthropic 的 `message_delta.usage.output_tokens` 是**累计值，赋值不是 `+=`**；`message_start` 的缓存字段取「最新非 nil」而非累加。nil 缓存字段保持 nil，**绝不归零**。
- 协议路径的 `bodyErr` 为 4xx 时不写 usage 行（请求从未发出），5xx 仍按既有规则记一行 error。

### 已知偏离（遗留，非本阶段引入）

- **预检级拒绝会写 error 行。** `relay/controller/protocol.go` 的能力不符分支（`upstream_protocol_unsupported`）与 A4 的直通投影失败路径都会 `recordUsage(..., UsageStatusError)`，而本文档 §「明确限制（阶段 4 收口）」记的规则是「未真正发起生成则不写 error 行」。两者冲突，属 A4 引入直通时就存在的行为。**有意留到文档收口阶段处理，不在 A7 扩大范围**——修它要动 `recordUsage` 的调用点分布，与矩阵无关。
- 直通路径上 `applyModelConfigOverrides` 无法照搬（它操作 `GeneralOpenAIRequest`）：当前行为是投影 `model` 与强制 prompt，配置了却无法在原生 body 上施加的覆盖项**具名拒绝**（`model_config_override_needs_normalization`），不静默忽略。`context_window` 的原生强制暂列为限制。

### 测试与验收

- **单测**：`relay/upstream/protocol_test.go`（`Resolve` 优先级、`CanServe` 全矩阵、非法配置拒绝）；`model/channel_select_test.go`（按协议过滤、过滤后才算优先级档位、无约束客户端仍能选中未翻译渠道、错误种类可区分）；`model/usage_test.go`（error→success 覆盖、反向不覆盖、并发仍只有一行）；`relay/controller/protocol_passthrough_test.go`（Messages 与 Responses 各自的逐字节投影保留、只改 `model`、强制 prompt 替换、覆盖项拒绝、缺 `model` 拒绝，以及两套 mirror 的 reported/estimated/nil 阶梯、缓存指针语义、终态判定）；`relay/controller/protocol_codec_test.go`（`openaiChatCodec` 与历史内联循环的差分等价、Anthropic codec 与 adaptor 产物一致、Responses codec 七例）；`relay/adaptor/openai_responses/`（请求往返与 14 项拒绝表、用量指针语义、finish_reason 映射、流式稠密 tool index、`OutputText` 计数范围）。
- **冒烟**：`mock_upstream.py` 增加 `/v1/messages`（Anthropic 非流式 + 真实 SSE 序列，含 `message_start` 缓存字段与 `thinking` block）与 `/v1/responses`（非流式 + `response.*` SSE 序列，工具格子参数分片故意交错）；新增 16e/16f/16g 三组格子覆盖全部九个矩阵位置与能力拒绝格子。硬编码计数**刻意更新**并保持精确相等（非 `>=`）：`RESULT pass=109 fail=0`。
- **命令**：`go build ./...` ✅ · `go vet` ✅ · `gofmt -l $(git ls-files '*.go')` 空 ✅ · `go test $(go list ./... | grep -v common/image)` ✅ · `bash scripts/smoke/run_smoke.sh` → `pass=109 fail=0` ✅。`common/image` 的 jpeg 失败是 §0.3 既存红线，未折进任何提交。

### 剩余限制（阶段 9 输入）

1. **hosted tools 跨协议**：`messages`/`responses` → `openai-chat` 时 Chat 协议没有 hosted tool 的位置，诚实结局是**具名拒绝**（`hosted_tool_unsupported_by_upstream`）而非现在的 debug 丢弃；`messages` ↔ `responses` 映射重叠子集，其余按名字拒绝。需要 `protocolStream` 增加 hosted 逃生舱（现有 `Chunk`/`Finish`/`Error` 无处安放），以及 `anthropic` 结构体扩 `server_tool_use`/`web_search_tool_result`/`citations`/`thinking`。
   - **（阶段 9 已交付，含三处修正）** 落地结果与这条设想有出入，记下来免得日后照旧文重做：① 具名拒绝不是唯一结局，也**不是默认结局**——默认 `allow` 是丢弃 + 具名诊断 + 请求成功，具名 400 只在 `safe`/`strict` 下出现；诊断码定为 `unsupported_hosted_tool`（**不是** `hosted_tool_unsupported_by_upstream`）。② `protocolStream` 的 hosted 逃生舱按设想加了（`Hosted(ev)` + `LossOf()`），`anthropic` 结构体也按设想扩了字段。③ `messages`/`responses` 的**响应方向不对等**：`mcp_call` 双向都能活，`web_search_call` 对 Claude 客户端只能整件丢弃并具名 `web_search_response_unrepresentable`——Claude 的结果块必须携带只有 Anthropic 能签发的加密搜索结果，网关重建不出来，参考实现在此处同样是丢弃。
2. **跨协议配对的流式 hosted 交错**预期要**每个配对一座桥**，而不是一个通用机制。
   - **（阶段 9 已交付，设想成立）** 两座桥（`claudeHostedBridge`、`responsesHostedBridge`）确实各写各的，且都是"坐在转换器前面的纯观察者”：认领属于托管产物的帧、吞掉它们、产出中立事件，未认领的帧原样交给转换器。事实上只需要两座——三个 identity 格子走直通不经过桥，两个 chat 上游格子根本不会产出托管产物（`web_search_options` 是一个开关，上游用普通 chat 流回答），所以 3×3 里需要桥的只有 messages ↔ responses 这一对方向。
3. **`upstream_protocol` 与渠道族的相容性检查**应收成一处表。
4. **预检级拒绝的 usage 行**规则需要与 §「明确限制（阶段 4 收口）」统一（见「已知偏离」）。
5. **前端 `upstream_protocol` 编辑界面**（决策 ③ 的后半）。

---

## 阶段 9：hosted tools（服务端工具，对齐 new-api）

### 完成时间
2026-10-10。分支 `feat/cross-protocol-matrix` 上 8 个提交：`a530df0`、`5cf1286`、`a500454`、`337e55a`、`6a6ed3e`、`c5ebd4b`、`7267871` + 本文档提交。

### 立项缘由
阶段 8 的「剩余限制」第 1、2 条就是本阶段。要解决的原本是两块语义相反的补丁：`relay/protocol/responses.go` **丢弃** hosted 工具声明（注释写明 Codex 默认带 `web_search`，硬拒会让它每个请求都失败），`relay/protocol/messages.go` **硬拒**同样内容（4xx）。阶段 9 让托管声明走真正的链路，并把"哪一格丢、哪一格转"从散落的补丁变成一条**写下来的、可配置的**规则。

### 前提修正
1. **托管工具不是"多一个字段"，而是第三种协议语义。** 它在 Claude 是"两个块 + 一个 id 关联"（`server_tool_use` 开、`web_search_tool_result` 结果），在 Responses 是"一个 item 走 added/progress/done 三段"。中立形态必须能表达"一件产物分阶段到达"，否则流式无法把 Claude 的两块折成 Responses 的一个 item。→ `HostedStreamEvent{Phase start|update|done}`。
2. **`GeneralOpenAIRequest` 一个类型就够。** new-api 把 `Set` 与请求并排传，是因为它的注册表按格式切换**请求类型**；这里四个格子都汇流到同一个中间结构，所以中立数据直接挂在它上面（`json:"-"`），`Parse`/`RequestBody`/`Render`/`protocolStream` 签名全部不变。
   - **（阶段 10 部分作废）** "签名全部不变"到此为止：custom 工具需要流式渲染器在**第一个 chunk 之前**就知道哪些名字原本是自由格式的，而那一刻还没有任何响应载体可嫁接，所以 `streamFactory` 加了一个参数。`Parse`/`RequestBody`/`Render`/`protocolStream` 确实仍然没动。见实施文档 §6.8 与本日志的阶段 10。
3. **`relay/protocol` 不能被拖着依赖 adaptor。** 它今天只依赖 `relay/model` + `relay/sse`。若把 kind 表放进一个依赖 adaptor 的新包，`relay/protocol` 为了查表就会间接依赖 `relay/adaptor/anthropic`。→ 纯类型/表/策略放 `relay/model/hosted.go`，需要 adaptor 的挂载与桥接放 `relay/controller/`。

### 已确定的范围与决策

| 维度 | 结论 |
| --- | --- |
| 丢弃策略 | **照 new-api**：默认 `allow` = 丢弃 + 面向运维的具名诊断 + 请求成功，客户端不被告知；按渠道可设 `allow`/`safe`/`strict`。**响应与流式阶段任何策略下都不拒绝** |
| 拆分 | **两段**：B1 = 中立类型 + 请求侧抽取/挂载 + 非流式响应建模 + 策略；B2 = 流式桥 |
| 工具广度 | **照 new-api 的窄表**：只有 `web_search` 与 `mcp` 跨协议映射，其余走策略 |
| chat 上游的 web_search | 在 `GeneralOpenAIRequest` 增加 `web_search_options` 字段并映射过去 |
| B1 历史范围 | **包含**：客户端回放的托管轮次在 B1 内建模，否则 messages↔responses 只有单轮可用 |
| `compatible` 上游 | **发** `web_search_options`（它按定义就是 chat 兼容端点，该字段是标准 Chat 字段） |

### 关键设计

**载体**：中立数据挂在三个既有归一化类型上，用 `json:"-"` 字段——`GeneralOpenAIRequest.Hosted`、`openai.TextResponse.Hosted`、`sse.Chunk.Hosted`。守卫：`sse.ParseChunk` 永不填充 `Hosted`、`StreamState.Absorb` 忽略它，所以托管流事件只能由 codec 写，不可能经 JSON 往返意外产生。

**窄表**：`web_search` 与 `mcp` 之外全部是 `native`。这不是省事而是照抄参考实现——new-api 的 `code_execution` 出站直接返回 `""`，理由是 Responses 没有 `container_id` 就无法表达 `code_interpreter_call`；发明一个映射比具名记一次丢损更糟。

**策略**：`ChannelConfig.tool_loss_policy` 落在既有 `channels.config` JSON blob，**零 schema 变更、无迁移条目**（先例：`upstream_protocol`）。保存时白名单校验，因为一个拼错的策略名静默退回 `allow`，等于运维以为自己设了 `strict` 而实际没有。

**约定例外（本阶段唯一放宽的既有约定）**：§1 第 7 条"不要静默丢弃"在托管工具上有一条记在案的例外，两端都有界——请求阶段在 `safe`/`strict` 下变成具名 400（`hosted_tool_loss`），而**每一个阶段**（`allow` 下同样）都为每次丢损写一行 `logger.SysWarnf` 落进 `oneapi.log`：`channel %d hosted_tool_loss: phase=%s code=%s path=%s %s`，`phase` ∈ `request`/`response`/`stream`。"客户端不知道"是照参考实现有意为之；"运维不知道"不是。本阶段只放宽这一条，没有顺带放宽任何其它约定。

### 逐提交记录

| 提交 | 内容 |
| --- | --- |
| `a530df0` | **B1a** `relay/model/hosted.go`：中立类型（`HostedSet`/`HostedDefinition`/`HostedHistoryItem`/`HostedOutputItem`/`HostedStreamEvent`）、kind 表、诊断码、策略（`ValidToolLossPolicy`/`RejectLoss`）。无行为变更。 |
| `5cf1286` | **B1b** 目标侧挂载：`controller/hosted_attach.go` 的 `attachChat`、`adaptor/openai_responses/request.go` 的 `attachHostedTools`、`adaptor/anthropic/main.go` 的 `hostedClaudeTools`；三个 codec 的 `RequestBody` 接线，随即 `logHostedLosses(..., "request", ...)` + `RejectLoss`。 |
| `a500454` | **B1c** 解析器：`parseMessagesTools`/`parseResponsesTools` 不再丢弃或 4xx，改为抽成托管声明；块/item switch 抽成托管历史；`tool_choice` 里被强制的托管 kind 记进 `ForcedChoice`；非流式渲染 `renderMessagesHosted`/`renderResponsesHosted`。 |
| `337e55a` | **B1d** 非流式响应建模（两个 codec 的 `NonStreamResponse` 抽取产物写进 `TextResponse.Hosted`）、`ChannelConfig.ToolLossPolicy` + 校验、chat 客户端的 `web_search_options` 两个小口子。 |
| `6a6ed3e` | **B1e** 单测 5 个文件 + 改靶子 3 处 + 冒烟 16h-1..16h-9。 |
| `c5ebd4b` | **B2a** 流式桥：`sse.Chunk.Hosted`、`protocolStream.Hosted(ev)`/`LossOf()`、`controller/hosted_bridge.go` 两座桥、`stream_responses.go`/`stream_messages.go` 的 `Hosted` 渲染器。 |
| `7267871` | **B2b** 单测 2 个文件 + 冒烟 16i-1/16i-2 + `messages_stream` 的托管夹具。 |

迁移提交：**无**（策略在 `channels.config` JSON blob 里）。

### 已知修正

- **计划稿 §4 的响应方向表是错的。** 计划稿写 `messages` 客户端 ← `openai-responses` 上游的响应方向是 `web_search_call` → `server_tool_use` + `web_search_tool_result`。实际不可行，实现为**整件丢弃并具名** `web_search_response_unrepresentable`：Claude 的结果块必须携带只有 Anthropic 能签发的加密搜索结果，网关重建不出来，而没有结果的调用块又不是 API 接受的形状。参考实现在此处同样是丢弃（`relaykit/relayconvert/internal/toolconv/response_artifacts.go:440`，诊断码与理由逐字相同）。**`mcp_call` 才是两个方向都能活下来的那一种。** 冒烟 16h-4 与 16i-2 从两个方向（非流式、流式）把这条钉住。
- **两个诊断码容易混为一谈，语义相反。** `web_search_result_continuation_lost` 是**请求阶段（历史回放）**的丢损（`adaptor/anthropic/main.go:121`、`adaptor/openai_responses/request.go:341`）：第二轮回放上一轮托管历史时，两种协议对"一条搜索记录"的定义不一致——Responses 的 `web_search_call` 只有 action 没有结果位，而 Anthropic 拒绝没有配对结果的 `server_tool_use`（用 `web_search_tool_result_error(unavailable)` 占位）。`web_search_response_unrepresentable` 是**响应阶段**的丢损。计划稿 §5 把两者写成了一条，已在本阶段更正。
- **`emitHostedBlock` 的串行化（`c5ebd4b` 内）。** 原渲染器把 text 块一直开着，托管块到达时才开新块，客户端会看到**两个块同时打开**——真实 API 从不这样。改为写入托管块前先关掉开着的 text 块，随后的文本开一个新块，这正是模型自己那一轮的样子（写了什么、搜了一次、又写了什么）。由 B2 的"严格串行"帧序列断言暴露出来。
- **`hosted_tool_streaming_unsupported` 被删除。** 它是 B1 期间"流式尚未桥接"的临时具名拒绝码。B2 桥接了每一个需要桥的格子，该常量随之删除；冒烟 16h-8 反过来断言它**不许回来**。

### 与参考实现的对应（逐条核对）

| 本项目 | new-api | 核对结果 |
| --- | --- | --- |
| `ToolLossAllow`/`Safe`/`Strict` = `allow`/`safe`/`strict`，空即 `allow`，仅请求阶段生效 | `ConversionLossPolicy*` + `convmeta.Options.ToolLossPolicy` | 逐字一致 |
| 拒绝码 `hosted_tool_loss`，400 | 同 | 一致 |
| `unsupported_hosted_tool`/`unsupported_search_limit`/`unsupported_domain_filter` | `toolconv/encode.go`、`channel/ali/web_search.go`、`channel/zhipu_4v/web_search.go` | 逐字一致 |
| `web_search_response_unrepresentable` | `toolconv/response_artifacts.go:440`（同样跳过渲染） | 逐字一致 |
| 窄表：只有 `web_search` 与 `mcp` 映射 | 同（`code_execution` 出站为空） | 一致 |

参考实现自己的注释把本阶段实现的三态语义说得最清楚，抄录在此作为对齐证据：
> The zero value uses the allow policy: conversion succeeds and every loss is returned as a diagnostic. safe/strict rejection is request-phase opt-in only; response and stream conversion never reject regardless of this field.

### 用量不变量（本阶段同样适用）

- 托管格子落同一套 reported / estimated / not_reported 三态，**与走哪条协议路径无关**。冒烟的两格正好构成双向证据：16i-1 落 Anthropic 上游（其回答总带 `cache_read_input_tokens`）→ 同时抬高命中分子与 reported 分母；16i-2 落 Responses 上游且无缓存标记 → 只进 not_reported。缓存归类由上游**报了什么**决定，不由路径决定。
- `safe` 策略下的具名 400 走 codec 层拒绝，不写 usage 行——所以 16h-7 对 §17 的计数是**中性**的，这正是那个精确等号的意义。

### 测试与验收

- **单测**：`relay/model/hosted_test.go`（`RejectLoss` 矩阵、三态策略）；`relay/protocol/{messages,responses}_hosted_test.go`（声明与历史抽取、`ForcedChoice`、未知块仍 4xx、**普通块输出与改动前逐字段一致**）；`relay/adaptor/anthropic/hosted_test.go` 与 `openai_responses/hosted_test.go`（产物提取与合并）；`relay/controller/protocol_codec_hosted_test.go`（三个目标侧挂载结果、`allow` 丢弃带诊断 / `safe` 返回 `hosted_tool_loss`）；`relay/controller/hosted_bridge_test.go`（Claude 闩住/吞帧/发射序列；Responses 三段式与无 id 寻址）；`relay/protocol/stream_hosted_test.go`（索引稠密、到达顺序、终态补 `incomplete`、严格串行帧序列）。
- **改靶子（改靶不删）**：`responses_test.go` 四个"托管工具被丢弃且不报错"的用例，行为已变成"被抽取进 `Hosted`"，断言搬进 `responses_hosted_test.go`；`openai_responses/request_test.go` 与 `stream_test.go` 的两处改名，因为原被当作"编程错误/unmapped"的托管条目现在有了归属。
- **冒烟**：`mock_upstream.py` 增加一对正交开关——`wants_hosted`（声明了托管工具吗）与 `runs_hosted`（额外要求请求带 `use-hosted-search`，声明使工具可用，请求才让上游真的去搜）。两者分开，才能让同样声明了 `web_search_20250305` 的直通格子（16c/16g）继续拿到逐字节保真的原信封，而只有真的要求搜索的格子才长出搜索产物。另给 `messages_stream` 补了托管分支（此前没有）。新增 11 格：16h-1..16h-9（B1）与 16i-1/16i-2（B2）。诊断用 `log_since`（按字节偏移截取）断言而非整文件 grep——同一批 code 会从数条路由发出，整文件 grep 会让后面的格子满足前面格子的断言，测试就失去了定位能力。
- **计数（刻意更新并保持精确相等，非 `>=`）**：§17/§18 `22→32`，§19 `15→23`，§20 `7→9`，§21 `cache_hit_rate 169/221→249/321`、`cache_coverage 7/22→9/32`、`not_reported_cache_calls 15→23`；`cache_write_input_tokens` 仍为 `0`（没有任何夹具上报过 cache 创建，这个 0 是"确实没有"而非"没问"）。`RESULT pass=109 → pass=142 fail=0`。
- **命令**：`go build ./...` ✅ · `gofmt -l $(git ls-files '*.go')` 空 ✅ · `go test $(go list ./... | grep -v common/image)` ✅ · `bash scripts/smoke/run_smoke.sh` → `pass=142 fail=0` ✅ · `go vet ./...` 空 ✅（须用干净的 `GOCACHE`：仓内 `.gocache` 存着过期的干净结果，会掩盖真实诊断，见 §13 的缓存陷阱）。`common/image` 的 jpeg 失败仍是 §0.3 既存红线，未折进任何提交。

### 剩余限制（阶段 10 输入）

1. **前端 `tool_loss_policy` 与 `upstream_protocol` 编辑界面**：两者都已在后端完整生效，但 `web/default` 无 UI（决策 ③ 的后半，与阶段 8 第 5 条合并）。
2. **`native` 类托管工具全部丢弃**：`computer_use`/`bash`/`text_editor`/`code_execution`/`file_search`/`image_generation`/`web_fetch`/`url_context` 目前只走策略、不做任何映射。若日后要支持，`code_execution` 会先撞上"Responses 缺 `container_id`"这堵墙——参考实现也停在这里，重新评估前应先确认那堵墙是否还在。
3. **`upstream_protocol` 与渠道族的相容性检查**（阶段 8 第 3 条）仍未收成一处表。
4. **预检级拒绝的 usage 行**规则仍未与 §「明确限制（阶段 4 收口）」统一（阶段 8 第 4 条）。

---

## 阶段 10：自由格式工具（Responses custom tools，对齐 new-api）

### 完成时间
2026-10-10。分支 `feat/cross-protocol-matrix` 上 6 个提交：`e5a5460`、`c825eac`、`077a959`、
`ceba1fc`、`75297ee`、`83f95be` + 本文档提交。另有一次生产部署（见下）。

### 立项缘由
**生产环境实证，不是推测。** 在腾讯云上海那台机器上用 root 的 test token（id=2）发一个把
`apply_patch` 声明为 `{"type":"custom"}` 的 `/v1/responses` 请求，`oneapi.log` 里出现：

```
hosted_tool_loss: phase=request code=unsupported_hosted_tool path=tools[0]
  OpenAI Chat Completions 无法表达托管工具 "apply_patch"（类型 custom）
```

请求 200 成功，客户端什么都不知道，只有 `function` 工具到了上游——于是 Codex 的 `apply_patch`
**永远调不到**。根因链：`parseResponsesTools` 把非 `function` 的 `type` 全送进托管分支 →
`HostedKindFromResponsesToolType` 默认返回 `HostedNative` → `attachChat` 对非 `web_search` 一律
`AddLoss` 后丢弃。

而这条路径是**活的**：服务器 7 个渠道**没有一个**配了 `upstream_protocol`，所以 Codex 流量必然走
跨协议路径（渠道 8 是 type 1/chat，模型列表里就有 `codex-auto-review`），没有直通可绕。

### 已确定的范围（用户已答）

| 维度 | 结论 |
| --- | --- |
| 总原则 | **一切都参考 new-api**：语义、线上形状、诊断码、严重级别、冲突规则、事件名一律照抄 |
| tool namespace | **不做**。继续完全忽略 `namespace` 字段 |
| 上游返回的 `custom_tool_call` | **不做**。只做"客户端声明 → 上游 → 回程还原"这个方向 |

### 关键设计

**降级而不是丢弃。** custom 工具在上游变成一个**只带一个必填字符串参数 `input` 的函数**
（`required:["input"]`、`additionalProperties:false`）。客户端的描述与语法（`format.type=="grammar"`
按 `lark`/`regex`/其它分别措辞）都折进 description——JSON Schema 只能说"是个字符串"，说不出
"必须是这个语法"。这样工具**仍然可调用**，这正是与"丢弃"的本质区别。

**回程按名字还原。** 请求侧把名字记在 `GeneralOpenAIRequest.CustomTools`（`json:"-"`），relay 在
`codec.NonStreamResponse` 与 `entry.Render` 之间嫁接到 `openai.TextResponse.CustomTools`（与 codec
嫁接 `Hosted` 同一位置同一手法，`Render` 签名因此不动）；流式则经 `streamFactory` 的新参数
传给渲染器。命中该名字的调用出 `custom_tool_call` 项，`input` 是**裸文本**、不带 `arguments` 键。

**两种严重级别，不可互换**（这是本阶段最容易被做错的地方）：

| 情形 | 码 | 级别 | 后果 |
| --- | --- | --- | --- |
| custom 降级成函数 | `custom_tool_as_function` | **warning** | `allow`/`safe` 放行，只有 `strict` 拒绝 |
| custom 与同名工具冲突 | `custom_tool_name_conflict` | **error** | 该 custom 工具被丢弃 |

`RejectLoss` **一行没改**：它早就是"strict 拒绝一切、其余只拒绝 error"，而它上方的注释（阶段 9 写的）
恰好预言了这里——"好让将来一个只影响呈现的损失能在 `safe` 下被报告而不使请求失败"。`NewHostedLoss`
写死 error、14 处调用点不动，另加 `NewPresentationLoss`。

**冲突判定必须在整份 `tools` 读完之后。** 目标协议只靠名字区分调用，同名的两件对它来说是同一件。
所以 custom 先暂存、循环后裁决，且**函数无论声明在前在后都赢**（预装所有函数名）——这也正是它不能
放进单趟循环的原因。被丢的名字不进留档集合，于是强制它的 `tool_choice` 自然落回既有分支、降级为
auto 并记 `forced_tool_choice_downgraded`："我们没送这个工具，就强制不了它"。

**流式不是改名**——见"已知修正"第 1 条。

### 逐提交记录

| 提交 | 内容 |
| --- | --- |
| `e5a5460` | **10a** `relay/model/hosted.go`：`ResponsesCustomToolNames` 类型（nil-safe `Has`/`Add`）、`DiagnosticWarning`、`NewPresentationLoss`、两个诊断码。无行为变更 |
| `c825eac` | **10b** 解析：`parseResponsesTools` 的 custom 分支 + 描述/schema 拼装、循环后冲突裁决、`parseResponsesToolChoice` 的 custom、`general.go.CustomTools` |
| `077a959` | **10c** 非流式还原：`TextResponse.CustomTools` 载体、relay 嫁接、`RenderResponses` 的两种 item、`customToolInputFromArguments` |
| `ceba1fc` | **10d** 流式还原：`streamFactory` 加参、`toolItemInProgress`、缓冲后单条 delta、两个事件名 |
| `75297ee` | **10e** 单测 14 例（新增 2 个文件） |
| `83f95be` | **10f** 冒烟 16j-1/16j-2 + `mock_upstream.py` 的 custom 夹具 |

迁移提交：**无**。

### 已知修正

- **流式不是"把 function_call 改成 custom_tool_call"那么简单。** 计划稿最初按改名来写，错了两处：
  ① new-api 对 custom **不发逐片段的 delta**——`{"input":"*** Beg` 这样的片段没法解包成裸文本，
  所以整个 input 缓冲到 item 关闭时**一次性**发出，逐片段的 `function_call_arguments.delta` 明确
  跳过；② in-progress 的 item 是 `input:""` 而不是 `arguments:""`，关闭时字段名也从 `arguments`
  换成 `input`（且空值保持 `""`，不套 `{}` 兜底——那里没有对象可退）。
- **`custom` 在两个入口本来就不一致。** `relay/protocol/messages.go` 早就把 `type:"custom"` **排除**
  在托管之外、当普通函数工具处理，只有 Responses 侧把它当 hosted。本阶段让 Responses 也走函数
  路径，两者因此一致。附带一提：我们**早就支持 `custom_tool_call` 的回放**（`responses.go` 的
  input 分支，有单测），却一直不支持它的**声明**——那条回放路径在跨协议下根本走不到。现在通了。
- **`streamFactory` 签名变了，CLAUDE.md 那句"签名全部不变"随之作废。** 已在 CLAUDE.md、实施文档
  §6.7 载体段与阶段 9 的"前提修正"第 2 条就地标注。`Parse`/`RequestBody`/`Render`/`protocolStream`
  确实仍然没动。

### 与参考实现的对应（逐条核对）

| 本项目 | new-api | 核对结果 |
| --- | --- | --- |
| custom → 单 `input` 字符串函数，`required:["input"]`、`additionalProperties:false` | `toolconv/encode.go:241-275` | 逐字一致（含 `CustomToolInputArgument = "input"`） |
| 描述 = 原描述 + freeform 约定 + 语法提示（lark/regex/其它） | `decodeResponsesCustomTool` | 逐字一致 |
| `custom_tool_as_function` = presentationLoss（Warning） | `encode.go:1657` | 一致 |
| `custom_tool_name_conflict` = semanticLoss（Error）+ 丢弃该工具 | `encode.go:345-352` | 一致 |
| 函数名预装、顺序无关；托管声明同名不算冲突 | `responsesCustomTools` | 一致 |
| 回程 `{type:"custom_tool_call", id, call_id, name, input}`，无 `arguments` | `to_oai_responses_resp.go:227-240` | 一致 |
| 解包失败原样回退（"绝不丢掉模型的输出"） | `customToolInputFromArguments` | 一致 |
| 流式 `custom_tool_call_input.delta`/`.done`，关闭时一次性发 | `to_oai_responses_stream_resp.go:688-698` | 一致 |
| `tool_choice {"type":"custom"}` → 具名 function choice | `responsesCustomToolChoice` | 一致（撞名被丢时除外，那里改成降级） |
| **传参机制**：new-api 挂在按请求的 `RelayInfo` 上 | 我们的 `Render`/`NewStream` 签名里没有请求对象 | **机制不同**（本仓用 `json:"-"` 载体 + `streamFactory` 参数），语义相同 |

### 测试与验收

- **单测 14 例**，两个新文件：`relay/protocol/custom_tool_test.go`（解析形状逐字段、缺 name 拒绝、
  tool_choice 转换、撞名负例含"函数声明在后仍赢"、严重级别、非流式解包与回退、流式单条 delta 与
  事件名、空输入不发 delta、以及"未声明时逐字回到旧行为"的回归）；
  `relay/controller/protocol_codec_custom_test.go`（策略矩阵 `""`/`allow`/`safe`/`strict`——前三者
  接受、`strict` 400 `hosted_tool_loss`；上游实收形状；撞名被丢的工具不被强制）。
- **冒烟 16j-1/16j-2**：16j-1 四个断言，其中一条是**反向**的——`unsupported_hosted_tool`
  **不再出现**（这正是要消灭的症状）；16j-2 钉住流式的三条（两个 custom 事件在、
  `function_call_arguments` 全程不在、item type 是 `custom_tool_call`）。
  夹具 `custom_tool_name(raw)` 认的是**降级后的形状**：上游若收到原样声明，夹具根本不命中，
  于是"没降级"会表现为"夹具没回工具调用"，而不是悄悄通过。
- **计数（精确相等，非 `>=`）**：§17/§18 `32→34`，§19 `23→25`，§21 `cache_coverage 9/32→9/34`、
  `not_reported_cache_calls 23→25`；`cache_hit_rate` 仍 `249/321`、`cache_write_input_tokens` 仍 0。
  `RESULT pass=142 → pass=151 fail=0`。
- **命令**：`go build ./...` ✅ · `gofmt -l $(git ls-files '*.go')` 空 ✅ · `go test $(go list ./... |
  grep -v common/image)` ✅ · **干净 `GOCACHE`** 下 `go vet ./...` 空、exit 0 ✅ ·
  `bash scripts/smoke/run_smoke.sh` → `pass=151 fail=0` ✅。`common/image` 的 jpeg 失败仍是 §0.3
  既存红线，未折进任何提交。

### 生产部署（2026-10-10）

部署到腾讯云上海 `106.54.224.78`（systemd `one-api.service`，非 Docker）。方式沿用 Oct 9 那次的
惯例：本地 CGO 编译 → sftp → 安装 → `systemctl restart`。**停机亚秒级**（stop 与 start 同一秒）。
迁移预检 `current=2 target=2 pending=[] warnings=[]`——本阶段与阶段 7/8/9 都没有迁移条目。

回滚点建在既有的命名惯例里：`/var/lib/one-api/backup/one-api-pre-protocolmatrix`（旧二进制）+ 同名
`.db`（`sqlite3 .backup` 在线快照，18 users / 7 channels）。

部署后在服务器上做了工具调用实测（root 的 test token，id=2；key 读进 shell 变量、全程未打印）：

| 路径 | 结果 |
| --- | --- |
| `/v1/responses` 非流式 + 函数工具 → chat 上游 | ✅ `function_call` 项，`id == call_id` |
| `/v1/responses` 流式 + 函数工具 → chat 上游 | ✅ `function_call_arguments.delta` ×5 + `done` + `completed` |
| `/v1/messages` + 工具 → chat 上游 | ✅ `tool_use` 块 + `stop_reason:"tool_use"` |
| `/v1/responses` + 函数工具 → **Anthropic 上游** | ⚠️ **未测成**：渠道 10 报 `no account is available`，且**同协议直通也失败**，是渠道不可用（该错误在部署前 08:33 就出现过），与本次部署无关 |
| `/v1/responses` + `custom` 工具 → chat 上游 | ✅ 200，且日志具名 `unsupported_hosted_tool`——即本阶段的立项证据 |

`responses ↔ anthropic` 那条腿（第 4 行）因此**仍未在生产环境验证过**，本地冒烟有格子覆盖。

### 剩余限制（阶段 11 输入）

1. **`responses ↔ anthropic` 未在生产验证**：本地冒烟覆盖了（16h-6、16i-1），但服务器上那个渠道
   当时不可用。渠道修好后应补一次。
2. **tool namespace 未实现**：`namespace` 字段继续被完全忽略。参考实现有 `ResponsesToolNamespaces`
   （把非默认命名空间的工具名拍平送上游、响应侧还原），若日后有客户端用，需要补。
3. **上游返回的 `custom_tool_call` 未处理**：`relay/adaptor/openai_responses/response.go` 的 `decode()`
   仍把它当 unmapped 丢弃（影响 chat 客户端 ← Responses 上游）。本次范围外。
4. **前端 `tool_loss_policy` 与 `upstream_protocol` 编辑界面**（阶段 9 第 1 条）仍未做。
5. **`native` 类托管工具全部丢弃**（阶段 9 第 2 条）不变。
6. **`upstream_protocol` 与渠道族的相容性检查**（阶段 8 第 3 条）仍未收成一处表。
7. **预检级拒绝的 usage 行**规则仍未统一（阶段 8 第 4 条）。
8. **部署脚本仍不存在**：仓库 `scripts/` 只有 `smoke`。这次的流程是照 Oct 9 那次的惯例手做的
   （本地编译 → sftp → `systemctl restart`），服务器上 `/opt/deploy/one-api-aixlab` 是个停在更老
   commit 的克隆，容易误导下一次部署。另：`VERSION` 文件是空的，二进制自报 `v0.0.0`，
   **没法从版本号分辨部署的是哪个提交**，建议下次部署前先填上。
