# Design Studio changes

## 0.11.0

- PCB enclosures (generator version 3) are separate parts: `base`, `lid` and the PCB as a `reference` part. The lid is stored outside face down, ready to print, and the assembly places it with a `seat`-on-`rim` mate. Existing enclosure revisions are unchanged; refreshing one creates a revision in the new layout.
- Enclosure magnets and pogo windows can go on the side walls (`face: north|south|east|west`, `along_mm`, `z_mm`); wall magnets open from the outside by default (`from: inside|outside`), keep 0.4 mm of material and must sit on the flat part of a box or rounded wall. Magnets take a `polarity` and an `id`.
- Every enclosure magnet and window becomes a mechanical interface on its part (`magnet` with polarity and diameter, `pogo_window`).
- Linked parts carry their source's interfaces as `<part>/<interface>`, resolved with the link's parameters; whole-design links carry interfaces of parts placed once, moved by that placement.
- New automatic `mate_compatibility` check: facing magnets must have opposite poles (fail), matching diameters (warning), and a magnet on a joined face without a partner is a warning; `parameters.mates_with` restricts the kinds an interface mates to.
- Each linked design is built once per distinct parameter set per build and reused; the report shows `component_builds`.
- Cutting or joining an imported STL adds a `mesh_derived` warning naming the operations.
- Moulding checks say when the undercut search was sampled, with `undercut_samples` (16-8192) to search more densely.
- Fix: binary STLs with zero facet normals (allowed by the format) failed to load for some coordinates; the runner now reads a copy with the normals filled in.
- Engine version `ds0.11`.

## 0.10.0

- STL import as reference geometry: `design_stl_import` (binary or ASCII, up to 50000 triangles, unit scaling) creates a reference design with an `import_stl` operation. The mesh becomes B-rep faces, so it can be placed, measured and clearance-checked exactly; it is never exported for printing.
- Text profiles with bundled DejaVu fonts (sans, sans-bold, mono; alignment), and `svg_path` profiles supporting M/L/H/V/C/S/Q/T/A/Z with nested subpaths as holes.
- GLB exports carry one node and PBR material per visible part in its colour; 3MF exports carry the part colour and material name as a base material.
- New `dxf` and `pdf` export formats: an A4/A3 sheet with front, top and right views, dashed hidden lines, a standard scale and overall size.
- New `moulding` exact check: draft angle per face against `min_draft_deg` and undercuts along the pull direction, by mesh normals and ray casting.
- Engine version `ds0.10`.

## 0.9.0

- Place parts by mating interfaces. Mechanical interfaces gain an `up` vector; an instance can use `mate: {interface, to_instance, to_interface, gap, angle, flip}` instead of a transform. Mates chain, and conflicts are rejected when saving: unknown or wrong-part interfaces, two mates on one interface, transform plus mate, alignment plus mate, two joints on one child, and placement cycles.
- Joint states. Revolute and prismatic joints take a `state` (number or expression, within `limits`); origin and axis are in the parent part's coordinates, and the child plus everything placed relative to it moves.
- New `joint_sweep` exact collision check samples a joint through its range and reports the states where moving parts hit fixed ones.
- PCB enclosures: `shape` rounded (default for new enclosures), outline (follows the PCB outline) or box (v1; kept by refreshes of existing enclosures); `lid: "snap"` with snap bumps and wall grooves; `magnets` (blind pockets in floor or lid with a minimum skin); `pogo_windows` (rounded through-windows). Generator version 2.
- Profiles accept `offset` to grow or shrink an outline.
- Engine version `ds0.9`.

## 0.8.0

- Fillet and chamfer on selected edges. `edges` takes a selector by description (side of the shape, direction, curve type, plane, box, nearby point, length, `not`, lists for "any of"), instead of always rounding every edge. Selecting nothing, or an unknown selector key, is a clear error.
- `shell` hollows a solid with chosen open faces.
- `extrude_profile` with rounded rectangles, slots, circles, regular polygons, polygons and paths with three-point, sagitta and tangent arcs; profiles can carry holes and be rotated.
- `hole` feature: one or many positions, any direction, through or blind, counterbore or countersink, and metric threads M2-M8 as clearance, tap-drill or heat-set insert sizes.
- `loft` between two or more profile sections, smooth or ruled.
- Acceptance test: a Pocket-style shell (rounded body, softened top edges, open-bottom shell, corner bosses, USB-C slot, countersunk lanyard hole, insert holes) builds as one valid solid.
- Engine version `ds0.8`.

## 0.7.0

- Richer formulas. Expressions gain `%`, comparisons, `and`/`or`/`not` (`&& || !`), `cond ? a : b`, `true`/`false`/`pi`, and the functions `min`, `max`, `abs`, `round(x[, step])`, `floor`, `ceil`, `sqrt`, `pow`, `clamp`, `hypot` and degree-based trigonometry. The parser lives in `runner/src/expressions.mjs` with shared cases in `testdata/expressions.json`.
- Parameter types `integer` and `boolean` (`true`/`false` stored as 1/0), and `options` (allowed values) on any numeric parameter. Validated both when saving and when building.
- Optional features: any operation can carry `when`. Skipped operations drop out of fuse, cut tools, intersect and compound; using one any other way is an error.
- New `component` operation brings another design's pinned geometry in as a normal shape, so it can be cut, fused or patterned. Linked parts and component operations accept per-link `parameters` overrides: numbers are range-checked against the source, strings are expressions over the owning design's parameters.
- New tools `component_dependents` (which designs link to a design, and which pins are stale) and `component_sources_refresh_all` (one new revision per linked design, transitive by default, failures reported per design). `assembly_sources_refresh` also advances component operations.
- Engine version `ds0.7`; revisions are re-hashed against it. Cached STEP import geometry from earlier ds0.x engines is still reused.

## 0.6.2

- Wait for Shapeways to finish analysing uploaded models before reading prices. Shapeways returns its material list while processing with every material marked not printable, so quotes came back with no options. The adapter now polls until `printable` is final, and reports "still processing" or "cannot print in any material" instead of an empty quote.
- Works with the Shapeways connector's client-credentials sign-in (integrations 0.56.2).

## 0.6.1

- Explain Craftcloud's partner-access requirement instead of passing on a raw HTTP error. Craftcloud's public API has no model upload, so when the upload has no partner API URL, is refused (401/403) or hits a host without upload (404), the quote reports that Craftcloud needs partner model API access and where to set it on the connection. Other upload errors are reported as before.
- Works with the updated Craftcloud connector, which sends upload_model and get_model to the connection's optional partner model API URL and key.

## 0.6.0

- Add an optional `fabrication_provider` integration role (multiple connections). Bind any of Craftcloud, Slant 3D, Shapeways, Treatstock and Sculpteo; each has an adapter behind one `FabricationProvider` interface (quote, prepare, submit, status, cancel).
- `fabrication_quote` builds STL exports for the printed parts of a passing revision, sends only those files to every bound service in parallel, and stores options ranked by total. Provider failures are recorded per connection without failing the quote.
- Orders are drafts first. `fabrication_order_prepare` re-prices the option at the provider and checks the operator's policy: auto-order switch, per-order maximum, monthly budget, allowed services, currency, and price drift over 5% since quoting. Outside the policy the order waits for a person.
- Approval, decline and the policy/addresses settings exist only as panel HTTP routes, never as MCP tools. Approval must restate the exact total and records the signed platform principal when present.
- `fabrication_order_submit` (new `fabrication.order` permission) moves an approved order to submitting atomically and calls the provider once. An ambiguous provider error marks the order `submit_unknown` instead of retrying.
- Pin app-sdk v0.116.0. A `fabrication-status` worker polls active orders every 30 minutes. The panel gains a Fabrication tab with quotes, orders and approval, and policy/address/service settings.

Craftcloud's public v5 API has no model upload endpoint, so live Craftcloud quotes depend on the integration connector's upload path. Craftcloud orders end in `awaiting_payment` unless invoice payment is configured. Treatstock has no status API; Sculpteo orders hold at most three parts.

## 0.5.0

- Import original manufacturer STEP as immutable, project-scoped source assets with XDE component names, hierarchy, placements, declared units, measured bounds, attribution and dimensional evidence. Normalize origins/orientation explicitly. Keep imported purchased parts out of manufacturing outputs.
- Check solid intersections, containment, exact surface distances, engineering clearances and explicit contact/ignored pairs. Return closest points and indeterminate results for unsupported geometry. Add owner-relative keepouts and conservative insertion/removal envelopes.
- Inspect assemblies with depth-rendered selection, component visibility, isolation, transparency, section planes, standard views, fit selection, exact instance/face measurements and clearly labelled approximate point measurements. Link validation issues to affected geometry.
- Generate editable multi-board enclosure templates without PCB Studio: front shell, rear cover, lip, measured display opening/retention features, screw bosses/bores, supports, brackets, cradle and service/ventilation cutouts. Add numeric and datum placement plus printer/material fit profiles.
- Separate declared FDM rules from sampled geometry checks for wall thickness, feature spans and overhangs. Sampled violations identify regions; a clean sample remains indeterminate.
- Export unique oriented manufacturing parts separately from reference assembly STEP/GLB. Archive resolved source dependencies, original/normalized assets, checksums, validation, evidence, BOM and depth-rendered orthographic inspection images with a database-independent replay tool.
- Gate verified packages on complete evidence, named passing checks, resolved assumptions, instructions and revision-bound physical-fit/thermal test records. Prototype exports remain explicitly identified. Fix hidden instances being omitted from physical validation.
- Pin app-sdk v0.115.0. Include pinned import WASM, dependency notices and licence files. Increase the geometry timeout default to 300 seconds and memory limit to 3 GiB for manufacturer assemblies.

Manufacturer STEP is a geometric reference, not original editable feature history. Non-ASCII imported names currently use an ASCII fallback. Flexible routing, RF/thermal simulation, automatic constraint solving, complete thin-feature recognition and slicer certification are outside this release. Physical measurements and tests are still required before Pocket's enclosure can be verified.
