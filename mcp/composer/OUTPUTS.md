# Saved composition outputs

Composer 0.8 adds three saved export presets beneath each composition: `song`
(audio), `image_video`, and `full_clip`. Composer owns output state; Persona
Studio 0.2 displays the same output panel through its canonical Composer link.
Media Studio 0.10.62 supplies read-only `media_job_get` for asynchronous assets.
The SDK pin is v0.77.1 (v0.77.0 is its ancestor).

## UI

Save a composition, then open **Outputs**. Select the shared audio master in
**Shared audio master**, or use the composition's existing audio. Each output
has its own format, aspect/resolution, excerpt, visual plan, status, history,
and last successful download. Excerpts use source seconds and leave the master
intact. Video defaults stop at the end of the visual plan if the audio is longer.

Image video requires still-image scenes; it never silently creates videos or
turns a moving-video plan into stills. **Visual scenes** accepts one `storage:N`
reference or generation prompt per line. Preparing scenes edits the output's
visual plan; save before exporting. Source audio in generated visual plans is
muted so the selected master is the soundtrack. Advanced visual plans use the
existing Edit schema. Native V2 image/shape/text scenes retain their layout and
animation timing, including excerpt offsets.

**Generate / export** reuses selected and completed assets. **Prepare new song
version** explicitly chooses a fresh generation version; exporting generates it.
No dependent video automatically regenerates. The open panel automatically resumes waiting attempts; **Check generation**
also resumes the same attempt after navigation. Failed attempts retain the last successful artifact.

## API and MCP

All routes use `/composition/{id}/outputs` and require an owning `project_id`.
MCP callers may use their current project context. A conflicting current project
cannot be overridden with arguments. Storage validates asset access in that
project before generation.

| Method and suffix | MCP tool |
| --- | --- |
| GET `/` | `composition_outputs` |
| PATCH `/{kind}` | `composition_output_update` |
| PATCH `/master` | `composition_output_master` |
| POST `/{kind}/estimate` | `composition_output_estimate` |
| POST `/{kind}/render` | `composition_output_render` |
| GET `/{kind}/renders` | `composition_output_history` |
| POST `/{kind}/adopt` | `composition_output_adopt` |

Output writes require `expected_revision`; master writes use `shared_revision`
from the list response as their expected revision. Render/adopt requests also
require a caller-generated `idempotency_key`. Repeating a render key returns its
terminal result or resumes a `waiting_ai` attempt from the frozen input snapshot.
New settings or source inputs never rewrite an existing attempt. History returns
up to 100 attempts, newest first. List responses distinguish `latest_attempt`
from `latest_successful_render`. `out_of_date` compares the successful attempt's
input fingerprint and output revision with the current definition.

Settings: `{format, resolution, aspect, fps, excerpt_start?, excerpt_end?}`.
An output plan is `{timeline:{tracks:[...]}}`, containing visuals/overlays only.
`{}` inherits the canonical visuals. Shared master input is a Clip, for example:

```json
{"asset":{"type":"audio","src":"storage:83760"},"length":30}
```

### Generation coordination and retries

A durable unique asset claim is keyed by project, composition, generation
parameters, reference assets, and explicit asset version. It excludes output
kind, export format and excerpt length. Concurrent outputs share that claim.
Queued jobs are polled by id; polling never calls `media_generate`. Existing
selected sources and ready assets are reused exactly.

An ambiguous submission is blocked rather than automatically billed again.
Inspect/reconcile the provider result, then select its Storage source, or
explicitly choose a new AI `cache_key` and clear `asset.src`, `ai.storage_id`,
`ai.job_id` and ready status for only the failed asset. Successful shots retain
their cached assets. On sidecar restart, interrupted exports become failed and
completed previews remain available. Uncertain asset submissions stay blocked.

Generation costs are recorded once per asset claim. The list separates shared
audio from visual generation, and attempts expose the generation costs they
initiated. Unknown provider costs remain `null`; estimates report missing
asset counts and unknown prices without submitting generation. Render costs
are unknown when the executor supplies no nonzero recorded cost. Detailed
provider price quoting is a later addition.

## Compatibility and migration

Migration `005_outputs.sql` only adds tables, columns and indexes. It preserves
all original composition JSON, render ids, snapshots and Storage references.
Presets are created lazily; no extra top-level compositions are created. Legacy
create/update/render APIs still operate on the original edit/output pair.
Legacy `latest_render` and list summaries select legacy renders (`output_id IS
NULL`) so an audio preset cannot replace an MP4 preview for an older client.

No historical render is automatically classified from its title or today's
output settings: those are insufficient evidence of its original contents.
Use `composition_output_adopt` with `render_id` for a completed render in the
same composition, or `storage_id` and optional `duration_ms` for retained media.
Adoption validates file type/access and copies references into a new output
history row. The original render/media remain untouched. Imported artifacts
have unknown input versions until exported again.

For the report's example, after verifying ids and project ownership on the
actual deployment: select vocal Storage 83760 as the 30-second master of
composition 72; adopt Storage 83764 as `full_clip` with duration 10000 ms; save
its excerpt end as 10; leave `image_video` ungenerated. The alternate instrumental
remains in Storage and can be selected as another master later. These production
operations are intentionally not run by this source change.

Before rollout, back up Composer and Persona Studio databases with SQLite's
backup facility and retain the previous binaries. To roll back, restore the
pre-migration database backup together with the prior binaries; retain any new
Storage files separately. Test restoration before production migration. Do not
merge or delete projects based on matching titles.

## Validation

Go tests cover output selection, concurrency/idempotency, pending-job reuse,
partial retries, failure isolation, input staleness, asset/project access,
legacy compatibility, additive migration/adoption, restart recovery, costs,
V2 scenes and real FFmpeg audio excerpts. Generation providers are fakes.
Persona Studio tests cover canonical links; Media Studio tests cover read-only
project-scoped job lookup. The output panel is shared by both app bundles.
