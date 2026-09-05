import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type PaymentMethod} from "../shared";
import {useAppEvents} from "../shared";
import {paymentMethodLabel} from "../shared";
import {fmtDateTime} from "../shared";
import {PaymentMethodSetupModal} from "./PaymentMethodSetupModal";

export function PaymentMethodsTab({ projectId, apiCall, }: {
    projectId: string;
    apiCall: ApiCall;
}) {
    const [methods, setMethods] = useState<PaymentMethod[]>([]);
    const [status, setStatus] = useState("");
    const [showSetup, setShowSetup] = useState(false);
    const load = useCallback(async () => {
        setStatus("Loading…");
        try {
            const res = await apiCall<{
                payment_methods: PaymentMethod[];
            }>("GET", "/payment-methods", undefined, { limit: "200" });
            setMethods(res.payment_methods || []);
            setStatus(`${(res.payment_methods || []).length} payment method${(res.payment_methods || []).length === 1 ? "" : "s"}`);
        }
        catch (err) {
            setStatus(`Error: ${(err as Error).message}`);
        }
    }, [apiCall]);
    useEffect(() => {
        load();
    }, [load]);
    useAppEvents("billing", projectId, (ev) => {
        if (ev.topic.startsWith("payment_method.") || ev.topic.startsWith("customer.")) {
            load();
        }
    });
    const setAsDefault = async (pm: PaymentMethod) => {
        setStatus("Updating default…");
        try {
            await apiCall("POST", `/payment-methods/${pm.id}/default`);
            await load();
        }
        catch (err) {
            setStatus(`Default update failed: ${(err as Error).message}`);
        }
    };
    const detach = async (pm: PaymentMethod) => {
        if (!window.confirm(`Detach ${paymentMethodLabel(pm)}?`))
            return;
        setStatus("Detaching…");
        try {
            await apiCall("POST", `/payment-methods/${pm.id}/detach`);
            await load();
        }
        catch (err) {
            setStatus(`Detach failed: ${(err as Error).message}`);
        }
    };
    return (<div className="h-full overflow-auto">
      <div className="p-4 border-b border-border space-y-3">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-lg font-semibold text-text">Payment methods</h1>
            <p className="text-sm text-text-muted">
              Reusable customer payment methods saved through the processor.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button type="button" onClick={() => setShowSetup(true)} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              + Setup link
            </button>
            <button type="button" onClick={load} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
              Refresh
            </button>
          </div>
        </div>
        <div className="text-xs text-text-muted">{status}</div>
      </div>

      <table className="w-full text-sm">
        <thead className="sticky top-0 bg-bg border-b border-border text-xs uppercase text-text-dim">
          <tr>
            <th className="text-left p-2">Customer</th>
            <th className="text-left p-2">Method</th>
            <th className="text-left p-2">Type</th>
            <th className="text-left p-2">Status</th>
            <th className="text-left p-2">Created</th>
            <th className="text-right p-2">Actions</th>
          </tr>
        </thead>
        <tbody>
          {methods.map((pm) => (<tr key={pm.id} className="border-b border-border/60 hover:bg-bg-input/30">
              <td className="p-2">
                <div className="text-text">{pm.customer_name || `Customer #${pm.customer_id}`}</div>
                <div className="text-xs text-text-dim">{pm.customer_email || `#${pm.customer_id}`}</div>
              </td>
              <td className="p-2">
                <div className="text-text">{paymentMethodLabel(pm)}</div>
                <div className="text-xs text-text-dim break-all">
                  {pm.provider_payment_method_id}
                </div>
              </td>
              <td className="p-2">
                <div className="text-text">{pm.type}</div>
                {pm.delayed_notification && (<div className="text-xs text-yellow-500">Delayed confirmation</div>)}
              </td>
              <td className="p-2">
                <span className="inline-flex items-center rounded border border-border px-2 py-0.5 text-xs">
                  {pm.status}
                </span>
                {pm.is_default && (<span className="ml-2 inline-flex items-center rounded border border-accent text-accent px-2 py-0.5 text-xs">
                    Default
                  </span>)}
              </td>
              <td className="p-2 text-text-muted">{fmtDateTime(pm.created_at)}</td>
              <td className="p-2">
                <div className="flex justify-end gap-2">
                  {!pm.is_default && pm.status === "active" && (<button type="button" onClick={() => setAsDefault(pm)} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
                      Set default
                    </button>)}
                  {pm.status === "active" && (<button type="button" onClick={() => detach(pm)} className="px-2 py-1 text-xs border border-red text-red rounded hover:bg-red hover:text-bg">
                      Detach
                    </button>)}
                </div>
              </td>
            </tr>))}
          {methods.length === 0 && (<tr>
              <td colSpan={6} className="p-6 text-center text-text-muted">
                No payment methods yet.
              </td>
            </tr>)}
        </tbody>
      </table>
      {showSetup && (<PaymentMethodSetupModal apiCall={apiCall} onClose={() => setShowSetup(false)} onCreated={async (message) => {
                setShowSetup(false);
                setStatus(message);
                await load();
            }}/>)}
    </div>);
}
