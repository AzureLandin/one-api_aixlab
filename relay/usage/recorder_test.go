package usage

import (
	"context"
	"testing"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/model/migration"
	relaymodel "github.com/songquanpeng/one-api/relay/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupRecorderDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	common.UsingSQLite = true
	if err := db.AutoMigrate(&model.Option{}); err != nil {
		t.Fatal(err)
	}
	if err := migration.Migrate(db, "test"); err != nil {
		t.Fatal(err)
	}
}

func firstRecord(t *testing.T, reqID string) *model.UsageRecord {
	t.Helper()
	var rec model.UsageRecord
	if err := model.DB.Where("request_id = ?", reqID).First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	return &rec
}

func TestRecorderMarksUnsetSourceAsEstimated(t *testing.T) {
	setupRecorderDB(t)
	ctx := context.Background()
	err := Default().RecordOnce(ctx, RecordInput{
		RequestID: "r1",
		UserID:    1,
		TokenID:   2,
		ChannelID: 3,
		ModelName: "m",
		Status:    relaymodel.UsageStatusSuccess,
		Usage:     relaymodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := firstRecord(t, "r1")
	if rec.UsageSource != model.UsageSourceEstimated {
		t.Fatalf("unset source must be honestly labeled estimated, got %q", rec.UsageSource)
	}
	if rec.CacheUsageStatus != model.CacheUsageNotReported {
		t.Fatalf("missing cache fields must be not_reported, got %q", rec.CacheUsageStatus)
	}
	if rec.TokenID == nil || *rec.TokenID != 2 {
		t.Fatal("token id must be stored")
	}
	if rec.RequestCount != 1 || rec.Status != model.UsageStatusSuccess {
		t.Fatal("success row bookkeeping wrong")
	}
}

func TestRecorderErrorStatusZeroRequestCount(t *testing.T) {
	setupRecorderDB(t)
	ctx := context.Background()
	if err := Default().RecordOnce(ctx, RecordInput{
		RequestID: "r2", UserID: 1, ChannelID: 3, ModelName: "m",
		Status: relaymodel.UsageStatusError,
	}); err != nil {
		t.Fatal(err)
	}
	rec := firstRecord(t, "r2")
	if rec.RequestCount != 0 || rec.Status != model.UsageStatusError {
		t.Fatalf("error row: rc=%d status=%q", rec.RequestCount, rec.Status)
	}
}

func TestRecorderIdempotentAndFallbackRequestID(t *testing.T) {
	setupRecorderDB(t)
	ctx := context.Background()
	in := RecordInput{RequestID: "r3", UserID: 1, ModelName: "m",
		Status: relaymodel.UsageStatusSuccess}
	if err := Default().RecordOnce(ctx, in); err != nil {
		t.Fatal(err)
	}
	// duplicate must be a silent no-op
	if err := Default().RecordOnce(ctx, in); err != nil {
		t.Fatalf("duplicate record must not error: %v", err)
	}
	var count int64
	model.DB.Model(&model.UsageRecord{}).Where("request_id = ?", "r3").Count(&count)
	if count != 1 {
		t.Fatalf("want single row, got %d", count)
	}
	// missing request id gets a generated fallback so the fact is still kept
	if err := Default().RecordOnce(ctx, RecordInput{UserID: 1, ModelName: "m"}); err != nil {
		t.Fatal(err)
	}
	var anyGen int64
	model.DB.Model(&model.UsageRecord{}).Where("request_id LIKE ?", "gen-%").Count(&anyGen)
	if anyGen != 1 {
		t.Fatalf("fallback request id row missing (got %d)", anyGen)
	}
}
