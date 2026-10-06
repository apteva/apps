import type { AudioDashboardHost } from "./audio-dashboard";
export type AudioUpdatesMode = "sse" | "connecting" | "fallback";
interface Envelope { app: string; project_id: string; install_id: number; topic: string; seq?: number }
interface EventBridge { subscribe(app: string, project: string, fn: (event: Envelope) => void): () => void }
const topics = new Set([
  "telephony.audio.reports.changed", "telephony.audio.degraded", "telephony.audio.recovered",
  "telephony.audio.alert", "telephony.audio.alert_recovered",
]);
export function isAudioDashboardEvent(host: AudioDashboardHost, event: Envelope | null | undefined): boolean {
  return !!event && event.app === (host.appName || "telephony") && event.project_id === host.projectId &&
    (!host.installId || event.install_id === host.installId) && topics.has(event.topic);
}
// Reuse the host's shared SSE channel, avoiding one socket per widget. Outside
// the dashboard, use the same authenticated app-event API with bounded retries.
export function subscribeAudioDashboardEvents(
  host: AudioDashboardHost, changed: () => void, mode: (value: AudioUpdatesMode) => void,
): () => void {
  if (!host.projectId) { mode("fallback"); return () => {}; }
  let stopped = false;
  const project = host.projectId;
  const receive = (event: Envelope) => { if (!stopped && isAudioDashboardEvent(host, event)) changed(); };
  const bridge = (window as unknown as { __aptevaAppEvents?: EventBridge }).__aptevaAppEvents;
  if (bridge) {
    const unsubscribe = bridge.subscribe(host.appName || "telephony", project, receive);
    const connected = (event: Event) => {
      if ((event as CustomEvent).detail?.projectId === project) { mode("sse"); changed(); }
    };
    window.addEventListener("apteva:app-events-connected", connected);
    mode("sse"); // A shared subscription; reconciliation also covers host reconnection failure.
    return () => { stopped = true; unsubscribe(); window.removeEventListener("apteva:app-events-connected", connected); };
  }
  if (typeof EventSource === "undefined") { mode("fallback"); return () => {}; }
  let source: EventSource | null = null, cancelled = false, retries = 0, lastSeq = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const connect = () => {
    if (cancelled) return;
    mode("connecting");
    try {
      source = new EventSource(`/api/app-events/${encodeURIComponent(host.appName || "telephony")}?project_id=${encodeURIComponent(project)}${lastSeq ? `&since=${lastSeq}` : ""}`, { withCredentials: true });
    } catch { mode("fallback"); return; }
    const current = source;
    current.onopen = () => { if (cancelled || source !== current) return; retries = 0; mode("sse"); changed(); };
    current.onmessage = (message) => {
      if (cancelled || source !== current) return;
      try {
        const event = JSON.parse(message.data) as Envelope;
        if (!isAudioDashboardEvent(host, event)) return;
        if (event.seq != null) { if (event.seq <= lastSeq) return; lastSeq = event.seq; }
        receive(event);
      } catch { /* Ignore malformed frames; durable query is authoritative. */ }
    };
    current.onerror = () => {
      if (source !== current || cancelled) return;
      current.close(); source = null; mode("fallback");
      if (++retries < 5) timer = setTimeout(connect, 2000);
    };
  };
  connect();
  return () => { stopped = true; cancelled = true; if (timer) clearTimeout(timer); source?.close(); };
}
