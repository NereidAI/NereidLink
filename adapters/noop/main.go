// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command noop is a protocol-less southbound module used to validate the
// adapter SDK chain end to end: management enrollment, heartbeat lease,
// Core NATS dispatch subscription and ack correlation. It accepts every
// dispatched command and replies SUCCEEDED with an echo response.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/adapter"
)

// version is overridden at build time via -ldflags.
var version = "noop-dev"

func main() {
	configPath := flag.String("c", "", "adapter configuration file")
	typ := flag.String("type", "coap", "module type to enroll as")
	ingressID := flag.String("ingress", "", "module ingress id")
	flag.Parse()

	if *configPath == "" || *ingressID == "" {
		slog.Error("usage: noop -c <config> --ingress <ingress-id>")
		return
	}

	cfg, err := adapter.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		return
	}
	heartbeat, err := cfg.Heartbeat()
	if err != nil {
		slog.Error("invalid config", "error", err)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	nc, err := nats.Connect(cfg.Core.NATSURL,
		nats.UserInfo(cfg.Core.NATSUser, cfg.Core.NATSPassword))
	if err != nil {
		slog.Error("connect nats failed", "error", err)
		return
	}
	defer nc.Close()

	enrolled, err := adapter.Enroll(ctx, cfg.Core.ManagementURL,
		cfg.Module.EnrollmentToken, *typ, *ingressID, version)
	if err != nil {
		slog.Error("enrollment failed", "error", err)
		return
	}
	slog.Info("module enrolled", "id", enrolled.ID, "ingress", enrolled.IngressID)

	dispatcher := adapter.NewDispatcher(nc, handleCommand)
	sub, err := dispatcher.Subscribe(enrolled.IngressID)
	if err != nil {
		slog.Error("subscribe dispatch failed", "error", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return adapter.RunHeartbeats(gctx, cfg.Core.ManagementURL,
			enrolled.ID, cfg.Module.EnrollmentToken, heartbeat)
	})
	if err := g.Wait(); err != nil {
		slog.Error("adapter stopped", "error", err)
	}
}

// handleCommand echoes every command as a SUCCEEDED response.
func handleCommand(_ context.Context, cmd *v1.Command) (*v1.CommandAck, error) {
	response, err := json.Marshal(map[string]any{
		"ok":      true,
		"command": cmd.Name,
		"params":  json.RawMessage(cmd.Params),
	})
	if err != nil {
		return nil, err
	}
	return adapter.Succeeded(cmd.Id, response), nil
}
