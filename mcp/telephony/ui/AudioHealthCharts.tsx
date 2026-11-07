import type { AudioHealthBucket } from "./audio-dashboard";

export default function AudioHealthCharts({
  buckets,
}: {
  buckets?: AudioHealthBucket[];
}) {
  if (!buckets) return null;
  const maxCalls = Math.max(1, ...buckets.map((b) => b.calls));
  const maxDiscard = Math.max(
    1,
    ...buckets.flatMap((b) => [b.sip_discard_ms, b.browser_discard_ms]),
  );
  const label = (b: AudioHealthBucket) =>
    new Date(b.from).toLocaleString() +
    " – " +
    new Date(b.until).toLocaleString();
  const charts = [
    {
      title: "Call observations over time",
      legend:
        "Blue: observed without issues · amber: reported issues · grey: no telemetry",
      kind: "calls",
    },
    {
      title: "Calls with reported issues (%)",
      legend: "Affected ÷ observed calls; missing telemetry stays unknown.",
      kind: "health",
    },
    {
      title: "Reported discard duration",
      legend:
        "Blue: SIP receive · amber: browser playback. Separate counters; do not add.",
      kind: "discard",
    },
  ];
  return (
    <section aria-label="Audio health charts" className="space-y-3">
      <p className="text-xs text-text-muted">
        Charts follow the time picker and all call filters, across every
        matching call. Each call is grouped by its latest audio observation.
        Values are cumulative retained counters, not a timeline of individual
        audible cuts.
      </p>
      <div className="grid gap-3 xl:grid-cols-3">
        {charts.map((chart) => (
          <div
            key={chart.kind}
            className="min-w-0 rounded-xl border border-border bg-bg-card p-3"
          >
            <h2 className="text-sm font-semibold">{chart.title}</h2>
            <p className="mt-1 min-h-10 text-xs text-text-muted">
              {chart.legend}
            </p>
            <div
              role="group"
              aria-label={chart.title}
              className="mt-3 flex items-end gap-1 overflow-x-auto border-b border-border"
              style={{ height: 155 }}
            >
              {buckets.map((b) => (
                <div
                  key={b.from}
                  className="flex-1 flex flex-col justify-end min-w-2 h-full"
                  title={
                    label(b) +
                    `: ${b.calls} calls, ${b.affected}/${b.observed} with issues; SIP ${b.sip_discard_ms} ms, browser ${b.browser_discard_ms} ms`
                  }
                >
                  {chart.kind === "calls" ? (
                    <div
                      className="flex flex-col justify-end"
                      style={{
                        height: Math.max(1, (b.calls / maxCalls) * 140),
                      }}
                    >
                      <span
                        style={{
                          height: `${b.calls ? ((b.calls - b.observed) / b.calls) * 100 : 0}%`,
                          background: "var(--text-dim)",
                        }}
                      />
                      <span
                        style={{
                          height: `${b.calls ? (b.affected / b.calls) * 100 : 0}%`,
                          background: "var(--warn)",
                        }}
                      />
                      <span
                        style={{
                          height: `${b.calls ? ((b.observed - b.affected) / b.calls) * 100 : 0}%`,
                          background: "#3b82f6",
                        }}
                      />
                    </div>
                  ) : chart.kind === "health" ? (
                    <span
                      style={{
                        height:
                          b.problem_percent == null
                            ? 2
                            : Math.max(2, (b.problem_percent / 100) * 140),
                        background:
                          b.problem_percent == null
                            ? "var(--text-dim)"
                            : "var(--warn)",
                        opacity: b.problem_percent == null ? 0.4 : 1,
                      }}
                    />
                  ) : (
                    <div className="flex items-end gap-px h-full">
                      <span
                        className="flex-1"
                        style={{
                          height: b.sip_calls
                            ? Math.max(1, (b.sip_discard_ms / maxDiscard) * 140)
                            : 0,
                          background: "#3b82f6",
                        }}
                      />
                      <span
                        className="flex-1"
                        style={{
                          height: b.browser_calls
                            ? Math.max(
                                1,
                                (b.browser_discard_ms / maxDiscard) * 140,
                              )
                            : 0,
                          background: "var(--warn)",
                        }}
                      />
                    </div>
                  )}
                </div>
              ))}
            </div>
            <div className="mt-2 flex justify-between gap-2 text-xs text-text-dim">
              <span>
                {buckets[0] ? new Date(buckets[0].from).toLocaleString() : ""}
              </span>
              <span>
                {buckets.length ? new Date(buckets[buckets.length - 1].until).toLocaleString() : ""}
              </span>
            </div>
            <p className="mt-1 text-right text-xs text-text-dim">
                {chart.kind === "calls"
                  ? `Max ${maxCalls} calls`
                  : chart.kind === "health"
                    ? "0–100%"
                    : `Max ${(maxDiscard / 1000).toFixed(2)}s`}
            </p>
          </div>
        ))}
      </div>
      <details className="rounded border border-border p-3 text-xs">
        <summary className="cursor-pointer text-text-muted">
          Chart data and intervals
        </summary>
        <div className="overflow-auto mt-3">
          <table className="w-full text-left">
            <thead>
              <tr>
                {[
                  "Interval",
                  "Calls",
                  "Observed",
                  "With issues",
                  "Issue rate",
                  "SIP discard",
                  "Browser discard",
                ].map((v) => (
                  <th key={v} className="p-2">
                    {v}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {buckets.map((b) => (
                <tr key={b.from} className="border-t border-border">
                  <td className="p-2 whitespace-nowrap">{label(b)}</td>
                  <td>{b.calls}</td>
                  <td>{b.observed}</td>
                  <td>{b.affected}</td>
                  <td>
                    {b.problem_percent == null
                      ? "Unknown"
                      : b.problem_percent.toFixed(1) + "%"}
                  </td>
                  <td>
                    {b.sip_calls
                      ? b.sip_discard_ms + " ms · " + b.sip_calls + " calls"
                      : "—"}
                  </td>
                  <td>
                    {b.browser_calls
                      ? b.browser_discard_ms +
                        " ms · " +
                        b.browser_calls +
                        " calls"
                      : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </section>
  );
}
