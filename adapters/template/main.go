// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command template is the minimal annotated adapter skeleton. Copy it
// into a new repository as the starting point for a custom southbound
// protocol. Search for "PROTOCOL HOOK" to find the points where wire
// protocol code plugs in.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/adapter"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// version identifies the adapter build; override it with -ldflags at
// release time.
var version = "template-dev"

// moduleType identifies the protocol family to the core. Every protocol
// gets its own stable string.
const moduleType = "myproto"

func main() {
	configPath := flag.String("c", "", "adapter configuration file")
	ingressID := flag.String("ingress", "", "provisioned ingress id")
	flag.Parse()
	if *configPath == "" || *ingressID == "" {
		slog.Error("usage: myadapter -c <config> --ingress <ingress-id>")
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

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("create jetstream failed", "error", err)
		return
	}
	if err := sdktelemetry.EnsureStreams(ctx, js); err != nil {
		slog.Error("ensure streams failed", "error", err)
		return
	}

	enrolled, err := adapter.Enroll(ctx, cfg.Core.ManagementURL,
		cfg.Module.EnrollmentToken, moduleType, *ingressID, version)
	if err != nil {
		slog.Error("enrollment failed", "error", err)
		return
	}
	slog.Info("module enrolled", "id", enrolled.ID,
		"ingress", enrolled.IngressID)

	// Presence maintains the node lease; device Connect/Disconnect calls
	// are made from protocol session events (see publishTelemetry).
	presence := adapter.NewPresenceClient(nc, js, enrolled.IngressID,
		adapter.PresenceTuning{
			HeartbeatInterval: heartbeat,
			LeaseTTL:          heartbeat * 3,
		})

	dispatcher := adapter.NewDispatcher(nc, handleCommand)
	sub, err := dispatcher.Subscribe(enrolled.IngressID)
	if err != nil {
		slog.Error("subscribe dispatch failed", "error", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return presence.Run(gctx) })
	g.Go(func() error {
		return adapter.RunHeartbeats(gctx, cfg.Core.ManagementURL,
			enrolled.ID, cfg.Module.EnrollmentToken, heartbeat)
	})
	// PROTOCOL HOOK: start the wire protocol listener here and translate
	// inbound frames into publishTelemetry calls.
	if err := g.Wait(); err != nil {
		slog.Error("adapter stopped", "error", err)
	}
}

// handleCommand translates one platform command into a protocol write
// and returns the ack. Replace the body with a real protocol operation;
// the template simply succeeds with an echo response.
func handleCommand(_ context.Context, cmd *v1.Command) (*v1.CommandAck, error) {
	response, err := json.Marshal(map[string]any{
		"ok":      true,
		"command": cmd.Name,
	})
	if err != nil {
		return nil, err
	}
	return adapter.Succeeded(cmd.Id, response), nil
}

// publishTelemetry is the exact call an adapter makes whenever a device
// frame is decoded. Frames are JSON objects mapping measurement keys to
// numbers, booleans or strings. Pass nil as resolver to carry values
// verbatim; pass a profile resolver to apply units and transforms.
//
//nolint:unused // template example called from the protocol hook only after a developer adds one
func publishTelemetry(ctx context.Context, js jetstream.JetStream,
	tenantID, deviceID string, frame []byte) error {
	_, err := sdktelemetry.Publish(ctx, js, nil,
		sdktelemetry.Identity{TenantID: tenantID, DeviceID: deviceID},
		moduleType, frame, time.Now().UTC())
	return err
}
