# OAuth Self-Auth Flow

> Phase 23 Tier 0 (Sesión 6) + Tier 1 (Sesión 7, 2026-10-14)
> Closes the operator-key dependency: users authenticate with
> their own Google or HuggingFace account via OAuth 2.0 + PKCE
> (RFC 6749 + RFC 7636). Tokens persist in the OS keyring.
> Auto-refresh is transparent — no manual rotation needed.

This document has three sections:

1. **Operator Setup** — register OAuth clients + configure env vars.
2. **End-User Flow** — what the user does after the operator
   installs dark-memory (one browser click, then judge works).
3. **Troubleshooting** — common issues + fix-it recipes.

---

## 1. Operator Setup

You need to register OAuth clients with Google and/or
HuggingFace so dark-memory can open the consent screen on
behalf of your users. The minimum is one provider; both are
recommended because they back each other up when one quota is
exhausted.

### 1.1. Required environment variables

| Variable                                  | Required | Default       | Purpose                                            |
|-------------------------------------------|----------|---------------|----------------------------------------------------|
| `DARK_OAUTH_GOOGLE_CLIENT_ID`             | No       | (empty)       | OAuth client id from Google Cloud Console          |
| `DARK_OAUTH_GOOGLE_CLIENT_SECRET`         | No       | (empty)       | OAuth client secret (confidential clients only)    |
| `DARK_OAUTH_HUGGINGFACE_CLIENT_ID`        | No       | (empty)       | OAuth client id from HuggingFace OAuth app         |
| `DARK_OAUTH_HUGGINGFACE_CLIENT_SECRET`    | No       | (empty)       | OAuth client secret (confidential clients only)    |
| `DARK_OAUTH_CALLBACK_PORT`                | No       | `9335`        | Localhost port the callback server listens on      |

If both `*_CLIENT_ID` and `*_CLIENT_SECRET` are empty, the
provider is skipped during `defaultDriftJudgeProvider`. A
deployment with zero `*_CLIENT_ID` set falls back to the
legacy `errMissingField("no project NLI override registered")`
message — backward compatibility preserved.

### 1.2. Registering a Google OAuth client

1. Open the Google Cloud Console: <https://console.cloud.google.com/>
2. Select or create a project for dark-memory.
3. Navigate to **APIs & Services → Credentials**.
4. **Create credentials → OAuth client ID**.
5. Application type: **Web application** (NOT Desktop — we use
   PKCE-protected auth code flow with a localhost callback).
6. **Authorized JavaScript origins**: leave empty.
7. **Authorized redirect URIs**: add exactly:
   ```
   http://localhost:9335/oauth/callback
   ```
   If you customized `DARK_OAUTH_CALLBACK_PORT`, substitute
   that port number.
8. Click **Create**. Copy the **Client ID** → set
   `DARK_OAUTH_GOOGLE_CLIENT_ID`.
9. (Optional, confidential clients only) Copy the **Client
   secret** → set `DARK_OAUTH_GOOGLE_CLIENT_SECRET`. For
   public PKCE-only clients, leave the secret unset.
10. Enable the **Generative Language API** under **APIs &
    Services → Library** for the project. Without this, the
    consent screen will succeed but every Judge call returns
    HTTP 403.

### 1.3. Registering a HuggingFace OAuth app

1. Open <https://huggingface.co/settings/connected-applications/new>
   (log in first if you haven't already).
2. **Application name**: e.g. `dark-memory-mcp (operator)`.
3. **Homepage URL**: your organization URL or
   `https://github.com/dark-agents/dark-memory-mcp`.
4. **Logo URL**: optional.
5. **Scopes**: select at least `openid`, `profile`,
   `inference-api`. Dark-memory uses the inference-api scope
   to call the free Inference API; the others cover identity
   claims.
6. **Redirect URLs**: add exactly:
   ```
   http://localhost:9335/oauth/callback
   ```
   Same caveat as Google — substitute the port if you changed
   it via env.
7. Click **Create application**. Copy the **Client ID** → set
   `DARK_OAUTH_HUGGINGFACE_CLIENT_ID`.
8. (Optional) Copy the **Client secret** for confidential
   clients → set `DARK_OAUTH_HUGGINGFACE_CLIENT_SECRET`.

### 1.4. PKCE-only mode (no client_secret)

Both Google and HuggingFace support **public clients** (PKCE
without `client_secret`). This is the recommended setup for
self-hosted dark-memory deployments because:

- No secret to leak in env vars or process inspection.
- The OAuth provider binds the auth flow to the running
  dark-memory instance via the `code_verifier` challenge
  (RFC 7636 §4.1).

If both `DARK_OAUTH_GOOGLE_CLIENT_SECRET` and
`DARK_OAUTH_HUGGINGFACE_CLIENT_SECRET` are unset, dark-memory
automatically operates in PKCE-only mode for both providers.
Tokens are still encrypted at rest (OS keyring) and rotated
via the provider's refresh_token flow.

### 1.5. Callback port conflicts

If port `9335` is already in use on the host machine, set:

```sh
export DARK_OAUTH_CALLBACK_PORT=9876
```

…and update the **Authorized redirect URIs** in BOTH the
Google and HuggingFace OAuth client registrations to use
`http://localhost:9876/oauth/callback`. The two values must
match — the OAuth provider rejects mismatched URIs with a
`redirect_uri_mismatch` error.

### 1.6. Headless / container deployments

For SSH-only or containerized deployments where the OS
keyring is unavailable, dark-memory falls back to env vars:

```sh
export DARK_OAUTH_GOOGLE_ACCESS_TOKEN="ya29.existing-google-token"
export DARK_OAUTH_GOOGLE_REFRESH_TOKEN="1//existing-refresh"
export DARK_OAUTH_GOOGLE_EXPIRES_AT="2026-12-31T23:59:59Z"
```

The same shape exists for `HUGGINGFACE_*`. Use RFC 3339 for
`EXPIRES_AT`. Keyring mode is preferred; this fallback is for
test runners and CI.

---

## 2. End-User Flow

After the operator installs dark-memory, end users do not
configure anything. They authenticate once per provider, and
drift_judge works thereafter (with auto-refresh).

### 2.1. One-time authentication

Call from your MCP harness:

```jsonc
{
  "tool": "dark_memory_oauth_connect",
  "params": {"provider": "google"}
}
```

Response:

```jsonc
{
  "auth_url": "https://accounts.google.com/o/oauth2/v2/auth?...",
  "state": "abc123...",
  "expires_at": "2026-10-14T20:00:00Z",
  "callback_port": 9335,
  "instructions": "Open this URL in any browser, approve, and return to your harness: ..."
}
```

The user opens `auth_url` in any browser (the same machine
running dark-memory is preferred — the callback redirects to
`http://localhost:9335/oauth/callback`). Approval on the
consent screen takes one click. After approval, the browser
displays a friendly HTML page confirming success.

### 2.2. Verify the connection

```jsonc
{"tool": "dark_memory_oauth_status", "params": {"provider": "google"}}
```

Response when connected:

```jsonc
{
  "provider": "google",
  "display_name": "Google Gemini",
  "status": "connected",
  "account_email": "operator@example.com",
  "expires_at": "2026-10-14T21:30:00Z",
  "free_tier": "60 RPM, 1500 RPD (Google free tier)"
}
```

`account_email` is populated for Google (it comes from the
OpenID userinfo call embedded in the provider's
`fetchAccountEmail` path). HuggingFace does not expose email
through its OAuth flow — `account_email` will be empty for HF
connections.

### 2.3. Judge works automatically

After step 2.1, calling `dark_memory_vibe_publish` triggers
`drift_judge`, which falls back to `defaultDriftJudgeProvider`
because the active project may have no `Project.NLIConfig`
configured. The provider iterates orderedOAuthProviders:

1. **Google** first — uses Gemini OpenAI-compat chat
   completions. Lower latency for short premise/hypothesis
   pairs typical of drift_judge scores.
2. **HuggingFace** second — uses DeBERTa-v3-large-mnli on the
   free Inference API. Higher-precision model, slower.

Whichever provider you authenticated with wins. If you
authenticated with both, Google wins (cheaper, faster).

### 2.4. Auto-refresh (transparent)

OAuth access tokens expire after ~1 hour for Google, ~8 hours
for HuggingFace. Dark-memory's `Router.LoadActiveToken`
automatically detects an expired token via the `IsExpired()`
check (30-second safety margin) and refreshes it using the
provider's refresh_token grant. The new token is persisted to
the OS keyring before the call returns.

This is why users never see "your token expired" prompts. The
refresh happens inside the drift_judge pipeline on each call.

### 2.5. Disconnect / reconnect

To wipe the connection (e.g., for testing or to switch accounts):

```jsonc
{"tool": "dark_memory_oauth_disconnect", "params": {"provider": "google"}}
```

Dark-memory revokes the refresh_token on the provider's side
where supported (Google yes; HuggingFace no public endpoint —
HF tokens simply expire on their own). After disconnect, the
next `drift_judge` call surfaces an actionable
`verdict=needs_human` with the message:

> no judge provider configured. Use `dark_memory_oauth_connect`
> to authenticate with Google Gemini or HuggingFace Hub. See
> docs/oauth-flow.md for setup.

---

## 3. Troubleshooting

### 3.1. Port conflicts

**Symptom**: `oauth: start callback server: bind: address
already in use`.

**Fix**:

```sh
# Find what's holding port 9335
netstat -ano | Select-String ":9335"    # PowerShell
lsof -i :9335                            # bash / WSL

# Pick a free port and set it
export DARK_OAUTH_CALLBACK_PORT=9876

# Update BOTH OAuth clients (Google Cloud Console + HuggingFace)
# to whitelist http://localhost:9876/oauth/callback
```

Restart dark-memory to pick up the new port.

### 3.2. Expired tokens

**Symptom**: `oauth: token expired and no refresh token` in
Error Observatory, or `status: "expired"` from
`dark_memory_oauth_status`.

**Diagnosis**:

```jsonc
{"tool": "dark_memory_oauth_status", "params": {"provider": "google"}}
```

If `status: "expired"`, the auto-refresh failed because:

- The user revoked the refresh grant on the provider side.
- The provider suspended the application (e.g., unusual
  activity detected; common for shared public IPs).
- `DARK_OAUTH_*_ACCESS_TOKEN` env vars are in use and
  `DARK_OAUTH_*_REFRESH_TOKEN` was not set alongside.

**Fix**: Disconnect and reconnect.

```jsonc
{"tool": "dark_memory_oauth_disconnect", "params": {"provider": "google"}}
{"tool": "dark_memory_oauth_connect",   "params": {"provider": "google"}}
```

### 3.3. Refresh failures on Google

**Symptom**: Error Observatory reports
`oauth: refresh token for google: oauth/google: refresh token: ...`.

**Common causes**:

- The user clicked **Deny** on the consent screen
  (uncommon — usually means the user revoked the app at
  <https://myaccount.google.com/permissions>).
- Clock skew between the host machine and the OAuth provider.
  The provider's clock must be within 5 minutes of the host's
  NTP-synced clock. Check `w32tm /query /status` (Windows) or
  `chronyc tracking` (Linux).
- The OAuth client is **in test mode** with only test users
  whitelisted. Google OAuth clients in test mode expire
  refresh_tokens after 7 days; promote to **Production** in
  the Google Cloud Console OAuth consent screen.

**Fix**: Disconnect + reconnect. If the issue persists, check
that the OAuth client is in **Production** status AND the user
account is on the test-users list (test mode) OR the
application is public (production mode).

### 3.4. Browser issues

**Symptom**: User clicks `auth_url` but nothing happens after
approval. Or: user sees a redirect loop.

**Common causes**:

- The user opened `auth_url` on a different machine than the
  one running dark-memory. The callback URL points to
  `http://localhost:9335`, which is the dark-memory host, not
  the user's phone.
- The user's browser blocks the redirect to `http://localhost`
  (some browser extensions reject plain-HTTP localhost). The
  user can manually copy the redirect URL from the address
  bar after approval and paste it into a browser on the
  dark-memory host.

**Fix**: Prefer same-machine browsers. For mobile-only or
remote-browser flows, expose dark-memory's callback port
through a tunnel (e.g., `ngrok http 9335`) and update the OAuth
clients' authorized redirect URIs to the tunnel URL.

### 3.5. Headless / no browser available

**Symptom**: SSH session, container, or terminal server with
no GUI browser.

**Fix**: HuggingFace supports the OAuth Device Code Flow
(RFC 8628). Use:

```jsonc
{"tool": "dark_memory_oauth_connect", "params": {"provider": "huggingface"}}
```

The response includes a `device_code` response shape (when
the operator has set up `DARK_OAUTH_HUGGINGFACE_CLIENT_ID` to
support Device Code Flow). The user visits
<https://huggingface.co/oauth/device> on any device, enters the
short code, and dark-memory's background polling picks up the
tokens automatically.

### 3.6. Audit provenance is "oauth-" but the verdict looks wrong

**Symptom**: drift_judge verdict is `drift_detected` for what
should be `aligned`. The Score.ProviderID is prefixed with
`oauth-` (e.g., `oauth-google-gemini-2.0-flash`).

**Diagnosis**: This is the EXPECTED audit shape for
OAuth-backed verdicts. The ID prefix distinguishes
user-auth from operator-key verdicts. To inspect the inner
provider's id (the model itself, not the OAuth wrapper),
look at the `score.provider_id` field in the artifact row —
it reads `chat-oauth-google` for Gemini or
`hf-oauth-deberta-v3-large-mnli` for HF.

The verdict itself is independent of the ID prefix. If a
verdict is wrong, the model is wrong, not the wiring.

---

## Cross-references

- `internal/orchestration/oauth_resolver.go` — interface
  definition
- `internal/orchestration/oauth_nli_provider.go` — nli.Provider
  adapter
- `cmd/dark-mem-mcp/internal/oauth/router.go` — Router with
  `LoadActiveToken`
- `cmd/dark-mem-mcp/internal/oauth/resolver_adapter.go` —
  concrete implementation bridging cmd ↔ parent module
- `cmd/dark-mem-mcp/internal/oauth/types.go` — Provider,
  TokenSet, ConnectionStatus
- `cmd/dark-mem-mcp/internal/oauth/google.go` — Google OAuth +
  Refresh
- `cmd/dark-mem-mcp/internal/oauth/huggingface.go` — HF OAuth +
  Refresh + Device Code
- `cmd/dark-mem-mcp/legacy_main.go:209-217` — boot wiring
- `internal/orchestration/orchestrator.go` — `OAuthResolver`
  field + `WithOAuthResolver` setter + `defaultDriftJudgeProvider`
- `docs/deploy-mcp-binary.md` — Phase 10 deploy procedure
  (touch-flag reload pattern, applies equally to OAuth-enabled
  binaries)
- Commit `8ed967a` — Sesión 6 Tier 0 foundation (the OAuth
  package itself, types/keystore/pkce/server/router/google/huggingface/tools)
- Commit `<sesion-7-sha>` — Sesión 7 Tier 1 wiring
  (this document; the resolver interface + adapter + provider)
