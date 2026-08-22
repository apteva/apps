import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  MicrophoneTestSession,
  SoftphoneSession,
  type MicrophoneAppliedSettings,
  type MicrophoneTestResult,
  type SoftphoneAudioOptions,
  type SoftphoneDiagnostics,
  type SoftphoneState,
} from "./softphone-audio";
import {
  loadAudioOptions,
  persistAudioOptions,
} from "./audio-settings";

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
  CarrierStatus?: string;
  carrier_status?: string;
  MediaStatus?: string;
  media_status?: string;
  MediaErrorMessage?: string;
  media_error_message?: string;
  MediaConnectedAt?: string;
  media_connected_at?: string;
  MediaDisconnectedAt?: string;
  media_disconnected_at?: string;
  MediaCloseCode?: number;
  media_close_code?: number;
  MediaCloseReason?: string;
  media_close_reason?: string;
  MediaCloseLeg?: string;
  media_close_leg?: string;
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
  Direction?: string;
  direction?: string;
  PeerKind?: string;
  peer_kind?: string;
  TerminationCause?: string;
  termination_cause?: string;
  TerminationCode?: string;
  termination_code?: string;
  TerminationInitiator?: string;
  termination_initiator?: string;
  BrowserAudioDiagnostics?: BrowserAudioDiagnostics;
  browser_audio_diagnostics?: BrowserAudioDiagnostics;
  CarrierAudioDiagnostics?: CarrierAudioDiagnostics;
  carrier_audio_diagnostics?: CarrierAudioDiagnostics;
}

interface BrowserAudioDiagnostics {
  received_at?: string;
  rtt_ms?: number;
  playback_queue_ms?: number;
  playback_target_ms?: number;
  playback_max_queue_ms?: number;
  playback_underruns?: number;
  playback_dropped_ms?: number;
  websocket_buffered_bytes?: number;
  audio_context_rate?: number;
  microphone_sample_rate?: number;
  microphone_channel_count?: number;
  echo_cancellation?: boolean;
  noise_suppression?: boolean;
  auto_gain_control?: boolean;
  mic_active_rms_dbfs?: number;
  mic_peak_dbfs?: number;
  mic_post_peak_dbfs?: number;
  mic_input_gain_db?: number;
  mic_limiter_reduction_db?: number;
  capture_sequence_gaps?: number;
  playback_sequence_gaps?: number;
  drop_events?: PersistedDropEvent[];
}

interface PersistedDropEvent {
  timestamp?: string;
  direction?: string;
  reason?: string;
  duration_ms?: number;
  queue_before_ms?: number;
  queue_after_ms?: number;
  sequence?: number;
}

interface CarrierAudioDiagnostics {
  updated_at?: string;
  provider?: string;
  codec?: string;
  sample_rate?: number;
  pacer_mode?: string;
  max_queued_ms?: number;
  dropped_stale_ms?: number;
  pre_answer_microphone_dropped_ms?: number;
  sequence_gaps?: number;
  drop_events?: PersistedDropEvent[];
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
  carrierStatus: string;
  mediaStatus: string;
  mediaErrorMessage: string;
  mediaConnectedAt: string;
  mediaDisconnectedAt: string;
  mediaCloseCode: number;
  mediaCloseReason: string;
  mediaCloseLeg: string;
  placedAt: string;
  answeredAt: string;
  endedAt: string;
  projectId: string;
  errorMessage: string;
  recordingMode: string;
  recordingCount: number;
  recordingStatus: string;
  direction: string;
  peerKind: string;
  terminationCause: string;
  terminationCode: string;
  terminationInitiator: string;
  browserAudioDiagnostics: BrowserAudioDiagnostics;
  carrierAudioDiagnostics: CarrierAudioDiagnostics;
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
  playback_urls?: Partial<Record<"mix" | "caller" | "agent" | "original", string>>;
}

const LIVE_STATUSES = new Set(["initiated", "ringing", "in-progress", "answered"]);
const TERMINAL_STATUSES = new Set(["completed", "failed", "no-answer", "busy", "canceled"]);
const CALL_COLUMNS = "8rem minmax(0,1fr) minmax(0,1fr) 8rem 7rem 6rem 7rem";
const CONNECTED_NUMBER_COLUMNS = "minmax(12rem,1.3fr) 7rem minmax(8rem,0.8fr) minmax(10rem,1fr) minmax(11rem,1fr) minmax(9rem,0.8fr) minmax(12rem,1fr) minmax(11rem,0.9fr)";
const NUMBER_COLUMNS = "10rem 7rem minmax(0,1fr) 9rem 9rem 9rem 9rem 7rem";
const ADDRESS_COLUMNS = "10rem minmax(0,1fr) 8rem 9rem 12rem";
const DETAILS_COLUMNS = "7.5rem minmax(0,1fr)";

function usePanelWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;

    const update = () => setWidth(element.getBoundingClientRect().width);
    update();

    if (typeof ResizeObserver === "undefined") {
      window.addEventListener("resize", update);
      return () => window.removeEventListener("resize", update);
    }

    const observer = new ResizeObserver((entries) => {
      setWidth(entries[0]?.contentRect.width ?? element.getBoundingClientRect().width);
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return { ref, width };
}

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
    carrierStatus: row.carrier_status ?? row.CarrierStatus ?? row.status ?? row.Status ?? "",
    mediaStatus: row.media_status ?? row.MediaStatus ?? "idle",
    mediaErrorMessage: row.media_error_message ?? row.MediaErrorMessage ?? "",
    mediaConnectedAt: row.media_connected_at ?? row.MediaConnectedAt ?? "",
    mediaDisconnectedAt: row.media_disconnected_at ?? row.MediaDisconnectedAt ?? "",
    mediaCloseCode: row.media_close_code ?? row.MediaCloseCode ?? 0,
    mediaCloseReason: row.media_close_reason ?? row.MediaCloseReason ?? "",
    mediaCloseLeg: row.media_close_leg ?? row.MediaCloseLeg ?? "",
    placedAt: row.placed_at ?? row.PlacedAt ?? "",
    answeredAt: row.answered_at ?? row.AnsweredAt ?? "",
    endedAt: row.ended_at ?? row.EndedAt ?? "",
    projectId: row.project_id ?? row.ProjectID ?? "",
    errorMessage: row.error_message ?? row.ErrorMessage ?? "",
    recordingMode: row.recording_mode ?? row.RecordingMode ?? "off",
    recordingCount: row.recording_count ?? row.RecordingCount ?? 0,
    recordingStatus: row.recording_status ?? row.RecordingStatus ?? "",
    direction: row.direction ?? row.Direction ?? "outbound",
    peerKind: row.peer_kind ?? row.PeerKind ?? "realtime",
    terminationCause: row.termination_cause ?? row.TerminationCause ?? "",
    terminationCode: row.termination_code ?? row.TerminationCode ?? "",
    terminationInitiator: row.termination_initiator ?? row.TerminationInitiator ?? "",
    browserAudioDiagnostics: row.browser_audio_diagnostics ?? row.BrowserAudioDiagnostics ?? {},
    carrierAudioDiagnostics: row.carrier_audio_diagnostics ?? row.CarrierAudioDiagnostics ?? {},
  };
}

function statusClass(status: string): string {
  if (status === "in-progress" || status === "answered") return "bg-success/10 text-success border-success/30";
  if (status === "ringing" || status === "initiated") return "bg-info/10 text-info border-info/30";
  if (status === "failed" || status === "busy" || status === "no-answer") return "bg-error/10 text-error border-error/30";
  if (status === "canceled") return "bg-warn/10 text-warn border-warn/30";
  return "bg-bg-muted text-text-muted border-border";
}

function mediaStatusClass(status: string): string {
  if (status === "connected") return "bg-success/10 text-success border-success/30";
  if (status === "connecting" || status === "degraded") return "bg-warn/10 text-warn border-warn/30";
  if (status === "error") return "bg-error/10 text-error border-error/30";
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

function RecordingPlayer({ recording }: { recording: Recording }) {
  const variants = recording.playback_urls ?? (recording.playback_url ? { mix: recording.playback_url } : {});
  const available = (["mix", "caller", "agent", "original"] as const).filter((variant) => Boolean(variants[variant]));
  const [variant, setVariant] = useState<(typeof available)[number]>(available[0] ?? "mix");
  const url = variants[variant] ?? recording.playback_url;
  if (!url) return null;
  const labels = { mix: "Balanced", caller: "Caller", agent: "Agent", original: "Original" };
  return (
    <div className="mt-3 space-y-2">
      {available.length > 1 ? (
        <div className="inline-flex max-w-full overflow-x-auto rounded border border-border p-0.5" role="group" aria-label="Recording track">
          {available.map((item) => (
            <button
              key={item}
              type="button"
              onClick={() => setVariant(item)}
              className={`h-7 whitespace-nowrap px-2 text-xs ${variant === item ? "bg-bg-muted font-medium text-text" : "text-text-muted hover:text-text"}`}
            >
              {labels[item]}
            </button>
          ))}
        </div>
      ) : null}
      <div className="flex items-center gap-3">
        <audio key={url} src={url} controls preload="metadata" className="min-w-0 flex-1 h-9" />
        <a href={url} download className="text-xs text-accent hover:underline">Download</a>
        <span className="text-xs text-text-dim">{recording.playback_source === "storage" ? "Storage" : "Carrier"}</span>
      </div>
    </div>
  );
}

// Icons inherit color through currentColor from the surrounding text class, so
// they follow the theme without any color utilities on the SVG itself.
function PhoneIcon({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72c.127.96.361 1.903.7 2.81a2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45c.907.339 1.85.573 2.81.7A2 2 0 0 1 22 16.92z" />
    </svg>
  );
}

function BackspaceIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M21 4H8l-7 8 7 8h13a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2z" />
      <line x1="18" y1="9" x2="12" y2="15" />
      <line x1="12" y1="9" x2="18" y2="15" />
    </svg>
  );
}

const E164_RE = /^\+[1-9]\d{7,14}$/;

// Accepts pasted numbers in any human formatting and reduces them to E.164
// characters. "00" international prefixes become "+".
function normalizeDialInput(raw: string): string {
  let value = raw.replace(/[^\d+]/g, "");
  if (value.startsWith("00")) value = "+" + value.slice(2);
  value = value[0] === "+" ? "+" + value.slice(1).replace(/\+/g, "") : value.replace(/\+/g, "");
  return value.slice(0, 16);
}

const KEYPAD_ROWS: string[][] = [
  ["1", "2", "3"],
  ["4", "5", "6"],
  ["7", "8", "9"],
  ["+", "0", "back"],
];

function Dialer({ value, from, timeoutSec, fromNumbers, fromLoading, fromError, onChange, onFromChange, onTimeoutChange, onCall, onApplyProfile, onRefreshNumbers, profileBusy, busy, disabled, recent, status }: {
  value: string;
  from: string;
  timeoutSec: number;
  fromNumbers: ConnectedNumber[];
  fromLoading: boolean;
  fromError: string;
  onChange: (next: string) => void;
  onFromChange: (next: string) => void;
  onTimeoutChange: (next: number) => void;
  onCall: () => void;
  onApplyProfile: (profileId: string) => void;
  onRefreshNumbers: () => void;
  profileBusy: boolean;
  busy: boolean;
  disabled: boolean;
  recent: { number: string; label: string }[];
  status: string;
}) {
  const valid = E164_RE.test(value);
  const selectedNumber = fromNumbers.find((number) => number.phone_number === from);
  const outbound = selectedNumber?.outbound;
  const [profileId, setProfileId] = useState("");
  const enabledProfiles = outbound?.profiles.filter((profile) => profile.enabled) ?? [];
  const outboundBlocked = Boolean(outbound?.required && outbound.status !== "ready" && outbound.status !== "auto_configurable");
  useEffect(() => {
    const recommended = outbound?.recommended_profile_id ?? "";
    setProfileId((current) => enabledProfiles.some((profile) => profile.id === current) ? current : recommended);
  }, [from, outbound?.recommended_profile_id, enabledProfiles.map((profile) => profile.id).join(",")]);
  const press = (key: string) => {
    if (key === "back") onChange(value.slice(0, -1));
    else onChange(normalizeDialInput(value + key));
  };
  return (
    <div className="p-4 space-y-4" style={{ maxWidth: "22rem", margin: "0 auto" }}>
      <label className="block">
        <span className="text-xs text-text-dim">Call from</span>
        <select
          value={from}
          disabled={fromLoading || fromNumbers.length === 0 || disabled}
          onChange={(event) => onFromChange(event.target.value)}
          className="mt-1 h-10 w-full rounded border border-border bg-bg px-3 text-sm tabular-nums disabled:opacity-50"
        >
          <option value="">{fromLoading ? "Loading connected numbers…" : "Select a caller ID"}</option>
          {fromNumbers.map((number) => (
            <option key={`${number.provider}-${number.phone_number}`} value={number.phone_number}>
              {number.phone_number}{number.friendly_name ? ` — ${number.friendly_name}` : ""}
            </option>
          ))}
        </select>
        {fromError ? <span className="mt-1 block text-xs text-error">{fromError}</span> : null}
      </label>
      <label className="block">
        <span className="text-xs text-text-dim">Ring destination for</span>
        <select
          value={timeoutSec}
          disabled={disabled}
          onChange={(event) => onTimeoutChange(Number(event.target.value))}
          className="mt-1 h-9 w-full rounded border border-border bg-bg px-3 text-sm disabled:opacity-50"
        >
          {[30, 45, 60, 90, 120].map((seconds) => <option key={seconds} value={seconds}>{seconds} seconds</option>)}
        </select>
      </label>
      {outbound?.required && outbound.status !== "ready" ? (
        <div className={`rounded border p-3 text-xs ${outbound.status === "auto_configurable" ? "border-info/30 bg-info/10" : "border-warn/30 bg-warn/10"}`}>
          <div className="font-medium">Outbound calling setup</div>
          <p className="mt-1 text-text-muted">{outbound.message || "This caller ID needs an outbound calling profile."}</p>
          {outbound.status === "selection_required" ? (
            <div className="mt-2 flex gap-2">
              <select value={profileId} onChange={(event) => setProfileId(event.target.value)} className="h-9 min-w-0 flex-1 rounded border border-border bg-bg px-2 text-xs">
                <option value="">Select a profile</option>
                {enabledProfiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name || profile.id}</option>)}
              </select>
              <button type="button" disabled={!profileId || profileBusy} onClick={() => onApplyProfile(profileId)} className="h-9 rounded bg-accent px-3 text-bg disabled:opacity-40">
                {profileBusy ? "Applying…" : "Apply"}
              </button>
            </div>
          ) : outbound.status === "setup_required" || outbound.status === "error" ? (
            <button type="button" disabled={profileBusy} onClick={onRefreshNumbers} className="mt-2 h-8 rounded border border-border bg-bg px-3 disabled:opacity-40">Refresh</button>
          ) : null}
        </div>
      ) : null}
      <div>
        <div className="text-xs text-text-dim">New call</div>
        <form onSubmit={(event) => { event.preventDefault(); if (valid && E164_RE.test(from) && !busy && !disabled && !outboundBlocked) onCall(); }}>
          <input
            type="tel"
            value={value}
            autoFocus
            onChange={(event) => onChange(normalizeDialInput(event.target.value))}
            placeholder="+1 415 555 1234"
            aria-label="Number to call"
            className="mt-1 w-full rounded border border-border bg-bg px-3 text-center font-semibold tabular-nums"
            style={{ height: "3rem", fontSize: "1.25rem", letterSpacing: "0.03em" }}
          />
        </form>
        <div className="mt-1 text-xs text-text-muted text-center" style={{ minHeight: "1rem" }}>
          {value && !valid ? "Enter a full international number starting with +" : status}
        </div>
      </div>

      <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(3, minmax(0, 1fr))" }}>
        {KEYPAD_ROWS.flat().map((key) => (
          <button
            key={key}
            type="button"
            onClick={() => press(key)}
            aria-label={key === "back" ? "Delete last digit" : `Dial ${key}`}
            className="flex items-center justify-center rounded border border-border hover:bg-bg-muted font-medium"
            style={{ height: "2.9rem", fontSize: "1.05rem" }}
          >
            {key === "back" ? <span className="text-text-muted"><BackspaceIcon /></span> : key}
          </button>
        ))}
      </div>

      <button
        type="button"
        disabled={!valid || !E164_RE.test(from) || busy || disabled || outboundBlocked}
        onClick={onCall}
        className="w-full flex items-center justify-center gap-2 rounded bg-accent text-bg text-sm font-medium disabled:opacity-40"
        style={{ height: "2.9rem" }}
      >
        <PhoneIcon size={15} />
        {busy ? "Calling…" : "Call"}
      </button>
      {disabled ? (
        <p className="text-xs text-text-muted text-center">Finish the current call before starting another.</p>
      ) : null}

      {recent.length > 0 ? (
        <div>
          <div className="text-xs text-text-dim">Recent</div>
          <div className="mt-1 space-y-1">
            {recent.map((entry) => (
              <button
                key={entry.number}
                type="button"
                onClick={() => onChange(entry.number)}
                className="w-full flex items-center justify-between gap-2 rounded border border-border/70 px-3 py-2 text-left hover:bg-bg-muted"
              >
                <span className="text-sm font-medium tabular-nums truncate">{entry.number}</span>
                <span className="text-xs text-text-muted shrink-0">{entry.label}</span>
              </button>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

// Audio level bar. RMS is compressed with a square root so ordinary speech
// occupies most of the bar instead of hugging the low end.
function LevelMeter({ label, value }: { label: string; value: number }) {
  const percent = Math.min(100, Math.round(Math.sqrt(Math.max(0, value)) * 140));
  return (
    <div className="flex items-center gap-2">
      <div className="text-xs text-text-muted" style={{ width: "3rem" }}>{label}</div>
      <div className="flex-1 rounded bg-border/60 overflow-hidden" style={{ height: "0.375rem" }}>
        <div className="h-full bg-accent" style={{ width: `${percent}%` }} />
      </div>
    </div>
  );
}

function AudioProcessingSettings({
  value,
  disabled,
  onChange,
}: {
  value: SoftphoneAudioOptions;
  disabled: boolean;
  onChange: (value: SoftphoneAudioOptions) => void;
}) {
  const options: { key: "echoCancellation" | "noiseSuppression" | "autoGainControl" | "highpassFilter"; label: string }[] = [
    { key: "echoCancellation", label: "Echo cancellation" },
    { key: "noiseSuppression", label: "Noise suppression" },
    { key: "autoGainControl", label: "Automatic gain" },
    { key: "highpassFilter", label: "Voice high-pass filter" },
  ];
  return (
    <div className="rounded border border-border/70 p-3">
      <div className="text-xs font-medium">Browser audio processing</div>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-2">
        {options.map((option) => (
          <label key={option.key} className="flex items-center gap-2 text-xs text-text-muted">
            <input
              type="checkbox"
              checked={value[option.key]}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, [option.key]: event.target.checked })}
            />
            {option.label}
          </label>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2 text-xs">
        <span className="text-text-dim">Output mode</span>
        <button type="button" disabled={disabled} onClick={() => onChange({ ...value, echoCancellation: true })} className={`rounded border px-2 py-1 ${value.echoCancellation ? "border-accent text-accent" : "border-border text-text-muted"}`}>Speakers</button>
        <button type="button" disabled={disabled} onClick={() => onChange({ ...value, echoCancellation: false })} className={`rounded border px-2 py-1 ${!value.echoCancellation ? "border-accent text-accent" : "border-border text-text-muted"}`}>Headset</button>
        <label className="ml-2 flex items-center gap-2 text-text-muted">
          Mic headroom
          <select disabled={disabled} value={value.inputGainDB} onChange={(event) => onChange({ ...value, inputGainDB: Number(event.target.value) })} className="rounded border border-border bg-bg px-2 py-1">
            <option value={-9}>-9 dB</option>
            <option value={-6}>-6 dB (recommended)</option>
            <option value={-3}>-3 dB</option>
            <option value={0}>0 dB</option>
          </select>
        </label>
      </div>
      <p className="mt-2 text-xs text-text-dim">
        Speakers enable echo cancellation; headsets disable it. The -6 dB headroom and -3 dBFS limiter protect the carrier stream from clipping. Changes apply to the next call.
      </p>
    </div>
  );
}

function appliedSetting(value: boolean | null): string {
  if (value === null) return "not reported";
  return value ? "on" : "off";
}

function microphoneLevelAssessment(result: MicrophoneTestResult): { label: string; className: string } {
  if (result.activeRmsDbfs === null) return { label: "No speech detected", className: "text-warn" };
  if (result.activeRmsDbfs < -35) return { label: "Very quiet — enable automatic gain or raise the input level", className: "text-error" };
  if (result.postPeakDbfs !== null && result.postPeakDbfs > -2.8) return { label: "Limiter active — add more microphone headroom", className: "text-warn" };
  return { label: "Input level looks healthy", className: "text-success" };
}

function MicrophoneTest({
  options,
  workletURL,
  disabled,
}: {
  options: SoftphoneAudioOptions;
  workletURL: string;
  disabled: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [recording, setRecording] = useState(false);
  const [level, setLevel] = useState(0);
  const [elapsedMs, setElapsedMs] = useState(0);
  const [error, setError] = useState("");
  const [settings, setSettings] = useState<MicrophoneAppliedSettings | null>(null);
  const [result, setResult] = useState<MicrophoneTestResult | null>(null);
  const [audioURL, setAudioURL] = useState("");
  const sessionRef = useRef<MicrophoneTestSession | null>(null);
  const urlRef = useRef("");
  const stopTimerRef = useRef<number | null>(null);
  const elapsedTimerRef = useRef<number | null>(null);
  const startedAtRef = useRef(0);

  const clearTimers = useCallback(() => {
    if (stopTimerRef.current !== null) window.clearTimeout(stopTimerRef.current);
    if (elapsedTimerRef.current !== null) window.clearInterval(elapsedTimerRef.current);
    stopTimerRef.current = null;
    elapsedTimerRef.current = null;
  }, []);

  const replaceAudioURL = useCallback((next: string) => {
    if (urlRef.current) URL.revokeObjectURL(urlRef.current);
    urlRef.current = next;
    setAudioURL(next);
  }, []);

  const finish = useCallback(async (session: MicrophoneTestSession) => {
    if (sessionRef.current !== session) return;
    sessionRef.current = null;
    clearTimers();
    setBusy(true);
    setRecording(false);
    try {
      const next = await session.stop();
      setResult(next);
      setSettings(next.settings);
      setElapsedMs(next.durationMs);
      replaceAudioURL(URL.createObjectURL(next.audio));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Microphone test failed.");
    } finally {
      setLevel(0);
      setBusy(false);
    }
  }, [clearTimers, replaceAudioURL]);

  const start = useCallback(async () => {
    if (disabled || sessionRef.current) return;
    setBusy(true);
    setError("");
    setResult(null);
    setSettings(null);
    setElapsedMs(0);
    replaceAudioURL("");
    const session = new MicrophoneTestSession(setLevel);
    sessionRef.current = session;
    try {
      const applied = await session.start(workletURL, options);
      if (sessionRef.current !== session) {
        await session.cancel();
        return;
      }
      setSettings(applied);
      setRecording(true);
      startedAtRef.current = Date.now();
      elapsedTimerRef.current = window.setInterval(() => setElapsedMs(Date.now() - startedAtRef.current), 100);
      stopTimerRef.current = window.setTimeout(() => void finish(session), 15_000);
    } catch (cause) {
      sessionRef.current = null;
      setError(cause instanceof Error ? cause.message : "Microphone permission was not granted.");
    } finally {
      setBusy(false);
    }
  }, [disabled, finish, options, replaceAudioURL, workletURL]);

  const discard = useCallback(() => {
    setResult(null);
    setSettings(null);
    setElapsedMs(0);
    setError("");
    replaceAudioURL("");
  }, [replaceAudioURL]);

  useEffect(() => {
    if (!disabled || !sessionRef.current) return;
    const session = sessionRef.current;
    sessionRef.current = null;
    clearTimers();
    setRecording(false);
    setBusy(false);
    setLevel(0);
    void session.cancel();
  }, [clearTimers, disabled]);

  useEffect(() => () => {
    clearTimers();
    void sessionRef.current?.cancel();
    sessionRef.current = null;
    if (urlRef.current) URL.revokeObjectURL(urlRef.current);
    urlRef.current = "";
  }, [clearTimers]);

  const assessment = result ? microphoneLevelAssessment(result) : null;
  const processingMismatch = settings ? [
    options.echoCancellation && settings.echoCancellation === false ? "echo cancellation" : "",
    options.noiseSuppression && settings.noiseSuppression === false ? "noise suppression" : "",
    options.autoGainControl && settings.autoGainControl === false ? "automatic gain" : "",
  ].filter(Boolean) : [];
  return (
    <div className="rounded border border-border/70 p-3 space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-xs font-medium">Microphone test</div>
          <p className="mt-1 text-xs text-text-dim">
            Records the exact browser-side call signal for up to 15 seconds. It stays on this device and is never uploaded.
          </p>
        </div>
        {recording ? (
          <button type="button" onClick={() => { const session = sessionRef.current; if (session) void finish(session); }} className="h-8 px-3 rounded bg-error text-bg text-xs font-medium">
            Stop
          </button>
        ) : (
          <button type="button" disabled={disabled || busy} onClick={() => void start()} className="h-8 px-3 rounded border border-border text-xs font-medium hover:bg-bg-muted disabled:opacity-40">
            {busy ? "Preparing…" : result ? "Record again" : "Test microphone"}
          </button>
        )}
      </div>

      {recording ? (
        <div className="space-y-2">
          <LevelMeter label="Mic" value={level} />
          <div className="text-xs text-error tabular-nums">Recording locally · {(elapsedMs / 1000).toFixed(1)}s / 15.0s</div>
        </div>
      ) : null}

      {settings ? (
        <div className="text-xs text-text-dim">
          <div className="truncate">{settings.deviceLabel}</div>
          <div className="mt-1 tabular-nums">
            Device {settings.sampleRate ? `${Math.round(settings.sampleRate / 1000)} kHz` : "rate unknown"}
            {settings.channelCount ? ` · ${settings.channelCount} channel${settings.channelCount === 1 ? "" : "s"}` : ""}
            {` · echo ${appliedSetting(settings.echoCancellation)}`}
            {` · noise suppression ${appliedSetting(settings.noiseSuppression)}`}
            {` · automatic gain ${appliedSetting(settings.autoGainControl)}`}
          </div>
        </div>
      ) : null}
      {processingMismatch.length > 0 ? (
        <div className="text-xs text-warn">
          The browser did not apply: {processingMismatch.join(", ")}.
        </div>
      ) : null}

      {result && audioURL ? (
        <div className="space-y-2">
          <audio className="w-full" controls preload="metadata" src={audioURL} />
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
            <span className={assessment?.className}>
              {assessment?.label}
              {result.activeRmsDbfs !== null ? ` · speech ${result.activeRmsDbfs.toFixed(1)} dBFS` : ""}
              {result.peakDbfs !== null ? ` · peak ${result.peakDbfs.toFixed(1)} dBFS` : ""}
              {result.postPeakDbfs !== null ? ` · sent peak ${result.postPeakDbfs.toFixed(1)} dBFS` : ""}
              {result.limiterReductionDb > 0.1 ? ` · limiter ${result.limiterReductionDb.toFixed(1)} dB` : ""}
            </span>
            <button type="button" onClick={discard} className="text-text-muted hover:text-text">Discard</button>
          </div>
        </div>
      ) : null}

      {error ? <div className="text-xs text-error">{error}</div> : null}
    </div>
  );
}

function PersistedAudioDiagnostics({ call }: { call: Call }) {
  const browser = call.browserAudioDiagnostics;
  const carrier = call.carrierAudioDiagnostics;
  if (Object.keys(browser).length === 0 && Object.keys(carrier).length === 0) return null;
  const microphoneWarning = typeof browser.mic_active_rms_dbfs === "number" && browser.mic_active_rms_dbfs < -35;
  const latencyWarning = (carrier.max_queued_ms ?? 0) > 150;
  const dropEvents = [...(browser.drop_events ?? []), ...(carrier.drop_events ?? [])].slice(-5);
  return (
    <div className="rounded border border-border/70 p-3 space-y-2 text-xs">
      <div className="font-medium">Saved audio diagnostics</div>
      {Object.keys(browser).length > 0 ? (
        <div className="text-text-dim tabular-nums">
          Browser: mic {typeof browser.mic_active_rms_dbfs === "number" ? `${browser.mic_active_rms_dbfs.toFixed(1)} dBFS` : "–"}
          {typeof browser.mic_peak_dbfs === "number" ? ` (peak ${browser.mic_peak_dbfs.toFixed(1)})` : ""}
          {typeof browser.mic_post_peak_dbfs === "number" ? ` · sent peak ${browser.mic_post_peak_dbfs.toFixed(1)}` : ""}
          {typeof browser.mic_limiter_reduction_db === "number" ? ` · limiter ${browser.mic_limiter_reduction_db.toFixed(1)} dB` : ""}
          {` · RTT ${browser.rtt_ms ?? "–"} ms`}
          {` · playback buffer ${browser.playback_queue_ms ?? 0}/${browser.playback_target_ms ?? 0} ms`}
          {` · underruns ${browser.playback_underruns ?? 0}`}
          {` · dropped ${browser.playback_dropped_ms ?? 0} ms`}
          {` · WS pending ${browser.websocket_buffered_bytes ?? 0} B`}
          {` · AGC ${appliedSetting(browser.auto_gain_control ?? null)}`}
        </div>
      ) : null}
      {Object.keys(carrier).length > 0 ? (
        <div className="text-text-dim tabular-nums">
          Carrier: {carrier.provider ?? "–"} {carrier.codec ?? ""}
          {carrier.sample_rate ? `/${Math.round(carrier.sample_rate / 1000)} kHz` : ""}
          {` · ${carrier.pacer_mode ?? "unknown"} pacer`}
          {` · max queue ${carrier.max_queued_ms ?? 0} ms`}
          {` · stale dropped ${carrier.dropped_stale_ms ?? 0} ms`}
          {` · pre-answer mic held ${carrier.pre_answer_microphone_dropped_ms ?? 0} ms`}
          {` · sequence gaps ${carrier.sequence_gaps ?? 0}`}
        </div>
      ) : null}
      {dropEvents.length > 0 ? (
        <div className="space-y-1 text-text-dim tabular-nums">
          <div className="font-medium text-text-muted">Recent media drops</div>
          {dropEvents.map((event, index) => (
            <div key={`${event.timestamp ?? "drop"}-${index}`}>
              {event.timestamp ? new Date(event.timestamp).toLocaleTimeString() : "–"}
              {` · ${event.direction ?? "unknown"} · ${event.duration_ms ?? 0} ms · ${event.reason ?? "unknown"}`}
              {typeof event.queue_before_ms === "number" ? ` · queue ${event.queue_before_ms}→${event.queue_after_ms ?? 0} ms` : ""}
            </div>
          ))}
        </div>
      ) : null}
      {microphoneWarning ? <div className="text-error">The saved microphone level was very quiet.</div> : null}
      {latencyWarning ? <div className="text-warn">The carrier microphone queue exceeded the live-call target.</div> : null}
    </div>
  );
}

function useInboundRingtone(active: boolean) {
  const [enabled, setEnabled] = useState(false);
  const contextRef = useRef<AudioContext | null>(null);

  const enable = useCallback(async () => {
    const AudioContextClass = window.AudioContext
      ?? (window as typeof window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AudioContextClass) return;
    const context = contextRef.current ?? new AudioContextClass();
    contextRef.current = context;
    await context.resume();
    setEnabled(true);
  }, []);

  const disable = useCallback(() => setEnabled(false), []);

  useEffect(() => {
    if (!active || !enabled || !contextRef.current) return;
    const context = contextRef.current;
    const ring = () => {
      const start = context.currentTime;
      for (const offset of [0, 0.32]) {
        const oscillator = context.createOscillator();
        const gain = context.createGain();
        oscillator.type = "sine";
        oscillator.frequency.value = 440;
        gain.gain.setValueAtTime(0, start + offset);
        gain.gain.linearRampToValueAtTime(0.07, start + offset + 0.02);
        gain.gain.setValueAtTime(0.07, start + offset + 0.18);
        gain.gain.linearRampToValueAtTime(0, start + offset + 0.25);
        oscillator.connect(gain).connect(context.destination);
        oscillator.start(start + offset);
        oscillator.stop(start + offset + 0.26);
      }
    };
    ring();
    const timer = window.setInterval(ring, 1800);
    return () => window.clearInterval(timer);
  }, [active, enabled]);

  useEffect(() => () => {
    void contextRef.current?.close();
    contextRef.current = null;
  }, []);

  return { ringtoneEnabled: enabled, enableRingtone: enable, disableRingtone: disable };
}

function CallsView({ projectId, installId }: NativePanelProps) {
  const layout = usePanelWidth();
  const [calls, setCalls] = useState<Call[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [ending, setEnding] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const [recordingSettings, setRecordingSettings] = useState<RecordingSettings | null>(null);
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [recordingBusy, setRecordingBusy] = useState("");

  // ─── softphone ───────────────────────────────────────────────────
  const [dialNumber, setDialNumber] = useState("");
  const [fromNumber, setFromNumber] = useState("");
  const [dialTimeoutSec, setDialTimeoutSec] = useState(60);
  const [fromNumbers, setFromNumbers] = useState<ConnectedNumber[]>([]);
  const [fromLoading, setFromLoading] = useState(false);
  const [fromError, setFromError] = useState("");
  const [profileBusy, setProfileBusy] = useState(false);
  const [dialerOpen, setDialerOpen] = useState(false);
  const [softphoneCallId, setSoftphoneCallId] = useState("");
  const [softphoneState, setSoftphoneState] = useState<SoftphoneState | "">("");
  const [softphoneDetail, setSoftphoneDetail] = useState("");
  const [muted, setMuted] = useState(false);
  const [levels, setLevels] = useState<{ mic: number; speaker: number }>({ mic: 0, speaker: 0 });
  const [audioOptions, setAudioOptions] = useState<SoftphoneAudioOptions>(loadAudioOptions);
  const [diagnostics, setDiagnostics] = useState<SoftphoneDiagnostics | null>(null);
  const [softphoneBusy, setSoftphoneBusy] = useState(false);
  const sessionRef = useRef<SoftphoneSession | null>(null);

  const endSoftphone = useCallback(() => {
    sessionRef.current?.stop();
    sessionRef.current = null;
    setSoftphoneCallId("");
    setSoftphoneState("");
    setMuted(false);
    setLevels({ mic: 0, speaker: 0 });
    setDiagnostics(null);
  }, []);

  useEffect(() => {
    persistAudioOptions(audioOptions);
  }, [audioOptions]);

  // The audio session owns a microphone and an AudioContext; unmounting the
  // panel without releasing them would leave the mic indicator lit.
  useEffect(() => () => {
    sessionRef.current?.stop();
    sessionRef.current = null;
  }, []);

  const withProject = useCallback((path: string) => {
    if (!projectId) return `${API}${path}`;
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const chooseFromNumber = useCallback((value: string) => {
    setFromNumber(value);
    if (value) localStorage.setItem(`apteva.telephony.softphone.from.v1:${projectId}`, value);
  }, [projectId]);

  const loadOutboundNumbers = useCallback(async () => {
    setFromLoading(true);
    setFromError("");
    try {
      const data = await postJSON<ConnectedNumbersResponse>(withProject("/numbers/connected"), {});
      const seen = new Set<string>();
      const available = (data.numbers ?? []).filter((number) => {
        if (!E164_RE.test(number.phone_number) || number.carrier_status === "not_found" || seen.has(number.phone_number)) return false;
        if ((number.capabilities?.length ?? 0) > 0 && !number.capabilities?.includes("voice")) return false;
        seen.add(number.phone_number);
        return true;
      });
      setFromNumbers(available);
      const saved = localStorage.getItem(`apteva.telephony.softphone.from.v1:${projectId}`) || "";
      setFromNumber((current) => {
        if (available.some((number) => number.phone_number === current)) return current;
        if (available.some((number) => number.phone_number === saved)) return saved;
        return available.length === 1 ? available[0].phone_number : "";
      });
      if (available.length === 0) setFromError("No voice-capable numbers are connected to this carrier.");
    } catch (error) {
      setFromNumbers([]);
      setFromNumber("");
      setFromError((error as Error).message || "Could not load connected numbers");
    } finally {
      setFromLoading(false);
    }
  }, [projectId, withProject]);

  const configureOutboundProfile = useCallback(async (profileId: string) => {
    if (!fromNumber || !profileId) return;
    setProfileBusy(true);
    setStatus("");
    try {
      await postJSON(withProject("/numbers/outbound-profile"), {
        phone_number: fromNumber,
        profile_id: profileId,
      });
      await loadOutboundNumbers();
      setStatus("Outbound calling profile applied");
    } catch (error) {
      setStatus((error as Error).message || "Could not apply outbound calling profile");
    } finally {
      setProfileBusy(false);
    }
  }, [fromNumber, loadOutboundNumbers, withProject]);

  useEffect(() => {
    if (dialerOpen) void loadOutboundNumbers();
  }, [dialerOpen, loadOutboundNumbers]);

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

  // A ringing softphone call is only answerable while the carrier holds the
  // caller, so poll fast whenever anything is live and fall back to the idle
  // cadence otherwise.
  const hasUrgentCall = useMemo(
    () => calls.some((call) => call.status === "pending" || LIVE_STATUSES.has(call.status)),
    [calls],
  );

  useEffect(() => {
    const timer = window.setInterval(loadCalls, hasUrgentCall ? 2000 : 10000);
    return () => window.clearInterval(timer);
  }, [loadCalls, hasUrgentCall]);

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

  // Inbound softphone calls waiting for a person to pick up.
  const ringing = useMemo(
    () => calls.filter((call) =>
      call.direction === "inbound" && call.peerKind === "human" && call.status === "pending"),
    [calls],
  );
  const { ringtoneEnabled, enableRingtone, disableRingtone } = useInboundRingtone(ringing.length > 0);

  // Redial shortcuts: the most recent distinct outbound destinations.
  const recentNumbers = useMemo(() => {
    const seen = new Set<string>();
    const out: { number: string; label: string }[] = [];
    for (const call of calls) {
      if (call.direction !== "outbound" || !call.toNumber || seen.has(call.toNumber)) continue;
      seen.add(call.toNumber);
      out.push({ number: call.toNumber, label: relative(call.placedAt, now) || "" });
      if (out.length === 4) break;
    }
    return out;
  }, [calls, now]);

  const terminalCount = calls.length - activeCount;

  // Carrier callbacks are the durable authority for call completion. A media
  // socket may disconnect transiently and reconnect, so keep the live panel
  // mounted until the call itself reaches a terminal state.
  useEffect(() => {
    if (!softphoneCallId) return;
    const call = calls.find((candidate) => candidate.id === softphoneCallId);
    if (call && ["completed", "failed", "no-answer", "busy", "canceled"].includes(call.status)) {
      endSoftphone();
    }
  }, [calls, softphoneCallId, endSoftphone]);

  const hangup = async (call: Call) => {
    // "pending" is included so an operator can decline a ringing inbound call.
    if (!call || (!LIVE_STATUSES.has(call.status) && call.status !== "pending")) return;
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

  // Opens the operator's audio path for a call the server has already created.
  // Both dialling out and answering an inbound call funnel through here, so the
  // microphone is acquired in exactly one place.
  type BrowserCallSession = { call_id: string; media_url: string; session_token?: string };

  const workletURL = `/api/apps/telephony/_install/${encodeURIComponent(String(installId))}/ui/softphone-worklet.js`;
  const workerURL = `/api/apps/telephony/_install/${encodeURIComponent(String(installId))}/ui/softphone-worker.js`;

  const startAudio = async (session: BrowserCallSession) => {
    sessionRef.current?.stop();
    const phone = new SoftphoneSession({
      onState: (state, detail) => {
        setSoftphoneState(state);
        setSoftphoneDetail(detail ?? "");
        if (state === "ended" || state === "error") {
          sessionRef.current = null;
          setSoftphoneCallId("");
          setLevels({ mic: 0, speaker: 0 });
          void loadCalls();
        }
      },
      onLevels: (mic, speaker) => setLevels({ mic, speaker }),
      onDiagnostics: setDiagnostics,
    });
    sessionRef.current = phone;
    setSoftphoneCallId(session.call_id);
    setMuted(false);
    setDiagnostics(null);
    // Browser media belongs to the gateway serving this panel. PublicURL is
    // intentionally reserved for carrier webhooks and can name another host
    // (for example production while the operator uses a local dashboard).
    // Preserve the install-scoped path but always pin the socket to this origin.
    const media = new URL(session.media_url, location.href);
    media.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    media.host = location.host;
    await phone.start(media.toString(), workletURL, workerURL, audioOptions);
    setSelectedId(session.call_id);
    await loadCalls();
  };

  const placeSoftphoneCall = async () => {
    const to = dialNumber.trim();
    if (!to) return;
    setSoftphoneBusy(true);
    setStatus("");
    try {
      const session = await postJSON<{ call_id: string; media_url: string }>(
        withProject("/softphone/place"), { to, from: fromNumber, timeout_sec: dialTimeoutSec },
      );
      await startAudio(session);
      setDialNumber("");
      setDialerOpen(false);
      setStatus(`calling ${to}`);
    } catch (e) {
      setStatus((e as Error).message || "Call failed");
      endSoftphone();
    } finally {
      setSoftphoneBusy(false);
    }
  };

  const answerSoftphoneCall = async (call: Call) => {
    setSoftphoneBusy(true);
    setStatus("");
    let session: BrowserCallSession | null = null;
    try {
      session = await postJSON<BrowserCallSession>(
        withProject(`/softphone/answer/${encodeURIComponent(call.id)}`), {},
      );
      await startAudio(session);
      setStatus(`connected to ${call.fromNumber || call.id}`);
    } catch (e) {
      console.error("Telephony softphone answer failed", e);
      if (session?.session_token) {
        try {
          await postJSON(
            withProject(`/softphone/release/${encodeURIComponent(session.call_id)}`),
            { session_token: session.session_token },
          );
        } catch {
          // A carrier event may have ended the call while browser setup failed.
        }
      }
      setStatus((e as Error).message || "Answer failed");
      endSoftphone();
      void loadCalls();
    } finally {
      setSoftphoneBusy(false);
    }
  };

  const toggleMute = () => {
    const next = !muted;
    sessionRef.current?.setMuted(next);
    setMuted(next);
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
    <div ref={layout.ref} className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex flex-wrap items-center gap-3">
        <div className="flex-1" style={{ minWidth: "9rem" }}>
          <h1 className="text-sm font-semibold leading-5">Calls</h1>
          <p className="text-xs text-text-muted">
            {activeCount} active / {terminalCount} recent
          </p>
        </div>
        <div className="text-xs text-text-muted truncate" style={{ maxWidth: "16rem" }}>{status}</div>
        <button
          type="button"
          onClick={() => { if (ringtoneEnabled) disableRingtone(); else void enableRingtone(); }}
          className={`h-8 px-3 rounded border text-xs ${ringtoneEnabled ? "border-success/30 bg-success/10 text-success" : "border-border hover:bg-bg-muted"}`}
          title="Browsers require one click before they may play an incoming-call sound"
        >
          {ringtoneEnabled ? "Call sounds on" : "Enable call sounds"}
        </button>
        {softphoneCallId && softphoneCallId !== selected?.id ? (
          <button
            type="button"
            onClick={() => { setDialerOpen(false); setSelectedId(softphoneCallId); }}
            className="h-8 px-3 rounded border border-success/30 bg-success/10 text-success text-xs font-medium flex items-center gap-2"
          >
            <PhoneIcon size={13} />
            On a call — view
          </button>
        ) : null}
        <button
          type="button"
          onClick={() => setDialerOpen((open) => !open)}
          className={`h-8 px-3 rounded text-xs font-medium flex items-center gap-2 ${dialerOpen ? "border border-border hover:bg-bg-muted" : "bg-accent text-bg"}`}
        >
          <PhoneIcon size={13} />
          {dialerOpen ? "Close dialer" : "New call"}
        </button>
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

      {ringing.length > 0 ? (
        <div className="shrink-0 border-b border-border px-4 py-2 space-y-2">
          {ringing.map((call) => (
            <div key={call.id} className="flex flex-wrap items-center gap-3 rounded border border-success/30 bg-success/10 px-3 py-2">
              <span className="text-success flex items-center gap-2 text-sm font-medium">
                <PhoneIcon size={15} />
                Incoming call
              </span>
              <span className="text-sm tabular-nums font-semibold">{call.fromNumber || "unknown"}</span>
              <span className="text-xs text-text-muted min-w-0 truncate">→ {call.toNumber}</span>
              <span className="flex-1" />
              <button
                type="button"
                disabled={softphoneBusy}
                onClick={() => { setDialerOpen(false); void answerSoftphoneCall(call); }}
                className="h-8 px-4 rounded bg-accent text-bg text-xs font-medium disabled:opacity-40"
              >
                Answer
              </button>
              <button
                type="button"
                disabled={ending === call.id}
                onClick={() => void hangup(call)}
                className="h-8 px-3 rounded border border-error/40 text-error text-xs disabled:opacity-40"
              >
                Decline
              </button>
            </div>
          ))}
        </div>
      ) : null}

      <main
        className="min-h-0 flex-1 grid"
        style={{ gridTemplateColumns: layout.width >= 900 ? "minmax(0,1.2fr) minmax(20rem,0.8fr)" : "minmax(0,1fr)" }}
      >
        <section
          className="min-h-0 min-w-0 border-r border-b border-border overflow-auto"
          style={{ borderRightWidth: layout.width >= 900 ? 1 : 0, borderBottomWidth: layout.width >= 900 ? 0 : 1 }}
        >
          {calls.length === 0 ? (
            <div className="h-full flex items-center justify-center text-sm text-text-muted" style={{ minHeight: "18rem" }}>
              No calls yet.
            </div>
          ) : (
            <div style={{ minWidth: "54rem" }}>
              <div className="grid gap-3 px-4 py-2 border-b border-border text-xs uppercase tracking-normal text-text-dim" style={{ gridTemplateColumns: CALL_COLUMNS }}>
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
                    className={`w-full grid gap-3 px-4 py-3 text-left border-b border-border/70 hover:bg-bg-muted/70 ${picked ? "bg-bg-muted" : ""}`}
                    style={{ gridTemplateColumns: CALL_COLUMNS }}
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
                      {call.recordingStatus
                        ? recordingStatusLabel(call.recordingStatus)
                        : call.recordingMode === "always" && TERMINAL_STATUSES.has(call.status) && !call.answeredAt
                          ? "Not recorded"
                          : call.recordingMode === "always" ? "Enabled" : "Off"}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </section>

        <aside className="min-h-0 overflow-auto">
          {dialerOpen ? (
            <div>
              <Dialer
                value={dialNumber}
                from={fromNumber}
                timeoutSec={dialTimeoutSec}
                fromNumbers={fromNumbers}
                fromLoading={fromLoading}
                fromError={fromError}
                onChange={setDialNumber}
                onFromChange={chooseFromNumber}
                onTimeoutChange={setDialTimeoutSec}
                onCall={() => void placeSoftphoneCall()}
                onApplyProfile={(profileId) => void configureOutboundProfile(profileId)}
                onRefreshNumbers={() => void loadOutboundNumbers()}
                profileBusy={profileBusy}
                busy={softphoneBusy}
                disabled={Boolean(softphoneCallId)}
                recent={recentNumbers}
                status={softphoneCallId ? "" : "Calls from your browser using the bound carrier number."}
              />
              <div className="border-t border-border p-4 space-y-4">
                <AudioProcessingSettings
                  value={audioOptions}
                  disabled={Boolean(softphoneCallId)}
                  onChange={setAudioOptions}
                />
                <MicrophoneTest
                  options={audioOptions}
                  workletURL={workletURL}
                  disabled={Boolean(softphoneCallId)}
                />
              </div>
            </div>
          ) : selected ? (
            <div className="p-4 space-y-5">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="text-xs text-text-dim">Selected call</div>
                  <div className="mt-1 text-lg font-semibold truncate">{selected.toNumber || selected.id}</div>
                  <div className="mt-1 text-xs text-text-muted truncate">{selected.threadId}</div>
                </div>
                {selected.peerKind === "human"
                  && selected.direction === "inbound"
                  && selected.status === "pending"
                  && softphoneCallId !== selected.id ? (
                  <button
                    type="button"
                    disabled={softphoneBusy}
                    onClick={() => void answerSoftphoneCall(selected)}
                    className="h-8 px-3 rounded bg-accent text-bg text-xs font-medium disabled:opacity-40"
                  >
                    Answer
                  </button>
                ) : null}
                <button
                  type="button"
                  disabled={!LIVE_STATUSES.has(selected.status) || ending === selected.id}
                  onClick={() => { if (softphoneCallId === selected.id) endSoftphone(); void hangup(selected); }}
                  className="h-8 px-3 rounded bg-error text-bg text-xs font-medium disabled:opacity-40"
                >
                  Hang up
                </button>
              </div>

              {softphoneCallId === selected.id ? (
                <div className="rounded border border-border bg-bg-muted/50 p-3 space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className={softphoneState === "live" ? "text-success" : "text-text-muted"}>
                        <PhoneIcon size={15} />
                      </span>
                      <div className="min-w-0">
                        <div className="text-xs font-medium truncate">
                          {selected.status === "answered" || selected.status === "in-progress"
                            ? "Connected"
                            : selected.status === "ringing"
                              ? "Destination ringing"
                              : selected.status === "initiated"
                                ? "Dialing — waiting for answer"
                                : selected.status === "no-answer"
                                  ? "No answer"
                                  : softphoneState === "live"
                                    ? "Browser audio ready"
                                    : softphoneState === "connecting"
                                      ? "Connecting browser audio…"
                                      : softphoneState === "reconnecting"
                                        ? "Reconnecting audio…"
                                        : "Audio ended"}
                          {softphoneDetail ? <span className="text-text-muted"> — {softphoneDetail}</span> : null}
                        </div>
                        <div className="text-xs text-text-muted tabular-nums">
                          {duration(selected, now) || "just started"}
                          {selected.recordingMode === "always" && (Boolean(selected.answeredAt) || selected.status === "answered" || selected.status === "in-progress")
                            ? <span className="text-error font-medium"> · REC</span> : null}
                        </div>
                      </div>
                    </div>
                    <button
                      type="button"
                      onClick={toggleMute}
                      disabled={softphoneState !== "live"}
                      className={`h-8 px-3 rounded border text-xs disabled:opacity-40 ${muted ? "border-warn/40 bg-warn/10 text-warn" : "border-border hover:bg-bg-muted"}`}
                    >
                      {muted ? "Unmute" : "Mute"}
                    </button>
                  </div>
                  <LevelMeter label="Mic" value={muted ? 0 : levels.mic} />
                  <LevelMeter label="Caller" value={levels.speaker} />
                  {diagnostics ? (
                    <div className="text-xs text-text-dim tabular-nums">
                      RTT {diagnostics.rttMs === null ? "–" : `${diagnostics.rttMs} ms`}
                      {` · buffer ${diagnostics.queueMs}/${diagnostics.targetMs} ms (max ${diagnostics.maxQueueMs})`}
                      {` · underruns ${diagnostics.underruns}`}
                      {` · dropped ${diagnostics.droppedMs} ms`}
                      {` · ${Math.round(diagnostics.audioContextRate / 1000)} kHz`}
                      {diagnostics.micActiveRmsDbfs !== null ? ` · mic ${diagnostics.micActiveRmsDbfs.toFixed(1)} dBFS` : ""}
                      {diagnostics.micPostPeakDbfs !== null ? ` · sent peak ${diagnostics.micPostPeakDbfs.toFixed(1)}` : ""}
                      {diagnostics.micLimiterReductionDb > 0.1 ? ` · limiter ${diagnostics.micLimiterReductionDb.toFixed(1)} dB` : ""}
                      {diagnostics.dropEvents.length > 0 ? ` · drop events ${diagnostics.dropEvents.length}` : ""}
                      {` · AGC ${appliedSetting(diagnostics.autoGainControl)}`}
                    </div>
                  ) : null}
                  <p className="text-xs text-text-dim">
                    Use headphones — speaker audio picked up by the microphone echoes back to the caller.
                  </p>
                </div>
              ) : null}

              <AudioProcessingSettings
                value={audioOptions}
                disabled={Boolean(softphoneCallId)}
                onChange={setAudioOptions}
              />

              <MicrophoneTest
                options={audioOptions}
                workletURL={workletURL}
                disabled={Boolean(softphoneCallId)}
              />

              {selected.peerKind === "human" ? <PersistedAudioDiagnostics call={selected} /> : null}

              <dl className="grid gap-x-3 gap-y-3 text-sm" style={{ gridTemplateColumns: DETAILS_COLUMNS }}>
                <dt className="text-text-dim">Status</dt>
                <dd>
                  <span className={`inline-flex rounded border px-2 py-0.5 text-xs ${statusClass(selected.status)}`}>
                    {selected.status || "unknown"}
                  </span>
                </dd>
                <dt className="text-text-dim">Media</dt>
                <dd>
                  <span className={`inline-flex rounded border px-2 py-0.5 text-xs ${mediaStatusClass(selected.mediaStatus)}`}>
                    {selected.mediaStatus || "idle"}
                  </span>
                </dd>
                <dt className="text-text-dim">Media connected</dt>
                <dd className="truncate">{selected.mediaConnectedAt || "-"}</dd>
                <dt className="text-text-dim">Media ended</dt>
                <dd className="truncate">{selected.mediaDisconnectedAt || (selected.mediaStatus === "connected" ? "live" : "-")}</dd>
                <dt className="text-text-dim">Ended by</dt>
                <dd className="truncate">{selected.mediaCloseLeg || "-"}</dd>
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
                <dt className="text-text-dim">Termination</dt>
                <dd className="truncate text-xs">
                  {[selected.terminationCause, selected.terminationCode, selected.terminationInitiator].filter(Boolean).join(" / ") || "-"}
                </dd>
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
                      : selected.recordingMode === "always" && !selected.answeredAt
                        ? "Not recorded — the call was never answered."
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
                        <div className="mt-1 truncate font-mono text-xs text-text-dim">{recording.provider_recording_id}</div>
                      </div>
                      {recording.storage_status === "failed" || recording.storage_status === "provider_only" ? (
                        <button type="button" disabled={recordingBusy === recording.id} onClick={() => void recordingAction(recording, "retry")} className="h-8 px-3 rounded border border-border text-xs disabled:opacity-50">{recording.storage_status === "provider_only" ? "Copy to Storage" : "Retry"}</button>
                      ) : null}
                      <button type="button" disabled={recordingBusy === recording.id} onClick={() => void recordingAction(recording, "delete")} className="h-8 px-3 rounded border border-error/40 text-error text-xs disabled:opacity-50">Delete</button>
                    </div>
                    <RecordingPlayer recording={recording} />
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
              {selected.mediaErrorMessage ? (
                <div className="rounded border border-warn/30 bg-warn/10 p-3 text-sm text-warn">
                  <div className="font-medium">Media bridge issue</div>
                  <div className="mt-1 whitespace-pre-wrap">{selected.mediaErrorMessage}</div>
                  {(selected.mediaCloseLeg || selected.mediaCloseCode || selected.mediaCloseReason) ? (
                    <div className="mt-1 text-xs opacity-80">
                      {[selected.mediaCloseLeg, selected.mediaCloseCode ? `Code ${selected.mediaCloseCode}` : "", selected.mediaCloseReason].filter(Boolean).join(" / ")}
                    </div>
                  ) : null}
                </div>
              ) : null}
            </div>
          ) : (
            <div className="h-full flex items-center justify-center text-sm text-text-muted" style={{ minHeight: "14rem" }}>
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
  recommended_compliance_id?: string;
  recommended_compliance_name?: string;
  matching_compliance_profiles?: number;
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

interface ConnectedRoute {
  id: string;
  agent_id: number;
  agent_name?: string;
  enabled: boolean;
  answer_mode: string;
  voice?: string;
  recording_mode: string;
  inbound_transport: "programmable_websocket" | "sip_direct";
  transport_configured: boolean;
}

interface ConnectedNumber {
  phone_number: string;
  provider: string;
  provider_number_id?: string;
  friendly_name?: string;
  capabilities?: string[] | null;
  carrier_status?: string;
  route_status: "enabled" | "disabled" | "not_configured";
  route?: ConnectedRoute;
  voice_webhook_status: string;
  status_callback_status: string;
  routing_health: string;
  health_message?: string;
  outbound: OutboundReadiness;
}

interface OutboundProfileOption {
  id: string;
  name?: string;
  enabled: boolean;
  destinations?: string[];
  daily_spend_limit?: string;
  daily_spend_limit_enabled?: boolean;
}

interface OutboundReadiness {
  required: boolean;
  status: "ready" | "auto_configurable" | "selection_required" | "setup_required" | "error";
  application_id?: string;
  profile_id?: string;
  recommended_profile_id?: string;
  profiles: OutboundProfileOption[];
  message?: string;
}

interface ConnectedNumbersResponse {
  provider: string;
  count: number;
  numbers: ConnectedNumber[];
  direct_sip?: {
    supported?: boolean;
    enabled: boolean;
    ready: boolean;
    available?: boolean;
    managed?: boolean;
    endpoint?: string;
    transport?: string;
    srtp?: string;
    reason?: string;
  };
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

function healthLabel(value: string): string {
  switch (value) {
    case "configured": return "Configured";
    case "not_configured": return "Not configured";
    case "mismatch": return "Mismatch";
    case "missing": return "Missing";
    case "disabled": return "Disabled";
    case "unsupported": return "Unsupported";
    case "unknown": return "Unverified";
    case "sip_direct": return "Direct SIP";
    case "not_applicable": return "N/A";
    default: return value || "Unknown";
  }
}

function healthClass(value: string): string {
  if (value === "configured" || value === "healthy" || value === "sip_direct") return "border-success/30 bg-success/10 text-success";
  if (value === "mismatch" || value === "missing" || value === "degraded") return "border-error/30 bg-error/10 text-error";
  if (value === "unknown" || value === "unverified") return "border-warn/30 bg-warn/10 text-warn";
  return "border-border bg-bg-muted text-text-muted";
}

function NumbersView({ projectId }: NativePanelProps) {
  const connectedRequestRef = useRef(0);
  const [connectedNumbers, setConnectedNumbers] = useState<ConnectedNumber[]>([]);
  const [connectedProvider, setConnectedProvider] = useState("");
  const [connectedLoading, setConnectedLoading] = useState(false);
  const [connectedError, setConnectedError] = useState("");
  const [directSIP, setDirectSIP] = useState<ConnectedNumbersResponse["direct_sip"]>();
  const [transportDrafts, setTransportDrafts] = useState<Record<string, string>>({});
  const [transportSaving, setTransportSaving] = useState("");
  const [transportStatus, setTransportStatus] = useState("");
  const [answerModeSaving, setAnswerModeSaving] = useState("");
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

  const loadConnected = useCallback(async () => {
    const requestId = ++connectedRequestRef.current;
    setConnectedLoading(true);
    setConnectedError("");
    try {
      const data = await postJSON<ConnectedNumbersResponse>(endpoint("/numbers/connected"), {});
      if (requestId !== connectedRequestRef.current) return;
      setConnectedProvider(data.provider || "");
      setConnectedNumbers(data.numbers ?? []);
      setDirectSIP(data.direct_sip);
      setTransportDrafts(Object.fromEntries(
        (data.numbers ?? [])
          .filter((number) => number.route)
          .map((number) => [number.route!.id, number.route!.inbound_transport || "programmable_websocket"]),
      ));
    } catch (e) {
      if (requestId !== connectedRequestRef.current) return;
      setConnectedNumbers([]);
      setConnectedProvider("");
      setDirectSIP(undefined);
      setConnectedError((e as Error).message || "Could not load connected numbers");
    } finally {
      if (requestId === connectedRequestRef.current) setConnectedLoading(false);
    }
  }, [endpoint]);

  useEffect(() => {
    setOffers([]);
    setProvider("");
    setSupportedTypes([]);
    setStatus("");
    setSelected(null);
    setAddressSid("");
    setBundleSid("");
    setAddresses([]);
    setBundles([]);
    setPurchaseResult("");
    void loadConnected();
  }, [loadConnected]);

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
    setBundleSid(offer.recommended_compliance_id ?? "");
    setAddresses([]);
    setBundles([]);
    if (offer.provider !== "twilio" && offer.provider !== "telnyx") return;
    setResourcesLoading(true);
    try {
      const [addressData, profileData] = await Promise.all([
        postJSON<{ addresses?: ProviderAddress[] }>(endpoint("/numbers/addresses/list"), { limit: 200 }),
        postJSON<{ profiles?: RegulatoryBundle[]; bundles?: RegulatoryBundle[] }>(endpoint("/numbers/regulatory/bundles/list"), {
          country: offer.country,
          number_type: offer.number_type,
          status: offer.provider === "twilio" ? "twilio-approved" : "approved",
          limit: 200,
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
      await loadConnected();
    } catch (e) {
      setPurchaseResult((e as Error).message || "Purchase failed");
    } finally {
      setPurchasing(false);
    }
  };

  const applyTransport = async (number: ConnectedNumber) => {
    if (!number.route) return;
    const routeId = number.route.id;
    const transport = transportDrafts[routeId] || number.route.inbound_transport || "programmable_websocket";
    setTransportSaving(routeId);
    setTransportStatus("");
    try {
      await postJSON(endpoint("/numbers/transport"), {
        route_id: routeId,
        inbound_transport: transport,
        configure: true,
      });
      setTransportStatus(`${number.phone_number} routing updated`);
    } catch (e) {
      setTransportStatus((e as Error).message || "Could not update inbound transport");
    } finally {
      await loadConnected();
      setTransportSaving("");
    }
  };

  // Flip an inbound number between agent answering and the browser softphone.
  // Switching back restores plain `agent` mode rather than realtime_immediate,
  // because we cannot know which directive the route previously wanted.
  const toggleBrowserRinging = async (number: ConnectedNumber) => {
    if (!number.route) return;
    const routeId = number.route.id;
    const next = number.route.answer_mode === "human_browser" ? "agent" : "human_browser";
    setAnswerModeSaving(routeId);
    setTransportStatus("");
    try {
      await postJSON(endpoint("/numbers/answer-mode"), { route_id: routeId, answer_mode: next });
      setTransportStatus(next === "human_browser"
        ? `${number.phone_number} now rings in the browser`
        : `${number.phone_number} now routes to the agent`);
    } catch (e) {
      setTransportStatus((e as Error).message || "Could not update answer mode");
    } finally {
      await loadConnected();
      setAnswerModeSaving("");
    }
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <main className="min-h-0 flex-1 overflow-auto">
        <section className="border-b border-border">
          <header className="px-4 py-3 flex flex-wrap items-center gap-3 border-b border-border">
            <div className="min-w-0 flex-1">
              <h2 className="text-sm font-semibold">Connected numbers</h2>
              <p className="mt-0.5 truncate text-xs text-text-muted">
                {connectedLoading
                  ? "Loading carrier numbers..."
                  : connectedError
                    ? "Carrier numbers unavailable"
                    : `${connectedNumbers.length} number${connectedNumbers.length === 1 ? "" : "s"}${connectedProvider ? ` on ${connectedProvider}` : ""}`}
              </p>
              {directSIP?.supported ? (
                <p className="mt-0.5 truncate text-xs text-text-dim">
                  {directSIP.ready
                    ? `Direct SIP ready · ${directSIP.endpoint || "-"} · ${directSIP.srtp || "RTP"}`
                    : directSIP.available
                      ? `Direct SIP managed · starts when selected · ${directSIP.endpoint || "-"}`
                      : `Direct SIP unavailable · ${directSIP.reason || "host preflight failed"}`}
                </p>
              ) : null}
              {transportStatus ? <p className="mt-0.5 truncate text-xs text-text-muted">{transportStatus}</p> : null}
            </div>
            <button
              type="button"
              onClick={loadConnected}
              disabled={connectedLoading}
              className="h-8 px-3 rounded border border-border text-xs hover:bg-bg-muted disabled:opacity-50"
            >
              Refresh
            </button>
          </header>

          {connectedLoading && connectedNumbers.length === 0 ? (
            <div className="flex items-center justify-center px-6 text-sm text-text-muted" style={{ minHeight: "10rem" }}>
              Loading connected numbers...
            </div>
          ) : connectedError ? (
            <div className="flex items-center justify-between gap-4 px-4 py-6">
              <div className="min-w-0 text-sm text-error whitespace-pre-wrap">{connectedError}</div>
              <button type="button" onClick={loadConnected} className="h-8 shrink-0 px-3 rounded border border-border text-xs hover:bg-bg-muted">Retry</button>
            </div>
          ) : connectedNumbers.length === 0 ? (
            <div className="flex items-center justify-center px-6 text-center text-sm text-text-muted" style={{ minHeight: "10rem" }}>
              No phone numbers found in the bound carrier account.
            </div>
          ) : (
            <div className="overflow-x-auto">
              <div style={{ minWidth: "90rem" }}>
                <div className="grid gap-3 px-4 py-2 border-b border-border text-xs uppercase tracking-normal text-text-dim" style={{ gridTemplateColumns: CONNECTED_NUMBER_COLUMNS }}>
                  <div>Number</div>
                  <div>Provider</div>
                  <div>Capabilities</div>
                  <div>Route</div>
                  <div>Assigned agent</div>
                  <div>Mode</div>
                  <div>Transport</div>
                  <div>Routing health</div>
                </div>
                {connectedNumbers.map((number) => (
                  <div
                    key={`${number.provider}-${number.provider_number_id || number.phone_number}`}
                    className="grid gap-3 items-center px-4 py-3 border-b border-border/70 text-sm"
                    style={{ gridTemplateColumns: CONNECTED_NUMBER_COLUMNS }}
                  >
                    <div className="min-w-0">
                      <div className="truncate font-medium">{number.phone_number}</div>
                      <div className="truncate text-xs text-text-muted">{number.friendly_name || "-"}</div>
                      <div className="truncate font-mono text-xs text-text-dim" title={number.provider_number_id}>{compactId(number.provider_number_id || "")}</div>
                    </div>
                    <div className="min-w-0">
                      <div className="truncate capitalize">{number.provider}</div>
                      <div className="truncate text-xs text-text-dim">{number.carrier_status || "owned"}</div>
                    </div>
                    <div className="truncate text-xs text-text-muted">{(number.capabilities ?? []).length > 0 ? number.capabilities!.join(", ") : "-"}</div>
                    <div className="min-w-0">
                      {number.route ? (
                        <>
                          <div className="truncate font-mono text-xs" title={number.route.id}>{compactId(number.route.id)}</div>
                          <span className={`mt-1 inline-flex rounded border px-2 py-0.5 text-xs ${number.route.enabled ? "border-success/30 bg-success/10 text-success" : "border-border bg-bg-muted text-text-muted"}`}>
                            {number.route.enabled ? "Enabled" : "Disabled"}
                          </span>
                        </>
                      ) : <span className="text-xs text-text-muted">Not routed</span>}
                    </div>
                    <div className="min-w-0">
                      {number.route ? (
                        <>
                          <div className="truncate">{number.route.agent_name || `Agent ${number.route.agent_id}`}</div>
                          <div className="truncate text-xs text-text-dim">ID {number.route.agent_id}</div>
                        </>
                      ) : <span className="text-xs text-text-muted">-</span>}
                    </div>
                    <div className="min-w-0">
                      {number.route ? (
                        <div className="flex min-w-0 items-center gap-1.5">
                          <select
                            aria-label={`Inbound transport for ${number.phone_number}`}
                            value={transportDrafts[number.route.id] || number.route.inbound_transport || "programmable_websocket"}
                            onChange={(event) => setTransportDrafts((current) => ({ ...current, [number.route!.id]: event.target.value }))}
                            disabled={transportSaving === number.route.id}
                            className="h-8 min-w-0 flex-1 rounded border border-border bg-bg px-2 text-xs"
                          >
                            <option value="programmable_websocket">Voice API</option>
                            {(number.provider === "twilio" || number.provider === "telnyx") ? (
                              <option value="sip_direct" disabled={!directSIP?.available && !directSIP?.ready}>Direct SIP</option>
                            ) : null}
                          </select>
                          <button
                            type="button"
                            onClick={() => void applyTransport(number)}
                            disabled={transportSaving === number.route.id
                              || ((transportDrafts[number.route.id] || number.route.inbound_transport) === number.route.inbound_transport
                                && (number.route.inbound_transport !== "sip_direct" || number.route.transport_configured))}
                            className="h-8 shrink-0 rounded border border-border px-2 text-xs hover:bg-bg-muted disabled:opacity-40"
                          >
                            Apply
                          </button>
                        </div>
                      ) : <span className="text-xs text-text-muted">-</span>}
                    </div>
                    <div className="min-w-0">
                      {number.route ? (
                        <>
                          <div className="truncate capitalize">{number.route.answer_mode.replaceAll("_", " ")}</div>
                          <div className="truncate text-xs text-text-dim">{number.route.voice || "Default voice"}</div>
                          <button
                            type="button"
                            disabled={answerModeSaving === number.route.id}
                            onClick={() => void toggleBrowserRinging(number)}
                            className="mt-1 h-7 rounded border border-border px-2 text-xs hover:bg-bg-muted disabled:opacity-40"
                          >
                            {number.route.answer_mode === "human_browser" ? "Route to agent" : "Ring in browser"}
                          </button>
                        </>
                      ) : <span className="text-xs text-text-muted">-</span>}
                    </div>
                    <div className="min-w-0">
                      <div className="flex items-center justify-between gap-2 text-xs">
                        <span className="text-text-dim">Voice</span>
                        <span className={`rounded border px-1.5 py-0.5 ${healthClass(number.voice_webhook_status)}`}>{healthLabel(number.voice_webhook_status)}</span>
                      </div>
                      <div className="mt-1 flex items-center justify-between gap-2 text-xs">
                        <span className="text-text-dim">Events</span>
                        <span className={`rounded border px-1.5 py-0.5 ${healthClass(number.status_callback_status)}`}>{healthLabel(number.status_callback_status)}</span>
                      </div>
                      {number.health_message ? (
                        <div className="mt-1 truncate text-xs text-text-muted" title={number.health_message}>{number.health_message}</div>
                      ) : null}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>

        <section>
          <header className="border-b border-border px-4 py-3">
            <h2 className="mb-3 text-sm font-semibold">Find a number</h2>
            <div className="flex flex-wrap items-end gap-3">
              <label className="flex-1" style={{ minWidth: "12rem", maxWidth: "28rem" }}>
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
              <div className="text-right text-xs text-text-muted" style={{ minWidth: "10rem" }}>
                <div>{provider ? `Carrier: ${provider}` : ""}</div>
                <div className="truncate">{status}</div>
              </div>
            </div>
          </header>

          {supportedTypes.length > 0 && numberType !== "any" && !supportedTypes.includes(numberType) ? (
            <div className="border-b border-warn/30 bg-warn/10 px-4 py-2 text-xs text-warn">
              {provider} supports {supportedTypes.join(", ")} number searches.
            </div>
          ) : null}
          {purchaseResult ? (
            <div className="border-b border-border px-4 py-3 text-sm">{purchaseResult}</div>
          ) : null}
          {offers.length === 0 ? (
            <div className="flex items-center justify-center px-6 text-center text-sm text-text-muted" style={{ minHeight: "14rem" }}>
              {status || "Search the bound carrier's live number inventory."}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <div style={{ minWidth: "64rem" }}>
                <div className="grid gap-3 px-4 py-2 border-b border-border text-xs uppercase tracking-normal text-text-dim" style={{ gridTemplateColumns: NUMBER_COLUMNS }}>
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
                    className="grid gap-3 items-center px-4 py-3 border-b border-border/70 text-sm"
                    style={{ gridTemplateColumns: NUMBER_COLUMNS }}
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
            </div>
          )}
        </section>
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
              {selected.recommended_compliance_name ? `; approved profile: ${selected.recommended_compliance_name}` : ""}
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
  const layout = usePanelWidth();
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
    <div
      ref={layout.ref}
      className="h-full min-h-0 grid bg-bg text-text"
      style={{ gridTemplateColumns: layout.width >= 900 ? "minmax(0,1fr) 22rem" : "minmax(0,1fr)" }}
    >
      <section
        className="min-h-0 min-w-0 overflow-auto border-r border-b border-border"
        style={{ borderRightWidth: layout.width >= 900 ? 1 : 0, borderBottomWidth: layout.width >= 900 ? 0 : 1 }}
      >
        <header className="h-12 px-4 border-b border-border flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">Provider addresses</h2>
          <span className="truncate text-xs text-text-muted">{status}</span>
        </header>
        <div style={{ minWidth: "48rem" }}>
          <div className="grid gap-3 px-4 py-2 border-b border-border text-xs uppercase tracking-normal text-text-dim" style={{ gridTemplateColumns: ADDRESS_COLUMNS }}>
            <div>Name</div><div>Address</div><div>Country</div><div>Validation</div><div>SID</div>
          </div>
          {addresses.map((address) => (
            <div key={address.sid} className="grid gap-3 px-4 py-3 border-b border-border/70 text-sm" style={{ gridTemplateColumns: ADDRESS_COLUMNS }}>
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
  const layout = usePanelWidth();
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
    <div
      ref={layout.ref}
      className="h-full min-h-0 grid bg-bg text-text"
      style={{ gridTemplateColumns: layout.width >= 1200 ? "20rem minmax(0,1fr) 22rem" : "minmax(0,1fr)" }}
    >
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
        ) : <div className="h-full flex items-center justify-center text-sm text-text-muted" style={{ minHeight: "18rem" }}>Select a compliance profile</div>}
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
        {requirements ? <pre className="overflow-auto whitespace-pre-wrap border-t border-border pt-3 text-xs leading-5 text-text-muted" style={{ maxHeight: "30rem" }}>{JSON.stringify(requirements, null, 2)}</pre> : null}
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

interface RoutingNode {
  id: string;
  type: string;
  label?: string;
  next?: string;
  branches?: Record<string, string>;
  config?: Record<string, unknown>;
}

interface RoutingDraft { entry: string; nodes: RoutingNode[] }
interface RoutingFlow {
  id: string;
  name: string;
  description?: string;
  draft: RoutingDraft;
  published_version_id?: string;
  generated?: boolean;
  updated_at?: string;
}
interface RoutingDestination {
  id: string;
  name: string;
  kind: "browser" | "agent" | "ai" | "pstn" | "sip" | "voicemail";
  config: Record<string, unknown>;
  enabled: boolean;
}
interface RingMember { destination_id: string; enabled: boolean; position?: number; priority?: number; weight?: number; timeout_sec?: number }
interface RoutingGroup { id: string; name: string; strategy: string; timeout_sec: number; members: RingMember[]; enabled: boolean }
interface RoutingRoute { id: string; phone_number: string; flow_id?: string; published_flow_version_id?: string; enabled: boolean }
interface RoutingSnapshot { flows: RoutingFlow[]; destinations: RoutingDestination[]; ring_groups: RoutingGroup[]; routes: RoutingRoute[]; node_types: string[] }
interface RoutingSimulation { valid: boolean; errors?: string[]; trace?: Array<{ node_id: string; node_type: string; label?: string; outcome: string; next?: string }>; destination_id?: string; ring_group_id?: string; terminal_type?: string }

const NODE_LABELS: Record<string, string> = {
  announcement: "Announcement", schedule: "Business hours", caller_match: "Caller rule",
  dtmf_menu: "Keypad menu", destination: "Destination", ring_group: "Ring group",
  voicemail: "Voicemail", reject: "Reject", hangup: "Hang up",
};

function RoutingView({ projectId }: NativePanelProps) {
  const [snapshot, setSnapshot] = useState<RoutingSnapshot>({ flows: [], destinations: [], ring_groups: [], routes: [], node_types: [] });
  const [selectedId, setSelectedId] = useState("");
  const [draft, setDraft] = useState<RoutingDraft>({ entry: "hangup", nodes: [{ id: "hangup", type: "hangup", label: "End call" }] });
  const [flowName, setFlowName] = useState("Main line");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [simulation, setSimulation] = useState<RoutingSimulation | null>(null);
  const [caller, setCaller] = useState("+33600000000");
  const [routeId, setRouteId] = useState("");
  const [destinationForm, setDestinationForm] = useState({ name: "Browser operator", kind: "browser", target: "", directive: "" });
  const [groupForm, setGroupForm] = useState({ name: "Team", strategy: "simultaneous", timeout_sec: 20, members: [] as string[] });

  const api = useCallback((path: string) => `${API}${path}${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`, [projectId]);
  const load = useCallback(async (preferred = "") => {
    try {
      const res = await fetch(api("/routing/snapshot"), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as RoutingSnapshot;
      setSnapshot(data);
      const id = preferred || selectedId || data.flows.find((flow) => !flow.generated)?.id || data.flows[0]?.id || "";
      if (id) {
        const flow = data.flows.find((item) => item.id === id);
        if (flow) { setSelectedId(flow.id); setFlowName(flow.name); setDescription(flow.description || ""); setDraft(flow.draft); }
      }
      if (!routeId && data.routes[0]) setRouteId(data.routes[0].id);
    } catch (error) { setStatus((error as Error).message || "Could not load routing"); }
  }, [api, routeId, selectedId]);

  useEffect(() => { void load(); }, [projectId]);

  const selectFlow = (flow: RoutingFlow) => {
    setSelectedId(flow.id); setFlowName(flow.name); setDescription(flow.description || "");
    setDraft(flow.draft); setSimulation(null); setStatus("");
  };
  const newFlow = () => {
    const first = snapshot.destinations.find((item) => item.enabled);
    const node: RoutingNode = first
      ? { id: "answer", type: "destination", label: first.name, config: { destination_id: first.id } }
      : { id: "hangup", type: "hangup", label: "End call" };
    setSelectedId(""); setFlowName("New call flow"); setDescription(""); setDraft({ entry: node.id, nodes: [node] }); setSimulation(null); setStatus("New draft");
  };
  const saveFlow = async () => {
    setBusy(true);
    try {
      const saved = await postJSON<RoutingFlow>(api("/routing/flows/save"), { id: selectedId, name: flowName, description, draft });
      setSelectedId(saved.id); setStatus("Draft saved"); await load(saved.id);
    } catch (error) { setStatus((error as Error).message || "Could not save flow"); } finally { setBusy(false); }
  };
  const publish = async () => {
    if (!selectedId) { setStatus("Save the draft before publishing"); return; }
    setBusy(true);
    try {
      const result = await postJSON<{ valid: boolean; errors?: string[]; version?: { id: string } }>(api("/routing/flows/publish"), { id: selectedId });
      setStatus(result.valid ? `Published ${result.version?.id || "new version"}` : (result.errors || []).join(" · "));
      await load(selectedId);
    } catch (error) { setStatus((error as Error).message || "Could not publish flow"); } finally { setBusy(false); }
  };
  const simulate = async () => {
    setBusy(true);
    try {
      const result = await postJSON<RoutingSimulation>(api("/routing/flows/simulate"), { id: selectedId, draft, context: { caller } });
      setSimulation(result); setStatus(result.valid ? "Simulation completed" : (result.errors || []).join(" · "));
    } catch (error) { setStatus((error as Error).message || "Simulation failed"); } finally { setBusy(false); }
  };
  const assign = async () => {
    if (!routeId || !selectedId) return;
    setBusy(true);
    try { await postJSON(api("/routing/routes/assign"), { route_id: routeId, flow_id: selectedId }); setStatus("Published flow assigned to number"); await load(selectedId); }
    catch (error) { setStatus((error as Error).message || "Could not assign flow"); } finally { setBusy(false); }
  };

  const updateNode = (id: string, patch: Partial<RoutingNode>) => setDraft((current) => ({ ...current, nodes: current.nodes.map((node) => node.id === id ? { ...node, ...patch } : node) }));
  const addNode = (type: string) => {
    let index = draft.nodes.length + 1;
    let id = `${type}_${index}`;
    while (draft.nodes.some((node) => node.id === id)) { index += 1; id = `${type}_${index}`; }
    const node: RoutingNode = { id, type, label: NODE_LABELS[type] || type, config: {} };
    if (type === "destination" && snapshot.destinations[0]) node.config = { destination_id: snapshot.destinations[0].id };
    if (type === "ring_group" && snapshot.ring_groups[0]) node.config = { ring_group_id: snapshot.ring_groups[0].id };
    if (type === "announcement") node.config = { text: "Welcome. Please wait while we connect you." };
    if (type === "schedule") { node.config = { timezone: "Europe/Paris", days: ["mon", "tue", "wed", "thu", "fri"], start: "09:00", end: "18:00" }; node.branches = { open: "", closed: "" }; }
    if (type === "dtmf_menu") { node.config = { prompt: "Press 1 for sales, or 2 for support." }; node.branches = { "1": "", "2": "", default: "" }; }
    if (type === "caller_match") { node.config = { prefixes: ["+33"] }; node.branches = { match: "", default: "" }; }
    setDraft((current) => ({ ...current, nodes: [...current.nodes, node] }));
  };
  const removeNode = (id: string) => setDraft((current) => ({ ...current, entry: current.entry === id ? (current.nodes.find((node) => node.id !== id)?.id || "") : current.entry, nodes: current.nodes.filter((node) => node.id !== id) }));

  const saveDestination = async () => {
    const kind = destinationForm.kind;
    let config: Record<string, unknown> = {};
    if (kind === "agent" || kind === "ai") config = { agent_id: Number(destinationForm.target), directive: destinationForm.directive };
    if (kind === "pstn") config = { phone_number: destinationForm.target };
    if (kind === "sip") config = { uri: destinationForm.target };
    setBusy(true);
    try { await postJSON(api("/routing/destinations/save"), { name: destinationForm.name, kind, config, enabled: true }); setStatus("Destination created"); await load(selectedId); }
    catch (error) { setStatus((error as Error).message || "Could not create destination"); } finally { setBusy(false); }
  };
  const saveGroup = async () => {
    setBusy(true);
    try {
      await postJSON(api("/routing/ring-groups/save"), { name: groupForm.name, strategy: groupForm.strategy, timeout_sec: groupForm.timeout_sec, members: groupForm.members.map((destination_id) => ({ destination_id, enabled: true })) });
      setStatus("Ring group created"); await load(selectedId);
    } catch (error) { setStatus((error as Error).message || "Could not create ring group"); } finally { setBusy(false); }
  };

  const selected = snapshot.flows.find((flow) => flow.id === selectedId);
  return (
    <div className="h-full min-h-0 grid bg-bg text-text" style={{ gridTemplateColumns: "17rem minmax(30rem,1fr) 21rem" }}>
      <aside className="min-h-0 overflow-auto border-r border-border">
        <div className="sticky top-0 z-10 border-b border-border bg-bg p-3">
          <div className="flex items-center justify-between gap-2"><div><h2 className="text-sm font-semibold">Call flows</h2><p className="text-xs text-text-muted">Published versions route live calls.</p></div><button type="button" onClick={newFlow} className="h-8 rounded border border-border px-2 text-xs">New</button></div>
        </div>
        {snapshot.flows.map((flow) => (
          <button type="button" key={flow.id} onClick={() => selectFlow(flow)} className={`w-full border-b border-border/70 p-3 text-left hover:bg-bg-muted/60 ${selectedId === flow.id ? "bg-bg-muted" : ""}`}>
            <div className="flex items-center justify-between gap-2"><span className="truncate text-sm font-medium">{flow.name}</span><span className={`rounded border px-1.5 py-0.5 text-xs ${flow.published_version_id ? "border-success/30 bg-success/10 text-success" : "border-border text-text-muted"}`}>{flow.published_version_id ? "Live" : "Draft"}</span></div>
            <div className="mt-1 truncate text-xs text-text-dim">{flow.generated ? "Existing route" : `${flow.draft.nodes.length} nodes`}</div>
          </button>
        ))}
      </aside>

      <main className="min-h-0 overflow-auto">
        <header className="sticky top-0 z-10 border-b border-border bg-bg p-3">
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-0 flex-1"><input value={flowName} onChange={(event) => setFlowName(event.target.value)} className="h-8 w-full bg-transparent text-base font-semibold outline-none" aria-label="Flow name" /><input value={description} onChange={(event) => setDescription(event.target.value)} placeholder="Describe when this flow should be used" className="h-7 w-full bg-transparent text-xs text-text-muted outline-none" /></div>
            <button type="button" disabled={busy} onClick={simulate} className="h-8 rounded border border-border px-3 text-xs disabled:opacity-50">Simulate</button>
            <button type="button" disabled={busy} onClick={saveFlow} className="h-8 rounded border border-border px-3 text-xs disabled:opacity-50">Save draft</button>
            <button type="button" disabled={busy || !selectedId} onClick={publish} className="h-8 rounded bg-accent px-3 text-xs font-medium text-bg disabled:opacity-50">Publish</button>
          </div>
          <div className="mt-2 flex items-center gap-2 text-xs"><span className="truncate text-text-muted">{status || "Build and test the path before publishing."}</span>{selected?.published_version_id ? <span className="ml-auto shrink-0 font-mono text-text-dim">{selected.published_version_id}</span> : null}</div>
        </header>

        <section className="p-4">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <span className="text-xs font-semibold uppercase text-text-dim">Add step</span>
            {snapshot.node_types.map((type) => <button type="button" key={type} onClick={() => addNode(type)} className="h-7 rounded border border-border px-2 text-xs hover:bg-bg-muted">{NODE_LABELS[type] || type}</button>)}
          </div>
          <div className="space-y-3">
            {draft.nodes.map((node, index) => (
              <div key={node.id} className={`rounded border p-3 ${draft.entry === node.id ? "border-accent/60 bg-accent/5" : "border-border bg-bg-muted/20"}`}>
                <div className="flex items-center gap-2">
                  <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-border text-xs">{index + 1}</span>
                  <select value={node.type} onChange={(event) => updateNode(node.id, { type: event.target.value, config: {}, branches: {} })} className="h-8 rounded border border-border bg-bg px-2 text-xs">{snapshot.node_types.map((type) => <option key={type} value={type}>{NODE_LABELS[type] || type}</option>)}</select>
                  <input value={node.label || ""} onChange={(event) => updateNode(node.id, { label: event.target.value })} className="h-8 min-w-0 flex-1 rounded border border-border bg-bg px-2 text-sm" placeholder="Step label" />
                  <button type="button" onClick={() => setDraft((current) => ({ ...current, entry: node.id }))} className="h-8 rounded border border-border px-2 text-xs">{draft.entry === node.id ? "Entry" : "Set entry"}</button>
                  <button type="button" onClick={() => removeNode(node.id)} className="h-8 rounded border border-danger/30 px-2 text-xs text-danger">Remove</button>
                </div>
                <NodeConfiguration node={node} nodes={draft.nodes} destinations={snapshot.destinations} groups={snapshot.ring_groups} update={(patch) => updateNode(node.id, patch)} />
              </div>
            ))}
          </div>
          {simulation ? (
            <section className={`mt-4 rounded border p-3 ${simulation.valid ? "border-success/30 bg-success/5" : "border-danger/30 bg-danger/5"}`}>
              <div className="flex items-center justify-between"><h3 className="text-sm font-semibold">Simulation trace</h3><span className="text-xs">{simulation.valid ? "Valid path" : "Needs attention"}</span></div>
              {simulation.errors?.length ? <ul className="mt-2 list-disc pl-5 text-xs text-danger">{simulation.errors.map((error) => <li key={error}>{error}</li>)}</ul> : null}
              <div className="mt-3 flex flex-wrap items-center gap-1">{simulation.trace?.map((step, index) => <div key={`${step.node_id}-${index}`} className="flex items-center gap-1"><span className="rounded border border-border bg-bg px-2 py-1 text-xs">{step.label || step.node_id} <span className="text-text-dim">· {step.outcome}</span></span>{index < (simulation.trace?.length || 0) - 1 ? <span className="text-text-dim">→</span> : null}</div>)}</div>
            </section>
          ) : null}
        </section>
      </main>

      <aside className="min-h-0 overflow-auto border-l border-border p-3 space-y-4">
        <section className="rounded border border-border p-3">
          <h3 className="text-sm font-semibold">Test and assign</h3>
          <Field label="Simulated caller" value={caller} onChange={setCaller} />
          <label className="mt-2 block"><span className="mb-1 block text-xs text-text-muted">Inbound number</span><select value={routeId} onChange={(event) => setRouteId(event.target.value)} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm"><option value="">Select a number</option>{snapshot.routes.map((route) => <option value={route.id} key={route.id}>{route.phone_number}{route.flow_id === selectedId ? " · assigned" : ""}</option>)}</select></label>
          <button type="button" onClick={assign} disabled={busy || !selected?.published_version_id || !routeId} className="mt-2 h-9 w-full rounded border border-border text-sm disabled:opacity-50">Assign published flow</button>
        </section>

        <section className="rounded border border-border p-3 space-y-2">
          <h3 className="text-sm font-semibold">New destination</h3>
          <Field label="Name" value={destinationForm.name} onChange={(name) => setDestinationForm({ ...destinationForm, name })} />
          <label className="block"><span className="mb-1 block text-xs text-text-muted">Type</span><select value={destinationForm.kind} onChange={(event) => setDestinationForm({ ...destinationForm, kind: event.target.value })} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm"><option value="browser">Browser user</option><option value="ai">AI agent</option><option value="agent">Agent offer</option><option value="pstn">External number</option><option value="sip">SIP endpoint</option><option value="voicemail">Voicemail</option></select></label>
          {destinationForm.kind === "agent" || destinationForm.kind === "ai" ? <Field label="Agent ID" value={destinationForm.target} onChange={(target) => setDestinationForm({ ...destinationForm, target })} type="number" /> : null}
          {destinationForm.kind === "pstn" ? <Field label="Telephone (E.164)" value={destinationForm.target} onChange={(target) => setDestinationForm({ ...destinationForm, target })} /> : null}
          {destinationForm.kind === "sip" ? <Field label="SIP URI" value={destinationForm.target} onChange={(target) => setDestinationForm({ ...destinationForm, target })} /> : null}
          {destinationForm.kind === "ai" ? <label className="block"><span className="mb-1 block text-xs text-text-muted">AI directive</span><textarea rows={3} value={destinationForm.directive} onChange={(event) => setDestinationForm({ ...destinationForm, directive: event.target.value })} className="w-full rounded border border-border bg-bg px-2 py-1 text-xs" /></label> : null}
          {destinationForm.kind === "pstn" ? <p className="rounded border border-warning/30 bg-warning/5 p-2 text-xs text-warning">External numbers create a second billable carrier leg.</p> : null}
          <button type="button" onClick={saveDestination} disabled={busy} className="h-9 w-full rounded bg-accent text-sm font-medium text-bg disabled:opacity-50">Create destination</button>
          <div className="space-y-1 pt-1">{snapshot.destinations.map((item) => <div key={item.id} className="flex items-center justify-between gap-2 rounded bg-bg-muted/40 px-2 py-1 text-xs"><span className="truncate">{item.name}</span><span className="shrink-0 capitalize text-text-dim">{item.kind}</span></div>)}</div>
        </section>

        <section className="rounded border border-border p-3 space-y-2">
          <h3 className="text-sm font-semibold">New ring group</h3>
          <Field label="Name" value={groupForm.name} onChange={(name) => setGroupForm({ ...groupForm, name })} />
          <label className="block"><span className="mb-1 block text-xs text-text-muted">Strategy</span><select value={groupForm.strategy} onChange={(event) => setGroupForm({ ...groupForm, strategy: event.target.value })} className="h-9 w-full rounded border border-border bg-bg px-2 text-sm"><option value="simultaneous">Simultaneous</option><option value="sequential">Sequential</option><option value="round_robin">Round robin</option><option value="priority">Priority</option></select></label>
          <Field label="Ring timeout (seconds)" value={String(groupForm.timeout_sec)} onChange={(value) => setGroupForm({ ...groupForm, timeout_sec: Number(value) })} type="number" />
          <div><span className="mb-1 block text-xs text-text-muted">Members</span>{snapshot.destinations.map((item) => <label key={item.id} className="flex items-center gap-2 py-1 text-xs"><input type="checkbox" checked={groupForm.members.includes(item.id)} onChange={(event) => setGroupForm({ ...groupForm, members: event.target.checked ? [...groupForm.members, item.id] : groupForm.members.filter((id) => id !== item.id) })} /> <span>{item.name}</span><span className="ml-auto text-text-dim">{item.kind}</span></label>)}</div>
          <button type="button" onClick={saveGroup} disabled={busy || groupForm.members.length === 0} className="h-9 w-full rounded border border-border text-sm disabled:opacity-50">Create ring group</button>
        </section>
      </aside>
    </div>
  );
}

function NodeConfiguration({ node, nodes, destinations, groups, update }: { node: RoutingNode; nodes: RoutingNode[]; destinations: RoutingDestination[]; groups: RoutingGroup[]; update: (patch: Partial<RoutingNode>) => void }) {
  const options = nodes.filter((item) => item.id !== node.id);
  const setConfig = (key: string, value: unknown) => update({ config: { ...(node.config || {}), [key]: value } });
  const setBranch = (key: string, value: string) => update({ branches: { ...(node.branches || {}), [key]: value } });
  const NextSelect = ({ label = "Then", value = node.next || "", onChange = (next: string) => update({ next }) }: { label?: string; value?: string; onChange?: (value: string) => void }) => <label className="block"><span className="mb-1 block text-xs text-text-muted">{label}</span><select value={value} onChange={(event) => onChange(event.target.value)} className="h-8 w-full rounded border border-border bg-bg px-2 text-xs"><option value="">Select next step</option>{options.map((item) => <option key={item.id} value={item.id}>{item.label || item.id}</option>)}</select></label>;
  return (
    <div className="mt-3 grid gap-3 md:grid-cols-2">
      {node.type === "announcement" ? <label className="block md:col-span-2"><span className="mb-1 block text-xs text-text-muted">Message</span><textarea rows={2} value={String(node.config?.text || "")} onChange={(event) => setConfig("text", event.target.value)} className="w-full rounded border border-border bg-bg px-2 py-1 text-sm" /></label> : null}
      {node.type === "destination" ? <label className="block"><span className="mb-1 block text-xs text-text-muted">Destination</span><select value={String(node.config?.destination_id || "")} onChange={(event) => setConfig("destination_id", event.target.value)} className="h-8 w-full rounded border border-border bg-bg px-2 text-xs"><option value="">Select destination</option>{destinations.map((item) => <option value={item.id} key={item.id}>{item.name} · {item.kind}</option>)}</select></label> : null}
      {node.type === "ring_group" ? <label className="block"><span className="mb-1 block text-xs text-text-muted">Ring group</span><select value={String(node.config?.ring_group_id || "")} onChange={(event) => setConfig("ring_group_id", event.target.value)} className="h-8 w-full rounded border border-border bg-bg px-2 text-xs"><option value="">Select group</option>{groups.map((item) => <option value={item.id} key={item.id}>{item.name} · {item.strategy}</option>)}</select></label> : null}
      {node.type === "schedule" ? <><Field label="Timezone" value={String(node.config?.timezone || "Europe/Paris")} onChange={(value) => setConfig("timezone", value)} /><div className="grid grid-cols-2 gap-2"><Field label="Opens" value={String(node.config?.start || "09:00")} onChange={(value) => setConfig("start", value)} type="time" /><Field label="Closes" value={String(node.config?.end || "18:00")} onChange={(value) => setConfig("end", value)} type="time" /></div><NextSelect label="When open" value={node.branches?.open || ""} onChange={(value) => setBranch("open", value)} /><NextSelect label="When closed" value={node.branches?.closed || ""} onChange={(value) => setBranch("closed", value)} /></> : null}
      {node.type === "dtmf_menu" ? <><label className="block md:col-span-2"><span className="mb-1 block text-xs text-text-muted">Prompt</span><textarea rows={2} value={String(node.config?.prompt || "")} onChange={(event) => setConfig("prompt", event.target.value)} className="w-full rounded border border-border bg-bg px-2 py-1 text-sm" /></label>{Object.keys(node.branches || {}).map((digit) => <NextSelect key={digit} label={digit === "default" ? "Invalid key / timeout" : `Key ${digit}`} value={node.branches?.[digit] || ""} onChange={(value) => setBranch(digit, value)} />)}</> : null}
      {node.type === "caller_match" ? <><Field label="Caller prefixes (comma separated)" value={Array.isArray(node.config?.prefixes) ? (node.config?.prefixes as string[]).join(", ") : ""} onChange={(value) => setConfig("prefixes", value.split(",").map((item) => item.trim()).filter(Boolean))} /><span /><NextSelect label="Match" value={node.branches?.match || ""} onChange={(value) => setBranch("match", value)} /><NextSelect label="Otherwise" value={node.branches?.default || ""} onChange={(value) => setBranch("default", value)} /></> : null}
      {node.type === "announcement" ? <NextSelect /> : null}
      {!(["announcement", "schedule", "caller_match", "dtmf_menu", "destination", "ring_group", "voicemail", "reject", "hangup"].includes(node.type)) ? <NextSelect /> : null}
    </div>
  );
}

export default function CallsPanel(props: NativePanelProps) {
	const [view, setView] = useState<"calls" | "routing" | "numbers" | "addresses" | "bundles">("calls");
  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
		<nav className="shrink-0 h-11 border-b border-border px-4 flex items-center gap-1" aria-label="Telephony views">
			<button
				type="button"
				onClick={() => setView("routing")}
				className={`h-8 px-3 rounded text-sm ${view === "routing" ? "bg-bg-muted font-medium" : "text-text-muted hover:bg-bg-muted/60"}`}
			>
				Routing
			</button>
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
			{view === "routing" ? <RoutingView {...props} /> : null}
        {view === "numbers" ? <NumbersView key={props.projectId} {...props} /> : null}
        {view === "addresses" ? <AddressesView {...props} /> : null}
        {view === "bundles" ? <BundlesView {...props} /> : null}
      </div>
    </div>
  );
}
