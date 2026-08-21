const FRAME_SAMPLES = 480;

class SoftphoneCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = new Float32Array(FRAME_SAMPLES);
    this.filled = 0;
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0];
    if (!channel) return true;
    for (let i = 0; i < channel.length; i += 1) {
      this.buffer[this.filled++] = channel[i];
      if (this.filled === this.buffer.length) {
        this.port.postMessage(this.buffer.slice(0));
        this.filled = 0;
      }
    }
    return true;
  }
}

class SoftphonePlaybackProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions || {};
    this.initialTargetMs = config.initialTargetMs || 60;
    this.minTargetMs = config.minTargetMs || 40;
    this.maxTargetMs = config.maxTargetMs || 140;
    this.hardMaxMs = config.hardMaxMs || 250;
    this.targetMs = this.initialTargetMs;
    this.queue = [];
    this.queued = 0;
    this.offset = 0;
    this.playing = false;
    this.underruns = 0;
    this.droppedSamples = 0;
    this.maxQueuedSamples = 0;
    this.processedSinceStats = 0;
    this.stableSamples = 0;
    this.port.onmessage = (event) => {
      if (event.data === "flush") {
        this.queue = [];
        this.queued = 0;
        this.offset = 0;
        this.playing = false;
        return;
      }
      const chunk = event.data;
      if (!chunk || typeof chunk.length !== "number") return;
      this.queue.push(chunk);
      this.queued += chunk.length;
      this.maxQueuedSamples = Math.max(this.maxQueuedSamples, this.queued);

      // Keep a slow consumer at the live edge instead of allowing latency to
      // ratchet upward forever. Retain one target plus a small safety margin.
      const softMax = this.msToSamples(this.targetMs + 60);
      if (this.playing && this.queued > softMax) {
        this.dropOldest(this.queued - this.msToSamples(this.targetMs + 20));
      }
      const hardMax = this.msToSamples(this.hardMaxMs);
      if (this.queued > hardMax) this.dropOldest(this.queued - hardMax);
    };
  }

  msToSamples(ms) {
    return Math.round(sampleRate * ms / 1000);
  }

  dropOldest(samples) {
    let remaining = Math.max(0, samples);
    while (remaining > 0 && this.queue.length > 0) {
      const chunk = this.queue[0];
      const available = chunk.length - this.offset;
      const take = Math.min(available, remaining);
      this.offset += take;
      this.queued -= take;
      this.droppedSamples += take;
      remaining -= take;
      if (this.offset >= chunk.length) {
        this.queue.shift();
        this.offset = 0;
      }
    }
  }

  reportStats() {
    this.port.postMessage({
      type: "stats",
      queue_ms: Math.round(this.queued * 1000 / sampleRate),
      target_ms: this.targetMs,
      underruns: this.underruns,
      dropped_ms: Math.round(this.droppedSamples * 1000 / sampleRate),
      max_queue_ms: Math.round(this.maxQueuedSamples * 1000 / sampleRate),
    });
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    if (!out) return true;
    out.fill(0);

    if (!this.playing && this.queued >= this.msToSamples(this.targetMs)) {
      this.playing = true;
    }

    let written = 0;
    if (this.playing) {
      while (written < out.length && this.queue.length > 0) {
        const chunk = this.queue[0];
        const take = Math.min(out.length - written, chunk.length - this.offset);
        out.set(chunk.subarray(this.offset, this.offset + take), written);
        written += take;
        this.offset += take;
        this.queued -= take;
        if (this.offset >= chunk.length) {
          this.queue.shift();
          this.offset = 0;
        }
      }
      if (written < out.length) {
        this.underruns += 1;
        this.targetMs = Math.min(this.maxTargetMs, this.targetMs + 20);
        this.playing = false;
        this.stableSamples = 0;
      } else {
        this.stableSamples += out.length;
        // After 30 stable seconds, cautiously reduce latency toward the floor.
        if (this.stableSamples >= sampleRate * 30 && this.targetMs > this.minTargetMs) {
          this.targetMs = Math.max(this.minTargetMs, this.targetMs - 10);
          this.stableSamples = 0;
        }
      }
    }

    this.processedSinceStats += out.length;
    if (this.processedSinceStats >= sampleRate) {
      this.processedSinceStats = 0;
      this.reportStats();
    }
    return true;
  }
}

registerProcessor("softphone-capture", SoftphoneCaptureProcessor);
registerProcessor("softphone-playback", SoftphonePlaybackProcessor);
