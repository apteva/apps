import type { AppHandle } from "@apteva/web-sdk";
import workletSource from "../../ui/softphone-worklet.js" with { type: "text" };
import workerSource from "../../ui/softphone-worker.js" with { type: "text" };

export interface AudioModules { urls: string[]; dispose(): void }

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
  if (typeof location === "undefined") return embeddedAudioModules(preview);
  const gateway = new URL(app.mcpURL(), location.href);
  if (gateway.origin !== location.origin) return embeddedAudioModules(preview);
  if (app.name !== "telephony" || !app.projectId || !Number.isSafeInteger(app.installId) || app.installId! <= 0) {
    throw new Error("Telephony audio requires a project and installation");
  }
  const sources = preview ? [workletSource] : [workletSource, workerSource];
  const urls = await Promise.all(sources.map(async (source, index) => {
    const bytes = new TextEncoder().encode(source);
    const hash = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), b => b.toString(16).padStart(2, "0")).join("");
    const path = `/ui/frontend/${index === 0 ? "worklet" : "worker"}-${hash}.js`;
    // Fetch through the original handle: authorization, project, installation,
    // and token refresh remain the SDK's responsibility. Match exact bytes to
    // the code embedded in the verified client before executing the asset.
    const served = await app.get<string>(path, { cache: "no-cache", redirect: "error", headers: { Accept: "text/plain" } });
    if (served !== source) throw new Error("Telephony audio asset integrity mismatch. Reload after the app update finishes.");
    const url = new URL(`/api/apps/telephony/_install/${app.installId}${path}`, gateway);
    url.searchParams.set("project_id", app.projectId!);
    url.searchParams.set("install_id", String(app.installId));
    return url.href;
  }));
  return { urls, dispose() {} };
}
