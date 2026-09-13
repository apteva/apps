import { createRoot } from "react-dom/client";
import CalendarPanel from "../ui/CalendarPanel";
(window as any).__aptevaAppEvents = {
  subscribe: (_: string, __: string, fn: unknown) => {
    (window as any).calendarRefresh = fn;
    return () => {};
  },
};
createRoot(document.getElementById("root")!).render(
  <CalendarPanel appName="calendar" installId={1} projectId="test-proj" />,
);
