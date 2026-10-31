# Agent Worlds 0.7.2

The dashboard widget fills its space and can be moved around.

- The widget has the page's pan and zoom: drag to pan, the zoom out / fit / zoom in buttons, and pinch or Ctrl/Cmd+scroll to zoom. A plain scroll still scrolls the dashboard.
- The village opens framed on its islands, and clicking an agent or building opens the full page on the same view.
- On high-DPI screens (2x and above) the village and the space station now scale to fill their space instead of rounding down to the next whole number. Before, standard and tall widgets showed the same small island. 1x screens keep whole-number scaling. This also makes the full page larger on high-DPI screens.
- Widget heights are now compact 240 px, standard 340 px, and tall 520 px.

# Agent Worlds 0.7.1

Packaging fix for 0.7.0, which could not be installed or upgraded to.

- The 0.7.0 release tarballs contained macOS metadata files (`._bin` and others), so servers rejected them with "unsupported artifact path". The tarballs are now packed without them. The app itself is unchanged from 0.7.0.

# Agent Worlds 0.7.0

Agent Worlds can now sit on the project dashboard.

- New "Agent world" dashboard widget: a live, read-only view of the project's agents at work. Its settings choose the view (Pixel village, Space station, System graph, or Operations), the source (main server, environments, or a remote server), and the height. Half and full width are supported.
- The widget draws through the same renderer as the page, so every view looks and moves the same in both places. It polls and animates only while it is on screen.
- Clicking the widget opens the full page on the same view and source. The page now accepts `?view=` and `?source=` in its address.

# Agent Worlds 0.6.1

Agents now work at the part of a building that matches the tool they call.

- Tool names pick the station: in the CRM, contact and list tools go to the card catalog, deal and pipeline tools to the pipeline board, and conversation, reply, and draft tools to the inbox desk. Unknown tools can use any spot.
- CRM and other people apps get a larger front office with an inbox desk and mail rack, a contact card catalog, and a pipeline board.
- Storage, mail and chat, build, and billing buildings also have stations (shelves vs. upload desk, sending vs. reading, forge vs. log monitor, counter vs. safe).
- The label over a working agent now says what it is doing in words, such as "searching contacts" or "updating conversation status".
- A director keeps busy agents readable and close to live: calls to one station less than 2.5 s apart are one visit; a one-off call to another building in the middle of an activity flies there as an envelope instead of a walk; an agent more than 2 s behind sprints, more than 6 s behind dashes with shorter stays, and skips visits whose successor has already started. Sprinting agents trail speed streaks.
- Fixed: furniture briefly disappeared behind a building's back wall when an agent walked behind it, and furniture in a building just behind another building could be hidden by its own walls.

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
