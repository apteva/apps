import { Card, CardHeader, DataList, StatusPill } from "@apteva/ui-kit";
import {
  codeAPIURL,
  codeVendor,
  ResourceStateCard,
  useCodeEvents,
  useCodeJSON,
} from "./components/codeCards";
import { codePanelURL } from "./components/codeLinks";

interface Issue {
  repo_slug: string;
  number: number;
  title: string;
  body?: string;
  type: string;
  status: string;
  state: string;
  state_reason?: string;
  priority: string;
  assignee?: string;
  claim_owner?: string;
  claim_label?: string;
  claimed_at?: string;
  comments_count?: number;
  updated_at?: string;
}

interface Props {
  repo: string;
  issue_number: number;
  projectId?: string;
  installId?: number;
  preview?: boolean;
}

const previewData = {
  issue: {
    repo_slug: "apteva-site",
    number: 42,
    title: "Improve repository import feedback",
    body: "Show dependency installation and build progress in the Code activity view.",
    type: "feature",
    status: "in_progress",
    state: "open",
    priority: "high",
    assignee: "agent-15",
    claim_owner: "agent:15",
    claim_label: "Agent 15",
    comments_count: 3,
  },
};

function priorityVariant(priority: string): "error" | "warn" | "info" | "neutral" {
  if (priority === "urgent") return "error";
  if (priority === "high") return "warn";
  if (priority === "medium") return "info";
  return "neutral";
}

export default function IssueCard({ repo, issue_number, projectId, installId, preview }: Props) {
  const url = preview
    ? null
    : codeAPIURL(
        `/repos/${encodeURIComponent(repo)}/issues/${issue_number}`,
        projectId,
        installId,
        { summary: 1 },
      );
  const resource = useCodeJSON<{ issue: Issue }>(url, preview ? previewData : undefined);

  useCodeEvents(preview ? undefined : projectId, (event) => {
    if (event.data.slug !== repo) return;
    if (event.topic === "repo.deleted") resource.refresh();
    if (event.topic.startsWith("issue.") && event.data.number === issue_number) resource.refresh();
  });

  if (!resource.data || resource.state !== "ready") {
    return <ResourceStateCard title={`${repo} #${issue_number}`} state={resource.state === "ready" ? "error" : resource.state} />;
  }
  const issue = resource.data.issue;
  const openURL = codePanelURL({ view: "issues", repo: issue.repo_slug, issue: issue.number });

  return (
    <Card>
      <CardHeader
        vendor={codeVendor}
        title={issue.title}
        subtitle={`${issue.repo_slug} #${issue.number}`}
        status={{ label: issue.state, variant: issue.state === "open" ? "active" : "muted" }}
        action={{ label: "Open issue", href: openURL }}
      />
      <div className="px-4 py-3 flex flex-col gap-3">
        {issue.body && <p className="text-sm text-text-muted leading-relaxed line-clamp-3">{issue.body}</p>}
        <div className="flex flex-wrap gap-1.5">
          <StatusPill variant={issue.type === "bug" ? "error" : "info"}>{issue.type}</StatusPill>
          <StatusPill variant={issue.status === "blocked" ? "error" : issue.status === "done" ? "success" : "neutral"}>{issue.status}</StatusPill>
          <StatusPill variant={priorityVariant(issue.priority)}>{issue.priority}</StatusPill>
          {issue.state_reason && <StatusPill variant="neutral">{issue.state_reason}</StatusPill>}
        </div>
        <DataList
          items={[
            { label: "Assignee", value: issue.assignee || "unassigned" },
            { label: "Claim", value: issue.claim_label || issue.claim_owner || "available" },
            { label: "Comments", value: (issue.comments_count || 0).toLocaleString() },
          ]}
        />
      </div>
    </Card>
  );
}
