# Games assets and content — v0.4.0

Games now owns a versioned game-content catalog alongside the existing player,
source, delivery and reporting features. Upgrade Games to v0.4.0 and bind the
optional companion apps below to enable imports and generation.

## Supported kinds

All thirteen kinds are first-class, validated asset versions. They are not aliases
for an unvalidated file upload.

| Kind | Source and validation | Rendition / engine handling |
|---|---|---|
| `sprite` | PNG, canvas bounds, alpha requirement, palette, frame geometry | Deterministic PNG atlas; Unity sprites / Godot SpriteFrames |
| `spriteset` | PNG with named frames, durations, pivots and animation clips | Same atlas contract, playable Unity clip and Godot animation resources |
| `rig` | Ordered 2D bones, pinned sprite/frame attachments, timed transform tracks | Validated JSON; Unity data asset / Godot data resource |
| `tileset` | Sprite frames with matching dimensions for native Godot tile import | PNG atlas; Unity sprites / Godot TileSet plus SpriteFrames |
| `style` | Palette and art-direction rules | JSON style data; palette enforced when dependent sprites bake |
| `material` | Portable `sprite2d` model, RGBA tint, optional pinned sprite texture | JSON; Unity Sprites/Default material / Godot canvas ShaderMaterial |
| `font` | Parsed standalone TTF/OTF and family/fallback references | Original font bytes; native engine font resources |
| `sfx` | WAV, OGG or MP3 source | OGG for Unity/Godot; MP3 for generic targets |
| `music` | WAV, OGG or MP3 source | Same audio pipeline |
| `stream` | Sequence or layered playlist of pinned music/SFX with gain/loop settings | Validated JSON; engine data resource |
| `locale` | Language tag, string keys, text/context and plural categories | Validated JSON; engine data resource |
| `table` | Schema version, typed columns, required values, unique string row key | Validated JSON; engine data resource |
| `blob` | Bounded opaque file | Byte-preserving export / engine data resource |

Rigs describe 2D transforms, not skinning or mesh deformation. Streams are portable
audio playlists/layers, not an HLS transcoding service. Locale plural categories
are validated, but a game's localization library selects the appropriate category.
Materials support sprite tinting, not arbitrary shader graphs or PBR interchange.
Font fallback references are retained in the manifest; the game applies its desired
fallback chain. Runtime code consumes rigs, streams, locales and tables according
to the documented JSON schema; importers do not invent game behavior. Scenes and
prefabs stay in the engine repository. Kiln is not claimed as supported without its
actual source/format contract.

## Ownership and installation

- Games contains the catalog, typed specs, immutable versions, baking, reviews,
  manifests, environment heads and the Assets dashboard tab.
- Bind Storage to import an existing file ID. Uploading directly from the Assets
  panel also works without Storage.
- Bind Media Studio **0.10.61 or later** to generate candidates.
  Its new `media_asset_source` tool exports bytes through **Media Studio's own**
  Storage binding, with generation identity and SHA-256. Both apps must use these
  releases or later for this flow. Media Studio must have Storage bound before
  generation; a thumbnail or expiring provider URL is not accepted as a master.
- Code continues to own the engine project. Deploy continues to own build commands,
  artifact tests, signing and publishing. No Code or Deploy code changes are needed.
- Both apps pin app-sdk v0.77.0. Audio baking additionally requires
  `ffmpeg` and `ffprobe` installed on the Games runner, with libvorbis/libmp3lame.
  A missing codec/tool fails explicitly; it does not return a mislabeled rendition.

## Versions, retention and release rules

`games_asset_save` requires an `asset_id`, `name`, `kind`, `spec`, and
`expected_parent` (empty for a new asset). It accepts one source: `storage_id`,
`content_base64`, or a retained `source` SHA. Updating a stale parent fails. The
asset ID and kind are stable; names, specifications and provenance are retained
per version. `games_asset_versions` lists history. Old versions can be inspected
and baked without moving the editing head.

Every dependency is `{ "asset": "id", "version": "exact-version-digest" }`.
Styles are pinned too. Dependent rig frames, material textures, stream tracks and
font fallbacks are checked against the exact referenced kinds. A manifest cannot
mix two versions of one asset, even across engines, or omit a required runtime
dependency rendition.

Source and baked bytes are kept in a game-scoped, content-addressed archive in the
Games SQLite database. This is an intentional **bounded retention copy**, in
addition to the original Storage/provider source: Storage currently allows hard
purges and has no cross-app retention pin contract. Deleting that original cannot
break a frozen release. SQL triggers reject modifications/deletion of retained
versions, blobs, renditions and manifests. There is no automatic garbage collection.
Back up the Games database consistently, including its WAL when applicable.

Limits: 25 MiB per source/output, 512 MiB of unique bytes per game, 10,000 asset
versions per game, 4096×4096 source/atlas dimensions, 256 sprite frames, 256 selected
renditions and 512 dependency versions per manifest, 2 MiB manifest JSON, and
128 MiB uncompressed release files. Large catalogs need a future Storage retention
contract before these limits should be expanded. Two bakes can run concurrently.
Audio is limited to ten minutes and baked with a 45-second timeout. It is not
silently truncated to fit those limits.

Baking uses fixed frame order, nearest-neighbor integer scaling (1–4), no rotation
or trimming, two pixels of edge extrusion, and preserved normalized top-left
pivots. Palette mismatches fail rather than being silently quantized. A generated
PNG must actually contain transparency when required; PNG conversion alone does
not remove an opaque background. Visual animation/style review is still necessary.
Audio encoder identity is retained in the rendition, and the produced bytes are
retained; portability does not depend on reproducing a provider generation later.

`games_content_freeze` accepts exact `rendition_ids`. A manifest can contain Unity,
Godot and generic renditions together. It includes exact version and file hashes;
private generation prompts and provider details are not sent to game clients.
`games_content_approve` records a review note for the exact digest through the
protected admin/MCP surface. Caller identity is retained when supplied by the
platform; older callers are recorded only as `authenticated-admin`. This content
review is not Deploy's named-approver policy and does not grant store release approval.

`games_content_promote` atomically moves `dev`, `staging` or `prod`, requiring
`expected_head`. Production requires a review of that exact manifest. Rollback
selects a retained, reviewed manifest and uses the current head as `expected_head`.
Content rollout percentages are not implemented in this version.

## Generation

Add a `recipe` with `prompt`, optional `provider`, `model`, `size`, `duration`,
`source_images` and provider `options`. Generate with `games_asset_generate` using
an exact `version_id` and stable `request_key`. This is an explicit paid-provider
operation; saving a recipe does not generate. Sprite generation forces PNG and
adds the pinned style instructions. Artwork/audio generation uses Media Studio's
existing provider support; it does not claim automatic font or rig generation.

Intent and a unique request marker are persisted before dispatch. Retrying the
same key returns its state and does not repeat a lost provider call. Inspect
`games_asset_jobs`; `games_asset_generation_sync` verifies the generation marker,
installation identity, byte size and checksum, then saves a new source version.
If the dispatch reply was lost, supply the exact inspected `generation_id` to sync.
There is no automatic retry of an uncertain generation. A provider output that
fails sprite validation remains in Media Studio for correction; it is not published.

## Engine workflow

1. Save/import all assets and review generated candidates.
2. Bake versions for each `target` (`generic`, `unity`, `godot`), `engine_version`,
   `platform` and scale. Include compatible runtime dependencies.
3. Freeze renditions together and download the locked ZIP from Games. It includes
   `manifest.json`, `content.lock.json`, exact rendition files and, for Unity targets,
   `bundle.gamescontent`.
4. Import those files into the engine project and commit the lockfile/importer
   version in Code. This puts the content digest inside Code's immutable source
   snapshot and therefore inside Deploy's build inputs.
5. Run engine import and content tests in Deploy's configured preparation/test
   commands, then build and release normally. The Games environment head does not
   prove a binary was deployed or downloaded from a store.

The Bun CLI `client/export-content.ts` downloads the same files without a ZIP
library. Set `GAMES_APP_URL` (ending in `/api/apps/games`), `GAMES_PROJECT_ID`,
`GAMES_GAME_ID`, and a server-side `GAMES_CONTENT_TOKEN` at runtime; run:

```sh
bun run client/export-content.ts --digest <exact-manifest-sha256> --out <new-directory>
```

It verifies every hash/size, refuses redirects and unsafe paths, stages downloads,
and refuses an existing destination. It also writes `bundle.gamescontent` for
Unity. Never put this admin credential in a game build. Review updates in Code;
when replacing a Unity bundle in a stable project path, preserve its `.meta` file.

### Godot 4.5.1

Copy `engines/godot/addons/apteva_games` into the project. Put the exported files
under `res://GameContent`, then run:

```sh
godot --headless --editor --path <project> --import
godot --headless --path <project> --script addons/apteva_games/import_content.gd -- --content=res://GameContent --platform=desktop
```

The importer requires the exact engine major/minor/patch from the rendition and
verifies **all** files before writing. Resources go under
`GameContent/imports/<digest>/`; prior content stays intact. Use
`AptevaContentSprite` with the imported SpriteFrames to apply pivots and logical
scale. Structured resources expose their validated JSON as `get_meta("data")`.
The receipt records the manifest, engine and produced resources.

### Unity 6

`engines/unity` is a local UPM package with runtime, importer and Editor-test
assemblies. Add it to the engine project. Put exported content under `Assets`,
with `bundle.gamescontent` equal byte-for-byte to `manifest.json`. The importer checks the lockfile and every source digest.
Its platform defaults to `desktop`; set it for other rendition platforms.

The bundle exposes sprites, timed clips, data, materials, fonts and audio. The
`AptevaContentAnimator` applies variable frame durations. Sprite subasset IDs are
based on stable logical names, not atlas positions. `Tests/Editor` includes a
native import/reimport test for pivots, timing and stable references. Unity is not
installed on this workstation, so native Unity validation remains unrun. This is
an importer implementation, not a claim of tested support for every Unity pipeline.

## Runtime content and tests

Player-authenticated routes expose only content that has been published to prod:

- `GET /v2/games/{game_id}/content/head` resolves the current production digest.
- `GET /v2/games/{game_id}/content/{digest}?download=manifest` returns exact JSON.
- `GET /v2/games/{game_id}/content/{digest}/blobs/{sha}` serves a file only if it is
  in that published manifest. Source masters and draft/staging content stay private.

Previously published digests remain fetchable for installed older builds. The
TypeScript client in `client/content.ts` takes an **exact** digest, checks every
file, and supports an engine-owned persistent cache for offline use. It never
silently substitutes a newer head. Rig simulation, gameplay consumption of data,
and asset compatibility with older game code belong to the engine project.

Validation includes all thirteen kinds, malformed types/dependencies, immutable
history and stale edits, review/promotion/rollback, retained bytes, lost-generation
recovery, Media Studio project isolation, client integrity/offline behavior and UI
selection races. Native Godot import tests build all thirteen fixture kinds and
reload the resulting resources in a fresh engine process:

```sh
GOWORK=off GAMES_GODOT_BIN=/path/to/godot go test -run TestAssetsGodotImport -v
```

These are local fixture tests. No real provider spend or store publication is
performed during validation.
