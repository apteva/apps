export type StreamEvent = { type: string; data: Record<string, any> };

// Incremental SSE decoder: events can cross UTF-8/network chunk boundaries.
export function sseDecoder(deliver: (event: StreamEvent) => void) {
  let buffer = "", type = "message", data: string[] = [], size = 0;
  return (chunk: string) => {
    buffer += chunk;
    if (buffer.length + size > 5 * 1024 * 1024) throw new Error("Live update exceeded the size limit");
    let newline: number;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, newline).replace(/\r$/, ""); buffer = buffer.slice(newline + 1);
      if (!line) {
        if (data.length) deliver({ type, data: JSON.parse(data.join("\n")) });
        type = "message"; data = []; size = 0;
      } else if (line.startsWith("event:")) type = line.slice(6).trim();
      else if (line.startsWith("data:")) { const value = line.slice(5).replace(/^ /, ""); data.push(value); size += value.length; }
    }
  };
}

export async function watchCollection(options: {
  url: string; database: string; collection: string; signal: AbortSignal;
  snapshot: (result: any) => void; changed: () => void; status: (status: string) => void;
}) {
  let after = "", failures = 0;
  while (!options.signal.aborted) {
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    try {
      options.status(after ? "Reconnecting" : "Connecting");
      const body: Record<string, any> = { database: options.database, collections: [options.collection] };
      if (after) body.after = after;
      else body.initial = { op: "find", args: { collection: options.collection, limit: 50 } };
      const response = await fetch(options.url, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: options.signal });
      if (!response.ok) {
        const error = (await response.json()).error;
        if (["history_expired", "resume_invalid"].includes(error?.code)) { after = ""; options.status("Refreshing"); continue; }
        if (["permission_denied", "realtime_disabled", "unsupported", "not_found"].includes(error?.code)) { options.status(error.message); return; }
        throw new Error(error?.message || "Live connection failed");
      }
      if (!response.body) throw new Error("Streaming is unavailable");
      reader = response.body.getReader();
      const decoder = new TextDecoder();
      const accept = sseDecoder(event => {
        if (event.type === "snapshot") options.snapshot(event.data.result);
        if (event.type === "ready") { after = event.data.cursor; failures = 0; options.status("Live"); }
        if (event.type === "invalidate") { options.changed(); after = event.data.cursor; }
        if (event.type === "progress") after = event.data.cursor;
        if (event.type === "error") {
          if (["history_expired", "resume_invalid"].includes(event.data.error?.code)) after = "";
          throw new Error(event.data.error?.message || "Live connection interrupted");
        }
      });
      for (;;) { const part = await reader.read(); if (part.done) break; accept(decoder.decode(part.value, { stream: true })); }
    } catch { if (options.signal.aborted) return; options.status("Reconnecting"); }
    finally { await reader?.cancel().catch(() => {}); }
    if (options.signal.aborted) return;
    const delay = Math.min(10000, 500 * 2 ** Math.min(failures++, 5)) * (0.8 + Math.random() * 0.4);
    await new Promise<void>(resolve => {
      const done = () => { clearTimeout(timer); options.signal.removeEventListener("abort", done); resolve(); };
      const timer = setTimeout(done, delay); options.signal.addEventListener("abort", done, { once: true });
      if (options.signal.aborted) done();
    });
  }
}
