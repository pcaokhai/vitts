"""US-02 against the real weights. Opt-in: `pytest -m model`."""

from __future__ import annotations

import pytest

from vitts_worker.config import Config
from vitts_worker.engine import Engine

pytestmark = pytest.mark.model

REVISION = "c2bfbd67dc648cac455077333f7cf5c18a2e3bb4"


@pytest.fixture(scope="module")
def loaded() -> Engine:
    engine = Engine(Config.from_env({"VITTS_MODEL_REVISION": REVISION}))
    engine.load()
    return engine


def test_ready_only_after_load_and_warmup(loaded: Engine) -> None:
    cold = Engine(Config.from_env({"VITTS_MODEL_REVISION": REVISION}))

    assert cold.ready is False
    assert loaded.ready is True


def test_model_version_is_the_pinned_revision(loaded: Engine) -> None:
    assert loaded.model_version == REVISION


def test_shipped_voices_are_reported(loaded: Engine) -> None:
    assert "maichi" in loaded.voice_ids
    assert len(loaded.voice_ids) >= 8


def test_streaming_produces_audio_and_a_usable_rtf(loaded: Engine) -> None:
    import time

    started = time.monotonic()
    samples = sum(
        chunk.reshape(-1).shape[0]
        for chunk in loaded.tts.synthesize_stream("Xin chào các bạn.", voice="maichi")
    )
    audio_s = samples / 48_000

    assert audio_s > 0.3
    rtf = loaded.observe_rtf(time.monotonic() - started, audio_s)
    assert 0 < rtf < 1.0, f"expected faster than real time on CPU, got RTF {rtf:.2f}"
