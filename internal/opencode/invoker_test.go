package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInvoke_SuccessOneVendor exercises the full happy path against a real
// vendor. Vendor used: DeepSeek (the only provider currently authed in
// ~/.local/share/opencode/auth.json on this dev box). Switching vendors is a
// matter of changing the Model + CredEnv block below.
//
// The composite user message follows docs/specs/context-assembly.dog.md
// ("<thread-context>…</thread-context>" + "<request>…</request>").
//
// Skipped automatically unless ESPUR_OPENCODE_LIVE=1 — keeps `go test ./...`
// fast and offline-safe in CI.
func TestInvoke_SuccessOneVendor(t *testing.T) {
	if os.Getenv("ESPUR_OPENCODE_LIVE") == "" {
		t.Skip("set ESPUR_OPENCODE_LIVE=1 to run live opencode invocation test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode CLI not on PATH: %v", err)
	}

	workDir := t.TempDir()
	userMsg := strings.Join([]string{
		`<thread-context note="recent user messages on this thread, oldest first">`,
		`alice: I'm testing the chat bridge.`,
		`</thread-context>`,
		`<request from="alice">`,
		`Reply with exactly the phrase: PING_OK`,
		`</request>`,
	}, "\n")

	req := Request{
		Vendor: Vendor{
			VendorID: "deepseek-api",
			Model:    "deepseek/deepseek-chat",
			CredEnv:  vendorCredsFromTestEnv(t),
		},
		WorkDir: workDir,
		UserMsg: userMsg,
		Timeout: DefaultTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	res, err := Invoke(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Invoke error: %v\nstderr:\n%s", err, res.Stderr)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("expected Success outcome, got %s (reason=%q)\nstdout:\n%s\nstderr:\n%s",
			res.Outcome, res.CrashReason, res.Stdout, res.Stderr)
	}
	if strings.TrimSpace(res.AssistantText) == "" {
		t.Fatalf("assistant text empty\nstdout:\n%s", res.Stdout)
	}
	if elapsed >= DefaultTimeout {
		t.Fatalf("invocation took %s, should be well inside %s", elapsed, DefaultTimeout)
	}
	t.Logf("vendor=%s model=%s elapsed=%s assistant=%q",
		req.Vendor.VendorID, req.Vendor.Model, elapsed, res.AssistantText)
}

// TestInvoke_TimeoutKillsChild forces a timeout by setting the budget to a
// fraction of opencode's startup time. Asserts the spec's Timeout outcome
// and that we return before the wall clock blows up.
func TestInvoke_TimeoutKillsChild(t *testing.T) {
	if os.Getenv("ESPUR_OPENCODE_LIVE") == "" {
		t.Skip("set ESPUR_OPENCODE_LIVE=1 to run live opencode invocation test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode CLI not on PATH: %v", err)
	}

	workDir := t.TempDir()
	req := Request{
		Vendor: Vendor{
			VendorID: "deepseek-api",
			Model:    "deepseek/deepseek-chat",
			CredEnv:  vendorCredsFromTestEnv(t),
		},
		WorkDir: workDir,
		UserMsg: "<request>say hi</request>",
		Timeout: 500 * time.Millisecond,
	}

	ctx := context.Background()
	hardLimit := req.Timeout + DefaultKillGrace + 10*time.Second
	deadline := time.Now().Add(hardLimit)

	res, err := Invoke(ctx, req)
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("expected Timeout outcome, got %s\nstdout:\n%s\nstderr:\n%s",
			res.Outcome, res.Stdout, res.Stderr)
	}
	if time.Now().After(deadline) {
		t.Fatalf("Invoke returned after hard limit %s — child not killed promptly", hardLimit)
	}
}

// vendorCredsFromTestEnv reads the vendor credentials this test should pass to
// the child. For DeepSeek we just need DEEPSEEK_API_KEY; opencode also reads
// from its auth.json so we additionally allow that as a no-env path.
func vendorCredsFromTestEnv(t *testing.T) map[string]string {
	t.Helper()
	creds := map[string]string{}
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
		creds["DEEPSEEK_API_KEY"] = v
	}
	// opencode reads ~/.local/share/opencode/auth.json (or platform equivalent)
	// for vendor credentials when no env override is set. Pass through XDG /
	// HOME so it can find that file.
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		creds["XDG_DATA_HOME"] = v
	}
	// Sanity check: the auth file must exist or env key must be set.
	if creds["DEEPSEEK_API_KEY"] == "" {
		authPath := filepath.Join(os.Getenv("HOME"), ".local/share/opencode/auth.json")
		if _, err := os.Stat(authPath); err != nil {
			t.Skipf("no DEEPSEEK_API_KEY env and no %s: %v", authPath, err)
		}
	}
	return creds
}
