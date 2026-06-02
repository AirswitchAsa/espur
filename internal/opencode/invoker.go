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

// DefaultExportTimeout caps a single `opencode export` invocation. The export
// itself is a sub-second DB read; this only guards against a hung process.
const DefaultExportTimeout = 30 * time.Second

// Export retry tuning. After a run, a fresh `opencode export` returns empty (and
// errors with "unexpected end of JSON input") for several seconds while the
// just-written session becomes visible in opencode's SQLite store — no external
// process holds the db, it's an internal visibility delay, and it grows with
// session size (observed 4–7s on large research turns). So retry export with
// capped exponential backoff until exportRetryBudget elapses, then give up. The
// budget is a var (not const) so tests can shrink it.
var exportRetryBudget = 45 * time.Second

const (
	exportBackoffMin = 300 * time.Millisecond
	exportBackoffMax = 2 * time.Second
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

	// OnMessage, if non-nil, is invoked once per completed assistant text
	// message as the run streams, in first-seen order. This is how progressive
	// "post each message as the agent produces it" delivery works: each call is
	// one finished opencode assistant message (its text parts concatenated). The
	// final message is also delivered this way — either live (its step_finish
	// arrived on stdout) or, if opencode dropped the trailing text event, via the
	// post-run export backstop. Callbacks run synchronously on the stdout-reader
	// goroutine during the run and on the caller's goroutine during the backstop;
	// they never overlap, so the callback need not be reentrant.
	OnMessage func(text string)
}

// Result is the terminal outcome of one invocation attempt.
type Result struct {
	Outcome       Outcome
	AssistantText string   // populated on Success: the final assistant message.
	Messages      []string // populated on Success: every assistant message, in order.
	Streamed      bool     // true if ≥1 message was delivered via Request.OnMessage.
	Stderr        string   // captured for diagnostics / failure classification.
	Stdout        string   // raw NDJSON; kept for diagnostics.
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

	// Stream stdout: we read opencode's NDJSON line-by-line as it arrives so each
	// assistant message can be posted the moment it completes, rather than after
	// the whole run. Stderr is small and only consulted post-mortem for failure
	// classification, so it stays a plain buffer. No TTY: pipes only.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Outcome: OutcomeCrash, CrashReason: "spawn_error"},
			fmt.Errorf("opencode: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
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

	// Drain + parse stdout on its own goroutine. The parser accumulates the raw
	// bytes (for diagnostics / Classify) and emits each completed assistant
	// message via req.OnMessage. We must read the pipe to completion before
	// inspecting parser state, hence scanDone.
	sp := newStreamParser(req.OnMessage)
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(stdoutPipe)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			sp.feed(sc.Bytes())
		}
	}()

	// Wait in a goroutine so we can race against runCtx.Done().
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	var timedOut bool
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
	<-scanDone // all stdout drained & parsed; parser state is now safe to read.

	res := Result{
		Stdout:   sp.rawStdout(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
		Messages: sp.messages(),
		Streamed: sp.count() > 0,
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
		// Any messages already streamed stand; the caller posts the timeout note.
		res.Outcome = OutcomeTimeout
		return res, nil
	}

	if sp.sessionID == "" {
		res.Outcome = OutcomeCrash
		res.CrashReason = "no_parseable_json"
		return res, nil
	}

	// Flush the final message from stdout. opencode often exits right after the
	// last message's text without emitting its closing step_finish, so that text
	// is in the parser but not yet delivered. The process exited cleanly (not a
	// timeout — handled above), so the accumulated text is complete: emit it
	// live and skip the export round-trip. Only a genuinely dropped final text
	// event leaves nothing to flush, falling through to the backstop.
	sp.flushFinal()

	// Export backstop. The stdout stream is the primary source: each assistant
	// message is emitted as its step_finish arrives. But opencode intermittently
	// drops the trailing type=text event, so the final message's text can be
	// missing from stdout even though the run produced it. The session export
	// holds it — once the freshly-written session settles in opencode's store.
	//
	// We only export when stdout left a gap: either no message streamed at all,
	// or the final message (the last messageID stdout announced via step_start)
	// never got its text. The retry is keyed on that final messageID — we wait
	// until the export actually contains it, instead of accepting the first
	// non-empty read. That precision is what stops a stale mid-turn snapshot from
	// serving an earlier preamble as the answer.
	//
	// Export's context is the caller's, not runCtx: if `opencode run` finished
	// right at the deadline, runCtx may already be canceled and exec would kill
	// `opencode export` before it prints anything.
	var exportErr error
	if sp.count() == 0 || !sp.emittedFinal() {
		var msgs []exportMsg
		msgs, exportErr = exportAssistantMessagesRetry(ctx, bin, sp.sessionID, sp.finalMessageID(), req.Vendor.CredEnv)
		if exportErr == nil {
			for _, em := range msgs {
				// Emit only messages stdout didn't already deliver, preserving
				// order; the recovered tail goes out after the streamed prefix.
				sp.emitExternal(em.ID, em.Text)
			}
		}
	}

	texts := sp.messages()
	if len(texts) == 0 {
		// Nothing from either source. Distinguish a hard export failure from a
		// run that simply produced no assistant text (spec: "A zero exit code
		// with no usable assistant text is also a crash").
		if exportErr != nil {
			res.Outcome = OutcomeCrash
			res.CrashReason = "export_failed"
			return res, exportErr
		}
		res.Outcome = OutcomeCrash
		res.CrashReason = "no_assistant_text"
		return res, nil
	}
	res.Messages = texts
	res.Streamed = sp.count() > 0
	res.AssistantText = texts[len(texts)-1]
	res.Outcome = OutcomeSuccess
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
// We care about assistant messages: their id (to match the run's messageID),
// their text parts, and whether they carry a tool call (a settle signal).
type ocExport struct {
	Messages []struct {
		Info struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"messages"`
}

// exportMsg is one assistant message distilled from the session export.
type exportMsg struct {
	ID      string
	Text    string
	HasTool bool
}

// streamParser consumes `opencode run --format json` NDJSON line-by-line as the
// child writes it, reconstructs each assistant message, and emits it via
// onMessage the moment it completes (its step_finish event). opencode emits one
// "message" per step (a model call); a message that calls a tool ends its step
// and the model continues in a new message, so step_finish is a reliable
// per-message boundary.
//
// Text parts stream as repeated events for the same part id (growing text), so
// text is keyed by part id with last-value-wins and concatenated in first-seen
// order — the same reconstruction the export uses. Tool-only and empty messages
// produce no emit.
type streamParser struct {
	onMessage func(string)

	raw       bytes.Buffer                 // verbatim stdout, for diagnostics/Classify
	sessionID string                       // first sessionID seen
	lastMsgID string                       // most recent messageID announced (step_start/text)
	text      map[string]map[string]string // messageID -> partID -> text
	partOrder map[string][]string          // messageID -> partID first-seen order
	emitted   map[string]bool              // messageID -> already emitted
	order     []string                     // emitted messages, in emit order
	byID      map[string]string            // messageID -> emitted text
	extSeq    int                          // synthesizes keys for id-less backstop messages
}

func newStreamParser(onMessage func(string)) *streamParser {
	return &streamParser{
		onMessage: onMessage,
		text:      map[string]map[string]string{},
		partOrder: map[string][]string{},
		emitted:   map[string]bool{},
		byID:      map[string]string{},
	}
}

// streamEvent is the slice of an NDJSON run event the parser reads.
type streamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		ID        string `json:"id"`
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
	} `json:"part"`
}

// feed parses one raw stdout line, updating state and emitting on a boundary.
func (sp *streamParser) feed(line []byte) {
	sp.raw.Write(line)
	sp.raw.WriteByte('\n')

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return
	}
	var ev streamEvent
	if json.Unmarshal(trimmed, &ev) != nil {
		return
	}
	if sp.sessionID == "" && ev.SessionID != "" {
		sp.sessionID = ev.SessionID
	}
	mid := ev.Part.MessageID
	switch ev.Type {
	case "step_start":
		if mid != "" {
			sp.lastMsgID = mid
		}
	case "text":
		if mid == "" || ev.Part.Type != "text" || ev.Part.Text == "" {
			return
		}
		sp.lastMsgID = mid
		if sp.text[mid] == nil {
			sp.text[mid] = map[string]string{}
		}
		if _, seen := sp.text[mid][ev.Part.ID]; !seen {
			sp.partOrder[mid] = append(sp.partOrder[mid], ev.Part.ID)
		}
		sp.text[mid][ev.Part.ID] = ev.Part.Text
	case "step_finish":
		if mid == "" {
			mid = sp.lastMsgID
		}
		sp.emitIfText(mid)
	}
}

// assembled concatenates a message's text parts in first-seen order.
func (sp *streamParser) assembled(mid string) string {
	var b strings.Builder
	for _, pid := range sp.partOrder[mid] {
		b.WriteString(sp.text[mid][pid])
	}
	return b.String()
}

// emitIfText delivers a completed message's text exactly once, if non-empty.
func (sp *streamParser) emitIfText(mid string) {
	sp.deliver(mid, sp.assembled(mid))
}

// flushFinal emits the last announced message from its accumulated stdout text
// if it has not already been delivered (no closing step_finish arrived). Called
// only after a clean exit, so the text is complete.
func (sp *streamParser) flushFinal() {
	if sp.lastMsgID != "" {
		sp.emitIfText(sp.lastMsgID)
	}
}

// emitExternal delivers text recovered from the export backstop for a message
// stdout didn't already emit, preserving the "emit once" and ordering contract.
// A message already streamed (matched by id) is skipped; an id-less export
// message (malformed / test fixture) gets a synthetic key so it still emits.
func (sp *streamParser) emitExternal(mid, text string) {
	if mid == "" {
		mid = fmt.Sprintf("__ext_%d", sp.extSeq)
		sp.extSeq++
	}
	sp.deliver(mid, text)
}

func (sp *streamParser) deliver(mid, text string) {
	if mid == "" || sp.emitted[mid] || strings.TrimSpace(text) == "" {
		return
	}
	sp.emitted[mid] = true
	sp.order = append(sp.order, mid)
	sp.byID[mid] = text
	if sp.onMessage != nil {
		sp.onMessage(text)
	}
}

func (sp *streamParser) rawStdout() string { return sp.raw.String() }
func (sp *streamParser) count() int        { return len(sp.order) }

// finalMessageID is the last messageID the run announced — the message the
// export backstop must wait for before it is considered settled.
func (sp *streamParser) finalMessageID() string { return sp.lastMsgID }

// emittedFinal reports whether the final announced message was already emitted
// from stdout (so no export tail recovery is needed).
func (sp *streamParser) emittedFinal() bool {
	return sp.lastMsgID != "" && sp.emitted[sp.lastMsgID]
}

// messages returns the emitted assistant messages in order.
func (sp *streamParser) messages() []string {
	out := make([]string, 0, len(sp.order))
	for _, mid := range sp.order {
		out = append(out, sp.byID[mid])
	}
	return out
}

// exportAssistantMessagesRetry reads the session's assistant messages, retrying
// while the export errors (a read-only, idempotent DB read) or has not yet
// settled. Right after `opencode run` exits, a fresh export can error with empty
// output for several seconds — the just-written session is not yet visible in
// opencode's SQLite store, and that window grows with session size (observed
// 4–7s on large research turns against opencode 1.15.13). The session can also
// be visible but mid-write: present yet missing its final message.
//
// "Settled" is keyed on finalID (the last messageID the run streamed): the
// export is settled once it contains that message. This is far more precise than
// "first non-empty read" — it is exactly what prevents a stale snapshot whose
// tail is still a preamble from being accepted as the answer. When finalID is
// unknown, fall back to "the last assistant message carries no tool call" (a
// tool-call tail means the model will continue, i.e. mid-turn).
//
// On budget exhaustion the last (msgs, err) is returned verbatim so the caller
// still distinguishes export_failed (err != nil) from no_assistant_text.
func exportAssistantMessagesRetry(parent context.Context, bin, sessionID, finalID string, creds map[string]string) ([]exportMsg, error) {
	ctx, cancel := context.WithTimeout(parent, exportRetryBudget)
	defer cancel()
	settled := func(msgs []exportMsg) bool {
		if finalID == "" {
			// stdout never announced a final message (degenerate: no step_start
			// reached us). With no target to wait for, accept the first
			// successful read — a parsed export is then definitive, including a
			// definitive "no assistant message" (matches the legacy semantics
			// and keeps the no_assistant_text path fast).
			return true
		}
		for _, m := range msgs {
			if m.ID == finalID {
				return true
			}
		}
		return false
	}
	var (
		msgs    []exportMsg
		err     error
		backoff = exportBackoffMin
	)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return msgs, err // budget exhausted — surface the last result
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > exportBackoffMax {
				backoff = exportBackoffMax
			}
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, DefaultExportTimeout)
		msgs, err = exportAssistantMessages(attemptCtx, bin, sessionID, creds)
		attemptCancel()
		if err == nil && settled(msgs) {
			return msgs, nil
		}
		if ctx.Err() != nil {
			return msgs, err
		}
	}
}

// exportAssistantMessages returns the session's assistant messages in order.
func exportAssistantMessages(ctx context.Context, bin, sessionID string, creds map[string]string) ([]exportMsg, error) {
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
		return nil, fmt.Errorf("opencode export %s: %w (stderr=%s)", sessionID, err, stderr.String())
	}
	var exp ocExport
	if err := json.Unmarshal(stdout.Bytes(), &exp); err != nil {
		return nil, fmt.Errorf("opencode export %s: parse: %w", sessionID, err)
	}
	var out []exportMsg
	for _, m := range exp.Messages {
		if m.Info.Role != "assistant" {
			continue
		}
		var b strings.Builder
		hasTool := false
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				b.WriteString(p.Text)
			case "tool":
				hasTool = true
			}
		}
		out = append(out, exportMsg{ID: m.Info.ID, Text: b.String(), HasTool: hasTool})
	}
	return out, nil
}
