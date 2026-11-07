import type { NetworkPolicy } from "./network-policy";

/** Optional upload cap, above native WebRTC congestion control. RTT alone is
 * not directional evidence of congestion. Never changes peers, audio or ICE.
 * All work is O(1) at the existing statistics cadence, not per audio frame. */
export class RTCSendPolicy {
  bitrate: number;
  reason = "initial";
  private last?: number;
  private path?: number;
  private ssrc?: number;
  private baseline?: number;
  private pairResponses?:number; private pairRTT?:number; private pairAt?:number;
  private congested?: number;
  private stable?: number;
  private changed = -Infinity;
  constructor(private readonly profile: NetworkPolicy) {
    this.bitrate = profile.initial_bitrate_bps;
  }
  observe(m: Record<string, number>, now: number) {
    if (!Number.isFinite(now)) return;
    const reset = this.last !== undefined && (now <= this.last || now - this.last > 5000) ||
      this.path !== m.path_revision || this.ssrc !== m.remote_receiver_ssrc;
    if (reset) {
      this.baseline = this.congested = this.stable = undefined;
      this.pairResponses=this.pairRTT=this.pairAt=undefined;
      this.changed = now;
      this.reason = "observation_reset";
    }
    this.last = now; this.path = m.path_revision; this.ssrc = m.remote_receiver_ssrc;
    const responses=m.pair_responsesReceived, pairRTT=m.pair_currentRoundTripTime;
    if(Number.isFinite(responses) && responses>0){
      if((this.pairResponses===undefined || responses>this.pairResponses) && Number.isFinite(pairRTT) && pairRTT>0 && pairRTT<5){this.pairRTT=pairRTT;this.pairAt=now;}
      this.pairResponses=responses;
    }
    // Re-reading a cached RTCP report is not another congestion observation.
    const usable = m.remote_receiver_report_age_ms >= 0 &&
      m.remote_receiver_report_age_ms <= 5000 && m.sender_packets_delta > 0 &&
      m.sender_window_ms > 0 && m.sender_window_ms <= 5000;
    const loss = m.remote_receiver_fractionLost;
    if (!usable || !Number.isFinite(loss) || loss < 0 || loss > 1) {
      this.congested = this.stable = undefined;
      return;
    }
    if(m.remote_receiver_report_updated !== 1) return;
    // Some browser/Pion combinations report loss but no RTCP RTT. A new
    // STUN response can supply path RTT; a cached candidate-pair value cannot.
    const rtt = m.remote_receiver_roundTripTime ?? (this.pairAt!==undefined && now-this.pairAt<=5000 ? this.pairRTT : undefined);
    const validRTT = rtt!==undefined && Number.isFinite(rtt) && rtt > 0 && rtt < 5;
    if (validRTT) this.baseline = Math.min(this.baseline ?? rtt, rtt);
    const growth = validRTT && this.baseline !== undefined ? rtt - this.baseline : 0;
    const available = m.pair_availableOutgoingBitrate;
    const bandwidthCap = Number.isFinite(available) && available > 0 && available < 64000
      ? Math.max(this.profile.min_bitrate_bps, Math.floor((available - 32000) * .8 / 1000) * 1000) : undefined;
    const uploadEvidence = loss >= .01 || m.sender_send_delay_interval_ms >= 20 ||
      bandwidthCap !== undefined && bandwidthCap < this.bitrate;
    const delayCongestion = validRTT && growth >= .08 && uploadEvidence;
    if (delayCongestion) this.congested ??= now; else this.congested = undefined;
    const delayReady = this.congested !== undefined && now - this.congested >= 4000;
    if (loss >= .03 || bandwidthCap !== undefined && bandwidthCap < this.bitrate || delayReady) {
      this.stable = undefined;
      if (now - this.changed < (loss >= .03 ? 2000 : 4000)) return;
      this.bitrate = Math.max(this.profile.min_bitrate_bps,
        Math.min(delayReady ? Math.floor(this.bitrate * .75 / 1000) * 1000 : this.bitrate - 4000, bandwidthCap ?? this.bitrate));
      this.changed = now;
      this.reason = loss >= .03 ? "loss" : delayReady ? "sustained_upload_delay" : "bandwidth";
    } else if (loss < .01 && (!validRTT || growth < .04) && !(m.sender_send_delay_interval_ms >= 20)) {
      this.stable ??= now;
      if (now - this.stable >= 10000 && now - this.changed >= 10000) {
        this.bitrate = Math.min(this.profile.max_bitrate_bps, this.bitrate + 2000);
        this.stable = this.changed = now;
        this.reason = "stable_recovery";
      }
    } else this.stable = undefined;
  }
}
