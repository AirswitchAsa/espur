# Behavior: MemorySeed

## Condition

A trigger arrives for a `thread_id` that does not yet have a working directory under `data/threads/<platform>/<encoded_thread_id>/`. Espur must materialize the working directory and seed it before the first invocation for that thread.

This behavior fires exactly once per thread, on the first trigger. On every subsequent trigger for the same thread, the working directory already exists and this behavior is a no-op (modulo the idempotent re-seed below).

## Description

**Working-directory creation**

- Path: `data/threads/<platform>/<encoded_thread_id>/`. The platform is its own path segment; the thread id is URL-safe (raw, unpadded) base64 with a length cap (no `/`, no `..`). The encoding is stable and reversible.
- Directory is created with the running process's default permissions (`0o755`); nothing inside is world-writable.

**Seed file: `AGENTS.md`**

A single file `AGENTS.md` is written into the new working directory. It is the only file Espur seeds — the memory index and detail files are created by the agent itself, on demand, the first time it records something. `AGENTS.md` has two parts:

1. **System memory instructions** (top of file). Compiled-in template text that tells the agent how to use the directory as durable, per-thread memory. The wording may evolve; the rules it expresses must not. Those rules are:

   - **Persistent, per-thread.** The working directory survives across invocations and process restarts. It is durable memory for *this one thread* (channel / group / DM); it is **not** shared with other threads.
   - **Index + detail-files pattern.** `memory_index.md` is the **index** — one line per entry in the form `- [short title](<slug>.md) — one-sentence gloss`. Long-form detail goes in a sibling `<slug>.md` file, one file per fact. The index links to it; detail is never pasted inline into the index.
   - **Slugs are kebab-case**, short, and stable. If a slug must change, rename the file and update the index entry to match.
   - **Read detail on demand.** Open `<slug>.md` with the read tool when the detail is actually needed; keep the index small to keep context cheap.
   - **Update, don't append blindly.** If a fact changes, edit the entry or detail file. If it is no longer true, delete both the entry and the file. Stale memory is worse than no memory.
   - **What belongs:** preferences, recurring projects, names of people / repos / services that come up often, decisions the user wants remembered, file paths the user keeps pointing at.
   - **What does not belong:** minute-by-minute conversation (Espur shows the recent transcript tail on every turn — see [[context-assembly]]); and secrets, credentials, API keys, or tokens, even if the user pastes them — acknowledge and forget, never write them to any file.

2. **Operator custom-instructions block** (bottom of file). A section delimited by stable HTML-comment markers:

   ```
   <!-- espur:user-instructions:start -->
   <!-- espur:user-instructions:end -->
   ```

   It is seeded empty. The operator can fill it from the admin UI (persona, tone, do/don't rules for this thread — see [[webui]]); the agent is told to treat its contents as authoritative guidance. Espur rewrites **only** the bytes between these two markers; the system instructions above are never modified by the UI.

The seed text is compiled in. The system-instructions wording is not end-user configurable; the operator block is the configuration surface.

**Ownership boundaries**

- **Agent-owned:** `memory_index.md`, every `<slug>.md` detail file, and any scratch files. Espur does not write or edit these (except the destructive `wipe-memory` UI action, which deletes them — see [[webui]]).
- **Shared:** `AGENTS.md`. The agent may read it freely; the system portion is fixed at seed time and the operator portion is rewritten only by the admin UI between the markers.

**Runtime parsing**

- At **trigger time** Espur never parses `AGENTS.md`, `memory_index.md`, or any `<slug>.md` to enforce memory structure. The discipline lives entirely in the seed prompt. Espur does inline the raw bytes of `AGENTS.md` into the assembled prompt (see [[context-assembly]]), but it does not interpret them.
- The **admin UI** does parse `AGENTS.md` — but only to extract / replace the operator custom-instructions block between the markers (see [[webui]]). It never rewrites the system instructions or the agent-owned memory files (other than a full `wipe-memory` delete).

**Idempotency**

- If the directory exists but `AGENTS.md` is missing (bug or manual ops), Espur re-seeds `AGENTS.md` and only `AGENTS.md`. It does not touch any existing `memory_index.md`, `<slug>.md`, or scratch file.
- If `AGENTS.md` exists, Espur leaves it alone, even if its contents diverge from the current template — the thread keeps whatever it has (including a now-empty or operator-edited block).

## Outcome

After this behavior runs, the thread's working directory exists at the expected path and contains an `AGENTS.md` carrying the memory instructions plus an (initially empty) operator custom-instructions block. The memory index (`memory_index.md`) and detail files do not exist yet — the agent creates them the first time it records something.

Subsequent invocations for this thread use this directory as the session `directory` (see [[opencode-invoke]]) and have full filesystem-tool access scoped to it.

## Notes

- The rule list above is the spec. To change a rule (e.g., allow multi-line index entries), update this spec first, then the compiled-in template (`internal/memory/seed.go`).
- Thread working directories are never auto-deleted by the runtime. The admin UI exposes per-thread `wipe-memory` (delete the bot's memory files, keep `AGENTS.md` + transcript) and a full thread `delete` (remove the whole workdir); see [[webui]].
- Legacy threads from before this layout may contain `fact_<slug>.md`-named files or use `AGENTS.md` itself as the index. Espur does not migrate them; the UI's memory-file listing treats every `*.md` except `AGENTS.md` as a bot-owned memory file, so old and new layouts both render.
- The seed never mentions specific vendor names or Espur internals — the agent should be agnostic to which vendor it is running under.
