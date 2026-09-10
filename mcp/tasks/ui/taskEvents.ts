import { useEffect, useRef } from "react";

type Scope = { appName?: string; projectId?: string; installId?: number };
export interface TaskBusEvent {
  app: string;
  project_id: string;
  install_id: number;
  topic: string;
}
type Bridge = { subscribe(app: string, project: string, listener: (event: TaskBusEvent) => void): () => void };

// Reuse the dashboard's single SSE connection per project across app bundles.
// Standalone hosts use the same bus endpoint through one app-owned connection.
const channels = new Map<string, { source: EventSource; listeners: Set<(event?: TaskBusEvent) => void> }>();
function subscribe(scope: Scope, listener: (event?: TaskBusEvent) => void) {
  const app = scope.appName || "tasks", project = scope.projectId!;
  const bridge = (window as unknown as { __aptevaAppEvents?: Bridge }).__aptevaAppEvents;
  if (bridge) {
    const unsubscribe = bridge.subscribe(app, project, listener);
    const connected = (event: Event) => {
      if ((event as CustomEvent).detail?.projectId === project) listener();
    };
    window.addEventListener("apteva:app-events-connected", connected);
    return () => { unsubscribe(); window.removeEventListener("apteva:app-events-connected", connected); };
  }
  let channel = channels.get(project);
  if (!channel) {
    const source = new EventSource(`/api/app-events/_all?project_id=${encodeURIComponent(project)}`, { withCredentials: true });
    channel = { source, listeners: new Set() };
    const listeners = channel.listeners;
    source.onmessage = event => {
      try { const envelope = JSON.parse(event.data); for (const fn of listeners) fn(envelope); } catch { /* Ignore malformed frames. */ }
    };
    // A full refetch on reconnect also recovers gaps beyond the bus replay buffer.
    source.onopen = () => { for (const fn of listeners) fn(); };
    channels.set(project, channel);
  }
  channel.listeners.add(listener);
  return () => {
    channel!.listeners.delete(listener);
    if (!channel!.listeners.size) { channel!.source.close(); channels.delete(project); }
  };
}

export function useTaskEvents(scope: Scope, refresh: () => void | Promise<unknown>, enabled = true) {
  const callback = useRef(refresh);
  callback.current = refresh;
  useEffect(() => {
    if (!enabled || !scope.projectId) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let active = true, running = false, dirty = false;
    const flush = async () => {
      timer = undefined;
      if (running) { dirty = true; return; }
      running = true; dirty = false;
      try { await callback.current(); }
      finally {
        running = false;
        if (active && dirty && !timer) timer = setTimeout(flush, 100);
      }
    };
    const unsubscribe = subscribe(scope, event => {
      if (event && (event.app !== (scope.appName || "tasks") || event.project_id !== scope.projectId ||
        (scope.installId != null && event.install_id !== scope.installId) || !event.topic.startsWith("task."))) return;
      // Coalesce a burst without postponing refresh indefinitely during execution.
      if (running) dirty = true;
      else if (!timer) timer = setTimeout(flush, 100);
    });
    return () => { active = false; unsubscribe(); clearTimeout(timer); };
  }, [scope.appName, scope.projectId, scope.installId, enabled]);
}
