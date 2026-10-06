const input = "w-full rounded border border-border bg-surface-2 px-2 py-1.5 text-xs text-text";
export default function RuntimeControls({ value, onChange }: { value: string; onChange: (value: string) => void }) {
 let limits: Record<string, any>;
 try { limits = JSON.parse(value); if (!limits || typeof limits !== "object" || Array.isArray(limits)) throw new Error(); }
 catch { return <p className="text-xs text-red-300">Enter a valid limits object below to use runtime controls.</p>; }
 const set = (key: string, v: any) => onChange(JSON.stringify({ ...limits, [key]: v }, null, 2));
 return <div className="space-y-3 rounded border border-border p-3 text-xs">
  <label className="flex items-center gap-2"><input type="checkbox" checked={!!limits.coalesce_reads} onChange={e => set("coalesce_reads", e.target.checked)} />Share identical in-flight reads</label>
  <p className="text-text-dim">Sharing requires a published release and pure reads. HTTP and Function sources must declare read_only. Identities and permission scopes stay isolated.</p>
  <label className="block space-y-1"><span>Read consistency</span><select className={input} value={limits.read_consistency || "none"} onChange={e => set("read_consistency", e.target.value)}><option value="none">Backend defaults</option><option value="batch">Snapshot per batch</option><option value="request">Snapshot per source for the request</option></select></label>
  <p className="text-text-dim">Tables supports batch snapshots. Request snapshots require a capable upstream adapter. Unsupported consistency returns an error.</p>
  <div className="grid grid-cols-2 gap-2">{[["max_concurrent_requests", "Concurrent requests / API", 128, 1, 4096], ["max_concurrent_operations", "Concurrent executions / operation", 32, 1, 1024], ["max_queued_operations", "Queued executions", 128, 0, 10000], ["max_queue_ms", "Queue timeout (ms)", 1000, 1, 300000], ["max_coalesced_waiters", "Shared callers / execution", 128, 1, 10000], ["max_snapshot_ms", "Snapshot lifetime (ms)", 15000, 1, 300000]].map(([key, label, fallback, min, max]) => <label key={String(key)} className="block space-y-1"><span>{label}</span><input className={input} type="number" min={Number(min)} max={Number(max)} value={limits[String(key)] ?? fallback} onChange={e => set(String(key), Number(e.target.value))} /></label>)}</div>
 </div>;
}
