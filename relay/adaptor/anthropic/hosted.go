package anthropic

import (
	"encoding/json"
	"strings"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Hosted response side: lifting the artifacts of upstream-executed tools out of
// an Anthropic answer into the neutral form, so a client protocol that has a
// vocabulary for them can put them back on the wire.
//
// It runs ALONGSIDE ResponseClaude2OpenAI rather than inside it. That converter
// is shared with the legacy chat entry point, whose behaviour — hosted and
// thinking blocks never become text and never reach the chat envelope — has to
// stay exactly as it was. Nothing is removed from anywhere here: the blocks are
// copied out, and every existing reader keeps its own, unchanged view of the
// same body.

// HostedOutputOf returns the hosted artifacts of an Anthropic answer, or nil
// when it carries none.
//
// A call block and its result are merged into ONE item, keyed by the id they
// share: the neutral form describes an artifact (what was searched, what came
// back), not the two wire blocks it happens to be spelled as. A result whose
// call is missing still becomes an item of its own — it is a real turn the
// upstream reported, and dropping it because its partner is absent would be
// exactly the silent loss this package exists to avoid.
func HostedOutputOf(resp *Response) *relaymodel.HostedOutput {
	if resp == nil || len(resp.Content) == 0 {
		return nil
	}
	out := &relaymodel.HostedOutput{}
	index := map[string]int{}
	// A result block's payload lands in a kind-specific field — a search carries
	// opaque results, an MCP call a plain string — but the failure is the same
	// thing for both, so it is recorded by one piece of code. Recording it only
	// for MCP would leave a failed search saying "failed" with no reason, which
	// is the same defect as dropping it, one step quieter.
	applyResult := func(item *relaymodel.HostedOutputItem, kind relaymodel.HostedKind,
		content json.RawMessage, failed bool, code string) {
		if failed {
			item.Status = relaymodel.HostedStatusFailed
			item.Error = hostedError(code)
		} else {
			item.Status = relaymodel.HostedStatusCompleted
		}
		if kind == relaymodel.HostedMCP {
			item.Output = content
		} else {
			item.Results = content
		}
	}
	for _, block := range resp.Content {
		switch block.Type {
		case "server_tool_use", "mcp_tool_use":
			item := relaymodel.HostedOutputItem{
				Kind:        relaymodel.HostedKindFromClaudeBlock(block.Type, block.Name),
				ID:          block.Id,
				CallID:      block.Id,
				Name:        block.Name,
				ServerLabel: block.ServerName,
				// A call that no result block answered stays in_progress, which is
				// what the Responses vocabulary says about a call still running.
				Status:    relaymodel.HostedStatusInProgress,
				Arguments: rawOfValue(block.Input),
			}
			if item.Kind == relaymodel.HostedWebSearch {
				item.Action = relaymodel.HostedSearchAction(
					relaymodel.HostedSearchQueryFromClaudeInput(block.Input))
			}
			if block.Id != "" {
				index[block.Id] = len(out.Items)
			}
			out.Items = append(out.Items, item)
		case "web_search_tool_result", "mcp_tool_result":
			kind := relaymodel.HostedKindFromClaudeBlock(block.Type, "")
			content := hostedResultValue(kind, block.Content)
			failed, code := hostedResultFailure(block.Type, block.Content, block.IsError, block.ErrorCode)
			if pos, ok := index[block.ToolUseId]; ok {
				applyResult(&out.Items[pos], kind, content, failed, code)
				continue
			}
			item := relaymodel.HostedOutputItem{
				Kind:   kind,
				ID:     block.ToolUseId,
				CallID: block.ToolUseId,
			}
			applyResult(&item, kind, content, failed, code)
			if block.ToolUseId != "" {
				index[block.ToolUseId] = len(out.Items)
			}
			out.Items = append(out.Items, item)
		}
	}
	if len(out.Items) == 0 {
		return nil
	}
	return out
}

// hostedResultValue re-encodes a result block's content into the neutral form.
//
// Claude spells an MCP result the documented way — an array holding one text
// block — but the neutral form, like the Responses `output` field it also has
// to serve, holds the plain string. So the single-text-block spelling is folded
// down here, at the boundary, which is the same place and the same reason the
// user_location nesting is normalized: one shape inside, however many shapes
// the protocols spell outside. A result that is not a single text block is left
// as it is, for the renderer to report as mcp_result_unrepresentable rather than
// for this extractor to flatten into something that says less.
func hostedResultValue(kind relaymodel.HostedKind, content any) json.RawMessage {
	raw := rawOfValue(content)
	if kind != relaymodel.HostedMCP {
		return raw
	}
	// The fold itself lives in relay/model, because the request parser needs the
	// same one for a replayed turn: two implementations would be two chances to
	// disagree about what an MCP result is.
	return relaymodel.HostedMCPResultAsString(raw)
}

// hostedError turns a hosted result's failure into the neutral error, keeping
// the upstream's own code and giving the message the code when the upstream
// sent nothing else — an error object with no words is still more useful to a
// client than a bare failed status.
func hostedError(code string) *relaymodel.Error {
	if code == "" {
		return &relaymodel.Error{Message: "托管工具调用失败", Type: "upstream_error"}
	}
	return &relaymodel.Error{Message: code, Type: "upstream_error", Code: code}
}

// hostedResultFailure decides whether a hosted result block reports a failure,
// and the code it reports.
//
// Every protocol spells this differently and Claude does it two ways at once:
// mcp_tool_result carries an explicit is_error flag, while a web search that
// failed carries an error OBJECT as its whole content
// ({"type":"web_search_tool_result_error","error_code":…}) with no flag at all.
// Both are read, and an explicit flag or code always wins over the content
// sniff — a result that says it failed HAS failed, whatever its payload looks
// like.
func hostedResultFailure(blockType string, content any, explicitError *bool, explicitCode string) (bool, string) {
	code := strings.TrimSpace(explicitCode)
	if explicitError != nil && *explicitError {
		return true, code
	}
	if code != "" {
		return true, code
	}
	if !strings.HasSuffix(strings.TrimSpace(blockType), "_tool_result") {
		return false, ""
	}
	obj, ok := content.(map[string]any)
	if !ok {
		return false, ""
	}
	inner, _ := obj["type"].(string)
	innerCode, _ := obj["error_code"].(string)
	if !strings.HasSuffix(strings.TrimSpace(inner), "_error") && strings.TrimSpace(innerCode) == "" {
		return false, ""
	}
	return true, strings.TrimSpace(innerCode)
}

// rawOfValue re-encodes an already-decoded field back into raw JSON, for the
// neutral fields that hold a fragment of the upstream body. A value that cannot
// be marshalled becomes absent rather than a fabricated "null".
func rawOfValue(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
