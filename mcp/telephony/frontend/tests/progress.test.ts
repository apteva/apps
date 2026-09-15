import { describe, expect, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension, type CallSession } from "../src/client";
import { phaseForStatus } from "../src/softphone";
import type { AudioRuntime } from "../src/audio";
import type { SoftphoneCallbacks } from "../../ui/softphone-audio";

function fixture(options: { ringback?: boolean | { country?: string }; calls?: unknown[] } = {}) {
  const session: CallSession = { call_id: "call-1", media_url: "/api/apps/telephony/_install/42/softphone/media/call-1/secret", session_token: "secret" };
  const { calls = [{ id: "call-1", status: "ringing" }], ...softphoneOptions } = options;
  const sdk = new AptevaClient({ baseURL: "https://gateway.example", accessToken: "token", fetch: (async (url) => {
    const parsed = new URL(String(url));
    if (parsed.pathname.endsWith("/calls")) return Response.json({ calls });
    if (parsed.pathname.endsWith("/hangup")) return Response.json({ ok: true });
    return Response.json(session);
  }) as typeof fetch });
  const client = sdk.use(telephonyExtension, { projectId: "p1", installId: 42 });
  let callbacks: SoftphoneCallbacks = {};
  const ringback: string[] = [];
  const runtime: AudioRuntime = {
    async preflight() {},
    create(cb) {
      callbacks = cb;
      return {
        async start() { cb.onState?.("live"); },
        stop() {}, setMuted() {}, sendDTMF() {}, setOutputVolume() {},
        startRingback(country) { ringback.push(`start:${country ?? "default"}`); },
        stopRingback() { ringback.push("stop"); },
      };
    },
  };
  const phone = client.createSoftphone({ audioRuntime: runtime, pollIntervalMs: 0, ...softphoneOptions });
  return { phone, ringback, get callbacks() { return callbacks; } };
}

describe("softphone call progress", () => {
  test("phase mapping covers every carrier status", () => {
    expect(phaseForStatus(undefined)).toBe("placing");
    expect(phaseForStatus("initiated")).toBe("placing");
    expect(phaseForStatus("ringing")).toBe("ringing");
    expect(phaseForStatus("answered")).toBe("connected");
    expect(phaseForStatus("in-progress")).toBe("connected");
    for (const terminal of ["completed", "busy", "no-answer", "failed", "canceled"]) expect(phaseForStatus(terminal)).toBe("ended");
  });

  test("dial walks placing → ringing → connected → ended and keeps termination facts", async () => {
    const f = fixture();
    expect(f.phone.getSnapshot().phase).toBe("idle");
    await f.phone.dial({ to: "+12025550100", from: "+12025550101" });
    expect(f.phone.getSnapshot().phase).toBe("placing");
    f.phone.observeCall({ id: "call-1", status: "ringing" });
    expect(f.phone.getSnapshot().phase).toBe("ringing");
    f.phone.observeCall({ id: "call-1", status: "answered", answered_by: "human" });
    expect(f.phone.getSnapshot().phase).toBe("connected");
    expect(f.phone.getSnapshot().answeredBy).toBe("human");
    f.callbacks.onCallStatus?.({ call_id: "call-1", status: "completed", ended_at: "2026-09-15T10:00:10Z", termination: { reason: "completed", cause: "normal_clearing" } });
    const ended = f.phone.getSnapshot();
    expect(ended.phase).toBe("ended");
    expect(ended.callId).toBeUndefined();
    expect(ended.busy).toBe(false);
    expect(ended.detail).toBeUndefined();
    expect(ended.termination?.reason).toBe("completed");
    expect(ended.answeredBy).toBe("human");
    expect(ended.endedAt).toBe("2026-09-15T10:00:10Z");
    // The next dial clears the previous outcome.
    await f.phone.dial({ to: "+12025550100", from: "+12025550101" });
    expect(f.phone.getSnapshot().termination).toBeUndefined();
    expect(f.phone.getSnapshot().phase).toBe("placing");
  });

  test("pushed call.status frames drive the phase without polling", async () => {
    const f = fixture();
    await f.phone.dial({ to: "+12025550100", from: "+12025550101" });
    f.callbacks.onCallStatus?.({ call_id: "call-1", status: "ringing" });
    expect(f.phone.getSnapshot().phase).toBe("ringing");
    f.callbacks.onCallStatus?.({ call_id: "other", status: "completed" });
    expect(f.phone.getSnapshot().phase).toBe("ringing");
    f.callbacks.onCallStatus?.({ call_id: "call-1", status: "busy", termination: { reason: "busy" } });
    expect(f.phone.getSnapshot().phase).toBe("ended");
    expect(f.phone.getSnapshot().termination?.reason).toBe("busy");
  });

  test("ringback is opt-in, follows the ringing phase, and carries the country", async () => {
    const silent = fixture();
    await silent.phone.dial({ to: "+12025550100", from: "+12025550101" });
    silent.phone.observeCall({ id: "call-1", status: "ringing" });
    silent.phone.observeCall({ id: "call-1", status: "answered" });
    expect(silent.ringback).toEqual([]);

    const f = fixture({ ringback: { country: "GB" } });
    await f.phone.dial({ to: "+12025550100", from: "+12025550101" });
    expect(f.ringback).toEqual([]);
    f.phone.observeCall({ id: "call-1", status: "ringing" });
    expect(f.ringback).toEqual(["start:GB"]);
    f.phone.observeCall({ id: "call-1", status: "ringing" });
    expect(f.ringback).toEqual(["start:GB"]);
    f.phone.observeCall({ id: "call-1", status: "answered" });
    expect(f.ringback).toEqual(["start:GB", "stop"]);

    const plain = fixture({ ringback: true });
    await plain.phone.dial({ to: "+12025550100", from: "+12025550101" });
    plain.phone.observeCall({ id: "call-1", status: "ringing" });
    expect(plain.ringback).toEqual(["start:default"]);
    await plain.phone.hangup();
    expect(plain.ringback).toEqual(["start:default", "stop"]);
  });

  test("attached outbound calls ring from the first read, inbound ones never do", async () => {
    const outbound = fixture({ ringback: true, calls: [{ id: "call-1", status: "ringing", direction: "outbound", peer_kind: "human", from_number: "+1", to_number: "+2" }] });
    await outbound.phone.attach("call-1");
    expect(outbound.phone.getSnapshot().phase).toBe("ringing");
    expect(outbound.ringback).toEqual(["start:default"]);
    outbound.callbacks.onCallStatus?.({ call_id: "call-1", status: "answered" });
    expect(outbound.ringback).toEqual(["start:default", "stop"]);

    const inbound = fixture({ ringback: true, calls: [{ id: "call-1", status: "ringing", direction: "inbound", peer_kind: "human", from_number: "+1", to_number: "+2" }] });
    await inbound.phone.takeover("call-1");
    expect(inbound.phone.getSnapshot().phase).toBe("ringing");
    expect(inbound.ringback).toEqual([]);

    // A pushed frame can also settle the direction when the read returned nothing.
    const late = fixture({ ringback: true, calls: [] });
    await late.phone.attach("call-1");
    expect(late.phone.getSnapshot().phase).toBe("placing");
    late.callbacks.onCallStatus?.({ call_id: "call-1", status: "ringing", direction: "outbound" });
    expect(late.ringback).toEqual(["start:default"]);
  });

  test("answered inbound calls never play ringback", async () => {
    const f = fixture({ ringback: true });
    await f.phone.answer("call-1");
    f.phone.observeCall({ id: "call-1", status: "ringing" });
    expect(f.ringback).toEqual([]);
    expect(f.phone.getSnapshot().phase).toBe("ringing");
  });
});
