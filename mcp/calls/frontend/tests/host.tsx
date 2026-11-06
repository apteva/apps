import { AptevaClient } from "@apteva/web-sdk";
import { createRoot } from "react-dom/client";
import type { CallsClient, CallsSession } from "../index";
import { RoomSession } from "../../ui/RoomSession";

const query = new URLSearchParams(location.search);
const root = document.getElementById("root")!;
const projectId = "test-proj";

if (location.pathname === "/panel") {
  createRoot(root).render(<RoomSession roomId={Number(query.get("room"))} projectId={projectId} onEnd={() => {}} />);
} else {
  const sdk = new AptevaClient({ baseURL: query.get("gateway")!, accessToken: "browser-fixture" });
  const { client } = await sdk.apps.load<CallsClient>("calls", { projectId, installId: 1 });
  const session = client.createSession();
  Object.assign(window, { calls: session });
  session.subscribe(snapshot => {
    root.textContent = snapshot.phase;
  });
  await session.join(query.get("token")!, "External guest");
}

declare global {
  interface Window { calls: CallsSession }
}
