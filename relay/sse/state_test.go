package sse

import (
	"encoding/json"
	"strings"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func chunkEvent(payload string) Event { return Event{Data: payload} }

func TestState_TextAndFinishReason(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hel"}}]}`))
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`))
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	_ = s.Absorb(chunkEvent(`[DONE]`))

	if s.TextValue() != "Hello" {
		t.Fatalf("text = %q", s.TextValue())
	}
	if s.FinishReason != "stop" {
		t.Fatalf("finish = %q", s.FinishReason)
	}
	if !s.SawDone() {
		t.Fatalf("expected done seen")
	}
	if s.TerminalStatus(false) != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s", s.TerminalStatus(false))
	}
}

func TestState_ToolArgsGroupedByIndex(t *testing.T) {
	s := NewState()
	// Two tool calls interleaved by index; fragments must not cross-contaminate.
	fragments := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"fa","arguments":"{\"x\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"fb","arguments":"{\"y\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	for _, f := range fragments {
		if err := s.Absorb(chunkEvent(f)); err != nil {
			t.Fatalf("absorb: %s", err)
		}
	}
	tools := s.ToolCalls()
	if len(tools) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(tools))
	}
	if tools[0].ID != "call_a" || tools[0].Name != "fa" || tools[0].Arguments() != `{"x":1}` {
		t.Fatalf("tool[0] crossed: %+v args=%q", tools[0], tools[0].Arguments())
	}
	if tools[1].ID != "call_b" || tools[1].Name != "fb" || tools[1].Arguments() != `{"y":2}` {
		t.Fatalf("tool[1] crossed: %+v args=%q", tools[1], tools[1].Arguments())
	}
}

func TestState_ReportedUsageWins(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`))
	_ = s.Absorb(chunkEvent(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	_ = s.Absorb(chunkEvent(`[DONE]`))

	u := s.FinalizeUsage(999, "gpt-4o", func(text, model string) int { return 42 })
	if u == nil || u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("reported usage must win: %+v", u)
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 3 || u.TotalTokens != 13 {
		t.Fatalf("reported values altered: %+v", u)
	}
}

func TestState_EstimatedWhenNoUsage(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"abcd"}}]}`))
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]}}]}`))
	_ = s.Absorb(chunkEvent(`[DONE]`))

	u := s.FinalizeUsage(7, "gpt-4o", func(text, model string) int {
		// estimator sees text + tool args
		if text != "abcd{}" {
			t.Fatalf("estimator input should include tool args, got %q", text)
		}
		return 5
	})
	if u == nil || u.Source != relaymodel.UsageSourceEstimated {
		t.Fatalf("expected estimated usage, got %+v", u)
	}
	if u.TotalTokens != 12 {
		t.Fatalf("total = %d", u.TotalTokens)
	}
}

func TestState_NoEstimateReturnsNilUsage(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`))
	_ = s.Absorb(chunkEvent(`[DONE]`))
	if u := s.FinalizeUsage(7, "gpt-4o", nil); u != nil {
		t.Fatalf("no counter + no reported usage must yield nil (never fabricate 0), got %+v", u)
	}
}

func TestState_UpstreamErrorNotSwallowed(t *testing.T) {
	s := NewState()
	errPayload, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": "boom", "type": "server_error"},
	})
	_ = s.Absorb(chunkEvent(string(errPayload)))
	if !s.HasError {
		t.Fatalf("upstream error must be recorded")
	}
	if s.TerminalStatus(false) != relaymodel.UsageStatusError {
		t.Fatalf("status should be error")
	}
}

func TestState_PartialOnAbortAndNoDone(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`))
	if s.TerminalStatus(true) != relaymodel.UsageStatusPartial {
		t.Fatalf("abort -> partial")
	}
	s2 := NewState()
	_ = s2.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`))
	// stream ended without [DONE] and not aborted by client -> still partial
	if s2.TerminalStatus(false) != relaymodel.UsageStatusPartial {
		t.Fatalf("missing done -> partial")
	}
}

func TestState_MalformedChunkReturnsError(t *testing.T) {
	s := NewState()
	if err := s.Absorb(chunkEvent("{not json")); err == nil {
		t.Fatalf("malformed json should return error for pass-through handling")
	}
}

func TestState_EstimatorUsesText(t *testing.T) {
	s := NewState()
	_ = s.Absorb(chunkEvent(`{"choices":[{"index":0,"delta":{"content":"hello"}}]}`))
	var captured string
	_ = s.FinalizeUsage(1, "m", func(text, model string) int { captured = text; return 1 })
	if !strings.Contains(captured, "hello") {
		t.Fatalf("estimator did not receive text: %q", captured)
	}
}
