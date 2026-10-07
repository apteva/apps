export function hostingPending(hostings: Array<{ status: string; id?: string }>, intents: Array<{ status: string; hosting_id?: string }>): boolean {
  return hostings.some(h => h.status === "processing" || h.status === "reserved" && h.id && intents.some(i => i.status === "submitted" && i.hosting_id === h.id)) || intents.some(i => i.status === "waiting_checksum");
}
export function hostingStageLabel(stage: string): string {
  return stage.replaceAll("_", " ").replace(/^./, c => c.toUpperCase());
}
export function transcodingMessages(value: unknown): string[] {
  if (value == null) return [];
  const values = Array.isArray(value) ? value : [value];
  return values.map(v => typeof v === "string" ? v : typeof v === "object" && v !== null ? String((v as Record<string, unknown>).message || JSON.stringify(v)) : String(v));
}
export function hostingNotice(result: { pending_checksum?: boolean; checksum_status?: string; warning?: string; hosting?: { status: string }; hosting_intent?: { status: string } }): string {
  if (result.pending_checksum) return `Waiting for checksum verification (${result.checksum_status || "pending"}). Upload will resume automatically.`;
  if (result.checksum_status === "failed") return "Checksum verification failed. Upload has not started.";
  if (result.warning) return result.warning;
  if (result.hosting?.status === "ready") return "Video is already ready on its host.";
  if (result.hosting?.status === "processing") return "Upload started. Encoding progress will update automatically.";
  return "Hosting request needs attention; check its recorded state.";
}
