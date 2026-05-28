// BrowserCard — chat-attached card showing one browser session the
// agent just opened. The agent calls
// respond(components=[{app:"computer", name:"browser-card", props:{...}}])
// after browser_session(open) succeeds and the dashboard mounts this
// under that message bubble.
//
// Pure render from props — no fetch. The "watch live" button deep-links
// to the operator panel where the live view + chat are composed together.

import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

interface Props {
  instance_id: string;
  backend: "local" | "browserbase" | "steel" | "browser-engine" | "service" | string;
  url: string;
  status?: string;
  /** Injected by the host — preview mode renders synthetic data so
   *  the dashboard's component-detail page doesn't need a live agent. */
  preview?: boolean;
}

const BACKEND_LABEL: Record<string, string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
  "browser-engine": "Browser Engine",
  service: "Browser Service",
};

const computerLogo = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden>
    <rect x="3" y="4" width="18" height="12" rx="2" />
    <path d="M8 20h8" />
    <path d="M12 16v4" />
  </svg>
);

const computerVendor: CardVendor = {
  name: "Computer",
  logo: computerLogo,
  color: { light: "#2563eb", dark: "#93c5fd" },
};

export default function BrowserCard(props: Props) {
  const status = props.status ?? "active";
  const watchURL = `/apps/computer/?instance=${encodeURIComponent(
    props.instance_id,
  )}`;

  let host = "";
  try {
    host = new URL(props.url).host;
  } catch {
    host = props.url;
  }

  return (
    <Card>
      <CardHeader
        vendor={computerVendor}
        title={host || "Browser session"}
        subtitle={BACKEND_LABEL[props.backend] ?? props.backend}
        status={{ label: status, variant: status === "active" ? "active" : "muted" }}
        action={props.preview ? undefined : { label: "Open", href: watchURL }}
      />
      <div className="px-4 py-3 border-t border-border">
        <DataList
          items={[
            { label: "URL", value: props.url },
            { label: "Session", value: props.instance_id },
          ]}
        />
      </div>
    </Card>
  );
}
