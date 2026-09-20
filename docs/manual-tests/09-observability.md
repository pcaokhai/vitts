# 9. Observability

Metrics, dashboards and alerts, checked against a running stack.

## Observability

| Where | Check |
|-------|-------|
| `curl -s $GATEWAY/metrics \| grep -c '^vitts\|^http_\|^tts_'` | Metrics are being emitted |
| `$GATEWAY/metrics` | `tts_ttfa_seconds`, `dispatch_queue_depth`, `cache_total`, `usage_chars_total`, `worker_slots_busy` all present |
| Prometheus `:9090` → Status → Targets | `gateway` is UP |
| Prometheus → Alerts | 10 rules loaded, none firing on a healthy stack |
| Grafana `:3000` (admin/admin) | The ViTTS overview dashboard has data in every panel |

Then make an alert *nearly* fire, so you know the wiring is real: stop the worker and
watch `worker_ready` drop to 0 and `NoReadyWorker` go pending in Prometheus.

---

---

[← Console and SDKs](08-console-and-sdks.md) · [Index](README.md) · [Resilience drills →](10-resilience.md)
