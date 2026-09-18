"""Container health probe: exit 0 only when the worker reports `ready`.

Run as `python -m vitts_worker.healthcheck`. A worker that is listening but still
loading weights must fail this probe, otherwise the gateway would route to it.
"""

from __future__ import annotations

import sys

import grpc

from .gen import worker_pb2, worker_pb2_grpc

TIMEOUT_SECONDS = 3.0


def main(addr: str = "127.0.0.1:50051") -> int:
    try:
        with grpc.insecure_channel(addr) as channel:
            reply = worker_pb2_grpc.WorkerStub(channel).Health(
                worker_pb2.HealthRequest(), timeout=TIMEOUT_SECONDS
            )
    except grpc.RpcError as exc:
        print(f"unhealthy: {exc.code().name}", file=sys.stderr)
        return 1

    if not reply.ready:
        print("unhealthy: model not loaded", file=sys.stderr)
        return 1
    print(f"ready model={reply.model_version} slots={reply.slots_busy}/{reply.slots_total}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1:50051"))
