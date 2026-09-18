"""T-01 — Synthesize framing and validation (US-01 acceptance criteria 1, 4, 5, 6)."""

from __future__ import annotations

import asyncio

import grpc
import pytest
from conftest import FAKE_VOICE, FakeTTS, make_engine

from vitts_worker.gen import worker_pb2, worker_pb2_grpc
from vitts_worker.server import serve

Served = tuple[worker_pb2_grpc.WorkerStub, object, FakeTTS]
TEXT = "Xin chào các bạn."


def _request(**overrides: object) -> worker_pb2.SynthesizeRequest:
    fields: dict[str, object] = {"request_id": "r-1", "text": TEXT, "voice_id": FAKE_VOICE}
    fields.update(overrides)
    return worker_pb2.SynthesizeRequest(**fields)


async def _collect(
    stub: worker_pb2_grpc.WorkerStub, **overrides: object
) -> list[worker_pb2.AudioFrame]:
    return [frame async for frame in stub.Synthesize(_request(**overrides))]


async def test_frames_are_ordered_and_terminated_once(served: Served) -> None:
    stub, _engine, fake = served

    frames = await _collect(stub)

    assert [f.seq for f in frames] == list(range(len(frames)))
    assert [f.last for f in frames].count(True) == 1
    assert frames[-1].last is True
    assert len(frames) == fake.chunks + 1  # one terminator after the audio frames


async def test_frames_carry_pcm16_at_the_native_rate(served: Served) -> None:
    stub, _engine, fake = served

    frames = await _collect(stub)

    audio = frames[:-1]
    assert all(f.sample_rate == 48_000 for f in frames)
    assert all(len(f.pcm16) == int(48_000 * fake.chunk_seconds) * 2 for f in audio)
    assert frames[-1].pcm16 == b""


@pytest.mark.parametrize("rate", [8_000, 16_000, 24_000, 48_000])
async def test_requested_sample_rate_preserves_duration(served: Served, rate: int) -> None:
    stub, _engine, fake = served

    frames = await _collect(stub, output_sample_rate=rate)

    samples = sum(len(f.pcm16) for f in frames) // 2
    expected = fake.chunks * fake.chunk_seconds * rate
    assert abs(samples - expected) / expected < 0.01  # US-01 AC-6
    assert all(f.sample_rate == rate for f in frames)


async def test_chars_consumed_is_exact_when_the_stream_completes(served: Served) -> None:
    stub, _engine, _fake = served

    frames = await _collect(stub)

    assert frames[-1].chars_consumed == len(TEXT)
    assert all(f.chars_consumed <= len(TEXT) for f in frames)


@pytest.mark.parametrize(
    ("overrides", "detail"),
    [
        ({"text": "   "}, "empty"),
        ({"text": "a" * 3001}, "3000"),
        ({"voice_id": "nobody"}, "unknown voice_id"),
        ({"output_sample_rate": 44_100}, "output_sample_rate"),
    ],
)
async def test_invalid_requests_are_rejected(
    served: Served, overrides: dict[str, object], detail: str
) -> None:
    stub, _engine, _fake = served

    with pytest.raises(grpc.aio.AioRpcError) as err:
        await _collect(stub, **overrides)

    assert err.value.code() is grpc.StatusCode.INVALID_ARGUMENT
    assert detail in (err.value.details() or "")


async def test_second_concurrent_request_is_refused_not_queued() -> None:
    """US-01 AC-4 and ADR-007: overload fails fast, it never accepts then times out."""
    engine, cfg, _fake = make_engine(FakeTTS(chunks=6, frame_delay=0.05))
    server, port = await serve(engine, cfg, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    stub = worker_pb2_grpc.WorkerStub(channel)
    try:
        first = stub.Synthesize(_request())
        await first.read()  # first frame in flight, so the slot is held

        with pytest.raises(grpc.aio.AioRpcError) as err:
            async for _ in stub.Synthesize(_request(request_id="r-2")):
                pass

        assert err.value.code() is grpc.StatusCode.RESOURCE_EXHAUSTED
        first.cancel()
    finally:
        await channel.close()
        await server.stop(None)


async def test_requests_before_the_model_loads_are_unavailable() -> None:
    engine, cfg, _fake = make_engine()
    engine._tts = None  # back to a cold engine
    server, port = await serve(engine, cfg, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        with pytest.raises(grpc.aio.AioRpcError) as err:
            async for _ in worker_pb2_grpc.WorkerStub(channel).Synthesize(_request()):
                pass

        assert err.value.code() is grpc.StatusCode.UNAVAILABLE
    finally:
        await channel.close()
        await server.stop(None)


async def test_slot_is_released_after_a_completed_stream(served: Served) -> None:
    stub, engine, _fake = served

    await _collect(stub)
    await asyncio.sleep(0)

    assert engine.slots_busy == 0  # type: ignore[attr-defined]
