# 01 — client: expose a local HTTP port

Exposes a service listening on the Docker **host** (here `:3000`) as
`https://api.example.com` (your base domain).

```bash
cp .env.example .env   # fill MYGROK_SERVER / MYGROK_TOKEN
docker compose up -d
docker compose logs -f api
```

Equivalent one-off `docker run` (Linux, or Docker Desktop ≥ 4.29 with host
networking):

```bash
docker run --rm --network host -e MYGROK_TOKEN=your-secret-token \
  mygrok:0.4.0 mygrok http 3000 api --server your-vps.com:7000 --no-tui
```

**Expected:** after `tunnel established` in the logs, visiting
`https://api.example.com` serves your local app (assuming the server runs
behind the nginx TLS front — see `../04-server-nginx/`).

Docker Desktop/WSL < 4.29: drop `network_mode: host` (or `--network host`)
and add `--local-host host.docker.internal` to the command.
