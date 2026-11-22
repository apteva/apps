import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type Severity = "critical" | "high" | "medium" | "low" | "info";
type Tab = "overview" | "scan" | "findings" | "hosts" | "targets";

interface Scan {
  id: number;
  profile: string;
  targets: string[];
  addresses?: string[];
  dropped?: Decision[];
  tcp_port_count: number;
  udp_ports: number[];
  started_by: string;
  status: string;
  phase: string;
  hosts_total: number;
  hosts_done: number;
  hosts_up: number;
  open_ports: number;
  findings_new: number;
  probes_sent: number;
  error?: string;
  created_at: string;
  completed_at?: string;
}

interface Decision { target: string; address?: string; hostname?: string; allowed: boolean; reason: string }
interface Target { id: number; kind: "allow" | "exclude"; value: string; note: string; created_at: string }
interface Settings { rate_pps: number; parallel_hosts: number; max_addresses: number; cve_lookup: boolean }

interface Finding {
  id: number;
  address: string;
  port: number;
  protocol: string;
  rule_id: string;
  title: string;
  severity: Severity;
  cvss: number;
  epss: number;
  kev: boolean;
  cve_ids: string[];
  evidence: string;
  remediation: string;
  status: string;
  note: string;
  first_seen: string;
  last_seen: string;
}

interface Service { port: number; protocol: string; service: string; product?: string; version?: string; tls: boolean; banner?: string; last_seen: string }
interface Host { id: number; address: string; hostname?: string; up: boolean; last_seen: string; services: Service[]; open_findings: number }

interface Overview {
  open_findings: Record<Severity, number>;
  recent_scans: Scan[];
  allowlist_entries: number;
  exclusions: number;
  hosts_up: number;
  open_services: number;
  settings: Settings;
}

const API = "/api/apps/security-scanner";
const SEVERITIES: Severity[] = ["critical", "high", "medium", "low", "info"];
const SEV_COLOR: Record<Severity, string> = {
  critical: "var(--color-red)",
  high: "var(--color-red)",
  medium: "var(--color-yellow)",
  low: "var(--color-blue)",
  info: "var(--color-text-dim)",
};
const controlClass = "w-full px-3 py-2 text-sm bg-bg border border-border rounded text-text";
const buttonClass = "px-3 py-1.5 text-xs border border-border rounded text-text disabled:opacity-40";
const primaryClass = "px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-40";

export default function SecurityPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [overview, setOverview] = useState<Overview | null>(null);
  const [scans, setScans] = useState<Scan[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const api = useCallback(async (path: string, init?: RequestInit) => {
    const separator = path.includes("?") ? "&" : "?";
    const response = await fetch(`${API}${path}${separator}project_id=${encodeURIComponent(projectId)}`, {
      credentials: "same-origin",
      ...init,
      headers: init?.body ? { "Content-Type": "application/json", ...(init.headers || {}) } : init?.headers,
    });
    if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
    return response.json();
  }, [projectId]);

  const load = useCallback(async () => {
    try {
      const [ov, sc] = await Promise.all([api("/overview"), api("/scans?limit=30")]);
      setOverview(ov);
      setScans(sc.scans || []);
    } catch (error) {
      setMessage(String((error as Error).message || error));
    }
  }, [api]);

  useEffect(() => { load(); }, [load]);

  const running = scans.some((s) => s.status === "running" || s.status === "queued");
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(load, 2000);
    return () => clearInterval(timer);
  }, [running, load]);

  const run = useCallback(async (label: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    setMessage(label);
    try {
      await fn();
      setMessage("");
      await load();
    } catch (error) {
      setMessage(String((error as Error).message || error));
    } finally {
      setBusy(false);
    }
  }, [load]);

  const stopAll = () => run("Stopping scans…", () => api("/scans/stop-all", { method: "POST" }));

  const tabs: Array<[Tab, string]> = [["overview", "Overview"], ["scan", "Scan"], ["findings", "Findings"], ["hosts", "Hosts"], ["targets", "Targets"]];

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <div className="shrink-0 px-4 py-3 border-b border-border flex items-center gap-1 flex-wrap">
        <ShieldIcon />
        <h1 className="text-sm font-medium mr-4 ml-2">Security Scanner</h1>
        {tabs.map(([key, label]) => (
          <button key={key} type="button" onClick={() => setTab(key)}
            className={`px-3 py-1.5 text-xs rounded ${tab === key ? "bg-bg-hover text-text font-medium" : "text-text-muted"}`}>{label}</button>
        ))}
        {running && <button type="button" onClick={stopAll} disabled={busy} className="ml-3 px-3 py-1.5 text-xs rounded font-medium text-bg" style={{ background: "var(--color-red)" }}>Stop all scans</button>}
        <div className="ml-auto text-xs text-text-muted max-w-xl text-right">{busy ? "Working…" : message}</div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        {tab === "overview" && <OverviewTab overview={overview} scans={scans} onGo={setTab} />}
        {tab === "scan" && <ScanTab api={api} run={run} busy={busy} scans={scans} allowCount={overview?.allowlist_entries || 0} onGo={setTab} />}
        {tab === "findings" && <FindingsTab api={api} projectId={projectId} setMessage={setMessage} />}
        {tab === "hosts" && <HostsTab api={api} />}
        {tab === "targets" && <TargetsTab api={api} run={run} busy={busy} />}
      </div>
    </div>
  );
}

// ---------- overview ----------

function OverviewTab({ overview, scans, onGo }: { overview: Overview | null; scans: Scan[]; onGo: (t: Tab) => void }) {
  if (!overview) return <div className="p-6 text-sm text-text-muted">Loading…</div>;
  if (overview.allowlist_entries === 0) {
    return (
      <div className="p-6">
        <section className="border border-border rounded-lg p-5 max-w-2xl">
          <h2 className="text-sm font-medium">Add targets before scanning</h2>
          <p className="mt-2 text-sm text-text-muted">The scanner only touches addresses on this project's allowlist. Add IP addresses, CIDR ranges or hostnames you own or are authorised to test.</p>
          <button type="button" onClick={() => onGo("targets")} className={`${primaryClass} mt-4`}>Open targets</button>
        </section>
      </div>
    );
  }
  return (
    <div className="p-4 space-y-4">
      <section className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        {SEVERITIES.map((sev) => (
          <button key={sev} type="button" onClick={() => onGo("findings")} className="border border-border rounded-lg p-4 text-left">
            <div className="text-xs text-text-muted flex items-center gap-2"><SevDot sev={sev} />{label(sev)}</div>
            <div className="mt-2 text-2xl font-semibold">{overview.open_findings[sev] || 0}</div>
          </button>
        ))}
      </section>
      <section className="grid grid-cols-3 gap-3">
        <Stat label="Hosts up" value={overview.hosts_up} />
        <Stat label="Open services" value={overview.open_services} />
        <Stat label="Allowlist entries" value={overview.allowlist_entries} />
      </section>
      <section className="border border-border rounded-lg overflow-hidden">
        <div className="px-4 py-3 border-b border-border flex items-center">
          <h2 className="text-sm font-medium">Recent scans</h2>
          <button type="button" onClick={() => onGo("scan")} className="ml-auto text-xs text-accent">New scan</button>
        </div>
        {scans.length === 0 ? <div className="px-4 py-6 text-sm text-text-muted">No scans yet.</div> : (
          <ul className="divide-y divide-border">{scans.slice(0, 8).map((s) => <ScanRow key={s.id} scan={s} />)}</ul>
        )}
      </section>
    </div>
  );
}

function Stat({ label: text, value }: { label: string; value: number }) {
  return <div className="border border-border rounded-lg p-4"><div className="text-xs text-text-muted">{text}</div><div className="mt-2 text-xl font-semibold">{value}</div></div>;
}

function ScanRow({ scan, onStop }: { scan: Scan; onStop?: () => void }) {
  const active = scan.status === "running" || scan.status === "queued";
  const pct = scan.hosts_total ? Math.round((scan.hosts_done / scan.hosts_total) * 100) : 0;
  return (
    <li className="px-4 py-3">
      <div className="flex items-center gap-3">
        <div className="text-sm font-medium">#{scan.id}</div>
        <div className="text-sm truncate">{scan.targets.join(", ")}</div>
        <div className="text-xs text-text-muted">{scan.profile}</div>
        <div className="ml-auto text-xs" style={{ color: scan.status === "failed" ? "var(--color-red)" : scan.status === "completed" ? "var(--color-green)" : "var(--color-text-muted)" }}>{scan.status}</div>
        {active && onStop && <button type="button" onClick={onStop} className={buttonClass}>Stop</button>}
      </div>
      {active && (
        <div className="mt-2 h-1.5 rounded bg-bg-hover overflow-hidden"><div className="h-full bg-accent" style={{ width: `${pct}%` }} /></div>
      )}
      <div className="mt-1 text-xs text-text-muted">
        {scan.hosts_up}/{scan.hosts_total} hosts up · {scan.open_ports} open ports · {scan.findings_new} new findings · {scan.probes_sent} probes
        {scan.phase ? ` · ${scan.phase}` : ""} · {scan.started_by} · {dateLabel(scan.created_at)}
      </div>
      {scan.error && <div className="mt-1 text-xs" style={{ color: "var(--color-yellow)" }}>{scan.error}</div>}
    </li>
  );
}

// ---------- scan ----------

type Api = (path: string, init?: RequestInit) => Promise<any>;
type Run = (label: string, fn: () => Promise<unknown>) => Promise<void>;

function ScanTab({ api, run, busy, scans, allowCount, onGo }: { api: Api; run: Run; busy: boolean; scans: Scan[]; allowCount: number; onGo: (t: Tab) => void }) {
  const [targets, setTargets] = useState("");
  const [profile, setProfile] = useState("standard");
  const [tcpPorts, setTcpPorts] = useState("");
  const [udpPorts, setUdpPorts] = useState("");
  const [treatAllUp, setTreatAllUp] = useState(false);
  const [cveLookup, setCveLookup] = useState(true);
  const [check, setCheck] = useState<{ allowed?: Decision[]; dropped?: Decision[]; tcp_port_count?: number; udp_ports?: number[]; error?: string } | null>(null);

  const body = () => JSON.stringify({
    targets: targets.split(/[\s,]+/).map((t) => t.trim()).filter(Boolean),
    profile, tcp_ports: tcpPorts, udp_ports: udpPorts, treat_all_up: treatAllUp, cve_lookup: cveLookup,
  });

  const dryRun = () => run("Checking scope…", async () => setCheck(await api("/scope-check", { method: "POST", body: body() })));
  const start = () => run("Starting scan…", async () => { await api("/scans", { method: "POST", body: body() }); setCheck(null); });
  const stop = (id: number) => run("Stopping…", () => api(`/scans/${id}/cancel`, { method: "POST" }));

  return (
    <div className="p-4 grid lg:grid-cols-2 gap-4">
      <section className="border border-border rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-medium">New scan</h2>
        {allowCount === 0 && <p className="text-xs" style={{ color: "var(--color-yellow)" }}>The allowlist is empty. <button type="button" className="text-accent" onClick={() => onGo("targets")}>Add targets</button> first.</p>}
        <Field label="Targets" hint="IPs, CIDRs or hostnames on the allowlist"><textarea rows={3} value={targets} onChange={(e) => setTargets(e.target.value)} placeholder="10.0.0.0/24, app.example.com" className={controlClass} /></Field>
        <Field label="Profile">
          <select value={profile} onChange={(e) => setProfile(e.target.value)} className={controlClass}>
            <option value="quick">Quick: 100 common TCP ports</option>
            <option value="standard">Standard: ports 1-1024, common high ports, DNS/NTP/SNMP</option>
            <option value="full">Full: all 65,535 TCP ports, DNS/NTP/SNMP</option>
            <option value="custom">Custom ports</option>
          </select>
        </Field>
        {profile === "custom" && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="TCP ports" hint="22,80,8000-8100"><input value={tcpPorts} onChange={(e) => setTcpPorts(e.target.value)} className={controlClass} /></Field>
            <Field label="UDP ports" hint="53, 123, 161"><input value={udpPorts} onChange={(e) => setUdpPorts(e.target.value)} className={controlClass} /></Field>
          </div>
        )}
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={treatAllUp} onChange={(e) => setTreatAllUp(e.target.checked)} />Treat all hosts as up (skip discovery)</label>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={cveLookup} onChange={(e) => setCveLookup(e.target.checked)} />Match versions to CVEs (NVD, CISA KEV, EPSS)</label>
        <div className="flex gap-2">
          <button type="button" onClick={dryRun} disabled={busy || !targets.trim()} className={buttonClass}>Check scope</button>
          <button type="button" onClick={start} disabled={busy || !targets.trim()} className={primaryClass}>Start scan</button>
        </div>
        <p className="text-xs text-text-muted">Detection only: no exploits and no password guessing. Speed is capped by the limits on the Targets tab.</p>
        {check && (
          <div className="border border-border rounded p-3 text-xs space-y-2">
            {check.error && <div style={{ color: "var(--color-red)" }}>{check.error}</div>}
            {check.allowed && <div><span className="font-medium">{check.allowed.length} address(es) will be scanned</span>{check.tcp_port_count !== undefined && <span className="text-text-muted"> · {check.tcp_port_count} TCP ports{check.udp_ports?.length ? ` · UDP ${check.udp_ports.join(", ")}` : ""}</span>}</div>}
            {check.allowed && check.allowed.length > 0 && <div className="text-text-muted">{check.allowed.slice(0, 20).map((a) => a.address).join(", ")}{check.allowed.length > 20 ? ` and ${check.allowed.length - 20} more` : ""}</div>}
            {check.dropped && check.dropped.length > 0 && (
              <div>
                <div className="font-medium">{check.dropped.length} dropped</div>
                <ul className="text-text-muted">{check.dropped.slice(0, 20).map((d, i) => <li key={i}>{d.address || d.target}: {d.reason}</li>)}</ul>
              </div>
            )}
          </div>
        )}
      </section>
      <section className="border border-border rounded-lg overflow-hidden">
        <div className="px-4 py-3 border-b border-border"><h2 className="text-sm font-medium">Scans</h2></div>
        {scans.length === 0 ? <div className="px-4 py-6 text-sm text-text-muted">No scans yet.</div> : (
          <ul className="divide-y divide-border">{scans.map((s) => <ScanRow key={s.id} scan={s} onStop={() => stop(s.id)} />)}</ul>
        )}
      </section>
    </div>
  );
}

// ---------- findings ----------

function FindingsTab({ api, projectId, setMessage }: { api: Api; projectId: string; setMessage: (m: string) => void }) {
  const [findings, setFindings] = useState<Finding[]>([]);
  const [total, setTotal] = useState(0);
  const [severity, setSeverity] = useState("");
  const [status, setStatus] = useState("open,confirmed");
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState(0);
  const [note, setNote] = useState("");

  const load = useCallback(async () => {
    const params = new URLSearchParams({ status, limit: "300" });
    if (severity) params.set("severity", severity);
    if (query.trim()) params.set("q", query.trim());
    try {
      const data = await api(`/findings?${params.toString()}`);
      setFindings(data.findings || []);
      setTotal(data.total || 0);
      setSelectedId((cur) => (data.findings || []).some((f: Finding) => f.id === cur) ? cur : data.findings?.[0]?.id || 0);
    } catch (error) {
      setMessage(String((error as Error).message || error));
    }
  }, [api, severity, status, query, setMessage]);

  useEffect(() => { load(); }, [load]);
  const selected = useMemo(() => findings.find((f) => f.id === selectedId) || null, [findings, selectedId]);
  useEffect(() => { setNote(selected?.note || ""); }, [selected?.id]);

  const triage = async (newStatus: string) => {
    if (!selected) return;
    try {
      await api(`/findings/${selected.id}`, { method: "PATCH", body: JSON.stringify({ status: newStatus, note }) });
      await load();
    } catch (error) {
      setMessage(String((error as Error).message || error));
    }
  };

  const reportURL = `${API}/report?format=md&project_id=${encodeURIComponent(projectId)}`;

  return (
    <div className="h-full flex flex-col">
      <div className="shrink-0 px-4 py-3 border-b border-border flex flex-wrap gap-2 items-center">
        <select value={severity} onChange={(e) => setSeverity(e.target.value)} className="px-2 py-1.5 text-xs bg-bg border border-border rounded">
          <option value="">All severities</option>
          {SEVERITIES.map((s) => <option key={s} value={s}>{label(s)}</option>)}
        </select>
        <select value={status} onChange={(e) => setStatus(e.target.value)} className="px-2 py-1.5 text-xs bg-bg border border-border rounded">
          <option value="open,confirmed">Open and confirmed</option>
          <option value="fixed">Fixed</option>
          <option value="false_positive">False positive</option>
          <option value="risk_accepted">Risk accepted</option>
          <option value="all">All</option>
        </select>
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search title, evidence, address" className="px-2 py-1.5 text-xs bg-bg border border-border rounded" style={{ width: 240 }} />
        <span className="text-xs text-text-muted">{total} finding(s)</span>
        <a href={reportURL} className={`${buttonClass} ml-auto`}>Download report</a>
      </div>
      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "minmax(280px, 420px) minmax(0, 1fr)" }}>
        <ul className="border-r border-border overflow-auto divide-y divide-border">
          {findings.length === 0 && <li className="px-4 py-6 text-sm text-text-muted">No findings match.</li>}
          {findings.map((f) => (
            <li key={f.id}>
              <button type="button" onClick={() => setSelectedId(f.id)} className={`w-full text-left px-4 py-3 ${f.id === selectedId ? "bg-bg-hover" : ""}`}>
                <div className="flex items-center gap-2"><SevBadge sev={f.severity} />{f.kev && <span className="text-xs font-medium" style={{ color: "var(--color-red)" }}>KEV</span>}<span className="text-xs text-text-muted ml-auto">{f.address}{f.port ? `:${f.port}` : ""}</span></div>
                <div className="mt-1 text-sm">{f.title}</div>
              </button>
            </li>
          ))}
        </ul>
        <div className="overflow-auto p-5">
          {!selected ? <div className="text-sm text-text-muted">Select a finding.</div> : (
            <div className="space-y-4 max-w-3xl">
              <div>
                <div className="flex items-center gap-2"><SevBadge sev={selected.severity} /><span className="text-xs text-text-muted">{selected.status.replace("_", " ")}</span></div>
                <h2 className="mt-2 text-base font-medium">{selected.title}</h2>
                <div className="mt-1 text-xs text-text-muted">{selected.address}{selected.port ? ` ${selected.protocol}/${selected.port}` : ""} · first seen {dateLabel(selected.first_seen)} · last seen {dateLabel(selected.last_seen)}</div>
              </div>
              {selected.cvss > 0 && (
                <div className="text-sm">CVSS {selected.cvss.toFixed(1)}{selected.epss > 0 ? ` · EPSS ${(selected.epss * 100).toFixed(1)}%` : ""}{selected.kev ? " · on the CISA Known Exploited Vulnerabilities list" : ""}</div>
              )}
              <Block title="Evidence">{selected.evidence}</Block>
              <Block title="How to fix">{selected.remediation}</Block>
              {selected.cve_ids.length > 0 && (
                <Block title={`CVEs (${selected.cve_ids.length})`}>
                  <div className="flex flex-wrap gap-2">{selected.cve_ids.map((id) => <a key={id} href={`https://nvd.nist.gov/vuln/detail/${id}`} target="_blank" rel="noreferrer" className="text-xs text-accent">{id}</a>)}</div>
                </Block>
              )}
              <Field label="Analyst note"><textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} className={controlClass} /></Field>
              <div className="flex flex-wrap gap-2">
                <button type="button" onClick={() => triage("confirmed")} className={primaryClass}>Confirm</button>
                <button type="button" onClick={() => triage("false_positive")} className={buttonClass}>False positive</button>
                <button type="button" onClick={() => triage("risk_accepted")} className={buttonClass}>Accept risk</button>
                <button type="button" onClick={() => triage("open")} className={buttonClass}>Reopen</button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ---------- hosts ----------

function HostsTab({ api }: { api: Api }) {
  const [hosts, setHosts] = useState<Host[]>([]);
  const [query, setQuery] = useState("");
  useEffect(() => {
    const params = new URLSearchParams({ limit: "300" });
    if (query.trim()) params.set("q", query.trim());
    api(`/hosts?${params.toString()}`).then((d) => setHosts(d.hosts || [])).catch(() => setHosts([]));
  }, [api, query]);
  return (
    <div className="p-4 space-y-3">
      <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter by address, service, product or port" className="px-3 py-2 text-sm bg-bg border border-border rounded" style={{ width: 360 }} />
      {hosts.length === 0 && <div className="text-sm text-text-muted">No hosts yet. Run a scan first.</div>}
      {hosts.map((h) => (
        <section key={h.id} className="border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-border flex items-center gap-3">
            <span className="text-sm font-medium">{h.address}</span>
            {h.hostname && <span className="text-xs text-text-muted">{h.hostname}</span>}
            <span className="text-xs" style={{ color: h.up ? "var(--color-green)" : "var(--color-text-dim)" }}>{h.up ? "up" : "down"}</span>
            <span className="ml-auto text-xs text-text-muted">{h.open_findings} open finding(s) · last seen {dateLabel(h.last_seen)}</span>
          </div>
          {h.services.length === 0 ? <div className="px-4 py-3 text-xs text-text-muted">No open ports found.</div> : (
            <table className="w-full text-sm">
              <thead><tr className="text-left text-xs text-text-muted"><th className="px-4 py-2 font-normal">Port</th><th className="px-4 py-2 font-normal">Service</th><th className="px-4 py-2 font-normal">Product</th><th className="px-4 py-2 font-normal">Banner</th></tr></thead>
              <tbody className="divide-y divide-border">
                {h.services.map((s) => (
                  <tr key={`${s.protocol}${s.port}`}>
                    <td className="px-4 py-2 whitespace-nowrap">{s.port}/{s.protocol}</td>
                    <td className="px-4 py-2">{s.service}{s.tls ? " (TLS)" : ""}</td>
                    <td className="px-4 py-2">{[s.product, s.version].filter(Boolean).join(" ")}</td>
                    <td className="px-4 py-2 text-xs text-text-muted truncate" style={{ maxWidth: 360 }}>{s.banner}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      ))}
    </div>
  );
}

// ---------- targets ----------

function TargetsTab({ api, run, busy }: { api: Api; run: Run; busy: boolean }) {
  const [targets, setTargets] = useState<Target[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [value, setValue] = useState("");
  const [kind, setKind] = useState<"allow" | "exclude">("allow");
  const [note, setNote] = useState("");

  const load = useCallback(async () => {
    const [t, s] = await Promise.all([api("/targets"), api("/settings")]);
    setTargets([...(t.allowlist || []), ...(t.exclusions || [])]);
    setSettings(s.settings);
  }, [api]);
  useEffect(() => { load().catch(() => undefined); }, [load]);

  const add = () => run("Adding…", async () => {
    await api("/targets", { method: "POST", body: JSON.stringify({ kind, values: value.split(/[\s,]+/).filter(Boolean), note }) });
    setValue("");
    setNote("");
    await load();
  });
  const remove = (id: number) => run("Removing…", async () => { await api(`/targets/${id}`, { method: "DELETE" }); await load(); });
  const save = () => run("Saving limits…", async () => { if (settings) await api("/settings", { method: "PUT", body: JSON.stringify(settings) }); await load(); });

  const list = (k: "allow" | "exclude") => targets.filter((t) => t.kind === k);
  return (
    <div className="p-4 grid lg:grid-cols-2 gap-4">
      <section className="border border-border rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-medium">Add targets</h2>
        <p className="text-xs text-text-muted">Only add systems you own or have written permission to test. Agents can read this list but cannot change it.</p>
        <div className="grid grid-cols-3 gap-3">
          <Field label="Type"><select value={kind} onChange={(e) => setKind(e.target.value as "allow" | "exclude")} className={controlClass}><option value="allow">Allow</option><option value="exclude">Exclude</option></select></Field>
          <div className="col-span-2"><Field label="IPs, CIDRs or hostnames"><input value={value} onChange={(e) => setValue(e.target.value)} placeholder="203.0.113.0/28, app.example.com" className={controlClass} /></Field></div>
        </div>
        <Field label="Note" hint="optional, e.g. who authorised it"><input value={note} onChange={(e) => setNote(e.target.value)} className={controlClass} /></Field>
        <button type="button" onClick={add} disabled={busy || !value.trim()} className={primaryClass}>Add</button>
        <TargetList title="Allowlist" items={list("allow")} onRemove={remove} empty="Nothing can be scanned until you add a target." />
        <TargetList title="Exclusions" items={list("exclude")} onRemove={remove} empty="No exclusions." />
      </section>
      <section className="border border-border rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-medium">Limits</h2>
        {settings && (
          <>
            <Field label="Probes per second" hint="max 1000"><input type="number" min={1} max={1000} value={settings.rate_pps} onChange={(e) => setSettings({ ...settings, rate_pps: Number(e.target.value) })} className={controlClass} /></Field>
            <Field label="Hosts scanned in parallel" hint="max 64"><input type="number" min={1} max={64} value={settings.parallel_hosts} onChange={(e) => setSettings({ ...settings, parallel_hosts: Number(e.target.value) })} className={controlClass} /></Field>
            <Field label="Max addresses per scan" hint="1024 is a /22"><input type="number" min={1} max={65536} value={settings.max_addresses} onChange={(e) => setSettings({ ...settings, max_addresses: Number(e.target.value) })} className={controlClass} /></Field>
            <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={settings.cve_lookup} onChange={(e) => setSettings({ ...settings, cve_lookup: e.target.checked })} />Allow CVE lookups (sends product names and versions to NVD and FIRST, never target addresses)</label>
            <button type="button" onClick={save} disabled={busy} className={primaryClass}>Save limits</button>
          </>
        )}
      </section>
    </div>
  );
}

function TargetList({ title, items, onRemove, empty }: { title: string; items: Target[]; onRemove: (id: number) => void; empty: string }) {
  return (
    <div>
      <div className="text-xs text-text-muted mb-2">{title} ({items.length})</div>
      {items.length === 0 ? <div className="text-xs text-text-dim">{empty}</div> : (
        <ul className="divide-y divide-border border border-border rounded">
          {items.map((t) => (
            <li key={t.id} className="px-3 py-2 flex items-center gap-3">
              <div className="min-w-0"><div className="text-sm">{t.value}</div>{t.note && <div className="text-xs text-text-muted">{t.note}</div>}</div>
              <button type="button" onClick={() => onRemove(t.id)} className="ml-auto text-xs text-text-muted">Remove</button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ---------- shared ----------

function Field({ label: text, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label className="block"><span className="block text-xs text-text-muted mb-1">{text}{hint && <span className="text-text-dim"> · {hint}</span>}</span>{children}</label>;
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return <div><div className="text-xs text-text-muted mb-1">{title}</div><div className="text-sm whitespace-pre-wrap">{children}</div></div>;
}

function SevDot({ sev }: { sev: Severity }) {
  return <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" fill={SEV_COLOR[sev]} /></svg>;
}

function SevBadge({ sev }: { sev: Severity }) {
  const solid = sev === "critical";
  return (
    <span className="px-2 py-0.5 text-xs rounded font-medium"
      style={solid ? { background: SEV_COLOR[sev], color: "var(--color-bg)" } : { color: SEV_COLOR[sev], border: `1px solid ${SEV_COLOR[sev]}` }}>
      {label(sev)}
    </span>
  );
}

function ShieldIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 3 4 6v6c0 4.5 3.4 8.3 8 9 4.6-.7 8-4.5 8-9V6l-8-3Z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

function label(sev: string) {
  return sev.charAt(0).toUpperCase() + sev.slice(1);
}

function dateLabel(iso?: string) {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
