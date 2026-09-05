import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type Invoice} from "../shared";
import {type ApiCall} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";

export // Shared close (X) button — same SVG used by all the modals.
// SendPaymentLinkModal — calls the MCP gateway's invoices_send_payment_link
// tool, shows the resulting Stripe Checkout URL with a copy button.
// Requires the payment_processor integration to be bound; if not,
// the backend returns a clean error which we surface.
function SendPaymentLinkModal({ invoice, apiCall, projectId, onClose, }: {
    invoice: Invoice;
    apiCall: ApiCall;
    projectId: string;
    onClose: () => void;
}) {
    const [phase, setPhase] = useState<"idle" | "loading" | "ready" | "error">("idle");
    const [url, setUrl] = useState("");
    const [error, setError] = useState("");
    const [copied, setCopied] = useState(false);
    const generate = async () => {
        setPhase("loading");
        setError("");
        try {
            // Dashboard MCP gateway lives at /api/apps/<app>/mcp; the
            // standard JSON-RPC envelope:
            const data = await apiCall<{
                result?: {
                    content?: Array<{
                        type: string;
                        text?: string;
                    }>;
                    isError?: boolean;
                };
                error?: {
                    message: string;
                };
            }>("POST", "/mcp", { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "invoices_send_payment_link", arguments: { invoice_id: invoice.id } } });
            if (data.error)
                throw new Error(data.error.message);
            const inner = data.result?.content?.[0]?.text;
            if (!inner)
                throw new Error("No response body from billing");
            const payload = JSON.parse(inner) as {
                url?: string;
                error?: string;
            };
            if (payload.error)
                throw new Error(payload.error);
            if (!payload.url)
                throw new Error("No URL in response");
            setUrl(payload.url);
            setPhase("ready");
        }
        catch (err) {
            setError((err as Error).message);
            setPhase("error");
        }
    };
    useEffect(() => {
        if (phase === "idle")
            generate();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);
    const copy = async () => {
        try {
            await navigator.clipboard.writeText(url);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        }
        catch {
            // some browsers reject clipboard from non-secure contexts
        }
    };
    return (<div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6" onClick={onClose}>
      <div className="bg-bg border border-border rounded-lg w-full" style={{ maxWidth: "520px" }} onClick={(e) => e.stopPropagation()}>
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">
            Stripe payment link for {invoice.number || `#${invoice.id}`}
          </h2>
          <ModalCloseButton onClose={onClose}/>
        </header>
        <div className="p-4 space-y-3 text-sm">
          {phase === "loading" && (<div className="text-text-muted">Generating Stripe Checkout Session…</div>)}
          {phase === "error" && (<>
              <div className="text-red border border-red/30 bg-red/10 rounded px-2 py-1">
                {error}
              </div>
              {/no payment_processor bound/.test(error) && (<div className="text-text-muted text-xs">
                  Bind a Stripe connection in the billing app's settings, then
                  retry. The integration is declared in billing's manifest as
                  the <code>payment_processor</code> role.
                </div>)}
            </>)}
          {phase === "ready" && (<>
              <p className="text-text">
                Share this URL with the customer. Stripe will record the
                payment via webhook on success; the invoice will transition to{" "}
                <strong>paid</strong> automatically.
              </p>
              <div className="flex items-center gap-2">
                <input type="text" value={url} readOnly onFocus={(e) => e.currentTarget.select()} className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"/>
                <button type="button" onClick={copy} className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
                  {copied ? "Copied!" : "Copy"}
                </button>
              </div>
              <p className="text-xs text-text-dim">
                Valid for 24 hours. If the customer doesn't pay in time,
                generate a new link from this same button.
              </p>
            </>)}
        </div>
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          {phase === "error" && (<button type="button" onClick={generate} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              Retry
            </button>)}
          <button type="button" onClick={onClose} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Close
          </button>
        </footer>
      </div>
    </div>);
}
