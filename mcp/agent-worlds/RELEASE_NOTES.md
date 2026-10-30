# Agent Worlds 0.5.0

Visualizations are now themes over one shared model, and the pixel map is rebuilt from scratch as a real pixel-art world.

- Four themes: System Graph, Operations, Pixel Village, and Space Station. Model, layout, theme, and renderer are separate layers, so a new theme only paints.
- New canvas pixel engine: 16-pixel tiles at native resolution with whole-number scaling, seeded layouts with plazas, paths, ponds, gardens, and forests, and A* routes.
- Village: buildings styled by what each app does, market stalls for integrations, cutaway interiors when agents work inside, character sprites with walk cycles and emotes, handoff envelopes, and a local-time day/night cycle with lit windows and lamps.
- Space Station: deck plates and corridors over a starfield, modules and docking pods, flight-suited crew.
- At instance scope every project is its own village or deck on one map; click one to zoom in.
- Scene errors no longer expose upstream details; the topology cache is bounded and swept.
- Builds against the published app-sdk v0.109.0 instead of a vendored copy.

# Agent Worlds 0.4.1

Clicking an agent now opens its details while keeping the current project/team, other agents, page, camera, and replay position intact. The inspector shows the selected agent’s actual status, project, threads, apps, and recent activity.

- Share agent inspection across Village, System Graph, Operations, and agent cards in project overviews.
- Highlight the selected agent and provide an explicit close button; Escape closes details before navigating out.
- Keep details beside the visualization on wide screens and prevent inspector sections from overlapping.
- Preserve selection across representation switches, and clear it when changing source or project.

Validation: TypeScript checks; 19 UI/model tests with expanded selection regression coverage; standalone Go race tests; four native builds; package checksums and native health/API/UI smoke checks; browser checks with real local agents.
