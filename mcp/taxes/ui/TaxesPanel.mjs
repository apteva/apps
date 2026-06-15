import { useEffect, useMemo, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/taxes";

function money(cents, currency = "EUR") {
  const value = Number(cents || 0) / 100;
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
    maximumFractionDigits: 0,
  }).format(value);
}

function statusClass(status) {
  if (status === "paid" || status === "filed") return "text-success";
  if (status === "waived") return "text-text-dim";
  if (status === "overdue") return "text-error";
  return "text-warn";
}

async function loadJSON(path) {
  const res = await fetch(`${API}${path}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`${res.status}`);
  return res.json();
}

export default function TaxesPanel() {
  const [profiles, setProfiles] = useState([]);
  const [obligations, setObligations] = useState([]);
  const [documents, setDocuments] = useState([]);
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);

  async function refresh() {
    setLoading(true);
    setStatus("");
    try {
      const [p, o, d] = await Promise.all([
        loadJSON("/profiles"),
        loadJSON("/obligations"),
        loadJSON("/documents"),
      ]);
      setProfiles(p.profiles || []);
      setObligations(o.obligations || []);
      setDocuments(d.documents || []);
      setStatus("updated");
    } catch (err) {
      setStatus(`load failed: ${err.message}`);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  const totals = useMemo(() => {
    let open = 0;
    let paid = 0;
    let currency = "EUR";
    for (const obligation of obligations) {
      currency = obligation.currency || currency;
      const amount = Number(obligation.amount_cents || 0);
      if (obligation.status === "paid" || obligation.status === "filed") paid += amount;
      else if (obligation.status !== "waived") open += amount;
    }
    return { open, paid, currency };
  }, [obligations]);

  return jsxs("div", {
    className: "h-full flex flex-col bg-bg text-text",
    children: [
      jsxs("header", {
        className: "flex items-center gap-3 border-b border-border px-4 py-2",
        children: [
          jsx("div", { className: "font-medium", children: "Taxes" }),
          jsx("button", {
            onClick: refresh,
            disabled: loading,
            className: "px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50",
            children: loading ? "Refreshing" : "Refresh",
          }),
          jsx("span", { className: "ml-auto text-xs text-text-dim", children: status }),
        ],
      }),
      jsxs("main", {
        className: "flex-1 overflow-auto p-4 flex flex-col gap-4",
        children: [
          jsxs("section", {
            className: "grid gap-3",
            style: { gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))" },
            children: [
              jsxs("div", {
                className: "border border-border rounded p-3",
                children: [
                  jsx("div", { className: "text-xs uppercase text-text-dim", children: "Open estimate" }),
                  jsx("div", { className: "text-xl font-semibold", children: money(totals.open, totals.currency) }),
                ],
              }),
              jsxs("div", {
                className: "border border-border rounded p-3",
                children: [
                  jsx("div", { className: "text-xs uppercase text-text-dim", children: "Paid or filed" }),
                  jsx("div", { className: "text-xl font-semibold", children: money(totals.paid, totals.currency) }),
                ],
              }),
              jsxs("div", {
                className: "border border-border rounded p-3",
                children: [
                  jsx("div", { className: "text-xs uppercase text-text-dim", children: "Profiles" }),
                  jsx("div", { className: "text-xl font-semibold", children: String(profiles.length) }),
                ],
              }),
            ],
          }),
          jsxs("section", {
            className: "border border-border rounded",
            children: [
              jsx("div", { className: "px-3 py-2 border-b border-border text-sm font-medium", children: "Obligations" }),
              obligations.length === 0
                ? jsx("div", { className: "p-4 text-sm text-text-muted", children: "No tax obligations yet." })
                : jsx("div", {
                    className: "divide-y divide-border",
                    children: obligations.slice(0, 12).map((o) =>
                      jsxs("div", {
                        className: "px-3 py-2 flex items-center gap-3 text-sm",
                        children: [
                          jsxs("div", {
                            className: "min-w-0 flex-1",
                            children: [
                              jsx("div", { className: "truncate", children: o.title || o.tax_type }),
                              jsxs("div", {
                                className: "text-xs text-text-dim truncate",
                                children: [o.tax_type, o.due_date ? ` due ${o.due_date}` : "", o.authority ? ` · ${o.authority}` : ""],
                              }),
                            ],
                          }),
                          jsx("div", { className: `text-xs ${statusClass(o.status)}`, children: o.status || "estimated" }),
                          jsx("div", { className: "font-medium tabular-nums", children: money(o.amount_cents, o.currency || totals.currency) }),
                        ],
                      }, o.id),
                    ),
                  }),
            ],
          }),
          jsxs("section", {
            className: "grid gap-4",
            style: { gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))" },
            children: [
              jsxs("div", {
                className: "border border-border rounded",
                children: [
                  jsx("div", { className: "px-3 py-2 border-b border-border text-sm font-medium", children: "Profiles" }),
                  profiles.length === 0
                    ? jsx("div", { className: "p-4 text-sm text-text-muted", children: "Create a profile with tax_profiles_create." })
                    : jsx("div", {
                        className: "divide-y divide-border",
                        children: profiles.map((p) =>
                          jsxs("div", {
                            className: "px-3 py-2 text-sm",
                            children: [
                              jsx("div", { children: p.name }),
                              jsxs("div", { className: "text-xs text-text-dim", children: [p.country, " · ", p.structure, " · ", p.filing_cadence] }),
                            ],
                          }, p.id),
                        ),
                      }),
                ],
              }),
              jsxs("div", {
                className: "border border-border rounded",
                children: [
                  jsx("div", { className: "px-3 py-2 border-b border-border text-sm font-medium", children: "Documents" }),
                  documents.length === 0
                    ? jsx("div", { className: "p-4 text-sm text-text-muted", children: "No reports generated yet." })
                    : jsx("div", {
                        className: "divide-y divide-border",
                        children: documents.slice(0, 8).map((d) =>
                          jsxs("div", {
                            className: "px-3 py-2 text-sm",
                            children: [
                              jsx("div", { children: d.title }),
                              jsxs("div", { className: "text-xs text-text-dim", children: [d.document_type, d.created_at ? ` · ${d.created_at}` : ""] }),
                            ],
                          }, d.id),
                        ),
                      }),
                ],
              }),
            ],
          }),
        ],
      }),
    ],
  });
}

