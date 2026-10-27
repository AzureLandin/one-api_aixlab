package protocol

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/songquanpeng/one-api/common/logger"
	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// responsesRequest models the subset of the OpenAI Responses API the gateway
// accepts in the first version (implementation doc §6.4). Server-side response
// persistence, conversation chaining (previous_response_id) and background
// tasks are intentionally NOT supported and rejected with named parameters.
type responsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       json.RawMessage `json:"instructions"`
	MaxOutputTokens    *int            `json:"max_output_tokens"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	N                  *int            `json:"n"`
	Stream             bool            `json:"stream"`
	Store              *bool           `json:"store"`
	PreviousResponseID *string         `json:"previous_response_id"`
	Background         *bool           `json:"background"`
	ReasoningEffort    *string         `json:"reasoning_effort"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
	Tools              json.RawMessage `json:"tools"`
	User               string          `json:"user"`
}

// ParseResponses converts a /v1/responses request body into the normalized
// GeneralOpenAIRequest intermediate structure (§6.1 step 2).
func ParseResponses(body []byte) (*relaymodel.GeneralOpenAIRequest, *ProtocolError) {
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, invalidField("body", "请求体不是合法的 Responses JSON：%s", err.Error())
	}
	if req.Model == "" {
		return nil, invalidField("model", "Responses 请求缺少 model")
	}
	// §6.4 first-version limits.
	if perr := checkResponsesLimits(&req); perr != nil {
		return nil, perr
	}

	var history []relaymodel.HostedHistoryItem
	messages, perr := responsesInputToMessages(req.Input, req.Instructions, &history)
	if perr != nil {
		return nil, perr
	}

	normalized := &relaymodel.GeneralOpenAIRequest{
		Model:               req.Model,
		Messages:            messages,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		N:                   derefInt(req.N),
		Stream:              req.Stream,
		ReasoningEffort:     req.ReasoningEffort,
		ParallelTooCalls:    req.ParallelToolCalls,
		User:                req.User,
		MaxCompletionTokens: req.MaxOutputTokens,
	}
	if req.MaxOutputTokens != nil {
		normalized.MaxTokens = *req.MaxOutputTokens
	}
	// Replayed hosted turns are indexed by arrival order. Sequence is assigned
	// here rather than in the walk so the walk only has to know where in the
	// transcript each item fell (BeforeMessage), which is the part it alone can
	// see.
	if len(history) > 0 {
		for i := range history {
			history[i].Sequence = i
		}
		normalized.Hosted = &relaymodel.HostedSet{History: history}
	}
	if perr := parseResponsesTools(req.Tools, normalized); perr != nil {
		return nil, perr
	}
	if perr := parseResponsesToolChoice(req.ToolChoice, normalized); perr != nil {
		return nil, perr
	}
	return normalized, nil
}

func checkResponsesLimits(req *responsesRequest) *ProtocolError {
	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		return unsupportedField("previous_response_id",
			"不支持 previous_response_id 对话链（不做服务端响应持久化），请改为在 input 中自带完整上下文")
	}
	if req.Background != nil && *req.Background {
		return unsupportedField("background", "不支持后台任务（background=true）")
	}
	// store=true would require server-side response persistence, which this
	// gateway does not provide. It is ignored rather than rejected because many
	// clients set it by default; not persisting is harmless here since the
	// response is always returned inline.
	if req.Store != nil && *req.Store {
		logger.Debugf(context.Background(),
			"responses: ignoring store=true (gateway keeps no server-side response state)")
	}
	return nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// responsesInputToMessages accepts either a bare string or an array of input
// items (message / function_call / function_call_output / reasoning). Unknown
// item types raise a structured error naming the type (§6.4).
//
// Hosted tool calls the client replayed are appended to history. They are an
// OUT parameter rather than a second return value because every error path in
// here already returns two values, and threading a third through a dozen of
// them would bury the real change.
func responsesInputToMessages(inputRaw, instructionsRaw json.RawMessage,
	history *[]relaymodel.HostedHistoryItem) ([]relaymodel.Message, *ProtocolError) {
	var messages []relaymodel.Message

	// instructions -> leading system message
	if len(instructionsRaw) > 0 {
		if s, ok := decodeString(instructionsRaw); ok {
			if s != "" {
				messages = append(messages, relaymodel.Message{Role: "system", Content: s})
			}
		} else {
			parts, perr := decodeResponsesContentParts(instructionsRaw)
			if perr != nil {
				return nil, perr
			}
			text := joinTextParts(parts)
			if text != "" {
				messages = append(messages, relaymodel.Message{Role: "system", Content: text})
			}
		}
	}

	if len(inputRaw) == 0 {
		return nil, invalidField("input", "Responses 请求缺少 input")
	}

	// bare string input
	if s, ok := decodeString(inputRaw); ok {
		if s == "" {
			return nil, invalidField("input", "input 不能为空字符串")
		}
		messages = append(messages, relaymodel.Message{Role: "user", Content: s})
		return messages, nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return nil, invalidField("input", "input 必须是字符串或 input item 数组")
	}
	skipped := 0
	for i, item := range items {
		itemType := jsonStringOf(item, "type")
		if itemType == "" {
			itemType = "message" // item without a type is treated as a message
		}
		switch itemType {
		case "message":
			role := jsonStringOf(item, "role")
			if role == "" {
				role = "user"
			}
			content, ok := item["content"]
			if !ok {
				return nil, invalidField(
					ResponsesItemParam(i, "content"), "message item 缺少 content")
			}
			if s, isStr := decodeString(content); isStr {
				messages = append(messages, relaymodel.Message{Role: role, Content: s})
				continue
			}
			parts, perr := decodeResponsesContentParts(content)
			if perr != nil {
				return nil, perr
			}
			if len(parts) == 0 {
				return nil, invalidField(
					ResponsesItemParam(i, "content"), "message item 的 content 数组为空")
			}
			messages = append(messages, relaymodel.Message{Role: role, Content: parts})
		case "function_call", "custom_tool_call":
			msg, perr := responsesFunctionCallItem(i, item)
			if perr != nil {
				return nil, perr
			}
			messages = append(messages, msg)
		case "function_call_output", "custom_tool_call_output":
			msg, perr := responsesFunctionCallOutputItem(i, item)
			if perr != nil {
				return nil, perr
			}
			messages = append(messages, msg)
		case "web_search_call", "mcp_call":
			// A hosted tool turn the client is replaying. It cannot become a chat
			// message — the upstream executed it, no client is waiting to answer —
			// but it does carry what the search found, so it is extracted rather
			// than dropped: a target protocol that HAS a hosted artifact vocabulary
			// can put it back on the wire, and the ones that do not report it under
			// the channel's loss policy instead of losing it wordlessly.
			*history = append(*history, responsesHostedItem(item, itemType, len(messages)))
		case "reasoning", "item_reference", "file_search_call",
			"computer_call", "computer_call_output", "image_generation_call",
			"code_interpreter_call", "local_shell_call", "local_shell_call_output",
			"mcp_list_tools", "mcp_approval_request", "mcp_approval_response":
			// None of these can be expressed in a Chat Completions request: they
			// are records of the model's own thinking or of hosted tool executions
			// whose artifacts this gateway does not model (the narrow kind table).
			// They are skipped rather than rejected, because clients replay them on
			// every following turn — rejecting made multi-turn reasoning and
			// web-search conversations fail outright. The drop is logged in debug
			// mode so it is never invisible.
			skipped++
			logger.Debugf(context.Background(),
				"responses: skipping input item type %q at index %d (no Chat Completions equivalent)",
				itemType, i)
			continue
		default:
			return nil, unsupportedField(ResponsesItemParam(i, "type"),
				"未识别的 input item 类型：%s", itemType)
		}
	}
	// A Responses function_call_output names only the call_id; the role:"tool"
	// message it becomes still has to name the function for strict
	// OpenAI-compatible upstreams, and the function_call item in the same input
	// list is where that name comes from.
	backfillToolResultNames(messages)
	if len(messages) == 0 {
		if skipped > 0 {
			return nil, invalidField("input",
				"input 只包含无法转为对话消息的条目（如 reasoning、内置工具调用等），未解析出任何消息")
		}
		return nil, invalidField("input", "input 未解析出任何消息")
	}
	return messages, nil
}

func responsesFunctionCallItem(i int, item map[string]json.RawMessage) (relaymodel.Message, *ProtocolError) {
	name := jsonStringOf(item, "name")
	if name == "" {
		return relaymodel.Message{}, invalidField(ResponsesItemParam(i, "name"), "function_call 缺少 name")
	}
	callID := jsonStringOf(item, "call_id")
	if callID == "" {
		return relaymodel.Message{}, invalidField(ResponsesItemParam(i, "call_id"), "function_call 缺少 call_id")
	}
	arguments := "{}"
	if raw, ok := item["arguments"]; ok {
		if s, isStr := decodeString(raw); isStr {
			arguments = s
		} else {
			arguments = string(raw)
		}
	} else if raw, ok := item["input"]; ok {
		// custom_tool_call carries its free-form payload in "input" instead of
		// the JSON "arguments" field; both map onto the chat tool_call string.
		if s, isStr := decodeString(raw); isStr {
			arguments = s
		} else {
			arguments = string(raw)
		}
	}
	return relaymodel.Message{
		Role: "assistant",
		ToolCalls: []relaymodel.Tool{{
			Id:       callID,
			Type:     "function",
			Function: relaymodel.Function{Name: name, Arguments: arguments},
		}},
	}, nil
}

func responsesFunctionCallOutputItem(i int, item map[string]json.RawMessage) (relaymodel.Message, *ProtocolError) {
	callID := jsonStringOf(item, "call_id")
	if callID == "" {
		return relaymodel.Message{}, invalidField(ResponsesItemParam(i, "call_id"), "function_call_output 缺少 call_id")
	}
	output := ""
	if raw, ok := item["output"]; ok {
		if s, isStr := decodeString(raw); isStr {
			output = s
		} else {
			output = string(raw)
		}
	}
	return relaymodel.Message{Role: "tool", ToolCallId: callID, Content: output}, nil
}

// responsesHostedItem reads one replayed hosted turn into the neutral form.
// before is the index into the normalized transcript the item preceded.
func responsesHostedItem(item map[string]json.RawMessage, itemType string, before int) relaymodel.HostedHistoryItem {
	h := relaymodel.HostedHistoryItem{
		NativeType:    itemType,
		ID:            jsonStringOf(item, "id"),
		BeforeMessage: before,
	}
	switch itemType {
	case "web_search_call":
		h.Kind = relaymodel.HostedWebSearch
		h.Status = jsonStringOf(item, "status")
		// The Responses shape ({"type":"search","query":…}) is the richer of the
		// two, so both parsers normalize onto it and a renderer only has to know
		// one.
		h.Action = rawOf(item, "action")
	case "mcp_call":
		h.Kind = relaymodel.HostedMCP
		h.CallID = jsonStringOf(item, "id")
		h.Name = jsonStringOf(item, "name")
		h.ServerLabel = jsonStringOf(item, "server_label")
		h.Status = jsonStringOf(item, "status")
		h.Arguments = responsesArguments(item["arguments"])
		h.Output = rawOf(item, "output")
		h.Error = rawOf(item, "error")
	}
	return h
}

// responsesArguments decodes an argument field that the Responses protocol
// carries as a JSON-encoded STRING (mcp_call.arguments, like function_call
// arguments) back into the object itself, so the neutral form is an object
// whichever protocol it came from. Claude's mcp_tool_use.input is already an
// object, and a renderer should not have to care which side it started on.
func responsesArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw
	}
	if !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

// ResponsesItemParam renders a stable, human-readable param path for input items.
func ResponsesItemParam(i int, field string) string {
	return "input[" + itoa(i) + "]." + field
}

func decodeString(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// decodeResponsesContentParts normalizes message content parts (text, image,
// refusal) into the OpenAI MessageContent list used downstream.
func decodeResponsesContentParts(raw json.RawMessage) ([]relaymodel.MessageContent, *ProtocolError) {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, invalidField("content", "content 必须是字符串或 part 数组")
	}
	out := make([]relaymodel.MessageContent, 0, len(parts))
	for i, part := range parts {
		pt := jsonStringOf(part, "type")
		switch pt {
		case "input_text", "output_text", "text":
			out = append(out, relaymodel.MessageContent{
				Type: relaymodel.ContentTypeText,
				Text: jsonStringOf(part, "text"),
			})
		case "input_image", "image_url":
			url := jsonStringOf(part, "image_url")
			if url == "" {
				if u, ok := part["image_url"]; ok {
					var m map[string]json.RawMessage
					if json.Unmarshal(u, &m) == nil {
						url = jsonStringOf(m, "url")
					}
				}
			}
			if url == "" {
				return nil, invalidField("content["+itoa(i)+"]", "图片 part 缺少 image_url")
			}
			out = append(out, relaymodel.MessageContent{
				Type:     relaymodel.ContentTypeImageURL,
				ImageURL: &relaymodel.ImageURL{Url: url},
			})
		case "refusal":
			out = append(out, relaymodel.MessageContent{
				Type: relaymodel.ContentTypeText,
				Text: jsonStringOf(part, "refusal"),
			})
		case "encrypted_content":
			// An opaque reasoning-state blob that accompanies an assistant turn.
			// Chat Completions has no field for it, so it is skipped rather than
			// rejected; only the surrounding text matters to the conversation.
			logger.Debugf(context.Background(),
				"responses: skipping encrypted_content part at index %d", i)
			continue
		case "input_audio", "input_file":
			// These carry real user content (audio, uploaded files) that the chat
			// format cannot represent. Refusing keeps the gateway from silently
			// answering without the attachment the user actually sent.
			return nil, unsupportedField("content["+itoa(i)+"].type",
				"第一版不支持的 content part 类型：%s", pt)
		default:
			return nil, unsupportedField("content["+itoa(i)+"].type",
				"未识别的 content part 类型：%s", pt)
		}
	}
	return out, nil
}

func joinTextParts(parts []relaymodel.MessageContent) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == relaymodel.ContentTypeText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func parseResponsesTools(raw json.RawMessage, normalized *relaymodel.GeneralOpenAIRequest) *ProtocolError {
	if len(raw) == 0 {
		return nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return invalidField("tools", "tools 必须是数组")
	}
	// Custom (freeform) tools are held back rather than appended: whether one
	// can be sent at all depends on the other tools in the same array, which
	// this walk has not finished reading. See admitCustomTools.
	var custom []customTool

	for i, tool := range tools {
		tt := jsonStringOf(tool, "type")
		path := "tools[" + itoa(i) + "]"
		if tt == "function" {
			fn := relaymodel.Function{
				Name:        jsonStringOf(tool, "name"),
				Description: jsonStringOf(tool, "description"),
				Parameters:  anyOf(tool, "parameters"),
			}
			// strict is a plain boolean in the Responses protocol, so it is read
			// directly rather than through the OpenAI-style nested shape.
			if raw, ok := tool["strict"]; ok {
				var strict bool
				if err := json.Unmarshal(raw, &strict); err == nil {
					fn.Strict = &strict
				}
			}
			normalized.Tools = append(normalized.Tools, relaymodel.Tool{
				Type:     "function",
				Function: fn,
			})
			continue
		}
		// A custom (freeform) tool. Chat Completions has no freeform tool, so
		// the only way to keep it callable is to send it as a function taking
		// one string argument — which is what the reference implementation
		// does, and what makes Codex's apply_patch reachable at all.
		if tt == "custom" {
			name := jsonStringOf(tool, "name")
			if name == "" {
				return invalidField(path+".name", "custom 工具缺少 name")
			}
			custom = append(custom, customTool{path: path, name: name, tool: relaymodel.Tool{
				Type: "function",
				Function: relaymodel.Function{
					Name:        name,
					Description: customToolDescription(tool),
					Parameters:  customToolInputSchema(),
				},
			}})
			continue
		}
		// A hosted/built-in tool (web_search, file_search, computer_use, mcp,
		// image_generation, code_interpreter, local_shell). It has no Chat
		// Completions equivalent, so it cannot go in normalized.Tools — but it is
		// still extracted rather than dropped, because the upstream codec is the
		// only place that knows whether the target can express it. Clients such as
		// Codex enable web_search by default, so whatever the policy decides, the
		// request must not fail merely for declaring one.
		set := hostedOf(normalized)
		def := relaymodel.HostedDefinition{
			Kind:       relaymodel.HostedKindFromResponsesToolType(tt),
			Execution:  relaymodel.HostedExecutionOf(tt),
			NativeType: tt,
			Name:       jsonStringOf(tool, "name"),
			Path:       path,
		}
		// The web-search knobs. Any one of them alone is enough to make the
		// declaration worth carrying, so the gate is "has any", not "has the
		// first one".
		if _, hasCtx := tool["search_context_size"]; hasCtx || tool["user_location"] != nil || tool["filters"] != nil {
			opts := &relaymodel.HostedSearchOptions{}
			if raw, ok := tool["search_context_size"]; ok {
				_ = json.Unmarshal(raw, &opts.SearchContextSize)
			}
			opts.UserLocation = relaymodel.HostedUserLocation(rawOf(tool, "user_location"))
			if filters, ok := tool["filters"]; ok {
				var f map[string]json.RawMessage
				if json.Unmarshal(filters, &f) == nil {
					opts.AllowedDomains = jsonStringListOf(f, "allowed_domains")
				}
			}
			def.WebSearch = opts
		}
		set.Definitions = append(set.Definitions, def)
	}
	admitCustomTools(normalized, custom)
	return nil
}

// customTool is a Responses custom (freeform) tool on its way upstream as a
// function, held back until the whole tools array has been read.
type customTool struct {
	path string
	name string
	tool relaymodel.Tool
}

// admitCustomTools appends the custom tools that can be sent and records what
// happened to the rest.
//
// Two tools sharing a name cannot be told apart by a protocol that identifies
// tools by name alone, so a custom tool colliding with another tool is dropped
// and named rather than left to make one of the two unreachable. Functions win
// whatever the order: this runs after the array has been read, so a function
// declared after the custom tool still wins. That is also why the check cannot
// live in the walk above.
//
// The custom tools that do go out are functions in every respect, so their
// names are remembered on the request — the renderers need them back to return
// their calls as custom_tool_call items.
func admitCustomTools(normalized *relaymodel.GeneralOpenAIRequest, custom []customTool) {
	if len(custom) == 0 {
		return
	}
	taken := make(map[string]struct{}, len(normalized.Tools)+len(custom))
	for _, t := range normalized.Tools {
		taken[t.Function.Name] = struct{}{}
	}
	for _, c := range custom {
		if _, exists := taken[c.name]; exists {
			hostedOf(normalized).AddLoss(relaymodel.NewHostedLoss(
				relaymodel.DiagCustomToolNameConflict, c.path,
				"custom 工具 %q 与同名工具冲突，目标协议只靠名字区分调用，已丢弃", c.name))
			continue
		}
		taken[c.name] = struct{}{}
		normalized.Tools = append(normalized.Tools, c.tool)
		normalized.CustomTools.Add(c.name)
		hostedOf(normalized).AddLoss(relaymodel.NewPresentationLoss(
			relaymodel.DiagCustomToolAsFunction, c.path,
			"目标协议没有自由格式工具，custom 工具 %q 已作为单 input 字符串参数的函数发送", c.name))
	}
}

// customToolInputArgument is the one argument a custom tool's function takes
// upstream. The reference implementation spells it the same way.
const customToolInputArgument = "input"

// customToolInputSchema is the schema a custom tool is sent with: a single
// required string holding the tool's raw input. additionalProperties is false
// because the whole point is that the model must not invent other arguments.
func customToolInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			customToolInputArgument: map[string]any{
				"type":        "string",
				"description": "Raw input for the tool.",
			},
		},
		"required":             []string{customToolInputArgument},
		"additionalProperties": false,
	}
}

// customToolDescription tells the model what a freeform tool actually wants,
// since the schema can only say "a string". The client's own description is
// kept, and so is its grammar — as a hint, because no JSON Schema can enforce
// one.
func customToolDescription(tool map[string]json.RawMessage) string {
	parts := make([]string, 0, 3)
	if description := jsonStringOf(tool, "description"); description != "" {
		parts = append(parts, description)
	}
	parts = append(parts, `This tool takes freeform text. Put the complete raw text in the "input" argument.`)
	if raw, ok := tool["format"]; ok {
		var format map[string]json.RawMessage
		if json.Unmarshal(raw, &format) == nil && jsonStringOf(format, "type") == "grammar" {
			if definition := jsonStringOf(format, "definition"); definition != "" {
				switch syntax := jsonStringOf(format, "syntax"); syntax {
				case "lark":
					parts = append(parts, "The input must match this Lark grammar:\n"+definition)
				case "regex":
					parts = append(parts, "The input must match this regular expression:\n"+definition)
				default:
					parts = append(parts, "The input must match this "+syntax+" grammar:\n"+definition)
				}
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func parseResponsesToolChoice(raw json.RawMessage, normalized *relaymodel.GeneralOpenAIRequest) *ProtocolError {
	if len(raw) == 0 {
		return nil
	}
	if s, ok := decodeString(raw); ok {
		switch s {
		case "auto", "none", "required":
			normalized.ToolChoice = s
			return nil
		default:
			return invalidField("tool_choice", "tool_choice 字符串只允许 auto/none/required，收到：%s", s)
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return invalidField("tool_choice", "tool_choice 必须是字符串或对象")
	}
	tt := jsonStringOf(m, "type")
	name := jsonStringOf(m, "name")
	// A custom tool travels upstream as a function of the same name, so forcing
	// one has the same shape as forcing a function. A custom tool that lost a
	// name conflict was never sent, so there is nothing to point at: it falls
	// through to the hosted path below, which records the downgrade rather than
	// naming a tool the upstream never received.
	if tt == "function" || (tt == "custom" && normalized.CustomTools.Has(name)) {
		normalized.ToolChoice = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name,
			},
		}
		return nil
	}
	// Any other non-function choice names a hosted tool (web_search, file_search,
	// computer_use, mcp, image_generation, ...). normalized.ToolChoice has no
	// way to say "force a hosted tool", so the forced choice is recorded on
	// the hosted set — a target that can express it applies it, and a target
	// that cannot reports forced_tool_choice_downgraded. Falling back to the
	// upstream default (auto) here is what keeps an unknown hosted name from
	// failing the whole request.
	logger.Debugf(context.Background(),
		"responses: tool_choice.type=%q is not a function tool; recording it as a forced hosted choice", tt)
	hostedOf(normalized).ForcedChoice = &relaymodel.HostedDefinition{
		Kind:       relaymodel.HostedKindFromResponsesToolType(tt),
		Execution:  relaymodel.HostedExecutionOf(tt),
		NativeType: tt,
		Name:       name,
		Path:       "tool_choice.type",
	}
	return nil
}

// RenderResponses converts an upstream OpenAI chat.completion into the
// Responses envelope with a stable response.id and output[] structure (§6.4).
func RenderResponses(upstream *openai.TextResponse, model string) map[string]any {
	status := "completed"
	var incompleteDetails any
	if len(upstream.Choices) > 0 && upstream.Choices[0].FinishReason == "length" {
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}

	output := make([]map[string]any, 0, len(upstream.Choices))
	// Hosted artifacts come first: a web_search_call is the search the assistant
	// message below is the answer to, and the Responses protocol orders a
	// transcript that way. Emitting them after the message would tell a client
	// the model answered before it looked anything up.
	output = append(output, renderResponsesHosted(upstream.Hosted)...)
	for _, choice := range upstream.Choices {
		msgID := "msg_" + strings.TrimPrefix(upstream.Id, "chatcmpl-")
		content := []map[string]any{}
		if text := choice.Message.StringContent(); text != "" {
			content = append(content, map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			})
		}
		if len(content) == 0 {
			content = append(content, map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			})
		}
		output = append(output, map[string]any{
			"type":    "message",
			"id":      msgID,
			"role":    "assistant",
			"status":  "completed",
			"content": content,
		})
		for i, tc := range choice.Message.ToolCalls {
			// new-api keys the output item by the upstream call id itself, so a
			// client can match item_id and call_id without translation; an
			// unidentified call falls back to a response-scoped id.
			itemID := tc.Id
			if itemID == "" {
				itemID = responseID(upstream.Id) + "_call_" + itoa(i)
			}
			arguments := toolArgumentsToString(tc.Function.Arguments)
			// A tool the client declared as custom went upstream as a function
			// taking one string argument, so it comes back in that shape and has
			// to be handed back in its own. Everything else is an ordinary
			// function call and is rendered exactly as before.
			if upstream.CustomTools.Has(tc.Function.Name) {
				output = append(output, map[string]any{
					"type":    "custom_tool_call",
					"id":      itemID,
					"call_id": tc.Id,
					"name":    tc.Function.Name,
					"input":   customToolInputFromArguments(arguments),
					"status":  "completed",
				})
				continue
			}
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        itemID,
				"call_id":   tc.Id,
				"name":      tc.Function.Name,
				"arguments": arguments,
				"status":    "completed",
			})
		}
	}

	envelope := map[string]any{
		"id":                 responseID(upstream.Id),
		"object":             "response",
		"created_at":         upstream.Created,
		"status":             status,
		"model":              model,
		"output":             output,
		"usage":              buildResponsesUsage(upstream.Usage),
		"incomplete_details": incompleteDetails,
	}
	if envelope["incomplete_details"] == nil {
		delete(envelope, "incomplete_details")
	}
	return envelope
}

// renderResponsesHosted turns the upstream's hosted artifacts into Responses
// output items. A kind with no Responses item type is skipped: the upstream
// produced an artifact this client protocol has no vocabulary for, the client
// never declared that tool, and inventing an item type would put a shape on the
// wire no Responses client knows how to read.
func renderResponsesHosted(hosted *relaymodel.HostedOutput) []map[string]any {
	if hosted == nil || len(hosted.Items) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(hosted.Items))
	for i, it := range hosted.Items {
		if item, ok := renderResponsesHostedItem(it, pathOfHosted(i), hosted); ok {
			items = append(items, item)
		}
	}
	return items
}

// renderResponsesHostedItem renders one artifact as its Responses output item.
// ok is false when the protocol has no item type for the kind, which the caller
// reports as a loss rather than an invented shape.
//
// It is split out from the loop above because a stream renders the same artifact
// once per phase, and the item it puts on the wire must be the same shape
// whether it was built from a whole body or refined event by event.
func renderResponsesHostedItem(it relaymodel.HostedOutputItem, path string,
	losses lossSink) (map[string]any, bool) {

	itemType := relaymodel.HostedResponsesItemType(it.Kind)
	if itemType == "" {
		losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedHostedTool, path,
			"OpenAI Responses 无法表达托管工具产物 %s", it.Kind))
		return nil, false
	}
	item := map[string]any{"type": itemType}
	if it.ID != "" {
		item["id"] = it.ID
	}
	if it.Status != "" {
		item["status"] = it.Status
	}
	switch it.Kind {
	case relaymodel.HostedWebSearch:
		if action := relaymodel.RawToValue(it.Action); action != nil {
			item["action"] = action
		}
		if len(it.Results) > 0 {
			// A web_search_call has no field for the results themselves: the
			// protocol reports them to the model inline and cites them as
			// annotations on the text. Claude's results are provider-encrypted,
			// so they could not be carried even if there were a field.
			losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagWebSearchResultOmitted, path+".results",
				"Claude 的 web_search_tool_result 内容无法写进 Responses 的 web_search_call（该协议把搜索结果内联给模型、以 citation 呈现），仅保留 status 与 action"))
		}
	case relaymodel.HostedMCP:
		if it.Name == "" || it.ServerLabel == "" {
			losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPIdentityMissing, path,
				"Responses 的 mcp_call 需要 name 与 server_label，上游只给了 name=%q server_label=%q", it.Name, it.ServerLabel))
			return nil, false
		}
		item["name"] = it.Name
		item["server_label"] = it.ServerLabel
		// `arguments` is a JSON-encoded string on this wire, not an object.
		args, ok := relaymodel.HostedMCPArgumentsString(it.Arguments)
		if !ok {
			losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPResultUnrepresentable, path+".arguments",
				"Responses 的 mcp_call.arguments 只能是 JSON 字符串，上游给出的入参不是对象"))
			return nil, false
		}
		item["arguments"] = args
		switch {
		case it.Failed():
			// A failed call reports the error INSTEAD of an output, which is
			// how the protocol distinguishes "the call returned this" from
			// "the call did not return". The item is still emitted — the call
			// really happened, and dropping it would hide the attempt.
			if errObj := mcpErrorObject(it.Error); len(errObj) > 0 {
				item["error"] = errObj
			}
		case it.Status == relaymodel.HostedStatusInProgress && len(it.Output) == 0:
			// A call the upstream has not finished has no outcome to report. The
			// protocol's own in_progress mcp_call is identity plus arguments with
			// no output at all, and that is the state a streamed call sits in
			// between being announced and returning. Writing output:"" here would
			// claim the call returned the empty string.
		default:
			out, ok := relaymodel.HostedMCPResultString(it.Output)
			if !ok {
				losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPResultUnrepresentable, path+".output",
					"Responses 的 mcp_call.output 只能是字符串，上游给出的结果无法降为该形式"))
				return nil, false
			}
			item["output"] = out
		}
	}
	return item, true
}

// mcpErrorObject renders a failed MCP call's error in the Responses shape
// ({"code":…,"message":…}), which is what the protocol's own mcp_call carries.
func mcpErrorObject(err *relaymodel.Error) map[string]any {
	if err == nil {
		return nil
	}
	obj := map[string]any{}
	if err.Message != "" {
		obj["message"] = err.Message
	}
	if err.Code != nil {
		obj["code"] = err.Code
	}
	return obj
}

func responseID(chatID string) string {
	if chatID == "" {
		return "resp_"
	}
	if strings.HasPrefix(chatID, "resp_") {
		return chatID
	}
	return "resp_" + strings.TrimPrefix(chatID, "chatcmpl-")
}

func toolArgumentsToString(args any) string {
	switch v := args.(type) {
	case string:
		return v
	case nil:
		return "{}"
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return "{}"
	}
}

// customToolInputFromArguments unwraps the raw input a custom tool's function
// call carries: upstream saw a function whose only argument is a string, so the
// call arrives as {"input": "..."} and the client expects the bare text.
//
// Any other shape is returned unchanged. A model that answered with something
// else is still answering, and handing the client an empty input would throw
// that away — the same fallback the reference implementation uses.
func customToolInputFromArguments(arguments string) string {
	var value map[string]any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return arguments
	}
	input, ok := value[customToolInputArgument]
	if !ok {
		return arguments
	}
	if text, ok := input.(string); ok {
		return text
	}
	return arguments
}

// buildResponsesUsage maps the normalized usage onto Responses usage fields,
// keeping the nil-vs-zero semantics: cache details are emitted ONLY when the
// upstream actually reported them (implementation doc §4.1 pointer rule).
//
// input_tokens is the normalized PromptTokens, which is total input (cache
// included) for every upstream family. cached_tokens comes from the
// normalized CacheReadInputTokens, so an Anthropic upstream — which reports
// cache reads but never an OpenAI prompt_tokens_details — is visible here too;
// the details fields remain fallbacks for a caller that has not normalized.
func buildResponsesUsage(u relaymodel.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  u.PromptTokens,
		"output_tokens": u.CompletionTokens,
		"total_tokens":  u.TotalTokens,
	}
	if cached, ok := responsesCachedTokens(u); ok {
		out["input_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != 0 {
		out["output_tokens_details"] = map[string]any{
			"reasoning_tokens": u.CompletionTokensDetails.ReasoningTokens,
		}
	}
	return out
}

// responsesCachedTokens returns the cache read count in any of the three shapes
// it can arrive in, or false when the upstream reported none. The normalized
// field is authoritative; the detail shapes are read only for callers that
// build a Usage by hand and skip NormalizeUsage.
func responsesCachedTokens(u relaymodel.Usage) (int, bool) {
	if u.CacheReadInputTokens != nil {
		return int(*u.CacheReadInputTokens), true
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		return *u.PromptTokensDetails.CachedTokens, true
	}
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens != nil {
		return *u.InputTokensDetails.CachedTokens, true
	}
	return 0, false
}
