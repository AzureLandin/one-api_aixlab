package anthropic

import "encoding/json"

// https://docs.anthropic.com/claude/reference/messages_post

type Metadata struct {
	UserId string `json:"user_id"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type Content struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
	// tool_calls
	Id    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// Content is `any` rather than string because a hosted tool result's
	// content is an ARRAY of result objects (or of error objects), not a
	// string: `web_search_tool_result.content` has no string form. An ordinary
	// text/tool_result block still marshals a string here, so the wire shape of
	// every non-hosted block is unchanged.
	Content   any    `json:"content,omitempty"`
	ToolUseId string `json:"tool_use_id,omitempty"`
	// The fields below belong to the hosted blocks. ServerName is the server an
	// mcp_tool_use dials, IsError the failure flag on a hosted result, and
	// Caller what invoked a server-side tool (a nested MCP call carries
	// {"type":"tool","tool_id":…}). They are empty on every function tool
	// block, so those keep their wire shape byte for byte.
	ServerName string          `json:"server_name,omitempty"`
	IsError    *bool           `json:"is_error,omitempty"`
	Caller     json.RawMessage `json:"caller,omitempty"`
	// ErrorCode is a compatibility extension, not an Anthropic field: Claude
	// puts a provider error code inside the nested result content
	// (web_search_tool_result_error.error_code). It is modelled so a code that
	// arrived that way has somewhere to be reported from.
	ErrorCode string `json:"error_code,omitempty"`
}

// ToolChoice is the Anthropic tool_choice object. DisableParallelToolUse is
// carried so an OpenAI `parallel_tool_calls: false` survives the conversion —
// dropping it let the upstream return parallel tool calls the client had
// explicitly refused.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

type Tool struct {
	Name string `json:"name"`
	// Description may legitimately be empty; Anthropic accepts the field absent.
	Description string `json:"description,omitempty"`
	// InputSchema is the client's JSON Schema passed through verbatim. It is
	// `any` rather than a struct with type/properties/required because a schema
	// is open-ended ($schema, additionalProperties, anyOf, definitions, …) and
	// re-shaping it into three fields both dropped the rest and panicked on the
	// unchecked `type` assertion.
	InputSchema any `json:"input_schema"`
	// CacheControl lets a client mark the tool definition as cacheable; it must
	// survive the conversion or prompt caching silently stops applying.
	CacheControl any `json:"cache_control,omitempty"`
	// Strict asks Anthropic to validate the generated arguments against the
	// schema instead of treating it as a best-effort hint.
	Strict *bool `json:"strict,omitempty"`
	// Type is the hosted-tool type ("web_search_20250305"). It is empty for an
	// ordinary function tool, and `omitempty` keeps that tool's wire shape
	// byte-identical to what it was before hosted tools existed.
	Type string `json:"type,omitempty"`
	// MaxUses, AllowedDomains, BlockedDomains and UserLocation are the hosted
	// web-search knobs. They are only ever set alongside Type.
	MaxUses        *int     `json:"max_uses,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	UserLocation   any      `json:"user_location,omitempty"`
}

type Request struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	System        string    `json:"system,omitempty"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
	StopSequences []string  `json:"stop_sequences,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	Temperature   *float64  `json:"temperature,omitempty"`
	TopP          *float64  `json:"top_p,omitempty"`
	TopK          int       `json:"top_k,omitempty"`
	Tools         []Tool    `json:"tools,omitempty"`
	ToolChoice    any       `json:"tool_choice,omitempty"`
	//Metadata    `json:"metadata,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Prompt-caching fields (§8.1). Pointers so "absent" (nil) is distinguishable
	// from "reported zero"; absence must never be turned into a fabricated 0 hit.
	CacheReadInputTokens     *int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`
}

type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type Response struct {
	Id           string    `json:"id"`
	Type         string    `json:"type"`
	Role         string    `json:"role"`
	Content      []Content `json:"content"`
	Model        string    `json:"model"`
	StopReason   *string   `json:"stop_reason"`
	StopSequence *string   `json:"stop_sequence"`
	Usage        Usage     `json:"usage"`
	Error        Error     `json:"error"`
}

type Delta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text"`
	PartialJson  string  `json:"partial_json,omitempty"`
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type StreamResponse struct {
	Type         string    `json:"type"`
	Message      *Response `json:"message"`
	Index        int       `json:"index"`
	ContentBlock *Content  `json:"content_block"`
	Delta        *Delta    `json:"delta"`
	Usage        *Usage    `json:"usage"`
}
