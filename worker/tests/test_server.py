"""The servicer is wiring: Health must answer over real gRPC before weights exist."""

from __future__ import annotations

from collections.abc import AsyncIterator

import grpc
import pytest
import pytest_asyncio

from vitts_worker.config import Config
from vitts_worker.engine import Engine
from vitts_worker.gen import worker_pb2, worker_pb2_grpc
from vitts_worker.server import serve

REVISION = "c2bfbd67dc648cac455077333f7cf5c18a2e3bb4"


@pytest_asyncio.fixture
async def stub() -> AsyncIterator[worker_pb2_grpc.WorkerStub]:
    """A stub talking to a real server over loopback, with no model loaded."""
    engine = Engine(Config.from_env({"VITTS_MODEL_REVISION": REVISION}))
    server, port = await serve(engine, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield worker_pb2_grpc.WorkerStub(channel)
    finally:
        await channel.close()
        await server.stop(None)


async def test_health_answers_while_the_model_is_still_loading(
    stub: worker_pb2_grpc.WorkerStub,
) -> None:
    reply = await stub.Health(worker_pb2.HealthRequest())

    assert reply.ready is False
    assert reply.model_version == REVISION
    assert reply.slots_total == 1
    assert reply.slots_busy == 0


async def test_unimplemented_rpcs_fail_fast_with_a_clear_status(
    stub: worker_pb2_grpc.WorkerStub,
) -> None:
    with pytest.raises(grpc.aio.AioRpcError) as err:
        await stub.Segment(worker_pb2.SegmentRequest(text="xin chào"))

    assert err.value.code() is grpc.StatusCode.UNIMPLEMENTED
    assert "task 2.2" in (err.value.details() or "")
