type Transport = "direct" | "relay";

interface InitResponse {
  upload_id: string;
  mode: "s3_multipart" | "s3_relay";
  relay_supported: boolean;
  part_size: number;
  max_parallel: number;
  max_parts: number;
}

interface SignedPart {
  url: string;
  headers?: Record<string, string>;
  size: number;
}

interface PartTiming {
  number: number;
  bytes: number;
  signMs: number;
  putMs: number;
}

const baseURL = required("APTEVA_BASE_URL").replace(/\/$/, "");
const apiKey = required("APTEVA_API_KEY");
const projectID = required("APTEVA_PROJECT_ID");
const installID = process.env.APTEVA_INSTALL_ID || "16";
const sizeMiB = positiveNumber("BENCHMARK_MIB", 256);
const requestedConcurrency = positiveInteger("BENCHMARK_CONCURRENCY", 4);
const order = parseOrder(process.env.BENCHMARK_ORDER || "direct,relay");
const totalBytes = Math.floor(sizeMiB * 1024 * 1024);
const api = `${baseURL}/api/apps/storage`;
const scope = new URLSearchParams({ project_id: projectID, install_id: installID });
const authHeaders = { Authorization: `Bearer ${apiKey}` };

console.log(`Storage upload-path benchmark`);
console.log(`Target: ${baseURL} (install ${installID})`);
console.log(`Payload: ${formatMiB(totalBytes)} MiB; order: ${order.join(" -> ")}`);
console.log("Each run is aborted after verification, so no file record or completed object remains.\n");

const results: Array<{ transport: Transport; elapsedMs: number; timings: PartTiming[] }> = [];
for (const transport of order) {
  results.push(await run(transport));
}

console.log("\nSummary");
console.log("transport  elapsed  throughput   PUT p50  PUT p95  sign total");
for (const result of results) {
  const putTimes = result.timings.map((part) => part.putMs).sort((a, b) => a - b);
  const signTotal = result.timings.reduce((sum, part) => sum + part.signMs, 0);
  console.log(
    `${result.transport.padEnd(9)}  ${formatSeconds(result.elapsedMs).padStart(7)}  ` +
      `${formatRate(totalBytes, result.elapsedMs).padStart(10)}  ` +
      `${formatSeconds(percentile(putTimes, 0.5)).padStart(7)}  ` +
      `${formatSeconds(percentile(putTimes, 0.95)).padStart(7)}  ` +
      `${formatSeconds(signTotal).padStart(10)}`,
  );
}

async function run(transport: Transport) {
  const init = await json<InitResponse>(`${api}/uploads?${scope}`, {
    method: "POST",
    headers: { ...authHeaders, "Content-Type": "application/json", Origin: baseURL },
    body: JSON.stringify({
      filename: `upload-path-benchmark-${transport}-${Date.now()}.bin`,
      size: totalBytes,
      content_type: "application/octet-stream",
      folder: "/.diagnostics/",
      visibility: "private",
      source: "upload-path-benchmark",
      direct: true,
    }),
  });

  if (!init.upload_id) throw new Error(`${transport}: init returned no upload_id`);
  if (transport === "direct" && init.mode !== "s3_multipart") {
    await abort(init.upload_id);
    throw new Error(`direct: server selected ${init.mode}; direct S3 is unavailable`);
  }
  if (transport === "relay" && !init.relay_supported) {
    await abort(init.upload_id);
    throw new Error("relay: server does not advertise multipart relay support");
  }

  const partSize = init.part_size;
  const partCount = Math.ceil(totalBytes / partSize);
  const concurrency = Math.min(8, init.max_parallel, requestedConcurrency);
  if (partCount > init.max_parts) {
    await abort(init.upload_id);
    throw new Error(`payload requires ${partCount} parts; server limit is ${init.max_parts}`);
  }

  // Allocate one reusable payload per worker before starting the clock. This
  // keeps a multi-gigabyte benchmark bounded to concurrency * part_size RAM.
  // A worker does not reuse its buffer until fetch has consumed the prior PUT.
  // Repeated bytes are not compressed by fetch.
  const partLengths = Array.from({ length: partCount }, (_, index) =>
    Math.min(partSize, totalBytes - index * partSize),
  );
  const payloadPool = Array.from({ length: concurrency }, (_, index) => {
    const payload = new Uint8Array(partSize);
    payload.fill((index * 31 + 17) & 0xff);
    return payload;
  });

  console.log(
    `\n${transport}: ${partCount} parts x ${formatMiB(partSize)} MiB, concurrency ${concurrency}`,
  );
  const timings: PartTiming[] = [];
  let nextPart = 0;
  const started = performance.now();

  try {
    const worker = async (workerIndex: number) => {
      while (true) {
        const index = nextPart++;
        if (index >= partLengths.length) return;
        const number = index + 1;
        const payload = payloadPool[workerIndex].subarray(0, partLengths[index]);
        const partEndpoint = `${api}/uploads/${init.upload_id}/parts/${number}?${scope}`;
        let target = partEndpoint;
        let headers: Record<string, string> = {
          ...authHeaders,
          "Content-Type": "application/octet-stream",
        };
        let signMs = 0;

        if (transport === "direct") {
          const signStarted = performance.now();
          const signed = await json<SignedPart>(partEndpoint, { headers: authHeaders });
          signMs = performance.now() - signStarted;
          target = signed.url;
          headers = signed.headers || {};
        }

        const putStarted = performance.now();
        const response = await fetch(target, { method: "PUT", headers, body: payload });
        const putMs = performance.now() - putStarted;
        if (!response.ok) {
          throw new Error(
            `${transport} part ${number}: HTTP ${response.status}: ${await response.text()}`,
          );
        }
        timings.push({ number, bytes: payload.byteLength, signMs, putMs });
        console.log(
          `${transport} part ${String(number).padStart(3)}/${partCount}: ` +
            `${formatSeconds(putMs)}, ${formatRate(payload.byteLength, putMs)}` +
            (signMs ? ` (sign ${formatSeconds(signMs)})` : ""),
        );
      }
    };

    await Promise.all(Array.from({ length: concurrency }, (_, index) => worker(index)));
    const elapsedMs = performance.now() - started;
    const status = await json<{ parts?: Array<{ n: number; size: number }> }>(
      `${api}/uploads/${init.upload_id}?${scope}`,
      { headers: authHeaders },
    );
    if ((status.parts || []).length !== partCount) {
      throw new Error(
        `${transport}: server reports ${(status.parts || []).length}/${partCount} parts`,
      );
    }
    console.log(
      `${transport} total: ${formatSeconds(elapsedMs)}, ${formatRate(totalBytes, elapsedMs)}`,
    );
    return { transport, elapsedMs, timings };
  } finally {
    await abort(init.upload_id);
  }
}

async function abort(id: string) {
  const response = await fetch(`${api}/uploads/${id}?${scope}`, {
    method: "DELETE",
    headers: authHeaders,
  });
  if (!response.ok && response.status !== 404) {
    console.warn(`cleanup ${id}: HTTP ${response.status}`);
  }
}

async function json<T>(url: string, init: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${await response.text()}`);
  }
  return (await response.json()) as T;
}

function required(name: string): string {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function positiveNumber(name: string, fallback: number): number {
  const value = process.env[name] ? Number(process.env[name]) : fallback;
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${name} must be positive`);
  return value;
}

function positiveInteger(name: string, fallback: number): number {
  const value = positiveNumber(name, fallback);
  if (!Number.isInteger(value)) throw new Error(`${name} must be an integer`);
  return value;
}

function parseOrder(raw: string): Transport[] {
  const values = raw.split(",").map((value) => value.trim()) as Transport[];
  if (!values.length || values.some((value) => value !== "direct" && value !== "relay")) {
    throw new Error("BENCHMARK_ORDER must be a comma-separated list of direct and relay");
  }
  return values;
}

function percentile(sorted: number[], fraction: number): number {
  if (!sorted.length) return 0;
  return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * fraction))];
}

function formatMiB(bytes: number): string {
  return (bytes / 1024 / 1024).toFixed(1);
}

function formatSeconds(ms: number): string {
  return `${(ms / 1000).toFixed(2)}s`;
}

function formatRate(bytes: number, ms: number): string {
  return `${(bytes / 1024 / 1024 / (ms / 1000)).toFixed(1)} MiB/s`;
}
