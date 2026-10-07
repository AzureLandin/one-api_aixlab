package migration

// Migrations is the ordered registry of all versioned schema migrations.
// Append new versions at the end; never renumber or delete existing ones.
var Migrations = []Migration{
	V001AddUsageRecords,
	V002AddUsageIndexes,
}

// UsageCutoverOptionKey is stored in the options table: aggregated usage
// queries never mix records created before this unix timestamp into new
// Token statistics (docs/one-api-modernization-implementation.md §4.2.2).
const UsageCutoverOptionKey = "usage_cutover_at"
