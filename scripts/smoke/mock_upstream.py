import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

# Every /chat/completions request body is parked here so the smoke script can
# assert on the wire shape the gateway produced from a /v1/responses or
# /v1/messages request (nested text/role shapes the client never sends).
LAST_REQUEST = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mock_last_request.json")


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
