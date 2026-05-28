# Behavior: WebUI

## Condition

The operator accesses Espur's admin web UI in a browser. The UI runs on a separate HTTP port from any IM webhook listeners and is intended to sit behind a reverse proxy with HTTP basic auth or external SSO — Espur itself ships no login.

## Description

The web UI is scoped to operator administration. It does **not** expose end-user chat, per-thread settings panels, analytics dashboards, or a logs viewer (the host's log pipeline is the logs viewer).

The UI is server-rendered. v0.1 uses plain `html/template` plus the Pico.css classless CDN — no templ build, no htmx, no JS. Pages do a full reload on form submit. The README's old "templ + htmx" stack is a future-tense aspiration, not what ships. Forms `POST` and redirect (303) to a `GET` of the same listing page.

**Pages / sections**

1. **Vendors**
   - Lists every configured vendor entry in priority order, top = most preferred.
   - For each entry: `vendor_id`, model string, enabled toggle, credential status (`set` / `missing` / `expired`), and current penalty-box state (`eligible` / `cooldown until HH:MM` / `auth_locked`).
   - Actions on each entry:
     - **Reorder** — drag-to-reorder or up/down buttons; reorder writes the new priority list atomically to SQLite. Order takes effect on the next trigger; in-flight invocations are not interrupted.
     - **Enable / disable** toggle.
     - **Edit credentials** — opens the credential flow for that vendor type:
       - **BYO API key**: a single password field (`type="password"`), submitted over the UI's own connection, stored encrypted via the secrets layer. The current key is never echoed back to the browser; only `set` / `missing` is shown.
       - **OAuth**: Espur does not own the flow. The vendor row simply uses `cred_kind=oauth` and the operator authorises the matching provider via `opencode auth login` from a shell — see [[oauth]] and the `/oauth` status page below. There is no "Connect" button on the vendor row in v0.1.
     - **Clear penalty** — manually returns the vendor to `eligible` (resets streak, drops cooldown). For `auth_locked`, "Clear penalty" requires the operator to also re-save credentials in the same session, otherwise the vendor will re-lock on next attempt.
     - **Delete** — removes the vendor entry entirely (credentials wiped, history of penalty state discarded).
   - An **Add vendor** action appends a new entry to the bottom of the list with a chosen `vendor_id` template (`chatgpt-oauth`, `claude-oauth`, `gemini-api`, generic `byo-key`).

2. **Threads**
   - Lists every thread that has a working directory under `data/threads/`.
   - Columns: `thread_id` (with platform prefix), platform, claim status (`idle` / `processing` / `coalesced waiting`), working-directory size on disk, last-activity timestamp.
   - Default sort: last-activity, most-recent first.
   - Clicking a row opens a **Thread detail** view:
     - A read-only **peek** at the thread's current `AGENTS.md` (rendered as monospace, no edit affordance — Espur does not edit it).
     - A list of `fact_<slug>.md` files in the working dir, with size and modification time; clicking opens the file as plaintext peek.
     - The recent transcript tail (same lines that [[context-assembly]] would pull on a fresh trigger).
     - No action buttons in v0.1 — no manual delete, no manual re-seed. Operator-level cleanup is via the host filesystem.

3. **Status / home**
   - A small landing page summarising: number of vendors (eligible / cooldown / auth-locked counts), number of threads, in-flight invocations, last error timestamp (if any).
   - Quick links to the Vendors and Threads pages.

4. **OAuth providers** (`/oauth`)
   - Read-only listing of providers configured in opencode's auth file: provider id, `type` (`api` / `oauth` / etc.), and whether a credential value is present. Token bytes are never displayed.
   - Includes copy-pasteable `opencode auth login <provider>` and `docker exec` commands targeted at the running deploy's `XDG_DATA_HOME`, so the operator never has to look up the right path.
   - This is the **only** OAuth surface in v0.1. See [[oauth]].

5. **Health** (`/healthz`)
   - Lightweight liveness JSON: `{ok, adapters:[{platform, healthy}]}`. 200 when every registered adapter is healthy; 503 (same body shape) otherwise.
   - Intended for reverse-proxy / orchestrator probes; deploys SHOULD allow this path past upstream auth. See [[observability]].

**What is explicitly out of scope**

- Per-user permissions / role management.
- Editing `AGENTS.md` or `fact_*.md` from the UI.
- Per-thread overrides of timeouts, model, transcript-tail length.
- Charts, analytics, request volume graphs.
- A separate logs viewer (use host logs).
- End-user-facing surface (chat, signup, anything not for the operator).

**Operational properties**

- All UI writes (reorder, enable/disable, credential save, clear penalty, delete) take effect atomically against SQLite and become visible to the next trigger; no process restart required.
- The UI never exposes plaintext credentials. Encrypted credential blobs are decrypted only in-process when about to be passed to a vendor invocation environment, never echoed to HTML.
- The dashboard URL referenced in the all-drained user reply (see [[reply]]) points at this UI's base URL — configurable per deploy.

## Outcome

After interacting with the UI:

- Vendor configuration (entries, order, credentials, enable state, penalty state) is the source of truth that [[vendor-pool]] consults on the next trigger.
- Operator has visibility into which threads exist, how big they are, what they remember in `AGENTS.md`, and what the bot last saw on each.
- No end-user state is mutated through the UI (no thread deletion in v0.1, no chat injection from the UI).

## Notes

- Decided: default UI port is `:8080` (`ESPUR_WEB_PORT`).
- Decided: Espur ships zero in-process auth — the admin UI relies entirely on reverse-proxy auth. Any in-process auth would have to be specced separately.
- OAuth callback URLs: not applicable. v0.1 delegates OAuth to opencode's own CLI per [[oauth]]; the admin port hosts no callback path.
- TODO(decision): does the operator want a "test this vendor now" button on the vendor row (fires a canned `opencode run` against it and shows ok / error)? Useful but not in README. Out of scope for v0.1 unless confirmed.
- TODO(decision): retention / hard-delete of a thread from the UI. README is silent and the threads page in this spec has no delete button. Confirm v0.1 deliberately omits it.
- Decided: the UI exposes transcript / `AGENTS.md` content; reverse-proxy auth in front of the admin port is therefore mandatory at deploy time (the UI itself has no auth — see above).
