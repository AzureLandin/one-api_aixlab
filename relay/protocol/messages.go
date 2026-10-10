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
		msgs, hist, perr := messagesInboundToOpenAI(i, inb.Role, inb.Content)
		if perr != nil {
			return nil, perr
		}
		// A hosted turn's position is relative to the message list this inbound
		// message produced; shift it onto the whole transcript's numbering
		// before filing it, or the upstream would be told to splice it in at
		// the wrong turn.
		if len(hist) > 0 {
			set := hostedOf(normalized)
			base := len(normalized.Messages)
			for k := range hist {
				hist[k].BeforeMessage += base
				hist[k].Sequence = len(set.History)
				set.History = append(set.History, hist[k])
			}
		}
		normalized.Messages = append(normalized.Messages, msgs...)
	}
	backfillToolResultNames(normalized.Messages)

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
func messagesInboundToOpenAI(i int, role string, contentRaw json.RawMessage) ([]relaymodel.Message, []relaymodel.HostedHistoryItem, *ProtocolError) {
	if role != "user" && role != "assistant" {
		return nil, nil, invalidField("messages["+itoa(i)+"].role",
			"messages role 只能是 user/assistant，收到：%s", role)
	}

	// bare string content
	if s, ok := decodeString(contentRaw); ok {
		return []relaymodel.Message{{Role: role, Content: s}}, nil, nil
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return nil, nil, invalidField("messages["+itoa(i)+"].content", "content 必须是字符串或 block 数组")
	}

	var (
		out      []relaymodel.Message
		hist     []relaymodel.HostedHistoryItem
		curText  []relaymodel.MessageContent
		curCalls []relaymodel.Tool
		// flush emits whatever the current Anthropic turn accumulated. Text and
		// tool_use blocks of one assistant turn belong to ONE OpenAI message:
		// splitting them into a text-only message followed by a tool-calls-only
		// message inserts a second assistant turn the client never sent, and the
		// upstream then rejects the tool_result that follows.
		flush = func() {
			if len(curText) == 0 && len(curCalls) == 0 {
				return
			}
			msg := relaymodel.Message{}
			if len(curCalls) > 0 {
				msg.Role = "assistant"
				msg.ToolCalls = curCalls
			} else {
				msg.Role = role
			}
			if len(curText) > 0 {
				msg.Content = normalizeContentPayload(curText)
			}
			out = append(out, msg)
			curText = nil
			curCalls = nil
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
				return nil, nil, perr
			}
			curText = append(curText, ic)
		case "tool_result":
			flush()
			msg, perr := decodeMessagesToolResult(i, j, blk)
			if perr != nil {
				return nil, nil, perr
			}
			out = append(out, msg)
		case "tool_use":
			if role != "assistant" {
				return nil, nil, invalidField(paramBlock(i, j), "tool_use 只能出现在 assistant 消息中")
			}
			t, perr := decodeMessagesToolUse(i, j, blk)
			if perr != nil {
				return nil, nil, perr
			}
			curCalls = append(curCalls, t)
		case "server_tool_use", "mcp_tool_use":
			// A tool the UPSTREAM ran. It is not a tool_use: there is no call
			// for the client to answer, so it must not become an OpenAI
			// tool_call. It is recorded as a hosted turn to be replayed into
			// whatever protocol the selected channel speaks, and what that
			// protocol cannot carry is a diagnostic rather than a 4xx — the
			// client is replaying a turn the gateway itself handed it.
			if role != "assistant" {
				return nil, nil, invalidField(paramBlock(i, j), "%s 只能出现在 assistant 消息中", bt)
			}
			flush()
			hist = append(hist, claudeHostedCall(blk, len(out)))
		case "web_search_tool_result", "mcp_tool_result":
			flush()
			if !attachHostedResult(hist, jsonStringOf(blk, "tool_use_id"), bt, rawOf(blk, "content")) {
				// A result whose call was not in this transcript: keep it, so
				// it is neither lost nor silently folded into a text block.
				hist = append(hist, relaymodel.HostedHistoryItem{
					Kind:          relaymodel.HostedKindFromClaudeBlock(bt, ""),
					NativeType:    bt,
					ID:            jsonStringOf(blk, "tool_use_id"),
					BeforeMessage: len(out),
				})
			}
		case "document":
			return nil, nil, unsupportedField(paramBlock(i, j), "第一版不支持 document block")
		default:
			return nil, nil, unsupportedField(paramBlock(i, j), "未识别的 content block 类型：%s", bt)
		}
	}
	flush()
	if len(out) == 0 && len(hist) == 0 {
		return nil, nil, invalidField("messages["+itoa(i)+"].content", "content block 数组未解析出任何内容")
	}
	return out, hist, nil
}

// claudeHostedCall records a server-executed tool call from a Claude turn.
func claudeHostedCall(blk map[string]json.RawMessage, before int) relaymodel.HostedHistoryItem {
	blockType := jsonStringOf(blk, "type")
	name := jsonStringOf(blk, "name")
	item := relaymodel.HostedHistoryItem{
		Kind:          relaymodel.HostedKindFromClaudeBlock(blockType, name),
		NativeType:    blockType,
		ID:            jsonStringOf(blk, "id"),
		Name:          name,
		ServerLabel:   jsonStringOf(blk, "server_name"),
		Arguments:     rawOf(blk, "input"),
		BeforeMessage: before,
	}
	if item.Kind == relaymodel.HostedWebSearch {
		item.Action = claudeSearchAction(blk)
	}
	return item
}

// claudeSearchAction normalizes Claude's server_tool_use input into the search
// action both directions share. Action is the richer of the two shapes, so it
// is what the neutral form carries; the renderers narrow it back.
func claudeSearchAction(blk map[string]json.RawMessage) json.RawMessage {
	return relaymodel.HostedSearchAction(
		relaymodel.HostedSearchQueryFromClaudeInput(relaymodel.RawToValue(rawOf(blk, "input"))))
}

// attachHostedResult pairs a hosted result block with the call it answers,
// matching on the call id the way the API does. It reports whether a call was
// found.
func attachHostedResult(hist []relaymodel.HostedHistoryItem, toolUseId, blockType string, content json.RawMessage) bool {
	if toolUseId == "" {
		return false
	}
	isMCP := relaymodel.HostedKindFromClaudeBlock(blockType, "") == relaymodel.HostedMCP
	for k := len(hist) - 1; k >= 0; k-- {
		if hist[k].ID != toolUseId {
			continue
		}
		if isMCP {
			// Claude spells an MCP result as an array holding one text block; the
			// neutral form holds the plain string, which is also what the Responses
			// `output` field it may be sent on to carries. Folding it here, at the
			// boundary, keeps the two directions from disagreeing about the shape.
			hist[k].Output = relaymodel.HostedMCPResultAsString(content)
		} else {
			hist[k].Results = content
		}
		return true
	}
	return false
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

// backfillToolResultNames fills in the function name on role:"tool" messages.
// Anthropic's tool_result block carries only tool_use_id, but the OpenAI Chat
// protocol asks the tool message to name the function as well, and strict
// OpenAI-compatible upstreams reject a multi-turn tool conversation without it.
// The name is recovered from the tool_use block that issued the call, which is
// why this runs over the finished list rather than per message.
func backfillToolResultNames(messages []relaymodel.Message) {
	toolNames := map[string]string{}
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.Id == "" || tc.Function.Name == "" {
				continue
			}
			if _, exists := toolNames[tc.Id]; !exists {
				toolNames[tc.Id] = tc.Function.Name
			}
		}
	}
	if len(toolNames) == 0 {
		return
	}
	for i := range messages {
		msg := &messages[i]
		if msg.Role != "tool" || msg.Name != nil {
			continue
		}
		if name, ok := toolNames[msg.ToolCallId]; ok {
			msg.Name = &name
		}
	}
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
		// A non-empty `type` other than "custom" is a hosted (server-side)
		// tool. This used to be a 4xx. It is carried instead: a client that
		// declares web_search is asking a question the gateway can often
		// answer (the chat target maps it to web_search_options, the Responses
		// target to a web_search tool), and where it cannot, the attach step
		// records the loss rather than the parser guessing.
		if n := jsonStringOf(tool, "type"); n != "" && n != "custom" {
			def := relaymodel.HostedDefinition{
				Kind:       relaymodel.HostedKindFromClaudeToolType(n),
				Execution:  relaymodel.HostedExecutionOf(n),
				NativeType: n,
				Name:       jsonStringOf(tool, "name"),
				Path:       "tools[" + itoa(i) + "]",
			}
			def.WebSearch = claudeWebSearchOptions(tool)
			set := hostedOf(normalized)
			set.Definitions = append(set.Definitions, def)
			continue
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

// claudeWebSearchOptions reads the web-search knobs Claude declares. They are
// read even for a kind that ends up unmapped, so the loss is reported with the
// detail it is losing rather than as a bare type name.
func claudeWebSearchOptions(tool map[string]json.RawMessage) *relaymodel.HostedSearchOptions {
	opts := &relaymodel.HostedSearchOptions{
		SearchContextSize: jsonStringOf(tool, "search_context_size"),
		UserLocation:      relaymodel.HostedUserLocation(rawOf(tool, "user_location")),
		AllowedDomains:    jsonStringListOf(tool, "allowed_domains"),
		BlockedDomains:    jsonStringListOf(tool, "blocked_domains"),
	}
	if raw := rawOf(tool, "max_uses"); len(raw) > 0 {
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			opts.MaxUses = &n
		}
	}
	return opts
}

// hostedByName finds a declared hosted tool by the name a tool_choice names.
func hostedByName(req *relaymodel.GeneralOpenAIRequest, name string) (*relaymodel.HostedDefinition, bool) {
	if name == "" || req.Hosted == nil {
		return nil, false
	}
	for i := range req.Hosted.Definitions {
		if req.Hosted.Definitions[i].Name == name {
			def := req.Hosted.Definitions[i]
			return &def, true
		}
	}
	return nil, false
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
		name := jsonStringOf(m, "name")
		if def, ok := hostedByName(normalized, name); ok {
			// The named tool is a hosted one. A chat-style function tool_choice
			// naming a function the upstream does not have would be worse than
			// degrading, so the forced choice is recorded and the target attach
			// either expresses it natively or downgrades it with a diagnostic.
			hostedOf(normalized).ForcedChoice = def
			break
		}
		normalized.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name},
		}
	case "none":
		normalized.ToolChoice = "none"
	default:
		return unsupportedField("tool_choice.type", "未识别的 tool_choice 类型：%s", jsonStringOf(m, "type"))
	}
	// Anthropic expresses "do not run tool calls in parallel" inside tool_choice;
	// OpenAI expresses it as its own top-level flag. Dropping it let the upstream
	// return parallel calls the client had explicitly refused.
	if raw, ok := m["disable_parallel_tool_use"]; ok {
		var disabled bool
		if err := json.Unmarshal(raw, &disabled); err == nil && disabled {
			no := false
			normalized.ParallelTooCalls = &no
		}
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
	case "content_filter":
		s = "refusal"
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
	// Hosted artifacts come first: in a Claude turn the search precedes the
	// answer, and a server_tool_use the client cannot see the result of is worse
	// than no block at all.
	content := renderMessagesHosted(upstream.Hosted)
	stopReason := "end_turn"
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
		if mapped := finishReasonToStopReason(choice.FinishReason); mapped != nil {
			stopReason = *mapped
		}
		// A turn that produced tool calls stopped for tool use, whatever
		// finish_reason the upstream left behind (some leave it empty);
		// reporting end_turn would tell the client the model is done and orphan
		// the tool_use blocks. A `length` stop is kept: the call was truncated.
		if len(choice.Message.ToolCalls) > 0 && stopReason == "end_turn" {
			stopReason = "tool_use"
		}
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
	return envelope
}

// messagesHostedArtifact is what one neutral artifact becomes on the Claude
// wire: the mcp_tool_use block that opens it, and the mcp_tool_result block that
// closes it once the call has returned.
//
// It is two fields rather than a slice because the two phases of a STREAM are
// exactly the two blocks, and they are rendered at different moments: the call
// block is complete as soon as the input is known, the result block only when
// the call returns. A body, where both are known at once, emits them adjacent.
//
// call is nil when Claude cannot express the artifact at all — the loss is
// already recorded — and result is nil while there is nothing to report yet.
type messagesHostedArtifact struct {
	call   map[string]any
	result map[string]any
}

// messagesHostedArtifactOf renders one artifact's blocks. fallbackID is the
// stand-in id used when the upstream sent none: Claude requires an id on the
// call block and a result that names it, so an id-less artifact is addressed by
// its position rather than shipped in a shape the API would reject.
func messagesHostedArtifactOf(it relaymodel.HostedOutputItem, path, fallbackID string,
	losses lossSink) messagesHostedArtifact {

	if relaymodel.HostedClaudeBlockName(it.Kind) == "" {
		losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedHostedTool, path,
			"Anthropic Messages 无法表达托管工具产物 %s", it.Kind))
		return messagesHostedArtifact{}
	}
	if it.Kind == relaymodel.HostedWebSearch {
		// A web search is the one artifact Claude cannot be handed at all. Its
		// result block is required to carry provider-encrypted search results,
		// which only Anthropic can produce, and a call block without one is not
		// a shape the API accepts. So the whole artifact is dropped and named,
		// which is what the reference implementation does here too.
		losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagWebSearchResponseUnrepresentable, path,
			"上游的 web_search_call 无法转成 Anthropic 的 web_search_tool_result（其 content 必须是 Anthropic 加密签发的搜索结果，网关无法重建），已丢弃该条搜索记录"))
		return messagesHostedArtifact{}
	}
	if it.Name == "" || it.ServerLabel == "" {
		losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPIdentityMissing, path,
			"Anthropic 的 mcp_tool_use 需要 name 与 server_name，上游只给了 name=%q server_label=%q", it.Name, it.ServerLabel))
		return messagesHostedArtifact{}
	}
	id := it.ID
	if id == "" {
		id = fallbackID
	}
	out := messagesHostedArtifact{call: map[string]any{
		"type":        "mcp_tool_use",
		"id":          id,
		"name":        it.Name,
		"server_name": it.ServerLabel,
		"input":       rawObjectOrEmpty(it.Arguments),
	}}
	// A call that reported an outcome gets its result block; one still running
	// has nothing to report yet. An outcome is an output or an error, and the
	// error is the text a failed call has to show — otherwise the result would
	// arrive with is_error set and nothing in it.
	outcome := it.Output
	if it.Failed() && len(outcome) == 0 && it.Error != nil && it.Error.Message != "" {
		outcome = relaymodel.HostedJSONString(it.Error.Message)
	}
	if len(outcome) == 0 {
		if it.Status != relaymodel.HostedStatusInProgress {
			losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPResultMissing, path,
				"上游的 mcp_call 状态为 %q 却既无 output 也无 error，没有可交回的调用结果", it.Status))
		}
		return out
	}
	// Claude spells an MCP result as an array holding one text block; the
	// neutral form holds the plain string both protocols carry it as on the
	// way in, so this is where it is wrapped back up.
	result, ok := relaymodel.HostedMCPResultString(outcome)
	if !ok {
		losses.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagMCPResultUnrepresentable, path+".output",
			"上游给出的 MCP 结果无法降为单个文本块，Anthropic 的 mcp_tool_result 只能承载文本"))
		return out
	}
	out.result = map[string]any{
		"type":        "mcp_tool_result",
		"tool_use_id": id,
		"content":     []map[string]any{{"type": "text", "text": result}},
		"is_error":    it.Failed(),
	}
	return out
}

// renderMessagesHosted turns the upstream's hosted artifacts into the Claude
// content blocks that describe them: a call block and its result block, per
// artifact.
//
// What it cannot express is recorded on the output rather than invented or
// silently dropped, and rendering still succeeds — the response phase does not
// reject under any policy, because the upstream has already done the work and
// failing now would throw away an answer the client paid for.
func renderMessagesHosted(hosted *relaymodel.HostedOutput) []map[string]any {
	if hosted == nil || len(hosted.Items) == 0 {
		return []map[string]any{}
	}
	blocks := make([]map[string]any, 0, len(hosted.Items)*2)
	for i, it := range hosted.Items {
		artifact := messagesHostedArtifactOf(it, pathOfHosted(i), "hosted_"+itoa(i), hosted)
		if artifact.call != nil {
			blocks = append(blocks, artifact.call)
		}
		if artifact.result != nil {
			blocks = append(blocks, artifact.result)
		}
	}
	return blocks
}

// pathOfHosted names a hosted artifact the way a diagnostic should: by where it
// sat in the response, so an operator can find the one that was dropped.
func pathOfHosted(i int) string {
	return "hosted_tools[" + itoa(i) + "]"
}

// lossSink is the surface the two hosted loss channels share: the non-streaming
// HostedOutput, which carries the losses of one rendered body, and the streaming
// HostedLossLog, which carries those of a stream. They are separate types because
// their lifetimes differ — one is per response, one per stream — but a renderer
// records a loss the same way into either, so the code that decides what a loss
// IS can be shared between the two phases.
type lossSink interface {
	AddLoss(d relaymodel.ConversionDiagnostic)
}

// discardLosses swallows diagnostics. It is passed when an artifact is rendered
// a second time — a stream re-renders an artifact as each phase refines it — so
// that a loss already recorded when the artifact was first seen is not written
// to the log once per event.
type discardLosses struct{}

func (discardLosses) AddLoss(relaymodel.ConversionDiagnostic) {}

// rawObjectOrEmpty renders a hosted input/argument field as the object Claude
// expects, for the same reason.
func rawObjectOrEmpty(raw json.RawMessage) any {
	if v := relaymodel.RawToValue(raw); v != nil {
		if obj, ok := v.(map[string]any); ok {
			return obj
		}
	}
	return map[string]any{}
}

// argumentsToInput turns the accumulated argument string of a tool call into the
// object Anthropic expects for tool_use.input. Anything that is not a JSON
// object becomes an empty object: `input` must be an object, and leaking the raw
// text under a synthetic key would hand the client a field its schema never
// declared.
func argumentsToInput(args any) any {
	empty := map[string]any{}
	switch v := args.(type) {
	case nil:
		return empty
	case string:
		if strings.TrimSpace(v) == "" {
			return empty
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil || m == nil {
			return empty
		}
		return m
	case map[string]any:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return empty
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return empty
		}
		return m
	}
}
