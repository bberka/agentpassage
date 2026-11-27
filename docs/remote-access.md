# Remote access guide (Tailscale & Cloudflare Tunnel)

By default, `apx` binds strictly to `127.0.0.1:8787` so it is never accidentally exposed on your LAN or public internet.

To reach your `apx` gateway safely from a laptop, phone, VPS, or work machine, keep `apx` bound to `127.0.0.1:8787` and serve it over HTTPS through Tailscale Serve or Cloudflare Tunnel.

## Step 1: Create a dedicated client API key

A `client` key reaches API-key models only. It can call the `/v1` endpoints, but gets `403 Forbidden` from `/admin/*`, which is where the Usage Hub gets its data, and from ACP and CLI models, which it also doesn't see in `/v1/models`.

> **Warning:** `apx keys agents <name> on` lets a client key run your ACP and CLI accounts (Claude, AGY, Codex, Grok). Those agents act on the host with the account's `permission_mode` (default `auto-approve`), and the caller can choose their working folder with `X-Apx-Cwd`. Treat such a key like shell access. Admin keys, including the primary key, always have this access.

Create a separate `client` key per remote device so you can revoke one device without rotating your primary key.

```bash
# API-key models only:
apx keys create laptop --role client

# Optional: restrict a key to specific models:
apx keys create work-ide --role client --models "deepseek/*"

# Optional: let a trusted device run your agent accounts:
apx keys agents work-ide on

# View, disable, or revoke anytime (hot-reloads the running service):
apx keys list
apx keys disable work-ide
apx keys revoke work-ide
```

You can also manage keys inside the TUI: `7 Settings`, then `API Keys`.

## Option A: Tailscale Serve (recommended, private tailnet HTTPS)

Tailscale gives you automatic HTTPS certificates on your private mesh network, with no public exposure and no open firewall ports.

1. Install and sign in to [Tailscale](https://tailscale.com/download) on both the `apx` host and your remote device, and enable HTTPS Certificates in the Tailscale Admin Console (`DNS` tab).
2. Make sure `apx` is running (e.g. `apx service install` or `apx serve`).
3. Proxy `127.0.0.1:8787` onto your tailnet over HTTPS:

```bash
tailscale serve --bg --https=443 http://127.0.0.1:8787
```

4. Check the generated HTTPS URL:

```bash
tailscale serve status
# Example output: https://my-workstation.tail1234.ts.net
```

5. Configure your remote client:
   - **OpenAI-compatible Base URL:** `https://my-workstation.tail1234.ts.net/v1`
   - **Anthropic-compatible Base URL:** `https://my-workstation.tail1234.ts.net`
   - **API Key:** the `apx_...` key from `apx keys create`

To stop exposing it later:
```bash
tailscale serve --https=443 off
```

### Alternative: Direct Tailscale IP (without Tailscale Serve)
If you prefer plain HTTP inside your WireGuard tailnet:
```bash
TS_IP=$(tailscale ip -4)
apx serve --bind "$TS_IP" --port 8787 --allow-remote
```
Then point clients on your tailnet to `http://<TS_IP>:8787/v1`.

## Option B: Cloudflare Tunnel (`cloudflared`, no open ports)

Cloudflare Tunnel exposes `http://127.0.0.1:8787` behind Cloudflare's edge TLS with no port forwarding on your router.

### B1. Quick temporary tunnel (30-second test)

```bash
cloudflared tunnel --url http://127.0.0.1:8787
```

`cloudflared` prints a temporary `https://<random>.trycloudflare.com` URL. Point clients to `https://<random>.trycloudflare.com/v1` with your `client`-role `apx_...` key.

### B2. Permanent custom domain (`https://ai.yourdomain.com/v1`)

1. Install `cloudflared` and authenticate with your Cloudflare account:
   ```bash
   cloudflared tunnel login
   cloudflared tunnel create apx
   cloudflared tunnel route dns apx ai.yourdomain.com
   ```
2. Create `~/.cloudflared/config.yml` (replace `<TUNNEL-UUID>` with the UUID printed by `create`):
   ```yaml
   tunnel: <TUNNEL-UUID>
   credentials-file: /home/<user>/.cloudflared/<TUNNEL-UUID>.json

   ingress:
     - hostname: ai.yourdomain.com
       service: http://127.0.0.1:8787
       originRequest:
         # Long-running streaming responses & reasoning models
         keepAliveTimeout: 90s
         connectTimeout: 15s
     - service: http_status:404
   ```
3. Test and install as an auto-starting service:
   ```bash
   cloudflared tunnel run apx
   # Once verified:
   sudo cloudflared service install
   ```

### Recommended Cloudflare hardening
- Use a `client`-role key (`apx keys create cf-client --role client`). `/admin/*` returns `403` even if someone guesses an admin path.
- Block `/usage` and `/admin/*` at Cloudflare WAF (Custom Rule: if `URI Path starts with /admin` or `URI Path equals /usage`, then Block), keeping the Usage Hub local-only or Tailscale-only.
- Add a Cloudflare Access Service Token or IP allowlist if you want an extra authentication layer before traffic even reaches `apx`.

## Remote client examples

### Environment variables (OpenAI / Anthropic SDKs & CLIs)
```bash
export OPENAI_BASE_URL="https://ai.yourdomain.com/v1"
export OPENAI_API_KEY="apx_<your_client_key>"

export ANTHROPIC_BASE_URL="https://ai.yourdomain.com"
export ANTHROPIC_AUTH_TOKEN="apx_<your_client_key>"
```

### Quick verification with `curl`
```bash
curl -s https://ai.yourdomain.com/v1/models \
  -H "Authorization: Bearer apx_<your_client_key>" | jq .
```

### Quick reference command
Run `apx remote` anytime (or open `7 Settings`, then `Remote Access`, in the TUI) to print and copy commands pre-filled with your active `apx` port.
