# Actors 0.2.24

The editable AdultFolio example waits for AJAX result cards or verified empty-state text before reading search results. Message pagination waits for loading to settle.

Reply verification selects the authenticated sender profile link rather than a Delete control, which is also present on incoming messages. The account profile path is configurable, and the canonical sender URL is independently asserted. Provider HTML indentation is removed at message boundaries while internal copy, case, spacing and line breaks remain exact. Preparation rejects intentional leading/trailing whitespace before sending.

Fixtures cover saved bodies with HTML indentation and incoming messages with Delete controls, including a quick incoming reply after the outgoing message. Read coverage remains explicit: bounded previews may be partial; stalled histories must never be interpreted as complete. No live messages are sent by examples or tests.
