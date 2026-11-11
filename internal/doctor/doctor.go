// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package doctor evaluates the local database against the storage design in
// ADR-0004. It is intentionally advisory rather than a repair tool: doctor
// reports whether the platform's non-negotiable dependencies are present and
// leaves remediation to the operator, because automatically altering
// compression policies or continuous aggregates can destroy data.
//
// A failed check is a hard correctness problem (the extension is absent, a
// required hypertable or policy is missing, or the migration ledger is dirty)
// and the CLI returns a non-zero exit status. An advisory check, notably an
// aged backup marker, never fails the command because backups are
// customer-owned and the ADR explicitly says "warn without enforcing".
package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Level classifies one doctor observation.
type Level string

const (
	// LevelPass means the checked invariant holds.
	LevelPass Level = "PASS"
	// LevelAdvisory means the platform remains safe to run but an operator
	// should review the finding.
	LevelAdvisory Level = "ADVISORY"
	// LevelFail means a required storage invariant is absent or broken.
	LevelFail Level = "FAIL"
)

// Check is one named storage observation.
type Check struct {
	// Name is the stable invariant label printed to the operator.
	Name string
	// Level determines whether the doctor run exits non-zero.
	Level Level
	// Message explains the finding; it never contains user payload data.
	Message string
}

// Report is the ordered set of checks produced by Run.
type Report []Check

// HasFailure reports whether any required invariant failed.
func (r Report) HasFailure() bool {
	for _, check := range r {
		if check.Level == LevelFail {
			return true
		}
	}
	return false
}

// Run evaluates the TimescaleDB storage invariants against pool.
//
// backupMaxAge sets the advisory threshold for the latest successful
// PostgreSQL backup marker. A zero or negative value disables backup age
// enforcement rather than treating every database as overdue, which keeps
// the command usable in environments where backup markers are intentionally
// not recorded.
func Run(ctx context.Context, pool *pgxpool.Pool, backupMaxAge time.Duration) Report {
	report := Report{}

	var timescaledbVersion string
	err := pool.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`,
	).Scan(&timescaledbVersion)
	switch err {
	case nil:
		report.add("timescaledb extension", LevelPass,
			"version "+timescaledbVersion)
	default:
		report.add("timescaledb extension", LevelFail,
			"TimescaleDB extension is not installed in this database")
	}

	var migrationVersion int64
	var dirty bool
	if err := pool.QueryRow(ctx,
		`SELECT version, dirty FROM schema_migrations`,
	).Scan(&migrationVersion, &dirty); err != nil {
		report.add("migration ledger", LevelFail,
			fmt.Sprintf("cannot read schema_migrations: %v", err))
	} else if dirty {
		report.add("migration ledger", LevelFail,
			fmt.Sprintf("migration %d is marked dirty; resolve it before serving", migrationVersion))
	} else {
		report.add("migration ledger", LevelPass,
			fmt.Sprintf("version %d, clean", migrationVersion))
	}

	report.addCount(ctx, pool, "ts_telemetry hypertable",
		`SELECT count(*) FROM timescaledb_information.hypertables
		 WHERE hypertable_schema = 'public' AND hypertable_name = 'ts_telemetry'`,
		"public.ts_telemetry is not a hypertable")
	report.addCount(ctx, pool, "compression enabled",
		`SELECT count(DISTINCT hypertable_name)
		 FROM timescaledb_information.compression_settings
		 WHERE hypertable_schema = 'public' AND hypertable_name = 'ts_telemetry'`,
		"compression is not enabled on public.ts_telemetry")
	report.addCount(ctx, pool, "compression policy",
		`SELECT count(*) FROM timescaledb_information.jobs
		 WHERE proc_name = 'policy_compression'`,
		"no scheduled compression policy exists")
	report.addCountAtLeast(ctx, pool, "rollup refresh policies",
		`SELECT count(*) FROM timescaledb_information.jobs
		 WHERE proc_name = 'policy_refresh_continuous_aggregate'`,
		2,
		"two continuous aggregate refresh policies are required")

	for _, view := range []string{"ts_telemetry_hourly", "ts_telemetry_daily"} {
		report.addContinuousAggregate(ctx, pool, view)
	}

	report.addBackupCheck(ctx, pool, backupMaxAge)
	return report
}

// add appends one observation to the report.
func (r *Report) add(name string, level Level, message string) {
	*r = append(*r, Check{Name: name, Level: level, Message: message})
}

// addCount passes when the scalar count equals one.
func (r *Report) addCount(ctx context.Context, pool *pgxpool.Pool, name, query, missingMessage string) {
	r.addCountAtLeast(ctx, pool, name, query, 1, missingMessage)
}

// addCountAtLeast passes when the scalar count meets the required value.
func (r *Report) addCountAtLeast(ctx context.Context, pool *pgxpool.Pool, name, query string, required int, missingMessage string) {
	var count int
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
		r.add(name, LevelFail, fmt.Sprintf("cannot inspect: %v", err))
		return
	}
	if count < required {
		r.add(name, LevelFail, missingMessage)
		return
	}
	r.add(name, LevelPass, fmt.Sprintf("%d found", count))
}

// addContinuousAggregate verifies one rollup exists and uses real-time union
// semantics. The latter is what lets queries past the refresh watermark see
// live hypertable rows without waiting for the next policy tick.
func (r *Report) addContinuousAggregate(ctx context.Context, pool *pgxpool.Pool, view string) {
	var materializedOnly bool
	err := pool.QueryRow(ctx,
		`SELECT materialized_only
		 FROM timescaledb_information.continuous_aggregates
		 WHERE view_schema = 'public' AND view_name = $1`, view,
	).Scan(&materializedOnly)
	if err != nil {
		r.add(view+" continuous aggregate", LevelFail,
			fmt.Sprintf("continuous aggregate is missing: %v", err))
		return
	}
	if materializedOnly {
		r.add(view+" continuous aggregate", LevelFail,
			"materialized_only is true; queries past the refresh watermark are invisible")
		return
	}
	r.add(view+" continuous aggregate", LevelPass, "real-time union enabled")
}

// addBackupCheck reports but never fails on an absent or aged PostgreSQL
// backup marker, matching the ADR's advisory-only backup boundary.
func (r *Report) addBackupCheck(ctx context.Context, pool *pgxpool.Pool, maxAge time.Duration) {
	if maxAge <= 0 {
		r.add("backup marker", LevelAdvisory, "backup age enforcement disabled")
		return
	}

	var lastSuccess *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT max(succeeded_at) FROM backup_marker WHERE scope = 'postgres'`,
	).Scan(&lastSuccess); err != nil {
		r.add("backup marker", LevelFail, fmt.Sprintf("cannot read marker: %v", err))
		return
	}
	if lastSuccess == nil {
		r.add("backup marker", LevelAdvisory,
			"no successful PostgreSQL backup marker recorded")
		return
	}

	age := time.Since(*lastSuccess)
	if age > maxAge {
		r.add("backup marker", LevelAdvisory,
			fmt.Sprintf("latest PostgreSQL backup is %s old; threshold is %s",
				age.Round(time.Minute), maxAge))
		return
	}
	r.add("backup marker", LevelPass,
		fmt.Sprintf("latest PostgreSQL backup recorded %s ago", age.Round(time.Second)))
}
