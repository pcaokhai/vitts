"""Shared fixtures.

The servicer tests run against a real gRPC server and a real `Engine`, with only the
ONNX session replaced. That keeps slot handling, cancellation and RTF accounting under
test in CI, where downloading weights is not acceptable.
"""

from __future__ import annotations

import threading
import time
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass

import grpc
import numpy as np
import pytest
import pytest_asyncio

from vitts_worker.config import Config
from vitts_worker.engine import Engine
from vitts_worker.gen import worker_pb2_grpc
from vitts_worker.server import serve

REVISION = "c2bfbd67dc648cac455077333f7cf5c18a2e3bb4"
FAKE_VOICE = "maichi"
NATIVE_RATE = 48_000


@dataclass
class FakeTTS:
    """Stands in for the ZeroTTS session: same streaming shape, no inference."""

    chunks: int = 8
    chunk_seconds: float = 0.1
    frame_delay: float = 0.0
    produced: int = 0
    closed: bool = False

    def list_voices(self) -> list[str]:
        return [FAKE_VOICE]

    def synthesize_stream(
        self, text: str, voice: str | None = None, **_: object
    ) -> Iterator[np.ndarray]:
        samples = int(NATIVE_RATE * self.chunk_seconds)
        try:
            for i in range(self.chunks):
                if self.frame_delay:
                    time.sleep(self.frame_delay)
                self.produced = i + 1
                tone = np.sin(np.arange(samples, dtype=np.float32) / 20.0) * 0.5
                yield tone.reshape(1, -1)
        finally:
            self.closed = True


def make_engine(tts: FakeTTS | None = None, **env: str) -> tuple[Engine, Config, FakeTTS]:
    cfg = Config.from_env({"VITTS_MODEL_REVISION": REVISION, **env})
    engine = Engine(cfg)
    fake = tts or FakeTTS()
    engine._tts = fake  # the ONNX session is the only thing faked
    engine._voice_ids = (FAKE_VOICE,)
    return engine, cfg, fake


@pytest_asyncio.fixture
async def served() -> AsyncIterator[tuple[worker_pb2_grpc.WorkerStub, Engine, FakeTTS]]:
    engine, cfg, fake = make_engine()
    server, port = await serve(engine, cfg, "127.0.0.1:0")
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield worker_pb2_grpc.WorkerStub(channel), engine, fake
    finally:
        await channel.close()
        await server.stop(None)


@pytest.fixture
def cancel_event() -> threading.Event:
    return threading.Event()
