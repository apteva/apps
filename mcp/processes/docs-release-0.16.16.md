# Processes 0.16.16

Selecting an editable procedure step now opens a spacious modal instead of the narrow flow side panel. The instructions field is larger, while the header and Save changes / Cancel controls stay visible as the settings scroll. The flow retains its full width behind the modal.

The modal adapts to mobile screens, keeps keyboard focus inside the editor, closes with Escape, and returns focus to the selected step. Closing retains pending procedure edits. The full procedure form offers Done editing step before saving the draft. Dependency ports remain usable without opening the editor.

Existing atomic draft saves, validation, failed-save retries, read-only historical versions, frozen run instructions, receipts and approval gates retain their behavior.

Verification:
- All 30 Playwright browser tests passed, including desktop/mobile modal size, fixed controls, keyboard focus, Escape, preserved edits, retry, history, flow connections and timing.
- All 36 UI tests passed.
- TypeScript and production bundle/import checks passed.
