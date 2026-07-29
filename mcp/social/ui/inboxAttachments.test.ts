import { describe, expect, test } from "bun:test";
import {
  inboxAttachmentCapabilities,
  parseInboxAttachments,
  storageAttachmentKind,
} from "./inboxAttachments";

describe("inbox attachments", () => {
  test("normalizes canonical and nested provider payloads", () => {
    expect(parseInboxAttachments([
      { kind: "audio", url: "https://cdn.example.test/a.mp3", mime: "audio/mpeg" },
    ])).toEqual([
      { kind: "audio", url: "https://cdn.example.test/a.mp3", mime: "audio/mpeg" },
    ]);
    expect(parseInboxAttachments({
      attachments: {
        data: [{ type: "video", payload: { url: "https://cdn.example.test/a.mp4" } }],
      },
    })[0]).toMatchObject({ kind: "video", url: "https://cdn.example.test/a.mp4" });
  });

  test("prefers provider account capabilities over native platform capabilities", () => {
    expect(inboxAttachmentCapabilities(
      { inbox_attachment_types: ["audio"], inbox_max_attachments: 1 },
      { dm_attachment_types: ["image", "video"], dm_max_attachments: 2 },
      "dm",
    )).toEqual({ types: ["audio"], max: 1 });
    expect(inboxAttachmentCapabilities(null, {
      dm_attachment_types: ["image", "audio"],
      dm_max_attachments: 1,
    }, "dm")).toEqual({ types: ["image", "audio"], max: 1 });
    expect(inboxAttachmentCapabilities(null, {
      dm_attachment_types: ["audio"],
      dm_max_attachments: 1,
    }, "comment")).toEqual({ types: [], max: 0 });
  });

  test("classifies storage content types", () => {
    expect(storageAttachmentKind("audio/mpeg")).toBe("audio");
    expect(storageAttachmentKind("application/pdf")).toBe("file");
  });
});
