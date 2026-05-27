package transcript

import (
	"testing"
)

func TestAppendAndTail(t *testing.T) {
	s := NewStore(t.TempDir())
	const platform = "discord"
	const tid = "channel-123"

	mustAppend := func(r Record) {
		t.Helper()
		if err := s.Append(platform, tid, r); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(Record{Kind: KindUser, Author: Author{ID: "alice", Label: "alice"}, Body: "hello", Meta: Meta{Mention: false}})
	mustAppend(Record{Kind: KindBot, Author: Author{ID: "bot", Label: "espur"}, Body: "ignored in tail"})
	mustAppend(Record{Kind: KindUser, Author: Author{ID: "alice", Label: "alice"}, Body: "@espur do thing", Meta: Meta{Mention: true}})
	mustAppend(Record{Kind: KindSystem, Body: "annotation"})

	users, err := s.TailUserMessages(platform, tid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 user records, got %d", len(users))
	}
	if users[0].Body != "hello" || users[1].Body != "@espur do thing" {
		t.Fatalf("order wrong: %+v", users)
	}

	all, err := s.TailAll(platform, tid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 records, got %d", len(all))
	}
}

func TestEncodeThreadID(t *testing.T) {
	a := EncodeThreadID("discord:channel-123")
	b := EncodeThreadID("discord:channel-123")
	if a != b {
		t.Fatalf("encoding not stable")
	}
	if a == "" {
		t.Fatal("empty encoding")
	}
}
