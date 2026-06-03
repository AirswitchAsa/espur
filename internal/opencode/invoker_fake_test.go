package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive Invoke against a fake opencode HTTP server (httptest)
// implementing the slice of the server API espur uses: /global/health,
// POST /session, POST /session/:id/message, GET /event (SSE), GET
// /session/:id/message, POST /session/:id/abort. A test Servers points Invoke at
// it without spawning a real `opencode serve`. This replaces the old
// re-exec-as-fake-CLI scheme: the transport is now HTTP + SSE, not stdout NDJSON.

// --- fake opencode server ---

// fakeMsg is one scripted assistant message in a turn.
type fakeMsg struct {
	id   string
	text string // "" → a tool-only message (emits no reply)
	tool bool
}

type fakeOC struct {
	mu   sync.Mutex
	msgs []fakeMsg

	// behaviour knobs
	dropSSE      map[string]bool // messageIDs whose text part.updated is NOT streamed
	listStatus   int             // GET /message status (0 → 200)
	sessionError string          // emit a session.error with this JSON, then idle
	sendStatus   int             // POST /message status (0 → 200)
	sendBody     string          // body for a non-2xx send
	sendDelay    time.Duration   // block the POST this long (timeout path)

	subs    map[chan string]bool
	aborted bool
}

func newFakeOC(msgs []fakeMsg) *fakeOC {
	return &fakeOC{msgs: msgs, dropSSE: map[string]bool{}, subs: map[chan string]bool{}}
}

func (f *fakeOC) publish(ev string) {
	f.mu.Lock()
	subs := make([]chan string, 0, len(f.subs))
	for ch := range f.subs {
		subs = append(subs, ch)
	}
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (f *fakeOC) subCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

func (f *fakeOC) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"healthy": true})
	})

	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": "ses_fake"})
	})

	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		ch := make(chan string, 256)
		f.mu.Lock()
		f.subs[ch] = true
		f.mu.Unlock()
		defer func() { f.mu.Lock(); delete(f.subs, ch); f.mu.Unlock() }()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"server.connected"}`)
		if flusher != nil {
			flusher.Flush()
		}
		for {
			select {
			case ev := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", ev)
				if flusher != nil {
					flusher.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	})

	mux.HandleFunc("/session/ses_fake/message", func(w http.ResponseWriter, r *http.Request) {
		// Timeout path: block until the client cancels (Invoke hit its deadline).
		if f.sendDelay > 0 {
			select {
			case <-time.After(f.sendDelay):
			case <-r.Context().Done():
				return
			}
		}
		// Wait for the SSE subscriber so streamed events are delivered live and
		// deterministically (Invoke registers its listener before this POST).
		for i := 0; i < 200 && f.subCount() == 0; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if f.sessionError != "" {
			f.publish(`{"type":"session.error","properties":{"sessionID":"ses_fake","error":` + f.sessionError + `}}`)
		}
		f.streamTurn()
		f.publish(`{"type":"session.idle","properties":{"sessionID":"ses_fake"}}`)

		if f.sendStatus != 0 && f.sendStatus != http.StatusOK {
			w.WriteHeader(f.sendStatus)
			_, _ = w.Write([]byte(f.sendBody))
			return
		}
		writeJSON(w, f.finalMessage())
	})

	mux.HandleFunc("/session/ses_fake/abort", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.aborted = true
		f.mu.Unlock()
		writeJSON(w, true)
	})

	return methodSplit(mux, f)
}

// methodSplit routes GET /session/ses_fake/message (the authoritative list),
// which shares a URL with the POST send path; net/http's mux can't branch by
// method, so we do it here.
func methodSplit(mux *http.ServeMux, f *fakeOC) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_fake/message" && r.Method == http.MethodGet {
			if f.listStatus != 0 && f.listStatus != http.StatusOK {
				w.WriteHeader(f.listStatus)
				return
			}
			writeJSON(w, f.allMessages())
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// streamTurn emits SSE events for each scripted message: a text part update
// (unless dropped) then a completed message.updated.
func (f *fakeOC) streamTurn() {
	for _, m := range f.msgs {
		if m.text != "" && !f.dropSSE[m.id] {
			part := map[string]any{
				"type": "message.part.updated",
				"properties": map[string]any{
					"sessionID": "ses_fake",
					"part":      map[string]any{"id": "prt_" + m.id, "messageID": m.id, "type": "text", "text": m.text},
				},
			}
			f.publish(mustJSON(part))
		}
		info := map[string]any{
			"type": "message.updated",
			"properties": map[string]any{
				"sessionID": "ses_fake",
				"info":      map[string]any{"id": m.id, "role": "assistant", "time": map[string]any{"completed": 1}},
			},
		}
		f.publish(mustJSON(info))
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fakeOC) finalMessage() apiMessage {
	if len(f.msgs) == 0 {
		return apiMessage{}
	}
	return msgToAPI(f.msgs[len(f.msgs)-1])
}

func (f *fakeOC) allMessages() []apiMessage {
	out := make([]apiMessage, 0, len(f.msgs)+1)
	out = append(out, apiMessage{Info: apiMsgInfo{ID: "msg_user", Role: "user"}, Parts: []apiPart{{Type: "text", Text: "x"}}})
	for _, m := range f.msgs {
		out = append(out, msgToAPI(m))
	}
	return out
}

func msgToAPI(m fakeMsg) apiMessage {
	parts := []apiPart{}
	if m.text != "" {
		parts = append(parts, apiPart{Type: "text", Text: m.text})
	}
	if m.tool {
		parts = append(parts, apiPart{Type: "tool"})
	}
	return apiMessage{Info: apiMsgInfo{ID: m.id, Role: "assistant"}, Parts: parts}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// --- test harness ---

// runInvoke spins up the fake server, points a test Servers at it, and runs one
// Invoke, returning the streamed messages alongside the result.
func runInvoke(t *testing.T, f *fakeOC, timeout time.Duration) ([]string, Result, error) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	conn := newServerConn(srv.URL, &http.Client{}, slog.Default(), nil)
	go conn.runSSE()
	t.Cleanup(conn.close)
	servers := &Servers{testConn: conn, logger: slog.Default(), httpc: conn.httpc}

	var streamed []string
	res, err := servers.Invoke(context.Background(), Request{
		Vendor:    Vendor{VendorID: "v", Model: "prov/model"},
		WorkDir:   t.TempDir(),
		UserMsg:   "<request>x</request>",
		Timeout:   timeout,
		OnMessage: func(text string) { streamed = append(streamed, text) },
	})
	return streamed, res, err
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

// --- tests ---

func TestInvoke_Fake_StreamsEachMessageLive(t *testing.T) {
	// A preamble, a tool-only step, then the answer. Each text-bearing message is
	// posted as it completes. The authoritative GET /message is forced to fail
	// (listStatus 500), so the preamble can ONLY have reached the user via the
	// live SSE stream — proving live delivery, not a post-turn backstop.
	f := newFakeOC([]fakeMsg{
		{id: "m1", text: "let me check~"},
		{id: "m2", tool: true},
		{id: "m3", text: "the real answer"},
	})
	f.listStatus = 500 // authoritative list unavailable → fallback to sync final

	streamed, res, err := runInvoke(t, f, 5*time.Second)
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

func TestInvoke_Fake_ToolOnlyMessagesEmitNothing(t *testing.T) {
	// A tool-only message carries no text and must not produce a reply; only the
	// final text message does.
	f := newFakeOC([]fakeMsg{
		{id: "m1", tool: true},
		{id: "m2", text: "done"},
	})
	streamed, res, err := runInvoke(t, f, 5*time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !equalStrings(streamed, []string{"done"}) {
		t.Fatalf("streamed=%v want [done]", streamed)
	}
	if res.AssistantText != "done" {
		t.Fatalf("AssistantText=%q", res.AssistantText)
	}
}

func TestInvoke_Fake_RecoversDroppedFinalFromList(t *testing.T) {
	// opencode streamed the preamble but DROPPED the final message's text event
	// over SSE. The authoritative GET /message recovers it and it's delivered
	// after the streamed preamble — answer last. This is the wechat:4ae004
	// regression: the authoritative read returns the real final, never a preamble.
	f := newFakeOC([]fakeMsg{
		{id: "m4", text: "let me look again~"},
		{id: "m5", text: "you cannot work in Sweden on a Danish permit alone"},
	})
	f.dropSSE["m5"] = true // final message's text never streams

	streamed, res, err := runInvoke(t, f, 5*time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := []string{"let me look again~", "you cannot work in Sweden on a Danish permit alone"}
	if !equalStrings(streamed, want) {
		t.Fatalf("streamed=%v want=%v", streamed, want)
	}
	if res.AssistantText != want[1] {
		t.Fatalf("AssistantText=%q want the recovered final answer, not the preamble", res.AssistantText)
	}
}

func TestInvoke_Fake_BackstopOnlyNoSSE(t *testing.T) {
	// Nothing streams over SSE at all (both messages dropped); the whole answer
	// comes from the authoritative list. Still delivered, in order.
	f := newFakeOC([]fakeMsg{
		{id: "m1", text: "first"},
		{id: "m2", text: "second"},
	})
	f.dropSSE["m1"] = true
	f.dropSSE["m2"] = true

	streamed, res, err := runInvoke(t, f, 5*time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !equalStrings(streamed, []string{"first", "second"}) {
		t.Fatalf("streamed=%v", streamed)
	}
	if res.Outcome != OutcomeSuccess || res.AssistantText != "second" {
		t.Fatalf("outcome=%s text=%q", res.Outcome, res.AssistantText)
	}
}

func TestInvoke_Fake_NoAssistantText(t *testing.T) {
	// A clean turn that produced only a tool call and no text → no_assistant_text.
	f := newFakeOC([]fakeMsg{{id: "m1", tool: true}})
	_, res, _ := runInvoke(t, f, 5*time.Second)
	if res.Outcome != OutcomeCrash || res.CrashReason != "no_assistant_text" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
}

func TestInvoke_Fake_VendorErrorFromSessionError(t *testing.T) {
	// A structured session.error with a rate-limit shape, no text. Surfaces as a
	// vendor_error crash whose Stderr carries the provider detail for classify.
	f := newFakeOC(nil)
	f.sessionError = `{"name":"APIError","data":{"statusCode":429,"message":"rate limit exceeded"}}`
	_, res, err := runInvoke(t, f, 5*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Outcome != OutcomeCrash || res.CrashReason != "vendor_error" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
	if !strings.Contains(res.Stderr, "429") || !strings.Contains(strings.ToLower(res.Stderr), "rate limit") {
		t.Fatalf("Stderr lacks classifiable detail: %q", res.Stderr)
	}
}

func TestInvoke_Fake_VendorErrorFromHTTPStatus(t *testing.T) {
	// A non-2xx send (auth failure) surfaces as a vendor_error crash; the body
	// reaches Stderr for classification.
	f := newFakeOC(nil)
	f.sendStatus = 401
	f.sendBody = `{"error":{"message":"invalid api key","statusCode":401}}`
	_, res, err := runInvoke(t, f, 5*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Outcome != OutcomeCrash || res.CrashReason != "vendor_error" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.CrashReason)
	}
	if !strings.Contains(strings.ToLower(res.Stderr), "invalid api key") {
		t.Fatalf("Stderr=%q", res.Stderr)
	}
}

func TestInvoke_Fake_Timeout(t *testing.T) {
	f := newFakeOC([]fakeMsg{{id: "m1", text: "never"}})
	f.sendDelay = 3 * time.Second // longer than the Invoke timeout below

	start := time.Now()
	_, res, err := runInvoke(t, f, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("want Timeout, got %s", res.Outcome)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Invoke took too long to return: %s", elapsed)
	}
	if !f.aborted {
		t.Fatalf("expected the turn to be aborted on the server")
	}
}

func TestMsgAccumulator_EmitsOncePerMessage(t *testing.T) {
	var got []string
	a := newMsgAccumulator(func(s string) { got = append(got, s) })
	// m1: two parts, last-wins on the first part id.
	a.handle(ev("message.part.updated", part("m1", "p1", "par")), new(json.RawMessage))
	a.handle(ev("message.part.updated", part("m1", "p1", "partial")), new(json.RawMessage))
	a.handle(ev("message.part.updated", part("m1", "p2", " done")), new(json.RawMessage))
	a.handle(completed("m1"), new(json.RawMessage))
	// m2: tool-only (no text part) → completion emits nothing.
	a.handle(completed("m2"), new(json.RawMessage))
	// reconcile re-emitting m1 is a no-op; m3 via reconcile path.
	a.emitText("m1", "partial done")
	a.emitText("m3", "third")

	want := []string{"partial done", "third"}
	if !equalStrings(got, want) {
		t.Fatalf("emitted=%v want=%v", got, want)
	}
	if a.count() != 2 {
		t.Fatalf("count=%d", a.count())
	}
}

func TestSplitModel(t *testing.T) {
	for _, tc := range []struct{ in, p, m string }{
		{"deepseek/deepseek-v4-pro", "deepseek", "deepseek-v4-pro"},
		{"anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6"},
		{"bare-model", "", "bare-model"},
	} {
		p, m := splitModel(tc.in)
		if p != tc.p || m != tc.m {
			t.Fatalf("splitModel(%q)=%q,%q want %q,%q", tc.in, p, m, tc.p, tc.m)
		}
	}
}

// --- event builders for the accumulator unit test ---

func ev(typ string, part *evtPart) event {
	return event{Type: typ, SessionID: "s", Part: part}
}
func part(mid, pid, text string) *evtPart {
	return &evtPart{ID: pid, MessageID: mid, Type: "text", Text: text}
}
func completed(mid string) event {
	return event{Type: "message.updated", SessionID: "s",
		Info: &apiMsgInfo{ID: mid, Role: "assistant", Time: struct {
			Completed int64 `json:"completed"`
		}{Completed: 1}}}
}

// --- buildEnv tests (unchanged behaviour) ---

func TestBuildEnv_PassthroughForwardsListedVars(t *testing.T) {
	t.Setenv("ESPUR_OPENCODE_ENV_PASSTHROUGH", "EXA_API_KEY, XAI_API_KEY ,UNSET_VAR")
	t.Setenv("EXA_API_KEY", "exa-secret")
	t.Setenv("XAI_API_KEY", "xai-secret")
	t.Setenv("UNSET_VAR_OFF", "x")

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
