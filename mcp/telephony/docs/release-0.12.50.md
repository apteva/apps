# Telephony 0.12.50

Fixes a managed remote softphone regression introduced by the dashboard proxy routing change. Automatic mode created call media sessions through the management proxy but connected the socket directly, without registering the remote media authorization. Answering could consequently fail with HTTP 403.

Media session creation, reconnection, renewal and release now use authenticated remote ingress when advertised, with scoped browser credentials. Listening and coaching use the same policy. Ordinary call lists, notifications and dashboard data stay on the Apteva gateway. Unmanaged/local and explicit proxy modes retain their existing route. Failed remote authorization never retries call creation through the proxy.

Validation includes a repeatable real-browser test across separate gateway and media origins: the prior code reproduces the WebSocket 403; the corrected code answers, exchanges identical PCM in both directions, refreshes credentials and renews the lease while continuing media. Run `bun run test:managed-media`. Frontend, runtime authorization/race, release consistency and panel build checks also cover the change.
