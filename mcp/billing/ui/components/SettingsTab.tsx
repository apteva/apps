import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type TaxIdDraft} from "../shared";
import {type Issuer} from "../shared";
import {type BillingAddress} from "../shared";
import {type BankCoords} from "../shared";

export function SettingsTab({ apiCall }: {
    apiCall: ApiCall;
}) {
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState("");
    const [displayName, setDisplayName] = useState("");
    const [legalName, setLegalName] = useState("");
    const [email, setEmail] = useState("");
    const [phone, setPhone] = useState("");
    const [website, setWebsite] = useState("");
    const [line1, setLine1] = useState("");
    const [line2, setLine2] = useState("");
    const [postalCode, setPostalCode] = useState("");
    const [city, setCity] = useState("");
    const [region, setRegion] = useState("");
    const [country, setCountry] = useState("");
    const [taxIds, setTaxIds] = useState<TaxIdDraft[]>([
        { type: "vat", value: "" },
    ]);
    const [iban, setIban] = useState("");
    const [bic, setBic] = useState("");
    const [bankName, setBankName] = useState("");
    const [bankCode, setBankCode] = useState("");
    const [beneficiary, setBeneficiary] = useState("");
    const [footerText, setFooterText] = useState("");
    const [defaultTerms, setDefaultTerms] = useState("");
    const [configured, setConfigured] = useState(false);
    const [saving, setSaving] = useState(false);
    const [saveError, setSaveError] = useState("");
    const [savedNotice, setSavedNotice] = useState("");
    // Load current issuer.
    useEffect(() => {
        let cancelled = false;
        (async () => {
            try {
                const res = await apiCall<{
                    issuer: Issuer;
                }>("GET", "/issuer");
                if (cancelled)
                    return;
                const i = res.issuer || {};
                setDisplayName(i.display_name || "");
                setLegalName(i.legal_name || "");
                setEmail(i.email || "");
                setPhone(i.phone || "");
                setWebsite(i.website || "");
                const addr = i.address || {};
                setLine1(addr.line1 || "");
                setLine2(addr.line2 || "");
                setPostalCode(addr.postal_code || "");
                setCity(addr.city || "");
                setRegion(addr.state || "");
                setCountry(addr.country || "");
                const ids = (i.tax_ids && i.tax_ids.length > 0)
                    ? i.tax_ids
                    : [{ type: "vat", value: "" }];
                setTaxIds(ids);
                const bank = i.bank || {};
                setIban(bank.iban || "");
                setBic(bank.bic || "");
                setBankName(bank.bank_name || "");
                setBankCode(bank.bank_code || "");
                setBeneficiary(bank.beneficiary || "");
                setFooterText(i.footer_text || "");
                setDefaultTerms(i.default_terms || "");
                setConfigured(!!i.configured);
            }
            catch (err) {
                if (!cancelled)
                    setLoadError((err as Error).message);
            }
            finally {
                if (!cancelled)
                    setLoading(false);
            }
        })();
        return () => {
            cancelled = true;
        };
    }, [apiCall]);
    const setTaxId = (i: number, patch: Partial<TaxIdDraft>) => setTaxIds((prev) => prev.map((t, j) => (j === i ? { ...t, ...patch } : t)));
    const addTaxId = () => setTaxIds((prev) => [...prev, { type: "vat", value: "" }]);
    const removeTaxId = (i: number) => setTaxIds((prev) => prev.length <= 1 ? prev : prev.filter((_, j) => j !== i));
    const save = async () => {
        setSaveError("");
        setSavedNotice("");
        if (!displayName.trim()) {
            setSaveError("Display name is required.");
            return;
        }
        const address: BillingAddress = {};
        if (line1.trim())
            address.line1 = line1.trim();
        if (line2.trim())
            address.line2 = line2.trim();
        if (postalCode.trim())
            address.postal_code = postalCode.trim();
        if (city.trim())
            address.city = city.trim();
        if (region.trim())
            address.state = region.trim();
        if (country.trim())
            address.country = country.trim().toUpperCase();
        const cleanTaxIds = taxIds
            .map((t) => ({ type: t.type.trim(), value: t.value.trim() }))
            .filter((t) => t.type && t.value);
        const bank: BankCoords = {};
        if (iban.trim())
            bank.iban = iban.trim().toUpperCase().replace(/\s+/g, "");
        if (bic.trim())
            bank.bic = bic.trim().toUpperCase().replace(/\s+/g, "");
        if (bankName.trim())
            bank.bank_name = bankName.trim();
        if (bankCode.trim())
            bank.bank_code = bankCode.trim();
        if (beneficiary.trim())
            bank.beneficiary = beneficiary.trim();
        const body: Record<string, unknown> = { display_name: displayName.trim() };
        if (legalName.trim())
            body.legal_name = legalName.trim();
        if (email.trim())
            body.email = email.trim();
        if (phone.trim())
            body.phone = phone.trim();
        if (website.trim())
            body.website = website.trim();
        if (Object.keys(address).length > 0)
            body.address = address;
        if (cleanTaxIds.length > 0)
            body.tax_ids = cleanTaxIds;
        if (Object.keys(bank).length > 0)
            body.bank = bank;
        if (footerText.trim())
            body.footer_text = footerText.trim();
        if (defaultTerms.trim())
            body.default_terms = defaultTerms.trim();
        setSaving(true);
        try {
            const res = await apiCall<{
                issuer: Issuer;
            }>("PUT", "/issuer", body);
            setConfigured(!!res.issuer.configured);
            setSavedNotice("Saved. Next invoice PDF will use these settings.");
            setTimeout(() => setSavedNotice(""), 3000);
        }
        catch (err) {
            setSaveError((err as Error).message);
        }
        finally {
            setSaving(false);
        }
    };
    if (loading) {
        return (<div className="p-6 text-text-muted text-sm">Loading settings…</div>);
    }
    if (loadError) {
        return (<div className="p-6 text-sm text-red">Load failed: {loadError}</div>);
    }
    return (<div className="h-full overflow-auto">
      <div className="max-w-2xl mx-auto p-6 space-y-6">
        <header>
          <h1 className="text-xl text-text font-semibold">Billing identity</h1>
          <p className="text-text-muted text-sm mt-1">
            The entity that emits invoices — appears as the BILL FROM block on
            every PDF and print page. Singleton across this install.
          </p>
          {!configured && (<div className="mt-2 text-xs text-yellow-500 border border-yellow-500/30 bg-yellow-500/10 rounded px-2 py-1">
              Not configured yet — PDFs currently show a placeholder. Fill in
              the form below and Save.
            </div>)}
        </header>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Identity
          </h2>
          <div className="grid grid-cols-2 gap-2">
            <input type="text" value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Display name *" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={legalName} onChange={(e) => setLegalName(e.target.value)} placeholder="Legal name (registered entity)" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="tel" value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="Phone" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>
          <input type="text" value={website} onChange={(e) => setWebsite(e.target.value)} placeholder="Website" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
        </section>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Address
          </h2>
          <input type="text" value={line1} onChange={(e) => setLine1(e.target.value)} placeholder="Street address" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          <input type="text" value={line2} onChange={(e) => setLine2(e.target.value)} placeholder="Address line 2 (optional)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          <div className="grid grid-cols-3 gap-2">
            <input type="text" value={postalCode} onChange={(e) => setPostalCode(e.target.value)} placeholder="Postal code" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={city} onChange={(e) => setCity(e.target.value)} placeholder="City" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={region} onChange={(e) => setRegion(e.target.value)} placeholder="State / region" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>
          <input type="text" value={country} onChange={(e) => setCountry(e.target.value.toUpperCase().slice(0, 2))} placeholder="Country (2-char ISO, e.g. EE, FR, US)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
        </section>

        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h2 className="text-xs uppercase tracking-wide text-text-dim">
              Tax IDs
            </h2>
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
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Bank details (rendered on PDF as "Pay by bank transfer")
          </h2>
          <input type="text" value={iban} onChange={(e) => setIban(e.target.value)} placeholder="IBAN" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          <div className="grid grid-cols-2 gap-2">
            <input type="text" value={bic} onChange={(e) => setBic(e.target.value)} placeholder="BIC / SWIFT" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={bankCode} onChange={(e) => setBankCode(e.target.value)} placeholder="Bank code" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={bankName} onChange={(e) => setBankName(e.target.value)} placeholder="Bank name" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <input type="text" value={beneficiary} onChange={(e) => setBeneficiary(e.target.value)} placeholder="Beneficiary (defaults to legal name)" className="bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>
        </section>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Footer + defaults
          </h2>
          <textarea value={footerText} onChange={(e) => setFooterText(e.target.value)} rows={2} placeholder="Footer text (small print at the bottom of each invoice)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          <input type="text" value={defaultTerms} onChange={(e) => setDefaultTerms(e.target.value)} placeholder="Default payment terms (e.g. Payment due within 30 days)" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
        </section>

        {saveError && <div className="text-sm text-red">{saveError}</div>}
        {savedNotice && (<div className="text-sm text-green-500 border border-green-500/30 bg-green-500/10 rounded px-2 py-1">
            {savedNotice}
          </div>)}

        <div className="pt-2 border-t border-border flex items-center justify-end">
          <button type="button" onClick={save} disabled={saving} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      </div>
    </div>);
}
