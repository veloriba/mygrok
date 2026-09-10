<p align="center">
  <img src="https://img.shields.io/github/license/veloriba/mygrok?style=for-the-badge&color=blue" />
  <img src="https://img.shields.io/github/go-mod/go-version/veloriba/mygrok?style=for-the-badge&color=00ADD8" />
  <img src="https://img.shields.io/github/stars/veloriba/mygrok?style=for-the-badge&color=gold" />
</p>

# mygrok

A minimal, high-performance ngrok clone for personal use. Built with Go and powered by [yamux](https://github.com/hashicorp/yamux) for robust connection multiplexing and [httputil](https://pkg.go.dev/net/http/httputil) for reliable reverse proxying with **full WebSocket support**.

Expose your local development servers (Next.js, React, etc.) to the internet through your own VPS with a single command. Support for custom subdomains and automated TUI dashboard.

---

## ✨ Features

- **Personal Infrastructure**: Total control over your data and domain.
- **Multiplexed**: Multiple concurrent HTTP requests over a single TCP connection.
- **WebSocket & HMR Support**: Works perfectly with Next.js, Webpack HMR, and real-time apps.
- **Streaming (SSE) Friendly**: Long-lived streamed responses (e.g. LLM token streams) are never cut off by write timeouts.
- **Wildcard SSL Support**: Full HTTPS support using Let's Encrypt wildcard certificates.
- **TUI Dashboard**: Real-time request logging, status monitoring, and URL display.
- **Resilient**: Clients auto-reconnect with exponential backoff + jitter; the server tears tunnels down deterministically so a wedged session can't leak.
- **Observability**: Loopback-only `/_mygrok/stats` endpoint exposing live tunnel, in-flight, and runtime metrics.
- **Structured Logging**: `log/slog` with levels and fields (`--log-level`, `--log-format json`) on both client and server.
- **Zero Dependencies**: Single binary for client and server.
- **Docker-First**: Minimal `scratch`-based image (linux/amd64 + linux/arm64) with a Compose template — one image runs on every client host (Linux, macOS, Windows via Docker Desktop).

## 🚀 Quick Start

### 1. Server Setup (Ubuntu VPS)

1.  **Build and Install**:
    Initialize your `config.mk` (copy from `config.mk.example`) and run:
    ```bash
    make server-install
    ```
    This will build the binary, deploy it to your VPS, and set up a systemd service.

2.  **Nginx & SSL Configuration**:
    Follow the instructions in the [Wiki/SSL Section] to obtain a wildcard certificate using `certbot` and configure Nginx to proxy traffic to port 8080.

### 2. Client Usage (Docker Compose, recommended)

The recommended way to run client tunnels is Docker Compose: every tunnel is a container, so `docker ps` shows all proxies at a glance, `docker logs -f <name>` streams logs, and crashed tunnels restart automatically. One `linux/amd64`/`linux/arm64` image covers Linux, macOS, and Windows (Docker Desktop) hosts — no per-OS binaries or WSL hacks needed.

1.  **Configure environment**:
    ```bash
    cp deploy/docker/.env.example deploy/docker/.env
    # edit deploy/docker/.env: MYGROK_SERVER, MYGROK_TOKEN
    ```
2.  **Start tunnels** (the minimal `scratch`-based image is built on first run):
    ```bash
    make docker-up
    ```
    Add more tunnels by adding services to `deploy/docker/docker-compose.client.yml` (one service per tunnel, see the commented `ssh` example).
3.  **Day-to-day**:
    ```bash
    make docker-status          # all tunnels at a glance
    make docker-logs SERVICE=api
    make docker-down
    ```
    On air-gapped hosts, build once and transfer: `make docker-save` → `docker load -i mygrok-<ver>-<arch>.tar`.

### 3. Client Usage (bare binary)

1.  **Configure environment**:
    ```bash
    export MYGROK_SERVER="yourdomain.com:7000"
    export MYGROK_TOKEN="your-secret-token"
    ```
2.  **Expose a local port**:
    ```bash
    ./bin/mygrok http 3000 my-app
    ```
    Or using Makefile:
    ```bash
    make run PORT=3000 SUB=my-app
    ```

## 🛠 Makefile Commands

- `make build`: Build both client and server binaries.
- `make test`: Run integration and unit tests.
- `make server-install`: Deploy server to VPS.
- `make server-status`: Check remote service status.
- `make cert-renew`: Trigger manual wildcard certificate renewal.
- `make run PORT=3000 SUB=name`: Launch client using `config.mk` settings.
- `make docker-build`: Build the Docker image (current platform, tag from `VERSION`).
- `make docker-buildx DOCKER_REGISTRY=ghcr.io/veloriba`: Build linux/amd64+arm64 and push.
- `make docker-save`: Export image tarball for offline transfer.
- `make docker-up` / `docker-down` / `docker-status` / `docker-logs SERVICE=<name>`: Manage client tunnels from `deploy/docker/docker-compose.client.yml`.

## 🛠 Configuration

### Client

| Flag | Environment Variable | Description |
| --- | --- | --- |
| `--server` | `MYGROK_SERVER` | **Required**. Server address (e.g. `yourdomain.com:7000`) |
| `--token` | `MYGROK_TOKEN` | **Required**. Authentication secret token |
| `--config` | — | Path to a profile `config.json` (default: `config.json` next to the binary) |
| `--public-port` | — | Requested public port for `tcp`/`udp` tunnels (`0` = auto-assign) |
| `--no-tui` | — | Run headless; logs lifecycle events to stderr (for systemd/journal) |
| `-v`, `--verbose` | — | Debug logging (equivalent to `--log-level debug`) |
| `--log-level` | `MYGROK_LOG_LEVEL` | `debug` \| `info` (default) \| `warn` \| `error` |
| `--log-format` | `MYGROK_LOG_FORMAT` | `text` (default) \| `json` |
| — | `MYGROK_RECONNECT_SEC` | Base reconnect interval in seconds (default `5`; backoff grows from here) |
| `--flush-interval` | `MYGROK_FLUSH_INTERVAL` | How often the reverse proxy flushes the response to the tunnel: `-1` (default) after every write (low-latency streaming), `0` only on completion, or a duration (e.g. `100ms`) for periodic flushes |
| `--local-host` | `MYGROK_LOCAL_HOST` | Local host to forward to (default `127.0.0.1`; use a container/non-loopback IP as needed) |
| `--insecure` | `MYGROK_INSECURE` | Skip TLS certificate verification for the local upstream (for self-signed certs, e.g. the `https` subcommand) |
| `--set-header` | — | Inject a static `Key: Value` header into every proxied request (repeatable for multiple headers) |
| `--upstream-timeout` | `MYGROK_UPSTREAM_TIMEOUT` | Timeout for receiving response headers from the local upstream (default `0` = no timeout) |

### Server

| Flag | Environment Variable | Description |
| --- | --- | --- |
| `-token` | `MYGROK_TOKEN` | **Required**. Authentication secret token |
| `-domain` | — | **Required**. Base domain for tunnels (e.g. `example.com`) |
| `-control` | — | Control listener address (default `:7000`) |
| `-http` | — | Public HTTP proxy listener (default `:8080`, fronted by Nginx) |
| `-admin` | — | Loopback admin/stats listener (default `127.0.0.1:7001`; `off` to disable) |
| `-admin-token` | `MYGROK_ADMIN_TOKEN` | Optional token granting **non-loopback** access to `/stats` |
| `-port-base` / `-port-count` | — | Auto-assign port range for `tcp`/`udp` tunnels (defaults `20000` / `100`) |
| `-v` | — | Debug logging |
| `-log-level` | `MYGROK_LOG_LEVEL` | `debug` \| `info` (default) \| `warn` \| `error` |
| `-log-format` | `MYGROK_LOG_FORMAT` | `text` (default) \| `json` |

## 🪵 Logging

Both binaries use [`log/slog`](https://pkg.go.dev/log/slog) with a text handler by default (journald-friendly, single timestamp per line). Example server lines:

```text
INFO  control server listening addr=:7000
INFO  HTTP proxy listening addr=:8080
INFO  admin listener listening addr=127.0.0.1:7001 token_auth=false
INFO  tunnel established conn=c1 remote=203.0.113.7:53112 subdomain=my-app proto=http url=http://my-app.example.com
WARN  subdomain takeover, closing old session conn=c9 ... old_conn=c1 old_age=4h12m
INFO  tunnel closed conn=c9 subdomain=my-app served=18234 duration=31m2s
```

Every tunnel logs a correlation `conn` id plus the client `remote` address, so a full connect → serve → disconnect lifecycle can be traced in one `journalctl` query. Use `--log-format json` to ship structured logs to an aggregator, and `-v` when diagnosing.

## 📊 Observability

The server exposes a **loopback-only** admin listener (default `127.0.0.1:7001`), separate from the public `:8080` front-end so metrics are never reachable over the internet:

```bash
curl -s http://127.0.0.1:7001/stats | jq
curl -s http://127.0.0.1:7001/healthz
```

`/stats` returns live operational state:

```json
{
  "now": "2026-09-06T11:00:00Z",
  "version": "0.2.0",
  "uptime": "6h23m10s",
  "tunnels": 2,
  "inflight_requests": 0,
  "tunnel_list": [
    { "subdomain": "my-app", "protocol": "http", "conn": "c9", "remote": "10.0.0.204:53112", "since": "2026-09-06T05:36:50Z", "served": 18234 },
    { "subdomain": "ssh", "protocol": "tcp", "port": 20002, "conn": "c1", "remote": "10.0.0.204:53110", "since": "2026-09-06T05:36:50Z", "served": 42 }
  ],
  "runtime": { "goroutines": 63, "num_cpu": 4, "heap_alloc_mb": 8.1, "sys_mb": 42.0, "num_gc": 210 },
  "addrs": { "control": ":7000", "http": ":8080", "admin": "127.0.0.1:7001" }
}
```

> **Why it matters:** a rising `inflight_requests` that never returns to 0, or a climbing `goroutines` count, is the early fingerprint of a stuck data path — exactly the class of failure that used to require `ss`/`ps`/`/proc` spelunking to spot. To expose the endpoint beyond localhost, set `-admin-token` (then send `X-Mygrok-Admin-Token: <token>`); without a token, only loopback callers are served.

## 📈 Resource usage

Rough figures measured on real traffic (an idle server fronting 6 tunnels):

| Component | Resident (RSS) | Notes |
| --- | --- | --- |
| `mygrok-server` | ~9 MB | Single static Go binary, ~38 goroutines at rest. Grows by a few MB under concurrent streams, then frees on idle. |
| `mygrok-client` | ~8–9 MB each | One process per tunnel; negligible CPU between requests. |

- Both are **statically linked Go binaries** with no runtime dependencies.
- `top`/`ps` will show ~1.2 GB **VIRT/VSZ** — that's Go's reserved **virtual address space** (arena hint), **not** physical memory. Watch **RSS**, not VIRT.
- Check live numbers yourself via the stats endpoint: `curl -s localhost:7001/stats | jq .runtime` (`heap_alloc_mb`, `sys_mb`, `goroutines`).
- For context: a prior build leaked yamux streams and crept up to ~145 MB over several days; the current build stays flat at ~9 MB (see [CHANGELOG](CHANGELOG.md)).

## 🧠 Example: expose a local LLM gateway

A common real-world use is publishing an OpenAI-compatible gateway (e.g. [LiteLLM](https://github.com/BerriAI/litellm) on `:4000`) from a home/dev box through a public server. Because mygrok leaves `WriteTimeout` unset, **token-by-token streaming (SSE) works end to end**:

```bash
# On the gateway host (e.g. an OpenAI-compatible server listening on :4000)
export MYGROK_SERVER="example.com:7000"
export MYGROK_TOKEN="your-secret-token"
mygrok http 4000 my-app
```

Then call it from anywhere through the wildcard TLS front-end:

```bash
curl https://my-app.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"hi"}],"stream":true}'
```

Run it as a service (`--no-tui`) so it reconnects automatically and logs to the journal:

```bash
mygrok --no-tui http 4000 my-app
```

## 🩺 Troubleshooting

| Symptom | Likely cause | Check |
| --- | --- | --- |
| Tunnel flaps / reconnects repeatedly | Server data path wedged or client unreachable | `curl -s localhost:7001/stats` → high `inflight_requests` / `goroutines`; server log `keepalive failed` |
| `502`/timeout on a subdomain | Client offline or wrong subdomain | `GET /stats` → is the subdomain present with a recent `since`? |
| Client never reconnects fast enough | Frequent transient drops | Raise/fall `MYGROK_RECONNECT_SEC`; watch `attempt`/`reconnect_in` in logs |
| Can't reach `/stats` from remote | Loopback-only by design | Set `-admin-token`, or `ssh -L 7001:127.0.0.1:7001 your-vps` and curl locally |

## 🏗 Architecture

```text
[User Browser] -> [Nginx :443] -> [mygrok-server :8080]   [operator] -> [:7001 /stats (loopback)]
                                          |
                                    (Yamux Stream)
                                          |
[mygrok-client] <- (Control :7000) ------'
        |
[Local App :3000] (HTTP / WebSocket / SSE streaming / TCP / UDP)
```

> **Access by protocol:**
> - **HTTP tunnels** — always on port **443** via the nginx front-end (`https://subdomain.yourdomain.com`). No port assignment; nginx routes by `Host` header to mygrok `:8080`.
> - **TCP/UDP tunnels** — auto-assigned a public port from the `-port-base`/`-port-count` range (default 20000–20100). Access via `subdomain.yourdomain.com:PORT` or `your-vps-ip:PORT`. The subdomain is optional; both resolve to the same tunnel.

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 💖 Support the Project

- [**GitHub Sponsors**](https://github.com/sponsors/veloriba)
- [**Buy Me a Coffee**](https://www.buymeacoffee.com/veloriba)

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
