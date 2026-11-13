import { useEffect, useMemo, useState } from "react";
import { AppCardHeader, Card, DataList } from "@apteva/ui-kit";
import { hostFor, panelURL, PREVIEW_SCREENSHOT, scopeComputerURL, sessionURL } from "./shared";

export interface SoMItem {
  label: number;
  x: number;
  y: number;
  w: number;
  h: number;
  kind?: string;
}

interface SessionPresentation {
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

export interface HostPresentation {
  mode: "inline" | "side-panel" | "floating";
  width: number;
  height: number;
  active: boolean;
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
  installId?: number;
  appName?: string;
  presentation?: HostPresentation;
  preview?: boolean;
}

export default function BrowserViewCard(props: BrowserViewProps) {
  // Routing changes start a new identity-bound renderer; placement/size changes
  // retain the mounted iframe and last frame.
  const identity = JSON.stringify([props.session_id ?? props.instance_id, props.projectId, props.installId, props.appName]);
  return <BrowserViewContent key={identity} {...props} />;
}

function BrowserViewContent(props: BrowserViewProps) {
  const id = props.session_id ?? props.instance_id ?? "";
  const requestedMode = props.mode ?? "follow";
  const [presentation, setPresentation] = useState<SessionPresentation | null>(null);
  const [stream, setStream] = useState<StreamDescriptor | null>(null);
  const [tick, setTick] = useState(0);
  const [error, setError] = useState("");
  const [dims, setDims] = useState({ w: 1280, h: 800 });
  const docked = props.presentation?.mode === "side-panel" || props.presentation?.mode === "floating";
  const visible = props.presentation?.active !== false;
  const available = (value: number | undefined, fallback: number) => Number.isFinite(value) ? Math.max(0, value!) : fallback;
  const height = docked ? Math.max(0, available(props.presentation?.height, 360) - 2) : props.height ?? 360;
  const width = Math.max(0, available(props.presentation?.width, 640) - 2);
  const route = (resource: string) => sessionURL(id, resource, props.projectId, props.installId, props.appName);
  const scope = (url: string) => scopeComputerURL(url, props.projectId, props.installId, props.appName);

  useEffect(() => {
    if (props.preview || !id || requestedMode === "snapshot" || !visible) return;
    let active = true;
    let busy = false;
    const controller = new AbortController();
    const refresh = async () => {
      if (busy) return;
      busy = true;
      try {
        const [presentationResponse, streamResponse] = await Promise.all([
          fetch(route("presentation"), { credentials: "include", signal: controller.signal }),
          fetch(route("stream"), { credentials: "include", signal: controller.signal }),
        ]);
        if (!presentationResponse.ok || !streamResponse.ok) throw new Error("Browser view unavailable");
        const nextPresentation = (await presentationResponse.json()) as SessionPresentation;
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
  }, [id, props.preview, props.projectId, props.installId, props.appName, requestedMode, visible]);

  const sessionIsActive = presentation?.session.status === "active";
  const isLive = (requestedMode === "follow" || requestedMode === "live") && sessionIsActive;
  const isLiveThumbnail = requestedMode === "thumb" && sessionIsActive;
  const baseScreenshot = props.preview
    ? PREVIEW_SCREENSHOT
    : requestedMode === "snapshot"
      ? scope(props.screenshot_url ?? "")
      : (presentation?.screenshot_url ? scope(presentation.screenshot_url) : "") || (id ? route("screenshot") : "");
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

  const viewportWidth = presentation?.session.width || dims.w;
  const viewportHeight = presentation?.session.height || dims.h;
  const scale = Math.min(width / viewportWidth, height / viewportHeight);

  return (
    <Card fullWidth={docked}>
      <div style={{ display: docked ? "none" : undefined }}>
        <AppCardHeader
        title={props.caption ?? (pageURL ? hostFor(pageURL) : "Browser view")}
        subtitle={pageURL || id}
        status={{ label: isLive ? "live" : status, variant: isLive ? "live" : "muted" }}
        action={!props.preview && id ? { label: "Open", href: panelURL(id, props.projectId, props.installId, props.appName) } : undefined}
        />
      </div>
      <div data-browser-viewport className={`relative w-full bg-black border-border overflow-hidden${docked ? "" : " border-t"}`} style={{ height, display: "flex", alignItems: "center", justifyContent: "center", flexShrink: 0 }}>
        {isLive && stream?.kind === "iframe" ? (
          <iframe src={scope(stream.url)} className="border-0" title="Live browser" style={{ width: docked ? viewportWidth * scale : "100%", height: docked ? viewportHeight * scale : "100%", border: 0, flexShrink: 0 }} />
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
      <div className="px-4 py-3 border-t border-border" style={{ display: docked ? "none" : undefined }}>
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
