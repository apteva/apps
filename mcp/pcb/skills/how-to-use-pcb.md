# PCB Studio

Use PCB Studio when the result is an electrical design, circuit board, bill of materials, sourcing workflow, or manufacturing handoff—not merely a picture of a board.

## Native model

PCB Studio owns the complete design engine. Its canonical source is `apteva-pcb/v1`: deterministic JSON with integer-nanometre geometry, integer-microdegree rotations, stable object IDs, layers, components, pins, native footprint pads and bodies, nets, copper traces, vias, zones, keepouts, differential-pair constraints, and manufacturing rules. It does not call KiCad, Flux, tscircuit, or another ECAD engine.

Every edit creates an immutable revision. Start with `pcb_examples`, then:

1. Create a board with `pcb_designs_create`.
2. Edit it with `pcb_operations_apply` for normal changes, using the current revision as `expected_parent_id`. Use `pcb_revisions_create` when replacing the complete definition.
3. Add native `body` and `pads` geometry to every component, plus `zones`, `keepouts`, and `differential_pairs` where the board needs them.
4. Run `pcb_validate` and resolve every error. Pad-aware validation checks pin-to-pad mapping, pad routing, drill and annular-ring rules, copper clearance, keepout incursions, and differential-pair gap/skew. Older geometry-free designs receive explicit warnings.
5. Use `pcb_render` for the footprint-aware SVG, `pcb_bom_generate` for the grouped BOM, and `pcb_manufacturing_generate` for the Gerber X2/Excellon manufacturing ZIP.
6. Use `pcb_release_create` to make a traceable ZIP with native source, validation, hashes, SVG, BOM, Gerbers, drill data, and Gerber job manifest. Failed ERC/DRC blocks manufacturing and release creation.

## Component data and sourcing

Local component, footprint, pin, and MPN fields remain authoritative. If the optional `component_data` provider is bound, `pcb_components_search` can search parts and `pcb_bom_source` can match revision MPNs against current distributor data. Provider results enrich a design but never mutate it automatically.

All dependencies are native install bindings. The required `storage` role selects a project or global Storage app install (`kind: app`); `component_data` and `pcb_fabricator` select external connections (`kind: integration`). Do not bypass these roles with connection IDs or assumed app installations.

## Compatibility

The native source remains the editable truth. Import/export compatibility is implemented as deterministic Apteva-owned adapters around that model. Adapters preserve the original source in Storage, emit a conversion report, and declare whether a format is detected, viewed, imported, edited, exported, or semantically round-tripped. V0.2 writes SVG, BOM CSV, Gerber copper/silkscreen/outline files, Excellon drills, a Gerber job manifest, and the native release ZIP without an external engine. Third-party project adapters remain a separate compatibility layer.

Fabrication and assembly providers consume a validated release and return quotes. A paid order must always be a separate, explicitly confirmed operation.
