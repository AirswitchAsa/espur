package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
