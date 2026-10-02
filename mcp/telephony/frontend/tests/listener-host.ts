import {TelephonyClient} from "../src/client";
import { AptevaClient } from "@apteva/web-sdk";
const w = window as any;
w.loadListener = async () => {
  const sdk = new AptevaClient({ baseURL: location.origin, accessToken: "headless-browser-fixture" });
  if(new URLSearchParams(location.search).has('strict')) {
    w.client=new TelephonyClient(sdk.app('telephony',{projectId:'telephony-tier2',installId:42}));
  } else {
    w.loaded = await sdk.apps.load<any>("telephony", {projectId:"telephony-tier2",installId:42});
    w.client = w.loaded.client;
  }
  w.calls = await w.client.listCalls();
  w.callListener = w.client.createCallListener({ stereo: true, onDiagnostics: (value: unknown) => { w.listenerDiagnostics = value; } });
  document.querySelector("#listen")!.addEventListener("click", () => { w.listenPromise = w.callListener.listen(w.calls[0].id); });
};

document.querySelector("#coach")!.addEventListener("click", () => { w.coachPromise = w.callListener.coach(w.calls[0].id); });
document.querySelector("#talk")!.addEventListener("pointerdown", () => { w.talkPromise = w.callListener.startTalking(); });
document.querySelector("#talk")!.addEventListener("pointerup", () => w.callListener.stopTalking());
