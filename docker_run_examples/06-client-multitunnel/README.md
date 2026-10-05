# 06 — client: one container, all tunnels

A single `mygrok` process runs **every tunnel listed in `tunnels.json`**
(`mygrok up --config /etc/mygrok/tunnels.json`), so one container exposes as
many tunnels as you want — no one-service-per-tunnel bookkeeping. N tunnels =
N server-side registrations, all in this one container.

| Tunnel | Protocol | What it exposes | As |
| --- | --- | --- | --- |
| `api` | http | a port on the Docker **host** (here `:3000`) | `https://api.example.com` |
| `ssh` | tcp | a port on the Docker **host** (here `:22`) | `your-vps.com:2222` |

```bash
cp .env.example .env   # fill MYGROK_SERVER / MYGROK_TOKEN
docker compose up -d
docker compose logs -f
```

The `server`/`token` fields in `tunnels.json` are left **empty on purpose** —
the container picks them up from `MYGROK_SERVER` / `MYGROK_TOKEN` (set in
`.env`), so the file stays safe to commit. Fill them in the file instead if
you prefer.

**Adding or removing a tunnel:** edit `tunnels.json` — each entry supports
`name`, `protocol` (`http|https|tcp|udp`), `port`, `subdomain`, optional
`public_port`, `insecure`, `set_headers`, and a per-tunnel `local_host`
override — then restart the container:

```bash
docker compose restart
```

(Equivalent one-off `docker run`, Linux or Docker Desktop ≥ 4.29 with host
networking:

```bash
docker run --rm --network host \
  -v "$PWD/tunnels.json:/etc/mygrok/tunnels.json:ro" \
  -e MYGROK_SERVER=your-vps.com:7000 -e MYGROK_TOKEN=your-secret-token \
  mygrok:0.3.0 mygrok up --config /etc/mygrok/tunnels.json --no-tui --log-format json
```)

**Expected:** after `tunnel established` lines for both `api` and `ssh` in
the log, `https://api.example.com` serves the host's `:3000` app (assuming
the server runs behind the nginx TLS front — see `../04-server-nginx/`) and
`ssh -p 2222 your-user@your-vps.com` reaches the host's sshd.

Docker Desktop/WSL < 4.29: drop `network_mode: host` (or `--network host`)
and add `local_host: "host.docker.internal"` to each tunnel entry instead.
