// Resumable chunked uploader for the storage app's HTTP routes.
// Used by the storage panel itself + by other panels (social,
// media, …) that need to upload a file the user picked. The
// signature is intentionally close to the existing single-shot
// `POST /files` so callers swap in without restructuring.
//
// Behavior:
//   - For files <= simpleUploadCap, fall back to a single multipart
//     POST /api/apps/storage/files. Same shape, less overhead, no
//     resume — small files don't need it.
//   - For larger files, use the resumable protocol:
//       POST  /uploads        init
//       PATCH /uploads/{id}   one chunk per round-trip, sequential
//       POST  /uploads/{id}/complete
//     On any fetch error, GET /uploads/{id} for the actual offset
//     and resume there. Capped at 5 retries with exponential backoff.
//
// We DON'T compute SHA256 client-side. Browsers can do it via
// Crypto.subtle.digest, but for a 2 GB file the streaming digest
// takes long enough that we'd rather just upload. The server
// dedups at complete-time anyway. (Pre-dedup short-circuit is
// available if a caller already has a hash on hand.)

const STORAGE_API = "/api/apps/storage";

// 25 MB threshold: below it, the single-shot path is faster.
const simpleUploadCap = 25 * 1024 * 1024;

// 5 MB chunks: small enough that a dropped connection only loses a
// few seconds of progress on a typical broadband uplink, large
// enough that the round-trip overhead doesn't dominate.
const defaultChunkSize = 5 * 1024 * 1024;

// FileRow mirrors the storage app's response shape (subset).
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
  /** Pre-computed SHA-256 hex string. When provided + the server
   *  already has this content, init short-circuits and the upload
   *  is skipped entirely. */
  sha256?: string;
  /** Fires repeatedly during upload with the bytes/total counters. */
  onProgress?: (bytesUploaded: number, total: number) => void;
  /** Abort the upload mid-flight. */
  signal?: AbortSignal;
}

/** Upload a file to the storage app, streaming + resumable for big
 *  files, single-shot for small ones. Returns the resulting file
 *  row on success. Throws on terminal failure. */
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
  if (opts.tags && opts.tags.length) fd.append("tags", JSON.stringify(opts.tags));

  const res = await fetch(`${STORAGE_API}/files`, {
    method: "POST",
    credentials: "same-origin",
    body: fd,
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(`upload failed (HTTP ${res.status}): ${await res.text()}`);
  }
  // The single-shot endpoint returns the file row at the top level
  // ({id, name, sha256, …}, not wrapped under .file). Fall back to
  // .file too in case storage gets re-wrapped later.
  const data = (await res.json()) as Record<string, unknown>;
  // Crude progress: simple uploads jump from 0 to total at end.
  opts.onProgress?.(file.size, file.size);
  return (typeof data?.id === "number" ? data : (data?.file as Record<string, unknown>)) as unknown as UploadedFile;
}

// ─── resumable path ───────────────────────────────────────────────

interface InitResponse {
  upload_id?: string;
  offset?: number;
  chunk_size_recommended?: number;
  expires_at?: string;
  // Pre-dedup short-circuit: server returns the existing row instead
  // of an upload_id when sha256 hits.
  file?: UploadedFile;
  was_existing?: boolean;
}

interface StatusResponse {
  upload_id: string;
  offset: number;
  declared_size: number;
  status: string;
}

async function uploadChunked(
  file: File,
  opts: UploadResumableOptions,
): Promise<UploadedFile> {
  // Init.
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

  // Pre-dedup hit — caller passed sha256 + server has the bytes.
  if (init.was_existing && init.file) {
    opts.onProgress?.(file.size, file.size);
    return init.file;
  }
  if (!init.upload_id) {
    throw new Error("init returned no upload_id");
  }
  const id = init.upload_id;
  const chunkSize = init.chunk_size_recommended ?? defaultChunkSize;

  // Patch chunks until we hit declared size. Resume by re-reading
  // the offset from the server on transient errors.
  let offset = init.offset ?? 0;
  let attempt = 0;
  const maxAttempts = 5;

  while (offset < file.size) {
    if (opts.signal?.aborted) {
      throw new DOMException("upload aborted", "AbortError");
    }
    const end = Math.min(offset + chunkSize, file.size);
    const blob = file.slice(offset, end);
    try {
      const rec = await fetch(`${STORAGE_API}/uploads/${id}`, {
        method: "PATCH",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/octet-stream",
          "Upload-Offset": String(offset),
        },
        body: blob,
        signal: opts.signal,
      });
      if (rec.status === 409) {
        // Offset drift — server tells us where it really is.
        const conflict = (await rec.json()) as { offset?: number };
        if (typeof conflict.offset === "number") {
          offset = conflict.offset;
          attempt = 0;
          continue;
        }
        throw new Error(`409 without offset: ${JSON.stringify(conflict)}`);
      }
      if (!rec.ok) {
        throw new Error(`PATCH ${rec.status}: ${await rec.text()}`);
      }
      const j = (await rec.json()) as { offset: number };
      offset = j.offset;
      attempt = 0;
      opts.onProgress?.(offset, file.size);
    } catch (e) {
      if ((e as DOMException).name === "AbortError") throw e;
      attempt += 1;
      if (attempt >= maxAttempts) {
        throw new Error(`upload failed after ${maxAttempts} retries: ${(e as Error).message}`);
      }
      // Reconcile with the server before next attempt — the
      // failure may have happened after some bytes landed.
      try {
        const status = (await jsonFetch<StatusResponse>("GET", `${STORAGE_API}/uploads/${id}`, {
          signal: opts.signal,
        })).body;
        offset = status.offset;
      } catch {
        // status itself failing; sleep + retry chunk anyway.
      }
      // Exponential backoff: 250ms, 500ms, 1s, 2s, 4s.
      await sleep(250 * Math.pow(2, attempt - 1));
    }
  }

  // Complete.
  const completion = (await jsonFetch<{ file: UploadedFile; was_existing: boolean }>("POST",
    `${STORAGE_API}/uploads/${id}/complete`, { body: {}, signal: opts.signal })).body;
  return completion.file;
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
