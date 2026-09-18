"""One streamed synthesis against a running worker, checked for the framing contract.

Called by `scripts/smoke.sh`; not a test, so it talks to a real deployed worker rather
than the in-process fixtures used by `make test`.
"""

from __future__ import annotations

import os
import sys
import time

import grpc

from vitts_worker.gen import worker_pb2, worker_pb2_grpc

TEXT = "Xin chào, đây là bản kiểm tra khói của ViTTS Gateway."
DEADLINE_SECONDS = 60.0


def main() -> int:
    addr = os.environ.get("WORKER_ADDR", "127.0.0.1:50051")
    with grpc.insecure_channel(addr) as channel:
        stub = worker_pb2_grpc.WorkerStub(channel)
        voice = stub.Health(worker_pb2.HealthRequest(), timeout=5).voice_ids[0]

        started = time.monotonic()
        first: float | None = None
        samples = 0
        seqs: list[int] = []
        last_seen = False
        for frame in stub.Synthesize(
            worker_pb2.SynthesizeRequest(request_id="smoke", text=TEXT, voice_id=voice),
            timeout=DEADLINE_SECONDS,
        ):
            if first is None:
                first = time.monotonic() - started
            seqs.append(frame.seq)
            samples += len(frame.pcm16) // 2
            last_seen = frame.last

    if seqs != list(range(len(seqs))):
        print(f"FAIL: frames out of order: {seqs[:10]}...", file=sys.stderr)
        return 1
    if not last_seen:
        print("FAIL: stream ended without a last=true frame", file=sys.stderr)
        return 1
    if samples == 0:
        print("FAIL: no audio produced", file=sys.stderr)
        return 1

    audio_seconds = samples / 48_000
    elapsed = time.monotonic() - started
    print(
        f"  voice={voice} frames={len(seqs)} audio={audio_seconds:.2f}s "
        f"ttfa={(first or 0) * 1000:.0f}ms rtf={elapsed / audio_seconds:.2f}x"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
