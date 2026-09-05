import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type Invoice} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";
import {fmtMoney} from "../shared";

export function VoidInvoiceModal({ invoice, onConfirm, onClose, }: {
    invoice: Invoice;
    onConfirm: (reason: string) => Promise<void>;
    onClose: () => void;
}) {
    const [reason, setReason] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const submit = async () => {
        setError("");
        setBusy(true);
        try {
            await onConfirm(reason.trim());
            onClose();
        }
        catch (err) {
            setError((err as Error).message);
        }
        finally {
            setBusy(false);
        }
    };
    return (<div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6" onClick={onClose}>
      <div className="bg-bg border border-border rounded-lg w-full" style={{ maxWidth: "480px" }} onClick={(e) => e.stopPropagation()}>
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">Void invoice?</h2>
          <ModalCloseButton onClose={onClose}/>
        </header>
        <div className="p-4 text-sm text-text space-y-3">
          <p className="text-text-muted">
            {invoice.number || `#${invoice.id}`} ·{" "}
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </p>
          <p>
            Voiding is permanent. The invoice will be marked{" "}
            <strong>void</strong> and excluded from open / outstanding totals.
            Recorded payments stay on the audit log but won't be reversed.
          </p>
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Reason (optional, kept in the audit log)
            </label>
            <textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={3} placeholder="Duplicate of INV-…, customer cancelled, billing error, …" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" autoFocus/>
          </div>
        </div>
        {error && (<div className="px-4 pb-2 text-sm text-red">{error}</div>)}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={busy} className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 disabled:opacity-50">
            {busy ? "Voiding…" : "Void invoice"}
          </button>
        </footer>
      </div>
    </div>);
}
