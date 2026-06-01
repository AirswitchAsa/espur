package contextasm

import (
	"strings"
	"testing"

	"github.com/punny/espur/internal/transcript"
)

func TestAssemble_HappyPath(t *testing.T) {
	tail := []transcript.Record{
		{Kind: transcript.KindUser, Author: transcript.Author{Label: "alice"}, Body: "previous message"},
		{Kind: transcript.KindUser, Author: transcript.Author{Label: "bob"}, Body: "a third party also chimed in"},
	}
	got := Assemble(Prefix{Platform: "discord", ThreadID: "t1"}, tail, Trigger{AuthorLabel: "alice", Body: "the current incoming message"})

	for _, want := range []string{
		`<thread-context note="recent user messages on this thread, oldest first">`,
		"alice: previous message",
		"bob: a third party also chimed in",
		"</thread-context>",
		`<request from="alice">`,
		"the current incoming message",
		"</request>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestAssemble_AttrEscape(t *testing.T) {
	got := Assemble(Prefix{}, nil, Trigger{AuthorLabel: `alice "quote"`, Body: "hi"})
	if strings.Contains(got, `"quote"`) {
		t.Fatalf("attribute not escaped: %s", got)
	}
}

// TestAssemble_ByteCapTrimsOldestLines exercises the 8KiB context cap. We
// stuff in many long lines, then verify the assembled output drops the
// OLDEST entries (head of the list) while keeping the newest ones, and
// the resulting thread-context block fits under the cap.
func TestAssemble_ByteCapTrimsOldestLines(t *testing.T) {
	var tail []transcript.Record
	// Each line ~200 bytes; 60 lines ≈ 12KiB → must drop ~oldest 20 lines.
	for i := 0; i < 60; i++ {
		tail = append(tail, transcript.Record{
			Kind:   transcript.KindUser,
			Author: transcript.Author{Label: "alice"},
			Body:   "line-" + repeat(byte('a'+i%26), 190) + "-end" + idxStr(i),
		})
	}
	got := Assemble(Prefix{Platform: "discord", ThreadID: "t1"}, tail, Trigger{AuthorLabel: "alice", Body: "now"})

	// Locate the thread-context block and assert size cap.
	start := strings.Index(got, `<thread-context`)
	end := strings.Index(got, `</thread-context>`)
	if start < 0 || end < 0 {
		t.Fatal("malformed output")
	}
	if size := end - start; size > MaxBytes+512 /* opening tag overhead */ {
		t.Fatalf("thread-context block too big: %d bytes (cap %d)", size, MaxBytes)
	}

	// The very last line must be present (newest preserved).
	if !strings.Contains(got, "-end59") {
		t.Fatal("newest line was dropped — cap trims from head not tail")
	}
	// The first line MUST be gone (oldest dropped).
	if strings.Contains(got, "-end0\n") {
		t.Fatal("oldest line should have been trimmed")
	}
}

func TestAssemble_StablePrefixOrdering(t *testing.T) {
	got := Assemble(
		Prefix{Platform: "discord", ThreadID: "abc", AgentsMD: "# memory\n- foo"},
		[]transcript.Record{{Kind: transcript.KindUser, Author: transcript.Author{Label: "alice"}, Body: "hi"}},
		Trigger{AuthorLabel: "alice", Body: "now"},
	)
	thread := strings.Index(got, `<thread platform=`)
	memory := strings.Index(got, `<memory`)
	ctx := strings.Index(got, `<thread-context`)
	req := strings.Index(got, `<request`)
	if !(thread == 0 && thread < memory && memory < ctx && ctx < req) {
		t.Fatalf("unexpected ordering thread=%d memory=%d ctx=%d req=%d\n%s", thread, memory, ctx, req, got)
	}
	if !strings.Contains(got, "# memory\n- foo") {
		t.Fatalf("AGENTS.md body not inlined: %s", got)
	}
}

func TestAssemble_NoMemoryBlockWhenEmpty(t *testing.T) {
	got := Assemble(Prefix{Platform: "discord", ThreadID: "abc"}, nil, Trigger{Body: "hi"})
	if strings.Contains(got, "<memory") {
		t.Fatalf("memory block emitted without AGENTS.md: %s", got)
	}
}

func TestTrimFromHead_AlignsToNextLine(t *testing.T) {
	in := "first line\nsecond line\nthird line\n"
	// Drop 3 bytes ("fir") — function should advance to next newline.
	got := trimFromHead(in, 3)
	if got != "second line\nthird line\n" {
		t.Fatalf("got %q", got)
	}
}

func TestTrimFromHead_DropAll(t *testing.T) {
	in := "abc"
	if got := trimFromHead(in, 100); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTrimFromHead_NoNewlineAfterCut(t *testing.T) {
	// If there's no newline after the cut, the rest is returned as-is.
	in := "abcdef"
	if got := trimFromHead(in, 2); got != "cdef" {
		t.Fatalf("got %q", got)
	}
}

// helpers
func repeat(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func idxStr(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
