package bot

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/opencode"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

// fakeAdapter captures Posts and pushes back to a Done channel after each.
type fakeAdapter struct {
	mu    sync.Mutex
	posts []post
	idSeq int
	done  chan string // signal after each Post: the posted body
	errOn string      // if body == errOn, return ErrThreadGone
}

type post struct{ thread, body string }

func (f *fakeAdapter) Platform() string { return "discord" }
func (f *fakeAdapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	return nil, errors.New("not used in tests")
}
func (f *fakeAdapter) Healthy() bool { return true }
func (f *fakeAdapter) Post(ctx context.Context, threadID, body string) (string, error) {
	f.mu.Lock()
	if body == f.errOn {
		f.mu.Unlock()
		return "", adapter.ErrThreadGone
	}
	f.idSeq++
	id := "pmid-" + itoa(f.idSeq)
	f.posts = append(f.posts, post{threadID, body})
	ch := f.done
	f.mu.Unlock()
	if ch != nil {
		ch <- body
	}
	return id, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [10]byte
	n := 0
	for i > 0 {
		b[n] = byte('0' + i%10)
		n++
		i /= 10
	}
	for x, y := 0, n-1; x < y; x, y = x+1, y-1 {
		b[x], b[y] = b[y], b[x]
	}
	return string(b[:n])
}

func newCore(t *testing.T, inv vendor.Invoker) (*Core, *fakeAdapter, *store.DB) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(filepath.Join(dataDir, "espur.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secrets.GenerateIdentity()
	vault, _ := secrets.New(key)
	pool := vendor.New(db, vault).WithInvoker(inv)
	ts := transcript.NewStore(dataDir)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "v1", Model: "m1", Enabled: true, Position: 0, CredKind: "byo_key",
	})
	core := New(Config{
		DB: db, Pool: pool, Transcript: ts,
		DashboardURL: "http://dashboard.local", InvokeTimeout: 5 * time.Second,
	})
	fa := &fakeAdapter{done: make(chan string, 4)}
	core.RegisterAdapter(fa)
	return core, fa, db
}

func TestBot_HappyPath(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{{Outcome: opencode.OutcomeSuccess, AssistantText: "pong"}}}
	core, fa, _ := newCore(t, inv)

	ctx := context.Background()
	core.Dispatch(ctx, adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-1",
		Author: adapter.Author{ID: "u1", Label: "alice"},
		Body:   "ping", Mention: true, ReceivedAt: time.Now(),
	}})

	select {
	case body := <-fa.done:
		if body != "pong" {
			t.Fatalf("unexpected body: %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never got a Post")
	}
}

// TestBot_QueueRetiredWhenIdle verifies the per-thread queue (and its worker
// goroutine) is removed from the map once the thread goes idle, so a long-lived
// deployment does not leak a goroutine per thread ever seen. Regression for #3.
func TestBot_QueueRetiredWhenIdle(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{{Outcome: opencode.OutcomeSuccess, AssistantText: "pong"}}}
	core, fa, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-1",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})
	<-fa.done // reply posted; worker is about to retire the queue

	deadline := time.After(time.Second)
	for {
		core.mu.Lock()
		n := len(core.queues)
		core.mu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("queue not retired; %d queue(s) remain", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBot_DropsDuplicate(t *testing.T) {
	inv := &fakeInvoker{results: []opencode.Result{{Outcome: opencode.OutcomeSuccess, AssistantText: "pong"}}}
	core, fa, _ := newCore(t, inv)

	ctx := context.Background()
	m := &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-1",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}
	core.Dispatch(ctx, adapter.Event{Message: m})
	core.Dispatch(ctx, adapter.Event{Message: m}) // duplicate

	<-fa.done
	// Wait briefly to ensure no second Post.
	select {
	case <-fa.done:
		t.Fatal("duplicate produced a second Post")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestBot_NonMention_StoresButDoesNotReply(t *testing.T) {
	inv := &fakeInvoker{} // no Invoke expected
	core, fa, db := newCore(t, inv)

	ctx := context.Background()
	core.Dispatch(ctx, adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-77",
		Author: adapter.Author{Label: "bob"}, Body: "just chatting", Mention: false,
	}})

	select {
	case <-fa.done:
		t.Fatal("non-mention should not produce a Post")
	case <-time.After(150 * time.Millisecond):
	}
	first, _ := db.SeenMessage(ctx, "discord", "m-77")
	if first {
		t.Fatal("dedup row should already exist")
	}
}

// fakeInvoker matches the shape used in vendor_test but re-declared here to
// keep the bot package test-isolated.
type fakeInvoker struct{ results []opencode.Result }

func (f *fakeInvoker) Invoke(ctx context.Context, req opencode.Request) (opencode.Result, error) {
	if len(f.results) == 0 {
		return opencode.Result{}, errors.New("no scripted result")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r, nil
}
