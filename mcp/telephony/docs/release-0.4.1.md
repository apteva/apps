# Telephony 0.4.1

Fix authenticated Web SDK loading of the headless frontend. In 0.4.0, the server
preserved the user's bearer on manifest-public frontend URLs, but the Go SDK did
not honor those manifest declarations in its sidecar auth gate. Anonymous asset
requests worked while authenticated SDK downloads returned 401.

- Pin Go app-sdk v0.77.1, which honors manifest no_auth routes, including
  framework-served frontend assets, without replacing the caller's bearer.
- Keep private routes protected and preserve application-user verification.
- Correct the compiled browser test proxy to forward user bearers on public
  frontend requests exactly as the production server does. The corrected test
  reproduces the 401 with v0.77.0 and passes with v0.77.1.
- Exercise signup/login through a real Auth sidecar, load the client using its
  signed user session, verify call monitoring, answer/audio, mute/reconnect, DTMF,
  hangup, and rejection after logout.

No server or Web SDK change is required for this fix. Upgrade the Telephony
installation; its existing permissions and authentication configuration remain.

Validation: SDK full Go suite and vet; Telephony Go short suite, application-user
race regressions, vet, TypeScript/frontend tests and panel import checks; compiled
browser checks for operator headless, built-in panel, and real Auth application-user
sessions. Carrier interactions use controlled test peers; no paid call is placed.
