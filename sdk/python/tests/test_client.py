"""The client's contract with the API, driven through a stubbed transport."""

from __future__ import annotations

import json

import httpx
import pytest

from vitts import (
    SynthesizeRequest,
    VittsClient,
    VittsError,
    VittsTransportError,
    job_status,
)
from vitts.models import JobCreateRequest


def problem(status: int, code: str, detail: str, headers: dict[str, str] | None = None):
    body = json.dumps(
        {
            "type": f"https://docs.vitts.dev/errors/{code}",
            "code": code,
            "title": code,
            "detail": detail,
            "status": status,
            "request_id": "req-1",
        }
    )
    return httpx.Response(
        status,
        content=body,
        headers={"content-type": "application/problem+json", **(headers or {})},
    )


def client_with(responses: list[httpx.Response], max_retries: int = 0):
    """Replays responses in order and records every request the client made."""
    sent: list[httpx.Request] = []
    queue = list(responses)

    def handler(request: httpx.Request) -> httpx.Response:
        sent.append(request)
        if not queue:
            raise AssertionError("the client made more requests than the test expected")
        return queue.pop(0)

    client = VittsClient(
        "zt_test_abc",
        "https://api.example.com/",
        max_retries=max_retries,
        transport=httpx.MockTransport(handler),
    )
    return client, sent


# T-116. The key goes in the Authorization header, never in the URL where proxies and
# history keep it.
def test_every_request_carries_the_key_as_a_bearer_header() -> None:
    client, sent = client_with(
        [httpx.Response(200, content=b"\x01\x02", headers={"content-type": "audio/wav"})]
    )

    audio = client.synthesize(SynthesizeRequest(text="Xin chào"))

    assert sent[0].headers["authorization"] == "Bearer zt_test_abc"
    assert "zt_test_abc" not in str(sent[0].url)
    assert str(sent[0].url) == "https://api.example.com/v1/synthesize"
    assert audio.data == b"\x01\x02"
    assert audio.content_type == "audio/wav"


def test_a_refusal_arrives_with_the_apis_own_code_and_detail() -> None:
    client, _ = client_with([problem(402, "quota_exceeded", "used 51000 of 50000")])

    with pytest.raises(VittsError) as caught:
        client.synthesize(SynthesizeRequest(text="x"))

    assert caught.value.code == "quota_exceeded"
    assert caught.value.status == 402
    assert str(caught.value) == "used 51000 of 50000"
    assert caught.value.request_id == "req-1"
    assert caught.value.retryable is False


# T-117. NFR-05 answers overload with Retry-After. A client that ignores it is why
# overloaded services stay overloaded.
def test_429_is_retried_after_the_interval_the_api_asked_for() -> None:
    client, sent = client_with(
        [
            problem(429, "rate_limited", "slow down", {"retry-after": "0"}),
            httpx.Response(200, content=b"\x09", headers={"content-type": "audio/wav"}),
        ],
        max_retries=2,
    )

    audio = client.synthesize(SynthesizeRequest(text="x"))

    assert len(sent) == 2
    assert audio.data == b"\x09"


def test_retries_are_bounded_and_the_last_error_reaches_the_caller() -> None:
    client, sent = client_with(
        [problem(503, "overloaded", f"attempt {i}", {"retry-after": "0"}) for i in range(3)],
        max_retries=2,
    )

    with pytest.raises(VittsError) as caught:
        client.synthesize(SynthesizeRequest(text="x"))

    assert str(caught.value) == "attempt 2"
    assert len(sent) == 3


def test_a_4xx_that_is_not_429_is_never_retried() -> None:
    client, sent = client_with([problem(422, "unknown_voice", "no such voice")], max_retries=3)

    with pytest.raises(VittsError):
        client.synthesize(SynthesizeRequest(text="x", voice="bob"))

    assert len(sent) == 1


def test_a_non_problem_body_still_produces_a_usable_error() -> None:
    client, _ = client_with(
        [
            httpx.Response(
                502, content=b"<html>bad gateway</html>", headers={"content-type": "text/html"}
            )
        ]
    )

    with pytest.raises(VittsError) as caught:
        client.synthesize(SynthesizeRequest(text="x"))

    assert caught.value.status == 502
    assert "502" in str(caught.value)


def test_an_unreachable_api_is_a_transport_error() -> None:
    def handler(_: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused")

    client = VittsClient("k", transport=httpx.MockTransport(handler))

    with pytest.raises(VittsTransportError):
        client.synthesize(SynthesizeRequest(text="x"))


# T-118. Ordering is the contract (NFR-06): a stream that reorders is corrupt audio.
def test_stream_yields_chunks_in_order() -> None:
    def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"".join([b"\x01", b"\x02", b"\x03"]))

    client = VittsClient("k", transport=httpx.MockTransport(handler))

    assert b"".join(client.stream(SynthesizeRequest(text="x"))) == b"\x01\x02\x03"


def test_a_stream_that_is_refused_raises_rather_than_yielding_the_error_body() -> None:
    client = VittsClient(
        "k",
        transport=httpx.MockTransport(lambda _: problem(402, "quota_exceeded", "no quota")),
    )

    with pytest.raises(VittsError) as caught:
        list(client.stream(SynthesizeRequest(text="x")))

    assert caught.value.code == "quota_exceeded"


# T-119. US-14: a retried create must not produce a second job.
def test_create_job_always_sends_an_idempotency_key() -> None:
    job = json.dumps({"id": "11111111-2222-3333-4444-555555555555", "status": "queued"})
    client, sent = client_with(
        [
            httpx.Response(202, content=job, headers={"content-type": "application/json"}),
            httpx.Response(202, content=job, headers={"content-type": "application/json"}),
        ]
    )

    client.create_job(JobCreateRequest(text="long"))
    client.create_job(JobCreateRequest(text="long"), idempotency_key="mine")

    assert len(sent[0].headers["idempotency-key"]) > 10
    assert sent[1].headers["idempotency-key"] == "mine"


def test_wait_for_job_polls_until_terminal() -> None:
    running = json.dumps({"id": "11111111-2222-3333-4444-555555555555", "status": "synthesizing"})
    done = json.dumps({"id": "11111111-2222-3333-4444-555555555555", "status": "completed"})
    client, sent = client_with(
        [
            httpx.Response(200, content=running, headers={"content-type": "application/json"}),
            httpx.Response(200, content=done, headers={"content-type": "application/json"}),
        ]
    )

    job = client.wait_for_job("11111111-2222-3333-4444-555555555555", poll=0.001, timeout=5)

    assert job_status(job) == "completed"
    assert len(sent) == 2


def test_wait_for_job_gives_up_rather_than_polling_forever() -> None:
    running = json.dumps({"id": "11111111-2222-3333-4444-555555555555", "status": "queued"})
    client, _ = client_with(
        [
            httpx.Response(200, content=running, headers={"content-type": "application/json"})
            for _ in range(10)
        ]
    )

    with pytest.raises(TimeoutError, match="still queued"):
        client.wait_for_job("11111111-2222-3333-4444-555555555555", poll=0.01, timeout=0.02)


def test_an_empty_api_key_is_refused_at_construction() -> None:
    with pytest.raises(ValueError, match="api_key is required"):
        VittsClient("")
