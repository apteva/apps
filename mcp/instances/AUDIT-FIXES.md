# Instances 0.4.42 release

Release based on `instances/v0.4.41` (`941615e106bbad834ee2c58f5259d8e7da317d5d`). Release manifests and source refs are set to 0.4.42. No production deployment or cloud resource mutation was performed while developing this patch.

## Changes by audit finding

| Findings | Change |
|---|---|
| 1, 5–7 | Fix the nil dereference on network failure. Track/cancel provisioning workers; observe readiness without writing it from wait requests; fence lifecycle transitions; retain create ownership until the creator settles; persist destroy options; recover destroy and rollback work. Unknown create outcomes remain recorded and cannot be silently forgotten. |
| 2, 18–21 | Quote file paths literally; write uploads atomically; bound SSH handshake, session-open, execution, transfer, and tunnel-dial waits. Retry commands only before dispatch. Preserve tunnel replies after half-close. Kill local process groups on timeout and use rooted file operations with regular-file/read-size checks. Pool identities include target/account/key material. |
| 3, 16 | Require a provider-confirmed attachment and an exact serial/by-id identity before preparing a disk; remove size-based discovery and forced formatting. Refuse ambiguous/unreadable signatures. Serialize fstab work, update atomically, match the entire managed marker, and verify the mounted filesystem before unmounting. |
| 4 | Allocate instance IDs from an AUTOINCREMENT reservation table, including imported IDs. Missing-row updates fail. |
| 8–9 | Dedibox uses the recorded provider account throughout delivery, installation, and deletion. Parse resource envelopes instead of unrelated action/request IDs; explicitly extract volume IDs. |
| 10–11, 17 | Reject boot-retention promises unsupported by an adapter. Inventory AWS root EBS disks and observe DeleteOnTermination before destructive overrides; verify retained disks detach. Only owned Elastic Metal Flexible IP IDs qualify for cleanup. Detached boot disks can be deleted or attached as data disks. |
| 12 | Build with Go 1.27.1 and x/crypto 0.56.0. Update the SDK from v0.71.0 to v0.75.0, verified as a descendant of v0.73.0 on the SDK main branch. |
| 13–15 | Implement Linode attach. Allocate AWS device slots from occupied paths, retaining provider attachment names separately from guest device paths. Persist volume intent before mutation and inventory before creation; confirm create/attach/detach/resize/delete via provider reads. Recover accepted operations without replaying them when already complete. Preserve the create-to-attach boundary and serialize conflicting operations. |
| 22–24 | Scope catalogs/defaults to provider account and zone. Correct Canonical AWS owners and expose both architectures; filter compatible type/region/image combinations. Fix RunPod boot defaults and skip cloud-init requirements for native-install adapters. |
| 25–26 | Serialize credential rotation, atomically retain old-key revocation intent and new expiry, retry failed revocations, and persist object-storage creation stages/identities before proceeding. Queue known-resource cleanup after failed Scaleway creation; retain unknown IAM outcomes. Record pending Vultr resources even when credentials are absent and reconcile readiness. Refuse adoption of an existing bucket during create. |
| 27–28 | Use unique benchmark files, literal quoting and OS-specific commands. Compare provider networking/state and actual volume observations. Inventory all configured Scaleway zones with bounded parallelism/pagination; expose errors/partial results and call unowned resources “untracked”. |
| 29–32 | Fix wire types and null-disk rendering. Abort/ignore stale catalogs and plan requests; enforce architecture compatibility. Keep operation errors visible, prevent busy-modal backdrop dismissal, and expose retained/detached disks in a global Volumes view. Remove the unused legacy card. Add strict type checking before production panel builds, account-scoped TTL/single-flight catalog caching, eight-way metrics concurrency, hidden-page polling suppression, and compute-estimate currency subtotals. |

## Validation

- All 125 Go test cases passed with `go test -race -count=1 -timeout=90s ./...`, including 23 adopted audit regressions and 11 additional failure/recovery tests.
- `go vet ./...` passed.
- Three Bun/React DOM tests passed: architecture aliases, null disk metrics, and delayed old-account catalog responses.
- Strict TypeScript checking passed. The production panel build passed the dashboard React import-surface verifier.
- Built the Linux amd64 binary with Go 1.27.1. Source and binary govulncheck scans reported **zero reachable vulnerabilities** and zero vulnerable imported packages.
- The scanner additionally reports GO-2026-5932 for the unused `x/crypto/openpgp` package in a required module. Instances imports SSH, not OpenPGP; no fix is available for that retired package.
- The single-flight test makes 12 concurrent identical catalog requests and verifies one upstream request, then verifies a second provider account gets its own request.

Tests use temporary databases, local SSH/TCP servers, fake provider responses and harmless shell-command stubs. No real disk was formatted. These checks do not establish live provider compatibility or production load performance.

## Explicit provider boundaries

- An upstream create/rotation response can be lost before it supplies an identity. Such work remains recorded with its account and stage, and blind create retries/forgetting are refused. An operator must reconcile the provider account when identity cannot be proven. Cleanup retries retain unresolved IAM stages instead of claiming success.
- The installed AWS integration cannot change DeleteOnTermination. A conflicting retention override is refused; change the flag in AWS and retry so the app can observe it. Unsupported boot-retention requests on other adapters are also refused.
- Scaleway credentials still use project-scoped ObjectStorageFullAccess because the installed integration does not expose bucket-policy management. The create flow and credential response disclose this scope. A dedicated project provides isolation. Bucket-only credentials require an integration/API expansion and a live policy test; this patch does not claim to provide them. See [Scaleway's IAM and bucket-policy model](https://www.scaleway.com/en/docs/object-storage/api-cli/combining-iam-and-object-storage/).
- Pricing remains a catalog-based compute estimate; storage, IPs, taxes and unpriced resources are excluded and labeled accordingly.

## Reproduce

From this app directory:

```sh
bun install --frozen-lockfile
bun run typecheck
bun test ui
env GOWORK=off go test -race -count=1 -timeout=90s ./...
env GOWORK=off go vet ./...
env APTEVA_DASHBOARD_DIR=/path/to/dashboard bun run ../../scripts/build-panels.ts --app instances
env GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/instances-fixed .
env GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@latest -mode=binary /tmp/instances-fixed
```

Migration 008 adds operation/revocation journals and provider device paths. Back up the application database before release deployment. This branch contains the rebuilt panel artifacts as well as its source.
