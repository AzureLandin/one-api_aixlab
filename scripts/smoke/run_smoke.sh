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
      tmp_cache.json tmp_sum.json tmp_models.json tmp_model1.json \
      tmp_resp_sse.h tmp_resp_sse.txt tmp_msg_sse.h tmp_msg_sse.txt mock_last_request.json \
      tmp_tool_chat.json tmp_tool_chat_sse.txt tmp_tool_resp.json tmp_tool_resp_sse.txt \
      tmp_tool_msg.json tmp_tool_msg_sse.txt tmp_tool_turn.json

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
# store=true 不再拒绝：上游不落库，忽略即可（见 relay/protocol/responses_test.go
# TestParseResponses_StoreTrueIsIgnored）。该请求会照常成功并落一行 usage。
CODE=$(curl -s -o tmp_resp3.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"hi","store":true}')
check "responses ignore store=true" 200 "$CODE"

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
#     chat(2: 基础 + usecache) + responses 非流式(1) + responses store=true(1)
#     + responses 流式(1) + messages 非流式(1) + messages 流式(1) = 7
sleep 1
SUC=$(sqlite3 one-api.db "select count(*) from usage_records where status='success';")
[ "$SUC" = 7 ] && { echo "PASS: usage_records 7 success rows"; pass=$((pass+1)); } || { echo "FAIL: success rows=$SUC"; fail=$((fail+1)); }

# 18. 所有 success 行都以 reported 收尾（mock 发了 usage/usage 尾块），绝不为零命中伪造
REP=$(sqlite3 one-api.db "select count(*) from usage_records where status='success' and usage_source='reported';")
[ "$REP" = 7 ] && { echo "PASS: all 7 success rows reported"; pass=$((pass+1)); } || { echo "FAIL: reported rows=$REP"; fail=$((fail+1)); }
# 19. 上游未报缓存的行 cache_usage_status=not_reported（非 invalid、非伪造命中）
NR=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='not_reported';")
[ "$NR" = 6 ] && { echo "PASS: 6 not_reported rows (upstream silent)"; pass=$((pass+1)); } || { echo "FAIL: not_reported rows=$NR"; fail=$((fail+1)); }
# 20. §8.1 上报缓存的行恰好 1 条且计入 cache_read，写入为 0（未上报→NULL）
RCC=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='reported';")
[ "$RCC" = 1 ] && { echo "PASS: exactly 1 reported-cache row"; pass=$((pass+1)); } || { echo "FAIL: reported-cache rows=$RCC"; fail=$((fail+1)); }
# 21. 管理端聚合三指标：cache_hit_rate 仅统计 reported 记录 = 3/7；coverage = 1/7
curl -s -b jar_root.txt 'http://127.0.0.1:13000/api/usage/summary' > tmp_sum.json
python3 - <<'PY' && { echo "PASS: /api/usage/summary metrics"; pass=$((pass+1)); } || { echo "FAIL: summary $(cat tmp_sum.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_sum.json'))['data']
ok=(abs(d['cache_hit_rate']-3/7)<1e-9 and d['call_hit_rate']==1.0
    and abs(d['cache_coverage']-1/7)<1e-9 and d['cache_read_input_tokens']==3
    and d['cache_write_input_tokens']==0 and d['not_reported_cache_calls']==6)
sys.exit(0 if ok else 1)
PY

# 22. 工具调用贯通：三种对外格式都必须把上游的 tool_calls 完整还原
#     mock 在请求带 tools 时返回两个并行工具调用（参数分片交错），
#     这正是"模型无法正常调用工具"的复现形态。
CHAT_TOOLS='[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}},{"type":"function","function":{"name":"get_time","description":"time","parameters":{"type":"object"}}}]'
RESP_TOOLS='[{"type":"function","name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"type":"function","name":"get_time","description":"time","parameters":{"type":"object"}}]'
MSG_TOOLS='[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"name":"get_time","description":"time","input_schema":{"type":"object"}}]'

# 22a. /v1/chat/completions 非流式
CODE=$(curl -s -o tmp_tool_chat.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"weather?"}],"tools":'"$CHAT_TOOLS"'}')
check "chat tools 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: chat tool_calls envelope"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_tool_chat.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_tool_chat.json'))
c=d['choices'][0]
tc=c['message'].get('tool_calls') or []
ok=(c['finish_reason']=='tool_calls' and len(tc)==2
    and [t['function']['name'] for t in tc]==['get_weather','get_time']
    and [t['id'] for t in tc]==['call_mock_a','call_mock_b']
    and json.loads(tc[0]['function']['arguments'])=={'city':'SF'}
    and json.loads(tc[1]['function']['arguments'])=={'tz':'UTC'})
sys.exit(0 if ok else 1)
PY

# 22b. /v1/chat/completions 流式（分片必须按 index 归属到各自的调用）
curl -s -N -o tmp_tool_chat_sse.txt -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":'"$CHAT_TOOLS"'}'
python3 - <<'PY' && { echo "PASS: chat stream tool_calls by index"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 500 tmp_tool_chat_sse.txt)"; fail=$((fail+1)); }
import json,sys
calls={}; finish=[]
for line in open('tmp_tool_chat_sse.txt'):
    if not line.startswith('data: ') or '[DONE]' in line: continue
    try: ch=json.loads(line[6:])
    except Exception: continue
    for c in ch.get('choices') or []:
        if c.get('finish_reason'): finish.append(c['finish_reason'])
        for tc in (c.get('delta') or {}).get('tool_calls') or []:
            s=calls.setdefault(tc['index'],{'id':'','name':'','args':''})
            if tc.get('id'): s['id']=tc['id']
            f=tc.get('function') or {}
            if f.get('name'): s['name']=f['name']
            s['args']+=f.get('arguments') or ''
ok=(sorted(calls)==[0,1]
    and calls[0]['id']=='call_mock_a' and calls[0]['name']=='get_weather'
    and json.loads(calls[0]['args'])=={'city':'SF'}
    and calls[1]['id']=='call_mock_b' and calls[1]['name']=='get_time'
    and json.loads(calls[1]['args'])=={'tz':'UTC'}
    and finish==['tool_calls'])
sys.exit(0 if ok else 1)
PY
grep -q '"finish_reason":""' tmp_tool_chat_sse.txt \
  && { echo "FAIL: empty finish_reason on a non-terminal chunk"; fail=$((fail+1)); } \
  || { echo "PASS: no empty finish_reason sentinel"; pass=$((pass+1)); }

# 22c. /v1/responses 非流式：输出必须是 function_call 项，而不是被丢掉
CODE=$(curl -s -o tmp_tool_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"weather?","tools":'"$RESP_TOOLS"'}')
check "responses tools 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: responses function_call output"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_tool_resp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_tool_resp.json'))
items=[i for i in d.get('output',[]) if i.get('type')=='function_call']
ok=(len(items)==2 and [i['name'] for i in items]==['get_weather','get_time']
    and [i['call_id'] for i in items]==['call_mock_a','call_mock_b']
    and json.loads(items[0]['arguments'])=={'city':'SF'})
sys.exit(0 if ok else 1)
PY

# 22d. /v1/responses 流式：response.completed 必须自带完整 output（客户端据此取函数调用）
curl -s -N -o tmp_tool_resp_sse.txt -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","input":"weather?","stream":true,"tools":'"$RESP_TOOLS"'}'
python3 - <<'PY' && { echo "PASS: responses stream function_call events + completed output"; pass=$((pass+1)); } || { echo "FAIL: $(tail -c 600 tmp_tool_resp_sse.txt)"; fail=$((fail+1)); }
import json,sys
events={}
for line in open('tmp_tool_resp_sse.txt'):
    if not line.startswith('data: '): continue
    try: ev=json.loads(line[6:])
    except Exception: continue
    events.setdefault(ev.get('type'),[]).append(ev)
added=[e['item'] for e in events.get('response.output_item.added',[]) if e['item'].get('type')=='function_call']
argdeltas=events.get('response.function_call_arguments.delta',[])
deltas=events.get('response.function_call_arguments.done',[])
completed=events.get('response.completed',[])
final=[i for i in (completed[-1]['response'].get('output') if completed else []) if i.get('type')=='function_call']
ok=(len(added)==2 and [i['name'] for i in added]==['get_weather','get_time']
    and all('output_index' in e for e in argdeltas)
    and all(e.get('call_id') is None for e in argdeltas)
    and len(deltas)==2 and len(final)==2
    and json.loads(final[0]['arguments'])=={'city':'SF'}
    and json.loads(final[1]['arguments'])=={'tz':'UTC'}
    and [i['status'] for i in final]==['completed','completed'])
sys.exit(0 if ok else 1)
PY

# 22e. /v1/messages 非流式
CODE=$(curl -s -o tmp_tool_msg.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"messages":[{"role":"user","content":"weather?"}],"tools":'"$MSG_TOOLS"'}')
check "messages tools 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: messages tool_use blocks + stop_reason"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_tool_msg.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_tool_msg.json'))
uses=[b for b in d['content'] if b.get('type')=='tool_use']
ok=(d['stop_reason']=='tool_use' and len(uses)==2
    and [u['name'] for u in uses]==['get_weather','get_time']
    and [u['id'] for u in uses]==['call_mock_a','call_mock_b']
    and uses[0]['input']=={'city':'SF'} and uses[1]['input']=={'tz':'UTC'})
sys.exit(0 if ok else 1)
PY

# 22f. /v1/messages 流式：tool_use 块必须有 id/name 且参数按块归属
curl -s -N -o tmp_tool_msg_sse.txt -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":'"$MSG_TOOLS"'}'
python3 - <<'PY' && { echo "PASS: messages stream tool_use blocks"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 600 tmp_tool_msg_sse.txt)"; fail=$((fail+1)); }
import json,sys
starts={}; args={}; stops=set(); stop_reason=None
for line in open('tmp_tool_msg_sse.txt'):
    if not line.startswith('data: '): continue
    try: ev=json.loads(line[6:])
    except Exception: continue
    t=ev.get('type')
    if t=='content_block_start' and ev['content_block'].get('type')=='tool_use':
        starts[ev['index']]=ev['content_block']
    elif t=='content_block_delta' and ev['delta'].get('type')=='input_json_delta':
        args[ev['index']]=args.get(ev['index'],'')+ev['delta']['partial_json']
    elif t=='content_block_stop':
        stops.add(ev['index'])
    elif t=='message_delta':
        stop_reason=(ev.get('delta') or {}).get('stop_reason')
ok=(len(starts)==2 and stop_reason=='tool_use'
    and [b['name'] for b in starts.values()]==['get_weather','get_time']
    and all(b['id'].startswith('call_mock_') for b in starts.values())
    and json.loads(args[0])=={'city':'SF'} and json.loads(args[1])=={'tz':'UTC'}
    and set(starts)<=stops)
sys.exit(0 if ok else 1)
PY

# 22g. 多轮工具对话：assistant 的 text+tool_use 必须合成同一条消息，tool_result 必须带 name
curl -s -o tmp_tool_turn.json -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-3.5-turbo","max_tokens":64,"tools":'"$MSG_TOOLS"',"messages":[
        {"role":"user","content":"weather?"},
        {"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"call_mock_a","name":"get_weather","input":{"city":"SF"}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_mock_a","content":"sunny"}]}]}'
python3 - <<'PY' && { echo "PASS: multi-turn tool conversation upstream shape"; pass=$((pass+1)); } || { echo "FAIL: $(python3 -c "import json;print(json.dumps(json.load(open('mock_last_request.json')).get('messages'),ensure_ascii=False))" 2>/dev/null | head -c 400)"; fail=$((fail+1)); }
import json,sys
msgs=json.load(open('mock_last_request.json'))['messages']
# system? no. Expect exactly: user, assistant(text+tool_calls), tool, assistant(reply)
assistant=[m for m in msgs if m.get('role')=='assistant' and m.get('tool_calls')]
tools=[m for m in msgs if m.get('role')=='tool']
ok=(len(assistant)==1 and assistant[0].get('content')=='checking'
    and len(assistant[0]['tool_calls'])==1
    and assistant[0]['tool_calls'][0]['function']['name']=='get_weather'
    and len(tools)==1 and tools[0].get('tool_call_id')=='call_mock_a'
    and tools[0].get('name')=='get_weather'
    and tools[0].get('content')=='sunny')
sys.exit(0 if ok else 1)
PY

echo
echo "RESULT pass=$pass fail=$fail"
kill $API_PID $MOCK_PID 2>/dev/null
[ "$fail" = 0 ] || tail -8 oneapi.log
exit $fail
