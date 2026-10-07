// Package migration provides explicit, versioned and idempotent schema
// migrations for one-api. GORM AutoMigrate remains only as a safety net for
// the legacy tables; every new table, column and index introduced by the
// modernization work must be owned by a migration version in this package.
//
// Design rules (see docs/one-api-modernization-implementation.md §3.4):
//   - every version can be safely retried; already applied versions never run again;
//   - DDL is generated per dialect (sqlite/mysql/postgres), never shared blindly;
//   - a schema_migrations bookkeeping table records version, name and applied time;
//   - Check() supports dry-run/pre-flight without touching data.
package migration

import (
	"fmt"
	"time"

	"github.com/songquanpeng/one-api/common/logger"

	"gorm.io/gorm"
)

// Migration is one versioned step. Apply must be idempotent.
type Migration struct {
	Version     int
	Name        string
	Description string
	Apply       func(db *gorm.DB, dialect string) error
}

type migrationRecord struct {
	Version     int       `gorm:"column:version;primaryKey" json:"version"`
	Name        string    `gorm:"column:name" json:"name"`
	Description string    `gorm:"column:description" json:"description"`
	AppliedAt   time.Time `gorm:"column:applied_at" json:"applied_at"`
}

func (migrationRecord) TableName() string { return "schema_migrations" }

// Dialect returns the short dialect name of the given gorm connection:
// "sqlite", "mysql" or "postgres".
func Dialect(db *gorm.DB) string {
	return db.Dialector.Name()
}

// ensureVersionTable creates the bookkeeping table if missing.
func ensureVersionTable(db *gorm.DB) error {
	if db.Migrator().HasTable(&migrationRecord{}) {
		return nil
	}
	return db.Migrator().CreateTable(&migrationRecord{})
}

func appliedVersions(db *gorm.DB) (map[int]string, error) {
	var records []migrationRecord
	if err := db.Find(&records).Error; err != nil {
		return nil, err
	}
	applied := make(map[int]string, len(records))
	for _, r := range records {
		applied[r.Version] = r.Name
	}
	return applied, nil
}

// Report is the pre-flight result for check/dry-run mode.
type Report struct {
	DBName         string   `json:"db_name"`
	Dialect        string   `json:"dialect"`
	CurrentVersion int      `json:"current_version"`
	TargetVersion  int      `json:"target_version"`
	Applied        []string `json:"applied"`
	Pending        []string `json:"pending"`
	MissingTables  []string `json:"missing_tables"`
	Warnings       []string `json:"warnings"`
}

// Check inspects the database and returns a report without modifying anything.
// requiredTables lists legacy tables a healthy one-api database must contain.
func Check(db *gorm.DB, requiredTables []string) (*Report, error) {
	dialect := Dialect(db)
	if err := ensureVersionTable(db); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(db)
	if err != nil {
		return nil, err
	}
	report := &Report{
		Dialect:       dialect,
		TargetVersion: TargetVersion(),
	}
	target := make(map[int]bool)
	for _, m := range Migrations {
		target[m.Version] = true
	}
	for v, name := range applied {
		if !target[v] {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("applied version %d (%s) is not registered in this build", v, name))
		}
		if v > report.CurrentVersion {
			report.CurrentVersion = v
		}
		report.Applied = append(report.Applied, fmt.Sprintf("%03d_%s", v, name))
	}
	for _, m := range Migrations {
		if _, ok := applied[m.Version]; !ok {
			report.Pending = append(report.Pending, fmt.Sprintf("%03d_%s", m.Version, m.Name))
		}
	}
	for _, t := range requiredTables {
		if !db.Migrator().HasTable(t) {
			report.MissingTables = append(report.MissingTables, t)
		}
	}
	return report, nil
}

// TargetVersion is the highest registered migration version.
func TargetVersion() int {
	max := 0
	for _, m := range Migrations {
		if m.Version > max {
			max = m.Version
		}
	}
	return max
}

// Migrate runs every pending migration in version order and records it.
// It logs database type, current and target version before starting, and it
// is safe to call on every process boot: applied versions are skipped.
func Migrate(db *gorm.DB, dbName string) error {
	dialect := Dialect(db)
	if err := ensureVersionTable(db); err != nil {
		return fmt.Errorf("migration: create bookkeeping table: %w", err)
	}
	applied, err := appliedVersions(db)
	if err != nil {
		return fmt.Errorf("migration: read bookkeeping table: %w", err)
	}
	logger.SysLog(fmt.Sprintf("[migration] db=%s dialect=%s current_version=%d target_version=%d",
		dbName, dialect, maxVersion(applied), TargetVersion()))
	for _, m := range Migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		logger.SysLog(fmt.Sprintf("[migration] %s applying %03d_%s", dbName, m.Version, m.Name))
		if err := m.Apply(db, dialect); err != nil {
			return fmt.Errorf("migration %03d_%s failed: %w", m.Version, m.Name, err)
		}
		record := &migrationRecord{
			Version:     m.Version,
			Name:        m.Name,
			Description: m.Description,
			AppliedAt:   time.Now(),
		}
		// clause conflict-ignore keeps re-recording harmless on retried steps.
		if err := db.Where("version = ?", m.Version).FirstOrCreate(record).Error; err != nil {
			return fmt.Errorf("migration %03d_%s recorded but bookkeeping write failed: %w", m.Version, m.Name, err)
		}
		logger.SysLog(fmt.Sprintf("[migration] %s applied %03d_%s", dbName, m.Version, m.Name))
	}
	return nil
}

func maxVersion(applied map[int]string) int {
	max := 0
	for v := range applied {
		if v > max {
			max = v
		}
	}
	return max
}
