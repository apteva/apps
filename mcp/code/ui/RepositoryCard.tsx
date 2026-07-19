import { useEffect } from "react";
import { Card, CardHeader, DataList, StatusPill } from "@apteva/ui-kit";
import {
  codeAPIURL,
  codeVendor,
  eventTouchesRepository,
  prettySize,
  ResourceStateCard,
  useCodeEvents,
  useCodeJSON,
  useDebouncedRefresh,
} from "./components/codeCards";
import { codePanelURL } from "./components/codeLinks";

interface DevRun {
  status: string;
  framework?: string;
  port?: number;
  error?: string;
}

interface RepositorySummary {
  slug: string;
  name: string;
  description?: string;
  framework?: string;
  archived: boolean;
  is_template: boolean;
  template_scope?: string;
  updated_at?: string;
  file_count: number;
  total_size: number;
  dev_run?: DevRun;
}

interface Props {
  repo: string;
  projectId?: string;
  installId?: number;
  preview?: boolean;
}

const previewData = {
  repository: {
    slug: "apteva-site",
    name: "Apteva Site",
    description: "Product site and documentation.",
    framework: "nextjs",
    archived: false,
    is_template: false,
    updated_at: new Date().toISOString(),
    file_count: 84,
    total_size: 428_000,
    dev_run: { status: "live", framework: "nextjs", port: 3000 },
  },
};

function devVariant(status?: string): "live" | "active" | "warn" | "error" | "muted" {
  if (status === "live") return "live";
  if (status === "starting") return "active";
  if (status === "crashed") return "error";
  if (status === "stopping") return "warn";
  return "muted";
}

export default function RepositoryCard({ repo, projectId, installId, preview }: Props) {
  const url = preview ? null : codeAPIURL(`/repos/${encodeURIComponent(repo)}/summary`, projectId, installId);
  const resource = useCodeJSON<{ repository: RepositorySummary }>(url, preview ? previewData : undefined);
  const scheduleRefresh = useDebouncedRefresh(resource.refresh, 400);

  useCodeEvents(preview ? undefined : projectId, (event) => {
    if (eventTouchesRepository(event.topic, event.data, repo)) scheduleRefresh();
  });
  useEffect(() => {
    if (preview || !projectId) return;
    const timer = window.setInterval(resource.refresh, 15_000);
    return () => window.clearInterval(timer);
  }, [preview, projectId, resource.refresh]);

  if (!resource.data || resource.state !== "ready") {
    return <ResourceStateCard title={repo || "Repository"} state={resource.state === "ready" ? "error" : resource.state} />;
  }
  const data = resource.data.repository;
  const state = data.archived ? "archived" : data.dev_run?.status || "ready";
  const openURL = codePanelURL({ view: "repositories", repo: data.slug });

  return (
    <Card>
      <CardHeader
        vendor={codeVendor}
        title={data.name}
        subtitle={data.slug}
        status={{ label: state, variant: data.archived ? "muted" : devVariant(data.dev_run?.status) }}
        action={{ label: "Open repository", href: openURL }}
      />
      <div className="px-4 py-3 flex flex-col gap-3">
        {data.description && <p className="text-sm text-text-muted leading-relaxed line-clamp-2">{data.description}</p>}
        <DataList
          items={[
            { label: "Framework", value: data.framework || "blank" },
            { label: "Source", value: `${data.file_count.toLocaleString()} files · ${prettySize(data.total_size)}` },
            ...(data.dev_run?.status
              ? [{ label: "Preview", value: data.dev_run.status }]
              : []),
          ]}
        />
        {(data.is_template || data.archived) && (
          <div className="flex flex-wrap gap-1.5 border-t border-border pt-3">
            {data.is_template && <StatusPill variant="info">{data.template_scope || "private"} template</StatusPill>}
            {data.archived && <StatusPill variant="neutral">archived</StatusPill>}
          </div>
        )}
      </div>
    </Card>
  );
}
