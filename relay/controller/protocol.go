package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/relaymode"
	"github.com/songquanpeng/one-api/relay/sse"
)

// streamFactory builds a protocol-specific typed SSE writer for a stream.
type streamFactory func(w *sse.Writer, id, model string, promptUsage *relaymodel.Usage) protocolStream

// protocolStream is the shared shape of the Responses and Messages typed
// stream renderers.
type protocolStream interface {
	Chunk(ch sse.Chunk) error
	Finish(finishReason string, usage *relaymodel.Usage) error
	Error(e relaymodel.Error) error
}

// protocolEntry describes one client-protocol HTTP entry point: how to parse
// its request into the normalized GeneralOpenAIRequest and how to render an
// upstream OpenAI chat.completion back into that protocol. The relay pipeline
// itself (distributor -> adaptor -> upstream -> usage recorder) is shared and
// never sees a client-specific structure (implementation doc §6.1).
type protocolEntry struct {
	Name      string
	Parse     func([]byte) (*relaymodel.GeneralOpenAIRequest, *protocol.ProtocolError)
	Render    func(*openai.TextResponse, string) map[string]any
	NewStream streamFactory
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
	if normalized.Stream && entry.NewStream == nil {
		return openai.ErrorWrapper(fmt.Errorf("%s 流式暂不支持", entry.Name), "streaming_not_supported", http.StatusBadRequest)
	}

	if normalized.Model == "" {
		return openai.ErrorWrapper(fmt.Errorf("%s 请求缺少 model", entry.Name), "invalid_text_request", http.StatusBadRequest)
	}

	// Model-name mapping and forced system prompt, identical to the chat path.
	meta.IsStream = normalized.Stream
	meta.OriginModelName = normalized.Model
	normalized.Model, _ = getMappedModelName(normalized.Model, meta.ModelMapping)
	meta.ActualModelName = normalized.Model
	systemPromptReset := setSystemPrompt(ctx, normalized, meta.ForcedSystemPrompt)
	meta.PromptTokens = getPromptTokens(normalized, relaymode.ChatCompletions)

	// Ask OpenAI-compatible upstreams to emit a final usage chunk so the stream
	// can be finalized with reported values where available (§7.2).
	if meta.IsStream && normalized.StreamOptions == nil {
		normalized.StreamOptions = &relaymodel.StreamOptions{IncludeUsage: true}
	}

	// Per-model overrides configured on the channel, identical to the chat path.
	if bizErr := applyModelConfigOverrides(meta, normalized); bizErr != nil {
		recordUsage(ctx, meta, normalized, nil, relaymodel.UsageStatusError)
		return bizErr
	}

	// Upstream capability check (§6.6): stage 3/4 supports OpenAI-compatible
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

	if meta.IsStream {
		return relayProtocolStream(c, ctx, meta, entry, normalized, resp, systemPromptReset)
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

// relayProtocolStream reads the upstream OpenAI SSE stream, mirrors it into
// protocol-typed events, and finalizes usage exactly once — including the
// client-disconnect -> partial case (§7.1-7.3).
func relayProtocolStream(c *gin.Context, ctx context.Context, m *meta.Meta, entry *protocolEntry,
	normalized *relaymodel.GeneralOpenAIRequest, resp *http.Response, systemPromptReset bool) *relaymodel.ErrorWithStatusCode {

	sse.SetEventStreamHeaders(c.Writer)
	writer := sse.NewWriter(c.Writer)
	reader := sse.NewReader(resp.Body)
	state := sse.NewState()
	respID := "resp_" + helper.GetRequestID(ctx)
	// Prompt-side estimate is known up front (from getPromptTokens); it seeds
	// message_start input_tokens for protocols that report it there. Output
	// tokens and the authoritative usage are finalized at stream end.
	promptSeed := &relaymodel.Usage{PromptTokens: m.PromptTokens}
	stream := entry.NewStream(writer, respID, m.ActualModelName, promptSeed)

	counter := func(text, model string) int { return openai.CountTokenText(text, model) }

	aborted := false
	for {
		// Client disconnect: stop writing, finalize partial, close upstream.
		if ctx.Err() != nil {
			aborted = true
			break
		}
		ev, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Network break mid-stream -> partial terminal (§7.1).
			aborted = true
			break
		}
		if aerr := state.Absorb(ev); aerr != nil {
			// Malformed chunk: skip typed re-emit, keep accumulating; do not swallow.
			continue
		}
		if ev.IsDone() {
			break
		}
		chunk, perr := sse.ParseChunk(ev.Data)
		if perr != nil {
			continue
		}
		if werr := stream.Chunk(chunk); werr != nil {
			// write failure -> client gone
			aborted = true
			break
		}
	}

	usage := state.FinalizeUsage(m.PromptTokens, m.ActualModelName, counter)
	status := state.TerminalStatus(aborted)

	// Surface upstream errors and terminal frames to the client, not swallow.
	switch status {
	case relaymodel.UsageStatusError:
		var e relaymodel.Error
		if state.UpstreamError != nil {
			e = *state.UpstreamError
		} else {
			e = relaymodel.Error{Message: "upstream stream error", Type: "server_error"}
		}
		_ = stream.Error(e)
	default:
		_ = stream.Finish(state.FinishReason, usage)
	}

	_ = resp.Body.Close()
	recordUsage(ctx, m, normalized, usage, status)
	go recordConsumeLog(ctx, usage, m, normalized, systemPromptReset)
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
