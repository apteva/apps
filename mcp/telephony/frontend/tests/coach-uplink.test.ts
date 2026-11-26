import { afterEach, beforeEach, expect, test } from "bun:test";
import { createListenerRTC } from "../src/listener-webrtc";
import { beginCoachTalk, resetCoachTalkForTests } from "../src/coach-talk";
import { FakePeer, FakeSocket, fakeDevices, installFakeBrowser, settle } from "./rtc-fake-browser";

type Track = { stopped: boolean; stop(): void; onended: unknown; readyState: string; getSettings(): object };
type Pending = { resolve(): void; reject(error: Error): void; tracks: Track[] };

let restore: () => void;
let restoreWindow: () => void;
let mics: Pending[];

/** Microphones open only when the test says so, and record their stop. */
function deferredMicrophones() {
  mics = [];
  (globalThis as any).navigator.mediaDevices.getUserMedia = () => new Promise((resolve, reject) => {
    const track: Track = { stopped: false, stop() { this.stopped = true; }, onended: null, readyState: "live", getSettings: () => ({}) };
    const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
    mics.push({ resolve: () => resolve(stream), reject, tracks: [track] });
  });
}

beforeEach(() => {
  restore = installFakeBrowser();
  resetCoachTalkForTests();
  const saved = ["window", "document"].map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)] as const);
  const events = { addEventListener() {}, removeEventListener() {} };
  Object.defineProperty(globalThis, "window", { configurable: true, writable: true, value: { ...events } });
  Object.defineProperty(globalThis, "document", { configurable: true, writable: true, value: { ...events, visibilityState: "visible" } });
  restoreWindow = () => { for (const [name, descriptor] of saved) { if (descriptor) Object.defineProperty(globalThis, name, descriptor); else Reflect.deleteProperty(globalThis, name); } };
  deferredMicrophones();
});
afterEach(() => { restoreWindow(); restore(); });

async function openCoaching() {
  const talking: boolean[] = [];
  const app = { mcpURL: () => "https://example.test/mcp" } as any;
  const connection = createListenerRTC(app, {}).create({ onReady() {}, onClose() {}, onTalking: (value: boolean) => talking.push(value) } as any);
  const started = connection.start("wss://example.test/listen", { coaching: true });
  await settle();
  const socket = FakeSocket.instances[0];
  socket.open();
  socket.receive({ type: "webrtc.config", listener: true, ice_servers: [] });
  await settle();
  socket.receive({ type: "webrtc.answer", sdp: "v=0 answer" });
  FakePeer.instances[0].state("connected");
  socket.receive({ type: "listener.ready", coaching: true });
  await started;
  await settle();
  return { connection, socket, talking };
}

/** Answers the uplink prepare the browser sent and connects it. */
async function connectUplink(socket: FakeSocket, index: number) {
  const prepares = socket.sentOfType("coach.webrtc.prepare");
  const connection = prepares[prepares.length - 1].connection;
  socket.receive({ type: "coach.webrtc.answer", connection, sdp: "v=0 answer" });
  await settle();
  FakePeer.instances[index].state("connected");
  await settle();
  return connection;
}

const lastStart = (socket: FakeSocket) => socket.sentOfType("coach.start").at(-1)?.generation;

test("one prepared uplink carries 30 presses; the mic opens alongside coach.start after the first", async () => {
  const { connection, socket, talking } = await openCoaching();
  // Prepared when coaching opened, before any press.
  expect(FakePeer.instances.length).toBe(2);
  expect(socket.sentOfType("coach.webrtc.prepare")).toHaveLength(1);
  // A zero source keeps the track sending, so the RTP clock runs between presses.
  expect(fakeDevices.silenceSources).toBe(1);
  await connectUplink(socket, 1);

  // First press: permission may prompt, so the server is asked only once the mic is open.
  let press = connection.startTalking!();
  await settle();
  expect(socket.sentOfType("coach.start")).toHaveLength(0);
  mics[0].resolve();
  await settle();
  socket.receive({ type: "coach.started", generation: lastStart(socket) });
  await press;
  expect(talking.at(-1)).toBe(true);
  connection.stopTalking!();
  expect(mics[0].tracks[0].stopped).toBe(true);

  for (let i = 1; i < 30; i++) {
    press = connection.startTalking!();
    await settle();
    // coach.start goes out while the microphone is still opening.
    expect(socket.sentOfType("coach.start")).toHaveLength(i + 1);
    expect(mics).toHaveLength(i + 1);
    socket.receive({ type: "coach.started", generation: lastStart(socket) });
    mics[i].resolve();
    await press;
    expect(talking.at(-1)).toBe(true);
    connection.stopTalking!();
    // Release stops the microphone synchronously and tells the server.
    expect(mics[i].tracks[0].stopped).toBe(true);
    expect(talking.at(-1)).toBe(false);
  }
  expect(socket.sentOfType("coach.stop")).toHaveLength(30);
  expect(FakePeer.instances.length).toBe(2);
  expect(FakePeer.instances[1].closed).toBe(false);
  expect(socket.sentOfType("coach.webrtc.prepare")).toHaveLength(1);
  expect(socket.sentOfType("coach.webrtc.offer")).toHaveLength(0);
  connection.stop();
  expect(FakePeer.instances[1].closed).toBe(true);
});

test("release while the mic opens or before the server's start leaves nothing running", async () => {
  const { connection, socket, talking } = await openCoaching();
  await connectUplink(socket, 1);
  let press = connection.startTalking!();
  mics[0].resolve();
  await settle();
  socket.receive({ type: "coach.started", generation: lastStart(socket) });
  await press;
  connection.stopTalking!();

  // Released while the microphone is still opening: it is stopped the moment it opens.
  press = connection.startTalking!();
  await settle();
  connection.stopTalking!();
  mics[1].resolve();
  await press;
  await settle();
  expect(mics[1].tracks[0].stopped).toBe(true);
  expect(talking.at(-1)).toBe(false);

  // Released after the mic opened but before coach.started: a late start is ignored.
  press = connection.startTalking!();
  await settle();
  const generation = lastStart(socket);
  mics[2].resolve();
  await settle();
  connection.stopTalking!();
  expect(mics[2].tracks[0].stopped).toBe(true);
  socket.receive({ type: "coach.started", generation });
  await press;
  await settle();
  expect(talking.at(-1)).toBe(false);
  expect(socket.sentOfType("coach.stop").at(-1).generation).toBeGreaterThan(generation);
  connection.stop();
});

test("a failed or refused uplink is prepared again on the next press", async () => {
  const { connection, socket, talking } = await openCoaching();
  await connectUplink(socket, 1);
  FakePeer.instances[1].state("failed");
  await settle();
  expect(FakePeer.instances[1].closed).toBe(true);

  let press = connection.startTalking!();
  await settle();
  expect(FakePeer.instances.length).toBe(3);
  const prepared = socket.sentOfType("coach.webrtc.prepare");
  expect(prepared).toHaveLength(2);
  expect(prepared[1].connection).toBeGreaterThan(prepared[0].connection);
  mics[0].resolve();
  await settle();
  socket.receive({ type: "coach.started", generation: lastStart(socket) });
  await connectUplink(socket, 2);
  await press;
  expect(talking.at(-1)).toBe(true);
  connection.stopTalking!();

  // The server refuses the next prepare: that press fails, the one after retries.
  FakePeer.instances[2].state("failed");
  await settle();
  press = connection.startTalking!();
  await settle();
  socket.receive({ type: "coach.webrtc.rejected", connection: socket.sentOfType("coach.webrtc.prepare").at(-1).connection });
  socket.receive({ type: "coach.started", generation: lastStart(socket) });
  mics[1].resolve();
  await expect(press).rejects.toThrow("Coaching media unavailable");
  expect(mics[1].tracks[0].stopped).toBe(true);
  press = connection.startTalking!();
  await settle();
  expect(socket.sentOfType("coach.webrtc.prepare")).toHaveLength(4);
  connection.stopTalking!();
  mics[2].resolve();
  await press;
  expect(mics[2].tracks[0].stopped).toBe(true);
  connection.stop();
});

test("beginCoachTalk: a refused start stops the opened microphone's owner; a failed mic fails at once", async () => {
  // Make the next press parallel.
  let opened: Track | undefined;
  const mic = () => Promise.resolve({ getTracks: () => [opened = { stopped: false, stop() { this.stopped = true; }, onended: null, readyState: "live", getSettings: () => ({}) }] } as any);
  await beginCoachTalk({ openMic: mic, requestStart: async () => {}, current: () => true, adopt() {} });

  let adopted: any;
  await expect(beginCoachTalk({ openMic: mic, requestStart: () => Promise.reject(new Error("refused")), current: () => true, adopt: s => { adopted = s; } })).rejects.toThrow("refused");
  expect(adopted).toBeDefined();

  let startSettled = false;
  const slowStart = new Promise<void>(resolve => setTimeout(() => { startSettled = true; resolve(); }, 200));
  await expect(beginCoachTalk({ openMic: () => Promise.reject(new Error("no mic")), requestStart: () => slowStart, current: () => true, adopt() {} })).rejects.toThrow("no mic");
  expect(startSettled).toBe(false);
});
