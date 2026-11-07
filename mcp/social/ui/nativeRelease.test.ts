import { describe, expect, test } from "bun:test";
import { nativeReleaseLabel, postReleaseLabel, type NativeRelease } from "./nativeRelease";

const at = "2026-10-28T19:00:00Z";
const scheduled: NativeRelease = { state: "scheduled", visibility: "private", release_at: at, source: "observed", observed_at: "2026-10-28T10:00:00Z" };
const publicRelease: NativeRelease = { state: "public", visibility: "public", source: "observed" };

describe("nativeReleaseLabel", () => {
  test("public keeps the ordinary published status", () => {
    expect(nativeReleaseLabel(publicRelease)).toBeNull();
    expect(nativeReleaseLabel(undefined)).toBeNull();
  });
  test("scheduled shows YouTube release time, not Published", () => {
    const label = nativeReleaseLabel(scheduled)!;
    expect(label.label).toBe(`Scheduled on YouTube · ${new Date(at).toLocaleString()}`);
    expect(label.title).toContain("Read from YouTube");
  });
  test("private and unlisted say uploaded, not public", () => {
    expect(nativeReleaseLabel({ state: "private", visibility: "private", source: "requested" })!.label).toBe("Uploaded · private");
    expect(nativeReleaseLabel({ state: "unlisted", visibility: "unlisted", source: "requested" })!.title).toContain("refresh from YouTube");
  });
  test("passed release asks for confirmation", () => {
    expect(nativeReleaseLabel({ ...scheduled, state: "release_due" })!.label).toStartWith("Release time passed");
  });
});

describe("postReleaseLabel", () => {
  const target = (native_release: NativeRelease | null, status = "published") => ({ status, native_release });
  test("single scheduled YouTube target relabels the post", () => {
    expect(postReleaseLabel({ status: "published", targets: [target(scheduled)] })!.label).toStartWith("Scheduled on YouTube");
  });
  test("a public target elsewhere keeps the post Published", () => {
    expect(postReleaseLabel({ status: "published", targets: [target(scheduled), target(null)] })).toBeNull();
  });
  test("mixed non-public targets summarize as not public", () => {
    const priv: NativeRelease = { state: "private", visibility: "private", source: "observed" };
    expect(postReleaseLabel({ status: "published", targets: [target(scheduled), target(priv)] })!.label).toBe("Uploaded · not public");
  });
  test("non-delivered posts are untouched", () => {
    expect(postReleaseLabel({ status: "scheduled", targets: [target(scheduled, "scheduled")] })).toBeNull();
  });
});
