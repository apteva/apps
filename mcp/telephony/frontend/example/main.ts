// This host imports only the shared SDK. Call logic comes from the installed app.
import { AptevaClient } from "@apteva/web-sdk";
import type { TelephonyClient, Call } from "../src/client";
import type { SoftphoneSnapshot } from "../src/softphone";

const el = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const notice = (text: string) => { el("status").textContent = text; };
let calls: Call[] = [];
let selectedIncoming: Call | undefined;
let current: SoftphoneSnapshot = { audioState: "idle", busy: false, muted: false };
const sdk = new AptevaClient({ baseURL: location.origin });
const start = performance.now();
try {
  const loaded = await sdk.apps.load<TelephonyClient>("telephony", { projectId: "demo", installId: 42 });
  const loadMS = Math.round(performance.now() - start);
  el("load-time").textContent = `Client ready · ${loadMS} ms`;
  const client = loaded.client;
  const phone = client.createSoftphone({
    pollIntervalMs: 0,
    onLevels: (mic, speaker) => {
      el("mic").style.width = `${Math.min(100, mic * 500)}%`;
      el("speaker").style.width = `${Math.min(100, speaker * 500)}%`;
    },
    onNotice: notice,
  });
  function render(state: SoftphoneSnapshot) {
    current = state;
    const active = Boolean(state.callId);
    el("connection").textContent = active ? (state.audioState === "live" ? "Audio connected" : state.audioState) : "Ready";
    el("connection-dot").classList.toggle("ready", !active || state.audioState === "live");
    el("contact-name").textContent = active ? "Local test line" : selectedIncoming ? "Incoming test call" : "Ready when you are";
    el("contact-subtitle").textContent = active ? "Connected through the Telephony client" : selectedIncoming?.from_number || "Make a test call or simulate an incoming one.";
    el("avatar").textContent = active ? "LT" : "☎";
    el("active-controls").hidden = !active;
    el("dial").hidden = active;
    el<HTMLButtonElement>("dial").disabled = state.busy || !el<HTMLInputElement>("number").value;
    el<HTMLInputElement>("number").disabled = active || state.busy;
    el("incoming").hidden = !selectedIncoming || active;
    el<HTMLButtonElement>("answer").disabled = state.busy;
    el<HTMLButtonElement>("simulate").disabled = active || state.busy || Boolean(selectedIncoming);
    for (const id of ["mute", "reconnect", "hangup"]) el<HTMLButtonElement>(id).disabled = state.busy;
    el("mute").textContent = state.muted ? "Unmute" : "Mute";
    el("mute").setAttribute("aria-pressed", String(state.muted));
    if (state.detail) notice(state.detail);
    else if (active) notice(state.muted ? "Microphone muted. Incoming audio still plays." : "Local generated audio. Your microphone stays on this computer.");
  }
  async function action(fn: () => Promise<unknown>) {
    try { await fn(); await refresh(); }
    catch (error) { notice(error instanceof Error ? error.message : String(error)); }
  }
  function renderCalls(rows: Call[]) {
    calls = rows;
    for (const call of calls) phone.observeCall(call);
    selectedIncoming = client.incomingCalls(calls)[0];
    el("activity-count").textContent = `${calls.length} call${calls.length === 1 ? "" : "s"}`;
    const activity = el("activity");
    activity.replaceChildren();
    if (!calls.length) {
      const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "Your test calls will appear here."; activity.append(empty);
    }
    for (const call of calls.slice(0, 4)) {
      const row = document.createElement("div"); row.className = "activity-row";
      const icon = document.createElement("span"); icon.className = "activity-icon"; icon.textContent = call.direction === "inbound" ? "↙" : "↗";
      const label = document.createElement("div"), title = document.createElement("strong"), subtitle = document.createElement("small");
      title.textContent = call.direction === "inbound" ? "Incoming test call" : "Local test line";
      subtitle.textContent = call.direction === "inbound" ? call.from_number : call.to_number;
      label.append(title, subtitle);
      const status = document.createElement("span"); status.className = "activity-state"; status.textContent = call.status;
      row.append(icon, label, status); activity.append(row);
    }
    render(phone.getSnapshot());
  }
  async function refresh() { renderCalls(await client.listCalls()); }
  const unsubscribe = phone.subscribe(render);
  const watch = client.watchCalls(renderCalls, { intervalMs: 750, onError: error => notice(String(error)) });
  const letters = ["", "ABC", "DEF", "GHI", "JKL", "MNO", "PQRS", "TUV", "WXYZ", "", "+", ""];
  for (const [i, digit] of [..."123456789*0#"].entries()) {
    const button = document.createElement("button"); button.type = "button";
    button.setAttribute("aria-label", `Key ${digit}`); button.textContent = digit;
    const small = document.createElement("small"); small.textContent = letters[i] || "\u00a0"; button.append(small);
    button.addEventListener("click", () => {
      try {
        if (current.callId) phone.sendDTMF(digit);
        else { el<HTMLInputElement>("number").value += digit; render(current); }
      } catch (error) { notice(String(error)); }
    });
    el("keypad").append(button);
  }
  el("number").addEventListener("input", () => render(current));
  el("erase").addEventListener("click", () => { if (!current.callId) el<HTMLInputElement>("number").value = el<HTMLInputElement>("number").value.slice(0, -1); render(current); });
  el("dial").addEventListener("click", () => void action(() => phone.dial({ to: "+" + el<HTMLInputElement>("number").value.replace(/^\+/, ""), from: "+15550100200" })));
  el("answer").addEventListener("click", () => { if (selectedIncoming) void action(() => phone.answer(selectedIncoming!.id)); });
  el("decline").addEventListener("click", () => { if (selectedIncoming) void action(() => client.hangup(selectedIncoming!.id)); });
  el("hangup").addEventListener("click", () => void action(async () => { await phone.hangup(); notice("Call ended. Ready for the next one."); }));
  el("mute").addEventListener("click", () => phone.setMuted(!current.muted));
  el("reconnect").addEventListener("click", () => void action(() => phone.reconnect()));
  el("simulate").addEventListener("click", () => void action(async () => {
    const result = await fetch("/demo/incoming", { method: "POST" });
    if (!result.ok) throw new Error("Could not simulate an incoming call");
  }));
  window.addEventListener("pagehide", () => { watch.close(); unsubscribe(); phone.dispose(); loaded.dispose(); }, { once: true });
  // Expose read-only measurements for the example's browser verification.
  Object.assign(window, { example: {
    getSnapshot: phone.getSnapshot, loadMS,
    async warmLoad() { const start = performance.now(); const again = await sdk.apps.load("telephony", { projectId: "demo", installId: 42 }); again.dispose(); return performance.now() - start; },
  } });
  render(current);
} catch (error) { notice(`Could not load Telephony: ${String(error)}`); el("connection").textContent = "Unavailable"; }
