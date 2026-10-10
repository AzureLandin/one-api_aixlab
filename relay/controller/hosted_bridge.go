package controller

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	"github.com/songquanpeng/one-api/relay/adaptor/openai_responses"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Hosted-tool bridging, response direction, streaming.
//
// A hosted artifact — a web search or an MCP call the UPSTREAM ran itself — has a
// vocabulary in the Responses and Anthropic protocols and none at all in Chat
// Completions. So only the two cells that can spell one need a bridge, and each
// needs it in one direction:
//
//	messages client   <- anthropic-messages upstream   } identity cells: the
//	responses client  <- openai-responses upstream     } passthrough forwards the
//	                                                   } upstream's own bytes, so
//	                                                   } no bridge is involved
//	responses client  <- anthropic-messages upstream   claudeHostedBridge
//	messages client   <- openai-responses upstream     responsesHostedBridge
//
// The two chat-upstream cells need no bridge in either direction: a chat upstream
// cannot produce a hosted artifact, because the declaration it received was the
// web_search_options toggle and it answers with an ordinary chat stream.
//
// Each bridge is a pure observer that sits in front of the conversion, not inside
// it. It turns the upstream's own frames into neutral HostedStreamEvents and says
// whether a frame was consumed; the frames it consumes are then NOT converted,
// because a server_tool_use block run through the chat converter would leak out as
// a function call the client never asked for.

// --- Anthropic Messages upstream --------------------------------------------

// claudeHostedBridge turns an Anthropic stream's server-tool blocks into neutral
// hosted events.
//
// Claude spells one artifact as TWO blocks: a call block (server_tool_use or
// mcp_tool_use) and, later, a result block (web_search_tool_result or
// mcp_tool_result) that names it. The neutral form describes one artifact, so the
// bridge latches the call and merges the result into it.
//
// The call block is not complete until its stop event: its identity arrives at
// content_block_start, but its input arrives as input_json_delta fragments in
// between. Emitting at the start would put an artifact on the wire with an empty
// input, so the event is emitted at the stop instead — which is also the only
// point at which a Claude client's own stream would have the input.
type claudeHostedBridge struct {
	calls map[int]*claudeHostedCall
	// results holds a result block between its start and its stop. Its payload
	// arrives whole with the block, so the delay is only for framing.
	results map[int]*claudeHostedResult
	// items is the artifact each id produced, so the result block that names the
	// same id refines it instead of becoming a second artifact.
	items map[string]relaymodel.HostedOutputItem
}

type claudeHostedCall struct {
	blockType string
	id        string
	name      string
	server    string
	// blockInput is the input as it arrived whole on the start block, for an
	// upstream that sends it that way instead of as deltas. Fragments win over it.
	blockInput any
	input      strings.Builder
}

type claudeHostedResult struct {
	blockType string
	toolUseID string
	content   any
	isError   *bool
	errorCode string
}

func newClaudeHostedBridge() *claudeHostedBridge {
	return &claudeHostedBridge{
		calls:   map[int]*claudeHostedCall{},
		results: map[int]*claudeHostedResult{},
		items:   map[string]relaymodel.HostedOutputItem{},
	}
}

// Observe folds one parsed Anthropic event in. It returns the neutral event the
// frame produced, if any, and whether the frame belonged to a hosted artifact —
// consumed frames must not be converted, because the chat converter would turn a
// server tool block into a function call the client never declared.
func (b *claudeHostedBridge) Observe(parsed *anthropic.StreamResponse) (*relaymodel.HostedStreamEvent, bool) {
	if parsed == nil {
		return nil, false
	}
	switch parsed.Type {
	case "content_block_start":
		if parsed.ContentBlock == nil {
			return nil, false
		}
		switch parsed.ContentBlock.Type {
		case "server_tool_use", "mcp_tool_use":
			b.calls[parsed.Index] = &claudeHostedCall{
				blockType:  parsed.ContentBlock.Type,
				id:         parsed.ContentBlock.Id,
				name:       parsed.ContentBlock.Name,
				server:     parsed.ContentBlock.ServerName,
				blockInput: parsed.ContentBlock.Input,
			}
			return nil, true
		case "web_search_tool_result", "mcp_tool_result":
			b.results[parsed.Index] = &claudeHostedResult{
				blockType: parsed.ContentBlock.Type,
				toolUseID: parsed.ContentBlock.ToolUseId,
				content:   parsed.ContentBlock.Content,
				isError:   parsed.ContentBlock.IsError,
				errorCode: parsed.ContentBlock.ErrorCode,
			}
			return nil, true
		}
		return nil, false

	case "content_block_delta":
		call, ok := b.calls[parsed.Index]
		if !ok {
			return nil, false
		}
		if parsed.Delta != nil {
			call.input.WriteString(parsed.Delta.PartialJson)
		}
		return nil, true

	case "content_block_stop":
		if call, ok := b.calls[parsed.Index]; ok {
			delete(b.calls, parsed.Index)
			item := anthropic.HostedCallItem(call.blockType, call.id, call.name, call.server, call.inputValue())
			b.address(&item, parsed.Index)
			b.items[item.ID] = item
			return &relaymodel.HostedStreamEvent{Phase: relaymodel.HostedPhaseStart, Item: item}, true
		}
		if res, ok := b.results[parsed.Index]; ok {
			delete(b.results, parsed.Index)
			// The result refines the artifact its call opened. When no call block
			// preceded it — a stream joined mid-artifact, or the upstream sent the
			// two blocks out of order — it still becomes an artifact of its own: it
			// is a real turn the upstream reported, and dropping it because its
			// partner is missing is the silent loss this whole layer exists to avoid.
			item := relaymodel.HostedOutputItem{
				Kind:   relaymodel.HostedKindFromClaudeBlock(res.blockType, ""),
				ID:     res.toolUseID,
				CallID: res.toolUseID,
			}
			if prev, ok := b.items[item.ID]; ok {
				item = prev
			}
			anthropic.ApplyHostedResultBlock(&item, res.blockType, res.content, res.isError, res.errorCode)
			b.address(&item, parsed.Index)
			b.items[item.ID] = item
			return &relaymodel.HostedStreamEvent{Phase: relaymodel.HostedPhaseDone, Item: item}, true
		}
		return nil, false
	}
	return nil, false
}

// address gives an artifact an id when the upstream sent none. A later phase is
// matched to an earlier one by that id, so an id-less artifact is addressed by
// its content-block index rather than left unaddressable.
func (b *claudeHostedBridge) address(item *relaymodel.HostedOutputItem, index int) {
	if item.ID == "" {
		item.ID = "hosted_" + strconv.Itoa(index)
	}
	if item.CallID == "" {
		item.CallID = item.ID
	}
}

// inputValue is a call's input: the fragments that streamed between its start and
// stop, or the whole input when the upstream put it on the start block instead.
//
// Fragments that do not concatenate into JSON yield nil rather than the raw text.
// A half-arrived input is not an input, and passing the text through would put a
// string where the protocol requires an object — the renderer names the
// unrepresentable result instead, which is the honest outcome.
func (c *claudeHostedCall) inputValue() any {
	if raw := c.input.String(); raw != "" {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			return v
		}
		return nil
	}
	return c.blockInput
}

// --- OpenAI Responses upstream ----------------------------------------------

// responsesHostedBridge turns a Responses stream's hosted output items into
// neutral hosted events.
//
// This protocol already spells an artifact as ONE item, so the bridge is a name
// translation plus a phase split: the item announced by
// `response.output_item.added` is the artifact starting, the one carried by
// `response.output_item.done` is it finishing, and the per-kind progress events
// in between refine the artifact without being artifacts of their own.
type responsesHostedBridge struct {
	items map[string]relaymodel.HostedOutputItem
}

func newResponsesHostedBridge() *responsesHostedBridge {
	return &responsesHostedBridge{items: map[string]relaymodel.HostedOutputItem{}}
}

// Observe folds one raw upstream event in and returns the neutral events it
// produced, in order. A frame that is not about a hosted item produces none.
func (b *responsesHostedBridge) Observe(eventType string, data []byte) []relaymodel.HostedStreamEvent {
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		var ev struct {
			Item openai_responses.OutputItem `json:"item"`
			// output_index is the fallback address of an item the upstream never
			// gave an id: a later phase has to be matched back to this one, and an
			// item's position in the response is the only other name it has.
			OutputIndex int `json:"output_index"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		item, ok := openai_responses.HostedItemOf(ev.Item)
		if !ok {
			return nil
		}
		b.address(&item, ev.OutputIndex)
		phase := relaymodel.HostedPhaseUpdate
		switch eventType {
		case "response.output_item.added":
			phase = relaymodel.HostedPhaseStart
			// An item that has only been announced has not run yet, whatever status
			// the announcement happens to carry: reporting it as finished would put
			// a completed search on the wire before the search returned.
			if item.Status == relaymodel.HostedStatusCompleted && !startedItemIsComplete(ev.Item) {
				item.Status = relaymodel.HostedStatusInProgress
			}
		case "response.output_item.done":
			phase = relaymodel.HostedPhaseDone
		}
		b.items[item.ID] = relaymodel.MergeHostedItem(b.items[item.ID], item)
		return []relaymodel.HostedStreamEvent{{Phase: phase, Item: b.items[item.ID]}}

	case "response.web_search_call.in_progress", "response.web_search_call.searching",
		"response.web_search_call.completed", "response.mcp_call.in_progress",
		"response.mcp_call.completed", "response.mcp_call.failed":
		// A progress event describes the item the added/done pair already opened
		// and will close; it is not a second artifact. It is folded into the one
		// held so the artifact's fields are as complete as the wire allows, and
		// emitted as an update rather than as a start, so the client is never handed
		// the same item twice.
		var ev struct {
			ItemID      string          `json:"item_id"`
			OutputIndex int             `json:"output_index"`
			Action      json.RawMessage `json:"action"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		id := ev.ItemID
		if id == "" {
			id = responsesHostedFallbackID(ev.OutputIndex)
		}
		prev, ok := b.items[id]
		if !ok {
			return nil
		}
		if len(ev.Action) > 0 {
			prev.Action = ev.Action
		}
		if eventType == "response.mcp_call.failed" {
			prev.Status = relaymodel.HostedStatusFailed
		}
		b.items[id] = prev
		return []relaymodel.HostedStreamEvent{{Phase: relaymodel.HostedPhaseUpdate, Item: prev}}
	}
	return nil
}

// address gives an item an id when the upstream sent none, so its later phases
// can be matched back to it.
func (b *responsesHostedBridge) address(item *relaymodel.HostedOutputItem, outputIndex int) {
	if item.ID == "" {
		item.ID = responsesHostedFallbackID(outputIndex)
	}
	if item.CallID == "" {
		item.CallID = item.ID
	}
}

// responsesHostedFallbackID names an artifact by its position in the response.
func responsesHostedFallbackID(outputIndex int) string {
	return "hosted_" + strconv.Itoa(outputIndex)
}

// startedItemIsComplete reports whether an announced item already carries the
// payload that says it finished. A Responses upstream that sends a whole item on
// the added event — some do, and the non-streaming body is exactly that shape —
// has finished it, and rewriting its status would claim a completed search was
// still running.
func startedItemIsComplete(item openai_responses.OutputItem) bool {
	if item.Type == "web_search_call" {
		return len(item.Action) > 0
	}
	if item.Type == "mcp_call" {
		return len(item.Output) > 0 || len(item.Error) > 0
	}
	return false
}
