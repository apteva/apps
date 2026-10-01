# Telephony 0.8.4

This patch fixes the combined Bun test suite. The AudioWorklet regression test
now executes `softphone-worklet.js` as a browser-style worklet script instead
of importing it as an ES module. That avoids Bun's module-cache collision when
the same source is imported as a text asset by the frontend tests.

Production audio code and the generated frontend client are unchanged.

## Verification

- 101 combined frontend and UI tests pass.
- TypeScript checks, frontend build, and focused Go manifest tests pass.
- No staging, production, carrier, or live-call configuration was changed.
