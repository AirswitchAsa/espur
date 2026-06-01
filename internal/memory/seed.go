// Package memory materializes the per-thread working directory and seeds
// AGENTS.md per docs/specs/memory-seed.dog.md.
package memory

import (
	"os"
	"path/filepath"
)

// UserInstructionsStart / End delimit the operator-editable section at the
// bottom of every seeded AGENTS.md. Espur's admin UI rewrites the bytes
// between these markers when the operator saves custom instructions; the
// rest of the file (the system seed above) is hidden from the UI and stays
// fixed. The bot reads the whole file naturally — no special parsing needed
// on its side.
const (
	UserInstructionsStart = "<!-- espur:user-instructions:start -->"
	UserInstructionsEnd   = "<!-- espur:user-instructions:end -->"
)

// agentsMD is the seed file written into a new thread's working directory.
// Layout: system instructions (memory rules) on top, then an empty
// operator-editable section delimited by [[UserInstructionsStart, End]].
const agentsMD = `# How to use your memory

This thread has a working directory you can write to. It survives across
invocations and process restarts. Use it as your durable memory for this one
conversation thread; it is not shared with other threads.

## Files

- ` + "`memory_index.md`" + ` — the index. One line per entry:
  ` + "`- [short title](<slug>.md) — one-sentence gloss`" + `.
- ` + "`<slug>.md`" + ` — one file per long-form fact, sitting next to the index.

## Hygiene rules

1. **Index, not notebook.** ` + "`memory_index.md`" + ` is an index — short lines
   pointing at slug files. Detail lives in the slug file, not the index.

2. **Slugs are kebab-case**, short, stable. If a slug must change, rename the
   file and update the index entry to match.

3. **Read detail on demand.** Open ` + "`<slug>.md`" + ` with your read tool when
   you need the detail. Don't paste detail-file content inline into the index.

4. **Update, don't append blindly.** If a fact changes, edit the entry or the
   detail file. If it's no longer true, delete both. Stale memory is worse
   than no memory.

5. **What belongs here:** preferences, recurring projects, names of people /
   repos / services that come up often, decisions the user wants you to
   remember, file paths the user keeps pointing at.

6. **What does not belong here:** minute-by-minute conversation (Espur shows
   you the recent transcript on every turn); secrets, credentials, API keys,
   or tokens — even if the user pastes them. Acknowledge and forget them.

## Custom instructions from the operator

Anything between the two markers below is operator-authored guidance for this
specific thread (persona, tone, do/don't rules). Treat it as authoritative.
It may be empty.

` + UserInstructionsStart + `
` + UserInstructionsEnd + `
`

// EnsureWorkDir creates the working directory and seeds AGENTS.md if missing.
// Idempotent — see docs/specs/memory-seed.dog.md "Idempotency".
func EnsureWorkDir(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	agents := filepath.Join(workDir, "AGENTS.md")
	if _, err := os.Stat(agents); err == nil {
		return nil // exists — leave alone, opencode owns it
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(agents, []byte(agentsMD), 0o644)
}
