package store

import (
	"context"
	"testing"
)

func TestConnectionsCRUD(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if n, err := db.CountConnections(ctx); err != nil || n != 0 {
		t.Fatalf("fresh count=%d err=%v", n, err)
	}

	c := Connection{ID: "wechat:abc", Kind: "wechat", Label: "WeChat", Enabled: true, Config: `{"base_url":"x"}`}
	if err := db.PutConnection(ctx, c); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := db.GetConnection(ctx, "wechat:abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != "wechat" || got.Label != "WeChat" || !got.Enabled || got.Config != `{"base_url":"x"}` {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}

	if err := db.SetConnectionEnabled(ctx, "wechat:abc", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := db.SetConnectionLabel(ctx, "wechat:abc", "WeChat bot-1"); err != nil {
		t.Fatalf("label: %v", err)
	}
	got, _ = db.GetConnection(ctx, "wechat:abc")
	if got.Enabled {
		t.Fatal("should be disabled")
	}
	if got.Label != "WeChat bot-1" {
		t.Fatalf("label=%q", got.Label)
	}

	// Delete also removes the connection's stored secret.
	if err := db.PutCredential(ctx, Credential{Scope: ConnScope, ID: "wechat:abc", Kind: "platform_session", Status: "set", Blob: []byte("blob")}); err != nil {
		t.Fatalf("put cred: %v", err)
	}
	if err := db.DeleteConnection(ctx, "wechat:abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetConnection(ctx, "wechat:abc"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := db.GetCredential(ctx, ConnScope, "wechat:abc"); err != ErrNotFound {
		t.Fatalf("secret should be deleted with the connection, got %v", err)
	}
}

func TestListConnectionsOrdered(t *testing.T) {
	ctx := context.Background()
	db, _ := Open(":memory:")
	defer db.Close()
	_ = db.PutConnection(ctx, Connection{ID: "discord", Kind: "discord", Enabled: true})
	_ = db.PutConnection(ctx, Connection{ID: "wechat:1", Kind: "wechat", Enabled: false})
	list, err := db.ListConnections(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d want 2", len(list))
	}
}
