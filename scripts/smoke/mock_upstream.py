import json
from http.server import BaseHTTPRequestHandler, HTTPServer


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
    """Mimic an OpenAI streaming chat.completion: 2 text deltas, a tool call,
    a terminal usage chunk (stream_options.include_usage), then [DONE]."""
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


class MockHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b"{}"
        wants_stream = False
        try:
            wants_stream = bool(json.loads(raw).get("stream"))
        except Exception:
            wants_stream = False

        if self.path.endswith("/chat/completions"):
            if wants_stream:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                for chunk in stream_chunks():
                    self.wfile.write(chunk)
                    self.wfile.flush()
                return
            body = json.dumps({
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "created": 1700000000,
                "model": "gpt-3.5-turbo",
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": "ok"},
                    "finish_reason": "stop",
                }],
                "usage": {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
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
