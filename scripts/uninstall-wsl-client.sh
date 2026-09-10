#!/usr/bin/env bash
# uninstall-wsl-client.sh -- reverse of install-wsl-client.sh: delete the
# Windows scheduled task 'mygrok-wsl' and remove the watchdog + client binary
# from the WSL distro.
#
# No secrets are involved; SUDO_PWD is not required (all files live in the
# WSL user's home, and deleting a logon task for the current Windows user
# does not need elevation).

set -euo pipefail

BIN_NAME="mygrok-client"
WATCHDOG_NAME="mygrok-watchdog.sh"
SCHEDULED_TASK="mygrok-wsl"

WIN_HOST=""
WSL_HOST=""
WSL_BINDIR=""

usage() {
    cat <<USAGE
Usage: $(basename "$0") --win-host <alias> --wsl-host <alias> [options]

Removes the WSL2 mygrok deployment created by install-wsl-client.sh:
  - deletes the Windows scheduled task 'mygrok-wsl'
  - kills any running watchdog/client inside WSL
  - removes <wsl-bindir>/mygrok-watchdog.sh and <wsl-bindir>/mygrok-client

Required:
  --win-host <alias>   SSH alias of the Windows host (cmd shell, schtasks)
  --wsl-host <alias>   SSH alias that lands inside the WSL distro

Optional:
  --wsl-bindir <dir>   directory inside WSL used at install time
                       (default: /home/<login user>)

Example:
  $(basename "$0") --win-host winhost --wsl-host winhost-wsl
USAGE
}

die() { echo "ERROR: $*" >&2; usage >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --win-host)   WIN_HOST="${2:?--win-host requires a value}"; shift 2 ;;
        --wsl-host)   WSL_HOST="${2:?--wsl-host requires a value}"; shift 2 ;;
        --wsl-bindir) WSL_BINDIR="${2:?--wsl-bindir requires a value}"; shift 2 ;;
        -h|--help)    usage; exit 0 ;;
        *)            die "unknown flag: $1" ;;
    esac
done

[[ -n "$WIN_HOST" ]] || die "missing required flag --win-host"
[[ -n "$WSL_HOST" ]] || die "missing required flag --wsl-host"

if [[ -z "$WSL_BINDIR" ]]; then
    WSL_BINDIR="/home/$(ssh "$WSL_HOST" 'whoami')"
fi

echo "==> Deleting Windows scheduled task '$SCHEDULED_TASK' on $WIN_HOST..."
ssh "$WIN_HOST" "schtasks /delete /tn $SCHEDULED_TASK /f 2>nul" \
    || echo "    note: could not delete task '$SCHEDULED_TASK' (it may not exist)"

echo "==> Stopping watchdog/client and removing files in WSL ($WSL_BINDIR)..."
ssh "$WSL_HOST" "pkill -f mygrok-watchdog.sh 2>/dev/null || true; pkill -f mygrok-client 2>/dev/null || true"
ssh "$WSL_HOST" "rm -f '$WSL_BINDIR/$WATCHDOG_NAME' '$WSL_BINDIR/$BIN_NAME'"

echo ""
echo "==> Done. WSL2 mygrok deployment removed."
echo "    task   : $SCHEDULED_TASK deleted on $WIN_HOST"
echo "    files  : $WSL_BINDIR/$WATCHDOG_NAME and $WSL_BINDIR/$BIN_NAME removed from WSL"
