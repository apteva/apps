# TURN 0.3.0

- Configure UDP and TCP client ports and up to eight simultaneous TLS ports, including 443 when available. Existing defaults remain 3478 and 5349.
- The native themed panel shows actual endpoints and firewall ports, adds listener settings and transport-specific temporary credential presets.
- Copy a forced-relay browser test configuration for comparing UDP, TCP and individual TLS ports. API and app-only MCP credentials accept transport and TLS-port filters.
- App-configured listeners bind before readiness with complete rollback on conflict. The fixed UDP media pool stays reserved by Apteva. New port settings are checked for conflicts before panel save; startup remains authoritative. Managed panel saves confirm stop through Apteva before persisting settings and queuing restart.
- Managed certificates and renewal cover every TLS listener. No changes to other apps, production, HTTPS services or firewall rules.

Stop the existing TURN execution before same-host upgrade/reconfiguration on native Instances targets: its fixed media pool prevents overlapping preparation. Open configured ports in host/cloud firewalls. Container deployments must publish configured client ports explicitly or use host networking.
