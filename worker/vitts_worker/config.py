"""Environment configuration for the worker.

Loaded once at boot and validated; an invalid value must stop the process with a clear
message rather than surface as a runtime failure (docs/08-variables.md).
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path


class ConfigError(ValueError):
    """Raised when the environment does not describe a runnable worker."""


def _int(src: Mapping[str, str], name: str, default: int, *, minimum: int = 1) -> int:
    raw = src.get(name)
    if raw is None or raw == "":
        return default
    try:
        value = int(raw)
    except ValueError as exc:
        raise ConfigError(f"{name} must be an integer, got {raw!r}") from exc
    if value < minimum:
        raise ConfigError(f"{name} must be >= {minimum}, got {value}")
    return value


@dataclass(frozen=True, slots=True)
class S3Config:
    """Object storage used for the model mirror and, from task 0.4, audio segments."""

    endpoint: str
    bucket: str
    region: str
    access_key: str
    secret_key: str


@dataclass(frozen=True, slots=True)
class Config:
    grpc_addr: str
    model_repo: str
    model_revision: str
    model_mirror_s3: str | None
    model_dir: Path
    ort_intra_op_threads: int
    max_text_chars: int
    s3: S3Config | None

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> Config:
        src: Mapping[str, str] = os.environ if env is None else env

        repo = src.get("VITTS_MODEL_REPO", "zeroweight-ai/ZeroTTS")
        revision = src.get("VITTS_MODEL_REVISION", "").strip()
        if not revision:
            raise ConfigError(
                "VITTS_MODEL_REVISION is required; pin a commit hash so every replica "
                "serves the same weights (docs/08-variables.md)"
            )

        mirror = src.get("VITTS_MODEL_MIRROR_S3", "").strip() or None
        s3 = _s3_from(src)
        if mirror and s3 is None:
            raise ConfigError("VITTS_MODEL_MIRROR_S3 is set but VITTS_S3_* is incomplete")

        return cls(
            grpc_addr=_listen_addr(src.get("VITTS_GRPC_ADDR", ":50051")),
            model_repo=repo,
            model_revision=revision,
            model_mirror_s3=mirror,
            model_dir=Path(
                src.get("VITTS_MODEL_DIR", "") or Path.home() / ".cache" / "vitts" / "model"
            ),
            ort_intra_op_threads=_int(src, "ORT_INTRA_OP_THREADS", 8),
            max_text_chars=_int(src, "VITTS_MAX_TEXT_CHARS", 3000),
            s3=s3,
        )


def _listen_addr(value: str) -> str:
    """Accept the Go-style `:port` used across `docs/08-variables.md` and compose.

    grpc.aio needs an explicit host, so a bare `:port` binds to every interface the way
    the Go gateway's `VITTS_HTTP_ADDR` does, instead of failing at startup.
    """
    addr = value.strip()
    if not addr:
        raise ConfigError("VITTS_GRPC_ADDR must not be empty")
    return f"[::]{addr}" if addr.startswith(":") else addr


def _s3_from(src: Mapping[str, str]) -> S3Config | None:
    keys = ("ENDPOINT", "BUCKET", "REGION", "ACCESS_KEY", "SECRET_KEY")
    values = [src.get(f"VITTS_S3_{k}", "").strip() for k in keys]
    if not any(values):
        return None
    if not all(values):
        missing = [f"VITTS_S3_{k}" for k, v in zip(keys, values, strict=True) if not v]
        raise ConfigError(f"incomplete S3 configuration, missing: {', '.join(missing)}")
    endpoint, bucket, region, access_key, secret_key = values
    return S3Config(
        endpoint=endpoint,
        bucket=bucket,
        region=region,
        access_key=access_key,
        secret_key=secret_key,
    )
