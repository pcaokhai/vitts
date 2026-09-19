"""Object storage access for `Merge`.

The worker reads segment PCM and writes the merged container; the gateway never proxies
those bytes, so a long job does not stream through it twice.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import structlog

if TYPE_CHECKING:
    from mypy_boto3_s3.client import S3Client

    from .config import S3Config

log = structlog.get_logger(__name__)


class ObjectMissingError(KeyError):
    """Raised when a segment object the caller named does not exist."""

    def __init__(self, key: str) -> None:
        super().__init__(key)
        self.key = key


class Store:
    """Reads and writes audio objects."""

    def __init__(self, cfg: S3Config) -> None:
        self._cfg = cfg
        self._client: S3Client | None = None

    @property
    def client(self) -> S3Client:
        # Built on first use: a worker that never merges should not need credentials to
        # have been valid at boot.
        if self._client is None:
            import boto3

            self._client = boto3.client(
                "s3",
                endpoint_url=self._cfg.endpoint,
                region_name=self._cfg.region,
                aws_access_key_id=self._cfg.access_key,
                aws_secret_access_key=self._cfg.secret_key,
            )
        return self._client

    def get(self, key: str) -> bytes:
        """Read one object, raising ObjectMissingError when it is not there."""
        from botocore.exceptions import ClientError

        try:
            response = self.client.get_object(Bucket=self._cfg.bucket, Key=key)
        except ClientError as exc:
            code = exc.response.get("Error", {}).get("Code")
            if code in {"NoSuchKey", "404", "NotFound"}:
                raise ObjectMissingError(key) from exc
            raise
        return bytes(response["Body"].read())

    def put(self, key: str, body: bytes, content_type: str) -> None:
        """Write one object."""
        self.client.put_object(
            Bucket=self._cfg.bucket, Key=key, Body=body, ContentType=content_type
        )
        log.info("object.written", key=key, bytes=len(body))
