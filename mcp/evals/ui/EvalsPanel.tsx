import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";

interface NativePanelProps {
  installId: number;
  projectId: string;
}

interface Agent {
  id: number;
  name: string;
  status: string;
  directive?: string;
  directive_etag?: string;
}

interface AgentCapability {
  app_name: string;
  install_id: number;
}

interface Model {
  gateway_model?: string;
  display_name?: string;
  provider?: string;
  model_id?: string;
}

interface Environment {
  id: string;
  name: string;
  description?: string;
  spec?: EnvironmentSpec;
}

interface IntegrationRole {
  role: string;
  kind?: string;
  compatible_slugs?: string[];
}

interface CatalogApp {
  install_id: number;
  name: string;
  display_name?: string;
  description?: string;
  integration_roles?: IntegrationRole[];
}

interface Connection {
  id: number;
  app_slug: string;
  name: string;
  status: string;
}

interface Integration {
  slug: string;
  name: string;
  description?: string;
  tool_count?: number;
}

interface EnvironmentTool {
  name: string;
  description?: string;
  inputSchema?: Record<string, any>;
}

interface SeedStep {
  app: string;
  tool: string;
  input: Record<string, any>;
}

interface EnvironmentSpec {
  version: number;
  ttl_seconds: number;
  app_install_ids: number[];
  connection_ids: number[];
  network_mode: string;
  integration_mode: string;
  integration_bindings: Record<string, any>[];
  seeds: SeedStep[];
}

interface TaskDraft {
  id: number;
  name: string;
  prompt: string;
  success: string;
}

interface Target {
  agent_id: number;
  agent_name?: string;
  model?: string;
  provider?: string;
}

interface Assertion {
  name: string;
  type: string;
  app?: string;
  tool?: string;
  input?: Record<string, any>;
  path?: string;
  equals?: any;
  method?: string;
  host?: string;
  min_calls?: number;
  agent_alias?: string;
  event_type?: string;
  fixture?: string;
}

interface EvalCase {
  id: string;
  suite_id: string;
  name: string;
  prompt: string;
  goals: string[];
  assertions: Assertion[];
  environment_id?: string;
  timeout_seconds: number;
  max_turns: number;
  enabled: boolean;
  revision: number;
}

interface Suite {
  id: string;
  name: string;
  description?: string;
  environment_id?: string;
  judge_model?: string;
  continuous_targets?: Target[];
  schedule_minutes: number;
  required_pass_rate: number;
  enabled: boolean;
  revision: number;
  cases: EvalCase[];
}

interface Metrics {
  provider?: string;
  model?: string;
  llm_calls: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  tool_calls: number;
  errors: number;
}

interface TraceEvent {
  index: number;
  role: string;
  content?: string;
  tool_call?: {
    name: string;
    input?: any;
    output?: string;
    is_error?: boolean;
  };
}

interface EvalRun {
  id: string;
  case_id: string;
  status: string;
  target_index: number;
  repetition: number;
  case: EvalCase;
  target: Target;
  overall_score?: number;
  correctness_score?: number;
  judge_score?: number;
  error?: string;
  assertions: any[];
  suggestions?: { id: string; directive: string; reason: string; status: string }[];
  execution?: {
    status: string;
    reason: string;
    turns: number;
    trace: TraceEvent[];
    metrics: Metrics;
  };
  judge?: {
    passed: boolean;
    score: number;
    reasoning: string;
    per_goal: any[];
  };
}

interface TargetSummary {
  target_index: number;
  target: Target;
  runs: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  average_cost_usd: number;
  average_tokens: number;
}

interface Summary {
  total: number;
  queued: number;
  running: number;
  passed: number;
  failed: number;
  errors: number;
  pass_rate: number;
  average_score: number;
  targets: TargetSummary[];
}

interface Experiment {
  id: string;
  suite_id: string;
  name: string;
  trigger_type: string;
  status: string;
  targets: Target[];
  repetitions: number;
  judge_model?: string;
  created_at: string;
  finished_at?: string;
  summary?: Summary;
  runs?: EvalRun[];
}

interface Catalog {
  agents: Agent[];
  models: Model[];
  environments: Environment[];
  apps: CatalogApp[];
  connections: Connection[];
  integrations: Integration[];
  snapshots: any[];
}

const API = "/api/apps/evals/api";
let installID = 0;
let projectID = "";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const url = new URL(API + path, window.location.origin);
  if (installID) url.searchParams.set("install_id", String(installID));
  if (projectID) url.searchParams.set("project_id", projectID);
  const response = await fetch(url.pathname + url.search, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(body.error || response.statusText);
  }
  return response.json();
}

const modelID = (model: Model) =>
  model.gateway_model || `${model.provider || ""}/${model.model_id || ""}`.replace(/^\//, "");
const modelLabel = (model: Model) => model.display_name || modelID(model);
const pct = (value?: number) => `${Math.round((value || 0) * 100)}%`;
const score = (value?: number) => (value == null ? "-" : String(Math.round(value)));
const goalsFromText = (value: string) => value.split("\n").map((line) => line.trim()).filter(Boolean);
const formatDate = (value: string) => new Date(value).toLocaleString([], {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

const styles = `
.ev-shell,.ev-shell *{box-sizing:border-box}.ev-shell{height:100%;width:100%;overflow:auto;background:var(--color-bg);color:var(--color-text)}
.ev-page{width:100%;min-width:0;padding:20px}.ev-toolbar{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:16px}
.ev-title{margin:0;font-size:20px;line-height:1.2;font-weight:650}.ev-tabs{display:flex;gap:18px;margin-top:10px}.ev-tab{padding:0 0 6px;border-bottom:2px solid transparent;color:var(--color-text-dim);font-size:13px}.ev-tab[data-active=true]{border-color:var(--color-accent);color:var(--color-text)}
.ev-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.ev-button,.ev-primary,.ev-icon-button{display:inline-flex;align-items:center;justify-content:center;border-radius:6px;font-size:13px;line-height:1;white-space:nowrap}.ev-button{min-height:34px;padding:0 12px;border:1px solid var(--color-border);color:var(--color-text)}.ev-button:hover{background:var(--color-bg-hover)}.ev-primary{min-height:36px;padding:0 14px;background:var(--color-accent);color:var(--color-bg);font-weight:650}.ev-primary:disabled,.ev-button:disabled{opacity:.4;cursor:not-allowed}.ev-icon-button{width:32px;height:32px;color:var(--color-text-dim);font-size:20px}.ev-icon-button:hover{color:var(--color-text);background:var(--color-bg-hover)}
.ev-error{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:14px;padding:10px 12px;border:1px solid color-mix(in srgb,var(--color-error) 45%,transparent);border-radius:6px;background:color-mix(in srgb,var(--color-error) 10%,transparent);color:var(--color-error);font-size:12px}
.ev-workspace{display:grid;grid-template-columns:minmax(280px,360px) minmax(0,1fr);min-height:calc(100vh - 126px);border:1px solid var(--color-border);border-radius:6px;overflow:hidden}.ev-run-list{min-width:0;border-right:1px solid var(--color-border);background:var(--color-bg-card)}.ev-pane-heading{display:flex;align-items:center;justify-content:space-between;min-height:44px;padding:0 14px;border-bottom:1px solid var(--color-border);font-size:11px;font-weight:650;text-transform:uppercase;color:var(--color-text-dim)}
.ev-run-row{display:block;width:100%;padding:12px 14px;border-bottom:1px solid var(--color-border);text-align:left}.ev-run-row:hover{background:var(--color-bg-hover)}.ev-run-row[data-active=true]{background:var(--color-bg)}.ev-run-name{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px;font-weight:600}.ev-run-meta,.ev-muted{color:var(--color-text-dim);font-size:11px}.ev-run-meta{display:flex;justify-content:space-between;gap:10px;margin-top:5px}.ev-run-stat{font-variant-numeric:tabular-nums}
.ev-detail{min-width:0}.ev-detail-header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:16px 18px;border-bottom:1px solid var(--color-border)}.ev-detail-title{margin:0;font-size:16px;font-weight:650}.ev-detail-id{margin-top:4px;color:var(--color-text-dim);font-size:11px}.ev-status{font-size:11px;font-weight:700;text-transform:uppercase}.ev-status[data-tone=good]{color:var(--color-success)}.ev-status[data-tone=bad]{color:var(--color-error)}.ev-status[data-tone=active]{color:var(--color-accent)}.ev-status[data-tone=muted]{color:var(--color-text-dim)}
.ev-metrics{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));border-bottom:1px solid var(--color-border)}.ev-metric{min-width:0;padding:13px 18px;border-right:1px solid var(--color-border)}.ev-metric:last-child{border-right:0}.ev-metric-label{display:block;color:var(--color-text-dim);font-size:10px;font-weight:650;text-transform:uppercase}.ev-metric-value{display:block;margin-top:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:18px;font-weight:650;font-variant-numeric:tabular-nums}.ev-section{padding:17px 18px;border-bottom:1px solid var(--color-border)}.ev-section:last-child{border-bottom:0}.ev-section-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:9px}.ev-section-title{font-size:11px;font-weight:650;text-transform:uppercase;color:var(--color-text-dim)}
.ev-table{border:1px solid var(--color-border);border-radius:5px;overflow:hidden}.ev-target-grid{display:grid;grid-template-columns:minmax(180px,1fr) 92px 82px 100px 90px;gap:12px;align-items:center}.ev-case-grid{display:grid;grid-template-columns:minmax(180px,1.3fr) minmax(160px,1fr) 66px 70px 78px;gap:12px;align-items:center}.ev-table-head{padding:8px 11px;border-bottom:1px solid var(--color-border);background:var(--color-bg-card);color:var(--color-text-dim);font-size:10px;font-weight:650;text-transform:uppercase}.ev-table-row{width:100%;padding:9px 11px;border-bottom:1px solid var(--color-border);font-size:12px;text-align:left}.ev-table-row:last-child{border-bottom:0}.ev-table-row:hover{background:var(--color-bg-hover)}.ev-truncate{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ev-empty{display:flex;min-height:220px;align-items:center;justify-content:center;padding:32px;text-align:center;color:var(--color-text-dim);font-size:13px}.ev-empty strong{display:block;margin-bottom:6px;color:var(--color-text);font-size:14px}.ev-empty .ev-primary{margin-top:14px}
.ev-plan-list{border:1px solid var(--color-border);border-radius:6px;overflow:hidden}.ev-plan{border-bottom:1px solid var(--color-border)}.ev-plan:last-child{border-bottom:0}.ev-plan-header{display:flex;align-items:center;justify-content:space-between;gap:18px;padding:14px 16px}.ev-plan-name{font-size:14px;font-weight:650}.ev-plan-meta{margin-top:4px;color:var(--color-text-dim);font-size:11px}.ev-scenarios{border-top:1px solid var(--color-border);background:var(--color-bg-card)}.ev-scenario{display:flex;width:100%;align-items:center;justify-content:space-between;gap:16px;padding:10px 16px;border-bottom:1px solid var(--color-border);text-align:left}.ev-scenario:last-child{border-bottom:0}.ev-scenario:hover{background:var(--color-bg-hover)}.ev-scenario-name{font-size:12px;font-weight:600}.ev-scenario-prompt{max-width:760px;margin-top:2px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--color-text-dim);font-size:11px}
.ev-drawer-backdrop{position:fixed;inset:0;z-index:40;display:flex;justify-content:flex-end;background:rgba(0,0,0,.58)}.ev-drawer{display:flex;height:100%;width:min(760px,100%);flex-direction:column;border-left:1px solid var(--color-border);background:var(--color-bg);box-shadow:0 0 30px rgba(0,0,0,.35)}.ev-drawer-header{display:flex;height:56px;flex:none;align-items:center;justify-content:space-between;padding:0 20px;border-bottom:1px solid var(--color-border)}.ev-drawer-title{font-size:14px;font-weight:650}.ev-drawer-body{min-height:0;flex:1;overflow:auto;padding:20px}.ev-drawer-footer{display:flex;flex:none;justify-content:flex-end;gap:8px;padding:12px 20px;border-top:1px solid var(--color-border)}
.ev-form{display:grid;gap:18px}.ev-grid-2{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.ev-label{display:block;margin-bottom:6px;color:var(--color-text-dim);font-size:10px;font-weight:650;text-transform:uppercase}.ev-field{display:block;width:100%;min-height:38px;padding:8px 10px;border:1px solid var(--color-border);border-radius:6px;background:var(--color-bg-card);color:var(--color-text);font:inherit;font-size:13px;outline:none}.ev-field:focus{border-color:var(--color-accent)}textarea.ev-field{resize:vertical;line-height:1.45}.ev-help{display:block;margin-top:5px;color:var(--color-text-dim);font-size:11px;line-height:1.4}.ev-search{position:relative}.ev-search-menu{position:absolute;z-index:10;top:calc(100% + 4px);width:100%;max-height:230px;overflow:auto;border:1px solid var(--color-border);border-radius:6px;background:var(--color-bg);box-shadow:0 12px 28px rgba(0,0,0,.3)}.ev-search-option{display:block;width:100%;padding:9px 10px;border-bottom:1px solid var(--color-border);font-size:12px;text-align:left}.ev-search-option:last-child{border-bottom:0}.ev-search-option:hover{background:var(--color-bg-hover)}.ev-selected{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:11px 12px;border:1px solid var(--color-border);border-radius:6px;background:var(--color-bg-card)}.ev-selected-name{font-size:13px;font-weight:650}.ev-selected-meta{margin-top:2px;color:var(--color-text-dim);font-size:11px}.ev-divider{padding-top:18px;border-top:1px solid var(--color-border)}.ev-check{display:flex;align-items:center;gap:8px;font-size:12px}.ev-advanced{border-top:1px solid var(--color-border);padding-top:14px}.ev-advanced summary{cursor:pointer;color:var(--color-text-dim);font-size:12px}.ev-advanced-body{display:grid;gap:12px;margin-top:14px}
.ev-segment{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));padding:3px;border:1px solid var(--color-border);border-radius:7px;background:var(--color-bg-card)}.ev-segment button{min-height:34px;border-radius:5px;color:var(--color-text-dim);font-size:12px}.ev-segment button[data-active=true]{background:var(--color-bg);color:var(--color-text);box-shadow:0 0 0 1px var(--color-border)}
.ev-setup{padding:13px;border:1px solid var(--color-border);border-radius:6px;background:var(--color-bg-card)}.ev-setup-title{font-size:13px;font-weight:650}.ev-setup-copy{margin-top:3px;color:var(--color-text-dim);font-size:11px;line-height:1.45}.ev-summary-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px;margin-top:12px}.ev-summary-item{padding:9px;border:1px solid var(--color-border);border-radius:5px;background:var(--color-bg)}.ev-summary-value{display:block;font-size:14px;font-weight:650}.ev-summary-label{display:block;margin-top:2px;color:var(--color-text-dim);font-size:10px}
.ev-picker{display:grid;gap:8px}.ev-picker-list{max-height:190px;overflow:auto;border:1px solid var(--color-border);border-radius:5px}.ev-picker-row{display:flex;width:100%;align-items:flex-start;gap:9px;padding:8px 9px;border-bottom:1px solid var(--color-border);text-align:left}.ev-picker-row:last-child{border-bottom:0}.ev-picker-row:hover{background:var(--color-bg-hover)}.ev-picker-mark{display:flex;width:16px;height:16px;flex:none;align-items:center;justify-content:center;border:1px solid var(--color-border);border-radius:3px;font-size:10px}.ev-picker-row[data-selected=true] .ev-picker-mark{border-color:var(--color-accent);background:var(--color-accent);color:var(--color-bg)}.ev-picker-name{display:block;font-size:12px;font-weight:600}.ev-picker-detail{display:block;margin-top:2px;color:var(--color-text-dim);font-size:10px}.ev-chip-list{display:flex;flex-wrap:wrap;gap:6px}.ev-chip{display:inline-flex;align-items:center;gap:6px;max-width:100%;padding:5px 7px;border:1px solid var(--color-border);border-radius:5px;background:var(--color-bg);font-size:11px}.ev-chip span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ev-chip button{color:var(--color-text-dim)}
.ev-task-list{display:grid;gap:10px}.ev-task-card{padding:12px;border:1px solid var(--color-border);border-radius:6px;background:var(--color-bg-card)}.ev-task-header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:10px}.ev-task-number{font-size:11px;font-weight:700;text-transform:uppercase;color:var(--color-text-dim)}.ev-task-fields{display:grid;grid-template-columns:minmax(0,1.1fr) minmax(0,1fr);gap:10px}.ev-task-fields textarea{min-height:94px}.ev-seed-card{padding:10px;border:1px solid var(--color-border);border-radius:5px;background:var(--color-bg)}.ev-seed-head{display:grid;grid-template-columns:minmax(120px,.8fr) minmax(160px,1.2fr) 32px;gap:8px;align-items:center}.ev-seed-fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;margin-top:10px}.ev-seed-fields .ev-label{text-transform:none}
.ev-inspector-summary{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));border:1px solid var(--color-border);border-radius:6px;overflow:hidden}.ev-inspector-section{margin-top:20px}.ev-result-list{border:1px solid var(--color-border);border-radius:6px;overflow:hidden}.ev-result-row{display:flex;align-items:flex-start;gap:10px;padding:9px 11px;border-bottom:1px solid var(--color-border)}.ev-result-row:last-child{border-bottom:0}.ev-trace-role{margin-bottom:4px;color:var(--color-text-dim);font-size:9px;font-weight:650;text-transform:uppercase}.ev-trace-text{white-space:pre-wrap;font-size:12px;line-height:1.45}.ev-code{max-height:170px;overflow:auto;white-space:pre-wrap;padding:8px;border-radius:4px;background:var(--color-bg-card);font-family:monospace;font-size:10px}.ev-suggestion{padding:12px;border:1px solid var(--color-border);border-radius:6px}.ev-suggestion-actions{display:flex;justify-content:flex-end;margin-top:10px}
@media(max-width:900px){.ev-workspace{grid-template-columns:1fr}.ev-run-list{max-height:300px;overflow:auto;border-right:0;border-bottom:1px solid var(--color-border)}.ev-metrics{grid-template-columns:repeat(3,minmax(0,1fr))}.ev-metric:nth-child(3){border-right:0}.ev-metric:nth-child(n+4){border-top:1px solid var(--color-border)}}
@media(max-width:680px){.ev-page{padding:14px}.ev-toolbar{align-items:stretch;flex-direction:column}.ev-actions{display:grid;grid-template-columns:1fr 1fr}.ev-actions>*{width:100%}.ev-grid-2,.ev-task-fields,.ev-seed-fields{grid-template-columns:1fr}.ev-summary-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.ev-plan-header{align-items:flex-start;flex-direction:column}.ev-plan-header .ev-actions{display:flex;width:100%}.ev-metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.ev-metric:nth-child(2n){border-right:0}.ev-metric:nth-child(n+3){border-top:1px solid var(--color-border)}.ev-target-grid,.ev-case-grid{grid-template-columns:minmax(0,1fr) 78px}.ev-table-head{display:none}.ev-table-row>*:nth-child(n+3){display:none}.ev-scenario{align-items:flex-start;flex-direction:column}.ev-drawer-body{padding:16px}.ev-seed-head{grid-template-columns:1fr 32px}.ev-seed-head select:nth-child(2){grid-column:1/-1;grid-row:2}.ev-selected{align-items:flex-start;flex-direction:column}.ev-selected .ev-actions{display:flex;width:100%}}
`;

function Status({ value }: { value: string }) {
  const tone = value === "pass" || value === "completed"
    ? "good"
    : value === "fail" || value === "error"
      ? "bad"
      : value === "running" || value === "queued"
        ? "active"
        : "muted";
  const label = value === "completed" ? "Complete" : value === "queued" ? "Queued" : value;
  return <span className="ev-status" data-tone={tone}>{label}</span>;
}

function Empty({ title, detail, action }: { title: string; detail?: string; action?: ReactNode }) {
  return <div className="ev-empty"><div><strong>{title}</strong>{detail && <div>{detail}</div>}{action}</div></div>;
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div className="ev-metric"><span className="ev-metric-label">{label}</span><span className="ev-metric-value">{value}</span></div>;
}

function SearchSelect<T>({ placeholder, items, label, onSelect }: {
  placeholder: string;
  items: T[];
  label: (item: T) => string;
  onSelect: (item: T) => void;
}) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const shown = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return items.filter((item) => !normalized || label(item).toLowerCase().includes(normalized)).slice(0, 10);
  }, [query, items, label]);
  return <div className="ev-search">
    <input
      value={query}
      onChange={(event) => { setQuery(event.target.value); setOpen(true); }}
      onFocus={() => setOpen(true)}
      onBlur={() => window.setTimeout(() => setOpen(false), 120)}
      placeholder={placeholder}
      className="ev-field"
    />
    {open && shown.length > 0 && <div className="ev-search-menu">
      {shown.map((item, index) => <button
        type="button"
        key={index}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => { onSelect(item); setQuery(""); setOpen(false); }}
        className="ev-search-option"
      >{label(item)}</button>)}
    </div>}
  </div>;
}

function Drawer({ title, onClose, children, footer }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer: ReactNode;
}) {
  return <div className="ev-drawer-backdrop">
    <div className="ev-drawer">
      <header className="ev-drawer-header"><span className="ev-drawer-title">{title}</span><button type="button" onClick={onClose} title="Close" className="ev-icon-button">×</button></header>
      <div className="ev-drawer-body">{children}</div>
      <footer className="ev-drawer-footer">{footer}</footer>
    </div>
  </div>;
}

function TogglePicker<T>({ placeholder, items, selected, keyOf, titleOf, detailOf, onToggle }: {
  placeholder: string;
  items: T[];
  selected: Set<string>;
  keyOf: (item: T) => string;
  titleOf: (item: T) => string;
  detailOf: (item: T) => string;
  onToggle: (item: T) => void;
}) {
  const [query, setQuery] = useState("");
  const shown = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return items
      .filter((item) => !normalized || `${titleOf(item)} ${detailOf(item)}`.toLowerCase().includes(normalized))
      .sort((left, right) => Number(selected.has(keyOf(right))) - Number(selected.has(keyOf(left))))
      .slice(0, 20);
  }, [query, items, selected, keyOf, titleOf, detailOf]);
  return <div className="ev-picker">
    <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={placeholder} className="ev-field" />
    <div className="ev-picker-list">{shown.length === 0 ? <div className="ev-help" style={{ padding: 10 }}>No matches</div> : shown.map((item) => {
      const key = keyOf(item);
      return <button type="button" key={key} onClick={() => onToggle(item)} className="ev-picker-row" data-selected={selected.has(key)}>
        <span className="ev-picker-mark">{selected.has(key) ? "✓" : ""}</span>
        <span><span className="ev-picker-name">{titleOf(item)}</span><span className="ev-picker-detail">{detailOf(item)}</span></span>
      </button>;
    })}</div>
  </div>;
}

function SeedEditor({ app, seed, onChange, onRemove }: {
  app: CatalogApp;
  seed: SeedStep;
  onChange: (seed: SeedStep) => void;
  onRemove: () => void;
}) {
  const [tools, setTools] = useState<EnvironmentTool[]>([]);
  useEffect(() => {
    request<EnvironmentTool[]>(`/environment-tools/${app.install_id}`).then(setTools).catch(() => setTools([]));
  }, [app.install_id]);
  const tool = tools.find((item) => item.name === seed.tool);
  const properties = tool?.inputSchema?.properties || {};
  const required: string[] = tool?.inputSchema?.required || [];
  const fields = Object.keys(properties).sort((left, right) => Number(required.includes(right)) - Number(required.includes(left)) || left.localeCompare(right));
  const setField = (field: string, raw: string) => {
    const schema = properties[field] || {};
    let value: any = raw;
    if (schema.type === "number" || schema.type === "integer") value = raw === "" ? undefined : Number(raw);
    if (schema.type === "boolean") value = raw === "true";
    const input = { ...seed.input };
    if (value === undefined || value === "") delete input[field]; else input[field] = value;
    onChange({ ...seed, input });
  };
  const renderField = (field: string) => {
    const schema = properties[field] || {};
    return <label key={field}><span className="ev-label">{field}{required.includes(field) ? " *" : ""}</span>{schema.type === "boolean" ? <select value={String(seed.input[field] ?? false)} onChange={(event) => setField(field, event.target.value)} className="ev-field"><option value="false">False</option><option value="true">True</option></select> : <input value={String(seed.input[field] ?? "")} onChange={(event) => setField(field, event.target.value)} placeholder={schema.description || schema.type || "Value"} className="ev-field" />}</label>;
  };
  return <div className="ev-seed-card">
    <div className="ev-seed-head"><strong className="ev-truncate" style={{ fontSize: 12 }}>{app.display_name || app.name}</strong><select value={seed.tool} onChange={(event) => onChange({ ...seed, tool: event.target.value, input: {} })} className="ev-field"><option value="">Select seed tool</option>{tools.map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}</select><button type="button" onClick={onRemove} title="Remove seed" className="ev-icon-button">×</button></div>
    {tool && <div style={{ marginTop: 10 }}>{tool.description && <div className="ev-help" style={{ marginBottom: 8 }}>{tool.description}</div>}<div className="ev-seed-fields">{fields.filter((field) => required.includes(field)).map(renderField)}</div>{fields.some((field) => !required.includes(field)) && <details className="ev-advanced" style={{ marginTop: 10 }}><summary>Optional arguments</summary><div className="ev-seed-fields">{fields.filter((field) => !required.includes(field)).map(renderField)}</div></details>}</div>}
  </div>;
}

const newTask = (id: number): TaskDraft => ({ id, name: "", prompt: "", success: "" });

function QuickRunBuilder({ catalog, onClose, onCreated }: {
  catalog: Catalog;
  onClose: () => void;
  onCreated: (experiment: Experiment) => void;
}) {
  const [agent, setAgent] = useState<Agent | null>(null);
  const [name, setName] = useState("");
  const [tasks, setTasks] = useState<TaskDraft[]>([newTask(1)]);
  const [environmentMode, setEnvironmentMode] = useState<"clone" | "saved">("clone");
  const [savedEnvironmentID, setSavedEnvironmentID] = useState("");
  const [environmentName, setEnvironmentName] = useState("");
  const [selectedAppIDs, setSelectedAppIDs] = useState<number[]>([]);
  const [fakeSlugs, setFakeSlugs] = useState<string[]>([]);
  const [seeds, setSeeds] = useState<SeedStep[]>([]);
  const [seedAppID, setSeedAppID] = useState("");
  const [capabilitiesLoading, setCapabilitiesLoading] = useState(false);
  const [judgeModel, setJudgeModel] = useState("");
  const [maxTurns, setMaxTurns] = useState(10);
  const [timeoutSeconds, setTimeoutSeconds] = useState(600);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!judgeModel && catalog.models.length > 0) setJudgeModel(modelID(catalog.models[0]));
  }, [catalog.models, judgeModel]);

  useEffect(() => {
    if (!agent) return;
    let cancelled = false;
    setEnvironmentName(`${agent.name} evaluation environment`);
    setCapabilitiesLoading(true);
    request<AgentCapability[]>(`/agent-capabilities/${agent.id}`).then((capabilities) => {
      if (cancelled) return;
      const appIDs = capabilities.map((item) => item.install_id).filter((id) => catalog.apps.some((app) => app.install_id === id));
      const apps = catalog.apps.filter((item) => appIDs.includes(item.install_id));
      const compatible = new Set(apps.flatMap((app) => (app.integration_roles || []).flatMap((role) => role.compatible_slugs || [])));
      const available = new Set(catalog.integrations.map((item) => item.slug));
      const connected = catalog.connections.map((item) => item.app_slug).filter((slug) => available.has(slug) && compatible.has(slug));
      setSelectedAppIDs(appIDs);
      setFakeSlugs([...new Set(connected)]);
      setSeeds([]);
    }).catch((cause) => {
      if (!cancelled) setError(`Could not load the agent's project apps: ${cause.message}`);
    }).finally(() => {
      if (!cancelled) setCapabilitiesLoading(false);
    });
    return () => { cancelled = true; };
  }, [agent?.id, catalog.apps.length, catalog.connections.length, catalog.integrations.length]);

  const selectedApps = catalog.apps.filter((item) => selectedAppIDs.includes(item.install_id));
  const selectedIntegrations = catalog.integrations.filter((item) => fakeSlugs.includes(item.slug));
  const tasksValid = tasks.length > 0 && tasks.every((item) => item.prompt.trim() && goalsFromText(item.success).length > 0);
  const environmentValid = environmentMode === "clone" ? Boolean(environmentName.trim()) : Boolean(savedEnvironmentID);

  const toggleApp = (item: CatalogApp) => {
    if (selectedAppIDs.includes(item.install_id)) {
      setSelectedAppIDs(selectedAppIDs.filter((id) => id !== item.install_id));
      setSeeds(seeds.filter((seed) => seed.app !== item.name));
    } else {
      setSelectedAppIDs([...selectedAppIDs, item.install_id]);
    }
  };
  const toggleIntegration = (item: Integration) => setFakeSlugs(fakeSlugs.includes(item.slug) ? fakeSlugs.filter((slug) => slug !== item.slug) : [...fakeSlugs, item.slug]);
  const updateTask = (id: number, patch: Partial<TaskDraft>) => setTasks(tasks.map((item) => item.id === id ? { ...item, ...patch } : item));
  const removeTask = (id: number) => setTasks(tasks.filter((item) => item.id !== id));
  const addTask = () => setTasks([...tasks, newTask(Math.max(0, ...tasks.map((item) => item.id)) + 1)]);

  const integrationBindings = () => {
    const bindings: Record<string, any>[] = [];
    selectedIntegrations.forEach((integration) => {
      bindings.push({ slug: integration.slug, expose_to_agents: true, name: `Mock ${integration.name}` });
      selectedApps.forEach((app) => (app.integration_roles || []).forEach((role) => {
        if ((role.kind || "integration") !== "app" && (role.compatible_slugs || []).includes(integration.slug)) {
          bindings.push({ app: app.name, role: role.role, slug: integration.slug, name: `Mock ${integration.name}` });
        }
      }));
    });
    return bindings;
  };

  const run = async () => {
    if (!agent || !tasksValid || !environmentValid || !judgeModel) return;
    setBusy(true);
    setError("");
    let suite: Suite | null = null;
    try {
      let environmentID = savedEnvironmentID;
      if (environmentMode === "clone") {
        const created = await request<Environment>("/environments", {
          method: "POST",
          body: JSON.stringify({
            name: environmentName.trim(),
            description: `Reusable project clone created for ${agent.name} evaluations`,
            desired_state: "stopped",
            spec: {
              version: 1,
              ttl_seconds: 3600,
              app_install_ids: selectedAppIDs,
              connection_ids: [],
              network_mode: "block",
              integration_mode: "mock",
              integration_bindings: integrationBindings(),
              seeds,
            } satisfies EnvironmentSpec,
          }),
        });
        environmentID = created.id;
        setSavedEnvironmentID(created.id);
        setEnvironmentMode("saved");
      }
      const planName = name.trim() || `${agent.name} validation`;
      suite = await request<Suite>("/suites", {
        method: "POST",
        body: JSON.stringify({
          name: planName,
          description: `${tasks.length} production behavior checks for ${agent.name}`,
          environment_id: environmentID,
          judge_model: judgeModel,
          schedule_minutes: 0,
          required_pass_rate: 1,
          enabled: true,
        }),
      });
      for (const [index, task] of tasks.entries()) {
        await request<EvalCase>("/cases", {
          method: "POST",
          body: JSON.stringify({
            suite_id: suite.id,
            name: task.name.trim() || `Task ${index + 1}`,
            prompt: task.prompt.trim(),
            goals: goalsFromText(task.success),
            assertions: [],
            timeout_seconds: timeoutSeconds,
            max_turns: maxTurns,
            enabled: true,
          }),
        });
      }
      const experiment = await request<Experiment>("/experiments", {
        method: "POST",
        body: JSON.stringify({
          suite_id: suite.id,
          name: `${planName} - ${agent.name}`,
          targets: [{ agent_id: agent.id, agent_name: agent.name }],
          repetitions: 1,
          judge_model: judgeModel,
        }),
      });
      onCreated(experiment);
    } catch (cause: any) {
      if (suite) await request(`/suites/${suite.id}`, { method: "DELETE" }).catch(() => undefined);
      setError(cause.message);
    } finally {
      setBusy(false);
    }
  };

  return <Drawer
    title="Validate an agent"
    onClose={onClose}
    footer={<><button type="button" onClick={onClose} className="ev-button">Cancel</button><button type="button" disabled={busy || !agent || !tasksValid || !environmentValid || !judgeModel} onClick={run} className="ev-primary">{busy ? "Starting..." : `Run ${tasks.length} ${tasks.length === 1 ? "task" : "tasks"}`}</button></>}
  >
    <div className="ev-form">
      <label>
        <span className="ev-label">1. Production agent</span>
        {agent ? <div className="ev-selected"><div><div className="ev-selected-name">{agent.name}</div><div className="ev-selected-meta">Current production directive snapshot · {agent.status}</div></div><div className="ev-actions"><a href={`/agents/${agent.id}`} className="ev-button">Edit production directive</a><button type="button" onClick={() => setAgent(null)} className="ev-button">Change</button></div></div> : <SearchSelect placeholder="Search agents" items={catalog.agents} label={(item) => `${item.name} · ${item.status}`} onSelect={setAgent} />}
      </label>
      <section>
        <span className="ev-label">2. Environment</span>
        <div className="ev-segment"><button type="button" data-active={environmentMode === "clone"} onClick={() => setEnvironmentMode("clone")}>Clone this project</button><button type="button" data-active={environmentMode === "saved"} onClick={() => setEnvironmentMode("saved")}>Select environment</button></div>
        {environmentMode === "clone" ? <div className="ev-setup" style={{ marginTop: 10 }}>
          <div className="ev-setup-title">Reusable isolated project clone</div><div className="ev-setup-copy">Fresh app data, mocked project connections, and blocked external traffic. The environment is saved in Environments and reused by this test plan.</div>
          <label style={{ display: "block", marginTop: 12 }}><span className="ev-label">Environment name</span><input value={environmentName} onChange={(event) => setEnvironmentName(event.target.value)} placeholder="Agent evaluation environment" className="ev-field" /></label>
          <div className="ev-summary-grid"><div className="ev-summary-item"><span className="ev-summary-value">{capabilitiesLoading ? "…" : selectedApps.length}</span><span className="ev-summary-label">agent apps</span></div><div className="ev-summary-item"><span className="ev-summary-value">{selectedIntegrations.length}</span><span className="ev-summary-label">mocked integrations</span></div><div className="ev-summary-item"><span className="ev-summary-value">{seeds.length}</span><span className="ev-summary-label">seed steps</span></div></div>
          <details className="ev-advanced" style={{ marginTop: 12 }}><summary>Customize project clone</summary><div className="ev-advanced-body">
            <div><span className="ev-label">Installed apps</span><TogglePicker placeholder="Search project apps" items={catalog.apps} selected={new Set(selectedAppIDs.map(String))} keyOf={(item) => String(item.install_id)} titleOf={(item) => item.display_name || item.name} detailOf={(item) => item.description || item.name} onToggle={toggleApp} /></div>
            <div><span className="ev-label">Fake integrations</span><TogglePicker placeholder="Search integrations" items={catalog.integrations} selected={new Set(fakeSlugs)} keyOf={(item) => item.slug} titleOf={(item) => item.name} detailOf={(item) => item.description || `${item.tool_count || 0} tools`} onToggle={toggleIntegration} /></div>
            <div><span className="ev-label">Seed test data</span><select value={seedAppID} onChange={(event) => { const app = selectedApps.find((item) => String(item.install_id) === event.target.value); if (app) setSeeds([...seeds, { app: app.name, tool: "", input: {} }]); setSeedAppID(""); }} className="ev-field"><option value="">Add seed step from an app</option>{selectedApps.map((item) => <option key={item.install_id} value={item.install_id}>{item.display_name || item.name}</option>)}</select><div className="ev-task-list" style={{ marginTop: 8 }}>{seeds.map((seed, index) => { const app = catalog.apps.find((item) => item.name === seed.app); return app ? <SeedEditor key={`${seed.app}-${index}`} app={app} seed={seed} onChange={(next) => setSeeds(seeds.map((item, itemIndex) => itemIndex === index ? next : item))} onRemove={() => setSeeds(seeds.filter((_, itemIndex) => itemIndex !== index))} /> : null; })}</div></div>
          </div></details>
        </div> : <div className="ev-setup" style={{ marginTop: 10 }}><label><span className="ev-label">Saved environment</span><select value={savedEnvironmentID} onChange={(event) => setSavedEnvironmentID(event.target.value)} className="ev-field"><option value="">Choose an environment</option>{catalog.environments.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>{catalog.environments.length === 0 && <div className="ev-help">No saved environments yet. Use Clone this project to create one here.</div>}</div>}
      </section>
      <section>
        <div className="ev-section-heading"><span className="ev-label" style={{ margin: 0 }}>3. Tasks to verify</span><button type="button" onClick={addTask} className="ev-button">Add task</button></div>
        <div className="ev-task-list">{tasks.map((task, index) => <div className="ev-task-card" key={task.id}><div className="ev-task-header"><span className="ev-task-number">Task {index + 1}</span>{tasks.length > 1 && <button type="button" onClick={() => removeTask(task.id)} title="Remove task" className="ev-icon-button">×</button>}</div><label><span className="ev-label">Name</span><input value={task.name} onChange={(event) => updateTask(task.id, { name: event.target.value })} placeholder={`Task ${index + 1}`} className="ev-field" /></label><div className="ev-task-fields" style={{ marginTop: 10 }}><label><span className="ev-label">What should the agent do?</span><textarea value={task.prompt} onChange={(event) => updateTask(task.id, { prompt: event.target.value })} placeholder="Describe the task exactly as the agent should receive it." className="ev-field" /></label><label><span className="ev-label">Pass when</span><textarea value={task.success} onChange={(event) => updateTask(task.id, { success: event.target.value })} placeholder={"One requirement per line\nExpected state or outcome\nActions that must not happen"} className="ev-field" /></label></div></div>)}</div>
        <span className="ev-help">Each task runs independently in the selected environment. The evaluation model grades every success criterion using the agent trace.</span>
      </section>
      <div className="ev-grid-2">
        <label><span className="ev-label">Test plan name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder={agent ? `${agent.name} validation` : "Agent validation"} className="ev-field" /></label>
        <label><span className="ev-label">Evaluation model</span><select value={judgeModel} onChange={(event) => setJudgeModel(event.target.value)} className="ev-field"><option value="">Choose a model</option>{catalog.models.map((item) => <option key={modelID(item)} value={modelID(item)}>{modelLabel(item)}</option>)}</select></label>
      </div>
      <details className="ev-advanced"><summary>Execution limits</summary><div className="ev-advanced-body ev-grid-2"><label><span className="ev-label">Maximum turns</span><input type="number" min={1} max={100} value={maxTurns} onChange={(event) => setMaxTurns(Number(event.target.value))} className="ev-field" /></label><label><span className="ev-label">Timeout seconds</span><input type="number" min={5} max={1800} value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(Number(event.target.value))} className="ev-field" /></label></div></details>
      {error && <div className="ev-error"><span>{error}</span></div>}
    </div>
  </Drawer>;
}

function PlanRunBuilder({ suites, catalog, initialSuiteID, onClose, onCreated }: {
  suites: Suite[];
  catalog: Catalog;
  initialSuiteID?: string;
  onClose: () => void;
  onCreated: (experiment: Experiment) => void;
}) {
  const initialSuite = suites.find((item) => item.id === initialSuiteID) || suites[0];
  const [suiteID, setSuiteID] = useState(initialSuite?.id || "");
  const [targets, setTargets] = useState<Target[]>([]);
  const [agent, setAgent] = useState<Agent | null>(null);
  const [repetitions, setRepetitions] = useState(1);
  const [judgeModel, setJudgeModel] = useState(initialSuite?.judge_model || "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const addTarget = (model?: Model) => {
    if (!agent) return;
    const value = { agent_id: agent.id, agent_name: agent.name, model: model ? modelID(model) : "" };
    if (!targets.some((item) => item.agent_id === value.agent_id && item.model === value.model)) setTargets([...targets, value]);
  };
  const run = async () => {
    setBusy(true);
    try {
      const experiment = await request<Experiment>("/experiments", { method: "POST", body: JSON.stringify({ suite_id: suiteID, targets, repetitions, judge_model: judgeModel }) });
      onCreated(experiment);
    } catch (cause: any) {
      setError(cause.message);
    } finally {
      setBusy(false);
    }
  };

  return <Drawer title="Run a test plan" onClose={onClose} footer={<><button type="button" onClick={onClose} className="ev-button">Cancel</button><button type="button" disabled={busy || !suiteID || targets.length === 0} onClick={run} className="ev-primary">Run test plan</button></>}>
    <div className="ev-form">
      <div className="ev-grid-2">
        <label><span className="ev-label">Test plan</span><select value={suiteID} onChange={(event) => { setSuiteID(event.target.value); setJudgeModel(suites.find((item) => item.id === event.target.value)?.judge_model || ""); }} className="ev-field">{suites.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.cases?.length || 0} scenarios</option>)}</select></label>
        <label><span className="ev-label">Repetitions</span><input type="number" min={1} max={20} value={repetitions} onChange={(event) => setRepetitions(Number(event.target.value))} className="ev-field" /></label>
      </div>
      <section><span className="ev-label">Agents and models</span><SearchSelect placeholder="Search agents" items={catalog.agents} label={(item) => `${item.name} · ${item.status}`} onSelect={setAgent} />{agent && <div className="ev-selected" style={{ marginTop: 8 }}><div><div className="ev-selected-name">{agent.name}</div><div className="ev-selected-meta">Add its default model, or compare another model</div></div><button type="button" onClick={() => addTarget()} className="ev-button">Add default</button></div>}{agent && <div style={{ marginTop: 8 }}><SearchSelect placeholder="Search models to compare" items={catalog.models} label={modelLabel} onSelect={addTarget} /></div>}<div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 10 }}>{targets.map((item, index) => <span key={`${item.agent_id}-${item.model}-${index}`} className="ev-button">{item.agent_name} · {item.model || "default"}<button type="button" onClick={() => setTargets(targets.filter((_, targetIndex) => targetIndex !== index))} title="Remove" style={{ marginLeft: 7 }}>×</button></span>)}</div></section>
      <label><span className="ev-label">Evaluation model</span><select value={judgeModel} onChange={(event) => setJudgeModel(event.target.value)} className="ev-field"><option value="">Deterministic checks only</option>{catalog.models.map((item) => <option key={modelID(item)} value={modelID(item)}>{modelLabel(item)}</option>)}</select></label>
      {error && <div className="ev-error"><span>{error}</span></div>}
    </div>
  </Drawer>;
}

function PlanEditor({ initial, catalog, onClose, onSaved }: {
  initial?: Suite;
  catalog: Catalog;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [item, setItem] = useState<Partial<Suite>>(initial || { name: "", description: "", environment_id: "", judge_model: "", schedule_minutes: 0, required_pass_rate: 1, enabled: true, continuous_targets: [] });
  const [agent, setAgent] = useState<Agent | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const targets = item.continuous_targets || [];
  const addTarget = (model?: Model) => {
    if (!agent) return;
    const value = { agent_id: agent.id, agent_name: agent.name, model: model ? modelID(model) : "" };
    if (!targets.some((target) => target.agent_id === value.agent_id && target.model === value.model)) setItem({ ...item, continuous_targets: [...targets, value] });
  };
  const save = async () => {
    setBusy(true);
    try {
      await request(initial ? `/suites/${initial.id}` : "/suites", { method: initial ? "PUT" : "POST", body: JSON.stringify(item) });
      onSaved();
      onClose();
    } catch (cause: any) {
      setError(cause.message);
    } finally {
      setBusy(false);
    }
  };

  return <Drawer title={initial ? "Edit test plan" : "New test plan"} onClose={onClose} footer={<><button type="button" onClick={onClose} className="ev-button">Cancel</button><button type="button" disabled={busy || !item.name} onClick={save} className="ev-primary">Save test plan</button></>}>
    <div className="ev-form">
      <label><span className="ev-label">Name</span><input autoFocus value={item.name || ""} onChange={(event) => setItem({ ...item, name: event.target.value })} className="ev-field" /></label>
      <label><span className="ev-label">Description</span><textarea rows={3} value={item.description || ""} onChange={(event) => setItem({ ...item, description: event.target.value })} className="ev-field" /></label>
      <div className="ev-grid-2"><label><span className="ev-label">Environment</span><select value={item.environment_id || ""} onChange={(event) => setItem({ ...item, environment_id: event.target.value })} className="ev-field"><option value="">Fresh isolated environment</option>{catalog.environments.map((environment) => <option key={environment.id} value={environment.id}>{environment.name}</option>)}</select></label><label><span className="ev-label">Evaluation model</span><select value={item.judge_model || ""} onChange={(event) => setItem({ ...item, judge_model: event.target.value })} className="ev-field"><option value="">Deterministic checks only</option>{catalog.models.map((model) => <option key={modelID(model)} value={modelID(model)}>{modelLabel(model)}</option>)}</select></label></div>
      <div className="ev-divider"><div className="ev-grid-2"><label><span className="ev-label">Continuous schedule</span><select value={item.schedule_minutes || 0} onChange={(event) => setItem({ ...item, schedule_minutes: Number(event.target.value) })} className="ev-field"><option value={0}>Manual only</option><option value={60}>Hourly</option><option value={360}>Every 6 hours</option><option value={1440}>Daily</option><option value={10080}>Weekly</option></select></label><label><span className="ev-label">Required pass rate</span><input type="number" min={0} max={100} value={Math.round((item.required_pass_rate || 0) * 100)} onChange={(event) => setItem({ ...item, required_pass_rate: Number(event.target.value) / 100 })} className="ev-field" /></label></div>{(item.schedule_minutes || 0) > 0 && <div style={{ marginTop: 14 }}><span className="ev-label">Production targets</span><SearchSelect placeholder="Search agents" items={catalog.agents} label={(value) => value.name} onSelect={setAgent} />{agent && <div className="ev-selected" style={{ marginTop: 8 }}><div className="ev-selected-name">{agent.name}</div><button type="button" onClick={() => addTarget()} className="ev-button">Add default</button></div>}<div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 8 }}>{targets.map((target, index) => <span key={index} className="ev-button">{target.agent_name} · {target.model || "default"}<button type="button" title="Remove" onClick={() => setItem({ ...item, continuous_targets: targets.filter((_, targetIndex) => targetIndex !== index) })} style={{ marginLeft: 7 }}>×</button></span>)}</div></div>}</div>
      <label className="ev-check"><input type="checkbox" checked={item.enabled !== false} onChange={(event) => setItem({ ...item, enabled: event.target.checked })} /> Enabled</label>
      {error && <div className="ev-error"><span>{error}</span></div>}
    </div>
  </Drawer>;
}

function ScenarioEditor({ suite, initial, onClose, onSaved }: {
  suite: Suite;
  initial?: EvalCase;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [item, setItem] = useState<Partial<EvalCase>>(initial || { suite_id: suite.id, name: "", prompt: "", goals: [""], assertions: [], timeout_seconds: 600, max_turns: 10, enabled: true });
  const [assertionRaw, setAssertionRaw] = useState(JSON.stringify(initial?.assertions || [], null, 2));
  const [error, setError] = useState("");
  const save = async () => {
    try {
      let assertions: Assertion[];
      try { assertions = JSON.parse(assertionRaw); } catch { throw new Error("Deterministic checks must be valid JSON"); }
      await request(initial ? `/cases/${initial.id}` : "/cases", { method: initial ? "PUT" : "POST", body: JSON.stringify({ ...item, suite_id: suite.id, assertions }) });
      onSaved();
      onClose();
    } catch (cause: any) {
      setError(cause.message);
    }
  };

  return <Drawer title={initial ? "Edit scenario" : "New scenario"} onClose={onClose} footer={<><button type="button" onClick={onClose} className="ev-button">Cancel</button><button type="button" onClick={save} disabled={!item.name || !item.prompt} className="ev-primary">Save scenario</button></>}>
    <div className="ev-form">
      <label><span className="ev-label">Scenario name</span><input autoFocus value={item.name || ""} onChange={(event) => setItem({ ...item, name: event.target.value })} className="ev-field" /></label>
      <label><span className="ev-label">Task to perform</span><textarea rows={5} value={item.prompt || ""} onChange={(event) => setItem({ ...item, prompt: event.target.value })} className="ev-field" /></label>
      <label><span className="ev-label">Success criteria</span><textarea rows={5} value={(item.goals || []).join("\n")} onChange={(event) => setItem({ ...item, goals: event.target.value.split("\n") })} placeholder="One requirement per line" className="ev-field" /></label>
      <details className="ev-advanced"><summary>Deterministic checks and execution limits</summary><div className="ev-advanced-body"><label><span className="ev-label">Deterministic checks (JSON)</span><textarea rows={9} value={assertionRaw} onChange={(event) => setAssertionRaw(event.target.value)} spellCheck={false} className="ev-field" style={{ fontFamily: "monospace", fontSize: 11 }} /></label><div className="ev-grid-2"><label><span className="ev-label">Maximum turns</span><input type="number" min={1} max={100} value={item.max_turns || 10} onChange={(event) => setItem({ ...item, max_turns: Number(event.target.value) })} className="ev-field" /></label><label><span className="ev-label">Timeout seconds</span><input type="number" min={5} max={1800} value={item.timeout_seconds || 600} onChange={(event) => setItem({ ...item, timeout_seconds: Number(event.target.value) })} className="ev-field" /></label></div></div></details>
      <label className="ev-check"><input type="checkbox" checked={item.enabled !== false} onChange={(event) => setItem({ ...item, enabled: event.target.checked })} /> Enabled</label>
      {error && <div className="ev-error"><span>{error}</span></div>}
    </div>
  </Drawer>;
}

function RunInspector({ run, onClose }: { run: EvalRun; onClose: () => void }) {
  const trace = run.execution?.trace || [];
  const [suggestions, setSuggestions] = useState(run.suggestions || []);
  const [error, setError] = useState("");
  const apply = async (id: string) => {
    try {
      await request(`/suggestions/${id}/apply`, { method: "POST" });
      setSuggestions(suggestions.map((item) => item.id === id ? { ...item, status: "applied" } : item));
    } catch (cause: any) {
      setError(cause.message);
    }
  };
  return <Drawer title={`${run.case.name} · ${run.target.agent_name || run.target.agent_id}`} onClose={onClose} footer={<button type="button" onClick={onClose} className="ev-button">Close</button>}>
    <div className="ev-inspector-summary"><Metric label="Result" value={run.status} /><Metric label="Score" value={score(run.overall_score)} /><Metric label="Turns" value={String(run.execution?.turns || 0)} /></div>
    {(run.error || error) && <div className="ev-error" style={{ marginTop: 16 }}><span>{run.error || error}</span></div>}
    <section className="ev-inspector-section"><div className="ev-section-title" style={{ marginBottom: 8 }}>Success checks</div>{run.assertions?.length ? <div className="ev-result-list">{run.assertions.map((result: any, index) => <div className="ev-result-row" key={index}><span style={{ color: result.passed ? "var(--color-success)" : "var(--color-error)" }}>{result.passed ? "✓" : "×"}</span><div><div style={{ fontSize: 12 }}>{result.name || `Check ${index + 1}`}</div><div className="ev-muted">{result.message}</div></div></div>)}</div> : <div className="ev-muted">No deterministic checks</div>}</section>
    {run.judge && <section className="ev-inspector-section"><div className="ev-section-title" style={{ marginBottom: 8 }}>Evaluation</div><div className="ev-suggestion"><div style={{ display: "flex", justifyContent: "space-between", marginBottom: 8 }}><Status value={run.judge.passed ? "pass" : "fail"} /><strong>{score(run.judge.score)}</strong></div><div style={{ fontSize: 12, lineHeight: 1.5 }}>{run.judge.reasoning}</div></div></section>}
    {suggestions.map((item) => <section className="ev-inspector-section" key={item.id}><div className="ev-section-title" style={{ marginBottom: 8 }}>Production directive change</div><div className="ev-suggestion"><div className="ev-muted" style={{ marginBottom: 8 }}>{item.reason}</div><pre className="ev-code">{item.directive}</pre><div className="ev-suggestion-actions"><button type="button" disabled={item.status !== "proposed"} onClick={() => apply(item.id)} className="ev-primary">{item.status === "applied" ? "Applied to production" : "Apply to production"}</button></div></div></section>)}
    <section className="ev-inspector-section"><div className="ev-section-title" style={{ marginBottom: 8 }}>Agent trace</div><div className="ev-result-list">{trace.length === 0 ? <Empty title="No trace recorded" /> : trace.map((event) => <div className="ev-result-row" key={event.index}><div style={{ width: "100%" }}><div className="ev-trace-role">{event.role}</div>{event.content && <div className="ev-trace-text">{event.content}</div>}{event.tool_call && <div><code style={{ color: "var(--color-accent)", fontSize: 11 }}>{event.tool_call.name}</code>{event.tool_call.input && <pre className="ev-code">{typeof event.tool_call.input === "string" ? event.tool_call.input : JSON.stringify(event.tool_call.input, null, 2)}</pre>}{event.tool_call.output && <pre className="ev-code" style={event.tool_call.is_error ? { color: "var(--color-error)" } : undefined}>{event.tool_call.output}</pre>}</div>}</div></div>)}</div></section>
  </Drawer>;
}

function RunDetail({ experiment, onInspect }: { experiment: Experiment; onInspect: (run: EvalRun) => void }) {
  const summary = experiment.summary;
  const runs = experiment.runs || [];
  return <div className="ev-detail">
    <header className="ev-detail-header"><div><h2 className="ev-detail-title">{experiment.name}</h2><div className="ev-detail-id">{experiment.trigger_type === "schedule" ? "Continuous check" : "Manual run"} · {formatDate(experiment.created_at)}</div></div><Status value={experiment.status} /></header>
    <div className="ev-metrics"><Metric label="Pass rate" value={pct(summary?.pass_rate)} /><Metric label="Score" value={score(summary?.average_score)} /><Metric label="Passed" value={String(summary?.passed || 0)} /><Metric label="Failed" value={String((summary?.failed || 0) + (summary?.errors || 0))} /><Metric label="In progress" value={String((summary?.queued || 0) + (summary?.running || 0))} /></div>
    {summary?.targets?.length ? <section className="ev-section"><div className="ev-section-heading"><span className="ev-section-title">Targets</span></div><div className="ev-table"><div className="ev-table-head ev-target-grid"><span>Agent and model</span><span>Pass rate</span><span>Score</span><span>Tokens</span><span>Cost</span></div>{summary.targets.map((target) => <div className="ev-table-row ev-target-grid" key={target.target_index}><span className="ev-truncate"><strong>{target.target.agent_name}</strong> · {target.target.model || "default"}</span><span>{pct(target.pass_rate)}</span><span>{score(target.average_score)}</span><span>{Math.round(target.average_tokens).toLocaleString()}</span><span>${target.average_cost_usd.toFixed(4)}</span></div>)}</div></section> : null}
    <section className="ev-section"><div className="ev-section-heading"><span className="ev-section-title">Scenarios</span><span className="ev-muted">{runs.length} runs</span></div><div className="ev-table"><div className="ev-table-head ev-case-grid"><span>Scenario</span><span>Agent and model</span><span>Repeat</span><span>Score</span><span>Result</span></div>{runs.map((run) => <button type="button" key={run.id} onClick={() => onInspect(run)} className="ev-table-row ev-case-grid"><span className="ev-truncate">{run.case.name}</span><span className="ev-truncate">{run.target.agent_name} · {run.target.model || "default"}</span><span>{run.repetition}</span><span>{score(run.overall_score)}</span><Status value={run.status} /></button>)}</div></section>
  </div>;
}

function RunsView({ experiments, selected, detail, onSelect, onInspect, onQuickRun }: {
  experiments: Experiment[];
  selected: string;
  detail: Experiment | null;
  onSelect: (id: string) => void;
  onInspect: (run: EvalRun) => void;
  onQuickRun: () => void;
}) {
  if (experiments.length === 0) return <div className="ev-plan-list"><Empty title="No test runs yet" detail="Start with one production agent and one expected behavior." action={<button type="button" onClick={onQuickRun} className="ev-primary">Test an agent</button>} /></div>;
  return <div className="ev-workspace">
    <aside className="ev-run-list"><div className="ev-pane-heading"><span>Recent runs</span><span>{experiments.length}</span></div>{experiments.map((experiment) => <button type="button" key={experiment.id} onClick={() => onSelect(experiment.id)} className="ev-run-row" data-active={selected === experiment.id}><span className="ev-run-name">{experiment.name}</span><span className="ev-run-meta"><Status value={experiment.status} /><span className="ev-run-stat">{pct(experiment.summary?.pass_rate)} · {formatDate(experiment.created_at)}</span></span></button>)}</aside>
    {detail ? <RunDetail experiment={detail} onInspect={onInspect} /> : <Empty title="Select a run" />}
  </div>;
}

function PlansView({ suites, catalog, onNew, onEdit, onScenario, onRun }: {
  suites: Suite[];
  catalog: Catalog;
  onNew: () => void;
  onEdit: (suite: Suite) => void;
  onScenario: (suite: Suite, item?: EvalCase) => void;
  onRun: (suite: Suite) => void;
}) {
  if (suites.length === 0) return <div className="ev-plan-list"><Empty title="No test plans yet" detail="A test plan groups reusable scenarios and success criteria." action={<button type="button" onClick={onNew} className="ev-primary">New test plan</button>} /></div>;
  return <div className="ev-plan-list">{suites.map((suite) => <section className="ev-plan" key={suite.id}><header className="ev-plan-header"><div><div className="ev-plan-name">{suite.name}</div><div className="ev-plan-meta">{suite.cases.length} scenarios · {catalog.environments.find((item) => item.id === suite.environment_id)?.name || "fresh environment"}{suite.schedule_minutes > 0 ? ` · every ${suite.schedule_minutes} minutes` : ""}</div></div><div className="ev-actions"><button type="button" onClick={() => onScenario(suite)} className="ev-button">Add scenario</button><button type="button" onClick={() => onEdit(suite)} className="ev-button">Settings</button><button type="button" onClick={() => onRun(suite)} className="ev-primary">Run</button></div></header>{suite.cases.length > 0 && <div className="ev-scenarios">{suite.cases.map((item) => <button type="button" key={item.id} onClick={() => onScenario(suite, item)} className="ev-scenario"><span style={{ minWidth: 0 }}><span className="ev-scenario-name">{item.name}</span><span className="ev-scenario-prompt">{item.prompt}</span></span><span className="ev-muted">{item.goals.length} criteria · {item.assertions.length} checks</span></button>)}</div>}</section>)}</div>;
}

export default function EvalsPanel(props: NativePanelProps) {
  installID = props.installId;
  projectID = props.projectId;
  const [tab, setTab] = useState<"runs" | "plans">("runs");
  const [suites, setSuites] = useState<Suite[]>([]);
  const [experiments, setExperiments] = useState<Experiment[]>([]);
  const [catalog, setCatalog] = useState<Catalog>({ agents: [], models: [], environments: [], apps: [], connections: [], integrations: [], snapshots: [] });
  const [selected, setSelected] = useState("");
  const [detail, setDetail] = useState<Experiment | null>(null);
  const [quickRun, setQuickRun] = useState(false);
  const [planRun, setPlanRun] = useState<string | null>(null);
  const [planEditor, setPlanEditor] = useState<Suite | true | null>(null);
  const [scenarioEditor, setScenarioEditor] = useState<{ suite: Suite; item?: EvalCase } | null>(null);
  const [inspected, setInspected] = useState<EvalRun | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [suiteRows, experimentRows, catalogValue] = await Promise.all([
        request<Suite[]>("/suites"),
        request<Experiment[]>("/experiments"),
        request<Catalog>("/catalog"),
      ]);
      setSuites(suiteRows);
      setExperiments(experimentRows);
      setCatalog(catalogValue);
      setSelected((current) => current || experimentRows[0]?.id || "");
      setError("");
    } catch (cause: any) {
      setError(cause.message);
    }
  }, []);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  const selectedSummary = experiments.find((item) => item.id === selected);
  useEffect(() => {
    if (!selected) { setDetail(null); return; }
    request<Experiment>(`/experiments/${selected}`).then(setDetail).catch((cause) => setError(cause.message));
  }, [selected, selectedSummary?.status, selectedSummary?.summary?.queued, selectedSummary?.summary?.running]);

  const created = (experiment: Experiment) => {
    setQuickRun(false);
    setPlanRun(null);
    setTab("runs");
    setSelected(experiment.id);
    load();
  };

  return <div className="ev-shell">
    <style>{styles}</style>
    <div className="ev-page">
      <header className="ev-toolbar">
        <div><h1 className="ev-title">Evals</h1><nav className="ev-tabs"><button type="button" onClick={() => setTab("runs")} className="ev-tab" data-active={tab === "runs"}>Runs</button><button type="button" onClick={() => setTab("plans")} className="ev-tab" data-active={tab === "plans"}>Test plans</button></nav></div>
        <div className="ev-actions">{tab === "runs" && suites.length > 0 && <button type="button" onClick={() => setPlanRun(suites[0].id)} className="ev-button">Run test plan</button>}<button type="button" onClick={() => tab === "runs" ? setQuickRun(true) : setPlanEditor(true)} className="ev-primary">{tab === "runs" ? "Test an agent" : "New test plan"}</button></div>
      </header>
      {error && <div className="ev-error"><span>{error}</span><button type="button" onClick={() => setError("")} title="Dismiss" className="ev-icon-button">×</button></div>}
      {tab === "runs" ? <RunsView experiments={experiments} selected={selected} detail={detail} onSelect={setSelected} onInspect={setInspected} onQuickRun={() => setQuickRun(true)} /> : <PlansView suites={suites} catalog={catalog} onNew={() => setPlanEditor(true)} onEdit={setPlanEditor} onScenario={(suite, item) => setScenarioEditor({ suite, item })} onRun={(suite) => { setTab("runs"); setPlanRun(suite.id); }} />}
    </div>
    {quickRun && <QuickRunBuilder catalog={catalog} onClose={() => setQuickRun(false)} onCreated={created} />}
    {planRun !== null && <PlanRunBuilder initialSuiteID={planRun} suites={suites} catalog={catalog} onClose={() => setPlanRun(null)} onCreated={created} />}
    {planEditor && <PlanEditor initial={planEditor === true ? undefined : planEditor} catalog={catalog} onClose={() => setPlanEditor(null)} onSaved={load} />}
    {scenarioEditor && <ScenarioEditor suite={scenarioEditor.suite} initial={scenarioEditor.item} onClose={() => setScenarioEditor(null)} onSaved={load} />}
    {inspected && <RunInspector run={inspected} onClose={() => setInspected(null)} />}
  </div>;
}
