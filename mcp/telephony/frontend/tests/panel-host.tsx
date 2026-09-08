import * as React from "react";
import { createRoot } from "react-dom/client";
import CallsPanel from "../../ui/CallsPanel";
createRoot(document.getElementById("root")!).render(<React.StrictMode><CallsPanel appName="telephony" projectId="telephony-tier2" installId={42} /></React.StrictMode>);
