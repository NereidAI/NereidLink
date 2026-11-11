# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

# NereidLink requires PostgreSQL with the TimescaleDB extension (migration
# 0001 runs CREATE EXTENSION timescaledb). The default bundled image is the
# official TimescaleDB image for PostgreSQL 16.
#
# The official image ships TimescaleDB Community Edition under the Timescale
# License (TSL): free to run and to redistribute embedded in a value-added
# product, but it must not be offered as a hosted/managed TimescaleDB service
# and its copyright notices must be retained. Migrations 0024/0036 require
# these TSL-only features (continuous aggregates, compression, retention).
# See the root THIRD_PARTY_NOTICES file (section 1) for the full terms.
#
# The chart now references this upstream image directly; rebuild a custom
# variant only when extra layers are needed, tag it under the suite's public
# GHCR namespace, and publish it to an internal registry with the
# NereidOverview harbor-sync.sh tool (treaty §18), e.g.:
#   docker build -f deploy/images/postgres/Dockerfile \
#       -t ghcr.io/nereidai/postgres:16-timescale .
#
# Bring your own managed PostgreSQL instead by setting
# postgresql.enabled=false and overriding the DSN in the rendered config.
FROM timescale/timescaledb:2.17.2-pg16
ARG VERSION=0.0.0-dev
ARG GIT_SHA=unknown
ARG BUILD_DATE=""
LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2026: YIQISOFT' \
      org.opencontainers.image.title='nereidlink-postgres' \
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

# Preload TimescaleDB so hypertable creation works without a manual ALTER.
ENV POSTGRES_INITDB_ARGS="--data-checksums"
