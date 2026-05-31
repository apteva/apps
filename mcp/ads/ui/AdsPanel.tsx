// AdsPanel — minimal v0.1 surface.
//
// What this panel does:
//   - Lists connected ad accounts.
//   - "Add account" opens a platform picker, starts OAuth in a popup
//     when needed, then shows an upstream ad-account picker.
//   - When an account is selected, shows its campaigns (campaign_list)
//     with pause/resume buttons.
//
// What it deliberately doesn't do (v0.1):
//   - Campaign / ad-set / ad / creative / audience CREATE flows. Those
//     are the agent's job today; the panel only surfaces state. Adding
//     forms is straightforward once the unified shape settles, but the
//     escape-hatch (platform_options) makes a clean form non-trivial
//     for v0.1 — punt to v0.2.

import { useCallback, useEffect, useState } from "react";

const API = "/api/apps/ads";
const PANEL_PROJECT_ID = new URL(import.meta.url).searchParams.get("project_id") || "";
let activePanelProjectId = PANEL_PROJECT_ID;

function appURL(path: string, projectId?: string | null): string {
  const scopedProject = projectId || activePanelProjectId || PANEL_PROJECT_ID;
  if (!scopedProject) return `${API}${path}`;
  const sep = path.includes("?") ? "&" : "?";
  return `${API}${path}${sep}project_id=${encodeURIComponent(scopedProject)}`;
}

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
}

interface Campaign {
  id: string;
  name: string;
  objective: string;
  status: string;
  effective_status?: string;
  daily_budget?: string;
  lifetime_budget?: string;
  created_time?: string;
}

interface PlatformInfo {
  platform: string;
  display_name: string;
  integration_slug: string;
  supported: boolean;
  available: boolean;
  can_add: boolean;
  requires_picker: boolean;
  connection_count: number;
  active_account: boolean;
  unavailable_reason?: string;
}

function mcpErrorText(data: any): string | null {
  if (!data?.isError) return null;
  return data.content?.find((c: any) => c.type === "text")?.text || "Request returned an error";
}

function campaignStatusClass(status: string): string {
  if (status === "ACTIVE") return "bg-accent/10 text-accent";
  if (status === "PAUSED") return "bg-yellow/15 text-yellow";
  return "bg-border text-text-muted";
}

function platformInitial(platform: string): string {
  if (platform === "meta") return "M";
  if (platform === "google") return "G";
  return platform.slice(0, 1).toUpperCase();
}

async function apiJSON(path: string, init?: RequestInit): Promise<any> {
  const res = await fetch(appURL(path), {
    credentials: "same-origin",
    ...init,
    headers: {
      ...(init?.body ? { "content-type": "application/json" } : {}),
      ...(init?.headers || {}),
    },
  });
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status}: ${await res.text()}`);
  const data = await res.json();
  const err = mcpErrorText(data);
  if (err) throw new Error(err);
  return data;
}

async function callTool(tool: string, args: Record<string, unknown>): Promise<any> {
  const res = await fetch(`${API}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: { name: tool, arguments: args },
    }),
  });
  if (!res.ok) throw new Error(`${tool}: ${res.status}`);
  const j = await res.json();
  if (j.error) throw new Error(j.error.message || tool);
  const text = j.result?.content?.[0]?.text;
  if (!text) return j.result;
  const parsed = JSON.parse(text);
  const err = mcpErrorText(parsed);
  if (err) throw new Error(err);
  return parsed;
}

export default function AdsPanel(props: NativePanelProps) {
  activePanelProjectId = props.projectId || PANEL_PROJECT_ID;
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [selected, setSelected] = useState<AdAccount | null>(null);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [loading, setLoading] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [startingPlatform, setStartingPlatform] = useState<string | null>(null);
  const [pendingPicker, setPendingPicker] = useState<{
    pendingId: number;
    platform?: string;
    pages: PendingAccountPage[];
  } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refreshAccounts = useCallback(async () => {
    try {
      const r = await apiJSON("/accounts");
      setAccounts(r.accounts || []);
    } catch (e: any) {
      setError(e.message);
    }
  }, []);

  const refreshPlatforms = useCallback(async () => {
    try {
      const r = await apiJSON("/platforms");
      setPlatforms(r.platforms || []);
    } catch (e: any) {
      setError(e.message);
    }
  }, []);

  useEffect(() => {
    refreshAccounts();
    refreshPlatforms();
    // After OAuth callback the platform 302s us back with ?pending=N;
    // pick that up and show the picker without a manual click.
    const pending = new URLSearchParams(window.location.search).get("pending");
    if (pending) {
      handleResumeFromOAuth(Number(pending));
    }
  }, [refreshAccounts, refreshPlatforms]);

  useEffect(() => {
    const onMsg = (ev: MessageEvent) => {
      if (ev.data?.type === "ads.oauth_ready" && ev.data.pending_account_id) {
        handleResumeFromOAuth(Number(ev.data.pending_account_id));
      }
      if (ev.data?.type === "ads.oauth_failed") {
        setError("OAuth authorization failed. Try connecting the account again.");
      }
    };
    window.addEventListener("message", onMsg);
    return () => window.removeEventListener("message", onMsg);
  }, []);

  const refreshCampaigns = useCallback(async (acct: AdAccount) => {
    setLoading(true);
    try {
      const r = await callTool("campaign_list", { ad_account_id: acct.id, limit: 50 });
      // facebook-ads returns {data:[...]}; surface that shape directly
      setCampaigns(r.data || r.campaigns || []);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const handleSelect = (acct: AdAccount) => {
    setSelected(acct);
    refreshCampaigns(acct);
  };

  const handleAddAccount = () => {
    setAddOpen(true);
  };

  const handleStartPlatform = (platform: PlatformInfo) => {
    if (!platform.can_add) {
      setError(platform.unavailable_reason || "This platform is not available.");
      return;
    }
    const popup = window.open("about:blank", "ads_oauth", "width=620,height=760");
    if (!popup) {
      setError("Popup blocked. Allow pop-ups for this site and try again.");
      return;
    }
    setStartingPlatform(platform.platform);
    setError(null);
    (async () => {
      const fail = (msg: string) => {
        setError(msg);
        setStartingPlatform(null);
        try { popup.close(); } catch {}
      };
      try {
        const r = await apiJSON("/accounts/start", {
          method: "POST",
          body: JSON.stringify({ platform: platform.platform }),
        });
        if (!r.authorize_url && r.pending_account_id && r.reused_connection) {
          try { popup.close(); } catch {}
          setStartingPlatform(null);
          setAddOpen(false);
          await handleResumeFromOAuth(Number(r.pending_account_id));
          return;
        }
        if (!r.authorize_url) {
          fail("Server did not return an OAuth authorization URL.");
          return;
        }
        popup.location.href = r.authorize_url;
        setStartingPlatform(null);
        setAddOpen(false);
      } catch (e: any) {
        fail("Start failed: " + e.message);
      }
    })();
  };

  const handleForceOAuth = async () => {
    try {
      await refreshPlatforms();
      setAddOpen(true);
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleResumeFromOAuth = async (pendingId: number) => {
    try {
      const r = await apiJSON(`/accounts/${pendingId}/pages`);
      setPendingPicker({ pendingId, platform: r.platform, pages: r.pages || [] });
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleFinalize = async (page: PendingAccountPage) => {
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
      window.history.replaceState({}, "", window.location.pathname);
      await refreshAccounts();
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleStatusToggle = async (c: Campaign) => {
    if (!selected) return;
    const tool = c.status === "ACTIVE" ? "campaign_pause" : "campaign_resume";
    try {
      await callTool(tool, { ad_account_id: selected.id, campaign_id: c.id });
      await refreshCampaigns(selected);
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleDisconnect = async (acct: AdAccount) => {
    if (!confirm(`Disconnect ${acct.display_name}?`)) return;
    try {
      await apiJSON(`/accounts/${acct.id}`, { method: "DELETE" });
      if (selected?.id === acct.id) setSelected(null);
      await refreshAccounts();
      await refreshPlatforms();
    } catch (e: any) {
      setError(e.message);
    }
  };

  return (
    <div className="h-full flex flex-col text-text">
      <header className="flex items-center justify-between gap-4 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold text-text">Ads</h1>
          <p className="text-xs text-text-muted truncate">
            {accounts.length} connected account{accounts.length === 1 ? "" : "s"}
            {selected ? ` · ${selected.display_name}` : ""}
          </p>
        </div>
        <button
          type="button"
          onClick={handleAddAccount}
          className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg whitespace-nowrap"
        >
          + Add account
        </button>
      </header>

      {error && (
        <div className="mx-4 mt-4 rounded border border-red/40 bg-red/10 px-3 py-2 text-sm text-red flex items-center gap-3">
          <span className="flex-1 min-w-0">{error}</span>
          <button
            type="button"
            onClick={() => setError(null)}
            className="text-red/80 hover:text-red px-1"
          >×</button>
        </div>
      )}

      {addOpen && (
        <div className="fixed inset-0 bg-black/40 grid place-items-center z-20 p-4">
          <div className="w-full max-w-lg bg-bg-card border border-border rounded shadow-xl">
            <div className="px-4 py-3 border-b border-border flex items-center gap-3">
              <div className="min-w-0">
                <h3 className="text-sm font-medium text-text">Add ad account</h3>
                <p className="text-xs text-text-muted">Choose an available ads integration.</p>
              </div>
              <button
                type="button"
                onClick={() => setAddOpen(false)}
                className="ml-auto text-text-muted hover:text-text text-lg leading-none px-1"
              >×</button>
            </div>
            <div className="p-4">
              {platforms.length === 0 ? (
                <p className="text-sm text-text-muted">Checking ad integrations…</p>
              ) : (
                <div className="grid gap-2">
                  {platforms.map(p => (
                    <button
                      type="button"
                      key={p.platform}
                      disabled={!p.can_add || startingPlatform === p.platform}
                      onClick={() => handleStartPlatform(p)}
                      className={`w-full text-left border rounded px-3 py-2 transition ${
                        p.can_add
                          ? "border-border hover:bg-bg-input"
                          : "border-border bg-bg-input/40 opacity-70 cursor-not-allowed"
                      }`}
                    >
                      <div className="flex items-center gap-3">
                        <span className="w-7 h-7 rounded bg-bg-input border border-border flex items-center justify-center text-xs font-medium text-text-muted shrink-0">
                          {platformInitial(p.platform)}
                        </span>
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2 min-w-0">
                            <span className="font-medium text-sm text-text truncate">{p.display_name}</span>
                            {p.active_account && (
                              <span className="text-[10px] px-1.5 py-0.5 rounded bg-accent/10 text-accent shrink-0">added</span>
                            )}
                            {p.available && !p.supported && (
                              <span className="text-[10px] px-1.5 py-0.5 rounded bg-yellow/15 text-yellow shrink-0">not wired</span>
                            )}
                          </div>
                          <div className="mt-0.5 text-xs text-text-muted">
                            {p.can_add
                              ? `${p.connection_count || 1} active connection${(p.connection_count || 1) === 1 ? "" : "s"} found.`
                              : p.unavailable_reason}
                          </div>
                        </div>
                      </div>
                    </button>
                  ))}
                </div>
              )}
              <button
                type="button"
                onClick={handleForceOAuth}
                className="mt-3 text-xs text-accent hover:underline"
              >
                Refresh available integrations
              </button>
            </div>
          </div>
        </div>
      )}

      {pendingPicker && (
        <section className="mx-4 mt-4 border border-border rounded bg-bg-input/30">
          <header className="px-3 py-2 border-b border-border">
            <h3 className="text-sm font-medium text-text">Pick an ad account</h3>
            <p className="text-xs text-text-muted">Select the upstream account to manage from this project.</p>
          </header>
          {pendingPicker.pages.length === 0 ? (
            <p className="px-3 py-4 text-sm text-text-muted">No ad accounts found on this connection.</p>
          ) : (
            <ul className="divide-y divide-border">
              {pendingPicker.pages.map(p => (
                <li key={p.id} className="px-3 py-2 flex items-center gap-3">
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-text truncate">{p.name || p.id}</div>
                    <div className="text-xs text-text-muted truncate">
                      {p.id} · {p.currency} · {p.timezone}
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => handleFinalize(p)}
                    className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg shrink-0"
                  >
                    Use this account
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

      <div className="flex-1 min-h-0 flex">
        <aside className="w-80 max-w-[40%] shrink-0 border-r border-border flex flex-col">
          <div className="px-3 py-2 border-b border-border">
            <h2 className="text-xs uppercase tracking-wide text-text-dim">Connected accounts</h2>
          </div>
          <div className="flex-1 overflow-auto">
            {accounts.length === 0 ? (
              <div className="py-12 px-4 text-center text-sm text-text-muted">
                No ad accounts connected.
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {accounts.map(a => (
                  <li key={a.id}>
                    <button
                      type="button"
                      onClick={() => handleSelect(a)}
                      className={`w-full text-left px-3 py-2 hover:bg-bg-input/60 ${
                        selected?.id === a.id ? "bg-accent/10" : ""
                      }`}
                    >
                      <div className="flex items-start gap-3">
                        <span className="w-7 h-7 rounded bg-bg-input border border-border flex items-center justify-center text-xs font-medium text-text-muted shrink-0">
                          {platformInitial(a.platform)}
                        </span>
                        <div className="flex-1 min-w-0">
                          <div className="text-sm font-medium text-text truncate">{a.display_name}</div>
                          <div className="text-xs text-text-muted truncate">
                            {a.platform} · {a.native_account_id} · {a.currency || "—"}
                          </div>
                          <button
                            type="button"
                            onClick={(e) => { e.stopPropagation(); handleDisconnect(a); }}
                            className="mt-1 text-[11px] text-red hover:underline"
                          >
                            Disconnect
                          </button>
                        </div>
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        <main className="flex-1 min-w-0 flex flex-col">
          {!selected ? (
            <div className="flex-1 grid place-items-center p-6">
              <div className="text-center">
                <h2 className="text-sm font-medium text-text">Select an ad account</h2>
                <p className="text-sm text-text-muted mt-1">Campaigns appear here after you choose an account.</p>
              </div>
            </div>
          ) : (
            <>
              <header className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <h2 className="text-sm font-medium text-text truncate">{selected.display_name}</h2>
                  <p className="text-xs text-text-muted truncate">
                    {selected.platform} · {selected.native_account_id} · {selected.currency || "—"}
                  </p>
                </div>
                <button
                  type="button"
                  onClick={() => refreshCampaigns(selected)}
                  disabled={loading}
                  className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-50 shrink-0"
                >
                  Refresh
                </button>
              </header>
              <div className="flex-1 overflow-auto">
                {loading && (
                  <div className="p-4 text-sm text-text-muted">Loading campaigns…</div>
                )}
                {!loading && campaigns.length === 0 && (
                  <div className="py-12 px-6 text-center text-sm text-text-muted">
                    No campaigns on this account yet.
                  </div>
                )}
                {!loading && campaigns.length > 0 && (
                  <table className="w-full text-sm">
                    <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
                      <tr>
                        <th className="text-left px-4 py-2 font-normal">Name</th>
                        <th className="text-left px-4 py-2 font-normal w-36">Objective</th>
                        <th className="text-left px-4 py-2 font-normal w-28">Status</th>
                        <th className="text-left px-4 py-2 font-normal w-32">Daily budget</th>
                        <th className="text-right px-4 py-2 font-normal w-28">Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {campaigns.map(c => (
                        <tr key={c.id} className="border-t border-border hover:bg-bg-input/30">
                          <td className="px-4 py-2">
                            <div className="text-text font-medium truncate max-w-lg" title={c.name}>{c.name}</div>
                            <div className="text-xs text-text-dim font-mono truncate max-w-lg">{c.id}</div>
                          </td>
                          <td className="px-4 py-2 text-text-muted">{c.objective || "—"}</td>
                          <td className="px-4 py-2">
                            <span className={`text-[10px] px-1.5 py-0.5 rounded ${campaignStatusClass(c.status)}`}>
                              {c.status || c.effective_status || "—"}
                            </span>
                          </td>
                          <td className="px-4 py-2 text-text-muted">
                            {c.daily_budget ? `$${(Number(c.daily_budget) / 100).toFixed(2)}` : "—"}
                          </td>
                          <td className="px-4 py-2 text-right">
                            <button
                              type="button"
                              onClick={() => handleStatusToggle(c)}
                              className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                            >
                              {c.status === "ACTIVE" ? "Pause" : "Resume"}
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </>
          )}
        </main>
      </div>
    </div>
  );
}
