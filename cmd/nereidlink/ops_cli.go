// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ops_cli.go is the operator command line for data deletion and
// cleanup (ADR-0023). An operator runs a destructive command locally;
// the CLI opens an operation order on the loopback ops listener and
// stops. Nothing is deleted until the super administrator reviews the
// full order on the web and confirms it. The operator identity (OS
// user by default) is recorded on the order.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultOpsAddress points at the loopback ops admin listener.
const defaultOpsAddress = "http://127.0.0.1:19190"

// opsAddress is the --addr value shared by ops subcommands.
var opsAddress string

// opsCommand returns the operator cleanup command group.
func opsCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "ops",
		Short: "Data cleanup orders (confirmed by the super administrator)",
	}
	group.PersistentFlags().StringVar(&opsAddress, "addr", defaultOpsAddress,
		"ops admin listener base URL")

	list := &cobra.Command{
		Use:   "list",
		Short: "List operation orders",
		RunE:  opsList,
	}
	list.Flags().String("status", "", "filter by status (PENDING, EXECUTED, ...)")
	list.Flags().String("tenant", "", "filter by tenant id")

	show := &cobra.Command{
		Use:   "show <order-id>",
		Short: "Show one operation order with its full payload",
		Args:  cobra.ExactArgs(1),
		RunE:  opsShow,
	}

	purge := &cobra.Command{
		Use:   "purge-telemetry",
		Short: "Delete telemetry older than a cutoff (awaiting confirmation)",
		RunE:  opsPurgeTelemetry,
	}
	purge.Flags().String("tenant", "", "tenant id (required)")
	purge.Flags().String("before", "",
		"absolute cutoff, RFC3339 or YYYY-MM-DD")
	purge.Flags().String("older-than", "",
		"relative cutoff duration, e.g. 720h")
	purge.Flags().String("reason", "", "why the data is purged")
	purge.Flags().String("operator", "",
		"operator name recorded on the order (defaults to OS user)")
	_ = purge.MarkFlagRequired("tenant")

	release := &cobra.Command{
		Use:   "release-quarantine",
		Short: "Release a quarantined device (awaiting confirmation)",
		RunE:  opsReleaseQuarantine,
	}
	release.Flags().String("tenant", "", "tenant id (required)")
	release.Flags().String("device", "", "device id (required)")
	release.Flags().String("note", "",
		"why the incident is resolved (required)")
	release.Flags().String("operator", "",
		"operator name recorded on the order (defaults to OS user)")
	_ = release.MarkFlagRequired("tenant")
	_ = release.MarkFlagRequired("device")
	_ = release.MarkFlagRequired("note")

	cancel := &cobra.Command{
		Use:   "cancel <order-id>",
		Short: "Withdraw an order you opened",
		Args:  cobra.ExactArgs(1),
		RunE:  opsCancel,
	}
	cancel.Flags().String("operator", "", "operator name (defaults to OS user)")

	group.AddCommand(list, show, purge, release, cancel)
	return group
}

// opsReleaseQuarantine opens a dual-control order to lift quarantine. The
// direct release endpoint already exists for the fast incident path; this
// command exists so the ADR-0023 route is reachable from the CLI as the ADR
// requires, rather than only by hand-crafting a POST to the ops API.
func opsReleaseQuarantine(cmd *cobra.Command, _ []string) error {
	tenant, _ := cmd.Flags().GetString("tenant")
	device, _ := cmd.Flags().GetString("device")
	note, _ := cmd.Flags().GetString("note")
	operator, _ := cmd.Flags().GetString("operator")
	if operator == "" {
		operator = defaultOperator()
	}
	payload, _ := json.Marshal(map[string]string{
		"device_id": device,
		"note":      note,
	})
	body, _ := json.Marshal(map[string]any{
		"tenant_id": tenant,
		"action":    "quarantine.release",
		"summary":   fmt.Sprintf("release quarantined device %s", device),
		"payload":   json.RawMessage(payload),
		"operator":  operator,
	})
	resp, err := opsCall(http.MethodPost, "/v1/ops/orders", body)
	if err != nil {
		return err
	}
	var order map[string]any
	if err := json.Unmarshal(resp, &order); err != nil {
		return err
	}
	fmt.Printf("order %s opened by %s; status PENDING\n", order["id"], operator)
	fmt.Println("the device stays quarantined until the super administrator confirms it on the web")
	return nil
}

// opsList prints orders as a compact table.
func opsList(cmd *cobra.Command, _ []string) error {
	status, _ := cmd.Flags().GetString("status")
	tenant, _ := cmd.Flags().GetString("tenant")
	query := ""
	parts := []string{}
	if status != "" {
		parts = append(parts, "status="+status)
	}
	if tenant != "" {
		parts = append(parts, "tenant_id="+tenant)
	}
	if len(parts) > 0 {
		query = "?" + strings.Join(parts, "&")
	}
	resp, err := opsCall(http.MethodGet, "/v1/ops/orders"+query, nil)
	if err != nil {
		return err
	}
	var orders []map[string]any
	if err := json.Unmarshal(resp, &orders); err != nil {
		return err
	}
	if len(orders) == 0 {
		fmt.Println("no orders")
		return nil
	}
	for _, order := range orders {
		fmt.Printf("%s  %-9s  %-22s  %s\n",
			order["id"], order["status"], order["action"], order["summary"])
	}
	return nil
}

// opsShow prints the full order document including payload.
func opsShow(_ *cobra.Command, args []string) error {
	resp, err := opsCall(http.MethodGet, "/v1/ops/orders/"+args[0], nil)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, resp, "", "  "); err != nil {
		return err
	}
	fmt.Println(pretty.String())
	return nil
}

// opsPurgeTelemetry resolves the cutoff and opens the purge order.
func opsPurgeTelemetry(cmd *cobra.Command, _ []string) error {
	tenant, _ := cmd.Flags().GetString("tenant")
	before, _ := cmd.Flags().GetString("before")
	olderThan, _ := cmd.Flags().GetString("older-than")
	reason, _ := cmd.Flags().GetString("reason")
	operator, _ := cmd.Flags().GetString("operator")
	if operator == "" {
		operator = defaultOperator()
	}
	if (before == "" && olderThan == "") || (before != "" && olderThan != "") {
		return fmt.Errorf("provide exactly one of --before or --older-than")
	}

	cutoff, err := resolveCutoff(before, olderThan)
	if err != nil {
		return err
	}
	cutoffText := cutoff.UTC().Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]string{
		"before": cutoffText,
		"reason": reason,
	})
	summary := fmt.Sprintf("purge telemetry before %s", cutoffText)
	body, _ := json.Marshal(map[string]any{
		"tenant_id": tenant,
		"action":    "telemetry.purge",
		"summary":   summary,
		"payload":   json.RawMessage(payload),
		"operator":  operator,
	})
	resp, err := opsCall(http.MethodPost, "/v1/ops/orders", body)
	if err != nil {
		return err
	}
	var order map[string]any
	if err := json.Unmarshal(resp, &order); err != nil {
		return err
	}
	fmt.Printf("order %s opened by %s; status PENDING\n", order["id"], operator)
	fmt.Println("nothing is deleted until the super administrator confirms it on the web")
	return nil
}

// opsCancel withdraws an order as the recorded operator.
func opsCancel(cmd *cobra.Command, args []string) error {
	operator, _ := cmd.Flags().GetString("operator")
	if operator == "" {
		operator = defaultOperator()
	}
	body, _ := json.Marshal(map[string]string{"operator": operator})
	resp, err := opsCall(http.MethodPost,
		"/v1/ops/orders/"+args[0]+"/cancel", body)
	if err != nil {
		return err
	}
	var order map[string]any
	if err := json.Unmarshal(resp, &order); err != nil {
		return err
	}
	fmt.Printf("order %s: %s\n", order["id"], order["status"])
	return nil
}

// resolveCutoff parses an absolute cutoff or computes it from a
// duration relative to now.
func resolveCutoff(before, olderThan string) (time.Time, error) {
	if olderThan != "" {
		duration, err := time.ParseDuration(olderThan)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse --older-than: %w", err)
		}
		if duration <= 0 {
			return time.Time{}, fmt.Errorf("--older-than must be positive")
		}
		return time.Now().UTC().Add(-duration), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, before); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse --before: expected RFC3339 or YYYY-MM-DD")
}

// defaultOperator returns the invoking OS user name, with the USER
// environment variable as a fallback.
func defaultOperator() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return os.Getenv("USER")
}

// opsCall executes one HTTP request against the loopback ops listener
// and decodes either the raw body or a REST error envelope.
func opsCall(method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, opsAddress+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ops request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		message := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
			message = envelope.Error.Code + ": " + envelope.Error.Message
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, message)
	}
	return raw, nil
}
