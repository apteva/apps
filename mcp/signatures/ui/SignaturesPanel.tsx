import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  projectId: string;
}

interface Envelope {
  id: number;
  public_id: string;
  source_file_id: number;
  source_name: string;
  completed_file_id?: number;
  audit_file_id?: number;
  title: string;
  sender_name: string;
  message: string;
  status: string;
  delivery_mode: string;
  expires_at: string;
  sent_at?: string;
  completed_at?: string;
  recipients?: Recipient[];
  fields?: Field[];
  audit?: AuditEvent[];
}

interface Recipient {
  id: number;
  name: string;
  email?: string;
  role: "signer" | "approver";
  signing_order: number;
  status: string;
}

interface Field {
  id?: number;
  recipient_id: number;
  field_type: "signature" | "initials" | "date_signed" | "text" | "checkbox";
  page: number;
  x: number;
  y: number;
  width: number;
  height: number;
  label: string;
  required: boolean;
}

interface AuditEvent {
  id: number;
  event_type: string;
  recipient_id?: number;
  occurred_at: string;
}

const API = "/api/apps/signatures";

export default function SignaturesPanel({ projectId }: NativePanelProps) {
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [detail, setDetail] = useState<Envelope | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newEnvelope, setNewEnvelope] = useState({ source_file_id: "", title: "", sender_name: "", message: "" });
  const [recipientDrafts, setRecipientDrafts] = useState<Array<Pick<Recipient, "name" | "email" | "role" | "signing_order">>>([]);
  const [fieldDrafts, setFieldDrafts] = useState<Field[]>([]);

  const url = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const request = useCallback(async (path: string, options?: RequestInit) => {
    const response = await fetch(url(path), {
      credentials: "same-origin",
      ...options,
      headers: options?.body ? { "Content-Type": "application/json", ...(options.headers || {}) } : options?.headers,
    });
    const text = await response.text();
    const data = text ? JSON.parse(text) : {};
    if (!response.ok) throw new Error(data.error || text || `Request failed (${response.status})`);
    return data;
  }, [url]);

  const loadList = useCallback(async () => {
    try {
      const data = await request("/envelopes?limit=100");
      const list: Envelope[] = data.envelopes || [];
      setEnvelopes(list);
      setSelectedId((current) => current && list.some((item) => item.id === current) ? current : list[0]?.id || 0);
    } catch (error) {
      setMessage((error as Error).message);
    }
  }, [request]);

  const loadDetail = useCallback(async (id: number) => {
    if (!id) {
      setDetail(null);
      return;
    }
    try {
      const data = await request(`/envelopes/${id}`);
      const envelope: Envelope = data.envelope;
      setDetail(envelope);
      setRecipientDrafts((envelope.recipients || []).map((recipient) => ({
        name: recipient.name,
        email: recipient.email || "",
        role: recipient.role,
        signing_order: recipient.signing_order,
      })));
      setFieldDrafts((envelope.fields || []).map((field) => ({ ...field })));
    } catch (error) {
      setMessage((error as Error).message);
    }
  }, [request]);

  useEffect(() => { loadList(); }, [loadList]);
  useEffect(() => { loadDetail(selectedId); }, [loadDetail, selectedId]);

  const currentRecipient = useMemo(
    () => detail?.recipients?.find((recipient) => recipient.status === "ready" || recipient.status === "viewed") || null,
    [detail],
  );

  const run = async (operation: () => Promise<void>) => {
    setBusy(true);
    setMessage("");
    try {
      await operation();
      await loadList();
      if (selectedId) await loadDetail(selectedId);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const createEnvelope = () => run(async () => {
    const data = await request("/envelopes", {
      method: "POST",
      body: JSON.stringify({
        source_file_id: Number(newEnvelope.source_file_id),
        title: newEnvelope.title,
        sender_name: newEnvelope.sender_name,
        message: newEnvelope.message,
      }),
    });
    setCreating(false);
    setNewEnvelope({ source_file_id: "", title: "", sender_name: "", message: "" });
    setSelectedId(data.envelope.id);
    setMessage("Draft created.");
  });

  const saveRecipients = () => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/recipients`, {
      method: "POST",
      body: JSON.stringify({ recipients: recipientDrafts.map((recipient, index) => ({ ...recipient, signing_order: index + 1 })) }),
    });
    setMessage("Recipients saved. Existing fields were cleared because recipient IDs changed.");
  });

  const saveFields = () => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/fields`, { method: "POST", body: JSON.stringify({ fields: fieldDrafts }) });
    setMessage("Fields saved.");
  });

  const send = (delivery_mode: "manual" | "messaging") => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/send`, {
      method: "POST",
      body: JSON.stringify({ delivery_mode, idempotency_key: crypto.randomUUID() }),
    });
    setMessage(delivery_mode === "manual" ? "Envelope activated. Copy the current recipient link next." : "Envelope activated and invitation requested.");
  });

  const copyLink = () => run(async () => {
    if (!detail || !currentRecipient) return;
    const data = await request(`/envelopes/${detail.id}/link`, { method: "POST", body: JSON.stringify({ recipient_id: currentRecipient.id }) });
    await navigator.clipboard.writeText(data.url);
    setMessage("A new signing link was copied. Any previous link for this recipient was revoked.");
  });

  const remind = () => run(async () => {
    if (!detail || !currentRecipient) return;
    await request(`/envelopes/${detail.id}/remind`, { method: "POST", body: JSON.stringify({ recipient_id: currentRecipient.id }) });
    setMessage("Reminder requested through Messaging.");
  });

  const voidEnvelope = () => run(async () => {
    if (!detail || !window.confirm("Void this envelope? Active signing links will stop working.")) return;
    await request(`/envelopes/${detail.id}/void`, { method: "POST", body: JSON.stringify({ reason: "Voided from dashboard" }) });
    setMessage("Envelope voided.");
  });

  const finalize = () => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/finalize`, { method: "POST", body: "{}" });
    setMessage("Completed PDF generated.");
  });

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Signatures</h1>
          <p className="text-xs text-text-muted">Send Storage PDFs for simple electronic signature.</p>
        </div>
        <button type="button" onClick={loadList} className="ml-auto px-3 py-1.5 text-xs border border-border rounded">Refresh</button>
        <button type="button" onClick={() => setCreating(true)} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">New envelope</button>
      </header>

      {message && <div className="shrink-0 px-4 py-2 text-xs border-b border-border bg-bg-input">{message}</div>}

      <main className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[340px_minmax(0,1fr)]">
        <aside className="border-r border-border overflow-auto">
          {envelopes.length === 0 ? <p className="p-4 text-sm text-text-muted">No envelopes yet.</p> : (
            <ul className="divide-y divide-border">
              {envelopes.map((envelope) => (
                <li key={envelope.id}>
                  <button type="button" onClick={() => setSelectedId(envelope.id)} className={`w-full text-left p-4 hover:bg-bg-input ${selectedId === envelope.id ? "bg-bg-input" : ""}`}>
                    <div className="flex gap-2 items-center"><span className="font-medium text-sm truncate">{envelope.title}</span><Status value={envelope.status} /></div>
                    <p className="text-xs text-text-muted mt-1 truncate">{envelope.source_name}</p>
                    <p className="text-[11px] text-text-dim mt-2">Expires {formatDate(envelope.expires_at)}</p>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <section className="min-h-0 overflow-auto p-5">
          {creating ? (
            <Card title="New envelope">
              <div className="grid gap-3 max-w-xl">
                <Input label="Storage PDF file ID" value={newEnvelope.source_file_id} onChange={(value) => setNewEnvelope({ ...newEnvelope, source_file_id: value })} />
                <Input label="Title" value={newEnvelope.title} onChange={(value) => setNewEnvelope({ ...newEnvelope, title: value })} />
                <Input label="Sender name" value={newEnvelope.sender_name} onChange={(value) => setNewEnvelope({ ...newEnvelope, sender_name: value })} />
                <Input label="Message" value={newEnvelope.message} onChange={(value) => setNewEnvelope({ ...newEnvelope, message: value })} />
                <div className="flex gap-2"><Primary onClick={createEnvelope} disabled={busy}>Create draft</Primary><Secondary onClick={() => setCreating(false)}>Cancel</Secondary></div>
              </div>
            </Card>
          ) : detail ? (
            <div className="grid gap-4 max-w-5xl">
              <Card title={detail.title} right={<Status value={detail.status} />}>
                <div className="grid md:grid-cols-3 gap-3 text-xs text-text-muted">
                  <div><span className="block text-text-dim">Source</span>{detail.source_name} · file {detail.source_file_id}</div>
                  <div><span className="block text-text-dim">Delivery</span>{detail.delivery_mode}</div>
                  <div><span className="block text-text-dim">Expires</span>{formatDate(detail.expires_at)}</div>
                </div>
                {detail.message && <p className="mt-3 text-sm">{detail.message}</p>}
                <div className="mt-4 flex flex-wrap gap-2">
                  {detail.status === "draft" && <><Primary onClick={() => send("manual")} disabled={busy}>Send manually</Primary><Secondary onClick={() => send("messaging")} disabled={busy}>Send with Messaging</Secondary></>}
                  {detail.status === "sent" && currentRecipient && <><Primary onClick={copyLink} disabled={busy}>Copy signing link</Primary>{currentRecipient.email && <Secondary onClick={remind} disabled={busy}>Send reminder</Secondary>}</>}
                  {detail.status === "sent" && !currentRecipient && <Secondary onClick={finalize} disabled={busy}>Retry finalization</Secondary>}
                  {(detail.status === "draft" || detail.status === "sent") && <Secondary onClick={voidEnvelope} disabled={busy}>Void</Secondary>}
                  {detail.completed_file_id && <span className="text-xs text-text-muted self-center">Completed file {detail.completed_file_id} · audit file {detail.audit_file_id}</span>}
                </div>
              </Card>

              {detail.status === "draft" && <Card title="Recipients" right={<Secondary onClick={() => setRecipientDrafts([...recipientDrafts, { name: "", email: "", role: "signer", signing_order: recipientDrafts.length + 1 }])}>Add recipient</Secondary>}>
                <div className="grid gap-2">
                  {recipientDrafts.map((recipient, index) => (
                    <div key={index} className="grid grid-cols-[36px_1fr_1fr_120px_36px] gap-2 items-center">
                      <span className="text-xs text-text-muted">{index + 1}</span>
                      <input className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder="Name" value={recipient.name} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { name: event.target.value })} />
                      <input className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder="Email (optional in manual mode)" value={recipient.email || ""} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { email: event.target.value })} />
                      <select className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" value={recipient.role} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { role: event.target.value as Recipient["role"] })}><option value="signer">Signer</option><option value="approver">Approver</option></select>
                      <button type="button" onClick={() => setRecipientDrafts(recipientDrafts.filter((_, i) => i !== index))}>×</button>
                    </div>
                  ))}
                  <div><Primary onClick={saveRecipients} disabled={busy || recipientDrafts.length === 0}>Save recipients</Primary></div>
                </div>
              </Card>}

              {detail.status === "draft" && (detail.recipients?.length || 0) > 0 && <Card title="Fields" right={<Secondary onClick={() => setFieldDrafts([...fieldDrafts, defaultField(detail.recipients![0].id)])}>Add field</Secondary>}>
                <p className="text-xs text-text-muted mb-3">Coordinates are normalized from the top-left of the PDF page: 0 to 1.</p>
                <div className="grid gap-2 overflow-x-auto">
                  {fieldDrafts.map((field, index) => (
                    <div key={index} className="grid grid-cols-[150px_130px_60px_repeat(4,70px)_1fr_36px] gap-2 items-center min-w-[920px]">
                      <select className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs" value={field.recipient_id} onChange={(event) => updateAt(setFieldDrafts, fieldDrafts, index, { recipient_id: Number(event.target.value) })}>{detail.recipients!.map((recipient) => <option key={recipient.id} value={recipient.id}>{recipient.name}</option>)}</select>
                      <select className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs" value={field.field_type} onChange={(event) => updateAt(setFieldDrafts, fieldDrafts, index, { field_type: event.target.value as Field["field_type"] })}>{["signature", "initials", "date_signed", "text", "checkbox"].map((type) => <option key={type}>{type}</option>)}</select>
                      {(["page", "x", "y", "width", "height"] as const).map((key) => <input key={key} className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs" type="number" step={key === "page" ? 1 : 0.01} title={key} value={field[key]} onChange={(event) => updateAt(setFieldDrafts, fieldDrafts, index, { [key]: Number(event.target.value) })} />)}
                      <input className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs" placeholder="Label" value={field.label} onChange={(event) => updateAt(setFieldDrafts, fieldDrafts, index, { label: event.target.value })} />
                      <button type="button" onClick={() => setFieldDrafts(fieldDrafts.filter((_, i) => i !== index))}>×</button>
                    </div>
                  ))}
                  <div><Primary onClick={saveFields} disabled={busy || fieldDrafts.length === 0}>Save fields</Primary></div>
                </div>
              </Card>}

              <Card title="Recipients">
                <div className="divide-y divide-border">{(detail.recipients || []).map((recipient) => <div key={recipient.id} className="py-2 flex items-center gap-3 text-sm"><span className="w-6 text-text-muted">{recipient.signing_order}</span><span>{recipient.name}</span><span className="text-text-muted">{recipient.role}</span><Status value={recipient.status} /></div>)}</div>
              </Card>

              <Card title="Audit timeline">
                <div className="divide-y divide-border">{(detail.audit || []).map((event) => <div key={event.id} className="py-2 flex gap-3 text-xs"><span className="text-text-muted w-44">{formatDate(event.occurred_at)}</span><span>{event.event_type}</span>{event.recipient_id && <span className="text-text-dim">recipient {event.recipient_id}</span>}</div>)}</div>
              </Card>
            </div>
          ) : <p className="text-sm text-text-muted">Select an envelope or create a new one.</p>}
        </section>
      </main>
    </div>
  );
}

function defaultField(recipientId: number): Field {
  return { recipient_id: recipientId, field_type: "signature", page: 1, x: 0.58, y: 0.76, width: 0.3, height: 0.08, label: "Signature", required: true };
}

function updateAt<T>(setter: (value: T[]) => void, list: T[], index: number, patch: Partial<T>) {
  setter(list.map((item, i) => i === index ? { ...item, ...patch } : item));
}

function formatDate(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function Status({ value }: { value: string }) {
  return <span className="ml-auto text-[10px] uppercase tracking-wide px-2 py-0.5 rounded bg-border text-text-muted">{value}</span>;
}

function Card({ title, right, children }: { title: string; right?: ReactNode; children: ReactNode }) {
  return <section className="border border-border rounded-lg bg-bg"><header className="px-4 py-3 border-b border-border flex items-center"><h2 className="text-sm font-semibold">{title}</h2><div className="ml-auto">{right}</div></header><div className="p-4">{children}</div></section>;
}

function Input({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <label className="text-xs text-text-muted">{label}<input className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm" value={value} onChange={(event) => onChange(event.target.value)} /></label>;
}

function Primary({ onClick, disabled, children }: { onClick: () => void; disabled?: boolean; children: ReactNode }) {
  return <button type="button" onClick={onClick} disabled={disabled} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50">{children}</button>;
}

function Secondary({ onClick, disabled, children }: { onClick: () => void; disabled?: boolean; children: ReactNode }) {
  return <button type="button" onClick={onClick} disabled={disabled} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50">{children}</button>;
}
