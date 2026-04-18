# AI Gateway

OpenAI-compatible proxy that routes requests across multiple free-tier AI providers (Google, Groq, Mistral, Cerebras, NVIDIA) with automatic rate-limit management, tier-based fallback, and per-account proxy isolation.

## Usage

All endpoints are OpenAI-compatible. Point any OpenAI SDK or HTTP client at `http://localhost:8080/v1`.

### Chat completion

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_GATEWAY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### Force a specific provider

Add `x_provider` to bypass automatic routing and force a specific provider:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_GATEWAY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta/llama-3.3-70b-instruct",
    "x_provider": "nvidia",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

When `x_provider` is set:
- Only accounts from that provider are considered
- Tier fallback to other providers is disabled
- Retry still works (waits and retries on the same provider)

Valid provider names: `google`, `groq`, `mistral`, `cerebras`, `nvidia`

### Explicit fallback models

Use `x_fallback_models` to specify an ordered list of models to try if the primary model fails, before (or instead of) automatic tier fallback:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_GATEWAY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini",
    "x_fallback_models": ["mistral-small-latest", "llama-3.3-70b-versatile"],
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### Disable automatic fallback

Set `x_no_fallback: true` to prevent the gateway from automatically trying other tier-equivalent models. Only the requested model (and any explicit `x_fallback_models`) will be attempted:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_GATEWAY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini",
    "x_no_fallback": true,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

Combining both gives you full control over the fallback chain with no surprises:

```json
{
  "model": "gemini",
  "x_fallback_models": ["gemma-3-12b-it"],
  "x_no_fallback": true
}
```

| Parameter | Type | Description |
|---|---|---|
| `x_provider` | string | Force a specific provider; disables all fallback |
| `x_fallback_models` | string[] | Ordered list of models to try if primary fails |
| `x_no_fallback` | bool | Skip automatic tier fallback (explicit list still applies) |



### List models

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer YOUR_GATEWAY_TOKEN"
```

### Model aliases

Use short names instead of full model IDs:

| Alias | Resolves to |
|---|---|
| `gemini` | `gemini-2.5-flash` |
| `gemini-lite` | `gemini-2.5-flash-lite` |
| `llama-70b` | `llama-3.3-70b-versatile` |
| `llama-8b` | `llama-3.1-8b-instant` |
| `mistral` | `mistral-large-latest` |
| `codestral` | `codestral-latest` |
| `nemotron` | `nvidia/llama-3.1-nemotron-70b-instruct` |

### Response metadata

Every response includes `x_gateway` with routing info:

```json
{
  "x_gateway": {
    "provider": "google",
    "account": "GEMINI_API_KEY_1",
    "fallback": false,
    "original_model": "llama-8b"
  }
}
```

### Dashboard

Live monitoring at `http://localhost:8080/dashboard` — prompts for your auth token on first visit, stores it in an HttpOnly cookie (30 days).

---

## Setup

### 1. Environment

Copy `.env.example` to `.env` and fill in your API keys:

```env
GATEWAY_PORT=8080
GATEWAY_AUTH_TOKEN=your-secret-token
GATEWAY_REQUEST_TIMEOUT=60s

# Telegram alerts (optional)
TELEGRAM_BOT_TOKEN=bot-token
TELEGRAM_CHAT_ID=chat-id

# Proxies (only if proxy.type = webshare)
WEBSHARE_API_KEY=key

# Provider API keys (at least one per provider you want)
GEMINI_API_KEY_1=AIza...
GEMINI_API_KEY_2=AIza...
GEMINI_API_KEY_3=AIza...
GROQ_API_KEY_1=gsk_...
GROQ_API_KEY_2=gsk_...
MISTRAL_API_KEY_1=key
CEREBRAS_API_KEY_1=key
NVIDIA_API_KEY_1=nvapi-...
```

Accounts with empty/missing keys are silently skipped. Providers with zero valid accounts are disabled.

### 2. Providers config

Copy `providers.yaml.example` to `providers.yaml` and edit to match your accounts and desired models:

```bash
cp providers.yaml.example providers.yaml
```

`providers.yaml` is git-ignored — your active config with real account counts and limits stays local.


### 3. Run

```bash
go run .
```

### 4. CLI management

The gateway binary doubles as a management CLI. The gateway must be running for these commands to work.

```bash
# List all accounts with status, request counts, and cooldown
./ai-gateway accounts

# Re-enable a disabled account (e.g. one that was auto-disabled for an expired key)
./ai-gateway enable google/GEMINI_API_KEY_1

# Manually disable an account
./ai-gateway disable groq/GROQ_API_KEY_1
```

Or via npm shortcuts (same thing):

```bash
npm run accounts
npm run enable  -- google/GEMINI_API_KEY_1
npm run disable -- groq/GROQ_API_KEY_1
```

Example output of `accounts`:

```
Account                  Status         Requests  Errors  Cooldown
────────────────────────────────────────────────────────────────────
google/GEMINI_API_KEY_1  ✅ ok               142       0  —
google/GEMINI_API_KEY_2  🔴 disabled           5       3  —
groq/GROQ_API_KEY_1      🟡 cooldown           18       1  240s
```

> **Note:** Disabled state survives restarts — it's persisted to `gateway_state.json`.
> Re-enable with `./ai-gateway enable <account>` or set `"disabled": false` in the JSON manually.

### 5. Sync providers (optional)

Check which models each provider has added or removed since you last updated `providers.yaml`:

```bash
go run . --sync
```

Fetches the live model list from each provider's API (using the same proxies as the gateway) and shows:

- **Red ✗** — model is in `providers.yaml` but no longer returned by the provider (safe to remove)
- **Green ✚** — model is available from the provider but not yet in `providers.yaml` (review & add if useful)

For OpenRouter, only **free models** (zero cost) are shown. To enable the OpenRouter check, add your key to `.env`:

```env
OPENROUTER_API_KEY=sk-or-...
```

No files are modified — output is informational only. Update `providers.yaml` manually based on what you want to keep.

### 6. Deployment (pm2)

A `deploy.sh` script is included for server deployments with [pm2](https://pm2.keymetrics.io/):

```bash
# First time: install pm2
npm install -g pm2
pm2 startup   # follow the printed command to enable auto-start on reboot

# Deploy (pulls git, recompiles if Go files changed, reloads pm2)
npm run deploy
# or directly:
bash deploy.sh
```

The script:
1. Pulls `origin master`
2. Runs `go mod download` only if `go.mod`/`go.sum` changed
3. Recompiles only if `.go` files changed (skips if binary is up to date)
4. `pm2 reload` if the process exists, otherwise `pm2 start`


### 7. Configuration

All provider config lives in `providers.yaml`. Structure:

```yaml
gateway:
  port: 8080
  auth_token_env: "GATEWAY_AUTH_TOKEN"    # env var name, not the value

proxy:
  type: webshare    # "webshare", "static", or "none"
  api_key_env: "WEBSHARE_API_KEY"

providers:
  - name: google
    type: gemini       # "gemini" (needs translation) or "openai" (pass-through)
    base_url: "https://generativelanguage.googleapis.com/v1beta"
    models:
      - id: gemini-2.5-flash
        tier: 2                           # used for fallback ordering
        limits: { rpm: 10, rpd: 500 }
    accounts:
      - api_key_env: "GEMINI_API_KEY_1"   # env var name
        proxy: true                       # route through webshare proxy
```

---

## How routing works

1. **Alias resolution** — short name → canonical model ID
2. **Proactive limit check** — skip accounts that are near their rate limit (RPM/RPD/TPM/TPD)
3. **Round-robin** — among eligible accounts, pick the least-recently-used
4. **Send request** — if 429, record cooldown and try next account
5. **Tier fallback** — if all accounts for the requested model are exhausted, try models at the same tier or higher (disabled when `x_provider` is set)
6. **Retry** — wait `retry_delay` (default 5s) and retry once
7. **Exhaustion alert** — if everything fails, send Telegram alert

### Tiers & Fallback Strategy

Models in `providers.yaml` are assigned a numeric `tier` (typically 1 to 3) which represents their relative capability and size. This system protects the quality of your application's outputs.

*   **Tier 1**: Small, fast models (e.g., Llama 3.1 8B, Gemini Lite, Mistral Small).
*   **Tier 2**: Balanced, mid-sized models (e.g., Mistral Medium, Codestral, Gemma 27B).
*   **Tier 3**: Highly capable, large models (e.g., Llama 3.3 70B, Mistral Large, Gemini Flash).

**How it works**: If you request a **Tier 3** model, but all of its API accounts hit rate limits, the gateway will automatically try to route the request to another available **Tier 3** model from a different provider. If you request a **Tier 1** model and it's constrained, the gateway can fall back to another **Tier 1**, or upgrade you to a **Tier 2/3** model.

**Crucially, the gateway will never automatically downgrade your request to a lower tier.** If your app requires a Tier 3 model's reasoning capabilities, it won't silently start making mistakes with a Tier 1 model when limits are reached.

> **Tip — paid last-resort models**: Assign a very high tier (e.g., `tier: 99`) to any paid model you only want used when all free alternatives are exhausted. Since fallback sorts by tier ascending, a tier-99 model is always tried last, regardless of what tier the original request was for. Example use case: adding an OpenRouter paid model as an absolute safety net so traffic is never dropped.
>
> ```yaml
> - id: openai/gpt-oss-120b
>   tier: 99          # only reached after every free model fails
>   # no limits — paid model, gateway will react to 429s as they come
> ```


### Provider types

| Type | API format | Providers |
|---|---|---|
| `openai` | OpenAI-compatible pass-through | Groq, Mistral, Cerebras, NVIDIA |
| `gemini` | Google AI Studio format (auto-translated) | Google |

### Proxy isolation

Accounts with `proxy: true` get routed through a dedicated [Webshare](https://www.webshare.io/?referral_code=r5ah58acc1n1) SOCKS5 proxy. Each API key gets a sticky proxy IP (tracked in `ip_mappings.json`). This prevents rate-limit sharing across keys that would otherwise resolve to the same IP.

---

## Providers & free-tier limits

| Provider | Models | RPM | RPD | Notes |
|---|---|---|---|---|
| [Google](https://ai.google.dev/pricing) | gemini-2.5-flash, flash-lite, gemma-3-27b | 10-15 | 500-1500 | Proxy recommended (IP-based limits) |
| [Groq](https://console.groq.com/docs/rate-limits) | llama-3.3-70b, llama-3.1-8b, llama-4-scout, mixtral-8x7b | 30 | 1000-14400 | Token limits vary |
| [Mistral](https://docs.mistral.ai/getting-started/models/) | mistral-large, mistral-small, codestral | 60 | — | 1B tokens/month, 1 RPS |
| [Cerebras](https://inference-docs.cerebras.ai/api-reference/rate-limits) | llama-3.3-70b, llama-3.1-8b, gpt-oss-120b | 30 | — | 1M TPD |
| [NVIDIA](https://build.nvidia.com/explore/discover) | meta/llama-3.3-70b, meta/llama-3.1-8b, nemotron-70b | 40 | — | Free dev tier, no proxy needed |
| [OpenRouter](https://openrouter.ai/docs#rate-limits) | *Various free models (`:free` alias or 0 cost)* | 20 | 50-1000 | [List of free models](https://openrouter.ai/models?max_price=0) |


---

## Telegram alerts

Alerts are sent when:
- All providers exhausted for a model
- Account capacity exceeds 80%
- 5+ consecutive 429s on an account

Configure in `providers.yaml`:
```yaml
telegram:
  bot_token_env: "TELEGRAM_BOT_TOKEN"
  chat_id_env: "TELEGRAM_CHAT_ID"
  alert_cooldown: 5m
```

---

## Disclaimer

This project is provided for educational and personal development purposes only. It is your responsibility to review and comply with each AI provider's terms of service and acceptable use policies. The authors are not responsible for any misuse, account suspensions, or violations arising from the use of this software. Use at your own risk.
