import "../../ui/testDom";
import { expect, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { PageContextChip, useMessagePageContext, type PageContext } from "./pageContext";
function Composer({ context }: { context?: PageContext }) {
  const shared = useMessagePageContext(context);
  return <><PageContextChip context={shared.context} onRemove={shared.dismiss}/><output>{JSON.stringify(shared.context) || "no context"}</output></>;
}
test("context is removable, reappears on navigation, and absent when sharing is off", () => {
  const context: PageContext = { version: 1, page: "app", project_id: "p", app: "tickets" };
  const container = document.createElement("div");
  const previousAct = (globalThis as any).IS_REACT_ACT_ENVIRONMENT;
  (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
  const root = createRoot(container);
  try {
    act(() => root.render(<Composer context={context}/>));
    expect(container.textContent).toContain("Using context: tickets app");
    act(() => container.querySelector<HTMLButtonElement>("button")!.click());
    expect(container.textContent).toBe("no context");
    act(() => root.render(<Composer context={{ ...context, app: "tasks" }}/>));
    expect(container.textContent).toContain("Using context: tasks app");
    act(() => root.render(<Composer/>));
    expect(container.querySelector("button")).toBeNull();
  } finally { act(() => root.unmount()); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = previousAct; }
});
