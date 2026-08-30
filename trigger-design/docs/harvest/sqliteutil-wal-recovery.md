---
title: "Harvest: sqliteutil — WAL recovery"
status: accepted
date: 2026-08-29
tags: [harvest, sqlite, durability, transplant]
source: agent-minder internal/sqliteutil (recover.go, recover_test.go)
related: "[[0002-local-sqlite-source-of-truth]]"
---

# Harvest: `internal/sqliteutil`

~170 lines, two files (code + tests), zero deps beyond `sqlx` + `modernc.org/sqlite`.
Transplant as-is; only the import path changes.

## What it provides

- **`OpenWithRecovery(dbPath, dsn)`** — open, health-check, auto-recover once, retry.
- **`CheckAndRecoverContext(ctx, db, dbPath)`** — ping, then `PRAGMA integrity_check`
  (ping alone misses corrupt WAL state). On `disk I/O error`: close, delete stale
  `-shm`/`-wal`, return `(false, nil)` meaning "caller must reopen".
- **`ConsumeRecoveryMarker(dbPath)`** — read-and-delete sidecar marker (bool result).

## Hardening that must survive any rewrite

1. **Pool config is part of the fix**: `SetMaxOpenConns(1)` (single-writer, prevents
   SQLITE_BUSY) + `SetConnMaxIdleTime(5m)` (recycles connections after laptop sleep).
2. **Detection is two-tier**: ping is cheap; integrity_check catches corrupt WAL state
   that ping misses.
3. **IO-error matching is stringly-typed by necessity**: `disk I/O error`, `(10)`,
   `SQLITE_IOERR` — modernc.org/sqlite errors don't unwrap to a code type.

## The subtle one: WAL deletion can truncate committed history

Removing a `-wal` discards committed-but-uncheckpointed transactions (Expedition IV F6).
So `removeStaleFiles` writes a `<db>.wal-recovered` marker **only when a `-wal` was
deleted** (not for `-shm`-only removal — that's not a history risk). `db.Open` consumes
the marker and rotates the `event_log_meta.epoch`, forcing clients to discard cursors
and resync instead of silently missing events.

**Transplant contract:** if the target has a durable event log, it must call
`ConsumeRecoveryMarker` during open and rotate its epoch; otherwise recovery is silent
history loss.

## Test coverage (acceptance gate)

Healthy open; garbage `-shm` recovered with data intact; stale-file removal; marker
written only on `-wal` removal; marker consumed once; no-op on missing files; IO-error
matcher table. Run with `go test ./internal/sqliteutil/...` — they need no other package.
