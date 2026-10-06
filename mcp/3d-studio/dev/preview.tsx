import React from "react";
import { createRoot } from "react-dom/client";
import StudioPanel from "../ui/StudioPanel";
createRoot(document.getElementById("root")!).render(
  <StudioPanel projectId="studio-dev" apiBase="/api" uiBase="/ui" />,
);
