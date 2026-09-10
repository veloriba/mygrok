#!/usr/bin/env bash
# uninstall-client.sh -- remove the mygrok client systemd service that was
# installed by install-client.sh from a remote Linux host over SSH.
#
# Requires SUDO_PWD to be set in the environment (used for `sudo -S` remotely;
# it is never stored or committed). No other secrets are involved.

set -euo pipefail

BIN_NAME="mygrok-client"

SSH_HOST=""
SUBDOMAIN=""
BINDIR=""
REMOVE_BINARY=0

usage() {
    cat <<USAGE
Usage: $(basename "$0") --host <ssh-alias> --subdomain <s> [options]

Removes the systemd service mygrok-<subdomain> from a remote Linux host.
The SUDO_PWD environment variable must be set.

Required:
  --host <ssh-alias>     SSH alias/host of the target machine
  --subdomain <s>        subdomain used at install time (unit name suffix)

Optional:
  --bindir <dir>      directory holding the client binary (default: /home/<login user>)
  --remove-binary     also delete $BINDIR/mygrok-client from the host
  -h, --help          show this help

Example:
  SUDO_PWD='...' $(basename "$0") --host your-host --subdomain ssh --remove-binary
USAGE
}

die() { echo "ERROR: $*" >&2; usage >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host)          SSH_HOST="${2:?--host requires a value}"; shift 2 ;;
        --subdomain)     SUBDOMAIN="${2:?--subdomain requires a value}"; shift 2 ;;
        --bindir)        BINDIR="${2:?--bindir requires a value}"; shift 2 ;;
        --remove-binary) REMOVE_BINARY=1; shift ;;
        -h|--help)       usage; exit 0 ;;
        *)               die "unknown flag: $1" ;;
    esac
done

[[ -n "$SSH_HOST" ]]    || die "missing required flag --host"
[[ -n "$SUBDOMAIN" ]]   || die "missing required flag --subdomain"
[[ -n "${SUDO_PWD:-}" ]] || die "SUDO_PWD environment variable is not set (required for remote sudo)"

UNIT_NAME="mygrok-$SUBDOMAIN"

echo "==> Disabling and stopping $UNIT_NAME on $SSH_HOST..."
ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S systemctl disable --now $UNIT_NAME 2>/dev/null || true"

echo "==> Removing /etc/systemd/system/$UNIT_NAME.service ..."
ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S rm -f /etc/systemd/system/$UNIT_NAME.service"
ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S systemctl daemon-reload"

if [[ "$REMOVE_BINARY" -eq 1 ]]; then
    if [[ -z "$BINDIR" ]]; then
        BINDIR="/home/$(ssh "$SSH_HOST" 'whoami')"
    fi
    echo "==> Removing $SSH_HOST:$BINDIR/$BIN_NAME ..."
    ssh "$SSH_HOST" "pkill -f mygrok-client 2>/dev/null || true; rm -f '$BINDIR/$BIN_NAME'"
fi

echo ""
echo "==> Done. $UNIT_NAME removed from $SSH_HOST."
if [[ "$REMOVE_BINARY" -eq 1 ]]; then
    echo "    binary also deleted: $BINDIR/$BIN_NAME"
else
    [[ -n "$BINDIR" ]] || BINDIR="/home/$(ssh "$SSH_HOST" 'whoami')"
    echo "    binary kept at $BINDIR/$BIN_NAME (delete it with --remove-binary)"
fi
