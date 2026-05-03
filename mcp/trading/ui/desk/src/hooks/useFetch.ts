import { useEffect, useRef, useState } from "react";

// Generic poll-with-error-state hook. Re-runs `fetcher` every
// `intervalMs` (default 5s — matches the engine tick) and on each of
// the values in `deps` changing. Stale data stays visible while the
// next fetch is in flight; on error we set `error` but keep prior data.

export type FetchState<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  refresh: () => void;
};

export function useFetch<T>(
  fetcher: () => Promise<T>,
  deps: any[] = [],
  intervalMs: number = 5000,
): FetchState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const tick = useRef(0);
  const refresh = () => { tick.current++; };

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let run = async () => {
      try {
        const v = await fetcherRef.current();
        if (cancelled) return;
        setData(v);
        setError(null);
      } catch (e: any) {
        if (cancelled) return;
        setError(e?.message ?? String(e));
      } finally {
        if (!cancelled) setLoading(false);
        if (!cancelled && intervalMs > 0) {
          timer = setTimeout(run, intervalMs);
        }
      }
    };
    setLoading(true);
    run();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick.current]);

  return { data, error, loading, refresh };
}
