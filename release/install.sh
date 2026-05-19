#!/bin/zsh
# Moodies installer — extracts pre-built binaries, prompts for backend URL,
# runs `moodies install`. Designed to be run from inside an extracted
# tarball where ./bin/ and ./MoodiesMenuBar.app sit next to this script.
#
# What this script does (in order):
#   1. macOS sanity check
#   2. Ensures Homebrew + mitmproxy are available (only runtime dep)
#   3. Asks you which backend URL the daemon should ship events to
#   4. Copies binaries to ~/.local/bin and MoodiesMenuBar.app to ~/Applications
#   5. Writes ~/.doomsday/config.toml with the backend URL
#   6. Runs `moodies install` (CA trust, PAC, launchd plists, shim)
#   7. Tells you where to sign in
#
# Reversal is a single command: ~/.local/bin/moodies uninstall

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${(%):-%x}")" && pwd)"
cd "$SCRIPT_DIR"

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }

bold "Moodies installer"
echo

# 1. macOS check ------------------------------------------------------
if [[ "$(uname -s)" != "Darwin" ]]; then
    red "This installer is macOS-only. Detected: $(uname -s)"
    exit 1
fi

ARCH="$(uname -m)"
if [[ "$ARCH" != "arm64" && "$ARCH" != "x86_64" ]]; then
    red "Unsupported CPU architecture: $ARCH"
    exit 1
fi

# 2. Homebrew + mitmproxy --------------------------------------------
if ! command -v brew >/dev/null 2>&1; then
    yellow "Homebrew not found. Installing it now (will prompt for sudo)..."
    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
    # The installer doesn't update PATH for the current shell, so source the brew shellenv
    if [[ -f /opt/homebrew/bin/brew ]]; then
        eval "$(/opt/homebrew/bin/brew shellenv)"
    elif [[ -f /usr/local/bin/brew ]]; then
        eval "$(/usr/local/bin/brew shellenv)"
    fi
fi

if ! command -v mitmdump >/dev/null 2>&1; then
    yellow "Installing mitmproxy (the only runtime dependency)..."
    brew install mitmproxy
fi
green "✓ Homebrew + mitmproxy ready"

# 3. Backend URL ------------------------------------------------------
echo
bold "Which backend should the daemon ship events to?"
DEFAULT_URL="${MOODIES_BACKEND_URL:-http://localhost:4000}"
echo "  (press Enter to use: $DEFAULT_URL)"
printf "Backend URL: "
read -r BACKEND_URL
BACKEND_URL="${BACKEND_URL:-$DEFAULT_URL}"

# Strip trailing slash so URL concatenation in the daemon doesn't double up.
BACKEND_URL="${BACKEND_URL%/}"
echo

# Quick reachability probe — informational only, not a blocker. People
# install offline all the time.
if curl -sf -o /dev/null -m 5 "$BACKEND_URL/" 2>/dev/null; then
    green "✓ Backend $BACKEND_URL is reachable"
else
    yellow "⚠ Couldn't reach $BACKEND_URL — events will buffer locally until it's up"
fi

# 4. Copy binaries + .app --------------------------------------------
BIN_DIR="$HOME/.local/bin"
APP_DIR="$HOME/Applications"
mkdir -p "$BIN_DIR" "$APP_DIR"

echo
echo "→ Installing binaries to $BIN_DIR"
for bin in doomsday doomsday-daemon doomsday-disable moodies-claude; do
    if [[ ! -x "./bin/$bin" ]]; then
        red "Missing ./bin/$bin in this tarball — installer is incomplete."
        exit 1
    fi
    install -m 0755 "./bin/$bin" "$BIN_DIR/$bin"
done

# Also expose `moodies` as an alias for the `doomsday` CLI — that's the
# user-facing name on Homebrew installs and matches our docs.
ln -sf "$BIN_DIR/doomsday" "$BIN_DIR/moodies"

echo "→ Installing MoodiesMenuBar.app to $APP_DIR/Moodies.app"
if [[ ! -d "./MoodiesMenuBar.app" ]]; then
    red "Missing ./MoodiesMenuBar.app in this tarball — installer is incomplete."
    exit 1
fi
rm -rf "$APP_DIR/Moodies.app"
cp -R "./MoodiesMenuBar.app" "$APP_DIR/Moodies.app"

# 5. Write config.toml ------------------------------------------------
mkdir -p "$HOME/.doomsday"
cat > "$HOME/.doomsday/config.toml" <<EOF
backend_url = "$BACKEND_URL"
storage_mode = "raw"
listen_port = 8080
# agent_token is left blank intentionally — the menu bar's Sign In flow
# writes it here after the backend authenticates you.
EOF
green "✓ Config written to ~/.doomsday/config.toml"

# 6. Run moodies install ---------------------------------------------
echo
bold "Running moodies install (will prompt for keychain password to trust the CA)..."
echo

# Make sure ~/.local/bin is on PATH for the rest of this script.
export PATH="$BIN_DIR:$PATH"

# The CLI lives at ~/.local/bin/moodies (symlink to doomsday). Run from
# that directory so it can locate sibling binaries (doomsday-daemon,
# moodies-claude) via filepath.Dir(os.Args[0]).
cd "$BIN_DIR"
./moodies install

echo
green "════════════════════════════════════════════════════════════"
bold "  Moodies installed."
green "════════════════════════════════════════════════════════════"
echo
echo "  Next steps:"
echo "    1. Look at your menu bar (top-right) for the Moodies icon."
echo "    2. Click it → Sign In → username: user1  password: user1"
echo "    3. Click Resume Capture to start the daemon."
echo "    4. Open Claude.ai in your browser, or type \`claude\` in a NEW"
echo "       terminal (so the shim takes effect)."
echo
echo "  Backend dashboard:  $BACKEND_URL/"
echo
echo "  To remove everything:"
echo "    moodies uninstall"
echo
