# Behavior: Bootstrap

## Condition

The Espur binary is started by the operator (directly, via `air` in dev, or as a container entrypoint). The process inherits an environment that may or may not contain a complete configuration. `data/` may be empty (fresh deploy) or populated (restart).

## Description

**Configuration sources, in precedence order.**

1. **Environment variables** — for operational knobs that must be settable per-deploy without UI access:
   - `ESPUR_MASTER_KEY` (age identity, required)
   - `ESPUR_DATA_DIR` (default `./data`)
   - `ESPUR_WEB_PORT` (default `8080`)
   - `ESPUR_LOG_LEVEL` (`debug`/`info`/`warn`/`error`, default `info`)
   - `ESPUR_DASHBOARD_URL` (used in [[reply]]'s drained message)
   - `ESPUR_OPENCODE_TIMEOUT` (default `120s`)
   - `ESPUR_SHUTDOWN_DRAIN` (phase-2 drain deadline; default `30s`, floored to `ESPUR_OPENCODE_TIMEOUT` so an in-flight invocation always gets one full attempt window — see [[shutdown]])
   - `ESPUR_DISCORD_TOKEN` (presence enables the Discord adapter)
   - `ESPUR_WECHAT_ENABLED` (`1`/`true` opts into the personal-WeChat adapter; QR-login flow per [[adapter]])
   - `XDG_DATA_HOME` (defaults to `$ESPUR_DATA_DIR/xdg-data` if unset; opencode reads its auth file from `$XDG_DATA_HOME/opencode/auth.json`, so this is the shared anchor between `opencode auth login` and Espur's child invocations — see [[oauth]])
2. **SQLite-persisted state** — vendor list + priority, encrypted credentials, penalty-box state, dedup table. Owned by the web UI at runtime; not editable via env.
3. **Compiled-in defaults** — for anything not set by 1 or 2: transcript-tail N, failure-classification patterns, retry/backoff constants, chunking rules, seed-AGENTS.md template, etc.

No config file (YAML/TOML) in v0.1. Env vars + UI cover the surface.

**Boot sequence.**

1. **Parse env.** Missing required env (`ESPUR_MASTER_KEY`) → abort boot with a clear error and non-zero exit.
2. **Open or create the data directory.** `ESPUR_DATA_DIR` (default `./data`) must be writable. Subdirs (`threads/`, ...) are created lazily on first use.
3. **Open SQLite.** `data/espur.db`. Run migrations idempotently. A failed migration aborts boot.
4. **Run the secrets self-test** per [[secrets]]: pick any one existing encrypted blob and attempt decryption with the master key. Failure aborts boot.
5. **Load vendor pool state** from SQLite into memory (priority list, penalty-box). Empty list is a valid state — Espur boots, but [[trigger]] will produce all-drained replies until vendors are configured via the web UI.
6. **Construct adapters** for each enabled platform whose configuration is present. Adapter construction failure (bad token, malformed config) is logged at error but does **not** abort boot — Espur runs with the remaining adapters, and the web UI surfaces the down adapter. This makes the web UI reachable even when one platform is misconfigured, which is the only way the operator can fix it.
7. **Start the web UI** on `ESPUR_WEB_PORT`. Failure to bind aborts boot.
8. **Start each constructed adapter's `Start(ctx)` loop** per [[adapter]]. Failures here surface as `LifecycleEvent`s, not boot aborts.
9. **Mark process as up.** A simple structured log line `event=boot.ready` is emitted exactly once. The web UI status page shows green.

Boot is considered successful once steps 1–7 complete; steps 8–9 are best-effort. The process exits non-zero only when a step in 1–7 fails.

**Durable-state inventory.**

The complete set of state that survives a restart:

- `data/espur.db` (SQLite): vendor list + priority, encrypted credentials + metadata (BYO API keys), penalty-box state per vendor, message-ID dedup table per platform, any future operator-facing tables.
- `$XDG_DATA_HOME/opencode/auth.json` (under `data/xdg-data/` by default): opencode's own auth file holding OAuth bundles. Owned and rotated by `opencode auth login` — see [[oauth]]. Espur reads it for `/oauth` display only.
- `data/wechat-session.json` (optional, present only when [[adapter]] WeChat is enabled): openwechat hot-reload session blob so subsequent boots skip the QR-login step.
- `data/threads/<platform>/<encoded_id>/`: per-thread working directory containing `AGENTS.md`, any `fact_*.md` opencode wrote, the `transcript.jsonl`, and any opencode-side scratch.

Everything else is process-local and recomputed at boot:

- In-flight queues, coalesce slots, the live event channel from each adapter, the secrets-self-test result, the in-memory copy of the vendor pool, request IDs.

**Fresh deploy.**

- `data/` empty → SQLite is created with all schemas migrated up; secrets self-test is skipped (no blobs); vendor pool is empty; web UI is reachable; first action the operator takes is configure at least one vendor.
- The bot will reply with "all vendors exhausted" (drained form) to any message that arrives before a vendor is configured. This is correct behavior — the user-visible message names the dashboard, which is exactly what the operator needs to set up.

**Restart.**

- SQLite reopened; vendor priority + penalty box restored; encrypted credentials usable as long as `ESPUR_MASTER_KEY` matches; dedup table preserved so platform retries do not double-process messages that were already handled before restart.
- In-flight invocations at the moment of prior shutdown are lost; see [[shutdown]] and the known minor gap in [[adapter]].

**Sandboxing.**

- The container or VM Espur runs in **is** the sandbox boundary. Espur does not attempt per-invocation Docker, namespaces, or seccomp.
- opencode runs as a child process with full tool access scoped to its thread working directory, per [[opencode-invoke]].
- The deploy doc instructs operators to run Espur in a dedicated container or small VM, mount `/data` as a volume, and put the web UI behind a reverse proxy with external auth. The container's filesystem outside `/data` is treated as ephemeral.

## Outcome

After a successful boot:

- All operator-required env vars are present and valid.
- The data directory and SQLite database are usable; existing encrypted secrets are decryptable.
- The web UI is reachable on its configured port.
- Each configured adapter is either running or surfaced as down in the UI; in neither case does an adapter failure block boot.
- Durable state (SQLite + thread working dirs) is intact; ephemeral state (queues, channels, request IDs) is freshly initialized.
- A single `event=boot.ready` log line is emitted.

A failed boot exits non-zero and logs a single human-readable reason. Espur never runs in a "half-up" mode where required guarantees (master-key validity, DB writability, UI port available) are silently broken.

## Notes

- TODO(decision): exact env-var names listed above are strawmen; pin once the deploy doc lands. The set itself is fixed by the spec.
- TODO(decision): on a fresh deploy, should the web UI ship a first-time-setup banner that walks the operator through "add a vendor" / "connect an adapter"? Suggest yes, but minor and can be added post-v0.1.
- TODO(decision): does `ESPUR_DATA_DIR` accept absolute paths only, or also relative? Suggest both, resolved at boot; confirm.
- The deliberate refusal to bring config files into v0.1 is to keep deploys boring: a container with env vars and a mounted volume is the whole story.
