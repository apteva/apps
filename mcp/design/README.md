# Design Studio

Design Studio is Apteva's agent-native parametric 2D/3D CAD app. It stores safe `apteva-design/v1` operation graphs as immutable revisions, evaluates them with Replicad + OpenCascade.js, validates exact geometry, renders browser meshes, and exports STEP, STL, 3MF, GLB, and traceable manufacturing ZIPs.

Version 0.5 adds manufacturer STEP import, solid fit checks, assembly inspection, multi-board enclosure templates and reproducible release evidence. The workflow and limitations are documented below.

Version 0.4 introduced reusable component documents. An assembly part may use a local operation `output` or a `source` containing another Design Studio design ID, immutable revision ID, SHA-256 source hash, and optional named part ID. Builds recursively materialize pinned dependencies, reject hash mismatches and cross-project links, and detect dependency cycles. `assembly_create` starts a linked assembly and `assembly_sources_refresh` advances direct links only by creating a new immutable assembly revision. Manufacturing ZIPs include `dependencies.json`, while per-part artifact metadata retains component provenance.

Version 0.3 added the backward-compatible open-hardware assembly layer. A definition may declare named parts, materials, FDM profiles, transformed assembly instances, reusable mechanical interfaces, fixed/revolute/prismatic joints, purchased BOM items, SPDX licensing, source links, and assembly documentation. The viewer renders assembly parts in their declared colors. STEP carries named assembly instances, while STEP/STL/3MF exports also include one manufacturing-oriented file per unique printable or fabricated part.

`design_examples` includes the complete Apteva Open Rover reference: a printable four-wheel skid-steer chassis with PETG structural parts, TPU tires, wheel hubs, mounting-grid deck, sensor fascia, payload rails, motor interfaces, revolute wheel joints, print profiles, BOM, and CERN-OHL-S-2.0 release metadata.

Version 0.2 added the optional native PCB Studio binding. `design_enclosure_from_pcb` consumes a validated `apteva-mechanical-envelope/v1` response and creates a parametric two-part enclosure with a hollow base, locating-lip lid, PCB standoffs, fastener bores, and side/top/bottom panel openings. The generated definition records the PCB design ID, revision ID, revision number, source hash, generator version, and enclosure options. `design_enclosure_refresh_from_pcb` never mutates geometry in place: it fetches a newer PCB revision and creates an explicit immutable Design revision while retaining user parameter overrides when they remain valid.

It does not execute user-authored Python or JavaScript. Numeric geometry fields support a deliberately small arithmetic-expression language over declared parameters.

## Architecture

- Go + `app-sdk`: project isolation, MCP tools, HTTP API, revisions, build runs, artifacts, validation, and Storage integration.
- Replicad/OpenCascade.js: exact B-rep geometry in a one-shot Bun process.
- Embedded JS/WASM assets: the Go sidecar remains deployable as one binary.
- Pinned Bun fallback: use configured `bun_path`, then `PATH`, then a SHA-256-verified Bun 1.2.22 download into the app data directory.
- React panel: source editing, parameter controls, depth-tested WebGL 3D preview, measurements, checks, exports, and artifact downloads.
- PCB handoff: optional bound-app call to PCB Studio, strict datum/schema/provenance validation, parametric enclosure generation, and upstream refresh controls.
- Open hardware: assembly/part identity, materials, interfaces, joints, BOM, licenses, build instructions, per-part exports, and print profiles remain in the immutable source revision.
- Assembly-aware validation: mass/centre of mass, ground and part clearances, collision candidates, print-volume fit, declared FDM wall/feature/overhang rules, and release completeness.

## Open-hardware assemblies

The existing operation graph remains the source of exact geometry. `parts` gives selected operation outputs stable product identities; `assembly.instances` places those parts without duplicating their source geometry. This separation makes four wheel instances one printable wheel design plus an explicit quantity in the BOM.

For SolidWorks-style document reuse, a part can replace `output` with `source: {design_id, revision_id, source_sha256, part_id?}`. The source design can be a standalone part, a named part inside another document, or a complete subassembly. Assembly-only documents may have an empty `operations` array and no top-level `output`. Pins never move during build; refreshing component sources creates a new revision, so an old product release remains reproducible.

Mechanical `interfaces` describe stable datums such as motor shafts, bearing seats, fastener grids, and PCB envelopes. `joints` describe product intent and motion axes without turning Design Studio into a physics simulator. Supported joint types are `fixed`, `revolute`, and `prismatic`.

The operation vocabulary now also includes `revolve_profile`, `sweep_circle`, `mirror`, `linear_pattern`, and `circular_pattern`. Pattern operations are bounded to 128 instances and the complete operation graph remains subject to the configured safety limit.

Manufacturing checks are deliberately auditable. Print wall, feature, and overhang values are declared by the part author and checked against its selected profile; Design Studio does not pretend these values were inferred when the current kernel cannot prove them geometrically. Version 0.5 uses exact solid clearance/intersection checks after a conservative bounding-box prefilter; unsupported geometry is reported as indeterminate.

## Develop

```sh
cd runner
bun install
bun run build

cd ..
go test ./...

cd ../../..
bun run scripts/build-panels.ts --app design
```

The engine integration test performs a real OpenCascade boolean build and writes STEP/STL/mesh output. It skips only when Bun is not available.

## PCB enclosure workflow

1. In PCB Studio, validate a revision with `pcb_mechanical_validate`.
2. Bind that PCB Studio install to Design Studio's optional `pcb_source` role.
3. Call `design_enclosure_from_pcb` with `pcb_design_id` and an optional immutable `pcb_revision_id`.
4. Adjust wall, clearance, floor, lid, standoff, and lip parameters in Design Studio, then build and export normally.
5. When the PCB changes, call `design_enclosure_refresh_from_pcb`. The new PCB source hash is recorded in a new Design revision.

Failed, unversioned, untraceable, or unsupported mechanical envelopes are rejected. Design Studio never reads PCB databases or files directly and never silently follows a moving PCB revision.

## V1 boundary

V1 generates both exact solids and derived triangle meshes from a deterministic parametric source. External text-to-3D/image-to-3D models and paid manufacturing providers are intentionally adapter seams, not bundled providers. A future mesh-source revision should retain model/provider/prompt provenance and run mesh repair checks; a manufacturing adapter should quote from a validated package and require an explicit human approval before ordering.

## Version 0.5 — purchased assemblies and enclosure verification

### STEP import

Use **Import STEP** in the panel or POST multipart `file`, `evidence` (JSON) and optional `name` to `/api/sources`. The maximum source file is 96 MiB. Large manufacturer assemblies can require multiple GiB during evaluation; the manifest allows up to 3 GiB. MCP `design_step_import` accepts `filename`, `content_base64`, `evidence` and optional `name`; HTTP multipart is preferable for large files. Evidence requires `model`, `geometry_status` (`manufacturer_cad`, `drawing_derived`, `measured`, `estimated`); it also records manufacturer, hardware revision, source URL, date, attribution, licence, known omissions, uncertainty in mm, and measurements still needed. `complete` defaults to false.

The separate, pinned OCCT XDE reader retains component names, nested hierarchy and placements. Names currently use an ASCII fallback for non-ASCII characters. Declared STEP length units are converted by OCCT to millimetres and reported with measured bounds. Missing unit declarations and external-file geometry references are rejected. The original file, importer version and normalized B-reps are immutable and hash checked on every build. Validated numerical reports and preview meshes are cached with the engine version and content hashes. Rigid placement reuses those assets; solid clearance still evaluates B-reps. No geometry healing runs silently. STEP does not restore the manufacturer's original feature history.

An `import_step` operation pins `asset_id`, may select a `component_id`, and accepts an explicit `transform` (translation/rotation) to normalize the origin/orientation. Combine its output with authored proxy solids for missing features using the existing operation graph. Preserve the distinction between a measured proxy and manufacturer CAD in part evidence. New revisions change pins explicitly; replacing an upload never edits old revisions. Source assets are project isolated. STL mesh import remains outside this release; the STEP route never treats a mesh as an exact solid.

### Solid checks and keepouts

`part_clearance` now uses exact B-rep distance and intersection volume. `assembly_collision` uses bounding boxes only to eliminate pairs whose separation is already sufficient, then evaluates exact intersections. Select instance IDs, not ambiguous reused part IDs. For printable linked parts, select `source.part_id`; whole linked multi-part documents containing purchased references are excluded from part manufacturing exports. Checks support stable `id`, minimum clearance `min` (mm), numerical `tolerance_mm` (default 0.001) and `volume_tolerance_mm3` (default 0.000001). Linear numerical tolerance and engineering fit allowance are separate. Direct part clearances add both components' declared dimensional uncertainties conservatively. Missing or invalid topology produces `indeterminate`; a solver failure cannot pass a check.

Collision checks accept `instances`, `ignore_pairs` and `allowed_contact_pairs` (pairs of exact instance IDs). Allowed contact permits touching, never positive-volume interference. Intentional connector/mounting contacts should be declared explicitly. Hiding/isolation/transparency/sections only affect inspection; all physical instances remain in checks and exports.

`assembly.keepouts` contains `{id, owner_instance, output, purpose, source, clearance_mm, against?}`. Its `output` is an operation shape in the owner's component coordinates; owner placement also places the region. Keepouts are checked separately and never exported as parts. Model ribbon bends, plug bodies, antenna restrictions, cell allowance, tool access, lever operation and service travel. `swept_box` takes `origin`, `size`, `travel` and creates a conservative box enclosing a linear insertion/removal path. It can over-report diagonal-path obstructions; use authored swept geometry when needed. It is not automatic motion planning or RF/thermal simulation.

The viewer provides an instance/component list, selection, hide/isolate, transparency, XYZ clipping, front/top/side/isometric views, fit selection, issue focus and closest-point markers. Mesh point measurement is labelled approximate. Instance/face measurement is evaluated by the CAD runner in the selected immutable revision through `design_measure` or POST `/api/measure`. Face indices refer to the named preview visual in that build, not persistent references across revisions. Inspection PNGs can be downloaded.

### Enclosure templates and fit profiles

`design_enclosure_from_assembly` / POST `/api/assembly-enclosures` takes `assembly_design_id`, optional `assembly_revision_id`, `name`, and `options`. No PCB Studio binding is needed. The editable template includes a hollow front shell, removable rear cover and locating lip around the complete assembly bounds. Options include `wall_mm`, `front_mm`, `rear_mm`, `component_gap_mm`, `lid_fit_mm`; a `display` opening with 2D `origin`/`size` and optional `glass_origin`/`glass_size`/`recess_depth_mm` seating pocket; measured `fasteners` with 2D `position`, `boss_diameter_mm`, `hole_diameter_mm` and optional `nut_width_mm`/`nut_depth_mm`; and lists of box `supports`, `brackets`, `battery_cradle`, `cutouts` with 3D `origin`/`size`. Cutouts cover USB-C, ventilation and service openings. Position supports and retention features from measured component datums, and validate screw reach and tool access. Template coordinates use assembly Z as enclosure depth; normalize the assembly first. Glass ledge width and retaining brackets follow the measured opening and authored support boxes.

The generated source retains the assembly pin and generator options/version. Refreshing component pins does not silently resize an enclosure; regenerate the template or adjust its explicit parameters, then validate. `assembly.instances[].alignment` supports `target_instance`, `local_point`, `target_point`, `offset` after its explicit rotations. Alignment dependency cycles fail. Numeric placement creates ordinary immutable revisions. Purchased components require rigid assembly placement.

A definition can select `fit_profile_id`. A parameter's `fit` key resolves against the selected print profile's `fits_mm`; explicit revision parameters override profile values. Built-in template keys cover lid, holes, nut pockets, display seating and component pockets. Calibrate these against printer/material coupons. `manufacturing.orientation` is optional Euler X/Y/Z degrees applied to unique part manufacturing exports; it does not rotate the design assembly.

`manufacturing_rules` remains **declared FDM rules**. Optional `print_geometry` (`part`, `min_wall_mm`, `min_feature_mm`, `max_overhang_deg`) samples mesh rays and surface angles, returning affected regions, sampling limits and mesh tolerance. It detects sampled thin walls even if author metadata is optimistic. Small spans are sampled thickness candidates, not complete feature recognition. Overhangs currently use the inspected geometry's Z direction; orient it appropriately before interpreting them. Clean samples remain `indeterminate`, not proof of printability. Validate final toolpaths in the slicer.

### Release and replay

Manufacturing ZIPs default to `prototype`. They contain source, explicit parameters, full resolved component snapshots, normalized imported B-reps and original STEP assets, hashes, checks, component evidence, BOM, profiles, assembly/service information and orthographic depth-rendered PNGs plus instance-envelope SVGs. Named inspection views support front/top/side directions, selected instance IDs and optional section planes. PNGs use the preview tessellation; the SVGs are labelled envelopes, not detailed section drawings. Downloaded viewer PNGs supplement them with actual inspection views.

Assembly STEP/GLB retain purchased references. Named assemblies export separate, oriented printable/fabricated part STEP/STL/3MF files; they do not emit an assembled STL/3MF that could accidentally be sliced as one product. Quantities and assembly placement remain in assembly/BOM metadata. `print/`, `parts/`, `assembly/` and `sources/` separate manufacturing and reference assets.

For a verified package, set `release.stage="verified"`, name `required_checks`, resolve assumptions and component evidence, and supply fastening/assembly/service instructions. Attach actual `physical_fit` and `enclosed_thermal` pass records with `design_verification_record` / POST `/api/verification` using the exact `revision_id`, `source_sha256` and `evidence_url`. These are immutable records outside CAD source, so recording a physical test does not invalidate the tested revision. A later failure supersedes an earlier pass. Failed, uncomputed or indeterminate required validation blocks a verified package. The app records evidence; it does not conduct physical tests or certify safety.

To replay without the original database, extract a ZIP and run `bun runner/replay.mjs /absolute/package/path /absolute/output/path` from the same Design release checkout. It verifies all packaged checksums and imported B-rep hashes, resolves package-local paths and calls the pinned runner. Match the manifest engine version before comparing release results. Build/install dependencies with `cd runner && bun install --frozen-lockfile && bun run build` first.

### Validation

Run `GOWORK=off go test -short ./...` from this directory; run `bunx playwright test --config mcp/design/tests/browser/playwright.config.ts` from the apps repository. The browser fixture uses real generated geometry and mocked API responses. Set `DESIGN_MANUFACTURER_FIXTURES` to Pocket's `research/manufacturer` directory and run `go test -run TestManufacturerSTEPFixtures -v` to test the actual collected CM5, carrier and PiSugar assets. Set `DESIGN_MANUFACTURER_CACHE` to a private temporary directory to retain imported fixtures between local runs. Manufacturer CAD is not copied into this public repository. `POCKET_FIXTURES.json` identifies the selected November 7 BOM and explicitly excludes the older HDMI display and V2 cooler references.
