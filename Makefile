# ViTTS Gateway. Targets are the ones documented in CLAUDE.md § Commands.
# A target whose owning task has not landed yet reports what it is waiting on and
# exits 0, so CI is green on a scaffold-only tree (task 0.1). Once the guarded path
# exists the real command runs and its failure fails the target.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.PHONY: help setup generate lint test test-model test-integration migrate seed up down smoke bench loadtest

# --env-file: .env.example is the single source of the pinned revision and the local
# dev credentials, so the compose file never repeats them.
COMPOSE := docker compose --env-file .env.example -f deploy/docker-compose.yml

# The gateway has no cgo dependency and ships as a static binary, so tests link the same
# way. It also sidesteps a broken system linker on some macOS SDK versions.
GOTEST := CGO_ENABLED=0 go test

# $(call pending,<task id>,<what>) — printed when the owning task has not landed.
pending = echo "  ..  $(2) — lands in task $(1)"

help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

setup: ## Install the toolchain (go, uv, buf, oapi-codegen, sqlc, golangci-lint, k6)
	@if [ -x scripts/setup.sh ]; then ./scripts/setup.sh; else $(call pending,0.2,toolchain bootstrap); fi

generate: ## Regenerate proto, OpenAPI and sqlc code (CI fails on diff)
	@if [ -f proto/buf.gen.yaml ]; then cd proto && buf generate && python3 ../scripts/postgen.py; else $(call pending,0.2,proto stubs); fi
	@if [ -f gateway/sqlc.yaml ]; then cd gateway && sqlc generate; else $(call pending,1.2,sqlc); fi
	@cp docs/api/openapi.yaml gateway/api/openapi.yaml

lint: ## golangci-lint, ruff, mypy, buf lint
	@if command -v gitleaks >/dev/null; then gitleaks dir . --no-banner --redact; else $(call pending,0.2,gitleaks); fi
	@if [ -f proto/buf.yaml ]; then buf lint proto; else $(call pending,0.2,buf lint); fi
	@if [ -f gateway/go.mod ]; then cd gateway && golangci-lint run ./...; else $(call pending,1.1,golangci-lint); fi
	@if [ -f worker/pyproject.toml ]; then cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy .; else $(call pending,0.3,ruff + mypy); fi

test: ## Unit tests, both languages
	@if [ -f gateway/go.mod ]; then cd gateway && $(GOTEST) ./...; else $(call pending,1.1,go test); fi
	@if [ -f worker/pyproject.toml ]; then cd worker && uv run pytest; else $(call pending,0.3,pytest); fi

# Same code path as the deploy's migrations job: the binary carries the migrations.
migrate: ## Apply database migrations to VITTS_DATABASE_URL
	@if [ -f gateway/go.mod ]; then \
		cd gateway && CGO_ENABLED=0 go run ./cmd/gateway -migrate; \
	else $(call pending,1.2,migrations); fi

# Plan tiers are configuration, not migration data: config/plans.yaml is the file an
# operator edits, and re-running this is how a limit changes.
seed: ## Upsert plan tiers from config/plans.yaml; needs `make migrate` first
	@if [ -f config/plans.yaml ]; then \
		cd gateway && CGO_ENABLED=0 go run ./cmd/gateway -seed-plans ../config/plans.yaml; \
	else $(call pending,1.4,plan config); fi

test-model: ## Worker tests that load the real ZeroTTS weights (~200 MB download)
	@if [ -f worker/pyproject.toml ]; then cd worker && uv run pytest -m model; else $(call pending,0.3,model tests); fi

test-integration: ## Testcontainers (Postgres, Redis, MinIO) + fake worker
	@if [ -f gateway/go.mod ]; then cd gateway && $(GOTEST) -tags=integration ./...; else $(call pending,1.2,integration tests); fi

up: ## Start the local stack
	@if [ -f deploy/docker-compose.yml ]; then $(COMPOSE) up -d; else $(call pending,0.6,local stack); fi

down: ## Stop the local stack and drop volumes
	@if [ -f deploy/docker-compose.yml ]; then $(COMPOSE) down -v; else $(call pending,0.6,local stack); fi

smoke: ## End-to-end: health, sync, stream, job
	@if [ -x scripts/smoke.sh ]; then ./scripts/smoke.sh; else $(call pending,0.6,smoke script); fi

bench: ## Worker RTF/TTFA on this machine
	@if [ -f scripts/bench.py ]; then cd worker && uv run ../scripts/bench.py --out ../docs/reports/bench-m0.md; else $(call pending,0.5,bench script); fi

# VITTS_KEY must be a live tenant key; VITTS_SCENARIO picks steady, spike or cache.
loadtest: ## Run a k6 scenario (VITTS_SCENARIO=steady|spike|cache, VITTS_KEY required)
	@if [ -d scripts/loadtest ]; then \
		k6 run scripts/loadtest/$${VITTS_SCENARIO:-steady}.js; \
	else $(call pending,3.2,k6 scenarios); fi
