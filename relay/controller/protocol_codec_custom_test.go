package controller

import (
	"testing"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
)

// A custom (freeform) tool is downgraded to a function, not dropped: it stays
// callable, so the diagnostic it records is presentation severity and only a
// strict policy refuses it. This is the boundary between the two.
func TestOpenAIChatCodecCustomToolDowngradeRespectsThePolicy(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[{"type":"custom","name":"apply_patch","description":"Edit files."}]}`
	for _, tc := range []struct {
		policy string
		refuse bool
	}{
		{"", false},
		{"allow", false},
		{"safe", false},
		{"strict", true},
	} {
		t.Run("policy="+tc.policy, func(t *testing.T) {
			// Parsed per subtest: the codec fills fields in on the request.
			parsed, perr := protocol.ParseResponses([]byte(body))
			if perr != nil {
				t.Fatalf("parse: %+v", perr)
			}
			_, _, bizErr := openaiChatCodec{}.RequestBody(hostedMeta(tc.policy), parsed)
			if !tc.refuse {
				if bizErr != nil {
					t.Fatalf("policy %q must accept a downgraded tool: %+v", tc.policy, bizErr)
				}
				return
			}
			if bizErr == nil {
				t.Fatal("strict is the operator asking to hear about every loss")
			}
			if bizErr.Error.Code != "hosted_tool_loss" {
				t.Fatalf("error code = %q, want hosted_tool_loss", bizErr.Error.Code)
			}
		})
	}
}

// The downgraded tool is what the upstream actually receives: a function with a
// single required string argument, and no trace of the custom shape.
func TestOpenAIChatCodecSendsCustomToolAsASingleInputFunction(t *testing.T) {
	parsed, perr := protocol.ParseResponses([]byte(
		`{"model":"gpt-4o","input":"hi","tools":[{"type":"custom","name":"apply_patch","description":"Edit files."}]}`))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	body, _, bizErr := openaiChatCodec{}.RequestBody(hostedMeta(""), parsed)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %+v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("a chat upstream has no custom tool: %+v", tool)
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "apply_patch" {
		t.Fatalf("function = %+v", fn)
	}
	params, _ := fn["parameters"].(map[string]any)
	if params["additionalProperties"] != false {
		t.Fatalf("the single slot must be the only one: %+v", params)
	}
	required, _ := params["required"].([]any)
	if len(required) != 1 || required[0] != "input" {
		t.Fatalf("required = %+v, want [input]", params["required"])
	}
	properties, _ := params["properties"].(map[string]any)
	if input, _ := properties["input"].(map[string]any); input["type"] != "string" {
		t.Fatalf("properties = %+v", properties)
	}
}

// A custom tool that lost a name conflict was never sent, so a request forcing
// it is not a forced function: the choice degrades to auto and says so, rather
// than naming a tool the upstream never received.
func TestOpenAIChatCodecDroppedCustomToolDoesNotBecomeAChoice(t *testing.T) {
	parsed, perr := protocol.ParseResponses([]byte(
		`{"model":"gpt-4o","input":"hi","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}},{"type":"custom","name":"exec"}],"tool_choice":{"type":"custom","name":"exec"}}`))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	if _, ok := parsed.ToolChoice.(map[string]any); ok {
		t.Fatalf("a tool we never sent cannot be forced: %+v", parsed.ToolChoice)
	}
	body, _, bizErr := openaiChatCodec{}.RequestBody(hostedMeta(""), parsed)
	if bizErr != nil {
		t.Fatalf("RequestBody: %v", bizErr)
	}
	out := readAll(t, body)
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("only the function should have been sent: %+v", tools)
	}
	if fn, _ := tools[0].(map[string]any)["function"].(map[string]any); fn["name"] != "exec" {
		t.Fatalf("the surviving tool is %+v", tools[0])
	}
	// Two losses, and both belong: the parse dropped the conflicting custom tool,
	// and the attach had to give up forcing a tool it never sent. Asserting on
	// containment rather than equality keeps the two independently checkable.
	found := false
	for _, d := range parsed.Hosted.LossOf() {
		if d.Code == relaymodel.DiagCustomToolNameConflict {
			found = true
		}
	}
	if !found {
		t.Fatalf("the conflict must be named: %+v", parsed.Hosted.LossOf())
	}
}
