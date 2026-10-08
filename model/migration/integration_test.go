package migration_test

import (
	"os"
	"sort"
	"testing"

	"github.com/songquanpeng/one-api/model/migration"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openDB opens a gorm connection for the given dialect using the raw DSN.
func openDB(t *testing.T, dialect, dsn string) *gorm.DB {
	t.Helper()
	var (
		db  *gorm.DB
		err error
	)
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	switch dialect {
	case "sqlite":
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
	case "postgres":
		db, err = gorm.Open(postgres.Open(dsn), cfg)
	case "mysql":
		db, err = gorm.Open(mysql.Open(dsn), cfg)
	default:
		t.Fatalf("unknown dialect %q", dialect)
	}
	if err != nil {
		t.Fatalf("open %s: %v", dialect, err)
	}
	return db
}

// appliedVersionsList reads the schema_migrations bookkeeping and returns the
// sorted list of applied version numbers.
func appliedVersionsList(t *testing.T, db *gorm.DB) []int {
	t.Helper()
	var versions []int
	if err := db.Table("schema_migrations").Pluck("version", &versions).Error; err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	sort.Ints(versions)
	return versions
}

// verifyMigrationSuite runs the full §13 migration guarantees against one live
// database connection: idempotent repeat, bookkeeping, per-dialect DDL, legacy
// data preservation, pre-flight report, and the request_id idempotency key.
func verifyMigrationSuite(t *testing.T, name, dialect, dsn string) {
	t.Helper()
	db := openDB(t, dialect, dsn)

	// A pre-existing legacy table with a row, to prove migration never drops
	// data it does not own (docs §10.4 "历史数据未丢失").
	db.Exec("DROP TABLE IF EXISTS migration_legacy_probe")
	if err := db.Exec("CREATE TABLE migration_legacy_probe (id INTEGER PRIMARY KEY, payload VARCHAR(64))").Error; err != nil {
		t.Fatalf("create legacy probe (%s): %v", dialect, err)
	}
	if err := db.Exec("INSERT INTO migration_legacy_probe (id, payload) VALUES (1, 'keep-me')").Error; err != nil {
		t.Fatalf("seed legacy probe (%s): %v", dialect, err)
	}

	// 1. First upgrade: fresh DB gets every registered version applied.
	if err := migration.Migrate(db, name); err != nil {
		t.Fatalf("Migrate first run failed: %v", err)
	}
	want := make([]int, 0, len(migration.Migrations))
	for _, m := range migration.Migrations {
		want = append(want, m.Version)
	}
	sort.Ints(want)
	got := appliedVersionsList(t, db)
	if len(got) != len(want) {
		t.Fatalf("after first Migrate got versions %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applied versions %v want %v", got, want)
		}
	}

	// 2. Idempotent repeat: running Migrate again must not re-apply or duplicate
	//    bookkeeping rows (docs §13 "可重复执行 / 幂等保护", §10 "中断重试").
	if err := migration.Migrate(db, name); err != nil {
		t.Fatalf("Migrate repeat run failed: %v", err)
	}
	if err := migration.Migrate(db, name); err != nil {
		t.Fatalf("Migrate third run failed: %v", err)
	}
	got = appliedVersionsList(t, db)
	if len(got) != len(want) {
		t.Fatalf("repeat Migrate duplicated bookkeeping: got %v want %v", got, want)
	}

	// 3. The new fact table exists (DDL ran for this dialect).
	if !db.Migrator().HasTable("usage_records") {
		t.Fatalf("usage_records not created for dialect %s", dialect)
	}

	// 4. request_id UNIQUE is the idempotency key: a second write with the same
	//    request_id must be rejected at the schema level.
	db.Exec("DELETE FROM usage_records WHERE request_id = 'dup-req'")
	if err := db.Exec("INSERT INTO usage_records (request_id, user_id, total_tokens, created_at) VALUES ('dup-req', 1, 5, 1)").Error; err != nil {
		t.Fatalf("first usage_records insert failed: %v", err)
	}
	if db.Exec("INSERT INTO usage_records (request_id, user_id, total_tokens, created_at) VALUES ('dup-req', 1, 5, 2)").Error == nil {
		t.Fatalf("duplicate request_id was accepted; UNIQUE key missing on %s", dialect)
	}
	db.Exec("DELETE FROM usage_records WHERE request_id = 'dup-req'")

	// 5. Legacy probe data survived the migration untouched.
	var payload string
	if err := db.Table("migration_legacy_probe").Where("id = 1").Pluck("payload", &payload).Error; err != nil {
		t.Fatalf("read legacy probe: %v", err)
	}
	if payload != "keep-me" {
		t.Fatalf("legacy row lost during migration: got %q", payload)
	}

	// 6. Pre-flight report: nothing pending, current == target, no warnings.
	report, err := migration.Check(db, []string{"migration_legacy_probe"})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if len(report.Pending) != 0 {
		t.Fatalf("report has pending versions after Migrate: %v", report.Pending)
	}
	if report.CurrentVersion != migration.TargetVersion() {
		t.Fatalf("report current=%d target=%d", report.CurrentVersion, migration.TargetVersion())
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("report has warnings: %v", report.Warnings)
	}
	if len(report.MissingTables) != 0 {
		t.Fatalf("report missing tables that exist: %v", report.MissingTables)
	}

	db.Exec("DROP TABLE IF EXISTS migration_legacy_probe")
	t.Logf("%s/%s migration suite OK (versions %v, dialect %s)", name, dialect, got, report.Dialect)
}

// SQLite runs unconditionally: it needs no external server, so CI always gets
// the idempotency / report / legacy-preservation signal.
func TestMigrationSuite_SQLite(t *testing.T) {
	verifyMigrationSuite(t, "main-sqlite", "sqlite", "file:memdb_mig?mode=memory&cache=shared")
}

// The PostgreSQL/MySQL suites run only when a test DSN is provided (see
// scripts/smoke/migration_matrix.sh). They prove §13's "旧版 PostgreSQL /
// MySQL 主库与独立日志库迁移均已验证" against real servers.
func TestMigrationSuite_Postgres(t *testing.T) {
	dsn := os.Getenv("OA_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("OA_TEST_PG_DSN not set; skipping Postgres migration suite")
	}
	verifyMigrationSuite(t, "main-postgres", "postgres", dsn)
}

func TestMigrationSuite_PostgresLog(t *testing.T) {
	dsn := os.Getenv("OA_TEST_PG_LOG_DSN")
	if dsn == "" {
		t.Skip("OA_TEST_PG_LOG_DSN not set; skipping Postgres log-DB migration suite")
	}
	verifyMigrationSuite(t, "log-postgres", "postgres", dsn)
}

func TestMigrationSuite_MySQL(t *testing.T) {
	dsn := os.Getenv("OA_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OA_TEST_MYSQL_DSN not set; skipping MySQL migration suite")
	}
	verifyMigrationSuite(t, "main-mysql", "mysql", dsn)
}

func TestMigrationSuite_MySQLLog(t *testing.T) {
	dsn := os.Getenv("OA_TEST_MYSQL_LOG_DSN")
	if dsn == "" {
		t.Skip("OA_TEST_MYSQL_LOG_DSN not set; skipping MySQL log-DB migration suite")
	}
	verifyMigrationSuite(t, "log-mysql", "mysql", dsn)
}
