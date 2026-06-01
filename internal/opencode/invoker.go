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
const DefaultExportTimeout = 30 * time.Second

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

	// `opencode run --format json` streams NDJSON events on stdout but, as of
	// opencode 1.15.11, intermittently drops the trailing `type=text` event
	// (the session record has the text — stdout doesn't). The session export
	// is authoritative, so we pull assistant text from there.
	//
	// Use a fresh context derived from the caller's, not runCtx: if
	// `opencode run` finished right at the deadline, runCtx may already be
	// canceled, and exec.CommandContext would kill `opencode export` before it
	// prints anything (manifests as "parse: unexpected end of JSON input").
	exportCtx, exportCancel := context.WithTimeout(ctx, DefaultExportTimeout)
	defer exportCancel()
	text, exportErr := exportAssistantText(exportCtx, bin, sessionID, req.Vendor.CredEnv)
	if exportErr != nil {
		res.Outcome = OutcomeCrash
		res.CrashReason = "export_failed"
		return res, exportErr
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
