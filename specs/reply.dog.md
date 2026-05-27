# Behavior: Reply

## Condition

A trigger has reached a terminal outcome from the invocation phase: success, timeout, all-drained, or crash. Espur is now responsible for posting exactly one user-visible reply on the originating thread (plus, in the coalesce case, at most one earlier "still thinking" ack already posted by [[trigger]]).

## Description

**Batch only, never streaming.** Espur posts the full reply text in a single platform message once the invocation phase ends. There is no token streaming, no progressive edits to a placeholder message, no partial flushes. This keeps adapters cross-platform-identical.

If the platform has a per-message length limit (Discord 2000 chars, etc.), the adapter is responsible for splitting the single logical reply into the minimum number of chunks needed, posted sequentially in order. From Espur's core's perspective, this is still "one reply" per trigger.

**Success reply**

- Body: the assistant text returned by opencode, verbatim, with no Espur-added prefix, suffix, or signature.
- Posted to the thread the trigger came from, attributed to the bot user.
- Transcript: the reply text is appended to the thread's transcript with the bot's author label and the wall-clock time of posting.

**Timeout reply**

- Triggered when the invocation timeout (default 120s per [[opencode-invoke]]) fires on any vendor attempt.
- Body (verbatim suggested wording — exact prose may be tuned; the **rules** are: clearly say it timed out, do not blame a vendor, do not auto-retry):

  > Took too long, aborted. Try again or rephrase.

- No automatic retry. The user must re-send (which will be deduped + re-queued normally).
- Transcript: a single line is appended noting the timeout, so opencode sees on the next turn that the previous turn aborted.

**All-drained reply**

- Triggered when [[vendor-pool]] signals all vendors are in cooldown or auth-locked, **or** when fallthrough exhausts every remaining eligible vendor.
- Body must contain:
  - A short plain-English explanation: "All vendors exhausted (rate-limited or out of quota)."
  - The list of vendors and **why each is penalized** at the moment of reply — e.g. `chatgpt-oauth (rate-limited, retry in ~4m)`, `claude-oauth (auth failed — needs reconfigure)`, `gemini-api (rate-limited, retry in ~30s)`.
  - A link to the admin dashboard URL (configured per deploy).
- Suggested shape:

  > All vendors exhausted (rate-limited or out of quota).
  > - chatgpt-oauth — rate-limited, retry in ~4m
  > - claude-oauth — auth failed, needs reconfigure
  > - gemini-api — rate-limited, retry in ~30s
  > Check the dashboard at <url>.

- No automatic retry. Cooldowns lapse on their own; the user can re-send when they want to try again.
- Transcript: the all-drained reply is appended as the bot's turn for that trigger.

**Crash / error reply**

- Triggered when opencode exited non-zero with no classifiable failure pattern, or returned no usable JSON, or any other internal Espur error during the invocation phase.
- Every crash gets a **request ID** — a short opaque token (e.g. ULID or 8-char base32) — that Espur logs alongside the stderr/output of the crashed invocation.
- Body:

  > Internal error. Check logs. Request ID: `<id>`.

- The request ID must appear both in the user-visible reply and in the host logs for that invocation, exactly once each, so an operator can correlate.
- Auth-only-fails-everywhere collapses to the **all-drained** reply, not the crash reply (per README: "if all auth-failed, treat as drained").

**Coalesce ack — already posted by [[trigger]]**

- The "still thinking, will use your latest message" ack is posted at most once per coalesced run by the trigger layer, not by Reply.
- Reply does not re-acknowledge coalescing; the eventual single reply implicitly satisfies the coalesced batch.

## Outcome

For every trigger that reaches a terminal invocation outcome, exactly one reply is posted to the originating thread:

| Outcome      | Reply                                                              |
| ------------ | ------------------------------------------------------------------ |
| Success      | opencode's assistant text, verbatim, no decoration                 |
| Timeout      | Plain timeout message, no retry, no request ID                     |
| All-drained  | Drained message naming penalized vendors + dashboard URL           |
| Crash        | Error message with a request ID matching a log entry               |

The thread's transcript is appended with the reply text and the appropriate author label so future invocations see it via [[context-assembly]].

A trigger never produces zero replies and never produces more than one (the coalesce ack is owned by [[trigger]], not Reply).

## Notes

- TODO(decision): exact prose of timeout / drained / crash messages. Wording above is a strawman; user should pin once before code lands.
- TODO(decision): should the all-drained reply include retry-in estimates derived from `cooldown_until`, or only "rate-limited" / "auth failed" with no time? Suggest include estimates (rounded to nearest minute, "<1m" floor) since they actually help the user; confirm.
- TODO(decision): request ID format. Suggest 8-char Crockford base32 (e.g. `XK4Q7B9R`); confirm.
- TODO(decision): on auth-only-drained, should the reply text differ from generic drained (e.g. lead with "All configured vendors need reconfiguration" since waiting won't help)? Suggest yes — it changes what the user should do. Confirm.
- Reply must never include raw vendor error strings, stack traces, or credential fragments. Adapter sanitization is the last line of defense; the request ID lookup is how operators get the detail.
- If posting the reply itself fails (network error to the IM platform), the adapter retries within a small bounded window. Persistent post failure is logged and the request is considered done — Espur does not block the thread queue waiting on IM-side recovery.
