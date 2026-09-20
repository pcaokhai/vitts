# Worker image. The base carries uv and CPython 3.12, pinned by digest so a rebuild
# months from now produces the same image rather than whatever the tag points at then.
#
# Unlike the gateway this cannot be distroless: onnxruntime and libsndfile need a libc
# and the model needs a writable cache. It runs as a non-root user instead, with the
# model directory as the only path it may write.
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim@sha256:e5b65587bce7de595f299855d7385fe7fca39b8a74baa261ba1b7147afa78e58

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PROJECT_ENVIRONMENT=/app/.venv \
    VITTS_MODEL_DIR=/var/lib/vitts/model

WORKDIR /app

# Dependencies first: a code change must not re-resolve the environment.
COPY worker/pyproject.toml worker/uv.lock ./
RUN uv sync --frozen --no-install-project --no-dev

COPY worker/vitts_worker ./vitts_worker

RUN useradd --system --uid 10001 --create-home vitts \
    && mkdir -p /var/lib/vitts/model \
    && chown -R vitts:vitts /var/lib/vitts /app
USER vitts

ENV PATH="/app/.venv/bin:$PATH"
EXPOSE 50051
ENTRYPOINT ["python", "-m", "vitts_worker"]
