// Package opencode invokes the `opencode` CLI as a stateless child process per
// trigger. See docs/specs/opencode-invoke.dog.md for the behavioral contract.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout is the per-invocation wall clock budget.
// Spec: opencode-invoke.dog.md "Timeout — Default 120 seconds."
const DefaultTimeout = 120 * time.Second

// DefaultKillGrace is the grace period between SIGTERM and SIGKILL on timeout.
// Spec: opencode-invoke.dog.md "Notes" — grace period pinned to 5 seconds.
const DefaultKillGrace = 5 * time.Second

// DefaultExportTimeout caps `opencode export` independently of the run
// timeout. Export is a local DB read — sub-second in practice — so a tight
// budget is fine and prevents long-tail run completions from starving export.
// It bounds the whole retry sequence below, not a single attempt.
const DefaultExportTimeout = 30 * time.Second

// Export retry tuning. `opencode export` is only a fallback now (assistant text
// normally comes straight from run stdout), but when used it occasionally
// returns nothing in the instant after `opencode run` exits — the just-written
// session hasn't settled in opencode's SQLite store yet, and that window grows
// with session size. Retry with escalating backoff (≈6s total over 5 attempts)
// so a slow-settling large session still resolves instead of crashing. The
// whole sequence stays bounded by DefaultExportTimeout.
const (
	exportMaxAttempts  = 5
	exportRetryBackoff = 400 * time.Millisecond
)

// Outcome enumerates the terminal categories defined in
// docs/specs/opencode-invoke.dog.md ("Outcome"). Vendor-fallthrough categories
// (rate-limit / quota / auth) are not yet classified — single-vendor slice.
type Outcome int

const (
	OutcomeUnknown Outcome = iota
	OutcomeSuccess
	OutcomeTimeout
	OutcomeCrash
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeCrash:
		return "crash"
	default:
		return "unknown"
	}
}

// Vendor is the concrete entry the pool yields per attempt. See
// docs/specs/vendor-pool.dog.md ("Outcome — A concrete (vendor_id, model,
// credentials)…").
type Vendor struct {
	// VendorID is the stable identifier (e.g. "chatgpt-oauth").
	VendorID string
	// Model is opencode's --model flag value (e.g. "anthropic/claude-…").
	Model string
	// CredEnv is the credentials env block exposed to the child process.
	// Per spec: "Only the credentials of the vendor currently being attempted
	// are exposed." Espur does not leak unrelated vendor credentials.
	CredEnv map[string]string
}

// Request bundles one invocation attempt.
type Request struct {
	Vendor  Vendor
	WorkDir string // child cwd; per-thread working dir.
	UserMsg string // composite user message from context assembly.
	Timeout time.Duration
	BinPath string // opencode binary; "" → look up "opencode" in PATH.
}

// Result is the terminal outcome of one invocation attempt.
type Result struct {
	Outcome       Outcome
	AssistantText string // populated on Success.
	Stderr        string // captured for diagnostics / failure classification.
	Stdout        string // raw NDJSON; kept for diagnostics.
	ExitCode      int
	Duration      time.Duration
	// CrashReason is a short tag explaining a Crash outcome
	// (e.g. "no_assistant_text", "no_parseable_json", "spawn_error").
	CrashReason string
}

// Invoke spawns `opencode run --format json --model <model>` and waits for
// it under the spec's timeout, then classifies the outcome.
func Invoke(ctx context.Context, req Request) (Result, error) {
	if req.Timeout <= 0 {
		req.Timeout = DefaultTimeout
	}
	bin := req.BinPath
	if bin == "" {
		bin = "opencode"
	}

	// Wall-clock timeout per spec; child is killed on expiry.
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	// Spec: command shape is exactly `opencode run --format json --model <m>`.
	// The user message is delivered as a positional message argument — pinned
	// experimentally; see spec note. `opencode run` accepts `[message..]`.
	cmd := exec.Command(bin, "run", "--format", "json", "--model", req.Vendor.Model, req.UserMsg)
	cmd.Dir = req.WorkDir

	// Spec: "minimal env — PATH, HOME, TMPDIR, and the vendor's credentials".
	cmd.Env = buildEnv(req.Vendor.CredEnv)

	// No TTY: pipes only, satisfying "must inherit no TTY".
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Own process group so SIGTERM/SIGKILL reach opencode's child processes too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{
			Outcome:     OutcomeCrash,
			CrashReason: "spawn_error",
			Duration:    time.Since(start),
		}, fmt.Errorf("opencode: spawn: %w", err)
	}

	// Wait in a goroutine so we can race against runCtx.Done().
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	var (
		err      error
		timedOut bool
	)
	select {
	case err = <-waitErr:
	case <-runCtx.Done():
		// Spec: "SIGTERM then SIGKILL after a grace period of a few seconds".
		timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		killGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case err = <-waitErr:
		case <-time.After(DefaultKillGrace):
			killGroup(cmd.Process.Pid, syscall.SIGKILL)
			err = <-waitErr
		}
	}

	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
	} else if err != nil && !timedOut {
		// Non-exit error from Wait itself (e.g. I/O) — treat as crash.
		res.Outcome = OutcomeCrash
		res.CrashReason = "wait_error"
		return res, fmt.Errorf("opencode: wait: %w", err)
	}

	if timedOut {
		// Spec: "A timeout is not counted as a vendor failure". Just report it.
		res.Outcome = OutcomeTimeout
		return res, nil
	}

	sessionID, parseErr := extractSessionID(strings.NewReader(res.Stdout))
	if parseErr != nil {
		res.Outcome = OutcomeCrash
		res.CrashReason = "no_parseable_json"
		return res, nil
	}

	// Prefer the assistant text straight from the run's stdout. `opencode run
	// --format json` emits the final answer as {"type":"text",...} events, so
	// the common path needs no second process at all.
	//
	// Only when stdout carries no text do we fall back to `opencode export`:
	// opencode 1.15.x intermittently drops the trailing text event from stdout
	// (the session record still has it). Export is authoritative but racy right
	// after a run — reading the freshly-written session from a fresh process can
	// return empty for a beat, and that settle window grows with session size.
	// Reading stdout first sidesteps that race for every normal turn; the
	// fallback (with retries) covers the rare dropped-event case. Historically
	// this was export-first, which is what made large research turns crash with
	// "export: unexpected end of JSON input" despite a complete answer.
	text := assistantTextFromStdout(res.Stdout)
	if strings.TrimSpace(text) == "" {
		var exportErr error
		text, exportErr = exportAssistantTextRetry(ctx, bin, sessionID, req.Vendor.CredEnv)
		if exportErr != nil {
			res.Outcome = OutcomeCrash
			res.CrashReason = "export_failed"
			return res, exportErr
		}
	}
	if strings.TrimSpace(text) == "" {
		// Spec: "A zero exit code with no usable assistant text is also a crash."
		res.Outcome = OutcomeCrash
		res.CrashReason = "no_assistant_text"
		return res, nil
	}
	res.Outcome = OutcomeSuccess
	res.AssistantText = text
	return res, nil
}

// buildEnv assembles the minimal env per spec: PATH, HOME, TMPDIR, plus the
// vendor's credentials. The XDG_* variables are passed through too so that
// opencode's own auth.json (used by OAuth providers — see docs/specs/oauth.dog.md)
// is resolved from a shared, persistent location across espur invocations
// and `opencode auth login` runs. Espur's own master key and unrelated vendor
// creds are deliberately excluded.
func buildEnv(creds map[string]string) []string {
	out := make([]string, 0, 6+len(creds))
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	// Operator-supplied passthrough: comma-separated env var names to forward
	// to opencode children. Lets a deployment hand the child non-vendor secrets
	// (e.g. keys consumed by user-installed skills) without code changes.
	for _, k := range strings.Split(os.Getenv("ESPUR_OPENCODE_ENV_PASSTHROUGH"), ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range creds {
		out = append(out, k+"="+v)
	}
	return out
}

func killGroup(pid int, sig syscall.Signal) {
	// Negative pid → signal the whole process group.
	_ = syscall.Kill(-pid, sig)
}

// opencode --format json emits NDJSON events on stdout. Each carries the
// session ID; the first event (step_start) is enough to pull the full
// session record after the run completes.
type ocEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

func extractSessionID(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev ocEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("no session id in opencode stdout")
}

// ocExport mirrors the JSON shape returned by `opencode export <sessionID>`.
// We only care about assistant message text parts here.
type ocExport struct {
	Messages []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"messages"`
}

// stdoutTextEvent mirrors the {"type":"text",...,"part":{...}} events
// `opencode run --format json` writes for assistant text parts.
type stdoutTextEvent struct {
	Type string `json:"type"`
	Part struct {
		ID        string `json:"id"`
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
	} `json:"part"`
}

// assistantTextFromStdout reconstructs the final assistant message's text from
// the run's NDJSON stdout, mirroring exportAssistantText's "last assistant
// message wins" semantics. Text parts are keyed by part id (last value wins, so
// a part that streams updates collapses to its final form) and grouped by
// message id; the parts of the last message to emit text are concatenated in
// first-seen order. Returns "" when stdout carries no text part — opencode
// occasionally omits it, and the caller then falls back to the session export.
func assistantTextFromStdout(stdout string) string {
	var lastMsg string
	textByPart := map[string]map[string]string{} // messageID -> partID -> text
	partOrder := map[string][]string{}            // messageID -> partID order
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev stdoutTextEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type != "text" || ev.Part.Type != "text" || ev.Part.Text == "" {
			continue
		}
		mid := ev.Part.MessageID
		if textByPart[mid] == nil {
			textByPart[mid] = map[string]string{}
		}
		if _, seen := textByPart[mid][ev.Part.ID]; !seen {
			partOrder[mid] = append(partOrder[mid], ev.Part.ID)
		}
		textByPart[mid][ev.Part.ID] = ev.Part.Text
		lastMsg = mid
	}
	if lastMsg == "" {
		return ""
	}
	var b strings.Builder
	for _, pid := range partOrder[lastMsg] {
		b.WriteString(textByPart[lastMsg][pid])
	}
	return b.String()
}

// exportAssistantTextRetry pulls the assistant text from the session export,
// retrying on failure or empty output. `opencode export` is a read-only,
// idempotent DB read, but run in the instant after `opencode run` exits it
// occasionally crashes or returns nothing: the just-written session hasn't
// fully settled in opencode's SQLite store yet (observed against opencode
// 1.15.13 — a heavy research turn produced a full answer, yet the immediate
// export failed while exporting the same session a moment later succeeded).
// A short backoff between attempts converts that transient into a delivered
// reply rather than an export_failed crash.
//
// The whole sequence is bounded by DefaultExportTimeout (attempts share one
// budget, so worst-case latency matches the old single-shot path). The last
// attempt's (text, err) is returned verbatim so the caller still distinguishes
// export_failed (err != nil) from no_assistant_text (empty text, no err).
func exportAssistantTextRetry(parent context.Context, bin, sessionID string, creds map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultExportTimeout)
	defer cancel()
	var (
		text string
		err  error
	)
	for attempt := 0; attempt < exportMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return text, err // out of budget — surface the last attempt's result
			case <-time.After(time.Duration(attempt) * exportRetryBackoff):
			}
		}
		text, err = exportAssistantText(ctx, bin, sessionID, creds)
		if err == nil && strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	return text, err
}

func exportAssistantText(ctx context.Context, bin, sessionID string, creds map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "export", sessionID)
	// Match the run env so auth.json + XDG resolution is consistent. We
	// also propagate the same CredEnv: BYO-key vendors need their key
	// available to `opencode export` too (it talks to the same API to
	// fetch the session record). Test fakes use this channel to control
	// the fake binary's behaviour.
	cmd.Env = buildEnv(creds)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("opencode export %s: %w (stderr=%s)", sessionID, err, stderr.String())
	}
	var exp ocExport
	if err := json.Unmarshal(stdout.Bytes(), &exp); err != nil {
		return "", fmt.Errorf("opencode export %s: parse: %w", sessionID, err)
	}
	// Concatenate text parts from the final assistant message.
	var lastText strings.Builder
	for _, m := range exp.Messages {
		if m.Info.Role != "assistant" {
			continue
		}
		lastText.Reset()
		for _, p := range m.Parts {
			if p.Type == "text" {
				lastText.WriteString(p.Text)
			}
		}
	}
	return lastText.String(), nil
}
