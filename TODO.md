# Doomsday Agent — TODO

## In Progress / Pick Up Tomorrow

### 1. Fix agent proxy capture (current blocker)
- [ ] Debug why sanitizer isn't writing events after claude.ai browser traffic
- [ ] Verify PAC proxy is routing browser traffic: System Settings → Network → Proxies → confirm PAC URL is set
- [ ] Check `ignore_hosts` regex in sanitizer isn't inverting incorrectly
- [ ] Test: send a message on claude.ai in browser, check `~/.doomsday/raw_events.jsonl`

### 2. Agent packaging (git repo, download & run)
- [ ] Add `Makefile` with `make build` — compiles `doomsday`, `doomsday-daemon`, `doomsday-disable` for darwin/arm64, darwin/amd64, linux/amd64
- [ ] Bundle `sanitizer/sanitizer.py` alongside binaries in `dist/`
- [ ] Write `install.sh` one-liner: clone repo → `make build` → `./doomsday install`
- [ ] Tag a GitHub release with pre-built binaries attached

### 3. Remaining agent fixes
- [ ] `doomsday uninstall` fails silently when cert isn't in login keychain — make idempotent
- [ ] Default `backend_url` in config is `localhost:4000` — prompt user on first install or set via `install.sh`
