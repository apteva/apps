# Computer v0.7.91

The Browsers panel now opens on a compact list of live sessions. Closed-session
history is available through an explicit control and loads 20 rows at a time.
Provider, proxy and saved-context controls are folded under Browser settings.

The session list endpoint supports `view=active` and `view=history` while its
existing default response remains compatible with older clients. The live panel
no longer reads the first 100 history rows on every four-second refresh.
Closing a session other than the selected one no longer switches the detail
panel away from the operator's current selection.

Validation: Computer Go short tests, panel tests, panel build and host React
import verification. This release has no new server dependency beyond v0.7.90.
