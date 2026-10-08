package protocol

import (
	"encoding/json"
	"strings"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// messagesRequest models the Anthropic Messages API request (§6.5). It covers
// system, content arrays, text/image blocks, tool_use and tool_result.
type messagesRequest struct {
	Model         string          `json:"model"`
	Messages      json.RawMessage `json:"messages"`
	System        json.RawMessage `json:"system"`
	MaxTokens     *int            `json:"max_tokens"`
	StopSequences []string        `json:"stop_sequences"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	TopK          int             `json:"top_k"`
	Tools         json.RawMessage `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
}

// ParseMessages converts a /v1/messages request body into the normalized
// GeneralOpenAIRequest intermediate structure.
func ParseMessages(body []byte) (*relaymodel.GeneralOpenAIRequest, *ProtocolError) {
	var req messagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, invalidField("body", "请求体不是合法的 Messages JSON：%s", err.Error())
	}
	if req.Model == "" {
		return nil, invalidField("model", "Messages 请求缺少 model")
	}
	if req.MaxTokens == nil || *req.MaxTokens <= 0 {
		return nil, invalidField("max_tokens", "Messages 请求必须提供正整数 max_tokens")
	}

	normalized := &relaymodel.GeneralOpenAIRequest{
		Model:       req.Model,
		Messages:    []relaymodel.Message{},
		MaxTokens:   *req.MaxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		TopK:        req.TopK,
	}
	if len(req.StopSequences) > 0 {
		normalized.Stop = req.StopSequences
	}

	// system: string OR array of {type:"text"} blocks
	if len(req.System) > 0 {
		sysText, perr := decodeMessagesSystem(req.System)
		if perr != nil {
			return nil, perr
		}
		if sysText != "" {
			normalized.Messages = append(normalized.Messages,
				relaymodel.Message{Role: "system", Content: sysText})
		}
	}

	if len(req.Messages) == 0 {
		return nil, invalidField("messages", "Messages 请求缺少 messages")
	}
	var inbounds []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(req.Messages, &inbounds); err != nil {
		return nil, invalidField("messages", "messages 必须是数组")
	}
	if len(inbounds) == 0 {
		return nil, invalidField("messages", "messages 不能为空")
	}
	for i, inb := range inbounds {
		msgs, perr := messagesInboundToOpenAI(i, inb.Role, inb.Content)
		if perr != nil {
			return nil, perr
		}
		normalized.Messages = append(normalized.Messages, msgs...)
	}

	if perr := parseMessagesTools(req.Tools, normalized); perr != nil {
		return nil, perr
	}
	if perr := parseMessagesToolChoice(req.ToolChoice, normalized); perr != nil {
		return nil, perr
	}
	return normalized, nil
}

func decodeMessagesSystem(raw json.RawMessage) (string, *ProtocolError) {
	if s, ok := decodeString(raw); ok {
		return s, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", invalidField("system", "system 必须是字符串或 text block 数组")
	}
	var b strings.Builder
	for i, blk := range blocks {
		if jsonStringOf(blk, "type") != "text" {
			return "", unsupportedField("system["+itoa(i)+"].type", "system 只支持 text block")
		}
		b.WriteString(jsonStringOf(blk, "text"))
	}
	return b.String(), nil
}

// messagesInboundToOpenAI converts one Anthropic inbound message (whose content
// is a string or a block array) into one or more normalized OpenAI messages.
// A tool_result block becomes a standalone role:"tool" message; assistant
// tool_use blocks are merged into a single assistant message carrying tool_calls.
func messagesInboundToOpenAI(i int, role string, contentRaw json.RawMessage) ([]relaymodel.Message, *ProtocolError) {
	if role != "user" && role != "assistant" {
		return nil, invalidField("messages["+itoa(i)+"].role",
			"messages role 只能是 user/assistant，收到：%s", role)
	}

	// bare string content
	if s, ok := decodeString(contentRaw); ok {
		return []relaymodel.Message{{Role: role, Content: s}}, nil
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return nil, invalidField("messages["+itoa(i)+"].content", "content 必须是字符串或 block 数组")
	}

	var (
		out       []relaymodel.Message
		curText   []relaymodel.MessageContent
		curCalls  []relaymodel.Tool
		flushText = func() {
			if len(curText) > 0 {
				out = append(out, relaymodel.Message{Role: role, Content: normalizeContentPayload(curText)})
				curText = nil
			}
		}
		flushCalls = func() {
			if len(curCalls) > 0 {
				out = append(out, relaymodel.Message{Role: "assistant", ToolCalls: curCalls})
				curCalls = nil
			}
		}
	)
	for j, blk := range blocks {
		bt := jsonStringOf(blk, "type")
		switch bt {
		case "text":
			curText = append(curText, relaymodel.MessageContent{
				Type: relaymodel.ContentTypeText,
				Text: jsonStringOf(blk, "text"),
			})
		case "image":
			ic, perr := decodeMessagesImageBlock(i, j, blk)
			if perr != nil {
				return nil, perr
			}
			curText = append(curText, ic)
		case "tool_result":
			flushText()
			flushCalls()
			msg, perr := decodeMessagesToolResult(i, j, blk)
			if perr != nil {
				return nil, perr
			}
			out = append(out, msg)
		case "tool_use":
			flushText()
			if role != "assistant" {
				return nil, invalidField(paramBlock(i, j), "tool_use 只能出现在 assistant 消息中")
			}
			t, perr := decodeMessagesToolUse(i, j, blk)
			if perr != nil {
				return nil, perr
			}
			curCalls = append(curCalls, t)
		case "document":
			return nil, unsupportedField(paramBlock(i, j), "第一版不支持 document block")
		default:
			return nil, unsupportedField(paramBlock(i, j), "未识别的 content block 类型：%s", bt)
		}
	}
	flushText()
	flushCalls()
	if len(out) == 0 {
		return nil, invalidField("messages["+itoa(i)+"].content", "content block 数组未解析出任何内容")
	}
	return out, nil
}

func normalizeContentPayload(parts []relaymodel.MessageContent) any {
	if len(parts) == 1 && parts[0].Type == relaymodel.ContentTypeText {
		return parts[0].Text
	}
	return parts
}

func paramBlock(i, j int) string {
	return "messages[" + itoa(i) + "].content[" + itoa(j) + "]"
}

func decodeMessagesImageBlock(i, j int, blk map[string]json.RawMessage) (relaymodel.MessageContent, *ProtocolError) {
	srcRaw, ok := blk["source"]
	if !ok {
		return relaymodel.MessageContent{}, invalidField(paramBlock(i, j), "image block 缺少 source")
	}
	var src map[string]json.RawMessage
	if err := json.Unmarshal(srcRaw, &src); err != nil {
		return relaymodel.MessageContent{}, invalidField(paramBlock(i, j), "image block source 非法")
	}
	srcType := jsonStringOf(src, "type")
	switch srcType {
	case "base64":
		mt := jsonStringOf(src, "media_type")
		data := jsonStringOf(src, "data")
		if mt == "" || data == "" {
			return relaymodel.MessageContent{}, invalidField(paramBlock(i, j), "base64 image source 缺少 media_type/data")
		}
		return relaymodel.MessageContent{
			Type:     relaymodel.ContentTypeImageURL,
			ImageURL: &relaymodel.ImageURL{Url: "data:" + mt + ";base64," + data},
		}, nil
	case "url":
		u := jsonStringOf(src, "url")
		if u == "" {
			return relaymodel.MessageContent{}, invalidField(paramBlock(i, j), "url image source 缺少 url")
		}
		return relaymodel.MessageContent{
			Type:     relaymodel.ContentTypeImageURL,
			ImageURL: &relaymodel.ImageURL{Url: u},
		}, nil
	default:
		return relaymodel.MessageContent{}, unsupportedField(paramBlock(i, j),
			"不支持的 image source 类型：%s", srcType)
	}
}

func decodeMessagesToolResult(i, j int, blk map[string]json.RawMessage) (relaymodel.Message, *ProtocolError) {
	toolUseID := jsonStringOf(blk, "tool_use_id")
	if toolUseID == "" {
		return relaymodel.Message{}, invalidField(paramBlock(i, j), "tool_result 缺少 tool_use_id")
	}
	content := ""
	if raw, ok := blk["content"]; ok {
		if s, isStr := decodeString(raw); isStr {
			content = s
		} else {
			// content may itself be a block array; extract text blocks.
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(raw, &blocks) == nil {
				var b strings.Builder
				for _, sub := range blocks {
					if jsonStringOf(sub, "type") == "text" {
						b.WriteString(jsonStringOf(sub, "text"))
					}
				}
				content = b.String()
			} else {
				content = string(raw)
			}
		}
	}
	return relaymodel.Message{Role: "tool", ToolCallId: toolUseID, Content: content}, nil
}

func decodeMessagesToolUse(i, j int, blk map[string]json.RawMessage) (relaymodel.Tool, *ProtocolError) {
	id := jsonStringOf(blk, "id")
	name := jsonStringOf(blk, "name")
	if name == "" {
		return relaymodel.Tool{}, invalidField(paramBlock(i, j), "tool_use 缺少 name")
	}
	args := "{}"
	if raw, ok := blk["input"]; ok {
		args = string(raw)
	}
	return relaymodel.Tool{
		Id:       id,
		Type:     "function",
		Function: relaymodel.Function{Name: name, Arguments: args},
	}, nil
}

func parseMessagesTools(raw json.RawMessage, normalized *relaymodel.GeneralOpenAIRequest) *ProtocolError {
	if len(raw) == 0 {
		return nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return invalidField("tools", "tools 必须是数组")
	}
	for i, tool := range tools {
		if n := jsonStringOf(tool, "type"); n != "" && n != "custom" {
			return unsupportedField("tools["+itoa(i)+"].type", "不支持的工具类型：%s", n)
		}
		params := anyOf(tool, "input_schema")
		normalized.Tools = append(normalized.Tools, relaymodel.Tool{
			Type: "function",
			Function: relaymodel.Function{
				Name:        jsonStringOf(tool, "name"),
				Description: jsonStringOf(tool, "description"),
				Parameters:  params,
			},
		})
	}
	return nil
}

func parseMessagesToolChoice(raw json.RawMessage, normalized *relaymodel.GeneralOpenAIRequest) *ProtocolError {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return invalidField("tool_choice", "tool_choice 必须是对象")
	}
	switch jsonStringOf(m, "type") {
	case "auto":
		normalized.ToolChoice = "auto"
	case "any":
		normalized.ToolChoice = "required"
	case "tool":
		normalized.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": jsonStringOf(m, "name")},
		}
	case "none":
		normalized.ToolChoice = "none"
	default:
		return unsupportedField("tool_choice.type", "未识别的 tool_choice 类型：%s", jsonStringOf(m, "type"))
	}
	return nil
}

// finishReasonToStopReason maps OpenAI finish_reason back to Anthropic
// stop_reason; it is the inverse of the anthropic adaptor's mapping.
func finishReasonToStopReason(finish string) *string {
	var s string
	switch finish {
	case "stop":
		s = "end_turn"
	case "length":
		s = "max_tokens"
	case "tool_calls":
		s = "tool_use"
	case "stop_sequence":
		s = "stop_sequence"
	case "":
		return nil
	default:
		s = "end_turn"
	}
	return &s
}

// RenderMessages converts an upstream OpenAI chat.completion into an Anthropic
// Messages response (§6.5). usage keeps nil-vs-zero semantics; Anthropic's
// Usage only has input/output tokens.
func RenderMessages(upstream *openai.TextResponse, model string) map[string]any {
	content := []map[string]any{}
	var stopReason *string
	if len(upstream.Choices) > 0 {
		choice := upstream.Choices[0]
		if text := choice.Message.StringContent(); text != "" {
			content = append(content, map[string]any{
				"type": "text",
				"text": text,
			})
		}
		for _, tc := range choice.Message.ToolCalls {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    tc.Id,
				"name":  tc.Function.Name,
				"input": argumentsToInput(tc.Function.Arguments),
			})
		}
		stopReason = finishReasonToStopReason(choice.FinishReason)
	}

	id := "msg_" + strings.TrimPrefix(upstream.Id, "chatcmpl-")
	envelope := map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": stopReason,
		"usage": map[string]int{
			"input_tokens":  upstream.Usage.PromptTokens,
			"output_tokens": upstream.Usage.CompletionTokens,
		},
	}
	if stopReason == nil {
		envelope["stop_reason"] = nil
	}
	return envelope
}

func argumentsToInput(args any) any {
	switch v := args.(type) {
	case nil:
		return map[string]any{}
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}
		}
		var m any
		if err := json.Unmarshal([]byte(v), &m); err == nil {
			return m
		}
		return map[string]any{"_raw": v}
	default:
		return v
	}
}
