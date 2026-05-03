import { getHealth } from "../api/health.ts";
import { useFetch } from "./useFetch.ts";

// Health is event-driven via the `tick` app-event (the desk's
// useAppEvents dispatch calls health.refresh() on every tick). The
// poll interval here is a backstop — 30s catches up if SSE drops
// without reconnecting cleanly.
export function useHealth() {
  return useFetch(getHealth, [], 30000);
}
