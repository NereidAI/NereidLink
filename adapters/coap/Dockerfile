# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

# Multi-stage image for the CoAP southbound module. The build context is
# the repository root so the adapter can reuse internal/ packages:
#   docker build -f adapters/coap/Dockerfile -t nereidlink/coap:VERSION .

# Compile on the build host architecture and cross-compile with Go's native
# GOOS/GOARCH via TARGETOS/TARGETARCH so the arm64 variant never builds under
# QEMU. Only the lightweight runtime stage runs foreign-architecture steps.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build

# HTTP_PROXY/HTTPS_PROXY are passed through automatically by BuildKit when
# set in the build environment. GOPROXY is injected as a build arg (treaty
# §17); empty makes the Go toolchain use its pristine upstream default.
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG GOPROXY=""

# Source is copied as owned by the build context root.
WORKDIR /src
COPY . .

# Default is the rolling dev channel; the release/dev workflows inject bare
# SemVer (0.0.1) or 0.0.0-dev. TARGETOS/TARGETARCH are BuildKit platform args.
ARG VERSION=0.0.0-dev
ARG TARGETOS=linux
ARG TARGETARCH

# Build a static binary; CGO is unused by the adapter and its NATS client.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    GOPROXY=${GOPROXY} go build \
    -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/coap ./adapters/coap/cmd

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=0.0.0-dev
ARG GIT_SHA=unknown
ARG BUILD_DATE=""
LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2026: YIQISOFT' \
      org.opencontainers.image.title='nereidlink-coap' \
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

# The adapter reads its YAML configuration from this path, supplied via a
# bind mount or derived image.
COPY --from=build /out/coap /usr/local/bin/coap
ENTRYPOINT ["/usr/local/bin/coap"]
CMD ["-c", "/etc/nereidlink/coap.yaml"]

# Plain CoAP UDP; DTLS 5684 is added once the DTLS listener lands.
EXPOSE 5683/udp
