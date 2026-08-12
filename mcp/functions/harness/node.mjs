// Apteva Functions — JS worker harness (node).
//
// Booted once per warm worker by the functions sidecar. Imports the
// function's handler module, then serves invocations over a
// socketpair fd until the sidecar closes the connection or kills us.
//
// Protocol (4-byte big-endian length prefix + JSON, both directions):
//   sidecar -> worker:  { id, event }                       (invocation)
//                       { type:"call_result", callId, ok, result?, error? }
//   worker  -> sidecar:  { type:"ready", ok, error? }        (once, on boot)
//                        { type:"call", callId, app, tool, input }
//                        { type:"integration", callId, conn, tool, input }
//                        { type:"stream_start", id, statusCode, headers }
//                        { type:"stream_chunk", id, encoding, data }
//                        { id, ok, result?, error?, logs? }  (invocation result)
//
// The handler contract:
//   export default async function handler(event, context) { return result }
// context = { functionName, functionId, runtime, env, log, call, integration,
//             stream, sse }.
//
// context.call(app, tool, input) reaches sibling Apteva apps.
// context.integration(conn, tool, input) reaches an in-project
//   integration connection (Pushover, Slack, Resend, ...). `conn` is
//   the numeric connection_id from the Connections panel OR the app
//   slug ("pushover") — the sidecar resolves slugs to the single
//   matching connection in the function's project.
//
// In both cases the sidecar mediates the call; the worker never
// holds a platform token.

import net from "node:net";
import { pathToFileURL } from "node:url";

const ENTRY = process.env.APTEVA_FN_ENTRY;
const FD = 3;

// ── framing ───────────────────────────────────────────────────────
function encodeFrame(obj) {
  const payload = Buffer.from(JSON.stringify(obj), "utf8");
  if (payload.length > MAX_INBOUND_FRAME) {
    throw new Error(`frame too large (${payload.length})`);
  }
  const len = Buffer.allocUnsafe(4);
  len.writeUInt32BE(payload.length, 0);
  return Buffer.concat([len, payload]);
}

function writeFrameAsync(sock, obj) {
  const frame = encodeFrame(obj);
  if (sock.write(frame)) return Promise.resolve();
  return new Promise((resolve) => sock.once("drain", resolve));
}

// ── log capture ───────────────────────────────────────────────────
// During a handler call, console.* is buffered and returned in the
// response frame. Outside a call (e.g. module top-level on import),
// it falls through to real stderr.
let currentLogs = null;
const MAX_LOG_BYTES = 16 * 1024;
const MAX_INBOUND_FRAME = 128 * 1024 * 1024;
let currentLogBytes = 0;
const realErr = console.error.bind(console);
function fmt(args) {
  return args
    .map((a) => {
      if (typeof a === "string") return a;
      try {
        return JSON.stringify(a);
      } catch {
        return String(a);
      }
    })
    .join(" ");
}
for (const m of ["log", "info", "warn", "error", "debug"]) {
  console[m] = (...args) => {
    const line = fmt(args);
    if (currentLogs) {
      if (currentLogBytes < MAX_LOG_BYTES) {
        const remaining = MAX_LOG_BYTES - currentLogBytes;
        const clipped = Buffer.byteLength(line) > remaining
          ? Buffer.from(line).subarray(0, remaining).toString("utf8")
          : line;
        currentLogs.push(clipped);
        currentLogBytes += Buffer.byteLength(clipped) + 1;
      }
    }
    else realErr(line);
  };
}

let bootError = null;

async function main() {
  let handler;
  try {
    const mod = await import(pathToFileURL(ENTRY).href);
    handler = mod.default;
    if (typeof handler !== "function") {
      throw new Error("function module must `export default` a handler function");
    }
  } catch (e) {
    bootError = e;
  }

  const sock = new net.Socket({ fd: FD, readable: true, writable: true });

  if (bootError) {
    sock.write(
      encodeFrame({
        type: "ready",
        ok: false,
        error: String((bootError && bootError.stack) || bootError),
      }),
    );
    sock.end();
    process.exit(1);
  }
  sock.write(encodeFrame({ type: "ready", ok: true }));

  // ── cross-app calls ─────────────────────────────────────────────
  // context.call sends a `call` frame and resolves when the matching
  // `call_result` frame comes back. The sidecar does the real work.
  let callSeq = 0;
  const pendingCalls = new Map(); // callId -> { resolve, reject }

  function makeCall(app, tool, input) {
    return new Promise((resolve, reject) => {
      if (!app || !tool) {
        reject(new Error("context.call(app, tool, input): app and tool are required"));
        return;
      }
      const callId = ++callSeq;
      pendingCalls.set(callId, { resolve, reject });
      try {
        sock.write(encodeFrame({ type: "call", callId, app, tool, input: input ?? {} }));
      } catch (e) {
        pendingCalls.delete(callId);
        reject(e);
      }
    });
  }

  function makeIntegration(conn, tool, input) {
    return new Promise((resolve, reject) => {
      if (conn == null || conn === "") {
        reject(new Error("context.integration(conn, tool, input): conn (numeric id or slug) is required"));
        return;
      }
      if (!tool) {
        reject(new Error("context.integration(conn, tool, input): tool is required"));
        return;
      }
      const callId = ++callSeq;
      pendingCalls.set(callId, { resolve, reject });
      try {
        // `conn` is passed verbatim — the sidecar accepts a number
        // (connection_id) or a string (app slug to resolve in the
        // project, e.g. "pushover").
        sock.write(encodeFrame({ type: "integration", callId, conn, tool, input: input ?? {} }));
      } catch (e) {
        pendingCalls.delete(callId);
        reject(e);
      }
    });
  }

  // ── invocation handling ─────────────────────────────────────────
  async function handle(req) {
    const { id, event } = req;
    const logs = [];
    currentLogs = logs;
    currentLogBytes = 0;
    let streamStarted = false;
    async function startStream(options = {}) {
      if (streamStarted) return;
      streamStarted = true;
      const statusCode = Number.isInteger(options.statusCode) ? options.statusCode : 200;
      const headers = {};
      for (const [key, value] of Object.entries(options.headers || {})) {
        if (value != null) headers[String(key)] = String(value);
      }
      await writeFrameAsync(sock, { type: "stream_start", id, statusCode, headers });
    }
    async function writeStream(chunk) {
      if (!streamStarted) {
        await startStream({ headers: { "Content-Type": "application/octet-stream" } });
      }
      let payload;
      if (Buffer.isBuffer(chunk)) payload = chunk;
      else if (ArrayBuffer.isView(chunk)) payload = Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
      else if (chunk instanceof ArrayBuffer) payload = Buffer.from(chunk);
      else payload = Buffer.from(String(chunk ?? ""), "utf8");
      await writeFrameAsync(sock, {
        type: "stream_chunk", id, encoding: "base64", data: payload.toString("base64"),
      });
    }
    async function sendSSE(data, options = {}) {
      if (!streamStarted) {
        await startStream({
          statusCode: options.statusCode,
          headers: {
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
            ...(options.headers || {}),
          },
        });
      }
      const lines = [];
      if (options.id != null) lines.push(`id: ${String(options.id).replace(/[\r\n]/g, "")}`);
      if (options.event) lines.push(`event: ${String(options.event).replace(/[\r\n]/g, "")}`);
      if (Number.isInteger(options.retry) && options.retry >= 0) lines.push(`retry: ${options.retry}`);
      const text = typeof data === "string" ? data : JSON.stringify(data ?? null);
      for (const line of text.split(/\r?\n/)) lines.push(`data: ${line}`);
      await writeStream(`${lines.join("\n")}\n\n`);
    }
    async function sendSSEComment(comment) {
      if (!streamStarted) {
        await startStream({
          headers: {
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
          },
        });
      }
      const text = String(comment ?? "").split(/\r?\n/).map((line) => `: ${line}`).join("\n");
      await writeStream(`${text}\n\n`);
    }
    const context = {
      functionName: process.env.APTEVA_FUNCTION_NAME || "",
      functionId: process.env.APTEVA_FUNCTION_ID || "",
      runtime: process.env.APTEVA_FUNCTION_RUNTIME || "",
      env: { ...process.env },
      log: (...a) => console.log(...a),
      call: (app, tool, input) => makeCall(app, tool, input),
      integration: (conn, tool, input) => makeIntegration(conn, tool, input),
      stream: { start: startStream, write: writeStream },
      sse: { send: sendSSE, comment: sendSSEComment },
    };
    let frame;
    try {
      const result = await handler(event, context);
      frame = { id, ok: true, result: result === undefined ? null : result, logs };
    } catch (e) {
      frame = { id, ok: false, error: String((e && e.stack) || e), logs };
    } finally {
      currentLogs = null;
    }
    try {
      await writeFrameAsync(sock, frame);
    } catch {
      // socket gone — the sidecar already moved on
    }
  }

  // ── read loop ───────────────────────────────────────────────────
  const chunks = [];
  let bufferedBytes = 0;
  let draining = false;

  function peekLength() {
    if (chunks[0].length >= 4) return chunks[0].readUInt32BE(0);
    const header = Buffer.allocUnsafe(4);
    let offset = 0;
    for (const chunk of chunks) {
      const n = Math.min(chunk.length, 4 - offset);
      chunk.copy(header, offset, 0, n);
      offset += n;
      if (offset === 4) break;
    }
    return header.readUInt32BE(0);
  }

  function takeBytes(n) {
    if (n === 0) return Buffer.alloc(0);
    const first = chunks[0];
    if (first.length === n) {
      chunks.shift();
      bufferedBytes -= n;
      return first;
    }
    if (first.length > n) {
      const out = first.subarray(0, n);
      chunks[0] = first.subarray(n);
      bufferedBytes -= n;
      return out;
    }
    const out = Buffer.allocUnsafe(n);
    let offset = 0;
    while (offset < n) {
      const chunk = chunks[0];
      const take = Math.min(chunk.length, n - offset);
      chunk.copy(out, offset, 0, take);
      offset += take;
      if (take === chunk.length) chunks.shift();
      else chunks[0] = chunk.subarray(take);
    }
    bufferedBytes -= n;
    return out;
  }

  function drain() {
    if (draining) return;
    draining = true;
    try {
      while (bufferedBytes >= 4) {
        const len = peekLength();
        if (len > MAX_INBOUND_FRAME) {
          realErr(`harness: inbound frame too large (${len})`);
          process.exit(1);
        }
        if (bufferedBytes < 4 + len) break;
        takeBytes(4);
        const payload = takeBytes(len);
        let msg;
        try {
          msg = JSON.parse(payload.toString("utf8"));
        } catch {
          continue;
        }
        if (msg.type === "call_result") {
          const p = pendingCalls.get(msg.callId);
          if (p) {
            pendingCalls.delete(msg.callId);
            if (msg.ok) p.resolve(msg.result ?? null);
            else p.reject(new Error(msg.error || "cross-app call failed"));
          }
          continue;
        }
        // Invocation request — fire it, do NOT await. The handler may
        // be mid-context.call; leaving the loop free lets the
        // matching call_result frame route back to it.
        handle(msg);
      }
    } finally {
      draining = false;
    }
  }

  sock.on("data", (chunk) => {
    chunks.push(chunk);
    bufferedBytes += chunk.length;
    drain();
  });
  sock.on("close", () => process.exit(0));
  sock.on("error", () => process.exit(0));
}

main().catch((e) => {
  realErr("harness fatal:", e);
  process.exit(1);
});
