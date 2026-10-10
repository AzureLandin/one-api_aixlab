package openai_responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// streamEvent is the union of the fields the Responses SSE events carry. One
// struct rather than one per event type: the events differ in which fields are
// present, and the conversion reads them by event type anyway.
type streamEvent struct {
	Type        string      `json:"type"`
	Response    *Response   `json:"response"`
	OutputIndex int         `json:"output_index"`
	ItemID      string      `json:"item_id"`
	Item        *OutputItem `json:"item"`
	// Delta is the incremental text of output_text / reasoning_summary /
	// function_call_arguments events.
	Delta string `json:"delta"`
	// Arguments is the complete argument string of function_call_arguments.done.
	Arguments any `json:"arguments"`
	// Refusal belongs to the refusal deltas; Code/Message/Error to the bare
	// `error` event and to a failed response.
	Refusal string         `json:"refusal"`
	Error   *ResponseError `json:"error"`
	Code    any            `json:"code"`
	Message string         `json:"message"`
}

// toolSlot is one upstream function call's place in the chat envelope: the
// upstream identifies it by output_index (shared with message items), while a
// chat client needs a dense tool index counting from zero, so the two are
// tracked apart. The same problem anthropic.StreamState.toolSlot solves for
// Anthropic content blocks.
type toolSlot struct {
	// slot is the dense index the chat client sees.
	slot   int
	outIdx int
	itemID string
	// id is the call id: the value the client echoes back as tool_call_id.
	id   string
	name string
	// sent is the argument text already emitted as deltas, so the terminal
	// snapshot can flush only what the upstream never streamed.
	sent strings.Builder
}

func (t *toolSlot) index() *int { i := t.slot; return &i }

// StreamState converts an upstream Responses event stream into normalized chat
// chunks. It is deliberately sink-free: the chat entry point writes the chunks
// out as `data:` lines and the protocol codec re-reads them as sse.Chunks, so
// the conversion is written once for both.
type StreamState struct {
	respID  string
	model   string
	created int64

	toolByItemID map[string]*toolSlot
	toolByOutIdx map[int]*toolSlot
	// toolSeq is the allocation order, which is the order a chat client must see
	// the tool calls in.
	toolSeq []*toolSlot

	// textSent is the answer text already emitted, used to flush the remainder
	// when an upstream reports text only in its terminal snapshot.
	textSent strings.Builder

	// started reports whether the leading role chunk has been emitted.
	started bool

	usage       *relaymodel.Usage
	terminal    bool
	upstreamErr *relaymodel.Error

	// logged memorizes the event/item types already reported as unmapped, so an
	// upstream that repeats one every turn does not flood the log.
	logged map[string]bool
}

// NewStreamState creates a converter for one upstream stream. modelName is the
// gateway's mapped model name, used only until the upstream names its own.
func NewStreamState(modelName string) *StreamState {
	return &StreamState{
		model:        modelName,
		created:      time.Now().Unix(),
		toolByItemID: map[string]*toolSlot{},
		toolByOutIdx: map[int]*toolSlot{},
		logged:       map[string]bool{},
	}
}

// Terminal reports whether the upstream's terminal event has been seen. Once it
// has, the stream is over: a Responses stream ends with response.completed,
// response.incomplete or response.failed rather than with a [DONE] marker.
func (s *StreamState) Terminal() bool { return s.terminal }

// UpstreamError is the failure that ended the stream, whether the upstream
// reported it (response.failed) or the answer carried output this gateway cannot
// put into a chat envelope. It is nil for a stream that succeeded.
func (s *StreamState) UpstreamError() *relaymodel.Error { return s.upstreamErr }

// Convert folds one upstream event into zero or more chat chunks. It never
// reports a parse failure to the caller: a frame the gateway cannot read is
// logged and skipped, exactly as the chat codec treats a malformed chunk, so one
// bad frame cannot truncate an otherwise healthy answer.
func (s *StreamState) Convert(eventType string, data []byte) []openai.ChatCompletionsStreamResponse {
	if s.terminal {
		// Nothing after the terminal event can improve the answer, and emitting it
		// would give the client content past the end of the response.
		return nil
	}
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		logger.SysErrorf("failed to parse upstream responses event (%s): %s", eventType, err.Error())
		return nil
	}
	// The type is carried both in the event: frame and in the payload; the
	// payload is authoritative when the two disagree or the frame is missing.
	if ev.Type != "" {
		eventType = ev.Type
	}
	s.capture(ev.Response)

	switch eventType {
	case "response.output_item.added":
		return s.onItemAdded(ev)
	case "response.output_text.delta", "response.refusal.delta":
		delta := ev.Delta
		if eventType == "response.refusal.delta" {
			delta = ev.Refusal
		}
		return s.onTextDelta(delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary.delta":
		return s.onReasoningDelta(ev.Delta)
	case "response.function_call_arguments.delta":
		return s.onArgumentsDelta(ev)
	case "response.function_call_arguments.done":
		return s.flushArguments(s.toolForEvent(ev), argumentsString(ev.Arguments))
	case "response.output_item.done":
		return s.onItemDone(ev)
	case "response.completed", "response.incomplete":
		status := "completed"
		if eventType == "response.incomplete" {
			status = "incomplete"
		}
		return s.onTerminal(ev, status)
	case "response.failed":
		return s.onFailed(ev)
	case "error":
		return s.onErrorEvent(ev)
	case "response.created", "response.in_progress", "response.queued":
		// Envelopes carrying no answer content; capture() already took the ids.
		return nil
	}

	// Events that restate content already delivered by deltas (content_part.*,
	// output_text.done, output_item.done for a message) are not drops. Anything
	// else is recorded once by name, so an upstream event this gateway does not
	// model is visible instead of silently swallowed.
	switch eventType {
	case "response.content_part.added", "response.content_part.done",
		"response.output_text.done", "response.refusal.done",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.output_text.annotation.added":
		return nil
	}
	s.noteUnmapped("event " + eventType)
	return nil
}

// capture records the identity and usage an envelope carries. A response object
// appears in the created/in_progress/terminal events alike, each carrying what
// it knows; empty fields are left as they were.
func (s *StreamState) capture(resp *Response) {
	if resp == nil {
		return
	}
	if resp.ID != "" {
		s.respID = resp.ID
	}
	if resp.Model != "" {
		s.model = resp.Model
	}
	if resp.CreatedAt > 0 {
		s.created = resp.CreatedAt
	}
	if u, ok := ToUsage(resp.Usage); ok {
		s.usage = u
	}
}

func (s *StreamState) onItemAdded(ev streamEvent) []openai.ChatCompletionsStreamResponse {
	if ev.Item == nil {
		s.noteUnmapped("output_item.added without an item")
		return nil
	}
	switch ev.Item.Type {
	case "message", "reasoning":
		// The message item opens the answer: the client gets the role before any
		// content, as it would from a chat upstream.
		return s.ensureStart()
	case "function_call":
		out := s.ensureStart()
		slot, err := s.ensureTool(ev.OutputIndex, ev.Item)
		if err != nil {
			s.fail(err)
			return nil
		}
		out = append(out, s.chunk(openai.ChatCompletionsStreamResponseChoice{
			Index: 0,
			Delta: relaymodel.Message{ToolCalls: []relaymodel.Tool{{
				Index:    slot.index(),
				Id:       slot.id,
				Type:     "function",
				Function: relaymodel.Function{Name: slot.name},
			}}},
		}))
		// An upstream may state the whole argument string on the item rather than
		// streaming it; that text has to reach the client too.
		out = append(out, s.flushArguments(slot, argumentsString(ev.Item.Arguments))...)
		return out
	}
	s.noteUnmapped("output_item " + ev.Item.Type)
	return nil
}

func (s *StreamState) onTextDelta(delta string) []openai.ChatCompletionsStreamResponse {
	if delta == "" {
		return nil
	}
	s.textSent.WriteString(delta)
	out := s.ensureStart()
	return append(out, s.chunk(openai.ChatCompletionsStreamResponseChoice{
		Index: 0,
		Delta: relaymodel.Message{Content: delta},
	}))
}

// onReasoningDelta forwards a reasoning summary as reasoning_content, the slot
// the normalized chat message has for it. A chat client that does not know the
// field ignores it; a Responses client loses it at the protocol renderer's Chunk
// framing, which has no reasoning slot (the same documented loss the Anthropic
// codec has for thinking blocks).
func (s *StreamState) onReasoningDelta(delta string) []openai.ChatCompletionsStreamResponse {
	if delta == "" {
		return nil
	}
	out := s.ensureStart()
	return append(out, s.chunk(openai.ChatCompletionsStreamResponseChoice{
		Index: 0,
		Delta: relaymodel.Message{ReasoningContent: delta},
	}))
}

func (s *StreamState) onArgumentsDelta(ev streamEvent) []openai.ChatCompletionsStreamResponse {
	slot := s.toolForEvent(ev)
	if slot == nil {
		return nil
	}
	if ev.Delta == "" {
		return nil
	}
	slot.sent.WriteString(ev.Delta)
	return []openai.ChatCompletionsStreamResponse{s.argumentsChunk(slot, ev.Delta)}
}

func (s *StreamState) onItemDone(ev streamEvent) []openai.ChatCompletionsStreamResponse {
	if ev.Item == nil {
		return nil
	}
	switch ev.Item.Type {
	case "function_call":
		return s.flushArguments(s.toolForEvent(ev), argumentsString(ev.Item.Arguments))
	case "message":
		// The item's text is normally the concatenation of the deltas already
		// sent; flushText only emits what is genuinely missing, which covers an
		// upstream that reports text on the finished item alone.
		out := s.ensureStart()
		return append(out, s.flushText(itemText(*ev.Item))...)
	case "reasoning":
		return nil
	}
	s.noteUnmapped("output_item.done for " + ev.Item.Type)
	return nil
}

// onTerminal converts the terminal event: whatever the upstream only reported in
// its final snapshot is flushed, then the finish_reason and the usage chunk are
// emitted as the chat tail.
func (s *StreamState) onTerminal(ev streamEvent, fallbackStatus string) []openai.ChatCompletionsStreamResponse {
	resp := ev.Response
	status := fallbackStatus
	if resp == nil {
		s.noteUnmapped("terminal event without a response")
	} else if resp.Status != "" {
		status = resp.Status
	}

	var out []openai.ChatCompletionsStreamResponse
	out = append(out, s.ensureStart()...)
	if resp != nil {
		// output is the answer in item order: the same order the deltas arrived
		// in, so the flush below only ever extends what the client has.
		var text strings.Builder
		for _, item := range resp.Output {
			switch item.Type {
			case "message":
				text.WriteString(itemText(item))
			case "function_call":
				slot, err := s.ensureTool(-1, &item)
				if err != nil {
					s.fail(err)
					return nil
				}
				out = append(out, s.flushArguments(slot, argumentsString(item.Arguments))...)
			}
		}
		out = append(out, s.flushText(text.String())...)
	}

	hasTools := len(s.toolSeq) > 0
	tail := &Response{Status: status, IncompleteDetails: incompleteOf(resp)}
	out = append(out, s.finishChunk(finishReasonFor(tail, hasTools)))
	if s.usage != nil {
		out = append(out, s.usageChunk())
	}
	s.terminal = true
	return out
}

func (s *StreamState) onFailed(ev streamEvent) []openai.ChatCompletionsStreamResponse {
	e := &relaymodel.Error{Message: "upstream response failed", Type: "upstream_error"}
	if ev.Response != nil && ev.Response.Error != nil {
		if ev.Response.Error.Message != "" {
			e.Message = ev.Response.Error.Message
		}
		if ev.Response.Error.Type != "" {
			e.Type = ev.Response.Error.Type
		}
		e.Code = ev.Response.Error.Code
	} else if ev.Message != "" {
		e.Message = ev.Message
		e.Code = ev.Code
	}
	s.fail(e)
	return nil
}

func (s *StreamState) onErrorEvent(ev streamEvent) []openai.ChatCompletionsStreamResponse {
	e := &relaymodel.Error{Message: "upstream stream error", Type: "server_error"}
	if ev.Error != nil {
		if ev.Error.Message != "" {
			e.Message = ev.Error.Message
		}
		if ev.Error.Type != "" {
			e.Type = ev.Error.Type
		}
		e.Code = ev.Error.Code
	} else if ev.Message != "" {
		e.Message = ev.Message
		e.Code = ev.Code
	}
	s.fail(e)
	return nil
}

// ensureStart emits the role chunk once, before the first content-bearing chunk.
func (s *StreamState) ensureStart() []openai.ChatCompletionsStreamResponse {
	if s.started {
		return nil
	}
	s.started = true
	return []openai.ChatCompletionsStreamResponse{s.chunk(openai.ChatCompletionsStreamResponseChoice{
		Index: 0,
		Delta: relaymodel.Message{Role: "assistant"},
	})}
}

// ensureTool finds or allocates the chat slot for an upstream function call.
// outIdx is the upstream's output_index, or -1 when the caller only has the item
// (the terminal snapshot carries no index).
func (s *StreamState) ensureTool(outIdx int, item *OutputItem) (*toolSlot, *relaymodel.Error) {
	if outIdx >= 0 {
		if slot, ok := s.toolByOutIdx[outIdx]; ok {
			return slot, nil
		}
	}
	if item.ID != "" {
		if slot, ok := s.toolByItemID[item.ID]; ok {
			if outIdx >= 0 {
				s.toolByOutIdx[outIdx] = slot
			}
			return slot, nil
		}
	}
	id := toolIDOf(item)
	if id == "" {
		return nil, &relaymodel.Error{
			Message: "上游 Responses 返回的 function_call 既没有 call_id 也没有 id，无法构造工具调用",
			Type:    "upstream_error",
			Code:    unmappedItemCode,
		}
	}
	if item.Name == "" {
		return nil, &relaymodel.Error{
			Message: fmt.Sprintf("上游 Responses 返回的 function_call %s 缺少函数名", id),
			Type:    "upstream_error",
			Code:    unmappedItemCode,
		}
	}
	slot := &toolSlot{slot: len(s.toolSeq), outIdx: outIdx, itemID: item.ID, id: id, name: item.Name}
	s.toolSeq = append(s.toolSeq, slot)
	if item.ID != "" {
		s.toolByItemID[item.ID] = slot
	}
	if outIdx >= 0 {
		s.toolByOutIdx[outIdx] = slot
	}
	return slot, nil
}

// toolIDOf resolves a function call's id. call_id is preferred because it is what
// a client echoes back as tool_call_id; the item id is the stand-in an upstream
// leaves when it sends no call_id, and the substitution is recorded rather than
// made silently, since it changes the id of the round trip.
func toolIDOf(item *OutputItem) string {
	if item.CallId != "" {
		return item.CallId
	}
	if item.ID != "" {
		logger.SysErrorf("responses upstream function_call %s has no call_id; using the item id", item.ID)
	}
	return item.ID
}

// toolForEvent resolves the slot an argument event belongs to. The event may
// carry either index; if the item was never announced the slot is allocated here
// so the arguments still reach the client.
func (s *StreamState) toolForEvent(ev streamEvent) *toolSlot {
	if slot, ok := s.toolByOutIdx[ev.OutputIndex]; ok {
		return slot
	}
	if ev.ItemID != "" {
		if slot, ok := s.toolByItemID[ev.ItemID]; ok {
			s.toolByOutIdx[ev.OutputIndex] = slot
			return slot
		}
	}
	if ev.Item == nil {
		// The announcing event was missed and this one names no item: the owner of
		// these arguments cannot be determined, so they are reported rather than
		// attached to whichever call happens to be open.
		s.noteUnmapped(fmt.Sprintf("arguments event for output_index %d without a known item", ev.OutputIndex))
		return nil
	}
	slot, err := s.ensureTool(ev.OutputIndex, ev.Item)
	if err != nil {
		s.fail(err)
		return nil
	}
	return slot
}

// flushArguments emits the part of an argument string the client has not been
// given yet. An upstream that streamed the arguments has nothing left to flush;
// one that stated them only on the finished item gets them here.
func (s *StreamState) flushArguments(slot *toolSlot, full string) []openai.ChatCompletionsStreamResponse {
	if slot == nil || full == "" {
		return nil
	}
	sent := slot.sent.String()
	switch {
	case full == sent:
		return nil
	case strings.HasPrefix(full, sent):
		rest := full[len(sent):]
		slot.sent.WriteString(rest)
		return []openai.ChatCompletionsStreamResponse{s.argumentsChunk(slot, rest)}
	}
	// The finished arguments disagree with what was streamed, so the client
	// already holds text the upstream has disowned. It cannot be un-sent; the
	// disagreement is recorded instead of being papered over with a duplicate.
	logger.SysErrorf("responses upstream function_call %s reported arguments that are not an extension of the streamed ones", slot.id)
	return nil
}

// flushText emits answer text the client has not been given yet, under the same
// prefix rule as flushArguments.
func (s *StreamState) flushText(full string) []openai.ChatCompletionsStreamResponse {
	if full == "" {
		return nil
	}
	sent := s.textSent.String()
	if full == sent || !strings.HasPrefix(full, sent) {
		if !strings.HasPrefix(full, sent) {
			logger.SysError("responses upstream reported text that is not an extension of the streamed text")
		}
		return nil
	}
	rest := full[len(sent):]
	s.textSent.WriteString(rest)
	return []openai.ChatCompletionsStreamResponse{s.chunk(openai.ChatCompletionsStreamResponseChoice{
		Index: 0,
		Delta: relaymodel.Message{Content: rest},
	})}
}

func (s *StreamState) argumentsChunk(slot *toolSlot, args string) openai.ChatCompletionsStreamResponse {
	return s.chunk(openai.ChatCompletionsStreamResponseChoice{
		Index: 0,
		Delta: relaymodel.Message{ToolCalls: []relaymodel.Tool{{
			Index:    slot.index(),
			Function: relaymodel.Function{Arguments: args},
		}}},
	})
}

func (s *StreamState) finishChunk(reason string) openai.ChatCompletionsStreamResponse {
	return s.chunk(openai.ChatCompletionsStreamResponseChoice{Index: 0, FinishReason: &reason})
}

func (s *StreamState) usageChunk() openai.ChatCompletionsStreamResponse {
	chunk := s.chunk()
	chunk.Choices = []openai.ChatCompletionsStreamResponseChoice{}
	usage := *s.usage
	chunk.Usage = &usage
	return chunk
}

func (s *StreamState) chunk(choices ...openai.ChatCompletionsStreamResponseChoice) openai.ChatCompletionsStreamResponse {
	return openai.ChatCompletionsStreamResponse{
		Id:      ChatIDFor(s.respID),
		Object:  "chat.completion.chunk",
		Created: s.created,
		Model:   s.model,
		Choices: choices,
	}
}

// fail records the terminal failure of the stream. The first failure wins: a
// stream that has already failed cannot be made to fail differently.
func (s *StreamState) fail(e *relaymodel.Error) {
	if s.upstreamErr == nil {
		s.upstreamErr = e
	}
	s.terminal = true
}

func (s *StreamState) noteUnmapped(what string) {
	if s.logged[what] {
		return
	}
	s.logged[what] = true
	logger.SysWarnf("responses upstream produced output this gateway cannot carry: %s", what)
}

// itemText concatenates a message item's text parts.
func itemText(item OutputItem) string {
	var b strings.Builder
	for _, part := range item.Content {
		switch part.Type {
		case "output_text", "text":
			b.WriteString(part.Text)
		case "refusal":
			b.WriteString(part.Refusal)
		}
	}
	return b.String()
}

func incompleteOf(resp *Response) *IncompleteDetails {
	if resp == nil {
		return nil
	}
	return resp.IncompleteDetails
}
