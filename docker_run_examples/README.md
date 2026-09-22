# Runnable Docker examples

Self-contained, copy-and-run examples for the `mygrok` Docker image (one
image, two binaries: `mygrok` client + `mygrok-server`). Each subfolder has a
`docker-compose.yml`, a `.env.example` (copy to `.env` and fill in), and a
short `README.md`. Only placeholders are used — replace `example.com`,
`your-vps.com`, and `your-secret-token` with your own values.

| Example | What | Key command |
| --- | --- | --- |
| [01-client-http](01-client-http/) | Expose a local HTTP port as `https://api.example.com` | `docker compose up -d` → visit `https://api.example.com` |
| [02-client-tcp](02-client-tcp/) | TCP port forward (local `:22` → public `:2222`, e.g. SSH) | `ssh -p 2222 your-user@your-vps.com` |
| [03-client-multi](03-client-multi/) | Several tunnels on one host (host network + shared container network) | `docker compose up -d` → tunnels `web` + `app-web` |
| [04-server-nginx](04-server-nginx/) | Full server stack: `mygrok-server` + nginx TLS front (builds the image itself) | `docker compose up -d` (wildcard certs in `certs/` required) |
| [05-server-minimal](05-server-minimal/) | Bare `mygrok-server`, no TLS front (tcp/udp tunnels or private networks) | `docker compose up -d` |

## Build the image once

Examples 01–03 and 05 expect the image to exist locally. Build it once from
the repo root:

```bash
# from the repo root
docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(cat VERSION) -t mygrok:$(cat VERSION) .
```

Example 04 builds the image itself (its compose file carries a `build:`
block), so it works on a machine without the prebuilt image.

Instead of building, every example can load a tarball produced by
`make docker-save` in the repo:

```bash
docker load -i dist/mygrok-<version>-<arch>.tar
```

## Docker Desktop / WSL note

`network_mode: host` (used by 01, 02, 03) needs Docker Desktop ≥ 4.29 on
Windows/macOS. On older versions (or WSL) use the default bridge network and
add `--local-host host.docker.internal` to each tunnel's command so it reaches
services running on the host.
