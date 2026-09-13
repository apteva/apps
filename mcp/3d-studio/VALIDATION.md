# Validation — 2026-09-13

- `GOWORK=off go test -race ./...`: passed for the app and engine.
- `GOWORK=off go vet ./...`: passed.
- `scripts/build.sh`: native Go binary, Go WebAssembly engine, and panel built.
- Real SDK sidecar boot: manifest validated; SQLite migration applied; HTTP and
  MCP mounted on a loopback preview port using a separate database.
- Browser workflow: created car; selected a roof face by clicking the viewport;
  previewed an extrusion through WebAssembly; saved a revision; restored the
  original from history. Also selected one vertex, previewed proportional
  movement with a 0.8m falloff radius, and discarded the candidate.
- Final car fixture: 10 objects, 152 vertices, 100 polygon faces, 264 triangles.
- Independent Khronos `gltf-validator@2.0.0-dev.3.10` check of the car GLB:
  **0 errors, 0 warnings, 0 infos, 0 hints**. Output: 26,248 bytes.
- Native PNG previews decoded and checked for nonempty geometry in front, side,
  top and orbit views. Final browser rendering visually inspected.

The validator was installed in a temporary verification directory, not added as
an app dependency. No production instance was modified. Godot/Unity/Unreal import
acceptance remains a separate integration check; this release exports standard
GLB without engine-specific collision or LOD metadata.
