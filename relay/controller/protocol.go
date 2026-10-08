package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// protocolEntry describes one client-protocol HTTP entry point: how to parse
// its request into the normalized GeneralOpenAIRequest and how to render an
// upstream OpenAI chat.completion back into that protocol. The relay pipeline
// itself (distributor -> adaptor -> upstream -> usage recorder) is shared and
// never sees a client-specific structure (implementation doc §6.1).
type protocolEntry struct {
	Name   string
	Parse  func([]byte) (*relaymodel.GeneralOpenAIRequest, *protocol.ProtocolError)
	Stream func() *protocol.ProtocolError
	Render func(*openai.TextResponse, string) map[string]any
}

// relayProtocolHelper is the shared body for /v1/responses and /v1/messages.
// Stage 3 wires non-streaming round-trips against OpenAI-compatible upstreams;
// streaming SSE is finalized in stage 4 and rejected here with a named error
// rather than silently degraded (§6.4, §7).
func relayProtocolHelper(c *gin.Context, entry *protocolEntry) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)

	body, err := common.GetRequestBody(c)
	if err != nil {
		return openai.ErrorWrapper(err, "read_request_body_failed", http.StatusBadRequest)
	}

	normalized, perr := entry.Parse(body)
	if perr != nil {
		// Request never became a usable generation; nothing to record.
		return perr.ToRelayError()
	}

	// Client/protocol-level rejections below are pre-flight: no generation was
	// attempted, so — exactly like the chat path's validation errors — they do
	// not produce error usage rows. Only real relay attempts record.
	if normalized.Stream {
		if sErr := entry.Stream(); sErr != nil {
			return sErr.ToRelayError()
		}
	}

	if normalized.Model == "" {
		return openai.ErrorWrapper(fmt.Errorf("%s 请求缺少 model", entry.Name), "invalid_text_request", http.StatusBadRequest)
	}

	// Model-name mapping and forced system prompt, identical to the chat path.
	meta.IsStream = false
	meta.OriginModelName = normalized.Model
	normalized.Model, _ = getMappedModelName(normalized.Model, meta.ModelMapping)
	meta.ActualModelName = normalized.Model
	systemPromptReset := setSystemPrompt(ctx, normalized, meta.ForcedSystemPrompt)
	meta.PromptTokens = getPromptTokens(normalized, relaymode.ChatCompletions)

	// Upstream capability check (§6.6): stage 3 supports OpenAI-compatible
	// upstreams only; other API types are rejected explicitly, not guessed.
	if meta.APIType != apitype.OpenAI {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(
			fmt.Errorf("渠道类型 %d 不支持 %s 入口（当前仅支持 OpenAI 兼容上游，其他上游协议在后续阶段接入）",
				meta.ChannelType, entry.Name),
			"upstream_protocol_unsupported", http.StatusBadRequest)
	}

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// Rewrite the request as a canonical OpenAI /v1/chat/completions call.
	requestBody, err := json.Marshal(normalized)
	if err != nil {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}
	savedPath := meta.RequestURLPath
	meta.RequestURLPath = "/v1/chat/completions"
	resp, err := adaptor.DoRequest(c, meta, bytes.NewReader(requestBody))
	meta.RequestURLPath = savedPath
	if err != nil {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return RelayErrorHandler(resp)
	}

	textResponse, usage, readErr := readOpenAIUsage(resp, meta)
	if readErr != nil {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return readErr
	}

	envelope := entry.Render(textResponse, normalized.Model)
	renderProtocolJSON(c, envelope)

	go recordConsumeLog(ctx, usage, meta, normalized, systemPromptReset)
	go recordUsage(ctx, meta, normalized, usage, relaymodel.UsageStatusSuccess)
	return nil
}

// readOpenAIUsage parses an upstream OpenAI chat.completion body and produces
// the normalized usage, applying the same reported-vs-estimated rule as the
// OpenAI adaptor's Handler (§4.1 pointer semantics).
func readOpenAIUsage(resp *http.Response, m *meta.Meta) (*openai.TextResponse, *relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var textResponse openai.TextResponse
	if err := json.Unmarshal(raw, &textResponse); err != nil {
		return nil, nil, openai.ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError)
	}

	usage := textResponse.Usage
	if usage.TotalTokens == 0 || (usage.PromptTokens == 0 && usage.CompletionTokens == 0) {
		completionTokens := 0
		for _, choice := range textResponse.Choices {
			completionTokens += openai.CountTokenText(choice.Message.StringContent(), m.ActualModelName)
		}
		usage = relaymodel.Usage{
			PromptTokens:     m.PromptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      m.PromptTokens + completionTokens,
			Source:           relaymodel.UsageSourceEstimated,
		}
		textResponse.Usage = usage
	} else {
		usage.Source = relaymodel.UsageSourceReported
		textResponse.Usage = usage
	}
	relaymodel.NormalizeUsage(&textResponse.Usage)
	return &textResponse, &textResponse.Usage, nil
}

func renderProtocolJSON(c *gin.Context, envelope map[string]any) {
	body, err := json.Marshal(envelope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": relaymodel.Error{Message: "render response failed", Type: "one_api_error"},
		})
		return
	}
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(append(body, '\n'))
}
