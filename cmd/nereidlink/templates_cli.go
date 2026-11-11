// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// templates_cli.go exposes business template-pack import and export for
// air-gapped delivery and versioned template promotion. The binary itself
// ships no business vocabulary; packs are data artifacts validated by the
// business module before they are persisted.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nereidai/nereidlink/internal/business"
	"github.com/nereidai/nereidlink/internal/store"
)

// templatesCommand returns the business template-pack command group.
func templatesCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "templates",
		Short: "Import and export business template packs",
	}

	var (
		filePath   string
		tenantID   string
		actor      string
		format     string
		outputPath string
	)

	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Import a YAML or JSON template pack (single document or templates envelope)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			if tenantID == "" {
				tenantID = cfg.Seed.TenantID
			}
			raw, err := os.ReadFile(filePath) // #nosec G304 -- operator-supplied CLI flag path
			if err != nil {
				return fmt.Errorf("read template pack %q: %w", filePath, err)
			}
			ctx := context.Background()
			pool, err := store.Connect(ctx, cfg.Postgres.DSN)
			if err != nil {
				return err
			}
			defer pool.Close()
			service := business.NewService(business.NewStore(pool), business.Owners{}, nil)
			imported, err := service.ImportTemplates(ctx, tenantID, actor, raw)
			if err != nil {
				return err
			}
			for _, template := range imported {
				fmt.Printf("imported %s/%s\n", template.ID, template.Version)
			}
			fmt.Printf("%d template(s) imported for tenant %s\n", len(imported), tenantID)
			return nil
		},
	}
	importCmd.Flags().StringVarP(&filePath, "file", "f", "", "template pack file path (YAML or JSON)")
	importCmd.Flags().StringVar(&tenantID, "tenant", "", "target tenant id (defaults to the seed tenant)")
	importCmd.Flags().StringVar(&actor, "actor", "cli", "created_by actor id recorded on the imported versions")
	_ = importCmd.MarkFlagRequired("file")

	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export every tenant template as one portable pack",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			if tenantID == "" {
				tenantID = cfg.Seed.TenantID
			}
			ctx := context.Background()
			pool, err := store.Connect(ctx, cfg.Postgres.DSN)
			if err != nil {
				return err
			}
			defer pool.Close()
			service := business.NewService(business.NewStore(pool), business.Owners{}, nil)
			pack, err := service.ExportTemplates(ctx, tenantID)
			if err != nil {
				return err
			}
			var body []byte
			switch format {
			case "yaml":
				body, err = yaml.Marshal(pack)
			case "json":
				body, err = json.MarshalIndent(pack, "", "  ")
				body = append(body, '\n')
			default:
				return fmt.Errorf("format must be yaml or json, got %q", format)
			}
			if err != nil {
				return err
			}
			if outputPath == "" {
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			return os.WriteFile(outputPath, body, 0o600)
		},
	}
	exportCmd.Flags().StringVar(&tenantID, "tenant", "", "source tenant id (defaults to the seed tenant)")
	exportCmd.Flags().StringVar(&format, "format", "yaml", "pack format: yaml or json")
	exportCmd.Flags().StringVarP(&outputPath, "output", "o", "", "write the pack to this file instead of stdout")

	group.AddCommand(importCmd, exportCmd)
	return group
}
