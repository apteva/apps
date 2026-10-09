// Benchmark-only observer. Direct Worklet ports keep observations independent
// of the UI thread being intentionally blocked. No production media is changed.
const ports = new Map(), samples = [], baselines = new Map();
let timer, previousTick = 0, maxTickGapMS = 0;
const nowMS = () => performance.timeOrigin + performance.now();
self.onmessage = ({data}) => {
  if (data.type === "attach") {
    const {stage, port} = data;
    ports.set(stage, port);
    port.onmessage = ({data: reply}) => {
      const now = nowMS(), rtt = now - reply.nonce;
      if (reply.type !== "clock.reply" || !Number.isFinite(reply.audio_ms) || rtt < 0 || rtt > 50) return;
      const wall = (now + reply.nonce) / 2;
      if (!baselines.has(stage)) baselines.set(stage, {wall, audio: reply.audio_ms});
      const base = baselines.get(stage), wallElapsed = wall - base.wall, audioElapsed = reply.audio_ms - base.audio;
      samples.push({stage, timestamp: new Date(wall).toISOString(), wall_time_ms: wall,
        audio_time_ms: reply.audio_ms, wall_elapsed_ms: wallElapsed, audio_elapsed_ms: audioElapsed,
        lag_ms: wallElapsed - audioElapsed, rtt_ms: rtt, uncertainty_ms: rtt / 2 + 10});
      if (samples.length > 640) samples.shift();
    };
    port.start();
    port.postMessage({type: "clock.probe", nonce: nowMS()});
    if (!timer) timer = setInterval(() => {
      const now = nowMS();
      if (previousTick) maxTickGapMS = Math.max(maxTickGapMS, now - previousTick);
      previousTick = now;
      for (const port of ports.values()) port.postMessage({type: "clock.probe", nonce: now});
    }, 250);
  } else if (data.type === "finish") {
    clearInterval(timer);
    const stages = {};
    for (const sample of samples) {
      const stage = stages[sample.stage] ||= {samples: 0, max_lag_ms: 0, max_rtt_ms: 0};
      stage.samples++;
      stage.max_lag_ms = Math.max(stage.max_lag_ms, sample.lag_ms);
      stage.max_rtt_ms = Math.max(stage.max_rtt_ms, sample.rtt_ms);
    }
    postMessage({stages, max_observer_tick_gap_ms: maxTickGapMS, samples});
  }
};
