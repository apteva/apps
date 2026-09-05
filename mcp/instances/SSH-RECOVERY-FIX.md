# Instances 0.4.43 — uncertain SSH completion

Based on the latest Instances 0.4.42 sources; retains its pre-dispatch-only retry rule and app-sdk 0.75.0.

Recognize typed/wrapped SSH missing-exit-status errors and deadline/command-timeout errors as requiring cached connection eviction. Normal typed/wrapped remote nonzero exits do not invalidate a healthy connection. A later explicit verification opens a new connection; an uncertain executed command is never automatically replayed.

Validation: full Go suite, race detector, vet and build. A real local SSH test server executes a write then drops its response. The test verifies the command executes exactly once, the pooled connection is removed, and a separate verification succeeds through a replacement connection. Classification tests cover missing status, wrapped errors, and normal command failures.

This closes a transport recovery gap; it does not prove what originally interrupted the production SSH session. Production installation is explicitly excluded.
