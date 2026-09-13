import * as React from "react";
import { createRoot } from "react-dom/client";
import CallsPanel from "../../ui/CallsPanel";
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension } from "../src/client";
import type { MicrophonePreview } from "../src/audio";
function SharedPreview() {
  const [status, setStatus] = React.useState("idle");
  const preview = React.useRef<MicrophonePreview | undefined>(undefined);
  function client() { return new AptevaClient({ baseURL: "" }).use(telephonyExtension, { projectId: "telephony-tier2", installId: 42 }); }
  return <><button onClick={() => {
    preview.current = client().createMicrophonePreview(level => { if (level > 0) setStatus("signal detected"); });
    void preview.current.start().catch(error => setStatus(String(error)));
  }}>Test shared microphone</button><button onClick={() => { void preview.current?.stop().then(() => setStatus("stopped")); }}>Stop shared microphone</button><output aria-label="Shared microphone status">{status}</output></>;
}
createRoot(document.getElementById("root")!).render(<React.StrictMode><SharedPreview /><CallsPanel appName="telephony" projectId="telephony-tier2" installId={42} /></React.StrictMode>);
