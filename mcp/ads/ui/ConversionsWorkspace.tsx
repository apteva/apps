import { useEffect, useRef, useState } from "react";

type CallTool = (tool: string, args: Record<string, unknown>) => Promise<any>;
type Account = { id: number; platform: string; currency: string };
type Resource = {
  id: number;
  name: string;
  status: string;
  metadata: Record<string, any>;
};
const sourceLabels: Record<string, string> = {
  meta_sdk: "Meta App Events SDK",
  firebase: "Firebase",
  google_play: "Google Play",
  mmp: "Mobile measurement partner",
};
const eventLabels: Record<string, string> = {
  install: "Installs",
  first_open: "First opens",
  registration: "Registrations",
  trial: "Trials",
  purchase: "Purchases",
  subscription: "Subscriptions",
  value: "Revenue",
  reengagement: "App re-engagements",
  in_app_event: "In-app events",
  installs: "Installs",
};
const field =
  "h-9 w-full rounded border border-border bg-bg-input px-3 text-sm text-text";
const button =
  "h-9 rounded border border-border px-3 text-sm hover:bg-bg-input disabled:opacity-50";

export function storeIdentifier(os: string, raw: string): string {
  try {
    const url = new URL(raw);
    if (url.protocol !== "https:" || url.username || url.password) return "";
    if (
      os === "android" &&
      url.hostname === "play.google.com" &&
      url.pathname === "/store/apps/details"
    ) {
      const id = url.searchParams.get("id") || "";
      return /^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$/.test(id)
        ? id
        : "";
    }
    if (os === "ios" && url.hostname === "apps.apple.com")
      return url.pathname.match(/\/id(\d+)(?:\/|$)/)?.[1] || "";
  } catch {
    /* Invalid URLs stay invalid. */
  }
  return "";
}
export function mobileCampaignInput(
  accountId: number,
  appId: number,
  sourceId: number,
  eventId: number,
  goal: string,
  name: string,
  budget: string,
  key: string,
): Record<string, unknown> {
  const daily = Number(budget);
  if (!name.trim() || !Number.isFinite(daily) || daily <= 0)
    throw new Error("Enter a campaign name and a positive daily budget.");
  if ((goal === "in_app_event" || goal === "value") && !eventId)
    throw new Error("Select an eligible conversion event.");
  return {
    ad_account_id: accountId,
    mobile_app_resource_id: appId,
    measurement_source_resource_id: sourceId,
    ...(eventId ? { conversion_event_resource_id: eventId } : {}),
    app_goal: goal,
    name: name.trim(),
    objective: "app_promotion",
    status: "PAUSED",
    daily_budget_cents: Math.round(daily * 100),
    idempotency_key: key,
  };
}
export function createdId(result: any, ...keys: string[]): string {
  for (const key of ["id", ...keys]) {
    const value = result?.[key];
    if (value != null && String(value) !== "") return String(value);
  }
  throw new Error(
    "Provider returned no entity ID. Reconcile this request before retrying.",
  );
}
export function metricDisplay(
  value: unknown,
  currency: string,
  money = false,
): string {
  if (value == null || !Number.isFinite(Number(value))) return "Unavailable";
  return money
    ? new Intl.NumberFormat(undefined, {
        style: "currency",
        currency: currency || "USD",
      }).format(Number(value) / 1e6)
    : new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(
        Number(value),
      );
}

export default function ConversionsWorkspace({
  account,
  callTool,
  dateFrom,
  dateTo,
  refreshKey = 0,
  cacheRefreshKey = 0,
  scopeKey = "",
}: {
  scopeKey?: string;
  account: Account;
  callTool: CallTool;
  dateFrom: string;
  dateTo: string;
  refreshKey?: number;
  cacheRefreshKey?: number;
}) {
  const [capabilities, setCapabilities] = useState<any>(null);
  const [apps, setApps] = useState<Resource[]>([]);
  const [sources, setSources] = useState<Resource[]>([]);
  const [events, setEvents] = useState<Resource[]>([]);
  const [appId, setAppId] = useState(0);
  const [sourceId, setSourceId] = useState(0);
  const [eventId, setEventId] = useState(0);
  const [sourceKind, setSourceKind] = useState("");
  const [sourceName, setSourceName] = useState("");
  const [readiness, setReadiness] = useState<any>(null);
  const [report, setReport] = useState<any>(null);
  const [reportSource, setReportSource] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [os, setOS] = useState("android");
  const [storeURL, setStoreURL] = useState("");
  const [providerAppId, setProviderAppId] = useState("");
  const [goal, setGoal] = useState("installs");
  const [campaignName, setCampaignName] = useState("");
  const [budget, setBudget] = useState("");
  const [country, setCountry] = useState("");
  const [locationQuery, setLocationQuery] = useState("");
  const [locations, setLocations] = useState<any[]>([]);
  const [locationId, setLocationId] = useState("");
  const [headline, setHeadline] = useState("");
  const [description, setDescription] = useState("");
  const [asset, setAsset] = useState("");
  const [mediaKeys, setMediaKeys] = useState("");
  const [beneficiary, setBeneficiary] = useState("");
  const [payor, setPayor] = useState("");
  const [build, setBuild] = useState<{
    campaign?: string;
    group?: string;
    creative?: string;
    ad?: string;
    job?: string;
    key: string;
  }>(() => ({ key: crypto.randomUUID() }));
  const [acknowledge, setAcknowledge] = useState(false);
  const [activation, setActivation] = useState<any>(null);
  const generation = useRef(0);
  const reportRequest = useRef("");
  const draftStorageKey = `ads:mobile:v1:${scopeKey}:${account.id}`;
  const [draftLoaded, setDraftLoaded] = useState(false);
  const args = { ad_account_id: account.id };
  const selectedApp = apps.find((app) => app.id === appId);
  const money = (value: any) => metricDisplay(value, account.currency, true);
  const operation = async (run: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await run();
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };
  const reloadResources = async () => {
    const [appsResult, sourcesResult] = await Promise.all([
      callTool("resource_list", { ...args, kind: "mobile_app" }),
      callTool("resource_list", { ...args, kind: "measurement_source" }),
    ]);
    setApps(appsResult.data || []);
    setSources(sourcesResult.data || []);
  };
  useEffect(() => {
    const current = ++generation.current;
    setApps([]);
    setSources([]);
    setEvents([]);
    setAppId(0);
    setSourceId(0);
    setEventId(0);
    setReadiness(null);
    setError("");
    setActivation(null);
    let draft: any = null;
    try {
      draft = JSON.parse(sessionStorage.getItem(draftStorageKey) || "null");
    } catch {}
    setBuild(draft?.build || { key: crypto.randomUUID() });
    if (draft) {
      setAppId(draft.appId || 0);
      setSourceId(draft.sourceId || 0);
      setEventId(draft.eventId || 0);
      setGoal(draft.goal || "installs");
      setCampaignName(draft.campaignName || "");
      setBudget(draft.budget || "");
      setCountry(draft.country || "");
      setLocationId(draft.locationId || "");
      setHeadline(draft.headline || "");
      setDescription(draft.description || "");
      setAsset(draft.asset || "");
      setMediaKeys(draft.mediaKeys || "");
      setBeneficiary(draft.beneficiary || "");
      setPayor(draft.payor || "");
      setEvents(draft.events || []);
    }
    setDraftLoaded(true);
    Promise.all([
      callTool("conversion_capabilities_get", args),
      callTool("resource_list", { ...args, kind: "mobile_app" }),
      callTool("resource_list", { ...args, kind: "measurement_source" }),
    ])
      .then(([caps, apps, sources]) => {
        if (generation.current !== current) return;
        setCapabilities(caps);
        setSourceKind(caps.measurement_sources?.[0] || "");
        setApps(apps.data || []);
        setSources(sources.data || []);
      })
      .catch((error) => {
        if (generation.current === current)
          setError(String(error.message || error));
      });
    return () => {
      generation.current++;
    };
  }, [account.id, callTool, draftStorageKey]);
  useEffect(() => {
    if (draftLoaded) {
      try {
        sessionStorage.setItem(
          draftStorageKey,
          JSON.stringify({
            build,
            appId,
            sourceId,
            eventId,
            goal,
            campaignName,
            budget,
            country,
            locationId,
            headline,
            description,
            asset,
            mediaKeys,
            beneficiary,
            payor,
            events,
          }),
        );
      } catch {}
    }
  }, [
    draftLoaded,
    draftStorageKey,
    build,
    appId,
    sourceId,
    eventId,
    goal,
    campaignName,
    budget,
    country,
    locationId,
    headline,
    description,
    asset,
    mediaKeys,
    beneficiary,
    payor,
    events,
  ]);
  useEffect(() => {
    if (!draftLoaded) return;
    let live = true;
    setReport(null);
    const signature = JSON.stringify([
      account.id,
      appId,
      dateFrom,
      dateTo,
      reportSource,
      refreshKey,
    ]);
    const liveRefresh = reportRequest.current !== signature;
    reportRequest.current = signature;
    const reportArgs = {
      ...args,
      date_from: dateFrom,
      date_to: dateTo,
      level: "campaign",
      ...(appId ? { mobile_app_resource_id: appId } : {}),
      ...(reportSource ? { measurement_source: reportSource } : {}),
    };
    callTool("conversion_performance_get", {
      ...args,
      date_from: dateFrom,
      date_to: dateTo,
      level: "campaign",
      refresh: liveRefresh,
      ...(appId ? { mobile_app_resource_id: appId } : {}),
      ...(reportSource ? { measurement_source: reportSource } : {}),
    })
      .then((value) => {
        if (live) setReport(value);
      })
      .catch(async (error) => {
        if (live) {
          setError(String(error.message || error));
          try {
            const cached = await callTool("conversion_performance_get", {
              ...reportArgs,
              refresh: false,
            });
            if (live) setReport(cached);
          } catch {}
        }
      });
    return () => {
      live = false;
    };
  }, [
    account.id,
    appId,
    dateFrom,
    dateTo,
    reportSource,
    refreshKey,
    callTool,
    draftLoaded,
    cacheRefreshKey,
  ]);
  const check = async () => {
    const base = {
      ...args,
      mobile_app_resource_id: appId,
      ...(sourceId ? { measurement_source_resource_id: sourceId } : {}),
      ...(eventId ? { conversion_event_resource_id: eventId } : {}),
    };
    const ready = await callTool("conversion_readiness_get", {
      ...base,
      refresh: true,
    });
    const discovered =
      ready.events ||
      (await callTool("conversion_event_list", { ...base, refresh: false }))
        .data ||
      [];
    setEvents(discovered);
    setReadiness(ready);
  };
  const createCampaign = async () => {
    const input = mobileCampaignInput(
      account.id,
      appId,
      sourceId,
      eventId,
      goal,
      campaignName,
      budget,
      `${build.key}:campaign`,
    );
    if (account.platform === "google" || account.platform === "x") {
      if (!locationId) throw new Error("Select the campaign location.");
      if (account.platform === "google") input.locations = [locationId];
      if (account.platform === "x" && !/^[A-Z]{2}$/.test(country))
        throw new Error("Enter the app store country.");
    } else if (!/^[A-Z]{2}$/.test(country))
      throw new Error("Enter the two-letter target country.");
    const result = await callTool("campaign_create", input);
    const campaign = createdId(result, "campaign_id");
    setBuild((current) => ({ ...current, campaign }));
  };
  const createGroup = async () => {
    const input: any = {
      ...args,
      campaign_id: build.campaign,
      name: `${campaignName} audience`,
      status: "PAUSED",
      idempotency_key: `${build.key}:group`,
    };
    if (account.platform === "meta" || account.platform === "reddit")
      input.targeting = {
        geo_locations: { countries: [country] },
        targeting_automation: { advantage_audience: 0 },
      };
    if (account.platform === "reddit")
      input.targeting = { geolocations: [country] };
    if (account.platform === "x")
      input.targeting = { location_ids: [locationId] };
    if (account.platform === "reddit")
      input.daily_budget_cents = Math.round(Number(budget) * 100);
    if (account.platform === "meta") {
      input.dsa_beneficiary = beneficiary;
      input.dsa_payor = payor;
    }
    const result = await callTool("adset_create", input);
    const group = createdId(result, "adset_id", "ad_group_id");
    setBuild((current) => ({ ...current, group }));
  };
  const createAd = async () => {
    const common = {
      ...args,
      mobile_app_resource_id: appId,
      measurement_source_resource_id: sourceId,
      ...(eventId ? { conversion_event_resource_id: eventId } : {}),
      app_goal: goal,
    };
    if (account.platform === "google") {
      const result = await callTool("ad_create", {
        ...common,
        adset_id: build.group,
        name: campaignName,
        ad_format: "app",
        headlines: [headline],
        descriptions: [description],
        ...(asset
          ? {
              image_asset_ids: asset
                .split(",")
                .map((value) => value.trim())
                .filter(Boolean),
            }
          : {}),
        idempotency_key: `${build.key}:ad`,
      });
      const ad = createdId(result, "ad_id");
      setBuild((current) => ({ ...current, ad }));
    } else if (!build.creative && !build.job) {
      const result = await callTool("creative_create", {
        ...common,
        name: campaignName,
        headline,
        primary_text: description,
        format: "image",
        ...(account.platform === "x"
          ? {
              media_keys: mediaKeys
                .split(",")
                .map((value) => value.trim())
                .filter(Boolean),
              app_country_code: country,
            }
          : account.platform === "reddit"
            ? { image_url: asset }
            : { image_hash: asset }),
        dsa_beneficiary: beneficiary,
        dsa_payor: payor,
        idempotency_key: `${build.key}:creative`,
      });
      const created =
        result.job_id || result.post_creation_job_id
          ? { job: String(result.job_id || result.post_creation_job_id) }
          : { creative: createdId(result, "creative_id", "post_id") };
      setBuild((current) => ({ ...current, ...created }));
    } else if (build.creative) {
      const result = await callTool("ad_create", {
        ...common,
        adset_id: build.group,
        creative_id: build.creative,
        name: campaignName,
        status: "PAUSED",
        idempotency_key: `${build.key}:ad`,
      });
      const ad = createdId(result, "ad_id");
      setBuild((current) => ({ ...current, ad }));
    }
  };
  const pollCreative = async () => {
    const result = await callTool("creative_asset_status", {
      ...args,
      asset_id: build.job,
    });
    const job = result.data || result;
    const id = job.post_id || job.creative_id;
    if (
      id &&
      !["PROCESSING", "PENDING", "FAILED"].includes(
        String(job.status).toUpperCase(),
      )
    )
      setBuild((current) => ({
        ...current,
        creative: String(id),
        job: undefined,
      }));
    else throw new Error("Creative is still processing. Check again shortly.");
  };
  if (!capabilities)
    return (
      <div className="p-5">{error || "Loading conversion capabilities…"}</div>
    );
  return (
    <div className="space-y-6 p-4">
      {error && (
        <p
          role="alert"
          className="rounded border border-red/30 bg-red/10 p-3 text-sm text-red"
        >
          {error}
        </p>
      )}
      <section className="rounded border border-border p-4">
        <h3 className="font-semibold">Mobile app measurement</h3>
        <p className="mt-1 text-sm text-text-muted">
          Connect an existing app and its measurement source, then check which
          events can be used for advertising.
        </p>
        <label className="mt-4 block text-sm">
          App
          <select
            className={`${field} mt-1`}
            value={appId}
            disabled={busy || !!build.campaign}
            onChange={(event) => {
              setAppId(Number(event.target.value));
              setEventId(0);
              setSourceId(0);
              setEvents([]);
              setReadiness(null);
            }}
          >
            <option value={0}>Select an app</option>
            {apps.map((app) => (
              <option key={app.id} value={app.id}>
                {app.name} · {app.metadata.os}
              </option>
            ))}
          </select>
        </label>
        <details className="mt-3">
          <summary className="cursor-pointer text-sm">Add an app</summary>
          <form
            className="mt-3 grid gap-3 sm:grid-cols-2"
            onSubmit={(event) => {
              event.preventDefault();
              operation(async () => {
                const id =
                  account.platform === "meta"
                    ? providerAppId
                    : storeIdentifier(os, storeURL);
                const result = await callTool("mobile_app_bind", {
                  ...args,
                  name,
                  os,
                  store_url: storeURL,
                  app_id: id,
                });
                await reloadResources();
                setAppId(result.resource.id);
              });
            }}
          >
            <label className="text-sm">
              Name
              <input
                required
                className={field}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <label className="text-sm">
              Operating system
              <select
                className={field}
                value={os}
                onChange={(event) => setOS(event.target.value)}
              >
                <option value="android">Android</option>
                <option value="ios">iOS</option>
              </select>
            </label>
            <label className="text-sm sm:col-span-2">
              Store URL
              <input
                required
                type="url"
                className={field}
                value={storeURL}
                onChange={(event) => setStoreURL(event.target.value)}
              />
            </label>
            {account.platform === "meta" && (
              <label className="text-sm">
                Meta application ID
                <input
                  required
                  className={field}
                  value={providerAppId}
                  onChange={(event) => setProviderAppId(event.target.value)}
                />
              </label>
            )}
            <button className={button} disabled={busy || !!build.campaign}>
              Save app
            </button>
          </form>
        </details>
        {selectedApp && (
          <div className="mt-4 space-y-3">
            <label className="block text-sm">
              Measurement source
              <select
                className={field}
                value={sourceId}
                disabled={busy || !!build.campaign}
                onChange={(event) => {
                  setSourceId(Number(event.target.value));
                  setReadiness(null);
                }}
              >
                <option value={0}>Select a source</option>
                {sources
                  .filter(
                    (source) =>
                      source.metadata.mobile_app_resource_id === appId,
                  )
                  .map((source) => (
                    <option key={source.id} value={source.id}>
                      {source.name}
                    </option>
                  ))}
              </select>
            </label>
            <form
              className="flex flex-wrap items-end gap-2"
              onSubmit={(event) => {
                event.preventDefault();
                operation(async () => {
                  const result = await callTool("measurement_source_bind", {
                    ...args,
                    mobile_app_resource_id: appId,
                    source: sourceKind,
                    name: sourceName,
                  });
                  await reloadResources();
                  setSourceId(result.resource.id);
                  setReadiness({ next_action: result.next_action });
                });
              }}
            >
              <label className="text-sm">
                Add measurement source
                <select
                  className={field}
                  value={sourceKind}
                  onChange={(event) => setSourceKind(event.target.value)}
                >
                  {capabilities.measurement_sources.map((source: string) => (
                    <option key={source} value={source}>
                      {sourceLabels[source] || source}
                    </option>
                  ))}
                </select>
              </label>
              {sourceKind === "mmp" && (
                <label className="text-sm">
                  Partner name
                  <input
                    required
                    className={field}
                    value={sourceName}
                    placeholder="Your measurement partner"
                    onChange={(event) => setSourceName(event.target.value)}
                  />
                </label>
              )}
              <button className={button} disabled={busy || !!build.campaign}>
                Connect existing source
              </button>
            </form>
            <p className="text-xs text-text-muted">
              Saving a source records your configuration. SDK installation,
              provider linking, and event receipt are checked separately.
            </p>
            <button
              className={button}
              disabled={busy}
              onClick={() => operation(check)}
            >
              Discover events and check setup
            </button>
            <label className="block text-sm">
              Conversion event
              <select
                className={field}
                value={eventId}
                disabled={!!build.campaign}
                onChange={(event) => {
                  setEventId(Number(event.target.value));
                  setReadiness(null);
                }}
              >
                <option value={0}>Provider default installs</option>
                {events.map((event) => (
                  <option key={event.id} value={event.id}>
                    {event.name} · {event.status} ·{" "}
                    {event.metadata.eligible === true
                      ? "eligible"
                      : event.metadata.eligible === false
                        ? "not eligible"
                        : "eligibility unknown"}
                  </option>
                ))}
              </select>
            </label>
            {events.find((event) => event.id === eventId)?.status ===
              "hidden" &&
              account.platform === "google" && (
                <button
                  className={button}
                  disabled={busy}
                  onClick={() =>
                    operation(async () => {
                      await callTool("conversion_event_enable", {
                        ...args,
                        mobile_app_resource_id: appId,
                        conversion_event_resource_id: eventId,
                      });
                      await check();
                    })
                  }
                >
                  Enable imported event
                </button>
              )}
            {readiness && (
              <div className="rounded bg-bg-input p-3 text-sm">
                <p>
                  {readiness.ready
                    ? "Provider setup is eligible for optimization."
                    : "Measurement needs verification."}
                </p>
                <dl className="mt-2 grid grid-cols-2 gap-1 text-xs">
                  <dt>Source configured</dt>
                  <dd>
                    {readiness.configured == null
                      ? "Unknown"
                      : readiness.configured
                        ? "Yes"
                        : "No"}
                  </dd>
                  <dt>App access</dt>
                  <dd>{readiness.app_access || "Unknown"}</dd>
                  <dt>Event observed</dt>
                  <dd>{readiness.event_observed || "Unknown"}</dd>
                  <dt>Optimization eligibility</dt>
                  <dd>
                    {readiness.eligible_for_optimization === true
                      ? "Eligible"
                      : readiness.eligible_for_optimization === false
                        ? "Not eligible"
                        : "Unknown"}
                  </dd>
                </dl>
                <p className="mt-2 text-text-muted">{readiness.next_action}</p>
                {readiness.diagnostics?.map(
                  (message: string, index: number) => (
                    <p key={index} className="mt-2 text-red">
                      {message}
                    </p>
                  ),
                )}
              </div>
            )}
          </div>
        )}
      </section>
      {selectedApp && sourceId > 0 && (
        <section className="rounded border border-border p-4">
          <h3 className="font-semibold">Create mobile campaign</h3>
          <p className="mt-1 text-sm text-text-muted">
            Build the campaign paused, inspect measurement, then activate when
            ready.
          </p>
          <fieldset
            disabled={busy || !!build.campaign}
            className="mt-4 grid gap-3 sm:grid-cols-2"
          >
            <label className="text-sm">
              Campaign name
              <input
                className={field}
                value={campaignName}
                onChange={(event) => setCampaignName(event.target.value)}
              />
            </label>
            <label className="text-sm">
              Goal
              <select
                className={field}
                value={goal}
                onChange={(event) => setGoal(event.target.value)}
              >
                {capabilities.goals.map((goal: string) => (
                  <option key={goal} value={goal}>
                    {eventLabels[goal] || goal}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-sm">
              Daily budget ({account.currency})
              <input
                className={field}
                type="number"
                min="0.01"
                step="0.01"
                value={budget}
                onChange={(event) => setBudget(event.target.value)}
              />
            </label>
            {account.platform === "google" || account.platform === "x" ? (
              <div>
                <label className="text-sm">
                  Location
                  <input
                    className={field}
                    value={locationQuery}
                    onChange={(event) => setLocationQuery(event.target.value)}
                  />
                </label>
                <button
                  className={`${button} mt-2`}
                  onClick={() =>
                    operation(async () => {
                      const result = await callTool(
                        "targeting_catalog_search",
                        {
                          ...args,
                          type: "location",
                          query: locationQuery,
                          ...(account.platform === "x"
                            ? { country_code: country || undefined }
                            : {}),
                        },
                      );
                      setLocations(result.data || []);
                    })
                  }
                >
                  Find locations
                </button>
                <select
                  aria-label="Campaign location"
                  className={`${field} mt-2`}
                  value={locationId}
                  onChange={(event) => setLocationId(event.target.value)}
                >
                  <option value="">Select location</option>
                  {locationId &&
                    !locations.some(
                      (item) => String(item.id) === locationId,
                    ) && (
                      <option value={locationId}>Location {locationId}</option>
                    )}
                  {locations.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name}
                    </option>
                  ))}
                </select>
              </div>
            ) : (
              <label className="text-sm">
                Target country
                <input
                  maxLength={2}
                  placeholder="US"
                  className={field}
                  value={country}
                  onChange={(event) =>
                    setCountry(event.target.value.toUpperCase())
                  }
                />
              </label>
            )}
            {account.platform === "x" && (
              <label className="text-sm">
                App store country
                <input
                  maxLength={2}
                  placeholder="US"
                  className={field}
                  value={country}
                  onChange={(event) =>
                    setCountry(event.target.value.toUpperCase())
                  }
                />
              </label>
            )}
            {account.platform === "meta" && (
              <>
                <label className="text-sm">
                  Beneficiary
                  <input
                    className={field}
                    value={beneficiary}
                    onChange={(event) => setBeneficiary(event.target.value)}
                  />
                </label>
                <label className="text-sm">
                  Payor
                  <input
                    className={field}
                    value={payor}
                    onChange={(event) => setPayor(event.target.value)}
                  />
                </label>
              </>
            )}
          </fieldset>
          {!build.campaign ? (
            <button
              className={`${button} mt-4`}
              disabled={busy}
              onClick={() => operation(createCampaign)}
            >
              Create paused campaign
            </button>
          ) : (
            <div className="mt-4 space-y-3">
              <p className="text-sm">
                Campaign {build.campaign}
                {build.group ? ` · Ad group ${build.group}` : ""}
                {build.ad ? ` · Ad ${build.ad}` : ""}
              </p>
              {!build.group ? (
                <button
                  className={button}
                  disabled={busy}
                  onClick={() => operation(createGroup)}
                >
                  Create paused ad group
                </button>
              ) : !build.ad ? (
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="text-sm">
                    Headline
                    <input
                      maxLength={account.platform === "google" ? 30 : 150}
                      className={field}
                      value={headline}
                      onChange={(event) => setHeadline(event.target.value)}
                    />
                  </label>
                  <label className="text-sm">
                    Description
                    <input
                      maxLength={account.platform === "google" ? 90 : 280}
                      className={field}
                      value={description}
                      onChange={(event) => setDescription(event.target.value)}
                    />
                  </label>
                  {account.platform === "x" ? (
                    <label className="text-sm">
                      Uploaded media keys
                      <input
                        className={field}
                        value={mediaKeys}
                        onChange={(event) => setMediaKeys(event.target.value)}
                      />
                    </label>
                  ) : (
                    <label className="text-sm">
                      {account.platform === "google"
                        ? "Image asset IDs (optional)"
                        : account.platform === "meta"
                          ? "Uploaded image hash"
                          : "Public image URL"}
                      <input
                        className={field}
                        value={asset}
                        onChange={(event) => setAsset(event.target.value)}
                      />
                    </label>
                  )}
                  <button
                    className={button}
                    disabled={busy}
                    onClick={() =>
                      operation(build.job ? pollCreative : createAd)
                    }
                  >
                    {build.job
                      ? "Check creative processing"
                      : account.platform === "google" || build.creative
                        ? "Create paused ad"
                        : "Create app creative"}
                  </button>
                </div>
              ) : (
                <div className="space-y-2">
                  <button
                    className={button}
                    disabled={busy}
                    onClick={() => operation(check)}
                  >
                    Recheck measurement
                  </button>
                  {readiness?.eligible_for_optimization === "unknown" && (
                    <label className="flex gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={acknowledge}
                        onChange={(event) =>
                          setAcknowledge(event.target.checked)
                        }
                      />
                      I verified mobile measurement in the provider or partner
                      console.
                    </label>
                  )}
                  <button
                    className={button}
                    disabled={busy || (!readiness?.ready && !acknowledge)}
                    onClick={() =>
                      operation(async () => {
                        setActivation(
                          await callTool("delivery_activate", {
                            ...args,
                            campaign_id: build.campaign,
                            adset_id: build.group,
                            ad_id: build.ad,
                            acknowledge_unverified_measurement: acknowledge,
                          }),
                        );
                      })
                    }
                  >
                    Activate campaign and ad
                  </button>
                  {activation && (
                    <p className="text-sm">
                      Delivery activation: {activation.status}
                    </p>
                  )}
                </div>
              )}
              <button
                className={button}
                disabled={busy}
                onClick={() => {
                  setBuild({ key: crypto.randomUUID() });
                  setActivation(null);
                  setReadiness(null);
                  setAcknowledge(false);
                }}
              >
                Start another campaign
              </button>
            </div>
          )}
        </section>
      )}
      <section className="rounded border border-border p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="font-semibold">Conversion results</h3>
          <select
            aria-label="Measurement report source"
            className="h-9 rounded border border-border bg-bg-input px-3 text-sm"
            value={reportSource}
            onChange={(event) => setReportSource(event.target.value)}
          >
            <option value="">All sources, shown separately</option>
            {[
              "provider",
              "app",
              "website",
              "firebase",
              "google_play",
              "mmp",
              "skadnetwork",
            ].map((source) => (
              <option key={source} value={source}>
                {sourceLabels[source] || source}
              </option>
            ))}
          </select>
        </div>
        <p className="mt-2 text-xs text-text-muted">
          Events and measurement sources can overlap. Their counts are shown
          separately. Missing metrics are unavailable.
        </p>
        <div className="mt-3 overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr>
                {[
                  "Event",
                  "Source",
                  "Attribution",
                  "Count",
                  "Cost / event",
                  "CPI",
                  "Revenue",
                  "ROAS",
                ].map((label) => (
                  <th key={label} className="p-2">
                    {label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {report?.events?.map((item: any) => (
                <tr
                  key={`${item.event_id}:${item.measurement_source}:${item.attribution_window}`}
                >
                  <td className="p-2" title={item.event_id}>
                    {eventLabels[item.event] || item.event}
                  </td>
                  <td className="p-2">{item.measurement_source}</td>
                  <td className="p-2">{item.attribution_window}</td>
                  <td className="p-2">
                    {metricDisplay(item.conversions, account.currency)}
                  </td>
                  <td className="p-2">{money(item.cost_per_event_micros)}</td>
                  <td className="p-2">{money(item.cpi_micros)}</td>
                  <td className="p-2">{money(item.value_micros)}</td>
                  <td className="p-2">
                    {metricDisplay(item.roas, account.currency)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {report?.sync?.status === "failed" && (
          <p className="mt-3 text-sm text-red">
            Event refresh failed: {report.sync.last_error}. Cached event data is
            shown.
          </p>
        )}
        {report && !report.events?.length && (
          <p className="mt-3 text-sm text-text-muted">
            No conversion events reported in this date range.
          </p>
        )}
        {!report && (
          <p className="mt-3 text-sm text-text-muted">Loading event report…</p>
        )}
        {report?.freshness?.fetched_at && (
          <p className="mt-2 text-xs text-text-muted">
            Updated {new Date(report.freshness.fetched_at).toLocaleString()} ·
            Provider attribution windows apply.
          </p>
        )}
      </section>
    </div>
  );
}
