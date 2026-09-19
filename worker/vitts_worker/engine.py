"""ZeroTTS wrapper: the only place that talks to the model (ADR-002).

One inference at a time per process (ADR-003): `_slot` is the whole admission control,
and the gateway's dispatcher is what keeps callers from queueing behind it.
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Any

import structlog

from . import mirror

if TYPE_CHECKING:
    from collections.abc import Generator

    import numpy as np

    from .config import Config

log = structlog.get_logger(__name__)

RTF_EWMA_ALPHA = 0.2

# Model frames per streamed chunk. Cancellation is checked between chunks, so this is the
# granularity of "stop now": upstream's default of 16 frames measured 735 ms to release
# the slot, well past the 200 ms US-01 acceptance criterion 3 allows. Four frames brings
# it inside the budget at the cost of slightly more per-chunk overhead.
STREAM_MAX_CHUNK_FRAMES = 4

# Characters of Vietnamese per second of audio, fixed by ADR-010. Used only to meter a
# request the caller cancelled mid-stream: upstream exposes no text position, so a
# cancelled request is billed from the audio it actually produced. A completed request
# is always billed the exact character count, so this never affects a normal invoice.
#
# Measured in task 0.4 over 4 voices x 3 lengths: 14.6 - 25.8 chars/s, mean 18.2. The
# floor of that range is deliberate: an estimate that is too low under-bills the tenant,
# one that is too high charges them for audio they cancelled and never received. The
# gateway measures delivered audio itself and does not bill from this field (ADR-010).
CHARS_PER_AUDIO_SECOND = 14.5

# Everything inference needs and nothing else: the repo also ships ~500 MB of demo
# audio and a banner that would otherwise land in every image and every mirror.
MODEL_FILE_PATTERNS = (
    "config.json",
    "tokenizer.json",
    "*.npy",
    "onnx/**",
    "voices/**/voice.npz",
    "voices/**/meta.json",
    "voices/index.json",
)


class EngineNotReadyError(RuntimeError):
    """Raised when inference is attempted before load() has succeeded."""


@dataclass(frozen=True, slots=True)
class SynthesisParams:
    """Proto `SynthesisParams` with the contract's documented defaults applied."""

    cfg_scale: float = 1.0
    audio_temperature: float = 0.8
    audio_topk: int = 25
    audio_topp: float = 0.95
    audio_repetition_penalty: float = 1.2
    eoa_extra_frames: int = 1


class Engine:
    """Owns the ZeroTTS session, its readiness and its rolling RTF."""

    def __init__(self, cfg: Config) -> None:
        self._cfg = cfg
        self._tts: Any | None = None
        self._model_dir: Path | None = None
        self._voice_ids: tuple[str, ...] = ()
        self._slot = threading.Lock()
        self._rtf_lock = threading.Lock()
        self._rtf_ewma = 0.0

    # --- lifecycle -----------------------------------------------------------------

    def load(self) -> None:
        """Fetch weights, open the ONNX session and warm it up. Blocking; call once."""
        from zerotts import ZeroTTS

        started = time.monotonic()
        model_dir = self._resolve_model_dir()

        tts = ZeroTTS(
            model_dir=model_dir,
            intra_op_num_threads=self._cfg.ort_intra_op_threads,
            warmup=False,  # warm up below so `ready` only flips after it succeeds
        )
        tts.warmup()

        self._tts = tts
        self._model_dir = model_dir
        self._voice_ids = tuple(sorted(str(v) for v in tts.list_voices()))
        log.info(
            "engine.ready",
            model_version=self.model_version,
            voices=len(self._voice_ids),
            load_seconds=round(time.monotonic() - started, 2),
            threads=self._cfg.ort_intra_op_threads,
        )

    def _resolve_model_dir(self) -> Path:
        cfg = self._cfg
        if cfg.model_mirror_s3 and cfg.s3 is not None:
            s3 = mirror.client(cfg.s3)
            prefix = mirror.revision_prefix(cfg.model_mirror_s3, cfg.model_repo, cfg.model_revision)
            dest = cfg.model_dir / cfg.model_revision
            if mirror.download(s3, cfg.s3.bucket, prefix, dest):
                return dest
            hub_dir = self._fetch_from_hub()
            mirror.upload(s3, cfg.s3.bucket, prefix, hub_dir)
            return hub_dir

        return self._fetch_from_hub()

    def _fetch_from_hub(self) -> Path:
        """Materialise the pinned revision as real files under the model dir.

        Not the shared HF blob cache: its snapshots are symlinks into `blobs/`, and
        onnxruntime rejects an ONNX external-data file whose resolved path leaves the
        model directory ("External data path escapes model directory"). `local_dir`
        writes real files, which also makes the directory safe to mirror to S3 as-is.
        """
        from huggingface_hub import snapshot_download

        cfg = self._cfg
        dest = cfg.model_dir / cfg.model_revision
        log.info(
            "model.hub.fetch", repo=cfg.model_repo, revision=cfg.model_revision, dest=str(dest)
        )
        snapshot_download(
            repo_id=cfg.model_repo,
            revision=cfg.model_revision,
            local_dir=dest,
            allow_patterns=list(MODEL_FILE_PATTERNS),
        )
        return dest

    # --- state ---------------------------------------------------------------------

    @property
    def ready(self) -> bool:
        """False until weights are loaded and a warm-up utterance has succeeded."""
        return self._tts is not None

    @property
    def model_version(self) -> str:
        return self._cfg.model_revision

    @property
    def voice_ids(self) -> tuple[str, ...]:
        return self._voice_ids

    @property
    def slots_total(self) -> int:
        return 1  # ADR-003

    @property
    def slots_busy(self) -> int:
        return 1 if self._slot.locked() else 0

    @property
    def rtf_ewma(self) -> float:
        with self._rtf_lock:
            return self._rtf_ewma

    def observe_rtf(self, elapsed_s: float, audio_s: float) -> float:
        """Fold one request into the rolling real-time factor. Returns the new value."""
        if audio_s <= 0:
            return self.rtf_ewma
        sample = elapsed_s / audio_s
        with self._rtf_lock:
            current = self._rtf_ewma
            self._rtf_ewma = (
                sample
                if current == 0.0
                else (RTF_EWMA_ALPHA * sample + (1 - RTF_EWMA_ALPHA) * current)
            )
            return self._rtf_ewma

    # --- inference -----------------------------------------------------------------

    @property
    def tts(self) -> Any:
        if self._tts is None:
            raise EngineNotReadyError("model is not loaded")
        return self._tts

    def stream(
        self,
        text: str,
        voice_id: str,
        params: SynthesisParams,
        cancel: threading.Event,
    ) -> Generator[np.ndarray, None, None]:
        """Yield float32 frames at the native rate until exhausted or cancelled.

        Cancellation is cooperative and checked between frames, which is the only safe
        point: upstream has no interrupt and a frame is the smallest unit it produces.
        Closing the generator releases the ONNX session state.
        """
        frames = self.tts.synthesize_stream(
            text,
            voice=voice_id,
            max_chunk_frames=STREAM_MAX_CHUNK_FRAMES,
            cfg_scale=params.cfg_scale,
            audio_temperature=params.audio_temperature,
            audio_topk=params.audio_topk,
            audio_topp=params.audio_topp,
            audio_repetition_penalty=params.audio_repetition_penalty,
            eoa_extra_frames=params.eoa_extra_frames,
        )
        try:
            for chunk in frames:
                if cancel.is_set():
                    log.info("synthesize.cancelled")
                    return
                yield chunk
        finally:
            frames.close()

    def acquire_slot(self, timeout: float) -> bool:
        """Take the single inference slot. Callers must release it in a finally block."""
        return self._slot.acquire(timeout=timeout)

    def release_slot(self) -> None:
        self._slot.release()
