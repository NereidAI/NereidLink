# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

# syntax=docker/dockerfile:1

# Static frontend image for NereidLink (frontend/backend packaging split):
# published as ghcr.io/nereidai/nereidlink-frontend.
#
# Build context MUST be the repository root (the build needs web/ and this
# Dockerfile tree):
#
#   docker buildx build -f deploy/images/frontend/Dockerfile \
#       --platform linux/amd64,linux/arm64 --load \
#       -t nereidlink-frontend:0.0.0-dev .
#
# VERSION defaults to the rolling dev channel 0.0.0-dev; tag releases
# inject the bare SemVer (v0.0.1 tag -> VERSION 0.0.1) via the workflow.
#
# The image only serves the built Vite assets with nginx; it never proxies
# to the backend (path splitting happens at the Ingress/edge layer). Base
# images default to public baseline coordinates (treaty §16.1). Sites with
# no public-registry access override *_BASE at build time with their
# node-level registry mirrors; internal hostnames never appear as committed
# defaults (treaty §15).

ARG NODE_BASE=node:24-alpine
ARG NGINX_BASE=nginx:alpine

# Stage 1: build the architecture-neutral Vite bundle once on the native
# builder; the same output is shared by every target platform.
FROM --platform=$BUILDPLATFORM ${NODE_BASE} AS build
# VERSION is consumed by the CI/release build-arg contract. The default is
# the rolling dev channel; release builds inject bare SemVer.
ARG VERSION=0.0.0-dev
# Regional npm registry injected at build time (treaty §17); empty keeps the
# official npm default registry.
ARG NPM_REGISTRY=""
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi \
    && (npm ci || npm install)
COPY web/ ./
# VITE_APP_VERSION is the identity baked into the static bundle's sidebar
# footer (dev channel: 0.0.0-dev-<short-sha>, releases: bare SemVer); it
# defaults to VERSION so a caller that only passes VERSION still gets a
# coherent footer. It is declared after the dependency layer because the dev
# value changes on every commit: placing it here keeps the npm ci layer cached
# and re-runs only the Vite build. Vite reads process.env at build time, so
# ENV promotes the ARG into the environment of the RUN step below.
ARG VITE_APP_VERSION=${VERSION}
ENV VITE_APP_VERSION=${VITE_APP_VERSION}
RUN npm run build

# Stage 2: static nginx runtime. No proxy_pass anywhere: the Ingress path
# table sends /v1, /ws, /api, /metrics, /healthz and /auth to
# nereidlink-backend, and everything else to this frontend.
FROM ${NGINX_BASE}
ARG VERSION=0.0.0-dev
ARG GIT_SHA=unknown
ARG BUILD_DATE=""
LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2026: YIQISOFT' \
      org.opencontainers.image.title='nereidlink-frontend' \
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
COPY LICENSE NOTICE THIRD_PARTY_NOTICES /licenses/
COPY deploy/images/frontend/nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
