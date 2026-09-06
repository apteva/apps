import { sha256 as createSHA256 } from "@noble/hashes/sha2.js";
// Resumable parallel-chunk uploader for the storage app's /uploads
// HTTP routes. Mirrors the S3 multipart-upload pattern omnikit uses:
// each part is an independent PUT, parts run concurrently up to a
// configurable parallelism, complete() concatenates server-side.
//
// Behavior:
//   - File <= simpleUploadCap → single multipart POST /files
//     (less overhead, no parallel benefit anyway).
//   - Larger → init / parallel-PUT-parts / complete.
//   - On per-part error, retry that part with exponential backoff
//     (max 5). Other parts continue uninflueced — that's the
//     point of parts vs offset.
//
// Large files are hashed incrementally for safe resume and deduplication.
// Check hard limits before reading bytes and surface preparation progress.

const STORAGE_API = "/api/apps/storage";
const simpleUploadCap = 25 * 1024 * 1024;
const defaultPartSize = 5 * 1024 * 1024;
const defaultParallel = 4;
const maxRetriesPerPart = 5;

export interface UploadedFile {
  id: number;
  name: string;
  folder: string;
  storage_key: string;
  content_type: string;
  size_bytes: number;
  sha256: string;
  visibility: string;
}

export interface UploadResumableOptions {
  folder?: string;
  tags?: string[];
  visibility?: "private" | "signed" | "public";
  /** Pre-computed SHA-256 hex string. If supplied AND the server
   *  already holds matching bytes, the upload is skipped entirely. */
  sha256?: string;
  /** Fired with cumulative bytes uploaded (sum across in-flight
   *  parts). The total includes parts not yet started. */
  onProgress?: (bytesUploaded: number, total: number) => void;
  onPhase?: (phase: "checking" | "preparing" | "uploading" | "finalizing") => void;
  onPreparationProgress?: (bytesRead: number, total: number) => void;
  /** Override the parallelism. Default 4. */
  parallel?: number;
  signal?: AbortSignal;
  /** Notified once the server has issued an upload_id, so the UI can
   *  surface a Cancel button + later trigger DELETE /uploads/<id> if
   *  the user tears the row down out-of-band. */
  onUploadIdAssigned?: (id: string) => void;
  /** Project + install IDs threaded onto every URL the uploader
   *  builds (POST /files, POST /uploads init, PUT parts, complete,
   *  DELETE). Required for global installs: the storage sidecar
   *  reads project_id from query when APTEVA_PROJECT_ID is empty,
   *  and refuses with 400 if neither is present. Before v0.10.1
   *  this was missing — every other panel fetch went through api()
   *  / withParams() which appended these automatically, but the
   *  upload path bypassed the wrapper. Latent bug, only surfaced
   *  once anyone moved storage to global scope. */
  projectId?: string;
  installId?: number;
}

/** Build "?project_id=…&install_id=…" if either is set. Empty when
 *  both are absent (matches pre-global-install behaviour for
 *  project-scoped sidecars whose APTEVA_PROJECT_ID env supplies
 *  the value). The same URL serves project + global installs —
 *  storage's resolver prefers the env, falls back to the query. */
function scopeQS(opts: UploadResumableOptions): string {
  const parts: string[] = [];
  if (opts.projectId) parts.push(`project_id=${encodeURIComponent(opts.projectId)}`);
  if (opts.installId != null) parts.push(`install_id=${opts.installId}`);
  return parts.length === 0 ? "" : "?" + parts.join("&");
}

export async function uploadResumable(
  file: File,
  opts: UploadResumableOptions = {},
): Promise<UploadedFile> {
  if (file.size <= simpleUploadCap && !opts.sha256) {
    return uploadSimple(file, opts);
  }
  return uploadChunked(file, opts);
}

// ─── single-shot path ─────────────────────────────────────────────

async function uploadSimple(
  file: File,
  opts: UploadResumableOptions,
): Promise<UploadedFile> {
  opts.onPhase?.("uploading");
  const fd = new FormData();
  fd.append("file", file);
  fd.append("folder", opts.folder ?? "/");
  if (opts.visibility) fd.append("visibility", opts.visibility);
  if (opts.tags?.length) fd.append("tags", JSON.stringify(opts.tags));

  const res = await fetch(`${STORAGE_API}/files${scopeQS(opts)}`, {
    method: "POST",
    credentials: "same-origin",
    body: fd,
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(`upload failed (HTTP ${res.status}): ${await res.text()}`);
  }
  const data = (await res.json()) as Record<string, unknown>;
  opts.onProgress?.(file.size, file.size);
  return (typeof data?.id === "number" ? data : (data?.file as Record<string, unknown>)) as unknown as UploadedFile;
}

// ─── parts-based parallel path ────────────────────────────────────

interface InitResponse {
  upload_id?: string;
  part_size?: number;
  max_parallel?: number;
  max_parts?: number;
  expires_at?: string;
  // Pre-dedup short-circuit shape:
  file?: UploadedFile;
  was_existing?: boolean;
}

async function uploadChunked(
  file: File,
  opts: UploadResumableOptions,
): Promise<UploadedFile> {
  opts.onPhase?.("checking");
  const limitsResponse = await fetch(`${STORAGE_API}/uploads${scopeQS(opts)}`, { credentials: "same-origin", signal: opts.signal });
  // Older installed backends do not expose the read-only limits endpoint.
  if (limitsResponse.status !== 405) {
    if (!limitsResponse.ok) throw new Error(`Cannot check upload limits (HTTP ${limitsResponse.status}): ${await limitsResponse.text()}`);
    const limits = await limitsResponse.json();
    if (file.size > limits.max_file_bytes) throw new Error(`File exceeds Storage's file limit (${Math.floor(limits.max_file_bytes / 1048576)} MiB). Adjust max_upload_size_mb in Storage settings.`);
    if (file.size > limits.max_pending_bytes) throw new Error(`File exceeds Storage's pending-upload allowance (${Math.floor(limits.max_pending_bytes / 1048576)} MiB). Adjust max_pending_upload_mb in Storage settings.`);
  }
  opts.onPhase?.("preparing");
  const sha256 = opts.sha256 || await hashFile(file, opts.signal, opts.onPreparationProgress);
  opts.signal?.throwIfAborted();
  opts.onPhase?.("checking");
  const resumeKey = "storage-upload:" + JSON.stringify([opts.projectId, opts.installId, opts.folder || "/", file.name, file.size, file.type, sha256, opts.visibility, opts.tags]);
  let init: InitResponse | undefined;
  let confirmed: { n: number; size: number }[] = [];
  let saved: InitResponse | null = null;
  try { saved = JSON.parse(localStorage.getItem(resumeKey) || "null"); } catch {}
  if (saved?.upload_id) {
    const response = await fetch(`${STORAGE_API}/uploads/${saved.upload_id}${scopeQS(opts)}`, { credentials: "same-origin", signal: opts.signal });
    if (response.ok) {
      const status = await response.json();
      if (status.declared_size === file.size) { init = saved; confirmed = status.parts || []; }
    } else if (response.status !== 404 && response.status !== 403) {
      throw new Error(`Resume failed: ${response.status}`);
    }
  }
  if (!init) {
  init = (await jsonFetch<InitResponse>("POST", `${STORAGE_API}/uploads${scopeQS(opts)}`, {
    body: {
      filename: file.name,
      size: file.size,
      content_type: file.type || "application/octet-stream",
      folder: opts.folder ?? "/",
      tags: opts.tags,
      visibility: opts.visibility,
      sha256,
    },
    signal: opts.signal,
  })).body;

    try { localStorage.setItem(resumeKey, JSON.stringify(init)); } catch {}
  }

  if (init.was_existing && init.file) {
    opts.onProgress?.(file.size, file.size);
    try { localStorage.removeItem(resumeKey); } catch {}
    return init.file;
  }
  if (!init.upload_id) {
    throw new Error("init returned no upload_id");
  }
  const id = init.upload_id;
  // Side-channel for the panel: tell the caller the upload-id as
  // soon as we have one, so a Cancel button can call DELETE even
  // if the AbortController is later misplaced. Best-effort.
  opts.onUploadIdAssigned?.(id);
  const partSize = init.part_size ?? defaultPartSize;
  if (!Number.isInteger(partSize) || partSize <= 0) throw new Error("Invalid upload part size");
  const parallel = Math.min(8, Math.max(1, opts.parallel ?? init.max_parallel ?? defaultParallel));

  // Build the parts queue (1-indexed, S3-style). Each entry is
  // {n, start, end} so workers can slice without recomputing.
  const totalParts = Math.ceil(file.size / partSize);
  if (init.max_parts && totalParts > init.max_parts) {
    throw new Error(
      `file too large: needs ${totalParts} parts but server caps at ${init.max_parts}`,
    );
  }
  type Part = { n: number; start: number; end: number };
  const queue: Part[] = [];
  const confirmedSizes = new Map(confirmed.map(p => [p.n, p.size]));
  for (let n = 1; n <= totalParts; n++) {
    const start = (n - 1) * partSize;
    const end = Math.min(start + partSize, file.size);
    if (confirmedSizes.get(n) !== end - start) queue.push({ n, start, end });
  }

  // Track per-part status so onProgress aggregates cleanly.
  // partBytes[n] = bytes the server has confirmed for part n.
  const partBytes = new Map<number, number>(confirmed.map(p => [p.n, p.size]));
  let confirmedBytes = [...partBytes.values()].reduce((a,b) => a+b, 0);
  const reportProgress = () => opts.onProgress?.(confirmedBytes, file.size);
  opts.onPhase?.("uploading");
  reportProgress();

  // Worker drains the queue. On error we retry with exp backoff;
  // after maxRetriesPerPart we let the error bubble up and the
  // overall upload aborts.
  let firstErr: Error | null = null;
  let nextPart = 0;
  const work = async () => {
    while (nextPart < queue.length) {
      if (opts.signal?.aborted) return;
      if (firstErr) return; // sibling worker tripped; bail
      const part = queue[nextPart++];
      if (!part) return;
      let attempt = 0;
      while (attempt < maxRetriesPerPart) {
        try {
          const blob = file.slice(part.start, part.end);
          const res = await fetch(`${STORAGE_API}/uploads/${id}/parts/${part.n}${scopeQS(opts)}`, {
            method: "PUT",
            credentials: "same-origin",
            headers: { "Content-Type": "application/octet-stream" },
            body: blob,
            signal: opts.signal,
          });
          if (!res.ok) {
            throw new Error(`PUT part ${part.n} → ${res.status}: ${await res.text()}`);
          }
          const j = (await res.json()) as { size: number };
          confirmedBytes += j.size - (partBytes.get(part.n) || 0);
          partBytes.set(part.n, j.size);
          reportProgress();
          break;
        } catch (e) {
          if ((e as DOMException).name === "AbortError") return;
          attempt += 1;
          if (attempt >= maxRetriesPerPart) {
            firstErr = new Error(
              `part ${part.n} failed after ${maxRetriesPerPart} retries: ${(e as Error).message}`,
            );
            return;
          }
          await sleep(250 * Math.pow(2, attempt - 1));
        }
      }
    }
  };

  const workers: Promise<void>[] = [];
  for (let i = 0; i < parallel; i++) workers.push(work());

  try {
    await Promise.all(workers);
    if (firstErr) throw firstErr;
    if (opts.signal?.aborted) {
      throw new DOMException("upload aborted", "AbortError");
    }
    // All parts are on the server. Complete.
    opts.onPhase?.("finalizing");
    const completion = (await jsonFetch<{ file: UploadedFile; was_existing: boolean }>(
      "POST",
      `${STORAGE_API}/uploads/${id}/complete${scopeQS(opts)}`,
      { body: {}, signal: opts.signal },
    )).body;
    try { localStorage.removeItem(resumeKey); } catch {}
    return completion.file;
  } catch (e) {
    // User cancel OR per-part retry exhaustion both leak partial
    // bytes on disk if we don't wipe the session. Fire-and-forget
    // DELETE — server is idempotent on missing sessions, so a
    // race with the sweeper is harmless.
    if (opts.signal?.aborted || (e as DOMException).name === "AbortError") {
      try { localStorage.removeItem(resumeKey); } catch {}
      await abortServerSession(id, opts);
    }
    throw e;
  }
}

/** Cancel a multipart upload server-side. Idempotent. The panel
 *  calls this from its per-row Cancel button after aborting the
 *  in-flight AbortController. Pass the same project/install IDs the
 *  upload was started with — required for global installs where the
 *  sidecar reads scope from the query string. */
export async function abortUploadServer(
  uploadId: string,
  opts: { projectId?: string; installId?: number } = {},
): Promise<void> {
  return abortServerSession(uploadId, opts);
}

async function abortServerSession(
  id: string,
  opts: { projectId?: string; installId?: number },
): Promise<void> {
  try {
    await fetch(`${STORAGE_API}/uploads/${id}${scopeQS(opts)}`, {
      method: "DELETE",
      credentials: "same-origin",
      // Deliberately no AbortSignal here: the user's AbortController
      // is what got us here. Using it would short-circuit the cleanup.
    });
  } catch {
    // Network failure during cleanup is logged-and-forgotten —
    // the sweeper will eventually reclaim the session.
  }
}

// ─── helpers ─────────────────────────────────────────────────────

async function jsonFetch<T>(
  method: string,
  url: string,
  opts: { body?: unknown; signal?: AbortSignal } = {},
): Promise<{ status: number; body: T }> {
  const init: RequestInit = {
    method,
    credentials: "same-origin",
    headers: opts.body ? { "Content-Type": "application/json" } : undefined,
    signal: opts.signal,
  };
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  const res = await fetch(url, init);
  if (!res.ok) {
    throw new Error(`${method} ${url} → ${res.status}: ${await res.text()}`);
  }
  return { status: res.status, body: (await res.json()) as T };
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function hashFile(file: File, signal?: AbortSignal, progress?: (read: number, total: number) => void): Promise<string> {
 const hash = createSHA256.create(); const reader = file.stream().getReader();
 let read = 0, updated = performance.now();
 progress?.(0, file.size);
 try { while (true) {
   signal?.throwIfAborted(); const {done,value} = await reader.read(); if (done) break;
   hash.update(value); read += value.length;
   if (performance.now() - updated >= 50) {
     progress?.(read, file.size);
     await sleep(0); // Let progress paint and Cancel run during large hashes.
     updated = performance.now();
   }
 } }
 finally { await reader.cancel(); reader.releaseLock(); }
 signal?.throwIfAborted();
 progress?.(read, file.size);
 return Array.from(hash.digest(), b => b.toString(16).padStart(2,"0")).join("");
}
