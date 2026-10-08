package anthropic

import (
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
