"""
Unit tests for the Doomsday sanitizer.

Run with: cd agent/sanitizer && python -m pytest tests/ -v
"""

import hashlib
import json
import os
import sys
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

# Add parent directory so we can import sanitizer
sys.path.insert(0, str(Path(__file__).parent.parent))

# Patch mitmproxy before import
import types
mitmproxy_mock = types.ModuleType("mitmproxy")
mitmproxy_http_mock = types.ModuleType("mitmproxy.http")
sys.modules.setdefault("mitmproxy", mitmproxy_mock)
sys.modules.setdefault("mitmproxy.http", mitmproxy_http_mock)

import sanitizer as san


# ── Helpers ──────────────────────────────────────────────────────────────────

def make_flow(
    method="POST",
    path="/v1/messages",
    host="api.anthropic.com",
    req_body=None,
    resp_body=None,
    resp_headers=None,
    status_code=200,
):
    flow = MagicMock()
    flow.request.method = method
    flow.request.path = path
    flow.request.pretty_host = host
    flow.request.content = json.dumps(req_body or {}).encode() if req_body else b"{}"
    flow.request.headers = {
        "content-type": "application/json",
        "anthropic-client-app": "claude-cli",
        "anthropic-client-version": "1.0.0",
        "anthropic-client-platform": "darwin",
    }
    flow.response.status_code = status_code
    resp_headers = resp_headers or {}
    resp_headers.setdefault("content-type", "application/json")
    flow.response.headers = resp_headers

    if resp_body is not None:
        flow.response.content = json.dumps(resp_body).encode()
    else:
        flow.response.content = b"{}"

    flow.timestamp_start = 0.0
    flow.timestamp_end = 0.1
    return flow


# ── Tests ────────────────────────────────────────────────────────────────────

class TestCredentialNeverLeaks:
    def test_session_key_never_in_output(self, tmp_path):
        outfile = tmp_path / "audit.jsonl"
        with patch.object(san, "OUTPUT_PATH", outfile), \
             patch.object(san, "ERRORS_PATH", tmp_path / "errors.jsonl"):
            addon = san.DoomsdaySanitizer()
            flow = make_flow(
                req_body={
                    "model": "claude-opus-4-7",
                    "messages": [{"role": "user", "content": "hello"}],
                },
                resp_body={
                    "id": "msg_001",
                    "content": [{"type": "text", "text": "hi there"}],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 10, "output_tokens": 5},
                },
            )
            flow.request.headers["Cookie"] = "sessionKey=supersecret123"
            addon.response(flow)

        assert outfile.exists()
        content = outfile.read_text()
        assert "supersecret123" not in content
        assert "sessionkey" not in content.lower()
        assert "sessionKey" not in content

    def test_api_key_never_in_output(self, tmp_path):
        outfile = tmp_path / "audit.jsonl"
        with patch.object(san, "OUTPUT_PATH", outfile), \
             patch.object(san, "ERRORS_PATH", tmp_path / "errors.jsonl"):
            addon = san.DoomsdaySanitizer()
            flow = make_flow(
                req_body={
                    "model": "claude-opus-4-7",
                    "messages": [{"role": "user", "content": "hi"}],
                },
                resp_body={
                    "content": [{"type": "text", "text": "hello"}],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 5, "output_tokens": 5},
                },
            )
            flow.request.headers["x-api-key"] = "sk-ant-api03-secretkey"
            flow.request.headers["Authorization"] = "Bearer sk-ant-api03-secretkey"
            addon.response(flow)

        content = outfile.read_text()
        assert "sk-ant-api03-secretkey" not in content


class TestSSEParsing:
    def test_tool_use_from_sse(self):
        sse_data = "\n".join([
            'data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100}}}',
            'data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"web_search"}}',
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":\'{"query":\'}}',
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":\'"\'}',  # wrong: just demo
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":\'hello"}\'}',
            'data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":50}}',
        ])

        # Use a simpler SSE payload that actually parses
        sse_bytes = (
            'data: {"type":"message_start","message":{"usage":{"input_tokens":100}}}\n'
            'data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"web_search"}}\n'
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\\\"query\\\": \\\"test\\\"}"}}\n'
            'data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":50}}\n'
        ).encode()

        result = san.parse_sse(sse_bytes)
        assert result["stop_reason"] == "tool_use"
        assert len(result["tool_uses"]) == 1
        assert result["tool_uses"][0]["tool_name"] == "web_search"
        assert result["input_tokens"] == 100
        assert result["output_tokens"] == 50

    def test_text_accumulation(self):
        sse_bytes = (
            'data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n'
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}\n'
            'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}\n'
            'data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}\n'
        ).encode()

        result = san.parse_sse(sse_bytes)
        assert result["content_text"] == "Hello world"
        assert result["stop_reason"] == "end_turn"


class TestUnknownEndpoint:
    def test_unknown_writes_to_errors(self, tmp_path):
        outfile = tmp_path / "audit.jsonl"
        errfile = tmp_path / "parse_errors.jsonl"
        with patch.object(san, "OUTPUT_PATH", outfile), \
             patch.object(san, "ERRORS_PATH", errfile):
            addon = san.DoomsdaySanitizer()
            flow = make_flow(method="GET", path="/some/unknown/endpoint")
            addon.response(flow)

        # No events written
        assert not outfile.exists() or outfile.read_text().strip() == ""
        # Error logged
        assert errfile.exists()
        err_data = json.loads(errfile.read_text())
        assert "no matching endpoint pattern" in err_data["error"]


class TestHashOnlyMode:
    def test_content_text_stripped_in_hash_mode(self, tmp_path):
        outfile = tmp_path / "audit.jsonl"
        with patch.object(san, "OUTPUT_PATH", outfile), \
             patch.object(san, "ERRORS_PATH", tmp_path / "errors.jsonl"), \
             patch.object(san, "STORAGE_MODE", "hash_only"):
            addon = san.DoomsdaySanitizer()
            flow = make_flow(
                req_body={
                    "model": "claude-opus-4-7",
                    "messages": [{"role": "user", "content": "secret prompt text"}],
                },
                resp_body={
                    "content": [{"type": "text", "text": "secret response text"}],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 5, "output_tokens": 5},
                },
            )
            addon.response(flow)

        content = outfile.read_text()
        event = json.loads(content)
        user_msg = event["completion"]["user_message"]
        asst_msg = event["completion"]["assistant_message"]

        assert "content_text" not in user_msg
        assert "content_sha256" in user_msg
        assert "char_count" in user_msg
        assert "content_text" not in asst_msg
        assert "content_sha256" in asst_msg

    def test_sha256_is_correct(self, tmp_path):
        outfile = tmp_path / "audit.jsonl"
        prompt = "test prompt for sha256"
        expected_sha = hashlib.sha256(prompt.encode()).hexdigest()

        with patch.object(san, "OUTPUT_PATH", outfile), \
             patch.object(san, "ERRORS_PATH", tmp_path / "errors.jsonl"):
            addon = san.DoomsdaySanitizer()
            flow = make_flow(
                req_body={
                    "model": "claude-opus-4-7",
                    "messages": [{"role": "user", "content": prompt}],
                },
                resp_body={
                    "content": [{"type": "text", "text": "response"}],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 5, "output_tokens": 5},
                },
            )
            addon.response(flow)

        event = json.loads(outfile.read_text())
        assert event["completion"]["user_message"]["content_sha256"] == expected_sha


class TestToolClassification:
    def test_builtin_tools(self):
        assert san.classify_tool("web_search") == "anthropic_builtin"
        assert san.classify_tool("bash") == "anthropic_builtin"
        assert san.classify_tool("computer_20250124") == "anthropic_builtin"
        assert san.classify_tool("str_replace_editor") == "anthropic_builtin"

    def test_mcp_tools(self):
        assert san.classify_tool("linear:create_issue") == "mcp"
        assert san.classify_tool("github:search_repos") == "mcp"

    def test_mcp_server_extraction(self):
        assert san.extract_mcp_server("linear:create_issue") == "linear"
        assert san.extract_mcp_server("web_search") is None


class TestPolicyClassification:
    def test_clean(self):
        result = san.classify_policy("What is the capital of France?")
        assert result["classification"] == "clean"

    def test_aws_key_flagged(self):
        result = san.classify_policy("my key is AKIAIOSFODNN7EXAMPLE123")
        assert result["classification"] == "flagged_secret"
        assert "aws_key" in result["matches"]

    def test_anthropic_key_flagged(self):
        result = san.classify_policy("sk-ant-api03-sometokenvalue here")
        assert result["classification"] == "flagged_secret"

    def test_email_pii(self):
        result = san.classify_policy("email me at foo@example.com please")
        assert result["classification"] == "flagged_pii"
        assert "email" in result["matches"]

    def test_ssn_pii(self):
        result = san.classify_policy("my SSN is 123-45-6789")
        assert result["classification"] == "flagged_pii"
        assert "us_ssn" in result["matches"]
