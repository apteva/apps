import * as React from "react";
import { createRoot } from "react-dom/client";
import CallsPanel from "../../ui/CallsPanel";
import CallDiagnostics from "../../ui/CallDiagnostics";
const host = {
  appName: "telephony",
  projectId: "fixture-project",
  installId: 42,
};
const standalone = new URLSearchParams(location.search).has("standalone");
createRoot(document.getElementById("root")!).render(
  standalone ? (
    <main style={{ maxWidth: 420, margin: "24px auto", padding: 12 }}>
      <CallDiagnostics host={host} callId="live-call" />
    </main>
  ) : (
    <CallsPanel {...host} />
  ),
);
