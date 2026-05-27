# Behavior: OAuthCredentialFlow

## Condition

The operator wants to add or refresh credentials for a vendor that uses OAuth rather than a BYO API key — currently ChatGPT subscription and Claude subscription. The operator interacts with the [[webui]] vendor row and clicks **Connect**.

## Description

**Initiate.**

- The operator clicks **Connect** on a vendor row whose `kind` is `oauth`.
- The web UI generates a fresh random `state` token, persists `(state, vendor_id, created_at)` in SQLite with a short TTL (e.g. 10 minutes), and redirects the operator's browser to the vendor's documented authorize URL with `state` and Espur's callback URL.
- The callback URL points at this same UI's port at a per-vendor path `/oauth/<vendor_id>/callback`. The deploy doc requires the reverse proxy to exempt `/oauth/*` from external auth so the IdP can reach it. Espur itself enforces no auth on the callback path beyond `state` validation.

**Callback.**

- The IdP redirects back with `code` and `state`.
- The callback handler:
  - Looks up `state` in the pending-state table. Missing, expired, or mismatched `vendor_id` → reject with a clear error page; no token exchange happens.
  - Exchanges `code` for the vendor's token bundle (access token, refresh token, expiry) using the vendor's documented token endpoint and Espur's client credentials for that vendor.
  - Writes the resulting token bundle through the [[secrets]] save path — encrypted blob, `kind=oauth`, `status=set`, expiry timestamp in plaintext metadata.
  - Deletes the pending-state row.
  - If the vendor was previously `auth_locked` in [[vendor-pool]], clears that state in the same transaction.
  - Renders a success page that links back to the vendors list.
- A failed token exchange leaves the previous credential (if any) untouched, deletes the pending-state row, and renders an error page with the IdP's error short-string. No partial state is persisted.

**Refresh.**

- Token refresh is **lazy and out-of-band of triggers**.
- Whenever a vendor is selected by [[vendor-pool]] for an opencode invocation, the secrets layer checks `expires_at` on the OAuth bundle:
  - If the access token is valid (with a small skew, e.g. 60s) → use it as-is.
  - If it is expired or near-expired → use the refresh token to obtain a new access token, persist the rotated bundle through the [[secrets]] write path (same `(scope, id)`, new value, new expiry), and proceed with the invocation using the fresh access token.
- Refresh failures (refresh token expired, revoked, IdP unreachable) are classified as **auth failures** and propagate exactly like an HTTP 401 from the vendor: the vendor enters `auth_locked` per [[vendor-pool]], and the trigger falls through to the next eligible vendor.

**Revocation.**

- Revocation can be observed in two ways:
  - The vendor's API rejects an access token with 401 / 403 → handled by [[vendor-pool]]'s auth-class classification, vendor goes `auth_locked`.
  - The operator clicks **Disconnect** in the web UI (if exposed) → the credential row's `status` is set to `revoked`, the encrypted blob is deleted, and the vendor enters `auth_locked`.
- Either way, the vendor stays `auth_locked` until a successful new **Connect** flow completes for it.

**Concurrency.**

- A second **Connect** initiated for a vendor while a pending `state` already exists is allowed; the older `state` is invalidated when the new one is written. The IdP redirect for the older state will be rejected by the callback handler.
- Token refresh attempts for the same vendor must not race: refresh is serialized per-vendor by holding a short-lived lock keyed on `(scope, id)`. A second invocation arriving during a refresh waits for the in-flight refresh to finish and then reads the rotated bundle.

**What the operator sees.**

- Connect → IdP login → success or failure page → vendor row shows `status=set` and the new expiry. No tokens are ever shown.
- A `auth_locked` vendor that recovers via successful Connect immediately returns to `eligible` (next trigger will try it).
- A refresh-failure-induced `auth_locked` shows on the vendor row with reason "OAuth refresh failed — reconnect".

## Outcome

For every vendor in the pool whose kind is `oauth`:

- The web UI offers a Connect flow that ends with either a usable token bundle stored encrypted at rest, or no change to existing state plus a visible error.
- The token bundle is refreshed transparently and atomically on use; the operator never has to think about access-token TTLs.
- Refresh-token or revocation failures cleanly downgrade the vendor to `auth_locked` so [[vendor-pool]] skips it until a Connect re-runs.
- The `state` parameter is validated on every callback; unsolicited callbacks cannot mutate state.

## Notes

- TODO(decision): pending-state TTL. Suggest 10 minutes; confirm.
- TODO(decision): per-vendor client credentials (the OAuth `client_id` / `client_secret` Espur uses to talk to ChatGPT's and Claude's IdPs) — where do these come from? They are themselves secrets and likely shipped per-deploy. Suggest: compiled-in defaults if the vendor allows public clients, otherwise env vars (`ESPUR_OAUTH_<VENDOR>_CLIENT_ID`/`_SECRET`); confirm once the vendor docs are checked.
- TODO(decision): expose **Disconnect** in v0.1 webui or leave revocation to "edit credentials → save empty"? Suggest explicit Disconnect button; confirm.
- The two OAuth vendors (ChatGPT subscription, Claude subscription) ride opencode's own auth-file mechanism downstream. Espur's job ends at "write a fresh access token where opencode expects it before launching the child"; details of that handoff are an implementation concern for [[opencode-invoke]] and not specified here.
- Refresh failure is treated as auth failure (permanent lock) rather than rate-limit failure (transient cooldown) — confirmed by [[vendor-pool]]'s classification rules.
