"""T-16 and US-03/US-04: segmentation and container merging."""

from __future__ import annotations

import io

import numpy as np
import pytest
import soundfile as sf

from vitts_worker import preprocess
from vitts_worker.encode import CONTAINERS, duration_ms, encode, from_pcm16, silence, to_pcm16

LONG_TEXT = (
    "Xin chào các bạn. Hôm nay trời đẹp, chúng ta cùng đi dạo phố cổ Hà Nội nhé. "
    "Công ty chúng tôi cung cấp dịch vụ tổng hợp giọng nói tiếng Việt chất lượng cao. "
) * 12


def test_segments_are_utterance_sized() -> None:
    segments = preprocess.prepare(LONG_TEXT, normalize=False, max_chunk_sec=15)

    assert len(segments) > 1
    assert all(segment.strip() for segment in segments), "no empty segments"


# US-03 acceptance criterion 2.
def test_segmentation_preserves_every_non_whitespace_character() -> None:
    segments = preprocess.prepare(LONG_TEXT, normalize=False, max_chunk_sec=15)

    rejoined = "".join(segments).replace(" ", "")
    assert rejoined == LONG_TEXT.replace(" ", "")


def test_shorter_budget_yields_more_segments() -> None:
    few = preprocess.prepare(LONG_TEXT, normalize=False, max_chunk_sec=15)
    many = preprocess.prepare(LONG_TEXT, normalize=False, max_chunk_sec=5)

    assert len(many) > len(few)


def test_normalize_rewrites_digits() -> None:
    normalized = preprocess.prepare("Hôm nay là ngày 31/12/2025.", normalize=True, max_chunk_sec=15)
    verbatim = preprocess.prepare("Hôm nay là ngày 31/12/2025.", normalize=False, max_chunk_sec=15)

    assert "31/12/2025" in " ".join(verbatim)
    assert " ".join(normalized) != " ".join(verbatim), "normalisation must change the text"


def test_rejects_a_nonsense_budget() -> None:
    with pytest.raises(ValueError, match="max_chunk_sec"):
        preprocess.prepare("Xin chào.", normalize=False, max_chunk_sec=0)


# US-03 acceptance criterion 3.
def test_hundred_thousand_characters_segment_quickly() -> None:
    import time

    text = ("Xin chào các bạn, đây là một câu tiếng Việt bình thường. " * 2000)[:100_000]

    started = time.monotonic()
    segments = preprocess.prepare(text, normalize=False, max_chunk_sec=15)
    elapsed = time.monotonic() - started

    assert segments
    assert elapsed < 2.0, f"segmenting 100k characters took {elapsed:.2f}s"


def speech() -> np.ndarray:
    t = np.arange(48_000 * 2) / 48_000
    return (
        0.4 * np.sin(2 * np.pi * 180 * t) * (0.6 + 0.4 * np.sin(2 * np.pi * 3 * t))
        + 0.2 * np.sin(2 * np.pi * 420 * t)
    ).astype(np.float32)


@pytest.mark.parametrize("container", sorted(CONTAINERS))
def test_every_container_is_decodable_at_the_requested_rate(container: str) -> None:
    audio = speech()

    encoded = encode(audio, 48_000, container)
    decoded, rate = sf.read(io.BytesIO(encoded), dtype="float32")

    assert rate == 48_000
    assert decoded.shape[0] > 0


# T-16: merged duration is the sum of the segments plus the gaps, within 50 ms.
def test_merged_duration_is_the_sum_plus_gaps() -> None:
    segment = speech()
    gap = silence(0.25, 48_000)
    merged = np.concatenate([segment, gap, segment, gap, segment])

    expected_ms = 3 * duration_ms(segment, 48_000) + 2 * 250

    assert abs(duration_ms(merged, 48_000) - expected_ms) <= 50


def test_pcm16_roundtrip_preserves_the_signal() -> None:
    original = speech()

    restored = from_pcm16(to_pcm16(original))

    assert restored.shape == original.shape
    assert np.max(np.abs(restored - original)) < 1e-3, "16-bit quantisation only"


def test_unknown_container_is_refused() -> None:
    with pytest.raises(ValueError, match="unsupported format"):
        encode(speech(), 48_000, "flac")
