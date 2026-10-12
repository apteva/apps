/** Observational only. No audio samples, URLs, SDP or candidate addresses. */
export interface TransportSample {
  id: string; timestamp: string; transport: "websocket" | "webrtc";
  reason: "periodic" | "incident" | "incident_context";
  window_ms: number; metrics: Record<string, number>; states: Record<string, string>;
}
export const TRANSPORT_METRICS = [
  "rtt_ms", "queue_ms", "target_ms", "buffered_bytes", "underruns", "dropped_ms",
  "capture_sequence_gaps", "playback_sequence_gaps", "main_thread_max_pause_ms", "worker_max_tick_gap_ms",
  "capture_sent_ms", "capture_muted_ms", "capture_dropped_ms", "playback_ingress_ms", "playback_transport_dropped_ms",
  "playback_source_dropped_ms", "clock_uncertainty_ms", "clock_sample_age_ms", "stats_errors", "stats_duration_ms",
  "send_bitrate_bps", "receive_bitrate_bps", "receive_gap_ms", "capture_age_ms", "transit_ms", "delivery_excess_ms", "server_queue_ms",
  "receiver_concealed_delta_ms", "receiver_silent_concealed_delta_ms", "receiver_concealed_window_ms", "requested_playback_target_ms", "requested_upload_bitrate_bps", "receiver_ssrc", "receiver_loss_delta", "receiver_loss_window_ms", "remote_receiver_ssrc", "remote_receiver_loss_delta", "remote_receiver_loss_window_ms",
  "receiver_bytesReceived", "receiver_packetsReceived", "receiver_packetsLost", "receiver_jitter", "receiver_packetsDiscarded",
  "receiver_concealedSamples", "receiver_silentConcealedSamples", "receiver_concealmentEvents", "receiver_insertedSamplesForDeceleration", "receiver_removedSamplesForAcceleration",
  "receiver_totalSamplesReceived", "receiver_audioLevel", "receiver_totalAudioEnergy", "receiver_totalSamplesDuration", "receiver_nackCount", "receiver_fecPacketsReceived", "receiver_fecPacketsDiscarded",
  "receiver_jitter_buffer_interval_ms", "receiver_jitter_target_interval_ms", "receiver_jitter_minimum_interval_ms", "receiver_processing_interval_ms", "receiver_totalProcessingDelay", "receiver_totalSamplesDecoded", "telemetry_skipped_samples", "telemetry_skipped_auxiliary", "telemetry_queue_age_ms", "receiver_last_packet_age_ms",
  "sender_bytesSent", "sender_packetsSent", "sender_headerBytesSent", "sender_retransmittedPacketsSent", "sender_retransmittedBytesSent", "sender_nackCount", "sender_targetBitrate", "sender_send_delay_interval_ms",
  "remote_receiver_packetsLost", "remote_receiver_fractionLost", "remote_receiver_jitter", "remote_receiver_roundTripTime", "remote_receiver_totalRoundTripTime", "remote_receiver_roundTripTimeMeasurements",
  "remote_sender_packetsSent", "remote_sender_bytesSent", "pair_availableOutgoingBitrate", "pair_availableIncomingBitrate", "pair_currentRoundTripTime", "pair_totalRoundTripTime",
  "pair_bytesSent", "pair_bytesReceived", "pair_requestsSent", "pair_requestsReceived", "pair_responsesSent", "pair_responsesReceived", "pair_consentRequestsSent", "path_revision", "codec_clock_rate", "codec_channels"
] as const;
export const TRANSPORT_STATES: Record<string, readonly string[]> = {
  receiver_processing: ["unavailable", "per_decoded_sample"],
  connection: ["connected", "reconnecting", "closed"], context: ["running", "suspended", "interrupted", "closed"],
  muted: ["true", "false"], device_muted: ["true", "false"], track: ["live", "ended"],
  ice: ["new", "checking", "connected", "completed", "disconnected", "failed", "closed"],
  dtls: ["new", "connecting", "connected", "closed", "failed"], pair: ["frozen", "waiting", "in-progress", "failed", "succeeded"],
  local_candidate: ["host", "srflx", "prflx", "relay"], remote_candidate: ["host", "srflx", "prflx", "relay"],
  protocol: ["udp", "tcp"], relay_protocol: ["udp", "tcp", "tls"], codec: ["audio/opus", "audio/PCMU", "audio/PCMA", "pcm16"]
};
export function transportMetrics(input: Record<string, unknown>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const k of TRANSPORT_METRICS) if (typeof input[k] === "number" && Number.isFinite(input[k])) out[k] = Math.max(0, Math.min(input[k] as number, 1e12));
  return out;
}
export function transportStates(input: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, allowed] of Object.entries(TRANSPORT_STATES)) if (typeof input[k] === "string" && allowed.includes(input[k] as string)) out[k] = input[k] as string;
  return out;
}
/** Sample off the render/frame path. At most two samples per five seconds,
 * including one preceding observation on a new incident. No catch-up loop. */
export class TransportTelemetry {
  private serial = 0;
  private lastEmit = -Infinity;
  private previousAt?: number;
  private previous?: TransportSample;
  private incident?: {sample:TransportSample;context?:TransportSample};
  private history: TransportSample[] = [];
  private pending: TransportSample[] = [];
  private counters: Record<string, number> = {};
  constructor(private transport: TransportSample["transport"]) {}
  observe(metrics: Record<string, unknown>, states: Record<string, unknown>, now = performance.now(), timestamp = new Date().toISOString(), windowMS?:number): void {
    if (!Number.isFinite(now) || !Number.isFinite(Date.parse(timestamp))) return;
    if (this.previousAt !== undefined && now < this.previousAt) { this.lastEmit=-Infinity; this.previous=undefined; this.counters={}; this.incident=undefined; }
    const values=transportMetrics(metrics), labels=transportStates(states);
    let incident=false;
    for (const k of ["underruns", "dropped_ms", "capture_dropped_ms", "playback_transport_dropped_ms", "playback_source_dropped_ms", "receiver_packetsLost", "remote_receiver_packetsLost", "receiver_concealmentEvents", "path_revision", "stats_errors"]) {
      if (values[k] !== undefined) { if (this.counters[k] !== undefined && values[k]>this.counters[k]) incident=true; this.counters[k]=values[k]; }
    }
    const sample:TransportSample={id:`s${++this.serial}`,timestamp,transport:this.transport,reason:incident?"incident":"periodic",window_ms:Math.min(60000,Math.max(0,typeof windowMS==="number"&&Number.isFinite(windowMS)?windowMS:this.previousAt===undefined?0:now-this.previousAt)),metrics:values,states:labels};
    this.previousAt=now;
    if(incident && !this.incident)this.incident={sample,context:this.previous};
    if(now-this.lastEmit>=5000) {
      if(this.incident){if(this.incident.context)this.enqueue({...this.incident.context,reason:"incident_context"});this.enqueue(this.incident.sample);this.incident=undefined;}
      else this.enqueue(sample);this.lastEmit=now;
    }
    this.previous=sample;
  }
  private enqueue(sample:TransportSample) { this.history.push(sample); if(this.history.length>24)this.history.shift();this.pending.push(sample);if(this.pending.length>8)this.pending.shift(); }
  recent():TransportSample[] { return this.history.map(s=>({...s,metrics:{...s.metrics},states:{...s.states}})); }
  drain():TransportSample[] { return this.pending.splice(0,2); }
}

export interface TransportWireSample extends TransportSample { part_index:number;part_count:number }
/** Small paced control frames keep rich monitoring from becoming a large
 * competing TCP burst on an Opus connection. Parts share one sample identity. */
export function transportSampleParts(sample:TransportSample):TransportWireSample[] {
  const base={...sample,metrics:{},states:{},part_index:0,part_count:16};
  const parts:TransportWireSample[]=[];let current:TransportWireSample=base;
  for(const field of ["metrics","states"] as const)for(const [key,value] of Object.entries(sample[field])) {
    const next={...current,[field]:{...current[field],[key]:value}} as TransportWireSample;
    if(JSON.stringify(next).length>768){parts.push(current);current={...base,metrics:{},states:{},[field]:{[key]:value}} as TransportWireSample;}
    else current=next;
  }
  parts.push(current);
  if(parts.length>16)return []; // Unknown/oversized observations never gate media.
  return parts.map((part,i)=>({...part,part_index:i,part_count:parts.length}));
}
// Cumulative counters remain in every compact sample; complete native counters
// are sent at most every 30s. Incidents take priority over their context copy.
const compactMetricKeys = ["receiver_loss_delta","remote_receiver_loss_delta","receiver_concealed_delta_ms","receiver_ssrc","remote_receiver_ssrc","rtt_ms","queue_ms","target_ms","receiver_packetsLost","remote_receiver_packetsLost","receiver_concealedSamples","dropped_ms","capture_dropped_ms","playback_transport_dropped_ms","playback_source_dropped_ms","receiver_jitter","buffered_bytes","main_thread_max_pause_ms","worker_max_tick_gap_ms","receiver_processing_interval_ms","path_revision","stats_errors"];
export function compactTransportSample(sample:TransportSample):TransportSample {
  const metrics:Record<string,number>={};
  for(const key of compactMetricKeys)if(sample.metrics[key]!==undefined){
    metrics[key]=sample.metrics[key];
    if(transportSampleParts({...sample,metrics}).length>2)delete metrics[key];
  }
  return {...sample,metrics};
}
export class TransportTelemetrySender {
  private auxiliary:{report:unknown;at:number;runtime:boolean}[]=[];
  skippedAuxiliary=0;
  skippedSamples=0;
  private auxiliaryTurn=false;
  private pending:TransportWireSample[]=[];
  private queuedAt=new Map<string,number>();
  private lastFull=-Infinity;
  private timer?:ReturnType<typeof setInterval>;
  private report?:unknown;
  private reportAt=0;
  static readonly maxAgeMS=12000;
  constructor(private send:(sample:TransportWireSample)=>boolean,private sendReport?:(report:unknown)=>boolean,private now=()=>performance.now()) {}
  enqueueReport(report:unknown):void {if(this.report!==undefined)this.skippedAuxiliary++;this.report=report;this.reportAt=this.now();this.start();}
  enqueueAuxiliary(report:unknown):void {
    const runtime=!!(report as any)?.diagnostics?.timing?.runtime;
    if(runtime){const old=this.auxiliary.length;this.auxiliary=this.auxiliary.filter(v=>!v.runtime);this.skippedAuxiliary+=old-this.auxiliary.length;}
    this.auxiliary.push({report,at:this.now(),runtime});
    if(this.auxiliary.length>16){this.auxiliary.shift();this.skippedAuxiliary++;}this.start();
  }
  private start():void {if(!this.timer)this.timer=setInterval(()=>this.tick(),250);}
  private discard(id:string):void {this.pending=this.pending.filter(p=>p.id!==id);this.queuedAt.delete(id);this.skippedSamples++;}
  enqueue(samples:TransportSample[]):void {
    const now=this.now();
    // A preceding context is useful locally, but sending both rich records
    // exceeds the 640B/s budget on every incident. Prefer its current evidence.
    const selected=[...samples].reverse().find(s=>s.reason==="incident")??samples.at(-1);
    this.skippedSamples+=Math.max(0,samples.length-(selected?1:0));
    if(selected){
      const full=selected.reason==="periodic"&&now-this.lastFull>=30000;
      if(full)this.lastFull=now;
      let sample=full?selected:compactTransportSample(selected);
      sample={...sample,metrics:{...sample.metrics,telemetry_skipped_samples:this.skippedSamples,telemetry_skipped_auxiliary:this.skippedAuxiliary}};
      const parts=transportSampleParts(sample);
      // Finish a young in-flight sample and retain only the newest successor.
      const active=this.pending[0]?.id;
      for(const id of this.queuedAt.keys())if(id!==active)this.discard(id);
      if(this.queuedAt.has(sample.id))this.discard(sample.id);
      if(parts.length){this.pending.push(...parts);this.queuedAt.set(sample.id,now);}
    }
    if(this.pending.length)this.start();
  }
  tick():void {
    const now=this.now();
    for(const [id,at] of this.queuedAt)if(now-at>TransportTelemetrySender.maxAgeMS)this.discard(id);
    this.auxiliary=this.auxiliary.filter(v=>{if(now-v.at>TransportTelemetrySender.maxAgeMS){this.skippedAuxiliary++;return false;}return true;});
    if(this.report!==undefined&&now-this.reportAt>TransportTelemetrySender.maxAgeMS){this.report=undefined;this.skippedAuxiliary++;}
    try{
      if(this.report!==undefined && this.sendReport){if(this.sendReport(this.report))this.report=undefined;}
      else if(this.auxiliary.length && this.sendReport && (!this.pending.length||this.auxiliaryTurn)){
        if(this.sendReport(this.auxiliary[0].report)){this.auxiliary.shift();this.auxiliaryTurn=false;}
      }
      else if(this.pending.length){
        const part=this.pending[0];
        const metrics={...part.metrics,telemetry_queue_age_ms:Math.max(0,now-(this.queuedAt.get(part.id)??now))};
        if(this.send({...part,metrics})){this.pending.shift();if(part.part_index===part.part_count-1)this.queuedAt.delete(part.id);this.auxiliaryTurn=true;}
      }
    }catch{/* observer failure never changes media */}
    if(!this.pending.length&&!this.auxiliary.length&&this.report===undefined){clearInterval(this.timer);this.timer=undefined;}
  }
  stop():void {clearInterval(this.timer);this.timer=undefined;this.pending=[];this.queuedAt.clear();this.auxiliary=[];this.report=undefined;}
}
