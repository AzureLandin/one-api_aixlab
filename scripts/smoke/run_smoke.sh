#!/bin/bash
# 阶段2 端到端冒烟：额度0用户可调用、usage_records 落表、logs quota=0、topup 410、billing 占位
# 用法：bash scripts/smoke/run_smoke.sh   （自动构建当前工作区代码的二进制）
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
cd "$HERE"
# 一次性产物放本目录（/tmp 每次 bash 调用是独立命名空间，不能跨调用复用）
rm -f one-api.db mock.log oneapi.log jar_root.txt jar_user.txt \
      tmp_login.json tmp_user.json tmp_l2.json tmp_tk.json tmp_ch.json tmp_relay.json tmp_topup.json \
      tmp_resp.json tmp_resp2.json tmp_resp3.json tmp_resp4.json tmp_msg.json tmp_msg2.json \
      tmp_cache.json tmp_sum.json tmp_models.json tmp_model1.json

# 构建被测二进制（缓存放仓库内 .gocache，绕开只读的 ~/.cache）
GOCACHE="$REPO/.gocache" go build -o ./one-api "$REPO" || { echo "FATAL: build failed"; exit 2; }

PORT=13000 SESSION_SECRET=smoke TIKTOKEN_CACHE_DIR=$PWD/tiktoken ./one-api > oneapi.log 2>&1 &
API_PID=$!
python3 mock_upstream.py > mock.log 2>&1 &
MOCK_PID=$!

# 首次启动需下载 tiktoken 编码文件（约 10MB），给足时间
UP=0
for i in $(seq 1 240); do
  if curl -s -o /dev/null http://127.0.0.1:13000/api/status; then UP=1; break; fi
  sleep 1
done
[ "$UP" = 1 ] || { echo "FATAL: server did not start"; tail -20 oneapi.log; kill $API_PID $MOCK_PID 2>/dev/null; exit 2; }
echo "== server up =="

pass=0; fail=0
check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 (expected=$2 actual=$3)"; fail=$((fail+1)); fi
}

# 1. root 登录
curl -s -c jar_root.txt -X POST http://127.0.0.1:13000/api/user/login \
  -H 'Content-Type: application/json' -d '{"username":"root","password":"123456"}' > tmp_login.json
check "root login" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_login.json)"

# 2. 创建 quota=0 普通用户
curl -s -b jar_root.txt -X POST http://127.0.0.1:13000/api/user/ \
  -H 'Content-Type: application/json' \
  -d '{"username":"zero_user","password":"Pass1234!","role":1,"email":"z@z.cn"}' > tmp_user.json
check "create zero-quota user" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_user.json)"

# 3. 创建渠道 → mock 上游
curl -s -b jar_root.txt -X POST http://127.0.0.1:13000/api/channel/ \
  -H 'Content-Type: application/json' \
  -d '{"name":"mock","type":1,"base_url":"http://127.0.0.1:19999","key":"sk-mock","models":"gpt-3.5-turbo","group":"default","priority":1,"weight":1,"status":1}' > tmp_ch.json
check "create channel" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_ch.json)"

# 4. 新用户登录并创建令牌（请求携带额度字段，应被忽略）
curl -s -c jar_user.txt -X POST http://127.0.0.1:13000/api/user/login \
  -H 'Content-Type: application/json' -d '{"username":"zero_user","password":"Pass1234!"}' > tmp_l2.json
check "zero user login" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_l2.json)"
curl -s -b jar_user.txt -X POST http://127.0.0.1:13000/api/token/ \
  -H 'Content-Type: application/json' \
  -d '{"name":"t-zero","expired_time":-1,"remain_quota":123,"unlimited_quota":false,"models":"","subnet":""}' > tmp_tk.json
check "create token" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_tk.json)"
KEY=$(curl -s -b jar_user.txt 'http://127.0.0.1:13000/api/token/?p=0&n=10' | python3 -c "import sys,json;print(json.load(sys.stdin)['data'][0]['key'])")
TQ=$(sqlite3 one-api.db "select remain_quota||'|'||unlimited_quota from tokens where key='$KEY';")
check "token quota forced 0|1" "0|1" "$TQ"

# 5. 核心：用户 quota=0 且令牌 quota=0，请求必须放行并成功
CODE=$(curl -s -o tmp_relay.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}')
check "relay 200 with zero quota" 200 "$CODE"
grep -Eq '"content": ?"ok"' tmp_relay.json && { echo "PASS: relay body"; pass=$((pass+1)); } || { echo "FAIL: body $(head -c 200 tmp_relay.json)"; fail=$((fail+1)); }

# 5b. /v1/models 正常（OpenAI 列表信封 + 单模型检索）
CODE=$(curl -s -o tmp_models.json -w '%{http_code}' http://127.0.0.1:13000/v1/models -H "Authorization: Bearer $KEY")
check "/v1/models 200" 200 "$CODE"
grep -Eq '"object": ?"list"' tmp_models.json && grep -Eq '"id": ?"gpt-3.5-turbo"' tmp_models.json && { echo "PASS: /v1/models list envelope"; pass=$((pass+1)); } || { echo "FAIL: /v1/models body $(head -c 200 tmp_models.json)"; fail=$((fail+1)); }
CODE=$(curl -s -o tmp_model1.json -w '%{http_code}' http://127.0.0.1:13000/v1/models/gpt-3.5-turbo -H "Authorization: Bearer $KEY")
check "/v1/models/:model retrieve 200" 200 "$CODE"

sleep 1
# 6. usage_records：success + reported，7/5/12
UR=$(sqlite3 one-api.db "select status||'|'||usage_source||'|'||input_tokens||'|'||output_tokens||'|'||total_tokens from usage_records order by id desc limit 1;")
check "usage_records success/reported" "success|reported|7|5|12" "$UR"

# 7. 旧 logs 表：消费行 quota=0
sleep 1
LG=$(sqlite3 one-api.db "select quota from logs where type=2 order by id desc limit 1;")
check "consume log quota=0" "0" "$LG"

# 8. 旧统计列保持不动
UC=$(sqlite3 one-api.db "select quota||'|'||used_quota||'|'||request_count from users where username='zero_user';")
check "user legacy cols untouched" "0|0|0" "$UC"
TC=$(sqlite3 one-api.db "select used_quota from tokens where key='$KEY';")
check "token used_quota untouched" "0" "$TC"
CC=$(sqlite3 one-api.db "select used_quota from channels where name='mock';")
check "channel used_quota untouched" "0" "$CC"

# 9. topup 明确 deprecated
CODE=$(curl -s -o tmp_topup.json -w '%{http_code}' -b jar_root.txt -X POST http://127.0.0.1:13000/api/topup -H 'Content-Type: application/json' -d '{"user_id":1,"quota":100}')
check "topup deprecated 410" 410 "$CODE"

# 10. dashboard billing 占位端点
SUB=$(curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:13000/v1/dashboard/billing/subscription)
echo "$SUB" | grep -q billing_subscription && { echo "PASS: subscription shim"; pass=$((pass+1)); } || { echo "FAIL: $SUB"; fail=$((fail+1)); }
USG=$(curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:13000/v1/dashboard/billing/usage)
echo "$USG" | grep -q '"total_usage":0' && { echo "PASS: usage shim zero"; pass=$((pass+1)); } || { echo "FAIL: $USG"; fail=$((fail+1)); }

# 11. plaza 无费用/倍率字段
PL=$(curl -s -b jar_user.txt http://127.0.0.1:13000/api/plaza/models)
echo "plaza: $(echo $PL | head -c 200)"
if echo "$PL" | grep -q "quota_per_unit\|group_ratio\|input_quota_per_1k\|model_ratio\|display_in_currency"; then echo "FAIL: plaza still has cost fields"; fail=$((fail+1)); else echo "PASS: plaza clean"; pass=$((pass+1)); fi

# 12. /v1/responses 非流式：归一化→OpenAI 兼容上游→渲染 Responses 信封
CODE=$(curl -s -o tmp_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"hi"}')
check "responses 200" 200 "$CODE"
grep -q '"object":"response"' tmp_resp.json && grep -q '"status":"completed"' tmp_resp.json \
  && { echo "PASS: responses envelope"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_resp.json)"; fail=$((fail+1)); }
grep -q '"input_tokens":7' tmp_resp.json && grep -q '"output_tokens":5' tmp_resp.json \
  && { echo "PASS: responses usage"; pass=$((pass+1)); } || { echo "FAIL: usage $(head -c 200 tmp_resp.json)"; fail=$((fail+1)); }

# 13. /v1/responses §6.4 拒绝规则（明确参数名，不静默降级）
CODE=$(curl -s -o tmp_resp2.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"hi","previous_response_id":"resp_x"}')
check "responses reject previous_response_id" 400 "$CODE"
grep -q 'previous_response_id' tmp_resp2.json \
  && { echo "PASS: rejection names param"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_resp2.json)"; fail=$((fail+1)); }
CODE=$(curl -s -o tmp_resp3.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"hi","store":true}')
check "responses reject store=true" 400 "$CODE"

# 13b. /v1/responses 流式：typed SSE 事件（阶段 4 §7），不再是 400
curl -s -N -D tmp_resp_sse.h -o tmp_resp_sse.txt -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"hi","stream":true}'
grep -qi 'text/event-stream' tmp_resp_sse.h \
  && { echo "PASS: responses stream content-type"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_resp_sse.h)"; fail=$((fail+1)); }
grep -q 'event: response.created' tmp_resp_sse.txt \
  && grep -q 'event: response.output_text.delta' tmp_resp_sse.txt \
  && grep -q 'event: response.completed' tmp_resp_sse.txt \
  && { echo "PASS: responses typed SSE events"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '"delta":"o"' tmp_resp_sse.txt && grep -q '"delta":"k"' tmp_resp_sse.txt \
  && { echo "PASS: responses deltas not concatenated"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '"input_tokens":7' tmp_resp_sse.txt && grep -q '"output_tokens":5' tmp_resp_sse.txt \
  && { echo "PASS: responses stream reported usage"; pass=$((pass+1)); } || { echo "FAIL: usage $(tail -c 400 tmp_resp_sse.txt)"; fail=$((fail+1)); }

# 14. /v1/messages 走 x-api-key 鉴权 + 渲染 Anthropic 信封
CODE=$(curl -s -o tmp_msg.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"system":"s","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}')
check "messages via x-api-key 200" 200 "$CODE"
grep -q '"type":"message"' tmp_msg.json && grep -q '"stop_reason":"end_turn"' tmp_msg.json \
  && { echo "PASS: messages envelope"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_msg.json)"; fail=$((fail+1)); }
grep -q '"input_tokens":7' tmp_msg.json && grep -q '"output_tokens":5' tmp_msg.json \
  && { echo "PASS: messages usage"; pass=$((pass+1)); } || { echo "FAIL: usage $(head -c 200 tmp_msg.json)"; fail=$((fail+1)); }

# 14b. /v1/messages 流式：typed Anthropic SSE（message_start/…/message_stop，阶段 4 §7）
curl -s -N -D tmp_msg_sse.h -o tmp_msg_sse.txt -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}'
grep -qi 'text/event-stream' tmp_msg_sse.h \
  && { echo "PASS: messages stream content-type"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_msg_sse.h)"; fail=$((fail+1)); }
grep -q 'event: message_start' tmp_msg_sse.txt \
  && grep -q 'event: content_block_delta' tmp_msg_sse.txt \
  && grep -q 'event: message_stop' tmp_msg_sse.txt \
  && { echo "PASS: messages typed SSE events"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_msg_sse.txt)"; fail=$((fail+1)); }
grep -q '"stop_reason":"end_turn"' tmp_msg_sse.txt \
  && { echo "PASS: messages finish_reason→stop_reason mapping"; pass=$((pass+1)); } || { echo "FAIL: $(tail -c 400 tmp_msg_sse.txt)"; fail=$((fail+1)); }

# 15. /v1/messages §6.5 冲突鉴权（x-api-key 与 Authorization 指向不同令牌→401）
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H "Authorization: Bearer sk-differenttoken" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}')
check "messages conflict auth rejected 401" 401 "$CODE"

# 16. /v1/messages 缺 max_tokens → 结构化 400
CODE=$(curl -s -o tmp_msg2.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}')
check "messages require max_tokens 400" 400 "$CODE"

# 16b. §8.1 缓存字段贯通：带 usecache 的请求让 mock 上报 cached_tokens
CODE=$(curl -s -o tmp_cache.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"usecache please"}]}')
check "cache relay 200" 200 "$CODE"
sleep 1
CR=$(sqlite3 one-api.db "select cache_usage_status||'|'||coalesce(cache_read_input_tokens,-1)||'|'||usage_source from usage_records order by id desc limit 1;")
check "cached_tokens→reported cache_read=3" "reported|3|reported" "$CR"

# 17. 三种协议入口的成功生成都应落 usage_records
#     chat(2: 基础 + usecache) + responses 非流式(1) + responses 流式(1) + messages 非流式(1) + messages 流式(1) = 6
sleep 1
SUC=$(sqlite3 one-api.db "select count(*) from usage_records where status='success';")
[ "$SUC" = 6 ] && { echo "PASS: usage_records 6 success rows"; pass=$((pass+1)); } || { echo "FAIL: success rows=$SUC"; fail=$((fail+1)); }

# 18. 所有 success 行都以 reported 收尾（mock 发了 usage/usage 尾块），绝不为零命中伪造
REP=$(sqlite3 one-api.db "select count(*) from usage_records where status='success' and usage_source='reported';")
[ "$REP" = 6 ] && { echo "PASS: all 6 success rows reported"; pass=$((pass+1)); } || { echo "FAIL: reported rows=$REP"; fail=$((fail+1)); }
# 19. 上游未报缓存的行 cache_usage_status=not_reported（非 invalid、非伪造命中）
NR=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='not_reported';")
[ "$NR" = 5 ] && { echo "PASS: 5 not_reported rows (upstream silent)"; pass=$((pass+1)); } || { echo "FAIL: not_reported rows=$NR"; fail=$((fail+1)); }
# 20. §8.1 上报缓存的行恰好 1 条且计入 cache_read，写入为 0（未上报→NULL）
RCC=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='reported';")
[ "$RCC" = 1 ] && { echo "PASS: exactly 1 reported-cache row"; pass=$((pass+1)); } || { echo "FAIL: reported-cache rows=$RCC"; fail=$((fail+1)); }
# 21. 管理端聚合三指标：cache_hit_rate 仅统计 reported 记录 = 3/7；coverage = 1/6
curl -s -b jar_root.txt 'http://127.0.0.1:13000/api/usage/summary' > tmp_sum.json
python3 - <<'PY' && { echo "PASS: /api/usage/summary metrics"; pass=$((pass+1)); } || { echo "FAIL: summary $(cat tmp_sum.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_sum.json'))['data']
ok=(abs(d['cache_hit_rate']-3/7)<1e-9 and d['call_hit_rate']==1.0
    and abs(d['cache_coverage']-1/6)<1e-9 and d['cache_read_input_tokens']==3
    and d['cache_write_input_tokens']==0 and d['not_reported_cache_calls']==5)
sys.exit(0 if ok else 1)
PY

echo
echo "RESULT pass=$pass fail=$fail"
kill $API_PID $MOCK_PID 2>/dev/null
[ "$fail" = 0 ] || tail -8 oneapi.log
exit $fail
