import { useEffect, useState } from "react";
import { commaIds } from "./CampaignBuilder";
type Call = (tool: string, args: Record<string, unknown>) => Promise<any>;
const field =
  "h-8 w-full min-w-0 rounded border border-border bg-bg-input px-2 text-xs text-text";
const button =
  "h-8 rounded border border-border px-3 text-xs hover:bg-bg-input disabled:opacity-50";
export default function CampaignSettings({
  accountId,
  campaignId,
  groupId,
  callTool,
  dateFrom,
  dateTo,
  currency,
}: {
  accountId: number;
  campaignId: string;
  groupId: string | null;
  callTool: Call;
  dateFrom: string;
  dateTo: string;
  currency: string;
}) {
  const [cfg, setCfg] = useState<any>(null),
    [group, setGroup] = useState<any>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [budget, setBudget] = useState(""),
    [goal, setGoal] = useState(""),
    [cpa, setCpa] = useState(""),
    [roas, setRoas] = useState(""),
    [placements, setPlacements] = useState<string[]>([]),
    [locations, setLocations] = useState(""),
    [languages, setLanguages] = useState(""),
    [report, setReport] = useState<any>(null),
    [reportMode, setReportMode] = useState("events"),
    [ready, setReady] = useState<any>(null),
    [ack, setAck] = useState(false);
  const load = async () => {
    const c = await callTool("campaign_configuration_get", {
      ad_account_id: accountId,
      campaign_id: campaignId,
    });
    setCfg(c);
    setGoal(c.settings?.optimization_goal || "");
    setBudget(
      String(
        Number(c.budget?.amountMicros || c.budget?.totalAmountMicros || 0) /
          1e6,
      ),
    );
    if (c.channel_type === "demand_gen")
      setReady(
        await callTool("conversion_readiness_get", {
          ad_account_id: accountId,
          campaign_id: campaignId,
        }),
      );
  };
  useEffect(() => {
    let current = true;
    callTool("campaign_configuration_get", {
      ad_account_id: accountId,
      campaign_id: campaignId,
    })
      .then((c) => {
        if (current) {
          setCfg(c);
          setGoal(c.settings?.optimization_goal || "");
          setBudget(
            String(
              Number(
                c.budget?.amountMicros || c.budget?.totalAmountMicros || 0,
              ) / 1e6,
            ),
          );
        }
      })
      .catch((e) => current && setError(e.message));
    return () => {
      current = false;
    };
  }, [accountId, campaignId]);
  useEffect(() => {
    let current = true;
    setGroup(null);
    if (groupId && cfg?.channel_type === "demand_gen")
      callTool("adset_configuration_get", {
        ad_account_id: accountId,
        adset_id: groupId,
      })
        .then((g) => {
          if (current) {
            setGroup(g);
            setPlacements(g.placements || []);
            setLocations((g.locations || []).join(", "));
            setLanguages((g.languages || []).join(", "));
          }
        })
        .catch((e) => current && setError(e.message));
    return () => {
      current = false;
    };
  }, [accountId, groupId, cfg?.channel_type]);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e: any) {
      setError(e.message || String(e));
    } finally {
      setBusy(false);
    }
  };
  if (cfg && cfg.channel_type !== "demand_gen") return null;
  return (
    <details className="border-b border-border px-4 py-3">
      <summary className="cursor-pointer text-xs font-medium">
        Campaign settings and conversion results
      </summary>
      <div className="mt-3 space-y-3">
        {error && (
          <p role="alert" className="text-xs text-red">
            {error}
          </p>
        )}
        {cfg && (
          <>
            <p className="text-xs text-text-muted">
              Demand Gen · {cfg.bidding_strategy} ·{" "}
              {cfg.start_time || "Started"} to {cfg.end_time || "No end date"}
            </p>
            <div className="grid gap-2 sm:grid-cols-3">
              <label className="grid gap-1 text-xs">
                {cfg.budget?.period === "CUSTOM_PERIOD"
                  ? "Total budget"
                  : "Daily budget"}
                <input
                  className={field}
                  type="number"
                  value={budget}
                  onChange={(e) => setBudget(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-xs">
                Optimization goal
                <select
                  className={field}
                  value={goal}
                  onChange={(e) => setGoal(e.target.value)}
                >
                  <option value="">Select a goal</option>
                  {[
                    "link_clicks",
                    "conversions",
                    "conversion_value",
                    "channel_subscriptions",
                    "follow_on_views",
                  ].map((v) => (
                    <option key={v} value={v}>
                      {v.replaceAll("_", " ")}
                    </option>
                  ))}
                </select>
              </label>
              <label className="grid gap-1 text-xs">
                {goal === "conversion_value"
                  ? "Target ROAS (optional)"
                  : "Target CPA (optional)"}
                <input
                  className={field}
                  value={goal === "conversion_value" ? roas : cpa}
                  onChange={(e) =>
                    goal === "conversion_value"
                      ? setRoas(e.target.value)
                      : setCpa(e.target.value)
                  }
                />
              </label>
            </div>
            <button
              className={button}
              type="button"
              disabled={busy}
              onClick={() =>
                run(async () => {
                  const cents = Math.round(Number(budget) * 100);
                  if (!Number.isFinite(cents) || cents <= 0)
                    throw new Error("Enter a positive budget.");
                  await callTool("campaign_update", {
                    ad_account_id: accountId,
                    campaign_id: campaignId,
                    [cfg.budget?.period === "CUSTOM_PERIOD"
                      ? "lifetime_budget_cents"
                      : "daily_budget_cents"]: cents,
                    ...(goal && goal !== cfg.settings?.optimization_goal
                      ? { optimization_goal: goal }
                      : {}),
                    ...(goal === "conversion_value" && roas
                      ? { target_roas: Number(roas) }
                      : goal !== "link_clicks" && cpa
                        ? { target_cpa_cents: Math.round(Number(cpa) * 100) }
                        : {}),
                  });
                  await load();
                })
              }
            >
              Save campaign settings
            </button>
          </>
        )}
        {group && (
          <div className="space-y-2 rounded border border-border p-2">
            <p className="text-xs font-medium">Ad group {groupId}</p>
            <div className="flex flex-wrap gap-2 text-xs">
              {[
                "youtube_in_stream",
                "youtube_in_feed",
                "youtube_shorts",
                "discover",
                "gmail",
                "display",
              ].map((v) => (
                <label key={v} className="flex items-center gap-1">
                  <input
                    type="checkbox"
                    checked={placements.includes(v)}
                    onChange={(e) =>
                      setPlacements(
                        e.target.checked
                          ? [...placements, v]
                          : placements.filter((x) => x !== v),
                      )
                    }
                  />
                  {v.replaceAll("_", " ")}
                </label>
              ))}
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <label className="grid gap-1 text-xs">
                Location IDs
                <input
                  className={field}
                  value={locations}
                  onChange={(e) => setLocations(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-xs">
                Language IDs
                <input
                  className={field}
                  value={languages}
                  onChange={(e) => setLanguages(e.target.value)}
                />
              </label>
            </div>
            <button
              className={button}
              type="button"
              disabled={busy}
              onClick={() =>
                run(async () => {
                  await callTool("adset_update", {
                    ad_account_id: accountId,
                    adset_id: groupId,
                    placements,
                    locations: commaIds(locations),
                    languages: commaIds(languages),
                  });
                  setGroup(
                    await callTool("adset_configuration_get", {
                      ad_account_id: accountId,
                      adset_id: groupId,
                    }),
                  );
                })
              }
            >
              Save ad group targeting
            </button>
          </div>
        )}
        <label className="grid gap-1 text-xs">
          Report breakdown
          <select
            className={field}
            value={reportMode}
            onChange={(e) => {
              setReportMode(e.target.value);
              setReport(null);
            }}
          >
            <option value="events">Conversion events</option>
            <option value="placement">Channels and placements</option>
            <option value="creative">Creative assets</option>
          </select>
        </label>
        <button
          className={button}
          type="button"
          disabled={busy}
          onClick={() =>
            run(async () => {
              await load();
              setReport(
                await callTool(
                  reportMode === "events"
                    ? "conversion_performance_get"
                    : "performance_get",
                  {
                    ad_account_id: accountId,
                    level: "campaign",
                    entity_ids: [campaignId],
                    date_from: dateFrom,
                    date_to: dateTo,
                    ...(reportMode !== "events"
                      ? { breakdown: reportMode }
                      : {}),
                    refresh: true,
                  },
                ),
              );
            })
          }
        >
          Refresh results
        </button>
        {ready && (
          <div className="space-y-2 text-xs">
            <p>
              Measurement: {ready.status}. {ready.reason}
            </p>
            {ready.status === "unknown" && (
              <>
                <label className="flex gap-2">
                  <input
                    type="checkbox"
                    checked={ack}
                    onChange={(e) => setAck(e.target.checked)}
                  />
                  I verified the promoted channel link in Google Ads.
                </label>
                <button
                  className={button}
                  type="button"
                  disabled={busy || !ack}
                  onClick={() =>
                    run(async () => {
                      await callTool("campaign_resume", {
                        ad_account_id: accountId,
                        campaign_id: campaignId,
                        acknowledge_unverified_measurement: ack,
                      });
                      await load();
                    })
                  }
                >
                  Resume campaign
                </button>
              </>
            )}
          </div>
        )}
        {report && (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr>
                  <th>
                    {reportMode === "events"
                      ? "Event"
                      : reportMode === "placement"
                        ? "Placement"
                        : "Asset"}
                  </th>
                  <th>{reportMode === "events" ? "Source" : "Impressions"}</th>
                  <th>{reportMode === "events" ? "Conversions" : "Clicks"}</th>
                  <th>
                    {reportMode === "events" ? "Cost per result" : "Spend"}
                  </th>
                </tr>
              </thead>
              <tbody>
                {(report.events || report.data || []).map(
                  (r: any, i: number) => {
                    const money =
                      reportMode === "events"
                        ? r.cost_per_event_micros
                        : r.spend_micros;
                    return (
                      <tr key={i}>
                        <td>
                          {reportMode === "events"
                            ? r.event
                            : reportMode === "placement"
                              ? `${r.channel || ""} ${r.placement || ""}`
                              : `${r.asset_id || ""} ${r.asset_type || ""}`}
                        </td>
                        <td>
                          {reportMode === "events"
                            ? r.measurement_source
                            : (r.impressions ?? "Unavailable")}
                        </td>
                        <td>
                          {reportMode === "events"
                            ? (r.conversions ?? "Unavailable")
                            : (r.clicks ?? "Unavailable")}
                        </td>
                        <td>
                          {money == null
                            ? "Unavailable"
                            : new Intl.NumberFormat(undefined, {
                                style: "currency",
                                currency,
                              }).format(money / 1e6)}
                        </td>
                      </tr>
                    );
                  },
                )}
              </tbody>
            </table>
            {reportMode === "creative" && (
              <p className="mt-2 text-xs text-text-muted">
                Asset metrics can overlap. Use campaign totals for delivery
                spend.
              </p>
            )}
          </div>
        )}
      </div>
    </details>
  );
}
