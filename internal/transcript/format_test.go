package transcript

import (
	"strings"
	"testing"
)

func TestFormat_LabelPriority(t *testing.T) {
	cases := []struct {
		name string
		in   Record
		want string
	}{
		{"label-first", Record{Author: Author{Label: "alice", ID: "u1"}, Body: "hi"}, "alice: hi"},
		{"id-fallback", Record{Author: Author{ID: "u1"}, Body: "hi"}, "u1: hi"},
		{"user-fallback", Record{Body: "hi"}, "user: hi"},
		{"multiline-body", Record{Author: Author{Label: "alice"}, Body: "line1\nline2"}, "alice: line1\nline2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Format(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEncodeThreadID_UnderCapNoSuffix(t *testing.T) {
	id := EncodeThreadID("simple-channel")
	if strings.Contains(id, "-") && len(id) == 64 {
		// We expect base64-only (no `-` suffix).
		t.Fatalf("unexpected suffix: %q", id)
	}
	if len(id) > 64 {
		t.Fatalf("encoded id over cap: len=%d", len(id))
	}
}

func TestEncodeThreadID_OverCapGetsHashSuffix(t *testing.T) {
	// Construct a raw id whose base64 form is well over 64 chars.
	raw := strings.Repeat("abcdefghij", 20) // 200 chars → base64 ≈ 268 chars
	id := EncodeThreadID(raw)
	if len(id) != 64 {
		t.Fatalf("expected exactly 64 chars (cap), got %d", len(id))
	}
	// Must end with "-<8hex>".
	if id[55] != '-' {
		t.Fatalf("expected '-' at position 55, got %q in %q", id[55], id)
	}
	suffix := id[56:]
	if len(suffix) != 8 {
		t.Fatalf("suffix wrong length: %q", suffix)
	}
	// Distinct raws must yield distinct encodings even at the cap.
	other := EncodeThreadID(raw + "x")
	if id == other {
		t.Fatalf("collision: both %q produced same id", raw)
	}
}
