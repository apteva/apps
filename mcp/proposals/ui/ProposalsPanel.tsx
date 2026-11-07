import { useEffect, useRef, useState } from "react";

type Choice = "unselected" | "include" | "later" | "decline";
type Rule = "any" | "one" | "one_required";
type Line = { name: string; description?: string; required?: boolean; amount_minor: number };
type Category = { name: string; rule: Rule; items: Line[] | null };
type Content = {
  title: string; sender: string; client_name: string; client_email?: string;
  overview?: string; terms?: string; currency: string; validity_days: number;
  opportunity_id?: number; categories: Category[] | null;
};
type DraftLine = Omit<Line, "amount_minor"> & { amount: string };
type Draft = Omit<Content, "categories"> & {
  categories: (Omit<Category, "items"> & { items: DraftLine[] })[];
};
type Reply = { outcome: string; name: string; note: string; selection: Record<string, Choice>; total_minor: number; created_at: number };
type Revision = {
  id: number; proposal_id: number; number: number; version: number; state: string;
  content: Content; expires_at?: number; response?: Reply;
};
type Detail = { id: number; revisions: Revision[] };
type List = { proposals: Revision[]; next_before?: number };
type Opportunity = { id: number; title: string; contact_name?: string; contact_email?: string; currency?: string };

const currencies = ["EUR", "USD", "GBP", "CAD", "AUD", "CHF", "NZD", "SEK", "NOK", "DKK"];
const states: Record<string, string> = {
  draft: "Draft", published: "Open", accepted: "Accepted", declined: "Declined",
  changes_requested: "Changes requested", expired: "Expired", superseded: "Superseded", withdrawn: "Withdrawn",
};
const labels: Record<Choice, string> = {
  unselected: "No decision", include: "Include", later: "For later", decline: "Decline",
};
const rules: Record<Rule, string> = { any: "Allow any", one: "Pick up to one", one_required: "Require one choice" };

function blankDraft(): Draft {
  return { title: "", sender: "", client_name: "", client_email: "", currency: "EUR", validity_days: 14, categories: [] };
}

function editable(c: Content): Draft {
  return {
    ...c,
    categories: (c.categories || []).map((group) => ({
      ...group,
      items: (group.items || []).map(({amount_minor, ...line}) => ({...line, amount: (amount_minor / 100).toFixed(2)})),
    })),
  };
}

function toContent(draft: Draft): Content {
  return {
    ...draft,
    categories: draft.categories.map((group) => ({
      ...group,
      items: group.items.map(({amount, ...line}) => {
        if (!/^\d{1,10}(?:[.,]\d{0,2})?$/.test(amount.trim())) {
          throw new Error("Enter an amount with up to two decimal places for " + (line.name || "each item") + ".");
        }
        const [whole, cents = ""] = amount.trim().split(/[.,]/);
        return {...line, amount_minor: Number(whole) * 100 + Number(cents.padEnd(2, "0"))};
      }),
    })),
  };
}

function money(amount: number, currency: string) {
  return new Intl.NumberFormat(undefined, {style: "currency", currency}).format(amount / 100);
}

async function api<T>(projectId: string, action: string, body?: object, query?: Record<string, string>) {
  const params = new URLSearchParams({project_id: projectId, ...query});
  const res = await fetch("/api/apps/proposals/proposals" + (action ? "/" + action : "") + "?" + params, {
    method: body ? "POST" : "GET",
    credentials: "same-origin",
    headers: body ? {"Content-Type": "application/json"} : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let result;
  try { result = JSON.parse(text); } catch { result = null; }
  if (!res.ok) throw new Error(result?.error || text.trim() || "Request failed (" + res.status + ").");
  return result as T;
}

export default function ProposalsPanel({projectId}: {projectId: string}) {
  return <ProjectPanel key={projectId} projectId={projectId}/>;
}

function ProjectPanel({projectId}: {projectId: string}) {
  const [list, setList] = useState<List>({proposals: []});
  const [record, setRecord] = useState<Detail | null>(null);
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft);
  const [preview, setPreview] = useState<Content | null>(null);
  const [historyId, setHistoryId] = useState<number>();
  const [crmID, setCrmID] = useState("");
  const [url, setURL] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const reminder = useRef("");

  function show(next: Detail) {
    setRecord(next);
    setList((previous) => ({
      ...previous,
      proposals: previous.proposals.map((row) => row.proposal_id === next.id ? next.revisions[0] : row),
    }));
    setCreating(false);
    setDraft(editable(next.revisions[0].content));
    setHistoryId(undefined);
    setPreview(null);
    setURL("");
    reminder.current = "";
  }

  async function reload() {
    setList(await api<List>(projectId, ""));
  }

  useEffect(() => {
    let active = true;
    api<List>(projectId, "").then((result) => { if (active) setList(result); })
      .catch((err) => { if (active) setError(err.message); });
    return () => { active = false; };
  }, [projectId]);

  async function run(action: () => Promise<void> | void) {
    setBusy(true);
    setError("");
    setNotice("");
    try { await action(); } catch (err) { setError((err as Error).message); } finally { setBusy(false); }
  }

  function start(c?: Content) {
    setRecord(null);
    setCreating(true);
    setDraft(c ? editable({...c, client_name: "", client_email: "", opportunity_id: undefined}) : blankDraft());
    setHistoryId(undefined);
    setPreview(null);
    setURL("");
    setCrmID("");
    setNotice("");
    setError("");
  }

  const latest = record?.revisions[0];
  const open = record?.revisions.find((r) => r.state === "published");
  const accepted = record?.revisions.find((r) => r.state === "accepted");
  const editing = creating || latest?.state === "draft";
  const shown = record?.revisions.find((r) => r.id === historyId) || latest;
  const fields = (revision: Revision) => ({proposal_id: revision.proposal_id, revision_id: revision.id, version: revision.version});

  async function save(publish: boolean) {
    const body = {content: toContent(draft)};
    const saved = await api<Detail>(projectId, creating ? "create" : "update_draft",
      creating ? body : {...body, ...fields(latest!)});
    show(saved);
    if (publish) {
      const released = await api<Detail>(projectId, "publish", fields(saved.revisions[0]));
      show(released);
      setNotice("Published. Copy the private link or send it by email.");
    } else {
      setNotice("Draft saved.");
    }
    await reload();
  }

  function loadRecord(id: number) {
    return run(async () => show(await api<Detail>(projectId, "get", undefined, {proposal_id: String(id)})));
  }

  function updateCategory(index: number, change: Partial<Draft["categories"][number]>) {
    setDraft((current) => ({...current, categories: current.categories.map((group, i) => i === index ? {...group, ...change} : group)}));
  }

  function updateLine(group: number, index: number, change: Partial<DraftLine>) {
    setDraft((current) => ({...current, categories: current.categories.map((category, i) => i === group ? {
      ...category, items: category.items.map((line, j) => j === index ? {...line, ...change} : line),
    } : category)}));
  }

  return <div className="prop-panel">
    <style>{styles}</style>
    <header className="prop-header">
      <div><h1>Proposals</h1><p>Share interactive scopes and track client decisions.</p></div>
      <div className="prop-actions">
        <button disabled={busy} onClick={() => run(reload)}>Refresh list</button>
        <button className="prop-primary" disabled={busy} onClick={() => start()}>New proposal</button>
      </div>
    </header>
    {error && <p className="prop-error" role="alert">{error}</p>}
    {notice && <p className="prop-notice" role="status">{notice}</p>}
    <div className="prop-layout">
      <nav className="prop-card prop-nav" aria-label="Proposals">
        <strong>Proposals</strong>
        {!list.proposals.length && <p>No proposals yet.</p>}
        {list.proposals.map((row) => <button key={row.proposal_id} type="button" disabled={busy} aria-current={record?.id === row.proposal_id}
          onClick={() => loadRecord(row.proposal_id)}>
          <span>{row.content.title}</span><small>{row.content.client_name || "No client"} · {states[row.state]}</small>
        </button>)}
        {list.next_before && <button disabled={busy} onClick={() => run(async () => {
          const next = await api<List>(projectId, "", undefined, {before: String(list.next_before)});
          setList({...next, proposals: [...list.proposals, ...next.proposals]});
        })}>Load more</button>}
      </nav>
      <main className="prop-main">
        {!editing && !record && <section className="prop-card"><h2>Create your first proposal</h2><p>Group the work into categories and let your client choose their scope.</p></section>}
        {open && <section className="prop-card">
          <h2>Live revision {open.number}</h2>
          <p>Open until {new Date(open.expires_at! * 1000).toLocaleDateString()}.</p>
          <div className="prop-actions">
            <button disabled={busy} onClick={() => run(async () => {
              const result = await api<{url: string}>(projectId, "link_create", fields(open));
              setURL(result.url);
              try { await navigator.clipboard.writeText(result.url); setNotice("Private link copied."); }
              catch { setNotice("Private link is ready to copy."); }
            })}>Copy private link</button>
            <button disabled={busy || !open.content.client_email} onClick={() => {
              if (!window.confirm("Send a real email to " + open.content.client_email + "?")) return;
              void run(async () => {
                const result = await api<{status: string}>(projectId, "send", fields(open));
                setNotice("Messaging status: " + result.status + ".");
              });
            }}>Email invitation</button>
            <button disabled={busy || !open.content.client_email} onClick={() => {
              if (!window.confirm("Email a reminder to " + open.content.client_email + "?")) return;
              reminder.current ||= crypto.randomUUID();
              void run(async () => {
                const result = await api<{status: string}>(projectId, "send", {...fields(open), reminder_key: reminder.current});
                reminder.current = "";
                setNotice("Messaging status: " + result.status + ".");
              });
            }}>Send reminder</button>
            <button disabled={busy} onClick={() => {
              if (!window.confirm("Revoke the current link and issue a new one?")) return;
              void run(async () => {
                const result = await api<{url: string}>(projectId, "link_create", {...fields(open), rotate: true});
                setURL(result.url);
                setNotice("New link issued. The previous link is revoked.");
              });
            }}>Rotate link</button>
            <button disabled={busy} onClick={() => {
              if (!window.confirm("Close this revision to new client responses?")) return;
              void run(async () => { show(await api<Detail>(projectId, "withdraw", fields(open))); await reload(); });
            }}>Withdraw</button>
          </div>
          {url && <label>Private client link<input readOnly value={url} onFocus={(e) => e.target.select()}/></label>}
        </section>}
        {editing && <form className="prop-main" onSubmit={(e) => { e.preventDefault(); void run(() => save(false)); }}>
          <section className="prop-card">
            <h2>{creating ? "New proposal" : "Edit revision " + latest!.number}</h2>
            <div className="prop-grid">
              <label>Proposal title<input required maxLength={200} value={draft.title} onChange={(e) => setDraft({...draft, title: e.target.value})}/></label>
              <label>Business or sender name<input maxLength={160} value={draft.sender} onChange={(e) => setDraft({...draft, sender: e.target.value})}/></label>
              <label>Client name<input maxLength={160} value={draft.client_name} onChange={(e) => setDraft({...draft, client_name: e.target.value})}/></label>
              <label>Client email<input type="email" value={draft.client_email || ""} onChange={(e) => setDraft({...draft, client_email: e.target.value})}/></label>
              <label>Currency<select value={draft.currency} onChange={(e) => setDraft({...draft, currency: e.target.value})}>
                {currencies.map((currency) => <option key={currency}>{currency}</option>)}</select></label>
              <label>Days open after publishing<input required type="number" min={1} max={365} value={draft.validity_days || ""}
                onChange={(e) => setDraft({...draft, validity_days: Number(e.target.value)})}/></label>
            </div>
            <label>Overview<textarea value={draft.overview || ""} onChange={(e) => setDraft({...draft, overview: e.target.value})}/></label>
            <label>Terms and exclusions<textarea value={draft.terms || ""} onChange={(e) => setDraft({...draft, terms: e.target.value})}/></label>
            <div className="prop-actions prop-crm">
              <label>CRM opportunity ID<input type="number" min={1} value={crmID} onChange={(e) => setCrmID(e.target.value)}/></label>
              <button type="button" disabled={busy || Number(crmID) <= 0} onClick={() => run(async () => {
                const match = await api<Opportunity>(projectId, "crm_lookup", {opportunity_id: Number(crmID)});
                setDraft((current) => ({...current, opportunity_id: match.id, title: current.title || match.title,
                  client_name: match.contact_name || current.client_name, client_email: match.contact_email || current.client_email,
                  currency: currencies.includes(match.currency || "") ? match.currency! : current.currency}));
              })}>Import from CRM</button>
              {draft.opportunity_id && <button type="button" onClick={() => setDraft({...draft, opportunity_id: undefined})}>Unlink CRM #{draft.opportunity_id}</button>}
            </div>
          </section>
          <section className="prop-card">
            <h2>Scope and pricing</h2>
            <p>Use categories like Core build, Integrations, and Support. Required items are always included.</p>
            {draft.categories.map((group, i) => <fieldset className="prop-category" key={i}>
              <legend>Category {i + 1}</legend>
              <div className="prop-grid">
                <label>Name<input aria-label={"Category " + (i + 1) + " name"} maxLength={120} value={group.name} onChange={(e) => updateCategory(i, {name: e.target.value})}/></label>
                <label>Selection rule<select value={group.rule} onChange={(e) => {
                  const rule = e.target.value as Rule;
                  updateCategory(i, {rule, items: rule === "any" ? group.items : group.items.map((item) => ({...item, required: false}))});
                }}>{Object.entries(rules).map(([rule, name]) => <option key={rule} value={rule}>{name}</option>)}</select></label>
              </div>
              {group.items.map((line, j) => <fieldset className="prop-line" key={j}>
                <legend>Item {j + 1}</legend>
                <div className="prop-grid">
                  <label>Item name<input maxLength={160} value={line.name} onChange={(e) => updateLine(i, j, {name: e.target.value})}/></label>
                  <label>Price ({draft.currency})<input inputMode="decimal" value={line.amount} onChange={(e) => updateLine(i, j, {amount: e.target.value})}/></label>
                </div>
                <label>Description<textarea maxLength={2000} value={line.description || ""} onChange={(e) => updateLine(i, j, {description: e.target.value})}/></label>
                <div className="prop-actions">
                  {group.rule === "any" && <label className="prop-check"><input type="checkbox" checked={!!line.required}
                    onChange={(e) => updateLine(i, j, {required: e.target.checked})}/> Required item</label>}
                  <button type="button" onClick={() => updateCategory(i, {items: group.items.filter((_, index) => index !== j)})}>Remove item</button>
                </div>
              </fieldset>)}
              <div className="prop-actions">
                <button type="button" disabled={draft.categories.reduce((sum, row) => sum + row.items.length, 0) >= 100}
                  onClick={() => updateCategory(i, {items: [...group.items, {name: "", amount: "0.00"}]})}>Add item</button>
                <button type="button" onClick={() => setDraft({...draft, categories: draft.categories.filter((_, index) => index !== i)})}>Remove category</button>
              </div>
            </fieldset>)}
            <div className="prop-actions">
              <button type="button" disabled={draft.categories.length >= 20}
                onClick={() => setDraft({...draft, categories: [...draft.categories, {name: "", rule: "any", items: []}]})}>Add category</button>
            </div>
          </section>
          <div className="prop-actions">
            <button type="submit" disabled={busy}>Save draft</button>
            <button type="button" disabled={busy} onClick={() => run(() => setPreview(toContent(draft)))}>Preview selection</button>
            <button type="button" className="prop-primary" disabled={busy} onClick={() => {
              if (open && !window.confirm("Publish this draft and close the previous client link?")) return;
              void run(() => save(true));
            }}>Save and publish</button>
          </div>
          {preview && <SelectionPreview key={JSON.stringify(preview)} content={preview}/>}
        </form>}
        {record && <section className="prop-card">
          <div className="prop-header"><h2>Revision history</h2><label>Revision<select value={shown!.id}
            onChange={(e) => setHistoryId(Number(e.target.value))}>
            {record.revisions.map((r) => <option key={r.id} value={r.id}>Revision {r.number} · {states[r.state]}</option>)}
          </select></label></div>
          <h3>{shown!.content.title}</h3>
          {shown!.response && <SelectionPreview key={shown!.id} content={shown!.content} reply={shown!.response}/>}
          {!shown!.response && shown!.state !== "draft" && <SelectionPreview key={shown!.id} content={shown!.content}/>}
          <div className="prop-actions">
            {!editing && !accepted && <button disabled={busy} onClick={() => run(async () => {
              show(await api<Detail>(projectId, "revise", {proposal_id: record.id})); await reload();
            })}>Start revision</button>}
            <button disabled={busy} onClick={() => start(shown!.content)}>Use as new proposal</button>
            {accepted?.content.opportunity_id && <button disabled={busy} onClick={() => {
              if (!window.confirm("Update the value of CRM opportunity #" + accepted.content.opportunity_id + "?")) return;
              void run(async () => {
                await api(projectId, "crm_sync", {proposal_id: record.id});
                setNotice("Accepted value synced to CRM.");
              });
            }}>Sync accepted value to CRM</button>}
          </div>
        </section>}
      </main>
    </div>
  </div>;
}

function SelectionPreview({content, reply}: {content: Content; reply?: Reply}) {
  const [choices, setChoices] = useState<Record<string, Choice>>({});
  const selection = reply?.selection || choices;
  let total = 0;
  const categories = (content.categories || []).map((group, i) =>
    <section className="prop-category" key={i}>
      <h3>{group.name || "Untitled category"}</h3>
      <small>{rules[group.rule]}</small>
      {(group.items || []).map((line, j) => {
        const key = i + ":" + j;
        const choice = selection[key] || "unselected";
        if (line.required || choice === "include") total += line.amount_minor;
        return <div className="prop-preview-row" key={j}>
          <div><strong>{line.name || "Untitled item"}</strong>{line.description && <p>{line.description}</p>}</div>
          <span>{money(line.amount_minor, content.currency)}</span>
          {line.required ? <span>Required</span> : reply ? <span>{labels[choice]}</span> :
            <select aria-label={"Choice for " + line.name} value={choice} onChange={(e) => {
              const value = e.target.value as Choice;
              setChoices((previous) => {
                const next = {...previous, [key]: value};
                if (value === "include" && group.rule !== "any") {
                  (group.items || []).forEach((_, index) => {
                    const other = i + ":" + index;
                    if (other !== key && next[other] === "include") next[other] = "unselected";
                  });
                }
                return next;
              });
            }}>{Object.entries(labels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>}
        </div>;
      })}
    </section>);
  return <section className="prop-card prop-preview">
    <h3>{reply ? states[reply.outcome] : "Client selection preview"}</h3>
    <p className="prop-copy">{content.overview}</p>
    {categories}
    <strong>{reply?.outcome === "accepted" ? "Accepted total" : "Selected total"} · {money(reply?.total_minor ?? total, content.currency)}</strong>
    <p className="prop-copy">{content.terms}</p>
    {reply && <div><p>{[reply.name, new Date(reply.created_at * 1000).toLocaleString()].filter(Boolean).join(" · ")}</p>
      {reply.note && <p className="prop-copy">{reply.note}</p>}</div>}
  </section>;
}

const styles = `
.prop-panel { max-width: 92rem; margin: 0 auto; padding: 1.25rem; color: var(--foreground, inherit); }
.prop-panel h1, .prop-panel h2, .prop-panel h3, .prop-panel p { margin: 0; }
.prop-panel h1 { font-size: 1.65rem; font-weight: 700; }
.prop-panel h2 { font-size: 1.15rem; font-weight: 650; }
.prop-panel h3 { font-size: 1rem; font-weight: 650; }
.prop-panel p, .prop-panel small { opacity: .8; overflow-wrap: anywhere; }
.prop-header, .prop-actions { display: flex; align-items: center; gap: .6rem; flex-wrap: wrap; }
.prop-header { justify-content: space-between; margin-bottom: .45rem; }
.prop-layout { display: grid; grid-template-columns: minmax(12rem, 17rem) minmax(0, 1fr); align-items: start; gap: 1rem; margin-top: 1rem; }
.prop-main, .prop-card, .prop-panel label { display: grid; gap: .7rem; min-width: 0; }
.prop-card, .prop-category, .prop-line { padding: 1rem; border: 1px solid color-mix(in srgb, currentColor 20%, transparent); border-radius: .7rem; }
.prop-category, .prop-line { display: grid; gap: .55rem; min-width: 0; }
.prop-category legend, .prop-line legend { padding: 0 .35rem; }
.prop-nav button { text-align: left; display: grid; gap: .15rem; overflow-wrap: anywhere; }
.prop-nav [aria-current="true"] { outline: 2px solid #228e81; }
.prop-panel input:not([type=checkbox]), .prop-panel textarea, .prop-panel select, .prop-panel button {
  border: 1px solid color-mix(in srgb, currentColor 28%, transparent); border-radius: .4rem; background: transparent;
  padding: .42rem .58rem; font: inherit; color: inherit; min-width: 0;
}
.prop-panel select option { color: #182535; background: #fff; }
.prop-panel button { cursor: pointer; }
.prop-panel button:disabled { opacity: .5; cursor: default; }
.prop-panel button.prop-primary { background: #075e55; color: #fff; border-color: #075e55; }
.prop-panel label { gap: .25rem; font-size: .87rem; font-weight: 560; }
.prop-panel textarea { min-height: 3.5rem; resize: vertical; }
.prop-panel .prop-check { display: flex; align-items: center; }
.prop-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .65rem; }
.prop-crm { align-items: end; }
.prop-preview-row { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(5rem, 8rem); align-items: center; gap: .55rem; }
.prop-copy { white-space: pre-wrap; }
.prop-panel .prop-error { color: #c43b36; opacity: 1; margin-top: .5rem; }
.prop-panel .prop-notice { margin-top: .5rem; }
@media (max-width: 760px) { .prop-layout { grid-template-columns: 1fr; } }
@media (max-width: 430px) { .prop-grid, .prop-preview-row { grid-template-columns: 1fr; } }
`;
