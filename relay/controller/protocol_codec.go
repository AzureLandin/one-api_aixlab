package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/adaptor/openai_responses"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

// upstreamCodec translates between the normalized OpenAI chat shape that the
// protocol entry points, their renderers and the usage layer speak, and the wire
// protocol the selected channel actually speaks (implementation doc §6.1/§6.6).
//
// It exists so that the client side of the pipeline does not grow a branch per
// upstream: whatever the channel answers with, it reaches the renderers as
// chat.completion / chat.completion.chunk, the same normalized shapes a chat
// upstream produces. Supporting another upstream protocol therefore means
// writing a codec, not touching the three client protocols.
//
// The chat codec is this path's historical behaviour, kept as it was; the
// Anthropic codec drives the adaptor's own converters rather than re-implementing
// them, so a request translated here travels to the upstream in exactly the body
// /v1/chat/completions would have sent, and the chunks rendered to the client are
// the ones that path already produced.
type upstreamCodec interface {
	// Protocol is the upstream wire protocol this codec produces and parses.
	Protocol() upstream.Protocol
	// RequestBody renders the normalized request in that protocol, and reports
	// the request path the adaptor must use for it. A request the upstream's
	// protocol cannot express is refused by name here rather than sent in a
	// shape the upstream would have to guess at.
	RequestBody(m *meta.Meta, req *relaymodel.GeneralOpenAIRequest) (body io.Reader, requestPath string, bizErr *relaymodel.ErrorWithStatusCode)
	// NonStreamResponse parses a non-streaming upstream body into the OpenAI
	// shape the renderers read, including the reported-vs-estimated decision for
	// its usage.
	NonStreamResponse(m *meta.Meta, resp *http.Response) (*openai.TextResponse, *relaymodel.Usage, *relaymodel.ErrorWithStatusCode)
	// Stream reads a streaming upstream body.
	Stream(m *meta.Meta, resp *http.Response) upstreamStream
}

// upstreamStream is one upstream stream reduced to the two things the protocol
// path needs from it: the chunks to render and the terminal facts to finalize
// with. The read loop and the finalization stay shared, so a clean end, a
// mid-stream break and a client disconnect are handled once for every protocol.
type upstreamStream interface {
	// Next returns the next normalized chunk. io.EOF ends the stream cleanly;
	// any other error is a transport break.
	Next() (sse.Chunk, error)
	// Finalize reports the terminal facts. aborted reports that the caller
	// stopped early, either because the client went away or because the stream
	// broke.
	Finalize(aborted bool) streamOutcome
}

// streamOutcome is the terminal result of one upstream stream.
type streamOutcome struct {
	// FinishReason is an OpenAI finish_reason; the renderers map it back to
	// their own protocol's stop reason.
	FinishReason string
	Usage        *relaymodel.Usage
	// Status is the usage-record status: success, partial or error.
	Status string
	// Error carries the upstream's own error event, when it sent one.
	Error *relaymodel.Error
}

// upstreamCodecFor returns the codec that speaks for the selected channel, or a
// named refusal when no translator is wired for the protocol it declared.
//
// The refusal is by name rather than by falling back to the chat codec: a
// channel declared as speaking the Responses API must not silently receive a
// chat/completions body, which is exactly what the declaration exists to
// prevent.
func upstreamCodecFor(m *meta.Meta, up upstream.Protocol, entry *protocolEntry) (upstreamCodec, *relaymodel.ErrorWithStatusCode) {
	switch up {
	case upstream.OpenAIChat, upstream.Compatible:
		return openaiChatCodec{}, nil
	case upstream.AnthropicMessages:
		return anthropicChatCodec{}, nil
	case upstream.OpenAIResponses:
		// The declaration is honoured only where the request can actually travel:
		// a channel of another adaptor family cannot send a Responses body.
		if bizErr := responsesUpstreamSupported(m); bizErr != nil {
			return nil, bizErr
		}
		return openaiResponsesCodec{}, nil
	}
	return nil, openai.ErrorWrapper(
		fmt.Errorf("渠道 %d 的上游协议 %s 无法服务 %s 入口", m.ChannelId, up, entry.Name),
		"upstream_protocol_unsupported", http.StatusBadRequest)
}

// --- OpenAI Chat Completions ------------------------------------------------

// openaiChatCodec posts the normalized request to the upstream's
// /v1/chat/completions and reads the answer back as it is.
type openaiChatCodec struct{}

func (openaiChatCodec) Protocol() upstream.Protocol { return upstream.OpenAIChat }

func (openaiChatCodec) RequestBody(_ *meta.Meta, req *relaymodel.GeneralOpenAIRequest) (io.Reader, string, *relaymodel.ErrorWithStatusCode) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, "", openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}
	// The client protocol's own path is not the upstream's: whatever entry point
	// the caller used, an OpenAI-compatible upstream is asked for a chat
	// completion.
	return bytes.NewReader(body), "/v1/chat/completions", nil
}

func (openaiChatCodec) NonStreamResponse(m *meta.Meta, resp *http.Response) (*openai.TextResponse, *relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	return readOpenAIUsage(resp, m)
}

func (openaiChatCodec) Stream(m *meta.Meta, resp *http.Response) upstreamStream {
	return &openaiChatStream{
		reader:       sse.NewReader(resp.Body),
		state:        sse.NewState(),
		promptTokens: m.PromptTokens,
		modelName:    m.ActualModelName,
	}
}

type openaiChatStream struct {
	reader       *sse.Reader
	state        *sse.StreamState
	promptTokens int
	modelName    string
}

// Next skips what the renderer cannot use — keep-alives, [DONE], malformed
// payloads — exactly as the read loop did inline, so the renderer sees the same
// chunks in the same order.
func (s *openaiChatStream) Next() (sse.Chunk, error) {
	for {
		ev, err := s.reader.Next()
		if err != nil {
			return sse.Chunk{}, err
		}
		if aerr := s.state.Absorb(ev); aerr != nil {
			// Malformed chunk: skip the typed re-emit, keep accumulating.
			continue
		}
		if ev.IsDone() {
			return sse.Chunk{}, io.EOF
		}
		chunk, perr := sse.ParseChunk(ev.Data)
		if perr != nil {
			continue
		}
		return chunk, nil
	}
}

func (s *openaiChatStream) Finalize(aborted bool) streamOutcome {
	counter := func(text, model string) int { return openai.CountTokenText(text, model) }
	return streamOutcome{
		FinishReason: s.state.FinishReason,
		Usage:        s.state.FinalizeUsage(s.promptTokens, s.modelName, counter),
		Status:       s.state.TerminalStatus(aborted),
		Error:        s.state.UpstreamError,
	}
}

// --- Anthropic Messages -----------------------------------------------------

// anthropicChatCodec serves a client speaking one of the other two protocols
// from an upstream that speaks Anthropic Messages. The same-protocol pair
// (/v1/messages -> Anthropic) is the passthrough, not this codec.
type anthropicChatCodec struct{}

func (anthropicChatCodec) Protocol() upstream.Protocol { return upstream.AnthropicMessages }

func (anthropicChatCodec) RequestBody(_ *meta.Meta, req *relaymodel.GeneralOpenAIRequest) (io.Reader, string, *relaymodel.ErrorWithStatusCode) {
	body, err := json.Marshal(anthropic.ConvertRequest(*req))
	if err != nil {
		return nil, "", openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}
	return bytes.NewReader(body), "/v1/messages", nil
}

func (anthropicChatCodec) NonStreamResponse(m *meta.Meta, resp *http.Response) (*openai.TextResponse, *relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var claudeResponse anthropic.Response
	if err := json.Unmarshal(raw, &claudeResponse); err != nil {
		return nil, nil, openai.ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError)
	}

	textResponse := anthropic.ResponseClaude2OpenAI(&claudeResponse)
	textResponse.Model = m.ActualModelName
	textResponse.Usage = anthropic.RelayUsage(claudeResponse.Usage)
	usage := anthropicResponseUsage(m, textResponse)
	return textResponse, &usage, nil
}

// anthropicResponseUsage decides the usage fact for a translated Anthropic body
// and writes it back onto the response, because the renderers read the usage off
// the same value the recorder persists.
//
// A body whose usage block carries neither tokens nor a cache field came from an
// upstream that said nothing about cost, which is not the claim "reported zero":
// the completion is counted locally and the fact is labelled estimated. This is
// the rule the passthrough applies to the same upstream body, so a translated
// and a mirrored Anthropic response agree on when the upstream was silent.
func anthropicResponseUsage(m *meta.Meta, textResponse *openai.TextResponse) relaymodel.Usage {
	usage := textResponse.Usage
	if usageLooksUnreported(usage) {
		completion := 0
		for _, choice := range textResponse.Choices {
			completion += openai.CountTokenText(choice.Message.StringContent(), m.ActualModelName)
		}
		usage = relaymodel.Usage{
			PromptTokens:     m.PromptTokens,
			CompletionTokens: completion,
			TotalTokens:      m.PromptTokens + completion,
			Source:           relaymodel.UsageSourceEstimated,
		}
	} else {
		usage.Source = relaymodel.UsageSourceReported
	}
	relaymodel.NormalizeUsage(&usage)
	textResponse.Usage = usage
	return usage
}

func (anthropicChatCodec) Stream(m *meta.Meta, resp *http.Response) upstreamStream {
	return &anthropicChatStream{
		reader:       sse.NewReader(resp.Body),
		tools:        anthropic.NewStreamState(),
		mirror:       &messagesMirror{},
		promptTokens: m.PromptTokens,
		modelName:    m.ActualModelName,
	}
}

// anthropicChatStream turns an Anthropic Messages SSE stream into normalized
// chunks. It holds no conversion logic of its own: the events are parsed by the
// same accumulator the passthrough mirrors with (messagesMirror), translated by
// the adaptor's own converter (StreamResponseClaude2OpenAI, over the same
// content-block-to-tool-index bookkeeping the adaptor's StreamHandler uses), and
// only then re-read in the normalized framing. A translated stream and a
// mirrored one therefore cannot disagree about the same upstream events.
type anthropicChatStream struct {
	reader *sse.Reader
	// tools maps an Anthropic content block index onto a dense OpenAI tool index.
	tools *anthropic.StreamState
	// mirror accumulates the usage fact, the terminal marker and the upstream
	// error, none of which the conversion produces.
	mirror       *messagesMirror
	promptTokens int
	modelName    string

	// finishReason is the last non-empty finish_reason the conversion produced.
	finishReason string
}

func (s *anthropicChatStream) Next() (sse.Chunk, error) {
	for {
		ev, err := s.reader.Next()
		if err != nil {
			return sse.Chunk{}, err
		}
		parsed, ok := s.mirror.observe(ev)
		if !ok {
			// A keep-alive or a frame this gateway does not model: nothing to
			// render, nothing to accumulate.
			continue
		}
		response, _ := anthropic.StreamResponseClaude2OpenAI(s.tools, parsed)
		if response == nil {
			// message_start carries only the message id and model, both of which
			// the renderer already wrote when the stream opened.
			continue
		}
		if reason := chunkFinishReason(response); reason != "" {
			s.finishReason = reason
		}
		chunk, cerr := chunkFromOpenAIStreamResponse(response)
		if cerr != nil {
			// The conversion produced this value, so a failure here is a bug in
			// this gateway rather than a bad upstream frame; it is logged and the
			// stream continues instead of being reported as a break.
			logger.SysError("failed to re-read converted anthropic chunk: " + cerr.Error())
			continue
		}
		return chunk, nil
	}
}

func (s *anthropicChatStream) Finalize(aborted bool) streamOutcome {
	return streamOutcome{
		FinishReason: s.finishReason,
		Usage:        s.mirror.finalizeUsage(s.modelName, s.promptTokens),
		Status:       s.mirror.status(aborted),
		Error:        s.mirror.upstreamErr,
	}
}

// chunkFinishReason reads the first non-empty finish_reason off a converted
// chunk. Unlike sse.Chunk.FinishReasonOf it works on the adaptor's own struct,
// because the chunk has not been re-read yet.
func chunkFinishReason(response *openai.ChatCompletionsStreamResponse) string {
	for _, choice := range response.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return *choice.FinishReason
		}
	}
	return ""
}

// --- OpenAI Responses -------------------------------------------------------

// openaiResponsesCodec serves a client speaking one of the other two protocols
// from an upstream that speaks the Responses API. The same-protocol pair
// (/v1/responses -> Responses) is the passthrough, not this codec.
//
// It holds no conversion of its own: the translation is the sink-free one in
// relay/adaptor/openai_responses, which the chat entry point drives too, and the
// stream is read by the reader that file also defines. Whatever the upstream
// answers, the client-protocol renderers see the normalized chat shapes they
// already read.
type openaiResponsesCodec struct{}

func (openaiResponsesCodec) Protocol() upstream.Protocol { return upstream.OpenAIResponses }

func (openaiResponsesCodec) RequestBody(_ *meta.Meta, req *relaymodel.GeneralOpenAIRequest) (io.Reader, string, *relaymodel.ErrorWithStatusCode) {
	converted, bizErr := openai_responses.ChatToResponsesRequest(req)
	if bizErr != nil {
		// A request the Responses API cannot express (a field with no counterpart,
		// a tool result with no call id) is refused by name instead of being sent
		// with the field dropped.
		return nil, "", bizErr
	}
	body, err := json.Marshal(converted)
	if err != nil {
		return nil, "", openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}
	return bytes.NewReader(body), "/v1/responses", nil
}

func (openaiResponsesCodec) NonStreamResponse(m *meta.Meta, resp *http.Response) (*openai.TextResponse, *relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var upstream openai_responses.Response
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, nil, openai.ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError)
	}
	// A Responses answer can report a failure with HTTP 200, so the body's own
	// status is checked before it is read as a success.
	if e := upstream.Failure(); e != nil {
		return nil, nil, &relaymodel.ErrorWithStatusCode{Error: *e, StatusCode: http.StatusBadGateway}
	}
	// ToChatResponse decides the usage fact itself: the upstream's numbers when it
	// reported any, a labelled local estimate when it was silent.
	textResponse, convertErr := openai_responses.ToChatResponse(&upstream, m.ActualModelName, m.PromptTokens)
	if convertErr != nil {
		// The upstream's answer carries output this gateway cannot express in a
		// chat envelope; rendering it anyway would hand the client a truncated
		// reply, so it is refused with the name of what could not be mapped.
		return nil, nil, &relaymodel.ErrorWithStatusCode{Error: *convertErr, StatusCode: http.StatusBadGateway}
	}
	// The renderers read the usage off the same value the recorder persists, so
	// the one ToChatResponse normalized is returned by reference.
	return textResponse, &textResponse.Usage, nil
}

func (openaiResponsesCodec) Stream(m *meta.Meta, resp *http.Response) upstreamStream {
	return &responsesUpstreamStream{newResponsesUpstream(resp.Body, m.ActualModelName, m.PromptTokens)}
}

// responsesUpstreamStream adapts the shared Responses reader to the codec seam.
// The reader hands back the bytes it already marshalled for the chat sink, so
// this side only re-reads them in the normalized framing.
type responsesUpstreamStream struct{ *responsesUpstream }

func (s *responsesUpstreamStream) Next() (sse.Chunk, error) {
	for {
		_, raw, err := s.responsesUpstream.next()
		if err != nil {
			return sse.Chunk{}, err
		}
		chunk, perr := sse.ParseChunk(raw)
		if perr != nil {
			// The converter produced these bytes, so a chunk that cannot be re-read
			// is a bug in this gateway rather than a bad upstream frame: it is logged
			// and skipped instead of being reported as a stream break.
			logger.SysError("failed to re-read converted responses chunk: " + perr.Error())
			continue
		}
		return chunk, nil
	}
}

func (s *responsesUpstreamStream) Finalize(aborted bool) streamOutcome {
	return s.responsesUpstream.outcome(aborted)
}

// chunkFromOpenAIStreamResponse re-reads a converted chunk in the normalized
// framing the renderers consume. Going through JSON keeps one definition of that
// framing instead of a second, hand-written translation that could drift from it.
func chunkFromOpenAIStreamResponse(response *openai.ChatCompletionsStreamResponse) (sse.Chunk, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return sse.Chunk{}, err
	}
	return sse.ParseChunk(string(raw))
}
