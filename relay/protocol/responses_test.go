package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// messageText flattens a parsed message's content whether it came back as a
// plain string or as normalized content parts.
func messageText(t *testing.T, m relaymodel.Message) string {
	t.Helper()
	if s, ok := m.Content.(string); ok {
		return s
	}
	if parts, ok := m.Content.([]relaymodel.MessageContent); ok {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	t.Fatalf("unexpected content type %T", m.Content)
	return ""
}

func TestParseResponses_BareStringInput(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hello"}`)
	req, perr := ParseResponses(body)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("expected single user message, got %+v", req.Messages)
	}
	if req.Messages[0].Content != "hello" {
		t.Fatalf("content = %v", req.Messages[0].Content)
	}
}

func TestParseResponses_InstructionsAndContentParts(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o",
		"instructions":"be nice",
		"input":[{"role":"user","content":[
			{"type":"input_text","text":"hi"},
			{"type":"input_image","image_url":"http://x/y.png"}
		]}]
	}`)
	req, perr := ParseResponses(body)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "be nice" {
		t.Fatalf("system message wrong: %+v", req.Messages[0])
	}
	usr := req.Messages[1]
	if usr.Role != "user" {
		t.Fatalf("role = %s", usr.Role)
	}
	parts, ok := usr.Content.([]relaymodel.MessageContent)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content not normalized parts: %+v", usr.Content)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.Url != "http://x/y.png" {
		t.Fatalf("image part wrong: %+v", parts[1])
	}
}

func TestParseResponses_ToolCallAndOutputItems(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":[
		{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SF\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"sunny"}
	]}`)
	req, perr := ParseResponses(body)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	asst := req.Messages[0]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant tool_call wrong: %+v", asst)
	}
	if asst.ToolCalls[0].Function.Name != "get_weather" ||
		asst.ToolCalls[0].Id != "call_1" {
		t.Fatalf("tool call fields wrong: %+v", asst.ToolCalls[0])
	}
	tool := req.Messages[1]
	if tool.Role != "tool" || tool.ToolCallId != "call_1" || tool.Content != "sunny" {
		t.Fatalf("tool result wrong: %+v", tool)
	}
}

func TestParseResponses_RejectsUnsupportedFeatures(t *testing.T) {
	cases := map[string]string{
		"previous_response_id": `{"model":"gpt-4o","input":"hi","previous_response_id":"resp_x"}`,
		"background":           `{"model":"gpt-4o","input":"hi","background":true}`,
		"unknown_item_type":    `{"model":"gpt-4o","input":[{"type":"mystery_item"}]}`,
		"reasoning_only_input": `{"model":"gpt-4o","input":[{"type":"reasoning"}]}`,
		"missing_model":        `{"input":"hi"}`,
		"missing_input":        `{"model":"gpt-4o"}`,
	}
	for name, body := range cases {
		_, perr := ParseResponses([]byte(body))
		if perr == nil {
			t.Fatalf("%s: expected rejection", name)
		}
		if perr.Param == "" {
			t.Fatalf("%s: rejection must name a param", name)
		}
	}
}

// store=true cannot be honored (the gateway keeps no server-side state), but
// many clients set it by default, so it is ignored rather than refused.
func TestParseResponses_StoreTrueIsIgnored(t *testing.T) {
	req, perr := ParseResponses([]byte(`{"model":"gpt-4o","input":"hi","store":true}`))
	if perr != nil {
		t.Fatalf("store=true must be tolerated, got %+v", perr)
	}
	if len(req.Messages) != 1 || messageText(t, req.Messages[0]) != "hi" {
		t.Fatalf("messages must still parse, got %+v", req.Messages)
	}
}

// Every hosted/built-in tool type must be tolerated: clients such as Codex
// enable web_search by default, and refusing made all their requests fail.
func TestParseResponses_HostedToolsAreDroppedNotRejected(t *testing.T) {
	for _, toolType := range []string{
		"web_search", "web_search_preview", "file_search", "computer_use_preview",
		"code_interpreter", "image_generation", "local_shell", "mcp",
		"web_search_2025_08_26",
	} {
		t.Run(toolType, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":"hi","tools":[{"type":"` + toolType + `"}]}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("tool type %q must be tolerated, got %+v", toolType, perr)
			}
			if len(req.Tools) != 0 {
				t.Fatalf("hosted tool must not be forwarded, got %+v", req.Tools)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("messages must survive, got %+v", req.Messages)
			}
		})
	}
}

// A function tool alongside hosted ones must still be forwarded.
func TestParseResponses_KeepsFunctionToolsBesideHostedOnes(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"web_search"},
		{"type":"function","name":"lookup","description":"d","parameters":{"type":"object"}}
	]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "lookup" {
		t.Fatalf("function tool must be kept, got %+v", req.Tools)
	}
}

// The full set of non-message item types must be skipped, not rejected.
func TestParseResponses_SkipsAllHostedItemTypes(t *testing.T) {
	itemTypes := []string{
		"reasoning", "item_reference", "web_search_call", "file_search_call",
		"computer_call", "computer_call_output", "image_generation_call",
		"code_interpreter_call", "local_shell_call", "local_shell_call_output",
		"mcp_call", "mcp_list_tools", "mcp_approval_request", "mcp_approval_response",
	}
	for _, itemType := range itemTypes {
		t.Run(itemType, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":[
				{"type":"message","role":"user","content":"hi"},
				{"type":"` + itemType + `","id":"x_1","call_id":"c_1"}
			]}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("item type %q must be skipped, got %+v", itemType, perr)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("only the real message may remain, got %+v", req.Messages)
			}
		})
	}
}

// encrypted_content is reasoning metadata with no chat equivalent, so it is
// skipped instead of failing the request.
func TestParseResponses_SkipsEncryptedContentPart(t *testing.T) {
	body := `{"model":"gpt-4o","input":[{"role":"assistant","content":[
		{"type":"output_text","text":"answer"},
		{"type":"encrypted_content","encrypted_content":"gAAAA"}
	]}]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("encrypted_content must be skipped, got %+v", perr)
	}
	if len(req.Messages) != 1 || messageText(t, req.Messages[0]) != "answer" {
		t.Fatalf("text must survive, got %+v", req.Messages)
	}
}

// custom_tool_call carries its payload in "input"; it maps onto a chat tool call
// rather than being dropped.
func TestParseResponses_MapsCustomToolCallItems(t *testing.T) {
	body := `{"model":"gpt-4o","input":[
		{"type":"custom_tool_call","call_id":"c_9","name":"my_tool","input":"raw payload"},
		{"type":"custom_tool_call_output","call_id":"c_9","output":"tool result"}
	]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("custom tool items must be mapped, got %+v", perr)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("want assistant + tool messages, got %+v", req.Messages)
	}
	call := req.Messages[0]
	if call.Role != "assistant" || len(call.ToolCalls) != 1 {
		t.Fatalf("custom_tool_call must become an assistant tool_call, got %+v", call)
	}
	if call.ToolCalls[0].Function.Name != "my_tool" || call.ToolCalls[0].Function.Arguments != "raw payload" {
		t.Fatalf("payload must be preserved, got %+v", call.ToolCalls[0])
	}
	if req.Messages[1].Role != "tool" || messageText(t, req.Messages[1]) != "tool result" {
		t.Fatalf("custom_tool_call_output must become a tool message, got %+v", req.Messages[1])
	}
}

func TestParseResponses_StoreFalseIsAllowed(t *testing.T) {
	_, perr := ParseResponses([]byte(`{"model":"gpt-4o","input":"hi","store":false}`))
	if perr != nil {
		t.Fatalf("store=false must be accepted, got %+v", perr)
	}
}

// Regression: a reasoning model replayed the model's own reasoning items on the
// following turn, and rejecting them made every multi-turn reasoning
// conversation fail with "第一版不支持的 input item 类型：reasoning".
func TestParseResponses_SkipsReasoningItemsAlongsideMessages(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"reasoning","id":"rs_2","encrypted_content":"gAAAA..."},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}
		]
	}`)
	req, perr := ParseResponses(body)
	if perr != nil {
		t.Fatalf("reasoning items must not be rejected: %+v", perr)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want the 3 real messages and no reasoning entries, got %d: %+v",
			len(req.Messages), req.Messages)
	}
	wantRoles := []string{"user", "assistant", "user"}
	for i, want := range wantRoles {
		if req.Messages[i].Role != want {
			t.Fatalf("message %d role want %q, got %q", i, want, req.Messages[i].Role)
		}
	}
	if messageText(t, req.Messages[0]) != "first" || messageText(t, req.Messages[2]) != "second" {
		t.Fatalf("message text must survive beside reasoning items: %+v", req.Messages)
	}
	// A reasoning item must never leak into the forwarded conversation.
	if messageText(t, req.Messages[1]) != "answer" {
		t.Fatalf("assistant turn corrupted: %+v", req.Messages[1])
	}
}

// A reasoning item appearing first (common when a client replays a stored
// response) must not break parsing either.
func TestParseResponses_LeadingReasoningItemIsSkipped(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o",
		"input":[
			{"type":"reasoning","id":"rs_0","summary":[]},
			{"type":"message","role":"user","content":"hi"}
		]
	}`)
	req, perr := ParseResponses(body)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(req.Messages) != 1 || messageText(t, req.Messages[0]) != "hi" {
		t.Fatalf("want just the user message, got %+v", req.Messages)
	}
}

// A tool_choice naming a dropped hosted tool must degrade to the upstream
// default rather than failing the request.
func TestParseResponses_HostedToolChoiceDegradesToAuto(t *testing.T) {
	for _, choice := range []string{
		`{"type":"web_search"}`,
		`{"type":"mcp"}`,
		`{"type":"custom"}`,
		`{"type":"allowed_tools"}`,
	} {
		t.Run(choice, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":"hi","tool_choice":` + choice + `}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("hosted tool_choice must be tolerated, got %+v", perr)
			}
			if req.ToolChoice != nil {
				t.Fatalf("tool_choice must be left unset so the upstream defaults to auto, got %+v", req.ToolChoice)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("messages must survive, got %+v", req.Messages)
			}
		})
	}
}

// A function tool_choice is still honoured exactly.
func TestParseResponses_FunctionToolChoiceKept(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tool_choice":{"type":"function","name":"lookup"}}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	m, ok := req.ToolChoice.(map[string]any)
	if !ok || m["type"] != "function" {
		t.Fatalf("function tool_choice must be preserved, got %+v", req.ToolChoice)
	}
}

// Content that carries real user payload and cannot be represented is still
// refused, so the gateway never silently answers without an attachment.
func TestParseResponses_StillRejectsContentBearingParts(t *testing.T) {
	rejected := map[string]string{
		"input_file": `{"model":"gpt-4o","input":[{"role":"user","content":[
			{"type":"input_text","text":"read this"},
			{"type":"input_file","file_id":"file_1"}
		]}]}`,
		"input_audio": `{"model":"gpt-4o","input":[{"role":"user","content":[
			{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}
		]}]}`,
	}
	for name, body := range rejected {
		if _, perr := ParseResponses([]byte(body)); perr == nil {
			t.Fatalf("%s must still be rejected rather than silently dropped", name)
		}
	}
}

// A genuinely unknown item type still errors: blanket-skipping would hide a
// mistyped or message-bearing item instead of surfacing it.
func TestParseResponses_UnknownItemTypeStillErrors(t *testing.T) {
	body := `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":"hi"},{"type":"totally_made_up"}]}`
	_, perr := ParseResponses([]byte(body))
	if perr == nil {
		t.Fatal("an unrecognized item type must still be reported")
	}
	if perr.Param == "" {
		t.Fatal("the error must name the offending param")
	}
}

func TestRenderResponses_BasicText(t *testing.T) {
	up := &openai.TextResponse{
		Id:      "chatcmpl-abc123",
		Created: 1700000000,
		Choices: []openai.TextResponseChoice{{
			Index: 0,
			Message: relaymodel.Message{
				Role:    "assistant",
				Content: "the answer",
			},
			FinishReason: "stop",
		}},
		Usage: relaymodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	env := RenderResponses(up, "gpt-4o")
	if env["id"] != "resp_abc123" {
		t.Fatalf("id = %v", env["id"])
	}
	if env["object"] != "response" || env["status"] != "completed" {
		t.Fatalf("object/status wrong: %v %v", env["object"], env["status"])
	}
	out, ok := env["output"].([]map[string]any)
	if !ok || len(out) != 1 {
		t.Fatalf("output wrong: %+v", env["output"])
	}
	if out[0]["type"] != "message" {
		t.Fatalf("output[0].type = %v", out[0]["type"])
	}
	usage := env["usage"].(map[string]any)
	if usage["input_tokens"] != 10 || usage["output_tokens"] != 5 || usage["total_tokens"] != 15 {
		t.Fatalf("usage wrong: %+v", usage)
	}
	// No cache details when upstream did not report them (§4.1).
	if _, exists := usage["input_tokens_details"]; exists {
		t.Fatalf("must not fabricate input_tokens_details")
	}
}

func TestRenderResponses_LengthMapsToIncomplete(t *testing.T) {
	up := &openai.TextResponse{
		Choices: []openai.TextResponseChoice{{FinishReason: "length", Message: relaymodel.Message{Content: "partial"}}},
	}
	env := RenderResponses(up, "gpt-4o")
	if env["status"] != "incomplete" {
		t.Fatalf("status = %v", env["status"])
	}
	if env["incomplete_details"] == nil {
		t.Fatalf("expected incomplete_details")
	}
}

func TestRenderResponses_ToolCallItem(t *testing.T) {
	up := &openai.TextResponse{
		Choices: []openai.TextResponseChoice{{
			Message: relaymodel.Message{
				Role: "assistant",
				ToolCalls: []relaymodel.Tool{{
					Id:       "call_9",
					Type:     "function",
					Function: relaymodel.Function{Name: "f", Arguments: `{"a":1}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
	env := RenderResponses(up, "gpt-4o")
	out := env["output"].([]map[string]any)
	var fc map[string]any
	for _, o := range out {
		if o["type"] == "function_call" {
			fc = o
		}
	}
	if fc == nil {
		t.Fatalf("missing function_call output: %+v", out)
	}
	if fc["call_id"] != "call_9" || fc["name"] != "f" || fc["arguments"] != `{"a":1}` {
		t.Fatalf("function_call wrong: %+v", fc)
	}
}

func TestRenderResponses_CacheDetailsOnlyWhenReported(t *testing.T) {
	cached := 7
	up := &openai.TextResponse{
		Usage: relaymodel.Usage{
			PromptTokens:        100,
			CompletionTokens:    3,
			TotalTokens:         103,
			PromptTokensDetails: &relaymodel.PromptTokensDetails{CachedTokens: &cached},
		},
	}
	env := RenderResponses(up, "gpt-4o")
	usage := env["usage"].(map[string]any)
	details, ok := usage["input_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("expected input_tokens_details")
	}
	if details["cached_tokens"] != 7 {
		t.Fatalf("cached_tokens = %v", details["cached_tokens"])
	}
	// Envelope must be JSON-serializable.
	if _, err := json.Marshal(env); err != nil {
		t.Fatalf("marshal failed: %s", err)
	}
}
