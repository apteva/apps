import { audioIssues, metricLabel, type AudioReport } from "./audio-dashboard";
const lossIssues = new Set(["playback_underrun", "dropped_audio", "carrier_stall", "browser_error", "audio_degraded", "sequence_gaps"]);
export function audioIssueTone(issue: string): "error" | "warning" {
  return lossIssues.has(issue) ? "error" : "warning";
}
export function audioWidgetProblems(report: AudioReport) {
  return report.issues.map((code) => ({ code, label: audioIssues[code as keyof typeof audioIssues] || code, tone: audioIssueTone(code) }))
    .sort((a, b) => Number(b.tone === "error") - Number(a.tone === "error"));
}
export function audioWidgetMeasurements(report: AudioReport) {
  const metrics = report.metrics;
  // Boundaries are displayed separately: summing their drops may double-count audio.
  const important = ["server_webrtc_ingress_concealed_ms", "webrtc_concealed_ms", "webrtc_packets_lost", "webrtc_packets_discarded", "playback_underrun_ms", "playback_dropped_ms", "carrier_max_gap_ms", "carrier_source_dropped_ms", "reconnects", "playback_sequence_gaps", "capture_sequence_gaps", "context_suspended_ms", "max_rtt_ms"];
  return important.filter((key) => Number.isFinite(metrics[key]) && metrics[key] > 0)
    .map((key) => ({ key, label: (metricLabel[key] || key.replace(/_/g, " ")).replace(/ \(ms\)$/, ""), value: key.endsWith("_ms") ? (metrics[key] >= 1000 ? `${(metrics[key] / 1000).toLocaleString(undefined, { maximumFractionDigits: 2 })} s` : `${Math.round(metrics[key])} ms`) : String(metrics[key]) }));
}
