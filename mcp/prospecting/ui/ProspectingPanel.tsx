import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Profile {
  id: number;
  name: string;
  description: string;
  industries: string[];
  locations: string[];
  employee_min?: number;
  employee_max?: number;
  target_titles: string[];
  keywords: string[];
  status: string;
  created_at: string;
  updated_at: string;
}

interface Candidate {
  id: number;
  profile_id: number;
  run_id?: number;
  company_name: string;
  company_domain: string;
  website: string;
  person_first_name: string;
  person_last_name: string;
  person_display_name: string;
  job_title: string;
  email: string;
  phone: string;
  location: string;
  employee_estimate?: number;
  location_count: number;
  summary: string;
  fit_score: number;
  confidence_score: number;
  score_reasons: string[];
  eligibility: string;
  eligibility_reasons: string[];
  automation_signals: Array<{ key: string; label: string; evidence: string; url: string; weight: number }>;
  status: string;
  source: string;
  source_url: string;
  decision_reason?: string;
  crm_contact_id?: number;
  researched_at?: string;
  enriched_at?: string;
  created_at: string;
  updated_at: string;
}

interface Evidence {
  id: number;
  candidate_id: number;
  source_kind: string;
  title: string;
  url: string;
  excerpt: string;
  artifact_id?: number;
  retrieved_at: string;
}

interface Run {
  id: number;
  profile_id: number;
  query: string;
  status: string;
  requested_limit: number;
  result_count: number;
  error?: string;
  started_at: string;
  completed_at?: string;
}

interface Exclusion {
  id: number;
  kind: string;
  value: string;
  reason: string;
  created_at: string;
}

interface Overview {
  active_profiles: number;
  search_runs: number;
  candidates: Record<string, number>;
  qualifications: Record<string, number>;
  enriched: number;
  evidence: number;
  exclusions: number;
}

interface Capabilities {
  web: boolean;
  crm: boolean;
  google_places?: boolean;
}

interface CRMSender {
  channel: string;
  address: string;
  verified: boolean;
  sending_enabled: boolean;
}

interface CRMConversation {
  id: number;
  channel: string;
  subject?: string;
  status: string;
  priority: string;
  last_activity_at: string;
}

interface CRMActivity {
  id: number;
  kind: string;
  body: string;
  occurred_at: string;
  conversation_id?: number;
  message_status?: { status?: string; status_reason?: string };
}

interface WhatsAppTemplate {
  id: number;
  name: string;
  body_text?: string;
  provider_status?: string;
  vars_schema?: Record<string, unknown>;
}

interface OutreachData {
  linked: boolean;
  error?: string;
  crm_contact_id?: number;
  context?: {
    activities?: CRMActivity[];
    conversations?: CRMConversation[];
    opportunities?: Array<Record<string, unknown>>;
  };
  messaging?: {
    available: boolean;
    error?: string;
    whatsapp_error?: string;
    senders?: { senders?: CRMSender[]; count?: number };
    whatsapp_session?: { active?: boolean; since?: string; last_inbound?: string };
    whatsapp_templates?: { templates?: WhatsAppTemplate[]; count?: number };
  };
}

type Tab = "overview" | "discover" | "candidates" | "settings";

const API = "/api/apps/prospecting";
const emptyProfile = {
  name: "",
  description: "",
  industries: "",
  locations: "",
  employee_min: "",
  employee_max: "",
  target_titles: "",
  keywords: "",
};

const emptyCandidate = {
  company_name: "",
  company_domain: "",
  website: "",
  person_first_name: "",
  person_last_name: "",
  person_display_name: "",
  job_title: "",
  email: "",
  phone: "",
  summary: "",
  source_url: "",
};

export default function ProspectingPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [exclusions, setExclusions] = useState<Exclusion[]>([]);
  const [overview, setOverview] = useState<Overview | null>(null);
  const [capabilities, setCapabilities] = useState<Capabilities>({ web: false, crm: false });
  const [selectedCandidateId, setSelectedCandidateId] = useState(0);
  const [selectedEvidence, setSelectedEvidence] = useState<Evidence[]>([]);
  const [handoff, setHandoff] = useState<Record<string, unknown> | null>(null);
  const [outreach, setOutreach] = useState<OutreachData | null>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("active");
  const [profileFilter, setProfileFilter] = useState(0);
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
      const params = new URLSearchParams({ status: statusFilter, limit: "200" });
      if (profileFilter) params.set("profile_id", String(profileFilter));
      if (query.trim()) params.set("q", query.trim());
      const [capabilityData, overviewData, profileData, candidateData, runData, exclusionData] = await Promise.all([
        api("/capabilities"),
        api("/overview"),
        api("/profiles?status=all"),
        api(`/candidates?${params.toString()}`),
        api("/runs?limit=30"),
        api("/exclusions?limit=200"),
      ]);
      setCapabilities(capabilityData);
      setOverview(overviewData);
      setProfiles(profileData.profiles || []);
      const loadedCandidates = candidateData.candidates || [];
      setCandidates(loadedCandidates);
      setRuns(runData.runs || []);
      setExclusions(exclusionData.exclusions || []);
      setSelectedCandidateId((current) => current && loadedCandidates.some((c: Candidate) => c.id === current)
        ? current
        : loadedCandidates[0]?.id || 0);
    } catch (error) {
      setMessage((error as Error).message);
    }
  }, [api, profileFilter, query, statusFilter]);

  useEffect(() => { load(); }, [load]);

  const selectedCandidate = useMemo(
    () => candidates.find((candidate) => candidate.id === selectedCandidateId) || null,
    [candidates, selectedCandidateId],
  );

  const loadCandidateDetail = useCallback(async (id: number) => {
    setOutreach(null);
    if (!id) {
      setSelectedEvidence([]);
      setHandoff(null);
      setOutreach(null);
      return;
    }
    try {
      const data = await api(`/candidates/${id}`);
      setSelectedEvidence(data.evidence || []);
      setHandoff(data.handoff || null);
      if (data.handoff) {
        const outreachData = await api(`/candidates/${id}/outreach`).catch((error) => ({ linked: true, error: (error as Error).message }));
        setOutreach(outreachData);
      } else {
        setOutreach({ linked: false });
      }
    } catch (error) {
      setMessage((error as Error).message);
    }
  }, [api]);

  useEffect(() => { loadCandidateDetail(selectedCandidateId); }, [loadCandidateDetail, selectedCandidateId]);

  const runAction = useCallback(async (action: () => Promise<unknown>, success: string) => {
    setBusy(true);
    setMessage("");
    try {
      await action();
      setMessage(success);
      await load();
      if (selectedCandidateId) await loadCandidateDetail(selectedCandidateId);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }, [load, loadCandidateDetail, selectedCandidateId]);

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <style>{panelLayoutCSS}</style>
      <header className="shrink-0 border-b border-border">
        <div className="px-5 lg:px-6 pt-4 pb-3 flex items-start gap-4">
          <div>
            <h1 className="text-lg font-semibold">Prospecting</h1>
            <p className="text-xs text-text-muted">Find, qualify, and contact leads. CRM handles email, SMS, and WhatsApp; calling comes later.</p>
          </div>
          <div className="ml-auto text-xs text-text-muted min-h-5 max-w-xl text-right">{busy ? "Working…" : message}</div>
          <button type="button" onClick={load} disabled={busy} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50">
            Refresh
          </button>
        </div>
        <nav className="px-3 lg:px-4 flex gap-1 text-sm">
          {(["overview", "discover", "candidates", "settings"] as Tab[]).map((item) => (
            <button
              key={item}
              type="button"
              onClick={() => setTab(item)}
              className={`px-3 py-3 border-b-2 ${tab === item ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"}`}
            >
              {item === "candidates" ? "Leads" : item[0].toUpperCase() + item.slice(1)}
            </button>
          ))}
        </nav>
      </header>

      <main className={`flex-1 min-h-0 ${tab === "candidates" ? "overflow-hidden" : "overflow-auto"}`}>
        {tab === "overview" && (
          <OverviewView overview={overview} profiles={profiles} candidates={candidates} runs={runs} capabilities={capabilities} onDiscover={() => setTab("discover")} onCandidates={() => setTab("candidates")} />
        )}
        {tab === "discover" && (
          <DiscoverView profiles={profiles.filter((profile) => profile.status === "active")} capabilities={capabilities} busy={busy} api={api} onDone={async (text) => { setMessage(text); await load(); setTab("candidates"); }} onError={setMessage} setBusy={setBusy} />
        )}
        {tab === "candidates" && (
          <CandidatesView
            profiles={profiles}
            candidates={candidates}
            selected={selectedCandidate}
            evidence={selectedEvidence}
            handoff={handoff}
            outreach={outreach}
            selectedId={selectedCandidateId}
            setSelectedId={setSelectedCandidateId}
            query={query}
            setQuery={setQuery}
            statusFilter={statusFilter}
            setStatusFilter={setStatusFilter}
            profileFilter={profileFilter}
            setProfileFilter={setProfileFilter}
            capabilities={capabilities}
            projectId={projectId}
            busy={busy}
            api={api}
            runAction={runAction}
          />
        )}
        {tab === "settings" && (
          <SettingsView profiles={profiles} exclusions={exclusions} busy={busy} api={api} runAction={runAction} />
        )}
      </main>
    </div>
  );
}

function OverviewView({ overview, profiles, candidates, runs, capabilities, onDiscover, onCandidates }: {
  overview: Overview | null;
  profiles: Profile[];
  candidates: Candidate[];
  runs: Run[];
  capabilities: Capabilities;
  onDiscover: () => void;
  onCandidates: () => void;
}) {
  const ready = overview?.candidates?.ready || 0;
  const accepted = overview?.candidates?.accepted || 0;
  const deferred = overview?.candidates?.deferred || 0;
  const recent = candidates.slice(0, 6);
  return (
    <div className="prospecting-full-width w-full p-5 lg:p-6 space-y-5">
      <section className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        <Metric label="Active profiles" value={overview?.active_profiles || 0} />
        <Metric label="Ready to review" value={ready} accent />
        <Metric label="Deferred" value={deferred} />
        <Metric label="Accepted" value={accepted} />
        <Metric label="Evidence sources" value={overview?.evidence || 0} />
      </section>
      <section className="border border-border rounded-lg p-5 flex items-center gap-5">
        <div>
          <h2 className="font-medium">Your standalone lead workspace</h2>
          <p className="mt-1 text-sm text-text-muted">Add or discover leads, qualify them, and start one-to-one outreach without leaving the workspace.</p>
          <div className="mt-2 flex gap-2 text-[11px]"><CapabilityBadge label="Google Places" available={!!capabilities.google_places} /><CapabilityBadge label="Web discovery" available={capabilities.web} /><CapabilityBadge label="CRM outreach" available={capabilities.crm} /></div>
        </div>
        <button type="button" onClick={capabilities.web || capabilities.google_places ? onDiscover : onCandidates} className="ml-auto px-4 py-2 text-sm bg-accent text-bg rounded font-medium">{capabilities.web || capabilities.google_places ? "Discover companies" : "Seed leads"}</button>
      </section>
      <div className="grid lg:grid-cols-2 gap-5">
        <section className="border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-border flex items-center">
            <h2 className="text-sm font-medium">Recent leads</h2>
            <button type="button" onClick={onCandidates} className="ml-auto text-xs text-accent">Review all</button>
          </div>
          {recent.length === 0 ? <Empty text="No active leads yet." /> : (
            <ul className="divide-y divide-border">
              {recent.map((candidate) => (
                <li key={candidate.id} className="px-4 py-3 flex items-center gap-3">
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate">{candidate.company_name}</div>
                    <div className="text-xs text-text-muted truncate">{candidate.person_display_name || candidate.company_domain || "Company candidate"}</div>
                  </div>
                  <Score value={candidate.fit_score} label="fit" />
                  <Status value={candidate.status} />
                </li>
              ))}
            </ul>
          )}
        </section>
        <section className="border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-border"><h2 className="text-sm font-medium">Recent discovery runs</h2></div>
          {runs.length === 0 ? <Empty text="No searches have run yet." /> : (
            <ul className="divide-y divide-border">
              {runs.slice(0, 6).map((run) => (
                <li key={run.id} className="px-4 py-3">
                  <div className="flex items-center gap-2">
                    <span className="text-sm truncate">{run.query}</span>
                    <Status value={run.status} />
                  </div>
                  <div className="mt-1 text-xs text-text-muted">{run.result_count} new candidate{run.result_count === 1 ? "" : "s"} · {dateLabel(run.started_at)}</div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {profiles.length === 0 && <div className="text-sm text-text-muted">Create a target profile in Settings before running discovery.</div>}
    </div>
  );
}

interface PipelineRun {
  id: number;
  status: string;
  error?: string;
  options: { query: string; source: string; crm_mode: string };
  progress: { phase: string; places_requests: number };
  counts: Record<string, number>;
  items: Array<{ source_key: string; candidate_id?: number; crm_contact_id?: number; status: string; reason?: string }>;
}

function DiscoverView({ profiles, capabilities, busy, api, onDone, onError, setBusy }: {
  profiles: Profile[]; capabilities: Capabilities; busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  onDone: (message: string) => Promise<void>; onError: (message: string) => void; setBusy: (busy: boolean) => void;
}) {
  const [profileId, setProfileId] = useState(profiles[0]?.id || 0);
  const [source, setSource] = useState(capabilities.google_places ? "google_places" : "web");
  const [customQuery, setCustomQuery] = useState("");
  const [area, setArea] = useState("");
  const [limit, setLimit] = useState(20);
  const [qualify, setQualify] = useState(capabilities.web);
  const [crmMode, setCRMMode] = useState("review");
  const [minFit, setMinFit] = useState(70);
  const [minConfidence, setMinConfidence] = useState(60);
  const [lists, setLists] = useState("");
  const [jobs, setJobs] = useState<PipelineRun[]>([]);
  const [selectedJob, setSelectedJob] = useState(0);
  const [bounds, setBounds] = useState({ south: "", west: "", north: "", east: "" });
  const [requestKey, setRequestKey] = useState("");
  const profile = profiles.find(p => p.id === profileId);
  const generated = source === "google_places"
    ? `${profile?.industries.join(", ") || "businesses"} in ${area.trim() || profile?.locations.join(", ") || "United States"}`
    : profile ? queryPreview(profile) : "";
  const available = source === "google_places" ? capabilities.google_places : capabilities.web;
  const current = jobs.find(j => j.id === selectedJob);
  const refreshJobs = useCallback(async () => {
    const data = await api("/pipeline/runs?limit=20");
    setJobs(data.runs || []);
    setSelectedJob(id => id || data.runs?.[0]?.id || 0);
  }, [api]);
  useEffect(() => {
    let active = true;
    const refresh = () => refreshJobs().catch(e => { if (active) onError(e.message); });
    refresh(); const timer = setInterval(refresh, 4000);
    return () => { active = false; clearInterval(timer); };
  }, [refreshJobs, onError]);
  useEffect(() => { if (!profiles.some(p => p.id === profileId)) setProfileId(profiles[0]?.id || 0); }, [profiles, profileId]);
  useEffect(() => { setRequestKey(""); }, [profileId, source, customQuery, area, limit, qualify, crmMode, minFit, minConfidence, lists, bounds]);
  const run = async () => {
    if (!profileId) return;
    setBusy(true); onError("");
    const key = requestKey || crypto.randomUUID(); setRequestKey(key);
    try {
      let locationRestriction;
      if (source === "google_places" && Object.values(bounds).some(Boolean)) {
        if (!Object.values(bounds).every(Boolean)) throw new Error("Fill all four geographic boundary coordinates.");
        locationRestriction = { rectangle: { low: { latitude: Number(bounds.south), longitude: Number(bounds.west) }, high: { latitude: Number(bounds.north), longitude: Number(bounds.east) } } };
      }
      const data = await api("/pipeline/runs", { method: "POST", body: JSON.stringify({
        profile_id: profileId, source, query: customQuery.trim() || generated, limit,
        qualify: qualify && capabilities.web, crm_mode: crmMode,
        min_fit_score: minFit, min_confidence_score: minConfidence, list_ids: split(lists),
        location_restriction: locationRestriction, idempotency_key: key,
      }) });
      setSelectedJob(data.run.id); setRequestKey(""); await refreshJobs();
    } catch (e) { onError((e as Error).message); } finally { setBusy(false); }
  };
  const resume = async (id: number) => {
    setBusy(true); onError("");
    try { await api(`/pipeline/runs/${id}/resume`, { method: "POST", body: "{}" }); await refreshJobs(); }
    catch (e) { onError((e as Error).message); } finally { setBusy(false); }
  };
  return <div className="prospecting-full-width prospecting-discover-grid w-full p-5 lg:p-6 gap-5">
    <section className="border border-border rounded-lg p-5 space-y-4 self-start">
      <div><h2 className="font-medium">Discover and qualify prospects</h2><p className="mt-1 text-sm text-text-muted">Add businesses to your lead workspace, check their websites, and optionally add matching prospects to CRM.</p></div>
      <Field label="Discovery source"><select value={source} onChange={e => setSource(e.target.value)} className={controlClass}><option value="google_places">Google Places</option><option value="web">Web search</option></select></Field>
      {!available && <p className="text-sm text-text-muted">{source === "google_places" ? "Connect Google Places in Settings to use this source." : "Connect the optional Web app to use Web search."}</p>}
      {profiles.length === 0 ? <Empty text="Create an active target profile in Settings first." /> : <>
        <Field label="Target profile"><select value={profileId} onChange={e => setProfileId(Number(e.target.value))} className={controlClass}>{profiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
        {source === "google_places" && <Field label="City or area" hint="For example Dallas, Texas, United States"><input value={area} onChange={e => setArea(e.target.value)} placeholder={profile?.locations.join(", ")} className={controlClass} /></Field>}
        <Field label="Search query" hint="Leave blank to use the planned query."><textarea value={customQuery} onChange={e => setCustomQuery(e.target.value)} placeholder={generated} rows={2} className={controlClass} /></Field>
        <p className="text-xs text-text-muted">Planned query: {customQuery.trim() || generated}</p>
        {source === "google_places" && <details><summary className="text-xs cursor-pointer">Restrict to geographic boundaries (optional)</summary><div className="grid grid-cols-2 gap-2 mt-3">{(["south", "west", "north", "east"] as const).map(k => <Field key={k} label={`${k[0].toUpperCase() + k.slice(1)} ${k === "south" || k === "north" ? "latitude" : "longitude"}`}><input type="number" step="any" value={bounds[k]} onChange={e => setBounds({ ...bounds, [k]: e.target.value })} className={controlClass} /></Field>)}</div></details>}
        <Field label="Maximum prospects"><input type="number" min={1} max={20} value={limit} onChange={e => setLimit(Number(e.target.value))} className={controlClass} /></Field>
        <label className="text-sm flex items-center gap-2"><input type="checkbox" checked={qualify && capabilities.web} disabled={!capabilities.web || crmMode === "auto"} onChange={e => setQualify(e.target.checked)} />Qualify using first-party websites</label>
        {!capabilities.web && <p className="text-xs text-text-muted">Connect Web to qualify prospects. Places discovery can still add businesses for review.</p>}
        <Field label="CRM handoff"><select value={crmMode} onChange={e => { setCRMMode(e.target.value); if (e.target.value === "auto") setQualify(true); }} className={controlClass}><option value="review">Save for review</option><option value="auto" disabled={!capabilities.crm || !capabilities.web}>Automatically add matching prospects to CRM</option></select></Field>
        {crmMode === "auto" && <div className="rounded border border-border p-3 space-y-3"><p className="text-xs text-text-muted">Only eligible prospects meeting both thresholds with an email or business phone will be added. Others stay in Prospecting.</p><div className="grid grid-cols-2 gap-3"><Field label="Minimum fit"><input type="number" min={0} max={100} value={minFit} onChange={e => setMinFit(Number(e.target.value))} className={controlClass} /></Field><Field label="Minimum confidence"><input type="number" min={0} max={100} value={minConfidence} onChange={e => setMinConfidence(Number(e.target.value))} className={controlClass} /></Field></div><Field label="CRM lists" hint="Optional comma-separated list IDs or slugs"><input value={lists} onChange={e => setLists(e.target.value)} className={controlClass} /></Field></div>}
        <button type="button" disabled={busy || !available || !profileId} onClick={run} className="px-4 py-2 bg-accent text-bg rounded text-sm font-medium disabled:opacity-50">{busy ? "Starting…" : "Start prospecting run"}</button>
      </>}
    </section>
    <section className="border border-border rounded-lg p-4 space-y-4 self-start min-w-0">
      <h2 className="text-sm font-medium">Saved runs</h2>
      {jobs.length === 0 ? <Empty text="No pipeline runs yet." /> : <>
        <select aria-label="Saved run" value={selectedJob} onChange={e => setSelectedJob(Number(e.target.value))} className={controlClass}>{jobs.map(j => <option key={j.id} value={j.id}>#{j.id} · {j.options.query} · {j.status}</option>)}</select>
        {current && <>
          <div className="flex flex-wrap items-center gap-2"><Status value={current.status} /><span className="text-xs text-text-muted">{current.options.source} · {current.progress.phase}</span></div>
          <div className="grid grid-cols-2 gap-2">{["created", "existing", "excluded", "qualified", "transferred", "retained", "failed", "pending"].map(k => <div key={k} className="border border-border rounded px-3 py-2 text-xs"><span className="capitalize">{k}</span><strong className="float-right">{current.counts[k] || 0}</strong></div>)}</div>
          {current.error && <p className="text-xs text-red">{current.error}</p>}
          {current.items.length > 0 && <ul className="divide-y divide-border max-h-[480px] overflow-auto">{current.items.map(i => <li key={i.source_key} className="py-3 text-xs"><div className="flex gap-2 flex-wrap"><strong>{i.candidate_id ? `Prospect #${i.candidate_id}` : "Excluded result"}</strong><Status value={i.status} />{i.crm_contact_id && <span className="text-accent">CRM #{i.crm_contact_id}</span>}</div>{i.reason && <p className="mt-1 text-text-muted">{i.reason}</p>}</li>)}</ul>}
          <div className="flex gap-2"><button className={secondaryButton} onClick={() => onDone(`Run #${current.id}: ${current.counts.created || 0} new prospects; ${current.counts.transferred || 0} in CRM.`)}>Open leads</button>{["failed", "completed_with_errors"].includes(current.status) && <button className={secondaryButton} disabled={busy} onClick={() => resume(current.id)}>Retry failed steps</button>}</div>
        </>}
      </>}
    </section>
  </div>;
}

function PlacesSettings({ api, runAction, busy }: { api: (path: string, init?: RequestInit) => Promise<any>; runAction: (action: () => Promise<unknown>, success: string) => Promise<void>; busy: boolean }) {
  const [connection, setConnection] = useState(0);
  const [dailyLimit, setDailyLimit] = useState(100);
  const [connections, setConnections] = useState<Array<{ id: number; name: string; status: string }>>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    Promise.all([api("/settings"), api("/connections")]).then(([s, cs]) => { if (active) { setConnection(s.places_connection_id || 0); setDailyLimit(s.daily_places_request_limit || 100); setConnections(cs.connections || []); } }).catch(e => { if (active) setError(e.message); });
    return () => { active = false; };
  }, [api]);
  const save = () => runAction(() => api("/settings", { method: "PATCH", body: JSON.stringify({ places_connection_id: connection, daily_places_request_limit: dailyLimit }) }), "Google Places settings saved.");
  return <section className="border border-border rounded-lg p-4 space-y-3"><h2 className="text-sm font-medium">Google Places discovery</h2><p className="text-xs text-text-muted">Connect a Google Cloud API key with Places API (New) enabled in Integrations, then select that connection here. Searches use paid API requests.</p>{error && <p className="text-xs text-red">{error}</p>}<Field label="Google Places connection"><select value={connection} onChange={e => setConnection(Number(e.target.value))} className={controlClass}><option value={0}>Disconnected</option>{connections.map(c => <option key={c.id} value={c.id}>{c.name} · {c.status}</option>)}</select></Field><Field label="Daily Places request limit"><input type="number" min={1} max={1000} value={dailyLimit} onChange={e => setDailyLimit(Number(e.target.value))} className={controlClass} /></Field><button type="button" disabled={busy || !!error} onClick={save} className={secondaryButton}>Save connection</button></section>;
}

function PlacesInfo({ id, api }: { id: number; api: (path: string, init?: RequestInit) => Promise<any> }) {
  const [place, setPlace] = useState<any>(null);
  const [error, setError] = useState("");
  useEffect(() => { let active = true; setPlace(null); setError(""); api(`/candidates/${id}`).then(d => { if (active) setPlace(d.place); }).catch(e => { if (active) setError(e.message); }); return () => { active = false; }; }, [api, id]);
  if (error) return <p className="text-xs text-red">{error}</p>;
  if (!place) return null;
  const p = place.details;
  return <section className="border border-border rounded-lg p-4 space-y-2"><div className="flex gap-2 items-center"><h3 className="text-sm font-medium">Google Maps</h3><a href={p.googleMapsUri || `https://www.google.com/maps/search/?api=1&query=business&query_place_id=${encodeURIComponent(p.id)}`} target="_blank" rel="noreferrer" className="ml-auto text-xs text-accent">View business listing</a></div><p className="text-sm">{p.displayName?.text}</p><p className="text-xs text-text-muted">{p.formattedAddress}</p><p className="text-xs text-text-muted">Business phone: {p.internationalPhoneNumber || p.nationalPhoneNumber || "Unknown"} · {p.businessStatus || "Status unknown"}</p><p className="text-xs text-text-muted">{p.primaryType?.replaceAll("_", " ")} · Retrieved {dateLabel(place.fetched_at)}</p>{p.attributions?.map((a: any, i: number) => <a key={i} href={a.providerUri} target="_blank" rel="noreferrer" className="block text-xs text-accent">{a.provider}</a>)}</section>;
}

function CandidatesView({ profiles, candidates, selected, evidence, handoff, outreach, selectedId, setSelectedId, query, setQuery, statusFilter, setStatusFilter, profileFilter, setProfileFilter, capabilities, projectId, busy, api, runAction }: {
  profiles: Profile[];
  candidates: Candidate[];
  selected: Candidate | null;
  evidence: Evidence[];
  handoff: Record<string, unknown> | null;
  outreach: OutreachData | null;
  selectedId: number;
  setSelectedId: (id: number) => void;
  query: string;
  setQuery: (query: string) => void;
  statusFilter: string;
  setStatusFilter: (status: string) => void;
  profileFilter: number;
  setProfileFilter: (id: number) => void;
  capabilities: Capabilities;
  projectId: string;
  busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  runAction: (action: () => Promise<unknown>, success: string) => Promise<void>;
}) {
  const [adding, setAdding] = useState(false);
  const qualifyReady = () => runAction(() => api("/qualify", {
    method: "POST",
    body: JSON.stringify({ profile_id: profileFilter || undefined, status: statusFilter === "all" || statusFilter === "active" ? "ready" : statusFilter, limit: 10, max_pages: 5 }),
  }), "Lead batch qualified from first-party websites without AI.");
  const exportLeads = (format: "csv" | "json") => {
    const params = new URLSearchParams({ project_id: projectId, format, status: statusFilter });
    if (profileFilter) params.set("profile_id", String(profileFilter));
    if (query.trim()) params.set("q", query.trim());
    const link = document.createElement("a");
    link.href = `${API}/candidates/export?${params.toString()}`;
    link.download = `prospecting-leads.${format}`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };
  return (
    <div className="h-full min-h-0 flex flex-col">
      <div className="shrink-0 px-5 lg:px-6 py-4 border-b border-border space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <div>
            <h2 className="text-base font-semibold">Lead workspace</h2>
            <p className="mt-0.5 text-xs text-text-muted">Select a lead from the working queue and qualify it in place.</p>
          </div>
          <div className="ml-auto flex items-center gap-2">
            <button type="button" onClick={() => exportLeads("csv")} disabled={candidates.length === 0} className={secondaryButton}>Export CSV</button>
            <button type="button" onClick={() => exportLeads("json")} disabled={candidates.length === 0} className={secondaryButton}>Export JSON</button>
            <button type="button" onClick={() => setAdding(true)} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">Add leads</button>
          </div>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-2">
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search leads" className={controlClass} />
          <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)} className={controlClass}>
            <option value="active">Active leads</option>
            <option value="ready">Ready to review</option>
            <option value="researching">Researching</option>
            <option value="deferred">Deferred</option>
            <option value="accepted">Accepted</option>
            <option value="rejected">Rejected</option>
            <option value="all">All statuses</option>
          </select>
          <select value={profileFilter} onChange={(event) => setProfileFilter(Number(event.target.value))} className={controlClass}>
            <option value={0}>All profiles</option>
            {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
          </select>
          <button type="button" onClick={qualifyReady} disabled={busy || candidates.length === 0 || !capabilities.web} title={capabilities.web ? "Qualify the next ten leads from first-party pages" : "Connect the optional Web app to qualify leads"} className={secondaryButton}>Qualify next 10</button>
        </div>
      </div>
      <div className="flex-1 min-h-0 p-4 lg:p-5 bg-bg-input/20">
        <div className="prospecting-workspace-grid h-full min-h-0 border border-border rounded-lg overflow-hidden bg-bg">
          <aside className="min-h-0 flex flex-col border-b lg:border-b-0 lg:border-r border-border">
            <div className="shrink-0 px-4 py-3 border-b border-border flex items-center">
              <div>
                <h3 className="text-sm font-medium">Working queue</h3>
                <p className="text-[11px] text-text-muted">{candidates.length} lead{candidates.length === 1 ? "" : "s"} in this view</p>
              </div>
              <span className="ml-auto text-[10px] text-text-dim">Rejected hidden by default</span>
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              {candidates.length === 0 ? <Empty text="No leads match these filters." /> : (
                <ul className="divide-y divide-border">
                  {candidates.map((candidate) => (
                    <li key={candidate.id}>
                      <button type="button" onClick={() => setSelectedId(candidate.id)} className={`w-full text-left px-4 py-3 border-l-2 hover:bg-bg-input ${candidate.id === selectedId ? "border-accent bg-bg-input" : "border-transparent"}`}>
                        <div className="flex gap-2 items-center">
                          <span className="text-sm font-medium truncate">{candidate.company_name}</span>
                          <Status value={candidate.status} />
                        </div>
                        <div className="mt-1 text-xs text-text-muted truncate">{candidate.person_display_name ? `${candidate.person_display_name}${candidate.job_title ? ` · ${candidate.job_title}` : ""}` : candidate.company_domain || "Contact not identified"}</div>
                        <div className="mt-2 flex items-center gap-2"><Score value={candidate.fit_score} label="fit" /><Score value={candidate.confidence_score} label="confidence" />{candidate.email && <span className="text-[10px] text-green">email</span>}{candidate.phone && <span className="text-[10px] text-green">phone</span>}</div>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </aside>
          <section className="prospecting-workspace-detail min-h-0 overflow-auto">
            {selected ? (
              <CandidateDetail candidate={selected} profile={profiles.find((profile) => profile.id === selected.profile_id)} evidence={evidence} handoff={handoff} outreach={outreach} capabilities={capabilities} busy={busy} api={api} runAction={runAction} />
            ) : <div className="h-full flex items-center justify-center"><Empty text="Select a lead from the working queue." /></div>}
          </section>
        </div>
      </div>
      {adding && <AddLeadsPanel
        profiles={profiles.filter((profile) => profile.status === "active")}
        onClose={() => setAdding(false)}
        onCreate={(profileId, body) => runAction(async () => {
          await api("/candidates", { method: "POST", body: JSON.stringify({ profile_id: profileId || undefined, ...body }) });
          setAdding(false);
        }, "Lead created.")}
        onImport={(profileId, format, data) => runAction(async () => {
          await api("/candidates/import", { method: "POST", body: JSON.stringify({ profile_id: profileId || undefined, format, data }) });
          setAdding(false);
        }, "Lead import completed. Duplicates were skipped.")}
      />}
    </div>
  );
}

function CandidateDetail({ candidate, profile, evidence, handoff, outreach, capabilities, busy, api, runAction }: {
  candidate: Candidate;
  profile?: Profile;
  evidence: Evidence[];
  handoff: Record<string, unknown> | null;
  outreach: OutreachData | null;
  capabilities: Capabilities;
  busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  runAction: (action: () => Promise<unknown>, success: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState({ ...emptyCandidate });
  useEffect(() => {
    setDraft(Object.fromEntries(Object.keys(emptyCandidate).map((key) => [key, String((candidate as any)[key] || "")])) as typeof emptyCandidate);
  }, [candidate]);
  const save = () => runAction(() => api(`/candidates/${candidate.id}`, { method: "PATCH", body: JSON.stringify(draft) }), "Candidate saved and rescored.");
	const research = () => runAction(() => api(`/candidates/${candidate.id}/research`, { method: "POST", body: "{}" }), "Web research completed.");
	const qualify = () => runAction(() => api(`/candidates/${candidate.id}/qualify`, { method: "POST", body: JSON.stringify({ max_pages: 5 }) }), "Candidate deterministically qualified from first-party pages.");
  const defer = () => runAction(() => api(`/candidates/${candidate.id}/defer`, { method: "POST", body: JSON.stringify({ reason: "Review later" }) }), "Candidate deferred.");
  const reject = () => {
    const reason = window.prompt("Reason for rejection", candidate.decision_reason || "Not a fit");
    if (reason === null) return;
    const exclude = window.confirm("Also exclude this company from future discovery runs?");
    return runAction(() => api(`/candidates/${candidate.id}/reject`, { method: "POST", body: JSON.stringify({ reason, exclude_company: exclude }) }), "Candidate rejected.");
  };
  return (
    <div className="prospecting-full-width w-full p-5 space-y-5">
      <div className="flex flex-wrap items-start gap-3">
        <div>
          <div className="text-[10px] uppercase tracking-wide text-text-dim">Qualification workspace</div>
          <div className="flex gap-2 items-center"><h2 className="text-lg font-semibold">{candidate.company_name}</h2><Status value={candidate.status} /></div>
          <div className="mt-1 text-xs text-text-muted">{profile?.name || `Profile ${candidate.profile_id}`} · {candidate.source} · updated {dateLabel(candidate.updated_at)}</div>
        </div>
		<div className="ml-auto flex flex-wrap gap-2">
		  <button type="button" onClick={qualify} disabled={busy || !capabilities.web || candidate.status === "accepted" || candidate.status === "rejected"} title={capabilities.web ? "Qualify from first-party pages" : "Connect the optional Web app"} className={secondaryButton}>Qualify</button>
		  <button type="button" onClick={research} disabled={busy || !capabilities.web || candidate.status === "accepted" || candidate.status === "rejected"} title={capabilities.web ? "Research with Web" : "Connect the optional Web app"} className={secondaryButton}>Research</button>
          <button type="button" onClick={defer} disabled={busy || candidate.status === "accepted"} className={secondaryButton}>Defer</button>
          <button type="button" onClick={reject} disabled={busy || candidate.status === "accepted"} className={secondaryButton}>Reject</button>
        </div>
      </div>
      <OutreachWorkspace candidate={candidate} handoff={handoff} outreach={outreach} capabilities={capabilities} busy={busy} api={api} runAction={runAction} />
      <section className="border border-border rounded-lg p-4 bg-bg-input/20">
        <div className="flex flex-wrap items-center gap-3"><h3 className="text-sm font-medium">Qualification</h3><Score value={candidate.fit_score} label="fit" /><Score value={candidate.confidence_score} label="confidence" />{candidate.eligibility && <Status value={candidate.eligibility} />}</div>
        {(candidate.location || candidate.employee_estimate || candidate.location_count > 0) && <div className="mt-3 text-xs text-text-muted">{candidate.location || "Location not found"}{candidate.employee_estimate ? ` · about ${candidate.employee_estimate} employees` : ""}{candidate.location_count > 0 ? ` · ${candidate.location_count} location${candidate.location_count === 1 ? "" : "s"}` : ""}</div>}
        <ul className="mt-3 grid md:grid-cols-2 gap-2 text-xs text-text-muted">
          {(candidate.score_reasons || []).map((reason, index) => <li key={`${reason}-${index}`} className="rounded bg-bg px-3 py-2 border border-border">{reason}</li>)}
        </ul>
        {(candidate.eligibility_reasons || []).length > 0 && <div className="mt-3 text-xs text-text-muted">Eligibility: {candidate.eligibility_reasons.join(" · ")}</div>}
        {(candidate.automation_signals || []).length > 0 && <div className="mt-4 grid md:grid-cols-2 gap-2">
          {candidate.automation_signals.map((signal) => <div key={signal.key} className="rounded border border-border bg-bg px-3 py-2"><div className="text-xs font-medium">{signal.label} <span className="text-text-dim">+{signal.weight}</span></div><div className="mt-1 text-[11px] text-text-muted">{signal.evidence}</div></div>)}
        </div>}
        {(candidate.score_reasons || []).length === 0 && (candidate.automation_signals || []).length === 0 && <p className="mt-3 text-xs text-text-muted">No qualification evidence yet. Connect Web and run Qualify to populate this workspace.</p>}
      </section>
      <div className="grid md:grid-cols-2 gap-5">
        <section className="border border-border rounded-lg p-4 space-y-3">
          <h3 className="text-sm font-medium">Company</h3>
          <PlacesInfo id={candidate.id} api={api} />
          <Field label="Company name"><input value={draft.company_name} onChange={(event) => setDraft({ ...draft, company_name: event.target.value })} className={controlClass} /></Field>
          <Field label="Website"><input value={draft.website} onChange={(event) => setDraft({ ...draft, website: event.target.value })} className={controlClass} /></Field>
          <Field label="Domain"><input value={draft.company_domain} onChange={(event) => setDraft({ ...draft, company_domain: event.target.value })} className={controlClass} /></Field>
          <Field label="Summary"><textarea value={draft.summary} onChange={(event) => setDraft({ ...draft, summary: event.target.value })} rows={7} className={controlClass} /></Field>
        </section>
        <section className="border border-border rounded-lg p-4 space-y-3">
          <h3 className="text-sm font-medium">Decision-maker</h3>
          <div className="grid grid-cols-2 gap-2">
            <Field label="First name"><input value={draft.person_first_name} onChange={(event) => setDraft({ ...draft, person_first_name: event.target.value })} className={controlClass} /></Field>
            <Field label="Last name"><input value={draft.person_last_name} onChange={(event) => setDraft({ ...draft, person_last_name: event.target.value })} className={controlClass} /></Field>
          </div>
          <Field label="Display name"><input value={draft.person_display_name} onChange={(event) => setDraft({ ...draft, person_display_name: event.target.value })} className={controlClass} /></Field>
          <Field label="Job title"><input value={draft.job_title} onChange={(event) => setDraft({ ...draft, job_title: event.target.value })} className={controlClass} /></Field>
          <Field label="Work email"><input type="email" value={draft.email} onChange={(event) => setDraft({ ...draft, email: event.target.value })} className={controlClass} /></Field>
          <Field label="Phone"><input value={draft.phone} onChange={(event) => setDraft({ ...draft, phone: event.target.value })} className={controlClass} /></Field>
          <button type="button" onClick={save} disabled={busy} className="px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">Save details</button>
        </section>
      </div>
      <section className="border border-border rounded-lg overflow-hidden">
        <div className="px-4 py-3 border-b border-border"><h3 className="text-sm font-medium">Evidence</h3></div>
        {evidence.length === 0 ? <Empty text="No evidence saved. Run Research to collect cited sources." /> : (
          <ul className="divide-y divide-border">
            {evidence.map((item) => (
              <li key={item.id} className="px-4 py-3">
                <a href={item.url} target="_blank" rel="noreferrer" className="text-sm font-medium text-accent hover:underline">{item.title || item.url}</a>
                <p className="mt-1 text-xs text-text-muted">{item.excerpt || "No excerpt available."}</p>
                <div className="mt-1 text-[10px] text-text-dim">{item.source_kind} · {dateLabel(item.retrieved_at)}</div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function OutreachWorkspace({ candidate, handoff, outreach, capabilities, busy, api, runAction }: {
  candidate: Candidate;
  handoff: Record<string, unknown> | null;
  outreach: OutreachData | null;
  capabilities: Capabilities;
  busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  runAction: (action: () => Promise<unknown>, success: string) => Promise<void>;
}) {
  const senderRows = outreach?.messaging?.senders?.senders || [];
  const senderChannels = new Set(senderRows.filter((sender) => sender.verified && sender.sending_enabled).map((sender) => sender.channel));
  const availableChannels = [
    candidate.email && senderChannels.has("email") ? "email" : "",
    candidate.phone && senderChannels.has("sms") ? "sms" : "",
    candidate.phone && senderChannels.has("whatsapp") ? "whatsapp" : "",
  ].filter(Boolean);
  const channelKey = availableChannels.join(",");
  const [channel, setChannel] = useState("");
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [from, setFrom] = useState("");
  const [conversationId, setConversationId] = useState(0);
  const [templateId, setTemplateId] = useState(0);
  const [templateVars, setTemplateVars] = useState("{}");
  useEffect(() => {
    setChannel((current) => availableChannels.includes(current) ? current : availableChannels[0] || "");
  }, [candidate.id, channelKey]);
  useEffect(() => {
    setConversationId(0);
    setFrom("");
    setTemplateId(0);
  }, [channel]);

  if (!capabilities.crm) {
    return <section className="border border-border rounded-lg p-4"><h3 className="text-sm font-medium">Outreach</h3><p className="mt-2 text-xs text-text-muted">Connect CRM to start email, SMS, or WhatsApp outreach. Prospecting never connects to Messaging directly.</p></section>;
  }
  if (!handoff) {
    return (
      <section className="border border-accent/40 rounded-lg p-4 flex flex-wrap items-center gap-4 bg-accent/5">
        <div><h3 className="text-sm font-medium">Ready for outreach?</h3><p className="mt-1 text-xs text-text-muted">Create or link the CRM contact first. This step does not send a message.</p></div>
        <button type="button" onClick={() => runAction(() => api(`/candidates/${candidate.id}/start-outreach`, { method: "POST", body: "{}" }), "Outreach started. The CRM contact is linked; no message was sent.")} disabled={busy || (!candidate.email && !candidate.phone)} className="ml-auto px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-40">Start outreach</button>
      </section>
    );
  }
  if (!outreach) {
    return <section className="border border-border rounded-lg p-4 text-xs text-text-muted">Loading CRM outreach…</section>;
  }

  const messaging = outreach.messaging;
  const conversations = outreach.context?.conversations || [];
  const activities = outreach.context?.activities || [];
  const channelConversations = conversations.filter((conversation) => conversation.channel === channel);
  const channelSenders = senderRows.filter((sender) => sender.channel === channel && sender.verified && sender.sending_enabled);
  const whatsappTemplates = outreach.messaging?.whatsapp_templates?.templates || [];
  const whatsappSessionActive = outreach.messaging?.whatsapp_session?.active === true;
  const whatsappNeedsTemplate = channel === "whatsapp" && !whatsappSessionActive;
  const needsSubject = channel === "email" && conversationId === 0 && templateId === 0;
  const canSend = !!channel && (!!body.trim() || templateId > 0) && (!needsSubject || !!subject.trim()) && (!whatsappNeedsTemplate || templateId > 0);
  const send = () => {
    let parsedVars: Record<string, unknown> | undefined;
    if (templateId > 0 && templateVars.trim()) {
      try {
        parsedVars = JSON.parse(templateVars);
      } catch {
        window.alert("Template variables must be valid JSON.");
        return;
      }
    }
    const destination = channel === "email" ? candidate.email : candidate.phone;
    if (!window.confirm(`Send this ${channel} message to ${destination} now? This is a real external send.`)) return;
    const idempotencyKey = globalThis.crypto?.randomUUID?.() || `prospecting-${candidate.id}-${Date.now()}`;
    return runAction(() => api(`/candidates/${candidate.id}/send`, {
      method: "POST",
      body: JSON.stringify({ channel, subject, body, from, conversation_id: conversationId || undefined, template_id: templateId || undefined, template_vars: parsedVars, idempotency_key: idempotencyKey, confirm: true }),
    }), `${channel === "email" ? "Email" : channel === "sms" ? "SMS" : "WhatsApp"} sent through CRM.`).then(() => { setBody(""); setSubject(""); setTemplateId(0); });
  };

  return (
    <section className="border border-border rounded-lg overflow-hidden">
      <div className="px-4 py-3 border-b border-border flex flex-wrap items-center gap-2">
        <div><h3 className="text-sm font-medium">Outreach</h3><p className="text-[11px] text-text-muted">CRM contact #{String(outreach.crm_contact_id || handoff.crm_contact_id)} · CRM records every message and reply</p></div>
        <span className="ml-auto rounded bg-green/10 px-2 py-1 text-[10px] font-medium text-green">CRM linked</span>
      </div>
      {outreach.error ? <div className="p-4 text-xs text-red">{outreach.error}</div> : (
        <div className="grid md:grid-cols-2">
          <div className="p-4 space-y-3 border-b md:border-b-0 md:border-r border-border">
            <h4 className="text-xs font-medium">Compose message</h4>
            {!messaging?.available ? (
              <div className="rounded border border-border bg-bg-input/40 p-3 text-xs"><div className="font-medium">CRM is connected, but Messaging is unavailable</div><p className="mt-1 text-text-muted">{messaging?.error || "Connect Messaging inside CRM to enable email, SMS, and WhatsApp."}</p></div>
            ) : availableChannels.length === 0 ? (
              <div className="rounded border border-border bg-bg-input/40 p-3 text-xs text-text-muted">No verified sender matches this lead’s email or phone channels. Configure senders in Messaging through CRM.</div>
            ) : (
              <>
                <div className="grid sm:grid-cols-2 gap-2">
                  <Field label="Channel"><select value={channel} onChange={(event) => setChannel(event.target.value)} className={controlClass}>{availableChannels.map((item) => <option key={item} value={item}>{item === "email" ? "Email" : item === "sms" ? "SMS" : "WhatsApp"}</option>)}</select></Field>
                  <Field label="Sender"><select value={from} onChange={(event) => setFrom(event.target.value)} className={controlClass}><option value="">CRM default</option>{channelSenders.map((sender) => <option key={`${sender.channel}-${sender.address}`} value={sender.address}>{sender.address}</option>)}</select></Field>
                </div>
                {channelConversations.length > 0 && <Field label="Conversation"><select value={conversationId} onChange={(event) => setConversationId(Number(event.target.value))} className={controlClass}><option value={0}>Start a new conversation</option>{channelConversations.map((conversation) => <option key={conversation.id} value={conversation.id}>Reply to {conversation.subject || `${conversation.channel} conversation`} · {conversation.status}</option>)}</select></Field>}
                {needsSubject && <Field label="Subject"><input value={subject} onChange={(event) => setSubject(event.target.value)} className={controlClass} /></Field>}
                {channel === "whatsapp" && <div className="rounded border border-border bg-bg-input/40 p-3 text-xs">
                  <div className="font-medium">{whatsappSessionActive ? "WhatsApp reply window is active" : "Approved template required"}</div>
                  {!whatsappSessionActive && <p className="mt-1 text-text-muted">There is no active 24-hour reply session for this contact.</p>}
                  {whatsappTemplates.length > 0 && <div className="mt-2"><select value={templateId} onChange={(event) => setTemplateId(Number(event.target.value))} className={controlClass}><option value={0}>{whatsappSessionActive ? "No template" : "Choose an approved template"}</option>{whatsappTemplates.map((template) => <option key={template.id} value={template.id}>{template.name}</option>)}</select></div>}
                  {!whatsappSessionActive && whatsappTemplates.length === 0 && <p className="mt-2 text-red">No approved WhatsApp templates are available.</p>}
                </div>}
                {templateId > 0 && <Field label="Template variables" hint='JSON, for example {"name":"Alex"}'><textarea value={templateVars} onChange={(event) => setTemplateVars(event.target.value)} rows={3} className={`${controlClass} font-mono text-xs`} /></Field>}
                <Field label={templateId > 0 ? "Additional message body" : "Message"} hint={templateId > 0 ? "Optional when using a template" : undefined}><textarea value={body} onChange={(event) => setBody(event.target.value)} rows={5} className={controlClass} /></Field>
                <button type="button" onClick={send} disabled={busy || !canSend} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-40">{conversationId ? "Send reply" : `Send ${channel}`}</button>
                <p className="text-[10px] text-text-dim">A confirmation is always required. CRM applies suppression, threading, sender, and WhatsApp rules.</p>
              </>
            )}
          </div>
          <div className="min-h-48">
            <div className="px-4 py-3 border-b border-border flex items-center"><h4 className="text-xs font-medium">CRM activity</h4><span className="ml-auto text-[10px] text-text-dim">{activities.length} recent</span></div>
            {activities.length === 0 ? <Empty text="No outreach activity yet." /> : <ul className="divide-y divide-border max-h-96 overflow-auto">{activities.map((activity) => <li key={activity.id} className="px-4 py-3"><div className="flex items-center gap-2"><span className="text-[10px] uppercase tracking-wide text-text-dim">{activity.kind.replaceAll("_", " ")}</span>{activity.message_status?.status && <Status value={activity.message_status.status} />}</div><p className="mt-1 text-xs whitespace-pre-wrap">{activity.body}</p><div className="mt-1 text-[10px] text-text-dim">{dateLabel(activity.occurred_at)}</div></li>)}</ul>}
          </div>
        </div>
      )}
    </section>
  );
}

function SettingsView({ profiles, exclusions, busy, api, runAction }: {
  profiles: Profile[];
  exclusions: Exclusion[];
  busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  runAction: (action: () => Promise<unknown>, success: string) => Promise<void>;
}) {
  const [editingId, setEditingId] = useState(0);
  const [draft, setDraft] = useState({ ...emptyProfile });
  const edit = (profile?: Profile) => {
    setEditingId(profile?.id || 0);
    setDraft(profile ? {
      name: profile.name,
      description: profile.description,
      industries: profile.industries.join(", "),
      locations: profile.locations.join(", "),
      employee_min: profile.employee_min == null ? "" : String(profile.employee_min),
      employee_max: profile.employee_max == null ? "" : String(profile.employee_max),
      target_titles: profile.target_titles.join(", "),
      keywords: profile.keywords.join(", "),
    } : { ...emptyProfile });
  };
  const save = () => {
    const payload = {
      name: draft.name.trim(),
      description: draft.description.trim(),
      industries: split(draft.industries),
      locations: split(draft.locations),
      employee_min: draft.employee_min === "" ? null : Number(draft.employee_min),
      employee_max: draft.employee_max === "" ? null : Number(draft.employee_max),
      target_titles: split(draft.target_titles),
      keywords: split(draft.keywords),
    };
    return runAction(async () => {
      await api(editingId ? `/profiles/${editingId}` : "/profiles", { method: editingId ? "PATCH" : "POST", body: JSON.stringify(payload) });
      edit();
    }, editingId ? "Target profile updated." : "Target profile created.");
  };
  return (
    <div className="prospecting-full-width w-full p-5 lg:p-6 grid grid-cols-1 lg:grid-cols-2 gap-5">
      <section className="border border-border rounded-lg overflow-hidden">
        <div className="px-4 py-3 border-b border-border flex items-center"><h2 className="text-sm font-medium">Target profiles</h2><button type="button" onClick={() => edit()} className="ml-auto text-xs text-accent">New profile</button></div>
        <div className="p-4 space-y-4">
          <div className="flex flex-wrap gap-2">
            {profiles.map((profile) => <button key={profile.id} type="button" onClick={() => edit(profile)} className={`px-2.5 py-1 text-xs rounded border ${editingId === profile.id ? "border-accent text-accent" : "border-border"}`}>{profile.name}{profile.status === "archived" ? " (archived)" : ""}</button>)}
          </div>
          <Field label="Name"><input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} className={controlClass} /></Field>
          <Field label="Description"><textarea value={draft.description} onChange={(event) => setDraft({ ...draft, description: event.target.value })} rows={2} className={controlClass} /></Field>
          <div className="grid sm:grid-cols-2 gap-3">
            <Field label="Industries" hint="Comma-separated"><input value={draft.industries} onChange={(event) => setDraft({ ...draft, industries: event.target.value })} className={controlClass} /></Field>
            <Field label="Locations" hint="Comma-separated"><input value={draft.locations} onChange={(event) => setDraft({ ...draft, locations: event.target.value })} className={controlClass} /></Field>
            <Field label="Minimum employees"><input type="number" min={0} value={draft.employee_min} onChange={(event) => setDraft({ ...draft, employee_min: event.target.value })} className={controlClass} /></Field>
            <Field label="Maximum employees"><input type="number" min={0} value={draft.employee_max} onChange={(event) => setDraft({ ...draft, employee_max: event.target.value })} className={controlClass} /></Field>
            <Field label="Target job titles" hint="Comma-separated"><input value={draft.target_titles} onChange={(event) => setDraft({ ...draft, target_titles: event.target.value })} className={controlClass} /></Field>
            <Field label="Keywords" hint="Comma-separated"><input value={draft.keywords} onChange={(event) => setDraft({ ...draft, keywords: event.target.value })} className={controlClass} /></Field>
          </div>
          <div className="flex gap-2">
            <button type="button" onClick={save} disabled={busy || !draft.name.trim()} className="px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">{editingId ? "Save profile" : "Create profile"}</button>
            {editingId > 0 && profiles.find((profile) => profile.id === editingId)?.status === "active" && <button type="button" onClick={() => runAction(() => api(`/profiles/${editingId}/archive`, { method: "POST", body: "{}" }), "Profile archived.")} disabled={busy} className={secondaryButton}>Archive</button>}
          </div>
        </div>
      </section>
      <div className="space-y-5">
        <PlacesSettings api={api} runAction={runAction} busy={busy} />
        <section className="border border-border rounded-lg p-4">
		  <h2 className="text-sm font-medium">Standalone by default</h2>
		  <ul className="mt-3 space-y-2 text-xs text-text-muted">
			<li>Manual creation, CSV/JSON import, catalog exploration, decisions, and export require no other app.</li>
			<li>Optional Web adds discovery and first-party page extraction; Google can fall back to DuckDuckGo when blocked.</li>
			<li>Noise filtering, field extraction, workflow signals, eligibility, and scoring use fixed rules—not an AI model.</li>
			<li>Prospecting owns candidates, evidence references, decisions, and scores.</li>
            <li>Optional CRM provides duplicate-safe contact ownership plus email, SMS, and WhatsApp through its Messaging binding.</li>
            <li>Starting outreach only links the contact. Every real message requires a separate confirmation.</li>
            <li>Prospecting does not call leads, run campaigns, or create opportunities.</li>
          </ul>
        </section>
        <section className="border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-border"><h2 className="text-sm font-medium">Exclusions</h2></div>
          {exclusions.length === 0 ? <Empty text="No exclusions yet. Reject a candidate and choose to exclude its company." /> : (
            <ul className="divide-y divide-border max-h-80 overflow-auto">
              {exclusions.map((item) => (
                <li key={item.id} className="px-4 py-3 flex gap-3 items-center">
                  <div className="min-w-0"><div className="text-sm truncate">{item.value}</div><div className="text-xs text-text-muted">{item.kind}{item.reason ? ` · ${item.reason}` : ""}</div></div>
                  <button type="button" onClick={() => runAction(() => api(`/exclusions/${item.id}`, { method: "DELETE" }), "Exclusion removed.")} disabled={busy} className="ml-auto text-xs text-red">Remove</button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}

function AddLeadsPanel({ profiles, onClose, onCreate, onImport }: {
  profiles: Profile[];
  onClose: () => void;
  onCreate: (profileId: number, body: typeof emptyCandidate) => Promise<void>;
  onImport: (profileId: number, format: string, data: string) => Promise<void>;
}) {
  const [mode, setMode] = useState<"manual" | "import">("manual");
  const [profileId, setProfileId] = useState(profiles[0]?.id || 0);
  const [draft, setDraft] = useState({ ...emptyCandidate });
  const [format, setFormat] = useState("auto");
  const [data, setData] = useState("");
  return (
    <div className="fixed inset-0 z-50 bg-black/60 flex justify-end" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <aside className="h-full w-full max-w-xl bg-bg border-l border-border shadow-xl flex flex-col">
        <div className="shrink-0 px-5 py-4 border-b border-border flex items-start gap-4">
          <div><h2 className="font-medium">Add leads</h2><p className="mt-1 text-xs text-text-muted">Create one lead or import a CSV/JSON list.</p></div>
          <button type="button" onClick={onClose} aria-label="Close add leads panel" className="ml-auto text-xl leading-none text-text-muted hover:text-text">×</button>
        </div>
        <div className="shrink-0 px-5 border-b border-border flex gap-1">
          <button type="button" onClick={() => setMode("manual")} className={`px-3 py-3 text-xs border-b-2 ${mode === "manual" ? "border-accent text-text" : "border-transparent text-text-muted"}`}>Add manually</button>
          <button type="button" onClick={() => setMode("import")} className={`px-3 py-3 text-xs border-b-2 ${mode === "import" ? "border-accent text-text" : "border-transparent text-text-muted"}`}>Import list</button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto p-5 space-y-4">
          <Field label="Target profile"><select value={profileId} onChange={(event) => setProfileId(Number(event.target.value))} className={controlClass}>{profiles.length === 0 && <option value={0}>Imported leads (created automatically)</option>}{profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}</select></Field>
          {mode === "manual" ? (
            <div className="grid sm:grid-cols-2 gap-3">
              <div className="sm:col-span-2"><Field label="Company name"><input value={draft.company_name} onChange={(event) => setDraft({ ...draft, company_name: event.target.value })} className={controlClass} /></Field></div>
              <Field label="Website"><input value={draft.website} onChange={(event) => setDraft({ ...draft, website: event.target.value })} className={controlClass} /></Field>
              <Field label="Company domain"><input value={draft.company_domain} onChange={(event) => setDraft({ ...draft, company_domain: event.target.value })} className={controlClass} /></Field>
              <Field label="Decision-maker"><input value={draft.person_display_name} onChange={(event) => setDraft({ ...draft, person_display_name: event.target.value })} className={controlClass} /></Field>
              <Field label="Job title"><input value={draft.job_title} onChange={(event) => setDraft({ ...draft, job_title: event.target.value })} className={controlClass} /></Field>
              <Field label="Work email"><input type="email" value={draft.email} onChange={(event) => setDraft({ ...draft, email: event.target.value })} className={controlClass} /></Field>
              <Field label="Phone"><input value={draft.phone} onChange={(event) => setDraft({ ...draft, phone: event.target.value })} className={controlClass} /></Field>
              <div className="sm:col-span-2"><Field label="Summary"><textarea value={draft.summary} onChange={(event) => setDraft({ ...draft, summary: event.target.value })} rows={4} className={controlClass} /></Field></div>
            </div>
          ) : (
            <>
              <Field label="Format"><select value={format} onChange={(event) => setFormat(event.target.value)} className={controlClass}><option value="auto">Detect automatically</option><option value="csv">CSV</option><option value="json">JSON</option></select></Field>
              <Field label="Lead data" hint="CSV headers: company, website, contact_name, title, email, phone, notes, source_url">
                <textarea value={data} onChange={(event) => setData(event.target.value)} rows={16} placeholder={'company,email,phone,website\nAcme Dental,hello@acme.com,512-555-0100,https://acme.com'} className={`${controlClass} font-mono text-xs`} />
              </Field>
              <p className="text-xs text-text-muted">Duplicates are skipped. Imports are limited to 1,000 rows per request.</p>
            </>
          )}
        </div>
        <div className="shrink-0 px-5 py-4 border-t border-border flex justify-end gap-2">
          <button type="button" onClick={onClose} className={secondaryButton}>Cancel</button>
          {mode === "manual" ? (
            <button type="button" onClick={() => onCreate(profileId, draft)} disabled={!draft.company_name.trim()} className="px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">Create lead</button>
          ) : (
            <button type="button" onClick={() => onImport(profileId, format, data)} disabled={!data.trim()} className="px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">Import leads</button>
          )}
        </div>
      </aside>
    </div>
  );
}

function Metric({ label, value, accent = false }: { label: string; value: number; accent?: boolean }) {
  return <div className={`border rounded-lg p-4 ${accent ? "border-accent/50 bg-accent/5" : "border-border"}`}><div className="text-2xl font-semibold">{value}</div><div className="mt-1 text-xs text-text-muted">{label}</div></div>;
}

function CapabilityBadge({ label, available }: { label: string; available: boolean }) {
  return <span className={`rounded px-2 py-1 ${available ? "bg-green/10 text-green" : "bg-border text-text-muted"}`}>{label}: {available ? "connected" : "optional"}</span>;
}

function Score({ value, label }: { value: number; label: string }) {
  return <span className="text-[10px] px-1.5 py-0.5 rounded bg-bg border border-border whitespace-nowrap">{value} {label}</span>;
}

function Status({ value }: { value: string }) {
	const tones: Record<string, string> = { ready: "bg-accent/10 text-accent", eligible: "bg-green/10 text-green", review: "bg-amber/10 text-amber", ineligible: "bg-red/10 text-red", accepted: "bg-green/10 text-green", rejected: "bg-red/10 text-red", failed: "bg-red/10 text-red", researching: "bg-amber/10 text-amber", running: "bg-amber/10 text-amber", deferred: "bg-border text-text-muted", completed: "bg-green/10 text-green" };
  return <span className={`ml-auto text-[10px] px-1.5 py-0.5 rounded whitespace-nowrap ${tones[value] || "bg-border text-text-muted"}`}>{value}</span>;
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label className="block"><span className="block text-xs text-text-muted mb-1">{label}{hint && <span className="text-text-dim"> · {hint}</span>}</span>{children}</label>;
}

function ProfileFact({ label, values }: { label: string; values: string[] }) {
  return <div><div className="text-[10px] uppercase tracking-wide text-text-dim">{label}</div><div className="mt-1 text-xs">{values.length ? values.join(", ") : "Any"}</div></div>;
}

function Empty({ text }: { text: string }) {
  return <div className="p-5 text-sm text-text-muted">{text}</div>;
}

function split(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function queryPreview(profile: Profile): string {
  const group = (values: string[]) => values.length > 1 ? `(${values.map(quoted).join(" OR ")})` : values.map(quoted).join("");
  const parts = [group(profile.industries), group(profile.locations), group(profile.keywords)].filter(Boolean);
  if (!parts.length) parts.push(quoted(profile.name));
  return `${parts.join(" ")} company`;
}

function quoted(value: string): string {
  return value.includes(" ") ? `"${value}"` : value;
}

function dateLabel(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

const controlClass = "w-full bg-bg-input border border-border rounded px-2.5 py-1.5 text-sm outline-none focus:border-accent";
const secondaryButton = "px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50";

const panelLayoutCSS = `
  .prospecting-full-width {
    box-sizing: border-box;
    width: 100% !important;
    max-width: none !important;
  }

  .prospecting-discover-grid {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(280px, 1fr);
  }

  .prospecting-workspace-grid {
    display: grid;
    grid-template-columns: clamp(320px, 28vw, 520px) minmax(0, 1fr);
  }

  .prospecting-workspace-detail {
    box-sizing: border-box;
    min-width: 0;
    width: 100%;
    max-width: none;
  }

  @media (max-width: 1023px) {
    .prospecting-discover-grid,
    .prospecting-workspace-grid {
      grid-template-columns: minmax(0, 1fr);
    }

    .prospecting-workspace-grid {
      grid-template-rows: minmax(260px, 42vh) minmax(0, 1fr);
    }
  }
`;
