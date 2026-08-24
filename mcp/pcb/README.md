# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.3 adds a deterministic constraint-aware autorouter, a native DC/RC/digital simulation engine, persisted probes and waveform results, and an Arduino-compatible virtual microcontroller lab with Serial, GPIO, I2C, and virtual sensors. These build on the native manufacturing foundation: canonical `apteva-pcb/v1` source, immutable revisions, typed operation batches, component bodies and footprint pads, net classes, copper zones, antenna/copper/component keepouts, differential-pair intent, connectivity-aware ERC/DRC, semantic diffs, footprint-aware SVG rendering, deterministic BOM and Gerber X2/Excellon generation, and traceable release ZIPs.

It has no external ECAD, routing, or simulation engine dependency. Storage and other first-party apps are selected through native `kind: app` bindings; external component-data, sourcing, fabrication, and assembly providers use native `kind: integration` bindings. The optional `firmware_executor` binding can delegate full toolchain compilation/execution to the first-party Functions app, while the built-in behavioral runtime stays available without it. The core model remains independent of every bound target.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel combines the source editor with a layer-aware board canvas and dedicated Route, Simulate, and Firmware workspaces. Autorouting is proposed as reviewable typed operations before it becomes an immutable revision; simulation displays rail state and waveforms; firmware runs show the serial monitor, GPIO state, and I2C transactions. The native writer produces Gerber/Excellon without invoking KiCad, Flux, or another ECAD engine. Later editor milestones can add schematic capture, richer direct-manipulation handles, a larger verified footprint library, 3D rendering, and third-party format adapters without changing the ownership boundary or revision model.

Manufacturing output is gated by native validation. A pad-aware design must route each net node through a same-net trace or zone; invalid pad/drill geometry, copper clearance, keepout incursions, differential-pair gap/skew, and incomplete routing block Gerber and release creation. Autorouting never bypasses these checks. Designs created before v0.2 remain readable but receive explicit missing-pad-geometry warnings until upgraded.
