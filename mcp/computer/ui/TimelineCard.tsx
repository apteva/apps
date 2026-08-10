import { useEffect, useState } from "react";
import { AppCardHeader, Card, DataList } from "@apteva/ui-kit";
import { hostFor, sessionURL } from "./shared";

interface Step {
  url: string;
  title?: string;
  thumbnail?: string;
  ts?: string;
  visited_at?: string;
}

interface Props {
  session_id?: string;
  steps?: Step[];
  projectId?: string;
  preview?: boolean;
}

export default function TimelineCard(props: Props) {
  const [remoteSteps, setRemoteSteps] = useState<Step[]>([]);
  useEffect(() => {
    if (props.preview || !props.session_id || props.steps) return;
    let active = true;
    fetch(sessionURL(props.session_id, "timeline", props.projectId), { credentials: "include" })
      .then((response) => response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`)))
      .then((body) => active && setRemoteSteps(body.steps ?? []))
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [props.preview, props.projectId, props.session_id, props.steps]);

  const steps = props.preview ? PREVIEW_STEPS : props.steps ?? remoteSteps;
  return (
    <Card>
      <AppCardHeader
        title="Navigation timeline"
        subtitle={steps.length ? `${steps.length} page${steps.length === 1 ? "" : "s"} visited` : "No pages visited"}
        status={steps.length ? undefined : { label: "empty", variant: "muted" }}
      />
      {steps.length > 0 && (
        <>
          <div className="overflow-x-auto border-t border-border bg-bg-input/30 px-4 py-3">
            <ol className="flex gap-3 min-w-max">
              {steps.map((step, index) => (
                <li key={`${step.url}-${index}`} className="w-[180px] shrink-0">
                  <a href={step.url} target="_blank" rel="noreferrer" className="block group">
                    <div className="aspect-[4/3] rounded border border-border bg-bg overflow-hidden group-hover:border-accent transition-colors">
                      {step.thumbnail ? (
                        <img src={step.thumbnail} alt={step.title ?? hostFor(step.url)} className="w-full h-full object-cover" />
                      ) : (
                        <div className="w-full h-full flex items-center justify-center text-2xl text-text-dim">{index + 1}</div>
                      )}
                    </div>
                    <p className="mt-1.5 text-xs font-medium text-text truncate">{step.title || hostFor(step.url)}</p>
                    <p className="text-[11px] text-text-muted truncate">{hostFor(step.url)}</p>
                  </a>
                </li>
              ))}
            </ol>
          </div>
          <div className="px-4 py-3 border-t border-border">
            <DataList items={[
              { label: "First", value: hostFor(steps[0]?.url ?? "") },
              { label: "Last", value: hostFor(steps[steps.length - 1]?.url ?? "") },
            ]} />
          </div>
        </>
      )}
    </Card>
  );
}

const PREVIEW_STEPS: Step[] = [
  { url: "https://example.com/", title: "Example Domain" },
  { url: "https://www.iana.org/help/example-domains", title: "Example Domains" },
];
