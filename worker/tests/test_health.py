"""US-02: the gateway routes on Health, so every field must reflect real engine state."""

from __future__ import annotations

import threading

import pytest

from vitts_worker import health
from vitts_worker.config import Config
from vitts_worker.engine import Engine, EngineNotReadyError

REVISION = "c2bfbd67dc648cac455077333f7cf5c18a2e3bb4"


@pytest.fixture
def engine() -> Engine:
    return Engine(Config.from_env({"VITTS_MODEL_REVISION": REVISION}))


def test_not_ready_before_load(engine: Engine) -> None:
    snap = health.snapshot(engine)

    assert snap.ready is False
    assert snap.slots_total == 1  # ADR-003
    assert snap.slots_busy == 0
    assert list(snap.voice_ids) == []


def test_inference_before_load_is_refused(engine: Engine) -> None:
    with pytest.raises(EngineNotReadyError):
        _ = engine.tts


def test_model_version_is_the_pinned_revision(engine: Engine) -> None:
    assert health.snapshot(engine).model_version == REVISION


def test_slots_busy_follows_the_single_slot(engine: Engine) -> None:
    assert engine.acquire_slot(timeout=0.1) is True
    try:
        assert health.snapshot(engine).slots_busy == 1

        blocked = threading.Thread(target=lambda: engine.acquire_slot(timeout=0.01))
        blocked.start()
        blocked.join()
        assert health.snapshot(engine).slots_busy == 1
    finally:
        engine.release_slot()

    assert health.snapshot(engine).slots_busy == 0


def test_rtf_ewma_updates_after_every_request(engine: Engine) -> None:
    assert engine.rtf_ewma == 0.0

    assert engine.observe_rtf(elapsed_s=1.0, audio_s=2.0) == pytest.approx(0.5)
    # Second sample is folded in, it does not replace the first.
    second = engine.observe_rtf(elapsed_s=3.0, audio_s=2.0)
    assert 0.5 < second < 1.5
    assert health.snapshot(engine).rtf_ewma == pytest.approx(second, rel=1e-6)


def test_rtf_ignores_requests_that_produced_no_audio(engine: Engine) -> None:
    engine.observe_rtf(elapsed_s=1.0, audio_s=2.0)

    assert engine.observe_rtf(elapsed_s=5.0, audio_s=0.0) == pytest.approx(0.5)
