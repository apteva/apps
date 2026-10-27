import { scopedAppURL } from "./panelScope";
import { mcpEnvelopeError } from "./accountFlow";
export interface HistorySyncReceipt {
  status: "partial" | "failed" | "complete" | "limited" | "unsupported";
  imported: number; updated: number; unchanged: number; unique_posts: number;
  observed_at: string; continuation_cursor: string | null;
  coverage?: { complete: boolean; scope: string };
  limitations?: string[]; error?: string; limitation?: string;
}
// Explicit UI action only. Persist the opaque Social cursor across navigation;
// never store provider tokens or change automatic account sync settings.
export async function syncAccountHistory(accountId: number, projectId?: string | null, progress?: (receipt: HistorySyncReceipt) => void): Promise<HistorySyncReceipt> {
  const key = `social:history:${projectId || ""}:${accountId}`;
  const storage = typeof window !== "undefined" ? window.sessionStorage : undefined;
  let cursor = storage?.getItem(key) || undefined;
  let receipt: HistorySyncReceipt | undefined;
  for (let batch = 0; batch < 5; batch++) {
    const response = await fetch(scopedAppURL("/api/apps/social", `/accounts/${accountId}/history-sync`, projectId), {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: "full", cursor, max_pages: 2 }),
    });
    if (!response.ok) throw new Error(await response.text());
    const data = await response.json();
    const issue = mcpEnvelopeError(data); if (issue) { if (/cursor|connection changed/i.test(issue)) storage?.removeItem(key); throw new Error(issue); }
    receipt = data as HistorySyncReceipt;
    if (receipt.continuation_cursor) storage?.setItem(key, receipt.continuation_cursor);
    else storage?.removeItem(key);
    progress?.(receipt);
    if (receipt.status === "failed") throw new Error(receipt.error || "History sync failed; retry to resume.");
    if (!receipt.continuation_cursor) return receipt;
    cursor = receipt.continuation_cursor;
  }
  return receipt!;
}
export function historySyncMessage(receipt: HistorySyncReceipt): string {
  const counts = `${receipt.imported || 0} imported; ${receipt.updated || 0} updated`;
  if (receipt.continuation_cursor) return `${counts}. History sync paused; click Sync posts again to continue.`;
  if (receipt.coverage?.complete) return `${counts}. Accessible history synced as of ${receipt.observed_at}. ${receipt.limitations?.join(" ") || ""}`.trim();
  return `${counts}. Coverage is limited. ${receipt.limitation || receipt.limitations?.join(" ") || "Check account history status."}`;
}
