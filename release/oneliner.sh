#!/bin/sh
# Moodies one-liner installer. Host this at /install on a public URL and
# users can pull + install with:
#
#   curl -fsSL https://your-host/install | sh
#
# Behaviour:
#   1. Download the latest moodies-darwin-<arch>.tar.gz from the same host
#   2. Extract to a temp dir
#   3. Run ./install.sh with MOODIES_BACKEND_URL pre-filled so the install
#      is non-interactive (recipient doesn't get prompted)
#
# Override via env:
#   MOODIES_BASE_URL      — where to download from (default: this script's host)
#   MOODIES_BACKEND_URL   — where the daemon will ship events to
#                           (default: MOODIES_BASE_URL — assumes bundle + backend
#                           live on the same host, which is how the dev
#                           ngrok setup works)
#   MOODIES_BUNDLE_NAME   — override the tarball filename (default: moodies.tar.gz)

set -eu

# Default base URL — the dev ngrok tunnel. Override via env or by editing
# this single line before hosting on a different domain.
DEFAULT_BASE_URL="https://421d-106-51-76-189.ngrok-free.app"

BASE_URL="${MOODIES_BASE_URL:-$DEFAULT_BASE_URL}"
BACKEND_URL="${MOODIES_BACKEND_URL:-$BASE_URL}"
BUNDLE_NAME="${MOODIES_BUNDLE_NAME:-moodies.tar.gz}"

# Strip trailing slashes so URL concatenation doesn't double up.
BASE_URL="${BASE_URL%/}"
BACKEND_URL="${BACKEND_URL%/}"

bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
red()    { printf '\033[31m%s\033[0m\n' "$*"; }

bold "Moodies one-liner installer"
echo "  base url:    $BASE_URL"
echo "  backend url: $BACKEND_URL"
echo "  bundle:      $BUNDLE_NAME"
echo

if [ "$(uname -s)" != "Darwin" ]; then
    red "macOS only. Detected: $(uname -s)"
    exit 1
fi

TMPDIR="$(mktemp -d -t moodies-install.XXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "→ Downloading $BASE_URL/$BUNDLE_NAME …"
if ! curl -fsSL "$BASE_URL/$BUNDLE_NAME" -o "$TMPDIR/$BUNDLE_NAME"; then
    red "Download failed. Check that $BASE_URL/$BUNDLE_NAME exists and is reachable."
    exit 1
fi
green "✓ downloaded $(du -h "$TMPDIR/$BUNDLE_NAME" | awk '{print $1}')"

echo "→ Extracting …"
cd "$TMPDIR"
tar -xzf "$BUNDLE_NAME"

BUNDLE_DIR="$(find . -maxdepth 1 -type d -name 'moodies-*' | head -n 1)"
if [ -z "$BUNDLE_DIR" ]; then
    red "Couldn't locate the extracted moodies-* directory."
    exit 1
fi
cd "$BUNDLE_DIR"

if [ ! -x ./install.sh ]; then
    red "Extracted bundle is missing install.sh."
    exit 1
fi

echo
bold "→ Handing off to bundle's install.sh"
echo
# Hand off with MOODIES_BACKEND_URL set so the bundled installer skips its
# interactive prompt — the one-liner UX should be zero typing past the
# initial curl line and the keychain auth dialog.
MOODIES_BACKEND_URL="$BACKEND_URL" exec ./install.sh
