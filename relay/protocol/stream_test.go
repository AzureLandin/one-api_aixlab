package protocol

import (
	"strings"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// fakeSSEWriter captures framed events; implements io.Writer only (no flusher).
type capture struct{ buf strings.Builder }

func (c *capture) Write(p []byte) (int, error) { return c.buf.Write(p) }

func mustParse(t *testing.T, data string) sse.Chunk {
	t.Helper()
	ch, err := sse.ParseChunk(data)
	if err != nil {
		t.Fatalf("parse chunk: %s", err)
	}
	return ch
}

func TestResponsesStream_TypedEventsText(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewResponsesStream(w, "resp_1", "gpt-4o", nil)

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`))
	_ = s.Finish("stop", &relaymodel.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})

	out := c.buf.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing typed event %q in:\n%s", want, out)
		}
	}
	// deltas must be individually framed, not concatenated into one blob.
	if strings.Count(out, "event: response.output_text.delta") != 2 {
		t.Fatalf("expected 2 text deltas, out:\n%s", out)
	}
	if !strings.Contains(out, `"delta":"Hel"`) || !strings.Contains(out, `"delta":"lo"`) {
		t.Fatalf("delta payloads wrong:\n%s", out)
	}
	if !strings.Contains(out, `"status":"completed"`) {
		t.Fatalf("completed frame missing status:\n%s", out)
	}
}

func TestResponsesStream_ToolArgsNotCrossed(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewResponsesStream(w, "resp_2", "gpt-4o", nil)

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"fa","arguments":"{\"x\":"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"fb","arguments":"{\"y\":"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}`))
	_ = s.Finish("tool_calls", nil)

	out := c.buf.String()
	if !strings.Contains(out, `"arguments":"{\"x\":1}"`) {
		t.Fatalf("tool a args wrong/crossed:\n%s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"y\":2}"`) {
		t.Fatalf("tool b args wrong/crossed:\n%s", out)
	}
}

func TestResponsesStream_ErrorSurfaced(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewResponsesStream(w, "resp_3", "gpt-4o", nil)
	_ = s.Error(relaymodel.Error{Message: "boom", Type: "server_error"})
	out := c.buf.String()
	if !strings.Contains(out, "event: response.failed") || !strings.Contains(out, "boom") {
		t.Fatalf("error event not surfaced:\n%s", out)
	}
}

func TestMessagesStream_TypedEventsText(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewMessagesStream(w, "msg_1", "claude-3-5-sonnet", &relaymodel.Usage{PromptTokens: 9})

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"Hi"}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`))
	_ = s.Finish("stop", &relaymodel.Usage{PromptTokens: 9, CompletionTokens: 2})

	out := c.buf.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("stop_reason mapping wrong:\n%s", out)
	}
	if !strings.Contains(out, `"input_tokens":9`) {
		t.Fatalf("message_start missing prompt seed:\n%s", out)
	}
}

func TestMessagesStream_ToolUseInputJSONDelta(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewMessagesStream(w, "msg_2", "claude", nil)

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`))
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`))
	_ = s.Finish("tool_calls", &relaymodel.Usage{CompletionTokens: 4})

	out := c.buf.String()
	if !strings.Contains(out, `"type":"tool_use"`) {
		t.Fatalf("no tool_use block_start:\n%s", out)
	}
	if strings.Count(out, `"type":"input_json_delta"`) != 2 {
		t.Fatalf("expected 2 input_json_delta, got:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("tool_calls must map stop_reason to tool_use:\n%s", out)
	}
}

func TestMessagesStream_ErrorEvent(t *testing.T) {
	c := &capture{}
	w := sse.NewWriter(c)
	s := NewMessagesStream(w, "msg_3", "claude", nil)
	_ = s.Error(relaymodel.Error{Message: "nope", Type: "overloaded_error"})
	out := c.buf.String()
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "overloaded_error") {
		t.Fatalf("error event not emitted:\n%s", out)
	}
	// must be terminal: a later Finish should not append stop events
	before := c.buf.Len()
	_ = s.Finish("stop", nil)
	if c.buf.Len() != before {
		t.Fatalf("Error must be terminal; Finish wrote more:\n%s", c.buf.String()[before:])
	}
}
