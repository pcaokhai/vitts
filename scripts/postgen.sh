#!/usr/bin/env bash
# Post-processes generated stubs. Run from make generate, never by hand.
set -euo pipefail
cd "$(dirname "$0")/.."

gen=worker/vitts_worker/gen

# grpc_tools emits a top-level `import worker_pb2`, which only resolves when the
# generated directory is on sys.path. Make it a package-relative import instead.
sed -i '' -E 's/^import ([a-z_]+_pb2) as /from . import \1 as /' "$gen"/*_pb2_grpc.py

cat > "$gen/__init__.py" <<'PY'
"""Generated gRPC stubs for vitts.worker.v1. Do not edit; run `make generate`."""
PY
