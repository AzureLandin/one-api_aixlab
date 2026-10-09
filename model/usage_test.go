package model

import (
	"testing"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/model/migration"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupUsageTestDB(t *testing.T) {
	t.Helper()
	// independent in-memory database per test
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	common.UsingSQLite = true
	if err := db.AutoMigrate(&Option{}, &User{}, &Channel{}); err != nil {
		t.Fatalf("legacy tables: %v", err)
	}
	if err := migration.Migrate(db, "test"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	setupUsageTestDB(t)
	// second run must be a no-op
	if err := migration.Migrate(DB, "test-again"); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if !DB.Migrator().HasTable("usage_records") {
		t.Fatal("usage_records missing after migration")
	}
	if !DB.Migrator().HasIndex("usage_records", "idx_usage_user_created") {
		t.Fatal("composite index missing after v002")
	}
	cutover := GetUsageCutover()
	if cutover == 0 {
		t.Fatal("usage_cutover_at option was not written")
	}
}

func ptrInt(v int) *int       { return &v }
func ptrInt64(v int64) *int64 { return &v }

func mkRecord(reqID string, day int64) *UsageRecord {
	return &UsageRecord{
		RequestID:        reqID,
		UserID:           1,
		TokenID:          ptrInt(7),
		ChannelID:        3,
		ModelName:        "gpt-test",
		InputTokens:      100,
		OutputTokens:     20,
		TotalTokens:      120,
		RequestCount:     1,
		UsageSource:      UsageSourceReported,
		CacheUsageStatus: CacheUsageNotReported,
		Status:           UsageStatusSuccess,
		CreatedAt:        day,
	}
}

func TestInsertUsageRecordOnceIdempotent(t *testing.T) {
	setupUsageTestDB(t)
	rec := mkRecord("req-1", 1_700_000_000)
	if err := InsertUsageRecordOnce(rec); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// retry / stream-finalize second call with same request_id must not duplicate
	if err := InsertUsageRecordOnce(mkRecord("req-1", 1_700_000_000)); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	var count int64
	DB.Model(&UsageRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("want 1 row for repeated request_id, got %d", count)
	}
	if err := InsertUsageRecordOnce(&UsageRecord{}); err == nil {
		t.Fatal("empty request_id must be rejected")
	}
}

// A retry records an error row first and a success row second for the same
// request_id. Without the upgrade the client sees a success while the table
// says error.
func TestInsertUsageRecordOnceUpgradesErrorToTerminal(t *testing.T) {
	setupUsageTestDB(t)
	failed := mkRecord("req-upgrade", 1_700_000_000)
	failed.Status = UsageStatusError
	failed.ChannelID = 3
	failed.InputTokens, failed.OutputTokens, failed.TotalTokens = 0, 0, 0
	failed.RequestCount = 0
	failed.UsageSource = UsageSourceEstimated
	if err := InsertUsageRecordOnce(failed); err != nil {
		t.Fatalf("error insert: %v", err)
	}

	// The retry succeeds on a different channel, a minute later.
	succeeded := mkRecord("req-upgrade", 1_700_000_060)
	succeeded.ChannelID = 9
	succeeded.TotalTokens = 120
	if err := InsertUsageRecordOnce(succeeded); err != nil {
		t.Fatalf("success insert: %v", err)
	}

	var rows []UsageRecord
	if err := DB.Where("request_id = ?", "req-upgrade").Find(&rows).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.Status != UsageStatusSuccess {
		t.Errorf("status = %q, want %q", got.Status, UsageStatusSuccess)
	}
	if got.ChannelID != 9 {
		t.Errorf("channel_id = %d, want 9 — the attempt that produced the usage", got.ChannelID)
	}
	if got.InputTokens != 100 || got.OutputTokens != 20 || got.TotalTokens != 120 {
		t.Errorf("tokens = %d/%d/%d, want 100/20/120",
			got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if got.UsageSource != UsageSourceReported {
		t.Errorf("usage_source = %q, want %q", got.UsageSource, UsageSourceReported)
	}
	if got.CreatedAt != 1_700_000_060 {
		t.Errorf("created_at = %d, want 1700000060 (the recorded outcome)", got.CreatedAt)
	}
}

func TestInsertUsageRecordOnceDoesNotDowngradeTerminal(t *testing.T) {
	setupUsageTestDB(t)
	if err := InsertUsageRecordOnce(mkRecord("req-down", 1_700_000_000)); err != nil {
		t.Fatalf("success insert: %v", err)
	}
	// A later failing attempt must not erase the outcome already recorded.
	late := mkRecord("req-down", 1_700_000_060)
	late.Status = UsageStatusError
	late.TotalTokens = 0
	if err := InsertUsageRecordOnce(late); err != nil {
		t.Fatalf("late error insert: %v", err)
	}

	var got UsageRecord
	if err := DB.Where("request_id = ?", "req-down").First(&got).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != UsageStatusSuccess {
		t.Errorf("status = %q, want the first recorded %q", got.Status, UsageStatusSuccess)
	}
	if got.TotalTokens != 120 {
		t.Errorf("total_tokens = %d, want 120 (unchanged)", got.TotalTokens)
	}
}

// Repeating a terminal outcome is still idempotent: the first one stands and no
// second row appears.
func TestInsertUsageRecordOnceRepeatedTerminalKeepsFirst(t *testing.T) {
	setupUsageTestDB(t)
	if err := InsertUsageRecordOnce(mkRecord("req-repeat", 1_700_000_000)); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := mkRecord("req-repeat", 1_700_000_060)
	second.TotalTokens = 999
	if err := InsertUsageRecordOnce(second); err != nil {
		t.Fatalf("second: %v", err)
	}

	var count int64
	DB.Model(&UsageRecord{}).Where("request_id = ?", "req-repeat").Count(&count)
	if count != 1 {
		t.Fatalf("want 1 row, got %d", count)
	}
	var got UsageRecord
	DB.Where("request_id = ?", "req-repeat").First(&got)
	if got.TotalTokens != 120 {
		t.Errorf("total_tokens = %d, want 120 (the first outcome stands)", got.TotalTokens)
	}
}

// An error row is only replaced by a terminal outcome, never by another error.
func TestInsertUsageRecordOnceRepeatedErrorKeepsFirst(t *testing.T) {
	setupUsageTestDB(t)
	first := mkRecord("req-err2", 1_700_000_000)
	first.Status = UsageStatusError
	first.ChannelID = 3
	if err := InsertUsageRecordOnce(first); err != nil {
		t.Fatalf("first error: %v", err)
	}
	second := mkRecord("req-err2", 1_700_000_060)
	second.Status = UsageStatusError
	second.ChannelID = 9
	if err := InsertUsageRecordOnce(second); err != nil {
		t.Fatalf("second error: %v", err)
	}

	var rows []UsageRecord
	DB.Where("request_id = ?", "req-err2").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 row, got %d", len(rows))
	}
	if rows[0].ChannelID != 3 {
		t.Errorf("channel_id = %d, want 3 (first error stands)", rows[0].ChannelID)
	}
}

func TestUsageAggregationRates(t *testing.T) {
	setupUsageTestDB(t)
	day1 := int64(1_700_000_000) // 2023-11-14
	day2 := day1 + 86400
	recs := []*UsageRecord{
		// A/B: cache reported; A hits, B reports zero (not fabricated!)
		{RequestID: "a", UserID: 1, ModelName: "m", InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
			CacheReadInputTokens: ptrInt64(40), UsageSource: UsageSourceReported,
			CacheUsageStatus: CacheUsageReported, RequestCount: 1, Status: UsageStatusSuccess, CreatedAt: day1},
		{RequestID: "b", UserID: 1, ModelName: "m", InputTokens: 50, OutputTokens: 10, TotalTokens: 60,
			CacheReadInputTokens: ptrInt64(0), UsageSource: UsageSourceReported,
			CacheUsageStatus: CacheUsageReported, RequestCount: 1, Status: UsageStatusSuccess, CreatedAt: day1},
		// C: cache not reported -> lowers coverage, never fakes a 0% hit
		{RequestID: "c", UserID: 2, ModelName: "m", InputTokens: 30, OutputTokens: 5, TotalTokens: 35,
			UsageSource: UsageSourceReported, CacheUsageStatus: CacheUsageNotReported,
			RequestCount: 1, Status: UsageStatusSuccess, CreatedAt: day2},
		// D: error record -> request_count 0, visible via error_calls
		{RequestID: "d", UserID: 1, ModelName: "m", InputTokens: 0, UsageSource: UsageSourceEstimated,
			CacheUsageStatus: CacheUsageNotReported, RequestCount: 0, Status: UsageStatusError, CreatedAt: day1},
		// E: invalid cache values are counted separately, excluded from hit rates
		{RequestID: "e", UserID: 1, ModelName: "m", InputTokens: 10,
			CacheReadInputTokens: ptrInt64(999), UsageSource: UsageSourceReported,
			CacheUsageStatus: CacheUsageInvalid, RequestCount: 1, Status: UsageStatusSuccess, CreatedAt: day1},
		// F: legacy source must never enter new statistics
		{RequestID: "f", UserID: 1, ModelName: "m", InputTokens: 99999, OutputTokens: 99999, TotalTokens: 199998,
			UsageSource: UsageSourceLegacy, CacheUsageStatus: CacheUsageNotReported,
			RequestCount: 1, Status: UsageStatusSuccess, CreatedAt: day1},
	}
	for _, r := range recs {
		if r.ModelName == "" {
			r.ModelName = "m"
		}
		if r.TokenID == nil {
			r.TokenID = ptrInt(7)
		}
		r.ChannelID = 3
		if err := InsertUsageRecordOnce(r); err != nil {
			t.Fatalf("insert %s: %v", r.RequestID, err)
		}
	}

	row, err := SummarizeUsage(UsageFilter{})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	d := row.Summary()
	// legacy F excluded: 100+50+30+0+10 = 190
	if d.InputTokens != 190 {
		t.Fatalf("input tokens want 190, got %d", d.InputTokens)
	}
	if d.RequestCount != 4 {
		t.Fatalf("request count want 4 (D contributes 0), got %d", d.RequestCount)
	}
	// token hit rate only over reported records: (40+0) / (100+50)
	if d.CacheHitRate == nil || *d.CacheHitRate != 40.0/150.0 {
		t.Fatalf("cache hit rate want 40/150, got %v", d.CacheHitRate)
	}
	// call hit rate: 1 of 2 reported calls hit
	if d.CallHitRate == nil || *d.CallHitRate != 0.5 {
		t.Fatalf("call hit rate want 0.5, got %v", d.CallHitRate)
	}
	// coverage: 2 reported / 4 generated (A,B,C,E are successes; D is error)
	if d.CacheCoverage == nil || *d.CacheCoverage != 2.0/4.0 {
		t.Fatalf("coverage want 2/4, got %v", d.CacheCoverage)
	}
	if d.InvalidCacheCalls != 1 || d.ErrorCalls != 1 {
		t.Fatalf("invalid/error counters wrong: invalid=%d error=%d", d.InvalidCacheCalls, d.ErrorCalls)
	}

	// per-user scoping: user 2 only sees its own record
	uid := 2
	u2, err := SummarizeUsage(UsageFilter{UserID: &uid})
	if err != nil {
		t.Fatal(err)
	}
	if u2.Summary().InputTokens != 30 {
		t.Fatalf("user 2 want 30, got %d", u2.Summary().InputTokens)
	}
	// user 2 has no reported cache data: rates must be null, not 0%
	s2 := u2.Summary()
	if s2.CacheHitRate != nil || s2.CallHitRate != nil {
		t.Fatalf("uncomputable rates must be null, got hit=%v call=%v", s2.CacheHitRate, s2.CallHitRate)
	}

	// trend grouping across days
	rows, err := SummarizeUsageByDay(UsageFilter{})
	if err != nil {
		t.Fatalf("trend: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 days, got %d", len(rows))
	}

	// by-model grouping
	grouped, err := SummarizeUsageBy(UsageFilter{}, "by-model", 10)
	if err != nil {
		t.Fatalf("by-model: %v", err)
	}
	if len(grouped) != 1 || grouped[0].ModelName != "m" {
		t.Fatalf("unexpected by-model rows: %+v", grouped)
	}

	// by-user grouping (verifying join with users table without ambiguous column error)
	byUser, err := SummarizeUsageBy(UsageFilter{}, "by-user", 10)
	if err != nil {
		t.Fatalf("by-user: %v", err)
	}
	if len(byUser) == 0 {
		t.Fatalf("expected by-user rows, got 0")
	}

	// by-channel grouping (verifying join with channels table without ambiguous column error)
	byChannel, err := SummarizeUsageBy(UsageFilter{}, "by-channel", 10)
	if err != nil {
		t.Fatalf("by-channel: %v", err)
	}
	if len(byChannel) == 0 {
		t.Fatalf("expected by-channel rows, got 0")
	}
}

func TestUsageFilterCutoverExcludesPreCutover(t *testing.T) {
	setupUsageTestDB(t)
	_ = InsertUsageRecordOnce(&UsageRecord{
		RequestID: "pre", UserID: 1, ModelName: "m", InputTokens: 100,
		UsageSource: UsageSourceReported, CacheUsageStatus: CacheUsageNotReported,
		RequestCount: 1, Status: UsageStatusSuccess,
		CreatedAt: 1, // far before cutover written by v001
	})
	row, err := SummarizeUsage(UsageFilter{AfterCutover: true})
	if err != nil {
		t.Fatal(err)
	}
	if row.InputTokens != 0 {
		t.Fatalf("pre-cutover record must be excluded, got %d", row.InputTokens)
	}
}

func TestRecorderErrorRowZeroRequestCount(t *testing.T) {
	setupUsageTestDB(t)
	// the relay/usage recorder maps Status=error to request_count=0; verify the
	// DB-level contract it relies on.
	rec := &UsageRecord{
		RequestID: "err-1", UserID: 1, ModelName: "m",
		UsageSource: UsageSourceEstimated, CacheUsageStatus: CacheUsageNotReported,
		RequestCount: 0, Status: UsageStatusError, CreatedAt: 1_700_000_000,
	}
	if err := InsertUsageRecordOnce(rec); err != nil {
		t.Fatal(err)
	}
	row, err := SummarizeUsage(UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if row.RequestCount != 0 || row.ErrorCalls != 1 {
		t.Fatalf("error row must not inflate request_count: rc=%d err=%d", row.RequestCount, row.ErrorCalls)
	}
}
