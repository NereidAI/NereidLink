// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Doctor and backup marker CLI commands for ADR-0004.
//
// doctor validates the local database's non-negotiable TimescaleDB
// invariants. Backup marks are separated from backup execution: the platform
// does not run managed backups, so an external successful job records its
// completion for doctor to evaluate as an advisory rather than a hard gate.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/doctor"
	"github.com/nereidai/nereidlink/internal/store"
)

// checkLocalBlob validates the directory backend's precondition when it
// is the effective blob provider: the directory must exist/creatable and
// be writable. The local backend is single-node storage by design; a
// shared or read-only directory makes firmware and media downloads fail
// at runtime, so it is reported as FAIL rather than discovered late.
func checkLocalBlob(cfg *config.Config) []doctor.Check {
	if cfg.Blob.Backend() != config.BlobProviderLocal {
		return nil
	}
	directory := cfg.Blob.LocalDirectory()
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return []doctor.Check{{
			Name:    "local blob directory",
			Level:   doctor.LevelFail,
			Message: "cannot create directory " + directory + ": " + err.Error(),
		}}
	}
	probe := filepath.Join(directory, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return []doctor.Check{{
			Name:    "local blob directory",
			Level:   doctor.LevelFail,
			Message: "directory not writable: " + err.Error(),
		}}
	}
	if err := os.Remove(probe); err != nil {
		return []doctor.Check{{
			Name:    "local blob directory",
			Level:   doctor.LevelFail,
			Message: "probe file not removable: " + err.Error(),
		}}
	}
	return []doctor.Check{{
		Name:    "local blob directory",
		Level:   doctor.LevelPass,
		Message: directory + " exists and is writable",
	}}
}

// doctorCommand returns the storage health check command.
func doctorCommand() *cobra.Command {
	var backupMaxAge time.Duration
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate TimescaleDB storage invariants",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			ctx := context.Background()
			pool, err := store.Connect(ctx, cfg.Postgres.DSN)
			if err != nil {
				return err
			}
			defer pool.Close()

			report := doctor.Run(ctx, pool, backupMaxAge)
			report = append(report, checkLocalBlob(cfg)...)
			for _, check := range report {
				fmt.Printf("%-8s %-34s %s\n",
					check.Level, check.Name, check.Message)
			}
			if report.HasFailure() {
				return fmt.Errorf("one or more required storage checks failed")
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&backupMaxAge, "backup-max-age", 24*time.Hour,
		"advisory threshold for the latest PostgreSQL backup marker")
	return cmd
}

// backupCommand returns the parent command for externally run backup jobs to
// record successful completion.
func backupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Record externally executed backup results",
	}
	cmd.AddCommand(backupMarkCommand())
	return cmd
}

// backupMarkCommand inserts a successful backup marker.
func backupMarkCommand() *cobra.Command {
	var scope string
	var details string
	cmd := &cobra.Command{
		Use:   "mark",
		Short: "Record a successful backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			if scope != "postgres" && scope != "nats" {
				return fmt.Errorf("scope must be postgres or nats")
			}
			raw := json.RawMessage("{}")
			if details != "" {
				if !json.Valid([]byte(details)) {
					return fmt.Errorf("details must be valid JSON")
				}
				raw = json.RawMessage(details)
			}

			ctx := context.Background()
			pool, err := store.Connect(ctx, cfg.Postgres.DSN)
			if err != nil {
				return err
			}
			defer pool.Close()

			var succeededAt time.Time
			if err := pool.QueryRow(ctx,
				`INSERT INTO backup_marker (scope, details)
				 VALUES ($1, $2)
				 RETURNING succeeded_at`,
				scope, raw).Scan(&succeededAt); err != nil {
				return fmt.Errorf("record backup marker: %w", err)
			}
			fmt.Printf("recorded %s backup at %s\n",
				scope, succeededAt.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "postgres", "backup scope: postgres or nats")
	cmd.Flags().StringVar(&details, "details", "", "JSON details about the successful backup")
	return cmd
}
