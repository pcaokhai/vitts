"""gRPC servicer. Transport only: inference lives in `engine`, readiness in `health`."""

from __future__ import annotations

import asyncio
import threading
import time
from typing import TYPE_CHECKING, cast

import grpc
import structlog

from . import health
from .encode import NATIVE_SAMPLE_RATE, SUPPORTED_SAMPLE_RATES, StreamResampler, to_pcm16
from .engine import CHARS_PER_AUDIO_SECOND, SynthesisParams
from .gen import worker_pb2, worker_pb2_grpc

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    import numpy as np

    from .config import Config
    from .engine import Engine

log = structlog.get_logger(__name__)

_EXHAUSTED = object()


class WorkerServicer(worker_pb2_grpc.WorkerServicer):
    def __init__(self, engine: Engine, cfg: Config) -> None:
        self._engine = engine
        self._cfg = cfg

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
        engine = self._engine
        if not engine.ready:
            await context.abort(grpc.StatusCode.UNAVAILABLE, "model is still loading")

        rate = await self._validated_rate(request.output_sample_rate, context)
        await self._validate_request(request, context)

        # Fail fast rather than queue: the gateway's dispatcher owns admission (ADR-007).
        if not engine.acquire_slot(timeout=0):
            await context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, "inference slot busy")

        cancel = threading.Event()
        resampler = StreamResampler(NATIVE_SAMPLE_RATE, rate)
        frames = engine.stream(request.text, request.voice_id, _params_of(request), cancel)
        pending: asyncio.Task[object] | None = None
        started = time.monotonic()
        native_samples = 0
        seq = 0

        try:
            while True:
                # shield: on client cancellation the CancelledError must reach us while
                # the worker thread finishes its frame, so nothing touches the generator
                # while it is running.
                pending = asyncio.ensure_future(asyncio.to_thread(next, frames, _EXHAUSTED))
                chunk = await asyncio.shield(pending)
                pending = None
                if chunk is _EXHAUSTED:
                    break

                frame = cast("np.ndarray", chunk)
                native_samples += frame.reshape(-1).shape[0]
                audio = resampler.process(frame)
                yield worker_pb2.AudioFrame(
                    seq=seq,
                    pcm16=to_pcm16(audio),
                    sample_rate=rate,
                    last=False,
                    chars_consumed=_chars_consumed(
                        len(request.text), native_samples / NATIVE_SAMPLE_RATE
                    ),
                )
                seq += 1

            yield worker_pb2.AudioFrame(
                seq=seq,
                pcm16=b"",
                sample_rate=rate,
                last=True,
                chars_consumed=len(request.text),
            )
        finally:
            cancel.set()
            if pending is not None:
                await asyncio.gather(pending, return_exceptions=True)
            frames.close()
            engine.release_slot()
            audio_seconds = native_samples / NATIVE_SAMPLE_RATE
            engine.observe_rtf(time.monotonic() - started, audio_seconds)
            log.info(
                "synthesize.done",
                request_id=request.request_id,
                frames=seq,
                audio_seconds=round(audio_seconds, 3),
                sample_rate=rate,
            )

    async def _validate_request(
        self,
        request: worker_pb2.SynthesizeRequest,
        context: grpc.aio.ServicerContext[worker_pb2.SynthesizeRequest, worker_pb2.AudioFrame],
    ) -> None:
        """Re-check at the worker what the gateway already checked at the edge."""
        if not request.text.strip():
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "text is empty")
        if len(request.text) > self._cfg.max_text_chars:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"text exceeds {self._cfg.max_text_chars} characters",
            )
        if request.voice_id not in self._engine.voice_ids:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "unknown voice_id")

    @staticmethod
    async def _validated_rate(
        requested: int,
        context: grpc.aio.ServicerContext[worker_pb2.SynthesizeRequest, worker_pb2.AudioFrame],
    ) -> int:
        rate = requested or NATIVE_SAMPLE_RATE
        if rate not in SUPPORTED_SAMPLE_RATES:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"output_sample_rate must be one of {SUPPORTED_SAMPLE_RATES}",
            )
        return rate

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


async def serve(engine: Engine, cfg: Config, addr: str) -> tuple[grpc.aio.Server, int]:
    """Start a server bound to `addr`. The caller owns wait_for_termination and stop.

    The bound port is returned because `addr` may end in `:0`, which tests use.
    """
    server = grpc.aio.server()
    worker_pb2_grpc.add_WorkerServicer_to_server(WorkerServicer(engine, cfg), server)
    port = server.add_insecure_port(addr)
    await server.start()
    log.info("grpc.listening", addr=addr, port=port)
    return server, port


def _params_of(request: worker_pb2.SynthesizeRequest) -> SynthesisParams:
    """Proto zero values mean "unset", so fall back to the contract's defaults."""
    p = request.params
    defaults = SynthesisParams()
    return SynthesisParams(
        cfg_scale=p.cfg_scale or defaults.cfg_scale,
        audio_temperature=p.audio_temperature or defaults.audio_temperature,
        audio_topk=p.audio_topk or defaults.audio_topk,
        audio_topp=p.audio_topp or defaults.audio_topp,
        audio_repetition_penalty=p.audio_repetition_penalty or defaults.audio_repetition_penalty,
        eoa_extra_frames=p.eoa_extra_frames or defaults.eoa_extra_frames,
    )


def _chars_consumed(total_chars: int, audio_seconds: float) -> int:
    """Best estimate of text consumed so far, for metering a cancelled stream."""
    return min(total_chars, int(audio_seconds * CHARS_PER_AUDIO_SECOND))
