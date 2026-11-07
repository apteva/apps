import { WebRTCAudioConnection, rtcStatistics } from "../src/webrtc-audio";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS } from "../../ui/softphone-audio";
(window as any).runNetworkFault = async (url: string, enabled: boolean) => {
  const events: any[] = [];
  const audio = new WebRTCAudioConnection({
    onState() {},
    onSessionEvent: (e) => events.push(e),
  });
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
  const until = async (test: () => Promise<boolean>, ms = 10000) => {
    const end = performance.now() + ms;
    while (!(await test())) {
      if (performance.now() > end) throw Error("RTP progress timed out");
      await sleep(50);
    }
  };
  const count = (report: RTCStatsReport) => {
    let received = 0,
      lost = 0,
      concealed = 0;
    report.forEach((s: any) => {
      if (s.type === "inbound-rtp") {
        received += s.packetsReceived ?? 0;
        lost += s.packetsLost ?? 0;
        concealed += s.concealedSamples ?? 0;
      }
    });
    return { received, lost, concealed };
  };
  try {
    await audio.start(
      url,
      {
        ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,
        mediaTransport: "webrtc",
        ...(enabled ? { iceTransportPolicy: "relay" as const } : {}),
      },
      "/worklet.js",
    );
    const peer: RTCPeerConnection = (audio as any).pc;
    const sender = peer.getSenders()[0];
    await until(async () => count(await peer.getStats()).received > 50);
    if (
      (rtcStatistics(await peer.getStats()).webrtc.candidateType ===
        "relay") !==
      enabled
    )
      throw Error("Fixture did not use the selected path");
    const before = count(await peer.getStats());
    await fetch("/arm", { method: "POST" });
    const armed = performance.now();
    await sleep(12000);
    const healing = events.find(
      (e) => e.action === "ice_restart" && e.outcome === "restart_completed",
    );
    const report = await peer.getStats(),
      after = count(report),
      path = rtcStatistics(report).webrtc;
    const tailBefore = count(report);
    await sleep(3000);
    const tail = count(await peer.getStats());
    if (peer !== (audio as any).pc || sender !== peer.getSenders()[0])
      throw Error(
        JSON.stringify({
          error: "Quality handling recreated audio peer or microphone",
          events,
          peer: peer.connectionState,
          current: (audio as any).pc?.connectionState,
          path,
          tail,
          tailBefore,
        }),
      );
    if (
      enabled &&
      (healing ||
        path.candidateType !== "relay" ||
        tail.lost - tailBefore.lost > 1 ||
        tail.received - tailBefore.received < 100)
    )
      throw Error(JSON.stringify({ healing, path, events, tailBefore, tail }));
    if (
      !enabled &&
      (healing ||
        path.candidateType === "relay" ||
        tail.lost - tailBefore.lost < 30)
    )
      throw Error("Baseline failed to retain the impaired direct route");
    return {
      enabled,
      lost_packets: tail.lost - before.lost,
      concealed_ms: (tail.concealed - before.concealed) / 48,
      tail_lost_packets: tail.lost - tailBefore.lost,
      path,
      peer_preserved: true,
      microphone_preserved: true,
      events: events.filter(
        (e) => e.action === "ice_restart" || e.action === "network_quality",
      ),
      observation_ms: performance.now() - armed,
    };
  } finally {
    audio.stop();
  }
};
