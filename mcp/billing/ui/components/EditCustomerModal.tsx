import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Customer} from "../shared";
import {type TaxIdDraft} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";

export // ─── Edit customer modal ────────────────────────────────────────────
//
// Mirrors CreateCustomerModal but pre-populates from the existing
// customer and PATCHes /customers/{id}. PATCH applies every field
// the user changed (we always send the full field set; absent values
// become empty strings server-side, which the update handler ignores
// only when the value is unchanged from the existing row — see
// dbCustomerUpdate). Same field surface: identity / address / tax IDs
// / contact / bank / notes.
function EditCustomerModal({ apiCall, customer, onClose, onUpdated, }: {
    apiCall: ApiCall;
    customer: Customer;
    onClose: () => void;
    onUpdated: (customer: Customer) => void;
}) {
    const initialBank = customer.metadata?.bank || {};
    const initialContact = customer.metadata?.contact || {};
    const initialAddress = customer.billing_address || {};
    const [name, setName] = useState(customer.name || "");
    const [email, setEmail] = useState(customer.email || "");
    const [phone, setPhone] = useState(customer.phone || "");
    const [currency, setCurrency] = useState(customer.currency || "");
    const [line1, setLine1] = useState(initialAddress.line1 || "");
    const [line2, setLine2] = useState(initialAddress.line2 || "");
    const [city, setCity] = useState(initialAddress.city || "");
    const [region, setRegion] = useState(initialAddress.state || "");
    const [postalCode, setPostalCode] = useState(initialAddress.postal_code || "");
    const [country, setCountry] = useState(initialAddress.country || "");
    const [taxIds, setTaxIds] = useState<TaxIdDraft[]>(customer.tax_ids && customer.tax_ids.length > 0
        ? customer.tax_ids.map((t) => ({ type: t.type, value: t.value }))
        : [{ type: "vat", value: "" }]);
    const [contactName, setContactName] = useState(initialContact.name || "");
    const [contactTitle, setContactTitle] = useState(initialContact.title || "");
    const [website, setWebsite] = useState((customer.metadata?.website as string) || "");
    const [iban, setIban] = useState(initialBank.iban || "");
    const [bic, setBic] = useState(initialBank.bic || "");
    const [bankName, setBankName] = useState(initialBank.bank_name || "");
    const [bankCode, setBankCode] = useState(initialBank.bank_code || "");
    const [notes, setNotes] = useState((customer.metadata?.notes as string) || "");
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState("");
    const setTaxId = (i: number, patch: Partial<TaxIdDraft>) => setTaxIds((prev) => prev.map((t, j) => (j === i ? { ...t, ...patch } : t)));
    const addTaxId = () => setTaxIds((prev) => [...prev, { type: "vat", value: "" }]);
    const removeTaxId = (i: number) => setTaxIds((prev) => prev.length <= 1 ? prev : prev.filter((_, j) => j !== i));
    const submit = async () => {
        setError("");
        const cleanName = name.trim();
        const cleanEmail = email.trim();
        if (!cleanName) {
            setError("Name is required.");
            return;
        }
        if (!cleanEmail) {
            setError("Email is required.");
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
        const metadata: Record<string, unknown> = { ...(customer.metadata || {}) };
        const bank: Record<string, string> = {};
        if (iban.trim())
            bank.iban = iban.trim().toUpperCase().replace(/\s+/g, "");
        if (bic.trim())
            bank.bic = bic.trim().toUpperCase().replace(/\s+/g, "");
        if (bankName.trim())
            bank.bank_name = bankName.trim();
        if (bankCode.trim())
            bank.bank_code = bankCode.trim();
        if (Object.keys(bank).length > 0) {
            metadata.bank = bank;
        }
        else {
            delete metadata.bank;
        }
        const contact: Record<string, string> = {};
        if (contactName.trim())
            contact.name = contactName.trim();
        if (contactTitle.trim())
            contact.title = contactTitle.trim();
        if (Object.keys(contact).length > 0) {
            metadata.contact = contact;
        }
        else {
            delete metadata.contact;
        }
        if (website.trim()) {
            metadata.website = website.trim();
        }
        else {
            delete metadata.website;
        }
        if (notes.trim()) {
            metadata.notes = notes.trim();
        }
        else {
            delete metadata.notes;
        }
        // PATCH /customers/{id} accepts {name, email, phone, currency,
        // billing_address, tax_ids, metadata} — send all, server overwrites
        // each field with what we provide.
        const body: Record<string, unknown> = {
            name: cleanName,
            email: cleanEmail,
            phone: phone.trim(),
            currency: currency.trim().toUpperCase(),
            billing_address: billingAddress,
            tax_ids: cleanTaxIds,
            metadata,
        };
        setSubmitting(true);
        try {
            const res = await apiCall<{
                customer: Customer;
            }>("PATCH", `/customers/${customer.id}`, body);
            onUpdated(res.customer);
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
          <h2 className="text-text font-semibold">Edit customer</h2>
          <ModalCloseButton onClose={onClose}/>
        </header>

        <div className="p-4 space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Identity
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Name or company *" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
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

          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <footer className="p-4 border-t border-border flex items-center justify-end gap-2 sticky bottom-0 bg-bg">
          <button type="button" onClick={onClose} disabled={submitting} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={submitting} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {submitting ? "Saving…" : "Save changes"}
          </button>
        </footer>
      </div>
    </div>);
}
