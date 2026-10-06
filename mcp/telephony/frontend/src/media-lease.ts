/** No credentials or media URLs are included in session diagnostics. */
export interface MediaSessionEvent {
  timestamp: string;
  action: string;
  outcome: string;
  status?: number;
  code?: string;
  remaining_ms?: number;
  detail?: string;
  was_clean?: boolean;
  duration_ms?: number;
}
export const leaseClock = () => performance.now();
export function mediaFailure(error: unknown): { status?: number; code?: string; denied: boolean; expired: boolean } {
  const value = error as {status?: number; body?: string};
  const status = value?.status;
  let code: string | undefined;
  try { const body = JSON.parse(value?.body ?? "{}"); if (typeof body.code === "string") code = body.code; } catch { /* Plain HTTP responses remain supported. */ }
  const expired = code === "media_lease_expired";
  return { status, code, expired, denied: !expired && [401, 403, 404, 410].includes(status!) };
}

export interface LeaseTimers {
  now(): number;
  setTimeout(callback: () => void, ms: number): ReturnType<typeof setTimeout>;
  clearTimeout(timer?: ReturnType<typeof setTimeout>): void;
}

/** An independent expiry watchdog also bounds a hung renewal request. */
export class MediaLease {
  private renewTimer?: ReturnType<typeof setTimeout>;
  private expiryTimer?: ReturnType<typeof setTimeout>;
  private requestTimer?: ReturnType<typeof setTimeout>;
  private stopped = false;
  private retryMS = 250;
  private deadline: number;
  constructor(private seconds: number, private renew: () => Promise<{lease_seconds?: number} | void>,
    private ended: (reason: "expired" | "revoked", error?: unknown) => void,
    private event?: (event: MediaSessionEvent) => void, startedMS = leaseClock(), private clock: LeaseTimers = {now:leaseClock,setTimeout:(callback,ms)=>setTimeout(callback,ms),clearTimeout:timer=>clearTimeout(timer)}) {
    // The server stores Unix seconds; reserve one second for truncation.
    this.deadline = startedMS + seconds * 1000 - 1000;
    this.watchExpiry(); this.schedule(Math.min(seconds * 1000 / 3, this.remaining()));
  }
  private remaining() { return Math.max(0, this.deadline - this.clock.now()); }
  private report(outcome: string, error?: unknown) {
    const {status,code} = mediaFailure(error);
    try { this.event?.({timestamp:new Date().toISOString(),action:"renew",outcome,
      status,code,remaining_ms:Math.round(this.remaining())}); } catch { /* host observers cannot interrupt media */ }
  }
  private watchExpiry() {
    this.clock.clearTimeout(this.expiryTimer);
    this.expiryTimer = this.clock.setTimeout(() => this.finish("expired"), this.remaining());
  }
  private schedule(ms: number) {
    this.renewTimer = this.clock.setTimeout(() => void this.tick(), Math.max(0, ms));
  }
  private async tick() {
    if (this.stopped) return;
    if (!this.remaining()) { this.finish("expired"); return; }
    const sent = this.clock.now();
    try {
      const result = await Promise.race([this.renew(), new Promise<never>((_, reject) => {
        this.requestTimer = this.clock.setTimeout(() => reject(new Error("Media renewal timed out")), Math.min(5000, this.remaining()));
      })]);
      if (this.stopped) return;
      if (result?.lease_seconds !== undefined) {
        if (!Number.isFinite(result.lease_seconds) || result.lease_seconds < 10 || result.lease_seconds > 3600) throw new Error("Invalid media renewal lease");
        this.seconds = result.lease_seconds;
      }
      this.deadline = sent + this.seconds * 1000 - 1000;
      this.retryMS = 250; this.watchExpiry(); this.report("renewed");
      this.schedule(Math.min(this.seconds * 1000 / 3, this.remaining()));
    } catch (error) {
      if (this.stopped) return;
      const failure = mediaFailure(error);
      if (failure.denied || failure.expired) { this.finish(failure.expired ? "expired" : "revoked", error); return; }
      this.report("retrying", error);
      this.schedule(Math.min(this.retryMS, this.remaining()));
      this.retryMS = Math.min(4000, this.retryMS * 2);
    } finally { this.clock.clearTimeout(this.requestTimer); this.requestTimer = undefined; }
  }
  private finish(reason: "expired" | "revoked", error?: unknown) {
    if (this.stopped) return;
    this.report(reason, error); this.stop(); this.ended(reason, error);
  }
  stop() {
    this.stopped = true;
    this.clock.clearTimeout(this.renewTimer); this.clock.clearTimeout(this.expiryTimer); this.clock.clearTimeout(this.requestTimer);
  }
}
