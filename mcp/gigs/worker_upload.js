// Browser upload client. Only one small Blob per in-flight part is retained;
// no whole-file reads and no base64. Server sessions survive page reloads.
const uploadSleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const activeFileUploads = new Map();
const uploadSessionIDs = new Map();
const failedUploadCards = new Set();
const pendingInstructionUploads = new Map();

async function uploadWithPreview(file, key, preview, onSuccess) {
  const actions = preview.card.querySelector(".preview-actions");
  const control = document.createElement("button");
  control.type = "button"; control.className = "preview-action";
  actions.appendChild(control);
  let controller = null;
  let discard = null;
  let busy = false;
  let attached = false;
  const run = async () => {
    if (busy) return;
    busy = true; failedUploadCards.delete(preview.card);
    discard?.remove(); discard = null;
    controller = new AbortController(); control.textContent = "Pause";
    control.onclick = () => controller.abort();
    pendingUploads++; pendingInstructionUploads.set(key, (pendingInstructionUploads.get(key) || 0) + 1); updateSubmitDisabled();
    try {
      const id = await uploadFile(file, key, n => preview.setProgress(n), text => preview.setStatus(text), controller.signal);
      if (!id) throw new Error("Upload finished without a saved file. Resume to check its result.");
      if (!attached) { await onSuccess(id); attached = true; }
      preview.setStatus("Ready to submit");
      updateStatus();
      await saveDraft(false);
      control.remove();
    } catch (error) {
      failedUploadCards.add(preview.card);
      preview.setStatus(error.message || "Upload interrupted. Resume to continue.", true);
      control.textContent = "Resume"; control.onclick = run;
      if (!attached) {
        discard = document.createElement("button"); discard.type = "button";
        discard.className = "preview-action danger"; discard.textContent = "Cancel upload";
        discard.onclick = async () => {
          discard.disabled = true;
          try {
            const identity = key + ":" + await uploadFingerprint(file);
            const id = uploadSessionIDs.get(identity);
            if (id) {
              const state = await uploadJSON("/upload/status", {upload_id:id});
              if (state.status === "completed") await discardUploadedFile(key, state.storage_file_id);
              else if (state.status !== "expired" && state.status !== "aborted") await uploadJSON("/upload/abort", {upload_id:id});
            }
            uploadSessionIDs.delete(identity);
            failedUploadCards.delete(preview.card); preview.card.remove(); updateSubmitDisabled();
          } catch (error) { preview.setStatus(error.message || "Cancellation failed. Resume to check this upload.", true); discard.disabled = false; }
        };
        actions.appendChild(discard);
      }
    } finally {
      pendingUploads--; pendingInstructionUploads.set(key, pendingInstructionUploads.get(key) - 1); busy = false; updateSubmitDisabled();
    }
  };
  await run();
}

async function uploadRequest(path, options = {}, retries = 4) {
  for (let attempt = 0; ; attempt++) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 120000);
    const abort = () => controller.abort();
    options.signal?.addEventListener("abort", abort, { once: true });
    try {
      if (options.signal?.aborted) throw new DOMException("Upload paused", "AbortError");
      const res = await fetch(publicWorkerURL(path), { ...options, signal: controller.signal });
      const data = await responseJSON(res);
      return data;
    } catch (error) {
      if (options.signal?.aborted) throw new Error("Upload paused. Choose Resume to continue without starting over.");
      const status = error.status;
      if (attempt >= retries || (status && status !== 408 && status !== 429 && status < 500)) throw error;
      await uploadSleep(Math.min(8000, 500 * 2 ** attempt) + Math.random() * 250);
    } finally {
      clearTimeout(timeout);
      options.signal?.removeEventListener("abort", abort);
    }
  }
}

function uploadJSON(path, body, signal, retries) {
  return uploadRequest(path, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body), signal,
  }, retries);
}

async function uploadFingerprint(file) {
  // Identity for resuming the same local file; Storage independently hashes
  // every full object before committing it. Sampling avoids a GB-sized buffer.
  const first = await file.slice(0, 65536).arrayBuffer();
  const last = await file.slice(Math.max(0, file.size - 65536)).arrayBuffer();
  const meta = new TextEncoder().encode(JSON.stringify([file.name, file.size, file.lastModified]));
  const bytes = new Uint8Array(first.byteLength + last.byteLength + meta.byteLength);
  bytes.set(new Uint8Array(first)); bytes.set(new Uint8Array(last), first.byteLength); bytes.set(meta, first.byteLength + last.byteLength);
  const hash = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(hash), n => n.toString(16).padStart(2, "0")).join("");
}

async function uploadFile(file, instructionKey, onProgress, onStage = () => {}, signal) {
  const clientKey = await uploadFingerprint(file);
  const identity = instructionKey + ":" + clientKey;
  if (activeFileUploads.has(identity)) throw new Error("This file is already uploading for this instruction.");
  activeFileUploads.set(identity, true);
  try {
    return await uploadFileSession(file, instructionKey, clientKey, onProgress, onStage, signal);
  } finally { activeFileUploads.delete(identity); }
}

async function uploadFileSession(file, instructionKey, clientKey, onProgress, onStage, signal) {
  onStage("Preparing upload…");
  const begin = () => uploadJSON("/upload/init", {
    instruction_key: instructionKey, name: file.name, content_type: file.type,
    size_bytes: file.size, transport: "binary", client_key: clientKey,
  }, signal);
  let init = await begin();
  if (init.status === "completed" && init.storage_file_id) { onProgress?.(100); return init.storage_file_id; }
  let uploadID = init.upload_id;
  uploadSessionIDs.set(instructionKey + ":" + clientKey, uploadID);
  if (!uploadID) throw new Error("Storage did not start the upload");
  let status = await uploadJSON("/upload/status", { upload_id: uploadID }, signal);
  if (status.status === "expired" || status.status === "aborted") {
    init = await begin(); uploadID = init.upload_id;
    uploadSessionIDs.set(instructionKey + ":" + clientKey, uploadID);
    status = await uploadJSON("/upload/status", { upload_id: uploadID }, signal);
  }
  if (status.status === "completed" && status.storage_file_id) { onProgress?.(100); return status.storage_file_id; }
  const partSize = Number(init.part_size);
  if (!Number.isSafeInteger(partSize) || partSize < 1 || partSize > 16 * 1024 * 1024) throw new Error("Invalid upload chunk size");

  if (status.status !== "finalizing" && status.stage !== "finalize") {
    const acknowledged = new Map((status.parts || []).map(part => [part.n, part.size]));
    const queue = [];
    let transferred = 0;
    for (let offset = 0, n = 1; offset < file.size; offset += partSize, n++) {
      const end = Math.min(offset + partSize, file.size);
      if (acknowledged.get(n) === end - offset) transferred += end - offset;
      else queue.push({ n, offset, end });
    }
    const progress = () => onProgress?.(Math.min(99, Math.floor(transferred / file.size * 100)));
    progress(); onStage(transferred ? "Resuming upload…" : "Uploading…");
    let firstError = null;
    const work = async () => {
      while (queue.length && !firstError) {
        const part = queue.shift();
        try {
          await uploadRequest("/upload/part?upload_id=" + encodeURIComponent(uploadID) + "&part_number=" + part.n, {
            method: "PUT", headers: { "Content-Type": "application/octet-stream" },
            body: file.slice(part.offset, part.end), signal,
          });
          transferred += part.end - part.offset; progress();
        } catch (error) { firstError = error; }
      }
    };
    await Promise.all(Array.from({length: Math.min(4, queue.length)}, () => work()));
    if (firstError) throw firstError;
  }

  onStage("Finalizing — keep the original file until this says Saved…");
  if (status.status !== "finalizing") await uploadJSON("/upload/complete", {upload_id: uploadID}, signal);
  const deadline = Date.now() + 35 * 60 * 1000;
  for (;;) {
    if (signal?.aborted) throw new Error("Upload paused. Finalization continues safely; Resume to check its result.");
    status = await uploadJSON("/upload/status", {upload_id: uploadID}, signal);
    if (status.status === "completed" && status.storage_file_id) { onProgress?.(100); onStage("Saved"); return status.storage_file_id; }
    if (status.status !== "finalizing") throw new Error(status.error_detail || "Finalization interrupted. Resume to retry saving this file.");
    if (Date.now() >= deadline) throw new Error("Saving is taking longer than expected. Resume to check its result.");
    await uploadSleep(1500);
  }
}
