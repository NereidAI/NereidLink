// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package adapter is the shared SDK for southbound protocol modules
// (CoAP, LwM2M, SNMP) deployed as separate containers. It owns the parts
// common to every module: management enrollment, heartbeat lease renewal,
// Core NATS command dispatch subscription with ack correlation, and
// configuration loading. Protocol packages only implement translation
// between their wire protocol and the platform contracts; they never
// implement the command state machine themselves.
package adapter

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root adapter configuration.
type Config struct {
	// Core locates the platform dependencies.
	Core CoreSection `yaml:"core"`

	// Module carries the adapter enrollment settings.
	Module ModuleSection `yaml:"module"`
}

// CoreSection locates the platform services.
type CoreSection struct {
	// NATSURL is used for JetStream uplink and dispatch.
	NATSURL string `yaml:"nats_url"`

	// NATSUser and NATSPassword select a permission-limited module user
	// when the hardened NATS configuration is enabled.
	NATSUser     string `yaml:"nats_user"`
	NATSPassword string `yaml:"nats_password"`

	// RegistryGRPC is the device authentication endpoint.
	RegistryGRPC string `yaml:"registry_grpc"`

	// RegistryToken is the shared module token for the gRPC endpoint.
	RegistryToken string `yaml:"registry_token"`

	// ManagementURL is the REST base for enrollment and heartbeat.
	ManagementURL string `yaml:"management_url"`

	// PostgresDSN is an optional read-only database connection used to
	// resolve device profile descriptors and expression transforms.
	PostgresDSN string `yaml:"postgres_dsn"`
}

// ModuleSection carries adapter enrollment settings.
type ModuleSection struct {
	// EnrollmentToken authenticates registration and heartbeats.
	EnrollmentToken string `yaml:"enrollment_token"`

	// HeartbeatInterval paces lease renewal as a duration string.
	HeartbeatInterval string `yaml:"heartbeat_interval"`
}

// Load reads and parses an adapter configuration file.
func Load(path string) (*Config, error) {
	// The path is an operator-supplied CLI value rather than untrusted
	// input, so the gosec file-inclusion warning does not apply.
	//nolint:gosec // operator-selected configuration file path
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Heartbeat returns the parsed renewal interval, applying a default.
func (c *Config) Heartbeat() (time.Duration, error) {
	if c.Module.HeartbeatInterval == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(c.Module.HeartbeatInterval)
	if err != nil {
		return 0, fmt.Errorf("parse heartbeat_interval: %w", err)
	}
	return d, nil
}
