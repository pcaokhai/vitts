"""Text preparation. A thin wrapper over upstream, never a reimplementation (ADR-002).

`chunk_text` lives in `zerotts.chunking` rather than the package's top level, which is
why an earlier survey of `zerotts` missed it; it is the upstream chunker US-03 names.
"""

from __future__ import annotations

from zerotts.chunking import chunk_text
from zerotts.text_norm import normalize_vi_text

# The contract's default, in seconds of estimated speech per segment.
DEFAULT_MAX_CHUNK_SEC = 15.0


def prepare(
    text: str, *, normalize: bool, max_chunk_sec: float = DEFAULT_MAX_CHUNK_SEC
) -> list[str]:
    """Normalize if asked, then split into utterance-sized segments.

    Order matters: normalising after chunking would let a number expand across a
    boundary and change how the segment is read.
    """
    if max_chunk_sec <= 0:
        raise ValueError(f"max_chunk_sec must be positive, got {max_chunk_sec}")

    prepared = normalize_vi_text(text) if normalize else text
    segments = [
        segment for segment in chunk_text(prepared, max_chunk_sec=max_chunk_sec) if segment.strip()
    ]
    return segments
