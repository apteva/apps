import { useCallback, useEffect, useState } from "react";
import { useAppEvents } from "./use-app-events";
import { analyticsRequest } from "./Management";
import { useLiveRefresh } from "./use-live-refresh";

const input = "border border-border bg-bg-input rounded px-2 py-1 text-sm";
const button =
  "border border-border rounded px-3 py-1 text-sm hover:text-accent disabled:opacity-50";
const date = (v: number) =>
  v > 0 && v < 8e15 ? new Date(v).toLocaleString() : "—";
export const financialStateLabel = (state?: string, pending?: boolean) =>
  pending
    ? "Refresh pending"
    : {
        current: "Current",
        pending: "Refresh pending",
        stale: "Stale",
        missing_fx: "Missing FX",
        source_unavailable: "Source unavailable",
        confirmed_zero: "Confirmed zero",
        unverified: "Source completeness unverified",
      }[state || ""] || "Not measured";

export function FinancialRefresh({ project }: { project: string }) {
  const [data, setData] = useState<any>(null),
    [shares, setShares] = useState<any[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const [economicKey, setEconomicKey] = useState("");
  const [target, setTarget] = useState(""),
    [destination, setDestination] = useState(""),
    [meaning, setMeaning] = useState("revenue");
  const [share, setShare] = useState(""),
    [destinationTarget, setDestinationTarget] = useState(""),
    [component, setComponent] = useState("");
  const [verifyTarget, setVerifyTarget] = useState(""),
    [verifiedThrough, setVerifiedThrough] = useState("");
  const load = useCallback(async () => {
    try {
      const [status, grants] = await Promise.all([
        analyticsRequest(project, "/financial-refresh"),
        analyticsRequest(project, "/financial-shares"),
      ]);
      setData(status);
      setShares(grants.shares || []);
    } catch (e) {
      setError(String(e));
    }
  }, [project]);
  const refresh = useLiveRefresh(load);
  useAppEvents("analytics", project, refresh);
  useEffect(() => {
    setData(null);
    setShares([]);
    setError("");
    setTarget("");
    setDestination("");
    setShare("");
    setDestinationTarget("");
    setComponent("");
    void load();
  }, [load]);
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const selectTarget = (
    label: string,
    value: string,
    set: (v: string) => void,
  ) => (
    <label className="flex flex-col gap-1 text-xs">
      {label}
      <select
        className={input}
        aria-label={label}
        value={value}
        onChange={(e) => set(e.target.value)}
      >
        <option value="">Select target</option>
        {data?.targets?.map((t: any) => (
          <option key={t.target_id} value={t.target_id}>
            {t.objective_name} · {t.name} (#{t.target_id})
          </option>
        ))}
      </select>
    </label>
  );
  return (
    <section className="space-y-4" aria-label="Financial refresh">
      <h3 className="font-medium">Automatic financial reporting</h3>
      <p className="text-sm text-text-dim">
        Processes queued changes every minute and reconciles saved periods every
        five minutes. Currencies is optional; ECB imports run only when needed.
        Existing amounts and timestamps are retained on failure.
      </p>
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
      {data && (
        <>
          <div className="flex flex-wrap gap-4 items-center">
            <label>
              <input
                type="checkbox"
                checked={data.enabled}
                disabled={busy}
                onChange={(e) =>
                  void run(() =>
                    analyticsRequest(project, "/financial-refresh", "PUT", {
                      enabled: e.target.checked,
                      fx_enabled: data.fx_enabled,
                      provider: "ecb",
                    }),
                  )
                }
              />{" "}
              Automatic refresh
            </label>
            <label>
              <input
                type="checkbox"
                checked={data.fx_enabled}
                disabled={busy}
                onChange={(e) =>
                  void run(() =>
                    analyticsRequest(project, "/financial-refresh", "PUT", {
                      enabled: data.enabled,
                      fx_enabled: e.target.checked,
                      provider: "ecb",
                    }),
                  )
                }
              />{" "}
              Import ECB rates through Currencies
            </label>
            <button
              className={button}
              disabled={busy}
              onClick={() =>
                void run(() =>
                  analyticsRequest(project, "/financial-refresh", "POST", {}),
                )
              }
            >
              Refresh now
            </button>
          </div>
          {!data.enabled && (
            <p className="text-sm text-text-dim">
              Automatic processing is paused. Queued changes will run when
              enabled.
            </p>
          )}
          <p className="text-xs text-text-dim">
            Last attempt: {date(data.last_attempt)} · Last successful pass:{" "}
            {date(data.last_success)}
          </p>
          {data.last_error && (
            <p role="status" className="text-error text-sm">
              {data.last_error}
            </p>
          )}
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr>
                  <th className="text-left">Target</th>
                  <th>Status</th>
                  <th>Last success</th>
                  <th>Next retry</th>
                </tr>
              </thead>
              <tbody>
                {data.targets.map((t: any) => (
                  <tr key={t.target_id}>
                    <td>
                      {t.objective_name} · {t.name}
                      {t.last_error && (
                        <p className="text-error text-xs">{t.last_error}</p>
                      )}
                    </td>
                    <td>{financialStateLabel(t.state, !!t.pending)}</td>
                    <td>{date(t.last_success)}</td>
                    <td>{date(t.next_retry)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <details>
            <summary>
              Required exchange rates ({data.fx_requests.length})
            </summary>
            <p className="text-xs text-text-dim">
              Rates retain their publication time. Weekends and ECB closing days
              use the latest applicable published observation.
            </p>
            <div className="max-h-64 overflow-auto">
              {data.fx_requests.map((f: any) => (
                <p
                  key={`${f.base}/${f.quote}/${f.day}`}
                  className="text-xs py-1"
                >
                  {f.base}/{f.quote} · {f.day} ·{" "}
                  {f.last_error || (f.last_success ? "Imported" : "Pending")} ·
                  Retry: {date(f.next_retry)}
                </p>
              ))}
            </div>
          </details>
          <details>
            <summary>Record a completed source reconciliation</summary>
            <p className="text-xs text-text-dim">
              Only confirm after checking the underlying source and resolving
              capture gaps. This records your attestation; Analytics cannot
              infer completeness from an empty event store. It expires on input
              changes or after five minutes for an open period.
            </p>
            <div className="flex flex-wrap gap-2 items-end">
              {selectTarget(
                "Reconciled source target",
                verifyTarget,
                setVerifyTarget,
              )}
              <label className="flex flex-col text-xs">
                Reconciled through
                <input
                  className={input}
                  type="datetime-local"
                  aria-label="Reconciled through"
                  value={verifiedThrough}
                  onChange={(e) => setVerifiedThrough(e.target.value)}
                />
              </label>
              <button
                className={button}
                disabled={busy || !verifyTarget || !verifiedThrough}
                onClick={() =>
                  void run(() =>
                    analyticsRequest(project, "/financial-refresh", "POST", {
                      verify_target: Number(verifyTarget),
                      verified_through: new Date(verifiedThrough).getTime(),
                    }),
                  )
                }
              >
                Confirm completed reconciliation
              </button>
            </div>
          </details>
          <h4 className="font-medium">Share a source target</h4>
          <p className="text-xs text-text-dim">
            Approve one target for one destination project. The destination must
            separately adopt a component. Sharing ends when revoked or when the
            source definition changes. Revenue and its settlement payout must
            not both be included.
          </p>
          <div className="flex flex-wrap gap-2 items-end">
            {selectTarget("Source target", target, setTarget)}
            <label className="flex flex-col text-xs">
              Destination project ID
              <input
                aria-label="Destination project ID"
                className={input}
                value={destination}
                onChange={(e) => setDestination(e.target.value)}
              />
            </label>
            <label className="flex flex-col text-xs">
              Metric meaning
              <select
                aria-label="Metric meaning"
                className={input}
                value={meaning}
                onChange={(e) => setMeaning(e.target.value)}
              >
                <option value="revenue">Revenue</option>
                <option value="realized_profit">Realized net profit</option>
                <option value="other">Other financial measure</option>
              </select>
            </label>
            <label className="flex flex-col text-xs">
              Income stream key
              <input
                aria-label="Income stream key"
                className={input}
                placeholder="Use the same key for revenue and its payout"
                value={economicKey}
                onChange={(e) => setEconomicKey(e.target.value)}
              />
            </label>
            <button
              className={button}
              disabled={busy || !target || !destination || !economicKey}
              onClick={() =>
                void run(() =>
                  analyticsRequest(project, "/financial-shares", "POST", {
                    target_id: Number(target),
                    destination_project: destination,
                    metric_meaning: meaning,
                    economic_key: economicKey,
                  }),
                )
              }
            >
              Approve source sharing
            </button>
          </div>
          {shares.map((s) => (
            <div
              key={s.id}
              className="text-xs border border-border rounded p-2"
            >
              Target #{s.target_id} → {s.destination_project} ·{" "}
              {s.metric_meaning}
              <div>
                Sharing ID: <code className="select-all">{s.id}</code>
              </div>
              {s.revoked_at ? (
                "Revoked"
              ) : (
                <button
                  className={button}
                  disabled={busy}
                  onClick={() =>
                    void run(() =>
                      analyticsRequest(
                        project,
                        `/financial-shares?id=${encodeURIComponent(s.id)}`,
                        "DELETE",
                      ),
                    )
                  }
                >
                  Revoke
                </button>
              )}
            </div>
          ))}
          <h4 className="font-medium">Combined-goal components</h4>
          <p className="text-xs text-text-dim">
            Adopt an existing component event by ID to preserve its identity and
            month filters. Source and destination must have matching saved
            periods, timezone and currency. A mix of revenue and realized profit
            is an operating goal, not pure revenue.
          </p>
          <div className="flex flex-wrap gap-2 items-end">
            <label className="flex flex-col text-xs">
              Source sharing ID
              <input
                aria-label="Source sharing ID"
                className={input}
                value={share}
                onChange={(e) => setShare(e.target.value)}
              />
            </label>
            {selectTarget(
              "Destination target",
              destinationTarget,
              setDestinationTarget,
            )}
            <label className="flex flex-col text-xs">
              Existing component event ID
              <input
                aria-label="Existing component event ID"
                className={input}
                type="number"
                value={component}
                onChange={(e) => setComponent(e.target.value)}
              />
            </label>
            <button
              className={button}
              disabled={busy || !share || !destinationTarget || !component}
              onClick={() =>
                void run(() =>
                  analyticsRequest(project, "/financial-mappings", "POST", {
                    share_id: share,
                    destination_target: Number(destinationTarget),
                    component_event_id: Number(component),
                  }),
                )
              }
            >
              Adopt component
            </button>
          </div>
          {data.mappings.map((m: any) => (
            <div
              key={m.id}
              className="border border-border rounded p-2 text-xs"
            >
              <p>
                {m.source_project} / target #{m.source_target} → target #
                {m.destination_target} · {m.metric_meaning}
              </p>
              <p>
                Source measured: {date(m.source_measured_at)} · Aggregated:{" "}
                {date(m.last_success)} · Component #{m.component_event_id}
              </p>
              {m.last_error && <p className="text-error">{m.last_error}</p>}
              <button
                className={button}
                disabled={busy || !m.enabled}
                onClick={() =>
                  void run(() =>
                    analyticsRequest(
                      project,
                      `/financial-mappings?id=${encodeURIComponent(m.id)}`,
                      "DELETE",
                    ),
                  )
                }
              >
                {m.enabled ? "Disable component refresh" : "Disabled"}
              </button>
            </div>
          ))}
        </>
      )}
    </section>
  );
}
