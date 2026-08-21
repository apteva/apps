const FRAME_SAMPLES = 480;
const JITTER_MAX_SAMPLES = 12000;

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
  constructor() {
    super();
    this.queue = [];
    this.queued = 0;
    this.offset = 0;
    this.port.onmessage = (event) => {
      if (event.data === "flush") {
        this.queue = [];
        this.queued = 0;
        this.offset = 0;
        return;
      }
      const chunk = event.data;
      this.queue.push(chunk);
      this.queued += chunk.length;
      while (this.queued > JITTER_MAX_SAMPLES && this.queue.length > 1) {
        const dropped = this.queue.shift();
        this.queued -= dropped.length - this.offset;
        this.offset = 0;
      }
    };
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    if (!out) return true;
    let written = 0;
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
    if (written < out.length) out.fill(0, written);
    return true;
  }
}

registerProcessor("softphone-capture", SoftphoneCaptureProcessor);
registerProcessor("softphone-playback", SoftphonePlaybackProcessor);
