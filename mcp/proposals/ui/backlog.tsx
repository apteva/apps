import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { Detail } from "./ProposalsPanel";

export type Client = {
  id: number; name: string; email: string; crm_contact_id: number; version: number;
};
type BacklogItem = {
  id: number; client_id: number; name: string; category: string; description: string; notes: string;
  amount_minor: number | null; currency: string; required: boolean; archived: boolean; version: number;
};
type ClientPage = {clients: Client[]; next_before?: number};
type ItemPage = {items: BacklogItem[]; next_before?: number};
type Request = <T>(action: string, body?: object) => Promise<T>;

export const currencyOptions = ["EUR", "USD", "GBP", "CAD", "AUD", "CHF", "NZD", "SEK", "NOK", "DKK"];
export function formatMoney(amount: number, currency: string) {
  return new Intl.NumberFormat(undefined, {style: "currency", currency}).format(amount / 100);
}

function Dialog({title, onClose, busy, children}: {title: string; onClose: () => void; busy?: boolean; children: ReactNode}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
    return () => dialog.current?.close();
  }, []);
  return <dialog className="pp-dialog" ref={dialog} aria-label={title}
    onCancel={(event) => { event.preventDefault(); if (!busy) onClose(); }}>
    <header><h2>{title}</h2><button type="button" disabled={busy} onClick={onClose} aria-label="Close dialog">×</button></header>
    {children}
  </dialog>;
}

function ClientEditor({value, request, onClose, onSaved}: {
  value: Client | null; request: Request; onClose: () => void; onSaved: (client: Client) => Promise<void>;
}) {
  const [name, setName] = useState(value?.name || "");
  const [email, setEmail] = useState(value?.email || "");
  const [contactID, setContactID] = useState(String(value?.crm_contact_id || ""));
  const [linkedContactID, setLinkedContactID] = useState(value?.crm_contact_id || 0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function lookup() {
    setBusy(true); setError("");
    try {
      const match = await request<Client>("clients_crm_lookup", {contact_id: Number(contactID)});
      setName(match.name); setEmail(match.email); setLinkedContactID(match.crm_contact_id);
    } catch (err) { setError((err as Error).message); } finally { setBusy(false); }
  }
  return <Dialog title={value ? "Edit client" : "New client"} onClose={onClose} busy={busy}>
    <form onSubmit={async (event) => {
      event.preventDefault(); setBusy(true); setError("");
      try {
        const client = await request<Client>(value ? "clients_update" : "clients_create", {
          ...(value ? {client_id: value.id, version: value.version} : {}),
          client: {name, email, crm_contact_id: linkedContactID},
        });
        await onSaved(client);
      } catch (err) { setError((err as Error).message); } finally { setBusy(false); }
    }}>
      <p className="pp-muted">Keep a private backlog for this client before preparing a proposal.</p>
      {error && <div className="pp-notice pp-error" role="alert">{error}</div>}
      <label>Client name<input required maxLength={160} value={name} onChange={(event) => setName(event.target.value)}/></label>
      <label>Client email<input type="email" maxLength={254} value={email} onChange={(event) => setEmail(event.target.value)}/></label>
      <div className="pp-section-divider">
        <h3>Import from CRM</h3><p>Optionally reuse an existing contact.</p>
        <div className="pp-inline-field"><label>CRM contact ID<input type="number" min={1} value={contactID}
          onChange={(event) => setContactID(event.target.value)}/></label>
          <button type="button" disabled={busy || Number(contactID) <= 0} onClick={lookup}>Import contact</button></div>
        {!!linkedContactID && <div className="pp-linked"><span>CRM contact #{linkedContactID}</span>
          <button type="button" className="pp-text-button" onClick={() => {setLinkedContactID(0);setContactID("");}}>Unlink</button></div>}
      </div>
      <footer><button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="pp-primary" disabled={busy} type="submit">{busy ? "Saving…" : "Save client"}</button></footer>
    </form>
  </Dialog>;
}

function ItemEditor({client, value, request, onClose, onSaved}: {
  client: Client; value: BacklogItem | null; request: Request; onClose: () => void; onSaved: () => Promise<void>;
}) {
  const [name, setName] = useState(value?.name || "");
  const [category, setCategory] = useState(value?.category || "General");
  const [description, setDescription] = useState(value?.description || "");
  const [notes, setNotes] = useState(value?.notes || "");
  const [amount, setAmount] = useState(value?.amount_minor == null ? "" : (value.amount_minor / 100).toFixed(2));
  const [currency, setCurrency] = useState(value?.currency || "EUR");
  const [required, setRequired] = useState(value?.required || false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <Dialog title={value ? "Edit backlog item" : "New backlog item"} onClose={onClose} busy={busy}>
    <form onSubmit={async (event) => {
      event.preventDefault(); setBusy(true); setError("");
      try {
        let amountMinor: number | null = null;
        if (amount.trim()) {
          if (!/^\d{1,10}(?:[.,]\d{0,2})?$/.test(amount.trim())) throw new Error("Enter a price with up to two decimal places.");
          const [whole, cents = ""] = amount.trim().split(/[.,]/);
          amountMinor = Number(whole) * 100 + Number(cents.padEnd(2, "0"));
        }
        await request(value ? "backlog_update" : "backlog_create", {
          client_id: client.id, ...(value ? {item_id: value.id, version: value.version} : {}),
          item: {name, category, description, notes, amount_minor: amountMinor, currency, required, archived: value?.archived || false},
        });
        await onSaved();
      } catch (err) { setError((err as Error).message); } finally { setBusy(false); }
    }}>
      <p className="pp-muted">Private planning for {client.name}. Add it to a proposal when you’re ready.</p>
      {error && <div className="pp-notice pp-error" role="alert">{error}</div>}
      <label>Item name<input required maxLength={160} value={name} onChange={(event) => setName(event.target.value)}/></label>
      <label>Category<input required maxLength={120} value={category} onChange={(event) => setCategory(event.target.value)}/></label>
      <label>Scope description<textarea maxLength={2000} value={description} placeholder="Copied into the proposal when you select this item."
        onChange={(event) => setDescription(event.target.value)}/></label>
      <div className="pp-grid-two">
        <label>Price (optional)<input inputMode="decimal" value={amount} placeholder="Not estimated yet" onChange={(event) => setAmount(event.target.value)}/></label>
        <label>Currency<select value={currency} onChange={(event) => setCurrency(event.target.value)}>
          {currencyOptions.map((code) => <option key={code}>{code}</option>)}</select></label>
      </div>
      <label className="pp-check"><input type="checkbox" checked={required} onChange={(event) => setRequired(event.target.checked)}/> Required when added to a proposal</label>
      <label>Internal notes<textarea maxLength={8000} value={notes} placeholder="Private context, estimates, or follow-up notes."
        onChange={(event) => setNotes(event.target.value)}/></label>
      <p className="pp-muted">Internal notes stay in the backlog. They are excluded from proposal copies.</p>
      <footer><button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button type="submit" className="pp-primary" disabled={busy}>{busy ? "Saving…" : "Save item"}</button></footer>
    </form>
  </Dialog>;
}

export function ClientSelector({request, selected, onChange}: {
  request: Request; selected?: {id: number; name: string}; onChange: (client?: Client) => void;
}) {
  const [page, setPage] = useState<ClientPage>({clients: []});
  const [query, setQuery] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    const timer = window.setTimeout(() => {
      request<ClientPage>("clients_list", {q: query}).then((next) => {if (active) {setPage(next);setError("");}})
        .catch((err) => {if (active) setError(err.message);});
    }, 180);
    return () => {active = false;window.clearTimeout(timer);};
  }, [query]);
  return <div className="pp-fields">
    <label>Find a saved client<input type="search" placeholder="Search name or email" value={query} onChange={(event) => setQuery(event.target.value)}/></label>
    <label>Saved client<select value={selected?.id || ""} onChange={(event) => onChange(page.clients.find((client) => client.id === Number(event.target.value)))}>
      <option value="">Enter client details below</option>
      {selected && !page.clients.some((client) => client.id === selected.id) && <option value={selected.id}>{selected.name}</option>}
      {page.clients.map((client) => <option key={client.id} value={client.id}>{client.name}{client.email ? " · " + client.email : ""}</option>)}
    </select></label>
    {page.next_before && <button type="button" onClick={async () => {
      try {
        const next = await request<ClientPage>("clients_list", {q: query, before: page.next_before});
        setPage({...next, clients: [...page.clients, ...next.clients]});
      } catch (err) {setError((err as Error).message);}
    }}>Load more clients</button>}
    {error && <p className="pp-error-text" role="alert">{error}</p>}
  </div>;
}

function ItemTable({items, selected, onToggle, unavailable, onEdit, onArchive}: {
  items: BacklogItem[]; selected: number[]; onToggle: (id: number) => void;
  unavailable?: (item: BacklogItem) => string | undefined;
  onEdit?: (item: BacklogItem) => void; onArchive?: (item: BacklogItem) => void;
}) {
  return <div className="pp-table-wrap"><table className="pp-table pp-backlog-table">
    <thead><tr><th><span className="pp-sr">Select</span></th><th>Item</th><th>Category</th><th>Price</th>{onEdit && <th>Actions</th>}</tr></thead>
    <tbody>{items.map((item) => {
      const reason = item.amount_minor == null ? "Set a price before adding to a proposal" : item.archived ? "Archived" : unavailable?.(item);
      return <tr key={item.id}>
        <td><input type="checkbox" aria-label={"Select " + item.name} checked={selected.includes(item.id)} disabled={!!reason}
          title={reason} onChange={() => onToggle(item.id)}/></td>
        <td>{onEdit ? <button className="pp-table-title" onClick={() => onEdit(item)}>{item.name}</button> : <strong>{item.name}</strong>}
          {item.description && <small className="pp-backlog-description">{item.description}</small>}
          {item.required && <small>Required in proposals</small>}
          {reason && <small>{reason}</small>}
          {onEdit && item.notes && <small className="pp-private-note">Has internal notes</small>}
        </td>
        <td>{item.category}</td>
        <td className="pp-amount">{item.amount_minor == null ? "Not estimated" : formatMoney(item.amount_minor, item.currency)}<small>{item.currency}</small></td>
        {onEdit && <td><div className="pp-actions"><button onClick={() => onEdit(item)}>Edit</button>
          <button onClick={() => onArchive?.(item)}>{item.archived ? "Restore" : "Archive"}</button></div></td>}
      </tr>;
    })}</tbody>
  </table></div>;
}

export function BacklogPicker({request, clientID, currency, imported, onClose, onImport}: {
  request: Request; clientID: number; currency: string; imported: number[];
  onClose: () => void; onImport: (ids: number[]) => Promise<void>;
}) {
  const [page, setPage] = useState<ItemPage>({items: []});
  const [selected, setSelected] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    request<ItemPage>("backlog_list", {client_id: clientID}).then((next) => {if (active) {setPage(next);setLoading(false);}})
      .catch((err) => {if (active) {setError(err.message);setLoading(false);}});
    return () => {active = false;};
  }, [clientID]);
  return <Dialog title="Add from client backlog" busy={busy} onClose={onClose}>
    <div className="pp-dialog-body">
      <p className="pp-muted">Select the items to copy into this draft. Unselected items and internal notes stay private.</p>
      {error && <div className="pp-notice pp-error" role="alert">{error}</div>}
      {loading ? <p>Loading backlog…</p> : !page.items.length ? <p>No active backlog items for this client.</p> :
        <ItemTable items={page.items} selected={selected}
          onToggle={(id) => setSelected((previous) => previous.includes(id) ? previous.filter((value) => value !== id) : [...previous, id])}
          unavailable={(item) => imported.includes(item.id) ? "Already in this draft" : item.currency !== currency ? "Uses " + item.currency + "; draft uses " + currency : undefined}/>}
      {page.next_before && <button disabled={busy} onClick={async () => {
        try {
          const next = await request<ItemPage>("backlog_list", {client_id: clientID, before: page.next_before});
          setPage({...next, items: [...page.items, ...next.items]});
        } catch (err) {setError((err as Error).message);}
      }}>Load more items</button>}
      <footer><button disabled={busy} onClick={onClose}>Cancel</button><button className="pp-primary" disabled={busy || !selected.length} onClick={async () => {
        setBusy(true);setError("");
        try {await onImport(selected);} catch (err) {setError((err as Error).message);} finally {setBusy(false);}
      }}>{busy ? "Adding…" : "Add selected items (" + selected.length + ")"}</button></footer>
    </div>
  </Dialog>;
}

export function BacklogPanel({request, initialClientID, onSelectClient, onProposal}: {
  request: Request; initialClientID: number; onSelectClient: (id: number) => void; onProposal: (proposal: Detail) => Promise<void>;
}) {
  const [clients, setClients] = useState<ClientPage>({clients: []});
  const [owner, setOwner] = useState<Client | null>(null);
  const [items, setItems] = useState<ItemPage>({items: []});
  const [query, setQuery] = useState("");
  const [archived, setArchived] = useState(false);
  const [selected, setSelected] = useState<number[]>([]);
  const [clientEditor, setClientEditor] = useState<Client | "new" | null>(null);
  const [itemEditor, setItemEditor] = useState<BacklogItem | "new" | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const requestKey = useRef("");
  function selectClient(client: Client) {
    setOwner(client);onSelectClient(client.id);setSelected([]);setArchived(false);requestKey.current = "";
  }
  useEffect(() => {
    let active = true;
    const timer = window.setTimeout(() => {
      request<ClientPage>("clients_list", {q: query}).then(async (page) => {
        if (!active) return;
        setClients(page);setError("");
        if (!owner && !query) {
          const initial = page.clients.find((client) => client.id === initialClientID) ||
            (initialClientID ? await request<Client>("clients_get", {client_id: initialClientID}) : page.clients[0]);
          if (active && initial) selectClient(initial);
        }
        if (active) setLoading(false);
      }).catch((err) => {if (active) {setError(err.message);setLoading(false);}});
    }, 180);
    return () => {active = false;window.clearTimeout(timer);};
  }, [query]);
  useEffect(() => {
    setItems({items: []});setSelected([]);requestKey.current = "";
    if (!owner) return;
    let active = true;
    request<ItemPage>("backlog_list", {client_id: owner.id, archived}).then((next) => {if (active) {setItems(next);setError("");}})
      .catch((err) => {if (active) setError(err.message);});
    return () => {active = false;};
  }, [owner?.id, archived]);
  async function reloadItems() {
    setItems(await request<ItemPage>("backlog_list", {client_id: owner!.id, archived}));
    setSelected([]);requestKey.current = "";
  }
  return <div className="pp-backlog-layout">
    <aside className="pp-card pp-client-list">
      <div className="pp-section-toolbar"><h2>Clients</h2><button onClick={() => setClientEditor("new")}>New client</button></div>
      <label>Find client<input type="search" value={query} placeholder="Name or email" onChange={(event) => setQuery(event.target.value)}/></label>
      {loading && <p className="pp-muted">Loading clients…</p>}
      {!loading && !clients.clients.length && <p className="pp-muted">No clients found. Add a client to start their backlog.</p>}
      <nav aria-label="Backlog clients">{clients.clients.map((client) => <button key={client.id} aria-current={owner?.id === client.id}
        onClick={() => selectClient(client)}><strong>{client.name}</strong><small>{client.email || "No email added"}</small></button>)}</nav>
      {clients.next_before && <button onClick={async () => {
        try {
          const next = await request<ClientPage>("clients_list", {q: query, before: clients.next_before});
          setClients({...next, clients: [...clients.clients, ...next.clients]});
        } catch (err) {setError((err as Error).message);}
      }}>Load more clients</button>}
    </aside>
    <section className="pp-stack">
      {error && <div className="pp-notice pp-error" role="alert">{error}</div>}
      {owner ? <>
        <header className="pp-section-toolbar"><div><h2>{owner.name}</h2><p className="pp-muted">{owner.email || "Private client backlog"}</p></div>
          <div className="pp-actions"><button onClick={() => setClientEditor(owner)}>Edit client</button>
            <button className="pp-primary" onClick={() => setItemEditor("new")}>Add backlog item</button></div></header>
        <div className="pp-backlog-controls"><label className="pp-check"><input type="checkbox" checked={archived}
          onChange={(event) => setArchived(event.target.checked)}/> Show archived items</label>
          <button disabled={busy} onClick={async () => {try {await reloadItems();} catch (err) {setError((err as Error).message);}}}>Refresh backlog</button></div>
        <p className="pp-muted">Items stay private until you select and publish them in a proposal. Leave the price blank while an idea is being estimated.</p>
        {items.items.length ? <ItemTable items={items.items} selected={selected}
          onToggle={(id) => {setSelected((previous) => previous.includes(id) ? previous.filter((value) => value !== id) : [...previous, id]);requestKey.current = "";}}
          onEdit={setItemEditor} onArchive={async (item) => {
            setBusy(true);setError("");
            try {
              const {name,category,description,notes,amount_minor,currency,required} = item;
              await request("backlog_update", {client_id: owner.id, item_id: item.id, version: item.version,
                item: {name,category,description,notes,amount_minor,currency,required,archived: !item.archived}});
              await reloadItems();
            } catch (err) {setError((err as Error).message);} finally {setBusy(false);}
          }}/> : <div className="pp-empty"><h2>{archived ? "No archived items" : "Plan this client’s next project"}</h2>
            <p>Add ideas and scope privately. Select only the items you want to offer when preparing a proposal.</p>
            {!archived && <button className="pp-primary" onClick={() => setItemEditor("new")}>Add first backlog item</button>}</div>}
        {items.next_before && <button disabled={busy} onClick={async () => {
          try {
            const next = await request<ItemPage>("backlog_list", {client_id: owner.id, archived, before: items.next_before});
            setItems({...next, items: [...items.items, ...next.items]});
          } catch (err) {setError((err as Error).message);}
        }}>Load more items</button>}
        {!archived && <div className="pp-backlog-selection"><span>{selected.length} {selected.length === 1 ? "item" : "items"} selected</span><button className="pp-primary" disabled={busy || !selected.length} onClick={async () => {
          setBusy(true);setError("");requestKey.current ||= crypto.randomUUID();
          try {
            const result = await request<Detail>("backlog_create_proposal", {client_id: owner.id, item_ids: selected, request_key: requestKey.current});
            await onProposal(result);
          } catch (err) {setError((err as Error).message);} finally {setBusy(false);}
        }}>{busy ? "Creating…" : "Create proposal from selection"}</button></div>}
      </> : !loading && <div className="pp-empty"><h2>Client backlog</h2><p>Create a client to collect ideas, scope, prices, and private notes before preparing a proposal.</p>
        <button className="pp-primary" onClick={() => setClientEditor("new")}>Create first client</button></div>}
    </section>
    {clientEditor && <ClientEditor value={clientEditor === "new" ? null : clientEditor} request={request} onClose={() => setClientEditor(null)}
      onSaved={async (client) => {
        const next = await request<ClientPage>("clients_list", {});
        setClients(next);setQuery("");selectClient(client);setClientEditor(null);
      }}/>}
    {itemEditor && owner && <ItemEditor client={owner} value={itemEditor === "new" ? null : itemEditor}
      request={request} onClose={() => setItemEditor(null)} onSaved={async () => {await reloadItems();setItemEditor(null);}}/>}
  </div>;
}
