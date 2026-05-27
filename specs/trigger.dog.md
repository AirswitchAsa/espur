# Behavior: Trigger

## Condition

An inbound message arrives on an IM platform that Espur has an adapter for (Discord, WeChat, Slack, ...), **and**:

- The message addresses the bot — either an explicit @-mention of Espur's user in a channel/group, or any message in a 1:1 DM (DM counts as an implicit mention).
- The platform has not already delivered this exact message before, identified by the platform-native message ID.

Messages that do not mention the bot are observed for transcript purposes but do not trigger.

## Description

The adapter normalizes the inbound event into a `Trigger` record with at least:

- `platform` — e.g. `discord`, `wechat`.
- `thread_id` — platform-stable identifier for the channel / group / DM the message arrived on. This is the queue key.
- `message_id` — platform-native unique ID of the message; used for deduplication.
- `author_id` — platform-native sender ID.
- `text` — message body with the bot mention stripped, whitespace-trimmed.
- `received_at` — adapter-side timestamp.

Dedup is by `(platform, message_id)`. A repeat delivery (webhook retry, reconnect replay) is dropped silently — no work, no reply.

Each `thread_id` has exactly one serial work queue. Triggers for that thread are processed strictly one at a time, in arrival order.

**Burst rule** — at most one trigger may be queued per thread while another trigger for the same thread is in flight:

- If the queue is empty: enqueue the trigger.
- If the queue holds one waiting trigger already: **coalesce** — replace the waiting trigger's text with the newer message's text, keep the newer `message_id` and timestamp, and post a one-time "still thinking, will use your latest message" acknowledgement on the thread. Only one such ack per coalesced run.
- Further messages arriving during the same in-flight run continue to coalesce into that single waiting slot.

DMs follow the exact same queueing rule as channels; the DM is itself the thread.

## Outcome

For every accepted, non-duplicate, non-coalesced-away message that mentions the bot, exactly one normalized `Trigger` is appended to the matching thread's queue and will eventually be processed. Coalesced triggers result in exactly one downstream invocation that sees the most recent user text.

Dropped duplicates and coalesced predecessors produce no opencode invocation and no reply other than the single "still thinking" ack noted above.

## Notes

- The bot's own outbound replies must never be treated as triggers, even if they quote the user.
- Mention detection is per-platform: Discord uses the bot user ID, WeChat uses configured nickname / @-token matching, etc. The adapter is the only place that knows this.
- Edited messages are out of scope for the first cut — treat the edit as a no-op, not a new trigger.
- TODO(decision): how long should the dedup memory of `(platform, message_id)` be retained? README does not say. Suggest 24h sliding window, persisted in SQLite, but confirm.
- TODO(decision): when an adapter restarts and replays history, should already-processed messages re-trigger? Implied "no" by the dedup rule, but the dedup table must outlive process restarts for that to hold — confirm SQLite-backed dedup is intended.
