# End-to-end testing (Docker)

A manual acceptance procedure for a real Espur container talking to a real IM
platform and a real opencode vendor. This is the "does it actually work for a
user" pass — for the Go unit/race suite see the README's _Local development_
section.

Full env-var reference lives in [`architecture.md`](architecture.md#configuration);
the authoritative behavioral contracts are in [`specs/`](specs/).

## What you need

- Docker.
- A **Discord bot token** (Developer Portal → your app → Bot → Reset Token),
  with the **Message Content Intent** enabled, and the bot invited to a server
  you can post in.
- One working **vendor credential**, either:
  - a BYO API key (e.g. an Anthropic/OpenAI key), or
  - an OAuth provider you'll log into via `opencode auth login` (step 6).

## 1. Build the image

```bash
docker build -t espur .
```

## 2. Generate a master key (one-time)

```bash
KEY=$(docker run --rm --entrypoint /usr/local/bin/espur-genkey espur)
echo "$KEY"   # AGE-SECRET-KEY-1...  — save this; losing it makes stored creds unrecoverable
```

## 3. Run the container

```bash
docker run -d --name espur \
  -p 8080:8080 \
  -v espur-data:/data \
  -e ESPUR_MASTER_KEY="$KEY" \
  -e ESPUR_LOG_LEVEL=debug \
  -e ESPUR_DISCORD_TOKEN="<your-bot-token>" \
  espur
```

`-v espur-data:/data` is what makes step 11 (persistence) meaningful — the
SQLite DB, thread working dirs, and opencode's `auth.json` all live under
`/data`.

## 4. Verify it booted

```bash
curl -fsS http://localhost:8080/healthz && echo OK
docker logs espur | tail -20
```

Expect `OK` and, in the logs, a clean boot with no `master key mismatch` and a
line showing the Discord adapter connecting (you set the token, so it starts).
A missing/empty `ESPUR_MASTER_KEY` aborts boot with a non-zero exit — that's the
intended fail-fast, not a bug.

## 5. Configure a vendor (admin UI)

Open <http://localhost:8080>. Add a vendor:

- **BYO**: set `vendor_id`, `model` (e.g. `anthropic/claude-haiku-4-5`), the
  **env var name** the key should be exposed under (e.g. `ANTHROPIC_API_KEY`),
  and paste the secret. Remember: one secret value per vendor — the env-var
  field names how that one value is exposed, it is not a second secret.
- **OAuth**: add the vendor without a key, then do step 6.

## 6. (OAuth only) Log opencode in

OAuth is delegated to opencode itself, sharing `/data` so the child processes
see the same `auth.json`:

```bash
docker exec -it espur opencode auth login anthropic
```

Back in the UI the vendor's OAuth status should now read connected.

## 7. Golden path — @mention the bot

In your Discord server, in a channel the bot can see:

```
@your-bot hello, what model are you?
```

Expect a reply in the same thread within the invocation timeout (default 120s).
Confirm in `docker logs -f espur` that a trigger was accepted, an opencode child
ran, and a reply was posted with a success outcome.

## 8. Coalescing — rapid follow-up

While the bot is still working on one message, send a second mention in the
**same** thread before the first reply lands:

```
@your-bot summarize the last 3 PRs
@your-bot actually, just the most recent one      <- send quickly after
```

Expected behavior (per [`specs/trigger.dog.md`](specs/trigger.dog.md)):

- You get **one** _"still thinking, will use your latest message"_ ack, not one
  per message.
- When the in-flight run finishes, the bot processes the **latest** message and
  replies to that — the superseded middle messages are coalesced away.

## 9. Graceful drain on shutdown

This exercises the drain fix directly. Send a mention, and **while opencode is
still running**, stop the container with a grace period longer than the drain
deadline (default `ESPUR_SHUTDOWN_DRAIN=30s`):

```bash
docker stop -t 60 espur
docker logs espur | tail -30
```

Expected:

- The in-flight reply is **delivered before the process exits** — it is not
  dropped. (`docker stop` sends `SIGTERM`; Espur stops accepting new triggers,
  drains the in-flight invocation, then exits.)
- New mentions sent _after_ the SIGTERM are ignored (no reply, no ack).
- Logs show an ordered shutdown, not a hard kill. If you instead use the default
  `docker stop` (10s grace) while a >10s invocation is running, Docker will
  `SIGKILL` it before the drain completes — so use `-t 60` to actually observe
  the drain.

Restart for the next step:

```bash
docker start espur
```

## 10. Vendor failover (optional)

If you configured two vendors, temporarily break the first (wrong API key /
revoked OAuth) and mention the bot. Expect the pool to penalty-box the failing
vendor and serve the reply from the next one; logs show the classify + backoff.
With **all** vendors drained you should get the "all vendors unavailable" reply
(which includes `ESPUR_DASHBOARD_URL` if you set it).

## 11. Persistence across restart

```bash
docker restart espur
curl -fsS http://localhost:8080/healthz && echo OK
```

- Your vendor config and credentials are still present in the UI (decryptable
  because the same `ESPUR_MASTER_KEY` is supplied).
- Re-deliver / re-mention an _already-handled_ message id (e.g. Discord replays
  on reconnect): it must **not** be double-processed — the dedup table survives
  the restart.

## 12. WeChat (optional)

WeChat is opt-in and uses QR login. Add `-e ESPUR_WECHAT_ENABLED=1` to step 3,
then watch the logs / UI for the login QR on first run and scan it with the
WeChat app. Group mentions (`@bot ...`) trigger; plain DMs do not mention at the
adapter layer.

## 13. Teardown

```bash
docker rm -f espur
docker volume rm espur-data   # wipes DB + creds + thread state — destructive
```

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Boot exits immediately, non-zero | `ESPUR_MASTER_KEY` unset/empty, or mismatched against an existing `/data` DB ("master key mismatch") |
| Bot online but never replies to mentions | Message Content Intent not enabled on the Discord app, or no vendor configured |
| Reply dropped on `docker stop` | Used the default 10s grace; raise it: `docker stop -t 60` |
| `opencode auth login` doesn't stick | Container not using a persistent `/data` volume, so `auth.json` is lost on restart |
| Two replies to one platform retry | dedup regression — the same `(platform, message_id)` should only process once |
