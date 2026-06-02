package store

import (
	"context"
	"database/sql"
	"time"
)

// Connection is one admin-configured adapter instance. The secret (Discord bot
// token, or the WeChat iLink session blob) is stored separately in the
// credentials table under scope "connection" with this row's ID; it is never
// part of this struct. See docs/specs/adapter.dog.md "Connection identity".
type Connection struct {
	ID        string // composite routing key: "kind:<gen>" or bare "kind" (legacy)
	Kind      string // "discord" | "wechat"
	Label     string // human display name, derived from the platform after connect
	Enabled   bool
	Config    string // small non-secret JSON (e.g. {"base_url":"..."})
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ConnScope is the credentials scope under which a connection's secret lives.
const ConnScope = "connection"

// ListConnections returns all connections ordered by creation time.
func (d *DB) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, kind, label, enabled, config, created_at, updated_at
		FROM connections ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConnection returns one connection by id, or ErrNotFound.
func (d *DB) GetConnection(ctx context.Context, id string) (Connection, error) {
	row := d.sql.QueryRowContext(ctx, `
		SELECT id, kind, label, enabled, config, created_at, updated_at
		FROM connections WHERE id = ?`, id)
	c, err := scanConnection(row)
	if err == sql.ErrNoRows {
		return Connection{}, ErrNotFound
	}
	return c, err
}

// CountConnections returns the number of configured connections. Used by the
// boot-time legacy migration to decide whether to seed from env.
func (d *DB) CountConnections(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM connections`).Scan(&n)
	return n, err
}

// PutConnection upserts a connection row (metadata only; the secret is written
// via PutCredential under scope ConnScope).
func (d *DB) PutConnection(ctx context.Context, c Connection) error {
	now := time.Now().Unix()
	created := now
	if !c.CreatedAt.IsZero() {
		created = c.CreatedAt.Unix()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO connections(id, kind, label, enabled, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind       = excluded.kind,
			label      = excluded.label,
			enabled    = excluded.enabled,
			config     = excluded.config,
			updated_at = excluded.updated_at`,
		c.ID, c.Kind, c.Label, boolToInt(c.Enabled), c.Config, created, now)
	return err
}

// SetConnectionEnabled flips the enabled flag.
func (d *DB) SetConnectionEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE connections SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), time.Now().Unix(), id)
	return err
}

// SetConnectionLabel updates the human display label (e.g. after login resolves
// the platform's bot username / id).
func (d *DB) SetConnectionLabel(ctx context.Context, id, label string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE connections SET label = ?, updated_at = ? WHERE id = ?`,
		label, time.Now().Unix(), id)
	return err
}

// DeleteConnection removes the connection row and its stored secret.
func (d *DB) DeleteConnection(ctx context.Context, id string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM credentials WHERE scope = ? AND id = ?`, ConnScope, id); err != nil {
		return err
	}
	return tx.Commit()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanConnection(s rowScanner) (Connection, error) {
	var c Connection
	var enabled int
	var created, updated int64
	if err := s.Scan(&c.ID, &c.Kind, &c.Label, &enabled, &c.Config, &created, &updated); err != nil {
		return Connection{}, err
	}
	c.Enabled = enabled != 0
	c.CreatedAt = time.Unix(created, 0)
	c.UpdatedAt = time.Unix(updated, 0)
	return c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
