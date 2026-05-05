// TimelineCard — chat-attached horizontal strip of pages the agent
// browsed. Manifest name "navigation-timeline".
//
// Pure render. The agent passes the steps; we render thumbnails
// + titles + URLs in a scrollable row.

import { Card, CardHeader } from "@apteva/ui-kit";

interface Step {
  url: string;
  title?: string;
  thumbnail?: string;
  ts?: string;
}

interface Props {
  steps?: Step[];
  preview?: boolean;
}

export default function TimelineCard(props: Props) {
  const steps = props.preview ? PREVIEW_STEPS : props.steps ?? [];
  if (steps.length === 0) {
    return (
      <Card>
        <p className="text-xs text-zinc-500">No navigation steps to show.</p>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader title={`Navigated through ${steps.length} page${steps.length === 1 ? "" : "s"}`} />
      <div className="overflow-x-auto -mx-2 px-2 pb-1">
        <ol className="flex gap-3 min-w-max">
          {steps.map((s, i) => {
            let host = "";
            try {
              host = new URL(s.url).host;
            } catch {
              host = s.url;
            }
            return (
              <li key={i} className="w-[180px] shrink-0">
                <a
                  href={s.url}
                  target="_blank"
                  rel="noreferrer"
                  className="block group"
                >
                  <div className="aspect-[4/3] rounded-md border border-zinc-200 dark:border-zinc-700 bg-zinc-100 dark:bg-zinc-900 overflow-hidden group-hover:border-zinc-400 transition-colors">
                    {s.thumbnail ? (
                      <img
                        src={s.thumbnail}
                        alt={s.title ?? host}
                        className="w-full h-full object-cover"
                      />
                    ) : (
                      <div className="w-full h-full flex items-center justify-center text-3xl text-zinc-300 dark:text-zinc-700">
                        {i + 1}
                      </div>
                    )}
                  </div>
                  <div className="mt-1.5">
                    <p className="text-xs font-medium text-zinc-800 dark:text-zinc-200 truncate">
                      {s.title ?? host}
                    </p>
                    <p className="text-[11px] text-zinc-500 truncate">
                      {host}
                      {s.ts && <span className="ml-1.5 text-zinc-400">· {s.ts}</span>}
                    </p>
                  </div>
                </a>
              </li>
            );
          })}
        </ol>
      </div>
    </Card>
  );
}

const PREVIEW_STEPS: Step[] = [
  { url: "https://docs.example.com/", title: "Docs · home", ts: "10:14" },
  { url: "https://docs.example.com/quickstart", title: "Quickstart", ts: "10:15" },
  { url: "https://docs.example.com/api/auth", title: "API · auth", ts: "10:16" },
  { url: "https://docs.example.com/api/files", title: "API · files", ts: "10:17" },
];
