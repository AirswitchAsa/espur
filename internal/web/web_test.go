package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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
	for _, want := range []string{
		"es-phead__title", "Home", "vendors", "pool status",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("home page missing %q in body", want)
		}
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
	form := "vendor_id=anthropic-oauth&model=anthropic/claude-sonnet-4-6&cred_kind=oauth"
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

func TestThreads_ListAndDetail(t *testing.T) {
	s, _ := newTestServer(t)
	platform, thread := "discord", "chan-xyz"

	if err := s.ts.Append(platform, thread, transcript.Record{
		Kind:   transcript.KindUser,
		Author: transcript.Author{ID: "u1", Label: "alice"},
		Body:   "hello world",
	}); err != nil {
		t.Fatal(err)
	}
	dir := s.ts.ThreadDir(platform, thread)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# memory index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fact_foo.md"), []byte("detail"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/threads", nil))
	if rec.Code != 200 {
		t.Fatalf("threads list status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), platform) {
		t.Fatalf("threads list missing platform: %s", rec.Body.String())
	}

	enc := filepath.Base(dir)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/threads/"+platform+"/"+enc, nil))
	if rec.Code != 200 {
		t.Fatalf("thread detail status %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"memory index", "fact_foo.md", "hello world"} {
		if !strings.Contains(body, want) {
			t.Fatalf("thread detail missing %q: %s", want, body)
		}
	}
}

func TestStaticAssetsServed(t *testing.T) {
	s, _ := newTestServer(t)
	for _, p := range []string{"/static/css/espur.css", "/static/js/app.js"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", p, rec.Code)
		}
		if rec.Body.Len() < 100 {
			t.Fatalf("GET %s suspiciously small: %d bytes", p, rec.Body.Len())
		}
	}
}

func TestSettingsPage(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/settings", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Settings", "Transcript tail", "Master key reminder"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("settings missing %q", want)
		}
	}
}

func TestHealthHumanPage(t *testing.T) {
	s, _ := newTestServer(t)
	s.RegisterAdapter(fakeAdapter{"discord", true})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	// JSON is HTML-escaped (&#34; for "); look for tokens that survive escaping.
	for _, want := range []string{"All systems operational", "raw /healthz", "uptime", "adapters"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("health page missing %q in body", want)
		}
	}
}

func TestVendorReorderAll_PersistsOrder(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "a", Model: "m", Enabled: true, Position: 0})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "b", Model: "m", Enabled: true, Position: 1})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "c", Model: "m", Enabled: true, Position: 2})

	rec := postForm(t, s, "/vendors/reorder-all", "ids=c&ids=a&ids=b")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	vs, _ := db.ListVendors(ctx)
	got := []string{vs[0].VendorID, vs[1].VendorID, vs[2].VendorID}
	want := []string{"c", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestThreadWipeMemory(t *testing.T) {
	s, _ := newTestServer(t)
	platform, thread := "discord", "thread-x"
	if err := s.ts.Append(platform, thread, transcript.Record{
		Kind: transcript.KindUser, Author: transcript.Author{Label: "alice"}, Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	dir := s.ts.ThreadDir(platform, thread)
	enc := filepath.Base(dir)
	agentsPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, s, "/threads/"+platform+"/"+enc+"/wipe-memory", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := os.ReadFile(agentsPath)
	if len(body) != 0 {
		t.Fatalf("AGENTS.md not wiped: %q", body)
	}
}

func TestThreadDelete_RemovesWorkdir(t *testing.T) {
	s, _ := newTestServer(t)
	platform, thread := "discord", "thread-del"
	if err := s.ts.Append(platform, thread, transcript.Record{
		Kind: transcript.KindUser, Author: transcript.Author{Label: "x"}, Body: "y",
	}); err != nil {
		t.Fatal(err)
	}
	dir := s.ts.ThreadDir(platform, thread)
	enc := filepath.Base(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("workdir missing: %v", err)
	}
	rec := postForm(t, s, "/threads/"+platform+"/"+enc+"/delete", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("workdir still exists after delete: %v", err)
	}
}

func TestVendorsPage_ShowsCatalogProviders(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/vendors", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	// catalog must populate the add-vendor drawer's <optgroup>s
	for _, want := range []string{"Anthropic", "OpenAI", "model-select", "Add vendor"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("vendors page missing %q", want)
		}
	}
}

func TestAddVendor_RejectsUnknownModel(t *testing.T) {
	s, _ := newTestServer(t)
	form := "vendor_id=x&model=mystery/foo&cred_kind=byo_key"
	rec := postForm(t, s, "/vendors/add", form)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "curated catalog") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestAddVendor_AutoFillsEnvKey(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	form := "vendor_id=anth-byo&model=anthropic/claude-haiku-4-5&cred_kind=byo_key"
	rec := postForm(t, s, "/vendors/add", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	c, err := db.GetCredential(ctx, "vendor", "anth-byo")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnvKeys) != 1 || c.EnvKeys[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("expected env_key auto-filled, got %+v", c.EnvKeys)
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
