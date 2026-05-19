#!/bin/zsh
# Builds a Moodies distribution tarball.
#
# Output: release/dist/moodies-darwin-<arch>-<version>.tar.gz
# Layout inside the tarball:
#   moodies/
#     bin/{doomsday, doomsday-daemon, doomsday-disable, moodies-claude}
#     MoodiesMenuBar.app/
#     install.sh
#     README.md
#
# Recipient untars, runs ./install.sh, gets prompted for the backend URL.
#
# Usage:
#   ./release/build-bundle.sh                  # version derived from `git describe`
#   ./release/build-bundle.sh v0.2.0           # explicit version tag
#   BACKEND_URL=... ./release/build-bundle.sh  # bake a default URL into config.toml
#                                              # (recipient can still override at install)

set -euo pipefail
cd "$(dirname "${(%):-%x}")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo v0.0.0)}"
ARCH="$(uname -m)"
BUNDLE_NAME="moodies-darwin-${ARCH}-${VERSION}"
DIST_DIR="release/dist"
STAGE="$DIST_DIR/$BUNDLE_NAME"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }

bold "Building bundle: $BUNDLE_NAME"

# Clean stage
rm -rf "$STAGE"
mkdir -p "$STAGE/bin"

# 1. Go binaries -------------------------------------------------------
# The ldflags vars are left empty here — the installer writes config.toml
# with the user-supplied backend URL, which is the runtime source of truth.
# (Pass BACKEND_URL=... when building if you want to hardcode a URL that
# users can't override; the daemon's main.backendURL ldflag wins over
# config.toml when set.)
LDFLAGS="-s -w"
if [[ -n "${BACKEND_URL:-}" ]]; then
    LDFLAGS="$LDFLAGS -X main.backendURL=${BACKEND_URL}"
    bold "  backendURL baked in: $BACKEND_URL"
fi

for pkg in doomsday doomsday-daemon doomsday-disable; do
    echo "→ go build $pkg"
    go build -ldflags "$LDFLAGS" -o "$STAGE/bin/$pkg" "./cmd/$pkg"
done
echo "→ go build moodies-claude (shim)"
go build -ldflags "$LDFLAGS" -o "$STAGE/bin/moodies-claude" "./cmd/moodies-claude"

# 2. Menu bar .app -----------------------------------------------------
echo "→ swift build MoodiesMenuBar (release)"
(cd menubar && ./build.sh > /dev/null)
cp -R menubar/MoodiesMenuBar.app "$STAGE/MoodiesMenuBar.app"

# 3. Installer + README -----------------------------------------------
cp release/install.sh "$STAGE/install.sh"
chmod +x "$STAGE/install.sh"
cp release/RECIPIENT_README.md "$STAGE/README.md"

# 4. Tarball -----------------------------------------------------------
echo "→ tar"
(cd "$DIST_DIR" && tar -czf "${BUNDLE_NAME}.tar.gz" "$BUNDLE_NAME")
SIZE=$(du -h "$DIST_DIR/${BUNDLE_NAME}.tar.gz" | awk '{print $1}')
SHA=$(shasum -a 256 "$DIST_DIR/${BUNDLE_NAME}.tar.gz" | awk '{print $1}')

# Optional cleanup of the staging tree (keep the tarball).
rm -rf "$STAGE"

# Versioned tarball is what's checked into a release; the static-named
# copy is what oneliner.sh expects to find at /moodies.tar.gz on the
# hosting server. Both files are bit-identical, just symlinked here.
cp "$DIST_DIR/${BUNDLE_NAME}.tar.gz" "$DIST_DIR/moodies.tar.gz"

# Stage the one-liner wrapper next to the tarball so the operator can
# `scp` the whole release/dist/ folder up to their server and immediately
# share `https://host/install`.
cp release/oneliner.sh "$DIST_DIR/install"
chmod +x "$DIST_DIR/install"

echo
bold "Built: $DIST_DIR/${BUNDLE_NAME}.tar.gz ($SIZE)"
echo "  sha256: $SHA"
echo
echo "release/dist/ contains:"
echo "  ${BUNDLE_NAME}.tar.gz   versioned bundle (archive this)"
echo "  moodies.tar.gz          stable filename — host this at /moodies.tar.gz"
echo "  install                 host this at /install"
echo
bold "Recipient one-liner (after you host both files at your base URL):"
echo "  curl -fsSL https://<your-host>/install | sh"
echo
echo "Examples of where to host:"
echo "  • behind your existing ngrok: serve release/dist/ via any static server"
echo "  • Caddy:   file_server browse"
echo "  • Python:  python3 -m http.server --directory release/dist 8080"
