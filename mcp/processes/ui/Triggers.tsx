import { useEffect, useState } from "react";
import type { Parameter, Assignment } from "./Assignments";
type Filter = { path: string; op: string; value?: unknown };
type Config = {
  name: string;
  source_install_id: number;
  topic: string;
  filters: Filter[];
  mappings: Record<string, string>;
};
type Trigger = {
  id: string;
  revision: number;
  status: string;
  config: Config;
  sync_pending: boolean;
  sync_error: string;
  subscription_enabled: boolean;
};
type Source = {
  install_id: number;
  app: string;
  name: string;
  events: {
    name: string;
    description?: string;
    payload?: Record<string, string>;
  }[];
};
type API = (path: string, method?: string, body?: unknown) => Promise<any>;
export default function Triggers({
  assignment,
  parameters,
  api,
  processStatus,
}: {
  assignment: Assignment;
  parameters: Parameter[];
  api: API;
  processStatus: string;
}) {
  const [open, setOpen] = useState(false),
    [items, setItems] = useState<Trigger[]>([]),
    [sources, setSources] = useState<Source[]>([]),
    [editing, setEditing] = useState<Trigger | null>(null),
    [config, setConfig] = useState<Config | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [sample, setSample] = useState('{"data": {}}'),
    [testKey, setTestKey] = useState(""),
    [preview, setPreview] = useState<any>(null),
    [history, setHistory] = useState<any[] | null>(null),
    [run, setRun] = useState<any>(null);
  const base = `/assignments/${assignment.id}/triggers`;
  const load = async () => {
    const r = await api(base);
    setItems(r.triggers || []);
  };
  useEffect(() => {
    if (!open) return;
    let active = true;
    Promise.all([api(base), api("/trigger-sources")])
      .then(([r, s]) => {
        if (active) {
          setItems(r.triggers || []);
          setSources(s.sources || []);
        }
      })
      .catch((e) => {
        if (active) setError(String(e.message || e));
      });
    return () => {
      active = false;
    };
  }, [open, assignment.id, assignment.status]);
  const work = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const edit = (t?: Trigger) => {
    setEditing(t || null);
    setTestKey("");
    setConfig(
      t
        ? structuredClone(t.config)
        : {
            name: "",
            source_install_id: sources[0]?.install_id || 0,
            topic: "",
            filters: [],
            mappings: {},
          },
    );
    setPreview(null);
    setHistory(null);
    setRun(null);
  };
  const update = (patch: Partial<Config>) => {
    setConfig((c) => (c ? { ...c, ...patch } : c));
    setPreview(null);
  };
  const source = sources.find(
    (s) => s.install_id === config?.source_install_id,
  );
  const topic = source?.events?.find((e) => e.name === config?.topic);
  const fieldOptions = Object.keys(topic?.payload || {}).map(
    (k) => `data.${k}`,
  );
  const sampleEvent = (t: Trigger) => {
    const e = JSON.parse(sample);
    return { ...e, topic: e.topic || t.config.topic };
  };
  return (
    <section style={{ marginTop: 16 }}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? "Hide event triggers" : "Event triggers"}
      </button>
      {open && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="row between">
            <h2>Event triggers</h2>
            <button
              disabled={
                busy ||
                assignment.status === "archived" ||
                processStatus === "archived"
              }
              onClick={() => edit()}
            >
              Add app event
            </button>
          </div>
          <p className="small muted">
            An app event starts this assignment with mapped parameters. Its
            agents receive work when their steps are ready. Schedules remain in
            the assignment settings.
          </p>
          {error && (
            <div role="alert" className="notice">
              {error}
            </div>
          )}
          {!config && !items.length && (
            <p className="muted">No event triggers yet.</p>
          )}
          {!config &&
            items.map((t) => (
              <div className="card" key={t.id} style={{ marginTop: 12 }}>
                <div className="row between">
                  <strong>{t.config.name}</strong>
                  <span className="pill">{t.status}</span>
                </div>
                <p>
                  {sources.find(
                    (s) => s.install_id === t.config.source_install_id,
                  )?.name || `App ${t.config.source_install_id}`}{" "}
                  · {t.config.topic}
                </p>
                <p className="small muted">
                  {t.sync_pending
                    ? "Applying listener change…"
                    : t.subscription_enabled
                      ? "Listening for new events"
                      : "Listener paused"}
                  {t.sync_error && ` · ${t.sync_error}`}
                </p>
                <div className="row">
                  <button
                    disabled={
                      busy ||
                      assignment.status === "archived" ||
                      processStatus === "archived" ||
                      (t.status !== "active" &&
                        (assignment.status !== "active" ||
                          processStatus !== "active"))
                    }
                    onClick={() =>
                      work(async () => {
                        await api(
                          `/triggers/${t.id}/${t.status === "active" ? "pause" : "activate"}`,
                          "POST",
                          { expected_revision: t.revision },
                        );
                      })
                    }
                  >
                    {t.status === "active" ? "Pause" : "Activate"}
                  </button>
                  <button
                    disabled={busy || t.status !== "paused"}
                    onClick={() => edit(t)}
                  >
                    Edit
                  </button>
                  <button
                    disabled={busy}
                    onClick={() => {
                      setEditing(t);
                      setTestKey("");
                      setPreview(null);
                      setHistory(null);
                      setRun(null);
                    }}
                  >
                    Test trigger
                  </button>
                  <button
                    disabled={busy}
                    onClick={() =>
                      work(async () => {
                        const r = await api(`/triggers/${t.id}/events`);
                        setEditing(null);
                        setHistory(r.events);
                        setRun(null);
                      })
                    }
                  >
                    Event history
                  </button>
                </div>
              </div>
            ))}
          {config && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                work(async () => {
                  await api(
                    editing ? `/triggers/${editing.id}` : base,
                    editing ? "PUT" : "POST",
                    {
                      assignment_id: assignment.id,
                      expected_revision: editing?.revision,
                      trigger: config,
                    },
                  );
                  setConfig(null);
                  setEditing(null);
                });
              }}
              style={{ marginTop: 16 }}
            >
              <div className="field">
                <label>
                  Trigger name
                  <input
                    required
                    maxLength={160}
                    value={config.name}
                    onChange={(e) => update({ name: e.target.value })}
                  />
                </label>
              </div>
              <div className="field">
                <label>
                  Source app
                  <select
                    required
                    value={config.source_install_id || ""}
                    onChange={(e) =>
                      update({
                        source_install_id: Number(e.target.value),
                        topic: "",
                      })
                    }
                  >
                    <option value="">Select an installed app</option>
                    {sources.map((s) => (
                      <option key={s.install_id} value={s.install_id}>
                        {s.name || s.app} · #{s.install_id}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <div className="field">
                <label>
                  Event
                  <input
                    required
                    list={`topics-${assignment.id}`}
                    value={config.topic}
                    placeholder="customer.signed_up"
                    onChange={(e) => update({ topic: e.target.value })}
                  />
                </label>
                <datalist id={`topics-${assignment.id}`}>
                  {source?.events?.map((e) => (
                    <option key={e.name} value={e.name}>
                      {e.description}
                    </option>
                  ))}
                </datalist>
                {topic?.description && (
                  <p className="small muted">{topic.description}</p>
                )}
              </div>
              <datalist id={`fields-${assignment.id}`}>
                {fieldOptions.map((p) => (
                  <option key={p} value={p} />
                ))}
              </datalist>
              <div className="row between">
                <h2>Conditions</h2>
                <button
                  type="button"
                  onClick={() =>
                    update({
                      filters: [
                        ...(config.filters || []),
                        { path: "data.", op: "eq", value: "" },
                      ],
                    })
                  }
                >
                  Add condition
                </button>
              </div>
              <p className="small muted">All conditions must match.</p>
              {(config.filters || []).map((f, i) => (
                <div className="row" key={i} style={{ marginBottom: 10 }}>
                  <label style={{ flex: 1 }}>
                    Event field
                    <input
                      required
                      list={`fields-${assignment.id}`}
                      value={f.path}
                      onChange={(e) =>
                        update({
                          filters: config.filters.map((x, j) =>
                            j === i ? { ...x, path: e.target.value } : x,
                          ),
                        })
                      }
                    />
                  </label>
                  <label>
                    Condition
                    <select
                      value={f.op}
                      onChange={(e) =>
                        update({
                          filters: config.filters.map((x, j) =>
                            j === i
                              ? {
                                  ...x,
                                  op: e.target.value,
                                  value:
                                    e.target.value === "exists"
                                      ? true
                                      : e.target.value === "contains"
                                        ? String(x.value ?? "")
                                        : ["gt", "gte", "lt", "lte"].includes(
                                              e.target.value,
                                            )
                                          ? Number(x.value) || 0
                                          : x.value,
                                }
                              : x,
                          ),
                        })
                      }
                    >
                      {Object.entries({
                        eq: "equals",
                        neq: "does not equal",
                        contains: "contains",
                        exists: "exists",
                        gt: "greater than",
                        gte: "at least",
                        lt: "less than",
                        lte: "at most",
                      }).map(([op, label]) => (
                        <option key={op} value={op}>
                          {label}
                        </option>
                      ))}
                    </select>
                  </label>
                  {["eq", "neq"].includes(f.op) && (
                    <label>
                      Value type
                      <select
                        value={typeof f.value}
                        onChange={(e) =>
                          update({
                            filters: config.filters.map((x, j) =>
                              j === i
                                ? {
                                    ...x,
                                    value:
                                      e.target.value === "number"
                                        ? Number(x.value) || 0
                                        : e.target.value === "boolean"
                                          ? x.value === true ||
                                            x.value === "true"
                                          : String(x.value ?? ""),
                                  }
                                : x,
                            ),
                          })
                        }
                      >
                        <option value="string">Text</option>
                        <option value="number">Number</option>
                        <option value="boolean">Boolean</option>
                      </select>
                    </label>
                  )}
                  {typeof f.value === "boolean" && f.op !== "contains" ? (
                    <label>
                      {f.op === "exists" ? "Present" : "Value"}
                      <select
                        value={String(f.value)}
                        onChange={(e) =>
                          update({
                            filters: config.filters.map((x, j) =>
                              j === i
                                ? { ...x, value: e.target.value === "true" }
                                : x,
                            ),
                          })
                        }
                      >
                        <option value="true">
                          {f.op === "exists" ? "Yes" : "True"}
                        </option>
                        <option value="false">
                          {f.op === "exists" ? "No" : "False"}
                        </option>
                      </select>
                    </label>
                  ) : (
                    f.op !== "exists" && (
                      <label style={{ flex: 1 }}>
                        Value
                        <input
                          type={typeof f.value === "number" ? "number" : "text"}
                          step="any"
                          required
                          value={
                            typeof f.value === "string"
                              ? f.value
                              : JSON.stringify(f.value)
                          }
                          onChange={(e) => {
                            let v: unknown = e.target.value;
                            if (typeof f.value === "number")
                              v = Number(e.target.value);
                            update({
                              filters: config.filters.map((x, j) =>
                                j === i ? { ...x, value: v } : x,
                              ),
                            });
                          }}
                        />
                      </label>
                    )
                  )}
                  <button
                    type="button"
                    onClick={() =>
                      update({
                        filters: config.filters.filter((_, j) => j !== i),
                      })
                    }
                  >
                    Remove
                  </button>
                </div>
              ))}
              <h2>Parameter mapping</h2>
              <p className="small muted">
                Choose an event field for each value that changes per run. Blank
                fields use assignment values.
              </p>
              {parameters.map((p) => (
                <div className="field" key={p.key}>
                  <label>
                    {p.label || p.key}
                    {p.required ? " *" : ""}
                    <input
                      list={`fields-${assignment.id}`}
                      placeholder={`data.${p.key}`}
                      value={config.mappings?.[p.key] || ""}
                      onChange={(e) => {
                        const m = { ...config.mappings };
                        if (e.target.value) m[p.key] = e.target.value;
                        else delete m[p.key];
                        update({ mappings: m });
                      }}
                    />
                  </label>
                </div>
              ))}
              <div className="row">
                <button className="primary" disabled={busy}>
                  Save paused trigger
                </button>
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => {
                    setConfig(null);
                    setEditing(null);
                  }}
                >
                  Cancel
                </button>
              </div>
            </form>
          )}
          {editing && !config && (
            <div className="card" style={{ marginTop: 16 }}>
              <h2>Test {editing.config.name}</h2>
              <p className="small muted">
                Preview checks conditions and parameters. Starting a test run
                sends real work to the assigned agents.
              </p>
              <label>
                Sample event JSON
                <textarea
                  rows={6}
                  value={sample}
                  onChange={(e) => {
                    setSample(e.target.value);
                    setTestKey("");
                    setPreview(null);
                  }}
                />
              </label>
              <div className="row" style={{ marginTop: 12 }}>
                <button
                  disabled={busy}
                  onClick={() =>
                    work(async () =>
                      setPreview(
                        await api(`/triggers/${editing.id}/preview`, "POST", {
                          event: sampleEvent(editing),
                        }),
                      ),
                    )
                  }
                >
                  Preview only
                </button>
                <button
                  disabled={
                    busy ||
                    !preview?.matched ||
                    assignment.status !== "active" ||
                    processStatus !== "active"
                  }
                  onClick={() =>
                    work(async () => {
                      const key = testKey || crypto.randomUUID();
                      setTestKey(key);
                      const r = await api(
                        `/triggers/${editing.id}/test_run`,
                        "POST",
                        { event: sampleEvent(editing), idempotency_key: key },
                      );
                      setPreview(r);
                    })
                  }
                >
                  Start test run
                </button>
                <button disabled={busy} onClick={() => setEditing(null)}>
                  Close
                </button>
              </div>
              {preview && (
                <pre className="prose">{JSON.stringify(preview, null, 2)}</pre>
              )}
            </div>
          )}
          {history && (
            <div style={{ marginTop: 16 }}>
              <h2>Recent events</h2>
              <button
                onClick={() =>
                  work(async () => {
                    const r = await api(base);
                    setItems(r.triggers || []);
                    const refreshed = await Promise.all(
                      items.map((t) => api(`/triggers/${t.id}/events`)),
                    );
                    setHistory(
                      refreshed
                        .flatMap((r) => r.events)
                        .sort((a, b) =>
                          b.created_at.localeCompare(a.created_at),
                        )
                        .slice(0, 100),
                    );
                  })
                }
              >
                Refresh history
              </button>
              {!history.length && <p>No events received.</p>}
              {history.map((h) => (
                <details key={h.id} className="card" style={{ marginTop: 8 }}>
                  <summary>
                    {h.status} · {new Date(h.created_at).toLocaleString()} ·{" "}
                    {h.event_id}
                  </summary>
                  {h.reason && <p>{h.reason}</p>}
                  {h.status === "failed" && (
                    <button
                      disabled={
                        busy ||
                        assignment.status !== "active" ||
                        processStatus !== "active" ||
                        items.find((t) => t.id === h.trigger.id)?.status !==
                          "active"
                      }
                      onClick={() =>
                        work(async () => {
                          const current = items.find(
                            (t) => t.id === h.trigger.id,
                          )!;
                          await api(
                            `/triggers/${current.id}/event_retry`,
                            "POST",
                            {
                              event_record_id: h.id,
                              idempotency_key: `revision-${current.revision}`,
                            },
                          );
                          const r = await api(`/triggers/${current.id}/events`);
                          setHistory(r.events);
                        })
                      }
                    >
                      Retry with current rules
                    </button>
                  )}
                  {h.run_id && (
                    <button
                      onClick={() =>
                        work(async () => setRun(await api(`/runs/${h.run_id}`)))
                      }
                    >
                      Inspect run
                    </button>
                  )}
                  <pre className="prose">
                    {JSON.stringify(
                      {
                        parameters: h.parameters,
                        event: h.event,
                        trigger: h.trigger.config,
                      },
                      null,
                      2,
                    )}
                  </pre>
                </details>
              ))}
            </div>
          )}
          {run && (
            <div className="card" style={{ marginTop: 16 }}>
              <h2>Triggered run · {run.run?.state}</h2>
              <pre className="prose">{JSON.stringify(run, null, 2)}</pre>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
