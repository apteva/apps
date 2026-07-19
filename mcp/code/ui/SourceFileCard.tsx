import { useEffect, useMemo, useState } from "react";
import { Card, CardHeader, StatusPill } from "@apteva/ui-kit";
import {
  codeAPIURL,
  codeVendor,
  eventTouchesFile,
  prettySize,
  ResourceStateCard,
  useCodeEvents,
  useCodeJSON,
} from "./components/codeCards";
import { codePanelURL } from "./components/codeLinks";

interface ReadResult {
  path: string;
  content: string;
  total_lines: number;
  start_line: number;
  end_line: number;
  size: number;
  sha256: string;
  truncated: boolean;
}

interface Props {
  repo: string;
  path: string;
  line_start?: number;
  line_end?: number;
  expected_sha256?: string;
  projectId?: string;
  installId?: number;
  preview?: boolean;
}

const previewData: ReadResult = {
  path: "src/App.tsx",
  content: "12\texport default function App() {\n13\t  return <Dashboard />;\n14\t}\n",
  total_lines: 84,
  start_line: 12,
  end_line: 14,
  size: 2_840,
  sha256: "preview-sha256",
  truncated: true,
};

function boundedRange(startValue?: number, endValue?: number) {
  const start = Math.max(1, Math.floor(startValue || 1));
  const requestedEnd = endValue && endValue >= start ? Math.floor(endValue) : start + 23;
  const end = Math.min(requestedEnd, start + 79);
  return { start, end, limit: end - start + 1 };
}

export default function SourceFileCard(props: Props) {
  const range = useMemo(() => boundedRange(props.line_start, props.line_end), [props.line_start, props.line_end]);
  const [currentPath, setCurrentPath] = useState(props.path);
  useEffect(() => setCurrentPath(props.path), [props.path]);

  const encodedPath = currentPath.split("/").map(encodeURIComponent).join("/");
  const url = props.preview
    ? null
    : codeAPIURL(
        `/repos/${encodeURIComponent(props.repo)}/files/${encodedPath}`,
        props.projectId,
        props.installId,
        { annotated: 1, offset: range.start, limit: range.limit },
      );
  const resource = useCodeJSON<ReadResult>(url, props.preview ? previewData : undefined);

  useCodeEvents(props.preview ? undefined : props.projectId, (event) => {
    if (!eventTouchesFile(event.topic, event.data, props.repo, currentPath)) return;
    if (event.topic === "file.renamed" && event.data.from === currentPath && event.data.to) {
      setCurrentPath(event.data.to);
      return;
    }
    resource.refresh();
  });

  if (!resource.data || resource.state !== "ready") {
    return <ResourceStateCard title={currentPath || "Source file"} state={resource.state === "ready" ? "error" : resource.state} />;
  }
  const data = resource.data;
  const changed = !!props.expected_sha256 && props.expected_sha256 !== data.sha256;
  const lineLabel = data.start_line === data.end_line
    ? `line ${data.start_line}`
    : `lines ${data.start_line}–${data.end_line}`;
  const openURL = codePanelURL({
    view: "repositories",
    repo: props.repo,
    path: currentPath,
    line: data.start_line,
  });

  return (
    <Card>
      <CardHeader
        vendor={codeVendor}
        title={<span className="font-mono">{currentPath}</span>}
        subtitle={`${props.repo} · ${lineLabel}`}
        status={{ label: changed ? "changed" : "current", variant: changed ? "warn" : "live" }}
        action={{ label: "Open file", href: openURL }}
      />
      <pre className="max-h-72 overflow-auto bg-bg-input px-4 py-3 text-[11px] leading-5 text-text font-mono whitespace-pre">
        {data.content || "(empty file)"}
      </pre>
      <div className="px-4 py-3 border-t border-border flex items-center justify-between gap-3 text-xs">
        <span className="text-text-dim">{data.total_lines.toLocaleString()} lines · {prettySize(data.size)}</span>
        {changed && <StatusPill variant="warn">changed since attached</StatusPill>}
      </div>
    </Card>
  );
}
