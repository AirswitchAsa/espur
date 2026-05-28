# Behavior: MemorySeed

## Condition

A trigger arrives for a `thread_id` that does not yet have a working directory under `data/threads/<thread_id>/`. Espur must materialize the working directory and seed it before the first `opencode run` for that thread.

This behavior fires exactly once per thread, on the first trigger. On every subsequent trigger for the same thread, the working directory already exists and this behavior is a no-op.

## Description

**Working-directory creation**

- Path: `data/threads/<thread_id>/`.
- `thread_id` is the platform-stable identifier from the [[trigger]] normalization; it may need lightweight filesystem-safe encoding (no `/`, no `..`, length-bounded). The encoding is stable and reversible.
- Directory is created with the running process's default permissions; nothing inside is world-writable.

**Seed file: `AGENTS.md`**

A single file `AGENTS.md` is written inside the new working directory with content that tells opencode (and any future agent reading this directory) how to use it as long-term memory. The seed text expresses the following hygiene rules as plain prose / bullets — wording may evolve, the rules must not:

1. **Persistent across conversations.** This file is your durable memory for this thread. It outlives any single invocation. Read it before answering when prior context might matter; update it when you learn something worth remembering across future messages.

2. **Index + detail-files pattern.** `AGENTS.md` is an **index**, not a notebook. Each entry is **one line** in the form:

   ```
   - [short title](fact_<slug>.md) — one-sentence gloss
   ```

   Detail goes into a separate file `fact_<slug>.md` in the same directory, written by you when (and only when) a fact is worth more than the one-line gloss. The index links to it.

3. **Slugs are kebab-case**, short, and stable. Prefer renaming the gloss over renaming the slug; if a slug must change, update the index entry to point at the new file and delete the old one.

4. **Read detail on demand.** Use your `read` tool to open `fact_<slug>.md` when you actually need the detail. Do **not** expand the index by pasting detail-file contents inline. Keeping the index small keeps your context window cheap.

5. **Update, don't append blindly.** If a fact changes, edit the relevant entry or detail file. If a fact is no longer true, delete the entry and its detail file. Stale memory is worse than no memory.

6. **Scope.** This memory is specific to this single thread (channel / group / DM). It is not shared across threads. Do not assume another thread knows what this one knows.

7. **What belongs here.** Preferences, recurring projects, names of people/repos/services that come up often, decisions the user wants you to remember, file paths the user keeps pointing at. Not: minute-by-minute conversation, which lives in the transcript Espur shows you on every turn.

8. **What does not belong here.** Secrets, credentials, API keys, tokens — even if the user pastes them. Acknowledge and then forget. Do not write them to `AGENTS.md` or any `fact_*.md`.

9. **You own this directory.** Espur will not edit `AGENTS.md` or any `fact_*.md` after seeding. If you want a scratch file for your own thinking, feel free; just don't let scratch clutter accumulate forever.

The seed text is delivered verbatim from a compiled-in template; it is not user-configurable in v0.1.

**No runtime parsing**

- Espur never reads or parses `AGENTS.md` or any `fact_*.md` to enforce structure at trigger time. The discipline lives entirely in the seed prompt.
- The web UI may render `AGENTS.md` as a peek (see [[webui]]), but that is read-only display, not enforcement.
- If the index format drifts in practice, structural enforcement may be added later — out of scope for this spec.

**Idempotency**

- If, due to a bug or manual ops, the directory exists but `AGENTS.md` is missing, Espur re-seeds `AGENTS.md` and only `AGENTS.md`. It does not touch any existing `fact_*.md` or scratch files.
- If `AGENTS.md` exists, Espur leaves it alone, even if its contents diverge from the seed template. opencode owns the file from that point on.

## Outcome

After this behavior runs, the thread's working directory exists at the expected path and contains an `AGENTS.md` whose content instructs the agent to maintain a one-line-per-entry memory index with detail in sibling `fact_<slug>.md` files, scoped to this thread, with the hygiene rules above.

Subsequent invocations of `opencode run` for this thread will use this directory as `cwd` and have access to the seed file and any files opencode has since written.

## Notes

- TODO(decision): exact final wording of the seed prompt is implementation work, but the **rule list above is the spec**. If we want to change a rule (e.g., allow multi-line index entries), update this spec first.
- Decided: no separate `README.md` in the working dir — `AGENTS.md` is already human-readable, so one file is simpler.
- Decided: thread working directories are never auto-deleted. The web UI thread list exposes each dir's size; the operator cleans up manually.
- Decided: the path is `<data>/threads/<platform>/<encoded_thread_id>`, where the platform is its own path segment and the thread id is URL-safe (raw, unpadded) base64 with a length cap.
- The seed never mentions specific vendor names or Espur internals — opencode should be agnostic to which vendor it is running under.
