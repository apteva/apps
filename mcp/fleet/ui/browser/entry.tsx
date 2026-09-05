import React from "react";
import { createRoot } from "react-dom/client";
import FleetPanel from "../FleetPanel";
createRoot(document.getElementById("root")!).render(
  <FleetPanel appName="fleet" projectId="test-project" installId={1} />,
);
