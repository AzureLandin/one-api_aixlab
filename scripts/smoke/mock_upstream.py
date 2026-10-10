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

# Every /responses request is parked here (raw + parsed), so the smoke script can
# assert that a client request the gateway translated into the Responses API
# really arrived as one: `input` items and `instructions`, not `messages`, and no
# `stream_options`, which the Responses protocol does not have.
LAST_RESPONSES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mock_last_responses.json")

# The input side and the cache read, reported once by message_start. The terminal
# delta reports output_tokens: 12 as the CUMULATIVE count, so a gateway that adds
# the events together records 13.
MESSAGES_USAGE = {"input_tokens": 50, "output_tokens": 12, "cache_read_input_tokens": 40}

# A hosted tool — one the UPSTREAM executes itself, like web search — is a
# different thing from a function tool the client executes, even though both
# travel in `tools`. The declaration is what makes the tool available; the token
# below is what makes the fixture's upstream actually USE it. Keeping the two
# apart is what lets the cells that declare a tool without running one stay on the
# plain envelope — the passthrough cells assert byte fidelity and must not
# suddenly grow a search block.
HOSTED_MARKER = b"use-hosted-search"


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


def messages_response(model, hosted=False):
    """An Anthropic non-streaming body. The thinking block carries a `signature`
    field that the gateway's own Content struct has no slot for: if the relay
    re-rendered this envelope instead of forwarding it, the field would vanish.

    With `hosted`, the answer is one where the upstream ran its own web search:
    the call block and the result block, sharing an id, ahead of the text — the
    two-block spelling the gateway merges back into one artifact. The result is
    what Anthropic's encrypted search results look like: carried opaquely, and
    impossible for a gateway to reconstruct, which is why the reverse direction
    records a loss instead of inventing one."""
    content = []
    if hosted:
        content.append({"type": "server_tool_use", "id": "srvtoolu_mock",
                        "name": "web_search", "input": {"query": "smoke"}})
        content.append({"type": "web_search_tool_result", "tool_use_id": "srvtoolu_mock",
                        "content": [{"type": "web_search_result", "url": "https://mock.example",
                                     "title": "Mock", "encrypted_content": "enc_mock"}]})
    content.append({"type": "thinking", "thinking": "weighing it", "signature": "sig_mock"})
    content.append({"type": "text", "text": "ok"})
    return {
        "id": "msg_mock", "type": "message", "role": "assistant",
        "model": model or "claude-mock",
        "content": content,
        "stop_reason": "end_turn", "stop_sequence": None,
        "usage": dict(MESSAGES_USAGE),
    }


def messages_stream(hosted=False):
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
    if hosted:
        yield ev("content_block_start", {
            "type": "content_block_start", "index": 0,
            "content_block": {"type": "server_tool_use", "id": "srvtoolu_mock",
                              "name": "web_search", "input": {}}})
        yield ev("content_block_delta", {
            "type": "content_block_delta", "index": 0,
            "delta": {"type": "input_json_delta", "partial_json": '{"query": "smoke"}'}})
        yield ev("content_block_stop", {"type": "content_block_stop", "index": 0})
        yield ev("content_block_start", {
            "type": "content_block_start", "index": 1,
            "content_block": {
                "type": "web_search_tool_result", "tool_use_id": "srvtoolu_mock",
                "content": [{"type": "web_search_result", "url": "https://mock.example",
                             "title": "Mock", "encrypted_content": "enc_mock"}]}})
        yield ev("content_block_stop", {"type": "content_block_stop", "index": 1})
        yield ev("content_block_start", {
            "type": "content_block_start", "index": 2,
            "content_block": {"type": "text", "text": ""}})
        yield ev("content_block_delta", {
            "type": "content_block_delta", "index": 2,
            "delta": {"type": "text_delta", "text": "o"}})
        yield ev("content_block_delta", {
            "type": "content_block_delta", "index": 2,
            "delta": {"type": "text_delta", "text": "k"}})
        yield ev("content_block_stop", {"type": "content_block_stop", "index": 2})
        yield ev("message_delta", {
            "type": "message_delta",
            "delta": {"stop_reason": "end_turn", "stop_sequence": None},
            "usage": {"output_tokens": 12}})
        yield ev("message_stop", {"type": "message_stop"})
        return
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

# Responses usage, in the protocol's own names. input_tokens_details is added only
# when the prompt carries the cache marker, exactly as the chat fixture does with
# prompt_tokens_details: a Responses upstream that reports nothing about the cache
# must leave the cache read absent (not_reported), not zero.
RESPONSES_USAGE = {"input_tokens": 7, "output_tokens": 5, "total_tokens": 12}
RESPONSES_TOOL_USAGE = {"input_tokens": 11, "output_tokens": 9, "total_tokens": 20}

# The two parallel calls as Responses output items. Unlike a chat message, each
# call is its own item with call_id/name/arguments flat on it.
RESPONSES_CALL_ITEMS = [
    {"id": "fc_mock_a", "type": "function_call", "call_id": "call_mock_a",
     "name": "get_weather", "arguments": json.dumps({"city": "SF"}), "status": "completed"},
    {"id": "fc_mock_b", "type": "function_call", "call_id": "call_mock_b",
     "name": "get_time", "arguments": json.dumps({"tz": "UTC"}), "status": "completed"},
]


def responses_usage(raw, tools):
    usage = dict(RESPONSES_TOOL_USAGE if tools else RESPONSES_USAGE)
    if b"usecache" in raw:
        usage["input_tokens_details"] = {"cached_tokens": 3}
    return usage


# One executed web search, as the Responses protocol reports it: an item of its
# own with the action it took. There is no results field — this protocol hands the
# results to the model inline — which is why the reverse direction can only report
# what the search was, never what it found.
RESPONSES_SEARCH_ITEM = {"id": "ws_mock", "type": "web_search_call", "status": "completed",
                         "action": {"type": "search", "query": "smoke"}}

# The answer that follows the search. Named because both the plain body and the
# hosted one end with it, and the hosted cells assert that the loss of the search
# item did not take the answer with it.
RESPONSES_MESSAGE_ITEM = {"id": "msg_mock", "type": "message", "role": "assistant",
                          "status": "completed",
                          "content": [{"type": "output_text", "text": "ok", "annotations": []}]}


def responses_response(model, tools, raw, hosted=False):
    """A Responses non-streaming body. A pure tool turn is function_call items
    with no message item — the shape a converter that only reads message content
    would answer with nothing at all.

    With `hosted`, the upstream ran its own search, so the search item leads and
    the answer follows it — the same order the streaming branch produces, and the
    order a real response has. A search item alone would make "the answer still
    arrived beside the loss" untestable."""
    if hosted:
        output = [dict(RESPONSES_SEARCH_ITEM), RESPONSES_MESSAGE_ITEM]
    elif tools:
        output = list(RESPONSES_CALL_ITEMS)
    else:
        output = [dict(RESPONSES_MESSAGE_ITEM)]
    return {
        "id": "resp_mock", "object": "response", "status": "completed",
        "created_at": 1700000000, "model": model or "gpt-mock",
        "output": output,
        "usage": responses_usage(raw, tools),
    }


def responses_stream(raw, tools, hosted=False):
    """A real Responses SSE sequence. There is no [DONE] marker: the stream ends
    with response.completed, which is also where the usage and the full output
    arrive. The two calls hold output_index 0 and 1, so a client chunk stream that
    reused those numbers would announce OpenAI tool indices 0 and 1 only by
    accident of this fixture — the message case is what pins the mapping."""
    def ev(kind, obj):
        return sse_event(obj, event=kind)

    yield ev("response.created", {"type": "response.created", "response": {
        "id": "resp_mock", "object": "response", "status": "in_progress",
        "created_at": 1700000000, "model": "gpt-mock"}})

    if hosted:
        # A hosted artifact arrives as an ordinary output item: announced, then
        # closed. Nothing about it is a text chunk, which is precisely why a
        # client protocol with no hosted vocabulary has to be told about the loss.
        yield ev("response.output_item.added", {
            "type": "response.output_item.added", "output_index": 0,
            "item": {"id": "ws_mock", "type": "web_search_call", "status": "in_progress",
                     "action": {"type": "search", "query": "smoke"}}})
        yield ev("response.output_item.done", {
            "type": "response.output_item.done", "output_index": 0,
            "item": dict(RESPONSES_SEARCH_ITEM)})
        yield ev("response.output_item.added", {
            "type": "response.output_item.added", "output_index": 1,
            "item": {"id": "msg_mock", "type": "message", "role": "assistant", "content": []}})
        yield ev("response.output_text.delta", {
            "type": "response.output_text.delta", "item_id": "msg_mock",
            "output_index": 1, "content_index": 0, "delta": "ok"})
        yield ev("response.completed", {"type": "response.completed", "response": {
            "id": "resp_mock", "status": "completed",
            "output": [dict(RESPONSES_SEARCH_ITEM), dict(RESPONSES_MESSAGE_ITEM)],
            "usage": responses_usage(raw, tools)}})
        return

    if tools:
        yield ev("response.output_item.added", {
            "type": "response.output_item.added", "output_index": 0,
            "item": {"id": "fc_mock_a", "type": "function_call", "call_id": "call_mock_a",
                     "name": "get_weather", "arguments": ""}})
        yield ev("response.output_item.added", {
            "type": "response.output_item.added", "output_index": 1,
            "item": {"id": "fc_mock_b", "type": "function_call", "call_id": "call_mock_b",
                     "name": "get_time", "arguments": ""}})
        # The second call's arguments arrive first, so the fragments have to be
        # routed by item rather than by arrival order.
        yield ev("response.function_call_arguments.delta", {
            "type": "response.function_call_arguments.delta", "item_id": "fc_mock_b",
            "output_index": 1, "delta": '{"tz":'})
        yield ev("response.function_call_arguments.delta", {
            "type": "response.function_call_arguments.delta", "item_id": "fc_mock_a",
            "output_index": 0, "delta": '{"city":'})
        yield ev("response.function_call_arguments.delta", {
            "type": "response.function_call_arguments.delta", "item_id": "fc_mock_b",
            "output_index": 1, "delta": '"UTC"}'})
        yield ev("response.function_call_arguments.delta", {
            "type": "response.function_call_arguments.delta", "item_id": "fc_mock_a",
            "output_index": 0, "delta": '"SF"}'})
        yield ev("response.completed", {"type": "response.completed", "response": {
            "id": "resp_mock", "status": "completed", "output": list(RESPONSES_CALL_ITEMS),
            "usage": responses_usage(raw, tools)}})
        return

    yield ev("response.output_item.added", {
        "type": "response.output_item.added", "output_index": 0,
        "item": {"id": "msg_mock", "type": "message", "role": "assistant", "content": []}})
    yield ev("response.content_part.added", {
        "type": "response.content_part.added", "item_id": "msg_mock",
        "output_index": 0, "content_index": 0,
        "part": {"type": "output_text", "text": "", "annotations": []}})
    yield ev("response.output_text.delta", {
        "type": "response.output_text.delta", "item_id": "msg_mock",
        "output_index": 0, "content_index": 0, "delta": "o"})
    yield ev("response.output_text.delta", {
        "type": "response.output_text.delta", "item_id": "msg_mock",
        "output_index": 0, "content_index": 0, "delta": "k"})
    yield ev("response.completed", {"type": "response.completed", "response": {
        "id": "resp_mock", "status": "completed",
        "output": [{"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed",
                    "content": [{"type": "output_text", "text": "ok", "annotations": []}]}],
        "usage": responses_usage(raw, tools)}})


def wants_tools(raw):
    """The gateway only forwards `tools` when the client declared them, so their
    presence is the switch between the text fixture and the function-call fixture.

    A hosted declaration travels in the same array — a web search the upstream
    executes itself is a tool entry too — so the check is for a FUNCTION entry.
    Selecting the function-call fixture for a hosted declaration would answer a
    question nobody asked, and would hide the very difference these cells exist to
    pin down."""
    try:
        tools = json.loads(raw).get("tools")
    except Exception:
        return False
    for tool in tools or []:
        if not isinstance(tool, dict):
            continue
        # A chat/Responses function carries type "function"; a Claude one carries
        # no type at all (it is identified by name + input_schema).
        if tool.get("type") in (None, "function", "custom"):
            return True
    return False


def wants_hosted(raw):
    """True when the request declared a hosted tool — one the UPSTREAM runs.

    Chat Completions spells it as the `web_search_options` toggle; the other two
    protocols put a typed entry in `tools`. Only web search and MCP are in the
    gateway's cross-protocol table, so only those are recognised here: a
    file_search or computer_use declaration cannot be asked of another target at
    all, which is a different outcome with its own cell."""
    try:
        body = json.loads(raw)
    except Exception:
        return False
    if body.get("web_search_options"):
        return True
    for tool in body.get("tools") or []:
        if not isinstance(tool, dict):
            continue
        if (tool.get("type") or "").startswith("web_search") or tool.get("type") == "mcp":
            return True
    return False


def runs_hosted(raw):
    """True when the request asks the upstream to actually run its hosted tool.

    Declaring the tool only makes it available; the upstream searches when the
    turn calls for it. A cell that declares a tool without using it therefore gets
    the plain envelope — which is what keeps the passthrough cells byte-identical
    to what they asserted before hosted tools existed."""
    return wants_hosted(raw) and HOSTED_MARKER in raw


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
        hosted = runs_hosted(raw)

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
                for chunk in messages_stream(hosted=hosted):
                    self.wfile.write(chunk)
                    self.wfile.flush()
                return
            body = json.dumps(messages_response((record["body"] or {}).get("model"), hosted=hosted)).encode()
        elif self.path.endswith("/responses"):
            record = {"raw": raw.decode("utf-8", "replace")}
            try:
                record["body"] = json.loads(raw)
            except Exception:
                record["body"] = None
            try:
                with open(LAST_RESPONSES, "w") as fh:
                    json.dump(record, fh)
            except OSError:
                pass

            if wants_stream:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                for chunk in responses_stream(raw, tools, hosted=hosted):
                    self.wfile.write(chunk)
                    self.wfile.flush()
                return
            body = json.dumps(responses_response((record["body"] or {}).get("model"), tools, raw, hosted=hosted)).encode()
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
