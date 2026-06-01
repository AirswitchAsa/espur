# Espur

A minimal Go service that bridges IM platforms (Discord, WeChat, …) to [opencode](https://opencode.ai) as the agent runtime. Named after the Pokémon Espurr. Personal / non-commercial use.

When you @mention the bot on a thread, Espur shells out to `opencode run`, posts the reply back, and lets opencode keep its own long-term memory per thread.
<img width="994" height="710" alt="image" src="https://github.com/user-attachments/assets/43ccd038-fe49-44eb-888d-fad9d5ed4546" />

## Quickstart

```bash
# 1. Build the image
docker build -t espur .

# 2. Generate a master key (one-time)
KEY=$(docker run --rm --entrypoint /usr/local/bin/espur-genkey espur)

# 3. Run
docker run -d --name espur \
  -p 8080:8080 \
  -v espur-data:/data \
  -e ESPUR_MASTER_KEY=$KEY \
  -e ESPUR_DISCORD_TOKEN=...  \
  espur

# 4. (one-time per OAuth provider)
docker exec -it espur opencode auth login anthropic

# 5. Open the admin UI
open http://localhost:8080
```

`GET /healthz` is exposed for orchestrator probes; the rest of the admin UI should sit behind your reverse proxy's auth.

## Local development

```bash
go mod tidy
go run ./cmd/espur-genkey > .env   # then prefix with `ESPUR_MASTER_KEY=`
./scripts/dev.sh

go test ./...
dog lint docs/specs
```

`.env` minimal:

```env
ESPUR_MASTER_KEY=AGE-SECRET-KEY-1...
ESPUR_LOG_LEVEL=debug
# ESPUR_DISCORD_TOKEN=...
# ESPUR_WECHAT_ENABLED=1
```

## Docs

- [`docs/overview.md`](docs/overview.md) — what Espur does and how it behaves.
- [`docs/architecture.md`](docs/architecture.md) — tech stack, repo layout, env-var reference, build order.
- [`docs/testing.md`](docs/testing.md) — end-to-end acceptance testing with Docker (golden path, coalescing, graceful drain, persistence).
- [`docs/specs/*.dog.md`](docs/specs/) — authoritative behavioral contracts. If prose docs disagree with a spec, the spec wins.

## License

MIT. Personal / non-commercial use intended. The Pokémon-derived name carries its own usage caveats — don't ship this as a paid product.
