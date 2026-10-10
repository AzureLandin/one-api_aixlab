package openai_responses

import (
	"encoding/json"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

type event struct {
	Type string
	Data string
}

// feed runs events through the converter and returns every chunk a client would
// see, in the converter's own shape and as the bytes that go on the wire.
func feed(t *testing.T, state *StreamState, events ...event) ([]openai.ChatCompletionsStreamResponse, []string) {
	t.Helper()
	var typed []openai.ChatCompletionsStreamResponse
	var wires []string
	for _, ev := range events {
		for _, chunk := range state.Convert(ev.Type, []byte(ev.Data)) {
			raw, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal converted chunk: %v", err)
			}
			typed = append(typed, chunk)
			wires = append(wires, string(raw))
		}
	}
	return typed, wires
}

// absorb folds the wire form into the same accumulator the two sinks use, which
// is what proves the chunks are readable as the normalized framing.
func absorb(t *testing.T, wires []string) *sse.StreamState {
	t.Helper()
	state := sse.NewState()
	for _, wire := range wires {
		if err := state.Absorb(sse.Event{Data: wire}); err != nil {
			t.Fatalf("absorb %s: %v", wire, err)
		}
	}
	return state
}

const (
	createdEvent = `{"type":"response.created","response":{"id":"resp_abc","model":"gpt-5-upstream","created_at":1700000000,"status":"in_progress"}}`

	messageItemAdded = `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`

	callAItemAdded = `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_A","name":"alpha","arguments":""}}`
	callBItemAdded = `{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_2","type":"function_call","call_id":"call_B","name":"beta","arguments":""}}`
)

func TestStreamStateMapsTextAndDenseToolIndex(t *testing.T) {
	state := NewStreamState("gpt-5-mock")
	typed, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.output_item.added", messageItemAdded},
		event{"response.content_part.added", `{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`},
		event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`},
		event{"response.output_item.added", callAItemAdded},
		event{"response.output_item.added", callBItemAdded},
		// The arguments interleave, and the second call's arrive first: a chat
		// client still has to see dense indices 0 and 1 in announcement order.
		event{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":2,"delta":"{\"b\":"}`},
		event{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"a\":1}"}`},
		event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":" world"}`},
		event{"response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","item_id":"fc_2","output_index":2,"arguments":"{\"b\":2}"}`},
		event{"response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_A","name":"alpha","arguments":"{\"a\":1}"}}`},
		event{"response.completed", `{"type":"response.completed","response":{"id":"resp_abc","status":"completed","output":[` +
			`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello world","annotations":[]}]},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_A","name":"alpha","arguments":"{\"a\":1}","status":"completed"},` +
			`{"id":"fc_2","type":"function_call","call_id":"call_B","name":"beta","arguments":"{\"b\":2}","status":"completed"}],` +
			`"usage":{"input_tokens":50,"output_tokens":12,"total_tokens":62,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":3}}}}`},
	)

	if !state.Terminal() {
		t.Fatalf("response.completed is the terminal event of a Responses stream")
	}
	if state.UpstreamError() != nil {
		t.Fatalf("a completed stream is not an error: %+v", state.UpstreamError())
	}

	// The leading chunk announces the role, carrying the identity from
	// response.created in chat terms.
	if len(typed) == 0 || typed[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("the first chunk must announce the assistant role, got %+v", typed)
	}
	if typed[0].Id != "chatcmpl-abc" || typed[0].Model != "gpt-5-upstream" || typed[0].Created != 1700000000 {
		t.Fatalf("unexpected chunk identity: %+v", typed[0])
	}
	if typed[0].Object != "chat.completion.chunk" {
		t.Fatalf("unexpected object: %q", typed[0].Object)
	}

	// The announcing chunk for each call carries the id and the name; the dense
	// index is what the client groups argument fragments by.
	announced := map[string]int{}
	for _, chunk := range typed {
		if len(chunk.Choices) == 0 {
			// The usage-only tail carries no choice.
			continue
		}
		for _, call := range chunk.Choices[0].Delta.ToolCalls {
			if call.Id != "" {
				announced[call.Id] = *call.Index
			}
		}
	}
	if announced["call_A"] != 0 || announced["call_B"] != 1 {
		t.Fatalf("tool indices must be dense in announcement order, got %+v", announced)
	}

	acc := absorb(t, wires)
	if acc.TextValue() != "Hello world" {
		t.Fatalf("unexpected accumulated text: %q", acc.TextValue())
	}
	calls := acc.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected two tool calls, got %+v", calls)
	}
	if calls[0].Index != 0 || calls[0].ID != "call_A" || calls[0].Name != "alpha" || calls[0].Arguments() != `{"a":1}` {
		t.Fatalf("unexpected first call: %+v", calls[0])
	}
	if calls[1].Index != 1 || calls[1].ID != "call_B" || calls[1].Name != "beta" || calls[1].Arguments() != `{"b":2}` {
		t.Fatalf("unexpected second call: %+v", calls[1])
	}
	if acc.FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q", acc.FinishReason)
	}
	if !acc.UsageReported || acc.Usage == nil {
		t.Fatalf("the terminal event's usage must reach the accumulator")
	}
	if acc.Usage.PromptTokens != 50 || acc.Usage.CompletionTokens != 12 || acc.Usage.TotalTokens != 62 {
		t.Fatalf("unexpected usage: %+v", *acc.Usage)
	}
	if acc.Usage.CacheReadInputTokens == nil || *acc.Usage.CacheReadInputTokens != 40 {
		t.Fatalf("input_tokens_details.cached_tokens must be the cache read count, got %v", acc.Usage.CacheReadInputTokens)
	}
	if acc.Usage.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("expected cache status reported, got %q", acc.Usage.CacheStatus)
	}
	if acc.Usage.CompletionTokensDetails == nil || acc.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens must survive the round trip: %+v", acc.Usage.CompletionTokensDetails)
	}
	// And the same ladder the sinks use reports the upstream's numbers.
	usage := acc.FinalizeUsage(999, "gpt-5-mock", func(text, model string) int { return openai.CountTokenText(text, model) })
	if usage == nil || usage.PromptTokens != 50 || usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("reported usage must win over the local estimate: %+v", usage)
	}
}

func TestStreamStateFlushesContentTheUpstreamNeverStreamed(t *testing.T) {
	// An upstream that reports nothing until its terminal snapshot: no text and
	// no argument deltas at all. The snapshot is the answer and must be flushed.
	state := NewStreamState("gpt-5-mock")
	_, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.output_item.added", callAItemAdded},
		event{"response.completed", `{"type":"response.completed","response":{"id":"resp_abc","status":"completed","output":[` +
			`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_A","name":"alpha","arguments":"{\"a\":1}","status":"completed"}],` +
			`"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`},
	)

	acc := absorb(t, wires)
	if acc.TextValue() != "done" {
		t.Fatalf("text reported only in the snapshot must reach the client, got %q", acc.TextValue())
	}
	calls := acc.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments() != `{"a":1}` {
		t.Fatalf("arguments reported only in the snapshot must reach the client, got %+v", calls)
	}
	if calls[0].ID != "call_A" || calls[0].Name != "alpha" {
		t.Fatalf("the snapshot call must stay identifiable: %+v", calls[0])
	}
	if acc.FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q", acc.FinishReason)
	}
}

func TestStreamStateDoesNotRepeatWhatWasAlreadyStreamed(t *testing.T) {
	// The snapshot repeats the deltas verbatim, which is the common case: nothing
	// may be emitted twice.
	state := NewStreamState("gpt-5-mock")
	_, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"Hello"}`},
		event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":" world"}`},
		event{"response.completed", `{"type":"response.completed","response":{"id":"resp_abc","status":"completed","output":[` +
			`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}]}}`},
	)
	acc := absorb(t, wires)
	if acc.TextValue() != "Hello world" {
		t.Fatalf("the snapshot must not be appended to the deltas, got %q", acc.TextValue())
	}
	if acc.UsageReported {
		t.Fatalf("no usage was reported, so none may be recorded as reported")
	}
}

func TestStreamStateLeavesDisagreeingArgumentsAlone(t *testing.T) {
	// The finished arguments are not an extension of what was streamed. The
	// client already holds the streamed text, so the only honest move is to keep
	// it and record the disagreement, not to emit a second, contradictory copy.
	state := NewStreamState("gpt-5-mock")
	_, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.output_item.added", callAItemAdded},
		event{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"a\":1}"}`},
		event{"response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":1,"arguments":"{\"a\":999}"}`},
	)
	acc := absorb(t, wires)
	calls := acc.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments() != `{"a":1}` {
		t.Fatalf("expected the streamed arguments once, got %+v", calls)
	}
}

func TestStreamStateCarriesRefusalAndReasoning(t *testing.T) {
	state := NewStreamState("gpt-5-mock")
	typed, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.output_item.added", messageItemAdded},
		event{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"summary_index":0,"delta":"weighing"}`},
		event{"response.refusal.delta", `{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"I can't help"}`},
	)

	// Reasoning rides on the typed chunk as reasoning_content: the normalized
	// sse.Chunk framing has no slot for it, which is the documented loss shared
	// with the Anthropic codec's thinking blocks.
	var reasoning string
	for _, chunk := range typed {
		if len(chunk.Choices) == 0 {
			continue
		}
		if rc, ok := chunk.Choices[0].Delta.ReasoningContent.(string); ok {
			reasoning += rc
		}
	}
	if reasoning != "weighing" {
		t.Fatalf("a reasoning summary must reach the typed chunk, got %q", reasoning)
	}

	acc := absorb(t, wires)
	if acc.TextValue() != "I can't help" {
		t.Fatalf("a refusal is carried as text, got %q", acc.TextValue())
	}
}

func TestStreamStateStopsAtTheTerminalEvent(t *testing.T) {
	state := NewStreamState("gpt-5-mock")
	_, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.completed", `{"type":"response.completed","response":{"id":"resp_abc","status":"completed","output":[` +
			`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"done","annotations":[]}]}]}}`},
		// Anything after the terminal event belongs to no response the client has.
		event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"late"}`},
	)
	acc := absorb(t, wires)
	if acc.TextValue() != "done" {
		t.Fatalf("events after the terminal one must be ignored, got %q", acc.TextValue())
	}
	if acc.FinishReason != "stop" {
		t.Fatalf("expected finish_reason stop, got %q", acc.FinishReason)
	}
}

func TestStreamStateReportsFailuresAndKeepsHostedItemsOut(t *testing.T) {
	t.Run("response.failed", func(t *testing.T) {
		state := NewStreamState("gpt-5-mock")
		_, wires := feed(t, state,
			event{"response.created", createdEvent},
			event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"partial"}`},
			event{"response.failed", `{"type":"response.failed","response":{"id":"resp_abc","status":"failed","error":{"code":"overloaded","message":"slow down","type":"server_error"}}}`},
		)
		if !state.Terminal() {
			t.Fatalf("a failure ends the stream")
		}
		err := state.UpstreamError()
		if err == nil || err.Message != "slow down" || err.Type != "server_error" {
			t.Fatalf("the upstream's own failure must be carried, got %+v", err)
		}
		if acc := absorb(t, wires); acc.TextValue() != "partial" {
			t.Fatalf("what was delivered before the failure is still the client's, got %q", acc.TextValue())
		}
	})

	t.Run("error event", func(t *testing.T) {
		state := NewStreamState("gpt-5-mock")
		feed(t, state, event{"error", `{"type":"error","code":"rate_limit","message":"too fast"}`})
		err := state.UpstreamError()
		if !state.Terminal() || err == nil || err.Message != "too fast" {
			t.Fatalf("a bare error event must be terminal and carry its message, got %+v", err)
		}
	})

	t.Run("hosted item does not truncate the answer", func(t *testing.T) {
		state := NewStreamState("gpt-5-mock")
		_, wires := feed(t, state,
			event{"response.created", createdEvent},
			// A hosted tool call. It is understood and carried out of band as a
			// hosted artifact, so it is neither fatal nor a chat-envelope item.
			event{"response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`},
			event{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"delta":"answer"}`},
			event{"response.completed", `{"type":"response.completed","response":{"id":"resp_abc","status":"completed","output":[` +
				`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}}`},
		)
		if state.UpstreamError() != nil {
			t.Fatalf("an unmapped item must not fail the whole answer: %+v", state.UpstreamError())
		}
		if acc := absorb(t, wires); acc.TextValue() != "answer" {
			t.Fatalf("the answer around the unmapped item must still arrive, got %q", acc.TextValue())
		}
	})
}

func TestStreamStatePassesFinishReasonFromIncomplete(t *testing.T) {
	state := NewStreamState("gpt-5-mock")
	_, wires := feed(t, state,
		event{"response.created", createdEvent},
		event{"response.incomplete", `{"type":"response.incomplete","response":{"id":"resp_abc","status":"incomplete",` +
			`"incomplete_details":{"reason":"max_output_tokens"},"output":[` +
			`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"half","annotations":[]}]}]}}`},
	)
	if !state.Terminal() {
		t.Fatalf("response.incomplete is terminal")
	}
	if state.UpstreamError() != nil {
		t.Fatalf("a truncated answer is not an upstream failure: %+v", state.UpstreamError())
	}
	if acc := absorb(t, wires); acc.FinishReason != "length" {
		t.Fatalf("a truncated answer must finish as length, got %q", acc.FinishReason)
	}
}
