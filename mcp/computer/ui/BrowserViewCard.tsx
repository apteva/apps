import { useEffect, useMemo, useState } from "react";
import { AppCardHeader, Card, DataList } from "@apteva/ui-kit";
import { hostFor, panelURL, PREVIEW_SCREENSHOT, sessionURL } from "./shared";

export interface SoMItem {
  label: number;
  x: number;
  y: number;
  w: number;
  h: number;
  kind?: string;
}

interface Presentation {
  session: {
    session_id: string;
    current_url: string;
    status: string;
    backend: string;
    width?: number;
    height?: number;
  };
  screenshot_url: string;
}

interface StreamDescriptor {
  kind: "iframe" | "snapshot" | "final";
  url: string;
  status: string;
}

export interface BrowserViewProps {
  session_id?: string;
  /** v0.7.57 compatibility */
  instance_id?: string;
  /** Compatibility/display override. Canonical browser-view follows the
   * session automatically and therefore omits this prop. */
  mode?: "follow" | "live" | "thumb" | "final" | "snapshot";
  screenshot_url?: string;
  som?: SoMItem[];
  caption?: string;
  height?: number;
  projectId?: string;
  preview?: boolean;
}

export default function BrowserViewCard(props: BrowserViewProps) {
  const id = props.session_id ?? props.instance_id ?? "";
  const requestedMode = props.mode ?? "follow";
  const [presentation, setPresentation] = useState<Presentation | null>(null);
  const [stream, setStream] = useState<StreamDescriptor | null>(null);
  const [tick, setTick] = useState(0);
  const [error, setError] = useState("");
  const [dims, setDims] = useState({ w: 1280, h: 800 });
  const height = props.height ?? 360;

  useEffect(() => {
    if (props.preview || !id || requestedMode === "snapshot") return;
    let active = true;
    let busy = false;
    const controller = new AbortController();
    setPresentation(null); setStream(null);
    const refresh = async () => {
      if (busy) return;
      busy = true;
      try {
        const [presentationResponse, streamResponse] = await Promise.all([
          fetch(sessionURL(id, "presentation", props.projectId), { credentials: "include", signal: controller.signal }),
          fetch(sessionURL(id, "stream", props.projectId), { credentials: "include", signal: controller.signal }),
        ]);
        if (!presentationResponse.ok || !streamResponse.ok) throw new Error("Browser view unavailable");
        const nextPresentation = (await presentationResponse.json()) as Presentation;
        const nextStream = (await streamResponse.json()) as StreamDescriptor;
        if (!active) return;
        setPresentation(nextPresentation);
        setStream(nextStream);
        setTick(Date.now());
        setError("");
        if (nextPresentation.session.status !== "active") {
          window.clearInterval(interval);
        }
      } catch (cause) {
        if (active) setError(cause instanceof Error ? cause.message : String(cause));
      } finally { busy = false; }
    };
    void refresh();
    const interval = window.setInterval(refresh, 3000);
    return () => {
      active = false;
      controller.abort();
      window.clearInterval(interval);
    };
  }, [id, props.preview, props.projectId, requestedMode]);

  const sessionIsActive = presentation?.session.status === "active";
  const isLive = (requestedMode === "follow" || requestedMode === "live") && sessionIsActive;
  const isLiveThumbnail = requestedMode === "thumb" && sessionIsActive;
  const baseScreenshot = props.preview
    ? PREVIEW_SCREENSHOT
    : requestedMode === "snapshot"
      ? props.screenshot_url ?? ""
      : presentation?.screenshot_url || (id ? sessionURL(id, "screenshot", props.projectId) : "");
  const screenshot = useMemo(() => {
    if (!baseScreenshot || props.preview || requestedMode === "snapshot") return baseScreenshot;
    const separator = baseScreenshot.includes("?") ? "&" : "?";
    return `${baseScreenshot}${separator}t=${tick}`;
  }, [baseScreenshot, props.preview, requestedMode, tick]);
  const pageURL = props.preview ? "https://example.com/" : presentation?.session.current_url ?? "";
  const status = props.preview ? "final" : presentation?.session.status ?? (requestedMode === "snapshot" ? "snapshot" : "loading");
  const viewLabel = isLive
    ? "Live"
    : isLiveThumbnail
      ? "Live snapshot"
      : requestedMode === "snapshot"
        ? "Snapshot"
        : status === "active"
          ? "Latest frame"
          : "Final frame";
  const som = props.som ?? [];

  return (
    <Card>
      <AppCardHeader
        title={props.caption ?? (pageURL ? hostFor(pageURL) : "Browser view")}
        subtitle={pageURL || id}
        status={{ label: isLive ? "live" : status, variant: isLive ? "live" : "muted" }}
        action={!props.preview && id ? { label: "Open", href: panelURL(id) } : undefined}
      />
      <div className="relative w-full bg-black border-t border-border overflow-hidden" style={{ height }}>
        {isLive && stream?.kind === "iframe" ? (
          <iframe src={stream.url} className="w-full h-full border-0" title="Live browser" />
        ) : screenshot ? (
          <div className="relative w-full h-full flex items-center justify-center">
            <img
              src={screenshot}
              alt={props.caption ?? "Browser page"}
              className="block w-full h-full object-contain"
              onLoad={(event) => setDims({ w: event.currentTarget.naturalWidth, h: event.currentTarget.naturalHeight })}
            />
            {som.length > 0 && (
              <svg viewBox={`0 0 ${dims.w} ${dims.h}`} preserveAspectRatio="xMidYMid meet" className="absolute inset-0 w-full h-full pointer-events-none">
                {som.map((mark) => (
                  <g key={mark.label}>
                    <rect x={mark.x} y={mark.y} width={mark.w} height={mark.h} fill="none" stroke="#ef4444" strokeWidth="2" />
                    <circle cx={mark.x + 12} cy={mark.y + 12} r="11" fill="#ef4444" stroke="white" strokeWidth="2" />
                    <text x={mark.x + 12} y={mark.y + 16} textAnchor="middle" fontSize="13" fontWeight="700" fill="white">{mark.label}</text>
                  </g>
                ))}
              </svg>
            )}
          </div>
        ) : (
          <div className="w-full h-full flex items-center justify-center text-sm text-white/70">
            {error || "Loading browser view…"}
          </div>
        )}
      </div>
      <div className="px-4 py-3 border-t border-border">
        <DataList
          items={[
            { label: "View", value: viewLabel },
            { label: "Page", value: hostFor(pageURL) },
          ]}
        />
      </div>
    </Card>
  );
}
