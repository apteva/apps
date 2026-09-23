# Processes 0.14.7

This patch fixes the project Runs endpoint introduced in 0.14.6 hanging when a
project contains structured workflow runs. Processes intentionally serializes
its app database through one SQLite connection. The endpoint previously queried
workflow steps while its outer run cursor still held that connection, causing the
panel's initial load to wait indefinitely.

The endpoint now reads and closes the bounded run cursor before loading step
details. A single-connection regression test covers the exact production failure.
No data migration or configuration change is required.
