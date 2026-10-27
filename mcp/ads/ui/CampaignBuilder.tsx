import { useEffect, useState } from "react";
import { createdId } from "./ConversionsWorkspace";
type Call = (tool: string, args: Record<string, unknown>) => Promise<any>;
const field =
  "h-9 w-full min-w-0 rounded border border-border bg-bg-input px-3 text-sm text-text";
const button =
  "h-9 rounded border border-border px-3 text-sm hover:bg-bg-input disabled:opacity-50";
const goals: Record<string, string> = {
  channel_subscriptions: "YouTube subscriptions",
  follow_on_views: "YouTube follow-on views",
  link_clicks: "Website traffic",
  conversions: "Conversions",
  conversion_value: "Conversion value",
};
const channels = [
  "youtube_in_stream",
  "youtube_in_feed",
  "youtube_shorts",
  "discover",
  "gmail",
  "display",
];
export const commaIds = (s: string) => s.split(/[,\s]+/).filter(Boolean);
const fresh = () => ({
  name: "",
  goal: "channel_subscriptions",
  budget: "",
  targetCpa: "",
  targetRoas: "",
  budgetType: "daily",
  start: "",
  end: "",
  locations: "",
  languages: "",
  audience: "",
  audienceResource: "",
  optimizedTargeting: false,
  placements: ["youtube_in_feed", "youtube_shorts"],
  format: "responsive_video",
  business: "",
  headline: "",
  longHeadline: "",
  description: "",
  finalUrl: "",
  video: "",
  videoUrl: "",
  logo: "",
  logoUrl: "",
  image: "",
  square: "",
  cards: "",
  action: "",
  key: String(crypto.randomUUID()),
  campaign: "",
  group: "",
  ad: "",
});
type Draft = ReturnType<typeof fresh>;
export function demandCampaignInput(account: number, d: Draft) {
  const amount = Number(d.budget);
  if (!d.name.trim() || !Number.isFinite(amount) || amount <= 0)
    throw new Error("Enter a name and a positive budget.");
  if (!commaIds(d.locations).length)
    throw new Error("Select at least one location ID.");
  if (d.budgetType === "lifetime" && (!d.start || !d.end || d.end < d.start))
    throw new Error("A total budget requires start and end dates.");
  return {
    ad_account_id: account,
    name: d.name.trim(),
    channel_type: "demand_gen",
    objective:
      d.goal === "link_clicks"
        ? "traffic"
        : d.goal === "conversion_value"
          ? "sales"
          : d.goal === "conversions"
            ? "leads"
            : "engagement",
    optimization_goal: d.goal,
    ...(d.goal === "conversion_value" && d.targetRoas
      ? { target_roas: Number(d.targetRoas) }
      : d.goal !== "link_clicks" && d.targetCpa
        ? { target_cpa_cents: Math.round(Number(d.targetCpa) * 100) }
        : {}),
    ...(d.action ? { conversion_action_resource_ids: [Number(d.action)] } : {}),
    [d.budgetType === "lifetime"
      ? "lifetime_budget_cents"
      : "daily_budget_cents"]: Math.round(amount * 100),
    ...(d.start ? { start_time: d.start } : {}),
    ...(d.end ? { end_time: d.end } : {}),
    locations: commaIds(d.locations),
    languages: commaIds(d.languages),
    status: "PAUSED",
    idempotency_key: d.key + ":campaign",
  };
}
function assetId(r: any): string {
  const ref = r?.results?.[0]?.resourceName || r?.results?.[0]?.resource_name;
  if (r?.id) return String(r.id);
  if (!ref)
    throw new Error(
      "No asset ID returned; reconcile this creation before retrying.",
    );
  return ref.split("/").pop();
}
export default function CampaignBuilder({
  account,
  callTool,
  scopeKey,
  dateFrom,
  dateTo,
  onChanged,
}: {
  account: { id: number; platform: string; currency: string };
  callTool: Call;
  scopeKey: string;
  dateFrom: string;
  dateTo: string;
  onChanged: () => void;
}) {
  const storage = `ads:campaign:${scopeKey}:${account.id}`;
  const [d, setD] = useState<Draft>(() => {
    try {
      return {
        ...fresh(),
        ...JSON.parse(sessionStorage.getItem(storage) || "{}"),
      };
    } catch {
      return fresh();
    }
  });
  const [audiences, setAudiences] = useState<any[]>([]);
  const [caps, setCaps] = useState<any>(null),
    [events, setEvents] = useState<any[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [readiness, setReadiness] = useState<any>(null),
    [report, setReport] = useState<any>(null),
    [ack, setAck] = useState(false);
  useEffect(() => {
    sessionStorage.setItem(storage, JSON.stringify(d));
  }, [storage, d]);
  useEffect(() => {
    let alive = true;
    Promise.all([
      callTool("account_context_get", {
        ad_account_id: account.id,
        refresh: false,
      }),
      callTool("conversion_event_list", { ad_account_id: account.id }),
      callTool("audience_list", { ad_account_id: account.id, refresh: false }),
    ])
      .then(([c, e, a]) => {
        if (alive) {
          setCaps(c.capabilities?.demand_gen);
          setEvents(e.events || []);
          setAudiences((a.data || []).filter((v: any) => v.type === "grouped" && v.status === "active"));
        }
      })
      .catch((e) => alive && setError(e.message));
    return () => {
      alive = false;
    };
  }, [account.id]);
  const change = (key: keyof Draft, value: any) =>
    setD((old) => ({ ...old, [key]: value }));
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
  const channelGoal = ["channel_subscriptions", "follow_on_views"].includes(
    d.goal,
  );
  const build = () =>
    run(async () => {
      let draft = { ...d };
      const save = (v: Partial<Draft>) => {
        draft = { ...draft, ...v };
        setD(draft);
        sessionStorage.setItem(storage, JSON.stringify(draft));
      };
      if (!draft.campaign) {
        const r = await callTool(
          "campaign_create",
          demandCampaignInput(account.id, draft),
        );
        save({ campaign: createdId(r, "campaign_id") });
      }
      if (!draft.group) {
        const r = await callTool("adset_create", {
          ad_account_id: account.id,
          campaign_id: draft.campaign,
          name: draft.name + " audience",
          placements: draft.placements,
          locations: commaIds(draft.locations),
          languages: commaIds(draft.languages),
          optimized_targeting: draft.optimizedTargeting,
          ...(draft.audienceResource ? { audience_resource_ids: [Number(draft.audienceResource)] } : draft.audience ? { audience_ids: commaIds(draft.audience) } : {}),
          status: "PAUSED",
          idempotency_key: draft.key + ":group",
        });
        save({ group: createdId(r, "adset_id") });
      }
      if (draft.format === "responsive_video" && !draft.video) {
        if (!draft.videoUrl)
          throw new Error("Enter a YouTube URL or existing video asset IDs.");
        const url = new URL(draft.videoUrl);
        const id =
          url.hostname === "youtu.be"
            ? url.pathname.slice(1)
            : url.searchParams.get("v") || url.pathname.split("/").pop();
        const r = await callTool("creative_create", {
          ad_account_id: account.id,
          name: draft.name + " video",
          format: "video",
          video_id: id,
          idempotency_key: draft.key + ":video",
        });
        save({ video: assetId(r) });
      }
      if (!draft.logo) {
        if (!draft.logoUrl)
          throw new Error("Enter a logo image URL or existing logo asset ID.");
        const r = await callTool("creative_create", {
          ad_account_id: account.id,
          name: draft.name + " logo",
          format: "image",
          image_url: draft.logoUrl,
          idempotency_key: draft.key + ":logo",
        });
        save({ logo: assetId(r) });
      }
      if (!draft.ad) {
        const r = await callTool("ad_create", {
          ad_account_id: account.id,
          adset_id: draft.group,
          name: draft.name + " ad",
          ad_format: draft.format,
          business_name: draft.business,
          headlines: [{ text: draft.headline }],
          descriptions: [{ text: draft.description }],
          final_urls: [draft.finalUrl],
          logo_asset_ids: commaIds(draft.logo),
          ...(draft.format === "responsive_video"
            ? {
                video_asset_ids: commaIds(draft.video),
                long_headlines: [{ text: draft.longHeadline }],
              }
            : draft.format === "responsive_image"
              ? {
                  image_asset_ids: commaIds(draft.image),
                  square_image_asset_ids: commaIds(draft.square),
                }
              : { card_asset_ids: commaIds(draft.cards) }),
          status: "PAUSED",
          idempotency_key: draft.key + ":ad",
        });
        save({ ad: createdId(r, "ad_id") });
      }
      setReadiness(
        await callTool("conversion_readiness_get", {
          ad_account_id: account.id,
          campaign_id: draft.campaign,
        }),
      );
      onChanged();
    });
  const label = (
    text: string,
    key: keyof Draft,
    type = "text",
    placeholder = "",
  ) => (
    <label className="grid min-w-0 gap-1 text-xs text-text-muted">
      {text}
      <input
        className={field}
        type={type}
        placeholder={placeholder}
        value={String(d[key])}
        onChange={(e) => change(key, e.target.value)}
      />
    </label>
  );
  if (account.platform !== "google")
    return (
      <p className="p-4 text-sm">
        Demand Gen is available for Google Ads accounts.
      </p>
    );
  return (
    <section className="space-y-4 p-4">
      <p className="text-xs text-text-muted">
        Demand Gen · Creates a paused campaign, ad group and ad in your existing
        campaign list.
      </p>
      {error && (
        <p
          role="alert"
          className="rounded border border-red/30 p-3 text-sm text-red"
        >
          {error}
        </p>
      )}
      {!caps && !error && (
        <p className="text-xs">Loading campaign capabilities…</p>
      )}
      <fieldset
        disabled={busy || !!d.campaign}
        className="grid min-w-0 gap-3 rounded border border-border p-3 sm:grid-cols-2"
      >
        <legend className="px-1 text-xs font-medium">Campaign</legend>
        {label("Campaign name", "name")}
        <label className="grid gap-1 text-xs">
          Optimization goal
          <select
            className={field}
            value={d.goal}
            onChange={(e) => {
              change("goal", e.target.value);
              change("action", "");
              if (
                ["channel_subscriptions", "follow_on_views"].includes(
                  e.target.value,
                )
              )
                change("placements", ["youtube_in_feed", "youtube_shorts"]);
            }}
          >
            {Object.entries(goals).map(([v, n]) => (
              <option key={v} value={v}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <label className="grid gap-1 text-xs">
          Budget type
          <select
            className={field}
            value={d.budgetType}
            onChange={(e) => change("budgetType", e.target.value)}
          >
            <option value="daily">Daily</option>
            <option value="lifetime">Campaign total</option>
          </select>
        </label>
        {label(`Budget (${account.currency})`, "budget", "number")}
        {d.goal === "conversion_value"
          ? label("Target ROAS (optional)", "targetRoas", "number")
          : d.goal !== "link_clicks"
            ? label(
                `Target CPA (${account.currency}, optional)`,
                "targetCpa",
                "number",
              )
            : null}
        {label("Start date", "start", "date")}
        {label("End date", "end", "date")}
        <label className="grid gap-1 text-xs sm:col-span-2">
          Conversion action
          <select
            className={field}
            value={d.action}
            onChange={(e) => change("action", e.target.value)}
          >
            <option value="">
              {channelGoal
                ? "Automatically select matching YouTube goal"
                : "Use account goals"}
            </option>
            {events
              .filter((r) => !channelGoal || r.metadata?.event === d.goal)
              .map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} · {r.status}
                </option>
              ))}
          </select>
        </label>
      </fieldset>
      <fieldset
        disabled={busy || !!d.group}
        className="grid min-w-0 gap-3 rounded border border-border p-3 sm:grid-cols-2"
      >
        <legend className="px-1 text-xs font-medium">Ad group</legend>
        {label(
          "Location IDs",
          "locations",
          "text",
          "2724 (Spain), 2840 (United States)",
        )}
        {label("Language IDs", "languages", "text", "1000 (English)")}
        <label className="grid gap-1 text-xs">Grouped audience (optional)<select className="h-9 min-w-0 rounded border border-border bg-bg-input px-2" value={d.audienceResource} onChange={e => change("audienceResource", e.target.value)}><option value="">{d.audience ? `Saved native audience ${d.audience}` : "No audience selected"}</option>{audiences.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}</select><span className="text-text-muted">Create reusable groups in Audiences.</span></label>
        <label className="flex items-start gap-2 text-xs sm:col-span-2"><input type="checkbox" checked={d.optimizedTargeting} onChange={e => change("optimizedTargeting", e.target.checked)} />Allow optimized targeting beyond selected audiences</label>
        <div className="flex flex-wrap gap-3 text-xs sm:col-span-2">
          {channels
            .filter((c) => !channelGoal || c.startsWith("youtube_"))
            .map((c) => (
              <label key={c} className="flex items-center gap-1">
                <input
                  type="checkbox"
                  checked={d.placements.includes(c)}
                  onChange={(e) =>
                    change(
                      "placements",
                      e.target.checked
                        ? [...d.placements, c]
                        : d.placements.filter((x) => x !== c),
                    )
                  }
                />
                {c.replaceAll("_", " ")}
              </label>
            ))}
        </div>
      </fieldset>
      <fieldset
        disabled={busy || !!d.ad}
        className="grid min-w-0 gap-3 rounded border border-border p-3 sm:grid-cols-2"
      >
        <legend className="px-1 text-xs font-medium">Creative and ad</legend>
        <label className="grid gap-1 text-xs">
          Ad format
          <select
            className={field}
            value={d.format}
            onChange={(e) => change("format", e.target.value)}
          >
            {(
              caps?.ad_formats || [
                "responsive_video",
                "responsive_image",
                "carousel",
              ]
            ).map((v: string) => (
              <option key={v} value={v}>
                {v.replaceAll("_", " ")}
              </option>
            ))}
          </select>
        </label>
        {label("Business name", "business")}
        {label("Headline", "headline")}
        {label("Description", "description")}
        {label("Destination URL", "finalUrl")}
        {label("Logo asset IDs", "logo")}
        {!d.logo && label("Or logo image URL", "logoUrl")}
        {d.format === "responsive_video" ? (
          <>
            {label("Long headline", "longHeadline")}
            {label("Video asset IDs", "video")}
            {!d.video && label("Or YouTube video URL", "videoUrl")}
          </>
        ) : d.format === "responsive_image" ? (
          <>
            {label("Landscape image asset IDs", "image")}
            {label("Square image asset IDs", "square")}
          </>
        ) : (
          label("Carousel card asset IDs (2–10)", "cards")
        )}
      </fieldset>
      <div className="flex flex-wrap gap-2">
        <button
          className={button}
          type="button"
          disabled={busy || !!d.ad || !caps?.supported}
          onClick={build}
        >
          {d.campaign ? "Continue paused campaign" : "Create paused campaign"}
        </button>
        {d.campaign && (
          <button
            className={button}
            type="button"
            disabled={busy}
            onClick={() =>
              run(async () => {
                setReadiness(
                  await callTool("conversion_readiness_get", {
                    ad_account_id: account.id,
                    campaign_id: d.campaign,
                  }),
                );
                setReport(
                  await callTool("conversion_performance_get", {
                    ad_account_id: account.id,
                    level: "campaign",
                    entity_ids: [d.campaign],
                    date_from: dateFrom,
                    date_to: dateTo,
                    refresh: true,
                  }),
                );
              })
            }
          >
            Refresh results
          </button>
        )}
        <button
          className={button}
          type="button"
          disabled={busy}
          onClick={() => {
            setD(fresh());
            setReadiness(null);
            setReport(null);
            setAck(false);
          }}
        >
          New draft
        </button>
      </div>
      {d.campaign && (
        <p className="break-words text-xs text-text-muted">
          Campaign {d.campaign}
          {d.group && ` · Ad group ${d.group}`}
          {d.ad && ` · Ad ${d.ad}`}. Creation keys are retained when resuming
          this draft.
        </p>
      )}
      {readiness && (
        <div className="space-y-2 rounded border border-border p-3 text-sm">
          <p>Measurement: {readiness.status}</p>
          <p className="text-xs text-text-muted">{readiness.reason}</p>
          {readiness.status === "unknown" && (
            <label className="flex gap-2 text-xs">
              <input
                type="checkbox"
                checked={ack}
                onChange={(e) => setAck(e.target.checked)}
              />
              I verified that the promoted YouTube channel is linked to this
              Google Ads account.
            </label>
          )}
          {d.ad && (
            <button
              className={button}
              type="button"
              disabled={
                busy ||
                (!readiness.ready && !(readiness.status === "unknown" && ack))
              }
              onClick={() =>
                run(async () => {
                  await callTool("delivery_activate", {
                    ad_account_id: account.id,
                    campaign_id: d.campaign,
                    adset_id: d.group,
                    ad_id: d.ad,
                    acknowledge_unverified_measurement: ack,
                  });
                  onChanged();
                })
              }
            >
              Activate delivery
            </button>
          )}
        </div>
      )}
      {report && (
        <div className="overflow-auto rounded border border-border p-3 text-xs">
          <p>
            Event-level results · {dateFrom} to {dateTo}
          </p>
          <table className="mt-2 w-full text-left">
            <thead>
              <tr>
                <th>Event</th>
                <th>Conversions</th>
                <th>Cost per result</th>
              </tr>
            </thead>
            <tbody>
              {(report.events || report.data || []).map((r: any, i: number) => (
                <tr key={i}>
                  <td>{r.event}</td>
                  <td>{r.conversions ?? "Unavailable"}</td>
                  <td>
                    {r.cost_per_event_micros == null
                      ? "Unavailable"
                      : new Intl.NumberFormat(undefined, {
                          style: "currency",
                          currency: account.currency,
                        }).format(r.cost_per_event_micros / 1e6)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
