/** Coalesces bursts with bounded latency and one in-flight refresh. */
export function createRefreshQueue(
  task: () => Promise<unknown>,
  delay = 1000,
  schedule = setTimeout,
  cancel = clearTimeout,
) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let running = false,
    pending = false,
    disposed = false;
  const run = async () => {
    timer = undefined;
    if (disposed || running) return;
    pending = false;
    running = true;
    try {
      await task();
    } catch {
      /* task owns error presentation */
    } finally {
      running = false;
      if (pending && !disposed) request();
    }
  };
  const request = () => {
    if (disposed) return;
    pending = true;
    if (!running && timer === undefined) timer = schedule(run, delay);
  };
  return {
    request,
    dispose() {
      disposed = true;
      if (timer !== undefined) cancel(timer);
    },
  };
}
