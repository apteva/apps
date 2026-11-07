import { useCallback, useEffect, useRef, useState } from "react";

// Buffer one polling cycle so newly observed calls begin before their walking
// animation reaches the event, rather than jumping forward on every refresh.
export const VILLAGE_DELAY_MS = 3500;
const API = "/api/apps/agent-worlds/api";

export function apiURL(path: string, installId: number, projectId: string = "") {
  const url = new URL(path.startsWith("/api/") ? path : API + path, window.location.origin);
  if (installId) url.searchParams.set("install_id", String(installId));
  if (projectId) url.searchParams.set("project_id", projectId);
  return url.pathname + url.search;
}

// Share one cancellable, sequential polling path for inventory and scenes.
// Pausing stops network refreshes without throwing away the replay snapshot.
export function usePolledJSON<T>(path: string, installId: number, projectId: string, interval: number, polling = true) {
  const url = apiURL(path, installId, projectId);
  const [result, setResult] = useState<{ url: string; data: T | null; error: string }>({ url: "", data: null, error: "" });
  const [revision, setRevision] = useState(0);
  const pollingRef = useRef(polling);
  pollingRef.current = polling;
  const refresh = useCallback(() => setRevision(value => value + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    let timer: number;
    const schedule = () => {
      if (controller.signal.aborted) return;
      timer = window.setTimeout(() => {
        if (pollingRef.current && document.visibilityState !== "hidden") void load();
        else schedule();
      }, interval);
    };
    const load = async () => {
      try {
        const response = await fetch(url, { signal: controller.signal });
        const body = await response.json();
        if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
        if (!controller.signal.aborted) setResult({ url, data: body, error: "" });
      } catch (caught) {
        if (!controller.signal.aborted) setResult(previous => ({ url, data: previous.url === url ? previous.data : null, error: (caught as Error).message }));
      } finally { schedule(); }
    };
    void load();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [url, interval, revision]);

  return { data: result.url === url ? result.data : null, error: result.url === url ? result.error : "", refresh };
}

// Map themes animate on a shared clock; other themes are static between polls.
export function useAnimationClock(running: boolean) {
  const [now, setNow] = useState(Date.now() - VILLAGE_DELAY_MS);
  useEffect(() => {
    if (!running) return;
    setNow(Date.now() - VILLAGE_DELAY_MS);
    const motion = typeof matchMedia === "function" ? matchMedia("(prefers-reduced-motion: reduce)") : null;
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "hidden" && !motion?.matches) setNow(Date.now() - VILLAGE_DELAY_MS);
    }, 80);
    return () => window.clearInterval(timer);
  }, [running]);
  return [now, setNow] as const;
}
