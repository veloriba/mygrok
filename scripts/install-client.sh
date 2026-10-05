#!/usr/bin/env bash
# install-client.sh -- cross-compile the mygrok client and install it as a
# systemd service on a remote Linux host over SSH (e.g. a home NUC).
#
# Requires SUDO_PWD to be set in the environment (used for `sudo -S` remotely;
# it is never stored or committed). All sensitive values come from flags only:
# nothing is hardcoded, and the token lands in the unit's Environment= line,
# never on the ExecStart command line.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_NAME="mygrok-client"
UNIT_TEMPLATE="$REPO_ROOT/systemd/mygrok-client.service"

SSH_HOST=""
REMOTE_USER=""
SERVER=""
TOKEN=""
SUBDOMAIN=""
PORT=""
PROTOCOL="http"
PUBLIC_PORT=0
BINDIR=""

usage() {
    cat <<USAGE
Usage: $(basename "$0") --host <ssh-alias> --user <u> --server <addr:port> \
  --token <t> --subdomain <s> --port <n> [options]

Installs the mygrok client as a systemd service (mygrok-<subdomain>) on a
remote Linux host. The SUDO_PWD environment variable must be set.

Required:
  --host <ssh-alias>     SSH alias/host of the target machine
  --user <u>             Unix user that runs the client (systemd User=)
  --server <addr:port>   mygrok server address (e.g. example.com:7000)
  --token <t>            authentication token (never hardcoded)
  --subdomain <s>        tunnel subdomain
  --port <n>             local port to expose

Optional:
  --protocol http|tcp|udp   tunnel protocol (default: http)
  --public-port <n>         requested public port for tcp/udp, 0 = auto (default: 0)
  --bindir <dir>            directory holding the client binary (default: /home/<user>)

Example:
  SUDO_PWD='...' $(basename "$0") --host your-host --user veloriba \
    --server my.example.com:7000 --token "\$MYGROK_TOKEN" \
    --subdomain ssh --port 22 --protocol tcp
USAGE
}

die() { echo "ERROR: $*" >&2; usage >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host)         SSH_HOST="${2:?--host requires a value}"; shift 2 ;;
        --user)         REMOTE_USER="${2:?--user requires a value}"; shift 2 ;;
        --server)       SERVER="${2:?--server requires a value}"; shift 2 ;;
        --token)        TOKEN="${2:?--token requires a value}"; shift 2 ;;
        --subdomain)    SUBDOMAIN="${2:?--subdomain requires a value}"; shift 2 ;;
        --port)         PORT="${2:?--port requires a value}"; shift 2 ;;
        --protocol)     PROTOCOL="${2:?--protocol requires a value}"; shift 2 ;;
        --public-port)  PUBLIC_PORT="${2:?--public-port requires a value}"; shift 2 ;;
        --bindir)       BINDIR="${2:?--bindir requires a value}"; shift 2 ;;
        -h|--help)      usage; exit 0 ;;
        *)              die "unknown flag: $1" ;;
    esac
done

[[ -n "$SSH_HOST" ]]    || die "missing required flag --host"
[[ -n "$REMOTE_USER" ]] || die "missing required flag --user"
[[ -n "$SERVER" ]]      || die "missing required flag --server"
[[ -n "$TOKEN" ]]       || die "missing required flag --token"
[[ -n "$SUBDOMAIN" ]]   || die "missing required flag --subdomain"
[[ -n "$PORT" ]]        || die "missing required flag --port"
case "$PROTOCOL" in http|tcp|udp) ;; *) die "--protocol must be one of: http, tcp, udp" ;; esac
[[ "$PORT" =~ ^[0-9]+$ ]]          || die "--port must be a number"
[[ "$PUBLIC_PORT" =~ ^[0-9]+$ ]]   || die "--public-port must be a number"
[[ -n "${SUDO_PWD:-}" ]]           || die "SUDO_PWD environment variable is not set (required for remote sudo)"

UNIT_NAME="mygrok-$SUBDOMAIN"
[[ -n "$BINDIR" ]] || BINDIR="/home/$REMOTE_USER"
[[ -f "$UNIT_TEMPLATE" ]] || die "unit template not found: $UNIT_TEMPLATE"

echo "==> Building $BIN_NAME (linux/amd64, v$(cat "$REPO_ROOT/VERSION"))..."
(
    cd "$REPO_ROOT"
    mkdir -p bin
    GOOS=linux GOARCH=amd64 go build \
        -ldflags "-X github.com/veloriba/mygrok/internal/version.Version=$(cat VERSION)" \
        -o "bin/$BIN_NAME" ./cmd/client
)

echo "==> Stopping any running $BIN_NAME on $SSH_HOST..."
# bracket trick: pkill's own remote shell cmdline must not match, or the wrapper dies mid-run
ssh "$SSH_HOST" "pkill -f 'mygrok-cl[i]ent' || true"

echo "==> Uploading binary to $SSH_HOST:$BINDIR/$BIN_NAME ..."
ssh "$SSH_HOST" "mkdir -p '$BINDIR'"
scp "$REPO_ROOT/bin/$BIN_NAME" "$SSH_HOST:$BINDIR/$BIN_NAME"

UNIT="$(cat "$UNIT_TEMPLATE")"
UNIT="${UNIT//"{{USER}}"/$REMOTE_USER}"
UNIT="${UNIT//"{{SERVER}}"/$SERVER}"
UNIT="${UNIT//"{{TOKEN}}"/$TOKEN}"
UNIT="${UNIT//"{{PORT}}"/$PORT}"
UNIT="${UNIT//"{{SUBDOMAIN}}"/$SUBDOMAIN}"
UNIT="${UNIT//"{{BINDIR}}"/$BINDIR}"
UNIT="${UNIT//"{{PROTOCOL}}"/$PROTOCOL}"
UNIT="${UNIT//"{{PUBLIC_PORT}}"/$PUBLIC_PORT}"
if grep -q '{{' <<<"$UNIT"; then
    die "unreplaced template token(s) remain in rendered unit"
fi

[[ -n "$UNIT" ]] || die "rendered unit is empty"

# stage as the user via ssh stdin, then install with sudo (a single
# "echo $PWD | sudo tee file" pipeline never reaches tee: its stdin is echo)
echo "==> Installing systemd unit /etc/systemd/system/$UNIT_NAME.service ..."
STAGE="$BINDIR/.$(basename "$UNIT_NAME").service.tmp"
ssh "$SSH_HOST" "cat > '$STAGE'" <<<"$UNIT"
ssh "$SSH_HOST" "[ -s '$STAGE' ] || { echo 'ERROR: staged unit file is empty' >&2; exit 1; } && \
    echo \"$SUDO_PWD\" | sudo -S cp '$STAGE' /etc/systemd/system/$UNIT_NAME.service && \
    echo \"$SUDO_PWD\" | sudo -S chmod 0644 /etc/systemd/system/$UNIT_NAME.service"
ssh "$SSH_HOST" "rm -f '$STAGE'"

echo "==> Enabling and starting $UNIT_NAME..."
ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S systemctl daemon-reload && echo '$SUDO_PWD' | sudo -S systemctl enable --now $UNIT_NAME"

STATE="$(ssh "$SSH_HOST" "systemctl is-active $UNIT_NAME 2>/dev/null || true")"
if [[ "$STATE" == "active" ]]; then
    ACTIVE="active"
else
    ACTIVE="$STATE (still starting? check logs)"
fi

echo ""
echo "==> Done. mygrok client installed on $SSH_HOST"
echo "    unit     : $UNIT_NAME.service ($ACTIVE)"
echo "    binary   : $BINDIR/$BIN_NAME"
if [[ "$PUBLIC_PORT" -gt 0 ]]; then PP="requested $PUBLIC_PORT"; else PP="auto-assign"; fi
echo "    tunnel   : $PROTOCOL 127.0.0.1:$PORT -> subdomain '$SUBDOMAIN' (public port: $PP)"
echo "    logs     : ssh \"$SSH_HOST\" journalctl -u $UNIT_NAME -f"
