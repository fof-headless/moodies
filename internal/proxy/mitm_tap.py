"""
Tap addon for mitmproxy. Filters by host suffix, then dumps every flow as
one raw JSON line to DOOMSDAY_OUTPUT (default ~/.doomsday/raw_events.jsonl).

ALL further processing — classification, extraction, redaction, schema
shaping — happens in the Go daemon's internal/filter package. This script
intentionally stays minimal so the data the daemon sees is faithful to the
wire.

Env (set by the daemon when it spawns mitmdump):
  DOOMSDAY_OUTPUT        Output JSONL path.
  DOOMSDAY_TARGET_HOSTS  Comma-separated host list. Entries starting with
                         '.' suffix-match (.foo.com matches sub.foo.com).
                         Entries without a leading dot are exact-match.
"""

import json
import os
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

from mitmproxy import http

OUTPUT_PATH = Path(os.environ.get(
    "DOOMSDAY_OUTPUT",
    Path.home() / ".doomsday" / "raw_events.jsonl",
))
ERRORS_PATH = OUTPUT_PATH.parent / "tap_errors.jsonl"

_targets_env = os.environ.get(
    "DOOMSDAY_TARGET_HOSTS",
    ".anthropic.com,claude.ai,.claude.ai,.claudeusercontent.com",
)
TARGETS = [h.strip().lower() for h in _targets_env.split(",") if h.strip()]


def host_matches(host: str) -> bool:
    h = host.lower()
    for t in TARGETS:
        if t.startswith("."):
            if h.endswith(t) or h == t[1:]:
                return True
        elif h == t:
            return True
    return False


def _decode(b: bytes | None) -> str:
    if not b:
        return ""
    try:
        return b.decode("utf-8", errors="replace")
    except Exception:
        return ""


class Tap:
    def __init__(self) -> None:
        OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)

    def response(self, flow: http.HTTPFlow) -> None:
        try:
            host = flow.request.pretty_host
            if not host_matches(host):
                return

            req = flow.request
            resp = flow.response
            start = getattr(flow, "timestamp_start", time.time())
            end = getattr(flow, "timestamp_end", time.time())

            event = {
                "event_id": str(uuid.uuid4()),
                "captured_at": datetime.now(timezone.utc).isoformat(),
                "request": {
                    "method": req.method,
                    "scheme": req.scheme,
                    "host": host,
                    "path": req.path,
                    "url": req.pretty_url,
                    "headers": dict(req.headers.items()),
                    "body": _decode(req.content),
                    "body_bytes": len(req.content or b""),
                },
                "response": {
                    "status_code": resp.status_code if resp else 0,
                    "headers": dict(resp.headers.items()) if resp else {},
                    "body": _decode(resp.content if resp else None),
                    "body_bytes": len(resp.content or b"") if resp else 0,
                },
                "duration_ms": int((end - start) * 1000),
            }

            with open(OUTPUT_PATH, "a") as f:
                f.write(json.dumps(event) + "\n")
        except Exception as exc:
            try:
                with open(ERRORS_PATH, "a") as f:
                    f.write(json.dumps({
                        "timestamp": datetime.now(timezone.utc).isoformat(),
                        "path": getattr(flow.request, "path", "?"),
                        "error": str(exc),
                    }) + "\n")
            except Exception:
                pass


addons = [Tap()]
