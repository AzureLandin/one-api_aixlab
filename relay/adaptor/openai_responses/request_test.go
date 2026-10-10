package openai_responses

import (
	"encoding/json"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func ptrInt(v int) *int       { return &v }
func ptrBool(v bool) *bool    { return &v }
func ptrStr(v string) *string { return &v }

func TestChatToResponsesRequestSplitsInstructionsAndTurns(t *testing.T) {
	limit := 256
	strict := true
	req := &relaymodel.GeneralOpenAIRequest{
		Model:     "gpt-5-mock",
		Stream:    true,
		MaxTokens: 128,
		// max_completion_tokens is the current spelling and must win.
		MaxCompletionTokens: &limit,
		Temperature:         func() *float64 { v := 0.2; return &v }(),
		ReasoningEffort:     ptrStr("high"),
		Messages: []relaymodel.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []relaymodel.Tool{{
				Id:   "call_A",
				Type: "function",
				Function: relaymodel.Function{
					Name:      "alpha",
					Arguments: map[string]any{"city": "sf"},
				},
			}}},
			{Role: "tool", ToolCallId: "call_A", Content: "42"},
		},
		Tools: []relaymodel.Tool{{
			Type: "function",
			Function: relaymodel.Function{
				Name:        "alpha",
				Description: "looks up",
				Parameters:  map[string]any{"type": "object"},
				Strict:      &strict,
			},
		}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "alpha"}},
	}

	out, bizErr := ChatToResponsesRequest(req)
	if bizErr != nil {
		t.Fatalf("unexpected error: %v", bizErr.Error.Message)
	}
	if out.Instructions != "be brief" {
		t.Fatalf("system turn must fold into instructions, got %q", out.Instructions)
	}
	if out.MaxOutputTokens == nil || *out.MaxOutputTokens != 256 {
		t.Fatalf("max_completion_tokens must win over max_tokens, got %v", out.MaxOutputTokens)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != "high" {
		t.Fatalf("reasoning_effort must carry over, got %+v", out.Reasoning)
	}

	// The tool exchange becomes two items, not fields on a message.
	if len(out.Input) != 3 {
		t.Fatalf("expected user turn + function_call + function_call_output, got %d items: %+v", len(out.Input), out.Input)
	}
	if out.Input[0].Role != "user" || out.Input[0].Content != "weather?" {
		t.Fatalf("first item must be the user turn, got %+v", out.Input[0])
	}
	if out.Input[1].Type != "function_call" || out.Input[1].CallId != "call_A" ||
		out.Input[1].Name != "alpha" || out.Input[1].Arguments != `{"city":"sf"}` {
		t.Fatalf("assistant tool call must become a function_call item, got %+v", out.Input[1])
	}
	if out.Input[2].Type != "function_call_output" || out.Input[2].CallId != "call_A" || out.Input[2].Output != "42" {
		t.Fatalf("tool result must become a function_call_output item, got %+v", out.Input[2])
	}

	// Tools and tool_choice are flattened, unlike chat's nested spelling.
	if len(out.Tools) != 1 || out.Tools[0].Name != "alpha" || out.Tools[0].Parameters == nil || *out.Tools[0].Strict != true {
		t.Fatalf("tools must be flattened, got %+v", out.Tools)
	}
	choice, ok := out.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" || choice["name"] != "alpha" {
		t.Fatalf("named tool_choice must be flattened, got %+v", out.ToolChoice)
	}
	if choice["function"] != nil {
		t.Fatalf("the chat nesting must not survive into the Responses body: %+v", choice)
	}
}

func TestChatToResponsesRequestKeepsTheEmptyAssistantTurnOutOfTheInput(t *testing.T) {
	// An assistant turn that only called a tool carries no content: the call item
	// expresses it, and an empty message item would be noise for the upstream.
	req := &relaymodel.GeneralOpenAIRequest{
		Model: "gpt-5-mock",
		Messages: []relaymodel.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []relaymodel.Tool{{
				Id: "call_A", Type: "function",
				Function: relaymodel.Function{Name: "alpha", Arguments: "{}"},
			}}},
		},
	}
	out, bizErr := ChatToResponsesRequest(req)
	if bizErr != nil {
		t.Fatalf("unexpected error: %v", bizErr.Error.Message)
	}
	if len(out.Input) != 2 {
		t.Fatalf("expected user turn + function_call only, got %+v", out.Input)
	}
	if out.Input[1].Type != "function_call" {
		t.Fatalf("second item must be the call, got %+v", out.Input[1])
	}
}

func TestChatToResponsesRequestMapsContentParts(t *testing.T) {
	req := &relaymodel.GeneralOpenAIRequest{
		Model: "gpt-5-mock",
		Messages: []relaymodel.Message{{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "what is this"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
		}}},
	}
	out, bizErr := ChatToResponsesRequest(req)
	if bizErr != nil {
		t.Fatalf("unexpected error: %v", bizErr.Error.Message)
	}
	parts, ok := out.Input[0].Content.([]ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected two content parts, got %#v", out.Input[0].Content)
	}
	if parts[0].Type != "input_text" || parts[0].Text != "what is this" {
		t.Fatalf("text part must map to input_text, got %+v", parts[0])
	}
	if parts[1].Type != "input_image" || parts[1].ImageURL != "https://example.com/a.png" {
		t.Fatalf("image part must map to input_image, got %+v", parts[1])
	}
}

func TestChatToResponsesRequestMapsResponseFormat(t *testing.T) {
	req := &relaymodel.GeneralOpenAIRequest{
		Model:    "gpt-5-mock",
		Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &relaymodel.ResponseFormat{
			Type: "json_schema",
			JsonSchema: &relaymodel.JSONSchema{
				Name:        "answer",
				Description: "an answer",
				Schema:      map[string]any{"type": "object"},
				Strict:      ptrBool(true),
			},
		},
	}
	out, bizErr := ChatToResponsesRequest(req)
	if bizErr != nil {
		t.Fatalf("unexpected error: %v", bizErr.Error.Message)
	}
	if out.Text == nil || out.Text.Format == nil {
		t.Fatalf("response_format must become text.format, got %+v", out.Text)
	}
	format := out.Text.Format
	// Responses keeps name/schema/strict beside type rather than nested.
	if format["type"] != "json_schema" || format["name"] != "answer" || format["strict"] != true {
		t.Fatalf("unexpected format mapping: %+v", format)
	}
	if _, nested := format["json_schema"]; nested {
		t.Fatalf("the chat nesting must not survive: %+v", format)
	}
}

func TestChatToResponsesRequestRefusesUntranslatableFields(t *testing.T) {
	base := func() *relaymodel.GeneralOpenAIRequest {
		return &relaymodel.GeneralOpenAIRequest{
			Model:    "gpt-5-mock",
			Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*relaymodel.GeneralOpenAIRequest)
		code   string
	}{
		{"n", func(r *relaymodel.GeneralOpenAIRequest) { r.N = 2 }, upstreamUnsupportedField},
		{"stop", func(r *relaymodel.GeneralOpenAIRequest) { r.Stop = []string{"x"} }, upstreamUnsupportedField},
		{"logit_bias", func(r *relaymodel.GeneralOpenAIRequest) { r.LogitBias = map[string]any{"1": 1} }, upstreamUnsupportedField},
		{"seed", func(r *relaymodel.GeneralOpenAIRequest) { r.Seed = 7 }, upstreamUnsupportedField},
		{"logprobs", func(r *relaymodel.GeneralOpenAIRequest) { r.Logprobs = ptrBool(true) }, upstreamUnsupportedField},
		{"top_logprobs", func(r *relaymodel.GeneralOpenAIRequest) { r.TopLogprobs = ptrInt(3) }, upstreamUnsupportedField},
		{"modalities", func(r *relaymodel.GeneralOpenAIRequest) { r.Modalities = []string{"audio"} }, upstreamUnsupportedField},
		{"prediction", func(r *relaymodel.GeneralOpenAIRequest) { r.Prediction = map[string]any{} }, upstreamUnsupportedField},
		{"functions", func(r *relaymodel.GeneralOpenAIRequest) { r.Functions = []any{} }, upstreamUnsupportedField},
		{"function_call", func(r *relaymodel.GeneralOpenAIRequest) { r.FunctionCall = "auto" }, upstreamUnsupportedField},
		{"top_k", func(r *relaymodel.GeneralOpenAIRequest) { r.TopK = 20 }, upstreamUnsupportedField},
		{"num_ctx", func(r *relaymodel.GeneralOpenAIRequest) { r.NumCtx = 4096 }, upstreamUnsupportedField},
		{"instruction", func(r *relaymodel.GeneralOpenAIRequest) { r.Instruction = "do it" }, upstreamUnsupportedField},
		{"tool result without call id", func(r *relaymodel.GeneralOpenAIRequest) {
			r.Messages = append(r.Messages, relaymodel.Message{Role: "tool", Content: "42"})
		}, upstreamMissingToolCallId},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base()
			tc.mutate(req)
			out, bizErr := ChatToResponsesRequest(req)
			if bizErr == nil {
				t.Fatalf("expected a refusal, got body %+v", out)
			}
			if bizErr.Error.Code != tc.code {
				t.Fatalf("expected code %q, got %v (%s)", tc.code, bizErr.Error.Code, bizErr.Error.Message)
			}
		})
	}
}

func TestChatToResponsesRequestDropsStreamOptionsWithoutRefusing(t *testing.T) {
	// The Responses API always reports usage, so stream_options has nothing to
	// ask for; refusing would break clients that set it by default.
	req := &relaymodel.GeneralOpenAIRequest{
		Model:         "gpt-5-mock",
		Stream:        true,
		StreamOptions: &relaymodel.StreamOptions{IncludeUsage: true},
		ServiceTier:   ptrStr("flex"),
		Messages:      []relaymodel.Message{{Role: "user", Content: "hi"}},
	}
	out, bizErr := ChatToResponsesRequest(req)
	if bizErr != nil {
		t.Fatalf("unexpected refusal: %v", bizErr.Error.Message)
	}
	if !out.Stream {
		t.Fatalf("stream must survive")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["stream_options"]; ok {
		t.Fatalf("stream_options must not be sent to a Responses upstream: %s", raw)
	}
	if _, ok := fields["service_tier"]; ok {
		t.Fatalf("service_tier must not be sent to a Responses upstream: %s", raw)
	}
}

func TestChatToResponsesRequestRefusesHostedToolType(t *testing.T) {
	req := &relaymodel.GeneralOpenAIRequest{
		Model:    "gpt-5-mock",
		Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		Tools:    []relaymodel.Tool{{Type: "web_search", Function: relaymodel.Function{Name: "search"}}},
	}
	if _, bizErr := ChatToResponsesRequest(req); bizErr == nil {
		t.Fatalf("a hosted tool must be refused by name, not silently dropped")
	}
}
