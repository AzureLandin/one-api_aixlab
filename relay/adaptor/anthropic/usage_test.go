package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// newTestContext builds a gin test context for the Handler under test.
func newTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return c
}

// handlerUsage runs the real Handler and returns the usage it built.
func handlerUsage(t *testing.T, body string) *relaymodel.Usage {
	t.Helper()
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
	errWithStatus, usage := Handler(newTestContext(), resp, 0, "claude-3-5-sonnet")
	if errWithStatus != nil {
		t.Fatalf("Handler error: %+v", errWithStatus)
	}
	if usage == nil {
		t.Fatalf("Handler returned nil usage")
	}
	return usage
}

func TestHandler_CacheFieldsMappedAndReported(t *testing.T) {
	body := `{
		"id":"msg_1","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hi"}],
		"model":"claude-3-5-sonnet","stop_reason":"end_turn",
		"usage":{"input_tokens":100,"output_tokens":10,
			"cache_read_input_tokens":40,"cache_creation_input_tokens":25}
	}`
	u := handlerUsage(t, body)

	if u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("Source = %s, want reported", u.Source)
	}
	// §8.1 cache_read -> CacheReadInputTokens
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("cache_read mapping wrong: %v", u.CacheReadInputTokens)
	}
	// §8.1 cache_creation -> CacheWriteInputTokens
	if u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 25 {
		t.Fatalf("cache_write mapping wrong: %v", u.CacheWriteInputTokens)
	}
	relaymodel.NormalizeUsage(u)
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
}

func TestHandler_AbsentCacheNeverFabricated(t *testing.T) {
	body := `{
		"id":"msg_2","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hi"}],
		"model":"claude","stop_reason":"end_turn",
		"usage":{"input_tokens":100,"output_tokens":10}
	}`
	u := handlerUsage(t, body)

	if u.CacheReadInputTokens != nil || u.CacheWriteInputTokens != nil {
		t.Fatalf("absent cache must stay nil, got read=%v write=%v",
			u.CacheReadInputTokens, u.CacheWriteInputTokens)
	}
	relaymodel.NormalizeUsage(u)
	// Upstream explicitly silent about cache -> not_reported, NOT a zero hit.
	if u.CacheStatus != relaymodel.CacheUsageNotReported {
		t.Fatalf("CacheStatus = %s, want not_reported", u.CacheStatus)
	}
}

func TestHandler_ReportedZeroCacheIsReported(t *testing.T) {
	body := `{
		"id":"msg_3","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hi"}],
		"model":"claude","stop_reason":"end_turn",
		"usage":{"input_tokens":100,"output_tokens":10,
			"cache_read_input_tokens":0,"cache_creation_input_tokens":0}
	}`
	u := handlerUsage(t, body)
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 0 {
		t.Fatalf("reported zero must be ptr-to-0, got %v", u.CacheReadInputTokens)
	}
	relaymodel.NormalizeUsage(u)
	// reported zero is still "reported" (upstream spoke), distinguishable from
	// the not_reported case above.
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
}

// accumulateStream folds a sequence of raw Anthropic SSE payloads through the
// shared accumulator, exactly as StreamHandler and the passthrough do.
func accumulateStream(t *testing.T, payloads ...string) relaymodel.Usage {
	t.Helper()
	var acc relaymodel.Usage
	for _, payload := range payloads {
		var ev StreamResponse
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("bad fixture %s: %v", payload, err)
		}
		if !AccumulateStreamUsage(&acc, &ev) {
			t.Fatalf("fixture %s carried no usage, want usage", payload)
		}
	}
	return acc
}

// The terminal delta carries the cumulative output count for the whole message,
// so adding the events together would count the completion more than once —
// message_start announces a running count of 1, which is where the classic
// off-by-one over-count of an Anthropic stream comes from.
func TestAccumulateStreamUsage_OutputIsAssignedNotAdded(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":1}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_delta","usage":{"output_tokens":25}}`,
	)
	if u.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", u.PromptTokens)
	}
	if u.CompletionTokens != 25 {
		t.Fatalf("CompletionTokens = %d, want 25 (cumulative, not summed)", u.CompletionTokens)
	}
	if u.Source != relaymodel.UsageSourceReported {
		t.Fatalf("Source = %s, want reported", u.Source)
	}
}

// The input side is announced once, by message_start. A later delta that does
// not repeat it must not zero it.
func TestAccumulateStreamUsage_LaterDeltaDoesNotClobberInput(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`,
		`{"type":"message_delta","usage":{"output_tokens":25}}`,
	)
	if u.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", u.PromptTokens)
	}
}

// Cache fields are adopted from whichever event reports them, latest non-nil
// wins: they must never be summed (a cache read is not a quantity that grows),
// and a delta that omits one must not erase the value message_start gave.
func TestAccumulateStreamUsage_CacheAdoptedNotSummed(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,
			"cache_read_input_tokens":40,"cache_creation_input_tokens":25}}}`,
		`{"type":"message_delta","usage":{"output_tokens":10,"cache_read_input_tokens":40}}`,
	)
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("cache read = %v, want 40 (adopted, not summed)", u.CacheReadInputTokens)
	}
	if u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 25 {
		t.Fatalf("cache write = %v, want 25 kept from message_start", u.CacheWriteInputTokens)
	}
}

func TestAccumulateStreamUsage_AbsentCacheStaysNil(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`,
		`{"type":"message_delta","usage":{"output_tokens":10}}`,
	)
	if u.CacheReadInputTokens != nil || u.CacheWriteInputTokens != nil {
		t.Fatalf("absent cache must stay nil, got read=%v write=%v",
			u.CacheReadInputTokens, u.CacheWriteInputTokens)
	}
	relaymodel.NormalizeUsage(&u)
	if u.CacheStatus != relaymodel.CacheUsageNotReported {
		t.Fatalf("CacheStatus = %s, want not_reported", u.CacheStatus)
	}
}

func TestAccumulateStreamUsage_ReportedZeroCacheStaysReported(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,
			"cache_read_input_tokens":0}}}`,
	)
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 0 {
		t.Fatalf("reported zero must stay ptr-to-0, got %v", u.CacheReadInputTokens)
	}
	relaymodel.NormalizeUsage(&u)
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
}

func TestAccumulateStreamUsage_IgnoresEventsWithoutUsage(t *testing.T) {
	acc := relaymodel.Usage{PromptTokens: 5, CompletionTokens: 6}
	var ev StreamResponse
	if err := json.Unmarshal([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`), &ev); err != nil {
		t.Fatal(err)
	}
	if AccumulateStreamUsage(&acc, &ev) {
		t.Fatal("event without usage reported true")
	}
	if acc.PromptTokens != 5 || acc.CompletionTokens != 6 {
		t.Fatalf("accumulator mutated by a usage-less event: %+v", acc)
	}
}

// Anthropic's input_tokens is the residual, so the normalized total input has
// to add both cache fields. The same conversion makes the normalized chat view
// (prompt_tokens_details) available without a second source of truth.
func TestRelayUsage_TotalInputIncludesCache(t *testing.T) {
	read, write := 40, 10
	u := RelayUsage(Usage{
		InputTokens: 50, OutputTokens: 7,
		CacheReadInputTokens: &read, CacheCreationInputTokens: &write,
	})
	if u.PromptTokens != 100 || u.CompletionTokens != 7 || u.TotalTokens != 107 {
		t.Fatalf("usage = %+v, want 100/7/107", u)
	}
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported", u.CacheStatus)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens == nil ||
		*u.PromptTokensDetails.CachedTokens != 40 {
		t.Fatalf("chat details not derived: %+v", u.PromptTokensDetails)
	}
}

// The regression this change exists for: a real Anthropic response can report
// cache_read > input_tokens, because input_tokens is only the uncached
// remainder. Once totalized, it is a consistent, reported fact — not invalid.
func TestRelayUsage_CacheReadGreaterThanResidualIsReported(t *testing.T) {
	read := 400
	u := RelayUsage(Usage{InputTokens: 5, OutputTokens: 2, CacheReadInputTokens: &read})
	if u.PromptTokens != 405 {
		t.Fatalf("PromptTokens = %d, want 405 (5 residual + 400 read)", u.PromptTokens)
	}
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("CacheStatus = %s, want reported, not invalid", u.CacheStatus)
	}
	if *u.CacheReadInputTokens != 400 {
		t.Fatalf("cache read = %d, want the reported 400", *u.CacheReadInputTokens)
	}
	relaymodel.NormalizeUsage(&u)
	if u.CacheStatus != relaymodel.CacheUsageReported {
		t.Fatalf("re-normalizing must stay reported, got %s", u.CacheStatus)
	}
}

// A stream can repeat the residual input in message_delta. It is totalized
// against the cache values adopted from earlier events, never treated as a
// total on its own.
func TestAccumulateStreamUsage_TotalizesRepeatedResidualInput(t *testing.T) {
	u := accumulateStream(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,
			"cache_read_input_tokens":40,"cache_creation_input_tokens":5}}}`,
		`{"type":"message_delta","usage":{"input_tokens":10,"output_tokens":7}}`,
	)
	if u.PromptTokens != 55 || u.CompletionTokens != 7 || u.TotalTokens != 62 {
		t.Fatalf("usage = %+v, want 55/7/62", u)
	}
}
