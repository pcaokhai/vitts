"""Worker entrypoint: load config, serve, load the model, shut down cleanly.

The server starts before the model so the gateway can poll Health and see `ready=false`
instead of a connection refused while weights load (US-02).
"""

from __future__ import annotations

import asyncio
import signal
import sys

import structlog

from .config import Config, ConfigError
from .engine import Engine
from .server import serve

log = structlog.get_logger(__name__)
SHUTDOWN_GRACE_SECONDS = 10.0


async def main() -> int:
    structlog.configure(processors=[structlog.processors.JSONRenderer()])

    try:
        cfg = Config.from_env()
    except ConfigError as exc:
        print(f"configuration error: {exc}", file=sys.stderr)
        return 2

    engine = Engine(cfg)
    server, _port = await serve(engine, cfg, cfg.grpc_addr)

    # Loading blocks on ONNX session creation and a warm-up utterance; keep it off the
    # event loop so Health stays answerable throughout.
    load = asyncio.create_task(asyncio.to_thread(engine.load))

    stopping = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stopping.set)

    done, _ = await asyncio.wait(
        [load, asyncio.create_task(stopping.wait())],
        return_when=asyncio.FIRST_COMPLETED,
    )
    if load in done and load.exception() is not None:
        log.error("engine.load_failed", error=str(load.exception()))
        await server.stop(0)
        return 1

    if not stopping.is_set():
        await stopping.wait()

    log.info("shutdown.begin")
    await server.stop(SHUTDOWN_GRACE_SECONDS)
    if not load.done():
        load.cancel()
    log.info("shutdown.done")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
