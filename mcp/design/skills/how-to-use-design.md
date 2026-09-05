# Design Studio

Use Design Studio when the result must be a measurable 2D profile, exact 3D solid, printable mesh, neutral CAD exchange file, or manufacturing handoff—not merely an image of an object.

## PCB-linked enclosures

When PCB Studio is bound as the optional `pcb_source`, use `design_enclosure_from_pcb` rather than manually copying board dimensions. It accepts a PCB design ID and optional revision ID, rejects failed mechanical validation, and creates a parametric base/lid enclosure with mounting bosses, fastener holes, panel cutouts, and immutable PCB provenance. Use `design_enclosure_refresh_from_pcb` to create a new Design revision from the current or specified PCB revision; never overwrite the existing enclosure or discard its source hash.

The generated parameters expose wall thickness, PCB clearance, floor/lid thickness, standoff height, top clearance, and lid-lip fit. Build a preview after generation, inspect connector access and clearances, then export STEP/STL/3MF. PCB Studio remains the authority for the board envelope; Design Studio remains the authority for enclosure geometry.

For a multi-part product, keep reusable geometry in `operations`, identify manufacturing outputs in `parts`, and place them with `assembly.instances`. Add materials, print profiles, interfaces, joints, purchased BOM rows, and `open_hardware` metadata. This produces colored assembly previews, mass properties, manufacturing checks, per-part exports, and a release-ready ZIP. Use `design_examples` and select `featured.open_rover` for a complete reference.

When a component should have its own revision history, create it as a separate design and use `assembly_create`, or set the assembly part's `source` to its `design_id`, immutable `revision_id`, and `source_sha256`; optionally select one named `part_id` from that revision. Builds resolve these links recursively but never follow a moving current revision. Use `assembly_sources_refresh` to check direct sources and create a new assembly revision with updated pins. The manufacturing package records the complete resolved graph in `dependencies.json`.

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

Supported primitives are `box`, `cylinder`, `sphere`, `extrude_rectangle`, `extrude_circle`, and `extrude_polygon`. Create rotational and routed solids with `revolve_profile` and `sweep_circle`. Combine geometry with `fuse`, `cut`, `intersect`, or `compound`; transform with `translate`, `rotate`, `scale`, or `mirror`; repeat with `linear_pattern` or `circular_pattern`; finish with `fillet` or `chamfer`.

Numeric fields accept arithmetic parameter expressions such as `width/2`, `thickness+2`, and `(diameter+clearance)/2`. Expressions have no function calls, property access, loops, file access, or network access.

Add checks to make requirements executable: `bounding_box`, `volume`, `surface_area`, `body_count`, `triangle_count`, or `face_count` with `min`, `max`, or `equals` as appropriate.

## Direct meshes and generative geometry

V1 produces direct meshes from exact parametric source. Future text-to-geometry or image-to-3D providers should enter as explicit mesh-source revisions, retain provider/model/provenance metadata, and never masquerade as editable B-rep CAD. Validate and repair those meshes before manufacturing. A manufacturing provider integration should consume the validated package, return quotes, and require human approval before placing a paid order.
