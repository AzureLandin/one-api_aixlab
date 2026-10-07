package model

// Usage is the normalized usage fact used across all client protocols and
// upstream adaptors (docs/one-api-modernization-implementation.md §4.1).
//
// Pointer semantics are load-bearing: an upstream that did not report a
// field must leave it nil; a zero reported by the upstream must be stored as
// a pointer-to-zero. Never replace nil with 0.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	InputTokensDetails      *InputTokensDetails      `json:"input_tokens_details,omitempty"`

	// Normalized cache fields, filled by adaptor-side normalization.
	// nil = not reported by upstream; non-nil = reported (possibly 0).
	CacheReadInputTokens  *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`

	// Source is set only by the code path that knows the truth:
	// "reported" from upstream bodies, "estimated" from local counting.
	Source string `json:"usage_source,omitempty"`
	// CacheStatus is derived by NormalizeUsage: reported/not_reported/invalid.
	CacheStatus string `json:"cache_usage_status,omitempty"`
	// Abnormal marks internal inconsistencies (e.g. total != prompt+completion).
	// Raw values are preserved; the frontend must surface the flag.
	Abnormal bool `json:"-"`
}

type PromptTokensDetails struct {
	// Pointer keeps "absent" distinct from "cached_tokens: 0".
	CachedTokens *int `json:"cached_tokens,omitempty"`
}

type InputTokensDetails struct {
	CachedTokens *int `json:"cached_tokens,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}

// Cache status values, kept in sync with model.UsageRecord constants.
const (
	CacheUsageReported    = "reported"
	CacheUsageNotReported = "not_reported"
	CacheUsageInvalid     = "invalid"
)

// Usage source values.
const (
	UsageSourceReported  = "reported"
	UsageSourceEstimated = "estimated"
	UsageSourceLegacy    = "legacy"
)

// Record status values (kept in sync with model.UsageRecord constants).
const (
	UsageStatusSuccess = "success"
	UsageStatusError   = "error"
	UsageStatusPartial = "partial"
)

// NormalizeUsage derives CacheStatus and Abnormal from the raw fields without
// rewriting any reported value. Rules (implementation doc §4.1, §8.2):
//   - no cache fields at all                -> not_reported
//   - cache fields present and consistent   -> reported
//   - negative, or read+write > prompt      -> invalid (values preserved)
//   - total reported but != prompt+completion -> Abnormal (values preserved)
func NormalizeUsage(u *Usage) {
	if u == nil {
		return
	}
	if u.CacheReadInputTokens == nil && u.CacheWriteInputTokens == nil {
		// Adaptors may carry cache info only in protocol details.
		cached := 0
		found := false
		if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
			cached, found = *u.PromptTokensDetails.CachedTokens, true
		}
		if !found && u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens != nil {
			cached, found = *u.InputTokensDetails.CachedTokens, true
		}
		if found {
			v := int64(cached)
			u.CacheReadInputTokens = &v
		} else {
			u.CacheStatus = CacheUsageNotReported
			normalizeTotalCheck(u)
			return
		}
	}
	valid := true
	if u.CacheReadInputTokens != nil && *u.CacheReadInputTokens < 0 {
		valid = false
	}
	if u.CacheWriteInputTokens != nil && *u.CacheWriteInputTokens < 0 {
		valid = false
	}
	var readWrite int64
	if u.CacheReadInputTokens != nil {
		readWrite += *u.CacheReadInputTokens
	}
	if u.CacheWriteInputTokens != nil {
		readWrite += *u.CacheWriteInputTokens
	}
	if readWrite > int64(u.PromptTokens) {
		valid = false
	}
	if valid {
		u.CacheStatus = CacheUsageReported
	} else {
		u.CacheStatus = CacheUsageInvalid
	}
	normalizeTotalCheck(u)
}

func normalizeTotalCheck(u *Usage) {
	if u.TotalTokens != 0 && u.TotalTokens != u.PromptTokens+u.CompletionTokens {
		u.Abnormal = true
	}
}

type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
}

type ErrorWithStatusCode struct {
	Error
	StatusCode int `json:"status_code"`
}
