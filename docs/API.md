# 使用 API 操控 & 扩展本系统

本系统是面向课题组内网的 LLM 路由器，已移除兑换码、在线充值、第三方登录等运营功能。
管理面 API 可以在无需二次开发的情况下扩展和自定义系统行为。

## 鉴权

支持两种鉴权方式：

1. **Cookie**：在浏览器中登录后，同源的 `/api/*` 请求会自动带上会话 Cookie。
2. **用户访问令牌（AccessToken）**：在「设置 - 个人设置 - 重置系统访问令牌」处获取，
   将令牌作为请求头 `Authorization` 的值即可调用管理 API，例如：

```bash
curl -X GET http://localhost:3000/api/channel/ \
  -H "Authorization: <你的访问令牌>"
```

令牌的权限等级与其所属用户的角色一致（普通用户 / 管理员 / 超级管理员）。

## 请求格式与响应格式

所有接口使用 JSON。响应体格式统一为：

```json
{
  "message": "请求信息",
  "success": true,
  "data": {}
}
```

## API 列表

> 当前文档只列出内网运营常用的接口，其余接口请通过浏览器开发者工具抓取前端请求获取。

### 获取当前登录用户信息

**GET** `/api/user/self`

### 模型广场

**GET** `/api/plaza/models`（需要登录）

返回当前用户分组下可调用的模型、输入/输出费用（每 1K tokens 的额度消耗）、倍率，以及本分组已启用渠道（不含 Key / BaseURL / config）。

```json
{
  "success": true,
  "data": {
    "group": "default",
    "group_ratio": 1,
    "quota_per_unit": 500000,
    "display_in_currency": false,
    "models": [
      {
        "name": "gpt-4o",
        "model_ratio": 1.25,
        "completion_ratio": 4,
        "group_ratio": 1,
        "input_quota_per_1k": 1250,
        "output_quota_per_1k": 5000,
        "ratio_configured": true,
        "ratio_varies": false,
        "channels": [
          {
            "id": 1,
            "name": "openai-main",
            "type": 1,
            "status": 1,
            "priority": 10
          }
        ]
      }
    ]
  }
}
```

费用计算：`input_quota_per_1k = 1000 × group_ratio × model_ratio`，`output_quota_per_1k = input_quota_per_1k × completion_ratio`。列表价取该模型优先级最高的渠道；若各渠道倍率不一致，`ratio_varies` 为 true。

### 为用户发放额度

**POST** `/api/topup` （需要管理员权限）

```json
{
  "user_id": 1,
  "quota": 100000,
  "remark": "9 月课题组额度发放"
}
```

- `quota` 为**增量**，会在用户现有额度上累加。
- `remark` 可选，省略时系统自动生成备注。
- 每次发放都会写入一条类型为「充值」的用户日志，可在日志页审计。

### 渠道管理

- **GET** `/api/channel/`：分页获取渠道列表。
- **POST** `/api/channel/`：新建渠道。
- **PUT** `/api/channel/`：更新渠道。
- **GET** `/api/channel/test/:id`：测试渠道。

渠道的差异化参数（Azure / 讯飞的 `api_version`、AI Proxy Library 的 `library_id`、
阿里的 `plugin` 等）统一放在渠道配置的 JSON 里，已不再使用旧的「其他」字段。

### 选项管理

- **GET** `/api/option/`：获取全部配置项（密钥类配置不返回）。
- **PUT** `/api/option/`：更新单个配置项，请求体为 `{"key": "...", "value": "..."}`。

如果现有的 API 无法满足需求，欢迎在内部仓库提 issue 讨论。
