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
  };
}

function statusClass(status: string): string {
  if (status === "in-progress" || status === "answered") return "bg-success/10 text-success border-success/30";
  if (status === "ringing" || status === "initiated") return "bg-info/10 text-info border-info/30";
  if (status === "failed" || status === "busy" || status === "no-answer") return "bg-error/10 text-error border-error/30";
  if (status === "canceled") return "bg-warn/10 text-warn border-warn/30";
  return "bg-bg-muted text-text-muted border-border";
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

export default function CallsPanel({ projectId }: NativePanelProps) {
  const [calls, setCalls] = useState<Call[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [ending, setEnding] = useState("");
  const [now, setNow] = useState(() => Date.now());

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

  useEffect(() => { loadCalls(); }, [loadCalls]);

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

  const activeCount = useMemo(
    () => calls.filter((call) => LIVE_STATUSES.has(call.status)).length,
    [calls],
  );

  const terminalCount = calls.length - activeCount;

  const hangup = async (call: Call) => {
    if (!call || !LIVE_STATUSES.has(call.status)) return;
    setEnding(call.id);
    try {
      const res = await fetch(`${API}/calls/${encodeURIComponent(call.id)}/hangup`, {
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
            <div className="min-w-[48rem]">
              <div className="grid grid-cols-[8rem_1fr_1fr_8rem_7rem_6rem] gap-3 px-4 py-2 border-b border-border text-[11px] uppercase tracking-normal text-text-dim">
                <div>Status</div>
                <div>To</div>
                <div>From</div>
                <div>Started</div>
                <div>Duration</div>
                <div>Voice</div>
              </div>
              {calls.map((call) => {
                const picked = selected?.id === call.id;
                return (
                  <button
                    key={call.id}
                    type="button"
                    onClick={() => setSelectedId(call.id)}
                    className={`w-full grid grid-cols-[8rem_1fr_1fr_8rem_7rem_6rem] gap-3 px-4 py-3 text-left border-b border-border/70 hover:bg-bg-muted/70 ${picked ? "bg-bg-muted" : ""}`}
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
