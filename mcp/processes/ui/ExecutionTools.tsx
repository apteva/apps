import { useEffect, useMemo, useState } from "react";
import {Lightbulb, Wrench} from "lucide-react";
import {eventsForStep, thoughtsFromEvents, useWorkerActivity, type TelemetryEvent} from "./execution-activity";

export type ToolSource = {
  key: string;
  label: string;
  icon?: string;
  iconStyle?: "image" | "monochrome";
  aliases: string[];
  tools?: string[];
};

type ToolCall = {
  id: string;
  name: string;
  reason: string;
  time: string;
  failed: boolean;
  status: "running" | "completed" | "failed";
  args: Record<string, unknown>;
  result?: unknown;
};

const normalize = (value: unknown) =>
  String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "");

export function resolveToolSource(name: string, sources: ToolSource[]) {
  const tool = normalize(name);
  return (
    sources.find((source) =>
      (source.tools || []).some((candidate) => normalize(candidate) === tool),
    ) ||
    sources.find((source) =>
      source.aliases.some((alias) => {
        const prefix = normalize(alias);
        return tool === prefix || tool.startsWith(`${prefix}_`);
      }),
    )
  );
}

function executionIncludes(data: Record<string, unknown>, executionID: string) {
  const ids = data.execution_ids;
  if (Array.isArray(ids)) return ids.some((id) => String(id) === executionID);
  return String(data.execution_id || "") === executionID;
}

export function callsForExecution(
  events: TelemetryEvent[],
  executionID: string,
): ToolCall[] {
  const relevant = events.filter((event) =>
    executionIncludes(event.data || {}, executionID),
  );
  const results = new Map<string, boolean>();
  const payloads = new Map<string, unknown>();
  for (const event of relevant) {
    if (event.type !== "tool.result") continue;
    const data = event.data || {};
    const id = String(data.id || data.call_id || "");
    if (id) payloads.set(id,data.result ?? data.output ?? data.content ?? data.error);
    if (id)
      results.set(
        id,
        data.is_error === true || data.success === false || !!data.error,
      );
  }
  return relevant
    .filter((event) => event.type === "tool.call")
    .map((event): ToolCall => {
      const data = event.data || {};
      const id = String(data.id || data.call_id || event.id);
      const args =
        data.args && typeof data.args === "object"
          ? (data.args as Record<string, unknown>)
          : {};
      return {
        id,
        name: String(data.name || data.tool || "Tool"),
        reason: String(data.reason || args._reason || ""),
        time: event.time,
        args,
        result: payloads.get(id),
        failed: results.get(id) || false,
        status: results.has(id) ? results.get(id) ? "failed" : "completed" : "running",
      };
    })
    .sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
}

function SourceIcon({ source }: { source?: ToolSource }) {
  const [failed, setFailed] = useState("");
  if (source?.icon && failed !== source.icon) {
    if (source.iconStyle === "monochrome") return <span className="tool-icon mono" aria-hidden="true">
      <span className="tool-icon-mask" style={{maskImage:`url("${source.icon}")`, WebkitMaskImage:`url("${source.icon}")`}} />
      <img className="tool-icon-probe" src={source.icon} alt="" onError={()=>setFailed(source.icon!)} />
    </span>;
    return <img className="tool-icon" src={source.icon} alt="" onError={()=>setFailed(source.icon!)} />;
  }
  return <span className="tool-icon fallback" aria-hidden="true"><Wrench size={18} /></span>;
}

export default function ExecutionTools({
  agentID,
  threadID,
  executionID,
  sources,
  defaultOpen = false,
  live = false, stepID, completedAt, onActivityStatus,
}: {
  agentID?: number;
  threadID?: string;
  executionID?: string;
  sources: ToolSource[];
  defaultOpen?: boolean;
  live?: boolean; stepID?: string; completedAt?: string;
  onActivityStatus?: (status: string) => void;
}) {
  const [requested, setRequested] = useState(defaultOpen);
  const [filter, setFilter] = useState("all");
  const {events,loading,error} = useWorkerActivity(agentID,threadID,requested,live);
  const scoped = useMemo(()=>eventsForStep(events,executionID || "",stepID,completedAt),[events,executionID,stepID,completedAt]);
  const thoughts = useMemo(()=>thoughtsFromEvents(scoped),[scoped]);
  const calls = useMemo(
    () => callsForExecution(scoped, executionID || ""),
    [scoped, executionID],
  );
  const items = [...thoughts.map(thought=>({kind:"thought" as const, time:thought.time, thought})), ...calls.map(call=>({kind:"tool" as const,time:call.time,call}))].sort((a,b)=>Date.parse(a.time)-Date.parse(b.time));
  const shown = items.filter(item=>filter === "all" || (filter === "thoughts" ? item.kind === "thought" : item.kind === "tool"));
  const latestCall = calls[calls.length-1];
  const latestStart = scoped.filter(event=>event.type === "llm.start").at(-1);
  const activityStatus = latestCall?.name === "pace" && (!latestStart || Date.parse(latestStart.time) <= Date.parse(latestCall.time)) ? `Worker is waiting${latestCall.reason ? ` · ${latestCall.reason}` : " for the next event"}` : "";
  useEffect(()=>{onActivityStatus?.(activityStatus);},[activityStatus,onActivityStatus]);
  if (!agentID || !executionID)
    return (
      <p className="small muted tool-unavailable">
        Tool activity was not tracked for this execution.
      </p>
    );
  return (
    <details
      className="tool-activity"
      open={requested}
      onToggle={(event) => {
        setRequested(event.currentTarget.open);
      }}
    >
      <summary>
        Worker activity{requested && !loading ? ` · ${calls.length} tool calls` : ""}
      </summary>
      <div className="activity-filters" role="group" aria-label="Worker activity filters">
        {["all","thoughts","tools"].map(value=><button type="button" key={value} aria-pressed={filter===value} onClick={()=>setFilter(value)}>{value === "all" ? "All activity" : value === "thoughts" ? "Thoughts" : "Tools"}</button>)}
      </div>
      {activityStatus && <p className="small muted">{activityStatus}</p>}
      {error && <p className="notice small" role="alert">{error}</p>}
      {loading && !items.length && <p className="small muted">Loading worker activity…</p>}
      <ol className="tool-calls">
        {shown.map(item=>item.kind === "thought" ? (
          <li key={`thought:${item.thought.id}`}>
            <span className="tool-icon fallback"><Lightbulb size={18} /></span>
            <details className="thought-detail">
              <summary><span>{item.thought.status === "running" ? "Thinking · in progress" : item.thought.status === "failed" ? "Thinking · failed" : "Thought · completed"}</span><span className="activity-preview">{item.thought.response || item.thought.reasoning || (item.thought.status === "running" ? "Waiting for model output…" : "No reasoning text was recorded for this turn.")}</span></summary>
              {item.thought.reasoning && <div><strong>Reasoning</strong><p className="prose small">{item.thought.reasoning}</p></div>}
              {item.thought.response && <div><strong>Response</strong><p className="prose small">{item.thought.response}</p></div>}
              {!item.thought.reasoning && !item.thought.response && <p className="small muted">{item.thought.status === "running" ? "Waiting for model output…" : "No reasoning text was recorded for this turn."}</p>}
            </details>
          </li>
        ) : (
          <li key={`tool:${item.call.id}`}>
            <SourceIcon source={resolveToolSource(item.call.name,sources)} />
            <span className="tool-call-copy">
              <span className="row between"><strong>{resolveToolSource(item.call.name,sources)?.label || item.call.name}</strong><span className={`pill ${item.call.status}`}>{item.call.status === "running" ? "running" : item.call.failed ? "failed" : "finished"}</span></span>
              <span className="small muted tool-name">{item.call.name}</span>
              <span className="tool-reason"><code>_reason</code>: {item.call.reason || "No reason supplied"}</span>
              <details className="thought-detail"><summary>Inputs and result</summary>
                <strong>Inputs</strong><pre className="prose small">{JSON.stringify(item.call.args,null,2)}</pre>
                <strong>Result</strong><pre className="prose small">{item.call.result === undefined ? item.call.status === "running" ? "Tool is running…" : "No result payload was recorded." : typeof item.call.result === "string" ? item.call.result : JSON.stringify(item.call.result,null,2)}</pre>
              </details>
            </span>
          </li>
        ))}
      </ol>
      {!loading && !error && !shown.length && <p className="small muted">{filter === "thoughts" ? "No reasoning events recorded yet." : "Waiting for worker activity. Tool calls and recorded reasoning will appear here."}</p>}
    </details>
  );
}
