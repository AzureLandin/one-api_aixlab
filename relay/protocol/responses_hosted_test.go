package protocol

import (
	"encoding/json"
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// A hosted declaration is not dropped and not rejected: it becomes a neutral
// definition a target-side attach can act on. The knobs it carries have to
// survive the trip, because they are what the target protocol is asked to
// honour and what a diagnostic names when it cannot.
func TestParseResponses_HostedDeclarationIsExtracted(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"web_search","search_context_size":"high",
		 "user_location":{"type":"approximate","city":"Shanghai"},
		 "filters":{"allowed_domains":["example.com"]}},
		{"type":"mcp","server_label":"srv","server_url":"https://x"},
		{"type":"file_search","vector_store_ids":["vs_1"]}
	]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("hosted declarations must be tolerated, got %+v", perr)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("no hosted declaration may reach req.Tools, got %+v", req.Tools)
	}
	set := req.Hosted
	if set == nil || len(set.Definitions) != 3 {
		t.Fatalf("definitions = %+v", set)
	}
	search, mcp, native := set.Definitions[0], set.Definitions[1], set.Definitions[2]
	if search.Kind != relaymodel.HostedWebSearch || search.NativeType != "web_search" {
		t.Fatalf("search = %+v", search)
	}
	if search.Path != "tools[0]" {
		t.Fatalf("search path = %q, want the position so a diagnostic can name it", search.Path)
	}
	if search.WebSearch == nil || search.WebSearch.SearchContextSize != "high" {
		t.Fatalf("search knobs dropped: %+v", search.WebSearch)
	}
	if got := relaymodel.RawToValue(search.WebSearch.UserLocation); got == nil {
		t.Fatalf("user_location dropped")
	} else if loc, ok := got.(map[string]any); !ok || loc["city"] != "Shanghai" {
		t.Fatalf("user_location = %+v, want the FLAT neutral spelling", got)
	}
	if len(search.WebSearch.AllowedDomains) != 1 || search.WebSearch.AllowedDomains[0] != "example.com" {
		t.Fatalf("allowed_domains dropped: %+v", search.WebSearch.AllowedDomains)
	}
	if mcp.Kind != relaymodel.HostedMCP {
		t.Fatalf("mcp = %+v", mcp)
	}
	// file_search is outside the narrow mapping table, so it is `native`: it
	// travels as a declaration and every target reports it as a loss.
	if native.Kind != relaymodel.HostedNative || native.NativeType != "file_search" {
		t.Fatalf("native = %+v", native)
	}
}

// The whole type vocabulary, not just the two the mapping table names: a built-in
// tool that arrives as an unknown string must still leave a declaration behind,
// because an operator asking "why did my file_search do nothing" needs it to be
// somewhere a diagnostic can point at. Nothing may be refused, and nothing may be
// forwarded as a function tool.
func TestParseResponses_HostedTypeTable(t *testing.T) {
	for _, tc := range []struct {
		toolType string
		kind     relaymodel.HostedKind
	}{
		{"web_search", relaymodel.HostedWebSearch},
		{"web_search_preview", relaymodel.HostedWebSearch},
		{"web_search_2025_08_26", relaymodel.HostedWebSearch},
		{"mcp", relaymodel.HostedMCP},
		{"file_search", relaymodel.HostedNative},
		{"computer_use_preview", relaymodel.HostedNative},
		{"code_interpreter", relaymodel.HostedNative},
		{"image_generation", relaymodel.HostedNative},
		{"local_shell", relaymodel.HostedNative},
	} {
		t.Run(tc.toolType, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":"hi","tools":[{"type":"` + tc.toolType + `"}]}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("tool type %q must be tolerated, got %+v", tc.toolType, perr)
			}
			if len(req.Tools) != 0 {
				t.Fatalf("a hosted tool must not reach the function slot, got %+v", req.Tools)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("messages must survive, got %+v", req.Messages)
			}
			set := req.Hosted
			if set == nil || len(set.Definitions) != 1 {
				t.Fatalf("definitions = %+v", set)
			}
			if set.Definitions[0].Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", set.Definitions[0].Kind, tc.kind)
			}
			if set.Definitions[0].NativeType != tc.toolType {
				t.Fatalf("the upstream's own spelling must be kept: %q", set.Definitions[0].NativeType)
			}
		})
	}
}

// The parsed knobs must not disturb the ordinary tool path, which is the
// highest-risk regression in a change to the same switch.
func TestParseResponses_FunctionToolsBesideHostedOnes(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"web_search"},
		{"type":"function","name":"lookup","description":"d","parameters":{"type":"object"}}
	]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "lookup" {
		t.Fatalf("function tool must be kept, got %+v", req.Tools)
	}
	if set := req.Hosted; set == nil || len(set.Definitions) != 1 {
		t.Fatalf("the hosted sibling must still be recorded, got %+v", set)
	}
}

// A forced hosted choice cannot be expressed by normalized.ToolChoice, so it is
// recorded rather than lost: the attach layer either applies it or reports
// forced_tool_choice_downgraded. Leaving ToolChoice unset keeps the upstream on
// its own default instead of failing a request over a name it may not know.
func TestParseResponses_HostedToolChoiceIsRecordedAndDegradesToAuto(t *testing.T) {
	for _, tc := range []struct {
		choice string
		kind   relaymodel.HostedKind
	}{
		{`{"type":"web_search"}`, relaymodel.HostedWebSearch},
		{`{"type":"mcp"}`, relaymodel.HostedMCP},
		{`{"type":"custom"}`, relaymodel.HostedNative},
		{`{"type":"allowed_tools"}`, relaymodel.HostedNative},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":"hi","tool_choice":` + tc.choice + `}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("hosted tool_choice must be tolerated, got %+v", perr)
			}
			if req.ToolChoice != nil {
				t.Fatalf("tool_choice must be left unset so the upstream defaults to auto, got %+v", req.ToolChoice)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("messages must survive, got %+v", req.Messages)
			}
			set := req.Hosted
			if set == nil || set.ForcedChoice == nil {
				t.Fatalf("the forced choice must be recorded, got %+v", set)
			}
			if set.ForcedChoice.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", set.ForcedChoice.Kind, tc.kind)
			}
			if set.ForcedChoice.Path != "tool_choice.type" {
				t.Fatalf("path = %q", set.ForcedChoice.Path)
			}
		})
	}
}

// A replayed hosted turn is history, not noise: the client is handing back what
// the upstream did last round, and a gateway that skips it breaks the second
// turn of every conversation that used a hosted tool.
func TestParseResponses_HostedHistoryIsExtracted(t *testing.T) {
	body := `{"model":"gpt-4o","input":[
		{"type":"message","role":"user","content":"who won"},
		{"type":"web_search_call","id":"ws_1","status":"completed",
		 "action":{"type":"search","query":"final score"}},
		{"type":"mcp_call","id":"mc_1","call_id":"c_1","name":"fetch","server_label":"srv",
		 "arguments":"{\"a\":1}","output":"done"},
		{"type":"message","role":"assistant","content":"answer"}
	]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("hosted history must be tolerated, got %+v", perr)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("only the two real messages may remain, got %+v", req.Messages)
	}
	set := req.Hosted
	if set == nil || len(set.History) != 2 {
		t.Fatalf("history = %+v", set)
	}
	ws, mcp := set.History[0], set.History[1]
	if ws.Kind != relaymodel.HostedWebSearch || ws.ID != "ws_1" || ws.Status != "completed" {
		t.Fatalf("web_search_call = %+v", ws)
	}
	if q := relaymodel.RawToValue(ws.Action); q == nil {
		t.Fatalf("the search action must survive so a target can re-declare the query")
	} else if m, ok := q.(map[string]any); !ok || m["query"] != "final score" {
		t.Fatalf("action = %+v", q)
	}
	// The search ran between the user turn and the assistant turn, so it belongs
	// before message index 1 — the place a target protocol rebuilds it into.
	if ws.BeforeMessage != 1 {
		t.Fatalf("BeforeMessage = %d, want 1", ws.BeforeMessage)
	}
	if ws.Sequence != 0 || mcp.Sequence != 1 {
		t.Fatalf("arrival order must be recorded: %d %d", ws.Sequence, mcp.Sequence)
	}
	if mcp.Kind != relaymodel.HostedMCP || mcp.Name != "fetch" || mcp.ServerLabel != "srv" {
		t.Fatalf("mcp_call = %+v", mcp)
	}
	// Both artifacts ran between the user turn and the assistant turn, so both
	// belong before message index 1.
	if mcp.BeforeMessage != 1 {
		t.Fatalf("mcp BeforeMessage = %d, want 1", mcp.BeforeMessage)
	}
	// `arguments` arrives as a JSON-encoded string on this wire and is held as
	// the object both directions can read.
	if v := relaymodel.RawToValue(mcp.Arguments); v == nil {
		t.Fatalf("arguments dropped")
	} else if m, ok := v.(map[string]any); !ok || m["a"] != float64(1) {
		t.Fatalf("arguments = %+v, want the decoded object", v)
	}
}

// The item types outside the narrow table are still skipped, and skipping one
// must not disturb the messages around it.
func TestParseResponses_NonHostedItemTypesAreStillSkipped(t *testing.T) {
	for _, itemType := range []string{
		"reasoning", "item_reference", "file_search_call", "computer_call",
		"computer_call_output", "image_generation_call", "code_interpreter_call",
		"local_shell_call", "local_shell_call_output", "mcp_list_tools",
		"mcp_approval_request", "mcp_approval_response",
	} {
		t.Run(itemType, func(t *testing.T) {
			body := `{"model":"gpt-4o","input":[
				{"type":"message","role":"user","content":"hi"},
				{"type":"` + itemType + `","id":"x_1","call_id":"c_1"}
			]}`
			req, perr := ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("item type %q must be skipped, got %+v", itemType, perr)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("only the real message may remain, got %+v", req.Messages)
			}
			if set := req.Hosted; set != nil && len(set.History) != 0 {
				t.Fatalf("%q is outside the hosted table and must not become history: %+v", itemType, set.History)
			}
		})
	}
}

// Rendering puts the artifacts BEFORE the message they informed: a search is
// what the answer rests on, and the reverse order would tell the client the
// model answered first and searched afterwards.
func TestRenderResponses_HostedItemsPrecedeTheMessage(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:   relaymodel.HostedWebSearch,
		ID:     "ws_1",
		Status: relaymodel.HostedStatusCompleted,
		Action: relaymodel.HostedSearchAction("go"),
	}}}
	env := RenderResponses(up, "gpt-4o")
	output := env["output"].([]map[string]any)
	if len(output) != 2 {
		t.Fatalf("output = %+v", output)
	}
	if output[0]["type"] != "web_search_call" || output[1]["type"] != "message" {
		t.Fatalf("hosted item must come first, got %v then %v", output[0]["type"], output[1]["type"])
	}
	if output[0]["id"] != "ws_1" || output[0]["status"] != "completed" {
		t.Fatalf("item = %+v", output[0])
	}
	action, _ := output[0]["action"].(map[string]any)
	if action["query"] != "go" || action["type"] != "search" {
		t.Fatalf("action = %+v", action)
	}
}

// `arguments` and `output` are JSON-ENCODED STRINGS on this wire, not objects:
// a client that reads them as objects would see the whole call as malformed.
func TestRenderResponses_MCPCallUsesTheStringSpelling(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:        relaymodel.HostedMCP,
		ID:          "mc_1",
		Name:        "fetch",
		ServerLabel: "srv",
		Status:      relaymodel.HostedStatusCompleted,
		Arguments:   json.RawMessage(`{"a":1}`),
		Output:      relaymodel.HostedJSONString("done"),
	}}}
	env := RenderResponses(up, "gpt-4o")
	item := env["output"].([]map[string]any)[0]
	if item["type"] != "mcp_call" || item["name"] != "fetch" || item["server_label"] != "srv" {
		t.Fatalf("item = %+v", item)
	}
	if item["arguments"] != `{"a":1}` {
		t.Fatalf("arguments = %#v, want the encoded string", item["arguments"])
	}
	if item["output"] != "done" {
		t.Fatalf("output = %#v, want the decoded string", item["output"])
	}
	if _, ok := item["error"]; ok {
		t.Fatalf("a successful call must not carry an error: %+v", item)
	}
}

// A failed call reports the error INSTEAD of an output — that is how this
// protocol tells "the call returned this" from "the call did not return".
func TestRenderResponses_FailedMCPCallCarriesErrorNotOutput(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:        relaymodel.HostedMCP,
		ID:          "mc_1",
		Name:        "fetch",
		ServerLabel: "srv",
		Status:      relaymodel.HostedStatusFailed,
		Arguments:   json.RawMessage(`{}`),
		Error:       &relaymodel.Error{Message: "exploded", Code: "boom"},
	}}}
	env := RenderResponses(up, "gpt-4o")
	item := env["output"].([]map[string]any)[0]
	if _, ok := item["output"]; ok {
		t.Fatalf("a failed call must not report an output: %+v", item)
	}
	errObj, ok := item["error"].(map[string]any)
	if !ok || errObj["message"] != "exploded" || errObj["code"] != "boom" {
		t.Fatalf("error = %+v", item["error"])
	}
}

// A Responses mcp_call without a server_label cannot be re-declared to a client
// that has to name the server, so it is refused at the identity check and the
// drop is named. The response still succeeds.
func TestRenderResponses_MCPCallWithoutIdentityIsDroppedAndNamed(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:      relaymodel.HostedMCP,
		ID:        "mc_1",
		Name:      "fetch",
		Arguments: json.RawMessage(`{}`),
	}}}
	env := RenderResponses(up, "gpt-4o")
	output := env["output"].([]map[string]any)
	if len(output) != 1 || output[0]["type"] != "message" {
		t.Fatalf("the identity-less call must not be rendered: %+v", output)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagMCPIdentityMissing)
}

// Claude's encrypted search results have no field on a Responses
// web_search_call, so the omission is recorded rather than pretended away.
func TestRenderResponses_WebSearchResultsAreNamedAsOmitted(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind:    relaymodel.HostedWebSearch,
		ID:      "ws_1",
		Status:  relaymodel.HostedStatusCompleted,
		Action:  relaymodel.HostedSearchAction("go"),
		Results: json.RawMessage(`[{"type":"web_search_result","url":"https://x"}]`),
	}}}
	env := RenderResponses(up, "gpt-4o")
	item := env["output"].([]map[string]any)[0]
	if item["type"] != "web_search_call" {
		t.Fatalf("item = %+v", item)
	}
	if _, ok := item["results"]; ok {
		t.Fatalf("this protocol has no results field on the item: %+v", item)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagWebSearchResultOmitted)
}

// A kind outside the narrow outbound table is skipped by name, not silently.
func TestRenderResponses_UnrepresentableKindIsNamed(t *testing.T) {
	up := hostedTextResponse()
	up.Hosted = &relaymodel.HostedOutput{Items: []relaymodel.HostedOutputItem{{
		Kind: relaymodel.HostedNative,
		ID:   "code_1",
	}}}
	env := RenderResponses(up, "gpt-4o")
	if output := env["output"].([]map[string]any); len(output) != 1 {
		t.Fatalf("nothing but the message may be rendered: %+v", output)
	}
	assertLossCode(t, up.Hosted, relaymodel.DiagUnsupportedHostedTool)
}

// hostedTextResponse is the smallest upstream answer the renderers can be run
// against: one assistant turn with text and no hosted artifacts.
func hostedTextResponse() *openai.TextResponse {
	return &openai.TextResponse{
		Id: "chatcmpl-abc",
		Choices: []openai.TextResponseChoice{{
			Message:      relaymodel.Message{Role: "assistant", Content: "answer"},
			FinishReason: "stop",
		}},
		Usage: relaymodel.Usage{PromptTokens: 5, CompletionTokens: 7},
	}
}

// assertLossCode fails unless the render recorded exactly one loss with this
// code. The count matters: a second, unintended drop is as much a defect as a
// missing one, and it is the kind of thing a substring assertion would hide.
func assertLossCode(t *testing.T, hosted *relaymodel.HostedOutput, code string) {
	t.Helper()
	losses := hosted.LossOf()
	if len(losses) != 1 || losses[0].Code != code {
		t.Fatalf("losses = %+v, want exactly one %q", losses, code)
	}
}
