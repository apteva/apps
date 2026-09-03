# Design Studio

Design Studio is Apteva's agent-native parametric 2D/3D CAD app. It stores safe `apteva-design/v1` operation graphs as immutable revisions, evaluates them with Replicad + OpenCascade.js, validates exact geometry, renders browser meshes, and exports STEP, STL, 3MF, GLB, and traceable manufacturing ZIPs.

Version 0.4 adds reusable component documents. An assembly part may use a local operation `output` or a `source` containing another Design Studio design ID, immutable revision ID, SHA-256 source hash, and optional named part ID. Builds recursively materialize pinned dependencies, reject hash mismatches and cross-project links, and detect dependency cycles. `assembly_create` starts a linked assembly and `assembly_sources_refresh` advances direct links only by creating a new immutable assembly revision. Manufacturing ZIPs include `dependencies.json`, while per-part artifact metadata retains component provenance.

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

Manufacturing checks are deliberately auditable. Print wall, feature, and overhang values are declared by the part author and checked against its selected profile; Design Studio does not pretend these values were inferred when the current kernel cannot prove them geometrically. Clearance and collision checks currently use conservative transformed bounding boxes.

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
