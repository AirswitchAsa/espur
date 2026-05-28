package store

import (
	"context"
	"time"
)

// SeenMessage records a (platform, message_id) so future redeliveries can be
// dropped. Returns true if this is the first time we've seen it, false if it
// was already present (i.e. a duplicate).
//
// Spec: trigger.dog.md — dedup by (platform, message_id), persisted so
// adapter restarts don't replay processed messages.
func (d *DB) SeenMessage(ctx context.Context, platform, messageID string) (firstTime bool, err error) {
	res, err := d.sql.ExecContext(ctx,
		`INSERT OR IGNORE INTO dedup(platform, message_id, seen_at) VALUES (?, ?, ?)`,
		platform, messageID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PurgeDedupOlderThan deletes dedup rows older than the given age. Not yet
// wired to a caller: the retention window is an open decision (see
// trigger.dog.md). The table is small, so unbounded growth is tolerable until
// that window is pinned.
func (d *DB) PurgeDedupOlderThan(ctx context.Context, age time.Duration) error {
	cutoff := time.Now().Add(-age).Unix()
	_, err := d.sql.ExecContext(ctx, `DELETE FROM dedup WHERE seen_at < ?`, cutoff)
	return err
}
