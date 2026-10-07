export interface AudioDashboardHost {
  appName?: string;
  installId?: number;
  projectId?: string;
  widgetSettings?: Record<string, unknown>;
  eventRevision?: number;
}
export const audioStages = {
  carrier_to_telephony: "Carrier → Telephony",
  telephony_to_browser: "Telephony → browser",
  browser_to_telephony: "Browser → Telephony",
};
export const audioIssues = {
  audio_degraded: "Audio degradation",
  dropped_audio: "Dropped audio",
  carrier_stall: "Carrier delivery gaps",
  sequence_gaps: "Sequence gaps",
  browser_error: "Browser / media errors",
  reconnect: "Reconnections",
  context_suspended: "AudioContext suspended",
  scheduling_pause: "Browser scheduling pauses",
  write_delay: "Socket write delay",
  high_rtt: "High browser RTT",
};
export type AudioFilters = {
  range: string;
  from: string;
  until: string;
  provider: string;
  adviser: string;
  destination: string;
  stage: string;
  issue: string;
  state: string;
  search: string;
};
export const defaultAudioFilters: AudioFilters = {
  range: "24h",
  from: "",
  until: "",
  provider: "",
  adviser: "",
  destination: "",
  stage: "",
  issue: "",
  state: "",
  search: "",
};
export interface AudioReport {
  call_id: string;
  provider: string;
  status: string;
  from: string;
  to: string;
  adviser: string;
  adviser_label: string;
  destination: string;
  active: boolean;
  state: string;
  observed_at: string;
  telemetry: boolean;
  issues: string[];
  stages: string[];
  metrics: Record<string, number>;
  health: {
    state?: string;
    stages?: Record<
      string,
      { state: string; signal?: string; last_bad_at?: string }
    >;
  };
}
export interface AudioAlert {
  provider: string;
  stage: string;
  state: string;
  call_count: number;
  call_ids: string[];
  occurred_at: string;
}
export interface AudioDashboardResponse {
  calls: AudioReport[];
  totals: {
    calls: number;
    active: number;
    affected: number;
    degraded: number;
    unobserved: number;
  };
  facets: Record<string, { value: string; label: string }[]>;
  alerts: AudioAlert[];
  next_cursor: string;
  pending_reports: number;
  generated_at: string;
  from: string;
  until: string;
}
export function audioDashboardURL(
  host: AudioDashboardHost,
  filters: AudioFilters,
  cursor = "",
  limit = 50,
  now = Date.now(),
): string {
  const q = new URLSearchParams();
  if (host.projectId) q.set("project_id", host.projectId);
  if (host.installId) q.set("install_id", String(host.installId));
  const hours = filters.range === "1h" ? 1 : filters.range === "7d" ? 168 : 24;
  const from =
    filters.range === "custom"
      ? Date.parse(filters.from)
      : now - hours * 3600000;
  const until = filters.range === "custom" ? Date.parse(filters.until) : now;
  if (
    !Number.isFinite(from) ||
    !Number.isFinite(until) ||
    from >= until ||
    until - from > 31 * 86400000
  )
    throw new Error("Choose a valid time range of at most 31 days.");
  q.set("from", new Date(from).toISOString());
  q.set("until", new Date(until).toISOString());
  q.set("limit", String(Math.min(100, Math.max(1, limit))));
  if (cursor) q.set("cursor", cursor);
  for (const k of [
    "provider",
    "adviser",
    "destination",
    "stage",
    "issue",
    "state",
    "search",
  ] as const)
    if (filters[k]) q.set(k, filters[k]);
  return `/api/apps/${encodeURIComponent(host.appName || "telephony")}/audio-health?${q}`;
}
export function audioDashboardLink(
  host: AudioDashboardHost,
  filters: Partial<AudioFilters> = {},
  callId = "",
): string {
  const q = new URLSearchParams({ tab: "audio-health" });
  if (host.projectId) q.set("project_id", host.projectId);
  if (host.installId) q.set("install_id", String(host.installId));
  for (const [k, v] of Object.entries(filters)) if (v) q.set(k, v);
  if (callId) q.set("audio_call_id", callId);
  return `/apps/${encodeURIComponent(host.appName || "telephony")}/page?${q}`;
}
export function initialAudioFilters(search: string): AudioFilters {
  const q = new URLSearchParams(search),
    f = { ...defaultAudioFilters };
  for (const k of Object.keys(f) as (keyof AudioFilters)[])
    if (q.has(k)) f[k] = q.get(k) || "";
  if (!["1h", "24h", "7d", "custom"].includes(f.range)) f.range = "24h";
  if (f.stage && !(f.stage in audioStages)) f.stage = "";
  if (f.issue && !(f.issue in audioIssues)) f.issue = "";
  if (!["", "issues", "degraded", "active", "unobserved"].includes(f.state))
    f.state = "";
  return f;
}
export function audioWidgetPreferences(value: Record<string, unknown> = {}) {
  return {
    range: ["1h", "24h", "7d"].includes(String(value.time_range))
      ? String(value.time_range)
      : "24h",
    maxCalls: Math.min(
      12,
      Math.max(3, Math.trunc(Number(value.max_calls) || 6)),
    ),
    provider: typeof value.provider === "string" ? value.provider : "",
  };
}
export const metricLabel: Record<string, string> = {
  playback_dropped_ms: "Browser playback dropped (ms)",
  playback_underruns: "Playback underruns",
  browser_queue_ms: "Browser queue (ms)",
  browser_max_queue_ms: "Max browser queue (ms)",
  rtt_ms: "Latest browser RTT (ms)",
  max_rtt_ms: "Max browser RTT (ms)",
  buffered_bytes: "WebSocket pending (bytes)",
  max_buffered_bytes: "Max WebSocket pending (bytes)",
  reconnects: "Server observed reconnections",
  connections: "Browser socket connections",
  disconnects: "Browser socket disconnects",
  reconnect_attempts: "Browser reconnect attempts",
  reconnect_successes: "Browser reconnect successes",
  worker_pauses: "Worker scheduling pauses",
  worker_max_tick_gap_ms: "Max Worker timer gap (ms)",
  main_thread_pauses: "Main-thread pauses",
  main_thread_max_pause_ms: "Max main-thread delay (ms)",
  context_suspensions: "AudioContext suspensions",
  context_suspended_ms: "AudioContext suspended (ms)",
  carrier_max_gap_ms: "Max carrier reception gap (ms)",
  carrier_stalls: "Carrier stalls",
  carrier_max_batch_ms: "Max carrier batch (ms)",
  carrier_max_age_ms: "Max carrier source age (ms)",
  carrier_source_dropped_ms: "Carrier source dropped (ms)",
  server_browser_dropped_ms: "Server → browser dropped (ms)",
  browser_transport_dropped_ms: "Browser transport dropped (ms)",
  browser_source_dropped_ms: "Browser source-age dropped (ms)",
  capture_dropped_ms: "Browser capture dropped (ms)",
  server_capture_dropped_ms: "Server capture dropped (ms)",
  carrier_send_dropped_ms: "Carrier send stale dropped (ms)",
  carrier_send_max_queue_ms: "Max carrier send queue (ms)",
  server_browser_queue_ms: "Server → browser queue (ms)",
  server_browser_max_queue_ms: "Max server → browser queue (ms)",
  browser_max_write_ms: "Max browser socket write (ms)",
  browser_max_queue_delay_ms: "Max browser queue residence (ms)",
  carrier_max_write_ms: "Max carrier send write (ms)",
  browser_transport_sequence_gaps: "Browser transport sequence gaps",
  capture_sequence_gaps: "Capture sequence gaps",
  capture_muted_frames: "Intentionally muted microphone frames",
  capture_muted_ms: "Intentional microphone mute (ms)",
  playback_sequence_gaps: "Playback sequence gaps",
  carrier_sequence_gaps: "Carrier sequence gaps",
};
export function audioStateLabel(state: string) {
  return (
    (
      {
        audio_degraded: "Degraded now",
        healthy: "Healthy at last report",
        ended: "Call ended",
        stale: "Telemetry stale",
        unobserved: "No telemetry",
        unavailable: "Health unavailable",
        inactive: "Browser audio inactive",
      } as Record<string, string>
    )[state] || state
  );
}
