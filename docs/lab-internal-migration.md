# 课题组内网 LLM 路由器 — 实施规格（给执行 Agent）

你是执行者。按本文从阶段 1 做到阶段 5，不要重新讨论产品方向。

工作区：仓库根目录  
技术栈：Go 1.20 + Gin + GORM，前端只改 `web/default`（React + Semantic UI）。

## 已拍板（禁止再问）

| 项 | 决定 |
|----|------|
| 额度 | 保留。管理员发放 + 新用户赠送。无兑换码、无支付、无邀请返利 |
| 开户 | 开放注册，**强制**邮箱验证码。可配邮箱域名白名单。管理员仍可后台开户 |
| 前端 | **只留 `web/default`**。删除 `web/berry`、`web/air` 目录及构建 |
| 登录 | 用户名或邮箱 + 密码。验证码只用于注册 / 绑邮箱 / 重置密码。不要做无密码登录 |
| 美元展示 | 默认关闭 `DisplayInCurrencyEnabled`，设置页保留开关 |
| 主题 | 删除 `Theme` 配置，固定 default |
| 中继 | **不准改** `relay/`、`middleware/distributor.go` 选路逻辑、`model/ability.go` 选渠算法 |

完成后必须 `go build` 通过；前端 `web/default` 能 `npm run build`。

---

## 禁止改动

- `relay/` 全部（adaptor、billing、controller、meta）
- `middleware/distributor.go` 的选渠（但 **必须删** 其中对 `channel.Other` 的兼容块，见阶段 3）
- `model/ability.go`、`model/cache.go` 的选路
- `model/token.go` 字段
- 不要引入新依赖
- 不要重构无关代码、不要顺便改格式
- 不要改 berry/air 里的代码（直接删目录）

---

## 阶段 0 — 编译基线

1. 确认 `go build -o one-api.exe .` 当前能过（Windows）。
2. 全程用仓库现有风格：中文错误信息、`gin.H{"success","message","data"}`。

---

## 阶段 1 — 鉴权收口

目标：OAuth / Turnstile 消失；注册强制邮箱验证码；重置密码改为 6 位码 + 新密码。

### 1.1 删除后端 OAuth / Turnstile 文件

整文件删除：

- `controller/auth/github.go`
- `controller/auth/lark.go`
- `controller/auth/oidc.go`
- `controller/auth/wechat.go`
- `controller/auth/` 目录（空了就删）
- `middleware/turnstile-check.go`

### 1.2 改 `router/api.go`

- 去掉 `"github.com/songquanpeng/one-api/controller/auth"` import。
- 这几行改成**只有** `CriticalRateLimit`，不要 `TurnstileCheck`：
  - `GET /verification`
  - `GET /reset_password`
  - `POST /user/register`
- **删除全部** OAuth 路由：
  - `/oauth/github` `/oauth/oidc` `/oauth/lark` `/oauth/state` `/oauth/wechat` `/oauth/wechat/bind` `/oauth/email/bind`
- 绑邮箱不要独立 OAuth 路径。把 `controller.EmailBind` 改挂到登录用户下：
  - `selfRoute.GET("/email/bind", controller.EmailBind)` 或 `POST`（与现有 query `email`+`code` 保持兼容即可，方法可仍是 GET）
- 删除 `selfRoute.GET("/aff", controller.GetAffCode)`

### 1.3 改 `common/config/config.go`

**删除这些变量：**

```
GitHubOAuthEnabled, GitHubClientId, GitHubClientSecret
LarkClientId, LarkClientSecret
OidcEnabled, OidcClientId, OidcClientSecret, OidcWellKnown,
OidcAuthorizationEndpoint, OidcTokenEndpoint, OidcUserinfoEndpoint
WeChatAuthEnabled, WeChatServerAddress, WeChatServerToken, WeChatAccountQRCodeImageURL
TurnstileCheckEnabled, TurnstileSiteKey, TurnstileSecretKey
PasswordLoginEnabled, PasswordRegisterEnabled
TopUpLink
QuotaForInviter, QuotaForInvitee
```

**保留但改默认：**

- `EmailVerificationEnabled = true`（代码里注册路径不再读这个开关当可选，见下；变量可删，建议删，注册恒需验证码）
- `DisplayInCurrencyEnabled = false`
- `ValidThemes` 只留 `"default": true`；`Theme` 仍可读 env，但默认 `"default"`，非法值回退 default

**保留：** SMTP 全套、`RegisterEnabled`、邮箱域名白名单、`QuotaForNewUser`、MessagePusher、ChatLink、倍率、RetryTimes 等。

### 1.4 改 `common/verification.go`

- `verificationMapMaxSize`：`10` → `1024`
- `GenerateVerificationCode` / `VerifyCodeWithKey` 行为不变

### 1.5 改 `controller/user.go`

**Login**

- 删除 `if !config.PasswordLoginEnabled` 整段。
- `ValidateAndFill` **已经**支持用户名或邮箱，不要再改 `model/user.go` 的查询逻辑。

**Register**

- 删除 `PasswordRegisterEnabled` 判断。
- 保留 `RegisterEnabled`。
- **强制** `user.Email` 与 `user.VerificationCode` 非空，并用 `common.VerifyCodeWithKey(..., EmailVerificationPurpose)`。失败返回「验证码错误或已过期」。
- 删除邀请码：
  - 删 `affCode := user.AffCode`、`GetUserIdByAffCode`、`InviterId`
  - `cleanUser.Insert(ctx, 0)`
- `cleanUser` 必须带 `Email`

**删除函数**

- `GetAffCode`

**TopUp（用户兑换码）**

- 删除 `TopUp` 函数（阶段 2 路由也会删）。`AdminTopUp` 保留。

**EmailBind**

- 保留逻辑（已登录用户绑邮箱），仅换路由位置。

### 1.6 改 `controller/misc.go`

**GetStatus `data` 只保留：**

```
version, start_time, system_name, logo, footer_html,
server_address, chat_link, quota_per_unit, display_in_currency
```

删：`github_oauth`、`github_client_id`、`lark_client_id`、`wechat_*`、`turnstile_*`、`top_up_link`、`oidc*`、`email_verification`（前端注册恒显示邮箱栏，不再靠开关）。

**SendEmailVerification**

- 逻辑保留（域名白名单、邮箱占用、6 位码、SMTP）。
- 若 `config.SMTPServer == ""`，直接返回失败：「请联系管理员先配置 SMTP」。

**SendPasswordResetEmail**

- 6 位码：`common.GenerateVerificationCode(6)`，purpose 仍 `PasswordResetPurpose`。
- 邮件正文改成展示验证码（抄 `SendEmailVerification` 的 HTML 结构），**不要再发** `/user/reset?email=&token=` 链接。
- SMTP 未配置同样报错。

**ResetPassword**

现结构：

```go
type PasswordResetRequest struct {
    Email string `json:"email"`
    Token string `json:"token"`
}
```

改成：

```go
type PasswordResetRequest struct {
    Email    string `json:"email"`
    Token    string `json:"token"`    // 6 位验证码，字段名保持 token 以免无谓改动
    Password string `json:"password"` // 新密码
}
```

行为：

1. email / token / password 缺一不可；password 长度仍按现有用户密码规则（min 8）。
2. `VerifyCodeWithKey(email, token, PasswordResetPurpose)`。
3. `model.ResetUserPasswordByEmail(email, req.Password)` —— **用用户提交的新密码**，不要 `GenerateVerificationCode(12)` 随机密码。
4. `DeleteKey` 后返回 success，data 可空。不要把新密码回给前端。

### 1.7 改 `controller/option.go`

删除对这些 key 的特殊校验：`Theme`（或只允许 default）、`GitHubOAuthEnabled`、`WeChatAuthEnabled`、`TurnstileCheckEnabled`、OIDC 相关。

### 1.8 改 `model/option.go`

`InitOptionMap` 与 `updateOptionMap` **删除**所有已删配置的 case / 初始化。

`InitOptionMap` 最终应包含的 key（不要多）：

```
RegisterEnabled
EmailDomainRestrictionEnabled, EmailDomainWhitelist
AutomaticDisableChannelEnabled, AutomaticEnableChannelEnabled
ApproximateTokenEnabled, LogConsumeEnabled
DisplayInCurrencyEnabled, DisplayTokenStatEnabled
ChannelDisableThreshold
SMTPServer, SMTPPort, SMTPAccount, SMTPFrom, SMTPToken
Notice, About, HomePageContent, Footer, SystemName, Logo, ServerAddress
MessagePusherAddress, MessagePusherToken
QuotaForNewUser, QuotaRemindThreshold, PreConsumedQuota
ModelRatio, GroupRatio, CompletionRatio
ChatLink, QuotaPerUnit, RetryTimes
```

不要：GitHub/Lark/OIDC/WeChat/Turnstile/TopUpLink/QuotaForInviter/QuotaForInvitee/PasswordLoginEnabled/PasswordRegisterEnabled/EmailVerificationEnabled/Theme。

### 1.9 前端 default — 登录 / 注册 / 重置

**`LoginForm.js`**

- 删 GitHub/飞书/微信 UI、`onGitHubOAuthClicked` / `onLarkOAuthClicked`、微信 Modal、`wechat_verification_code`。
- 用户名输入框 placeholder 改为「用户名或邮箱」。
- 提交仍 `POST /api/user/login` `{username, password}`。

**`RegisterForm.js`**

- 删 `react-turnstile`、aff（URL `aff`、localStorage、`inputs.aff_code`）。
- **始终**显示邮箱 + 验证码 +「获取验证码」。
- 获取验证码：`GET /api/verification?email=`，不再带 `turnstile=`。
- 提交：`username, password, email, verification_code`。无验证码禁止提交。
- 删 `showEmailVerification` / `turnstile*` state。

**`PasswordResetForm.js`**

改成单页完成重置：

1. 邮箱
2. 获取验证码 → `GET /api/reset_password?email=`（无 turnstile）
3. 验证码 + 新密码 + 确认密码
4. `POST /api/user/reset` `{email, token, password}`
5. 成功后跳转 `/login`

**删除文件**

- `web/default/src/components/PasswordResetConfirm.js`
- `web/default/src/components/GitHubOAuth.js`
- `web/default/src/components/LarkOAuth.js`

**`App.js`**

- 删路由：`/oauth/github`、`/oauth/lark`、`/user/reset`
- 保留 `/reset` → `PasswordResetForm`

**`PersonalSetting.js`**

- 删 Turnstile、GitHub/飞书/微信绑定、邀请链接（`/api/user/aff`、`affLink`）。
- 保留：系统 AccessToken、改密（若有）、邮箱绑定（发码不再带 turnstile）、删除账号。
- 邮箱绑定请求改到新路径，例如 `GET /api/user/email/bind?email=&code=`。

**`components/utils.js`**

- 删除 `onGitHubOAuthClicked`、`onLarkOAuthClicked`。若文件因此只剩无关工具，保留其余函数。

**`SystemSetting.js`**

删整段：GitHub OAuth、微信、Turnstile、以及对应 submit 函数和开关（`GitHubOAuthEnabled`、`WeChatAuthEnabled`、`TurnstileCheckEnabled`、`PasswordLoginEnabled`、`PasswordRegisterEnabled`、`EmailVerificationEnabled`）。

保留：SMTP、注册开关 `RegisterEnabled`、邮箱域名白名单、ServerAddress、MessagePusher。

**i18n**

- `web/default/src/locales/zh-CN.json`、`en.json`：删掉已无 UI 的 oauth/turnstile/aff 文案不是必须，但登录 placeholder 要改。不要留死链翻译也行，优先改仍在用的 key。

阶段 1 验收：`go build` 通过；全仓库 grep 无 `TurnstileCheck`、`GitHubOAuth`、`controller/auth`。

---

## 阶段 2 — 去掉营业功能

### 2.1 删除兑换码

整文件删：

- `model/redemption.go`
- `controller/redemption.go`

`model/main.go` `migrateDB()`：删除 `DB.AutoMigrate(&Redemption{})`。

`router/api.go`：

- 删整个 `redemptionRoute`
- 删 `userRoute` 里 `POST /topup`（`controller.TopUp`）
- **保留** `POST /api/topup`（`AdminAuth` + `controller.AdminTopUp`）

### 2.2 删除渠道上游余额爬取

- 删 `controller/channel-billing.go`（整个文件）
- `router/api.go` 删：
  - `GET /channel/update_balance`
  - `GET /channel/update_balance/:id`
- 前端 `ChannelsTable.js`：删「更新余额」按钮和对应 API 调用、余额列（若仅展示 `channel.balance`）。
- `model/channel.go`：删除 `UpdateBalance` 方法（字段在阶段 3 删）。

若 `channel-billing.go` 里有被测试渠道复用的 HTTP helper，先 grep `GetResponseBody` / `GetAuthHeader`：只被余额用则一起删；若 `channel-test.go` 在用则把 helper 挪到 `channel-test.go` 或 `channel.go`。

### 2.3 运营设置 `OperationSetting.js`

删除表单项及 submit：

- `TopUpLink`
- `QuotaForInviter` / `QuotaForInvitee`

保留：`QuotaForNewUser`、提醒阈值、预扣、倍率 JSON、重试次数、ChatLink、渠道自动禁用、`DisplayInCurrencyEnabled`（默认关）。

### 2.4 导航与页面

**`Header.js`**

`headerButtons` 删除：

- `header.redemption` → `/redemption`
- `header.topup` → `/topup`

**`App.js` 删除路由及 import：**

- `/redemption`、`/redemption/edit/:id`、`/redemption/add`
- `/topup`

**删目录/文件：**

- `web/default/src/pages/Redemption/`（含 EditRedemption）
- `web/default/src/components/RedemptionsTable.js`
- `web/default/src/pages/TopUp/`

### 2.5 管理员发放额度（用户编辑页）

`web/default/src/pages/User/EditUser.js`：

- 删 `github_id` / `wechat_id` 展示与提交。
- 保留现有 `quota` 绝对修改（`PUT /api/user/`）。
- 额外加一块「发放额度」：数字输入 + 备注，调用已有 `POST /api/topup`：

```json
{ "user_id": <id>, "quota": <int>, "remark": "<string>" }
```

仅 `userId` 存在（管理员编辑他人）时显示。成功后 `loadUser()` 刷新。

`controller.AdminTopUp` 已写日志，不要重复造接口。

阶段 2 验收：grep `redemption` / `Redemption` / `TopUpLink` / `update_balance` 仅可能出现在迁移 SQL、本文件或注释；`go build` 通过。

---

## 阶段 3 — 表结构精简

GORM AutoMigrate **不会 DROP COLUMN**。必须同时改 struct + SQL。

### 3.1 `model/user.go`

**User struct 最终字段：**

```go
type User struct {
    Id               int    `json:"id"`
    Username         string `json:"username" gorm:"unique;index" validate:"max=12"`
    Password         string `json:"password" gorm:"not null;" validate:"min=8,max=20"`
    DisplayName      string `json:"display_name" gorm:"index" validate:"max=20"`
    Role             int    `json:"role" gorm:"type:int;default:1"`
    Status           int    `json:"status" gorm:"type:int;default:1"`
    Email            string `json:"email" gorm:"index" validate:"max=50"`
    VerificationCode string `json:"verification_code" gorm:"-:all"`
    AccessToken      string `json:"access_token" gorm:"type:char(32);column:access_token;uniqueIndex"`
    Quota            int64  `json:"quota" gorm:"bigint;default:0"`
    UsedQuota        int64  `json:"used_quota" gorm:"bigint;default:0;column:used_quota"`
    RequestCount     int    `json:"request_count" gorm:"type:int;default:0;"`
    Group            string `json:"group" gorm:"type:varchar(32);default:'default'"`
}
```

**删除字段：** `GitHubId` `WeChatId` `LarkId` `OidcId` `AffCode` `InviterId`

**删除函数：**

- `GetUserIdByAffCode`
- `FillUserByGitHubId` `FillUserByLarkId` `FillUserByOidcId` `FillUserByWeChatId`
- `IsWeChatIdAlreadyTaken` `IsGitHubIdAlreadyTaken` `IsLarkIdAlreadyTaken` `IsOidcIdAlreadyTaken`

**改 `Insert`：**

```go
func (user *User) Insert(ctx context.Context, inviterId int) error
```

`inviterId` 参数可改为无 inviter 版本 `Insert(ctx context.Context)`，并更新所有调用点（Register、管理员创建用户）。不要再写 AffCode、不要给邀请人加额度。仍设置 `Quota = config.QuotaForNewUser`、`AccessToken = random.GetUUID()`。

Grep `Insert(ctx` 和 `InviterId` 清干净。

### 3.2 `model/channel.go`

删除字段：`Weight` `Other` `Balance` `BalanceUpdatedTime`  
删除方法：`UpdateBalance`、`GetWeight`（若有）

**保留：** Id Type Key Status Name CreatedTime TestTime ResponseTime BaseURL Models Group UsedQuota ModelMapping Priority Config SystemPrompt

`middleware/distributor.go` `SetupContextForSelectedChannel`：删除 `if channel.Other != nil { switch channel.Type ... }` 整块。只留 `cfg, _ := channel.LoadConfig(); c.Set(ctxkey.Config, cfg)`。

前端 `EditChannel.js` / `ChannelsTable.js`：删 other/weight/balance 表单项与列。渠道「其他」里若把 API Version 写在 Other，改走已有 `config` JSON（Azure/Gemini 的 api_version 本来就在 ChannelConfig）。

### 3.3 日志类型语义

`model/log.go` 的 `LogTypeTopup` 留下，表示管理员发放额度（`AdminTopUp` 已在用）。不要删。

### 3.4 迁移 SQL

新建 `bin/migration_lab.sql`，SQLite / MySQL 都能尽量用标准 DDL。文件头注释：

- 全新部署只需 AutoMigrate，不必跑此脚本
- 已有库在停机后执行

内容必须包含：

```sql
-- SQLite 3.35+ / MySQL 8
DROP TABLE IF EXISTS redemptions;

-- users
-- SQLite: ALTER TABLE users DROP COLUMN ...
-- 列：github_id, wechat_id, lark_id, oidc_id, aff_code, inviter_id

-- channels
-- 列：weight, other, balance, balance_updated_time

DELETE FROM options WHERE `key` IN (
  'GitHubOAuthEnabled','GitHubClientId','GitHubClientSecret',
  'LarkClientId','LarkClientSecret',
  'OidcEnabled','OidcClientId','OidcClientSecret','OidcWellKnown',
  'OidcAuthorizationEndpoint','OidcTokenEndpoint','OidcUserinfoEndpoint',
  'WeChatAuthEnabled','WeChatServerAddress','WeChatServerToken','WeChatAccountQRCodeImageURL',
  'TurnstileCheckEnabled','TurnstileSiteKey','TurnstileSecretKey',
  'PasswordLoginEnabled','PasswordRegisterEnabled','EmailVerificationEnabled',
  'TopUpLink','QuotaForInviter','QuotaForInvitee','Theme'
);
```

SQLite 一次只能 DROP 一列。按列分行写，并注明：若列已不存在会报错，可忽略。

MySQL 可用一条 `ALTER TABLE users DROP COLUMN ..., DROP COLUMN ...;`。若维护两份太烦，只写 SQLite 版本（课题组默认 SQLite），文件顶部写明。

### 3.5 旧渠道 `other` 数据

不写自动 JSON 迁移。在 `migration_lab.sql` 注释：若渠道曾把 API Version 填在「其他」，删列前请在后台把该值填进渠道配置 JSON 的 `api_version` 后保存。内网新部署可忽略。

阶段 3 验收：新库 AutoMigrate 后 `users` 无 oauth/aff 列、无 `redemptions` 表；`go build` 通过。

---

## 阶段 4 — 前端收口与去主题

### 4.1 删除 berry / air

删除目录：

- `web/berry/`
- `web/air/`

改 `web/THEMES` 只留一行：`default`

改 `web/build.sh`：仍读 THEMES，不用特殊处理。

改 `Dockerfile`：

- builder 阶段只 `npm install` / `npm run build` **default**，删 berry/air 并行。
- 不要留 `& wait` 三主题。

改 `common/config/config.go`：`ValidThemes` 仅 default。

`router/web.go` 已用 `config.Theme`，确保非法/空时落到 `default`。

### 4.2 依赖

`web/default/package.json` 删除 `"react-turnstile"`。

不要提交 `package-lock` 的无关大面积变化以外的内容；若有 lockfile，用 npm 卸依赖让它自然更新。

### 4.3 default 其它清理

- `web/default/src/components/Header.js`：已在阶段 2 删兑换/充值。
- 侧栏若另有一份（没有就忽略）。
- `web/default/src/images/lark.svg`：OAuth 用完可删。
- grep `web/default`：`turnstile`、`github_oauth`、`wechat`、`aff`、`redemption`、`topup`、`TopUp` 应为 0（i18n 残留可顺手删）。

### 4.4 默认文案

不要大改品牌。`SystemName` 仍默认 `"One API"`，管理员可在设置改。不要把 README 改成营销稿；`docs/API.md` 改成：

- 鉴权：Cookie 或用户 AccessToken
- 管理发放额度：`POST /api/topup` `{user_id, quota, remark}`
- 删除兑换码/充值链接段落

阶段 4 验收：`Dockerfile` 无 berry/air；仓库无 `web/berry`、`web/air`。

---

## 阶段 5 — 全库扫描与验收

### 5.1 必须为 0 的 grep（大小写不敏感）

在 Go + `web/default` 内搜索，命中则未完成：

```
TurnstileCheck
react-turnstile
GitHubOAuth
LarkOAuth
OidcAuth
WeChatAuth
GetAffCode
AffCode
QuotaForInviter
TopUpLink
controller/auth
Redemption
update_balance
onGitHubOAuthClicked
```

允许命中：`bin/migration_lab.sql`、`docs/lab-internal-migration.md`。

`PasswordLoginEnabled` / `GitHubClientId` 等 config 变量必须已删。

### 5.2 行为清单（执行者至少用 curl / 代码路径过一遍）

1. `POST /api/user/register` 无 email/code → 失败。
2. `GET /api/verification` 无 turnstile 参数应成功（SMTP 未配则明确失败，不算回归）。
3. `POST /api/user/login` 支持用户名（已有逻辑也支持邮箱）。
4. `GET /api/oauth/github` → 404。
5. `GET /api/redemption/` → 404。
6. `POST /api/user/topup` → 404。
7. `POST /api/topup` 无 Admin → 失败；有 Admin → 加额度。
8. `GET /v1/models` 带 `sk-` 仍走 `TokenAuth`（中继未改，编译过即可，不必真连上游）。
9. `go build` 成功。

### 5.3 不要做的验收

- 不要为了测 SMTP 去接真邮箱服务（代码路径正确即可）。
- 不要启动完整 Docker 除非环境已有。
- 不要改 CI（仓库若无测试工作流就算了）。现有 Go test 能跑的跑一下：`go test ./...`，失败若来自已删包则删对应 `_test.go`。

---

## 建议提交切分（若执行者自己 commit）

1. `auth: remove oauth and turnstile, require email code`
2. `feat: password reset via email code`
3. `chore: remove redemption, invite, channel balance`
4. `schema: slim users/channels, drop redemptions`
5. `web: drop berry/air, remove commercial UI`

也可以一个 commit。不要混入无关格式化。

---

## 关键调用关系（改完后应如此）

```
POST /api/user/register
  → 校验邮箱验证码 → User.Insert（无邀请）

POST /api/user/login
  → User.ValidateAndFill（username 或 email）→ SetupLogin

GET /api/verification
  → 域名白名单 → 6 位码 → SMTP（无 Turnstile）

GET /api/reset_password
  → 6 位码邮件（无 Turnstile）

POST /api/user/reset
  → 校验码 → ResetUserPasswordByEmail(新密码)

POST /api/topup  (AdminAuth)
  → IncreaseUserQuota + LogTypeTopup

/v1/chat/completions
  → TokenAuth → Distribute → Relay   （未改）
```

---

## 若与代码冲突

- 以「删除营业功能、不改中继」为准。
- 发现 berry 被 Go embed 引用：`main.go` 是 `//go:embed web/build/*`，所以必须保证 `web/build/default` 仍在；删源码主题不影响 embed，除非 `web/build/berry` 已提交。若仓库里有 `web/build/berry`、`web/build/air`，一并删掉，只留 `web/build/default`。
- Windows 上不要用 Linux 才有的 `rm -rf` 写进 Go 代码；Dockerfile 仍是 Linux 没问题。

开始前先读：`router/api.go`、`controller/user.go`、`model/user.go`、`web/default/src/App.js`、`web/default/src/components/Header.js`。然后按阶段 1 → 5 顺序改，每阶段编译一次。
