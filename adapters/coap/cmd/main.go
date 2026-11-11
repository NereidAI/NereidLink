// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command adapter-coap runs the CoAP southbound module. It enrolls with
// the platform over REST, maintains presence and a node lease over NATS,
// serves CoAP requests over UDP and publishes telemetry through the
// shared JetStream uplink.

package main

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	coapmod "github.com/nereidai/nereidlink/adapters/coap"
	"github.com/nereidai/nereidlink/sdk/adapter"
	"github.com/nereidai/nereidlink/sdk/profile"
	"github.com/nereidai/nereidlink/sdk/registry"
	"github.com/nereidai/nereidlink/sdk/scriptruntime"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// version is overridden at build time via -ldflags.
var version = "coap-dev"

// Tuning defaults for the module.
const (
	listen              = ":5683"
	longPoll            = 25 * time.Second
	sessionTTL          = 90 * time.Second
	sweepInterval       = 30 * time.Second
	mailboxSize         = 16
	maxBytes      int64 = 64 * 1024
	nodeHB              = 5 * time.Second
	nodeLease           = 15 * time.Second
	profileTTL          = time.Minute
	profileCache        = 4096
)

func main() {
	configPath := flag.String("c", "", "adapter configuration file")
	flag.Parse()
	if *configPath == "" {
		slog.Error("usage: adapter-coap -c <config>")
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

	ctx, stop := signalNotify()
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
	if err := ensureStreams(ctx, js); err != nil {
		slog.Error("ensure streams failed", "error", err)
		return
	}

	registryClient, err := registry.NewClient(cfg.Core.RegistryGRPC, cfg.Core.RegistryToken)
	if err != nil {
		slog.Error("dial registry failed", "error", err)
		return
	}
	defer func() { _ = registryClient.Close() }()

	resolver := buildResolver(ctx, cfg)

	server := coapmod.NewServer(coapmod.Config{
		Listen:        listen,
		LongPoll:      longPoll,
		SessionTTL:    sessionTTL,
		SweepInterval: sweepInterval,
		MailboxSize:   mailboxSize,
		MaxBytes:      maxBytes,
	}, registryClient, makePublisher(js, resolver))

	presence := adapter.NewPresenceClient(nc, js, "", adapter.PresenceTuning{
		HeartbeatInterval: nodeHB,
		LeaseTTL:          nodeLease,
	})
	relay := adapter.NewMailboxRelay(nc, server.Sessions())
	server.SetRelay(relay)
	server.SetPresence(presence)

	enrolled, err := adapter.Enroll(ctx, cfg.Core.ManagementURL,
		cfg.Module.EnrollmentToken, "coap", "", version)
	if err != nil {
		slog.Error("enrollment failed", "error", err)
		return
	}
	slog.Info("module enrolled", "id", enrolled.ID, "ingress", enrolled.IngressID)
	presence.SetIngressID(enrolled.IngressID)

	sub, err := relay.Subscribe(enrolled.IngressID)
	if err != nil {
		slog.Error("subscribe dispatch failed", "error", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return presence.Run(gctx) })
	g.Go(func() error { return server.Run(gctx) })
	g.Go(func() error {
		return runSessionSweeper(gctx, server, presence)
	})
	g.Go(func() error {
		return adapter.RunHeartbeats(gctx, cfg.Core.ManagementURL,
			enrolled.ID, cfg.Module.EnrollmentToken, heartbeat)
	})
	if err := g.Wait(); err != nil {
		slog.Error("adapter stopped", "error", err)
	}
}

// signalNotify returns a context canceled on SIGINT/SIGTERM.
func signalNotify() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// ensureStreams creates the core telemetry streams so a module can publish
// even when started before the main platform.
func ensureStreams(ctx context.Context, js jetstream.JetStream) error {
	return sdktelemetry.EnsureStreams(ctx, js)
}

// buildResolver optionally connects to PostgreSQL for descriptor resolution.
func buildResolver(ctx context.Context, cfg *adapter.Config) sdktelemetry.DescriptorResolver {
	if cfg.Core.PostgresDSN == "" {
		return nil
	}
	pool, err := pgxpool.New(ctx, cfg.Core.PostgresDSN)
	if err != nil {
		slog.Warn("connect postgres failed, descriptors disabled", "error", err)
		return nil
	}
	return profile.NewResolver(pool, profileTTL, profileCache,
		scriptruntime.New(time.Second))
}

// makePublisher returns the telemetry publisher bound to the shared uplink.
func makePublisher(js jetstream.JetStream,
	resolver sdktelemetry.DescriptorResolver) coapmod.TelemetryPublisher {
	return func(ctx context.Context, id adapter.SessionIdentity,
		frame []byte, now time.Time) (int, error) {
		return sdktelemetry.Publish(ctx, js, resolver,
			sdktelemetry.Identity{TenantID: id.TenantID, DeviceID: id.DeviceID},
			sdktelemetry.ProtocolCoAP, frame, now)
	}
}

// runSessionSweeper expires idle sessions and disconnects their presence.
func runSessionSweeper(ctx context.Context, server *coapmod.Server, presence *adapter.PresenceClient) error {
	return server.Sessions().Run(ctx, sweepInterval, sessionTTL,
		func(ctx context.Context, swept adapter.SweptSession) error {
			return presence.Disconnect(ctx, swept.Identity, swept.SessionID)
		})
}
