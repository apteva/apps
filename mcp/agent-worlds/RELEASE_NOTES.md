# Agent Worlds 0.4.0-preview.1

A preview of the instance-wide Worlds view, with one village per project and shared project → observed team → agent navigation across Pixel Village, System Graph, and Operations.

- A single compact toolbar with a representation selector; Conversation is removed.
- Shared wheel/pinch zoom, drag panning, keyboard camera controls, and fit/reset.
- Colorful procedural pixel villages with open furnished houses and independently roaming agents.
- App display names in house labels, graph nodes, and details.
- Generic SDK inventory, topology, and bounded metadata history; cached topology, frozen replay, and cancellable polling.

Requires an Apteva server with the local generic telemetry observation APIs. The supporting SDK additions are not yet in a published SDK tag. This preview ships native packages for macOS/Linux on arm64/amd64; standalone source fallback is unavailable until those APIs are published. The stable marketplace release is unchanged.

Validation: Go race tests; TypeScript checks; 19 UI/model tests; four cross-platform builds; archive/checksum validation; packaged binary smoke checks; local installation from the public release artifacts.
