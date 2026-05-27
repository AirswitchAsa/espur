package textchunk

import (
	"strings"
	"testing"
)

func TestSplit_ShortBodyUnchanged(t *testing.T) {
	got := Split("hello", 50)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %#v", got)
	}
}

func TestSplit_EmptyBody(t *testing.T) {
	if got := Split("", 50); len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

func TestSplit_NoCapPassThrough(t *testing.T) {
	in := strings.Repeat("x", 10000)
	got := Split(in, 0)
	if len(got) != 1 || got[0] != in {
		t.Fatalf("maxLen<=0 should disable splitting")
	}
}

func TestSplit_LineBoundaryPreferred(t *testing.T) {
	body := "line-one is short\nline-two is also short\nline-three"
	got := Split(body, 25)
	if len(got) < 2 {
		t.Fatalf("expected multi-chunk, got %#v", got)
	}
	for _, ch := range got {
		if len(ch) > 25 {
			t.Fatalf("chunk over cap: %q (len=%d)", ch, len(ch))
		}
	}
	// No chunk should start with a stray newline (we should split BEFORE
	// the newline that joins lines, not after).
	for _, ch := range got {
		if strings.HasPrefix(ch, "\n") {
			t.Fatalf("chunk starts with newline: %q", ch)
		}
	}
}

func TestSplit_KeepsCodeFenceIntact(t *testing.T) {
	body := "before the fence\n```go\nlong line of code inside fence one\nanother long line of code inside fence two\nyet another inside fence three\n```\nafter"
	got := Split(body, 50)
	// At least one chunk should contain both the opening and closing
	// fence — i.e. the fence is not split across chunks.
	openClosePair := false
	for _, ch := range got {
		if strings.Contains(ch, "```go") && strings.Count(ch, "```") >= 2 {
			openClosePair = true
		}
	}
	if !openClosePair {
		t.Fatalf("fenced block was split across chunks: %#v", got)
	}
}

func TestSplit_HardSplitLongSingleLine(t *testing.T) {
	body := strings.Repeat("a ", 200) // ~400 chars in one line
	got := Split(body, 50)
	for _, ch := range got {
		if len(ch) > 50 {
			t.Fatalf("hard-split chunk exceeds cap: len=%d", len(ch))
		}
	}
	// Reassembly should preserve content (modulo whitespace tweaks at
	// chunk boundaries — we accept loss of a single inter-chunk space).
	joined := strings.Join(got, " ")
	if strings.Count(joined, "a") != strings.Count(body, "a") {
		t.Fatalf("hard-split lost characters; got %d a's, want %d",
			strings.Count(joined, "a"), strings.Count(body, "a"))
	}
}
