# 12. Gaps and teardown

What this guide cannot cover, and how to stop the stack.

## Known gaps

Things this guide cannot check, and why:

| Gap | Why |
|-----|-----|
| Webhook delivery | Every local receiver is on a private address and the SSRF guard correctly refuses it. Needs a public HTTPS endpoint, e.g. a tunnel. |
| Quota exhaustion | Burning a plan's full allowance takes real traffic; the counter and the report are checked instead. |
| Model upgrade | Needs a second pinned `VITTS_MODEL_REVISION` to mean anything. |
| Real pricing | `config/plans.yaml` is still `TODO(pricing)` placeholders. |

## Tear down

```bash
make down    # also drops volumes: model weights re-download next time
```

To keep the weights, stop without `-v`:

```bash
docker compose --env-file .env.example -f deploy/docker-compose.yml down
```

---

[← Automated suites](11-automated-suites.md) · [Index](README.md)
