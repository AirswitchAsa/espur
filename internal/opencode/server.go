package opencode

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Servers manages one long-lived `opencode serve` process per vendor. A vendor's
// server is started lazily on first use and reused across turns, so the server
// startup cost is paid once, not per turn. Each server's env carries ONLY that
// vendor's credentials (buildEnv), so the per-vendor credential-isolation
// property is preserved exactly as with the old per-invocation child: a
// prompt-injected run in one vendor's session can never read another vendor's
// key. Per-thread working directories are handled per request via the
// ?directory= query param, so a single server per vendor serves all threads.
//
// There is no background supervisor: health is gated at acquire() time. If a
// server died (or never became healthy), the next acquire kills the stale entry
// and spawns a fresh one synchronously.
type Servers struct {
	mu      sync.Mutex
	conns   map[string]*serverConn
	pending map[string]*pendingSpawn // vendor_id -> in-flight spawn (lock released while it runs)
	closed  bool
	binPath string
	logger  *slog.Logger
	httpc   *http.Client

	// testConn, when set, is returned by acquire for every vendor and no real
	// process is spawned. Used by the in-package tests to point Invoke at an
	// httptest server.
	testConn *serverConn
}

// pendingSpawn lets concurrent acquires for the same vendor share one spawn
// instead of each starting (and killing) a redundant server. The map lock is
// released while a spawn runs, so cold-starting one vendor never blocks acquires
// for a different (or already-warm) vendor.
type pendingSpawn struct {
	done chan struct{}
	conn *serverConn
	err  error
}

// healthDeadline bounds how long acquire waits for a freshly spawned server to
// report healthy before giving up. Cold start is low single-digit seconds; this
// leaves generous margin. A var so tests can shrink it.
var healthDeadline = 20 * time.Second

// NewServers builds a manager. binPath is the opencode binary ("" → "opencode"
// on PATH). logger may be nil.
func NewServers(binPath string, logger *slog.Logger) *Servers {
	if binPath == "" {
		binPath = "opencode"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Servers{
		conns:   map[string]*serverConn{},
		pending: map[string]*pendingSpawn{},
		binPath: binPath,
		logger:  logger,
		// One client shared across all servers. No global timeout: a turn can
		// run for minutes and the per-request context (the invocation timeout)
		// is what bounds it. The SSE stream is a long-lived response and must
		// not be cut by a client timeout either.
		httpc: &http.Client{},
	}
}

// SetLogger swaps the logger used for server lifecycle and SSE diagnostics.
func (s *Servers) SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	s.mu.Lock()
	s.logger = l
	for _, conn := range s.conns {
		conn.logger = l
	}
	s.mu.Unlock()
}

// acquire returns a healthy connection for the vendor, starting or restarting
// the server as needed.
func (s *Servers) acquire(ctx context.Context, vendor Vendor) (*serverConn, error) {
	if s.testConn != nil {
		return s.testConn, nil
	}
	id := vendor.VendorID
	for {
		s.mu.Lock()
		if conn := s.conns[id]; conn != nil {
			if conn.alive() {
				s.mu.Unlock()
				return conn, nil
			}
			// Stale/dead entry — tear it down and respawn below.
			conn.close()
			delete(s.conns, id)
		}
		// A spawn for this vendor is already in flight — wait for it instead of
		// starting a second server. The lock is held only to read s.pending.
		if p := s.pending[id]; p != nil {
			s.mu.Unlock()
			select {
			case <-p.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if p.err != nil {
				return nil, p.err
			}
			// Spawn succeeded; loop to pick up the (still-alive) conn.
			continue
		}
		// We own the spawn. Publish a placeholder and release the lock so other
		// vendors' acquires proceed while this one starts (health-wait is slow).
		p := &pendingSpawn{done: make(chan struct{})}
		s.pending[id] = p
		s.mu.Unlock()

		conn, err := s.spawn(ctx, vendor)

		s.mu.Lock()
		delete(s.pending, id)
		if err == nil && s.closed {
			// Manager shut down mid-spawn; don't leak the child.
			conn.close()
			conn, err = nil, fmt.Errorf("opencode: servers closed")
		}
		if err == nil {
			s.conns[id] = conn
		}
		p.conn, p.err = conn, err
		close(p.done)
		s.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// spawn starts a new `opencode serve` for the vendor, waits for it to become
// healthy, and starts its SSE reader.
func (s *Servers) spawn(ctx context.Context, vendor Vendor) (*serverConn, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("opencode: alloc port: %w", err)
	}
	cmd := exec.Command(s.binPath, "serve",
		"--port", strconv.Itoa(port),
		"--hostname", "127.0.0.1",
		"--log-level", "INFO",
	)
	cmd.Env = buildEnv(vendor.CredEnv)
	// serve logs to its XDG log dir; its stderr is noise we don't need.
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	// Own process group so close() can signal the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode: spawn serve: %w", err)
	}

	conn := newServerConn(fmt.Sprintf("http://127.0.0.1:%d", port), s.httpc, s.logger, &cmdHandle{cmd: cmd})
	conn.startReaper()

	if err := waitHealthy(ctx, conn); err != nil {
		conn.close()
		return nil, fmt.Errorf("opencode: serve for %s not healthy: %w", vendor.VendorID, err)
	}
	go conn.runSSE()
	s.logger.Info("opencode server started", "vendor_id", vendor.VendorID, "addr", conn.baseURL)
	return conn, nil
}

// Close kills every running server. Called on espur shutdown after invocations
// have drained.
func (s *Servers) Close() {
	s.mu.Lock()
	s.closed = true
	conns := make([]*serverConn, 0, len(s.conns))
	for id, conn := range s.conns {
		conns = append(conns, conn)
		delete(s.conns, id)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		conn.close()
	}
}

// waitHealthy polls /global/health until the server is up, the per-spawn
// deadline elapses, or the caller's context is cancelled.
func waitHealthy(ctx context.Context, conn *serverConn) error {
	deadline := time.Now().Add(healthDeadline)
	for {
		hctx, cancel := context.WithTimeout(ctx, 1*time.Second)
		ok := conn.healthy(hctx)
		cancel()
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy within %s", healthDeadline)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// freePort asks the OS for an unused loopback TCP port. There's a small window
// between closing the listener and serve binding it; acceptable for a lazily
// started, retried-on-failure server.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// cmdHandle adapts *exec.Cmd to processHandle.
type cmdHandle struct{ cmd *exec.Cmd }

func (h *cmdHandle) pid() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}
func (h *cmdHandle) wait() error { return h.cmd.Wait() }

// killProcess signals the process group SIGTERM, then SIGKILL after a grace
// period, mirroring the run-child kill path.
func killProcess(h processHandle) {
	pid := h.pid()
	if pid <= 0 {
		return
	}
	killGroup(pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = h.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(DefaultKillGrace):
		killGroup(pid, syscall.SIGKILL)
	}
}
