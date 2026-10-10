package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// parseSSE decodes a captured stream into event name -> decoded payloads.
func parseSSE(t *testing.T, raw string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	var name string
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(payload), &v); err != nil {
				t.Fatalf("bad payload %q: %s", payload, err)
			}
			out[name] = append(out[name], v)
		}
	}
	return out
}

// An Anthropic assistant turn carries text and tool_use in ONE message; splitting
// them into two OpenAI assistant turns inserts a turn the client never sent.
func TestParseMessages_MergesTextAndToolUse(t *testing.T) {
	body := `{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":[
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"call_a","name":"get_weather","input":{"city":"SF"}}
		]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"sunny"}]}
	]}`
	req, perr := ParseMessages([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	assistant := req.Messages[1]
	if assistant.Role != "assistant" || assistant.Content != "checking" {
		t.Fatalf("assistant turn wrong: %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls missing from the merged turn: %+v", assistant.ToolCalls)
	}
	// tool_result has no name of its own; it is recovered from the tool_use that
	// issued the call, or a strict upstream rejects the conversation.
	tool := req.Messages[2]
	if tool.Role != "tool" || tool.ToolCallId != "call_a" {
		t.Fatalf("tool message wrong: %+v", tool)
	}
	if tool.Name == nil || *tool.Name != "get_weather" {
		t.Fatalf("tool message name not backfilled: %#v", tool.Name)
	}
}

func TestParseMessages_DisableParallelToolUse(t *testing.T) {
	body := `{"model":"m","max_tokens":16,
		"tool_choice":{"type":"auto","disable_parallel_tool_use":true},
		"messages":[{"role":"user","content":"hi"}]}`
	req, perr := ParseMessages([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	if req.ParallelTooCalls == nil || *req.ParallelTooCalls {
		t.Fatalf("disable_parallel_tool_use dropped: %#v", req.ParallelTooCalls)
	}
}

// A turn that produced tool calls stopped for tool use, and `input` must be an
// object: leaking the malformed argument text under a synthetic key hands the
// client a field its schema never declared.
func TestRenderMessages_ToolCallDefaults(t *testing.T) {
	up := textResponse(
		relaymodel.Message{Role: "assistant", ToolCalls: []relaymodel.Tool{{
			Id: "call_a", Type: "function",
			Function: relaymodel.Function{Name: "f", Arguments: `{"city":`},
		}}},
		"", // finish_reason absent, as some OpenAI-compatible upstreams leave it
	)
	env := RenderMessages(up, "claude")
	blocks := env["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["type"] != "tool_use" {
		t.Fatalf("tool_use block missing: %+v", blocks)
	}
	if env["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", env["stop_reason"])
	}
	input := blocks[0]["input"].(map[string]any)
	if len(input) != 0 {
		t.Fatalf("malformed arguments must collapse to {}, got %#v", input)
	}
}

// A message with no tool calls and no finish_reason is still a completed turn.
func TestRenderMessages_EmptyFinishReasonDefaultsEndTurn(t *testing.T) {
	up := textResponse(relaymodel.Message{Role: "assistant", Content: "hi"}, "")
	env := RenderMessages(up, "claude")
	if env["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", env["stop_reason"])
	}
}

func textResponse(msg relaymodel.Message, finish string) *openai.TextResponse {
	return &openai.TextResponse{
		Id:      "chatcmpl-1",
		Choices: []openai.TextResponseChoice{{Message: msg, FinishReason: finish}},
	}
}

// Two parallel tool calls must arrive as two identified tool_use blocks, with the
// block announced only once its name is known and the arguments attributed by
// content-block index.
func TestMessagesStream_ParallelToolUseBlocks(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)

	for _, raw := range []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[
			{"index":0,"function":{"arguments":"{\"city\":"}},
			{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[
			{"index":0,"function":{"arguments":"\"SF\"}"}},
			{"index":1,"function":{"arguments":"{\"tz\":\"UTC\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`,
	} {
		if err := s.Chunk(mustParse(t, raw)); err != nil {
			t.Fatalf("chunk: %s", err)
		}
	}
	if err := s.Finish("tool_calls", &relaymodel.Usage{CompletionTokens: 9}); err != nil {
		t.Fatalf("finish: %s", err)
	}

	events := parseSSE(t, c.buf.String())
	starts := map[int]map[string]any{}
	for _, ev := range events["content_block_start"] {
		block := ev["content_block"].(map[string]any)
		if block["type"] != "tool_use" {
			continue
		}
		starts[int(ev["index"].(float64))] = block
	}
	if len(starts) != 2 {
		t.Fatalf("want 2 tool_use blocks, got %+v", starts)
	}
	if starts[0]["name"] != "get_weather" || starts[1]["name"] != "get_time" {
		t.Fatalf("block names wrong: %+v", starts)
	}
	if starts[0]["id"] != "call_a" || starts[1]["id"] != "call_b" {
		t.Fatalf("block ids wrong: %+v", starts)
	}
	args := map[int]string{}
	for _, ev := range events["content_block_delta"] {
		d := ev["delta"].(map[string]any)
		if d["type"] != "input_json_delta" {
			continue
		}
		idx := int(ev["index"].(float64))
		args[idx] += d["partial_json"].(string)
	}
	var got0, got1 map[string]any
	if err := json.Unmarshal([]byte(args[0]), &got0); err != nil || got0["city"] != "SF" {
		t.Fatalf("call 0 arguments = %q", args[0])
	}
	if err := json.Unmarshal([]byte(args[1]), &got1); err != nil || got1["tz"] != "UTC" {
		t.Fatalf("call 1 arguments = %q", args[1])
	}
	if len(events["content_block_stop"]) != 2 {
		t.Fatalf("want 2 content_block_stop, got %d", len(events["content_block_stop"]))
	}
	deltas := events["message_delta"]
	if len(deltas) != 1 {
		t.Fatalf("want 1 message_delta, got %d", len(deltas))
	}
	if reason := deltas[0]["delta"].(map[string]any)["stop_reason"]; reason != "tool_use" {
		t.Fatalf("stop_reason = %v", reason)
	}
}

// A tool delta whose name arrives after the arguments must not open the block
// early (a tool_use without a name is unusable) and must not lose the fragment.
func TestMessagesStream_DefersBlockUntilNamed(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}`))
	if strings.Contains(c.buf.String(), "content_block_start") {
		t.Fatalf("block opened before the name was known:\n%s", c.buf.String())
	}
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"f"}}]}}]}`))
	if !strings.Contains(c.buf.String(), `"name":"f"`) {
		t.Fatalf("block not opened once named:\n%s", c.buf.String())
	}
	// The buffered fragment is flushed exactly once, right after the start.
	if n := strings.Count(c.buf.String(), `"partial_json":"{\"a\":1}"`); n != 1 {
		t.Fatalf("buffered fragment emitted %d times:\n%s", n, c.buf.String())
	}
}

// The terminal response.completed must repeat the finished function calls: a
// Responses client reads them from response.output, not from the deltas.
func TestResponsesStream_CompletedCarriesFunctionCalls(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o", nil)
	for _, raw := range []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[
			{"index":0,"function":{"arguments":"{\"city\":\"SF\"}"}},
			{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"UTC\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`,
	} {
		if err := s.Chunk(mustParse(t, raw)); err != nil {
			t.Fatalf("chunk: %s", err)
		}
	}
	if err := s.Finish("tool_calls", &relaymodel.Usage{CompletionTokens: 9}); err != nil {
		t.Fatalf("finish: %s", err)
	}

	events := parseSSE(t, c.buf.String())
	added := events["response.output_item.added"]
	if len(added) != 2 {
		t.Fatalf("want 2 output_item.added, got %d", len(added))
	}
	for i, ev := range added {
		if int(ev["output_index"].(float64)) != i {
			t.Fatalf("output_index not dense: %+v", ev)
		}
		item := ev["item"].(map[string]any)
		if item["type"] != "function_call" || item["name"] == "" || item["call_id"] == "" {
			t.Fatalf("function_call item incomplete: %+v", item)
		}
	}
	// The argument delta identifies the call by item_id + output_index; the real
	// API sends no call_id there.
	for _, ev := range events["response.function_call_arguments.delta"] {
		if _, ok := ev["output_index"]; !ok {
			t.Fatalf("argument delta without output_index: %+v", ev)
		}
		if _, ok := ev["call_id"]; ok {
			t.Fatalf("argument delta must not carry call_id: %+v", ev)
		}
	}
	dones := events["response.function_call_arguments.done"]
	if len(dones) != 2 {
		t.Fatalf("want 2 arguments.done, got %d", len(dones))
	}
	completed := events["response.completed"]
	if len(completed) != 1 {
		t.Fatalf("want 1 response.completed, got %d", len(completed))
	}
	output := completed[0]["response"].(map[string]any)["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("response.completed carries %d output items, want 2", len(output))
	}
	first := output[0].(map[string]any)
	if first["name"] != "get_weather" || first["call_id"] != "call_a" || first["status"] != "completed" {
		t.Fatalf("completed item wrong: %+v", first)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(first["arguments"].(string)), &args); err != nil || args["city"] != "SF" {
		t.Fatalf("completed arguments = %v", first["arguments"])
	}
}

// A tool call that never streamed arguments must still close with a parseable
// object in the terminal output.
func TestResponsesStream_SilentToolCallGetsEmptyObject(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o", nil)
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f"}}]}}]}`))
	_ = s.Finish("tool_calls", nil)

	events := parseSSE(t, c.buf.String())
	output := events["response.completed"][0]["response"].(map[string]any)["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %+v", output)
	}
	if item := output[0].(map[string]any); item["arguments"] != "{}" {
		t.Fatalf("silent call arguments = %v, want {}", item["arguments"])
	}
}

// strict must survive: without it the upstream silently downgrades to a
// best-effort schema.
func TestParseResponses_StrictPassthrough(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"function","name":"f","parameters":{"type":"object"},"strict":true}]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Strict == nil || !*req.Tools[0].Function.Strict {
		t.Fatalf("strict dropped: %+v", req.Tools)
	}
}

// A Responses function_call_output names only the call_id; the tool message it
// becomes must still name the function for strict upstreams.
func TestParseResponses_ToolOutputNameBackfilled(t *testing.T) {
	body := `{"model":"gpt-4o","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"weather?"}]},
		{"type":"function_call","call_id":"call_a","name":"get_weather","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","output":"sunny"}]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	var tool *relaymodel.Message
	for i := range req.Messages {
		if req.Messages[i].Role == "tool" {
			tool = &req.Messages[i]
		}
	}
	if tool == nil {
		t.Fatalf("no tool message: %+v", req.Messages)
	}
	if tool.Name == nil || *tool.Name != "get_weather" {
		t.Fatalf("tool name not backfilled: %#v", tool.Name)
	}
}

// The client echoes the tool_use id back as the next turn's tool_use_id, so a
// block must not open until the upstream has actually issued the id: a block
// closed with a synthesized id would send the upstream a call id it never made.
func TestMessagesStream_WaitsForCallID(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)

	// Arguments, then the name — but still no id.
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f"}}]}}]}`))
	if strings.Contains(c.buf.String(), "content_block_start") {
		t.Fatalf("block opened before the call id was known:\n%s", c.buf.String())
	}

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function"}]}}]}`))
	if !strings.Contains(c.buf.String(), `"id":"call_a"`) {
		t.Fatalf("block not opened with the real call id:\n%s", c.buf.String())
	}
	if strings.Contains(c.buf.String(), "toolu_") {
		t.Fatalf("synthesized an id the upstream never issued:\n%s", c.buf.String())
	}
	// The fragment buffered across both early chunks is flushed once, in order.
	if n := strings.Count(c.buf.String(), `"partial_json":"{\"a\":"`); n != 1 {
		t.Fatalf("buffered fragment emitted %d times:\n%s", n, c.buf.String())
	}

	_ = s.Finish("tool_calls", nil)
	if n := strings.Count(c.buf.String(), `"partial_json":"{\"a\":"`); n != 1 {
		t.Fatalf("Finish re-emitted the buffered fragment:\n%s", c.buf.String())
	}
}

// A tool delta that never names the call produces no tool_use block, so the turn
// did not stop for tool use; reporting tool_use would leave the client waiting
// for a tool_result that can never come.
func TestMessagesStream_NamelessCallIsNotToolUse(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`))
	_ = s.Finish("stop", nil)

	if strings.Contains(c.buf.String(), "tool_use") {
		t.Fatalf("nameless call produced a tool_use block:\n%s", c.buf.String())
	}
	deltas := parseSSE(t, c.buf.String())["message_delta"]
	if len(deltas) != 1 || deltas[0]["delta"].(map[string]any)["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %+v, want end_turn", deltas)
	}
}

// A Responses call is announced once named, but the upstream may only identify
// it later; the terminal output must still carry the real call_id.
func TestResponsesStream_CallIDArrivesLate(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o", nil)
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"SF\"}"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function"}]}}]}`))
	_ = s.Finish("tool_calls", nil)

	output := parseSSE(t, c.buf.String())["response.completed"][0]["response"].(map[string]any)["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %+v", output)
	}
	item := output[0].(map[string]any)
	if item["call_id"] != "call_a" {
		t.Fatalf("call_id = %v, want the id that arrived late", item["call_id"])
	}
	if item["name"] != "get_weather" || item["status"] != "completed" {
		t.Fatalf("item wrong: %+v", item)
	}
}

// An argument fragment arriving before the name must not announce the item: a
// function_call without a name is not dispatchable by the client.
func TestResponsesStream_DoesNotAnnounceBeforeName(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o", nil)
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"arguments":"{\"c\":"}}]}}]}`))
	if strings.Contains(c.buf.String(), "response.output_item.added") {
		t.Fatalf("nameless function_call was announced:\n%s", c.buf.String())
	}
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather"}}]}}]}`))
	if !strings.Contains(c.buf.String(), `"name":"get_weather"`) {
		t.Fatalf("item not announced once named:\n%s", c.buf.String())
	}
}

// One tool index carrying two different calls cannot be rendered as one item;
// silently keeping the latest would send the client a call the upstream never
// completed.
func TestStream_RejectsIndexReuse(t *testing.T) {
	c := &capture{}
	ms := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)
	_ = ms.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"f"}}]}}]}`))
	if err := ms.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_b"}]}}]}`)); err == nil {
		t.Fatal("messages: mid-stream id change was accepted")
	}

	c2 := &capture{}
	rs := NewResponsesStream(sse.NewWriter(c2), "resp_1", "gpt-4o", nil)
	_ = rs.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"f"}}]}}]}`))
	if err := rs.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"g"}}]}}]}`)); err == nil {
		t.Fatal("responses: mid-stream name change was accepted")
	}
}
