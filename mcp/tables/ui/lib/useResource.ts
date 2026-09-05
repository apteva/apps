import { useEffect, useRef, useState } from "react";

// One request per resource. Invalidations during a request coalesce into one
// follow-up; changing identity cancels the old request and hides its data.
export function useResource<T>(
  key: string,
  epoch: number,
  loader: (signal: AbortSignal) => Promise<T>,
) {
  const latest = useRef(loader);
  latest.current = loader;
  const reload = useRef<() => void>(() => {});
  const previousEpoch = useRef(epoch);
  const [state, setState] = useState<{
    key: string;
    data?: T;
    error?: string;
    busy: boolean;
  }>({ key: "", busy: false });
  useEffect(() => {
    const controller = new AbortController();
    let active = true,
      running = false,
      pending = false;
    const run = async () => {
      if (!key || !active) return;
      if (running) {
        pending = true;
        return;
      }
      running = true;
      setState((old) => ({
        key,
        data: old.key === key ? old.data : undefined,
        busy: true,
      }));
      try {
        const data = await latest.current(controller.signal);
        if (active) setState({ key, data, busy: false });
      } catch (error) {
        if (active && !controller.signal.aborted)
          setState((old) => ({
            key,
            data: old.key === key ? old.data : undefined,
            error: String((error as Error).message),
            busy: false,
          }));
      } finally {
        running = false;
        if (pending && active) {
          pending = false;
          void run();
        }
      }
    };
    reload.current = () => {
      void run();
    };
    void run();
    return () => {
      active = false;
      controller.abort();
    };
  }, [key]);
  useEffect(() => {
    if (epoch !== previousEpoch.current) {
      previousEpoch.current = epoch;
      reload.current();
    }
  }, [epoch]);
  return state.key === key
    ? state
    : { key, data: undefined, error: undefined, busy: !!key };
}
