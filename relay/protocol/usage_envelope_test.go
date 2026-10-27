package protocol_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	anthropic "github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	openai_responses "github.com/songquanpeng/one-api/relay/adaptor/openai_responses"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/sse"
)

// upstreamCase is one upstream family's raw usage, after the adaptor has
// normalized it. totalInput is the real input the upstream processed; cacheRead
// and cacheWrite are nil when the upstream did not report them.
type upstreamCase struct {
	name       string
	usage      relaymodel.Usage
	totalInput int
	cacheRead  *int
	cacheWrite *int
}

func ptr(v int) *int { return &v }

// upstreamCases builds the three upstream sources from their wire bodies, so the
// matrix exercises each adaptor's own ingestion rather than a hand-built fact.
func upstreamCases(t *testing.T) []upstreamCase {
	t.Helper()

	read := 40
	write := 10
	return []upstreamCase{
		{
			// OpenAI: prompt_tokens includes cached_tokens.
			name: "openai",
			usage: normalizeOpenAI(t, `{"usage":{"prompt_tokens":100,"completion_tokens":10,
				"total_tokens":110,"prompt_tokens_details":{"cached_tokens":40}}}`),
			totalInput: 100,
			cacheRead:  ptr(40),
		},
		{
			// Responses: input_tokens includes cached_tokens.
			name: "responses",
			usage: normalizeResponses(t, `{"usage":{"input_tokens":100,"output_tokens":10,
				"total_tokens":110,"input_tokens_details":{"cached_tokens":40}}}`),
			totalInput: 100,
			cacheRead:  ptr(40),
		},
		{
			// Anthropic: input_tokens is the residual; total input is
			// 50 + read 40 + write 10 = 100.
			name: "anthropic",
			usage: normalizeAnthropic(t, `{"usage":{"input_tokens":50,"output_tokens":10,
				"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}`),
			totalInput: 100,
			cacheRead:  &read,
			cacheWrite: &write,
		},
		{
			// No cache reported at all: the nil-semantics control.
			name:       "silent",
			usage:      normalizeOpenAI(t, `{"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`),
			totalInput: 100,
		},
	}
}

func normalizeOpenAI(t *testing.T, body string) relaymodel.Usage {
	t.Helper()
	var tr openai.TextResponse
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatalf("openai fixture: %v", err)
	}
	u := tr.Usage
	u.Source = relaymodel.UsageSourceReported
	relaymodel.NormalizeUsage(&u)
	return u
}

func normalizeResponses(t *testing.T, body string) relaymodel.Usage {
	t.Helper()
	var resp openai_responses.Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("responses fixture: %v", err)
	}
	u, ok := openai_responses.ToUsage(resp.Usage)
	if !ok {
		t.Fatal("responses fixture reported no usage")
	}
	return *u
}

func normalizeAnthropic(t *testing.T, body string) relaymodel.Usage {
	t.Helper()
	var resp anthropic.Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("anthropic fixture: %v", err)
	}
	return anthropic.RelayUsage(resp.Usage)
}

// TestUsageEnvelopeCrossProtocolMatrix is the client-facing proof: for each
// upstream family, all three client envelopes must expose the cache and obey
// their own additivity rule.
//
//   - Anthropic input_tokens is the uncached residual, so
//     input_tokens + cache_read + cache_creation == real total input.
//   - OpenAI/Responses input_tokens (prompt_tokens) already includes the cache,
//     so input_tokens == real total input and cached_tokens is a subset.
func TestUsageEnvelopeCrossProtocolMatrix(t *testing.T) {
	for _, c := range upstreamCases(t) {
		c := c
		t.Run(c.name, func(t *testing.T) {
			assertAnthropicEnvelope(t, c)
			assertAnthropicStream(t, c)
			assertResponsesEnvelope(t, c)
			assertResponsesStream(t, c)
			assertChatEnvelope(t, c)
		})
	}
}

func assertAnthropicEnvelope(t *testing.T, c upstreamCase) {
	t.Helper()
	env := protocol.RenderMessages(&openai.TextResponse{Usage: c.usage}, "m")
	usage := env["usage"].(map[string]any)
	assertAnthropicUsage(t, usage, c)
}

func assertAnthropicStream(t *testing.T, c upstreamCase) {
	t.Helper()
	var buf bytes.Buffer
	w := sse.NewWriter(&buf)
	stream := protocol.NewMessagesStream(w, "msg_1", "m", &relaymodel.Usage{PromptTokens: c.totalInput})
	if err := stream.Finish("stop", &c.usage); err != nil {
		t.Fatalf("finish: %v", err)
	}
	events := parseSSE(t, buf.String())
	delta, ok := events["message_delta"]
	if !ok {
		t.Fatalf("no message_delta event in:\n%s", buf.String())
	}
	usage, ok := delta["usage"].(map[string]any)
	if !ok {
		t.Fatalf("message_delta has no usage: %+v", delta)
	}
	// The terminal delta is where the authoritative usage arrives; it must be
	// additive on the same terms as the non-streaming body.
	assertAnthropicUsage(t, usage, c)
}

func assertAnthropicUsage(t *testing.T, usage map[string]any, c upstreamCase) {
	t.Helper()
	residual := intValue(t, usage, "input_tokens")
	sum := residual
	if v, ok := optionalInt(usage, "cache_read_input_tokens"); ok {
		sum += v
	}
	if v, ok := optionalInt(usage, "cache_creation_input_tokens"); ok {
		sum += v
	}
	if sum != c.totalInput {
		t.Fatalf("input + cache read + cache write = %d, want the real total input %d (%+v)",
			sum, c.totalInput, usage)
	}
	assertOptionalInt(t, usage, "cache_read_input_tokens", c.cacheRead)
	assertOptionalInt(t, usage, "cache_creation_input_tokens", c.cacheWrite)
	if got := intValue(t, usage, "output_tokens"); got != c.usage.CompletionTokens {
		t.Fatalf("output_tokens = %d, want %d", got, c.usage.CompletionTokens)
	}
}

func assertResponsesEnvelope(t *testing.T, c upstreamCase) {
	t.Helper()
	env := protocol.RenderResponses(&openai.TextResponse{Usage: c.usage}, "m")
	usage := env["usage"].(map[string]any)
	assertResponsesUsage(t, usage, c)
}

func assertResponsesStream(t *testing.T, c upstreamCase) {
	t.Helper()
	var buf bytes.Buffer
	w := sse.NewWriter(&buf)
	stream := protocol.NewResponsesStream(w, "resp_1", "m", nil)
	if err := stream.Finish("stop", &c.usage); err != nil {
		t.Fatalf("finish: %v", err)
	}
	events := parseSSE(t, buf.String())
	completed, ok := events["response.completed"]
	if !ok {
		t.Fatalf("no response.completed event in:\n%s", buf.String())
	}
	resp, ok := completed["response"].(map[string]any)
	if !ok {
		t.Fatalf("response.completed has no response object: %+v", completed)
	}
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("terminal response has no usage: %+v", resp)
	}
	assertResponsesUsage(t, usage, c)
}

func assertResponsesUsage(t *testing.T, usage map[string]any, c upstreamCase) {
	t.Helper()
	// OpenAI/Responses semantics: the input count already includes the cache.
	if got := intValue(t, usage, "input_tokens"); got != c.totalInput {
		t.Fatalf("input_tokens = %d, want total input %d", got, c.totalInput)
	}
	details, ok := usage["input_tokens_details"].(map[string]any)
	if c.cacheRead == nil {
		if ok {
			t.Fatalf("absent cache must omit input_tokens_details, got %+v", details)
		}
		return
	}
	if !ok {
		t.Fatalf("reported cache missing from input_tokens_details: %+v", usage)
	}
	if got := intValue(t, details, "cached_tokens"); got != *c.cacheRead {
		t.Fatalf("cached_tokens = %d, want %d", got, *c.cacheRead)
	}
}

// assertChatEnvelope marshals the same normalized usage the way the chat path
// writes it. The standard OpenAI field is prompt_tokens_details.cached_tokens;
// it must be derived from the normalized cache read, not left empty.
func assertChatEnvelope(t *testing.T, c upstreamCase) {
	t.Helper()
	body, err := json.Marshal(openai.TextResponse{Usage: c.usage})
	if err != nil {
		t.Fatalf("marshal chat envelope: %v", err)
	}
	var parsed struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal chat envelope: %v", err)
	}
	usage := parsed.Usage
	if got := intValue(t, usage, "prompt_tokens"); got != c.totalInput {
		t.Fatalf("prompt_tokens = %d, want total input %d", got, c.totalInput)
	}
	details, ok := usage["prompt_tokens_details"].(map[string]any)
	if c.cacheRead == nil {
		if ok {
			t.Fatalf("absent cache must omit prompt_tokens_details, got %+v", details)
		}
		return
	}
	if !ok {
		t.Fatalf("reported cache missing from prompt_tokens_details: %+v", usage)
	}
	if got := intValue(t, details, "cached_tokens"); got != *c.cacheRead {
		t.Fatalf("cached_tokens = %d, want %d", got, *c.cacheRead)
	}
}

func parseSSE(t *testing.T, raw string) map[string]map[string]any {
	t.Helper()
	events := map[string]map[string]any{}
	eventType := ""
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				continue
			}
			if eventType != "" {
				events[eventType] = payload
			}
		}
	}
	return events
}

func intValue(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := optionalInt(m, key)
	if !ok {
		t.Fatalf("%s missing from %+v", key, m)
	}
	return v
}

func optionalInt(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// assertOptionalInt enforces nil semantics: want == nil means the key must be
// absent, and want != nil means it must be present with exactly that value
// (including a reported zero).
func assertOptionalInt(t *testing.T, m map[string]any, key string, want *int) {
	t.Helper()
	got, ok := optionalInt(m, key)
	if want == nil {
		if ok {
			t.Fatalf("%s must be omitted when the upstream did not report it, got %d", key, got)
		}
		return
	}
	if !ok {
		t.Fatalf("%s missing; want reported %d", key, *want)
	}
	if got != *want {
		t.Fatalf("%s = %d, want %d", key, got, *want)
	}
}

// message_start can carry the cache fields whenever the caller already knows
// them (the same-protocol passthrough is byte-for-byte and does not use this
// renderer at all). The seed is normally cache-less, but the shape must not be
// what hides a reported hit.
func TestMessagesStream_MessageStartCarriesReportedCache(t *testing.T) {
	read, write := 40, 10
	read64, write64 := int64(read), int64(write)
	var buf bytes.Buffer
	w := sse.NewWriter(&buf)
	seed := &relaymodel.Usage{
		PromptTokens:          100,
		CacheReadInputTokens:  &read64,
		CacheWriteInputTokens: &write64,
	}
	_ = protocol.NewMessagesStream(w, "msg_1", "m", seed)

	events := parseSSE(t, buf.String())
	start, ok := events["message_start"]
	if !ok {
		t.Fatalf("no message_start in:\n%s", buf.String())
	}
	message := start["message"].(map[string]any)
	usage := message["usage"].(map[string]any)
	assertOptionalInt(t, usage, "cache_read_input_tokens", &read)
	assertOptionalInt(t, usage, "cache_creation_input_tokens", &write)
	if got := intValue(t, usage, "input_tokens"); got != 50 {
		t.Fatalf("input_tokens = %d, want the residual 50", got)
	}
	if got := intValue(t, usage, "output_tokens"); got != 0 {
		t.Fatalf("message_start output_tokens = %d, want 0", got)
	}
}
