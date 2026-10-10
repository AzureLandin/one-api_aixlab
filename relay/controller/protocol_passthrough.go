package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/adaptor/openai_responses"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

// Same-protocol passthrough (implementation doc §6.6). When the selected
// channel's upstream speaks the protocol the client used, the request and the
// response travel as the client wrote and read them: nothing is normalized in
// either direction, so cache_control, thinking, hosted-tool declarations and
// fields this gateway has never heard of survive with their values intact — the
// one property the chat-pivot path cannot promise, since GeneralOpenAIRequest
// has no slot for them.
//
// The gateway still owes the channel two rewrites, applied as a targeted patch
// on the raw JSON rather than a struct round-trip: the model name (mapping) and
// the forced system prompt.
func relayPassthrough(c *gin.Context, m *meta.Meta, entry *protocolEntry, body []byte) *relaymodel.ErrorWithStatusCode {
	proto, ok := passthroughProtocols[entry.Protocol]
	if !ok {
		return openai.ErrorWrapper(
			fmt.Errorf("客户端协议 %s 暂不支持同协议直通", entry.Protocol),
			"passthrough_not_supported", http.StatusBadRequest)
	}
	return relayPassthroughRequest(c, m, proto, body)
}

// passthroughProtocol is one protocol's same-protocol passthrough: the three
// steps that genuinely differ between protocols. The pipeline around them —
// project the body, pick the adaptor, send it, branch on streaming, finalize the
// usage fact — is written once in relayPassthroughRequest.
type passthroughProtocol struct {
	// project applies the model mapping and the forced prompt to the client's own
	// body, leaving every other field as the client wrote it.
	project func(body []byte, m *meta.Meta) (*projectedRequest, *relaymodel.ErrorWithStatusCode)
	// once forwards a non-streaming upstream answer and reads the usage fact out
	// of it on the way past.
	once func(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode
	// stream mirrors a streaming upstream answer and finalizes the usage fact once.
	stream func(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode
}

var passthroughProtocols = map[upstream.Protocol]passthroughProtocol{
	upstream.AnthropicMessages: {
		project: projectMessagesRequest,
		once:    relayMessagesPassthroughOnce,
		stream:  relayMessagesPassthroughStream,
	},
	upstream.OpenAIResponses: {
		project: projectResponsesRequest,
		once:    relayResponsesPassthroughOnce,
		stream:  relayResponsesPassthroughStream,
	},
}

// relayPassthroughRequest sends a request the upstream already speaks. The body
// goes out as the client wrote it (modulo the projection), which is also why
// there is no path rewriting here: the client protocol's own URL path is the
// upstream's too.
func relayPassthroughRequest(c *gin.Context, m *meta.Meta, proto passthroughProtocol, body []byte) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	projected, perr := proto.project(body, m)
	if perr != nil {
		recordUsage(ctx, m, m.ActualModelName, nil, relaymodel.UsageStatusError)
		return perr
	}

	adaptor := relay.GetAdaptor(m.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", m.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(m)

	resp, err := adaptor.DoRequest(c, m, bytes.NewReader(projected.Body))
	if err != nil {
		recordUsage(ctx, m, projected.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(m, resp) {
		recordUsage(ctx, m, projected.Model, nil, relaymodel.UsageStatusError)
		return RelayErrorHandler(resp)
	}

	if m.IsStream {
		return proto.stream(c, m, projected, resp)
	}
	return proto.once(c, m, projected, resp)
}

// projectedRequest is the outcome of patching a native protocol body.
type projectedRequest struct {
	Body []byte
	// Model is the post-mapping model name the upstream will be asked for.
	Model string
	// SystemPromptReset reports whether the operator's system prompt replaced the
	// client's, which the legacy consumption log surfaces.
	SystemPromptReset bool
}

// nativeRequestMeta reads the model name and the stream flag straight out of a
// raw protocol body. It exists for the branches that deliberately never build a
// normalized request — the capability gate and the passthrough — which still owe
// the usage row a model name and a stream marker.
func nativeRequestMeta(body []byte) (string, bool) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", false
	}
	return probe.Model, probe.Stream
}

// decodeNativeRequest splits a raw protocol body into its top-level fields, kept
// as raw JSON so that every field this gateway does not touch keeps its value
// and its shape, plus the two facts needed without normalizing.
func decodeNativeRequest(body []byte) (map[string]json.RawMessage, string, bool, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, "", false, err
	}
	var model string
	if raw, ok := fields["model"]; ok {
		_ = json.Unmarshal(raw, &model)
	}
	var stream bool
	if raw, ok := fields["stream"]; ok {
		_ = json.Unmarshal(raw, &stream)
	}
	return fields, model, stream, nil
}

// projectNativeRequest applies the two rewrites the gateway owes the channel to a
// raw protocol body. Everything else in the body is carried through unchanged.
//
// The two are the same rewrite on both protocols and differ only in where the
// forced prompt lives, so promptField names the protocol's own field:
// `system` on the Messages path, `instructions` on the Responses one.
func projectNativeRequest(body []byte, m *meta.Meta, protocolName, promptField string) (*projectedRequest, *relaymodel.ErrorWithStatusCode) {
	fields, clientModel, stream, err := decodeNativeRequest(body)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "unmarshal_request_body_failed", http.StatusBadRequest)
	}
	if clientModel == "" {
		return nil, openai.ErrorWrapper(fmt.Errorf("%s 请求缺少 model", protocolName), "invalid_text_request", http.StatusBadRequest)
	}

	// Model mapping and stream detection, identical to the chat path.
	m.IsStream = stream
	m.OriginModelName = clientModel
	actualModel, _ := getMappedModelName(clientModel, m.ModelMapping)
	m.ActualModelName = actualModel
	if actualModel != clientModel {
		if fields["model"], err = json.Marshal(actualModel); err != nil {
			return nil, openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
		}
	}

	// Per-model overrides are written against a normalized request (context
	// window, max output tokens, input modalities), and this path deliberately
	// never builds one. Refuse by name instead of forwarding a request the admin
	// believes is constrained.
	if hasModelConfigOverrides(m) {
		return nil, openai.ErrorWrapper(
			fmt.Errorf("模型 %s 在渠道 %d 上配置了逐模型覆盖项，同协议直通不解析请求体，无法施加；请移除覆盖项或让该渠道走转换路径",
				actualModel, m.ChannelId),
			"model_config_override_needs_normalization", http.StatusBadRequest)
	}

	systemPromptReset := false
	if m.ForcedSystemPrompt != "" {
		// Same semantics as setSystemPrompt on the chat path: the operator's
		// prompt replaces the client's rather than being appended to. What the
		// client sent under that field is a string on the Responses path and may be
		// a string or a content-block array on the Messages one; either way it is
		// replaced, so the shape it had does not matter here.
		if fields[promptField], err = json.Marshal(m.ForcedSystemPrompt); err != nil {
			return nil, openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
		}
		systemPromptReset = true
	}

	patched, err := json.Marshal(fields)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "marshal_normalized_request_failed", http.StatusInternalServerError)
	}
	return &projectedRequest{Body: patched, Model: actualModel, SystemPromptReset: systemPromptReset}, nil
}

// projectMessagesRequest projects a raw Anthropic Messages body.
func projectMessagesRequest(body []byte, m *meta.Meta) (*projectedRequest, *relaymodel.ErrorWithStatusCode) {
	return projectNativeRequest(body, m, "/v1/messages", "system")
}

// projectResponsesRequest projects a raw Responses body. What the client sent
// under `instructions` is what the operator's prompt replaces; the hosted-tool
// declarations, `include`, `store` and the newer sampling fields travel in the
// untouched fields around it.
func projectResponsesRequest(body []byte, m *meta.Meta) (*projectedRequest, *relaymodel.ErrorWithStatusCode) {
	return projectNativeRequest(body, m, "/v1/responses", "instructions")
}

// relayMessagesPassthroughOnce forwards a non-streaming upstream body to the
// client and reads the usage fact out of it on the way past.
func relayMessagesPassthroughOnce(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	raw, err := readUpstreamBody(resp)
	if err != nil {
		recordUsage(ctx, m, projected.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}

	var usage *relaymodel.Usage
	var parsed anthropic.Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// The body still goes out untouched; only the usage fact is lost, and it
		// is lost loudly rather than as a zero.
		logger.Errorf(ctx, "failed to parse upstream messages body for usage: %s", err.Error())
	} else if u := anthropic.RelayUsage(parsed.Usage); !usageLooksUnreported(u) {
		usage = &u
	} else {
		// An all-zero usage means the upstream reported nothing, which is not the
		// same claim as "reported zero". Same rule as readOpenAIUsage: estimate the
		// completion locally and label it, rather than record a fabricated report.
		// The prompt side stays 0 — this path has no normalized request to count.
		completion := 0
		for _, block := range parsed.Content {
			completion += openai.CountTokenText(block.Text, projected.Model)
		}
		u := relaymodel.Usage{
			CompletionTokens: completion,
			TotalTokens:      completion,
			Source:           relaymodel.UsageSourceEstimated,
		}
		relaymodel.NormalizeUsage(&u)
		usage = &u
	}

	writeUpstreamBody(c, resp, raw)

	go recordConsumeLog(ctx, usage, m, projected.Model, projected.SystemPromptReset)
	go recordUsage(ctx, m, projected.Model, usage, relaymodel.UsageStatusSuccess)
	return nil
}

// messagesMirror accumulates everything the usage fact needs while the upstream's
// own event bytes go out to the client. It never re-renders an event: the client
// asked for Anthropic Messages and the upstream speaks Anthropic Messages, so a
// re-rendered envelope could only lose fidelity, and the payload's cache_control
// and thinking blocks must arrive as written.
type messagesMirror struct {
	usage relaymodel.Usage
	// The two halves of the usage are reported by different events: the input
	// side once by message_start, the output side by the terminal delta. An
	// aborted stream can arrive with only the first, so they are tracked apart
	// (§7.2 reported / estimated / nil ladder).
	inputReported  bool
	outputReported bool
	// usageText is the response as seen on the wire, used only for the local
	// estimate when the upstream never reported the output side. Tool-call
	// arguments are counted too: an upstream that streams a large call and no
	// text would otherwise be recorded as a handful of tokens (the same reason
	// sse.StreamState.ResponseTextForUsage counts them).
	usageText   strings.Builder
	terminal    bool
	upstreamErr *relaymodel.Error
}

// observe folds one Anthropic SSE event into the mirror and returns it in the
// adaptor's own StreamResponse shape, for callers that have to render it (the
// translated codec does; the passthrough does not, because it mirrors the
// upstream's bytes). ok is false for an event with no JSON payload at all — a
// keep-alive or a proxy's own frame — which is not an error: such an event was
// carried to the client and contributes nothing here.
func (mir *messagesMirror) observe(ev sse.Event) (*anthropic.StreamResponse, bool) {
	// The shape carries every field a caller may need, not only the ones the
	// mirror accumulates: content_block and index are what the adaptor's
	// converter reads to announce a tool call.
	var raw struct {
		Type         string              `json:"type"`
		Message      *anthropic.Response `json:"message"`
		Index        int                 `json:"index"`
		ContentBlock *anthropic.Content  `json:"content_block"`
		Delta        *anthropic.Delta    `json:"delta"`
		Usage        *anthropic.Usage    `json:"usage"`
		Error        *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &raw); err != nil {
		// Not a JSON event (a keep-alive or a proxy's own frame). It was mirrored
		// to the client regardless; there is nothing to accumulate.
		return nil, false
	}

	// The type lives in the `event:` frame and, redundantly, in the payload.
	// Either may be missing depending on the upstream, so prefer the frame and
	// fall back to the payload.
	eventType := ev.Type
	if eventType == "" {
		eventType = raw.Type
	}
	switch eventType {
	case "error":
		e := relaymodel.Error{Type: "server_error", Message: "upstream stream error"}
		if raw.Error != nil {
			if raw.Error.Type != "" {
				e.Type = raw.Error.Type
			}
			if raw.Error.Message != "" {
				e.Message = raw.Error.Message
			}
		}
		mir.upstreamErr = &e
	case "message_stop":
		mir.terminal = true
	}

	switch {
	case raw.Message != nil:
		// message_start: the only event reporting the input side.
		mir.inputReported = true
	case raw.Usage != nil:
		// message_delta: the terminal, cumulative output count.
		mir.outputReported = true
	}
	parsed := &anthropic.StreamResponse{
		Type:         eventType,
		Message:      raw.Message,
		Index:        raw.Index,
		ContentBlock: raw.ContentBlock,
		Delta:        raw.Delta,
		Usage:        raw.Usage,
	}
	// One shared accumulation rule with the chat adaptor's StreamHandler —
	// output_tokens is assigned, never added (see anthropic.AccumulateStreamUsage).
	anthropic.AccumulateStreamUsage(&mir.usage, parsed)

	if raw.Delta != nil {
		mir.usageText.WriteString(raw.Delta.Text)
		mir.usageText.WriteString(raw.Delta.PartialJson)
	}
	return parsed, true
}

// status mirrors sse.StreamState.TerminalStatus: an upstream error event wins, a
// stream that never reached message_stop is partial, everything else succeeded.
func (mir *messagesMirror) status(aborted bool) string {
	switch {
	case mir.upstreamErr != nil:
		return relaymodel.UsageStatusError
	case aborted || !mir.terminal:
		return relaymodel.UsageStatusPartial
	}
	return relaymodel.UsageStatusSuccess
}

// finalizeUsage picks the usage fact for the mirrored stream on the same
// reported / estimated / nil ladder the OpenAI path uses (§7.2). A stream that
// reported only its input side is labelled estimated as a whole: a mixed row
// must not claim that its numbers came from the upstream.
//
// promptFallback is the gateway's own estimate of the prompt, used only when the
// upstream never reported the input side. The passthrough has no normalized
// request to count one from and passes 0 — an invented number would be worse
// than a gap — while the translated codec passes the estimate it does have.
func (mir *messagesMirror) finalizeUsage(modelName string, promptFallback int) *relaymodel.Usage {
	switch {
	case mir.outputReported:
		u := mir.usage
		u.Source = relaymodel.UsageSourceReported
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
		relaymodel.NormalizeUsage(&u)
		return &u
	case mir.inputReported || mir.usageText.Len() > 0:
		prompt := mir.usage.PromptTokens
		if prompt == 0 {
			prompt = promptFallback
		}
		u := relaymodel.Usage{
			// The prompt side is what the upstream reported, or the caller's own
			// estimate when it reported nothing.
			PromptTokens:          prompt,
			CompletionTokens:      openai.CountTokenText(mir.usageText.String(), modelName),
			CacheReadInputTokens:  mir.usage.CacheReadInputTokens,
			CacheWriteInputTokens: mir.usage.CacheWriteInputTokens,
			Source:                relaymodel.UsageSourceEstimated,
		}
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
		relaymodel.NormalizeUsage(&u)
		return &u
	}
	return nil
}

// usageLooksUnreported applies the rule readOpenAIUsage uses for an OpenAI body:
// an all-zero usage carrying no cache field means the upstream said nothing. A
// reported zero (a cache field present and zero) is not this case, and neither is
// a partial report — those are real statements and must be kept as such.
func usageLooksUnreported(u relaymodel.Usage) bool {
	if u.CacheReadInputTokens != nil || u.CacheWriteInputTokens != nil {
		return false
	}
	return u.TotalTokens == 0 || (u.PromptTokens == 0 && u.CompletionTokens == 0)
}

// relayMessagesPassthroughStream mirrors the upstream SSE stream to the client
// and finalizes the usage fact exactly once, including the client-disconnect and
// upstream-break cases.
func relayMessagesPassthroughStream(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	sse.SetEventStreamHeaders(c.Writer)
	mirror := &messagesMirror{}
	aborted := mirrorUpstreamStream(c, resp, func(ev sse.Event) { mirror.observe(ev) })
	closeUpstreamBody(resp)

	status := mirror.status(aborted)
	// No prompt fallback: this path never built a normalized request, so there is
	// no local prompt estimate to fall back to.
	usage := mirror.finalizeUsage(projected.Model, 0)
	recordUsage(ctx, m, projected.Model, usage, status)
	go recordConsumeLog(ctx, usage, m, projected.Model, projected.SystemPromptReset)
	return nil
}

// --- the Responses protocol ----------------------------------------------

// relayResponsesPassthroughOnce forwards a non-streaming upstream body to the
// client and reads the usage fact out of it on the way past.
func relayResponsesPassthroughOnce(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	raw, err := readUpstreamBody(resp)
	if err != nil {
		recordUsage(ctx, m, projected.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}

	status := relaymodel.UsageStatusSuccess
	var usage *relaymodel.Usage
	var parsed openai_responses.Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// The body still goes out untouched; only the usage fact is lost, and it
		// is lost loudly rather than as a zero.
		logger.Errorf(ctx, "failed to parse upstream responses body for usage: %s", err.Error())
	} else {
		// A Responses answer can report a failure inside an HTTP 200, and an answer
		// that failed did not succeed here either. The body carrying it already went
		// to the client, which reads the same status from it.
		if parsed.Failure() != nil {
			status = relaymodel.UsageStatusError
		}
		if u, ok := openai_responses.ToUsage(parsed.Usage); ok {
			usage = u
		} else {
			// An upstream that reported nothing is not an upstream reporting zero:
			// the answer is counted locally and labelled estimated, as readOpenAIUsage
			// does. The prompt side stays 0 — this path has no normalized request to
			// count one from.
			completion := openai.CountTokenText(openai_responses.OutputText(&parsed), projected.Model)
			u := relaymodel.Usage{
				CompletionTokens: completion,
				TotalTokens:      completion,
				Source:           relaymodel.UsageSourceEstimated,
			}
			relaymodel.NormalizeUsage(&u)
			usage = &u
		}
	}

	writeUpstreamBody(c, resp, raw)

	if status == relaymodel.UsageStatusSuccess {
		go recordConsumeLog(ctx, usage, m, projected.Model, projected.SystemPromptReset)
	}
	go recordUsage(ctx, m, projected.Model, usage, status)
	return nil
}

// responsesMirror accumulates everything the usage fact needs while the
// upstream's own event bytes go out to the client. Like messagesMirror it never
// re-renders an event: the client asked for the Responses API and the upstream
// speaks it, so a re-rendered envelope could only lose fidelity — and the
// response ids, the reasoning items and the hosted-tool items must arrive as
// written.
//
// It is deliberately not openai_responses.StreamState, even though that type
// reads the same event stream: StreamState's job is to build a chat envelope, and
// it fails the stream when the answer cannot be expressed in one (a function_call
// with no call_id, say). A passthrough builds no envelope and must not fail a
// stream it has already forwarded intact, so the mirror tracks only what the
// usage fact is made of.
type responsesMirror struct {
	// usage is the last usage a response envelope reported, or nil when none has.
	// Unlike Anthropic's split input/output events, a Responses upstream reports
	// the whole fact at once, in the terminal envelope.
	usage *relaymodel.Usage
	// usageText is the answer as seen on the wire, used only for the local
	// estimate when the upstream never reported usage. Text and tool-call
	// arguments both count, as they do in sse.StreamState.ResponseTextForUsage;
	// reasoning text does not, there as here.
	usageText strings.Builder

	terminal bool
	failed   bool
}

// observe folds one upstream event into the mirror. It keeps no typed event
// shape: the passthrough writes the upstream's own bytes, so nothing downstream
// needs the decoded form.
func (mir *responsesMirror) observe(ev sse.Event) {
	var raw struct {
		Type     string                     `json:"type"`
		Response *openai_responses.Response `json:"response"`
		// Delta belongs to the output_text and function_call_arguments deltas.
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &raw); err != nil {
		// Not a JSON event (a keep-alive or a proxy's own frame). It was mirrored
		// to the client regardless; there is nothing to accumulate.
		return
	}

	// The type lives in the `event:` frame and, redundantly, in the payload.
	// Either may be missing depending on the upstream, so prefer the frame and
	// fall back to the payload.
	eventType := ev.Type
	if eventType == "" {
		eventType = raw.Type
	}
	switch eventType {
	case "response.output_text.delta", "response.function_call_arguments.delta":
		mir.usageText.WriteString(raw.Delta)
	case "response.completed", "response.incomplete":
		mir.terminal = true
	case "response.failed", "error":
		// The upstream's own failure, as a failed response or as a bare error
		// event. Both are terminal: nothing more is read after either.
		mir.terminal = true
		mir.failed = true
	}

	if raw.Response != nil {
		if u, ok := openai_responses.ToUsage(raw.Response.Usage); ok {
			mir.usage = u
		}
	}
}

// status mirrors sse.StreamState.TerminalStatus: an upstream failure wins, a
// stream that never reached a terminal event is partial, everything else
// succeeded.
func (mir *responsesMirror) status(aborted bool) string {
	switch {
	case mir.failed:
		return relaymodel.UsageStatusError
	case aborted || !mir.terminal:
		return relaymodel.UsageStatusPartial
	}
	return relaymodel.UsageStatusSuccess
}

// finalizeUsage picks the usage fact for the mirrored stream on the same
// reported / estimated / nil ladder the other paths use (§7.2), with the floor
// the Anthropic passthrough applies for the same reason: a stream that reported
// nothing and carried no text leaves nothing to count, so its record keeps the
// status alone rather than claiming an estimated zero.
func (mir *responsesMirror) finalizeUsage(modelName string) *relaymodel.Usage {
	if mir.usage == nil && mir.usageText.Len() == 0 {
		return nil
	}
	if mir.usage != nil {
		u := *mir.usage
		relaymodel.NormalizeUsage(&u)
		return &u
	}
	completion := openai.CountTokenText(mir.usageText.String(), modelName)
	u := relaymodel.Usage{
		CompletionTokens: completion,
		TotalTokens:      completion,
		Source:           relaymodel.UsageSourceEstimated,
	}
	relaymodel.NormalizeUsage(&u)
	return &u
}

// relayResponsesPassthroughStream mirrors the upstream event stream to the client
// and finalizes the usage fact exactly once, including the client-disconnect and
// upstream-break cases.
func relayResponsesPassthroughStream(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	sse.SetEventStreamHeaders(c.Writer)
	mirror := &responsesMirror{}
	aborted := mirrorUpstreamStream(c, resp, mirror.observe)
	closeUpstreamBody(resp)

	status := mirror.status(aborted)
	usage := mirror.finalizeUsage(projected.Model)
	recordUsage(ctx, m, projected.Model, usage, status)
	go recordConsumeLog(ctx, usage, m, projected.Model, projected.SystemPromptReset)
	return nil
}

// --- shared by both protocols -------------------------------------------

// mirrorUpstreamStream is the passthrough stream loop: read the upstream's own
// events, hand each to observe — which accumulates the usage fact — and re-frame
// it to the client unchanged. It reports whether the stream ended abnormally:
// the client went away, a write failed, or the connection broke (§7.1), which is
// the caller's partial marker.
func mirrorUpstreamStream(c *gin.Context, resp *http.Response, observe func(sse.Event)) bool {
	reader := sse.NewReader(resp.Body)
	writer := sse.NewWriter(c.Writer)
	ctx := c.Request.Context()

	for {
		// Client disconnect: stop writing and finalize what was seen as partial.
		if ctx.Err() != nil {
			return true
		}
		ev, err := reader.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			// Network break mid-stream -> partial terminal (§7.1).
			return true
		}
		observe(ev)
		if werr := writer.Raw(frameSSE(ev)); werr != nil {
			// Write failure means the client is gone.
			return true
		}
	}
}

// readUpstreamBody reads an upstream response body whole and closes it. Both
// passthrough sinks treat a read failure the same way, so only the bytes and the
// error are returned here.
func readUpstreamBody(resp *http.Response) ([]byte, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	closeUpstreamBody(resp)
	return raw, nil
}

func closeUpstreamBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		logger.SysError("close response body failed: " + err.Error())
	}
}

// writeUpstreamBody forwards an upstream body to the client verbatim, keeping
// the upstream's own media type and status: a passthrough that re-encoded the
// body would be a passthrough in name only.
func writeUpstreamBody(c *gin.Context, resp *http.Response, raw []byte) {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.WriteHeader(resp.StatusCode)
	if _, err := c.Writer.Write(raw); err != nil {
		logger.SysError("write response body failed: " + err.Error())
	}
}

// frameSSE re-frames one parsed event for the client. The payload is the
// upstream's own bytes; only the framing is rebuilt, because sse.Reader resolves
// comments, keep-alives and field lines into a single event. The `id:` field is
// carried over when present (neither the Anthropic nor the Responses protocol
// uses it, so this is about not inventing loss rather than about correctness).
func frameSSE(ev sse.Event) string {
	var b strings.Builder
	if ev.Type != "" {
		b.WriteString("event: ")
		b.WriteString(ev.Type)
		b.WriteByte('\n')
	}
	if ev.ID != "" {
		b.WriteString("id: ")
		b.WriteString(ev.ID)
		b.WriteByte('\n')
	}
	// Multi-line data must be re-prefixed line by line, per the SSE spec.
	for _, line := range strings.Split(ev.Data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}
