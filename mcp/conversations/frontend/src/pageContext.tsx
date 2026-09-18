import { useState } from "react";

export interface PageContext {
  version: 1;
  page: "dashboard" | "app" | "agent" | "apps" | "settings";
  project_id: string;
  project_name?: string;
  app?: string;
  installation_id?: number;
  panel?: string;
  viewed_agent_id?: number;
  viewed_agent_name?: string;
  thread_id?: string;
  tab?: string;
}
export function useMessagePageContext(context?: PageContext) {
  const signature = JSON.stringify(context);
  const [dismissed, setDismissed] = useState<string>();
  const active = context && dismissed !== signature ? context : undefined;
  return { context: active, dismiss: () => setDismissed(signature) };
}
export function PageContextChip({ context, onRemove }: { context?: PageContext; onRemove: () => void }) {
  if (!context) return null;
  const label = context.page === "app" ? `${context.app} app`
    : context.page === "agent" ? context.viewed_agent_name || `Agent #${context.viewed_agent_id}`
    : context.page === "settings" ? `Settings${context.tab ? ` · ${context.tab}` : ""}`
    : context.page === "apps" ? `Apps${context.app ? ` · ${context.app}` : ""}` : "Dashboard";
  return <div className="mb-2 flex min-w-0 items-center gap-1 text-xs text-text-muted">
    <span className="truncate" title="Only page identifiers are shared, not page contents or form values.">Using context: {label}</span>
    <button type="button" aria-label="Remove page context" onClick={onRemove} className="flex h-8 w-8 shrink-0 items-center justify-center rounded hover:bg-bg-hover">×</button>
  </div>;
}
