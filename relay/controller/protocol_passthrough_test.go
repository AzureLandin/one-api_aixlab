package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

// nativeMessagesBody is an Anthropic Messages request carrying exactly what the
// chat-pivot path cannot represent: cache_control on the system prompt and on a
// content block, a thinking budget, a server-side (hosted) tool declaration, and
// two fields this repository's own Request struct does not even model
// (metadata, service_tier). Passthrough exists so that none of it is lost.
const nativeMessagesBody = `{
  "model": "claude-3-7-sonnet",
  "max_tokens": 1024,
  "stream": true,
  "system": [{"type":"text","text":"be brief","cache_control":{"type":"ephemeral"}}],
  "thinking": {"type":"enabled","budget_tokens":2048},
  "messages": [{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],
  "tools": [
    {"type":"web_search_20250305","name":"web_search","max_uses":5},
    {"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}
  ],
  "tool_choice": {"type":"auto","disable_parallel_tool_use":true},
  "metadata": {"user_id": "u1"},
  "service_tier": "standard_only"
}`

func decodeJSONFields(t *testing.T, body []byte) map[string]any {
	t.Helper()
	fields := map[string]any{}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode JSON body: %v\nbody: %s", err, body)
	}
	return fields
}

func projectMessages(t *testing.T, body string, m *meta.Meta) *projectedRequest {
	t.Helper()
	projected, perr := projectMessagesRequest([]byte(body), m)
	if perr != nil {
		t.Fatalf("projection failed: %+v", perr)
	}
	return projected
}

// Everything the client sent survives the projection with its value intact;
// nothing is dropped, re-shaped or invented.
func TestProjectMessagesRequestPreservesNativeFields(t *testing.T) {
	projected := projectMessages(t, nativeMessagesBody, &meta.Meta{})

	want := decodeJSONFields(t, []byte(nativeMessagesBody))
	got := decodeJSONFields(t, projected.Body)

	if len(got) != len(want) {
		t.Fatalf("field count changed: got %d (%v), want %d", len(got), got, len(want))
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Fatalf("field %q was dropped", key)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("field %q changed:\n got %#v\nwant %#v", key, gotValue, wantValue)
		}
	}

	// The properties the round-trip through GeneralOpenAIRequest cannot keep.
	for _, needle := range []string{"cache_control", "thinking", "web_search_20250305", "service_tier"} {
		if !strings.Contains(string(projected.Body), needle) {
			t.Fatalf("projected body lost %q: %s", needle, projected.Body)
		}
	}
}

// With no mapping and no forced prompt, the projection is a pure re-encoding:
// only the model field may differ, and here it does not.
func TestProjectMessagesRequestWithoutRewritesKeepsModel(t *testing.T) {
	m := &meta.Meta{}
	projected := projectMessages(t, nativeMessagesBody, m)

	if projected.Model != "claude-3-7-sonnet" {
		t.Fatalf("Model = %s, want claude-3-7-sonnet", projected.Model)
	}
	if projected.SystemPromptReset {
		t.Fatal("SystemPromptReset = true, want false")
	}
	if m.OriginModelName != "claude-3-7-sonnet" || m.ActualModelName != "claude-3-7-sonnet" {
		t.Fatalf("model names not recorded: origin=%s actual=%s", m.OriginModelName, m.ActualModelName)
	}
	if !m.IsStream {
		t.Fatal("IsStream = false, want true (the body says stream:true)")
	}
}

// The model mapping is applied to the model field and to no other field.
func TestProjectMessagesRequestPatchesOnlyModel(t *testing.T) {
	m := &meta.Meta{ModelMapping: map[string]string{"claude-3-7-sonnet": "claude-upstream-v2"}}
	projected := projectMessages(t, nativeMessagesBody, m)

	want := decodeJSONFields(t, []byte(nativeMessagesBody))
	got := decodeJSONFields(t, projected.Body)

	if got["model"] != "claude-upstream-v2" {
		t.Fatalf("model = %v, want claude-upstream-v2", got["model"])
	}
	delete(got, "model")
	delete(want, "model")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the model patch touched other fields:\n got %#v\nwant %#v", got, want)
	}
	if projected.Model != "claude-upstream-v2" || m.ActualModelName != "claude-upstream-v2" {
		t.Fatalf("mapped name not propagated: %s / %s", projected.Model, m.ActualModelName)
	}
	if m.OriginModelName != "claude-3-7-sonnet" {
		t.Fatalf("OriginModelName = %s, want the client's own name", m.OriginModelName)
	}
}

// The forced system prompt mirrors setSystemPrompt on the chat path: it replaces
// what the client sent (a content-block array here) rather than being appended.
func TestProjectMessagesRequestForcedPromptReplacesSystem(t *testing.T) {
	m := &meta.Meta{ForcedSystemPrompt: "operator prompt"}
	projected := projectMessages(t, nativeMessagesBody, m)

	if !projected.SystemPromptReset {
		t.Fatal("SystemPromptReset = false, want true")
	}
	got := decodeJSONFields(t, projected.Body)
	if got["system"] != "operator prompt" {
		t.Fatalf("system = %#v, want the operator prompt as a string", got["system"])
	}
	// Everything else is still untouched.
	want := decodeJSONFields(t, []byte(nativeMessagesBody))
	delete(want, "system")
	delete(got, "system")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forced prompt rewrite touched other fields:\n got %#v\nwant %#v", got, want)
	}
}

// A per-model override cannot be enforced on a body this path never parses, so
// the request is refused by name instead of forwarded as if it were constrained.
func TestProjectMessagesRequestRejectsModelConfigOverride(t *testing.T) {
	m := &meta.Meta{
		ChannelId: 42,
		Config: model.ChannelConfig{
			ModelConfigs: map[string]model.ModelConfig{
				"claude-3-7-sonnet": {ContextWindow: 8192},
			},
		},
	}
	_, perr := projectMessagesRequest([]byte(nativeMessagesBody), m)
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "model_config_override_needs_normalization" {
		t.Fatalf("error code = %v, want model_config_override_needs_normalization", perr.Error.Code)
	}
}

func TestProjectMessagesRequestRejectsBodyWithoutModel(t *testing.T) {
	_, perr := projectMessagesRequest([]byte(`{"max_tokens":16,"messages":[]}`), &meta.Meta{})
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "invalid_text_request" {
		t.Fatalf("error code = %v, want invalid_text_request", perr.Error.Code)
	}
}

// The Responses passthrough used to be refused outright ("its upstream adapter
// arrives with A6/A7"); it is wired now, and the dispatch hands the body to the
// Responses projection. The sink itself is covered end to end by the smoke
// matrix, so this test stops where the request would leave for the upstream: an
// api type with no adaptor at all, which is also what keeps it free of a
// database (that exit records nothing).
func TestRelayPassthroughDispatchesResponsesToItsProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	m := &meta.Meta{ChannelId: 3, APIType: apitype.Dummy}
	perr := relayPassthrough(c, m, &protocolEntry{Protocol: upstream.OpenAIResponses}, []byte(`{"model":"gpt-5","stream":false}`))
	if perr != nil && perr.Error.Code == "upstream_protocol_not_implemented" {
		t.Fatal("the Responses passthrough is still unimplemented")
	}
	if m.OriginModelName != "gpt-5" || m.ActualModelName != "gpt-5" {
		t.Fatalf("the body never reached the Responses projection: origin=%q actual=%q", m.OriginModelName, m.ActualModelName)
	}
	if m.IsStream {
		t.Fatal("IsStream = true, want false (the body says stream:false)")
	}
}

func TestRelayPassthroughRefusesUnknownClientProtocol(t *testing.T) {
	perr := relayPassthrough(nil, &meta.Meta{ChannelId: 3}, &protocolEntry{Protocol: upstream.Compatible}, nil)
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "passthrough_not_supported" {
		t.Fatalf("error code = %v, want passthrough_not_supported", perr.Error.Code)
	}
}

// --- Responses projection ------------------------------------------------

// nativeResponsesBody is a Responses request carrying exactly what the chat
// pivot cannot represent: a hosted tool declaration, the reasoning
// configuration, encrypted reasoning content in the answer, a stored response,
// and previous_response_id — which the normalized request refuses outright, so a
// client could not use it through the translation path at all. Passthrough
// exists so that none of it is lost.
const nativeResponsesBody = `{
  "model": "gpt-5-upstream",
  "instructions": "be brief",
  "input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
  "stream": true,
  "max_output_tokens": 512,
  "tools": [
    {"type":"web_search"},
    {"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}
  ],
  "reasoning": {"effort":"high","summary":"auto"},
  "include": ["reasoning.encrypted_content"],
  "store": true,
  "previous_response_id": "resp_previous",
  "metadata": {"user_id": "u1"},
  "text": {"format": {"type":"json_schema","name":"answer","schema":{"type":"object"}}}
}`

func projectResponses(t *testing.T, body string, m *meta.Meta) *projectedRequest {
	t.Helper()
	projected, perr := projectResponsesRequest([]byte(body), m)
	if perr != nil {
		t.Fatalf("projection failed: %+v", perr)
	}
	return projected
}

// Everything the client sent survives the projection with its value intact;
// nothing is dropped, re-shaped or invented.
func TestProjectResponsesRequestPreservesNativeFields(t *testing.T) {
	projected := projectResponses(t, nativeResponsesBody, &meta.Meta{})

	want := decodeJSONFields(t, []byte(nativeResponsesBody))
	got := decodeJSONFields(t, projected.Body)

	if len(got) != len(want) {
		t.Fatalf("field count changed: got %d (%v), want %d", len(got), got, len(want))
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Fatalf("field %q was dropped", key)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("field %q changed:\n got %#v\nwant %#v", key, gotValue, wantValue)
		}
	}

	// The properties the round-trip through GeneralOpenAIRequest cannot keep.
	for _, needle := range []string{"web_search", "previous_response_id", "reasoning.encrypted_content", "json_schema"} {
		if !strings.Contains(string(projected.Body), needle) {
			t.Fatalf("projected body lost %q: %s", needle, projected.Body)
		}
	}
}

// The model mapping is applied to the model field and to no other field.
func TestProjectResponsesRequestPatchesOnlyModel(t *testing.T) {
	m := &meta.Meta{ModelMapping: map[string]string{"gpt-5-upstream": "gpt-5-v2"}}
	projected := projectResponses(t, nativeResponsesBody, m)

	want := decodeJSONFields(t, []byte(nativeResponsesBody))
	got := decodeJSONFields(t, projected.Body)

	if got["model"] != "gpt-5-v2" {
		t.Fatalf("model = %v, want gpt-5-v2", got["model"])
	}
	delete(got, "model")
	delete(want, "model")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the model patch touched other fields:\n got %#v\nwant %#v", got, want)
	}
	if projected.Model != "gpt-5-v2" || m.ActualModelName != "gpt-5-v2" {
		t.Fatalf("mapped name not propagated: %s / %s", projected.Model, m.ActualModelName)
	}
	if m.OriginModelName != "gpt-5-upstream" {
		t.Fatalf("OriginModelName = %s, want the client's own name", m.OriginModelName)
	}
	if !m.IsStream {
		t.Fatal("IsStream = false, want true (the body says stream:true)")
	}
}

// The forced prompt mirrors setSystemPrompt on the chat path: it replaces the
// client's own instructions rather than being appended to them.
func TestProjectResponsesRequestForcedPromptReplacesInstructions(t *testing.T) {
	m := &meta.Meta{ForcedSystemPrompt: "operator prompt"}
	projected := projectResponses(t, nativeResponsesBody, m)

	if !projected.SystemPromptReset {
		t.Fatal("SystemPromptReset = false, want true")
	}
	got := decodeJSONFields(t, projected.Body)
	if got["instructions"] != "operator prompt" {
		t.Fatalf("instructions = %#v, want the operator prompt", got["instructions"])
	}
	// Everything else is still untouched.
	want := decodeJSONFields(t, []byte(nativeResponsesBody))
	delete(want, "instructions")
	delete(got, "instructions")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forced prompt rewrite touched other fields:\n got %#v\nwant %#v", got, want)
	}
}

// A per-model override cannot be enforced on a body this path never parses, so
// the request is refused by name instead of forwarded as if it were constrained.
func TestProjectResponsesRequestRejectsModelConfigOverride(t *testing.T) {
	m := &meta.Meta{
		ChannelId: 42,
		Config: model.ChannelConfig{
			ModelConfigs: map[string]model.ModelConfig{
				"gpt-5-upstream": {ContextWindow: 8192},
			},
		},
	}
	_, perr := projectResponsesRequest([]byte(nativeResponsesBody), m)
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "model_config_override_needs_normalization" {
		t.Fatalf("error code = %v, want model_config_override_needs_normalization", perr.Error.Code)
	}
}

func TestProjectResponsesRequestRejectsBodyWithoutModel(t *testing.T) {
	_, perr := projectResponsesRequest([]byte(`{"input":"hi","max_output_tokens":16}`), &meta.Meta{})
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "invalid_text_request" {
		t.Fatalf("error code = %v, want invalid_text_request", perr.Error.Code)
	}
}

// --- stream mirror -------------------------------------------------------

func observeAll(mir *messagesMirror, events ...sse.Event) {
	for _, ev := range events {
		mir.observe(ev)
	}
}

func anthropicEvent(eventType, data string) sse.Event {
	return sse.Event{Type: eventType, Data: data}
}

// approximateTokenCounting switches the local token estimate to the length-based
// approximation for the duration of a test. The tiktoken encoders are built by
// main() at startup, not by the test binary, and reaching for one here would
// couple these tests to an encoder download; the assertions are about which rung
// of the reported / estimated / nil ladder was taken, not about a token count.
func approximateTokenCounting(t *testing.T) {
	t.Helper()
	prev := config.ApproximateTokenEnabled
	config.ApproximateTokenEnabled = true
	t.Cleanup(func() { config.ApproximateTokenEnabled = prev })
}

// A stream that reported both halves yields a reported fact, with the input side
// taken from message_start and the output side from the terminal delta.
func TestMessagesMirrorReportedStream(t *testing.T) {
	mir := &messagesMirror{}
	observeAll(mir,
		anthropicEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":40}}}`),
		anthropicEvent("content_block_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello there"}}`),
		anthropicEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`),
		anthropicEvent("message_stop", `{"type":"message_stop"}`),
	)

	if status := mir.status(false); status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", status)
	}
	u := mir.finalizeUsage("claude", 0)
	if u == nil {
		t.Fatal("usage is nil, want a reported fact")
	}
	if u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("Source = %s, want reported", u.Source)
	}
	if u.PromptTokens != 100 || u.CompletionTokens != 12 || u.TotalTokens != 112 {
		t.Fatalf("usage = %+v, want prompt 100 completion 12 total 112", u)
	}
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("cache read = %v, want 40", u.CacheReadInputTokens)
	}
	// Never reported -> nil, never a fabricated zero.
	if u.CacheWriteInputTokens != nil {
		t.Fatalf("cache write = %v, want nil", u.CacheWriteInputTokens)
	}
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
}

// A stream that ends without message_stop (client gone, upstream break) is
// partial, and its usage is labelled estimated: an input-only report must not be
// presented as if the upstream had reported the whole fact.
func TestMessagesMirrorInterruptedStreamIsPartialAndEstimated(t *testing.T) {
	approximateTokenCounting(t)
	mir := &messagesMirror{}
	observeAll(mir,
		anthropicEvent("message_start", `{"type":"message_start","message":{"id":"msg_2","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":40}}}`),
		anthropicEvent("content_block_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello there"}}`),
	)
	if status := mir.status(true); status != relaymodel.UsageStatusPartial {
		t.Fatalf("status = %s, want partial", status)
	}
	u := mir.finalizeUsage("claude", 0)
	if u == nil {
		t.Fatal("usage is nil, want an estimated fact")
	}
	if u.Source != relaymodel.UsageSourceEstimated {
		t.Fatalf("Source = %s, want estimated", u.Source)
	}
	// The input side really was reported, so it is kept rather than zeroed.
	if u.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want the reported 100", u.PromptTokens)
	}
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("reported cache read must survive the estimate, got %v", u.CacheReadInputTokens)
	}
}

// The same absence of message_stop on a clean EOF is partial too, not success.
func TestMessagesMirrorMissingTerminalIsPartial(t *testing.T) {
	mir := &messagesMirror{}
	observeAll(mir, anthropicEvent("", `{"type":"message_delta","usage":{"output_tokens":5}}`))
	if status := mir.status(false); status != relaymodel.UsageStatusPartial {
		t.Fatalf("status = %s, want partial", status)
	}
}

func TestMessagesMirrorErrorEventIsError(t *testing.T) {
	mir := &messagesMirror{}
	observeAll(mir, anthropicEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	if status := mir.status(false); status != relaymodel.UsageStatusError {
		t.Fatalf("status = %s, want error", status)
	}
	if mir.upstreamErr == nil || mir.upstreamErr.Type != "overloaded_error" {
		t.Fatalf("upstream error not captured: %+v", mir.upstreamErr)
	}
	// Nothing was reported, so there is no usage fact to invent.
	if u := mir.finalizeUsage("claude", 0); u != nil {
		t.Fatalf("usage = %+v, want nil when nothing was reported", u)
	}
}

// The type may live only in the payload when the upstream omits the `event:`
// frame; the terminal detection must not depend on the frame.
func TestMessagesMirrorReadsTerminalFromPayloadType(t *testing.T) {
	mir := &messagesMirror{}
	observeAll(mir,
		anthropicEvent("", `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`),
		anthropicEvent("", `{"type":"message_delta","usage":{"output_tokens":4}}`),
		anthropicEvent("", `{"type":"message_stop"}`),
	)
	if status := mir.status(false); status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", status)
	}
}

// An event that is not JSON (a keep-alive, a proxy's own frame) is still
// mirrored to the client; it simply contributes nothing to the usage fact.
func TestMessagesMirrorIgnoresNonJSONEvent(t *testing.T) {
	mir := &messagesMirror{}
	observeAll(mir, anthropicEvent("", "ping"))
	if mir.inputReported || mir.outputReported || mir.terminal {
		t.Fatal("a non-JSON event changed the mirror state")
	}
}

// --- responses stream mirror ---------------------------------------------

func responsesEvent(eventType, data string) sse.Event {
	return sse.Event{Type: eventType, Data: data}
}

// A stream whose terminal envelope reports usage yields a reported fact,
// including the cache read that only the Responses spelling carries.
func TestResponsesMirrorReportedStream(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir,
		responsesEvent("response.created", `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5","usage":null}}`),
		responsesEvent("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hello"}`),
		responsesEvent("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed",
			"usage":{"input_tokens":50,"output_tokens":12,"total_tokens":62,
			         "input_tokens_details":{"cached_tokens":40},
			         "output_tokens_details":{"reasoning_tokens":3}}}}`),
	)

	if status := mir.status(false); status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", status)
	}
	u := mir.finalizeUsage("gpt-5")
	if u == nil {
		t.Fatal("usage is nil, want a reported fact")
	}
	if u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("Source = %s, want reported", u.Source)
	}
	if u.PromptTokens != 50 || u.CompletionTokens != 12 || u.TotalTokens != 62 {
		t.Fatalf("usage = %+v, want prompt 50 completion 12 total 62", u)
	}
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("cache read = %v, want 40", u.CacheReadInputTokens)
	}
	// Never reported -> nil, never a fabricated zero.
	if u.CacheWriteInputTokens != nil {
		t.Fatalf("cache write = %v, want nil", u.CacheWriteInputTokens)
	}
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
	if u.CompletionTokensDetails == nil || u.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %+v, want 3", u.CompletionTokensDetails)
	}
}

// A usage object that reports a zero cache read is still a report, not silence:
// the pointer is present, so the fact is reported with the upstream's own zero.
func TestResponsesMirrorReportedZeroStaysReported(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir, responsesEvent("response.completed", `{"type":"response.completed","response":{
		"status":"completed","usage":{"input_tokens":0,"output_tokens":0,
		"input_tokens_details":{"cached_tokens":0}}}}`))

	u := mir.finalizeUsage("gpt-5")
	if u == nil {
		t.Fatal("usage is nil, want a reported fact")
	}
	if u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("Source = %s, want reported: a reported zero is not an estimate", u.Source)
	}
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 0 {
		t.Fatalf("cache read = %v, want a reported 0", u.CacheReadInputTokens)
	}
}

// A stream that ends without a terminal event (client gone, upstream break) is
// partial, and its usage is labelled estimated from what was streamed.
func TestResponsesMirrorMissingTerminalIsPartialAndEstimated(t *testing.T) {
	approximateTokenCounting(t)
	mir := &responsesMirror{}
	observeResponses(mir,
		responsesEvent("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hello there"}`),
	)
	if status := mir.status(true); status != relaymodel.UsageStatusPartial {
		t.Fatalf("status = %s, want partial", status)
	}
	u := mir.finalizeUsage("gpt-5")
	if u == nil {
		t.Fatal("usage is nil, want an estimated fact")
	}
	if u.Source != relaymodel.UsageSourceEstimated {
		t.Fatalf("Source = %s, want estimated", u.Source)
	}
	if u.CompletionTokens == 0 {
		t.Fatal("CompletionTokens = 0, want the streamed text counted")
	}
	// Never reported -> nil, never a fabricated zero.
	if u.CacheReadInputTokens != nil {
		t.Fatalf("cache read = %v, want nil", u.CacheReadInputTokens)
	}
}

// Tool-call arguments count towards the local estimate, so an answer that
// streams only a call is not recorded as empty.
func TestResponsesMirrorCountsToolArgumentsInTheEstimate(t *testing.T) {
	approximateTokenCounting(t)
	mir := &responsesMirror{}
	observeResponses(mir,
		responsesEvent("response.output_item.added", `{"type":"response.output_item.added","output_index":0,
			"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather"}}`),
		responsesEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta",
			"output_index":0,"delta":"{\"city\":\"Shanghai\"}"}`),
	)
	u := mir.finalizeUsage("gpt-5")
	if u == nil {
		t.Fatal("usage is nil, want an estimated fact")
	}
	if u.CompletionTokens == 0 {
		t.Fatal("CompletionTokens = 0, want the streamed arguments counted")
	}
}

// An upstream that reports a failure is an error even though the stream ended
// cleanly, and a failed answer with nothing to count yields no usage fact.
func TestResponsesMirrorFailedEventIsError(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir, responsesEvent("response.failed", `{"type":"response.failed","response":{
		"status":"failed","error":{"code":"server_error","message":"upstream exploded"}}}`))

	if status := mir.status(false); status != relaymodel.UsageStatusError {
		t.Fatalf("status = %s, want error", status)
	}
	if u := mir.finalizeUsage("gpt-5"); u != nil {
		t.Fatalf("usage = %+v, want nil when nothing was reported", u)
	}
}

// The bare error event is a terminal failure too.
func TestResponsesMirrorErrorEventIsError(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir, responsesEvent("error", `{"type":"error","code":"rate_limit","message":"slow down"}`))
	if status := mir.status(false); status != relaymodel.UsageStatusError {
		t.Fatalf("status = %s, want error", status)
	}
}

// The type may live only in the payload when the upstream omits the `event:`
// frame; the terminal detection must not depend on the frame.
func TestResponsesMirrorReadsTerminalFromPayloadType(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir, responsesEvent("", `{"type":"response.completed","response":{"status":"completed",
		"usage":{"input_tokens":10,"output_tokens":4}}}`))
	if status := mir.status(false); status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", status)
	}
}

// An event that is not JSON (a keep-alive, a proxy's own frame) is still
// mirrored to the client; it simply contributes nothing to the usage fact.
func TestResponsesMirrorIgnoresNonJSONEvent(t *testing.T) {
	mir := &responsesMirror{}
	observeResponses(mir, responsesEvent("", "ping"))
	if mir.terminal || mir.failed || mir.usage != nil || mir.usageText.Len() != 0 {
		t.Fatal("a non-JSON event changed the mirror state")
	}
}

func observeResponses(mir *responsesMirror, events ...sse.Event) {
	for _, ev := range events {
		mir.observe(ev)
	}
}

func TestUsageLooksUnreported(t *testing.T) {
	zero := int64(0)
	forty := int64(40)
	cases := []struct {
		name string
		u    relaymodel.Usage
		want bool
	}{
		{"silent upstream", relaymodel.Usage{}, true},
		{"zero total only", relaymodel.Usage{TotalTokens: 0, PromptTokens: 12, CompletionTokens: 0}, true},
		{"reported prompt", relaymodel.Usage{PromptTokens: 5, TotalTokens: 5}, false},
		// A reported cache field is the upstream speaking, so a zero prompt /
		// zero completion alongside it is still a report, not silence.
		{"reported zero cache", relaymodel.Usage{CacheReadInputTokens: &zero}, false},
		{"reported cache read", relaymodel.Usage{CacheReadInputTokens: &forty, PromptTokens: 50, TotalTokens: 50}, false},
		{"reported zero cache write", relaymodel.Usage{CacheWriteInputTokens: &zero}, false},
	}
	for _, tc := range cases {
		if got := usageLooksUnreported(tc.u); got != tc.want {
			t.Errorf("%s: usageLooksUnreported(%+v) = %v, want %v", tc.name, tc.u, got, tc.want)
		}
	}
}

func TestFrameSSEPreservesPayload(t *testing.T) {
	payload := `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"a\nb"}}`
	framed := frameSSE(sse.Event{Type: "content_block_delta", ID: "evt_1", Data: payload})

	want := "event: content_block_delta\nid: evt_1\ndata: " + payload + "\n\n"
	if framed != want {
		t.Fatalf("framed event:\n got %q\nwant %q", framed, want)
	}
	// Multi-line data must be re-prefixed line by line, per the SSE spec.
	multi := frameSSE(sse.Event{Data: "a\nb"})
	if multi != "data: a\ndata: b\n\n" {
		t.Fatalf("multi-line framing = %q", multi)
	}
}
