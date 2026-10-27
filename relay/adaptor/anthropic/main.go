package anthropic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/songquanpeng/one-api/common/render"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/image"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/model"
)

func stopReasonClaude2OpenAI(reason *string) string {
	if reason == nil {
		return ""
	}
	switch *reason {
	case "end_turn":
		return "stop"
	case "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "pause_turn":
		// Claude's resumable server-side loop has no OpenAI equivalent; the
		// Responses protocol reports it as an incomplete turn, not a clean stop.
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return *reason
	}
}

func ConvertRequest(textRequest model.GeneralOpenAIRequest) *Request {
	claudeTools := make([]Tool, 0, len(textRequest.Tools))

	for _, tool := range textRequest.Tools {
		if tool.Function.Name == "" {
			// An unnamed function tool cannot be called by name and Anthropic
			// rejects the definition outright.
			continue
		}
		params := tool.Function.Parameters
		if params == nil {
			// Anthropic requires input_schema; an object schema is the only
			// safe default when the client declared none.
			params = map[string]any{"type": "object"}
		}
		claudeTools = append(claudeTools, Tool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: params,
			Strict:      tool.Function.Strict,
		})
	}

	// Hosted declarations share the tools array with function tools; Anthropic
	// tells them apart by `type`. A kind Anthropic cannot declare is recorded
	// rather than dropped in silence.
	for _, tool := range hostedClaudeTools(textRequest.Hosted) {
		claudeTools = append(claudeTools, tool)
	}

	claudeRequest := Request{
		Model:       textRequest.Model,
		MaxTokens:   textRequest.MaxTokens,
		Temperature: textRequest.Temperature,
		TopP:        textRequest.TopP,
		TopK:        textRequest.TopK,
		Stream:      textRequest.Stream,
		Tools:       claudeTools,
		// OpenAI `stop` maps onto Anthropic stop_sequences. Without this the
		// client's stop sequences were silently dropped for Claude upstreams.
		StopSequences: stopSequencesOf(textRequest),
	}
	if len(claudeTools) > 0 {
		claudeRequest.ToolChoice = claudeToolChoice(textRequest, claudeTools)
	}
	if claudeRequest.MaxTokens == 0 {
		claudeRequest.MaxTokens = 4096
	}
	// legacy model name mapping
	if claudeRequest.Model == "claude-instant-1" {
		claudeRequest.Model = "claude-instant-1.1"
	} else if claudeRequest.Model == "claude-2" {
		claudeRequest.Model = "claude-2.1"
	}
	// A replayed hosted turn goes back in at the boundary it fell on, in
	// arrival order. Anthropic puts the call and its result inside the
	// assistant turn that follows, so an inserted assistant message is merged
	// into its neighbour by mergeAdjacentClaudeMessages below.
	nextHosted := 0
	replayHosted := func(before int) {
		set := textRequest.Hosted
		for set != nil && nextHosted < len(set.History) && set.History[nextHosted].BeforeMessage <= before {
			h := set.History[nextHosted]
			nextHosted++
			blocks := hostedHistoryBlocks(h)
			if len(blocks) == 0 {
				set.AddLoss(model.NewHostedLoss(model.DiagUnsupportedHostedHistory, "messages",
					"Anthropic Messages 无法回放托管工具历史（类型 %s）", h.NativeType))
				continue
			}
			if h.Kind == model.HostedWebSearch && len(h.Results) == 0 {
				// The source carried the call but not its results (a Responses
				// web_search_call has no results slot), and Anthropic rejects a
				// server_tool_use with no matching result. The API's own
				// "unavailable" error result is the honest stand-in.
				set.AddLoss(model.NewHostedLoss(model.DiagSearchContinuationLost, "messages",
					"托管搜索结果缺失，已用 web_search_tool_result_error(unavailable) 占位"))
			}
			claudeRequest.Messages = append(claudeRequest.Messages, Message{Role: "assistant", Content: blocks})
		}
	}

	for i, message := range textRequest.Messages {
		replayHosted(i)
		if message.Role == "system" {
			// Anthropic carries one top-level system string; later system
			// messages are folded in rather than emitted as role:"system"
			// messages, which the API rejects.
			if text := message.StringContent(); text != "" {
				if claudeRequest.System == "" {
					claudeRequest.System = text
				} else {
					claudeRequest.System += "\n\n" + text
				}
			}
			continue
		}
		claudeMessage := Message{}
		if message.Role == "tool" {
			// A tool result becomes the tool_result block of a user message.
			claudeMessage.Role = "user"
			claudeMessage.Content = append(claudeMessage.Content, Content{
				Type:      "tool_result",
				Content:   message.StringContent(),
				ToolUseId: message.ToolCallId,
			})
		} else {
			claudeMessage.Role = message.Role
			if message.IsStringContent() {
				if text := message.StringContent(); text != "" {
					claudeMessage.Content = append(claudeMessage.Content, Content{
						Type: "text",
						Text: text,
					})
				}
			} else {
				for _, part := range message.ParseContent() {
					switch part.Type {
					case model.ContentTypeText:
						claudeMessage.Content = append(claudeMessage.Content, Content{
							Type: "text",
							Text: part.Text,
						})
					case model.ContentTypeImageURL:
						if part.ImageURL == nil {
							continue
						}
						mimeType, data, err := image.GetImageFromUrl(part.ImageURL.Url)
						if err != nil || data == "" {
							// Anthropic can only take inline base64 image data;
							// an unreachable URL is dropped here rather than sent
							// as an empty source the upstream would reject.
							logger.SysError("anthropic: dropping unsupported image url: " + part.ImageURL.Url)
							continue
						}
						claudeMessage.Content = append(claudeMessage.Content, Content{
							Type: "image",
							Source: &ImageSource{
								Type:      "base64",
								MediaType: mimeType,
								Data:      data,
							},
						})
					}
				}
			}
		}
		// Tool calls live on the message regardless of how its content was
		// shaped. This used to sit inside the IsStringContent branch, so an
		// assistant turn written the standard OpenAI way — content null with
		// tool_calls — lost every tool_use block and left the following
		// tool_result without its matching call.
		for _, toolCall := range message.ToolCalls {
			claudeMessage.Content = append(claudeMessage.Content, Content{
				Type:  "tool_use",
				Id:    toolCall.Id,
				Name:  toolCall.Function.Name,
				Input: toolCallInput(toolCall.Function.Arguments),
			})
		}
		if len(claudeMessage.Content) == 0 {
			// Anthropic rejects an empty content array, so a message that
			// carried nothing convertible is skipped instead of sent blank.
			continue
		}
		claudeRequest.Messages = append(claudeRequest.Messages, claudeMessage)
	}
	replayHosted(len(textRequest.Messages))
	claudeRequest.Messages = mergeAdjacentClaudeMessages(claudeRequest.Messages)
	return &claudeRequest
}

// hostedClaudeTools renders the hosted declarations a client asked for as
// Anthropic tool definitions, recording the kinds Anthropic cannot declare.
// No protocol's MCP *declaration* form is mapped — the reference
// implementation drops those too — so only web search is emitted here.
func hostedClaudeTools(set *model.HostedSet) []Tool {
	if set == nil {
		return nil
	}
	var out []Tool
	for _, def := range set.Definitions {
		t := model.HostedClaudeDeclarationType(def.Kind)
		if t == "" {
			set.AddLoss(model.UnsupportedHostedLoss("Anthropic Messages", def))
			continue
		}
		tool := Tool{Type: t, Name: model.HostedClaudeBlockName(def.Kind)}
		if def.WebSearch != nil {
			tool.MaxUses = def.WebSearch.MaxUses
			tool.AllowedDomains = def.WebSearch.AllowedDomains
			tool.BlockedDomains = def.WebSearch.BlockedDomains
			// Claude's user_location is the flat spelling, which is the neutral
			// one, so it crosses over untouched.
			tool.UserLocation = model.RawToValue(def.WebSearch.UserLocation)
			if def.WebSearch.SearchContextSize != "" {
				// Claude's web_search has no context-size knob. It is reported
				// rather than dropped: it changes how much the upstream reads, so
				// an operator who set it deserves to know it did not apply.
				set.AddLoss(model.NewHostedLoss(model.DiagUnsupportedSearchLimit, def.Path,
					"Anthropic Messages 的 web_search 没有 search_context_size 开关（收到 %q）",
					def.WebSearch.SearchContextSize))
			}
		}
		out = append(out, tool)
	}
	if set.ForcedChoice != nil {
		if t := model.HostedClaudeDeclarationType(set.ForcedChoice.Kind); t != "" {
			out = append(out, Tool{Type: t, Name: model.HostedClaudeBlockName(set.ForcedChoice.Kind)})
		} else {
			set.AddLoss(model.NewHostedLoss(model.DiagForcedChoiceDowngraded, set.ForcedChoice.Path,
				"Anthropic Messages 无法强制使用托管工具 %s，已降级为自动选择",
				model.DescribeHosted(*set.ForcedChoice)))
		}
	}
	return out
}

// hostedHistoryBlocks renders one replayed hosted turn as Anthropic content
// blocks: the server_tool_use that ran it and the result block that answers it.
func hostedHistoryBlocks(h model.HostedHistoryItem) []Content {
	switch h.Kind {
	case model.HostedWebSearch:
		result := model.RawToValue(h.Results)
		if result == nil {
			result = []any{map[string]any{
				"type":       "web_search_tool_result_error",
				"error_code": "unavailable",
			}}
		}
		return []Content{
			{Type: "server_tool_use", Id: h.ID, Name: "web_search", Input: model.HostedClaudeSearchInput(h.Action)},
			{Type: "web_search_tool_result", ToolUseId: h.ID, Content: result},
		}
	case model.HostedMCP:
		return []Content{
			{Type: "mcp_tool_use", Id: h.ID, Name: h.Name, ServerName: h.ServerLabel,
				Input: model.RawToValue(h.Arguments)},
			{Type: "mcp_tool_result", ToolUseId: h.ID, Content: model.RawToValue(h.Output)},
		}
	default:
		return nil
	}
}

// stopSequencesOf reads the OpenAI `stop` field, which the spec allows to be
// either a single string or an array of strings.
func stopSequencesOf(textRequest model.GeneralOpenAIRequest) []string {
	switch v := textRequest.Stop.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// claudeToolChoice maps the OpenAI tool_choice onto Anthropic's object form.
// Anthropic defaults to auto when tools are present, which is what the OpenAI
// absent/`auto` cases mean.
func claudeToolChoice(textRequest model.GeneralOpenAIRequest, tools []Tool) ToolChoice {
	claudeToolChoice := ToolChoice{Type: "auto"}
	switch choice := textRequest.ToolChoice.(type) {
	case string:
		switch choice {
		case "required", "any":
			claudeToolChoice.Type = "any"
		case "none":
			claudeToolChoice.Type = "none"
		case "auto":
			claudeToolChoice.Type = "auto"
		}
	case map[string]any:
		// {"type":"function","function":{"name":...}} and the flat
		// {"type":"tool","name":...} shape both name one function.
		name, _ := choice["name"].(string)
		if function, ok := choice["function"].(map[string]any); ok {
			if v, ok := function["name"].(string); ok {
				name = v
			}
		}
		if name != "" {
			claudeToolChoice.Type = "tool"
			claudeToolChoice.Name = name
		}
	}
	if textRequest.ParallelTooCalls != nil && !*textRequest.ParallelTooCalls {
		disabled := true
		claudeToolChoice.DisableParallelToolUse = &disabled
	}
	return claudeToolChoice
}

// toolCallInput turns the accumulated argument string of a tool call into the
// object Anthropic expects for tool_use.input. Anything not a JSON object —
// empty, malformed, or a non-object literal — becomes an empty object, because
// `input: null` and scalar inputs are both rejected by the API.
func toolCallInput(arguments any) map[string]any {
	empty := map[string]any{}
	switch v := arguments.(type) {
	case nil:
		return empty
	case string:
		if strings.TrimSpace(v) == "" {
			return empty
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(v), &input); err != nil || input == nil {
			return empty
		}
		return input
	case map[string]any:
		return v
	default:
		// A non-string argument value (some clients send the object directly)
		// is round-tripped through JSON so it lands as a plain map.
		raw, err := json.Marshal(v)
		if err != nil {
			return empty
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil || input == nil {
			return empty
		}
		return input
	}
}

// mergeAdjacentClaudeMessages folds consecutive same-role messages into one.
// Anthropic requires the turns to alternate, and the OpenAI shape routinely
// produces runs (several role:"tool" messages after a parallel tool call, a
// user text turn following them) that would otherwise be rejected.
func mergeAdjacentClaudeMessages(messages []Message) []Message {
	merged := make([]Message, 0, len(messages))
	for _, message := range messages {
		if n := len(merged); n > 0 && merged[n-1].Role == message.Role {
			merged[n-1].Content = append(merged[n-1].Content, message.Content...)
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

// StreamState carries the per-stream tool bookkeeping that translating Anthropic
// content blocks into OpenAI tool_calls deltas needs. OpenAI identifies a tool
// call by a dense per-message `index` while Anthropic identifies it by content
// block index, and Anthropic may announce the id/name in one event and stream
// the arguments in later ones — both have to be reconciled across events, so
// the state outlives one StreamResponseClaude2OpenAI call.
type StreamState struct {
	// toolIndex maps an Anthropic content block index onto the dense OpenAI
	// tool_calls index. Text/thinking blocks occupy content block indexes but
	// are not tool calls, so the OpenAI index cannot be the block index.
	toolIndex map[int]int
	// args accumulates the argument fragments per content block index, so a
	// call that streamed no arguments at all can still be closed with `{}`.
	args map[int]*strings.Builder
	next int
}

// NewStreamState creates the per-stream state for one response.
func NewStreamState() *StreamState {
	return &StreamState{
		toolIndex: map[int]int{},
		args:      map[int]*strings.Builder{},
	}
}

// toolSlot returns the OpenAI tool_calls index for an Anthropic content block,
// assigning the next free one on first sight.
func (s *StreamState) toolSlot(blockIndex int) int {
	if idx, ok := s.toolIndex[blockIndex]; ok {
		return idx
	}
	idx := s.next
	s.next++
	s.toolIndex[blockIndex] = idx
	s.args[blockIndex] = &strings.Builder{}
	return idx
}

// RewriteEmptyToolArguments returns a delta carrying `{}` for every tool call
// that streamed no argument fragment, so a client accumulating arguments never
// receives an empty string it cannot parse. It is emitted on the terminal
// event, right before finish_reason.
func (s *StreamState) RewriteEmptyToolArguments() []model.Tool {
	tools := make([]model.Tool, 0, len(s.toolIndex))
	for blockIndex, idx := range s.toolIndex {
		b := s.args[blockIndex]
		if b == nil || b.Len() > 0 {
			continue
		}
		index := idx
		tools = append(tools, model.Tool{
			Index: &index,
			Function: model.Function{
				Arguments: "{}",
			},
		})
	}
	if len(tools) == 0 {
		return nil
	}
	// Stable order keeps the emitted deltas deterministic across runs.
	sort.Slice(tools, func(i, j int) bool { return *tools[i].Index < *tools[j].Index })
	return tools
}

// https://docs.anthropic.com/claude/reference/messages-streaming
func StreamResponseClaude2OpenAI(state *StreamState, claudeResponse *StreamResponse) (*openai.ChatCompletionsStreamResponse, *Response) {
	var response *Response
	var responseText string
	var stopReason string
	tools := make([]model.Tool, 0)

	switch claudeResponse.Type {
	case "message_start":
		return nil, claudeResponse.Message
	case "content_block_start":
		if claudeResponse.ContentBlock != nil {
			responseText = claudeResponse.ContentBlock.Text
			if claudeResponse.ContentBlock.Type == "tool_use" {
				index := state.toolSlot(claudeResponse.Index)
				tools = append(tools, model.Tool{
					Index: &index,
					Id:    claudeResponse.ContentBlock.Id,
					Type:  "function",
					Function: model.Function{
						Name:      claudeResponse.ContentBlock.Name,
						Arguments: "",
					},
				})
			}
		}
	case "content_block_delta":
		if claudeResponse.Delta != nil {
			responseText = claudeResponse.Delta.Text
			if claudeResponse.Delta.Type == "input_json_delta" {
				index := state.toolSlot(claudeResponse.Index)
				if b := state.args[claudeResponse.Index]; b != nil {
					b.WriteString(claudeResponse.Delta.PartialJson)
				}
				tools = append(tools, model.Tool{
					Index: &index,
					Function: model.Function{
						Arguments: claudeResponse.Delta.PartialJson,
					},
				})
			}
		}
	case "message_delta":
		if claudeResponse.Usage != nil {
			response = &Response{
				Usage: *claudeResponse.Usage,
			}
		}
		if claudeResponse.Delta != nil && claudeResponse.Delta.StopReason != nil {
			stopReason = *claudeResponse.Delta.StopReason
		}
		// The terminal event is where a tool call that produced no arguments
		// gets its `{}`.
		tools = append(tools, state.RewriteEmptyToolArguments()...)
	}
	var choice openai.ChatCompletionsStreamResponseChoice
	choice.Delta.Content = responseText
	if len(tools) > 0 {
		choice.Delta.Content = nil // compatible with other OpenAI derivative applications, like LobeOpenAICompatibleFactory ...
		choice.Delta.ToolCalls = tools
	}
	choice.Delta.Role = "assistant"
	// finish_reason must only be set when the turn actually ended: every other
	// chunk carries nil, because OpenAI clients treat any non-null finish_reason
	// as the end of the stream (the old code emitted "" on every chunk).
	if finishReason := stopReasonClaude2OpenAI(&stopReason); finishReason != "" {
		choice.FinishReason = &finishReason
	}
	var openaiResponse openai.ChatCompletionsStreamResponse
	openaiResponse.Object = "chat.completion.chunk"
	openaiResponse.Choices = []openai.ChatCompletionsStreamResponseChoice{choice}
	return &openaiResponse, response
}

func ResponseClaude2OpenAI(claudeResponse *Response) *openai.TextResponse {
	// Anthropic returns the answer as an ordered block list that may hold
	// several text blocks around the tool calls; every text block has to be
	// concatenated, not just the first one.
	var responseText strings.Builder
	tools := make([]model.Tool, 0)
	for _, v := range claudeResponse.Content {
		if v.Type == "tool_use" {
			args, _ := json.Marshal(v.Input)
			tools = append(tools, model.Tool{
				Id:   v.Id,
				Type: "function", // compatible with other OpenAI derivative applications
				Function: model.Function{
					Name:      v.Name,
					Arguments: string(args),
				},
			})
			continue
		}
		if v.Text != "" {
			responseText.WriteString(v.Text)
		}
	}
	text := responseText.String()
	// OpenAI clients require a finish_reason; an unmapped or absent upstream
	// stop_reason becomes tool_calls when calls were returned, else stop.
	finishReason := stopReasonClaude2OpenAI(claudeResponse.StopReason)
	if finishReason == "" {
		if len(tools) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}
	choice := openai.TextResponseChoice{
		Index: 0,
		Message: model.Message{
			Role:      "assistant",
			Content:   text,
			Name:      nil,
			ToolCalls: tools,
		},
		FinishReason: finishReason,
	}
	fullTextResponse := openai.TextResponse{
		Id:      fmt.Sprintf("chatcmpl-%s", claudeResponse.Id),
		Model:   claudeResponse.Model,
		Object:  "chat.completion",
		Created: helper.GetTimestamp(),
		Choices: []openai.TextResponseChoice{choice},
	}
	return &fullTextResponse
}

func StreamHandler(c *gin.Context, resp *http.Response) (*model.ErrorWithStatusCode, *model.Usage) {
	createdTime := helper.GetTimestamp()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := strings.Index(string(data), "\n"); i >= 0 {
			return i + 1, data[0:i], nil
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	})

	common.SetEventStreamHeaders(c)

	var usage model.Usage
	var modelName string
	var id string
	// The tool call index/argument bookkeeping spans the whole stream, so it
	// lives outside the per-event conversion.
	streamState := NewStreamState()

	for scanner.Scan() {
		data := scanner.Text()
		if len(data) < 6 || !strings.HasPrefix(data, "data:") {
			continue
		}
		data = strings.TrimPrefix(data, "data:")
		data = strings.TrimSpace(data)

		var claudeResponse StreamResponse
		err := json.Unmarshal([]byte(data), &claudeResponse)
		if err != nil {
			logger.SysError("error unmarshalling stream response: " + err.Error())
			continue
		}

		response, meta := StreamResponseClaude2OpenAI(streamState, &claudeResponse)
		if AccumulateStreamUsage(&usage, &claudeResponse) && meta != nil && len(meta.Id) > 0 {
			// Only message_start carries an id; every other usage-bearing event is
			// a finish_reason delta that still has to be rendered below.
			modelName = meta.Model
			id = fmt.Sprintf("chatcmpl-%s", meta.Id)
			continue
		}
		if response == nil {
			continue
		}

		response.Id = id
		response.Model = modelName
		response.Created = createdTime

		err = render.ObjectData(c, response)
		if err != nil {
			logger.SysError(err.Error())
		}
	}

	if err := scanner.Err(); err != nil {
		logger.SysError("error reading stream: " + err.Error())
	}

	render.Done(c)

	err := resp.Body.Close()
	if err != nil {
		return openai.ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	return nil, &usage
}

func Handler(c *gin.Context, resp *http.Response, promptTokens int, modelName string) (*model.ErrorWithStatusCode, *model.Usage) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return openai.ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	var claudeResponse Response
	err = json.Unmarshal(responseBody, &claudeResponse)
	if err != nil {
		return openai.ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}
	if claudeResponse.Error.Type != "" {
		return &model.ErrorWithStatusCode{
			Error: model.Error{
				Message: claudeResponse.Error.Message,
				Type:    claudeResponse.Error.Type,
				Param:   "",
				Code:    claudeResponse.Error.Type,
			},
			StatusCode: resp.StatusCode,
		}, nil
	}
	fullTextResponse := ResponseClaude2OpenAI(&claudeResponse)
	fullTextResponse.Model = modelName
	usage := RelayUsage(claudeResponse.Usage)
	fullTextResponse.Usage = usage
	jsonResponse, err := json.Marshal(fullTextResponse)
	if err != nil {
		return openai.ErrorWrapper(err, "marshal_response_body_failed", http.StatusInternalServerError), nil
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = c.Writer.Write(jsonResponse)
	return nil, &usage
}

// RelayUsage converts one Anthropic usage block into the normalized usage fact.
// The cache fields keep their pointer identity: an absent field stays nil and is
// never coerced to a zero cache hit (§8.1/§8.2).
//
// PromptTokens is TOTAL input. Anthropic's input_tokens excludes cache tokens
// (a cache read is billed/reported beside it, not inside it), while the
// normalized fact uses the OpenAI meaning where prompt_tokens includes them.
// Adding the cache fields here is what makes the stored fact and the cache hit
// rate comparable across upstream families; without it a cache read could even
// exceed the residual input and be misclassified as invalid by NormalizeUsage.
func RelayUsage(u Usage) model.Usage {
	promptTokens := anthropicTotalInputTokens(u)
	out := model.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      promptTokens + u.OutputTokens,
		// §8.1: Anthropic cache_read -> normalized cache_read, cache_creation ->
		// normalized cache_write.
		CacheReadInputTokens:  i64Ptr(u.CacheReadInputTokens),
		CacheWriteInputTokens: i64Ptr(u.CacheCreationInputTokens),
		// These numbers came from the upstream body, so they are reported.
		Source: model.UsageSourceReported,
	}
	model.NormalizeUsage(&out)
	return out
}

// anthropicTotalInputTokens applies the normalized total-input meaning to an
// Anthropic usage block. Absent cache fields contribute nothing and stay absent
// on the way out; this only decides the scalar sum.
func anthropicTotalInputTokens(u Usage) int {
	total := u.InputTokens
	if u.CacheReadInputTokens != nil {
		total += *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		total += *u.CacheCreationInputTokens
	}
	return total
}

// normalizedCacheTokens returns the cache tokens already adopted into acc. It
// mirrors RelayUsage's sum so a stream delta that repeats the input side is
// totalized against the cache values accumulated from earlier events.
func normalizedCacheTokens(u model.Usage) int {
	total := 0
	if u.CacheReadInputTokens != nil {
		total += int(*u.CacheReadInputTokens)
	}
	if u.CacheWriteInputTokens != nil {
		total += int(*u.CacheWriteInputTokens)
	}
	return total
}

// AccumulateStreamUsage folds one Anthropic stream event's usage into acc and
// reports whether the event carried usage at all. It is shared by the chat
// adaptor's StreamHandler and by the same-protocol passthrough, so both paths
// obey one rule instead of two.
//
// The rule is subtle and is the whole reason this lives in one place:
//   - message_start reports the input side and announces a *running* output
//     count, which the terminal delta supersedes.
//   - message_delta's usage.output_tokens is the *cumulative* count for the
//     whole message, so it is assigned, never added. Summing the events counts
//     the completion twice (message_start announces output_tokens: 1, hence the
//     classic off-by-one over-count of every Anthropic stream).
//
// Cache fields are adopted only when the event carries them: an absent field
// keeps its previous value and stays nil, never a fabricated 0.
func AccumulateStreamUsage(acc *model.Usage, ev *StreamResponse) bool {
	switch {
	case ev.Message != nil:
		u := RelayUsage(ev.Message.Usage)
		acc.PromptTokens = u.PromptTokens
		acc.CompletionTokens = u.CompletionTokens
		acc.TotalTokens = u.TotalTokens
		acc.CacheReadInputTokens = u.CacheReadInputTokens
		acc.CacheWriteInputTokens = u.CacheWriteInputTokens
		acc.Source = model.UsageSourceReported
		return true
	case ev.Usage != nil:
		// Cumulative, not incremental. Adopt the cache fields first: an input
		// count in this event is the Anthropic residual, so the total has to be
		// rebuilt from the cache values the stream has reported so far.
		if v := i64Ptr(ev.Usage.CacheReadInputTokens); v != nil {
			acc.CacheReadInputTokens = v
		}
		if v := i64Ptr(ev.Usage.CacheCreationInputTokens); v != nil {
			acc.CacheWriteInputTokens = v
		}
		// Input is not repeated here, so it is only taken when the event
		// actually states it.
		if ev.Usage.InputTokens > 0 {
			acc.PromptTokens = ev.Usage.InputTokens + normalizedCacheTokens(*acc)
		}
		acc.CompletionTokens = ev.Usage.OutputTokens
		acc.TotalTokens = acc.PromptTokens + acc.CompletionTokens
		acc.Source = model.UsageSourceReported
		return true
	}
	return false
}

// i64Ptr converts an Anthropic *int usage field to the normalized *int64 cache
// field, preserving nil so "not reported" is never coerced to a zero cache hit
// (§8.1/§8.2).
func i64Ptr(v *int) *int64 {
	if v == nil {
		return nil
	}
	out := int64(*v)
	return &out
}
