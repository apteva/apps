# Agent Worlds 0.4.1

Clicking an agent now opens its details while keeping the current project/team, other agents, page, camera, and replay position intact. The inspector shows the selected agent’s actual status, project, threads, apps, and recent activity.

- Share agent inspection across Village, System Graph, Operations, and agent cards in project overviews.
- Highlight the selected agent and provide an explicit close button; Escape closes details before navigating out.
- Keep details beside the visualization on wide screens and prevent inspector sections from overlapping.
- Preserve selection across representation switches, and clear it when changing source or project.

Validation: TypeScript checks; 19 UI/model tests with expanded selection regression coverage; standalone Go race tests; four native builds; package checksums and native health/API/UI smoke checks; browser checks with real local agents.
