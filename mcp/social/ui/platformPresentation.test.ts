import { expect, test } from "bun:test";
import { platformPresentation } from "./platformPresentation";

test("provides recognizable, stable platform marks and colors", () => {
  expect(platformPresentation("linkedin")).toEqual({
    key: "linkedin",
    label: "LinkedIn",
    mark: "in",
    color: "#4FA3E3",
  });
  expect(platformPresentation("twitter")).toEqual(platformPresentation("x"));
  expect(platformPresentation("Google Business Profile").key).toBe("googlebusiness");
});

test("unknown platforms receive a neutral readable fallback", () => {
  expect(platformPresentation("Mastodon")).toEqual({
    key: "mastodon",
    label: "Mastodon",
    mark: "MA",
    color: "#A1A1AA",
  });
});
