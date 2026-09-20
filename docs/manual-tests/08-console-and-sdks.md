# 8. Console and SDKs

The two things a customer actually touches.

## Console

Open `http://127.0.0.1:3001`.

| Step | Pass |
|------|------|
| Paste a bad key | A message about the key, from the API, not a generic error |
| Paste `$KEY` | Lands on Usage |
| Usage chart | One bar per day across the range; quiet days are faint marks, not gaps |
| Range 7/30/90 | Figures and chart change together |
| `usage.csv` | Downloads a real CSV, not a 401 document |
| Keys → create | Secret shown once, in the red panel; list refreshes |
| Keys → revoke | Row disappears; the count drops |
| Forget | Returns to the gate |
| Close the tab, reopen | Gate again — the key lives in `sessionStorage` only |
| `/docs` | Readable **without** a key; "Sign in" in the corner |
| Phone width | Table collapses to stacked rows, no horizontal scroll |

Keep the browser console open throughout. **Pass:** no errors at any step.

---

## SDKs

```bash
cd sdk/python && uv sync && cd ../..
cd sdk/js && npm ci && cd ../..
```

Python:

```bash
cd sdk/python && VITTS_KEY="$KEY" GATEWAY="$GATEWAY" uv run python - <<'PY'
import os
from vitts import JobCreateRequest, SynthesizeRequest, VittsClient, job_status

with VittsClient(os.environ["VITTS_KEY"], os.environ["GATEWAY"]) as c:
    print("voices:", len(c.list_voices()))
    print("sync:  ", len(c.synthesize(SynthesizeRequest(text="Xin chào.")).data), "bytes")
    print("stream:", sum(len(x) for x in c.stream(SynthesizeRequest(text="Xin chào."))), "bytes")
    job = c.create_job(JobCreateRequest(text="Một đoạn dài hơn cho công việc nền."))
    print("job:   ", job_status(c.wait_for_job(str(job.id), poll=2, timeout=300)))
PY
cd ../..
```

JavaScript:

```bash
cd sdk/js && VITTS_KEY="$KEY" GATEWAY="$GATEWAY" node --experimental-strip-types - <<'TS'
import { VittsClient } from "./src/index.ts";
const c = new VittsClient({ apiKey: process.env.VITTS_KEY!, baseUrl: process.env.GATEWAY! });
console.log("voices:", (await c.listVoices()).length);
console.log("sync:  ", (await c.synthesize({ text: "Xin chào." })).bytes.length, "bytes");
let n = 0; for await (const ch of c.stream({ text: "Xin chào." })) n += ch.length;
console.log("stream:", n, "bytes");
const job = await c.createJob({ text: "Một đoạn dài hơn cho công việc nền." });
console.log("job:   ", (await c.waitForJob(job.id!, { pollMs: 2000 })).status);
TS
cd ../..
```

**Pass:** both print voices, byte counts and `completed`.

---

---

[← Isolation, keys and admin](07-isolation-and-keys.md) · [Index](README.md) · [Observability →](09-observability.md)
