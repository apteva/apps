# PCB Studio App Proposal

## Version 0.5 implementation status

PCB Studio now includes a native physical Wiring workspace for Arduino and breadboard tutorials. Its semantic model stores reference parts, stable named pins, jumper endpoints, tutorial steps, and firmware pin mappings in the immutable PCB revision. PCB Studio renders that model itself as detailed SVG and PNG, packages tutorial JSON/ZIP, validates endpoint and LED/resistor compatibility, and maps Arduino GPIO execution back onto visible parts. The first reference design is an Arduino Uno R3 connected to a half breadboard, 220 Ω resistor, and red LED on pin 9. No Fritzing or external illustration engine is required.

## Version 0.4 implementation status

The first native routing and simulation milestone is implemented. PCB Studio now owns deterministic grid/A* autorouting with net-class rules and reviewable typed operations; failed or constraint-invalid route proposals are blocked by default. It also owns a deterministic DC, RC-transient, and digital-event simulator with sources, probes, fault injection, waveform artifacts, and behavioral regulator, passive, LED, and sensor models.

An Arduino-compatible virtual hardware runtime executes bounded sketches against Serial, GPIO, I2C, timing, and modeled sensors. An optional native Functions app binding provides the contract for project-owned full compiler/runtime adapters without making PCB Studio dependent on a third-party engine. Route, Simulate, and Firmware workspaces expose these capabilities in the Apteva panel, while MCP and HTTP surfaces make the same operations agent-accessible. Simulation, firmware, manufacturing, and fabrication-verification artifacts persist through the required native Storage binding.

Version 0.4 adds an interactive snapped layout editor for component movement, rotation, nudging, manual trace and via authoring, deletion, and undo/redo. Changes remain staged until committed as an immutable revision. It also adds an independent Apteva-owned Gerber/Excellon parser and reconciliation gate; both manufacturing packages and releases are rejected if generated syntax, bounds, outline, drill, geometry, or Gerber-job checks fail.

This is an engineering-preview milestone, not a claim that arbitrary boards are production-ready. Fabrication remains gated by native footprint, connectivity, clearance, keepout, drill, annular-ring, and differential-pair checks. Advanced coupled differential-pair routing, SPICE-class analog models, thermal/RF simulation, a full C++ toolchain image, and real-board coupon validation remain later milestones.

Status: Proposed

Internal name: `pcb`

Display name: **PCB Studio**

## Decision

Build a first-party, project-scoped `pcb` app that owns the complete electronics
design workflow:

```text
requirements
  -> component selection
  -> schematic
  -> electrical checks
  -> board definition and stackup
  -> placement and routing
  -> physical checks
  -> sourcing snapshot
  -> immutable manufacturing release
  -> fabrication / assembly quote
  -> approved production order
  -> received hardware
```

PCB Studio is not a wrapper around KiCad, tscircuit, Flux, Altium, EasyEDA,
KiCanvas, an external autorouter, or another ECAD engine. Apteva owns the
canonical format, electrical graph, layout model, editor, renderer, validation,
routing, exports, revision history, and agent operation vocabulary.

External integrations are restricted to replaceable data and real-world
services:

- Electronic component data.
- Distributor inventory, pricing, and sourcing.
- PCB fabrication and assembly.
- Shipping and production status.
- Optional specialized compliance or laboratory data.

The app remains useful with no external integration bound. A user can create,
edit, validate, render, and export a design using only PCB Studio and Storage.

## Product Goal

An agent or project member should be able to start with a plain-language
hardware requirement and finish with a reviewable, reproducible manufacturing
release without leaving Apteva.

The app must support both modes equally:

- **Manual engineering:** a browser editor for schematic and PCB work.
- **Agent engineering:** safe structured operations against the same design,
  with every change reviewable, reversible, and validated.

The agent does not edit opaque files or execute arbitrary design code. The UI
and the agent both use the same versioned domain operations and validation
engine.

## Non-Goals For The First Release

- Full compatibility with every historical ECAD file format.
- Analog, RF, thermal, electromagnetic, or mechanical simulation.
- High-speed automatic placement and routing.
- Rigid-flex, chip/package design, IC layout, wire harnesses, or FPGA place and
  route.
- Panelization controlled by a specific fabricator.
- Automatic purchases or production orders without explicit approval.
- A public component marketplace.
- Executing user-authored JavaScript, TypeScript, Python, or plugins.

These can be added without changing the ownership or provider boundaries.

## Core Principles

### 1. Apteva owns design intent

The source of truth is a versioned `apteva-pcb/v1` document, not a provider or
third-party tool format.

### 2. Integer geometry

All persisted physical coordinates and dimensions use signed 64-bit integer
nanometres. Angles use integer microdegrees. User-selected units are display
preferences only. Geometry never depends on floating-point equality.

### 3. Immutable revisions

Every accepted mutation creates an immutable revision. Builds, checks,
renders, exports, sourcing snapshots, and releases always reference a precise
revision hash.

### 4. One operation model

Mouse edits, forms, imports, agent actions, and future collaborative editing
all compile into the same typed operation batch. There is no privileged AI
editing path.

### 5. Validation before confidence

An agent response is not evidence that a change succeeded. The app returns the
applied operation result, affected objects, validation state, and unresolved
issues. A manufacturing release cannot be created from a failed build.

### 6. Providers are replaceable

Provider identities, credentials, offer IDs, and order IDs never leak into the
electrical model. They live in sourcing and manufacturing records connected to
a frozen design release.

### 7. No mandatory cloud service

Manual editing, agent editing, local libraries, validation, rendering, and
manufacturing export work without Nexar, JLCPCB, or any other external
connection.

## App Boundaries

### PCB Studio owns

- Electronics designs and variants.
- Component definitions used by a design.
- Symbols, pins, footprints, pads, and optional 3D-model references.
- Schematic sheets, components, ports, nets, buses, and annotations.
- Board outlines, cutouts, stackups, layers, placement, traces, vias, zones,
  keepouts, and dimensioning.
- Electrical and physical design rules.
- ERC, DRC, connectivity, and manufacturability checks.
- Deterministic renders and manufacturing exports.
- Revisions, diffs, releases, waivers, provenance, and audit history.
- Provider-neutral sourcing snapshots, quotes, and production jobs.
- The browser editor and agent-safe mutation surface.

### Storage owns

- Uploaded datasheets and reference files.
- Component media and 3D assets.
- Immutable source snapshots and portable project packages.
- Generated SVG, PNG, CSV, Gerber, drill, and release ZIP artifacts.
- Provider documents such as quotations, invoices, certificates, and reports.

PCB Studio stores Storage file IDs and content hashes. It never duplicates
large file bytes in SQLite.

### Optional first-party apps

- **Design Studio:** exchanges board outlines, mounting holes, keepout volumes,
  enclosure envelopes, and later assembled-board mechanical models. It is not
  an electrical engine dependency.
- **Code:** consumes pin maps and hardware-interface manifests so firmware can
  stay synchronized with a released board revision.
- **Devices:** links produced hardware to a design/release and records bring-up,
  identity, telemetry, and exposed firmware capabilities.
- **Inventory:** tracks purchased components, bare boards, and assembled units.
- **Workflows:** reacts to release, quote, order, shipment, and validation
  events. PCB Studio does not require Workflows to operate.
- **Docs or Notes:** may reference a design or release but do not own design
  records.

## Canonical Portable Format

The portable extension is `.apteva-pcb`, a ZIP package with deterministic file
ordering, normalized timestamps, and a package SHA-256.

```text
manifest.json
design.json
libraries/
  components.json
  symbols.json
  footprints.json
assets/
  datasheets/...
  models/...
provenance/
  imports.json
  operations.jsonl
```

`manifest.json` declares:

- Schema and format versions.
- Design ID, revision ID, and parent revision ID.
- Units contract.
- Required and optional features used by the design.
- File hashes.
- Generator version.
- Original import provenance when applicable.

Unknown optional fields are retained when a package is opened and resaved.
Unknown required features make the package read-only instead of silently
dropping data.

The database stores an indexed working representation, but the canonical
snapshot is always reproducible as a portable package.

## Compatibility With Other ECAD Formats

Owning the PCB engine and canonical format does not prevent compatibility with
other tools. Compatibility is implemented through Apteva-owned import and
export adapters around `apteva-pcb/v1`; no external format or engine becomes a
runtime dependency or source of truth.

This follows the useful part of Design Studio's model:

- Keep one native, versioned source representation.
- Treat other formats as revision-bound inputs or derived artifacts.
- Store the original or generated bytes in Storage.
- Record format, content hash, converter version, provenance, and warnings.
- Put native source, validation, hashes, and handoff files together in a
  traceable manufacturing package.

Design Studio currently applies this pattern strongly to exports: its native
`apteva-design/v1` graph remains authoritative while STEP, STL, 3MF, GLB, and
mesh representations are derived artifacts. General-purpose import is not yet
exposed there, so PCB Studio should implement the input half explicitly rather
than assuming it comes for free.

### Compatibility levels

Every format adapter declares separate, testable capabilities:

- `detect`: identify the format and version safely.
- `view`: extract enough information for a non-editable preview.
- `import`: convert supported content into the native graph.
- `edit`: imported content is editable without known structural loss.
- `export`: generate a file accepted by the target tool or workflow.
- `roundtrip`: import and re-export preserve the adapter's documented semantic
  subset.

The UI displays these capabilities and never turns “can preview” into “fully
compatible.” Capability declarations are version-specific; support for one
KiCad, Altium, or EasyEDA generation does not imply all versions.

### Import lifecycle

```text
original file/package
  -> store original bytes + SHA-256 in Storage
  -> detect format and version
  -> parse into an adapter-owned intermediate result
  -> validate references, units, geometry, and connectivity
  -> map supported objects into apteva-pcb/v1
  -> produce a conversion report
  -> create a new native revision
```

The original input is never overwritten or discarded. It remains attached to
the imported revision so a user can download it, audit the conversion, or retry
with a later adapter version.

Each imported native object may retain safe provenance:

- Source format and version.
- Source file ID and hash.
- Source object identifier or path.
- Adapter and adapter version.
- Mapping confidence.
- Normalization or approximation notes.

Source identifiers are provenance only; native stable IDs control all edits
after import.

### Conversion report

Every import returns a machine-readable and human-readable report containing:

- Objects imported exactly.
- Objects normalized into an equivalent native representation.
- Objects approximated with a stated difference.
- Unsupported objects preserved only in the original source.
- Missing libraries, symbols, footprints, models, or fonts.
- Ambiguous units, layers, nets, pin mappings, or variants.
- ERC/DRC changes introduced by conversion.
- Whether the result is editable, partially editable, or reference-only.

Unsupported required behavior blocks editable import unless the user explicitly
chooses a lossy fork. A lossy fork is a new native design and never pretends to
be a reversible conversion.

### Import modes

- **Native editable:** conversion met the adapter's editable fidelity contract.
- **Editable with warnings:** known approximations exist and are enumerated.
- **Reference-only:** PCB Studio can render or inspect the source but will not
  mutate it.
- **Rejected:** the source is malformed, unsafe, encrypted, unsupported, or
  depends on unresolved required assets.

Reference-only support is valuable for design review and manufacturing intake,
but it is clearly separated from native editing.

### Export lifecycle

Exports are immutable artifacts of a particular revision and variant. Each
artifact records:

- Target format and version/profile.
- Revision, release, and variant IDs.
- Source hash.
- Adapter version.
- Options and units.
- Content hash and Storage file ID.
- Validation state.
- Warnings and omitted features.

Generating a KiCad, EasyEDA, Altium, Gerber, or netlist file never changes the
native design. Re-exporting the same revision with the same adapter version and
options must produce identical bytes where the target format permits it.

### Format priorities

#### Manufacturing formats

These are part of the core manufacturing path and receive the highest
correctness bar:

- Gerber X2 and compatible RS-274X profiles.
- Excellon plated and non-plated drill files.
- IPC-D-356 netlist.
- BoM CSV with configurable provider profiles.
- Pick-and-place / centroid CSV.
- Assembly, fabrication, and schematic drawings in SVG/PDF.
- Native release manifest and checksum files.

#### Design interchange

Adapters are delivered incrementally:

1. KiCad projects, schematics, boards, symbols, and footprints.
2. EasyEDA JSON/project packages and libraries.
3. Eagle XML schematics, boards, and libraries.
4. Altium ASCII/structured export formats.
5. EDIF and other schematic/netlist interchange formats.

The order can change based on customer demand, but each adapter must publish
its supported subset before release.

#### Mechanical and visualization formats

- SVG and PDF drawings.
- PNG previews.
- STEP for a bare or assembled board once the native 3D model is ready.
- STL/3MF only where mesh handoff is useful.
- GLB for browser review.

These are generated by PCB Studio or optionally handed to Design Studio as
artifacts; Design Studio is not required to interpret the electrical design.

### Round-trip guarantees

Round-trip compatibility is semantic, not necessarily byte-for-byte. Tests
compare:

- Component identities and values.
- Pin and pad mappings.
- Net connectivity.
- Board outline and layer stack.
- Placement and rotations.
- Trace, via, zone, and keepout geometry.
- Rules representable in both formats.
- Variants and fitted-state information when supported.
- ERC/DRC outcomes within the documented shared subset.

Unknown source constructs remain available in the preserved original even when
they cannot participate in native editing. PCB Studio never injects opaque
foreign fragments into its canonical graph merely to claim lossless support.

### Adapter boundary

Format adapters are maintained by Apteva inside PCB Studio or a first-party
converter package. They are deterministic parsers/writers, not external ECAD
engines, subprocesses, hosted conversion APIs, or provider integrations.

Adapters receive bounded bytes and return typed intermediate data plus a
conversion report. They have no network access, credentials, or ability to
change a design directly. The normal operation and revision layer creates the
native revision after validating the adapter result.

## Domain Model

### Design

A design contains:

- Stable UUID and human-readable slug.
- Name, description, intent, lifecycle state, and tags.
- One or more variants.
- One logical schematic graph.
- One or more physical boards in later versions; V1 supports one board.
- Current revision pointer.
- Default ruleset and manufacturing preferences.

### Variants

Variants share connectivity and layout while allowing:

- Fitted or not-fitted components.
- Alternate manufacturer parts.
- Alternate component values.
- Variant-specific BoMs and placement exports.
- Variant-specific firmware/profile metadata.

Every export, sourcing snapshot, quote, and release names its variant.

### Component definition

A component definition is owned locally even when created from external data.
It contains:

- Manufacturer and manufacturer part number when known.
- Description, category, value fields, ratings, tolerances, and lifecycle.
- Units and pins with stable IDs, names, numbers, electrical types, and
  swap-group metadata.
- One or more schematic symbols.
- One or more footprints.
- Optional simulation, compliance, datasheet, and 3D-model metadata.
- Provenance for every externally enriched field.

External providers enrich a local definition; they never remain the only copy
of information required to interpret a released design.

### Schematic graph

The logical graph includes:

- Hierarchical sheets.
- Component instances and units.
- Pins, ports, junctions, wires, labels, buses, and no-connect markers.
- Power symbols and global nets.
- Net classes and electrical constraints.
- Design annotations and comments.

Connectivity is derived from stable object references, not inferred only from
drawn line intersections.

### Board model

The physical model includes:

- Board outline and internal cutouts.
- Origin, auxiliary origin, and datum definitions.
- Arbitrary copper-layer count with semantic layer roles.
- Dielectric and copper stackup.
- Component and footprint placement.
- Pads, holes, slots, traces, arcs, vias, copper zones, keepouts, teardrops,
  text, dimensions, fabrication notes, and graphics.
- Differential pairs, matched groups, impedance targets, and length budgets.
- Mechanical height and courtyard constraints.

The schematic graph is authoritative for electrical connectivity. The board
model maps physical pads to schematic pins and cannot invent a net without an
explicit board-only-net record.

### Rules

Rules use typed selectors and priorities rather than provider-specific text.
Initial rule types include:

- Minimum clearance.
- Trace width range and preferred width.
- Via diameter and drill range.
- Annular ring.
- Copper-to-edge clearance.
- Hole-to-hole and hole-to-copper clearance.
- Solder-mask expansion and minimum sliver.
- Silk-to-mask and silk-to-edge clearance.
- Courtyard overlap.
- Allowed layers.
- Differential-pair width, gap, skew, and maximum uncoupled length.
- Net or group length targets.
- Maximum current metadata.
- Component height.
- Keepout behavior.

Rules can be defined globally, by net class, by component/footprint class, or
on a specific object. Precedence is deterministic and visible in the editor.

## Revision And Operation Model

### Revision

Each revision stores:

- Parent revision ID.
- Canonical snapshot Storage file ID and hash.
- Ordered operation batch that created it.
- Author type and identity.
- Agent run/thread provenance when applicable.
- Note and timestamps.
- Build status and engine version.

Optimistic concurrency requires `expected_parent_id`. A stale mutation returns
a conflict and a semantic diff; it never overwrites newer work.

### Typed operations

The first operation vocabulary includes:

- `component_definition.create`
- `component_instance.add`, `update`, `remove`, `annotate`
- `sheet.create`, `update`, `remove`
- `wire.add`, `update`, `remove`
- `net.create`, `rename`, `connect`, `disconnect`
- `port.add`, `update`, `remove`
- `board.configure`
- `outline.set`
- `stackup.set`
- `footprint.assign`
- `placement.set`, `placement.lock`
- `trace.add`, `trace.update`, `trace.remove`
- `via.add`, `via.update`, `via.remove`
- `zone.add`, `zone.update`, `zone.remove`, `zone.refill`
- `keepout.add`, `keepout.update`, `keepout.remove`
- `rule.create`, `rule.update`, `rule.remove`
- `variant.create`, `variant.update`, `variant.remove`
- `waiver.create`, `waiver.revoke`

Operations refer to stable IDs. Deleting an object with dependants is rejected
unless the operation batch also resolves those dependants. Multi-operation
batches are atomic.

### Semantic diff

Diffs describe engineering changes, not JSON line changes:

- Components added, removed, or substituted.
- Pins connected or disconnected.
- Nets renamed or merged.
- Placement movement and rotation.
- Routing length, via-count, or layer changes.
- Rule and stackup changes.
- Check regressions and resolutions.
- BoM and sourcing changes.

## Native Electrical Engine

The engine is implemented inside the app repository and versioned with the
app. It has no runtime dependency on another ECAD engine.

### Backend responsibilities

The Go sidecar owns:

- Schema normalization and migrations.
- Connectivity graph construction.
- Referential-integrity validation.
- Spatial indexes and exact 2D geometry predicates.
- Zone fill and connectivity calculation.
- ERC and DRC execution.
- Deterministic placement/routing algorithms.
- Gerber, Excellon, BoM, placement, netlist, SVG, and package export.
- Revision builds and artifact generation.

### Browser responsibilities

The TypeScript/React panel owns:

- Schematic and board canvas interaction.
- WebGL/Canvas rendering from app-owned primitives.
- Selection, snapping, handles, measurement, overlays, and rule visualization.
- Local previews of pending operations.
- Keyboard shortcuts and editing ergonomics.

The browser does not become the source of truth. A local edit preview is
committed only after the backend validates and accepts its operation batch.

### Geometry implementation

V1 geometry supports deterministic integer implementations of:

- Points, segments, arcs, circles, rectangles, capsules, and polygons.
- Bounding boxes and spatial indexing.
- Segment intersection and point-in-polygon tests.
- Clearance expansion for primitive shapes.
- Polygon clipping and offsetting needed for copper zones.
- Pad, trace, via, hole, edge, mask, and silkscreen collision tests.

Every algorithm receives golden fixtures for boundary contact, acute angles,
arcs, holes, nested polygons, and large coordinate values. Generated artifacts
must be byte-stable for identical inputs.

### ERC

Initial electrical checks include:

- Unconnected required pins.
- Output-to-output conflicts.
- Power-input pins without a valid source.
- No-connect marker conflicts.
- Duplicate designators.
- Missing values, footprints, or pin mappings.
- Single-pin nets and dangling wires.
- Incompatible alternate-part pin maps.
- Variant-specific connectivity failures.

### DRC

Initial physical checks include:

- Copper clearance.
- Trace-width and via-size limits.
- Annular ring.
- Copper and hole edge clearance.
- Drill spacing.
- Unrouted or partially routed nets.
- Short circuits and isolated copper.
- Pad-to-pin mapping errors.
- Courtyard overlap.
- Solder-mask and silkscreen clearances.
- Differential-pair gap and skew.
- Net-length constraints.
- Placement outside the board or in a keepout.

Checks produce stable fingerprints. Waivers point to a fingerprint, rule,
revision, author, reason, and optional expiry. A changed violation does not
silently inherit an old waiver.

### Routing

Routing is built in stages:

1. Manual trace editing with shove-free collision feedback.
2. Deterministic point-to-point route suggestions on a visibility/grid graph.
3. Batch routing for simple two-layer boards with rip-up and retry.
4. Interactive shove routing.
5. Constraint-aware differential pairs and matched groups.
6. Placement optimization and higher-complexity routing.

The first production release does not claim automatic routing for high-speed,
RF, power-dense, or safety-critical nets. Those nets can be marked manual-only.

### Simulation

Simulation is deferred until the authoring and manufacturing path is reliable.
When added, it is an Apteva-owned circuit solver consuming the canonical graph,
not a hidden third-party engine. The initial target should be DC operating
point and linear transient primitives before nonlinear semiconductor models.

## Component Library And Sourcing

### Local-first component library

PCB Studio ships with a small verified common library and lets users create,
clone, review, and publish project or organization components.

Quality states:

- `draft`
- `verified`
- `deprecated`
- `blocked`

Verification separately covers:

- Pinout.
- Symbol.
- Footprint geometry.
- Pad mapping.
- Ratings.
- Datasheet provenance.
- Optional 3D model.

A provider search result is not automatically a verified design component.
The agent may draft a definition, but a release reports all unverified library
elements.

### Provider-neutral component-data contract

The `component_data_provider` integration role normalizes:

- Manufacturer and MPN lookup.
- Parametric search.
- Technical specifications.
- Datasheet and lifecycle metadata.
- Package names and compliance.
- Cross references and alternates.

Nexar/Octopart is the first compatible provider. Future compatible providers
can include DigiKey, Mouser, Arrow, Farnell, LCSC, SiliconExpert, or internal
enterprise catalogs without changing the PCB domain model.

### Provider-neutral sourcing contract

The `sourcing_provider` role normalizes:

- Batch offers by MPN and quantity.
- Stock, lead time, MOQ, price breaks, currency, and geography.
- Authorized-distributor status.
- Packaging and order multiples.
- Alternative parts.
- Quote timestamps and expiry.

A sourcing snapshot is immutable and records the exact query, providers,
responses, normalization version, time, target quantity, and region. A release
never presents live prices as if they were frozen production costs.

## Fabrication And Assembly Integrations

### Fabricator contract

The `pcb_fabricator` role exposes provider-neutral capabilities:

- Supported layers, dimensions, materials, thicknesses, copper weights,
  finishes, masks, silkscreens, impedance options, drills, slots, tolerances,
  panelization, and accepted artifacts.
- Capability validation against a release.
- Quote request and retrieval.
- Order preparation and submission.
- Production and shipment status.
- Manufacturer feedback and file requests.

### Assembly contract

The `pcb_assembler` role additionally supports:

- Turnkey, consigned, and mixed sourcing.
- Assembly side and technology.
- Placement constraints and fiducials.
- Parts substitutions and customer approval.
- DFM review results.
- First-article and test options.
- Excess-part handling.

One provider connection may satisfy both roles. The core records the roles and
capabilities separately.

Potential providers include JLCPCB/JLC3DP, PCBWay, MacroFab, AISLER, OSH Park,
Seeed Fusion, NextPCB, and internal contract manufacturers. A provider is not
listed as compatible until its integration implements and tests the required
tools. The existing JLCPCB integration is discovery-only and is therefore not
yet a production adapter.

### Quote request

A normalized request contains:

- Release and variant IDs plus immutable hashes.
- Quantity and optional panel quantity.
- Board dimensions and layer count.
- Stackup and material requirements.
- Copper, finish, mask, silkscreen, impedance, tolerance, and testing options.
- Assembly mode and sourcing preference.
- Destination country/region without copying a full address into ordinary
  logs or events.
- Storage file IDs for the exact release artifacts.

Normalized quote results include itemized price, currency, taxes if known,
shipping if known, lead-time range, capability warnings, expiry, and the raw
provider response stored as a protected artifact.

### Safe production ordering

Production is a two-step operation:

```text
pcb_order_prepare
  -> validates release + quote + current price
  -> returns exact provider, artifacts, quantity, total, currency, and warnings

pcb_order_submit
  -> requires prepare token, expected total/currency, confirm=true,
     idempotency key, and pcb.orders permission
```

Rules:

- No agent can create an order from a mutable revision.
- Any artifact or release hash mismatch invalidates preparation.
- Any price increase above the approved total invalidates preparation.
- Credentials and payment methods stay inside the provider connection.
- Raw addresses, payment data, and credentials never appear in events or chat
  cards.
- Repeated submission is idempotent.
- Cancellation is a separate explicit action and reports provider policy.

## Manufacturing Release

A release freezes:

- Design revision and variant.
- Canonical source package.
- Schematic and board renders.
- ERC, DRC, and DFM reports.
- Explicit waivers and approval identities.
- Gerber layer set.
- Plated and non-plated drill files.
- BoM and alternate-part policy.
- Pick-and-place / centroid files.
- Assembly drawings.
- Netlist and pin map.
- Stackup, dimensions, tolerances, and fabrication notes.
- Checksums and generator versions.
- Sourcing snapshot when requested.

The resulting release ZIP is deterministic. Editing the design creates a new
revision but never modifies an existing release.

Release states:

```text
draft -> validating -> ready -> released
                    -> failed

released -> quoted -> ordered -> in_production -> shipped -> received
```

`released` and later records are immutable except for external lifecycle state
and append-only audit events.

## MCP Surface

### Resources and permissions

- `pcb.designs.read`
- `pcb.designs.write`
- `pcb.components.read`
- `pcb.components.write`
- `pcb.validate`
- `pcb.export`
- `pcb.release`
- `pcb.sourcing`
- `pcb.quotes`
- `pcb.orders`

Design-scoped permissions use `resource_from: design/{id}`. Quote and order
permissions do not imply design-write permission.

### Core tools

- `pcb_examples`
- `pcb_designs_create`
- `pcb_designs_list`
- `pcb_designs_get`
- `pcb_designs_archive`
- `pcb_designs_import`
- `pcb_packages_export`
- `pcb_revisions_get`
- `pcb_revisions_diff`
- `pcb_operations_apply`
- `pcb_graph_query`
- `pcb_validate`
- `pcb_render`
- `pcb_artifacts_list`

### Library tools

- `pcb_components_create`
- `pcb_components_get`
- `pcb_components_search`
- `pcb_components_verify`
- `pcb_components_enrich`
- `pcb_footprints_create`
- `pcb_symbols_create`

### Sourcing and production tools

- `pcb_bom_generate`
- `pcb_sourcing_refresh`
- `pcb_sourcing_get`
- `pcb_providers_list`
- `pcb_release_create`
- `pcb_release_get`
- `pcb_quote_request`
- `pcb_quotes_list`
- `pcb_quote_get`
- `pcb_order_prepare`
- `pcb_order_submit`
- `pcb_order_get`
- `pcb_order_cancel`

`pcb_operations_apply` accepts only the typed operation vocabulary, an
`expected_parent_id`, and an optional idempotency key. Domain-specific helper
tools may later compile into the same operation batch, but they do not bypass
validation.

## Agent Contract

The bundled `/pcb` skill teaches agents to:

1. Read the current revision and outstanding checks before proposing changes.
2. Separate requirements, assumptions, constraints, and unresolved questions.
3. Use verified components where possible.
4. Ground pinouts and ratings in stored provenance.
5. Apply one coherent operation batch at a time.
6. Validate after every electrical or physical mutation.
7. Report affected nets/components, new issues, resolved issues, and remaining
   uncertainty.
8. Never claim a board is manufacturable merely because files were generated.
9. Never waive a check without an explicit reason and authorization.
10. Never submit a purchase or production order without the dedicated approved
    order flow.

The agent should default to planning and schematic work before layout. It must
not silently choose safety-critical ratings, isolation requirements, RF
geometry, regulatory classifications, or mains-voltage constraints.

## UI

PCB Studio provides one project page with these tabs:

- **Overview:** requirements, revision, validation status, releases, and open
  decisions.
- **Schematic:** hierarchical canvas, library search, properties, nets, and ERC
  overlays.
- **Board:** layers, stackup, placement, routing, zones, constraints, and DRC
  overlays.
- **3D:** initially a simple assembled-board preview from app-owned geometry;
  richer component bodies arrive with verified models.
- **Components:** local library definitions, provenance, verification, and
  external enrichment.
- **BoM & Sourcing:** variant BoM, availability, offers, risk, and alternates.
- **Manufacture:** releases, provider capability checks, quotes, orders, and
  status.
- **History:** semantic revision diff, operations, authors, validations, and
  audit events.

Chat attachments:

- `pcb-design-card`
- `pcb-check-report-card`
- `pcb-component-card`
- `pcb-sourcing-card`
- `pcb-release-card`
- `pcb-quote-card`
- `pcb-order-card`

The canvas supports comments through ordinary project conversation links or a
future generic annotation surface; PCB Studio should not implement a second
chat system.

## Integration Binding Sketch

The eventual manifest should follow the existing provider-neutral multi-binding
pattern:

```yaml
requires:
  permissions:
    - db.write.app
    - platform.apps.call
    - platform.connections.execute
    - net.egress
  integrations:
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.read, files.write]
      required: true
      label: Storage
    - role: mechanical_design
      kind: app
      compatible_app_names: [design]
      capabilities: [designs.read, designs.write]
      required: false
      label: Mechanical Design
    - role: firmware_source
      kind: app
      compatible_app_names: [code]
      required: false
      label: Firmware source
    - role: device_management
      kind: app
      compatible_app_names: [devices]
      required: false
      label: Device management
    - role: inventory
      kind: app
      compatible_app_names: [inventory]
      required: false
      label: Inventory
    - role: component_data_provider
      kind: integration
      mode: multiple
      compatible_slugs: [nexar]
      required: false
      label: Component data providers
    - role: sourcing_provider
      kind: integration
      mode: multiple
      compatible_slugs: [nexar]
      required: false
      label: Component sourcing providers
    - role: pcb_fabricator
      kind: integration
      mode: multiple
      compatible_slugs: [] # populated only as complete adapters land
      required: false
      label: PCB fabricators
    - role: pcb_assembler
      kind: integration
      mode: multiple
      compatible_slugs: [] # populated only as complete adapters land
      required: false
      label: PCB assemblers
```

Provider adapters live behind internal interfaces. No core handler switches on
a provider slug outside the adapter registry.

## Events

- `pcb.design.created`
- `pcb.revision.created`
- `pcb.validation.completed`
- `pcb.validation.failed`
- `pcb.component.created`
- `pcb.component.verified`
- `pcb.sourcing.completed`
- `pcb.sourcing.risk_changed`
- `pcb.release.created`
- `pcb.quote.received`
- `pcb.quote.expired`
- `pcb.order.submitted`
- `pcb.order.status_changed`
- `pcb.order.shipped`
- `pcb.hardware.received`

Events contain stable IDs and safe summaries, never complete designs,
datasheets, credentials, addresses, provider raw responses, or payment data.

## Persistence

Initial project-scoped SQLite tables:

- `designs`
- `revisions`
- `revision_operations`
- `component_definitions`
- `component_versions`
- `component_provenance`
- `builds`
- `check_runs`
- `check_results`
- `waivers`
- `artifacts`
- `sourcing_snapshots`
- `sourcing_offers`
- `releases`
- `quotes`
- `production_orders`
- `production_events`
- `audit_events`

Large snapshots and artifacts live in Storage. Tables contain metadata, hashes,
indexes, current pointers, provider-neutral normalized fields, and protected
references to raw provider artifacts.

## Security And Reliability

- Imported packages are untrusted: reject path traversal, symlinks, duplicate
  normalized paths, decompression bombs, oversized assets, and unsupported
  required features.
- No arbitrary scripts or executable plugins in a design package.
- Every mutation is project-gated, permission-gated, atomic, bounded, and
  idempotent where retry is possible.
- Builds run with time, memory, object-count, vertex-count, and output-size
  ceilings.
- External provider responses are schema-validated before normalization.
- Credentials remain in platform connections.
- Raw provider payloads and sensitive order documents use protected Storage
  objects.
- Release and order audit rows are append-only.
- Source and artifact hashes are rechecked before quote and order operations.
- Manufacturing exports include generator/schema versions for reproducibility.

## Delivery Plan

### Phase 0 — specification and golden fixtures

- Freeze `apteva-pcb/v1` IDs, units, geometry, layers, symbols, footprints,
  connectivity, rules, and operation schemas.
- Build hand-reviewed fixtures ranging from an LED board to a four-layer MCU
  board.
- Define deterministic canonical JSON and package hashing.
- Define Gerber and Excellon golden outputs with independent visual inspection.

Exit condition: the format can represent the selected fixtures without escape
hatches or provider fields.

### Phase 1 — schematic and library

- Design/component persistence and immutable revisions.
- Local component, symbol, footprint, and provenance model.
- Schematic canvas and typed editing operations.
- Connectivity graph, annotation, and initial ERC.
- Portable package import/export.
- Nexar enrichment and sourcing snapshots.

Exit condition: create and validate a complete small schematic manually and by
agent, then reopen it byte-identically from its package.

### Phase 2 — board and manufacturing export

- Board outline, layers, stackup, placement, pads, manual traces, vias,
  keepouts, and simple zones.
- Board canvas, snapping, measurement, and layer controls.
- Initial DRC and connectivity checks.
- Native Gerber, Excellon, BoM, pick-and-place, SVG, and release ZIP writers.
- Deterministic release lifecycle.

Exit condition: fabricate a simple two-layer internal test board from files
generated entirely by PCB Studio and pass electrical bring-up.

### Phase 3 — agent layout and production adapters

- Route suggestions and simple deterministic autorouting.
- Placement assistance with explicit constraints.
- Fabricator/assembler capability adapters.
- Quote normalization and comparison.
- Approved idempotent production ordering.
- Production status and Inventory/Devices linkage.

Exit condition: design, quote, order, receive, provision, and test a board
through Apteva with a complete audit chain.

### Phase 4 — advanced engineering

- Multilayer routing improvements.
- Differential pairs and length tuning.
- Better zone filling and thermal reliefs.
- Native simulation milestones.
- Mechanical exchange with Design Studio.
- Organization libraries, templates, reviews, and component governance.
- Import/export converters written and maintained by Apteva.

## Testing Strategy

### Schema and operations

- Canonicalization and hash stability.
- Round-trip package tests.
- Migration fixtures for every schema version.
- Property tests for valid/invalid operation sequences.
- Optimistic-concurrency and idempotency tests.

### Geometry

- Golden primitive and clearance fixtures.
- Property/fuzz tests for symmetry and translation invariance.
- Extreme-coordinate and integer-overflow tests.
- Visual snapshots for zones, pads, traces, mask, and silk.

### Electrical

- Connectivity graph fixtures.
- ERC and DRC positive/negative cases.
- Variant and alternate-part pin-map tests.
- Stable check fingerprint and waiver invalidation tests.

### Manufacturing

- Gerber and Excellon parser-back validation implemented separately from the
  writers.
- Golden image renders of every exported layer.
- BoM/placement reconciliation against design objects.
- Deterministic release ZIP tests.
- Quote/order adapter contract suites with recording stubs.
- Explicit approval, price-change, hash-mismatch, and idempotency tests.

### Physical validation

Each milestone includes real manufactured test coupons and boards. Software
tests alone are insufficient evidence for manufacturing correctness.

## Principal Risks

### Scope

ECAD is a large domain. The mitigation is a narrow, vertically complete first
board rather than broad partial compatibility.

### Geometry correctness

Copper geometry errors can produce plausible-looking but unusable boards. The
mitigation is integer geometry, separate writer/parser validation, rendered
goldens, and real fabrication coupons.

### Component trust

Incorrect pinouts and footprints are more dangerous than missing components.
The mitigation is local ownership, field-level provenance, independent
verification states, and release warnings.

### AI overconfidence

The agent can make coherent but unsafe engineering decisions. The mitigation is
typed operations, explicit constraints, validation, provenance, review gates,
and no automatic waivers or purchases.

### Provider inconsistency

Fabricators expose different capabilities and quote semantics. The mitigation
is capability negotiation, normalized contracts, raw-response preservation,
and provider-specific adapter tests without provider fields in the core model.

## MVP Definition

The MVP is complete when a user can:

1. Create or agent-generate a small two-layer design in the browser.
2. Use locally owned component definitions enriched optionally from Nexar.
3. Build a schematic and board using the same canonical graph.
4. Run ERC and DRC and resolve or explicitly waive every release-blocking issue.
5. Review semantic revision history.
6. Export deterministic Gerber, drill, BoM, and pick-and-place files.
7. Create an immutable manufacturing release in Storage.
8. Send those files to a fabricator manually.
9. Receive and electrically validate a real PCB produced from the release.

Automated manufacturer ordering is the next milestone, not a condition for
trusting the design and export core.

## Final Recommendation

Build PCB Studio as a new first-party sidecar rather than extending Design
Studio. Reuse Apteva platform contracts—Storage, project isolation,
permissions, resources, UI panels, chat cards, events, app calls, integration
bindings, and immutable revision patterns—but own every ECAD-specific data
structure and algorithm.

Start with the smallest vertically complete board and manufacturing path. Do
not begin with a broad editor mockup, a provider marketplace, or autorouting.
The first durable moat is a trustworthy agent-editable electrical graph and a
manufacturing release produced entirely by Apteva.
