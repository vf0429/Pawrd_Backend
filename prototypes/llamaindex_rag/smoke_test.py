from __future__ import annotations

import argparse
import json
from urllib import error, request
from dataclasses import dataclass

try:
    from .constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS
    from .config import PrototypeConfig
    from .runtime import answer_query, configure_settings, ensure_index
except ImportError:
    from constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS
    from config import PrototypeConfig
    from runtime import answer_query, configure_settings, ensure_index


@dataclass(frozen=True)
class SmokeCase:
    name: str
    question: str
    expected_mode: str
    provider: str | None = None
    language: str | None = None


SMOKE_CASES: tuple[SmokeCase, ...] = (
    SmokeCase(
        name="prudential_room_and_board_limit",
        question="What is the annual limit for Prudential room and board?",
        provider="prudential",
        language="en",
        expected_mode="deterministic_benefit_limit_single",
    ),
    SmokeCase(
        name="bluecross_consult_coverage_zh",
        question="Blue Cross 包唔包獸醫診症？",
        provider="bluecross",
        language="zh",
        expected_mode="deterministic_consult_coverage_single",
    ),
    SmokeCase(
        name="bluecross_vs_prudential_consult_limit",
        question="Compare Blue Cross and Prudential veterinary consultation limits.",
        language="en",
        expected_mode="deterministic_consult_limit_comparison",
    ),
)


def main() -> None:
    parser = argparse.ArgumentParser(description="Run smoke tests against the PetWell LlamaIndex RAG prototype")
    parser.add_argument(
        "--case",
        action="append",
        dest="case_names",
        help="Optional smoke-case name to run. Repeat to run multiple named cases.",
    )
    parser.add_argument(
        "--via-http",
        action="store_true",
        help="Run smoke tests through HTTP POST /query instead of calling runtime functions directly.",
    )
    parser.add_argument(
        "--base-url",
        default="http://127.0.0.1:8098",
        help="Base URL used with --via-http. Default: http://127.0.0.1:8098",
    )
    args = parser.parse_args()

    selected = select_cases(args.case_names)

    cfg = None
    index = None
    if not args.via_http:
        cfg = PrototypeConfig.load()
        cfg.validate()
        configure_settings(cfg)
        index = ensure_index(cfg)
    else:
        check_http_capabilities(args.base_url)

    failures: list[str] = []
    target = args.base_url if args.via_http else str(cfg.persist_dir)
    print(f"Running {len(selected)} smoke case(s) against {target}")
    for case in selected:
        if args.via_http:
            actual_mode, answer, ok = run_http_case(args.base_url, case)
        else:
            result = answer_query(
                cfg=cfg,
                index=index,
                question=case.question,
                provider=case.provider,
                language=case.language,
            )
            answer = result.text.strip()
            actual_mode = result.mode
            ok = bool(answer) and actual_mode == case.expected_mode
        status = "PASS" if ok else "FAIL"
        print(f"[{status}] {case.name}")
        print(f"  question: {case.question}")
        print(f"  expected_mode: {case.expected_mode}")
        print(f"  actual_mode:   {actual_mode}")
        print(f"  answer:        {one_line(answer)}")
        if not ok:
            failures.append(case.name)

    if failures:
        raise SystemExit("Smoke test failed: " + ", ".join(failures))

    print("All smoke cases passed.")


def select_cases(case_names: list[str] | None) -> tuple[SmokeCase, ...]:
    if not case_names:
        return SMOKE_CASES
    wanted = set(case_names)
    selected = tuple(case for case in SMOKE_CASES if case.name in wanted)
    missing = sorted(wanted - {case.name for case in selected})
    if missing:
        raise SystemExit("Unknown smoke case(s): " + ", ".join(missing))
    return selected


def one_line(text: str) -> str:
    return " ".join(text.split())


def run_http_case(base_url: str, case: SmokeCase) -> tuple[str, str, bool]:
    payload = {
        "question": case.question,
        "provider": case.provider,
        "language": case.language,
    }
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = request.Request(
        url=base_url.rstrip("/") + "/query",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with request.urlopen(req, timeout=180) as response:
            raw = response.read()
    except error.URLError as exc:
        return "", f"http_error: {exc}", False

    try:
        parsed = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return "", "invalid_json_response", False

    answer = str(parsed.get("answer", "")).strip()
    actual_mode = str(parsed.get("answer_mode", "")).strip()
    ok = bool(answer) and actual_mode == case.expected_mode
    return actual_mode, answer, ok


def check_http_capabilities(base_url: str) -> None:
    req = request.Request(
        url=base_url.rstrip("/") + "/capabilities",
        method="GET",
    )
    try:
        with request.urlopen(req, timeout=60) as response:
            raw = response.read()
    except error.URLError as exc:
        raise SystemExit(f"HTTP capabilities check failed: {exc}") from exc

    try:
        parsed = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SystemExit("HTTP capabilities check failed: invalid_json_response") from exc

    providers = parsed.get("supported_providers") or []
    languages = parsed.get("supported_languages") or []
    methods = parsed.get("query_methods") or []
    if not isinstance(providers, list) or not set(SUPPORTED_PROVIDERS).issubset(set(providers)):
        raise SystemExit("HTTP capabilities check failed: supported_providers mismatch")
    if not isinstance(languages, list) or not set(SUPPORTED_LANGUAGES).issubset(set(languages)):
        raise SystemExit("HTTP capabilities check failed: supported_languages mismatch")
    if not isinstance(methods, list) or "POST" not in methods:
        raise SystemExit("HTTP capabilities check failed: POST query method missing")
    print("[PASS] capabilities")
    print(f"  providers: {', '.join(providers)}")
    print(f"  languages: {', '.join(languages)}")
    print(f"  methods:   {', '.join(methods)}")


if __name__ == "__main__":
    main()
