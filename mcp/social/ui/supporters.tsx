import { useEffect, useRef, useState } from "react";
import { scopedAppURL } from "./panelScope";
import { mcpEnvelopeError } from "./accountFlow";

export interface SupporterLevel { id: string; name: string; count: number }
export interface SupporterSnapshot {
  social_account_id: number;
  platform: string;
  state: "available" | "access_required" | "scope_missing" | "unsupported" | "error" | string;
  reason?: string;
  source?: string;
  observed_at?: string;
  total: number | null;
  provider_total?: number | null;
  totals_agree?: boolean | null;
  complete: boolean;
  levels: SupporterLevel[];
}

const STATE_LABELS: Record<string, string> = {
  access_required: "Access required",
  scope_missing: "Reconnect needed",
  unsupported: "Not available",
  error: "Could not read",
};

async function request(path: string, projectId?: string | null, init?: RequestInit): Promise<any> {
  const response = await fetch(scopedAppURL("/api/apps/social", path, projectId), { credentials: "same-origin", ...init });
  const data = await response.json().catch(() => ({}));
  const envelope = mcpEnvelopeError(data);
  if (!response.ok || envelope) throw new Error(envelope || data?.error || `Request failed (${response.status})`);
  return data;
}

// Paid supporters for one account: the count per level when available,
// otherwise the state and the next step. An unknown count is never shown as 0.
export function SupportersCard({ accountId, projectId }: { accountId: number; projectId?: string | null }) {
  const [snap, setSnap] = useState<SupporterSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = useRef(accountId);
  current.current = accountId;

  const load = async (refresh: boolean) => {
    const id = accountId;
    setBusy(true);
    setError("");
    try {
      const data = await request(`/supporters/accounts/${id}${refresh ? "?refresh=true" : ""}`, projectId);
      if (current.current === id) setSnap(data);
    } catch (e: any) {
      if (current.current === id) setError(e?.message || "Supporters request failed");
    } finally {
      if (current.current === id) setBusy(false);
    }
  };

  useEffect(() => {
    setSnap(null);
    load(false);
  }, [accountId, projectId]);

  if (!snap && !error) return <div className="text-text-dim text-sm">Loading supporters…</div>;
  if (snap?.state === "unsupported" && !snap.reason?.includes("integration")) return null;

  const available = snap?.state === "available" && snap.total != null;
  return (
    <section className="border border-border rounded px-3 py-2 bg-bg flex flex-col gap-2" aria-label="Paid supporters">
      <div className="flex items-center gap-2">
        <div className="text-xs uppercase tracking-wide text-text-dim">Paid supporters</div>
        {snap && !available && <span className="text-xs text-warn">{STATE_LABELS[snap.state] || snap.state}</span>}
        <button type="button" onClick={() => load(true)} disabled={busy} className="ml-auto text-xs text-accent hover:underline disabled:opacity-50">
          {busy ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      {available && (
        <>
          <div className="text-text font-medium">
            {snap!.total!.toLocaleString()}
            {!snap!.complete && <span className="text-text-dim text-xs font-normal"> at least (count stopped early)</span>}
          </div>
          {snap!.levels.length > 0 && (
            <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
              {snap!.levels.map((level) => (
                <div key={level.id} className="border border-border rounded px-2 py-1">
                  <div className="text-text text-sm">{level.count.toLocaleString()}</div>
                  <div className="text-text-dim text-[10px] uppercase tracking-wide">{level.name}</div>
                </div>
              ))}
            </div>
          )}
          {snap!.totals_agree === false && (
            <div className="text-text-dim text-xs">The platform reports {snap!.provider_total?.toLocaleString()}; Social counted {snap!.total!.toLocaleString()} current members.</div>
          )}
        </>
      )}
      {snap && !available && <div className="text-text-dim text-xs">{snap.reason}</div>}
      {snap?.state === "error" && snap.total != null && (
        <div className="text-text text-sm">{snap.total.toLocaleString()} <span className="text-text-dim text-xs">last successful count</span></div>
      )}
      {snap?.observed_at && <div className="text-text-dim text-[10px]">Checked {new Date(snap.observed_at).toLocaleString()}</div>}
      {error && <div className="text-error text-xs">{error}</div>}
    </section>
  );
}
