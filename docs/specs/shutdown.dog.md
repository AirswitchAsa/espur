# Behavior: Shutdown

## Condition

The Espur process receives a termination signal — `SIGTERM` or `SIGINT` — either from the container runtime, the operator, or a deploy rollout. Shutdown is also entered on an unrecoverable internal error that asks the process to exit cleanly.

## Description

**Two phases: drain, then stop.**

Espur uses a root context cancelled at signal receipt. Components observe cancellation and shut down in a fixed order that prioritizes user-visible coherence over speed.

**Phase 1 — stop taking new work.**

- Each [[adapter]] stops accepting **inbound** events: it stops reading from its transport (closes the gateway connection, stops listening on the webhook port). Any inbound platform-side retry that happens during shutdown is handled by the platform's normal retry on next boot, deduped via the [[trigger]] dedup table.
- The bot core's dispatcher stops pulling from adapter event channels.
- The web UI stops accepting new HTTP connections; in-flight requests are given a small grace window to finish (e.g. 5s).

After this phase, no new triggers enter any per-thread queue.

**Phase 2 — finish what's in flight.**

- Each per-thread queue has at most one in-flight invocation (per [[trigger]]) and at most one coalesced-waiting trigger. Shutdown waits for the **in-flight** invocation to complete, up to a hard drain deadline. The deadline is controlled by `ESPUR_SHUTDOWN_DRAIN` (default 30s) and is floored at `ESPUR_OPENCODE_TIMEOUT` so that whatever timeout the operator picked for one attempt always has room to finish.
- Coalesced-waiting triggers are **not** started. They remain on disk only via the dedup table — they were accepted at the IM platform level (and recorded in [[transcript]]) but produce no reply on this run. Their `platform_message_id`s remain in the dedup table, so a future user resend is deduped to a no-op. This matches the known minor gap acknowledged in [[adapter]].
- If the drain deadline expires with an invocation still running, the opencode child is killed with `SIGTERM` then `SIGKILL` after a short grace, exactly as in [[opencode-invoke]]'s timeout path. The user-visible reply for that trigger is not posted; a [[transcript]] `kind=system` record with `note=shutdown-aborted-turn` is appended.
- Adapters' outbound `Post` calls that are mid-retry honour the same root context cancellation: they return `ctx.Err()` and the caller handles per [[adapter]]'s "ctx cancelled" path.

**Phase 3 — close resources.**

- Adapter `Start` loops exit; their channels close.
- SQLite is closed cleanly (WAL checkpoint, then close). A failed checkpoint is logged at warn; the database remains consistent because of WAL semantics.
- Open file handles for transcript writes are flushed and closed.
- The process exits 0 if all phases completed within their deadlines; 1 if a hard kill was needed in phase 2; 2 if a resource close failed in phase 3 (rare, but distinguishable for the operator).

**No queue persistence.**

In-flight and coalesced-waiting triggers are not serialized to disk to be resumed on next boot. The contract is: anything that was successfully *recorded* (transcript, dedup table, OAuth state, vendor pool) survives; anything that was *in motion* (queues, opencode children, in-flight HTTP) does not. This keeps shutdown simple and avoids resume-paths that would have to deal with stale opencode state.

**A second signal.**

If a second `SIGTERM`/`SIGINT` arrives during shutdown, Espur escalates: the in-flight opencode child is killed immediately, the drain deadline is collapsed to zero, and phase 3 runs at once. A third signal causes immediate process exit without further cleanup.

**What the operator sees.**

- A structured log sequence: `event=shutdown.start signal=SIGTERM`, then per-component `event=shutdown.<comp>.done`, then `event=shutdown.complete exit_code=N`.
- Web UI is unreachable for the duration of phase 2+3; the reverse proxy returns 502/504 to any user during the drain window. This is normal container-rollout behavior and not Espur's problem to mask.
- No user-visible message is posted to IM platforms about the shutdown — the only signal a user gets is that their in-flight request received no reply (and they can resend, which will be deduped if Espur comes back fast enough or re-enqueued if the dedup window has lapsed).

## Outcome

After a clean shutdown:

- No opencode children are running.
- All durable state is consistent: SQLite WAL checkpointed, transcript files flushed.
- The in-flight invocation either completed and posted (its transcript record reflects success) or was aborted (its transcript record reflects `shutdown-aborted-turn`).
- Coalesced-waiting triggers are silently dropped — their message ids are in the dedup table so the platform's retries or the user's resends won't double-fire on next boot.
- Exit code distinguishes clean (0), drain-deadline-exceeded (1), and resource-close-failed (2) so a supervisor / orchestrator can react appropriately.

A subsequent boot per [[bootstrap]] resumes from durable state with no knowledge of the prior in-motion work.

## Notes

- Default drain deadline of 30s, overridable via `ESPUR_SHUTDOWN_DRAIN`, with a runtime floor at `ESPUR_OPENCODE_TIMEOUT`. The floor exists because shipping a drain shorter than the per-invocation timeout makes every in-flight invocation a guaranteed abort. Operators with shorter container grace windows (e.g. Fly's 5s default) should either accept the abort rate or extend the platform grace window to match.
- Decided: phase-2 does NOT drain coalesced-waiting triggers — once draining begins, a message still sitting in a thread's coalesce slot is dropped. It was never started, and starting work after the shutdown signal would violate the "no new work" contract. In-flight invocations still finish.
- The deliberate decision to not serialize in-flight state to disk is a simplicity bet. The alternative (persistent queue + resume) is implementable later if real usage shows the dropped-turn rate is bad.
- Shutdown does not attempt to rotate or delete OAuth tokens or any other secret. Whatever was encrypted at rest stays as it was.
