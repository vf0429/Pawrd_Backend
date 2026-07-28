from __future__ import annotations

from dataclasses import dataclass

try:
    from .constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS
except ImportError:
    from constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS


@dataclass(frozen=True)
class RequestValidationError(ValueError):
    code: str
    message: str


def validate_provider(provider: str | None) -> str | None:
    normalized = normalize_optional_string(provider)
    if normalized is None:
        return None
    if normalized not in SUPPORTED_PROVIDERS:
        raise RequestValidationError(
            code="invalid_provider",
            message=(
                "provider must be one of: " + ", ".join(SUPPORTED_PROVIDERS)
            ),
        )
    return normalized


def validate_language(language: str | None) -> str | None:
    normalized = normalize_optional_string(language)
    if normalized is None:
        return None
    if normalized not in SUPPORTED_LANGUAGES:
        raise RequestValidationError(
            code="invalid_language",
            message=(
                "language must be one of: " + ", ".join(SUPPORTED_LANGUAGES)
            ),
        )
    return normalized


def validate_max_sources(
    value: object,
    *,
    default_max_sources: int,
    max_allowed: int,
) -> int:
    if value is None or str(value).strip() == "":
        return default_max_sources

    try:
        parsed = int(str(value).strip())
    except ValueError as exc:
        raise RequestValidationError(
            code="invalid_max_sources",
            message=f"max_sources must be an integer between 1 and {max_allowed}",
        ) from exc

    if parsed < 1 or parsed > max_allowed:
        raise RequestValidationError(
            code="invalid_max_sources",
            message=f"max_sources must be between 1 and {max_allowed}",
        )
    return parsed


def build_validation_error_payload(
    exc: RequestValidationError,
    *,
    max_allowed_sources: int | None = None,
) -> dict[str, object]:
    payload: dict[str, object] = {
        "error": exc.code,
        "message": exc.message,
    }
    if exc.code == "invalid_provider":
        payload["supported_providers"] = list(SUPPORTED_PROVIDERS)
    if exc.code == "invalid_language":
        payload["supported_languages"] = list(SUPPORTED_LANGUAGES)
    if exc.code == "invalid_max_sources" and max_allowed_sources is not None:
        payload["max_allowed_sources"] = max_allowed_sources
    return payload


def normalize_optional_string(value: object) -> str | None:
    if value is None:
        return None
    text = str(value).strip().lower()
    return text or None
