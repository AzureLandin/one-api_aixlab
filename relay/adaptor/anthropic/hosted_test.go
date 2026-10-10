package anthropic

import (
	"encoding/json"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func claudeResponse(t *testing.T, content string) *Response {
	t.Helper()
	var resp Response
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",
		"content":` + content + `,
		"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":7}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &resp
}

// Claude spells one artifact as two blocks sharing an id. The neutral form
// describes the artifact, so the two are merged — and the result is not lost
// merely because its partner arrived first.
func TestHostedOutputOfMergesTheCallAndItsResult(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go"}},
		{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1",
		 "content":[{"type":"web_search_result","url":"https://x","title":"X"}]},
		{"type":"text","text":"answer"}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v, want one merged artifact", hosted)
	}
	item := hosted.Items[0]
	if item.Kind != relaymodel.HostedWebSearch || item.ID != "srvtoolu_1" {
		t.Fatalf("item = %+v", item)
	}
	if item.Status != relaymodel.HostedStatusCompleted {
		t.Fatalf("status = %q, want completed once the result arrived", item.Status)
	}
	if got := relaymodel.RawToValue(item.Action); got == nil {
		t.Fatalf("the search action must be normalized out of `input`")
	} else if m, ok := got.(map[string]any); !ok || m["query"] != "go" {
		t.Fatalf("action = %+v", got)
	}
	if len(item.Results) == 0 {
		t.Fatalf("the encrypted results must be carried, opaque, rather than dropped")
	}
}

// A call with no result yet is a call still running, which is exactly what the
// status vocabulary has a word for.
func TestHostedOutputOfLeavesAnUnansweredCallInProgress(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go"}}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	if hosted.Items[0].Status != relaymodel.HostedStatusInProgress {
		t.Fatalf("status = %q", hosted.Items[0].Status)
	}
}

// A result whose call is missing is still a turn the upstream reported.
// Dropping it because its partner is absent would be the silent loss this layer
// exists to prevent.
func TestHostedOutputOfKeepsAnOrphanedResult(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"web_search_tool_result","tool_use_id":"srvtoolu_9",
		 "content":[{"type":"web_search_result","url":"https://x"}]}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	if hosted.Items[0].ID != "srvtoolu_9" || hosted.Items[0].Status != relaymodel.HostedStatusCompleted {
		t.Fatalf("item = %+v", hosted.Items[0])
	}
}

// Claude documents an MCP result as an array holding one text block. The
// neutral form holds the plain string, so the fold happens here — at the same
// boundary that flattens user_location — and the Responses `output` field it may
// be sent on to is the same string.
func TestHostedOutputOfFoldsTheMCPTextBlock(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"mcp_tool_use","id":"mcptoolu_1","name":"fetch","server_name":"srv","input":{"a":1}},
		{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","is_error":false,
		 "content":[{"type":"text","text":"done"}]}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	item := hosted.Items[0]
	if item.Name != "fetch" || item.ServerLabel != "srv" {
		t.Fatalf("identity = %+v", item)
	}
	if s, ok := relaymodel.HostedMCPResultString(item.Output); !ok || s != "done" {
		t.Fatalf("output = %s, want the folded string", item.Output)
	}
	if item.Failed() {
		t.Fatalf("is_error was false: %+v", item)
	}
}

// An MCP failure carries both the flag and the text, so a target can say what
// went wrong instead of only that something did.
func TestHostedOutputOfReadsMCPFailure(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"mcp_tool_use","id":"mcptoolu_1","name":"fetch","server_name":"srv","input":{}},
		{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","is_error":true,
		 "error_code":"tool_timeout","content":[{"type":"text","text":"timed out"}]}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	item := hosted.Items[0]
	if !item.Failed() || item.Status != relaymodel.HostedStatusFailed {
		t.Fatalf("item = %+v", item)
	}
	if item.Error == nil || item.Error.Code != "tool_timeout" {
		t.Fatalf("error = %+v", item.Error)
	}
}

// A web search that failed says so by shipping an error OBJECT as its whole
// content, with no flag anywhere. That spelling has to be read too: a search
// that errored is not a search that found nothing.
func TestHostedResultFailureReadsTheContentSniffedSpelling(t *testing.T) {
	resp := claudeResponse(t, `[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go"}},
		{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1",
		 "content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}
	]`)
	hosted := HostedOutputOf(resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	item := hosted.Items[0]
	if item.Status != relaymodel.HostedStatusFailed {
		t.Fatalf("status = %q, want failed", item.Status)
	}
	if item.Error == nil || item.Error.Code != "max_uses_exceeded" {
		t.Fatalf("error = %+v", item.Error)
	}
}

// An explicit flag wins over the content sniff: a result that says it failed HAS
// failed, whatever its payload looks like.
func TestHostedResultFailurePrefersTheExplicitFlag(t *testing.T) {
	failed, code := hostedResultFailure("mcp_tool_result",
		map[string]any{"type": "text", "text": "looks fine"}, boolPtr(false), "")
	if failed || code != "" {
		t.Fatalf("failed = %v code = %q, want a clean result", failed, code)
	}
	failed, code = hostedResultFailure("web_search_tool_result",
		map[string]any{"type": "web_search_result"}, boolPtr(true), "boom")
	if !failed || code != "boom" {
		t.Fatalf("failed = %v code = %q, want the explicit pair", failed, code)
	}
}

func boolPtr(v bool) *bool { return &v }

// An answer with nothing hosted yields no set, so the renderers take their
// ordinary path and a plain text turn is untouched.
func TestHostedOutputOfIsNilForAPlainAnswer(t *testing.T) {
	resp := claudeResponse(t, `[{"type":"text","text":"answer"}]`)
	if hosted := HostedOutputOf(resp); hosted != nil {
		t.Fatalf("hosted = %+v, want nil", hosted)
	}
}
