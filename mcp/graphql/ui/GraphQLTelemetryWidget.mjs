// mcp/graphql/ui/GraphQLTelemetryWidget.tsx
import { useEffect, useState } from "react";

// mcp/graphql/ui/telemetry.ts
function hasLogErrors(log) {
  return Number(log.status_code) >= 400 || !!log.error || (log.errors?.length || 0) > 0 || (log.error_codes?.length || 0) > 0;
}
function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024)
    return `${bytes} B`;
  if (bytes < 1024 * 1024)
    return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
function telemetryQuery(filters, slowMS, now = new Date) {
  const query = new URLSearchParams;
  Object.entries(filters).forEach(([key, value]) => {
    if (key !== "range" && key !== "since" && key !== "until" && value !== "")
      query.set(key, value);
  });
  if (filters.range === "custom") {
    for (const key of ["since", "until"])
      if (filters[key])
        query.set(key, new Date(filters[key]).toISOString());
  } else if (filters.range !== "all") {
    query.set("since", new Date(now.getTime() - Number(filters.range || 60) * 60000).toISOString());
  }
  query.set("slow_threshold_ms", String(slowMS));
  return query;
}

// mcp/graphql/ui/GraphQLTelemetryWidget.tsx
import { jsx, jsxs, Fragment } from "react/jsx-runtime";
var input = "w-full rounded border border-border bg-surface-2 px-2 py-1.5 text-xs text-text focus:border-accent outline-none";
var button = "rounded border border-border px-2.5 py-1.5 text-xs hover:bg-surface-2 disabled:opacity-50";
var defaults = { range: "60", sort_by: "created_at", sort_order: "desc", limit: "100" };
var windows = [["15", "Last 15 minutes"], ["60", "Last hour"], ["1440", "Last 24 hours"], ["10080", "Last 7 days"], ["all", "All time"], ["custom", "Custom dates"]];
async function telemetryFetch(props, path, query, signal) {
  if (props.projectId)
    query.set("project_id", props.projectId);
  if (props.installId)
    query.set("install_id", String(props.installId));
  const response = await fetch(`/api/apps/${encodeURIComponent(props.appName || "graphql")}/admin/${path}?${query}`, { credentials: "same-origin", signal });
  const body = await response.json();
  if (!response.ok)
    throw new Error(body.error || body.message || `Request failed (${response.status})`);
  return body;
}
function Field({ label, children }) {
  return /* @__PURE__ */ jsxs("label", {
    className: "block min-w-0 space-y-1 text-xs",
    children: [
      /* @__PURE__ */ jsx("span", {
        className: "text-text-dim",
        children: label
      }),
      children
    ]
  });
}
function NumberFilter({ name, label, draft, setField }) {
  return /* @__PURE__ */ jsx(Field, {
    label,
    children: /* @__PURE__ */ jsx("input", {
      className: input,
      type: "number",
      min: "0",
      step: "1",
      value: draft[name] || "",
      onChange: (e) => setField(name, e.target.value)
    })
  });
}
function FilterButton({ active, children, onClick }) {
  return /* @__PURE__ */ jsx("button", {
    type: "button",
    "aria-pressed": active,
    className: `${button} ${active ? "border-accent bg-accent/10 text-accent" : "text-text-dim"}`,
    onClick,
    children
  });
}
function RuntimeDetails({ metrics }) {
  const resolvers = Object.entries(metrics.resolver_timings || {}).sort((a, b) => b[1].total_ms - a[1].total_ms);
  return /* @__PURE__ */ jsxs("div", {
    className: "space-y-2",
    children: [
      /* @__PURE__ */ jsx("h3", {
        className: "font-semibold",
        children: "Runtime diagnostics"
      }),
      /* @__PURE__ */ jsx("div", {
        className: "flex flex-wrap gap-2",
        children: [["Queue", `${Number(metrics.queue_ms || 0).toFixed(1)} ms`], ["Execution", metrics.coalesced ? "Joined shared execution" : "Own execution"], ["Waiters", metrics.waiters || 1], ["Loader hits", metrics.loader_hits || 0], ["Backend calls", metrics.backend_calls || 0], ["Backend operations", metrics.backend_reads || 0], ["Snapshot acquisition", `${Number(metrics.snapshot_ms || 0).toFixed(1)} ms`], ["Consistency", metrics.consistency || "none"]].map(([name, value]) => /* @__PURE__ */ jsxs("span", {
          className: "rounded border border-border px-2 py-1",
          children: [
            name,
            ": ",
            value
          ]
        }, String(name)))
      }),
      metrics.execution_id && /* @__PURE__ */ jsxs("p", {
        className: "break-all text-text-dim",
        children: [
          "Shared execution ID: ",
          metrics.execution_id
        ]
      }),
      metrics.batch_sizes?.length > 0 && /* @__PURE__ */ jsxs("p", {
        className: "text-text-dim",
        children: [
          "Batch sizes: ",
          metrics.batch_sizes.join(", ")
        ]
      }),
      resolvers.length > 0 && /* @__PURE__ */ jsx("div", {
        className: "overflow-x-auto",
        children: /* @__PURE__ */ jsxs("table", {
          className: "w-full text-left",
          children: [
            /* @__PURE__ */ jsx("caption", {
              className: "mb-1 text-left text-text-dim",
              children: "Resolver completion time, including waits for source reads"
            }),
            /* @__PURE__ */ jsx("thead", {
              children: /* @__PURE__ */ jsx("tr", {
                children: ["Field", "Calls", "Total ms", "Max ms", "Errors"].map((label) => /* @__PURE__ */ jsx("th", {
                  className: "px-2 py-1 font-normal text-text-dim",
                  children: label
                }, label))
              })
            }),
            /* @__PURE__ */ jsx("tbody", {
              children: resolvers.map(([field, raw]) => /* @__PURE__ */ jsxs("tr", {
                className: "border-t border-border",
                children: [
                  /* @__PURE__ */ jsx("td", {
                    className: "px-2 py-1 font-mono",
                    children: field
                  }),
                  /* @__PURE__ */ jsx("td", {
                    className: "px-2",
                    children: raw.calls
                  }),
                  /* @__PURE__ */ jsx("td", {
                    className: "px-2",
                    children: Number(raw.total_ms).toFixed(1)
                  }),
                  /* @__PURE__ */ jsx("td", {
                    className: "px-2",
                    children: Number(raw.max_ms).toFixed(1)
                  }),
                  /* @__PURE__ */ jsx("td", {
                    className: `px-2 ${raw.errors ? "text-red-300" : ""}`,
                    children: raw.errors
                  })
                ]
              }, field))
            })
          ]
        })
      }),
      metrics.sources?.length > 0 && /* @__PURE__ */ jsxs("details", {
        children: [
          /* @__PURE__ */ jsx("summary", {
            className: "cursor-pointer text-text-dim",
            children: "Source freshness and coverage"
          }),
          /* @__PURE__ */ jsx("pre", {
            className: "mt-2 overflow-auto whitespace-pre-wrap break-words",
            children: JSON.stringify(metrics.sources, null, 2)
          })
        ]
      })
    ]
  });
}
function Details({ log }) {
  const errors = log.errors?.length ? log.errors : log.error ? [{ message: log.error }] : [];
  const [copied, setCopied] = useState(false);
  return /* @__PURE__ */ jsxs("div", {
    className: "space-y-3 rounded border border-border bg-surface-2/40 p-3 text-xs",
    children: [
      errors.length > 0 && /* @__PURE__ */ jsx("div", {
        className: "space-y-2",
        children: errors.map((error, index) => /* @__PURE__ */ jsxs("div", {
          className: "rounded border border-red-400/30 bg-red-400/5 p-2",
          children: [
            /* @__PURE__ */ jsx("p", {
              className: "whitespace-pre-wrap break-words text-red-300",
              children: error.message
            }),
            /* @__PURE__ */ jsxs("p", {
              className: "mt-1 break-words text-text-dim",
              children: [
                error.extensions?.code || "GraphQL error",
                error.path?.length ? ` · Field: ${error.path.join(".")}` : "",
                error.locations?.length ? ` · ${error.locations.map((l) => `line ${l.line}:${l.column}`).join(", ")}` : ""
              ]
            })
          ]
        }, index))
      }),
      /* @__PURE__ */ jsx("dl", {
        className: "grid grid-cols-1 gap-2 sm:grid-cols-2",
        children: [["Request ID", log.request_id || "Unavailable (older request)"], ["Operation hash", log.operation_hash || "Unavailable"], ["Environment", log.environment || "Not recorded (older request)"], ["Authorization scope", log.authorization_scope || "Not recorded"], ["Release", log.api_release ? `v${log.api_release}` : "Legacy"], ["Response", `${formatBytes(log.response_bytes)} · ${log.row_count} rows · ${log.resolver_count} resolvers`]].map(([label, value]) => /* @__PURE__ */ jsxs("div", {
          children: [
            /* @__PURE__ */ jsx("dt", {
              className: "text-text-dim",
              children: label
            }),
            /* @__PURE__ */ jsx("dd", {
              className: "break-all text-text",
              children: value
            })
          ]
        }, label))
      }),
      log.error_codes?.length > 0 && /* @__PURE__ */ jsxs("p", {
        className: "break-words text-red-300",
        children: [
          "Error codes: ",
          log.error_codes.join(", ")
        ]
      }),
      /* @__PURE__ */ jsx("div", {
        className: "flex flex-wrap gap-2",
        children: Object.entries(log.timings || {}).map(([name, ms]) => /* @__PURE__ */ jsxs("span", {
          className: "rounded border border-border px-2 py-1",
          children: [
            name,
            ": ",
            Number(ms).toFixed(1),
            " ms"
          ]
        }, name))
      }),
      /* @__PURE__ */ jsxs("p", {
        className: "text-text-dim",
        children: [
          "Sources: ",
          Object.entries(log.source_timings || {}).map(([name, ms]) => `${name} ${Number(ms).toFixed(1)} ms`).join(" · ") || "None recorded"
        ]
      }),
      log.runtime && Object.keys(log.runtime).length > 0 && /* @__PURE__ */ jsx(RuntimeDetails, {
        metrics: log.runtime
      }),
      /* @__PURE__ */ jsx("button", {
        type: "button",
        className: button,
        onClick: async () => {
          try {
            await navigator.clipboard.writeText(JSON.stringify(log, null, 2));
            setCopied(true);
          } catch {
            setCopied(false);
          }
        },
        children: copied ? "Copied request details" : "Copy request details"
      })
    ]
  });
}
function LogsPanel(props) {
  const slowMS = props.slowMS || 1000;
  const initial = { ...defaults, range: props.initialRange || "60", limit: String(props.initialLimit || (props.compact ? 10 : 100)), ...props.initialView === "errors" ? { has_errors: "true" } : props.initialView === "slow" ? { min_duration_ms: String(slowMS), sort_by: "duration_ms" } : {} };
  const [active, setActive] = useState(initial);
  const [draft, setDraft] = useState(initial);
  const [logs, setLogs] = useState([]);
  const [summary, setSummary] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [updated, setUpdated] = useState(null);
  const [expanded, setExpanded] = useState(null);
  const [refresh, setRefresh] = useState(0);
  const [live, setLive] = useState(!!props.compact);
  useEffect(() => {
    const controller = new AbortController;
    setLoading(true);
    setError("");
    let query;
    try {
      query = telemetryQuery(active, slowMS);
      query.set("api_slug", props.apiSlug);
    } catch {
      setError("Enter valid dates for the custom time range.");
      setLoading(false);
      return;
    }
    telemetryFetch(props, "logs", query, controller.signal).then((body) => {
      if (controller.signal.aborted)
        return;
      setLogs(body.logs || []);
      setSummary(body.summary || null);
      setUpdated(new Date);
    }).catch((err) => {
      if (!controller.signal.aborted)
        setError(err.message);
    }).finally(() => {
      if (!controller.signal.aborted)
        setLoading(false);
    });
    return () => controller.abort();
  }, [props.projectId, props.installId, props.appName, props.apiSlug, active, slowMS, refresh]);
  useEffect(() => {
    setLogs([]);
    setSummary(null);
    setUpdated(null);
    setExpanded(null);
  }, [props.projectId, props.installId, props.apiSlug]);
  useEffect(() => {
    if (!live)
      return;
    const timer = setInterval(() => {
      if (!document.hidden)
        setRefresh((value) => value + 1);
    }, 30000);
    return () => clearInterval(timer);
  }, [live]);
  function apply(next) {
    setDraft(next);
    setActive(next);
    setExpanded(null);
  }
  function quick(view) {
    const next = { ...active };
    for (const key of ["has_errors", "min_duration_ms", "min_response_bytes", "error_code", "status_code", "min_queue_ms", "coalesced"])
      delete next[key];
    next.sort_order = "desc";
    next.sort_by = "created_at";
    if (view === "errors")
      next.has_errors = "true";
    if (view === "slow") {
      next.min_duration_ms = String(slowMS);
      next.sort_by = "duration_ms";
    }
    if (view === "queued") {
      next.min_queue_ms = "1";
      next.sort_by = "queue_ms";
    }
    if (view === "shared")
      next.coalesced = "true";
    if (view === "large") {
      next.min_response_bytes = "1048576";
      next.sort_by = "response_bytes";
    }
    apply(next);
  }
  const setField = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const count = Number(summary?.requests || 0);
  return /* @__PURE__ */ jsxs("section", {
    className: `${props.compact ? "" : "p-5"} text-text`,
    children: [
      /* @__PURE__ */ jsxs("header", {
        className: "mb-3 flex flex-wrap items-center justify-between gap-2",
        children: [
          /* @__PURE__ */ jsxs("div", {
            children: [
              /* @__PURE__ */ jsx("h2", {
                className: "text-sm font-semibold",
                children: props.compact ? "Request health" : "Requests and errors"
              }),
              /* @__PURE__ */ jsx("p", {
                className: "mt-1 text-xs text-text-dim",
                children: "HTTP failures and GraphQL errors are counted, including partial errors with status 200."
              })
            ]
          }),
          /* @__PURE__ */ jsxs("div", {
            className: "flex items-center gap-2",
            children: [
              /* @__PURE__ */ jsxs("label", {
                className: "flex items-center gap-1 text-xs text-text-dim",
                children: [
                  /* @__PURE__ */ jsx("input", {
                    type: "checkbox",
                    checked: live,
                    onChange: (e) => setLive(e.target.checked)
                  }),
                  "Live · 30s"
                ]
              }),
              /* @__PURE__ */ jsx("button", {
                className: button,
                disabled: loading,
                onClick: () => setRefresh((value) => value + 1),
                children: loading ? "Refreshing…" : "Refresh"
              })
            ]
          })
        ]
      }),
      /* @__PURE__ */ jsxs("nav", {
        "aria-label": "Filter GraphQL requests",
        className: "mb-3 flex flex-wrap gap-2",
        children: [
          /* @__PURE__ */ jsx(FilterButton, {
            active: !active.has_errors && !active.min_duration_ms && !active.min_response_bytes && !active.min_queue_ms && !active.coalesced,
            onClick: () => quick("all"),
            children: "All requests"
          }),
          /* @__PURE__ */ jsx(FilterButton, {
            active: active.has_errors === "true",
            onClick: () => quick("errors"),
            children: "Errors"
          }),
          /* @__PURE__ */ jsxs(FilterButton, {
            active: !!active.min_duration_ms,
            onClick: () => quick("slow"),
            children: [
              "Slow ≥ ",
              slowMS,
              " ms"
            ]
          }),
          /* @__PURE__ */ jsx(FilterButton, {
            active: !!active.min_response_bytes,
            onClick: () => quick("large"),
            children: "Large ≥ 1 MB"
          }),
          /* @__PURE__ */ jsx(FilterButton, {
            active: !!active.min_queue_ms,
            onClick: () => quick("queued"),
            children: "Queued"
          }),
          /* @__PURE__ */ jsx(FilterButton, {
            active: active.coalesced === "true",
            onClick: () => quick("shared"),
            children: "Shared executions"
          })
        ]
      }),
      /* @__PURE__ */ jsxs("form", {
        className: "mb-3 space-y-3 rounded border border-border p-3",
        onSubmit: (e) => {
          e.preventDefault();
          apply(draft);
        },
        children: [
          /* @__PURE__ */ jsxs("div", {
            className: `grid gap-2 ${props.compact ? "grid-cols-2" : "grid-cols-1 sm:grid-cols-2 xl:grid-cols-4"}`,
            children: [
              /* @__PURE__ */ jsx(Field, {
                label: "Time range",
                children: /* @__PURE__ */ jsxs("select", {
                  className: input,
                  value: draft.range,
                  onChange: (e) => setField("range", e.target.value),
                  children: [
                    !windows.some(([value]) => value === draft.range) && /* @__PURE__ */ jsxs("option", {
                      value: draft.range,
                      children: [
                        "Last ",
                        draft.range,
                        " minutes"
                      ]
                    }),
                    windows.map(([value, label]) => /* @__PURE__ */ jsx("option", {
                      value,
                      children: label
                    }, value))
                  ]
                })
              }),
              /* @__PURE__ */ jsx(Field, {
                label: "Search",
                children: /* @__PURE__ */ jsx("input", {
                  className: input,
                  placeholder: "Error, operation, or request ID",
                  value: draft.search || "",
                  onChange: (e) => setField("search", e.target.value)
                })
              }),
              !props.compact && /* @__PURE__ */ jsx(Field, {
                label: "Environment",
                children: /* @__PURE__ */ jsx("select", {
                  className: input,
                  value: draft.environment || "",
                  onChange: (e) => setField("environment", e.target.value),
                  children: [["", "All environments"], ["development", "Development"], ["staging", "Staging"], ["production", "Production"]].map(([value, label]) => /* @__PURE__ */ jsx("option", {
                    value,
                    children: label
                  }, value))
                })
              }),
              /* @__PURE__ */ jsx(Field, {
                label: "Sort",
                children: /* @__PURE__ */ jsx("select", {
                  className: input,
                  value: draft.sort_by,
                  onChange: (e) => setField("sort_by", e.target.value),
                  children: [["created_at", "Time"], ["duration_ms", "Duration"], ["response_bytes", "Response size"], ["row_count", "Rows"], ["resolver_count", "Resolvers"], ["status_code", "Status"], ["queue_ms", "Queue wait"], ["backend_reads", "Backend operations"], ["operation_name", "Operation"]].map(([value, label]) => /* @__PURE__ */ jsx("option", {
                    value,
                    children: label
                  }, value))
                })
              })
            ]
          }),
          draft.range === "custom" && /* @__PURE__ */ jsx("div", {
            className: "grid grid-cols-2 gap-2",
            children: [["since", "From"], ["until", "Until"]].map(([key, label]) => /* @__PURE__ */ jsx(Field, {
              label,
              children: /* @__PURE__ */ jsx("input", {
                className: input,
                type: "datetime-local",
                value: draft[key] || "",
                onChange: (e) => setField(key, e.target.value)
              })
            }, key))
          }),
          /* @__PURE__ */ jsxs("details", {
            children: [
              /* @__PURE__ */ jsx("summary", {
                className: "cursor-pointer text-xs text-text-dim",
                children: "Advanced filters"
              }),
              /* @__PURE__ */ jsxs("div", {
                className: "mt-2 grid grid-cols-2 gap-2 xl:grid-cols-4",
                children: [
                  /* @__PURE__ */ jsx(Field, {
                    label: "Result",
                    children: /* @__PURE__ */ jsxs("select", {
                      className: input,
                      value: draft.has_errors || "",
                      onChange: (e) => setField("has_errors", e.target.value),
                      children: [
                        /* @__PURE__ */ jsx("option", {
                          value: "",
                          children: "All results"
                        }),
                        /* @__PURE__ */ jsx("option", {
                          value: "true",
                          children: "With errors"
                        }),
                        /* @__PURE__ */ jsx("option", {
                          value: "false",
                          children: "Successful only"
                        })
                      ]
                    })
                  }),
                  props.compact && /* @__PURE__ */ jsx(Field, {
                    label: "Environment",
                    children: /* @__PURE__ */ jsx("select", {
                      className: input,
                      value: draft.environment || "",
                      onChange: (e) => setField("environment", e.target.value),
                      children: [["", "All environments"], ["development", "Development"], ["staging", "Staging"], ["production", "Production"]].map(([value, label]) => /* @__PURE__ */ jsx("option", {
                        value,
                        children: label
                      }, value))
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "Error code",
                    children: /* @__PURE__ */ jsx("input", {
                      className: input,
                      placeholder: "permission_denied",
                      value: draft.error_code || "",
                      onChange: (e) => setField("error_code", e.target.value)
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "HTTP status",
                    children: /* @__PURE__ */ jsx("input", {
                      className: input,
                      type: "number",
                      min: "100",
                      max: "599",
                      value: draft.status_code || "",
                      onChange: (e) => setField("status_code", e.target.value)
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "Exact operation name",
                    children: /* @__PURE__ */ jsx("input", {
                      className: input,
                      value: draft.operation_name || "",
                      onChange: (e) => setField("operation_name", e.target.value)
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "Operation type",
                    children: /* @__PURE__ */ jsx("select", {
                      className: input,
                      value: draft.operation_type || "",
                      onChange: (e) => setField("operation_type", e.target.value),
                      children: [["", "All types"], ["query", "Query"], ["mutation", "Mutation"], ["subscription", "Subscription"]].map(([value, label]) => /* @__PURE__ */ jsx("option", {
                        value,
                        children: label
                      }, value))
                    })
                  }),
                  [["min_duration_ms", "Min duration (ms)"], ["max_duration_ms", "Max duration (ms)"], ["min_response_bytes", "Min response (bytes)"], ["max_response_bytes", "Max response (bytes)"], ["min_rows", "Min rows"], ["max_rows", "Max rows"], ["min_resolvers", "Min resolvers"], ["max_resolvers", "Max resolvers"], ["min_queue_ms", "Min queue wait (ms)"], ["min_backend_reads", "Min backend operations"]].map(([name, label]) => /* @__PURE__ */ jsx(NumberFilter, {
                    name,
                    label,
                    draft,
                    setField
                  }, name)),
                  /* @__PURE__ */ jsx(Field, {
                    label: "Execution sharing",
                    children: /* @__PURE__ */ jsxs("select", {
                      className: input,
                      value: draft.coalesced || "",
                      onChange: (e) => setField("coalesced", e.target.value),
                      children: [
                        /* @__PURE__ */ jsx("option", {
                          value: "",
                          children: "All executions"
                        }),
                        /* @__PURE__ */ jsx("option", {
                          value: "true",
                          children: "Joined shared execution"
                        }),
                        /* @__PURE__ */ jsx("option", {
                          value: "false",
                          children: "Own execution"
                        })
                      ]
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "Order",
                    children: /* @__PURE__ */ jsxs("select", {
                      className: input,
                      value: draft.sort_order,
                      onChange: (e) => setField("sort_order", e.target.value),
                      children: [
                        /* @__PURE__ */ jsx("option", {
                          value: "desc",
                          children: "Descending"
                        }),
                        /* @__PURE__ */ jsx("option", {
                          value: "asc",
                          children: "Ascending"
                        })
                      ]
                    })
                  }),
                  /* @__PURE__ */ jsx(Field, {
                    label: "List limit",
                    children: /* @__PURE__ */ jsx("input", {
                      className: input,
                      type: "number",
                      min: "1",
                      max: "500",
                      value: draft.limit,
                      onChange: (e) => setField("limit", e.target.value)
                    })
                  })
                ]
              })
            ]
          }),
          /* @__PURE__ */ jsxs("div", {
            className: "flex flex-wrap gap-2",
            children: [
              /* @__PURE__ */ jsx("button", {
                className: `${button} bg-accent text-white`,
                type: "submit",
                disabled: loading,
                children: "Apply filters"
              }),
              /* @__PURE__ */ jsx("button", {
                className: button,
                type: "button",
                onClick: () => apply({ ...defaults, range: props.initialRange || "60", limit: String(props.initialLimit || (props.compact ? 10 : 100)) }),
                children: "Clear filters"
              })
            ]
          })
        ]
      }),
      error && /* @__PURE__ */ jsxs("div", {
        role: "alert",
        className: "mb-3 rounded border border-red-400/30 bg-red-400/10 p-3 text-xs text-red-300",
        children: [
          error,
          updated && " · Previous results are shown below."
        ]
      }),
      summary && /* @__PURE__ */ jsx("div", {
        className: `mb-3 grid gap-2 ${props.compact ? "grid-cols-3" : "grid-cols-2 md:grid-cols-4"}`,
        children: [["Matching requests", count.toLocaleString()], ["Errors", `${summary.errors}${count ? ` · ${(summary.errors * 100 / count).toFixed(1)}%` : ""}`], ["Slow requests", summary.slow], ["Average", `${Number(summary.avg_duration_ms).toFixed(1)} ms`], ["Slowest", `${summary.max_duration_ms} ms`], ["Response bytes", formatBytes(summary.response_bytes)], ["Avg queue wait", `${Number(summary.avg_queue_ms || 0).toFixed(1)} ms`], ["Shared callers", summary.coalesced || 0]].map(([label, value]) => /* @__PURE__ */ jsxs("div", {
          className: "rounded border border-border p-2",
          children: [
            /* @__PURE__ */ jsx("p", {
              className: "text-[10px] text-text-dim",
              children: label
            }),
            /* @__PURE__ */ jsx("p", {
              className: `mt-1 text-sm font-semibold ${label === "Errors" && summary.errors ? "text-red-300" : ""}`,
              children: value
            })
          ]
        }, label))
      }),
      /* @__PURE__ */ jsx("p", {
        className: "mb-2 text-[10px] text-text-dim",
        children: updated ? `Updated ${updated.toLocaleTimeString()} · Showing ${logs.length} of ${count.toLocaleString()} matching requests. Counts include all matches, not just this list.` : "Loading requests…"
      }),
      !loading && !error && logs.length === 0 && /* @__PURE__ */ jsx("p", {
        className: "py-6 text-center text-xs text-text-dim",
        children: "No requests match these filters."
      }),
      props.compact ? /* @__PURE__ */ jsx("div", {
        className: "space-y-2",
        children: logs.map((log) => /* @__PURE__ */ jsxs("article", {
          className: `rounded border p-2 ${hasLogErrors(log) ? "border-red-400/30 bg-red-400/5" : "border-border"}`,
          children: [
            /* @__PURE__ */ jsxs("button", {
              className: "w-full text-left",
              "aria-expanded": expanded === log.id,
              onClick: () => setExpanded(expanded === log.id ? null : log.id),
              children: [
                /* @__PURE__ */ jsxs("div", {
                  className: "flex justify-between gap-2 text-xs",
                  children: [
                    /* @__PURE__ */ jsx("span", {
                      className: "min-w-0 truncate font-semibold",
                      children: log.operation_name || "Unnamed operation"
                    }),
                    /* @__PURE__ */ jsxs("span", {
                      className: "shrink-0",
                      children: [
                        log.duration_ms,
                        " ms"
                      ]
                    })
                  ]
                }),
                /* @__PURE__ */ jsx("p", {
                  className: `mt-1 whitespace-pre-wrap break-words text-xs ${hasLogErrors(log) ? "text-red-300" : "text-text-dim"}`,
                  children: log.error || (hasLogErrors(log) ? (log.error_codes || []).join(", ") || `HTTP ${log.status_code}` : `HTTP ${log.status_code} · ${formatBytes(log.response_bytes)} · ${log.row_count} rows`)
                }),
                /* @__PURE__ */ jsxs("p", {
                  className: "mt-1 text-[10px] text-text-dim",
                  children: [
                    new Date(log.created_at).toLocaleString(),
                    " · ",
                    expanded === log.id ? "Hide" : "View",
                    " details"
                  ]
                })
              ]
            }),
            expanded === log.id && /* @__PURE__ */ jsx("div", {
              className: "mt-2",
              children: /* @__PURE__ */ jsx(Details, {
                log
              })
            })
          ]
        }, log.id))
      }) : /* @__PURE__ */ jsx("div", {
        className: "overflow-x-auto",
        children: /* @__PURE__ */ jsxs("table", {
          className: "w-full text-left text-xs",
          children: [
            /* @__PURE__ */ jsx("thead", {
              className: "text-text-dim",
              children: /* @__PURE__ */ jsx("tr", {
                children: ["Time", "Operation", "Result / errors", "Duration", "Rows", "Resolvers", "Response", "Sources", "Details"].map((label) => /* @__PURE__ */ jsx("th", {
                  className: "whitespace-nowrap px-2 py-2 font-normal",
                  children: label
                }, label))
              })
            }),
            /* @__PURE__ */ jsx("tbody", {
              children: logs.map((log) => {
                const failed = hasLogErrors(log);
                const slow = log.duration_ms >= slowMS;
                return /* @__PURE__ */ jsx(LogRows, {
                  log,
                  failed,
                  slow,
                  expanded: expanded === log.id,
                  toggle: () => setExpanded(expanded === log.id ? null : log.id)
                }, log.id);
              })
            })
          ]
        })
      }),
      /* @__PURE__ */ jsx("p", {
        className: "mt-3 text-[10px] text-text-dim",
        children: "HTTP requests only. Earlier records may lack environment or full error details."
      })
    ]
  });
}
function LogRows({ log, failed, slow, expanded, toggle }) {
  return /* @__PURE__ */ jsxs(Fragment, {
    children: [
      /* @__PURE__ */ jsxs("tr", {
        className: `border-t border-border ${failed ? "bg-red-400/5" : slow ? "bg-yellow-400/5" : ""}`,
        children: [
          /* @__PURE__ */ jsx("td", {
            className: "whitespace-nowrap px-2 py-3 align-top",
            children: new Date(log.created_at).toLocaleString()
          }),
          /* @__PURE__ */ jsxs("td", {
            className: "px-2 py-3 align-top",
            children: [
              /* @__PURE__ */ jsx("p", {
                className: "font-semibold",
                children: log.operation_name || "Unnamed operation"
              }),
              /* @__PURE__ */ jsxs("p", {
                className: "mt-1 text-[10px] text-text-dim",
                children: [
                  log.operation_type || "Rejected request",
                  " · ",
                  log.environment || "Legacy environment",
                  " · ",
                  log.api_release ? `v${log.api_release}` : "Legacy release"
                ]
              })
            ]
          }),
          /* @__PURE__ */ jsxs("td", {
            className: "min-w-[220px] max-w-[360px] px-2 py-3 align-top",
            children: [
              /* @__PURE__ */ jsxs("span", {
                className: failed ? "font-semibold text-red-300" : "text-green-400",
                children: [
                  failed ? "Error" : "Success",
                  " · HTTP ",
                  log.status_code
                ]
              }),
              log.error && /* @__PURE__ */ jsx("p", {
                className: "mt-1 whitespace-pre-wrap break-words text-red-300",
                children: log.error
              }),
              log.error_codes?.length > 0 && /* @__PURE__ */ jsx("p", {
                className: "mt-1 break-words text-[10px] text-red-300",
                children: log.error_codes.join(", ")
              })
            ]
          }),
          /* @__PURE__ */ jsxs("td", {
            className: `whitespace-nowrap px-2 py-3 align-top ${slow ? "font-semibold text-yellow-300" : ""}`,
            children: [
              log.duration_ms,
              " ms",
              slow && /* @__PURE__ */ jsx("p", {
                className: "text-[10px]",
                children: "Slow"
              })
            ]
          }),
          /* @__PURE__ */ jsx("td", {
            className: "px-2 py-3 align-top",
            children: log.row_count
          }),
          /* @__PURE__ */ jsx("td", {
            className: "px-2 py-3 align-top",
            children: log.resolver_count
          }),
          /* @__PURE__ */ jsx("td", {
            className: "whitespace-nowrap px-2 py-3 align-top",
            children: formatBytes(log.response_bytes)
          }),
          /* @__PURE__ */ jsx("td", {
            className: "px-2 py-3 align-top",
            children: Object.entries(log.source_timings || {}).map(([name, ms]) => /* @__PURE__ */ jsxs("p", {
              className: "whitespace-nowrap",
              children: [
                name,
                " ",
                Number(ms).toFixed(1),
                " ms"
              ]
            }, name))
          }),
          /* @__PURE__ */ jsx("td", {
            className: "px-2 py-3 align-top",
            children: /* @__PURE__ */ jsx("button", {
              className: button,
              "aria-expanded": expanded,
              onClick: toggle,
              children: expanded ? "Hide" : "Details"
            })
          })
        ]
      }),
      expanded && /* @__PURE__ */ jsx("tr", {
        children: /* @__PURE__ */ jsx("td", {
          colSpan: 9,
          className: "px-2 pb-3",
          children: /* @__PURE__ */ jsx(Details, {
            log
          })
        })
      })
    ]
  });
}
function integerSetting(value, fallback, min, max) {
  const number = Number(value);
  return Number.isInteger(number) && number >= min && number <= max ? number : fallback;
}
function GraphQLTelemetryWidget(props) {
  const settings = props.widgetSettings || {};
  const preferredAPI = typeof settings.api_slug === "string" && settings.api_slug.trim() ? settings.api_slug.trim() : "default";
  const [apiSlug, setApiSlug] = useState(preferredAPI);
  const [apis, setAPIs] = useState([]);
  const [error, setError] = useState("");
  const slowMS = integerSetting(settings.slow_threshold_ms, 1000, 1, 3600000);
  const range = String(integerSetting(settings.window_minutes, 60, 1, 10080));
  const limit = integerSetting(settings.recent_limit, 10, 1, 50);
  const view = ["all", "errors", "slow"].includes(settings.default_view) ? settings.default_view : "all";
  useEffect(() => {
    const controller = new AbortController;
    setError("");
    setAPIs([]);
    setApiSlug(preferredAPI);
    telemetryFetch(props, "apis", new URLSearchParams, controller.signal).then((body) => {
      if (!controller.signal.aborted)
        setAPIs(body.apis || []);
    }).catch((err) => {
      if (!controller.signal.aborted)
        setError(err.message);
    });
    return () => controller.abort();
  }, [props.projectId, props.installId, props.appName, preferredAPI]);
  return /* @__PURE__ */ jsxs("section", {
    className: "h-full overflow-auto rounded border border-border bg-bg-card p-4 text-text",
    children: [
      /* @__PURE__ */ jsxs("header", {
        className: "mb-3 flex flex-wrap items-center justify-between gap-2",
        children: [
          /* @__PURE__ */ jsx("h2", {
            className: "text-sm font-bold",
            children: "GraphQL"
          }),
          /* @__PURE__ */ jsxs("label", {
            className: "flex items-center gap-2 text-xs text-text-dim",
            children: [
              "API",
              /* @__PURE__ */ jsxs("select", {
                className: `${input} w-auto`,
                value: apiSlug,
                onChange: (e) => setApiSlug(e.target.value),
                children: [
                  !apis.some((api) => api.slug === apiSlug) && /* @__PURE__ */ jsx("option", {
                    value: apiSlug,
                    children: apiSlug
                  }),
                  apis.map((api) => /* @__PURE__ */ jsx("option", {
                    value: api.slug,
                    children: api.name || api.slug
                  }, api.slug))
                ]
              })
            ]
          })
        ]
      }),
      error && /* @__PURE__ */ jsx("p", {
        role: "alert",
        className: "mb-2 text-xs text-red-300",
        children: error
      }),
      /* @__PURE__ */ jsx(LogsPanel, {
        ...props,
        apiSlug,
        compact: true,
        slowMS,
        initialRange: range,
        initialView: view,
        initialLimit: limit
      }, `${props.projectId}:${props.installId}:${range}:${view}:${limit}:${slowMS}`)
    ]
  });
}
export {
  GraphQLTelemetryWidget as default,
  LogsPanel
};
