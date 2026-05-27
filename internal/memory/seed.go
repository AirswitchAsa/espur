// Package memory materializes the per-thread working directory and seeds
// AGENTS.md per specs/memory-seed.dog.md.
package memory

import (
	"os"
	"path/filepath"
)

// agentsMD is the seed file written into a new thread's working directory.
// The wording realizes the rule list pinned in specs/memory-seed.dog.md.
const agentsMD = `# Long-term memory for this thread

This file is your durable memory for this conversation thread. It survives
across invocations and across process restarts. Espur will not edit it; you
own this directory.

## Hygiene rules

1. **Index, not notebook.** This file is an index. Each entry is one line:
   - ` + "`- [short title](fact_<slug>.md) — one-sentence gloss`" + `
   Detail goes in a separate ` + "`fact_<slug>.md`" + ` next to this file.

2. **Slugs are kebab-case**, short, stable. If a slug must change, update the
   index entry to point at the new file and delete the old one.

3. **Read detail on demand.** Open ` + "`fact_<slug>.md`" + ` with your read tool when
   you need the detail. Do not paste detail-file content inline into the index.

4. **Update, don't append blindly.** If a fact changes, edit the entry or the
   detail file. If it's no longer true, delete both. Stale memory is worse
   than no memory.

5. **Scope.** This memory is specific to this one thread (channel / group /
   DM). It is not shared with other threads.

6. **What belongs here:** preferences, recurring projects, names of people /
   repos / services that come up often, decisions the user wants you to
   remember, file paths the user keeps pointing at.

7. **What does not belong here:** minute-by-minute conversation (Espur shows
   you the recent transcript on every turn); secrets, credentials, API keys,
   or tokens — even if the user pastes them. Acknowledge and forget them.

## Index

<!-- one line per entry, format: - [title](fact_<slug>.md) — gloss -->
`

// EnsureWorkDir creates the working directory and seeds AGENTS.md if missing.
// Idempotent — see specs/memory-seed.dog.md "Idempotency".
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
