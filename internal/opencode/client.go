package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// serverConn is a long-lived handle to one `opencode serve` process (one per
// vendor — see server.go). It owns the HTTP client, a single shared SSE
// subscription to GET /event, and a registry that demultiplexes events to the
// per-turn listener that owns each session. The opencode server is the canonical
// integration surface (the TUI uses the same API); we talk to it over HTTP +
// SSE instead of scraping `opencode run` stdout, which dropped trailing events
// and forced the old export/settle hack.
type serverConn struct {
	baseURL string
	httpc   *http.Client
	logger  *slog.Logger

	cmd    processHandle // nil for test connections (no child to supervise)
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	listeners map[string]chan event
	closed    bool
	exited    bool // set by the reaper when the child process exits
}

// processHandle is the slice of *exec.Cmd serverConn needs; an interface so
// tests can inject a connection with no real child.
type processHandle interface {
	pid() int
	wait() error
}

func newServerConn(baseURL string, httpc *http.Client, logger *slog.Logger, cmd processHandle) *serverConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &serverConn{
		baseURL:   strings.TrimRight(baseURL, "/"),
		httpc:     httpc,
		logger:    logger,
		cmd:       cmd,
		ctx:       ctx,
		cancel:    cancel,
		listeners: map[string]chan event{},
	}
}

// --- HTTP API ---

// createSession opens a fresh session whose working directory is dir. The
// directory is a per-request query param, so one server serves every thread's
// cwd (verified against opencode 1.15.13). Statelessness is preserved: espur
// creates a new session per turn and never reuses one.
func (c *serverConn) createSession(ctx context.Context, dir string) (string, error) {
	path := "/session"
	if dir != "" {
		path += "?directory=" + url.QueryEscape(dir)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, path, map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("opencode: create session: empty id")
	}
	return out.ID, nil
}

// sendMessage delivers the user prompt synchronously: the call blocks until the
// turn goes idle (through any tool calls) and returns the final assistant
// message with its parts — the authoritative answer, with no dropped events and
// no settle race. Live per-message streaming happens in parallel over SSE.
func (c *serverConn) sendMessage(ctx context.Context, sid, providerID, modelID, text string) (apiMessage, error) {
	body := map[string]any{
		"model": map[string]string{"providerID": providerID, "modelID": modelID},
		"parts": []map[string]any{{"type": "text", "text": text}},
	}
	var out apiMessage
	err := c.doJSON(ctx, http.MethodPost, "/session/"+sid+"/message", body, &out)
	return out, err
}

// listMessages returns every message in the session, authoritatively, after the
// turn is idle. This is the export replacement: the data is already complete in
// the server when the turn ends, so there is no settle race to retry around.
func (c *serverConn) listMessages(ctx context.Context, sid string) ([]apiMessage, error) {
	var out []apiMessage
	err := c.doJSON(ctx, http.MethodGet, "/session/"+sid+"/message?limit=200", nil, &out)
	return out, err
}

// abort cancels an in-flight turn (the timeout path).
func (c *serverConn) abort(ctx context.Context, sid string) error {
	return c.doJSON(ctx, http.MethodPost, "/session/"+sid+"/abort", map[string]any{}, nil)
}

// healthy reports whether GET /global/health says the server is up.
func (c *serverConn) healthy(ctx context.Context) bool {
	var out struct {
		Healthy bool `json:"healthy"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/global/health", nil, &out); err != nil {
		return false
	}
	return out.Healthy
}

// doJSON performs one request and decodes a JSON response into out (out may be
// nil to ignore the body). A non-2xx status is an error carrying the body, so
// the caller can classify provider failures.
func (c *serverConn) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// httpError is a non-2xx response. Its Error string feeds vendor.Classify, so it
// includes the body (which carries the provider's statusCode/message).
type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("opencode http %d: %s", e.Status, e.Body)
}

// --- SSE subscription ---

// subscribe registers a listener for one session's events and returns it with an
// unsubscribe func. The channel is buffered; if it ever fills, the SSE reader
// drops events for this session rather than stalling every other session — the
// post-turn reconcile against listMessages guarantees no message is lost.
func (c *serverConn) subscribe(sid string) (<-chan event, func()) {
	ch := make(chan event, 256)
	c.mu.Lock()
	c.listeners[sid] = ch
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.listeners, sid)
		c.mu.Unlock()
	}
}

func (c *serverConn) route(ev event) {
	if ev.SessionID == "" {
		return
	}
	c.mu.Lock()
	ch := c.listeners[ev.SessionID]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
		c.logger.Debug("opencode sse listener buffer full; dropping event (reconcile will recover)",
			"session_id", ev.SessionID, "event_type", ev.Type)
	}
}

// runSSE maintains the GET /event stream for this server's lifetime, reconnecting
// while the connection is open. One reader per server fans every event out to the
// owning per-session listener.
func (c *serverConn) runSSE() {
	for c.ctx.Err() == nil {
		if err := c.streamOnce(); err != nil && c.ctx.Err() == nil {
			c.logger.Debug("opencode sse stream ended; reconnecting", "err", err.Error())
		}
		if c.ctx.Err() != nil {
			return
		}
		// Brief pause before reconnecting so a flapping server doesn't spin.
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (c *serverConn) streamOnce() error {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.baseURL+"/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode /event status %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "": // blank line terminates one SSE frame
			if data.Len() > 0 {
				c.dispatch(data.String())
				data.Reset()
			}
		}
	}
	return sc.Err()
}

// dispatch parses one SSE data payload and routes the event the listener cares
// about. Unknown event types are ignored.
func (c *serverConn) dispatch(payload string) {
	var env sseEnvelope
	if json.Unmarshal([]byte(payload), &env) != nil {
		return
	}
	switch env.Type {
	case "message.part.updated", "message.updated", "session.error", "session.idle":
		c.route(event{
			Type:      env.Type,
			SessionID: env.Properties.SessionID,
			Part:      env.Properties.Part,
			Info:      env.Properties.Info,
			Error:     env.Properties.Error,
		})
	}
}

// --- lifecycle ---

// startReaper waits on the child so it's never a zombie and records exit so the
// manager can respawn on the next turn. No-op for test connections.
func (c *serverConn) startReaper() {
	if c.cmd == nil {
		return
	}
	go func() {
		_ = c.cmd.wait()
		c.mu.Lock()
		c.exited = true
		c.mu.Unlock()
		c.cancel() // stop the SSE reader; this server is gone
	}()
}

// alive reports whether this connection is still usable (open and, for a real
// server, the child hasn't exited).
func (c *serverConn) alive() bool {
	if c.ctx.Err() != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && !c.exited
}

// close stops the SSE reader and kills the child process group (if any).
func (c *serverConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	if c.cmd != nil {
		killProcess(c.cmd)
	}
}

// --- wire types ---

// sseEnvelope is the shape of one GET /event payload we read (opencode 1.15.13).
type sseEnvelope struct {
	Type       string `json:"type"`
	Properties struct {
		SessionID string          `json:"sessionID"`
		Part      *evtPart        `json:"part"`
		Info      *apiMsgInfo     `json:"info"`
		Error     json.RawMessage `json:"error"`
	} `json:"properties"`
}

// event is the demultiplexed event handed to a session's listener.
type event struct {
	Type      string
	SessionID string
	Part      *evtPart
	Info      *apiMsgInfo
	Error     json.RawMessage
}

type evtPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

// apiMessage mirrors {info, parts} from POST /message and GET /message.
type apiMessage struct {
	Info  apiMsgInfo `json:"info"`
	Parts []apiPart  `json:"parts"`
}

type apiMsgInfo struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Time struct {
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error json.RawMessage `json:"error"`
}

type apiPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// text concatenates the message's text parts.
func (m apiMessage) text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
