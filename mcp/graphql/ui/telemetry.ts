type Log = Record<string, any>;
type Filters = Record<string, string>;
export function hasLogErrors(log: Log) { return Number(log.status_code) >= 400 || !!log.error || (log.errors?.length || 0) > 0 || (log.error_codes?.length || 0) > 0; }
export function formatBytes(value: number) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
export function telemetryQuery(filters: Filters, slowMS: number, now = new Date()) {
  const query = new URLSearchParams();
  Object.entries(filters).forEach(([key, value]) => {
    if (key !== "range" && key !== "since" && key !== "until" && value !== "") query.set(key, value);
  });
  if (filters.range === "custom") {
    for (const key of ["since", "until"]) if (filters[key]) query.set(key, new Date(filters[key]).toISOString());
  } else if (filters.range !== "all") {
    query.set("since", new Date(now.getTime() - Number(filters.range || 60) * 60_000).toISOString());
  }
  query.set("slow_threshold_ms", String(slowMS));
  return query;
}
