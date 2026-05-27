package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVendorCRUD(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	if err := db.UpsertVendor(ctx, Vendor{
		VendorID: "deepseek-api", Model: "deepseek/deepseek-chat",
		Enabled: true, Position: 0, CredKind: "byo_key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertVendor(ctx, Vendor{
		VendorID: "claude-oauth", Model: "anthropic/claude-haiku-4-5",
		Enabled: true, Position: 1, CredKind: "oauth",
	}); err != nil {
		t.Fatal(err)
	}

	vs, err := db.ListVendors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].VendorID != "deepseek-api" || vs[1].VendorID != "claude-oauth" {
		t.Fatalf("order wrong: %+v", vs)
	}

	if err := db.ReorderVendors(ctx, []string{"claude-oauth", "deepseek-api"}); err != nil {
		t.Fatal(err)
	}
	vs, _ = db.ListVendors(ctx)
	if vs[0].VendorID != "claude-oauth" {
		t.Fatalf("reorder didn't take: %+v", vs)
	}

	if err := db.DeleteVendor(ctx, "deepseek-api"); err != nil {
		t.Fatal(err)
	}
	vs, _ = db.ListVendors(ctx)
	if len(vs) != 1 {
		t.Fatalf("delete didn't take: %+v", vs)
	}
}

func TestPenaltyDefault(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	p, err := db.GetPenalty(ctx, "anything")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != PenaltyEligible {
		t.Fatalf("default status should be eligible, got %s", p.Status)
	}

	until := time.Now().Add(30 * time.Second)
	if err := db.PutPenalty(ctx, Penalty{
		VendorID: "anything", Status: PenaltyCooldown, FailureStreak: 1, CooldownUntil: &until,
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = db.GetPenalty(ctx, "anything")
	if p.Status != PenaltyCooldown || p.FailureStreak != 1 || p.CooldownUntil == nil {
		t.Fatalf("put didn't take: %+v", p)
	}
}

func TestDedup(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	first, err := db.SeenMessage(ctx, "discord", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Fatal("first sighting should report true")
	}
	first, err = db.SeenMessage(ctx, "discord", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if first {
		t.Fatal("second sighting should report false")
	}
}

func TestCredentialPut(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	if err := db.PutCredential(ctx, Credential{
		Scope: "vendor", ID: "deepseek-api", Kind: "byo_key", Status: "set",
		Blob: []byte("encrypted-bytes"), EnvKeys: []string{"DEEPSEEK_API_KEY"},
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetCredential(ctx, "vendor", "deepseek-api")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "set" || string(c.Blob) != "encrypted-bytes" || len(c.EnvKeys) != 1 || c.EnvKeys[0] != "DEEPSEEK_API_KEY" {
		t.Fatalf("get mismatch: %+v", c)
	}
	blob, err := db.AnyCredentialBlob(ctx)
	if err != nil || string(blob) != "encrypted-bytes" {
		t.Fatalf("AnyCredentialBlob: %v / %q", err, blob)
	}
}
