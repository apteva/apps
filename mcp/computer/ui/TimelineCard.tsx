// TimelineCard — chat-attached horizontal strip of pages the agent
// browsed. Manifest name "navigation-timeline".
//
// Pure render. The agent passes the steps; we render thumbnails
// + titles + URLs in a scrollable row.

import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

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

export default function TimelineCard(props: Props) {
  const steps = props.preview ? PREVIEW_STEPS : props.steps ?? [];
  if (steps.length === 0) {
    return (
      <Card>
        <CardHeader
          vendor={computerVendor}
          title="Navigation timeline"
          subtitle="No pages visited"
          status={{ label: "empty", variant: "muted" }}
        />
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader
        vendor={computerVendor}
        title="Navigation timeline"
        subtitle={`${steps.length} page${steps.length === 1 ? "" : "s"} visited`}
      />
      <div className="overflow-x-auto border-t border-border bg-bg-input/30 px-4 py-3">
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
                  <div className="aspect-[4/3] rounded border border-border bg-bg overflow-hidden group-hover:border-accent transition-colors">
                    {s.thumbnail ? (
                      <img
                        src={s.thumbnail}
                        alt={s.title ?? host}
                        className="w-full h-full object-cover"
                      />
                    ) : (
                      <div className="w-full h-full flex items-center justify-center text-2xl text-text-dim">
                        {i + 1}
                      </div>
                    )}
                  </div>
                  <div className="mt-1.5">
                    <p className="text-xs font-medium text-text truncate">
                      {s.title ?? host}
                    </p>
                    <p className="text-[11px] text-text-muted truncate">
                      {host}
                      {s.ts && <span className="ml-1.5 text-text-dim">· {s.ts}</span>}
                    </p>
                  </div>
                </a>
              </li>
            );
          })}
        </ol>
      </div>
      <div className="px-4 py-3 border-t border-border">
        <DataList
          items={[
            { label: "First", value: hostFor(steps[0]?.url ?? "") },
            { label: "Last", value: hostFor(steps[steps.length - 1]?.url ?? "") },
          ]}
        />
      </div>
    </Card>
  );
}

function hostFor(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return raw || "-";
  }
}

const PREVIEW_STEPS: Step[] = [
  { url: "https://docs.example.com/", title: "Docs · home", ts: "10:14" },
  { url: "https://docs.example.com/quickstart", title: "Quickstart", ts: "10:15" },
  { url: "https://docs.example.com/api/auth", title: "API · auth", ts: "10:16" },
  { url: "https://docs.example.com/api/files", title: "API · files", ts: "10:17" },
];
