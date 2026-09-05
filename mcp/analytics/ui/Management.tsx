import { useCallback, useEffect, useState } from "react";
import { scopedAppURL } from "./dashboard-ui";
const input = "border border-border bg-bg-input rounded px-2 py-1 text-sm";
const button =
  "border border-border rounded px-3 py-1 text-sm hover:text-accent";
export async function analyticsRequest(
  project: string,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const r = await fetch(scopedAppURL(`/api/apps/analytics${path}`, project), {
    method,
    credentials: "same-origin",
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok)
    throw new Error((await r.text()).trim() || `Request failed (${r.status})`);
  return r.json();
}
function Field({
  name,
  value,
  set,
  type = "text",
}: {
  name: string;
  value: any;
  set: (v: string) => void;
  type?: string;
}) {
  return (
    <label className="flex flex-col gap-1 text-xs text-text-dim">
      {name}
      <input
        aria-label={name}
        className={input}
        type={type}
        step={type === "number" ? "any" : undefined}
        value={value ?? ""}
        onChange={(e) => set(e.target.value)}
      />
    </label>
  );
}
export function MetricFields({
  config,
  set,
}: {
  config: Record<string, any>;
  set: (cfg: Record<string, any>) => void;
}) {
  const update = (k: string, v: unknown) => set({ ...config, [k]: v });
  const agg = String(config.aggregation || "count");
  return (
    <div
      className="grid gap-2"
      style={{ gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))" }}
    >
      <Field name="App" value={config.app} set={(v) => update("app", v)} />
      <Field
        name="Event topic"
        value={config.topic}
        set={(v) => update("topic", v)}
      />
      <label className="flex flex-col gap-1 text-xs">
        Aggregation
        <select
          className={input}
          value={agg}
          onChange={(e) =>
            set({
              ...config,
              aggregation: e.target.value,
              ...(e.target.value === "sum_money"
                ? {
                    amount_unit: config.amount_unit || "minor",
                    currency_field: config.currency_field || "props.currency",
                    reporting_currency: config.reporting_currency || "EUR",
                    value: config.value || "props.amount_cents",
                  }
                : {}),
            })
          }
        >
          {[
            "count",
            "distinct",
            "sum",
            "sum_money",
            "average",
            "min",
            "max",
            "latest",
            "change",
          ].map((v) => (
            <option key={v}>{v}</option>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1 text-xs">
        Source
        <select
          className={input}
          value={config.source || ""}
          onChange={(e) => update("source", e.target.value)}
        >
          {["", "track", "bus", "web", "rollup", "auto"].map((v) => (
            <option key={v} value={v}>
              {v || "All sources"}
            </option>
          ))}
        </select>
      </label>
      {!["count", "distinct"].includes(agg) && (
        <Field
          name="Value field"
          value={config.value}
          set={(v) => update("value", v)}
        />
      )}
      {agg === "distinct" && (
        <Field
          name="Distinct field"
          value={config.by}
          set={(v) => update("by", v)}
        />
      )}
      {agg === "sum_money" && (
        <>
          <Field
            name="Currency field"
            value={config.currency_field}
            set={(v) => update("currency_field", v)}
          />
          <Field
            name="Reporting currency"
            value={config.reporting_currency}
            set={(v) => update("reporting_currency", v.toUpperCase())}
          />
          <label className="flex flex-col gap-1 text-xs">
            Input amount unit
            <select
              className={input}
              value={config.amount_unit || "minor"}
              onChange={(e) => update("amount_unit", e.target.value)}
            >
              <option value="minor">Minor (cents)</option>
              <option value="major">Major</option>
            </select>
          </label>
          <Field
            name="FX date field (blank uses event time)"
            value={config.rate_date_field}
            set={(v) => update("rate_date_field", v)}
          />
        </>
      )}
      <div className="col-span-full">
        <WhereFields
          value={config.where || {}}
          set={(where) => update("where", where)}
        />
      </div>
    </div>
  );
}
function WhereFields({
  value,
  set,
}: {
  value: Record<string, any>;
  set: (v: Record<string, any>) => void;
}) {
  const [rows, setRows] = useState(() =>
    Object.entries(value).map(([key, v]) => ({
      key,
      type: v === null ? "null" : Array.isArray(v) ? "array" : typeof v,
      raw: typeof v === "string" ? v : JSON.stringify(v),
    })),
  );
  const parse = (row: (typeof rows)[number]): unknown => {
    if (row.type === "string") return row.raw;
    if (row.type === "null") return null;
    if (row.type === "number") {
      if (!row.raw.trim() || !Number.isFinite(Number(row.raw)))
        throw new Error("Enter a finite number");
      return Number(row.raw);
    }
    if (row.type === "boolean") {
      if (row.raw !== "true" && row.raw !== "false")
        throw new Error("Enter true or false");
      return row.raw === "true";
    }
    const list: unknown = JSON.parse(row.raw);
    if (
      !Array.isArray(list) ||
      list.length > 100 ||
      list.some(
        (v) =>
          v !== null && !["string", "number", "boolean"].includes(typeof v),
      )
    )
      throw new Error("Enter a JSON list of at most 100 scalar values");
    return list;
  };
  const change = (next: typeof rows) => {
    setRows(next);
    const out: Record<string, unknown> = {};
    try {
      for (const r of next) {
        if (r.key) out[r.key] = parse(r);
      }
    } catch {
      return;
    }
    set(out);
  };
  return (
    <fieldset className="space-y-2">
      <legend className="text-xs">Property filters</legend>
      {rows.map((r, i) => (
        <div className="flex gap-2" key={i}>
          <input
            aria-label={`Filter field ${i + 1}`}
            placeholder="props.plan"
            required
            className={input}
            value={r.key}
            onChange={(e) =>
              change(
                rows.map((r, j) =>
                  i === j ? { ...r, key: e.target.value } : r,
                ),
              )
            }
          />
          <select
            aria-label={`Filter type ${i + 1}`}
            className={input}
            value={r.type}
            onChange={(e) =>
              change(
                rows.map((r, j) =>
                  i === j ? { ...r, type: e.target.value } : r,
                ),
              )
            }
          >
            {["string", "number", "boolean", "null", "array"].map((v) => (
              <option key={v}>{v}</option>
            ))}
          </select>
          <input
            aria-label={`Filter value ${i + 1}`}
            ref={(node) => {
              if (!node) return;
              try {
                parse(r);
                node.setCustomValidity("");
              } catch (error) {
                node.setCustomValidity(String(error));
              }
            }}
            className={input}
            disabled={r.type === "null"}
            value={r.raw}
            onChange={(e) =>
              change(
                rows.map((r, j) =>
                  i === j ? { ...r, raw: e.target.value } : r,
                ),
              )
            }
          />
          <button
            type="button"
            className={button}
            onClick={() => change(rows.filter((_, j) => j !== i))}
          >
            Remove
          </button>
        </div>
      ))}
      <button
        type="button"
        className={button}
        onClick={() => change([...rows, { key: "", raw: "", type: "string" }])}
      >
        Add filter
      </button>
    </fieldset>
  );
}
export function WidgetEditor({
  project,
  widget,
  onSaved,
}: {
  project: string;
  widget: any;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false),
    [draft, setDraft] = useState(widget),
    [error, setError] = useState("");
  if (!open)
    return (
      <button
        className={button}
        onClick={() => {
          setDraft(widget);
          setOpen(true);
        }}
      >
        Edit widget
      </button>
    );
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await analyticsRequest(project, `/widgets/${widget.id}`, "PUT", draft);
      setOpen(false);
      onSaved();
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <form
      onSubmit={save}
      className="col-span-full space-y-3 border border-border p-3 rounded"
    >
      <Field
        name="Widget title"
        value={draft.title}
        set={(title) => setDraft({ ...draft, title })}
      />
      <MetricFields
        config={draft.config}
        set={(config) => setDraft({ ...draft, config })}
      />
      <div className="flex flex-wrap gap-2">
        <Field
          name="Window (24h, 7d, all or $filters.window)"
          value={draft.config.window}
          set={(window) =>
            setDraft({ ...draft, config: { ...draft.config, window } })
          }
        />
        {["timeseries"].includes(draft.type) && (
          <Field
            name="Interval (minute, hour, day)"
            value={draft.config.interval}
            set={(interval) =>
              setDraft({ ...draft, config: { ...draft.config, interval } })
            }
          />
        )}
        <Field
          name="Display format (number, percent, currency, compact)"
          value={draft.config.format}
          set={(format) =>
            setDraft({ ...draft, config: { ...draft.config, format } })
          }
        />
        {draft.config.format === "percent" && (
          <label>
            Percentage input
            <select
              className={input}
              value={draft.config.percent_input || "fraction"}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  config: { ...draft.config, percent_input: e.target.value },
                })
              }
            >
              <option value="fraction">Fraction (0.25 = 25%)</option>
              <option value="points">Points (25 = 25%)</option>
            </select>
          </label>
        )}
        {["top", "breakdown"].includes(draft.type) && (
          <Field
            name="Group field"
            value={draft.config.by}
            set={(by) =>
              setDraft({ ...draft, config: { ...draft.config, by } })
            }
          />
        )}
      </div>
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button className={button}>Save widget</button>
        <button type="button" className={button} onClick={() => setOpen(false)}>
          Cancel
        </button>
        <button
          type="button"
          className={button}
          onClick={async () => {
            if (!confirm("Delete this widget?")) return;
            try {
              await analyticsRequest(
                project,
                `/widgets/${widget.id}`,
                "DELETE",
              );
              onSaved();
            } catch (e) {
              setError(String(e));
            }
          }}
        >
          Delete widget
        </button>
      </div>
    </form>
  );
}
export function ObjectiveEditor({
  project,
  objective,
  onSaved,
}: {
  project: string;
  objective: any;
  onSaved: () => void;
}) {
  const [draft, setDraft] = useState<any>(null),
    [error, setError] = useState("");
  if (!draft)
    return (
      <button
        className={button}
        onClick={() => setDraft(structuredClone(objective))}
      >
        Edit objective
      </button>
    );
  const update = (i: number, patch: any) =>
    setDraft({
      ...draft,
      targets: draft.targets.map((t: any, j: number) =>
        i === j ? { ...t, ...patch } : t,
      ),
    });
  return (
    <form
      className="space-y-3 border border-border rounded p-3"
      onSubmit={async (e) => {
        e.preventDefault();
        if (
          draft.targets.some(
            (t: any) => t.target_value === "" || t.target_value == null,
          )
        ) {
          setError("Target values are required");
          return;
        }
        try {
          await analyticsRequest(
            project,
            `/objectives/${draft.id}`,
            "PUT",
            draft,
          );
          setDraft(null);
          onSaved();
        } catch (e) {
          setError(String(e));
        }
      }}
    >
      <Field
        name="Objective name"
        value={draft.name}
        set={(name) => setDraft({ ...draft, name })}
      />
      {draft.targets.map((t: any, i: number) => (
        <fieldset className="space-y-2 border border-border p-2" key={t.id}>
          <legend>{t.name}</legend>
          <div className="grid grid-cols-2 gap-2">
            <Field
              name="Target name"
              value={t.name}
              set={(name) => update(i, { name })}
            />
            <Field
              name="Target value"
              type="number"
              value={t.target_value}
              set={(v) =>
                update(i, { target_value: v === "" ? null : Number(v) })
              }
            />
            <label>
              Direction
              <select
                className={input}
                value={t.direction}
                onChange={(e) => update(i, { direction: e.target.value })}
              >
                <option value="at_least">At least</option>
                <option value="at_most">At most</option>
              </select>
            </label>
            <Field
              name="Unit"
              value={t.unit}
              set={(unit) => update(i, { unit })}
            />
            {t.unit === "money" && (
              <Field
                name="Target currency"
                value={t.currency}
                set={(currency) =>
                  update(i, { currency: currency.toUpperCase() })
                }
              />
            )}
            <Field
              name="Period start (UTC)"
              type="datetime-local"
              value={new Date(t.period_start).toISOString().slice(0, 16)}
              set={(v) => {
                const ms = Date.parse(v + "Z");
                if (Number.isFinite(ms)) update(i, { period_start: ms });
              }}
            />
            <Field
              name="Period end exclusive (UTC)"
              type="datetime-local"
              value={new Date(t.period_end).toISOString().slice(0, 16)}
              set={(v) => {
                const ms = Date.parse(v + "Z");
                if (Number.isFinite(ms)) update(i, { period_end: ms });
              }}
            />
          </div>
          <MetricFields
            config={t.query}
            set={(query) => update(i, { query })}
          />
          <button
            type="button"
            className={button}
            disabled={draft.targets.length === 1}
            onClick={() =>
              setDraft({
                ...draft,
                targets: draft.targets.filter((_: any, j: number) => j !== i),
              })
            }
          >
            Retire target
          </button>
        </fieldset>
      ))}
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
      <button className={button}>Save objective</button>{" "}
      <button type="button" className={button} onClick={() => setDraft(null)}>
        Cancel
      </button>
      <p className="text-xs text-text-dim">
        Percent targets use percentage points (25 means 25%). Target IDs are
        preserved on save.
      </p>
    </form>
  );
}
export function ManagementTab({ project }: { project: string }) {
  const [tab, setTab] = useState("references"),
    [error, setError] = useState(""),
    [status, setStatus] = useState("");
  const [sets, setSets] = useState<any[]>([]),
    [set, setSet] = useState(""),
    [values, setValues] = useState<any[]>([]),
    [search, setSearch] = useState(""),
    [cursor, setCursor] = useState(0),
    [total, setTotal] = useState(0);
  const [key, setKey] = useState(""),
    [label, setLabel] = useState(""),
    [value, setValue] = useState(""),
    [valueStatus, setValueStatus] = useState("active");
  const [rates, setRates] = useState<any[]>([]),
    [base, setBase] = useState("USD"),
    [quote, setQuote] = useState("EUR"),
    [date, setDate] = useState(new Date().toISOString().slice(0, 10)),
    [rate, setRate] = useState("");
  const [policy, setPolicy] = useState({
      event_days: 0,
      diagnostic_days: 30,
      archive_days: 30,
    }),
    [health, setHealth] = useState<any>(null),
    [archiveID, setArchiveID] = useState("");
  const run = async (fn: () => Promise<void>) => {
    setError("");
    setStatus("");
    try {
      await fn();
    } catch (e) {
      setError(String(e));
    }
  };
  const loadSets = useCallback(async () => {
    const data = await analyticsRequest(project, "/references");
    setSets(data.sets ?? []);
  }, [project]);
  useEffect(() => {
    let active = true;
    Promise.all([
      loadSets(),
      analyticsRequest(project, "/fx-rates"),
      analyticsRequest(project, "/retention"),
      analyticsRequest(project, "/diagnostics"),
    ])
      .then(([, fx, p, h]) => {
        if (active) {
          setRates(fx.rates ?? []);
          setPolicy(p);
          setHealth(h);
        }
      })
      .catch((e) => {
        if (active) setError(String(e));
      });
    return () => {
      active = false;
    };
  }, [project, loadSets]);
  useEffect(() => {
    if (!set) {
      setValues([]);
      return;
    }
    let active = true;
    analyticsRequest(
      project,
      `/references?reference_set=${encodeURIComponent(set)}&search=${encodeURIComponent(search)}`,
    )
      .then((d) => {
        if (active) {
          setValues(d.values);
          setCursor(d.next_cursor);
          setTotal(d.total);
        }
      })
      .catch((e) => {
        if (active) setError(String(e));
      });
    return () => {
      active = false;
    };
  }, [project, set, search]);
  return (
    <section className="flex-1 overflow-auto p-4 space-y-4">
      <nav className="flex gap-2">
        {["references", "FX rates", "retention"].map((v) => (
          <button key={v} className={button} onClick={() => setTab(v)}>
            {v}
          </button>
        ))}
      </nav>
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
      {status && <p role="status">{status}</p>}
      {tab === "references" && (
        <>
          <h3>Reference sets</h3>
          <form
            className="flex gap-2 items-end"
            onSubmit={(e) => {
              e.preventDefault();
              void run(async () => {
                await analyticsRequest(project, "/references", "POST", {
                  key,
                  label,
                });
                await loadSets();
                setSet(key);
                setStatus("Reference set saved");
              });
            }}
          >
            <Field name="Set key" value={key} set={setKey} />
            <Field name="Set label" value={label} set={setLabel} />
            <button className={button}>Save set</button>
          </form>
          <select
            aria-label="Reference set"
            className={input}
            value={set}
            onChange={(e) => {
              setSet(e.target.value);
              setValue("");
            }}
          >
            <option value="">Choose a set</option>
            {sets.map((s) => (
              <option key={s.key} value={s.key}>
                {s.label}
              </option>
            ))}
          </select>
          {set && (
            <>
              <Field name="Search values" value={search} set={setSearch} />
              <form
                className="flex gap-2 items-end"
                onSubmit={(e) => {
                  e.preventDefault();
                  void run(async () => {
                    await analyticsRequest(project, "/references", "POST", {
                      reference_set: set,
                      value,
                      label,
                      status: valueStatus,
                    });
                    const data = await analyticsRequest(
                      project,
                      `/references?reference_set=${encodeURIComponent(set)}`,
                    );
                    setValues(data.values);
                    setTotal(data.total);
                    setCursor(data.next_cursor);
                    setStatus("Reference value saved");
                  });
                }}
              >
                <Field name="Value" value={value} set={setValue} />
                <Field name="Value label" value={label} set={setLabel} />
                <select
                  aria-label="Value status"
                  className={input}
                  value={valueStatus}
                  onChange={(e) => setValueStatus(e.target.value)}
                >
                  <option value="active">Active</option>
                  <option value="inactive">Inactive</option>
                </select>
                <button className={button}>Save value</button>
              </form>
              <p>
                {values.length} of {total} values
              </p>
              <ul>
                {values.map((v) => (
                  <li key={v.id}>
                    <button
                      className="py-1 text-sm"
                      onClick={() => {
                        setValue(v.value);
                        setLabel(v.label);
                        setValueStatus(v.status);
                      }}
                    >
                      {v.label} · {v.value} · {v.status}
                    </button>
                  </li>
                ))}
              </ul>
              {cursor > 0 && (
                <button
                  className={button}
                  onClick={() =>
                    void run(async () => {
                      const data = await analyticsRequest(
                        project,
                        `/references?reference_set=${encodeURIComponent(set)}&after=${cursor}&search=${encodeURIComponent(search)}`,
                      );
                      setValues((prev) => [...prev, ...data.values]);
                      setCursor(data.next_cursor);
                    })
                  }
                >
                  Load more
                </button>
              )}
            </>
          )}
        </>
      )}
      {tab === "FX rates" && (
        <>
          <h3>Exchange rates</h3>
          <p className="text-sm text-text-dim">
            Each edit creates an immutable revision. Reports identify the
            revisions used. The latest applicable direct or inverse quote wins;
            direct wins ties.
          </p>
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (!rate.trim()) {
                setError("Rate is required");
                return;
              }
              void run(async () => {
                await analyticsRequest(project, "/fx-rates", "POST", {
                  base_currency: base,
                  quote_currency: quote,
                  as_of: Date.parse(date + "T00:00:00Z"),
                  rate: Number(rate),
                  source: "manual",
                });
                const d = await analyticsRequest(project, "/fx-rates");
                setRates(d.rates);
                setStatus("Rate revision saved");
              });
            }}
          >
            <Field name="Base currency" value={base} set={setBase} />
            <Field name="Quote currency" value={quote} set={setQuote} />
            <Field
              name="Effective date (UTC)"
              type="date"
              value={date}
              set={setDate}
            />
            <Field name="Rate" type="number" value={rate} set={setRate} />
            <button className={button}>Save rate</button>
          </form>
          <table className="w-full text-sm">
            <thead>
              <tr>
                <th>Pair</th>
                <th>Date</th>
                <th>Rate</th>
                <th>Revision</th>
              </tr>
            </thead>
            <tbody>
              {rates.map((r) => (
                <tr key={r.id}>
                  <td>
                    {r.base_currency}/{r.quote_currency}
                  </td>
                  <td>{new Date(r.as_of).toISOString().slice(0, 10)}</td>
                  <td>{r.rate}</td>
                  <td>{r.revision_id}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {tab === "retention" && (
        <>
          <h3>Retention and diagnostics</h3>
          <p className="text-sm">
            Raw event expiry is disabled by default (0 days). Expired raw events
            enter a recoverable archive, then are permanently removed after the
            archive period. Rollup inputs and snapshots are retained.
          </p>
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void run(async () => {
                setPolicy(
                  await analyticsRequest(project, "/retention", "PUT", policy),
                );
                setStatus("Retention policy saved");
              });
            }}
          >
            {(
              [
                ["event_days", "Raw event days (0 keeps all)"],
                ["diagnostic_days", "Diagnostic days"],
                ["archive_days", "Archive days"],
              ] as const
            ).map(([key, label]) => (
              <Field
                key={key}
                name={label}
                type="number"
                value={policy[key]}
                set={(v) =>
                  setPolicy({ ...policy, [key]: v === "" ? NaN : Number(v) })
                }
              />
            ))}
            <button className={button}>Save retention</button>
          </form>
          <form
            className="flex items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void run(async () => {
                await analyticsRequest(project, "/archive/restore", "POST", {
                  id: Number(archiveID),
                });
                setStatus("Archived event restored");
              });
            }}
          >
            <Field
              name="Archived event ID"
              type="number"
              value={archiveID}
              set={setArchiveID}
            />
            <button className={button}>Restore event</button>
          </form>
          {health && (
            <dl className="grid grid-cols-2 gap-2 text-sm">
              {Object.entries(health).map(([key, value]) => (
                <div key={key}>
                  <dt className="text-text-dim">{key.replaceAll("_", " ")}</dt>
                  <dd>{String(value)}</dd>
                </div>
              ))}
            </dl>
          )}
          <button
            className={button}
            onClick={() =>
              void run(async () =>
                setHealth(await analyticsRequest(project, "/diagnostics")),
              )
            }
          >
            Refresh diagnostics
          </button>
        </>
      )}
    </section>
  );
}
