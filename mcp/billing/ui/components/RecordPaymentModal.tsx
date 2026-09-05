import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type Invoice} from "../shared";
import {roundMoney} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";
import {fmtMoney} from "../shared";

export function RecordPaymentModal({ invoice, onConfirm, onClose, }: {
    invoice: Invoice;
    onConfirm: (amountCents: number, method: string, notes: string, receivedAt: string) => Promise<void>;
    onClose: () => void;
}) {
    const outstandingCents = Math.max(0, invoice.total_cents - invoice.amount_paid_cents);
    const outstandingDecimal = (outstandingCents / 100).toFixed(2);
    const [receivedAt, setReceivedAt] = useState("");
    const [amount, setAmount] = useState(outstandingDecimal);
    const [method, setMethod] = useState("wire");
    const [notes, setNotes] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const submit = async () => {
        setError("");
        const value = parseFloat(amount);
        if (!isFinite(value) || value === 0) {
            setError("Amount must be a non-zero number.");
            return;
        }
        const cents = roundMoney(value * 100);
        setBusy(true);
        try {
            await onConfirm(cents, method, notes.trim(), receivedAt);
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
          <h2 className="text-text font-semibold">Record payment</h2>
          <ModalCloseButton onClose={onClose}/>
        </header>
        <div className="p-4 space-y-3 text-sm">
          <div className="flex items-center justify-between text-text-muted">
            <span>{invoice.number || `#${invoice.id}`}</span>
            <span>
              Outstanding{" "}
              <span className="text-text">
                {fmtMoney(outstandingCents, invoice.currency)}
              </span>
            </span>
          </div>

          <label>Received at (leave blank for now)<input type="datetime-local" value={receivedAt} onChange={e => setReceivedAt(e.target.value)} className="block bg-bg-input border border-border p-1"/></label>
 <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Amount ({invoice.currency})
              </label>
              <input type="number" step="0.01" value={amount} onChange={(e) => setAmount(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" autoFocus/>
              <p className="text-xs text-text-dim mt-1">
                Use a negative number for a refund record.
              </p>
            </div>
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Method
              </label>
              <select value={method} onChange={(e) => setMethod(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm">
                <option value="wire">Wire</option>
                <option value="cash">Cash</option>
                <option value="check">Check</option>
                <option value="other">Other</option>
              </select>
            </div>
          </div>

          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Notes (optional)
            </label>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={2} placeholder="Transaction reference, payer name, …" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>
        </div>
        {error && (<div className="px-4 pb-2 text-sm text-red">{error}</div>)}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={busy} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {busy ? "Recording…" : "Record payment"}
          </button>
        </footer>
      </div>
    </div>);
}
