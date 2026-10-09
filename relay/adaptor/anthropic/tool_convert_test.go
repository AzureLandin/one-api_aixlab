package anthropic

import (
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func toolPointer(b bool) *bool { return &b }

// A standard OpenAI assistant turn is `content: null` plus tool_calls. The
// tool_use blocks used to be built only inside the string-content branch, so
// this exact turn lost every call and orphaned the tool_result that follows.
func TestConvertRequest_AssistantToolCallsWithNullContent(t *testing.T) {
	req := ConvertRequest(relaymodel.GeneralOpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []relaymodel.Message{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", Content: nil, ToolCalls: []relaymodel.Tool{{
				Id:       "call_1",
				Type:     "function",
				Function: relaymodel.Function{Name: "get_weather", Arguments: `{"city":"SF"}`},
			}}},
			{Role: "tool", ToolCallId: "call_1", Content: "sunny"},
		},
	})

	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	assistant := req.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("assistant role = %q", assistant.Role)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].Type != "tool_use" {
		t.Fatalf("tool_use block missing: %+v", assistant.Content)
	}
	use := assistant.Content[0]
	if use.Id != "call_1" || use.Name != "get_weather" {
		t.Fatalf("tool_use identity wrong: %+v", use)
	}
	input, ok := use.Input.(map[string]any)
	if !ok || input["city"] != "SF" {
		t.Fatalf("tool_use input not parsed: %#v", use.Input)
	}
	// The tool result folds into a user turn with the matching tool_use_id.
	last := req.Messages[2]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
		t.Fatalf("tool_result block wrong: %+v", last)
	}
	if last.Content[0].ToolUseId != "call_1" || last.Content[0].Content != "sunny" {
		t.Fatalf("tool_result payload wrong: %+v", last.Content[0])
	}
}

// A JSON Schema is open-ended, and a client may omit even `type`. The old code
// asserted `params["type"].(string)` and panicked (HTTP 500) instead.
func TestConvertRequest_OpenEndedAndMissingSchema(t *testing.T) {
	req := ConvertRequest(relaymodel.GeneralOpenAIRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		Tools: []relaymodel.Tool{
			{Type: "function", Function: relaymodel.Function{
				Name:       "open_ended",
				Parameters: map[string]any{"anyOf": []any{map[string]any{"type": "string"}}},
			}},
			{Type: "function", Function: relaymodel.Function{Name: "no_schema"}},
			// An unnamed function cannot be called by name; Anthropic rejects it.
			{Type: "function", Function: relaymodel.Function{Name: ""}},
		},
	})
	if len(req.Tools) != 2 {
		t.Fatalf("want 2 usable tools, got %d: %+v", len(req.Tools), req.Tools)
	}
	if _, ok := req.Tools[0].InputSchema.(map[string]any); !ok {
		t.Fatalf("open-ended schema not passed through: %#v", req.Tools[0].InputSchema)
	}
	schema, ok := req.Tools[1].InputSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("missing schema must default to an object: %#v", req.Tools[1].InputSchema)
	}
}

// An empty or malformed argument string must not reach Anthropic as `input: null`
// (rejected) or as a non-object literal.
func TestConvertRequest_EmptyAndMalformedToolArguments(t *testing.T) {
	for name, args := range map[string]any{
		"nil":       nil,
		"empty":     "",
		"malformed": `{"city":`,
		"scalar":    `"just-a-string"`,
	} {
		req := ConvertRequest(relaymodel.GeneralOpenAIRequest{
			Model: "claude-3-5-sonnet",
			Messages: []relaymodel.Message{{Role: "assistant", ToolCalls: []relaymodel.Tool{{
				Id: "call_1", Type: "function",
				Function: relaymodel.Function{Name: "f", Arguments: args},
			}}}},
		})
		if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
			t.Fatalf("%s: tool_use missing: %+v", name, req.Messages)
		}
		input, ok := req.Messages[0].Content[0].Input.(map[string]any)
		if !ok || len(input) != 0 {
			t.Fatalf("%s: input must be an empty object, got %#v", name, req.Messages[0].Content[0].Input)
		}
	}
}

// Consecutive OpenAI turns of the same role are routine (parallel tool results,
// a user text turn after them) but Anthropic requires alternating roles.
func TestConvertRequest_MergesAdjacentToolResults(t *testing.T) {
	req := ConvertRequest(relaymodel.GeneralOpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []relaymodel.Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []relaymodel.Tool{
				{Id: "a", Type: "function", Function: relaymodel.Function{Name: "f", Arguments: "{}"}},
				{Id: "b", Type: "function", Function: relaymodel.Function{Name: "g", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallId: "a", Content: "1"},
			{Role: "tool", ToolCallId: "b", Content: "2"},
			{Role: "user", Content: "thanks"},
		},
	})
	if len(req.Messages) != 3 {
		t.Fatalf("want user/assistant/user, got %d: %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[2].Role != "user" || len(req.Messages[2].Content) != 3 {
		t.Fatalf("tool results and the user turn must merge into one turn: %+v", req.Messages[2])
	}
}

func TestConvertRequest_StrictAndToolChoice(t *testing.T) {
	req := ConvertRequest(relaymodel.GeneralOpenAIRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		Tools: []relaymodel.Tool{{Type: "function", Function: relaymodel.Function{
			Name: "f", Parameters: map[string]any{"type": "object"}, Strict: toolPointer(true),
		}}},
		ToolChoice:       map[string]any{"type": "function", "function": map[string]any{"name": "f"}},
		ParallelTooCalls: toolPointer(false),
		Stop:             []string{"END"},
	})
	if req.Tools[0].Strict == nil || !*req.Tools[0].Strict {
		t.Fatalf("strict flag dropped: %+v", req.Tools[0])
	}
	choice, ok := req.ToolChoice.(ToolChoice)
	if !ok {
		t.Fatalf("tool_choice type = %T", req.ToolChoice)
	}
	if choice.Type != "tool" || choice.Name != "f" {
		t.Fatalf("tool_choice not mapped: %+v", choice)
	}
	if choice.DisableParallelToolUse == nil || !*choice.DisableParallelToolUse {
		t.Fatalf("parallel_tool_calls=false dropped: %+v", choice)
	}
	if len(req.StopSequences) != 1 || req.StopSequences[0] != "END" {
		t.Fatalf("stop sequences dropped: %+v", req.StopSequences)
	}
}

// Anthropic returns an ordered block list; only reading Content[0] both loses
// the text around a tool call and yields "" when the first block is a tool_use.
func TestResponseClaude2OpenAI_ConcatenatesTextAroundToolUse(t *testing.T) {
	resp := &Response{
		Id: "msg_1", Model: "claude-3-5-sonnet",
		Content: []Content{
			{Type: "text", Text: "before "},
			{Type: "tool_use", Id: "call_1", Name: "get_weather", Input: map[string]any{"city": "SF"}},
			{Type: "text", Text: "after"},
		},
	}
	out := ResponseClaude2OpenAI(resp)
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d", len(out.Choices))
	}
	msg := out.Choices[0].Message
	if msg.Content != "before after" {
		t.Fatalf("text blocks not concatenated: %#v", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool calls missing: %+v", msg.ToolCalls)
	}
	if args, _ := msg.ToolCalls[0].Function.Arguments.(string); args != `{"city":"SF"}` {
		t.Fatalf("arguments = %#v", msg.ToolCalls[0].Function.Arguments)
	}
}

// Two tool_use blocks streamed with interleaved argument fragments must carry a
// dense OpenAI index, otherwise a client cannot tell the calls apart.
func TestStreamResponseClaude2OpenAI_ToolIndexAndFinishReason(t *testing.T) {
	state := NewStreamState()
	cases := []StreamResponse{
		{Type: "content_block_start", Index: 0, ContentBlock: &Content{Type: "text"}},
		{Type: "content_block_start", Index: 1, ContentBlock: &Content{Type: "tool_use", Id: "call_a", Name: "get_weather"}},
		{Type: "content_block_delta", Index: 1, Delta: &Delta{Type: "input_json_delta", PartialJson: `{"city":`}},
		{Type: "content_block_start", Index: 2, ContentBlock: &Content{Type: "tool_use", Id: "call_b", Name: "get_time"}},
		{Type: "content_block_delta", Index: 2, Delta: &Delta{Type: "input_json_delta", PartialJson: `{"tz":"UTC"}`}},
		{Type: "content_block_delta", Index: 1, Delta: &Delta{Type: "input_json_delta", PartialJson: `"SF"}`}},
		{Type: "message_delta", Delta: &Delta{StopReason: strPtr("tool_use")}},
	}

	seen := map[int]string{}
	for _, ev := range cases {
		chunk, _ := StreamResponseClaude2OpenAI(state, &ev)
		if chunk == nil {
			continue
		}
		for _, choice := range chunk.Choices {
			// Every non-terminal chunk must leave finish_reason nil: clients treat
			// any non-null value as the end of the stream.
			if ev.Type != "message_delta" && choice.FinishReason != nil {
				t.Fatalf("%s: finish_reason set to %q on a non-terminal chunk", ev.Type, *choice.FinishReason)
			}
			for _, tc := range choice.Delta.ToolCalls {
				if tc.Index == nil {
					t.Fatalf("%s: tool delta without index: %+v", ev.Type, tc)
				}
				if tc.Id != "" {
					seen[*tc.Index] = tc.Id
				}
				if tc.Function.Name != "" {
					seen[*tc.Index] = tc.Function.Name
				}
			}
			if ev.Type == "message_delta" {
				if choice.FinishReason == nil || *choice.FinishReason != "tool_calls" {
					t.Fatalf("terminal finish_reason = %v", choice.FinishReason)
				}
			}
		}
	}
	// The text block occupies content-block 0 but must not consume an OpenAI
	// tool index, so the two calls map to 0 and 1 — not 1 and 2.
	if seen[0] != "get_weather" || seen[1] != "get_time" {
		t.Fatalf("tool indices not dense: %+v", seen)
	}
}

// A tool call that streamed no argument fragment must still close with `{}`, for
// every call, not just the last one seen.
func TestStreamResponseClaude2OpenAI_EmptyArgumentsPatched(t *testing.T) {
	state := NewStreamState()
	_ = state
	for _, ev := range []StreamResponse{
		{Type: "content_block_start", Index: 0, ContentBlock: &Content{Type: "tool_use", Id: "a", Name: "f"}},
		{Type: "content_block_start", Index: 1, ContentBlock: &Content{Type: "tool_use", Id: "b", Name: "g"}},
	} {
		_, _ = StreamResponseClaude2OpenAI(state, &ev)
	}
	chunk, _ := StreamResponseClaude2OpenAI(state, &StreamResponse{Type: "message_delta", Delta: &Delta{StopReason: strPtr("tool_use")}})
	if chunk == nil || len(chunk.Choices) == 0 {
		t.Fatalf("no terminal chunk")
	}
	patched := map[int]string{}
	for _, tc := range chunk.Choices[0].Delta.ToolCalls {
		if tc.Index == nil {
			t.Fatalf("patched delta without index: %+v", tc)
		}
		args, _ := tc.Function.Arguments.(string)
		patched[*tc.Index] = args
	}
	if len(patched) != 2 || patched[0] != "{}" || patched[1] != "{}" {
		t.Fatalf("empty args not patched for every call: %+v", patched)
	}
}

func strPtr(s string) *string { return &s }
