import { createRoot } from "react-dom/client";
import DeployPanel from "../DeployPanel";
(window as any).__aptevaAppEvents = {subscribe: () => () => {}};
const root = createRoot(document.getElementById("root")!);
(window as any).renderProject = (projectId: string) => root.render(<DeployPanel projectId={projectId} installId={1} />);
(window as any).renderProject("p1");
