# Application-user softphones (Telephony 0.4.0)

Telephony owns communication permissions and call ownership. Consuming apps own
business rules. No customer-specific roles, IDs, or databases are required.
The browser needs `@apteva/web-sdk@0.7.0` and an installed Telephony 0.4.0.

## Current Auth sessions

Configure an online provider and user grants with operator credentials, then:

```ts
import { AptevaClient } from "@apteva/web-sdk";
const sdk = new AptevaClient({ baseURL, accessToken: login.access_token });
const loaded = await sdk.apps.load("telephony", {
  projectId, installId,
  clientOptions: { authProvider: "customer-login" },
});
const telephony = loaded.client;
const phone = telephony.createSoftphone();
phone.subscribe(renderCallState);
const incoming = telephony.watchCalls(calls => {
  // Render the incoming-call UI and play the host's permitted ringtone.
  renderIncomingCalls(telephony.incomingCalls(calls));
});
// From a user gesture:
await phone.answer(callId, { destination_id: browserDestinationId });
// Or dial using an explicitly permitted connected caller ID:
await phone.dial({ to: "+12025550100", from: "+12025550101" });
// After the application's normal login refresh:
sdk.setAccessToken(refreshed.access_token);
// On logout: await phone.hangup() if the carrier call should end, then:
incoming.close(); phone.dispose(); loaded.dispose();
```

With `authProvider`, the client uses `/user/calls` and `/user/softphone/*`.
Every request validates the bearer against the administrator-configured online
identity endpoint. No independent delegated credential is minted. Current Auth
0.11's `/me` validates the session family, current authorization, disabled users,
and logout. A raw Auth JWT on the old operator endpoints remains unsupported.

Without `authProvider`, the existing operator/delegated gateway flow is preserved.
Delegated gateway identities must have exact `app_user`/`telephony` actions and
matching Telephony grants. Their expiry/revocation follows the platform credential,
not an unrelated Auth session; use the online provider for current Auth logins.

## Configure communication access

Use authenticated operator HTTP requests against the selected installation:

- `GET /access/policy`: current policy and revision (initially revision 0).
- `PUT /access/policy`: replace the policy using that revision. Stale writes return
  409. This is an operator-only administrative API, including for supervisors.
- `GET /softphone/access` (or `/user/softphone/access?auth_provider=...`): the
  authenticated user's effective destinations, numbers, identity, and supervisor flag.

Example policy (substitute actual project, installation, organization, user,
connected phone-number and browser-destination values):

```json
{
  "revision": 0,
  "providers": [{
    "id": "customer-login",
    "issuer_app": "auth",
    "issuer_install_id": "11",
    "url": "https://agents.example.com/api/apps/auth/_install/11/me?project_id=PROJECT",
    "format": "apteva-auth",
    "actions": ["call.read", "call.dial", "call.answer", "call.attach", "call.hangup"]
  }],
  "groups": [{
    "id": "sales-team",
    "role": "user",
    "destinations": ["BROWSER_DESTINATION_ID"],
    "outbound_numbers": ["+12025550101"]
  }],
  "users": [{
    "identity": {
      "issuer_app": "auth",
      "issuer_install_id": "11",
      "subject_type": "user",
      "subject_id": "42",
      "organization_id": "7"
    },
    "enabled": true,
    "role": "user",
    "groups": ["sales-team"],
    "destinations": [],
    "outbound_numbers": []
  }]
}
```

Identity values come from the verified provider response, never request-supplied
user IDs. Auth's numeric IDs are represented as decimal strings in policy.
Issuer app + issuer installation + organization + subject isolate identities;
the enclosing installation database and project isolate resources. Missing or
disabled users have no access. Direct and group grants are additive. Destination
IDs must refer to enabled browser destinations in the same project. Outbound
numbers must also pass the existing connected-carrier number resolution on dial.

Other authentication systems can use `format: "userinfo"` with an approved HTTPS
GET endpoint. It must accept the user's bearer token, verify its **current**
session on every request, and return `{ "sub": "USER", "organization_id": "ORG" }`.
The configured issuer namespace identifies this provider; an unverified JWT or a
client-provided subject is never used. Configure an online session-aware endpoint
if the provider's standard user-info endpoint does not check revocation. Telephony
forwards Origin for the provider's browser-origin checks and never follows redirects.
HTTP is permitted only for localhost development. Provider URLs must not contain
credentials; the user bearer remains only in the Authorization header.

## Numbers, IVR, groups, and ringing

Create browser destinations and routing flows with the existing Telephony panel
or `/routing/` operator API. Assign a number to a published flow; its IVR branches
can select a browser destination or a ring group. Grant that browser destination
to a user or an access group. An individual destination can represent one person's
desk; a shared destination can represent a team. Existing ring groups can offer
multiple destinations using their configured strategy and timeout/overflow.

Only authorized users receive those pending calls/offers. The first successful
answer owns the call; peers lose visibility after it is claimed unless they have
supervisor access. Supervisors remain bounded by their granted destinations and
outbound numbers. `call.takeover` is also required in their provider/gateway scope.

The host's `watchCalls()` polls every two seconds by default; it renders incoming
calls and plays its own ringtone. Keep the controller and watcher alive across
navigation. Browser audio needs HTTPS/localhost, microphone permission and a user
gesture. Closed-tab/native push ringing and presence-based scheduling are not added.
Use the routing flow's existing timeout/overflow for unanswered calls. Configure
host CORS in the gateway and client origins in Auth. Only frontend module assets
and the explicitly authenticated `/user/` entry point are newly public; policy,
number purchasing, routing administration, recordings and MCP remain inaccessible
to application-user sessions.

## Backend-created calls, reconnect, and takeover

A trusted backend can create a human call via `POST /softphone/place` (not the
AI-call MCP tool), then assign it before any browser connects:

```http
POST /access/assign/CALL_ID
Content-Type: application/json

{"issuer_app":"auth","issuer_install_id":"11","subject_type":"user","subject_id":"42","organization_id":"7"}
```

Assignment validates the user's resource grants and rejects an already assigned
call or one with connected browser audio. It invalidates the original browser
credential. Deliver only `call_id` to the intended application user:

```ts
await phone.attach(callId);
```

Attach authorizes the current user, issues a browser session for the existing
human leg, and connects audio. It does not dial, answer an unclaimed incoming
offer, or take another user's call. Pending incoming offers use `answer()`.

`join(callId)` remains an explicit reconnect for the owning user. Supervisors must
use `takeover(callId)` to replace another operator. Takeover changes ownership and
invalidates the previous media session; it is not implied by `rejoin: true`.
Trusted administrative integrations retain access to all project calls.

## Revocation and media sessions

User media credentials are distinct from carrier secrets, bound to the call,
principal, project and policy revision. Only a hash is persisted. One current
browser grant per call makes replaced tokens unusable. The controller renews a
60-second media lease every 20 seconds through authenticated HTTP, independently
of call-status polling. Failed renewal stops local audio; the server disconnects
expired or invalidated sessions. Auth logout/disable takes effect at the next
renewal, with a server-side upper bound of 60 seconds plus the one-second check.
Policy edits invalidate existing user leases within approximately one second;
still-authorized users can attach again. Revoking browser access does not itself
hang up the remote carrier leg. Route/operator handling owns that decision.

Low-level clients using `place`, `answer`, `attach`, or `takeover` must honor
`lease_seconds` and call `renew(session)` while audio is attached. The shared
controller does this automatically. Never persist or log media URLs/session tokens.
A failed attachment leaves the existing carrier leg available for explicit recovery.

Tests cover scoped offers and call reads, ownership, concurrent answering, explicit
supervisor takeover, issuer/org/project isolation, forbidden administrative/MCP
paths, backend assignment, session renewal, online login revocation, redirect
rejection, stale policy edits, media replay and disconnect, and visibility before
pagination. Browser integration uses the built SDK-loaded module and compiled
Telephony sidecar with controlled identity and carrier servers; it exercises real
browser capture/playback, mute/reconnect, keypad input and hangup.
