import type { AppHandle } from "@apteva/web-sdk";
import workletSource from "../../ui/softphone-worklet.js" with { type: "text" };
import listenerSource from "../../ui/listener-worklet.js" with { type: "text" };
import workerSource from "../../ui/softphone-worker.js" with { type: "text" };

export interface AudioModules { urls: string[]; dispose(): void }

// At most three immutable scripts per handle. Weak ownership prevents retaining
// clients after logout; different handles never share authorization results.
const verified = new WeakMap<AppHandle, { scope: string; entries: Map<string, Promise<string>> }>();
const hashes = new Map<string, Promise<string>>();
export const AUDIO_ASSET_TIMEOUT_MS = 8000;

export async function loadAudioAsset(app: AppHandle, kind: "worklet" | "worker" | "listener"): Promise<AudioModules> {
  const source = kind === "worklet" ? workletSource : kind === "worker" ? workerSource : listenerSource;
  if (typeof location === "undefined" || new URL(app.mcpURL(), location.href).origin !== location.origin) {
    const urls = [URL.createObjectURL(new Blob([source], { type: "text/javascript" }))];
    return { urls, dispose() { urls.splice(0).forEach(url => URL.revokeObjectURL(url)); } };
  }
  if (app.name !== "telephony" || !app.projectId || !Number.isSafeInteger(app.installId) || app.installId! <= 0) {
    throw new Error("Telephony audio requires a project and installation");
  }
  const gateway = new URL(app.mcpURL(), location.href);
  const scope = `${gateway.origin}:${gateway.pathname}:${app.projectId}:${app.installId}`;
  let owner = verified.get(app);
  if (!owner || owner.scope !== scope) { owner = { scope, entries: new Map() }; verified.set(app, owner); }
  const cache = owner.entries, key = kind;
  let pending = cache.get(key);
  if (!pending) {
    pending = (async () => {
      // Source is embedded in the verified bundle and cannot change in this
      // module instance. Hash once, outside every call's startup path.
      let hash = hashes.get(kind);
      if (!hash) {
        hash = crypto.subtle.digest("SHA-256", new TextEncoder().encode(source))
          .then(bytes => Array.from(new Uint8Array(bytes), b => b.toString(16).padStart(2, "0")).join(""));
        hashes.set(kind, hash);
        void hash.catch(() => { if (hashes.get(kind) === hash) hashes.delete(kind); });
      }
      const path = `/ui/frontend/${kind}-${await hash}.js`;
      const controller = new AbortController();
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        const served = await Promise.race([
          app.get<string>(path, { cache: "force-cache", redirect: "error", signal: controller.signal, headers: { Accept: "text/plain" } }),
          new Promise<never>((_, reject) => { timer = setTimeout(() => { reject(new Error("Telephony audio asset preparation timed out")); controller.abort(); }, AUDIO_ASSET_TIMEOUT_MS); }),
        ]);
        if (served !== source) throw new Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");
        const url = new URL(`/api/apps/telephony/_install/${app.installId}${path}`, gateway);
        url.searchParams.set("project_id", app.projectId!);
        url.searchParams.set("install_id", String(app.installId));
        return url.href;
      } finally { clearTimeout(timer); }
    })();
    cache.set(key, pending);
    const entry = pending;
    void entry.catch(() => { if (cache!.get(key) === entry) cache!.delete(key); });
  }
  return { urls: [await pending], dispose() {} };
}

export function embeddedAudioModules(preview = false): AudioModules {
  const urls = (preview ? [workletSource] : [workletSource, workerSource])
    .map(source => URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
  return { urls, dispose() { urls.splice(0).forEach(url => URL.revokeObjectURL(url)); } };
}

/** AudioWorklet uses script-src, even when worker-src permits blob URLs.
 * Same-origin hosts use public, content-addressed installation assets. External
 * hosts keep the embedded modules: Worker cannot load a cross-origin script.
 * No bearer or installation secret is ever put in a native module URL.
 */
export async function loadAudioModules(app: AppHandle, preview = false): Promise<AudioModules> {
  // Blob URLs remain individually owned; only verified same-origin URLs cache.
  if (typeof location === "undefined" || new URL(app.mcpURL(), location.href).origin !== location.origin) return embeddedAudioModules(preview);
  const modules = await Promise.all(preview
    ? [loadAudioAsset(app, "worklet")]
    : [loadAudioAsset(app, "worklet"), loadAudioAsset(app, "worker")]);
  return { urls: modules.flatMap(module => module.urls), dispose() { modules.forEach(module => module.dispose()); } };
}
