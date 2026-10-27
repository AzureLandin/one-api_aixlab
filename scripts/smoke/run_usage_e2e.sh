#!/bin/bash
# 端到端缓存口径证明：假上游 → 三种客户端协议，断言客户端收到的 usage 里
# 缓存字段存在、且可加性成立。
#
# 三个上游族各一个渠道，各自只列一个模型，所以选路是确定的：
#   e2e-gpt    OpenAI chat    prompt_tokens=7   + prompt_tokens_details.cached_tokens=3
#   e2e-resp   OpenAI Responses input_tokens=7  + input_tokens_details.cached_tokens=3
#   e2e-claude Anthropic      input_tokens=50   + cache_read_input_tokens=40 (总输入 90)
#
# 对每一族都向 /v1/messages、/v1/responses、/v1/chat/completions 各发一次，
# 断言的不变量只有一条：
#   Anthropic 信封  input_tokens + cache_read + cache_creation == 真实总输入
#   Responses 信封  input_tokens == 真实总输入（cache 是其子集）
#   Chat 信封       prompt_tokens == 真实总输入（cache 是其子集）
# 同时断言上游没报的缓存字段必须缺席，而不是 0。
#
# 与 run_smoke.sh 分开，是因为它用独立的数据库与端口，不扰动冒烟脚本里
# 逐次累计的 input_tokens / cache_hit_rate 等硬编码计数。
#
# 用法：bash scripts/smoke/run_usage_e2e.sh
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
RUN="$HERE/.usage-e2e"
BIN="$RUN/one-api"
PORT=13001

rm -rf "$RUN"
mkdir -p "$RUN"
cd "$RUN"

GOCACHE="$REPO/.gocache" go build -o "$BIN" "$REPO" || { echo "FATAL: build failed"; exit 2; }

PORT=$PORT SESSION_SECRET=usage-e2e TIKTOKEN_CACHE_DIR="$HERE/tiktoken" "$BIN" > oneapi.log 2>&1 &
API_PID=$!
python3 "$HERE/mock_upstream.py" > mock.log 2>&1 &
MOCK_PID=$!
cleanup() { kill $API_PID $MOCK_PID 2>/dev/null; wait $API_PID $MOCK_PID 2>/dev/null; }
trap cleanup EXIT

UP=0
for _ in $(seq 1 240); do
  if curl -s -o /dev/null "http://127.0.0.1:$PORT/api/status"; then UP=1; break; fi
  sleep 1
done
[ "$UP" = 1 ] || { echo "FATAL: server did not start"; tail -30 oneapi.log; exit 2; }
echo "== server up =="

pass=0; fail=0
check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 (expected=$2 actual=$3)"; fail=$((fail+1)); fi
}

API="http://127.0.0.1:$PORT"
ok_json() { sed -n 's/.*"success":\([a-z]*\).*/\1/p;q' "$1"; }

curl -s -c jar_root.txt -X POST "$API/api/user/login" \
  -H 'Content-Type: application/json' -d '{"username":"root","password":"123456"}' > tmp_login.json
check "root login" true "$(ok_json tmp_login.json)"

curl -s -b jar_root.txt -X POST "$API/api/channel/" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-chat","type":1,"base_url":"http://127.0.0.1:19999","key":"sk-chat",
       "models":"e2e-gpt","group":"default","priority":1,"weight":1,"status":1}' > tmp_ch_chat.json
check "create chat channel" true "$(ok_json tmp_ch_chat.json)"

curl -s -b jar_root.txt -X POST "$API/api/channel/" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-resp","type":1,"base_url":"http://127.0.0.1:19999","key":"sk-resp",
       "models":"e2e-resp","group":"default","priority":1,"weight":1,"status":1,
       "config":"{\"upstream_protocol\":\"openai-responses\"}"}' > tmp_ch_resp.json
check "create responses channel" true "$(ok_json tmp_ch_resp.json)"

curl -s -b jar_root.txt -X POST "$API/api/channel/" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-claude","type":14,"base_url":"http://127.0.0.1:19999","key":"sk-claude",
       "models":"e2e-claude","group":"default","priority":1,"weight":1,"status":1,
       "config":"{\"upstream_protocol\":\"anthropic-messages\"}"}' > tmp_ch_claude.json
check "create anthropic channel" true "$(ok_json tmp_ch_claude.json)"

curl -s -b jar_root.txt -X POST "$API/api/user/" -H 'Content-Type: application/json' \
  -d '{"username":"e2e_user","password":"Pass1234!","role":1,"email":"e2e@e2e.cn"}' > tmp_user.json
check "create user" true "$(ok_json tmp_user.json)"

curl -s -c jar_user.txt -X POST "$API/api/user/login" \
  -H 'Content-Type: application/json' -d '{"username":"e2e_user","password":"Pass1234!"}' > tmp_login2.json
check "user login" true "$(ok_json tmp_login2.json)"
curl -s -b jar_user.txt -X POST "$API/api/token/" -H 'Content-Type: application/json' \
  -d '{"name":"e2e","expired_time":-1,"remain_quota":0,"unlimited_quota":true}' > tmp_tk.json
check "create token" true "$(ok_json tmp_tk.json)"
KEY=$(curl -s -b jar_user.txt "$API/api/token/?p=0&n=10" | python3 -c "import sys,json;print(json.load(sys.stdin)['data'][0]['key'])")
[ -n "$KEY" ] && { echo "PASS: token key"; pass=$((pass+1)); } || { echo "FAIL: no token key"; fail=$((fail+1)); }

msg() { # msg <out> <model>
  curl -s -o "$1" -X POST "$API/v1/messages" \
    -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "{\"model\":\"$2\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"usecache please\"}]}"
}
resp() { # resp <out> <model>
  curl -s -o "$1" -X POST "$API/v1/responses" \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$2\",\"input\":\"usecache please\"}"
}
chat() { # chat <out> <model>
  curl -s -o "$1" -X POST "$API/v1/chat/completions" \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$2\",\"messages\":[{\"role\":\"user\",\"content\":\"usecache please\"}]}"
}

# ---- 非流式：3 客户端 × 3 上游 ----
msg  tmp_msg_from_chat.json   e2e-gpt
msg  tmp_msg_from_resp.json   e2e-resp
msg  tmp_msg_from_claude.json e2e-claude
resp tmp_resp_from_chat.json   e2e-gpt
resp tmp_resp_from_resp.json   e2e-resp
resp tmp_resp_from_claude.json e2e-claude
chat tmp_chat_from_chat.json   e2e-gpt
chat tmp_chat_from_resp.json   e2e-resp
chat tmp_chat_from_claude.json e2e-claude

check_anthropic_envelope() { # <file> <total input> <read> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,read,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4]
d=json.load(open(f)); u=d.get('usage') or {}
got=(u.get('input_tokens'),u.get('cache_read_input_tokens'),u.get('cache_creation_input_tokens',0))
ok=(got[0]+got[1]+got[2]==total and got[1]==read
    and 'cache_creation_input_tokens' not in u
    and 'cache_read_input_tokens' in u)
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

check_responses_envelope() { # <file> <total input> <cached> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,cached,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4]
d=json.load(open(f)); u=d.get('usage') or {}
det=u.get('input_tokens_details') or {}
ok=(u.get('input_tokens')==total and det.get('cached_tokens')==cached
    and det.get('cached_tokens',0)<=u.get('input_tokens',0))
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

check_chat_envelope() { # <file> <total input> <cached> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,cached,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4]
d=json.load(open(f)); u=d.get('usage') or {}
det=u.get('prompt_tokens_details') or {}
ok=(u.get('prompt_tokens')==total and det.get('cached_tokens')==cached
    and det.get('cached_tokens',0)<=u.get('prompt_tokens',0))
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

run() { # run <checker> <name> <args...>
  local checker=$1 name=$2; shift 2
  if "$checker" "$@" "$name"; then pass=$((pass+1)); else fail=$((fail+1)); fi
}

# chat / responses 上游的总输入是 7（含缓存 3）；Anthropic 上游是 90（50 + 40）。
run check_anthropic_envelope "messages client from chat upstream"        tmp_msg_from_chat.json   7  3
run check_anthropic_envelope "messages client from responses upstream"   tmp_msg_from_resp.json   7  3
run check_anthropic_envelope "messages client from anthropic upstream"   tmp_msg_from_claude.json 90 40
run check_responses_envelope "responses client from chat upstream"       tmp_resp_from_chat.json   7  3
run check_responses_envelope "responses client from responses upstream"  tmp_resp_from_resp.json   7  3
run check_responses_envelope "responses client from anthropic upstream"  tmp_resp_from_claude.json 90 40
run check_chat_envelope      "chat client from chat upstream"            tmp_chat_from_chat.json   7  3
run check_chat_envelope      "chat client from responses upstream"       tmp_chat_from_resp.json   7  3
run check_chat_envelope      "chat client from anthropic upstream"       tmp_chat_from_claude.json 90 40

# ---- 流式：重点覆盖三条翻译流（chat→messages、responses→messages、
#      anthropic→responses、chat→responses、responses→chat）与一条直通流 ----
stream_req() { # stream_req <client> <out> <model>
  local client=$1 out=$2 model=$3
  case "$client" in
    messages)
      curl -s -N -o "$out" -X POST "$API/v1/messages" \
        -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
        -d "{\"model\":\"$model\",\"max_tokens\":64,\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"usecache please\"}]}" ;;
    responses)
      curl -s -N -o "$out" -X POST "$API/v1/responses" \
        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
        -d "{\"model\":\"$model\",\"input\":\"usecache please\",\"stream\":true}" ;;
    chat)
      curl -s -N -o "$out" -X POST "$API/v1/chat/completions" \
        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
        -d "{\"model\":\"$model\",\"stream\":true,\"stream_options\":{\"include_usage\":true},\"messages\":[{\"role\":\"user\",\"content\":\"usecache please\"}]}" ;;
  esac
}

stream_req messages  sse_msg_chat.txt   e2e-gpt
stream_req messages  sse_msg_resp.txt   e2e-resp
stream_req messages  sse_msg_claude.txt e2e-claude
stream_req responses sse_resp_claude.txt e2e-claude
stream_req responses sse_resp_chat.txt   e2e-gpt
stream_req chat      sse_chat_resp.txt   e2e-resp

# message_delta / message_start 里的 usage 断言。
check_messages_stream() { # <file> <total> <read> <mode start|delta> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,read,mode,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4],sys.argv[5]
u={}
for line in open(f):
    line=line.strip()
    if not line.startswith('data: '): continue
    try: ev=json.loads(line[6:])
    except Exception: continue
    if ev.get('type')==('message_start' if mode=='start' else 'message_delta'):
        u=ev.get('message',{}).get('usage') if mode=='start' else (ev.get('usage') or {})
        if mode=='start' and u: break
got=(u.get('input_tokens'),u.get('cache_read_input_tokens'),u.get('cache_creation_input_tokens',0))
ok=(got[0]+got[1]+got[2]==total and got[1]==read
    and 'cache_creation_input_tokens' not in u and 'cache_read_input_tokens' in u)
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

check_responses_stream() { # <file> <total> <cached> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,cached,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4]
u={}
for line in open(f):
    line=line.strip()
    if not line.startswith('data: '): continue
    try: ev=json.loads(line[6:])
    except Exception: continue
    if ev.get('type')=='response.completed':
        u=(ev.get('response') or {}).get('usage') or {}
det=u.get('input_tokens_details') or {}
ok=(u.get('input_tokens')==total and det.get('cached_tokens')==cached
    and det.get('cached_tokens',0)<=u.get('input_tokens',0))
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

check_chat_stream() { # <file> <total> <cached> <name>
  python3 - "$@" <<'PY'
import json,sys
f,total,cached,name=sys.argv[1],int(sys.argv[2]),int(sys.argv[3]),sys.argv[4]
u={}
for line in open(f):
    line=line.strip()
    if not line.startswith('data: '): continue
    try: ch=json.loads(line[6:])
    except Exception: continue
    if ch.get('usage'): u=ch['usage']
det=u.get('prompt_tokens_details') or {}
ok=(u.get('prompt_tokens')==total and det.get('cached_tokens')==cached
    and det.get('cached_tokens',0)<=u.get('prompt_tokens',0))
print(("PASS: " if ok else "FAIL: ")+name+(" %s"%str(u) if ok else " -> %s"%str(u)))
sys.exit(0 if ok else 1)
PY
}

# 翻译流：message_delta 现在携带完整 usage（残差 + 缓存）。
if check_messages_stream sse_msg_chat.txt 7 3 delta "messages stream from chat upstream (translated)"; then pass=$((pass+1)); else fail=$((fail+1)); fi
if check_messages_stream sse_msg_resp.txt 7 3 delta "messages stream from responses upstream (translated)"; then pass=$((pass+1)); else fail=$((fail+1)); fi
# 直通流：客户端看到的是上游 message_start 的原始 usage。
if check_messages_stream sse_msg_claude.txt 90 40 start "messages stream from anthropic upstream (passthrough)"; then pass=$((pass+1)); else fail=$((fail+1)); fi
if check_responses_stream sse_resp_claude.txt 90 40 "responses stream from anthropic upstream (translated)"; then pass=$((pass+1)); else fail=$((fail+1)); fi
# chat→responses 流式：Responses 信封的 input_tokens 是总输入，cached 是子集。
if check_responses_stream sse_resp_chat.txt 7 3 "responses stream from chat upstream (translated)"; then pass=$((pass+1)); else fail=$((fail+1)); fi
# responses→chat 流式：chat 信封的 prompt_tokens 是总输入，details 由归一化值派生。
if check_chat_stream sse_chat_resp.txt 7 3 "chat stream from responses upstream (translated)"; then pass=$((pass+1)); else fail=$((fail+1)); fi

echo "RESULT pass=$pass fail=$fail"
[ "$fail" = 0 ]
