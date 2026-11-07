import { useRef, useState } from "react";
import {
  audioFirstFailedLabels,
  audioIncidentURL,
  type AudioDashboardHost,
  type AudioIncidentCall,
  type AudioIncidentReport,
} from "./audio-dashboard";

const seconds = (ms: number | undefined) =>
  ms === undefined ? "—" : `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)} s`;
const clock = (value: string | undefined) => {
  if (!value) return "—";
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? "—" : at.toLocaleTimeString();
};

function recoveryDetail(call: AudioIncidentCall): string {
  const r = call.recovery;
  if (!r) return "";
  const parts: string[] = [];
  if (r.outcome === "handed_over") parts.push("audio kept, connection replaced");
  if (r.outcome === "handover_failed") parts.push("replacement failed");
  if (r.attempts_total) parts.push(`${r.attempts_total} attempt${r.attempts_total === 1 ? "" : "s"}`);
  if (r.online === false) parts.push("browser offline");
  if (r.checks?.telephony) parts.push(`Telephony ${r.checks.telephony.ok ? "reachable" : "unreachable"}`);
  if (r.checks?.page) parts.push(`page host ${r.checks.page.ok ? "reachable" : "unreachable"}`);
  return parts.join(", ");
}

/** Loads only when asked, so an open dashboard adds no load during normal use. */
export default function AudioIncidentSection({ host }: { host: AudioDashboardHost }) {
  const [open, setOpen] = useState(false),
    [from, setFrom] = useState(""),
    [to, setTo] = useState(""),
    [report, setReport] = useState<AudioIncidentReport | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false);
  const request = useRef<AbortController | null>(null);
  const load = async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError("");
    const timeout = window.setTimeout(() => controller.abort(), 12000);
    try {
      const response = await fetch(audioIncidentURL(host, from, to), { credentials: "same-origin", signal: controller.signal });
      if (!response.ok) throw new Error((await response.text()) || `Incident request failed (HTTP ${response.status}).`);
      const value = (await response.json()) as AudioIncidentReport;
      if (request.current === controller) setReport(value);
    } catch (e) {
      if (request.current === controller) {
        setReport(null);
        setError(controller.signal.aborted ? "The incident report timed out. Try a shorter window." : (e as Error).message);
      }
    } finally {
      window.clearTimeout(timeout);
      if (request.current === controller) setLoading(false);
    }
  };
  return (
    <section className="rounded border border-border" aria-label="Audio incident">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-3 p-3 text-left"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span>
          <span className="block text-sm font-medium">Incident view</span>
          <span className="block text-xs text-text-muted">What stopped first on each call in a time window, and when it came back.</span>
        </span>
        <span className="text-xs text-text-muted">{open ? "Hide" : "Show"}</span>
      </button>
      {open && (
        <div className="space-y-3 border-t border-border p-3">
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs text-text-muted">
              From
              <input aria-label="Incident from" type="datetime-local" step="1" value={from} onChange={(e) => setFrom(e.target.value)} className="mt-1 block h-9 rounded border border-border bg-bg px-2 text-text" />
            </label>
            <label className="text-xs text-text-muted">
              To
              <input aria-label="Incident to" type="datetime-local" step="1" value={to} onChange={(e) => setTo(e.target.value)} className="mt-1 block h-9 rounded border border-border bg-bg px-2 text-text" />
            </label>
            <button type="button" onClick={() => void load()} disabled={loading} className="h-9 rounded border border-border px-3 text-xs">
              {loading ? "Loading…" : "Load report"}
            </button>
            <span className="text-xs text-text-muted">Blank means the last 15 minutes. At most 6 hours.</span>
          </div>
          {error && <p role="alert" className="text-sm text-error">{error}</p>}
          {report && (
            <>
              <p className="text-sm">{report.reading}</p>
              {report.truncated && <p className="text-xs text-text-muted">More events than one report can read. Try a shorter window.</p>}
              {report.calls.length > 0 && (
                <div className="overflow-x-auto">
                  <table className="w-full text-left text-xs">
                    <thead className="text-text-muted">
                      <tr>
                        <th className="p-2 font-medium">Adviser</th>
                        <th className="p-2 font-medium">Dropped at</th>
                        <th className="p-2 font-medium">Stopped first</th>
                        <th className="p-2 font-medium">Last microphone audio</th>
                        <th className="p-2 font-medium">Last signaling</th>
                        <th className="p-2 font-medium">Back after</th>
                        <th className="p-2 font-medium">Browser report</th>
                      </tr>
                    </thead>
                    <tbody>
                      {report.calls.map((call) => (
                        <tr key={call.call_id} className="border-t border-border">
                          <td className="p-2">
                            <span className="block">{call.adviser || "Unattributed"}</span>
                            <span className="block font-mono text-text-muted">{call.call_id}</span>
                          </td>
                          <td className="p-2 tabular-nums">{clock(call.disconnected_at)}</td>
                          <td className="p-2">{audioFirstFailedLabels[call.first_failed] ?? call.first_failed}</td>
                          <td className="p-2 tabular-nums">{call.audio_age_ms === undefined ? "—" : `${seconds(call.audio_age_ms)} before`}</td>
                          <td className="p-2 tabular-nums">{call.signaling_age_ms === undefined ? "—" : `${seconds(call.signaling_age_ms)} before`}</td>
                          <td className={`p-2 tabular-nums ${call.reconnect_ms === undefined ? "text-error" : ""}`}>{call.reconnect_ms === undefined ? "Not reconnected" : seconds(call.reconnect_ms)}</td>
                          <td className="p-2 text-text-muted">{recoveryDetail(call) || "—"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {report.healthy_calls.length > 0 && (
                <p className="text-xs text-text-muted">
                  Stayed connected: {report.healthy_calls.map((call) => call.adviser || call.call_id).join(", ")}
                  {report.summary.healthy_calls > report.healthy_calls.length ? ` and ${report.summary.healthy_calls - report.healthy_calls.length} more` : ""}
                </p>
              )}
              {report.network_checks.map((round) => (
                <div key={round.occurred_at} className="text-xs">
                  <span className="text-text-muted">Network checks from this server at {clock(round.occurred_at)}: </span>
                  {round.results.map((result) => `${result.target} ${result.ok ? `reachable in ${result.ms} ms` : `failed (${result.error || "error"})`}`).join("; ")}
                </div>
              ))}
              {report.alerts.length > 0 && (
                <p className="text-xs text-text-muted">
                  Audio alerts in this window: {report.alerts.map((alert) => `${alert.stage} at ${clock(alert.occurred_at)} (${alert.call_count} calls)`).join("; ")}
                </p>
              )}
            </>
          )}
        </div>
      )}
    </section>
  );
}
