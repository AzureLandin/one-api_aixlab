package openai_responses

import (
	"os"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// TestMain switches token counting to the approximate counter. The real
// tiktoken encoders are loaded by main.go's openai.InitTokenEncoders at process
// startup, which a unit test does not run; the approximate switch is the same
// one the gateway itself uses when no encoder is available, and it keeps these
// tests about the conversion rather than about tokenization.
func TestMain(m *testing.M) {
	config.ApproximateTokenEnabled = true
	os.Exit(m.Run())
}

func TestToUsageKeepsAbsentCacheDistinctFromReportedZero(t *testing.T) {
	// Nothing at all: not a usage report, so the caller must estimate instead.
	if usage, ok := ToUsage(nil); ok || usage != nil {
		t.Fatalf("a missing usage block is not a report, got %+v ok=%v", usage, ok)
	}
	if usage, ok := ToUsage(&Usage{}); ok || usage != nil {
		t.Fatalf("an all-zero block with no details is not a report, got %+v ok=%v", usage, ok)
	}

	// A details block present with zero cached tokens IS a report: §4.1 forbids
	// turning a reported zero into "not reported".
	usage, ok := ToUsage(&Usage{
		InputTokens:        50,
		OutputTokens:       12,
		TotalTokens:        62,
		InputTokensDetails: &InputTokensDetails{CachedTokens: ptrInt(0)},
	})
	if !ok {
		t.Fatalf("a details block makes the usage a report")
	}
	if usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 0 {
		t.Fatalf("a reported zero must stay a pointer-to-zero, got %v", usage.CacheReadInputTokens)
	}
	if usage.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("expected cache status reported, got %q", usage.CacheStatus)
	}

	// Absent details: the cache side stays nil and the status says so.
	silent, ok := ToUsage(&Usage{InputTokens: 50, OutputTokens: 12, TotalTokens: 62})
	if !ok {
		t.Fatalf("token counts alone are a report")
	}
	if silent.CacheReadInputTokens != nil {
		t.Fatalf("an absent cache field must stay nil, got %v", *silent.CacheReadInputTokens)
	}
	if silent.CacheStatus != relaymodel.CacheUsageNotReported {
		t.Fatalf("expected cache status not_reported, got %q", silent.CacheStatus)
	}
}

func TestToUsageMapsCacheReasoningAndSource(t *testing.T) {
	usage, ok := ToUsage(&Usage{
		InputTokens:  50,
		OutputTokens: 12,
		// The upstream's own total is not trusted: the aggregation layer sums the
		// two halves, so the recorded total is recomputed.
		TotalTokens:         999,
		InputTokensDetails:  &InputTokensDetails{CachedTokens: ptrInt(40)},
		OutputTokensDetails: &OutputTokensDetails{ReasoningTokens: ptrInt(7)},
	})
	if !ok {
		t.Fatalf("expected a report")
	}
	if usage.PromptTokens != 50 || usage.CompletionTokens != 12 || usage.TotalTokens != 62 {
		t.Fatalf("unexpected token counts: %+v", usage)
	}
	if usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("expected source reported, got %q", usage.Source)
	}
	if usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 40 {
		t.Fatalf("cached tokens must reach the normalized cache read count, got %v", usage.CacheReadInputTokens)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 7 {
		t.Fatalf("reasoning tokens must carry over, got %+v", usage.CompletionTokensDetails)
	}
}

func TestToChatResponseRendersTextAndToolCalls(t *testing.T) {
	resp := &Response{
		ID:        "resp_abc",
		Model:     "gpt-5-upstream",
		Status:    "completed",
		CreatedAt: 1700000000,
		Output: []OutputItem{
			{Type: "message", ID: "msg_1", Role: "assistant", Content: []OutputContent{
				{Type: "output_text", Text: "let me check"},
			}},
			{Type: "function_call", ID: "fc_1", CallId: "call_A", Name: "alpha", Arguments: `{"city":"sf"}`},
		},
		Usage: &Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
	}

	textResponse, err := ToChatResponse(resp, "mapped-model", 99)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Message)
	}
	if textResponse.Id != "chatcmpl-abc" {
		t.Fatalf("the responses id must become a chat id, got %q", textResponse.Id)
	}
	if textResponse.Object != "chat.completion" {
		t.Fatalf("unexpected object: %q", textResponse.Object)
	}
	if textResponse.Model != "gpt-5-upstream" {
		t.Fatalf("the upstream's own model name is what the client sees, got %q", textResponse.Model)
	}
	if len(textResponse.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(textResponse.Choices))
	}
	choice := textResponse.Choices[0]
	if choice.Message.StringContent() != "let me check" {
		t.Fatalf("unexpected text: %q", choice.Message.StringContent())
	}
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("a tool call must be reported as tool_calls, got %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", choice.Message.ToolCalls)
	}
	call := choice.Message.ToolCalls[0]
	if call.Id != "call_A" || call.Type != "function" || call.Function.Name != "alpha" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
	if call.Function.Arguments != `{"city":"sf"}` {
		t.Fatalf("arguments must arrive as the upstream's JSON string, got %#v", call.Function.Arguments)
	}
	// The reported usage wins over the fallback prompt count.
	if textResponse.Usage.PromptTokens != 10 || textResponse.Usage.CompletionTokens != 4 {
		t.Fatalf("reported usage must win: %+v", textResponse.Usage)
	}
	if textResponse.Usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("expected source reported, got %q", textResponse.Usage.Source)
	}
}

func TestToChatResponseMapsIncompleteStatus(t *testing.T) {
	resp := &Response{
		ID:                "resp_abc",
		Status:            "incomplete",
		Output:            []OutputItem{{Type: "message", Content: []OutputContent{{Type: "output_text", Text: "half"}}}},
		Usage:             &Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		IncompleteDetails: &IncompleteDetails{Reason: "max_output_tokens"},
	}
	textResponse, err := ToChatResponse(resp, "m", 0)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Message)
	}
	if textResponse.Choices[0].FinishReason != "length" {
		t.Fatalf("a truncated answer must be reported as length, got %q", textResponse.Choices[0].FinishReason)
	}

	resp.IncompleteDetails.Reason = "content_filter"
	textResponse, err = ToChatResponse(resp, "m", 0)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Message)
	}
	if textResponse.Choices[0].FinishReason != "content_filter" {
		t.Fatalf("a filtered answer must be reported as content_filter, got %q", textResponse.Choices[0].FinishReason)
	}
}

func TestToChatResponseEstimatesWhenTheUpstreamIsSilent(t *testing.T) {
	// The same answer twice, once with a tool call: the arguments are part of what
	// the model produced, so the estimate has to count them.
	plain := &Response{
		ID: "resp_abc", Status: "completed",
		Output: []OutputItem{{Type: "message", Content: []OutputContent{{Type: "output_text", Text: "ok"}}}},
	}
	withCall := &Response{
		ID: "resp_abc", Status: "completed",
		Output: []OutputItem{
			{Type: "message", Content: []OutputContent{{Type: "output_text", Text: "ok"}}},
			{Type: "function_call", ID: "fc_1", CallId: "call_A", Name: "alpha",
				Arguments: `{"location":"san francisco","unit":"celsius"}`},
		},
	}

	plainResponse, err := ToChatResponse(plain, "m", 42)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Message)
	}
	callResponse, err := ToChatResponse(withCall, "m", 42)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Message)
	}

	for _, got := range []relaymodel.Usage{plainResponse.Usage, callResponse.Usage} {
		if got.Source != relaymodel.UsageSourceEstimated {
			t.Fatalf("a silent upstream must be recorded as estimated, got %q", got.Source)
		}
		if got.PromptTokens != 42 {
			t.Fatalf("the prompt fallback must be used, got %d", got.PromptTokens)
		}
		if got.TotalTokens != got.PromptTokens+got.CompletionTokens {
			t.Fatalf("estimated totals must be consistent: %+v", got)
		}
		if got.CacheReadInputTokens != nil {
			t.Fatalf("an estimate must not invent a cache report: %v", *got.CacheReadInputTokens)
		}
	}
	if callResponse.Usage.CompletionTokens <= plainResponse.Usage.CompletionTokens {
		t.Fatalf("tool arguments must be counted: %d vs %d",
			callResponse.Usage.CompletionTokens, plainResponse.Usage.CompletionTokens)
	}
}

func TestToChatResponseRefusesACallWithoutAnyId(t *testing.T) {
	resp := &Response{
		ID: "resp_abc", Status: "completed",
		Output: []OutputItem{{Type: "function_call", Name: "alpha", Arguments: "{}"}},
	}
	textResponse, err := ToChatResponse(resp, "m", 0)
	if err == nil {
		t.Fatalf("a call with no id cannot be handed to a client, got %+v", textResponse)
	}
	if err.Code != unmappedItemCode {
		t.Fatalf("expected a named refusal, got %v", err.Code)
	}
}

func TestFailureReadsOnlyAFailedStatus(t *testing.T) {
	if e := (&Response{Status: "completed"}).Failure(); e != nil {
		t.Fatalf("a completed answer is not a failure: %+v", e)
	}
	e := (&Response{Status: "failed", Error: &ResponseError{
		Message: "model overloaded", Type: "server_error", Code: "overloaded",
	}}).Failure()
	if e == nil || e.Message != "model overloaded" || e.Type != "server_error" {
		t.Fatalf("the upstream's own error must be carried through, got %+v", e)
	}
}

func TestChatIDForRoundTripsAResponsesID(t *testing.T) {
	cases := map[string]string{
		"resp_abc":    "chatcmpl-abc",
		"chatcmpl-ab": "chatcmpl-ab",
		"":            "",
	}
	for in, want := range cases {
		if got := ChatIDFor(in); got != want {
			t.Fatalf("ChatIDFor(%q) = %q, want %q", in, got, want)
		}
	}
}
