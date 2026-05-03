import { useEffect, useRef } from "react";

// Inlined SDK app-event subscription — same 40-line pattern storage
// + crm use. EventSource → /api/app-events/<app>?project_id=…&since=N
// with sequence-based replay on reconnect.
//
// We deliberately don't pull this into a shared module: each app
// is independently installable from its own source, and the trading
// app's bundle should not depend on the storage app being present.
// Copy-and-paste is the right call until the SDK ships an official
// TS client bundle.

export type AppEventEnvelope<T = unknown> = {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
};

declare const __PROJECT_ID__: string | undefined;

function projectId(): string {
  if (typeof window !== "undefined") {
    const w = (window as any).__PROJECT_ID__;
    if (typeof w === "string" && w) return w;
    const fromQuery = new URLSearchParams(window.location.search).get("project_id");
    if (fromQuery) return fromQuery;
  }
  if (typeof __PROJECT_ID__ !== "undefined" && __PROJECT_ID__) return __PROJECT_ID__;
  return "";
}

export function useAppEvents<T = unknown>(
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    const pid = projectId();
    if (!pid) return; // global-scope install — agent self-bootstraps; no SSE.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;

    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/trading?project_id=${encodeURIComponent(pid)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch { /* ignore malformed */ }
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, []);
}
