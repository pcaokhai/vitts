"""Readiness reporting. The gateway routes on this, so it must never lie (US-02)."""

from __future__ import annotations

from typing import TYPE_CHECKING

from .gen import worker_pb2

if TYPE_CHECKING:
    from .engine import Engine


def snapshot(engine: Engine) -> worker_pb2.HealthResponse:
    """Build a HealthResponse from live engine state."""
    return worker_pb2.HealthResponse(
        ready=engine.ready,
        model_version=engine.model_version,
        slots_total=engine.slots_total,
        slots_busy=engine.slots_busy,
        rtf_ewma=engine.rtf_ewma,
        voice_ids=list(engine.voice_ids),
    )
