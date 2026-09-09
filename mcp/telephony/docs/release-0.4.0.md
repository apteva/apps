# Telephony 0.4.0

External applications can now use the shared headless softphone with authenticated
users and Telephony-owned communication permissions, without exposing project-wide
operator credentials. Existing operator and agent integrations remain supported.

- Add installation/project policies with issuer-namespaced users, access groups,
  allowed browser destinations and outbound numbers, and bounded supervisor roles.
- Validate current Auth sessions online through `/me`, or use an approved online
  user-info provider. Existing platform delegated identities require exact action
  scopes and matching grants. No independent Auth credentials are minted.
- Filter call lists and incoming offers before applying the result limit. IVR and
  ring-group browser destinations use the same user/group permissions.
- Persist outbound and answering-user ownership, keep concurrent answering atomic,
  and enforce call ownership on reads, hangup, rejoin, attachment and media issuance.
- Add explicit supervisor takeover, backend call assignment, and
  `phone.attach(callId)` without redialing or implicitly taking another user's call.
- Issue hashed, short-lived browser media grants with authenticated renewal,
  replacement protection and active-socket revocation. The shared controller renews
  leases independently of call polling. Auth revocation has a maximum server-side
  delay of 60 seconds plus the one-second lease check; policy changes invalidate
  existing user leases within approximately one second.
- Deny application-user access to operator administration, recordings and legacy
  MCP tools. Preserve the existing trusted operator/agent paths.
- Update app-sdk to v0.77.0 and rebuild the panel and hashed headless client.

Configure grants and the online provider using the operator-only `/access/policy`
API. With current Auth, load the app through Web SDK 0.7.0 using
`clientOptions: { authProvider: "your-provider-id" }`; `createSoftphone()` stays the
same. See `docs/application-users.md` for full setup, IVR/ringing, session refresh,
backend attachment and permission details.

Validation: Go short suite, race detection, Go vet, TypeScript checking, frontend
and audio regression tests, panel build/import checks, and compiled-sidecar browser
integration for the operator headless client, built-in panel, and online-authenticated
application user. Controlled carrier and identity servers exercise actual browser
capture/playback, mute/reconnect, DTMF and hangup. No paid carrier call or physical
headset was used.

This release does not add closed-tab/native push ringing. Hosts render incoming
calls and play their own permitted ringtone using the filtered watcher. Removing
browser access disconnects audio; it does not automatically hang up the carrier
leg. Existing deployments need an app upgrade and explicit access configuration.
