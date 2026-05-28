# Behavior: ContextAssembly

## Condition

A `Trigger` has reached the head of its thread queue and is about to be handed to opencode. The thread has a transcript log (possibly empty for a brand-new thread).

## Description

Espur builds the single user message that will be passed to `opencode run` as the request. The message has two labelled blocks, in this order:

1. **Thread context** — the last N **user messages** from the thread transcript (see [[transcript]]), verbatim, in chronological order. Bot replies are deliberately excluded — opencode sees only what the user(s) said, not its own prior outputs. Each line is formatted as `author label + message body`. The block is wrapped with clear markers so opencode can tell context from request, for example:

   ```
   <thread-context note="recent user messages on this thread, oldest first">
   alice: previous message
   alice: another message
   bob: a third party also chimed in
   </thread-context>
   ```

   Records with `kind` of `bot` or `system` are filtered out before formatting. Coalesced-away user messages (`meta.coalesced_into` set) **are** included — they were said.

2. **Request** — the current `Trigger.text`, highlighted as the message to act on now:

   ```
   <request from="alice">
   the current incoming message text
   </request>
   ```

The current trigger's message is **not** duplicated into the thread-context block — context is "prior" by definition.

opencode is stateless across invocations. Espur builds this composite user message fresh on every trigger. There is no persistent opencode session, no `--continue`, no thread-side chat history sent through any opencode-managed memory.

The working directory for the invocation already contains `AGENTS.md` and any prior `fact_<slug>.md` files for this thread; opencode is told via its system prompt / `AGENTS.md` how to consult them. Context assembly does not inline memory files into the user message.

## Outcome

A single string is produced and passed as the user message to `opencode run`. That string contains, in order:

- A thread-context block with the most recent user-message tail (up to N records of `kind = user`, fewer if the thread is short). Bot replies and system records are filtered out.
- A request block with the current incoming message text and author label.

No other content is in the user message. opencode receives nothing else from Espur on this axis.

## Notes

- Decided: N (transcript-tail length) defaults to 30, configurable per-deploy.
- Decided: the tail is line-count bounded with a hard 8 KiB byte cap as a guard; on overflow the oldest lines are dropped (the current request is always preserved verbatim, outside the cap).
- Decided: multi-line messages preserve their newlines and render as one labelled block per message (not a single `\n`-literal line, not repeated author labels per physical line).
- The transcript itself (storage, append, tail read) is described separately — context assembly is a pure read of the tail.
- Attachments, images, embeds: out of scope for the first cut. Adapters render them to a placeholder text token in the transcript.
- The exact wrapper tag names (`thread-context`, `request`) are an implementation choice but must be stable so opencode behavior is reproducible.
