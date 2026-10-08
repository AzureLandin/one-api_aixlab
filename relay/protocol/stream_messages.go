package protocol

import (
	"strings"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// MessagesStream emits Anthropic Messages typed SSE events (§6.5/§7.1):
// message_start -> content_block_start/delta/stop -> message_delta(stop_reason,
// usage) -> message_stop. Tool-call argument fragments are grouped by index so
// each becomes one input_json_delta stream feeding a single tool_use block.
type MessagesStream struct {
	w     *sse.Writer
	id    string
	model string

	textBlock     int // index of the text content block, -1 if none opened
	nextIndex     int // next assignable content-block index
	openBlocks    map[int]int
	toolBlocks    map[int]int
	toolFirstSeen map[int]bool
	argsSeen      map[int]*strings.Builder
	closed        bool
}

// NewMessagesStream writes the message_start event with an empty content array.
func NewMessagesStream(w *sse.Writer, id, model string, usage *relaymodel.Usage) *MessagesStream {
	m := &MessagesStream{
		w:             w,
		id:            id,
		model:         model,
		textBlock:     -1,
		openBlocks:    map[int]int{},
		toolBlocks:    map[int]int{},
		toolFirstSeen: map[int]bool{},
		argsSeen:      map[int]*strings.Builder{},
	}
	inputTokens := 0
	if usage != nil {
		inputTokens = usage.PromptTokens
	}
	_ = w.Event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": m.id, "type": "message", "role": "assistant", "model": m.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
	return m
}

func (m *MessagesStream) openTextBlock() error {
	if m.textBlock >= 0 {
		return nil
	}
	m.textBlock = m.nextIndex
	m.nextIndex++
	return m.w.Event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         m.textBlock,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

// Chunk translates one OpenAI chat chunk into Anthropic stream events.
func (m *MessagesStream) Chunk(ch sse.Chunk) error {
	if txt := ch.TextDelta(); txt != "" {
		if err := m.openTextBlock(); err != nil {
			return err
		}
		if err := m.w.Event("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": m.textBlock,
			"delta": map[string]any{"type": "text_delta", "text": txt},
		}); err != nil {
			return err
		}
	}
	for _, td := range ch.ToolDeltas() {
		blockIdx, seen := m.toolBlocks[td.Index]
		if !seen {
			blockIdx = m.nextIndex
			m.nextIndex++
			m.toolBlocks[td.Index] = blockIdx
			m.toolFirstSeen[td.Index] = true
			m.argsSeen[td.Index] = &strings.Builder{}
			if err := m.w.Event("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": blockIdx,
				"content_block": map[string]any{
					"type": "tool_use", "id": firstNonEmpty(td.ID, itoa(td.Index)),
					"name": td.Name, "input": map[string]any{},
				},
			}); err != nil {
				return err
			}
		}
		if td.Arguments != "" {
			m.argsSeen[td.Index].WriteString(td.Arguments)
			if err := m.w.Event("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": td.Arguments},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Finish closes open blocks and writes message_delta + message_stop.
// finishReason is the OpenAI finish_reason, mapped to an Anthropic stop_reason
// here (inverse of the anthropic adaptor's mapping). The usage is the SAME
// normalized value the recorder persists.
func (m *MessagesStream) Finish(finishReason string, usage *relaymodel.Usage) error {
	if m.closed {
		return nil
	}
	m.closed = true
	if m.textBlock >= 0 {
		_ = m.w.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": m.textBlock})
	}
	for _, idx := range sortedKeys(m.toolBlocks) {
		_ = m.w.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": m.toolBlocks[idx]})
	}
	stopReason := finishReasonToStopReason(finishReason)
	out := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}}
	if usage != nil {
		out["usage"] = map[string]int{"output_tokens": usage.CompletionTokens}
	}
	_ = m.w.Event("message_delta", out)
	_ = m.w.Event("message_stop", map[string]any{"type": "message_stop"})
	return nil
}

// Error surfaces an upstream error as an Anthropic error event (§7.1).
func (m *MessagesStream) Error(e relaymodel.Error) error {
	if m.closed {
		return nil
	}
	m.closed = true
	errType := e.Type
	if errType == "" {
		errType = "api_error"
	}
	return m.w.Event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": e.Message},
	})
}

func sortedKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion order of tool indices is by wire arrival; sort for stable close.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
