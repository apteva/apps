# Apteva Code 0.20.1

Fixes the safety check of `repos_git_branch_delete` for local-only deletes.

## Fixes

- Deleting only the local copy of a branch counted as safe when the
  repository's `origin/<branch>` tracking ref contained its commits. That ref
  is only as fresh as the last fetch, so a branch deleted on origin since
  could let unmerged work be deleted without `force`. Origin is now asked
  directly (`ls-remote`):
  - still there and containing the commits: deleted, `merged_into` names it;
  - gone: the stale tracking ref is dropped and the delete is refused
    unless the work is merged elsewhere or `force=true`;
  - unreachable: refused unless merged elsewhere or forced, and the error
    says origin could not be reached.

## Validation

- `TestGitBranchDelete_LocalMergedUnmergedAndPushed` now deletes the branch
  on origin behind the repository's back and checks the stale tracking ref
  no longer lets unmerged work go.
- `TestGitBranchDelete_FetchDropsBranchesDeletedOnOrigin`: fetch prunes
  branches deleted on origin from the branch list.

# Apteva Code 0.20.0

Adds branch deletion for Git-backed repositories. Existing tools keep their
arguments and output.

## Changes

- **`repos_git_branch_delete`** (new; REST `POST /api/repos/<slug>/git/branches/delete`):
  deletes a branch locally, on origin, or both (`where=local|remote|both`,
  default `local`).
  - A branch whose commits are not in the checked-out branch or origin's
    default branch is refused unless `force=true`. Deleting only the local
    copy of a branch that is still on origin counts as safe.
  - A branch on origin this repository never fetched counts as unmerged.
  - The checked-out branch and origin's default branch are never deleted,
    even with `force`.
  - Nothing is deleted until every check passes. Origin is deleted first and
    only if its branch still points where it was checked (a leased push), so
    a refused or lost race leaves the local branch untouched.
  - The result returns each deleted tip SHA and what it was merged into
    (`merged_into`, empty when forced); `repos_git_branch_create
    start_point=<sha>` recreates it. Refusals are 409 on REST.
  - Logged as `branch_delete` and emitted as `repo.git.branch_deleted`.

## Validation

- Tier 1: `TestGitBranchDelete_*` against a real bare remote through the
  service (merged, unmerged, pushed, remote-only, both, unfetched, default
  branch, checked-out branch).
- Tier 2: `TestSidecar_GitRelease_*` deletes a release branch on origin and a
  local branch over MCP and refuses origin's default branch over MCP and REST.

# Apteva Code 0.19.0

Adds what was missing to release from Code: Git tags, pushing to a release
branch, and GitHub pull requests. Existing tools keep their arguments and
output; `repos_git_push` only gains optional arguments and a `pushed` field.

## Changes

- **`repos_git_tag`** (new), for Git-backed repositories:
  - `create`: an annotated tag on HEAD or `target` (commit, branch or tag),
    with an optional `message`. Names are checked with Git's ref rules.
  - `list`: local tags, newest first, with the commit each points at.
    `compare_remote=true` adds `on_remote`: `same`, `different` or `absent`.
  - `push`: publishes one tag to origin. If origin already has the name on
    another commit, the push is refused (409 on REST): published tags are
    never moved. Pushing the same tag again is a no-op.
  - `delete`: removes a local tag only; origin is never changed.
- **`repos_git_push`** gains `source` (a local branch other than the
  checked-out one) and `remote_branch` (another branch name on origin), for
  example `remote_branch=release/x` to advance a release branch from main.
  - Only new branches and fast-forwards are accepted. A rewind or a diverged
    history returns a clear error (409 on REST); force push is still not
    available.
  - The response's new `pushed` field gives the source, remote branch and
    commit. Upstream tracking is only set when pushing the checked-out branch
    to its own name, as before.
- **`repos_git_pull_request`** (new), for repositories whose origin is on
  github.com, through the repository's GitHub connection (or the install's
  bound GitHub connection). The token stays on the platform.
  - `create`: from `head` (default the checked-out branch) into `base`
    (default the remote's default branch), with `title`, `body`, `draft`. The
    head branch must already be on origin; `unpushed_commits` says when the
    local branch is ahead of it.
  - `list` (`state`, `head`, `base`, `limit`), `get` (`number`).
  - `merge` (`number`, `method` merge|squash|rebase, `expected_head_sha` to
    merge only what was reviewed, `commit_title`, `commit_message`).
  - `close` (`number`).
  - GitHub's error messages are returned as-is (for example a 422 "A pull
    request already exists").
- **REST**: `GET|POST /api/repos/<slug>/git/tags`,
  `GET|POST /api/repos/<slug>/git/pull-requests`, and `source` /
  `remote_branch` on `POST /api/repos/<slug>/git/push`.
- **Private Git servers**: `GIT_SSL_CAINFO` in the sidecar's environment is
  now passed to Git, so a self-hosted server with a private certificate
  authority can be trusted.

## Upgrade and rollback

No migration. Tags and branches are ordinary Git refs, so rolling back to
0.18.1 keeps them; only the new tools and arguments disappear.

## Validation

- Tier 1: tag creation (name rules, duplicates, missing targets, a backport
  tag on an older commit), listing, push, re-push, and refusal to move a
  published tag; pushes to a new release branch, a fast-forward, a branch
  that is not checked out, and refused rewinds and diverged histories,
  against a Git server over HTTP. Pull requests against a fake GitHub:
  every action's inputs and outputs, validation before any call, an unpushed
  head, a 422 and a 401. The full suite passes with `-race`.
- Tier 2: the real sidecar against a local Git server over HTTPS: import,
  commit, tag, compare with origin, push, refusal to move the tag (MCP and
  REST), main pushed to a release branch without moving main, a refused
  rewind (MCP and REST), and the pull-request guard for a non-GitHub origin.
  The integration suite passes.

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
