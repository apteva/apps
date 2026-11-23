import { useCallback, useEffect, useRef, useState } from "react";
import type { AudioDashboardHost } from "./audio-dashboard";

// Shared data loading for the Telephony dashboard widgets. Each widget reads one
// operator-only view of /audio-health and refreshes on the host's shared app
// event channel, with polling as the authoritative fallback.

interface Envelope { app: string; project_id: string; install_id: number; topic: string }
interface EventBridge { subscribe(app: string, project: string, fn: (event: Envelope) => void): () => void }

export const callLifecycleTopics = [
  "call.incoming", "call.offered", "call.initiated", "call.ringing", "call.answered",
  "call.completed", "call.failed", "call.busy", "call.no_answer", "call.canceled",
];

export function widgetURL(host: AudioDashboardHost, view: string, params: Record<string, string>): string {
  const q = new URLSearchParams();
  q.set("view", view);
  if (host.projectId) q.set("project_id", host.projectId);
  if (host.installId) q.set("install_id", String(host.installId));
  for (const [key, value] of Object.entries(params)) if (value) q.set(key, value);
  return `/api/apps/${encodeURIComponent(host.appName || "telephony")}/audio-health?${q}`;
}

export function isWidgetEvent(host: AudioDashboardHost, topics: Set<string>, event: Envelope | null | undefined): boolean {
  return !!event && event.app === (host.appName || "telephony") && event.project_id === host.projectId &&
    (!host.installId || event.install_id === host.installId) && topics.has(event.topic);
}

export function useWidgetQuery<T>(
  host: AudioDashboardHost,
  view: string,
  params: Record<string, string>,
  topics: string[],
  interval: number,
) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const request = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const key = JSON.stringify(params);
  const load = useCallback(async () => {
    if (!host.projectId) return;
    request.current?.abort();
    const ctrl = new AbortController();
    request.current = ctrl;
    const current = ++generation.current;
    setLoading(true);
    const timeout = window.setTimeout(() => ctrl.abort("timeout"), 12000);
    try {
      const response = await fetch(widgetURL(host, view, JSON.parse(key)), { credentials: "same-origin", signal: ctrl.signal });
      if (!response.ok) {
        throw new Error(response.status === 403 ? "Operator access is required." : `Request failed (HTTP ${response.status}).`);
      }
      const value = (await response.json()) as T;
      if (current !== generation.current) return;
      setData(value);
      setError("");
    } catch (e) {
      if (current !== generation.current) return;
      if (ctrl.signal.aborted && ctrl.signal.reason !== "timeout") return;
      setError(ctrl.signal.reason === "timeout" ? "Request timed out. Figures may be out of date." : (e as Error).message);
    } finally {
      window.clearTimeout(timeout);
      if (current === generation.current) setLoading(false);
    }
  }, [host.appName, host.installId, host.projectId, view, key]);

  // A different project, install or view starts empty; filter changes keep the
  // previous figures on screen until the new ones arrive.
  useEffect(() => { setData(null); setError(""); }, [host.appName, host.installId, host.projectId, view]);
  useEffect(() => {
    void load();
    return () => { generation.current++; request.current?.abort(); };
  }, [load]);

  const latest = useRef(load);
  latest.current = load;
  const [live, setLive] = useState(false);
  const topicKey = topics.join(",");
  useEffect(() => {
    const bridge = (window as unknown as { __aptevaAppEvents?: EventBridge }).__aptevaAppEvents;
    if (!bridge || !host.projectId || !topicKey) { setLive(false); return; }
    const wanted = new Set(topicKey.split(","));
    let pending: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = bridge.subscribe(host.appName || "telephony", host.projectId, (event) => {
      if (!isWidgetEvent(host, wanted, event) || document.hidden || pending) return;
      pending = setTimeout(() => { pending = undefined; void latest.current(); }, 300);
    });
    setLive(true);
    return () => { unsubscribe(); if (pending) clearTimeout(pending); };
  }, [host.appName, host.installId, host.projectId, topicKey]);

  useEffect(() => {
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, interval);
    const resume = () => { if (!document.hidden) void load(); };
    document.addEventListener("visibilitychange", resume);
    return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", resume); };
  }, [load, interval]);

  return { data, error, loading, live, reload: load };
}

export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return "—";
  const s = Math.round(seconds);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(r).padStart(2, "0")}`;
  return `${m}:${String(r).padStart(2, "0")}`;
}

export function stringSetting(settings: Record<string, unknown> | undefined, key: string, allowed: string[], fallback: string): string {
  const value = settings?.[key];
  return typeof value === "string" && allowed.includes(value) ? value : fallback;
}

export function freeSetting(settings: Record<string, unknown> | undefined, key: string): string {
  const value = settings?.[key];
  return typeof value === "string" ? value.trim().slice(0, 64) : "";
}
