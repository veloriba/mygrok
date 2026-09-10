# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
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
- Client reconnect now uses exponential backoff with jitter (base = `MYGROK_RECONNECT_SEC` / 5s, capped at 30s, reset after a healthy session) instead of a fixed 5s retry, avoiding a thundering-herd of simultaneous reconnects after a server restart.
- HTTP tunneling reuses one per-tunnel `http.Transport` with `DisableKeepAlives` (a single yamux stream opened and closed per request) instead of constructing a fresh transport per request.

### Fixed
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
