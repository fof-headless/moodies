# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`moodies` (formerly `doomsday`) is a macOS-only local proxy agent that captures and sanitizes Claude.ai / Anthropic API traffic on the user's own machine, buffers it to SQLite, and ships sanitized events to a backend.

The data path is: **PAC file** routes claude.ai/anthropic.com hosts to **mitmdump on 127.0.0.1:8080** → **`sanitizer/sanitizer.py`** mitmproxy addon writes JSONL to `~/.doomsday/raw_events.jsonl` → **`doomsday-daemon`** tails that file into SQLite (`~/.doomsday/buffer.db`) → **sync client** POSTs batches to `BackendURL` and marks rows synced.

## Build and run

```
go build -o doomsday          ./cmd/doomsday
go build -o doomsday-daemon   ./cmd/doomsday-daemon
go build -o doomsday-disable  ./cmd/doomsday-disable
```

Tests:
```
go test ./...                             # all Go tests
go test ./internal/store/...              # one package
cd sanitizer && python -m pytest tests/   # sanitizer tests (mocks mitmproxy)
```

The sanitizer requires `mitmproxy` available on `PATH` (`brew install mitmproxy`) at runtime; tests stub it out via `sys.modules`.

End-to-end install (after building) does macOS-level setup — generates the mitmproxy CA and trusts it in the login keychain, writes the PAC file, sets it as the auto-proxy URL on every active network service, initializes SQLite, copies `sanitizer/sanitizer.py` to `~/.doomsday/sanitizer.py`, and loads a launchd agent:
```
./doomsday install
./doomsday doctor          # verify components; --fix for safe auto-fixes; --json for machine output
./doomsday uninstall
```

## Naming: doomsday vs moodies

The repo is `moodies` and the Homebrew formula installs the binaries as `moodies`, `moodies-daemon`, `moodies-disable`. **Internally the code is still `doomsday`**:
- Go module: `github.com/doomsday/agent`
- Binary names produced by `go build`: `doomsday*`
- launchd label: `com.doomsday.agent`
- Runtime dir: `~/.doomsday/` (config, state, sqlite, sanitizer, logs, heartbeat, PAC)
- Keychain certificate CN substring: `mitmproxy`

The Homebrew formula at `Formula/moodies.rb` renames the binaries with `std_go_args(output: bin/"moodies", ...)` and bundles `sanitizer/` into `libexec`. When editing install/uninstall logic, keep both names in mind — paths that the daemon resolves at runtime (`filepath.Dir(os.Args[0])` for `schema.sql`, `sanitizer/sanitizer.py`) must work whether the binary is `doomsday-daemon` next to a sibling tree or `moodies-daemon` under Homebrew's `libexec`.

## Code map

- `cmd/doomsday/main.go` — cobra CLI: `install`, `uninstall`, `start`, `stop`, `status`, `doctor`, `logs`, `config`. Install is **resumable** via `internal/state` — each component (cert, PAC file, PAC active services, sqlite, launchd) is checkpointed and skipped on re-run.
- `cmd/doomsday-daemon/main.go` — long-running process launched by launchd. Spawns mitmdump, tails its JSONL output into SQLite, runs the sync goroutine + heartbeat + watchdog (restarts mitmdump if `127.0.0.1:8080` stops accepting connections). Honors `~/.doomsday/disable_marker` — if present, runs the disable sequence and exits without restarting.
- `cmd/doomsday-disable/main.go` — emergency kill switch that turns off PAC on all stored network services and unloads launchd.
- `internal/proxy/` — mitmdump spawn (`mitmdump.go`), CA generation/trust via `security` (`cert.go`), PAC template (`pac.go`), `mitmdump` binary discovery (`resolve.go`).
- `internal/foreign/scan.go` — pre-install detection of pre-existing proxy config in shell rcs, `~/.claude/settings.json`, `~/.claude.json`, `npm config`, `git config`, and process env. Install is **blocked** if any non-doomsday `HTTPS_PROXY` / `NODE_EXTRA_CA_CERTS` is found.
- `internal/state/state.go` — `~/.doomsday/state.json`, atomic write via tmp+rename. Tracks per-component install status; uninstall reads `PACActiveOnServices` to know which services to disable.
- `internal/config/config.go` — `~/.doomsday/config.toml`. Defaults: `BackendURL=http://localhost:4000`, `StorageMode=raw`, `ListenPort=8080`.
- `internal/store/` — SQLite buffer (`modernc.org/sqlite`, pure Go, no CGO). `INSERT OR IGNORE` on `event_id` makes ingestion idempotent. `OpenWithSchema` reads `schema.sql` next to the binary, falling back to an embedded copy.
- `internal/sync/client.go` — every 30s pulls up to 100 unsynced events, POSTs to `/api/v1/agent/events`, marks synced on 200. Heartbeat to `/api/v1/agent/heartbeat` every 5min.
- `sanitizer/sanitizer.py` — mitmproxy addon. Endpoint patterns are matched by exact regex; unknown paths go to `parse_errors.jsonl`, not `raw_events.jsonl`. Storage mode `hash_only` strips `content_text` and keeps only `content_sha256` + `char_count`. `STORAGE_MODE` and `DOOMSDAY_OUTPUT` are passed via env from `SpawnMitmdump`.

## Things that have to stay in sync

These three definitions of "Anthropic hosts" must agree, or the proxy will silently drop traffic:

1. `internal/proxy/pac.go` `pacTemplate` — hosts that get routed to the proxy.
2. `internal/proxy/mitmdump.go` `--set ignore_hosts=...` — mitmdump's negative match (note the negative-lookahead form `^(?!.*(...)).*$` — anything matching this is ignored, so the listed hosts are the ones we capture).
3. `sanitizer/sanitizer.py` `TARGET_HOSTS` — the addon's own filter; non-matching responses are silently dropped.

When adding a new host (e.g. a new claude subdomain), update all three.

## Release / Homebrew

`RELEASE.md` is authoritative. Per release: tag, push, recompute the tarball sha256, update `Formula/moodies.rb` (`url` + `sha256`), commit. End users tap with the explicit URL form because the repo is not named `homebrew-moodies`:
```
brew tap fof-headless/moodies https://github.com/fof-headless/moodies.git
```

## Active work

`TODO.md` lists the current blockers — sanitizer not writing events from browser traffic (PAC routing / `ignore_hosts` regex), packaging via `Makefile` + `install.sh`, and a few uninstall/idempotency fixes. Read it before picking up tangent work.
