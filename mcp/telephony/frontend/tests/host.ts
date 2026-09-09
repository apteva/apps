import { AptevaClient } from "@apteva/web-sdk";
const w = window as any;
w.loadPhone = async (gateway: string) => {
  const sdk = new AptevaClient({ baseURL: gateway, accessToken: "headless-browser-fixture" });
  w.loaded = await sdk.apps.load<any>("telephony", { projectId: "telephony-tier2", installId: 42, clientOptions: new URLSearchParams(location.search).has("application-user") ? { authProvider: "browser-login" } : undefined });
  w.client = w.loaded.client;
  w.maxMic = 0; w.maxSpeaker = 0; w.notices = [];
  w.phone = w.client.createSoftphone({
    pollIntervalMs: 100,
    onLevels: (mic: number, speaker: number) => { w.maxMic = Math.max(w.maxMic, mic); w.maxSpeaker = Math.max(w.maxSpeaker, speaker); },
    onDiagnostics: (value: unknown) => { w.diagnostics = value; },
    onNotice: (detail: string) => { w.notices.push(detail); },
  });
  w.calls = await w.client.listCalls();
  document.querySelector("#answer")!.addEventListener("click", () => {
    w.answerPromise = w.phone.answer(w.calls[0].id).catch((e: Error) => { w.answerError = e.message; });
  });
};
