package vendor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punny/espur/internal/opencode"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
)

type fakeInvoker struct {
	results []opencode.Result
	calls   []opencode.Request
}

func (f *fakeInvoker) Invoke(ctx context.Context, req opencode.Request) (opencode.Result, error) {
	f.calls = append(f.calls, req)
	r := f.results[0]
	f.results = f.results[1:]
	return r, nil
}

func newTestPool(t *testing.T, inv Invoker) (*Pool, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secrets.GenerateIdentity()
	v, _ := secrets.New(key)
	p := New(db, v).WithInvoker(inv)
	return p, db
}

func TestRun_SuccessFirstVendor(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{
		{Outcome: opencode.OutcomeSuccess, AssistantText: "hello"},
	}}
	p, db := newTestPool(t, inv)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "v1", Model: "m1", Enabled: true, Position: 0, CredKind: "byo_key",
	})

	res, err := p.Run(ctx, t.TempDir(), "<request>hi</request>", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess || res.Text != "hello" || res.VendorID != "v1" {
		t.Fatalf("got %+v", res)
	}
	if len(inv.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(inv.calls))
	}
}

func TestRun_FallthroughOnRateLimit(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{
		{Outcome: opencode.OutcomeCrash, CrashReason: "no_assistant_text",
			Stdout: `{"error":{"data":{"statusCode":429,"message":"rate limit"}}}`},
		{Outcome: opencode.OutcomeSuccess, AssistantText: "pong"},
	}}
	p, db := newTestPool(t, inv)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m1", Enabled: true, Position: 0})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v2", Model: "m2", Enabled: true, Position: 1})

	res, err := p.Run(ctx, t.TempDir(), "x", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess || res.VendorID != "v2" {
		t.Fatalf("expected v2 success, got %+v", res)
	}
	pen, _ := db.GetPenalty(ctx, "v1")
	if pen.Status != store.PenaltyCooldown {
		t.Fatalf("v1 should be in cooldown, got %s", pen.Status)
	}
}

func TestRun_AuthLocksVendor(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{
		{Outcome: opencode.OutcomeCrash, Stdout: `{"error":{"data":{"statusCode":401,"message":"unauthorized"}}}`},
		{Outcome: opencode.OutcomeSuccess, AssistantText: "fallback"},
	}}
	p, db := newTestPool(t, inv)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m1", Enabled: true, Position: 0})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v2", Model: "m2", Enabled: true, Position: 1})

	res, _ := p.Run(ctx, t.TempDir(), "x", 30*time.Second)
	if res.Outcome != OutcomeSuccess || res.VendorID != "v2" {
		t.Fatalf("expected fallthrough to v2: %+v", res)
	}
	pen, _ := db.GetPenalty(ctx, "v1")
	if pen.Status != store.PenaltyAuthLocked {
		t.Fatalf("expected auth_locked, got %s", pen.Status)
	}
}

func TestRun_TimeoutDoesNotPenalize(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{{Outcome: opencode.OutcomeTimeout}}}
	p, db := newTestPool(t, inv)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m1", Enabled: true, Position: 0})
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v2", Model: "m2", Enabled: true, Position: 1})

	res, _ := p.Run(ctx, t.TempDir(), "x", 30*time.Second)
	if res.Outcome != OutcomeTimeout || res.VendorID != "v1" {
		t.Fatalf("expected timeout on v1, got %+v", res)
	}
	if len(inv.calls) != 1 {
		t.Fatalf("timeout must not fall through; got %d calls", len(inv.calls))
	}
	pen, _ := db.GetPenalty(ctx, "v1")
	if pen.Status != store.PenaltyEligible {
		t.Fatalf("timeout must not mutate penalty; got %s", pen.Status)
	}
}

func TestRun_AllDrained_NothingEligible(t *testing.T) {
	p, db := newTestPool(t, &fakeInvoker{})
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{VendorID: "v1", Model: "m1", Enabled: true, Position: 0})
	until := time.Now().Add(time.Hour)
	_ = db.PutPenalty(ctx, store.Penalty{VendorID: "v1", Status: store.PenaltyCooldown, FailureStreak: 3, CooldownUntil: &until})

	res, _ := p.Run(ctx, t.TempDir(), "x", 30*time.Second)
	if res.Outcome != OutcomeAllDrained {
		t.Fatalf("expected drained, got %+v", res)
	}
	if len(res.Penalized) != 1 || res.Penalized[0].VendorID != "v1" {
		t.Fatalf("expected v1 in penalized list: %+v", res.Penalized)
	}
}

func TestClassify_Buckets(t *testing.T) {
	// Inputs mirror real `opencode run --format json` error events: a JSON line
	// carrying a top-level `error` object. Only this envelope is classified.
	errEvent := func(data string) string {
		return `{"type":"error","sessionID":"ses_x","error":{"name":"UnknownError","data":` + data + `}}`
	}
	cases := []struct {
		name string
		in   string
		want FailureClass
	}{
		{"rate", errEvent(`{"message":"rate limit"}`), ClassRateLimit},
		{"429", errEvent(`{"statusCode":429,"message":"too many"}`), ClassRateLimit},
		{"quota", errEvent(`{"message":"quota exceeded"}`), ClassRateLimit},
		{"5xx", errEvent(`{"statusCode":503,"message":"bad gateway"}`), ClassServer5xx},
		{"auth-phrase", errEvent(`{"message":"invalid api key"}`), ClassAuth},
		{"401-http", errEvent(`{"statusCode":401,"message":"unauthorized"}`), ClassAuth},
		{"opencode-model-not-found", errEvent(`{"message":"Model not found: openai/gpt-4o-mini. Did you mean: gpt-4o-mini?"}`), ClassAuth},
		{"opencode-unknown-provider", errEvent(`{"message":"unknown provider for some-model"}`), ClassAuth},
		{"clean-answer-event", `{"type":"text","part":{"text":"here is your answer"}}`, ClassNone},
		{"non-json-fallback", `panic: runtime error: invalid api key`, ClassAuth},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in, ""); got != c.want {
				t.Fatalf("Classify(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestClassify_IgnoresToolOutput is the regression guard for the false
// auth-lock bug: a healthy run whose web-search tool crawled pages returning
// HTTP 401/403. Those errors live inside message parts, not opencode's error
// envelope, so they must NOT classify as a vendor auth failure. (Two live
// vendors were permanently auth-locked by exactly this on 2026-06-01.)
func TestClassify_IgnoresToolOutput(t *testing.T) {
	// A realistic mix: assistant text + a tool result carrying crawl errors,
	// none of which is opencode's own error envelope.
	stdout := strings.Join([]string{
		`{"type":"step_start","sessionID":"ses_x","part":{"id":"p1"}}`,
		`{"type":"tool","sessionID":"ses_x","part":{"tool":"webfetch","state":{"output":"{\"error\":{\"httpStatusCode\":401,\"tag\":\"CRAWL_HTTP_401\"}}"}}}`,
		`{"type":"tool","sessionID":"ses_x","part":{"tool":"webfetch","state":{"output":"{\"httpStatusCode\":403,\"tag\":\"SOURCE_NOT_AVAILABLE\"}"}}}`,
		`{"type":"text","sessionID":"ses_x","part":{"text":"The site returned 403 Forbidden and an unauthorized 401, see https://x.com/a401b403."}}`,
		`{"type":"step_finish","sessionID":"ses_x","part":{"reason":"stop"}}`,
	}, "\n")
	if got := Classify(stdout, ""); got != ClassNone {
		t.Fatalf("crawl-side HTTP 401/403 in tool output must not classify; got %d, want ClassNone", got)
	}
}
