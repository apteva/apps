# Remote rendering through an owned renderer component

Status: implemented in Composer v0.11.0 (renderer worker `composer-renderer` 0.11.0)
Replaces: the `render_host_id` / Instances remote executor (`exec_remote.go`)

## Decision summary

Composer no longer talks to Instances. Offloaded rendering runs in a private
**`renderer` component** that Composer declares in its manifest. Apteva Server
deploys it wherever the operator places it: on the Composer host (local
placement) or on a remote host through a registered execution backend, such
as Instances' `native` backend. Instances is still a machine provider, but as
a platform backend the operator picks; Composer never references it.

Composer keeps owning compositions, render history, caching, Storage writes,
progress, and the panel. The renderer is a bounded plan worker: it runs one
Composer-built plan per job and reports a durable receipt.

## Why change

The previous remote path (`exec_remote.go`) SSHed a generated bash script per
render through `instances.instance_run_command`. Problems:

1. **Most work could not go remote.** Only V1 FFmpeg renders used it. Browser
   V2 renders and `web-canvas-1` procedures (the slow part: Chrome capture took
   nearly all of the ~5-minute 60 s tutorial render; the final composite took
   8 s) always ran in the sidecar.
2. **Composer's own credential left the platform.** The script embedded
   `APTEVA_OUTBOUND_TOKEN`/`APTEVA_APP_TOKEN` to fetch fonts and upload.
3. **No lifecycle.** No drain, no progress, no confirmed cancellation; each
   render was a fresh 30-minute SSH session.
4. **Not reproducible.** FFmpeg came from BtbN's moving `latest` release.
5. **Hard dependency and UI coupling** on the Instances app and its inventory.

## What was built

### Declaration

`apteva.yaml` and the embedded `manifestYAML` declare:

```yaml
components:
  renderer:
    protocol: apteva.composer-renderer/v1
    required: false
    capabilities: { passive_start: true, drain: true, replica_safe: false,
                    worker_coordination: false, routing: key }
    manifest:   # identical to renderer/apteva.yaml except source.ref
      name: composer-renderer
      runtime:
        kind: source
        source: { repo: github.com/apteva/apps, ref: <exact commit>, entry: mcp/composer/renderer }
```

`TestRendererDeclarationMatchesWorkerManifest` keeps both declarations equal
to `renderer/apteva.yaml` apart from the ref, which Server also enforces.

### Worker (`mcp/composer/renderer`)

Modeled on Media's executor (same lifecycle, journal and admission code):

- Managed lifecycle: passive start, `activate`/`drain`/`status` bound to the
  deployment identity.
- Tools: `capabilities`, `submit`, `get`, `cancel`, `acknowledge`.
  - `submit` is idempotent on `job_id` and rejects a different plan under the
    same ID.
  - Receipts are journaled; a restart never replays an attempt.
  - Weighted admission (browser jobs weigh 2, encodes 1; default capacity 2).
  - `get` reports progress from `APTEVA_PROGRESS:<fraction>` lines.
- Pinned dependencies, verified by SHA-256 and provisioned in the worker's data
  directory:
  - FFmpeg BtbN `autobuild-2026-10-25-13-00` (only `ffmpeg` and `ffprobe` are
    unpacked) at mount, unless found on PATH or configured.
  - Chrome headless shell 156.0.8078.80 and Node.js v24.22.1 on the first
    browser job, unless a local Chrome/Node 22+ is found or configured.
- Missing Chrome system libraries are reported by name (`ldd`) in
  `capabilities` and in the failing job. The native backend does not install
  host packages.
- Chrome gets `--no-sandbox` only when the worker runs as root, and
  `--disable-dev-shm-usage` on Linux.
- The plan environment never contains the worker's token or gateway
  credentials.

### Parent

| Path | On the renderer (`execution_mode=component`) |
| --- | --- |
| V1 FFmpeg (`exec_component.go`) | Inputs fetched by the worker, bundled fonts embedded in the plan, same ffmpeg graph as local |
| Browser V2 (`browser_component.go`) | Same document and CDP capture script as local; assets become worker files |
| `web-canvas-1` procedures | Same document builder; artifact uploaded directly, duration checked with ffprobe |
| Native V2, script procedures | Stay in the sidecar |

- **Durability** (`component.go`, migration `007`): every plan and its job ID
  are persisted in `renderer_calls` before submission. A lost response is
  resolved with `get`, never with a second submission. Renders carry an owner
  key (`render:<id>:<stage>`), so a render resumed after a sidecar restart
  re-attaches to its worker job.
- **Transfers without Composer's token** (`component_transfer.go`): each plan
  gets a random grant, stored only as a hash and scoped to the project, the
  `/.composer/` folder, a byte budget, a request count and an expiry. The
  worker uploads through `/api/apps/composer/_install/<id>/renderer-io/...`,
  which forwards to the Storage callback proxy with Composer's own token. Parts
  travel base64-encoded because public gateways can reject binary bodies. The
  grant is revoked when the receipt is recorded.
- **Gateway**: the worker reports the gateway it reaches. The relay is
  addressed through it, and signed Storage URLs minted for the public URL are
  rebased onto it.
- **Selection**: `execution_mode` (`local` | `component`). Installs that had a
  `render_host_id` keep offloading: previously offloaded rendering never
  silently consumes local CPU. Without a deployed renderer those renders fail
  with a deploy hint. `composition_render(executor)` accepts `local` and
  `component`; `remote` is kept as an alias.
- **Legacy cleanup**: renders still `rendering` on an Instances host when the
  migration runs are failed with `legacy_executor_cleanup_required`, not
  replayed.
- **Panel**: the host picker is gone. The strip shows whether the renderer is
  deployed (with browser readiness in the tooltip) and a local/renderer
  selector that writes `execution_mode`.

## Deploying the renderer

Component deployment is an operator action on the Composer installation:

```
POST /api/apps/installs/<composer install>/components/renderer
Idempotency-Key: <unique>
{"placement": "local" | {"type": "backend", ...},
 "approved_permissions": ["net.egress"]}
```

Then set Composer's `execution_mode` to `component`. For a backend placement
the host must reach the gateway URL in the placement (a public https URL, or a
loopback URL through a tunnel).

## Host sizing

Browser capture needs Chrome plus FFmpeg in memory at once. 1080p30 needs at
least 2 vCPU and 4 GB RAM; a 512 MB host can only manage short, small
captures, using swap. Provisioned dependencies need about 700 MB of disk:
FFmpeg about 270 MB, Chrome headless shell about 300 MB, Node about 120 MB.

## Later

- Procedure script runtimes (Python/Bun/Go) on the worker.
- Native V2 renders on the worker (would need the engine as a shared package).
- More than one renderer once Server allows several executions per component.
- A panel "Deploy renderer" action backed by the operator's session.
