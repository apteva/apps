import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/ads";
const PANEL_PROJECT_ID = new URL(import.meta.url).searchParams.get("project_id") || "";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface AdAccount {
  id: number;
  platform: string;
  connection_id: number;
  native_account_id: string;
  display_name: string;
  currency: string;
  timezone: string;
  status: string;
  created_at: string;
}

interface PendingAccountPage {
  id: string;
  name: string;
  currency: string;
  timezone: string;
  test_account?: boolean;
}

interface ConnectionOption {
  id: number;
  name: string;
}

interface Campaign {
  id: string;
  name: string;
  objective?: string;
  status?: string;
  effective_status?: string;
  daily_budget?: string;
}

interface PlatformInfo {
  platform: string;
  display_name: string;
  integration_slug: string;
  supported: boolean;
  configured: boolean;
  available: boolean;
  state: "setup_required" | "ready" | "connected" | "unsupported" | "unavailable";
  can_add: boolean;
  setup_url: string;
  connection_count: number;
  connections: ConnectionOption[];
  active_account: boolean;
  unavailable_reason?: string;
}

interface AdResource {
  id: number;
  ad_account_id: number;
  kind: string;
  provider_type: string;
  name: string;
  status: string;
  capabilities: string[];
  metadata: Record<string, unknown>;
  parent_resource_id?: number | null;
  managed_by_app: boolean;
  refreshed_at?: string;
}

interface AccountContext {
  ad_account_id: number;
  platform: string;
  resource_kinds: string[];
  resources: AdResource[];
  defaults: Record<string, AdResource>;
  refresh_errors: Record<string, string>;
}

interface PendingPicker {
  pendingId: number;
  platform?: string;
  pages: PendingAccountPage[];
}

function mcpErrorText(data: any): string | null {
  if (!data?.isError) return null;
  return data.content?.find((item: any) => item.type === "text")?.text || "Request returned an error";
}

function ProviderMark({ platform, size = "md" }: { platform: string; size?: "sm" | "md" }) {
  const meta = platform === "meta";
  return (
    <span
      aria-hidden="true"
      className={`${size === "sm" ? "h-7 w-7 text-xs" : "h-9 w-9 text-sm"} ${meta ? "bg-blue text-white" : "bg-white text-blue"} grid shrink-0 place-items-center rounded border border-border font-semibold shadow-sm`}
    >
      {meta ? "M" : "G"}
    </span>
  );
}

function statusStyle(status?: string): string {
  if (status === "ACTIVE") return "bg-green/15 text-green";
  if (status === "PAUSED") return "bg-yellow/15 text-yellow";
  return "bg-border text-text-muted";
}

function displayStatus(campaign: Campaign): string {
  return campaign.status || campaign.effective_status || "UNKNOWN";
}

function formatBudget(value: string | undefined, currency: string): string {
  if (!value) return "-";
  const cents = Number(value);
  if (!Number.isFinite(cents)) return value;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: 2,
    }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency || "USD"}`;
  }
}

const RESOURCE_KIND_LABELS: Record<string, string> = {
  identity: "Publishing identities",
  tracking_source: "Conversion tracking",
  conversion_action: "Conversion actions",
  lead_form: "Lead forms",
  audience: "Audiences",
};

const RESOURCE_TYPE_LABELS: Record<string, string> = {
  facebook_page: "Facebook Page",
  instagram_business: "Instagram account",
  meta_pixel: "Meta Pixel",
  meta_lead_form: "Meta lead form",
  meta_audience: "Meta audience",
  google_conversion_action: "Google conversion action",
  google_lead_form: "Google lead form",
  google_user_list: "Google audience",
};

const LEAD_QUESTION_OPTIONS = [
  { type: "full_name", label: "Full name" },
  { type: "email", label: "Email" },
  { type: "phone", label: "Phone" },
  { type: "company_name", label: "Company" },
  { type: "job_title", label: "Job title" },
];

function resourcePurpose(resource: AdResource): string | null {
  if (resource.provider_type === "facebook_page") return "publishing_identity";
  if (resource.provider_type === "instagram_business") return "instagram_identity";
  if (resource.kind === "tracking_source" || resource.kind === "conversion_action") return "conversion_source";
  if (resource.kind === "lead_form") return "lead_form";
  if (resource.kind === "audience") return "audience";
  return null;
}

function Modal({
  title,
  description,
  actions,
  size = "default",
  onClose,
  children,
  labelledBy,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  size?: "default" | "large";
  onClose: () => void;
  children: React.ReactNode;
  labelledBy: string;
}) {
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-30 grid place-items-center bg-black/50 p-4" onMouseDown={onClose}>
      <section
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        className={`min-w-0 w-full overflow-hidden rounded border border-border bg-bg-card shadow-xl ${size === "large" ? "max-w-3xl" : "max-w-lg"}`}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <h2 id={labelledBy} className="text-sm font-semibold text-text">{title}</h2>
            {description && <p className="mt-1 text-xs text-text-muted">{description}</p>}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {actions}
            <button
              ref={closeRef}
              type="button"
              onClick={onClose}
              aria-label="Close"
              title="Close"
              className="grid h-8 w-8 place-items-center rounded text-lg text-text-muted hover:bg-bg-input hover:text-text"
            >
              ×
            </button>
          </div>
        </header>
        {children}
      </section>
    </div>
  );
}

export default function AdsPanel({ projectId, installId }: NativePanelProps) {
  const scopedProject = projectId || PANEL_PROJECT_ID;
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [loadingAccounts, setLoadingAccounts] = useState(true);
  const [loadingCampaigns, setLoadingCampaigns] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [startingPlatform, setStartingPlatform] = useState<string | null>(null);
  const [copyingPlatform, setCopyingPlatform] = useState<string | null>(null);
  const [copiedPlatform, setCopiedPlatform] = useState<string | null>(null);
  const [connectionPicker, setConnectionPicker] = useState<PlatformInfo | null>(null);
  const [connectionPickerMode, setConnectionPickerMode] = useState<"account" | "link">("account");
  const [pendingPicker, setPendingPicker] = useState<PendingPicker | null>(null);
  const [accountFilter, setAccountFilter] = useState("");
  const [disconnectTarget, setDisconnectTarget] = useState<AdAccount | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [setupAccount, setSetupAccount] = useState<AdAccount | null>(null);
  const [accountContext, setAccountContext] = useState<AccountContext | null>(null);
  const [loadingResources, setLoadingResources] = useState(false);
  const [savingResourceId, setSavingResourceId] = useState<number | null>(null);
  const [resourceError, setResourceError] = useState<string | null>(null);
  const [leadFormOpen, setLeadFormOpen] = useState(false);
  const [creatingLeadForm, setCreatingLeadForm] = useState(false);
  const [leadFormName, setLeadFormName] = useState("");
  const [leadFormPrivacyURL, setLeadFormPrivacyURL] = useState("");
  const [leadFormFollowUpURL, setLeadFormFollowUpURL] = useState("");
  const [leadFormBusinessName, setLeadFormBusinessName] = useState("");
  const [leadFormCampaignID, setLeadFormCampaignID] = useState("");
  const [leadFormHigherIntent, setLeadFormHigherIntent] = useState(true);
  const [leadFormQuestions, setLeadFormQuestions] = useState<string[]>(["full_name", "email", "phone"]);
  const [error, setError] = useState<string | null>(null);
  const campaignRequest = useRef(0);
  const copyFeedbackTimer = useRef<number | null>(null);
  const resourceRequest = useRef(0);

  const selected = useMemo(
    () => accounts.find((account) => account.id === selectedId) || null,
    [accounts, selectedId],
  );
  const filteredPendingPages = useMemo(() => {
    if (!pendingPicker) return [];
    const query = accountFilter.trim().toLowerCase();
    if (!query) return pendingPicker.pages;
    return pendingPicker.pages.filter((page) =>
      `${page.name} ${page.id} ${page.currency} ${page.timezone}`.toLowerCase().includes(query),
    );
  }, [accountFilter, pendingPicker]);
  const groupedResources = useMemo(() => {
    if (!accountContext) return [];
    return accountContext.resource_kinds.map((kind) => ({
      kind,
      resources: accountContext.resources.filter((resource) => resource.kind === kind),
      error: accountContext.refresh_errors?.[kind],
    }));
  }, [accountContext]);

  const appURL = useCallback((path: string) => {
    const url = new URL(`${API}${path}`, window.location.origin);
    if (scopedProject) url.searchParams.set("project_id", scopedProject);
    url.searchParams.set("install_id", String(installId));
    return url.pathname + url.search;
  }, [installId, scopedProject]);

  const apiJSON = useCallback(async (path: string, init?: RequestInit): Promise<any> => {
    const response = await fetch(appURL(path), {
      credentials: "same-origin",
      ...init,
      headers: {
        ...(init?.body ? { "content-type": "application/json" } : {}),
        ...(init?.headers || {}),
      },
    });
    if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
    const data = await response.json();
    const message = mcpErrorText(data);
    if (message) throw new Error(message);
    return data;
  }, [appURL]);

  const callTool = useCallback(async (tool: string, args: Record<string, unknown>): Promise<any> => {
    const response = await fetch(appURL("/mcp"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: {
          name: tool,
          arguments: { ...args, _project_id: scopedProject },
        },
      }),
    });
    if (!response.ok) throw new Error((await response.text()) || `${tool}: HTTP ${response.status}`);
    const envelope = await response.json();
    if (envelope.error) throw new Error(envelope.error.message || tool);
    const text = envelope.result?.content?.find((item: any) => item.type === "text")?.text;
    const data = text ? JSON.parse(text) : envelope.result;
    const message = mcpErrorText(data);
    if (message) throw new Error(message);
    return data;
  }, [appURL, scopedProject]);

  const refreshAccounts = useCallback(async () => {
    setLoadingAccounts(true);
    try {
      const result = await apiJSON("/accounts");
      const next = result.accounts || [];
      setAccounts(next);
      setSelectedId((current) => current && next.some((account: AdAccount) => account.id === current) ? current : null);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoadingAccounts(false);
    }
  }, [apiJSON]);

  const refreshPlatforms = useCallback(async () => {
    try {
      const result = await apiJSON("/platforms");
      setPlatforms(result.platforms || []);
    } catch (err) {
      setError((err as Error).message);
    }
  }, [apiJSON]);

  const refreshCampaigns = useCallback(async (account: AdAccount) => {
    const request = ++campaignRequest.current;
    setLoadingCampaigns(true);
    setCampaigns([]);
    try {
      const result = await callTool("campaign_list", { ad_account_id: account.id, limit: 50 });
      if (request !== campaignRequest.current) return;
      setCampaigns(result.data || result.campaigns || []);
    } catch (err) {
      if (request === campaignRequest.current) setError((err as Error).message);
    } finally {
      if (request === campaignRequest.current) setLoadingCampaigns(false);
    }
  }, [callTool]);

  const loadAccountContext = useCallback(async (account: AdAccount, refresh = true) => {
    const request = ++resourceRequest.current;
    setLoadingResources(true);
    setResourceError(null);
    try {
      const result = await callTool("account_context_get", { ad_account_id: account.id, refresh });
      if (request === resourceRequest.current) setAccountContext(result as AccountContext);
    } catch (err) {
      if (request === resourceRequest.current) setResourceError((err as Error).message);
    } finally {
      if (request === resourceRequest.current) setLoadingResources(false);
    }
  }, [callTool]);

  const openAccountResources = useCallback((account: AdAccount) => {
    setSetupAccount(account);
    setAccountContext(null);
    loadAccountContext(account, true);
  }, [loadAccountContext]);

  const closeAccountResources = useCallback(() => {
    resourceRequest.current++;
    setSetupAccount(null);
    setAccountContext(null);
    setResourceError(null);
    setLoadingResources(false);
    setLeadFormOpen(false);
  }, []);

  const openLeadFormCreate = () => {
    setLeadFormName("");
    setLeadFormPrivacyURL("");
    setLeadFormFollowUpURL("");
    setLeadFormBusinessName("");
    setLeadFormCampaignID("");
    setLeadFormHigherIntent(true);
    setLeadFormQuestions(["full_name", "email", "phone"]);
    setResourceError(null);
    setLeadFormOpen(true);
  };

  const toggleLeadQuestion = (type: string) => {
    setLeadFormQuestions((current) => current.includes(type)
      ? current.filter((item) => item !== type)
      : [...current, type]);
  };

  const createLeadForm = async () => {
    if (!setupAccount || !leadFormName.trim() || !leadFormPrivacyURL.trim() || leadFormQuestions.length === 0) return;
    setCreatingLeadForm(true);
    setResourceError(null);
    try {
      await callTool("lead_form_create", {
        ad_account_id: setupAccount.id,
        name: leadFormName.trim(),
        privacy_policy_url: leadFormPrivacyURL.trim(),
        follow_up_url: leadFormFollowUpURL.trim() || undefined,
        business_name: leadFormBusinessName.trim() || undefined,
        headline: leadFormName.trim(),
        description: "Request more information",
        call_to_action: "get_quote",
        call_to_action_description: "Submit the form to get in touch",
        higher_intent: leadFormHigherIntent,
        campaign_id: leadFormCampaignID || undefined,
        questions: leadFormQuestions.map((type) => ({ type })),
      });
      setLeadFormOpen(false);
      await loadAccountContext(setupAccount, true);
    } catch (err) {
      setResourceError((err as Error).message);
    } finally {
      setCreatingLeadForm(false);
    }
  };

  const setResourceDefault = async (resource: AdResource) => {
    if (!setupAccount) return;
    const purpose = resourcePurpose(resource);
    if (!purpose) return;
    setSavingResourceId(resource.id);
    setResourceError(null);
    try {
      const result = await callTool("resource_set_default", {
        ad_account_id: setupAccount.id,
        purpose,
        resource_id: resource.id,
      });
      setAccountContext((current) => current ? {
        ...current,
        defaults: { ...current.defaults, [purpose]: result.resource },
      } : current);
    } catch (err) {
      setResourceError((err as Error).message);
    } finally {
      setSavingResourceId(null);
    }
  };

  const resumeOAuth = useCallback(async (pendingId: number) => {
    try {
      const result = await apiJSON(`/accounts/${pendingId}/pages`);
      setPendingPicker({
        pendingId,
        platform: result.platform,
        pages: result.pages || [],
      });
      setAccountFilter("");
      setConnectionPicker(null);
      setAddOpen(false);
    } catch (err) {
      setError((err as Error).message);
    }
  }, [apiJSON]);

  useEffect(() => {
    refreshAccounts();
    refreshPlatforms();
  }, [refreshAccounts, refreshPlatforms]);

  useEffect(() => {
    const pending = Number(new URLSearchParams(window.location.search).get("pending"));
    if (pending > 0) resumeOAuth(pending);
  }, [resumeOAuth]);

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return;
      if (event.data?.type === "ads.oauth_ready" && event.data.pending_account_id) {
        resumeOAuth(Number(event.data.pending_account_id));
      } else if (event.data?.type === "ads.oauth_failed") {
        setError("Authorization failed. Try connecting the account again.");
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [resumeOAuth]);

  useEffect(() => {
    return () => {
      campaignRequest.current++;
      resourceRequest.current++;
      if (copyFeedbackTimer.current !== null) window.clearTimeout(copyFeedbackTimer.current);
    };
  }, []);

  const selectAccount = (account: AdAccount) => {
    setSelectedId(account.id);
    refreshCampaigns(account);
  };

  const startPlatform = async (platform: PlatformInfo, connectionId?: number) => {
    if (!platform.can_add) {
      setError(platform.unavailable_reason || "Set up this integration before adding an account.");
      return;
    }
    const activeConnections = platform.connections || [];
    if (!connectionId && activeConnections.length > 1) {
      setConnectionPickerMode("account");
      setConnectionPicker(platform);
      setAddOpen(false);
      return;
    }
    if (connectionId) setConnectionPicker(null);
    const reusableConnectionId = connectionId || activeConnections[0]?.id;
    if (reusableConnectionId) {
      setStartingPlatform(platform.platform);
      setError(null);
      try {
        const result = await apiJSON("/accounts/start", {
          method: "POST",
          body: JSON.stringify({
            platform: platform.platform,
            connection_id: reusableConnectionId,
          }),
        });
        if (!result.pending_account_id || !result.reused_connection) {
          throw new Error("The selected integration connection could not be reused.");
        }
        await resumeOAuth(Number(result.pending_account_id));
      } catch (err) {
        setError((err as Error).message);
      } finally {
        setStartingPlatform(null);
      }
      return;
    }

    const popup = window.open("about:blank", "ads_oauth", "width=620,height=760");
    if (!popup) {
      setError("Popup blocked. Allow pop-ups for this site and try again.");
      return;
    }
    setStartingPlatform(platform.platform);
    setError(null);
    try {
      const result = await apiJSON("/accounts/start", {
        method: "POST",
        body: JSON.stringify({ platform: platform.platform }),
      });
      if (result.pending_account_id && result.reused_connection) {
        popup.close();
        await resumeOAuth(Number(result.pending_account_id));
      } else if (result.authorize_url) {
        popup.location.href = result.authorize_url;
        setAddOpen(false);
      } else {
        popup.close();
        throw new Error("The server did not return an authorization URL.");
      }
    } catch (err) {
      popup.close();
      setError((err as Error).message);
    } finally {
      setStartingPlatform(null);
    }
  };

  const copyAccessLink = async (platform: PlatformInfo, connectionId?: number) => {
    const activeConnections = platform.connections || [];
    if (!connectionId && activeConnections.length > 1) {
      setConnectionPickerMode("link");
      setConnectionPicker(platform);
      setAddOpen(false);
      return;
    }
    const templateConnectionId = connectionId || activeConnections[0]?.id;
    if (!templateConnectionId) {
      setError("Set up this integration before creating an access link.");
      return;
    }

    setCopyingPlatform(platform.platform);
    setError(null);
    try {
      const response = await fetch("/api/invites", {
        method: "POST",
        credentials: "same-origin",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          app_slug: platform.integration_slug,
          source: "local",
          project_id: scopedProject,
          template_connection_id: templateConnectionId,
          ttl_seconds: 24 * 60 * 60,
        }),
      });
      if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
      const result = await response.json();
      if (!result.url) throw new Error("The server did not return an access link.");
      await navigator.clipboard.writeText(result.url);
      setConnectionPicker(null);
      setCopiedPlatform(platform.platform);
      if (copyFeedbackTimer.current !== null) window.clearTimeout(copyFeedbackTimer.current);
      copyFeedbackTimer.current = window.setTimeout(() => {
        setCopiedPlatform((current) => current === platform.platform ? null : current);
        copyFeedbackTimer.current = null;
      }, 2200);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setCopyingPlatform(null);
    }
  };

  const finalizeAccount = async (page: PendingAccountPage) => {
    if (!pendingPicker) return;
    try {
      await apiJSON("/accounts/finalize", {
        method: "POST",
        body: JSON.stringify({
          pending_account_id: pendingPicker.pendingId,
          page_id: page.id,
          name: page.name,
        }),
      });
      setPendingPicker(null);
      const cleanURL = new URL(window.location.href);
      cleanURL.searchParams.delete("pending");
      window.history.replaceState({}, "", cleanURL.pathname + cleanURL.search);
      await Promise.all([refreshAccounts(), refreshPlatforms()]);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const toggleCampaign = async (account: AdAccount, campaign: Campaign) => {
    const tool = displayStatus(campaign) === "ACTIVE" ? "campaign_pause" : "campaign_resume";
    try {
      await callTool(tool, { ad_account_id: account.id, campaign_id: campaign.id });
      if (selectedId === account.id) await refreshCampaigns(account);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const disconnectAccount = async () => {
    if (!disconnectTarget) return;
    setDisconnecting(true);
    try {
      await apiJSON(`/accounts/${disconnectTarget.id}`, { method: "DELETE" });
      if (selectedId === disconnectTarget.id) {
        campaignRequest.current++;
        setSelectedId(null);
        setCampaigns([]);
      }
      setDisconnectTarget(null);
      await Promise.all([refreshAccounts(), refreshPlatforms()]);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setDisconnecting(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-text">
      <header className="flex min-h-16 items-center justify-between gap-4 border-b border-border px-4 py-3 md:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h1 className="text-base font-semibold text-text">Ads</h1>
            <span className="rounded bg-border px-2 py-0.5 text-xs text-text-muted">
              {accounts.length}
            </span>
          </div>
          <p className="mt-0.5 truncate text-xs text-text-muted">Meta and Google campaign accounts</p>
        </div>
        <button
          type="button"
          onClick={() => setAddOpen(true)}
          className="inline-flex h-9 items-center gap-2 rounded bg-accent px-3 text-sm font-medium text-bg hover:opacity-90"
        >
          <span aria-hidden="true" className="text-base">+</span>
          <span>Add account</span>
        </button>
      </header>

      {error && (
        <div role="alert" className="mx-4 mt-3 flex items-start gap-3 rounded border border-red/40 bg-red/10 px-3 py-2 text-sm text-red">
          <span className="min-w-0 flex-1">{error}</span>
          <button type="button" onClick={() => setError(null)} aria-label="Dismiss error" title="Dismiss" className="grid h-6 w-6 place-items-center rounded hover:bg-red/10">
            ×
          </button>
        </div>
      )}

      {pendingPicker && (
        <Modal
          title="Choose an ad account"
          description={`${pendingPicker.pages.length} available from ${pendingPicker.platform === "meta" ? "Meta Ads" : "Google Ads"}.`}
          size="large"
          onClose={() => {
            setPendingPicker(null);
            setAccountFilter("");
          }}
          labelledBy="ads-account-picker-title"
        >
          {pendingPicker.pages.length > 5 && (
            <div className="border-b border-border px-4 py-3">
              <input
                type="search"
                value={accountFilter}
                onChange={(event) => setAccountFilter(event.target.value)}
                placeholder="Search accounts"
                aria-label="Search ad accounts"
                className="h-9 w-full rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent"
              />
            </div>
          )}
          <div className="max-h-[60vh] divide-y divide-border overflow-y-auto">
            {pendingPicker.pages.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">No ad accounts were returned by this connection.</p>
            ) : filteredPendingPages.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">No accounts match that search.</p>
            ) : filteredPendingPages.map((page) => (
              <div key={page.id} className="flex items-center gap-3 px-4 py-3">
                <ProviderMark platform={pendingPicker.platform || ""} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{page.name || page.id}</div>
                  <div className="truncate text-xs text-text-muted">
                    {page.id} · {page.currency || "-"} · {page.timezone || "-"}{page.test_account ? " · Test account" : ""}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => finalizeAccount(page)}
                  aria-label={`Select ${page.name || page.id}`}
                  className="h-8 w-20 shrink-0 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input"
                >
                  Select
                </button>
              </div>
            ))}
          </div>
        </Modal>
      )}

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <aside className="flex max-h-56 w-full shrink-0 flex-col border-b border-border md:max-h-none md:w-72 md:border-b-0 md:border-r">
          <div className="flex h-11 items-center justify-between border-b border-border px-3">
            <h2 className="text-xs font-medium uppercase text-text-dim">Accounts</h2>
            <button
              type="button"
              onClick={refreshAccounts}
              disabled={loadingAccounts}
              aria-label="Refresh accounts"
              title="Refresh accounts"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
            >
              ↻
            </button>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">
            {loadingAccounts && accounts.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">Loading accounts...</p>
            ) : accounts.length === 0 ? (
              <div className="px-4 py-8 text-center">
                <p className="text-sm font-medium">No ad accounts</p>
                <p className="mt-1 text-xs text-text-muted">Connect Meta Ads or Google Ads to begin.</p>
                <button type="button" onClick={() => setAddOpen(true)} className="mt-4 h-8 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input">
                  Add account
                </button>
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {accounts.map((account) => (
                  <li key={account.id} className={selectedId === account.id ? "bg-accent/10" : ""}>
                    <div className="flex items-center gap-2 px-2 py-2">
                      <button type="button" onClick={() => selectAccount(account)} className="flex min-w-0 flex-1 items-center gap-3 rounded p-1 text-left hover:bg-bg-input/60">
                        <ProviderMark platform={account.platform} size="sm" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium">{account.display_name}</span>
                          <span className="block truncate text-xs text-text-muted">{account.native_account_id} · {account.currency || "-"}</span>
                        </span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setDisconnectTarget(account)}
                        aria-label={`Remove ${account.display_name}`}
                        title="Remove account"
                        className="grid h-8 w-8 shrink-0 place-items-center rounded text-text-muted hover:bg-red/10 hover:text-red"
                      >
                        ×
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {!selected ? (
            <div className="grid min-h-64 flex-1 place-items-center p-6 text-center">
              <div>
                <div className="mx-auto grid h-12 w-12 place-items-center rounded border border-border bg-bg-card text-xl text-text-muted">▥</div>
                <h2 className="mt-4 text-sm font-medium">Select an account</h2>
                <p className="mt-1 text-sm text-text-muted">Campaigns for the selected provider appear here.</p>
              </div>
            </div>
          ) : (
            <>
              <header className="flex min-h-16 items-center justify-between gap-3 border-b border-border px-4 py-3">
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderMark platform={selected.platform} size="sm" />
                  <div className="min-w-0">
                    <h2 className="truncate text-sm font-semibold">{selected.display_name}</h2>
                    <p className="truncate text-xs text-text-muted">{selected.platform === "meta" ? "Meta Ads" : "Google Ads"} · {selected.native_account_id} · {selected.currency || "-"}</p>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <button
                    type="button"
                    onClick={() => openAccountResources(selected)}
                    className="h-9 rounded border border-border px-3 text-xs font-medium text-text-muted hover:bg-bg-input hover:text-text"
                  >
                    Resources
                  </button>
                  <button
                    type="button"
                    onClick={() => refreshCampaigns(selected)}
                    disabled={loadingCampaigns}
                    aria-label="Refresh campaigns"
                    title="Refresh campaigns"
                    className="grid h-9 w-9 place-items-center rounded border border-border text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
                  >
                    ↻
                  </button>
                </div>
              </header>
              <div className="min-h-0 flex-1 overflow-auto">
                {loadingCampaigns ? (
                  <p className="p-4 text-sm text-text-muted">Loading campaigns...</p>
                ) : campaigns.length === 0 ? (
                  <div className="grid min-h-64 place-items-center p-6 text-center">
                    <div>
                      <h3 className="text-sm font-medium">No campaigns found</h3>
                      <p className="mt-1 text-sm text-text-muted">This account has no campaigns to display.</p>
                    </div>
                  </div>
                ) : (
                  <table className="w-full table-fixed text-sm" style={{ minWidth: "48rem" }}>
                    <thead className="sticky top-0 z-10 bg-bg-input text-xs text-text-dim">
                      <tr>
                        <th className="w-2/5 px-4 py-2 text-left font-medium">Campaign</th>
                        <th className="w-1/5 px-3 py-2 text-left font-medium">Objective</th>
                        <th className="w-28 px-3 py-2 text-left font-medium">Status</th>
                        <th className="w-36 px-3 py-2 text-right font-medium">Daily budget</th>
                        <th className="w-24 px-3 py-2 text-right font-medium">Action</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {campaigns.map((campaign) => {
                        const status = displayStatus(campaign);
                        return (
                          <tr key={campaign.id} className="hover:bg-bg-input/40">
                            <td className="px-4 py-3">
                              <div className="truncate font-medium" title={campaign.name}>{campaign.name || campaign.id}</div>
                              <div className="truncate font-mono text-xs text-text-dim">{campaign.id}</div>
                            </td>
                            <td className="truncate px-3 py-3 text-text-muted">{campaign.objective || "-"}</td>
                            <td className="px-3 py-3">
                              <span className={`rounded px-2 py-1 text-xs font-medium ${statusStyle(status)}`}>{status}</span>
                            </td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{formatBudget(campaign.daily_budget, selected.currency)}</td>
                            <td className="px-3 py-3 text-right">
                              <button type="button" onClick={() => toggleCampaign(selected, campaign)} className="h-8 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input">
                                {status === "ACTIVE" ? "Pause" : "Resume"}
                              </button>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                )}
              </div>
            </>
          )}
        </main>
      </div>

      {addOpen && (
        <Modal
          title="Add ad account"
          description="Choose a provider to connect an ad account."
          actions={(
            <button
              type="button"
              onClick={refreshPlatforms}
              aria-label="Refresh providers"
              title="Refresh providers"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text"
            >
              ↻
            </button>
          )}
          size="large"
          onClose={() => setAddOpen(false)}
          labelledBy="ads-add-title"
        >
          <div className="divide-y divide-border">
            {platforms.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">Checking providers...</p>
            ) : platforms.map((platform) => (
              <div key={platform.platform} className="flex items-center gap-3 px-4 py-3">
                <ProviderMark platform={platform.platform} />
                <div className="min-w-0 flex-1">
                  <span className="block text-sm font-medium">
                    {platform.platform === "meta" ? "Meta Ads" : platform.display_name}
                  </span>
                  <span className="mt-0.5 block text-xs text-text-muted">
                    {platform.state === "connected"
                      ? `${platform.connection_count} active connection${platform.connection_count === 1 ? "" : "s"}`
                      : platform.state === "ready"
                        ? "Ready to connect"
                        : platform.state === "setup_required"
                          ? `${platform.platform === "meta" ? "Facebook & Instagram" : platform.display_name} integration required`
                          : platform.unavailable_reason || "Integration unavailable"}
                  </span>
                </div>
                {platform.state === "setup_required" ? (
                  <a
                    href={platform.setup_url || "/integrations"}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex h-8 w-28 shrink-0 items-center justify-center whitespace-nowrap rounded border border-accent px-3 text-xs font-medium text-accent hover:bg-accent/10"
                  >
                    Set up
                  </a>
                ) : (
                  <div className="flex shrink-0 items-center gap-2">
                    {platform.configured && (
                      <button
                        type="button"
                        disabled={copyingPlatform === platform.platform}
                        onClick={() => copyAccessLink(platform)}
                        className="h-8 w-20 whitespace-nowrap rounded border border-border px-2 text-xs font-medium text-text-muted hover:bg-bg-input hover:text-text disabled:cursor-wait disabled:opacity-50"
                      >
                        {copyingPlatform === platform.platform
                          ? "Copying..."
                          : copiedPlatform === platform.platform
                            ? "Copied"
                            : "Copy link"}
                      </button>
                    )}
                    <button
                      type="button"
                      disabled={!platform.can_add || startingPlatform === platform.platform}
                      onClick={() => startPlatform(platform)}
                      className="h-8 w-24 whitespace-nowrap rounded border border-accent px-3 text-xs font-medium text-accent hover:bg-accent/10 disabled:cursor-not-allowed disabled:border-border disabled:text-text-muted disabled:opacity-50"
                    >
                      {startingPlatform === platform.platform
                        ? "Starting..."
                        : platform.state === "connected"
                          ? "Choose"
                          : "Connect"}
                    </button>
                  </div>
                )}
              </div>
            ))}
          </div>
        </Modal>
      )}

      {connectionPicker && (
        <Modal
          title={connectionPickerMode === "link"
            ? `${connectionPicker.display_name} access link`
            : `Choose ${connectionPicker.display_name} connection`}
          description={connectionPickerMode === "link"
            ? "Select the connection to use for this access link."
            : "Select the dashboard integration connection whose ad accounts you want to use."}
          size="large"
          onClose={() => setConnectionPicker(null)}
          labelledBy="ads-connection-picker-title"
        >
          <div className="divide-y divide-border">
            {(connectionPicker.connections || []).map((connection) => (
              <button
                key={connection.id}
                type="button"
                disabled={
                  connectionPickerMode === "link"
                    ? copyingPlatform === connectionPicker.platform
                    : startingPlatform === connectionPicker.platform
                }
                onClick={() => connectionPickerMode === "link"
                  ? copyAccessLink(connectionPicker, connection.id)
                  : startPlatform(connectionPicker, connection.id)}
                className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-bg-input disabled:cursor-wait disabled:opacity-50"
              >
                <ProviderMark platform={connectionPicker.platform} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-text">{connection.name || connectionPicker.display_name}</div>
                  <div className="text-xs text-text-muted">Connection #{connection.id}</div>
                </div>
                <span className="text-xs font-medium text-accent">
                  {connectionPickerMode === "link"
                    ? copyingPlatform === connectionPicker.platform ? "Copying..." : "Copy link"
                    : startingPlatform === connectionPicker.platform ? "Loading..." : "Choose"}
                </span>
              </button>
            ))}
          </div>
        </Modal>
      )}

      {setupAccount && !leadFormOpen && (
        <Modal
          title="Account resources"
          description={`${setupAccount.display_name} · ${setupAccount.platform === "meta" ? "Meta Ads" : "Google Ads"}`}
          actions={(
            <button
              type="button"
              onClick={() => loadAccountContext(setupAccount, true)}
              disabled={loadingResources}
              aria-label="Refresh account resources"
              title="Refresh account resources"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
            >
              ↻
            </button>
          )}
          size="large"
          onClose={closeAccountResources}
          labelledBy="ads-resources-title"
        >
          <div className="max-h-[70vh] overflow-y-auto">
            {resourceError && (
              <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-3 text-sm text-red">
                {resourceError}
              </div>
            )}
            {loadingResources && !accountContext ? (
              <p className="px-4 py-10 text-center text-sm text-text-muted">Discovering account resources...</p>
            ) : groupedResources.length === 0 ? (
              <p className="px-4 py-10 text-center text-sm text-text-muted">No provider resources are available for this account.</p>
            ) : (
              <div className="divide-y divide-border">
                {groupedResources.map((group) => (
                  <section key={group.kind}>
                    <div className="flex items-center justify-between gap-3 bg-bg-input px-4 py-2">
                      <h3 className="text-xs font-medium uppercase text-text-dim">
                        {RESOURCE_KIND_LABELS[group.kind] || group.kind}
                      </h3>
                      <div className="flex items-center gap-3">
                        <span className="text-xs tabular-nums text-text-dim">{group.resources.length}</span>
                        {group.kind === "lead_form" && (
                          <button
                            type="button"
                            onClick={openLeadFormCreate}
                            className="h-7 rounded border border-border px-2.5 text-xs font-medium text-text hover:bg-bg-card"
                          >
                            Create
                          </button>
                        )}
                      </div>
                    </div>
                    {group.error ? (
                      <p className="px-4 py-3 text-xs text-red">{group.error}</p>
                    ) : group.resources.length === 0 ? (
                      <p className="px-4 py-3 text-xs text-text-muted">None available from this provider connection.</p>
                    ) : (
                      <div className="divide-y divide-border">
                        {group.resources.map((resource) => {
                          const purpose = resourcePurpose(resource);
                          const isDefault = purpose ? accountContext?.defaults?.[purpose]?.id === resource.id : false;
                          const isActive = resource.status === "active";
                          return (
                            <div key={resource.id} className="flex items-center gap-3 px-4 py-3">
                              <div className="min-w-0 flex-1">
                                <div className="truncate text-sm font-medium text-text">{resource.name || RESOURCE_TYPE_LABELS[resource.provider_type] || "Unnamed resource"}</div>
                                <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-text-muted">
                                  <span>{RESOURCE_TYPE_LABELS[resource.provider_type] || resource.provider_type}</span>
                                  {!isActive && <span className="text-yellow">{resource.status}</span>}
                                </div>
                              </div>
                              {purpose && (
                                <button
                                  type="button"
                                  onClick={() => setResourceDefault(resource)}
                                  disabled={!isActive || isDefault || savingResourceId !== null}
                                  className={`h-8 w-24 shrink-0 rounded border px-3 text-xs font-medium disabled:cursor-default ${isDefault ? "border-green/40 bg-green/10 text-green" : "border-border text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"}`}
                                >
                                  {isDefault ? "Default" : savingResourceId === resource.id ? "Saving..." : "Use"}
                                </button>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </section>
                ))}
              </div>
            )}
          </div>
        </Modal>
      )}

      {setupAccount && leadFormOpen && (
        <Modal
          title="Create lead form"
          description={`${setupAccount.display_name} · ${setupAccount.platform === "meta" ? "Meta Ads" : "Google Ads"}`}
          size="large"
          onClose={() => !creatingLeadForm && setLeadFormOpen(false)}
          labelledBy="ads-lead-form-title"
        >
          <form onSubmit={(event) => { event.preventDefault(); createLeadForm(); }}>
            {resourceError && (
              <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-3 text-sm text-red">{resourceError}</div>
            )}
            <div className="grid gap-4 px-4 py-4 md:grid-cols-2">
              <label className="grid gap-1.5 text-xs font-medium text-text-muted">
                Form name
                <input required value={leadFormName} onChange={(event) => setLeadFormName(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              {setupAccount.platform === "google" && (
                <label className="grid gap-1.5 text-xs font-medium text-text-muted">
                  Business name
                  <input required value={leadFormBusinessName} onChange={(event) => setLeadFormBusinessName(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
                </label>
              )}
              <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                Privacy policy URL
                <input required type="url" value={leadFormPrivacyURL} onChange={(event) => setLeadFormPrivacyURL(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                Follow-up URL
                <input type="url" value={leadFormFollowUpURL} onChange={(event) => setLeadFormFollowUpURL(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              {setupAccount.platform === "google" && campaigns.length > 0 && selectedId === setupAccount.id && (
                <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                  Campaign
                  <select value={leadFormCampaignID} onChange={(event) => setLeadFormCampaignID(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent">
                    <option value="">Not attached</option>
                    {campaigns.map((campaign) => <option key={campaign.id} value={campaign.id}>{campaign.name || campaign.id}</option>)}
                  </select>
                </label>
              )}
              <fieldset className="grid gap-2 md:col-span-2">
                <legend className="mb-1 text-xs font-medium text-text-muted">Questions</legend>
                <div className="grid gap-2 sm:grid-cols-2 md:grid-cols-3">
                  {LEAD_QUESTION_OPTIONS.map((question) => (
                    <label key={question.type} className="flex h-9 items-center gap-2 rounded border border-border bg-bg-input px-3 text-sm text-text">
                      <input type="checkbox" checked={leadFormQuestions.includes(question.type)} onChange={() => toggleLeadQuestion(question.type)} className="h-4 w-4 accent-accent" />
                      {question.label}
                    </label>
                  ))}
                </div>
              </fieldset>
              <label className="flex h-9 items-center gap-2 text-sm text-text md:col-span-2">
                <input type="checkbox" checked={leadFormHigherIntent} onChange={(event) => setLeadFormHigherIntent(event.target.checked)} className="h-4 w-4 accent-accent" />
                Higher intent
              </label>
            </div>
            <footer className="flex justify-end gap-2 border-t border-border px-4 py-3">
              <button type="button" disabled={creatingLeadForm} onClick={() => setLeadFormOpen(false)} className="h-9 rounded border border-border px-3 text-sm text-text hover:bg-bg-input disabled:opacity-50">Cancel</button>
              <button type="submit" disabled={creatingLeadForm || !leadFormName.trim() || !leadFormPrivacyURL.trim() || leadFormQuestions.length === 0 || (setupAccount.platform === "google" && !leadFormBusinessName.trim())} className="h-9 rounded bg-accent px-3 text-sm font-medium text-black hover:opacity-90 disabled:opacity-50">
                {creatingLeadForm ? "Creating..." : "Create form"}
              </button>
            </footer>
          </form>
        </Modal>
      )}

      {disconnectTarget && (
        <Modal title="Remove ad account?" description={disconnectTarget.display_name} onClose={() => !disconnecting && setDisconnectTarget(null)} labelledBy="ads-remove-title">
          <div className="px-4 py-4 text-sm text-text-muted">
            The account is removed from this project. The shared provider connection remains available to other apps.
          </div>
          <footer className="flex justify-end gap-2 border-t border-border px-4 py-3">
            <button type="button" disabled={disconnecting} onClick={() => setDisconnectTarget(null)} className="h-9 rounded border border-border px-3 text-sm hover:bg-bg-input disabled:opacity-50">Cancel</button>
            <button type="button" disabled={disconnecting} onClick={disconnectAccount} className="h-9 rounded bg-red px-3 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50">
              {disconnecting ? "Removing..." : "Remove"}
            </button>
          </footer>
        </Modal>
      )}
    </div>
  );
}
