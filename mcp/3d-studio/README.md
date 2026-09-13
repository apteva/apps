# 3D Studio 0.1.3

First-party polygon modeling for game assets, implemented with Apteva's Go
app-sdk **v0.81.0**. No Blender, CAD kernel, Three.js, or JavaScript server runtime.
The browser editor and MCP tools share our Go mesh engine; the browser runs it
as WebAssembly in a worker. Bun only builds the frontend and test tooling.

## What works

- Indexed polygon meshes with stable vertex/face IDs, derived adjacency, and
  concave-face triangulation. Geometry uses meters, right-handed coordinates,
  and Y-up. Document schema: `apteva-3d/v1`.
- Box/cylinder primitives; face/vertex selection by IDs, position bounds, and
  face normals; selection growth by shared-vertex adjacency.
- Translation, positive scale, rotation, and proportional transforms with
  smooth/linear falloff and connected-component restriction.
- Region extrusion (shared vertices, boundary walls, named output caps),
  individual-face centroid inset, flattening, direct vertex patches, face
  deletion, and bounded vertex welding.
- Editable mirrored copies, duplication, renaming, and per-object colors.
- Atomic command batches, immutable revisions, optimistic concurrency,
  idempotent commits, revision-bound selection handles, and preview candidates.
- Custom WebGL viewport with orbit/zoom, face/vertex picking, shift-add selection,
  wireframe, top/front/side views, modeling controls, preview/save/discard,
  revision restore, and GLB download.
- Native Go 640px PNG previews for unattended agents, including selection
  overlays. No headless browser is needed to produce these images.
- GLB export with separate named nodes, centered pivots, flat normals, and
  solid-color metallic/roughness PBR materials. Editable source JSON export.
- A small car example built by exactly the same engine commands as agent edits.

## Layout

| Path | Responsibility |
|---|---|
| `engine/` | Portable Go geometry, selection, editing, validation, GLB and PNG |
| `cmd/wasm/` | Browser-worker bridge to the same Go engine |
| `main.go`, `tools.go` | SDK lifecycle, HTTP and MCP adapters |
| `store.go`, `migrations/` | Project-scoped SQLite persistence |
| `ui/` | React editor, custom WebGL viewport, built panel and WASM |
| `skills/` | Agent-facing modeling workflow |
| `dev/` | Optional local preview build entry |

## Build and test

```sh
cd apps/mcp/3d-studio
GOWORK=off go test -race ./...
./scripts/build.sh
```

The panel build explicitly selects production JSX and rejects development-runtime imports.
The preview loads the shipped panel bundle against the dashboard vendor import map,
so it exercises the same React module boundary as installed apps.

The build creates `ui/engine.wasm`, its matching Go `wasm_exec.js`, the panel
bundle, and `/tmp/apteva-3d-studio`. Built UI files ship with the source app so
installing it only needs a Go build. There is no runtime Bun dependency.

`GOWORK=off` intentionally verifies the published SDK pin rather than the older
local SDK checkout overlaid by the workspace's `go.work`. The root workspace
file has not been changed. The pin was checked against fetched tags and commit
ancestry: v0.81.0 resolves to c4b16c3be59a833f722b47ee456889968021f087.

Run the app with normal SDK-injected environment variables. For an isolated
local preview, build the dev page using the workspace dashboard's React deps:

```sh
bun run dev/build.ts
APTEVA_PROJECT_ID=studio-dev APTEVA_APP_PORT=18086 \
  APTEVA_BIND_HOST=127.0.0.1 DB_PATH=/tmp/3d-studio-dev.db \
  /tmp/apteva-3d-studio
```

Open `http://127.0.0.1:18086/ui/preview.html`. The dev page calls the real Go
sidecar and uses a separate database; it is not a mock service. Generated dev
page files are gitignored. Installed apps use `/ui/StudioPanel.mjs` through
Apteva's project panel.

## MCP surface

| Tools | Purpose |
|---|---|
| `studio_capabilities` | Actual command schemas, limits, coordinates and workflow |
| `assets_create`, `assets_list`, `assets_get` | Create and retrieve editable models |
| `mesh_inspect`, `mesh_select`, `selection_expand` | Inspect and select geometry |
| `mesh_edit` | Atomic edit, either committed or previewed |
| `mesh_edit_commit`, `mesh_edit_discard` | Review and resolve a preview |
| `revisions_list`, `revisions_restore` | History and non-destructive restore |
| `assets_validate`, `assets_render` | Geometry checks and PNG visual feedback |
| `assets_export`, `artifacts_list` | GLB/JSON exports and saved images |

HTTP uses the same dispatcher: `POST /api/tools/<tool_name>` with a JSON body.
Artifacts are downloaded from `GET /api/artifacts/<id>`. In the dashboard these
paths are behind `/api/apps/3d-studio`. Project identity comes from the SDK;
conflicting project headers or query parameters are rejected. MCP permissions
are declared in `apteva.yaml`, with asset-scoped resource bindings.

### Example edit

Create a `box`, then select its top face:

```json
{
  "asset_id": 1,
  "revision_id": 1,
  "name": "roof",
  "query": {"node_id": "body", "kind": "face", "normal": [0, 1, 0]}
}
```

Use the returned selection ID in `mesh_edit`:

```json
{
  "asset_id": 1,
  "expected_revision_id": 1,
  "mode": "preview",
  "selections": {"roof": "<saved-selection-id>"},
  "commands": [
    {
      "op": "extrude", "node_id": "body", "selection": "roof",
      "direction": [0, 1, 0], "distance": 0.4,
      "result_selection": "cap"
    },
    {
      "op": "transform", "node_id": "body", "selection": "cap",
      "scale": [0.7, 1, 0.8]
    }
  ]
}
```

Render the candidate with `assets_render`, then commit it with
`mesh_edit_commit` and a unique `request_key`. Use actual IDs returned by the
app. A repeat of the same commit input/key returns its existing revision and
selection handles. Different input with the same key is rejected.

Selections are tied to a specific revision. Edits invalidate removed elements;
there is no speculative remapping. Named surviving/new selections are saved
with the new revision and returned as fresh handles. UI drags orbit the camera;
geometry edits use the inspector and preview controls in this release.

## Persistence and limits

Six SQLite tables hold assets, immutable revision snapshots and command logs,
selection handles, preview candidates, and artifact bytes. The low-poly first
release stores bounded source JSON and artifact BLOBs in the SDK database;
external content-addressed Storage integration is deferred. No extra app binding
is needed to get started.

Edits are synchronous and bounded: 64 commands per batch, 256 nodes, 20,000
vertices and faces per document, 128 vertices per face. Proportional edits and
welding have additional work limits. Preview candidates expire after one hour
and are cleaned when creating later previews. Failed edits do not advance the
head. Restoring an old revision creates a new child; it does not delete history.

## Deliberate first-release boundaries

- No persistent modifier stack, seam-welded mirror, subdivision, bevel, loop
  cut, sculpting, rigging, UVs, textures, animation, collision, or LOD generation.
- Inset interpolates each face toward its centroid by a fraction. It is not a
  constant-distance region inset, and difficult polygons may be rejected.
- Mirror creates a baked, separately editable node. Overlapping nodes are
  allowed. Geometry validation does not prove absence of all self-intersections.
- Normals are generated from triangles; arbitrary polygon edits can produce
  nonplanar surfaces. Open boundaries are warnings, not automatic failures.
- Previews use orthographic flat-shaded rendering; the “3D” view is an orbitable
  orthographic camera. It is not a perspective or physically based render.
- SDK v0.81.0 wraps tool results in JSON text. Render tools return authenticated
  PNG artifact URLs; vision clients must fetch those images to inspect them.
- No scene-node parenting or live multi-user editing yet. Independent object
  pivots are centered at export; transforms are baked into editable vertices.
- No background job queue in this bounded release. Large production assets
  will require durable jobs and binary blob storage before raising limits.

Validation includes kernel topology tests, export structure, PNG rendering,
atomicity, retries, project isolation, preview expiry, HTTP/MCP agreement, and
concurrent-edit races. Game-engine-specific import behavior still needs target
engine acceptance testing.
