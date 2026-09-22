# 05 — server: minimal (no TLS front)

Only `mygrok-server`, no nginx. Best for **tcp/udp-only** setups or
**private networks** where clients reach the VPS directly (no public DNS /
TLS needed).

```bash
cp .env.example .env   # fill DOMAIN / MYGROK_TOKEN
docker compose up -d
docker compose logs -f mygrok-server
```

**Firewall:** open `7000/tcp` (clients dial `your-vps.com:7000` directly)
and `20000–20099` tcp+udp (auto-assigned tunnel ports).

**Expected:** clients connect with e.g.

```bash
mygrok tcp 22 ssh --server your-vps.com:7000 --public-port 2222 --no-tui
# then, from anywhere that can reach the VPS:
ssh -p 2222 your-user@your-vps.com
```

**HTTP tunnels without a TLS front are NOT recommended** — the `:8080`
listener is bound inside the container but not published. Publishing it
would serve tunnels as plain HTTP; use `04-server-nginx` for public HTTP.
