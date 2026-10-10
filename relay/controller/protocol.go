package controller

import (
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
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/relaymode"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

// streamFactory builds a protocol-specific typed SSE writer for a stream.
type streamFactory func(w *sse.Writer, id, model string, promptUsage *relaymodel.Usage) protocolStream

// protocolStream is the shared shape of the Responses and Messages typed
// stream renderers.
type protocolStream interface {
	Chunk(ch sse.Chunk) error
	// Hosted renders one hosted-tool artifact transition — a web search or an
	// MCP call the upstream ran itself. It is separate from Chunk because it is
	// not a text/tool-call delta: a client protocol with a hosted vocabulary
	// spells it as its own block or item type, and one without simply has
	// nowhere to put it (see LossOf).
	Hosted(ev relaymodel.HostedStreamEvent) error
	Finish(finishReason string, usage *relaymodel.Usage) error
	Error(e relaymodel.Error) error
	// LossOf reports what the client's protocol could not carry back over the
	// whole stream. Like the non-streaming renderer's HostedOutput.Losses it is
	// a diagnostic channel only: the response phase never rejects under any
	// loss policy, and the relay logs these once the stream has ended.
	LossOf() []relaymodel.ConversionDiagnostic
}

// protocolEntry describes one client-protocol HTTP entry point: how to parse
// its request into the normalized GeneralOpenAIRequest and how to render an
// upstream OpenAI chat.completion back into that protocol. The relay pipeline
// itself (distributor -> adaptor -> upstream -> usage recorder) is shared and
// never sees a client-specific structure (implementation doc §6.1).
type protocolEntry struct {
	Name string
	// Protocol is the client protocol this entry point speaks. It is what the
	// capability check and the passthrough decision are made against.
	Protocol  upstream.Protocol
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

	// Upstream capability check (§6.6). It runs before the request is parsed
	// because the answer does not depend on the parsed form, and because the
	// passthrough branch below must be reached with the client's own body
	// untouched.
	upstreamProtocol := upstream.Resolve(meta.Config.UpstreamProtocol, meta.APIType)
	if !upstream.CanServe(entry.Protocol, upstreamProtocol) {
		// The model name is read straight off the raw body: no normalized
		// request exists on this branch, and the error row still wants a name.
		modelName, stream := nativeRequestMeta(body)
		meta.ActualModelName = modelName
		meta.IsStream = stream
		recordUsage(ctx, meta, modelName, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(
			fmt.Errorf("渠道 %d 的上游协议 %s 无法服务 %s 入口",
				meta.ChannelId, upstreamProtocol, entry.Name),
			"upstream_protocol_unsupported", http.StatusBadRequest)
	}

	// Same upstream protocol: forward the request as the client wrote it. This
	// is the only path that keeps cache_control, thinking and hosted tool
	// declarations byte-for-byte, and it exists precisely so that the client's
	// own protocol is never round-tripped through a lossy intermediate form.
	if upstream.Passthrough(entry.Protocol, upstreamProtocol) {
		return relayPassthrough(c, meta, entry, body)
	}

	// Different protocol: a codec translates in both directions (§6.6). The
	// lookup happens before the request is parsed because a protocol no codec
	// speaks is refused on the client's own terms, and because the answer does
	// not depend on the parsed form.
	codec, codecErr := upstreamCodecFor(meta, upstreamProtocol, entry)
	if codecErr != nil {
		modelName, stream := nativeRequestMeta(body)
		meta.ActualModelName = modelName
		meta.IsStream = stream
		recordUsage(ctx, meta, modelName, nil, relaymodel.UsageStatusError)
		return codecErr
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
		recordUsage(ctx, meta, normalized.Model, nil, relaymodel.UsageStatusError)
		return bizErr
	}

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// Rewrite the request in the upstream's own protocol: a chat completion for a
	// chat upstream, a Messages call for an Anthropic one, a response for a
	// Responses one.
	requestBody, requestPath, bodyErr := codec.RequestBody(meta, normalized)
	if bodyErr != nil {
		// A request the upstream's protocol cannot express is a pre-flight
		// rejection (4xx, named), so — like the parse and validation rejections
		// above — nothing was sent and no usage row is written. A 5xx here is this
		// gateway failing to marshal a body it built, which follows the existing
		// rule of recording a real attempt's failure.
		if bodyErr.StatusCode >= http.StatusInternalServerError {
			recordUsage(ctx, meta, normalized.Model, nil, relaymodel.UsageStatusError)
		}
		return bodyErr
	}
	savedPath := meta.RequestURLPath
	meta.RequestURLPath = requestPath
	resp, err := adaptor.DoRequest(c, meta, requestBody)
	meta.RequestURLPath = savedPath
	if err != nil {
		recordUsage(ctx, meta, normalized.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		recordUsage(ctx, meta, normalized.Model, nil, relaymodel.UsageStatusError)
		return RelayErrorHandler(resp)
	}

	if meta.IsStream {
		return relayProtocolStream(c, ctx, meta, entry, normalized, resp, codec, systemPromptReset)
	}

	textResponse, usage, readErr := codec.NonStreamResponse(meta, resp)
	if readErr != nil {
		recordUsage(ctx, meta, normalized.Model, nil, relaymodel.UsageStatusError)
		return readErr
	}

	envelope := entry.Render(textResponse, normalized.Model)
	// Rendering records what the client's protocol could not carry back. Unlike
	// the request phase these never become a refusal — the upstream has already
	// answered, and failing now would throw away a reply the client asked for —
	// so a log line is the only place the loss can surface, and it is written
	// before the envelope goes out so the two are ordered in the log.
	logHostedLosses(meta.ChannelId, "response", textResponse.Hosted.LossOf())
	renderProtocolJSON(c, envelope)

	go recordConsumeLog(ctx, usage, meta, normalized.Model, systemPromptReset)
	go recordUsage(ctx, meta, normalized.Model, usage, relaymodel.UsageStatusSuccess)
	return nil
}

// relayProtocolStream reads the upstream stream through the channel's codec,
// renders it into protocol-typed events, and finalizes usage exactly once —
// including the client-disconnect -> partial case (§7.1-7.3). Which upstream
// protocol produced the chunks is the codec's business: by the time they reach
// here they are the normalized chat.completion.chunk framing every renderer
// reads.
func relayProtocolStream(c *gin.Context, ctx context.Context, m *meta.Meta, entry *protocolEntry,
	normalized *relaymodel.GeneralOpenAIRequest, resp *http.Response, codec upstreamCodec,
	systemPromptReset bool) *relaymodel.ErrorWithStatusCode {

	sse.SetEventStreamHeaders(c.Writer)
	writer := sse.NewWriter(c.Writer)
	source := codec.Stream(m, resp)
	respID := "resp_" + helper.GetRequestID(ctx)
	// Prompt-side estimate is known up front (from getPromptTokens); it seeds
	// message_start input_tokens for protocols that report it there. Output
	// tokens and the authoritative usage are finalized at stream end.
	promptSeed := &relaymodel.Usage{PromptTokens: m.PromptTokens}
	stream := entry.NewStream(writer, respID, m.ActualModelName, promptSeed)

	aborted := false
	for {
		// Client disconnect: stop writing, finalize partial, close upstream.
		if ctx.Err() != nil {
			aborted = true
			break
		}
		chunk, err := source.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Network break mid-stream -> partial terminal (§7.1).
			aborted = true
			break
		}
		// A hosted artifact is not a chat.completion delta: it carries no text and
		// no tool call, and only a client protocol with a hosted vocabulary can
		// spell it. Routing it through Chunk would feed an empty delta into the
		// text/tool accumulators of every renderer.
		if chunk.Hosted != nil {
			if werr := stream.Hosted(*chunk.Hosted); werr != nil {
				aborted = true
				break
			}
			continue
		}
		if werr := stream.Chunk(chunk); werr != nil {
			// write failure -> client gone
			aborted = true
			break
		}
	}

	outcome := source.Finalize(aborted)

	// Surface upstream errors and terminal frames to the client, not swallow.
	switch outcome.Status {
	case relaymodel.UsageStatusError:
		e := relaymodel.Error{Message: "upstream stream error", Type: "server_error"}
		if outcome.Error != nil {
			e = *outcome.Error
		}
		_ = stream.Error(e)
	default:
		_ = stream.Finish(outcome.FinishReason, outcome.Usage)
	}

	// What the client's protocol could not spell is logged once, here — after the
	// terminal frame, since finalizing can itself close an artifact the stream
	// never finished. Same rule as the non-streaming path: the stream is already
	// being written, so a loss can never become a refusal, and the log is the
	// only place an operator learns it happened.
	logHostedLosses(m.ChannelId, "stream", stream.LossOf())

	_ = resp.Body.Close()
	recordUsage(ctx, m, normalized.Model, outcome.Usage, outcome.Status)
	go recordConsumeLog(ctx, outcome.Usage, m, normalized.Model, systemPromptReset)
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
