# 阶段验收冒烟脚本

- `run_smoke.sh`：端到端验收。自动构建当前工作区代码的二进制，在**独立 SQLite 库**上
  拉起 one-api（端口 13000）+ mock 上游（端口 19999），断言阶段 2 的核心契约：
  额度为 0 的用户/令牌可正常调用 `/v1/chat/completions`；`usage_records` 落表；
  旧 `logs` 消费行 `quota=0`；旧额度/统计列不被新请求写入；`/api/topup` → 410；
  `/v1/dashboard/billing/*` 为占位；`/api/plaza/models` 无费用/倍率字段。
- `mock_upstream.py`：本地假上游，返回固定 usage（输入 7 / 输出 5 / 合计 12）。

## 运行

```bash
bash scripts/smoke/run_smoke.sh
```

首次运行会联网下载 tiktoken 编码文件（约 10MB，缓存到本目录 `tiktoken/`）。
需要 `curl`、`sqlite3`、`python3`。脚本退出码 0 表示全部断言通过。

## 注意

- 脚本使用**独立数据库文件** `scripts/smoke/one-api.db`，不影响仓库根或生产库。
- 一次性产物（db、日志、cookie jar、临时 json、二进制、tiktoken 缓存）已在
  仓库根 `.gitignore` 中忽略，仅 `run_smoke.sh`/`mock_upstream.py` 两个脚本入库。
- 冒烟脚本会随阶段推进补充新的断言（阶段 3 协议入口、阶段 4 流式 usage、阶段 5 缓存三率）。
