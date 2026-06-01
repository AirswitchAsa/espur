package memory

import (
	"strings"
	"testing"
)

func TestExtractUserInstructions_FromSeed(t *testing.T) {
	// Empty section right after seeding.
	got := ExtractUserInstructions(agentsMD)
	if got != "" {
		t.Fatalf("fresh seed should have empty user section, got %q", got)
	}
}

func TestExtractUserInstructions_WithContent(t *testing.T) {
	full := "preamble\n" + UserInstructionsStart + "\n# rules\n- be terse\n" + UserInstructionsEnd + "\ntrailing\n"
	got := ExtractUserInstructions(full)
	want := "# rules\n- be terse"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExtractUserInstructions_NoMarkersReturnsEmpty(t *testing.T) {
	if got := ExtractUserInstructions("# legacy\n- fact\n"); got != "" {
		t.Fatalf("legacy file should return empty, got %q", got)
	}
}

func TestReplaceUserInstructions_PreservesSystemContent(t *testing.T) {
	in := "# How to use your memory\n\nsystem rules ...\n\n" +
		UserInstructionsStart + "\nold body\n" + UserInstructionsEnd + "\n"
	out := ReplaceUserInstructions(in, "new body")
	if !strings.HasPrefix(out, "# How to use your memory\n\nsystem rules ...\n\n") {
		t.Fatalf("system content lost:\n%s", out)
	}
	if ExtractUserInstructions(out) != "new body" {
		t.Fatalf("user section not updated: %q", ExtractUserInstructions(out))
	}
}

func TestReplaceUserInstructions_AppendsMarkersWhenAbsent(t *testing.T) {
	legacy := "# Long-term memory for this thread\n\n- old fact\n"
	out := ReplaceUserInstructions(legacy, "be terse")
	if !strings.HasPrefix(out, legacy) {
		t.Fatalf("legacy prefix lost:\n%s", out)
	}
	if !strings.Contains(out, UserInstructionsStart) || !strings.Contains(out, UserInstructionsEnd) {
		t.Fatalf("markers not appended:\n%s", out)
	}
	if ExtractUserInstructions(out) != "be terse" {
		t.Fatalf("user section wrong: %q", ExtractUserInstructions(out))
	}
}

func TestReplaceUserInstructions_EmptyBodyKeepsMarkers(t *testing.T) {
	in := "preamble\n" + UserInstructionsStart + "\nold\n" + UserInstructionsEnd + "\n"
	out := ReplaceUserInstructions(in, "")
	if !strings.Contains(out, UserInstructionsStart) || !strings.Contains(out, UserInstructionsEnd) {
		t.Fatalf("markers lost on empty save")
	}
	if ExtractUserInstructions(out) != "" {
		t.Fatalf("expected empty user section, got %q", ExtractUserInstructions(out))
	}
}
