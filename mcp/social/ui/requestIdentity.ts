// Request identity is separate from selection identity: navigating away and
// back must not let an older response overwrite a newer fetch for the same item.
export function createRequestGate() {
  let revision = 0;
  return {
    begin() { const current = ++revision; return () => current === revision; },
    invalidate() { revision++; },
  };
}
