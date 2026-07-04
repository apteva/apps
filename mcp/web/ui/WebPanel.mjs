import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

const API = "/api/apps/web/runs";

function WebPanel() {
  const [runs, setRuns] = useState([]);
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      const res = await fetch(API, { credentials: "include" });
      const body = await res.json();
      if (!res.ok || body.error) throw new Error(body.error || `HTTP ${res.status}`);
      setRuns(body.runs || []);
    } catch (err) {
      setError(String(err?.message || err));
    }
  }

  useEffect(() => {
    load();
  }, []);

  return React.createElement("div", { style: { padding: 16, height: "100%", overflow: "auto" } },
    React.createElement("div", { style: { display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 16 } },
      React.createElement("div", null,
        React.createElement("h1", { style: { fontSize: 20, fontWeight: 600, margin: 0 } }, "Web"),
        React.createElement("p", { style: { margin: "2px 0 0", fontSize: 13, color: "var(--text-muted, #64748b)" } },
          `${runs.length} recent run${runs.length === 1 ? "" : "s"}`)
      ),
      React.createElement("button", {
        onClick: load,
        style: { padding: "6px 10px", borderRadius: 6, border: "1px solid var(--border, #d0d7de)", background: "var(--bg, white)", cursor: "pointer" }
      }, "Refresh")
    ),
    error && React.createElement("div", { style: { padding: 12, border: "1px solid #fecaca", color: "#991b1b", borderRadius: 6, marginBottom: 12 } }, error),
    React.createElement("div", { style: { display: "grid", gap: 8 } },
      runs.map((run) => React.createElement("div", {
        key: run.id,
        style: { border: "1px solid var(--border, #d0d7de)", borderRadius: 8, padding: 12, background: "var(--bg, white)" }
      },
        React.createElement("div", { style: { display: "flex", justifyContent: "space-between", gap: 12 } },
          React.createElement("strong", null, run.kind),
          React.createElement("span", { style: { fontSize: 12, color: "var(--text-muted, #64748b)" } }, run.status)
        ),
        React.createElement("div", { style: { fontSize: 12, color: "var(--text-muted, #64748b)", marginTop: 4 } }, run.created_at),
        run.summary && React.createElement("div", { style: { fontSize: 13, marginTop: 8 } }, run.summary)
      ))
    )
  );
}

export default WebPanel;

if (typeof document !== "undefined") {
  const el = document.getElementById("root");
  if (el) createRoot(el).render(React.createElement(WebPanel));
}
