import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type NativePanelProps} from "../shared";
import {API} from "../shared";
import {InvoicesTab} from "./InvoicesTab";
import {CustomersTab} from "./CustomersTab";
import {PaymentMethodsTab} from "./PaymentMethodsTab";
import {SettingsTab} from "./SettingsTab";

export function BillingWorkspace({ projectId, installId }: NativePanelProps) {
    const [tab, setTab] = useState<"invoices" | "customers" | "payment_methods" | "settings">("invoices");
    const queryString = useCallback((extra: Record<string, string> = {}) => new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
    }).toString(), [projectId, installId]);
    const apiCall = useCallback(async <T,>(method: string, path: string, body?: unknown, query: Record<string, string> = {}): Promise<T> => {
        const r = await fetch(`${API}${path}?${queryString(query)}`, {
            method,
            credentials: "same-origin",
            cache: "no-store", // never serve a cached list/detail after a mutation
            headers: body ? { "Content-Type": "application/json" } : {},
            body: body ? JSON.stringify(body) : undefined,
        });
        if (!r.ok)
            throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        return r.json() as Promise<T>;
    }, [queryString]);
    return (<div className="h-full flex flex-col">
      <nav className="flex gap-2 p-2 border-b border-border text-sm">
        <button type="button" onClick={() => setTab("invoices")} className={`px-3 py-1 rounded ${tab === "invoices" ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}>
          Invoices
        </button>
        <button type="button" onClick={() => setTab("customers")} className={`px-3 py-1 rounded ${tab === "customers" ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}>
          Customers
        </button>
        <button type="button" onClick={() => setTab("payment_methods")} className={`px-3 py-1 rounded ${tab === "payment_methods" ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}>
          Payment methods
        </button>
        <button type="button" onClick={() => setTab("settings")} className={`px-3 py-1 rounded ml-auto ${tab === "settings" ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}>
          Settings
        </button>
      </nav>

      <div className="flex-1 overflow-hidden">
        {tab === "invoices" && (<InvoicesTab installId={installId} projectId={projectId} apiCall={apiCall}/>)}
        {tab === "customers" && (<CustomersTab projectId={projectId} apiCall={apiCall}/>)}
        {tab === "payment_methods" && (<PaymentMethodsTab projectId={projectId} apiCall={apiCall}/>)}
        {tab === "settings" && <SettingsTab apiCall={apiCall}/>}
      </div>
    </div>);
}
