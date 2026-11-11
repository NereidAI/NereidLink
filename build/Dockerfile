# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

# syntax=docker/dockerfile:1

# Stage 1: build the web assets.
FROM node:24-alpine AS web
# Regional npm registry injected at build time (treaty §17); empty keeps the
# official npm default registry.
ARG NPM_REGISTRY=""
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi \
    && (npm ci || npm install)
COPY web/ ./
RUN npm run build

# Stage 2: build the static Go binary with embedded web assets. Compile on
# the native builder and cross-compile via TARGETOS/TARGETARCH so arm64
# never builds under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build
ARG GOPROXY=""
ENV GOPROXY=${GOPROXY}
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=0.0.0-dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-$(go env GOARCH)} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/nereidlink ./cmd/nereidlink

# Stage 3: minimal runtime image.
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=0.0.0-dev
ARG GIT_SHA=unknown
ARG BUILD_DATE=""
LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2026: YIQISOFT' \
      org.opencontainers.image.title='nereidlink' \
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
COPY --from=build /out/nereidlink /usr/local/bin/nereidlink
EXPOSE 8080 1883
ENTRYPOINT ["/usr/local/bin/nereidlink"]
CMD ["serve", "-c", "/etc/nereidlink/config.yaml"]
