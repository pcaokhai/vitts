"""gRPC servicer. Transport only: inference lives in `engine`, readiness in `health`."""

from __future__ import annotations

import asyncio
import threading
import time
from typing import TYPE_CHECKING, cast

import grpc
import numpy as np
import structlog

from . import health, preprocess
from .encode import (
    CONTAINERS,
    NATIVE_SAMPLE_RATE,
    SUPPORTED_SAMPLE_RATES,
    StreamResampler,
    duration_ms,
    encode,
    from_pcm16,
    silence,
    to_pcm16,
)
from .engine import CHARS_PER_AUDIO_SECOND, SynthesisParams
from .gen import worker_pb2, worker_pb2_grpc
from .objects import ObjectMissingError, Store

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    import numpy as np

    from .config import Config
    from .engine import Engine

log = structlog.get_logger(__name__)

_EXHAUSTED = object()


# Segmenting 100k characters is pure Python and must not block the event loop, so it
# runs in a thread like inference does.
MAX_SEGMENT_TEXT_CHARS = 100_000

# Default gap between merged segments, in milliseconds (worker.proto).
DEFAULT_GAP_MS = 250


class WorkerServicer(worker_pb2_grpc.WorkerServicer):
    def __init__(self, engine: Engine, cfg: Config, store: Store | None = None) -> None:
        self._engine = engine
        self._cfg = cfg
        self._store = store

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
        text = request.text
        if not text.strip():
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "text is empty")
        if len(text) > MAX_SEGMENT_TEXT_CHARS:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"text exceeds {MAX_SEGMENT_TEXT_CHARS} characters",
            )

        max_chunk_sec = request.max_chunk_sec or preprocess.DEFAULT_MAX_CHUNK_SEC
        if max_chunk_sec <= 0:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "max_chunk_sec must be positive")

        started = time.monotonic()
        segments = await asyncio.to_thread(
            preprocess.prepare,
            text,
            normalize=request.normalize,
            max_chunk_sec=float(max_chunk_sec),
        )

        log.info(
            "segment.done",
            chars=len(text),
            segments=len(segments),
            seconds=round(time.monotonic() - started, 3),
        )
        return worker_pb2.SegmentResponse(
            segments=segments, model_version=self._engine.model_version
        )

    async def Merge(  # noqa: N802
        self,
        request: worker_pb2.MergeRequest,
        context: grpc.aio.ServicerContext[worker_pb2.MergeRequest, worker_pb2.MergeResponse],
    ) -> worker_pb2.MergeResponse:
        if self._store is None:
            await context.abort(
                grpc.StatusCode.FAILED_PRECONDITION, "worker has no object storage configured"
            )
        if not request.s3_keys:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "no segments to merge")
        if not request.output_s3_key:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "output_s3_key is required")

        container = request.format or "wav"
        if container not in CONTAINERS:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"format must be one of {sorted(CONTAINERS)}",
            )

        sample_rate = request.sample_rate or NATIVE_SAMPLE_RATE
        if sample_rate not in SUPPORTED_SAMPLE_RATES:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"sample_rate must be one of {SUPPORTED_SAMPLE_RATES}",
            )

        gap_ms = request.gap_ms if request.gap_ms > 0 else DEFAULT_GAP_MS

        try:
            merged, encoded = await asyncio.to_thread(
                self._merge_segments, list(request.s3_keys), sample_rate, gap_ms, container
            )
        except ObjectMissingError as missing:
            # Naming the key is what makes this actionable for the orchestrator, which
            # knows which segment failed to upload (US-04 acceptance criterion 3).
            await context.abort(
                grpc.StatusCode.FAILED_PRECONDITION, f"segment object not found: {missing.key}"
            )

        content_type = {"wav": "audio/wav", "mp3": "audio/mpeg", "ogg_opus": "audio/ogg"}[container]
        await asyncio.to_thread(self._store_output, request.output_s3_key, encoded, content_type)

        total_ms = duration_ms(merged, sample_rate)
        log.info(
            "merge.done",
            segments=len(request.s3_keys),
            format=container,
            duration_ms=total_ms,
            bytes=len(encoded),
        )
        return worker_pb2.MergeResponse(
            output_s3_key=request.output_s3_key, duration_ms=total_ms, bytes=len(encoded)
        )

    def _merge_segments(
        self, keys: list[str], sample_rate: int, gap_ms: int, container: str
    ) -> tuple[np.ndarray, bytes]:
        """Concatenate PCM segments with gaps, then encode. Blocking; runs in a thread."""
        assert self._store is not None  # guarded by the caller

        gap = silence(gap_ms / 1000.0, sample_rate)
        parts: list[np.ndarray] = []
        for index, key in enumerate(keys):
            if index:
                parts.append(gap)
            parts.append(from_pcm16(self._store.get(key)))

        merged = np.concatenate(parts) if parts else np.zeros(0, dtype=np.float32)
        return merged, encode(merged, sample_rate, container)

    def _store_output(self, key: str, body: bytes, content_type: str) -> None:
        assert self._store is not None  # guarded by the caller
        self._store.put(key, body, content_type)


async def serve(
    engine: Engine, cfg: Config, addr: str, store: Store | None = None
) -> tuple[grpc.aio.Server, int]:
    """Start a server bound to `addr`. The caller owns wait_for_termination and stop.

    The bound port is returned because `addr` may end in `:0`, which tests use.
    """
    server = grpc.aio.server()
    worker_pb2_grpc.add_WorkerServicer_to_server(WorkerServicer(engine, cfg, store), server)
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
