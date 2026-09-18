"""S3 mirror for model weights.

A cold worker must not depend on huggingface.co being reachable. `VITTS_MODEL_MIRROR_S3`
names a key prefix inside `VITTS_S3_BUCKET`; weights are read from there when the mirror
holds the pinned revision, otherwise they are fetched from the Hub and the mirror is
populated for the next replica (US-02 acceptance criterion 4).
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING

import structlog

if TYPE_CHECKING:
    from mypy_boto3_s3.client import S3Client

    from .config import S3Config

log = structlog.get_logger(__name__)


def client(cfg: S3Config) -> S3Client:
    import boto3

    return boto3.client(
        "s3",
        endpoint_url=cfg.endpoint,
        region_name=cfg.region,
        aws_access_key_id=cfg.access_key,
        aws_secret_access_key=cfg.secret_key,
    )


def revision_prefix(mirror_prefix: str, repo: str, revision: str) -> str:
    """Key prefix holding one immutable revision of one repo."""
    return f"{mirror_prefix.strip('/')}/{repo}/{revision}/"


def download(s3: S3Client, bucket: str, prefix: str, dest: Path) -> bool:
    """Copy a mirrored revision into `dest`. False when the mirror does not hold it."""
    keys = _list(s3, bucket, prefix)
    if not keys:
        return False
    for key in keys:
        target = dest / key[len(prefix) :]
        target.parent.mkdir(parents=True, exist_ok=True)
        s3.download_file(bucket, key, str(target))
    log.info("model.mirror.hit", bucket=bucket, prefix=prefix, files=len(keys))
    return True


def upload(s3: S3Client, bucket: str, prefix: str, source: Path) -> int:
    """Populate the mirror from a local snapshot. Returns the number of files written."""
    files = [p for p in source.rglob("*") if p.is_file()]
    for path in files:
        s3.upload_file(str(path), bucket, prefix + str(path.relative_to(source)))
    log.info("model.mirror.populated", bucket=bucket, prefix=prefix, files=len(files))
    return len(files)


def _list(s3: S3Client, bucket: str, prefix: str) -> list[str]:
    keys: list[str] = []
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        keys.extend(obj["Key"] for obj in page.get("Contents", []))
    return keys
