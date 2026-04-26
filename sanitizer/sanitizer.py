"""
Doomsday mitmproxy addon — captures Anthropic/Claude traffic and writes
structured JSONL to ~/.doomsday/audit.jsonl.

Run with:
  mitmdump -s sanitizer.py
  STORAGE_MODE=hash_only mitmdump -s sanitizer.py
"""

import hashlib
import json
import os
import re
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mitmproxy import http


# ── Config ──────────────────────────────────────────────────────────────────

STORAGE_MODE = os.environ.get("STORAGE_MODE", "raw")  # "raw" | "hash_only"
OUTPUT_PATH = Path(
    os.environ.get("DOOMSDAY_OUTPUT", Path.home() / ".doomsday" / "audit.jsonl")
)
ERRORS_PATH = OUTPUT_PATH.parent / "parse_errors.jsonl"

TARGET_HOSTS = re.compile(r"(^|\.)(anthropic\.com|claude\.ai|claudeusercontent\.com)$")

ALLOWED_HEADERS = {
    "content-type",
    "anthropic-client-app",
    "anthropic-client-version",
    "anthropic-client-platform",
    "user-agent",
    "x-stainless-package-version",
}

ALWAYS_DROP_HEADERS = {
    "cookie", "authorization", "x-api-key", "sessionkey", "routinghint",
    "cf_clearance", "set-cookie",
}

# ── Policy regexes ──────────────────────────────────────────────────────────

POLICY_PATTERNS = {
    "aws_key":        re.compile(r"AKIA[0-9A-Z]{16}"),
    "github_pat":     re.compile(r"gh[psoru]_[A-Za-z0-9_]{36,255}"),
    "anthropic_key":  re.compile(r"sk-ant-(?:api|sid)\d+-[A-Za-z0-9_-]+"),
    "email":          re.compile(r"[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}"),
    "us_ssn":         re.compile(r"\b\d{3}-\d{2}-\d{4}\b"),
}

SECRET_PATTERNS = {"aws_key", "github_pat", "anthropic_key"}
PII_PATTERNS = {"email", "us_ssn"}

# ── Endpoint registry ───────────────────────────────────────────────────────

ENDPOINTS = [
    ("POST", re.compile(r"^/v1/messages$"),                                        "completion"),
    ("POST", re.compile(r"^/api/organizations/[^/]+/chat_conversations/[^/]+/completion$"), "completion"),
    ("POST", re.compile(r"^/api/organizations/[^/]+/chat_conversations/[^/]+/retry_completion$"), "retry_completion"),
    ("GET",  re.compile(r"^/api/organizations/[^/]+/chat_conversations$"),          "conversation_list"),
    ("GET",  re.compile(r"^/api/organizations/[^/]+/chat_conversations/[^/]+$"),    "conversation_fetch"),
    ("POST", re.compile(r"^/api/organizations/[^/]+/upload$"),                     "upload"),
    ("GET",  re.compile(r"^/api/account$"),                                         "account"),
]


# ── Tool classification ─────────────────────────────────────────────────────

BUILTIN_PREFIXES = ("web_", "bash", "computer_", "str_replace_", "text_editor_", "str_edit_", "file_")

def classify_tool(name: str) -> str:
    for p in BUILTIN_PREFIXES:
        if name.startswith(p) or name == p.rstrip("_"):
            return "anthropic_builtin"
    if ":" in name:
        return "mcp"
    return "unknown"

def extract_mcp_server(name: str) -> str | None:
    return name.split(":")[0] if ":" in name else None


# ── SSE parser ──────────────────────────────────────────────────────────────

def parse_sse(raw: bytes) -> dict:
    """Walk SSE lines and accumulate assistant text, tool_uses, stop_reason, usage."""
    text_parts: list[str] = []
    tool_uses: list[dict] = {}  # index -> {tool_name, tool_input_parts, tool_use_id}
    stop_reason = None
    input_tokens = 0
    output_tokens = 0

    for line in raw.decode("utf-8", errors="replace").splitlines():
        if not line.startswith("data: "):
            continue
        payload = line[6:]
        if payload == "[DONE]":
            break
        try:
            data = json.loads(payload)
        except json.JSONDecodeError:
            continue

        etype = data.get("type", "")

        if etype == "content_block_start":
            block = data.get("content_block", {})
            idx = data.get("index", 0)
            if block.get("type") == "tool_use":
                tool_uses[idx] = {
                    "tool_name": block.get("name", ""),
                    "tool_use_id": block.get("id", ""),
                    "input_parts": [],
                }

        elif etype == "content_block_delta":
            delta = data.get("delta", {})
            idx = data.get("index", 0)
            dtype = delta.get("type", "")
            if dtype == "text_delta":
                text_parts.append(delta.get("text", ""))
            elif dtype == "input_json_delta":
                if idx in tool_uses:
                    tool_uses[idx]["input_parts"].append(delta.get("partial_json", ""))

        elif etype == "message_delta":
            delta = data.get("delta", {})
            stop_reason = delta.get("stop_reason", stop_reason)
            usage = data.get("usage", {})
            output_tokens = usage.get("output_tokens", output_tokens)

        elif etype == "message_start":
            msg = data.get("message", {})
            usage = msg.get("usage", {})
            input_tokens = usage.get("input_tokens", input_tokens)

    # Resolve tool input
    tool_list = []
    for idx in sorted(tool_uses.keys()):
        tu = tool_uses[idx]
        raw_input = "".join(tu.get("input_parts", []))
        try:
            parsed_input = json.loads(raw_input) if raw_input else {}
        except json.JSONDecodeError:
            parsed_input = {"_raw": raw_input}
        tool_list.append({
            "tool_name": tu["tool_name"],
            "tool_input": parsed_input,
            "tool_use_id": tu["tool_use_id"],
        })

    return {
        "content_text": "".join(text_parts),
        "tool_uses": tool_list,
        "stop_reason": stop_reason,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
    }


# ── Content helpers ─────────────────────────────────────────────────────────

def make_content(text: str | None, char_count: int = 0) -> dict:
    if text is None:
        return {"content_sha256": None, "char_count": char_count}
    sha = hashlib.sha256(text.encode()).hexdigest()
    result: dict = {"content_sha256": sha, "char_count": len(text)}
    if STORAGE_MODE == "raw":
        result["content_text"] = text
    return result


def classify_policy(text: str) -> dict:
    if not text:
        return {"classification": "clean", "matches": []}
    matches = [name for name, pat in POLICY_PATTERNS.items() if pat.search(text)]
    if any(m in SECRET_PATTERNS for m in matches):
        classification = "flagged_secret"
    elif any(m in PII_PATTERNS for m in matches):
        classification = "flagged_pii"
    else:
        classification = "clean"
    return {"classification": classification, "matches": matches}


def filter_headers(flow_headers) -> dict:
    result = {}
    for k, v in flow_headers.items():
        lower = k.lower()
        if lower in ALWAYS_DROP_HEADERS:
            continue
        if lower in ALLOWED_HEADERS:
            result[k] = v
    return result


def extract_client(headers: dict) -> dict:
    app = headers.get("anthropic-client-app") or headers.get("Anthropic-Client-App") or ""
    version = headers.get("anthropic-client-version") or headers.get("Anthropic-Client-Version") or ""
    platform = headers.get("anthropic-client-platform") or headers.get("Anthropic-Client-Platform") or ""
    if not app:
        ua = headers.get("user-agent", "")
        if "claude" in ua.lower():
            app = "browser"
        elif "python" in ua.lower():
            app = "claude-cli"
    return {"app": app, "version": version, "platform": platform}


# ── Handlers ────────────────────────────────────────────────────────────────

def extract_completion(flow: http.HTTPFlow) -> dict:
    req_body = {}
    try:
        req_body = json.loads(flow.request.content or b"{}")
    except json.JSONDecodeError:
        pass

    model = req_body.get("model", "")
    messages = req_body.get("messages", [])
    system = req_body.get("system", None)
    tools_raw = req_body.get("tools", [])

    # Extract last user message
    user_text = None
    attachments = []
    for msg in reversed(messages):
        if msg.get("role") == "user":
            content = msg.get("content", "")
            if isinstance(content, str):
                user_text = content
            elif isinstance(content, list):
                parts = []
                for block in content:
                    if isinstance(block, dict):
                        if block.get("type") == "text":
                            parts.append(block.get("text", ""))
                        elif block.get("type") in ("image", "document"):
                            src = block.get("source", {})
                            attachments.append({
                                "file_uuid": src.get("url", ""),
                                "filename": block.get("name", ""),
                                "mime": src.get("media_type", ""),
                            })
                user_text = "\n".join(parts)
            break

    # Tools declared
    tools_declared = []
    for t in tools_raw:
        name = t.get("name") or t.get("function", {}).get("name", "")
        tools_declared.append({
            "name": name,
            "category": classify_tool(name),
            "mcp_server": extract_mcp_server(name),
        })

    # Parse response
    resp_body = {}
    assistant_text = None
    tool_uses = []
    stop_reason = None
    input_tokens = 0
    output_tokens = 0

    ct = (flow.response.headers.get("content-type") or "") if flow.response else ""
    if "text/event-stream" in ct:
        sse = parse_sse(flow.response.content or b"")
        assistant_text = sse["content_text"]
        tool_uses = sse["tool_uses"]
        stop_reason = sse["stop_reason"]
        input_tokens = sse["input_tokens"]
        output_tokens = sse["output_tokens"]
    else:
        try:
            resp_body = json.loads(flow.response.content or b"{}")
        except json.JSONDecodeError:
            pass
        stop_reason = resp_body.get("stop_reason")
        usage = resp_body.get("usage", {})
        input_tokens = usage.get("input_tokens", 0)
        output_tokens = usage.get("output_tokens", 0)
        for block in resp_body.get("content", []):
            if block.get("type") == "text":
                assistant_text = (assistant_text or "") + block.get("text", "")
            elif block.get("type") == "tool_use":
                name = block.get("name", "")
                tool_uses.append({
                    "tool_name": name,
                    "tool_input": block.get("input", {}),
                    "tool_use_id": block.get("id", ""),
                })

    path = flow.request.path
    conv_uuid = None
    if "/chat_conversations/" in path:
        parts = path.split("/chat_conversations/")
        if len(parts) > 1:
            conv_uuid = parts[1].split("/")[0]

    user_msg = {**make_content(user_text), "role": "user"}
    if attachments:
        user_msg["attachments"] = attachments

    asst_msg = {
        **make_content(assistant_text),
        "role": "assistant",
        "stop_reason": stop_reason,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
    }
    if tool_uses:
        asst_msg["tool_uses"] = tool_uses

    return {
        "model": model,
        "conversation_uuid": conv_uuid,
        "message_uuid": resp_body.get("id"),
        "parent_message_uuid": req_body.get("parent_message_uuid"),
        "user_message": user_msg,
        "assistant_message": asst_msg,
        "system_prompt_present": system is not None,
        "tools_declared": tools_declared,
    }


def extract_upload(flow: http.HTTPFlow) -> dict:
    size = int(flow.request.headers.get("content-length", 0))
    ct = flow.request.headers.get("content-type", "")
    sha = None
    if size < 10 * 1024 * 1024:
        sha = hashlib.sha256(flow.request.content or b"").hexdigest()
    return {
        "filename": "",
        "mime_type": ct,
        "size_bytes": size,
        "file_sha256": sha,
    }


def extract_account(flow: http.HTTPFlow) -> dict:
    try:
        body = json.loads(flow.response.content or b"{}")
    except json.JSONDecodeError:
        body = {}
    return {
        "account_uuid": body.get("uuid") or body.get("id") or body.get("account_id"),
        "org_uuid": body.get("organizations", [{}])[0].get("uuid") if body.get("organizations") else None,
    }


def extract_conversation_fetch(flow: http.HTTPFlow) -> dict:
    try:
        body = json.loads(flow.response.content or b"{}")
    except json.JSONDecodeError:
        body = {}
    return {
        "title": body.get("name") or body.get("title"),
        "message_count": len(body.get("chat_messages", [])),
    }


# ── Addon class ─────────────────────────────────────────────────────────────

class DoomsdaySanitizer:
    def __init__(self):
        OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)

    def response(self, flow: http.HTTPFlow) -> None:
        host = flow.request.pretty_host
        if not TARGET_HOSTS.search(host):
            return

        method = flow.request.method
        path = flow.request.path.split("?")[0]  # strip query string

        endpoint_type = None
        handler = None
        path_template = None

        for ep_method, pattern, ep_type in ENDPOINTS:
            if method == ep_method and pattern.fullmatch(path):
                endpoint_type = ep_type
                path_template = pattern.pattern
                break

        if endpoint_type is None:
            self._write_error(flow, "no matching endpoint pattern")
            return

        start = getattr(flow, "timestamp_start", time.time())
        end = getattr(flow, "timestamp_end", time.time())

        headers = filter_headers(flow.request.headers)
        client = extract_client(dict(flow.request.headers))

        # Account: extract from URL org component
        parts = path.split("/")
        org_uuid = None
        if "organizations" in parts:
            idx = parts.index("organizations")
            if idx + 1 < len(parts):
                org_uuid = parts[idx + 1]

        account = {"account_uuid": None, "org_uuid": org_uuid}

        event: dict[str, Any] = {
            "event_id": str(uuid.uuid4()),
            "captured_at": datetime.now(timezone.utc).isoformat(),
            "endpoint_type": endpoint_type,
            "method": method,
            "path_template": path_template,
            "status_code": flow.response.status_code if flow.response else 0,
            "duration_ms": int((end - start) * 1000),
            "request_bytes": len(flow.request.content or b""),
            "response_bytes": len(flow.response.content or b"") if flow.response else 0,
            "account": account,
            "client": client,
            "policy": {"classification": "clean", "matches": []},
            "annotations": {
                "completed": None,
                "frustration_score": None,
                "prompt_quality": None,
                "annotated_at": None,
                "annotated_by": None,
            },
        }

        try:
            if endpoint_type in ("completion", "retry_completion"):
                comp = extract_completion(flow)
                event["completion"] = comp
                user_text = comp.get("user_message", {}).get("content_text") or ""
                event["policy"] = classify_policy(user_text)
            elif endpoint_type == "upload":
                event["upload"] = extract_upload(flow)
            elif endpoint_type == "account":
                acc = extract_account(flow)
                event["account"].update(acc)
            elif endpoint_type in ("conversation_list", "conversation_fetch"):
                event["conversation_fetch"] = extract_conversation_fetch(flow)
        except Exception as exc:
            self._write_error(flow, str(exc))
            return

        self._write_event(event)

    def _write_event(self, event: dict) -> None:
        try:
            with open(OUTPUT_PATH, "a") as f:
                f.write(json.dumps(event) + "\n")
        except Exception as exc:
            print(f"[doomsday] write event error: {exc}")

    def _write_error(self, flow: http.HTTPFlow, exc: str) -> None:
        err = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "path": flow.request.path,
            "method": flow.request.method,
            "status": flow.response.status_code if flow.response else 0,
            "error": exc,
        }
        try:
            with open(ERRORS_PATH, "a") as f:
                f.write(json.dumps(err) + "\n")
        except Exception:
            pass


def load(l=None):
    return DoomsdaySanitizer()


addons = [DoomsdaySanitizer()]
