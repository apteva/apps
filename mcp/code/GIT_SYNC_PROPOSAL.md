# Code-owned, provider-neutral Git synchronization

Status: implemented in the Code app for v0.6.0

## Decision

Git synchronization belongs in the Code app. The server is not a Git transport
and provider integrations are not asked to proxy Git protocol traffic.

The Code app runs a constrained system Git client over standard HTTPS. For a
private remote, it resolves the connection already bound to the Code install
and obtains its credentials through the existing app credential callback. The
credential is passed to Git through a temporary `GIT_ASKPASS` process; it is
not put in the remote URL, command arguments, Git configuration, or the Code
database.

This requires no server or app-sdk source change. Code's app-sdk dependency is
updated to a version that already exposes `GetConnectionCredentials`, and the
manifest requests `platform.connections.read_credentials`. The manifest also
declares `git` as a required runtime binary, so installation fails clearly on a
host that cannot provide it instead of failing later when Code mounts.

## Product model

A Code repository is either local or Git-backed. The same Code file tools and
dev runtime operate on its working tree in both cases. Git metadata is kept
outside the visible repository tree and `.git` is rejected by file APIs,
excluded from listings, quotas, and exports, and removed on hard delete.

The generic surface supports:

- clone from a public or authenticated HTTPS Git remote;
- safely attach an existing Code repository to a remote;
- status with branch, upstream, ahead/behind, changes, and conflicts;
- fetch and fast-forward-only pull;
- local commit and non-force push;
- bounded diff and log;
- list, create, and switch local branches.

The original GitHub archive importer remains available for compatibility. The
new Git import dialog may use GitHub for repository discovery, but the clone
and subsequent synchronization use the provider-neutral Git path. GitLab and
Bitbucket credentials are adapted in the same Code-owned credential layer.

## Safety boundaries

- Only `https://` remotes are accepted in the first release. Embedded
  credentials, query strings, fragments, local/file transports, SSH, and Git
  external transports are rejected.
- Provider credentials are host-scoped: GitHub to `github.com`, Bitbucket to
  `bitbucket.org`, and GitLab to the connection's configured instance.
- Git is invoked by absolute executable path with argument arrays, a sanitized
  environment, disabled hooks, disabled submodule recursion and LFS smudging,
  timeouts, and capped output. No raw Git-command API is exposed.
- Code writes and working-tree-changing Git operations share a per-repository
  lock.
- Pull refuses dirty, conflicted, detached, or non-fast-forward updates. Push
  never exposes force.
- Connecting an existing repository never replaces differing local files. It
  commits them to `apteva/local-before-connect` and reports that reconciliation
  is required.

## Persistence

`007_git_sync.sql` stores non-secret remote configuration and a Git-operation
audit trail. Git refs and the working tree remain authoritative. Each remote
row stores only its bound connection ID/provider slug and canonical HTTPS URL;
credentials are resolved just in time for each network operation.
