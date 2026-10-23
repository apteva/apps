// App-owned, same-origin microphone capture for the realtime audio bridge.
const TARGET_RATE = 24000;
const FRAME_SAMPLES = 480;
class ConversationsPCMCapture extends AudioWorkletProcessor {
  constructor() {
    super(); this.pending = []; this.cursor = 0;
    this.speaking = false; this.attack = 0; this.quiet = 0; this.noise = 0.003;
    this.port.onmessage = event => { if (event.data?.type === "reset-speech") { this.speaking = false; this.attack = 0; this.quiet = 0; } };
  }
  detectSpeech(input) {
    let energy = 0;
    for (const sample of input) energy += sample * sample;
    const rms = Math.sqrt(energy / input.length);
    const ms = input.length * 1000 / sampleRate;
    const threshold = Math.max(0.015, this.noise * 3);
    if (!this.speaking && rms < threshold) this.noise = Math.min(0.025, this.noise * 0.98 + rms * 0.02);
    if (rms >= threshold) {
      this.quiet = 0; this.attack += ms;
      if (!this.speaking && this.attack >= 60) { this.speaking = true; this.port.postMessage({ type: "input.speech_started" }); }
    } else {
      this.attack = 0; this.quiet += ms;
      if (this.speaking && this.quiet >= 400) { this.speaking = false; this.port.postMessage({ type: "input.speech_stopped" }); }
    }
  }
  process(inputs, outputs) {
    const output = outputs[0] && outputs[0][0];
    if (output) output.fill(0);
    const input = inputs[0] && inputs[0][0];
    if (!input || !input.length) return true;
    this.detectSpeech(input);
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
