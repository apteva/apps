import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/telephony";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface RawCall {
  ID?: string;
  id?: string;
  ThreadID?: string;
  thread_id?: string;
  CarrierSID?: string;
  carrier_sid?: string;
  ToNumber?: string;
  to_number?: string;
  FromNumber?: string;
  from_number?: string;
  Directive?: string;
  directive?: string;
  Voice?: string;
  voice?: string;
  Status?: string;
  status?: string;
  PlacedAt?: string;
  placed_at?: string;
  AnsweredAt?: string;
  answered_at?: string;
  EndedAt?: string;
  ended_at?: string;
  ProjectID?: string;
  project_id?: string;
  ErrorMessage?: string;
  error_message?: string;
  RecordingMode?: string;
  recording_mode?: string;
  RecordingCount?: number;
  recording_count?: number;
  RecordingStatus?: string;
  recording_status?: string;
}

interface Call {
  id: string;
  threadId: string;
  carrierSid: string;
  toNumber: string;
  fromNumber: string;
  directive: string;
  voice: string;
  status: string;
  placedAt: string;
  answeredAt: string;
  endedAt: string;
  projectId: string;
  errorMessage: string;
  recordingMode: string;
  recordingCount: number;
  recordingStatus: string;
}

interface RecordingSettings {
  default_mode: "off" | "always";
  channels: "mono" | "dual";
  storage_mode: "copy_to_storage" | "copy_then_delete_provider";
  retention_days: number;
  carrier: string;
  recording_supported: boolean;
}

interface Recording {
  id: string;
  call_id: string;
  provider: string;
  provider_recording_id: string;
  provider_status: string;
  channels: number;
  format: string;
  duration_ms: number;
  size_bytes: number;
  storage_file_id: number;
  storage_status: string;
  import_attempts: number;
  last_error: string;
  provider_deleted: boolean;
  retention_expires_at: string;
  created_at: string;
  stored_at: string;
  playback_url?: string;
  playback_source?: "storage" | "provider";
}

const LIVE_STATUSES = new Set(["initiated", "ringing", "in-progress", "answered"]);
const TERMINAL_STATUSES = new Set(["completed", "failed", "no-answer", "busy", "canceled"]);

function normalizeCall(row: RawCall): Call {
  return {
    id: row.id ?? row.ID ?? "",
    threadId: row.thread_id ?? row.ThreadID ?? "",
    carrierSid: row.carrier_sid ?? row.CarrierSID ?? "",
    toNumber: row.to_number ?? row.ToNumber ?? "",
    fromNumber: row.from_number ?? row.FromNumber ?? "",
    directive: row.directive ?? row.Directive ?? "",
    voice: row.voice ?? row.Voice ?? "",
    status: row.status ?? row.Status ?? "",
    placedAt: row.placed_at ?? row.PlacedAt ?? "",
    answeredAt: row.answered_at ?? row.AnsweredAt ?? "",
    endedAt: row.ended_at ?? row.EndedAt ?? "",
    projectId: row.project_id ?? row.ProjectID ?? "",
    errorMessage: row.error_message ?? row.ErrorMessage ?? "",
    recordingMode: row.recording_mode ?? row.RecordingMode ?? "off",
    recordingCount: row.recording_count ?? row.RecordingCount ?? 0,
    recordingStatus: row.recording_status ?? row.RecordingStatus ?? "",
  };
}

function statusClass(status: string): string {
  if (status === "in-progress" || status === "answered") return "bg-success/10 text-success border-success/30";
  if (status === "ringing" || status === "initiated") return "bg-info/10 text-info border-info/30";
  if (status === "failed" || status === "busy" || status === "no-answer") return "bg-error/10 text-error border-error/30";
  if (status === "canceled") return "bg-warn/10 text-warn border-warn/30";
  return "bg-bg-muted text-text-muted border-border";
}

function recordingStatusLabel(status: string): string {
  switch (status) {
    case "stored": return "Ready";
    case "provider_only": return "Carrier cloud";
    case "importing": return "Copying to Storage";
    case "pending": return "Processing";
    case "failed": return "Storage copy failed";
    case "absent": return "Unavailable";
    default: return status || "Processing";
  }
}

function parseTime(iso: string): number {
  const value = new Date(iso).getTime();
  return Number.isFinite(value) ? value : 0;
}

function duration(call: Call, now: number): string {
  const start = parseTime(call.placedAt);
  if (!start) return "";
  const end = parseTime(call.endedAt) || now;
  const total = Math.max(0, Math.floor((end - start) / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  if (hours) return `${hours}h ${minutes}m`;
  if (minutes) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

function relative(iso: string, now: number): string {
  const time = parseTime(iso);
  if (!time) return "";
  const seconds = Math.max(0, Math.floor((now - time) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function compactId(value: string): string {
  if (!value) return "-";
  if (value.length <= 18) return value;
  return `${value.slice(0, 9)}...${value.slice(-6)}`;
}

function formatBytes(value: number): string {
  if (!value) return "-";
  if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDurationMS(value: number): string {
  const seconds = Math.max(0, Math.round((value || 0) / 1000));
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

function CallsView({ projectId }: NativePanelProps) {
  const [calls, setCalls] = useState<Call[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [ending, setEnding] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const [recordingSettings, setRecordingSettings] = useState<RecordingSettings | null>(null);
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [recordingBusy, setRecordingBusy] = useState("");

  const withProject = useCallback((path: string) => {
    if (!projectId) return `${API}${path}`;
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const loadCalls = useCallback(async () => {
    setLoading(true);
    try {
      const res = await fetch(withProject("/calls"), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const list = ((data.calls ?? []) as RawCall[]).map(normalizeCall);
      setCalls(list);
      setSelectedId((current) => current && list.some((c) => c.id === current)
        ? current
        : list[0]?.id ?? "");
      setStatus(`${list.length} calls`);
    } catch (e) {
      setStatus((e as Error).message || "Load failed");
    } finally {
      setLoading(false);
    }
  }, [withProject]);

  const loadRecordingSettings = useCallback(async () => {
    try {
      const res = await fetch(withProject("/recording-settings"), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      setRecordingSettings(await res.json() as RecordingSettings);
    } catch (e) {
      setStatus((e as Error).message || "Could not load recording settings");
    }
  }, [withProject]);

  const loadRecordings = useCallback(async (callId: string) => {
    if (!callId) {
      setRecordings([]);
      return;
    }
    try {
      const path = `/recordings/?call_id=${encodeURIComponent(callId)}`;
      const res = await fetch(withProject(path), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { recordings?: Recording[] };
      setRecordings(data.recordings ?? []);
    } catch (e) {
      setRecordings([]);
      setStatus((e as Error).message || "Could not load recordings");
    }
  }, [withProject]);

  useEffect(() => { void Promise.all([loadCalls(), loadRecordingSettings()]); }, [loadCalls, loadRecordingSettings]);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    const timer = window.setInterval(loadCalls, 10000);
    return () => window.clearInterval(timer);
  }, [loadCalls]);

  const selected = useMemo(
    () => calls.find((call) => call.id === selectedId) ?? calls[0] ?? null,
    [calls, selectedId],
  );

  useEffect(() => { void loadRecordings(selected?.id ?? ""); }, [selected?.id, loadRecordings]);

  useEffect(() => {
    if (!selected?.id || selected.recordingMode !== "always") return;
    const timer = window.setInterval(() => void loadRecordings(selected.id), 5000);
    return () => window.clearInterval(timer);
  }, [selected?.id, selected?.recordingMode, loadRecordings]);

  const activeCount = useMemo(
    () => calls.filter((call) => LIVE_STATUSES.has(call.status)).length,
    [calls],
  );

  const terminalCount = calls.length - activeCount;

  const hangup = async (call: Call) => {
    if (!call || !LIVE_STATUSES.has(call.status)) return;
    setEnding(call.id);
    try {
      const res = await fetch(withProject(`/calls/${encodeURIComponent(call.id)}/hangup`), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      setStatus("call ended");
      await loadCalls();
    } catch (e) {
      setStatus((e as Error).message || "Hangup failed");
    } finally {
      setEnding("");
    }
  };

  const toggleFutureRecording = async () => {
    if (!recordingSettings || !recordingSettings.recording_supported) return;
    setRecordingBusy("settings");
    try {
      const next = recordingSettings.default_mode === "always" ? "off" : "always";
      const res = await fetch(withProject("/recording-settings"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ default_mode: next }),
      });
      if (!res.ok) throw new Error(await res.text());
      setRecordingSettings(await res.json() as RecordingSettings);
      setStatus(next === "always" ? "Future calls will be recorded" : "Future call recording disabled");
    } catch (e) {
      setStatus((e as Error).message || "Could not update recording settings");
    } finally {
      setRecordingBusy("");
    }
  };

  const recordingAction = async (recording: Recording, action: "retry" | "delete") => {
    if (action === "delete") {
      const location = recording.storage_file_id > 0 ? "Storage and the carrier" : "the carrier";
      if (!window.confirm(`Delete this recording from ${location}?`)) return;
    }
    setRecordingBusy(recording.id);
    try {
      const res = await fetch(withProject(`/recordings/${encodeURIComponent(recording.id)}/${action}`), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      await loadRecordings(recording.call_id);
      setStatus(action === "delete" ? "Recording deleted" : "Storage copy queued");
    } catch (e) {
      setStatus((e as Error).message || `Recording ${action} failed`);
    } finally {
      setRecordingBusy("");
    }
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="text-sm font-semibold leading-5">Calls</h1>
          <p className="text-xs text-text-muted">
            {activeCount} active / {terminalCount} recent
          </p>
        </div>
        <div className="text-xs text-text-muted truncate max-w-[16rem]">{status}</div>
        {recordingSettings ? (
          <label className={`h-8 flex items-center gap-2 px-2 border border-border rounded text-xs ${recordingSettings.recording_supported ? "cursor-pointer" : "opacity-50"}`} title={recordingSettings.recording_supported ? `Record future ${recordingSettings.carrier} calls` : `Recording is not yet available for ${recordingSettings.carrier}`}>
            <input
              type="checkbox"
              checked={recordingSettings.default_mode === "always"}
              disabled={!recordingSettings.recording_supported || recordingBusy === "settings"}
              onChange={() => void toggleFutureRecording()}
            />
            Record future calls
          </label>
        ) : null}
        <button
          type="button"
          onClick={loadCalls}
          disabled={loading}
          className="h-8 px-3 rounded border border-border text-xs hover:bg-bg-muted disabled:opacity-50"
        >
          Refresh
        </button>
      </header>

      <main className="min-h-0 flex-1 grid grid-cols-1 lg:grid-cols-[minmax(0,1.2fr)_minmax(20rem,0.8fr)]">
        <section className="min-h-0 border-b lg:border-b-0 lg:border-r border-border overflow-auto">
          {calls.length === 0 ? (
            <div className="h-full min-h-[18rem] flex items-center justify-center text-sm text-text-muted">
              No calls yet.
            </div>
          ) : (
            <div className="min-w-[54rem]">
              <div className="grid grid-cols-[8rem_1fr_1fr_8rem_7rem_6rem_7rem] gap-3 px-4 py-2 border-b border-border text-[11px] uppercase tracking-normal text-text-dim">
                <div>Status</div>
                <div>To</div>
                <div>From</div>
                <div>Started</div>
                <div>Duration</div>
                <div>Voice</div>
                <div>Recording</div>
              </div>
              {calls.map((call) => {
                const picked = selected?.id === call.id;
                return (
                  <button
                    key={call.id}
                    type="button"
                    onClick={() => setSelectedId(call.id)}
                    className={`w-full grid grid-cols-[8rem_1fr_1fr_8rem_7rem_6rem_7rem] gap-3 px-4 py-3 text-left border-b border-border/70 hover:bg-bg-muted/70 ${picked ? "bg-bg-muted" : ""}`}
                  >
                    <div>
                      <span className={`inline-flex max-w-full items-center rounded border px-2 py-0.5 text-xs ${statusClass(call.status)}`}>
                        <span className="truncate">{call.status || "unknown"}</span>
                      </span>
                    </div>
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{call.toNumber || "-"}</div>
                      <div className="truncate text-xs text-text-dim">{compactId(call.id)}</div>
                    </div>
                    <div className="min-w-0 truncate text-sm text-text-muted">{call.fromNumber || "-"}</div>
                    <div className="text-sm text-text-muted">{relative(call.placedAt, now) || "-"}</div>
                    <div className="text-sm tabular-nums">{duration(call, now) || "-"}</div>
                    <div className="truncate text-sm text-text-muted">{call.voice || "-"}</div>
                    <div className="truncate text-sm text-text-muted">
                      {call.recordingStatus ? recordingStatusLabel(call.recordingStatus) : call.recordingMode === "always" ? "Enabled" : "Off"}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </section>

        <aside className="min-h-0 overflow-auto">
          {selected ? (
            <div className="p-4 space-y-5">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="text-xs text-text-dim">Selected call</div>
                  <div className="mt-1 text-lg font-semibold truncate">{selected.toNumber || selected.id}</div>
                  <div className="mt-1 text-xs text-text-muted truncate">{selected.threadId}</div>
                </div>
                <button
                  type="button"
                  disabled={!LIVE_STATUSES.has(selected.status) || ending === selected.id}
                  onClick={() => hangup(selected)}
                  className="h-8 px-3 rounded bg-error text-bg text-xs font-medium disabled:opacity-40"
                >
                  Hang up
                </button>
              </div>

              <dl className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-x-3 gap-y-3 text-sm">
                <dt className="text-text-dim">Status</dt>
                <dd>
                  <span className={`inline-flex rounded border px-2 py-0.5 text-xs ${statusClass(selected.status)}`}>
                    {selected.status || "unknown"}
                  </span>
                </dd>
                <dt className="text-text-dim">Duration</dt>
                <dd className="tabular-nums">{duration(selected, now) || "-"}</dd>
                <dt className="text-text-dim">Placed</dt>
                <dd className="truncate">{selected.placedAt || "-"}</dd>
                <dt className="text-text-dim">Ended</dt>
                <dd className="truncate">{selected.endedAt || (TERMINAL_STATUSES.has(selected.status) ? "-" : "live")}</dd>
                <dt className="text-text-dim">From</dt>
                <dd className="truncate">{selected.fromNumber || "-"}</dd>
                <dt className="text-text-dim">Carrier SID</dt>
                <dd className="truncate font-mono text-xs">{selected.carrierSid || "-"}</dd>
                <dt className="text-text-dim">Call ID</dt>
                <dd className="truncate font-mono text-xs">{selected.id}</dd>
              </dl>

              <section className="border-t border-border pt-4">
                <div className="mb-3 flex items-center justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold">Recordings</h3>
                    <p className="text-xs text-text-muted">
                      {selected.recordingMode === "always" ? "Captured by the carrier. Storage persistence is optional." : "Recording was not enabled for this call."}
                    </p>
                  </div>
                  <button type="button" onClick={() => void loadRecordings(selected.id)} className="h-8 px-3 rounded border border-border text-xs hover:bg-bg-muted">Refresh</button>
                </div>
                {recordings.length === 0 ? (
                  <div className="py-4 text-sm text-text-muted">
                    {selected.recordingMode === "always" && !TERMINAL_STATUSES.has(selected.status)
                      ? "Recording is in progress."
                      : selected.recordingMode === "always"
                        ? "The carrier is still processing the recording."
                        : "No recording."}
                  </div>
                ) : recordings.map((recording) => (
                  <div key={recording.id} className="border-t border-border py-3 first:border-t-0 first:pt-0">
                    <div className="flex items-start gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium">{recordingStatusLabel(recording.storage_status)}</span>
                          <span className="text-xs text-text-dim">{formatDurationMS(recording.duration_ms)}{recording.size_bytes > 0 ? ` / ${formatBytes(recording.size_bytes)}` : ""} / {recording.channels} channel{recording.channels === 1 ? "" : "s"}</span>
                        </div>
                        <div className="mt-1 truncate font-mono text-[11px] text-text-dim">{recording.provider_recording_id}</div>
                      </div>
                      {recording.storage_status === "failed" || recording.storage_status === "provider_only" ? (
                        <button type="button" disabled={recordingBusy === recording.id} onClick={() => void recordingAction(recording, "retry")} className="h-8 px-3 rounded border border-border text-xs disabled:opacity-50">{recording.storage_status === "provider_only" ? "Copy to Storage" : "Retry"}</button>
                      ) : null}
                      <button type="button" disabled={recordingBusy === recording.id} onClick={() => void recordingAction(recording, "delete")} className="h-8 px-3 rounded border border-error/40 text-error text-xs disabled:opacity-50">Delete</button>
                    </div>
                    {recording.playback_url ? (
                      <div className="mt-3 flex items-center gap-3">
                        <audio src={recording.playback_url} controls preload="metadata" className="min-w-0 flex-1 h-9" />
                        <a href={recording.playback_url} download className="text-xs text-accent hover:underline">Download</a>
                        <span className="text-[11px] text-text-dim">{recording.playback_source === "storage" ? "Storage" : "Carrier"}</span>
                      </div>
                    ) : null}
                    {recording.last_error ? <div className="mt-2 text-xs text-error whitespace-pre-wrap">{recording.last_error}</div> : null}
                  </div>
                ))}
              </section>

              <div>
                <div className="mb-2 text-xs font-medium text-text-muted">Directive</div>
                <div className="max-h-48 overflow-auto rounded border border-border bg-bg-muted/40 p-3 text-sm leading-6 whitespace-pre-wrap">
                  {selected.directive || "-"}
                </div>
              </div>

              {selected.errorMessage ? (
                <div className="rounded border border-error/30 bg-error/10 p-3 text-sm text-error">
                  {selected.errorMessage}
                </div>
              ) : null}
            </div>
          ) : (
            <div className="h-full min-h-[14rem] flex items-center justify-center text-sm text-text-muted">
              Select a call.
            </div>
          )}
        </aside>
      </main>
    </div>
  );
}

interface NumberOffer {
  confirmation_token?: string;
  expires_at?: string;
  provider: string;
  phone_number: string;
  country: string;
  number_type: string;
  friendly_name?: string;
  locality?: string;
  region?: string;
  features?: string[];
  monthly_price?: string;
  upfront_price?: string;
  inbound_price?: string;
  currency?: string;
  address_requirement?: string;
  requirements_met?: boolean;
  compliance_required?: boolean;
  purchase_ready: boolean;
  purchase_blocker?: string;
}

interface NumberSearchResponse {
  provider: string;
  supported: boolean;
  purchase_supported: boolean;
  supported_number_types?: string[];
  reason?: string;
  offers?: NumberOffer[];
  offer_count?: number;
  pricing_note?: string;
}

interface ProviderAddress {
  sid: string;
  friendly_name?: string;
  customer_name?: string;
  street?: string;
  city?: string;
  region?: string;
  postal_code?: string;
  iso_country?: string;
  validated?: boolean;
  verified?: boolean;
}

interface RegulatoryBundle {
  sid: string;
  friendly_name?: string;
  status?: string;
  regulation_sid?: string;
  email?: string;
  valid_until?: string;
  date_updated?: string;
}

async function postJSON<T>(url: string, body: Record<string, unknown>): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return await res.json() as T;
}

function money(value?: string, currency?: string, suffix = ""): string {
  if (!value) return "-";
  const amount = Number(value);
  const rendered = Number.isFinite(amount)
    ? amount.toLocaleString(undefined, { maximumFractionDigits: 4 })
    : value;
  return `${currency ? `${currency} ` : ""}${rendered}${suffix}`;
}

function NumbersView({ projectId }: NativePanelProps) {
  const [countries, setCountries] = useState("EE, AT");
  const [numberType, setNumberType] = useState("local");
  const [offers, setOffers] = useState<NumberOffer[]>([]);
  const [provider, setProvider] = useState("");
  const [supportedTypes, setSupportedTypes] = useState<string[]>([]);
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<NumberOffer | null>(null);
  const [addressSid, setAddressSid] = useState("");
  const [bundleSid, setBundleSid] = useState("");
  const [addresses, setAddresses] = useState<ProviderAddress[]>([]);
  const [bundles, setBundles] = useState<RegulatoryBundle[]>([]);
  const [resourcesLoading, setResourcesLoading] = useState(false);
  const [purchasing, setPurchasing] = useState(false);
  const [purchaseResult, setPurchaseResult] = useState("");

  const endpoint = useCallback((path: string) => {
    const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return `${API}${path}${query}`;
  }, [projectId]);

  const search = async () => {
    const values = countries
      .split(",")
      .map((value) => value.trim().toUpperCase())
      .filter(Boolean);
    if (values.length === 0) {
      setStatus("Enter at least one country code");
      return;
    }
    setLoading(true);
    setSelected(null);
    setAddressSid("");
    setBundleSid("");
    setPurchaseResult("");
    try {
      const data = await postJSON<NumberSearchResponse>(endpoint("/numbers/search"), {
        countries: values, number_type: numberType, features: ["voice"], limit: 10,
      });
      setProvider(data.provider || "");
      setSupportedTypes(data.supported_number_types ?? []);
      setOffers(data.offers ?? []);
      setStatus(data.supported
        ? `${data.offer_count ?? data.offers?.length ?? 0} offers`
        : data.reason || "Number search is not supported by this carrier");
    } catch (e) {
      setOffers([]);
      setStatus((e as Error).message || "Search failed");
    } finally {
      setLoading(false);
    }
  };

  const review = async (offer: NumberOffer) => {
    setSelected(offer);
    setAddressSid("");
    setBundleSid("");
    setAddresses([]);
    setBundles([]);
    if (offer.provider !== "twilio" && offer.provider !== "telnyx") return;
    setResourcesLoading(true);
    try {
      const [addressData, profileData] = await Promise.all([
        postJSON<{ addresses?: ProviderAddress[] }>(endpoint("/numbers/addresses/list"), { limit: 200 }),
        postJSON<{ profiles?: RegulatoryBundle[]; bundles?: RegulatoryBundle[] }>(endpoint("/numbers/regulatory/bundles/list"), {
          country: offer.country, ...(offer.provider === "twilio" ? { status: "twilio-approved" } : {}), limit: 200,
        }),
      ]);
      setAddresses(addressData.addresses ?? []);
      setBundles(profileData.profiles ?? profileData.bundles ?? []);
    } catch (e) {
      setPurchaseResult((e as Error).message || "Could not load provider compliance resources");
    } finally {
      setResourcesLoading(false);
    }
  };

  const purchase = async () => {
    if (!selected?.confirmation_token) return;
    setPurchasing(true);
    try {
      const data = await postJSON<Record<string, string>>(endpoint("/numbers/purchase"), {
        confirmation_token: selected.confirmation_token,
        ...(addressSid.trim() ? { address_id: addressSid.trim() } : {}),
        ...(bundleSid.trim() ? { compliance_id: bundleSid.trim() } : {}),
      });
      setPurchaseResult(`${data.phone_number || selected.phone_number} purchased through ${data.provider || selected.provider}`);
      setSelected(null);
      setStatus("Purchase completed");
    } catch (e) {
      setPurchaseResult((e as Error).message || "Purchase failed");
    } finally {
      setPurchasing(false);
    }
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex flex-wrap items-end gap-3">
        <label className="min-w-[12rem] flex-1 max-w-[28rem]">
          <span className="block text-xs text-text-muted mb-1">Countries</span>
          <input
            value={countries}
            onChange={(event) => setCountries(event.target.value)}
            placeholder="EE, AT, DE"
            className="h-9 w-full rounded border border-border bg-bg px-3 text-sm outline-none focus:border-text-dim"
          />
        </label>
        <label className="w-40">
          <span className="block text-xs text-text-muted mb-1">Number type</span>
          <select
            value={numberType}
            onChange={(event) => setNumberType(event.target.value)}
            className="h-9 w-full rounded border border-border bg-bg px-2 text-sm outline-none focus:border-text-dim"
          >
            <option value="local">Local</option>
            <option value="mobile">Mobile</option>
            <option value="national">National</option>
            <option value="toll_free">Toll-free</option>
            <option value="any">Any</option>
          </select>
        </label>
        <button
          type="button"
          onClick={search}
          disabled={loading}
          className="h-9 px-4 rounded bg-accent text-bg text-sm font-medium disabled:opacity-50"
        >
          {loading ? "Searching..." : "Search"}
        </button>
        <div className="min-w-[10rem] text-right text-xs text-text-muted">
          <div>{provider ? `Carrier: ${provider}` : ""}</div>
          <div className="truncate">{status}</div>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-auto">
        {supportedTypes.length > 0 && numberType !== "any" && !supportedTypes.includes(numberType) ? (
          <div className="border-b border-warn/30 bg-warn/10 px-4 py-2 text-xs text-warn">
            {provider} supports {supportedTypes.join(", ")} number searches.
          </div>
        ) : null}
        {purchaseResult ? (
          <div className="border-b border-border px-4 py-3 text-sm">{purchaseResult}</div>
        ) : null}
        {offers.length === 0 ? (
          <div className="min-h-[18rem] flex items-center justify-center px-6 text-center text-sm text-text-muted">
            {status || "Search the bound carrier's live number inventory."}
          </div>
        ) : (
          <div className="min-w-[64rem]">
            <div className="grid grid-cols-[10rem_7rem_1fr_9rem_9rem_9rem_9rem_7rem] gap-3 px-4 py-2 border-b border-border text-[11px] uppercase tracking-normal text-text-dim">
              <div>Number</div>
              <div>Country</div>
              <div>Location</div>
              <div>Monthly</div>
              <div>Inbound</div>
              <div>Setup</div>
              <div>Requirement</div>
              <div />
            </div>
            {offers.map((offer) => (
              <div
                key={`${offer.provider}-${offer.phone_number}`}
                className="grid grid-cols-[10rem_7rem_1fr_9rem_9rem_9rem_9rem_7rem] gap-3 items-center px-4 py-3 border-b border-border/70 text-sm"
              >
                <div className="font-medium truncate">{offer.phone_number}</div>
                <div>
                  <div>{offer.country}</div>
                  <div className="text-xs text-text-dim">{offer.number_type.replace("_", " ")}</div>
                </div>
                <div className="min-w-0">
                  <div className="truncate">{[offer.locality, offer.region].filter(Boolean).join(", ") || "-"}</div>
                  <div className="truncate text-xs text-text-dim">{(offer.features ?? []).join(", ")}</div>
                </div>
                <div className="tabular-nums">{money(offer.monthly_price, offer.currency, "/mo")}</div>
                <div className="tabular-nums">{money(offer.inbound_price, offer.currency, "/min")}</div>
                <div className="tabular-nums">{money(offer.upfront_price, offer.currency)}</div>
                <div className="truncate text-xs text-text-muted" title={offer.purchase_blocker || offer.address_requirement}>
                  {offer.purchase_blocker || (offer.compliance_required ? "compliance profile" : offer.address_requirement) || "none stated"}
                </div>
                <button
                  type="button"
                  onClick={() => review(offer)}
                  disabled={!offer.purchase_ready}
                  title={offer.purchase_blocker || "Review purchase"}
                  className="h-8 px-3 rounded border border-border text-xs hover:bg-bg-muted disabled:opacity-40"
                >
                  Review
                </button>
              </div>
            ))}
          </div>
        )}
      </main>

      {selected ? (
        <section className="shrink-0 border-t border-border bg-bg-muted/40 px-4 py-3 flex flex-wrap items-center gap-4">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-semibold">Confirm purchase of {selected.phone_number}</div>
            <div className="mt-1 text-xs text-text-muted">
              {money(selected.monthly_price, selected.currency, "/month")}
              {selected.upfront_price ? ` + ${money(selected.upfront_price, selected.currency)} setup` : ""}
              {selected.inbound_price ? `; ${money(selected.inbound_price, selected.currency, "/minute inbound")}` : ""}
              {selected.address_requirement ? `; address requirement: ${selected.address_requirement}` : ""}
            </div>
            {selected.provider === "twilio" || selected.provider === "telnyx" ? (
              <div className="mt-3 grid max-w-3xl gap-3 md:grid-cols-2">
                {selected.provider === "twilio" ? <label>
                  <span className="mb-1 block text-xs text-text-muted">Address</span>
                  <select
                    value={addressSid}
                    onChange={(event) => setAddressSid(event.target.value)}
                    className="h-9 w-full rounded border border-border bg-bg px-2 text-sm outline-none focus:border-text-dim"
                  >
                    <option value="">{resourcesLoading ? "Loading addresses..." : "No address selected"}</option>
                    {addresses.map((address) => (
                      <option key={address.sid} value={address.sid}>
                        {address.friendly_name || address.customer_name || address.sid} ({address.iso_country || "--"})
                      </option>
                    ))}
                  </select>
                </label> : <div />}
                <label>
                  <span className="mb-1 block text-xs text-text-muted">Compliance profile</span>
                  <select
                    value={bundleSid}
                    onChange={(event) => setBundleSid(event.target.value)}
                    className="h-9 w-full rounded border border-border bg-bg px-2 text-sm outline-none focus:border-text-dim"
                  >
                    <option value="">{resourcesLoading ? "Loading profiles..." : "No profile selected"}</option>
                    {bundles.map((bundle) => (
                      <option key={bundle.sid} value={bundle.sid}>{bundle.friendly_name || bundle.sid}</option>
                    ))}
                  </select>
                </label>
              </div>
            ) : null}
          </div>
          <button
            type="button"
            onClick={() => setSelected(null)}
            disabled={purchasing}
            className="h-9 px-4 rounded border border-border text-sm disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={purchase}
            disabled={purchasing || Boolean(
              selected.provider === "twilio" &&
              ((selected.address_requirement && selected.address_requirement !== "none" && !/^AD[0-9a-fA-F]{32}$/.test(addressSid.trim())) ||
              (bundleSid.trim() && !/^BU[0-9a-fA-F]{32}$/.test(bundleSid.trim()))) ||
              (selected.compliance_required && !bundleSid.trim())
            )}
            className="h-9 px-4 rounded bg-error text-bg text-sm font-medium disabled:opacity-50"
          >
            {purchasing ? "Purchasing..." : "Confirm purchase"}
          </button>
        </section>
      ) : null}
    </div>
  );
}

function AddressesView({ projectId }: NativePanelProps) {
  const [addresses, setAddresses] = useState<ProviderAddress[]>([]);
  const [status, setStatus] = useState("");
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState({
    customer_name: "", friendly_name: "", street: "", street_secondary: "",
    city: "", region: "", postal_code: "", country: "",
  });
  const endpoint = useCallback((path: string) => {
    const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return `${API}${path}${query}`;
  }, [projectId]);
  const load = useCallback(async () => {
    try {
      const data = await postJSON<{ addresses?: ProviderAddress[] }>(endpoint("/numbers/addresses/list"), { limit: 500 });
      setAddresses(data.addresses ?? []);
      setStatus(`${data.addresses?.length ?? 0} addresses`);
    } catch (e) {
      setStatus((e as Error).message || "Could not load addresses");
    }
  }, [endpoint]);
  useEffect(() => { void load(); }, [load]);

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      await postJSON(endpoint("/numbers/addresses/create"), { ...form, auto_correct: true });
      setForm({ ...form, customer_name: "", friendly_name: "", street: "", street_secondary: "", city: "", region: "", postal_code: "" });
      setStatus("Address created");
      await load();
    } catch (e) {
      setStatus((e as Error).message || "Address creation failed");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="h-full min-h-0 grid lg:grid-cols-[minmax(0,1fr)_22rem] bg-bg text-text">
      <section className="min-h-0 overflow-auto border-r border-border">
        <header className="h-12 px-4 border-b border-border flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">Provider addresses</h2>
          <span className="truncate text-xs text-text-muted">{status}</span>
        </header>
        <div className="min-w-[48rem]">
          <div className="grid grid-cols-[10rem_1fr_8rem_9rem_12rem] gap-3 px-4 py-2 border-b border-border text-[11px] uppercase text-text-dim">
            <div>Name</div><div>Address</div><div>Country</div><div>Validation</div><div>SID</div>
          </div>
          {addresses.map((address) => (
            <div key={address.sid} className="grid grid-cols-[10rem_1fr_8rem_9rem_12rem] gap-3 px-4 py-3 border-b border-border/70 text-sm">
              <div className="truncate">{address.friendly_name || address.customer_name || "-"}</div>
              <div className="truncate text-text-muted">{[address.street, address.city, address.region, address.postal_code].filter(Boolean).join(", ")}</div>
              <div>{address.iso_country || "-"}</div>
              <div>{address.validated ? "Validated" : address.verified ? "Verified" : "Unverified"}</div>
              <div className="truncate font-mono text-xs">{address.sid}</div>
            </div>
          ))}
          {addresses.length === 0 ? <div className="p-8 text-center text-sm text-text-muted">No addresses</div> : null}
        </div>
      </section>
      <form onSubmit={create} className="min-h-0 overflow-auto p-4 space-y-3">
        <h2 className="text-sm font-semibold">New address</h2>
        <Field label="Person or business name" value={form.customer_name} onChange={(value) => setForm({ ...form, customer_name: value })} required />
        <Field label="Label" value={form.friendly_name} onChange={(value) => setForm({ ...form, friendly_name: value })} />
        <Field label="Street" value={form.street} onChange={(value) => setForm({ ...form, street: value })} required />
        <Field label="Street secondary" value={form.street_secondary} onChange={(value) => setForm({ ...form, street_secondary: value })} />
        <div className="grid grid-cols-2 gap-3">
          <Field label="City" value={form.city} onChange={(value) => setForm({ ...form, city: value })} required />
          <Field label="Region" value={form.region} onChange={(value) => setForm({ ...form, region: value })} required />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Postal code" value={form.postal_code} onChange={(value) => setForm({ ...form, postal_code: value })} required />
          <Field label="Country" value={form.country} onChange={(value) => setForm({ ...form, country: value.toUpperCase().slice(0, 2) })} required />
        </div>
        <button type="submit" disabled={saving} className="h-9 w-full rounded bg-accent text-bg text-sm font-medium disabled:opacity-50">
          {saving ? "Creating..." : "Create address"}
        </button>
      </form>
    </div>
  );
}

interface BundleDetails {
  bundle?: RegulatoryBundle;
  regulation?: Record<string, unknown>;
  items?: Array<Record<string, unknown>>;
}

function BundlesView({ projectId }: NativePanelProps) {
  const [bundles, setBundles] = useState<RegulatoryBundle[]>([]);
  const [provider, setProvider] = useState("");
  const [selected, setSelected] = useState<BundleDetails | null>(null);
  const [requirements, setRequirements] = useState<unknown>(null);
  const [result, setResult] = useState<unknown>(null);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [bundleForm, setBundleForm] = useState({
    country: "EE", number_type: "national", end_user_type: "individual", friendly_name: "Estonia national", email: "",
  });
  const [itemForm, setItemForm] = useState({ kind: "end_user", friendly_name: "", type: "individual", attributes: "{}", requirement_id: "", field_value: "" });
  const [itemFile, setItemFile] = useState<File | null>(null);
  const endpoint = useCallback((path: string) => {
    const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return `${API}${path}${query}`;
  }, [projectId]);
  const load = useCallback(async () => {
    try {
      const data = await postJSON<{ provider?: string; profiles?: RegulatoryBundle[]; bundles?: RegulatoryBundle[] }>(endpoint("/numbers/regulatory/bundles/list"), { limit: 500 });
      const profiles = data.profiles ?? data.bundles ?? [];
      setProvider(data.provider ?? "");
      setBundles(profiles);
      setStatus(`${profiles.length} profiles`);
    } catch (e) {
      setStatus((e as Error).message || "Could not load bundles");
    }
  }, [endpoint]);
  useEffect(() => { void load(); }, [load]);

  const inspect = async (bundleSid: string, preserveResult = false) => {
    setBusy(true);
    try {
      const data = await postJSON<BundleDetails>(endpoint("/numbers/regulatory/bundles/get"), { compliance_id: bundleSid });
      setSelected(data);
      if (!preserveResult) setResult(null);
    } catch (e) {
      setStatus((e as Error).message || "Could not load bundle");
    } finally {
      setBusy(false);
    }
  };
  const discover = async () => {
    setBusy(true);
    try {
      const data = await postJSON(endpoint("/numbers/regulatory/requirements"), bundleForm);
      setRequirements(data);
      setStatus("Requirements loaded");
    } catch (e) {
      setStatus((e as Error).message || "Requirement lookup failed");
    } finally {
      setBusy(false);
    }
  };
  const createBundle = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      const data = await postJSON<{ bundle?: RegulatoryBundle }>(endpoint("/numbers/regulatory/bundles/create"), bundleForm);
      setStatus("Compliance profile created");
      await load();
      if (data.bundle?.sid) await inspect(data.bundle.sid);
    } catch (e) {
      setStatus((e as Error).message || "Bundle creation failed");
    } finally {
      setBusy(false);
    }
  };
  const addItem = async (event: React.FormEvent) => {
    event.preventDefault();
    const bundleSid = selected?.bundle?.sid;
    if (!bundleSid) return;
    let attributes: Record<string, unknown>;
    try {
      attributes = JSON.parse(itemForm.attributes) as Record<string, unknown>;
    } catch {
      setStatus("Attributes must be valid JSON");
      return;
    }
    setBusy(true);
    try {
      const body: Record<string, unknown> = { compliance_id: bundleSid, ...itemForm, attributes };
      if (itemFile) {
        body.file = await readAsDataURL(itemFile);
        body.file_name = itemFile.name;
      }
      const data = await postJSON(endpoint("/numbers/regulatory/bundles/items/create"), body);
      setResult(data);
      setStatus("Compliance requirement assigned");
      setItemFile(null);
      await inspect(bundleSid, true);
    } catch (e) {
      setStatus((e as Error).message || "Item creation failed");
    } finally {
      setBusy(false);
    }
  };
  const bundleAction = async (action: "evaluate" | "submit") => {
    const bundleSid = selected?.bundle?.sid;
    if (!bundleSid) return;
    setBusy(true);
    try {
      const data = await postJSON(endpoint(`/numbers/regulatory/bundles/${action}`), { compliance_id: bundleSid });
      setResult(data);
      setStatus(action === "submit" ? "Submission processed" : "Evaluation complete");
      await inspect(bundleSid, true);
    } catch (e) {
      setStatus((e as Error).message || `${action} failed`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full min-h-0 grid xl:grid-cols-[20rem_minmax(0,1fr)_22rem] bg-bg text-text">
      <section className="min-h-0 overflow-auto border-r border-border">
        <header className="h-12 px-4 border-b border-border flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">Compliance profiles</h2>
          <span className="truncate text-xs text-text-muted">{status}</span>
        </header>
        {bundles.map((bundle) => (
          <button key={bundle.sid} type="button" onClick={() => inspect(bundle.sid)} className={`w-full px-4 py-3 text-left border-b border-border/70 hover:bg-bg-muted/60 ${selected?.bundle?.sid === bundle.sid ? "bg-bg-muted" : ""}`}>
            <div className="flex items-center justify-between gap-2">
              <span className="truncate text-sm font-medium">{bundle.friendly_name || bundle.sid}</span>
              <span className="shrink-0 text-xs text-text-muted">{bundle.status || "unknown"}</span>
            </div>
            <div className="mt-1 truncate font-mono text-xs text-text-dim">{bundle.sid}</div>
          </button>
        ))}
      </section>

      <section className="min-h-0 overflow-auto border-r border-border">
        {selected?.bundle ? (
          <div>
            <header className="px-4 py-3 border-b border-border flex flex-wrap items-center gap-3">
              <div className="min-w-0 flex-1">
                <h2 className="truncate text-sm font-semibold">{selected.bundle.friendly_name || selected.bundle.sid}</h2>
                <div className="mt-1 truncate font-mono text-xs text-text-dim">{selected.bundle.sid}</div>
              </div>
              <span className="rounded border border-border px-2 py-1 text-xs">{selected.bundle.status || "unknown"}</span>
              <button type="button" disabled={busy} onClick={() => bundleAction("evaluate")} className="h-8 px-3 rounded border border-border text-xs disabled:opacity-50">Evaluate</button>
              <button type="button" disabled={busy || selected.bundle.status !== "draft"} onClick={() => bundleAction("submit")} className="h-8 px-3 rounded bg-accent text-bg text-xs font-medium disabled:opacity-50">Submit</button>
            </header>
            <div className="grid lg:grid-cols-2">
              <section className="p-4 border-b lg:border-r border-border">
                <h3 className="mb-2 text-xs font-semibold uppercase text-text-dim">Requirements</h3>
                <pre className="max-h-80 overflow-auto whitespace-pre-wrap text-xs leading-5 text-text-muted">{JSON.stringify(selected.regulation ?? {}, null, 2)}</pre>
              </section>
              <section className="p-4 border-b border-border">
                <h3 className="mb-2 text-xs font-semibold uppercase text-text-dim">Assigned values</h3>
                <pre className="max-h-80 overflow-auto whitespace-pre-wrap text-xs leading-5 text-text-muted">{JSON.stringify(selected.items ?? [], null, 2)}</pre>
              </section>
            </div>
            {result ? (
              <section className="p-4 border-b border-border">
                <h3 className="mb-2 text-xs font-semibold uppercase text-text-dim">Latest result</h3>
                <pre className="max-h-80 overflow-auto whitespace-pre-wrap text-xs leading-5 text-text-muted">{JSON.stringify(result, null, 2)}</pre>
              </section>
            ) : null}
            <form onSubmit={addItem} className="p-4 space-y-3">
              <h3 className="text-sm font-semibold">Set requirement</h3>
              {provider === "telnyx" ? (
                <div className="grid md:grid-cols-2 gap-3">
                  <Field label="Requirement ID" value={itemForm.requirement_id} onChange={(value) => setItemForm({ ...itemForm, requirement_id: value })} required />
                  <Field label="Field value" value={itemForm.field_value} onChange={(value) => setItemForm({ ...itemForm, field_value: value })} />
                </div>
              ) : null}
              {provider !== "telnyx" ? <>
              <div className="grid md:grid-cols-3 gap-3">
                <label className="block">
                  <span className="mb-1 block text-xs text-text-muted">Kind</span>
                  <select value={itemForm.kind} onChange={(event) => setItemForm({ ...itemForm, kind: event.target.value })} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm">
                    <option value="end_user">End user</option><option value="document">Document</option>
                  </select>
                </label>
                <Field label="Name" value={itemForm.friendly_name} onChange={(value) => setItemForm({ ...itemForm, friendly_name: value })} required />
                <Field label="Type" value={itemForm.type} onChange={(value) => setItemForm({ ...itemForm, type: value })} required />
              </div>
              <label className="block">
                <span className="mb-1 block text-xs text-text-muted">Attributes JSON</span>
                <textarea value={itemForm.attributes} onChange={(event) => setItemForm({ ...itemForm, attributes: event.target.value })} rows={5} className="w-full resize-y rounded border border-border bg-bg px-3 py-2 font-mono text-xs outline-none focus:border-text-dim" />
              </label>
              </> : null}
              {provider === "telnyx" || itemForm.kind === "document" ? (
                <input type="file" accept="image/jpeg,image/png,application/pdf" onChange={(event) => setItemFile(event.target.files?.[0] ?? null)} className="block w-full text-xs text-text-muted file:mr-3 file:h-8 file:rounded file:border file:border-border file:bg-bg file:px-3 file:text-xs file:text-text" />
              ) : null}
              <button type="submit" disabled={busy} className="h-9 px-4 rounded bg-accent text-bg text-sm font-medium disabled:opacity-50">Create and assign</button>
            </form>
          </div>
        ) : <div className="h-full min-h-[18rem] flex items-center justify-center text-sm text-text-muted">Select a compliance profile</div>}
      </section>

      <form onSubmit={createBundle} className="min-h-0 overflow-auto p-4 space-y-3">
        <h2 className="text-sm font-semibold">New compliance profile</h2>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Country" value={bundleForm.country} onChange={(value) => setBundleForm({ ...bundleForm, country: value.toUpperCase().slice(0, 2) })} required />
          <label className="block">
            <span className="mb-1 block text-xs text-text-muted">Number type</span>
            <select value={bundleForm.number_type} onChange={(event) => setBundleForm({ ...bundleForm, number_type: event.target.value })} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm">
              <option value="local">Local</option><option value="mobile">Mobile</option><option value="national">National</option><option value="toll_free">Toll-free</option>
            </select>
          </label>
        </div>
        <label className="block">
          <span className="mb-1 block text-xs text-text-muted">End user</span>
          <select value={bundleForm.end_user_type} onChange={(event) => setBundleForm({ ...bundleForm, end_user_type: event.target.value })} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm">
            <option value="individual">Individual</option><option value="business">Business</option>
          </select>
        </label>
        <Field label="Name" value={bundleForm.friendly_name} onChange={(value) => setBundleForm({ ...bundleForm, friendly_name: value })} required />
        {provider !== "telnyx" ? <Field label="Status email" value={bundleForm.email} onChange={(value) => setBundleForm({ ...bundleForm, email: value })} type="email" required /> : null}
        <div className="grid grid-cols-2 gap-2">
          <button type="button" onClick={discover} disabled={busy} className="h-9 rounded border border-border text-sm disabled:opacity-50">Requirements</button>
          <button type="submit" disabled={busy} className="h-9 rounded bg-accent text-bg text-sm font-medium disabled:opacity-50">Create profile</button>
        </div>
        {requirements ? <pre className="max-h-[30rem] overflow-auto whitespace-pre-wrap border-t border-border pt-3 text-xs leading-5 text-text-muted">{JSON.stringify(requirements, null, 2)}</pre> : null}
      </form>
    </div>
  );
}

function Field({ label, value, onChange, required, type = "text" }: { label: string; value: string; onChange: (value: string) => void; required?: boolean; type?: string }) {
  return (
    <label className="block min-w-0">
      <span className="mb-1 block text-xs text-text-muted">{label}</span>
      <input type={type} value={value} required={required} onChange={(event) => onChange(event.target.value)} className="h-9 w-full rounded border border-border bg-bg px-3 text-sm outline-none focus:border-text-dim" />
    </label>
  );
}

function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("Could not read file"));
    reader.readAsDataURL(file);
  });
}

export default function CallsPanel(props: NativePanelProps) {
  const [view, setView] = useState<"calls" | "numbers" | "addresses" | "bundles">("calls");
  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <nav className="shrink-0 h-11 border-b border-border px-4 flex items-center gap-1" aria-label="Telephony views">
        <button
          type="button"
          onClick={() => setView("calls")}
          className={`h-8 px-3 rounded text-sm ${view === "calls" ? "bg-bg-muted font-medium" : "text-text-muted hover:bg-bg-muted/60"}`}
        >
          Calls
        </button>
        <button
          type="button"
          onClick={() => setView("numbers")}
          className={`h-8 px-3 rounded text-sm ${view === "numbers" ? "bg-bg-muted font-medium" : "text-text-muted hover:bg-bg-muted/60"}`}
        >
          Numbers
        </button>
        <button
          type="button"
          onClick={() => setView("addresses")}
          className={`h-8 px-3 rounded text-sm ${view === "addresses" ? "bg-bg-muted font-medium" : "text-text-muted hover:bg-bg-muted/60"}`}
        >
          Addresses
        </button>
        <button
          type="button"
          onClick={() => setView("bundles")}
          className={`h-8 px-3 rounded text-sm ${view === "bundles" ? "bg-bg-muted font-medium" : "text-text-muted hover:bg-bg-muted/60"}`}
        >
          Compliance
        </button>
      </nav>
      <div className="min-h-0 flex-1">
        {view === "calls" ? <CallsView {...props} /> : null}
        {view === "numbers" ? <NumbersView {...props} /> : null}
        {view === "addresses" ? <AddressesView {...props} /> : null}
        {view === "bundles" ? <BundlesView {...props} /> : null}
      </div>
    </div>
  );
}
