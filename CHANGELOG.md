# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- `install.sh`: one-shot server installer — `curl -fsSL https://raw.githubusercontent.com/veloriba/mygrok/main/install.sh | bash -` on the VPS. Interactive (domain / image source / certificate / port-conflict questions) or fully non-interactive via flags/env (`--domain`, `--token`, `--image-tar`, `--registry`, `--cert-dir`, `--adopt-host-nginx`, `--dry-run`). Stages the stack into `/opt/mygrok`, reuses an existing Let's Encrypt dir or runs the interactive DNS-01 certbot flow, detects and (on confirmation) adopts conflicting host nginx / old binary-mode installs, verifies the front-end, and supports in-place upgrade and `--uninstall [--purge]`.
- Client: run N tunnels in one process. New `mygrok up` subcommand starts every tunnel from a config file's `"tunnels"` list (`--config` / `-f`, default `config.json` next to the binary) or from positional `<protocol> <port> <subdomain>` triples (`mygrok up http 3000 api tcp 22 ssh`). Each spec entry supports `name`, `protocol` (http|https|tcp|udp; https implies insecure), `port`, `subdomain`, `public_port`, `insecure`, `set_headers`, and a per-tunnel `local_host` override; global flags (`--local-host`, `--insecure`, `--set-header`, `--public-port`, `--flush-interval`, `--upstream-timeout`) act as defaults. The legacy single-tunnel profile shape (top-level `port`/`subdomain`/`scheme`) still works in `up` and in the root command; a non-empty `"tunnels"` list takes precedence over the legacy fields. Duplicate subdomains are rejected client-side (the server closes the older tunnel on takeover).
- Client: in TUI mode the dashboard now renders all tunnels — the classic single-tunnel layout for one tunnel, and a table dashboard for several: one row per tunnel (name, status, forwarding address, request count, sent/received traffic) above a detail panel for the selected tunnel (last requests for http, or sent/received + uptime for tcp/udp). `--no-tui` log lines from every tunnel carry a `tunnel=<name>` attribute (name falls back to subdomain, then local address) so interleaved output stays attributable.
- `mygrok-task`: multi-tunnel mode — `mygrok-task add --multi <dir> --port N --sub name [--proto http|https|tcp|udp] [--local-host h] [--public-port N] [--network name]` scaffolds `<dir>/mygrok/{tunnels.json,docker-compose.yml,.env}`: one container (`mygrok up --config /etc/mygrok/tunnels.json`, `tunnels.json` mounted read-only, server/token from the `MYGROK_SERVER`/`MYGROK_TOKEN` environment) runs every tunnel added to the directory. Later `add --multi` calls append the spec to `tunnels.json` (duplicate subdomains are refused, the compose file is left untouched); `tunnels.json` never contains secrets (mode 0644), `.env` keeps them (mode 0600). Single-mode and multi-mode directories can't be mixed — both conflicts are detected and refused. `up`/`down`/`status`/`logs` work unchanged.
- `docker_run_examples/06-client-multitunnel`: single-container multi-tunnel example — compose + `.env.example` + sample `tunnels.json` (two tunnels, placeholders only) + `docker run` one-liner equivalent.
- `deploy/docker/docker-compose.client.yml`: commented `mygrok-multi` service showing the one-container alternative to the one-service-per-tunnel style (`mygrok up --config /etc/mygrok/tunnels.json` with `tunnels.json` mounted read-only, `MYGROK_SERVER`/`MYGROK_TOKEN` from the environment, `tunnels.json` example in the comments); `.env.example` notes that multi-mode containers read the server/token from the environment, keeping `tunnels.json` secret-free.
- `systemd/mygrok-client.service`: commented alternative `ExecStart` for multi mode (`mygrok-client up --config /etc/mygrok/tunnels.json --no-tui`); the default unit behavior is unchanged.
- README: new "Multi-tunnel: one process, many tunnels" section (`mygrok up -f tunnels.json` and the triples form, minimal config, N tunnels = N server-side registrations in one process/container/unit) with pointers to the new Docker example and `mygrok-task add --multi`; task-CLI and runnable-examples sections updated accordingly.

### Changed
- The `mgrok-task` CLI is renamed to `mygrok-task` (built as `bin/mygrok-task` via `make task`) to match the project name; the old binary name is gone, so reinstall it if it was on your PATH. Historical changelog entries keep the old name as released.
- Client TUI (multi-tunnel): the dashboard is now interactive on a real terminal — `j`/`k` move the selection (wrapping around; the initial selection prefers error tunnels, then recent activity), `r` force-refreshes, and `q` (or `Ctrl-C`) quits. Keys are read in raw mode only when stdin is a TTY; when stdin is piped/captured, or on Windows (no raw-mode key reader), the selection auto-rotates instead. The single-tunnel view is unchanged and not affected by keys.
- Server docker stack now uses `network_mode: host` for both `mygrok-server` and the nginx sidecar (binds `:7000`, tunnel range, `127.0.0.1:8080`, and `80`/`443` directly, matching bare-binary topology). Rationale, found in a production migration: publishing the full 100-port tcp+udp tunnel range through dockerd userland proxies is slow and crashed dockerd on a small host (docker 27.3.1/kernel 6.1), and a loopback-bound HTTP front is unreachable from a bridge-network nginx (its requests die with an upstream-refused response). The front stays loopback (`MYGROK_HTTP_ADDR`), the chain never leaves the host loopback, and no docker port publishing is needed. Listener ports configurable via `MYGROK_CONTROL_ADDR` / `MYGROK_HTTP_ADDR` / `MYGROK_PORT_BASE` / `MYGROK_PORT_COUNT` (compose files, examples, install.sh template, docs updated).
- README: documented how to get the server image (build on the VPS / registry / offline tarball) and added an "Upgrading / migrating the server" section (image update + `up -d` recreate, idempotent `make server-install`, binary↔Docker migration, client auto-reconnect behavior, cert renewal in both modes).

### Fixed
- Client: HTTP tunnels no longer hang on SIGINT/SIGTERM — the shutdown path closed a separate `http.Server` instance that never served, while `http.Serve` kept blocking on the still-open yamux session, so the reconnect loop never observed the shutdown (tcp/udp tunnels already closed their session on shutdown).
- `install.sh`: in-place upgrade no longer aborts on its own ports — the still-running stack's `docker-proxy` listeners on 80/443/7000 are now recognized as the installer's own stack (recreated by the upcoming `compose up`) instead of being reported as host-nginx conflicts.
- `install.sh`: `confirm()` crashed with `$2: unbound variable` when called without a default answer (port-conflict prompt), aborting non-interactive upgrades; the default is now optional.
- `install.sh`: `--dry-run` printed a garbled "image" line (leftover text from the next branch) when `--image-tar` was set; the three image-source branches are now mutually exclusive.
- `install.sh`: `--uninstall --purge` no longer reports "purged" when the image removal fails (e.g. image still in use) — it warns and prints the manual `docker image rm` command instead.

## [0.3.0] - 2026-09-23

### Added
- Docker deployment for the **server**: `deploy/docker/docker-compose.server.yml` runs `mygrok-server` behind an nginx TLS sidecar (wildcard cert mounted from `./certs/`, `proxy_buffering off` for SSE/LLM streaming, canonical `map`-based WebSocket upgrade handling). Published ports: 7000 (control), 80/443 (public front), 20000-20099 tcp+udp (tunnel ports); the raw `:8080` front stays inside the compose network. Plus `deploy/docker/.env.server.example` and Makefile targets `docker-server-build/up/down/status/logs`.
- `deploy/nginx/mygrok-nginx.conf.example`: a bare-VPS nginx template (wildcard `server_name`, HTTP→HTTPS redirect, ACME challenge location) for deployments without the compose stack.
- `docker_run_examples/`: five self-contained, placeholder-only examples — client (http, tcp, multi-service on shared networks) and server (with nginx, minimal) — each with compose + `.env.example` + a `docker run` one-liner, and an index README.
- Regression tests: `TestSubdomainFromHost` (host-header edge cases) and `TestBuildCommandArgs` (mgrok-task client argument assembly).
- `mgrok-task` CLI (`cmd/task`, built via `make task`): scaffolds and manages per-project tunnels. `mgrok-task add <dir> --port <n> --sub <name>` creates `<dir>/mygrok/{docker-compose.yml,.env}` (project `mygrok-<sub>`, container `mygrok_<sub>`, `restart: unless-stopped`); `up`/`down`/`status`/`logs` drive it. `mgrok-task config set` stores `MYGROK_SERVER`/`MYGROK_TOKEN`/`MYGROK_TAG` in `~/.config/mygrok/env` (mode 0600). `--network <name>` + `--local-host <container>` target services on an existing docker network instead of `network_mode: host`.
- Docker deployment for client tunnels (recommended way to run clients): `deploy/docker/Dockerfile` (multi-stage, static Go build, `scratch` final image, non-root, ~7 MB) and `deploy/docker/docker-compose.client.yml` with a `.env.example` — one tunnel per service, `network_mode: host`, `restart: unless-stopped`. A single linux/amd64+arm64 image covers Linux, macOS, and Windows (Docker Desktop) hosts, replacing per-OS binaries and WSL setups.
- Makefile Docker targets: `docker-build` (current platform), `docker-buildx` (multi-arch push to a registry), `docker-save` (offline tarball in `dist/`), and `docker-up` / `docker-down` / `docker-status` / `docker-logs SERVICE=<name>` for day-to-day management.
- Deployment tooling: `systemd/mygrok-client.service` template plus `scripts/install-client.sh`, `scripts/uninstall-client.sh` (Linux + systemd hosts) and `scripts/install-wsl-client.sh`, `scripts/uninstall-wsl-client.sh` (Windows host + WSL2 distro, watchdog loop + on-logon scheduled task). No secrets are hardcoded; the token is delivered to the client via the `MYGROK_TOKEN` environment variable.
- Server deployment tooling for Ubuntu: `systemd/mygrok.service` template plus `scripts/install-server.sh` and `scripts/uninstall-server.sh` (build + remote install/uninstall over SSH, with binary backup, staged upload, and validation). The auth token is delivered via a `0600` `EnvironmentFile` (`/etc/mygrok/mygrok.env`), never on the `ExecStart` command line, so it no longer appears in `ps`.
- Makefile: `server-install`/`server-uninstall` now delegate to the scripts, and a new `check-config` guard refuses to run when required settings (`SERVER_HOST`/`SERVER_USER`/`DOMAIN`/`TOKEN`/`SUDO_PWD`) are missing or the token is left at a known-insecure placeholder. Insecure defaults were removed from the Makefile.
- Structured logging via `log/slog` on both server and client, with `-v` / `--log-level` (`debug|info|warn|error`) and `--log-format` (`text|json`); defaults to `info` / `text`. Env overrides: `MYGROK_LOG_LEVEL`, `MYGROK_LOG_FORMAT`.
- yamux's internal `[ERR]`/`[WARN]` output and the HTTP reverse-proxy error log are routed through slog on the server and in client `--no-tui` mode; in TUI mode yamux output is discarded so it can no longer corrupt the dashboard redraw.
- Correlation fields on server lifecycle logs: a per-connection `conn` id, the client `remote` address, and `subdomain`/`proto`. Tunnels now emit an explicit `tunnel closed` event with `served` count and `duration` (previously only "established" was logged, so disconnects were invisible).
- Loopback admin/stats listener (`-admin`, default `127.0.0.1:7001`; `off`/empty disables) serving `GET /stats` (alias `/_mygrok/stats`) and `GET /healthz`. It is bound separately from the public `:8080` front-end, so metrics are never reachable through the tunnel. Access is allowed from loopback, or from any source presenting `-admin-token` / `MYGROK_ADMIN_TOKEN` (via `X-Mygrok-Admin-Token` or `Authorization: Bearer`). `/stats` reports uptime, active tunnels (subdomain, protocol, port, remote, since, served), in-flight request count, and runtime stats (goroutines, heap/sys MB, GC count).
- Regression tests: `TestAdminStats` (endpoint reachability, counters, public-path 404) and `TestHTTPStreamReclaimed` (a request burst asserting in-flight drains back to zero).
- Client flag `--flush-interval` (env `MYGROK_FLUSH_INTERVAL`, default `-1`) controls how often the reverse proxy flushes the response to the tunnel: `-1` flushes after every write (recommended for low-latency streaming), `0` flushes only when the response completes, or a duration (e.g. `100ms`) for periodic flushes.
- Client flag `--local-host` (env `MYGROK_LOCAL_HOST`, default `127.0.0.1`) makes the local upstream host configurable (previously hardcoded to `127.0.0.1`) for Docker/container IPs or non-loopback binds.
- Client flag `--insecure` (env `MYGROK_INSECURE`, default `false`) skips TLS certificate verification for the local upstream, needed when the local service (the `https` subcommand) uses a self-signed certificate.
- Client flag `--set-header` (repeatable) injects static `Key: Value` headers into every proxied request.
- Client flag `--upstream-timeout` (env `MYGROK_UPSTREAM_TIMEOUT`, default `0` = no timeout) sets the timeout for receiving response headers from the local upstream (the transport's `ResponseHeaderTimeout`). Together with `--local-host`, `--insecure`, and `--set-header`, these round out the client's upstream controls.

### Changed
- The Docker image now contains **both** binaries (`mygrok` client and `mygrok-server`) in one `scratch` image; compose files and `docker run` select the role via the command. `docker-buildx` publishes both under the same tag.
- README reworked: Docker is now the recommended (not required) way to run both server and client; added Use Cases, SSL & Nginx (pointing at `deploy/nginx/mygrok-nginx.conf.example` instead of the dead wiki link), and Firewall sections.
- `mgrok-task`: the `--scheme` flag is gone; `--insecure` is derived from `--proto https` (a local upstream that speaks TLS is assumed self-signed in dev).
- Client reconnect now uses exponential backoff with jitter (base = `MYGROK_RECONNECT_SEC` / 5s, capped at 30s, reset after a healthy session) instead of a fixed 5s retry, avoiding a thundering-herd of simultaneous reconnects after a server restart.
- HTTP tunneling reuses one per-tunnel `http.Transport` with `DisableKeepAlives` (a single yamux stream opened and closed per request) instead of constructing a fresh transport per request.

### Fixed
- Server: subdomain extraction from the `Host` header now requires a dot separator (a host like `subex.com` can no longer be misparsed as a subdomain of the domain `ex.com`) and tolerates a port suffix (`sub.example.com:8080` on the raw front now routes correctly instead of 404-ing).
- `.dockerignore` added so local artifacts (`bin/`, `dist/`, `.git/`, gitignored env files) never enter the Docker build context.
- `scripts/install-client.sh`: pre-install `pkill -f mygrok-client` matched its own remote wrapper shell and killed it mid-run, aborting the script (now uses a bracket pattern that cannot self-match).
- `scripts/install-client.sh`: unit installation piped content into `echo "$SUDO_PWD" | sudo -S tee <unit>`, so tee only ever received the password line — an empty ("masked") 0-byte unit file was installed. The rendered unit is now staged over ssh stdin and installed with `sudo cp`.
- Server: fixed a regression where HTTP requests routed by hostname to a **`tcp`-protocol tunnel** (e.g. a vllm server reached via `https://<sub>.domain/v1`) panicked with `transport is nil` — the proxy transport was only created for `http`-protocol tunnels. A transport is now created for every tunnel (with a defensive nil-guard in `ServeHTTP`), restoring the previous behavior of proxying HTTP over any tunnel's session. Covered by `TestHTTPRequestToTCPTunnel`.
- Server: fixed a yamux **stream leak** on the HTTP data path. A new `http.Transport` was built per request and discarded without closing its idle connection, so each proxied request leaked a yamux stream (and its window buffer). Streams accumulated until the proxy accept loop wedged — symptoms: the `:8080` accept backlog pinned high, in-flight handlers pinned, `keepalive failed` / `frame for missing stream` churn, and every tunnel on the server flapping in lockstep.
- Server: added `http.Server` timeouts (`ReadHeaderTimeout`, `IdleTimeout`, `MaxHeaderBytes`) and transport timeouts (`ResponseHeaderTimeout`, a 15s dial budget, `ExpectContinueTimeout`) so a hung backend or vanished client cannot pin a handler/stream indefinitely. `WriteTimeout` is intentionally left unset to preserve long-lived SSE/streaming responses.
- Server: tunnel teardown is now deterministic — a takeover closes the old session's transport/listeners, and the map entry is removed only if it still points at the closing tunnel (so a reconnecting client can't have its freshly-registered tunnel deleted by a stale handler).
- Client: set the yamux config's `LogOutput` to nil when providing a `Logger` (yamux rejects both being set), which otherwise failed every session init with "both Logger and LogOutput may not be set".
- Fixed a data race on the resolved listener addresses, now read through `ControlListenAddr()` / `HTTPListenAddr()` / `AdminListenAddr()` accessors.

## [0.2.0] - 2026-08-30

### Added
- TCP tunneling: `mygrok tcp [local-port] [subdomain]` forwards a raw local TCP port (e.g. SSH) through the tunnel to an auto-assigned or requested public port on the server.
- UDP tunneling: `mygrok udp [local-port] [subdomain]` forwards a local UDP port through the tunnel (packets are framed with the peer address; single-peer friendly).
- Client flag `--public-port` to request a specific public port for tcp/udp tunnels (0 = auto-assign from the server's port range).
- Server flags `-port-base` and `-port-count` to configure the auto-assign port range for tcp/udp tunnels (defaults 20000 / 100).
- In `--no-tui` mode, lifecycle events (connected / disconnected / reconnecting) are logged to stderr so a service manager or journal can observe them.

### Changed
- Faster dead-connection detection: yamux keepalive interval lowered from 30s to 10s on both client and server, plus TCP keepalive (30s) on the client's outbound dial.
- Reconnect retry interval is now configurable via the `MYGROK_RECONNECT_SEC` environment variable (default 5s).

### Fixed
- `--no-tui` flag now actually disables the TUI dashboard (previously it was a no-op).

## [0.1.1] - 2026-05-03

### Added
- Traffic statistics in client UI (Requests, KB Transmitted, KB Received).
- Uptime tracking for client sessions.
- `VERSION` file for centralized version management.

### Changed
- Improved HTTP requests table layout (swapped Status and Path columns).
- Fixed alignment issues with colored status codes in terminal.

## [0.1.0] - 2026-05-03

### Added
- Initial release of mygrok.
- Basic tunneling functionality (HTTP).
- Simple terminal UI for client.
