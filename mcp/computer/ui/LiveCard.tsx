import BrowserViewCard, { type BrowserViewProps } from "./BrowserViewCard";

interface LegacyProps extends Omit<BrowserViewProps, "mode"> {
  mode?: "thumb" | "live";
}

/** Compatibility wrapper for the v0.7.57 live-view component. */
export default function LiveCard(props: LegacyProps) {
  return <BrowserViewCard {...props} mode={props.mode === "live" ? "live" : "final"} />;
}
