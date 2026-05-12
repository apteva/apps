// DomainsPanel — operator UI for the domains app.
//
// Two views:
//   - List: domains the project has registered with this app, with
//     add + remove. Click a row to open the records browser.
//   - Records: live-fetched from the bound DNS provider (Porkbun
//     today, Namecheap once XML support lands). Add / edit / delete
//     individual records.
//
// All mutations go through /api/apps/domains/tools/call with the
// generic tool dispatcher pattern messaging uses.

import { useCallback, useEffect, useMemo, useState } from "react";

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
  notes?: string;
}

interface Connection {
  id: number;
  app_slug: string;
  name: string;
  status: string;
}

function providerLabel(slug: string): string {
  if (slug === "porkbun") return "Porkbun";
  if (slug === "namecheap") return "Namecheap";
  return slug;
}

const API = "/api/apps/domains";
const RECORD_TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "ALIAS"] as const;

// Shared input class. Same tokens messaging uses so the look matches
// across the dashboard's dark theme.
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

export default function DomainsPanel({ projectId, installId }: NativePanelProps) {
  const [domains, setDomains] = useState<Domain[]>([]);
  const [connections, setConnections] = useState<Connection[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [selected, setSelected] = useState<Domain | null>(null);

  const withParams = useCallback((extra: Record<string, string>) => {
    return new URLSearchParams({
      project_id: projectId,
      install_id: String(installId),
      ...extra,
    }).toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(
    method: string, path: string,
    params?: Record<string, string>, body?: unknown,
  ): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const qs = withParams(params || {});
    const res = await fetch(`${API}${path}?${qs}`, opts);
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`${res.status}: ${text}`);
    }
    return res.json();
  }, [withParams]);

  const reload = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ domains: Domain[] }>("GET", "/domains", {});
      setDomains(r.domains || []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [api]);

  useEffect(() => { reload(); }, [reload]);

  // Fetch the project's compatible DNS connections so the Add form
  // can offer per-domain pinning. Soft-fail: if the platform doesn't
  // grant connections.read, the form falls back to "Default" only.
  useEffect(() => {
    api<{ connections: Connection[] }>("GET", "/connections")
      .then((r) => setConnections(r.connections || []))
      .catch(() => setConnections([]));
  }, [api]);

  const callTool = useCallback(async (tool: string, args: Record<string, unknown>) => {
    return api<Record<string, unknown>>("POST", "/tools/call", {}, { tool, args });
  }, [api]);

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <h1 className="text-lg font-semibold">Domains</h1>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading…</span>}
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={reload}
          >Refresh</button>
        </div>
      </div>

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      <div className="flex-1 min-h-0 flex">
        <div className="flex-1 min-w-0 overflow-auto">
          <AddDomainForm
            connections={connections}
            onAdded={(d) => { reload(); if (d) setSelected(d); }}
            callTool={callTool}
          />
          <DomainList
            rows={domains}
            onSelect={setSelected}
            onRemoved={() => { reload(); setSelected(null); }}
            callTool={callTool}
            selectedId={selected?.id}
          />
        </div>
        {selected && (
          <RecordsPane
            domain={selected}
            onClose={() => setSelected(null)}
            api={api}
            callTool={callTool}
          />
        )}
      </div>
    </div>
  );
}

// ─── Add domain form ─────────────────────────────────────────────

// "default"  → no connection_id sent; backend snapshots the role binding.
// "other"    → no connection_id, sends skip_validation; provider unknown.
// "<id>"     → pin this domain to that specific connection.
type ConnectionChoice = "default" | "other" | string;

function AddDomainForm({
  connections, onAdded, callTool,
}: {
  connections: Connection[];
  onAdded: (domain?: Domain) => void;
  callTool: (tool: string, args: Record<string, unknown>) => Promise<Record<string, unknown>>;
}) {
  const [name, setName] = useState("");
  const [pick, setPick] = useState<ConnectionChoice>("default");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

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
    <form onSubmit={submit} className="p-4 border-b border-border flex gap-2 items-end flex-wrap">
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
          <option value="default">Default (install binding)</option>
          {connections.map((c) => (
            <option key={c.id} value={String(c.id)}>
              {providerLabel(c.app_slug)} — {c.name || `connection ${c.id}`}
            </option>
          ))}
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

// ─── Domain list ─────────────────────────────────────────────────

function DomainList({
  rows, onSelect, onRemoved, callTool, selectedId,
}: {
  rows: Domain[];
  onSelect: (d: Domain) => void;
  onRemoved: () => void;
  callTool: (tool: string, args: Record<string, unknown>) => Promise<Record<string, unknown>>;
  selectedId?: number;
}) {
  if (rows.length === 0) {
    return <div className="p-6 text-text-dim text-sm">No domains yet. Add one above.</div>;
  }
  const remove = async (d: Domain) => {
    if (!confirm(`Remove ${d.name} from this app's inventory? (The actual registration at the provider is untouched.)`)) return;
    try {
      await callTool("domain_remove", { name: d.name });
      onRemoved();
    } catch (e) {
      alert((e as Error).message);
    }
  };
  return (
    <table className="w-full text-sm">
      <thead className="text-xs text-text-dim">
        <tr className="border-b border-border">
          <th className="text-left px-4 py-2">Domain</th>
          <th className="text-left px-4 py-2">Registrar</th>
          <th className="text-left px-4 py-2">DNS provider</th>
          <th className="text-left px-4 py-2">Notes</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {rows.map((d) => (
          <tr
            key={d.id}
            className={`border-b border-border cursor-pointer hover:bg-surface-2 ${selectedId === d.id ? "bg-surface-2" : ""}`}
            onClick={() => onSelect(d)}
          >
            <td className="px-4 py-2 font-medium">{d.name}</td>
            <td className="px-4 py-2 text-text-dim">{d.registrar_slug || "—"}</td>
            <td className="px-4 py-2 text-text-dim">{d.dns_provider_slug || "—"}</td>
            <td className="px-4 py-2 text-text-dim truncate max-w-md">{d.notes || ""}</td>
            <td className="px-4 py-2 text-right">
              <button
                type="button"
                className="text-text-dim hover:text-red-400 text-xs"
                onClick={(e) => { e.stopPropagation(); remove(d); }}
              >Remove</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ─── Records pane (right-side detail) ────────────────────────────

interface ToolCaller {
  (tool: string, args: Record<string, unknown>): Promise<Record<string, unknown>>;
}

function RecordsPane({
  domain, onClose, api, callTool,
}: {
  domain: Domain;
  onClose: () => void;
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  callTool: ToolCaller;
}) {
  const [records, setRecords] = useState<DNSRecord[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [filter, setFilter] = useState<string>("ALL");

  const reload = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await callTool("domain_records_list", { domain: domain.name });
      setRecords((r.records as DNSRecord[]) || []);
    } catch (e) {
      setRecords([]);
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [callTool, domain.name]);

  useEffect(() => { reload(); }, [reload]);

  const visible = useMemo(() => {
    if (filter === "ALL") return records;
    return records.filter((r) => r.type === filter);
  }, [records, filter]);

  return (
    <div className="w-[36rem] border-l border-border overflow-auto p-5 text-sm">
      <div className="flex items-center justify-between mb-3">
        <h3 className="font-semibold">
          {domain.name}
          {domain.dns_provider_slug && (
            <span className="ml-2 text-xs text-text-dim">via {domain.dns_provider_slug}</span>
          )}
        </h3>
        <button type="button" className="text-text-dim hover:text-text" onClick={onClose}>×</button>
      </div>

      <AddRecordForm
        domain={domain.name}
        onAdded={reload}
        callTool={callTool}
      />

      <div className="flex items-center gap-2 my-3">
        <span className="text-xs text-text-dim">Filter</span>
        <select
          className={inputCls + " w-32 py-1"}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        >
          <option value="ALL">All types</option>
          {RECORD_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <div className="flex-1" />
        <button
          type="button"
          className="text-xs px-2 py-1 rounded border border-border hover:bg-surface-2"
          onClick={reload}
        >{busy ? "Loading…" : "Refresh"}</button>
      </div>

      {err && (
        <div className="mb-3 p-2 rounded border border-red-500/30 bg-red-500/10 text-xs text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      {visible.length === 0 && !err ? (
        <div className="text-text-dim text-xs">No records.</div>
      ) : (
        <table className="w-full text-xs">
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
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function AddRecordForm({
  domain, onAdded, callTool,
}: {
  domain: string;
  onAdded: () => void;
  callTool: ToolCaller;
}) {
  const [name, setName] = useState("@");
  const [type, setType] = useState<string>("A");
  const [value, setValue] = useState("");
  const [ttl, setTtl] = useState(600);
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
    <form onSubmit={submit} className="p-2 rounded bg-surface-2/40 border border-border space-y-2">
      <div className="text-xs text-text-dim font-medium">Add or update record</div>
      <div className="flex gap-2 items-end">
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
            {RECORD_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        </Field>
        <Field label="Value">
          <input
            className={inputCls + " py-1 min-w-[14rem]"}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={
              type === "MX"
                ? "10 mail.acme.com"
                : type === "CNAME"
                  ? "target.acme.com"
                  : type === "TXT"
                    ? "v=spf1 include:_spf.acme.com ~all"
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
            min={60}
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
        <div className="text-xs text-text-dim">MX value format: priority then host, e.g. <code>10 inbound-smtp.eu-west-1.amazonaws.com</code></div>
      )}
    </form>
  );
}

function RecordRow({
  record, domain, onChanged, callTool,
}: {
  record: DNSRecord;
  domain: string;
  onChanged: () => void;
  callTool: ToolCaller;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(record.value);
  const [ttl, setTtl] = useState(record.ttl || 600);
  const [busy, setBusy] = useState(false);

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
    try {
      await callTool("domain_records_set", {
        domain,
        name: shortName === "@" ? "" : shortName,
        type: record.type,
        value,
        ttl,
      });
      setEditing(false);
      onChanged();
    } catch (e) {
      alert((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`Delete ${record.type} record for ${shortName}.${domain}?`)) return;
    setBusy(true);
    try {
      await callTool("domain_records_delete", {
        domain,
        name: shortName === "@" ? "" : shortName,
        type: record.type,
      });
      onChanged();
    } catch (e) {
      alert((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <tr className="border-b border-border align-top">
      <td className="px-2 py-1 font-mono text-text">{shortName}</td>
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
            {record.type === "MX" && record.prio ? `${record.prio} ` : ""}
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
        ) : record.ttl}
      </td>
      <td className="px-2 py-1 text-right whitespace-nowrap">
        {editing ? (
          <>
            <button type="button" disabled={busy} onClick={save} className="text-xs text-accent hover:underline">Save</button>
            <button type="button" disabled={busy} onClick={() => setEditing(false)} className="text-xs text-text-dim ml-2 hover:text-text">Cancel</button>
          </>
        ) : (
          <>
            <button type="button" disabled={busy} onClick={() => setEditing(true)} className="text-xs text-text-dim hover:text-text">Edit</button>
            <button type="button" disabled={busy} onClick={remove} className="text-xs text-text-dim hover:text-red-400 ml-2">Delete</button>
          </>
        )}
      </td>
    </tr>
  );
}

// ─── Tiny shared primitives ──────────────────────────────────────

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      {children}
    </label>
  );
}
