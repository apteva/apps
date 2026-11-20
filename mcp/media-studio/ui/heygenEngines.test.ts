import { describe, expect, test } from "bun:test";
import {
  estimateHeyGenAvatarCostUSD,
  heygenAvatarEngineOptions,
  heygenEngineBlocked,
  heygenEngineChoices,
  heygenEngineLabel,
  heygenEngineMotion,
  heygenEngineResolutions,
} from "./mediaPanelLogic";

describe("HeyGen engine choices", () => {
  test("follow the look's supported_api_engines, including unknown engines", () => {
    const look = { supported_api_engines: ["avatar_iv", "heygen-avatar-6", "heygen-avatar-7"] };
    expect(heygenEngineChoices(look)).toEqual(["auto", "avatar_iv", "heygen-avatar-6", "heygen-avatar-7"]);
    expect(heygenEngineLabel("heygen-avatar-6")).toBe("Avatar 6");
    expect(heygenEngineLabel("heygen-avatar-7")).toBe("heygen-avatar-7");
  });

  test("looks that advertise nothing exclude gated engines", () => {
    const choices = heygenEngineChoices({ supported_api_engines: [] });
    expect(choices).toContain("avatar_iv");
    expect(choices).not.toContain("avatar_v");
    expect(choices).not.toContain("heygen-avatar-6");
  });

  test("no look selected offers every known engine", () => {
    expect(heygenEngineChoices()).toContain("heygen-avatar-6-turbo");
  });

  test("blocked when the look doesn't offer the engine", () => {
    expect(heygenEngineBlocked("heygen-avatar-6", { supported_api_engines: ["avatar_iv"] })).toBe(true);
    expect(heygenEngineBlocked("auto", { supported_api_engines: [] })).toBe(false);
  });
});

describe("HeyGen engine options", () => {
  test("Avatar 6 renders 1080p or 2K only; unknown engines allow everything", () => {
    expect(heygenEngineResolutions("heygen-avatar-6")).toEqual(["1080p", "2k"]);
    expect(heygenEngineResolutions("avatar_iv")).not.toContain("2k");
    expect(heygenEngineResolutions("heygen-avatar-7")).toContain("2k");
  });

  test("Avatar 6 sends motion_expressiveness, Avatar IV sends expressiveness", () => {
    expect(heygenAvatarEngineOptions("heygen-avatar-6", "  calm ", "less")).toEqual({
      engine: { type: "heygen-avatar-6" },
      motion_prompt: "calm",
      motion_expressiveness: "less",
    });
    expect(heygenAvatarEngineOptions("avatar_iv", "", "high")).toEqual({
      engine: { type: "avatar_iv" },
      expressiveness: "high",
    });
  });

  test("auto and Avatar V send no motion fields", () => {
    expect(heygenAvatarEngineOptions("auto", "wave", "high")).toEqual({});
    expect(heygenAvatarEngineOptions("avatar_v", "wave", "high")).toEqual({ engine: { type: "avatar_v" } });
    expect(heygenEngineMotion("heygen-avatar-6").maxChars).toBe(1200);
  });

  test("cost: Turbo 1080p is $0.05/s, unpriced engines give no estimate", () => {
    expect(estimateHeyGenAvatarCostUSD(10, "heygen-avatar-6-turbo", "photo_avatar", "1080p")).toBeCloseTo(0.5);
    expect(estimateHeyGenAvatarCostUSD(10, "heygen-avatar-6-turbo", "photo_avatar", "2k")).toBe(0);
    expect(estimateHeyGenAvatarCostUSD(10, "heygen-avatar-6", "photo_avatar", "1080p")).toBe(0);
    expect(estimateHeyGenAvatarCostUSD(10, "avatar_iv", "digital_twin", "4k")).toBeCloseTo(0.833);
  });
});
