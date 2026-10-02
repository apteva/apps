# Proposal: Generic agent communication authorization

**Status:** draft for implementation planning  
**Target:** A2A app, with a reusable policy model for future MCP/app calls  
**Compatibility target:** A2A `v0.6.x` → next minor release

## Summary

Apteva needs an explicit answer to “which agent may send work to which other
agent?” The current A2A app has a coarse local boundary: agents must be in the
same project, and an `agent_ask` recipient normally needs the A2A app attached.
Every eligible local agent can otherwise address every other eligible local
agent. Remote nodes already have target-side discovery and invocation grants,
but the same policy vocabulary is not available for local agent pairs.

This proposal adds a generic communication authorization model. It treats a
request as a tuple of **principal**, **target**, **action**, and **context** and
evaluates target-owned policy before work is created or delivered. A2A is the
first consumer. The model is deliberately independent of deployment topology,
Fleet, conversation threads, or a particular MCP app, so the same evaluator can
later protect app-to-agent and agent-to-MCP calls.

The policy is target-side: the agent that owns the resource decides who may
discover it, start work, send one-way messages, or continue an existing task.
The sender cannot grant itself access by putting a claim in a prompt or tool
argument. Existing-task replies remain possible after new-work access is
revoked, allowing work to terminate cleanly.

## Current behavior and gap

### Local communication

- `resolveTarget` accepts only a different agent in the caller's project.
- `agent_ask` checks whether the target has A2A attached when the platform
  exposes attachment metadata.
- `agent_send` can deliver a one-way event even when the target cannot reply.
- There is no configurable local allowlist or denylist for caller/target pairs.
- Rate limits and the open-task cap limit volume, but they are not authorization.

### Remote communication

Configured node peers already have encrypted credentials and two grants:

- `discover_agents`: which local cards the peer may see;
- `invoke_agents`: which local cards the peer may invoke.

Empty grants deny access and `*` grants all exposed agents. The panel edits
these installation-wide grants. They are target-side and are the right basis
for the local policy model.

One consistency issue must be fixed as part of this work: remote `tasks/get`
and `tasks/cancel` currently verify peer/task ownership but do not re-evaluate
the invocation grant. The proposal defines the intended behavior explicitly.

## Goals

1. Let an operator express which agents may discover or invoke each target.
2. Apply the same semantics to local and remote A2A traffic.
3. Keep authorization target-owned and independent of conversation-thread
   selection or worker spawning.
4. Distinguish discovery, new work, one-way messaging, task continuation, and
   task completion.
5. Preserve existing trusted-project behavior during migration.
6. Make policy decisions auditable without exposing prompts, secrets, or hidden
   reasoning.
7. Define primitives reusable by future MCP/app authorization checks.

## Non-goals

- Building a global identity provider or organization-wide role system.
- Giving agents the ability to edit authorization policy.
- Encoding Fleet, tenant, hub, or server topology into the policy language.
- Replacing A2A task ownership, rate limits, or delivery recovery.
- Restricting internal Core scheduler operations such as worker wakeups.
- Exposing every internal tool call to the target agent or operator.

## Generic authorization model

Every communication request is evaluated as:

```text
principal  the authenticated/caller agent, peer node, or app identity
target     the agent, task, or MCP resource being addressed
action     discover | invoke | message | continue | reply | read | cancel
context    project, connection, task ownership, and request metadata
```

The evaluator returns an immutable decision:

```json
{
  "effect": "allow",
  "reason": "target policy allows coordinator to invoke Codix",
  "policy_id": "local:project-42:agent-3",
  "policy_version": 7
}
```

The reason is for audit and operator diagnostics. It must not be inserted into
an agent prompt unless it is safe and useful to do so.

### Principals

- `agent`: a local platform agent, identified by project and stable numeric ID;
- `peer`: an authenticated remote A2A node;
- `app`: an authenticated app installation making an app-level call;
- `operator`: an administrative caller, used only for policy management.

Numeric local agent IDs remain internal. Remote requests resolve a bearer token
to a configured peer identity before policy evaluation. Request bodies never
assert their own identity or permissions.

### Resources

Policies may select a target by:

- stable local agent ID;
- generated opaque Agent Card ID;
- exact local agent name (configuration convenience only);
- a capability/skill selector;
- `*` for all agents exposed by the policy scope.

Names are normalized at write time and resolved to stable IDs. A renamed agent
must not silently change an existing grant.

### Actions

| Action | Meaning | Default relationship |
|---|---|---|
| `discover` | list or fetch a target card/summary | independent from invocation |
| `invoke` | create a new ask/task for the target | requires target policy |
| `message` | deliver a one-way message | may be separately restricted |
| `continue` | add a follow-up to an open task | requires task participation and policy |
| `reply` | return status/result for an existing task | allowed to the task participant |
| `read` | retrieve task state/messages/artifacts | limited to task participants |
| `cancel` | cancel an open task | limited to owner plus policy |

`invoke` does not imply `discover`, and `discover` does not imply `invoke`.
`reply` is intentionally different from starting new work: revoking `invoke`
must not strand already accepted work.

## Policy shape

The first implementation uses target-side rules. A rule contains:

```yaml
subject:
  kind: agent | peer | app | any
  ids: [agent-id-or-peer-id]
resource:
  kind: agent
  ids: [target-agent-id-or-card-id]
action: discover | invoke | message | continue | reply | read | cancel
effect: allow | deny
scope:
  project_id: project-42
conditions:
  require_a2a_attachment: true
  capabilities: [html-status]
priority: 100
```

The compact operator form can use selectors:

```json
{
  "subject": "agent:2",
  "resource": "agent:3",
  "actions": ["discover", "invoke", "message"],
  "effect": "allow"
}
```

For remote nodes, `subject` is the configured peer ID and `resource` is the
local Agent Card. Existing `discover_agents` and `invoke_agents` fields remain
valid compatibility inputs and compile into equivalent rules.

### Evaluation order

1. Authenticate the principal and resolve it to a stable identity.
2. Enforce hard platform boundaries: valid project, enabled target, and A2A
   attachment where required.
3. Check task ownership and direction for task-scoped actions.
4. Evaluate explicit target policy in the target's scope.
5. Apply deny-overrides-allow for matching rules at the same or broader scope.
6. If no rule matches, use the compatibility default for that scope.
7. Record the policy version and decision in audit telemetry.

An explicit deny always wins over a wildcard allow. A policy update affects new
requests immediately. Existing task replies remain allowed for the original
participants; `read`, `continue`, and `cancel` can be revoked immediately if
the operator chooses that behavior in the policy.

## Defaults and migration

The migration must not unexpectedly break current trusted projects.

### Existing local projects

Until a project creates a local policy, preserve today's behavior:

- same-project agents may discover each other when A2A is attached;
- attached agents may invoke each other;
- one-way sends retain their current delivery behavior;
- rate limits, open-task caps, and attachment checks remain active.

The panel should label this state **Compatibility default: all attached local
agents** and offer one-click conversion to an explicit policy.

### New or locked-down projects

Operators may choose **closed by default**. In that mode an agent is
discoverable and invokable only through explicit rules. Sensitive agents should
use this mode, especially agents connected to external systems or expensive
tools.

### Remote nodes

Keep the current safe default: a new node connection has no inbound discovery
or invocation grants. Existing `peers_json` entries are imported as before.
An empty list continues to mean deny, and `*` remains an explicit broad grant.

## Storage design

Add a migration with normalized rules rather than storing a pairwise matrix:

```text
a2a_policies
  id                  INTEGER PRIMARY KEY
  scope_kind          project | installation | peer
  scope_id            TEXT NOT NULL
  target_kind         agent | card | capability | any
  target_id           TEXT NOT NULL
  subject_kind        agent | peer | app | any
  subject_id          TEXT NOT NULL DEFAULT '*'
  action              TEXT NOT NULL
  effect              TEXT NOT NULL CHECK (allow | deny)
  conditions_json     TEXT NOT NULL DEFAULT '{}'
  priority            INTEGER NOT NULL DEFAULT 0
  enabled             INTEGER NOT NULL DEFAULT 1
  created_at          TEXT NOT NULL
  updated_at          TEXT NOT NULL
  created_by          TEXT NOT NULL
  policy_version      INTEGER NOT NULL
```

Indexes should cover `(scope_kind, scope_id, target_kind, target_id, action)`
and `(subject_kind, subject_id)`. Keep a policy snapshot/version on each task
at creation so the ledger explains the decision that admitted the work.

The existing peer registry remains the source of encrypted credentials and
backwards-compatible grant lists. Reconciliation compiles those lists into
effective policy entries; it must not duplicate or overwrite operator rules
owned by another source.

## Enforcement points

All paths must call one shared evaluator. Checking only the visible MCP tool is
insufficient because remote JSON-RPC and retry workers can bypass it.

### Local A2A tools

- `agents_discover`: evaluate `discover` for every returned local target;
- `agent_ask`: evaluate `invoke` before creating the task;
- `agent_send`: evaluate `message` before delivering a one-way event;
- `agent_send(task_id=...)`: evaluate `continue` after verifying participation;
- `agent_reply`: verify responder/task ownership, then allow `reply` for the
  accepted task;
- `agent_tasks`: filter results to tasks the caller may `read`;
- cancellation: verify task ownership and evaluate `cancel`.

The recipient's main thread remains responsible for deciding whether to handle
the request directly or assign it to a suitable focused worker. Authorization
must finish before that dispatch decision.

### Remote protocol

- Authenticate the bearer token to a peer identity.
- Apply `discover` to directory and Agent Card routes.
- Apply `invoke` to `SendMessage`/`message/send`.
- Re-check policy for `GetTask`/`tasks/get`, follow-ups, and cancellation.
- Permit `reply` and result synchronization only for the task's authenticated
  participants.

The implementation must return stable authorization errors without revealing
which hidden agent or policy rule exists. Discovery should omit unauthorized
cards; invocation should return a generic forbidden result.

### Generic MCP/app adoption

The evaluator should live behind an app-SDK/Core-facing interface with no A2A
types in its core contract:

```go
type CommunicationRequest struct {
    Principal Principal
    Target    Resource
    Action    string
    Context   RequestContext
}

type AuthorizationDecision struct {
    Allowed       bool
    PolicyID      string
    PolicyVersion int64
    ReasonCode    string
}

func Authorize(ctx context.Context, req CommunicationRequest) (AuthorizationDecision, error)
```

A2A can ship the first storage/evaluator implementation. Later MCP tools that
call an agent or app can use the same request shape without inheriting A2A task
tables or A2A wire protocol details.

## Operator experience

Add a **Local access** view beside Connections:

- list local agents and their current exposure state;
- show “Compatibility default” versus “Explicit policy”;
- choose **All attached agents**, **Selected agents**, or **Nobody**;
- configure discovery separately from new-work invocation;
- optionally restrict one-way messages;
- preview the effective policy for a caller/target/action;
- show recent denied attempts and the policy that made the decision;
- provide a safe “revoke new work” action that preserves replies to open tasks.

The UI should present a relationship view such as:

```text
Swifty General Manager → Codix       Discover + Invoke
Swifty General Manager → Finance     Denied
Codix → Swifty General Manager       Reply to existing tasks only
```

Do not require operators to maintain a full N×N matrix. The editor should
write target-side rules and derive the matrix for inspection.

Agent-facing tools should not expose policy-editing operations. An agent may
receive a concise “not authorized to invoke target” error and can ask an
operator through the normal product workflow.

## Audit and telemetry

Emit a generic authorization event for every decision:

```json
{
  "type": "communication.authorization",
  "principal": "agent:2",
  "target": "agent:3",
  "action": "invoke",
  "effect": "deny",
  "reason_code": "no_matching_grant",
  "policy_version": 7,
  "task_id": null,
  "timestamp": "..."
}
```

Telemetry contains IDs, action, outcome, latency, and bounded reason codes. It
does not contain bearer tokens, full prompts, hidden reasoning, or arbitrary
tool arguments. The A2A panel may show these events in an operator-only audit
view and correlate them with the task ledger.

## Task and revocation semantics

Authorization is evaluated at each boundary, but task ownership is durable.

- A new `invoke` is denied immediately after revocation.
- A saved task can still receive the responder's terminal `reply`, unless the
  operator explicitly cancels it.
- `continue`, `read`, and `cancel` are re-authorized on each request.
- Delivery retries use the decision recorded when the task/reply was admitted;
  retrying a previously accepted delivery must not create a new authorization
  decision that strands a completed result.
- A task that was never admitted remains rejected and is not written to the
  ledger.

This avoids duplicate work while allowing operators to close access safely.

## Security properties

- Target-side policy is authoritative; sender claims are untrusted.
- Peer tokens map to exactly one configured peer and remain encrypted at rest.
- Discovery and invocation are separate capabilities.
- Trust is not transitive between connected peers.
- Cross-project local access remains denied unless a future explicit bridge is
  added.
- Policy changes are versioned and auditable.
- Wildcards are explicit and visible in the UI.
- Default-deny is available for sensitive targets without forcing it on all
  existing projects.
- Authorization failures do not reveal hidden target names or policy contents.

## Tier 3 test plan

Extend the existing multi-agent runner with deterministic authorization cases.
Run the full suite with the approved model:

```sh
apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol ./scenarios
```

Required scenarios:

1. Compatibility default: two attached local agents can ask and reply.
2. Explicit allow: coordinator may invoke Codix.
3. Explicit deny: coordinator cannot invoke Finance.
4. Discovery-only: an agent can see a card but cannot start work.
5. Invocation-only: an address is actionable only when discovery is allowed;
   direct guessing still receives a generic denial.
6. One-way message policy is independent from ask/invoke.
7. Existing task reply succeeds after new-work access is revoked.
8. Follow-up, read, and cancel are denied after their grants are revoked.
9. Cross-project local requests remain denied.
10. Unattached recipients cannot accept asks; one-way behavior follows policy.
11. Remote peer grants compile to the same decisions as local rules.
12. Remote discovery, send, get, follow-up, and cancel all re-check grants.
13. Deny-overrides-wildcard behavior is deterministic.
14. A renamed agent does not change an ID-based grant.
15. Policy version and decision appear in audit telemetry.
16. A denied request does not create a task, delivery row, or worker wakeup.
17. Revocation does not duplicate work or cause blind A2A resubmission.
18. Recipient main dispatches allowed work directly or to a focused worker;
    authorization does not select an arbitrary idle conversation thread.
19. Rate limits and authorization both apply, with distinct error codes.
20. Two independent nodes cannot use a transitive peer relationship to reach a
    third node.

The runner should inspect both operator-visible outcomes and the A2A ledger so
that a request denied before admission cannot be mistaken for a delivery or a
worker failure.

## Delivery plan

### Phase 1: evaluator and compatibility path

- Define the generic request/decision types.
- Implement local compatibility-default evaluation.
- Compile existing remote grants into the evaluator.
- Add unit tests for matching, precedence, scope, and stable IDs.

### Phase 2: local policies

- Add the policy migration and project-scoped local rules.
- Enforce all local tool paths and task operations.
- Add operator API and Local access panel.
- Add audit events and effective-policy preview.

### Phase 3: remote tightening and tier 3 coverage

- Re-check grants on remote task reads, follow-ups, and cancellation.
- Add explicit authorization error codes and safe redaction.
- Add the scenarios above to the multi-agent runner using `gpt-6.1-sol`.
- Release as a minor A2A version after local tests, UI tests, and tier 3 pass.

### Phase 4: future generic MCP adoption

- Expose the evaluator through app-sdk/Core without A2A-specific storage.
- Add policy adapters for agent-to-app and app-to-agent calls.
- Keep A2A task lifecycle and MCP tool execution telemetry separate while
  sharing principal, target, action, and audit semantics.

## Open decisions

1. Should a project with no policy remain compatibility-open forever, or should
   the UI recommend converting it after an upgrade?
2. Should `continue`, `read`, and `cancel` revocation be immediate by default,
   or should operators choose a grace period for long-running work?
3. Should capability selectors be enabled in the first local-policy release,
   or limited to stable agent/card IDs until the UI can explain them clearly?
4. Should policy management be project-admin-only, or also available to a
   global A2A installation administrator?
5. Which app-sdk/Core release should expose the generic evaluator contract?

## Acceptance criteria

The proposal is implemented when an operator can explicitly allow Swifty
General Manager to invoke Codix, deny it from invoking Finance, see the
effective relationship in the A2A panel, and verify the decision in audit
telemetry. The same task paths work locally and remotely, revocation does not
strand accepted replies, and the tier 3 suite passes without production
changes or external resubmission of already-delivered work.
