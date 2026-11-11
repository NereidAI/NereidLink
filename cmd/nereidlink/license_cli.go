// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// license_cli.go exposes operator-controlled offline license actions.
// Installation is CLI-only per ADR-0024 and is recorded in the audit log.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/nereidai/nereidlink/internal/license"
	"github.com/nereidai/nereidlink/internal/store"
)

// licenseFilePath is the --file value for license install.
var licenseFilePath string

// licenseCommand returns the license management command group.
func licenseCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "license",
		Short: "Manage offline licenses",
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Install a signed license file",
		RunE:  installLicense,
	}
	install.Flags().StringVar(&licenseFilePath, "file", "", "signed license file path")
	_ = install.MarkFlagRequired("file")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show deployment license status as JSON",
		RunE:  showLicenseStatus,
	}
	group.AddCommand(install, status)
	return group
}

// openLicenseService constructs the CLI's license facade from the same merged
// configuration used by the server. The caller owns and must close the pool.
func openLicenseService() (*license.Service, *pgxpool.Pool, error) {
	cfg, err := requireConfig()
	if err != nil {
		return nil, nil, err
	}
	publicKey, err := license.LoadPublicKey(cfg.License.PublicKeyPath)
	if err != nil {
		return nil, nil, err
	}
	pool, err := store.Connect(context.Background(), cfg.Postgres.DSN)
	if err != nil {
		return nil, nil, err
	}
	service := license.New(pool, nil, nil, license.Config{
		PublicKey:   publicKey,
		GracePeriod: durationOrDefault(cfg.License.GracePeriod, 0),
		ClockFile:   cfg.License.ClockFile,
	})
	return service, pool, nil
}

// installLicense verifies the file and replaces the current license scope.
func installLicense(cmd *cobra.Command, args []string) error {
	file, err := license.ReadFile(licenseFilePath)
	if err != nil {
		return err
	}
	service, pool, err := openLicenseService()
	if err != nil {
		return err
	}
	defer pool.Close()
	payload, err := service.Install(context.Background(), file, "cli")
	if err != nil {
		return err
	}
	fmt.Printf("installed license for %s until %s\n", payload.Scope, payload.ExpiresAt)
	return nil
}

// showLicenseStatus lets deployment automation preserve an existing license
// instead of replacing it on every idempotent Helm upgrade.
func showLicenseStatus(cmd *cobra.Command, args []string) error {
	service, pool, err := openLicenseService()
	if err != nil {
		return err
	}
	defer pool.Close()
	info, err := service.DeploymentInfo(context.Background())
	if err != nil {
		return err
	}
	if info.Features == nil {
		info.Features = []string{}
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
}
