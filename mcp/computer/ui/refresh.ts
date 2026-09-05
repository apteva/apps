import { useCallback, useEffect, useRef } from "react";

/** One request at a time; bursts request at most one trailing refresh. */
export function createRefreshQueue(task: (signal: AbortSignal) => Promise<void>) {
  const controller = new AbortController();
  let active: Promise<void> | null = null;
  let dirty = false;
  return {
    refresh(): Promise<void> {
      if (controller.signal.aborted) return Promise.resolve();
      dirty = true;
      if (!active) active = (async () => {
        do {
          dirty = false;
          await task(controller.signal);
        } while (dirty && !controller.signal.aborted);
      })().finally(() => { active = null; });
      return active;
    },
    dispose() { controller.abort(); dirty = false; },
  };
}

export function usePollingRefresh(task: (signal: AbortSignal) => Promise<void>, key: string | undefined, interval: number) {
  const taskRef = useRef(task);
  taskRef.current = task;
  const queueRef = useRef<ReturnType<typeof createRefreshQueue> | null>(null);
  useEffect(() => {
    const queue = createRefreshQueue((signal) => taskRef.current(signal));
    queueRef.current = queue;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try { await queue.refresh(); } catch { /* The task owns its error state. */ }
      finally { if (!cancelled && interval > 0) timer = setTimeout(poll, interval); }
    };
    void poll();
    return () => {
      cancelled = true; clearTimeout(timer); queue.dispose();
      if (queueRef.current === queue) queueRef.current = null;
    };
  }, [key, interval]);
  return useCallback(() => queueRef.current?.refresh() ?? Promise.resolve(), []);
}
