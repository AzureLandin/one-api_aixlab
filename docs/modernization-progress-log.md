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

（待实施）

## 阶段 2：移除额度与倍率

（待实施）

## 阶段 3：多 API 格式路由

（待实施）

## 阶段 4：流式与 Usage 收尾

（待实施）

## 阶段 5：缓存归一化

（待实施）

## 阶段 6：前端现代化

（待实施）
