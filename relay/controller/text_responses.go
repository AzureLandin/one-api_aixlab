package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/common/render"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/adaptor/openai_responses"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// Serving /v1/chat/completions from a channel that speaks the Responses API.
//
// The translation itself has no sink: relay/adaptor/openai_responses turns a
// normalized chat request into a Responses body and a Responses answer back into
// chat shape. This file is the chat entry point's sink for it — and the read loop
// below is also what the protocol codec drives (protocol_codec.go's
// openaiResponsesCodec), so an upstream Responses stream is read by one
// implementation whichever client protocol asked for it.

// responsesUpstreamSupported refuses a channel whose declared upstream protocol
// cannot be honoured by the adaptor that would send the request. The Responses
// request body and its URL shape belong to the OpenAI adaptor family; a channel
// of another family (Anthropic, Gemini, Bedrock, …) that declared itself as
// speaking the Responses API would otherwise be sent a Responses body on a URL
// built for a different protocol — a failure with no error anywhere, which is
// exactly what the declaration exists to prevent.
func responsesUpstreamSupported(m *meta.Meta) *relaymodel.ErrorWithStatusCode {
	if m.APIType != apitype.OpenAI {
		return openai.ErrorWrapper(
			fmt.Errorf("渠道 %d 声明了 openai-responses 上游，但其 api 类型为 %d，不是能发送 Responses 请求的 OpenAI 系渠道",
				m.ChannelId, m.APIType),
			"upstream_protocol_unsupported", http.StatusBadRequest)
	}
	return nil
}

// responsesUpstream reads an upstream Responses SSE stream and converts it into
// normalized chat chunks. Reading is shared, writing is not: the chat entry point
// writes the typed chunk out as a `data:` line, the protocol codec re-reads the
// same bytes as an sse.Chunk, and the usage fact is accumulated from those bytes
// either way.
type responsesUpstream struct {
	reader *sse.Reader
	conv   *openai_responses.StreamState
	// acc is the usage ladder every other path uses (reported wins, else a local
	// estimate, else nil). It is fed the very bytes the sink writes, so the
	// recorded fact and the client's stream can never disagree.
	acc          *sse.StreamState
	promptTokens int
	modelName    string
	// pending holds the chunks one upstream event converted into, so a multi-chunk
	// event (a snapshot flush plus the terminal tail, say) is not truncated by the
	// next read.
	pending []openai.ChatCompletionsStreamResponse
}

func newResponsesUpstream(body io.Reader, modelName string, promptTokens int) *responsesUpstream {
	return &responsesUpstream{
		reader:       sse.NewReader(body),
		conv:         openai_responses.NewStreamState(modelName),
		acc:          sse.NewState(),
		promptTokens: promptTokens,
		modelName:    modelName,
	}
}

// next returns the next chunk the client must see, in the converter's own typed
// shape and as the bytes that go on the wire. io.EOF ends the stream: unlike a
// chat upstream, a Responses stream has no [DONE] marker, so the end of the body
// is the end of the answer. A transport break is returned as-is; whether it is a
// failure or a truncated answer is the caller's terminal decision.
func (r *responsesUpstream) next() (openai.ChatCompletionsStreamResponse, string, error) {
	for {
		if len(r.pending) > 0 {
			chunk := r.pending[0]
			r.pending = r.pending[1:]
			raw, err := json.Marshal(chunk)
			if err != nil {
				// The conversion produced this value, so a failure here is a bug in
				// this gateway rather than a bad upstream frame: it is logged and the
				// chunk skipped instead of being reported as a stream break.
				logger.SysError("failed to marshal converted responses chunk: " + err.Error())
				continue
			}
			_ = r.acc.Absorb(sse.Event{Data: string(raw)})
			return chunk, string(raw), nil
		}
		ev, err := r.reader.Next()
		if err != nil {
			return openai.ChatCompletionsStreamResponse{}, "", err
		}
		r.pending = r.conv.Convert(ev.Type, []byte(ev.Data))
	}
}

// status mirrors sse.StreamState.TerminalStatus for a Responses stream. The one
// difference is the terminal marker: a chat upstream ends with [DONE], a
// Responses upstream ends with response.completed / .incomplete / .failed, so the
// converter's own terminal flag is what says the stream finished.
func (r *responsesUpstream) status(aborted bool) string {
	switch {
	case r.conv.UpstreamError() != nil:
		return relaymodel.UsageStatusError
	case aborted || !r.conv.Terminal():
		return relaymodel.UsageStatusPartial
	}
	return relaymodel.UsageStatusSuccess
}

// outcome is the terminal fact in the shape the protocol codec's Finalize speaks.
func (r *responsesUpstream) outcome(aborted bool) streamOutcome {
	counter := func(text, model string) int { return openai.CountTokenText(text, model) }
	return streamOutcome{
		FinishReason: r.acc.FinishReason,
		Usage:        r.finalizeUsage(counter),
		Status:       r.status(aborted),
		Error:        r.conv.UpstreamError(),
	}
}

// finalizeUsage picks the usage fact on the reported / estimated / nil ladder,
// with the same floor the Anthropic codec's mirror applies: an upstream that
// reported nothing and streamed no text leaves nothing to count, so the record
// carries its status alone rather than a fabricated zero-output row. An upstream
// error is the case that reaches this — a failed response with no output is not
// a generation whose cost is merely unknown.
func (r *responsesUpstream) finalizeUsage(counter func(text, model string) int) *relaymodel.Usage {
	if !r.acc.UsageReported && r.acc.ResponseTextForUsage() == "" {
		return nil
	}
	return r.acc.FinalizeUsage(r.promptTokens, r.modelName, counter)
}

// relayResponsesChat serves one chat completion from a Responses upstream.
func relayResponsesChat(c *gin.Context, m *meta.Meta, textRequest *relaymodel.GeneralOpenAIRequest,
	a adaptor.Adaptor, systemPromptReset bool) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	responsesRequest, bizErr := openai_responses.ChatToResponsesRequest(textRequest)
	if bizErr != nil {
		// A pre-flight rejection: the client's own request cannot be expressed in
		// the Responses API, so no generation was attempted and no usage row is
		// written — the rule the protocol entries apply to their parse errors.
		return bizErr
	}
	body, err := json.Marshal(responsesRequest)
	if err != nil {
		return openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}

	// The upstream's own path, not the client's: whichever entry point was used, a
	// Responses upstream is asked for a response.
	savedPath := m.RequestURLPath
	m.RequestURLPath = "/v1/responses"
	resp, err := a.DoRequest(c, m, bytes.NewReader(body))
	m.RequestURLPath = savedPath
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		recordUsage(ctx, m, textRequest.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(m, resp) {
		recordUsage(ctx, m, textRequest.Model, nil, relaymodel.UsageStatusError)
		return RelayErrorHandler(resp)
	}

	if m.IsStream {
		return relayResponsesChatStream(c, m, textRequest.Model, resp, systemPromptReset)
	}
	return relayResponsesChatOnce(c, m, textRequest.Model, resp, systemPromptReset)
}

// relayResponsesChatOnce reads one non-streaming Responses answer, renders it as
// the chat envelope the client asked for, and records the usage it just reported
// to that client.
func relayResponsesChatOnce(c *gin.Context, m *meta.Meta, modelName string, resp *http.Response,
	systemPromptReset bool) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		recordUsage(ctx, m, modelName, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	if err := resp.Body.Close(); err != nil {
		logger.SysError("close response body failed: " + err.Error())
	}

	var upstream openai_responses.Response
	if err := json.Unmarshal(raw, &upstream); err != nil {
		recordUsage(ctx, m, modelName, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError)
	}
	// A Responses answer can report a failure with HTTP 200, so the body's own
	// status is checked before it is read as a success.
	if e := upstream.Failure(); e != nil {
		recordUsage(ctx, m, modelName, nil, relaymodel.UsageStatusError)
		return &relaymodel.ErrorWithStatusCode{Error: *e, StatusCode: http.StatusBadGateway}
	}

	textResponse, convertErr := openai_responses.ToChatResponse(&upstream, m.ActualModelName, m.PromptTokens)
	if convertErr != nil {
		// The answer carries output a chat envelope cannot express, so the client
		// would get a truncated reply if this were rendered anyway.
		recordUsage(ctx, m, modelName, nil, relaymodel.UsageStatusError)
		return &relaymodel.ErrorWithStatusCode{Error: *convertErr, StatusCode: http.StatusBadGateway}
	}

	rendered, err := json.Marshal(textResponse)
	if err != nil {
		recordUsage(ctx, m, modelName, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "marshal_response_body_failed", http.StatusInternalServerError)
	}
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(resp.StatusCode)
	if _, err := c.Writer.Write(append(rendered, '\n')); err != nil {
		logger.SysError("write response body failed: " + err.Error())
	}

	usage := textResponse.Usage
	go recordConsumeLog(ctx, &usage, m, modelName, systemPromptReset)
	go recordUsage(ctx, m, modelName, &usage, relaymodel.UsageStatusSuccess)
	return nil
}

// relayResponsesChatStream writes the translated stream to the client as the
// data: lines a chat client reads, and finalizes the usage fact exactly once,
// including the client-disconnect -> partial case.
func relayResponsesChatStream(c *gin.Context, m *meta.Meta, modelName string, resp *http.Response,
	systemPromptReset bool) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	common.SetEventStreamHeaders(c)
	source := newResponsesUpstream(resp.Body, m.ActualModelName, m.PromptTokens)

	aborted := false
	for {
		// Client disconnect: stop writing and finalize what was seen as partial.
		if ctx.Err() != nil {
			aborted = true
			break
		}
		_, raw, err := source.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Network break mid-stream -> partial terminal (§7.1).
			aborted = true
			break
		}
		render.StringData(c, raw)
	}

	outcome := source.outcome(aborted)
	if outcome.Status == relaymodel.UsageStatusError {
		// The upstream's own failure goes out in the framing a chat client reads a
		// mid-stream error in, rather than being left as a stream that simply stops.
		e := relaymodel.Error{Message: "upstream stream error", Type: "server_error"}
		if outcome.Error != nil {
			e = *outcome.Error
		}
		_ = render.ObjectData(c, map[string]any{"error": e})
	}
	render.Done(c)

	if err := resp.Body.Close(); err != nil {
		logger.SysError("close response body failed: " + err.Error())
	}

	recordUsage(ctx, m, modelName, outcome.Usage, outcome.Status)
	go recordConsumeLog(ctx, outcome.Usage, m, modelName, systemPromptReset)
	return nil
}
