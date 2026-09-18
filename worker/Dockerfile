# Base carries uv and CPython 3.12. Digest pinning and image hardening are task 3.3;
# this image is what `make up` runs locally.
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim

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
