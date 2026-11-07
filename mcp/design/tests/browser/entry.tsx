import React from "react";
import { createRoot } from "react-dom/client";
import DesignPanel from "../../ui/DesignPanel";
createRoot(document.getElementById("root")!).render(
  <DesignPanel projectId="browser-fixture" />,
);
