package openai_responses

import (
	"fmt"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Response is the part of a Responses answer the gateway reads. Everything the
// gateway does not model is left out deliberately rather than decoded and
// re-encoded, so a field this struct does not know about cannot be half-carried.
type Response struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Model             string             `json:"model"`
	Status            string             `json:"status"`
	CreatedAt         int64              `json:"created_at"`
	Output            []OutputItem       `json:"output"`
	Usage             *Usage             `json:"usage"`
	Error             *ResponseError     `json:"error"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details"`
}

// OutputItem is one item of the answer. A Responses answer is a list of items
// rather than a message with fields: text lives in a "message" item's content
// parts, a tool call is its own "function_call" item.
type OutputItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	// Role/Status/Content belong to a "message" item.
	Role    string          `json:"role"`
	Status  string          `json:"status"`
	Content []OutputContent `json:"content"`
	// Summary/Content of a "reasoning" item, whose text is a display summary of
	// the model's thinking.
	Summary []OutputContent `json:"summary"`
	// CallId/Name/Arguments belong to a "function_call" item.
	CallId string `json:"call_id"`
	Name   string `json:"name"`
	// Arguments is the model's JSON-encoded argument string. It is `any` because
	// a non-conformant upstream may send an object instead of the string the
	// protocol documents; argumentsString handles both.
	Arguments any `json:"arguments"`
}

// OutputContent is one content part of a message item.
type OutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Refusal text arrives as a part of type "refusal" in the same field.
	Refusal string `json:"refusal"`
}

// IncompleteDetails explains a terminal status of "incomplete".
type IncompleteDetails struct {
	Reason string `json:"reason"`
}

// ResponseError is the error object an upstream attaches to a failed answer.
type ResponseError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Usage is the Responses usage object. Both detail objects are pointers so that
// "the upstream did not report cached tokens" stays distinguishable from
// "the upstream reported zero cached tokens" (§4.1 pointer rule).
type Usage struct {
	InputTokens         int                  `json:"input_tokens"`
	OutputTokens        int                  `json:"output_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	InputTokensDetails  *InputTokensDetails  `json:"input_tokens_details"`
	OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details"`
}

type InputTokensDetails struct {
	CachedTokens *int `json:"cached_tokens"`
}

type OutputTokensDetails struct {
	ReasoningTokens *int `json:"reasoning_tokens"`
}

// ToUsage maps the upstream usage object onto the normalized fact. ok is false
// when the upstream reported nothing at all, which is not the claim "reported
// zero": the caller then falls back to its own estimate, labelled as such.
func ToUsage(u *Usage) (*relaymodel.Usage, bool) {
	if u == nil {
		return nil, false
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 && u.InputTokensDetails == nil {
		return nil, false
	}
	out := relaymodel.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		// Recomputed rather than taken from the upstream's own total_tokens: the
		// aggregation layer sums prompt+completion, so a total that disagreed
		// would be an inconsistency this gateway invented a store for.
		TotalTokens: u.InputTokens + u.OutputTokens,
		Source:      relaymodel.UsageSourceReported,
	}
	if u.InputTokensDetails != nil {
		// input_tokens_details.cached_tokens is the Responses spelling of the
		// cache read count; NormalizeUsage folds it into CacheReadInputTokens and
		// keeps "absent" (nil details) as not_reported.
		out.InputTokensDetails = &relaymodel.InputTokensDetails{CachedTokens: u.InputTokensDetails.CachedTokens}
	}
	if u.OutputTokensDetails != nil {
		out.CompletionTokensDetails = &relaymodel.CompletionTokensDetails{
			ReasoningTokens: derefOr(u.OutputTokensDetails.ReasoningTokens),
		}
	}
	relaymodel.NormalizeUsage(&out)
	return &out, true
}

func derefOr(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// Failure returns the upstream's own error when the answer reports one. A failed
// answer can still arrive with HTTP 200, so the status is worth checking before
// the body is read as a success.
func (r *Response) Failure() *relaymodel.Error {
	if r == nil || r.Status != "failed" {
		return nil
	}
	e := &relaymodel.Error{Message: "upstream response failed", Type: "upstream_error"}
	if r.Error != nil {
		if r.Error.Message != "" {
			e.Message = r.Error.Message
		}
		if r.Error.Type != "" {
			e.Type = r.Error.Type
		}
		e.Code = r.Error.Code
	}
	return e
}

// answer is the decoded answer: the assistant text, any reasoning summary, the
// tool calls, and the output item types this gateway could not place.
type answer struct {
	text      string
	reasoning string
	calls     []relaymodel.Tool
	unmapped  []string
}

// decode walks the output items in order. Item order is the order the model
// produced them, which is the order the chat envelope's tool_calls must keep.
//
// An output item of a type this gateway does not model (a hosted tool call, say)
// is reported and skipped: the answer text and the function calls around it are
// still delivered, which is why an unmapped item is a log entry rather than a
// failed request here — unlike a function call missing its id or name, which the
// client could not act on at all and which is refused by toolCall.
func (r *Response) decode() (answer, *relaymodel.Error) {
	var out answer
	var text, reasoning strings.Builder
	for _, item := range r.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					text.WriteString(part.Text)
				case "refusal":
					// The chat envelope's message has no refusal slot, so a refusal
					// is carried as the answer's text: the model's words reach the
					// client, only the label is lost.
					logger.SysWarnf("responses upstream returned a refusal; carried as message content (item=%s)", item.ID)
					text.WriteString(part.Refusal)
				default:
					out.unmapped = append(out.unmapped, "message.content."+part.Type)
				}
			}
		case "reasoning":
			for _, part := range item.Summary {
				reasoning.WriteString(part.Text)
			}
			for _, part := range item.Content {
				reasoning.WriteString(part.Text)
			}
		case "function_call":
			call, err := item.toolCall()
			if err != nil {
				return out, err
			}
			out.calls = append(out.calls, call)
		case "":
			// An item with no type is not a statement the gateway can classify.
			out.unmapped = append(out.unmapped, "output_item.<empty type>")
		default:
			out.unmapped = append(out.unmapped, "output_item."+item.Type)
		}
	}
	out.text = text.String()
	out.reasoning = reasoning.String()
	return out, nil
}

// toolCall converts a function_call item into the normalized tool call.
func (item OutputItem) toolCall() (relaymodel.Tool, *relaymodel.Error) {
	id := item.CallId
	if id == "" {
		// call_id is what a client echoes back as tool_call_id; without it the
		// exchange cannot be resolved later. The item id is the closest stand-in an
		// upstream can offer, so it is used and the substitution is recorded.
		id = item.ID
		logger.SysErrorf("responses upstream function_call %s has no call_id; using the item id", item.ID)
	}
	if id == "" {
		return relaymodel.Tool{}, &relaymodel.Error{
			Message: "上游 Responses 返回的 function_call 既没有 call_id 也没有 id，无法构造工具调用",
			Type:    "upstream_error",
			Code:    unmappedItemCode,
		}
	}
	if item.Name == "" {
		return relaymodel.Tool{}, &relaymodel.Error{
			Message: fmt.Sprintf("上游 Responses 返回的 function_call %s 缺少函数名", id),
			Type:    "upstream_error",
			Code:    unmappedItemCode,
		}
	}
	return relaymodel.Tool{
		Id:   id,
		Type: "function",
		Function: relaymodel.Function{
			Name:      item.Name,
			Arguments: argumentsString(item.Arguments),
		},
	}, nil
}

// unmappedItemCode names the refusal for upstream output this gateway cannot
// carry into the chat envelope. The alternative — forwarding the request as if
// the answer were complete — would hand the client a truncated reply.
const unmappedItemCode = "responses_upstream_unmapped_item"

// ToChatResponse renders a Responses answer as the normalized chat envelope the
// client-protocol renderers and the chat entry point read.
//
// A silent upstream is estimated here (promptFallback is the gateway's own count
// of the request) rather than recorded as a zero; a reported answer keeps the
// upstream's numbers, including a reported zero. The returned error is non-nil
// only when the upstream produced output the chat envelope cannot express.
func ToChatResponse(resp *Response, modelName string, promptFallback int) (*openai.TextResponse, *relaymodel.Error) {
	decoded, decodeErr := resp.decode()
	if decodeErr != nil {
		return nil, decodeErr
	}
	for _, kind := range decoded.unmapped {
		logger.SysWarnf("responses upstream produced output this gateway cannot carry into a chat envelope: %s", kind)
	}

	message := relaymodel.Message{Role: "assistant", Content: decoded.text}
	if decoded.text == "" {
		message.Content = nil
	}
	if decoded.reasoning != "" {
		message.ReasoningContent = decoded.reasoning
	}
	message.ToolCalls = decoded.calls

	textResponse := &openai.TextResponse{
		Id:      ChatIDFor(resp.ID),
		Object:  "chat.completion",
		Created: resp.createdAt(),
		Model:   firstNonEmpty(resp.Model, modelName),
		Choices: []openai.TextResponseChoice{{
			Index:        0,
			Message:      message,
			FinishReason: finishReasonFor(resp, len(decoded.calls) > 0),
		}},
	}

	if usage, ok := ToUsage(resp.Usage); ok {
		textResponse.Usage = *usage
		return textResponse, nil
	}
	// The upstream said nothing about cost, which is not the claim "reported
	// zero": the answer is counted locally and the fact is labelled estimated.
	// Tool arguments count too, so a tool-only answer is not recorded as empty.
	var counted strings.Builder
	counted.WriteString(decoded.text)
	for _, call := range decoded.calls {
		counted.WriteString(argumentsString(call.Function.Arguments))
	}
	completion := openai.CountTokenText(counted.String(), modelName)
	estimated := relaymodel.Usage{
		PromptTokens:     promptFallback,
		CompletionTokens: completion,
		TotalTokens:      promptFallback + completion,
		Source:           relaymodel.UsageSourceEstimated,
	}
	relaymodel.NormalizeUsage(&estimated)
	textResponse.Usage = estimated
	return textResponse, nil
}

// finishReasonFor maps the Responses terminal status onto an OpenAI
// finish_reason. A tool call is reported as tool_calls even when the upstream
// said "completed", because that is what a chat client has to act on.
func finishReasonFor(resp *Response, hasToolCall bool) string {
	switch {
	case hasToolCall:
		return "tool_calls"
	case resp.Status == "incomplete":
		if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "content_filter" {
			return "content_filter"
		}
		return "length"
	}
	return "stop"
}

func (r *Response) createdAt() int64 {
	if r.CreatedAt > 0 {
		return r.CreatedAt
	}
	return time.Now().Unix()
}

// ChatIDFor turns a Responses id into the chat completion id a chat client
// expects. It is the inverse of relay/protocol's responseID, so an id that
// travelled out as resp_<x> comes back as chatcmpl-<x> and returns to resp_<x>
// if the client relays it again.
func ChatIDFor(responseID string) string {
	if responseID == "" {
		return ""
	}
	if strings.HasPrefix(responseID, "chatcmpl-") {
		return responseID
	}
	return "chatcmpl-" + strings.TrimPrefix(responseID, "resp_")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
