#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# Restore a logical PostgreSQL backup produced by postgres-backup.sh.
#
# The safest target is an empty database with the TimescaleDB extension
# available. Restoring over a populated database is possible but more fragile
# because TimescaleDB object dependencies must be dropped in the correct order;
# prefer that path only during a practiced recovery drill.

set -euo pipefail

backup_file=${1:?"usage: postgres-restore.sh BACKUP_FILE"}
dsn=${DATABASE_URL:?"DATABASE_URL must point at the target PostgreSQL database"}

# --clean and --if-exists replace existing objects without failing because an
# object was absent. Single-transaction restore makes a partial recovery a
# rollback rather than a half-restored database.
pg_restore \
    --dbname="$dsn" \
    --clean \
    --if-exists \
    --no-owner \
    --no-privileges \
    --single-transaction \
    "$backup_file"

echo "restore completed: $backup_file"
