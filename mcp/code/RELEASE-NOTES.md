# Apteva Code 0.17.0

Adds native change requests (Code's pull requests) and protected branches,
so work can be reviewed and approved before it lands. Every existing tool
keeps its arguments and output.

## Changes

- **`change_requests`** (new): one tool with these actions.
  - `create`: `title`, `source` branch, `target` (default `main`), `body`,
    linked `issues`. Only one open request per source and target pair.
  - `list`: by state (`open`, `merged`, `closed` or `all`).
  - `get`: reviews, approvals, whether it would merge cleanly, and why it
    cannot merge yet. Pass `lines=true` for patches since the merge base.
  - `comment`: optionally on a `path` and `line`.
  - `review`: `approve`, `request_changes` or `comment`.
  - `merge`: `merge` (two parents) or `squash`, optionally `delete_source`.
  - `close` and `reopen`.
- **Review rules.**
  - Each reviewer's latest review counts.
  - An approval counts only on the source branch's current head, so a new
    revision needs fresh approval.
  - `request_changes` blocks until that reviewer approves.
  - Authors cannot approve their own request.
  - Merging checks the approvals against the exact head it merges; if the
    source moves in between, the merge is refused.
- **Merging without checking out.** A request merges without the target being
  checked out. If the target is checked out, it must have no unsaved changes,
  and its files are updated. Conflicts block the merge and are listed with a
  hint: merge the target into the source with `repos_merge`, resolve, then
  retry.
- **Protected branches.** `repos_branch action=protect name=<branch>
  required_approvals=<n>` (and `unprotect`).
  - A protected branch refuses checkpoints, restores that record a revision,
    `repos_merge` onto it, `repos_undo` and deletion. It only moves when a
    change request is merged (logged as `change_request.merge` or
    `change_request.squash`).
  - Editing files on it is still allowed, and Git auto-sync can still record
    on it.
  - REST returns 409 for refused writes.
- **Work branches.** `repos_branch action=create switch=true` starts a branch
  at the checked-out head and checks it out at once, carrying unsaved
  changes. This is how an agent moves work off a protected branch.
- **REST:**
  - `GET` / `POST version/change-requests`
  - `GET version/change-requests/<n>[?lines=1]`
  - `POST version/change-requests/<n>/{comment,review,merge,close,reopen}`
  - `POST version/protection`
- **Storage:** migration `018_change_requests.sql` adds the
  `repo_change_requests` and `repo_change_request_notes` tables. Protection
  is stored in `protection.json` with the native objects.

## Upgrade and rollback

The migration only adds tables. Merge revisions created by change requests
are ordinary revisions. On a rollback to 0.16.0, protection is not enforced
(0.16.0 does not know `protection.json`) and change requests are not visible.
Both come back when 0.17.0 is restored. A real-binary round trip passed:
0.17.0 merges a request into protected `main`; 0.16.0 reads and blames it and
checkpoints on top; 0.17.0 enforces protection again and still lists the
request.

## Validation

- Tier 1: 4 new tests.
  - Every guarded write is refused on a protected branch, while auto-sync is
    still allowed.
  - Work branches carry unsaved changes.
  - Merging without a working tree: merge and squash, a checked-out target
    clean or with unsaved changes, and a stale source head.
  - Change requests from create to merge: validation, duplicates, blockers,
    self-approval, request-changes then approve, stale approvals,
    `delete_source` kept for a checked-out source, merged-request guards.
  - Conflicts block the merge; close and reopen; merging after the conflict
    is resolved on the source branch.
  - The full suite passes with `-race`.
- Tier 2: two agents against the real sidecar. Checkpoints on protected
  `main` are refused over MCP and REST (409); a work branch carries the edit;
  a merge without approval is refused; the second agent approves; a new
  revision makes the approval stale (REST detail with patches); re-approval
  and a squash merge by the reviewer; `main`'s head and move log; the REST
  merged list.
- Tier 3: `scenarios/13-change-request.yaml` passed all 15 checks with
  `opencode-go` / `kimi-k2.7-code` in 11 iterations on a clean spawned
  server. The agent protected `main`, worked on a separate branch, opened
  and merged the request, with no Git and no failed calls. `main`'s files
  and head were verified through REST blame, history and moves.
