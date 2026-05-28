# Behavior: Secrets

## Condition

Espur stores credentials that an attacker with read access to `data/espur.db` must not be able to recover: IM-platform tokens used by [[adapter]] and vendor credentials (BYO API keys, OAuth refresh/access tokens) used by [[opencode-invoke]] via [[vendor-pool]]. At process start, an operator-provided master key (age identity) is supplied via the `ESPUR_MASTER_KEY` environment variable.

## Description

**At rest.**

- Every credential is stored as a single age-encrypted blob in SQLite, keyed by `(scope, id)` where `scope` is e.g. `vendor`, `adapter`, `oauth`, and `id` identifies the specific credential (vendor id, platform id, etc.).
- The SQLite row also carries non-secret metadata in plaintext columns: created/updated timestamps, a credential `kind` (`byo_key`, `oauth`, `platform_token`, ...), and a short `status` flag (`set` / `expired` / `revoked`).
- The credential **value** itself (api key, OAuth token bundle, platform token) is never present in plaintext columns, log lines, web UI HTML, or process memory dumps for longer than the lifetime of a single use.

**Credential model — one secret, name aliases.**

- A credential row holds exactly **one** secret value (one age blob). It does **not** hold multiple distinct secrets.
- The row also carries one or more env-var **names** (`env_keys`). At use time, [[vendor-pool]] exposes that single secret value under each of those names — they are aliases for the same value (e.g. a provider that accepts either `FOO_API_KEY` or `FOO_KEY`), never a way to carry a second secret such as a separate "key + secret" pair.
- A vendor that genuinely needs two distinct secret values is out of scope for v0.1; if one is ever added, this model gets revisited (a JSON-encoded blob of name→value), and this spec is updated first.

**At boot.**

- `ESPUR_MASTER_KEY` is read once at process start and held as an age identity in process memory. It is never written to disk or logged.
- A self-test runs at boot: pick one known-encrypted blob (if any exist) and attempt decryption.
  - Success → proceed.
  - Failure → abort boot with a clear error naming "master key mismatch" and exit non-zero. Espur does not start in a degraded mode where it can't read its own secrets.
- If `ESPUR_MASTER_KEY` is unset, abort boot with a clear error.
- A blank database (no encrypted blobs yet) is a valid state: the self-test is skipped and the key is held for the first encryption.

**At runtime — read path.**

- A credential is decrypted only **at the moment of use**, by the component that needs it:
  - [[vendor-pool]] decrypts a vendor's credential immediately before constructing the child-process environment in [[opencode-invoke]], passes it via env to the child, and drops the plaintext from its own memory as soon as the child has been spawned.
  - [[adapter]] decrypts its platform token at construction time only (adapters hold a live connection; rotation requires reconstruct).
  - The web UI never decrypts a credential to render it. It can only render `status`, kind, and timestamps.
- Decrypted credentials live in narrowly-scoped variables in the consuming goroutine. They are not pooled, not cached across calls, and not passed through the bot core's queueing or transcript paths.

**At runtime — write path.**

- The only writer is the web UI's credential-save handlers (see [[webui]] and, for OAuth, [[oauth]]).
- A save accepts the plaintext credential over the UI's own connection, encrypts it with the master key, writes the blob + metadata in a single SQLite transaction, and discards the plaintext.
- Saving a new credential for a vendor that is currently `auth_locked` in [[vendor-pool]] returns that vendor to `eligible` as part of the same transaction.
- Saves never echo the credential back to the browser; only the `status=set` badge updates.

**Rotation.**

- Per-credential rotation is just a re-save with the same `(scope, id)` and a new plaintext value.
- Master-key rotation is **out of scope for v0.1** and called out in the deploy doc. There is no built-in re-encrypt-all command yet.

**Logging discipline.**

- Credential values, master-key bytes, and OAuth tokens are never logged at any level.
- Credential ids, kinds, statuses, and timestamps may be logged.
- An accidental log of a credential is treated as a security incident; the spec exists in part to make that easy to spot in review.

**What is and isn't a secret.**

- Secrets: vendor API keys, OAuth access + refresh tokens, IM platform bot tokens, webhook signing secrets, the master key itself.
- Not secrets (stored plaintext is fine): vendor `vendor_id` and `model` strings, vendor priority order, penalty-box state, thread ids, message ids, transcript bodies, `AGENTS.md` contents, dashboard URL.

## Outcome

For every credential Espur holds:

- Its plaintext value exists on disk only inside an age blob that requires `ESPUR_MASTER_KEY` to decrypt.
- It is decrypted only at the moment of use, by the single component that uses it, and never echoed back to any UI or log.
- Its presence and freshness are observable through plaintext metadata (status, timestamps), which is what the operator-facing web UI displays.
- Boot fails fast on a missing or mismatched master key — Espur does not start in a state where it can't honor its own encryption contract.

## Notes

- Decided: age identity format is a raw `AGE-SECRET-KEY-...` X25519 secret-key string in the `ESPUR_MASTER_KEY` env var (simplest, fits the container env model). Passphrase-wrapped key files are not supported in v0.1.
- Decided: the boot self-test is implicitly skipped when the DB has no blobs yet (a blank database is a valid first-run state — see "At boot" above). No separate opt-out flag.
- The choice to keep transcript bodies in plaintext on disk is deliberate — the threat model is "someone exfiltrates `data/`" and transcripts are recoverable from the IM platform itself anyway. Credentials are the asymmetric loss.
- A future hardening step is to extend encryption to transcripts and `AGENTS.md`. Explicitly out of scope for v0.1.
