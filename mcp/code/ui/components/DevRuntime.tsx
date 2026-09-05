import {useState,useRef,useEffect,useCallback} from "react";
import {RequestGate,boundedLog} from "./editorState";
import {DeviceFrame} from "./DeviceFrame";
// ─── DevBar / DevLogsView ─────────────────────────────────────────
//
// Dev runtime UI for the right pane. DevBar is the thin status strip
// above the file editor; it polls /api/repos/<slug>/dev/status every
// 2s and offers Run / Stop. DevLogsView replaces the file editor when
// the user toggles "Logs", streaming the dev process's stdout/stderr
// via SSE on /api/repos/<slug>/dev/log?follow=1.

export interface DevRunWire {
  preview_url?: string;
  id: number;
  status: "starting" | "live" | "stopped" | "crashed" | "orphaned";
  port: number;
  pid: number;
  framework: string;
  run_cmd?: string;
  started_at?: string;
  stopped_at?: string;
  error?: string;
  // Remote-runner fields — populated for mobile repos delegated to the
  // Simulator app. runner="" for local web dev runs.
  runner?: string;
  sim_id?: string;
  stream_url?: string;
}

function devStatusColor(s?: string): string {
  if (s === "live") return "text-green";
  if (s === "starting") return "text-blue";
  if (s === "crashed") return "text-red";
  return "text-text-dim";
}

function uptimeStr(startedAt?: string): string {
  if (!startedAt) return "";
  const ms = Date.now() - new Date(startedAt).getTime();
  if (ms < 0) return "";
  if (ms < 60_000) return `${Math.floor(ms / 1000)}s`;
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`;
  return `${Math.floor(ms / 3_600_000)}h${Math.floor((ms % 3_600_000) / 60_000)}m`;
}

export function DevBar({
  slug,
  api,
  withParams,
  showLogs,
  onToggleLogs,
  onError,
  onRunChange,
}: {
  slug: string;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  withParams: (extra?: Record<string, string>) => string;
  showLogs: boolean;
  onToggleLogs: () => void;
  onError: (msg: string) => void;
  onRunChange?: (run: DevRunWire | null) => void;
}) {
  const [run, setRun] = useState<DevRunWire | null>(null);
  const [busy, setBusy] = useState(false);
 const alive=useRef(true);const gate=useRef(new RequestGate());
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;gate.current.invalidate();};},[]);

  const refresh = useCallback(async () => {
    const token=gate.current.next();
    try {
      const r = await api<{ dev_run: DevRunWire | null }>("GET", `/repos/${slug}/dev/status`);
      if(!alive.current||!gate.current.current(token))return;
      setRun(r.dev_run);
      onRunChange?.(r.dev_run);
    } catch (e) {
      // Swallow — this polls in the background; the panel-wide error
      // banner is for explicit user actions.
    }
  }, [api, slug, onRunChange]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 2000);
    return () => clearInterval(t);
  }, [refresh]);

  const start = async () => {
    setBusy(true);
    try {
      await api("POST", `/repos/${slug}/dev/start`, {});
      await refresh();
    } catch (e) {
      if (alive.current) onError("Run failed: " + (e as Error).message);
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const stop = async () => {
    setBusy(true);
    try {
      await api("POST", `/repos/${slug}/dev/stop`, {});
      await refresh();
    } catch (e) {
      if (alive.current) onError("Stop failed: " + (e as Error).message);
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const status = run?.status ?? "stopped";
  const isLive = status === "live";
  const isBusy = status === "starting" || busy;
  const isRemote = run?.runner === "simulator";

  return (
    <div className="px-3 py-2 border-b border-border flex items-center gap-2 bg-bg-input/40">
      <span className={`text-xs ${devStatusColor(status)}`}>●</span>
      <span className="text-xs text-text-muted">
        {status === "live" && isRemote ? (
          <>
            Running on {run?.framework === "ios" ? "iOS Simulator" : "Android emulator"}
            {" "}· {uptimeStr(run?.started_at)}
          </>
        ) : status === "live" ? (
          <>
            Running on{" "}
            <a
              href={run?.preview_url || (typeof window!=="undefined" && ["localhost","127.0.0.1","[::1]"].includes(window.location.hostname) ? `http://127.0.0.1:${run?.port}/` : undefined)}
              target="_blank"
              rel="noreferrer"
              className="text-accent hover:underline font-mono"
            >{run?.preview_url ? "Preview" : `127.0.0.1:${run?.port}`}</a>
            {" "}· {uptimeStr(run?.started_at)} · {run?.framework}
          </>
        ) : status === "starting" ? (
          <>Starting {run?.framework}…</>
        ) : status === "crashed" ? (
          <span className="text-red">
            Crashed: <span className="font-mono">{(run?.error || "").split("\n")[0].slice(0, 80)}</span>
          </span>
        ) : (
          <>Dev preview stopped · deploy production from the Deploy app</>
        )}
      </span>
      <span className="flex-1" />
      <button
        type="button"
        onClick={onToggleLogs}
        className={`px-2 py-0.5 text-xs border rounded ${showLogs ? "border-accent text-accent" : "border-border text-text-muted hover:text-text"}`}
      >Logs</button>
      {isLive || status === "starting" ? (
        <button
          type="button"
          onClick={stop}
          disabled={busy && status !== "starting"}
          className="px-2 py-0.5 text-xs border border-red text-red rounded hover:bg-red hover:text-white disabled:opacity-50"
        >Stop</button>
      ) : (
        <button
          type="button"
          onClick={start}
          disabled={isBusy}
          className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >{isBusy ? "Starting…" : "Run"}</button>
      )}
    </div>
  );
}

// RemoteDeviceView renders the live mobile device for a Simulator-app
// dev run. Mounted by the panel body below the DevBar when the active
// dev run has runner=simulator and a stream URL.
export function RemoteDeviceView({ run }: { run: DevRunWire }) {
  if (!run.stream_url) {
    return (
      <div className="p-4 text-xs text-text-muted">
        Device starting — waiting for the stream URL…
      </div>
    );
  }
  return (
    <div className="p-4 flex justify-center">
      <DeviceFrame streamUrl={run.stream_url} platform={run.framework} />
    </div>
  );
}

// DevLogsView streams the dev run log via SSE. Auto-scrolls to bottom
// unless the user scrolls up; resumes auto-scroll when they scroll
// back to the bottom. Each run has a distinct stream identity and a bounded
// browser tail; EventSource sends Last-Event-ID when reconnecting.
export function DevLogsView({
  slug,
  withParams,
}: {
  slug: string;
  withParams: (extra?: Record<string, string>) => string;
}) {
  const [lines, setLines] = useState<string>("");
  const containerRef = useRef<HTMLPreElement | null>(null);
  const stickToBottom = useRef(true);

  useEffect(() => {
    setLines("");
    const url = `/api/apps/code/api/repos/${slug}/dev/log?${withParams({ follow: "1" })}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      // SSE delivers each `data:` line as a separate message; the
      // server emits them line-by-line so we just append.
      setLines((prev) => boundedLog(prev,e.data));
    };
    es.addEventListener("reset",()=>setLines(""));
    es.onerror = () => {
      // EventSource auto-reconnects; nothing to do.
    };
    return () => es.close();
  }, [slug, withParams]);

  useEffect(() => {
    if (stickToBottom.current && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [lines]);

  const onScroll = (e: React.UIEvent<HTMLPreElement>) => {
    const el = e.currentTarget;
    const distFromBottom = el.scrollHeight - el.clientHeight - el.scrollTop;
    stickToBottom.current = distFromBottom < 24;
  };

  return (
    <pre
      ref={containerRef}
      onScroll={onScroll}
      className="flex-1 overflow-auto bg-bg text-text font-mono text-[11px] p-3 whitespace-pre"
    >
      {lines.length === 0 ? (
        <span className="text-text-dim">Waiting for output…</span>
      ) : (
        lines
      )}
    </pre>
  );
}

