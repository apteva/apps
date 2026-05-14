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
//                        { id, ok, result?, error?, logs? }  (invocation result)
//
// The handler contract:
//   export default async function handler(event, context) { return result }
// context = { functionName, functionId, runtime, env, log, call }.
// context.call(app, tool, input) reaches other Apteva apps — the
// sidecar mediates it; the worker never holds a platform token.

import net from "node:net";
import { pathToFileURL } from "node:url";

const ENTRY = process.env.APTEVA_FN_ENTRY;
const FD = 3;

// ── framing ───────────────────────────────────────────────────────
function encodeFrame(obj) {
  const payload = Buffer.from(JSON.stringify(obj), "utf8");
  const len = Buffer.allocUnsafe(4);
  len.writeUInt32BE(payload.length, 0);
  return Buffer.concat([len, payload]);
}

// ── log capture ───────────────────────────────────────────────────
// During a handler call, console.* is buffered and returned in the
// response frame. Outside a call (e.g. module top-level on import),
// it falls through to real stderr.
let currentLogs = null;
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
    if (currentLogs) currentLogs.push(line);
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

  // ── invocation handling ─────────────────────────────────────────
  async function handle(req) {
    const { id, event } = req;
    const logs = [];
    currentLogs = logs;
    const context = {
      functionName: process.env.APTEVA_FUNCTION_NAME || "",
      functionId: process.env.APTEVA_FUNCTION_ID || "",
      runtime: process.env.APTEVA_FUNCTION_RUNTIME || "",
      env: { ...process.env },
      log: (...a) => console.log(...a),
      call: (app, tool, input) => makeCall(app, tool, input),
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
      sock.write(encodeFrame(frame));
    } catch {
      // socket gone — the sidecar already moved on
    }
  }

  // ── read loop ───────────────────────────────────────────────────
  let buf = Buffer.alloc(0);
  let draining = false;

  function drain() {
    if (draining) return;
    draining = true;
    try {
      while (buf.length >= 4) {
        const len = buf.readUInt32BE(0);
        if (buf.length < 4 + len) break;
        const payload = buf.subarray(4, 4 + len);
        buf = buf.subarray(4 + len);
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
    buf = Buffer.concat([buf, chunk]);
    drain();
  });
  sock.on("close", () => process.exit(0));
  sock.on("error", () => process.exit(0));
}

main().catch((e) => {
  realErr("harness fatal:", e);
  process.exit(1);
});
