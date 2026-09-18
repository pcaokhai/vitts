"""T-02 — cancellation stops inference and frees the slot (US-01 acceptance criterion 3)."""

from __future__ import annotations

import asyncio
import threading
import time

import grpc
import pytest
from conftest import FAKE_VOICE, FakeTTS, make_engine

from vitts_worker.engine import SynthesisParams
from vitts_worker.gen import worker_pb2, worker_pb2_grpc
from vitts_worker.server import serve

CANCEL_BUDGET_SECONDS = 0.2
FRAME_DELAY = 0.02


async def _wait_for_free_slot(engine: object, budget: float) -> float:
    """Seconds until the inference slot is released, or `budget` if it never is."""
    deadline = time.monotonic() + budget
    while time.monotonic() < deadline:
        if engine.slots_busy == 0:  # type: ignore[attr-defined]
            return time.monotonic() - (deadline - budget)
        await asyncio.sleep(0.005)
    return budget


async def test_client_cancel_stops_inference_and_frees_the_slot() -> None:
    fake = FakeTTS(chunks=500, chunk_seconds=0.05, frame_delay=FRAME_DELAY)
    engine, cfg, _ = make_engine(fake)
    server, port = await serve(engine, cfg, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        call = worker_pb2_grpc.WorkerStub(channel).Synthesize(
            worker_pb2.SynthesizeRequest(request_id="r-1", text="Xin chào.", voice_id=FAKE_VOICE)
        )
        await call.read()
        await call.read()
        produced_at_cancel = fake.produced

        started = time.monotonic()
        call.cancel()
        freed_after = await _wait_for_free_slot(engine, CANCEL_BUDGET_SECONDS)

        assert freed_after < CANCEL_BUDGET_SECONDS, (
            f"slot still held {freed_after * 1000:.0f} ms after cancel"
        )
        assert engine.slots_busy == 0
        assert fake.closed is True, "the upstream generator was not closed"
        # One more frame may land: cancellation is checked between frames, by design.
        assert fake.produced <= produced_at_cancel + 2
        assert fake.produced < fake.chunks, "inference ran to completion despite cancel"
        assert time.monotonic() - started < CANCEL_BUDGET_SECONDS
    finally:
        await channel.close()
        await server.stop(None)


async def test_a_cancelled_stream_leaves_the_worker_usable() -> None:
    fake = FakeTTS(chunks=500, chunk_seconds=0.05, frame_delay=FRAME_DELAY)
    engine, cfg, _ = make_engine(fake)
    server, port = await serve(engine, cfg, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    stub = worker_pb2_grpc.WorkerStub(channel)
    try:
        call = stub.Synthesize(
            worker_pb2.SynthesizeRequest(request_id="r-1", text="Xin chào.", voice_id=FAKE_VOICE)
        )
        await call.read()
        call.cancel()
        await _wait_for_free_slot(engine, CANCEL_BUDGET_SECONDS)

        engine._tts = FakeTTS(chunks=3, chunk_seconds=0.05)
        frames = [
            f
            async for f in stub.Synthesize(
                worker_pb2.SynthesizeRequest(request_id="r-2", text="Lần hai.", voice_id=FAKE_VOICE)
            )
        ]

        assert frames[-1].last is True
        assert [f.seq for f in frames] == list(range(len(frames)))
    finally:
        await channel.close()
        await server.stop(None)


def test_engine_stream_stops_when_the_cancel_flag_is_set() -> None:
    """The engine-level contract the servicer depends on, without gRPC in the way."""
    fake = FakeTTS(chunks=50, chunk_seconds=0.01)
    engine, _cfg, _ = make_engine(fake)
    cancel = threading.Event()

    taken = []
    for chunk in engine.stream("Xin chào.", FAKE_VOICE, _default_params(), cancel):
        taken.append(chunk)
        if len(taken) == 3:
            cancel.set()

    assert len(taken) == 3
    assert fake.closed is True


def _default_params() -> SynthesisParams:
    return SynthesisParams()


@pytest.mark.model
def test_real_inference_cancels_within_the_budget() -> None:
    """The same guarantee against real weights: a frame is the cancellation granularity."""
    from vitts_worker.config import Config
    from vitts_worker.engine import Engine, SynthesisParams

    engine = Engine(Config.from_env({"VITTS_MODEL_REVISION": conftest_revision()}))
    engine.load()
    cancel = threading.Event()

    text = "Đây là một câu tiếng Việt đủ dài để mô hình phải sinh nhiều khung âm thanh liên tiếp."
    frames = engine.stream(text, "maichi", SynthesisParams(), cancel)
    next(frames)
    next(frames)

    started = time.monotonic()
    cancel.set()
    for _ in frames:
        pass
    stopped = time.monotonic() - started

    assert stopped < CANCEL_BUDGET_SECONDS, f"cancel took {stopped * 1000:.0f} ms"


def conftest_revision() -> str:
    from conftest import REVISION

    return REVISION
