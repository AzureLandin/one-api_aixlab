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
	Path       string // "tools[2]" — for diagnostics
}

// HostedSearchOptions carries the web-search knobs a declaration may set. The
// protocol-specific ones (Claude's max_uses and domain filters) are read too,
// so that dropping them can be diagnosed rather than silently ignored.
type HostedSearchOptions struct {
	MaxUses        *int
	AllowedDomains []string
	BlockedDomains []string
	// UserLocation is held in the neutral FLAT spelling
	// ({"type":"approximate", city, region, country, timezone}) whichever
	// protocol it arrived on; HostedApproximateLocation re-wraps it for the one
	// target that nests.
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
	Sequence  int             // arrival order, so a renderer can interleave faithfully
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

// ResponsesCustomToolNames is the set of tool names a Responses request declared
// as custom (freeform) tools. They are sent upstream as ordinary functions —
// that is the only way a protocol without freeform tools can carry them — so
// the response side needs the names back to restore their calls as
// custom_tool_call items instead of function_call ones.
//
// This is a remember-set, not a loss record: it rides on the request as a
// `json:"-"` field and is read by the renderers.
type ResponsesCustomToolNames map[string]struct{}

// Has reports whether name was declared as a custom tool. A nil set has
// nothing, so callers never need a nil check.
func (n ResponsesCustomToolNames) Has(name string) bool {
	if n == nil || name == "" {
		return false
	}
	_, ok := n[name]
	return ok
}

// Add records a name. A nil set is a no-op, so a caller can build the set
// lazily through the zero value of the field that holds it.
func (n *ResponsesCustomToolNames) Add(name string) {
	if *n == nil {
		*n = make(ResponsesCustomToolNames, 1)
	}
	(*n)[name] = struct{}{}
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
	// Results is the web-search result list; Output is the MCP call's output.
	// They are separate fields rather than one, because the two target protocols
	// put them in different places: Claude wraps both in a result block's
	// `content`, but Responses spells a search result as `action`/`status` on the
	// call item and an MCP result as `output`.
	Results json.RawMessage
	Output  json.RawMessage
	Error   *Error
}

// HostedOutput is the non-streaming form: the artifacts of one response.
type HostedOutput struct {
	Items []HostedOutputItem

	// Losses accumulates what the RENDERER could not put back on the wire, for
	// the relay to log once it has rendered. It mirrors HostedSet.Losses and
	// exists for the same reason — a renderer is a pure function whose signature
	// carries the response, and a second return value would churn every caller
	// for a side channel only the relay reads.
	//
	// It is an output of rendering, not of extraction: extraction never drops
	// anything, and the losses here are what the TARGET protocol could not
	// express. Nothing recorded here is ever rejected — the response phase
	// succeeds under every policy — so this is a diagnostic channel only.
	Losses []ConversionDiagnostic
}

// AddLoss records one thing rendering could not carry. A nil output is a no-op,
// so a renderer can call it unconditionally.
func (o *HostedOutput) AddLoss(d ConversionDiagnostic) {
	if o != nil {
		o.Losses = append(o.Losses, d)
	}
}

// LossOf returns the losses recorded so far, tolerating a nil output.
func (o *HostedOutput) LossOf() []ConversionDiagnostic {
	if o == nil {
		return nil
	}
	return o.Losses
}

// HostedLossLog is the streaming counterpart of HostedOutput.Losses: a
// diagnostic sink for a renderer that consumes hosted stream events one at a
// time and never builds a whole HostedOutput. It exists as its own type because
// the two stream renderers are long-lived objects owned by relayProtocolStream,
// which reads the log once the stream ends; a HostedOutput would have to be
// fabricated per event for no one's benefit.
//
// Like HostedOutput.Losses, nothing recorded here is ever rejected: the stream
// phase never refuses under any policy, so this is a diagnostic channel only.
type HostedLossLog struct {
	Losses []ConversionDiagnostic
}

// AddLoss records one thing the client's protocol could not carry. Nil-tolerant,
// so a renderer can call it unconditionally.
func (l *HostedLossLog) AddLoss(d ConversionDiagnostic) {
	if l != nil {
		l.Losses = append(l.Losses, d)
	}
}

// LossOf returns the losses recorded so far, tolerating a nil log.
func (l *HostedLossLog) LossOf() []ConversionDiagnostic {
	if l == nil {
		return nil
	}
	return l.Losses
}

// Failed reports whether this artifact records a failure, by either spelling a
// failure arrives in: an explicit error object, or the failed status.
func (i HostedOutputItem) Failed() bool {
	return i.Error != nil || i.Status == HostedStatusFailed
}

// MergeHostedItem folds a later phase of an artifact into the one already held.
//
// A stream refines an artifact rather than replacing it — the arguments arrive
// after the identity, the status and the result after both — so the later value
// wins for every field it carries. A field the later event left empty is kept:
// an update that does not repeat a field is not withdrawing it, and treating
// absence as withdrawal would delete an id mid-stream that the client has
// already been handed.
func MergeHostedItem(prev, next HostedOutputItem) HostedOutputItem {
	if next.Kind == "" {
		next.Kind = prev.Kind
	}
	if next.ID == "" {
		next.ID = prev.ID
	}
	if next.CallID == "" {
		next.CallID = prev.CallID
	}
	if next.Name == "" {
		next.Name = prev.Name
	}
	if next.ServerLabel == "" {
		next.ServerLabel = prev.ServerLabel
	}
	if next.Status == "" {
		next.Status = prev.Status
	}
	if len(next.Action) == 0 {
		next.Action = prev.Action
	}
	if len(next.Arguments) == 0 {
		next.Arguments = prev.Arguments
	}
	if len(next.Results) == 0 {
		next.Results = prev.Results
	}
	if len(next.Output) == 0 {
		next.Output = prev.Output
	}
	if next.Error == nil {
		next.Error = prev.Error
	}
	return next
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

// HostedSearchAction builds the neutral search action ({"type":"search",
// "query":…}) that both parsers produce and both renderers read. It is defined
// once so the two directions cannot drift into two shapes: Claude's
// server_tool_use input is a strict subset of this, and the Responses action is
// superset-equal to it.
func HostedSearchAction(query any) json.RawMessage {
	action := map[string]any{"type": "search"}
	if query != nil {
		action["query"] = query
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return nil
	}
	return raw
}

// HostedSearchQueryFromClaudeInput reads the query out of a Claude
// server_tool_use `input` object, which spells a search as {"query": …}.
func HostedSearchQueryFromClaudeInput(input any) any {
	if m, ok := input.(map[string]any); ok {
		return m["query"]
	}
	return nil
}

// HostedClaudeSearchInput is the inverse: it narrows the neutral action back to
// the `input` object a Claude server_tool_use block carries. The extra `type`
// key of the neutral shape is dropped, because Claude's input is documented as
// {"query": …} and an unknown key there is not something the upstream ignores.
func HostedClaudeSearchInput(action json.RawMessage) any {
	var m map[string]any
	if len(action) > 0 {
		if err := json.Unmarshal(action, &m); err != nil {
			m = nil
		}
	}
	if q, ok := m["query"]; ok {
		return map[string]any{"query": q}
	}
	if v := RawToValue(action); v != nil {
		return v
	}
	return map[string]any{}
}

// --- the MCP result --------------------------------------------------------
//
// Both target protocols spell an MCP result as a STRING, which is why the
// neutral form holds it that way too: Responses says `output`, Claude says
// `mcp_tool_result.content`. Claude also accepts the structured form — an array
// holding one text block — and that is the one shape that has to be folded down
// before it can be sent on, so the folding lives here rather than in either
// renderer. Doing it once is what stops the two directions from disagreeing
// about what an MCP result is.

// HostedMCPResultAsString folds Claude's documented spelling of an MCP result —
// an array holding one text block — down to the plain JSON string the neutral
// form holds, and which the Responses `output` field is too.
//
// A value that is already a string is returned as it is. So is one that cannot
// be folded without changing what it says (several blocks, a non-text block, a
// bare object): the renderer reports that as mcp_result_unrepresentable, which
// is a decision this normalizer is not in a position to make for it.
func HostedMCPResultAsString(raw json.RawMessage) json.RawMessage {
	blocks, ok := RawToValue(raw).([]any)
	if !ok {
		return raw
	}
	if len(blocks) == 0 {
		return HostedJSONString("")
	}
	if len(blocks) != 1 {
		return raw
	}
	blk, ok := blocks[0].(map[string]any)
	if !ok {
		return raw
	}
	if t, _ := blk["type"].(string); t != "text" {
		return raw
	}
	text, ok := blk["text"].(string)
	if !ok {
		return raw
	}
	return HostedJSONString(text)
}

// HostedMCPResultString renders a neutral MCP result as the string both
// protocols expect. The second return value is false when the value has no
// string spelling, which the caller reports as mcp_result_unrepresentable
// rather than sending a shape the upstream would reject.
func HostedMCPResultString(raw json.RawMessage) (string, bool) {
	folded := HostedMCPResultAsString(raw)
	switch v := RawToValue(folded).(type) {
	case nil:
		return "", true
	case string:
		return v, true
	default:
		return "", false
	}
}

// HostedJSONString encodes a Go string as the JSON string both protocols carry
// a hosted result in, so a caller holding plain text does not hand-roll the
// quoting.
func HostedJSONString(s string) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return raw
}

// HostedMCPArgumentsString renders neutral MCP arguments (an object) as the
// JSON-encoded string Responses carries in `arguments`. The second return value
// is false when the neutral value is not an object, which is the only shape
// both protocols can round-trip.
func HostedMCPArgumentsString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "{}", true
	}
	if _, ok := RawToValue(raw).(map[string]any); !ok {
		return "", false
	}
	return string(raw), true
}

// --- the user_location knob -------------------------------------------------
//
// The two spellings in the wild differ by exactly one level of nesting: Chat
// Completions wraps the four fields under "approximate", while the Responses
// tool declaration and Claude's web_search put them at the top level. The
// neutral form takes the FLAT spelling, so a declaration that travelled from
// Claude to Responses needs no rewrite and only the chat target has to re-wrap.
// Normalizing at the parse boundary is what keeps that true: the field means the
// same thing whichever protocol a request arrived on.

// locationFields reads the city/region/country/timezone of either spelling.
// It returns nil for a value that is absent or not an object, so a malformed
// knob degrades to "no location" instead of a fabricated one.
func locationFields(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if inner, ok := m["approximate"].(map[string]any); ok {
		return inner
	}
	fields := map[string]any{}
	for _, key := range []string{"city", "region", "country", "timezone"} {
		if v, ok := m[key]; ok {
			fields[key] = v
		}
	}
	return fields
}

// HostedUserLocation normalizes a user_location into the neutral flat form.
func HostedUserLocation(raw json.RawMessage) json.RawMessage {
	fields := locationFields(raw)
	if fields == nil {
		return nil
	}
	loc := map[string]any{"type": "approximate"}
	for k, v := range fields {
		loc[k] = v
	}
	out, err := json.Marshal(loc)
	if err != nil {
		return nil
	}
	return out
}

// HostedApproximateLocation renders the neutral flat form in the nested shape
// Chat Completions expects.
func HostedApproximateLocation(raw json.RawMessage) json.RawMessage {
	fields := locationFields(raw)
	if fields == nil {
		return nil
	}
	loc := map[string]any{"type": "approximate", "approximate": fields}
	out, err := json.Marshal(loc)
	if err != nil {
		return nil
	}
	return out
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

	// A Responses custom (freeform) tool has no chat equivalent, so it is sent
	// upstream as a function taking one string argument. Nothing is lost — the
	// tool stays callable — so this is presentation severity: it is recorded
	// and logged, and only a strict policy turns it into a refusal.
	DiagCustomToolAsFunction = "custom_tool_as_function"

	// A custom tool that shares its name with another tool has to be dropped:
	// the target protocol tells their calls apart by name alone, so one of them
	// would be unreachable. Unlike the downgrade above this changes what the
	// model can do, hence error severity.
	DiagCustomToolNameConflict = "custom_tool_name_conflict"

	// Response-phase codes. They never reach a client: the response is already
	// being written, and the reference implementation's rule is that the
	// response phase does not reject under any policy. They exist so the
	// operator can see what a cross-protocol response could not carry back.
	DiagWebSearchResponseUnrepresentable = "web_search_response_unrepresentable"
	DiagWebSearchResultOmitted           = "web_search_result_omitted"
	DiagMCPIdentityMissing               = "mcp_identity_missing"
	DiagMCPResultUnrepresentable         = "mcp_result_unrepresentable"
	DiagMCPResultMissing                 = "mcp_result_missing"
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

const (
	DiagnosticError DiagnosticSeverity = "error"
	// DiagnosticWarning marks a loss that changes only how the answer is
	// presented. It is reported and logged like any other, but it does not
	// fail a request under "safe" — only "strict" refuses on it.
	DiagnosticWarning DiagnosticSeverity = "warning"
)

// ConversionDiagnostic records one thing the translation could not carry.
type ConversionDiagnostic struct {
	Code     string
	Path     string
	Message  string
	Severity DiagnosticSeverity
}

// NewHostedLoss builds a diagnostic for something the target protocol could not
// express. A hosted tool that disappears changes what the model does, not just
// how the reply looks, so this builder is error severity throughout; use
// NewPresentationLoss for a loss that only changes the reply's shape.
func NewHostedLoss(code, path, format string, args ...any) ConversionDiagnostic {
	return ConversionDiagnostic{
		Code:     code,
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
		Severity: DiagnosticError,
	}
}

// NewPresentationLoss builds a diagnostic for a loss that changes how the
// answer is presented rather than what the model was asked to do. The
// distinction is the reference implementation's (it calls this
// presentationLoss): such a loss is logged and counted, passes under "allow"
// and "safe", and is refused only under "strict".
func NewPresentationLoss(code, path, format string, args ...any) ConversionDiagnostic {
	return ConversionDiagnostic{
		Code:     code,
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
		Severity: DiagnosticWarning,
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
