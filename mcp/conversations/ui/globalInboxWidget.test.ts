import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

test("Inbox dashboard wrapper mounts both project and global scopes", () => {
  const wrapper = readFileSync(new URL("./InboxWidget.tsx", import.meta.url), "utf8");
  expect(wrapper).toContain('props.dashboardScope === "global"');
  expect(wrapper).toContain("if (!global && !props.projectId)");
  expect(wrapper).toContain("dashboardScope={props.dashboardScope}");
  expect(wrapper).toContain("conversationsHref={global ? undefined");
});

test("global Inbox filters the existing route and scopes actions per item", () => {
  const widget = readFileSync(new URL("../frontend/src/InboxWidget.tsx", import.meta.url), "utf8");
  expect(widget).toContain('props.dashboardScope === "global"');
  expect(widget).toContain('t("inbox.projectFilter")');
  expect(widget).toContain("project_id: global ? selectedProject || undefined");
  expect(widget).toContain("projectId={item.project_id || projectId}");
  expect(widget).toContain("item.project_name || item.project_id");
  expect(widget).not.toContain('t("common.refresh")');
  expect(widget).toContain("window.setInterval(load, 15000)");
});
