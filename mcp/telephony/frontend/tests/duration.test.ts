import {expect, test} from "bun:test";
import {callTerminationLabel} from "../src/client";

test("duration expiry has a specific localized label, separate from failure", () => {
  expect(callTerminationLabel({reason:"time_limit"},"fr-FR")).toBe("Durée maximale atteinte");
  expect(callTerminationLabel({reason:"time_limit"},"en-GB")).toBe("Maximum call duration reached");
  expect(callTerminationLabel({reason:"failed"},"fr")).not.toBe("Durée maximale atteinte");
  expect(callTerminationLabel(undefined,"fr")).toBe("");
});
