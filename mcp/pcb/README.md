# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.2 moves the local-first vertical slice into native manufacturing geometry: canonical `apteva-pcb/v1` source, immutable revisions, typed operation batches, component bodies and footprint pads, copper zones, antenna/copper/component keepouts, differential-pair intent, connectivity-aware ERC/DRC, semantic diffs, footprint-aware SVG rendering, deterministic BOM and Gerber X2/Excellon generation, traceable release ZIPs, required native Storage persistence, optional Nexar component/BOM lookup, HTTP/MCP APIs, a project panel, and a chat card.

It has no external ECAD engine dependency. Storage and other first-party apps are selected through native `kind: app` bindings; external component-data, sourcing, fabrication, and assembly providers use native `kind: integration` bindings. The core model remains independent of every bound target.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel uses the source editor for complete authoring and a layer-aware board canvas showing real pad geometry, bodies, traces, zones, keepouts, drills, dimensions, selection details, and visibility controls. The native writer produces Gerber/Excellon without invoking KiCad, Flux, or another ECAD engine. Later editor milestones can add schematic capture, object handles, interactive routing assistance, a larger verified footprint library, 3D rendering, and third-party format adapters without changing the ownership boundary or revision model.

Manufacturing output is gated by native validation. A pad-aware design must route each net node through a same-net trace or zone; invalid pad/drill geometry, copper clearance, keepout incursions, differential-pair gap/skew, and incomplete routing block Gerber and release creation. Designs created before v0.2 remain readable but receive explicit missing-pad-geometry warnings until upgraded.
