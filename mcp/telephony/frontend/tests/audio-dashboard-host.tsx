import * as React from "react";
import { createRoot } from "react-dom/client";
import AudioHealthView from "../../ui/AudioHealthView";
import AudioHealthWidget from "../../ui/AudioHealthWidget";
const props = {
  appName: "telephony",
  projectId: "monitor-project",
  installId: 42,
};
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    {new URLSearchParams(window.location.search).has("widget") ? (
      <div style={{ height: 420, maxWidth: 620, margin: 24 }}>
        <AudioHealthWidget {...props} />
      </div>
    ) : (
      <div style={{ height: "100vh" }}>
        <AudioHealthView {...props} />
      </div>
    )}
  </React.StrictMode>,
);
