import "./testDom";
import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { createRoot, type Root } from "react-dom/client";
import { act } from "react";
import { VoiceControls } from "../frontend/src/voiceControls";
import { ConversationsClient } from "../frontend/src/client";
import type { AppHandle } from "@apteva/web-sdk";
import { finalSpeechText } from "../frontend/src/voiceDictation";

let windowMock: Window, root: Root, element: HTMLElement;
let calls: string[];
let track: { enabled: boolean; stop: () => void };

class FakeContext {
  state = "running";
  destination = {};
  audioWorklet = { addModule: async (url: string) => { calls.push("worklet:" + url); } };
  createMediaStreamSource() { return { connect() {}, disconnect() {} }; }
  close() { return Promise.resolve(); }
}
class FakeWorklet {
  port = { onmessage: null as ((event: MessageEvent<ArrayBuffer>) => void) | null };
  connect() {}
  disconnect() {}
}
class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = 1;
  binaryType = "";
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  private events = new Map<string, () => void>();
  constructor(public url: string) { FakeSocket.instances.push(this); calls.push("socket:" + url); queueMicrotask(() => this.events.get("open")?.()); }
  addEventListener(name: string, fn: () => void) { this.events.set(name, fn); }
  send() {}
  close() { this.onclose?.(); }
}
class FakeRecognition {
  static instance: FakeRecognition;
  lang = "";
  continuous = false;
  interimResults = false;
  onresult: ((event: any) => void) | null = null;
  onerror: ((event: any) => void) | null = null;
  onend: (() => void) | null = null;
  constructor() { FakeRecognition.instance = this; }
  start() { calls.push("recognition:start"); }
  stop() { calls.push("recognition:stop"); this.onend?.(); }
  abort() { calls.push("recognition:abort"); this.onend?.(); }
}

beforeEach(() => {
  windowMock = new Window({ url: "https://example.test/" });
  element = windowMock.document.createElement("div") as unknown as HTMLElement;
  windowMock.document.body.appendChild(element as any);
  root = createRoot(element);
  calls = [];
  FakeSocket.instances = [];
  track = { enabled: true, stop: () => { calls.push("track:stop"); } };
  Object.assign(globalThis, { window: windowMock, document: windowMock.document, navigator: windowMock.navigator,
    AudioContext: FakeContext, AudioWorkletNode: FakeWorklet, WebSocket: FakeSocket, IS_REACT_ACT_ENVIRONMENT: true });
  Object.assign(windowMock, { AudioWorkletNode: FakeWorklet });
  delete (windowMock as any).SpeechRecognition;
  Object.defineProperty(windowMock.navigator, "mediaDevices", { configurable: true,
    value: { getUserMedia: async () => ({ getAudioTracks: () => [track], getTracks: () => [track] }) } });
});
afterEach(async () => { await act(async () => root.unmount()); await windowMock.happyDOM.abort(); });

test("capture worklet stays on the scoped Conversations asset route", () => {
  const client = new ConversationsClient({ name: "conversations", projectId: "project", installId: 7,
    mcpURL: () => "https://example.test/api/apps/conversations/mcp?project_id=project&install_id=7",
  } as AppHandle);
  expect(client.voiceWorkletURL()).toBe("https://example.test/api/apps/conversations/ui/realtime-capture-worklet.js?project_id=project&install_id=7");
});

test("mic joins the selected chat, mutes locally and ends the realtime child", async () => {
  const activeChanges: boolean[] = [];
  const client = {
    voiceWorkletURL: () => "https://example.test/api/apps/conversations/ui/realtime-capture-worklet.js",
    voiceStatus: async () => { calls.push("status"); return { status: "closed" }; },
    startVoice: async (id: string) => { calls.push("start:" + id); return { status: "active", audio_bridge_url: "wss://example.test/audio?token=one" }; },
    endVoice: async (id: string) => { calls.push("end:" + id); return { status: "closed" }; },
  } as unknown as ConversationsClient;
  await act(async () => root.render(<VoiceControls client={client} chatId="conv-one" onActiveChange={active => activeChanges.push(active)} />));
  const click = async (label: string) => { await act(async () => {
    (element.querySelector(`[aria-label="${label}"]`) ?? [...element.querySelectorAll("button")].find(button => button.textContent === label))?.dispatchEvent(new windowMock.MouseEvent("click", { bubbles: true }) as unknown as Event);
    await new Promise(resolve => setTimeout(resolve, 10));
  }); };
  await click("Start voice in this chat");
  expect(calls).toContain("start:conv-one");
  expect(activeChanges).toContain(true);
  expect(calls).toContain("socket:wss://example.test/audio?token=one");
  expect(element.textContent).toContain("Listening");
  expect(element.textContent).toContain("Live transcript may have gaps");
  await click("Mute");
  expect(track.enabled).toBe(false);
  await click("End voice");
  expect(calls).toContain("end:conv-one");
  expect(calls).toContain("track:stop");
  expect(activeChanges.at(-1)).toBe(false);
});

test("leaving a chat during microphone permission does not spawn or leak capture", async () => {
  let grant!: (stream: unknown) => void;
  Object.defineProperty(windowMock.navigator, "mediaDevices", { configurable: true,
    value: { getUserMedia: () => new Promise(resolve => { grant = resolve; }) } });
  const client = { voiceWorkletURL: () => "https://example.test/worklet.js",
    voiceStatus: async () => { calls.push("status"); return { status: "closed" }; },
    startVoice: async () => { calls.push("start"); return { status: "active" }; },
    endVoice: async () => { calls.push("end"); return { status: "closed" }; },
  } as unknown as ConversationsClient;
  await act(async () => root.render(<VoiceControls client={client} chatId="conv-one" />));
  await act(async () => { element.querySelector("button")!.dispatchEvent(new windowMock.MouseEvent("click", { bubbles: true }) as unknown as Event); });
  await act(async () => root.unmount());
  await act(async () => { grant({ getTracks: () => [track] }); await new Promise(resolve => setTimeout(resolve, 5)); });
  expect(calls).toContain("track:stop");
  expect(calls).toContain("status");
  expect(calls).not.toContain("start");
});

test("realtime-unavailable agents dictate into a reviewed draft without spawning or sending", async () => {
  Object.assign(windowMock, { SpeechRecognition: FakeRecognition });
  const transcripts: string[] = [];
  const client = { voiceStatus: async () => ({ status: "closed", mode: "dictation" }),
    voiceWorkletURL: () => "https://example.test/worklet.js",
    startVoice: async () => { calls.push("spawn"); return { status: "active" }; },
    send: async () => { calls.push("send"); },
  } as unknown as ConversationsClient;
  await act(async () => { root.render(<VoiceControls client={client} chatId="conv-one" onTranscript={text => transcripts.push(text)} />); await new Promise(resolve => setTimeout(resolve, 0)); });
  await act(async () => { element.querySelector('button[aria-label="Dictate a message"]')!.dispatchEvent(new windowMock.MouseEvent("click", { bubbles: true }) as unknown as Event); await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(calls).toContain("recognition:start");
  expect(calls).not.toContain("spawn");
  const result = { isFinal: true, 0: { transcript: "Find the process" } };
  await act(async () => { FakeRecognition.instance.onresult?.({ resultIndex: 0, results: [result] }); });
  expect(transcripts).toEqual(["Find the process"]);
  expect(calls).not.toContain("send");
  expect(element.textContent).toContain("Review the transcript, then press Send");
  await act(async () => { [...element.querySelectorAll("button")].find(button => button.textContent === "Stop dictation")!.dispatchEvent(new windowMock.MouseEvent("click", { bubbles: true }) as unknown as Event); });
  expect(calls).toContain("recognition:stop");
});

test("repeated browser speech results append only once", () => {
  const committed = new Set<number>();
  const event = { resultIndex: 0, results: [{ isFinal: true, 0: { transcript: "hello" } }] };
  expect(finalSpeechText(event, committed)).toBe("hello");
  expect(finalSpeechText(event, committed)).toBe("");
});

test("dropped audio bridge renews its one-use capability", async () => {
  const client = { voiceWorkletURL: () => "https://example.test/worklet.js",
    voiceStatus: async () => ({ status: "closed" }),
    startVoice: async () => ({ status: "active", audio_bridge_url: "wss://example.test/audio?token=one" }),
    renewVoice: async () => { calls.push("renew"); return { status: "active", audio_bridge_url: "wss://example.test/audio?token=two" }; },
    endVoice: async () => ({ status: "closed" }),
  } as unknown as ConversationsClient;
  await act(async () => root.render(<VoiceControls client={client} chatId="conv-one" />));
  await act(async () => { element.querySelector("button")!.dispatchEvent(new windowMock.MouseEvent("click", { bubbles: true }) as unknown as Event); await new Promise(resolve => setTimeout(resolve, 10)); });
  await act(async () => { FakeSocket.instances[0].close(); await new Promise(resolve => setTimeout(resolve, 10)); });
  expect(calls).toContain("renew");
  expect(calls).toContain("socket:wss://example.test/audio?token=two");
});
