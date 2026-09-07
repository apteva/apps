/** Bounded, opt-in gameplay telemetry. Use a Games guest access token; never a
 * platform token. Persist through your engine's storage adapter if desired. */
export interface PlayEvent {
  id: string;
  name:
    | "session_started"
    | "run_started"
    | "run_completed"
    | "run_failed"
    | "tutorial_completed"
    | "performance_summary";
  session_id: string;
  run_id?: string;
  release: string;
  environment: string;
  time: string;
  props: Record<string, string | number | boolean | null>;
}
export class GameTelemetry {
  private queue: PlayEvent[] = [];
  private flushing = false;
  constructor(
    private options: {
      endpoint: string;
      token: () => Promise<string>;
      enabled: boolean;
      persist?: (events: PlayEvent[]) => void;
      restored?: PlayEvent[];
      request?: typeof fetch;
    },
  ) {
    this.queue = (options.restored || [])
      .filter((e) => Date.parse(e.time) > Date.now() - 7 * 86400000)
      .slice(-500);
  }
  track(event: PlayEvent) {
    if (!this.options.enabled || this.queue.some((e) => e.id === event.id))
      return;
    this.queue.push(structuredClone(event));
    this.queue = this.queue.slice(-500);
    this.save();
  }
  private save() {
    this.options.persist?.(structuredClone(this.queue));
  }
  get pending() {
    return this.queue.length;
  }
  async flush() {
    if (!this.options.enabled || this.flushing || !this.queue.length) return;
    this.flushing = true;
    try {
      this.queue = this.queue.filter(
        (e) => Date.parse(e.time) > Date.now() - 7 * 86400000,
      );
      this.save();
      const batch = this.queue.slice(0, 50);
      if (!batch.length) return;
      const response = await (this.options.request || fetch)(
        this.options.endpoint,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${await this.options.token()}`,
          },
          body: JSON.stringify({ events: batch }),
          signal: AbortSignal.timeout(15000),
        },
      );
      if (!response.ok) throw new Error(`Telemetry HTTP ${response.status}`);
      const receipt = (await response.json()) as {
        accepted?: number;
        disabled?: boolean;
      };
      if (typeof receipt.accepted !== "number" && receipt.disabled !== true)
        throw new Error("Invalid telemetry receipt");
      const ids = new Set(batch.map((e) => e.id));
      this.queue = this.queue.filter((e) => !ids.has(e.id));
      this.save();
    } finally {
      this.flushing = false;
    }
  }
}
