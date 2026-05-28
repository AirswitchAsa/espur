# Behavior: Transcript

## Condition

A message exists that must be persisted to the per-thread record — either an inbound user message accepted by [[trigger]] (mentioning the bot or not) or an outbound bot reply successfully posted by [[reply]]. The thread's working directory exists (created lazily by [[memory-seed]] on the first trigger).

## Description

**Storage**

- One append-only JSONL file per thread at `data/threads/<platform>/<encoded_id>/transcript.jsonl`.
- `<platform>` is the platform identifier (`discord`, `wechat`, ...).
- `<encoded_id>` is a URL-safe base64 of the platform-native thread id, truncated to 64 chars with a sha256-hex suffix appended if truncation occurred (so collisions are improbable and the encoding stays reversible enough for diagnostics).
- UTF-8 text, one JSON object per line, no trailing comma, no surrounding array.
- Espur never truncates, rotates, or compacts the file. Operators manage retention via the host filesystem; the web UI exposes per-thread working-dir size to help spot growth.

**Serialization**

- All writes for a given `thread_id` go through a single writer that shares the per-thread queue lock (see [[trigger]]). Inbound and outbound writes for the same thread are therefore strictly serialized; no separate concurrency control is needed.
- A write is one `fmt.Fprintln`-style append of a single JSON line plus a newline. Partial writes are possible on crash but ordinary operation produces whole lines only.

**Record schema**

Every line is a JSON object with the fields below. `meta` is always present (possibly empty); all `meta` sub-fields are optional and conditional on `kind`.

```
{
  "ts":                  string,   RFC3339 UTC, when the record was committed
  "kind":                string,   one of "user" | "bot" | "system"
  "platform_message_id": string,   adapter-native id, prefixed by platform; "" allowed for kind=system
  "author":              { "id": string, "label": string },
  "body":                string,   already-normalized text (mentions stripped from inbound; full reply text for outbound)
  "meta":                object    kind-conditional, see below
}
```

`meta` fields by kind:

- **kind = user**
  - `mention` (bool) — whether this message addressed the bot (explicit @-mention, or implicit DM).
  - `coalesced_into` (string, optional) — set on user messages that were superseded by burst coalescing; value is the `platform_message_id` of the winning message that the bot ultimately replied to.
- **kind = bot**
  - `reply_outcome` (string) — one of `success`, `timeout`, `drained`, `crash`.
  - `request_id` (string, optional) — present on `crash` outcomes (and useful on `drained` for log correlation); 8-char Crockford base32, matches the value in the user-visible reply.
- **kind = system**
  - `note` (string) — short tag identifying the system event (e.g. `timeout-aborted-previous-turn`). Used to give future invocations a stable signal that the prior turn didn't end normally.

**Write rules**

- `kind = user` — appended by [[trigger]] on accept (after dedup), **regardless of whether the message mentions the bot**. Non-mention messages carry `meta.mention = false` and are still recorded, so the channel context reflects what was actually said. Espur thereby sees and stores every message in any channel or group it sits in; this is a deliberate consequence and is called out in the deploy documentation.
- **Coalesced-away** user messages are recorded normally with `meta.coalesced_into` set to the winning message id. Transcript order preserves the message sequence as the user typed it; only the bot's reply (one record) attaches to the winning message.
- `kind = bot` — appended once per logical reply, on successful first-chunk post by the adapter. `body` is the full reply text even when the IM platform required the adapter to split it into multiple chunks; chunking is a pure adapter render concern and is not visible in the transcript.
- `kind = system` — appended sparingly. The current defined uses are: after a [[reply]] timeout/crash where the bot reply itself already carries the outcome (so a system line is *not* needed); and an explicit `previous-turn-aborted` line when no bot reply was posted at all (e.g. adapter-side post failure after exhausting its small retry window).
- A failed inbound write (disk full, fs error) is logged and the trigger fails closed: no opencode invocation, no reply. The IM platform will retry the inbound webhook (or the user will resend); the deferred dedup record means we won't double-process once the disk situation recovers.

**Read rules**

- [[context-assembly]] reads the last N records and uses **only `kind = user` records** to populate the thread-context block. Bot replies are not echoed back into the model — opencode sees the user's side of the conversation plus the current request, and reasons fresh each turn.
- `kind = system` records are also not surfaced to opencode by default; they exist for operator visibility and possible future use.
- The web UI thread peek (see [[webui]]) decodes and renders all kinds, with visual differentiation by `kind`.
- Malformed lines (e.g. a torn write from a crash) are skipped by the reader with a single log warning per file per process lifetime. The reader does not attempt to repair.

## Outcome

Every accepted user message and every successfully posted bot reply produces exactly one line appended to the thread's `transcript.jsonl`, in arrival/post order, with a fully-populated record. Coalesced-away messages are preserved with a back-pointer to their winner. System records are appended only for explicit annotations the operator needs to see.

[[context-assembly]] can rely on the file to provide the last N user-side messages, in order, by reading the tail and filtering to `kind = user`.

## Notes

- Schema versioning: there is no `schema_version` field in v0.1. If the schema changes, future Espur reads tolerate older records (additive fields only) and emit a deprecation log when missing fields are encountered. A breaking change would require an explicit migration, out of scope for now.
- The choice to exclude bot replies from the model's context is deliberate. It keeps each turn's input compact, avoids the model echo-loop of restating its own prior phrasings, and lets opencode treat each turn as a fresh take on the user's evolving request. If conversational coherence suffers in practice, this is the first knob to revisit.
- Author labels are captured at write time (snapshot of the display name as the adapter knew it). Espur does not back-fill renames; if a user changes their display name, old records keep the old label. This matches how IM transcripts already render historically.
- Decided: the transcript does not persist across a `data/` wipe — `data/` is the durable state, and wiping it is a full reset by definition.
- Decided: `kind = bot` records carry `meta.vendor_id` (which vendor produced the reply) alongside the reply outcome and request id.
