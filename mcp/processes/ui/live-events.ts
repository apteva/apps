import { createContext, useContext, useEffect, useRef, useState } from "react";
export type AppEvent = { app: string; project_id: string; install_id: number; seq: number; topic: string; data: Record<string, any> };
export type LiveSnapshot = { eventRevision: number; appEvents: AppEvent[] };
export type LiveProps = { projectId?: string; installId?: number; eventRevision?: number; appEvents?: AppEvent[]; eventStreamManaged?: boolean };
export const LiveContext = createContext<LiveSnapshot>({ eventRevision: 0, appEvents: [] });

// Existing dashboards do not forward app events to pages. Share one stream per
// project within this module until the host advertises managed subscriptions.
type Listener = (event?: AppEvent) => void;
const channels = new Map<string, { es: EventSource; listeners: Set<Listener> }>();
export function subscribe(project: string, listener: Listener) {
  if (typeof EventSource === "undefined") return () => {};
  let channel = channels.get(project);
  if (!channel) {
    const listeners = new Set<Listener>();
    const es = new EventSource(`/api/app-events/_all?project_id=${encodeURIComponent(project)}`, { withCredentials: true });
    channel = {es, listeners}; channels.set(project, channel);
    let lastSeq = 0;
    es.onopen = () => { lastSeq = 0; listeners.forEach(fn => fn()); };
    es.onmessage = message => {
      try {
        const event = JSON.parse(message.data) as AppEvent;
        if (event.project_id !== project || !Number.isFinite(event.seq) || event.seq <= lastSeq) return;
        lastSeq = event.seq;
        listeners.forEach(fn => fn(event));
      } catch { /* Reconciliation recovers malformed or missing notifications. */ }
    };
  }
  channel.listeners.add(listener);
  return () => {
    channel!.listeners.delete(listener);
    if (!channel!.listeners.size) { channel!.es.close(); channels.delete(project); }
  };
}
export function useProcessEvents(props: LiveProps): LiveSnapshot {
  const [local, setLocal] = useState<LiveSnapshot>({eventRevision: 0, appEvents: []});
  useEffect(() => {
    if (!props.projectId || props.eventStreamManaged) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let events: AppEvent[] = [], all = false;
    const enqueue = (event?: AppEvent) => {
      if (event && (event.app !== 'processes' || (props.installId && event.install_id !== props.installId))) return;
      if (!event) all = true;
      else if (!all) events.push(event);
      if (events.length > 200) { all = true; events = []; }
      if (timer) return;
      timer = setTimeout(() => {
        timer = undefined;
        const batch = all ? [] : events;
        all = false; events = [];
        setLocal(s => ({eventRevision: s.eventRevision + 1, appEvents: batch}));
      }, 200);
    };
    const stop = subscribe(props.projectId, enqueue);
    const reconcile = () => { if (document.visibilityState !== 'hidden') enqueue(); };
    const poll = setInterval(reconcile, 30000);
    document.addEventListener('visibilitychange', reconcile);
    return () => { stop(); clearInterval(poll); clearTimeout(timer); document.removeEventListener('visibilitychange', reconcile); };
  }, [props.projectId, props.installId, props.eventStreamManaged]);
  return props.eventStreamManaged
    ? {eventRevision: props.eventRevision || 0, appEvents: props.appEvents || []}
    : local;
}
export function useScopedRevision(match: (e: AppEvent) => boolean, source?: LiveSnapshot) {
  const context = useContext(LiveContext);
  const snapshot = source || context;
  const revision = useRef(0);
  if (!snapshot.appEvents.length || snapshot.appEvents.some(match)) revision.current = snapshot.eventRevision;
  return revision.current;
}
