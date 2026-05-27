package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/vendor"
)

func TestNewRequestID_ShapeAndAlphabet(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewRequestID()
		if len(id) != 8 {
			t.Fatalf("len=%d for %q", len(id), id)
		}
		for _, r := range id {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("char %q not in Crockford base32 alphabet (id=%q)", r, id)
			}
		}
	}
}

func TestNewRequestID_NotConstant(t *testing.T) {
	a := NewRequestID()
	b := NewRequestID()
	if a == b {
		t.Fatalf("two consecutive ids matched: %q", a)
	}
}

func TestCrashReply_Format(t *testing.T) {
	got := CrashReply("XK4Q7B9R")
	want := "Internal error. Check logs. Request ID: `XK4Q7B9R`."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTimeoutReply_Pinned(t *testing.T) {
	if TimeoutReply != "Took too long, aborted. Try again or rephrase." {
		t.Fatalf("wording drifted: %q", TimeoutReply)
	}
}

func TestDrainedReply_AllAuthLocked_LeadsWithReconfigure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snaps := []vendor.PenalizedSnapshot{
		{VendorID: "v1", Status: store.PenaltyAuthLocked},
		{VendorID: "v2", Status: store.PenaltyAuthLocked},
	}
	body := DrainedReply(snaps, "https://dash.local", now)
	if !strings.HasPrefix(body, "All configured vendors need reconfiguration") {
		t.Fatalf("wrong lead-in:\n%s", body)
	}
	if !strings.Contains(body, "v1 — auth failed") || !strings.Contains(body, "v2 — auth failed") {
		t.Fatalf("missing per-vendor reconfigure note:\n%s", body)
	}
	if !strings.Contains(body, "https://dash.local") {
		t.Fatal("dashboard URL not rendered")
	}
}

func TestDrainedReply_MixedLeadsWithExhausted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cd := now.Add(75 * time.Second)
	snaps := []vendor.PenalizedSnapshot{
		{VendorID: "v1", Status: store.PenaltyAuthLocked},
		{VendorID: "v2", Status: store.PenaltyCooldown, CooldownUntil: &cd},
	}
	body := DrainedReply(snaps, "", now)
	if !strings.HasPrefix(body, "All vendors exhausted") {
		t.Fatalf("wrong lead-in:\n%s", body)
	}
	if !strings.Contains(body, "v1 — auth failed") {
		t.Fatal("v1 line missing")
	}
	if !strings.Contains(body, "v2 — rate-limited, retry in ~1m") {
		t.Fatalf("v2 line wrong (want 1m ish):\n%s", body)
	}
	if !strings.HasSuffix(body, "Check the dashboard.") {
		t.Fatalf("dashboard tail wrong:\n%s", body)
	}
}

func TestDrainedReply_CooldownNoUntil(t *testing.T) {
	now := time.Unix(0, 0)
	snaps := []vendor.PenalizedSnapshot{
		{VendorID: "v1", Status: store.PenaltyCooldown}, // no CooldownUntil
	}
	body := DrainedReply(snaps, "", now)
	if !strings.Contains(body, "v1 — rate-limited\n") {
		t.Fatalf("wrong line for nil CooldownUntil:\n%s", body)
	}
}

func TestDrainedReply_EmptySnapshotsStillReadable(t *testing.T) {
	body := DrainedReply(nil, "https://d", time.Unix(0, 0))
	if !strings.HasPrefix(body, "All vendors exhausted") {
		t.Fatalf("got %q", body)
	}
}

func TestHumanizeDuration_Buckets(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{59 * time.Second, "<1m"},
		{60 * time.Second, "1m"},
		{30 * time.Minute, "30m"},
		{59*time.Minute + 50*time.Second, "60m"}, // rounds up to 60m (still in <1h bucket)
		{61 * time.Minute, "1h1m"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
	}
	for _, c := range cases {
		if got := humanizeDuration(c.in); got != c.want {
			t.Fatalf("humanizeDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}
