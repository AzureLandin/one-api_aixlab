package protocol

import (
	"strings"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// ResponsesStream emits typed SSE events for the OpenAI Responses API (§6.4:
// "流式事件必须有明确事件类型，不能把所有片段拼成一条非标准字符串"). It consumes
// canonical OpenAI chat.completion.chunk deltas and translates them into the
// response.* event family, tracking item/part lifecycles so fragments never
// cross-contaminate.
type ResponsesStream struct {
	w      *sse.Writer
	respID string
	model  string

	itemID   string // assistant message item
	textOpen bool
	partOpen bool

	toolOpen map[int]string // tool index -> function_call item id
	toolSeen []int          // wire order of tool indices
	toolArgs map[int]*strings.Builder
}

// NewResponsesStream writes response.created and response.in_progress.
func NewResponsesStream(w *sse.Writer, respID, model string) *ResponsesStream {
	r := &ResponsesStream{
		w:        w,
		respID:   respID,
		model:    model,
		itemID:   "msg_" + strings.TrimPrefix(respID, "resp_"),
		toolOpen: map[int]string{},
		toolArgs: map[int]*strings.Builder{},
	}
	base := r.snapshot("in_progress", nil, false)
	_ = w.Event("response.created", map[string]any{"type": "response.created", "response": base})
	_ = w.Event("response.in_progress", map[string]any{"type": "response.in_progress", "response": base})
	return r
}

func (r *ResponsesStream) ensureTextItem() error {
	if !r.textOpen {
		r.textOpen = true
		if err := r.w.Event("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"item_id":      r.itemID,
			"output_index": 0,
			"item":         map[string]any{"id": r.itemID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
		}); err != nil {
			return err
		}
	}
	if !r.partOpen {
		r.partOpen = true
		if err := r.w.Event("response.content_part.added", map[string]any{
			"type":          "response.content_part.added",
			"item_id":       r.itemID,
			"output_index":  0,
			"content_index": 0,
			"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// Chunk translates one OpenAI chat chunk into zero or more Responses events.
func (r *ResponsesStream) Chunk(ch sse.Chunk) error {
	if txt := ch.TextDelta(); txt != "" {
		if err := r.ensureTextItem(); err != nil {
			return err
		}
		if err := r.w.Event("response.output_text.delta", map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       r.itemID,
			"output_index":  0,
			"content_index": 0,
			"delta":         txt,
		}); err != nil {
			return err
		}
	}
	for _, td := range ch.ToolDeltas() {
		fcID, open := r.toolOpen[td.Index]
		if !open {
			fcID = "fc_" + firstNonEmpty(td.ID, itoa(td.Index))
			r.toolOpen[td.Index] = fcID
			r.toolSeen = append(r.toolSeen, td.Index)
			r.toolArgs[td.Index] = &strings.Builder{}
			name := td.Name
			// emit item.added for the function_call
			if err := r.w.Event("response.output_item.added", map[string]any{
				"type":         "response.output_item.added",
				"item_id":      fcID,
				"output_index": len(r.toolSeen), // message occupies 0
				"item": map[string]any{"id": fcID, "type": "function_call", "call_id": td.ID,
					"name": name, "arguments": "", "status": "in_progress"},
			}); err != nil {
				return err
			}
		}
		if td.Arguments != "" {
			r.toolArgs[td.Index].WriteString(td.Arguments)
			if err := r.w.Event("response.function_call_arguments.delta", map[string]any{
				"type":    "response.function_call_arguments.delta",
				"item_id": fcID,
				"call_id": r.toolOpen[td.Index],
				"delta":   td.Arguments,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Finish closes all open items/parts and emits response.completed (or
// .incomplete when the model hit max tokens) with the final usage. The usage
// passed in is the SAME normalized value the recorder persists (wire and
// record can never disagree).
func (r *ResponsesStream) Finish(finishReason string, usage *relaymodel.Usage) error {
	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}
	if r.textOpen {
		if err := r.w.Event("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": r.itemID,
			"output_index": 0, "content_index": 0,
		}); err != nil {
			return err
		}
		if err := r.w.Event("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": r.itemID,
			"output_index": 0, "content_index": 0,
		}); err != nil {
			return err
		}
		if err := r.w.Event("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "item_id": r.itemID, "output_index": 0,
			"item": map[string]any{"id": r.itemID, "type": "message", "role": "assistant", "status": "completed"},
		}); err != nil {
			return err
		}
	}
	for _, idx := range r.toolSeen {
		fcID := r.toolOpen[idx]
		args := ""
		if b := r.toolArgs[idx]; b != nil {
			args = b.String()
		}
		if err := r.w.Event("response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "item_id": fcID, "arguments": args,
		}); err != nil {
			return err
		}
		if err := r.w.Event("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "item_id": fcID,
			"item": map[string]any{"id": fcID, "type": "function_call", "status": "completed"},
		}); err != nil {
			return err
		}
	}
	_ = r.w.Event("response.completed", map[string]any{
		"type":     "response.completed",
		"response": r.snapshot(status, usage, true),
	})
	return nil
}

// Error surfaces an upstream error as a typed response.failed event (§7.1).
func (r *ResponsesStream) Error(e relaymodel.Error) error {
	return r.w.Event("response.failed", map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": r.respID, "object": "response", "status": "failed", "error": e,
		},
	})
}

func (r *ResponsesStream) snapshot(status string, usage *relaymodel.Usage, withUsage bool) map[string]any {
	out := map[string]any{
		"id":     r.respID,
		"object": "response",
		"status": status,
		"model":  r.model,
		"output": []any{},
	}
	if withUsage && usage != nil {
		out["usage"] = buildResponsesUsage(*usage)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
