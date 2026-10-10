package protocol

import (
	"encoding/json"
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// A Claude tool declaration with a `type` used to be a 4xx. It is now a neutral
// definition, and the function-tool path beside it is untouched.
func TestParseMessages_HostedToolIsExtracted(t *testing.T) {
	body := []byte(`{
		"model":"claude-3-5-sonnet","max_tokens":64,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":3,
			 "allowed_domains":["example.com"],"blocked_domains":["bad.example"],
			 "user_location":{"type":"approximate","city":"Shanghai"}},
			{"name":"lookup","description":"d","input_schema":{"type":"object"}}
		]
	}`)
	req, perr := ParseMessages(body)
	if perr != nil {
		t.Fatalf("hosted declaration must be tolerated, got %+v", perr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "lookup" {
		t.Fatalf("the function tool must still be forwarded, got %+v", req.Tools)
	}
	set := req.Hosted
	if set == nil || len(set.Definitions) != 1 {
		t.Fatalf("definitions = %+v", set)
	}
	def := set.Definitions[0]
	if def.Kind != relaymodel.HostedWebSearch || def.NativeType != "web_search_20250305" {
		t.Fatalf("def = %+v", def)
	}
	if def.WebSearch == nil || def.WebSearch.MaxUses == nil || *def.WebSearch.MaxUses != 3 {
		t.Fatalf("max_uses dropped: %+v", def.WebSearch)
	}
	if len(def.WebSearch.AllowedDomains) != 1 || def.WebSearch.AllowedDomains[0] != "example.com" {
		t.Fatalf("allowed_domains dropped: %+v", def.WebSearch.AllowedDomains)
	}
	if len(def.WebSearch.BlockedDomains) != 1 || def.WebSearch.BlockedDomains[0] != "bad.example" {
		t.Fatalf("blocked_domains dropped: %+v", def.WebSearch.BlockedDomains)
	}
	if v := relaymodel.RawToValue(def.WebSearch.UserLocation); v == nil {
		t.Fatalf("user_location dropped")
	} else if loc, ok := v.(map[string]any); !ok || loc["city"] != "Shanghai" {
		t.Fatalf("user_location = %+v, want the flat neutral spelling", v)
	}
}

// A replayed hosted turn is history. The items are renumbered onto the whole
// transcript, because a per-message index says nothing to a target that is
// rebuilding the conversation.
func TestParseMessages_HostedHistoryIsExtracted(t *testing.T) {
	body := []byte(`{
		"model":"claude-3-5-sonnet","max_tokens":64,
		"messages":[
			{"role":"user","content":"who won"},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"final score"}},
				{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1",
				 "content":[{"type":"web_search_result","url":"https://x","title":"X"}]},
				{"type":"mcp_tool_use","id":"mcptoolu_1","name":"fetch","server_name":"srv","input":{"a":1}},
				{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","is_error":false,
				 "content":[{"type":"text","text":"done"}]},
				{"type":"text","text":"answer"}
			]}
		]
	}`)
	req, perr := ParseMessages(body)
	if perr != nil {
		t.Fatalf("hosted history must be tolerated, got %+v", perr)
	}
	// The ordinary blocks are unaffected: the assistant turn is still a message
	// whose text is exactly what it was before hosted blocks were understood.
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if got := messageText(t, req.Messages[1]); got != "answer" {
		t.Fatalf("assistant text = %q, want %q", got, "answer")
	}
	set := req.Hosted
	if set == nil || len(set.History) != 2 {
		t.Fatalf("history = %+v", set)
	}
	ws, mcp := set.History[0], set.History[1]
	if ws.Kind != relaymodel.HostedWebSearch || ws.ID != "srvtoolu_1" {
		t.Fatalf("web search = %+v", ws)
	}
	if ws.Sequence != 0 || mcp.Sequence != 1 {
		t.Fatalf("arrival order: %d %d", ws.Sequence, mcp.Sequence)
	}
	// The hosted turn sits inside the assistant message that follows the user
	// turn, so it belongs before message index 1.
	if ws.BeforeMessage != 1 || mcp.BeforeMessage != 1 {
		t.Fatalf("BeforeMessage = %d %d, want 1 1 (shifted onto the whole transcript)", ws.BeforeMessage, mcp.BeforeMessage)
	}
	if q := relaymodel.RawToValue(ws.Action); q == nil {
		t.Fatalf("search action dropped")
	} else if m, ok := q.(map[string]any); !ok || m["query"] != "final score" {
		t.Fatalf("action = %+v", q)
	}
	if mcp.Kind != relaymodel.HostedMCP || mcp.Name != "fetch" || mcp.ServerLabel != "srv" {
		t.Fatalf("mcp = %+v", mcp)
	}
	// The single-text-block result is folded to the neutral string on the way
	// in, which is the shape the Responses `output` field also carries.
	if got, want := string(mcp.Output), `"done"`; got != want {
		t.Fatalf("mcp output = %s, want %s", got, want)
	}
}

// Rendering to a Claude client: the call block and its result block, in the
// documented shapes.
func TestRenderMessages_MCPCallBlocksUseTheDocumentedShapes(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:        relaymodel.HostedMCP,
		ID:          "mcptoolu_1",
		Name:        "fetch",
		ServerLabel: "srv",
		Status:      relaymodel.HostedStatusCompleted,
		Arguments:   json.RawMessage(`{"a":1}`),
		Output:      relaymodel.HostedJSONString("done"),
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	content := env["content"].([]map[string]any)
	if len(content) != 3 {
		t.Fatalf("content = %+v", content)
	}
	call := content[0]
	if call["type"] != "mcp_tool_use" || call["name"] != "fetch" || call["server_name"] != "srv" {
		t.Fatalf("call = %+v", call)
	}
	if input, ok := call["input"].(map[string]any); !ok || input["a"] != float64(1) {
		t.Fatalf("input = %+v, want the object", call["input"])
	}
	result := content[1]
	if result["type"] != "mcp_tool_result" || result["tool_use_id"] != "mcptoolu_1" {
		t.Fatalf("result = %+v", result)
	}
	// Claude spells an MCP result as an array holding ONE text block. A bare
	// string here would be a shape no Claude client reads.
	blocks, ok := result["content"].([]map[string]any)
	if !ok || len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "done" {
		t.Fatalf("result content = %#v", result["content"])
	}
	if result["is_error"] != false {
		t.Fatalf("is_error = %v", result["is_error"])
	}
	if content[2]["type"] != "text" || content[2]["text"] != "answer" {
		t.Fatalf("the message text must follow the artifacts: %+v", content[2])
	}
}

// A failed call still renders, with is_error set and the error as the text —
// otherwise the result would arrive flagged as failed and empty.
func TestRenderMessages_FailedMCPCallCarriesTheErrorText(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:        relaymodel.HostedMCP,
		ID:          "mcptoolu_1",
		Name:        "fetch",
		ServerLabel: "srv",
		Status:      relaymodel.HostedStatusFailed,
		Arguments:   json.RawMessage(`{}`),
		Error:       &relaymodel.Error{Message: "exploded"},
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	content := env["content"].([]map[string]any)
	if len(content) != 3 {
		t.Fatalf("content = %+v", content)
	}
	result := content[1]
	if result["type"] != "mcp_tool_result" || result["is_error"] != true {
		t.Fatalf("result = %+v", result)
	}
	blocks := result["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["text"] != "exploded" {
		t.Fatalf("content = %#v", result["content"])
	}
}

// A web search cannot be handed to a Claude client at all: its result block is
// required to carry provider-encrypted results, which no gateway can rebuild.
// So the artifact is dropped — and named, because a silent drop here would make
// a completed search invisible to both the client and the operator.
func TestRenderMessages_WebSearchIsDroppedAndNamed(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:   relaymodel.HostedWebSearch,
		ID:     "ws_1",
		Status: relaymodel.HostedStatusCompleted,
		Action: relaymodel.HostedSearchAction("go"),
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	content := env["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("nothing but the message text may be rendered: %+v", content)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagWebSearchResponseUnrepresentable)
}

// An MCP call without a server_name cannot be re-declared to a Claude client,
// which requires one; the drop is named and the response still succeeds.
func TestRenderMessages_MCPCallWithoutIdentityIsDroppedAndNamed(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:      relaymodel.HostedMCP,
		ID:        "mcptoolu_1",
		Name:      "fetch",
		Arguments: json.RawMessage(`{}`),
		Output:    relaymodel.HostedJSONString("done"),
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	content := env["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("the identity-less call must not be rendered: %+v", content)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagMCPIdentityMissing)
}

// A kind outside the narrow outbound table is skipped by name.
func TestRenderMessages_UnrepresentableKindIsNamed(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind: relaymodel.HostedNative,
		ID:   "code_1",
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	if content := env["content"].([]map[string]any); len(content) != 1 {
		t.Fatalf("nothing but the message text may be rendered: %+v", content)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagUnsupportedHostedTool)
}

// An artifact that arrived without an id still gets one: Claude rejects a call
// block with no id, and the result names it.
func TestRenderMessages_IdlessArtifactGetsAStandIn(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:        relaymodel.HostedMCP,
		Name:        "fetch",
		ServerLabel: "srv",
		Status:      relaymodel.HostedStatusCompleted,
		Arguments:   json.RawMessage(`{}`),
		Output:      relaymodel.HostedJSONString("done"),
	}}}
	env := RenderMessages(up, "claude-3-5-sonnet")
	content := env["content"].([]map[string]any)
	if content[0]["id"] != "hosted_0" {
		t.Fatalf("call id = %v, want a position-scoped stand-in", content[0]["id"])
	}
	if content[1]["tool_use_id"] != "hosted_0" {
		t.Fatalf("the result must name the same id: %v", content[1]["tool_use_id"])
	}
}
