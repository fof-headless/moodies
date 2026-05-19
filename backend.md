# backend.md — daemon ⇄ backend contract

This document is the wire contract between the moodies daemon and a backend.
The daemon owns this file; backend implementations (the reference backend
lives at `moodies-backend/`) MUST conform.

## Overview

The daemon captures Anthropic-domain HTTPS traffic via mitmproxy, runs it
through the in-process `internal/filter` pipeline, and ships the resulting
structured `Event`s to the backend. Policy (which patterns to redact, which
modules to enable, which storage mode to use) is owned by the backend and
delivered to the daemon as a `Manifest` at handshake time and on refresh.

The daemon is resilient to backend outages: it caches the last-seen manifest
to disk, falls back to defaults if no cache is present, and continues
buffering events to SQLite until sync succeeds.

## Auth

v1 uses a shared-secret `agent_token` configured in
`~/.doomsday/config.toml`. After a successful `/handshake` the backend issues
a `session_token`; the daemon includes it on subsequent requests, but
`agent_token` alone is also accepted. There is no rotation; rotating means
editing the config file and restarting the daemon.

Future: mTLS, signed manifests, token rotation. Out of scope for v1.

## Endpoints

All endpoints are POST, JSON in / JSON out. The daemon retries on transport
failure but treats any non-200 response as authoritative — no automatic retry
on 4xx or 5xx (the next scheduled tick will try again).

### POST /api/v1/agent/handshake

Daemon → backend: identifies itself and asks for its manifest. Called once at
startup (and again if the session expires).

Request:
```json
{
  "agent_token": "doomsday-testbed-token-2026",
  "hostname": "shreyans-mac",
  "version": "0.1.0",
  "capabilities": ["redaction", "classification", "extraction", "body_text"]
}
```

Response (200):
```json
{
  "session_token": "<uuid>",
  "session_expires_at": "2026-05-10T12:00:00Z",
  "manifest_version": "<sha256 of manifest config_json>",
  "manifest": { /* full Manifest, see schema below */ },
  "refresh_interval_seconds": 300
}
```

Response (401): `agent_token` unknown / disabled.

Daemon behaviour on non-200: load `~/.doomsday/manifest.json` if present, else
fall back to `config.DefaultManifest()`. Continues capturing in either case.

### POST /api/v1/agent/events

Daemon → backend: ships a batch of buffered events. Called every 30s by the
sync loop; up to 100 events per batch.

Request:
```json
{
  "agent_token": "...",
  "session_token": "<optional, sent if available>",
  "events": [ /* array of Event, see schema below */ ]
}
```

Response (200): treat as success, mark events synced. The body MAY contain
`{"received": N, "duplicates": M}` for observability; the daemon does not
parse it.

Idempotency: events have stable `event_id`s (UUIDs minted by the tap). The
backend MUST treat `INSERT` as idempotent on `event_id`. The daemon may
re-send the same event after a flaky network — deduping is the backend's
responsibility.

### POST /api/v1/agent/heartbeat

Daemon → backend: liveness ping every 5min. Backend uses this to mark agents
"online" and to advertise the current manifest version so the daemon can
detect drift cheaply.

Request:
```json
{
  "agent_token": "...",
  "hostname": "shreyans-mac",
  "version": "0.1.0"
}
```

Response (200):
```json
{
  "manifest_version": "<sha256>"
}
```

Daemon behaviour: if the returned `manifest_version` differs from the cached
one, the daemon issues `/config` to fetch the new manifest and hot-swaps it
into the running pipeline.

### POST /api/v1/agent/config

Daemon → backend: pull the latest manifest. Called on heartbeat-detected
drift and from a periodic refresh loop running at the cadence specified by
`refresh_interval_seconds`.

Request:
```json
{
  "agent_token": "...",
  "session_token": "<optional>"
}
```

Response (200): full `Manifest` JSON (same shape as the `manifest` field of
the handshake response).

## Manifest schema

```json
{
  "version": "<sha256 of canonicalised config_json>",
  "modules": {
    "redaction": true,
    "classification": true,
    "extraction": true,
    "body_text": true
  },
  "filter": {
    "target_hosts": [".anthropic.com", "claude.ai", ".claude.ai", ".claudeusercontent.com"],
    "headers": {
      "allowlist": ["content-type", "user-agent", "anthropic-client-app", ...],
      "blocklist": ["cookie", "set-cookie", "authorization", "x-api-key", ...]
    }
  },
  "redaction": {
    "patterns": [
      "anthropic_key:sk-ant-(?:api|sid)\\d+-[A-Za-z0-9_-]+",
      "aws_key:AKIA[0-9A-Z]{16}",
      "github_pat:gh[psoru]_[A-Za-z0-9_]{36,255}",
      "email:[a-zA-Z0-9._%+\\-]+@[a-zA-Z0-9.\\-]+\\.[a-zA-Z]{2,}",
      "us_ssn:\\b\\d{3}-\\d{2}-\\d{4}\\b"
    ]
  },
  "storage_mode": "raw",
  "refresh_interval_seconds": 300,
  "expires_at": "2026-06-09T00:00:00Z"
}
```

Field semantics:

- `version` — sha256 of the canonical JSON of every other field. The daemon
  uses this for cheap drift checks; the backend MUST keep it stable as long
  as the policy is unchanged.
- `modules` — pipeline toggles. Disabling `extraction` means no
  endpoint-specific fields (Completion / ConversationFetch / Upload / Account)
  on the event. Disabling `body_text` keeps SHA + char count but drops the
  decoded text. Disabling `classification` sets `endpoint_type` to
  `"unfiltered"` and skips path-template matching.
- `filter.target_hosts` — passed to mitmproxy via env so the addon drops
  non-matching flows before they ever hit the JSONL tap. Entries starting
  with `.` suffix-match (`.foo.com` matches `sub.foo.com`); others are
  exact-match.
- `filter.headers.allowlist` — case-insensitive header-name allowlist applied
  AFTER blocklist removal. Empty allowlist means "everything not blocklisted".
- `redaction.patterns` — `name:regex` strings. Patterns that fail to compile
  are logged and skipped; the daemon does not reject the manifest. Pattern
  names `anthropic_key`, `aws_key`, `github_pat` are classified as secrets;
  `email`, `us_ssn` as PII; anything else is just a redaction.
- `storage_mode` — `"raw"` keeps decoded body text; `"hash_only"` keeps only
  SHA256 + char counts. Independent of the `body_text` module toggle:
  `hash_only` overrides `body_text=true`.
- `refresh_interval_seconds` — daemon's periodic-refresh cadence. Defaults
  to 300 if missing.
- `expires_at` — RFC3339. Optional. If set and in the past, the daemon
  discards the cached manifest and falls back to defaults on next start.

## Event schema

The full shape is defined in `internal/filter/types.go`. JSON wire form:

```json
{
  "event_id": "uuid",
  "captured_at": "2026-05-09T12:00:00Z",
  "endpoint_type": "completion",
  "path_template": "^/v1/messages$",
  "method": "POST",
  "host": "api.anthropic.com",
  "url": "https://api.anthropic.com/v1/messages",
  "status_code": 200,
  "duration_ms": 1234,
  "request_bytes": 512,
  "response_bytes": 4096,

  "request_headers":  {"content-type": "application/json", ...},
  "response_headers": {"content-type": "text/event-stream", ...},

  "request_body":  {"sha256": "...", "chars": 512, "text": "...", "content_type": "application/json"},
  "response_body": {"sha256": "...", "chars": 4096, "text": "...", "content_type": "text/event-stream"},

  "client":  {"app": "claude-cli", "version": "0.4.0", "platform": "darwin"},
  "account": {"org_uuid": "...", "account_uuid": "..."},

  "completion":         { ... },
  "conversation_fetch": { ... },
  "upload":             { ... },

  "policy":     {"classification": "clean", "matches": []},
  "redactions": [{"pattern": "email", "field": "request.body", "count": 1}]
}
```

Field semantics:

- `endpoint_type` — one of `completion`, `retry_completion`,
  `conversation_list`, `conversation_fetch`, `upload`, `account`,
  `unknown`, or `unfiltered` (classification disabled).
- Exactly one of `completion` / `conversation_fetch` / `upload` is set per
  event (depending on `endpoint_type`); `account` may be set independently
  whenever an org UUID is in the path.
- `request_body.text` and `response_body.text` are omitted in `hash_only`
  storage mode and when `modules.body_text=false`.
- `policy.classification` — `clean` | `flagged_secret` | `flagged_pii`.
  `flagged_secret` wins over `flagged_pii` if both are present.
- `redactions[].field` — `request.body`, `response.body`, or
  `request.headers.<lowercased-name>` / `response.headers.<lowercased-name>`.

### Endpoint-specific fields

`completion` / `retry_completion`:
```json
{
  "model": "claude-3-5-sonnet",
  "conversation_uuid": "<uuid>",
  "message_uuid": "msg_xyz",
  "parent_message_uuid": "<uuid>",
  "system_prompt_present": true,
  "tools_declared": [{"name": "bash", "category": "anthropic_builtin", "mcp_server": ""}],
  "user_message":      {"role": "user", "sha256": "...", "chars": 42, "text": "...", "attachments": []},
  "assistant_message": {"role": "assistant", "sha256": "...", "chars": 99, "text": "...",
                         "stop_reason": "end_turn", "input_tokens": 5, "output_tokens": 7,
                         "tool_uses": [{"tool_name": "...", "tool_input": {...}, "tool_use_id": "..."}]}
}
```

`conversation_fetch`:
```json
{"title": "Friendly Chat Title", "message_count": 12}
```

`upload`:
```json
{"filename": "...", "mime_type": "image/png", "size_bytes": 12345, "file_sha256": "..."}
```

`account`:
```json
{"org_uuid": "...", "account_uuid": "..."}
```

## Backwards compatibility

- The daemon treats any non-2xx from `/handshake`, `/config`, or
  `/heartbeat` as "fall back to cached manifest, else defaults". A backend
  that 404s these endpoints will not break event capture; events will buffer
  to local SQLite and the daemon will keep retrying.
- New fields on the `Event` struct: backend SHOULD persist `payload_json`
  verbatim (full Event JSON as-string) and decode index columns on the side,
  so adding fields on the daemon is non-breaking.
- New fields on the `Manifest`: daemon ignores unknown fields (standard JSON
  decoding). Removing fields the daemon expects falls back to defaults via
  `Defaults()` in `internal/config/config.go`.

## Reference implementation

The companion backend at `moodies-backend/` (sibling repo) implements this
contract end-to-end with an admin UI for editing the default manifest.
See `moodies-backend/README.md` for run instructions.
