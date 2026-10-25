import { memo, useEffect, useMemo, useRef, useState } from "react";
import type { AudioDashboardHost } from "./audio-dashboard";
import {
  createCallInspector,
  type InspectorState,
} from "./call-diagnostics-controller";
import {
  diagnosticsModel,
  formatMetric,
  text,
  type DiagnosticMetric,
  type DiagnosticSeries,
} from "./call-diagnostics";

const button =
  "rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:bg-bg-subtle focus-visible:outline-accent disabled:opacity-50";
function Metrics({ items }: { items: DiagnosticMetric[] }) {
  return (
    <dl className="grid grid-cols-2 gap-2">
      {items.map((m) => (
        <div
          key={m.label}
          className="min-w-0 rounded-lg border border-border p-3"
        >
          <dt className="text-xs text-text-muted">{m.label}</dt>
          <dd className="mt-1 text-lg font-semibold tabular-nums text-text">
            {formatMetric(m.value, m.unit)}
          </dd>
          <dd className="mt-1 text-xs text-text-dim">{m.basis}</dd>
        </div>
      ))}
    </dl>
  );
}
function Chart({ series }: { series: DiagnosticSeries }) {
  const points = series.points,
    start = points[0]?.time ?? 0,
    end = points.at(-1)?.time ?? start;
  const max = Math.max(1, ...points.map((p) => p.value));
  const x = (t: number) => 34 + ((t - start) / Math.max(1, end - start)) * 310,
    y = (v: number) => 82 - (v / max) * 65;
  // Do not imply continuity across gaps in retained observations.
  const intervals = points
    .slice(1)
    .map((p, i) => p.time - points[i].time)
    .sort((a, b) => a - b);
  const gap = Math.min(
    30000,
    Math.max(5000, (intervals[Math.floor(intervals.length / 2)] || 5000) * 3),
  );
  return (
    <figure className="min-w-0 rounded-lg border border-border p-3">
      <figcaption className="text-xs font-medium text-text">
        {series.label}
      </figcaption>
      {points.length ? (
        <>
          <svg
            role="img"
            aria-label={`${series.label}: ${points.length} observations, latest ${formatMetric(points.at(-1)?.value, series.unit)}`}
            viewBox="0 0 356 110"
            className="mt-2 w-full text-accent"
          >
            <path
              d="M34 16V82H344"
              fill="none"
              stroke="currentColor"
              opacity=".15"
            />
            <text x="0" y="20" fill="currentColor" fontSize="9">
              {Math.round(max)}
            </text>
            <text x="15" y="85" fill="currentColor" fontSize="9">
              0
            </text>
            {points
              .slice(1)
              .map((p, i) =>
                p.time - points[i].time <= gap ? (
                  <line
                    key={p.time}
                    x1={x(points[i].time)}
                    y1={y(points[i].value)}
                    x2={x(p.time)}
                    y2={y(p.value)}
                    stroke="currentColor"
                    strokeWidth="1.8"
                  />
                ) : null,
              )}
            {points.map((p) => (
              <circle
                key={p.time}
                cx={x(p.time)}
                cy={y(p.value)}
                r="2"
                fill="currentColor"
              >
                <title>
                  {new Date(p.time).toLocaleTimeString()}:{" "}
                  {formatMetric(p.value, series.unit)}
                </title>
              </circle>
            ))}
            <text x="34" y="103" fill="currentColor" fontSize="9">
              {new Date(start).toLocaleTimeString()}
            </text>
            <text
              x="344"
              y="103"
              textAnchor="end"
              fill="currentColor"
              fontSize="9"
            >
              {new Date(end).toLocaleTimeString()}
            </text>
          </svg>
          <p className="text-xs text-text-dim">
            {series.unit} · {points.length} retained observations
          </p>
        </>
      ) : (
        <p className="py-8 text-xs text-text-muted">
          No retained samples for this measurement.
        </p>
      )}
    </figure>
  );
}
export default function CallDiagnostics({
  host,
  callId,
}: {
  host: AudioDashboardHost;
  callId: string;
}) {
  // Key the inner view by full authorization scope so evidence never flashes across selections.
  return (
    <Inspector
      key={`${host.appName}:${host.installId}:${host.projectId}:${callId}`}
      host={host}
      callId={callId}
    />
  );
}
const Inspector = memo(
  function Inspector({
    host,
    callId,
  }: {
    host: AudioDashboardHost;
    callId: string;
  }) {
    const [state, setState] = useState<InspectorState>({
      data: null,
      error: "",
      loading: true,
      receivedAt: 0,
    });
    const [tab, setTab] = useState("Overview"),
      [expanded, setExpanded] = useState(false),
      [rawOpen, setRawOpen] = useState(false),
      [carrierOpen, setCarrierOpen] = useState(false);
    const controller = useRef<ReturnType<typeof createCallInspector> | null>(
        null,
      ),
      dialog = useRef<HTMLDialogElement>(null),
      expandButton = useRef<HTMLButtonElement>(null),
      restoreFocus = useRef(false);
    useEffect(() => {
      const c = createCallInspector(host, callId, setState);
      controller.current = c;
      c.start();
      document.addEventListener("visibilitychange", c.visibility);
      return () => {
        document.removeEventListener("visibilitychange", c.visibility);
        c.dispose();
        controller.current = null;
      };
    }, [host.appName, host.installId, host.projectId, callId]);
    useEffect(() => {
      if (expanded && !dialog.current?.open) dialog.current?.showModal();
      else if (!expanded && restoreFocus.current) {
        expandButton.current?.focus();
        restoreFocus.current = false;
      }
    }, [expanded]);
    const m = useMemo(
      () => diagnosticsModel(state.data ?? {}, state.receivedAt),
      [state],
    );
    const raw = useMemo(
      () => (rawOpen && state.data ? JSON.stringify(state.data, null, 2) : ""),
      [rawOpen, state.data],
    );
    const tabs = [
      "Overview",
      "Browser",
      "SIP / carrier",
      ...(m.hasAI ? ["AI / Core"] : []),
      "Timeline",
    ];
    const download = () => {
      if (!state.data) return;
      const url = URL.createObjectURL(
        new Blob([JSON.stringify(state.data, null, 2)], {
          type: "application/json",
        }),
      );
      const a = document.createElement("a");
      a.href = url;
      a.download = `call-${callId.replace(/[^a-zA-Z0-9_-]/g, "_")}-diagnostics.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    };
    const content = (
      <>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <h3 className="text-sm font-semibold text-text">
              Call diagnostics
            </h3>
            <p className="mt-1 text-xs text-text-muted">
              {state.data
                ? `${m.active ? "Live call" : "Finished call"} · ${text(m.call.status)}`
                : "Loading call observations…"}
            </p>
            <p className="mt-1 break-all text-xs text-text-dim">
              Call {callId}
            </p>
          </div>
          <div className="flex gap-1">
            <button
              type="button"
              className={button}
              onClick={() => controller.current?.refresh()}
              disabled={state.loading}
            >
              Refresh
            </button>
            <button
              type="button"
              className={button}
              onClick={download}
              disabled={!state.data}
            >
              Export
            </button>
            {expanded ? (
              <button
                type="button"
                className={button}
                onClick={() => dialog.current?.close()}
              >
                Close
              </button>
            ) : (
              <button
                ref={expandButton}
                type="button"
                className={button}
                onClick={() => setExpanded(true)}
              >
                Expand
              </button>
            )}
          </div>
        </div>
        {state.error ? (
          <p
            role="alert"
            className="rounded border border-error/30 bg-error/10 p-3 text-xs text-error"
          >
            {state.error}
          </p>
        ) : null}
        {state.data ? (
          <>
            <div className="flex flex-wrap items-center gap-2 text-xs text-text-muted">
              <span
                className={`rounded-full border border-border px-2 py-1 ${m.state === "Telemetry stale" ? "text-warn" : ""}`}
              >
                {m.state}
              </span>
              <span>
                {m.observed
                  ? `Last evidence ${new Date(m.observed).toLocaleTimeString()}`
                  : "No audio observations received"}
              </span>
              {state.loading ? <span>Updating…</span> : null}
            </div>
            <div
              role="group"
              aria-label="Diagnostic sections"
              className="flex flex-wrap gap-1 border-b border-border pb-2"
            >
              {tabs.map((t) => (
                <button
                  type="button"
                  key={t}
                  aria-pressed={tab === t}
                  onClick={() => setTab(t)}
                  className={`rounded px-2.5 py-1.5 text-xs ${tab === t ? "bg-accent/10 text-accent font-medium" : "text-text-muted hover:bg-bg-subtle"}`}
                >
                  {t}
                </button>
              ))}
            </div>
            {tab === "Overview" ? (
              <>
                <div className="rounded-lg bg-bg-subtle/50 p-3 text-xs text-text-muted">
                  <p className="font-medium text-text">
                    {m.hasAI
                      ? "Caller ↔ carrier ↔ Telephony ↔ AI / Core"
                      : "Caller ↔ carrier ↔ Telephony ↔ browser"}
                  </p>
                  <p className="mt-1">
                    {[text(m.call.provider), m.sipCodec, m.browserPath]
                      .filter(Boolean)
                      .join(" · ") || "Transport details not reported yet."}
                  </p>
                </div>
                {m.stages.length ? (
                  <div className="grid grid-cols-2 gap-2">
                    {m.stages.map((s) => (
                      <div
                        key={s.label}
                        className="rounded border border-border p-2 text-xs"
                      >
                        <p className="text-text-muted">{s.label}</p>
                        <p
                          className={`mt-1 ${s.state.includes("degraded") || s.state.includes("error") ? "text-warn" : "text-text"}`}
                        >
                          {s.state}
                        </p>
                        <p className="mt-1 text-text-dim">
                          {s.detail
                            ? s.detail.replaceAll("_", " ") + " · "
                            : ""}
                          {s.observed
                            ? `Reported ${new Date(s.observed).toLocaleTimeString()}`
                            : "Observation time unavailable"}
                        </p>
                      </div>
                    ))}
                  </div>
                ) : null}
                <Metrics
                  items={[
                    ...(m.hasBrowser ? m.browserMetrics.slice(0, 2) : []),
                    ...(m.hasSIP ? [m.sipMetrics[5], m.sipMetrics[7]] : []),
                    ...(m.hasAI ? m.agentMetrics.slice(0, 2) : []),
                  ]}
                />
                {!m.hasBrowser && !m.hasSIP && !m.hasAI ? (
                  <p className="text-sm text-text-muted">
                    No telemetry is available for this call. This does not
                    establish healthy audio.
                  </p>
                ) : null}
                <div
                  className={`grid gap-3 ${expanded ? "md:grid-cols-2" : ""}`}
                >
                  {m.charts.map((s) => (
                    <Chart key={s.label} series={s} />
                  ))}
                </div>
                <p className="text-xs text-text-dim">
                  Observed transport timings are not end-to-end audible delay.
                  Discard and concealment counters may include silence. History
                  is bounded to retained samples.
                </p>
              </>
            ) : null}
            {tab === "Browser" ? (
              <>
                <p className="text-xs text-text-muted">
                  {m.hasBrowser
                    ? `${m.browserPath} · ${m.browserFreshness}`
                    : m.hasAI
                      ? "This AI call has no adviser browser leg. Browser metrics are not applicable unless a listener reports them."
                      : "No browser telemetry received. Missing measurements are shown as —."}
                </p>
                {m.hasBrowser ? <Metrics items={m.browserMetrics} /> : null}
              </>
            ) : null}
            {tab === "SIP / carrier" ? (
              <>
                <p className="text-xs text-text-muted">
                  {m.hasSIP
                    ? `${m.sipCodec || "SIP"} · ${m.sipFreshness}`
                    : "No SIP observations retained. WebSocket carrier calls do not have SIP packet metrics here."}
                </p>
                {m.hasSIP ? <Metrics items={m.sipMetrics} /> : null}
                {m.hasCarrier ? <Metrics items={m.carrierMetrics} /> : null}
                <details
                  open={carrierOpen}
                  onToggle={(e) => setCarrierOpen(e.currentTarget.open)}
                  className="text-xs text-text-muted"
                >
                  <summary className="cursor-pointer">
                    Carrier evidence and commands
                  </summary>
                  <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all">
                    {carrierOpen
                      ? JSON.stringify(
                          {
                            carrier: state.data.carrier,
                            commands: state.data.carrier_commands,
                            recovery: state.data.recovery_episodes,
                          },
                          null,
                          2,
                        )
                      : ""}
                  </pre>
                </details>
              </>
            ) : null}
            {tab === "AI / Core" ? (
              <>
                <p className="text-xs text-text-muted">
                  Telephony-observed reply timing includes turn detection, model
                  work and transport. Carrier writes confirm transmission, not
                  listening. {m.agentFreshness}.
                </p>
                <Metrics items={m.agentMetrics} />
              </>
            ) : null}
            {tab === "Timeline" ? (
              <>
                <p className="text-xs text-text-muted">
                  Latest 100 retained events. No event is not proof of
                  uninterrupted speech.
                </p>
                {m.events.length ? (
                  <ol className="space-y-2">
                    {m.events.map((e) => (
                      <li
                        key={e.id}
                        className="border-l-2 border-border pl-3 py-1"
                      >
                        <div className="flex flex-wrap gap-2 text-xs text-text-dim">
                          <time dateTime={new Date(e.time).toISOString()}>
                            {new Date(e.time).toLocaleTimeString()}
                          </time>
                          <span>{e.stage}</span>
                        </div>
                        <p className="mt-1 text-sm text-text">{e.label}</p>
                        {e.detail ? (
                          <p className="text-xs text-text-muted">{e.detail}</p>
                        ) : null}
                      </li>
                    ))}
                  </ol>
                ) : (
                  <p className="py-4 text-sm text-text-muted">
                    No retained events.
                  </p>
                )}
              </>
            ) : null}
            <details
              open={rawOpen}
              onToggle={(e) => setRawOpen(e.currentTarget.open)}
              className="border-t border-border pt-3 text-xs text-text-muted"
            >
              <summary className="cursor-pointer">
                Advanced · raw diagnostics
              </summary>
              <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-subtle p-3">
                {raw}
              </pre>
            </details>
          </>
        ) : !state.error ? (
          <div
            role="status"
            className="py-8 text-center text-sm text-text-muted"
          >
            Loading diagnostics…
          </div>
        ) : null}
      </>
    );
    return (
      <section
        aria-label="Call diagnostics"
        className="min-w-0 rounded-xl border border-border bg-bg-card p-4 space-y-4"
      >
        {expanded ? (
          <p className="text-xs text-text-muted">
            Diagnostics open in expanded view.
          </p>
        ) : (
          content
        )}
        {expanded ? (
          <dialog
            ref={dialog}
            aria-label={`Call diagnostics ${callId}`}
            onClose={() => {
              restoreFocus.current = true;
              setExpanded(false);
            }}
            style={{ maxHeight: "90vh", width: "min(960px,94vw)" }}
            className="m-auto overflow-auto rounded-xl border border-border bg-bg-card p-5 text-text backdrop:bg-black/50"
          >
            <div className="space-y-4">{content}</div>
          </dialog>
        ) : null}
      </section>
    );
  },
  (a, b) =>
    a.callId === b.callId &&
    a.host.appName === b.host.appName &&
    a.host.installId === b.host.installId &&
    a.host.projectId === b.host.projectId,
);
