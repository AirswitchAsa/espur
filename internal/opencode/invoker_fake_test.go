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
		// Optional transient injection, tracked across the fresh export
		// processes via a counter file:
		//   FAKE_OC_EXPORT_FAIL_TIMES   — first N attempts emit empty stdout
		//                                 (→ json.Unmarshal err in the caller),
		//                                 the same shape as the real post-run
		//                                 "session not visible yet" transient.
		//   FAKE_OC_EXPORT_STALE_TIMES  — first N *successful* attempts emit
		//                                 FAKE_OC_EXPORT_STALE (a mid-turn
		//                                 snapshot missing the final message);
		//                                 later attempts emit FAKE_OC_EXPORT.
		//                                 Reproduces the settle race where a
		//                                 non-empty-but-incomplete read would
		//                                 otherwise serve a preamble.
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
			staleTimes := 0
			if s := os.Getenv("FAKE_OC_EXPORT_STALE_TIMES"); s != "" {
				_, _ = fmt.Sscanf(s, "%d", &staleTimes)
			}
			if n <= failTimes+staleTimes {
				fmt.Fprint(os.Stdout, os.Getenv("FAKE_OC_EXPORT_STALE"))
				os.Exit(0)
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

// shortExportBudget shrinks the export retry budget for tests that exhaust it
// (export never succeeds), so they finish quickly instead of waiting the
// production budget. It must still comfortably exceed two fake-export spawns so
// the "it retried" assertion holds — each export is a fresh re-exec of the test
// binary, which is slow under the race detector. Restored after the test.
func shortExportBudget(t *testing.T) {
	t.Helper()
	old := exportRetryBudget
	exportRetryBudget = 4 * time.Second
	t.Cleanup(func() { exportRetryBudget = old })
}

// --- NDJSON stream fixture helpers ---

// stepStart, textEv, stepFinish, toolEv build the `opencode run --format json`
// events the streamParser reads, so tests can script realistic multi-message
// turns without hand-writing JSON.
func stepStart(sid, mid string) string {
	return `{"type":"step_start","sessionID":"` + sid + `","part":{"type":"step-start","messageID":"` + mid + `"}}`
}
func textEv(sid, mid, pid, text string) string {
	return `{"type":"text","sessionID":"` + sid + `","part":{"id":"` + pid + `","messageID":"` + mid + `","type":"text","text":"` + text + `"}}`
}
func stepFinish(sid, mid string) string {
	return `{"type":"step_finish","sessionID":"` + sid + `","part":{"type":"step-finish","messageID":"` + mid + `"}}`
}
func toolEv(sid, mid string) string {
	return `{"type":"tool_use","sessionID":"` + sid + `","part":{"messageID":"` + mid + `","type":"tool","tool":"bash"}}`
}

// exportMsgJSON builds one assistant message for a fake `opencode export`.
func exportMsgJSON(id, text string, hasTool bool) string {
	parts := `{"type":"text","text":"` + text + `"}`
	if hasTool {
		parts += `,{"type":"tool"}`
	}
	return `{"info":{"role":"assistant","id":"` + id + `"},"parts":[` + parts + `]}`
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// collectInvoke runs Invoke with an OnMessage sink and returns the streamed
// messages alongside the result.
func collectInvoke(t *testing.T, env map[string]string, timeout time.Duration) ([]string, Result, error) {
	t.Helper()
	var streamed []string
	res, err := Invoke(context.Background(), Request{
		Vendor:    Vendor{VendorID: "v", Model: "m", CredEnv: fakeCredEnv(env)},
		WorkDir:   t.TempDir(),
		UserMsg:   "<request>x</request>",
		Timeout:   timeout,
		BinPath:   fakeBin(t),
		OnMessage: func(text string) { streamed = append(streamed, text) },
	})
	return streamed, res, err
}

// --- tests ---

func TestInvoke_Fake_StreamsEachMessageLive(t *testing.T) {
	// A preamble message, a tool-only step, then the answer — all on stdout with
	// their step_finish. Each text-bearing message is posted as it completes; the
	// export backstop is never consulted (FAKE_OC_EXPORT is empty, which would
	// error — so reaching it would fail the run).
	s := "ses_live"
	stdout := strings.Join([]string{
		stepStart(s, "m1"), textEv(s, "m1", "p1", "let me check~"), stepFinish(s, "m1"),
		stepStart(s, "m2"), toolEv(s, "m2"), stepFinish(s, "m2"),
		stepStart(s, "m3"), textEv(s, "m3", "p3", "the real answer"), stepFinish(s, "m3"),
	}, "\n") + "\n"

	streamed, res, err := collectInvoke(t, map[string]string{
		"FAKE_OC_STDOUT": stdout,
		"FAKE_OC_EXIT":   "0",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, res.Stderr)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
	want := []string{"let me check~", "the real answer"}
	if !equalStrings(streamed, want) {
		t.Fatalf("streamed=%v want=%v", streamed, want)
	}
	if !equalStrings(res.Messages, want) {
		t.Fatalf("res.Messages=%v want=%v", res.Messages, want)
	}
	if res.AssistantText != "the real answer" {
		t.Fatalf("AssistantText=%q", res.AssistantText)
	}
	if !res.Streamed {
		t.Fatalf("Streamed=false, want true")
	}
}

func TestInvoke_Fake_FlushesFinalMessageWithoutStepFinish(t *testing.T) {
	// opencode commonly exits right after the final message's text without a
	// closing step_finish. That text is on stdout, so it must be flushed live on
	// clean exit — no export round-trip (FAKE_OC_EXPORT is empty, which would
	// error if consulted).
	s := "ses_noend"
	stdout := strings.Join([]string{
		stepStart(s, "m1"), textEv(s, "m1", "p1", "preamble"), stepFinish(s, "m1"),
		stepStart(s, "m2"), textEv(s, "m2", "p2", "the final answer"), // no step_finish
	}, "\n") + "\n"

	streamed, res, err := collectInvoke(t, map[string]string{
		"FAKE_OC_STDOUT": stdout,
		"FAKE_OC_EXIT":   "0",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v stderr=%s", err, res.Stderr)
	}
	want := []string{"preamble", "the final answer"}
	if !equalStrings(streamed, want) {
		t.Fatalf("streamed=%v want=%v", streamed, want)
	}
	if res.AssistantText != "the final answer" {
		t.Fatalf("AssistantText=%q", res.AssistantText)
	}
}

func TestInvoke_Fake_RecoversDroppedFinalMessageFromExport(t *testing.T) {
	// opencode streamed the preamble (with step_finish) but DROPPED the trailing
	// text event of the final message m2 — stdout has m2's step_start/step_finish
	// but no text. The export backstop recovers m2's text and emits it after the
	// streamed preamble. Both messages reach the user, answer last.
	s := "ses_drop"
	stdout := strings.Join([]string{
		stepStart(s, "m1"), textEv(s, "m1", "p1", "preamble"), stepFinish(s, "m1"),
		stepStart(s, "m2"), stepFinish(s, "m2"), // m2 text dropped
	}, "\n") + "\n"
	export := `{"messages":[` +
		exportMsgJSON("m1", "preamble", false) + `,` +
		exportMsgJSON("m2", "the real final answer", false) + `]}`

	streamed, res, err := collectInvoke(t, map[string]string{
		"FAKE_OC_STDOUT": stdout,
		"FAKE_OC_EXPORT": export,
		"FAKE_OC_EXIT":   "0",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"preamble", "the real final answer"}
	if !equalStrings(streamed, want) {
		t.Fatalf("streamed=%v want=%v", streamed, want)
	}
	if res.AssistantText != "the real final answer" {
		t.Fatalf("AssistantText=%q want final answer", res.AssistantText)
	}
}

func TestInvoke_Fake_ExportWaitsForFinalMessage(t *testing.T) {
	// Regression for the wechat:4ae004 incident. stdout announces the final
	// message m5 (step_start/step_finish) but its text was dropped; the first two
	// export reads are STALE mid-turn snapshots that end at the preamble m4 and do
	// NOT yet contain m5. The precise-settle retry must keep going until m5
	// appears, then serve m5 — not the m4 preamble a "first non-empty read" would
	// have accepted.
	s := "ses_settle"
	stdout := strings.Join([]string{
		stepStart(s, "m4"), textEv(s, "m4", "p4", "let me look again~"), stepFinish(s, "m4"),
		stepStart(s, "m5"), stepFinish(s, "m5"), // m5 text dropped
	}, "\n") + "\n"
	stale := `{"messages":[` + exportMsgJSON("m4", "let me look again~", true) + `]}`
	settled := `{"messages":[` +
		exportMsgJSON("m4", "let me look again~", true) + `,` +
		exportMsgJSON("m5", "you cannot work in Sweden on a Danish permit alone", false) + `]}`

	counter := filepath.Join(t.TempDir(), "export-attempts")
	streamed, res, err := collectInvoke(t, map[string]string{
		"FAKE_OC_STDOUT":             stdout,
		"FAKE_OC_EXPORT":             settled,
		"FAKE_OC_EXPORT_STALE":       stale,
		"FAKE_OC_EXPORT_STALE_TIMES": "2",
		"FAKE_OC_COUNTER_FILE":       counter,
		"FAKE_OC_EXIT":               "0",
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"let me look again~", "you cannot work in Sweden on a Danish permit alone"}
	if !equalStrings(streamed, want) {
		t.Fatalf("streamed=%v want=%v", streamed, want)
	}
	if res.AssistantText != want[1] {
		t.Fatalf("AssistantText=%q want the settled final answer, not the preamble", res.AssistantText)
	}
	if b, err := os.ReadFile(counter); err == nil {
		var n int
		_, _ = fmt.Sscanf(string(b), "%d", &n)
		if n < 3 {
			t.Fatalf("expected ≥3 export reads (2 stale + settled), got %d", n)
		}
	}
}

func TestInvoke_Fake_Success_BackstopOnly(t *testing.T) {
	// stdout carries no text events at all (only a session id); the whole answer
	// comes from the export backstop. Single message delivered.
	streamed, res, err := collectInvoke(t, map[string]string{
		"FAKE_OC_STDOUT": `{"type":"step_start","sessionID":"ses_fake_1"}` + "\n",
		"FAKE_OC_EXPORT": `{"messages":[` + exportMsgJSON("m1", "hello world", false) + `]}`,
		"FAKE_OC_EXIT":   "0",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, res.Stderr)
	}
	if res.Outcome != OutcomeSuccess || res.AssistantText != "hello world" {
		t.Fatalf("outcome=%s text=%q", res.Outcome, res.AssistantText)
	}
	if !equalStrings(streamed, []string{"hello world"}) {
		t.Fatalf("streamed=%v", streamed)
	}
}

func TestStreamParser_Boundaries(t *testing.T) {
	// step_finish is the emit boundary; streamed text parts collapse to their
	// final value (last-wins per part id); tool-only and empty messages emit
	// nothing.
	s := "s"
	var got []string
	sp := newStreamParser(func(text string) { got = append(got, text) })
	for _, line := range []string{
		stepStart(s, "m1"), textEv(s, "m1", "p1", "par"), textEv(s, "m1", "p1", "partial done"), stepFinish(s, "m1"),
		stepStart(s, "m2"), toolEv(s, "m2"), stepFinish(s, "m2"), // tool-only: no emit
		stepStart(s, "m3"), textEv(s, "m3", "a", "final "), textEv(s, "m3", "b", "answer"), stepFinish(s, "m3"),
	} {
		sp.feed([]byte(line))
	}
	want := []string{"partial done", "final answer"}
	if !equalStrings(got, want) {
		t.Fatalf("emitted=%v want=%v", got, want)
	}
	if sp.sessionID != s {
		t.Fatalf("sessionID=%q", sp.sessionID)
	}
	if sp.finalMessageID() != "m3" || !sp.emittedFinal() {
		t.Fatalf("finalMessageID=%q emittedFinal=%v", sp.finalMessageID(), sp.emittedFinal())
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
	shortExportBudget(t)
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
	// First 2 export attempts fail (transient); the 3rd succeeds. The retry loop
	// should keep going within its budget and deliver the recovered text.
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
	// Every export attempt fails; stdout has no text either → export_failed once
	// the budget is exhausted.
	shortExportBudget(t)
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
	// Confirm it actually retried (more than one attempt) before giving up.
	if b, err := os.ReadFile(counter); err == nil {
		var n int
		_, _ = fmt.Sscanf(string(b), "%d", &n)
		if n < 2 {
			t.Fatalf("expected the export to be retried, got %d attempt(s)", n)
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
