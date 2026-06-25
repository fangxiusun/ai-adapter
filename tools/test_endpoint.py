#!/usr/bin/env python3
"""
AI Adapter endpoint smoke test.

Covers:
- docs/08-接口测试指南.md core requests
- encoding edge cases (UTF-8, UTF-8 BOM, GBK, Latin-1)
- basic negative tests (missing model, invalid JSON, unmatched model)

Each test prints:
  - request URL
  - configured headers
  - POST body (truncated)
  - response status + body preview

Full request/response data is written to a timestamped log file under tools/logs/.

Usage examples:
  python tools/test_endpoint.py
  python tools/test_endpoint.py --url http://localhost:8081
  python tools/test_endpoint.py --model mimo-v2.5-pro
  python tools/test_endpoint.py --encoding-only
  python tools/test_endpoint.py --log-file tools/logs/custom.log
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Dict, List, Optional, Tuple


DEFAULT_URL = "http://localhost:8081"
DEFAULT_MODEL = "mimo-v2.5-pro"
DEFAULT_TIMEOUT = 30


# ---------------------------------------------------------------------------
# Data types
# ---------------------------------------------------------------------------

@dataclass
class CaseResult:
    name: str
    passed: bool
    status: int
    message: str


class TestLogger:
    """Writes full request/response detail to a log file."""

    def __init__(self, path: str) -> None:
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        self.path = path
        self._fh = open(path, "w", encoding="utf-8")
        self._fh.write(f"# AI Adapter endpoint test log\n")
        self._fh.write(f"# Started: {datetime.now().isoformat()}\n\n")

    def close(self) -> None:
        self._fh.write(f"\n# Finished: {datetime.now().isoformat()}\n")
        self._fh.close()

    def _write(self, text: str) -> None:
        self._fh.write(text + "\n")
        self._fh.flush()

    def log_request(self, case: str, method: str, url: str, headers: Dict[str, str], body_preview: str) -> None:
        self._write(f"{'=' * 72}")
        self._write(f"[{case}] {method} {url}")
        self._write(f"Headers: {json.dumps(headers, ensure_ascii=False)}")
        if body_preview:
            self._write(f"Body:\n{body_preview}")
        self._write("")

    def log_response(self, case: str, status: int, body: str) -> None:
        self._write(f"[{case}] Response status={status}")
        self._write(f"Body:\n{body[:20000]}")
        self._write("")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def load_default_url_from_config() -> str:
    try:
        config_path = os.path.join(os.path.dirname(__file__), "..", "dist", "config.yaml")
        if not os.path.exists(config_path):
            return DEFAULT_URL
        with open(config_path, "r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line.startswith("port:"):
                    port = line.split(":", 1)[1].strip().strip('"').strip("'")
                    return f"http://localhost:{port}"
    except Exception:
        pass
    return DEFAULT_URL


def truncate(s: str, limit: int = 800) -> str:
    if len(s) <= limit:
        return s
    return s[:limit] + f"...({len(s)} chars total)"


def preview_bytes(data: bytes, limit: int = 800) -> str:
    try:
        text = data.decode("utf-8", errors="replace")
    except Exception:
        text = repr(data)
    return truncate(text, limit)


def preview_json(obj: Any, limit: int = 800) -> str:
    try:
        text = json.dumps(obj, ensure_ascii=False, indent=2)
    except Exception:
        text = str(obj)
    return truncate(text, limit)


def post_json(
    url: str,
    payload: Dict[str, Any],
    headers: Optional[Dict[str, str]] = None,
    timeout: int = DEFAULT_TIMEOUT,
    logger: Optional[TestLogger] = None,
    case: str = "",
) -> Tuple[int, Any]:
    merged = {"Content-Type": "application/json", **(headers or {})}
    data = json.dumps(payload, ensure_ascii=False).encode("utf-8")

    if logger:
        logger.log_request(case, "POST", url, merged, preview_bytes(data))

    req = urllib.request.Request(url, data=data, headers=merged, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            if logger:
                logger.log_response(case, resp.status, body)
            try:
                return resp.status, json.loads(body)
            except Exception:
                return resp.status, body
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        if logger:
            logger.log_response(case, e.code, body)
        try:
            return e.code, json.loads(body)
        except Exception:
            return e.code, body


def post_raw(
    url: str,
    raw: bytes,
    headers: Optional[Dict[str, str]] = None,
    timeout: int = DEFAULT_TIMEOUT,
    logger: Optional[TestLogger] = None,
    case: str = "",
) -> Tuple[int, str]:
    merged = headers or {}
    if logger:
        logger.log_request(case, "POST", url, merged, preview_bytes(raw))

    req = urllib.request.Request(url, data=raw, headers=merged, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            if logger:
                logger.log_response(case, resp.status, body)
            return resp.status, body
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        if logger:
            logger.log_response(case, e.code, body)
        return e.code, body


def get_text(
    url: str,
    timeout: int = 5,
    logger: Optional[TestLogger] = None,
    case: str = "",
) -> Tuple[int, str]:
    if logger:
        logger.log_request(case, "GET", url, {}, "")
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            if logger:
                logger.log_response(case, resp.status, body)
            return resp.status, body
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        if logger:
            logger.log_response(case, e.code, body)
        return e.code, body
    except Exception as e:
        if logger:
            logger.log_response(case, 0, str(e))
        return 0, str(e)


def ok_like(status: int) -> bool:
    return 200 <= status < 300


def result(name: str, status: int, passed: bool, message: str = "") -> CaseResult:
    return CaseResult(name=name, passed=passed, status=status, message=message)


# ---------------------------------------------------------------------------
# Test cases
# ---------------------------------------------------------------------------

def test_chat_nonstream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "chat.nonstream"
    url = f"{base}/v1/chat/completions"
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "Reply with only the word: ok"},
        ],
        "stream": False,
        "max_completion_tokens": 64,
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_chat_stream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "chat.stream"
    url = f"{base}/v1/chat/completions"
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": "Reply with only the word: ok"}],
        "stream": True,
        "max_completion_tokens": 64,
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_responses_nonstream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "responses.nonstream"
    url = f"{base}/v1/responses"
    payload = {
        "model": model,
        "input": "Reply with only the word: ok",
        "instructions": "You are a helpful assistant.",
        "stream": False,
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_responses_stream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "responses.stream"
    url = f"{base}/v1/responses"
    payload = {
        "model": model,
        "input": "Reply with only the word: ok",
        "stream": True,
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_messages_nonstream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "messages.nonstream"
    url = f"{base}/v1/messages"
    payload = {
        "model": model,
        "max_tokens": 64,
        "system": "You are a helpful assistant.",
        "messages": [{"role": "user", "content": "Reply with only the word: ok"}],
        "stream": False,
    }
    headers = {"anthropic-version": "2023-06-01"}
    status, body = post_json(url, payload, headers=headers, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_messages_stream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "messages.stream"
    url = f"{base}/v1/messages"
    payload = {
        "model": model,
        "max_tokens": 64,
        "messages": [{"role": "user", "content": "Reply with only the word: ok"}],
        "stream": True,
    }
    headers = {"anthropic-version": "2023-06-01"}
    status, body = post_json(url, payload, headers=headers, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_gemini_nonstream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "gemini.nonstream"
    url = f"{base}/v1beta/models/{model}:generateContent"
    payload = {
        "contents": [{"role": "user", "parts": [{"text": "Reply with only the word: ok"}]}],
        "generationConfig": {"maxOutputTokens": 64},
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_gemini_stream(base: str, model: str, logger: TestLogger) -> CaseResult:
    case = "gemini.stream"
    url = f"{base}/v1beta/models/{model}:streamGenerateContent?alt=sse"
    payload = {
        "contents": [{"role": "user", "parts": [{"text": "Reply with only the word: ok"}]}]
    }
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else truncate(str(body)))


def test_missing_model(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "error.missing_model"
    url = f"{base}/v1/chat/completions"
    payload = {"messages": [{"role": "user", "content": "hi"}]}
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = status == 400
    return result(case, status, passed, "" if passed else f"expected 400, got {status}")


def test_invalid_json(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "error.invalid_json"
    url = f"{base}/v1/chat/completions"
    raw = b"not json"
    status, body = post_raw(url, raw, headers={"Content-Type": "application/json"}, logger=logger, case=case)
    passed = status == 400
    return result(case, status, passed, "" if passed else f"expected 400, got {status}")


def test_unmatched_model(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "error.unmatched_model"
    url = f"{base}/v1/chat/completions"
    payload = {"model": "nonexistent-model", "messages": [{"role": "user", "content": "hi"}]}
    status, body = post_json(url, payload, logger=logger, case=case)
    passed = status == 404
    return result(case, status, passed, "" if passed else f"expected 404, got {status}")


# Encoding edge cases --------------------------------------------------------

def build_chinese_json() -> bytes:
    payload = {
        "model": "mimo-v2.5-pro",
        "max_tokens": 256,
        "system": "You are a helpful assistant.",
        "messages": [{"role": "user", "content": "用一句话介绍 Go 语言"}],
        "stream": False,
    }
    return json.dumps(payload, ensure_ascii=False).encode("utf-8")


def test_encoding_utf8(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "encoding.utf8"
    url = f"{base}/v1/messages"
    raw = build_chinese_json()
    headers = {"Content-Type": "application/json; charset=utf-8", "anthropic-version": "2023-06-01"}
    status, body = post_raw(url, raw, headers=headers, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else f"status={status}")


def test_encoding_utf8_bom(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "encoding.utf8_bom"
    url = f"{base}/v1/messages"
    raw = b"\xef\xbb\xbf" + build_chinese_json()
    headers = {"Content-Type": "application/json; charset=utf-8", "anthropic-version": "2023-06-01"}
    status, body = post_raw(url, raw, headers=headers, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else f"status={status}")


def test_encoding_gbk(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "encoding.gbk"
    url = f"{base}/v1/messages"
    raw = build_chinese_json().decode("utf-8").encode("gbk")
    headers = {"Content-Type": "application/json; charset=gbk", "anthropic-version": "2023-06-01"}
    status, body = post_raw(url, raw, headers=headers, logger=logger, case=case)
    passed = ok_like(status)
    return result(case, status, passed, "" if passed else f"status={status}")


def test_encoding_latin1(base: str, _model: str, logger: TestLogger) -> CaseResult:
    case = "encoding.latin1_fallback"
    url = f"{base}/v1/messages"
    raw = build_chinese_json().decode("utf-8").encode("latin-1", errors="ignore")
    headers = {"Content-Type": "application/json; charset=latin-1", "anthropic-version": "2023-06-01"}
    status, body = post_raw(url, raw, headers=headers, logger=logger, case=case)
    passed = status < 500
    return result(case, status, passed, "" if passed else f"status={status}")


# ---------------------------------------------------------------------------
# Case sets
# ---------------------------------------------------------------------------

def build_doc_cases(base: str, model: str, logger: TestLogger) -> List[CaseResult]:
    return [
        test_chat_nonstream(base, model, logger),
        test_chat_stream(base, model, logger),
        test_responses_nonstream(base, model, logger),
        test_responses_stream(base, model, logger),
        test_messages_nonstream(base, model, logger),
        test_messages_stream(base, model, logger),
        test_gemini_nonstream(base, model, logger),
        test_gemini_stream(base, model, logger),
        test_missing_model(base, model, logger),
        test_invalid_json(base, model, logger),
        test_unmatched_model(base, model, logger),
    ]


def build_encoding_cases(base: str, model: str, logger: TestLogger) -> List[CaseResult]:
    return [
        test_encoding_utf8(base, model, logger),
        test_encoding_utf8_bom(base, model, logger),
        test_encoding_gbk(base, model, logger),
        test_encoding_latin1(base, model, logger),
    ]


# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

def print_table(title: str, cases: List[CaseResult]) -> None:
    print("")
    print("=" * 60)
    print(f" {title}")
    print("=" * 60)
    for c in cases:
        tag = "PASS" if c.passed else "FAIL"
        print(f" [{tag}] {c.name:<28} status={c.status}")
        if not c.passed and c.message:
            print(f"       -> {c.message}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    default_url = load_default_url_from_config()
    default_log = os.path.join(
        os.path.dirname(__file__), "logs", f"test_{datetime.now().strftime('%Y%m%d_%H%M%S')}.log"
    )

    parser = argparse.ArgumentParser(description="AI Adapter endpoint smoke test")
    parser.add_argument("--url", default=default_url, help=f"Server base URL (default: {default_url})")
    parser.add_argument("--model", default=DEFAULT_MODEL, help=f"Model name (default: {DEFAULT_MODEL})")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT, help=f"Request timeout seconds (default: {DEFAULT_TIMEOUT})")
    parser.add_argument("--encoding-only", action="store_true", help="Run only encoding edge-case tests")
    parser.add_argument("--log-file", default=default_log, help=f"Full-detail log file (default: {default_log})")
    args = parser.parse_args()

    base = args.url.rstrip("/")
    model = args.model

    logger = TestLogger(args.log_file)

    # Health check
    status, body = get_text(f"{base}/admin/api/health", timeout=5, logger=logger, case="health")
    if status == 0:
        print(f"Server unreachable at {base}: {body}")
        print(f"Full log: {args.log_file}")
        logger.close()
        return 2

    print(f"Testing server : {base}")
    print(f"Model          : {model}")
    print(f"Full log       : {args.log_file}")

    started = time.time()
    cases: List[CaseResult] = []

    if args.encoding_only:
        cases.extend(build_encoding_cases(base, model, logger))
    else:
        cases.extend(build_doc_cases(base, model, logger))
        cases.extend(build_encoding_cases(base, model, logger))

    elapsed = time.time() - started
    passed = sum(1 for c in cases if c.passed)
    failed = len(cases) - passed

    if args.encoding_only:
        print_table("Encoding Tests", cases)
    else:
        print_table("Doc Endpoint Tests", cases[:11])
        print_table("Encoding Tests", cases[11:])

    print("")
    print("-" * 60)
    print(f"Total={len(cases)} Passed={passed} Failed={failed} Time={elapsed:.1f}s")

    logger.close()
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
