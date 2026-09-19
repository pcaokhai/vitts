"""PCM conversion, resampling, and container encoding.

The streaming path needs only PCM16 at the requested rate; `Merge` additionally wraps
concatenated segments in a container (US-04).
"""

from __future__ import annotations

import io
from math import gcd

import numpy as np
import soundfile as sf
from scipy.signal import resample_poly

NATIVE_SAMPLE_RATE = 48_000
SUPPORTED_SAMPLE_RATES = (8_000, 16_000, 24_000, 48_000)

# Input samples carried into the next chunk so the polyphase filter sees real context
# across a chunk boundary instead of an implicit zero pad, which would click.
_CONTEXT_SAMPLES = 64


def to_pcm16(samples: np.ndarray) -> bytes:
    """float32 in [-1, 1] → little-endian int16 bytes. Clipped, not wrapped."""
    clipped = np.clip(samples.reshape(-1), -1.0, 1.0)
    return bytes((clipped * 32767.0).astype("<i2").tobytes())


class StreamResampler:
    """Chunk-by-chunk resampler that keeps filter context across calls.

    Resampling each chunk independently produces a discontinuity at every boundary;
    carrying `_CONTEXT_SAMPLES` of input and discarding the matching output samples
    removes it. Output length tracks `len(input) * dst / src` to within a sample per
    chunk, which keeps total duration inside the ±1% US-01 requires.
    """

    def __init__(self, src_rate: int = NATIVE_SAMPLE_RATE, dst_rate: int = NATIVE_SAMPLE_RATE):
        if dst_rate not in SUPPORTED_SAMPLE_RATES:
            raise ValueError(f"unsupported sample rate {dst_rate}")
        divisor = gcd(src_rate, dst_rate)
        self._up = dst_rate // divisor
        self._down = src_rate // divisor
        self._context = np.zeros(0, dtype=np.float32)
        self._emitted = 0
        self._consumed = 0

    @property
    def passthrough(self) -> bool:
        return self._up == 1 and self._down == 1

    def process(self, chunk: np.ndarray) -> np.ndarray:
        flat = chunk.reshape(-1).astype(np.float32, copy=False)
        if self.passthrough:
            return flat

        buffered = np.concatenate([self._context, flat])
        resampled = resample_poly(buffered, self._up, self._down)

        # Drop the output that belongs to the carried context, it was emitted already.
        skip = (len(self._context) * self._up) // self._down
        out = resampled[skip:]

        # Hold total output to the exact ratio so per-chunk rounding cannot accumulate.
        self._consumed += len(flat)
        wanted = (self._consumed * self._up) // self._down - self._emitted
        out = out[: max(wanted, 0)]

        self._emitted += len(out)
        self._context = buffered[-_CONTEXT_SAMPLES:]
        return np.asarray(out, dtype=np.float32)


# Container formats `Merge` can produce, mapped to (libsndfile format, subtype).
#
# libsndfile carries LAME and Opus, so the worker needs no external encoder. Both are
# variable-bitrate, so US-04's "MP3 at 64 kbps, Opus at 32 kbps" is approached through a
# quality setting rather than set exactly - see MERGE_QUALITY below.
CONTAINERS = {
    "wav": ("WAV", "PCM_16"),
    "mp3": ("MP3", "MPEG_LAYER_III"),
    "ogg_opus": ("OGG", "OPUS"),
}

# libsndfile takes a 0..1 compression level, not a bitrate: 0 is best quality and largest,
# 1 is smallest, and both codecs are variable-bitrate. US-04 asks for "MP3 at 64 kbps,
# Opus at 32 kbps", which these values approach rather than pin - measured on a
# speech-shaped signal, 0.55 gives ~65 kbps MP3 and 0.9 gives ~34 kbps Opus. Exact
# constant bitrates would need an encoder that exposes one; the sweep behind these
# numbers is in docs/plans/2.1-2.2.md.
MERGE_QUALITY = {
    "mp3": 0.55,
    "ogg_opus": 0.90,
}


def silence(seconds: float, sample_rate: int) -> np.ndarray:
    """A gap between merged segments, so sentences do not run together (US-04)."""
    return np.zeros(max(0, int(seconds * sample_rate)), dtype=np.float32)


def from_pcm16(raw: bytes) -> np.ndarray:
    """Little-endian int16 bytes back to float32 in [-1, 1]."""
    return np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0


def encode(samples: np.ndarray, sample_rate: int, container: str) -> bytes:
    """Encode mono float32 audio into a container.

    Raises ValueError for an unknown container: a caller asking for a format we cannot
    produce must be told, not handed a mislabelled file.
    """
    if container not in CONTAINERS:
        raise ValueError(f"unsupported format {container!r}; expected one of {sorted(CONTAINERS)}")

    fmt, subtype = CONTAINERS[container]
    buffer = io.BytesIO()
    with sf.SoundFile(
        buffer,
        mode="w",
        samplerate=sample_rate,
        channels=1,
        format=fmt,
        subtype=subtype,
        compression_level=MERGE_QUALITY.get(container),
    ) as handle:
        handle.write(samples.reshape(-1))

    return buffer.getvalue()


def duration_ms(samples: np.ndarray, sample_rate: int) -> int:
    """Length of `samples` in milliseconds."""
    if sample_rate <= 0:
        return 0
    return round(samples.reshape(-1).shape[0] * 1000 / sample_rate)
