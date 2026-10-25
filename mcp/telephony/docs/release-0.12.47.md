# Telephony 0.12.47

Requires Apteva 0.94.1 or later for automatic managed dashboard connection policy.

Automatic softphone mode uses the authenticated Apteva gateway for normal HTTP
reads, actions and call-notification streams. Remote endpoint discovery happens
only when preparing direct media sessions; it never redirects dashboard data.
Call placement and answer requests are sent once through the gateway. WebRTC and
PCM media retain the managed direct endpoints and existing per-call credentials.
Explicit direct clients keep their scoped browser-credential behavior.

Telephony no longer needs platform.dashboard.connect or app-specific origin
registration for its deployment. Server derives exact browser, media and
signaling origins from active managed executions. External UIs still own CSP.

Regressions cover gateway reads/writes/SSE, cancellation before media actions,
direct media URL selection, explicit direct credentials, browser CSP, call
history, Audio Health and Statistics. Audio processing and phone routes are
unchanged.
