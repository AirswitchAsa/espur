package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake-opencode tests use the standard library "re-exec self as helper
// process" trick: this test binary, when invoked with FAKE_OC_BEHAVIOR set,
// pretends to be the opencode CLI and writes whatever stdout/stderr/exit
// the test wants. Lets us exercise extractSessionID, exportAssistantText,
// crash classification, and timeout behaviour without a real opencode CLI
// or a live vendor.

const fakeOCEnv = "ESPUR_FAKE_OPENCODE"

// TestMain catches the re-exec entry and runs the fake CLI before the test
// framework would otherwise take over.
func TestMain(m *testing.M) {
	if os.Getenv(fakeOCEnv) != "" {
		runFakeOpencode()
		return
	}
	os.Exit(m.Run())
}

func runFakeOpencode() {
	args := os.Args[1:] // first arg is the test binary path
	subcommand := ""
	if len(args) > 0 {
		subcommand = args[0]
	}
	switch subcommand {
	case "run":
		// Emit scripted run stdout (and optional stderr) and exit.
		if s := os.Getenv("FAKE_OC_SLEEP_MS"); s != "" {
			d, _ := time.ParseDuration(s + "ms")
			time.Sleep(d)
		}
		if s := os.Getenv("FAKE_OC_STDERR"); s != "" {
			fmt.Fprint(os.Stderr, s)
		}
		if s := os.Getenv("FAKE_OC_STDOUT"); s != "" {
			fmt.Fprint(os.Stdout, s)
		}
	case "export":
		// Optional transient injection: fail the first N export attempts to
		// exercise the retry wrapper. Each export is a fresh process, so the
		// attempt count is tracked in a counter file. A "failure" is empty
		// stdout + exit 0, which makes exportAssistantText's json.Unmarshal err
		// — the same shape as the real post-run transient.
		if cf := os.Getenv("FAKE_OC_COUNTER_FILE"); cf != "" {
			n := 0
			if b, err := os.ReadFile(cf); err == nil {
				_, _ = fmt.Sscanf(string(b), "%d", &n)
			}
			n++
			_ = os.WriteFile(cf, []byte(fmt.Sprintf("%d", n)), 0o644)
			failTimes := 0
			if s := os.Getenv("FAKE_OC_EXPORT_FAIL_TIMES"); s != "" {
				_, _ = fmt.Sscanf(s, "%d", &failTimes)
			}
			if n <= failTimes {
				os.Exit(0) // empty stdout → parse failure in the caller
			}
		}
		if s := os.Getenv("FAKE_OC_EXPORT"); s != "" {
			fmt.Fprint(os.Stdout, s)
		}
	default:
		fmt.Fprintf(os.Stderr, "fake-opencode: unknown subcommand %q\n", subcommand)
		os.Exit(2)
	}
	exit := 0
	if s := os.Getenv("FAKE_OC_EXIT"); s != "" {
		_, _ = fmt.Sscanf(s, "%d", &exit)
	}
	os.Exit(exit)
}

// fakeBin returns the path to this test binary, suitable for use as
// Request.BinPath. The fake-mode is selected via FAKE_OC_BEHAVIOR in env.
func fakeBin(t *testing.T) string {
	t.Helper()
	// os.Args[0] is the test binary itself.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("can't locate test binary: %v", err)
	}
	if _, err := exec.LookPath(exe); err != nil && !strings.Contains(exe, "/") {
		t.Skipf("test binary not addressable: %v", err)
	}
	return exe
}

// fakeCredEnv packages the fake-binary's behavior knobs into a CredEnv map,
// which Invoke passes through verbatim. We can't use os.Setenv: buildEnv()
// only whitelists a small set of variables onto the child, so test-only
// knobs would be dropped.
func fakeCredEnv(kv map[string]string) map[string]string {
	out := map[string]string{fakeOCEnv: "1"}
	for k, v := range kv {
		out[k] = v
	}
	return out
}

// --- tests ---

func TestInvoke_Fake_Success(t *testing.T) {
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{VendorID: "v", Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_fake_1"}` + "\n",
			"FAKE_OC_EXPORT": `{"messages":[{"info":{"role":"assistant"},"parts":[{"type":"text","text":"hello world"}]}]}`,
			"FAKE_OC_EXIT":   "0",
		})},
		WorkDir: t.TempDir(),
		UserMsg: "<request>x</request>",
		Timeout: 5 * time.Second,
		BinPath: fakeBin(t),
	})
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, res.Stderr)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
	if res.AssistantText != "hello world" {
		t.Fatalf("text=%q", res.AssistantText)
	}
}

func TestInvoke_Fake_TextFromStdout(t *testing.T) {
	// stdout carries the answer as a type:text event. Export is deliberately
	// left to fail (empty FAKE_OC_EXPORT) — success proves the text came from
	// stdout and export was never consulted.
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_so"}` + "\n" +
				`{"type":"text","sessionID":"ses_so","part":{"id":"p1","messageID":"m1","type":"text","text":"answer from stdout"}}` + "\n",
			"FAKE_OC_EXIT": "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Outcome != OutcomeSuccess || res.AssistantText != "answer from stdout" {
		t.Fatalf("outcome=%s reason=%s text=%q", res.Outcome, res.CrashReason, res.AssistantText)
	}
}

func TestInvoke_Fake_StdoutPrefersLastMessageConcatParts(t *testing.T) {
	// Two assistant messages stream text; the final answer is the last
	// message's parts, concatenated in order (an earlier reasoning step is
	// dropped, matching export semantics).
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_ml"}` + "\n" +
				`{"type":"text","sessionID":"ses_ml","part":{"id":"p1","messageID":"m1","type":"text","text":"reasoning step"}}` + "\n" +
				`{"type":"text","sessionID":"ses_ml","part":{"id":"p2","messageID":"m2","type":"text","text":"final "}}` + "\n" +
				`{"type":"text","sessionID":"ses_ml","part":{"id":"p3","messageID":"m2","type":"text","text":"answer"}}` + "\n",
			"FAKE_OC_EXIT": "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Outcome != OutcomeSuccess || res.AssistantText != "final answer" {
		t.Fatalf("outcome=%s text=%q", res.Outcome, res.AssistantText)
	}
}

func TestInvoke_Fake_NoParseableJSON(t *testing.T) {
	res, _ := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": "this is not json at all\nnor is this\n",
			"FAKE_OC_EXIT":   "0",
		})},
		WorkDir: t.TempDir(),
		UserMsg: "x",
		Timeout: 5 * time.Second,
		BinPath: fakeBin(t),
	})
	if res.Outcome != OutcomeCrash {
		t.Fatalf("want Crash, got %s", res.Outcome)
	}
	if res.CrashReason != "no_parseable_json" {
		t.Fatalf("reason=%q", res.CrashReason)
	}
}

func TestInvoke_Fake_NoAssistantText(t *testing.T) {
	res, _ := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_empty"}` + "\n",
			"FAKE_OC_EXPORT": `{"messages":[]}`,
			"FAKE_OC_EXIT":   "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if res.Outcome != OutcomeCrash || res.CrashReason != "no_assistant_text" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
}

func TestInvoke_Fake_ExportFails(t *testing.T) {
	res, _ := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_x"}` + "\n",
			// Empty FAKE_OC_EXPORT → empty export stdout → JSON parse fails.
			"FAKE_OC_EXIT": "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if res.Outcome != OutcomeCrash || res.CrashReason != "export_failed" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
}

func TestInvoke_Fake_ExportRetrySucceeds(t *testing.T) {
	// First 2 export attempts fail (transient); the 3rd succeeds. With
	// exportMaxAttempts=3 the wrapper should recover and deliver the text.
	counter := filepath.Join(t.TempDir(), "export-attempts")
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT":            `{"type":"step_start","sessionID":"ses_retry"}` + "\n",
			"FAKE_OC_EXPORT":            `{"messages":[{"info":{"role":"assistant"},"parts":[{"type":"text","text":"recovered answer"}]}]}`,
			"FAKE_OC_COUNTER_FILE":      counter,
			"FAKE_OC_EXPORT_FAIL_TIMES": "2",
			"FAKE_OC_EXIT":              "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Outcome != OutcomeSuccess || res.AssistantText != "recovered answer" {
		t.Fatalf("outcome=%s reason=%s text=%q", res.Outcome, res.CrashReason, res.AssistantText)
	}
}

func TestInvoke_Fake_ExportRetryExhausted(t *testing.T) {
	// Every attempt fails (FAIL_TIMES exceeds exportMaxAttempts) → export_failed.
	counter := filepath.Join(t.TempDir(), "export-attempts")
	res, _ := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			"FAKE_OC_STDOUT":            `{"type":"step_start","sessionID":"ses_retry_x"}` + "\n",
			"FAKE_OC_EXPORT":            `{"messages":[{"info":{"role":"assistant"},"parts":[{"type":"text","text":"never reached"}]}]}`,
			"FAKE_OC_COUNTER_FILE":      counter,
			"FAKE_OC_EXPORT_FAIL_TIMES": "99",
			"FAKE_OC_EXIT":              "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 5 * time.Second, BinPath: fakeBin(t),
	})
	if res.Outcome != OutcomeCrash || res.CrashReason != "export_failed" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
	// Confirm it actually retried the full budget rather than giving up early.
	if b, err := os.ReadFile(counter); err == nil {
		var n int
		_, _ = fmt.Sscanf(string(b), "%d", &n)
		if n != exportMaxAttempts {
			t.Fatalf("expected %d export attempts, got %d", exportMaxAttempts, n)
		}
	} else {
		t.Fatalf("counter file unreadable: %v", err)
	}
}

func TestInvoke_Fake_Timeout(t *testing.T) {
	start := time.Now()
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m", CredEnv: fakeCredEnv(map[string]string{
			// Sleep longer than the Invoke timeout. The child must be killed.
			"FAKE_OC_SLEEP_MS": "3000",
			"FAKE_OC_STDOUT":   `{"type":"step_start","sessionID":"ses_late"}` + "\n",
			"FAKE_OC_EXIT":     "0",
		})},
		WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 200 * time.Millisecond,
		BinPath: fakeBin(t),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("want Timeout, got %s", res.Outcome)
	}
	if elapsed > 200*time.Millisecond+DefaultKillGrace+time.Second {
		t.Fatalf("Invoke took too long to return: %s", elapsed)
	}
}

func TestInvoke_Fake_SpawnError(t *testing.T) {
	res, err := Invoke(context.Background(), Request{
		Vendor: Vendor{Model: "m"}, WorkDir: t.TempDir(), UserMsg: "x",
		Timeout: 1 * time.Second,
		BinPath: "/path/that/does/not/exist/opencode",
	})
	if err == nil {
		t.Fatal("expected spawn error")
	}
	if res.Outcome != OutcomeCrash || res.CrashReason != "spawn_error" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
}

func TestExtractSessionID_FirstWinsAndIgnoresJunk(t *testing.T) {
	in := strings.NewReader(`
not json at all
{"unrelated":"line"}
{"type":"step_start","sessionID":"ses_winner"}
{"type":"later","sessionID":"ses_loser"}
`)
	id, err := extractSessionID(in)
	if err != nil {
		t.Fatal(err)
	}
	if id != "ses_winner" {
		t.Fatalf("got %q", id)
	}
}

func TestExtractSessionID_None(t *testing.T) {
	id, err := extractSessionID(strings.NewReader("nothing\nuseful\nhere\n"))
	if err == nil {
		t.Fatalf("expected error, got id=%q", id)
	}
}

func TestBuildEnv_PassthroughForwardsListedVars(t *testing.T) {
	t.Setenv("ESPUR_OPENCODE_ENV_PASSTHROUGH", "EXA_API_KEY, XAI_API_KEY ,UNSET_VAR")
	t.Setenv("EXA_API_KEY", "exa-secret")
	t.Setenv("XAI_API_KEY", "xai-secret")
	// UNSET_VAR is deliberately not set — passthrough should silently skip it.
	os.Unsetenv("UNSET_VAR")

	env := buildEnv(map[string]string{"ANTHROPIC_API_KEY": "anth-secret"})

	got := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			got[kv[:i]] = kv[i+1:]
		}
	}
	if got["EXA_API_KEY"] != "exa-secret" || got["XAI_API_KEY"] != "xai-secret" {
		t.Fatalf("passthrough missing: env=%v", got)
	}
	if _, ok := got["UNSET_VAR"]; ok {
		t.Fatalf("UNSET_VAR should be skipped when not set")
	}
	if got["ANTHROPIC_API_KEY"] != "anth-secret" {
		t.Fatalf("vendor cred not forwarded: %v", got)
	}
}

func TestBuildEnv_PassthroughEmpty(t *testing.T) {
	t.Setenv("ESPUR_OPENCODE_ENV_PASSTHROUGH", "")
	t.Setenv("EXA_API_KEY", "should-not-leak")

	env := buildEnv(map[string]string{})
	for _, kv := range env {
		if strings.HasPrefix(kv, "EXA_API_KEY=") {
			t.Fatalf("EXA_API_KEY leaked through empty passthrough: %s", kv)
		}
	}
}
