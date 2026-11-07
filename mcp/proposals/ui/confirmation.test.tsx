import {afterAll, beforeAll, expect, test} from "bun:test";
import {createElement} from "react";
import {act, create, type ReactTestRenderer} from "react-test-renderer";
import {useConfirmation} from "./confirmation";

const previousActEnvironment = (globalThis as any).IS_REACT_ACT_ENVIRONMENT;
beforeAll(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true; });
afterAll(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = previousActEnvironment; });

test("admin actions wait for confirmation and dismiss safely on cancel or unmount", async () => {
  let confirmation!: ReturnType<typeof useConfirmation>;
  function Harness() { confirmation = useConfirmation(); return confirmation.modal; }
  let renderer!: ReactTestRenderer;
  await act(async () => { renderer = create(createElement(Harness)); });
  let result: boolean | undefined;
  let answer!: Promise<boolean>;
  await act(async () => { answer = confirmation.confirm("Remove the category?", "Remove category", "Remove category"); answer.then(value => { result = value; }); });
  expect(result).toBeUndefined();
  expect(renderer.root.findByType("dialog").props["aria-label"]).toBe("Remove category");
  await act(async () => { renderer.root.findAllByType("button").find(button => button.children.includes("Cancel"))!.props.onClick(); });
  expect(await answer).toBe(false);
  await act(async () => { answer = confirmation.confirm("Publish revision?", "Publish"); });
  await act(async () => { renderer.root.findAllByType("button").find(button => button.children.includes("Publish"))!.props.onClick(); });
  expect(await answer).toBe(true);
  await act(async () => { answer = confirmation.confirm("Replace link?"); });
  await act(async () => { renderer.unmount(); });
  expect(await answer).toBe(false);
});
