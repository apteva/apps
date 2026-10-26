import { describe, expect, test } from "bun:test";
import {
  diagnosticsModel,
  mergeDiagnostics,
  formatMetric,
  diagnosticsURL,
} from "./call-diagnostics";
import {
  createCallInspector,
  type InspectorState,
} from "./call-diagnostics-controller";
const epoch = Date.parse("2026-10-25T12:00:00Z"),
  date = (n = 0) => new Date(epoch + n).toISOString();
describe("call inspector evidence", () => {
  test("missing evidence is unknown, not healthy zero", () => {
    const m = diagnosticsModel(
      { call: { active: true }, browser: {} },
      epoch,
      epoch,
    );
    expect(m.state).toBe("No telemetry");
    expect(m.hasBrowser).toBe(false);
    expect(formatMetric(m.browserMetrics[0].value)).toBe("—");
    expect(formatMetric(0, "ms")).toBe("0 ms");
  });
  test("browser units, SIP actual field names and independent freshness", () => {
    const m = diagnosticsModel(
      {
        generated_at: date(),
        call: { active: true },
        browser: {
          received_at: date(-20000),
          server: { webrtc: { rtp_ingress: { max_queue_wait_ms: 42 } } },
        },
        transport_samples: [
          { timestamp: date(-20000), metrics: { receiver_jitter: 0.025 } },
        ],
        carrier_live: {
          at: date(),
          sip: {
            rtp: {
              max_gap_ms: 81,
              socket_receive_drops: 0,
              socket_receive_drops_available: false,
            },
            jitter_buffer: { queue_ms: 60 },
          },
        },
      },
      epoch,
      epoch,
    );
    expect(m.browserMetrics[1].value).toBe(25);
    expect(m.browserMetrics[10].value).toBe(42);
    expect(m.sipMetrics[6].value).toBe(81);
    expect(m.sipMetrics[8].value).toBeUndefined();
    expect(m.browserFreshness).toBe("Observation stale");
    expect(m.state).toBe("Live observations");
  });
  test("AI does not fabricate browser leg; clear receipts stay unconfirmed", () => {
    const m = diagnosticsModel(
      {
        call: { active: false, peer_kind: "realtime" },
        carrier: {
          updated_at: date(),
          agent_audio: {
            clears: [
              {
                requested_at_ms: epoch,
                scope: "item",
                remote_command_sent: true,
                local_discarded_ms: 100,
              },
            ],
          },
        },
      },
      epoch,
      epoch,
    );
    expect(m.hasAI).toBe(true);
    expect(m.hasBrowser).toBe(false);
    expect(m.state).toBe("Recorded");
    expect(m.events[0].label).toBe("Playback cleared");
    expect(m.events[0].detail).toContain("unconfirmed");
  });
  test("history is deduplicated, bounded, resettable; does not add reset counters", () => {
    const records = Array.from({ length: 300 }, (_, i) => ({
      id: String(i),
      at: date(i * 1000),
      sip: { rtp: { sequence_loss: i === 299 ? 0 : i } },
    }));
    const merged = mergeDiagnostics(
      { carrier_transport_history: records.slice(0, 100) },
      { carrier_history: { records } },
    );
    expect((merged.carrier_transport_history as unknown[]).length).toBe(256);
    const m = diagnosticsModel(merged, epoch, epoch);
    expect(m.sipMetrics[2].value).toBe(0);
    expect(
      mergeDiagnostics(merged, {
        carrier_history: { reset_required: true, records: [] },
      }).carrier_transport_history,
    ).toEqual([]);
  });
  test("timeline includes browser actions and deduplicates SIP snapshots", () => {
    const event = {
      occurred_at: date(),
      reason: "decoded_partial_age",
      duration_ms: 15.75,
    };
    const m = diagnosticsModel(
      {
        browser: {
          session_events: [
            { timestamp: date(), action: "ice_restart", outcome: "failed" },
          ],
        },
        carrier_transport_history: [
          { at: date(), sip: { jitter_buffer: { events: [event] } } },
          { at: date(1), sip: { jitter_buffer: { events: [event] } } },
        ],
      },
      epoch,
      epoch,
    );
    expect(m.events.length).toBe(2);
    expect(m.events.some((e) => e.label === "ice restart")).toBe(true);
  });
  test("WebSocket carriers retain readable counters without inventing SIP", () => {
    const m = diagnosticsModel({call:{active:false,peer_kind:"realtime"}, carrier:{provider:"twilio",max_queued_ms:180,dropped_stale_ms:20},carrier_live:{at:date(),carrier:{media_frames:500,decode_errors:0,max_gap_across_generations_ms:85}}},epoch,epoch);
    expect(m.hasSIP).toBe(false);expect(m.hasCarrier).toBe(true);
    expect(m.state).toBe("Recorded");expect(m.carrierMetrics[0].value).toBe(500);
    expect(m.carrierMetrics[1].value).toBe(0);expect(m.carrierMetrics[2].value).toBe(85);
  });
  test("reported stage state keeps its own observation timestamp", () => {
    const m = diagnosticsModel({call:{active:true}, browser:{received_at:date(-30000),server:{audio_health:{stages:{browser_to_telephony:{state:"audio_degraded",signal:"capture_drop"}}}}},generated_at:date()},epoch,epoch);
    expect(m.state).toBe("Telemetry stale");expect(m.stages[0]).toEqual({label:"Browser → Telephony",state:"audio degraded",detail:"capture_drop",observed:date(-30000)});
  });
  test("URL preserves scope and encodes call/cursor", () => {
    const url = new URL(
      diagnosticsURL(
        { projectId: "p&a", installId: 42 },
        "call&1",
        "incremental",
        "x+y",
      ),
      "http://test",
    );
    expect(url.searchParams.get("call_id")).toBe("call&1");
    expect(url.searchParams.get("project_id")).toBe("p&a");
    expect(url.searchParams.get("history_cursor")).toBe("x+y");
  });
});
function harness() {
  let now = epoch,
    hidden = false,
    sequence = 0;
  const timers = new Map<number, { at: number; fn: () => void }>();
  const states: InspectorState[] = [],
    requests: {
      url: string;
      signal: AbortSignal;
      resolve: (r: Response) => void;
      reject: (e: Error) => void;
    }[] = [];
  const c = createCallInspector(
    { projectId: "p", installId: 1 },
    "a",
    (s) => states.push(s),
    {
      now: () => now,
      hidden: () => hidden,
      schedule: (fn, ms) => {
        const id = ++sequence;
        timers.set(id, { at: now + ms, fn });
        return id as unknown as ReturnType<typeof setTimeout>;
      },
      cancel: (id) => {
        timers.delete(id as unknown as number);
      },
      fetch: (url, init) =>
        new Promise((resolve, reject) => {
          const signal = init.signal!;
          requests.push({ url, signal, resolve, reject });
          signal.addEventListener("abort", () => reject(new Error("abort")));
        }),
    },
  );
  const flush = async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve();
  };
  const reply = async (data: unknown, status = 200) => {
    requests.at(-1)!.resolve(Response.json(data, { status }));
    await flush();
  };
  const advance = async (ms: number) => {
    now += ms;
    for (const [id, t] of [...timers])
      if (t.at <= now) {
        timers.delete(id);
        t.fn();
      }
    await flush();
  };
  return {
    c,
    requests,
    states,
    timers,
    reply,
    advance,
    flush,
    hide: (v: boolean) => {
      hidden = v;
      c.visibility();
    },
  };
}
describe("call inspector request lifecycle", () => {
  test("fast snapshot then bounded history; slow requests never overlap", async () => {
    const h = harness();
    h.c.start();
    h.c.refresh();
    expect(h.requests.length).toBe(1);
    expect(h.requests[0].url).toContain("history=none");
    await h.reply({
      call: { call_id: "a", active: true },
      browser: { rtt_ms: 30 },
    });
    await h.advance(50);
    expect(h.requests[1].url).toContain("history=incremental");
    await h.advance(3000);
    expect(h.requests.length).toBe(2);
    await h.reply({
      call: { active: true },
      carrier_history: { records: [], next_cursor: "next" },
    });
    await h.advance(3000);
    expect(h.requests[2].url).toContain("history=none");
    h.c.dispose();
  });
  test("finished calls get history then stop, manual refresh remains", async () => {
    const h = harness();
    h.c.start();
    const call = { call_id: "a", active: false, ended_at: date(-60000) };
    await h.reply({ call });
    await h.advance(50);
    await h.reply({ call, carrier_history: { records: [] } });
    await h.advance(60000);
    expect(h.requests.length).toBe(2);
    expect(h.timers.size).toBe(0);
    h.c.refresh();
    expect(h.requests.length).toBe(3);
    h.c.dispose();
  });
  test("recent hangup continues short settling window", async () => {
    const h = harness();
    h.c.start();
    const call = { active: false, ended_at: date() };
    await h.reply({ call });
    await h.advance(50);
    await h.reply({ call, carrier_history: { records: [] } });
    await h.advance(3000);
    expect(h.requests.length).toBe(3);
    h.c.dispose();
  });
  test("permission loss clears evidence and stops automatic retry", async () => {
    const h = harness();
    h.c.start();
    await h.reply({ call: { active: true }, browser: { rtt_ms: 32 } });
    await h.advance(50);
    await h.reply({}, 403);
    expect(h.states.at(-1)?.data).toBeNull();
    expect(h.states.at(-1)?.error).toContain("Operator access");
    await h.advance(60000);
    expect(h.requests.length).toBe(2);
    h.c.dispose();
  });
  test("hidden tab pauses; dispose aborts and ignores late responses", async () => {
    const h = harness();
    h.c.start();
    await h.reply({ call: { active: true } });
    h.hide(true);
    await h.advance(60000);
    expect(h.requests.length).toBe(1);
    h.hide(false);
    expect(h.requests.length).toBe(2);
    h.c.dispose();
    const count = h.states.length;
    await h.reply({ call: { active: true }, browser: { rtt_ms: 999 } });
    expect(h.states.length).toBe(count);
    expect(h.requests[1].signal.aborted).toBe(true);
  });
  test("timeout reports stale state, backs off, cleans up timers", async () => {
    const h = harness();
    h.c.start();
    await h.advance(12000);
    expect(h.states.at(-1)?.error).toContain("timed out");
    await h.advance(3000);
    expect(h.requests.length).toBe(1);
    await h.advance(3000);
    expect(h.requests.length).toBe(2);
    h.c.dispose();
    await h.flush();
    expect(h.timers.size).toBe(0);
  });
  test("history pages retain cursor and reset replaced history", async () => {
    const h = harness();
    h.c.start();
    await h.reply({ call: { active: true } });
    await h.advance(50);
    await h.reply({
      carrier_history: {
        records: [{ id: "one", at: date() }],
        next_cursor: "two",
        has_more: true,
      },
    });
    await h.advance(50);
    expect(h.requests[2].url).toContain("history_cursor=two");
    await h.reply({
      carrier_history: {
        records: [{ id: "three", at: date(1) }],
        reset_required: true,
      },
    });
    expect(
      (h.states.at(-1)?.data?.carrier_transport_history as any[]).map(
        (r) => r.id,
      ),
    ).toEqual(["three"]);
    h.c.dispose();
  });
});

test("pickup timeline separates receipts from explicit display and playback evidence",()=>{
 const offer={offer_id:"offer-a",offer_created_at:date(),receipt_acknowledged_at:date(10),status:"pending"};
 const receipt=diagnosticsModel({offer_delivery:{offers:[offer],events:[]}},epoch,epoch);
 expect(receipt.events.some(e=>e.label==="receipt acknowledged"&&e.detail.includes("Receipt only"))).toBe(true);
 expect(receipt.events.some(e=>e.label.includes("ringtone")||e.label.includes("displayed"))).toBe(false);
 const explicit=diagnosticsModel({offer_delivery:{offers:[offer],events:[{id:1,offer_id:"offer-a",stage:"ui_displayed",received_at:date(20),browser_tab_id:"tab-a"},{id:2,offer_id:"offer-a",stage:"ringtone_started",received_at:date(30),browser_tab_id:"tab-a"}]}},epoch,epoch);
 expect(explicit.events.filter(e=>e.stage==="Pickup")).toHaveLength(3);
 expect(explicit.events.some(e=>e.label==="ringtone started"&&e.detail.includes("Browser report"))).toBe(true);
});
