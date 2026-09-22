# 03 — client: several tunnels on one host

Two tunnels plus a dummy app, demonstrating the two wiring modes:

| Service | Mode | What it exposes | As |
| --- | --- | --- | --- |
| `web` | `network_mode: host` | a port on the Docker **host** (here `:8080`) | `https://web.example.com` |
| `app-web` | shared network `appnet` | the `app` **container**'s `:80` (via `--local-host app`) | `https://app-web.example.com` |

`app` is just `nginx:alpine` standing in for your real service — replace it
with any container that listens on the port the tunnel targets.

```bash
cp .env.example .env   # fill MYGROK_SERVER / MYGROK_TOKEN
docker compose up -d
docker compose logs -f web app-web
```

**Mode 1 (host network):** the tunnel container shares the host's network
stack, so it can reach `127.0.0.1:<port>` directly. Needs Docker Desktop
≥ 4.29 on Windows/macOS; on older versions/WSL use bridge +
`--local-host host.docker.internal` instead.

**Mode 2 (shared docker network):** the tunnel and the app live on the same
user-defined network; the tunnel dials the app by **service name**
(`--local-host app`). Works everywhere, and the app never needs to bind a
host port.

**Expected:** after `tunnel established` in both logs, `https://web.example.com`
serves the host's `:8080` app and `https://app-web.example.com` serves the
nginx welcome page from the `app` container.
