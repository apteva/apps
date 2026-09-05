import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type Invoice} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";
import {fmtMoney} from "../shared";

export // ─── Invoice action modals ──────────────────────────────────────────
function FinalizeConfirmModal({ invoice, onConfirm, onClose, }: {
    invoice: Invoice;
    onConfirm: () => Promise<void>;
    onClose: () => void;
}) {
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const submit = async () => {
        setError("");
        setBusy(true);
        try {
            await onConfirm();
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
      <div className="bg-bg border border-border rounded-lg w-full" style={{ maxWidth: "440px" }} onClick={(e) => e.stopPropagation()}>
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">Finalize this draft?</h2>
          <ModalCloseButton onClose={onClose}/>
        </header>
        <div className="p-4 text-sm text-text space-y-2">
          <p>
            An invoice number will be minted and the invoice transitions from{" "}
            <strong>draft</strong> to <strong>open</strong>. Line items can no
            longer be added or edited after this.
          </p>
          <p className="text-text-muted">
            {invoice.number || `Draft #${invoice.id}`} ·{" "}
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </p>
        </div>
        {error && (<div className="px-4 pb-2 text-sm text-red">{error}</div>)}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={busy} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {busy ? "Finalizing…" : "Finalize"}
          </button>
        </footer>
      </div>
    </div>);
}
