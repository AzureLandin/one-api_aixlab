package protocol

import (
	"fmt"
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
	text     strings.Builder

	toolOpen    map[int]string // tool index -> function_call item id
	toolSeen    []int          // wire order of tool indices
	toolArgs    map[int]*strings.Builder
	toolName    map[int]string // tool index -> function name
	toolCall    map[int]string // tool index -> upstream call id
	toolStarted map[int]bool   // tool index -> output_item.added already emitted
	toolByID    map[string]int // function_call item id -> tool index

	// order lists the output item ids in the order they were added. A position in
	// it is the output_index every response.* event must carry: the message item
	// and the function_call items share one numbering, so the indices stay dense
	// even when a turn opens a tool call before any text. It is also the ordered
	// source for the terminal response.output, which is why a client can read the
	// finished tool calls off response.completed alone.
	order []string

	// hostedItem is the rendered form of each hosted artifact, keyed by the item id
	// it is addressed by, and hostedIndex its position in order. A hosted artifact
	// IS an output item on this protocol — web_search_call and mcp_call are item
	// types — so it takes a place in the same numbering as the message and
	// function_call items. hostedDone marks the ones already closed, so Finish only
	// closes what the upstream left open.
	hostedItem  map[string]map[string]any
	hostedIndex map[string]int
	hostedDone  map[string]bool
	hostedSeq   int
	losses      relaymodel.HostedLossLog

	// customTools names the tools the client declared as freeform. They go
	// upstream as functions taking one string argument, so their calls have to
	// come back as custom_tool_call items — with the raw input instead of an
	// argument object. A nil set means the client declared none.
	customTools relaymodel.ResponsesCustomToolNames
}

// NewResponsesStream writes response.created and response.in_progress.
// customTools is the request's set of Responses custom (freeform) tool names;
// pass nil when the client declared none.
func NewResponsesStream(w *sse.Writer, respID, model string, customTools relaymodel.ResponsesCustomToolNames) *ResponsesStream {
	r := &ResponsesStream{
		w:           w,
		respID:      respID,
		model:       model,
		itemID:      "msg_" + strings.TrimPrefix(respID, "resp_"),
		toolOpen:    map[int]string{},
		toolArgs:    map[int]*strings.Builder{},
		toolName:    map[int]string{},
		toolCall:    map[int]string{},
		toolStarted: map[int]bool{},
		toolByID:    map[string]int{},
		hostedItem:  map[string]map[string]any{},
		hostedIndex: map[string]int{},
		hostedDone:  map[string]bool{},
		customTools: customTools,
	}
	base := r.snapshot("in_progress", nil, false)
	_ = w.Event("response.created", map[string]any{"type": "response.created", "response": base})
	_ = w.Event("response.in_progress", map[string]any{"type": "response.in_progress", "response": base})
	return r
}

// custom reports whether the call at this tool index is one the client declared
// as a freeform tool. A call the upstream never named is never custom: without a
// name there is nothing to match against.
func (r *ResponsesStream) custom(idx int) bool {
	return r.customTools.Has(r.toolName[idx])
}

// addItem appends an output item and returns its output_index.
func (r *ResponsesStream) addItem(id string) int {
	r.order = append(r.order, id)
	return len(r.order) - 1
}

// outputIndex returns the output_index of an already-added item.
func (r *ResponsesStream) outputIndex(id string) int {
	for i, v := range r.order {
		if v == id {
			return i
		}
	}
	return 0
}

// textItem builds the assistant message output item at the given status.
func (r *ResponsesStream) textItem(status string) map[string]any {
	return map[string]any{
		"id": r.itemID, "type": "message", "role": "assistant", "status": status,
		"content": []any{
			map[string]any{"type": "output_text", "text": r.text.String(), "annotations": []any{}},
		},
	}
}

// functionCallItem builds the tool-call output item at the given status: a
// custom_tool_call when the client declared this tool freeform, a function_call
// otherwise.
//
// A function's `arguments` is always a JSON object string — the Responses API
// rejects an empty or non-object payload, so a call that streamed nothing is
// closed with `{}` exactly like the chat-completions path does. A custom tool
// carries its raw input instead and an empty one stays empty: there is no object
// to fall back to, and `{}` would be the wrong type there.
func (r *ResponsesStream) functionCallItem(toolIdx int, status string) map[string]any {
	args := ""
	if b := r.toolArgs[toolIdx]; b != nil {
		args = b.String()
	}
	if r.custom(toolIdx) {
		return map[string]any{
			"id": r.toolOpen[toolIdx], "type": "custom_tool_call",
			"call_id": r.toolCall[toolIdx], "name": r.toolName[toolIdx],
			"input": customToolInputFromArguments(args), "status": status,
		}
	}
	if args == "" {
		args = "{}"
	}
	return map[string]any{
		"id": r.toolOpen[toolIdx], "type": "function_call",
		"call_id": r.toolCall[toolIdx], "name": r.toolName[toolIdx],
		"arguments": args, "status": status,
	}
}

// toolItemInProgress builds the item a response.output_item.added announces.
// It is a shape of its own because nothing is known about the payload yet: the
// arguments are still arriving, so a function announces an empty argument object
// and a custom tool an empty input.
func (r *ResponsesStream) toolItemInProgress(toolIdx int) map[string]any {
	if r.custom(toolIdx) {
		return map[string]any{"id": r.toolOpen[toolIdx], "type": "custom_tool_call", "call_id": r.toolCall[toolIdx],
			"name": r.toolName[toolIdx], "input": "", "status": "in_progress"}
	}
	return map[string]any{"id": r.toolOpen[toolIdx], "type": "function_call", "call_id": r.toolCall[toolIdx],
		"name": r.toolName[toolIdx], "arguments": "", "status": "in_progress"}
}

func (r *ResponsesStream) ensureTextItem() error {
	if !r.textOpen {
		r.textOpen = true
		outIdx := r.addItem(r.itemID)
		if err := r.w.Event("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"item_id":      r.itemID,
			"output_index": outIdx,
			"item": map[string]any{"id": r.itemID, "type": "message", "role": "assistant",
				"status": "in_progress", "content": []any{}},
		}); err != nil {
			return err
		}
	}
	if !r.partOpen {
		r.partOpen = true
		if err := r.w.Event("response.content_part.added", map[string]any{
			"type":          "response.content_part.added",
			"item_id":       r.itemID,
			"output_index":  r.outputIndex(r.itemID),
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
		r.text.WriteString(txt)
		if err := r.w.Event("response.output_text.delta", map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       r.itemID,
			"output_index":  r.outputIndex(r.itemID),
			"content_index": 0,
			"delta":         txt,
		}); err != nil {
			return err
		}
	}
	for _, td := range ch.ToolDeltas() {
		if _, registered := r.toolArgs[td.Index]; !registered {
			r.toolArgs[td.Index] = &strings.Builder{}
			r.toolSeen = append(r.toolSeen, td.Index)
		}
		// The call id, the name and the arguments may each arrive first and be
		// spread over several fragments, so every fragment is folded in as it
		// arrives. An id or a name that changes mid-call means the upstream reused
		// one index for two different calls, which cannot be rendered faithfully.
		if td.ID != "" {
			if prev := r.toolCall[td.Index]; prev != "" && prev != td.ID {
				return fmt.Errorf("上游把工具索引 %d 同时用于 %q 和 %q", td.Index, prev, td.ID)
			}
			r.toolCall[td.Index] = td.ID
		}
		if td.Name != "" {
			if prev := r.toolName[td.Index]; prev != "" && prev != td.Name {
				return fmt.Errorf("上游把工具索引 %d 从 %q 改名为 %q", td.Index, prev, td.Name)
			}
			r.toolName[td.Index] = td.Name
		}
		if td.Arguments != "" {
			r.toolArgs[td.Index].WriteString(td.Arguments)
		}
		if r.toolStarted[td.Index] {
			if td.Arguments != "" {
				if err := r.emitArgsDelta(td.Index, td.Arguments); err != nil {
					return err
				}
			}
			continue
		}
		// The item is announced only once it can be identified: a function_call
		// without a name is not dispatchable by the client. Fragments that arrived
		// before the name are already buffered and are flushed as one delta
		// immediately after the announcement, so no fragment is emitted twice and
		// none is lost from the delta stream.
		if r.toolName[td.Index] == "" {
			continue
		}
		fcID := r.toolItemID(td.Index)
		r.toolOpen[td.Index] = fcID
		r.toolByID[fcID] = td.Index
		r.toolStarted[td.Index] = true
		outIdx := r.addItem(fcID)
		if err := r.w.Event("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"item_id":      fcID,
			"output_index": outIdx,
			"item":         r.toolItemInProgress(td.Index),
		}); err != nil {
			return err
		}
		if b := r.toolArgs[td.Index]; b.Len() > 0 {
			if err := r.emitArgsDelta(td.Index, b.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Hosted renders one hosted-tool artifact transition as a Responses output item.
//
// A hosted artifact is an output item in this protocol, so it is announced and
// closed the same way a function_call is, and it takes its place in the same
// r.order — which is what makes its output_index dense and its position the order
// the upstream reported. A later phase of the same artifact (its arguments
// arriving, its result landing) refines the item rather than adding a second one,
// so the client sees one item whose id stays the same from added to done.
//
// A kind this protocol has no item type for is recorded and skipped rather than
// invented, and nothing here is ever a refusal: the response is already streaming.
func (r *ResponsesStream) Hosted(ev relaymodel.HostedStreamEvent) error {
	item := ev.Item
	if item.ID == "" {
		// Defensive: both stream bridges address an artifact by an id the upstream
		// gave it, because a later phase has to be matched back to an earlier one.
		item.ID = r.respID + "_hosted_" + itoa(r.hostedSeq)
		r.hostedSeq++
	}
	losses := lossSink(&r.losses)
	path := pathOfHosted(len(r.hostedItem))
	if _, seen := r.hostedItem[item.ID]; seen {
		// What the protocol cannot express was named when the artifact was first
		// seen; re-rendering it as each phase refines it must not name it again.
		losses = discardLosses{}
	}
	rendered, ok := renderResponsesHostedItem(item, path, losses)
	if !ok {
		return nil
	}
	r.hostedItem[item.ID] = rendered

	outIdx, announced := r.hostedIndex[item.ID]
	if !announced {
		outIdx = r.addItem(item.ID)
		r.hostedIndex[item.ID] = outIdx
		return r.w.Event("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "item_id": item.ID,
			"output_index": outIdx, "item": rendered,
		})
	}
	if ev.Phase != relaymodel.HostedPhaseDone {
		return nil
	}
	r.hostedDone[item.ID] = true
	return r.w.Event("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "item_id": item.ID,
		"output_index": outIdx, "item": rendered,
	})
}

// LossOf reports what the client's protocol could not carry back. The stream
// phase never rejects under any policy, so this is the only place the loss can
// surface.
func (r *ResponsesStream) LossOf() []relaymodel.ConversionDiagnostic { return r.losses.LossOf() }

// emitArgsDelta writes one function_call_arguments.delta. The event names the
// call by item_id and output_index; the real API sends no call_id here.
//
// A custom tool emits nothing from here. Its input arrives as a JSON object
// being assembled fragment by fragment, and a fragment like `{"input":"*** Beg`
// cannot be unwrapped into the raw text the client expects — so the whole input
// is sent once, at close, as custom_tool_call_input.delta. See Finish.
func (r *ResponsesStream) emitArgsDelta(idx int, args string) error {
	if r.custom(idx) {
		return nil
	}
	return r.w.Event("response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      r.toolOpen[idx],
		"output_index": r.outputIndex(r.toolOpen[idx]),
		"delta":        args,
	})
}

// toolItemID is the id of the function_call output item. It is resolved when the
// item is announced, not when the call is first seen, because the upstream may
// identify the call on a later fragment.
func (r *ResponsesStream) toolItemID(idx int) string {
	if id := r.toolCall[idx]; id != "" {
		return id
	}
	return r.respID + "_call_" + itoa(idx)
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
		text := r.text.String()
		outIdx := r.outputIndex(r.itemID)
		if err := r.w.Event("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": r.itemID,
			"output_index": outIdx, "content_index": 0, "text": text,
		}); err != nil {
			return err
		}
		if err := r.w.Event("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": r.itemID,
			"output_index": outIdx, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}); err != nil {
			return err
		}
		if err := r.w.Event("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "item_id": r.itemID, "output_index": outIdx,
			"item": r.textItem(status),
		}); err != nil {
			return err
		}
	}
	for _, idx := range r.toolSeen {
		if r.toolStarted[idx] {
			continue
		}
		// A call the upstream introduced but never named still has to appear in the
		// terminal output: dropping it would silently delete a call the model made.
		// It is announced here, before being closed.
		fcID := r.toolItemID(idx)
		r.toolOpen[idx] = fcID
		r.toolByID[fcID] = idx
		r.toolStarted[idx] = true
		outIdx := r.addItem(fcID)
		if err := r.w.Event("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"item_id":      fcID,
			"output_index": outIdx,
			"item":         r.toolItemInProgress(idx),
		}); err != nil {
			return err
		}
		// Fragments buffered while the call was still nameless must reach the delta
		// stream too, so it stays a faithful prefix of the item's arguments.
		if b := r.toolArgs[idx]; b.Len() > 0 {
			if err := r.emitArgsDelta(idx, b.String()); err != nil {
				return err
			}
		}
	}
	// Emit in output_index order so the closing events match the item numbering.
	for _, id := range r.order {
		if item, ok := r.hostedItem[id]; ok {
			// A hosted artifact the upstream opened and never closed is closed here
			// as incomplete. It is not fabricated a result it did not have, and it
			// is not left dangling in the terminal output either: response.completed
			// repeats every item, and an item still marked in_progress there would
			// leave the client to guess whether the search ran.
			if r.hostedDone[id] {
				continue
			}
			if st, _ := item["status"].(string); st == relaymodel.HostedStatusInProgress {
				item["status"] = relaymodel.HostedStatusIncomplete
			}
			if err := r.w.Event("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "item_id": id,
				"output_index": r.hostedIndex[id], "item": item,
			}); err != nil {
				return err
			}
			continue
		}
		idx, ok := r.toolByID[id]
		if !ok {
			continue
		}
		fcID := id
		outIdx := r.outputIndex(fcID)
		item := r.functionCallItem(idx, status)
		// A custom tool's input never streamed fragment by fragment, and cannot:
		// each fragment is a piece of a JSON object that only means anything once
		// whole. It is delivered here instead — one delta carrying the entire raw
		// text, then the closing event. An empty call skips the delta rather than
		// announcing an empty payload twice.
		if !r.custom(idx) {
			if err := r.w.Event("response.function_call_arguments.done", map[string]any{
				"type": "response.function_call_arguments.done", "item_id": fcID,
				"output_index": outIdx, "arguments": item["arguments"],
			}); err != nil {
				return err
			}
		} else {
			input, _ := item["input"].(string)
			if input != "" {
				if err := r.w.Event("response.custom_tool_call_input.delta", map[string]any{
					"type": "response.custom_tool_call_input.delta", "item_id": fcID,
					"output_index": outIdx, "delta": input,
				}); err != nil {
					return err
				}
			}
			if err := r.w.Event("response.custom_tool_call_input.done", map[string]any{
				"type": "response.custom_tool_call_input.done", "item_id": fcID,
				"output_index": outIdx, "input": input,
			}); err != nil {
				return err
			}
		}
		if err := r.w.Event("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "item_id": fcID,
			"output_index": outIdx, "item": item,
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

// snapshot is the response object carried by response.created/in_progress and by
// response.completed. The terminal one is not a skeleton: it repeats every output
// item in order, because a Responses client reads the finished function calls
// from response.output rather than accumulating the deltas.
//
// A hosted item keeps its OWN status here rather than taking the response's: a
// search that completed did complete, and an mcp_call that failed did fail, even
// when the turn they belong to ended some other way. Only an artifact the
// upstream never closed is still in_progress at this point, and Finish has
// already rewritten those to incomplete.
func (r *ResponsesStream) snapshot(status string, usage *relaymodel.Usage, withUsage bool) map[string]any {
	items := make([]any, 0, len(r.order))
	for _, id := range r.order {
		if id == r.itemID {
			items = append(items, r.textItem(status))
			continue
		}
		if item, ok := r.hostedItem[id]; ok {
			items = append(items, item)
			continue
		}
		if idx, ok := r.toolByID[id]; ok {
			items = append(items, r.functionCallItem(idx, status))
		}
	}
	out := map[string]any{
		"id":     r.respID,
		"object": "response",
		"status": status,
		"model":  r.model,
		"output": items,
	}
	if withUsage && usage != nil {
		out["usage"] = buildResponsesUsage(*usage)
	}
	return out
}
