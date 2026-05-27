package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestAddVendor_OAuth(t *testing.T) {
	s, db := newTestServer(t)
	form := "vendor_id=anthropic-oauth&model=anthropic/claude-sonnet-4-5&cred_kind=oauth"
	req := httptest.NewRequest("POST", "/vendors/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	vs, _ := db.ListVendors(ctx)
	if len(vs) != 1 {
		t.Fatalf("expected 1 vendor, got %d", len(vs))
	}
	if vs[0].CredKind != "oauth" {
		t.Fatalf("CredKind = %q, want oauth", vs[0].CredKind)
	}
	c, err := db.GetCredential(ctx, "vendor", "anthropic-oauth")
	if err != nil {
		t.Fatalf("expected cred row for oauth vendor: %v", err)
	}
	if c.Kind != "oauth" || c.Status != "set" || len(c.Blob) != 0 || len(c.EnvKeys) != 0 {
		t.Fatalf("unexpected cred shape: %+v", c)
	}
}

func TestAddVendor_BYORequiresEnvKey(t *testing.T) {
	s, _ := newTestServer(t)
	form := "vendor_id=byo-no-key&model=foo/bar&cred_kind=byo_key" // no env_key
	req := httptest.NewRequest("POST", "/vendors/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAddVendor_RejectsUnknownCredKind(t *testing.T) {
	s, _ := newTestServer(t)
	form := "vendor_id=v&model=m&cred_kind=banana&env_key=K"
	req := httptest.NewRequest("POST", "/vendors/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func postForm(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestVendorToggle_FlipsEnabled(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m", Enabled: true, Position: 0, CredKind: "byo_key"})

	if rec := postForm(t, s, "/vendors/v1/toggle", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	vs, _ := db.ListVendors(ctx)
	if vs[0].Enabled {
		t.Fatal("toggle should have disabled v1")
	}
	_ = postForm(t, s, "/vendors/v1/toggle", "")
	vs, _ = db.ListVendors(ctx)
	if !vs[0].Enabled {
		t.Fatal("toggle should have re-enabled v1")
	}
}

func TestVendorDelete_RemovesVendorAndCred(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m", Enabled: true, Position: 0, CredKind: "byo_key"})
	_ = db.PutCredential(ctx, store.Credential{Scope: "vendor", ID: "v1", Kind: "byo_key", Status: "set", Blob: []byte("x")})
	until := time.Now().Add(time.Hour)
	_ = db.PutPenalty(ctx, store.Penalty{VendorID: "v1", Status: store.PenaltyCooldown, FailureStreak: 1, CooldownUntil: &until})

	if rec := postForm(t, s, "/vendors/v1/delete", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	vs, _ := db.ListVendors(ctx)
	if len(vs) != 0 {
		t.Fatalf("expected vendor removed: %+v", vs)
	}
	if _, err := db.GetCredential(ctx, "vendor", "v1"); err == nil {
		t.Fatal("credential row should have been removed")
	}
}

func TestVendorClearPenalty_ReturnsToEligible(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m", Enabled: true, Position: 0, CredKind: "byo_key"})
	_ = db.PutPenalty(ctx, store.Penalty{VendorID: "v1", Status: store.PenaltyAuthLocked, FailureStreak: 0})

	if rec := postForm(t, s, "/vendors/v1/clear-penalty", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	pen, _ := db.GetPenalty(ctx, "v1")
	if pen.Status != store.PenaltyEligible {
		t.Fatalf("expected eligible after clear, got %s", pen.Status)
	}
}

func TestVendorReorder_UpAndDown(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "a", Model: "m", Enabled: true, Position: 0})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "b", Model: "m", Enabled: true, Position: 1})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "c", Model: "m", Enabled: true, Position: 2})

	// move b up → [b a c]
	_ = postForm(t, s, "/vendors/reorder", "vendor_id=b&dir=up")
	vs, _ := db.ListVendors(ctx)
	if vs[0].VendorID != "b" || vs[1].VendorID != "a" {
		t.Fatalf("after up, expected [b a c], got %+v", vs)
	}
	// move a down → [b c a]
	_ = postForm(t, s, "/vendors/reorder", "vendor_id=a&dir=down")
	vs, _ = db.ListVendors(ctx)
	if vs[0].VendorID != "b" || vs[1].VendorID != "c" || vs[2].VendorID != "a" {
		t.Fatalf("after down, expected [b c a], got %+v", vs)
	}
}

func TestVendorReorder_UnknownVendorIsNoop(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "a", Model: "m", Enabled: true, Position: 0})
	if rec := postForm(t, s, "/vendors/reorder", "vendor_id=does-not-exist&dir=up"); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	vs, _ := db.ListVendors(ctx)
	if len(vs) != 1 || vs[0].VendorID != "a" {
		t.Fatalf("unexpected change: %+v", vs)
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
