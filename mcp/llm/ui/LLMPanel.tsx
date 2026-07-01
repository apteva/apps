import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface ProviderConfig {
  id?: number;
  project_id?: string;
  provider: string;
  base_url: string;
  auth_mode: string;
  connection_id?: number;
  key_ref?: string;
  enabled: boolean;
  priority: number;
  source?: string;
}

interface Policy {
  project_id: string;
  allowed_models?: string[];
  blocked_models?: string[];
  allowed_providers?: string[];
  limits?: {
    monthly_request_limit?: number;
    monthly_input_token_limit?: number;
    monthly_output_token_limit?: number;
    monthly_spend_cap_cents?: number;
    max_tokens_per_request?: number;
  };
}

interface UsageSummary {
  project_id: string;
  period: string;
  requests: number;
  request_tokens: number;
  response_tokens: number;
  total_tokens: number;
  estimated_cost_cents: number;
}

interface ProviderModel {
  id?: number;
  project_id: string;
  provider: string;
  model_id: string;
  display_name?: string;
  gateway_model: string;
  status: string;
  last_seen_at?: string;
}

type Tab = "test" | "providers" | "policy" | "tokens" | "usage";

const API = "/api/apps/llm";
const providerChoices = ["openai", "anthropic", "fireworks", "openrouter"];
const defaultProvider: ProviderConfig = {
  provider: "openai",
  base_url: "",
  auth_mode: "platform_shared",
  key_ref: "openai_api_key",
  enabled: true,
  priority: 100,
};

export default function LLMPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("test");
  const [providers, setProviders] = useState<ProviderConfig[]>([]);
  const [models, setModels] = useState<ProviderModel[]>([]);
  const [providerDraft, setProviderDraft] = useState<ProviderConfig>(defaultProvider);
  const [policy, setPolicy] = useState<Policy>({ project_id: projectId, limits: {} });
  const [usage, setUsage] = useState<UsageSummary | null>(null);
  const [token, setToken] = useState("");
  const [tokenSubjectType, setTokenSubjectType] = useState("agent");
  const [tokenSubjectId, setTokenSubjectId] = useState("manual-test");
  const [selectedProvider, setSelectedProvider] = useState("");
  const [modelId, setModelId] = useState("");
  const [prompt, setPrompt] = useState("Reply with one short sentence confirming the gateway works.");
  const [response, setResponse] = useState<any>(null);
  const [status, setStatus] = useState("");
  const [syncStatus, setSyncStatus] = useState("");
  const [autoSyncedProviders, setAutoSyncedProviders] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const sep = path.includes("?") ? "&" : "?";
    const res = await fetch(`${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
      ...init,
    });
    const text = await res.text();
    const data = text ? JSON.parse(text) : null;
    if (!res.ok) throw new Error((data as any)?.error?.message || (data as any)?.error || res.statusText);
    return data as T;
  }, [projectId]);

  const load = useCallback(async () => {
    try {
      const [providerData, policyData, usageData] = await Promise.all([
        api<{ providers: ProviderConfig[] }>("/providers"),
        api<{ policy: Policy }>("/policy"),
        api<UsageSummary>("/usage"),
      ]);
      const modelData = await api<{ models: ProviderModel[] }>("/models");
      const nextProviders = providerData.providers || [];
      setProviders(nextProviders);
      setModels(modelData.models || []);
      setSelectedProvider((current) => {
        if (nextProviders.some((p) => p.enabled && p.provider === current)) return current;
        return nextProviders.find((p) => p.enabled)?.provider || current;
      });
      setPolicy(policyData.policy || { project_id: projectId, limits: {} });
      setUsage(usageData);
      setStatus("");
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [api, projectId]);

  useEffect(() => { load(); }, [load]);

  const activeProviders = useMemo(() => (
    providers.filter((p) => p.enabled)
  ), [providers]);

  const modelSuggestions = useMemo(() => {
    const discovered = models
      .filter((m) => m.provider === selectedProvider && m.status === "active")
      .map((m) => m.model_id);
    if (discovered.length > 0) return discovered;
    const prefix = `${selectedProvider}/`;
    return (policy.allowed_models || [])
      .filter((m) => m.startsWith(prefix))
      .map((m) => m.slice(prefix.length))
      .filter((m) => m && m !== "*");
  }, [models, policy.allowed_models, selectedProvider]);

  useEffect(() => {
    if (!selectedProvider) return;
    if (models.some((m) => m.provider === selectedProvider && m.status === "active")) return;
    if (autoSyncedProviders.includes(selectedProvider)) return;
    const provider = activeProviders.find((p) => p.provider === selectedProvider);
    if (!provider) return;
    void syncModels(selectedProvider, true);
  }, [activeProviders, autoSyncedProviders, models, selectedProvider]);

  const saveProvider = async () => {
    setBusy(true);
    setStatus("");
    try {
      const payload = { ...providerDraft, project_id: projectId };
      const out = await api<{ provider: ProviderConfig }>("/providers", {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      setProviderDraft(out.provider);
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const savePolicy = async () => {
    setBusy(true);
    setStatus("");
    try {
      const payload = {
        project_id: projectId,
        allowed_models: splitLines(textFromList(policy.allowed_models)),
        blocked_models: splitLines(textFromList(policy.blocked_models)),
        allowed_providers: splitLines(textFromList(policy.allowed_providers)),
        limits: policy.limits || {},
      };
      const out = await api<{ policy: Policy }>("/policy", {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      setPolicy(out.policy);
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const issueToken = async () => {
    const out = await api<{ token: string }>("/tokens", {
      method: "POST",
      body: JSON.stringify({
        project_id: projectId,
        subject_type: tokenSubjectType,
        subject_id: tokenSubjectId,
        scopes: ["chat", "models", "usage"],
      }),
    });
    setToken(out.token);
    return out.token;
  };

  const createToken = async () => {
    setBusy(true);
    setStatus("");
    try {
      await issueToken();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const syncModels = async (provider = selectedProvider, quiet = false) => {
    if (!provider) return;
    if (quiet) {
      setAutoSyncedProviders((current) => current.includes(provider) ? current : [...current, provider]);
    }
    setBusy(true);
    if (!quiet) {
      setSyncStatus("");
      setStatus("");
    }
    try {
      const out = await api<{ results: Array<{ provider: string; status: string; model_count: number; error?: string }> }>("/models/sync", {
        method: "POST",
        body: JSON.stringify({ project_id: projectId, provider }),
      });
      const result = out.results?.find((r) => r.provider === provider) || out.results?.[0];
      if (result?.status === "error") {
        setSyncStatus(`${provider}: ${result.error || "sync failed"}`);
      } else {
        setSyncStatus(`${provider}: ${result?.model_count || 0} models synced`);
      }
      const modelData = await api<{ models: ProviderModel[] }>("/models");
      setModels(modelData.models || []);
    } catch (e) {
      setSyncStatus((e as Error).message);
      if (!quiet) setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const runTest = async () => {
    setBusy(true);
    setStatus("");
    setResponse(null);
    try {
      const runToken = token || await issueToken();
      const gatewayModel = modelForRequest(selectedProvider, modelId);
      const res = await fetch(`${API}/v1/chat/completions?project_id=${encodeURIComponent(projectId)}`, {
        method: "POST",
        credentials: "include",
        headers: {
          "Content-Type": "application/json",
          "Authorization": `Bearer ${runToken}`,
        },
        body: JSON.stringify({
          model: gatewayModel,
          messages: [{ role: "user", content: prompt }],
          max_tokens: 256,
        }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data?.error?.message || res.statusText);
      setResponse(data);
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const editProvider = (p: ProviderConfig) => {
    setProviderDraft({ ...p });
    setTab("providers");
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="flex items-center gap-2 border-b border-border px-4 py-2">
        <div className="font-medium">LLM Gateway</div>
        <nav className="flex items-center gap-1">
          <TabButton active={tab === "test"} onClick={() => setTab("test")}>Test</TabButton>
          <TabButton active={tab === "providers"} onClick={() => setTab("providers")}>Providers</TabButton>
          <TabButton active={tab === "policy"} onClick={() => setTab("policy")}>Policy</TabButton>
          <TabButton active={tab === "tokens"} onClick={() => setTab("tokens")}>Tokens</TabButton>
          <TabButton active={tab === "usage"} onClick={() => setTab("usage")}>Usage</TabButton>
        </nav>
        {status && <div className="ml-auto text-xs text-red max-w-[48rem] truncate">{status}</div>}
      </header>

      {tab === "test" && (
        <div className="flex-1 min-h-0 grid grid-cols-[minmax(380px,520px)_1fr]">
          <main className="overflow-auto p-4 border-r border-border space-y-3">
            <Field label="Provider">
              <select
                value={selectedProvider}
                onChange={(e) => setSelectedProvider(e.target.value)}
                disabled={activeProviders.length === 0}
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                {activeProviders.length === 0 && <option value="">No enabled providers</option>}
                {activeProviders.map((p) => (
                  <option key={`${p.provider}:${p.connection_id || p.id || p.source || ""}`} value={p.provider}>
                    {p.provider}{p.source === "bound_integration" ? " (bound integration)" : ""}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Model ID">
              <input
                value={modelId}
                onChange={(e) => setModelId(e.target.value)}
                list="llm-models"
                placeholder={selectedProvider ? `${selectedProvider} model id` : "Select a provider first"}
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono"
              />
              <datalist id="llm-models">
                {modelSuggestions.map((m) => <option key={m} value={m} />)}
              </datalist>
            </Field>
            <div className="flex items-center gap-2">
              <button type="button" onClick={() => syncModels()} disabled={busy || !selectedProvider} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
                Sync models
              </button>
              {syncStatus && <span className="text-xs text-text-dim truncate">{syncStatus}</span>}
            </div>
            <Field label="Prompt">
              <textarea
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                className="w-full min-h-[180px] bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
            </Field>
            <div className="flex items-center gap-2">
              <button type="button" disabled={busy || !selectedProvider || !modelId || !prompt} onClick={runTest} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">
                {busy ? "Running" : "Run"}
              </button>
              <button type="button" onClick={createToken} disabled={busy} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
                New token
              </button>
            </div>
          </main>
          <section className="overflow-auto p-4">
            <h2 className="text-sm font-medium mb-2">Response</h2>
            {response ? <pre className="bg-bg-input border border-border rounded p-3 text-xs overflow-auto">{pretty(response)}</pre> : <div className="text-sm text-text-dim">No response</div>}
          </section>
        </div>
      )}

      {tab === "providers" && (
        <div className="flex-1 min-h-0 grid grid-cols-[360px_1fr]">
          <aside className="border-r border-border overflow-auto">
            <div className="p-2 border-b border-border">
              <button type="button" onClick={() => setProviderDraft(defaultProvider)} className="w-full text-sm px-2 py-1.5 border border-border rounded hover:bg-bg-input">New provider</button>
            </div>
            {providers.map((p) => (
              <button key={`${p.project_id}:${p.provider}:${p.id}`} type="button" onClick={() => editProvider(p)} className="w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input">
                <div className="flex items-center gap-2">
                  <span className={`h-2 w-2 rounded-full ${p.enabled ? "bg-success" : "bg-text-dim"}`} />
                  <span className="text-sm font-medium">{p.provider}</span>
                  <span className="ml-auto text-xs text-text-dim">{p.auth_mode}</span>
                </div>
                <div className="mt-1 text-xs text-text-dim truncate">{p.base_url || "default base URL"}</div>
                <div className="mt-1 text-xs text-text-dim">{models.filter((m) => m.provider === p.provider && m.status === "active").length} models</div>
              </button>
            ))}
          </aside>
          <main className="overflow-auto p-4 max-w-3xl space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <Field label="Provider">
                <select value={providerDraft.provider} onChange={(e) => setProviderDraft({ ...providerDraft, provider: e.target.value, key_ref: `${e.target.value}_api_key` })} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                  {providerChoices.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </Field>
              <Field label="Auth mode">
                <select value={providerDraft.auth_mode} onChange={(e) => setProviderDraft({ ...providerDraft, auth_mode: e.target.value })} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                  <option value="platform_shared">platform_shared</option>
                  <option value="customer_owned">customer_owned</option>
                  <option value="provider_aggregator">provider_aggregator</option>
                </select>
              </Field>
            </div>
            <Field label="Base URL">
              <input value={providerDraft.base_url || ""} onChange={(e) => setProviderDraft({ ...providerDraft, base_url: e.target.value })} placeholder="default" className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono" />
            </Field>
            <div className="grid grid-cols-3 gap-3">
              <Field label="Key ref">
                <input value={providerDraft.key_ref || ""} onChange={(e) => setProviderDraft({ ...providerDraft, key_ref: e.target.value })} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono" />
              </Field>
              <Field label="Connection ID">
                <input value={providerDraft.connection_id || ""} onChange={(e) => setProviderDraft({ ...providerDraft, connection_id: Number(e.target.value) || 0 })} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              </Field>
              <Field label="Priority">
                <input type="number" value={providerDraft.priority} onChange={(e) => setProviderDraft({ ...providerDraft, priority: Number(e.target.value) || 100 })} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              </Field>
            </div>
            <label className="flex items-center gap-2 text-sm text-text-muted">
              <input type="checkbox" checked={providerDraft.enabled} onChange={(e) => setProviderDraft({ ...providerDraft, enabled: e.target.checked })} />
              Enabled
            </label>
            <button type="button" disabled={busy || !providerDraft.provider} onClick={saveProvider} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">Save provider</button>
          </main>
        </div>
      )}

      {tab === "policy" && (
        <div className="flex-1 overflow-auto p-4 max-w-5xl space-y-4">
          <div className="grid grid-cols-3 gap-3">
            <TextList label="Allowed models" value={textFromList(policy.allowed_models)} onChange={(value) => setPolicy({ ...policy, allowed_models: splitLines(value) })} />
            <TextList label="Blocked models" value={textFromList(policy.blocked_models)} onChange={(value) => setPolicy({ ...policy, blocked_models: splitLines(value) })} />
            <TextList label="Allowed providers" value={textFromList(policy.allowed_providers)} onChange={(value) => setPolicy({ ...policy, allowed_providers: splitLines(value) })} />
          </div>
          <div className="grid grid-cols-5 gap-3">
            <NumberField label="Requests / month" value={policy.limits?.monthly_request_limit} onChange={(v) => setLimit(policy, setPolicy, "monthly_request_limit", v)} />
            <NumberField label="Input tokens / month" value={policy.limits?.monthly_input_token_limit} onChange={(v) => setLimit(policy, setPolicy, "monthly_input_token_limit", v)} />
            <NumberField label="Output tokens / month" value={policy.limits?.monthly_output_token_limit} onChange={(v) => setLimit(policy, setPolicy, "monthly_output_token_limit", v)} />
            <NumberField label="Spend cap cents" value={policy.limits?.monthly_spend_cap_cents} onChange={(v) => setLimit(policy, setPolicy, "monthly_spend_cap_cents", v)} />
            <NumberField label="Max tokens" value={policy.limits?.max_tokens_per_request} onChange={(v) => setLimit(policy, setPolicy, "max_tokens_per_request", v)} />
          </div>
          <button type="button" disabled={busy} onClick={savePolicy} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">Save policy</button>
        </div>
      )}

      {tab === "tokens" && (
        <div className="flex-1 overflow-auto p-4 max-w-3xl space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Field label="Subject type">
              <select value={tokenSubjectType} onChange={(e) => setTokenSubjectType(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                <option value="agent">agent</option>
                <option value="app">app</option>
                <option value="project">project</option>
                <option value="system">system</option>
              </select>
            </Field>
            <Field label="Subject ID">
              <input value={tokenSubjectId} onChange={(e) => setTokenSubjectId(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono" />
            </Field>
          </div>
          <button type="button" disabled={busy || !tokenSubjectId} onClick={createToken} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">Create token</button>
          {token && (
            <pre className="bg-bg-input border border-border rounded p-3 text-xs overflow-auto select-all">{token}</pre>
          )}
        </div>
      )}

      {tab === "usage" && (
        <div className="flex-1 overflow-auto p-4 max-w-4xl">
          <button type="button" onClick={load} className="mb-3 px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Refresh</button>
          {usage ? (
            <div className="grid grid-cols-5 gap-3">
              <Metric label="Requests" value={usage.requests} />
              <Metric label="Input tokens" value={usage.request_tokens} />
              <Metric label="Output tokens" value={usage.response_tokens} />
              <Metric label="Total tokens" value={usage.total_tokens} />
              <Metric label="Spend cents" value={usage.estimated_cost_cents} />
            </div>
          ) : (
            <div className="text-sm text-text-dim">No usage</div>
          )}
        </div>
      )}
    </div>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className={`px-2 py-1 text-sm border rounded ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:bg-bg-input"}`}>{children}</button>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><div className="text-sm text-text-muted mb-1">{label}</div>{children}</label>;
}

function TextList({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <Field label={label}><textarea value={value} onChange={(e) => onChange(e.target.value)} spellCheck={false} className="w-full min-h-[160px] bg-bg-input border border-border rounded px-2 py-1.5 text-xs font-mono" /></Field>;
}

function NumberField({ label, value, onChange }: { label: string; value?: number; onChange: (value: number) => void }) {
  return <Field label={label}><input type="number" value={value || ""} onChange={(e) => onChange(Number(e.target.value) || 0)} className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm" /></Field>;
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div className="border border-border rounded p-3 bg-bg-card"><div className="text-xs text-text-muted">{label}</div><div className="mt-1 text-xl font-semibold tabular-nums">{value}</div></div>;
}

function setLimit(policy: Policy, setPolicy: (p: Policy) => void, key: keyof NonNullable<Policy["limits"]>, value: number) {
  setPolicy({ ...policy, limits: { ...(policy.limits || {}), [key]: value } });
}

function splitLines(value: string): string[] {
  return value.split(/\r?\n/).map((v) => v.trim()).filter(Boolean);
}

function textFromList(values?: string[]): string {
  return (values || []).join("\n");
}

function pretty(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

function modelForRequest(provider: string, modelId: string): string {
  const cleanProvider = provider.trim();
  const cleanModel = modelId.trim();
  if (!cleanProvider || !cleanModel) return cleanModel;
  if (cleanModel.startsWith(`${cleanProvider}/`)) return cleanModel;
  return `${cleanProvider}/${cleanModel}`;
}
