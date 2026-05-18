// ScreenshotCard — chat-attached card the agent posts after a
// successful screenshot_capture call. Pure render from props; no
// fetch. The agent supplies the signed storage URL it just received
// — that URL is time-limited, so the card includes a deep-link to
// the gallery for long-term reference.

import { Card, CardHeader } from "@apteva/ui-kit";

interface Props {
  screenshot_id: number;
  /** Signed storage URL the agent received from screenshot_capture.
   *  Embedded directly so the card renders without a fetch — the
   *  tradeoff is that the URL expires; the gallery link is the
   *  durable fallback. */
  url?: string;
  caption?: string;
  /** Injected by the host — preview mode renders synthetic data so
   *  the dashboard's component-detail page doesn't need a real
   *  capture. */
  preview?: boolean;
}

const DEMO_URL =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 200"><rect width="320" height="200" fill="#e2e8f0"/><text x="160" y="105" font-family="system-ui, sans-serif" font-size="14" fill="#475569" text-anchor="middle">Screenshot preview</text></svg>`,
  );

export default function ScreenshotCard(props: Props) {
  const imgURL = props.preview ? DEMO_URL : (props.url ?? "");
  const galleryURL = `/apps/screenshots/`;
  const caption = props.caption ?? "Screenshot";

  return (
    <Card>
      <CardHeader title={caption} />
      {imgURL ? (
        <a
          href={imgURL}
          target="_blank"
          rel="noreferrer"
          style={{ display: "block", lineHeight: 0 }}
          title="Open full size"
        >
          <img
            src={imgURL}
            alt={caption}
            className="rounded border border-border"
            style={{
              width: "100%",
              maxWidth: "480px",
              height: "auto",
              display: "block",
            }}
          />
        </a>
      ) : (
        <div
          className="text-text-muted"
          style={{
            padding: "32px 16px",
            textAlign: "center",
            fontSize: "13px",
          }}
        >
          Image unavailable — the signed URL may have expired. Open the
          gallery for the latest link.
        </div>
      )}
      {!props.preview && (
        <a
          href={galleryURL}
          className="mt-2 inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md border border-border bg-bg text-text-muted hover:bg-bg-subtle transition-colors"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <rect x="3" y="3" width="18" height="18" rx="2" />
            <circle cx="9" cy="9" r="2" />
            <path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21" />
          </svg>
          Open in Gallery (#{props.screenshot_id})
        </a>
      )}
    </Card>
  );
}
