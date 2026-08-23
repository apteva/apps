# PCB Studio

PCB Studio is Apteva's native electronics-design app. Version 0.1 is a complete local-first vertical slice: canonical `apteva-pcb/v1` source, immutable revisions, typed operation batches, native ERC/DRC, semantic diffs, deterministic SVG and BOM generation, traceable release ZIPs, Storage persistence, optional Nexar component/BOM lookup, HTTP/MCP APIs, a project panel, and a chat card.

It has no external ECAD engine dependency. Provider integrations are boundary adapters for live component data, sourcing, fabrication, and assembly.

## Develop

```sh
GOWORK=off go test ./...
bun run scripts/build-panels.ts --app pcb
```

The panel uses the source editor for complete v0.1 authoring and a native board canvas for immediate review. Later editor milestones can add schematic capture, object handles, routing assistance, footprint editing, Gerber/Excellon, and third-party format adapters without changing the ownership boundary or revision model.
