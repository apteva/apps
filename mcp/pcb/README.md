# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.9 turns the routed ESP32-C3/DHT22 board into a production-prototype fixture. Every populated component carries a frozen footprint revision, source hash, pad/pin count, courtyard, solder-mask/paste intent, compatible MPNs, and—where mechanically relevant—a versioned native 3D model contract. The geometric DRC now checks actual rotated pad polygons, mask slivers, via-to-via/trace/pad clearance, annular rings, slots, plating declarations, and polygonal board-edge clearance.

V0.9 also adds deterministic KiCad `.kicad_pcb` import/export with an independent structural reconciliation pass, AP242 faceted-BREP STEP assembly export, and an `apteva-pcb-production-readiness/v1` gate. Release ZIPs now include native source, Gerber/Excellon, KiCad exchange, both verification reports, STEP when mechanical data is present, a readiness report, and a physical bring-up plan. Four accessible DHT-board test points cover USB 5 V, 3.3 V, ground, and DHT data. The gate deliberately reports physical validation as pending until real boards are ordered and tested.

Version 0.5's physical Wiring workspace remains available for Fritzing-style Arduino and breadboard tutorials, with an engine-independent part/pin/wire model, reference library, electrical compatibility checks, Arduino-to-physical-pin simulation, and deterministic SVG, PNG, tutorial JSON, and tutorial ZIP exports. Version 0.4's direct board editing, independent fabrication-file verification, autorouter, DC/RC/digital simulation engine, and Arduino-compatible virtual microcontroller lab also remain available.

These capabilities build on the native manufacturing foundation: canonical `apteva-pcb/v1` source, typed operation batches, component bodies and footprint pads, net classes, copper zones, antenna/copper/component keepouts, differential-pair intent, connectivity-aware ERC/DRC, semantic diffs, footprint-aware SVG rendering, deterministic BOM generation, and traceable release ZIPs.

It has no external ECAD, routing, or simulation engine dependency. Storage and other first-party apps are selected through native `kind: app` bindings; external component-data, sourcing, fabrication, and assembly providers use native `kind: integration` bindings. The optional `firmware_executor` binding can delegate full toolchain compilation/execution to the first-party Functions app, while the built-in behavioral runtime stays available without it. The core model remains independent of every bound target.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel combines the source editor with interactive Layout, Wiring, and Mechanical canvases and dedicated Route, Simulate, and Firmware workspaces. The Raspberry Pi starter creates an enclosure-ready mechanical reference programmatically; its immutable revision drives validation, 3D display, envelope, DXF, and STEP export. The routed DHT22 starter exercises frozen footprints, test points, fabrication parsing, KiCad exchange, and the production gate. The Arduino Uno + LED starter creates a semantic tutorial programmatically. Native writers and parsers produce every artifact without invoking Fritzing, KiCad, Flux, or another ECAD engine; KiCad is a compatibility format, not a runtime dependency.

Manufacturing output is gated twice: first by native ERC/DRC, then by a separate parser that checks file syntax, termination, bounds, geometry counts, drill tools/holes, outline edges, and Gerber-job reconciliation. A pad-aware design must route each net node through a same-net trace or zone; invalid pad/drill geometry, copper clearance, keepout incursions, differential-pair gap/skew, incomplete routing, or malformed generated files block manufacturing and release creation. Autorouting never bypasses these checks. Designs created before v0.2 remain readable but receive explicit missing-pad-geometry warnings until upgraded.
