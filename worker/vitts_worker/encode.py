"""PCM conversion and resampling for the streaming path.

Container formats (mp3, ogg_opus) belong to `Merge` in task 2.2; this module only has to
turn the model's float32 frames into the little-endian int16 the proto specifies, at the
sample rate the caller asked for.
"""

from __future__ import annotations

from math import gcd

import numpy as np
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
