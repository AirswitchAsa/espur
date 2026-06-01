package memory

import "strings"

// ExtractUserInstructions returns the content between the user-instructions
// delimiters in a seeded AGENTS.md. If the markers are absent (e.g. legacy
// pre-delimiter AGENTS.md from an old thread), it returns "" so the UI
// renders an empty editor — the operator hasn't written anything yet by
// definition. Leading/trailing newlines inside the block are trimmed.
func ExtractUserInstructions(agents string) string {
	i := strings.Index(agents, UserInstructionsStart)
	if i < 0 {
		return ""
	}
	i += len(UserInstructionsStart)
	j := strings.Index(agents[i:], UserInstructionsEnd)
	if j < 0 {
		return ""
	}
	return strings.Trim(agents[i:i+j], "\n")
}

// ReplaceUserInstructions returns AGENTS.md with the user-instructions block
// replaced by `body`. If the existing AGENTS.md has no markers (legacy seed
// from before the delimiter convention), the markers are appended at the end
// with the new body inside — the operator's edit is what brings an old
// thread up to the new layout, on demand. The system content above the
// markers is never modified.
func ReplaceUserInstructions(agents, body string) string {
	body = strings.Trim(body, "\n")
	block := UserInstructionsStart + "\n" + body + "\n" + UserInstructionsEnd

	i := strings.Index(agents, UserInstructionsStart)
	if i < 0 {
		// Legacy file: append a delimited block at the end.
		tail := agents
		if !strings.HasSuffix(tail, "\n") {
			tail += "\n"
		}
		return tail + "\n" + block + "\n"
	}
	j := strings.Index(agents[i:], UserInstructionsEnd)
	if j < 0 {
		// Start marker without an end — corrupt; replace from start onward.
		return agents[:i] + block + "\n"
	}
	end := i + j + len(UserInstructionsEnd)
	return agents[:i] + block + agents[end:]
}
