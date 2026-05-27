package textchunk

import (
	"strings"
	"testing"
)

// FuzzSplit_InvariantNoChunkOverCap is the meta-property the chunker must
// preserve regardless of input: no produced chunk exceeds maxLen unless
// the input itself contains a single un-splittable line (which the
// hardSplit path will still cut). When maxLen <= 0 splitting is disabled,
// so we skip that case.
//
// Run with: go test ./internal/adapter/textchunk -fuzz=FuzzSplit -fuzztime=30s
func FuzzSplit_InvariantNoChunkOverCap(f *testing.F) {
	f.Add("hello world", 80)
	f.Add("```\ncode\n```\nafter", 8)
	f.Add(strings.Repeat("x", 5000), 100)
	f.Add("a\nb\nc", 1)
	f.Add("", 50)
	f.Add("```go\n"+strings.Repeat("long\n", 50)+"```", 20)

	f.Fuzz(func(t *testing.T, body string, maxLen int) {
		if maxLen <= 0 {
			return // disabled-split mode, not under test here
		}
		// Bound the search: the chunker uses byte-length, so any sane
		// maxLen makes sense, but we cap maxLen at the input length+1 so
		// pathological huge maxLens don't waste fuzz time.
		if maxLen > len(body)+1 && maxLen > 16 {
			maxLen = 16
		}

		out := Split(body, maxLen)

		// Invariant 1: nothing in `out` exceeds maxLen by more than the
		// hardSplit boundary tolerance. The current implementation guarantees
		// strict <=maxLen for hard-split fragments and <=maxLen for joined
		// line-batches. So all chunks must be <=maxLen.
		for i, ch := range out {
			if len(ch) > maxLen {
				t.Fatalf("chunk[%d] over cap: len=%d > %d, body=%q",
					i, len(ch), maxLen, body)
			}
		}

		// Invariant 2: empty input yields no chunks.
		if body == "" && len(out) != 0 {
			t.Fatalf("empty body produced chunks: %#v", out)
		}

		// Invariant 3: non-empty input always yields at least one chunk.
		if body != "" && len(out) == 0 {
			t.Fatalf("non-empty body produced no chunks; body=%q", body)
		}
	})
}
