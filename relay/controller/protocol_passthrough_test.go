package controller

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
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

// The Responses passthrough arrives with A6/A7, but the declaration is honoured
// today: such a channel must not fall through to the chat pivot either.
func TestRelayPassthroughHonoursUnimplementedUpstream(t *testing.T) {
	perr := relayPassthrough(nil, &meta.Meta{ChannelId: 3}, &protocolEntry{Protocol: upstream.OpenAIResponses}, nil)
	if perr == nil {
		t.Fatal("expected a named refusal")
	}
	if perr.Error.Code != "upstream_protocol_not_implemented" {
		t.Fatalf("error code = %v, want upstream_protocol_not_implemented", perr.Error.Code)
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
	u := mir.finalizeUsage("claude")
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
	u := mir.finalizeUsage("claude")
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
	if u := mir.finalizeUsage("claude"); u != nil {
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
