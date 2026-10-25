import type { AudioDashboardHost } from "./audio-dashboard";
export type DiagnosticObject = Record<string, unknown>;
export const object = (value: unknown): DiagnosticObject =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as DiagnosticObject)
    : {};
export const list = (value: unknown): DiagnosticObject[] =>
  Array.isArray(value) ? value.map(object) : [];
export const at = (value: unknown, path: string): unknown =>
  path.split(".").reduce<unknown>((v, key) => object(v)[key], value);
export const number = (value: unknown): number | undefined =>
  typeof value === "number" && Number.isFinite(value) ? value : undefined;
export const text = (value: unknown): string =>
  typeof value === "string" ? value : "";
export const timestamp = (value: unknown): number =>
  typeof value === "string" && Number.isFinite(Date.parse(value))
    ? Date.parse(value)
    : 0;
const newest = (rows: DiagnosticObject[], key: string) =>
  [...rows].sort((a, b) => timestamp(a[key]) - timestamp(b[key])).at(-1) ?? {};
export const callIsActive = (status: unknown) =>
  [
    "pending",
    "answering",
    "initiated",
    "ringing",
    "answered",
    "in-progress",
  ].includes(text(status));
export function diagnosticsURL(
  host: AudioDashboardHost,
  callID: string,
  history: "none" | "incremental",
  cursor = "",
): string {
  const q = new URLSearchParams({
    call_id: callID,
    history,
    project_id: host.projectId ?? "",
    install_id: String(host.installId ?? ""),
  });
  if (cursor) q.set("history_cursor", cursor);
  return `/api/apps/${encodeURIComponent(host.appName || "telephony")}/audio-health?${q}`;
}
export function mergeDiagnostics(
  previous: DiagnosticObject | null,
  next: DiagnosticObject,
): DiagnosticObject {
  const page = object(next.carrier_history);
  const history = page.reset_required
    ? []
    : list(previous?.carrier_transport_history);
  const byID = new Map(history.map((v) => [text(v.id), v]));
  for (const v of list(page.records ?? next.carrier_transport_history))
    if (text(v.id)) byID.set(text(v.id), v);
  return {
    ...previous,
    ...next,
    carrier_transport_history: [...byID.values()]
      .sort((a, b) => timestamp(a.at) - timestamp(b.at))
      .slice(-256),
  };
}
export type DiagnosticMetric = {
  label: string;
  value?: number;
  unit?: string;
  basis: string;
};
export type DiagnosticSeries = {
  label: string;
  unit: string;
  points: { time: number; value: number }[];
};
export type DiagnosticEvent = {
  id: string;
  time: number;
  stage: string;
  label: string;
  detail: string;
};
const humanize = (v: string) => v.replaceAll("_", " ").replaceAll("-", " ");
const metricFormatters = [0, 1].map(maximumFractionDigits => new Intl.NumberFormat(undefined, { maximumFractionDigits }));
export function formatMetric(value: number | undefined, unit = ""): string {
  if (value === undefined) return "—";
  const digits = unit === "ms" ? 1 : unit === "kb/s" ? 1 : 0;
  return `${metricFormatters[digits].format(value)}${unit ? ` ${unit}` : ""}`;
}
export function diagnosticsModel(
  data: DiagnosticObject,
  receivedAt = Date.now(),
  now = Date.now(),
) {
  const call = object(data.call),
    browser = object(data.browser),
    carrier = object(data.carrier);
  const ingress = object(at(browser, "server.webrtc.rtp_ingress"));
  const history = list(data.carrier_transport_history);
  const current = object(data.carrier_live);
  const sipRecord = Object.keys(object(current.sip)).length
    ? current
    : newest(
        history.filter((r) => Object.keys(object(r.sip)).length > 0),
        "at",
      );
  const carrierRecord = Object.keys(object(current.carrier)).length
    ? current
    : newest(
        history.filter((r) => Object.keys(object(r.carrier)).length > 0),
        "at",
      );
  const carrierLive = object(carrierRecord.carrier);
  const sip = object(sipRecord.sip),
    rtp = object(sip.rtp),
    jitter = object(sip.jitter_buffer);
  const samples = list(data.transport_samples ?? browser.transport_samples);
  const sample = newest(samples, "timestamp"),
    metrics = object(sample.metrics),
    states = object(sample.states);
  const rtc = object(browser.webrtc);
  const agent = Object.keys(object(data.agent_audio_live)).length
    ? object(data.agent_audio_live)
    : object(carrier.agent_audio);
  const hasBrowser = !!(
    text(browser.received_at) ||
    text(browser.media_transport) ||
    samples.length
  );
  const hasSIP = Object.keys(sip).length > 0;
  const hasAI = Object.keys(agent).length > 0 || call.peer_kind === "realtime";
  const active =
    typeof call.active === "boolean" ? call.active : callIsActive(call.status);
  const observed = Math.max(
    timestamp(browser.received_at),
    timestamp(carrier.updated_at),
    timestamp(sipRecord.at),
    timestamp(carrierRecord.at),
    timestamp(sample.received_at ?? sample.timestamp),
  );
  const age = observed
    ? Math.max(0, (timestamp(data.generated_at) || receivedAt) - observed) +
      Math.max(0, now - receivedAt)
    : undefined;
  const state = !observed
    ? "No telemetry"
    : !active
      ? "Recorded"
      : (age ?? 0) > 15000
        ? "Telemetry stale"
        : "Live observations";
  const metric = (
    label: string,
    value: unknown,
    unit = "",
    basis = "Reported cumulative total",
  ): DiagnosticMetric => ({ label, value: number(value), unit, basis });
  const browserMetrics = [
    metric(
      "Browser ↔ Telephony RTT",
      browser.rtt_ms ?? metrics.rtt_ms,
      "ms",
      "Latest observation",
    ),
    metric(
      "Receive jitter",
      rtc.jitterMs ??
        (number(metrics.receiver_jitter) === undefined
          ? undefined
          : Number(metrics.receiver_jitter) * 1000),
      "ms",
      "Latest observation",
    ),
    metric(
      "Playback buffer",
      rtc.jitterBufferIntervalMs ?? browser.playback_queue_ms,
      "ms",
      rtc.jitterBufferIntervalMs !== undefined
        ? "Recent interval"
        : "Latest observation",
    ),
    metric(
      "Received packets lost",
      rtc.packetsLost ?? metrics.receiver_packetsLost,
    ),
    metric("Replacement audio", rtc.concealedMs, "ms"),
    metric(
      "Microphone audio discarded",
      at(browser, "timing.transport.capture_dropped_ms"),
      "ms",
    ),
    metric(
      "Send bitrate",
      number(rtc.sendBitrateBps) === undefined
        ? undefined
        : Number(rtc.sendBitrateBps) / 1000,
      "kb/s",
      "Latest observation",
    ),
    metric(
      "Receive bitrate",
      number(rtc.receiveBitrateBps) === undefined
        ? undefined
        : Number(rtc.receiveBitrateBps) / 1000,
      "kb/s",
      "Latest observation",
    ),
    metric(
      "Main-thread pause",
      at(browser, "timing.runtime.main_thread_max_pause_ms"),
      "ms",
      "Call maximum",
    ),
    metric("Reconnections", at(browser, "server.browser_socket.reconnects")),
    metric(
      "Capture processing wait",
      at(browser, "server.webrtc.rtp_ingress.max_queue_wait_ms"),
      "ms",
      "Call maximum",
    ),
    metric("Playback audio discarded", browser.playback_dropped_ms, "ms"),
    metric("Microphone RTP packets", ingress.packets),
    metric(
      "Microphone arrival gap",
      ingress.max_arrival_gap_ms,
      "ms",
      "Call maximum",
    ),
    metric(
      "Microphone kernel drops",
      ingress.socket_drops_available === true
        ? ingress.socket_drops
        : undefined,
    ),
    metric(
      "WebRTC ingress discard",
      at(browser, "server.webrtc.ingress_discarded_ms"),
      "ms",
    ),
    metric("WebRTC decode errors", at(browser, "server.webrtc.decode_errors")),
    metric(
      "Send queue discard",
      at(browser, "server.carrier_pacer.dropped_ms"),
      "ms",
    ),
    metric(
      "Capture transit excess",
      at(browser, "server.capture_transit_excess_ms"),
      "ms",
      "Latest observation",
    ),
    metric(
      "Server scheduler P99",
      at(browser, "server.process.scheduler_p99_ms"),
      "ms",
      "Process histogram; shared across calls",
    ),
    metric(
      "Server GC pause P99",
      at(browser, "server.process.gc_pause_p99_ms"),
      "ms",
      "Process histogram; shared across calls",
    ),
  ];
  const sipMetrics = [
    metric("Packets received", rtp.packets_received),
    metric("Packets sent", rtp.packets_sent),
    metric("Missing RTP sequences", rtp.sequence_loss),
    metric("Out-of-order packets", rtp.out_of_order),
    metric("Receive jitter", rtp.jitter_ms, "ms", "Latest observation"),
    metric("Buffered audio", jitter.queue_ms, "ms", "Latest observation"),
    metric("Longest arrival gap", rtp.max_gap_ms, "ms", "Call maximum"),
    metric("Longest outgoing gap", rtp.max_send_gap_ms, "ms", "Call maximum"),
    metric(
      "Kernel receive drops",
      rtp.socket_receive_drops_available === true
        ? rtp.socket_receive_drops
        : undefined,
    ),
    metric("Read errors", rtp.read_errors),
    metric("Send errors", rtp.send_errors),
    metric("Decode errors", rtp.decode_errors),
    metric("Late-arrival discard", jitter.late_discarded_ms, "ms"),
    metric("Buffer expiry discard", jitter.expired_discarded_ms, "ms"),
    metric("Buffer overflow discard", jitter.overflow_discarded_ms, "ms"),
    metric("Concealment", jitter.concealed_ms, "ms"),
    metric("Clock drift", jitter.clock_drift_ppm, "ppm", "Latest estimate"),
    metric("Rebuffer events", jitter.fresh_delivery_rebuffers),
    metric(
      "Send queue discard",
      sip.send_pacer_available === true
        ? at(sip, "send_pacer.dropped_ms")
        : undefined,
      "ms",
    ),
    metric(
      "Send source age",
      sip.send_pacer_available === true
        ? at(sip, "send_pacer.max_source_age_ms")
        : undefined,
      "ms",
      "Call maximum",
    ),
    metric(
      "Send queue residence",
      sip.send_pacer_available === true
        ? at(sip, "send_pacer.max_residence_ms")
        : undefined,
      "ms",
      "Call maximum",
    ),
    metric(
      "Core forwarding buffer",
      sip.core_forward_available === true
        ? at(sip, "core_forward.queued_ms")
        : undefined,
      "ms",
      "Latest observation",
    ),
    metric("Hangup failures", at(sip, "signaling.bye_failures")),
    metric("Session refresh failures", at(sip, "signaling.refresh_failures")),
  ];
  const carrierMetrics = [
    metric("Incoming media frames", carrierLive.media_frames),
    metric("Carrier decode errors", carrierLive.decode_errors),
    metric(
      "Longest carrier arrival gap",
      carrierLive.max_gap_across_generations_ms,
      "ms",
      "Call maximum across connections",
    ),
    metric(
      "Outgoing audio queued",
      carrier.max_queued_ms,
      "ms",
      "Call maximum",
    ),
    metric("Outgoing stale audio discarded", carrier.dropped_stale_ms, "ms"),
    metric("Incoming audio discarded", carrier.inbound_dropped_ms, "ms"),
    metric(
      "Incoming queue age",
      carrier.inbound_max_queue_age_ms,
      "ms",
      "Call maximum",
    ),
    metric("Carrier sequence gaps", carrier.carrier_sequence_gaps),
  ];
  const stageNames: Record<string, string> = {
    carrier_to_telephony: "Carrier → Telephony",
    telephony_to_browser: "Telephony → browser",
    browser_to_telephony: "Browser → Telephony",
    internal_processing: "Internal audio processing",
  };
  const stages = Object.entries(
    object(at(browser, "server.audio_health.stages")),
  ).map(([key, value]) => ({
    label: stageNames[key] || humanize(key),
    state: humanize(text(object(value).state)) || "Unobserved",
    detail: text(object(value).signal ?? object(value).reason),
    observed: text(browser.received_at),
  }));
  if (hasSIP)
    for (const [key, label] of [
      ["transport_state", "SIP transport"],
      ["processing_state", "SIP processing"],
    ])
      if (text(sip[key]))
        stages.push({
          label,
          state: humanize(text(sip[key])),
          detail: "",
          observed: text(sipRecord.at),
        });
  const reply = object(agent.reply_timing),
    replies = list(reply.recent),
    lastReply = replies.at(-1) ?? {},
    link = object(agent.core_link);
  const agentMetrics = [
    metric(
      "Caller speech → first Core audio",
      lastReply.speech_to_core_audio_ms,
      "ms",
      "Latest reply; Telephony clock",
    ),
    metric(
      "First Core audio → carrier write",
      lastReply.core_audio_to_carrier_write_ms,
      "ms",
      "Latest reply; transmission",
    ),
    metric(
      "Longest wait for Core audio",
      reply.max_speech_to_core_audio_ms,
      "ms",
      "Call maximum",
    ),
    metric(
      "Longest dispatch delay",
      reply.max_core_audio_to_carrier_write_ms,
      "ms",
      "Call maximum",
    ),
    metric("Output staging", link.max_output_staging_ms, "ms", "Call maximum"),
    metric("Core reconnects", link.reconnects),
    metric("Input discarded", link.input_discarded_ms, "ms"),
    metric("Output omitted", link.output_omitted_ms, "ms"),
  ];
  const series = (
    label: string,
    unit: string,
    rows: DiagnosticObject[],
    time: (r: DiagnosticObject) => unknown,
    value: (r: DiagnosticObject) => unknown,
  ): DiagnosticSeries => {
    const points = new Map<number, number>();
    for (const r of rows) {
      const t = timestamp(time(r)),
        n = number(value(r));
      if (t && n !== undefined) points.set(t, n);
    }
    return {
      label,
      unit,
      points: [...points]
        .sort(([a], [b]) => a - b)
        .slice(-256)
        .map(([time, value]) => ({ time, value })),
    };
  };
  const sipRows = [
    ...history,
    ...(Object.keys(current).length ? [current] : []),
  ];
  const charts: DiagnosticSeries[] = [];
  if (hasBrowser)
    charts.push(
      series(
        "Browser round-trip time",
        "ms",
        samples,
        (r) => r.timestamp,
        (r) => at(r, "metrics.rtt_ms"),
      ),
      series(
        "Browser receive buffer",
        "ms",
        samples,
        (r) => r.timestamp,
        (r) =>
          at(r, "metrics.receiver_jitter_buffer_interval_ms") ??
          at(r, "metrics.queue_ms"),
      ),
    );
  if (hasSIP)
    charts.push(
      series(
        "SIP buffered audio",
        "ms",
        sipRows,
        (r) => r.at,
        (r) => at(r, "sip.jitter_buffer.queue_ms"),
      ),
      series(
        "SIP receive jitter",
        "ms",
        sipRows,
        (r) => r.at,
        (r) => at(r, "sip.rtp.jitter_ms"),
      ),
    );
  if (hasAI)
    charts.push(
      series(
        "Reply: speech → first Core audio",
        "ms",
        replies,
        (r) => r.first_core_audio_received_at,
        (r) => r.speech_to_core_audio_ms,
      ),
      series(
        "Reply: Core audio → carrier write",
        "ms",
        replies,
        (r) => r.first_carrier_write_at,
        (r) => r.core_audio_to_carrier_write_ms,
      ),
    );
  const events = new Map<string, DiagnosticEvent>();
  const add = (rows: DiagnosticObject[], stage: string) => {
    for (const r of rows) {
      const time = timestamp(
        r.occurred_at ?? r.timestamp ?? r.at ?? r.started_at,
      );
      const label = humanize(
        text(r.reason ?? r.action ?? r.kind ?? r.type ?? r.event),
      );
      if (!time || !label) continue;
      const duration = number(r.duration_ms);
      const id = `${stage}:${text(r.id)}:${time}:${label}`;
      if (events.has(id)) continue;
      events.set(id, {
        id,
        time,
        stage,
        label,
        detail:
          duration !== undefined
            ? `${formatMetric(duration, "ms")} · reported audio duration`
            : text(r.outcome ?? r.detail).slice(0, 180),
      });
    }
  };
  add(list(at(browser, "server.webrtc.ice_events")), "WebRTC connection");
  add(list(at(browser, "server.webrtc.rtp_ingress.events")), "Microphone RTP");
  add(list(at(browser, "server.webrtc.events")), "WebRTC processing");
  add(list(at(browser, "server.capture_drop_events")), "Microphone capture");
  add(list(at(browser, "server.browser_socket.events")), "Browser connection");
  add(list(browser.session_events), "Browser");
  add(list(browser.drop_events), "Browser");
  add(list(browser.playback_events), "Playback");
  add(list(carrier.drop_events), "Carrier output");
  // Latest snapshot wins; repeated retained events are formatted only once.
  for (const r of [...sipRows].sort((a,b)=>timestamp(b.at)-timestamp(a.at))) {
    add(list(at(r, "sip.jitter_buffer.events")), "SIP buffer");
    add(list(at(r, "sip.signaling.events")), "SIP signaling");
    if (r.event) add([object(r.event)], "Carrier");
  }
  add(list(link.events), "Core link");
  add(
    list(agent.clears).map((r) => ({
      ...r,
      at: number(r.requested_at_ms)
        ? new Date(Number(r.requested_at_ms)).toISOString()
        : undefined,
      kind: "Playback cleared",
      detail: `${text(r.scope) || "Unspecified scope"} · local discard ${formatMetric(number(r.local_discarded_ms), "ms")} · remote ${r.remote_confirmed === true ? "confirmed" : r.remote_command_sent === true ? "command sent; unconfirmed" : "not confirmed"}`,
    })),
    "Agent playback",
  );
  const freshness = (t: unknown) => {
    const ts = timestamp(t);
    return !ts
      ? "No observation time"
      : !active
        ? `Recorded ${new Date(ts).toLocaleTimeString()}`
        : (timestamp(data.generated_at) || receivedAt) - ts + now - receivedAt >
            15000
          ? "Observation stale"
          : `Observed ${new Date(ts).toLocaleTimeString()}`;
  };
  return {
    browserFreshness: freshness(browser.received_at),
    sipFreshness: freshness(sipRecord.at),
    agentFreshness: freshness(carrier.updated_at),
    call,
    active,
    browser,
    carrier,
    sip,
    agent,
    stages,
    carrierMetrics,
    hasCarrier: Object.keys(carrierLive).length > 0 || !!text(carrier.provider),
    hasBrowser,
    hasSIP,
    hasAI,
    observed,
    age,
    state,
    browserMetrics,
    sipMetrics,
    agentMetrics,
    charts,
    events: [...events.values()].sort((a, b) => b.time - a.time).slice(0, 100),
    browserPath: [
      text(browser.media_transport),
      text(rtc.candidateType ?? states.local_candidate),
      text(rtc.relayProtocol ?? rtc.protocol ?? states.protocol),
    ]
      .filter(Boolean)
      .join(" · "),
    sipCodec: text(at(sip, "signaling.codec")),
    historyCount: history.length,
  };
}
