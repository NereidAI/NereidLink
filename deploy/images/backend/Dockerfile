# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

# syntax=docker/dockerfile:1

# Backend production image for NereidLink (frontend/backend packaging split):
# published as ghcr.io/nereidai/nereidlink-backend. The static console ships
# in the separate nereidlink-frontend image; the go:embed web/dist tree here
# only holds the tracked .gitkeep placeholder, so the standalone single
# binary built by `make build` still embeds a real UI while container
# deployments serve the console from the frontend tier.
#
# Build context MUST be the repository root (the build needs cmd/, go.mod,
# the embedded SQL migrations under internal/store/migrations, web/dist's
# tracked placeholder and the license public key):
#
#   docker buildx build -f deploy/images/backend/Dockerfile \
#       --platform linux/amd64,linux/arm64 --load \
#       --build-arg VERSION=0.0.0-dev \
#       -t nereidlink-backend:0.0.0-dev .
#
# VERSION defaults to the rolling dev channel 0.0.0-dev; tag releases
# inject the bare SemVer (v0.0.1 tag -> VERSION 0.0.1) via the workflow.
#
# Base images default to the suite's public baseline coordinates (treaty
# §16.1). Sites with no public-registry access override *_BASE at build time
# with their node-level Harbor mirrors; internal hostnames never appear as
# committed defaults (treaty §15).

ARG GOLANG_BASE=golang:1.27.3-alpine3.24
ARG RUNTIME_BASE=gcr.io/distroless/static-debian12:nonroot

# Cross-compile the static Go binary on the native builder for each
# requested target. Runtime emulation is never needed for the compiler stage.
FROM --platform=$BUILDPLATFORM ${GOLANG_BASE} AS build
ARG GOPROXY=""
ENV GOPROXY=${GOPROXY}
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-$(go env GOARCH)} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/nereidlink ./cmd/nereidlink

# Minimal runtime image. The license vendor public key is required at
# startup, so it is copied to a fixed path referenced by the Helm/compose
# configuration. State that must survive restarts (secret KEK, license clock)
# is mounted at /var/lib/nereidlink.
FROM ${RUNTIME_BASE}
ARG VERSION=0.0.0-dev
ARG GIT_SHA=unknown
ARG BUILD_DATE=""
LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2026: YIQISOFT' \
      org.opencontainers.image.title='nereidlink-backend' \
      org.opencontainers.image.description='Privatized IoT data pipeline — multi-protocol device access, unified messaging, rule engine, alerting, and real-time dashboards' \
      org.opencontainers.image.vendor='YIQISOFT' \
      org.opencontainers.image.licenses='Apache-2.0' \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source='https://github.com/NereidAI/NereidLink' \
      org.opencontainers.image.url='https://github.com/NereidAI/NereidLink' \
      org.opencontainers.image.documentation='https://github.com/NereidAI/NereidLink#readme' \
      org.opencontainers.image.authors='NereidAI (https://github.com/NereidAI)'
COPY --from=build /out/nereidlink /usr/local/bin/nereidlink
COPY --from=build /src/configs/license/public.pem /etc/nereidlink/license/public.pem
# distroless-static ships no root CAs; copy the Alpine ca-certificates bundle
# so outbound HTTPS (OIDC discovery/JWKS, S3, webhooks) can verify peers.
# The chart may mount a secret with an internal CA over this same path.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Project and third-party license notices (notably the TimescaleDB Community
# Edition TSL terms for the bundled database image described in deploy/README.md).
COPY LICENSE NOTICE THIRD_PARTY_NOTICES /licenses/
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV NL_DATA_DIR=/var/lib/nereidlink
VOLUME ["/var/lib/nereidlink"]
EXPOSE 8080 1883
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/nereidlink"]
CMD ["serve", "-c", "/etc/nereidlink/config.yaml"]
