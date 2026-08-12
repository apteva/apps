# Design Studio

Design Studio is Apteva's agent-native parametric 2D/3D CAD app. It stores safe `apteva-design/v1` operation graphs as immutable revisions, evaluates them with Replicad + OpenCascade.js, validates exact geometry, renders browser meshes, and exports STEP, STL, 3MF, GLB, and traceable manufacturing ZIPs.

It does not execute user-authored Python or JavaScript. Numeric geometry fields support a deliberately small arithmetic-expression language over declared parameters.

## Architecture

- Go + `app-sdk`: project isolation, MCP tools, HTTP API, revisions, build runs, artifacts, validation, and Storage integration.
- Replicad/OpenCascade.js: exact B-rep geometry in a one-shot Bun process.
- Embedded JS/WASM assets: the Go sidecar remains deployable as one binary.
- Pinned Bun fallback: use configured `bun_path`, then `PATH`, then a SHA-256-verified Bun 1.2.22 download into the app data directory.
- React panel: source editing, parameter controls, software 3D preview, measurements, checks, exports, and artifact downloads.

## Develop

```sh
cd runner
bun install
bun run build

cd ..
go test ./...

cd ../../..
bun run scripts/build-panels.ts --app design
```

The engine integration test performs a real OpenCascade boolean build and writes STEP/STL/mesh output. It skips only when Bun is not available.

## V1 boundary

V1 generates both exact solids and derived triangle meshes from a deterministic parametric source. External text-to-3D/image-to-3D models and paid manufacturing providers are intentionally adapter seams, not bundled providers. A future mesh-source revision should retain model/provider/prompt provenance and run mesh repair checks; a manufacturing adapter should quote from a validated package and require an explicit human approval before ordering.
