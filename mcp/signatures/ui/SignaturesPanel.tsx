import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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

interface StorageFile {
  id: number;
  name: string;
  folder?: string;
  size?: number;
  content_type?: string;
  created_at?: string;
}

const API = "/api/apps/signatures";
const STORAGE_API = "/api/apps/storage";

const RECIPIENT_COLORS = ["#2457d6", "#0e9f6e", "#d97706", "#b43403", "#7c3aed", "#0891b2"];

const FIELD_DEFAULTS: Record<Field["field_type"], { width: number; height: number; label: string }> = {
  signature: { width: 0.28, height: 0.055, label: "Signature" },
  initials: { width: 0.1, height: 0.045, label: "Initials" },
  date_signed: { width: 0.16, height: 0.04, label: "Date" },
  text: { width: 0.25, height: 0.045, label: "Text" },
  checkbox: { width: 0.035, height: 0.025, label: "Checkbox" },
};

// pdf.js is vendored next to this panel (ui/vendor/) and loaded on
// demand; the dynamic non-literal import keeps the bundler from
// inlining ~450KB into the panel bundle.
let pdfjsPromise: Promise<any> | null = null;
function loadPdfjs(): Promise<any> {
  if (!pdfjsPromise) {
    const entry = new URL("./vendor/pdf.min.mjs", import.meta.url).href;
    pdfjsPromise = import(entry).then((mod) => {
      mod.GlobalWorkerOptions.workerSrc = new URL("./vendor/pdf.worker.min.mjs", import.meta.url).href;
      return mod;
    });
  }
  return pdfjsPromise;
}

export default function SignaturesPanel({ projectId }: NativePanelProps) {
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [detail, setDetail] = useState<Envelope | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newEnvelope, setNewEnvelope] = useState({ title: "", sender_name: "", message: "" });
  const [pickedFile, setPickedFile] = useState<StorageFile | null>(null);
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
    if (!pickedFile) throw new Error("Pick a PDF first.");
    const data = await request("/envelopes", {
      method: "POST",
      body: JSON.stringify({
        source_file_id: pickedFile.id,
        title: newEnvelope.title || pickedFile.name.replace(/\.pdf$/i, ""),
        sender_name: newEnvelope.sender_name,
        message: newEnvelope.message,
      }),
    });
    setCreating(false);
    setNewEnvelope({ title: "", sender_name: "", message: "" });
    setPickedFile(null);
    setSelectedId(data.envelope.id);
    setMessage("Draft created. Add recipients, then place their fields on the document.");
  });

  const saveRecipients = () => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/recipients`, {
      method: "POST",
      body: JSON.stringify({ recipients: recipientDrafts.map((recipient, index) => ({ ...recipient, signing_order: index + 1 })) }),
    });
    setMessage("Recipients saved. Existing fields were cleared because recipient IDs changed.");
  });

  const saveFields = (fields: Field[]) => run(async () => {
    if (!detail) return;
    await request(`/envelopes/${detail.id}/fields`, { method: "POST", body: JSON.stringify({ fields }) });
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

      <main className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "340px minmax(0, 1fr)" }}>
        <aside className="border-r border-border overflow-auto">
          {envelopes.length === 0 ? <p className="p-4 text-sm text-text-muted">No envelopes yet.</p> : (
            <ul className="divide-y divide-border">
              {envelopes.map((envelope) => (
                <li key={envelope.id}>
                  <button type="button" onClick={() => setSelectedId(envelope.id)} className={`w-full text-left p-4 hover:bg-bg-input ${selectedId === envelope.id ? "bg-bg-input" : ""}`}>
                    <div className="flex gap-2 items-center"><span className="font-medium text-sm truncate">{envelope.title}</span><Status value={envelope.status} /></div>
                    <p className="text-xs text-text-muted mt-1 truncate">{envelope.source_name}</p>
                    <p className="text-xs text-text-dim mt-2">Expires {formatDate(envelope.expires_at)}</p>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <section className="min-h-0 overflow-auto p-5">
          {creating ? (
            <Card title="New envelope">
              <div className="grid gap-3 max-w-2xl">
                <FilePicker projectId={projectId} picked={pickedFile} onPick={setPickedFile} onError={setMessage} />
                <Input label="Title" value={newEnvelope.title} onChange={(value) => setNewEnvelope({ ...newEnvelope, title: value })} />
                <Input label="Sender name" value={newEnvelope.sender_name} onChange={(value) => setNewEnvelope({ ...newEnvelope, sender_name: value })} />
                <Input label="Message" value={newEnvelope.message} onChange={(value) => setNewEnvelope({ ...newEnvelope, message: value })} />
                <div className="flex gap-2"><Primary onClick={createEnvelope} disabled={busy || !pickedFile}>Create draft</Primary><Secondary onClick={() => { setCreating(false); setPickedFile(null); }}>Cancel</Secondary></div>
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
                    <div key={index} className="grid gap-2 items-center" style={{ gridTemplateColumns: "36px 1fr 1fr 120px 36px" }}>
                      <span className="text-xs font-semibold" style={{ color: RECIPIENT_COLORS[index % RECIPIENT_COLORS.length] }}>{index + 1}</span>
                      <input className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder="Name" value={recipient.name} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { name: event.target.value })} />
                      <input className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder="Email (optional in manual mode)" value={recipient.email || ""} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { email: event.target.value })} />
                      <select className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" value={recipient.role} onChange={(event) => updateAt(setRecipientDrafts, recipientDrafts, index, { role: event.target.value as Recipient["role"] })}><option value="signer">Signer</option><option value="approver">Approver</option></select>
                      <button type="button" onClick={() => setRecipientDrafts(recipientDrafts.filter((_, i) => i !== index))}>×</button>
                    </div>
                  ))}
                  <div><Primary onClick={saveRecipients} disabled={busy || recipientDrafts.length === 0}>Save recipients</Primary></div>
                </div>
              </Card>}

              {detail.status === "draft" && (detail.recipients?.length || 0) > 0 && (
                <FieldEditor
                  key={detail.id}
                  projectId={projectId}
                  envelope={detail}
                  recipients={detail.recipients!}
                  fields={fieldDrafts}
                  setFields={setFieldDrafts}
                  onSave={saveFields}
                  busy={busy}
                />
              )}

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

// ─── Storage file picker ────────────────────────────────────────────

function FilePicker({ projectId, picked, onPick, onError }: {
  projectId: string;
  picked: StorageFile | null;
  onPick: (file: StorageFile | null) => void;
  onError: (message: string) => void;
}) {
  const [files, setFiles] = useState<StorageFile[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const storageUrl = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${STORAGE_API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const load = useCallback(async (search: string) => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ content_type: "application/pdf", limit: "40" });
      if (search.trim()) params.set("q", search.trim());
      const response = await fetch(storageUrl(`/files?${params}`), { credentials: "same-origin" });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || `Storage list failed (${response.status})`);
      setFiles((data.files || []).filter((f: StorageFile) => (f.content_type || "").includes("pdf") || /\.pdf$/i.test(f.name)));
    } catch (error) {
      onError((error as Error).message);
    } finally {
      setLoading(false);
    }
  }, [storageUrl, onError]);

  useEffect(() => {
    const t = setTimeout(() => load(query), 250);
    return () => clearTimeout(t);
  }, [query, load]);

  const upload = async (file: File) => {
    setUploading(true);
    try {
      const body = new FormData();
      body.append("file", file, file.name);
      body.append("folder", "/contracts/");
      const response = await fetch(storageUrl("/files"), { method: "POST", credentials: "same-origin", body });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || `Upload failed (${response.status})`);
      const uploaded: StorageFile = data.file || data;
      onPick(uploaded);
      load(query);
    } catch (error) {
      onError((error as Error).message);
    } finally {
      setUploading(false);
    }
  };

  return (
    <div className="border border-border rounded-lg">
      <div className="flex items-center gap-2 p-2 border-b border-border">
        <input
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          placeholder="Search PDFs in Storage"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <input ref={fileInputRef} type="file" accept="application/pdf,.pdf" className="hidden" onChange={(event) => { const f = event.target.files?.[0]; if (f) upload(f); event.target.value = ""; }} />
        <Secondary onClick={() => fileInputRef.current?.click()} disabled={uploading}>{uploading ? "Uploading…" : "Upload PDF"}</Secondary>
      </div>
      <div className="max-h-56 overflow-auto">
        {loading && files.length === 0 ? <p className="p-3 text-xs text-text-muted">Loading…</p> :
          files.length === 0 ? <p className="p-3 text-xs text-text-muted">No PDFs found. Upload one to get started.</p> : (
            <ul className="divide-y divide-border">
              {files.map((file) => (
                <li key={file.id}>
                  <button type="button" onClick={() => onPick(picked?.id === file.id ? null : file)} className={`w-full text-left px-3 py-2 flex items-center gap-3 hover:bg-bg-input ${picked?.id === file.id ? "bg-bg-input" : ""}`}>
                    <PdfIcon />
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm truncate">{file.name}</span>
                      <span className="block text-xs text-text-muted truncate">{file.folder || "/"}{file.size ? ` · ${formatSize(file.size)}` : ""}</span>
                    </span>
                    {picked?.id === file.id && <span className="text-xs font-semibold text-text">Selected</span>}
                  </button>
                </li>
              ))}
            </ul>
          )}
      </div>
    </div>
  );
}

// ─── Visual field placement ─────────────────────────────────────────

type DragState = {
  mode: "move" | "resize";
  index: number;
  startX: number;
  startY: number;
  orig: Field;
};

function FieldEditor({ projectId, envelope, recipients, fields, setFields, onSave, busy }: {
  projectId: string;
  envelope: Envelope;
  recipients: Recipient[];
  fields: Field[];
  setFields: (fields: Field[]) => void;
  onSave: (fields: Field[]) => void;
  busy: boolean;
}) {
  const [pageNum, setPageNum] = useState(1);
  const [numPages, setNumPages] = useState(0);
  const [pageSize, setPageSize] = useState<{ width: number; height: number } | null>(null);
  const [activeRecipient, setActiveRecipient] = useState(recipients[0]?.id || 0);
  const [selected, setSelected] = useState(-1);
  const [docError, setDocError] = useState("");
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);
  const pdfRef = useRef<any>(null);
  const dragRef = useRef<DragState | null>(null);
  const fieldsRef = useRef(fields);
  fieldsRef.current = fields;

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const pdfjs = await loadPdfjs();
        const contentUrl = `${STORAGE_API}/files/${envelope.source_file_id}/content?project_id=${encodeURIComponent(projectId)}`;
        const response = await fetch(contentUrl, { credentials: "same-origin" });
        if (!response.ok) throw new Error(`document fetch failed (${response.status})`);
        const bytes = await response.arrayBuffer();
        const doc = await pdfjs.getDocument({ data: bytes }).promise;
        if (cancelled) return;
        pdfRef.current = doc;
        setNumPages(doc.numPages);
        setPageNum(1);
      } catch (error) {
        if (!cancelled) setDocError((error as Error).message);
      }
    })();
    return () => { cancelled = true; };
  }, [envelope.source_file_id, projectId]);

  useEffect(() => {
    const doc = pdfRef.current;
    const canvas = canvasRef.current;
    if (!doc || !canvas || !numPages) return;
    let cancelled = false;
    (async () => {
      const page = await doc.getPage(pageNum);
      if (cancelled) return;
      const base = page.getViewport({ scale: 1 });
      const cssWidth = Math.min(canvas.parentElement?.clientWidth || 760, 860);
      const scale = cssWidth / base.width;
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      const viewport = page.getViewport({ scale: scale * dpr });
      canvas.width = Math.floor(viewport.width);
      canvas.height = Math.floor(viewport.height);
      const cssHeight = Math.floor(base.height * scale);
      setPageSize({ width: cssWidth, height: cssHeight });
      // intent "print" renders in one pass without requestAnimationFrame
      // chunking — rAF throttling in background tabs left renders hanging.
      await page.render({ canvasContext: canvas.getContext("2d"), viewport, intent: "print" }).promise;
    })();
    return () => { cancelled = true; };
  }, [pageNum, numPages]);

  const recipientColor = useCallback((recipientId: number) => {
    const index = recipients.findIndex((r) => r.id === recipientId);
    return RECIPIENT_COLORS[(index < 0 ? 0 : index) % RECIPIENT_COLORS.length];
  }, [recipients]);

  const addField = (type: Field["field_type"]) => {
    if (!activeRecipient) return;
    const preset = FIELD_DEFAULTS[type];
    const next: Field = {
      recipient_id: activeRecipient,
      field_type: type,
      page: pageNum,
      x: 0.35,
      y: 0.4,
      width: preset.width,
      height: preset.height,
      label: preset.label,
      required: type !== "checkbox",
    };
    setFields([...fieldsRef.current, next]);
    setSelected(fieldsRef.current.length);
  };

  const beginDrag = (event: React.PointerEvent, index: number, mode: DragState["mode"]) => {
    event.preventDefault();
    event.stopPropagation();
    setSelected(index);
    dragRef.current = { mode, index, startX: event.clientX, startY: event.clientY, orig: { ...fieldsRef.current[index] } };
    const onMove = (e: PointerEvent) => {
      const drag = dragRef.current;
      const overlay = overlayRef.current;
      if (!drag || !overlay) return;
      const rect = overlay.getBoundingClientRect();
      const dx = (e.clientX - drag.startX) / rect.width;
      const dy = (e.clientY - drag.startY) / rect.height;
      const list = [...fieldsRef.current];
      const f = { ...drag.orig };
      if (drag.mode === "move") {
        f.x = clamp(drag.orig.x + dx, 0, 1 - f.width);
        f.y = clamp(drag.orig.y + dy, 0, 1 - f.height);
      } else {
        f.width = clamp(drag.orig.width + dx, 0.02, 1 - f.x);
        f.height = clamp(drag.orig.height + dy, 0.012, 1 - f.y);
      }
      list[drag.index] = f;
      setFields(list);
    };
    const onUp = () => {
      dragRef.current = null;
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  const selectedField = selected >= 0 && selected < fields.length ? fields[selected] : null;
  const pageFields = fields.map((field, index) => ({ field, index })).filter(({ field }) => field.page === pageNum);

  return (
    <Card title="Place fields" right={
      <div className="flex items-center gap-2">
        <Secondary onClick={() => { setPageNum(Math.max(1, pageNum - 1)); setSelected(-1); }} disabled={pageNum <= 1}>Prev</Secondary>
        <span className="text-xs text-text-muted">Page {pageNum} / {numPages || "…"}</span>
        <Secondary onClick={() => { setPageNum(Math.min(numPages, pageNum + 1)); setSelected(-1); }} disabled={pageNum >= numPages}>Next</Secondary>
      </div>
    }>
      <div className="flex flex-wrap items-center gap-2 mb-3">
        <span className="text-xs text-text-muted">For</span>
        <select className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs" value={activeRecipient} onChange={(event) => setActiveRecipient(Number(event.target.value))}>
          {recipients.map((recipient) => <option key={recipient.id} value={recipient.id}>{recipient.name} ({recipient.role})</option>)}
        </select>
        <span className="text-xs text-text-muted">add</span>
        {(Object.keys(FIELD_DEFAULTS) as Field["field_type"][]).map((type) => (
          <Secondary key={type} onClick={() => addField(type)} disabled={!activeRecipient}>{FIELD_DEFAULTS[type].label}</Secondary>
        ))}
      </div>

      {docError ? <p className="text-sm text-text-muted">Could not load the PDF preview: {docError}</p> : (
        <div className="overflow-auto border border-border rounded-lg p-3" style={{ backgroundColor: "#e9edf2" }}>
          <div ref={overlayRef} className="relative mx-auto" style={pageSize ? { width: pageSize.width, height: pageSize.height } : { width: "100%", minHeight: 240 }} onPointerDown={() => setSelected(-1)}>
            <canvas ref={canvasRef} className="block w-full h-full rounded" style={{ backgroundColor: "#ffffff", boxShadow: "0 2px 10px rgba(16,24,40,0.13)" }} />
            {pageFields.map(({ field, index }) => {
              const color = recipientColor(field.recipient_id);
              const isSelected = index === selected;
              return (
                <div
                  key={index}
                  onPointerDown={(event) => beginDrag(event, index, "move")}
                  className="absolute rounded flex items-center justify-center overflow-hidden select-none"
                  style={{
                    left: `${field.x * 100}%`,
                    top: `${field.y * 100}%`,
                    width: `${field.width * 100}%`,
                    height: `${field.height * 100}%`,
                    border: `2px solid ${color}`,
                    backgroundColor: color + "1f",
                    cursor: "move",
                    boxShadow: isSelected ? `0 0 0 3px ${color}55` : "none",
                    fontSize: 11,
                    fontWeight: 600,
                    color,
                  }}
                >
                  <span className="truncate px-1">{field.label || FIELD_DEFAULTS[field.field_type].label}</span>
                  <span
                    onPointerDown={(event) => beginDrag(event, index, "resize")}
                    className="absolute"
                    style={{ right: -1, bottom: -1, width: 12, height: 12, cursor: "nwse-resize", backgroundColor: color, borderTopLeftRadius: 4 }}
                  />
                </div>
              );
            })}
          </div>
        </div>
      )}

      {selectedField && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-xs">
          <span className="font-semibold" style={{ color: recipientColor(selectedField.recipient_id) }}>{FIELD_DEFAULTS[selectedField.field_type].label}</span>
          <input className="bg-bg-input border border-border rounded px-2 py-1.5" placeholder="Label" value={selectedField.label} onChange={(event) => updateAt(setFields, fields, selected, { label: event.target.value })} />
          <select className="bg-bg-input border border-border rounded px-2 py-1.5" value={selectedField.recipient_id} onChange={(event) => updateAt(setFields, fields, selected, { recipient_id: Number(event.target.value) })}>
            {recipients.map((recipient) => <option key={recipient.id} value={recipient.id}>{recipient.name}</option>)}
          </select>
          <label className="flex items-center gap-1 text-text-muted"><input type="checkbox" checked={selectedField.required} onChange={(event) => updateAt(setFields, fields, selected, { required: event.target.checked })} /> required</label>
          <Secondary onClick={() => { setFields(fields.filter((_, i) => i !== selected)); setSelected(-1); }}>Delete</Secondary>
        </div>
      )}

      <div className="mt-3 flex items-center gap-3">
        <Primary onClick={() => onSave(fields)} disabled={busy || fields.length === 0}>Save fields</Primary>
        <span className="text-xs text-text-muted">{fields.length} field{fields.length === 1 ? "" : "s"} across {new Set(fields.map((f) => f.page)).size || 0} page{new Set(fields.map((f) => f.page)).size === 1 ? "" : "s"}. Drag to move, corner handle to resize.</span>
      </div>
    </Card>
  );
}

// ─── Helpers ────────────────────────────────────────────────────────

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

function updateAt<T>(setter: (value: T[]) => void, list: T[], index: number, patch: Partial<T>) {
  setter(list.map((item, i) => i === index ? { ...item, ...patch } : item));
}

function formatDate(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function PdfIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" className="text-text-muted shrink-0">
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
      <path d="M14 2v6h6" />
    </svg>
  );
}

function Status({ value }: { value: string }) {
  return <span className="ml-auto text-xs uppercase tracking-wide px-2 py-0.5 rounded bg-border text-text-muted">{value}</span>;
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
