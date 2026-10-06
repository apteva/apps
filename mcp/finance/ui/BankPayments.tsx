import { useEffect, useRef, useState } from "react";

type Connection = { id: number; provider: string; name: string };
type Account = { id: number; name: string; currency: string; connection_id?: string; archived: boolean };
type Request = { connection_id: number; provider: string; mode: string; amount: number; currency: string; reference: string; account_id?: number; recipient_name?: string; recipient_address?: string; recipient_id?: string; direction?: string; legal_name?: string; network?: string; user_id?: string; bank_name?: string; payment_type?: string; psu_type?: string; redirect_url?: string; deferred?: boolean; debtor_iban?: string; customer_id?: string; customer_ip?: string; email?: string; sort_code?: string; account_number?: string; country?: string };
type Payment = { id: string; request: Request; state: string; provider_id: string; provider_status: string; error?: string; response?: { cancellable?: boolean; candidates?: { id: string; date: string; reference: string }[] } };
type Bank = { name: string; country?: string; code?: string; payments?: { payment_type: string; deferred_submission_supported?: boolean; psu_type?: string }[] };
export function authorizationLink(provider: string, raw: string): string {
 const u=new URL(raw);const domains:Record<string,string[]>={plaid:["plaid.com"],"enable-banking":["enablebanking.com"],"truelayer-payments":["truelayer.com","truelayer-sandbox.com"],"saltedge-payments":["saltedge.com"]};
 if(u.protocol!=="https:"||u.username||u.password||!domains[provider]?.some(d=>u.hostname===d||u.hostname.endsWith(`.${d}`)))throw new Error("Provider returned an unexpected authorization URL.");return u.href;
}
type API = <T>(path: string, options?: RequestInit) => Promise<T>;
type Props = { connection: Connection | null; accounts: Account[]; api: API };

export function decimalMinor(value: string): number {
  if (!/^\d+(\.\d{1,2})?$/.test(value.trim())) throw new Error("Enter a positive amount with at most two decimal places.");
  const [whole, fraction = ""] = value.trim().split(".");
  const result = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, "0"));
  if (result <= 0n || result > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("Amount is outside the supported range.");
  return Number(result);
}

export function displayMinor(amount: number): string {
  const n=BigInt(amount);return `${n/100n}.${String(n%100n).padStart(2,"0")}`;
}

let tellerScript: Promise<void> | null = null;
async function openTeller(token: string, applicationId: string, environment: string, onDone: () => void) {
  if (!applicationId.trim() || !environment) throw new Error("Enter your Teller application ID and select its environment.");
  if (!tellerScript) tellerScript = new Promise((resolve, reject) => {
    const script = document.createElement("script"); script.src = "https://cdn.teller.io/connect/connect.js";
    script.onload = () => resolve(); script.onerror = () => { tellerScript = null; script.remove(); reject(new Error("Could not load Teller Connect.")); };
    document.head.appendChild(script);
  });
  await tellerScript;
  const teller = (window as unknown as { TellerConnect: { setup: (options: Record<string, unknown>) => { open: () => void } } }).TellerConnect;
  teller.setup({ applicationId, environment, connectToken: token, onSuccess: onDone, onExit: onDone }).open();
}

export default function BankPayments({ connection, accounts, api }: Props) {
  const [payments, setPayments] = useState<Payment[]>([]);
  const [capabilities, setCapabilities] = useState<Record<string, { modes: string[]; note: string }>>({});
  const [selected, setSelected] = useState<Payment | null>(null);
  const [mode, setMode] = useState("payment");
  const [amount, setAmount] = useState(""); const [currency, setCurrency] = useState("EUR");
  const [source, setSource] = useState(""); const [recipient, setRecipient] = useState("");
  const [address, setAddress] = useState(""); const [recipientID, setRecipientID] = useState("");
  const [userID, setUserID] = useState(""); const [country, setCountry] = useState("ES");
  const [reference, setReference] = useState(""); const [direction, setDirection] = useState("credit");
  const [legalName, setLegalName] = useState(""); const [achClass, setACHClass] = useState("ppd");
  const [tellerApp, setTellerApp] = useState(""); const [environment, setEnvironment] = useState("");
  const [bankID, setBankID] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const [authorizationURL, setAuthorizationURL] = useState("");
 const [redirectURL,setRedirectURL]=useState("");const [returnURL,setReturnURL]=useState("");
 const [bankName,setBankName]=useState("");const [paymentType,setPaymentType]=useState("SEPA");const [psuType,setPSUType]=useState("personal");const [deferred,setDeferred]=useState(false);
 const [debtorIBAN,setDebtorIBAN]=useState("");const [customerID,setCustomerID]=useState("");const [customerIP,setCustomerIP]=useState("");const [email,setEmail]=useState("");const [sortCode,setSortCode]=useState("");const [accountNumber,setAccountNumber]=useState("");
 const [banks,setBanks]=useState<Bank[]>([]);const [recipients,setRecipients]=useState<{recipient_id:string;name:string;iban?:string}[]>([]);const [optionCursor,setOptionCursor]=useState("");const [optionNote,setOptionNote]=useState("");
  const pendingDraft = useRef<{ body: string; key: string } | null>(null);
  const active = useRef(true);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  const call = <T,>(action: string, body: object) => api<T>(`/banking/payments/${action}`, { method: "POST", body: JSON.stringify(body) });
  const reload = async () => { const r = await call<{ payments: Payment[]; capabilities: typeof capabilities }>("list", {}); if (active.current) { setPayments(r.payments); setCapabilities(r.capabilities); } };
  useEffect(() => { void reload().catch(e => setError(String(e))); }, []);
  useEffect(() => { setMode("payment"); setCurrency(connection?.provider === "teller" ? "USD" : "EUR"); setSource(""); setAuthorizationURL(""); setSelected(null);setPaymentType("SEPA");setBanks([]);setRecipients([]);setOptionCursor("");setOptionNote("");setBankName("");setDeferred(false);setReturnURL(""); }, [connection?.id]);
  const run = async (fn: () => Promise<void>) => { setBusy(true); setError(""); try { await fn(); await reload(); } catch (e) { if (active.current) setError(e instanceof Error ? e.message : String(e)); } finally { if (active.current) setBusy(false); } };
  const supported = connection ? capabilities[connection.provider]?.modes ?? [] : [];
  const linked = accounts.filter(a => !a.archived && a.connection_id === String(connection?.id) && a.currency === "USD");
  const european = ["enable-banking","truelayer-payments","saltedge-payments"].includes(connection?.provider || "");
 const loadOptions = (more=false) => run(async()=>{
 if(!connection)return;
 const r=await call<{aspsps?:Bank[];data?:Bank[];recipients?:{recipient_id:string;name:string;iban?:string}[];next_cursor?:string;meta?:{next_id?:string};schemes?:{name:string}[]}>("options",{connection_id:connection.id,country,psu_type:psuType,account_id:source?Number(source):undefined,cursor:more?optionCursor:undefined});
 if(connection.provider==="plaid"){setRecipients(old=>more?[...old,...(r.recipients||[])]:r.recipients||[]);setOptionCursor(r.next_cursor||"");}
 else if(connection.provider==="teller")setOptionNote(`Supported schemes: ${(r.schemes||[]).map(x=>x.name).join(", ")||"none"}`);
 else {setBanks(old=>more?[...old,...(r.aspsps||r.data||[])]:r.aspsps||r.data||[]);setOptionCursor(r.meta?.next_id||"");}
 });
 const prepare = () => run(async () => {
    if (!connection) return;
    const data = { connection_id: connection.id, mode, amount: decimalMinor(amount), currency: connection.provider === "teller" || mode === "ach" ? "USD" : currency,
      account_id: source ? Number(source) : undefined, recipient_name: recipient, recipient_address: address, recipient_id: recipientID, user_id: userID,
      reference, direction, network: "ach", ach_class: achClass, legal_name: legalName, country,
 ...(european?{bank_name:bankName,payment_type:paymentType,psu_type:psuType,redirect_url:redirectURL,deferred,debtor_iban:debtorIBAN,customer_id:customerID,customer_ip:customerIP,email,sort_code:sortCode,account_number:accountNumber}:{}) };
    const body = JSON.stringify(data);
    if (!pendingDraft.current || pendingDraft.current.body !== body) pendingDraft.current = { body, key: crypto.randomUUID() };
    const p = await call<Payment>("prepare", { ...data, request_key: pendingDraft.current.key });
    setSelected(p); setAuthorizationURL(""); setBankID("");
  });
  const refresh = (id: string, providerID?: string) => run(async () => { setSelected(await call<Payment>("get", { id, refresh: true, ...(providerID ? { provider_id: providerID } : {}) })); });
  const authorize = () => run(async () => {
    if (!selected) return;
    if (selected.request.provider === "teller" && (!tellerApp || !environment)) throw new Error("Enter your Teller application ID and environment first.");
    const r = await call<{ provider: string; hosted_link_url?: string; connect_token?: string }>("authorize", { id: selected.id });
    if (r.hosted_link_url) {
      setAuthorizationURL(authorizationLink(r.provider,r.hosted_link_url));
    } else if (r.connect_token) {
      const id = selected.id;
      await openTeller(r.connect_token, tellerApp, environment, () => { if (active.current) void refresh(id); });
    } else throw new Error("Provider did not return an authorization handoff.");
  });
  const field = (label: string, value: string, setter: (s: string) => void, hint?: string) => <label className="block text-sm">{label}<input value={value} onChange={e => setter(e.target.value)} className="input mt-1" />{hint && <span className="text-xs text-text-muted">{hint}</span>}</label>;
  return <section className="rounded-lg border border-border bg-bg-card p-4 lg:col-span-3">
    <h3 className="font-medium">Bank payments & transfers</h3>
    <p className="mt-1 text-sm text-text-muted">Create a payment, review its details, then submit and complete any bank authorization. Bank sync records the resulting transaction.</p>
    {error && <div role="alert" className="mt-3 text-sm text-error">{error}</div>}
    {connection && <p className="mt-3 text-sm text-text-muted">{capabilities[connection.provider]?.note}</p>}
    {supported.length > 0 && <fieldset disabled={busy} className="mt-4 grid gap-3 md:grid-cols-3">
      {supported.length > 1 && <label className="text-sm">Payment type<select className="input mt-1" value={mode} onChange={e => setMode(e.target.value)}><option value="payment">UK/EU bank payment</option><option value="ach">US ACH transfer</option></select></label>}
      {field("Amount", amount, setAmount)}
      {connection?.provider === "plaid" && mode === "payment" && <>
        <label className="text-sm">Currency<select className="input mt-1" value={currency} onChange={e => { setCurrency(e.target.value); setCountry(e.target.value === "GBP" ? "GB" : "ES"); }}><option>EUR</option><option>GBP</option></select></label>
        {field("Payer bank country", country, setCountry, "Two-letter code, e.g. ES or GB")}
        {field("Recipient name", recipient, setRecipient)}
        <label className="text-sm">Registered recipient<select className="input mt-1" value={recipientID} onChange={e=>{setRecipientID(e.target.value);const r=recipients.find(x=>x.recipient_id===e.target.value);if(r){setRecipient(r.name);setAddress(r.iban||"");}}}><option value="">Choose a recipient</option>{recipients.map(r=><option key={r.recipient_id} value={r.recipient_id}>{r.name} · {r.iban||r.recipient_id}</option>)}</select></label>
 <button type="button" className="btn-secondary" onClick={()=>void loadOptions()}>Load recipients</button>{optionCursor&&<button type="button" className="btn-secondary" onClick={()=>void loadOptions(true)}>More recipients</button>}
 {field("Plaid recipient ID", recipientID, setRecipientID, "You can also enter an existing recipient ID") }
        {field("Plaid user ID", userID, setUserID, "Payer registered through the Plaid integration")}
      </>}
      {european && <>
 <label className="text-sm">Currency<select className="input mt-1" value={currency} onChange={e=>{setCurrency(e.target.value);setCountry(e.target.value==="GBP"?"GB":"ES");}}><option>EUR</option>{connection?.provider==="truelayer-payments"&&<option>GBP</option>}</select></label>
 {field("Recipient name",recipient,setRecipient)}
 {currency==="GBP"?<>{field("Recipient sort code",sortCode,setSortCode)}{field("Recipient account number",accountNumber,setAccountNumber)}</>:field("Recipient IBAN",address,setAddress)}
 {field("Payer bank country",country,v=>{setCountry(v.toUpperCase());setBanks([]);setBankName("");setDeferred(false);},"Two-letter code, e.g. ES")}
 {field("Registered return URL",redirectURL,setRedirectURL,"Use the HTTPS return URL configured with your payment provider")}
 {connection?.provider==="truelayer-payments"?<>{field("Payer full name",legalName,setLegalName)}{field("Payer email",email,setEmail)}</>:<>
 {connection?.provider==="enable-banking"&&<label className="text-sm">Payer type<select className="input mt-1" value={psuType} onChange={e=>{setPSUType(e.target.value);setBanks([]);setBankName("");setDeferred(false);}}><option value="personal">Personal</option><option value="business">Business</option></select></label>}
 <label className="text-sm">Payer bank<select className="input mt-1" value={bankName} onChange={e=>{setBankName(e.target.value);setDeferred(false);}}><option value="">Load and choose a bank</option>{banks.map(b=><option key={b.code||b.name} value={b.code||b.name}>{b.name}</option>)}</select></label>
 <button type="button" className="btn-secondary" onClick={()=>void loadOptions()}>Load payment banks</button>{optionCursor&&<button type="button" className="btn-secondary" onClick={()=>void loadOptions(true)}>More banks</button>}
 {field("Payer IBAN",debtorIBAN,setDebtorIBAN,connection?.provider==="saltedge-payments"?"Required":"Required by some banks")}
 {connection?.provider==="enable-banking"&&<>
 <label className="text-sm">Payment type<select className="input mt-1" value={paymentType} onChange={e=>{setPaymentType(e.target.value);setDeferred(false);}}><option>SEPA</option><option value="INST_SEPA">Instant SEPA</option></select></label>
 <label className="text-sm flex items-center gap-2"><input type="checkbox" checked={deferred} disabled={!banks.find(b=>b.name===bankName)?.payments?.some(m=>m.payment_type===paymentType&&m.deferred_submission_supported)} onChange={e=>setDeferred(e.target.checked)}/>Authorize first, then confirm execution</label>
 </>}
 {connection?.provider==="saltedge-payments"&&<>{field("Salt Edge PIS customer ID",customerID,setCustomerID)}{field("Payer IP address",customerIP,setCustomerIP,"Actual initiating user's IP address")}</>}
 </>}
 </>}
 {(connection?.provider === "teller" || mode === "ach") && <label className="text-sm">Bank account (USD)<select className="input mt-1" value={source} onChange={e => setSource(e.target.value)}><option value="">Choose an account</option>{linked.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>}
      {connection?.provider === "teller" && <>{field("Recipient name", recipient, setRecipient)}{field("Recipient Zelle email or phone", address, setAddress)}<button type="button" disabled={!source} className="btn-secondary" onClick={()=>void loadOptions()}>Check payment availability</button>{optionNote&&<p className="text-sm">{optionNote}</p>}</>}
      {mode === "ach" && <>
        <label className="text-sm">Direction<select className="input mt-1" value={direction} onChange={e => setDirection(e.target.value)}><option value="credit">Pay bank account from Plaid funding account</option><option value="debit">Pull money from bank account</option></select></label>
        {field("Account holder legal name", legalName, setLegalName)}
        <label className="text-sm">ACH authorization class<select className="input mt-1" value={achClass} onChange={e => setACHClass(e.target.value)}><option value="ppd">PPD — consumer authorization</option><option value="web">WEB — online consumer authorization</option><option value="ccd">CCD — corporate authorization</option></select></label>
      </>}
      {field("Reference", reference, setReference, mode === "ach" ? "Up to 10 characters" : ["plaid","truelayer-payments"].includes(connection?.provider||"") ? "Up to 18 characters" : "Payment reference shown to the bank")}
      <div className="flex items-end"><button type="button" className="btn-primary" onClick={prepare}>Review payment</button></div>
    </fieldset>}
    {selected && <div className="mt-5 rounded border border-border p-4">
      <h4 className="font-medium">{selected.state === "draft" ? "Review before submitting" : "Payment request"}</h4>
      <p className="mt-2 text-lg">{selected.request.currency} {displayMinor(selected.request.amount)}</p>
      <dl className="mt-2 grid gap-1 text-sm">
        <div>Provider: {selected.request.provider} · connection #{selected.request.connection_id}</div>
        {selected.request.account_id && <div>Bank account: {accounts.find(a => a.id === selected.request.account_id)?.name || selected.request.account_id}</div>}
        {selected.request.direction && selected.request.mode === "ach" && <div>Direction: {selected.request.direction === "debit" ? "Pull money from bank account" : "Pay bank account from Plaid funding account"}</div>}
        <div>Recipient / account holder: {selected.request.recipient_name || selected.request.legal_name}</div>
        {selected.request.recipient_address && <div>{selected.request.provider==="teller"?"Zelle address":"Recipient account"}: {selected.request.recipient_address}</div>}
        {selected.request.recipient_id && <div>Recipient ID: {selected.request.recipient_id}</div>}
        {selected.request.sort_code&&<div>Recipient sort code/account: {selected.request.sort_code} / {selected.request.account_number}</div>}
 {selected.request.bank_name&&<div>Payer bank: {selected.request.bank_name} · {selected.request.country} · {selected.request.payment_type}</div>}
 {selected.request.debtor_iban&&<div>Payer IBAN: {selected.request.debtor_iban}</div>}
 {selected.request.email&&<div>Payer: {selected.request.legal_name} · {selected.request.email}</div>}
 {selected.request.customer_id&&<div>PIS customer: {selected.request.customer_id}</div>}
 {selected.request.deferred&&<div>Execution: separate confirmation after bank authorization</div>}
 <div>Reference: {selected.request.reference}</div>
        <div>Request: {selected.id}</div>
        <div>Status: {selected.state}{selected.provider_status ? ` · ${selected.provider_status}` : ""}</div>
        {selected.provider_id && <div>Bank reference: {selected.provider_id}</div>}
      </dl>
      {selected.error && <p className="mt-2 text-error">{selected.error}</p>}
      <div className="mt-3 flex flex-wrap gap-2">
        {selected.state === "draft" && <>
          <button disabled={busy} className="btn-primary" onClick={() => void run(async () => { setSelected(await call<Payment>("submit", { id: selected.id, confirmed: true })); })}>Confirm and submit payment</button>
          <button disabled={busy} className="btn-secondary" onClick={() => void run(async () => { setSelected(await call<Payment>("cancel", { id: selected.id })); pendingDraft.current = null; })}>Cancel draft</button>
        </>}
        {selected.state === "authorization_required" && <button disabled={busy} className="btn-primary" onClick={authorize}>Authorize with bank</button>}
        {(selected.state==="ready_to_execute"||(selected.state==="authorization_required"&&selected.request.mode==="ach"))&&<button disabled={busy} className="btn-primary" onClick={()=>void run(async()=>{setSelected(await call<Payment>("continue",{id:selected.id,confirmed:true}));})}>I completed bank authorization — confirm execution</button>}
 {selected.request.mode==="ach"&&selected.state==="submitted"&&selected.response?.cancellable&&<button disabled={busy} className="btn-secondary" onClick={()=>void run(async()=>{setSelected(await call<Payment>("cancel",{id:selected.id,confirmed:true}));})}>Request bank cancellation</button>}
 {!["draft", "cancelled"].includes(selected.state) && <button disabled={busy} className="btn-secondary" onClick={() => void refresh(selected.id)}>Refresh bank status</button>}
      </div>
      {selected.state === "authorization_required" && selected.request.provider === "teller" && <div className="mt-3 grid gap-2 md:grid-cols-2">{field("Teller application ID", tellerApp, setTellerApp)}<label className="text-sm">Teller environment<select className="input mt-1" value={environment} onChange={e => setEnvironment(e.target.value)}><option value="">Select environment</option><option>sandbox</option><option>development</option><option>production</option></select></label></div>}
      {selected.request.provider==="enable-banking"&&selected.request.deferred&&selected.state==="authorization_required"&&<div className="mt-3 space-y-2">{field("Complete return URL after bank authorization",returnURL,setReturnURL,"Copy the full URL from the page the bank returned you to, including its state parameter") }<button disabled={busy||!returnURL} className="btn-secondary" onClick={()=>void run(async()=>{setSelected(await call<Payment>("callback",{id:selected.id,return_url:returnURL}));setReturnURL("");})}>Verify bank return</button></div>}
 {authorizationURL && <a className="mt-3 inline-block text-accent underline" href={authorizationURL} target="_blank" rel="noopener noreferrer">Open bank authorization</a>}
      {!selected.provider_id && ["unknown", "submitting", "continuing", "authorization_required"].includes(selected.state) && <div className="mt-3 space-y-2 text-sm">
        <p>Reconcile with the provider before creating another payment. Verify the date and bank reference for any matching payment below.</p>
        {selected.response?.candidates?.map(c => <button key={c.id} disabled={busy} className="btn-secondary block" onClick={() => void refresh(selected.id, c.id)}>Match {c.date} · {c.reference || c.id}</button>)}
        {field("Provider payment or transfer ID", bankID, setBankID)}
        <button disabled={busy || !bankID} className="btn-secondary" onClick={() => void refresh(selected.id, bankID)}>Verify and reconcile</button>
      </div>}
    </div>}
    <h4 className="mt-5 text-sm font-medium">Recent payment requests</h4>
    {payments.length === 0 ? <p className="mt-2 text-sm text-text-muted">No payment requests yet.</p> : <ul className="mt-2 divide-y divide-border">{payments.map(p => <li key={p.id}><button disabled={busy} className="flex w-full justify-between gap-3 py-2 text-left text-sm" onClick={() => { setSelected(p); setAuthorizationURL(""); setBankID(""); }}>{p.request.currency} {displayMinor(p.request.amount)} · {p.request.recipient_name || p.request.legal_name}<span>{p.state}</span></button></li>)}</ul>}
  </section>;
}
