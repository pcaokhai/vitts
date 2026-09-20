// Content for the docs page. Kept as data so the page stays layout and the samples stay
// reviewable — these are the first thing a developer copies, so they must actually run.

export interface Sample {
  id: string;
  label: string;
  code: string;
  streamCode: string;
}

const BASE = "https://api.vitts.dev";

export const QUICKSTART: Sample[] = [
  {
    id: "curl",
    label: "curl",
    code: `curl -X POST ${BASE}/v1/synthesize \\
  -H "Authorization: Bearer $VITTS_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"text":"Xin chào, đây là ViTTS.","voice":"maichi","format":"wav"}' \\
  -o hello.wav`,
    streamCode: `curl -N -X POST ${BASE}/v1/synthesize/stream \\
  -H "Authorization: Bearer $VITTS_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"text":"Xin chào, đây là ViTTS.","voice":"maichi"}' \\
  -o stream.wav`,
  },
  {
    id: "python",
    label: "Python",
    code: `from vitts import SynthesizeRequest, VittsClient

with VittsClient(api_key, "${BASE}") as client:
    audio = client.synthesize(
        SynthesizeRequest(text="Xin chào, đây là ViTTS.", voice="maichi")
    )
    open("hello.wav", "wb").write(audio.data)`,
    streamCode: `from vitts import SynthesizeRequest, VittsClient

with VittsClient(api_key, "${BASE}") as client, open("stream.wav", "wb") as out:
    request = SynthesizeRequest(text="Xin chào, đây là ViTTS.", voice="maichi")
    for chunk in client.stream(request):
        out.write(chunk)  # play it here instead, to hear the first audio sooner`,
  },
  {
    id: "javascript",
    label: "JavaScript",
    code: `import { writeFile } from "node:fs/promises";
import { VittsClient } from "@vitts/sdk";

const client = new VittsClient({ apiKey, baseUrl: "${BASE}" });

const audio = await client.synthesize({
  text: "Xin chào, đây là ViTTS.",
  voice: "maichi",
});
await writeFile("hello.wav", audio.bytes);`,
    streamCode: `import { createWriteStream } from "node:fs";
import { VittsClient } from "@vitts/sdk";

const client = new VittsClient({ apiKey, baseUrl: "${BASE}" });
const out = createWriteStream("stream.wav");

for await (const chunk of client.stream({ text: "Xin chào, đây là ViTTS." })) {
  out.write(chunk); // play it here instead, to hear the first audio sooner
}
out.end();`,
  },
];

export interface Endpoint {
  method: string;
  path: string;
  scope: string;
  summary: string;
}

export const ENDPOINTS: Endpoint[] = [
  {
    method: "POST",
    path: "/v1/synthesize",
    scope: "synth",
    summary: "Up to 3,000 characters, returned as one audio file.",
  },
  {
    method: "POST",
    path: "/v1/synthesize/stream",
    scope: "synth",
    summary: "The same, streamed as it is produced. Also speaks WebSocket.",
  },
  {
    method: "GET",
    path: "/v1/voices",
    scope: "none",
    summary: "The catalogue. Readable without a key; a key adds your own voices.",
  },
  {
    method: "POST",
    path: "/v1/jobs",
    scope: "jobs",
    summary: "Up to 100,000 characters. Needs an Idempotency-Key. Returns 202.",
  },
  {
    method: "GET",
    path: "/v1/jobs/{id}",
    scope: "jobs",
    summary: "Progress, and a signed output URL once it completes.",
  },
  {
    method: "DELETE",
    path: "/v1/jobs/{id}",
    scope: "jobs",
    summary: "Cancel. Segments already synthesized are still billed.",
  },
  {
    method: "GET",
    path: "/v1/usage",
    scope: "usage",
    summary: "Daily characters, audio and cache hits. JSON or CSV.",
  },
  {
    method: "GET",
    path: "/v1/keys",
    scope: "keys",
    summary: "Your live keys, by prefix. The secret is shown only at creation.",
  },
];
