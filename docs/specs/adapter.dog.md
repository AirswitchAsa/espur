# Behavior: Adapter

## Condition

Espur is configured with zero or more **connections**, each one a configured instance of an IM platform (Discord, WeChat, Slack, ...). At process start, and whenever the admin adds a connection at runtime, the **connection manager** constructs one **Adapter** per enabled connection and calls `Start(ctx)`. Multiple connections of the same platform are allowed — each is an independent bot login with its own transport, its own credential, and its own identity. Each adapter owns its transport (gateway WebSocket, webhook listener, long-poll loop, etc.) and is the **only** component in Espur that knows that platform's wire format, mention semantics, and chunking limits.

## Description

**Shape — ports & adapters.**

Adapters are driving + driven: they push inbound events into the bot core through a single channel returned by `Start`, and they accept outbound posts from the core through a synchronous `Post` call. The dependency arrow is one-way: `cmd/espur/main.go` depends on both adapters and core; adapters depend only on the `Event` types defined alongside the interface; the core does not import any concrete adapter package.

**Interface.**

- `Start(ctx) (<-chan Event, error)` — begin listening. Returns the inbound event channel. Caller cancels `ctx` to stop. On clean shutdown the adapter closes the channel after draining.
- `Post(ctx, threadID, body) (platformMessageID, error)` — synchronous post of one logical reply. Adapter performs chunking and any small bounded retry internally. Returns the platform-native id of the **first chunk** so the transcript record correlates.
- `Platform() string` — stable connection identity. Used as the `platform` field in every emitted event and every persisted record, and as the routing key throughout the core (dedup, transcripts, thread working dirs, outbound poster lookup). See "Connection identity" below.
- `Healthy() bool` — cheap, non-blocking, atomic read. True iff transport is currently connected and the last successful heartbeat / inbound is within a platform-appropriate freshness window. Backs the web UI status panel.

**Event types** (sum, single channel).

- `MessageEvent` — normalized inbound message:
  - `Platform`, `ThreadID` (raw, platform-native), `PlatformMessageID` (raw), `Author{ ID, Label }`, `Body` (bot mention stripped, attachments → placeholder tokens like `[image]`), `Mention` (bool — explicit @ or implicit DM), `ReceivedAt`.
- `LifecycleEvent` — adapter-global transport state:
  - `Platform`, `Kind ∈ { Connected, Disconnected, Reconnecting, AuthRevoked }`, `Cause` (free-text for logs), `Attempt` (populated on `Reconnecting`), `At`.

Both variants ride the same channel. Ordering inside the channel reflects the order the adapter observed events.

**Per-adapter responsibilities.**

- **Transport client.** Holds the live connection. Reconnects on transient drops with bounded exponential backoff (1s base, 60s cap, jitter). Exits the start loop on hard auth failure after emitting `AuthRevoked`.
- **Inbound normalizer.** The only place that knows the platform's payload shape. Extracts ids/body/author, performs **mention detection** using the platform's native mechanism (Discord: bot user id in `mentions`; WeChat/iLink: DM-only in v0.6.0, so always true; DMs in general: always true), strips the bot's mention token from `body`, renders attachments to placeholder tokens, distinguishes the bot's own outbound posts from inbound user messages (and drops the former).
- **Outbound poster.** Chunks `body` per the rules in [[reply]] (paragraph → sentence → word → byte; never inside a fenced code block) at the platform's documented per-message limit (Discord 2000 chars, etc.). Posts chunks sequentially. Performs bounded retry on transient send errors (3 attempts, 1s/3s/9s backoff). Honours the platform's own rate-limit headers (e.g. Discord 429 `Retry-After`) once.
- **Webhook verification & replay defense** (webhook-style adapters only). Verify the platform's signature (HMAC, `X-*` headers, etc.) before parsing the payload. Reject unsigned requests. Reject obvious replays via the platform's nonce/timestamp window if one exists; otherwise rely on core dedup as the second line of defense.
- **Lifecycle emitter.** Pushes `LifecycleEvent` records onto the same channel as `MessageEvent`. Required events: `Connected` on first successful handshake; `Reconnecting` per attempt; `Disconnected` with cause on drop / downstream backpressure; `AuthRevoked` (terminal) on hard auth failure.
- **Healthcheck.** Atomic state read; never blocks.

**What adapters do not own** (boundary fixed deliberately):

- Dedup of `(platform, message_id)` — bot core (see [[trigger]]).
- Per-thread queueing, coalescing, the "still thinking" ack — bot core.
- Transcript persistence — bot core (see [[transcript]]).
- Working-dir creation, opencode invocation, vendor selection — bot core.
- Credential storage — secrets layer; the adapter receives decrypted creds only at construction time.
- Encoding `ThreadID` to a filesystem-safe form — bot core (raw IDs cross the boundary; encoding happens core-side).

**Inbound data flow.**

1. Adapter receives a raw platform message.
2. Adapter normalizes → `MessageEvent`.
3. Adapter pushes the event onto the channel synchronously from the transport goroutine. The channel is small-buffered (e.g. 16). If full for >1s, the adapter logs at warn and drops; platform-side webhook retry / gateway redelivery is the recovery path, and core dedup makes recovery idempotent.
4. Bot core's dispatcher reads the channel in a tight loop and routes each `MessageEvent` to [[trigger]]; `LifecycleEvent` records go to a status cache backing `Healthy()` and the web UI.
5. The adapter does not await acknowledgement.

**Outbound data flow.**

1. Bot core finishes an invocation (or has a terminal reply per [[reply]]).
2. Bot core calls `adapter.Post(ctx, threadID, body)`; the call blocks until success, retry-budget exhaustion, or `ctx` cancellation.
3. Core writes the [[transcript]] `kind=bot` record using the returned `platformMessageID` (or `""` on full failure with a `request_id` and `reply_outcome=crash`).
4. Per-thread queue advances.

**Error handling at the boundary.**

- *Inbound — malformed payload / bad signature.* Adapter returns the platform's expected HTTP error, logs, emits no event. Bodies are never logged; ids and sizes may be.
- *Inbound — bot's own echo.* Normalizer drops silently.
- *Inbound — channel backpressure.* Drop with warn log. Repeated backpressure within a short window emits `Disconnected{Cause="downstream backpressure"}` for operator visibility; transport stays up.
- *Transport — transient drop.* Reconnect loop. `Reconnecting` events per attempt. User-invisible.
- *Transport — reconnect budget exhausted.* `Disconnected` (terminal), start loop exits, `Healthy()=false`. Operator action required.
- *Transport — hard auth failure.* `AuthRevoked` (terminal). Distinct from `Disconnected` so the web UI can show "needs reconfigure" vs. "platform unreachable".
- *Outbound — transient.* Bounded retry inside `Post`.
- *Outbound — platform rate-limited.* Honour `Retry-After` once, then fail.
- *Outbound — partial chunk delivery.* Return `(firstChunkID, err)`; core writes one `kind=bot` record as crash. Posted chunks are not deleted — the user already saw them.
- *Outbound — `ctx` cancelled.* Return `ctx.Err()`. Core logs and skips the transcript write.
- *Outbound — thread gone* (channel deleted, permissions revoked). `Post` returns a typed `ErrThreadGone`; core downgrades log level (not an Espur bug) and handles as crash.
- *Core-side — adapter down at reply time.* Core skips `Post`, writes a `kind=system` transcript record `note=adapter-down-reply-dropped` with intended outcome + request id in `meta`, and surfaces the dropped count in the web UI.

**Logging discipline.**

- Adapter logs carry a `platform=<id>` tag and cover transport state, signature failures, retry attempts, drops.
- Core logs cover dedup, queueing, invocation, vendor-pool transitions, transcript writes.
- Request IDs from [[reply]] appear in both adapter and core logs whenever the failure was bot-side; this is the join key for operator triage.
- Message bodies are never logged. Author labels and message ids are.

### Connection identity & multi-connection

A **connection** is a persisted, admin-configured instance of a platform. The admin does not type a free-text name; they select a platform **type** (Discord, WeChat, ...) and supply its credential (Discord: a bot token; WeChat: a QR scan). The system assigns each connection a stable, opaque **connection id** at creation, and the adapter's `Platform()` returns the composite key `kind:id` (e.g. `wechat:7Gx9q2`). That composite is the only identity the rest of Espur sees, and because dedup, transcripts, thread working dirs, and the outbound poster map are all keyed on it, two connections of the same platform are fully isolated with no shared state — there is no shared session, cursor, or client between them.

The single exception is the **migrated legacy connection**: when an existing deployment first boots after this feature lands, the manager seeds one connection per pre-existing env-configured platform using the **bare** platform key (`wechat`, `discord`) as its id, so existing thread history, transcripts, and dedup rows keep resolving. New connections created through the web UI always get a generated id.

A human-readable **label** for the UI is derived from the platform after connect (the Discord bot username, the WeChat bot id) rather than entered by the admin.

### Adapter: WeChat (iLink Bot API)

The WeChat adapter (`internal/adapter/wechat/`) talks to Tencent's **personal-account iLink Bot API** (`https://ilinkai.weixin.qq.com`) via the Go SDK [`github.com/openilink/openilink-sdk-go`](https://github.com/openilink/openilink-sdk-go) (MIT, pinned at v0.6.0). It replaces the earlier Web/Desktop WeChat protocol (`openwechat`), which Tencent actively bans. iLink is the officially sanctioned personal-bot transport, so it is the lower-ban-risk path — but it is still **personal / non-commercial** and opt-in (`ESPUR_WECHAT_ENABLED=1`).

**Transport — pure HTTP/JSON, no gateway socket.**

- **Login.** QR-code scan → `bot_token`. The SDK's `LoginWithQR` fetches a QR and surfaces it through an `OnQRCode(imgContent)` callback. The payload is the **QR image content** (`qrcode_img_content`), *not* a `login.weixin.qq.com/qrcode/<uuid>` URL as in the old protocol. The operator scans it from the WeChat mobile client; the SDK polls scan status (`wait`/`scaned`/`confirmed`/`expired`) until connected, then returns `{Connected, BotToken, BotID, UserID, BaseURL}`.
- **Inbound.** Long-poll (`getUpdates`). The SDK's `Monitor(ctx, handler, opts)` runs the poll loop, auto-retry/backoff, and a server-driven dynamic timeout. Each `WeixinMessage` carries `FromUserID`, optional `GroupID`, `ContextToken`, `MessageType` (user vs. bot), and an `ItemList` of typed content items. `ilink.ExtractText` collapses text + quoted-reply + voice-transcription items into a single body string.
- **Outbound.** Reply-only, DM only. `SendText(ctx, toUserID, text, contextToken)`. The SDK has no persistent socket — `Post` is a single HTTP round-trip per chunk.

**Context-token threading (the key constraint).** Every iLink reply must echo the `context_token` observed on the *inbound* message it answers. But `Adapter.Post(ctx, threadID, body)` carries no token. Resolution: the adapter caches, per `ThreadID`, the latest `context_token` seen inbound. `Post` looks the token up and threads it into the send call. The SDK also keeps its own per-`FromUserID` token cache for `Push`, but the adapter keeps an authoritative cache of its own so the behaviour is testable without the live API.

**Reply-only (no cold open).** Like a Telegram bot, the adapter cannot initiate a conversation with a user that has never messaged it — without a cached `context_token` there is nothing to echo. If `Post` is called for a `ThreadID` with no cached token, it returns `ErrThreadGone`; the core handles that as a downgraded crash outcome (not an Espur bug).

**ThreadID mapping (DM only).** `ThreadID == FromUserID` (the peer's iLink user id). `Mention` is always `true` (implicit DM mention).

**Group chats are unsupported.** Tencent's grayscale ClawBot does not deliver group messages, and the v0.6.0 SDK exposes no group-send helper (`SendText`/`Push` address an individual `ToUserID`; the token cache keys on `FromUserID`). Inbound messages with a populated `GroupID` are dropped defensively, and the adapter never sends to a group. Group support waits on the platform shipping it.

**Self-echo and bot identity.** A QR login provisions a **new bot** (its own `BotID`, an `im.bot`-suffixed id) that the scanning account *owns*; the login `UserID` is that human operator (an `im.wechat`-suffixed id), **not** the bot. `Monitor` may redeliver the bot's own outbound messages; they arrive with `MessageType == MsgTypeBot`, so the normalizer drops any non-user message and replies never loop. It deliberately does **not** filter on `UserID` — that would drop the operator's own legitimate messages to their bot.

**Persistence.** Replaces the old `openwechat` hot-reload blob. The adapter persists `bot_token`, `bot_id`, `user_id`, the connected `base_url`, and the `getUpdates` cursor (`buf`) as one JSON session blob, written through an injected session store rather than a hard-coded path. A web-managed connection backs that store with the encrypted credentials table (scope `connection`), so the secret never lands on disk in plaintext; the standalone `cmd/wechat-login` helper backs it with a `data/wechat-session.json` file (mode 0600, atomic write) for headless first-login. On boot: if a saved `bot_token` exists, the QR step is skipped, the saved `base_url` is restored, and `Monitor` resumes from `InitialBuf` equal to the saved cursor; `OnBufUpdate` persists the cursor as it advances.

**Lifecycle.** `Connected` after a successful login/resume. `AuthRevoked` when the server reports session expiry (`errcode -14`, surfaced via `OnSessionExpired`); on that event the adapter wipes the persisted token so the next boot forces a fresh QR scan. `Disconnected` on an unexpected `Monitor` exit. There is no gateway heartbeat, so `Healthy()` reflects the login/poll state rather than a socket.

## Outcome

Each enabled connection has exactly one running `Adapter` whose `Start`-returned channel is the sole inbound path for that connection and whose `Post` method is the sole outbound path; the connection's `kind:id` identity keeps multiple connections of the same platform fully isolated. All platform-specific knowledge (wire formats, mention semantics, chunking limits, signature schemes, reconnect strategy) is contained inside the adapter package; adding a platform is a matter of adding one package under `internal/adapter/<platform>/` and teaching the connection manager to construct it. Connections are created, enabled, disabled, and deleted at runtime from the web UI and persisted in the database; the connection manager starts and stops adapters to match. Failures inside the adapter never produce a user-visible message of their own — they surface via logs and the web UI status panel; user-visible replies are exclusively the four forms defined in [[reply]].

## Notes

- Decided: inbound channel buffer is 16 and the backpressure-drop threshold is >1s blocked (after which the adapter surfaces a `Disconnected{cause="downstream backpressure"}`). May be tuned after real Discord-load testing.
- TODO(decision): per-platform outbound retry constants (3 attempts, 1s/3s/9s) — confirm before Discord adapter lands.
- TODO(decision): per-platform freshness window for `Healthy()` (Discord gateway heartbeat ack window ≈ 60s; pin per adapter, not as a global constant).
- Known minor gap: if Espur is shutting down between `opencode` success and `Post` completion, the user's trigger is in the dedup table but no reply was sent. On next boot the user's resend would be deduped. Acceptable for v0.1; logged at warn so the operator can see it. Revisit if it bites in practice.
- The set of platforms supported in v0.1 is Discord first, WeChat second. The interface is shaped to absorb Slack / Matrix / Telegram without changes.
- Adapter packages must not import the bot core or the store. They may import the secrets layer only to receive decrypted credentials at construction time; they must not call back into it for live lookups.
