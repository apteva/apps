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
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("ready");
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
      setCandidates(candidateData.candidates || []);
      setRuns(runData.runs || []);
      setExclusions(exclusionData.exclusions || []);
      setSelectedCandidateId((current) => current && (candidateData.candidates || []).some((c: Candidate) => c.id === current)
        ? current
        : candidateData.candidates?.[0]?.id || 0);
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
    if (!id) {
      setSelectedEvidence([]);
      setHandoff(null);
      return;
    }
    try {
      const data = await api(`/candidates/${id}`);
      setSelectedEvidence(data.evidence || []);
      setHandoff(data.handoff || null);
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
      <header className="shrink-0 border-b border-border">
        <div className="px-5 lg:px-6 pt-4 pb-3 flex items-start gap-4">
          <div>
            <h1 className="text-lg font-semibold">Prospecting</h1>
            <p className="text-xs text-text-muted">Seed, explore, and share a lead catalog. Web discovery and CRM handoff are optional.</p>
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
          <DiscoverView profiles={profiles.filter((profile) => profile.status === "active")} runs={runs} webAvailable={capabilities.web} busy={busy} api={api} onDone={async (text) => { setMessage(text); await load(); setTab("candidates"); }} onError={setMessage} setBusy={setBusy} />
        )}
        {tab === "candidates" && (
          <CandidatesView
            profiles={profiles}
            candidates={candidates}
            selected={selectedCandidate}
            evidence={selectedEvidence}
            handoff={handoff}
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
    <div className="w-full p-5 lg:p-6 space-y-5">
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
          <p className="mt-1 text-sm text-text-muted">Add or import leads, review them here, and export a portable list. Optional integrations add discovery and CRM handoff.</p>
          <div className="mt-2 flex gap-2 text-[11px]"><CapabilityBadge label="Web discovery" available={capabilities.web} /><CapabilityBadge label="CRM handoff" available={capabilities.crm} /></div>
        </div>
        <button type="button" onClick={capabilities.web ? onDiscover : onCandidates} className="ml-auto px-4 py-2 text-sm bg-accent text-bg rounded font-medium">{capabilities.web ? "Discover companies" : "Seed leads"}</button>
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

function DiscoverView({ profiles, runs, webAvailable, busy, api, onDone, onError, setBusy }: {
  profiles: Profile[];
  runs: Run[];
  webAvailable: boolean;
  busy: boolean;
  api: (path: string, init?: RequestInit) => Promise<any>;
  onDone: (message: string) => Promise<void>;
  onError: (message: string) => void;
  setBusy: (busy: boolean) => void;
}) {
  const [profileId, setProfileId] = useState(profiles[0]?.id || 0);
  const [customQuery, setCustomQuery] = useState("");
  const [limit, setLimit] = useState(20);
  useEffect(() => {
    if (!profiles.some((profile) => profile.id === profileId)) setProfileId(profiles[0]?.id || 0);
  }, [profileId, profiles]);
  const profile = profiles.find((item) => item.id === profileId);
  const generated = profile ? queryPreview(profile) : "";
  const run = async () => {
    if (!profileId) return;
    setBusy(true);
    onError("");
    try {
      const data = await api("/discover", { method: "POST", body: JSON.stringify({ profile_id: profileId, query: customQuery.trim(), limit }) });
      await onDone(`${data.created || 0} new candidates via ${data.engine || "Web"}; ${data.duplicates || 0} duplicates and ${data.excluded || 0} noisy or excluded results skipped${data.fallback_used ? " (fallback search used)" : ""}.`);
    } catch (error) {
      onError((error as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="w-full p-5 lg:p-6 grid grid-cols-1 lg:grid-cols-3 gap-5">
      <section className="lg:col-span-2 border border-border rounded-lg p-5 space-y-5">
        <div>
          <h2 className="font-medium">Discover companies</h2>
          <p className="mt-1 text-sm text-text-muted">Run a bounded browser-backed search with automatic provider fallback and deterministic noise filtering. No messages are sent.</p>
        </div>
        {!webAvailable ? (
          <div className="rounded border border-border bg-bg-input/40 p-4 text-sm">
            <div className="font-medium">Web discovery is optional and not connected</div>
            <p className="mt-1 text-text-muted">You can continue using the Leads tab to add, import, explore, and export leads. Connect the Web app only when you want browser-backed discovery and enrichment.</p>
          </div>
        ) : profiles.length === 0 ? <Empty text="Create an active target profile in Settings first." /> : (
          <>
            <Field label="Target profile">
              <select value={profileId} onChange={(event) => setProfileId(Number(event.target.value))} className={controlClass}>
                {profiles.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
            </Field>
            {profile && (
              <div className="rounded border border-border bg-bg-input/40 p-4 text-sm">
                <div className="grid sm:grid-cols-2 gap-3">
                  <ProfileFact label="Industries" values={profile.industries} />
                  <ProfileFact label="Locations" values={profile.locations} />
                  <ProfileFact label="Target roles" values={profile.target_titles} />
                  <ProfileFact label="Keywords" values={profile.keywords} />
                </div>
              </div>
            )}
            <Field label="Search query" hint="Leave blank to use the generated query shown below.">
              <textarea value={customQuery} onChange={(event) => setCustomQuery(event.target.value)} rows={3} placeholder={generated} className={controlClass} />
            </Field>
            <div className="text-xs text-text-muted"><span className="font-medium text-text">Planned query:</span> {customQuery.trim() || generated}</div>
            <Field label={`Maximum results: ${limit}`}>
              <input type="range" min={5} max={50} step={5} value={limit} onChange={(event) => setLimit(Number(event.target.value))} className="w-full" />
            </Field>
            <button type="button" onClick={run} disabled={busy || !profileId} className="px-4 py-2 bg-accent text-bg rounded text-sm font-medium disabled:opacity-50">
              {busy ? "Searching the Web…" : "Run discovery"}
            </button>
          </>
        )}
      </section>
      <section className="border border-border rounded-lg overflow-hidden self-start">
        <div className="px-4 py-3 border-b border-border"><h2 className="text-sm font-medium">Recent runs</h2></div>
        {runs.length === 0 ? <Empty text="No runs yet." /> : (
          <ul className="divide-y divide-border max-h-[520px] overflow-auto">
            {runs.slice(0, 15).map((item) => (
              <li key={item.id} className="px-4 py-3">
                <div className="flex gap-2 items-center"><span className="text-xs truncate">{item.query}</span><Status value={item.status} /></div>
                <div className="mt-1 text-[11px] text-text-muted">{item.result_count} new · {dateLabel(item.started_at)}</div>
                {item.error && <div className="mt-1 text-xs text-red">{item.error}</div>}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function CandidatesView({ profiles, candidates, selected, evidence, handoff, selectedId, setSelectedId, query, setQuery, statusFilter, setStatusFilter, profileFilter, setProfileFilter, capabilities, projectId, busy, api, runAction }: {
  profiles: Profile[];
  candidates: Candidate[];
  selected: Candidate | null;
  evidence: Evidence[];
  handoff: Record<string, unknown> | null;
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
    body: JSON.stringify({ profile_id: profileFilter || undefined, status: statusFilter === "all" ? "ready" : statusFilter, limit: 10, max_pages: 5 }),
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
        <div className="h-full min-h-0 grid grid-cols-1 lg:grid-cols-3 border border-border rounded-lg overflow-hidden bg-bg">
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
          <section className="lg:col-span-2 min-h-0 overflow-auto">
            {selected ? (
              <CandidateDetail candidate={selected} profile={profiles.find((profile) => profile.id === selected.profile_id)} evidence={evidence} handoff={handoff} capabilities={capabilities} busy={busy} api={api} runAction={runAction} />
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

function CandidateDetail({ candidate, profile, evidence, handoff, capabilities, busy, api, runAction }: {
  candidate: Candidate;
  profile?: Profile;
  evidence: Evidence[];
  handoff: Record<string, unknown> | null;
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
  const accept = () => runAction(() => api(`/candidates/${candidate.id}/accept`, { method: "POST", body: "{}" }), "Candidate accepted into CRM. No message was sent.");
  return (
    <div className="w-full p-5 space-y-5">
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
          <button type="button" onClick={accept} disabled={busy || !capabilities.crm || candidate.status === "accepted" || (!candidate.email && !candidate.phone)} title={!capabilities.crm ? "Connect the optional CRM app to hand off this lead" : !candidate.email && !candidate.phone ? "Add an email or phone first" : "Writes to CRM; does not send a message"} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-40">Send to CRM</button>
        </div>
      </div>
      {handoff && <div className="rounded border border-green/40 bg-green/10 px-4 py-3 text-sm">Linked to CRM contact #{String(handoff.crm_contact_id)}. No message was sent.</div>}
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
    <div className="w-full p-5 lg:p-6 grid grid-cols-1 lg:grid-cols-2 gap-5">
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
        <section className="border border-border rounded-lg p-4">
		  <h2 className="text-sm font-medium">Standalone by default</h2>
		  <ul className="mt-3 space-y-2 text-xs text-text-muted">
			<li>Manual creation, CSV/JSON import, catalog exploration, decisions, and export require no other app.</li>
			<li>Optional Web adds discovery and first-party page extraction; Google can fall back to DuckDuckGo when blocked.</li>
			<li>Noise filtering, field extraction, workflow signals, eligibility, and scoring use fixed rules—not an AI model.</li>
			<li>Prospecting owns candidates, evidence references, decisions, and scores.</li>
            <li>Optional CRM receives only leads explicitly sent to it and provides duplicate-safe contact ownership.</li>
            <li>Sending a lead to CRM never sends a message or creates an opportunity.</li>
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
