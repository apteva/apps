/** One bounded summary of a browser audio recovery. Built only while recovering
 * and sent over HTTP, because a recovery usually means signaling is unavailable.
 * It holds states and timings only: no URL, token, device name or address. */
export type RecoveryStage = "requested" | "session" | "socket_open" | "configured" | "offer_sent" | "answered" | "media_connected" | "ready";
export type RecoveryOutcome = "connected" | "timed_out" | "revoked" | "cancelled" | "handed_over" | "handover_failed";
export interface RecoveryAttemptRecord {
  attempt_id: string;
  /** Since the recovery started. */
  at_ms: number;
  duration_ms?: number;
  /** Furthest stage this attempt reached. */
  reached: RecoveryStage;
  outcome?: "connected" | "failed";
  code?: string;
}
export interface RecoveryPathCheck { ok: boolean; ms: number }
export interface RecoveryEvidence {
  connection_state?: string;
  ice_state?: string;
  signaling_state?: string;
  last_packet_age_ms?: number;
  online?: boolean;
  visibility?: string;
  microphone?: string;
}
export interface MediaRecoveryRecord extends RecoveryEvidence {
  recovery_id: string;
  trigger: string;
  started_at: string;
  ended_at?: string;
  outcome?: RecoveryOutcome;
  /** From noticing the fault to audio restored. Audio stopped last_packet_age_ms earlier. */
  lost_ms?: number;
  checks?: { telephony?: RecoveryPathCheck; page?: RecoveryPathCheck };
  attempts: RecoveryAttemptRecord[];
  attempts_total: number;
}

const maxRecordedAttempts = 5;

export class RecoveryRecorder {
  readonly record: MediaRecoveryRecord;
  private started: number;
  private current?: RecoveryAttemptRecord;
  private currentStarted = 0;
  constructor(recoveryID: string, trigger: string, evidence: RecoveryEvidence, private now = () => performance.now(), timestamp = () => new Date().toISOString(), private endTimestamp = timestamp) {
    this.started = now();
    this.record = { ...evidence, recovery_id: recoveryID, trigger, started_at: timestamp(), attempts: [], attempts_total: 0 };
  }
  /** Keeps the first attempts, usually the informative ones, and the latest. */
  attempt(id: string): void {
    this.closeAttempt("failed");
    const entry: RecoveryAttemptRecord = { attempt_id: id, at_ms: Math.round(this.now() - this.started), reached: "requested" };
    this.record.attempts_total++;
    if (this.record.attempts.length < maxRecordedAttempts) this.record.attempts.push(entry);
    else this.record.attempts[maxRecordedAttempts - 1] = entry;
    this.current = entry;
    this.currentStarted = this.now();
  }
  stage(stage: RecoveryStage): void {
    if (this.current) this.current.reached = stage;
  }
  attemptFailed(code?: string): void { this.closeAttempt("failed", code); }
  check(name: "telephony" | "page", result: RecoveryPathCheck): void {
    (this.record.checks ??= {})[name] = result;
  }
  finish(outcome: RecoveryOutcome): MediaRecoveryRecord {
    if (this.record.outcome) return this.record;
    this.closeAttempt(outcome === "connected" || outcome === "handed_over" ? "connected" : "failed");
    this.record.outcome = outcome;
    this.record.ended_at = this.endTimestamp();
    if (outcome === "connected") this.record.lost_ms = Math.round(this.now() - this.started);
    return this.record;
  }
  private closeAttempt(outcome: "connected" | "failed", code?: string) {
    const entry = this.current;
    if (!entry) return;
    this.current = undefined;
    entry.duration_ms = Math.round(this.now() - this.currentStarted);
    entry.outcome = outcome;
    if (code) entry.code = code;
  }
}

/** Only that the host answered at all: opaque, uncredentialed and bounded. */
export async function recoveryPathCheck(url: string, timeoutMs = 3000, fetcher: typeof fetch = globalThis.fetch, now = () => performance.now()): Promise<RecoveryPathCheck> {
  const started = now(), controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    await fetcher(url, { method: "HEAD", mode: "no-cors", cache: "no-store", credentials: "omit", redirect: "manual", signal: controller.signal });
    return { ok: true, ms: Math.round(now() - started) };
  } catch {
    return { ok: false, ms: Math.round(now() - started) };
  } finally { clearTimeout(timer); }
}

/** One round per recovery: can this browser still reach Telephony and its own page host? */
export function runRecoveryChecks(recorder: RecoveryRecorder, mediaURL: string, pageOrigin: string | undefined, fetcher: typeof fetch = globalThis.fetch): Promise<void> {
  let telephony: string;
  try {
    const parsed = new URL(mediaURL);
    telephony = `${parsed.protocol === "wss:" || parsed.protocol === "https:" ? "https:" : "http:"}//${parsed.host}`;
  } catch { return Promise.resolve(); }
  const checks = [recoveryPathCheck(`${telephony}/health`, 3000, fetcher).then(result => recorder.check("telephony", result))];
  if (pageOrigin && pageOrigin !== telephony) checks.push(recoveryPathCheck(`${pageOrigin}/`, 3000, fetcher).then(result => recorder.check("page", result)));
  return Promise.all(checks).then(() => undefined, () => undefined);
}
