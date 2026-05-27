package discord

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

const botID = "987654321"

func TestNormalizeBody_MentionStripped(t *testing.T) {
	m := &discordgo.Message{
		Content: "<@" + botID + "> ping me",
		Mentions: []*discordgo.User{
			{ID: botID, Username: "espur"},
		},
	}
	body, mention := normalizeBody(m, botID)
	if !mention {
		t.Fatal("expected mention true")
	}
	if body != "ping me" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_NickMentionVariant(t *testing.T) {
	// Discord renders nickname mentions as "<@!ID>".
	m := &discordgo.Message{
		Content:  "<@!" + botID + "> hi there",
		Mentions: []*discordgo.User{{ID: botID}},
	}
	body, mention := normalizeBody(m, botID)
	if !mention || body != "hi there" {
		t.Fatalf("mention=%v body=%q", mention, body)
	}
}

func TestNormalizeBody_OtherUserMentionUntouched(t *testing.T) {
	m := &discordgo.Message{
		Content:  "<@123> hey",
		Mentions: []*discordgo.User{{ID: "123"}},
	}
	body, mention := normalizeBody(m, botID)
	if mention {
		t.Fatal("non-bot mention must not flag")
	}
	// The non-bot mention token is preserved as Discord-render-able text.
	if !strings.Contains(body, "<@123>") {
		t.Fatalf("expected mention token preserved, got %q", body)
	}
}

func TestNormalizeBody_AttachmentsRenderToTokens(t *testing.T) {
	m := &discordgo.Message{
		Content: "look at this",
		Attachments: []*discordgo.MessageAttachment{
			{ContentType: "image/png", Filename: "a.png"},
			{ContentType: "application/pdf", Filename: "spec.pdf"},
		},
	}
	body, _ := normalizeBody(m, botID)
	if !strings.Contains(body, "[image]") {
		t.Fatalf("expected [image] token, got %q", body)
	}
	if !strings.Contains(body, "[attachment]") {
		t.Fatalf("expected [attachment] token, got %q", body)
	}
}

func TestNormalizeBody_EmptyMentionStays(t *testing.T) {
	m := &discordgo.Message{
		Content:  "",
		Mentions: []*discordgo.User{{ID: botID}},
	}
	body, mention := normalizeBody(m, botID)
	if !mention || body != "" {
		t.Fatalf("got mention=%v body=%q", mention, body)
	}
}

func TestIsThreadGone_403And404(t *testing.T) {
	for _, code := range []int{403, 404} {
		rerr := &discordgo.RESTError{
			Response: &http.Response{StatusCode: code},
		}
		if !isThreadGone(rerr) {
			t.Fatalf("status %d should be ErrThreadGone", code)
		}
	}
}

func TestIsThreadGone_OtherCodesNoMatch(t *testing.T) {
	for _, code := range []int{200, 400, 401, 429, 500} {
		rerr := &discordgo.RESTError{
			Response: &http.Response{StatusCode: code},
		}
		if isThreadGone(rerr) {
			t.Fatalf("status %d must not be ErrThreadGone", code)
		}
	}
}

func TestIsThreadGone_NonRESTErrorPassthrough(t *testing.T) {
	if isThreadGone(nil) {
		t.Fatal("nil err must not be ErrThreadGone")
	}
}
