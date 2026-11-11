#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# Back up NATS JetStream streams, their data and consumers.
#
# PostgreSQL is the source of truth for entity state, while JetStream carries
# buffered telemetry, commands and other operational streams. A recovery set is
# incomplete without both. The NATS CLI performs the backup over the protocol,
# so this script only standardizes the safe options and output location.

set -euo pipefail

output_dir=${1:?"usage: nats-backup.sh OUTPUT_DIR"}
server=${NATS_URL:?"NATS_URL must point at the NATS server"}

mkdir -p "$output_dir"
backup_dir="$output_dir/nats-js-$(date -u +%Y%m%dT%H%M%SZ)"

# Check health before snapshotting; fail on critical stream warnings. The
# backup directory must not already contain a prior partial backup.
nats backup account "$backup_dir" \
    --server "$server" \
    --check \
    --critical-warnings \
    --force

echo "JetStream backup written: $backup_dir"

if [[ -n ${NL_CONFIG:-} ]]; then
    nl_bin=${NL_BIN:-nereidlink}
    "$nl_bin" backup mark -c "$NL_CONFIG" --scope nats
fi
