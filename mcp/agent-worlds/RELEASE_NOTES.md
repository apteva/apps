# Agent Worlds 0.6.0

Pixel Village is redrawn as a clean, bright isometric village.

- Every project is an island: a green with a well, buildings spaced well apart, winding footpaths from each door to the green, a pond with ducks, a garden, and trees thickening towards the coast.
- Eleven building types chosen from what each app does (hall, office, archive, post office, workshop, bank, studio, planner, records office, market, cottage), each with its own size, walls, furniture, and wall decor. The roof and sign carry the category colour.
- Buildings are open-walled, so you see who is working where. Agents walk in through the door to a work spot by the furniture and back out; walls are crossed only at doors.
- Cards above each building show name, status, call count, and time since the last call; working agents show their latest action.
- Space Station, System Graph, and Operations are unchanged.

Known limits: cards show call counts rather than app-specific metrics; colours come from the app category, not app branding; the village no longer has a day/night cycle.

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
