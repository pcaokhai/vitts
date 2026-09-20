#!/usr/bin/env bash
# Installs the toolchain listed in CLAUDE.md § Commands. Idempotent.
# Versions are pinned (docs/14-engineering-standards.md § Security: dependencies pinned).
set -euo pipefail

BUF_VERSION=1.47.2
PROTOC_GEN_GO_VERSION=v1.35.2
PROTOC_GEN_GO_GRPC_VERSION=v1.5.1
GOLANGCI_LINT_VERSION=v2.6.1
GITLEAKS_VERSION=v8.21.2
GOVULNCHECK_VERSION=latest
GOOSE_VERSION=v3.22.1
SQLC_VERSION=v1.27.0

need() { command -v "$1" >/dev/null 2>&1; }
gobin="$(go env GOPATH)/bin"

echo "==> go plugins"
need protoc-gen-go      || go install google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}
need protoc-gen-go-grpc || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}

echo "==> buf"
need buf || go install github.com/bufbuild/buf/cmd/buf@v${BUF_VERSION}

echo "==> golangci-lint"
# CGO off: golangci-lint fails to link against some macOS SDK versions.
need golangci-lint || CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}

echo "==> goose and sqlc"
need goose || CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}
need sqlc  || CGO_ENABLED=0 go install github.com/sqlc-dev/sqlc/cmd/sqlc@${SQLC_VERSION}

echo "==> gitleaks"
need gitleaks || CGO_ENABLED=0 go install github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}

echo "==> govulncheck"
need govulncheck || CGO_ENABLED=0 go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}

echo "==> python (worker deps come from worker/pyproject.toml once 0.3 lands)"
need uv || { echo "install uv: https://docs.astral.sh/uv/getting-started/installation/"; exit 1; }

case ":$PATH:" in
  *":$gobin:"*) ;;
  *) echo "NOTE: add $gobin to PATH" ;;
esac
echo "==> done"
