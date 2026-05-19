#!/bin/zsh
# Builds MoodiesMenuBar.app — a Swift Package executable wrapped in a
# minimal .app bundle so macOS treats it as an LSUIElement (menu-bar app,
# no Dock icon).
#
# Usage:
#   ./build.sh          # builds release, assembles .app, prints next steps
#   ./build.sh --run    # same, then `open` the .app
#   ./build.sh --dev    # `swift run` (no bundle; foreground, prints logs)

set -euo pipefail
cd "$(dirname "$0")"

if [[ "${1:-}" == "--dev" ]]; then
    exec swift run MoodiesMenuBar
fi

echo "→ swift build (release)"
swift build -c release

EXE=".build/release/MoodiesMenuBar"
APP="MoodiesMenuBar.app"
CONTENTS="$APP/Contents"

echo "→ assembling $APP"
rm -rf "$APP"
mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Resources"
cp "$EXE" "$CONTENTS/MacOS/MoodiesMenuBar"
cp Resources/Info.plist "$CONTENTS/Info.plist"

# Without a real code signature macOS Gatekeeper will warn on first launch.
# Ad-hoc sign so the app at least runs without "damaged" errors after a
# move/copy. For distribution, swap this for a real Developer ID sign +
# `xcrun notarytool submit` once Xcode.app is available.
echo "→ ad-hoc codesign"
codesign --force --sign - --timestamp=none "$CONTENTS/MacOS/MoodiesMenuBar"
codesign --force --sign - --timestamp=none "$APP"

echo "✓ built $APP"
echo
echo "  open it:    open ./$APP"
echo "  uninstall:  rm -rf ./$APP   (no system traces; LSUIElement = no Dock state)"
echo
echo "First launch: Gatekeeper will warn because the app isn't notarised."
echo "Right-click the .app in Finder → Open → 'Open' on the prompt. One time only."

if [[ "${1:-}" == "--run" ]]; then
    open "./$APP"
fi
