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
// We don't compute SHA256 client-side. Pre-dedup short-circuit is
// available if the caller already has a hash on hand.

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
  /** Override the parallelism. Default 4. */
  parallel?: number;
  signal?: AbortSignal;
  /** Notified once the server has issued an upload_id, so the UI can
   *  surface a Cancel button + later trigger DELETE /uploads/<id> if
   *  the user tears the row down out-of-band. */
  onUploadIdAssigned?: (id: string) => void;
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
  const fd = new FormData();
  fd.append("file", file);
  fd.append("folder", opts.folder ?? "/");
  if (opts.visibility) fd.append("visibility", opts.visibility);
  if (opts.tags?.length) fd.append("tags", JSON.stringify(opts.tags));

  const res = await fetch(`${STORAGE_API}/files`, {
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
  const init = (await jsonFetch<InitResponse>("POST", `${STORAGE_API}/uploads`, {
    body: {
      filename: file.name,
      size: file.size,
      content_type: file.type || "application/octet-stream",
      folder: opts.folder ?? "/",
      tags: opts.tags,
      visibility: opts.visibility,
      sha256: opts.sha256,
    },
    signal: opts.signal,
  })).body;

  if (init.was_existing && init.file) {
    opts.onProgress?.(file.size, file.size);
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
  const parallel = Math.max(1, opts.parallel ?? init.max_parallel ?? defaultParallel);

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
  for (let n = 1; n <= totalParts; n++) {
    const start = (n - 1) * partSize;
    const end = Math.min(start + partSize, file.size);
    queue.push({ n, start, end });
  }

  // Track per-part status so onProgress aggregates cleanly.
  // partBytes[n] = bytes the server has confirmed for part n.
  const partBytes = new Map<number, number>();
  const reportProgress = () => {
    let total = 0;
    for (const v of partBytes.values()) total += v;
    opts.onProgress?.(total, file.size);
  };

  // Worker drains the queue. On error we retry with exp backoff;
  // after maxRetriesPerPart we let the error bubble up and the
  // overall upload aborts.
  let firstErr: Error | null = null;
  const work = async () => {
    while (queue.length > 0) {
      if (opts.signal?.aborted) return;
      if (firstErr) return; // sibling worker tripped; bail
      const part = queue.shift();
      if (!part) return;
      let attempt = 0;
      while (attempt < maxRetriesPerPart) {
        try {
          const blob = file.slice(part.start, part.end);
          const res = await fetch(`${STORAGE_API}/uploads/${id}/parts/${part.n}`, {
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
    const completion = (await jsonFetch<{ file: UploadedFile; was_existing: boolean }>(
      "POST",
      `${STORAGE_API}/uploads/${id}/complete`,
      { body: {}, signal: opts.signal },
    )).body;
    return completion.file;
  } catch (e) {
    // User cancel OR per-part retry exhaustion both leak partial
    // bytes on disk if we don't wipe the session. Fire-and-forget
    // DELETE — server is idempotent on missing sessions, so a
    // race with the sweeper is harmless.
    abortServerSession(id).catch(() => undefined);
    throw e;
  }
}

/** Cancel a multipart upload server-side. Idempotent. The panel
 *  calls this from its per-row Cancel button after aborting the
 *  in-flight AbortController. */
export async function abortUploadServer(uploadId: string): Promise<void> {
  return abortServerSession(uploadId);
}

async function abortServerSession(id: string): Promise<void> {
  try {
    await fetch(`${STORAGE_API}/uploads/${id}`, {
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
