
## Gateway control and direct media (0.12.47)

Automatic mode keeps ordinary HTTP requests, call actions and notification SSE
on the authenticated Apteva proxy, independent of media discovery. Media session
creation discovers the execution endpoint before placing/answering/attaching,
but the action itself still uses the gateway. WebRTC and PCM media remain direct.
Network-check signaling also uses the gateway; its UDP probe stays direct.
Explicit `direct` connections retain the existing scoped credential contract.

The updated Apteva Server derives dashboard connection origins from active
managed deployments. Telephony no longer registers origins or requires
`platform.dashboard.connect` for its deployment. External hosts own their CSP.
