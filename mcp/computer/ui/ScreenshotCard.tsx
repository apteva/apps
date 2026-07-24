import BrowserViewCard, { type BrowserViewProps } from "./BrowserViewCard";

/** Compatibility wrapper for the v0.7.57 screenshot-with-som component. */
export default function ScreenshotCard(props: BrowserViewProps) {
  return <BrowserViewCard {...props} mode="snapshot" />;
}
