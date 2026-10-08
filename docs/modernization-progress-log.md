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
   - `responses.go`：`ParseResponses`（OpenAI Responses → `GeneralOpenAIRequest`）+ `RenderResponses`（`openai.TextResponse` → Responses 信封）。
   - `messages.go`：`ParseMessages`（Anthropic Messages → `GeneralOpenAIRequest`）+ `RenderMessages`（`openai.TextResponse` → Anthropic 信封），含 `finish_reason ↔ stop_reason` 反向映射（现有 anthropic 适配器只有“Claude 上游→OpenAI 客户端”方向）。
   - `util.go`：`json.RawMessage` 防御式读取小工具。
4. **共享 relay 内核** `relay/controller/protocol.go`：`relayProtocolHelper` 走 §6.1 管线——解析→模型映射/强制 system prompt/prompt 估算（与 chat 路径一致）→**上游协议能力检查**→用 OpenAI 兼容上游发起 `/v1/chat/completions`→读取并归一 `openai.TextResponse`+usage（reported/estimated 判定同 `openai.Handler`）→按客户端协议渲染→收尾 `recordConsumeLog`+`recordUsage`（复用阶段 1/2 的幂等 recorder，`request_id` 不变）。
5. **薄入口** `relay/controller/protocol_responses.go`、`protocol_messages.go`：各自 `protocolEntry`（Parse/Stream/Render）+ `RelayResponsesHelper`/`RelayMessagesHelper`。

### §6.4 Responses 第一版边界（均返回点名结构化错误）
- `store`：默认按 false；`store=true` → 拒绝（不做服务端持久化）。
- `previous_response_id` → 拒绝；`background=true` → 拒绝。
- `input` 支持裸字符串、message 项（含 text/image content part）、`function_call`、`function_call_output`；`reasoning`/`item_reference`/`computer_call`/未识别项类型 → 点名 `input[i].type` 拒绝。
- `tools` 仅 function（内置 web_search/file_search 等拒绝）；`tool_choice` 支持 auto/none/required 与 function 对象。
- 稳定 `response.id`（`resp_` + 上游 chatcmpl 后缀）、`output[]`（message + function_call 项）、`output_text` 由 content 派生；`finish_reason=length` → `status:"incomplete"`+`incomplete_details`。
- usage 指针语义：`input_tokens_details.cached_tokens`/`output_tokens_details.reasoning_tokens` **仅在上游上报时出现**，绝不伪造 0。

### §6.5 Messages 覆盖
- 请求：`system`（string 或 text block 数组）、`messages`（string 或 block 数组）、`max_tokens`（必填正整数）、`stop_sequences`、`temperature/top_p/top_k`、`tools`（input_schema）、`tool_choice`（auto/any→required/tool/none）。
- content block：`text`、`image`（base64→data URI、url→image_url）、`assistant` 的 `tool_use`（归一为 OpenAI tool_calls，arguments 存 JSON 字符串）、`tool_result`（归一为独立 `role:"tool"` 消息，带 `tool_call_id`）。`document`/未知块 → 点名拒绝；`tool_use` 出现在 user 消息 → 拒绝。
- 响应：`id/msg_*`、`type:"message"`、`role:"assistant"`、`content`（text + tool_use 块，input 由 arguments 反解析）、`stop_reason`（stop→end_turn、length→max_tokens、tool_calls→tool_use、空→null）、`usage`（input/output_tokens）。

### §6.6 上游协议选择
- 阶段 3 两个入口仅支持 **OpenAI 兼容上游**（`meta.APIType==apitype.OpenAI`）；其他渠道类型返回明确 `upstream_protocol_unsupported`，不猜测、不静默降级。多上游协议矩阵在阶段 4+ 扩展。
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
- **仅 OpenAI 兼容上游流式**：与阶段 3 一致，非 OpenAI 上游仍返回 `upstream_protocol_unsupported`；Anthropic/AWS 等原生上游协议矩阵在后续扩展。
- **`/v1/chat/completions` 既有透传未改动**：仍走 `openai.StreamHandler` 原样透传（含 reported/estimated usage 收尾），避免破坏大量渠道；新 `sse` 层与 typed 渲染当前只服务 `/v1/responses`、`/v1/messages`。chat 路径的 `partial`/错误不吞如需统一到 `sse` 层，留待评估后再迁移（改动面大）。
- 缓存字段归一化与三指标贯通 adaptor 属阶段 5。

### 测试与验收
- 新增单测：`relay/sse/reader_test.go`（多行 data/event 类型/注释/尾事件冲刷/跨分片）、`relay/sse/writer_test`（并入 reader_test，帧法与多行前缀）、`relay/sse/state_test.go`（文本+finish、**工具参数按 index 分组不串台**、reported 优先、estimated 含工具参数、无估算返回 nil、上游 error 不吞、abort/无 DONE→partial、malformed 报错）、`relay/protocol/stream_test.go`（Responses/Messages typed 事件序列、delta 不拼接、工具参数不串台、error 事件终止性、stop_reason 映射、message_start 提示种子）。
- 冒烟（`scripts/smoke/`，mock 上游升级为 `stream=true` 时发真实 OpenAI SSE：两段文本 delta + usage 尾块 + `[DONE]`）：新增 responses 流式 content-type/typed 事件/delta 不拼接/reported usage，messages 流式 content-type/typed 事件/finish_reason→stop_reason；收尾计数改为 5 条 success（chat + responses 非流式/流式 + messages 非流式/流式）、全部 `usage_source=reported`、上游未报缓存时 `cache_usage_status=not_reported`。`RESULT pass=38 fail=0`。
- `go build ./...`、`gofmt -l`（含全仓 relay/model/middleware/controller/router）、`go vet`、`go test`（排除存量红 `common/image`）全部通过。

## 阶段 5：缓存归一化

（待实施）

## 阶段 6：前端现代化

（待实施）
