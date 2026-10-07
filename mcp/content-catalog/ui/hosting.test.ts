import { expect, test } from "bun:test";
import { hostingNotice, hostingPending, transcodingMessages } from "./hosting";

test("checksum waiting explains automatic resume and failure", () => {
  expect(hostingNotice({ pending_checksum: true, checksum_status: "running" })).toContain("Waiting for checksum verification (running)");
  expect(hostingNotice({ pending_checksum: true })).toContain("resume automatically");
  expect(hostingNotice({ checksum_status: "failed" })).toContain("has not started");
  expect(hostingNotice({ warning: "provider result uncertain" })).toBe("provider result uncertain");
});
test("poll only unfinished requests, preserving terminal and uncertain states", () => {
  expect(hostingPending([{ status: "processing" }], [])).toBe(true);
  expect(hostingPending([], [{ status: "waiting_checksum" }])).toBe(true);
	// A checksum resume can be between reserving and receiving fetch_video's
	// response. Keep the dialog fresh through that transient submitted phase.
	expect(hostingPending([{id:"h",status:"reserved"}],[{status:"submitted",hosting_id:"h"}])).toBe(true);
	expect(hostingPending([{id:"h",status:"reserved"}],[{status:"blocked",hosting_id:"h"}])).toBe(false);
  expect(hostingPending([{ status: "ready" }, { status: "failed" }, { status: "uncertain" }], [{ status: "cancelled" }, { status: "blocked" }])).toBe(false);
});
test("render provider messages without discarding unknown structured messages", () => {
  expect(transcodingMessages([{ message: "Encoding 1080p" }, { stage: "audio" }, "Finished"])).toEqual(["Encoding 1080p", '{"stage":"audio"}', "Finished"]);
  expect(transcodingMessages(null)).toEqual([]);
});
