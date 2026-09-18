# ADR-002: Worker stays in Python on onnxruntime

Status: Accepted

## Context
ZeroTTS ships as a Python package with ONNX graphs, text normalizer and chunker.
Re-implementing normalization or the streaming loop in another language risks drift from
upstream quality numbers (1.03% WER measured with upstream defaults).

## Decision
We will wrap the upstream package in a thin Python gRPC service and never fork model
logic. Preprocessing (normalize, chunk) lives in the worker so it reuses upstream code.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Port to Go/Rust ONNX runtime | Single language | Weeks of work, tokenizer + normalizer parity risk |
| Call `zerotts` CLI per request | Zero code | Process spawn per call, no streaming, no warm session |

## Consequences
Two languages in the repo (accepted, see ADR-001). Python must be pinned (`uv.lock`),
run under a process supervisor, and expose health so the gateway can route around a
crashed process.
