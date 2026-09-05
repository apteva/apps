import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Customer} from "../shared";
import {type TaxIdDraft} from "../shared";

export function CreateCustomerModal({ apiCall, onClose, onCreated, }: {
    apiCall: ApiCall;
    onClose: () => void;
    onCreated: (customer: Customer) => void;
}) {
    const [name, setName] = useState("");
    const [email, setEmail] = useState("");
    const [phone, setPhone] = useState("");
    const [currency, setCurrency] = useState("");
    const [line1, setLine1] = useState("");
    const [line2, setLine2] = useState("");
    const [city, setCity] = useState("");
    const [region, setRegion] = useState("");
    const [postalCode, setPostalCode] = useState("");
    const [country, setCountry] = useState("");
    const [taxIds, setTaxIds] = useState<TaxIdDraft[]>([
        { type: "vat", value: "" },
    ]);
    const [contactName, setContactName] = useState("");
    const [contactTitle, setContactTitle] = useState("");
    const [website, setWebsite] = useState("");
    const [iban, setIban] = useState("");
    const [bic, setBic] = useState("");
    const [bankName, setBankName] = useState("");
    const [bankCode, setBankCode] = useState("");
    const [notes, setNotes] = useState("");
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState("");
    const [warning, setWarning] = useState("");
    const setTaxId = (i: number, patch: Partial<TaxIdDraft>) => setTaxIds((prev) => prev.map((t, j) => (j === i ? { ...t, ...patch } : t)));
    const addTaxId = () => setTaxIds((prev) => [...prev, { type: "vat", value: "" }]);
    const removeTaxId = (i: number) => setTaxIds((prev) => prev.length <= 1 ? prev : prev.filter((_, j) => j !== i));
    const submit = async () => {
        setError("");
        setWarning("");
        const cleanEmail = email.trim();
        const cleanName = name.trim();
        if (!cleanEmail) {
            setError("Email is required.");
            return;
        }
        if (!cleanName) {
            setError("Name is required.");
            return;
        }
        const billingAddress: Record<string, string> = {};
        if (line1.trim())
            billingAddress.line1 = line1.trim();
        if (line2.trim())
            billingAddress.line2 = line2.trim();
        if (postalCode.trim())
            billingAddress.postal_code = postalCode.trim();
        if (city.trim())
            billingAddress.city = city.trim();
        if (region.trim())
            billingAddress.state = region.trim();
        if (country.trim())
            billingAddress.country = country.trim().toUpperCase();
        const cleanTaxIds = taxIds
            .map((t) => ({ type: t.type.trim(), value: t.value.trim() }))
            .filter((t) => t.type && t.value);
        const metadata: Record<string, unknown> = {};
        const bank: Record<string, string> = {};
        if (iban.trim())
            bank.iban = iban.trim().toUpperCase().replace(/\s+/g, "");
        if (bic.trim())
            bank.bic = bic.trim().toUpperCase().replace(/\s+/g, "");
        if (bankName.trim())
            bank.name = bankName.trim();
        if (bankCode.trim())
            bank.code = bankCode.trim();
        if (Object.keys(bank).length > 0)
            metadata.bank = bank;
        const contact: Record<string, string> = {};
        if (contactName.trim())
            contact.name = contactName.trim();
        if (contactTitle.trim())
            contact.title = contactTitle.trim();
        if (Object.keys(contact).length > 0)
            metadata.contact = contact;
        if (website.trim())
            metadata.website = website.trim();
        if (notes.trim())
            metadata.notes = notes.trim();
        const defaults: Record<string, unknown> = { name: cleanName };
        if (phone.trim())
            defaults.phone = phone.trim();
        if (currency.trim())
            defaults.currency = currency.trim().toUpperCase();
        if (Object.keys(billingAddress).length > 0)
            defaults.billing_address = billingAddress;
        if (cleanTaxIds.length > 0)
            defaults.tax_ids = cleanTaxIds;
        if (Object.keys(metadata).length > 0)
            defaults.metadata = metadata;
        setSubmitting(true);
        try {
            const res = await apiCall<{
                customer: Customer;
                was_created: boolean;
            }>("POST", "/customers", { email: cleanEmail, defaults });
            if (res.was_created === false) {
                // Upsert-by-email returns the existing row and ignores defaults.
                // Surface that instead of silently dropping the user's entries.
                setWarning("A customer with this email already exists. Opening the existing record — your new field values were NOT applied.");
                setTimeout(() => onCreated(res.customer), 1500);
                return;
            }
            onCreated(res.customer);
        }
        catch (err) {
            setError((err as Error).message);
        }
        finally {
            setSubmitting(false);
        }
    };
    return (<div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6" onClick={onClose}>
      <div className="bg-bg border border-border rounded-lg w-full overflow-auto" style={{ maxWidth: "640px", maxHeight: "90vh" }} onClick={(e) => e.stopPropagation()}>
        <header className="p-4 border-b border-border flex items-center justify-between sticky top-0 bg-bg z-10">
          <h2 className="text-text font-semibold">New customer</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <path d="M4 4 L12 12"/>
              <path d="M12 4 L4 12"/>
            </svg>
          </button>
        </header>

        <div className="p-4 space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Identity
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Name or company *" className="bg-bg-input border border-border rounded px-2 py-1 text-sm" autoFocus/>
              <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email *" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="Phone" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase().slice(0, 3))} placeholder="Default currency (e.g. EUR)" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Billing address
            </h3>
            <input type="text" value={line1} onChange={(e) => setLine1(e.target.value)} placeholder="Street address" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={line2} onChange={(e) => setLine2(e.target.value)} placeholder="Address line 2 (optional)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <div className="grid grid-cols-3 gap-2">
              <input type="text" value={postalCode} onChange={(e) => setPostalCode(e.target.value)} placeholder="Postal code" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={city} onChange={(e) => setCity(e.target.value)} placeholder="City" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={region} onChange={(e) => setRegion(e.target.value)} placeholder="State / region" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
            <input type="text" value={country} onChange={(e) => setCountry(e.target.value.toUpperCase().slice(0, 2))} placeholder="Country (2-char ISO, e.g. FR, EE, US)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </section>

          <section className="space-y-2">
            <div className="flex items-center justify-between">
              <h3 className="text-xs uppercase tracking-wide text-text-dim">
                Tax IDs
              </h3>
              <button type="button" onClick={addTaxId} className="text-xs text-accent hover:underline">
                + Add ID
              </button>
            </div>
            {taxIds.map((t, i) => (<div key={i} className="flex items-center gap-2">
                <select value={t.type} onChange={(e) => setTaxId(i, { type: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-1 text-sm">
                  <option value="vat">VAT</option>
                  <option value="ein">EIN</option>
                  <option value="gst">GST</option>
                  <option value="abn">ABN</option>
                  <option value="company_reg">Company reg.</option>
                  <option value="siret">SIRET</option>
                  <option value="other">Other</option>
                </select>
                <input type="text" value={t.value} onChange={(e) => setTaxId(i, { value: e.target.value })} placeholder="Value" className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
                {taxIds.length > 1 && (<button type="button" onClick={() => removeTaxId(i)} aria-label="Remove tax ID" className="text-text-muted hover:text-red">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                      <path d="M4 4 L12 12"/>
                      <path d="M12 4 L4 12"/>
                    </svg>
                  </button>)}
              </div>))}
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Contact (optional)
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input type="text" value={contactName} onChange={(e) => setContactName(e.target.value)} placeholder="Contact name" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={contactTitle} onChange={(e) => setContactTitle(e.target.value)} placeholder="Title" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
            <input type="text" value={website} onChange={(e) => setWebsite(e.target.value)} placeholder="Website" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Bank details (optional)
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input type="text" value={iban} onChange={(e) => setIban(e.target.value)} placeholder="IBAN" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={bic} onChange={(e) => setBic(e.target.value)} placeholder="BIC / SWIFT" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={bankName} onChange={(e) => setBankName(e.target.value)} placeholder="Bank name" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              <input type="text" value={bankCode} onChange={(e) => setBankCode(e.target.value)} placeholder="Bank code" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Notes
            </h3>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={2} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </section>

          {warning && (<div className="text-sm text-yellow-500 border border-yellow-500/30 bg-yellow-500/10 rounded px-2 py-1">
              {warning}
            </div>)}
          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <footer className="p-4 border-t border-border flex items-center justify-end gap-2 sticky bottom-0 bg-bg">
          <button type="button" onClick={onClose} disabled={submitting} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={submitting} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {submitting ? "Creating…" : "Create customer"}
          </button>
        </footer>
      </div>
    </div>);
}
