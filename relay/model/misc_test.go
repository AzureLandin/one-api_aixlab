package model

import (
	"encoding/json"
	"testing"
)

func i64(v int64) *int64 { return &v }
func ip(v int) *int       { return &v }

func TestNormalizeUsageNotReported(t *testing.T) {
	u := &Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
	NormalizeUsage(u)
	if u.CacheStatus != CacheUsageNotReported {
		t.Fatalf("want not_reported, got %q", u.CacheStatus)
	}
	if u.CacheReadInputTokens != nil {
		t.Fatal("nil cache field must not be fabricated into zero")
	}
}

func TestNormalizeUsageReportedZeroStaysReported(t *testing.T) {
	// wire format: prompt_tokens_details.cached_tokens == 0 is a *reported*
	// zero, distinct from an absent field.
	var u Usage
	body := `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,` +
		`"prompt_tokens_details":{"cached_tokens":0}}`
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatal(err)
	}
	NormalizeUsage(&u)
	if u.CacheStatus != CacheUsageReported {
		t.Fatalf("reported zero must stay reported, got %q", u.CacheStatus)
	}
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 0 {
		t.Fatalf("want pointer-to-zero, got %v", u.CacheReadInputTokens)
	}
}

func TestNormalizeUsageInvalidNotTrimmed(t *testing.T) {
	// cache read larger than prompt tokens: values preserved, status invalid
	u := &Usage{
		PromptTokens:         10,
		CompletionTokens:     5,
		TotalTokens:          15,
		CacheReadInputTokens: i64(999),
	}
	NormalizeUsage(u)
	if u.CacheStatus != CacheUsageInvalid {
		t.Fatalf("want invalid, got %q", u.CacheStatus)
	}
	if *u.CacheReadInputTokens != 999 {
		t.Fatal("invalid values must not be silently rewritten")
	}

	neg := &Usage{PromptTokens: 10, CacheReadInputTokens: i64(-1), CacheWriteInputTokens: i64(2)}
	NormalizeUsage(neg)
	if neg.CacheStatus != CacheUsageInvalid {
		t.Fatalf("negative cache value must be invalid, got %q", neg.CacheStatus)
	}
}

func TestNormalizeUsageAnthropicBothFields(t *testing.T) {
	// anthropic: cache_read + cache_creation both reported, sum <= prompt
	u := &Usage{
		PromptTokens:          1000,
		CacheReadInputTokens:  i64(600),
		CacheWriteInputTokens: i64(300),
	}
	NormalizeUsage(u)
	if u.CacheStatus != CacheUsageReported {
		t.Fatalf("want reported, got %q", u.CacheStatus)
	}
	if u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 300 {
		t.Fatal("cache write must be preserved separately")
	}
}

func TestNormalizeUsageAbnormalTotalPreserved(t *testing.T) {
	u := &Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 999}
	NormalizeUsage(u)
	if !u.Abnormal {
		t.Fatal("inconsistent total must be flagged")
	}
	if u.TotalTokens != 999 {
		t.Fatal("reported raw value must be preserved, not rewritten")
	}
}

func TestNormalizeUsageInputDetailsFallback(t *testing.T) {
	// Responses API style: input_tokens_details.cached_tokens
	var u Usage
	body := `{"prompt_tokens":100,"input_tokens_details":{"cached_tokens":80}}`
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatal(err)
	}
	NormalizeUsage(&u)
	if u.CacheStatus != CacheUsageReported || *u.CacheReadInputTokens != 80 {
		t.Fatalf("input_tokens_details must map to cache read, got %+v", u)
	}
}
