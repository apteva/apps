# Design Studio

Use Design Studio when the result must be a measurable 2D profile, exact 3D solid, printable mesh, neutral CAD exchange file, or manufacturing handoff—not merely an image of an object.

## Mental model

Every design is an immutable chain of revisions. A revision contains:

- an `apteva-design/v1` declarative operation graph;
- resolved numeric parameters in millimetres;
- an exact OpenCascade B-rep build report;
- validation checks and derived artifacts.

The format is intentionally data, not Python or arbitrary JavaScript. Agents can generate and edit it reliably; the app validates the vocabulary before a sandboxed one-shot geometry runner sees it.

Start with `design_examples`. Then:

1. Call `designs_create` with a name and definition.
2. Call `design_validate` and inspect bounds, volume, face count, and checks.
3. To change it, call `revisions_create` with the current revision as `expected_parent_id`. You may replace only parameters and inherit the operation graph.
4. Call `design_render` for a mesh/GLB preview.
5. Call `design_export` for STEP, STL, 3MF, or GLB.
6. Call `manufacturing_package_create` only after validation passes.

## Choosing formats

- STEP: exact solids and the best V1 handoff to CAD/CAM or a manufacturer.
- 3MF: unit-aware 3D-print package.
- STL: ubiquitous triangle mesh; units are conventionally millimetres here.
- GLB: fast browser and review preview.
- Manufacturing ZIP: source, parameters, report, checks, hashes, and all production files together.

## Geometry graph

Supported primitives are `box`, `cylinder`, `sphere`, `extrude_rectangle`, `extrude_circle`, and `extrude_polygon`. Combine them with `fuse`, `cut`, `intersect`, or `compound`; transform with `translate`, `rotate`, or `scale`; finish with `fillet` or `chamfer`.

Numeric fields accept arithmetic parameter expressions such as `width/2`, `thickness+2`, and `(diameter+clearance)/2`. Expressions have no function calls, property access, loops, file access, or network access.

Add checks to make requirements executable: `bounding_box`, `volume`, `surface_area`, `body_count`, `triangle_count`, or `face_count` with `min`, `max`, or `equals` as appropriate.

## Direct meshes and generative geometry

V1 produces direct meshes from exact parametric source. Future text-to-geometry or image-to-3D providers should enter as explicit mesh-source revisions, retain provider/model/provenance metadata, and never masquerade as editable B-rep CAD. Validate and repair those meshes before manufacturing. A manufacturing provider integration should consume the validated package, return quotes, and require human approval before placing a paid order.
