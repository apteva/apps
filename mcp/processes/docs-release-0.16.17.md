# Processes 0.16.17

Focused fields in the step-editing modal now keep their complete outline. The scroll area reserves space around the fields, including on mobile, so the instructions focus ring is no longer clipped on the left edge. Save controls and the existing modal editing behavior remain intact.

Verification:
- All 32 Playwright browser tests passed, including new desktop/mobile checks that focused name, instructions and required-output outlines fit within the scroll area.
- Focused-field screenshots visually checked.
- TypeScript, production bundle/import checks and diff checks passed.
