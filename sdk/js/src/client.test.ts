import { strict as assert } from "node:assert";
import { test } from "node:test";

import { VittsClient } from "./client.ts";
import { VittsError, VittsTransportError } from "./errors.ts";

interface Call {
  url: string;
  method: string;
  headers: Headers;
  body: string | undefined;
}

/** stub records what the client sent and replays the responses given to it. */
function stub(responses: Response[]) {
  const calls: Call[] = [];
  const queue = [...responses];
  const fetchImpl = (async (url: string | URL, init?: RequestInit) => {
    calls.push({
      url: String(url),
      method: init?.method ?? "GET",
      headers: new Headers(init?.headers),
      body: typeof init?.body === "string" ? init.body : undefined,
    });
    const next = queue.shift();
    if (!next) throw new Error("the client made more requests than the test expected");
    return next;
  }) as unknown as typeof globalThis.fetch;

  return { calls, fetchImpl };
}

function problem(status: number, code: string, detail: string, headers: HeadersInit = {}) {
  return new Response(JSON.stringify({ code, title: code, detail, status, request_id: "req-1" }), {
    status,
    headers: { "content-type": "application/problem+json", ...headers },
  });
}

function client(fetchImpl: typeof globalThis.fetch, maxRetries = 0) {
  return new VittsClient({
    apiKey: "zt_test_abc",
    baseUrl: "https://api.example.com/",
    fetch: fetchImpl,
    maxRetries,
  });
}

// T-116. The key goes in the Authorization header on every call, and never in the URL,
// where proxies and browser history would keep it.
test("every request carries the key as a bearer header", async () => {
  const { calls, fetchImpl } = stub([
    new Response(new Uint8Array([1, 2, 3]), { headers: { "content-type": "audio/wav" } }),
  ]);

  const audio = await client(fetchImpl).synthesize({ text: "Xin chào" });

  assert.equal(calls[0]?.headers.get("authorization"), "Bearer zt_test_abc");
  assert.ok(!calls[0]?.url.includes("zt_test_abc"), "the key must never reach the URL");
  assert.equal(calls[0]?.url, "https://api.example.com/v1/synthesize");
  assert.equal(audio.contentType, "audio/wav");
  assert.deepEqual([...audio.bytes], [1, 2, 3]);
});

test("a refusal arrives as VittsError with the API's own code and detail", async () => {
  const { fetchImpl } = stub([problem(402, "quota_exceeded", "used 51000 of 50000")]);

  await assert.rejects(
    () => client(fetchImpl).synthesize({ text: "x" }),
    (error: unknown) => {
      assert.ok(error instanceof VittsError);
      assert.equal(error.code, "quota_exceeded");
      assert.equal(error.status, 402);
      assert.equal(error.message, "used 51000 of 50000");
      assert.equal(error.requestId, "req-1");
      assert.equal(error.retryable, false);
      return true;
    },
  );
});

// T-117. NFR-05 says overload is answered with Retry-After. A client that ignores it is
// the reason overloaded services stay overloaded.
test("429 is retried after the interval the API asked for", async () => {
  const { calls, fetchImpl } = stub([
    problem(429, "rate_limited", "slow down", { "retry-after": "0" }),
    new Response(new Uint8Array([9]), { headers: { "content-type": "audio/wav" } }),
  ]);

  const audio = await client(fetchImpl, 2).synthesize({ text: "x" });

  assert.equal(calls.length, 2, "the request is retried once");
  assert.deepEqual([...audio.bytes], [9]);
});

test("retries are bounded and the last error is what the caller sees", async () => {
  const { calls, fetchImpl } = stub([
    problem(503, "overloaded", "no capacity", { "retry-after": "0" }),
    problem(503, "overloaded", "no capacity", { "retry-after": "0" }),
    problem(503, "overloaded", "still no capacity", { "retry-after": "0" }),
  ]);

  await assert.rejects(
    () => client(fetchImpl, 2).synthesize({ text: "x" }),
    (error: unknown) => {
      assert.ok(error instanceof VittsError);
      assert.equal(error.message, "still no capacity");
      return true;
    },
  );
  assert.equal(calls.length, 3, "one attempt plus two retries");
});

test("a 4xx that is not 429 is never retried", async () => {
  const { calls, fetchImpl } = stub([problem(422, "unknown_voice", "no such voice: bob")]);

  await assert.rejects(() => client(fetchImpl, 3).synthesize({ text: "x", voice: "bob" }));
  assert.equal(calls.length, 1);
});

test("a non-problem body still produces a usable error", async () => {
  const { fetchImpl } = stub([
    new Response("<html>502 Bad Gateway</html>", { status: 502, headers: { "content-type": "text/html" } }),
  ]);

  await assert.rejects(
    () => client(fetchImpl).synthesize({ text: "x" }),
    (error: unknown) => {
      assert.ok(error instanceof VittsError);
      assert.equal(error.status, 502);
      assert.match(error.message, /502/);
      return true;
    },
  );
});

test("an unreachable API is a transport error, not a protocol error", async () => {
  const fetchImpl = (() => Promise.reject(new TypeError("fetch failed"))) as unknown as typeof globalThis.fetch;

  await assert.rejects(
    () => client(fetchImpl).synthesize({ text: "x" }),
    (error: unknown) => error instanceof VittsTransportError,
  );
});

// T-118. Ordering is the contract (NFR-06): a stream that reorders chunks is silently
// corrupt audio.
test("stream yields chunks in the order they arrive", async () => {
  const chunks = [new Uint8Array([1]), new Uint8Array([2]), new Uint8Array([3])];
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(chunk);
      controller.close();
    },
  });
  const { fetchImpl } = stub([new Response(body, { headers: { "content-type": "audio/wav" } })]);

  const seen: number[] = [];
  for await (const chunk of client(fetchImpl).stream({ text: "Xin chào" })) {
    seen.push(...chunk);
  }

  assert.deepEqual(seen, [1, 2, 3]);
});

test("breaking out of a stream cancels the body instead of leaking the socket", async () => {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array([1]));
      controller.enqueue(new Uint8Array([2]));
    },
    cancel() {
      cancelled = true;
    },
  });
  const { fetchImpl } = stub([new Response(body)]);

  for await (const _chunk of client(fetchImpl).stream({ text: "x" })) {
    break;
  }

  assert.equal(cancelled, true);
});

// T-119. US-14: a retried create must not produce a second job.
test("createJob always sends an Idempotency-Key, generating one when not given", async () => {
  const job = { id: "job-1", status: "queued" };
  const { calls, fetchImpl } = stub([
    new Response(JSON.stringify(job), { status: 202, headers: { "content-type": "application/json" } }),
    new Response(JSON.stringify(job), { status: 202, headers: { "content-type": "application/json" } }),
  ]);
  const sdk = client(fetchImpl);

  await sdk.createJob({ text: "long text" });
  await sdk.createJob({ text: "long text" }, "my-key");

  const generated = calls[0]?.headers.get("idempotency-key");
  assert.ok(generated && generated.length > 10, "a key is generated when the caller omits one");
  assert.equal(calls[1]?.headers.get("idempotency-key"), "my-key");
});

test("waitForJob polls until the job is terminal", async () => {
  const running = JSON.stringify({ id: "job-1", status: "synthesizing" });
  const done = JSON.stringify({ id: "job-1", status: "completed", output_url: "https://x/a.mp3" });
  const { calls, fetchImpl } = stub([
    new Response(running, { headers: { "content-type": "application/json" } }),
    new Response(running, { headers: { "content-type": "application/json" } }),
    new Response(done, { headers: { "content-type": "application/json" } }),
  ]);

  const job = await client(fetchImpl).waitForJob("job-1", { pollMs: 1 });

  assert.equal(job.status, "completed");
  assert.equal(calls.length, 3);
});

test("waitForJob gives up rather than polling forever", async () => {
  const running = JSON.stringify({ id: "job-1", status: "queued" });
  const { fetchImpl } = stub(
    Array.from({ length: 10 }, () => new Response(running, { headers: { "content-type": "application/json" } })),
  );

  await assert.rejects(
    () => client(fetchImpl).waitForJob("job-1", { pollMs: 5, timeoutMs: 12 }),
    /still queued after the timeout/,
  );
});

test("an empty apiKey is refused at construction, not at the first call", () => {
  assert.throws(() => new VittsClient({ apiKey: "" }), /apiKey is required/);
});
