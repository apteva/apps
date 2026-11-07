import { expect, test } from "bun:test";
import { WebRTCAudioConnection } from "../src/webrtc-audio";
import { LegacyRTCQualityPolicy } from "./fixtures/network-policy-01240";
import {
  NetworkProbeSamples,
  runNetworkCheck,
  withNetworkAbort,
} from "../src/network-check";
import {
  DEFAULT_NETWORK_POLICY,
  validateNetworkPolicy,
} from "../src/network-policy";
import { RTCPathHealth, rtcQualityPolicy } from "../src/webrtc-quality";
import { writeFile } from "node:fs/promises";

function packet(
  seq: number,
  upCount = seq + 1,
  highest = seq,
  echo = seq * 20 + 1,
  serverTime = seq * 20,
) {
  const value = new ArrayBuffer(96),
    v = new DataView(value);
  v.setUint32(0, 0x31504e54, true);
  v.setUint32(4, seq, true);
  v.setBigUint64(8, BigInt(serverTime * 1000), true);
  v.setUint32(16, upCount, true);
  v.setUint32(20, highest, true);
  v.setBigUint64(24, BigInt(echo * 1000), true);
  return value;
}
test("network probe measures reorder/loss separately from browser pauses and missing evidence", () => {
  const s = new NetworkProbeSamples();
  for (let i = 0; i < 150; i++) {
    s.sentAt(i * 20 + 1);
    s.receive(packet(i), i * 20 + 51);
  }
  const healthy = s.result("direct", 100, 3000);
  expect(healthy).toMatchObject({
    state: "ready",
    uploadLossPct: 0,
    downloadLossPct: 0,
    received: 150,
  });
  expect(healthy.rttMs).toBe(50);
  s.receive(packet(10), 4000);
  expect(s.result("direct", 0, 4000).received).toBe(150);
  const reorder = new NetworkProbeSamples();
  for (let i = 0; i < 150; i += 2) {
    reorder.receive(packet(i + 1), i * 20 + 51);
    reorder.receive(packet(i), i * 20 + 52);
  }
  expect(reorder.result("direct", 0, 3000).downloadLossPct).toBe(0);
  const loss = new NetworkProbeSamples();
  for (let i = 0; i < 150; i++) {
    loss.sentAt(i * 20 + 1);
    if (i % 10)
      loss.receive(packet(i, Math.floor((i + 1) * 0.9), i), i * 20 + 51);
  }
  expect(loss.result("direct", 0, 3000).state).toBe("degraded");
  expect(loss.result("direct", 0, 3000).downloadLossPct).toBeGreaterThan(9);
  s.sentAt(5000);
  expect(s.result("direct", 0, 5000)).toMatchObject({
    state: "inconclusive",
    reason: "browser_busy",
  });
  expect(new NetworkProbeSamples().result("relay", 0, 2000)).toMatchObject({
    state: "inconclusive",
    reason: "insufficient_samples",
  });
});
test("network checks abort immediately even if authentication never settles", async () => {
  const controller = new AbortController();
  let calls = 0;
  const checking = runNetworkCheck(
    async () => {
      calls++;
      return await new Promise(() => {});
    },
    { signal: controller.signal },
  );
  controller.abort();
  expect((await checking).state).toBe("cancelled");
  expect(calls).toBe(1);
  const aborted = withNetworkAbort(new Promise(() => {}), controller.signal);
  await expect(aborted).rejects.toThrow("cancelled");
});
test("new network profiles validate safe limits without widening queues", () => {
  expect(validateNetworkPolicy({}).media_transport).toBe("inherit");
  for (const policy of [
    { min_bitrate_bps: 8000 },
    { playback_max_ms: 1000 },
    { initial_bitrate_bps: NaN },
    { warning_cooldown_seconds: 1 },
    { degraded_seconds: 1 },
    { media_transport: "magic" },
  ])
    expect(() => validateNetworkPolicy(policy)).toThrow();
  const q = rtcQualityPolicy({
    networkProfile: validateNetworkPolicy({
      initial_bitrate_bps: 24000,
      max_bitrate_bps: 24000,
      min_bitrate_bps: 20000,
    }),
  });
  expect(q.bitrate).toBe(24000);
  for (let i = 0; i < 10; i++)
    q.observe(
      { remote_receiver_fractionLost: 0.2, remote_receiver_report_updated: 1 },
      i * 2000,
    );
  expect(q.bitrate).toBe(20000);
});
test("quality notices require sustained evidence, respects cooldown, and stops after two attempts", () => {
  const h = new RTCPathHealth({
    ...DEFAULT_NETWORK_POLICY,
    quality_warnings: true,
  });
  const bad = { receiver_concealed_delta_ms: 120, receiver_jitter: 0.06 };
  expect(h.observe({}, 0, true)).toBeUndefined();
  for (let t = 1000; t < 7000; t += 1000)
    expect(h.observe(bad, t, true)).toBeUndefined();
  expect(h.observe(bad, 7000, true)).toBe("degraded");
  for (let t = 8000; t < 67000; t += 1000)
    expect(h.observe(bad, t, true)).toBeUndefined();
  expect(h.observe(bad, 67000, true)).toBe("degraded");
  for (let t = 68000; t < 200000; t += 1000)
    expect(h.observe(bad, t, true)).toBeUndefined();
  const paused = new RTCPathHealth({
    ...DEFAULT_NETWORK_POLICY,
    quality_warnings: true,
  });
  paused.observe(bad, 0, true);
  expect(paused.observe(bad, 8000, true)).toBeUndefined();
  const noStats = new RTCPathHealth({
    ...DEFAULT_NETWORK_POLICY,
    quality_warnings: true,
  });
  for (let t = 0; t < 10000; t += 1000)
    expect(noStats.observe({}, t, true)).toBeUndefined();
  const held = new RTCPathHealth({
    ...DEFAULT_NETWORK_POLICY,
    quality_warnings: true,
  });
  for (let t = 0; t < 10000; t += 1000)
    expect(held.observe(bad, t, false)).toBeUndefined();
  const disabled = new RTCPathHealth({
    ...DEFAULT_NETWORK_POLICY,
    quality_warnings: false,
  });
  for (let t = 0; t < 10000; t += 1000)
    expect(disabled.observe(bad, t, true)).toBeUndefined();
});
// Frozen 0.12.40 loss-only controller, for repeatable comparisons against the
// shipped policy. The link queues actual 20 ms packet sizes + wire overhead;
// this is a deterministic congestion model, not a physical audio quality score.
function simulate(
  before: boolean,
  capacity: number,
  healthy = false,
  resilient = false,
) {
  let bitrate = resilient ? 20000 : 32000,
    queue = 0,
    maxQueue = 0,
    lost = 0,
    delivered = 0;
  const delays: number[] = [];
  const q = before
    ? new LegacyRTCQualityPolicy()
    : rtcQualityPolicy({
        networkProfile: validateNetworkPolicy({
          ...(resilient
            ? { initial_bitrate_bps: 20000, max_bitrate_bps: 20000 }
            : {}),
        }),
      });
  let intervalLost = 0;
  for (let i = 0; i < 6000; i++) {
    const now = i * 20,
      link = healthy || now < 10000 || now >= 70000 ? 96000 : capacity;
    const bytes = bitrate / 8 / 50 + 64;
    queue = Math.max(0, queue - link / 8 / 50) + bytes;
    const age = ((queue * 8) / link) * 1000;
    maxQueue = Math.max(maxQueue, age);
    if (age > 1600) {
      queue -= bytes;
      lost++;
      intervalLost++;
    } else {
      delivered++;
      delays.push(age);
    }
    if (i % 100 === 0) {
      const loss = intervalLost / 100;
      intervalLost = 0;
      q.observe(
        {
          pair_currentRoundTripTime: 0.04 + (age / 1000) * 2,
          remote_receiver_fractionLost: loss,
          remote_receiver_report_updated: 1,
        },
        now,
      );
      bitrate = q.bitrate;
    }
  }
  delays.sort((a, b) => a - b);
  return {
    lostPackets: lost,
    deliveredPackets: delivered,
    addedDelayP95Ms: delays[Math.floor(delays.length * 0.95)],
    maxQueueMs: maxQueue,
    finalBitrate: bitrate,
  };
}
test("before/after: busy upload, narrow link and healthy voice acceptance", async () => {
  const results = [];
  for (const [name, capacity, healthy] of [
    ["healthy", 96000, true],
    ["busy-upload", 48000, false],
    ["severe-contention", 44000, false],
  ] as const) {
    const before = simulate(true, capacity, healthy),
      after = simulate(false, capacity, healthy);
    const afterResilientPreset = simulate(false, capacity, healthy, true);
    results.push({ name, before, after, afterResilientPreset });
    if (healthy) {
      expect(after.lostPackets).toBe(0);
      expect(after.addedDelayP95Ms).toBe(before.addedDelayP95Ms);
      expect(after.finalBitrate).toBe(32000);
    } else {
      // The standard controller no longer saw-tooths on alternating loss
      // reports, so it may only be better than the frozen release.
      expect(after.lostPackets).toBeLessThanOrEqual(before.lostPackets);
      expect(after.addedDelayP95Ms).toBeLessThanOrEqual(before.addedDelayP95Ms);
      expect(afterResilientPreset.lostPackets).toBeLessThan(before.lostPackets);
      expect(afterResilientPreset.addedDelayP95Ms).toBeLessThan(
        before.addedDelayP95Ms,
      );
    }
  }
  if (process.env.TELEPHONY_NETWORK_RESULTS)
    await writeFile(
      process.env.TELEPHONY_NETWORK_RESULTS,
      JSON.stringify(
        {
          scope:
            "deterministic packet/queue simulation, actual browser policy vs frozen 0.12.40 policy",
          results,
        },
        null,
        2,
      ),
    );
});

test("network playback profile overrides inherited legacy defaults and retains explicit opt-out", () => {
  const q = rtcQualityPolicy({
    playbackTargetMs: 60,
    playbackMaxMs: 160,
    networkProfile: validateNetworkPolicy({
      playback_target_ms: 80,
      playback_max_ms: 120,
    }),
  });
  expect(q.target).toBe(80);
  for (let i = 0; i < 10; i++) q.observe({ receiver_jitter: 0.1 }, i * 2000);
  expect(q.target).toBe(120);
  expect(DEFAULT_NETWORK_POLICY.background_check).toBe(false);
});

// Upload bitrate intentionally differs from the frozen release: it ignores
// cached receiver reports and recovers only after a 10 s clean window.
test("default balanced settings keep released playback target with a steadier, bounded bitrate", () => {
  const before = new LegacyRTCQualityPolicy(),
    after = rtcQualityPolicy();
  let beforeChanges = 0,
    afterChanges = 0,
    beforeRate = before.bitrate,
    afterRate = after.bitrate;
  for (let i = 0; i < 200; i++) {
    const metrics = {
      remote_receiver_fractionLost: i % 9 === 0 ? 0.1 : 0,
      remote_receiver_report_updated: 1,
      pair_availableOutgoingBitrate: i % 8 === 0 ? 48000 : 96000,
      pair_currentRoundTripTime: 0.04 + (i % 20) * 0.04,
    };
    before.observe(metrics, i * 2000);
    after.observe(metrics, i * 2000);
    if (before.bitrate !== beforeRate) beforeChanges++;
    if (after.bitrate !== afterRate) afterChanges++;
    beforeRate = before.bitrate;
    afterRate = after.bitrate;
    expect(after.bitrate).toBeGreaterThanOrEqual(16000);
    expect(after.bitrate).toBeLessThanOrEqual(32000);
    expect(after.target).toBe(before.target);
  }
  expect(afterChanges).toBeLessThan(beforeChanges);
});

test("quality notices can remain local without adding traffic on an impaired link", () => {
  let writes = 0,
    observations = 0;
  const c = new WebRTCAudioConnection({
    onState() {},
    onSessionEvent() {
      observations++;
    },
  });
  (c as any).enqueueRTCEvents = () => writes++;
  c.recordSessionEvent(
    {
      timestamp: new Date().toISOString(),
      action: "network_quality",
      outcome: "sustained_degradation",
    },
    false,
  );
  expect(writes).toBe(0);
  expect(observations).toBe(1);
  c.stop();
});


test("quality warnings include sustained microphone loss, with fresh reports and path reset",()=>{
 const p={...DEFAULT_NETWORK_POLICY,quality_warnings:true};
 const h=new RTCPathHealth(p);
 const bad={path_revision:1,remote_receiver_report_updated:1,remote_receiver_report_age_ms:0,sender_packets_delta:50,remote_receiver_fractionLost:.1};
 for(let t=0;t<6000;t+=1000)expect(h.observe({...bad,remote_receiver_report_updated:t%2000===0?1:0},t,true)).toBeUndefined();
 expect(h.observe(bad,6000,true)).toBe("degraded");
 const stale=new RTCPathHealth(p);
 for(let t=0;t<20000;t+=1000)expect(stale.observe({...bad,remote_receiver_report_updated:t===0?1:0},t,true)).toBeUndefined();
 const moved=new RTCPathHealth(p);
 for(let t=0;t<6000;t+=1000)moved.observe(bad,t,true);
 expect(moved.observe({...bad,path_revision:2},6000,true)).toBeUndefined();
 expect(moved.observe({...bad,path_revision:2,remote_receiver_fractionLost:0},7000,true)).toBeUndefined();
});
