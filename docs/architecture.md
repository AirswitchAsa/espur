# Architecture

## Tech stack

| Layer | Choice | Why |
| ----- | ------ | --- |
| Language | Go 1.25+ | Single binary, fast startup, great stdlib for HTTP + sqlite |
| Web UI | `html/template` + [Pico.css](https://picocss.com) (classless, via CDN) | Deliberately minimal in v0.1 — no JS build, no htmx, no templ. Forms POST and 303-redirect. |
| Storage | SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) | Pure-Go driver, no CGo, one file on disk |
| Secrets | [age](https://github.com/FiloSottile/age) for column-level encryption | Master key from env var at boot. OAuth credentials are not stored by Espur — delegated to `opencode auth login`. |
| Agent runtime | opencode CLI (`opencode run --format json --model …`) | Stateless invocation per trigger |
| IM adapters | One package per platform under `internal/adapter/` | Discord (gateway) + WeChat personal via the iLink Bot API SDK [openilink-sdk-go](https://github.com/openilink/openilink-sdk-go) (HTTP/JSON long-poll, QR-image login) |
| Testing | stdlib `testing` (+ Go fuzz) | Boring |
| Build | `go build` + multi-stage Dockerfile | No Make heroics |

## Repo layout

```
espur/
├── README.md
├── LICENSE                 # MIT
├── go.mod
├── Dockerfile
├── .dockerignore
├── .gitignore              # data/, *.db, .env
├── cmd/
│   ├── espur/main.go       # entrypoint, wires everything
│   └── espur-genkey/       # master-key generator helper
├── internal/
│   ├── adapter/            # IM platforms
│   │   ├── adapter.go      # Adapter interface
│   │   ├── textchunk/      # shared cap-respecting splitter
│   │   ├── discord/
│   │   └── wechat/
│   ├── bot/                # core: queue, trigger routing, reply formatting
│   ├── contextasm/         # transcript tail + prompt assembly
│   ├── memory/             # working-dir lifecycle, AGENTS.md seeding
│   ├── opencode/           # invoker + auth.json reader
│   ├── vendor/             # pool, classify, penalty box
│   ├── transcript/         # JSONL append + tail per thread
│   ├── store/              # SQLite schema, migrations, queries
│   ├── secrets/            # age-encrypted credential storage
│   ├── obs/                # event-name registry for slog
│   └── web/                # admin UI handlers + templates
├── docs/
│   ├── overview.md         # behavior tour (prose)
│   ├── architecture.md     # this file
│   └── specs/*.dog.md      # behavioral specs — source of truth
├── data/                   # gitignored — runtime state
│   ├── espur.db            # SQLite
│   └── threads/<thread_id>/  # opencode working dirs
└── scripts/
    └── dev.sh              # local launcher
```

## Specs are source of truth

Espur uses [DOG](https://pypi.org/project/dog-cli/) (Documented Operational Guarantees) for behavioral contracts. Specs in `docs/specs/*.dog.md` describe what the bot does; code must trace back to a spec; `dog lint docs/specs` runs cleanly.

For each subsystem (adapter, queue, context assembly, vendor pool, memory seed, web UI), the `.dog.md` is written *before* the Go file it describes. When behavior changes: **update the spec first**, then the code, then `dog lint` to confirm they agree.

If a design doc and a DOG spec disagree, the DOG spec wins. Design docs are scratch.

```bash
pipx install dog-cli
dog lint docs/specs
```

## Order things were built in

1. ✅ **Specs for the trigger flow** — adapter → queue → context assembly → opencode invoke → reply.
2. ✅ **opencode invoker + vendor pool** — the riskiest unknown. One-vendor invocation working end-to-end from a Go test.
3. ✅ **SQLite store + secrets** — needed by the web UI and the vendor pool.
4. ✅ **Discord adapter**.
5. ✅ **Transcript + context assembly**.
6. ✅ **Memory seed**.
7. ✅ **Web UI** — vendor config, thread list, OAuth status. (OAuth flows themselves are delegated to `opencode auth login`; see [`specs/oauth.dog.md`](specs/oauth.dog.md).)
8. ✅ **WeChat adapter** — personal account via the iLink Bot API (`openilink-sdk-go`); reply-only DMs (+ best-effort group send); opt-in via `ESPUR_WECHAT_ENABLED=1`.
9. ✅ **Penalty box** — exponential backoff with jitter, auth-locked permanent state.
10. ✅ **Graceful shutdown + observability** — phase-ordered drain, JSON logs to stdout with stable `event=` names, `/healthz`.
11. ✅ **Dockerfile + smoke** — multi-stage build (Go + Node 20 Alpine), opencode pre-installed, non-root.

Not yet exercised against real infrastructure:

- Real-world OAuth smoke against a live provider account.
- Real-world WeChat smoke against an actual iLink QR-login session (incl. verifying the best-effort group-send path, which the SDK does not exercise).
- Real-world Discord smoke against a live guild.
- Per-thread / per-vendor "test now" affordances in the UI.

## Configuration

All configuration is via environment variables; no config file.

| Var | Default | Purpose |
| --- | ------- | ------- |
| `ESPUR_MASTER_KEY` | *required* | age identity for credential encryption. Generate via `espur-genkey`. |
| `ESPUR_DATA_DIR` | `./data` (container: `/data`) | SQLite + thread working dirs + transcripts |
| `ESPUR_WEB_PORT` | `8080` | Admin UI port |
| `ESPUR_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `ESPUR_DASHBOARD_URL` | unset | Used in the "all vendors drained" reply |
| `ESPUR_OPENCODE_TIMEOUT` | `120s` | Per-invocation timeout |
| `ESPUR_OPENCODE_MAX_CONCURRENT` | `4` | Global concurrency cap on opencode children |
| `ESPUR_SHUTDOWN_DRAIN` | `30s` (floored to `OPENCODE_TIMEOUT`) | Drain deadline after SIGTERM |
| `ESPUR_DISCORD_TOKEN` | unset | If set, Discord adapter starts |
| `ESPUR_WECHAT_ENABLED` | unset | If `1`, WeChat adapter starts (iLink QR-login at first run) |
| `ESPUR_WECHAT_BOT_NAME` | unset | Bot display name used to detect `@`-mentions in WeChat groups. If unset, group mentions can't be detected and the bot only responds in DMs |
| `XDG_DATA_HOME` | container: `/data/xdg-data` | Shared with `opencode auth login` so child processes see the same auth.json |

See [`specs/bootstrap.dog.md`](specs/bootstrap.dog.md) for the authoritative list.
