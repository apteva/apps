// App-owned, same-origin microphone capture for the realtime audio bridge.
const TARGET_RATE = 24000;
const FRAME_SAMPLES = 480;
// A single short jitter reserve per start, never a delay per PCM packet.
const PLAYBACK_BUFFER_MS = 120;
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

// Render ordered PCM on the audio thread. One fractional cursor resamples the
// entire stream, including packet boundaries; packet arrival never schedules a
// new AudioBufferSource or inserts a fresh per-packet lead-in.
class ConversationsPCMPlayback extends AudioWorkletProcessor {
  constructor() {
    super();
    this.epoch = 0; this.queue = []; this.cancelled = new Set();
    this.clock = 0; this.startAt = 0; this.running = false;
    this.position = 0; this.lastProgress = 0; this.speaking = false;
    this.fade = 0; this.previous = 0;
    this.port.onmessage = ({data}) => {
      if (data.type === "reset") {
        this.epoch = data.epoch; this.queue = []; this.cancelled.clear();
        this.position = 0; this.running = false; this.startAt = 0; this.fade = 0; this.previous = 0;
        this.speaking = false;
      } else if (data.epoch !== this.epoch) return;
      else if (data.type === "pcm") {
        if (data.item_id && this.cancelled.has(data.item_id)) return;
        const pcm = new Int16Array(data.pcm);
        if (!pcm.length) return;
        this.queue.push({...data, pcm});
        if (!this.running && !this.startAt) this.startAt = this.clock + Math.round(sampleRate * PLAYBACK_BUFFER_MS / 1000);
        this.state();
      } else if (data.type === "clear") this.clear(data.item_id);
    };
  }
  emit(data) { this.port.postMessage({...data, epoch:this.epoch}); }
  state() {
    const speaking = this.queue.some(q => !q.silence);
    if (speaking !== this.speaking) { this.speaking = speaking; this.emit({type:"state",speaking}); }
  }
  progress(entry, position) {
    if (!entry?.item_id) return;
    const end = Math.max(0, Math.min(entry.audio_end_ms, Math.floor(entry.audio_end_ms - (entry.pcm.length - position) / 24 + 0.001)));
    this.emit({type:"progress",item_id:entry.item_id,audio_end_ms:end});
  }
  clear(item) {
    this.progress(this.queue[0], this.position);
    const removed = this.queue.filter(q => !item || q.item_id === item);
    const removedHead = removed.includes(this.queue[0]);
    for (const q of removed) if (q.item_id) this.cancelled.add(q.item_id);
    if (item) this.cancelled.add(item);
    this.queue = this.queue.filter(q => item && q.item_id !== item);
    if (removedHead) { this.position = 0; this.fade = Math.round(sampleRate * 0.003); }
    if (!this.queue.length) { this.running = false; this.startAt = 0; }
    this.state();
    // This is emitted only after the audio thread has removed the sources.
    if (item) this.emit({type:"cleared",item_id:item});
  }
  process(_inputs, outputs) {
    const output = outputs[0]?.[0];
    if (!output) return true;
    const ratio = TARGET_RATE / sampleRate, fadeLength = Math.round(sampleRate * 0.003);
    for (let i=0; i<output.length; i++,this.clock++) {
      if (!this.running && this.queue.length && this.clock >= this.startAt) {
        this.running = true; this.startAt = 0; this.fade = fadeLength;
      }
      let value = 0;
      if (this.running && this.queue.length) {
        const q=this.queue[0], index=Math.floor(this.position), fraction=this.position-index;
        const a=q.pcm[index]/32768;
        const b=(index+1<q.pcm.length?q.pcm[index+1]:this.queue[1]?.pcm[0]??q.pcm[index])/32768;
        value=a+(b-a)*fraction;
        this.position+=ratio;
        if (this.position>=q.pcm.length) {
          this.progress(q,q.pcm.length);
          this.position-=q.pcm.length;this.queue.shift();this.state();
          if (!this.queue.length) {this.running=false;this.startAt=0;this.position=0;this.state();}
        }
      }
      // Smooth only real starts, interruptions and starvation, never ordinary
      // packet boundaries. A short tail also avoids a nonzero-to-zero click.
      if (this.fade>0) {value=this.previous+(value-this.previous)/this.fade;this.fade--;}
      else if (!this.running && !this.queue.length) value=this.previous*0.96;
      if (Math.abs(value)<1e-8) value=0;
      output[i]=value;this.previous=value;
    }
    if (this.clock-this.lastProgress>=sampleRate*0.05) {
      this.lastProgress=this.clock;this.progress(this.queue[0],this.position);
    }
    return true;
  }
}
registerProcessor("conversations-pcm-playback", ConversationsPCMPlayback);
