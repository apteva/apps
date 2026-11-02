import { describe, expect, test } from "bun:test";
import { canvasSize, proceduralClips, withClipParameters } from "./procedure-preview";

const edit = JSON.stringify({
  timeline: {
    tracks: [
      { type: "visual", clips: [{ uid: "plain", asset: { type: "image", src: "storage:1" }, start: 0, length: 2 }] },
      { type: "visual", clips: [{ uid: "scene", start: 2, length: 8, source_start: 1, asset: { type: "procedural", procedure: { procedure_id: 9, revision: 2, parameters: { title: "A" } } } }] },
    ],
  },
});

describe("procedure preview helpers", () => {
  test("finds procedural clips with timing and binding", () => {
    const clips = proceduralClips(edit);
    expect(clips).toHaveLength(1);
    expect(clips[0]).toMatchObject({ key: "1:0", uid: "scene", start: 2, length: 8, sourceStart: 1, procedureId: 9, revision: 2, parameters: { title: "A" } });
    expect(proceduralClips("{not json")).toEqual([]);
  });

  test("writes parameters back into the selected clip only", () => {
    const [ref] = proceduralClips(edit);
    const next = JSON.parse(withClipParameters(edit, ref, { title: "B" }));
    expect(next.timeline.tracks[1].clips[0].asset.procedure.parameters).toEqual({ title: "B" });
    expect(next.timeline.tracks[0].clips[0].asset).toEqual({ type: "image", src: "storage:1" });
  });

  test("canvas size mirrors the renderer's resolution table", () => {
    expect(canvasSize(JSON.stringify({ resolution: "fullhd", aspect: "16:9", fps: 30 }))).toEqual({ width: 1920, height: 1080, fps: 30 });
    expect(canvasSize(JSON.stringify({ resolution: "hd", aspect: "9:16" }))).toEqual({ width: 720, height: 1280, fps: 30 });
    expect(canvasSize("")).toEqual({ width: 1280, height: 720, fps: 30 });
  });
});
