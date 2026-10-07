// Package usage provides the unified UsageRecorder that every generation
// path must call exactly once per request (implementation doc §4.3).
//
// The recorder deliberately knows nothing about quota, ratios or refunds;
// it only persists normalized usage facts keyed by request_id.
package usage

import (
	"context"
	"time"

	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/common/random"
	"github.com/songquanpeng/one-api/model"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// RecordInput is the recorder's normalized input.
type RecordInput struct {
	RequestID string
	UserID    int
	TokenID   int
	ChannelID int
	ModelName string
	Usage     relaymodel.Usage
	Stream    bool
	Status    string // model.UsageStatusSuccess / Error / Partial
	CreatedAt time.Time
}

// Recorder persists usage records. Implementations must be safe to call from
// goroutines after the HTTP response has been written.
type Recorder interface {
	// Record writes a usage row. It never overwrites an upstream error.
	Record(ctx context.Context, input RecordInput) error
	// RecordOnce is idempotent for the same RequestID (retry + stream finalize).
	RecordOnce(ctx context.Context, input RecordInput) error
}

type dbRecorder struct{}

var defaultRecorder Recorder = dbRecorder{}

// Default returns the process-wide recorder.
func Default() Recorder { return defaultRecorder }

func (dbRecorder) Record(ctx context.Context, input RecordInput) error {
	return model.InsertUsageRecordOnce(toRecord(ctx, input))
}

func (r dbRecorder) RecordOnce(ctx context.Context, input RecordInput) error {
	return r.Record(ctx, input)
}

// toRecord converts and normalizes the input; it never invents cache values
// and never converts an unreported cache into zero.
func toRecord(ctx context.Context, input RecordInput) *model.UsageRecord {
	u := input.Usage
	relaymodel.NormalizeUsage(&u)
	if u.Source == "" {
		// Conservative truth: anything not explicitly marked reported is local.
		u.Source = relaymodel.UsageSourceEstimated
	}
	if input.Status == "" {
		input.Status = model.UsageStatusSuccess
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = helper.GetRequestID(ctx)
	}
	if requestID == "" {
		requestID = "gen-" + random.GetUUID()
		logger.Warnf(ctx, "usage record without request id, generated fallback id %s", requestID)
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	record := &model.UsageRecord{
		RequestID:             requestID,
		UserID:                input.UserID,
		ChannelID:             input.ChannelID,
		ModelName:             input.ModelName,
		InputTokens:           int64(u.PromptTokens),
		OutputTokens:          int64(u.CompletionTokens),
		TotalTokens:           int64(u.TotalTokens),
		CacheReadInputTokens:  u.CacheReadInputTokens,
		CacheWriteInputTokens: u.CacheWriteInputTokens,
		RequestCount:          1,
		UsageSource:           u.Source,
		CacheUsageStatus:      u.CacheStatus,
		Stream:                input.Stream,
		Status:                input.Status,
		CreatedAt:             createdAt.Unix(),
	}
	if input.TokenID > 0 {
		tokenID := input.TokenID
		record.TokenID = &tokenID
	}
	// Failed generations are observable but must not inflate request counts.
	if input.Status == model.UsageStatusError {
		record.RequestCount = 0
	}
	if u.Abnormal {
		logger.Warnf(ctx, "usage record %s marked abnormal: total=%d prompt=%d completion=%d",
			requestID, u.TotalTokens, u.PromptTokens, u.CompletionTokens)
	}
	return record
}
