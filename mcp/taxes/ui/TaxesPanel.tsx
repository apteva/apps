import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  Calculator,
  CalendarDays,
  Check,
  ChevronRight,
  CircleDollarSign,
  FileText,
  Landmark,
  Link2,
  Loader2,
  Plus,
  ReceiptText,
  RefreshCw,
  Save,
  Settings2,
  ShieldCheck,
  Trash2,
  WalletCards,
  X,
} from "lucide-react";

type NativePanelProps = {
  projectId: string;
  installId: number;
};

type Profile = {
  id: number;
  name: string;
  country: "ES" | "FR";
  structure: string;
  region: string;
  fiscal_year_start: string;
  fiscal_year_end: string;
  vat_registered: boolean;
  filing_cadence: "monthly" | "quarterly" | "annual";
  accounting_basis: "cash" | "accrual";
  currency: string;
  config?: Record<string, unknown>;
  archived?: boolean;
};

type Period = {
  id: number;
  profile_id: number;
  tax_type: string;
  period_start: string;
  period_end: string;
  due_date: string;
  status: string;
  metadata?: Record<string, unknown>;
};

type Obligation = {
  id: number;
  profile_id: number;
  period_id?: number;
  tax_type: string;
  authority: string;
  title: string;
  amount_cents: number;
  currency: string;
  due_date: string;
  direction: "payable" | "receivable";
  status: string;
  filed_at?: string;
  filing_ref?: string;
  waived_reason?: string;
  metadata?: Record<string, unknown>;
};

type Payment = {
  id: number;
  obligation_id: number;
  amount_cents: number;
  currency: string;
  paid_at: string;
  method: string;
  reference: string;
  bills_bill_id?: number;
  bills_payment_id?: number;
};

type Rule = {
  id: number;
  country: string;
  structure: string;
  tax_type: string;
  year: number;
  version: string;
  source_url: string;
  rules?: Record<string, unknown>;
};

type DocumentRow = {
  id: number;
  profile_id: number;
  period_id?: number;
  document_type: string;
  title: string;
  created_at: string;
};

type Adjustment = {
  id: number;
  profile_id: number;
  period_id?: number;
  tax_type: string;
  kind: string;
  amount_cents: number;
  currency: string;
  reason: string;
  status: string;
};

type MCPTool = {
  name: string;
  description: string;
  input_schema?: Record<string, unknown>;
};

type Tab = "overview" | "setup" | "estimate" | "obligations" | "advanced";
type AdvancedTab = "rules" | "reports" | "adjustments" | "sync" | "tools";

const API = "/api/apps/taxes";
const TAX_LABELS: Record<string, string> = {
  vat: "VAT",
  income_tax: "Income tax",
  corporate_tax: "Corporate tax",
  social_contributions: "Social contributions",
};
const STRUCTURES: Record<string, Array<{ value: string; label: string }>> = {
  ES: [
    { value: "ES_AUTONOMO", label: "Autonomo" },
    { value: "ES_SL", label: "Sociedad Limitada (SL)" },
  ],
  FR: [
    { value: "FR_SAS", label: "SAS" },
    { value: "FR_SASU", label: "SASU" },
    { value: "FR_SARL", label: "SARL" },
    { value: "FR_EURL", label: "EURL" },
  ],
};

const emptyProfile = {
  id: 0,
  name: "",
  country: "ES" as const,
  structure: "ES_AUTONOMO",
  region: "",
  fiscal_year_start: "01-01",
  fiscal_year_end: "12-31",
  vat_registered: true,
  filing_cadence: "quarterly" as const,
  accounting_basis: "accrual" as const,
  currency: "EUR",
  tax_regime: "income_tax",
  monthly_social: "",
};

function cx(...values: Array<string | false | null | undefined>) {
  return values.filter(Boolean).join(" ");
}

function money(cents: number, currency = "EUR") {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
    maximumFractionDigits: 2,
  }).format(Number(cents || 0) / 100);
}

function cents(value: string) {
  if (value.trim() === "") return undefined;
  const amount = Number(value);
  return Number.isFinite(amount) ? Math.round(amount * 100) : undefined;
}

function dateLabel(value: string) {
  if (!value) return "Needs confirmation";
  const parsed = new Date(`${value}T00:00:00`);
  return Number.isFinite(parsed.getTime())
    ? parsed.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })
    : value;
}

function taxLabel(value: string) {
  return TAX_LABELS[value] || value.replaceAll("_", " ");
}

function statusTone(status: string) {
  if (status === "paid" || status === "filed" || status === "closed") return "text-success border-success/30 bg-success/10";
  if (status === "waived" || status === "superseded") return "text-text-dim border-border bg-bg";
  if (status === "overdue") return "text-error border-error/30 bg-error/10";
  return "text-warn border-warn/30 bg-warn/10";
}

function compact(input: Record<string, unknown>) {
  return Object.fromEntries(
    Object.entries(input).filter(([, value]) => value !== "" && value !== undefined && value !== null),
  );
}

export default function TaxesPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [advancedTab, setAdvancedTab] = useState<AdvancedTab>("rules");
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [periods, setPeriods] = useState<Period[]>([]);
  const [obligations, setObligations] = useState<Obligation[]>([]);
  const [payments, setPayments] = useState<Payment[]>([]);
  const [rules, setRules] = useState<Rule[]>([]);
  const [documents, setDocuments] = useState<DocumentRow[]>([]);
  const [adjustments, setAdjustments] = useState<Adjustment[]>([]);
  const [tools, setTools] = useState<MCPTool[]>([]);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [result, setResult] = useState<Record<string, unknown> | null>(null);
  const [confirmAction, setConfirmAction] = useState<null | {
    title: string;
    body: string;
    confirm: string;
    danger?: boolean;
    run: () => Promise<void>;
  }>(null);

  const query = useMemo(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const callTool = useCallback(
    async <T,>(name: string, args: Record<string, unknown> = {}): Promise<T> => {
      const response = await fetch(`${API}/tools/${encodeURIComponent(name)}?${query}`, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...args, _project_id: projectId }),
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error || `${response.status} ${response.statusText}`);
      return body as T;
    },
    [projectId, query],
  );

  const loadTools = useCallback(async () => {
    const response = await fetch(`${API}/tools/?${query}`, {
      credentials: "same-origin",
      cache: "no-store",
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `${response.status} ${response.statusText}`);
    return (body.tools || []) as MCPTool[];
  }, [query]);

  const refresh = useCallback(async () => {
    setBusy("refresh");
    setError("");
    const loaders = [
      callTool<{ profiles: Profile[] }>("tax_profiles_list"),
      callTool<{ periods: Period[] }>("tax_periods_list"),
      callTool<{ obligations: Obligation[] }>("tax_obligations_list"),
      callTool<{ payments: Payment[] }>("tax_payments_list"),
      callTool<{ rules: Rule[] }>("tax_rules_list"),
      callTool<{ documents: DocumentRow[] }>("tax_documents_list"),
      callTool<{ adjustments: Adjustment[] }>("tax_adjustments_list"),
      loadTools(),
    ] as const;
    const settled = await Promise.allSettled(loaders);
    const failures = settled.filter((item) => item.status === "rejected") as PromiseRejectedResult[];
    if (failures.length) {
      setError(failures.map((item) => item.reason?.message || String(item.reason)).join(" · "));
    }
    if (settled[0].status === "fulfilled") setProfiles(settled[0].value.profiles || []);
    if (settled[1].status === "fulfilled") setPeriods(settled[1].value.periods || []);
    if (settled[2].status === "fulfilled") setObligations(settled[2].value.obligations || []);
    if (settled[3].status === "fulfilled") setPayments(settled[3].value.payments || []);
    if (settled[4].status === "fulfilled") setRules(settled[4].value.rules || []);
    if (settled[5].status === "fulfilled") setDocuments(settled[5].value.documents || []);
    if (settled[6].status === "fulfilled") setAdjustments(settled[6].value.adjustments || []);
    if (settled[7].status === "fulfilled") setTools(settled[7].value || []);
    if (!failures.length) setNotice("Updated");
    setBusy("");
  }, [callTool, loadTools]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const run = useCallback(
    async <T,>(name: string, args: Record<string, unknown>, reload = true): Promise<T | null> => {
      setBusy(name);
      setError("");
      setNotice("");
      try {
        const output = await callTool<T>(name, args);
        setResult(output as Record<string, unknown>);
        setNotice(`${name.replace("tax_", "").replaceAll("_", " ")} completed`);
        if (reload) await refresh();
        return output;
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return null;
      } finally {
        setBusy("");
      }
    },
    [callTool, refresh],
  );

  const profileName = useCallback(
    (id: number) => profiles.find((profile) => profile.id === id)?.name || `Profile ${id}`,
    [profiles],
  );
  const periodName = useCallback(
    (period: Period) => `${taxLabel(period.tax_type)} · ${dateLabel(period.period_start)} – ${dateLabel(period.period_end)}`,
    [],
  );
  const obligationName = useCallback(
    (id: number) => obligations.find((obligation) => obligation.id === id)?.title || `Obligation ${id}`,
    [obligations],
  );

  const tabs: Array<{ id: Tab; label: string; icon: typeof Landmark }> = [
    { id: "overview", label: "Overview", icon: Landmark },
    { id: "setup", label: "Tax profile", icon: Settings2 },
    { id: "estimate", label: "Periods & estimates", icon: Calculator },
    { id: "obligations", label: "Filings & payments", icon: WalletCards },
    { id: "advanced", label: "Advanced", icon: ShieldCheck },
  ];

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-text">
      <style>{`
        .tax-field input,
        .tax-field select,
        .tax-field textarea,
        .tax-control {
          width: 100%;
          border: 1px solid var(--border, rgba(127, 127, 127, 0.35));
          border-radius: 6px;
          background: var(--bg-input, transparent);
          color: var(--text, inherit);
          font-size: 14px;
          line-height: 20px;
          outline: none;
        }
        .tax-field input,
        .tax-field select,
        .tax-control {
          height: 36px;
          padding: 7px 10px;
        }
        .tax-field textarea {
          padding: 9px 10px;
          resize: vertical;
        }
        .tax-field input:focus,
        .tax-field select:focus,
        .tax-field textarea:focus,
        .tax-control:focus {
          border-color: var(--accent, currentColor);
        }
      `}</style>
      <header className="border-b border-border bg-bg">
        <div className="flex min-h-14 items-center gap-3 px-4">
          <div className="min-w-0">
            <h1 className="text-base font-semibold">Taxes</h1>
            <p className="truncate text-xs text-text-dim">Planning estimates, filings, and statutory payments</p>
          </div>
          <div className="ml-auto flex items-center gap-2">
            {notice && <span className="hidden text-xs text-success sm:block">{notice}</span>}
            <IconButton label="Refresh" onClick={() => void refresh()} disabled={!!busy}>
              <RefreshCw className={cx("h-4 w-4", busy === "refresh" && "animate-spin")} />
            </IconButton>
          </div>
        </div>
        <nav className="flex gap-1 overflow-x-auto px-3 pb-2" aria-label="Taxes sections">
          {tabs.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              onClick={() => setTab(id)}
              aria-current={tab === id ? "page" : undefined}
              className={cx(
                "flex h-9 shrink-0 items-center gap-2 rounded-md px-3 text-sm",
                tab === id ? "bg-bg-hover text-text" : "text-text-muted hover:bg-bg-hover hover:text-text",
              )}
            >
              <Icon className="h-4 w-4" />
              {label}
            </button>
          ))}
        </nav>
      </header>

      {error && (
        <div className="flex items-start gap-2 border-b border-error/30 bg-error/10 px-4 py-2 text-sm text-error">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <span>{error}</span>
          <button className="ml-auto" onClick={() => setError("")} aria-label="Dismiss error">
            <X className="h-4 w-4" />
          </button>
        </div>
      )}

      <main className="min-h-0 flex-1 overflow-auto p-3 sm:p-4">
        {tab === "overview" && (
          <Overview
            profiles={profiles}
            periods={periods}
            obligations={obligations}
            documents={documents}
            profileName={profileName}
            onOpenObligation={() => setTab("obligations")}
            onOpenSetup={() => setTab("setup")}
          />
        )}
        {tab === "setup" && (
          <Setup
            profiles={profiles}
            periods={periods}
            busy={busy}
            run={run}
            profileName={profileName}
            periodName={periodName}
          />
        )}
        {tab === "estimate" && (
          <Estimates
            profiles={profiles}
            periods={periods}
            busy={busy}
            result={result}
            run={run}
            profileName={profileName}
            periodName={periodName}
          />
        )}
        {tab === "obligations" && (
          <Filings
            profiles={profiles}
            obligations={obligations}
            payments={payments}
            busy={busy}
            run={run}
            profileName={profileName}
            obligationName={obligationName}
            setConfirmAction={setConfirmAction}
          />
        )}
        {tab === "advanced" && (
          <Advanced
            tab={advancedTab}
            setTab={setAdvancedTab}
            profiles={profiles}
            periods={periods}
            rules={rules}
            documents={documents}
            adjustments={adjustments}
            tools={tools}
            result={result}
            busy={busy}
            run={run}
            profileName={profileName}
            periodName={periodName}
            setConfirmAction={setConfirmAction}
          />
        )}
      </main>

      {confirmAction && (
        <ConfirmDialog
          action={confirmAction}
          onClose={() => setConfirmAction(null)}
        />
      )}
    </div>
  );
}

function Overview({
  profiles,
  periods,
  obligations,
  documents,
  profileName,
  onOpenObligation,
  onOpenSetup,
}: {
  profiles: Profile[];
  periods: Period[];
  obligations: Obligation[];
  documents: DocumentRow[];
  profileName: (id: number) => string;
  onOpenObligation: () => void;
  onOpenSetup: () => void;
}) {
  const totals = useMemo(() => {
    const grouped: Record<string, { payable: number; receivable: number; paid: number }> = {};
    for (const obligation of obligations) {
      const bucket = grouped[obligation.currency] ||= { payable: 0, receivable: 0, paid: 0 };
      if (obligation.status === "waived") continue;
      if (obligation.status === "paid") bucket.paid += obligation.amount_cents;
      else if (obligation.direction === "receivable") bucket.receivable += obligation.amount_cents;
      else bucket.payable += obligation.amount_cents;
    }
    return grouped;
  }, [obligations]);
  const upcoming = obligations
    .filter((row) => row.status !== "paid" && row.status !== "waived")
    .sort((a, b) => (a.due_date || "9999").localeCompare(b.due_date || "9999"))
    .slice(0, 8);
  const missingDates = periods.filter((period) => period.status === "open" && !period.due_date).length;

  if (!profiles.length) {
    return (
      <EmptyState
        icon={Settings2}
        title="Create the first tax profile"
        body="Country, structure, VAT registration, and filing cadence generate the periods used throughout the app."
        action={<Button icon={Plus} onClick={onOpenSetup}>Create profile</Button>}
      />
    );
  }

  return (
    <div className="space-y-5">
      <section>
        <div className="mb-3 flex items-end justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold">Position by currency</h2>
            <p className="text-xs text-text-dim">Open estimates are separated from recorded payments and expected refunds.</p>
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {Object.entries(totals).map(([currency, total]) => (
            <div key={currency} className="rounded-md border border-border p-3">
              <div className="mb-3 text-xs font-medium text-text-dim">{currency}</div>
              <Metric label="To pay" value={money(total.payable, currency)} />
              <Metric label="Expected refunds" value={money(total.receivable, currency)} tone="success" />
              <Metric label="Paid" value={money(total.paid, currency)} muted />
            </div>
          ))}
          {!Object.keys(totals).length && (
            <div className="rounded-md border border-dashed border-border p-4 text-sm text-text-muted">
              No obligations have been estimated yet.
            </div>
          )}
        </div>
      </section>

      {missingDates > 0 && (
        <section className="flex items-start gap-3 border-y border-warn/30 bg-warn/10 px-3 py-3">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" />
          <div>
            <div className="text-sm font-medium">{missingDates} filing deadline{missingDates === 1 ? "" : "s"} need confirmation</div>
            <p className="mt-0.5 text-xs text-text-muted">
              French deadlines and custom regimes remain unconfirmed until a specific date is entered.
            </p>
          </div>
          <Button className="ml-auto" variant="secondary" onClick={onOpenSetup}>Review</Button>
        </section>
      )}

      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.4fr)_minmax(280px,.6fr)]">
        <section className="min-w-0">
          <SectionHeading title="Upcoming filings and payments" count={upcoming.length} />
          <DataTable
            rows={upcoming}
            empty="No open obligations."
            columns={[
              { label: "Obligation", render: (row) => <div><div className="font-medium">{row.title}</div><div className="text-xs text-text-dim">{profileName(row.profile_id)}</div></div> },
              { label: "Due", render: (row) => dateLabel(row.due_date) },
              { label: "Status", render: (row) => <Status value={row.status} /> },
              { label: "Amount", align: "right", render: (row) => <span className={row.direction === "receivable" ? "text-success" : ""}>{money(row.amount_cents, row.currency)}</span> },
            ]}
            onSelect={onOpenObligation}
          />
        </section>
        <section>
          <SectionHeading title="Coverage" />
          <dl className="divide-y divide-border border-y border-border text-sm">
            <SummaryRow label="Profiles" value={profiles.length} />
            <SummaryRow label="Open periods" value={periods.filter((row) => row.status === "open").length} />
            <SummaryRow label="Generated reports" value={documents.length} />
            <SummaryRow label="Unconfirmed dates" value={missingDates} warn={missingDates > 0} />
          </dl>
        </section>
      </div>
    </div>
  );
}

function Setup({
  profiles,
  periods,
  busy,
  run,
  profileName,
  periodName,
}: {
  profiles: Profile[];
  periods: Period[];
  busy: string;
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  profileName: (id: number) => string;
  periodName: (period: Period) => string;
}) {
  const [form, setForm] = useState(emptyProfile);
  const [selectedProfile, setSelectedProfile] = useState(0);
  const [year, setYear] = useState(String(new Date().getFullYear()));
  const profilePeriods = periods.filter((period) => !selectedProfile || period.profile_id === selectedProfile);
  const structures = STRUCTURES[form.country] || [];

  function editProfile(profile: Profile) {
    const config = profile.config || {};
    setSelectedProfile(profile.id);
    setForm({
      id: profile.id,
      name: profile.name,
      country: profile.country,
      structure: profile.structure,
      region: profile.region || "",
      fiscal_year_start: profile.fiscal_year_start || "01-01",
      fiscal_year_end: profile.fiscal_year_end || "12-31",
      vat_registered: profile.vat_registered,
      filing_cadence: profile.filing_cadence,
      accounting_basis: profile.accounting_basis,
      currency: profile.currency || "EUR",
      tax_regime: String(config.tax_regime || "income_tax"),
      monthly_social: config.monthly_social_contribution_cents
        ? String(Number(config.monthly_social_contribution_cents) / 100)
        : "",
    });
  }

  async function saveProfile() {
    const config = compact({
      tax_regime: form.structure === "FR_EURL" ? form.tax_regime : undefined,
      monthly_social_contribution_cents: cents(form.monthly_social),
    });
    const payload = {
      name: form.name,
      country: form.country,
      structure: form.structure,
      region: form.region,
      fiscal_year_start: form.fiscal_year_start,
      fiscal_year_end: form.fiscal_year_end,
      vat_registered: form.vat_registered,
      filing_cadence: form.filing_cadence,
      accounting_basis: form.accounting_basis,
      currency: form.currency.toUpperCase(),
      config,
      auto_open_periods: true,
    };
    const output = form.id
      ? await run<{ profile: Profile }>("tax_profiles_update", { id: form.id, ...payload })
      : await run<{ profile: Profile }>("tax_profiles_create", payload);
    if (output?.profile) editProfile(output.profile);
  }

  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(320px,400px)_minmax(0,1fr)]">
      <section className="min-w-0">
        <SectionHeading
          title={form.id ? "Edit tax profile" : "New tax profile"}
          action={
            <IconButton label="New profile" onClick={() => { setForm(emptyProfile); setSelectedProfile(0); }}>
              <Plus className="h-4 w-4" />
            </IconButton>
          }
        />
        <div className="space-y-3 border-y border-border py-3">
          <Field label="Business name">
            <input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Country">
              <select
                value={form.country}
                onChange={(event) => {
                  const country = event.target.value as "ES" | "FR";
                  setForm({ ...form, country, structure: STRUCTURES[country][0].value });
                }}
              >
                <option value="ES">Spain</option>
                <option value="FR">France</option>
              </select>
            </Field>
            <Field label="Structure">
              <select value={form.structure} onChange={(event) => setForm({ ...form, structure: event.target.value })}>
                {structures.map((structure) => <option key={structure.value} value={structure.value}>{structure.label}</option>)}
              </select>
            </Field>
          </div>
          {form.structure === "FR_EURL" && (
            <Field label="EURL tax regime">
              <select value={form.tax_regime} onChange={(event) => setForm({ ...form, tax_regime: event.target.value })}>
                <option value="income_tax">Income tax</option>
                <option value="corporate_tax">Corporate tax election</option>
              </select>
            </Field>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Filing cadence">
              <select value={form.filing_cadence} onChange={(event) => setForm({ ...form, filing_cadence: event.target.value as Profile["filing_cadence"] })}>
                <option value="monthly">Monthly</option>
                <option value="quarterly">Quarterly</option>
                <option value="annual">Annual</option>
              </select>
            </Field>
            <Field label="Accounting basis">
              <select value={form.accounting_basis} onChange={(event) => setForm({ ...form, accounting_basis: event.target.value as Profile["accounting_basis"] })}>
                <option value="accrual">Accrual</option>
                <option value="cash">Cash</option>
              </select>
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Fiscal year starts">
              <input value={form.fiscal_year_start} onChange={(event) => setForm({ ...form, fiscal_year_start: event.target.value })} placeholder="01-01" />
            </Field>
            <Field label="Fiscal year ends">
              <input value={form.fiscal_year_end} onChange={(event) => setForm({ ...form, fiscal_year_end: event.target.value })} placeholder="12-31" />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Region">
              <input value={form.region} onChange={(event) => setForm({ ...form, region: event.target.value })} />
            </Field>
            <Field label="Currency">
              <input value={form.currency} maxLength={3} onChange={(event) => setForm({ ...form, currency: event.target.value.toUpperCase() })} />
            </Field>
          </div>
          {(form.structure === "ES_AUTONOMO" || form.structure === "FR_EURL") && (
            <Field label="Known monthly social contribution">
              <div className="relative">
                <input className="pr-10" type="number" min="0" step="0.01" value={form.monthly_social} onChange={(event) => setForm({ ...form, monthly_social: event.target.value })} />
                <span className="absolute right-3 top-2 text-xs text-text-dim">{form.currency}</span>
              </div>
            </Field>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={form.vat_registered} onChange={(event) => setForm({ ...form, vat_registered: event.target.checked })} />
            VAT registered
          </label>
        </div>
        <div className="flex gap-2 pt-3">
          <Button icon={Save} disabled={!form.name.trim() || !!busy} onClick={() => void saveProfile()}>
            {form.id ? "Save profile" : "Create profile"}
          </Button>
        </div>
      </section>

      <div className="min-w-0 space-y-5">
        <section>
          <SectionHeading title="Profiles" count={profiles.length} />
          <DataTable
            rows={profiles}
            empty="No tax profiles."
            columns={[
              { label: "Business", render: (row) => <div><div className="font-medium">{row.name}</div><div className="text-xs text-text-dim">{row.country} · {row.structure}</div></div> },
              { label: "VAT", render: (row) => row.vat_registered ? "Registered" : "Not registered" },
              { label: "Cadence", render: (row) => row.filing_cadence },
              { label: "", align: "right", render: () => <ChevronRight className="ml-auto h-4 w-4 text-text-dim" /> },
            ]}
            onSelect={editProfile}
            selectedId={selectedProfile}
          />
        </section>

        <section>
          <SectionHeading
            title="Generated periods"
            count={profilePeriods.length}
            action={
              <div className="flex items-center gap-2">
                <input className="tax-control h-8 w-20" type="number" value={year} onChange={(event) => setYear(event.target.value)} aria-label="Period year" />
                <Button
                  icon={CalendarDays}
                  variant="secondary"
                  disabled={!selectedProfile || !!busy}
                  onClick={() => void run("tax_periods_generate", { profile_id: selectedProfile, year: Number(year) })}
                >
                  Regenerate
                </Button>
              </div>
            }
          />
          <DataTable
            rows={profilePeriods}
            empty={selectedProfile ? "No periods for this profile." : "Select a profile to review its periods."}
            columns={[
              { label: "Period", render: (row) => <div><div className="font-medium">{periodName(row)}</div><div className="text-xs text-text-dim">{profileName(row.profile_id)}</div></div> },
              { label: "Due", render: (row) => <span className={!row.due_date ? "text-warn" : ""}>{dateLabel(row.due_date)}</span> },
              { label: "Status", render: (row) => <Status value={row.status} /> },
              { label: "Source", render: (row) => String(row.metadata?.deadline_state || "custom").replaceAll("_", " ") },
            ]}
          />
        </section>
      </div>
    </div>
  );
}

function Estimates({
  profiles,
  periods,
  busy,
  result,
  run,
  profileName,
  periodName,
}: {
  profiles: Profile[];
  periods: Period[];
  busy: string;
  result: Record<string, unknown> | null;
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  profileName: (id: number) => string;
  periodName: (period: Period) => string;
}) {
  const [profileId, setProfileId] = useState(0);
  const [periodId, setPeriodId] = useState(0);
  const [sync, setSync] = useState(true);
  const [createObligation, setCreateObligation] = useState(true);
  const [dueDate, setDueDate] = useState("");
  const [values, setValues] = useState({
    revenue: "",
    expenses: "",
    outputTax: "",
    inputTax: "",
    taxableProfit: "",
    monthlySocial: "",
  });
  const selectedProfile = profiles.find((profile) => profile.id === profileId);
  const availablePeriods = periods.filter((period) => period.profile_id === profileId && period.status === "open");
  const selectedPeriod = availablePeriods.find((period) => period.id === periodId);

  useEffect(() => {
    if (!profileId && profiles[0]) setProfileId(profiles[0].id);
  }, [profileId, profiles]);
  useEffect(() => {
    const first = periods.find((period) => period.profile_id === profileId && period.status === "open");
    if (!availablePeriods.some((period) => period.id === periodId)) setPeriodId(first?.id || 0);
  }, [availablePeriods, periodId, periods, profileId]);
  useEffect(() => {
    setDueDate(selectedPeriod?.due_date || "");
    const configured = Number(selectedProfile?.config?.monthly_social_contribution_cents || 0);
    setValues((current) => ({
      ...current,
      monthlySocial: current.monthlySocial || (configured ? String(configured / 100) : ""),
    }));
  }, [selectedPeriod?.id, selectedProfile?.id]);

  async function estimate() {
    if (!selectedPeriod) return;
    const tool = `tax_estimate_${selectedPeriod.tax_type}`;
    await run(tool, compact({
      profile_id: profileId,
      period_id: periodId,
      due_date: dueDate,
      revenue_cents: cents(values.revenue),
      expenses_cents: cents(values.expenses),
      output_tax_cents: cents(values.outputTax),
      input_tax_cents: cents(values.inputTax),
      taxable_profit_cents: cents(values.taxableProfit),
      social_contribution_cents: cents(values.monthlySocial),
      sync_sources: sync && selectedPeriod.tax_type !== "social_contributions",
      create_obligation: createObligation,
    }));
  }

  const output = result?.outputs as Record<string, unknown> | undefined;
  const warnings = (result?.warnings || []) as string[];
  const estimated = Number(output?.estimated_payable_cents || 0);

  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(320px,420px)_minmax(0,1fr)]">
      <section>
        <SectionHeading title="Estimate an open period" />
        <div className="space-y-3 border-y border-border py-3">
          <Field label="Tax profile">
            <select value={profileId} onChange={(event) => setProfileId(Number(event.target.value))}>
              <option value={0}>Select profile</option>
              {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
            </select>
          </Field>
          <Field label="Generated period">
            <select value={periodId} onChange={(event) => setPeriodId(Number(event.target.value))}>
              <option value={0}>Select period</option>
              {availablePeriods.map((period) => <option key={period.id} value={period.id}>{periodName(period)}</option>)}
            </select>
          </Field>
          {selectedPeriod && (
            <div className="flex items-center justify-between border-y border-border py-2 text-sm">
              <span>{taxLabel(selectedPeriod.tax_type)}</span>
              <span className="text-text-muted">{profileName(selectedPeriod.profile_id)}</span>
            </div>
          )}
          <Field label="Confirmed due date" hint={!selectedPeriod?.due_date ? "Required before an obligation can be created." : undefined}>
            <input type="date" value={dueDate} onChange={(event) => setDueDate(event.target.value)} />
          </Field>
          {selectedPeriod?.tax_type !== "social_contributions" && (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <MoneyField label="Revenue" value={values.revenue} onChange={(value) => setValues({ ...values, revenue: value })} />
                <MoneyField label="Expenses" value={values.expenses} onChange={(value) => setValues({ ...values, expenses: value })} />
              </div>
              {selectedPeriod?.tax_type === "vat" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <MoneyField label="Output VAT" value={values.outputTax} onChange={(value) => setValues({ ...values, outputTax: value })} />
                  <MoneyField label="Recoverable input VAT" value={values.inputTax} onChange={(value) => setValues({ ...values, inputTax: value })} />
                </div>
              )}
              {selectedPeriod?.tax_type !== "vat" && (
                <MoneyField label="Taxable profit override" value={values.taxableProfit} onChange={(value) => setValues({ ...values, taxableProfit: value })} />
              )}
            </>
          )}
          {selectedPeriod?.tax_type === "social_contributions" && (
            <MoneyField label="Monthly contribution" value={values.monthlySocial} onChange={(value) => setValues({ ...values, monthlySocial: value })} />
          )}
          {selectedPeriod?.tax_type !== "social_contributions" && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={sync} onChange={(event) => setSync(event.target.checked)} />
              Use Billing and Bills records
            </label>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={createObligation} onChange={(event) => setCreateObligation(event.target.checked)} />
            Create or update the period obligation
          </label>
        </div>
        <div className="pt-3">
          <Button
            icon={Calculator}
            disabled={!selectedPeriod || (!!createObligation && !dueDate) || !!busy}
            onClick={() => void estimate()}
          >
            Run estimate
          </Button>
        </div>
      </section>

      <section className="min-w-0">
        <SectionHeading title="Estimate result" />
        {!output ? (
          <EmptyState icon={Calculator} title="No estimate yet" body="Select one generated period and review the inputs before calculating." />
        ) : (
          <div className="space-y-5">
            <div className="border-y border-border py-5">
              <div className="text-xs font-medium uppercase text-text-dim">{estimated < 0 ? "Expected refund" : "Estimated payable"}</div>
              <div className={cx("mt-1 text-3xl font-semibold", estimated < 0 && "text-success")}>
                {money(Math.abs(estimated), selectedProfile?.currency)}
              </div>
              <div className="mt-2 text-sm text-text-muted">
                {taxLabel(String(result?.tax_type || ""))} · {String(result?.period_start || "")} to {String(result?.period_end || "")}
              </div>
            </div>
            <div className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
              {Object.entries(output)
                .filter(([key]) => key.endsWith("_cents") || key === "rate_bps" || key === "months")
                .map(([key, value]) => (
                  <div key={key} className="flex items-center justify-between border-b border-border pb-2 text-sm">
                    <span className="text-text-muted">{key.replaceAll("_", " ")}</span>
                    <span className="font-medium tabular-nums">
                      {key.endsWith("_cents") ? money(Number(value), selectedProfile?.currency) : String(value)}
                    </span>
                  </div>
                ))}
            </div>
            {warnings.length > 0 && (
              <div className="space-y-2 border-l-2 border-warn pl-3">
                {warnings.map((warning) => <p key={warning} className="text-sm text-text-muted">{warning}</p>)}
              </div>
            )}
          </div>
        )}
      </section>
    </div>
  );
}

function Filings({
  profiles,
  obligations,
  payments,
  busy,
  run,
  profileName,
  obligationName,
  setConfirmAction,
}: {
  profiles: Profile[];
  obligations: Obligation[];
  payments: Payment[];
  busy: string;
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  profileName: (id: number) => string;
  obligationName: (id: number) => string;
  setConfirmAction: (action: null | { title: string; body: string; confirm: string; danger?: boolean; run: () => Promise<void> }) => void;
}) {
  const [selectedId, setSelectedId] = useState(0);
  const [filter, setFilter] = useState("open");
  const [filingRef, setFilingRef] = useState("");
  const [waiverReason, setWaiverReason] = useState("");
  const [payment, setPayment] = useState({ amount: "", paidAt: new Date().toISOString().slice(0, 10), method: "bank_transfer", reference: "" });
  const [billLink, setBillLink] = useState({ billId: "", paymentId: "" });
  const selected = obligations.find((obligation) => obligation.id === selectedId);
  const visible = obligations.filter((obligation) => {
    if (filter === "all") return true;
    if (filter === "open") return obligation.status !== "paid" && obligation.status !== "waived";
    return obligation.status === filter;
  });

  useEffect(() => {
    if (!selectedId && visible[0]) setSelectedId(visible[0].id);
  }, [selectedId, visible]);
  useEffect(() => {
    if (selected) {
      const paid = payments.filter((row) => row.obligation_id === selected.id).reduce((sum, row) => sum + row.amount_cents, 0);
      setPayment((current) => ({ ...current, amount: String(Math.max(0, selected.amount_cents - paid) / 100) }));
      setBillLink({
        billId: selected.metadata?.bills_bill_id ? String(selected.metadata.bills_bill_id) : "",
        paymentId: "",
      });
    }
  }, [selected?.id]);

  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(0,1.15fr)_minmax(340px,.85fr)]">
      <section className="min-w-0">
        <SectionHeading
          title="Obligations"
          count={visible.length}
          action={
            <select className="tax-control h-8 w-auto" value={filter} onChange={(event) => setFilter(event.target.value)} aria-label="Filter obligations">
              <option value="open">Open</option>
              <option value="filed">Filed</option>
              <option value="paid">Paid</option>
              <option value="waived">Waived</option>
              <option value="all">All</option>
            </select>
          }
        />
        <DataTable
          rows={visible}
          empty="No obligations match this view."
          selectedId={selectedId}
          onSelect={(row) => setSelectedId(row.id)}
          columns={[
            { label: "Obligation", render: (row) => <div><div className="font-medium">{row.title}</div><div className="text-xs text-text-dim">{profileName(row.profile_id)} · {taxLabel(row.tax_type)}</div></div> },
            { label: "Due", render: (row) => dateLabel(row.due_date) },
            { label: "Status", render: (row) => <Status value={row.status} /> },
            { label: "Amount", align: "right", render: (row) => <span className={row.direction === "receivable" ? "text-success" : ""}>{money(row.amount_cents, row.currency)}</span> },
          ]}
        />
        <div className="mt-5">
          <SectionHeading title="Recorded payments" count={payments.length} />
          <DataTable
            rows={payments}
            empty="No payments recorded."
            columns={[
              { label: "Obligation", render: (row) => obligationName(row.obligation_id) },
              { label: "Paid", render: (row) => dateLabel(row.paid_at.slice(0, 10)) },
              { label: "Method", render: (row) => row.method || "Unspecified" },
              { label: "Reference", render: (row) => row.reference || "—" },
              { label: "Amount", align: "right", render: (row) => money(row.amount_cents, row.currency) },
            ]}
          />
        </div>
      </section>

      <section className="min-w-0">
        <SectionHeading title="Selected obligation" />
        {!selected ? (
          <EmptyState icon={ReceiptText} title="Select an obligation" body="Filing and payment actions appear here." />
        ) : (
          <div className="space-y-5">
            <div className="border-y border-border py-3">
              <div className="flex items-start justify-between gap-3">
                <div>
                  <h3 className="font-medium">{selected.title}</h3>
                  <p className="mt-1 text-xs text-text-dim">{profileName(selected.profile_id)} · {selected.authority || "No authority"}</p>
                </div>
                <Status value={selected.status} />
              </div>
              <div className={cx("mt-4 text-2xl font-semibold", selected.direction === "receivable" && "text-success")}>
                {money(selected.amount_cents, selected.currency)}
              </div>
              <div className="mt-1 text-xs text-text-dim">{selected.direction === "receivable" ? "Expected refund" : `Due ${dateLabel(selected.due_date)}`}</div>
            </div>

            {selected.status !== "paid" && selected.status !== "waived" && (
              <div className="space-y-3">
                <h3 className="text-sm font-semibold">Filing</h3>
                <Field label="Filing reference">
                  <input value={filingRef} onChange={(event) => setFilingRef(event.target.value)} />
                </Field>
                <div className="flex flex-wrap gap-2">
                  <Button
                    icon={Check}
                    variant="secondary"
                    disabled={!!busy}
                    onClick={() => setConfirmAction({
                      title: "Mark this obligation filed?",
                      body: "This records the filing timestamp and reference. It does not record a payment.",
                      confirm: "Mark filed",
                      run: async () => {
                        await run("tax_obligations_mark_filed", {
                          id: selected.id,
                          filing_ref: filingRef,
                          filed_at: new Date().toISOString(),
                        });
                      },
                    })}
                  >
                    Mark filed
                  </Button>
                </div>
                <Field label="Waiver reason">
                  <input value={waiverReason} onChange={(event) => setWaiverReason(event.target.value)} />
                </Field>
                <Button
                  icon={X}
                  variant="danger"
                  disabled={!waiverReason.trim() || !!busy}
                  onClick={() => setConfirmAction({
                    title: "Waive this obligation?",
                    body: `The reason “${waiverReason}” will be retained in the audit trail.`,
                    confirm: "Waive obligation",
                    danger: true,
                    run: async () => {
                      await run("tax_obligations_mark_waived", { id: selected.id, reason: waiverReason });
                    },
                  })}
                >
                  Waive
                </Button>
              </div>
            )}

            {selected.direction === "payable" && selected.status !== "paid" && selected.status !== "waived" && (
              <div className="space-y-3 border-t border-border pt-4">
                <h3 className="text-sm font-semibold">Record payment</h3>
                <div className="grid gap-3 sm:grid-cols-2">
                  <MoneyField label={`Amount (${selected.currency})`} value={payment.amount} onChange={(value) => setPayment({ ...payment, amount: value })} />
                  <Field label="Paid at">
                    <input type="date" value={payment.paidAt} onChange={(event) => setPayment({ ...payment, paidAt: event.target.value })} />
                  </Field>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Method">
                    <select value={payment.method} onChange={(event) => setPayment({ ...payment, method: event.target.value })}>
                      <option value="bank_transfer">Bank transfer</option>
                      <option value="direct_debit">Direct debit</option>
                      <option value="card">Card</option>
                      <option value="cash">Cash</option>
                      <option value="other">Other</option>
                    </select>
                  </Field>
                  <Field label="Reference">
                    <input value={payment.reference} onChange={(event) => setPayment({ ...payment, reference: event.target.value })} />
                  </Field>
                </div>
                <Button
                  icon={CircleDollarSign}
                  disabled={!cents(payment.amount) || !!busy}
                  onClick={() => setConfirmAction({
                    title: "Record this tax payment?",
                    body: `${money(cents(payment.amount) || 0, selected.currency)} will be applied to ${selected.title}.`,
                    confirm: "Record payment",
                    run: async () => {
                      await run("tax_payments_record", {
                        obligation_id: selected.id,
                        amount_cents: cents(payment.amount),
                        currency: selected.currency,
                        paid_at: payment.paidAt,
                        method: payment.method,
                        reference: payment.reference,
                      });
                    },
                  })}
                >
                  Record payment
                </Button>
              </div>
            )}

            {selected.direction === "payable" && (
              <div className="space-y-3 border-t border-border pt-4">
                <h3 className="text-sm font-semibold">Bills link</h3>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Bill ID">
                    <input inputMode="numeric" value={billLink.billId} onChange={(event) => setBillLink({ ...billLink, billId: event.target.value })} />
                  </Field>
                  <Field label="Payment ID" hint="Optional. Payment is recorded only after verification.">
                    <input inputMode="numeric" value={billLink.paymentId} onChange={(event) => setBillLink({ ...billLink, paymentId: event.target.value })} />
                  </Field>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    icon={Link2}
                    variant="secondary"
                    disabled={!billLink.billId || !!busy}
                    onClick={() => void run("tax_payments_link_bill", compact({
                      obligation_id: selected.id,
                      bills_bill_id: Number(billLink.billId),
                      bills_payment_id: billLink.paymentId ? Number(billLink.paymentId) : undefined,
                    }))}
                  >
                    Verify and link
                  </Button>
                  <Button
                    icon={Plus}
                    variant="secondary"
                    disabled={!!selected.metadata?.bills_bill_id || !!busy}
                    onClick={() => setConfirmAction({
                      title: "Create this payable in Bills?",
                      body: "A vendor bill will be created for the tax authority and linked to this obligation.",
                      confirm: "Create bill",
                      run: async () => {
                        await run("tax_payments_create_bill", { obligation_id: selected.id });
                      },
                    })}
                  >
                    Create in Bills
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}
      </section>
    </div>
  );
}

function Advanced({
  tab,
  setTab,
  profiles,
  periods,
  rules,
  documents,
  adjustments,
  tools,
  result,
  busy,
  run,
  profileName,
  periodName,
  setConfirmAction,
}: {
  tab: AdvancedTab;
  setTab: (tab: AdvancedTab) => void;
  profiles: Profile[];
  periods: Period[];
  rules: Rule[];
  documents: DocumentRow[];
  adjustments: Adjustment[];
  tools: MCPTool[];
  result: Record<string, unknown> | null;
  busy: string;
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  profileName: (id: number) => string;
  periodName: (period: Period) => string;
  setConfirmAction: (action: null | { title: string; body: string; confirm: string; danger?: boolean; run: () => Promise<void> }) => void;
}) {
  const items: Array<{ id: AdvancedTab; label: string }> = [
    { id: "rules", label: "Rules" },
    { id: "reports", label: "Reports" },
    { id: "adjustments", label: "Adjustments" },
    { id: "sync", label: "Source sync" },
    { id: "tools", label: "MCP tools" },
  ];
  return (
    <div className="grid gap-5 lg:grid-cols-[180px_minmax(0,1fr)]">
      <nav className="flex gap-1 overflow-x-auto lg:flex-col" aria-label="Advanced taxes sections">
        {items.map((item) => (
          <button
            key={item.id}
            type="button"
            onClick={() => setTab(item.id)}
            className={cx(
              "h-9 shrink-0 rounded-md px-3 text-left text-sm",
              tab === item.id ? "bg-bg-hover text-text" : "text-text-muted hover:bg-bg-hover",
            )}
          >
            {item.label}
          </button>
        ))}
      </nav>
      <div className="min-w-0">
        {tab === "rules" && <RulesView rules={rules} />}
        {tab === "reports" && <ReportsView profiles={profiles} periods={periods} documents={documents} run={run} periodName={periodName} />}
        {tab === "adjustments" && <AdjustmentsView profiles={profiles} periods={periods} adjustments={adjustments} run={run} profileName={profileName} periodName={periodName} setConfirmAction={setConfirmAction} />}
        {tab === "sync" && <SyncView profiles={profiles} run={run} result={result} />}
        {tab === "tools" && <ToolsView tools={tools} run={run} result={result} busy={busy} />}
      </div>
    </div>
  );
}

function RulesView({ rules }: { rules: Rule[] }) {
  return (
    <section>
      <SectionHeading title="Rule versions" count={rules.length} />
      <p className="mb-3 text-xs text-text-dim">Rates are planning defaults. Warnings and official sources remain attached to each calculation.</p>
      <DataTable
        rows={rules}
        empty="No rules available."
        columns={[
          { label: "Jurisdiction", render: (row) => `${row.country} · ${row.structure}` },
          { label: "Tax", render: (row) => taxLabel(row.tax_type) },
          { label: "Year", render: (row) => row.year },
          { label: "Version", render: (row) => row.version },
          { label: "Source", render: (row) => row.source_url ? <a className="text-accent hover:underline" href={row.source_url} target="_blank" rel="noreferrer">Official source</a> : "—" },
        ]}
      />
    </section>
  );
}

function ReportsView({
  profiles,
  periods,
  documents,
  run,
  periodName,
}: {
  profiles: Profile[];
  periods: Period[];
  documents: DocumentRow[];
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  periodName: (period: Period) => string;
}) {
  const [profileId, setProfileId] = useState(0);
  const [periodId, setPeriodId] = useState(0);
  return (
    <div className="space-y-5">
      <section>
        <SectionHeading title="Generate scoped report" />
        <div className="grid gap-3 border-y border-border py-3 sm:grid-cols-[minmax(180px,260px)_minmax(240px,1fr)_auto]">
          <Field label="Profile">
            <select value={profileId} onChange={(event) => { setProfileId(Number(event.target.value)); setPeriodId(0); }}>
              <option value={0}>Select profile</option>
              {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
            </select>
          </Field>
          <Field label="Period">
            <select value={periodId} onChange={(event) => setPeriodId(Number(event.target.value))}>
              <option value={0}>All profile records</option>
              {periods.filter((period) => period.profile_id === profileId).map((period) => <option key={period.id} value={period.id}>{periodName(period)}</option>)}
            </select>
          </Field>
          <Button className="self-end" icon={FileText} disabled={!profileId} onClick={() => void run("tax_report_generate", compact({ profile_id: profileId, period_id: periodId || undefined }))}>
            Generate
          </Button>
        </div>
      </section>
      <section>
        <SectionHeading title="Documents" count={documents.length} />
        <DataTable
          rows={documents}
          empty="No reports generated."
          columns={[
            { label: "Title", render: (row) => row.title },
            { label: "Type", render: (row) => row.document_type },
            { label: "Created", render: (row) => dateLabel(row.created_at.slice(0, 10)) },
            { label: "", align: "right", render: (row) => <Button variant="ghost" onClick={() => void run("tax_documents_get", { id: row.id }, false)}>Open</Button> },
          ]}
        />
      </section>
    </div>
  );
}

function AdjustmentsView({
  profiles,
  periods,
  adjustments,
  run,
  profileName,
  periodName,
  setConfirmAction,
}: {
  profiles: Profile[];
  periods: Period[];
  adjustments: Adjustment[];
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  profileName: (id: number) => string;
  periodName: (period: Period) => string;
  setConfirmAction: (action: null | { title: string; body: string; confirm: string; danger?: boolean; run: () => Promise<void> }) => void;
}) {
  const [form, setForm] = useState({ profileId: 0, periodId: 0, kind: "manual", amount: "", reason: "" });
  const profile = profiles.find((row) => row.id === form.profileId);
  const period = periods.find((row) => row.id === form.periodId);
  return (
    <div className="space-y-5">
      <section>
        <SectionHeading title="Create manual adjustment" />
        <div className="grid gap-3 border-y border-border py-3 md:grid-cols-2 xl:grid-cols-5">
          <Field label="Profile">
            <select value={form.profileId} onChange={(event) => setForm({ ...form, profileId: Number(event.target.value), periodId: 0 })}>
              <option value={0}>Select profile</option>
              {profiles.map((row) => <option key={row.id} value={row.id}>{row.name}</option>)}
            </select>
          </Field>
          <Field label="Period">
            <select value={form.periodId} onChange={(event) => setForm({ ...form, periodId: Number(event.target.value) })}>
              <option value={0}>Select period</option>
              {periods.filter((row) => row.profile_id === form.profileId).map((row) => <option key={row.id} value={row.id}>{periodName(row)}</option>)}
            </select>
          </Field>
          <Field label="Kind">
            <input value={form.kind} onChange={(event) => setForm({ ...form, kind: event.target.value })} />
          </Field>
          <MoneyField label={`Amount (${profile?.currency || "EUR"})`} value={form.amount} onChange={(value) => setForm({ ...form, amount: value })} allowNegative />
          <Field label="Reason">
            <input value={form.reason} onChange={(event) => setForm({ ...form, reason: event.target.value })} />
          </Field>
        </div>
        <div className="pt-3">
          <Button icon={Plus} disabled={!profile || !period || !form.reason.trim() || !cents(form.amount)} onClick={() => void run("tax_adjustments_create", {
            profile_id: profile?.id,
            period_id: period?.id,
            tax_type: period?.tax_type,
            kind: form.kind,
            amount_cents: cents(form.amount),
            currency: profile?.currency,
            reason: form.reason,
          })}>Create adjustment</Button>
        </div>
      </section>
      <section>
        <SectionHeading title="Adjustments" count={adjustments.length} />
        <DataTable
          rows={adjustments}
          empty="No manual adjustments."
          columns={[
            { label: "Profile", render: (row) => profileName(row.profile_id) },
            { label: "Tax", render: (row) => taxLabel(row.tax_type) },
            { label: "Reason", render: (row) => row.reason },
            { label: "Amount", align: "right", render: (row) => money(row.amount_cents, row.currency) },
            { label: "", align: "right", render: (row) => <IconButton label="Delete adjustment" onClick={() => setConfirmAction({
              title: "Delete this adjustment?",
              body: row.reason,
              confirm: "Delete",
              danger: true,
              run: async () => { await run("tax_adjustments_delete", { id: row.id }); },
            })}><Trash2 className="h-4 w-4" /></IconButton> },
          ]}
        />
      </section>
    </div>
  );
}

function SyncView({
  profiles,
  run,
  result,
}: {
  profiles: Profile[];
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  result: Record<string, unknown> | null;
}) {
  const year = new Date().getFullYear();
  const [form, setForm] = useState({ profileId: 0, source: "all", start: `${year}-01-01`, end: `${year}-12-31` });
  const tool = form.source === "billing" ? "tax_sync_from_billing" : form.source === "bills" ? "tax_sync_from_bills" : "tax_sync_all";
  return (
    <div className="grid gap-5 xl:grid-cols-[360px_minmax(0,1fr)]">
      <section>
        <SectionHeading title="Inspect source totals" />
        <div className="space-y-3 border-y border-border py-3">
          <Field label="Profile">
            <select value={form.profileId} onChange={(event) => setForm({ ...form, profileId: Number(event.target.value) })}>
              <option value={0}>Select profile</option>
              {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
            </select>
          </Field>
          <Field label="Source">
            <select value={form.source} onChange={(event) => setForm({ ...form, source: event.target.value })}>
              <option value="all">Billing and Bills</option>
              <option value="billing">Billing only</option>
              <option value="bills">Bills only</option>
            </select>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Start"><input type="date" value={form.start} onChange={(event) => setForm({ ...form, start: event.target.value })} /></Field>
            <Field label="End"><input type="date" value={form.end} onChange={(event) => setForm({ ...form, end: event.target.value })} /></Field>
          </div>
        </div>
        <div className="pt-3">
          <Button icon={RefreshCw} disabled={!form.profileId} onClick={() => void run(tool, { profile_id: form.profileId, period_start: form.start, period_end: form.end }, false)}>Run sync</Button>
        </div>
      </section>
      <section className="min-w-0">
        <SectionHeading title="Source response" />
        <JSONResult value={result} />
      </section>
    </div>
  );
}

function ToolsView({
  tools,
  run,
  result,
  busy,
}: {
  tools: MCPTool[];
  run: <T,>(name: string, args: Record<string, unknown>, reload?: boolean) => Promise<T | null>;
  result: Record<string, unknown> | null;
  busy: string;
}) {
  const [name, setName] = useState("tax_profiles_list");
  const [args, setArgs] = useState("{}");
  const [parseError, setParseError] = useState("");
  function execute() {
    try {
      const parsed = JSON.parse(args || "{}");
      setParseError("");
      void run(name, parsed, false);
    } catch (err) {
      setParseError(err instanceof Error ? err.message : String(err));
    }
  }
  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(300px,420px)_minmax(0,1fr)]">
      <section>
        <SectionHeading title="MCP tool console" />
        <div className="space-y-3 border-y border-border py-3">
          <Field label="Tool">
            <select value={name} onChange={(event) => setName(event.target.value)}>
              {tools.map((tool) => <option key={tool.name} value={tool.name}>{tool.name}</option>)}
            </select>
          </Field>
          <Field label="JSON arguments" hint={tools.find((tool) => tool.name === name)?.description}>
            <textarea className="min-h-44 font-mono text-xs" value={args} onChange={(event) => setArgs(event.target.value)} />
          </Field>
          {parseError && <p className="text-xs text-error">{parseError}</p>}
        </div>
        <div className="pt-3">
          <Button icon={ChevronRight} disabled={!!busy} onClick={execute}>Run tool</Button>
        </div>
      </section>
      <section className="min-w-0">
        <SectionHeading title="Result" />
        <JSONResult value={result} />
      </section>
    </div>
  );
}

function ConfirmDialog({
  action,
  onClose,
}: {
  action: { title: string; body: string; confirm: string; danger?: boolean; run: () => Promise<void> };
  onClose: () => void;
}) {
  const [working, setWorking] = useState(false);
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" role="dialog" aria-modal="true" aria-label={action.title}>
      <div className="w-full max-w-md rounded-md border border-border bg-bg p-4 shadow-xl">
        <h2 className="text-base font-semibold">{action.title}</h2>
        <p className="mt-2 text-sm text-text-muted">{action.body}</p>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="secondary" disabled={working} onClick={onClose}>Cancel</Button>
          <Button
            variant={action.danger ? "danger" : "primary"}
            disabled={working}
            icon={working ? Loader2 : Check}
            onClick={() => {
              setWorking(true);
              void action.run().finally(onClose);
            }}
          >
            {action.confirm}
          </Button>
        </div>
      </div>
    </div>
  );
}

function SectionHeading({ title, count, action }: { title: string; count?: number; action?: React.ReactNode }) {
  return (
    <div className="mb-2 flex min-h-8 items-center gap-2">
      <h2 className="text-sm font-semibold">{title}</h2>
      {count !== undefined && <span className="text-xs tabular-nums text-text-dim">{count}</span>}
      {action && <div className="ml-auto">{action}</div>}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="tax-field block min-w-0 text-xs text-text-dim">
      <span className="mb-1 block">{label}</span>
      {children}
      {hint && <span className="mt-1 block leading-4 text-text-dim">{hint}</span>}
    </label>
  );
}

function MoneyField({
  label,
  value,
  onChange,
  allowNegative = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  allowNegative?: boolean;
}) {
  return (
    <Field label={label}>
      <input type="number" min={allowNegative ? undefined : "0"} step="0.01" value={value} onChange={(event) => onChange(event.target.value)} />
    </Field>
  );
}

function Button({
  icon: Icon,
  variant = "primary",
  className,
  children,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  icon?: typeof Plus;
  variant?: "primary" | "secondary" | "danger" | "ghost";
}) {
  return (
    <button
      type="button"
      className={cx(
        "inline-flex h-9 items-center justify-center gap-2 rounded-md px-3 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-50",
        variant === "primary" && "bg-accent text-accent-contrast",
        variant === "secondary" && "border border-border text-text hover:bg-bg-hover",
        variant === "danger" && "border border-error/50 text-error hover:bg-error/10",
        variant === "ghost" && "text-text-muted hover:bg-bg-hover hover:text-text",
        className,
      )}
      {...props}
    >
      {Icon && <Icon className={cx("h-4 w-4", Icon === Loader2 && "animate-spin")} />}
      {children}
    </button>
  );
}

function IconButton({ label, children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      className="grid h-9 w-9 shrink-0 place-items-center rounded-md border border-border text-text-muted hover:bg-bg-hover hover:text-text disabled:opacity-50"
      {...props}
    >
      {children}
    </button>
  );
}

function Status({ value }: { value: string }) {
  return <span className={cx("inline-flex rounded-md border px-2 py-0.5 text-xs capitalize", statusTone(value))}>{value.replaceAll("_", " ")}</span>;
}

function Metric({ label, value, tone, muted }: { label: string; value: string; tone?: "success"; muted?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-1">
      <span className="text-xs text-text-dim">{label}</span>
      <span className={cx("text-sm font-semibold tabular-nums", tone === "success" && "text-success", muted && "text-text-muted")}>{value}</span>
    </div>
  );
}

function SummaryRow({ label, value, warn }: { label: string; value: React.ReactNode; warn?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 py-2">
      <dt className="text-text-muted">{label}</dt>
      <dd className={cx("font-medium tabular-nums", warn && "text-warn")}>{value}</dd>
    </div>
  );
}

function EmptyState({
  icon: Icon,
  title,
  body,
  action,
}: {
  icon: typeof Settings2;
  title: string;
  body: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex min-h-48 flex-col items-center justify-center border-y border-dashed border-border px-4 py-8 text-center">
      <Icon className="h-6 w-6 text-text-dim" />
      <h2 className="mt-3 text-sm font-semibold">{title}</h2>
      <p className="mt-1 max-w-lg text-sm text-text-muted">{body}</p>
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

function DataTable<T extends { id: number }>({
  rows,
  columns,
  empty,
  onSelect,
  selectedId,
}: {
  rows: T[];
  columns: Array<{ label: string; align?: "right"; render: (row: T) => React.ReactNode }>;
  empty: string;
  onSelect?: (row: T) => void;
  selectedId?: number;
}) {
  if (!rows.length) {
    return <div className="border-y border-dashed border-border py-8 text-center text-sm text-text-muted">{empty}</div>;
  }
  return (
    <div className="overflow-x-auto border-y border-border">
      <table className="w-full min-w-[620px] text-sm">
        <thead>
          <tr className="text-xs text-text-dim">
            {columns.map((column, index) => <th key={`${column.label}-${index}`} className={cx("px-2 py-2 text-left font-normal", column.align === "right" && "text-right")}>{column.label}</th>)}
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {rows.map((row) => (
            <tr
              key={row.id}
              onClick={onSelect ? () => onSelect(row) : undefined}
              className={cx(onSelect && "cursor-pointer hover:bg-bg-hover", selectedId === row.id && "bg-bg-hover")}
            >
              {columns.map((column, index) => <td key={index} className={cx("px-2 py-2 align-middle", column.align === "right" && "text-right tabular-nums")}>{column.render(row)}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function JSONResult({ value }: { value: Record<string, unknown> | null }) {
  if (!value) return <EmptyState icon={FileText} title="No result" body="Run an action to inspect its structured response." />;
  return <pre className="max-h-[560px] overflow-auto border-y border-border bg-bg-input p-3 text-xs leading-5">{JSON.stringify(value, null, 2)}</pre>;
}
