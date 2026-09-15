// Isolated synthetic preview; never bundled into the shipped panel.
import { createRoot } from "react-dom/client";
import A2APanel from "./A2APanel";
(window as any).__aptevaAppEvents = { subscribe: () => () => {} };
createRoot(document.getElementById("root")!).render(
  <A2APanel projectId="preview" appName="a2a" installId={1} />,
);
