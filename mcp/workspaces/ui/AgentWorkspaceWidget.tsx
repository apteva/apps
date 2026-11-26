import { useEffect, useMemo, useState } from "react";
import {
  Command,
  EmptyIcon,
  StatusDot,
  WidgetHostProps,
  Workspace,
  apiRequest,
  commandDuration,
  intSetting,
  panelLink,
  timeLeft,
  useWidgetData,
  widgetCSS,
} from "./widgetShared";

interface ListResponse {
  workspaces: Workspace[];
}
interface DetailResponse {
  workspace: Workspace;
  commands: Command[];
}
interface LogsResponse {
  logs?: string;
}

const ACTIVE = ["queued", "running", "cancelling"];

const sample: { workspace: Workspace; commands: Command[]; output: string } = {
  workspace: { id: "wsp_a", name: "api-fix", status: "busy", lifecycle_status: "running", host_label: "Local Docker", expires_at: new Date(Date.now() + 80 * 60000).toISOString(), preview: { status: "live", url: "http://127.0.0.1:49152/" } },
  commands: [
    { id: "c3", display_command: "go test ./...", status: "running", created_at: "", started_at: new Date(Date.now() - 25000).toISOString() },
    { id: "c2", display_command: "go build ./...", status: "succeeded", exit_code: 0, created_at: "", started_at: new Date(Date.now() - 90000).toISOString(), finished_at: new Date(Date.now() - 70000).toISOString() },
    { id: "c1", display_command: "go vet ./...", status: "failed", exit_code: 1, created_at: "", started_at: new Date(Date.now() - 200000).toISOString(), finished_at: new Date(Date.now() - 190000).toISOString() },
  ],
  output: "ok  \tgithub.com/example/api/store\t0.41s\n--- FAIL: TestHandler (0.02s)\n    handler_test.go:31: want 200, got 500",
};

export default function AgentWorkspaceWidget(props: WidgetHostProps) {
  const settings = props.widgetSettings;
  const outputLines = intSetting(settings, "output_lines", 12, 0, 40);
  const recentLimit = intSetting(settings, "recent_limit", 5, 0, 15);
  const [chosen, setChosen] = useState("");
  const [now, setNow] = useState(Date.now());
  // The thread sidebar shows that thread's workspace; agent surfaces show the agent's.
  const byThread = props.slot === "dashboard.thread_sidebar" && !!props.threadId;
  const scoped = byThread || (props.agentId ?? 0) > 0;
  const live = !props.preview && !!props.projectId && scoped;

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const list = useWidgetData<ListResponse>(
    props,
    JSON.stringify({ agent: props.agentId, thread: byThread ? props.threadId : "" }),
    () =>
      apiRequest<ListResponse>(props, "/workspaces", {
        kind: "dev",
        owner_agent_id: !byThread && props.agentId ? String(props.agentId) : "",
        owner_thread_id: byThread ? props.threadId || "" : "",
        limit: "20",
      }),
    live,
  );
  const workspaces = props.preview ? [sample.workspace] : list.data?.workspaces || [];
  const selected = useMemo(() => {
    if (chosen) {
      const match = workspaces.find((workspace) => workspace.id === chosen);
      if (match) return match;
    }
    const rank = (w: Workspace) => (w.status === "busy" ? 0 : w.status === "running" ? 1 : 2);
    return [...workspaces].sort((a, b) => rank(a) - rank(b) || String(b.last_activity_at || "").localeCompare(String(a.last_activity_at || "")))[0];
  }, [workspaces, chosen]);

  const detail = useWidgetData<DetailResponse>(
    props,
    `${selected?.id || ""}:${selected?.current_command?.id || ""}:${selected?.status || ""}`,
    () => apiRequest<DetailResponse>(props, `/workspaces/${encodeURIComponent(selected!.id)}`),
    live && !!selected,
  );
  const commands = props.preview ? sample.commands : detail.data?.commands || [];
  const current = commands.find((command) => ACTIVE.includes(command.status));
  const finished = commands.filter((command) => !ACTIVE.includes(command.status));
  const latest = finished[0];
  // Output appears when a command finishes, so show the latest finished one.
  const logs = useWidgetData<LogsResponse>(
    props,
    `${selected?.id || ""}:${latest?.id || ""}:${outputLines}`,
    () => apiRequest<LogsResponse>(props, `/workspaces/${encodeURIComponent(selected!.id)}/commands/${encodeURIComponent(latest!.id)}/logs`, { tail: String(outputLines) }),
    live && !!selected && !!latest && outputLines > 0,
  );
  const output = props.preview ? sample.output : (logs.data?.logs || "").split("\n").slice(-outputLines).join("\n").trimEnd();

  if (!props.preview && !props.projectId) {
    return (
      <section className="wsw">
        <style>{widgetCSS}</style>
        <div className="wsw-card wsw-empty">Select a project to see agent workspaces.</div>
      </section>
    );
  }
  if (!props.preview && !scoped) {
    return (
      <section className="wsw">
        <style>{widgetCSS}</style>
        <div className="wsw-card wsw-empty">Open an agent or a thread to see its workspace.</div>
      </section>
    );
  }

  const error = list.error || detail.error;
  const left = selected ? timeLeft(selected.expires_at, now) : { label: "", soon: false };
  return (
    <section className="wsw" aria-label="Agent workspace">
      <style>{widgetCSS}</style>
      <header className="wsw-head">
        <div style={{ minWidth: 0 }}>
          <h2>Workspace</h2>
          {selected ? (
            <p className="wsw-trunc">
              <a href={panelLink(props, selected.id)}>{selected.name}</a> · {selected.host_label}
            </p>
          ) : (
            <p>{byThread ? "This thread" : "This agent"}</p>
          )}
        </div>
        {workspaces.length > 1 && selected && (
          <select className="wsw-select" aria-label="Choose workspace" value={selected.id} onChange={(event) => setChosen(event.target.value)}>
            {workspaces.map((workspace) => (
              <option key={workspace.id} value={workspace.id}>
                {workspace.name} ({workspace.status})
              </option>
            ))}
          </select>
        )}
      </header>
      {error && <div className="wsw-banner" role="alert">{error}</div>}
      {!selected && list.loaded && !list.error && (
        <div className="wsw-card wsw-empty">
          <EmptyIcon />
          <span>{byThread ? "No workspace for this thread yet." : "No workspace for this agent yet."}</span>
        </div>
      )}
      {!selected && !list.loaded && !props.preview && <div className="wsw-card wsw-empty wsw-dim">Loading workspace…</div>}
      {selected && (
        <>
          <div className="wsw-card">
            <div className="wsw-ws-top">
              <StatusDot status={selected.status} />
              <span className="wsw-main">
                <span className="wsw-line">
                  <span>{selected.status === "busy" ? "Running a command" : selected.status}</span>
                  {selected.status === "failed" && selected.last_error && <span className="wsw-error wsw-trunc">{selected.last_error}</span>}
                </span>
              </span>
              <span className="wsw-side">
                {selected.preview?.status === "live" && selected.preview.url && (
                  <a className="wsw-pill wsw-link" href={selected.preview.url} target="_blank" rel="noreferrer">Preview</a>
                )}
                {selected.preview?.status === "crashed" && <span className="wsw-pill wsw-error">Preview crashed</span>}
                {selected.lifecycle_status === "running" && left.label && <span className={`wsw-pill ${left.soon ? "wsw-warn" : ""}`}>{left.label}</span>}
              </span>
            </div>
          </div>
          {current && (
            <div className="wsw-card">
              <h3>Running now</h3>
              <div className="wsw-line">
                <span className="wsw-mono wsw-trunc">{current.display_command}</span>
                <span className="wsw-when">{commandDuration(current, now)}</span>
              </div>
              <div className="wsw-dim" style={{ marginTop: 2 }}>Output appears when the command finishes.</div>
            </div>
          )}
          {latest && outputLines > 0 && (
            <div className="wsw-card">
              <h3>
                Last output · <span className="wsw-mono wsw-trunc" style={{ textTransform: "none", letterSpacing: 0, fontWeight: 400 }}>{latest.display_command}</span>
              </h3>
              <pre className="wsw-output">{output || (logs.loaded || props.preview ? "No output." : "Loading…")}</pre>
            </div>
          )}
          {recentLimit > 0 && finished.length > 0 && (
            <div className="wsw-card">
              <h3>Recent commands</h3>
              <ul className="wsw-list">
                {finished.slice(0, recentLimit).map((command) => {
                  const failed = command.status !== "succeeded";
                  return (
                    <li key={command.id} className="wsw-act" style={{ gridTemplateColumns: "minmax(0,1fr) auto auto" }}>
                      <span className={`wsw-mono wsw-trunc ${failed ? "wsw-error" : ""}`} title={command.display_command}>{command.display_command}</span>
                      <span className={failed ? "wsw-error" : "wsw-dim"}>
                        {command.exit_code != null ? `exit ${command.exit_code}` : command.status.replace("_", " ")}
                      </span>
                      <span className="wsw-when">{commandDuration(command, now)}</span>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </>
      )}
    </section>
  );
}
