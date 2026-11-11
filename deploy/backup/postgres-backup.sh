#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# Create a logical PostgreSQL backup including TimescaleDB objects.
#
# The platform does not provide managed backups. This script wraps pg_dump
# with the options required for a portable NereidLink backup and optionally
# records a doctor marker after the dump finishes successfully. The caller is
# responsible for encrypting and copying the output outside the database host.

set -euo pipefail

output_dir=${1:?"usage: postgres-backup.sh OUTPUT_DIR"}
dsn=${DATABASE_URL:?"DATABASE_URL must point at the PostgreSQL database"}

mkdir -p "$output_dir"
output_file="$output_dir/nereidlink-postgres-$(date -u +%Y%m%dT%H%M%SZ).dump"

# pg_dump must be at least the server major version. The custom format is
# compressed, selective on restore, and includes extension definitions such as
# TimescaleDB. Owner and privilege statements are omitted for portability
# across environments that do not share role OIDs.
pg_dump \
    --dbname="$dsn" \
    --file="$output_file" \
    --format=custom \
    --no-owner \
    --no-privileges

echo "backup written: $output_file"

if [[ -n ${NL_CONFIG:-} ]]; then
    nl_bin=${NL_BIN:-nereidlink}
    "$nl_bin" backup mark -c "$NL_CONFIG" --scope postgres
fi
