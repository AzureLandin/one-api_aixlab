// Package openai_responses converts between the gateway's normalized OpenAI
// chat shape and the upstream OpenAI Responses API (/v1/responses) — the third
// upstream protocol, alongside chat completions and Anthropic Messages
// (implementation doc §6.6).
//
// It is a conversion library, not an adaptor: the same three functions serve the
// chat entry point (through its own sink) and the codec seam that carries
// /v1/messages and /v1/responses to a Responses-speaking channel, so a request
// translated here reaches the upstream in exactly one shape regardless of which
// client protocol asked for it.
package openai_responses

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Request is a Responses API request body. Only the fields the gateway can
// populate from a normalized chat request are modelled; anything else would be
// invented rather than translated.
type Request struct {
	Model             string      `json:"model"`
	Input             []InputItem `json:"input"`
	Instructions      string      `json:"instructions,omitempty"`
	MaxOutputTokens   *int        `json:"max_output_tokens,omitempty"`
	Temperature       *float64    `json:"temperature,omitempty"`
	TopP              *float64    `json:"top_p,omitempty"`
	FrequencyPenalty  *float64    `json:"frequency_penalty,omitempty"`
	PresencePenalty   *float64    `json:"presence_penalty,omitempty"`
	Stream            bool        `json:"stream,omitempty"`
	Tools             []ToolDef   `json:"tools,omitempty"`
	ToolChoice        any         `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"`
	Text              *TextConfig `json:"text,omitempty"`
	Reasoning         *Reasoning  `json:"reasoning,omitempty"`
	User              string      `json:"user,omitempty"`
	Metadata          any         `json:"metadata,omitempty"`
	Store             *bool       `json:"store,omitempty"`
}

// InputItem is one entry of a Responses `input` array. A conversation turn is a
// message item; a tool exchange is two items (the call and its output) rather
// than fields on a message, which is the structural difference from Chat
// Completions the translation has to bridge.
type InputItem struct {
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
	CallId  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	// Arguments is the JSON-encoded argument string of a function_call item.
	Arguments string `json:"arguments,omitempty"`
	// Output is the tool result, either a string or structured content.
	Output any `json:"output,omitempty"`
	// The fields below are set only on a hosted item (web_search_call,
	// mcp_call). They are what makes a replayed hosted turn survive the
	// translation: `id` pairs a call with what it produced, `action` is the
	// search that was run, and `server_label`/`error` are MCP's.
	ID          string          `json:"id,omitempty"`
	Status      string          `json:"status,omitempty"`
	Action      json.RawMessage `json:"action,omitempty"`
	ServerLabel string          `json:"server_label,omitempty"`
	Error       any             `json:"error,omitempty"`
}

// ContentPart is one entry of a message item's content array.
type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// ToolDef is a function tool declaration. The Responses protocol flattens what
// Chat Completions nests under `function`.
type ToolDef struct {
	Type string `json:"type"`
	// Name is omitempty so a hosted declaration ({"type":"web_search"}) does not
	// carry an empty one. A function tool always has a name — convertTools
	// refuses an unnamed one — so this changes nothing for those.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
	// The fields below belong to the hosted web_search declaration.
	SearchContextSize string          `json:"search_context_size,omitempty"`
	UserLocation      json.RawMessage `json:"user_location,omitempty"`
	Filters           *ToolFilters    `json:"filters,omitempty"`
}

// ToolFilters is the domain filter a Responses web_search declaration accepts.
type ToolFilters struct {
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// TextConfig is the Responses equivalent of chat's response_format.
type TextConfig struct {
	Format map[string]any `json:"format,omitempty"`
}

// Reasoning carries chat's reasoning_effort in the shape Responses expects.
type Reasoning struct {
	Effort string `json:"effort,omitempty"`
}

const (
	// upstreamUnsupportedField is the code for a chat request field the Responses
	// API cannot carry. The field is named in the message; refusing beats sending
	// a request whose promise (a stop sequence, a logit bias, a reproducible seed)
	// silently disappears.
	upstreamUnsupportedField = "responses_upstream_unsupported_field"
	// upstreamMissingToolCallId is the code for a tool message with no
	// tool_call_id: a function_call_output item cannot be built without it, and
	// folding the output into a user message (as some gateways do) would invent a
	// conversation the client did not write.
	upstreamMissingToolCallId = "responses_upstream_missing_tool_call_id"
)

func unsupportedField(format string, args ...any) *relaymodel.ErrorWithStatusCode {
	return openai.ErrorWrapper(fmt.Errorf(format, args...), upstreamUnsupportedField, http.StatusBadRequest)
}

// ChatToResponsesRequest translates a normalized chat request into a Responses
// request body.
//
// Fields with no Responses equivalent that would change the model's output or
// the shape of the answer are refused by name rather than dropped; only fields
// that affect transport or accounting (stream_options, service_tier) are
// dropped, with the reason recorded here.
func ChatToResponsesRequest(req *relaymodel.GeneralOpenAIRequest) (*Request, *relaymodel.ErrorWithStatusCode) {
	if req == nil {
		return nil, unsupportedField("请求为空，无法转换为 Responses 请求")
	}
	if req.Model == "" {
		return nil, unsupportedField("请求缺少 model")
	}
	// Responses returns exactly one answer, so a request for several is not
	// translatable; dropping `n` would answer a question the client did not ask.
	if req.N > 1 {
		return nil, unsupportedField("n=%d：Responses 上游一次只生成一个回答，无法表达 n>1", req.N)
	}
	if bizErr := checkUntranslatableFields(req); bizErr != nil {
		return nil, bizErr
	}

	input, instructions, bizErr := buildInput(req.Messages, req.Hosted)
	if bizErr != nil {
		return nil, bizErr
	}

	out := &Request{
		Model:             req.Model,
		Input:             input,
		Instructions:      instructions,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		FrequencyPenalty:  req.FrequencyPenalty,
		PresencePenalty:   req.PresencePenalty,
		Stream:            req.Stream,
		ParallelToolCalls: req.ParallelTooCalls,
		User:              req.User,
		Metadata:          req.Metadata,
		Store:             req.Store,
		Text:              convertResponseFormat(req.ResponseFormat),
	}
	// max_completion_tokens is the current chat field and wins when both are set;
	// max_tokens is the legacy spelling of the same knob.
	if req.MaxCompletionTokens != nil {
		out.MaxOutputTokens = req.MaxCompletionTokens
	} else if req.MaxTokens > 0 {
		limit := req.MaxTokens
		out.MaxOutputTokens = &limit
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "" {
		out.Reasoning = &Reasoning{Effort: *req.ReasoningEffort}
	}

	tools, bizErr := convertTools(req.Tools)
	if bizErr != nil {
		return nil, bizErr
	}
	out.Tools = tools
	choice, bizErr := convertToolChoice(req.ToolChoice)
	if bizErr != nil {
		return nil, bizErr
	}
	out.ToolChoice = choice

	attachHostedTools(out, req)

	return out, nil
}

// attachHostedTools declares on the Responses body the hosted tools the client
// asked for, and records what Responses cannot express.
//
// It reads the request rather than just its hosted set, because a hosted web
// search reaches this converter two ways: as a neutral declaration (a Claude or
// Responses client) and as the client's own chat-level `web_search_options`
// toggle (a chat client). Both name the same upstream tool, and neither may be
// dropped on the floor.
//
// The declaration type is always the canonical "web_search": a Responses client
// talking to a Responses upstream is the passthrough and never gets here, so
// there is no native Responses spelling to preserve.
func attachHostedTools(out *Request, req *relaymodel.GeneralOpenAIRequest) {
	set := req.Hosted
	if set == nil {
		// A chat client that set the toggle itself carries no hosted set at all;
		// one is made so recording a loss on this path is never a silent no-op.
		set = &relaymodel.HostedSet{}
	}
	declared := map[string]bool{}
	for _, def := range set.Definitions {
		t := relaymodel.HostedResponsesDeclarationType(def.Kind)
		if t == "" {
			set.AddLoss(relaymodel.UnsupportedHostedLoss("OpenAI Responses", def))
			continue
		}
		declared[t] = true
		out.Tools = append(out.Tools, responsesWebSearchTool(set, t, def.WebSearch, def.Path))
	}
	// The client's own chat toggle. Chat Completions spells hosted web search as
	// web_search_options and nothing else, so this is the same declaration
	// arriving by the other road; declaring it twice would be rejected by the
	// upstream, and the client's explicit field is the one that wins.
	if req.WebSearchOptions != nil && !declared["web_search"] {
		opts := &relaymodel.HostedSearchOptions{
			SearchContextSize: req.WebSearchOptions.SearchContextSize,
			// The chat spelling nests the fields one level deeper than the
			// Responses declaration; the neutral form is the flat one, so it is
			// unwrapped here and re-emitted as the declaration expects.
			UserLocation: relaymodel.HostedUserLocation(req.WebSearchOptions.UserLocation),
		}
		out.Tools = append(out.Tools, responsesWebSearchTool(set, "web_search", opts, "web_search_options"))
	}
	if set.ForcedChoice == nil {
		return
	}
	if t := relaymodel.HostedResponsesDeclarationType(set.ForcedChoice.Kind); t != "" {
		out.ToolChoice = map[string]any{"type": t}
		return
	}
	set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagForcedChoiceDowngraded, set.ForcedChoice.Path,
		"OpenAI Responses 无法强制使用托管工具 %s，已降级为自动选择",
		relaymodel.DescribeHosted(*set.ForcedChoice)))
}

// responsesWebSearchTool builds the declaration, carrying the search knobs
// Responses can express and recording the ones it cannot.
//
// Responses' web search takes a context size, a location and an ALLOW filter.
// Claude's two extras do not survive: max_uses and blocked_domains have no
// field here, so they become diagnostics rather than quietly changing what the
// upstream is allowed to do.
func responsesWebSearchTool(set *relaymodel.HostedSet, toolType string, search *relaymodel.HostedSearchOptions, path string) ToolDef {
	tool := ToolDef{Type: toolType}
	if search == nil {
		return tool
	}
	tool.SearchContextSize = search.SearchContextSize
	tool.UserLocation = search.UserLocation
	if len(search.AllowedDomains) > 0 {
		tool.Filters = &ToolFilters{AllowedDomains: search.AllowedDomains}
	}
	if search.MaxUses != nil {
		set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedSearchLimit, path,
			"OpenAI Responses 无法表达搜索次数上限 max_uses=%d", *search.MaxUses))
	}
	if len(search.BlockedDomains) > 0 {
		set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedDomainFilter, path,
			"OpenAI Responses 的 web_search 只支持 allowed_domains 白名单，无法表达 blocked_domains 黑名单"))
	}
	return tool
}

// checkUntranslatableFields refuses the chat fields this translation cannot
// carry. The list is deliberate rather than exhaustive-by-accident: each entry
// is something a client asked for by name, so forwarding the request without it
// would answer a different question than the one asked.
func checkUntranslatableFields(req *relaymodel.GeneralOpenAIRequest) *relaymodel.ErrorWithStatusCode {
	switch {
	case req.Stop != nil:
		return unsupportedField("stop：Responses 上游没有停止序列字段，转发会让停止序列失效")
	case req.LogitBias != nil:
		return unsupportedField("logit_bias：Responses 上游没有该字段")
	case req.Seed != 0:
		return unsupportedField("seed：Responses 上游没有该字段，转发会假装可复现")
	case req.Logprobs != nil || req.TopLogprobs != nil:
		return unsupportedField("logprobs/top_logprobs：Responses 上游返回的 logprobs 无法回填进 chat 信封")
	case req.Audio != nil || len(req.Modalities) > 0:
		return unsupportedField("audio/modalities：Responses 上游的音频输出无法回填进 chat 信封")
	case req.Prediction != nil:
		return unsupportedField("prediction：Responses 上游没有预测输出字段")
	case req.Functions != nil || req.FunctionCall != nil:
		return unsupportedField("functions/function_call：旧版函数字段无法映射到 Responses 的 tools/tool_choice")
	case req.TopK != 0:
		return unsupportedField("top_k：Responses 上游没有该字段")
	case req.NumCtx != 0:
		return unsupportedField("num_ctx：Responses 上游没有该字段")
	case req.Instruction != "":
		return unsupportedField("instruction：Responses 上游没有该字段")
	}
	// stream_options is deliberately not refused: the Responses API always
	// reports usage, so a request for usage is already satisfied, and refusing
	// would break clients that set it by default. service_tier is dropped for the
	// same class of reason — it steers routing on the upstream's own account, not
	// the answer this gateway relays.
	return nil
}

// buildInput splits the chat message list into Responses input items plus the
// instructions that system/developer turns become.
//
// A replayed hosted turn is spliced back in at the boundary it fell on: the
// Responses `input` array is the transcript, so a web_search_call belongs
// between the two messages it sat between, not appended at the end where the
// model would read the search as having happened after the question that
// followed it.
func buildInput(messages []relaymodel.Message, set *relaymodel.HostedSet) ([]InputItem, string, *relaymodel.ErrorWithStatusCode) {
	items := make([]InputItem, 0, len(messages))
	var instructions []string

	nextHistory := 0
	replayHistory := func(before int) {
		for set != nil && nextHistory < len(set.History) && set.History[nextHistory].BeforeMessage <= before {
			h := set.History[nextHistory]
			nextHistory++
			item, resultsLost := responsesHistoryItem(h)
			if item == nil {
				set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedHostedHistory, "input",
					"OpenAI Responses 无法回放托管工具历史（类型 %s）", h.NativeType))
				continue
			}
			items = append(items, *item)
			if resultsLost {
				set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagSearchContinuationLost, "input",
					"托管搜索结果无法回放进 Responses 的 web_search_call（该 item 只带搜索动作，没有结果位）"))
			}
		}
	}

	for i, msg := range messages {
		replayHistory(i)
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			return nil, "", unsupportedField("messages[%d] 缺少 role", i)
		}

		switch role {
		case "system", "developer":
			// The Responses API has one instructions field rather than system
			// messages in the turn list, so every such turn folds into it.
			if text := messageText(msg); text != "" {
				instructions = append(instructions, text)
			}
			continue
		case "tool", "function":
			if msg.ToolCallId == "" {
				return nil, "", openai.ErrorWrapper(
					fmt.Errorf("messages[%d] 是 tool 结果但没有 tool_call_id，无法构造 function_call_output", i),
					upstreamMissingToolCallId, http.StatusBadRequest)
			}
			items = append(items, InputItem{
				Type:   "function_call_output",
				CallId: msg.ToolCallId,
				Output: outputValue(msg.Content),
			})
			continue
		}

		content, empty, bizErr := messageContent(msg, role, i)
		if bizErr != nil {
			return nil, "", bizErr
		}
		// An assistant turn that only called a tool is fully expressed by the
		// function_call items below; an empty message item would add an item
		// carrying no information to the upstream.
		if !empty {
			items = append(items, InputItem{Role: role, Content: content})
		}
		if role == "assistant" {
			calls, bizErr := toolCallItems(msg.ToolCalls)
			if bizErr != nil {
				return nil, "", bizErr
			}
			items = append(items, calls...)
		}
	}
	// A hosted turn that trailed the whole transcript has no following message
	// to sit before, so it lands at the end.
	replayHistory(len(messages))

	return items, strings.Join(instructions, "\n\n"), nil
}

// responsesHistoryItem renders one replayed hosted turn as a Responses input
// item. It reports whether the turn's search results had to be left behind:
// a Responses web_search_call carries the search that was run, not its
// results, so a Claude-sourced turn loses them.
func responsesHistoryItem(h relaymodel.HostedHistoryItem) (*InputItem, bool) {
	switch h.Kind {
	case relaymodel.HostedWebSearch:
		return &InputItem{
			Type:   "web_search_call",
			ID:     h.ID,
			Status: h.Status,
			Action: h.Action,
		}, len(h.Results) > 0
	case relaymodel.HostedMCP:
		item := &InputItem{
			Type:        "mcp_call",
			ID:          h.ID,
			ServerLabel: h.ServerLabel,
			Name:        h.Name,
			Output:      relaymodel.RawToValue(h.Output),
			Error:       relaymodel.RawToValue(h.Error),
		}
		if len(h.Arguments) > 0 {
			item.Arguments = string(h.Arguments)
		}
		return item, false
	default:
		return nil, false
	}
}

// messageText flattens a message's text for the instructions field.
func messageText(msg relaymodel.Message) string {
	if msg.IsStringContent() {
		return strings.TrimSpace(msg.StringContent())
	}
	var b strings.Builder
	for _, part := range msg.ParseContent() {
		if part.Type == relaymodel.ContentTypeText {
			b.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// outputValue renders a tool result the way Responses expects it: a plain string
// stays a string, structured content keeps its structure.
func outputValue(content any) any {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	return content
}

// messageContent converts a user/assistant message body into a string or a part
// array. empty reports that the message carries no content at all.
func messageContent(msg relaymodel.Message, role string, index int) (content any, empty bool, bizErr *relaymodel.ErrorWithStatusCode) {
	if msg.Content == nil {
		return "", true, nil
	}
	if msg.IsStringContent() {
		text := msg.StringContent()
		return text, text == "", nil
	}

	rawParts, ok := msg.Content.([]any)
	if !ok {
		// Not a string and not a part list: refuse rather than guess what the
		// client meant to send.
		return nil, false, unsupportedField("messages[%d].content 既不是字符串也不是 part 数组", index)
	}
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	parts := make([]ContentPart, 0, len(rawParts))
	for j, raw := range rawParts {
		part, ok := raw.(map[string]any)
		if !ok {
			return nil, false, unsupportedField("messages[%d].content[%d] 不是对象", index, j)
		}
		partType, _ := part["type"].(string)
		switch partType {
		case "text":
			text, _ := part["text"].(string)
			parts = append(parts, ContentPart{Type: textType, Text: text})
		case "image_url":
			url := imageURLOf(part["image_url"])
			if url == "" {
				return nil, false, unsupportedField("messages[%d].content[%d].image_url 缺少 url", index, j)
			}
			parts = append(parts, ContentPart{Type: "input_image", ImageURL: url})
		case "input_audio", "file":
			// The normalized message models text and image parts only, so audio
			// and file attachments would have to be re-derived from the raw body
			// rather than translated. Refusing keeps the model from answering
			// without the attachment the user actually sent.
			return nil, false, unsupportedField("messages[%d].content[%d].type=%s：第一版不支持转换为 Responses part", index, j, partType)
		default:
			return nil, false, unsupportedField("messages[%d].content[%d] 未识别的 part 类型：%s", index, j, partType)
		}
	}
	return parts, len(parts) == 0, nil
}

// imageURLOf accepts both shapes a chat part may carry the URL in: the nested
// object form the API documents and the bare string some clients send.
func imageURLOf(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case map[string]any:
		url, _ := v["url"].(string)
		return url
	}
	return ""
}

// toolCallItems turns an assistant turn's tool calls into function_call items.
func toolCallItems(calls []relaymodel.Tool) ([]InputItem, *relaymodel.ErrorWithStatusCode) {
	items := make([]InputItem, 0, len(calls))
	for _, call := range calls {
		if call.Type != "" && call.Type != "function" {
			return nil, unsupportedField("tool_calls.type=%s：Responses 只承载 function 调用", call.Type)
		}
		if call.Id == "" {
			// The id is the only thing that binds a call to its later output; an
			// anonymous call would make the tool exchange unresolvable.
			return nil, unsupportedField("tool_calls[%s] 缺少 id，无法构造 function_call", call.Function.Name)
		}
		if call.Function.Name == "" {
			return nil, unsupportedField("tool_calls[%s] 缺少函数名", call.Id)
		}
		items = append(items, InputItem{
			Type:      "function_call",
			CallId:    call.Id,
			Name:      call.Function.Name,
			Arguments: argumentsString(call.Function.Arguments),
		})
	}
	return items, nil
}

// argumentsString renders a function call's arguments as the JSON string the
// Responses protocol carries. A value that is already a string is passed through
// (validated as JSON when possible, but never re-encoded, so a model's own
// formatting survives).
func argumentsString(args any) string {
	switch v := args.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// convertTools maps chat function tools onto flattened Responses tool
// declarations.
func convertTools(tools []relaymodel.Tool) ([]ToolDef, *relaymodel.ErrorWithStatusCode) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]ToolDef, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			return nil, unsupportedField("tools.type=%s：Responses 上游只支持 function 工具", tool.Type)
		}
		if tool.Function.Name == "" {
			return nil, unsupportedField("tools 中存在无名函数")
		}
		out = append(out, ToolDef{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
			Strict:      tool.Function.Strict,
		})
	}
	return out, nil
}

// convertToolChoice maps chat's tool_choice onto the Responses spellings: the
// named-function form is flat there instead of nested under `function`.
func convertToolChoice(choice any) (any, *relaymodel.ErrorWithStatusCode) {
	switch v := choice.(type) {
	case nil:
		return nil, nil
	case string:
		switch v {
		case "auto", "none", "required":
			return v, nil
		}
		return nil, unsupportedField("tool_choice=%s：只允许 auto/none/required 或具名函数", v)
	case map[string]any:
		tt, _ := v["type"].(string)
		if tt != "function" {
			return nil, unsupportedField("tool_choice.type=%s：只支持 function", tt)
		}
		name := ""
		if n, ok := v["name"].(string); ok {
			name = n
		} else if fn, ok := v["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			return nil, unsupportedField("tool_choice 缺少函数名")
		}
		return map[string]any{"type": "function", "name": name}, nil
	}
	return nil, unsupportedField("tool_choice 的形态无法识别")
}

// convertResponseFormat maps chat's response_format onto Responses' text.format.
// The two protocols nest the schema differently: chat keeps it under
// json_schema, Responses carries name/schema/strict at the same level as type.
func convertResponseFormat(format *relaymodel.ResponseFormat) *TextConfig {
	if format == nil || strings.TrimSpace(format.Type) == "" {
		return nil
	}
	out := map[string]any{"type": format.Type}
	if format.Type == "json_schema" && format.JsonSchema != nil {
		if format.JsonSchema.Name != "" {
			out["name"] = format.JsonSchema.Name
		}
		if format.JsonSchema.Description != "" {
			out["description"] = format.JsonSchema.Description
		}
		if format.JsonSchema.Schema != nil {
			out["schema"] = format.JsonSchema.Schema
		}
		if format.JsonSchema.Strict != nil {
			out["strict"] = *format.JsonSchema.Strict
		}
	}
	return &TextConfig{Format: out}
}
