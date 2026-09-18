#!/usr/bin/env python3
"""Post-processes generated stubs. Run from `make generate`, never by hand.

Python rather than sed: `sed -i` takes a backup suffix on BSD/macOS and not on GNU, so a
shell one-liner here works on one developer's machine and fails in CI.
"""

from __future__ import annotations

import re
from pathlib import Path

GEN_DIR = Path(__file__).resolve().parents[1] / "worker" / "vitts_worker" / "gen"

# grpc_tools emits a top-level `import worker_pb2`, which only resolves when the
# generated directory is on sys.path. Make it a package-relative import instead.
TOP_LEVEL_IMPORT = re.compile(r"^import ([a-z_]+_pb2) as ", re.MULTILINE)

INIT_DOC = '"""Generated gRPC stubs for vitts.worker.v1. Do not edit; run `make generate`."""\n'


def main() -> int:
    for stub in sorted(GEN_DIR.glob("*_pb2_grpc.py")):
        source = stub.read_text()
        rewritten = TOP_LEVEL_IMPORT.sub(r"from . import \1 as ", source)
        if rewritten != source:
            stub.write_text(rewritten)

    (GEN_DIR / "__init__.py").write_text(INIT_DOC)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
