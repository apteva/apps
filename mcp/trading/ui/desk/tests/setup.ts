import { Window } from "happy-dom";
const win = new Window({ url: "http://localhost" });
for (const key of ["window", "document", "navigator", "HTMLElement", "HTMLInputElement", "HTMLTextAreaElement", "Event", "MouseEvent"] as const) Object.defineProperty(globalThis, key, { configurable: true, value: key === "window" ? win : key === "document" ? win.document : (win as any)[key] });
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
