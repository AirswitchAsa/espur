package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

func newTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secrets.GenerateIdentity()
	v, _ := secrets.New(key)
	pool := vendor.New(db, v)
	ts := transcript.NewStore(dir)
	return New(db, v, pool, ts), db
}

func TestHomePage(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Status") {
		t.Fatalf("missing Status: %s", rec.Body.String())
	}
}

func TestAddVendor_ThenList(t *testing.T) {
	s, _ := newTestServer(t)
	form := "vendor_id=deepseek-api&model=deepseek/deepseek-chat&env_key=DEEPSEEK_API_KEY"
	req := httptest.NewRequest("POST", "/vendors/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/vendors", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "deepseek-api") {
		t.Fatal("vendor not listed")
	}
}

type fakeAdapter struct {
	platform string
	healthy  bool
}

func (f fakeAdapter) Platform() string { return f.platform }
func (f fakeAdapter) Healthy() bool    { return f.healthy }

func TestHealthz_AllUp(t *testing.T) {
	s, _ := newTestServer(t)
	s.RegisterAdapter(fakeAdapter{"discord", true})
	s.RegisterAdapter(fakeAdapter{"wechat", true})
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) ||
		!strings.Contains(rec.Body.String(), `"platform":"discord"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestHealthz_AdapterDown(t *testing.T) {
	s, _ := newTestServer(t)
	s.RegisterAdapter(fakeAdapter{"discord", false})
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestHealthz_NoAdapters(t *testing.T) {
	// Default to OK if no adapter registered — Espur itself is responding.
	s, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestSetKey_RoundTrip(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "v1", Model: "m1", Enabled: true, Position: 0, CredKind: "byo_key",
	})
	_ = db.PutCredential(ctx, store.Credential{
		Scope: "vendor", ID: "v1", Kind: "byo_key", Status: "missing",
		EnvKeys: []string{"V1_KEY"},
	})

	form := "env_key=V1_KEY&key=secret-123"
	req := httptest.NewRequest("POST", "/vendors/v1/key", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d body=%s", rec.Code, rec.Body.String())
	}

	c, err := db.GetCredential(ctx, "vendor", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "set" || len(c.Blob) == 0 {
		t.Fatalf("credential not stored: %+v", c)
	}
	if strings.Contains(string(c.Blob), "secret-123") {
		t.Fatal("plaintext leaked into blob")
	}
	// blob must decrypt back to the plaintext.
	got, err := s.vault.Decrypt(c.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "secret-123" {
		t.Fatalf("decrypt mismatch: %q", got)
	}
}
