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
  "receiver_jitter_buffer_interval_ms", "receiver_jitter_target_interval_ms", "receiver_jitter_minimum_interval_ms", "receiver_processing_interval_ms", "receiver_last_packet_age_ms",
  "sender_bytesSent", "sender_packetsSent", "sender_headerBytesSent", "sender_retransmittedPacketsSent", "sender_retransmittedBytesSent", "sender_nackCount", "sender_targetBitrate", "sender_send_delay_interval_ms",
  "remote_receiver_packetsLost", "remote_receiver_fractionLost", "remote_receiver_jitter", "remote_receiver_roundTripTime", "remote_receiver_totalRoundTripTime", "remote_receiver_roundTripTimeMeasurements",
  "remote_sender_packetsSent", "remote_sender_bytesSent", "pair_availableOutgoingBitrate", "pair_availableIncomingBitrate", "pair_currentRoundTripTime", "pair_totalRoundTripTime",
  "pair_bytesSent", "pair_bytesReceived", "pair_requestsSent", "pair_requestsReceived", "pair_responsesSent", "pair_responsesReceived", "pair_consentRequestsSent", "path_revision", "codec_clock_rate", "codec_channels"
] as const;
export const TRANSPORT_STATES: Record<string, readonly string[]> = {
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
export class TransportTelemetrySender {

  private auxiliary:unknown[]=[];
  skippedAuxiliary=0;
  skippedSamples=0;
  private auxiliaryTurn=false;
  private pending:TransportWireSample[]=[];
  private timer?:ReturnType<typeof setInterval>;
  private report?:unknown;
  constructor(private send:(sample:TransportWireSample)=>boolean,private sendReport?:(report:unknown)=>boolean) {}
  enqueueReport(report:unknown):void {this.report=report;this.start();}
  enqueueAuxiliary(report:unknown):void {this.auxiliary.push(report);if(this.auxiliary.length>32){this.auxiliary.shift();this.skippedAuxiliary++;}this.start();}
  private start():void {if(!this.timer)this.timer=setInterval(()=>this.tick(),250);}
  enqueue(samples:TransportSample[]):void {
    for(const sample of samples){
      const parts=transportSampleParts(sample);
      // Preserve the in-flight sample. Dropping its leading parts on every
      // refresh otherwise prevents any complete record on a slow connection.
      while(this.pending.length+parts.length>32){
        const tail=this.pending.at(-1);if(!tail||tail.id===this.pending[0]?.id)break;
        this.pending=this.pending.filter(p=>p.id!==tail.id);this.skippedSamples++;
      }
      if(this.pending.length+parts.length<=32)this.pending.push(...parts);else this.skippedSamples++;
    }
    if(this.pending.length)this.start();
  }
  tick():void {
    try{
      if(this.report!==undefined && this.sendReport){if(this.sendReport(this.report))this.report=undefined;}
      else if(this.auxiliary.length && this.sendReport && (!this.pending.length||this.auxiliaryTurn)){
        if(this.sendReport(this.auxiliary[0])){this.auxiliary.shift();this.auxiliaryTurn=false;}
      }
      else if(this.pending.length && this.send(this.pending[0])){this.pending.shift();this.auxiliaryTurn=true;}
    }catch{/* observer failure never changes media */}
    if(!this.pending.length&&!this.auxiliary.length&&this.report===undefined){clearInterval(this.timer);this.timer=undefined;}
  }
  stop():void {clearInterval(this.timer);this.timer=undefined;this.pending=[];this.auxiliary=[];this.report=undefined;}
}
