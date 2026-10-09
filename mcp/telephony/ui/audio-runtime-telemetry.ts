import type { MediaSessionEvent } from "../frontend/src/media-lease";

/** Observations only: never changes DSP, socket ownership or call state. */
export class AudioRuntimeTelemetry {
  private lastTick?: number;
  private suspendedAt?: number;
  private contextState?: string;
  private environmentCleanup: (() => void)[] = [];
  readonly counters = {
    main_thread_pause_count: 0, main_thread_max_pause_ms: 0,
    audio_context_suspend_count: 0, audio_context_suspended_ms: 0,
  };
  constructor(private emit: (event: MediaSessionEvent) => void,
    private now = () => performance.now(), private timestamp = () => new Date().toISOString()) {}
  observeEnvironment(): void {
    this.stopEnvironment();
    // Events only; no polling, device enumeration, permissions or audio changes.
    const listen = (target: EventTarget | undefined, name: string, observe: () => void) => {
      if (!target) return;
      try {
        target.addEventListener(name, observe);
        this.environmentCleanup.push(() => target.removeEventListener(name, observe));
      } catch { /* Monitoring is optional in embedded/headless environments. */ }
    };
    try {
    if (typeof document !== "undefined") {
      listen(document, "visibilitychange", () => this.report("tab_visibility", document.visibilityState));
      for (const name of ["freeze", "resume"]) listen(document, name, () => this.report("page_lifecycle", name));
      this.report("tab_visibility", document.visibilityState);
    }
    if (typeof window !== "undefined") {
      for (const name of ["pagehide", "pageshow"]) listen(window, name, () => this.report("page_lifecycle", name));
    }
    if (typeof navigator !== "undefined") {
      listen(navigator.mediaDevices, "devicechange", () => this.report("microphone", "devices_changed"));
    }
    } catch { /* Restricted environment getters cannot affect audio startup. */ }
  }
  stopEnvironment(): void {
    for (const cleanup of this.environmentCleanup.splice(0)) {
      try { cleanup(); } catch { /* Cleanup must not prevent media cleanup. */ }
    }
  }
  tick(): void {
    const now = this.now();
    if (this.lastTick !== undefined) {
      const delayed = Math.max(0, now - this.lastTick - 1000);
      if (delayed >= 250) {
        this.counters.main_thread_pause_count++;
        this.counters.main_thread_max_pause_ms = Math.max(this.counters.main_thread_max_pause_ms, delayed);
        this.report("main_thread", "scheduling_gap", delayed);
      }
    }
    this.lastTick = now;
  }
  context(state: string): void {
    if (state === this.contextState) return;
    this.contextState = state;
    const now = this.now();
    if (state === "suspended" || state === "interrupted") {
      if (this.suspendedAt === undefined) {
        this.suspendedAt = now;
        this.counters.audio_context_suspend_count++;
      }
      this.report("audio_context", state);
    } else {
      let duration: number | undefined;
      if (this.suspendedAt !== undefined) {
        duration = Math.max(0, now - this.suspendedAt);
        this.counters.audio_context_suspended_ms += duration;
        this.suspendedAt = undefined;
      }
      this.report("audio_context", state, duration);
    }
  }
  private report(action: string, outcome: string, duration_ms?: number) {
    try { this.emit({timestamp:this.timestamp(), action, outcome, duration_ms: duration_ms === undefined ? undefined : Math.round(duration_ms)}); }
    catch { /* Diagnostic observers cannot interrupt audio. */ }
  }
}
