package protocol

import (
	"encoding/json"
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

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
		"store_true":           `{"model":"gpt-4o","input":"hi","store":true}`,
		"unknown_item_type":    `{"model":"gpt-4o","input":[{"type":"mystery_item"}]}`,
		"reasoning_item":       `{"model":"gpt-4o","input":[{"type":"reasoning"}]}`,
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

func TestParseResponses_StoreFalseIsAllowed(t *testing.T) {
	_, perr := ParseResponses([]byte(`{"model":"gpt-4o","input":"hi","store":false}`))
	if perr != nil {
		t.Fatalf("store=false must be accepted, got %+v", perr)
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
