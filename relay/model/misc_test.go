package model

import (
	"encoding/json"
	"testing"
)

func i64(v int64) *int64 { return &v }
func ip(v int) *int      { return &v }

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

// A cache write being reported must not hide a cache read that arrived in the
// protocol details: the two sides are folded independently.
func TestNormalizeUsageFoldsReadWhenWriteAlsoReported(t *testing.T) {
	u := &Usage{
		PromptTokens:          100,
		CacheWriteInputTokens: i64(10),
		PromptTokensDetails:   &PromptTokensDetails{CachedTokens: ip(40)},
	}
	NormalizeUsage(u)
	if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 40 {
		t.Fatalf("cache read from details not folded: %v", u.CacheReadInputTokens)
	}
	if u.CacheStatus != CacheUsageReported {
		t.Fatalf("want reported, got %q", u.CacheStatus)
	}
}

// A chat client reads the hit from prompt_tokens_details. The normalizer
// derives that view from the single normalized value, preserving a reported
// zero and never inventing one.
func TestNormalizeUsageDerivesChatDetailsFromNormalizedCache(t *testing.T) {
	u := &Usage{PromptTokens: 100, CacheReadInputTokens: i64(40)}
	NormalizeUsage(u)
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens == nil ||
		*u.PromptTokensDetails.CachedTokens != 40 {
		t.Fatalf("chat details not derived: %+v", u.PromptTokensDetails)
	}

	zero := &Usage{PromptTokens: 100, CacheReadInputTokens: i64(0)}
	NormalizeUsage(zero)
	if zero.PromptTokensDetails == nil || zero.PromptTokensDetails.CachedTokens == nil ||
		*zero.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("a reported zero must be derived as zero, got %+v", zero.PromptTokensDetails)
	}

	silent := &Usage{PromptTokens: 100}
	NormalizeUsage(silent)
	if silent.PromptTokensDetails != nil {
		t.Fatalf("absent cache must not fabricate details: %+v", silent.PromptTokensDetails)
	}
}

// After adaptor normalization PromptTokens is total input, so read+write is a
// subset and the cache is reported. This is the invariant that replaced the
// residual-shaped check that only held for OpenAI-style upstreams.
func TestNormalizeUsageTotalInputIsTheCacheSuperset(t *testing.T) {
	u := &Usage{
		PromptTokens:          100, // total input: read 40 + write 10 + residual 50
		CacheReadInputTokens:  i64(40),
		CacheWriteInputTokens: i64(10),
	}
	NormalizeUsage(u)
	if u.CacheStatus != CacheUsageReported {
		t.Fatalf("want reported, got %q", u.CacheStatus)
	}
}

func TestPromptResidual(t *testing.T) {
	u := Usage{
		PromptTokens:          100,
		CacheReadInputTokens:  i64(40),
		CacheWriteInputTokens: i64(10),
	}
	if got := u.PromptResidual(); got != 50 {
		t.Fatalf("residual = %d, want 50", got)
	}
	if got := (&Usage{PromptTokens: 25}).PromptResidual(); got != 25 {
		t.Fatalf("without cache residual = %d, want the whole prompt 25", got)
	}
	// Inconsistent data is clamped rather than rendered as a negative count.
	bad := Usage{PromptTokens: 10, CacheReadInputTokens: i64(40)}
	if got := bad.PromptResidual(); got != 0 {
		t.Fatalf("inconsistent residual = %d, want 0", got)
	}
}
