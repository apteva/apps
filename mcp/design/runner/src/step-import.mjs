// XDE imports run once; portable B-reps are consumed by the pinned modelling kernel.
import init from "opencascade.js/dist/opencascade.wasm.js";
import { join } from "node:path";
import { mkdir } from "node:fs/promises";
const request = JSON.parse(await Bun.stdin.text());
let stage = "initialize";
try {
  const oc = await init({
    wasmBinary: await Bun.file(
      join(import.meta.dir, "import.wasm"),
    ).arrayBuffer(),
  });
  const bytes = new Uint8Array(await Bun.file(request.input).arrayBuffer());
  const text = new TextDecoder().decode(bytes);
  if (!/^\s*ISO-10303-21;/i.test(text))
    throw new Error(
      "Not a STEP Part 21 file. Upload the original .step/.stp file, not HTML or an archive.",
    );
  const lengthUnits = new Set();
  for (const m of text.matchAll(
    /SI_UNIT\s*\(\s*(\.[A-Z]+\.|\$)\s*,\s*\.METRE\.\s*\)/gi,
  ))
    lengthUnits.add({ ".MILLI.": "mm", ".CENTI.": "cm", $: "m" }[m[1]] || m[1]);
  for (const entity of text.matchAll(/#\d+\s*=\s*([\s\S]*?);/g)) {
    const match = entity[1].match(/CONVERSION_BASED_UNIT\s*\(\s*'([^']+)'/i);
    if (
      match &&
      (/LENGTH_UNIT\s*\(/i.test(entity[1]) ||
        /^(inch|in|foot|feet|ft|millimet(?:re|er)|centimet(?:re|er)|met(?:re|er))s?$/i.test(
          match[1],
        ))
    )
      lengthUnits.add(match[1]);
  }
  const units = [...lengthUnits];
  if (!units.length)
    throw new Error(
      "No supported length-unit declaration found; supply a corrected manufacturer STEP with explicit units.",
    );
  if (/EXTERNALLY_DEFINED_REPRESENTATION|EXTERNAL_SOURCE\s*\(/i.test(text))
    throw new Error(
      "STEP references external geometry. Obtain a self-contained STEP assembly.",
    );
  stage = "write input";
  oc.FS.writeFile("input.step", bytes);
  const reader = new oc.STEPCAFControl_Reader_1();
  stage = "read STEP";
  const status = reader.ReadFile("input.step");
  if (status.value !== 1)
    throw new Error(
      `STEP reader failed (${status.value}); check that the file is a complete supported AP203/AP214/AP242 model.`,
    );
  const doc = new oc.TDocStd_Document(
    new oc.TCollection_ExtendedString_2("Design import", true),
  );
  stage = "transfer XDE";
  if (!reader.Transfer_1(new oc.Handle_TDocStd_Document_2(doc)))
    throw new Error("STEP contains no transferable geometry.");
  const st = oc.XCAFDoc_ShapeTool;
  const components = [],
    hierarchy = [];
  await mkdir(request.output, { recursive: true });
  function name(label) {
    const h = new oc.Handle_TDF_Attribute_1();
    if (!label.FindAttribute_1(oc.TDataStd_Name.GetID(), h)) {
      h.delete();
      return "";
    }
    const s = new oc.TCollection_AsciiString_13(h.get().Get(), 63);
    const result = Array.from({ length: s.Length() }, (_, i) =>
      String.fromCharCode(s.Value(i + 1)),
    ).join("");
    s.delete();
    h.delete();
    return result;
  }
  async function write(shape, file) {
    stage = "serialize " + file;
    const before = new Set(oc.FS.readdir("/"));
    const progress = new oc.Handle_Message_ProgressIndicator_1();
    if (!oc.BRepTools.Write_2(shape, "out.brep", progress))
      throw new Error("Failed to serialize imported solid.");
    progress.delete();
    // Use a short constant name: the legacy embind CString adapter is unreliable for long filenames.
    // The VFS is private to this one-shot process.
    // Locate the single new VFS file, then store it under our own stable ID.
    const created = oc.FS.readdir("/").filter((n) => !before.has(n));
    if (created.length !== 1)
      throw new Error("Import serializer did not create one B-rep file");
    stage = "read serialized " + file;
    const body = oc.FS.readFile(created[0]);
    oc.FS.unlink(created[0]);
    await Bun.write(join(request.output, file), body);
  }
  let pending = [];
  function walk(label, parentLocation, parentId = "", depth = 0) {
    if (depth > 32 || hierarchy.length >= 4096)
      throw new Error(
        "Assembly exceeds 32 levels or 4096 nodes; simplify the source model.",
      );
    const id = (parentId ? parentId + "_" : "component_") + label.Tag();
    const referred = new oc.TDF_Label();
    const isRef = st.GetReferredShape(label, referred);
    const target = isRef ? referred : label;
    const location = parentLocation.Multiplied(st.GetLocation(label));
    const title = name(label) || name(target) || id;
    const tr = location.Transformation();
    const transform = Array.from({ length: 3 }, (_, r) =>
      Array.from({ length: 4 }, (_, c) => tr.Value(r + 1, c + 1)),
    );
    hierarchy.push({ id, parent_id: parentId, name: title, transform });
    if (st.IsAssembly(target)) {
      const it = new oc.TDF_ChildIterator_2(target, false);
      for (; it.More(); it.Next())
        if (st.IsComponent(it.Value()))
          walk(it.Value(), location, id, depth + 1);
      it.delete();
    } else {
      const shape = st.GetShape_2(label);
      if (!shape.IsNull()) {
        const placed = shape.Moved(parentLocation);
        const file = id + ".brep";
        pending.push(write(placed, file));
        components.push({
          id,
          name: title,
          parent_id: parentId,
          file,
          transform,
        });
      }
    }
  }
  const root = oc.XCAFDoc_DocumentTool.ShapesLabel(doc.Main());
  const it = new oc.TDF_ChildIterator_2(root, false);
  for (; it.More(); it.Next())
    if (st.IsShape(it.Value()) && st.IsFree(it.Value()))
      walk(it.Value(), new oc.TopLoc_Location_1());
  if (!components.length)
    throw new Error("No solid or surface components found.");
  await Promise.all(pending);
  console.log(
    JSON.stringify({
      ok: true,
      source_units: units,
      units: "mm",
      importer: "occt-xde-7.4.0p1-v1",
      components,
      hierarchy,
      diagnostics: [
        "Original STEP retained. XDE assembly names and placements retained; non-ASCII name characters are replaced with ?. No automatic geometry repair is applied. Topology is validated separately by the modelling kernel.",
      ],
    }),
  );
  process.exit(0);
} catch (e) {
  console.log(
    JSON.stringify({
      ok: false,
      error: stage + ": " + (e instanceof Error ? e.message : String(e)),
    }),
  );
  process.exit(1);
}
