// mcp/graphql/ui/GraphQLRuntimeControlsWidget.tsx
import { jsx, jsxs } from "react/jsx-runtime";
var input = "w-full rounded border border-border bg-surface-2 px-2 py-1.5 text-xs text-text";
function RuntimeControls({ value, onChange }) {
  let limits;
  try {
    limits = JSON.parse(value);
    if (!limits || typeof limits !== "object" || Array.isArray(limits))
      throw new Error;
  } catch {
    return /* @__PURE__ */ jsx("p", {
      className: "text-xs text-red-300",
      children: "Enter a valid limits object below to use runtime controls."
    });
  }
  const set = (key, v) => onChange(JSON.stringify({ ...limits, [key]: v }, null, 2));
  return /* @__PURE__ */ jsxs("div", {
    className: "space-y-3 rounded border border-border p-3 text-xs",
    children: [
      /* @__PURE__ */ jsxs("label", {
        className: "flex items-center gap-2",
        children: [
          /* @__PURE__ */ jsx("input", {
            type: "checkbox",
            checked: !!limits.coalesce_reads,
            onChange: (e) => set("coalesce_reads", e.target.checked)
          }),
          "Share identical in-flight reads"
        ]
      }),
      /* @__PURE__ */ jsx("p", {
        className: "text-text-dim",
        children: "Sharing requires a published release and pure reads. HTTP and Function sources must declare read_only. Identities and permission scopes stay isolated."
      }),
      /* @__PURE__ */ jsxs("label", {
        className: "block space-y-1",
        children: [
          /* @__PURE__ */ jsx("span", {
            children: "Read consistency"
          }),
          /* @__PURE__ */ jsxs("select", {
            className: input,
            value: limits.read_consistency || "none",
            onChange: (e) => set("read_consistency", e.target.value),
            children: [
              /* @__PURE__ */ jsx("option", {
                value: "none",
                children: "Backend defaults"
              }),
              /* @__PURE__ */ jsx("option", {
                value: "batch",
                children: "Snapshot per batch"
              }),
              /* @__PURE__ */ jsx("option", {
                value: "request",
                children: "Snapshot per source for the request"
              })
            ]
          })
        ]
      }),
      /* @__PURE__ */ jsx("p", {
        className: "text-text-dim",
        children: "Tables supports batch snapshots. Request snapshots require a capable upstream adapter. Unsupported consistency returns an error."
      }),
      /* @__PURE__ */ jsx("div", {
        className: "grid grid-cols-2 gap-2",
        children: [["max_concurrent_requests", "Concurrent requests / API", 128, 1, 4096], ["max_concurrent_operations", "Concurrent executions / operation", 32, 1, 1024], ["max_queued_operations", "Queued executions", 128, 0, 1e4], ["max_queue_ms", "Queue timeout (ms)", 1000, 1, 300000], ["max_coalesced_waiters", "Shared callers / execution", 128, 1, 1e4], ["max_snapshot_ms", "Snapshot lifetime (ms)", 15000, 1, 300000]].map(([key, label, fallback, min, max]) => /* @__PURE__ */ jsxs("label", {
          className: "block space-y-1",
          children: [
            /* @__PURE__ */ jsx("span", {
              children: label
            }),
            /* @__PURE__ */ jsx("input", {
              className: input,
              type: "number",
              min: Number(min),
              max: Number(max),
              value: limits[String(key)] ?? fallback,
              onChange: (e) => set(String(key), Number(e.target.value))
            })
          ]
        }, String(key)))
      })
    ]
  });
}
export {
  RuntimeControls as default
};
