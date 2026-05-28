package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Credential is metadata for one stored secret. The encrypted blob lives in
// the database; callers only see plaintext via secrets.Decrypt at use time.
type Credential struct {
	Scope     string // vendor | adapter | oauth
	ID        string
	Kind      string   // byo_key | oauth | platform_token
	Status    string   // set | missing | expired | revoked
	Blob      []byte   // age ciphertext of the single secret value; never plaintext
	EnvKeys   []string // env var name(s) to expose that one secret under (aliases, not distinct values)
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetCredential returns the row for (scope, id), or ErrNotFound.
func (d *DB) GetCredential(ctx context.Context, scope, id string) (Credential, error) {
	row := d.sql.QueryRowContext(ctx, `
		SELECT scope, id, kind, status, blob, env_keys, created_at, updated_at
		FROM credentials WHERE scope = ? AND id = ?`, scope, id)
	var c Credential
	var envKeys string
	var created, updated int64
	if err := row.Scan(&c.Scope, &c.ID, &c.Kind, &c.Status, &c.Blob, &envKeys, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return Credential{}, ErrNotFound
		}
		return Credential{}, err
	}
	if envKeys != "" {
		c.EnvKeys = strings.Split(envKeys, ",")
	}
	c.CreatedAt = time.Unix(created, 0)
	c.UpdatedAt = time.Unix(updated, 0)
	return c, nil
}

// PutCredential upserts the row. Caller is responsible for encryption.
func (d *DB) PutCredential(ctx context.Context, c Credential) error {
	now := time.Now().Unix()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Unix(now, 0)
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO credentials(scope, id, kind, status, blob, env_keys, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, id) DO UPDATE SET
			kind       = excluded.kind,
			status     = excluded.status,
			blob       = excluded.blob,
			env_keys   = excluded.env_keys,
			updated_at = excluded.updated_at`,
		c.Scope, c.ID, c.Kind, c.Status, c.Blob, strings.Join(c.EnvKeys, ","),
		c.CreatedAt.Unix(), now)
	return err
}

// AnyCredentialBlob returns one encrypted blob from the table, used by the
// secrets self-test at boot. Returns ErrNotFound when the table is empty.
func (d *DB) AnyCredentialBlob(ctx context.Context) ([]byte, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT blob FROM credentials WHERE blob IS NOT NULL AND length(blob) > 0 LIMIT 1`)
	var blob []byte
	if err := row.Scan(&blob); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return blob, nil
}
