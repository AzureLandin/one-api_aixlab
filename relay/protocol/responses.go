package protocol

import (
	"encoding/json"
	"strings"

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

// ResponsesStreamUnsupported reports whether a Responses request asked for
// streaming, which is finalized in stage 4 (implementation doc §7). Callers
// surface it as an explicit structured error rather than degrading silently.
func ResponsesStreamUnsupported() *ProtocolError {
	return unsupportedField("stream",
		"/v1/responses 流式（typed SSE events）在阶段 4 实现，当前请使用非流式或 /v1/chat/completions")
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

	messages, perr := responsesInputToMessages(req.Input, req.Instructions)
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
	// store defaults to false; store=true would require server-side persistence.
	if req.Store != nil && *req.Store {
		return unsupportedField("store",
			"不支持 store=true（网关不做服务端响应持久化）；请保持 store=false")
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
func responsesInputToMessages(inputRaw, instructionsRaw json.RawMessage) ([]relaymodel.Message, *ProtocolError) {
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
		case "function_call":
			msg, perr := responsesFunctionCallItem(i, item)
			if perr != nil {
				return nil, perr
			}
			messages = append(messages, msg)
		case "function_call_output":
			msg, perr := responsesFunctionCallOutputItem(i, item)
			if perr != nil {
				return nil, perr
			}
			messages = append(messages, msg)
		case "reasoning", "item_reference", "image_generation_call", "computer_call", "computer_call_output", "web_search_call":
			return nil, unsupportedField(ResponsesItemParam(i, "type"),
				"第一版不支持的 input item 类型：%s", itemType)
		default:
			return nil, unsupportedField(ResponsesItemParam(i, "type"),
				"未识别的 input item 类型：%s", itemType)
		}
	}
	if len(messages) == 0 {
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
		case "input_audio", "input_file", "encrypted_content":
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
	for i, tool := range tools {
		tt := jsonStringOf(tool, "type")
		if tt == "function" {
			normalized.Tools = append(normalized.Tools, relaymodel.Tool{
				Type: "function",
				Function: relaymodel.Function{
					Name:        jsonStringOf(tool, "name"),
					Description: jsonStringOf(tool, "description"),
					Parameters:  anyOf(tool, "parameters"),
				},
			})
			continue
		}
		// built-in tools (web_search, file_search, computer_use, ...) unsupported
		return unsupportedField("tools["+itoa(i)+"].type",
			"第一版只支持 function tools，收到：%s", tt)
	}
	return nil
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
	switch tt {
	case "function":
		normalized.ToolChoice = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": jsonStringOf(m, "name"),
			},
		}
		return nil
	case "allowed_tools", "hosted_tool", "mcp", "custom":
		return unsupportedField("tool_choice.type", "第一版不支持的 tool_choice 类型：%s", tt)
	default:
		return unsupportedField("tool_choice.type", "未识别的 tool_choice 类型：%s", tt)
	}
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
		for _, tc := range choice.Message.ToolCalls {
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        "fc_" + tc.Id,
				"call_id":   tc.Id,
				"name":      tc.Function.Name,
				"arguments": toolArgumentsToString(tc.Function.Arguments),
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

// buildResponsesUsage maps the normalized usage onto Responses usage fields,
// keeping the nil-vs-zero semantics: cache details are emitted ONLY when the
// upstream actually reported them (implementation doc §4.1 pointer rule).
func buildResponsesUsage(u relaymodel.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  u.PromptTokens,
		"output_tokens": u.CompletionTokens,
		"total_tokens":  u.TotalTokens,
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		out["input_tokens_details"] = map[string]any{
			"cached_tokens": *u.PromptTokensDetails.CachedTokens,
		}
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != 0 {
		out["output_tokens_details"] = map[string]any{
			"reasoning_tokens": u.CompletionTokensDetails.ReasoningTokens,
		}
	}
	return out
}
