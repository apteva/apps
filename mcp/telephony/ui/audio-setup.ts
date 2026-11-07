import type { MediaSessionEvent } from "../frontend/src/media-lease";

/** Sparse setup observations only. No URLs, device IDs, tokens or error text. */
export async function measureAudioSetup<T>(action: string, emit: ((event: MediaSessionEvent) => void) | undefined, operation: () => Promise<T>): Promise<T> {
  const started = performance.now();
  const report = (outcome: string) => {
    try { emit?.({ timestamp: new Date().toISOString(), action, outcome, duration_ms: Math.round(Math.max(0, performance.now() - started)) }); }
    catch { /* Observers cannot interfere with device or transport setup. */ }
  };
  report("started");
  try { const result = await operation(); report("completed"); return result; }
  catch (error) { report("failed"); throw error; }
}
