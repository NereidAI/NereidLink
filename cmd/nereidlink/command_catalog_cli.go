// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// command_catalog_cli.go exposes the one-off backfill that materializes
// automatic command catalog entries (read/write points of the bound
// profile) for existing devices created before profile materialization
// existed. It is safe to run repeatedly: re-materialization only inserts
// missing automatic rows and prunes stale ones; manual declarations are
// never touched.

package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/store"
)

// reconcileTimeout bounds a full-tenant backfill.
const reconcileTimeout = 10 * time.Minute

// reconcileCommandCatalogCommand returns the reconcile parent command.
func reconcileCommandCatalogCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Run explicit reconciliation jobs",
	}
	cmd.AddCommand(commandCatalogBackfillCommand())
	return cmd
}

// commandCatalogBackfillCommand backfills automatic command catalog rows.
func commandCatalogBackfillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "command-catalog",
		Short: "Backfill automatic command catalog entries from device profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), reconcileTimeout)
			defer cancel()
			pool, err := store.Connect(ctx, cfg.Postgres.DSN)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := command.NewStore(pool).MaterializeAll(ctx); err != nil {
				return err
			}
			cmd.Println("command catalog reconciled")
			return nil
		},
	}
}
