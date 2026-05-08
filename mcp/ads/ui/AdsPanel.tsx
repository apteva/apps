// AdsPanel — minimal v0.1 surface.
//
// What this panel does:
//   - Lists connected ad accounts (account_list).
//   - "Add account" button starts the OAuth flow (account_add → opens
//     authorize_url in a new tab → on return, picker over the upstream
//     ad-account list → account_finalize).
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
  return JSON.parse(text);
}

export default function AdsPanel(_props: NativePanelProps) {
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  const [selected, setSelected] = useState<AdAccount | null>(null);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [loading, setLoading] = useState(false);
  const [pendingPicker, setPendingPicker] = useState<{
    pendingId: number;
    pages: PendingAccountPage[];
  } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refreshAccounts = useCallback(async () => {
    try {
      const r = await callTool("account_list", {});
      setAccounts(r.accounts || []);
    } catch (e: any) {
      setError(e.message);
    }
  }, []);

  useEffect(() => {
    refreshAccounts();
    // After OAuth callback the platform 302s us back with ?pending=N;
    // pick that up and show the picker without a manual click.
    const pending = new URLSearchParams(window.location.search).get("pending");
    if (pending) {
      handleResumeFromOAuth(Number(pending));
    }
  }, [refreshAccounts]);

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

  const handleAddAccount = async () => {
    try {
      const r = await callTool("account_add", { platform: "meta" });
      if (r.authorize_url) {
        window.open(r.authorize_url, "_blank");
      } else if (r.pending_account_id) {
        // Reused an existing connection — go straight to picker.
        await handleResumeFromOAuth(r.pending_account_id);
      }
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleResumeFromOAuth = async (pendingId: number) => {
    try {
      const r = await callTool("account_list_pending_pages", { pending_account_id: pendingId });
      setPendingPicker({ pendingId, pages: r.pages || [] });
    } catch (e: any) {
      setError(e.message);
    }
  };

  const handleFinalize = async (page: PendingAccountPage) => {
    if (!pendingPicker) return;
    try {
      await callTool("account_finalize", {
        pending_account_id: pendingPicker.pendingId,
        page_id: page.id,
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
      await callTool("account_disconnect", { id: acct.id });
      if (selected?.id === acct.id) setSelected(null);
      await refreshAccounts();
    } catch (e: any) {
      setError(e.message);
    }
  };

  return (
    <div style={{ padding: 24, fontFamily: "system-ui, sans-serif" }}>
      <header style={{ display: "flex", alignItems: "center", marginBottom: 24 }}>
        <h1 style={{ margin: 0, fontSize: 22, fontWeight: 600 }}>Ads</h1>
        <button
          onClick={handleAddAccount}
          style={{ marginLeft: "auto", padding: "8px 14px", borderRadius: 6, border: "1px solid #333", background: "#000", color: "#fff", cursor: "pointer" }}
        >
          + Add Account
        </button>
      </header>

      {error && (
        <div style={{ padding: 12, marginBottom: 16, background: "#fee", border: "1px solid #f99", borderRadius: 6 }}>
          {error}
          <button onClick={() => setError(null)} style={{ float: "right", background: "transparent", border: 0, cursor: "pointer" }}>×</button>
        </div>
      )}

      {pendingPicker && (
        <div style={{ padding: 16, marginBottom: 16, border: "1px solid #ccc", borderRadius: 6, background: "#fafafa" }}>
          <h3 style={{ marginTop: 0 }}>Pick an ad account</h3>
          {pendingPicker.pages.length === 0 ? (
            <p style={{ color: "#666" }}>No ad accounts found on this connection.</p>
          ) : (
            <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
              {pendingPicker.pages.map(p => (
                <li key={p.id} style={{ padding: 10, borderBottom: "1px solid #eee", display: "flex", alignItems: "center" }}>
                  <div style={{ flex: 1 }}>
                    <div style={{ fontWeight: 500 }}>{p.name || p.id}</div>
                    <div style={{ fontSize: 12, color: "#666" }}>
                      {p.id} · {p.currency} · {p.timezone}
                    </div>
                  </div>
                  <button onClick={() => handleFinalize(p)} style={{ padding: "6px 12px", borderRadius: 4, border: "1px solid #333", cursor: "pointer" }}>
                    Use this account
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div style={{ display: "grid", gridTemplateColumns: "300px 1fr", gap: 24 }}>
        <aside>
          <h3 style={{ marginTop: 0, fontSize: 14, textTransform: "uppercase", color: "#666" }}>Connected accounts</h3>
          {accounts.length === 0 ? (
            <p style={{ color: "#999", fontSize: 14 }}>No accounts connected yet. Click "Add Account" to start.</p>
          ) : (
            <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
              {accounts.map(a => (
                <li
                  key={a.id}
                  onClick={() => handleSelect(a)}
                  style={{
                    padding: 10,
                    borderRadius: 6,
                    cursor: "pointer",
                    background: selected?.id === a.id ? "#eef" : "transparent",
                    border: "1px solid " + (selected?.id === a.id ? "#99c" : "transparent"),
                    marginBottom: 4,
                  }}
                >
                  <div style={{ fontWeight: 500 }}>{a.display_name}</div>
                  <div style={{ fontSize: 12, color: "#666" }}>
                    {a.platform} · {a.native_account_id} · {a.currency}
                  </div>
                  <button
                    onClick={(e) => { e.stopPropagation(); handleDisconnect(a); }}
                    style={{ marginTop: 4, fontSize: 11, background: "transparent", border: 0, color: "#c33", cursor: "pointer", padding: 0 }}
                  >
                    Disconnect
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <main>
          {!selected ? (
            <p style={{ color: "#999" }}>Select an ad account on the left to see its campaigns.</p>
          ) : (
            <>
              <h3 style={{ marginTop: 0 }}>{selected.display_name} · Campaigns</h3>
              {loading && <p>Loading…</p>}
              {!loading && campaigns.length === 0 && (
                <p style={{ color: "#999" }}>No campaigns on this account yet. Ask an agent to create one via campaign_create.</p>
              )}
              {!loading && campaigns.length > 0 && (
                <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 14 }}>
                  <thead>
                    <tr style={{ borderBottom: "1px solid #ddd", textAlign: "left" }}>
                      <th style={{ padding: 8 }}>Name</th>
                      <th style={{ padding: 8 }}>Objective</th>
                      <th style={{ padding: 8 }}>Status</th>
                      <th style={{ padding: 8 }}>Daily budget</th>
                      <th style={{ padding: 8 }}></th>
                    </tr>
                  </thead>
                  <tbody>
                    {campaigns.map(c => (
                      <tr key={c.id} style={{ borderBottom: "1px solid #eee" }}>
                        <td style={{ padding: 8 }}>{c.name}</td>
                        <td style={{ padding: 8, color: "#666" }}>{c.objective}</td>
                        <td style={{ padding: 8 }}>
                          <span style={{
                            padding: "2px 8px",
                            borderRadius: 12,
                            fontSize: 12,
                            background: c.status === "ACTIVE" ? "#cfc" : "#eee",
                            color: c.status === "ACTIVE" ? "#060" : "#666",
                          }}>
                            {c.status}
                          </span>
                        </td>
                        <td style={{ padding: 8, color: "#666" }}>
                          {c.daily_budget ? `$${(Number(c.daily_budget) / 100).toFixed(2)}` : "—"}
                        </td>
                        <td style={{ padding: 8, textAlign: "right" }}>
                          <button onClick={() => handleStatusToggle(c)} style={{ padding: "4px 10px", borderRadius: 4, border: "1px solid #ccc", cursor: "pointer" }}>
                            {c.status === "ACTIVE" ? "Pause" : "Resume"}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </>
          )}
        </main>
      </div>
    </div>
  );
}
