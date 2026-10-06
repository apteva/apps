import { useMemo } from "react";
import {
  audioWidgetPreferences,
  defaultAudioFilters,
  audioDashboardLink,
  audioStateLabel,
  type AudioDashboardHost,
} from "./audio-dashboard";
import { useAudioDashboard } from "./AudioHealthView";
export default function AudioHealthWidget(props: AudioDashboardHost) {
  const preferences = useMemo(
    () => audioWidgetPreferences(props.widgetSettings),
    [props.widgetSettings],
  );
  const filters = useMemo(
    () => ({
      ...defaultAudioFilters,
      range: preferences.range,
      provider: preferences.provider,
      state: "issues",
    }),
    [preferences],
  );
  const { data, error, loading, reload } = useAudioDashboard(
    props,
    filters,
    "",
    preferences.maxCalls,
    30000,
  );
  if (!props.projectId)
    return (
      <section className="rounded border border-border bg-bg-card p-4">
        <h2 className="text-sm font-semibold">Telephony audio health</h2>
        <p className="text-xs text-text-muted">
          Select a project to see audio observations.
        </p>
      </section>
    );
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex items-start justify-between gap-2 border-b border-border p-4">
        <div>
          <h2 className="text-sm font-semibold">Telephony audio health</h2>
          <p className="text-xs text-text-muted">
            {preferences.range === "1h"
              ? "Last hour"
              : preferences.range === "7d"
                ? "Last 7 days"
                : "Last 24 hours"}{" "}
            · refreshes every 30s
          </p>
        </div>
        <a
          href={audioDashboardLink(props, filters)}
          className="text-xs text-text-muted underline"
        >
          View audio health →
        </a>
      </header>
      {error ? (
        <div role="alert" className="p-3 text-xs text-error">
          {error}
          <button
            type="button"
            onClick={() => void reload()}
            className="ml-2 underline"
          >
            Retry
          </button>
          {data ? (
            <p>
              Last successful update:{" "}
              {new Date(data.generated_at).toLocaleTimeString()}
            </p>
          ) : null}
        </div>
      ) : null}
      {data ? (
        <>
          <div className="grid grid-cols-2 gap-2 border-b border-border p-3">
            <div>
              <p className="text-xs text-text-muted">Degraded now</p>
              <strong
                className={
                  data.totals.degraded > 0 ? "text-error" : "text-text"
                }
              >
                {data.totals.degraded}
              </strong>
            </div>
            <div>
              <p className="text-xs text-text-muted">Calls with observations</p>
              <strong>{data.totals.affected}</strong>
            </div>
          </div>
          {data.pending_reports > 0 ? (
            <p className="px-3 pt-2 text-xs text-warn">
              {data.pending_reports} reports awaiting indexing.
            </p>
          ) : null}
          <div className="min-h-0 flex-1 overflow-auto divide-y divide-border">
            {data.calls.length === 0 ? (
              <p className="p-4 text-xs text-text-muted">
                No matching observations. This does not confirm audio quality
                for unreported calls.
              </p>
            ) : (
              data.calls.map((r) => (
                <a
                  key={r.call_id}
                  href={audioDashboardLink(
                    props,
                    { ...filters, search: r.call_id },
                    r.call_id,
                  )}
                  className="block p-3 hover:bg-bg-muted"
                >
                  <div className="flex justify-between gap-2 text-xs">
                    <strong className="truncate">{r.adviser_label}</strong>
                    <span
                      className={
                        r.state === "audio_degraded"
                          ? "text-error"
                          : "text-text-muted"
                      }
                    >
                      {audioStateLabel(r.state)}
                    </span>
                  </div>
                  <p className="mt-1 text-xs text-text-muted">
                    {r.provider} · dropped{" "}
                    {Math.round(r.metrics.playback_dropped_ms || 0)} ms ·
                    reconnects {r.metrics.reconnects || 0}
                  </p>
                </a>
              ))
            )}
          </div>
          <footer className="border-t border-border px-3 py-2 text-xs text-text-dim">
            Updated {new Date(data.generated_at).toLocaleTimeString()}
            {loading ? " · refreshing" : ""}
          </footer>
        </>
      ) : !error ? (
        <p className="p-4 text-xs text-text-muted">Loading audio health…</p>
      ) : null}
    </section>
  );
}
