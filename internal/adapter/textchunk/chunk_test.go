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

func TestSplit_LargeFenceClosedCleanlyOnSplit(t *testing.T) {
	// A long code block that won't fit in one chunk under a typical
	// Discord-ish cap. The chunker must respect the cap AND close the
	// fence with "```" when splitting mid-fence, so downstream
	// renderers don't drag the code styling onto plain text.
	body := "```go\n" + strings.Repeat("filler line of code\n", 200) + "```\nafter the block"
	got := Split(body, 500)
	for i, ch := range got {
		if len(ch) > 500 {
			t.Fatalf("chunk[%d] over cap: len=%d", i, len(ch))
		}
	}
	// At least one of the early chunks should end with the close marker
	// so Discord stops rendering as code.
	if !strings.HasSuffix(strings.TrimSpace(got[0]), "```") {
		t.Fatalf("first chunk should close the fence; got tail=%q",
			tail(got[0], 12))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
