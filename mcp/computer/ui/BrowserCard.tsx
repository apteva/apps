// BrowserCard — chat-attached card showing one browser session the
// agent just opened. The agent calls
// respond(components=[{app:"computer", name:"browser-card", props:{...}}])
// after browser_session(open) succeeds and the dashboard mounts this
// under that message bubble.
//
// Pure render from props — no fetch. The "watch live" button deep-links
// to the operator panel where the live view + chat are composed together.

import { Card, CardHeader, StatusPill, DataList } from "@apteva/ui-kit";

interface Props {
  instance_id: string;
  backend: "local" | "browserbase" | "steel";
  url: string;
  status?: string;
  /** Injected by the host — preview mode renders synthetic data so
   *  the dashboard's component-detail page doesn't need a live agent. */
  preview?: boolean;
}

const BACKEND_LABEL: Record<Props["backend"], string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
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
        title={host || "Browser session"}
        right={
          <StatusPill
            variant={status === "active" ? "success" : "neutral"}
            label={status}
          />
        }
      />
      <DataList
        items={[
          { label: "Backend", value: BACKEND_LABEL[props.backend] ?? props.backend },
          { label: "URL", value: props.url },
          { label: "Agent", value: props.instance_id },
        ]}
      />
      {!props.preview && (
        <a
          href={watchURL}
          target="_blank"
          rel="noreferrer"
          className="mt-2 inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md border border-zinc-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-zinc-700 dark:text-zinc-300 hover:bg-zinc-50 dark:hover:bg-zinc-800 transition-colors"
        >
          <span aria-hidden>▶</span> Watch live
        </a>
      )}
    </Card>
  );
}
