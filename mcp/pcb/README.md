# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.4 adds direct board editing and independent fabrication-file verification to the deterministic autorouter, DC/RC/digital simulation engine, and Arduino-compatible virtual microcontroller lab introduced in 0.3. Components can be selected, dragged, nudged, and rotated on a snapped canvas; traces and vias can be authored or removed by net and layer; edits support undo/redo and are committed as immutable revisions. Generated Gerber X2, Excellon, and Gerber-job files are independently parsed and reconciled against the native design before fabrication or release succeeds.

These capabilities build on the native manufacturing foundation: canonical `apteva-pcb/v1` source, typed operation batches, component bodies and footprint pads, net classes, copper zones, antenna/copper/component keepouts, differential-pair intent, connectivity-aware ERC/DRC, semantic diffs, footprint-aware SVG rendering, deterministic BOM generation, and traceable release ZIPs.

It has no external ECAD, routing, or simulation engine dependency. Storage and other first-party apps are selected through native `kind: app` bindings; external component-data, sourcing, fabrication, and assembly providers use native `kind: integration` bindings. The optional `firmware_executor` binding can delegate full toolchain compilation/execution to the first-party Functions app, while the built-in behavioral runtime stays available without it. The core model remains independent of every bound target.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel combines the source editor with an interactive layer-aware board canvas and dedicated Route, Simulate, and Firmware workspaces. Layout edits stay staged until explicitly saved; server-side routing, simulation, validation, and export actions cannot accidentally run against a stale revision. Autorouting is proposed as reviewable typed operations before it becomes an immutable revision; simulation displays rail state and waveforms; firmware runs show the serial monitor, GPIO state, and I2C transactions. The native writer and verification parser produce and inspect Gerber/Excellon without invoking KiCad, Flux, or another ECAD engine. Later editor milestones can add schematic capture, richer selection and shove-routing tools, a larger verified footprint library, 3D rendering, and third-party format adapters without changing the ownership boundary or revision model.

Manufacturing output is gated twice: first by native ERC/DRC, then by a separate parser that checks file syntax, termination, bounds, geometry counts, drill tools/holes, outline edges, and Gerber-job reconciliation. A pad-aware design must route each net node through a same-net trace or zone; invalid pad/drill geometry, copper clearance, keepout incursions, differential-pair gap/skew, incomplete routing, or malformed generated files block manufacturing and release creation. Autorouting never bypasses these checks. Designs created before v0.2 remain readable but receive explicit missing-pad-geometry warnings until upgraded.
