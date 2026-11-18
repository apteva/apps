# Apteva Code 0.16.0

Adds native merging. Branches can now be combined, compared and deleted,
changes can be cherry-picked or reverted, and conflicts come back as data an
agent settles with tool calls. Every existing tool keeps working as before,
except that switching branches with unsaved changes now auto-stashes them
instead of failing.

## Changes

- **`repos_merge`** (new): `start` merges `source` (branch, tag or revision)
  into the checked-out branch.
  - `mode`: `merge` (default; fast-forwards when possible unless `no_ff`, and
    otherwise records a revision with two parents), `squash` (one ordinary
    revision), `cherry_pick` (apply one revision) or `revert` (undo one
    revision).
  - Merges are three-way against the nearest common ancestor, per path and
    then per line, so edits to different lines of one file merge
    automatically. Binary files, and files over 1 MiB, changed on both sides
    always conflict.
  - A merge that cannot finish returns each conflict with its reason
    (`content`, `add_add`, `modify_delete`, `binary`, `too_large`) and the
    base, ours and theirs text, plus a preview with conflict markers. The
    working file keeps our side unless `markers=true`.
  - `resolve` settles one path with `choice` (`ours`, `theirs`, `base`,
    `delete`, or `working` to keep the file as edited) or with `content`.
    Content still containing conflict markers is refused. `continue` records
    the result; `abort` restores the branch head. `status` shows the merge in
    progress.
  - While a merge is in progress, checkpoints, restores that record a
    revision, branch switches, undo and Git auto-sync pause, so a half-merged
    tree is never saved or pushed.
- **`repos_branch`** (new): `list`, `create` (from any revision, branch or
  tag), `switch`, `delete` and `compare`.
  - `delete` refuses the checked-out branch, and an unmerged or auto-stashed
    branch unless `force=true`. It returns the head so the branch can be
    recreated; the deleted branch's log keeps its history safe from garbage
    collection.
  - `compare` gives ahead/behind counts, the merge base, whether a
    fast-forward is possible, and the paths changed since the base.
- **Auto-stash on switch.** Switching away from a branch with unsaved changes
  saves them as a stash revision for that branch, and switching back puts
  them back. If the branch moved meanwhile, the stash is re-applied with a
  three-way merge; if that would conflict, the stash is kept and the switch
  reports `stash_conflicts`. `repos_branch_switch` behaves the same way.
- **`repos_diff`** accepts branch and tag names as well as revision IDs. With
  `lines=true` it returns a unified patch per file with added and removed
  counts. `paths` limits the diff.
- **`repos_history`** takes `ref` (another branch, tag or revision) and `path`
  (only revisions that changed that file or folder; a merge that kept one
  side's version follows only that side). With `path` and `lines=true` it
  returns blame: the revision, actor, date and message that last changed each
  line, traced through both parents of merges.
- **REST:** `GET version/merge`, `POST version/merge/{start,resolve,continue,abort}`,
  `GET version/compare?from=&to=`, `GET version/diff?from=&to=&path=`,
  `GET version/blame?path=&ref=`, `GET version/history?ref=&path=`,
  `POST version/branches/delete`. A merge or checkpoint refused because a
  merge is in progress returns 409.

## Upgrade and rollback

There is no storage migration: merge revisions are ordinary revisions with
two parents, and 0.15.0 and 0.14.10 read them (history follows the first
parent). Two things are new on disk: `MERGE.json` while a merge is in
progress, and `stash/<branch>.json` for auto-stashes.
- Before rolling back, finish or abort any merge in progress, and switch to
  each branch that has an auto-stash so its changes come back.
- 0.15.0 does not know these files. After a rollback, a leftover stash stays
  on disk but is not restored, and 0.15.0's garbage collection may remove its
  revisions after 14 days.

## Validation

- Tier 1: 17 new tests.
  - Diff: the line diff is an optimal common subsequence (checked against a
    dynamic-programming LCS on 2,000 random cases); exact unified patch
    output; diff3 merging and conflict markers.
  - Merge: fast-forward, up-to-date and `no_ff`; a clean line-level merge with
    two parents; conflicts as data, and every write refused during a merge;
    resolve by `working`, `theirs`, `content`, `base` and `delete`; abort;
    `markers`; binary and add/add conflicts; squash, cherry-pick (including a
    repeat) and revert; unrelated histories.
  - Branches and history: auto-stash both ways, re-applied cleanly or kept on
    conflict; branch delete, compare, recreation after GC; path history and
    blame across a merge; line diff by branch name and against the working
    tree.
  - The full suite passes with `-race`.
- Tier 2: the real sidecar over MCP and REST. It covers branch create,
  switch with auto-stash and restore, compare, a conflicting merge (a
  checkpoint during it is refused), resolve by content, continue to a
  two-parent revision, REST merge status and blame, line diff between branch
  names, path history and branch delete.
- Tier 3: `scenarios/12-native-merge.yaml` passed all 18 checks with
  `opencode-go` / `kimi-k2.7-code` in 15 iterations on a clean spawned
  server. The agent created the branch, switched both ways, and ran
  `repos_merge` start → resolve → continue, with no Git and no failed calls;
  the merged file, clean `main`, no merge in progress and a two-parent head
  were verified over REST. `11-native-undo.yaml` still passes (10
  iterations).
- Rollback: a real-binary round trip (0.16.0 records a line-level merge;
  0.15.0 reads, diffs, exports and checkpoints on top of it; 0.16.0 blames
  across it) passed all checks.
