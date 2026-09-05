import { useCallback, useEffect, useRef } from "react";
import { createRefreshQueue } from "./live-refresh";
export function useLiveRefresh(task: () => Promise<unknown>, poll = 30000) {
  const latest = useRef(task);
  latest.current = task;
  const queue = useRef<ReturnType<typeof createRefreshQueue> | null>(null);
  useEffect(() => {
    const q = createRefreshQueue(() => latest.current());
    queue.current = q;
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") q.request();
    }, poll);
    return () => {
      q.dispose();
      clearInterval(timer);
      queue.current = null;
    };
  }, [poll]);
  return useCallback(() => queue.current?.request(), []);
}
