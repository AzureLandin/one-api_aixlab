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
