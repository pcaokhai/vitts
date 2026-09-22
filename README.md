<div align="center">
  <h1>ViTTS Gateway</h1>
  <p>A hosted, multi-tenant Vietnamese text-to-speech API built on the open <a href="https://github.com/">ZeroTTS</a> model.</p>
  <p>
    <a href="https://github.com/vitts-project/vitts/actions"><img src="https://img.shields.io/github/actions/workflow/status/vitts-project/vitts/ci.yml?style=flat-square" alt="Build Status"></a>
    <a href="https://github.com/vitts-project/vitts/blob/main/LICENSE"><img src="https://img.shields.io/github/license/vitts-project/vitts?style=flat-square" alt="License"></a>
    <a href="https://pkg.go.dev/github.com/vitts-project/vitts"><img src="https://pkg.go.dev/badge/github.com/vitts-project/vitts.svg" alt="Go Reference"></a>
  </p>
</div>

---

**ViTTS** is a high-performance API gateway and inference worker ecosystem designed to serve Vietnamese Text-to-Speech at scale. By leveraging the open ZeroTTS model, it provides a sub-300ms streaming experience with per-character pricing and robust multi-tenancy capabilities—all without requiring a GPU.

## ✨ Key Features

### 🎙️ Core Synthesis API
- **Multiple Formats:** Sync requests support `wav`, `mp3`, and `ogg` outputs.
- **Low-latency Streaming:** HTTP chunked streaming and WebSocket support with cooperative/client-initiated cancellation.
- **Voice Catalog:** 8 built-in preset voices readily available.
- **Smart Caching:** Audio cached (Redis index + S3/MinIO) using request fingerprinting for instant repeated synthesis.
- **Resilient Inference:** Worker client pool featuring health polling, least-busy routing, and circuit breaking.

### 🏢 Multi-tenant & Management
- **API Key Authentication:** Secure, sha256-hashed keys with environment prefixes (`zt_live_`, `zt_test_`).
- **Quota & Rate Limiting:** Built-in per-tenant concurrency leases and usage quota enforcement.
- **Self-service Console:** Minimal Next.js admin console for key management, usage charts, and documentation.
- **Detailed Reporting:** Usage analytics accessible via JSON and CSV.

### 🚀 Async Jobs & Pipelines
- **Long-form Synthesis:** Async job API (create, get, list, cancel) for processing extensive text using segment fan-out and merge.
- **Robust Orchestration:** Redis Streams consumer with a state machine, automatic retries, and reconciliation.
- **Webhooks:** Secure delivery with HMAC signing and SSRF protection.

### 🛡️ Production Ready
- **Observability:** Comprehensive Prometheus metrics, Grafana dashboards, and alerting rules.
- **Hardened Deployment:** Non-root/digest-pinned images, env-store secrets, and integrated security scans (`govulncheck`, `pip-audit`, `gitleaks`).
- **Thoroughly Tested:** Extensive k6 load scenarios (steady, spike, cache) and documented runbooks for operational drills.

## 🏗️ Architecture

The system is separated into highly cohesive services:

```text
├── gateway/   # Go: High-performance API gateway (auth, ratelimit, cache, dispatch)
├── worker/    # Python: Inference workers (ZeroTTS engine, preprocessing, encode)
├── console/   # Next.js: Admin dashboard for tenants
├── sdk/       # Python & JS: Auto-generated client libraries
├── proto/     # gRPC: Contract between gateway and workers
├── docs/      # Documentation: PRD, architecture, API spec, flows
├── deploy/    # Infrastructure: Docker Compose, Prometheus, Grafana
└── scripts/   # Utilities: Benchmarks, load tests, seed data
```

## 🚀 Getting Started

### Prerequisites
Make sure you have the required toolchain installed: Go, uv, buf, oapi-codegen, sqlc, golangci-lint, k6.

### Local Development

Bring up the local stack (Gateway, Worker, Postgres, Redis, MinIO):

```bash
make setup
make up
make smoke # Run end-to-end health, sync, stream, and job tests
```

*For more details, see [System Architecture](docs/02-architecture.md) and [Operations Runbook](docs/13-runbook.md).*

## 📖 Documentation

Dive deeper into the system's design and usage:
- **Product:** [PRD](docs/01-prd.md) | [User Stories](docs/09-user-stories.md)
- **Architecture:** [Design](docs/02-architecture.md) | [ADRs](docs/adr/) | [Data Model](docs/05-data-model.md) | [Flows](docs/06-flows.md)
- **API Contracts:** [OpenAPI Spec](docs/api/openapi.yaml) | [gRPC Contract](proto/worker.proto)
- **Quality & Ops:** [Tests](docs/10-tests.md) | [Load Reports](docs/reports/) | [Runbook](docs/13-runbook.md)
- **Client SDKs:** [Python & JS Guides](sdk/README.md)

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
