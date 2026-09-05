import { useCallback, useEffect, useRef, useState } from "react";

// Generic poll-with-error-state hook. Re-runs `fetcher` every
// `intervalMs` (default 5s — matches the engine tick) and on each of
// the values in `deps` changing. Stale data stays visible while the
// next fetch is in flight; on error we set `error` but keep prior data.

export type FetchState<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  refresh: () => void;
  updateData: (update: (current: T | null) => T | null) => void;
};

export function useFetch<T>(
  fetcher: () => Promise<T>,
  deps: any[] = [],
  intervalMs: number = 5000,
): FetchState<T> {
  const identity = JSON.stringify(deps);
  const [snapshot, setSnapshot] = useState<{ identity: string; value: T | null }>({ identity, value: null });
  const data = snapshot.identity === identity ? snapshot.value : null;
  const identityRef = useRef(identity); identityRef.current = identity;
  const setData = useCallback((value: T | null | ((current: T | null) => T | null)) => {
    const key = identityRef.current;
    setSnapshot((old) => ({ identity: key, value: typeof value === "function" ? (value as (v: T | null) => T | null)(old.identity === key ? old.value : null) : value }));
  }, []);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const [refreshSeq, setRefreshSeq] = useState(0);
  const refresh = useCallback(() => { setRefreshSeq((n) => n + 1); }, []);
  const updateData = useCallback((update: (current: T | null) => T | null) => setData(update), []);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let run = async () => {
      try {
        const v = await fetcherRef.current();
        if (cancelled) return;
        setSnapshot({ identity, value: v });
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
  }, [...deps, refreshSeq]);

  return { data, error: snapshot.identity === identity ? error : null, loading: snapshot.identity !== identity || loading, refresh, updateData };
}
