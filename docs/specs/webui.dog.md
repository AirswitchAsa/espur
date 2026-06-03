# Behavior: WebUI

## Condition

The operator accesses Espur's admin web UI in a browser. The UI runs on a separate HTTP port from any IM webhook listeners and is intended to sit behind a reverse proxy with HTTP basic auth or external SSO — Espur itself ships no login.

## Description

The web UI is scoped to operator administration. It does **not** expose end-user chat, per-thread settings panels, analytics dashboards, or a logs viewer (the host's log pipeline is the logs viewer).

The UI is server-rendered. v0.1 uses plain `html/template` plus the Pico.css classless CDN — no templ build, no htmx, no JS. Pages do a full reload on form submit. The README's old "templ + htmx" stack is a future-tense aspiration, not what ships. Forms `POST` and redirect (303) to a `GET` of the same listing page.

**Pages / sections**

1. **Vendors**
   - Lists every configured vendor entry in priority order, top = most preferred.
   - For each entry: `vendor_id`, model string, enabled toggle, credential status (`set` / `missing`, plus the OAuth-aware `linked` / `pending`), and current penalty-box state (`eligible` / `cooldown until HH:MM` / `auth_locked`).
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
   - Columns: `thread_id` (with platform prefix), platform, working-directory size on disk, last-activity timestamp. (Live claim status — idle / processing — is not currently surfaced as a column; the thread's queue state lives in the bot core, not on disk.)
   - Default sort: last-activity, most-recent first.
   - Clicking a row opens a **Thread detail** view:
     - The thread's **bot-owned memory files** — every `*.md` in the working dir except `AGENTS.md` (i.e. `memory_index.md` and the `<slug>.md` detail files, plus any legacy `fact_<slug>.md`), each with size and a plaintext body peek.
     - The full working-directory file listing (names, sizes, mod times, subdirs).
     - An **edit** affordance for the operator custom-instructions block: the UI extracts the text between the `AGENTS.md` markers (see [[memory-seed]]) into an editor and, on save (`POST …/instructions`), rewrites **only** that block via `ReplaceUserInstructions`. The system instructions above the markers are never touched. The change is picked up on the next invocation.
     - The recent transcript tail (the same `DefaultTailN` lines that [[context-assembly]] would pull on a fresh trigger).
     - **Wipe memory** (`POST …/wipe-memory`) — deletes the bot's memory files (`memory_index.md` + slug/`fact_*` files), keeping `AGENTS.md` (instructions + operator block) and the transcript.
     - **Delete thread** (`POST …/delete`) — removes the entire thread working directory (guarded to refuse any path outside the `threads/` root).

3. **Connections / Settings** (`/settings`)
   - Manages the IM connections Espur runs (the multi-connection model — see [[adapter]]). Each connection is an enable-able, web-managed instance of a platform adapter, persisted in SQLite.
   - Actions: **add** a Discord connection (`POST /connections/discord`) or a WeChat connection (`POST /connections/wechat`); **enable** / **disable** (`POST /connections/{id}/enable|disable`); **delete** (`POST /connections/{id}/delete`).
   - WeChat first-login is a **QR** flow: the page serves the login QR image (`GET /connections/{id}/qr.png`) and the settings page auto-refreshes while a connection is starting, so the operator scans and the connection comes up without a manual reload. The iLink session is persisted encrypted so later boots skip the QR step (see [[bootstrap]]).
   - Connection-management *behavior* (lifecycle, identity, persistence) is owned by the adapter/connection-manager layer; this page is its operator surface.

4. **Status / home**
   - A small landing page summarising: number of vendors (eligible / cooldown / auth-locked counts), number of threads, in-flight invocations, last error timestamp (if any).
   - Quick links to the Vendors and Threads pages.

5. **OAuth providers** (`/oauth`)
   - Read-only listing of providers configured in opencode's auth file: provider id, `type` (`api` / `oauth` / etc.), and whether a credential value is present. Token bytes are never displayed.
   - Includes copy-pasteable `opencode auth login <provider>` and `docker exec` commands targeted at the running deploy's `XDG_DATA_HOME`, so the operator never has to look up the right path.
   - This is the **only** OAuth surface in v0.1. See [[oauth]].

6. **Health** (`/healthz`)
   - Lightweight liveness JSON: `{ok, adapters:[{platform, healthy}]}`. 200 when every registered adapter is healthy; 503 (same body shape) otherwise.
   - Intended for reverse-proxy / orchestrator probes; deploys SHOULD allow this path past upstream auth. See [[observability]].

**What is explicitly out of scope**

- Per-user permissions / role management.
- Editing the bot's memory files (`memory_index.md` / `<slug>.md`) directly from the UI — only the operator custom-instructions block of `AGENTS.md` is editable; the memory files are bot-owned (wipe-all is offered, per-file edit is not).
- Per-thread overrides of timeouts, model, transcript-tail length.
- Charts, analytics, request volume graphs.
- A separate logs viewer (use host logs).
- End-user-facing surface (chat, signup, anything not for the operator).

**Operational properties**

- All UI writes (reorder, enable/disable, credential save, clear penalty, delete) take effect atomically against SQLite and become visible to the next trigger; no process restart required.
- The UI never exposes plaintext credentials. Encrypted credential blobs are decrypted only in-process when about to be passed to a vendor invocation environment, never echoed to HTML.
- **CSRF / same-origin.** Every state-changing request (POST) is checked against a lenient same-origin rule: if the request carries an `Origin` (or `Referer`) whose host differs from the request `Host`, it is rejected with `403`. The check is deliberately permissive so it can't lock out the operator — a request with neither header is allowed (some reverse proxies strip both), and hosts listed in `ESPUR_WEB_TRUSTED_ORIGINS` (comma-separated `host[:port]`) are always accepted for proxies that rewrite `Host`. This is defense in depth; reverse-proxy auth in front of the admin port is still required (the UI ships no auth of its own).
- **Filesystem-mutating thread actions** (memory wipe, instructions edit, thread delete) and the read-only thread detail page all resolve their target directory through a single traversal guard that refuses any path escaping the `threads/` root, so a crafted/`%2f`-encoded `platform`/`enc_id` cannot read or delete outside it.
- The dashboard URL referenced in the all-drained user reply (see [[reply]]) points at this UI's base URL — configurable per deploy.

## Outcome

After interacting with the UI:

- Vendor configuration (entries, order, credentials, enable state, penalty state) is the source of truth that [[vendor-pool]] consults on the next trigger.
- Operator has visibility into which threads exist, how big they are, what they remember (`memory_index.md` + detail files), and what the bot last saw on each — and can edit the per-thread operator instructions, wipe a thread's memory, or delete the thread workdir.
- IM connections configured through the UI (add / enable / disable / delete) are the source of truth for which adapters Espur runs.
- No chat injection from the UI — the operator cannot post messages into a thread on the bot's behalf.

## Notes

- Decided: default UI port is `:8080` (`ESPUR_WEB_PORT`).
- Decided: Espur ships zero in-process auth — the admin UI relies entirely on reverse-proxy auth. Any in-process auth would have to be specced separately.
- OAuth callback URLs: not applicable. v0.1 delegates OAuth to opencode's own CLI per [[oauth]]; the admin port hosts no callback path.
- TODO(decision): does the operator want a "test this vendor now" button on the vendor row (fires a canned `opencode run` against it and shows ok / error)? Useful but not in README. Out of scope for v0.1 unless confirmed.
- Decided: thread hard-delete and memory-wipe are implemented (`POST …/delete`, `POST …/wipe-memory`), guarded to stay within the `threads/` root. The earlier "no delete in v0.1" stance is superseded.
- Decided: the UI exposes transcript / memory content; reverse-proxy auth in front of the admin port is therefore mandatory at deploy time (the UI itself has no auth — see above).
