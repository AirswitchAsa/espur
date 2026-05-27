package wechat

import "testing"

func TestNormalizeBody_DMNoMention(t *testing.T) {
	body, mention := normalizeBody("hello there", "espur-bot")
	if mention {
		t.Fatal("no @ token should be no mention at this layer")
	}
	if body != "hello there" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_GroupAtBot(t *testing.T) {
	// WeChat group mentions render as "@<name> <text>" where
	// is a four-per-em space (the open-wechat docs note the trailer).
	in := "@espur-bot what's the weather?"
	body, mention := normalizeBody(in, "espur-bot")
	if !mention {
		t.Fatal("should detect @espur-bot mention")
	}
	if body != "what's the weather?" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_OtherMentionsStripped(t *testing.T) {
	in := "@alice @bob ping the bot please"
	body, mention := normalizeBody(in, "espur-bot")
	if mention {
		t.Fatal("no @espur-bot here")
	}
	if body != "ping the bot please" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_NoBotName(t *testing.T) {
	body, mention := normalizeBody("hi", "")
	if mention {
		t.Fatal("empty botName must not match anything")
	}
	if body != "hi" {
		t.Fatalf("body=%q", body)
	}
}
