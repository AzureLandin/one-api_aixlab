package model

import (
	"strings"
	"testing"
)

func TestHostedKindTables(t *testing.T) {
	responsesTypes := map[string]HostedKind{
		"web_search":                    HostedWebSearch,
		"web_search_preview":            HostedWebSearch,
		"web_search_preview_2025_03_11": HostedWebSearch,
		"web_search_2025_08_26":         HostedWebSearch,
		"mcp":                           HostedMCP,
		"file_search":                   HostedNative,
		"code_interpreter":              HostedNative,
		"image_generation":              HostedNative,
		"computer_use_preview":          HostedNative,
		"local_shell":                   HostedNative,
		"":                              HostedNative,
	}
	for in, want := range responsesTypes {
		if got := HostedKindFromResponsesToolType(in); got != want {
			t.Fatalf("HostedKindFromResponsesToolType(%q) = %q, want %q", in, got, want)
		}
	}

	claudeTypes := map[string]HostedKind{
		"web_search_20250305":  HostedWebSearch,
		"web_search_20260210":  HostedWebSearch,
		"computer_20250124":    HostedNative,
		"bash_20250124":        HostedNative,
		"text_editor_20250728": HostedNative,
		"":                     HostedNative,
	}
	for in, want := range claudeTypes {
		if got := HostedKindFromClaudeToolType(in); got != want {
			t.Fatalf("HostedKindFromClaudeToolType(%q) = %q, want %q", in, got, want)
		}
	}

	// A server_tool_use only counts as web search when it names web_search:
	// the block type alone is shared by every server-executed tool.
	claudeBlocks := []struct {
		blockType, name string
		want            HostedKind
	}{
		{"server_tool_use", "web_search", HostedWebSearch},
		{"server_tool_use", "code_execution", HostedNative},
		{"web_search_tool_result", "", HostedWebSearch},
		{"mcp_tool_use", "", HostedMCP},
		{"mcp_tool_result", "", HostedMCP},
		{"text", "", HostedNative},
	}
	for _, c := range claudeBlocks {
		if got := HostedKindFromClaudeBlock(c.blockType, c.name); got != c.want {
			t.Fatalf("HostedKindFromClaudeBlock(%q,%q) = %q, want %q", c.blockType, c.name, got, c.want)
		}
	}
}

func TestHostedExecutionClassifiesByWhoRunsTheTool(t *testing.T) {
	for _, tt := range []struct {
		nativeType string
		want       HostedExecution
	}{
		{"computer_20250124", HostedClientExecution},
		{"bash_20250124", HostedClientExecution},
		{"text_editor_20250728", HostedClientExecution},
		{"memory_20250818", HostedClientExecution},
		{"web_search_20250305", HostedServerExecution},
		{"code_execution_20250522", HostedServerExecution},
		{"mcp", HostedServerExecution},
	} {
		if got := HostedExecutionOf(tt.nativeType); got != tt.want {
			t.Fatalf("HostedExecutionOf(%q) = %q, want %q", tt.nativeType, got, tt.want)
		}
	}
}

// The outbound tables are deliberately narrow: a kind the target protocol
// cannot express maps to "", which is the signal to route it through the loss
// policy instead of inventing a shape for it. code_execution is the documented
// case — Responses has no code_interpreter_call without a container id.
func TestHostedOutboundTablesAreNarrow(t *testing.T) {
	if got := HostedResponsesItemType(HostedWebSearch); got != "web_search_call" {
		t.Fatalf("web_search item type = %q", got)
	}
	if got := HostedResponsesItemType(HostedMCP); got != "mcp_call" {
		t.Fatalf("mcp item type = %q", got)
	}
	if got := HostedResponsesItemType(HostedNative); got != "" {
		t.Fatalf("a native kind has no Responses item type, got %q", got)
	}
	if got := HostedClaudeDeclarationType(HostedWebSearch); got != "web_search_20250305" {
		t.Fatalf("web_search declaration type = %q", got)
	}
	if got := HostedClaudeDeclarationType(HostedMCP); got != "" {
		t.Fatalf("no protocol's MCP DECLARATION is mapped, got %q", got)
	}
	if got := HostedClaudeBlockName(HostedMCP); got != "mcp" {
		t.Fatalf("mcp block name = %q", got)
	}
	if got := HostedClaudeBlockName(HostedNative); got != "" {
		t.Fatalf("a native kind has no Claude block name, got %q", got)
	}
}

func TestHostedSetEmpty(t *testing.T) {
	var nilSet *HostedSet
	if !nilSet.Empty() {
		t.Fatal("a nil set is empty")
	}
	if !(&HostedSet{}).Empty() {
		t.Fatal("a zero set is empty")
	}
	if (&HostedSet{Definitions: []HostedDefinition{{Kind: HostedWebSearch}}}).Empty() {
		t.Fatal("a set with a definition is not empty")
	}
	if (&HostedSet{History: []HostedHistoryItem{{Kind: HostedWebSearch}}}).Empty() {
		t.Fatal("a set with history is not empty")
	}
	if (&HostedSet{ForcedChoice: &HostedDefinition{Kind: HostedWebSearch}}).Empty() {
		t.Fatal("a set with only a forced choice is not empty")
	}
}

func TestValidToolLossPolicy(t *testing.T) {
	for _, ok := range []string{"", ToolLossAllow, ToolLossSafe, ToolLossStrict} {
		if !ValidToolLossPolicy(ok) {
			t.Fatalf("%q must be accepted", ok)
		}
	}
	for _, bad := range []string{"ALLOW", "deny", "warn", "safe "} {
		if ValidToolLossPolicy(bad) {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestRejectLoss(t *testing.T) {
	errDiag := NewHostedLoss(DiagUnsupportedHostedTool, "tools[0]", "chat cannot express %q", "code_interpreter")
	// A presentation-only loss: reported, but not something a client must act
	// on, so "safe" tolerates it while "strict" does not.
	warnDiag := ConversionDiagnostic{
		Code: DiagSearchContinuationLost, Path: "output[1]", Message: "continuation lost",
		Severity: "warning",
	}

	// No diagnostics is never a rejection, whatever the policy.
	for _, policy := range []string{"", ToolLossAllow, ToolLossSafe, ToolLossStrict} {
		if got := RejectLoss(policy, nil); got != nil {
			t.Fatalf("policy %q with no diagnostics must not reject, got %+v", policy, got)
		}
	}

	// allow — and the empty string that means it — never rejects: the drop is
	// the sanctioned behaviour, and the diagnostic goes to the log instead.
	for _, policy := range []string{"", ToolLossAllow} {
		if got := RejectLoss(policy, []ConversionDiagnostic{errDiag}); got != nil {
			t.Fatalf("policy %q must drop-and-continue, got %+v", policy, got)
		}
	}

	// safe rejects a semantic loss; strict rejects anything at all.
	if got := RejectLoss(ToolLossSafe, []ConversionDiagnostic{errDiag}); got == nil {
		t.Fatal("safe must reject an error-severity loss")
	} else {
		if got.StatusCode != 400 {
			t.Fatalf("expected 400, got %d", got.StatusCode)
		}
		if got.Code != "hosted_tool_loss" {
			t.Fatalf("expected a named code, got %v", got.Code)
		}
		if got.Param != "tools[0]" {
			t.Fatalf("the rejection must name the offending path, got %q", got.Param)
		}
	}
	if got := RejectLoss(ToolLossSafe, []ConversionDiagnostic{warnDiag}); got != nil {
		t.Fatalf("safe tolerates a presentation-only loss, got %+v", got)
	}
	if got := RejectLoss(ToolLossStrict, []ConversionDiagnostic{warnDiag}); got == nil {
		t.Fatal("strict rejects even a presentation-only loss")
	}

	// The body names every dropped thing, so an operator reading a 400 sees
	// what the request lost even under a policy that refuses it.
	body := RejectLoss(ToolLossStrict, []ConversionDiagnostic{errDiag, warnDiag})
	if body == nil {
		t.Fatal("strict must reject")
	}
	for _, want := range []string{DiagUnsupportedHostedTool, DiagSearchContinuationLost} {
		if !strings.Contains(body.Message, want) {
			t.Fatalf("the rejection body must name %q, got %q", want, body.Message)
		}
	}
}
