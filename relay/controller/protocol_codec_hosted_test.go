package controller

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// hostedMeta is the smallest channel context the codecs read: an id for the log
// line and the channel's own config, which is where the loss policy lives.
func hostedMeta(policy string) *meta.Meta {
	return &meta.Meta{ChannelId: 7, Config: model.ChannelConfig{ToolLossPolicy: policy}}
}

func readAll(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("body is not an object: %v (%s)", err, raw)
	}
	return out
}

func webSearchSet() *relaymodel.HostedSet {
	return &relaymodel.HostedSet{Definitions: []relaymodel.HostedDefinition{{
		Kind:       relaymodel.HostedWebSearch,
		Execution:  relaymodel.HostedServerExecution,
		NativeType: "web_search",
		Path:       "tools[0]",
		WebSearch: &relaymodel.HostedSearchOptions{
			SearchContextSize: "high",
			MaxUses:           ptrInt(5),
			AllowedDomains:    []string{"example.com"},
		},
	}}}
}

func ptrInt(v int) *int { return &v }

// hostedRequest builds the normalized request every codec's RequestBody takes.
func hostedRequest(set *relaymodel.HostedSet) *relaymodel.GeneralOpenAIRequest {
	return &relaymodel.GeneralOpenAIRequest{
		Model:     "m",
		MaxTokens: 64,
		Messages:  []relaymodel.Message{{Role: "user", Content: "hi"}},
		Hosted:    set,
	}
}

// A chat upstream can express exactly one thing hosted — the web_search_options
// toggle — so the declaration becomes that field, and the knobs the field has no
// room for are named rather than dropped in silence.
func TestOpenAIChatCodecAttachesHostedSearchAsTheToggle(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	body, _, bizErr := openaiChatCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	opts, ok := out["web_search_options"].(map[string]any)
	if !ok {
		t.Fatalf("web_search_options missing: %+v", out)
	}
	if opts["search_context_size"] != "high" {
		t.Fatalf("opts = %+v", opts)
	}
	// Chat has no field for either of these, so both are recorded.
	assertLossCodes(t, req.Hosted, relaymodel.DiagUnsupportedSearchLimit, relaymodel.DiagUnsupportedDomainFilter)
}

// The client's own toggle wins: it is the same rule every other field this
// gateway fills in follows, and the duplicate declaration is named.
func TestOpenAIChatCodecKeepsTheClientsOwnToggle(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	req.WebSearchOptions = &relaymodel.WebSearchOptions{SearchContextSize: "low"}
	body, _, bizErr := openaiChatCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	opts := out["web_search_options"].(map[string]any)
	if opts["search_context_size"] != "low" {
		t.Fatalf("the client's own value must survive: %+v", opts)
	}
	losses := req.Hosted.LossOf()
	if len(losses) == 0 || losses[0].Code != relaymodel.DiagDuplicateHostedWebSearch {
		t.Fatalf("the duplicate must be named: %+v", losses)
	}
}

// An MCP declaration has no chat spelling at all, so under the default policy
// the request still succeeds and the drop is named.
func TestOpenAIChatCodecDropsUnrepresentableToolsUnderAllow(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(&relaymodel.HostedSet{Definitions: []relaymodel.HostedDefinition{{
		Kind:       relaymodel.HostedMCP,
		NativeType: "mcp",
		Name:       "fetch",
		Path:       "tools[0]",
	}}})
	body, _, bizErr := openaiChatCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("allow must not refuse: %v", bizErr)
	}
	out := readAll(t, body)
	if _, ok := out["web_search_options"]; ok {
		t.Fatalf("an MCP declaration must not invent a search toggle: %+v", out)
	}
	assertLossCodes(t, req.Hosted, relaymodel.DiagUnsupportedHostedTool)
}

// The same request under `safe` fails with the named refusal — the operator's
// own choice, and the only place a loss stops a request.
func TestOpenAIChatCodecRefusesUnrepresentableToolsUnderSafe(t *testing.T) {
	for _, policy := range []string{"safe", "strict"} {
		t.Run(policy, func(t *testing.T) {
			m := hostedMeta(policy)
			req := hostedRequest(&relaymodel.HostedSet{Definitions: []relaymodel.HostedDefinition{{
				Kind:       relaymodel.HostedMCP,
				NativeType: "mcp",
				Name:       "fetch",
				Path:       "tools[0]",
			}}})
			_, _, bizErr := openaiChatCodec{}.RequestBody(m, req)
			if bizErr == nil {
				t.Fatalf("%s must refuse a tool the target cannot express", policy)
			}
			if bizErr.Error.Code != "hosted_tool_loss" {
				t.Fatalf("error code = %q, want hosted_tool_loss", bizErr.Error.Code)
			}
		})
	}
}

// A Responses upstream can express a hosted search as a declaration, so the
// same neutral set becomes a tool rather than a toggle.
func TestOpenAIResponsesCodecAttachesHostedSearchAsATool(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	body, path, bizErr := openaiResponsesCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	if path != "/v1/responses" {
		t.Fatalf("path = %q", path)
	}
	out := readAll(t, body)
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %+v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "web_search" || tool["search_context_size"] != "high" {
		t.Fatalf("tool = %+v", tool)
	}
	// Responses has room for the domain filter, so only max_uses is lost.
	assertLossCodes(t, req.Hosted, relaymodel.DiagUnsupportedSearchLimit)
}

// An Anthropic upstream spells a hosted search as a tool declaration too.
func TestAnthropicChatCodecAttachesHostedSearchAsATool(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	body, path, bizErr := anthropicChatCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	if path != "/v1/messages" {
		t.Fatalf("path = %q", path)
	}
	out := readAll(t, body)
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %+v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
		t.Fatalf("tool = %+v", tool)
	}
	if tool["max_uses"] != float64(5) {
		t.Fatalf("max_uses = %v, want 5 (Anthropic can express the limit)", tool["max_uses"])
	}
	// Claude's web_search tool has no search_context_size knob, so that one knob
	// is the only thing lost — and it is named rather than quietly dropped.
	if _, ok := tool["search_context_size"]; ok {
		t.Fatalf("Anthropic has no search_context_size to set: %+v", tool)
	}
	assertLossCodes(t, req.Hosted, relaymodel.DiagUnsupportedSearchLimit)
}

// A chat client's own toggle has no Anthropic equivalent — Claude's hosted
// search is a tool, not a switch — so it is refused by name. This is outside the
// hosted exception: it is an ordinary untranslatable field, not a declaration.
func TestAnthropicChatCodecRefusesTheChatToggleByName(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(nil)
	req.WebSearchOptions = &relaymodel.WebSearchOptions{SearchContextSize: "low"}
	_, _, bizErr := anthropicChatCodec{}.RequestBody(m, req)
	if bizErr == nil {
		t.Fatalf("the toggle has no Anthropic spelling and must be refused")
	}
	if bizErr.Error.Code != "anthropic_upstream_unsupported_field" {
		t.Fatalf("error code = %q", bizErr.Error.Code)
	}
}

// A chat client's toggle IS forwardable to a Responses upstream, as a
// web_search declaration — the gap this closes is that it used to vanish.
func TestOpenAIResponsesCodecCarriesTheChatToggle(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(nil)
	req.WebSearchOptions = &relaymodel.WebSearchOptions{SearchContextSize: "medium"}
	body, _, bizErr := openaiResponsesCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %+v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "web_search" || tool["search_context_size"] != "medium" {
		t.Fatalf("tool = %+v", tool)
	}
}

// A chat client's toggle must NOT be duplicated when the same request also
// declared the hosted tool explicitly.
func TestOpenAIResponsesCodecDoesNotDuplicateTheSearchTool(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	req.WebSearchOptions = &relaymodel.WebSearchOptions{SearchContextSize: "low"}
	body, _, bizErr := openaiResponsesCodec{}.RequestBody(m, req)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	if tools := out["tools"].([]any); len(tools) != 1 {
		t.Fatalf("the declaration must be emitted once, got %+v", tools)
	}
}

// B1 has no stream bridge for the messages<->responses cells, so a streaming
// request that declared a hosted tool carries a named diagnostic — but under the
// default policy it still streams. This is what keeps a Codex request that works
// today working.
func TestHostedStreamIsDiagnosedNotRefusedUnderAllow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codec upstreamCodec
	}{
		{"responses", openaiResponsesCodec{}},
		{"anthropic", anthropicChatCodec{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := hostedMeta("")
			req := hostedRequest(webSearchSet())
			req.Stream = true
			if _, _, bizErr := tc.codec.RequestBody(m, req); bizErr != nil {
				t.Fatalf("allow must not refuse a stream: %v", bizErr)
			}
			found := false
			for _, d := range req.Hosted.LossOf() {
				if d.Code == relaymodel.DiagHostedStreamUnsupported {
					found = true
				}
			}
			if !found {
				t.Fatalf("the unbridged stream must be named: %+v", req.Hosted.LossOf())
			}
		})
	}
}

// Under safe the same stream is refused by name.
func TestHostedStreamIsRefusedUnderSafe(t *testing.T) {
	m := hostedMeta("safe")
	req := hostedRequest(webSearchSet())
	req.Stream = true
	_, _, bizErr := openaiResponsesCodec{}.RequestBody(m, req)
	if bizErr == nil {
		t.Fatalf("safe must refuse an unbridged hosted stream")
	}
	if bizErr.Error.Code != "hosted_tool_loss" {
		t.Fatalf("error code = %q", bizErr.Error.Code)
	}
}

// A non-streaming request must NOT carry the streaming diagnostic: the bridge
// that does not exist yet is only missing on the stream path.
func TestHostedNonStreamIsNotDiagnosedAsUnbridged(t *testing.T) {
	m := hostedMeta("")
	req := hostedRequest(webSearchSet())
	for _, tc := range []upstreamCodec{openaiResponsesCodec{}, anthropicChatCodec{}} {
		if _, _, bizErr := tc.RequestBody(m, req); bizErr != nil {
			t.Fatalf("RequestBody: %v", bizErr)
		}
	}
	for _, d := range req.Hosted.LossOf() {
		if d.Code == relaymodel.DiagHostedStreamUnsupported {
			t.Fatalf("a non-streaming request must not be told its stream is unbridged: %+v", req.Hosted.LossOf())
		}
	}
}

// assertLossCodes requires exactly these codes, in this order.
func assertLossCodes(t *testing.T, set *relaymodel.HostedSet, want ...string) {
	t.Helper()
	got := set.LossOf()
	if len(got) != len(want) {
		t.Fatalf("losses = %+v, want %v", got, want)
	}
	for i := range want {
		if got[i].Code != want[i] {
			t.Fatalf("loss[%d] = %q, want %q (all: %+v)", i, got[i].Code, want[i], got)
		}
	}
}
