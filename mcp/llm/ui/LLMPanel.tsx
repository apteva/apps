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

interface Limits {
  monthly_request_limit?: number;
  monthly_input_token_limit?: number;
  monthly_output_token_limit?: number;
  max_tokens_per_request?: number;
  monthly_provider_cost_limit_microunits?: number;
  provider_cost_currency?: string;
  unpriced_model_behavior?: "allow" | "deny";
}

interface FallbackRoute {
  provider: string;
  model: string;
}

interface Policy {
  project_id: string;
  subject_type?: string;
  subject_id?: string;
  allowed_models?: string[];
  blocked_models?: string[];
  allowed_providers?: string[];
  limits?: Limits;
  disabled?: boolean;
  fallback_policy?: { routes?: FallbackRoute[] };
}

interface UsageSummary {
  project_id: string;
  period: string;
  requests: number;
  request_tokens: number;
  response_tokens: number;
  total_tokens: number;
  provider_cost_microunits: number;
  provider_cost_currency: string;
  reported_cost_requests: number;
  calculated_cost_requests: number;
  partial_cost_requests: number;
  unpriced_requests: number;
}

interface UsageEvent {
  id: number;
  subject_type: string;
  subject_id: string;
  provider: string;
  model: string;
  request_tokens: number;
  response_tokens: number;
  total_tokens: number;
  provider_cost_microunits: number;
  provider_cost_currency: string;
  provider_cost_status: string;
  provider_cost_source?: string;
  provider_rate_id?: number;
  status: string;
  request_id: string;
  created_at: string;
}

interface TokenRecord {
  id: number;
  subject_type: string;
  subject_id: string;
  scopes: string[];
  expires_at?: string;
  created_at: string;
  revoked_at?: string;
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

interface ProviderRate {
  id?: number;
  project_id: string;
  provider: string;
  model_id: string;
  currency: string;
  input_microunits_per_million: number;
  output_microunits_per_million: number;
  cached_input_microunits_per_million: number;
  cache_write_microunits_per_million: number;
  request_microunits: number;
  source: string;
  source_reference?: string;
  extra_rates?: {
    catalog_version?: string;
    verified_at?: string;
    cache_write_1h_microunits_per_million?: number;
    standard_context_max_tokens?: number;
  };
  effective_from?: string;
  effective_to?: string;
}

interface AuditLog {
  id: number;
  subject_type: string;
  subject_id: string;
  action: string;
  provider: string;
  model: string;
  status: string;
  message: string;
  created_at: string;
}

type Tab = "test" | "providers" | "rates" | "policy" | "tokens" | "usage" | "audit";

const API = "/api/apps/llm";
const knownProviders = ["openai", "anthropic", "opencode-go", "fireworks", "openrouter"];
const providerBaseURLs: Record<string, string> = {
  openai: "https://api.openai.com/v1",
  anthropic: "https://api.anthropic.com/v1",
  fireworks: "https://api.fireworks.ai/inference/v1",
  openrouter: "https://openrouter.ai/api/v1",
  "opencode-go": "https://opencode.ai/zen/go/v1",
};
const providerKeyRefs: Record<string, string> = { "opencode-go": "opencode_go_api_key" };
const defaultProvider: ProviderConfig = {
  provider: "openai",
  base_url: "https://api.openai.com/v1",
  auth_mode: "platform_shared",
  key_ref: "openai_api_key",
  enabled: true,
  priority: 100,
};
const defaultRate: ProviderRate = {
  project_id: "",
  provider: "",
  model_id: "",
  currency: "USD",
  input_microunits_per_million: 0,
  output_microunits_per_million: 0,
  cached_input_microunits_per_million: 0,
  cache_write_microunits_per_million: 0,
  request_microunits: 0,
  source: "manual",
};

export default function LLMPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("test");
  const [providers, setProviders] = useState<ProviderConfig[]>([]);
  const [models, setModels] = useState<ProviderModel[]>([]);
  const [providerRates, setProviderRates] = useState<ProviderRate[]>([]);
  const [rateDraft, setRateDraft] = useState<ProviderRate>({ ...defaultRate, project_id: projectId });
  const [providerDraft, setProviderDraft] = useState<ProviderConfig>({ ...defaultProvider });
  const [policy, setPolicy] = useState<Policy>({ project_id: projectId, limits: {}, fallback_policy: { routes: [] } });
  const [policyScope, setPolicyScope] = useState<"project" | "subject">("project");
  const [policySubjectType, setPolicySubjectType] = useState("tenant");
  const [policySubjectId, setPolicySubjectId] = useState("");
  const [usage, setUsage] = useState<UsageSummary | null>(null);
  const [usageEvents, setUsageEvents] = useState<UsageEvent[]>([]);
  const [usageSubjectType, setUsageSubjectType] = useState("");
  const [usageSubjectId, setUsageSubjectId] = useState("");
  const [usageModel, setUsageModel] = useState("");
  const [tokens, setTokens] = useState<TokenRecord[]>([]);
  const [issuedToken, setIssuedToken] = useState("");
  const [tokenSubjectType, setTokenSubjectType] = useState("agent");
  const [tokenSubjectId, setTokenSubjectId] = useState("");
  const [tokenScopes, setTokenScopes] = useState(["chat", "embeddings", "models", "usage"]);
  const [tokenLifetimeDays, setTokenLifetimeDays] = useState(30);
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [auditLogs, setAuditLogs] = useState<AuditLog[]>([]);
  const [selectedProvider, setSelectedProvider] = useState("");
  const [modelId, setModelId] = useState("");
  const [customModel, setCustomModel] = useState(false);
  const [prompt, setPrompt] = useState("Reply with one short sentence confirming the gateway works.");
  const [maxTokens, setMaxTokens] = useState(256);
  const [response, setResponse] = useState<any>(null);
  const [testUsage, setTestUsage] = useState<UsageEvent | null>(null);
  const [status, setStatus] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [autoSyncedProviders, setAutoSyncedProviders] = useState<string[]>([]);

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const sep = path.includes("?") ? "&" : "?";
    const res = await fetch(`${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
      ...init,
    });
    const text = await res.text();
    let data: any = null;
    if (text) {
      try { data = JSON.parse(text); } catch { data = { error: text }; }
    }
    if (!res.ok) throw new Error(data?.error?.message || data?.error || res.statusText);
    return data as T;
  }, [projectId]);

  const loadCore = useCallback(async () => {
    setStatus("");
    try {
      const [providerData, modelData, rateData, policyData, usageData] = await Promise.all([
        api<{ providers: ProviderConfig[] }>("/providers"),
        api<{ models: ProviderModel[] }>("/models"),
        api<{ provider_rates: ProviderRate[] }>("/provider-rates"),
        api<{ policy: Policy }>("/policy"),
        api<UsageSummary>("/usage"),
      ]);
      const nextProviders = providerData.providers || [];
      setProviders(nextProviders);
      setModels(modelData.models || []);
      setProviderRates(rateData.provider_rates || []);
      setSelectedProvider((current) => nextProviders.some((p) => p.enabled && p.provider === current)
        ? current
        : nextProviders.find((p) => p.enabled)?.provider || "");
      if (policyScope === "project") setPolicy(normalizePolicy(policyData.policy, projectId));
      setUsage(usageData);
    } catch (error) {
      setStatus((error as Error).message);
    }
  }, [api, policyScope, projectId]);

  const loadTokens = useCallback(async () => {
    const data = await api<{ tokens: TokenRecord[] }>(`/tokens?include_revoked=${includeRevoked}`);
    setTokens(data.tokens || []);
  }, [api, includeRevoked]);

  const loadUsage = useCallback(async () => {
    const query = queryString({ subject_type: usageSubjectType, subject_id: usageSubjectId, model: usageModel });
    const [summary, events] = await Promise.all([
      api<UsageSummary>(`/usage${query}`),
      api<{ usage_events: UsageEvent[] }>(`/usage/events${query}`),
    ]);
    setUsage(summary);
    setUsageEvents(events.usage_events || []);
  }, [api, usageModel, usageSubjectId, usageSubjectType]);

  const loadRates = useCallback(async () => {
    const data = await api<{ provider_rates: ProviderRate[] }>("/provider-rates");
    setProviderRates(data.provider_rates || []);
  }, [api]);

  const loadAudit = useCallback(async () => {
    const data = await api<{ audit_logs: AuditLog[] }>("/audit?limit=200");
    setAuditLogs(data.audit_logs || []);
  }, [api]);

  useEffect(() => { void loadCore(); }, [loadCore]);
  useEffect(() => {
    if (tab === "tokens") void loadTokens().catch((error) => setStatus(error.message));
    if (tab === "usage") void loadUsage().catch((error) => setStatus(error.message));
    if (tab === "rates") void loadRates().catch((error) => setStatus(error.message));
    if (tab === "audit") void loadAudit().catch((error) => setStatus(error.message));
  }, [loadAudit, loadRates, loadTokens, loadUsage, tab]);

  const activeProviders = useMemo(() => providers.filter((provider) => provider.enabled), [providers]);
  const modelSuggestions = useMemo(() => models
    .filter((model) => model.provider === selectedProvider && model.status === "active")
    .map((model) => ({ id: model.model_id, label: model.display_name || model.model_id })), [models, selectedProvider]);

  useEffect(() => {
    if (!selectedProvider || modelSuggestions.length === 0 || customModel) return;
    if (!modelSuggestions.some((model) => model.id === modelId)) setModelId(modelSuggestions[0].id);
  }, [customModel, modelId, modelSuggestions, selectedProvider]);

  useEffect(() => {
    if (!selectedProvider || modelSuggestions.length > 0 || autoSyncedProviders.includes(selectedProvider)) return;
    if (!activeProviders.some((provider) => provider.provider === selectedProvider)) return;
    setAutoSyncedProviders((current) => [...current, selectedProvider]);
    void syncModels(selectedProvider, true);
  }, [activeProviders, autoSyncedProviders, modelSuggestions.length, selectedProvider]);

  const perform = async (name: string, task: () => Promise<void>) => {
    setBusy(name);
    setStatus("");
    setNotice("");
    try { await task(); } catch (error) { setStatus((error as Error).message); } finally { setBusy(""); }
  };

  const syncModels = async (provider = selectedProvider, quiet = false, all = false) => {
    if (!provider && !all) return;
    if (!quiet) setBusy("sync");
    try {
      const data = await api<{ results: Array<{ provider: string; status: string; model_count: number; error?: string }> }>("/models/sync", {
        method: "POST", body: JSON.stringify(all ? {} : { provider }),
      });
      const result = data.results?.find((item) => item.provider === provider) || data.results?.[0];
      const failures = data.results?.filter((item) => item.status === "error") || [];
      if (failures.length > 0) throw new Error(failures.map((item) => `${item.provider}: ${item.error || "sync failed"}`).join("; "));
      const modelData = await api<{ models: ProviderModel[] }>("/models");
      setModels(modelData.models || []);
      await loadRates();
      if (!quiet) setNotice(all ? `${data.results?.reduce((sum, item) => sum + (item.model_count || 0), 0) || 0} models synced` : `${provider}: ${result?.model_count || 0} models synced`);
    } catch (error) {
      if (!quiet) setStatus((error as Error).message);
    } finally {
      if (!quiet) setBusy("");
    }
  };

  const runTest = () => perform("test", async () => {
    setResponse(null);
    setTestUsage(null);
    const data = await api<any>("/test/chat", {
      method: "POST",
      body: JSON.stringify({
        model: modelForRequest(selectedProvider, modelId),
        messages: [{ role: "user", content: prompt }],
        max_tokens: maxTokens,
      }),
    });
    setResponse(data);
    const [summary, events] = await Promise.all([
      api<UsageSummary>("/usage"),
      api<{ usage_events: UsageEvent[] }>("/usage/events?subject_type=app&subject_id=llm-panel&limit=1"),
    ]);
    setUsage(summary);
    setTestUsage(events.usage_events?.[0] || null);
  });

  const saveRate = () => perform("rate-save", async () => {
    await api("/provider-rates", { method: "PUT", body: JSON.stringify({ ...rateDraft, source: "manual" }) });
    await loadRates();
    setNotice(`${rateDraft.provider}/${rateDraft.model_id} rate saved`);
  });

  const refreshRates = () => perform("rate-refresh", async () => {
    const data = await api<{ updated: number }>("/provider-rates/refresh", { method: "POST", body: JSON.stringify({}) });
    await loadRates();
    setNotice(`${data.updated || 0} provider rates refreshed`);
  });

  const deleteRate = () => perform("rate-delete", async () => {
    if (!rateDraft.id) return;
    await api(`/provider-rates?id=${rateDraft.id}`, { method: "DELETE" });
    await loadRates();
    setRateDraft({ ...defaultRate, project_id: projectId });
    setNotice("Provider rate closed");
  });

  const saveProvider = () => perform("provider", async () => {
    await api("/providers", { method: "PUT", body: JSON.stringify(providerDraft) });
    await loadCore();
    setNotice(`${providerDraft.provider} saved`);
  });

  const loadPolicy = () => perform("policy-load", async () => {
    const query = policyScope === "subject" ? queryString({ subject_type: policySubjectType, subject_id: policySubjectId }) : "";
    const data = await api<{ policy: Policy }>(`/policy${query}`);
    setPolicy(normalizePolicy(data.policy, projectId));
  });

  const savePolicy = () => perform("policy", async () => {
    const payload = {
      ...policy,
      subject_type: policyScope === "subject" ? policySubjectType : "",
      subject_id: policyScope === "subject" ? policySubjectId : "",
      allowed_models: policy.allowed_models || [],
      blocked_models: policy.blocked_models || [],
      allowed_providers: policy.allowed_providers || [],
      limits: policy.limits || {},
      fallback_policy: policy.fallback_policy || { routes: [] },
    };
    const data = await api<{ policy: Policy }>("/policy", { method: "PUT", body: JSON.stringify(payload) });
    setPolicy(normalizePolicy(data.policy, projectId));
    setNotice("Policy saved");
  });

  const setSubjectDisabled = (disabled: boolean) => perform("subject-status", async () => {
    if (!policySubjectId) throw new Error("Subject ID is required");
    const data = await api<{ policy: Policy }>(disabled ? "/subjects/suspend" : "/subjects/resume", {
      method: "POST", body: JSON.stringify({ subject_type: policySubjectType, subject_id: policySubjectId }),
    });
    setPolicy(normalizePolicy(data.policy, projectId));
    setNotice(disabled ? "Subject suspended" : "Subject resumed");
  });

  const issueToken = () => perform("token-create", async () => {
    if (!tokenSubjectId) throw new Error("Subject ID is required");
    const expiresAt = tokenLifetimeDays > 0
      ? new Date(Date.now() + tokenLifetimeDays * 86400000).toISOString()
      : undefined;
    const data = await api<{ token: string }>("/tokens", {
      method: "POST",
      body: JSON.stringify({ subject_type: tokenSubjectType, subject_id: tokenSubjectId, scopes: tokenScopes, expires_at: expiresAt }),
    });
    setIssuedToken(data.token);
    await loadTokens();
  });

  const revokeToken = (id: number) => perform(`token-${id}`, async () => {
    await api("/tokens/revoke", { method: "POST", body: JSON.stringify({ token_id: id }) });
    await loadTokens();
  });

  const assistantText = response?.choices?.[0]?.message?.content || "";
  const toolCalls = response?.choices?.[0]?.message?.tool_calls || [];

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2 min-w-0">
        <div className="font-medium whitespace-nowrap">LLM Gateway</div>
        <nav className="flex items-center gap-1 overflow-x-auto min-w-0" aria-label="LLM Gateway views">
          {(["test", "providers", "rates", "policy", "tokens", "usage", "audit"] as Tab[]).map((item) => (
            <TabButton key={item} active={tab === item} onClick={() => setTab(item)}>{item === "rates" ? "Provider rates" : titleCase(item)}</TabButton>
          ))}
        </nav>
        <div className="ml-auto min-w-0 text-right">
          {status && <div className="text-xs text-error truncate max-w-[36rem]" title={status}>{status}</div>}
          {!status && notice && <div className="text-xs text-success truncate max-w-[36rem]" title={notice}>{notice}</div>}
        </div>
      </header>

      {tab === "test" && (
        <div className="flex-1 min-h-0 grid grid-cols-1 xl:grid-cols-[minmax(360px,500px)_1fr]">
          <main className="overflow-auto p-4 xl:border-r border-border space-y-3">
            <Field label="Provider">
              <select value={selectedProvider} onChange={(event) => { setSelectedProvider(event.target.value); setCustomModel(false); }} disabled={activeProviders.length === 0} className={controlClass}>
                {activeProviders.length === 0 && <option value="">No enabled providers</option>}
                {activeProviders.map((provider) => <option key={provider.provider} value={provider.provider}>{provider.provider}{provider.source === "bound_integration" ? " (integration)" : ""}</option>)}
              </select>
            </Field>
            <Field label="Model">
              {modelSuggestions.length > 0 && !customModel ? (
                <div className="flex gap-2">
                  <select value={modelId} onChange={(event) => setModelId(event.target.value)} className={controlClass}>
                    {modelSuggestions.map((model) => <option key={model.id} value={model.id}>{model.label === model.id ? model.id : `${model.label} (${model.id})`}</option>)}
                  </select>
                  <button type="button" onClick={() => { setCustomModel(true); setModelId(""); }} className={secondaryButton}>Custom</button>
                </div>
              ) : (
                <div className="flex gap-2">
                  <input value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="Provider model ID" className={`${controlClass} font-mono`} />
                  {modelSuggestions.length > 0 && <button type="button" onClick={() => setCustomModel(false)} className={secondaryButton}>List</button>}
                </div>
              )}
            </Field>
            <div className="flex items-center gap-2 min-w-0">
              <button type="button" onClick={() => void syncModels()} disabled={!!busy || !selectedProvider} className={secondaryButton}>{busy === "sync" ? "Syncing..." : "Sync models"}</button>
              <span className="text-xs text-text-dim truncate">Models also refresh automatically.</span>
            </div>
            <Field label="Prompt">
              <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} className={`${controlClass} min-h-[180px] resize-y`} />
            </Field>
            <NumberField label="Maximum output tokens" value={maxTokens} onChange={setMaxTokens} />
            <button type="button" disabled={!!busy || !selectedProvider || !modelId || !prompt} onClick={runTest} className={primaryButton}>{busy === "test" ? "Running..." : "Run"}</button>
          </main>
          <section className="overflow-auto p-4 border-t xl:border-t-0 border-border">
            <h2 className="text-sm font-medium mb-3">Response</h2>
            {!response && <div className="text-sm text-text-dim">No response yet</div>}
            {testUsage && <div className="mb-4 grid grid-cols-3 gap-2"><CompactMetric label="Input" value={testUsage.request_tokens.toLocaleString()} /><CompactMetric label="Output" value={testUsage.response_tokens.toLocaleString()} /><CompactMetric label="Provider cost" value={formatMicrounits(testUsage.provider_cost_microunits, testUsage.provider_cost_currency)} detail={testUsage.provider_cost_status} /></div>}
            {assistantText && <div className="text-sm whitespace-pre-wrap leading-6">{assistantText}</div>}
            {toolCalls.length > 0 && <div className="mt-4 space-y-2">{toolCalls.map((call: any) => <div key={call.id} className="border border-border rounded p-3"><div className="text-xs font-medium">{call.function?.name}</div><pre className="mt-2 text-xs overflow-auto">{prettyJSON(call.function?.arguments)}</pre></div>)}</div>}
            {response && <details className="mt-5"><summary className="text-xs text-text-muted cursor-pointer">Raw response</summary><pre className="mt-2 bg-bg-input border border-border rounded p-3 text-xs overflow-auto max-h-[32rem]">{pretty(response)}</pre></details>}
          </section>
        </div>
      )}

      {tab === "providers" && (
        <div className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[320px_1fr]">
          <aside className="border-b lg:border-b-0 lg:border-r border-border overflow-auto max-h-64 lg:max-h-none">
            <div className="p-2 border-b border-border flex gap-2">
              <button type="button" onClick={() => setProviderDraft({ ...defaultProvider })} className={`${secondaryButton} flex-1`}>New provider</button>
              <button type="button" onClick={() => void syncModels("", false, true)} disabled={!!busy} className={secondaryButton}>Sync all</button>
            </div>
            {providers.map((provider) => (
              <button key={`${provider.project_id}:${provider.provider}:${provider.source || provider.id}`} type="button" onClick={() => setProviderDraft({ ...provider })} className="w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input">
                <div className="flex items-center gap-2 min-w-0"><span className={`h-2 w-2 rounded-full ${provider.enabled ? "bg-success" : "bg-text-dim"}`} /><span className="text-sm font-medium truncate">{provider.provider}</span><span className="ml-auto text-xs text-text-dim truncate">{provider.source === "bound_integration" ? "integration" : provider.auth_mode}</span></div>
                <div className="mt-1 text-xs text-text-dim truncate">{provider.base_url}</div>
                <div className="mt-1 text-xs text-text-dim">{models.filter((model) => model.provider === provider.provider && model.status === "active").length} models</div>
              </button>
            ))}
          </aside>
          <main className="overflow-auto p-4 max-w-3xl space-y-3">
            {providerDraft.source === "bound_integration" && <div className="text-xs border border-info/30 bg-info/10 text-info rounded p-3">This route comes from a bound integration. Saving creates a project override; disabling that override disables the provider for this project.</div>}
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <Field label="Provider ID"><input list="known-llm-providers" value={providerDraft.provider} onChange={(event) => { const provider = event.target.value.toLowerCase(); setProviderDraft({ ...providerDraft, provider, base_url: providerBaseURLs[provider] || providerDraft.base_url, key_ref: providerKeyRefs[provider] || `${provider.replaceAll("-", "_")}_api_key` }); }} className={`${controlClass} font-mono`} /><datalist id="known-llm-providers">{knownProviders.map((provider) => <option key={provider} value={provider} />)}</datalist></Field>
              <Field label="Authentication"><select value={providerDraft.auth_mode} onChange={(event) => setProviderDraft({ ...providerDraft, auth_mode: event.target.value })} className={controlClass}><option value="platform_shared">Shared app credential</option><option value="customer_owned">Bound connection</option><option value="provider_aggregator">Aggregator credential</option></select></Field>
            </div>
            <Field label="OpenAI-compatible base URL"><input value={providerDraft.base_url || ""} onChange={(event) => setProviderDraft({ ...providerDraft, base_url: event.target.value })} placeholder="https://provider.example/v1" className={`${controlClass} font-mono`} /></Field>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <Field label="Credential key reference"><input value={providerDraft.key_ref || ""} onChange={(event) => setProviderDraft({ ...providerDraft, key_ref: event.target.value })} className={`${controlClass} font-mono`} /></Field>
              <NumberField label="Connection ID" value={providerDraft.connection_id || 0} onChange={(value) => setProviderDraft({ ...providerDraft, connection_id: value })} />
              <NumberField label="Priority" value={providerDraft.priority} onChange={(value) => setProviderDraft({ ...providerDraft, priority: value })} />
            </div>
            <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={providerDraft.enabled} onChange={(event) => setProviderDraft({ ...providerDraft, enabled: event.target.checked })} />Enabled for this project</label>
            <button type="button" disabled={!!busy || !providerDraft.provider || !providerDraft.base_url || (providerDraft.auth_mode === "customer_owned" && !providerDraft.connection_id)} onClick={saveProvider} className={primaryButton}>{busy === "provider" ? "Saving..." : "Save provider"}</button>
          </main>
        </div>
      )}

      {tab === "rates" && (
        <div className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[360px_1fr]">
          <aside className="border-b lg:border-b-0 lg:border-r border-border overflow-auto max-h-72 lg:max-h-none">
            <div className="p-2 border-b border-border flex gap-2">
              <button type="button" onClick={() => setRateDraft({ ...defaultRate, project_id: projectId, provider: selectedProvider })} className={`${secondaryButton} flex-1`}>New override</button>
              <button type="button" onClick={refreshRates} disabled={!!busy} className={secondaryButton}>{busy === "rate-refresh" ? "Refreshing..." : "Refresh"}</button>
            </div>
            {providerRates.length === 0 && <div className="p-4 text-sm text-text-dim">No provider rates available.</div>}
            {providerRates.map((rate) => (
              <button key={rate.id} type="button" onClick={() => setRateDraft({ ...rate })} className="w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input">
                <div className="flex items-center gap-2 min-w-0"><span className="text-sm font-medium truncate">{rate.provider}/{rate.model_id}</span><span className="ml-auto text-xs text-text-dim">{rate.project_id ? "project" : "global"}</span></div>
                <div className="mt-1 flex items-center gap-3 text-xs text-text-dim"><span>{formatRate(rate.input_microunits_per_million)} in</span><span>{formatRate(rate.output_microunits_per_million)} out</span><span className="ml-auto">{rateSourceLabel(rate.source)}</span></div>
              </button>
            ))}
          </aside>
          <main className="overflow-auto p-4 max-w-4xl space-y-4">
            {rateDraft.id && <div className="text-xs text-text-muted flex flex-wrap gap-x-2"><span>{rateSourceLabel(rateDraft.source)} rate effective {formatDate(rateDraft.effective_from)}{rateDraft.effective_to ? ` through ${formatDate(rateDraft.effective_to)}` : ""}.</span>{rateDraft.extra_rates?.verified_at && <span>Verified {rateDraft.extra_rates.verified_at}.</span>}{rateDraft.source_reference && isHTTPURL(rateDraft.source_reference) && <a href={rateDraft.source_reference} target="_blank" rel="noreferrer" className="text-accent hover:underline">Pricing source</a>}</div>}
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <Field label="Provider"><input list="known-llm-providers" value={rateDraft.provider} onChange={(event) => setRateDraft({ ...rateDraft, provider: event.target.value.toLowerCase() })} className={`${controlClass} font-mono`} /></Field>
              <Field label="Native model ID"><input list="known-provider-models" value={rateDraft.model_id} onChange={(event) => setRateDraft({ ...rateDraft, model_id: event.target.value })} className={`${controlClass} font-mono`} /><datalist id="known-provider-models">{models.filter((model) => !rateDraft.provider || model.provider === rateDraft.provider).map((model) => <option key={`${model.provider}:${model.model_id}`} value={model.model_id} />)}</datalist></Field>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
              <MoneyField label="Input / 1M tokens" microunits={rateDraft.input_microunits_per_million} onChange={(value) => setRateDraft({ ...rateDraft, input_microunits_per_million: value })} />
              <MoneyField label="Output / 1M tokens" microunits={rateDraft.output_microunits_per_million} onChange={(value) => setRateDraft({ ...rateDraft, output_microunits_per_million: value })} />
              <MoneyField label="Cached input / 1M" microunits={rateDraft.cached_input_microunits_per_million} onChange={(value) => setRateDraft({ ...rateDraft, cached_input_microunits_per_million: value })} />
              <MoneyField label="Cache write / 1M" microunits={rateDraft.cache_write_microunits_per_million} onChange={(value) => setRateDraft({ ...rateDraft, cache_write_microunits_per_million: value })} />
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3 max-w-xl">
              <MoneyField label="Fee per request" microunits={rateDraft.request_microunits} onChange={(value) => setRateDraft({ ...rateDraft, request_microunits: value })} />
              <Field label="Currency"><input value={rateDraft.currency} maxLength={3} onChange={(event) => setRateDraft({ ...rateDraft, currency: event.target.value.toUpperCase() })} className={`${controlClass} font-mono`} /></Field>
            </div>
            <div className="flex flex-wrap gap-2">
              <button type="button" disabled={!!busy || !rateDraft.provider || !rateDraft.model_id || rateDraft.currency.length !== 3} onClick={saveRate} className={primaryButton}>{busy === "rate-save" ? "Saving..." : "Save manual override"}</button>
              {rateDraft.id && rateDraft.project_id === projectId && <button type="button" disabled={!!busy} onClick={deleteRate} className={dangerButton}>{busy === "rate-delete" ? "Closing..." : "Close rate"}</button>}
            </div>
          </main>
        </div>
      )}

      {tab === "policy" && (
        <div className="flex-1 overflow-auto p-4 space-y-4">
          <div className="flex flex-wrap items-end gap-3 border-b border-border pb-4">
            <Segmented value={policyScope} options={[{ value: "project", label: "Project" }, { value: "subject", label: "Subject" }]} onChange={(value) => setPolicyScope(value as "project" | "subject")} />
            {policyScope === "subject" && <><Field label="Subject type"><input value={policySubjectType} onChange={(event) => setPolicySubjectType(event.target.value)} className={`${controlClass} w-40`} /></Field><Field label="Subject ID"><input value={policySubjectId} onChange={(event) => setPolicySubjectId(event.target.value)} className={`${controlClass} w-64 font-mono`} /></Field><button type="button" disabled={!policySubjectId || !!busy} onClick={loadPolicy} className={secondaryButton}>Load</button></>}
          </div>
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-3">
            <TextList label="Allowed models" value={textFromList(policy.allowed_models)} onChange={(value) => setPolicy({ ...policy, allowed_models: splitLines(value) })} />
            <TextList label="Blocked models" value={textFromList(policy.blocked_models)} onChange={(value) => setPolicy({ ...policy, blocked_models: splitLines(value) })} />
            <TextList label="Allowed providers" value={textFromList(policy.allowed_providers)} onChange={(value) => setPolicy({ ...policy, allowed_providers: splitLines(value) })} />
          </div>
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
            <NumberField label="Requests per month" value={policy.limits?.monthly_request_limit || 0} onChange={(value) => setPolicyLimit(policy, setPolicy, "monthly_request_limit", value)} />
            <NumberField label="Input tokens per month" value={policy.limits?.monthly_input_token_limit || 0} onChange={(value) => setPolicyLimit(policy, setPolicy, "monthly_input_token_limit", value)} />
            <NumberField label="Output tokens per month" value={policy.limits?.monthly_output_token_limit || 0} onChange={(value) => setPolicyLimit(policy, setPolicy, "monthly_output_token_limit", value)} />
            <NumberField label="Maximum output per request" value={policy.limits?.max_tokens_per_request || 0} onChange={(value) => setPolicyLimit(policy, setPolicy, "max_tokens_per_request", value)} />
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3 max-w-3xl">
            <MoneyField label="Monthly provider cost limit" microunits={policy.limits?.monthly_provider_cost_limit_microunits || 0} onChange={(value) => setPolicyLimit(policy, setPolicy, "monthly_provider_cost_limit_microunits", value)} />
            <Field label="Cost currency"><input value={policy.limits?.provider_cost_currency || "USD"} maxLength={3} onChange={(event) => setPolicy({ ...policy, limits: { ...(policy.limits || {}), provider_cost_currency: event.target.value.toUpperCase() } })} className={`${controlClass} font-mono`} /></Field>
            <Field label="Unpriced models"><select value={policy.limits?.unpriced_model_behavior || "deny"} onChange={(event) => setPolicy({ ...policy, limits: { ...(policy.limits || {}), unpriced_model_behavior: event.target.value as "allow" | "deny" } })} className={controlClass}><option value="deny">Deny when cost cap is active</option><option value="allow">Allow and mark unpriced</option></select></Field>
          </div>
          <Field label="Failover routes"><textarea value={formatFallbackRoutes(policy.fallback_policy?.routes)} onChange={(event) => setPolicy({ ...policy, fallback_policy: { routes: parseFallbackRoutes(event.target.value) } })} placeholder={"openrouter | openrouter/anthropic/claude-sonnet-4\nfireworks | fireworks/accounts/example/models/model-id"} className={`${controlClass} min-h-24 font-mono text-xs`} /></Field>
          {policyScope === "subject" && <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={!!policy.disabled} onChange={(event) => setPolicy({ ...policy, disabled: event.target.checked })} />Subject access disabled</label>}
          <div className="flex flex-wrap gap-2">
            <button type="button" disabled={!!busy || (policyScope === "subject" && !policySubjectId)} onClick={savePolicy} className={primaryButton}>{busy === "policy" ? "Saving..." : "Save policy"}</button>
            {policyScope === "subject" && <><button type="button" disabled={!!busy || !policySubjectId} onClick={() => setSubjectDisabled(true)} className={dangerButton}>Suspend subject</button><button type="button" disabled={!!busy || !policySubjectId} onClick={() => setSubjectDisabled(false)} className={secondaryButton}>Resume subject</button></>}
          </div>
        </div>
      )}

      {tab === "tokens" && (
        <div className="flex-1 overflow-auto p-4 space-y-5">
          <div className="grid grid-cols-1 md:grid-cols-4 gap-3 max-w-5xl">
            <Field label="Subject type"><input value={tokenSubjectType} onChange={(event) => setTokenSubjectType(event.target.value)} className={controlClass} /></Field>
            <Field label="Subject ID"><input value={tokenSubjectId} onChange={(event) => setTokenSubjectId(event.target.value)} className={`${controlClass} font-mono`} /></Field>
            <NumberField label="Lifetime in days (0 = none)" value={tokenLifetimeDays} onChange={setTokenLifetimeDays} />
            <div className="flex items-end"><button type="button" disabled={!!busy || !tokenSubjectId || tokenScopes.length === 0} onClick={issueToken} className={primaryButton}>{busy === "token-create" ? "Creating..." : "Create token"}</button></div>
          </div>
          <div className="flex flex-wrap gap-4">{["chat", "embeddings", "models", "usage", "usage:project"].map((scope) => <label key={scope} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={tokenScopes.includes(scope)} onChange={() => setTokenScopes(toggleValue(tokenScopes, scope))} />{scope}</label>)}</div>
          {issuedToken && <div className="max-w-5xl border border-warn/40 bg-warn/10 rounded p-3"><div className="flex items-center gap-2 mb-2"><span className="text-xs text-warn">Shown once</span><button type="button" onClick={() => void navigator.clipboard.writeText(issuedToken)} className={`${secondaryButton} ml-auto`}>Copy</button></div><div className="font-mono text-xs break-all select-all">{issuedToken}</div></div>}
          <div className="flex items-center gap-3 border-t border-border pt-4"><h2 className="text-sm font-medium">Issued tokens</h2><label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={includeRevoked} onChange={(event) => setIncludeRevoked(event.target.checked)} />Include revoked</label><button type="button" onClick={() => void loadTokens()} className={`${secondaryButton} ml-auto`}>Refresh</button></div>
          <div className="overflow-auto border border-border rounded"><table className="w-full text-xs"><thead className="bg-bg-input text-text-muted"><tr><Th>ID</Th><Th>Subject</Th><Th>Scopes</Th><Th>Expires</Th><Th>Status</Th><Th></Th></tr></thead><tbody>{tokens.map((token) => <tr key={token.id} className="border-t border-border"><Td mono>{token.id}</Td><Td>{token.subject_type}<div className="font-mono text-text-dim">{token.subject_id}</div></Td><Td>{token.scopes.join(", ")}</Td><Td>{formatDate(token.expires_at)}</Td><Td>{token.revoked_at ? "revoked" : "active"}</Td><Td>{!token.revoked_at && <button type="button" disabled={!!busy} onClick={() => revokeToken(token.id)} className={dangerButton}>{busy === `token-${token.id}` ? "Revoking..." : "Revoke"}</button>}</Td></tr>)}</tbody></table></div>
        </div>
      )}

      {tab === "usage" && (
        <div className="flex-1 overflow-auto p-4 space-y-4">
          <div className="flex flex-wrap items-end gap-3"><Field label="Subject type"><input value={usageSubjectType} onChange={(event) => setUsageSubjectType(event.target.value)} className={`${controlClass} w-40`} /></Field><Field label="Subject ID"><input value={usageSubjectId} onChange={(event) => setUsageSubjectId(event.target.value)} className={`${controlClass} w-56 font-mono`} /></Field><Field label="Model"><input value={usageModel} onChange={(event) => setUsageModel(event.target.value)} className={`${controlClass} w-72 font-mono`} /></Field><button type="button" onClick={() => void perform("usage", loadUsage)} className={secondaryButton}>{busy === "usage" ? "Loading..." : "Apply filters"}</button></div>
          {usage && <><div className="grid grid-cols-2 lg:grid-cols-5 gap-3"><Metric label="Requests" value={usage.requests} /><Metric label="Input tokens" value={usage.request_tokens} /><Metric label="Output tokens" value={usage.response_tokens} /><Metric label="Total tokens" value={usage.total_tokens} /><Metric label="Provider cost" value={formatMicrounits(usage.provider_cost_microunits, usage.provider_cost_currency)} /></div><div className="text-xs text-text-muted">{usage.reported_cost_requests || 0} reported · {usage.calculated_cost_requests || 0} calculated · {usage.partial_cost_requests || 0} partial · {usage.unpriced_requests || 0} unpriced</div></>}
          <div className="overflow-auto border border-border rounded"><table className="w-full text-xs"><thead className="bg-bg-input text-text-muted"><tr><Th>Time</Th><Th>Subject</Th><Th>Route</Th><Th>Tokens</Th><Th>Provider cost</Th><Th>Status</Th><Th>Request ID</Th></tr></thead><tbody>{usageEvents.map((event) => <tr key={event.id} className="border-t border-border"><Td>{formatDate(event.created_at)}</Td><Td>{event.subject_type}<div className="font-mono text-text-dim">{event.subject_id}</div></Td><Td>{event.provider}<div className="font-mono text-text-dim max-w-72 truncate" title={event.model}>{event.model}</div></Td><Td mono>{event.request_tokens} + {event.response_tokens}</Td><Td><span className="font-mono tabular-nums">{formatMicrounits(event.provider_cost_microunits, event.provider_cost_currency)}</span><div className="text-text-dim">{event.provider_cost_status}{event.provider_cost_source ? ` · ${event.provider_cost_source}` : ""}</div></Td><Td>{event.status}</Td><Td mono><span className="block max-w-56 truncate" title={event.request_id}>{event.request_id}</span></Td></tr>)}</tbody></table></div>
        </div>
      )}

      {tab === "audit" && (
        <div className="flex-1 overflow-auto p-4 space-y-3"><div className="flex items-center"><h2 className="text-sm font-medium">Recent gateway activity</h2><button type="button" onClick={() => void loadAudit()} className={`${secondaryButton} ml-auto`}>Refresh</button></div><div className="overflow-auto border border-border rounded"><table className="w-full text-xs"><thead className="bg-bg-input text-text-muted"><tr><Th>Time</Th><Th>Subject</Th><Th>Action</Th><Th>Route</Th><Th>Status</Th><Th>Message</Th></tr></thead><tbody>{auditLogs.map((entry) => <tr key={entry.id} className="border-t border-border"><Td>{formatDate(entry.created_at)}</Td><Td>{entry.subject_type}<div className="font-mono text-text-dim">{entry.subject_id}</div></Td><Td mono>{entry.action}</Td><Td>{entry.provider}<div className="font-mono text-text-dim max-w-64 truncate">{entry.model}</div></Td><Td>{entry.status}</Td><Td><span className="block max-w-96 truncate" title={entry.message}>{entry.message}</span></Td></tr>)}</tbody></table></div></div>
      )}
    </div>
  );
}

const controlClass = "w-full min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text";
const primaryButton = "px-3 py-1.5 text-sm bg-accent text-bg rounded hover:opacity-90 disabled:opacity-50 whitespace-nowrap";
const secondaryButton = "px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50 whitespace-nowrap";
const dangerButton = "px-3 py-1.5 text-sm border border-error/40 text-error rounded hover:bg-error/10 disabled:opacity-50 whitespace-nowrap";

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} aria-current={active ? "page" : undefined} className={`px-2 py-1 text-sm border rounded whitespace-nowrap ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:bg-bg-input"}`}>{children}</button>;
}

function Segmented({ value, options, onChange }: { value: string; options: Array<{ value: string; label: string }>; onChange: (value: string) => void }) {
  return <div className="inline-flex border border-border rounded p-0.5">{options.map((option) => <button key={option.value} type="button" onClick={() => onChange(option.value)} className={`px-3 py-1 text-sm rounded ${value === option.value ? "bg-accent text-bg" : "text-text-muted hover:bg-bg-input"}`}>{option.label}</button>)}</div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block min-w-0"><div className="text-xs text-text-muted mb-1">{label}</div>{children}</label>;
}

function TextList({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <Field label={label}><textarea value={value} onChange={(event) => onChange(event.target.value)} spellCheck={false} className={`${controlClass} min-h-[140px] font-mono text-xs resize-y`} /></Field>;
}

function NumberField({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) {
  return <Field label={label}><input type="number" min={0} value={value || ""} onChange={(event) => onChange(Math.max(0, Number(event.target.value) || 0))} className={controlClass} /></Field>;
}

function MoneyField({ label, microunits, onChange }: { label: string; microunits: number; onChange: (value: number) => void }) {
  return <Field label={label}><div className="relative"><span className="absolute left-2 top-1.5 text-sm text-text-dim">$</span><input type="number" min={0} step="0.000001" value={microunits ? microunits / 1_000_000 : ""} onChange={(event) => onChange(Math.max(0, Math.round((Number(event.target.value) || 0) * 1_000_000)))} className={`${controlClass} pl-6 font-mono`} /></div></Field>;
}

function Metric({ label, value }: { label: string; value: number | string }) {
  return <div className="border border-border rounded p-3 bg-bg-card"><div className="text-xs text-text-muted">{label}</div><div className="mt-1 text-xl font-semibold tabular-nums">{typeof value === "number" ? value.toLocaleString() : value}</div></div>;
}

function CompactMetric({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return <div className="border border-border rounded p-2 min-w-0"><div className="text-[11px] text-text-muted">{label}</div><div className="mt-0.5 text-sm font-medium tabular-nums truncate" title={value}>{value}</div>{detail && <div className="text-[10px] text-text-dim truncate">{detail}</div>}</div>;
}

function Th({ children }: { children?: ReactNode }) { return <th className="text-left font-medium px-3 py-2 whitespace-nowrap">{children}</th>; }
function Td({ children, mono = false }: { children: ReactNode; mono?: boolean }) { return <td className={`px-3 py-2 align-top ${mono ? "font-mono tabular-nums" : ""}`}>{children}</td>; }

function normalizePolicy(policy: Policy | undefined, projectId: string): Policy {
  return { project_id: projectId, limits: {}, ...(policy || {}), fallback_policy: policy?.fallback_policy || { routes: [] } };
}

function setPolicyLimit(policy: Policy, setPolicy: (policy: Policy) => void, key: keyof Limits, value: number) {
  setPolicy({ ...policy, limits: { ...(policy.limits || {}), [key]: value } });
}

function splitLines(value: string): string[] { return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean); }

function rateSourceLabel(source: string): string {
  if (source === "builtin_catalog") return "Built-in catalog";
  if (source === "provider_api") return "Provider API";
  if (source === "provider_response") return "Provider response";
  return titleCase(source || "unknown");
}

function isHTTPURL(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:";
  } catch {
    return false;
  }
}
function textFromList(values?: string[]): string { return (values || []).join("\n"); }
function pretty(value: unknown): string { return JSON.stringify(value ?? {}, null, 2); }
function prettyJSON(value: unknown): string { try { return JSON.stringify(typeof value === "string" ? JSON.parse(value) : value, null, 2); } catch { return String(value || ""); } }
function titleCase(value: string): string { return value.charAt(0).toUpperCase() + value.slice(1); }
function toggleValue(values: string[], value: string): string[] { return values.includes(value) ? values.filter((item) => item !== value) : [...values, value]; }
function formatDate(value?: string): string { if (!value) return "-"; const date = new Date(value); return Number.isNaN(date.getTime()) ? value : date.toLocaleString(); }
function formatRate(microunits: number): string { return `$${(microunits / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 6 })}`; }
function formatMicrounits(microunits = 0, currency = "USD"): string { try { return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD", minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(microunits / 1_000_000); } catch { return `${currency || "USD"} ${(microunits / 1_000_000).toFixed(6)}`; } }
function queryString(values: Record<string, string>): string { const query = new URLSearchParams(); Object.entries(values).forEach(([key, value]) => { if (value.trim()) query.set(key, value.trim()); }); const out = query.toString(); return out ? `?${out}` : ""; }
function modelForRequest(provider: string, modelId: string): string { const cleanProvider = provider.trim(); const cleanModel = modelId.trim(); return cleanProvider && cleanModel && !cleanModel.startsWith(`${cleanProvider}/`) ? `${cleanProvider}/${cleanModel}` : cleanModel; }
function formatFallbackRoutes(routes?: FallbackRoute[]): string { return (routes || []).map((route) => `${route.provider} | ${route.model}`).join("\n"); }
function parseFallbackRoutes(value: string): FallbackRoute[] { return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).map((line) => { const [provider, ...model] = line.split("|"); return { provider: provider.trim(), model: model.join("|").trim() }; }).filter((route) => route.model); }
