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
	got := Assemble(tail, Trigger{AuthorLabel: "alice", Body: "the current incoming message"})

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
	got := Assemble(nil, Trigger{AuthorLabel: `alice "quote"`, Body: "hi"})
	if strings.Contains(got, `"quote"`) {
		t.Fatalf("attribute not escaped: %s", got)
	}
}
