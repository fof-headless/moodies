# Moodies menu bar app

A tiny macOS menu bar app that wraps the moodies daemon with a kill
switch. Mainly here so you can pause capture or fully uninstall without
remembering shell commands when something goes sideways.

## What it shows

| Icon | State | Meaning |
|---|---|---|
| `dot.radiowaves.left.and.right` | Capturing | mitmdump up, heartbeat fresh, `disable_marker` absent |
| `pause.circle` | Paused | `~/.doomsday/disable_marker` exists; daemon refuses to run |
| `circle.dotted` | Off | Daemon stopped, PAC off |
| `exclamationmark.triangle` | Degraded | Port responds but heartbeat is stale → daemon may be wedged |

Status is polled every 5 seconds via direct file/socket checks (no shell
fork on each tick).

## What it does

- **Open Dashboard** → opens `http://localhost:4000/` in your default browser.
- **Pause Capture** → writes `~/.doomsday/disable_marker`, kills `doomsday-daemon` and `mitmdump`, runs `moodies-disable` to flip PAC off on every active network service.
- **Resume Capture** → removes the marker, `launchctl load`s the agent plist if not already loaded, and `launchctl kickstart`s it. PAC re-enables itself via the daemon's watchdog once mitmdump binds 8080.
- **Uninstall Moodies…** → confirmation dialog, then runs `moodies uninstall` (CA cert, PAC, launchd plist, claude shim, shell rc edits). Buffer + manifest cache in `~/.doomsday/` are kept.
- **Quit Menu Bar App** → quits *only* the menu bar app. The daemon keeps running.

## Build

Requires Swift 5.9+ (Command Line Tools is enough — full Xcode is **not** needed). macOS 13+.

```bash
cd menubar/
./build.sh           # builds release, assembles MoodiesMenuBar.app
./build.sh --run     # same, plus `open` the bundle
./build.sh --dev     # foreground `swift run` for log-watching
```

The build script ad-hoc signs the binary so it survives Finder copies. **First launch will be blocked by Gatekeeper** because there's no Developer ID signature — right-click the `.app` in Finder → Open → confirm the prompt. One time, then it launches normally.

To distribute later: install full Xcode.app, switch the codesign step to a real Developer ID, then `xcrun notarytool submit`.

## Where it finds the moodies CLI

For the Uninstall action, the app shells out to the `moodies` binary. Search order:

1. `MOODIES_BIN` env var (full path)
2. `/opt/homebrew/bin/moodies` (Homebrew install)
3. `~/.local/bin/moodies`
4. `~/dayjob/moodies/doomsday` (dev path — repo root binary)
5. `which moodies` on PATH

If none resolve, you'll get a dialog telling you to set `MOODIES_BIN`.

## Daemon lifecycle assumption

Pause/Resume assume the daemon is or will be managed by launchd
(`~/Library/LaunchAgents/com.doomsday.agent.plist`). The plist is created
by `moodies install`. If you've been running `./doomsday-daemon` by hand
during development, the first time you hit Resume the menu bar will
`launchctl load` the plist — your terminal-launched daemon should be
killed first to avoid two daemons fighting over port 8080.
