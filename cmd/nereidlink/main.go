// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command nereidlink is the single entry point for every platform role.
//
// Subcommands:
//
//   - migrate: apply embedded SQL migrations explicitly.
//   - seed: write deterministic development data.
//   - serve: run the all-in-one composition with every role enabled.
//
// The same binary is used for Kubernetes role selection through future
// role flags, preserving one codebase for both deployment forms.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/accounting"
	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/blob"
	"github.com/nereidai/nereidlink/internal/buscodec"
	"github.com/nereidai/nereidlink/internal/business"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/companion"
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/connectorcatalog"
	"github.com/nereidai/nereidlink/internal/credcache"
	"github.com/nereidai/nereidlink/internal/dashboard"
	"github.com/nereidai/nereidlink/internal/dashboards"
	"github.com/nereidai/nereidlink/internal/deadman"
	"github.com/nereidai/nereidlink/internal/deviceevent"
	"github.com/nereidai/nereidlink/internal/discovery"
	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/edgeprofile"
	"github.com/nereidai/nereidlink/internal/egress"
	"github.com/nereidai/nereidlink/internal/escalation"
	"github.com/nereidai/nereidlink/internal/firmware"
	"github.com/nereidai/nereidlink/internal/floodguard"
	"github.com/nereidai/nereidlink/internal/gatewayapi"
	"github.com/nereidai/nereidlink/internal/horizon"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/ingesthttp"
	"github.com/nereidai/nereidlink/internal/ingestlimit"
	"github.com/nereidai/nereidlink/internal/ingestmqtt"
	"github.com/nereidai/nereidlink/internal/leaderelection"
	"github.com/nereidai/nereidlink/internal/license"
	"github.com/nereidai/nereidlink/internal/linkhealth"
	"github.com/nereidai/nereidlink/internal/linknotify"
	"github.com/nereidai/nereidlink/internal/machineauth"
	"github.com/nereidai/nereidlink/internal/manifest"
	"github.com/nereidai/nereidlink/internal/modelinventory"
	"github.com/nereidai/nereidlink/internal/module"
	"github.com/nereidai/nereidlink/internal/notify"
	"github.com/nereidai/nereidlink/internal/oidc"
	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/opinvoke"
	"github.com/nereidai/nereidlink/internal/opscenter"
	"github.com/nereidai/nereidlink/internal/ota"
	"github.com/nereidai/nereidlink/internal/pki"
	"github.com/nereidai/nereidlink/internal/presence"
	"github.com/nereidai/nereidlink/internal/probe"
	"github.com/nereidai/nereidlink/internal/profilepush"
	"github.com/nereidai/nereidlink/internal/prometrics"
	"github.com/nereidai/nereidlink/internal/protocolcatalog"
	"github.com/nereidai/nereidlink/internal/provision"
	"github.com/nereidai/nereidlink/internal/quota"
	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/registry"
	"github.com/nereidai/nereidlink/internal/retention"
	"github.com/nereidai/nereidlink/internal/rule"
	"github.com/nereidai/nereidlink/internal/secretbinding"
	"github.com/nereidai/nereidlink/internal/secretcatalog"
	"github.com/nereidai/nereidlink/internal/secretdelete"
	"github.com/nereidai/nereidlink/internal/secrets"
	"github.com/nereidai/nereidlink/internal/shadow"
	"github.com/nereidai/nereidlink/internal/silence"
	"github.com/nereidai/nereidlink/internal/sso"
	"github.com/nereidai/nereidlink/internal/stats"
	"github.com/nereidai/nereidlink/internal/stepup"
	"github.com/nereidai/nereidlink/internal/storagehook"
	"github.com/nereidai/nereidlink/internal/store"
	"github.com/nereidai/nereidlink/internal/streams"
	"github.com/nereidai/nereidlink/internal/telemetry"
	"github.com/nereidai/nereidlink/internal/userapikey"
	"github.com/nereidai/nereidlink/internal/usermgmt"
	"github.com/nereidai/nereidlink/sdk/profile"
	sdkregistry "github.com/nereidai/nereidlink/sdk/registry"
	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// blobBackend is the union of blob capabilities consumed by the wiring:
// the gateway REST surface plus ranged reads for the device HTTP relay.
// *blob.Store and *blob.localStore both satisfy it.
type blobBackend interface {
	gatewayapi.BlobStore
	GetObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// configPath is the global configuration file selected by -c.
var configPath string

// version is overridden at build time via -ldflags. The fallback is the
// rolling dev channel version; release builds overwrite it with the bare
// SemVer derived from the git tag (v0.0.1 -> 0.0.1).
var version = "0.0.0-dev"

func main() {
	root := &cobra.Command{Use: "nereidlink"}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "configuration file path")

	root.AddCommand(migrateCommand(), seedCommand(), serveCommand(),
		simulateCommand(), licenseCommand(), opsCommand(), pkiCommand(),
		doctorCommand(), backupCommand(), reconcileCommandCatalogCommand(),
		templatesCommand())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// migrateCommand returns the explicit schema migration command.
func migrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate up",
		Short: "Apply embedded PostgreSQL migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			return store.Migrate(cfg.Postgres.DSN)
		},
	}
}

// seedCommand returns the deterministic development seed command.
func seedCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Write deterministic development data",
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
			return store.Seed(ctx, pool, store.SeedParams{
				TenantID:    cfg.Seed.TenantID,
				DeviceID:    cfg.Seed.DeviceID,
				DeviceToken: cfg.Seed.DeviceToken,
			})
		},
	}
}

// serveCommand runs the all-in-one composition.
func serveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run all platform roles",
		RunE:  runServe,
	}
}

// requireConfig loads the file supplied through the global -c flag.
func requireConfig() (*config.Config, error) {
	if configPath == "" {
		return nil, fmt.Errorf("configuration file is required, use -c")
	}
	return config.Load(configPath)
}

// runServe wires the full dependency graph and runs every role until an
// interrupt is received or a mandatory role fails.
func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := requireConfig()
	if err != nil {
		return err
	}
	applyPodIdentity(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startedAt := time.Now()

	natsTLS, err := cfg.NATS.TLS.TLSConfig()
	if err != nil {
		return fmt.Errorf("nats tls: %w", err)
	}
	var natsOptions []nats.Option
	if natsTLS != nil {
		natsOptions = append(natsOptions, nats.Secure(natsTLS))
	}
	nc, err := nats.Connect(cfg.NATS.URL, natsOptions...)
	if err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("create jetstream: %w", err)
	}
	if err := streams.EnsureTelemetryStreams(ctx, js, cfg.NATS.JetStreamReplicas); err != nil {
		return err
	}

	pool, err := store.Connect(ctx, cfg.Postgres.DSN)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	licensePublicKey, err := license.LoadPublicKey(cfg.License.PublicKeyPath)
	if err != nil {
		return err
	}
	licenseService := license.New(pool, nc, js, license.Config{
		PublicKey:   licensePublicKey,
		GracePeriod: durationOrDefault(cfg.License.GracePeriod, 7*24*time.Hour),
		ClockFile:   cfg.License.ClockFile,
		JSReplicas:  cfg.NATS.JetStreamReplicas,
	})
	shadowService := shadow.New(pool, nc)
	registryAuthServer := registry.NewServer(pool)
	registryAuthServer.SetConnectionPolicy(licenseService)
	jwksResolver := registry.NewJWKSResolver()
	registryAuthServer.SetKeyResolver(jwksResolver)
	registryServerTLS, err := cfg.Registry.TLS.ServerTLSConfig()
	if err != nil {
		return fmt.Errorf("registry server tls: %w", err)
	}
	grpcServer, err := startRegistry(cfg.Registry.Listen, cfg.Registry.ModuleToken,
		registryAuthServer, registryServerTLS)
	if err != nil {
		return err
	}
	defer grpcServer.GracefulStop()

	registryClientTLS, err := cfg.Registry.TLS.TLSConfig()
	if err != nil {
		return fmt.Errorf("registry client tls: %w", err)
	}
	registryClient, err := sdkregistry.NewClientWithTLS(
		cfg.Registry.Listen, cfg.Registry.ModuleToken, registryClientTLS)
	if err != nil {
		return fmt.Errorf("dial registry: %w", err)
	}
	defer func() { _ = registryClient.Close() }()

	// Optional L1/L2 credential cache on the device authentication hot
	// path (ADR-0014). It is disabled by default: all-in-one and small
	// deployments authenticate through the gRPC client exactly as before.
	var (
		deviceAuthenticator any = registryClient
		cacheMetrics        *credcache.Metrics
	)
	if cfg.CredentialCache.Enabled {
		l1, err := credcache.NewL1(credcache.L1Config{
			MaxCost:     cfg.CredentialCache.MaxCost,
			NumCounters: cfg.CredentialCache.NumCounters,
		})
		if err != nil {
			return fmt.Errorf("create credential l1 cache: %w", err)
		}
		var l2 *credcache.L2Cache
		if !cfg.CredentialCache.L2.Disabled {
			jsc, err := nc.JetStream()
			if err != nil {
				return fmt.Errorf("jetstream context for credential cache: %w", err)
			}
			l2, err = credcache.NewL2(jsc, cfg.CredentialCache.L2.Bucket,
				durationOrDefault(cfg.CredentialCache.PositiveTTL, 0),
				cfg.NATS.JetStreamReplicas)
			if err != nil {
				return fmt.Errorf("create credential l2 cache: %w", err)
			}
		}
		cacheMetrics = credcache.NewMetrics(nil)
		cachedAuth := credcache.NewCachedAuthenticator(registryClient, l1, l2,
			credcache.AuthenticatorConfig{
				PositiveTTL: durationOrDefault(cfg.CredentialCache.PositiveTTL, 0),
				NegativeTTL: durationOrDefault(cfg.CredentialCache.NegativeTTL, 0),
				Resolver:    jwksResolver,
			}, cacheMetrics)
		if err := cachedAuth.StartInvalidationListener(ctx, nc); err != nil {
			return fmt.Errorf("start credential cache invalidation listener: %w", err)
		}
		deviceAuthenticator = cachedAuth
	}
	credentialAuthenticator := deviceAuthenticator.(ingestmqtt.Authenticator)

	presenceTune, err := presenceTuning(cfg)
	if err != nil {
		return err
	}
	presenceService := presence.NewService(nc, js, pool, cfg.Presence.IngressID, presenceTune)
	scriptRunner := scriptruntime.New(mustDuration(cfg.Rules.ScriptTimeout))
	profileResolver := profile.NewResolver(
		pool, profileCacheTTL, profileCacheSize, scriptRunner)

	pkiManager, err := pki.NewManager(pool, filepath.Dir(cfg.IngestMQTT.TLSClientCAFile))
	if err != nil {
		return fmt.Errorf("create pki manager: %w", err)
	}
	if err := pkiManager.SyncBuiltinCA(ctx, cfg.IngestMQTT.TLSClientCAFile); err != nil {
		return fmt.Errorf("sync built-in CA: %w", err)
	}

	// Per-tenant secret vault for ADR-0026 secret:// connector credential
	// delivery (migration 0047). The AES-256 KEK lives alongside the PKI
	// material, never in the database; the store seals values and the MQTT
	// relay resolves references only at the downlink last mile.
	secretCodec, err := secrets.LoadCodec(filepath.Dir(cfg.IngestMQTT.TLSClientCAFile))
	if err != nil {
		return fmt.Errorf("create secret codec: %w", err)
	}
	secretStore := secrets.NewStore(pool, secretCodec)

	var mqttTLSConfig *tls.Config
	if cfg.IngestMQTT.TLSListen != "" {
		mqttTLSConfig, err = pkiManager.ServerTLSConfigDynamic(
			cfg.IngestMQTT.TLSCertFile,
			cfg.IngestMQTT.TLSKeyFile,
			cfg.IngestMQTT.TLSRequireClientCert)
		if err != nil {
			return fmt.Errorf("load mqtt tls config: %w", err)
		}
	}

	mqttServer, err := ingestmqtt.NewServer(
		trimAddress(cfg.IngestMQTT.Listen), trimAddress(cfg.IngestMQTT.TLSListen),
		cfg.Presence.IngressID, mqttTLSConfig, nc,
		credentialAuthenticator, presenceService, js, profileResolver, shadowService)
	if err != nil {
		return fmt.Errorf("create mqtt ingress: %w", err)
	}
	// CRL revocation for mTLS device certificates (ADR-0009); results are
	// short-TTL cached in-process and an unreachable CRL denies (fail
	// closed) rather than silently admitting a possibly revoked device.
	mqttServer.SetRevocationChecker(registry.NewRevocationChecker())

	httpTune := ingesthttp.Tuning{
		LongPollWait:      mustDuration(cfg.IngestHTTP.LongPollWait),
		SessionTTL:        mustDuration(cfg.IngestHTTP.SessionTTL),
		SweepInterval:     mustDuration(cfg.IngestHTTP.SweepInterval),
		MailboxSize:       cfg.IngestHTTP.MailboxSize,
		MaxTelemetryBytes: cfg.IngestHTTP.MaxTelemetryBytes,
	}
	httpServer := ingesthttp.NewServer(
		cfg.IngestHTTP.IngressID, credentialAuthenticator, presenceService, js,
		profileResolver, httpTune)
	httpRelay := ingesthttp.NewRelay(nc, httpServer)
	httpServer.SetRelay(httpRelay)
	httpServer.SetShadowReporter(shadowService)
	if err := httpRelay.Subscribe(cfg.IngestHTTP.IngressID); err != nil {
		return fmt.Errorf("subscribe http command dispatch: %w", err)
	}

	tune, err := telemetryTuning(cfg)
	if err != nil {
		return err
	}
	hookStore := storagehook.NewStore(pool)
	hookPipeline := storagehook.NewPipeline(hookStore, scriptRunner)
	hookStore.SetWasmOptions(wasmHookOptions(cfg))
	if wasmHooksEnabled(cfg) {
		wasmHooks, err := storagehook.NewWasmRuntime(ctx, wasmHookOptions(cfg))
		if err != nil {
			slog.Warn("wasm hook runtime disabled; wasm hooks will fail by policy", "error", err)
		} else {
			hookPipeline.SetWasm(wasmHooks)
			hookStore.SetWasmValidator(wasmHooks)
			go func() {
				<-ctx.Done()
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = wasmHooks.Close(closeCtx)
			}()
		}
	}
	telemetryServer := telemetry.NewServer(js, pool, tune, hookPipeline, hookStore)
	telemetryServer.SetTenantWriteGate(licenseService)

	hub := gatewayapi.NewHub(nc, gatewayapi.Tuning{
		ReadLimit:    cfg.Server.WSReadLimit,
		PingInterval: mustDuration(cfg.Server.WSPingInterval),
	})
	// In cluster mode the login captcha must verify on any replica: the
	// in-process default would reject every challenge issued by a peer
	// (CAPTCHA_INVALID). Share it through NATS KV like the OIDC state.
	if cfg.Cluster.Enabled {
		kvCtx, kvCancel := context.WithTimeout(context.Background(), 15*time.Second)
		captchaStore, err := gatewayapi.NewKVCaptchaStore(kvCtx, js,
			cfg.NATS.JetStreamReplicas, 10*time.Minute)
		kvCancel()
		if err != nil {
			return fmt.Errorf("create captcha store: %w", err)
		}
		hub.SetCaptchaStore(captchaStore)
	}
	registryAdmin := registry.NewAdmin(pool)
	registryAdmin.SetPresenceEvictor(presenceService)
	registryAdmin.SetDeviceDisconnector(mqttServer)
	attrPublisher := &natsAttributePublisher{nc: nc}
	registryAdmin.SetAttributePublisher(attrPublisher)
	registryAdmin.SetAttributeEventPublisher(attrPublisher)
	mqttServer.SetGatewayManager(registryAdmin)
	edgeCfg := edge.DefaultConfig()
	edgeCfg.ServerVersion = "nereidlink-" + version
	// Device-profile downlink formats the server can render (ADR-0038). The
	// neutral format is always offered; EdgeX is a cloud-side adapter.
	edgeCfg.SupportedProfileFormats = profilepush.SupportedFormats()
	mqttServer.SetEdgeNegotiator(edge.NewNegotiator(edgeCfg))

	// Gateway manifest reconciliation (ADR-0026): event-driven desired vs
	// reported sub-device reconciliation with lease-watch/sweep backstop.
	manifestReconciler := manifest.New(js, nc, pool, registryAdmin,
		presenceService, manifestTuning(cfg), slog.Default())
	mqttServer.SetManifestReconciler(manifestReconciler)

	// Gateway auto-provisioning (ADR-0026/ADR-0013): a gateway reports local
	// devices on v1/gateway/discovery; when enabled the discovery service
	// creates them bound to the gateway and returns the local_id -> UUID
	// mapping. The feature is disabled by policy unless opted in.
	discoveryService := discovery.New(registryAdmin, nc, discoveryPolicy(cfg), slog.Default())
	discoveryService.SetPolicyAdmin(discovery.NewPolicyStore(pool))
	discoveryService.SetCandidateStore(discovery.NewCandidateStore(pool))
	discoveryService.SetLocationResolver(discoveryPresenceLocator{presence: presenceService})
	mqttServer.SetDiscoveryReceiver(discoveryService)

	// EdgeX profile uplink: a gateway reports its connector device profiles on
	// v1/gateway/profiles; NL caches the raw profiles per gateway and lets the
	// console adopt them as native device profiles on demand.
	edgeProfileService := edgeprofile.NewService(edgeprofile.NewStore(pool), registryAdmin)
	edgeProfileService.SetDriftSink(edgeprofile.NewNatsDriftSink(nc))
	mqttServer.SetEdgeProfileReceiver(edgeProfileService)

	// Gateway application watchdog (ADR-0026): heartbeats, SUSPECT/DOWN
	// leases, planned-goodbye suppression and flapping advisories. It is
	// kept off the connection plane and emits Core NATS advisories only.
	linkMonitor := linkhealth.NewMonitor(nc, linkHealthTuning(cfg), slog.Default())
	linkMonitor.SetCascade(presenceService)
	mqttServer.SetLinkHealthObserver(linkMonitor)
	hub.SetRegistryAdmin(registryAdmin)
	hub.SetOwnershipAdmin(registryAdmin)
	hub.SetOverviewReader(dashboard.NewService(pool, presenceService))

	statsService := stats.NewService(pool, js, nc, startedAt)
	statsService.SetBroker(mqttServer)
	statsService.SetHTTPIngest(httpServer)
	statsService.SetOnline(presenceService)
	hub.SetStatsReader(statsService)

	// Capacity watermark queues (ADR-0025): presence is constructed already;
	// the notify service is constructed below, so the closure dereferences it
	// lazily at scrape time.
	var notifyServicePtr *notify.Service
	queueWatermarks := func() []stats.QueueWatermark {
		cur, limit := presenceService.PersistQueueWatermark()
		wm := []stats.QueueWatermark{{Plane: "presence_persist", Current: cur, Limit: limit}}
		if notifyServicePtr != nil {
			critical, normal := notifyServicePtr.QueueWatermarks()
			wm = append(wm,
				stats.QueueWatermark{Plane: "notify_queue", Sub: "critical", Current: critical[0], Limit: critical[1]},
				stats.QueueWatermark{Plane: "notify_queue", Sub: "normal", Current: normal[0], Limit: normal[1]},
			)
		}
		return wm
	}

	metrics, err := prometrics.New(statsService, startedAt, hub.ClientCount, queueWatermarks)
	if err != nil {
		return fmt.Errorf("create metrics: %w", err)
	}
	hub.SetMetricsHandler(metrics.Handler(), metrics.Instrument)
	if cacheMetrics != nil {
		if err := metrics.Registry().Register(cacheMetrics); err != nil {
			return fmt.Errorf("register credential cache metrics: %w", err)
		}
	}

	busCodec, err := buscodec.New(buscodec.Config{
		Level:    cfg.Bus.Compression.Level,
		MinBytes: cfg.Bus.Compression.MinBytes,
	})
	if err != nil {
		return fmt.Errorf("create bus codec: %w", err)
	}
	if err := busCodec.RegisterMetrics(metrics.Registry()); err != nil {
		return fmt.Errorf("register bus codec metrics: %w", err)
	}
	mqttServer.SetEncoder(busCodec)
	httpServer.SetEncoder(busCodec)
	telemetryServer.SetFrameDecoder(busCodec)
	// The WS hub and the rule processor are independent Core NATS / JetStream
	// consumers of telemetry envelopes; wire the same codec so compressed
	// frames reach real-time fanout and chains, not only the DB writer.
	hub.SetFrameDecoder(busCodec)

	deviceLimiter := ingestlimit.New(ingestlimit.Config{
		Rate:  cfg.IngestLimit.PerDeviceRate,
		Burst: cfg.IngestLimit.PerDeviceBurst,
	})
	limitRejections := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "nereidlink",
		Name:      "ingest_limit_rejections_total",
		Help:      "Total telemetry frames rejected by the per-device token bucket.",
	})
	deviceLimiter.SetMetrics(limitRejections)
	if err := metrics.Registry().Register(limitRejections); err != nil {
		return fmt.Errorf("register ingest limit metric: %w", err)
	}
	mqttServer.SetDeviceLimit(deviceLimiter)
	httpServer.SetDeviceLimit(deviceLimiter)

	// Resource accounting (ADR-0025): a bounded 1/N sample of the ingest
	// hot path per dimension. The sampler is optional; when accounting is
	// disabled neither ingress is wired and the hot path is untouched.
	var accountingSampler *accounting.Sampler
	var accountingReporter *accounting.Reporter
	var accountingAggregator *accounting.Aggregator
	if cfg.Accounting.Enabled {
		accountingNodeID := cfg.Accounting.NodeID
		if accountingNodeID == "" {
			// Fall back to the node identity the rest of the topology
			// already uses, so accounting and presence agree on which
			// node a report came from.
			accountingNodeID = cfg.Presence.IngressID
		}
		accountingSampler = accounting.NewSampler(accountingSamplerConfig(cfg, accountingNodeID))
		accountingReporter = accounting.NewReporter(nc, accountingSampler, slog.Default()).
			WithSchedule(mustDuration(cfg.Accounting.ReportInterval), cfg.Accounting.TopN)
		accountingStore := accounting.NewStore(pool)
		accountingAggregator = accounting.NewAggregator(nc, accountingStore, slog.Default())
		mqttServer.SetAccounting(accountingSampler)
		httpServer.SetAccounting(accountingSampler)
		hub.SetAccounting(accountingStore)
		slog.Info("resource accounting enabled",
			"node", accountingNodeID,
			"sample_rate", cfg.Accounting.SampleRate,
			"window", cfg.Accounting.ReportInterval)
	}

	hub.SetHookAdmin(hookStore)
	hub.SetDeviceIngest(httpServer.Handler())
	hub.SetTelemetryHistory(telemetryServer)
	hub.SetTelemetryKeys(telemetryServer)
	hub.SetPresenceLister(presenceService)
	hub.SetTelemetrySeries(telemetryServer)

	notificationInbox := notify.NewInbox(pool, nc)
	hub.SetNotificationInbox(notificationInbox)

	commandTune, err := commandTuning(cfg)
	if err != nil {
		return err
	}
	commandStore := command.NewStore(pool)
	hub.SetCommandHistory(commandStore)
	commandRouter := command.NewRouter(nc, commandStore, presenceService, commandTune)
	commandMetrics, err := command.NewMetrics(metrics.Registry())
	if err != nil {
		return err
	}
	commandRouter.SetMetrics(commandMetrics)
	commandBackground := command.NewService(nc, commandRouter, commandStore, commandTune)
	commandAPI := command.NewAPI(commandRouter, commandStore,
		mustDuration(cfg.Command.DefaultTimeout), cfg.Command.MaxBodyBytes)
	hub.SetCommands(commandAPI)
	hub.SetCommandCatalog(command.NewCatalogAdmin(commandStore))
	registryAdmin.SetCommandMaterializer(commandStore)
	batchStore := command.NewBatchStore(pool)
	batchManager := command.NewBatchManager(batchStore, commandAPI, nc,
		cfg.Command.MaxBodyBytes, cfg.Command.MaxBatchTargets)
	hub.SetBatchCommands(batchManager)

	// On-demand gateway probes (ADR-0026 section 9): async tasks grouped per
	// gateway, dispatched over MQTT, correlated from per-device responses and
	// retained 24h. Admission limits are in-process; the locator is the shared
	// presence service so an offline gateway fails its devices immediately.
	probeStore := probe.NewStore(pool)
	probeManager := probe.NewManager(probeStore, nc, presenceService,
		probeTuning(cfg), slog.Default())
	hub.SetProbes(probeManager)

	// Firmware OTA tasks (ADR-0026): one firmware descriptor dispatched per
	// gateway, per-device progress correlated from v1/gateway/ota/progress.
	// The edge downloads the package from the supplied URL and enforces
	// sha256; NL never stores the blob in the database — bytes live in
	// either an S3-compatible store or a local directory for single-node
	// all-in-one deployments (ADR-0006).
	var blobStore blobBackend
	switch cfg.Blob.Backend() {
	case config.BlobProviderS3:
		blobStore, err = blob.New(ctx, blob.Config{
			Endpoint:         cfg.Blob.Endpoint,
			PublicEndpoint:   cfg.Blob.PublicEndpoint,
			Region:           cfg.Blob.Region,
			AccessKey:        cfg.Blob.AccessKey,
			SecretKey:        cfg.Blob.SecretKey,
			Bucket:           cfg.Blob.Bucket,
			PresignTTLString: cfg.Blob.PresignTTL,
			Secure:           cfg.Blob.Secure,
			PublicSecure:     cfg.Blob.PublicSecure,
		})
		if err != nil {
			return fmt.Errorf("init blob store: %w", err)
		}
		slog.Info("blob store enabled (s3)", "endpoint", cfg.Blob.Endpoint,
			"bucket", cfg.Blob.Bucket, "public_endpoint", cfg.Blob.PublicEndpoint)
	case config.BlobProviderLocal:
		blobStore, err = blob.NewLocal(ctx, blob.LocalConfig{
			Directory:  cfg.Blob.LocalDirectory(),
			SigningKey: cfg.Identity.JWTSecret,
			SignTTL:    durationOrDefault(cfg.Blob.PresignTTL, 15*time.Minute),
		})
		if err != nil {
			return fmt.Errorf("init local blob store: %w", err)
		}
		slog.Info("blob store enabled (local)", "directory", cfg.Blob.LocalDirectory())
	default:
		slog.Info("blob store disabled: platform-hosted binary features unavailable")
	}

	otaStore := ota.NewStore(pool)
	// firmwareService is set below when object storage is enabled; the OTA
	// manager accepts a nil resolver, in which case only external-URL tasks
	// work.
	var otaFirmwareResolver ota.FirmwareResolver
	otaManager := ota.NewManager(otaStore, nc, presenceService,
		otaFirmwareResolver, ota.DefaultTuning(), slog.Default())
	hub.SetOTA(otaManager)
	if blobStore != nil {
		hub.SetBlobStore(blobStore)
		hub.SetBlobSigningKey(cfg.Identity.JWTSecret)
		// Platform-hosted firmware repository (ADR-0006b): only available
		// when object storage is configured.
		firmwareService := firmware.NewService(firmware.NewStore(pool),
			blobStore, cfg.Blob.MaxUploadBytes)
		hub.SetFirmware(firmwareService)
		otaFirmwareResolver = firmwareResolver{svc: firmwareService}
		otaManager.SetFirmwareResolver(otaFirmwareResolver)
		// Expose the authenticated device relay (list/download/progress) on
		// the HTTPS device ingress for direct devices and gateways.
		httpServer.SetOTA(otaManager, blobStore)

		// Device media offload (ADR-0006 BlobRef): images attached to
		// frames land in blob storage while ts_telemetry keeps only a
		// blob:// reference. One policy instance serves HTTP and MQTT.
		mediaPolicy := blob.NewMediaStore(blobStore,
			cfg.Blob.MediaMaxBytes, cfg.Blob.MediaAllowedTypes)
		httpServer.SetMediaBlobs(blobStore, mediaPolicy, mediaPolicy.MaxBytes())
		mqttServer.SetMediaStore(mediaPolicy)
	}

	// Rule engine wiring: graph store, debug store and side effect
	// dependencies.
	ruleChainStore := rule.NewChainStore(pool)
	ruleDebug := rule.NewDebugStore(
		ruleDebugCaptures, ruleDebugTraces, ruleDebugTTL)
	ruleAttrStore := rule.NewAttrStore(pool)
	// Live console updates for device-reported and rule-produced attribute
	// changes (CLIENT scope and rule-node SERVER/SHARED writes).
	ruleAttrStore.SetAttributeEventPublisher(attrPublisher)
	// Device- and gateway-reported CLIENT-scope attribute uplinks persist
	// through the same attribute store the rule engine's Save Attributes
	// node uses (ADR-0011); devices never write SERVER/SHARED scope.
	mqttServer.SetAttributeSaver(ruleAttrStore)
	// On-demand device/gateway pulls of platform-managed attributes
	// (v1/.../attributes/get) read straight from the registry, which owns
	// the device_attribute table and the scope allowlist.
	mqttServer.SetAttributeFetcher(registryAdmin)

	// General device domain-event channel (v1/me/events): persist to the
	// append-only ledger, fan out over NATS to the WebSocket plane, and back
	// the console event query.
	deviceEventService := deviceevent.New(pool, nc)
	mqttServer.SetEventIngester(deviceEventService)
	hub.SetDeviceEventReader(deviceEventService)

	alarmService := alarm.NewService(pool, nc)
	registryAdmin.SetAlarmCloser(alarmService)

	// Flood protection (ADR-0014): turn per-device ingest rejections into a
	// device alarm that auto-clears after a calm recovery window. Disabled
	// unless a positive threshold is configured.
	var floodGuard *floodguard.Service
	if cfg.IngestLimit.FloodThreshold > 0 {
		floodGuard = floodguard.New(alarmService)
		deviceLimiter.SetFloodSink(ingestlimit.FloodDetector{
			Threshold: cfg.IngestLimit.FloodThreshold,
			Window:    durationOrDefault(cfg.IngestLimit.FloodWindow, 10*time.Second),
			Recover:   durationOrDefault(cfg.IngestLimit.FloodRecover, 2*time.Minute),
		}, floodGuard)
	}

	// Online-but-silent detection (ADR-0014): a periodic sweep reconciles
	// ONLINE_BUT_SILENT alarms from the last-telemetry watermark, the
	// descriptor expected period and live presence. No per-device timers.
	var silenceSweeper *silence.Sweeper
	if cfg.SilenceDetection.Enabled {
		silenceSweeper = silence.New(pool, presenceService, alarmService, slog.Default())
	}

	// Third-party data forwarding (ADR-0018): management service, producer
	// publisher used by rule forward nodes, and the in-process worker.
	egressStore := egress.NewStore(pool)
	egressService := egress.NewService(js, egressStore, cfg.NATS.JetStreamReplicas)
	egressPublisher := egress.NewPublisher(js, 0)
	egressWorker := egress.NewWorker(js, egressStore, egress.WorkerTuning{}, cfg.NATS.JetStreamReplicas)
	hub.SetForwarderAdmin(egressService)

	ruleDeps := rule.Deps{
		Script:     scriptRunner,
		Attributes: ruleAttrStore,
		Alarms:     &alarmAdapter{raiser: alarmService},
		Commands:   &commandAdapter{api: commandAPI},
		Egress:     &egressRuleAdapter{pub: egressPublisher},
	}
	ruleProcessor := rule.NewProcessor(js, ruleChainStore, ruleDeps, ruleDebug,
		rule.ProcessorTuning{
			MaxAckPending:  cfg.Rules.MaxAckPending,
			ReloadInterval: mustDuration(cfg.Rules.ReloadInterval),
		})
	ruleProcessor.SetFrameDecoder(busCodec)

	notifyService := notify.NewService(nc, notifyConfig(cfg), cfg.Notify.EmailRecipients, notificationInbox)
	notifyServicePtr = notifyService
	if err := notifyService.RegisterMetrics(metrics.Registry()); err != nil {
		return fmt.Errorf("register notify metrics: %w", err)
	}

	// Dead-man fallback (ADR-0019): an independent path that reports
	// platform self-check failures even if ordinary notification is broken.
	deadmanNotifier := deadman.NewNotifier(deadmanConfig(cfg))
	deadmanMonitor := deadman.NewMonitor(deadmanNotifier, slog.Default(),
		deadman.Probe{Name: "postgres", Check: func(pctx context.Context) error {
			return pool.Ping(pctx)
		}},
		deadman.Probe{Name: "nats", Check: func(context.Context) error {
			if !nc.IsConnected() {
				return fmt.Errorf("nats connection down")
			}
			return nil
		}},
	).WithSchedule(mustDuration(cfg.Deadman.Interval), cfg.Deadman.Threshold)
	if cfg.Deadman.Enabled {
		// Expose a synthetic delivery test on the loopback ops plane so
		// operators can confirm the fallback endpoint is reachable.
		hub.SetDeadmanProbe(deadmanNotifier)
	}

	hub.SetRuleTools(&ruleRESTAdapter{processor: ruleProcessor})
	hub.SetRuleChainAdmin(&chainAdminAdapter{
		store: ruleChainStore, processor: ruleProcessor,
	})
	hub.SetAlarmAdmin(&alarmRESTAdapter{service: alarmService})

	moduleStore := module.NewStore(pool)
	hub.SetModuleAdmin(moduleStore)
	commandRouter.SetModuleState(moduleStore)

	provisionTune := provisionTuning(cfg)
	quotaService := quota.NewService(pool)
	registryAdmin.SetQuotaGuard(quotaService)
	provisionService := provision.New(pool, js, provisionTune)
	provisionService.SetQuotaInitializer(quotaService)
	hub.SetQuotaAdmin(quotaService)
	hub.SetSubscriptionQuota(quotaService)

	retentionStore := retention.NewStore(pool)
	retentionEnforcer := retention.NewEnforcer(pool, retentionStore)
	hub.SetRetentionStore(retentionStore)
	hub.SetDiscoveryPolicyAdmin(discoveryService)
	hub.SetDiscoveryApprovalAdmin(discoveryService)
	hub.SetSecretStore(secretStore)
	hub.SetProvisionAdmin(&provisionAdminAdapter{service: provisionService})

	identityTune := identity.DefaultTuning()
	identityTune.AccessTTL = durationOrDefault(cfg.Identity.AccessTTL, identityTune.AccessTTL)
	identityTune.RefreshTTL = durationOrDefault(cfg.Identity.RefreshTTL, identityTune.RefreshTTL)
	identityService, err := identity.NewService(pool, cfg.Identity.JWTSecret, identityTune)
	if err != nil {
		return err
	}
	auditService := audit.New(pool)
	identityService.SetAuditService(auditService)
	shadowService.SetAuditor(auditService)
	mqttServer.SetShadowAuditor(auditService)
	registryAdmin.SetCredentialAuditor(auditService)
	registryAdmin.SetCredentialAlarmRaiser(alarmService)
	registryAdmin.SetCredentialCacheInvalidator(&credcacheInvalidator{nc: nc})
	registryAdmin.SetCertificateIssuer(pkiManager)
	rbacResolver := rbac.NewResolver(pool)
	hub.SetAuthService(identityService)
	hub.SetIdentity(identityService, rbacResolver)

	// ADR-0021 external IdP single sign-on (authorization-code + PKCE). It is
	// opt-in; a misconfiguration fails wiring rather than the first login.
	if cfg.OIDC.Enabled {
		if cfg.OIDC.Issuer == "" || cfg.OIDC.ClientID == "" ||
			cfg.OIDC.RedirectURL == "" || cfg.OIDC.TenantID == "" {
			return fmt.Errorf("oidc enabled but issuer, client_id, redirect_url and tenant_id are all required")
		}
		discCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var stateStore oidc.StateStore
		if cfg.OIDC.StateStore == "nats" {
			stateStore, err = oidc.NewKVStateStore(discCtx, js,
				cfg.NATS.JetStreamReplicas,
				durationOrDefault(cfg.OIDC.StateTTL, 10*time.Minute))
			if err != nil {
				cancel()
				return fmt.Errorf("create oidc state store: %w", err)
			}
		}
		oidcSvc, err := oidc.NewService(discCtx, oidc.Config{
			Issuer:          cfg.OIDC.Issuer,
			ClientID:        cfg.OIDC.ClientID,
			ClientSecret:    cfg.OIDC.ClientSecret,
			RedirectURL:     cfg.OIDC.RedirectURL,
			FrontendBaseURL: cfg.OIDC.FrontendBaseURL,
			Scopes:          cfg.OIDC.Scopes,
			TenantID:        cfg.OIDC.TenantID,
			DefaultRole:     cfg.OIDC.DefaultRole,
			GroupsClaim:     cfg.OIDC.GroupsClaim,
			GroupRole:       cfg.OIDC.GroupRole,
			StateTTL:        durationOrDefault(cfg.OIDC.StateTTL, 10*time.Minute),
			CAFile:          cfg.OIDC.CAFile,
			Store:           stateStore,
		}, identityService, pool)
		cancel()
		if err != nil {
			return fmt.Errorf("create oidc service: %w", err)
		}
		hub.SetOIDCService(oidcAdapter{svc: oidcSvc}, cfg.OIDC.FrontendBaseURL)
	}
	// ADR-0021 NereidSphere one-time handoff ticket SSO (alternative to OIDC).
	if cfg.SSO.Enabled {
		if cfg.SSO.NSBaseURL == "" || cfg.SSO.ClientID == "" ||
			cfg.SSO.SSOSecret == "" || cfg.SSO.TenantID == "" {
			return fmt.Errorf("sso enabled but ns_base_url, client_id, sso_secret and tenant_id are all required")
		}
		ssoSvc, err := sso.NewService(sso.Config{
			NSBaseURL:            cfg.SSO.NSBaseURL,
			ClientID:             cfg.SSO.ClientID,
			SSOSecret:            cfg.SSO.SSOSecret,
			CallbackPath:         cfg.SSO.CallbackPath,
			FrontendBaseURL:      cfg.SSO.FrontendBaseURL,
			TenantID:             cfg.SSO.TenantID,
			DefaultRole:          cfg.SSO.DefaultRole,
			RoleMap:              cfg.SSO.RoleMap,
			GroupRole:            cfg.SSO.GroupRole,
			ReconcileMemberships: cfg.SSO.ReconcileMemberships != nil && *cfg.SSO.ReconcileMemberships,
			CAFile:               cfg.SSO.CAFile,
		}, identityService, pool)
		if err != nil {
			return fmt.Errorf("create sso service: %w", err)
		}
		hub.SetSSOService(ssoAdapter{svc: ssoSvc}, cfg.SSO.FrontendBaseURL)
		hub.SetNSIntegration(ssoAdapter{svc: ssoSvc})
	}
	hub.SetAuditReader(auditService)
	hub.SetTrustAnchorManager(pkiManager)
	hub.SetLicenseAdmin(licenseService)
	hub.SetShadowStore(shadowService)

	memberService := usermgmt.New(pool)
	memberService.SetAuditor(auditService)
	memberService.SetEventBus(nc)
	hub.SetMemberAdmin(memberService)

	// ADR-0021 machine identity: the same service manages tenant
	// service accounts and authenticates client_credentials grants,
	// signing tokens through the identity signer.
	machineAuthService := machineauth.New(pool, identityService)
	machineAuthService.SetAuditor(auditService)
	hub.SetMachineAuth(machineAuthService, machineAuthService)

	userAPIKeyService := userapikey.New(pool)
	userAPIKeyService.SetAuditor(auditService)
	userAPIKeyService.SetPermissionReader(rbacResolver)
	hub.SetUserAPIKeys(userAPIKeyService, userAPIKeyService)

	hub.SetDashboardAdmin(dashboards.New(pool))

	escalationService := escalation.NewService(pool)
	hub.SetEscalationAdmin(escalationAdminAdapter{svc: escalationService})
	escalationRunner := escalation.NewRunner(pool,
		escalationSender{cfg: notifyConfig(cfg)},
		escalationInbox{inbox: notificationInbox}, 0)

	// ADR-0026 link-down notifications: consume gateway link-health
	// advisories into deduplicated alarms, escalate long outages to
	// CRITICAL and send the daily 24-hour digest.
	linkNotifyStore := linknotify.NewStore(pool)
	linkNotifyService := linknotify.NewService(nc,
		linkNotifyStore, alarmService,
		linkDigestSink{inbox: notificationInbox, cfg: notifyConfig(cfg)},
		linkNotifyTuning(cfg), slog.Default())
	mqttServer.SetLinkEpisodeStore(linkNotifyStore)
	hub.SetLinkEpisodeReader(linkNotifyStore)

	// ADR-0026 edge connector desired/reported lifecycle: persist desired
	// specs, push them to the connected gateway, re-send on reconnect,
	// consume edge status and raise the 5-minute pending / 1-hour failed
	// escalation alarms.
	connectorService := connector.NewService(nc,
		connector.NewStore(pool), alarmService,
		connectorTuning(cfg), slog.Default())
	mqttServer.SetConnectorStatusReceiver(connectorService)
	mqttServer.SetConnectorRedriver(connectorService)
	mqttServer.SetConnectorManifestReconciler(connectorService)
	hub.SetConnectorAdmin(connectorService)

	// ADR-0026 edge-registered connector config schemas: inline JSON Schemas
	// advertised in hello are compiled/stored here and used to validate
	// profile template and connector-instance config before persisting.
	connectorCatalog := connectorcatalog.New(connectorcatalog.NewStore(pool),
		connectorcatalog.WithLogger(slog.Default()))
	mqttServer.SetConnectorSchemaRegistrar(connectorCatalog)
	connectorService.SetConfigValidator(connectorCatalog)
	connectorService.SetProtocolFamilyReader(connectorCatalog)
	// Profile connector templates are full config documents validated with
	// the same edge-registered schemas; a type without a schema still
	// passes (fail-open).
	registryAdmin.SetConnectorTemplateValidator(connectorCatalog)
	hub.SetConnectorSchemaReader(connectorCatalog)
	// ADR-0034 edge secret type descriptor catalog: credential field
	// schemas advertised by gateways in hello and managed over REST.
	secretTypeCatalog := secretcatalog.New(secretcatalog.NewStore(pool))
	mqttServer.SetSecretTypeRegistrar(secretTypeCatalog)
	hub.SetSecretTypeAdmin(secretTypeCatalog)
	hub.SetSecretValidator(secretTypeCatalog)
	// ADR-0034 secret delivery desired-state: bindings are pushed over
	// Core NATS with secret fields as references and decrypted only at
	// the MQTT last mile.
	secretBindingService := secretbinding.New(secretbinding.NewStore(pool), nc, secretStore, secretTypeCatalog)
	secretBindingService.SetSecretMetaReader(secretStore)
	secretBindingService.SetAlarmRaiser(alarmService)
	hub.SetSecretBindingAdmin(secretBindingService)
	mqttServer.SetSecretStatusReceiver(secretBindingService)
	mqttServer.SetSecretRedriver(secretBindingService)
	mqttServer.SetDeliverySecretResolver(secretStore, secretBindingService)

	// ADR-0038 device-profile downlink: the tenant profile library is the
	// single content source, and delivery only fans a library profile out to
	// gateways that need it (explicit distribution ∪ connector references).
	// Documents are rendered per the format each gateway declared.
	profilePushService := profilepush.New(profilepush.NewStore(pool), nc)
	profilePushService.SetAlarmRaiser(alarmService)
	hub.SetProfileDeliveryAdmin(profilePushService)
	mqttServer.SetProfileStatusReceiver(profilePushService)
	mqttServer.SetProfileRedriver(profilePushService)
	mqttServer.SetProfileDispatchMarker(profilePushService)
	go func() {
		if err := profilePushService.Run(context.Background()); err != nil {
			slog.Error("profile delivery sweep stopped", "error", err)
		}
	}()

	// ADR-0039 edge model inference (Phase A): reconcile the manifest
	// models full-replacement segment, serve the inventory over REST/WS,
	// mark it stale when the shared gw-manifest lease expires, and run the
	// model/runtime stale watchdog (FAILED-model and runtime OFFLINE/stale
	// alarms). No model bytes transit NereidLink, and the generic
	// edge-inference connector reuses the connector lifecycle with no kernel
	// inference semantics.
	modelInventoryService := modelinventory.NewService(nc,
		modelinventory.NewStore(pool), alarmService,
		modelinventory.DefaultSweepInterval, slog.Default())
	mqttServer.SetModelInventoryReconciler(modelInventoryService)
	hub.SetModelInventoryReader(modelInventoryService)
	manifestReconciler.AddLeaseExpirySink(modelInventoryService)

	// Platform-neutral companion connector
	// (companion-subscription/v1): one generic owner reconciles every
	// conformant companion through registered endpoints; the admin service
	// exposes endpoint registration and desired/read-back visibility.
	companionStore := companion.NewPGStore(pool)
	hub.SetCompanionAdmin(companion.NewAdmin(companionStore))
	companionOwner := companion.NewOwner(companionStore, nil, slog.Default())

	// NereidHorizon model distribution (nh-model-distribution/v1): one
	// generic ensure client for every model_distribution artifact. Disabled
	// unless the horizon config section is enabled; a nil owner makes
	// preflight block instead of silently skipping distribution.
	var modelDistributor business.ModelDistributor
	if cfg.Horizon.Enabled {
		modelDistributor, err = horizon.NewOwner(cfg.Horizon, nil, slog.Default())
		if err != nil {
			return fmt.Errorf("init horizon model distribution: %w", err)
		}
	}

	// Visual business orchestration: deterministic assembly expansion with
	// generation-fenced persistence. Destination owners stay authoritative;
	// manager resources, inference bindings and optional job stats reuse the
	// connector lifecycle.
	businessService := business.NewService(business.NewStore(pool), business.Owners{
		Connectors:   connectorService,
		Rules:        ruleChainStore,
		Devices:      registryAdmin,
		Forwarders:   egressService,
		Models:       modelInventoryService,
		Managers:     connectorService,
		Companions:   companionOwner,
		Metrics:      connectorService,
		Distribution: modelDistributor,
	}, slog.Default())
	hub.SetBusinessAdmin(businessService)

	stepUpKeySum := sha256.Sum256(append(
		[]byte("nereidlink-stepup-payload-v1:"), []byte(cfg.Identity.JWTSecret)...))
	stepUpService := stepup.New(stepup.NewStore(pool), identityService, stepUpKeySum[:])
	stepUpService.SetAuditService(auditService)
	hub.SetStepUp(stepUpService)

	// ADR-0034 deletion protection: block deletion of in-use secrets; a
	// forced delete withdraws all bindings (TOTP step-up verified) and
	// hard-deletes the vault original after the last REMOVED report.
	secretDeleteService := secretdelete.New(secretdelete.NewStore(pool),
		secretBindingService, secretStore)
	secretDeleteService.SetAuditor(auditService)
	hub.SetSecretDeletionAdmin(secretDeleteService)
	secretBindingService.SetRemovalHook(secretDeleteService.OnRemoval)

	operationCatalog := opcatalog.New(opcatalog.NewStore(pool))
	hub.SetOperationCatalog(operationCatalog)
	mqttServer.SetOperationRegistrar(operationCatalog)
	// ADR-0035 synchronous operation execution: presence locating, sync
	// dispatch through Core NATS, 90-day task retention and audit.
	operationInvokeService := opinvoke.New(nc, opinvoke.NewStore(pool),
		operationCatalog, presenceService)
	operationInvokeService.SetAuditService(auditService)
	hub.SetOperationInvoker(operationInvokeService)
	mqttServer.SetOperationSecretResolver(secretStore)
	// ADR-0033 global, operator-editable protocol/device-service catalog:
	// the single source for the console's structured connection/point forms
	// and the visible "Device Services" page. Rows are global vendor facts.
	hub.SetProtocolCatalog(protocolcatalog.New(protocolcatalog.NewStore(pool)))
	hub.SetEdgeProfileAdmin(edgeProfileService)
	connectorService.SetSecretResolver(secretStore)
	mqttServer.SetConnectorSecretResolver(secretStore)

	opsService := opscenter.New(pool)
	opsService.SetAuditor(auditService)
	opsService.SetExecutors(map[string]opscenter.ActionExecutor{
		// telemetry.purge deletes every stored row of the tenant
		// older than the reviewed cutoff and reports the row count.
		opscenter.ActionTelemetryPurge: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				Before string `json:"before"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse purge payload: %w", err)
			}
			if order.TenantID == "" || body.Before == "" {
				return nil, fmt.Errorf("tenant and before are required")
			}
			cutoff, err := time.Parse(time.RFC3339, body.Before)
			if err != nil {
				return nil, fmt.Errorf("parse cutoff: %w", err)
			}
			tag, err := pool.Exec(ctx,
				`DELETE FROM ts_telemetry
				 WHERE tenant_id = $1 AND time < $2`,
				order.TenantID, cutoff)
			if err != nil {
				return nil, fmt.Errorf("purge telemetry: %w", err)
			}
			return json.RawMessage(
				fmt.Sprintf(`{"deleted":%d}`, tag.RowsAffected())), nil
		},

		// tenant.deprovision permanently closes the tenant: the row
		// stays (foreign keys and history reference it) but the
		// credential epoch is raised and every device disconnects.
		opscenter.ActionTenantDeprovision: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse deprovision payload: %w", err)
			}
			if order.TenantID == "" {
				return nil, fmt.Errorf("tenant is required")
			}

			var current string
			err := pool.QueryRow(ctx,
				`SELECT status FROM tenant WHERE id = $1`,
				order.TenantID).Scan(&current)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, fmt.Errorf("tenant not found")
				}
				return nil, fmt.Errorf("load tenant: %w", err)
			}
			if current == "DEPROVISIONED" {
				return nil, fmt.Errorf("tenant is already deprovisioned")
			}

			if _, err := pool.Exec(ctx,
				`UPDATE tenant
				 SET status = 'DEPROVISIONED',
				     status_reason = $2,
				     credential_epoch = credential_epoch + 1,
				     updated_at = now()
				 WHERE id = $1`,
				order.TenantID, body.Reason); err != nil {
				return nil, fmt.Errorf("deprovision tenant: %w", err)
			}

			rows, err := pool.Query(ctx,
				`SELECT id::text FROM device WHERE tenant_id = $1`,
				order.TenantID)
			if err != nil {
				return nil, fmt.Errorf("list tenant devices: %w", err)
			}
			deviceIDs, err := collectDeviceIDs(rows)
			if err != nil {
				return nil, fmt.Errorf("scan tenant devices: %w", err)
			}

			for _, deviceID := range deviceIDs {
				if err := presenceService.ForceRemove(ctx, presence.Identity{
					TenantID: order.TenantID,
					DeviceID: deviceID,
				}, "tenant deprovisioned"); err != nil {
					slog.Warn("presence eviction during deprovision failed",
						"device", deviceID, "error", err)
				}
				if err := mqttServer.DisconnectDevice(ctx, order.TenantID, deviceID); err != nil {
					slog.Warn("device disconnect during deprovision failed",
						"device", deviceID, "error", err)
				}
			}
			return json.RawMessage(fmt.Sprintf(
				`{"devices_disconnected":%d}`, len(deviceIDs))), nil
		},

		// retention.shorten performs the reviewed one-off cleanup:
		// every row older than the new window is deleted.
		opscenter.ActionRetentionShorten: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				Days   int    `json:"days"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse retention payload: %w", err)
			}
			if order.TenantID == "" || body.Days <= 0 {
				return nil, fmt.Errorf("tenant and positive days are required")
			}
			cutoff := time.Now().UTC().Add(
				-time.Duration(body.Days) * 24 * time.Hour)
			tag, err := pool.Exec(ctx,
				`DELETE FROM ts_telemetry
				 WHERE tenant_id = $1 AND time < $2`,
				order.TenantID, cutoff)
			if err != nil {
				return nil, fmt.Errorf("apply retention cutoff: %w", err)
			}
			return json.RawMessage(fmt.Sprintf(
				`{"deleted":%d}`, tag.RowsAffected())), nil
		},

		// node.force-disconnect evicts presence and closes the live
		// transport client of the reviewed device.
		opscenter.ActionForceDisconnect: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				DeviceID string `json:"device_id"`
				Reason   string `json:"reason"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse disconnect payload: %w", err)
			}
			if order.TenantID == "" || body.DeviceID == "" {
				return nil, fmt.Errorf("tenant and device_id are required")
			}
			if err := presenceService.ForceRemove(ctx, presence.Identity{
				TenantID: order.TenantID,
				DeviceID: body.DeviceID,
			}, body.Reason); err != nil {
				return nil, fmt.Errorf("evict presence: %w", err)
			}
			if err := mqttServer.DisconnectDevice(ctx, order.TenantID, body.DeviceID); err != nil {
				return nil, fmt.Errorf("disconnect device: %w", err)
			}
			return json.RawMessage(`{"disconnected":true}`), nil
		},

		// license.replace installs the reviewed signed envelope,
		// replacing the current license scope atomically.
		opscenter.ActionLicenseReplace: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				License json.RawMessage `json:"license"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse license replace payload: %w", err)
			}
			if len(body.License) == 0 {
				return nil, fmt.Errorf("license envelope is required")
			}
			signed, err := license.ParseFile(body.License)
			if err != nil {
				return nil, fmt.Errorf("parse license envelope: %w", err)
			}
			payload, err := licenseService.Install(ctx, signed, "ops-order")
			if err != nil {
				return nil, fmt.Errorf("install license: %w", err)
			}
			result, err := json.Marshal(map[string]any{
				"scope":         payload.Scope,
				"customer_name": payload.CustomerName,
				"expires_at":    payload.ExpiresAt.UTC().Format(time.RFC3339),
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},

		// quarantine.release lifts the incident-response state so the
		// device can open connections again.
		opscenter.ActionQuarantineRelease: func(ctx context.Context, order opscenter.Order) (json.RawMessage, error) {
			var body struct {
				DeviceID string `json:"device_id"`
			}
			if err := json.Unmarshal(order.Payload, &body); err != nil {
				return nil, fmt.Errorf("parse quarantine release payload: %w", err)
			}
			if order.TenantID == "" || body.DeviceID == "" {
				return nil, fmt.Errorf("tenant and device_id are required")
			}
			tag, err := pool.Exec(ctx,
				`UPDATE device
				 SET state = 'ACTIVE',
				     quarantined_at = NULL,
				     quarantine_reason = '',
				     updated_at = now()
				 WHERE id = $1 AND tenant_id = $2 AND state = 'QUARANTINED'`,
				body.DeviceID, order.TenantID)
			if err != nil {
				return nil, fmt.Errorf("release quarantine: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return nil, fmt.Errorf("device is not quarantined")
			}
			return json.RawMessage(fmt.Sprintf(
				`{"device_id":%q,"state":"ACTIVE"}`, body.DeviceID)), nil
		},
	})
	hub.SetOpsCenter(opsService)

	g, gctx := errgroup.WithContext(ctx)

	// runRole starts a background role. In the single all-in-one topology
	// every role runs directly; in clustered mode only the elected leader
	// for that role runs, with automatic failover and no split-brain.
	var runRole func(role string, fn func(context.Context) error)
	if cfg.Cluster.Enabled {
		instanceID := cfg.Cluster.InstanceID
		if instanceID == "" {
			instanceID, _ = os.Hostname()
		}
		leaseTTL := durationOrDefault(cfg.Cluster.LeaseTTL, 15*time.Second)
		elector, err := leaderelection.New(gctx, js, instanceID,
			cfg.NATS.JetStreamReplicas, leaseTTL, slog.Default())
		if err != nil {
			return fmt.Errorf("create leader elector: %w", err)
		}
		runRole = func(role string, fn func(context.Context) error) {
			g.Go(func() error { return elector.Run(gctx, role, fn) })
		}
	} else {
		runRole = func(_ string, fn func(context.Context) error) {
			g.Go(func() error { return fn(gctx) })
		}
	}
	// One-time ADR-0009 backfill: populate leaf fingerprints on X509
	// credentials created before migration 0034 so reuse detection also
	// covers historical rows. Idempotent; no-op once all rows are set.
	if filled, err := registryAdmin.BackfillCertFingerprints(ctx); err != nil {
		return fmt.Errorf("backfill cert fingerprints: %w", err)
	} else if filled > 0 {
		slog.Info("backfilled X.509 credential fingerprints", "count", filled)
	}
	g.Go(func() error { return presenceService.Run(gctx) })
	runRole("manifest-reconciler", manifestReconciler.Run)
	g.Go(func() error { return linkMonitor.Run(gctx) })
	runRole("link-notify", linkNotifyService.Run)
	runRole("connector-sweep", connectorService.Run)
	g.Go(func() error { return modelInventoryService.Run(gctx) })
	g.Go(func() error { return secretBindingService.Run(gctx) })
	g.Go(func() error { return secretDeleteService.Run(gctx) })
	g.Go(func() error { return mqttServer.Run(gctx) })
	g.Go(func() error { return httpServer.Run(gctx) })
	g.Go(func() error { return telemetryServer.Run(gctx) })
	runRole("retention", func(ctx context.Context) error {
		// Retention runs daily at a low cadence: deletion is driven by
		// age, not realtime pressure.
		interval := mustDuration(cfg.Retention.SweepInterval)
		return retentionEnforcer.Run(ctx, interval)
	})
	g.Go(func() error {
		interval := mustDuration(cfg.IngestLimit.SweepInterval)
		idle := mustDuration(cfg.IngestLimit.Idle)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case now := <-ticker.C:
				deviceLimiter.Sweep(now.UTC(), idle)
				if floodGuard != nil {
					deviceLimiter.SweepFlood(now.UTC(), idle)
				}
			}
		}
	})
	if floodGuard != nil {
		g.Go(func() error { floodGuard.Run(gctx); return nil })
	}
	if silenceSweeper != nil {
		silenceSweeper := silenceSweeper
		runRole("silence-sweep", func(ctx context.Context) error {
			return silenceSweeper.Run(ctx,
				durationOrDefault(cfg.SilenceDetection.SweepInterval, time.Minute))
		})
	}
	g.Go(func() error { return commandBackground.Run(gctx) })
	runRole("stepup-gc", func(ctx context.Context) error { return stepUpService.Run(ctx, time.Minute) })
	runRole("opinvoke-gc", func(ctx context.Context) error { return operationInvokeService.Run(ctx, time.Hour) })
	runRole("rule-processor", func(ctx context.Context) error { return ruleProcessor.Run(ctx, ruleShardCount) })
	g.Go(func() error { return egressWorker.Run(gctx) })
	runRole("notify", notifyService.Run)
	runRole("deadman", func(ctx context.Context) error { deadmanMonitor.Run(ctx); return nil })
	if accountingReporter != nil {
		// Both halves run on every node: this node publishes its own
		// window, and every node competes in the shared aggregator group
		// so a report is stored exactly once cluster-wide.
		g.Go(func() error { return accountingReporter.Run(gctx) })
		g.Go(func() error { return accountingAggregator.Run(gctx) })
	}
	runRole("escalation", escalationRunner.Run)
	runRole("module-heartbeat", func(ctx context.Context) error {
		return moduleStore.Run(ctx, module.HeartbeatLease{
			Window:        mustDuration(cfg.Module.HeartbeatLease),
			SweepInterval: mustDuration(cfg.Module.SweepInterval),
		})
	})
	runRole("provision", provisionService.Run)
	runRole("audit-retention", auditService.Run)
	runRole("credential-expiry", registryAdmin.RunCredentialExpiry)
	runRole("device-lifecycle", registryAdmin.RunLifecycleSweep)
	runRole("license", licenseService.Run)
	runRole("shadow-push", shadowService.Run)
	runRole("ops-sweeper", func(ctx context.Context) error { return opsService.RunSweeper(ctx, time.Hour) })
	g.Go(func() error { return hub.Run(gctx) })
	runRole("probe", probeManager.Run)
	runRole("ota", otaManager.Run)
	runRole("command-batch-recover", func(ctx context.Context) error {
		if err := batchManager.Recover(ctx); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	g.Go(func() error {
		return gatewayapi.ListenAndServe(gctx,
			trimAddress(cfg.Server.HTTPListen), cfg.Server.WSPath,
			mustDuration(cfg.Server.ShutdownTimeout), hub.Handler(cfg.Server.WSPath))
	})
	if cfg.OpsAdmin.Listen != "" {
		opsTimeout := durationOrDefault(cfg.OpsAdmin.ShutdownTimeout, 10*time.Second)
		// The listener binds loopback (or a management network) and
		// needs no login: every destructive order still requires the
		// super administrator to confirm it on the web.
		g.Go(func() error {
			return gatewayapi.ListenAndServe(gctx,
				trimAddress(cfg.OpsAdmin.Listen), "", opsTimeout,
				hub.OpsServeMux())
		})
		slog.Info("ops admin listener enabled", "addr", cfg.OpsAdmin.Listen)
	}

	slog.Info("nereidlink serving",
		"version", version,
		"http", cfg.Server.HTTPListen,
		"mqtt", cfg.IngestMQTT.Listen,
		"registry", cfg.Registry.Listen)
	err = g.Wait()
	if err == nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), presenceShutdownTimeout)
		defer cancel()
		if shutdownErr := presenceService.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.Warn("presence shutdown failed", "error", shutdownErr)
		}
	}
	return err
}

// startRegistry launches the registry gRPC server on a background
// goroutine. When tlsConfig is non-nil the listener serves TLS 1.3 with
// mutual authentication, per the ADR-0003 internal-link profile.
func startRegistry(address, moduleToken string, server v1.RegistryAuthServer,
	tlsConfig *tls.Config) (*grpc.Server, error) {
	listener, err := net.Listen("tcp", trimAddress(address))
	if err != nil {
		return nil, fmt.Errorf("listen registry: %w", err)
	}
	interceptor := registry.NewTokenInterceptor(moduleToken)
	opts := []grpc.ServerOption{grpc.UnaryInterceptor(interceptor.Unary)}
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	grpcServer := grpc.NewServer(opts...)
	v1.RegisterRegistryAuthServer(grpcServer, server)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			slog.Error("registry grpc serve failed", "error", err)
		}
	}()
	return grpcServer, nil
}

// telemetryTuning maps declarative YAML values to the writer tuning object.
func telemetryTuning(cfg *config.Config) (telemetry.Tuning, error) {
	return telemetry.Tuning{
		FlushInterval: mustDuration(cfg.Telemetry.FlushInterval),
		FlushSize:     cfg.Telemetry.FlushSize,
		MaxAckPending: cfg.Telemetry.MaxAckPending,
		DedupWindow:   cfg.Telemetry.DedupWindow,
	}, nil
}

// mustDuration parses a duration that configuration validation guarantees.
func mustDuration(value string) time.Duration {
	d, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return d
}

// trimAddress converts a ":port" host specification into an explicit loopback
// address suitable for listeners/clients that require a host component.
func trimAddress(value string) string {
	if len(value) > 0 && value[0] == ':' {
		return "127.0.0.1" + value
	}
	return value
}

// collectDeviceIDs drains device id rows returned by pool.Query.
func collectDeviceIDs(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
