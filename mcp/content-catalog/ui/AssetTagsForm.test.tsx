import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { create, type ReactTestRenderer } from "react-test-renderer";
import { AssetTagsForm } from "./AssetTagsForm";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let view: ReactTestRenderer;
afterEach(async () => { await act(async () => view?.unmount()); });

async function mount(tags = ["best-take", "review:hold"], busy = false) {
  const calls: Record<string, unknown>[] = [];
  await act(async () => {
    view = create(<AssetTagsForm asset={{ id: "asset-a", name: "Video A", revision: 7, tags }} busy={busy} onSubmit={input => calls.push(input)} />);
  });
  const input = () => view.root.findByType("input");
  const type = async (value: string) => { await act(async () => input().props.onChange({ target: { value } })); };
  const submit = async () => { await act(async () => view.root.findByType("form").props.onSubmit({ preventDefault() {} })); };
  const click = async (label: string) => {
    await act(async () => view.root.findAllByType("button").find(button => button.props["aria-label"] === label || button.children.join("") === label)!.props.onClick());
  };
  return { calls, input, type, submit, click };
}

test("remove one namespaced tag while preserving all other labels", async () => {
  const editor = await mount();
  await editor.click("Remove tag review:hold");
  await editor.submit();
  expect(editor.calls).toEqual([{
    asset_ids: ["asset-a"], expected_revisions: { "asset-a": 7 }, tags_add: [], tags_remove: ["review:hold"],
  }]);
});

test("save includes a typed tag, normalizes and deduplicates", async () => {
  const editor = await mount();
  await editor.type(" Review:Approved, BEST-TAKE, review:approved ");
  await editor.submit();
  expect(editor.calls[0].tags_add).toEqual(["review:approved"]);
  expect(editor.calls[0].tags_remove).toEqual([]);
});

test("Enter adds chips without submitting the form", async () => {
  const editor = await mount([]);
  await editor.type("review:hold, teaser");
  await act(async () => editor.input().props.onKeyDown({ key: "Enter", preventDefault() {} }));
  expect(editor.calls).toHaveLength(0);
  expect(editor.input().props.value).toBe("");
  await editor.submit();
  expect(editor.calls[0].tags_add).toEqual(["review:hold", "teaser"]);
});

test("removing the last saved chip emits an explicit removal", async () => {
  const editor = await mount(["review:hold"]);
  await editor.click("Remove tag review:hold");
  await editor.submit();
  expect(editor.calls[0].tags_remove).toEqual(["review:hold"]);
});

test("invalid tags keep the draft and prevent saving", async () => {
  const editor = await mount();
  for (const value of ["has spaces", "é".repeat(33)]) {
    await editor.type(value);
    await editor.submit();
    expect(editor.calls).toHaveLength(0);
    expect(view.root.findByProps({ role: "alert" }).children.join("")).toContain("64 bytes");
    expect(editor.input().props.value).toBe(value);
  }
});

test("enforces the final tag limit and allows a removal before an addition", async () => {
  const editor = await mount(Array.from({ length: 25 }, (_, index) => `tag-${index}`));
  await editor.type("new-tag");
  await editor.submit();
  expect(editor.calls).toHaveLength(0);
  expect(view.root.findByProps({ role: "alert" }).children.join("")).toContain("25 tags");
  await editor.click("Remove tag tag-0");
  await editor.submit();
  expect(editor.calls[0].tags_add).toEqual(["new-tag"]);
  expect(editor.calls[0].tags_remove).toEqual(["tag-0"]);
});

test("busy edits disable controls and ignore repeated submissions", async () => {
  const editor = await mount(["review:hold"], true);
  expect(view.root.findByType("fieldset").props.disabled).toBe(true);
  await editor.submit();
  expect(editor.calls).toHaveLength(0);
});
