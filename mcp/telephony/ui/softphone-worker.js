// Owns the media WebSocket off the React/main thread. AudioWorklet ports feed
// and consume frames directly so panel rendering cannot stall live speech.
const MAGIC = 0x31545041; // "APT1" in little endian.
const HEADER_BYTES = 16;
const SAMPLE_RATE = 24_000;
const MAX_RECONNECT_MS = 30_000;
const MAX_RECONNECT_DELAY_MS = 4_000;
const MAX_CAPTURE_BUFFERED_BYTES = SAMPLE_RATE * 2 * 0.12;

let socket = null;
let mediaURL = "";
let capturePort = null;
let playbackPort = null;
let closed = false;
let microphoneReady = false;
let muted = false;
let stableTimer = null;
let reconnectStartedAt = 0;
let reconnectAttempt = 0;
let playbackSequence = 0;
let contextRate = SAMPLE_RATE;
let reconnectTimer = null;

function pcm16(floatFrame) {
  const out = new Int16Array(floatFrame.length);
  for (let i = 0; i < floatFrame.length; i += 1) {
    const value = Math.max(-1, Math.min(1, floatFrame[i]));
    out[i] = value < 0 ? value * 0x8000 : value * 0x7fff;
  }
  return out;
}

// Streaming, windowed-sinc low-pass conversion. History and fractional phase
// carry across frames; the fixed delay supplies future taps without edge resets.
const resamplers = new Map();
function resample(frame, from, to) {
  if (from === to) return frame;
  const key = `${from}:${to}`;
  let state = resamplers.get(key);
  if (!state) { state = {history:new Float32Array(64), phase:0}; resamplers.set(key,state); }
  const input = new Float32Array(state.history.length + frame.length);
  input.set(state.history); input.set(frame, state.history.length);
  const ratio = from / to, cutoff = Math.min(1,to/from) * 0.90, taps = 64;
  const values = [];
  let pos = state.phase;
  for (; pos < frame.length; pos += ratio) {
    const center = pos + 32;
    let value=0, weight=0;
    for (let j=Math.ceil(center-32); j<=Math.floor(center+32); j++) {
      const x=j-center;
      const sinc = Math.abs(x)<1e-9 ? cutoff : Math.sin(Math.PI*cutoff*x)/(Math.PI*x);
      const w=sinc*(0.5+0.5*Math.cos(Math.PI*x/32));
      if (j>=0 && j<input.length) { value+=input[j]*w; weight+=w; }
    }
    values.push(weight ? value/weight : 0);
  }
  state.phase = pos-frame.length;
  state.history = input.slice(-taps);
  return new Float32Array(values);
}

function framedCapture(frame, sequence, timestampMS, sourceRate) {
  const pcm = pcm16(resample(frame, sourceRate || SAMPLE_RATE, SAMPLE_RATE));
  const out = new ArrayBuffer(HEADER_BYTES + pcm.byteLength);
  const view = new DataView(out);
  view.setUint32(0, MAGIC, true);
  view.setUint32(4, sequence >>> 0, true);
  view.setFloat64(8, timestampMS, true);
  new Uint8Array(out, HEADER_BYTES).set(new Uint8Array(pcm.buffer));
  return out;
}

function decodePlayback(buffer) {
  const input = new Int16Array(buffer);
  const out = new Float32Array(input.length);
  for (let i = 0; i < input.length; i += 1) out[i] = input[i] / 0x8000;
  return out;
}

function capture(message) {
  if (message?.type !== "capture" || muted || !microphoneReady || !socket || socket.readyState !== WebSocket.OPEN) return;
  if (socket.bufferedAmount > MAX_CAPTURE_BUFFERED_BYTES) {
    postMessage({
      type: "transport.drop",
      event: {
        timestamp: new Date().toISOString(), direction: "operator_to_carrier", reason: "websocket_backpressure",
        duration_ms: Math.round(message.frame.length * 1000 / (message.sample_rate || SAMPLE_RATE)),
        queue_before_ms: Math.round(socket.bufferedAmount * 1000 / (SAMPLE_RATE * 2)),
        queue_after_ms: Math.round(socket.bufferedAmount * 1000 / (SAMPLE_RATE * 2)), sequence: message.sequence,
      },
    });
    return;
  }
  socket.send(framedCapture(message.frame, message.sequence, message.timestamp_ms, message.sample_rate));
}

function connect() {
  if (closed || !mediaURL) return;
  const ws = new WebSocket(mediaURL);
  ws.binaryType = "arraybuffer";
  socket = ws;
  ws.onopen = () => {
    if (socket !== ws || closed) { ws.close(); return; }
    // A handshake alone is not recovery. Require a stable socket before
    // resetting the retry budget, otherwise open/close loops run forever.
    stableTimer = setTimeout(() => { if (socket === ws) { if (microphoneReady) {reconnectStartedAt=0; reconnectAttempt=0;} else {ws.close();} } }, 10000);
    postMessage({ type: "socket.open" });
  };
  ws.onmessage = (event) => {
    if (socket !== ws) return;
    if (typeof event.data === "string") {
      postMessage({ type: "socket.message", data: event.data });
      return;
    }
    const frame = resample(decodePlayback(event.data), SAMPLE_RATE, contextRate);
    const sequence = playbackSequence++;
    playbackPort?.postMessage({ type: "playback", frame, sequence, timestamp_ms: performance.now() }, [frame.buffer]);
  };
  ws.onerror = () => postMessage({ type: "socket.error" });
  ws.onclose = () => {
    if (socket !== ws) return;
    if (stableTimer !== null) clearTimeout(stableTimer);
    stableTimer = null;
    socket = null;
    microphoneReady = false;
    playbackPort?.postMessage({ type: "flush" });
    postMessage({ type: "socket.close" });
    scheduleReconnect();
  };
}

function scheduleReconnect() {
  if (closed || reconnectTimer !== null) return;
  const now = Date.now();
  if (reconnectStartedAt === 0) reconnectStartedAt = now;
  if (now - reconnectStartedAt >= MAX_RECONNECT_MS) {
    postMessage({ type: "socket.failed", detail: "audio connection lost after repeated retries" });
    return;
  }
  const delay = Math.min(250 * (2 ** reconnectAttempt), MAX_RECONNECT_DELAY_MS);
  reconnectAttempt += 1;
  reconnectTimer = setTimeout(() => { reconnectTimer = null; connect(); }, delay);
}

self.onmessage = (event) => {
  const message = event.data;
  switch (message?.type) {
    case "init":
      mediaURL = message.mediaURL;
      contextRate = message.contextRate || SAMPLE_RATE;
      capturePort = message.capturePort;
      playbackPort = message.playbackPort;
      capturePort.onmessage = (captureEvent) => capture(captureEvent.data);
      capturePort.start?.();
      playbackPort.start?.();
      connect();
      break;
    case "muted":
      muted = Boolean(message.value);
      resamplers.clear();
      break;
    case "microphone.ready":
      microphoneReady = Boolean(message.value);
      break;
    case "send.text":
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(message.data);
        postMessage({ type: "transport.stats", buffered_bytes: socket.bufferedAmount });
      }
      break;
    case "flush":
      playbackPort?.postMessage({ type: "flush" });
      break;
    case "close":
      closed = true;
      if(stableTimer!==null) clearTimeout(stableTimer);
      if (reconnectTimer !== null) clearTimeout(reconnectTimer);
      reconnectTimer = null;
      socket?.close();
      socket = null;
      capturePort?.close();
      playbackPort?.close();
      close();
      break;
  }
};
