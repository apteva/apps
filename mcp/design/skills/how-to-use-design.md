# Design Studio

Use Design Studio when the result must be a measurable 2D profile, exact 3D solid, printable mesh, neutral CAD exchange file, or manufacturing handoff—not merely an image of an object.

## PCB-linked enclosures

When PCB Studio is bound as the optional `pcb_source`, use `design_enclosure_from_pcb` rather than manually copying board dimensions. It accepts a PCB design ID and optional revision ID, rejects failed mechanical validation, and creates a parametric base/lid enclosure with mounting bosses, fastener holes, panel cutouts, and immutable PCB provenance. Use `design_enclosure_refresh_from_pcb` to create a new Design revision from the current or specified PCB revision; never overwrite the existing enclosure or discard its source hash.

Enclosure options: `shape` is `rounded` for new enclosures (corners follow `corner_radius_mm` or the board's corner radius), `outline` to follow the PCB outline exactly, or `box`. `lid: "snap"` adds snap bumps on the lip and grooves in the base walls (`snap_mm`, box/rounded only). `magnets` adds blind pockets opened on the inside of the floor or lid (`part: base|lid`, at least 0.4 mm skin is kept), and `pogo_windows` adds rounded through-windows in the floor or lid. Positions are in PCB board coordinates. Connector openings come from PCB Studio's `panel_openings`; side openings assume the connector sits on the outline's bounding edge.

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

Supported primitives are `box`, `cylinder`, `sphere`, `extrude_rectangle`, `extrude_circle`, and `extrude_polygon`. Create rotational and routed solids with `revolve_profile` and `sweep_circle`. Combine geometry with `fuse`, `cut`, `intersect`, or `compound` (and bring in other designs with `component`); transform with `translate`, `rotate`, `scale`, or `mirror`; repeat with `linear_pattern` or `circular_pattern`; finish with `fillet` or `chamfer`.

Numeric fields accept parameter expressions such as `width/2`, `(diameter+clearance)/2`, `max(wall*2, 3)`, `clamp(width-2*margin, 10, 80)` or `has_vent and width > 40 ? 6 : 0`. Expressions support `+ - * / %`, comparisons, `and`/`or`/`not` (or `&& || !`), `cond ? a : b`, `true`/`false`/`pi`, and the functions `min`, `max`, `abs`, `round(x[, step])`, `floor`, `ceil`, `sqrt`, `pow`, `clamp`, `hypot`, and `sin`/`cos`/`tan`/`asin`/`acos`/`atan`/`atan2` in degrees. Booleans are 1/0. There is no property access, assignment, loop, file or network access.

Parameter types are `length` (default), `number`, `angle`, `integer` (whole numbers, for counts) and `boolean` (`true`/`false`). Any numeric type may list `options` (allowed values) alongside `min`/`max`.

Make features optional with `when` on any operation (a boolean, number, or expression such as `"has_vent"` or `"width > 60"`). A skipped operation drops out of `fuse`, `cut` tools, `intersect` and `compound` inputs; using it any other way (as a cut base, a transform input, or an output) is an error, so the mistake is visible.

## Mates and joints

Give parts named mechanical interfaces in `assembly.interfaces`: `{id, kind, part_id, position, direction, up}` in the part's own coordinates. `direction` is the outward normal of the mating face; `up` fixes the turn about it (default: world +Z projected onto the face, or +Y for horizontal faces). Place an instance with `mate: {interface, to_instance, to_interface, gap, angle, flip}` instead of a transform: the faces oppose, the up vectors align, then `gap` (mm along the target normal) and `angle` (degrees about it) apply. Mates chain: a part mated to a mated part follows it. An instance uses one of transform, alignment or mate; an interface takes one mate; cycles are rejected.

Joints (`revolute`, `prismatic`, `fixed`) take `origin` and `axis` in the parent instance's part coordinates and a `state` (degrees or mm, number or expression, within `limits`). The child, and everything placed relative to it, moves with the state, so a parameter can open a lid. Add a `joint_sweep` check (`joint`, optional `from`/`to` defaulting to the limits, `steps`, `min` clearance, `ignore_pairs`) to test collisions through the range; parent-child contact at the hinge is allowed.

## Edges, shells, profiles, holes and lofts

`fillet` and `chamfer` take an optional `edges` selector; without it every edge is rounded. Selectors pick by description instead of index, so they survive parameter changes: `{"side":"top"}` (edges in the shape's top plane), `{"direction":"Z"}` (vertical edges), `{"curve":"circle"}`, `{"in_box":[[x,y,z],[x,y,z]]}`, `{"near":{"point":[x,y,z],"distance":1}}`, `{"in_plane":{"plane":"XY","offset":"height"}}`, `{"not":…}`. Keys in one object must all match; a list matches any entry. A selector that finds nothing is an error. Large radii can fail where faces are too small; the error says so.

`shell` hollows a solid inward by `thickness`, removing the faces chosen by `open_faces` (same selector keys for faces, plus `surface`). Fillet the outside before shelling so the inside follows.

`extrude_profile` extrudes a 2D profile on `XY`, `XZ` or `YZ` at `origin`: `rounded_rectangle`, `slot` (length end to end), `circle`, `regular_polygon`, `polygon`, or `path` with `line`, three-point `arc`+`via`, `arc`+`sagitta` and `tangent_arc` segments. Profiles accept `center`, `angle`, and `holes` (more profiles cut out). `loft` blends two or more single-outline sections at different `offset`s (`ruled: true` for straight sides).

`hole` cuts at one `position` or many `positions` on a surface, drilling along `direction` (default `[0,0,-1]`). Give `diameter` or `thread: {size: "M3", mode: "clearance"|"tap"|"insert"}`; omit `depth` for a through hole (inserts default to the insert length plus 1 mm). Add `counterbore: {diameter, depth}` or `countersink: {diameter, angle}`. Insert sizes are typical values; check the insert's datasheet.

## Reusable components and variants

To reuse one design's geometry inside another as an ordinary shape (to cut openings into a shared base, fuse a boss onto it, or pattern it), add a `component` operation: `{"id":"body","type":"component","source":{"design_id":12,"revision_id":34,"source_sha256":"…","part_id":"optional"},"parameters":{"width":"size","depth":30},"transform":{…}}`. `parameters` overrides the source revision's values for this link only: numbers are checked against the source's min/max/options, and strings are expressions over this design's parameters. Linked assembly parts (`part.source`) accept the same `parameters`.

Build variants as separate designs that each start from the shared base with a `component` operation and add their own cuts. After changing the base, call `component_dependents` to see which designs link to it and which pins are stale, then `component_sources_refresh_all` to give every linked design a new revision pinned to the base's current revision (transitively by default). Failures, such as an override now outside a changed range, are reported per design and do not stop the others; fix those and refresh again.

Add checks to make requirements executable: `bounding_box`, `volume`, `surface_area`, `body_count`, `triangle_count`, or `face_count` with `min`, `max`, or `equals` as appropriate.

## Direct meshes and generative geometry

V1 produces direct meshes from exact parametric source. Future text-to-geometry or image-to-3D providers should enter as explicit mesh-source revisions, retain provider/model/provenance metadata, and never masquerade as editable B-rep CAD. Validate and repair those meshes before manufacturing. 

## Purchased assemblies (v0.5)

Use `design_step_import` for manufacturer STEP (large files: HTTP multipart `/api/sources`). Record exact model/revision, source, uncertainty, omissions and measurements needed; do not mark incomplete CAD as verified fit. Its returned design is purchased, not printable. Build assembly links with `assembly_create`; author measured proxy solids for missing features. Pins include original source and normalized geometry hashes. Normalize component orientation with an explicit `import_step.transform`; never resize a purchased component to make it fit.

Use exact `part_clearance` and `assembly_collision` checks with named instance IDs and stable check IDs. Explicitly list intended contact/ignored pairs. `indeterminate` and `not_checked` are not passes. Keepouts are named operation solids attached to `assembly.keepouts` with owner, purpose, source, required clearance and optional targets. Use conservative `swept_box` volumes for plug insertion, connector levers and service travel. Hiding a component does not exclude it physically.

`design_measure` evaluates exact instance/face distances in the selected revision; mesh point picks are approximate. `design_enclosure_from_assembly` creates the front-shell/rear-cover template without PCB Studio. Position display opening/retention, screw bosses, supports, cradle, brackets and service openings from measured datums. Use `fit_profile_id`, parameter `fit` keys and printer/material `fits_mm` allowances, then calibrate with physical coupons.

Treat `manufacturing_rules` as declared rules. `print_geometry` samples thickness/feature spans/overhangs and reports its limits; absence of sampled defects is not certification. Export unique oriented parts for printing. Assembly STEP/GLB include reference components; assemblies no longer produce a combined print mesh.

Create prototype manufacturing packages while assumptions remain. A verified package requires named passing checks, complete component evidence, resolved assumptions and assembly/service/fastening instructions plus actual physical-fit and enclosed-thermal evidence pinned to the exact revision/hash through `design_verification_record`. Never fabricate physical test records. The ZIP contains replayable resolved source/assets and evidence. The Pocket 15 mm appearance study is not a dimensional requirement; size around the complete measured assembly and the current BOM.

## Print-service quotes and orders (v0.6)

When print services are bound (`fabrication_providers` lists them and the policy), quote a passing revision with `fabrication_quote` (design_id, optional revision_id, part_ids, quantity or per-part quantities). Only printed parts are sent. Compare options by total, lead time and material; say which services failed and why.

To order, call `fabrication_order_prepare` with quote_id, option_id and a saved address_id. Report the provider-confirmed total. If the status is `awaiting_approval`, tell the user to approve it in the Design panel's Fabrication tab and stop; never try to change the policy or approve it yourself. Call `fabrication_order_submit` only for an `approved` order the user wants placed. If an order is `submit_unknown`, do not resubmit: ask the user to check with the provider. Use `fabrication_orders` with refresh=true to report progress.
