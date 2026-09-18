#!/usr/bin/env -S uv run --project worker python
"""Measure RTF and time-to-first-audio for the worker engine on this machine.

Answers PRD assumptions A1 (RTF ~0.5 with 8 threads) and A2 (two 4-thread processes
still beat real time). Writes a markdown report; `docs/reports/bench-m0.md` is the
artefact task 0.5 delivers.

Usage:
    make bench                       # both configurations, default corpus
    uv run scripts/bench.py --json   # machine-readable, for CI trend jobs
"""

from __future__ import annotations

import argparse
import json
import multiprocessing as mp
import os
import platform
import statistics
import subprocess
import sys
import threading
import time
from dataclasses import asdict, dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "worker"))

# Three lengths, matching the model card's short / medium / long buckets.
CORPUS = (
    ("short", "Xin chào các bạn nhé."),
    ("medium", "Hôm nay trời đẹp, chúng ta cùng đi dạo phố cổ Hà Nội."),
    (
        "long",
        "Công ty chúng tôi cung cấp dịch vụ tổng hợp giọng nói tiếng Việt chất lượng cao "
        "cho doanh nghiệp, với độ trễ thấp và khả năng phát trực tuyến theo thời gian thực.",
    ),
)
VOICE = "maichi"
SINGLE = "1 process x 8 threads"
PAIRED = "2 processes x 4 threads"
WARMUP_RUNS = 2  # discarded: first runs pay cold-cache costs
MEASURED_RUNS = 4
NATIVE_SAMPLE_RATE = 48_000


@dataclass(frozen=True)
class Sample:
    label: str
    chars: int
    threads: int
    processes: int
    ttfa_ms: float
    rtf: float
    audio_seconds: float


def run_engine(threads: int, processes: int, out: mp.Queue | None = None) -> list[Sample]:
    """One worker process's worth of measurements."""
    os.environ.setdefault("VITTS_MODEL_REVISION", _pinned_revision())
    os.environ["ORT_INTRA_OP_THREADS"] = str(threads)

    from vitts_worker.config import Config
    from vitts_worker.engine import Engine, SynthesisParams

    engine = Engine(Config.from_env())
    engine.load()

    samples: list[Sample] = []
    for label, text in CORPUS:
        for run in range(WARMUP_RUNS + MEASURED_RUNS):
            started = time.monotonic()
            first: float | None = None
            emitted = 0
            for chunk in engine.stream(text, VOICE, SynthesisParams(), threading.Event()):
                if first is None:
                    first = time.monotonic() - started
                emitted += chunk.reshape(-1).shape[0]
            elapsed = time.monotonic() - started
            audio_seconds = emitted / NATIVE_SAMPLE_RATE
            if run < WARMUP_RUNS:
                continue
            samples.append(
                Sample(
                    label=label,
                    chars=len(text),
                    threads=threads,
                    processes=processes,
                    ttfa_ms=round((first or 0.0) * 1000, 1),
                    rtf=round(elapsed / audio_seconds, 3),
                    audio_seconds=round(audio_seconds, 2),
                )
            )

    if out is not None:
        out.put([asdict(s) for s in samples])
    return samples


def run_parallel(threads: int, processes: int) -> list[Sample]:
    """A2: several worker processes contending for the same cores."""
    queue: mp.Queue = mp.Queue()
    workers = [
        mp.Process(target=run_engine, args=(threads, processes, queue)) for _ in range(processes)
    ]
    for worker in workers:
        worker.start()
    collected = [Sample(**row) for _ in workers for row in queue.get()]
    for worker in workers:
        worker.join()
    return collected


def summarise(samples: list[Sample]) -> dict[str, dict[str, float]]:
    by_label: dict[str, list[Sample]] = {}
    for sample in samples:
        by_label.setdefault(sample.label, []).append(sample)
    return {
        label: {
            "rtf_mean": round(statistics.mean(s.rtf for s in rows), 3),
            "rtf_max": max(s.rtf for s in rows),
            "ttfa_ms_mean": round(statistics.mean(s.ttfa_ms for s in rows), 1),
            "ttfa_ms_max": max(s.ttfa_ms for s in rows),
        }
        for label, rows in by_label.items()
    }


def machine() -> str:
    if platform.system() == "Darwin":
        cpu = subprocess.run(
            ["/usr/sbin/sysctl", "-n", "machdep.cpu.brand_string"],
            capture_output=True,
            text=True,
        ).stdout.strip()
    else:
        cpu = platform.processor() or "unknown"
    return f"{cpu}, {os.cpu_count()} cores, {platform.system()} {platform.release()}"


def report(results: dict[str, dict[str, dict[str, float]]]) -> str:
    lines = [
        "# Bench M0 — worker RTF and TTFA",
        "",
        f"Machine: {machine()}",
        f"Corpus: {len(CORPUS)} Vietnamese texts, {MEASURED_RUNS} measured runs each "
        f"({WARMUP_RUNS} warm-up runs discarded), voice `{VOICE}`, native 48 kHz.",
        "",
        "| configuration | text | RTF mean | RTF max | TTFA mean | TTFA max |",
        "|---|---|---|---|---|---|",
    ]
    for config, per_label in results.items():
        for label, stats in per_label.items():
            lines.append(
                f"| {config} | {label} | {stats['rtf_mean']}x | {stats['rtf_max']}x | "
                f"{stats['ttfa_ms_mean']} ms | {stats['ttfa_ms_max']} ms |"
            )
    lines += ["", "## PRD assumptions", "", *_verdicts(results)]
    return "\n".join(lines) + "\n"


def _verdicts(results: dict[str, dict[str, dict[str, float]]]) -> list[str]:
    """Judge A1 and A2 from the numbers, so the report cannot drift from the data."""
    single = max(s["rtf_max"] for s in results[SINGLE].values())
    paired = max(s["rtf_max"] for s in results[PAIRED].values())
    ttfa = max(s["ttfa_ms_max"] for s in results[SINGLE].values())
    return [
        f"- **A1** (RTF ≈ 0.5 with 8 threads): worst RTF {single}x — "
        f"{'holds' if single <= 0.5 else 'DOES NOT HOLD'} on this machine.",
        f"- **A2** (two 4-thread processes still beat real time): worst RTF {paired}x — "
        f"{'holds' if paired < 1.0 else 'DOES NOT HOLD'}.",
        f"- **US-01 AC-2** (first frame ≤ 150 ms): worst TTFA {ttfa} ms — "
        f"{'holds' if ttfa <= 150 else 'DOES NOT HOLD'}.",
        "",
        "Measured on the machine named above. A1 is stated for Hetzner CCX / AWS c7i;",
        "re-run `make bench` on the target host before treating it as settled there.",
    ]


def _pinned_revision() -> str:
    env_file = Path(__file__).resolve().parents[1] / ".env.example"
    for line in env_file.read_text().splitlines():
        if line.startswith("VITTS_MODEL_REVISION="):
            return line.split("=", 1)[1].strip()
    raise SystemExit("VITTS_MODEL_REVISION not found in .env.example")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true", help="print raw samples as JSON")
    parser.add_argument("--out", type=Path, help="write the markdown report here")
    args = parser.parse_args()

    results = {
        SINGLE: summarise(run_engine(threads=8, processes=1)),
        PAIRED: summarise(run_parallel(threads=4, processes=2)),
    }

    if args.json:
        print(json.dumps(results, indent=2))
    else:
        text = report(results)
        print(text)
        if args.out:
            args.out.parent.mkdir(parents=True, exist_ok=True)
            args.out.write_text(text)
            print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
