package model

import (
	"errors"
	"fmt"

	"github.com/songquanpeng/one-api/common"

	"gorm.io/gorm"
)

// usage_source values (docs/one-api-modernization-plan.md §2.2)
const (
	UsageSourceReported  = "reported"  // explicitly reported by the upstream
	UsageSourceEstimated = "estimated" // estimated locally from request/response content
	UsageSourceLegacy    = "legacy"    // historical data only; never enters new Token stats
)

// cache_usage_status values (plan §2.3): missing must never be stored as 0.
const (
	CacheUsageReported    = "reported"
	CacheUsageNotReported = "not_reported"
	CacheUsageInvalid     = "invalid"
)

// usage record status
const (
	UsageStatusSuccess = "success"
	UsageStatusError   = "error"
	UsageStatusPartial = "partial"
)

// UsageRecord is the usage fact table created by migration v001/v002.
// request_id is unique so retries and stream finalization stay idempotent.
type UsageRecord struct {
	Id                    int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	RequestID             string `json:"request_id" gorm:"column:request_id;type:varchar(64);uniqueIndex"`
	UserID                int    `json:"user_id" gorm:"column:user_id"`
	TokenID               *int   `json:"token_id" gorm:"column:token_id"`
	ChannelID             int    `json:"channel_id" gorm:"column:channel_id"`
	ModelName             string `json:"model_name" gorm:"column:model_name"`
	InputTokens           int64  `json:"input_tokens" gorm:"column:input_tokens"`
	OutputTokens          int64  `json:"output_tokens" gorm:"column:output_tokens"`
	TotalTokens           int64  `json:"total_tokens" gorm:"column:total_tokens"`
	CacheReadInputTokens  *int64 `json:"cache_read_input_tokens" gorm:"column:cache_read_input_tokens"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens" gorm:"column:cache_write_input_tokens"`
	RequestCount          int    `json:"request_count" gorm:"column:request_count"`
	UsageSource           string `json:"usage_source" gorm:"column:usage_source"`
	CacheUsageStatus      string `json:"cache_usage_status" gorm:"column:cache_usage_status"`
	Stream                bool   `json:"stream" gorm:"column:stream"`
	Status                string `json:"status" gorm:"column:status"`
	CreatedAt             int64  `json:"created_at" gorm:"column:created_at"`
}

func (UsageRecord) TableName() string { return "usage_records" }

// InsertUsageRecordOnce writes the record idempotently keyed by request_id.
// A second call with the same request_id (retry, or stream finalize racing
// with disconnect cleanup) is a no-op returning nil.
func InsertUsageRecordOnce(record *UsageRecord) error {
	if record == nil {
		return errors.New("usage record is nil")
	}
	if record.RequestID == "" {
		return errors.New("usage record requires request_id")
	}
	var count int64
	if err := DB.Model(&UsageRecord{}).
		Where("request_id = ?", record.RequestID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if err := DB.Create(record).Error; err != nil {
		// Two concurrent finalizers can both pass the count check; the unique
		// index is the arbiter. If the row exists now, treat it as success.
		var after int64
		if cntErr := DB.Model(&UsageRecord{}).
			Where("request_id = ?", record.RequestID).
			Count(&after).Error; cntErr == nil && after > 0 {
			return nil
		}
		return err
	}
	return nil
}

// UsageFilter narrows aggregation queries. All fields are optional.
type UsageFilter struct {
	UserID        *int
	TokenID       *int
	ChannelID     *int
	ModelName     string
	Start         int64 // unix seconds, inclusive
	End           int64 // unix seconds, inclusive
	AfterCutover  bool  // when true, exclude records before usage_cutover_at
	IncludeLegacy bool  // legacy records are excluded from all sums unless true
}

func (f UsageFilter) apply(tx *gorm.DB) *gorm.DB {
	tx = tx.Table("usage_records")
	if !f.IncludeLegacy {
		tx = tx.Where("usage_source != ?", UsageSourceLegacy)
	}
	if f.UserID != nil {
		tx = tx.Where("user_id = ?", *f.UserID)
	}
	if f.TokenID != nil {
		tx = tx.Where("token_id = ?", *f.TokenID)
	}
	if f.ChannelID != nil {
		tx = tx.Where("channel_id = ?", *f.ChannelID)
	}
	if f.ModelName != "" {
		tx = tx.Where("model_name = ?", f.ModelName)
	}
	if f.Start != 0 {
		tx = tx.Where("created_at >= ?", f.Start)
	}
	if f.End != 0 {
		tx = tx.Where("created_at <= ?", f.End)
	}
	if f.AfterCutover {
		if cutover := GetUsageCutover(); cutover > 0 {
			tx = tx.Where("created_at >= ?", cutover)
		}
	}
	return tx
}

// UsageAggRow holds one aggregation result row (totals, optionally with a
// group key such as day/user_id/model_name/channel_id).
type UsageAggRow struct {
	Day                 string `gorm:"column:day"`
	UserID              int    `gorm:"column:user_id"`
	Username            string `gorm:"column:username"`
	TokenName           string `gorm:"column:token_name"`
	TokenID             int    `gorm:"column:token_id"`
	ModelName           string `gorm:"column:model_name"`
	ChannelID           int    `gorm:"column:channel_id"`
	InputTokens         int64  `gorm:"column:input_tokens"`
	OutputTokens        int64  `gorm:"column:output_tokens"`
	TotalTokens         int64  `gorm:"column:total_tokens"`
	RequestCount        int64  `gorm:"column:request_count"`
	CacheReadTokens     int64  `gorm:"column:cache_read_tokens"`
	CacheWriteTokens    int64  `gorm:"column:cache_write_tokens"`
	ReportedInputTokens int64  `gorm:"column:reported_input_tokens"`
	ReportedCacheCalls  int64  `gorm:"column:reported_cache_calls"`
	NotReportedCalls    int64  `gorm:"column:not_reported_calls"`
	InvalidCacheCalls   int64  `gorm:"column:invalid_cache_calls"`
	HitCalls            int64  `gorm:"column:hit_calls"`
	GeneratedCalls      int64  `gorm:"column:generated_calls"`
	ErrorCalls          int64  `gorm:"column:error_calls"`
}

// usageAggSelect is portable across sqlite/mysql/postgres: COALESCE and CASE
// are supported by all three dialects in this codebase.
const usageAggSelect = `
	COALESCE(SUM(input_tokens), 0) as input_tokens,
	COALESCE(SUM(output_tokens), 0) as output_tokens,
	COALESCE(SUM(total_tokens), 0) as total_tokens,
	COALESCE(SUM(request_count), 0) as request_count,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'reported' THEN cache_read_input_tokens ELSE 0 END), 0) as cache_read_tokens,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'reported' THEN cache_write_input_tokens ELSE 0 END), 0) as cache_write_tokens,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'reported' THEN input_tokens ELSE 0 END), 0) as reported_input_tokens,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'reported' THEN 1 ELSE 0 END), 0) as reported_cache_calls,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'not_reported' THEN 1 ELSE 0 END), 0) as not_reported_calls,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'invalid' THEN 1 ELSE 0 END), 0) as invalid_cache_calls,
	COALESCE(SUM(CASE WHEN cache_usage_status = 'reported' AND cache_read_input_tokens > 0 THEN 1 ELSE 0 END), 0) as hit_calls,
	COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) as generated_calls,
	COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) as error_calls
`

// SummarizeUsage returns the overall aggregation for the filter.
func SummarizeUsage(f UsageFilter) (*UsageAggRow, error) {
	row := &UsageAggRow{}
	err := f.apply(DB).Select(usageAggSelect).Scan(row).Error
	return row, err
}

// usageDayExpr groups records by calendar day per dialect, mirroring
// SearchLogsByDayAndModel in log.go.
func usageDayExpr() string {
	switch {
	case common.UsingPostgreSQL:
		return "TO_CHAR(date_trunc('day', to_timestamp(created_at)), 'YYYY-MM-DD')"
	case common.UsingSQLite:
		return "strftime('%Y-%m-%d', datetime(created_at, 'unixepoch'))"
	default:
		return "DATE_FORMAT(FROM_UNIXTIME(created_at), '%Y-%m-%d')"
	}
}

// SummarizeUsageByDay returns trend rows ordered by day.
func SummarizeUsageByDay(f UsageFilter) ([]*UsageAggRow, error) {
	var rows []*UsageAggRow
	err := f.apply(DB).
		Select(usageDayExpr() + " as day, " + usageAggSelect).
		Group("day").
		Order("day asc").
		Scan(&rows).Error
	return rows, err
}

// usageGroupColumns whitelists group-by dimensions to prevent injection.
var usageGroupColumns = map[string]string{
	"by-user":    "user_id",
	"by-model":   "model_name",
	"by-channel": "channel_id",
	"by-token":   "token_id",
}

// SummarizeUsageBy groups totals along one dimension ("by-user", "by-model",
// "by-channel" or "by-token"), ordered by total tokens descending.
func SummarizeUsageBy(f UsageFilter, dimension string, limit int) ([]*UsageAggRow, error) {
	col, ok := usageGroupColumns[dimension]
	if !ok {
		return nil, fmt.Errorf("unknown usage dimension %q", dimension)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	selects := col + ", " + usageAggSelect
	if dimension == "by-user" {
		// username is convenient for admin views; join is safe, column fixed.
		selects = "usage_records.user_id as user_id, COALESCE(users.username, '') as username, " + usageAggSelect
		tx := DB.Table("usage_records").
			Joins("LEFT JOIN users ON users.id = usage_records.user_id")
		tx = f.apply(tx)
		var rows []*UsageAggRow
		err := tx.Select(selects).
			Group("usage_records.user_id, users.username").
			Order("total_tokens desc").
			Limit(limit).
			Scan(&rows).Error
		return rows, err
	}
	var rows []*UsageAggRow
	err := f.apply(DB).Select(selects).
		Group(col).
		Order("total_tokens desc").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

// GetUsageCutover reads usage_cutover_at from options; 0 means unset.
func GetUsageCutover() int64 {
	var option Option
	// struct condition lets GORM quote the reserved "key" identifier per dialect
	if err := DB.Where(&Option{Key: "usage_cutover_at"}).First(&option).Error; err != nil {
		return 0
	}
	var ts int64
	if _, err := fmt.Sscanf(option.Value, "%d", &ts); err != nil {
		return 0
	}
	return ts
}

// UsageSummaryData is the frozen wire contract for aggregation endpoints
// (docs/modernization-progress-log.md §0.7). Rates are pointers so that a
// non-computable metric serializes as null instead of a fake 0.
type UsageSummaryData struct {
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	RequestCount          int64    `json:"request_count"`
	CacheReadInputTokens  int64    `json:"cache_read_input_tokens"`
	CacheWriteInputTokens int64    `json:"cache_write_input_tokens"`
	CacheHitRate          *float64 `json:"cache_hit_rate"`
	CallHitRate           *float64 `json:"call_hit_rate"`
	CacheCoverage         *float64 `json:"cache_coverage"`
	GeneratedCalls        int64    `json:"generated_calls"`
	ErrorCalls            int64    `json:"error_calls"`
	NotReportedCacheCalls int64    `json:"not_reported_cache_calls"`
	InvalidCacheCalls     int64    `json:"invalid_cache_calls"`
}

// Summary converts an aggregation row to the wire contract. Rates are always
// computed from aggregated numerator/denominator, never averaged per call.
func (r *UsageAggRow) Summary() UsageSummaryData {
	d := UsageSummaryData{
		InputTokens:           r.InputTokens,
		OutputTokens:          r.OutputTokens,
		TotalTokens:           r.TotalTokens,
		RequestCount:          r.RequestCount,
		CacheReadInputTokens:  r.CacheReadTokens,
		CacheWriteInputTokens: r.CacheWriteTokens,
		GeneratedCalls:        r.GeneratedCalls,
		ErrorCalls:            r.ErrorCalls,
		NotReportedCacheCalls: r.NotReportedCalls,
		InvalidCacheCalls:     r.InvalidCacheCalls,
	}
	if r.ReportedInputTokens > 0 {
		v := float64(r.CacheReadTokens) / float64(r.ReportedInputTokens)
		d.CacheHitRate = &v
	}
	if r.ReportedCacheCalls > 0 {
		v := float64(r.HitCalls) / float64(r.ReportedCacheCalls)
		d.CallHitRate = &v
	}
	if r.GeneratedCalls > 0 {
		v := float64(r.ReportedCacheCalls) / float64(r.GeneratedCalls)
		d.CacheCoverage = &v
	}
	return d
}
