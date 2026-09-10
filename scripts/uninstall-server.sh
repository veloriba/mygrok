#!/usr/bin/env bash
# uninstall-server.sh -- remove the mygrok server systemd service from a remote
# Ubuntu host over SSH. By default it leaves the binary in place (so you can
# reinstall or roll back); pass --purge to remove the binary and env file too.
#
# SUDO_PWD must be set in the environment (used for `sudo -S` remotely).

set -euo pipefail

SSH_HOST=""
UNIT_NAME="mygrok"
BIN_NAME="mygrok-server"
BINDIR=""
REMOTE_USER=""
PURGE=0
KEEP_BACKUPS=1

usage() {
    cat <<USAGE
Usage: $(basename "$0") --host <ssh-alias> [options]

Stops and disables the mygrok server systemd service on a remote Ubuntu host.

Required:
  --host <ssh-alias>     SSH alias/host of the target server

Optional:
  --user <u>             Unix user (used to locate default --bindir)
  --bindir <dir>         directory holding the binary (default: /home/<user>)
  --unit-name <name>     systemd unit name (default: mygrok)
  --purge                also remove the binary and EnvironmentFile
  --purge-backups        also remove .bak-* binaries (default: keep)
  -h, --help             show this help

Example:
  SUDO_PWD='...' $(basename "$0") --host your-vps.com --user your-user --purge
USAGE
}

die() { echo "ERROR: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host)          SSH_HOST="${2:?--host requires a value}"; shift 2 ;;
        --user)          REMOTE_USER="${2:?--user requires a value}"; shift 2 ;;
        --bindir)        BINDIR="${2:?--bindir requires a value}"; shift 2 ;;
        --unit-name)     UNIT_NAME="${2:?--unit-name requires a value}"; shift 2 ;;
        --purge)         PURGE=1; shift ;;
        --purge-backups) KEEP_BACKUPS=0; shift ;;
        -h|--help)       usage; exit 0 ;;
        *)               die "unknown flag: $1" ;;
    esac
done

[[ -n "$SSH_HOST" ]] || die "missing required flag --host"
[[ -n "${SUDO_PWD:-}" ]] || die "SUDO_PWD environment variable is not set (required for remote sudo)"
if [[ -z "$BINDIR" ]]; then
    [[ -n "$REMOTE_USER" ]] || die "provide --user or --bindir"
    BINDIR="/home/$REMOTE_USER"
fi

sudoc() { ssh "$SSH_HOST" "echo \"$SUDO_PWD\" | sudo -S -p '' bash -c $(printf '%q' "$1")"; }

echo "==> Stopping and disabling $UNIT_NAME.service ..."
sudoc "systemctl stop '$UNIT_NAME' 2>/dev/null || true; systemctl disable '$UNIT_NAME' 2>/dev/null || true"

echo "==> Removing unit file /etc/systemd/system/$UNIT_NAME.service ..."
sudoc "rm -f '/etc/systemd/system/$UNIT_NAME.service' && systemctl daemon-reload"

if [[ "$PURGE" -eq 1 ]]; then
    echo "==> Purging binary and EnvironmentFile ..."
    ssh "$SSH_HOST" "rm -f '$BINDIR/$BIN_NAME'"
    sudoc "rm -f '/etc/mygrok/$UNIT_NAME.env'"
    if [[ "$KEEP_BACKUPS" -eq 0 ]]; then
        ssh "$SSH_HOST" "rm -f '$BINDIR/$BIN_NAME.bak-'*"
    fi
else
    echo "==> Leaving $BINDIR/$BIN_NAME in place (use --purge to remove)."
fi

echo ""
echo "==> Uninstalled mygrok server from $SSH_HOST."
echo "    (env dir /etc/mygrok left in place unless --purge)"
