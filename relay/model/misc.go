package model

// Usage is the normalized usage fact used across all client protocols and
// upstream adaptors (docs/one-api-modernization-implementation.md §4.1).
//
// Pointer semantics are load-bearing: an upstream that did not report a
// field must leave it nil; a zero reported by the upstream must be stored as
// a pointer-to-zero. Never replace nil with 0.
//
// PromptTokens is TOTAL input: it includes the tokens a cache read or write
// covered. OpenAI/Responses/Gemini already report prompt_tokens that way;
// Anthropic's input_tokens is the *uncached remainder*, so the Anthropic
// adaptor adds cache_read + cache_creation before normalizing (see
// anthropic.RelayUsage). One meaning is what makes Σread/Σinput comparable
// across upstream families and what makes the cache-subset check in
// NormalizeUsage correct for every family.
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
	// Adaptors may carry cache info only in protocol details. Fold the read
	// and write sides independently: an upstream can report one without the
	// other, and a reported write must not hide a read that lived in details.
	if u.CacheReadInputTokens == nil {
		if cached := firstCachedTokens(u.PromptTokensDetails, u.InputTokensDetails); cached != nil {
			v := int64(*cached)
			u.CacheReadInputTokens = &v
		}
	}
	if u.CacheReadInputTokens == nil && u.CacheWriteInputTokens == nil {
		u.CacheStatus = CacheUsageNotReported
		normalizeTotalCheck(u)
		return
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
	// Valid because PromptTokens is total input (see the type comment): after
	// adaptor normalization, cache read + write is a subset of it for every
	// upstream family. Raw Anthropic usage (where cache can exceed the
	// *residual* input_tokens) must be totalized by the adaptor before it
	// reaches this check, which is what anthropic.RelayUsage does.
	if readWrite > int64(u.PromptTokens) {
		valid = false
	}
	if valid {
		u.CacheStatus = CacheUsageReported
	} else {
		u.CacheStatus = CacheUsageInvalid
	}
	// A chat client reads the cache hit from prompt_tokens_details, not from
	// the normalized field. Derive that view from the single normalized value
	// (never the other way around), so Anthropic- and Responses-origin usage is
	// visible in a chat envelope too. A reported zero stays a zero; an absent
	// field stays absent.
	if u.CacheReadInputTokens != nil {
		cached := int(*u.CacheReadInputTokens)
		if u.PromptTokensDetails == nil {
			u.PromptTokensDetails = &PromptTokensDetails{}
		}
		u.PromptTokensDetails.CachedTokens = &cached
	}
	normalizeTotalCheck(u)
}

// firstCachedTokens returns the first reported cached-token count from the two
// protocol detail shapes, or nil when neither reported one.
func firstCachedTokens(prompt *PromptTokensDetails, input *InputTokensDetails) *int {
	if prompt != nil && prompt.CachedTokens != nil {
		return prompt.CachedTokens
	}
	if input != nil && input.CachedTokens != nil {
		return input.CachedTokens
	}
	return nil
}

// PromptResidual returns the uncached part of the prompt: the value Anthropic
// spells as input_tokens, whose input_tokens excludes cache tokens. Normalized
// PromptTokens is total input, so a renderer subtracts the reported cache.
// Inconsistent data (cache larger than the prompt) is clamped to 0 rather than
// sent to a client as a negative token count.
func (u Usage) PromptResidual() int {
	residual := u.PromptTokens
	if u.CacheReadInputTokens != nil {
		residual -= int(*u.CacheReadInputTokens)
	}
	if u.CacheWriteInputTokens != nil {
		residual -= int(*u.CacheWriteInputTokens)
	}
	if residual < 0 {
		return 0
	}
	return residual
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
