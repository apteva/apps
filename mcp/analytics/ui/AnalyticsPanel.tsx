// AnalyticsPanel — live overview of the generic event store.
//
// Layout:
//   ┌─ header: title · window (24h/7d/30d/All) · live · refresh ─────┐
//   │ filter bar: app · event type · props.key = value · clear       │
//   │ stat tiles: total events · topics · apps                       │
//   │ events/day  — inline SVG bar chart over /series                │
//   │ top values  — horizontal bars over /top?by=props.X             │
//   │ event feed  — recent rows over /events (real-time)             │
//   │ topics      — table over /summary.topics_list                  │
//   └──────────────────────────────────────────────────────────────┘
//
// Real-time: each analytics_track emits "event.recorded" on the app bus;
// this panel subscribes via the inlined useAppEvents helper and reloads
// (debounced) when events land — same pattern as the crm / calendar
// panels. Charts are hand-rolled inline SVG coloured via currentColor +
// a theme text token. Backend: GET /api/apps/analytics/{summary,series,
// top,events,dimensions}.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps are independently
// installable — cross-app imports would break a one-off install.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents so every
    // panel reuses one EventSource per (app, project).
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

const API = "/api/apps/analytics";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface TopicRow {
  app: string;
  topic: string;
  last_ts: number;
  count: number;
}

interface Summary {
  total: number;
  apps: number;
  topics: number;
  topics_list: TopicRow[];
}

interface SeriesPoint {
  day: string;
  count: number;
}

interface TopRow {
  value: string | null;
  count: number;
}

interface EventRow {
  id: number;
  ts: number;
  app: string;
  topic: string;
  source: string;
  user_id?: string;
  session_id?: string;
  props: Record<string, unknown>;
}

interface Dimensions {
  apps: string[];
  topics: string[];
}

const WINDOWS = [
  { key: "24h", label: "24h", ms: 24 * 3600e3 },
  { key: "7d", label: "7d", ms: 7 * 24 * 3600e3 },
  { key: "30d", label: "30d", ms: 30 * 24 * 3600e3 },
  { key: "all", label: "All", ms: 0 },
];

function fmt(n: number): string {
  return (n ?? 0).toLocaleString();
}

function relTime(ms: number): string {
  if (!ms) return "";
  const dt = Date.now() - ms;
  const s = Math.max(0, Math.floor(dt / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function propsPreview(props: Record<string, unknown>): string {
  if (!props || typeof props !== "object") return "";
  return Object.entries(props)
    .slice(0, 5)
    .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : String(v)}`)
    .join("  ");
}

function ChartIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 3 3 5-6" />
    </svg>
  );
}

function Dot() {
  return (
    <svg width="8" height="8" viewBox="0 0 8 8" className="text-success" aria-hidden="true">
      <circle cx="4" cy="4" r="4" fill="currentColor" />
    </svg>
  );
}

function Empty({ label }: { label: string }) {
  return <div className="py-8 text-center text-text-muted text-sm">{label}</div>;
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="border border-border rounded px-4 py-3 flex-1">
      <div
        className="text-text font-semibold tabular-nums"
        style={{ fontSize: "24px", lineHeight: 1.1 }}
      >
        {fmt(value)}
      </div>
      <div className="text-text-dim text-xs uppercase mt-1">{label}</div>
    </div>
  );
}

function BarSeries({ data }: { data: SeriesPoint[] }) {
  if (!data.length) return <Empty label="No events in this window." />;
  const W = 600;
  const H = 120;
  const max = Math.max(...data.map((d) => d.count), 1);
  const bw = W / data.length;
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      className="w-full text-accent"
      style={{ height: "120px" }}
      preserveAspectRatio="none"
    >
      {data.map((d, i) => {
        const h = Math.max(1, (d.count / max) * (H - 2));
        return (
          <rect
            key={d.day}
            x={i * bw + bw * 0.12}
            y={H - h}
            width={bw * 0.76}
            height={h}
            fill="currentColor"
          >
            <title>{`${d.day}: ${fmt(d.count)}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}

function TopBars({ rows }: { rows: TopRow[] }) {
  if (!rows.length) return <Empty label="No values for this key in this window." />;
  const max = Math.max(...rows.map((r) => r.count), 1);
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <div key={String(r.value)} className="flex items-center gap-2 text-xs">
          <div className="text-text-muted truncate" style={{ width: "140px" }} title={r.value ?? ""}>
            {r.value ?? "—"}
          </div>
          <div className="flex-1 bg-bg-input rounded overflow-hidden" style={{ height: "14px" }}>
            <div className="h-full bg-accent" style={{ width: `${(r.count / max) * 100}%` }} />
          </div>
          <div className="text-text-dim tabular-nums" style={{ width: "56px", textAlign: "right" }}>
            {fmt(r.count)}
          </div>
        </div>
      ))}
    </div>
  );
}

function EventFeed({ rows }: { rows: EventRow[] }) {
  if (!rows.length) return <Empty label="No events match these filters." />;
  return (
    <div style={{ maxHeight: "320px", overflow: "auto" }}>
      {rows.map((e) => (
        <div key={e.id} className="flex items-center gap-3 text-xs border-t border-border py-1.5">
          <span className="text-text-dim tabular-nums" style={{ width: "64px" }}>
            {relTime(e.ts)}
          </span>
          <span className="text-text-muted truncate" style={{ width: "84px" }}>
            {e.app}
          </span>
          <span className="text-text font-medium truncate" style={{ width: "120px" }}>
            {e.topic}
          </span>
          <span className="text-text-dim truncate flex-1" title={JSON.stringify(e.props)}>
            {propsPreview(e.props)}
          </span>
        </div>
      ))}
    </div>
  );
}

interface WriteKey {
  id: number;
  key: string;
  site: string;
  project_id: string;
  allowed_origins?: string[];
  created_at: number;
  revoked_at?: number;
  last_used_ts?: number;
  event_count: number;
}

function CopyButton({ text, label }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context) — no-op */
    }
  };
  return (
    <button
      onClick={copy}
      className={
        "px-2 py-1 text-xs rounded border whitespace-nowrap " +
        (done ? "border-success text-success" : "border-border text-text-muted hover:text-text")
      }
    >
      {done ? "Copied" : label || "Copy"}
    </button>
  );
}

// TrackingTab — mint/copy public write keys and the static-site snippet.
// All routes are authenticated (dashboard session cookie travels with
// credentials: same-origin). The snippet points at this server's origin.
function TrackingTab({ projectId }: { projectId: string }) {
  const [keys, setKeys] = useState<WriteKey[]>([]);
  const [site, setSite] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/keys?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (r.ok) setKeys((await r.json()).keys ?? []);
      else setErr(`keys ${r.status}`);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const create = async () => {
    const s = site.trim();
    if (!s) return;
    setBusy(true);
    setErr("");
    try {
      const r = await fetch(`${API}/keys`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ site: s, project_id: projectId }),
      });
      if (r.ok) {
        setSite("");
        load();
      } else {
        setErr("create: " + (await r.text()));
      }
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (id: number) => {
    if (!confirm("Revoke this key? Sites using it stop tracking immediately.")) return;
    try {
      await fetch(`${API}/keys/revoke`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id, project_id: projectId }),
      });
      load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const active = keys.filter((k) => !k.revoked_at);
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  const tagURL = `${origin}/api/apps/analytics/ui/tag.js`;
  const featured = active[0];
  const snippet = featured
    ? `<script async src="${tagURL}" data-key="${featured.key}"></script>`
    : "";

  return (
    <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
      {err && <div className="text-error text-xs">{err}</div>}

      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="text-text-dim text-xs uppercase">Install on your site</div>
        {featured ? (
          <>
            <div className="text-text-muted text-sm">
              Paste this once in your site's <code>&lt;head&gt;</code>. It tracks page views
              automatically; call <code>apa("event_name", {`{ ...props }`})</code> for custom events.
            </div>
            <div className="flex items-start gap-2">
              <pre
                className="flex-1 bg-bg-input border border-border rounded p-2 text-xs overflow-auto"
                style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}
              >
                {snippet}
              </pre>
              <CopyButton text={snippet} label="Copy snippet" />
            </div>
            <div className="flex items-center gap-2 text-xs">
              <span className="text-text-dim" style={{ width: "72px" }}>Write key</span>
              <code className="text-text truncate flex-1">{featured.key}</code>
              <CopyButton text={featured.key} label="Copy key" />
            </div>
            <div className="flex items-center gap-2 text-xs">
              <span className="text-text-dim" style={{ width: "72px" }}>Script URL</span>
              <code className="text-text-muted truncate flex-1">{tagURL}</code>
              <CopyButton text={tagURL} label="Copy URL" />
            </div>
          </>
        ) : (
          <Empty label="Create a write key below to get your tracking snippet." />
        )}
      </section>

      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="text-text-dim text-xs uppercase">Write keys</div>
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={site}
            onChange={(e) => setSite(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") create();
            }}
            placeholder="site name (e.g. marketing-site)"
            spellCheck={false}
            className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
          <button
            onClick={create}
            disabled={busy || !site.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            Create key
          </button>
        </div>
        {keys.length ? (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-dim text-xs uppercase">
                <th className="text-left font-normal py-1">Site</th>
                <th className="text-left font-normal py-1">Key</th>
                <th className="text-right font-normal py-1">Events</th>
                <th className="text-right font-normal py-1">Last used</th>
                <th className="py-1"></th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr
                  key={k.id}
                  className="border-t border-border"
                  style={{ opacity: k.revoked_at ? 0.5 : 1 }}
                >
                  <td className="py-1.5 text-text">{k.site}</td>
                  <td className="py-1.5">
                    <div className="flex items-center gap-1.5">
                      <code className="text-text-muted truncate" style={{ maxWidth: "190px" }}>
                        {k.key}
                      </code>
                      {!k.revoked_at && <CopyButton text={k.key} />}
                    </div>
                  </td>
                  <td className="py-1.5 text-right tabular-nums text-text">{fmt(k.event_count)}</td>
                  <td className="py-1.5 text-right text-text-dim">
                    {k.last_used_ts ? relTime(k.last_used_ts) : "—"}
                  </td>
                  <td className="py-1.5 text-right">
                    {k.revoked_at ? (
                      <span className="text-text-dim text-xs">revoked</span>
                    ) : (
                      <button
                        onClick={() => revoke(k.id)}
                        className="text-xs text-text-muted hover:text-error"
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <Empty label="No write keys yet." />
        )}
      </section>
    </div>
  );
}

// CaptureTab — toggle + tune the bus auto-capture subscriber. Records
// what every app already emits on the platform bus, no per-app dependency.
function CaptureTab({ projectId }: { projectId: string }) {
  const [enabled, setEnabled] = useState(false);
  const [mode, setMode] = useState("denylist");
  const [patterns, setPatterns] = useState("");
  const [sampleRate, setSampleRate] = useState(1);
  const [captured, setCaptured] = useState(0);
  const [status, setStatus] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/capture?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!r.ok) {
        setStatus(`load ${r.status}`);
        return;
      }
      const d = await r.json();
      setEnabled(!!d.enabled);
      setMode(d.mode || "denylist");
      setPatterns((d.topic_patterns ?? []).join("\n"));
      setSampleRate(typeof d.sample_rate === "number" ? d.sample_rate : 1);
      setCaptured(d.captured ?? 0);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setStatus("saving…");
    const topic_patterns = patterns
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      const r = await fetch(`${API}/capture`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled, mode, topic_patterns, sample_rate: sampleRate }),
      });
      setStatus(r.ok ? "saved" : "save: " + (await r.text()));
      if (r.ok) load();
    } catch (e) {
      setStatus((e as Error).message);
    }
  };

  return (
    <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="flex items-center gap-3">
          <div className="text-text-dim text-xs uppercase">Bus auto-capture</div>
          <button
            onClick={() => setEnabled((v) => !v)}
            className={
              "px-2 py-1 text-xs rounded border " +
              (enabled ? "border-success text-success" : "border-border text-text-muted hover:text-text")
            }
          >
            {enabled ? "Enabled" : "Disabled"}
          </button>
          <span className="ml-auto text-text-dim text-xs">{captured.toLocaleString()} captured</span>
        </div>
        <div className="text-text-muted text-sm">
          Records what other apps already emit on the platform bus (e.g.{" "}
          <code>campaign.sent</code>, <code>file.deleted</code>) — no per-app dependency. Off by
          default. Recorded events show up with <code>source=bus</code>.
        </div>

        <div className="flex items-center gap-2">
          <span className="text-text-dim text-xs" style={{ width: "72px" }}>Mode</span>
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
          >
            <option value="all">All topics</option>
            <option value="denylist">Everything except…</option>
            <option value="allowlist">Only…</option>
          </select>
        </div>

        {mode !== "all" && (
          <div className="flex flex-col gap-1">
            <span className="text-text-dim text-xs">
              Topic patterns ({mode === "denylist" ? "exclude" : "include"}) — one per line; exact
              (<code>file.deleted</code>), prefix (<code>campaign.*</code>), or <code>*</code>
            </span>
            <textarea
              value={patterns}
              onChange={(e) => setPatterns(e.target.value)}
              spellCheck={false}
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
              style={{ minHeight: "80px", fontFamily: "monospace" }}
            />
          </div>
        )}

        <div className="flex items-center gap-2">
          <span className="text-text-dim text-xs" style={{ width: "72px" }}>Sample</span>
          <input
            type="number"
            min={0}
            max={1}
            step={0.05}
            value={sampleRate}
            onChange={(e) => setSampleRate(Math.max(0, Math.min(1, parseFloat(e.target.value) || 0)))}
            className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
            style={{ width: "90px" }}
          />
          <span className="text-text-dim text-xs">fraction kept (1 = all)</span>
        </div>

        <div className="flex items-center gap-2 justify-end">
          <span className="text-text-dim text-xs mr-auto">{status}</span>
          <button
            onClick={save}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold"
          >
            Save
          </button>
        </div>
      </section>
    </div>
  );
}

export default function AnalyticsPanel({ projectId }: NativePanelProps) {
  const [view, setView] = useState<"overview" | "tracking" | "capture">("overview");
  const [windowKey, setWindowKey] = useState("7d");
  const [byKey, setByKey] = useState("props.platform");
  const [appF, setAppF] = useState("");
  const [topicF, setTopicF] = useState("");
  const [whereKey, setWhereKey] = useState("");
  const [whereVal, setWhereVal] = useState("");

  const [summary, setSummary] = useState<Summary | null>(null);
  const [series, setSeries] = useState<SeriesPoint[]>([]);
  const [top, setTop] = useState<TopRow[]>([]);
  const [events, setEvents] = useState<EventRow[]>([]);
  const [dims, setDims] = useState<Dimensions>({ apps: [], topics: [] });
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const [lastEventAt, setLastEventAt] = useState(0);

  const since = useMemo(() => {
    const w = WINDOWS.find((x) => x.key === windowKey);
    return w && w.ms ? Date.now() - w.ms : 0;
  }, [windowKey]);

  // Shared query string for every data endpoint: window + dimension
  // filters (app / event type / one props.X = value equality).
  const qs = useMemo(() => {
    const p = new URLSearchParams();
    if (projectId) p.set("project_id", projectId); // scope the panel to this project
    if (since) p.set("since", String(since));
    if (appF) p.set("app", appF);
    if (topicF) p.set("topic", topicF);
    if (whereKey.trim() && whereVal.trim()) {
      p.set("where", JSON.stringify({ [whereKey.trim()]: whereVal.trim() }));
    }
    return p.toString();
  }, [projectId, since, appF, topicF, whereKey, whereVal]);

  const withQs = useCallback(
    (base: string) => base + (qs ? (base.includes("?") ? "&" : "?") + qs : ""),
    [qs],
  );

  const loadMain = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const [sumR, serR, evR] = await Promise.all([
        fetch(withQs(`${API}/summary`), { credentials: "same-origin" }),
        fetch(withQs(`${API}/series`), { credentials: "same-origin" }),
        fetch(withQs(`${API}/feed?limit=50`), { credentials: "same-origin" }),
      ]);
      if (sumR.ok) {
        const d = await sumR.json();
        setSummary({
          total: d.total ?? 0,
          apps: d.apps ?? 0,
          topics: d.topics ?? 0,
          topics_list: Array.isArray(d.topics_list) ? d.topics_list : [],
        });
      } else {
        setErr(`summary ${sumR.status}`);
      }
      if (serR.ok) setSeries((await serR.json()).series ?? []);
      if (evR.ok) setEvents((await evR.json()).events ?? []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [withQs]);

  const loadTop = useCallback(async () => {
    try {
      const res = await fetch(withQs(`${API}/top?by=${encodeURIComponent(byKey)}`), {
        credentials: "same-origin",
      });
      if (res.ok) setTop((await res.json()).top ?? []);
    } catch {
      /* top is best-effort; loadMain surfaces errors */
    }
  }, [withQs, byKey]);

  // Dimension options for the filter dropdowns — scoped to this project.
  useEffect(() => {
    const p = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    fetch(`${API}/dimensions${p}`, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : { apps: [], topics: [] }))
      .then((d) => setDims({ apps: d.apps ?? [], topics: d.topics ?? [] }))
      .catch(() => {});
  }, [projectId]);

  useEffect(() => {
    loadMain();
  }, [loadMain]);

  useEffect(() => {
    const t = setTimeout(loadTop, 250);
    return () => clearTimeout(t);
  }, [loadTop]);

  // Live: debounce bus events so a burst of tracks = one reload.
  const reloadTimer = useRef<number | null>(null);
  const onBusEvent = useCallback(() => {
    setLastEventAt(Date.now());
    if (reloadTimer.current) window.clearTimeout(reloadTimer.current);
    reloadTimer.current = window.setTimeout(() => {
      loadMain();
      loadTop();
    }, 1200);
  }, [loadMain, loadTop]);
  useAppEvents("analytics", projectId, onBusEvent);

  const refresh = () => {
    loadMain();
    loadTop();
  };

  const hasFilters = !!(appF || topicF || (whereKey && whereVal));
  const clearFilters = () => {
    setAppF("");
    setTopicF("");
    setWhereKey("");
    setWhereVal("");
  };

  const selCls = "bg-bg-input border border-border rounded px-2 py-1 text-xs";

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="flex items-center gap-2 text-text font-medium">
          <span className="text-accent">
            <ChartIcon />
          </span>
          Analytics
        </div>
        <div className="flex items-center gap-1 ml-2">
          {(["overview", "tracking", "capture"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={
                "px-2 py-1 text-xs rounded border " +
                (view === v
                  ? "border-accent text-accent"
                  : "border-border text-text-muted hover:text-text")
              }
            >
              {v === "overview" ? "Overview" : v === "tracking" ? "Tracking" : "Capture"}
            </button>
          ))}
        </div>
        {view === "overview" && (
          <>
            <div className="flex items-center gap-1 ml-2">
              {WINDOWS.map((w) => (
                <button
                  key={w.key}
                  onClick={() => setWindowKey(w.key)}
                  className={
                    "px-2 py-1 text-xs rounded border " +
                    (windowKey === w.key
                      ? "border-accent text-accent"
                      : "border-border text-text-muted hover:text-text")
                  }
                >
                  {w.label}
                </button>
              ))}
            </div>
            <span className="flex items-center gap-1.5 text-text-dim text-xs ml-1" title="Live — updates as events are recorded">
              <Dot />
              {lastEventAt ? `live · ${relTime(lastEventAt)}` : "live"}
            </span>
            <button
              onClick={refresh}
              disabled={loading}
              className="ml-auto px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50"
            >
              Refresh
            </button>
            <span className="text-text-dim text-xs" style={{ minWidth: "48px" }}>
              {err ? <span className="text-error">{err}</span> : loading ? "loading…" : ""}
            </span>
          </>
        )}
      </header>

      {view === "tracking" ? (
        <TrackingTab projectId={projectId} />
      ) : view === "capture" ? (
        <CaptureTab projectId={projectId} />
      ) : (
      <>
      <div className="flex items-center gap-2 flex-wrap border-b border-border px-4 py-2">
        <select value={appF} onChange={(e) => setAppF(e.target.value)} className={selCls}>
          <option value="">All apps</option>
          {dims.apps.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </select>
        <select value={topicF} onChange={(e) => setTopicF(e.target.value)} className={selCls}>
          <option value="">All event types</option>
          {dims.topics.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <input
          type="text"
          value={whereKey}
          onChange={(e) => setWhereKey(e.target.value)}
          placeholder="props.country"
          spellCheck={false}
          className={selCls}
          style={{ width: "140px" }}
        />
        <span className="text-text-dim text-xs">=</span>
        <input
          type="text"
          value={whereVal}
          onChange={(e) => setWhereVal(e.target.value)}
          placeholder="value"
          spellCheck={false}
          className={selCls}
          style={{ width: "120px" }}
        />
        {hasFilters && (
          <button onClick={clearFilters} className="px-2 py-1 text-xs text-text-muted hover:text-text">
            Clear
          </button>
        )}
      </div>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        <div className="flex gap-3">
          <Stat label="Events" value={summary?.total ?? 0} />
          <Stat label="Topics" value={summary?.topics ?? 0} />
          <Stat label="Apps" value={summary?.apps ?? 0} />
        </div>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Events per day</div>
          <BarSeries data={series} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <div className="text-text-dim text-xs uppercase">Top values</div>
            <input
              type="text"
              value={byKey}
              onChange={(e) => setByKey(e.target.value)}
              placeholder="props.platform"
              spellCheck={false}
              className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-xs"
              style={{ width: "200px" }}
            />
          </div>
          <TopBars rows={top} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Event feed</div>
          <EventFeed rows={events} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Topics</div>
          {summary && summary.topics_list.length ? (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-text-dim text-xs uppercase">
                  <th className="text-left font-normal py-1">App</th>
                  <th className="text-left font-normal py-1">Topic</th>
                  <th className="text-right font-normal py-1">Count</th>
                  <th className="text-right font-normal py-1">Last seen</th>
                </tr>
              </thead>
              <tbody>
                {summary.topics_list.map((t) => (
                  <tr key={`${t.app}/${t.topic}`} className="border-t border-border">
                    <td className="py-1.5 text-text-muted">{t.app}</td>
                    <td className="py-1.5 text-text">{t.topic}</td>
                    <td className="py-1.5 text-right tabular-nums text-text">{fmt(t.count)}</td>
                    <td className="py-1.5 text-right text-text-dim">{relTime(t.last_ts)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Empty label="No events recorded yet. Apps write via the analytics_track tool." />
          )}
        </section>
      </div>
      </>
      )}
    </div>
  );
}
