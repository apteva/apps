import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { proposalLocale } from "./locale";
import { useConfirmation } from "./confirmation";
import { BacklogPanel, BacklogPicker, ClientSelector, currencyOptions as currencies, formatMoney as money } from "./backlog";

type Choice = "unselected" | "include" | "later" | "decline";
type Rule = "any" | "one" | "one_required";
type Line = { name: string; description?: string; required?: boolean; backlog_item_id?: number; amount_minor: number };
type Category = { name: string; rule: Rule; items: Line[] | null };
type Content = {
  title: string; sender: string; client_name: string; client_email?: string;
  overview?: string; terms?: string; currency: string; validity_days: number; language?: "en" | "fr";
  client_id?: number; opportunity_id?: number; categories: Category[] | null;
};
type DraftLine = Omit<Line, "amount_minor"> & { amount: string };
type Draft = Omit<Content, "categories"> & {
  categories: (Omit<Category, "items"> & { items: DraftLine[] })[];
};
type Reply = { outcome: string; name: string; note: string; selection: Record<string, Choice>; total_minor: number; created_at: number };
type Revision = {
  id: number; proposal_id: number; number: number; version: number; state: string;
  content: Content; expires_at?: number; published_at?: number; response?: Reply;
};
export type Detail = { id: number; revisions: Revision[] };
type List = { proposals: Revision[]; next_before?: number };
type Opportunity = { id: number; title: string; contact_name?: string; contact_email?: string; currency?: string };

const states: Record<string, string> = {
  draft: "Draft", published: "Open", accepted: "Accepted", declined: "Declined",
  changes_requested: "Changes requested", expired: "Expired", superseded: "Superseded", withdrawn: "Withdrawn",
};
const labels: Record<Choice, string> = {
  unselected: "No decision", include: "Include", later: "For later", decline: "Decline",
};
const rules: Record<Rule, string> = { any: "Allow any", one: "Pick up to one", one_required: "Require one choice" };

function blankDraft(): Draft {
  return { language: "en", title: "", sender: "", client_name: "", client_email: "", currency: "EUR", validity_days: 14, categories: [] };
}

function editable(c: Content): Draft {
  return {
    ...c, language: c.language || "en",
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

async function api<T>(projectId: string, action: string, body?: object, query?: Record<string, string>, installId?: number) {
  const params = new URLSearchParams({project_id: projectId, ...query});
  if (installId) params.set("install_id", String(installId));
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

type Props = { projectId: string; installId?: number; eventRevision?: number };
type DetailTab = "overview" | "scope" | "preview" | "sharing" | "history";
type ListTab = "all" | "drafts" | "open" | "responses" | "backlog";

const detailTabs = [
  ["overview", "Overview"], ["scope", "Scope & pricing"], ["preview", "Client preview"],
  ["sharing", "Sharing"], ["history", "History"],
];
const listTabs = [["all", "All proposals"], ["drafts", "Drafts"], ["open", "Open"], ["responses", "Responses"], ["backlog", "Client backlog"]];

const iconPaths = {
  file: "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z M14 2v6h6 M8 13h8 M8 17h5",
  plus: "M12 5v14 M5 12h14",
  back: "M19 12H5 M12 19l-7-7 7-7",
  refresh: "M20 7v5h-5 M4 17v-5h5 M6 7a7 7 0 0 1 12-2l2 2 M4 17l2 2a7 7 0 0 0 12-2",
  search: "m21 21-5-5 M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
  link: "M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-2 2 M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l2-2",
  send: "m22 2-7 20-4-9-9-4Z M22 2 11 13",
  arrow: "M7 17 17 7 M7 7h10v10",
  check: "m5 12 4 4L19 6",
  layers: "m12 3 10 6-10 6L2 9Z M2 15l10 6 10-6",
  clock: "M12 8v4l3 2 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
  trash: "M3 6h18 M9 6V3h6v3 M5 6l1 15h12l1-15 M10 10v7 M14 10v7",
};
function Icon({name}: {name: keyof typeof iconPaths}) {
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
    strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={iconPaths[name]}/></svg>;
}
function StatePill({state}: {state: string}) {
  return <span className={"pp-pill pp-state-" + state}><span/>{states[state] || state}</span>;
}
function Tabs({items, value, onChange, label}: {
  items: string[][]; value: string; onChange: (value: string) => void; label: string;
}) {
  return <div className="pp-tabs" role="tablist" aria-label={label}>
    {items.map(([id, title], index) => <button key={id} type="button" role="tab" id={"pp-tab-" + id}
      aria-selected={value === id} aria-controls={"pp-panel-" + id} tabIndex={value === id ? 0 : -1}
      onKeyDown={(event) => {
        let next = index;
        if (event.key === "ArrowRight") next = (index + 1) % items.length;
        else if (event.key === "ArrowLeft") next = (index + items.length - 1) % items.length;
        else if (event.key === "Home") next = 0;
        else if (event.key === "End") next = items.length - 1;
        else return;
        event.preventDefault();
        onChange(items[next][0]);
        (event.currentTarget.parentElement?.children[next] as HTMLButtonElement)?.focus();
      }} onClick={() => onChange(id)}>{title}</button>)}
  </div>;
}

export default function ProposalsPanel(props: Props) {
  return <ProjectPanel key={props.projectId + ":" + props.installId} {...props}/>;
}

function ProjectPanel({projectId, installId, eventRevision}: Props) {
  const [list, setList] = useState<List>({proposals: []});
  const [record, setRecord] = useState<Detail | null>(null);
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft);
  const [tab, setTab] = useState<DetailTab>("overview");
  const [backlogClientID, setBacklogClientID] = useState(0);
  const [backlogPicker, setBacklogPicker] = useState(false);
  const [listTab, setListTab] = useState<ListTab>("all");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [historyId, setHistoryId] = useState<number>();
  const [crmID, setCrmID] = useState("");
  const [url, setURL] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const reminder = useRef("");
  const root = useRef<HTMLDivElement>(null);
  const {confirm, modal} = useConfirmation();
  const latest = record?.revisions[0];
  const open = record?.revisions.find((r) => r.state === "published");
  const accepted = record?.revisions.find((r) => r.state === "accepted");
  const responseRevision = record?.revisions.find((r) => r.response);
  const editing = creating || latest?.state === "draft";
  const detailVisible = creating || !!record;
  const shown = record?.revisions.find((r) => r.id === historyId) || latest;
  const dirty = editing && (creating || JSON.stringify(draft) !== JSON.stringify(editable(latest!.content)));
  const request = <T,>(action: string, body?: object, query?: Record<string, string>) =>
    api<T>(projectId, action, body, query, installId);
  const fields = (revision: Revision) => ({
    proposal_id: revision.proposal_id, revision_id: revision.id, version: revision.version,
  });

  function changeTab(next: DetailTab) {
    setTab(next);
    root.current?.scrollTo({top: 0});
  }
  function show(next: Detail, nextTab?: DetailTab) {
    setRecord(next);
    setList((previous) => ({
      ...previous, proposals: previous.proposals.map((row) => row.proposal_id === next.id ? next.revisions[0] : row),
    }));
    setCreating(false);
    setDraft(editable(next.revisions[0].content));
    setHistoryId(undefined);
    setCrmID(String(next.revisions[0].content.opportunity_id || ""));
    setURL("");
    reminder.current = "";
    if (nextTab) changeTab(nextTab);
  }
  async function confirmLeave() {
    return !dirty || await confirm("Leave this draft and discard your unsaved changes?", "Discard changes", "Unsaved draft");
  }
  async function start(c?: Content) {
    if (!(await confirmLeave())) return;
    setRecord(null);
    setCreating(true);
    setDraft(c ? editable({...c, client_name: "", client_email: "", client_id: undefined, opportunity_id: undefined,
      categories: (c.categories || []).map((group) => ({...group, items: (group.items || []).map((item) => ({...item, backlog_item_id: undefined}))})),
    }) : blankDraft());
    setHistoryId(undefined);
    setURL("");
    setCrmID("");
    setNotice("");
    setError("");
    changeTab("overview");
  }
  async function back() {
    if (!(await confirmLeave())) return;
    setRecord(null);
    setCreating(false);
    setURL("");
    setNotice("");
    setError("");
  }
  async function reload() {
    setList(await request<List>(""));
  }
  useEffect(() => {
    let active = true;
    api<List>(projectId, "", undefined, undefined, installId).then((result) => {
      if (active) { setList(result); setLoading(false); }
    }).catch((err) => {
      if (active) { setError(err.message); setLoading(false); }
    });
    return () => { active = false; };
  }, [projectId, installId, eventRevision]);


  async function run(action: () => Promise<unknown> | void) {
    setBusy(true);
    setError("");
    setNotice("");
    try { await action(); } catch (err) { setError((err as Error).message); } finally { setBusy(false); }
  }
  async function loadRecord(id: number) {
    await run(async () => show(await request<Detail>("get", undefined, {proposal_id: String(id)}), "overview"));
  }
  async function save(publish: boolean) {
    const content = toContent(draft);
    const saved = await request<Detail>(creating ? "create" : "update_draft",
      creating ? {content} : {content, ...fields(latest!)});
    show(saved);
    if (publish) {
      const released = await request<Detail>("publish", fields(saved.revisions[0]));
      show(released, "sharing");
      setNotice("Proposal published. Your client can now review and choose their scope.");
    } else {
      setNotice("Draft saved.");
    }
    await reload();
    return saved;
  }
  function updateCategory(index: number, change: Partial<Draft["categories"][number]>) {
    setDraft((current) => ({
      ...current, categories: current.categories.map((group, i) => i === index ? {...group, ...change} : group),
    }));
  }
  function updateLine(group: number, index: number, change: Partial<DraftLine>) {
    setDraft((current) => ({
      ...current, categories: current.categories.map((category, i) => i === group ? {
        ...category, items: category.items.map((line, j) => j === index ? {...line, ...change} : line),
      } : category),
    }));
  }
  async function shareLink(rotate = false) {
    if (!open) return;
    const result = await request<{url: string}>("link_create", {...fields(open), rotate});
    setURL(result.url);
    if (rotate) { setNotice("A new private link is ready. The previous link is revoked."); return; }
    try { await navigator.clipboard.writeText(result.url); setNotice("Private client link copied."); }
    catch { setNotice("Your private client link is ready below."); }
  }
  async function email(remind: boolean) {
    if (!open || !(await confirm((remind ? "Send a reminder" : "Send a proposal invitation") + " to " + open.content.client_email + "?", remind ? "Send reminder" : "Send invitation", "Email proposal"))) return;
    if (remind) reminder.current ||= crypto.randomUUID();
    await run(async () => {
      const result = await request<{status: string}>("send", {
        ...fields(open), ...(remind ? {reminder_key: reminder.current} : {}),
      });
      if (remind) reminder.current = "";
      setNotice("Messaging status · " + result.status + ".");
    });
  }

  const filtered = list.proposals.filter((row) => {
    const stateMatch = listTab === "all" || (listTab === "drafts" && row.state === "draft") ||
      (listTab === "open" && row.state === "published") ||
      (listTab === "responses" && !!row.response);
    return stateMatch && (row.content.title + " " + row.content.client_name + " " + (row.content.client_email || ""))
      .toLowerCase().includes(search.toLowerCase().trim());
  });
  const draftItemCount = draft.categories.reduce((sum, group) => sum + group.items.length, 0);

  let preview: Content | undefined = shown?.content;
  let previewError = "";
  if (editing && tab === "preview") {
    try { preview = toContent(draft); } catch (err) { previewError = (err as Error).message; }
  }

  return <div className="ap-proposals" ref={root}>
    <style>{styles}</style>
    {modal}
    {detailVisible && <button type="button" className="pp-back" disabled={busy} onClick={back}>
      <Icon name="back"/> All proposals
    </button>}
    <header className="pp-header">
      <div className="pp-title">
        <div className="pp-title-line">
          <h1>{detailVisible ? draft.title || "New proposal" : "Proposals"}</h1>
          {detailVisible && <StatePill state={creating ? "draft" : latest!.state}/>}
        </div>
        <p>{detailVisible
          ? [draft.client_name || "Add a client", latest ? "Revision " + latest.number : "New draft", dirty ? "Unsaved changes" : ""].filter(Boolean).join(" · ")
          : "Create proposals, share scope, and follow client decisions."}</p>
      </div>
      <div className="pp-actions">
        {detailVisible
          ? <>
            {record && <button disabled={busy || dirty} onClick={() => run(async () =>
              show(await request<Detail>("get", undefined, {proposal_id: String(record.id)}))
            )}><Icon name="refresh"/> Refresh proposal</button>}
            {!editing && !accepted && <button disabled={busy} onClick={() => run(async () => {
              show(await request<Detail>("revise", {proposal_id: record!.id}), "overview"); await reload();
            })}><Icon name="plus"/> Start revision</button>}
            {!editing && <button disabled={busy} onClick={() => start(shown!.content)}>Use as new proposal</button>}
          </>
          : <>
            <button disabled={busy || loading} onClick={() => run(reload)}><Icon name="refresh"/> Refresh</button>
            <button className="pp-primary" disabled={busy} onClick={() => start()}><Icon name="plus"/> New proposal</button>
          </>}
      </div>
    </header>
    {error && <div className="pp-notice pp-error" role="alert">{error}</div>}
    {notice && <div className="pp-notice" role="status"><Icon name="check"/>{notice}</div>}

    <Tabs items={detailVisible ? detailTabs : listTabs} value={detailVisible ? tab : listTab}
      onChange={(next) => detailVisible ? changeTab(next as DetailTab) : setListTab(next as ListTab)}
      label={detailVisible ? "Proposal sections" : "Proposal status"}/>
    <main role="tabpanel" id={"pp-panel-" + (detailVisible ? tab : listTab)}
      aria-labelledby={"pp-tab-" + (detailVisible ? tab : listTab)}>
      {!detailVisible && listTab === "backlog" && <BacklogPanel request={request} initialClientID={backlogClientID}
        onSelectClient={setBacklogClientID} onProposal={async (proposal) => {show(proposal, "scope");await reload();}}/>}
      {!detailVisible && listTab !== "backlog" && <>
        <div className="pp-list-toolbar">
          <label className="pp-search"><Icon name="search"/><input type="search" placeholder="Search proposals or clients"
            aria-label="Search proposals" value={search} onChange={(event) => setSearch(event.target.value)}/></label>
          <span className="pp-muted">{filtered.length} {list.next_before ? "loaded " : ""}{filtered.length === 1 ? "proposal" : "proposals"}</span>
        </div>
        {loading ? <Empty title="Loading proposals" description="Getting your project’s proposals." icon="clock"/> :
          filtered.length === 0 ? <Empty
            title={search ? "No matching proposals" : ({all: "No proposals yet", drafts: "No drafts yet", open: "No open proposals", responses: "No client responses yet"})[listTab]}
            description={search ? "Try another client name or proposal title." : "Build a clear scope, give clients choices, and keep every decision in one place."}
            icon="file">
              {!search && <button className="pp-primary" disabled={busy} onClick={() => start()}><Icon name="plus"/> Create proposal</button>}
          </Empty> :
          <div className="pp-table-wrap"><table className="pp-table">
            <thead><tr><th>Proposal</th><th>Client</th><th>Status</th><th>Scope</th><th>Amount</th><th>Valid until</th><th><span className="pp-sr">Open</span></th></tr></thead>
            <tbody>{filtered.map((row) => <tr key={row.proposal_id}>
              <td><button className="pp-table-title" disabled={busy} onClick={() => loadRecord(row.proposal_id)}>{row.content.title}</button>
                <small>Revision {row.number}</small></td>
              <td>{row.content.client_name || "—"}<small>{row.content.client_email}</small></td>
              <td><StatePill state={row.state}/></td>
              <td>{(row.content.categories || []).reduce((sum, group) => sum + (group.items?.length || 0), 0)} items</td>
              <td className="pp-amount">{row.response
                ? <>{money(row.response.total_minor, row.content.currency)}<small>{row.response.outcome === "accepted" ? "Accepted total" : "Selected total"}</small></>
                : <>{money(requiredTotal(row.content), row.content.currency)}<small>Required scope</small></>}</td>
              <td>{row.expires_at ? formatDate(row.expires_at) : "—"}</td>
              <td><button className="pp-icon-button" disabled={busy} aria-label={"Open " + row.content.title}
                onClick={() => loadRecord(row.proposal_id)}><Icon name="arrow"/></button></td>
            </tr>)}</tbody>
          </table></div>}
        {list.next_before && <div className="pp-load-more"><button disabled={busy} onClick={() => run(async () => {
          const next = await request<List>("", undefined, {before: String(list.next_before)});
          setList({...next, proposals: [...list.proposals, ...next.proposals]});
        })}>Load more proposals</button></div>}
      </>}

      {detailVisible && tab === "overview" && (editing ? <div className="pp-editor-grid">
        <section className="pp-card">
          <SectionTitle title="Proposal details" description="Give your client the context they need before choosing their scope."/>
          <div className="pp-fields">
            <label>Proposal title<input maxLength={200} value={draft.title} placeholder="Website redesign & launch"
              onChange={(event) => setDraft({...draft, title: event.target.value})}/></label>
            <div className="pp-grid-two">
              <label>Business or sender name<input maxLength={160} value={draft.sender} placeholder="Your business"
                onChange={(event) => setDraft({...draft, sender: event.target.value})}/></label>
              <label>Days open after publishing<input type="number" min={1} max={365} value={draft.validity_days || ""}
                onChange={(event) => setDraft({...draft, validity_days: Number(event.target.value)})}/></label>
            </div>
            <div className="pp-language-field"><span>Proposal language</span>
              <div className="pp-visual-choices" role="group" aria-label="Proposal language">
                <button type="button" aria-pressed={(draft.language || "en") === "en"} onClick={() => setDraft({...draft, language: "en"})}>English (Default)</button>
                <button type="button" aria-pressed={draft.language === "fr"} onClick={() => setDraft({...draft, language: "fr"})}>Français</button>
              </div>
              <small>Sets client-facing buttons, messages, dates, and prices. Write your title, scope, and terms in the chosen language.</small>
            </div>
            <label>Overview<textarea className="pp-overview-input" maxLength={16000} value={draft.overview || ""}
              placeholder="What you’re building, the outcome, and how you’ll work together."
              onChange={(event) => setDraft({...draft, overview: event.target.value})}/></label>
            <label>Terms and exclusions<textarea maxLength={16000} value={draft.terms || ""}
              placeholder="Delivery timeline, payment terms, assumptions, and anything outside the scope."
              onChange={(event) => setDraft({...draft, terms: event.target.value})}/></label>
          </div>
        </section>
        <div className="pp-stack">
          <section className="pp-card">
            <SectionTitle title="Client" description="Choose a saved client to use their private backlog, or enter details below."/>
            <div className="pp-fields">
              <ClientSelector request={request} selected={draft.client_id ? {id: draft.client_id, name: draft.client_name} : undefined}
                onChange={async (client) => {
                  if (draft.client_id === client?.id) return;
                  const imported = draft.categories.some((group) => group.items.some((item) => item.backlog_item_id));
                  if (imported && !(await confirm("Change the client and remove items imported from the previous client’s backlog?", "Change client", "Change proposal client"))) return;
                  setDraft({...draft, client_id: client?.id, client_name: client?.name || draft.client_name,
                    client_email: client?.email || "", categories: imported ? draft.categories.map((group) => ({
                      ...group, items: group.items.filter((item) => !item.backlog_item_id),
                    })).filter((group) => group.items.length > 0) : draft.categories});
                }}/>
              <div className="pp-client-fields-divider"/>

              <label>Client name<input maxLength={160} value={draft.client_name} placeholder="Client or company"
                onChange={(event) => setDraft({...draft, client_name: event.target.value})}/></label>
              <label>Client email<input type="email" value={draft.client_email || ""} placeholder="client@company.com"
                onChange={(event) => setDraft({...draft, client_email: event.target.value})}/></label>
              <label>Currency<select value={draft.currency} onChange={(event) => setDraft({...draft, currency: event.target.value})}>
                {currencies.map((currency) => <option key={currency}>{currency}</option>)}
              </select></label>
            </div>
          </section>
          <section className="pp-card">
            <SectionTitle title="CRM connection" description="Import an opportunity’s client details, then sync the accepted value later."/>
            <div className="pp-inline-field">
              <label>CRM opportunity ID<input inputMode="numeric" type="number" min={1} value={crmID}
                onChange={(event) => setCrmID(event.target.value)}/></label>
              <button disabled={busy || Number(crmID) <= 0} onClick={() => run(async () => {
                const match = await request<Opportunity>("crm_lookup", {opportunity_id: Number(crmID)});
                setDraft((current) => ({
                  ...current, opportunity_id: match.id, title: current.title || match.title,
                  client_name: match.contact_name || current.client_name, client_email: match.contact_email || current.client_email,
                  currency: currencies.includes(match.currency || "") ? match.currency! : current.currency,
                }));
              })}>Import from CRM</button>
            </div>
            {!!draft.opportunity_id && <div className="pp-linked"><span><Icon name="link"/> Opportunity #{draft.opportunity_id}</span>
              <button className="pp-text-button" onClick={() => setDraft({...draft, opportunity_id: undefined})}>Unlink CRM</button></div>}
          </section>
          <button className="pp-next-step" onClick={() => changeTab("scope")}><span><Icon name="layers"/> Add scope & pricing</span><Icon name="arrow"/></button>
        </div>
      </div> : <div className="pp-editor-grid">
        <section className="pp-card">
          <SectionTitle title="Proposal overview" description={"Prepared by " + latest!.content.sender}/>
          <div className="pp-metadata">
            <div><span>Client</span><strong>{latest!.content.client_name || "—"}</strong><small>{latest!.content.client_email}</small></div>
            <div><span>Required scope</span><strong>{money(requiredTotal(latest!.content), latest!.content.currency)}</strong></div>
            <div><span>Language</span><strong>{latest!.content.language === "fr" ? "Français" : "English"}</strong></div>
            <div><span>Revision</span><strong>{latest!.number}</strong></div>
            <div><span>Valid until</span><strong>{latest!.expires_at ? formatDate(latest!.expires_at) : "—"}</strong></div>
          </div>
          <div className="pp-prose-block"><h3>Overview</h3><p className="pp-copy">{latest!.content.overview || "No overview added."}</p></div>
          {latest!.content.terms && <div className="pp-prose-block"><h3>Terms & exclusions</h3><p className="pp-copy">{latest!.content.terms}</p></div>}
        </section>
        <div className="pp-stack">
          <section className="pp-card">
            <SectionTitle title="Client response" description={responseRevision?.response ? "Recorded decision on revision " + responseRevision.number + "." : "Responses appear here after your client submits a decision."}/>
            {responseRevision?.response ? <>
              <StatePill state={responseRevision.response.outcome}/>
              <p className="pp-response-total">{money(responseRevision.response.total_minor, responseRevision.content.currency)}</p>
              <p className="pp-muted">{responseRevision.response.name || "Client"} · {formatDate(responseRevision.response.created_at)}</p>
              {responseRevision.response.note && <p className="pp-copy pp-response-note">{responseRevision.response.note}</p>}
              <DecisionCounts reply={responseRevision.response}/>
              <button onClick={() => changeTab("history")}>Review decisions <Icon name="arrow"/></button>
            </> : <div className="pp-waiting"><Icon name="clock"/><span>Awaiting a client response</span></div>}
          </section>
          {!!accepted?.content.opportunity_id && <section className="pp-card">
            <SectionTitle title="CRM opportunity" description={"Opportunity #" + accepted.content.opportunity_id}/>
            <button disabled={busy} onClick={async () => {
              if (!(await confirm("Update the value of CRM opportunity #" + accepted.content.opportunity_id + "?", "Update CRM", "Sync accepted value"))) return;
              void run(async () => {
                await request("crm_sync", {proposal_id: record!.id});
                setNotice("Accepted value synced to CRM.");
              });
            }}><Icon name="refresh"/> Sync accepted value to CRM</button>
          </section>}
          {open && <button className="pp-next-step" onClick={() => changeTab("sharing")}><span><Icon name="link"/> Share this proposal</span><Icon name="arrow"/></button>}
        </div>
      </div>)}

      {detailVisible && tab === "scope" && (editing ? <div className="pp-stack">
        <div className="pp-section-toolbar">
          <SectionTitle title="Scope & pricing" description="Group work into categories. Clients can include, defer, or decline optional items."/>
          <div className="pp-actions"><button disabled={busy || !draft.client_id} onClick={() => setBacklogPicker(true)}>Add from client backlog</button>
          <button disabled={busy || draft.categories.length >= 20} onClick={() => setDraft({
            ...draft, categories: [...draft.categories, {name: "", rule: "any", items: []}],
          })}><Icon name="plus"/> Add category</button></div>
        </div>
        {!draft.categories.length && <Empty title="Build your scope" icon="layers"
          description="Start with a category such as Core build, Integrations, or Support. Add items and set how your client can choose.">
          <button className="pp-primary" onClick={() => setDraft({
            ...draft, categories: [{name: "", rule: "any", items: []}],
          })}><Icon name="plus"/> Add first category</button>
        </Empty>}
        {draft.categories.map((group, i) => <fieldset className="pp-scope-category" key={i}>
          <legend className="pp-sr">Category {i + 1}</legend>
          <div className="pp-category-header">
            <span className="pp-category-number">{String(i + 1).padStart(2, "0")}</span>
            <label className="pp-category-name">Category name<input aria-label={"Category " + (i + 1) + " name"} maxLength={120}
              placeholder="e.g. Core build" value={group.name} onChange={(event) => updateCategory(i, {name: event.target.value})}/></label>
            <label className="pp-category-rule">Selection rule<select value={group.rule} onChange={(event) => {
              const rule = event.target.value as Rule;
              updateCategory(i, {rule, items: rule === "any" ? group.items : group.items.map((item) => ({...item, required: false}))});
            }}>{Object.entries(rules).map(([rule, name]) => <option key={rule} value={rule}>{name}</option>)}</select></label>
            <button className="pp-icon-button pp-danger-text" aria-label={"Remove category " + (i + 1)}
              onClick={async () => {
                if (group.items.length && !(await confirm("Remove this category and its items?", "Remove category", "Remove category"))) return;
                setDraft({...draft, categories: draft.categories.filter((_, index) => index !== i)});
              }}><Icon name="trash"/></button>
          </div>
          <div className="pp-category-body">
            {group.items.length === 0 && <p className="pp-muted pp-no-items">Add the first item in this category.</p>}
            {group.items.map((line, j) => <fieldset className="pp-scope-item" key={j}>
              <legend className="pp-sr">Item {j + 1}</legend>
              <div className="pp-item-main">
                <label>Item name<input maxLength={160} value={line.name} placeholder="e.g. Stripe billing"
                  onChange={(event) => updateLine(i, j, {name: event.target.value})}/></label>
                <label>Description<textarea maxLength={2000} value={line.description || ""}
                  placeholder="Explain what is included and the outcome."
                  onChange={(event) => updateLine(i, j, {description: event.target.value})}/></label>
              </div>
              <div className="pp-item-settings">
                <label>Price ({draft.currency})<input inputMode="decimal" value={line.amount}
                  onChange={(event) => updateLine(i, j, {amount: event.target.value})}/></label>
                {group.rule === "any" ? <label className="pp-check"><input type="checkbox" checked={!!line.required}
                  onChange={(event) => updateLine(i, j, {required: event.target.checked})}/> Required item</label>
                  : <p className="pp-muted">Part of a pick-one category</p>}
                <button className="pp-text-button pp-danger-text" onClick={async () => {
                  if ((line.name || line.description || Number(line.amount)) && !(await confirm("Remove this item from the draft?", "Remove item", "Remove item"))) return;
                  updateCategory(i, {items: group.items.filter((_, index) => index !== j)});
                }}><Icon name="trash"/> Remove item</button>
              </div>
            </fieldset>)}
            <button className="pp-add-item" disabled={draftItemCount >= 100}
              onClick={() => updateCategory(i, {items: [...group.items, {name: "", amount: "0.00"}]})}><Icon name="plus"/> Add item</button>
          </div>
        </fieldset>)}
        {!!draft.categories.length && <p className="pp-muted">{draft.categories.length} categories · {draftItemCount} items · Prices in {draft.currency}</p>}
      </div> : <SelectionPreview content={shown!.content} reply={shown!.response}/>)}

      {detailVisible && tab === "preview" && (previewError
        ? <div className="pp-notice pp-error" role="alert">{previewError} <button onClick={() => changeTab("scope")}>Back to pricing</button></div>
        : <div className="pp-stack">
          <div className="pp-preview-intro"><span><Icon name="file"/> Client preview</span><p>Try the item choices and review the total before sharing.</p></div>
          <SelectionPreview key={JSON.stringify(preview)} content={preview!}/>
        </div>)}

      {detailVisible && tab === "sharing" && (open ? <div className="pp-share-grid">
        <section className="pp-card">
          <SectionTitle title="Private client link" description="Anyone with this link can review this revision and submit a decision."/>
          <div className="pp-live-banner"><StatePill state="published"/><span>Revision {open.number} · Open until {formatDate(open.expires_at!)}</span></div>
          <button className="pp-primary" disabled={busy} onClick={() => run(() => shareLink())}><Icon name="link"/> Copy private link</button>
          {url && <div className="pp-link-field"><label>Private client link<input readOnly value={url} onFocus={(event) => event.target.select()}/></label>
            <a className="pp-button" href={url} target="_blank" rel="noopener noreferrer">Open client page <Icon name="arrow"/></a></div>}
          <div className="pp-section-divider">
            <h3>Replace a shared link</h3><p>Issue a new link if you need to revoke access through the previous one.</p>
            <button disabled={busy} onClick={async () => {
              if (await confirm("Revoke the current link and issue a new one?", "Replace link", "Replace private link")) void run(() => shareLink(true));
            }}><Icon name="refresh"/> Rotate link</button>
          </div>
        </section>
        <div className="pp-stack">
          <section className="pp-card">
            <SectionTitle title="Email invitation" description="Send through your project’s configured Messaging sender."/>
            <dl className="pp-recipient"><dt>Recipient</dt><dd>{open.content.client_name || "Client"}</dd><dd>{open.content.client_email || "No email on this revision"}</dd></dl>
            <div className="pp-actions">
              <button disabled={busy || !open.content.client_email} onClick={() => email(false)}><Icon name="send"/> Email invitation</button>
              <button disabled={busy || !open.content.client_email} onClick={() => email(true)}>Send reminder</button>
            </div>
          </section>
          <section className="pp-card">
            <SectionTitle title="Close this revision" description="Withdraw it to stop new responses. Its record stays in your history."/>
            <button className="pp-danger-text" disabled={busy} onClick={async () => {
              if (!(await confirm("Close this revision to new client responses?", "Withdraw proposal", "Withdraw proposal"))) return;
              void run(async () => { show(await request<Detail>("withdraw", fields(open))); await reload(); });
            }}>Withdraw proposal</button>
          </section>
        </div>
      </div> : <Empty title={editing ? "Publish when you’re ready" : "This proposal is closed"} icon="link"
        description={editing ? "Save and publish your draft to get a private client link and send an invitation." : "Review its client response and revision history for the recorded scope."}>
        {editing && <button onClick={() => changeTab("preview")}>Review client preview</button>}
      </Empty>)}

      {detailVisible && tab === "history" && (record ? <div className="pp-history-grid">
        <nav className="pp-card pp-history-list" aria-label="Revision history">
          <SectionTitle title="Revisions" description="Published scope and client decisions."/>
          {record.revisions.map((r) => <button className={shown?.id === r.id ? "is-selected" : ""} key={r.id} aria-current={shown?.id === r.id}
            onClick={() => setHistoryId(r.id)}>
            <span><strong>Revision {r.number}</strong><StatePill state={r.state}/></span>
            <small>{r.response ? "Response · " + formatDate(r.response.created_at) : r.published_at ? "Published · " + formatDate(r.published_at) : "Draft"}</small>
          </button>)}
        </nav>
        <section className="pp-stack">
          <div className="pp-section-toolbar"><div><h2>{shown!.content.title}</h2><p className="pp-muted">Revision {shown!.number}</p></div>
            <button disabled={busy} onClick={() => start(shown!.content)}>Use as new proposal</button></div>
          <SelectionPreview key={shown!.id} content={shown!.content} reply={shown!.response} readOnly/>
        </section>
      </div> : <Empty title="Your revision history will appear here" icon="clock" description="Save your first draft to start the proposal record."/> )}
    </main>
    {backlogPicker && <BacklogPicker request={request} clientID={draft.client_id!} currency={draft.currency}
      imported={draft.categories.flatMap((group) => group.items.map((item) => item.backlog_item_id || 0))}
      onClose={() => setBacklogPicker(false)} onImport={async (ids) => {
        setBusy(true);
        try {
          const saved = await save(false);
          const imported = await request<Detail>("backlog_add_to_draft", {
            ...fields(saved.revisions[0]), client_id: saved.revisions[0].content.client_id, item_ids: ids,
          });
          show(imported, "scope");setBacklogPicker(false);setNotice("Selected backlog items copied into the draft.");await reload();
        } finally {setBusy(false);}
      }}/>}
    {editing && detailVisible && <footer className="pp-save-bar">
      <span className="pp-muted"><span className={"pp-save-dot" + (dirty ? " is-dirty" : "")}/>{dirty ? "Unsaved draft" : "All changes saved"}</span>
      <div className="pp-actions">
        <button disabled={busy} onClick={() => changeTab("preview")}>Preview</button>
        <button disabled={busy} onClick={() => run(() => save(false))}>Save draft</button>
        <button className="pp-primary" disabled={busy} onClick={async () => {
          if (open && !(await confirm("Publish this draft and close the previous client link?", "Publish revision", "Publish updated proposal"))) return;
          void run(() => save(true));
        }}>{busy ? "Saving…" : "Save and publish"}<Icon name="arrow"/></button>
      </div>
    </footer>}
  </div>;
}

function requiredTotal(content: Content) {
  return (content.categories || []).reduce((total, group) => total +
    (group.items || []).reduce((sum, item) => sum + (item.required ? item.amount_minor : 0), 0), 0);
}
function formatDate(timestamp: number) {
  return new Date(timestamp * 1000).toLocaleDateString(undefined, {day: "numeric", month: "short", year: "numeric"});
}
function SectionTitle({title, description}: {title: string; description?: string}) {
  return <div className="pp-section-title"><h2>{title}</h2>{description && <p>{description}</p>}</div>;
}
function Empty({title, description, icon, children}: {
  title: string; description: string; icon: keyof typeof iconPaths; children?: ReactNode;
}) {
  return <div className="pp-empty"><span className="pp-empty-icon"><Icon name={icon}/></span><h2>{title}</h2><p>{description}</p>{children}</div>;
}
function DecisionCounts({reply}: {reply: Reply}) {
  const choices = Object.values(reply.selection);
  return <div className="pp-decision-counts">
    <span><strong>{choices.filter((choice) => choice === "include").length}</strong> Included</span>
    <span><strong>{choices.filter((choice) => choice === "later").length}</strong> For later</span>
    <span><strong>{choices.filter((choice) => choice === "decline").length}</strong> Declined</span>
  </div>;
}
function SelectionPreview({content, reply, readOnly = false}: {content: Content; reply?: Reply; readOnly?: boolean}) {
  const [choices, setChoices] = useState<Record<string, Choice>>({});
  const text = proposalLocale(content.language);
  const selection = reply?.selection || choices;
  const format = (amount: number) => new Intl.NumberFormat(text.locale, {style: "currency", currency: content.currency}).format(amount / 100);
  const values: Choice[] = ["include", "later", "decline"];
  let total = 0;
  const categories = (content.categories || []).map((group, i) => {
    const items = group.items || [];
    const included = items.filter((line, j) => line.required || selection[i + ":" + j] === "include");
    const subtotal = included.reduce((sum, line) => sum + line.amount_minor, 0);
    total += subtotal;
    return <section className="pp-preview-category" key={i}>
      <div className="pp-preview-category-head"><h3>{group.name || text.untitledCategory}</h3><span>{text.groupCount(included.length, items.length)} · {format(subtotal)}</span></div>
      <p className="pp-muted">{text.rules[group.rule]}</p>
      {!reply && !readOnly && group.rule === "any" && items.some((line) => !line.required) && <div className="pp-visual-choices pp-group-choices" role="group" aria-label={text.groupChoices + " " + group.name}>
        {values.map((value) => {
          const active = items.every((line, j) => line.required || selection[i + ":" + j] === value);
          return <button key={value} type="button" data-choice={value} aria-pressed={active} onClick={() => setChoices((previous) => {
            const next = {...previous};
            items.forEach((line, j) => { if (!line.required) next[i + ":" + j] = active ? "unselected" : value; });
            return next;
          })}>{value === "include" ? text.includeGroup : text.decisions[value]}</button>;
        })}
      </div>}
      {items.map((line, j) => {
        const key = i + ":" + j;
        const choice = selection[key] || "unselected";
        return <div className="pp-preview-item" key={j}>
          <div><strong>{line.name || text.untitledItem}</strong>{line.description && <p className="pp-copy">{line.description}</p>}</div>
          <span className="pp-amount">{format(line.amount_minor)}</span>
          {line.required ? <span className="pp-required"><Icon name="check"/> {text.required}</span> :
            reply || readOnly ? <span className={"pp-choice pp-choice-" + choice}>{text.decisions[choice]}</span> :
            <div className="pp-visual-choices" role="group" aria-label={text.itemChoices + " " + line.name}>
              {values.map((value) => <button key={value} type="button" data-choice={value} aria-pressed={choice === value} onClick={() => {
                setChoices((previous) => {
                  const next = {...previous, [key]: previous[key] === value ? "unselected" : value};
                  if (next[key] === "include" && group.rule !== "any") {
                    items.forEach((_, index) => {
                      const other = i + ":" + index;
                      if (other !== key && next[other] === "include") next[other] = "unselected";
                    });
                  }
                  return next;
                });
              }}>{text.decisions[value]}</button>)}
            </div>}
        </div>;
      })}
    </section>;
  });
  return <div className="pp-client-preview" lang={text.lang}>
    <section className="pp-card pp-preview-document">
      <header className="pp-document-header"><p className="pp-eyebrow">{content.sender || text.business} · {text.proposal}</p>
        <h2>{content.title || text.untitledProposal}</h2>{content.client_name && <p>{text.prepared} {content.client_name}</p>}
        {content.overview && <p className="pp-copy pp-document-overview">{content.overview}</p>}
      </header>
      {categories.length ? categories : <p className="pp-muted">{text.previewEmpty}</p>}
      {content.terms && <section className="pp-preview-terms"><h3>{text.terms}</h3><p className="pp-copy">{content.terms}</p></section>}
    </section>
    <aside className="pp-card pp-preview-summary">
      <SectionTitle title={reply ? text.outcomes[reply.outcome as keyof typeof text.outcomes] : text.total} description={reply ? text.recorded : text.livePricing}/>
      <p className="pp-price">{format(reply?.total_minor ?? total)}</p>
      <span className="pp-muted">{content.currency} · {reply?.outcome === "accepted" ? text.acceptedTotal : text.total}</span>
      {reply && <div className="pp-section-divider">
        <strong>{reply.name || "Client"}</strong><p>{new Intl.DateTimeFormat(text.locale, {dateStyle: "long"}).format(new Date(reply.created_at * 1000))}</p>
        {reply.note && <p className="pp-copy">{reply.note}</p>}
      </div>}
      {!reply && <div className="pp-preview-help"><Icon name="link"/><p>{readOnly ? text.saved : text.help}</p></div>}
    </aside>
  </div>;
}

const styles = `
.pp-language-field { display: grid; gap: 10px; font-size: 12px; }
.pp-language-field > small { color: var(--pp-muted); line-height: 1.5; }
.pp-visual-choices { display: flex; gap: 7px; flex-wrap: wrap; }
.pp-preview-item > .pp-visual-choices { grid-column: 1/-1; }
.ap-proposals .pp-visual-choices button { min-height: 40px; padding: 8px 12px; font-size: 12px; }
.ap-proposals .pp-visual-choices button[aria-pressed="true"] { background: var(--pp-accent); color: var(--pp-on-accent, #fff); border-color: var(--pp-accent); }
.ap-proposals .pp-visual-choices button[data-choice="later"][aria-pressed="true"] { background: var(--pp-warn); border-color: var(--pp-warn); color: #1b1b1b; }
.ap-proposals .pp-visual-choices button[data-choice="decline"][aria-pressed="true"] { background: var(--pp-error); border-color: var(--pp-error); color: #fff; }
.pp-group-choices { margin: 14px 0 8px; }

.pp-crm-picker { display: grid; gap: 12px; }
.pp-crm-picker.pp-section-divider { margin-top: 0; }
.ap-proposals .pp-crm-picker p { margin: 0; }
.pp-crm-results { list-style: none; padding: 0; margin: 0; max-height: 265px; overflow: auto;
  border: 1px solid var(--pp-line); border-radius: var(--radius-md, 8px); }
.pp-crm-results li + li { border-top: 1px solid var(--pp-line); }
.ap-proposals .pp-crm-results button { width: 100%; display: grid; gap: 4px; padding: 12px 14px;
  text-align: left; justify-content: stretch; white-space: normal; border: 0; border-radius: 0; background: transparent; }
.pp-crm-results button span { color: var(--pp-muted); font-size: 12px; }
.pp-crm-results button small { color: var(--pp-accent); font-size: 11px; }
.ap-proposals .pp-crm-results button.is-selected { background: var(--pp-hover); box-shadow: inset 3px 0 var(--pp-accent); }
.pp-crm-search-error { display: grid; gap: 8px; justify-items: start; }

.pp-backlog-layout { display: grid; grid-template-columns: 280px minmax(0, 1fr); gap: 24px; align-items: start; }
.pp-client-list { display: grid; gap: 20px; padding: 20px; }
.pp-client-list nav { display: grid; gap: 6px; max-height: 58vh; overflow: auto; }
.ap-proposals .pp-client-list nav button { display: grid; justify-content: stretch; text-align: left; gap: 5px; border-color: transparent; white-space: normal; }
.pp-client-list nav small { color: var(--pp-muted); font-size: 11px; }
.ap-proposals .pp-client-list nav button[aria-current="true"] { background: var(--pp-hover); border-color: var(--pp-accent); }
.pp-backlog-controls, .pp-backlog-selection { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.pp-backlog-selection { position: sticky; bottom: -28px; background: var(--pp-bg); border-top: 1px solid var(--pp-line); padding: 18px 0; z-index: 2; }
.ap-proposals .pp-backlog-table :is(th,td):first-child { width: 40px; min-width: 40px; }
.pp-backlog-table td:nth-child(2) { min-width: 220px; }
.pp-backlog-description { max-width: 480px; white-space: pre-wrap; }
.pp-private-note { color: var(--pp-accent) !important; }
.pp-client-fields-divider { border-top: 1px solid var(--pp-line); }
.ap-proposals .pp-dialog { width: min(940px, calc(100vw - 36px)); max-height: min(92vh, 1100px); margin: auto; padding: 0;
  border: 1px solid var(--pp-line); border-radius: var(--pp-radius); background: var(--pp-card); color: var(--pp-text);
  font: inherit; overflow: auto; box-shadow: var(--shadow-popover, 0 16px 60px #0005); }
.ap-proposals .pp-dialog.pp-confirm-dialog { width: min(480px, calc(100vw - 36px)); }
.ap-proposals .pp-dialog::backdrop { background: var(--color-bg-overlay, var(--bg-overlay, #0009)); }
.pp-dialog > header { position: sticky; top: 0; z-index: 1; background: var(--pp-card); border-bottom: 1px solid var(--pp-line);
  padding: 18px 24px; display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.pp-dialog form, .pp-dialog-body { display: grid; gap: 20px; padding: 24px; }
.pp-dialog footer { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 10px; padding-top: 20px; border-top: 1px solid var(--pp-line); }
.pp-error-text { color: var(--pp-error); font-size: 12px; }
@media (max-width: 900px) { .pp-backlog-layout { grid-template-columns: 1fr; }.pp-client-list nav { max-height: 180px; } }
@media (max-width: 700px) {
  .ap-proposals .pp-backlog-table :is(th,td):nth-child(n) { display: table-cell; }
  .ap-proposals .pp-backlog-table :is(th,td) { padding: 12px; }
  .ap-proposals .pp-backlog-table :is(th,td):first-child { width: 30px; }
  .pp-backlog-table .pp-actions { flex-wrap: nowrap; }
  .pp-dialog form, .pp-dialog-body { padding: 18px; }.pp-backlog-selection { bottom: -18px; }
}

.ap-proposals {
  --pp-bg: var(--color-bg, var(--bg, #11161c));
  --pp-card: var(--color-bg-card, var(--bg-card, #1a222b));
  --pp-input: var(--color-bg-input, var(--bg-input, #141c24));
  --pp-hover: var(--color-bg-hover, var(--bg-hover, #232d38));
  --pp-text: var(--color-text, var(--text, #e8edf2));
  --pp-muted: var(--color-text-muted, var(--text-muted, #a7b0ba));
  --pp-dim: var(--color-text-dim, var(--text-dim, #7c8793));
  --pp-line: var(--color-border, var(--border, #2b3642));
  --pp-accent: var(--color-accent, var(--accent, #4f9cf7));
  --pp-success: var(--color-success, var(--success, #4ade80));
  --pp-warn: var(--color-warn, var(--warn, #fbbf24));
  --pp-error: var(--color-error, var(--error, #f87171));
  --pp-radius: var(--radius-lg, 10px);
  color: var(--pp-text); background: var(--pp-bg);
  font-family: var(--font-base, system-ui, sans-serif); font-size: 14px; line-height: 1.5;
  width: 100%; height: 100%; min-height: 100%; overflow: auto; padding: 28px; box-sizing: border-box;
}
.ap-proposals *, .ap-proposals *::before, .ap-proposals *::after { box-sizing: border-box; }
.ap-proposals :is(h1,h2,h3,p,fieldset,dl,dd) { margin: 0; }
.ap-proposals h1 { font-size: 24px; font-weight: 650; letter-spacing: -.025em; line-height: 1.3; overflow-wrap: anywhere; }
.ap-proposals h2 { font-size: 16px; font-weight: 600; line-height: 1.4; }
.ap-proposals h3 { font-size: 14px; font-weight: 600; }
.ap-proposals p, .ap-proposals small { overflow-wrap: anywhere; }
.ap-proposals svg { flex-shrink: 0; vertical-align: middle; }
.ap-proposals button, .ap-proposals .pp-button {
  display: inline-flex; align-items: center; justify-content: center; gap: 7px; padding: 9px 13px;
  border: 1px solid var(--pp-line); border-radius: var(--radius-md, 8px); background: var(--pp-card);
  color: var(--pp-text); font: inherit; font-size: 13px; line-height: 1.4; font-weight: 500;
  cursor: pointer; text-decoration: none; white-space: nowrap;
}
.ap-proposals button:hover, .ap-proposals .pp-button:hover { background: var(--pp-hover); border-color: var(--pp-dim); }
.ap-proposals button:disabled { opacity: .45; cursor: default; }
.ap-proposals button.pp-primary { background: var(--pp-accent); border-color: var(--pp-accent); color: var(--pp-bg); font-weight: 650; }
.ap-proposals button.pp-primary:hover { background: var(--color-accent-hover, var(--accent-hover, var(--pp-accent))); }
.ap-proposals :is(button,a,input,select,textarea):focus-visible { outline: 2px solid var(--pp-accent); outline-offset: 3px; }
.ap-proposals :is(input,select,textarea) {
  width: 100%; min-width: 0; padding: 10px 12px; background: var(--pp-input); color: var(--pp-text);
  border: 1px solid var(--pp-line); border-radius: var(--radius-md, 8px); font: inherit; font-size: 13px; line-height: 1.5;
}
.ap-proposals :is(input,textarea)::placeholder { color: var(--pp-dim); opacity: 1; }
.ap-proposals select option { background: var(--pp-card); color: var(--pp-text); }
.ap-proposals textarea { min-height: 104px; resize: vertical; }
.ap-proposals label { display: grid; gap: 7px; font-size: 12px; font-weight: 550; color: var(--pp-muted); min-width: 0; }
.ap-proposals input[type="checkbox"] { width: 15px; height: 15px; accent-color: var(--pp-accent); margin: 0; }
.pp-header { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 24px; }
.pp-title { min-width: 0; }.pp-title-line { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ap-proposals .pp-title > p { color: var(--pp-muted); margin-top: 5px; font-size: 13px; }
.pp-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.ap-proposals button.pp-back { border: 0; background: transparent; color: var(--pp-muted); padding: 0; margin-bottom: 18px; font-size: 12px; }
.pp-tabs { display: flex; gap: 24px; border-bottom: 1px solid var(--pp-line); margin-bottom: 24px; overflow-x: auto; }
.ap-proposals .pp-tabs button { border: 0; border-bottom: 2px solid transparent; border-radius: 0; background: transparent; padding: 10px 0 13px; color: var(--pp-muted); }
.ap-proposals .pp-tabs button[aria-selected="true"] { border-bottom-color: var(--pp-accent); color: var(--pp-accent); }
.pp-list-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 16px; }
.ap-proposals .pp-search { position: relative; display: flex; align-items: center; width: min(380px, 100%); }
.pp-search svg { position: absolute; left: 12px; color: var(--pp-dim); }.ap-proposals .pp-search input { padding-left: 36px; }
.pp-muted { color: var(--pp-muted); font-size: 12px; }.pp-amount { font-variant-numeric: tabular-nums; white-space: nowrap; }
.pp-table-wrap { border: 1px solid var(--pp-line); border-radius: var(--pp-radius); overflow-x: auto; background: var(--pp-card); }
.pp-table { width: 100%; border-collapse: collapse; text-align: left; font-size: 13px; }
.pp-table th { padding: 13px 18px; color: var(--pp-muted); font-size: 11px; font-weight: 500; text-transform: uppercase; letter-spacing: .055em; background: var(--pp-bg); white-space: nowrap; }
.pp-table td { padding: 18px; border-top: 1px solid var(--pp-line); vertical-align: middle; }
.pp-table tbody tr:hover { background: var(--pp-hover); }.pp-table td:first-child { min-width: 200px; }
.pp-table small { display: block; margin-top: 3px; color: var(--pp-dim); font-size: 11px; }
.ap-proposals button.pp-table-title { border: 0; background: transparent; padding: 0; text-align: left; font-weight: 600; white-space: normal; }
.ap-proposals button.pp-table-title:hover { color: var(--pp-accent); }
.ap-proposals .pp-icon-button { padding: 8px; }.pp-load-more { text-align: center; padding: 20px; }
.pp-pill { display: inline-flex; align-items: center; gap: 6px; border-radius: 999px; padding: 3px 9px; border: 1px solid var(--pp-line); color: var(--pp-muted); font-size: 11px; font-weight: 500; white-space: nowrap; }
.pp-pill > span { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.pp-state-published { color: var(--pp-accent); background: color-mix(in srgb, var(--pp-accent) 9%, transparent); border-color: color-mix(in srgb, var(--pp-accent) 30%, transparent); }
.pp-state-accepted { color: var(--pp-success); background: color-mix(in srgb, var(--pp-success) 8%, transparent); border-color: color-mix(in srgb, var(--pp-success) 28%, transparent); }
.pp-state-changes_requested { color: var(--pp-warn); background: color-mix(in srgb, var(--pp-warn) 8%, transparent); border-color: color-mix(in srgb, var(--pp-warn) 28%, transparent); }
.pp-state-declined { color: var(--pp-error); background: color-mix(in srgb, var(--pp-error) 8%, transparent); border-color: color-mix(in srgb, var(--pp-error) 28%, transparent); }
.pp-empty { width: 100%; min-height: 310px; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 52px 24px; border: 1px dashed var(--pp-line); border-radius: var(--pp-radius); text-align: center; gap: 12px; }
.pp-empty p { color: var(--pp-muted); max-width: 450px; font-size: 13px; }.pp-empty button { margin-top: 6px; }
.pp-empty-icon { display: inline-flex; padding: 12px; border: 1px solid var(--pp-line); background: var(--pp-card); border-radius: var(--pp-radius); color: var(--pp-muted); margin-bottom: 4px; }
.pp-empty-icon svg { width: 22px; height: 22px; }
.pp-card { background: var(--pp-card); border: 1px solid var(--pp-line); border-radius: var(--pp-radius); padding: 24px; min-width: 0; box-shadow: var(--shadow-card, none); }
.pp-section-title { margin-bottom: 22px; }.ap-proposals .pp-section-title p { margin-top: 5px; color: var(--pp-muted); font-size: 12px; max-width: 600px; }
.pp-section-toolbar { display: flex; align-items: start; justify-content: space-between; gap: 20px; }.pp-section-toolbar .pp-section-title { margin-bottom: 0; }
.pp-editor-grid { display: grid; grid-template-columns: minmax(0, 1.65fr) minmax(300px, 1fr); gap: 22px; align-items: start; }
.pp-share-grid { display: grid; grid-template-columns: minmax(0, 1.3fr) minmax(300px, 1fr); gap: 22px; align-items: start; }
.pp-stack { display: grid; gap: 20px; min-width: 0; }.pp-fields { display: grid; gap: 22px; }.pp-grid-two { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.ap-proposals .pp-overview-input { min-height: 146px; }
.pp-inline-field { display: flex; align-items: end; gap: 10px; }.pp-inline-field label { flex: 1; }
.pp-linked { display: flex; align-items: center; justify-content: space-between; gap: 8px; border-top: 1px solid var(--pp-line); margin-top: 18px; padding-top: 15px; font-size: 12px; }.pp-linked > span { display: inline-flex; align-items: center; gap: 7px; }
.ap-proposals button.pp-next-step { display: flex; justify-content: space-between; padding: 18px 20px; color: var(--pp-accent); width: 100%; }.pp-next-step > span { display: inline-flex; align-items: center; gap: 8px; }
.pp-scope-category { border: 1px solid var(--pp-line); border-radius: var(--pp-radius); overflow: hidden; padding: 0; min-width: 0; background: var(--pp-card); }
.pp-category-header { display: flex; gap: 18px; align-items: center; padding: 20px 24px; background: color-mix(in srgb, var(--pp-hover) 45%, var(--pp-card)); border-bottom: 1px solid var(--pp-line); }
.pp-category-number { font-size: 12px; color: var(--pp-dim); }.pp-category-name { flex: 1; }.pp-category-rule { width: 220px; }.pp-category-header > button { align-self: end; margin-bottom: 1px; }
.pp-category-body { padding: 0 24px 20px; }.pp-no-items { padding: 24px 0; }
.pp-scope-item { display: grid; grid-template-columns: minmax(0, 1fr) 230px; gap: 28px; border: 0; border-bottom: 1px solid var(--pp-line); padding: 22px 0; min-width: 0; }
.pp-item-main { display: grid; gap: 15px; min-width: 0; }.ap-proposals .pp-item-main textarea { min-height: 72px; }
.pp-item-settings { display: grid; align-content: start; gap: 18px; }.ap-proposals label.pp-check { display: flex; align-items: center; gap: 8px; color: var(--pp-text); }
.ap-proposals button.pp-text-button { border: 0; padding: 0; background: transparent; justify-content: start; font-size: 12px; }
.ap-proposals .pp-danger-text { color: var(--pp-error); }.ap-proposals button.pp-add-item { margin-top: 18px; border-style: dashed; background: transparent; }
.pp-save-bar { display: flex; justify-content: space-between; align-items: center; gap: 16px; position: sticky; bottom: -28px; padding: 18px 0; margin-top: 24px; border-top: 1px solid var(--pp-line); background: var(--pp-bg); z-index: 2; }
.pp-save-dot { display: inline-block; width: 6px; height: 6px; background: var(--pp-success); border-radius: 50%; margin-right: 7px; }.pp-save-dot.is-dirty { background: var(--pp-warn); }
.pp-notice { display: flex; align-items: center; gap: 10px; padding: 12px 15px; border: 1px solid var(--pp-line); border-left: 3px solid var(--pp-success); background: var(--pp-card); border-radius: var(--radius-md, 8px); margin-bottom: 20px; font-size: 13px; overflow-wrap: anywhere; }.pp-notice.pp-error { border-left-color: var(--pp-error); color: var(--pp-error); }
.pp-metadata { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; padding-bottom: 24px; border-bottom: 1px solid var(--pp-line); }.pp-metadata span, .pp-metadata small { display: block; font-size: 12px; color: var(--pp-muted); margin-bottom: 4px; }.pp-metadata strong { display: block; font-size: 14px; font-weight: 550; }
.pp-prose-block { margin-top: 24px; }.ap-proposals .pp-prose-block p { color: var(--pp-muted); margin-top: 10px; }.pp-copy { white-space: pre-wrap; line-height: 1.7; overflow-wrap: anywhere; }
.pp-waiting { display: flex; align-items: center; gap: 9px; padding: 18px 0; color: var(--pp-muted); font-size: 13px; }
.pp-response-total { font-size: 28px; font-weight: 650; letter-spacing: -.025em; padding-top: 16px; }.pp-response-note { margin-top: 18px !important; }
.pp-decision-counts { display: flex; gap: 20px; flex-wrap: wrap; margin: 20px 0; padding-top: 18px; border-top: 1px solid var(--pp-line); font-size: 11px; color: var(--pp-muted); }.pp-decision-counts strong { display: block; color: var(--pp-text); font-size: 18px; font-weight: 600; margin-bottom: 2px; }
.pp-live-banner { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; color: var(--pp-muted); font-size: 12px; padding-bottom: 22px; }.pp-link-field { display: grid; gap: 12px; margin-top: 20px; }.pp-link-field a { justify-self: start; }
.pp-section-divider { border-top: 1px solid var(--pp-line); padding-top: 22px; margin-top: 24px; }.ap-proposals .pp-section-divider p { color: var(--pp-muted); margin: 8px 0 18px; font-size: 12px; }
.pp-recipient { font-size: 13px; margin-bottom: 22px !important; }.pp-recipient dt { font-size: 11px; color: var(--pp-muted); margin-bottom: 7px; }.pp-recipient dd + dd { color: var(--pp-muted); font-size: 12px; margin-top: 3px; }
.pp-preview-intro { display: flex; justify-content: space-between; gap: 16px; flex-wrap: wrap; color: var(--pp-muted); font-size: 12px; }.pp-preview-intro span { display: flex; align-items: center; gap: 8px; }
.pp-client-preview { display: grid; grid-template-columns: minmax(0, 1fr) minmax(270px, 320px); gap: 24px; align-items: start; }
.pp-preview-document { padding: 32px; }.pp-document-header { padding-bottom: 28px; }.pp-eyebrow { color: var(--pp-accent); text-transform: uppercase; letter-spacing: .07em; font-size: 11px; font-weight: 600; }
.ap-proposals .pp-document-header h2 { font-size: 25px; font-weight: 650; margin: 14px 0 8px; letter-spacing: -.025em; }.pp-document-header > p:not(.pp-eyebrow) { color: var(--pp-muted); font-size: 13px; }.pp-document-overview { margin-top: 24px !important; }
.pp-preview-category { border-top: 1px solid var(--pp-line); padding: 24px 0 8px; }.pp-preview-category-head { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; margin-bottom: 8px; }.pp-preview-category-head span { color: var(--pp-dim); font-size: 11px; }
.pp-preview-item { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 20px; padding: 18px 0; }.pp-preview-item strong { font-size: 13px; font-weight: 550; }.pp-preview-item p { color: var(--pp-muted); font-size: 12px; margin-top: 5px; }.pp-preview-item .pp-amount { font-size: 13px; }
.pp-required { display: flex; align-items: center; gap: 5px; color: var(--pp-dim); font-size: 11px; }.pp-required svg { width: 13px; height: 13px; }
.pp-preview-summary { position: sticky; top: 0; }.pp-preview-summary .pp-section-title { margin-bottom: 18px; }.pp-price { font-size: 30px; font-weight: 650; letter-spacing: -.035em; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }.pp-preview-help { border-top: 1px solid var(--pp-line); margin-top: 24px; padding-top: 20px; color: var(--pp-muted); display: flex; align-items: start; gap: 9px; font-size: 12px; }.pp-preview-terms { border-top: 1px solid var(--pp-line); padding-top: 24px; margin-top: 20px; }.pp-preview-terms p { color: var(--pp-muted); margin-top: 10px !important; font-size: 12px; }
.pp-choice { font-size: 12px; color: var(--pp-muted); }.pp-choice-include { color: var(--pp-success); }.pp-choice-later { color: var(--pp-warn); }.pp-choice-decline { color: var(--pp-error); }
.pp-history-grid { display: grid; grid-template-columns: 260px minmax(0, 1fr); gap: 24px; align-items: start; }.pp-history-list { padding: 18px; }.ap-proposals .pp-history-list button { display: grid; gap: 9px; width: 100%; padding: 14px 10px; border-color: transparent; background: transparent; text-align: left; margin-top: 8px; white-space: normal; }.pp-history-list button > span { display: flex; justify-content: space-between; align-items: center; gap: 10px; }.pp-history-list small { color: var(--pp-muted); font-size: 11px; }.ap-proposals .pp-history-list .is-selected { background: var(--pp-hover); border-color: var(--pp-line); }
.pp-history-grid .pp-client-preview { grid-template-columns: minmax(0, 1fr); }.pp-history-grid .pp-preview-summary { position: static; }.pp-history-grid .pp-price { font-size: 26px; }
.pp-sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0,0,0,0); white-space: nowrap; border: 0; }
@media (min-width: 1800px) { .pp-editor-grid { grid-template-columns: minmax(0, 2fr) minmax(400px, 1fr); }.pp-client-preview { grid-template-columns: minmax(0, 1fr) 380px; } }
@media (max-width: 1000px) { .pp-editor-grid, .pp-share-grid, .pp-history-grid { grid-template-columns: 1fr; }.pp-client-preview { grid-template-columns: minmax(0, 1fr) 270px; }.pp-preview-item { grid-template-columns: minmax(0, 1fr) auto; }.pp-history-list { display: flex; flex-wrap: wrap; gap: 8px; }.pp-history-list .pp-section-title { width: 100%; }.ap-proposals .pp-history-list button { width: auto; } }
@media (max-width: 700px) {
  .ap-proposals { padding: 18px; }.pp-header { align-items: start; flex-direction: column; gap: 14px; margin-bottom: 18px; }.ap-proposals h1 { font-size: 21px; }
  .pp-tabs { gap: 22px; margin-bottom: 20px; }.pp-tabs button { white-space: nowrap; }.pp-list-toolbar { align-items: start; flex-direction: column; gap: 10px; }.ap-proposals .pp-search { width: 100%; }
  .pp-grid-two, .pp-client-preview, .pp-scope-item { grid-template-columns: 1fr; }.pp-scope-item { gap: 18px; }.pp-category-header { flex-wrap: wrap; padding: 18px; gap: 12px; }.pp-category-name { flex-basis: calc(100% - 45px); }.pp-category-rule { width: calc(100% - 50px); }.pp-category-body { padding: 0 18px 18px; }
  .pp-card { padding: 20px; }.pp-inline-field { flex-wrap: wrap; }.pp-inline-field label { min-width: 150px; }.pp-preview-document { padding: 22px; }.pp-preview-summary { position: static; }
  .pp-save-bar { bottom: -18px; flex-wrap: wrap; padding: 14px 0; gap: 10px; }.pp-save-bar .pp-actions { width: 100%; justify-content: flex-end; gap: 6px; }.pp-save-bar button { padding: 8px 10px; }.pp-section-toolbar { flex-wrap: wrap; gap: 12px; }
  .pp-empty { min-height: 280px; padding: 35px 20px; }.pp-preview-category-head { align-items: start; flex-direction: column; gap: 5px; }.pp-preview-item { gap: 12px; }.pp-document-header h2 { font-size: 22px !important; }
  .pp-table th:nth-child(n+4), .pp-table td:nth-child(n+4) { display: none; }.pp-table td, .pp-table th { padding: 14px 12px; }.pp-table td:first-child { min-width: 0; }.pp-table th:nth-child(2), .pp-table td:nth-child(2) { display: none; }.pp-table td:first-child { width: 100%; }.pp-table .pp-table-title { font-size: 12px; }
}
`;
