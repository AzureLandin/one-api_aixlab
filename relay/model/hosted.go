package model

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Hosted tools (server-side / built-in tools) in their neutral form.
//
// A hosted tool is one the UPSTREAM executes: the client declares it, the
// upstream performs the work and hands back an artifact (a search result, an
// MCP call) rather than a tool call for the client to answer. Every protocol
// spells this differently — Claude has `web_search_20250305` plus
// `server_tool_use` / `web_search_tool_result` blocks, Responses has
// `{"type":"web_search"}` plus `web_search_call` items, Chat Completions has
// only the `web_search_options` toggle and no artifact vocabulary at all.
//
// This file is the neutral vocabulary those three are translated through. It
// deliberately mirrors new-api's narrow table: only web search and MCP are
// named, and everything else is HostedNative, which means "carried far enough
// to be named in a diagnostic, never mapped". Mapping a kind the reference
// implementation itself refuses to map (its `code_execution` outbound is the
// empty string, because Responses cannot express a `code_interpreter_call`
// without a container id) would be inventing behaviour, not porting it.
//
// Nothing here depends on an adaptor package: the kind tables are pure string
// functions, so relay/protocol can consult them without pulling the upstream
// wire types into its import graph. The adaptor-aware half — attaching a
// neutral definition to a concrete upstream body, and bridging the stream —
// lives in relay/controller.

// HostedKind is the neutral identity of a hosted tool.
type HostedKind string

const (
	// HostedWebSearch is the one kind mapped in both directions.
	HostedWebSearch HostedKind = "web_search"
	// HostedMCP is mapped for response artifacts. Note that no protocol's MCP
	// DECLARATION form is mapped on the request side — the reference
	// implementation drops those too (see the attach switches).
	HostedMCP HostedKind = "mcp"
	// HostedNative is any other hosted type: carried, named in diagnostics,
	// never mapped.
	HostedNative HostedKind = "native"
)

// HostedExecution distinguishes a tool the upstream runs and reports back
// (server) from one it merely emits a call for, exactly like an ordinary
// function (client). Only server-executed kinds can produce hosted artifacts;
// the client-executed ones (computer_use, bash, text_editor, memory) are
// function-shaped in every protocol that has them.
type HostedExecution string

const (
	HostedServerExecution HostedExecution = "server"
	HostedClientExecution HostedExecution = "client"
)

// HostedDefinition is one hosted tool a client declared.
type HostedDefinition struct {
	Kind       HostedKind
	Execution  HostedExecution
	NativeType string // the source protocol's own type string, e.g. "web_search_20250305"
	Name       string // the declaration's name, when it has one
	WebSearch  *HostedSearchOptions
	Raw        json.RawMessage // the declaration as it arrived, for same-family re-emit
	Path       string          // "tools[2]" — for diagnostics
}

// HostedSearchOptions carries the web-search knobs a declaration may set. The
// protocol-specific ones (Claude's max_uses and domain filters) are read too,
// so that dropping them can be diagnosed rather than silently ignored.
type HostedSearchOptions struct {
	MaxUses           *int
	AllowedDomains    []string
	BlockedDomains    []string
	UserLocation      json.RawMessage
	SearchContextSize string
}

// HostedHistoryItem is one hosted turn the client replayed in its request
// (`input` for Responses, `messages` for Claude). A client that used a hosted
// tool must send the artifact back on the next turn, so a cross-protocol
// translation that cannot carry these cannot carry a multi-turn hosted
// conversation at all.
type HostedHistoryItem struct {
	Kind        HostedKind
	NativeType  string
	ID          string
	CallID      string
	Name        string
	ServerLabel string
	Status      string
	// Action is the search that was run, in the Responses shape
	// ({"type":"search","query":…}). Both parsers normalize to it, because it
	// is the richer of the two and Claude's server_tool_use input is a strict
	// subset of it.
	Action    json.RawMessage
	Results   json.RawMessage
	Arguments json.RawMessage // MCP call arguments
	Output    json.RawMessage // MCP call output
	Error     json.RawMessage // MCP call error, when the call failed
	Raw       json.RawMessage
	Sequence  int // arrival order, so a renderer can interleave faithfully
	// BeforeMessage is the index into the normalized Messages this item
	// preceded in the client's transcript (len(Messages) means "it came last").
	// A hosted turn sits between two messages in every protocol — Claude puts
	// the `server_tool_use` and its result inside the assistant message that
	// follows, Responses puts a `web_search_call` item between two message
	// items — so a target that has to rebuild the transcript can only place it
	// faithfully if it knows the boundary it fell on.
	BeforeMessage int
}

// HostedSet is everything hosted a parsed request carried, in neutral form.
// It rides on GeneralOpenAIRequest as a `json:"-"` field: it must never reach
// an upstream body, and it is consumed by the upstream codec's RequestBody.
type HostedSet struct {
	Definitions  []HostedDefinition
	History      []HostedHistoryItem
	ForcedChoice *HostedDefinition

	// Losses accumulates what the translation could not carry, for the caller
	// to log and to weigh against the channel's policy. It is an OUTPUT, not
	// part of what the client sent: the converters that build an upstream body
	// append to it as they walk, and the caller reads it once they return. It
	// lives here because the converters are pure functions whose signatures
	// already carry the request, and a second return value would churn every
	// caller for a side channel only the relay reads.
	Losses []ConversionDiagnostic
}

// Empty reports whether there is nothing hosted to translate. A nil set is
// empty, so callers can test `req.Hosted.Empty()` without a nil check.
func (s *HostedSet) Empty() bool {
	return s == nil ||
		(len(s.Definitions) == 0 && len(s.History) == 0 && s.ForcedChoice == nil)
}

// AddLoss records one thing the translation could not carry. A nil set is a
// no-op, so converters can call it unconditionally.
func (s *HostedSet) AddLoss(d ConversionDiagnostic) {
	if s != nil {
		s.Losses = append(s.Losses, d)
	}
}

// LossOf returns the losses recorded so far, tolerating a nil set.
func (s *HostedSet) LossOf() []ConversionDiagnostic {
	if s == nil {
		return nil
	}
	return s.Losses
}

// HostedStatus is the Responses status vocabulary, reused as the neutral one.
const (
	HostedStatusInProgress = "in_progress"
	HostedStatusSearching  = "searching"
	HostedStatusCompleted  = "completed"
	HostedStatusFailed     = "failed"
	HostedStatusIncomplete = "incomplete"
)

// HostedOutputItem is a hosted artifact an upstream produced, to be rendered
// back into whatever protocol the client speaks.
type HostedOutputItem struct {
	Kind        HostedKind
	ID          string
	CallID      string
	Name        string
	ServerLabel string
	Status      string
	Action      json.RawMessage
	Arguments   json.RawMessage
	Output      json.RawMessage
	Error       *Error
}

// HostedOutput is the non-streaming form: the artifacts of one response.
type HostedOutput struct {
	Items []HostedOutputItem
}

// Streaming phases of a hosted artifact.
const (
	HostedPhaseStart  = "start"
	HostedPhaseUpdate = "update"
	HostedPhaseDone   = "done"
)

// HostedStreamEvent is the streaming form of one HostedOutputItem transition.
type HostedStreamEvent struct {
	Phase string
	Item  HostedOutputItem
}

// --- kind tables -----------------------------------------------------------
//
// Source side: a wire type string becomes a kind. Target side: a kind becomes
// the target protocol's own string, or "" when that protocol cannot express it
// at all — and "" is the signal to route the definition through the loss
// policy rather than to invent a shape.

// HostedKindFromResponsesToolType maps a Responses tool declaration type.
// web_search and all its dated/preview variants share the prefix.
func HostedKindFromResponsesToolType(t string) HostedKind {
	switch {
	case strings.HasPrefix(t, "web_search"):
		return HostedWebSearch
	case t == "mcp":
		return HostedMCP
	default:
		return HostedNative
	}
}

// HostedKindFromClaudeToolType maps a Claude tool declaration type
// ("web_search_20250305" and its future dated variants share the prefix).
func HostedKindFromClaudeToolType(t string) HostedKind {
	switch {
	case strings.HasPrefix(t, "web_search"):
		return HostedWebSearch
	default:
		return HostedNative
	}
}

// HostedKindFromClaudeBlock maps a Claude response/request content block.
// A `server_tool_use` only counts as web search when it names web_search.
func HostedKindFromClaudeBlock(blockType, name string) HostedKind {
	switch blockType {
	case "mcp_tool_use", "mcp_tool_result":
		return HostedMCP
	case "web_search_tool_result":
		return HostedWebSearch
	case "server_tool_use":
		if name == "web_search" {
			return HostedWebSearch
		}
		return HostedNative
	default:
		return HostedNative
	}
}

// HostedExecutionOf classifies a hosted tool by who runs it.
func HostedExecutionOf(nativeType string) HostedExecution {
	for _, prefix := range []string{"computer_", "bash_", "text_editor_", "memory_"} {
		if strings.HasPrefix(nativeType, prefix) {
			return HostedClientExecution
		}
	}
	return HostedServerExecution
}

// HostedResponsesItemType is the Responses output item type for a kind, or ""
// when Responses cannot express it. code_execution is deliberately "" here,
// matching the reference implementation: without a container id there is no
// honest way to build a code_interpreter_call.
func HostedResponsesItemType(k HostedKind) string {
	switch k {
	case HostedWebSearch:
		return "web_search_call"
	case HostedMCP:
		return "mcp_call"
	default:
		return ""
	}
}

// HostedResponsesDeclarationType is the Responses TOOL DECLARATION type for a
// kind. It is not the same string as the output item type above: a client
// declares "web_search" and the upstream answers with a "web_search_call".
func HostedResponsesDeclarationType(k HostedKind) string {
	switch k {
	case HostedWebSearch:
		return "web_search"
	default:
		return ""
	}
}

// RawToValue decodes a raw JSON fragment into the untyped form an upstream
// body's `any` field wants. An empty or malformed fragment is nil, which the
// callers' omitempty tags then leave out of the body entirely.
func RawToValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// HostedClaudeDeclarationType is the Claude TOOL declaration type for a kind.
func HostedClaudeDeclarationType(k HostedKind) string {
	switch k {
	case HostedWebSearch:
		return "web_search_20250305"
	default:
		return ""
	}
}

// HostedClaudeBlockName is the name a Claude hosted content block carries.
func HostedClaudeBlockName(k HostedKind) string {
	switch k {
	case HostedWebSearch:
		return "web_search"
	case HostedMCP:
		return "mcp"
	default:
		return ""
	}
}

// --- loss policy -----------------------------------------------------------

// Diagnostic codes. They are constants so the log line, the rejected request
// body and the smoke assertions all name the same thing.
const (
	DiagUnsupportedHostedTool    = "unsupported_hosted_tool"
	DiagUnsupportedHostedHistory = "unsupported_hosted_history"
	DiagUnsupportedSearchLimit   = "unsupported_search_limit"
	DiagUnsupportedDomainFilter  = "unsupported_domain_filter"
	DiagDuplicateHostedWebSearch = "duplicate_hosted_web_search"
	DiagForcedChoiceDowngraded   = "forced_tool_choice_downgraded"
	DiagSearchContinuationLost   = "web_search_result_continuation_lost"
	DiagHostedStreamUnsupported  = "hosted_tool_streaming_unsupported"
)

// Tool loss policies, per channel. Empty means allow.
const (
	ToolLossAllow  = "allow"
	ToolLossSafe   = "safe"
	ToolLossStrict = "strict"
)

// ValidToolLossPolicy reports whether a configured policy is one this gateway
// knows. The empty string is valid and means allow.
func ValidToolLossPolicy(p string) bool {
	switch p {
	case "", ToolLossAllow, ToolLossSafe, ToolLossStrict:
		return true
	}
	return false
}

// DiagnosticSeverity follows the reference implementation: an error-severity
// loss is one that changes what the model is asked to do, as opposed to one
// that only changes how the answer is presented.
type DiagnosticSeverity string

const DiagnosticError DiagnosticSeverity = "error"

// ConversionDiagnostic records one thing the translation could not carry.
type ConversionDiagnostic struct {
	Code     string
	Path     string
	Message  string
	Severity DiagnosticSeverity
}

// NewHostedLoss builds a diagnostic for something the target protocol could not
// express. Every loss this stage reports is severity=error — a hosted tool
// that disappears changes what the model does, not just how the reply looks.
func NewHostedLoss(code, path, format string, args ...any) ConversionDiagnostic {
	return ConversionDiagnostic{
		Code:     code,
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
		Severity: DiagnosticError,
	}
}

// DescribeHosted names a definition the way a diagnostic should: by its name
// when it has one, and always by the type the client actually wrote.
func DescribeHosted(def HostedDefinition) string {
	if def.Name != "" {
		return fmt.Sprintf("%q（类型 %s）", def.Name, def.NativeType)
	}
	return fmt.Sprintf("%q", def.NativeType)
}

// UnsupportedHostedLoss records that a target protocol cannot express def.
// Shared by the three attach sites so a drop reads the same wherever it happens.
func UnsupportedHostedLoss(target string, def HostedDefinition) ConversionDiagnostic {
	return NewHostedLoss(DiagUnsupportedHostedTool, def.Path,
		"%s 无法表达托管工具 %s", target, DescribeHosted(def))
}

// RejectLoss decides whether the diagnostics collected while translating a
// request are fatal, under the channel's policy.
//
// It is consulted on the REQUEST PHASE ONLY. The response and stream phases
// never reject under any policy: by then the upstream has already done the
// work, and refusing to hand back an artifact the client asked for would turn
// a lossy answer into no answer at all. This is the one place this repository
// deliberately allows a drop instead of a named rejection — the "never
// silently drop" rule is honoured by making every drop a diagnostic (logged,
// and a 400 body under safe/strict), not by pretending nothing was lost.
//
// "allow" (the default, and the empty string) never rejects. "safe" rejects on
// any severity=error diagnostic; "strict" rejects on any diagnostic. Every
// diagnostic this stage emits is severity=error, so the two agree today, but
// the distinction is kept so a future presentation-only loss can be reported
// without failing requests under "safe".
func RejectLoss(policy string, diags []ConversionDiagnostic) *ErrorWithStatusCode {
	if policy == "" || policy == ToolLossAllow || len(diags) == 0 {
		return nil
	}
	var kept []ConversionDiagnostic
	for _, d := range diags {
		if policy == ToolLossStrict || d.Severity == DiagnosticError {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	parts := make([]string, 0, len(kept))
	for _, d := range kept {
		parts = append(parts, fmt.Sprintf("%s（%s）", d.Message, d.Code))
	}
	return &ErrorWithStatusCode{
		Error: Error{
			Message: fmt.Sprintf("渠道策略 %q 不允许托管工具（内置工具）丢损，上游协议无法表达：%s",
				policy, strings.Join(parts, "；")),
			Type:  "invalid_request_error",
			Param: kept[0].Path,
			Code:  "hosted_tool_loss",
		},
		StatusCode: http.StatusBadRequest,
	}
}
