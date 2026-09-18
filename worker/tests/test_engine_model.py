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


def test_first_frame_arrives_within_the_latency_budget(loaded: Engine) -> None:
    """US-01 AC-2: first frame ≤ 150 ms for a short input on the benchmark machine."""
    import threading
    import time

    from vitts_worker.engine import SynthesisParams

    short = "Xin chào các bạn nhé."  # 21 chars
    started = time.monotonic()
    frames = loaded.stream(short, "maichi", SynthesisParams(), threading.Event())
    next(frames)
    ttfa = time.monotonic() - started
    frames.close()

    assert ttfa < 0.150, f"first frame took {ttfa * 1000:.0f} ms"


def test_requested_sample_rates_preserve_duration(loaded: Engine) -> None:
    """US-01 AC-6 end to end: real audio resampled, duration within 1%."""
    import threading

    from vitts_worker.encode import NATIVE_SAMPLE_RATE, StreamResampler
    from vitts_worker.engine import SynthesisParams

    chunks = list(
        loaded.stream("Hôm nay trời đẹp.", "maichi", SynthesisParams(), threading.Event())
    )
    native = sum(c.reshape(-1).shape[0] for c in chunks)

    for rate in (8_000, 16_000, 24_000):
        resampler = StreamResampler(NATIVE_SAMPLE_RATE, rate)
        got = sum(len(resampler.process(c)) for c in chunks)
        expected = native * rate / NATIVE_SAMPLE_RATE
        assert abs(got - expected) / expected < 0.01
