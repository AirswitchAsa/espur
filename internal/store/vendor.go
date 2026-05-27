package store

import (
	"context"
	"database/sql"
	"time"
)

// Vendor is one row in the priority list. See docs/specs/vendor-pool.dog.md.
type Vendor struct {
	VendorID string
	Model    string
	Enabled  bool
	Position int
	CredKind string // byo_key | oauth | ...
}

// PenaltyStatus matches docs/specs/vendor-pool.dog.md.
type PenaltyStatus string

const (
	PenaltyEligible   PenaltyStatus = "eligible"
	PenaltyCooldown   PenaltyStatus = "cooldown"
	PenaltyAuthLocked PenaltyStatus = "auth_locked"
)

// Penalty mirrors the penalty-box row.
type Penalty struct {
	VendorID      string
	Status        PenaltyStatus
	FailureStreak int
	CooldownUntil *time.Time
	UpdatedAt     time.Time
}

// ListVendors returns enabled+disabled vendors ordered by ascending position.
func (d *DB) ListVendors(ctx context.Context) ([]Vendor, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT vendor_id, model, enabled, position, cred_kind FROM vendors ORDER BY position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vendor
	for rows.Next() {
		var v Vendor
		var enabled int
		if err := rows.Scan(&v.VendorID, &v.Model, &enabled, &v.Position, &v.CredKind); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpsertVendor inserts or updates a vendor row (position is preserved on
// update unless explicitly overwritten by ReorderVendors).
func (d *DB) UpsertVendor(ctx context.Context, v Vendor) error {
	now := time.Now().Unix()
	en := 0
	if v.Enabled {
		en = 1
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO vendors(vendor_id, model, enabled, position, cred_kind, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(vendor_id) DO UPDATE SET
			model      = excluded.model,
			enabled    = excluded.enabled,
			cred_kind  = excluded.cred_kind,
			updated_at = excluded.updated_at`,
		v.VendorID, v.Model, en, v.Position, v.CredKind, now, now)
	return err
}

// DeleteVendor removes a vendor row and its credential + penalty.
func (d *DB) DeleteVendor(ctx context.Context, vendorID string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM vendors WHERE vendor_id = ?`, []any{vendorID}},
		{`DELETE FROM credentials WHERE scope = 'vendor' AND id = ?`, []any{vendorID}},
		{`DELETE FROM penalty WHERE vendor_id = ?`, []any{vendorID}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReorderVendors writes the new priority list atomically.
func (d *DB) ReorderVendors(ctx context.Context, orderedVendorIDs []string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for i, id := range orderedVendorIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE vendors SET position = ?, updated_at = ? WHERE vendor_id = ?`,
			i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetPenalty returns the penalty row, creating an eligible row if missing.
func (d *DB) GetPenalty(ctx context.Context, vendorID string) (Penalty, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT vendor_id, status, failure_streak, cooldown_until, updated_at FROM penalty WHERE vendor_id = ?`,
		vendorID)
	var p Penalty
	var status string
	var cooldown sql.NullInt64
	var updated int64
	if err := row.Scan(&p.VendorID, &status, &p.FailureStreak, &cooldown, &updated); err != nil {
		if err == sql.ErrNoRows {
			return Penalty{VendorID: vendorID, Status: PenaltyEligible, UpdatedAt: time.Now()}, nil
		}
		return Penalty{}, err
	}
	p.Status = PenaltyStatus(status)
	p.UpdatedAt = time.Unix(updated, 0)
	if cooldown.Valid {
		t := time.Unix(cooldown.Int64, 0)
		p.CooldownUntil = &t
	}
	return p, nil
}

// PutPenalty upserts a penalty row.
func (d *DB) PutPenalty(ctx context.Context, p Penalty) error {
	var cooldown sql.NullInt64
	if p.CooldownUntil != nil {
		cooldown = sql.NullInt64{Int64: p.CooldownUntil.Unix(), Valid: true}
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO penalty(vendor_id, status, failure_streak, cooldown_until, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(vendor_id) DO UPDATE SET
			status         = excluded.status,
			failure_streak = excluded.failure_streak,
			cooldown_until = excluded.cooldown_until,
			updated_at     = excluded.updated_at`,
		p.VendorID, string(p.Status), p.FailureStreak, cooldown, time.Now().Unix())
	return err
}
