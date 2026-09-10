#!/usr/bin/env bash
# install-server.sh -- cross-compile the mygrok server and install it as a
# systemd service on a remote Ubuntu host over SSH (e.g. a VPS).
#
# SUDO_PWD must be set in the environment (used for `sudo -S` remotely; it is
# never stored or committed). All sensitive values come from flags/env only:
# nothing is hardcoded. The auth token is written to a 0600 EnvironmentFile and
# is NOT placed on the ExecStart command line, so it never shows up in `ps`.
#
# Idempotent: safe to re-run to upgrade the binary in place (backs up the old
# binary first). Existing service is stopped, swapped, and restarted.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_NAME="mygrok-server"
UNIT_TEMPLATE="$REPO_ROOT/systemd/mygrok.service"

SSH_HOST=""
REMOTE_USER=""
DOMAIN=""
TOKEN="${MYGROK_TOKEN:-}"
BINDIR=""
UNIT_NAME="mygrok"
ENV_DIR="/etc/mygrok"
EXTRA_ARGS=""
ADMIN=""
ADMIN_TOKEN="${MYGROK_ADMIN_TOKEN:-}"
CONTROL=""
HTTP=""

usage() {
    cat <<USAGE
Usage: $(basename "$0") --host <ssh-alias> --user <u> --domain <base-domain> \\
  --token <t> [options]

Builds and installs the mygrok server as a systemd service on a remote Ubuntu
host over SSH. The SUDO_PWD environment variable must be set.

Required:
  --host <ssh-alias>     SSH alias/host of the target server
  --user <u>             Unix user that runs the service (systemd User=)
  --domain <base>        Base domain for tunnels (e.g. example.com)
  --token <t>            auth token (or set MYGROK_TOKEN in env)

Optional:
  --bindir <dir>         directory for the binary (default: /home/<user>)
  --unit-name <name>     systemd unit name (default: mygrok)
  --control <addr>       control listener (e.g. :7000)
  --http <addr>          public HTTP proxy listener (e.g. :8080)
  --admin <addr>         admin/stats listener (default server-side 127.0.0.1:7001; "off" to disable)
  --admin-token <t>      token for non-loopback /stats access (or MYGROK_ADMIN_TOKEN env)
  --extra-args "<args>"  raw extra flags appended to ExecStart

Example:
  SUDO_PWD='...' $(basename "$0") --host your-vps.com --user your-user \\
    --domain example.com --token "\$MYGROK_TOKEN"
USAGE
}

die() { echo "ERROR: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host)        SSH_HOST="${2:?--host requires a value}"; shift 2 ;;
        --user)        REMOTE_USER="${2:?--user requires a value}"; shift 2 ;;
        --domain)      DOMAIN="${2:?--domain requires a value}"; shift 2 ;;
        --token)       TOKEN="${2:?--token requires a value}"; shift 2 ;;
        --bindir)      BINDIR="${2:?--bindir requires a value}"; shift 2 ;;
        --unit-name)   UNIT_NAME="${2:?--unit-name requires a value}"; shift 2 ;;
        --control)     CONTROL="${2:?--control requires a value}"; shift 2 ;;
        --http)        HTTP="${2:?--http requires a value}"; shift 2 ;;
        --admin)       ADMIN="${2:?--admin requires a value}"; shift 2 ;;
        --admin-token) ADMIN_TOKEN="${2:?--admin-token requires a value}"; shift 2 ;;
        --extra-args)  EXTRA_ARGS="${2:?--extra-args requires a value}"; shift 2 ;;
        -h|--help)     usage; exit 0 ;;
        *)             die "unknown flag: $1" ;;
    esac
done

[[ -n "$SSH_HOST" ]]    || die "missing required flag --host"
[[ -n "$REMOTE_USER" ]] || die "missing required flag --user"
[[ -n "$DOMAIN" ]]      || die "missing required flag --domain"
[[ -n "$TOKEN" ]]       || die "missing --token (or set MYGROK_TOKEN)"
[[ -n "${SUDO_PWD:-}" ]] || die "SUDO_PWD environment variable is not set (required for remote sudo)"
[[ -f "$UNIT_TEMPLATE" ]] || die "unit template not found: $UNIT_TEMPLATE"

[[ -n "$BINDIR" ]] || BINDIR="/home/$REMOTE_USER"
ENV_FILE="$ENV_DIR/$UNIT_NAME.env"

# Assemble ExecStart extra args from the listener flags (token is NOT here).
ARGS="$EXTRA_ARGS"
[[ -n "$CONTROL" ]] && ARGS="$ARGS -control $CONTROL"
[[ -n "$HTTP" ]]    && ARGS="$ARGS -http $HTTP"
[[ -n "$ADMIN" ]]   && ARGS="$ARGS -admin $ADMIN"
ARGS="$(echo "$ARGS" | xargs || true)"  # trim/collapse whitespace

# sudo helper (runs a remote command as root, feeding the password on stdin).
sudoc() { ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S -p '' bash -c $(printf '%q' "$1")"; }

echo "==> Building $BIN_NAME (linux/amd64, v$(cat "$REPO_ROOT/VERSION"))..."
(
    cd "$REPO_ROOT"
    mkdir -p bin
    GOOS=linux GOARCH=amd64 go build \
        -ldflags "-X github.com/veloriba/mygrok/internal/version.Version=$(cat VERSION)" \
        -o "bin/$BIN_NAME" cmd/server/main.go
)

echo "==> Uploading to $SSH_HOST:$BINDIR/$BIN_NAME.new ..."
ssh "$SSH_HOST" "mkdir -p '$BINDIR'"
scp "$REPO_ROOT/bin/$BIN_NAME" "$SSH_HOST:$BINDIR/$BIN_NAME.new"
ssh "$SSH_HOST" "chmod +x '$BINDIR/$BIN_NAME.new' && '$BINDIR/$BIN_NAME.new' -h >/dev/null 2>&1" \
    || die "uploaded binary failed to execute on $SSH_HOST (wrong arch?)"

echo "==> Backing up existing binary (if any) and stopping service (if present)..."
ssh "$SSH_HOST" "
    TS=\$(date +%Y%m%d-%H%M%S)
    if [ -f '$BINDIR/$BIN_NAME' ]; then
        cp -a '$BINDIR/$BIN_NAME' '$BINDIR/$BIN_NAME.bak-\$TS'
        echo '    backup: $BINDIR/$BIN_NAME.bak-\$TS'
    fi
    if systemctl list-unit-files '$UNIT_NAME.service' >/dev/null 2>&1 && systemctl cat '$UNIT_NAME.service' >/dev/null 2>&1; then
        echo \"$SUDO_PWD\" | sudo -S -p '' systemctl stop '$UNIT_NAME' || true
    fi
"

echo "==> Swapping binary into place..."
ssh "$SSH_HOST" "mv -f '$BINDIR/$BIN_NAME.new' '$BINDIR/$BIN_NAME' && chmod 0755 '$BINDIR/$BIN_NAME'"

echo "==> Writing EnvironmentFile $ENV_FILE (mode 0600)..."
ENV_CONTENT="MYGROK_TOKEN=$(printf '%s' "$TOKEN")"
[[ -n "$ADMIN_TOKEN" ]] && ENV_CONTENT="$ENV_CONTENT
MYGROK_ADMIN_TOKEN=$(printf '%s' "$ADMIN_TOKEN")"
STAGE_ENV="$BINDIR/.$UNIT_NAME.env.tmp"
ssh "$SSH_HOST" "cat > '$STAGE_ENV'" <<<"$ENV_CONTENT"
sudoc "install -d -m 0750 -o '$REMOTE_USER' -g '$REMOTE_USER' '$ENV_DIR' && \
    install -m 0600 '$STAGE_ENV' '$ENV_FILE' && rm -f '$STAGE_ENV'"

echo "==> Installing systemd unit /etc/systemd/system/$UNIT_NAME.service ..."
UNIT="$(cat "$UNIT_TEMPLATE")"
UNIT="${UNIT//"{{USER}}"/$REMOTE_USER}"
UNIT="${UNIT//"{{DOMAIN}}"/$DOMAIN}"
UNIT="${UNIT//"{{BINDIR}}"/$BINDIR}"
UNIT="${UNIT//"{{ENV_FILE}}"/$ENV_FILE}"
UNIT="${UNIT//"{{EXTRA_ARGS}}"/$ARGS}"
grep -q '{{' <<<"$UNIT" && die "unreplaced template placeholder(s) remain in rendered unit"
[[ -n "$UNIT" ]] || die "rendered unit is empty"

STAGE_UNIT="$BINDIR/.$UNIT_NAME.service.tmp"
ssh "$SSH_HOST" "cat > '$STAGE_UNIT'" <<<"$UNIT"
sudoc "[ -s '$STAGE_UNIT' ] || { echo 'ERROR: staged unit file is empty' >&2; exit 1; } && \
    cp '$STAGE_UNIT' '/etc/systemd/system/$UNIT_NAME.service' && \
    chmod 0644 '/etc/systemd/system/$UNIT_NAME.service' && rm -f '$STAGE_UNIT'"

echo "==> Enabling and starting $UNIT_NAME..."
sudoc "systemctl daemon-reload && systemctl enable --now '$UNIT_NAME'"

STATE="$(ssh "$SSH_HOST" "systemctl is-active '$UNIT_NAME' 2>/dev/null || true")"
[[ "$STATE" == "active" ]] || echo "WARNING: service state is '$STATE' — check: ssh $SSH_HOST journalctl -u $UNIT_NAME -n 50"

echo ""
echo "==> Done. mygrok server installed on $SSH_HOST"
echo "    unit      : $UNIT_NAME.service ($STATE)"
echo "    binary    : $BINDIR/$BIN_NAME"
echo "    domain    : $DOMAIN"
echo "    env file  : $ENV_FILE (token stored here, mode 0600)"
[[ -n "$ARGS" ]] && echo "    extra args: $ARGS"
echo "    logs      : ssh \"$SSH_HOST\" journalctl -u $UNIT_NAME -f"
echo "    stats     : ssh -N -L 7001:127.0.0.1:7001 $SSH_HOST  # then curl http://127.0.0.1:7001/stats"
