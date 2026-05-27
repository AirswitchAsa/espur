# Espur

A minimal chat bot that bridges IM platforms (Discord, WeChat, Slack, ...) to [opencode](https://opencode.ai) as the agent runtime. Named after the Pokémon Espurr. Personal / non-commercial use.

---

## What it is

Espur is a single Go binary that:

1. Listens to one or more IM platforms via thin adapters.
2. When @mentioned on a thread, assembles a fresh context and shells out to `opencode run`.
3. Posts opencode's reply back to the thread.
4. Lets opencode maintain its own long-term memory in an `AGENTS.md`-shaped index, scoped per thread.

It is **not** a coding agent for your phone. It is **not** a multi-user SaaS. It is a personal-deploy chat surface for getting work done through opencode from inside IM clients.

---

## Behavior spec (high-level)

### Trigger

- Espur only acts on a message when **@mentioned** (DM counts as implicit mention).
- One **queue per thread** (channel / group / DM). Messages on the same thread are processed serially. Bursts beyond one queued message are dropped or coalesced with a "still thinking" reply.
- IM webhook retries are deduped by message ID.

### Context assembly

For each trigger, Espur builds a fresh opencode invocation. opencode is **stateless** per invocation — there is no persistent opencode session.

The assembled user message contains:

- **Thread context** — last N lines of the channel transcript, verbatim, labelled as recent conversation.
- **Request** — the current incoming message, highlighted as the thing to act on.

The working directory for opencode persists per thread and contains:

- `AGENTS.md` — the memory index, owned and edited by opencode itself.
- `fact_<slug>.md` — detail files written by opencode when something is worth more than a one-liner.
- Any scratch files opencode chooses to keep.

### Memory

Espur seeds each new thread's `AGENTS.md` with instructions telling opencode to:

- Treat the file as a long-term memory index across conversations on this thread.
- Keep entries to **one line each**, in the form `[short title](fact_<slug>.md) — gloss`.
- Write detail to a new `fact_<slug>.md` and add an index entry pointing to it.
- Read detail files on demand via the `read` tool rather than expanding the index.
- Update or remove entries when facts change.

Espur does **not** parse or enforce memory format at runtime. The discipline lives in the seed prompt. If it breaks down in practice, structural enforcement gets bolted on later.

### Vendor pool

- One ordered list of vendors configured via the web UI, e.g. `[chatgpt-oauth, claude-oauth, gemini-api]`.
- Each vendor is the same opencode invocation with the `--model` flag swapped.
- Always start from the **top** of the priority list per trigger.
- On vendor failure, fall through to the next.
- "Failure" means: HTTP 429, "quota exceeded", "usage limit", "high concurrency", or persistent 5xx. Error patterns cribbed from the `opencode-rate-limit-fallback` plugin source.
- A failed vendor enters a **penalty box** (cooldown, exponential backoff with jitter, persisted in SQLite). Subsequent triggers skip vendors currently in the penalty box.
- 401 / 403 puts a vendor in a permanent penalty until reconfigured via the web UI.

### Reply

- **Batch only.** No streaming. Espur posts one reply when opencode returns. This keeps the cross-platform code identical.
- **All vendors drained** → reply with: *"All vendors exhausted (rate-limited or out of quota). Check the dashboard at `<url>`."* Include which vendors are penalized.
- **Invocation timeout** (default 120s) → reply with a clear timeout message; do not retry automatically.

### Web UI

A small admin UI on a separate port. Scope:

- Configure provider credentials (BYO keys, OAuth flows for ChatGPT/Claude subs).
- Order the vendor priority list.
- See penalty-box state per vendor.
- List threads with their claim status, working-dir size, and last activity.
- Peek at a thread's `AGENTS.md` and recent transcript.

No analytics, no per-thread settings panel (use sensible defaults), no separate logs viewer (use host logs).

### Sandboxing

The deployment unit **is** the sandbox. Run Espur in a container or a small VM. opencode runs as a child process with full tool access (`read`, `write`, `edit`, `bash`) scoped to its working directory. Do not attempt per-invocation Docker.

### Failure modes the user can see

| Scenario | Reply |
| -------- | ----- |
| All vendors drained | Plain message naming the drained vendors, link to dashboard |
| Timeout | "Took too long, aborted. Try again or rephrase." |
| opencode crash | "Internal error. Check logs." (with a request ID) |
| Auth failure on selected vendor | Silent fallthrough to next vendor; if all auth-failed, treat as drained |
| Memory file write conflict | Should not happen — per-thread queue prevents concurrent writes |

---

## How to start

> Specs first. Code second.

### 1. Write DOG specs before code

Espur uses [DOG](https://pypi.org/project/dog-cli/) (Documented Operational Guarantees) as the source of truth for behavior. Specs in `specs/*.dog.md` describe what the bot does; code must trace back to a spec; CI runs `dog check`.

```bash
pipx install dog-cli
dog --help
```

For each subsystem (adapter, queue, context assembly, vendor pool, memory seed, web UI), write the `.dog.md` *before* writing the Go file it describes. When behavior changes, **update the spec first**, then the code, then run `dog check` to confirm they agree.

If a design doc and a DOG spec disagree, the DOG spec wins. Design docs are scratch.

### 2. Tech stack

| Layer | Choice | Why |
| ----- | ------ | --- |
| Language | Go 1.23+ | Single binary, fast startup, great stdlib for HTTP + sqlite |
| Web UI | `html/template` + [Pico.css](https://picocss.com) (classless, via CDN) | Deliberately minimal in v0.1 — no JS build, no htmx, no templ. Forms POST and 303-redirect. |
| Storage | SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) | Pure-Go driver, no CGo, one file on disk |
| Secrets | [age](https://github.com/FiloSottile/age) for column-level encryption | Master key from env var at boot. OAuth credentials are not stored by Espur — see "OAuth" below. |
| Agent runtime | opencode CLI (`opencode run --format json --model …`) | Stateless invocation per trigger |
| IM adapters | One package per platform under `internal/adapter/` | Discord (gateway) + WeChat personal via [openwechat](https://github.com/eatmoreapple/openwechat) (QR-login) |
| Testing | stdlib `testing` | Boring |
| Build | `go build` + multi-stage Dockerfile | No Make heroics |

### 3. Repo organization

```
espur/
├── README.md
├── LICENSE                 # MIT
├── go.mod
├── Dockerfile
├── .dockerignore
├── .gitignore              # data/, *.db, .env
├── cmd/
│   └── espur/
│       └── main.go         # entrypoint, wires everything
├── internal/
│   ├── adapter/            # IM platforms
│   │   ├── adapter.go      # Adapter interface
│   │   ├── discord/
│   │   └── wechat/
│   ├── bot/                # core: queue, trigger routing
│   ├── context/            # transcript tail + prompt assembly
│   ├── memory/             # working-dir lifecycle, AGENTS.md seeding
│   ├── opencode/           # invoker + vendor pool + fallback
│   ├── transcript/         # JSONL append + tail per thread
│   ├── store/              # SQLite schema, migrations, queries
│   ├── secrets/            # age-encrypted credential storage
│   └── web/                # templ components + htmx handlers
├── specs/
│   └── *.dog.md            # behavioral specs — source of truth
├── data/                   # gitignored — runtime state
│   ├── espur.db            # SQLite
│   └── threads/<thread_id>/  # opencode working dirs
└── scripts/
    └── dev.sh              # run with air + templ watcher
```

### 4. Dev workflow

```bash
# one-time
go mod tidy

# generate a master key (run once, paste into .env or your shell)
go run ./cmd/espur-genkey

# run (reads .env if present)
./scripts/dev.sh

# specs
dog lint specs

# tests + lint
go test ./...
go vet ./...
gofmt -l .  # should print nothing
```

`.env` minimal example:

```env
ESPUR_MASTER_KEY=AGE-SECRET-KEY-1...   # from espur-genkey
ESPUR_LOG_LEVEL=debug
# optional adapters:
# ESPUR_DISCORD_TOKEN=...
# ESPUR_WECHAT_ENABLED=1
```

### 5. Deploy

Single Docker container (multi-stage build: Go binary on a Node 20 Alpine runtime that has `opencode-ai` pre-installed). Mount a volume at `/data` for SQLite, thread working dirs, opencode's auth file, and the WeChat hot-reload session. Pass `ESPUR_MASTER_KEY` (age identity) as an env var. The container ships with `XDG_DATA_HOME=/data/xdg-data` and `HOME=/data` so `opencode auth login` and Espur's children share one auth file.

Build + run:

```bash
docker build -t espur .
docker run -d --name espur \
  -v espur-data:/data \
  -e ESPUR_MASTER_KEY=AGE-SECRET-KEY-1... \
  -e ESPUR_DISCORD_TOKEN=... \
  -p 8080:8080 \
  espur
```

Expose:

- IM adapter ports as required (Discord is outbound gateway, no inbound; WeChat is QR-login + long-poll, no inbound).
- Web UI port (default `:8080`) **behind a reverse proxy with HTTP basic auth or your own SSO**. Allow `GET /healthz` past auth for orchestrator probes; the rest of `/` is operator-only.

OAuth bring-up (one-time per provider — see `specs/oauth.dog.md`):

```bash
docker exec -it espur opencode auth login anthropic
# or: openai, copilot, ... whichever opencode supports
```

Graceful shutdown is the responsibility of the container runtime sending `SIGTERM`. Espur drains in-flight invocations for up to `ESPUR_SHUTDOWN_DRAIN` (default 30s, floored to `ESPUR_OPENCODE_TIMEOUT`) before forcing a hard cancel. Set Kubernetes `terminationGracePeriodSeconds` (or your platform's equivalent) to at least that value.

Recommended host: a small EC2 / Fly / Hetzner box. The container *is* the sandbox.

### 6. Order to build things in

1. ✅ **Specs for the trigger flow** in `specs/` — adapter → queue → context assembly → opencode invoke → reply.
2. ✅ **opencode invoker + vendor pool** — the riskiest unknown. One-vendor invocation working end-to-end from a Go test.
3. ✅ **SQLite store + secrets** — needed by the web UI and the vendor pool.
4. ✅ **Discord adapter**.
5. ✅ **Transcript + context assembly**.
6. ✅ **Memory seed**.
7. ✅ **Web UI** — vendor config, thread list, OAuth status. (OAuth flows themselves are delegated to `opencode auth login`; see `specs/oauth.dog.md`.)
8. ✅ **WeChat adapter** — personal account via [openwechat](https://github.com/eatmoreapple/openwechat); opt-in via `ESPUR_WECHAT_ENABLED=1`.
9. ✅ **Penalty box** — exponential backoff with jitter, auth-locked permanent state.
10. ✅ **Graceful shutdown + observability** — phase-ordered drain, JSON logs to stdout with stable `event=` names, `/healthz`.

Not yet shipped (would push past v0.1):

- Real-world OAuth smoke against a live provider account.
- Real-world WeChat smoke against an actual QR-login session.
- Global per-process concurrency cap on opencode children.
- Per-thread / per-vendor "test now" affordances in the UI.

Each step ships behind a spec. Each step gets a `dog lint specs` pass before moving on.

---

## License

MIT. Personal / non-commercial use intended. Pokémon-derived name carries its own usage caveats — don't ship this as a paid product.
