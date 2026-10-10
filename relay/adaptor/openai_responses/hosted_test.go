package openai_responses

import (
	"encoding/json"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// A Responses answer spells an artifact as ONE item already, so extraction is a
// renaming rather than a merge — and the failure this guards against is the
// item being understood by the chat path and therefore missing here.
func TestHostedOutputOfLiftsSearchAndMCPItems(t *testing.T) {
	var resp Response
	body := `{"id":"resp_1","object":"response","model":"gpt-5-mock","status":"completed","created_at":1,
		"output":[
			{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"go"}},
			{"type":"mcp_call","id":"mc_1","call_id":"c_1","name":"fetch","server_label":"srv",
			 "status":"completed","arguments":"{\"a\":1}","output":"done"},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"answer"}]}
		],
		"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	hosted := HostedOutputOf(&resp)
	if hosted == nil || len(hosted.Items) != 2 {
		t.Fatalf("hosted = %+v (the message item is not an artifact)", hosted)
	}
	search, mcp := hosted.Items[0], hosted.Items[1]
	if search.Kind != relaymodel.HostedWebSearch || search.ID != "ws_1" {
		t.Fatalf("search = %+v", search)
	}
	if got := relaymodel.RawToValue(search.Action); got == nil {
		t.Fatalf("action dropped")
	} else if m, ok := got.(map[string]any); !ok || m["query"] != "go" {
		t.Fatalf("action = %+v", got)
	}
	// A Responses web_search_call carries no results — they are reported to the
	// model inline — so nothing may be invented to fill the field.
	if search.Results != nil {
		t.Fatalf("results must stay absent: %s", search.Results)
	}
	if mcp.Kind != relaymodel.HostedMCP || mcp.Name != "fetch" || mcp.ServerLabel != "srv" || mcp.CallID != "c_1" {
		t.Fatalf("mcp = %+v", mcp)
	}
	// `arguments` arrives as a JSON-encoded string and is held as the object both
	// directions read.
	if v := relaymodel.RawToValue(mcp.Arguments); v == nil {
		t.Fatalf("arguments dropped")
	} else if m, ok := v.(map[string]any); !ok || m["a"] != float64(1) {
		t.Fatalf("arguments = %+v", v)
	}
	if s, ok := relaymodel.HostedMCPResultString(mcp.Output); !ok || s != "done" {
		t.Fatalf("output = %s", mcp.Output)
	}
}

// A failed call's error is lifted too: without it the artifact would reach a
// client as "failed" with nothing saying why.
func TestHostedOutputOfLiftsMCPError(t *testing.T) {
	var resp Response
	body := `{"id":"resp_1","object":"response","model":"gpt-5-mock","status":"completed","created_at":1,
		"output":[{"type":"mcp_call","id":"mc_1","name":"fetch","server_label":"srv","status":"failed",
		 "arguments":"{}","error":{"code":"boom","message":"exploded"}}],
		"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	hosted := HostedOutputOf(&resp)
	if hosted == nil || len(hosted.Items) != 1 {
		t.Fatalf("hosted = %+v", hosted)
	}
	item := hosted.Items[0]
	if !item.Failed() {
		t.Fatalf("status = %q, want a failure", item.Status)
	}
	if item.Error == nil || item.Error.Message != "exploded" || item.Error.Code != "boom" {
		t.Fatalf("error = %+v", item.Error)
	}
}

// An answer with nothing hosted yields no set at all, so the renderers take
// their ordinary path.
func TestHostedOutputOfIsNilWithoutArtifacts(t *testing.T) {
	var resp Response
	body := `{"id":"resp_1","object":"response","model":"gpt-5-mock","status":"completed","created_at":1,
		"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed",
		 "content":[{"type":"output_text","text":"answer"}]}],
		"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if hosted := HostedOutputOf(&resp); hosted != nil {
		t.Fatalf("hosted = %+v, want nil", hosted)
	}
}
