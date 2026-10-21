# Agent Worlds 0.4.0

The instance-wide Worlds view, with one village per project and shared project → observed team → agent navigation across Pixel Village, System Graph, and Operations.

- A single compact toolbar with a representation selector; Conversation is removed.
- Shared wheel/pinch zoom, drag panning, keyboard camera controls, and fit/reset.
- Colorful procedural pixel villages with open furnished houses and independently roaming agents.
- App display names in house labels, graph nodes, and details.
- Generic SDK inventory, topology, and bounded metadata history; cached topology, frozen replay, and cancellable polling.

Includes checksummed native packages for macOS and Linux on arm64/amd64. Standalone source builds use the included, checksummed SDK dependency and do not need a local workspace overlay.

Requires an Apteva server providing the generic telemetry observation APIs (agent/runtime inventory, retained telemetry queries, and agent topology). The existing local server provides them. This release updates Worlds only.

Validation: Standalone Go builds and race tests with GOWORK=off; TypeScript checks; 19 UI/model tests; four cross-platform builds; archive/checksum validation; packaged binary smoke checks; local installation from the public release artifacts.
