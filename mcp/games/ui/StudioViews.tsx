import { useCallback, useEffect, useRef, useState } from "react";

type Item = Record<string, any>;
const input =
  "bg-bg-input border border-border rounded px-2 py-1 text-sm w-full";
const button =
  "border border-border rounded px-3 py-1 text-xs disabled:opacity-50 hover:bg-bg-input";
const card = "border border-border rounded p-3 space-y-2";
export async function studioRequest(
  project: string,
  game: string,
  action: string,
  data?: Item,
): Promise<any> {
  const base = game
    ? `/admin/games/${encodeURIComponent(game)}/studio`
    : "/admin/studio";
  const res = await fetch(
    `/api/apps/games${base}/${action}?project_id=${encodeURIComponent(project)}`,
    {
      credentials: "same-origin",
      method: data ? "POST" : "GET",
      headers: data ? { "Content-Type": "application/json" } : undefined,
      body: data ? JSON.stringify(data) : undefined,
    },
  );
  const out = await res.json();
  if (!res.ok) throw new Error(out.error || `HTTP ${res.status}`);
  return out.data;
}
function ErrorBox({ error }: { error: string }) {
  return error ? (
    <p role="alert" className="text-red">
      {error}
    </p>
  ) : null;
}
function Detail({ title, value }: { title: string; value: unknown }) {
  return (
    <details className={card}>
      <summary>{title}</summary>
      <pre className="text-xs whitespace-pre-wrap break-all max-h-80 overflow-auto">
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  );
}
function Field({
  label,
  value,
  onChange,
  placeholder = "",
}: {
  label: string;
  value: string;
  onChange: (s: string) => void;
  placeholder?: string;
}) {
  return (
    <label className="block text-xs space-y-1">
      <span>{label}</span>
      <input
        className={input}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </label>
  );
}

export function Portfolio({ projectId }: { projectId: string }) {
  const [data, setData] = useState<Item>();
  const [error, setError] = useState("");
  const [offset, setOffset] = useState(0);
  useEffect(() => {
    let active = true;
    studioRequest(projectId, "", "portfolio", { offset, limit: 25 })
      .then((v) => {
        if (active) {
          setData(v);
          setError("");
        }
      })
      .catch((e) => {
        if (active) setError(String(e.message));
      });
    return () => {
      active = false;
    };
  }, [projectId, offset]);
  if (!data && !error) return <p>Loading portfolio…</p>;
  return (
    <section className="space-y-2">
      <h3 className="font-medium">Delivery and reporting</h3>
      <ErrorBox error={error} />
      {data?.games?.map((entry: Item) => (
        <article className={card} key={entry.game.id}>
          <strong>{entry.game.name}</strong>
          <span className="ml-2 text-text-muted">{entry.game.status}</span>
          <p>
            {entry.sources.length} source links · {entry.targets.length} targets
            · {entry.metric_sources.length} reporting sources
          </p>
          {entry.targets.map((t: Item) => (
            <p key={t.id} className="text-xs">
              {t.platform} / {t.environment}:{" "}
              {t.summary?.release_status || "Release status not refreshed"}
              {t.summary?.refreshed_at
                ? ` · checked ${new Date(t.summary.refreshed_at).toLocaleString()}`
                : ""}
            </p>
          ))}
          {entry.deliveries?.[0] && (
            <p className="text-xs">
              Latest request: {entry.deliveries[0].action} —{" "}
              {entry.deliveries[0].status}
            </p>
          )}
          {entry.metric_sources.map((s: Item) => (
            <p key={s.id} className="text-xs">
              {s.provider}:{" "}
              {s.last_error ||
                (s.last_success
                  ? `updated ${new Date(s.last_success).toLocaleString()}`
                  : "Awaiting first import")}
            </p>
          ))}
        </article>
      ))}
      {data && (
        <div className="flex gap-2">
          <button
            className={button}
            disabled={!offset}
            onClick={() => setOffset(Math.max(0, offset - 25))}
          >
            Previous games
          </button>
          <button
            className={button}
            disabled={offset + 25 >= data.total}
            onClick={() => setOffset(offset + 25)}
          >
            Next games
          </button>
        </div>
      )}
    </section>
  );
}

export function StudioPanel({
  projectId,
  gameId,
  view,
}: {
  projectId: string;
  gameId: string;
  view: "source" | "releases" | "store" | "metrics";
}) {
  const call = useCallback(
    (action: string, data?: Item) =>
      studioRequest(projectId, gameId, action, data),
    [projectId, gameId],
  );
  const [sources, setSources] = useState<Item[]>([]);
  const [targets, setTargets] = useState<Item[]>([]);
  const [target, setTarget] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const generation = useRef(0);
  const reload = useCallback(async () => {
    const [s, t] = await Promise.all([call("sources"), call("targets")]);
    setSources(s);
    setTargets(t);
    setTarget((old) =>
      t.some((x: Item) => x.id === old) ? old : t[0]?.id || "",
    );
  }, [call]);
  useEffect(() => {
    const n = ++generation.current;
    call("sources")
      .then((s) => {
        if (generation.current === n) setSources(s);
      })
      .catch((e) => {
        if (generation.current === n) setError(e.message);
      });
    call("targets")
      .then((t) => {
        if (generation.current === n) {
          setTargets(t);
          setTarget(t[0]?.id || "");
        }
      })
      .catch((e) => {
        if (generation.current === n) setError(e.message);
      });
    return () => {
      generation.current++;
    };
  }, [call]);
  const run = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="p-4 space-y-4 h-full overflow-auto">
      <ErrorBox error={error} />
      {notice && <p role="status">{notice}</p>}
      {view === "source" && (
        <SourceView
          call={call}
          sources={sources}
          targets={targets}
          reload={reload}
          run={run}
          busy={busy}
        />
      )}
      {(view === "releases" || view === "store") && (
        <>
          <label className="block text-xs">
            Platform and environment
            <select
              aria-label="Platform and environment"
              className={input}
              value={target}
              onChange={(e) => setTarget(e.target.value)}
            >
              <option value="">Select a target</option>
              {targets.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} · {t.platform} · {t.environment}
                </option>
              ))}
            </select>
          </label>
          {!targets.length && (
            <p>Link a deployment in Source to manage releases.</p>
          )}
          {target &&
            (view === "releases" ? (
              <Releases
                key={target}
                call={call}
                target={target}
                run={run}
                busy={busy}
                notice={setNotice}
              />
            ) : (
              <StoreListing
                key={target}
                call={call}
                target={target}
                run={run}
                busy={busy}
                notice={setNotice}
              />
            ))}
        </>
      )}
      {view === "metrics" && (
        <Metrics call={call} run={run} busy={busy} notice={setNotice} />
      )}
    </section>
  );
}
type ActionProps = {
  call: (action: string, data?: Item) => Promise<any>;
  run: (fn: () => Promise<void>) => Promise<void>;
  busy: boolean;
};
function SourceView({
  call,
  sources,
  targets,
  reload,
  run,
  busy,
}: ActionProps & {
  sources: Item[];
  targets: Item[];
  reload: () => Promise<void>;
}) {
  const [repos, setRepos] = useState<Item[]>([]);
  const [deployments, setDeployments] = useState<Item[]>([]);
  const [slug, setSlug] = useState("");
  const [source, setSource] = useState("");
  const [deployment, setDeployment] = useState("");
  const [env, setEnv] = useState("production");
  const [platform, setPlatform] = useState("android");
  return (
    <>
      <h3 className="font-medium">Source and platform targets</h3>
      <p className="text-text-muted">
        Connect Code 0.10.0+ and Deploy 0.26.0+ in Games settings. Create
        repositories in Code and configure build commands, runners and
        publishing in Deploy.
      </p>
      <button
        className={button}
        disabled={busy}
        onClick={() =>
          run(async () => {
            const r = await call("discovery", { app: "code" });
            setRepos(r.repositories || []);
            const d = await call("discovery", { app: "deploy" });
            setDeployments(d.deployments || []);
          })
        }
      >
        Discover repositories and deployments
      </button>
      <div className={card}>
        <h4>Link a repository</h4>
        <select
          aria-label="Repository"
          className={input}
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
        >
          <option value="">Select repository</option>
          {repos.map((r) => (
            <option key={r.id} value={r.slug}>
              {r.name} ({r.slug})
            </option>
          ))}
        </select>
        <button
          className={button}
          disabled={busy || !slug}
          onClick={() =>
            run(async () => {
              await call("source_set", { repo_slug: slug });
              await reload();
            })
          }
        >
          Link source
        </button>
        {sources.map((s) => (
          <p key={s.id}>
            {s.name} · {s.repo_slug} · {s.role}
          </p>
        ))}
      </div>
      <div className={card}>
        <h4>Link a deployment</h4>
        <select
          aria-label="Linked source"
          className={input}
          value={source}
          onChange={(e) => setSource(e.target.value)}
        >
          <option value="">Select linked source</option>
          {sources.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <select
          aria-label="Deployment"
          className={input}
          value={deployment}
          onChange={(e) => setDeployment(e.target.value)}
        >
          <option value="">Select deployment</option>
          {deployments.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name} ({d.target_kind})
            </option>
          ))}
        </select>
        <Field label="Environment" value={env} onChange={setEnv} />
        <label className="block text-xs">
          Platform
          <select
            className={input}
            value={platform}
            onChange={(e) => setPlatform(e.target.value)}
          >
            {["android", "ios", "steam", "desktop"].map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </label>
        <button
          className={button}
          disabled={busy || !source || !deployment}
          onClick={() =>
            run(async () => {
              await call("target_set", {
                source_id: source,
                deployment_id: Number(deployment),
                environment: env,
                platform,
              });
              await reload();
            })
          }
        >
          Link target
        </button>
        {targets.map((t) => (
          <p key={t.id}>
            {t.name} · {t.platform} · {t.environment}
          </p>
        ))}
      </div>
    </>
  );
}
function Releases({
  call,
  target,
  run,
  busy,
  notice,
}: ActionProps & { target: string; notice: (s: string) => void }) {
  const [state, setState] = useState<Item>();
  const [plan, setPlan] = useState<Item>();
  const [history, setHistory] = useState<Item[]>([]);
  const [channel, setChannel] = useState("internal");
  const [build, setBuild] = useState("");
  const [release, setRelease] = useState("");
  const [offset, setOffset] = useState(0);
  const [reconcile, setReconcile] = useState("");
  const [remote, setRemote] = useState("");
  const [fraction, setFraction] = useState("0.1");
  const [logs, setLogs] = useState<Item>();
  const [reason, setReason] = useState("");
  const [localError, setLocalError] = useState("");
  const load = useCallback(async () => {
    const [s, h] = await Promise.all([
      call("release_status", { target_id: target }),
      call("history", { offset, limit: 25 }),
    ]);
    setState(s);
    setHistory(h);
  }, [call, target, offset]);
  useEffect(() => {
    let active = true;
    Promise.all([
      call("release_status", { target_id: target }),
      call("history", { offset, limit: 25 }),
    ])
      .then(([s, h]) => {
        if (active) {
          setState(s);
          setHistory(h);
          setLocalError("");
        }
      })
      .catch((e) => {
        if (active) setLocalError(e.message);
      });
    return () => {
      active = false;
    };
  }, [call, target, offset]);
  const dispatch = (action: string) =>
    run(async () => {
      const receipt = await call(action, {
        target_id: target,
        request_key: crypto.randomUUID(),
        build_id: Number(build),
        release_id: Number(release),
        channel,
        ...(action === "rollout" ? { fraction: Number(fraction) } : {}),
      });
      notice(
        receipt.status === "unknown"
          ? "Outcome uncertain. Reconcile this request before retrying."
          : "Request recorded. Refresh to follow progress.",
      );
      await load();
    });
  return (
    <>
      <ErrorBox error={localError} />
      <div className="flex gap-2 flex-wrap">
        <button className={button} disabled={busy} onClick={() => run(load)}>
          Refresh release status
        </button>
        <button
          className={button}
          disabled={busy}
          onClick={() =>
            run(async () =>
              setPlan(await call("release_plan", { target_id: target })),
            )
          }
        >
          Check readiness
        </button>
        <button
          className={button}
          disabled={busy}
          onClick={() => dispatch("build")}
        >
          Build game
        </button>
      </div>
      {plan && <Detail title="Readiness and configuration" value={plan} />}
      <div className={card}>
        <h4>Publish a tested build</h4>
        <Field label="Release channel" value={channel} onChange={setChannel} />
        <select
          aria-label="Build"
          className={input}
          value={build}
          onChange={(e) => setBuild(e.target.value)}
        >
          <option value="">Select build</option>
          {state?.builds?.map((b: Item) => (
            <option key={b.id} value={b.id}>
              #{b.id} · {b.status}
            </option>
          ))}
        </select>
        <button
          className={button}
          disabled={busy || !build || !channel}
          onClick={() => dispatch("release")}
        >
          Release selected build
        </button>
        <select
          aria-label="Release"
          className={input}
          value={release}
          onChange={(e) => setRelease(e.target.value)}
        >
          <option value="">Select release</option>
          {state?.releases?.map((r: Item) => (
            <option key={r.id} value={r.id}>
              #{r.id} · {r.status}
            </option>
          ))}
        </select>
        <div className="flex gap-2">
          <button
            className={button}
            disabled={busy || !release || !channel}
            onClick={() => dispatch("promote")}
          >
            Promote selected release
          </button>
          <button
            className={button}
            disabled={busy || !release}
            onClick={() =>
              run(async () => {
                await call("release_sync", {
                  target_id: target,
                  release_id: Number(release),
                });
                await load();
              })
            }
          >
            Sync provider status
          </button>
        </div>
        <p className="text-xs text-text-muted">
          Deploy enforces the selected target’s policy. Complete any required
          approval through the authorized Deploy approval action.
        </p>
      </div>
      {state && (
        <>
          <Detail
            title="Build artifacts and test evidence"
            value={state.builds}
          />
          <Detail title="Publication and availability" value={state.releases} />
          <div className="flex gap-2">
            <button
              className={button}
              disabled={busy || !build}
              onClick={() =>
                run(async () =>
                  setLogs(
                    await call("logs", {
                      target_id: target,
                      build_id: Number(build),
                    }),
                  ),
                )
              }
            >
              Read selected build logs
            </button>
            <button
              className={button}
              disabled={busy || !release}
              onClick={() =>
                run(async () =>
                  setLogs(
                    await call("logs", {
                      target_id: target,
                      release_id: Number(release),
                    }),
                  ),
                )
              }
            >
              Read selected release logs
            </button>
          </div>
          {logs && <Detail title="Recent logs" value={logs.log} />}
          {["android", "ios"].includes(state.deployment?.target_kind) && (
            <details className={card}>
              <summary>Rollout controls</summary>
              <p className="text-xs">
                These apply to the selected release. Halting Android or expiring
                TestFlight does not undo installations.
              </p>
              {state.deployment?.target_kind === "android" && (
                <>
                  <Field
                    label="Rollout fraction (0–1)"
                    value={fraction}
                    onChange={setFraction}
                  />
                  <button
                    className={button}
                    disabled={
                      busy ||
                      !release ||
                      !(Number(fraction) > 0 && Number(fraction) <= 1)
                    }
                    onClick={() => dispatch("rollout")}
                  >
                    Set Android rollout fraction
                  </button>
                </>
              )}
              <button
                className={button}
                disabled={busy || !release}
                onClick={() => dispatch("halt")}
              >
                {state.deployment?.target_kind === "ios"
                  ? "Expire selected TestFlight build"
                  : "Halt selected Android rollout"}
              </button>
            </details>
          )}
        </>
      )}
      <div className={card}>
        <h4>Delivery requests</h4>
        {history.map((h) => (
          <div key={h.request_key} className="border-b border-border py-2">
            <p>
              {h.action} · {h.status} · {h.created_at}
            </p>
            {h.error && <p className="text-red">{h.error}</p>}
            <code className="text-xs">{h.request_key}</code>
            {["unknown", "dispatching"].includes(h.status) &&
              h.target_id === target && (
                <button
                  className={button}
                  onClick={() => {
                    setReconcile(h.request_key);
                    setRemote("");
                  }}
                >
                  Reconcile request
                </button>
              )}
          </div>
        ))}
        <div className="flex gap-2">
          <button
            className={button}
            disabled={busy || offset === 0}
            onClick={() => setOffset(Math.max(0, offset - 25))}
          >
            Previous requests
          </button>
          <button
            className={button}
            disabled={busy || history.length < 25}
            onClick={() => setOffset(offset + 25)}
          >
            Next requests
          </button>
        </div>
        {reconcile && (
          <div className={card}>
            <p>
              Verify the exact build or release created by this request in
              Deploy.
            </p>
            <Field
              label="Verified build or release ID"
              value={remote}
              onChange={setRemote}
            />
            <button
              className={button}
              disabled={busy || !remote}
              onClick={() =>
                run(async () => {
                  await call("reconcile", {
                    request_key: reconcile,
                    build_id: Number(remote),
                    release_id: Number(remote),
                    confirm: true,
                  });
                  setReconcile("");
                  await load();
                })
              }
            >
              Confirm verified result
            </button>
            <Field
              label="Verification notes if no operation was created"
              value={reason}
              onChange={setReason}
            />
            <button
              className={button}
              disabled={busy || reason.trim().length < 10}
              onClick={() =>
                run(async () => {
                  await call("reconcile", {
                    request_key: reconcile,
                    resolution: "not_created",
                    reason,
                    confirm: true,
                  });
                  setReconcile("");
                  await load();
                })
              }
            >
              Confirm no remote operation was created
            </button>
          </div>
        )}
      </div>
    </>
  );
}
function StoreListing({
  call,
  target,
  run,
  busy,
  notice,
}: ActionProps & { target: string; notice: (s: string) => void }) {
  const [document, setDocument] = useState("");
  const [state, setState] = useState<Item>();
  return (
    <>
      <h3 className="font-medium">Store listing</h3>
      <p>
        Store copy is managed separately from the game’s internal title and
        description. Updates here change Deploy’s saved listing; applying it to
        a store remains a Deploy action.
      </p>
      <button
        className={button}
        disabled={busy}
        onClick={() =>
          run(async () => {
            const v = await call("store_get", { target_id: target });
            setState(v);
            const raw =
              v.config?.desired_json ||
              v.store_config?.desired_json ||
              v.desired_json;
            setDocument(
              typeof raw === "string"
                ? raw
                : JSON.stringify(v.desired || {}, null, 2),
            );
          })
        }
      >
        Load store listing
      </button>
      {state && <Detail title="Store state" value={state} />}
      <label className="block">
        Store document (advanced)
        <textarea
          aria-label="Store document"
          className={`${input} font-mono h-72`}
          value={document}
          onChange={(e) => setDocument(e.target.value)}
        />
      </label>
      <button
        className={button}
        disabled={busy || !document}
        onClick={() =>
          run(async () => {
            JSON.parse(document);
            await call("store_update", {
              target_id: target,
              store_config_json: document,
            });
            notice("Store draft saved in Deploy.");
          })
        }
      >
        Save store draft
      </button>
    </>
  );
}
export function formatMicros(raw: string): string {
  try {
    const n = BigInt(raw);
    const sign = n < 0n ? "-" : "";
    const a = n < 0n ? -n : n;
    return `${sign}${a / 1000000n}.${(a % 1000000n).toString().padStart(6, "0")}`;
  } catch {
    return "Unavailable";
  }
}
function Metrics({
  call,
  run,
  busy,
  notice,
}: ActionProps & { notice: (s: string) => void }) {
  const [sources, setSources] = useState<Item[]>([]);
  const [connections, setConnections] = useState<Item[]>([]);
  const [connection, setConnection] = useState("");
  const [external, setExternal] = useState("");
  const [account, setAccount] = useState("");
  const [stream, setStream] = useState("");
  const [zone, setZone] = useState("UTC");
  const [vendor, setVendor] = useState("");
  const [family, setFamily] = useState("network");
  const [source, setSource] = useState("");
  const [events, setEvents] = useState<Item[]>([]);
  const [inventory, setInventory] = useState<any>();
  const [topic, setTopic] = useState("provider_daily");
  const [error, setError] = useState("");
  const selected = connections.find(
    (c) => String(c.connection_id) === connection,
  );
  const load = async () => setSources(await call("metric_sources"));
  useEffect(() => {
    let active = true;
    call("metric_sources")
      .then((v) => {
        if (active) setSources(v);
      })
      .catch((e) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [call]);
  return (
    <>
      <ErrorBox error={error} />
      <h3 className="font-medium">Game metrics</h3>
      <p className="text-text-muted">
        Gameplay, publisher earnings and store proceeds retain their own
        definitions. Reports import every six hours with a seven-day correction
        window. AdMob is optional.
      </p>
      <div className={card}>
        <h4>Reporting sources</h4>
        <button
          className={button}
          disabled={busy}
          onClick={() =>
            run(async () => setConnections(await call("discovery", {})))
          }
        >
          Discover reporting connections
        </button>
        <select
          aria-label="Reporting connection"
          className={input}
          value={connection}
          onChange={(e) => setConnection(e.target.value)}
        >
          <option value="">Select connection</option>
          {connections.map((c) => (
            <option key={c.connection_id} value={c.connection_id}>
              {c.provider} · #{c.connection_id}
            </option>
          ))}
        </select>
        {selected && (
          <>
            <Field
              label="External app or GA4 property ID"
              value={external}
              onChange={setExternal}
            />
            {selected.provider === "admob" && (
              <>
                <Field
                  label="Publisher account ID"
                  value={account}
                  onChange={setAccount}
                  placeholder="pub-…"
                />
                <select
                  aria-label="AdMob report family"
                  className={input}
                  value={family}
                  onChange={(e) => setFamily(e.target.value)}
                >
                  <option value="network">AdMob Network</option>
                  <option value="mediation">Mediation</option>
                </select>
              </>
            )}
            {selected.provider === "google-analytics" && (
              <>
                <Field
                  label="GA4 stream ID"
                  value={stream}
                  onChange={setStream}
                />
                <Field
                  label="Reporting timezone"
                  value={zone}
                  onChange={setZone}
                />
              </>
            )}
            {selected.provider === "app-store-connect" && (
              <Field
                label="Apple vendor number"
                value={vendor}
                onChange={setVendor}
              />
            )}
            <button
              className={button}
              disabled={busy}
              onClick={() =>
                run(async () =>
                  setInventory(
                    await call("discovery", {
                      provider: selected.provider,
                      connection_id: Number(connection),
                      account_id: account,
                    }),
                  ),
                )
              }
            >
              View provider inventory
            </button>
            <button
              className={button}
              disabled={busy || !external}
              onClick={() =>
                run(async () => {
                  await call("metric_source_set", {
                    provider: selected.provider,
                    connection_id: Number(connection),
                    external_id: external,
                    account_id: account,
                    stream_id: stream,
                    timezone: zone,
                    family,
                    config: { vendor_number: vendor },
                  });
                  await load();
                  notice("Reporting source connected.");
                })
              }
            >
              Connect reporting source
            </button>
          </>
        )}
        {inventory && <Detail title="Provider inventory" value={inventory} />}
        {sources.map((s) => (
          <article key={s.id} className={card}>
            <strong>
              {s.provider} · {s.family}
            </strong>
            <p>{s.external_id}</p>
            <p>
              {s.last_success
                ? `Updated ${new Date(s.last_success).toLocaleString()}`
                : "No imported data yet"}
            </p>
            {s.last_error && <p className="text-red">{s.last_error}</p>}
            <button
              className={button}
              disabled={busy}
              onClick={() =>
                run(async () => {
                  await call("metrics_sync", { source_id: s.id });
                  await load();
                  notice("Seven-day report window refreshed.");
                })
              }
            >
              Refresh reports
            </button>
          </article>
        ))}
      </div>
      <div className={card}>
        <h4>Measurements</h4>
        <select
          aria-label="Metric source"
          className={input}
          value={source}
          onChange={(e) => setSource(e.target.value)}
        >
          <option value="">All sources (shown separately)</option>
          {sources.map((s) => (
            <option key={s.id} value={s.id}>
              {s.provider} · {s.family} · {s.external_id}
            </option>
          ))}
        </select>
        <select
          aria-label="Measurement type"
          className={input}
          value={topic}
          onChange={(e) => setTopic(e.target.value)}
        >
          {[
            "provider_daily",
            "play.session_started",
            "play.run_completed",
            "play.run_failed",
            "play.performance_summary",
          ].map((t) => (
            <option key={t}>{t}</option>
          ))}
        </select>
        <button
          className={button}
          disabled={busy}
          onClick={() =>
            run(async () => {
              const v = await call("metrics_query", {
                source_id: source,
                topic,
                limit: 100,
              });
              setEvents(v.events || []);
            })
          }
        >
          Load measurements
        </button>
        {!events.length && (
          <p>No measurements loaded. Missing reports are not zero.</p>
        )}
        {events.map((e: Item) => {
          const p = typeof e.props === "string" ? JSON.parse(e.props) : e.props;
          return (
            <article className={card} key={e.id}>
              <p>
                {p?.date || p?.event_time} · {p?.provider || "Gameplay"} ·{" "}
                {p?.family || e.topic}
              </p>
              {p?.facts?.map((f: Item, i: number) => (
                <dl key={i} className="grid grid-cols-2 gap-1 text-xs">
                  {Object.entries(f).map(([k, v]) => (
                    <div key={k}>
                      <dt>{k.replace(/_micros$/, "").replaceAll("_", " ")}</dt>
                      <dd>
                        {k.endsWith("_micros")
                          ? `${formatMicros(String(v))} ${f.currency || ""}`
                          : String(v)}
                      </dd>
                    </div>
                  ))}
                </dl>
              ))}
              {!p?.facts && <Detail title="Event details" value={p} />}
            </article>
          );
        })}
        <p className="text-xs text-text-muted">
          Showing up to 100 recent records. Daily active users must not be
          summed into monthly unique users.
        </p>
      </div>
    </>
  );
}
