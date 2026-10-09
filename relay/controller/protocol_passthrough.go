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
	switch entry.Protocol {
	case upstream.AnthropicMessages:
		return relayMessagesPassthrough(c, m, body)
	case upstream.OpenAIResponses:
		// The Responses upstream adaptor arrives with A6/A7. Until then the
		// declaration is honoured rather than ignored: this channel was declared
		// as speaking the Responses API, and sending it a chat/completions body —
		// which is what falling through to the pivot path would do — is exactly
		// what the declaration exists to prevent.
		return openai.ErrorWrapper(
			fmt.Errorf("渠道 %d 的上游协议为 openai-responses，其同协议直通尚未接入", m.ChannelId),
			"upstream_protocol_not_implemented", http.StatusBadRequest)
	}
	return openai.ErrorWrapper(
		fmt.Errorf("客户端协议 %s 暂不支持同协议直通", entry.Protocol),
		"passthrough_not_supported", http.StatusBadRequest)
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

// projectMessagesRequest applies the two rewrites the gateway owes the channel
// to a raw Anthropic Messages body. Everything else in the body is carried
// through unchanged.
func projectMessagesRequest(body []byte, m *meta.Meta) (*projectedRequest, *relaymodel.ErrorWithStatusCode) {
	fields, clientModel, stream, err := decodeNativeRequest(body)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "unmarshal_request_body_failed", http.StatusBadRequest)
	}
	if clientModel == "" {
		return nil, openai.ErrorWrapper(fmt.Errorf("/v1/messages 请求缺少 model"), "invalid_text_request", http.StatusBadRequest)
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
		// prompt replaces the client's rather than being appended to. Anthropic
		// carries it in the top-level `system` field, which may be a string or a
		// content-block array — either way the upstream must see this prompt.
		if fields["system"], err = json.Marshal(m.ForcedSystemPrompt); err != nil {
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

// relayMessagesPassthrough serves /v1/messages against an upstream that speaks
// the Anthropic Messages API.
func relayMessagesPassthrough(c *gin.Context, m *meta.Meta, body []byte) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	projected, perr := projectMessagesRequest(body, m)
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
		return relayMessagesPassthroughStream(c, m, projected, resp)
	}
	return relayMessagesPassthroughOnce(c, m, projected, resp)
}

// relayMessagesPassthroughOnce forwards a non-streaming upstream body to the
// client and reads the usage fact out of it on the way past.
func relayMessagesPassthroughOnce(c *gin.Context, m *meta.Meta, projected *projectedRequest, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		recordUsage(ctx, m, projected.Model, nil, relaymodel.UsageStatusError)
		return openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	if err := resp.Body.Close(); err != nil {
		logger.SysError("close response body failed: " + err.Error())
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
			completion += openai.CountTokenText(block.Text, m.ActualModelName)
		}
		u := relaymodel.Usage{
			CompletionTokens: completion,
			TotalTokens:      completion,
			Source:           relaymodel.UsageSourceEstimated,
		}
		relaymodel.NormalizeUsage(&u)
		usage = &u
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.WriteHeader(resp.StatusCode)
	if _, err := c.Writer.Write(raw); err != nil {
		logger.SysError("write response body failed: " + err.Error())
	}

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
	writer := sse.NewWriter(c.Writer)
	reader := sse.NewReader(resp.Body)

	mirror := &messagesMirror{}
	aborted := false
	for {
		// Client disconnect: stop writing and finalize what was seen as partial.
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
		mirror.observe(ev)
		if werr := writer.Raw(frameSSE(ev)); werr != nil {
			// Write failure means the client is gone.
			aborted = true
			break
		}
	}

	if err := resp.Body.Close(); err != nil {
		logger.SysError("close response body failed: " + err.Error())
	}

	status := mirror.status(aborted)
	// No prompt fallback: this path never built a normalized request, so there is
	// no local prompt estimate to fall back to.
	usage := mirror.finalizeUsage(m.ActualModelName, 0)
	recordUsage(ctx, m, projected.Model, usage, status)
	go recordConsumeLog(ctx, usage, m, projected.Model, projected.SystemPromptReset)
	return nil
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
