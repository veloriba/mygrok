#!/usr/bin/env bash
# install-wsl-client.sh -- cross-compile the mygrok client and deploy it to a
# WSL2 Ubuntu distro on a Windows host: binary + watchdog loop inside WSL,
# plus a Windows scheduled task (on logon) that boots the distro and starts
# the watchdog. The watchdog also keeps WSL sshd up on a best-effort basis.
#
# No secrets or hosts are hardcoded; everything comes from flags. SUDO_PWD is
# NOT needed by this script: WSL file writes go to the user's home (no sudo),
# and the Windows scheduled task uses /ru <windows-user> instead of sudo. The
# watchdog exports MYGROK_SERVER/MYGROK_TOKEN as env vars so the token never
# sits on a command line, and its `service ssh start` is best-effort (it
# silently no-ops if it lacks permission).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_NAME="mygrok-client"
WATCHDOG_NAME="mygrok-watchdog.sh"
SCHEDULED_TASK="mygrok-wsl"

WIN_HOST=""
WSL_HOST=""
DISTRO="Ubuntu-24.04"
WSL_USER="veloriba"
WINDOWS_USER="velor"
SERVER=""
TOKEN=""
SUBDOMAIN=""
PORT=""
PROTOCOL="tcp"
PUBLIC_PORT=0
WSL_BINDIR=""

usage() {
    cat <<USAGE
Usage: $(basename "$0") --win-host <alias> --wsl-host <alias> \
  --server <addr:port> --token <t> --subdomain <s> --port <n> [options]

Deploys the mygrok client to a WSL2 distro on a Windows host:
  - uploads the linux client binary into WSL (<wsl-bindir>/mygrok-client)
  - writes a watchdog loop script into WSL (<wsl-bindir>/mygrok-watchdog.sh)
    that keeps sshd up and restarts the client every 5s if it exits
  - creates the Windows scheduled task 'mygrok-wsl' (on logon) which runs:
      wsl.exe -d <distro> -u <wsl-user> -- <wsl-bindir>/mygrok-watchdog.sh

Required:
  --win-host <alias>     SSH alias of the Windows host (cmd shell, schtasks)
  --wsl-host <alias>     SSH alias that lands inside the WSL distro
  --server <addr:port>   mygrok server address (e.g. example.com:7000)
  --token <t>            authentication token (never hardcoded)
  --subdomain <s>        tunnel subdomain
  --port <n>             local port to expose inside WSL

Optional:
  --distro <name>       WSL distro name (default: Ubuntu-24.04)
  --wsl-user <u>        Unix user inside WSL (default: veloriba)
  --windows-user <u>    Windows logon user for the task (default: velor)
  --protocol tcp|udp    tunnel protocol (default: tcp)
  --public-port <n>     requested public port, 0 = auto (default: 0)
  --wsl-bindir <dir>    directory inside WSL for binary/watchdog (default: /home/<wsl-user>)

Example:
  $(basename "$0") --win-host winhost --wsl-host winhost-wsl \
    --server my.example.com:7000 --token "\$MYGROK_TOKEN" \
    --subdomain ssh --port 22 --protocol tcp
USAGE
}

die() { echo "ERROR: $*" >&2; usage >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --win-host)      WIN_HOST="${2:?--win-host requires a value}"; shift 2 ;;
        --wsl-host)      WSL_HOST="${2:?--wsl-host requires a value}"; shift 2 ;;
        --distro)        DISTRO="${2:?--distro requires a value}"; shift 2 ;;
        --wsl-user)      WSL_USER="${2:?--wsl-user requires a value}"; shift 2 ;;
        --windows-user)  WINDOWS_USER="${2:?--windows-user requires a value}"; shift 2 ;;
        --server)        SERVER="${2:?--server requires a value}"; shift 2 ;;
        --token)         TOKEN="${2:?--token requires a value}"; shift 2 ;;
        --subdomain)     SUBDOMAIN="${2:?--subdomain requires a value}"; shift 2 ;;
        --port)          PORT="${2:?--port requires a value}"; shift 2 ;;
        --protocol)      PROTOCOL="${2:?--protocol requires a value}"; shift 2 ;;
        --public-port)   PUBLIC_PORT="${2:?--public-port requires a value}"; shift 2 ;;
        --wsl-bindir)    WSL_BINDIR="${2:?--wsl-bindir requires a value}"; shift 2 ;;
        -h|--help)       usage; exit 0 ;;
        *)               die "unknown flag: $1" ;;
    esac
done

[[ -n "$WIN_HOST" ]]     || die "missing required flag --win-host"
[[ -n "$WSL_HOST" ]]     || die "missing required flag --wsl-host"
[[ -n "$SERVER" ]]       || die "missing required flag --server"
[[ -n "$TOKEN" ]]        || die "missing required flag --token"
[[ -n "$SUBDOMAIN" ]]    || die "missing required flag --subdomain"
[[ -n "$PORT" ]]         || die "missing required flag --port"
case "$PROTOCOL" in tcp|udp) ;; *) die "--protocol must be one of: tcp, udp" ;; esac
[[ "$PORT" =~ ^[0-9]+$ ]]        || die "--port must be a number"
[[ "$PUBLIC_PORT" =~ ^[0-9]+$ ]] || die "--public-port must be a number"

[[ -n "$WSL_BINDIR" ]] || WSL_BINDIR="/home/$WSL_USER"
WATCHDOG_PATH="$WSL_BINDIR/$WATCHDOG_NAME"

# Values below are interpolated unquoted into the schtasks action string (which
# is itself one quoted blob on the Windows side) or baked into a generated bash
# script; reject anything that would break that quoting instead of shipping a
# silently-broken deploy.
[[ ! "$DISTRO" =~ [[:space:]] ]]     || die "--distro must not contain spaces (schtasks action string)"
[[ ! "$WSL_USER" =~ [[:space:]] ]]   || die "--wsl-user must not contain spaces (schtasks action string)"
[[ ! "$WSL_BINDIR" =~ [[:space:]] ]] || die "--wsl-bindir must not contain spaces (watchdog path in schtasks action string)"
case "$TOKEN"     in *\"*|*\$*|*\\*) die "--token may not contain quotes, \$ or backslashes (it is baked into the watchdog script)" ;; esac
case "$SUBDOMAIN" in *\"*|*\$*|*\\*) die "--subdomain may not contain quotes, \$ or backslashes (it is baked into the watchdog script)" ;; esac

echo "==> Building $BIN_NAME (linux/amd64, v$(cat "$REPO_ROOT/VERSION"))..."
(
    cd "$REPO_ROOT"
    mkdir -p bin
    GOOS=linux GOARCH=amd64 go build \
        -ldflags "-X github.com/veloriba/mygrok/internal/version.Version=$(cat VERSION)" \
        -o "bin/$BIN_NAME" cmd/client/main.go
)

echo "==> Stopping any running watchdog/client on $WSL_HOST..."
# Bracket trick: pkill's own remote shell cmdline contains this very command,
# so a plain 'pkill -f mygrok-watchdog.sh' would kill the wrapper itself. Kill
# the watchdog first so it cannot respawn the client between the two pkills;
# failures are non-fatal (a fresh distro has nothing to stop).
ssh "$WSL_HOST" "pkill -f 'mygrok-watchdog.s[h]' || true; pkill -f 'mygrok-cl[i]ent' || true"

echo "==> Uploading binary to WSL ($WSL_HOST:$WSL_BINDIR/$BIN_NAME)..."
ssh "$WSL_HOST" "mkdir -p '$WSL_BINDIR'"
scp "$REPO_ROOT/bin/$BIN_NAME" "$WSL_HOST:$WSL_BINDIR/$BIN_NAME"

WATCHDOG_CONTENT="$(cat <<EOF
#!/usr/bin/env bash
# mygrok client watchdog (WSL2). Keeps sshd up and restarts the mygrok
# client whenever it exits. Started by the Windows scheduled task
# '$SCHEDULED_TASK' on user logon. Server and token are exported as
# environment variables (MYGROK_SERVER / MYGROK_TOKEN) so the token never
# appears on a command line, matching how the systemd unit delivers them.
set -u

BINDIR="$WSL_BINDIR"
PROTOCOL="$PROTOCOL"
PORT="$PORT"
SUBDOMAIN="$SUBDOMAIN"
PUBLIC_PORT="$PUBLIC_PORT"
export MYGROK_SERVER="$SERVER"
export MYGROK_TOKEN="$TOKEN"

ensure_sshd() {
    pgrep -x sshd >/dev/null 2>&1 || service ssh start >/dev/null 2>&1 || true
}

ensure_sshd
while true; do
    ensure_sshd
    "\$BINDIR/mygrok-client" --no-tui "\$PROTOCOL" "\$PORT" "\$SUBDOMAIN" --public-port "\$PUBLIC_PORT"
    sleep 5
done
EOF
)"

echo "==> Writing watchdog to WSL ($WSL_HOST:$WATCHDOG_PATH)..."
[[ -n "$WATCHDOG_CONTENT" ]] || die "watchdog content rendered empty"
# Content travels over ssh stdin straight into 'cat' -- no sudo/tee in the pipe,
# so nothing can swallow it; then verify remotely that the file is non-empty.
ssh "$WSL_HOST" "cat > '$WATCHDOG_PATH'" <<<"$WATCHDOG_CONTENT"
ssh "$WSL_HOST" "[ -s '$WATCHDOG_PATH' ] || { echo 'ERROR: watchdog file is empty on $WSL_HOST (remote write failed?)' >&2; exit 1; }"
ssh "$WSL_HOST" "chmod +x '$WATCHDOG_PATH' '$WSL_BINDIR/$BIN_NAME'"

echo "==> Creating Windows scheduled task '$SCHEDULED_TASK' on $WIN_HOST..."
# Two separate ssh calls: the remote shell is cmd.exe, which does NOT treat ';'
# as a command separator (only '&' does), so one compound line would arrive as
# a single mangled schtasks invocation. Delete first for idempotent re-runs;
# tolerate "task not found" locally (first run) -- we rely on the local '||' ,
# not on the remote '2>nul', for correctness.
ssh "$WIN_HOST" "schtasks /delete /tn $SCHEDULED_TASK /f 2>nul" || true
ssh "$WIN_HOST" \
    "schtasks /create /tn $SCHEDULED_TASK /tr \"wsl.exe -d $DISTRO -u $WSL_USER -- $WATCHDOG_PATH\" /sc onlogon /ru \"$WINDOWS_USER\"" \
    || die "failed to create Windows scheduled task '$SCHEDULED_TASK' on $WIN_HOST"

echo ""
echo "==> Done. mygrok client deployed to WSL2."
echo "    binary   : $WATCHDOG_PATH (and $WSL_BINDIR/$BIN_NAME) in distro '$DISTRO'"
if [[ "$PUBLIC_PORT" -gt 0 ]]; then PP="requested $PUBLIC_PORT"; else PP="auto-assign"; fi
echo "    tunnel   : $PROTOCOL 127.0.0.1:$PORT -> subdomain '$SUBDOMAIN' (public port: $PP)"
echo "    task     : $SCHEDULED_TASK on $WIN_HOST (runs at Windows logon as $WINDOWS_USER)"
echo ""
echo "Verify:"
echo "  ssh \"$WIN_HOST\" \"schtasks /query /tn $SCHEDULED_TASK\""
echo "Start now without waiting for next logon:"
echo "  ssh \"$WIN_HOST\" \"wsl.exe -d $DISTRO -u $WSL_USER -- $WATCHDOG_PATH\""
