// App-owned, same-origin microphone capture for the realtime audio bridge.
const TARGET_RATE = 24000;
const FRAME_SAMPLES = 480;
class ConversationsPCMCapture extends AudioWorkletProcessor {
  constructor() { super(); this.pending = []; this.cursor = 0; }
  process(inputs, outputs) {
    const output = outputs[0] && outputs[0][0];
    if (output) output.fill(0);
    const input = inputs[0] && inputs[0][0];
    if (!input || !input.length) return true;
    const ratio = sampleRate / TARGET_RATE;
    let cursor = this.cursor;
    while (cursor < input.length) {
      const sample = Math.max(-1, Math.min(1, input[Math.min(input.length - 1, Math.floor(cursor))]));
      this.pending.push(sample < 0 ? sample * 32768 : sample * 32767);
      cursor += ratio;
      if (this.pending.length >= FRAME_SAMPLES) {
        const pcm = new Int16Array(this.pending.splice(0, FRAME_SAMPLES));
        this.port.postMessage(pcm.buffer, [pcm.buffer]);
      }
    }
    this.cursor = cursor - input.length;
    return true;
  }
}
registerProcessor("conversations-pcm-capture", ConversationsPCMCapture);
