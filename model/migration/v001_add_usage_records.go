package migration

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// V001AddUsageRecords creates the usage_records fact table with a unique
// request_id key and writes the usage_cutover_at option when missing.
// It is idempotent: re-running on a table that already exists is a no-op.
var V001AddUsageRecords = Migration{
	Version:     1,
	Name:        "add_usage_records",
	Description: "create usage_records fact table and usage_cutover_at option",
	Apply: func(db *gorm.DB, dialect string) error {
		if db.Migrator().HasTable("usage_records") {
			return nil
		}
		var ddl string
		switch dialect {
		case "mysql":
			ddl = `CREATE TABLE IF NOT EXISTS usage_records (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				request_id VARCHAR(64) NOT NULL,
				user_id INT NOT NULL,
				token_id INT NULL,
				channel_id INT NOT NULL DEFAULT 0,
				model_name VARCHAR(128) NOT NULL DEFAULT '',
				input_tokens BIGINT NOT NULL DEFAULT 0,
				output_tokens BIGINT NOT NULL DEFAULT 0,
				total_tokens BIGINT NOT NULL DEFAULT 0,
				cache_read_input_tokens BIGINT NULL,
				cache_write_input_tokens BIGINT NULL,
				request_count INT NOT NULL DEFAULT 1,
				usage_source VARCHAR(16) NOT NULL DEFAULT '',
				cache_usage_status VARCHAR(16) NOT NULL DEFAULT '',
				stream TINYINT(1) NOT NULL DEFAULT 0,
				status VARCHAR(16) NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL,
				UNIQUE KEY uk_usage_request_id (request_id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
		case "postgres":
			ddl = `CREATE TABLE IF NOT EXISTS usage_records (
				id BIGSERIAL PRIMARY KEY,
				request_id VARCHAR(64) NOT NULL UNIQUE,
				user_id INT NOT NULL,
				token_id INT NULL,
				channel_id INT NOT NULL DEFAULT 0,
				model_name VARCHAR(128) NOT NULL DEFAULT '',
				input_tokens BIGINT NOT NULL DEFAULT 0,
				output_tokens BIGINT NOT NULL DEFAULT 0,
				total_tokens BIGINT NOT NULL DEFAULT 0,
				cache_read_input_tokens BIGINT NULL,
				cache_write_input_tokens BIGINT NULL,
				request_count INT NOT NULL DEFAULT 1,
				usage_source VARCHAR(16) NOT NULL DEFAULT '',
				cache_usage_status VARCHAR(16) NOT NULL DEFAULT '',
				stream BOOLEAN NOT NULL DEFAULT FALSE,
				status VARCHAR(16) NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL
			)`
		default: // sqlite
			ddl = `CREATE TABLE IF NOT EXISTS usage_records (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				request_id TEXT NOT NULL UNIQUE,
				user_id INTEGER NOT NULL,
				token_id INTEGER,
				channel_id INTEGER NOT NULL DEFAULT 0,
				model_name TEXT NOT NULL DEFAULT '',
				input_tokens INTEGER NOT NULL DEFAULT 0,
				output_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens INTEGER NOT NULL DEFAULT 0,
				cache_read_input_tokens INTEGER,
				cache_write_input_tokens INTEGER,
				request_count INTEGER NOT NULL DEFAULT 1,
				usage_source TEXT NOT NULL DEFAULT '',
				cache_usage_status TEXT NOT NULL DEFAULT '',
				stream INTEGER NOT NULL DEFAULT 0,
				status TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL
			)`
		}
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("create usage_records: %w", err)
		}
		return ensureUsageCutover(db, dialect)
	},
}

// ensureUsageCutover writes the cutover timestamp option once. On a fresh
// install the value simply equals first-boot time; on an upgraded install it
// marks where quota-era history ends and Token-era records begin.
func ensureUsageCutover(db *gorm.DB, dialect string) error {
	if !db.Migrator().HasTable("options") {
		// Fresh install where options does not exist yet (tests only): the
		// boot flow runs AutoMigrate before migrations in production, so the
		// option row is created there. Missing cutover means "no filter".
		return nil
	}
	key := quoteIdent(dialect, "key")
	val := quoteIdent(dialect, "value")
	var count int64
	if err := db.Table("options").
		Where(key+" = ?", UsageCutoverOptionKey).
		Count(&count).Error; err != nil {
		return fmt.Errorf("check usage_cutover_at option: %w", err)
	}
	if count > 0 {
		return nil
	}
	return db.Exec(
		"INSERT INTO options ("+key+", "+val+") VALUES (?, ?)",
		UsageCutoverOptionKey, fmt.Sprintf("%d", time.Now().Unix()),
	).Error
}

func quoteIdent(dialect, name string) string {
	switch dialect {
	case "mysql":
		return "`" + name + "`"
	default: // sqlite and postgres accept double quotes
		return `"` + name + `"`
	}
}
