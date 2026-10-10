package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// The hosted stream renderers. Two things are worth pinning here and are not
// visible in the codec tests: the ORDER the artifacts take among the text, and
// what a stream does with an artifact the upstream never finished. Both are
// properties of the wire, so both are asserted on whole frames rather than on a
// substring of the captured output.

// frames parses a captured SSE stream into the data payload of every frame, in
// order. Asserting on frames rather than on substrings is what lets a test state
// an order — the thing both renderers have to get right and the thing a
// "contains" check cannot see.
func frames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var parsed []map[string]any
	for _, block := range strings.Split(out, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var data []string
		for _, line := range strings.Split(block, "\n") {
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				data = append(data, payload)
			}
		}
		if len(data) == 0 {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &frame); err != nil {
			t.Fatalf("frame is not JSON: %v in %q", err, block)
		}
		parsed = append(parsed, frame)
	}
	return parsed
}

// frameTypes lists the frame types in order — the sequence a client sees.
func frameTypes(fs []map[string]any) []string {
	types := make([]string, 0, len(fs))
	for _, f := range fs {
		if s, ok := f["type"].(string); ok {
			types = append(types, s)
		}
	}
	return types
}

// findFrame returns the first frame of the given type whose item_id matches, or
// nil. Used to address one artifact's own events in a stream that carries more.
func findFrame(fs []map[string]any, frameType, itemID string) map[string]any {
	for _, f := range fs {
		if f["type"] != frameType {
			continue
		}
		if itemID == "" || f["item_id"] == itemID {
			return f
		}
	}
	return nil
}

// countFrames counts the frames of a type that name one item.
func countFrames(fs []map[string]any, frameType, itemID string) int {
	n := 0
	for _, f := range fs {
		if f["type"] == frameType && f["item_id"] == itemID {
			n++
		}
	}
	return n
}

func searchArtifact(id, status string) relaymodel.HostedOutputItem {
	return relaymodel.HostedOutputItem{
		Kind:   relaymodel.HostedWebSearch,
		ID:     id,
		CallID: id,
		Status: status,
		Action: relaymodel.HostedSearchAction("final score"),
	}
}

// mcpArtifact builds an MCP call. Only a call that has finished carries an
// output: an in-flight one has no result yet, and that is exactly what makes it
// in flight — a fixture that gave it one would be asserting against a shape the
// stream can never produce.
func mcpArtifact(id, status string) relaymodel.HostedOutputItem {
	item := relaymodel.HostedOutputItem{
		Kind:        relaymodel.HostedMCP,
		ID:          id,
		CallID:      id,
		Name:        "fetch",
		ServerLabel: "github",
		Status:      status,
		Arguments:   json.RawMessage(`{"repo":"acme"}`),
	}
	if status != relaymodel.HostedStatusInProgress {
		item.Output = relaymodel.HostedJSONString("sunny")
	}
	return item
}

// --- Responses --------------------------------------------------------------

// A hosted artifact is an output item on this protocol, so it takes a place in
// the SAME numbering as the message and function_call items — which is what makes
// its output_index dense and its position the arrival order. The terminal
// response.completed repeats it, because that is where a Responses client reads
// the finished output from.
func TestResponsesStream_HostedItemTakesItsPlaceInOutputOrder(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o")

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"before "}}]}`))
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseStart,
		Item:  searchArtifact("ws_1", relaymodel.HostedStatusInProgress),
	})
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"after"}}]}`))
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseDone,
		Item:  searchArtifact("ws_1", relaymodel.HostedStatusCompleted),
	})
	_ = s.Finish("stop", &relaymodel.Usage{PromptTokens: 3, CompletionTokens: 4})

	fs := frames(t, c.buf.String())

	added := findFrame(fs, "response.output_item.added", "ws_1")
	if added == nil {
		t.Fatalf("the artifact was never announced:\n%s", c.buf.String())
	}
	// The message item opened first, so the artifact is the second output item.
	if added["output_index"] != float64(1) {
		t.Fatalf("output_index = %v, want 1 (dense, after the message item)", added["output_index"])
	}
	if item, _ := added["item"].(map[string]any); item["status"] != relaymodel.HostedStatusInProgress {
		t.Fatalf("an announced artifact must not claim to have finished: %+v", item)
	}
	done := findFrame(fs, "response.output_item.done", "ws_1")
	if done == nil || done["output_index"] != float64(1) {
		t.Fatalf("done frame = %+v, want output_index 1", done)
	}
	if item, _ := done["item"].(map[string]any); item["status"] != relaymodel.HostedStatusCompleted {
		t.Fatalf("the finished artifact must report completed: %+v", item)
	}
	// One artifact, one announcement: a later phase refines the item the client
	// already holds rather than handing it a second one.
	if n := countFrames(fs, "response.output_item.added", "ws_1"); n != 1 {
		t.Fatalf("%d added frames for one artifact", n)
	}

	completed := findFrame(fs, "response.completed", "")
	if completed == nil {
		t.Fatalf("no terminal frame:\n%s", c.buf.String())
	}
	resp, _ := completed["response"].(map[string]any)
	output, _ := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("terminal output = %+v, want the message and the search", output)
	}
	first, _ := output[0].(map[string]any)
	second, _ := output[1].(map[string]any)
	if first["type"] != "message" || second["id"] != "ws_1" {
		t.Fatalf("terminal output order wrong: %+v", output)
	}
	// A completed search completed, whatever the turn's own status: the artifact
	// keeps its own outcome rather than inheriting the response's.
	if second["status"] != relaymodel.HostedStatusCompleted {
		t.Fatalf("the search's own status was overwritten: %+v", second)
	}
	// The frame order is the arrival order: the artifact was reported after the
	// first text and before the second.
	types := frameTypes(fs)
	addedAt, secondTextAt := indexOf(types, "response.output_item.added", 1), indexOf(types, "response.output_text.delta", 1)
	if !(addedAt > 0 && addedAt < secondTextAt) {
		t.Fatalf("artifact announced at %d, second text delta at %d — order not preserved:\n%v", addedAt, secondTextAt, types)
	}
}

// An artifact the upstream opened and never closed is closed at the end of the
// stream as incomplete. It is never left dangling in response.completed, and it is
// never fabricated a result it did not have.
func TestResponsesStream_UnclosedHostedItemIsClosedIncomplete(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_2", "gpt-4o")
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseStart,
		Item:  searchArtifact("ws_2", relaymodel.HostedStatusInProgress),
	})
	_ = s.Finish("length", nil)

	fs := frames(t, c.buf.String())
	done := findFrame(fs, "response.output_item.done", "ws_2")
	if done == nil {
		t.Fatalf("the unfinished artifact was left dangling:\n%s", c.buf.String())
	}
	item, _ := done["item"].(map[string]any)
	if item["status"] != relaymodel.HostedStatusIncomplete {
		t.Fatalf("status = %v, want incomplete", item["status"])
	}
	if _, invented := item["output"]; invented {
		t.Fatalf("a result was invented for a search that never returned: %+v", item)
	}
	completed := findFrame(fs, "response.completed", "")
	resp, _ := completed["response"].(map[string]any)
	output, _ := resp["output"].([]any)
	last, _ := output[len(output)-1].(map[string]any)
	if last["status"] != relaymodel.HostedStatusIncomplete {
		t.Fatalf("terminal output still claims the search is running: %+v", output)
	}
}

// indexOf returns the position of the n-th frame of a type, or -1.
func indexOf(types []string, frameType string, n int) int {
	seen := 0
	for i, ty := range types {
		if ty == frameType {
			seen++
			if seen == n {
				return i
			}
		}
	}
	return -1
}

// --- Messages ---------------------------------------------------------------

// Claude numbers content blocks in announcement order, so the artifact's blocks
// take the indices they were announced at and the text around them keeps its own:
// the wire order is the order the upstream reported. Blocks are strictly
// sequential — a hosted block is written whole, and the text block still open when
// it arrives is closed first and a new one opened after, which is exactly the
// shape the model's own turn had (what it wrote, the search, what it wrote next).
func TestMessagesStream_HostedBlocksKeepArrivalOrderAndDenseIndices(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_1", "claude", nil)

	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"answer: "}}]}`))
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseStart,
		Item:  mcpArtifact("mcp_1", relaymodel.HostedStatusInProgress),
	})
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"content":"42"}}]}`))
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseDone,
		Item:  mcpArtifact("mcp_1", relaymodel.HostedStatusCompleted),
	})
	_ = s.Finish("stop", &relaymodel.Usage{PromptTokens: 3, CompletionTokens: 4})

	fs := frames(t, c.buf.String())

	var starts []map[string]any
	for _, f := range fs {
		if f["type"] == "content_block_start" {
			starts = append(starts, f)
		}
	}
	if len(starts) != 4 {
		t.Fatalf("content_block_start frames = %d, want text + call + text + result:\n%s", len(starts), c.buf.String())
	}
	// Block 0: the text before the call. Block 1: the call. Block 2: the text
	// after it, in its own block because block 0 was closed for the call. Block 3:
	// the result. Dense, and in the order the upstream reported them.
	if starts[0]["index"] != float64(0) {
		t.Fatalf("text block index = %v, want 0", starts[0]["index"])
	}
	call, _ := starts[1]["content_block"].(map[string]any)
	if starts[1]["index"] != float64(1) || call["type"] != "mcp_tool_use" {
		t.Fatalf("call block = %+v at %v", call, starts[1]["index"])
	}
	if call["server_name"] != "github" || call["name"] != "fetch" {
		t.Fatalf("call block lost its identity: %+v", call)
	}
	if block, _ := starts[2]["content_block"].(map[string]any); starts[2]["index"] != float64(2) || block["type"] != "text" {
		t.Fatalf("the text after the call did not get its own block: %+v", starts[2])
	}
	result, _ := starts[3]["content_block"].(map[string]any)
	if starts[3]["index"] != float64(3) || result["type"] != "mcp_tool_result" {
		t.Fatalf("result block = %+v at %v", result, starts[3]["index"])
	}
	if result["tool_use_id"] != "mcp_1" || result["is_error"] != false {
		t.Fatalf("result block does not name its call: %+v", result)
	}

	// The exact wire shape: strictly sequential, with each hosted block opened and
	// stopped back to back, because that is what a Claude client is entitled to
	// assume and what the real API always produces.
	want := []string{
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop",
	}
	var got []string
	for _, ty := range frameTypes(fs) {
		if strings.HasPrefix(ty, "content_block_") {
			got = append(got, ty)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("block frames:\n got %v\nwant %v\n\n%s", got, want, c.buf.String())
	}
	// Every block is closed, so a client's accumulator never holds a half-open
	// one, and every text delta lands on a text block.
	for _, f := range fs {
		if f["type"] != "content_block_delta" {
			continue
		}
		delta, _ := f["delta"].(map[string]any)
		if delta["type"] != "text_delta" {
			t.Fatalf("unexpected delta in a text stream: %+v", f)
		}
		if idx := f["index"].(float64); idx != 0 && idx != 2 {
			t.Fatalf("text delta landed on block %v, want 0 or 2", idx)
		}
	}
}

// A search artifact is the one thing Claude cannot be handed: its result block
// must carry provider-encrypted results the gateway cannot rebuild, and a call
// block without one is not a shape the API accepts. The loss is recorded once, at
// the phase the artifact was first seen — not once per event, which is what a
// naive re-render would do.
func TestMessagesStream_UnrepresentableArtifactIsNamedOncePerStream(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_2", "claude", nil)
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseStart,
		Item:  searchArtifact("ws_1", relaymodel.HostedStatusInProgress),
	})
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseDone,
		Item:  searchArtifact("ws_1", relaymodel.HostedStatusCompleted),
	})
	_ = s.Finish("stop", nil)

	if strings.Contains(c.buf.String(), "server_tool_use") || strings.Contains(c.buf.String(), "web_search_tool_result") {
		t.Fatalf("an unrepresentable artifact was rendered anyway:\n%s", c.buf.String())
	}
	losses := s.LossOf()
	if len(losses) != 1 || losses[0].Code != relaymodel.DiagWebSearchResponseUnrepresentable {
		t.Fatalf("losses = %+v, want exactly one %s", losses, relaymodel.DiagWebSearchResponseUnrepresentable)
	}
}

// An artifact still running when the stream ends leaves its call block on the
// wire and no result block: the client is told a call is in flight rather than
// being handed a fabricated outcome. Nothing is left open — the call block was
// closed as it was written.
func TestMessagesStream_UnfinishedArtifactHasNoResultBlock(t *testing.T) {
	c := &capture{}
	s := NewMessagesStream(sse.NewWriter(c), "msg_3", "claude", nil)
	_ = s.Hosted(relaymodel.HostedStreamEvent{
		Phase: relaymodel.HostedPhaseStart,
		Item:  mcpArtifact("mcp_2", relaymodel.HostedStatusInProgress),
	})
	_ = s.Finish("stop", nil)

	out := c.buf.String()
	if !strings.Contains(out, `"type":"mcp_tool_use"`) {
		t.Fatalf("the in-flight call was dropped:\n%s", out)
	}
	if strings.Contains(out, `"type":"mcp_tool_result"`) {
		t.Fatalf("a result was invented for a call that never returned:\n%s", out)
	}
	types := frameTypes(frames(t, out))
	if opens, closes := countType(types, "content_block_start"), countType(types, "content_block_stop"); opens != closes {
		t.Fatalf("unbalanced blocks: %d starts, %d stops:\n%v", opens, closes, types)
	}
}

func countType(types []string, t string) int {
	n := 0
	for _, ty := range types {
		if ty == t {
			n++
		}
	}
	return n
}
