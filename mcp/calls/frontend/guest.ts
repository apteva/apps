import { CallsSession, parseCallsInvite, type CallsAdmission, type CallsCapabilities, type CallsSnapshot } from "./client";

function element<T extends HTMLElement = HTMLElement>(id: string): T {
  return document.getElementById(id) as T;
}

const options = JSON.parse(element("calls-config").textContent ?? "{}") as {
  roomId: number; title: string; roomStatus: string; capabilities: CallsCapabilities;
};
const { token, apiBaseURL } = parseCallsInvite(location.href);
const session = new CallsSession({ apiBaseURL });
const sessionKey = "calls-session-" + options.roomId + "-" + token.slice(-8);
const name = element<HTMLInputElement>("display-name");
const localVideo = element<HTMLVideoElement>("local-video");
const localTile = element("local-tile");
const mic = element<HTMLButtonElement>("mic-button");
const camera = element<HTMLButtonElement>("cam-button");
const share = element<HTMLButtonElement>("share-button");
const leave = element<HTMLButtonElement>("leave-button");
const join = element<HTMLButtonElement>("join-button");
const messages = element("messages");
const message = element<HTMLInputElement>("message-input");
const videos = new Map<MediaStream, HTMLDivElement>();
let lastMessages: CallsSnapshot["messages"];

function report(error: unknown) {
  const node = element("error");
  node.textContent = (error as Error).message;
  node.style.display = "block";
}

function toggle(button: HTMLButtonElement, enabled: boolean, on: boolean, label: string) {
  button.disabled = !enabled;
  button.classList.toggle("active", on);
  button.setAttribute("aria-pressed", String(on));
  button.textContent = label;
}

function syncName() {
  const label = name.value.trim() || "Guest";
  element("avatar").textContent = label[0].toUpperCase();
  element("tile-label").textContent = label;
}

function render(snapshot: CallsSnapshot) {
  const joined = snapshot.phase === "joined";
  const caps = snapshot.capabilities;
  element("room-status").textContent = snapshot.phase === "idle" ? options.roomStatus : snapshot.phase;
  element("participant-count").textContent = String(snapshot.participants.length);
  join.disabled = snapshot.phase !== "idle" || options.roomStatus === "ended";
  join.textContent = { idle: "Join room", joining: "Joining…", joined: "Joined", ended: "Room closed" }[snapshot.phase];
  const notice = element("joined");
  notice.style.display = joined ? "block" : "none";
  notice.textContent = joined ? "Joined " + options.title + " as " + (snapshot.participant?.display_name || "Guest") + "." : "";
  toggle(mic, joined && !!caps.audio, snapshot.microphone, snapshot.microphone ? "Mic on" : "Mic");
  toggle(camera, joined && !!caps.video, snapshot.camera, snapshot.camera ? "Cam on" : "Cam");
  toggle(share, joined && !!caps.screen, !!snapshot.screenStream, snapshot.screenStream ? "Stop share" : "Share");
  leave.disabled = !joined;
  leave.textContent = (joined ? caps : options.capabilities).room_control ? "End" : "Leave";
  element("chat").classList.toggle("open", joined && !!caps.chat);
  const preview = snapshot.screenStream ?? snapshot.localStream ?? null;
  if (localVideo.srcObject !== preview) localVideo.srcObject = preview;
  localTile.classList.toggle("video-on", snapshot.camera || !!snapshot.screenStream);
  for (const [stream, tile] of videos) {
    if (!snapshot.remoteStreams.some(item => item.stream === stream)) {
      tile.remove();
      videos.delete(stream);
    }
  }
  for (const { participant, stream } of snapshot.remoteStreams) {
    const existing = videos.get(stream);
    if (existing) {
      existing.classList.toggle("video-on", stream.getVideoTracks().length > 0);
      continue;
    }
    const tile = document.createElement("div");
    tile.className = "tile";
    tile.classList.toggle("video-on", stream.getVideoTracks().length > 0);
    const video = document.createElement("video");
    video.autoplay = true;
    video.playsInline = true;
    video.srcObject = stream;
    const label = document.createElement("div");
    label.className = "tile-label";
    label.textContent = participant.display_name || "Participant " + participant.id;
    tile.append(video, label);
    localTile.parentElement?.append(tile);
    videos.set(stream, tile);
  }
  if (snapshot.messages !== lastMessages) {
    lastMessages = snapshot.messages;
    messages.replaceChildren(...snapshot.messages.map(item => {
      const row = document.createElement("div");
      row.className = "message";
      const by = document.createElement("div");
      by.className = "by";
      by.textContent = item.speaker_name || snapshot.participants.find(p => p.id === item.participant_id)?.display_name || "Participant";
      const body = document.createElement("div");
      body.className = "body";
      body.textContent = item.body;
      row.append(by, body);
      return row;
    }));
    messages.scrollTop = messages.scrollHeight;
  }
  if (snapshot.error) report(new Error(snapshot.error));
  if (snapshot.phase === "ended") forgetAdmission();
}

function forgetAdmission() {
  try { sessionStorage.removeItem(sessionKey); } catch {}
}

name.addEventListener("input", syncName);
mic.addEventListener("click", () => void session.setMicrophone(!session.getSnapshot().microphone).catch(report));
camera.addEventListener("click", () => void session.setCamera(!session.getSnapshot().camera).catch(report));
share.addEventListener("click", () => void session.setScreenShare(!session.getSnapshot().screenStream).catch(report));
leave.addEventListener("click", () => {
  if (session.getSnapshot().capabilities.room_control) {
    if (confirm("End this room for everyone?")) void session.end().catch(report);
  } else {
    forgetAdmission();
    void session.leave();
  }
});
element("join-form").addEventListener("submit", async event => {
  event.preventDefault();
  element("error").style.display = "none";
  try {
    await session.join(token, name.value);
    const admission = session.getAdmission();
    if (admission) {
      try { sessionStorage.setItem(sessionKey, JSON.stringify(admission)); } catch {}
    }
  } catch (error) { report(error); }
});
element("chat-form").addEventListener("submit", async event => {
  event.preventDefault();
  const body = message.value.trim();
  if (!body) return;
  try {
    await session.sendMessage(body);
    if (message.value.trim() === body) message.value = "";
  } catch (error) { report(error); }
});
syncName();
render(session.getSnapshot());
session.subscribe(render);

async function resume() {
  try {
    const saved = sessionStorage.getItem(sessionKey);
    if (saved) await session.resume(JSON.parse(saved) as CallsAdmission);
  } catch { forgetAdmission(); }
}
void resume();
