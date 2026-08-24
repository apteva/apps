# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.5 adds a physical Wiring workspace for Fritzing-style Arduino and breadboard tutorials. It owns an engine-independent part/pin/wire model, reference library, electrical compatibility checks, Arduino-to-physical-pin simulation, and deterministic SVG, PNG, tutorial JSON, and tutorial ZIP exports. Version 0.4's direct board editing, independent fabrication-file verification, autorouter, DC/RC/digital simulation engine, and Arduino-compatible virtual microcontroller lab remain available.

These capabilities build on the native manufacturing foundation: canonical `apteva-pcb/v1` source, typed operation batches, component bodies and footprint pads, net classes, copper zones, antenna/copper/component keepouts, differential-pair intent, connectivity-aware ERC/DRC, semantic diffs, footprint-aware SVG rendering, deterministic BOM generation, and traceable release ZIPs.

It has no external ECAD, routing, or simulation engine dependency. Storage and other first-party apps are selected through native `kind: app` bindings; external component-data, sourcing, fabrication, and assembly providers use native `kind: integration` bindings. The optional `firmware_executor` binding can delegate full toolchain compilation/execution to the first-party Functions app, while the built-in behavioral runtime stays available without it. The core model remains independent of every bound target.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel combines the source editor with interactive Layout and Wiring canvases and dedicated Route, Simulate, and Firmware workspaces. The Arduino Uno + LED starter creates a semantic tutorial programmatically; every illustrated jumper terminates at a named pin, and the same revision drives validation, code execution, display, and export. The native writers and verification parser produce illustrations and Gerber/Excellon without invoking Fritzing, KiCad, Flux, or another ECAD engine.

Manufacturing output is gated twice: first by native ERC/DRC, then by a separate parser that checks file syntax, termination, bounds, geometry counts, drill tools/holes, outline edges, and Gerber-job reconciliation. A pad-aware design must route each net node through a same-net trace or zone; invalid pad/drill geometry, copper clearance, keepout incursions, differential-pair gap/skew, incomplete routing, or malformed generated files block manufacturing and release creation. Autorouting never bypasses these checks. Designs created before v0.2 remain readable but receive explicit missing-pad-geometry warnings until upgraded.
