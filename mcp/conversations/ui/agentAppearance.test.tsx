import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { AgentMark, AGENT_ICONS } from "../frontend/src/AgentMark";
import { agentAvatarMode, showsAgentAvatar } from "../frontend/src/agentAppearance";

test("appearance defaults and unknown settings remain compatible with older embeds", () => {
 expect(agentAvatarMode()).toBe("hidden");
 expect(agentAvatarMode(undefined, "both")).toBe("both");
 expect(agentAvatarMode("unrecognized" as any)).toBe("hidden");
 for (const mode of ["hidden", "header", "threads", "both"] as const) {
  expect(showsAgentAvatar(mode,"header")).toBe(mode === "header" || mode === "both");
  expect(showsAgentAvatar(mode,"threads")).toBe(mode === "threads" || mode === "both");
 }
});

test("all shared icons follow theme tokens and unknown/missing icons fall back safely", () => {
 for (const icon of [...AGENT_ICONS.map(item=>item.id), undefined, "unknown"]) {
  const markup = renderToStaticMarkup(<AgentMark icon={icon} color="pink" size="sm"/>);
  expect(markup).toContain(`data-agent-icon="${icon && icon !== "unknown" ? icon : "robot"}"`);
  expect(markup).toContain("var(--accent)");
  expect(markup).toContain("var(--bg-input)");
  expect(markup).toContain("shrink-0");
  expect(markup).toContain('aria-hidden="true"');
  expect(markup).not.toContain("working-dot");
  expect(markup).not.toContain("unread-dot");
 }
});
