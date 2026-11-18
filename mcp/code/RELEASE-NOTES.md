# Apteva Code 0.18.1

Fixes Git imports of repositories that hold files above the editing limit.

## Fixes

- **Large files in version history.** After `repos_git_import` cloned a
  repository with a file above `max_file_size_mb` (default 10 MB), building
  Code's own version history read every file under that editing limit, so
  the tool returned an error even though the clone had succeeded. History
  stopped working for that repository. The bug was already present in
  0.14.10. `github.com/apteva/apps` hits it (a 16 MB binary and a 66 MB
  `.wasm` file).
  - History now stores and restores such files up to the repository limit
    (`max_repo_size_mb`, default 1024 MB). Files at or under the editing
    limit are read exactly as before.
  - Writing, uploading and reading files through the editing tools keeps the
    editing limit, as do changes applied back from a workspace.

## Upgrade and rollback

No migration or format change. Installs that raised `max_file_size_mb` to
work around the import error can set it back to the default after
upgrading. Rolling back to 0.18.0 brings the error back for such
repositories.

## Validation

- Tier 1: a new test snapshots, changes and restores a file 1 MB above the
  editing limit. The full suite passes with `-race`; the integration suite
  passes.
- End to end on a local 0.18.0 install, where the bug was found: with the
  file limit raised as a workaround, `github.com/apteva/apps` was imported,
  an edit in `mcp/inventory` was tested in a Go workspace (vet and tests
  pass), committed and pushed to a branch on GitHub from Code.

# Apteva Code 0.18.0

Adds native remotes: Code repositories can now clone, fetch from and push to
each other without Git, in the same project or across Apteva installs. Every
existing tool keeps its arguments and output.

## Changes

- **`repos_remote`** (new): one tool with these actions.
  - `add`: a remote `name` (e.g. `origin`) with either `from_slug` (another
    repository in this project) or `url`. A `url` is another install's
    repository API, `https://host/api/apps/code/api/repos/<slug>`; plain
    http is accepted only for loopback; an optional `project_id` can be
    given.
  - `list`, `remove`.
  - `fetch`: downloads new revisions and records the remote's branches and
    tags. A remote branch then reads as `<remote>/<branch>` anywhere a ref
    is accepted (`repos_merge` source, `repos_diff`, `repos_history`,
    `repos_branch` compare). Pull is fetch followed by `repos_merge
    source=origin/main`.
  - `push`: sends a branch (default the checked-out one) to `remote_branch`
    (default the same name).
  - `clone`: creates a new repository with the source's full history (same
    revision IDs), all tags, `main` checked out and an `origin` remote.
- **Push rules.**
  - Pushes are fast-forward only. If the remote has revisions you have not
    fetched, or the histories diverged, the error says to fetch and merge
    first.
  - A protected remote branch refuses pushes; it only changes through change
    requests.
  - A checked-out remote branch is updated only if its working tree has no
    unsaved changes, and its files are then updated. Every push is logged
    as `push` in the remote's move log.
- **Verified transfer.**
  - Every received blob, folder tree, flat tree and revision is checked
    against its hash, and every revision's parents and blobs must be present.
  - Entries named `..`, `.` or containing `/` are rejected.
  - Transfers only include revisions the other side lacks, and skip folders
    and files already in the other side's head trees.
  - One transfer is capped at 64 MiB.
  - History in the 0.14 format travels too.
- **Credentials.** For a `url` remote, `token` is passed with each call and
  sent as a Bearer header; it is never stored.
- **Garbage collection** keeps every revision a remote branch or tag points
  at.
- **REST** (served by every Code repository, used by remotes):
  - `GET version/sync/refs`
  - `POST version/sync/fetch`
  - `POST version/sync/receive`, which returns 409 when the branch moved, is
    protected or a merge is in progress
  - `GET version/remotes`

## Not included

`repos_git_pull` is still fast-forward only. Line-level history tools beyond
blame (rebase, cherry-pick of a range) and delta compression of stored
objects are not part of this release.

## Upgrade and rollback

There is no migration; remote settings live in `remotes.json` with the native
objects. Clones and pushed revisions are ordinary revisions. On a rollback to
0.17.0, remotes are not visible, and revisions known only through a remote
branch may be garbage-collected after 14 days. A real-binary round trip
passed: 0.18.0 clones and pushes; 0.17.0 reads both repositories and
checkpoints; 0.18.0 pushes that work afterwards.

## Validation

- Tier 1: 5 new tests.
  - Clone, fetch and a repeat empty fetch, pull by merge, push to a new
    branch and repeated, protected, not-yet-fetched, diverged, dirty and
    clean checked-out remote branches, and tracking refs surviving GC.
  - Six tampering cases rejected (blob, tree entry, revision message,
    revision tree, missing parent, `..` path).
  - An incremental fetch sends one revision and one blob, and not the
    unchanged folder.
  - Cloning 0.14-format history.
  - The HTTP transport's wire format: Bearer token, `project_id`, clone and
    push, and URL validation.
  - The full suite passes with `-race`.
- Tier 2: the real sidecar.
  - Clone from the same project with shared revision IDs.
  - Fetch and pull-merge of `origin/main`.
  - A push over HTTP to the sidecar's own sync API: a wrong token is
    refused; pushes to a new branch and to checked-out `main` update its
    files.
  - Tokens are absent from the stored remotes.
- Tier 3: `scenarios/14-native-remote.yaml` passed all 14 checks with
  `opencode-go` / `kimi-k2.7-code` in 13 iterations on a clean spawned
  server. The agent cloned, edited and pushed back to `main` with no Git and
  no failed calls; the original's file, clean status, history and `push`
  move were verified over REST.
