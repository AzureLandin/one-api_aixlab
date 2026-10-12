package protocol

import (
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func TestParseMessages_SystemAndTextBlocks(t *testing.T) {
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":1024,
		"system":"you are helpful",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	req, perr := ParseMessages(body)
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "you are helpful" {
		t.Fatalf("system wrong: %+v", req.Messages[0])
	}
	if req.MaxTokens != 1024 {
		t.Fatalf("max_tokens = %d", req.MaxTokens)
	}
	if req.Messages[1].StringContent() != "hi" {
		t.Fatalf("user content = %+v", req.Messages[1].Content)
	}
}

func TestParseMessages_StringContent(t *testing.T) {
	req, perr := ParseMessages([]byte(`{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":"plain"}]}`))
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if req.Messages[0].Content != "plain" {
		t.Fatalf("content = %v", req.Messages[0].Content)
	}
}

func TestParseMessages_ImageBase64Block(t *testing.T) {
	req, perr := ParseMessages([]byte(`{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}
	]}]}`))
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	parts, ok := req.Messages[0].Content.([]relaymodel.MessageContent)
	if !ok || len(parts) != 1 {
		t.Fatalf("content parts wrong: %+v", req.Messages[0].Content)
	}
	if parts[0].ImageURL == nil || parts[0].ImageURL.Url != "data:image/png;base64,QUJD" {
		t.Fatalf("image url wrong: %+v", parts[0])
	}
}

func TestParseMessages_ToolUseAndResult(t *testing.T) {
	body := []byte(`{"model":"claude","max_tokens":10,"messages":[
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}
		]}
	]}`)
	req, perr := ParseMessages(body)
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	// assistant tool_use -> message with ToolCalls
	var asst, tool *relaymodel.Message
	for i := range req.Messages {
		switch req.Messages[i].Role {
		case "assistant":
			asst = &req.Messages[i]
		case "tool":
			tool = &req.Messages[i]
		}
	}
	if asst == nil || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant tool_use missing: %+v", req.Messages)
	}
	if asst.ToolCalls[0].Function.Name != "get_weather" || asst.ToolCalls[0].Id != "toolu_1" {
		t.Fatalf("tool_use fields wrong: %+v", asst.ToolCalls[0])
	}
	if tool == nil || tool.ToolCallId != "toolu_1" || tool.Content != "sunny" {
		t.Fatalf("tool_result mapping wrong: %+v", req.Messages)
	}
}

func TestParseMessages_ToolsAndToolChoice(t *testing.T) {
	body := []byte(`{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"get_weather","description":"d","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice":{"type":"any"}}`)
	req, perr := ParseMessages(body)
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools wrong: %+v", req.Tools)
	}
	if req.Tools[0].Function.Parameters == nil {
		t.Fatalf("parameters dropped")
	}
	if req.ToolChoice != "required" {
		t.Fatalf("tool_choice any -> required mapping wrong: %v", req.ToolChoice)
	}
}

// The role travels to the upstream as the client wrote it. Anthropic's own API
// allows only user/assistant here, but the gateway is not Anthropic: a chat
// upstream accepts more, and both other converters fold a system turn into their
// single system slot. Refusing it here only refused requests every downstream was
// built to serve — it was a named 400 until a client hit it in production.
func TestParseMessages_PassesSystemRoleThrough(t *testing.T) {
	req, perr := ParseMessages([]byte(`{"model":"claude","max_tokens":10,"messages":[
		{"role":"system","content":"be terse"},
		{"role":"user","content":"hi"}]}`))
	if perr != nil {
		t.Fatalf("a system turn must not be refused: %+v", perr)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if req.Messages[0].Role != "system" || req.Messages[0].StringContent() != "be terse" {
		t.Fatalf("the system turn must survive verbatim: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", req.Messages)
	}
}

func TestParseMessages_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing_max_tokens": `{"model":"claude","messages":[{"role":"user","content":"hi"}]}`,
		"zero_max_tokens":    `{"model":"claude","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
		"missing_messages":   `{"model":"claude","max_tokens":10}`,
		"unknown_block":      `{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":[{"type":"document"}]}]}`,
		"tool_use_in_user":   `{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_use","name":"f"}]}]}`,
		"missing_model":      `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
	}
	for name, body := range cases {
		_, perr := ParseMessages([]byte(body))
		if perr == nil {
			t.Fatalf("%s: expected rejection", name)
		}
		if perr.Param == "" {
			t.Fatalf("%s: must name param", name)
		}
	}
}

func TestRenderMessages_Text(t *testing.T) {
	up := &openai.TextResponse{
		Id: "chatcmpl-zzz",
		Choices: []openai.TextResponseChoice{{
			Message:      relaymodel.Message{Role: "assistant", Content: "hello there"},
			FinishReason: "stop",
		}},
		Usage: relaymodel.Usage{PromptTokens: 12, CompletionTokens: 4},
	}
	env := RenderMessages(up, "claude-3-5-sonnet")
	if env["type"] != "message" || env["role"] != "assistant" {
		t.Fatalf("type/role wrong: %v %v", env["type"], env["role"])
	}
	if env["id"] != "msg_zzz" {
		t.Fatalf("id = %v", env["id"])
	}
	content := env["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "text" || content[0]["text"] != "hello there" {
		t.Fatalf("content wrong: %+v", content)
	}
	if env["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason wrong: %v", env["stop_reason"])
	}
	usage := env["usage"].(map[string]int)
	if usage["input_tokens"] != 12 || usage["output_tokens"] != 4 {
		t.Fatalf("usage wrong: %+v", usage)
	}
}

func TestRenderMessages_ToolUseAndStopReason(t *testing.T) {
	up := &openai.TextResponse{
		Choices: []openai.TextResponseChoice{{
			Message: relaymodel.Message{Role: "assistant", ToolCalls: []relaymodel.Tool{{
				Id:       "toolu_5",
				Function: relaymodel.Function{Name: "f", Arguments: `{"x":1}`},
			}}},
			FinishReason: "tool_calls",
		}},
	}
	env := RenderMessages(up, "claude")
	content := env["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "tool_use" {
		t.Fatalf("tool_use block missing: %+v", content)
	}
	if content[0]["id"] != "toolu_5" || content[0]["name"] != "f" {
		t.Fatalf("tool_use fields wrong: %+v", content[0])
	}
	input, ok := content[0]["input"].(map[string]any)
	if !ok || input["x"] != float64(1) {
		t.Fatalf("input not parsed from arguments: %+v", content[0]["input"])
	}
	if env["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason should be tool_use: %v", env["stop_reason"])
	}
}

func TestRenderMessages_LengthMapsToMaxTokens(t *testing.T) {
	up := &openai.TextResponse{
		Choices: []openai.TextResponseChoice{{FinishReason: "length", Message: relaymodel.Message{Content: "x"}}},
	}
	env := RenderMessages(up, "claude")
	sr := env["stop_reason"]
	if sr != "max_tokens" {
		t.Fatalf("stop_reason = %v", sr)
	}
}

func TestFinishReasonToStopReason(t *testing.T) {
	cases := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "stop_sequence": "stop_sequence"}
	for fr, want := range cases {
		got := finishReasonToStopReason(fr)
		if got == nil || *got != want {
			t.Fatalf("%s -> %v want %s", fr, got, want)
		}
	}
	if finishReasonToStopReason("") != nil {
		t.Fatalf("empty finish must map to nil stop_reason")
	}
}
