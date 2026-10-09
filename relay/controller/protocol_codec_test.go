package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

func responseWithBody(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

// --- request bodies ---------------------------------------------------------

// The chat codec must keep rendering the request exactly as the protocol path
// did before the codecs existed, and ask for the chat path whatever entry point
// the client used.
func TestOpenAIChatCodecRequestBodyIsTheNormalizedRequest(t *testing.T) {
	req := &relaymodel.GeneralOpenAIRequest{
		Model:    "gpt-4o",
		Messages: []relaymodel.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	}
	body, path, err := openaiChatCodec{}.RequestBody(&meta.Meta{}, req)
	if err != nil {
		t.Fatalf("RequestBody: %v", err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", path)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want, _ := json.Marshal(req)
	if string(raw) != string(want) {
		t.Fatalf("body changed:\n got %s\nwant %s", raw, want)
	}
}

// The Anthropic codec posts the adaptor's own conversion, so a request
// translated here is byte-identical to the one /v1/chat/completions sends to an
// Anthropic channel.
func TestAnthropicChatCodecRequestBodyIsTheAdaptorConversion(t *testing.T) {
	req := &relaymodel.GeneralOpenAIRequest{
		Model:     "claude-3-7-sonnet",
		MaxTokens: 64,
		Messages: []relaymodel.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
		Tools: []relaymodel.Tool{{
			Type:     "function",
			Function: relaymodel.Function{Name: "get_time", Parameters: map[string]any{"type": "object"}},
		}},
	}
	body, path, err := anthropicChatCodec{}.RequestBody(&meta.Meta{}, req)
	if err != nil {
		t.Fatalf("RequestBody: %v", err)
	}
	if path != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", path)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want, _ := json.Marshal(anthropic.ConvertRequest(*req))
	if string(raw) != string(want) {
		t.Fatalf("body is not the adaptor's conversion:\n got %s\nwant %s", raw, want)
	}
}

func TestUpstreamCodecForRefusesBySide(t *testing.T) {
	entry := &protocolEntry{Name: "/v1/responses", Protocol: upstream.OpenAIResponses}

	// Declared-but-unwired is its own code: A6/A7 will implement it, and until
	// then the declaration must not be ignored.
	_, perr := upstreamCodecFor(&meta.Meta{ChannelId: 7}, upstream.OpenAIResponses, entry)
	if perr == nil || perr.Error.Code != "upstream_protocol_not_implemented" {
		t.Fatalf("responses upstream: %+v, want upstream_protocol_not_implemented", perr)
	}

	// A family with no translator at all is a different answer.
	_, perr = upstreamCodecFor(&meta.Meta{ChannelId: 7}, upstream.Unsupported, entry)
	if perr == nil || perr.Error.Code != "upstream_protocol_unsupported" {
		t.Fatalf("untranslated upstream: %+v, want upstream_protocol_unsupported", perr)
	}

	for _, up := range []upstream.Protocol{upstream.OpenAIChat, upstream.Compatible} {
		codec, perr := upstreamCodecFor(&meta.Meta{}, up, entry)
		if perr != nil || codec.Protocol() != upstream.OpenAIChat {
			t.Fatalf("%s: codec %v, err %+v; want the chat codec", up, codec, perr)
		}
	}
	codec, perr := upstreamCodecFor(&meta.Meta{}, upstream.AnthropicMessages, entry)
	if perr != nil || codec.Protocol() != upstream.AnthropicMessages {
		t.Fatalf("anthropic upstream: codec %v, err %+v; want the anthropic codec", codec, perr)
	}
}

// --- the chat codec keeps the historical behaviour --------------------------

// The chat codec replaced an inline read loop. This asserts it still yields
// exactly the chunks that loop produced, from the same fixture, and that a
// terminal [DONE] is still what ends the stream.
func TestOpenAIChatStreamMatchesTheInlineLoop(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
		``,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
		``,
		`data: not-json`,
		``,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	m := &meta.Meta{PromptTokens: 4, ActualModelName: "gpt-4o"}
	stream := openaiChatCodec{}.Stream(m, responseWithBody(fixture))

	// The same fixture read by the historical algorithm.
	state := sse.NewState()
	reader := sse.NewReader(strings.NewReader(fixture))
	var want []sse.Chunk
	for {
		ev, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reference reader: %v", err)
		}
		if aerr := state.Absorb(ev); aerr != nil {
			continue
		}
		if ev.IsDone() {
			break
		}
		chunk, perr := sse.ParseChunk(ev.Data)
		if perr != nil {
			continue
		}
		want = append(want, chunk)
	}

	var got []sse.Chunk
	for {
		chunk, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("codec stream: %v", err)
		}
		got = append(got, chunk)
	}
	if len(got) != len(want) {
		t.Fatalf("chunk count = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range got {
		if !jsonEqual(t, got[i], want[i]) {
			t.Fatalf("chunk %d differs:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}

	outcome := stream.Finalize(false)
	if outcome.Status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", outcome.Status)
	}
	if outcome.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", outcome.FinishReason)
	}
	if outcome.Usage == nil || outcome.Usage.PromptTokens != 4 || outcome.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v, want the reported 4/2", outcome.Usage)
	}
	if outcome.Usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("source = %s, want reported", outcome.Usage.Source)
	}
}

// A stream that ends without [DONE] is partial, not a clean success — the rule
// the inline loop applied through TerminalStatus and which the codec must keep.
func TestOpenAIChatStreamWithoutDoneIsPartial(t *testing.T) {
	approximateTokenCounting(t)
	fixture := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	stream := openaiChatCodec{}.Stream(&meta.Meta{ActualModelName: "gpt-4o"}, responseWithBody(fixture))
	for {
		if _, err := stream.Next(); err != nil {
			break
		}
	}
	if outcome := stream.Finalize(false); outcome.Status != relaymodel.UsageStatusPartial {
		t.Fatalf("status = %s, want partial", outcome.Status)
	}
}

// --- the Anthropic codec ----------------------------------------------------

// anthropicSSE is a real Anthropic stream: a text block, a tool_use block whose
// arguments arrive in two fragments, and a terminal delta reporting the
// CUMULATIVE output count. The text block occupies content block index 0 and the
// tool call index 1, so an implementation that used the block index as the
// OpenAI tool index would emit index 1 instead of the dense 0.
const anthropicSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_up","type":"message","role":"assistant","model":"claude-upstream","content":[],"stop_reason":null,"usage":{"input_tokens":30,"output_tokens":1,"cache_read_input_tokens":7}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_time","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"UTC\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`

func drainAnthropicStream(t *testing.T, m *meta.Meta, body string) ([]sse.Chunk, streamOutcome) {
	t.Helper()
	stream := anthropicChatCodec{}.Stream(m, responseWithBody(body))
	var chunks []sse.Chunk
	for {
		chunk, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("codec stream: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks, stream.Finalize(false)
}

func TestAnthropicChatStreamTranslatesTextAndTools(t *testing.T) {
	chunks, outcome := drainAnthropicStream(t, &meta.Meta{ActualModelName: "claude-3-7-sonnet", PromptTokens: 30}, anthropicSSE)

	var text strings.Builder
	tools := map[int]*sse.ToolDelta{}
	for _, chunk := range chunks {
		text.WriteString(chunk.TextDelta())
		for _, td := range chunk.ToolDeltas() {
			acc := tools[td.Index]
			if acc == nil {
				acc = &sse.ToolDelta{Index: td.Index}
				tools[td.Index] = acc
			}
			if td.ID != "" {
				acc.ID = td.ID
			}
			if td.Name != "" {
				acc.Name = td.Name
			}
			acc.Arguments += td.Arguments
		}
	}
	if text.String() != "hi" {
		t.Fatalf("text = %q, want %q", text.String(), "hi")
	}
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1 (%+v)", len(tools), tools)
	}
	// The text block held content block index 0, so the call must land on the
	// dense OpenAI index 0 rather than on its content block index.
	tool, ok := tools[0]
	if !ok {
		t.Fatalf("no tool at the dense index 0: %+v", tools)
	}
	if tool.ID != "toolu_1" || tool.Name != "get_time" || tool.Arguments != `{"tz":"UTC"}` {
		t.Fatalf("tool = %+v, want toolu_1/get_time with the joined arguments", tool)
	}
	if outcome.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls (stop_reason tool_use)", outcome.FinishReason)
	}
	if outcome.Status != relaymodel.UsageStatusSuccess {
		t.Fatalf("status = %s, want success", outcome.Status)
	}
	// message_delta reports the cumulative output count; 1+9 would be the
	// double-count this accumulator exists to prevent.
	if outcome.Usage == nil {
		t.Fatal("usage is nil, want a reported fact")
	}
	if outcome.Usage.PromptTokens != 30 || outcome.Usage.CompletionTokens != 9 || outcome.Usage.TotalTokens != 39 {
		t.Fatalf("usage = %+v, want 30/9/39", outcome.Usage)
	}
	if outcome.Usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("source = %s, want reported", outcome.Usage.Source)
	}
	if outcome.Usage.CacheReadInputTokens == nil || *outcome.Usage.CacheReadInputTokens != 7 {
		t.Fatalf("cache read = %v, want 7", outcome.Usage.CacheReadInputTokens)
	}
	// Never reported, so never a fabricated zero.
	if outcome.Usage.CacheWriteInputTokens != nil {
		t.Fatalf("cache write = %v, want nil", outcome.Usage.CacheWriteInputTokens)
	}
}

// An Anthropic stream that never reports the output side still yields a fact:
// the completion is counted from the stream and the prompt from the gateway's
// own estimate of the normalized request, and the whole row is labelled
// estimated rather than claiming the numbers came from the upstream.
func TestAnthropicChatStreamWithoutUsageIsEstimated(t *testing.T) {
	approximateTokenCounting(t)
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_up","content":[],"usage":{"input_tokens":0,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello there"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chunks, outcome := drainAnthropicStream(t, &meta.Meta{ActualModelName: "claude-3-7-sonnet", PromptTokens: 12}, body)
	if len(chunks) == 0 {
		t.Fatal("no chunks rendered")
	}
	if outcome.Usage == nil {
		t.Fatal("usage is nil, want an estimated fact")
	}
	if outcome.Usage.Source != relaymodel.UsageSourceEstimated {
		t.Fatalf("source = %s, want estimated", outcome.Usage.Source)
	}
	// The upstream reported nothing for the prompt, so the gateway's own
	// estimate stands in — unlike the passthrough, which has none to offer.
	if outcome.Usage.PromptTokens != 12 {
		t.Fatalf("prompt = %d, want the 12 from the normalized request", outcome.Usage.PromptTokens)
	}
	if outcome.Usage.CompletionTokens == 0 {
		t.Fatalf("completion = 0, want a local count of the streamed text")
	}
}

// An upstream error event ends the stream as an error and is carried out for the
// renderer to surface, not swallowed.
func TestAnthropicChatStreamErrorEvent(t *testing.T) {
	body := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		``,
	}, "\n")

	_, outcome := drainAnthropicStream(t, &meta.Meta{ActualModelName: "claude-3-7-sonnet"}, body)
	if outcome.Status != relaymodel.UsageStatusError {
		t.Fatalf("status = %s, want error", outcome.Status)
	}
	if outcome.Error == nil || outcome.Error.Type != "overloaded_error" || outcome.Error.Message != "Overloaded" {
		t.Fatalf("error = %+v, want the upstream's own error", outcome.Error)
	}
	// Nothing was reported and nothing was streamed, so there is no usage fact to
	// invent.
	if outcome.Usage != nil {
		t.Fatalf("usage = %+v, want nil", outcome.Usage)
	}
}

// A stream that breaks mid-way is partial, and the input side the upstream did
// report is kept.
func TestAnthropicChatStreamBreakIsPartial(t *testing.T) {
	approximateTokenCounting(t)
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_up","content":[],"usage":{"input_tokens":20,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half a sen"}}`,
		``,
	}, "\n")

	stream := anthropicChatCodec{}.Stream(&meta.Meta{ActualModelName: "claude-3-7-sonnet", PromptTokens: 20}, responseWithBody(body))
	for {
		if _, err := stream.Next(); err != nil {
			break
		}
	}
	outcome := stream.Finalize(true)
	if outcome.Status != relaymodel.UsageStatusPartial {
		t.Fatalf("status = %s, want partial", outcome.Status)
	}
	if outcome.Usage == nil || outcome.Usage.PromptTokens != 20 {
		t.Fatalf("usage = %+v, want the reported prompt kept", outcome.Usage)
	}
}

// --- non-streaming ----------------------------------------------------------

func TestAnthropicChatNonStreamResponseReportsUsage(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream",
	  "content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_9","name":"get_time","input":{"tz":"UTC"}}],
	  "stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":4,"cache_creation_input_tokens":3}}`

	m := &meta.Meta{ActualModelName: "claude-3-7-sonnet"}
	textResponse, usage, perr := anthropicChatCodec{}.NonStreamResponse(m, responseWithBody(body))
	if perr != nil {
		t.Fatalf("NonStreamResponse: %+v", perr)
	}
	if textResponse.Id != "chatcmpl-msg_1" || textResponse.Model != "claude-3-7-sonnet" {
		t.Fatalf("response = %+v, want the adaptor's conversion with the mapped model", textResponse)
	}
	if len(textResponse.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(textResponse.Choices))
	}
	choice := textResponse.Choices[0]
	if choice.FinishReason != "tool_calls" || len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("choice = %+v, want a tool_calls turn with one call", choice)
	}
	if usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("source = %s, want reported", usage.Source)
	}
	if usage.PromptTokens != 11 || usage.CompletionTokens != 4 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want 11/4/15", usage)
	}
	if usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens != 3 {
		t.Fatalf("cache write = %v, want the reported 3", usage.CacheWriteInputTokens)
	}
	// The renderers read the usage off the same value the recorder persists, so
	// the two can never disagree on the wire.
	if textResponse.Usage.PromptTokens != usage.PromptTokens || textResponse.Usage.CompletionTokens != usage.CompletionTokens {
		t.Fatalf("envelope usage %+v != recorded usage %+v", textResponse.Usage, usage)
	}
}

// An empty usage block is silence, not a report of zero: the completion is
// counted locally and the row is labelled estimated.
func TestAnthropicChatNonStreamSilentUpstreamIsEstimated(t *testing.T) {
	approximateTokenCounting(t)
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream",
	  "content":[{"type":"text","text":"hello there"}],"stop_reason":"end_turn","usage":{}}`

	m := &meta.Meta{ActualModelName: "claude-3-7-sonnet", PromptTokens: 5}
	_, usage, perr := anthropicChatCodec{}.NonStreamResponse(m, responseWithBody(body))
	if perr != nil {
		t.Fatalf("NonStreamResponse: %+v", perr)
	}
	if usage.Source != relaymodel.UsageSourceEstimated {
		t.Fatalf("source = %s, want estimated", usage.Source)
	}
	if usage.PromptTokens != 5 || usage.CompletionTokens == 0 || usage.TotalTokens != 5+usage.CompletionTokens {
		t.Fatalf("usage = %+v, want the prompt estimate plus a local count", usage)
	}
	if usage.CacheReadInputTokens != nil || usage.CacheWriteInputTokens != nil {
		t.Fatalf("cache fields = %v/%v, want nil: nothing was reported", usage.CacheReadInputTokens, usage.CacheWriteInputTokens)
	}
}

// A reported zero is a statement and survives as reported: only a body that said
// nothing at all is estimated.
func TestAnthropicChatNonStreamReportedZeroStaysReported(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],
	  "stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}}`

	m := &meta.Meta{ActualModelName: "claude-3-7-sonnet", PromptTokens: 9}
	_, usage, perr := anthropicChatCodec{}.NonStreamResponse(m, responseWithBody(body))
	if perr != nil {
		t.Fatalf("NonStreamResponse: %+v", perr)
	}
	if usage.Source != relaymodel.UsageSourceReported {
		t.Fatalf("source = %s, want reported", usage.Source)
	}
	if usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 0 {
		t.Fatalf("cache read = %v, want a reported zero", usage.CacheReadInputTokens)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ra, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var va, vb any
	if err := json.Unmarshal(ra, &va); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := json.Unmarshal(rb, &vb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sa, _ := json.Marshal(va)
	sb, _ := json.Marshal(vb)
	return string(sa) == string(sb)
}
