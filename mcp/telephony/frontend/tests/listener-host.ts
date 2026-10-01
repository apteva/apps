import { AptevaClient } from "@apteva/web-sdk";
import { TelephonyClient } from "../src/client";
const w = window as any;
w.loadListener = async () => {
  const sdk = new AptevaClient({ baseURL: location.origin, accessToken: "headless-browser-fixture" });
  w.client = new TelephonyClient(sdk.app("telephony", { projectId: "telephony-tier2", installId: 42 }));
  w.calls = await w.client.listCalls();
  w.callListener = w.client.createCallListener({ stereo: true, onDiagnostics: (value: unknown) => { w.listenerDiagnostics = value; } });
  document.querySelector("#listen")!.addEventListener("click", () => { w.listenPromise = w.callListener.listen(w.calls[0].id); });
};
