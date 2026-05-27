// Package store owns Espur's SQLite database: schema, migrations, and the
// concrete queries other packages call. See specs/bootstrap.dog.md for the
// "durable-state inventory" this file is the substrate for.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens the SQLite database at the given path and runs migrations.
// path of ":memory:" gives a fresh in-memory DB (used by tests).
func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = ":memory:"
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	sqldb.SetMaxOpenConns(1) // simplest correct concurrency story for SQLite.
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	db := &DB{sql: sqldb}
	if err := db.migrate(); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

// DB is the typed handle other packages use.
type DB struct{ sql *sql.DB }

func (d *DB) Close() error { return d.sql.Close() }

// Checkpoint forces a WAL checkpoint (TRUNCATE mode) so the database file
// is fully self-contained. Used by the shutdown sequencer per
// specs/shutdown.dog.md "Phase 3 — close resources." Safe to call repeatedly;
// returns an error which the operator usually wants to log at warn (the DB
// is still consistent via WAL semantics even if checkpoint fails).
func (d *DB) Checkpoint(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`)
	return err
}

// SQL exposes the raw handle for tests that need it; ordinary callers use
// the typed methods below.
func (d *DB) SQL() *sql.DB { return d.sql }

var migrations = []string{
	// 1: schema version table is implicit; record applied migrations.
	`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);`,
	// 2: vendors — ordered list. position is sparse; reorders rewrite all rows.
	`CREATE TABLE IF NOT EXISTS vendors (
		vendor_id TEXT PRIMARY KEY,
		model     TEXT NOT NULL,
		enabled   INTEGER NOT NULL DEFAULT 1,
		position  INTEGER NOT NULL,
		cred_kind TEXT NOT NULL DEFAULT 'byo_key',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);`,
	// 3: credentials — encrypted blobs keyed by (scope,id). Plaintext never lands here.
	`CREATE TABLE IF NOT EXISTS credentials (
		scope      TEXT NOT NULL,
		id         TEXT NOT NULL,
		kind       TEXT NOT NULL,
		status     TEXT NOT NULL,
		blob       BLOB,
		env_keys   TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (scope, id)
	);`,
	// 4: penalty box per vendor.
	`CREATE TABLE IF NOT EXISTS penalty (
		vendor_id      TEXT PRIMARY KEY,
		status         TEXT NOT NULL,
		failure_streak INTEGER NOT NULL DEFAULT 0,
		cooldown_until INTEGER,
		updated_at     INTEGER NOT NULL
	);`,
	// 5: message-ID dedup table. (platform, message_id) is unique.
	`CREATE TABLE IF NOT EXISTS dedup (
		platform   TEXT NOT NULL,
		message_id TEXT NOT NULL,
		seen_at    INTEGER NOT NULL,
		PRIMARY KEY (platform, message_id)
	);`,
}

func (d *DB) migrate() error {
	ctx := context.Background()
	for i, stmt := range migrations {
		if _, err := d.sql.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	_, _ = d.sql.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		len(migrations), time.Now().Unix())
	return nil
}

// ErrNotFound is returned by lookups that find no row.
var ErrNotFound = errors.New("store: not found")
