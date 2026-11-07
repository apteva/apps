# Telephony 0.12.49

The Calls page starts without a selected call and keeps that empty state through refreshes. Selecting a call adds a subtle accent tint and a thin left edge, with an accessible pressed state. Selection stays on the chosen call while it remains in the list; a removed call clears selection rather than selecting another call automatically.

Validation: call-inspector browser checks, TypeScript checks, panel build/import checks, and release manifest/frontend consistency tests. Calling, media, routing, and runtime recovery are unchanged.
