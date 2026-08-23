# PCB Studio

Use PCB Studio when the result is an electrical design, circuit board, bill of materials, sourcing workflow, or manufacturing handoff—not merely a picture of a board.

## Native model

PCB Studio owns the complete design engine. Its canonical source is `apteva-pcb/v1`: deterministic JSON with integer-nanometre geometry, integer-microdegree rotations, stable object IDs, layers, components, pins, nets, copper traces, vias, and rules. It does not call KiCad, Flux, tscircuit, or another ECAD engine.

Every edit creates an immutable revision. Start with `pcb_examples`, then:

1. Create a board with `pcb_designs_create`.
2. Edit it with `pcb_operations_apply` for normal changes, using the current revision as `expected_parent_id`. Use `pcb_revisions_create` when replacing the complete definition.
3. Run `pcb_validate` and resolve every error. Warnings are visible but do not block a release.
4. Use `pcb_render` for an SVG preview and `pcb_bom_generate` for the grouped CSV bill of materials.
5. Use `pcb_release_create` to make a traceable ZIP with native source, validation, hashes, SVG, and BOM. Failed ERC/DRC blocks this step.

## Component data and sourcing

Local component, footprint, pin, and MPN fields remain authoritative. If the optional `component_data` provider is bound, `pcb_components_search` can search parts and `pcb_bom_source` can match revision MPNs against current distributor data. Provider results enrich a design but never mutate it automatically.

## Compatibility

The native source remains the editable truth. Import/export compatibility is implemented as deterministic Apteva-owned adapters around that model. Adapters preserve the original source in Storage, emit a conversion report, and declare whether a format is detected, viewed, imported, edited, exported, or semantically round-tripped. V0.1 exports SVG, BOM CSV, and a native release ZIP. Gerber/Excellon and third-party project adapters are the next compatibility layer; never claim fabrication readiness until those outputs pass golden-file and round-trip tests.

Fabrication and assembly providers consume a validated release and return quotes. A paid order must always be a separate, explicitly confirmed operation.
