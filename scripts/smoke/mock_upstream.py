import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

# Every /chat/completions request body is parked here so the smoke script can
# assert on the wire shape the gateway produced from a /v1/responses or
# /v1/messages request (nested text/role shapes the client never sends).
LAST_REQUEST = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mock_last_request.json")

# Every /messages request is parked here with its body and the beta/version
# headers, so the smoke script can prove that same-protocol passthrough carried
# the client's own fields (cache_control, thinking, hosted tool declarations)
# through untouched, and that the gateway's own anthropic-beta flag was merged
# into the client's rather than replacing it.
LAST_MESSAGES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mock_last_messages.json")

# The input side and the cache read, reported once by message_start. The terminal
# delta reports output_tokens: 12 as the CUMULATIVE count, so a gateway that adds
# the events together records 13.
MESSAGES_USAGE = {"input_tokens": 50, "output_tokens": 12, "cache_read_input_tokens": 40}


def sse_event(obj, event=None):
    """Frame one OpenAI chat.completion.chunk (event line only when present)."""
    lines = []
    if event is not None:
        lines.append("event: %s" % event)
    lines.append("data: %s" % json.dumps(obj))
    lines.append("")
    lines.append("")
    return "\n".join(lines).encode()


def stream_chunks():
    """Mimic an OpenAI streaming chat.completion: 2 text deltas, a terminal usage
    chunk (stream_options.include_usage), then [DONE]."""
    yield sse_event({
        "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1700000000,
        "model": "gpt-3.5-turbo",
        "choices": [{"index": 0, "delta": {"role": "assistant", "content": "o"}}],
    })
    yield sse_event({
        "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1700000000,
        "model": "gpt-3.5-turbo",
        "choices": [{"index": 0, "delta": {"content": "k"}, "finish_reason": "stop"}],
    })
    # usage tail chunk (no choices), as produced with stream_options.include_usage
    yield sse_event({
        "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1700000000,
        "model": "gpt-3.5-turbo", "choices": [],
        "usage": {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
    })
    yield b"data: [DONE]\n\n"


def stream_tool_chunks():
    """Two PARALLEL tool calls streamed with interleaved argument fragments — the
    shape that breaks a converter which cannot tell the calls apart by a dense
    per-message index."""
    def chunk(delta, finish=None):
        choice = {"index": 0, "delta": delta}
        if finish is not None:
            choice["finish_reason"] = finish
        return sse_event({
            "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1700000000,
            "model": "gpt-3.5-turbo", "choices": [choice],
        })

    # role + first call announced with its id/name, no other tool call yet
    yield chunk({"role": "assistant", "tool_calls": [
        {"index": 0, "id": "call_mock_a", "type": "function",
         "function": {"name": "get_weather", "arguments": ""}},
    ]})
    # second call opens while the first is still streaming its arguments
    yield chunk({"tool_calls": [
        {"index": 0, "function": {"arguments": "{\"city\":"}},
        {"index": 1, "id": "call_mock_b", "type": "function",
         "function": {"name": "get_time", "arguments": ""}},
    ]})
    yield chunk({"tool_calls": [
        {"index": 0, "function": {"arguments": "\"SF\"}"}},
        {"index": 1, "function": {"arguments": "{\"tz\":\"UTC\"}"}},
    ]})
    yield chunk({"tool_calls": []}, finish="tool_calls")
    yield sse_event({
        "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1700000000,
        "model": "gpt-3.5-turbo", "choices": [],
        "usage": {"prompt_tokens": 11, "completion_tokens": 9, "total_tokens": 20},
    })
    yield b"data: [DONE]\n\n"


def messages_response(model):
    """An Anthropic non-streaming body. The thinking block carries a `signature`
    field that the gateway's own Content struct has no slot for: if the relay
    re-rendered this envelope instead of forwarding it, the field would vanish."""
    return {
        "id": "msg_mock", "type": "message", "role": "assistant",
        "model": model or "claude-mock",
        "content": [
            {"type": "thinking", "thinking": "weighing it", "signature": "sig_mock"},
            {"type": "text", "text": "ok"},
        ],
        "stop_reason": "end_turn", "stop_sequence": None,
        "usage": dict(MESSAGES_USAGE),
    }


def messages_stream():
    """A real Anthropic SSE sequence: message_start (input side + cache read,
    running output count), a thinking block, a text block, then the terminal
    message_delta carrying the cumulative output count, then message_stop."""
    def ev(event, obj):
        return sse_event(obj, event=event)

    yield ev("message_start", {
        "type": "message_start", "message": {
            "id": "msg_mock", "type": "message", "role": "assistant",
            "model": "claude-mock", "content": [], "stop_reason": None,
            "stop_sequence": None,
            "usage": {"input_tokens": 50, "output_tokens": 1,
                      "cache_read_input_tokens": 40},
        }})
    yield ev("content_block_start", {
        "type": "content_block_start", "index": 0,
        "content_block": {"type": "thinking", "thinking": ""}})
    yield ev("content_block_delta", {
        "type": "content_block_delta", "index": 0,
        "delta": {"type": "thinking_delta", "thinking": "weighing it"}})
    yield ev("content_block_stop", {"type": "content_block_stop", "index": 0})
    yield ev("content_block_start", {
        "type": "content_block_start", "index": 1,
        "content_block": {"type": "text", "text": ""}})
    yield ev("content_block_delta", {
        "type": "content_block_delta", "index": 1,
        "delta": {"type": "text_delta", "text": "o"}})
    yield ev("content_block_delta", {
        "type": "content_block_delta", "index": 1,
        "delta": {"type": "text_delta", "text": "k"}})
    yield ev("content_block_stop", {"type": "content_block_stop", "index": 1})
    yield ev("message_delta", {
        "type": "message_delta",
        "delta": {"stop_reason": "end_turn", "stop_sequence": None},
        "usage": {"output_tokens": 12}})
    yield ev("message_stop", {"type": "message_stop"})


TOOL_CALLS = [
    {"index": 0, "id": "call_mock_a", "type": "function",
     "function": {"name": "get_weather", "arguments": json.dumps({"city": "SF"})}},
    {"index": 1, "id": "call_mock_b", "type": "function",
     "function": {"name": "get_time", "arguments": json.dumps({"tz": "UTC"})}},
]


def wants_tools(raw):
    """The gateway only forwards `tools` when the client declared them, so their
    presence is the switch between the text fixture and the tool-call fixture."""
    try:
        return bool(json.loads(raw).get("tools"))
    except Exception:
        return False


class MockHandler(BaseHTTPRequestHandler):
    def _read_body(self):
        """Read the request body whether framed by Content-Length or chunked.
        Go's http client uses chunked transfer-encoding when the outgoing body
        reader has no known length; BaseHTTPRequestHandler does not auto-decode
        it, so we must, else we'd drop the payload and desync the connection."""
        te = (self.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            parts = []
            while True:
                line = self.rfile.readline().strip()
                if not line:
                    continue
                try:
                    n = int(line.split(b";")[0], 16)
                except ValueError:
                    break
                if n == 0:
                    self.rfile.readline()  # trailing CRLF
                    break
                parts.append(self.rfile.read(n))
                self.rfile.readline()  # CRLF after chunk
            return b"".join(parts)
        length = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(length) if length else b""

    def do_POST(self):
        raw = self._read_body()
        wants_stream = False
        try:
            wants_stream = bool(json.loads(raw).get("stream"))
        except Exception:
            wants_stream = False
        tools = wants_tools(raw)

        if self.path.endswith("/chat/completions"):
            try:
                with open(LAST_REQUEST, "wb") as fh:
                    fh.write(raw)
            except OSError:
                pass
            if wants_stream:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                for chunk in (stream_tool_chunks() if tools else stream_chunks()):
                    self.wfile.write(chunk)
                    self.wfile.flush()
                return
            usage = {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12}
            message = {"role": "assistant", "content": "ok"}
            finish_reason = "stop"
            if tools:
                usage = {"prompt_tokens": 11, "completion_tokens": 9, "total_tokens": 20}
                # content null + tool_calls is how OpenAI reports a pure tool turn;
                # a converter that only reads content drops the calls entirely.
                message = {"role": "assistant", "content": None, "tool_calls": TOOL_CALLS}
                finish_reason = "tool_calls"
            # §8.1 end-to-end: when the prompt carries the marker, report cached
            # tokens via the OpenAI detail field (maps to cache_read_input_tokens).
            if b"usecache" in raw:
                usage["prompt_tokens_details"] = {"cached_tokens": 3}
            body = json.dumps({
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "created": 1700000000,
                "model": "gpt-3.5-turbo",
                "choices": [{
                    "index": 0,
                    "message": message,
                    "finish_reason": finish_reason,
                }],
                "usage": usage,
            }).encode()
        elif self.path.endswith("/messages"):
            record = {
                "raw": raw.decode("utf-8", "replace"),
                "anthropic-beta": self.headers.get("anthropic-beta", ""),
                "anthropic-version": self.headers.get("anthropic-version", ""),
            }
            try:
                record["body"] = json.loads(raw)
            except Exception:
                record["body"] = None
            try:
                with open(LAST_MESSAGES, "w") as fh:
                    json.dump(record, fh)
            except OSError:
                pass

            if wants_stream:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                for chunk in messages_stream():
                    self.wfile.write(chunk)
                    self.wfile.flush()
                return
            body = json.dumps(messages_response((record["body"] or {}).get("model"))).encode()
        else:
            body = b'{"error":"not found"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        body = b'{"object":"list","data":[]}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 19999), MockHandler).serve_forever()
