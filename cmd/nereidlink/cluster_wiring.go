// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// cluster_wiring.go applies per-pod identity overrides for the multi-replica
// Helm topology. The config file carries shared values; anything that must be
// unique per replica (presence ingress id, cluster instance id) is injected by
// Kubernetes as the pod name via the downward API. Single-node deployments
// (docker compose, one-pod k3s) set no env vars and keep the config values.

package main

import (
	"os"

	"github.com/nereidai/nereidlink/internal/config"
)

// applyPodIdentity overrides shared config identifiers from the environment.
func applyPodIdentity(cfg *config.Config) {
	pod := os.Getenv("NL_POD_NAME")
	if pod == "" {
		return
	}
	cfg.Presence.IngressID = pod
	if cfg.IngestHTTP.IngressID != "" {
		cfg.IngestHTTP.IngressID = pod + "-http"
	}
	if cfg.Cluster.InstanceID == "" {
		cfg.Cluster.InstanceID = pod
	}
}
