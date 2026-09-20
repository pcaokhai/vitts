"""Synchronous and async clients over the ViTTS HTTP API."""

from __future__ import annotations

import time
import uuid
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass
from typing import Any

import anyio
import httpx
from pydantic import AnyUrl, ValidationError

from .errors import VittsError, VittsTransportError
from .models import (
    ApiKey,
    Job,
    JobCreateRequest,
    Problem,
    SynthesizeRequest,
    UsageReport,
    Voice,
)

DEFAULT_BASE_URL = "http://127.0.0.1:8080"
DEFAULT_TIMEOUT = 30.0
DEFAULT_MAX_RETRIES = 2
# A server's Retry-After is honoured up to this, past which failing fast is kinder.
MAX_RETRY_WAIT = 30.0
# Terminal job states. Anything else means the job is still moving.
_TERMINAL: frozenset[str] = frozenset({"completed", "failed", "cancelled"})


@dataclass(frozen=True)
class Audio:
    """A complete audio file and the content type the API sent it as."""

    data: bytes
    content_type: str


def _headers(api_key: str, extra: dict[str, str] | None = None) -> dict[str, str]:
    headers = {"Authorization": f"Bearer {api_key}"}
    if extra:
        headers.update(extra)
    return headers


def _retry_after(response: httpx.Response) -> float | None:
    raw = response.headers.get("retry-after")
    if not raw:
        return None
    try:
        return float(raw)
    except ValueError:
        return None


def _problem(response: httpx.Response) -> Problem:
    """Never raises: a dead gateway or a proxy answers with HTML, and a parse failure
    there would surface as a JSON error instead of the status the caller needs."""
    body: object = None
    try:
        body = response.json()
    except ValueError:
        body = None

    if isinstance(body, dict) and isinstance(body.get("code"), str):
        # The code is the part callers branch on, so it is filled in from the body even
        # when another field is missing. Losing it because a proxy dropped `type` would
        # turn a quota refusal into an unexplained failure.
        try:
            return Problem.model_validate(
                {
                    "type": body.get("type") or "about:blank",
                    "title": body.get("title") or f"HTTP {response.status_code}",
                    "status": body.get("status") or response.status_code,
                    **{k: v for k, v in body.items() if k in ("detail", "instance", "request_id")},
                    "code": body["code"],
                }
            )
        except ValidationError:
            # An unrecognised code: fall through rather than guess at its meaning.
            pass
    # about:blank is what RFC 9457 says to use when there is no problem type, which is
    # exactly the case when the body was not a problem document at all.
    return Problem(
        type=AnyUrl("about:blank"),
        title=f"HTTP {response.status_code}",
        status=response.status_code,
        code="internal",
    )


def _raise_for(response: httpx.Response) -> VittsError:
    return VittsError(_problem(response), response.status_code, _retry_after(response))


def job_status(job: Job) -> str:
    """The job's state as a plain string.

    The generated model wraps the status in a RootModel, so ``job.status`` is a wrapper
    rather than the value. Callers should not have to know that.
    """
    return "" if job.status is None else str(job.status.root)


def _idempotency_key(given: str | None) -> str:
    """A create without one is a create that duplicates on retry (US-14)."""
    return given or str(uuid.uuid4())


class VittsClient:
    """Blocking client. Safe to share between threads; httpx handles pooling."""

    def __init__(
        self,
        api_key: str,
        base_url: str = DEFAULT_BASE_URL,
        *,
        timeout: float = DEFAULT_TIMEOUT,
        max_retries: int = DEFAULT_MAX_RETRIES,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        if not api_key:
            raise ValueError("api_key is required")
        self._api_key = api_key
        self._max_retries = max_retries
        self._client = httpx.Client(
            base_url=base_url.rstrip("/"),
            timeout=timeout,
            transport=transport,
        )

    def __enter__(self) -> VittsClient:
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    def synthesize(self, request: SynthesizeRequest) -> Audio:
        """Return a complete audio file. Use :meth:`stream` when latency matters."""
        response = self._send(
            "POST", "/v1/synthesize", json=request.model_dump(mode="json", exclude_none=True)
        )
        return Audio(
            data=response.content,
            content_type=response.headers.get("content-type", "application/octet-stream"),
        )

    def stream(self, request: SynthesizeRequest) -> Iterator[bytes]:
        """Yield audio as it is produced, in order.

        Abandoning the iterator closes the connection, and the gateway cancels the worker
        within 200 ms (US-01). You are billed for the audio delivered, not the text sent
        (ADR-010). Not retried: replaying would repeat audio the caller already has.
        """
        try:
            with self._client.stream(
                "POST",
                "/v1/synthesize/stream",
                json=request.model_dump(mode="json", exclude_none=True),
                headers=_headers(self._api_key),
                timeout=None,
            ) as response:
                if response.status_code >= 400:
                    response.read()
                    raise _raise_for(response)
                yield from response.iter_bytes()
        except httpx.HTTPError as cause:
            raise VittsTransportError(str(cause)) from cause

    def list_voices(self) -> list[Voice]:
        return [Voice.model_validate(row) for row in self._send("GET", "/v1/voices").json()]

    def usage(self, date_from: str, date_to: str) -> UsageReport:
        response = self._send("GET", "/v1/usage", params={"from": date_from, "to": date_to})
        return UsageReport.model_validate(response.json())

    def create_job(self, request: JobCreateRequest, idempotency_key: str | None = None) -> Job:
        response = self._send(
            "POST",
            "/v1/jobs",
            json=request.model_dump(mode="json", exclude_none=True),
            headers={"Idempotency-Key": _idempotency_key(idempotency_key)},
        )
        return Job.model_validate(response.json())

    def get_job(self, job_id: str) -> Job:
        return Job.model_validate(self._send("GET", f"/v1/jobs/{job_id}").json())

    def list_jobs(self) -> list[Job]:
        return [Job.model_validate(row) for row in self._send("GET", "/v1/jobs").json()]

    def cancel_job(self, job_id: str) -> None:
        self._send("DELETE", f"/v1/jobs/{job_id}")

    def wait_for_job(self, job_id: str, poll: float = 2.0, timeout: float = 600.0) -> Job:
        """Poll until the job is terminal, then return it.

        Gives up rather than looping forever: a caller who wants to wait all day can pass
        a longer timeout, but the default returns control.
        """
        deadline = time.monotonic() + timeout
        while True:
            job = self.get_job(job_id)
            status = job_status(job)
            if status in _TERMINAL:
                return job
            if time.monotonic() + poll > deadline:
                raise TimeoutError(f"job {job_id} was still {status} after the timeout")
            time.sleep(poll)

    def list_keys(self) -> list[ApiKey]:
        return [ApiKey.model_validate(row) for row in self._send("GET", "/v1/keys").json()]

    def _send(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        headers = _headers(self._api_key, kwargs.pop("headers", None))
        attempt = 0

        while True:
            try:
                response = self._client.request(method, path, headers=headers, **kwargs)
            except httpx.HTTPError as cause:
                raise VittsTransportError(str(cause)) from cause

            if response.status_code < 400:
                return response

            error = _raise_for(response)
            if not error.retryable or attempt >= self._max_retries:
                raise error

            attempt += 1
            time.sleep(min(error.retry_after or 1.0, MAX_RETRY_WAIT))


class AsyncVittsClient:
    """The same surface on asyncio. Streaming is an async iterator."""

    def __init__(
        self,
        api_key: str,
        base_url: str = DEFAULT_BASE_URL,
        *,
        timeout: float = DEFAULT_TIMEOUT,
        max_retries: int = DEFAULT_MAX_RETRIES,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        if not api_key:
            raise ValueError("api_key is required")
        self._api_key = api_key
        self._max_retries = max_retries
        self._client = httpx.AsyncClient(
            base_url=base_url.rstrip("/"),
            timeout=timeout,
            transport=transport,
        )

    async def __aenter__(self) -> AsyncVittsClient:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def synthesize(self, request: SynthesizeRequest) -> Audio:
        response = await self._send(
            "POST", "/v1/synthesize", json=request.model_dump(mode="json", exclude_none=True)
        )
        return Audio(
            data=response.content,
            content_type=response.headers.get("content-type", "application/octet-stream"),
        )

    async def stream(self, request: SynthesizeRequest) -> AsyncIterator[bytes]:
        try:
            async with self._client.stream(
                "POST",
                "/v1/synthesize/stream",
                json=request.model_dump(mode="json", exclude_none=True),
                headers=_headers(self._api_key),
                timeout=None,
            ) as response:
                if response.status_code >= 400:
                    await response.aread()
                    raise _raise_for(response)
                async for chunk in response.aiter_bytes():
                    yield chunk
        except httpx.HTTPError as cause:
            raise VittsTransportError(str(cause)) from cause

    async def list_voices(self) -> list[Voice]:
        response = await self._send("GET", "/v1/voices")
        return [Voice.model_validate(row) for row in response.json()]

    async def create_job(
        self, request: JobCreateRequest, idempotency_key: str | None = None
    ) -> Job:
        response = await self._send(
            "POST",
            "/v1/jobs",
            json=request.model_dump(mode="json", exclude_none=True),
            headers={"Idempotency-Key": _idempotency_key(idempotency_key)},
        )
        return Job.model_validate(response.json())

    async def get_job(self, job_id: str) -> Job:
        response = await self._send("GET", f"/v1/jobs/{job_id}")
        return Job.model_validate(response.json())

    async def _send(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        headers = _headers(self._api_key, kwargs.pop("headers", None))
        attempt = 0

        while True:
            try:
                response = await self._client.request(method, path, headers=headers, **kwargs)
            except httpx.HTTPError as cause:
                raise VittsTransportError(str(cause)) from cause

            if response.status_code < 400:
                return response

            error = _raise_for(response)
            if not error.retryable or attempt >= self._max_retries:
                raise error

            attempt += 1
            await anyio.sleep(min(error.retry_after or 1.0, MAX_RETRY_WAIT))
