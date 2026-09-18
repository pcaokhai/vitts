"""gRPC servicer. Transport only: inference lives in `engine`, readiness in `health`."""

from __future__ import annotations

from typing import TYPE_CHECKING

import grpc
import structlog

from . import health
from .gen import worker_pb2, worker_pb2_grpc

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from .engine import Engine

log = structlog.get_logger(__name__)


class WorkerServicer(worker_pb2_grpc.WorkerServicer):
    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    async def Health(  # noqa: N802 - name fixed by the proto contract
        self,
        request: worker_pb2.HealthRequest,
        context: grpc.aio.ServicerContext[worker_pb2.HealthRequest, worker_pb2.HealthResponse],
    ) -> worker_pb2.HealthResponse:
        return health.snapshot(self._engine)

    async def Synthesize(  # noqa: N802
        self,
        request: worker_pb2.SynthesizeRequest,
        context: grpc.aio.ServicerContext[worker_pb2.SynthesizeRequest, worker_pb2.AudioFrame],
    ) -> AsyncIterator[worker_pb2.AudioFrame]:
        await context.abort(grpc.StatusCode.UNIMPLEMENTED, "Synthesize lands in task 0.4")
        raise AssertionError("unreachable")  # abort() never returns
        yield  # marks this a generator for the grpc.aio streaming contract

    async def Segment(  # noqa: N802
        self,
        request: worker_pb2.SegmentRequest,
        context: grpc.aio.ServicerContext[worker_pb2.SegmentRequest, worker_pb2.SegmentResponse],
    ) -> worker_pb2.SegmentResponse:
        await context.abort(grpc.StatusCode.UNIMPLEMENTED, "Segment lands in task 2.2")
        raise AssertionError("unreachable")

    async def Merge(  # noqa: N802
        self,
        request: worker_pb2.MergeRequest,
        context: grpc.aio.ServicerContext[worker_pb2.MergeRequest, worker_pb2.MergeResponse],
    ) -> worker_pb2.MergeResponse:
        await context.abort(grpc.StatusCode.UNIMPLEMENTED, "Merge lands in task 2.2")
        raise AssertionError("unreachable")


async def serve(engine: Engine, addr: str) -> tuple[grpc.aio.Server, int]:
    """Start a server bound to `addr`. The caller owns wait_for_termination and stop.

    The bound port is returned because `addr` may end in `:0`, which tests use.
    """
    server = grpc.aio.server()
    worker_pb2_grpc.add_WorkerServicer_to_server(WorkerServicer(engine), server)
    port = server.add_insecure_port(addr)
    await server.start()
    log.info("grpc.listening", addr=addr, port=port)
    return server, port
