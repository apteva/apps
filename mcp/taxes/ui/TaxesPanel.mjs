import { useCallback, useEffect, useMemo, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/taxes";
const TABS = ["Overview", "Profiles", "Rules", "Periods", "Estimates", "Obligations", "Payments", "Adjustments", "Reports", "Sync", "Tools"];
const TAX_TYPES = ["vat", "income_tax", "corporate_tax", "social_contributions"];
const STRUCTURES = ["ES_AUTONOMO", "ES_SL", "FR_SAS", "FR_SASU", "FR_SARL", "FR_EURL"];
const CADENCES = ["monthly", "quarterly", "annual"];

function today() {
  return new Date().toISOString().slice(0, 10);
}

function yearStart() {
  return `${new Date().getFullYear()}-01-01`;
}

function yearEnd() {
  return `${new Date().getFullYear()}-12-31`;
}

function money(cents, currency = "EUR") {
  const value = Number(cents || 0) / 100;
  return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 0 }).format(value);
}

function cents(value) {
  if (value === "" || value == null) return undefined;
  return Math.round(Number(value) * 100);
}

function compact(input) {
  const out = {};
  for (const [key, value] of Object.entries(input)) {
    if (value === "" || value == null) continue;
    out[key] = value;
  }
  return out;
}

function statusClass(status) {
  if (status === "paid" || status === "filed" || status === "closed") return "text-success";
  if (status === "waived" || status === "cancelled") return "text-text-dim";
  if (status === "overdue") return "text-error";
  return "text-warn";
}

async function callTool(name, args = {}) {
  const res = await fetch(`${API}/tools/${name}`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(args),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `${res.status}`);
  return body;
}

async function loadTools() {
  const res = await fetch(`${API}/tools/`, { credentials: "same-origin" });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `${res.status}`);
  return body.tools || [];
}

function TextInput({ label, value, onChange, type = "text", placeholder = "", className = "" }) {
  return jsxs("label", {
    className: `flex flex-col gap-1 text-xs text-text-dim ${className}`,
    children: [
      label,
      jsx("input", {
        type,
        value,
        placeholder,
        onChange: (event) => onChange(event.target.value),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text",
      }),
    ],
  });
}

function SelectInput({ label, value, onChange, options, className = "" }) {
  return jsxs("label", {
    className: `flex flex-col gap-1 text-xs text-text-dim ${className}`,
    children: [
      label,
      jsx("select", {
        value,
        onChange: (event) => onChange(event.target.value),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text",
        children: options.map((option) => jsx("option", { value: option, children: option || "any" }, option || "any")),
      }),
    ],
  });
}

function CheckInput({ label, checked, onChange }) {
  return jsxs("label", {
    className: "flex items-center gap-2 text-sm text-text-muted",
    children: [
      jsx("input", { type: "checkbox", checked, onChange: (event) => onChange(event.target.checked) }),
      label,
    ],
  });
}

function Button({ children, onClick, disabled = false, tone = "default" }) {
  const cls = tone === "primary"
    ? "bg-accent text-bg border-accent font-semibold"
    : tone === "danger"
      ? "border-error text-error"
      : "border-border text-text-muted hover:text-text";
  return jsx("button", {
    onClick,
    disabled,
    className: `px-3 py-1.5 text-sm border rounded disabled:opacity-50 ${cls}`,
    children,
  });
}

function Panel({ title, actions, children }) {
  return jsxs("section", {
    className: "border border-border rounded overflow-hidden",
    children: [
      jsxs("div", {
        className: "px-3 py-2 border-b border-border flex items-center gap-2",
        children: [
          jsx("div", { className: "text-sm font-medium", children: title }),
          actions && jsx("div", { className: "ml-auto flex items-center gap-2", children: actions }),
        ],
      }),
      jsx("div", { className: "p-3", children }),
    ],
  });
}

function Empty({ children }) {
  return jsx("div", { className: "py-8 text-center text-sm text-text-muted", children });
}

function Table({ columns, rows, onSelect, selectedId }) {
  if (!rows.length) return jsx(Empty, { children: "No rows yet." });
  return jsx("div", {
    className: "overflow-auto",
    children: jsxs("table", {
      className: "w-full text-sm",
      children: [
        jsx("thead", {
          className: "text-xs text-text-dim",
          children: jsx("tr", {
            children: columns.map((column) => jsx("th", { className: "text-left font-normal py-1.5 pr-3", children: column.label }, column.key)),
          }),
        }),
        jsx("tbody", {
          className: "divide-y divide-border",
          children: rows.map((row) =>
            jsx("tr", {
              onClick: onSelect ? () => onSelect(row) : undefined,
              className: `${onSelect ? "cursor-pointer hover:bg-bg-hover" : ""} ${selectedId === row.id ? "bg-bg-hover" : ""}`,
              children: columns.map((column) => jsx("td", { className: "py-2 pr-3 align-top", children: column.render ? column.render(row) : row[column.key] }, column.key)),
            }, row.id || JSON.stringify(row)),
          ),
        }),
      ],
    }),
  });
}

function JSONBlock({ value }) {
  if (!value) return jsx(Empty, { children: "No result selected." });
  return jsx("pre", {
    className: "bg-bg-input border border-border rounded p-3 text-xs overflow-auto",
    style: { maxHeight: "360px" },
    children: JSON.stringify(value, null, 2),
  });
}

function firstProfileId(profiles) {
  return profiles[0] ? String(profiles[0].id) : "";
}

export default function TaxesPanel() {
  const [tab, setTab] = useState("Overview");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [profiles, setProfiles] = useState([]);
  const [rules, setRules] = useState([]);
  const [periods, setPeriods] = useState([]);
  const [obligations, setObligations] = useState([]);
  const [payments, setPayments] = useState([]);
  const [adjustments, setAdjustments] = useState([]);
  const [documents, setDocuments] = useState([]);
  const [tools, setTools] = useState([]);
  const [result, setResult] = useState(null);

  const [profileForm, setProfileForm] = useState({ id: "", name: "", country: "ES", structure: "ES_AUTONOMO", region: "", fiscal_year_start: "01-01", fiscal_year_end: "12-31", vat_registered: true, filing_cadence: "quarterly", accounting_basis: "accrual", currency: "EUR", auto_open_periods: true });
  const [periodForm, setPeriodForm] = useState({ id: "", profile_id: "", tax_type: "vat", period_start: yearStart(), period_end: yearEnd(), due_date: "" });
  const [estimateForm, setEstimateForm] = useState({ profile_id: "", period_id: "", tax_type: "all", period_start: yearStart(), period_end: yearEnd(), due_date: "", revenue: "", expenses: "", output_tax: "", input_tax: "", taxable_profit: "", social_contribution: "", months: "", sync_sources: false, create_obligation: true });
  const [obligationForm, setObligationForm] = useState({ id: "", profile_id: "", period_id: "", tax_type: "vat", title: "", amount: "", currency: "EUR", due_date: "", authority: "", status: "estimated" });
  const [paymentForm, setPaymentForm] = useState({ obligation_id: "", amount: "", paid_at: today(), method: "", reference: "", notes: "", bills_bill_id: "", bills_payment_id: "" });
  const [adjustmentForm, setAdjustmentForm] = useState({ id: "", profile_id: "", period_id: "", tax_type: "vat", kind: "manual", amount: "", currency: "EUR", reason: "" });
  const [reportForm, setReportForm] = useState({ profile_id: "", period_id: "", period_start: yearStart(), period_end: yearEnd() });
  const [syncForm, setSyncForm] = useState({ profile_id: "", period_start: yearStart(), period_end: yearEnd(), source: "all" });
  const [toolForm, setToolForm] = useState({ name: "tax_profiles_list", body: "{}" });

  const refresh = useCallback(async () => {
    setLoading(true);
    setStatus("");
    try {
      const [p, r, pe, o, pa, a, d, t] = await Promise.all([
        callTool("tax_profiles_list", {}),
        callTool("tax_rules_list", {}),
        callTool("tax_periods_list", {}),
        callTool("tax_obligations_list", {}),
        callTool("tax_payments_list", {}),
        callTool("tax_adjustments_list", {}),
        callTool("tax_documents_list", {}),
        loadTools(),
      ]);
      setProfiles(p.profiles || []);
      setRules(r.rules || []);
      setPeriods(pe.periods || []);
      setObligations(o.obligations || []);
      setPayments(pa.payments || []);
      setAdjustments(a.adjustments || []);
      setDocuments(d.documents || []);
      setTools(t);
      const pid = firstProfileId(p.profiles || []);
      if (pid) {
        setPeriodForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
        setEstimateForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
        setObligationForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
        setAdjustmentForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
        setReportForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
        setSyncForm((f) => ({ ...f, profile_id: f.profile_id || pid }));
      }
      setStatus("updated");
    } catch (err) {
      setStatus(`load failed: ${err.message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  async function run(name, args = {}, after = true) {
    setLoading(true);
    setStatus(name);
    try {
      const out = await callTool(name, args);
      setResult(out);
      setStatus(`${name} ok`);
      if (after) await refresh();
      return out;
    } catch (err) {
      setStatus(`${name}: ${err.message}`);
      setResult({ error: err.message });
      return null;
    } finally {
      setLoading(false);
    }
  }

  const totals = useMemo(() => {
    let open = 0, paid = 0, filed = 0, currency = "EUR";
    for (const obligation of obligations) {
      currency = obligation.currency || currency;
      const amount = Number(obligation.amount_cents || 0);
      if (obligation.status === "paid") paid += amount;
      else if (obligation.status === "filed") filed += amount;
      else if (obligation.status !== "waived") open += amount;
    }
    return { open, paid, filed, currency };
  }, [obligations]);

  const profileOptions = ["", ...profiles.map((p) => String(p.id))];
  const periodOptions = ["", ...periods.map((p) => String(p.id))];
  const obligationOptions = ["", ...obligations.map((o) => String(o.id))];

  function selectProfile(profile) {
    setProfileForm({
      id: String(profile.id),
      name: profile.name || "",
      country: profile.country || "ES",
      structure: profile.structure || "ES_AUTONOMO",
      region: profile.region || "",
      fiscal_year_start: profile.fiscal_year_start || "01-01",
      fiscal_year_end: profile.fiscal_year_end || "12-31",
      vat_registered: !!profile.vat_registered,
      filing_cadence: profile.filing_cadence || "quarterly",
      accounting_basis: profile.accounting_basis || "accrual",
      currency: profile.currency || "EUR",
      auto_open_periods: false,
    });
  }

  function profilePayload() {
    return compact({ ...profileForm, id: profileForm.id ? Number(profileForm.id) : undefined, vat_registered: profileForm.vat_registered, auto_open_periods: profileForm.auto_open_periods });
  }

  function estimatePayload() {
    return compact({
      profile_id: Number(estimateForm.profile_id),
      period_id: estimateForm.period_id ? Number(estimateForm.period_id) : undefined,
      period_start: estimateForm.period_start,
      period_end: estimateForm.period_end,
      due_date: estimateForm.due_date,
      revenue_cents: cents(estimateForm.revenue),
      expenses_cents: cents(estimateForm.expenses),
      output_tax_cents: cents(estimateForm.output_tax),
      input_tax_cents: cents(estimateForm.input_tax),
      taxable_profit_cents: cents(estimateForm.taxable_profit),
      social_contribution_cents: cents(estimateForm.social_contribution),
      months: estimateForm.months ? Number(estimateForm.months) : undefined,
      sync_sources: estimateForm.sync_sources,
      create_obligation: estimateForm.create_obligation,
    });
  }

  function obligationPayload() {
    return compact({
      id: obligationForm.id ? Number(obligationForm.id) : undefined,
      profile_id: Number(obligationForm.profile_id),
      period_id: obligationForm.period_id ? Number(obligationForm.period_id) : undefined,
      tax_type: obligationForm.tax_type,
      title: obligationForm.title,
      amount_cents: cents(obligationForm.amount),
      currency: obligationForm.currency,
      due_date: obligationForm.due_date,
      authority: obligationForm.authority,
      status: obligationForm.status,
    });
  }

  function renderOverview() {
    return jsxs("div", { className: "flex flex-col gap-4", children: [
      jsxs("section", { className: "grid gap-3", style: { gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))" }, children: [
        metric("Open", money(totals.open, totals.currency)),
        metric("Paid", money(totals.paid, totals.currency)),
        metric("Filed", money(totals.filed, totals.currency)),
        metric("Profiles", profiles.length),
        metric("Periods", periods.length),
        metric("Rules", rules.length),
      ] }),
      jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "minmax(0, 1.3fr) minmax(300px, .7fr)" }, children: [
        jsx(Panel, { title: "Upcoming obligations", children: jsx(Table, { columns: obligationColumns(), rows: obligations.slice(0, 10), onSelect: (o) => { setTab("Obligations"); selectObligation(o); } }) }),
        jsx(Panel, { title: "Recent reports", children: jsx(Table, { columns: [{ key: "title", label: "Title" }, { key: "document_type", label: "Type" }, { key: "created_at", label: "Created" }], rows: documents.slice(0, 8) }) }),
      ] }),
      jsx(Panel, { title: "Last result", children: jsx(JSONBlock, { value: result }) }),
    ] });
  }

  function metric(label, value) {
    return jsxs("div", { className: "border border-border rounded p-3", children: [
      jsx("div", { className: "text-xs uppercase text-text-dim", children: label }),
      jsx("div", { className: "text-xl font-semibold", children: String(value) }),
    ] }, label);
  }

  function renderProfiles() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: profileForm.id ? "Edit profile" : "Create profile", actions: jsx(Button, { onClick: () => setProfileForm({ id: "", name: "", country: "ES", structure: "ES_AUTONOMO", region: "", fiscal_year_start: "01-01", fiscal_year_end: "12-31", vat_registered: true, filing_cadence: "quarterly", accounting_basis: "accrual", currency: "EUR", auto_open_periods: true }), children: "New" }), children:
        jsxs("div", { className: "flex flex-col gap-3", children: [
          jsx(TextInput, { label: "Name", value: profileForm.name, onChange: (v) => setProfileForm({ ...profileForm, name: v }) }),
          jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
            jsx(SelectInput, { label: "Country", value: profileForm.country, onChange: (v) => setProfileForm({ ...profileForm, country: v }), options: ["ES", "FR"] }),
            jsx(SelectInput, { label: "Structure", value: profileForm.structure, onChange: (v) => setProfileForm({ ...profileForm, structure: v }), options: STRUCTURES }),
          ] }),
          jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
            jsx(TextInput, { label: "Region", value: profileForm.region, onChange: (v) => setProfileForm({ ...profileForm, region: v }) }),
            jsx(TextInput, { label: "Currency", value: profileForm.currency, onChange: (v) => setProfileForm({ ...profileForm, currency: v }) }),
          ] }),
          jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
            jsx(SelectInput, { label: "Filing cadence", value: profileForm.filing_cadence, onChange: (v) => setProfileForm({ ...profileForm, filing_cadence: v }), options: CADENCES }),
            jsx(SelectInput, { label: "Accounting basis", value: profileForm.accounting_basis, onChange: (v) => setProfileForm({ ...profileForm, accounting_basis: v }), options: ["accrual", "cash"] }),
          ] }),
          jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
            jsx(TextInput, { label: "Fiscal year start", value: profileForm.fiscal_year_start, onChange: (v) => setProfileForm({ ...profileForm, fiscal_year_start: v }) }),
            jsx(TextInput, { label: "Fiscal year end", value: profileForm.fiscal_year_end, onChange: (v) => setProfileForm({ ...profileForm, fiscal_year_end: v }) }),
          ] }),
          jsx(CheckInput, { label: "VAT registered", checked: profileForm.vat_registered, onChange: (v) => setProfileForm({ ...profileForm, vat_registered: v }) }),
          !profileForm.id && jsx(CheckInput, { label: "Auto-open standard periods", checked: profileForm.auto_open_periods, onChange: (v) => setProfileForm({ ...profileForm, auto_open_periods: v }) }),
          jsxs("div", { className: "flex flex-wrap gap-2", children: [
            jsx(Button, { tone: "primary", disabled: !profileForm.name, onClick: () => run(profileForm.id ? "tax_profiles_update" : "tax_profiles_create", profilePayload()), children: profileForm.id ? "Save profile" : "Create profile" }),
            jsx(Button, { disabled: !profileForm.id, onClick: () => run("tax_periods_generate", { profile_id: Number(profileForm.id), year: new Date().getFullYear() }), children: "Generate periods" }),
          ] }),
        ] }),
      }),
      jsx(Panel, { title: "Profiles", children: jsx(Table, { columns: [{ key: "name", label: "Name" }, { key: "country", label: "Country" }, { key: "structure", label: "Structure" }, { key: "filing_cadence", label: "Cadence" }, { key: "currency", label: "Currency" }], rows: profiles, onSelect: selectProfile, selectedId: Number(profileForm.id) }) }),
    ] });
  }

  function renderRules() {
    return jsx(Panel, { title: "Rule catalog", children: jsx(Table, { columns: [
      { key: "country", label: "Country" }, { key: "structure", label: "Structure" }, { key: "tax_type", label: "Tax type" }, { key: "year", label: "Year" }, { key: "version", label: "Version" }, { key: "source_url", label: "Source", render: (r) => r.source_url ? jsx("a", { className: "text-accent", href: r.source_url, target: "_blank", children: "open" }) : "" },
    ], rows: rules, onSelect: (rule) => run("tax_rules_get", { id: rule.id }, false) }) });
  }

  function renderPeriods() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Open period", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: periodForm.profile_id, onChange: (v) => setPeriodForm({ ...periodForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Tax type", value: periodForm.tax_type, onChange: (v) => setPeriodForm({ ...periodForm, tax_type: v }), options: TAX_TYPES }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Start", type: "date", value: periodForm.period_start, onChange: (v) => setPeriodForm({ ...periodForm, period_start: v }) }),
          jsx(TextInput, { label: "End", type: "date", value: periodForm.period_end, onChange: (v) => setPeriodForm({ ...periodForm, period_end: v }) }),
        ] }),
        jsx(TextInput, { label: "Due date", type: "date", value: periodForm.due_date, onChange: (v) => setPeriodForm({ ...periodForm, due_date: v }) }),
        jsx(Button, { tone: "primary", disabled: !periodForm.profile_id, onClick: () => run("tax_periods_open", { ...periodForm, profile_id: Number(periodForm.profile_id) }), children: "Open custom period" }),
        jsx(Button, { disabled: !periodForm.profile_id, onClick: () => run("tax_periods_generate", { profile_id: Number(periodForm.profile_id), year: new Date().getFullYear() }), children: "Generate standard periods" }),
        jsx(Button, { disabled: !periodForm.id, onClick: () => run("tax_periods_close", { id: Number(periodForm.id), status: "closed" }), children: "Close selected" }),
      ] }) }),
      jsx(Panel, { title: "Periods", children: jsx(Table, { columns: [{ key: "tax_type", label: "Tax type" }, { key: "period_start", label: "Start" }, { key: "period_end", label: "End" }, { key: "due_date", label: "Due" }, { key: "status", label: "Status", render: (p) => jsx("span", { className: statusClass(p.status), children: p.status }) }], rows: periods, onSelect: (p) => setPeriodForm({ ...periodForm, id: String(p.id), profile_id: String(p.profile_id), tax_type: p.tax_type, period_start: p.period_start, period_end: p.period_end, due_date: p.due_date || "" }), selectedId: Number(periodForm.id) }) }),
    ] });
  }

  function renderEstimates() {
    const tool = estimateForm.tax_type === "all" ? "tax_estimate_all" : `tax_estimate_${estimateForm.tax_type}`;
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "380px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Run estimate", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: estimateForm.profile_id, onChange: (v) => setEstimateForm({ ...estimateForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Tax type", value: estimateForm.tax_type, onChange: (v) => setEstimateForm({ ...estimateForm, tax_type: v }), options: ["all", ...TAX_TYPES] }),
        jsx(SelectInput, { label: "Period row", value: estimateForm.period_id, onChange: (v) => {
          const p = periods.find((row) => String(row.id) === v);
          setEstimateForm({ ...estimateForm, period_id: v, period_start: p ? p.period_start : estimateForm.period_start, period_end: p ? p.period_end : estimateForm.period_end });
        }, options: periodOptions }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Start", type: "date", value: estimateForm.period_start, onChange: (v) => setEstimateForm({ ...estimateForm, period_start: v }) }),
          jsx(TextInput, { label: "End", type: "date", value: estimateForm.period_end, onChange: (v) => setEstimateForm({ ...estimateForm, period_end: v }) }),
        ] }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Revenue", type: "number", value: estimateForm.revenue, onChange: (v) => setEstimateForm({ ...estimateForm, revenue: v }) }),
          jsx(TextInput, { label: "Expenses", type: "number", value: estimateForm.expenses, onChange: (v) => setEstimateForm({ ...estimateForm, expenses: v }) }),
          jsx(TextInput, { label: "Output tax", type: "number", value: estimateForm.output_tax, onChange: (v) => setEstimateForm({ ...estimateForm, output_tax: v }) }),
          jsx(TextInput, { label: "Input tax", type: "number", value: estimateForm.input_tax, onChange: (v) => setEstimateForm({ ...estimateForm, input_tax: v }) }),
          jsx(TextInput, { label: "Taxable profit", type: "number", value: estimateForm.taxable_profit, onChange: (v) => setEstimateForm({ ...estimateForm, taxable_profit: v }) }),
          jsx(TextInput, { label: "Monthly social", type: "number", value: estimateForm.social_contribution, onChange: (v) => setEstimateForm({ ...estimateForm, social_contribution: v }) }),
        ] }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Months", type: "number", value: estimateForm.months, onChange: (v) => setEstimateForm({ ...estimateForm, months: v }) }),
          jsx(TextInput, { label: "Due date", type: "date", value: estimateForm.due_date, onChange: (v) => setEstimateForm({ ...estimateForm, due_date: v }) }),
        ] }),
        jsx(CheckInput, { label: "Sync billing/bills sources", checked: estimateForm.sync_sources, onChange: (v) => setEstimateForm({ ...estimateForm, sync_sources: v }) }),
        jsx(CheckInput, { label: "Create/update obligation", checked: estimateForm.create_obligation, onChange: (v) => setEstimateForm({ ...estimateForm, create_obligation: v }) }),
        jsx(Button, { tone: "primary", disabled: !estimateForm.profile_id, onClick: () => run(tool, estimatePayload()), children: "Run estimate" }),
      ] }) }),
      jsx(Panel, { title: "Estimate result", children: jsx(JSONBlock, { value: result }) }),
    ] });
  }

  function selectObligation(o) {
    setObligationForm({ id: String(o.id), profile_id: String(o.profile_id), period_id: o.period_id ? String(o.period_id) : "", tax_type: o.tax_type || "vat", title: o.title || "", amount: String(Number(o.amount_cents || 0) / 100), currency: o.currency || "EUR", due_date: o.due_date || "", authority: o.authority || "", status: o.status || "estimated" });
    setPaymentForm((f) => ({ ...f, obligation_id: String(o.id), amount: String(Number(o.amount_cents || 0) / 100) }));
  }

  function obligationColumns() {
    return [
      { key: "title", label: "Title" },
      { key: "tax_type", label: "Tax" },
      { key: "due_date", label: "Due" },
      { key: "status", label: "Status", render: (o) => jsx("span", { className: statusClass(o.status), children: o.status }) },
      { key: "amount_cents", label: "Amount", render: (o) => jsx("span", { className: "tabular-nums font-medium", children: money(o.amount_cents, o.currency) }) },
    ];
  }

  function renderObligations() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "380px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: obligationForm.id ? "Edit obligation" : "Create obligation", actions: jsx(Button, { onClick: () => setObligationForm({ id: "", profile_id: firstProfileId(profiles), period_id: "", tax_type: "vat", title: "", amount: "", currency: "EUR", due_date: "", authority: "", status: "estimated" }), children: "New" }), children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: obligationForm.profile_id, onChange: (v) => setObligationForm({ ...obligationForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Period", value: obligationForm.period_id, onChange: (v) => setObligationForm({ ...obligationForm, period_id: v }), options: periodOptions }),
        jsx(SelectInput, { label: "Tax type", value: obligationForm.tax_type, onChange: (v) => setObligationForm({ ...obligationForm, tax_type: v }), options: TAX_TYPES }),
        jsx(TextInput, { label: "Title", value: obligationForm.title, onChange: (v) => setObligationForm({ ...obligationForm, title: v }) }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Amount", type: "number", value: obligationForm.amount, onChange: (v) => setObligationForm({ ...obligationForm, amount: v }) }),
          jsx(TextInput, { label: "Currency", value: obligationForm.currency, onChange: (v) => setObligationForm({ ...obligationForm, currency: v }) }),
        ] }),
        jsx(TextInput, { label: "Due date", type: "date", value: obligationForm.due_date, onChange: (v) => setObligationForm({ ...obligationForm, due_date: v }) }),
        jsx(TextInput, { label: "Authority", value: obligationForm.authority, onChange: (v) => setObligationForm({ ...obligationForm, authority: v }) }),
        jsx(SelectInput, { label: "Status", value: obligationForm.status, onChange: (v) => setObligationForm({ ...obligationForm, status: v }), options: ["estimated", "draft", "filed", "paid", "waived"] }),
        jsxs("div", { className: "flex flex-wrap gap-2", children: [
          jsx(Button, { tone: "primary", disabled: !obligationForm.profile_id, onClick: () => run(obligationForm.id ? "tax_obligations_update" : "tax_obligations_create", obligationPayload()), children: obligationForm.id ? "Save" : "Create" }),
          jsx(Button, { disabled: !obligationForm.id, onClick: () => run("tax_obligations_mark_filed", { id: Number(obligationForm.id), filed_at: new Date().toISOString() }), children: "Mark filed" }),
          jsx(Button, { disabled: !obligationForm.id, onClick: () => run("tax_obligations_mark_waived", { id: Number(obligationForm.id), reason: "waived from panel" }), children: "Waive" }),
        ] }),
      ] }) }),
      jsx(Panel, { title: "Obligations", children: jsx(Table, { columns: obligationColumns(), rows: obligations, onSelect: selectObligation, selectedId: Number(obligationForm.id) }) }),
    ] });
  }

  function renderPayments() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Record/link payment", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Obligation", value: paymentForm.obligation_id, onChange: (v) => setPaymentForm({ ...paymentForm, obligation_id: v }), options: obligationOptions }),
        jsx(TextInput, { label: "Amount", type: "number", value: paymentForm.amount, onChange: (v) => setPaymentForm({ ...paymentForm, amount: v }) }),
        jsx(TextInput, { label: "Paid at", type: "date", value: paymentForm.paid_at, onChange: (v) => setPaymentForm({ ...paymentForm, paid_at: v }) }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Method", value: paymentForm.method, onChange: (v) => setPaymentForm({ ...paymentForm, method: v }) }),
          jsx(TextInput, { label: "Reference", value: paymentForm.reference, onChange: (v) => setPaymentForm({ ...paymentForm, reference: v }) }),
          jsx(TextInput, { label: "Bills bill id", value: paymentForm.bills_bill_id, onChange: (v) => setPaymentForm({ ...paymentForm, bills_bill_id: v }) }),
          jsx(TextInput, { label: "Bills payment id", value: paymentForm.bills_payment_id, onChange: (v) => setPaymentForm({ ...paymentForm, bills_payment_id: v }) }),
        ] }),
        jsx(TextInput, { label: "Notes", value: paymentForm.notes, onChange: (v) => setPaymentForm({ ...paymentForm, notes: v }) }),
        jsxs("div", { className: "flex flex-wrap gap-2", children: [
          jsx(Button, { tone: "primary", disabled: !paymentForm.obligation_id, onClick: () => run("tax_payments_record", compact({ obligation_id: Number(paymentForm.obligation_id), amount_cents: cents(paymentForm.amount), paid_at: paymentForm.paid_at, method: paymentForm.method, reference: paymentForm.reference, notes: paymentForm.notes, bills_bill_id: paymentForm.bills_bill_id ? Number(paymentForm.bills_bill_id) : undefined, bills_payment_id: paymentForm.bills_payment_id ? Number(paymentForm.bills_payment_id) : undefined })), children: "Record payment" }),
          jsx(Button, { disabled: !paymentForm.obligation_id, onClick: () => run("tax_payments_link_bill", compact({ obligation_id: Number(paymentForm.obligation_id), amount_cents: cents(paymentForm.amount), paid_at: paymentForm.paid_at, bills_bill_id: paymentForm.bills_bill_id ? Number(paymentForm.bills_bill_id) : undefined, bills_payment_id: paymentForm.bills_payment_id ? Number(paymentForm.bills_payment_id) : undefined })), children: "Link bills row" }),
          jsx(Button, { disabled: !paymentForm.obligation_id, onClick: () => run("tax_payments_create_bill", { obligation_id: Number(paymentForm.obligation_id) }), children: "Create bill" }),
        ] }),
      ] }) }),
      jsx(Panel, { title: "Payments", children: jsx(Table, { columns: [{ key: "obligation_id", label: "Obligation" }, { key: "paid_at", label: "Paid" }, { key: "method", label: "Method" }, { key: "reference", label: "Reference" }, { key: "amount_cents", label: "Amount", render: (p) => money(p.amount_cents, p.currency) }], rows: payments }) }),
    ] });
  }

  function renderAdjustments() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Create adjustment", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: adjustmentForm.profile_id, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Period", value: adjustmentForm.period_id, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, period_id: v }), options: periodOptions }),
        jsx(SelectInput, { label: "Tax type", value: adjustmentForm.tax_type, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, tax_type: v }), options: TAX_TYPES }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Kind", value: adjustmentForm.kind, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, kind: v }) }),
          jsx(TextInput, { label: "Amount", type: "number", value: adjustmentForm.amount, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, amount: v }) }),
        ] }),
        jsx(TextInput, { label: "Reason", value: adjustmentForm.reason, onChange: (v) => setAdjustmentForm({ ...adjustmentForm, reason: v }) }),
        jsx(Button, { tone: "primary", disabled: !adjustmentForm.profile_id || !adjustmentForm.reason, onClick: () => run("tax_adjustments_create", compact({ profile_id: Number(adjustmentForm.profile_id), period_id: adjustmentForm.period_id ? Number(adjustmentForm.period_id) : undefined, tax_type: adjustmentForm.tax_type, kind: adjustmentForm.kind, amount_cents: cents(adjustmentForm.amount), currency: adjustmentForm.currency, reason: adjustmentForm.reason })), children: "Create adjustment" }),
      ] }) }),
      jsx(Panel, { title: "Adjustments", children: jsx(Table, { columns: [{ key: "tax_type", label: "Tax" }, { key: "kind", label: "Kind" }, { key: "reason", label: "Reason" }, { key: "status", label: "Status" }, { key: "amount_cents", label: "Amount", render: (a) => money(a.amount_cents, a.currency) }, { key: "delete", label: "", render: (a) => jsx(Button, { tone: "danger", onClick: () => run("tax_adjustments_delete", { id: Number(a.id) }), children: "Delete" }) }], rows: adjustments }) }),
    ] });
  }

  function renderReports() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Generate report", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: reportForm.profile_id, onChange: (v) => setReportForm({ ...reportForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Period", value: reportForm.period_id, onChange: (v) => setReportForm({ ...reportForm, period_id: v }), options: periodOptions }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Start", type: "date", value: reportForm.period_start, onChange: (v) => setReportForm({ ...reportForm, period_start: v }) }),
          jsx(TextInput, { label: "End", type: "date", value: reportForm.period_end, onChange: (v) => setReportForm({ ...reportForm, period_end: v }) }),
        ] }),
        jsx(Button, { tone: "primary", disabled: !reportForm.profile_id, onClick: () => run("tax_report_generate", compact({ profile_id: Number(reportForm.profile_id), period_id: reportForm.period_id ? Number(reportForm.period_id) : undefined, period_start: reportForm.period_start, period_end: reportForm.period_end })), children: "Generate report" }),
      ] }) }),
      jsx(Panel, { title: "Documents", children: jsx(Table, { columns: [{ key: "title", label: "Title" }, { key: "document_type", label: "Type" }, { key: "created_at", label: "Created" }], rows: documents, onSelect: (d) => run("tax_documents_get", { id: Number(d.id) }, false) }) }),
    ] });
  }

  function renderSync() {
    const tool = syncForm.source === "billing" ? "tax_sync_from_billing" : syncForm.source === "bills" ? "tax_sync_from_bills" : "tax_sync_all";
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "360px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Source sync", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Profile", value: syncForm.profile_id, onChange: (v) => setSyncForm({ ...syncForm, profile_id: v }), options: profileOptions }),
        jsx(SelectInput, { label: "Source", value: syncForm.source, onChange: (v) => setSyncForm({ ...syncForm, source: v }), options: ["all", "billing", "bills"] }),
        jsxs("div", { className: "grid grid-cols-2 gap-2", children: [
          jsx(TextInput, { label: "Start", type: "date", value: syncForm.period_start, onChange: (v) => setSyncForm({ ...syncForm, period_start: v }) }),
          jsx(TextInput, { label: "End", type: "date", value: syncForm.period_end, onChange: (v) => setSyncForm({ ...syncForm, period_end: v }) }),
        ] }),
        jsx(Button, { tone: "primary", disabled: !syncForm.profile_id, onClick: () => run(tool, { profile_id: Number(syncForm.profile_id), period_start: syncForm.period_start, period_end: syncForm.period_end }, false), children: "Run sync" }),
      ] }) }),
      jsx(Panel, { title: "Sync result", children: jsx(JSONBlock, { value: result }) }),
    ] });
  }

  function renderTools() {
    return jsxs("div", { className: "grid gap-4", style: { gridTemplateColumns: "420px minmax(0, 1fr)" }, children: [
      jsx(Panel, { title: "Tool console", children: jsxs("div", { className: "flex flex-col gap-3", children: [
        jsx(SelectInput, { label: "Tool", value: toolForm.name, onChange: (v) => setToolForm({ ...toolForm, name: v }), options: tools.map((tool) => tool.name) }),
        jsxs("label", { className: "flex flex-col gap-1 text-xs text-text-dim", children: [
          "JSON arguments",
          jsx("textarea", { value: toolForm.body, onChange: (event) => setToolForm({ ...toolForm, body: event.target.value }), className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono", style: { minHeight: "180px" } }),
        ] }),
        jsx(Button, { tone: "primary", onClick: () => {
          let args = {};
          try { args = JSON.parse(toolForm.body || "{}"); } catch (err) { setResult({ error: err.message }); return; }
          run(toolForm.name, args);
        }, children: "Run tool" }),
      ] }) }),
      jsx(Panel, { title: "All MCP tools", children: jsx(Table, { columns: [{ key: "name", label: "Name" }, { key: "description", label: "Description" }], rows: tools.map((tool, i) => ({ ...tool, id: i + 1 })), onSelect: (tool) => setToolForm({ name: tool.name, body: "{}" }) }) }),
      jsx(Panel, { title: "Result", children: jsx(JSONBlock, { value: result }) }),
    ] });
  }

  function body() {
    if (tab === "Overview") return renderOverview();
    if (tab === "Profiles") return renderProfiles();
    if (tab === "Rules") return renderRules();
    if (tab === "Periods") return renderPeriods();
    if (tab === "Estimates") return renderEstimates();
    if (tab === "Obligations") return renderObligations();
    if (tab === "Payments") return renderPayments();
    if (tab === "Adjustments") return renderAdjustments();
    if (tab === "Reports") return renderReports();
    if (tab === "Sync") return renderSync();
    return renderTools();
  }

  return jsxs("div", { className: "h-full flex flex-col bg-bg text-text", children: [
    jsxs("header", { className: "border-b border-border", children: [
      jsxs("div", { className: "flex items-center gap-3 px-4 py-2", children: [
        jsx("div", { className: "font-medium", children: "Taxes" }),
        jsx(Button, { onClick: refresh, disabled: loading, children: loading ? "Working" : "Refresh" }),
        jsx("span", { className: "ml-auto text-xs text-text-dim truncate", children: status }),
      ] }),
      jsx("nav", { className: "flex gap-1 overflow-x-auto px-3 pb-2", children: TABS.map((name) => jsx("button", { onClick: () => setTab(name), className: `px-3 py-1.5 text-sm border rounded whitespace-nowrap ${tab === name ? "border-accent text-accent" : "border-border text-text-muted"}`, children: name }, name)) }),
    ] }),
    jsx("main", { className: "flex-1 overflow-auto p-4", children: body() }),
  ] });
}
