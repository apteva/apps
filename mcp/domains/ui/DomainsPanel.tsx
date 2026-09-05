// DomainsPanel — operator UI for the domains app.
//
// Two views:
//   - List: domains the project has registered with this app, with
//     add + remove. Click a row to open the records browser.
//   - Records: live-fetched from the bound DNS provider. Add / edit /
//     delete individual records.
//
// All mutations go through /api/apps/domains/tools/call with the
// generic tool dispatcher pattern messaging uses.

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useRef,
  useId,
} from "react";

import { DomainSettings, DNSRecoveryNotice } from "./DomainOperations";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Domain {
  id: number;
  name: string;
  registrar_slug?: string;
  dns_provider_slug?: string;
  connection_id?: number;
  connection_mode?: string;
  expires_at?: string;
  notes?: string;
  created_at?: string;
  updated_at?: string;
}

interface DNSRecord {
  id: string;
  name: string;
  type: string;
  value: string;
  ttl: number;
  prio?: number;
  disabled?: boolean;
  warnings?: string[];
  notes?: string;
}

interface Connection {
  id: number;
  app_slug: string;
  name: string;
  status: string;
  dns_bound: boolean;
  dns_default: boolean;
  registrar_bound: boolean;
  registrar_default: boolean;
}

interface DomainAvailability {
  domain: string;
  available: boolean;
  known?: boolean;
  min_duration?: number;
  provider: string;
  connection_id?: number;
  source?: string;
  confidence?: string;
  warning?: string;
  price?: string;
  currency?: string;
  premium?: boolean;
  raw?: unknown;
}

interface RegistrationIntent {
  status?: string;
  error?: string;
  confirmation_token: string;
  expires_at: string;
  domain: string;
  years: number;
  auto_renew: boolean;
  whois_privacy: boolean;
  provider: string;
  connection_id: number;
  price?: string;
  currency?: string;
  premium?: boolean;
}

function providerLabel(slug: string): string {
  if (slug === "porkbun") return "Porkbun";
  if (slug === "namecheap") return "Namecheap";
  if (slug === "ionos") return "IONOS";
  if (slug === "spaceship") return "Spaceship";
  if (slug === "rdap") return "Public RDAP";
  return slug;
}

const API = "/api/apps/domains";
const RECORD_TYPES = [
  "A",
  "AAAA",
  "CNAME",
  "MX",
  "TXT",
  "NS",
  "SRV",
  "CAA",
  "ALIAS",
  "PTR",
  "HTTPS",
  "SVCB",
  "TLSA",
] as const;

// Shared input class. Same tokens messaging uses so the look matches
// across the dashboard's dark theme.
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

export default function DomainsPanel(props: NativePanelProps) {
  return (
    <ScopedDomainsPanel
      key={`${props.installId}:${props.projectId}`}
      {...props}
    />
  );
}

function ScopedDomainsPanel({ projectId, installId }: NativePanelProps) {
  const [domains, setDomains] = useState<Domain[]>([]);
  const [connections, setConnections] = useState<Connection[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [selected, setSelected] = useState<Domain | null>(null);
  const [view, setView] = useState<"inventory" | "register">("inventory");

  const activeConnections = useMemo(
    () =>
      connections
        .filter(
          (connection) => (connection.status || "").toLowerCase() === "active",
        )
        .sort(
          (a, b) =>
            Number(b.dns_bound) - Number(a.dns_bound) ||
            a.app_slug.localeCompare(b.app_slug),
        ),
    [connections],
  );

  const withParams = useCallback(
    (extra: Record<string, string>) => {
      return new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString();
    },
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(
      method: string,
      path: string,
      params?: Record<string, string>,
      body?: unknown,
    ): Promise<T> => {
      const opts: RequestInit = {
        method,
        credentials: "same-origin",
        headers: {},
      };
      if (body) {
        (opts.headers as Record<string, string>)["Content-Type"] =
          "application/json";
        opts.body = JSON.stringify(body);
      }
      const qs = withParams(params || {});
      const res = await fetch(`${API}${path}?${qs}`, opts);
      if (!res.ok) {
        const text = await res.text().catch(() => "");
        throw new Error(`${res.status}: ${text}`);
      }
      return res.json();
    },
    [withParams],
  );

  const loadVersion = useRef(0);
  useEffect(
    () => () => {
      loadVersion.current++;
    },
    [],
  );
  const reload = useCallback(async () => {
    const version = ++loadVersion.current;
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ domains: Domain[] }>("GET", "/domains", {});
      if (version === loadVersion.current) setDomains(r.domains || []);
    } catch (e) {
      if (version === loadVersion.current) setErr((e as Error).message);
    } finally {
      if (version === loadVersion.current) setBusy(false);
    }
  }, [api]);

  useEffect(() => {
    reload();
  }, [reload]);

  // Fetch the project's compatible DNS connections so the Add form
  // can offer per-domain pinning. Soft-fail: if the platform doesn't
  // grant connections.read, the form falls back to "Default" only.
  useEffect(() => {
    api<{ connections: Connection[] }>("GET", "/connections")
      .then((r) => setConnections(r.connections || []))
      .catch(() => setConnections([]));
  }, [api]);

  const callTool = useCallback(
    async (tool: string, args: Record<string, unknown>) => {
      // Inject _project_id so globally-scoped installs resolve the project:
      // a global sidecar has no APTEVA_PROJECT_ID env, so the tool's
      // resolveProjectFromArgs falls back to this. Project-scoped installs
      // ignore it — the env var wins. Mirrors the CRM panel.
      return api<Record<string, unknown>>(
        "POST",
        "/tools/call",
        {},
        {
          tool,
          args: { ...args, _project_id: projectId },
        },
      );
    },
    [api, projectId],
  );

  return (
    <div className="h-full flex flex-col">
      <div className="px-4 md:px-6 pt-5 md:pt-6 pb-3 flex flex-wrap items-center justify-between gap-3 border-b border-border">
        <div className="flex flex-wrap items-center gap-4">
          <h1 className="text-lg font-semibold">Domains</h1>
          <div className="flex rounded border border-border overflow-hidden text-xs">
            <button
              type="button"
              className={`px-3 py-1 ${view === "inventory" ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
              onClick={() => setView("inventory")}
            >
              Inventory
            </button>
            <button
              type="button"
              className={`px-3 py-1 border-l border-border ${view === "register" ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
              onClick={() => setView("register")}
            >
              Register
            </button>
          </div>
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading…</span>}
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={reload}
          >
            Refresh
          </button>
        </div>
      </div>

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      {view === "inventory" ? (
        <>
          <DNSRecoveryNotice callTool={callTool} />
          <AddDomainForm
            connections={activeConnections}
            onAdded={(d) => {
              reload();
              if (d) setSelected(d);
            }}
            callTool={callTool}
          />

          <div className="flex-1 min-h-0 flex flex-col md:flex-row">
            <div className="w-full md:w-72 max-h-56 md:max-h-none overflow-auto border-b md:border-b-0 border-border">
              <DomainList
                rows={domains}
                onSelect={setSelected}
                onRemoved={() => {
                  reload();
                  setSelected(null);
                }}
                callTool={callTool}
                selectedId={selected?.id}
              />
            </div>
            <div className="flex-1 min-w-0 md:border-l border-border">
              {selected ? (
                <RecordsPane
                  key={`${installId}:${projectId}:${selected.id}:${selected.connection_id}`}
                  domain={selected}
                  onClose={() => setSelected(null)}
                  api={api}
                  callTool={callTool}
                  onUpdated={(d) => {
                    setSelected(d);
                    reload();
                  }}
                  connections={activeConnections}
                />
              ) : (
                <div className="p-6 text-text-dim text-sm">
                  Select a domain to view its DNS records.
                </div>
              )}
            </div>
          </div>
        </>
      ) : (
        <RegisterDomainPane
          connections={activeConnections.filter(
            (c) => c.app_slug === "porkbun" || c.app_slug === "spaceship",
          )}
          callTool={callTool}
          onRegistered={(d) => {
            reload();
            if (d) setSelected(d);
            setView("inventory");
          }}
        />
      )}
    </div>
  );
}

// ─── Add domain form ─────────────────────────────────────────────

// "default"  → no connection_id sent; backend snapshots the role binding.
// "other"    → no connection_id, sends skip_validation; provider unknown.
// "<id>"     → pin this domain to that specific connection.
type ConnectionChoice = "default" | "other" | string;

function AddDomainForm({
  connections,
  onAdded,
  callTool,
}: {
  connections: Connection[];
  onAdded: (domain?: Domain) => void;
  callTool: (
    tool: string,
    args: Record<string, unknown>,
  ) => Promise<Record<string, unknown>>;
}) {
  const [name, setName] = useState("");
  const [pick, setPick] = useState<ConnectionChoice>("default");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const boundConnections = connections.filter(
    (connection) => connection.dns_bound,
  );
  const otherConnections = connections.filter(
    (connection) => !connection.dns_bound,
  );
  const defaultConnection = connections.find(
    (connection) => connection.dns_default,
  );

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const args: Record<string, unknown> = {
        name: name.trim(),
        notes: notes.trim(),
      };
      if (pick === "other") {
        args.skip_validation = true;
        args.use_default_connection = false;
      } else if (pick !== "default") {
        args.connection_id = parseInt(pick, 10);
      }
      const result = await callTool("domain_add", args);
      setName("");
      setNotes("");
      setPick("default");
      onAdded(result.domain as Domain | undefined);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      onSubmit={submit}
      className="p-4 border-b border-border flex gap-2 items-end flex-wrap"
    >
      <Field label="Domain">
        <input
          className={inputCls + " w-72"}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="acme.com"
          required
        />
      </Field>
      <Field label="DNS connection">
        <select
          className={inputCls + " w-56"}
          value={pick}
          onChange={(e) => setPick(e.target.value as ConnectionChoice)}
        >
          <option value="default">
            {defaultConnection
              ? `Default - ${providerLabel(defaultConnection.app_slug)} / ${defaultConnection.name || `connection ${defaultConnection.id}`}`
              : "Default DNS connection"}
          </option>
          {boundConnections.length > 0 && (
            <optgroup label="Connected DNS providers">
              {boundConnections.map((c) => (
                <option key={c.id} value={String(c.id)}>
                  {providerLabel(c.app_slug)} - {c.name || `connection ${c.id}`}
                  {c.dns_default ? " (default)" : ""}
                </option>
              ))}
            </optgroup>
          )}
          {otherConnections.length > 0 && (
            <optgroup label="Other available connections">
              {otherConnections.map((c) => (
                <option key={c.id} value={String(c.id)}>
                  {providerLabel(c.app_slug)} - {c.name || `connection ${c.id}`}
                </option>
              ))}
            </optgroup>
          )}
          <option value="other">Other / unknown</option>
        </select>
      </Field>
      <Field label="Notes (optional)">
        <input
          className={inputCls + " w-64"}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder="primary marketing domain"
        />
      </Field>
      <button
        type="submit"
        disabled={busy || !name.trim()}
        className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
      >
        {busy ? "Adding…" : "Add domain"}
      </button>
      {err && <div className="text-xs text-red-400 w-full">{err}</div>}
    </form>
  );
}

// ─── Register domain form ────────────────────────────────────────

function RegisterDomainPane({
  connections,
  callTool,
  onRegistered,
}: {
  connections: Connection[];
  callTool: (
    tool: string,
    args: Record<string, unknown>,
  ) => Promise<Record<string, unknown>>;
  onRegistered: (domain?: Domain) => void;
}) {
  const [name, setName] = useState("");
  const [pick, setPick] = useState<ConnectionChoice>("default");

  const autoRenew = true;
  const [whoisPrivacy, setWhoisPrivacy] = useState(true);

  const [notes, setNotes] = useState("");
  const [availability, setAvailability] = useState<DomainAvailability | null>(
    null,
  );
  const [intent, setIntent] = useState<RegistrationIntent | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const boundConnections = connections.filter(
    (connection) => connection.registrar_bound,
  );
  const otherConnections = connections.filter(
    (connection) => !connection.registrar_bound,
  );
  const defaultConnection =
    connections.find((connection) => connection.registrar_default) ||
    connections.find((connection) => connection.dns_default);

  const [pending, setPending] = useState<RegistrationIntent[]>([]);
  const loadPending = useCallback(
    () =>
      callTool("domain_registration_status", {})
        .then((r) => setPending((r.intents as RegistrationIntent[]) || []))
        .catch((e) => setErr((e as Error).message)),
    [callTool],
  );
  useEffect(() => {
    loadPending();
  }, [loadPending]);
  const cancelIntent = async () => {
    if (!intent) return;
    if (intent.status === "prepared") {
      setBusy(true);
      try {
        await callTool("domain_registration_status", {
          confirmation_token: intent.confirmation_token,
          cancel: true,
        });
      } catch (e) {
        setErr((e as Error).message);
        return;
      } finally {
        setBusy(false);
      }
    }
    setIntent(null);
    loadPending();
  };
  const selectedConnection = useMemo(() => {
    if (pick === "default" || pick === "other") return null;
    return connections.find((c) => String(c.id) === pick) || null;
  }, [connections, pick]);
  const providerCanRegister =
    availability?.available &&
    availability.known === true &&
    !availability.premium &&
    availability.provider === "porkbun";

  const argsForProvider = (): Record<string, unknown> => {
    const args: Record<string, unknown> = { domain: name.trim() };
    if (pick !== "default" && pick !== "other") {
      args.connection_id = parseInt(pick, 10);
    }
    return args;
  };

  const check = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    setBusy(true);
    setErr("");
    setAvailability(null);
    try {
      const result = await callTool(
        "domain_availability_check",
        argsForProvider(),
      );
      setAvailability(result.availability as DomainAvailability);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const prepareRegistration = async () => {
    if (!availability?.available) return;
    setBusy(true);
    setErr("");
    try {
      const result = await callTool("domain_registration_prepare", {
        ...argsForProvider(),
        years: availability.min_duration,
        auto_renew: true,
        whois_privacy: whoisPrivacy,
        notes: notes.trim(),
      });
      setIntent(result as unknown as RegistrationIntent);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const register = async () => {
    if (!intent) return;
    setBusy(true);
    setErr("");
    try {
      const result = await callTool("domain_register", {
        confirmation_token: intent.confirmation_token,
        resume: intent.status === "unknown" || intent.status === "processing",
      });
      setName("");

      setNotes("");
      setAvailability(null);
      setIntent(null);
      onRegistered(result.domain as Domain | undefined);
    } catch (e) {
      setErr((e as Error).message);
      try {
        const status = await callTool("domain_registration_status", {
          confirmation_token: intent.confirmation_token,
        });
        setIntent(status as unknown as RegistrationIntent);
      } catch {
        /* Keep original token for safe recovery. */
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex-1 min-h-0 overflow-auto">
      {pending.length > 0 && (
        <div className="p-4 border-b border-border space-y-2">
          <div>Pending purchases</div>
          {pending.map((p) => (
            <button
              key={p.confirmation_token}
              type="button"
              className="block text-accent"
              onClick={() => {
                setErr("");
                setIntent(p);
              }}
            >
              {p.domain} — {p.status}: review
            </button>
          ))}
        </div>
      )}
      <form
        onSubmit={check}
        className="p-4 border-b border-border flex gap-2 items-end flex-wrap"
      >
        <Field label="Domain">
          <input
            className={inputCls + " w-72"}
            disabled={busy}
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setAvailability(null);
            }}
            placeholder="acme.com"
            required
          />
        </Field>
        <Field label="Registrar connection">
          <select
            className={inputCls + " w-64"}
            disabled={busy}
            value={pick}
            onChange={(e) => {
              setPick(e.target.value as ConnectionChoice);
              setAvailability(null);
            }}
          >
            <option value="default">
              {defaultConnection
                ? `Default - ${providerLabel(defaultConnection.app_slug)} / ${defaultConnection.name || `connection ${defaultConnection.id}`}`
                : "Default registrar connection"}
            </option>
            {boundConnections.length > 0 && (
              <optgroup label="Connected registrar providers">
                {boundConnections.map((c) => (
                  <option key={c.id} value={String(c.id)}>
                    {providerLabel(c.app_slug)} -{" "}
                    {c.name || `connection ${c.id}`}
                    {c.registrar_default ? " (default)" : ""}
                  </option>
                ))}
              </optgroup>
            )}
            {otherConnections.length > 0 && (
              <optgroup label="Other available connections">
                {otherConnections.map((c) => (
                  <option key={c.id} value={String(c.id)}>
                    {providerLabel(c.app_slug)} -{" "}
                    {c.name || `connection ${c.id}`}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </Field>
        <button
          type="submit"
          disabled={busy || !name.trim()}
          className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
        >
          {busy ? "Checking..." : "Check availability"}
        </button>
        {err && <div className="text-xs text-red-400 w-full">{err}</div>}
      </form>

      {availability && (
        <div className="p-5 max-w-3xl">
          <div className="rounded border border-border bg-surface-2/40 p-4 space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div>
                <div className="font-medium">{availability.domain}</div>
                <div className="text-xs text-text-dim mt-1">
                  {providerLabel(availability.provider)}
                  {availability.connection_id
                    ? ` - connection ${availability.connection_id}`
                    : ""}
                  {availability.confidence
                    ? ` - ${availability.confidence}`
                    : ""}
                </div>
              </div>
              <span
                className={
                  availability.available
                    ? "text-green text-sm"
                    : "text-red text-sm"
                }
              >
                {availability.known === false
                  ? "Availability unconfirmed"
                  : availability.available
                    ? "Available"
                    : "Unavailable"}
              </span>
            </div>

            {availability.price && (
              <div className="text-sm">
                <span className="text-text-dim">Registration price </span>
                <span className="font-mono">
                  {availability.price} {availability.currency || "USD"}
                </span>
                {availability.premium && (
                  <span className="ml-2 text-yellow-400">premium</span>
                )}
              </div>
            )}

            {availability.warning && (
              <div className="text-xs text-yellow-300 border border-yellow-500/30 bg-yellow-500/10 rounded p-2">
                {availability.warning}
              </div>
            )}

            {availability.available && !providerCanRegister && (
              <div className="text-xs text-yellow-300 border border-yellow-500/30 bg-yellow-500/10 rounded p-2">
                {providerLabel(availability.provider)} confirmed availability,
                but this app only performs paid domain registration through
                Porkbun.
              </div>
            )}

            {providerCanRegister && (
              <div className="flex gap-3 items-end flex-wrap">
                <div className="text-xs text-text-dim">
                  Registry term: {availability.min_duration || "unconfirmed"}{" "}
                  year(s)
                </div>
                <Field label="Notes">
                  <input
                    className={inputCls + " w-56"}
                    value={notes}
                    onChange={(e) => setNotes(e.target.value)}
                    placeholder="optional"
                  />
                </Field>
                <label className="flex items-center gap-2 text-xs text-text-dim py-2">
                  <input type="checkbox" checked={autoRenew} disabled />
                  auto-renew (registrar default)
                </label>
                <label className="flex items-center gap-2 text-xs text-text-dim py-2">
                  <input
                    type="checkbox"
                    checked={whoisPrivacy}
                    onChange={(e) => setWhoisPrivacy(e.target.checked)}
                  />
                  WHOIS privacy
                </label>
                <button
                  type="button"
                  disabled={busy}
                  onClick={prepareRegistration}
                  className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
                >
                  {busy ? "Preparing..." : "Review purchase"}
                </button>
              </div>
            )}
          </div>
        </div>
      )}
      {intent && (
        <ConfirmDialog
          title="Confirm domain registration"
          confirmLabel={
            busy
              ? "Registering..."
              : intent.status === "unknown" || intent.status === "processing"
                ? "Resume same purchase"
                : "Register and pay"
          }
          busy={busy}
          onCancel={cancelIntent}
          onConfirm={register}
        >
          <div className="space-y-2 text-sm">
            <div>
              <span className="text-text-dim">Domain </span>
              <span className="font-mono">{intent.domain}</span>
            </div>
            <div>
              <span className="text-text-dim">Term </span>
              {intent.years} year{intent.years === 1 ? "" : "s"}
            </div>
            <div>
              <span className="text-text-dim">Provider </span>
              {providerLabel(intent.provider)} / connection{" "}
              {intent.connection_id}
            </div>
            <div>
              Auto-renew: {intent.auto_renew ? "on" : "off"}; WHOIS privacy:{" "}
              {intent.whois_privacy ? "on" : "off"}
            </div>
            <div>
              Quote expires: {new Date(intent.expires_at).toLocaleString()}
            </div>
            {intent.status !== "prepared" && (
              <p>
                Purchase status: {intent.status}. Resuming uses the original
                payment request and idempotency key.
              </p>
            )}
            {intent.price && (
              <div>
                <span className="text-text-dim">Quoted price </span>
                <span className="font-mono">
                  {intent.price} {intent.currency || "USD"}
                </span>
              </div>
            )}
            <p className="text-yellow-300">
              This action spends real money and cannot be undone.
            </p>
            {err && <div className="text-xs text-red-400">{err}</div>}
          </div>
        </ConfirmDialog>
      )}
    </div>
  );
}

// ─── Domain list ─────────────────────────────────────────────────

function DomainList({
  rows,
  onSelect,
  onRemoved,
  callTool,
  selectedId,
}: {
  rows: Domain[];
  onSelect: (d: Domain) => void;
  onRemoved: () => void;
  callTool: (
    tool: string,
    args: Record<string, unknown>,
  ) => Promise<Record<string, unknown>>;
  selectedId?: number;
}) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const matches = rows.filter((r) =>
    `${r.name} ${r.notes || ""}`.toLowerCase().includes(query.toLowerCase()),
  );
  const [pendingRemove, setPendingRemove] = useState<Domain | null>(null);
  const [removeError, setRemoveError] = useState("");
  const [removing, setRemoving] = useState(false);
  if (rows.length === 0) {
    return (
      <div className="p-6 text-text-dim text-sm">
        No domains yet. Add one above.
      </div>
    );
  }
  const remove = async () => {
    if (!pendingRemove) return;
    setRemoving(true);
    setRemoveError("");
    try {
      await callTool("domain_remove", { name: pendingRemove.name });
      setPendingRemove(null);
      onRemoved();
    } catch (e) {
      setRemoveError((e as Error).message);
    } finally {
      setRemoving(false);
    }
  };
  return (
    <div className="text-sm">
      {removeError && (
        <div className="m-3 text-xs text-red-400">{removeError}</div>
      )}
      <input
        aria-label="Search domains"
        className={inputCls}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setPage(0);
        }}
        placeholder="Search domains"
      />
      <div className="flex gap-2 p-2">
        <button disabled={page === 0} onClick={() => setPage((p) => p - 1)}>
          Previous
        </button>
        <span>{matches.length} domains</span>
        <button
          disabled={(page + 1) * 100 >= matches.length}
          onClick={() => setPage((p) => p + 1)}
        >
          Next
        </button>
      </div>
      {matches.slice(page * 100, (page + 1) * 100).map((d) => (
        <div
          key={d.id}
          className={`border-b border-border cursor-pointer hover:bg-surface-2 px-4 py-3 ${selectedId === d.id ? "bg-surface-2" : ""}`}
          onClick={() => onSelect(d)}
        >
          <div className="flex items-center justify-between gap-2 mb-1">
            <span className="font-medium truncate">{d.name}</span>
            <button
              type="button"
              className="text-text-dim hover:text-red-400 text-xs"
              onClick={(e) => {
                e.stopPropagation();
                setRemoveError("");
                setPendingRemove(d);
              }}
            >
              Remove
            </button>
          </div>
          <div className="text-xs text-text-dim truncate">
            {d.dns_provider_slug
              ? providerLabel(d.dns_provider_slug)
              : "no provider"}
            {d.notes ? ` · ${d.notes}` : ""}
          </div>
        </div>
      ))}
      {pendingRemove && (
        <ConfirmDialog
          title="Remove domain from inventory"
          confirmLabel={removing ? "Removing..." : "Remove"}
          danger
          busy={removing}
          onCancel={() => setPendingRemove(null)}
          onConfirm={remove}
        >
          <p className="text-sm">
            Remove <span className="font-mono">{pendingRemove.name}</span> from
            this app? The registration and DNS records at the provider remain
            untouched.
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}

// ─── Records pane (right-side detail) ────────────────────────────

interface ToolCaller {
  (
    tool: string,
    args: Record<string, unknown>,
  ): Promise<Record<string, unknown>>;
}

function RecordsPane({
  domain,
  onClose,
  api,
  callTool,
  onUpdated,
  connections,
}: {
  domain: Domain;
  onClose: () => void;
  onUpdated: (d: Domain) => void;
  connections: Connection[];
  api: <T>(
    m: string,
    p: string,
    q?: Record<string, string>,
    b?: unknown,
  ) => Promise<T>;
  callTool: ToolCaller;
}) {
  const [records, setRecords] = useState<DNSRecord[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [mailMode, setMailMode] = useState("");
  const [mailModeRequired, setMailModeRequired] = useState(false);
  const [filter, setFilter] = useState<string>("ALL");

  const [activeConnection, setActiveConnection] = useState<number | undefined>(
    domain.connection_id,
  );
  const [caps, setCaps] = useState<{
    write_types: string[];
    delete_types: string[];
    min_ttl: number;
    max_ttl: number;
  } | null>(null);
  const [page, setPage] = useState(0);
  const [search, setSearch] = useState("");
  const loadVersion = useRef(0);
  useEffect(
    () => () => {
      loadVersion.current++;
    },
    [],
  );
  const reload = useCallback(async () => {
    const version = ++loadVersion.current;
    setBusy(true);
    setRecords([]);
    setCaps(null);
    setErr("");
    if (domain.connection_mode === "unmanaged") {
      setBusy(false);
      return;
    }
    try {
      const r = await callTool("domain_records_list", { domain: domain.name });
      if (version !== loadVersion.current) return;
      setRecords((r.records as DNSRecord[]) || []);
      setMailMode((r.namecheap_email_type as string) || "");
      setMailModeRequired(!!r.namecheap_email_type_required);
      setCaps(r.capabilities as typeof caps);
      setActiveConnection(r.connection_id as number);
    } catch (e) {
      if (version === loadVersion.current) setErr((e as Error).message);
    } finally {
      if (version === loadVersion.current) setBusy(false);
    }
  }, [callTool, domain.name, domain.connection_mode]);

  useEffect(() => {
    reload();
  }, [reload]);

  const filtered = useMemo(
    () =>
      records.filter(
        (r) =>
          (filter === "ALL" || r.type === filter) &&
          `${r.name} ${r.value}`.toLowerCase().includes(search.toLowerCase()),
      ),
    [records, filter, search],
  );
  const visible = filtered.slice(page * 100, (page + 1) * 100);
  useEffect(() => setPage(0), [filter, search, records]);

  return (
    <div className="h-full flex flex-col text-sm">
      <div className="p-5 pb-3 border-b border-border">
        <div className="flex items-center justify-between mb-3">
          <h3 className="font-semibold">
            {domain.name}
            {domain.dns_provider_slug && (
              <span className="ml-2 text-xs text-text-dim">
                via {domain.dns_provider_slug}
              </span>
            )}
          </h3>
          <button
            type="button"
            className="text-text-dim hover:text-text"
            onClick={onClose}
          >
            ×
          </button>
        </div>

        <DomainSettings
          domain={domain}
          connections={connections}
          callTool={callTool}
          onUpdated={onUpdated}
        />
        <div className="text-xs mb-2">
          {domain.connection_mode === "unmanaged"
            ? "Unmanaged — select a DNS connection to edit records"
            : `DNS account: ${connections.find((c) => c.id === domain.connection_id)?.name || domain.connection_id || "default"}`}
        </div>
        {domain.connection_mode !== "unmanaged" &&
          caps &&
          (!mailModeRequired || mailMode) && (
            <AddRecordForm
              domain={domain.name}
              onAdded={reload}
              callTool={callTool}
              caps={caps}
              connectionId={activeConnection}
              namecheapEmailType={mailMode || undefined}
            />
          )}

        {mailModeRequired && (
          <Field label="Current Namecheap mail routing">
            <p className="text-xs text-yellow-400">
              Namecheap omitted the mail configuration. Select the current
              setting to preserve mail delivery during DNS edits.
            </p>
            <select
              className={inputCls}
              value={mailMode}
              onChange={(e) => setMailMode(e.target.value)}
            >
              <option value="">Select current setting</option>
              <option value="MX">Custom MX records</option>
              <option value="MXE">Custom MXE</option>
              <option value="FWD">Namecheap email forwarding</option>
              <option value="OX">Namecheap Private Email</option>
            </select>
          </Field>
        )}
        <input
          aria-label="Search DNS records"
          className={inputCls}
          placeholder="Search records"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <div className="flex items-center gap-2 my-3">
          <span className="text-xs text-text-dim">Filter</span>
          <select
            className={inputCls + " w-32 py-1"}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          >
            <option value="ALL">All types</option>
            {Array.from(
              new Set([...RECORD_TYPES, ...records.map((r) => r.type)]),
            ).map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
          <div className="flex-1" />
          <button
            type="button"
            className="text-xs px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={reload}
            disabled={busy}
          >
            {busy ? "Loading…" : "Refresh"}
          </button>
        </div>

        {err && (
          <div className="p-2 rounded border border-red-500/30 bg-red-500/10 text-xs text-red-300 whitespace-pre-wrap">
            {err}
          </div>
        )}
      </div>

      <div className="px-5 flex gap-3 text-xs">
        <button disabled={page === 0} onClick={() => setPage((p) => p - 1)}>
          Previous
        </button>
        <span>
          {filtered.length} records — page {page + 1}
        </span>
        <button
          disabled={(page + 1) * 100 >= filtered.length}
          onClick={() => setPage((p) => p + 1)}
        >
          Next
        </button>
      </div>

      <div className="flex-1 min-h-0 overflow-auto p-3 md:p-5">
        {visible.length === 0 && !err ? (
          <div className="text-text-dim text-xs">No records.</div>
        ) : (
          <table className="w-full min-w-[42rem] text-xs">
            <thead className="text-xs text-text-dim">
              <tr className="border-b border-border">
                <th className="text-left px-2 py-1">Name</th>
                <th className="text-left px-2 py-1">Type</th>
                <th className="text-left px-2 py-1">Value</th>
                <th className="text-left px-2 py-1">TTL</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {visible.map((r) => (
                <RecordRow
                  key={`${r.id}-${r.name}-${r.type}`}
                  record={r}
                  domain={domain.name}
                  onChanged={reload}
                  callTool={callTool}
                  connectionId={activeConnection}
                  namecheapEmailType={mailMode || undefined}
                  canEdit={
                    !!caps?.write_types.includes(r.type) &&
                    (!mailModeRequired || !!mailMode)
                  }
                  canDelete={
                    !!caps?.delete_types.includes(r.type) &&
                    (!mailModeRequired || !!mailMode)
                  }
                />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function AddRecordForm({
  domain,
  onAdded,
  callTool,
  caps,
  connectionId,
  namecheapEmailType,
}: {
  domain: string;
  connectionId?: number;
  namecheapEmailType?: string;
  caps: { write_types: string[]; min_ttl: number; max_ttl: number };
  onAdded: () => void;
  callTool: ToolCaller;
}) {
  const [name, setName] = useState("@");
  const [type, setType] = useState<string>("A");
  const [value, setValue] = useState("");
  const [ttl, setTtl] = useState(Math.max(600, caps.min_ttl));
  const [mode, setMode] = useState("create");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await callTool("domain_records_set", {
        domain,
        name: name === "@" ? "" : name,
        type,
        value,
        ttl,
        mode,
        expected_connection_id: connectionId,
        namecheap_email_type: namecheapEmailType,
      });
      setValue("");
      setName("@");
      onAdded();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      onSubmit={submit}
      className="p-2 rounded bg-surface-2/40 border border-border space-y-2"
    >
      <div className="text-xs text-text-dim font-medium">Add record</div>
      <select
        aria-label="Record operation"
        value={mode}
        onChange={(e) => setMode(e.target.value)}
        className={inputCls}
      >
        <option value="create">Add another record</option>
        <option value="ensure">Ensure value exists</option>
        <option value="upsert">Update the single matching record</option>
      </select>
      <div className="flex flex-wrap gap-2 items-end">
        <Field label="Name">
          <input
            className={inputCls + " w-24 py-1"}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="@"
          />
        </Field>
        <Field label="Type">
          <select
            className={inputCls + " w-24 py-1"}
            value={type}
            onChange={(e) => setType(e.target.value)}
          >
            {caps.write_types.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Value">
          <input
            className={inputCls + " py-1 w-full sm:min-w-[14rem]"}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={
              type === "MX"
                ? "10 mail.acme.com"
                : type === "CNAME"
                  ? "target.acme.com"
                  : type === "TXT"
                    ? "v=spf1 include:_spf.acme.com ~all"
                    : type === "SRV"
                      ? "10 5 443 service.acme.com"
                      : type === "CAA"
                        ? "0 issue letsencrypt.org"
                        : "1.2.3.4"
            }
            required
          />
        </Field>
        <Field label="TTL">
          <input
            type="number"
            className={inputCls + " w-20 py-1"}
            value={ttl}
            onChange={(e) => setTtl(parseInt(e.target.value, 10) || 600)}
            min={caps.min_ttl}
            max={caps.max_ttl}
          />
        </Field>
        <button
          type="submit"
          disabled={busy || !value.trim()}
          className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50 text-xs"
        >
          {busy ? "Saving…" : "Save"}
        </button>
      </div>
      {err && <div className="text-xs text-red-400">{err}</div>}
      {type === "MX" && (
        <div className="text-xs text-text-dim">
          MX value format: priority then host, e.g.{" "}
          <code>10 inbound-smtp.eu-west-1.amazonaws.com</code>
        </div>
      )}
    </form>
  );
}

function RecordRow({
  record,
  domain,
  onChanged,
  callTool,
  canEdit,
  canDelete,
  connectionId,
  namecheapEmailType,
}: {
  record: DNSRecord;
  connectionId?: number;
  namecheapEmailType?: string;
  canEdit: boolean;
  canDelete: boolean;
  domain: string;
  onChanged: () => void;
  callTool: ToolCaller;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(editableRecordValue(record));
  const [ttl, setTtl] = useState(record.ttl || 600);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    setValue(editableRecordValue(record));
    setTtl(record.ttl || 600);
  }, [record.value, record.prio, record.ttl, record.type]);

  // Strip the FQDN suffix for display when the provider returns
  // fully-qualified names like "mail.acme.com" — show "mail" instead.
  const shortName = useMemo(() => {
    if (record.name === domain) return "@";
    if (record.name.endsWith("." + domain)) {
      return record.name.slice(0, -("." + domain).length);
    }
    return record.name;
  }, [record.name, domain]);

  const save = async () => {
    setBusy(true);
    setErr("");
    try {
      await callTool("domain_records_set", {
        domain,
        name: shortName === "@" ? "" : shortName,
        type: record.type,
        value,
        ttl,
        record_id: record.id,
        expected_connection_id: connectionId,
        namecheap_email_type: namecheapEmailType,
        expected_record: {
          value: record.value,
          prio: record.prio || 0,
          ttl: record.ttl,
        },
      });
      setEditing(false);
      onChanged();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    setErr("");
    try {
      await callTool("domain_records_delete", {
        domain,
        name: shortName === "@" ? "" : shortName,
        type: record.type,
        record_id: record.id,
        expected_connection_id: connectionId,
        namecheap_email_type: namecheapEmailType,
        expected_record: {
          value: record.value,
          prio: record.prio || 0,
          ttl: record.ttl,
        },
      });
      setConfirmDelete(false);
      onChanged();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <tr className="border-b border-border align-top">
        <td className="px-2 py-1 font-mono text-text break-all">
          {shortName}
          {record.disabled && <span> (disabled)</span>}
          {record.warnings?.map((w) => (
            <div key={w} className="text-yellow-400">
              {w}
            </div>
          ))}
        </td>
        <td className="px-2 py-1 text-text-dim">{record.type}</td>
        <td className="px-2 py-1">
          {editing ? (
            <input
              className={inputCls + " py-0.5 text-xs"}
              value={value}
              onChange={(e) => setValue(e.target.value)}
            />
          ) : (
            <span className="font-mono break-all">
              {(record.type === "MX" || record.type === "SRV") &&
              record.prio !== undefined
                ? `${record.prio} `
                : ""}
              {record.value}
            </span>
          )}
        </td>
        <td className="px-2 py-1 text-text-dim">
          {editing ? (
            <input
              type="number"
              className={inputCls + " w-16 py-0.5 text-xs"}
              value={ttl}
              onChange={(e) => setTtl(parseInt(e.target.value, 10) || 600)}
            />
          ) : (
            record.ttl
          )}
        </td>
        <td className="px-2 py-1 text-right whitespace-nowrap">
          {editing ? (
            <>
              <button
                type="button"
                disabled={busy}
                onClick={save}
                className="text-xs text-accent hover:underline"
              >
                Save
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => {
                  setValue(editableRecordValue(record));
                  setTtl(record.ttl);
                  setErr("");
                  setEditing(false);
                }}
                className="text-xs text-text-dim ml-2 hover:text-text"
              >
                Cancel
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                disabled={busy || !canEdit}
                onClick={() => setEditing(true)}
                className="text-xs text-text-dim hover:text-text"
              >
                Edit
              </button>
              <button
                type="button"
                disabled={busy || !canDelete}
                onClick={() => setConfirmDelete(true)}
                className="text-xs text-text-dim hover:text-red-400 ml-2"
              >
                Delete
              </button>
            </>
          )}
        </td>
      </tr>
      {err && (
        <tr>
          <td colSpan={5} className="px-2 py-1 text-xs text-red-400">
            {err}
          </td>
        </tr>
      )}
      {confirmDelete && (
        <tr>
          <td colSpan={5} className="p-0">
            <ConfirmDialog
              title="Delete DNS record"
              confirmLabel={busy ? "Deleting..." : "Delete record"}
              danger
              busy={busy}
              onCancel={() => setConfirmDelete(false)}
              onConfirm={remove}
            >
              <p className="text-sm">
                Delete only this <strong>{record.type}</strong> record for{" "}
                <span className="font-mono">
                  {shortName === "@" ? domain : `${shortName}.${domain}`}
                </span>
                ?
              </p>
            </ConfirmDialog>
          </td>
        </tr>
      )}
    </>
  );
}

function editableRecordValue(record: DNSRecord): string {
  if (
    (record.type === "MX" || record.type === "SRV") &&
    record.prio !== undefined
  ) {
    return `${record.prio} ${record.value}`.trim();
  }
  return record.value;
}

// ─── Tiny shared primitives ──────────────────────────────────────

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      {children}
    </label>
  );
}

function ConfirmDialog({
  title,
  confirmLabel,
  danger = false,
  busy = false,
  onCancel,
  onConfirm,
  children,
}: {
  title: string;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  children: React.ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const busyRef = useRef(busy);
  busyRef.current = busy;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const dialog = dialogRef.current;
    dialog?.querySelector<HTMLButtonElement>("button")?.focus();
    const keepFocus = (event: FocusEvent) => {
      if (dialog && !dialog.contains(event.target as Node)) dialog.focus();
    };
    document.addEventListener("focusin", keepFocus);
    return () => {
      document.removeEventListener("focusin", keepFocus);
      previous?.focus();
    };
  }, []);
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Tab") {
        const items = Array.from(
          dialogRef.current?.querySelectorAll<HTMLElement>(
            'button:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex="0"]',
          ) || [],
        );
        const first = items[0],
          last = items[items.length - 1];
        if (!first) {
          event.preventDefault();
          dialogRef.current?.focus();
        } else if (
          event.shiftKey &&
          (document.activeElement === first ||
            document.activeElement === dialogRef.current)
        ) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
      if (event.key === "Escape" && !busyRef.current) {
        event.preventDefault();
        event.stopPropagation();
        onCancel();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy, onCancel]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      role="presentation"
      onMouseDown={() => !busy && onCancel()}
    >
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="w-full max-w-md rounded border border-border bg-surface p-5 shadow-xl"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="text-base font-semibold">
          {title}
        </h2>
        <div className="mt-3 text-text-dim">{children}</div>
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={onCancel}
            className="px-3 py-1.5 rounded border border-border hover:bg-surface-2 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={onConfirm}
            className={`px-3 py-1.5 rounded text-white disabled:opacity-50 ${danger ? "bg-red-600 hover:bg-red-500" : "bg-accent"}`}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
