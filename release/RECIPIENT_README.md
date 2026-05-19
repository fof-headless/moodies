# Moodies — install

You received this because someone wants to capture and analyze your
Claude.ai / Anthropic API traffic on this machine. **Read this before
running** — it modifies your network configuration and installs a CA
certificate.

## What this installs

1. **Mitmproxy CA certificate** in your macOS login keychain. This lets a
   local proxy decrypt your traffic to claude.ai / api.anthropic.com. Other
   sites are untouched.
2. **`moodies-daemon`** — a background process (launchd-managed) that
   spawns mitmproxy on `127.0.0.1:8080`, captures Anthropic traffic, and
   ships it to the backend URL you provide during install.
3. **PAC file** routing claude.ai / anthropic.com hosts through the proxy.
   Active on every network service (Wi-Fi, Ethernet, etc.).
4. **`claude` shim** — a binary at `~/.moodies/bin/claude` that wraps the
   real Claude Code CLI so its API calls also flow through the proxy.
   Other CLI tools (`git`, `gh`, `npm`, your own Python scripts) are
   **not affected**.
5. **Menu bar app** at `~/Applications/Moodies.app` for status + a
   one-click kill switch (Pause / Resume / Uninstall).

## What this does NOT do

- Doesn't route traffic to sites other than claude.ai / anthropic.com.
- Doesn't intercept traffic from your Python / Node / other apps unless
  you explicitly set `HTTPS_PROXY` yourself.
- Doesn't transmit any traffic to anyone except the backend URL you
  provide.

## Install

```bash
./install.sh
```

You'll be prompted for the backend URL. You'll also be prompted for your
macOS login password (once, to authorize the CA cert into your keychain).

After install completes:

1. Look at your menu bar (top-right). Click the Moodies icon.
2. Click **Sign In** → enter the username / password your admin gave you
   (v1 testbed uses `user1` / `user1`).
3. Click **Resume Capture**. The icon switches to a radio-waves indicator.
4. Open Claude.ai in your browser, or run `claude` in a **new terminal**
   (so the shim takes effect). All traffic is now captured.

## Uninstall (reverses everything)

```bash
moodies uninstall
```

This removes the CA cert from your keychain, disables PAC on every
service, unloads the launchd agents, deletes the `claude` shim, and
strips the `PATH` line from your shell rc.

Local capture data in `~/.doomsday/buffer.db` is **kept** (so you don't
lose unsynced events). Delete it manually if you want a clean slate:

```bash
rm -rf ~/.doomsday/
```

## Pause without uninstalling

Click the menu bar icon → **Pause Capture**. The daemon stops, PAC turns
off, and your machine routes everything direct. Click **Resume Capture**
to bring it back.

## Troubleshooting

- **Menu bar icon doesn't appear after install** — open Finder, navigate
  to `~/Applications`, right-click `Moodies.app` → **Open**. macOS may
  block first launch because the app isn't notarized; this is a one-time
  bypass.
- **Claude Code returns connection errors** — the daemon may be off. Click
  the menu bar → Resume Capture. If you want Claude to work even when the
  daemon is off, the shim has a fallback: it'll print one warning line
  and run claude direct (uncaptured).
- **Other CLIs (git, gh, npm) feel broken** — moodies should not affect
  them. If something looks off, run `moodies doctor`. If the issue
  persists, run `moodies uninstall` and the issue should clear.

## Requirements

- macOS 13 (Ventura) or later
- Homebrew (the installer will set this up if missing)
- A modern Anthropic-using browser or Claude Code CLI to actually generate
  captured events

## What's in this tarball

```
bin/
  doomsday           # CLI: install / uninstall / doctor / status
  doomsday-daemon    # the background process
  doomsday-disable   # emergency kill switch
  moodies-claude     # the claude shim
MoodiesMenuBar.app/  # status + control UI in the menu bar
install.sh           # this installer
README.md            # this file
```
