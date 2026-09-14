export interface TicketEvent { topic: string; seq: number; project_id?: string; install_id?: number }
type Listener = (event: TicketEvent) => void;
export interface EventHost {
  __aptevaAppEvents?: { subscribe(app: string, project: string, listener: Listener): () => void };
  EventSource: typeof EventSource;
}

// Prefer the dashboard's shared connection. Standalone panels get a resumable SSE stream.
export function subscribeTickets(host: EventHost, project: string, install: number, refresh: () => void) {
  const receive = (event: TicketEvent) => {
    if (event.project_id && event.project_id !== project) return;
    if (install && event.install_id && event.install_id !== install) return;
    if (event.topic.startsWith("ticket.")) refresh();
  };
  if (host.__aptevaAppEvents) return host.__aptevaAppEvents.subscribe("tickets", project, receive);
  let stopped = false, since = 0;
  let stream: EventSource;
  let retry: ReturnType<typeof setTimeout> | undefined;
  const connect = () => {
    if (stopped) return;
    stream = new host.EventSource(`/api/app-events/tickets?project_id=${encodeURIComponent(project)}&since=${since}`, { withCredentials: true });
    stream.onopen = refresh;
    stream.onmessage = message => {
      try {
        const event = JSON.parse(message.data) as TicketEvent;
        if (event.seq <= since) return;
        since = event.seq;
        receive(event);
      } catch { /* Ignore malformed envelopes. */ }
    };
    stream.onerror = () => {
      stream.close();
      retry = setTimeout(connect, 2000);
    };
  };
  connect();
  return () => { stopped = true; clearTimeout(retry); stream.close(); };
}

// Keep refreshes serial, combine bursts, and wait for local mutations to finish.
export function liveRefresh(refresh: () => Promise<void>, busy: () => boolean, delay = 150) {
  let pending = false, running = false, stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const run = async () => {
    timer = undefined;
    if (stopped || running || !pending) return;
    if (busy()) { timer = setTimeout(run, delay); return; }
    pending = false;
    running = true;
    try { await refresh(); } finally {
      running = false;
      if (pending && !stopped) timer = setTimeout(run, delay);
    }
  };
  return {
    request() { if (stopped) return; pending = true; if (!timer && !running) timer = setTimeout(run, delay); },
    stop() { stopped = true; clearTimeout(timer); },
  };
}

export function mergeDraft<T extends Record<string, string>>(current: T, previous: T, next: T): T {
  return Object.fromEntries(Object.keys(next).map(key => [key, current[key] === previous[key] ? next[key] : current[key]])) as T;
}
