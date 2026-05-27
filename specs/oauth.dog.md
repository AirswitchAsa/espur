# Behavior: OAuthCredentialFlow

## Condition

The operator wants to add or refresh credentials for a vendor that uses OAuth rather than a BYO API key — currently any provider opencode itself supports OAuth for (e.g. Anthropic Claude subscription, OpenAI / ChatGPT subscription, GitHub Copilot, and whatever else `opencode auth login` learns to handle).

## Description

**Delegation model.**

Espur does **not** own the OAuth flow. It delegates entirely to the bundled `opencode` CLI's `opencode auth login <provider>` command, which already implements PKCE / device-code / token-refresh flows per provider and persists the resulting bundle to opencode's auth file. Espur's job is to make sure:

1. The auth file lives at a path that survives container restarts and is shared between the operator's one-shot `opencode auth login` invocation and every `opencode run` child Espur spawns.
2. The vendor row in [[vendor-pool]] is wired with `cred_kind=oauth` and a matching `model` whose provider prefix lines up with the provider key in the auth file. No encrypted blob is stored for OAuth vendors; the credential lives in the opencode auth file.

**Auth-file location.**

- Opencode resolves its auth file via `$XDG_DATA_HOME/opencode/auth.json`, falling back to `$HOME/.local/share/opencode/auth.json`.
- Espur sets `XDG_DATA_HOME` at boot to `$ESPUR_DATA_DIR/xdg-data` if unset, and exports it into every opencode child (run + export). This pins the auth file to a known, persistent, container-survivable path.
- The Dockerfile sets the same default so `docker exec` invocations inherit it.

**Authorising a provider.**

The operator runs, inside the container or local shell:

```
opencode auth login <provider>
```

This is a one-time interactive flow opencode owns end-to-end: it prints a URL, the operator authorises in a browser, opencode receives the token bundle and writes it to its auth file. Espur is not in the loop.

For a container deploy this becomes:

```
docker exec -it espur opencode auth login <provider>
```

For local dev (with `XDG_DATA_HOME` pointing into the dev data dir), the same command from the operator's shell.

**Status surface in the web UI.**

- The `/oauth` page reads the auth file (read-only, parses JSON) and lists each configured provider, its `type` (`api` / `oauth` / etc.), and whether a credential value is present. Token bytes are never displayed.
- The page includes the exact `opencode auth login` and `docker exec` commands tailored to the running deploy's `XDG_DATA_HOME`. This is the entire "Connect" UX — no in-UI redirect, no callback path, no PKCE handling in Espur's HTTP server.

**Vendor pool integration.**

- A vendor row with `cred_kind=oauth` does not need an `env_keys` entry and does not get its credentials decrypted from Espur's secrets vault before invocation. Espur passes no provider-specific env vars to the child; opencode reads the auth file itself.
- Penalty-box semantics are unchanged: if an OAuth credential is missing or expired and the provider returns 401/403, [[vendor-pool]] classifies that as a `ClassAuth` failure and the vendor enters `auth_locked`. The fix is the operator re-running `opencode auth login`. Clearing the penalty via the web UI without re-auth will simply re-lock on the next attempt.

**Refresh.**

- Opencode owns refresh. When its CLI exchanges an expired access token for a new one, the rotated bundle is written back to the same auth file. Espur sees no transitions — the next child invocation picks up the rotated value because it reads the same file.
- A refresh that fails (e.g. refresh token revoked) surfaces to Espur as an upstream 401/403 on the next invocation; that flows through the auth-class penalty path.

**Revocation.**

- The operator removes a credential by editing the auth file or by running an opencode CLI command that does the same (e.g. `opencode auth logout <provider>`, when supported). The web UI's reload of `/oauth` reflects the change.
- Espur does not expose its own Disconnect button in v0.1; revocation lives entirely in the opencode CLI surface.

**Concurrency.**

- A second `opencode auth login` invocation for the same provider overwrites the previous bundle atomically (opencode's file write semantics, not Espur's concern). Espur's `opencode run` children may race against an in-progress login: opencode is responsible for not corrupting its own file. Espur reads it lazily on `/oauth` requests; a stale read is fine — the next refresh will fix it.

**What the operator sees.**

- `/oauth` page → list of providers and their state.
- After `opencode auth login`, the page (reloaded) shows the new entry with `has_key=yes`.
- Vendor rows that reference a provider with a valid OAuth credential leave `auth_locked` on the next successful invocation, exactly like a re-keyed BYO vendor.

## Outcome

For every vendor in the pool whose kind is `oauth`:

- A working credential is the result of an `opencode auth login` run by the operator, persisted in `$XDG_DATA_HOME/opencode/auth.json`.
- Espur's child invocations read that file at run time; no Espur-side state needs to track tokens or expiries.
- Refresh and revocation are handled by opencode; Espur observes only the eventual auth outcomes via the penalty-box classification.
- The web UI surfaces auth status read-only and instructs the operator on how to remediate.

## Notes

- The original spec described Espur owning the OAuth flow end-to-end (state token, callback handler, refresh-on-use). That approach was rejected for v0.1 because it requires per-provider reverse-engineering of client IDs / endpoints that opencode already has. Reverting to delegation eliminates a major code surface and a security-sensitive callback path on the admin port.
- A future spec extension can claw back the flow if Espur ever needs to support a provider opencode does not, or if the UX of "shell into the container to authorise" becomes untenable. The vendor-pool contract does not need to change in that case — only this spec and the `/oauth` handler.
- The auth file is treated as an opaque blob outside of presence-checks for the UI. Espur never logs its contents and never copies the file out of its on-disk location.
