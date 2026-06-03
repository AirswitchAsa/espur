package transcript

import (
	"os"
	"path/filepath"
	"strings"
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

// TestTail_OversizedLineNotFatal guards the robustness fix: a single huge or
// corrupt line must not poison the whole tail read (which would crash every
// future turn on the thread). The oversized line is skipped; valid records
// before and after it still come back.
func TestTail_OversizedLineNotFatal(t *testing.T) {
	s := NewStore(t.TempDir())
	const platform, tid = "discord", "big"
	if err := s.Append(platform, tid, Record{Kind: KindUser, Author: Author{Label: "a"}, Body: "before"}); err != nil {
		t.Fatal(err)
	}
	// A ~8 MiB line — larger than the old 4 MiB scanner cap that made the read fatal.
	huge := "{\"kind\":\"user\",\"body\":\"" + strings.Repeat("x", 8*1024*1024) + "\"}\n"
	path := filepath.Join(s.ThreadDir(platform, tid), "transcript.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(huge); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := s.Append(platform, tid, Record{Kind: KindUser, Author: Author{Label: "a"}, Body: "after"}); err != nil {
		t.Fatal(err)
	}

	users, err := s.TailUserMessages(platform, tid, 10)
	if err != nil {
		t.Fatalf("tail must not error on an oversized line: %v", err)
	}
	// The huge line is valid JSON, so it may or may not parse depending on memory,
	// but the read must not fail and must include the small records around it.
	var bodies []string
	for _, u := range users {
		if u.Body == "before" || u.Body == "after" {
			bodies = append(bodies, u.Body)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("expected both small records to survive, got %v", bodies)
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
