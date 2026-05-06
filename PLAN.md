# PLAN.md — Backend MVP first, then daemon refactor

This document is self-contained and intended to be read cold by a fresh Claude
Code session. It captures what's built, what's broken, what to build next, and
the technical detail needed to execute without re-reading the conversation that
produced it.

---

## TL;DR

The moodies daemon is mid-refactor. Build is currently broken.

**Chunk 1 (Backend MVP) is DONE** as of 2026-05-06 — see "Status" below.
**Chunk 2 (daemon refactor)** is the only remaining work: fix the broken
build, wire `internal/filter/`, add handshake/manifest fetch on the daemon
side, write `backend.md`.

---

## Status — 2026-05-06

### ✅ Chunk 1 — Backend MVP — complete

Built at `/Users/shreyanshkushwaha/dayjob/moodies-backend/` (sibling repo, not
yet `git init`'d — user will `git init && git remote add` when ready to push).

What landed:
- `go.mod` — module `github.com/fof-headless/moodies-backend`, sole external
  dep is `modernc.org/sqlite` (pure-Go, matches daemon).
- `internal/types/manifest.go` — Manifest, ModuleToggles, FilterConfig,
  HeadersConfig, RedactionConfig, `DefaultManifest()` mirroring the daemon's
  `internal/config/config.go` `Defaults()`.
- `internal/types/event.go` — full mirror of daemon's `internal/filter/types.go`
  Event struct (the wire contract).
- `internal/store/{schema.sql,store.go}` — agents/events/sessions/manifests
  tables, idempotent event upsert (`ON CONFLICT(event_id) DO NOTHING`),
  sha256-keyed manifest version, helper queries for UI (count-since,
  distinct-endpoint-types, paginated list with filters).
- `internal/api/{api.go,handshake.go,events.go,heartbeat.go,config.go}` — the
  four daemon-facing endpoints. Auth helper accepts agent_token OR
  session_token from request body.
- `internal/ui/ui.go` + `templates/{layout,dashboard,events_list,event_detail,
  agents,config}.html` + `static/style.css` — admin UI with dashboard,
  paginated events, event detail (extracted highlights + raw JSON), agents
  table with heartbeat-freshness colours, config form with regex validation
  and flash messages.
- `main.go` — env-driven (PORT, SQLITE_PATH), `//go:embed` for templates +
  static so the binary is self-contained.

Smoke-tested end-to-end (curl on localhost):
- `POST /api/v1/agent/handshake` → 200 with session + manifest.
- `POST /api/v1/agent/events` → received/duplicates split (idempotent on
  event_id confirmed by re-posting same event).
- `POST /api/v1/agent/heartbeat` → returns current manifest_version sha.
- `GET /` dashboard, `GET /events`, `GET /events/:id`, `GET /agents`,
  `GET /config` all render.

Bug found + fixed during smoke: SQLite's `CURRENT_TIMESTAMP` writes
`YYYY-MM-DD HH:MM:SS` (space separator), so passing RFC3339 (`T` separator)
as a `received_at >= ?` threshold lexicographically dropped fresh rows from
"events in last hour" counts. Threshold formatter switched to match SQLite's
own format. See `internal/store/store.go` `CountEventsSince`.

Deferred from MVP (intentional, not blockers):
- Admin UI is unauthenticated — gate behind ngrok URL secrecy.
- Single default manifest; per-agent manifests not yet supported.
- No mTLS / signed manifests yet.
- Backend has no `git init` / GitHub remote yet.

### ⏳ Chunk 2 — daemon refactor — pending

This is now the only remaining work. Spec is unchanged from below — fix the
two `SpawnMitmdump` call sites, add `internal/filter/filter.go` (Apply
pipeline), wire it into `tailEvents`, delete `sanitizer/`, add
`internal/config/manifest.go` + handshake/refresh in `internal/sync/client.go`,
write `backend.md`. See "Chunk 2" section below for line-level detail.

The backend at the sibling repo is ready to receive — once the daemon's
handshake/manifest plumbing is wired, the dashboard will start showing real
events.

---

## Project context (orient yourself)

**moodies** (formerly `doomsday`) is a macOS-only local proxy agent. It uses
mitmdump to MITM Anthropic-domain HTTPS traffic, sanitises it, and ships
events to a backend. Users install it on their Mac; admins control sanitization
policy centrally via the backend.

- Repo: `/Users/shreyanshkushwaha/dayjob/moodies` (this one)
- Remote: `https://github.com/fof-headless/moodies.git`
- Current branch: `working-daemon` (2 commits ahead of `main`, both pushed)
- Go module: `github.com/doomsday/agent` (legacy module name; binaries renamed
  to `moodies` by the Homebrew formula at install time)
- See `CLAUDE.md` at repo root for full codebase orientation — read it first.

The daemon stack (current state of the refactor):
```
Client → mitmdump (background, launchd) → embedded Python tap (mitm_tap.py)
                                              ↓
                                       ~/.doomsday/raw_events.jsonl
                                              ↓
                                       Go daemon tail
                                              ↓
                                  internal/filter (NOT YET WIRED — this is the gap)
                                              ↓
                                       SQLite buffer
                                              ↓
                                       sync to backend
```

---

## Current state snapshot

### Committed on `working-daemon` (already pushed to origin)

```
709e94e  chore: add debug-run.sh, CLAUDE.md, .gitignore
e2339c3  fix(proxy): use mitmproxy default confdir; drop dead host filter
1f03cef  moodies v0.1.0  (← branch point from main)
```

### Uncommitted changes in working tree (mid-refactor)

Run `git status` to confirm. As of this writing the working tree has:

**New files:**
- `internal/proxy/mitm_tap.py` — minimal mitmproxy addon (~85 lines), does
  host filter + raw flow dump as JSONL. Replaces sanitizer.py logic. Embedded
  into the daemon binary via `//go:embed` and written to `~/.doomsday/_tap.py`
  at every spawn.
- `internal/filter/types.go` (~165 lines) — `RawFlow` (input from tap) +
  `Event` (output to backend) + all sub-types: `ClientInfo`, `AccountInfo`,
  `CompletionData`, `ConversationFetchData`, `UploadData`, `Body`, `Message`,
  `AssistantMessage`, `ToolUse`, `PolicyInfo`, `RedactionAudit`.
- `internal/filter/redact.go` (~104 lines) — `CompiledPattern`, `RedactString`,
  `ClassifyMatches`, secret/PII pattern name sets.
- `internal/filter/classify.go` (~102 lines) — endpoint regex table,
  `Classify`, `ClassifyTool`, `ExtractMCPServer`, `ExtractOrgUUID`,
  `ExtractConversationUUID`.
- `internal/filter/sse.go` (~166 lines) — `ParseSSE` walker, mirrors the old
  Python `parse_sse` accumulator.
- `internal/filter/extract.go` (~403 lines) — per-endpoint extractors. Handles
  BOTH the public `/v1/messages` shape (messages[].role==user) AND the web
  app shape (top-level `prompt` / `text` / `completion.prompt`). This is the
  bug fix from earlier today — the old sanitizer.py only handled the first.

**Modified files:**
- `internal/proxy/mitmdump.go` — rewritten. New `SpawnOptions` struct, embeds
  `mitm_tap.py` via `go:embed`, writes to `~/.doomsday/_tap.py` on spawn,
  passes `DOOMSDAY_TARGET_HOSTS` env to the addon. `DefaultTargetHosts()`
  helper. `RestartWithBackoff` updated to take `SpawnOptions`. Old
  `SanitizerPath()` removed.
- `internal/config/config.go` — added `FilterConfig`, `HeadersConfig`,
  `RedactionConfig` structs and `Defaults()` function. Fields fall back to
  defaults if missing from `config.toml`.
- `cmd/doomsday/main.go` — install no longer copies `sanitizer/sanitizer.py`
  (the addon is embedded now). Lines around 118-125 simplified.

**Still present, NOT yet deleted (need to be removed in Chunk 2):**
- `sanitizer/sanitizer.py` (480 lines, dead code)
- `sanitizer/tests/test_sanitizer.py` (dead tests)
- `sanitizer/` directory itself

### Build status — BROKEN

```
$ go build ./...
cmd/doomsday-daemon/main.go:71:44: too many arguments in call to proxy.SpawnMitmdump
        have (number, string, string)
        want (proxy.SpawnOptions)
cmd/doomsday-daemon/main.go:179:61: too many arguments in call to proxy.SpawnMitmdump
        have (int, string, string)
        want (proxy.SpawnOptions)
```

Both call sites still pass the old 3-arg signature. They need to pass:
```go
proxy.SpawnMitmdump(proxy.SpawnOptions{
    Port:        cfg.ListenPort,
    OutputPath:  outputPath,
    TargetHosts: cfg.Filter.TargetHosts,  // or current applyCfg's target hosts
})
```

### Runtime state on the user's machine

- launchd: `com.doomsday.agent` is unloaded. Plist still at
  `~/Library/LaunchAgents/com.doomsday.agent.plist`.
- mitmdump: not running. Port 8080 closed.
- Wi-Fi proxy: off (both explicit and PAC).
- `~/.doomsday/state.json` — exists (last clean shutdown 2026-05-02T20:26:46Z).
- `~/.doomsday/config.toml`:
  ```toml
  backend_url = "https://7368-2406-7400-11d-4675-5cf-4a79-4874-26ac.ngrok-free.app"
  agent_token = "doomsday-testbed-token-2026"
  storage_mode = "raw"
  listen_port = 8080
  ```
  (No `[filter]` or `[redaction]` section — relies on `Defaults()` from the
  refactored `config.go`.)
- `~/.doomsday/raw_events.jsonl` — 20 events from prior session (in the OLD
  Python-sanitized shape, not the new RawFlow→Event shape).
- `~/.mitmproxy/mitmproxy-ca-cert.pem` — generated, trusted in System keychain.
  This is the cert mitmdump signs with. **Don't regenerate; will break trust.**
- `~/.doomsday/mitmproxy/` — orphan directory with stale CA from before the
  confdir fix. Safe to delete; daemon doesn't reference it anymore.

---

## Cold-start bootstrap (run these commands first)

In a fresh session, before doing anything else:

```bash
cd /Users/shreyanshkushwaha/dayjob/moodies
git status                                # confirm uncommitted state matches above
git log --oneline -5                      # confirm working-daemon HEAD
go build ./... 2>&1                       # confirm build is broken at the same lines
ls internal/filter/                       # confirm 5 files exist
cat ~/.doomsday/config.toml               # confirm backend_url is the ngrok URL
```

Then read in this order:
1. `CLAUDE.md` — codebase orientation
2. `PLAN.md` — this file
3. `internal/filter/types.go` — Event schema (the wire contract)
4. `internal/proxy/mitmdump.go` — `SpawnMitmdump(opts SpawnOptions)` signature

---

## Sequencing (and why)

1. **Backend MVP** (Chunk 1) — separate sibling repo, runnable today, shows
   events + edits feature flags. Once it's up, ngrok forwards to it and the
   user can see real events arriving.
2. **Daemon refactor finishing** (Chunk 2) — fix the broken build, wire
   `internal/filter/`, add handshake/manifest fetch on the daemon side.
3. **`backend.md`** (part of Chunk 2) — produced in the daemon repo as the
   contract; backend repo's README points back to it.

This order means the backend can be developed and verified in isolation
against curl before the daemon starts sending real traffic. It also matches
the user's explicit direction.

---

## Chunk 1 — Backend MVP

**Repo location:** `/Users/shreyanshkushwaha/dayjob/moodies-backend/` (sibling
to the daemon repo). Separate `go.mod`, separate git repo. User pushes to a
new GitHub repo when ready.

**Stack:** Go + `modernc.org/sqlite` (pure-Go, same as daemon) +
`html/template` for admin UI. No JS framework. Tiny bit of HTMX (vendored
~14KB, no build step) for the toggle UI.

### Layout
```
moodies-backend/
  go.mod
  go.sum
  main.go
  internal/
    api/
      handshake.go       # POST /api/v1/agent/handshake
      events.go          # POST /api/v1/agent/events
      heartbeat.go       # POST /api/v1/agent/heartbeat
      config.go          # POST /api/v1/agent/config
      auth.go            # middleware: agent_token / session_token check
    ui/
      dashboard.go       # GET /
      events.go          # GET /events, GET /events/:id
      config.go          # GET /config, POST /config
      agents.go          # GET /agents
    store/
      schema.sql
      store.go           # *Store with methods: UpsertAgent, InsertEvent, ...
    types/
      manifest.go        # Manifest, ModuleToggles, FilterConfig, RedactionConfig
      event.go           # mirrors daemon's filter.Event for unmarshal/index
  templates/
    layout.html
    dashboard.html
    events_list.html
    event_detail.html
    config.html
    agents.html
  static/
    style.css
    htmx.min.js
  README.md
  .gitignore
```

### SQLite schema (`internal/store/schema.sql`)
```sql
CREATE TABLE IF NOT EXISTS agents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_token TEXT UNIQUE NOT NULL,
  hostname TEXT,
  version TEXT,
  capabilities TEXT,                          -- JSON array as string
  first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  last_heartbeat TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
  event_id TEXT PRIMARY KEY,
  agent_id INTEGER REFERENCES agents(id),
  captured_at TIMESTAMP,
  received_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  endpoint_type TEXT,
  status_code INTEGER,
  host TEXT,
  url TEXT,
  payload_json TEXT NOT NULL                  -- full Event JSON
);
CREATE INDEX IF NOT EXISTS idx_events_received   ON events(received_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_endpoint   ON events(endpoint_type);
CREATE INDEX IF NOT EXISTS idx_events_agent      ON events(agent_id);

CREATE TABLE IF NOT EXISTS manifests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  config_json TEXT NOT NULL,                  -- the Manifest, sans Version
  is_default INTEGER DEFAULT 0,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  session_token TEXT PRIMARY KEY,
  agent_id INTEGER REFERENCES agents(id),
  issued_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP
);
```

### Manifest type (`internal/types/manifest.go`)
```go
type Manifest struct {
    Version             string          `json:"version"`     // sha256 of config_json
    Modules             ModuleToggles   `json:"modules"`
    Filter              FilterConfig    `json:"filter"`
    Redaction           RedactionConfig `json:"redaction"`
    StorageMode         string          `json:"storage_mode"` // "raw" | "hash_only"
    RefreshIntervalSecs int             `json:"refresh_interval_seconds"`
    ExpiresAt           string          `json:"expires_at,omitempty"`
}
type ModuleToggles struct {
    Redaction      bool `json:"redaction"`
    Classification bool `json:"classification"`
    Extraction     bool `json:"extraction"`
    BodyText       bool `json:"body_text"`
}
type FilterConfig struct {
    TargetHosts []string      `json:"target_hosts"`
    Headers     HeadersConfig `json:"headers"`
}
type HeadersConfig struct {
    Allowlist []string `json:"allowlist"`
    Blocklist []string `json:"blocklist"`
}
type RedactionConfig struct {
    Patterns []string `json:"patterns"` // "name:regex"
}

// DefaultManifest mirrors what's in the daemon's
// internal/config/config.go Defaults() — keep in sync.
func DefaultManifest() Manifest { /* ... */ }
```

### Handshake protocol

The user explicitly asked for a handshake between daemon and backend.

```
POST /api/v1/agent/handshake
Content-Type: application/json
{
  "agent_token": "doomsday-testbed-token-2026",
  "hostname": "shreyans-mac",
  "version": "0.1.0",
  "capabilities": ["redaction","classification","extraction","body_text"]
}

→ 200
{
  "session_token": "<uuid>",
  "session_expires_at": "2026-05-04T10:00:00Z",
  "manifest_version": "<sha256>",
  "manifest": { ... full Manifest ... },
  "refresh_interval_seconds": 300
}

→ 401 if agent_token unknown / disabled (future feature)
```

Server-side handshake handler skeleton:
```go
func (h *Handler) Handshake(w http.ResponseWriter, r *http.Request) {
    var req HandshakeRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "bad request", 400); return
    }
    agent, err := h.store.UpsertAgent(req.AgentToken, req.Hostname, req.Version, req.Capabilities)
    if err != nil { /* 500 */ }
    sess, err := h.store.IssueSession(agent.ID, 24*time.Hour)
    if err != nil { /* 500 */ }
    manifest, version, err := h.store.LoadDefaultManifest()
    if err != nil { manifest = types.DefaultManifest() }
    json.NewEncoder(w).Encode(HandshakeResponse{
        SessionToken:           sess.Token,
        SessionExpiresAt:       sess.ExpiresAt.Format(time.RFC3339),
        ManifestVersion:        version,
        Manifest:               manifest,
        RefreshIntervalSeconds: manifest.RefreshIntervalSecs,
    })
}
```

### Other API endpoints

**`POST /api/v1/agent/events`** (idempotent on event_id):
- Body: `{"agent_token" or "session_token": "...", "events": [Event, Event, ...]}`
- Backend: look up agent, for each event upsert into `events` table.
  Index columns derive from the parsed JSON: `endpoint_type`, `status_code`,
  `host`, `url`, `captured_at`. `payload_json` stores the full event verbatim.
- Returns: `{"received": N, "duplicates": M}` or just 200.

**`POST /api/v1/agent/heartbeat`**:
- Body: `{"agent_token": "...", "hostname": "...", "version": "..."}`
- Backend: bump `agents.last_heartbeat`. Return current `manifest_version`
  (sha256) so the daemon can detect drift cheaply.
- Response: `{"manifest_version": "<sha>"}`.

**`POST /api/v1/agent/config`** (refresh):
- Body: same as heartbeat.
- Returns: full `Manifest` JSON. Used by daemon's periodic refresh and on
  manifest_version drift detection.

### Auth middleware (`internal/api/auth.go`)

Both `agent_token` and `session_token` accepted. Look up agent record from
either; reject if neither present.

```go
func (h *Handler) authAgent(r *http.Request, body json.RawMessage) (*store.Agent, error) {
    // try session_token first, then agent_token, both from body
    // returns *store.Agent or err
}
```

### Admin UI

**`GET /`** — dashboard. Live counts (last 1h events, online agents). Links.

**`GET /events`** — paginated table. Columns: timestamp, agent, endpoint_type,
status, host, url (truncated). Filters via query string: `?endpoint_type=`,
`?agent=`, `?status=`. Page size 50.

**`GET /events/:id`** — single event. Pretty-printed JSON. Highlight box for:
user_message.text, assistant_message.text, tool_uses, redactions, policy
classification.

**`GET /agents`** — table: hostname, version, capabilities, last_heartbeat
(green if <2 min ago, yellow <30 min, red older).

**`GET /config`** — feature flag editor. Form fields:
- 4 module toggles (checkboxes, all default true)
- target_hosts (textarea, one per line)
- headers.allowlist (textarea)
- headers.blocklist (textarea)
- redaction.patterns (textarea, one `name:regex` per line)
- storage_mode (select: raw, hash_only)
- refresh_interval_seconds (number, default 300)
- Save button

**`POST /config`** — validate (regex compile + required fields) → marshal to
`config_json` → compute sha256 as `version` → `UPDATE manifests SET ... WHERE
is_default=1` → redirect with success flash.

### Run instructions (in `README.md`)
```
go run ./...                                # serves on :4000
PORT=4000 SQLITE_PATH=./backend.db go run ./...
ngrok http 4000                              # for daemon-facing URL
```

### Backend MVP `.gitignore`
```
/backend.db
/backend.db-*
/moodies-backend
```

---

## Chunk 2 — Daemon refactor (deferred, runs after backend is up)

### 2A. Make build green

1. Fix `cmd/doomsday-daemon/main.go:71` and `:179`. Replace each call:
   ```go
   // OLD:
   proxy.SpawnMitmdump(8080, cfg.StorageMode, outputPath)

   // NEW:
   proxy.SpawnMitmdump(proxy.SpawnOptions{
       Port:        cfg.ListenPort,
       OutputPath:  outputPath,
       TargetHosts: applyCfg.Filter.TargetHosts,  // see 2B for applyCfg
   })
   ```

2. Add `internal/filter/filter.go`:
   ```go
   package filter

   type ApplyConfig struct {
       Modules     ModuleToggles
       Headers     HeaderRules
       Redactions  []CompiledPattern
       StorageMode string  // "raw" | "hash_only"
       Filter      FilterRules  // includes target_hosts (for SpawnMitmdump)
   }

   func Apply(rf *RawFlow, ac ApplyConfig) (*Event, error) {
       e := &Event{
           EventID:       rf.EventID,
           CapturedAt:    rf.CapturedAt,
           Method:        rf.Request.Method,
           Host:          rf.Request.Host,
           URL:           rf.Request.URL,
           StatusCode:    rf.Response.StatusCode,
           DurationMs:    rf.DurationMs,
           RequestBytes:  rf.Request.BodyBytes,
           ResponseBytes: rf.Response.BodyBytes,
       }

       // 1. Headers
       e.RequestHeaders  = filterHeaders(rf.Request.Headers, ac.Headers)
       e.ResponseHeaders = filterHeaders(rf.Response.Headers, ac.Headers)

       var allRedactions []RedactionAudit

       // 2. Redaction
       reqBody, respBody := rf.Request.Body, rf.Response.Body
       if ac.Modules.Redaction {
           var hits []match
           reqBody, hits = RedactString(reqBody, ac.Redactions)
           for _, h := range hits {
               allRedactions = append(allRedactions, RedactionAudit{
                   Pattern: h.Name, Field: "request.body", Count: h.Count,
               })
           }
           respBody, hits = RedactString(respBody, ac.Redactions)
           for _, h := range hits {
               allRedactions = append(allRedactions, RedactionAudit{
                   Pattern: h.Name, Field: "response.body", Count: h.Count,
               })
           }
           // Also redact remaining header values
           for k, v := range e.RequestHeaders {
               nv, hits := RedactString(v, ac.Redactions)
               if len(hits) > 0 {
                   e.RequestHeaders[k] = nv
                   for _, h := range hits {
                       allRedactions = append(allRedactions, RedactionAudit{
                           Pattern: h.Name, Field: "request.headers." + k, Count: h.Count,
                       })
                   }
               }
           }
           // Same for ResponseHeaders.
       }

       // 3. Classification
       if ac.Modules.Classification {
           e.EndpointType, e.PathTemplate = Classify(rf.Request.Method, rf.Request.Path)
       } else {
           e.EndpointType = "unfiltered"
       }

       // 4. Extraction
       storeText := ac.StorageMode == "raw" && ac.Modules.BodyText
       if ac.Modules.Extraction {
           // Override Request/Response.Body in rf with redacted versions
           // before per-endpoint extraction.
           rfCopy := *rf
           rfCopy.Request.Body  = reqBody
           rfCopy.Response.Body = respBody

           e.Client = extractClient(rf.Request.Headers)
           switch e.EndpointType {
           case "completion", "retry_completion":
               e.Completion = extractCompletion(&rfCopy, storeText)
           case "conversation_fetch":
               e.ConversationFetch = extractConversationFetch(&rfCopy)
           case "account":
               e.Account = extractAccount(&rfCopy)
           case "upload":
               e.Upload = extractUpload(&rfCopy)
           }
           // Always set Account from URL even if endpoint doesn't have a handler.
           if e.Account == nil {
               if org := ExtractOrgUUID(rf.Request.Path); org != "" {
                   e.Account = &AccountInfo{OrgUUID: org}
               }
           }
       }

       // 5. Body wrappers (after potential overrides above).
       e.RequestBody  = makeBody(reqBody,  lookupHeader(e.RequestHeaders,  "content-type"), storeText)
       e.ResponseBody = makeBody(respBody, lookupHeader(e.ResponseHeaders, "content-type"), storeText)

       // 6. Policy classification
       e.Policy = ClassifyMatches(allRedactions)
       e.Redactions = allRedactions

       return e, nil
   }
   ```

3. Wire `filter.Apply` into `tailEvents` in `cmd/doomsday-daemon/main.go`.
   Currently it does an ad-hoc partial JSON parse of the line. Replace with:
   ```go
   var rf filter.RawFlow
   if err := json.Unmarshal([]byte(line.Text), &rf); err != nil {
       log.Printf("[tail] decode raw flow: %v", err); continue
   }
   ev, err := filter.Apply(&rf, currentApplyCfg.Load())  // atomic.Pointer
   if err != nil { /* log & skip */ continue }
   payload, _ := json.Marshal(ev)
   capturedAt, _ := time.Parse(time.RFC3339, rf.CapturedAt)
   _ = st.Insert(store.Event{
       EventID:      rf.EventID,
       CapturedAt:   capturedAt,
       EndpointType: ev.EndpointType,
       PayloadJSON:  string(payload),
   })
   ```

4. Delete the legacy:
   ```bash
   rm sanitizer/sanitizer.py sanitizer/tests/test_sanitizer.py
   rmdir sanitizer/tests sanitizer
   ```

5. Update `Formula/moodies.rb` — remove line `libexec.install "sanitizer"`.

### 2B. Daemon-side handshake + manifest

1. Add `internal/config/manifest.go`:
   ```go
   package config

   type Manifest struct { /* mirror backend's */ }

   func ManifestPath() string {
       home, _ := os.UserHomeDir()
       return filepath.Join(home, ".doomsday", "manifest.json")
   }
   func LoadCachedManifest() (*Manifest, error) { /* read+unmarshal */ }
   func (m *Manifest) SaveCache() error { /* atomic write */ }
   func DefaultManifest() *Manifest { /* match Defaults() */ }
   ```

2. Add to `internal/sync/client.go`:
   ```go
   func (c *Client) Handshake(ctx context.Context, hostname, version string) (*config.Manifest, string, time.Time, error)
   func (c *Client) RefreshManifest(ctx context.Context) (*config.Manifest, error)
   ```
   Both POST to `/api/v1/agent/handshake` and `/api/v1/agent/config` respectively.
   `Handshake` also returns and stores `session_token` on the Client.

3. In `cmd/doomsday-daemon/main.go`, replace direct config use with manifest:
   ```go
   // After loading cfg from config.toml...
   syncer := syncclient.New(cfg.BackendURL, cfg.AgentToken, st)
   manifest, version, expires, err := syncer.Handshake(ctx, hostname(), "0.1.0")
   if err != nil {
       manifest, _ = config.LoadCachedManifest()   // fallback chain
       if manifest == nil { manifest = config.DefaultManifest() }
   } else {
       _ = manifest.SaveCache()
   }

   var currentApplyCfg atomic.Pointer[filter.ApplyConfig]
   currentApplyCfg.Store(buildApplyCfg(cfg, manifest))

   // Refresh goroutine
   go refreshManifestLoop(ctx, syncer, &currentApplyCfg, manifest.RefreshIntervalSecs)
   ```

4. Heartbeat behavior change: parse response body for `manifest_version`, on
   drift call `syncer.RefreshManifest()` and `currentApplyCfg.Store(...)`.

### 2C. Write `backend.md`

`/Users/shreyanshkushwaha/dayjob/moodies/backend.md`. Sections:
- Overview (one paragraph)
- Auth model (agent_token v1; future mTLS)
- Endpoints (handshake, events, heartbeat, config) with curl examples
- Manifest schema (full JSON shape, all fields)
- Event schema (full JSON shape — copy from `internal/filter/types.go`)
- One example payload per `endpoint_type`
- Backwards-compat rule: 404/non-2xx triggers daemon fallback to cache → defaults

### 2D. Build, test, commit on `working-daemon`

```
go build ./...                               # exit 0
go test ./...                                # passes
git add -A
git commit -m "refactor(filter): wire Go pipeline into daemon"
git commit -m "feat: backend handshake + manifest fetch + backend.md"
git push origin working-daemon
```

---

## Files modified or added

### Backend repo (new — `/Users/shreyanshkushwaha/dayjob/moodies-backend/`)
See "Layout" under Chunk 1.

### Daemon repo (existing — `/Users/shreyanshkushwaha/dayjob/moodies/`)
**New:**
- `internal/filter/filter.go`
- `internal/filter/filter_test.go` (smoke tests for completion + redaction)
- `internal/config/manifest.go`
- `backend.md`

**Modified:**
- `cmd/doomsday-daemon/main.go` (fix SpawnMitmdump, add handshake + refresh,
  atomic applyCfg)
- `internal/sync/client.go` (add Handshake, RefreshManifest; heartbeat parses
  manifest_version response)
- `internal/config/config.go` (helper to derive `filter.ApplyConfig` from
  Config + Manifest)
- `Formula/moodies.rb` (drop sanitizer install line)

**Deleted:**
- `sanitizer/sanitizer.py`
- `sanitizer/tests/test_sanitizer.py`
- `sanitizer/` directory

---

## Critical files to read when implementing

- `internal/filter/types.go` — Event schema. Backend MUST match this for
  parsing `payload_json`.
- `internal/filter/extract.go` — already handles both public-API and web-app
  payload shapes. The user-text extraction bug from the old sanitizer is
  fixed here.
- `internal/proxy/mitmdump.go:47` — `SpawnMitmdump(opts SpawnOptions)`.
- `internal/sync/client.go:24-32` — pattern for new sync methods (Handshake,
  RefreshManifest follow this style).
- `cmd/doomsday-daemon/main.go:71,179` — broken call sites.
- `~/.doomsday/raw_events.jsonl` (20 events from prior session, OLD shape) —
  not directly reusable for backend testing since the JSON format will change
  with the refactor.

---

## Verification

### Backend (after Chunk 1)
1. `go build ./...` exits 0; `go run ./...` starts on :4000.
2. ```
   curl -X POST localhost:4000/api/v1/agent/handshake \
     -H 'content-type: application/json' \
     -d '{"agent_token":"test","hostname":"x","version":"0.1.0","capabilities":[]}'
   ```
   → 200 with manifest + session.
3. Construct an Event-shaped JSON by hand and POST to `/api/v1/agent/events`;
   confirm row appears at `GET /events`.
4. Browser visits `http://localhost:4000/` → dashboard. `/events` → list.
   `/events/:id` → JSON detail. `/config` → form, edit toggles, save
   round-trips and reload shows the new values.
5. `ngrok http 4000` → use the public URL as `backend_url` in daemon config,
   repeat (3) against the public URL.

### Daemon (after Chunk 2)
1. `go build ./...` exits 0.
2. `go test ./...` passes (existing + new filter smoke tests).
3. With backend running and `backend_url` set: install daemon, send a curl
   through the proxy, verify event arrives at backend's `/events` page with
   correct `endpoint_type`, redactions, structured fields.
4. In backend's `/config`, toggle `body_text` off, save. Within
   `refresh_interval_seconds`, the daemon should pick up the change. Send
   another event; verify next event has hashes only — no body text.
5. Stop the backend mid-session, send more events. Verify daemon keeps
   capturing (uses cached manifest), backlog drains when backend returns.

---

## Risks

- **Manifest cache going stale**: mitigated by `expires_at` field — daemon
  discards expired manifest and falls back to defaults.
- **Modules disabling extraction silently breaks backend consumers**:
  `backend.md` must document the omitempty-driven shape so consumers know to
  expect missing fields when modules are off.
- **agent_token leakage**: shared-secret is fine for v1; backend.md notes
  mTLS / signed manifests as future work.
- **Heavy refactor — limited test coverage**: minimal smoke tests cover happy
  paths only; the per-endpoint extractors port a lot of Python logic; edge
  cases (empty bodies, malformed SSE, partial JSON) are best-effort.
- **Admin UI is unauthenticated in MVP**: anyone with the URL can edit flags.
  Flag in README as v1 caveat; user controls who has the ngrok URL.

## What's deferred (NOT in this plan)

- Multi-agent / per-agent manifests (every agent gets the same default in MVP)
- Auth hardening (mTLS, signed manifests, token rotation)
- Manifest schema versioning (single field — no negotiation)
- Backend deployment beyond local + ngrok
- Authentication on the admin UI
- E2E tests beyond manual verification
- Real GitHub repo creation for the backend (user does this manually after
  first commit; I just `git init` locally)

---

## Useful state for future sessions

- ngrok URL (will rotate): `https://7368-2406-7400-11d-4675-5cf-4a79-4874-26ac.ngrok-free.app`
- agent_token (testbed): `doomsday-testbed-token-2026`
- Daemon repo branch: `working-daemon` (2 commits ahead of main)
- Backend repo: doesn't exist yet; user will create on GitHub after `git init`
- User preference: prefers seeing minimal end-to-end working before completing
  refactors. Build the smallest backend first, validate via curl + UI, then
  return to client refactors.
