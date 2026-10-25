import {
  diagnosticsURL,
  mergeDiagnostics,
  object,
  text,
  timestamp,
  type DiagnosticObject,
} from "./call-diagnostics";
import type { AudioDashboardHost } from "./audio-dashboard";
export type InspectorState = {
  data: DiagnosticObject | null;
  error: string;
  loading: boolean;
  receivedAt: number;
};
export type InspectorEnvironment = {
  fetch: (url: string, init: RequestInit) => Promise<Response>;
  now: () => number;
  hidden: () => boolean;
  schedule: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
  cancel: (id: ReturnType<typeof setTimeout>) => void;
};
const defaults: InspectorEnvironment = {
  fetch: (...args) => fetch(...args),
  now: () => Date.now(),
  hidden: () => document.hidden,
  schedule: (fn, ms) => setTimeout(fn, ms),
  cancel: (id) => clearTimeout(id),
};
/** One request at a time; retained history is fetched less often than current counters. */
export function createCallInspector(
  host: AudioDashboardHost,
  callID: string,
  changed: (state: InspectorState) => void,
  overrides: Partial<InspectorEnvironment> = {},
) {
  const env = { ...defaults, ...overrides };
  let state: InspectorState = {
    data: null,
    error: "",
    loading: false,
    receivedAt: 0,
  };
  let disposed = false,
    denied = false,
    busy = false,
    cursor = "",
    historyDue = 0,
    failures = 0,
    first = true,
    more = false;
  let endedObserved = 0;
  let timer: ReturnType<typeof setTimeout> | undefined,
    request: AbortController | undefined;
  const publish = () => {
    if (!disposed) changed({ ...state });
  };
  const stopTimer = () => {
    if (timer !== undefined) env.cancel(timer);
    timer = undefined;
  };
  const schedule = (ms: number) => {
    stopTimer();
    if (!disposed && !denied && !env.hidden())
      timer = env.schedule(() => {
        void load();
      }, ms);
  };
  const load = async (manual = false) => {
    if (disposed || busy || env.hidden() || (!manual && denied)) return;
    stopTimer();
    busy = true;
    request = new AbortController();
    const ctrl = request;
    const timeout = env.schedule(() => ctrl.abort("timeout"), 12000);
    state = { ...state, loading: true };
    publish();
    const history = !first && (manual || more || env.now() >= historyDue);
    let success = false;
    try {
      const response = await env.fetch(
        diagnosticsURL(host, callID, history ? "incremental" : "none", cursor),
        { credentials: "same-origin", signal: ctrl.signal },
      );
      if (disposed) return;
      if (ctrl.signal.aborted) throw new Error("Request aborted");
      if (!response.ok) {
        if ([401, 403, 404].includes(response.status)) {
          denied = true;
          cursor = "";
          more = false;
          first = true;
          historyDue = 0;
          state = { ...state, data: null, receivedAt: 0 };
        }
        throw new Error(
          response.status === 403
            ? "Operator access is required to view call diagnostics."
            : response.status === 404
              ? "This call is no longer available."
              : `Diagnostics unavailable (HTTP ${response.status}).`,
        );
      }
      const next = object(await response.json());
      if (disposed) return;
      if (ctrl.signal.aborted) throw new Error("Request aborted");
      if (
        text(object(next.call).call_id) &&
        object(next.call).call_id !== callID
      )
        throw new Error("Diagnostics returned a different call.");
      const page = object(next.carrier_history);
      if (history) {
        cursor = text(page.next_cursor);
        more = page.has_more === true;
        historyDue = env.now() + 15000;
      }
      state = {
        data: mergeDiagnostics(state.data, next),
        error: "",
        loading: false,
        receivedAt: env.now(),
      };
      first = false;
      failures = 0;
      denied = false;
      success = true;
      if (object(next.call).active === false && !endedObserved)
        endedObserved = env.now();
    } catch (e) {
      if (!disposed) {
        failures++;
        state = {
          ...state,
          error: ctrl.signal.aborted
            ? "Diagnostics timed out. Previous observations may be stale."
            : e instanceof Error
              ? e.message
              : "Diagnostics unavailable.",
        };
      }
    } finally {
      env.cancel(timeout);
      busy = false;
      request = undefined;
      if (!disposed) {
        state = { ...state, loading: false };
        publish();
        const call = object(state.data?.call),
          end = timestamp(call.ended_at) || endedObserved;
        const settled =
          call.active === false && end > 0 && env.now() - end >= 30000;
        if (!denied) {
          if (!success)
            schedule(Math.min(30000, 3000 * 2 ** Math.min(failures, 4)));
          else if (more || historyDue === 0) schedule(50);
          else if (!settled) schedule(3000);
        }
      }
    }
  };
  return {
    start: () => {
      void load();
    },
    refresh: () => {
      historyDue = 0;
      void load(true);
    },
    visibility: () => {
      stopTimer();
      if (!env.hidden()) void load();
    },
    dispose: () => {
      disposed = true;
      stopTimer();
      request?.abort("unmount");
    },
  };
}
