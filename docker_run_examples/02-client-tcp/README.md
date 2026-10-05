# 02 — client: TCP port forward (e.g. SSH)

Forwards local TCP `:22` (on the Docker host) to a public port on the server
— here `2222`, requested with `--public-port` (`0` would auto-assign from the
server's `20000–20099` range; check the port in the logs or the server's
`/stats`).

```bash
cp .env.example .env   # fill MYGROK_SERVER / MYGROK_TOKEN
docker compose up -d
ssh -p 2222 your-user@your-vps.com
```

Equivalent one-off `docker run` (host networking):

```bash
docker run --rm --network host -e MYGROK_TOKEN=your-secret-token \
  mygrok:0.4.0 mygrok tcp 22 ssh --server your-vps.com:7000 --public-port 2222 --no-tui
```

**Docker Desktop/WSL variant** (bridge network — the container cannot see
host ports, so point `--local-host` at the host):

```bash
docker run --rm -e MYGROK_TOKEN=your-secret-token \
  mygrok:0.4.0 mygrok tcp 22 ssh --server your-vps.com:7000 \
  --public-port 2222 --local-host host.docker.internal --no-tui
```

**Expected:** `ssh -p 2222 your-user@your-vps.com` lands on the host's local
SSH daemon (open `2222/tcp` in the server firewall).
