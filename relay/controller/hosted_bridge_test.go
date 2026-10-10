package controller

import (
	"encoding/json"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// The two stream bridges. Each one is a pure observer sitting in front of the
// converter, so what is worth pinning is (a) which frames it claims — a claimed
// frame must not reach the converter, and an unclaimed one must — and (b) what a
// claimed frame produces, in the order it produces it.

// --- claudeHostedBridge -----------------------------------------------------

// observeClaude feeds one parsed event through the bridge.
func observeClaude(b *claudeHostedBridge, ev *anthropic.StreamResponse) (*relaymodel.HostedStreamEvent, bool) {
	return b.Observe(ev)
}

func claudeBlockStart(index int, block *anthropic.Content) *anthropic.StreamResponse {
	return &anthropic.StreamResponse{Type: "content_block_start", Index: index, ContentBlock: block}
}

func claudeDelta(index int, partial string) *anthropic.StreamResponse {
	return &anthropic.StreamResponse{Type: "content_block_delta", Index: index,
		Delta: &anthropic.Delta{Type: "input_json_delta", PartialJson: partial}}
}

func claudeStop(index int) *anthropic.StreamResponse {
	return &anthropic.StreamResponse{Type: "content_block_stop", Index: index}
}

// A server tool call is announced with an empty input and completed by fragments.
// The bridge claims all three frames — a claimed frame must not reach the chat
// converter, which would turn the input fragments into a function call the client
// never declared — and emits its event only at the stop, when the input is whole.
func TestClaudeHostedBridge_LatchesTheCallUntilItsInputIsComplete(t *testing.T) {
	b := newClaudeHostedBridge()

	ev, claimed := observeClaude(b, claudeBlockStart(1, &anthropic.Content{
		Type: "server_tool_use", Id: "srvtoolu_1", Name: "web_search",
	}))
	if !claimed || ev != nil {
		t.Fatalf("the call's start must be claimed and produce nothing yet: ev=%v claimed=%v", ev, claimed)
	}
	if _, claimed := observeClaude(b, claudeDelta(1, `{"query":`)); !claimed {
		t.Fatalf("an input fragment of a hosted call must be claimed")
	}
	if _, claimed := observeClaude(b, claudeDelta(1, `"final score"}`)); !claimed {
		t.Fatalf("an input fragment of a hosted call must be claimed")
	}
	ev, claimed = observeClaude(b, claudeStop(1))
	if !claimed || ev == nil {
		t.Fatalf("the call's stop must produce the artifact: ev=%v claimed=%v", ev, claimed)
	}
	if ev.Phase != relaymodel.HostedPhaseStart {
		t.Fatalf("phase = %q, want start", ev.Phase)
	}
	item := ev.Item
	if item.Kind != relaymodel.HostedWebSearch || item.ID != "srvtoolu_1" || item.Name != "web_search" {
		t.Fatalf("artifact = %+v", item)
	}
	// The fragments were assembled, not dropped: the action carries the query the
	// upstream actually searched for.
	if q := relaymodel.RawToValue(item.Action); q == nil {
		t.Fatalf("the assembled input was lost: %+v", item)
	} else if m, _ := q.(map[string]any); m["query"] != "final score" {
		t.Fatalf("action = %+v, want the query from the fragments", q)
	}
	if item.Status != relaymodel.HostedStatusInProgress {
		t.Fatalf("a call with no result yet is in progress, got %q", item.Status)
	}

	// The result block names the call, so it refines that artifact rather than
	// becoming a second one.
	if _, claimed := observeClaude(b, claudeBlockStart(2, &anthropic.Content{
		Type: "web_search_tool_result", ToolUseId: "srvtoolu_1",
		Content: []any{map[string]any{"type": "web_search_result", "url": "https://x"}},
	})); !claimed {
		t.Fatalf("the result's start must be claimed")
	}
	ev, claimed = observeClaude(b, claudeStop(2))
	if !claimed || ev == nil || ev.Phase != relaymodel.HostedPhaseDone {
		t.Fatalf("the result's stop must produce the done phase: ev=%v claimed=%v", ev, claimed)
	}
	if ev.Item.ID != "srvtoolu_1" {
		t.Fatalf("the result became a second artifact: %+v", ev.Item)
	}
	if ev.Item.Status != relaymodel.HostedStatusCompleted || len(ev.Item.Results) == 0 {
		t.Fatalf("the result was not folded into the call: %+v", ev.Item)
	}
}

// A plain text block and an ordinary tool call are NOT claimed: the converter has
// to see them, and claiming one would delete it from the client's stream.
func TestClaudeHostedBridge_LeavesOrdinaryBlocksAlone(t *testing.T) {
	b := newClaudeHostedBridge()
	for _, ev := range []*anthropic.StreamResponse{
		claudeBlockStart(0, &anthropic.Content{Type: "text"}),
		claudeBlockStart(1, &anthropic.Content{Type: "tool_use", Id: "toolu_1", Name: "get_weather"}),
		claudeDelta(1, `{"city":"sf"}`),
		claudeStop(1),
		{Type: "message_start"},
		{Type: "message_delta", Delta: &anthropic.Delta{Type: "message_delta"}},
		{Type: "message_stop"},
	} {
		if _, claimed := observeClaude(b, ev); claimed {
			t.Fatalf("the bridge claimed an ordinary %s frame", ev.Type)
		}
	}
}

// An mcp_tool_use carries the server it dials, and an mcp_tool_result its text
// outcome. A failure arrives on the result block, and it is recorded on the
// artifact rather than replacing it: the call happened either way.
func TestClaudeHostedBridge_MCPFailureKeepsTheCall(t *testing.T) {
	b := newClaudeHostedBridge()
	_, _ = observeClaude(b, claudeBlockStart(0, &anthropic.Content{
		Type: "mcp_tool_use", Id: "mcptoolu_1", Name: "fetch", ServerName: "github",
		Input: map[string]any{"repo": "acme"},
	}))
	ev, _ := observeClaude(b, claudeStop(0))
	if ev == nil || ev.Item.ServerLabel != "github" || ev.Item.Kind != relaymodel.HostedMCP {
		t.Fatalf("mcp call artifact = %+v", ev)
	}

	isErr := true
	_, _ = observeClaude(b, claudeBlockStart(1, &anthropic.Content{
		Type: "mcp_tool_result", ToolUseId: "mcptoolu_1", IsError: &isErr,
		Content: []any{map[string]any{"type": "text", "text": "rate limited"}},
	}))
	ev, claimed := observeClaude(b, claudeStop(1))
	if !claimed || ev == nil || ev.Phase != relaymodel.HostedPhaseDone {
		t.Fatalf("mcp result = %+v claimed=%v", ev, claimed)
	}
	if !ev.Item.Failed() {
		t.Fatalf("the failed call was not marked failed: %+v", ev.Item)
	}
	if ev.Item.Name != "fetch" || ev.Item.ServerLabel != "github" {
		t.Fatalf("the result replaced the call instead of refining it: %+v", ev.Item)
	}
}

// A result whose call never arrived still becomes an artifact of its own: it is a
// real turn the upstream reported, and dropping it for want of its partner is the
// silent loss this whole layer exists to avoid.
func TestClaudeHostedBridge_OrphanResultStillBecomesAnArtifact(t *testing.T) {
	b := newClaudeHostedBridge()
	_, _ = observeClaude(b, claudeBlockStart(0, &anthropic.Content{
		Type: "mcp_tool_result", ToolUseId: "mcptoolu_9",
		Content: []any{map[string]any{"type": "text", "text": "42"}},
	}))
	ev, claimed := observeClaude(b, claudeStop(0))
	if !claimed || ev == nil || ev.Phase != relaymodel.HostedPhaseDone {
		t.Fatalf("orphan result = %+v claimed=%v", ev, claimed)
	}
	if ev.Item.Kind != relaymodel.HostedMCP || ev.Item.ID != "mcptoolu_9" {
		t.Fatalf("orphan artifact = %+v", ev.Item)
	}
}

// --- responsesHostedBridge --------------------------------------------------

func responsesEvents(t *testing.T, b *responsesHostedBridge, eventType, data string) []relaymodel.HostedStreamEvent {
	t.Helper()
	return b.Observe(eventType, []byte(data))
}

// An artifact on this protocol is ONE item, so the bridge is a phase split: the
// announcement opens it, the progress events refine it, and the item's own `done`
// closes it. A client must never be handed the same artifact twice.
func TestResponsesHostedBridge_AnnounceRefineClose(t *testing.T) {
	b := newResponsesHostedBridge()

	evs := responsesEvents(t, b, "response.output_item.added", `{
		"output_index": 1,
		"item": {"type":"web_search_call","id":"ws_1","status":"in_progress"}
	}`)
	if len(evs) != 1 || evs[0].Phase != relaymodel.HostedPhaseStart {
		t.Fatalf("announcement = %+v", evs)
	}
	if evs[0].Item.Kind != relaymodel.HostedWebSearch || evs[0].Item.ID != "ws_1" {
		t.Fatalf("artifact = %+v", evs[0].Item)
	}

	// The progress events carry the query. They refine the artifact the
	// announcement opened — they are not artifacts of their own, so the client is
	// not handed a second web_search_call.
	evs = responsesEvents(t, b, "response.web_search_call.searching", `{
		"item_id":"ws_1","output_index":1,"action":{"type":"search","query":"final score"}
	}`)
	if len(evs) != 1 || evs[0].Phase != relaymodel.HostedPhaseUpdate {
		t.Fatalf("progress = %+v", evs)
	}
	if q := relaymodel.RawToValue(evs[0].Item.Action); q == nil {
		t.Fatalf("the query never reached the artifact: %+v", evs[0].Item)
	}

	evs = responsesEvents(t, b, "response.output_item.done", `{
		"output_index": 1,
		"item": {"type":"web_search_call","id":"ws_1","status":"completed"}
	}`)
	if len(evs) != 1 || evs[0].Phase != relaymodel.HostedPhaseDone {
		t.Fatalf("close = %+v", evs)
	}
	// "done" carries the status only; the query the progress event reported is
	// still on the artifact, because an update that omits a field is not a
	// withdrawal of it.
	if evs[0].Item.Status != relaymodel.HostedStatusCompleted {
		t.Fatalf("status = %q", evs[0].Item.Status)
	}
	if q := relaymodel.RawToValue(evs[0].Item.Action); q == nil {
		t.Fatalf("the query was dropped on the way out: %+v", evs[0].Item)
	}
}

// An announced item that already carries its payload has finished: some upstreams
// send a whole item on the announcement, and rewriting its status would claim a
// completed search was still running.
func TestResponsesHostedBridge_CompleteAnnouncementKeepsItsStatus(t *testing.T) {
	b := newResponsesHostedBridge()
	evs := responsesEvents(t, b, "response.output_item.added", `{
		"output_index": 0,
		"item": {"type":"web_search_call","id":"ws_2","status":"completed",
		         "action":{"type":"search","query":"q"}}
	}`)
	if len(evs) != 1 || evs[0].Item.Status != relaymodel.HostedStatusCompleted {
		t.Fatalf("a finished search was rewritten as running: %+v", evs)
	}

	// An announcement with nothing but an identity, by contrast, has not run yet.
	b2 := newResponsesHostedBridge()
	evs = responsesEvents(t, b2, "response.output_item.added", `{
		"output_index": 0,
		"item": {"type":"web_search_call","id":"ws_3","status":"completed"}
	}`)
	if len(evs) != 1 || evs[0].Item.Status != relaymodel.HostedStatusInProgress {
		t.Fatalf("an announced-only search must be in progress: %+v", evs)
	}
}

// Everything on a Responses stream that is not a hosted item must pass through
// untouched: the bridge observes, it does not consume, and a function_call or a
// message item belongs to the converter.
func TestResponsesHostedBridge_LeavesNonHostedItemsAlone(t *testing.T) {
	b := newResponsesHostedBridge()
	for _, tc := range []struct{ eventType, data string }{
		{"response.output_item.added", `{"output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`},
		{"response.output_item.done", `{"output_index":1,"item":{"type":"function_call","id":"fc_1","name":"get_weather"}}`},
		{"response.output_text.delta", `{"item_id":"msg_1","delta":"hi"}`},
		{"response.completed", `{"response":{"id":"resp_1"}}`},
		// A progress event for an item the bridge never saw open: there is nothing
		// to refine, and inventing an artifact from a progress event would put a
		// call on the wire that was never announced.
		{"response.web_search_call.completed", `{"item_id":"ws_unknown","output_index":7}`},
	} {
		if evs := responsesEvents(t, b, tc.eventType, tc.data); len(evs) != 0 {
			t.Fatalf("%s produced %+v", tc.eventType, evs)
		}
	}
}

// An MCP call carries its identity and can fail. A failure is a status on the
// artifact, not the disappearance of it.
func TestResponsesHostedBridge_MCPCallAndFailure(t *testing.T) {
	b := newResponsesHostedBridge()
	evs := responsesEvents(t, b, "response.output_item.added", `{
		"output_index": 0,
		"item": {"type":"mcp_call","id":"mcp_1","name":"fetch","server_label":"github",
		         "arguments":"{\"repo\":\"acme\"}","status":"in_progress"}
	}`)
	if len(evs) != 1 || evs[0].Item.Kind != relaymodel.HostedMCP {
		t.Fatalf("mcp artifact = %+v", evs)
	}
	if evs[0].Item.Name != "fetch" || evs[0].Item.ServerLabel != "github" {
		t.Fatalf("identity lost: %+v", evs[0].Item)
	}
	if string(evs[0].Item.Arguments) != `{"repo":"acme"}` {
		t.Fatalf("arguments = %s, want the decoded object", evs[0].Item.Arguments)
	}

	evs = responsesEvents(t, b, "response.mcp_call.failed", `{"item_id":"mcp_1","output_index":0}`)
	if len(evs) != 1 || evs[0].Item.Status != relaymodel.HostedStatusFailed {
		t.Fatalf("failure = %+v", evs)
	}
	if evs[0].Item.Name != "fetch" {
		t.Fatalf("the failure replaced the call: %+v", evs[0].Item)
	}

	evs = responsesEvents(t, b, "response.output_item.done", `{
		"output_index": 0,
		"item": {"type":"mcp_call","id":"mcp_1","name":"fetch","server_label":"github",
		         "arguments":"{\"repo\":\"acme\"}","status":"failed","error":"rate limited"}
	}`)
	if len(evs) != 1 || evs[0].Phase != relaymodel.HostedPhaseDone || !evs[0].Item.Failed() {
		t.Fatalf("done phase = %+v", evs)
	}
	if evs[0].Item.Error == nil || evs[0].Item.Error.Message == "" {
		t.Fatalf("the failure has no reason: %+v", evs[0].Item)
	}
}

// An item the upstream never gave an id is addressed by its position, so its
// later phases still find it rather than becoming a run of separate artifacts.
func TestResponsesHostedBridge_IdlessItemIsAddressedByPosition(t *testing.T) {
	b := newResponsesHostedBridge()
	evs := responsesEvents(t, b, "response.output_item.added", `{
		"output_index": 2,
		"item": {"type":"web_search_call","status":"in_progress"}
	}`)
	if len(evs) != 1 || evs[0].Item.ID == "" {
		t.Fatalf("an id-less artifact was left unaddressable: %+v", evs)
	}
	id := evs[0].Item.ID

	evs = responsesEvents(t, b, "response.web_search_call.completed", `{"output_index":2}`)
	if len(evs) != 1 || evs[0].Item.ID != id {
		t.Fatalf("the progress event did not find the artifact: %+v", evs)
	}
	evs = responsesEvents(t, b, "response.output_item.done", `{
		"output_index": 2,
		"item": {"type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}
	}`)
	if len(evs) != 1 || evs[0].Item.ID != id || evs[0].Phase != relaymodel.HostedPhaseDone {
		t.Fatalf("the close did not find the artifact: %+v", evs)
	}
}

// A Responses item whose action arrived as a JSON object rather than the string
// some gateways send must not become a malformed artifact: the bridge passes the
// raw payload through and the renderer decides what it can spell.
func TestResponsesHostedBridge_KeepsTheRawAction(t *testing.T) {
	b := newResponsesHostedBridge()
	evs := responsesEvents(t, b, "response.output_item.added", `{
		"output_index": 0,
		"item": {"type":"web_search_call","id":"ws_9","status":"completed",
		         "action":{"type":"search","query":"q"}}
	}`)
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	var action map[string]any
	if err := json.Unmarshal(evs[0].Item.Action, &action); err != nil {
		t.Fatalf("action is not JSON: %v (%s)", err, evs[0].Item.Action)
	}
	if action["query"] != "q" {
		t.Fatalf("action = %+v", action)
	}
}
