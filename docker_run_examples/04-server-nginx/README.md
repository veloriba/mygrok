# 04 — server: full stack (mygrok-server + nginx TLS front)

Self-contained server deployment: the `mygrok-server` container (tunnel
control on `:7000`, HTTP front on `127.0.0.1:8080`) and an `nginx:alpine` TLS
front routing wildcard subdomains to it — **both on `network_mode: host`**,
so nginx reaches the loopback front directly and dockerd's userland proxies
are never involved. Stop any host nginx on 80/443 before starting.

```bash
cp .env.example .env        # fill DOMAIN / MYGROK_TOKEN
mkdir -p certs              # drop fullchain.pem / privkey.pem here
docker compose up -d
docker compose ps
```

**Getting the image.** This example's compose file builds the image on first `up`
when it sits inside a mygrok repo checkout (`context: ../../`). Otherwise,
provide the image yourself — build from the repo root
(`docker build -f deploy/docker/Dockerfile --build-arg VERSION=0.4.0 -t mygrok:0.4.0 .`),
pull it from a registry, or `docker load -i mygrok-0.4.0-<arch>.tar`
(from `make docker-save`) — then delete the `build:` block and point
`image:` at the loaded tag.

**Certs (wildcard, required).** HTTP-01 cannot issue wildcards, so use
DNS-01 — either on the host:

```bash
sudo certbot certonly --manual --preferred-challenges dns -d "*.DOMAIN"
sudo cp /etc/letsencrypt/live/DOMAIN/fullchain.pem \
         /etc/letsencrypt/live/DOMAIN/privkey.pem certs/
```

or provision it any way you like and drop `fullchain.pem` / `privkey.pem`
into `certs/` (a `*.DOMAIN` cert covering your tunnel subdomains). The
`certs/` directory is gitignored — never commit real certificates.

**Firewall.** Open exactly:

| Port | Proto | Purpose |
| --- | --- | --- |
| `7000` | tcp | tunnel control — clients dial in here |
| `80` | tcp | nginx → 301 to HTTPS |
| `443` | tcp | public HTTPS (all HTTP tunnels) |
| `20000–20099` | tcp + udp | auto-assigned tcp/udp tunnel ports |

Keep `8080/tcp` **closed** (nginx is the only front) and never open
`7001` (loopback-only admin/stats listener).

**Expected:** `docker compose ps` shows `mygrok-server` and `nginx` running;
once a client tunnel (e.g. `01-client-http`) connects, `https://api.DOMAIN`
serves it. Clients point at `your-vps.com:7000` with the shared token.
