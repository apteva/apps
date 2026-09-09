// SQLite legacy timestamps are UTC even though they have no zone suffix.
export function normalizeTimestamp(value: string): string {
  return /^\d{4}-\d{2}-\d{2} /.test(value) ? value.replace(" ", "T") + "Z" : value;
}

export function durationOf(r: { started_at: string; finished_at?: string; status: string }): string {
  if (!r.finished_at) return r.status === "running" ? "running…" : "—";
  try {
    const start = new Date(normalizeTimestamp(r.started_at)).getTime();
    const end = new Date(normalizeTimestamp(r.finished_at)).getTime();
    const ms = end - start;
    if (!Number.isFinite(ms) || ms < 0) return "—";
    if (ms < 1000) return `${ms} ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
    if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
    return `${(ms / 3_600_000).toFixed(1)} h`;
  } catch { return "—"; }
}

