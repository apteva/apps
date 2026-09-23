import { useEffect, useMemo, useState } from "react";

export type ToolSource = {
  key: string;
  label: string;
  icon?: string;
  iconStyle?: "image" | "monochrome";
  aliases: string[];
  tools?: string[];
};

type TelemetryEvent = {
  id: string;
  type: string;
  time: string;
  data?: Record<string, unknown>;
};

type ToolCall = {
  id: string;
  name: string;
  reason: string;
  time: string;
  failed: boolean;
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
  for (const event of relevant) {
    if (event.type !== "tool.result") continue;
    const data = event.data || {};
    const id = String(data.id || data.call_id || "");
    if (id)
      results.set(
        id,
        data.is_error === true || data.success === false || !!data.error,
      );
  }
  return relevant
    .filter((event) => event.type === "tool.call")
    .map((event) => {
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
        failed: results.get(id) || false,
      };
    })
    .sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
}

function SourceIcon({ source }: { source?: ToolSource }) {
  if (source?.icon)
    return (
      <img
        className={source.iconStyle === "monochrome" ? "tool-icon mono" : "tool-icon"}
        src={source.icon}
        alt=""
      />
    );
  return <span className="tool-icon fallback" aria-hidden="true">⌁</span>;
}

export default function ExecutionTools({
  agentID,
  threadID,
  executionID,
  sources,
}: {
  agentID?: number;
  threadID?: string;
  executionID?: string;
  sources: ToolSource[];
}) {
  const [requested, setRequested] = useState(false),
    [events, setEvents] = useState<TelemetryEvent[]>([]),
    [loading, setLoading] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    if (!requested || !agentID || !executionID) return;
    let live = true;
    const controller = new AbortController();
    const query = new URLSearchParams({
      agent_id: String(agentID),
      type: "tool",
      limit: "1000",
    });
    if (threadID) query.set("thread_id", threadID);
    setLoading(true);
    fetch(`/api/telemetry?${query}`, {
      credentials: "same-origin",
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Tool activity unavailable (${response.status})`);
        return response.json();
      })
      .then((value) => {
        if (!live) return;
        setEvents(Array.isArray(value) ? value : []);
        setError("");
      })
      .catch((reason) => {
        if (live && reason?.name !== "AbortError") setError(reason.message);
      })
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
      controller.abort();
    };
  }, [requested, agentID, threadID, executionID]);
  const calls = useMemo(
    () => callsForExecution(events, executionID || ""),
    [events, executionID],
  );
  if (!agentID || !executionID)
    return (
      <p className="small muted tool-unavailable">
        Tool activity was not tracked for this execution.
      </p>
    );
  return (
    <details
      className="tool-activity"
      onToggle={(event) => {
        if (event.currentTarget.open) setRequested(true);
      }}
    >
      <summary>
        Tool calls{requested && !loading ? ` · ${calls.length}` : ""}
      </summary>
      {loading ? (
        <p className="small muted">Loading tool activity…</p>
      ) : error ? (
        <p className="notice small">{error}</p>
      ) : calls.length ? (
        <ol className="tool-calls">
          {calls.map((call) => {
            const source = resolveToolSource(call.name, sources);
            return (
              <li key={call.id}>
                <SourceIcon source={source} />
                <span className="tool-call-copy">
                  <span className="row between">
                    <strong>{source?.label || call.name}</strong>
                    <span className={`pill ${call.failed ? "failed" : "completed"}`}>
                      {call.failed ? "failed" : "called"}
                    </span>
                  </span>
                  <span className="small muted tool-name">{call.name}</span>
                  <span className="tool-reason">
                    <code>_reason</code>: {call.reason || "No reason supplied"}
                  </span>
                </span>
              </li>
            );
          })}
        </ol>
      ) : (
        <p className="small muted">No tool calls were recorded for this execution.</p>
      )}
    </details>
  );
}
