import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Code2 } from "lucide-react";
import { Card, CardHeader, type CardVendor } from "@apteva/ui-kit";
import {
  eventTouchesFile,
  eventTouchesRepository,
  type CodeEventData,
} from "./codeLinks";

export { eventTouchesFile, eventTouchesRepository };

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  seq: number;
  data: T;
}

export const codeVendor: CardVendor = {
  name: "Code",
  logo: <Code2 aria-hidden className="h-3 w-3" />,
  color: { light: "#334155", dark: "#cbd5e1" },
};

export function codeAPIURL(
  path: string,
  projectId?: string,
  installId?: number,
  query: Record<string, string | number | undefined> = {},
): string | null {
  if (!projectId) return null;
  const params = new URLSearchParams({ project_id: projectId });
  if (installId) params.set("install_id", String(installId));
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  return `/api/apps/code/api${path}?${params.toString()}`;
}

export function useCodeEvents(
  projectId: string | undefined,
  onEvent: (event: AppEventEnvelope<CodeEventData>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    if (!projectId) return;
    const handler = (event: AppEventEnvelope<CodeEventData>) => handlerRef.current(event);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (event: AppEventEnvelope<CodeEventData>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe("code", projectId, handler);

    let lastSeq = 0;
    const events = new EventSource(
      `/api/app-events/code?project_id=${encodeURIComponent(projectId)}`,
      { withCredentials: true },
    );
    events.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data) as AppEventEnvelope<CodeEventData>;
        if (event.seq <= lastSeq) return;
        lastSeq = event.seq;
        handler(event);
      } catch {
        // Ignore malformed event frames; the next valid app event refreshes the card.
      }
    };
    return () => events.close();
  }, [projectId]);
}

export function useCodeJSON<T>(url: string | null, previewData?: T) {
  const [data, setData] = useState<T | null>(previewData ?? null);
  const [state, setState] = useState<"loading" | "ready" | "missing" | "error">(
    previewData ? "ready" : "loading",
  );
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((value) => value + 1), []);

  useEffect(() => {
    if (previewData) {
      setData(previewData);
      setState("ready");
      return;
    }
    if (!url) return;
    const controller = new AbortController();
    fetch(url, { credentials: "same-origin", signal: controller.signal })
      .then(async (response) => {
        if (response.status === 404) {
          setData(null);
          setState("missing");
          return null;
        }
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return response.json() as Promise<T>;
      })
      .then((next) => {
        if (next === null || controller.signal.aborted) return;
        setData(next);
        setState("ready");
      })
      .catch((error) => {
        if (error?.name !== "AbortError") setState("error");
      });
    return () => controller.abort();
  }, [url, revision, previewData]);

  return { data, state, refresh };
}

export function useDebouncedRefresh(refresh: () => void, delay = 300) {
  const timer = useRef<number | null>(null);
  useEffect(() => () => {
    if (timer.current !== null) window.clearTimeout(timer.current);
  }, []);
  return useCallback(() => {
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(refresh, delay);
  }, [delay, refresh]);
}

export function ResourceStateCard({
  title,
  state,
}: {
  title: ReactNode;
  state: "loading" | "missing" | "error";
}) {
  const label = state === "missing" ? "unavailable" : state;
  return (
    <Card>
      <CardHeader
        vendor={codeVendor}
        title={title}
        status={{ label, variant: state === "error" ? "error" : "muted" }}
      />
    </Card>
  );
}

export function prettySize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} kB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
