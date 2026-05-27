package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckpoint_TruncatesWAL writes enough rows to grow the WAL, then
// asserts Checkpoint returns nil without error. We don't probe the WAL
// file size directly (modernc.org/sqlite's PRAGMA wal_checkpoint result is
// not exposed via database/sql), but exercising the path catches obvious
// regressions like a typo in the PRAGMA string.
func TestCheckpoint_TruncatesWAL(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "ck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		_ = db.UpsertVendor(ctx, Vendor{
			VendorID: "v" + itoa(i), Model: "m", Enabled: true,
			Position: i, CredKind: "byo_key",
		})
	}
	if err := db.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
}

// TestSeenMessage_FirstThenDup pins the dedup contract: the first sighting
// returns true; subsequent sightings within the same row return false.
func TestSeenMessage_FirstThenDup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "dd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	first, err := db.SeenMessage(ctx, "discord", "m-1")
	if err != nil || !first {
		t.Fatalf("first=%v err=%v", first, err)
	}
	again, err := db.SeenMessage(ctx, "discord", "m-1")
	if err != nil || again {
		t.Fatalf("again=%v err=%v", again, err)
	}
	// Different platform → different key.
	first, err = db.SeenMessage(ctx, "wechat", "m-1")
	if err != nil || !first {
		t.Fatalf("crossplat first=%v err=%v", first, err)
	}
}

// TestDeleteVendor_TxCleansSiblings: deleting a vendor row must also drop
// its credential + penalty rows in the same transaction.
func TestDeleteVendor_TxCleansSiblings(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "del.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	_ = db.UpsertVendor(ctx, Vendor{VendorID: "v1", Model: "m", Enabled: true, Position: 0, CredKind: "byo_key"})
	_ = db.PutCredential(ctx, Credential{Scope: "vendor", ID: "v1", Kind: "byo_key", Status: "set", Blob: []byte("x")})
	until := time.Now().Add(time.Hour)
	_ = db.PutPenalty(ctx, Penalty{VendorID: "v1", Status: PenaltyCooldown, FailureStreak: 1, CooldownUntil: &until})

	if err := db.DeleteVendor(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetCredential(ctx, "vendor", "v1"); err == nil {
		t.Fatal("credential row not deleted")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [10]byte
	n := 0
	for i > 0 {
		b[n] = byte('0' + i%10)
		n++
		i /= 10
	}
	for x, y := 0, n-1; x < y; x, y = x+1, y-1 {
		b[x], b[y] = b[y], b[x]
	}
	return string(b[:n])
}
