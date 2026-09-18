---
paths: ["worker/**/*.py"]
---
# Python worker rules

- Wrap `zerotts`; never copy or modify its model code (ADR-002). Pin its version in
  `pyproject.toml` and `uv.lock`.
- One in-flight inference per process (`asyncio.Lock` around the engine). Report
  `slots_total=1`, `slots_busy` accurately. Do not add intra-process concurrency (ADR-003).
- Check `context.cancelled()` / `context.is_active()` between every emitted frame; stop the
  generator and free resources on cancel. Increment `worker_orphan_inference_total` if an
  inference outlives its call.
- `Synthesize` frames: PCM16 little-endian mono, `seq` from 0, `last=true` exactly once,
  `chars_consumed` monotonic.
- Preprocess only via `zerotts.normalize_vi_text`, `zerotts.chunking.*`.
- Encoding via `soundfile` for WAV/OGG; MP3 via `ffmpeg` subprocess with timeouts; resample
  via `scipy.signal.resample_poly`.
- Config from env only (`VITTS_*`, `ORT_INTRA_OP_THREADS`); validate at startup; fail fast.
- Logging via `structlog` JSON; never log text; log `request_id`, durations, RTF.
- Type hints everywhere; `mypy --strict` clean; `ruff` clean.
- Tests with `pytest`; use a `FakeEngine` yielding deterministic frames so tests never
  download weights. Real-model tests are marked `@pytest.mark.model` and skipped in CI.
