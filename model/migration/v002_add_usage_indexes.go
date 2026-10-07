package migration

import (
	"fmt"

	"gorm.io/gorm"
)

// V002AddUsageIndexes adds the composite indexes required by the aggregation
// queries in docs/one-api-modernization-implementation.md §4.2. Idempotent via
// Migrator().HasIndex, because CREATE INDEX IF NOT EXISTS is not portable.
var V002AddUsageIndexes = Migration{
	Version:     2,
	Name:        "add_usage_indexes",
	Description: "add composite indexes on usage_records for aggregation queries",
	Apply: func(db *gorm.DB, dialect string) error {
		if !db.Migrator().HasTable("usage_records") {
			return fmt.Errorf("usage_records missing; v002 must run after v001")
		}
		indexes := []struct {
			name string
			ddl  string
		}{
			{"idx_usage_user_created", "CREATE INDEX idx_usage_user_created ON usage_records (user_id, created_at)"},
			{"idx_usage_token_created", "CREATE INDEX idx_usage_token_created ON usage_records (token_id, created_at)"},
			{"idx_usage_model_created", "CREATE INDEX idx_usage_model_created ON usage_records (model_name, created_at)"},
			{"idx_usage_channel_created", "CREATE INDEX idx_usage_channel_created ON usage_records (channel_id, created_at)"},
			{"idx_usage_created_at", "CREATE INDEX idx_usage_created_at ON usage_records (created_at)"},
		}
		for _, idx := range indexes {
			if db.Migrator().HasIndex("usage_records", idx.name) {
				continue
			}
			if err := db.Exec(idx.ddl).Error; err != nil {
				return fmt.Errorf("create index %s: %w", idx.name, err)
			}
		}
		return nil
	},
}
