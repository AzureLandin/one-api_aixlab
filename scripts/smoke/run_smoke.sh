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
      tmp_tool_msg.json tmp_tool_msg_sse.txt tmp_tool_turn.json \
      tmp_ch_anthropic.json tmp_ch_gemini.json tmp_pass_msg.json tmp_pass_msg_sse.h \
      tmp_pass_msg_sse.txt tmp_pass_reject.json tmp_pass_chat.json mock_last_messages.json \
      tmp_xlate_resp.json tmp_xlate_resp_sse.h tmp_xlate_resp_sse.txt mock_last_responses.json \
      tmp_ch_responses.json tmp_chat_resp.json tmp_chat_resp_sse.txt tmp_msg_resp.json \
      tmp_msg_resp_sse.txt tmp_tool_chatresp.json tmp_tool_msgresp.json tmp_cache_resp.json \
      tmp_pass_resp.json tmp_pass_resp_cache.json tmp_pass_resp_sse.h tmp_pass_resp_sse.txt \
      tmp_pass_resp_tools_sse.txt

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

# 16c. 同协议直通：/v1/messages → 声明 anthropic-messages 的上游
#      该渠道只列出 claude-mock-sonnet，因此选渠道是确定的（模型名即能力键）。
#      channel.model_mapping 让"定点补丁只换 model"这一条也能被端到端验证。
curl -s -b jar_root.txt -X POST http://127.0.0.1:13000/api/channel/ \
  -H 'Content-Type: application/json' \
  -d '{"name":"mock-anthropic","type":14,"base_url":"http://127.0.0.1:19999","key":"sk-anthropic",
       "models":"claude-mock-sonnet","group":"default","priority":1,"weight":1,"status":1,
       "model_mapping":"{\"claude-mock-sonnet\":\"claude-upstream-actual\"}",
       "config":"{\"upstream_protocol\":\"anthropic-messages\"}"}' > tmp_ch_anthropic.json
check "create anthropic-messages channel" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_ch_anthropic.json)"

# 客户端自己的请求体：thinking / cache_control / hosted tool 声明都是规范化路径
# 表达不了的字段，直通必须原样带过去（结构化往返会丢，甚至会被具名拒绝）。
PASS_HEAD='{"model":"claude-mock-sonnet","max_tokens":64,'
PASS_BODY='"system":[{"type":"text","text":"be brief","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5},{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}'
BETA='computer-use-2025-01-24'

# 16c-1 非流式：客户端收到的是上游自己的信封，不是重新渲染的
CODE=$(curl -s -o tmp_pass_msg.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H "anthropic-beta: $BETA" \
  -H 'Content-Type: application/json' -d "$PASS_HEAD$PASS_BODY")
check "passthrough messages 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: passthrough forwards the upstream envelope"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_pass_msg.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_pass_msg.json'))
# 上游自己的 id + 一个本仓库 Content 结构体没有槽位的 thinking 签名：
# 只要响应被重新渲染过，signature 就会消失。
ok=(d.get('id')=='msg_mock' and d.get('type')=='message' and d.get('stop_reason')=='end_turn'
    and [b.get('type') for b in d.get('content',[])]==['thinking','text']
    and d['content'][0].get('signature')=='sig_mock')
sys.exit(0 if ok else 1)
PY
sleep 1
MR=$(sqlite3 one-api.db "select status||'|'||usage_source||'|'||input_tokens||'|'||output_tokens||'|'||cache_usage_status||'|'||coalesce(cache_read_input_tokens,-1)||'|'||coalesce(cache_write_input_tokens,-1)||'|'||total_tokens from usage_records order by id desc limit 1;")
check "passthrough non-stream usage fact" "success|reported|50|12|reported|40|-1|62" "$MR"

# 16c-2 流式：逐事件镜像上游 SSE（含 thinking 块与缓存字段），无 [DONE] 尾标
curl -s -N -D tmp_pass_msg_sse.h -o tmp_pass_msg_sse.txt -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H "anthropic-beta: $BETA" \
  -H 'Content-Type: application/json' -d "$PASS_HEAD\"stream\":true,$PASS_BODY"
grep -qi 'text/event-stream' tmp_pass_msg_sse.h \
  && { echo "PASS: passthrough stream content-type"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_pass_msg_sse.h)"; fail=$((fail+1)); }
grep -q 'event: message_start' tmp_pass_msg_sse.txt \
  && grep -q 'event: content_block_delta' tmp_pass_msg_sse.txt \
  && grep -q 'event: message_delta' tmp_pass_msg_sse.txt \
  && grep -q 'event: message_stop' tmp_pass_msg_sse.txt \
  && { echo "PASS: passthrough mirrors upstream SSE events"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_pass_msg_sse.txt)"; fail=$((fail+1)); }
grep -q '"thinking_delta"' tmp_pass_msg_sse.txt \
  && grep -q '"cache_read_input_tokens": *40' tmp_pass_msg_sse.txt \
  && { echo "PASS: passthrough keeps thinking + cache fields on the wire"; pass=$((pass+1)); } || { echo "FAIL: $(cat tmp_pass_msg_sse.txt)"; fail=$((fail+1)); }
# 逐字节镜像：上游 payload 里的空白也原样抵达客户端。Go 侧任何一次
# json.Marshal 重新序列化都会去掉 `": "` 的空格，所以这条断言直接证明事件没有被重渲染。
grep -qF 'data: {"type": "message_stop"}' tmp_pass_msg_sse.txt \
  && grep -qF '{"type": "thinking", "thinking": ""}' tmp_pass_msg_sse.txt \
  && { echo "PASS: upstream event bytes mirrored verbatim"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_pass_msg_sse.txt)"; fail=$((fail+1)); }
grep -q '\[DONE\]' tmp_pass_msg_sse.txt \
  && { echo "FAIL: [DONE] leaked into an Anthropic stream"; fail=$((fail+1)); } \
  || { echo "PASS: no OpenAI [DONE] marker in messages stream"; pass=$((pass+1)); }
# 用量归集：message_delta 的 output_tokens 是累计值，赋值而非累加（累加会得 13）
sleep 1
CUM=$(sqlite3 one-api.db "select count(*) from usage_records where model_name='claude-upstream-actual' and status='success' and usage_source='reported' and input_tokens=50 and output_tokens=12 and total_tokens=62 and cache_read_input_tokens=40 and cache_write_input_tokens is null;")
[ "$CUM" = 2 ] && { echo "PASS: passthrough stream usage cumulative, absent cache NULL"; pass=$((pass+1)); } || { echo "FAIL: matching passthrough rows=$CUM (want 2)"; fail=$((fail+1)); }

# 16c-3 上游收到的是客户端自己的字节：模型已映射，其余字段逐字未改
python3 - <<'PY' && { echo "PASS: upstream request kept the client's native fields"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 mock_last_messages.json)"; fail=$((fail+1)); }
import json,sys
r=json.load(open('mock_last_messages.json'))
b=r['body'] or {}
ok=(b.get('model')=='claude-upstream-actual'
    and b.get('thinking')=={'type':'enabled','budget_tokens':2048}
    and b['system'][0]['cache_control']=={'type':'ephemeral'}
    and b['messages'][0]['content'][0]['cache_control']=={'type':'ephemeral'}
    and b['tools'][0]['type']=='web_search_20250305'
    and b['tools'][0]['max_uses']==5
    and b['tools'][1]['input_schema']['properties']['city']['type']=='string'
    and b.get('stream') is True)
sys.exit(0 if ok else 1)
PY
python3 - <<'PY' && { echo "PASS: anthropic-beta merged, not overwritten"; pass=$((pass+1)); } || { echo "FAIL: beta=$(python3 -c "import json;print(json.load(open('mock_last_messages.json'))['anthropic-beta'])")"; fail=$((fail+1)); }
import json,sys
beta=json.load(open('mock_last_messages.json'))['anthropic-beta']
got=[p.strip() for p in beta.split(',') if p.strip()]
sys.exit(0 if 'computer-use-2025-01-24' in got and 'messages-2023-12-15' in got else 1)
PY

# 16d. 能力拒绝格子：不翻译的渠道族（Gemini）在 /v1/responses 入口被按能力排除
curl -s -b jar_root.txt -X POST http://127.0.0.1:13000/api/channel/ \
  -H 'Content-Type: application/json' \
  -d '{"name":"mock-gemini","type":24,"base_url":"http://127.0.0.1:19999","key":"sk-gemini",
       "models":"gemini-pro","group":"default","priority":1,"weight":1,"status":1}' > tmp_ch_gemini.json
check "create untranslated channel" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_ch_gemini.json)"
CODE=$(curl -s -o tmp_pass_reject.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gemini-pro","input":"hi"}')
check "untranslated channel refused on /v1/responses" 503 "$CODE"
grep -q 'anthropic-messages\|openai-responses\|客户端协议' tmp_pass_reject.json \
  && { echo "PASS: refusal names the client protocol"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_pass_reject.json)"; fail=$((fail+1)); }

# 16e. 跨协议翻译格子：/v1/responses 客户端 → anthropic-messages 上游
#      同一个 mock-anthropic 渠道，但客户端说的是另一种协议，所以走 codec 而不是直通：
#      请求经 anthropic.ConvertRequest 改写为 Messages 体，响应经 ResponseClaude2OpenAI /
#      StreamResponseClaude2OpenAI 折回规范化 TextResponse/chunk，再由 Responses 渲染器
#      包成 response.* 信封。放在 16c-3 之后——这些请求同样会覆盖 mock_last_messages.json。
#      已知有损：thinking 块在 OpenAI 归一化形态里没有槽位，本格丢弃（与既有 chat pivot
#      同）；下面的断言反过来把这个损失钉住——输出里只应出现 text 块的内容。
CODE=$(curl -s -o tmp_xlate_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"claude-mock-sonnet","input":"hi","max_output_tokens":64}')
check "cross-protocol responses→anthropic 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: responses envelope from a translated Anthropic body"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_xlate_resp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_xlate_resp.json'))
items=d.get('output') or []
content=(items[0].get('content') if items else None) or []
u=d.get('usage') or {}
ok=(d.get('object')=='response' and d.get('status')=='completed'
    and [i.get('type') for i in items]==['message']
    and [p.get('text') for p in content]==['ok']
    and (u.get('input_tokens'),u.get('output_tokens'),u.get('total_tokens'))==(50,12,62))
sys.exit(0 if ok else 1)
PY
# 上游收到的是 Messages 体（转换后的形态），不是客户端自己的 Responses 体
python3 - <<'PY' && { echo "PASS: codec rewrote the body into the Messages shape"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 mock_last_messages.json)"; fail=$((fail+1)); }
import json,sys
b=json.load(open('mock_last_messages.json'))['body'] or {}
ok=(b.get('model')=='claude-upstream-actual' and isinstance(b.get('messages'),list)
    and isinstance(b.get('max_tokens'),int) and 'input' not in b and 'instructions' not in b)
sys.exit(0 if ok else 1)
PY

# 16e-1 流式：Anthropic SSE 被翻成规范化 chunk，再由 Responses 渲染器发 response.* 事件
curl -s -N -D tmp_xlate_resp_sse.h -o tmp_xlate_resp_sse.txt -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"claude-mock-sonnet","input":"hi","max_output_tokens":64,"stream":true}'
grep -qi 'text/event-stream' tmp_xlate_resp_sse.h \
  && { echo "PASS: cross-protocol responses stream content-type"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_xlate_resp_sse.h)"; fail=$((fail+1)); }
grep -q 'event: response.output_text.delta' tmp_xlate_resp_sse.txt \
  && grep -q 'event: response.completed' tmp_xlate_resp_sse.txt \
  && { echo "PASS: translated Anthropic stream renders response.* events"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_xlate_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '"delta":"o"' tmp_xlate_resp_sse.txt && grep -q '"delta":"k"' tmp_xlate_resp_sse.txt \
  && { echo "PASS: cross-protocol deltas not concatenated"; pass=$((pass+1)); } || { echo "FAIL: $(tail -c 400 tmp_xlate_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '"input_tokens":50' tmp_xlate_resp_sse.txt && grep -q '"output_tokens":12' tmp_xlate_resp_sse.txt \
  && { echo "PASS: cross-protocol stream reported usage"; pass=$((pass+1)); } || { echo "FAIL: usage $(tail -c 400 tmp_xlate_resp_sse.txt)"; fail=$((fail+1)); }
# 用量事实与直通路径落在同一组数字上：同一份上游响应，镜像与翻译不得各记一套
sleep 1
XU=$(sqlite3 one-api.db "select count(*) from usage_records where model_name='claude-upstream-actual' and status='success' and usage_source='reported' and input_tokens=50 and output_tokens=12 and total_tokens=62 and cache_read_input_tokens=40 and cache_write_input_tokens is null;")
[ "$XU" = 4 ] && { echo "PASS: translated rows match the mirrored ones (4 = 2 mirror + 2 codec)"; pass=$((pass+1)); } || { echo "FAIL: matching anthropic rows=$XU (want 4)"; fail=$((fail+1)); }

# 工具声明（三种客户端协议各自的形状）。定义在这里，供 16f 的跨协议工具格子和
# 后面的 22 用同一份夹具。
CHAT_TOOLS='[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}},{"type":"function","function":{"name":"get_time","description":"time","parameters":{"type":"object"}}}]'
RESP_TOOLS='[{"type":"function","name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"type":"function","name":"get_time","description":"time","parameters":{"type":"object"}}]'
MSG_TOOLS='[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"name":"get_time","description":"time","input_schema":{"type":"object"}}]'

# 16f. 第三个上游协议：声明 openai-responses 的 OpenAI 系渠道。
#      chat 入口经 RelayTextHelper 的分支直接驱动转换器，messages 入口经 codec 驱动同一套
#      转换器与同一个读流器——两条路都把上游的 response.* 事件翻成规范化 chat chunk，
#      再交给各自的客户端渲染器。上游的 usage 用 Responses 自己的字段名。
curl -s -b jar_root.txt -X POST http://127.0.0.1:13000/api/channel/ \
  -H 'Content-Type: application/json' \
  -d '{"name":"mock-responses","type":1,"base_url":"http://127.0.0.1:19999","key":"sk-responses",
       "models":"gpt-5-mock","group":"default","priority":1,"weight":1,"status":1,
       "config":"{\"upstream_protocol\":\"openai-responses\"}"}' > tmp_ch_responses.json
check "create openai-responses channel" true "$(sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' tmp_ch_responses.json)"

# 16f-1 chat → responses 非流式：客户端拿 chat.completion，用量来自 response.usage
CODE=$(curl -s -o tmp_chat_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","messages":[{"role":"user","content":"hi"}]}')
check "chat→responses 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: chat envelope from a Responses upstream"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_chat_resp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_chat_resp.json'))
c=(d.get('choices') or [{}])[0]
u=d.get('usage') or {}
ok=(d.get('object')=='chat.completion' and c.get('finish_reason')=='stop'
    and (c.get('message') or {}).get('content')=='ok'
    and (u.get('prompt_tokens'),u.get('completion_tokens'),u.get('total_tokens'))==(7,5,12))
sys.exit(0 if ok else 1)
PY
# 16f-2 上游收到的是 Responses 体而非 chat 体：input 条目、没有 messages。
python3 - <<'PY' && { echo "PASS: upstream body is a Responses body"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 mock_last_responses.json)"; fail=$((fail+1)); }
import json,sys
b=json.load(open('mock_last_responses.json'))['body'] or {}
ok=('input' in b and 'messages' not in b and isinstance(b.get('input'),list)
    and b['input'][0].get('role')=='user')
sys.exit(0 if ok else 1)
PY

# 16f-3 chat → responses 流式：逐 chunk 输出 chat.completion.chunk，末尾补 [DONE]
#      （Responses 上游自己没有结束标记，[DONE] 是 chat 路径写出来的）
curl -s -N -o tmp_chat_resp_sse.txt -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","stream":true,"messages":[{"role":"user","content":"hi"}]}'
python3 - <<'PY' && { echo "PASS: responses upstream streamed as chat chunks"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 500 tmp_chat_resp_sse.txt)"; fail=$((fail+1)); }
import json,sys
deltas=[]; usage=None; done=False
for line in open('tmp_chat_resp_sse.txt'):
    if line.strip()=='data: [DONE]': done=True
    if not line.startswith('data: ') or '[DONE]' in line: continue
    try: ch=json.loads(line[6:])
    except Exception: continue
    if ch.get('usage'): usage=ch['usage']
    for c in ch.get('choices') or []:
        delta=c.get('delta') or {}
        if 'content' in delta: deltas.append(delta['content'])
ok=(done and deltas==['o','k'] and usage is not None
    and (usage.get('prompt_tokens'),usage.get('completion_tokens'))==(7,5))
sys.exit(0 if ok else 1)
PY

# 16f-4 messages → responses 非流式：codec 用同一套转换器，system 折进 instructions
CODE=$(curl -s -o tmp_msg_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"hi"}]}')
check "messages→responses 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: messages envelope from a Responses upstream"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_msg_resp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_msg_resp.json'))
blocks=d.get('content') or []
u=d.get('usage') or {}
ok=(d.get('type')=='message' and d.get('stop_reason')=='end_turn'
    and [b.get('text') for b in blocks if b.get('type')=='text']==['ok']
    and (u.get('input_tokens'),u.get('output_tokens'))==(7,5))
sys.exit(0 if ok else 1)
PY
python3 - <<'PY' && { echo "PASS: codec folded the system turn into instructions"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 mock_last_responses.json)"; fail=$((fail+1)); }
import json,sys
b=json.load(open('mock_last_responses.json'))['body'] or {}
sys.exit(0 if b.get('instructions')=='be brief' and 'messages' not in b else 1)
PY

# 16f-5 messages → responses 流式：response.* 被翻成 Anthropic 事件
curl -s -N -o tmp_msg_resp_sse.txt -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}'
grep -q 'event: message_start' tmp_msg_resp_sse.txt \
  && grep -q 'event: content_block_delta' tmp_msg_resp_sse.txt \
  && grep -q 'event: message_delta' tmp_msg_resp_sse.txt \
  && grep -q 'event: message_stop' tmp_msg_resp_sse.txt \
  && { echo "PASS: responses upstream renders Anthropic typed SSE"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_msg_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '"stop_reason":"end_turn"' tmp_msg_resp_sse.txt \
  && grep -q '"output_tokens":5' tmp_msg_resp_sse.txt \
  && { echo "PASS: responses stream usage reached the Anthropic tail"; pass=$((pass+1)); } || { echo "FAIL: $(tail -c 400 tmp_msg_resp_sse.txt)"; fail=$((fail+1)); }

# 16f-6 chat → responses 工具调用：两个并行调用的参数分片交错，仍要落到稠密的 tool index
CODE=$(curl -s -o tmp_tool_chatresp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","messages":[{"role":"user","content":"weather?"}],"tools":'"$CHAT_TOOLS"'}')
check "chat→responses tools 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: chat tool_calls from a Responses upstream"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_tool_chatresp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_tool_chatresp.json'))
c=d['choices'][0]
tc=c['message'].get('tool_calls') or []
ok=(c['finish_reason']=='tool_calls' and len(tc)==2
    and [t['function']['name'] for t in tc]==['get_weather','get_time']
    and [t['id'] for t in tc]==['call_mock_a','call_mock_b']
    and json.loads(tc[0]['function']['arguments'])=={'city':'SF'}
    and json.loads(tc[1]['function']['arguments'])=={'tz':'UTC'})
sys.exit(0 if ok else 1)
PY

# 16f-7 messages → responses 工具调用：function_call 项折回 tool_use 块
CODE=$(curl -s -o tmp_tool_msgresp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","max_tokens":64,"messages":[{"role":"user","content":"weather?"}],"tools":'"$MSG_TOOLS"'}')
check "messages→responses tools 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: messages tool_use blocks from a Responses upstream"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_tool_msgresp.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_tool_msgresp.json'))
uses=[b for b in d['content'] if b.get('type')=='tool_use']
ok=(d['stop_reason']=='tool_use' and len(uses)==2
    and [u['name'] for u in uses]==['get_weather','get_time']
    and [u['id'] for u in uses]==['call_mock_a','call_mock_b']
    and uses[0]['input']=={'city':'SF'} and uses[1]['input']=={'tz':'UTC'})
sys.exit(0 if ok else 1)
PY

# 16f-8 Responses 上游的缓存字段：input_tokens_details.cached_tokens → cache_read_input_tokens
CODE=$(curl -s -o tmp_cache_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","messages":[{"role":"user","content":"usecache please"}]}')
check "chat→responses cache 200" 200 "$CODE"
sleep 1
CR=$(sqlite3 one-api.db "select cache_usage_status||'|'||coalesce(cache_read_input_tokens,-1)||'|'||usage_source from usage_records order by id desc limit 1;")
check "responses cached_tokens→reported cache_read=3" "reported|3|reported" "$CR"

# 16g. 第二个直通格子：/v1/responses 客户端 → 声明 openai-responses 的上游。
#      16f 的四个格子都是跨协议翻译（客户端说的是别的协议），这里客户端说的就是 Responses，
#      于是走直通：请求体原样过去（只补 model），上游的信封与事件原样回来，usage 由镜像
#      从终态 response.completed 的 response.usage 取。previous_response_id 是转换路径
#      明确拒绝的字段（responses.go 的解析器不接受它），能被原样送到上游正是直通的意义。
PASS_RESP_HEAD='{"model":"gpt-5-mock","instructions":"be brief","store":true,"previous_response_id":"resp_prev","include":["reasoning.encrypted_content"],"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}},'
PASS_RESP_BODY='"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'

# 16g-1 非流式：客户端收到的是上游自己的信封，不是重新渲染的
CODE=$(curl -s -o tmp_pass_resp.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$PASS_RESP_HEAD$PASS_RESP_BODY")
check "passthrough responses 200" 200 "$CODE"
python3 - <<'PY' && { echo "PASS: passthrough forwards the upstream envelope"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_pass_resp.json)"; fail=$((fail+1)); }
import json,sys
raw=open('tmp_pass_resp.json').read()
d=json.loads(raw)
u=d.get('usage') or {}
ok=(d.get('id')=='resp_mock' and d.get('object')=='response' and d.get('status')=='completed'
    and [i.get('type') for i in d.get('output',[])]==['message']
    and d['output'][0]['content'][0].get('text')=='ok'
    and (u.get('input_tokens'),u.get('output_tokens'),u.get('total_tokens'))==(7,5,12))
# 逐字节回写：上游 payload 里的空白（json.dumps 的 ", " / ": "）也原样抵达客户端，
# 任何一次 Go 侧重新序列化都会去掉它们。
ok = ok and '"object": "response"' in raw and '"status": "completed"' in raw
sys.exit(0 if ok else 1)
PY
sleep 1
PR=$(sqlite3 one-api.db "select status||'|'||usage_source||'|'||input_tokens||'|'||output_tokens||'|'||total_tokens||'|'||cache_usage_status from usage_records order by id desc limit 1;")
check "passthrough responses non-stream usage fact" "success|reported|7|5|12|not_reported" "$PR"

# 16g-2 上游收到的是客户端自己的字节：只补过 model
python3 - <<'PY' && { echo "PASS: upstream requests kept the client's native fields"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 mock_last_responses.json)"; fail=$((fail+1)); }
import json,sys
b=json.load(open('mock_last_responses.json'))['body'] or {}
ok=(b.get('model')=='gpt-5-mock' and b.get('instructions')=='be brief'
    and b.get('store') is True and b.get('previous_response_id')=='resp_prev'
    and b.get('include')==['reasoning.encrypted_content']
    and b['text']['format']['type']=='json_schema'
    and b['input'][0]['content'][0]['type']=='input_text'
    and 'messages' not in b)
sys.exit(0 if ok else 1)
PY

# 16g-3 流式：逐事件镜像上游 SSE，无 [DONE] 尾标
curl -s -N -D tmp_pass_resp_sse.h -o tmp_pass_resp_sse.txt -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "${PASS_RESP_HEAD}\"stream\":true,${PASS_RESP_BODY}"
grep -qi 'text/event-stream' tmp_pass_resp_sse.h \
  && { echo "PASS: passthrough responses stream content-type"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 200 tmp_pass_resp_sse.h)"; fail=$((fail+1)); }
grep -q 'event: response.created' tmp_pass_resp_sse.txt \
  && grep -q 'event: response.output_text.delta' tmp_pass_resp_sse.txt \
  && grep -q 'event: response.completed' tmp_pass_resp_sse.txt \
  && { echo "PASS: passthrough mirrors upstream response.* events"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 400 tmp_pass_resp_sse.txt)"; fail=$((fail+1)); }
grep -qF 'data: {"type": "response.output_text.delta"' tmp_pass_resp_sse.txt \
  && { echo "PASS: upstream event bytes mirrored verbatim"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 300 tmp_pass_resp_sse.txt)"; fail=$((fail+1)); }
grep -q '\[DONE\]' tmp_pass_resp_sse.txt \
  && { echo "FAIL: [DONE] leaked into a Responses stream"; fail=$((fail+1)); } \
  || { echo "PASS: no OpenAI [DONE] marker in responses stream"; pass=$((pass+1)); }
sleep 1
PRS=$(sqlite3 one-api.db "select status||'|'||usage_source||'|'||input_tokens||'|'||output_tokens||'|'||total_tokens from usage_records order by id desc limit 1;")
check "passthrough responses stream usage fact" "success|reported|7|5|12" "$PRS"

# 16g-4 工具调用：两个 function_call 项（含 call_id）与交错的参数分片原样镜像
RESP_TOOLS='[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}},{"type":"function","name":"get_time","parameters":{"type":"object"}}]'
curl -s -N -o tmp_pass_resp_tools_sse.txt -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"model\":\"gpt-5-mock\",\"stream\":true,\"tools\":$RESP_TOOLS,\"input\":\"weather?\"}"
python3 - <<'PY' && { echo "PASS: responses tool items and argument fragments mirrored in upstream order"; pass=$((pass+1)); } || { echo "FAIL: $(head -c 500 tmp_pass_resp_tools_sse.txt)"; fail=$((fail+1)); }
import json,sys
txt=open('tmp_pass_resp_tools_sse.txt').read()
ids=[]
for line in txt.splitlines():
    if not line.startswith('data: ') or 'function_call_arguments.delta' not in line:
        continue
    ids.append(json.loads(line[6:]).get('item_id'))
# 上游先发第二个调用的首片：直通按上游自己的顺序镜像（翻译路径必须重排成稠密 index）
ok=(ids==['fc_mock_b','fc_mock_a','fc_mock_b','fc_mock_a']
    and '"call_id": "call_mock_a"' in txt and '"call_id": "call_mock_b"' in txt)
sys.exit(0 if ok else 1)
PY
sleep 1
PRT=$(sqlite3 one-api.db "select status||'|'||usage_source||'|'||input_tokens||'|'||output_tokens||'|'||total_tokens from usage_records order by id desc limit 1;")
check "passthrough responses tool-use usage fact" "success|reported|11|9|20" "$PRT"

# 16g-5 Responses 直通的缓存字段：input_tokens_details.cached_tokens → cache_read_input_tokens
CODE=$(curl -s -o tmp_pass_resp_cache.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/responses \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5-mock","input":"usecache please"}')
check "passthrough responses cache 200" 200 "$CODE"
sleep 1
PRC=$(sqlite3 one-api.db "select cache_usage_status||'|'||coalesce(cache_read_input_tokens,-1)||'|'||usage_source from usage_records order by id desc limit 1;")
check "passthrough responses cached_tokens→reported cache_read=3" "reported|3|reported" "$PRC"

# 17. 三种协议入口的成功生成都应落 usage_records
#     chat(2: 基础 + usecache) + responses 非流式(1) + responses store=true(1)
#     + responses 流式(1) + messages 非流式(1) + messages 流式(1) = 7
#     + 直通 messages 非流式(1) + 直通 messages 流式(1) = 9
#     + 跨协议 responses→anthropic 非流式(1) + 流式(1) = 11
#     + responses 上游：chat→responses 非流式(1) + 流式(1) + 工具(1)
#       + messages→responses 非流式(1) + 流式(1) + 工具(1) + 缓存(1) = 18
#     + 直通 responses 非流式(1) + 流式(1) + 工具流式(1) + 缓存(1) = 22
sleep 1
SUC=$(sqlite3 one-api.db "select count(*) from usage_records where status='success';")
[ "$SUC" = 22 ] && { echo "PASS: usage_records 22 success rows"; pass=$((pass+1)); } || { echo "FAIL: success rows=$SUC"; fail=$((fail+1)); }

# 18. 所有 success 行都以 reported 收尾（mock 发了 usage/usage 尾块），绝不为零命中伪造
REP=$(sqlite3 one-api.db "select count(*) from usage_records where status='success' and usage_source='reported';")
[ "$REP" = 22 ] && { echo "PASS: all 22 success rows reported"; pass=$((pass+1)); } || { echo "FAIL: reported rows=$REP"; fail=$((fail+1)); }
# 19. 上游未报缓存的行 cache_usage_status=not_reported（非 invalid、非伪造命中）
#     跨协议格子与 responses 上游上报缓存的那条都计入 reported，所以计数不变——这同时是
#     "没上报才写 not_reported"的反面证据。
NR=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='not_reported';")
[ "$NR" = 15 ] && { echo "PASS: 15 not_reported rows (upstream silent)"; pass=$((pass+1)); } || { echo "FAIL: not_reported rows=$NR"; fail=$((fail+1)); }
# 20. §8.1 上报缓存的行 7 条：1 条 OpenAI details（3）+ 4 条 Anthropic（各 40）
#     + 2 条 Responses input_tokens_details（各 3：翻译路径 1 条 + 直通 1 条）
RCC=$(sqlite3 one-api.db "select count(*) from usage_records where cache_usage_status='reported';")
[ "$RCC" = 7 ] && { echo "PASS: 7 reported-cache rows"; pass=$((pass+1)); } || { echo "FAIL: reported-cache rows=$RCC"; fail=$((fail+1)); }
# 21. 管理端聚合三指标：cache_hit_rate = 169/221（3+40×4+3×2 命中 / 7+50×4+7×2 上报输入）；coverage = 7/22
curl -s -b jar_root.txt 'http://127.0.0.1:13000/api/usage/summary' > tmp_sum.json
python3 - <<'PY' && { echo "PASS: /api/usage/summary metrics"; pass=$((pass+1)); } || { echo "FAIL: summary $(cat tmp_sum.json)"; fail=$((fail+1)); }
import json,sys
d=json.load(open('tmp_sum.json'))['data']
ok=(abs(d['cache_hit_rate']-169/221)<1e-9 and d['call_hit_rate']==1.0
    and abs(d['cache_coverage']-7/22)<1e-9 and d['cache_read_input_tokens']==169
    and d['cache_write_input_tokens']==0 and d['not_reported_cache_calls']==15)
sys.exit(0 if ok else 1)
PY

# 21b. 同一渠道在 /v1/chat/completions 上仍然可达（它靠自己的 adaptor 服务该入口）。
#      放在用量统计之后：这次上游调用会失败，失败本身也会落一行 error usage。
CODE=$(curl -s -o tmp_pass_chat.json -w '%{http_code}' -X POST http://127.0.0.1:13000/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gemini-pro","messages":[{"role":"user","content":"hi"}]}')
if [ "$CODE" != 503 ] && ! grep -q '客户端协议' tmp_pass_chat.json; then
  echo "PASS: untranslated channel still reachable on chat"; pass=$((pass+1))
else
  echo "FAIL: chat refused the channel (code=$CODE body=$(head -c 200 tmp_pass_chat.json))"; fail=$((fail+1))
fi

# 22. 工具调用贯通：三种对外格式都必须把上游的 tool_calls 完整还原
#     mock 在请求带 tools 时返回两个并行工具调用（参数分片交错），
#     这正是"模型无法正常调用工具"的复现形态。（CHAT_TOOLS/RESP_TOOLS/MSG_TOOLS
#     在 16f 之前定义。）

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
