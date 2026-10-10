package protocol

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
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

	textBlock int // index of the text content block, -1 if none opened
	nextIndex int // next assignable content-block index
	closed    bool

	toolBlock map[int]int // OpenAI tool index -> content-block index (-1 until opened)
	toolID    map[int]string
	toolName  map[int]string
	toolArgs  map[int]*strings.Builder
	toolOpen  map[int]bool
	toolSeen  []int // tool indices in the order the upstream introduced them

	// hosted is the artifact each hosted item id currently stands for, kept so a
	// later phase can refine it; hostedCall/hostedResult record which of its two
	// blocks has already been written, so a re-rendered artifact never repeats one.
	hosted       map[string]relaymodel.HostedOutputItem
	hostedCall   map[string]bool
	hostedResult map[string]bool
	losses       relaymodel.HostedLossLog
}

// NewMessagesStream writes the message_start event with an empty content array.
func NewMessagesStream(w *sse.Writer, id, model string, usage *relaymodel.Usage) *MessagesStream {
	m := &MessagesStream{
		w:            w,
		id:           id,
		model:        model,
		textBlock:    -1,
		toolBlock:    map[int]int{},
		toolID:       map[int]string{},
		toolName:     map[int]string{},
		toolArgs:     map[int]*strings.Builder{},
		toolOpen:     map[int]bool{},
		hosted:       map[string]relaymodel.HostedOutputItem{},
		hostedCall:   map[string]bool{},
		hostedResult: map[string]bool{},
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

// openToolBlock announces the tool_use block for one OpenAI tool index. The
// index is claimed here rather than when the tool was first seen, so a text
// block that starts in between still gets the lower content-block index, which
// is what Anthropic guarantees (blocks are numbered in announcement order).
func (m *MessagesStream) openToolBlock(idx int) error {
	id := m.toolID[idx]
	if id == "" {
		// Reached only from Finish: the upstream named the call but never
		// identified it, and a tool_use block without an id is invalid. The id is
		// synthesized rather than dropping a call the model actually made.
		id = "toolu_" + uuid.New().String()
		m.toolID[idx] = id
	}
	blockIdx := m.nextIndex
	m.nextIndex++
	m.toolBlock[idx] = blockIdx
	m.toolOpen[idx] = true
	return m.w.Event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": blockIdx,
		"content_block": map[string]any{
			"type": "tool_use", "id": id, "name": m.toolName[idx], "input": map[string]any{},
		},
	})
}

func (m *MessagesStream) emitToolArgs(idx int, args string) error {
	return m.w.Event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": m.toolBlock[idx],
		"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
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
		if _, seen := m.toolArgs[td.Index]; !seen {
			m.toolArgs[td.Index] = &strings.Builder{}
			m.toolBlock[td.Index] = -1
			m.toolSeen = append(m.toolSeen, td.Index)
		}
		// The id, the name and the arguments may each arrive first and be spread
		// over several fragments, so every fragment is folded in as it arrives. An
		// id or a name that changes mid-call means the upstream reused one index
		// for two different calls, which cannot be rendered as one tool_use block.
		if td.ID != "" {
			if prev := m.toolID[td.Index]; prev != "" && prev != td.ID {
				return fmt.Errorf("上游把工具索引 %d 同时用于 %q 和 %q", td.Index, prev, td.ID)
			}
			m.toolID[td.Index] = td.ID
		}
		if td.Name != "" {
			if prev := m.toolName[td.Index]; prev != "" && prev != td.Name {
				return fmt.Errorf("上游把工具索引 %d 从 %q 改名为 %q", td.Index, prev, td.Name)
			}
			m.toolName[td.Index] = td.Name
		}
		if td.Arguments != "" {
			m.toolArgs[td.Index].WriteString(td.Arguments)
		}
		if m.toolOpen[td.Index] {
			if td.Arguments != "" {
				if err := m.emitToolArgs(td.Index, td.Arguments); err != nil {
					return err
				}
			}
			continue
		}
		// A tool_use block is announced only once the call is identifiable. Waiting
		// for the id as well as the name matters: the client echoes the id back as
		// the next turn's tool_use_id, so a block closed with a synthesized id sends
		// the upstream a tool_call_id it never issued. Fragments that arrived early
		// stay buffered until the block can open.
		if m.toolName[td.Index] == "" || m.toolID[td.Index] == "" {
			continue
		}
		if err := m.openToolBlock(td.Index); err != nil {
			return err
		}
		// Everything buffered so far is flushed as one delta, so no fragment is
		// emitted twice.
		if b := m.toolArgs[td.Index]; b.Len() > 0 {
			if err := m.emitToolArgs(td.Index, b.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitHostedBlock writes one hosted content block whole: Claude frames a block
// as start/delta*/stop, and a hosted block's content is known in its entirety
// when it is announced — a server tool's input arrives at once, a result arrives
// as a payload — so the block is opened and stopped together instead of being
// left open across the text that follows.
//
// A text block still being written is closed first, because Claude never has two
// blocks open at once: a client is entitled to assume the blocks arrive in
// sequence, and a block started before its predecessor stopped would be the first
// time such a client ever saw otherwise. The text that follows opens a NEW block,
// which is what the model's own turn looked like — what it wrote before the
// search, the search, then what it wrote after it.
//
// Because every hosted block is closed as it is written, Finish has no dangling
// hosted block to close — the terminal rule ("never leave an item half-open") is
// satisfied here by construction rather than by a final sweep.
func (m *MessagesStream) emitHostedBlock(block map[string]any) error {
	if m.textBlock >= 0 {
		if err := m.w.Event("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": m.textBlock,
		}); err != nil {
			return err
		}
		m.textBlock = -1
	}
	blockIdx := m.nextIndex
	m.nextIndex++
	if err := m.w.Event("content_block_start", map[string]any{
		"type": "content_block_start", "index": blockIdx, "content_block": block,
	}); err != nil {
		return err
	}
	return m.w.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIdx})
}

// Hosted renders one hosted-tool artifact transition as Claude content blocks.
//
// The artifact is spelled as the two blocks Claude describes a server tool with:
// the call block, which is written as soon as the artifact is known, and the
// result block, which is written when the call returns. Text that arrived between
// them keeps its place, so the block order on the wire is the order the upstream
// reported — not the tidier call-then-result adjacency a body would have.
//
// A kind Claude cannot express is recorded and skipped rather than invented,
// exactly as in the non-streaming renderer. This is the one place the gateway
// drops something by design; it is never silent, and it is never a refusal — the
// response is already streaming and the upstream has already done the work.
func (m *MessagesStream) Hosted(ev relaymodel.HostedStreamEvent) error {
	item := ev.Item
	if item.ID == "" {
		// Defensive: both stream bridges address an artifact by an id the upstream
		// gave it, because a later phase has to be matched back to an earlier one.
		item.ID = m.id + "_hosted_" + itoa(len(m.hosted))
	}
	losses := lossSink(&m.losses)
	prev, seen := m.hosted[item.ID]
	if seen {
		item = relaymodel.MergeHostedItem(prev, item)
		// What the client's protocol cannot express was named when the artifact was
		// first seen; re-rendering it must not name it once per event.
		losses = discardLosses{}
	}
	// Only read on the first sight, which is the only sight that can record a loss.
	path := pathOfHosted(len(m.hosted))
	m.hosted[item.ID] = item

	artifact := messagesHostedArtifactOf(item, path, item.ID, losses)
	// The call block opens the artifact; a done phase written by a stream that
	// joined mid-artifact writes it here first, so a result never names a block
	// the client never saw.
	if artifact.call != nil && !m.hostedCall[item.ID] {
		if err := m.emitHostedBlock(artifact.call); err != nil {
			return err
		}
		m.hostedCall[item.ID] = true
	}
	if artifact.result != nil && !m.hostedResult[item.ID] {
		if err := m.emitHostedBlock(artifact.result); err != nil {
			return err
		}
		m.hostedResult[item.ID] = true
	}
	return nil
}

// LossOf reports what Claude could not be told about. The stream phase never
// rejects under any policy, so this is the only place the loss can surface.
func (m *MessagesStream) LossOf() []relaymodel.ConversionDiagnostic { return m.losses.LossOf() }

// Finish closes open blocks and writes message_delta + message_stop.
// finishReason is the OpenAI finish_reason, mapped to an Anthropic stop_reason
// here (inverse of the anthropic adaptor's mapping). The usage is the SAME
// normalized value the recorder persists.
func (m *MessagesStream) Finish(finishReason string, usage *relaymodel.Usage) error {
	if m.closed {
		return nil
	}
	m.closed = true
	// A call the upstream named but never identified still reaches the client:
	// dropping it would silently delete a tool call the model made. It is
	// announced here, with a synthesized id — the only option left once the
	// stream is over — so the turn's tool_use blocks match what the model asked.
	for _, idx := range m.toolSeen {
		if m.toolOpen[idx] || m.toolName[idx] == "" {
			continue
		}
		if err := m.openToolBlock(idx); err != nil {
			return err
		}
		if b := m.toolArgs[idx]; b.Len() > 0 {
			if err := m.emitToolArgs(idx, b.String()); err != nil {
				return err
			}
		}
	}
	// Anthropic requires a tool_use block that was never announced to be dropped
	// rather than half-opened, so only the blocks that actually started are closed.
	blocks := make([]int, 0, len(m.toolSeen)+1)
	if m.textBlock >= 0 {
		blocks = append(blocks, m.textBlock)
	}
	opened := 0
	for _, idx := range m.toolSeen {
		if m.toolOpen[idx] {
			opened++
			blocks = append(blocks, m.toolBlock[idx])
		}
	}
	sort.Ints(blocks)
	for _, blockIdx := range blocks {
		_ = m.w.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIdx})
	}
	// Anthropic's stop_reason is never null on a completed message. A turn that
	// emitted tool blocks stopped for tool use even if the upstream's
	// finish_reason was empty or plain "stop".
	stopReason := "end_turn"
	if mapped := finishReasonToStopReason(finishReason); mapped != nil {
		stopReason = *mapped
	}
	if opened > 0 && stopReason == "end_turn" {
		stopReason = "tool_use"
	}
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
