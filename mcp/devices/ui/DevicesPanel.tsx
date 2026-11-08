import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/devices";

interface Device {
  id: string;
  name: string;
  description: string;
  protocol: string;
  model: string;
  manufacturer: string;
  firmware: string;
  enabled: boolean;
  status: string;
  availability: string;
  last_seen?: string;
  manifest: Record<string, any>;
  state?: StateValue[];
}

interface StateValue { key: string; value: any; value_type: string; unit?: string; source: string; updated_at: string }
interface Command { id: string; operation: string; target?: string; status: string; result?: any; error?: string; created_at: string }
interface Summary { devices: number; online: number; offline: number; disabled: number; commands_24h: number; failed_commands_24h: number }
interface Enrollment { device: Device; mqtt: { endpoint: string; host: string; port: number; username: string; password: string }; topics: Record<string, string> }
type Tab = "overview" | "controls" | "commands" | "history" | "setup";
interface PanelProps { installId?: number; projectId?: string }

export default function DevicesPanel(props:PanelProps = {}){return <ScopedDevicesPanel key={`${props.installId || ""}:${props.projectId || ""}`} {...props}/>}
function ScopedDevicesPanel({installId, projectId}: PanelProps) {
 const provisionKey=useRef(crypto.randomUUID());
 const actionKeys=useRef(new Map<string,string>());
 const deviceRequest=useRef(0);
 const [pendingOperation,setPendingOperation]=useState("");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [devices, setDevices] = useState<Device[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [selected, setSelected] = useState<Device | null>(null);
  const [commands, setCommands] = useState<Command[]>([]);
  const [tab, setTab] = useState<Tab>("overview");
  const [query, setQuery] = useState("");
  const [notice, setNotice] = useState("");
  const [showProvision, setShowProvision] = useState(false);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [form, setForm] = useState({ name: "", device_id: "", model: "", protocol: "apteva.devices/v2" });

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const url=new URL(`${API}${installId ? `/_install/${encodeURIComponent(String(installId))}` : ""}${path}`,window.location.origin);
 if(projectId)url.searchParams.set("project_id",projectId);
 const response = await fetch(url.toString(), {
      credentials: "same-origin",
      ...init,
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed: ${response.status}`);
    return body;
  }, [installId, projectId]);

  const loadList = useCallback(async () => {
    const q = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : "";
    const [sum, rows] = await Promise.all([api<Summary>("/summary"), api<Device[]>(`/devices${q}`)]);
    setSummary(sum);
    setDevices(rows || []);
    if (!selectedId && rows?.length) setSelectedId(rows[0].id);
  }, [api, query, selectedId]);

  const loadDevice = useCallback(async () => {
    const request=++deviceRequest.current;
    if (!selectedId) { setSelected(null); setCommands([]); return; }
    const [device, history] = await Promise.all([
      api<Device>(`/devices/${encodeURIComponent(selectedId)}`),
      api<Command[]>(`/devices/${encodeURIComponent(selectedId)}/commands?limit=100`),
    ]);
    if(request!==deviceRequest.current)return;
    setSelected(device);
    setCommands(history || []);
  }, [api, selectedId]);

  useEffect(() => { loadList().catch(err => setNotice(err.message)); }, [loadList]);
  useEffect(() => { loadDevice().catch(err => setNotice(err.message)); }, [loadDevice]);
  useEffect(() => {
    const timer = window.setInterval(() => { loadList().catch(() => {}); loadDevice().catch(() => {}); }, 5000);
    return () => window.clearInterval(timer);
  }, [loadList, loadDevice]);

  async function provision() {
    if (!form.name.trim()) return;
    const result = await api<any>("/devices", { method: "POST", headers:{"Idempotency-Key":provisionKey.current}, body: JSON.stringify(form) });
    if(result.status==="pending"){setPendingOperation(result.operation_id);setNotice("Provisioning is recovering. Check the operation below.");return}
    if(!result.mqtt){setNotice(result.recovery_action || "Credential recovery is unavailable.");return}
    provisionKey.current=crypto.randomUUID();
    setEnrollment(result); setSelectedId(result.device.id); setShowProvision(false);
    setForm({ name: "", device_id: "", model: "", protocol:"apteva.devices/v2" });
    await loadList(); await loadDevice();
  }

  async function action(path: string, body: any = {}) {
    const identity=`${path}:${JSON.stringify(body)}`;let key=body.idempotency_key || actionKeys.current.get(identity);if(!key){key=crypto.randomUUID();actionKeys.current.set(identity,key)}
    const result = await api<any>(path, { method: "POST", body: JSON.stringify(body), headers:{"Idempotency-Key":key} });
    actionKeys.current.delete(identity);
    if(result.status==="pending"){setPendingOperation(result.operation_id);setNotice("Operation is recovering.")}else{setNotice(result.command?.status ? `Command ${result.command.status}` : "Saved");}
    await loadList(); await loadDevice();
    return result;
  }

  const filtered = useMemo(() => devices, [devices]);

  return <div className="h-full min-h-[640px] bg-bg text-text flex flex-col">
    <header className="px-5 py-4 border-b border-border flex items-center justify-between gap-4">
      <div><h1 className="text-xl font-semibold">Devices</h1><p className="text-sm text-text-muted">Connect devices, inspect structured data, and run advertised operations.</p></div>
      <button className="rounded-md bg-accent text-bg px-3 py-2 text-sm font-medium" onClick={() => setShowProvision(true)}>Provision device</button>
    </header>

    <section className="grid grid-cols-2 md:grid-cols-6 gap-3 px-5 py-4 border-b border-border">
      <Metric label="Devices" value={summary?.devices ?? 0}/><Metric label="Online" value={summary?.online ?? 0} good />
      <Metric label="Offline" value={summary?.offline ?? 0}/><Metric label="Disabled" value={summary?.disabled ?? 0}/>
      <Metric label="Commands · 24h" value={summary?.commands_24h ?? 0}/><Metric label="Failures · 24h" value={summary?.failed_commands_24h ?? 0}/>
    </section>

    {pendingOperation && <button className="mx-5 mt-3 text-left text-sm text-accent" onClick={async()=>{try{const result=await api<any>(`/operations/${pendingOperation}`);setNotice(`Operation ${result.status}${result.operation?.error ? `: ${result.operation.error}` : ""}`);if(result.status==="completed"){setPendingOperation("");if(result.mqtt)setEnrollment(result);else if(result.password)window.alert(`Recovered password: ${result.password}`);await loadList();await loadDevice();}}catch(err){setNotice(String(err))}}}>Check operation {pendingOperation}</button>}
    {notice && <button className="mx-5 mt-3 text-left text-xs text-text-muted" onClick={() => setNotice("")}>{notice} ×</button>}

    <main className="flex-1 min-h-0 grid lg:grid-cols-[300px_minmax(0,1fr)]">
      <aside className="border-r border-border min-h-0 overflow-auto p-3">
        <input className="w-full bg-bg-input border border-border rounded-md px-3 py-2 text-sm mb-3" value={query} onChange={e => setQuery(e.target.value)} placeholder="Search devices…" />
        <div className="space-y-1">{filtered.map(device => <button key={device.id} onClick={() => setSelectedId(device.id)} className={`w-full text-left rounded-md p-3 border ${selectedId === device.id ? "border-accent bg-accent/10" : "border-transparent hover:bg-bg-input/50"}`}>
          <div className="flex items-center justify-between gap-2"><span className="font-medium truncate">{device.name}</span><Status value={device.enabled ? device.status : "disabled"}/></div>
          <div className="text-xs text-text-muted mt-1 truncate">{device.id}{device.model ? ` · ${device.model}` : ""}</div>
        </button>)}</div>
        {!filtered.length && <div className="text-sm text-text-muted p-4 text-center">No devices yet.</div>}
      </aside>

      <section className="min-w-0 min-h-0 overflow-auto">
        {selected ? <>
          <div className="px-5 pt-5 flex items-start justify-between gap-4">
            <div><div className="flex items-center gap-2"><h2 className="text-lg font-semibold">{selected.name}</h2><Status value={selected.enabled ? selected.status : "disabled"}/></div><p className="text-xs text-text-muted font-mono mt-1">{selected.id}</p></div>
            <button className="text-xs border border-border rounded-md px-3 py-1.5" onClick={() => action(`/devices/${selected.id}/${selected.enabled ? "disable" : "enable"}`)}>{selected.enabled ? "Disable" : "Enable"}</button>
          </div>
          <nav className="px-5 mt-4 flex gap-1 border-b border-border">{(["overview", "controls", "commands", "history", "setup"] as Tab[]).map(value => <button key={value} onClick={() => setTab(value)} className={`capitalize text-sm px-3 py-2 border-b-2 ${tab === value ? "border-accent text-text" : "border-transparent text-text-muted"}`}>{value}</button>)}</nav>
          <div className="p-5">
            {tab === "overview" && <Overview device={selected}/>}
            {tab === "controls" && <Controls key={selected.id} device={selected} action={action}/>}
            {tab === "commands" && <Commands rows={commands}/>}
            {tab === "history" && <History key={selected.id} deviceId={selected.id} api={api}/> }
            {tab === "setup" && <Setup device={selected} api={api} action={action} onSaved={loadDevice}/>}
          </div>
        </> : <div className="h-full grid place-items-center text-sm text-text-muted">Select or provision a device.</div>}
      </section>
    </main>

    {showProvision && <Modal title="Provision a device" onClose={() => setShowProvision(false)}>
      <div className="space-y-3"><label className="block text-xs text-text-muted">Device protocol<select className="block w-full mt-1 bg-bg-input border border-border p-2 rounded-md" value={form.protocol} onChange={e=>setForm({...form,protocol:e.target.value})}><option value="apteva.devices/v2">Generic structured data</option><option value="apteva.devices/v1">Native GPIO / firmware</option></select></label><Field label="Name" value={form.name} onChange={name => setForm({...form, name})}/><Field label="Device ID (optional)" value={form.device_id} onChange={device_id => setForm({...form, device_id})} placeholder="greenhouse-1"/><Field label="Model (optional)" value={form.model} onChange={model => setForm({...form, model})}/>
        <button className="w-full rounded-md bg-accent text-bg py-2 text-sm font-medium" onClick={() => provision().catch(err => setNotice(err.message))}>Create credentials</button>
      </div>
    </Modal>}

    {enrollment && <Modal title="Save these credentials now" onClose={() => setEnrollment(null)}>
      <p className="text-sm text-text-muted mb-3">Save the returned username, password, and exact topics in your client. Credentials can be recovered through the operation for the configured recovery window.</p>
      <Secret label="MQTT endpoint" value={enrollment.mqtt.endpoint}/><Secret label="MQTT username" value={enrollment.mqtt.username}/><Secret label="MQTT password" value={enrollment.mqtt.password}/>
      <Secret label="Device ID" value={enrollment.device.id}/><pre className="mt-3 bg-bg-input border border-border rounded-md p-3 text-xs overflow-auto">{JSON.stringify(enrollment.topics,null,2)}</pre>
      <button className="mt-4 w-full rounded-md bg-accent text-bg py-2 text-sm" onClick={() => setEnrollment(null)}>I saved the credentials</button>
    </Modal>}
  </div>;
}

function Overview({device}: {device: Device}) {
  return <div className="space-y-5"><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-3"><Info label="Protocol" value={device.protocol}/><Info label="Model" value={device.model || "—"}/><Info label="Firmware" value={device.firmware || "—"}/><Info label="Last seen" value={device.last_seen ? new Date(device.last_seen).toLocaleString() : "Never"}/></div>
    <div><h3 className="text-sm font-medium mb-2">Current state</h3><div className="grid sm:grid-cols-2 xl:grid-cols-3 gap-2">{(device.state || []).map(item => <div key={item.key} className="border border-border rounded-md p-3"><div className="text-xs text-text-muted">{item.key}</div><div className="font-mono mt-1 break-all">{formatValue(item.value)} {item.unit}</div><div className="text-[10px] text-text-muted mt-1">{item.source} · {new Date(item.updated_at).toLocaleString()}</div></div>)}</div>{!device.state?.length && <p className="text-sm text-text-muted">No state received yet.</p>}</div>
  </div>;
}

function Controls({device, action}: {device: Device; action: (path: string, body?: any) => Promise<any>}) {
  const variables = (device.manifest?.variables || []) as any[];
  const functions = (device.manifest?.functions || []) as any[];
  const pins = (device.manifest?.pins || []) as any[];
  const command = (operation: string, target: string, args: any = {}) => action(`/devices/${device.id}/commands`, {operation, target, arguments: args, wait: false});
  const operations=(device.manifest?.operations || []) as any[];
  const [operation,setOperation]=useState("");const [argumentsJSON,setArgumentsJSON]=useState("{}");const [commandKey,setCommandKey]=useState(()=>crypto.randomUUID());const [error,setError]=useState("");
  async function submitGeneric(){try{const args=JSON.parse(argumentsJSON);if(!args || typeof args!=="object" || Array.isArray(args))throw new Error("Arguments must be a JSON object");await action(`/devices/${device.id}/commands`,{operation,arguments:args,wait:false,idempotency_key:commandKey});setCommandKey(crypto.randomUUID());setError("")}catch(e){setError(String(e))}}
  if(device.protocol==="apteva.devices/v2")return <section className="space-y-3"><h3 className="font-medium">Available operations</h3><p className="text-xs text-text-muted">Commands run asynchronously. Results appear in Commands.</p><select className="bg-bg-input border border-border p-2 rounded-md" value={operation} onChange={e=>setOperation(e.target.value)}><option value="">Select operation</option>{operations.map(op=><option key={op.name} value={op.name}>{op.name}</option>)}</select><textarea aria-label="Command arguments" className="w-full min-h-40 font-mono text-xs bg-bg-input border border-border rounded-md p-3" value={argumentsJSON} onChange={e=>setArgumentsJSON(e.target.value)}/><button disabled={!operation} className="border border-border rounded-md px-3 py-2" onClick={submitGeneric}>Submit command</button>{error && <p className="text-sm text-red-400">{error}</p>}<pre className="text-xs overflow-auto">{JSON.stringify(operations.find(op=>op.name===operation)?.input_schema || {},null,2)}</pre>{!operations.length && <Empty text="Advertise operations in Setup or from your device manifest."/>}</section>;
  return <div className="space-y-6">
    <section><div className="flex justify-between mb-2"><h3 className="text-sm font-medium">Variables</h3><button className="text-xs text-accent" onClick={() => action(`/devices/${device.id}/capabilities/refresh`)}>Refresh capabilities</button></div><div className="grid sm:grid-cols-2 gap-2">{variables.map(v => <button key={v.name} className="border border-border rounded-md p-3 text-left hover:bg-bg-input/50" onClick={() => command("variable.get", v.name, {name: v.name})}><div className="font-medium text-sm">{v.name}</div><div className="text-xs text-text-muted">Read {v.type || "value"}{v.unit ? ` · ${v.unit}` : ""}</div></button>)}</div>{!variables.length && <Empty text="No variables advertised."/>}</section>
    <section><h3 className="text-sm font-medium mb-2">Functions</h3><div className="grid sm:grid-cols-2 gap-2">{functions.map(fn => <button key={fn.name || fn} className="border border-border rounded-md p-3 text-left hover:bg-bg-input/50" onClick={() => { const raw = window.prompt(`Arguments for ${fn.name || fn} (JSON object or text)`, "{}"); if (raw == null) return; let args: any = {}; try { args = JSON.parse(raw); } catch { args = {args: raw}; } command("function.call", fn.name || fn, args); }}><div className="font-medium text-sm">{fn.name || fn}</div><div className="text-xs text-text-muted">Run firmware function</div></button>)}</div>{!functions.length && <Empty text="No functions advertised."/>}</section>
    <section><h3 className="text-sm font-medium mb-2">GPIO pins</h3><div className="grid sm:grid-cols-2 xl:grid-cols-3 gap-2">{pins.map(pin => <PinControl key={pin.name || pin.number} pin={pin} command={command}/>)}</div>{!pins.length && <Empty text="No GPIO pins allowlisted. Add pins in Setup before controlling them."/>}</section>
  </div>;
}

function PinControl({pin, command}: {pin: any; command: (op: string, target: string, args?: any) => void}) {
  const key = String(pin.name || pin.number); const kind = pin.type || "digital";
  return <div className="border border-border rounded-md p-3"><div className="flex justify-between"><div><div className="font-medium text-sm">{key}</div><div className="text-xs text-text-muted">Pin {pin.number} · {kind}</div></div></div><div className="flex gap-1 mt-3"><button className="border border-border rounded px-2 py-1 text-xs" onClick={() => command("pin.read", key, {pin: pin.number, kind})}>Read</button>{pin.writable !== false && <><button className="border border-border rounded px-2 py-1 text-xs" onClick={() => command("pin.write", key, {pin: pin.number, kind, value: 1})}>On</button><button className="border border-border rounded px-2 py-1 text-xs" onClick={() => command("pin.write", key, {pin: pin.number, kind, value: 0})}>Off</button></>}</div></div>;
}

function Commands({rows}: {rows: Command[]}) { return <div className="space-y-2">{rows.map(row => <div key={row.id} className="border border-border rounded-md p-3"><div className="flex justify-between gap-3"><div><span className="font-mono text-xs">{row.operation}</span>{row.target && <span className="text-xs text-text-muted"> · {row.target}</span>}</div><Status value={row.status}/></div><div className="text-[10px] text-text-muted mt-1">{row.id} · {new Date(row.created_at).toLocaleString()}</div>{row.error && <div className="text-xs text-red-400 mt-2">{row.error}</div>}{row.result != null && <pre className="text-xs mt-2 bg-bg-input rounded p-2 overflow-auto">{JSON.stringify(row.result, null, 2)}</pre>}</div>)}{!rows.length && <Empty text="No commands sent yet."/>}</div> }

function Setup({device, api, action, onSaved}: {device: Device; api: <T>(path: string, init?: RequestInit) => Promise<T>; action: (path: string, body?: any) => Promise<any>; onSaved: () => Promise<void>}) {
  const [manifest, setManifest] = useState(JSON.stringify(device.manifest || {}, null, 2));
  useEffect(() => setManifest(JSON.stringify(device.manifest || {}, null, 2)), [device.id, device.manifest]);
  async function save() { const value = JSON.parse(manifest); await api(`/devices/${device.id}/capabilities`, {method: "PUT", body: JSON.stringify(value)}); await onSaved(); }
  return <div className="space-y-5"><section><h3 className="text-sm font-medium">Capabilities</h3><p className="text-xs text-text-muted my-2">Describe available operations and their JSON input/output schemas. Native GPIO devices also use an explicit pin allowlist.</p><textarea className="w-full min-h-64 font-mono text-xs bg-bg-input border border-border rounded-md p-3" value={manifest} onChange={e => setManifest(e.target.value)}/><button className="mt-2 rounded-md bg-accent text-bg px-3 py-2 text-sm" onClick={() => save().catch(err => window.alert(err.message))}>Save capabilities</button></section>
    <section className="border-t border-border pt-4"><h3 className="text-sm font-medium">Credentials</h3><p className="text-xs text-text-muted my-2">Rotation immediately disconnects clients using the old password.</p><button className="border border-border rounded-md px-3 py-2 text-sm" onClick={async () => { const result = await action(`/devices/${device.id}/credentials/rotate`); if(result.password)window.alert(`New password:\n${result.password}`); }}>Rotate password</button></section>
  </div>;
}

function Metric({label, value, good}: {label: string; value: number; good?: boolean}) { return <div className="border border-border rounded-md p-3"><div className="text-xs text-text-muted">{label}</div><div className={`text-xl font-semibold mt-1 ${good && value ? "text-emerald-400" : ""}`}>{value}</div></div> }
function Info({label, value}: {label: string; value: string}) { return <div className="border border-border rounded-md p-3"><div className="text-xs text-text-muted">{label}</div><div className="text-sm mt-1 break-all">{value}</div></div> }
function Status({value}: {value: string}) { const good = value === "online" || value === "succeeded"; const bad = value === "failed" || value === "timed_out"; return <span className={`text-[10px] uppercase tracking-wide rounded-full px-2 py-0.5 ${good ? "bg-emerald-500/15 text-emerald-400" : bad ? "bg-red-500/15 text-red-400" : "bg-bg-input text-text-muted"}`}>{value}</span> }
function Empty({text}: {text: string}) { return <p className="text-sm text-text-muted py-3">{text}</p> }
function Field({label, value, onChange, placeholder}: {label: string; value: string; onChange: (v: string) => void; placeholder?: string}) { return <label className="block text-xs text-text-muted">{label}<input className="mt-1 w-full bg-bg-input border border-border rounded-md px-3 py-2 text-sm text-text" value={value} placeholder={placeholder} onChange={e => onChange(e.target.value)}/></label> }
function Secret({label, value}: {label: string; value: string}) { return <div className="mb-2"><div className="text-xs text-text-muted">{label}</div><button className="mt-1 w-full text-left font-mono text-xs bg-bg-input border border-border rounded-md p-2 break-all" onClick={() => navigator.clipboard.writeText(value)}>{value}</button></div> }
function Modal({title, onClose, children}: {title: string; onClose: () => void; children: any}) { return <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4" onMouseDown={onClose}><div className="w-full max-w-lg max-h-[90vh] overflow-auto bg-bg border border-border rounded-lg shadow-xl" onMouseDown={e => e.stopPropagation()}><div className="px-4 py-3 border-b border-border flex justify-between"><h2 className="font-semibold">{title}</h2><button onClick={onClose}>×</button></div><div className="p-4">{children}</div></div></div> }
function formatValue(value: any) { return typeof value === "string" ? value : JSON.stringify(value); }

function History({deviceId,api}:{deviceId:string;api:<T>(path:string,init?:RequestInit)=>Promise<T>}){
 const [kind,setKind]=useState("data");const [rows,setRows]=useState<any[]>([]);const [cursor,setCursor]=useState("");const [error,setError]=useState("");
 const load=useCallback(async(next="")=>{try{const endpoint=kind==="events"?"history/events":kind;const page=await api<{items:any[];next_cursor?:string}>(`/${endpoint}?device_id=${encodeURIComponent(deviceId)}&limit=25${next?`&cursor=${encodeURIComponent(next)}`:""}`);setRows(old=>next?[...old,...page.items]:page.items);setCursor(page.next_cursor || "");setError("")}catch(e){setError(String(e))}},[kind,deviceId,api]);
 useEffect(()=>{setRows([]);setCursor("");void load()},[load]);
 return <section className="space-y-3"><div className="flex gap-3"><select className="bg-bg-input border border-border rounded p-2" value={kind} onChange={e=>setKind(e.target.value)}><option value="data">Data samples</option><option value="events">Events</option><option value="usage">Usage measurements</option><option value="responses">Command responses</option></select><button className="text-sm text-accent" onClick={()=>void load()}>Refresh</button></div>{error && <p className="text-red-400 text-sm">{error}</p>}{rows.map((row,i)=><pre key={row.seq || row.id || i} className="text-xs bg-bg-input border border-border rounded-md p-3 overflow-auto">{JSON.stringify(row,null,2)}</pre>)}{!rows.length && <Empty text="No records."/>}{cursor && <button className="text-sm border border-border rounded p-2" onClick={()=>void load(cursor)}>Load more</button>}</section>
}
