#!/usr/bin/env bash
#
# install.sh — one-shot installer for the mygrok server stack (mygrok-server
# + nginx TLS front) on the target host.
#
#   curl -fsSL https://raw.githubusercontent.com/veloriba/mygrok/main/install.sh | bash -
#
# Interactive: asks a few questions (domain, image source, certificate,
# port conflicts). Non-interactive: override via flags or environment:
#
#   DOMAIN=example.com MYGROK_TOKEN=... \
#     curl -fsSL .../install.sh | bash - --non-interactive --image-tar /path/mygrok-0.3.0-amd64.tar.gz
#
# Flags:
#   --domain <base>        base domain for tunnels (required; env DOMAIN)
#   --token <t>            auth token (env MYGROK_TOKEN; auto-generated if empty)
#   --image-tar <file>     load this image tarball instead of pull/build
#   --registry <reg>       registry prefix for the image (default ghcr.io/veloriba)
#   --repo <dir>           build the image from this mygrok checkout
#   --cert-dir <dir>       dir containing fullchain.pem + privkey.pem on this host
#   --dir <path>           install/staging directory (default /opt/mygrok)
#   --adopt-host-nginx     stop+disable the host nginx that owns 80/443 (else: ask)
#   --non-interactive      never prompt; everything must come from flags/env
#   --dry-run              show what would happen, change nothing
#   --uninstall            stop and remove the stack (add --purge to delete files/image)
#   --purge                with --uninstall: also remove the install dir and the image
#   -h | --help            this help
#
# Templates and a few expressions are intentionally kept literal (placeholders
# are substituted with sed at render time); REPLY_ANSWER and loop counters are
# used by convention, not by reference.
# shellcheck disable=SC2016
# shellcheck disable=SC2034
set -euo pipefail

VERSION="0.3.0"   # kept in sync with the VERSION file / release tag
REGISTRY="ghcr.io/veloriba"
IMAGE_NAME="mygrok"

DOMAIN=""
TOKEN=""
IMAGE_TAR=""
REPO_DIR=""
CERT_DIR=""
STAGE_DIR="/opt/mygrok"
ADOPT_HOST_NGINX="ask"
NON_INTERACTIVE=0
DRY_RUN=0
UNINSTALL=0
PURGE=0

# ---------------------------------------------------------------- helpers ---

c_red=$'\033[31m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'; c_cyn=$'\033[36m'; c_rst=$'\033[0m'
log()  { printf '%s==>%s %s\n' "$c_cyn" "$c_rst" "$*"; }
ok()   { printf '%s  ok%s %s\n' "$c_grn" "$c_rst" "$*"; }
warn() { printf '%s  !!%s %s\n' "$c_ylw" "$c_rst" "$*" >&2; }
die()  { printf '%s  !!%s %s\n' "$c_red" "$c_rst" "$*" >&2; exit 1; }

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || die "not root and sudo is not available (re-run with sudo, or as root)"
    SUDO="sudo"
fi

# Prompts read from /dev/tty so `curl | bash` (script on stdin) still works.
can_ask() { [ "$NON_INTERACTIVE" -eq 0 ] && [ -e /dev/tty ] && ( : </dev/tty ) 2>/dev/null; }
ask() { # ask <prompt> <default> -> $REPLY_ANSWER
    local prompt="$1" default="${2:-}"
    if can_ask; then
        local ans=""
        if [ -n "$default" ]; then
            read -r -p "$prompt [$default]: " ans </dev/tty || ans=""
            REPLY_ANSWER="${ans:-$default}"
        else
            read -r -p "$prompt: " ans </dev/tty || die "no answer (non-interactive tty?)"
            [ -n "$ans" ] || die "a value is required: $prompt"
            REPLY_ANSWER="$ans"
        fi
    else
        [ -n "$default" ] || die "missing value for '$prompt' (non-interactive: pass it via flag/env)"
        REPLY_ANSWER="$default"
    fi
}
confirm() { # confirm <question> <y|n> -> returns 0 for yes
    # In non-interactive mode never assume "yes": interactive steps (certbot
    # DNS-01, apt installs, stopping host services) must be explicit.
    local q="$1" def="${2:-}" ans=""
    if can_ask; then
        read -r -p "$q [y/N]: " ans </dev/tty || ans=""
        case "${ans:-$def}" in [Yy]*) return 0 ;; *) return 1 ;; esac
    else
        return 1
    fi
}

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }

# ------------------------------------------------------------------- args ---

while [ $# -gt 0 ]; do
    case "$1" in
        --domain)           DOMAIN="${2:?}"; shift 2 ;;
        --token)            TOKEN="${2:?}"; shift 2 ;;
        --image-tar)        IMAGE_TAR="${2:?}"; shift 2 ;;
        --registry)         REGISTRY="${2:?}"; shift 2 ;;
        --repo)             REPO_DIR="${2:?}"; shift 2 ;;
        --cert-dir)         CERT_DIR="${2:?}"; shift 2 ;;
        --dir)              STAGE_DIR="${2:?}"; shift 2 ;;
        --adopt-host-nginx) ADOPT_HOST_NGINX="yes"; shift ;;
        --non-interactive)  NON_INTERACTIVE=1; shift ;;
        --dry-run)          DRY_RUN=1; shift ;;
        --uninstall)        UNINSTALL=1; shift ;;
        --purge)            PURGE=1; shift ;;
        -h|--help)          usage; exit 0 ;;
        *) die "unknown flag: $1 (see --help)" ;;
    esac
done
DOMAIN="${DOMAIN:-${MYGROK_DOMAIN:-}}"
TOKEN="${TOKEN:-${MYGROK_TOKEN:-}}"

[ "$UNINSTALL" -eq 0 ] || [ "$PURGE" -eq 0 ] || true
if [ "$UNINSTALL" -eq 1 ] && [ "$PURGE" -eq 1 ]; then :; fi
if [ "$PURGE" -eq 1 ] && [ "$UNINSTALL" -eq 0 ]; then die "--purge requires --uninstall"; fi

# ------------------------------------------------------------- templates ---
# Kept in sync with deploy/docker/docker-compose.server.yml (image-only
# variant: the image is always provided by pull/build/load, never by compose)
# and deploy/docker/nginx-server.conf (with the example.com placeholder).

COMPOSE_TEMPLATE='
name: mygrok-server

services:
  mygrok-server:
    image: mygrok:${MYGROK_TAG:-TAG_PLACEHOLDER}
    network_mode: host
    command: ["mygrok-server", "-domain", "${DOMAIN}", "-control", "${MYGROK_CONTROL_ADDR:-:7000}", "-http", "${MYGROK_HTTP_ADDR:-127.0.0.1:8080}", "-port-base", "${MYGROK_PORT_BASE:-20000}", "-port-count", "${MYGROK_PORT_COUNT:-100}", "-log-format", "json"]
    environment:
      MYGROK_TOKEN: ${MYGROK_TOKEN}
    restart: unless-stopped

  nginx:
    image: nginx:alpine
    volumes:
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
      - ./certs:/etc/nginx/certs:ro
    ports:
      - "80:80"
      - "443:443"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on:
      - mygrok-server
    restart: unless-stopped
'

NGINX_TEMPLATE='# mygrok server — nginx front-end (rendered by install.sh).
#
# WebSocket upgrade support (mygrok proxies WebSocket/HMR connections).
map $http_upgrade $connection_upgrade {
    default upgrade;
    '"'"''"'"'      close;
}

# Plain HTTP: redirect everything to HTTPS.
server {
    listen 80;
    server_name ~^(?<subdomain>.+)\.DOMAIN_PLACEHOLDER$;

    return 301 https://$host$request_uri;
}

# HTTPS: wildcard tunnel front-end. Nginx passes the Host header through and
# the mygrok server dispatches to the right tunnel.
server {
    listen 443 ssl;
    http2 on;
    server_name ~^(?<subdomain>.+)\.DOMAIN_PLACEHOLDER$;

    ssl_certificate     /etc/nginx/certs/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/privkey.pem;

    location / {
        # server runs with network_mode: host, HTTP front on the host loopback
        proxy_pass http://host.docker.internal:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        # Required for SSE/LLM streaming: never buffer streamed responses.
        proxy_buffering off;
        # Long-lived streams (SSE, LLM token streams) must not be cut off.
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
'

# ----------------------------------------------- install (uninstall) ---

do_uninstall() {
    [ -f "$STAGE_DIR/docker-compose.yml" ] || die "no mygrok install found at $STAGE_DIR"
    log "stopping the mygrok stack in $STAGE_DIR"
    $SUDO docker compose --env-file "$STAGE_DIR/.env" -f "$STAGE_DIR/docker-compose.yml" down
    ok "stack stopped (image kept)"
    if [ "$PURGE" -eq 1 ]; then
        log "removing $STAGE_DIR and the mygrok image"
        $SUDO rm -rf "$STAGE_DIR"
        if docker image rm "mygrok:$VERSION" >/dev/null 2>&1; then
            ok "purged"
        else
            warn "purged files, but image mygrok:$VERSION is still in use — remove it later with: docker image rm mygrok:$VERSION"
        fi
    else
        warn "files kept in $STAGE_DIR (re-run install.sh to start it again, or remove manually)"
    fi
    echo
    echo "If you previously adopted the host nginx or a binary-mode unit,"
    echo "re-enabling those is manual: systemctl enable --now <unit>."
}

# --------------------------------------------------------------- preflight ---

preflight() {
    log "preflight checks"
    [ "$(uname -s)" = "Linux" ] || die "this installer targets Linux hosts (VPS); on other OSes use the manual instructions in the repo README"
    command -v docker >/dev/null 2>&1 || {
        if confirm "docker is not installed — install docker.io + docker-compose-v2 via apt now?" y; then
            $SUDO apt-get update -qq && $SUDO apt-get install -y -qq docker.io docker-compose-v2
            $SUDO systemctl enable --now docker
        else
            die "docker is required (install it and re-run)"
        fi
    }
    ok "docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 'daemon not running')"
    $SUDO systemctl is-active docker >/dev/null 2>&1 || { $SUDO systemctl start docker || die "cannot start the docker daemon"; }
    if ! docker compose version >/dev/null 2>&1; then
        command -v docker-compose >/dev/null 2>&1 || die "docker compose v2 (or docker-compose) is required"
    fi
    ok "docker compose"
    command -v curl >/dev/null 2>&1 || die "curl is required"
    ok "curl"
}

# ------------------------------------------------------------- gather inputs ---

gather() {
    # Existing install?
    if [ -f "$STAGE_DIR/.env" ]; then
        log "existing install found at $STAGE_DIR — upgrading in place (token is preserved)"
        [ -n "$TOKEN" ] || TOKEN="$(grep '^MYGROK_TOKEN=' "$STAGE_DIR/.env" | cut -d= -f2-)"
        local old_domain
        old_domain="$(grep '^DOMAIN=' "$STAGE_DIR/.env" | cut -d= -f2-)"
        if [ -z "$DOMAIN" ]; then DOMAIN="$old_domain"; fi
        [ "$DOMAIN" = "$old_domain" ] || warn "domain changed: $old_domain -> $DOMAIN"
    fi

    [ -n "$DOMAIN" ] || {
        if can_ask; then
            read -r -p "Base domain for tunnels (e.g. tunnels.example.com): " DOMAIN </dev/tty
        else
            die "--domain (or DOMAIN env) is required"
        fi
    }
    case "$DOMAIN" in
        *.*) ;;
        *) die "domain '$DOMAIN' must be a full domain (e.g. example.com)" ;;
    esac
    printf '%s' "$DOMAIN" | grep -qE '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' \
        || die "domain '$DOMAIN' looks invalid (lowercase labels, hyphens ok)"
    ok "domain: $DOMAIN"

    if [ -z "$TOKEN" ]; then
        if command -v openssl >/dev/null 2>&1; then
            TOKEN="$(openssl rand -hex 32)"
            ok "token: auto-generated (stored in $STAGE_DIR/.env, mode 600)"
        else
            die "MYGROK_TOKEN not provided and openssl is unavailable to generate one"
        fi
    else
        ok "token: provided"
    fi

    # Image source: explicit flag > local repo checkout > registry pull.
    if [ -z "$REPO_DIR" ] && [ -n "${MYGROK_REPO:-}" ]; then REPO_DIR="$MYGROK_REPO"; fi
    if [ -z "$REPO_DIR" ] && [ -f "$(dirname "$0")/deploy/docker/Dockerfile" ]; then
        REPO_DIR="$(cd "$(dirname "$0")" && pwd)"   # running from a repo checkout
    fi
    if [ -n "$IMAGE_TAR" ]; then
        [ -f "$IMAGE_TAR" ] || die "image tarball not found: $IMAGE_TAR"
        ok "image: tarball $IMAGE_TAR"
    elif [ -n "$REPO_DIR" ]; then
        [ -f "$REPO_DIR/deploy/docker/Dockerfile" ] || die "no deploy/docker/Dockerfile in $REPO_DIR"
        ok "image: built from checkout $REPO_DIR"
    else
        ok "image: pulled from $REGISTRY/$IMAGE_NAME:$VERSION"
    fi

    # Certificate source.
    if [ -n "$CERT_DIR" ]; then
        [ -f "$CERT_DIR/fullchain.pem" ] && [ -f "$CERT_DIR/privkey.pem" ] \
            || die "$CERT_DIR must contain fullchain.pem and privkey.pem"
        ok "certificate: $CERT_DIR"
    elif [ -d "/etc/letsencrypt/live/$DOMAIN" ] && [ -e "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" ]; then
        CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
        ok "certificate: existing Let's Encrypt live dir /etc/letsencrypt/live/$DOMAIN"
    else
        if confirm "no wildcard certificate found for *.$DOMAIN — run certbot (manual DNS-01) now?" y; then
            command -v certbot >/dev/null 2>&1 || { $SUDO apt-get install -y -qq certbot || die "certbot install failed"; }
            log "creating wildcard certificate for *.$DOMAIN (follow the DNS TXT prompts)"
            $SUDO certbot certonly --manual --preferred-challenges dns -d "*.$DOMAIN" \
                || die "certbot failed — provision the certificate manually and re-run with --cert-dir"
            CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
        else
            die "a wildcard certificate for *.$DOMAIN is required (provide one and re-run with --cert-dir <dir>)"
        fi
    fi
}

# --------------------------------------------------------- conflict handling ---

port_owner() { $SUDO ss -tlnp 2>/dev/null | awk -v p=":$1$" '$4 ~ p {print $6, $7}' | head -2; }

handle_conflicts() {
    local p owner
    # On an in-place upgrade our own stack (still running) holds these ports
    # via docker-proxy; compose up below recreates it, so that is not a conflict.
    local own_stack=0
    [ -f "$STAGE_DIR/docker-compose.yml" ] && own_stack=1
    for p in 80 443 7000; do
        owner="$(port_owner "$p")"
        [ -n "$owner" ] || continue
        if [ "$own_stack" -eq 1 ] && printf '%s' "$owner" | grep -q docker-proxy; then
            ok "port $p is held by our own stack — it will be restarted by the upgrade"
            continue
        fi
        warn "port $p is already in use: $owner"
        case "$ADOPT_HOST_NGINX" in
            yes)
                if $SUDO systemctl is-active nginx >/dev/null 2>&1; then
                    log "stopping and disabling the host nginx (--adopt-host-nginx)"
                    $SUDO systemctl disable --now nginx
                fi
                ;;
            ask)
                if confirm "stop+disable the host nginx to free port $p? (its other sites stop serving!)"; then
                    $SUDO systemctl disable --now nginx
                else
                    die "port $p must be free — free it manually (e.g. disable the host nginx or its site) and re-run"
                fi
                ;;
        esac
    done
    # Old binary-mode server (scripts/install-server.sh layout)?
    if [ -f /etc/systemd/system/mygrok.service ] && $SUDO systemctl is-enabled mygrok >/dev/null 2>&1; then
        warn "found a binary-mode mygrok systemd unit (scripts/install-server.sh layout)"
        if confirm "stop+disable the old mygrok.service (its token is carried over, files are kept)" y; then
            $SUDO systemctl disable --now mygrok
            if [ -z "$TOKEN" ] && [ -r /etc/mygrok/mygrok.env ]; then
                TOKEN="$(grep '^MYGROK_TOKEN=' /etc/mygrok/mygrok.env | cut -d= -f2-)"
                [ -n "$TOKEN" ] && ok "token: carried over from /etc/mygrok/mygrok.env"
            fi
        else
            die "the old mygrok service still owns port 7000 — disable it (systemctl disable --now mygrok) and re-run"
        fi
    fi
}

# --------------------------------------------------------------------- image ---

provide_image() {
    if [ -n "$IMAGE_TAR" ]; then
        log "loading image from $IMAGE_TAR"
        docker load -i "$IMAGE_TAR"
    elif [ -n "$REPO_DIR" ]; then
        local ver
        ver="$(cat "$REPO_DIR/VERSION" 2>/dev/null || echo "$VERSION")"
        log "building image mygrok:$ver from $REPO_DIR"
        $SUDO docker build -f "$REPO_DIR/deploy/docker/Dockerfile" \
            --build-arg VERSION="$ver" -t "mygrok:$ver" "$REPO_DIR"
        IMAGE_TAG="$ver"
    else
        log "pulling $REGISTRY/$IMAGE_NAME:$VERSION"
        $SUDO docker pull "$REGISTRY/$IMAGE_NAME:$VERSION"
        $SUDO docker tag "$REGISTRY/$IMAGE_NAME:$VERSION" "mygrok:$VERSION"
    fi
}

# ---------------------------------------------------------------------- main ---

main() {
    if [ "$UNINSTALL" -eq 1 ]; then
        preflight
        do_uninstall
        exit 0
    fi

    echo "mygrok server installer (image v$VERSION)"
    preflight
    gather
    handle_conflicts

    if [ "$DRY_RUN" -eq 1 ]; then
        echo
        log "dry run — what would be done:"
        echo "  stage dir   : $STAGE_DIR (compose + rendered nginx.conf + .env + certs/)"
        echo "  domain      : $DOMAIN"
        if [ -n "$IMAGE_TAR" ]; then
            image_desc="tarball $IMAGE_TAR"
        elif [ -n "$REPO_DIR" ]; then
            image_desc="built from $REPO_DIR"
        else
            image_desc="pulled from $REGISTRY/$IMAGE_NAME:$VERSION"
        fi
        echo "  image       : $image_desc"
        echo "  certificate : $CERT_DIR"
        echo "  ports       : 7000/tcp (control), 80+443 (nginx front), 20000-20099 tcp+udp (tunnels)"
        echo "  then        : docker compose up -d + verification"
        exit 0
    fi

    log "staging $STAGE_DIR"
    $SUDO mkdir -p "$STAGE_DIR/certs"
    $SUDO cp /dev/null "$STAGE_DIR/.keep" && $SUDO rm -f "$STAGE_DIR/.keep"   # ensure dir perms

    # Compose file: from a repo checkout if available, else the embedded template.
    if [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/deploy/docker/docker-compose.server.yml" ]; then
        cp "$REPO_DIR/deploy/docker/docker-compose.server.yml" "$STAGE_DIR/docker-compose.yml"
    else
        printf '%s' "$COMPOSE_TEMPLATE" > "$STAGE_DIR/docker-compose.yml"
    fi
    # The compose may carry a `build:` block (repo variant) — strip it: the
    # image is always provided by pull/build/load above. The repo variant also
    # mounts ./nginx-server.conf; we stage the rendered conf as ./nginx.conf.
    sed -i '/^    build:$/,/^      args:$/d; /^        VERSION: ${MYGROK_TAG/d; s|./nginx-server.conf|./nginx.conf|' "$STAGE_DIR/docker-compose.yml"
    if [ -n "${IMAGE_TAG:-}" ]; then
        sed -i "s/\${MYGROK_TAG:-[0-9.]*/\${MYGROK_TAG:-$IMAGE_TAG}/" "$STAGE_DIR/docker-compose.yml"
    else
        sed -i "s/TAG_PLACEHOLDER/$VERSION/" "$STAGE_DIR/docker-compose.yml"
    fi

    # Rendered nginx conf.
    if [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/deploy/docker/nginx-server.conf" ]; then
        cp "$REPO_DIR/deploy/docker/nginx-server.conf" "$STAGE_DIR/nginx.conf"
    else
        printf '%s' "$NGINX_TEMPLATE" > "$STAGE_DIR/nginx.conf"
    fi
    sed -i "s/example\.com/$DOMAIN/g; s/DOMAIN_PLACEHOLDER/$DOMAIN/g" "$STAGE_DIR/nginx.conf"

    # .env (token secret, mode 600) — on upgrades the existing file is kept.
    if [ ! -f "$STAGE_DIR/.env" ]; then
        umask 077
        cat > "$STAGE_DIR/.env" <<EOF
DOMAIN=$DOMAIN
MYGROK_TOKEN=$TOKEN
MYGROK_TAG=$VERSION
EOF
        $SUDO chown "$(id -un)" "$STAGE_DIR/.env" 2>/dev/null || true
    fi
    ok "staged: compose, nginx.conf, .env"

    # Certificates into the stack (kept current on re-runs).
    log "installing certificates from $CERT_DIR"
    $SUDO cp "$CERT_DIR/fullchain.pem" "$CERT_DIR/privkey.pem" "$STAGE_DIR/certs/"

    provide_image

    log "starting the stack"
    $SUDO docker compose --env-file "$STAGE_DIR/.env" -f "$STAGE_DIR/docker-compose.yml" up -d

    # Verify: the TLS front answers (any non-5xx from the front is a success;
    # 404 = "Tunnel Not Found" from mygrok, which means the whole chain works).
    log "verifying (up to 30s)"
    local tries code=""
    for tries in $(seq 1 15); do
        code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 "https://127.0.0.1/" 2>/dev/null || true)"
        case "$code" in 2*|3*|4*) break ;; esac
        sleep 2
    done
    case "$code" in
        2*|3*|4*) ok "front-end answering on 443 (HTTP $code)" ;;
        *) die "front-end not answering (HTTP '${code:-none}') — check: docker compose --env-file $STAGE_DIR/.env -f $STAGE_DIR/docker-compose.yml logs" ;;
    esac
    $SUDO docker compose --env-file "$STAGE_DIR/.env" -f "$STAGE_DIR/docker-compose.yml" ps

    local host_ip
    host_ip="$(curl -s --max-time 3 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')"
    cat <<EOF

$(ok "mygrok server is up on this host")

  Tunnels   : https://<subdomain>.$DOMAIN  (clients dial $( [ -n "$host_ip" ] && echo "$host_ip" || echo '<this host>' ):7000)
  Client    : docker run --rm --network host -e MYGROK_TOKEN=... mygrok:$VERSION \\
                mygrok http 3000 <name> --server $( [ -n "$host_ip" ] && echo "$host_ip" || echo '<this host>' ):7000 --no-tui
  Firewall  : open 7000/tcp, 80, 443, 20000-20099 (tcp+udp); 8080 stays loopback (MYGROK_HTTP_ADDR)
  Logs      : docker compose --env-file $STAGE_DIR/.env -f $STAGE_DIR/docker-compose.yml logs -f
  Upgrade   : re-run this script (token is preserved)
  Uninstall : $0 --uninstall   (add --purge to remove files + image)
  Cert renew: certbot ... -d "*.$DOMAIN" then re-copy into $STAGE_DIR/certs and
              docker compose ... exec nginx nginx -s reload
EOF
}

main "$@"
