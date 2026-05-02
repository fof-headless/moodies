#!/usr/bin/env bash
# debug-run.sh — temporarily route all macOS HTTPS traffic through mitmproxy
# so you can see flows. Auto-reverts proxy on exit (ctrl-C, signal, terminal
# close, crash). Does NOT use launchd or doomsday install state.
#
# Usage:
#   ./debug-run.sh                      # mitmweb (browser UI), no addon
#   ./debug-run.sh web                  # same as above, explicit
#   ./debug-run.sh cli                  # mitmdump (terminal output, no UI)
#   ./debug-run.sh tui                  # mitmproxy (interactive terminal UI)
#   ./debug-run.sh cli sanitize         # cli + load moodies sanitizer addon
#   ./debug-run.sh web  sanitize        # web + load moodies sanitizer addon
#                                       # (events go to ~/.doomsday/raw_events.jsonl)
#
# Env overrides:
#   SERVICE=Wi-Fi      network service to proxy (default Wi-Fi)
#   PORT=8080          listen port (default 8080)
#   WEB_PORT=8081      mitmweb UI port (web mode only)
#
# Caveats from testing:
#   - HSTS-preloaded sites (claude.ai, google.com, banks) will refuse the
#     mitmproxy CA. That's a browser hardcode, not a proxy bug.
#   - Chrome's HTTP/3 to Cloudflare-fronted sites bypasses HTTP proxies.
#     For those, see chrome://flags/#enable-quic.

set -euo pipefail

SERVICE="${SERVICE:-Wi-Fi}"
PORT="${PORT:-8080}"
WEB_PORT="${WEB_PORT:-8081}"

# Parse args: any order, recognized tokens only.
ENGINE="web"
ADDON_MODE=""
for arg in "$@"; do
  case "$arg" in
    web|cli|tui)  ENGINE="$arg" ;;
    sanitize)     ADDON_MODE="sanitize" ;;
    -h|--help)    sed -n '2,20p' "$0"; exit 0 ;;
    *)            echo "Unknown arg: $arg (use: web|cli|tui [sanitize])" >&2; exit 1 ;;
  esac
done

case "$ENGINE" in
  web) BIN_NAME="mitmweb" ;;
  cli) BIN_NAME="mitmdump" ;;
  tui) BIN_NAME="mitmproxy" ;;
esac

MITM="$(command -v "$BIN_NAME" || echo "/opt/homebrew/bin/$BIN_NAME")"
if [[ ! -x "$MITM" ]]; then
  echo "ERROR: $BIN_NAME not found. brew install mitmproxy" >&2
  exit 1
fi

if lsof -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "ERROR: port $PORT is already in use." >&2
  echo "       If doomsday daemon is running: ./doomsday-disable" >&2
  exit 1
fi

# Safety net: if a previous run left explicit proxy on (e.g. terminal was closed
# while no trap fired), wipe it before starting so we don't stack proxies or
# leave the user in a confused state on exit.
if [[ "$(networksetup -getwebproxy "$SERVICE" | awk '/Enabled:/ {print $2}')" == "Yes" ]] \
   || [[ "$(networksetup -getsecurewebproxy "$SERVICE" | awk '/Enabled:/ {print $2}')" == "Yes" ]]; then
  echo "[debug-run] stale proxy config detected on $SERVICE — clearing before start"
  networksetup -setwebproxystate "$SERVICE" off 2>/dev/null || true
  networksetup -setsecurewebproxystate "$SERVICE" off 2>/dev/null || true
  networksetup -setproxybypassdomains "$SERVICE" Empty 2>/dev/null || true
fi

cleanup() {
  echo
  if [[ -n "${MITM_PID:-}" ]] && kill -0 "$MITM_PID" 2>/dev/null; then
    kill -TERM "$MITM_PID" 2>/dev/null || true
    # Give mitm a moment to flush; SIGKILL if it ignores TERM.
    for _ in 1 2 3 4 5; do
      kill -0 "$MITM_PID" 2>/dev/null || break
      sleep 0.2
    done
    kill -KILL "$MITM_PID" 2>/dev/null || true
  fi
  echo "[debug-run] reverting proxy on $SERVICE..."
  networksetup -setwebproxystate "$SERVICE" off 2>/dev/null || true
  networksetup -setsecurewebproxystate "$SERVICE" off 2>/dev/null || true
  networksetup -setproxybypassdomains "$SERVICE" Empty 2>/dev/null || true
  echo "[debug-run] proxy off. Browsing back to direct."
}
# HUP catches terminal-close / SSH disconnect — most common reason cleanup is missed.
trap cleanup EXIT INT TERM HUP

echo "[debug-run] routing $SERVICE HTTP+HTTPS → 127.0.0.1:$PORT"
networksetup -setwebproxy "$SERVICE" 127.0.0.1 "$PORT"
networksetup -setsecurewebproxy "$SERVICE" 127.0.0.1 "$PORT"
networksetup -setproxybypassdomains "$SERVICE" \
  localhost 127.0.0.1 "*.local" "169.254/16" \
  "*.ngrok-free.app" "*.ngrok.io" "*.ngrok.app"

ARGS=(--listen-port "$PORT" --set flow_detail=2)
[[ "$ENGINE" == "web" ]] && ARGS+=(--web-port "$WEB_PORT")

if [[ "$ADDON_MODE" == "sanitize" ]]; then
  ARGS+=(-s "$HOME/.doomsday/sanitizer.py")
  echo "[debug-run] sanitizer addon enabled — events go to ~/.doomsday/raw_events.jsonl"
fi

case "$ENGINE" in
  web) echo "[debug-run] starting mitmweb (UI: http://127.0.0.1:$WEB_PORT)" ;;
  cli) echo "[debug-run] starting mitmdump (flows print here)" ;;
  tui) echo "[debug-run] starting mitmproxy (interactive — q to quit)" ;;
esac
echo "[debug-run] press ctrl-C to stop and auto-revert proxy"
echo

# Do NOT use `exec` — that replaces the bash process and discards our trap,
# which means a terminal close (SIGHUP) won't run cleanup. Run as a child so
# the parent shell stays alive to handle the signal and run cleanup().
"$MITM" "${ARGS[@]}" &
MITM_PID=$!
wait "$MITM_PID"
