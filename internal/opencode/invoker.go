// Package opencode drives the `opencode` agent through its headless HTTP server
// (`opencode serve`) — the same API surface opencode's own TUI uses. espur runs
// one server per vendor (see server.go), sends each turn as a synchronous prompt,
// and streams the assistant's messages live over the server's SSE event bus.
// See docs/specs/opencode-invoke.dog.md for the behavioral contract.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout is the per-invocation wall clock budget.
// Spec: opencode-invoke.dog.md "Timeout — Default 120 seconds."
const DefaultTimeout = 120 * time.Second

// DefaultKillGrace is the grace period between SIGTERM and SIGKILL when killing a
// server process group on shutdown.
const DefaultKillGrace = 5 * time.Second

// Outcome enumerates the terminal categories defined in
// docs/specs/opencode-invoke.dog.md ("Outcome").
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
// docs/specs/vendor-pool.dog.md.
type Vendor struct {
	// VendorID is the stable identifier (e.g. "chatgpt-oauth"). It also keys the
	// per-vendor opencode server.
	VendorID string
	// Model is opencode's model id (e.g. "deepseek/deepseek-v4-pro"); split into
	// providerID/modelID for the server API.
	Model string
	// CredEnv is the credentials env block exposed to this vendor's server.
	// Per spec: only the credentials of the vendor being attempted are exposed.
	CredEnv map[string]string
}

// Request bundles one invocation attempt.
type Request struct {
	Vendor  Vendor
	WorkDir string // session working directory (the per-thread cwd).
	UserMsg string // composite user message from context assembly.
	Timeout time.Duration

	// OnMessage, if non-nil, is invoked once per completed assistant text
	// message, in order. Each call is one finished opencode assistant message —
	// this is how progressive "post each message as the agent produces it"
	// delivery works. Messages stream live from the SSE event bus as they
	// complete; any that the live stream missed are delivered by the post-turn
	// reconcile against the authoritative message list. Callbacks run
	// synchronously on the Invoke goroutine and never overlap.
	OnMessage func(text string)
}

// Result is the terminal outcome of one invocation attempt.
type Result struct {
	Outcome       Outcome
	AssistantText string   // populated on Success: the final assistant message.
	Messages      []string // every assistant message, in order.
	Streamed      bool     // true if ≥1 message was delivered via Request.OnMessage.
	Stderr        string   // synthesized failure detail, for vendor.Classify.
	Stdout        string   // retained for struct compatibility; unused.
	ExitCode      int      // retained for struct compatibility; unused.
	Duration      time.Duration
	// CrashReason is a short tag explaining a Crash outcome
	// (e.g. "no_assistant_text", "vendor_error", "server_unavailable").
	CrashReason string
}

// Invoke runs one turn against the vendor's opencode server: create a session,
// subscribe to its events, send the prompt synchronously, stream each completed
// assistant message live, and reconcile against the authoritative message list
// so nothing is lost.
func (s *Servers) Invoke(ctx context.Context, req Request) (Result, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	start := time.Now()

	conn, err := s.acquire(ctx, req.Vendor)
	if err != nil {
		return Result{Outcome: OutcomeCrash, CrashReason: "server_unavailable",
			Stderr: err.Error(), Duration: time.Since(start)}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sid, err := conn.createSession(runCtx, req.WorkDir)
	if err != nil {
		return Result{Outcome: OutcomeCrash, CrashReason: "session_create_failed",
			Stderr: err.Error(), Duration: time.Since(start)}, err
	}

	events, unsub := conn.subscribe(sid)
	defer unsub()

	providerID, modelID := splitModel(req.Vendor.Model)
	acc := newMsgAccumulator(req.OnMessage)

	sendCh := make(chan sendResult, 1)
	go func() {
		msg, err := conn.sendMessage(runCtx, sid, providerID, modelID, req.UserMsg)
		sendCh <- sendResult{msg: msg, err: err}
	}()

	var (
		send     sendResult
		gotSend  bool
		sessErr  json.RawMessage
		idleSeen bool
		timedOut bool
	)
	// consume folds one event in, reporting whether it was the turn-end marker.
	consume := func(ev event) bool {
		if ev.Type == "session.idle" {
			return true
		}
		acc.handle(ev, &sessErr)
		return false
	}
loop:
	for {
		select {
		case ev := <-events:
			if consume(ev) {
				idleSeen = true
			}
		case send = <-sendCh:
			gotSend = true
			break loop
		case <-runCtx.Done():
			timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
			break loop
		}
	}

	res := Result{Duration: time.Since(start)}

	// Timeout: abort the turn on the server and return whatever streamed. Spec:
	// a timeout is not a vendor failure; already-streamed messages stand.
	if timedOut && !gotSend {
		actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.abort(actx, sid)
		acancel()
		<-sendCh // reap the send goroutine (sendMessage returns once runCtx cancels)
		res.Outcome = OutcomeTimeout
		res.Messages = acc.messages()
		res.Streamed = acc.count() > 0
		if n := len(res.Messages); n > 0 {
			res.AssistantText = res.Messages[n-1]
		}
		return res, nil
	}

	// Turn finished. The synchronous response can land before the tail of the SSE
	// stream (a final message.updated, or a session.error that precedes idle), so
	// drain through session.idle — bounded, in case idle never arrives — to
	// observe those trailing events. If idle was already seen in the main loop,
	// only the buffered remainder is left.
	if !idleSeen {
		idleDeadline := time.After(500 * time.Millisecond)
	drain:
		for {
			select {
			case ev := <-events:
				if consume(ev) {
					break drain
				}
			case <-idleDeadline:
				break drain
			}
		}
	} else {
		for drained := true; drained; {
			select {
			case ev := <-events:
				consume(ev)
			default:
				drained = false
			}
		}
	}

	recCtx, recCancel := context.WithTimeout(ctx, 10*time.Second)
	msgs, listErr := conn.listMessages(recCtx, sid)
	recCancel()
	if listErr == nil {
		for _, m := range msgs {
			if m.Info.Role == "assistant" {
				acc.emitText(m.Info.ID, m.text())
			}
		}
	} else if gotSend && send.err == nil && send.msg.Info.Role == "assistant" {
		// Authoritative list unavailable — fall back to the sync response, which
		// at least carries the final message.
		acc.emitText(send.msg.Info.ID, send.msg.text())
	}

	res.Messages = acc.messages()
	res.Streamed = acc.count() > 0
	if n := len(res.Messages); n > 0 {
		res.AssistantText = res.Messages[n-1]
	}

	// A genuine vendor/runtime error (structured session.error, a non-2xx send,
	// or an error on the final message) is a crash. Its detail goes to Stderr so
	// vendor.Classify can decide fallthrough vs. penalty. Streamed stays set so
	// the pool won't replay the turn on another vendor after we've already posted.
	if errText := classifyFailure(sessErr, send, gotSend); errText != "" {
		res.Stderr = errText
		res.Outcome = OutcomeCrash
		res.CrashReason = "vendor_error"
		return res, errors.New(errText)
	}

	if len(res.Messages) == 0 {
		// Clean turn that produced no assistant text (e.g. tool-only). Spec: a
		// turn with no usable assistant text is a crash.
		res.Outcome = OutcomeCrash
		res.CrashReason = "no_assistant_text"
		return res, nil
	}

	res.Outcome = OutcomeSuccess
	return res, nil
}

// sendResult carries the synchronous prompt outcome from its goroutine.
type sendResult struct {
	msg apiMessage
	err error
}

// classifyFailure renders any vendor/runtime error into a single string for
// vendor.Classify (which scans for statusCode/auth/rate-limit phrases). Returns
// "" when the turn carried no error.
func classifyFailure(sessErr json.RawMessage, send sendResult, gotSend bool) string {
	var parts []string
	if !isNull(sessErr) {
		parts = append(parts, "session error: "+string(sessErr))
	}
	if gotSend && send.err != nil {
		parts = append(parts, send.err.Error())
	}
	if gotSend && send.err == nil && !isNull(send.msg.Info.Error) {
		parts = append(parts, "message error: "+string(send.msg.Info.Error))
	}
	return strings.Join(parts, "\n")
}

func isNull(r json.RawMessage) bool {
	s := strings.TrimSpace(string(r))
	return s == "" || s == "null"
}

// splitModel divides "provider/model-id" into its providerID and modelID on the
// first slash (e.g. "deepseek/deepseek-v4-pro" → "deepseek", "deepseek-v4-pro").
func splitModel(model string) (providerID, modelID string) {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[:i], model[i+1:]
	}
	return "", model
}

// --- assistant-message accumulator ---

// msgAccumulator reconstructs assistant messages from SSE part updates and
// delivers each completed one exactly once via onMessage, in first-delivered
// order. Text parts arrive as repeated message.part.updated events for the same
// part id (growing text), so text is keyed by part id with last-value-wins and
// concatenated in first-seen order — then emitted when the message completes
// (message.updated with time.completed) or recovered by the reconcile.
type msgAccumulator struct {
	onMessage func(string)

	parts   map[string]map[string]string // messageID -> partID -> text
	order   map[string][]string          // messageID -> partID first-seen order
	emitted map[string]bool              // messageID -> already delivered
	seq     []string                     // delivered messageIDs, in order
	byID    map[string]string            // messageID -> delivered text
	extSeq  int                          // synthesizes keys for id-less messages
}

func newMsgAccumulator(onMessage func(string)) *msgAccumulator {
	return &msgAccumulator{
		onMessage: onMessage,
		parts:     map[string]map[string]string{},
		order:     map[string][]string{},
		emitted:   map[string]bool{},
		byID:      map[string]string{},
	}
}

// handle folds one SSE event into the accumulator, emitting live on completion.
func (a *msgAccumulator) handle(ev event, sessErr *json.RawMessage) {
	switch ev.Type {
	case "message.part.updated":
		if ev.Part != nil && ev.Part.Type == "text" && ev.Part.MessageID != "" {
			a.addPart(ev.Part.MessageID, ev.Part.ID, ev.Part.Text)
		}
	case "message.updated":
		if ev.Info != nil && ev.Info.Role == "assistant" && ev.Info.Time.Completed > 0 {
			a.emitText(ev.Info.ID, a.assemble(ev.Info.ID))
		}
		if ev.Info != nil && !isNull(ev.Info.Error) {
			*sessErr = ev.Info.Error
		}
	case "session.error":
		if !isNull(ev.Error) {
			*sessErr = ev.Error
		}
	}
}

func (a *msgAccumulator) addPart(mid, pid, text string) {
	if a.parts[mid] == nil {
		a.parts[mid] = map[string]string{}
	}
	if _, seen := a.parts[mid][pid]; !seen {
		a.order[mid] = append(a.order[mid], pid)
	}
	a.parts[mid][pid] = text
}

func (a *msgAccumulator) assemble(mid string) string {
	var b strings.Builder
	for _, pid := range a.order[mid] {
		b.WriteString(a.parts[mid][pid])
	}
	return b.String()
}

// emitText delivers text for a message exactly once. An id-less message (a
// malformed/test fixture) gets a synthetic key so it still emits.
func (a *msgAccumulator) emitText(mid, text string) {
	if mid == "" {
		mid = "__ext_" + itoa(a.extSeq)
		a.extSeq++
	}
	if a.emitted[mid] || strings.TrimSpace(text) == "" {
		return
	}
	a.emitted[mid] = true
	a.seq = append(a.seq, mid)
	a.byID[mid] = text
	if a.onMessage != nil {
		a.onMessage(text)
	}
}

func (a *msgAccumulator) count() int { return len(a.seq) }

func (a *msgAccumulator) messages() []string {
	out := make([]string, 0, len(a.seq))
	for _, mid := range a.seq {
		out = append(out, a.byID[mid])
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// buildEnv assembles the minimal env per spec: PATH, HOME, TMPDIR, plus the
// vendor's credentials. The XDG_* variables are passed through too so that
// opencode's own auth.json (used by OAuth providers — see docs/specs/oauth.dog.md)
// is resolved from a shared, persistent location across espur invocations and
// `opencode auth login` runs. Espur's own master key and unrelated vendor creds
// are deliberately excluded — only the attempted vendor's creds reach its server.
func buildEnv(creds map[string]string) []string {
	out := make([]string, 0, 6+len(creds))
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	// Operator-supplied passthrough: comma-separated env var names to forward to
	// opencode servers. Lets a deployment hand the child non-vendor secrets (e.g.
	// keys consumed by user-installed skills) without code changes.
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
