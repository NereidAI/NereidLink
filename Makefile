# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

SHELL := /bin/bash

# Optional local build acceleration (treaty §19.3): copy local.mk.example to
# local.mk and fill in regional proxy/mirror endpoints. The file is git-ignored;
# a missing file leaves every endpoint empty for a pristine direct-internet
# build, which is also how CI runs.
-include local.mk

export GOROOT := $(HOME)/go
export GOPATH := $(HOME)/gopath
export PATH := $(GOROOT)/bin:$(GOPATH)/bin:$(HOME)/node/bin:$(PATH)
# Build network endpoints are never hardcoded (treaty §17): set them in the
# git-ignored local.mk or the shell for regional/lab networks. Empty values
# keep the toolchains' pristine upstream defaults.
export GOPROXY ?=
export HTTP_PROXY ?=
export HTTPS_PROXY ?=

.PHONY: dev infra-up infra-down migrate seed test lint web-dev web-build build image image-frontend image-coap proto adapters

# No VERSION file: the git tag is the single version source (treaty §19).
# On an exact v* tag checkout use its bare SemVer (v0.0.1 -> 0.0.1); any
# other checkout builds the rolling dev channel 0.0.0-dev. ?= lets CI
# inject VERSION from the workflow environment.
VERSION ?= $(shell ref=$$(git describe --tags --exact-match 2>/dev/null) && echo "$${ref#v}" || echo 0.0.0-dev)

# Local image coordinates. Make only builds/packages (treaty §19.1): every
# image carries the three-tag scheme (§19.2) — VERSION, VERSION-timestamp and
# VERSION-git-sha — and nothing here pushes. GHCR publication is owned by the
# tag-triggered release workflow; local hub copies are the operator's own
# manual docker tag/push decision or NereidOverview harbor-sync.sh.
# The split topology publishes two suite images: nereidlink-backend (Go)
# and nereidlink-frontend (static nginx console).
IMAGE_NAME ?= nereidlink-backend
FRONTEND_IMAGE_NAME ?= nereidlink-frontend
GIT_SHA ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TS := $(shell date -u +%Y%m%d-%H%M)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE_TAGS := -t $(IMAGE_NAME):$(VERSION) \
	-t $(IMAGE_NAME):$(VERSION)-$(BUILD_TS) \
	-t $(IMAGE_NAME):$(VERSION)-$(GIT_SHA)
FRONTEND_IMAGE_TAGS := -t $(FRONTEND_IMAGE_NAME):$(VERSION) \
	-t $(FRONTEND_IMAGE_NAME):$(VERSION)-$(BUILD_TS) \
	-t $(FRONTEND_IMAGE_NAME):$(VERSION)-$(GIT_SHA)
# PLATFORMS always includes the two supported deployment architectures unless
# the caller explicitly narrows or extends the local build.
PLATFORMS ?= linux/amd64,linux/arm64
# DOCKER preserves the existing sudo-by-default convention; rootless setups can
# use DOCKER=docker. IMAGE_BUILD_ARGS accepts site-specific base overrides.
DOCKER ?= sudo docker
IMAGE_BUILD_ARGS ?=

# Build-network injection (treaty §17): empty defaults keep pristine upstream
# sources; regional/lab builds set GOPROXY/NPM_REGISTRY via local.mk.
NPM_REGISTRY ?=
NETWORK_BUILD_ARGS := --build-arg GOPROXY=$(GOPROXY) \
	--build-arg NPM_REGISTRY=$(NPM_REGISTRY)

infra-up:
	sudo docker compose -f docker-compose.dev.yml up -d

infra-down:
	sudo docker compose -f docker-compose.dev.yml down

migrate:
	go run ./cmd/nereidlink migrate up -c configs/dev.yaml

seed:
	go run ./cmd/nereidlink seed -c configs/dev.yaml

dev:
	go run ./cmd/nereidlink serve -c configs/dev.yaml

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

# build produces the complete single binary with embedded web assets.
build: web-build
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/nereidlink ./cmd/nereidlink

# image packages the backend production image (deploy/images/backend). It
# bakes the license vendor public key and the CA bundle without which serve
# exits at startup. Multi-platform --load requires Docker's containerd image
# store and never pushes. Use PLATFORMS=linux/amd64 only for an explicitly
# narrowed local build. The three tags (VERSION/timestamp/git-sha, treaty
# §19.2) are all loaded into the local daemon; publication belongs to the
# release workflow.
image:
	$(DOCKER) buildx build -f deploy/images/backend/Dockerfile \
		--platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		--build-arg GIT_SHA=$(GIT_SHA) --build-arg BUILD_DATE=$(BUILD_DATE) \
		$(NETWORK_BUILD_ARGS) $(IMAGE_BUILD_ARGS) $(IMAGE_TAGS) --load .

# image-frontend packages the static console image (deploy/images/frontend):
# a node build of web/ served by nginx with no backend proxying — path
# splitting happens at the Ingress/edge. Same three-tag, load-only contract
# as image; NPM_REGISTRY is the only network build arg it needs.
image-frontend:
	$(DOCKER) buildx build -f deploy/images/frontend/Dockerfile \
		--platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		--build-arg GIT_SHA=$(GIT_SHA) --build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		$(IMAGE_BUILD_ARGS) $(FRONTEND_IMAGE_TAGS) --load .

test:
	go test ./...

lint:
	# config verify validates the v2 JSON schema; `run` silently ignores
	# unknown keys, so skipping verify lets CI-only config drift through.
	golangci-lint config verify
	golangci-lint run

proto:
	cd api && buf generate

# adapter-% builds one southbound module binary. The map gives the package
# path of each module command entry point.
adapter-pkg-noop = ./adapters/noop
adapter-pkg-coap = ./adapters/coap/cmd
ADAPTERS := noop coap

adapters: $(addprefix adapter-,$(ADAPTERS))

adapter-%:
	mkdir -p bin/adapters
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
		-o bin/adapters/$* $(adapter-pkg-$*)

# image-coap packages the CoAP module container image with the same
# three-tag scheme as the main image (treaty §19.2), loaded locally only.
image-coap:
	sudo docker build --build-arg GOPROXY=$(GOPROXY) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_SHA=$(GIT_SHA) --build-arg BUILD_DATE=$(BUILD_DATE) \
		-f adapters/coap/Dockerfile \
		-t nereidlink/coap:$(VERSION) \
		-t nereidlink/coap:$(VERSION)-$(BUILD_TS) \
		-t nereidlink/coap:$(VERSION)-$(GIT_SHA) .

# Device-side Go SDK lives in its own independent module so external
# firmware never inherits the server's NATS/database dependencies.
DEVICE_SDK_DIR := sdk/device/go

sdk-device-test:
	cd $(DEVICE_SDK_DIR) && go test ./...

sdk-device-vet:
	cd $(DEVICE_SDK_DIR) && go vet ./...

sdk-device-lint:
	cd $(DEVICE_SDK_DIR) && golangci-lint run --config ../../../.golangci.yml

sdk-device-examples:
	cd $(DEVICE_SDK_DIR) && go build ./examples/...

sdk-device: sdk-device-vet sdk-device-lint sdk-device-test sdk-device-examples
