<!--
 Copyright (C) 2026 YIQISOFT
 SPDX-License-Identifier: Apache-2.0
-->

# Backup and Restore Runbook

This runbook covers the backup boundary in ADR-0004. NereidLink provides
scripts and a doctor advisory; it does not perform managed backups or promise
one-click restore. Practice the restore path before relying on the backup.

## What to back up

- **PostgreSQL**: entity tables, rules, users and TimescaleDB hypertables.
- **NATS JetStream**: buffered stream data and consumer state.

Back up both components. PostgreSQL contains the durable platform state, while
JetStream contains in-flight operational messages that can matter during
recovery.

## PostgreSQL backup

Run with a `pg_dump` binary whose major version is at least the server's:

```bash
DATABASE_URL='postgres://nereidlink:nereidlink@127.0.0.1:5432/nereidlink?sslmode=disable' \
  ./deploy/backup/postgres-backup.sh /var/backups/nereidlink
```

The script writes a custom-format dump that includes TimescaleDB extension and
continuous aggregate definitions. Encrypt and copy the output off the database
host.

Record a marker for doctor after a successful backup:

```bash
nereidlink backup mark -c configs/prod.yaml --scope postgres
```

The backup script records the marker automatically when `NL_CONFIG` points at
the platform configuration.

## PostgreSQL restore

Prefer restoring into an empty database with TimescaleDB installed:

```bash
DATABASE_URL='postgres://nereidlink:nereidlink@127.0.0.1:5432/recovery?sslmode=disable' \
  ./deploy/backup/postgres-restore.sh /var/backups/nereidlink/<backup>.dump
```

The restore runs in one transaction, so a failure rolls back rather than
leaving a partially recovered database. Stop application traffic during
restore.

## NATS JetStream backup

```bash
NATS_URL='nats://127.0.0.1:4222' \
  ./deploy/backup/nats-backup.sh /var/backups/nereidlink
```

The script checks stream health before taking the account backup and fails on
critical warnings. Set `NL_CONFIG` to record the NATS marker.

## NATS JetStream restore

```bash
nats backup restore account /var/backups/nereidlink/<backup-directory> \
  --server nats://127.0.0.1:4222
```

## Verify recovery

After restoring both components:

```bash
nereidlink doctor -c configs/prod.yaml
```

Expected checks include the TimescaleDB extension, clean migration ledger,
telemetry hypertable, compression settings and policies, both continuous
aggregates and the latest backup marker. A missing or aged marker is an
advisory only; absent required storage objects is a failure.
