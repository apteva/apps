import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  Activity,
  ArrowDownLeft,
  ArrowRight,
  ArrowUpRight,
  Bot,
  Check,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Clock,
  Copy,
  ExternalLink,
  FileText,
  Globe2,
  LayoutGrid,
  Link2,
  Loader2,
  MessageSquare,
  Network,
  Plus,
  RefreshCw,
  Search,
  Server,
  Shield,
  Unplug,
  X,
  AlertCircle,
} from "lucide-react";
import { useAppEvents } from "./events";
import { styles } from "./styles";

const API = "/api/apps/a2a";
interface Props {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}
type View = "Overview" | "Agents" | "Exchanges" | "Connections";
interface Task {
  id: number;
  kind: string;
  status: string;
  from_agent_id: number;
  from_agent_name?: string;
  to_agent_id: number;
  to_agent_name?: string;
  from_thread_id?: string;
  to_thread_id?: string;
  direction?: string;
  peer_id?: string;
  created_at: string;
  updated_at: string;
  last_synced_at?: string;
  preview?: string;
  message_count?: number;
  pending_delivery?: number;
  poll_failures?: number;
  artifacts?: unknown[];
}
interface Message {
  id: number;
  from_agent_id: number;
  body: string;
  status_after?: string;
  created_at: string;
}
interface Card {
  name: string;
  description: string;
  version: string;
  provider?: { organization?: string };
  skills?: {
    id: string;
    name: string;
    description?: string;
    examples?: string[];
  }[];
  supportedInterfaces?: {
    url: string;
    protocolVersion: string;
    protocolBinding: string;
  }[];
  capabilities?: { streaming?: boolean; pushNotifications?: boolean };
  defaultInputModes?: string[];
  defaultOutputModes?: string[];
}
interface Agent {
  address: string;
  id?: number;
  name: string;
  description: string;
  peer_id: string;
  peer_name: string;
  kind: string;
  status: string;
  skills?: string[];
  card?: Card;
  fetched_at?: string;
  expires_at?: string;
}
interface Connection {
  id: string;
  name: string;
  kind: "node" | "agent_card";
  base_url: string;
  card_url?: string;
  protocol_version?: string;
  managed_by: string;
  authenticated: boolean;
  agents?: string[];
  discover_agents?: string[];
  invoke_agents?: string[];
}
interface NetworkData {
  agents: Agent[];
  node: { node_id: string; display_name: string };
  warnings?: string[];
}
interface OverviewData {
  total: number;
  active: number;
  input_required: number;
  failed: number;
  completed: number;
  attention: number;
  pending_delivery: number;
  as_of: string;
}
interface TaskPage {
  tasks: Task[];
  total: number;
  limit: number;
  offset: number;
}
interface CheckResult {
  ok: boolean;
  message: string;
  checked_at: string;
  latency_ms: number;
  agents?: number;
}
interface Filters {
  q: string;
  status: string;
  peer: string;
  agent_address: string;
  from: string;
  to: string;
}
const clearFilters: Filters = {
  q: "",
  status: "",
  peer: "",
  agent_address: "",
  from: "",
  to: "",
};
const tabs = [
  { name: "Overview", icon: LayoutGrid },
  { name: "Agents", icon: Bot },
  { name: "Exchanges", icon: MessageSquare },
  { name: "Connections", icon: Link2 },
] as const;
function url(project: string, path: string) {
  return `${API}${path}${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(project)}`;
}
async function request<T>(
  project: string,
  path: string,
  options?: RequestInit,
): Promise<T> {
  const res = await fetch(url(project, path), {
    credentials: "same-origin",
    ...options,
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(
      body.length < 300 && !body.includes("<html")
        ? body
        : `Request failed (${res.status}).`,
    );
  }
  return res.json();
}
function useResource<T>(project: string, path: string | null, revision = 0) {
  const [state, setState] = useState<{
    data?: T;
    error: string;
    loading: boolean;
    key: string;
  }>({ error: "", loading: true, key: "" });
  const [retry, setRetry] = useState(0);
  const key = `${project}:${path}`;
  useEffect(() => {
    if (path === null) return;
    const controller = new AbortController();
    setState((previous) => ({
      data: previous.key === key ? previous.data : undefined,
      error: "",
      loading: true,
      key,
    }));
    request<T>(project, path, { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted)
          setState({ data, loading: false, error: "", key });
      })
      .catch((err) => {
        if (!controller.signal.aborted)
          setState((previous) => ({
            ...previous,
            loading: false,
            error: err.message,
          }));
      });
    return () => controller.abort();
  }, [project, path, key, revision, retry]);
  return {
    ...state,
    data: state.key === key ? state.data : undefined,
    loading: path !== null && (state.loading || state.key !== key),
    reload: () => setRetry((n) => n + 1),
  };
}
function time(value?: string) {
  if (!value) return "Never";
  const date = new Date(value);
  return Number.isNaN(+date) ? "Unknown" : date.toLocaleString();
}
function age(value?: string) {
  if (!value) return "Never";
  const delta = Date.now() - new Date(value).getTime();
  if (!Number.isFinite(delta)) return "Unknown";
  const mins = Math.max(0, Math.floor(delta / 60000));
  return mins < 1
    ? "Just now"
    : mins < 60
      ? `${mins}m ago`
      : mins < 1440
        ? `${Math.floor(mins / 60)}h ago`
        : `${Math.floor(mins / 1440)}d ago`;
}
function participant(t: Task, side: "from" | "to") {
  const remote =
    side === "from" ? t.direction === "inbound" : t.direction === "outbound";
  return (
    t[`${side}_agent_name`] ||
    (remote ? "Remote agent" : `Agent ${t[`${side}_agent_id`]}`)
  );
}
function peerLabel(t: Task, connections: Connection[]) {
  return !t.peer_id
    ? "Local exchange"
    : connections.find((c) => c.id === t.peer_id)?.name || t.peer_id;
}
function grantNames(grants: string[] | undefined, agents: Agent[]) {
  return grants?.length
    ? grants
        .map((g) =>
          g === "*"
            ? "All exposed agents (all projects)"
            : agents.find(
                (a) =>
                  a.kind === "local" && (String(a.id) === g || a.name === g),
              )?.name || g,
        )
        .join(", ")
    : "No inbound access";
}
function Badge({ status }: { status: string }) {
  const tone = ["completed", "running"].includes(status)
    ? "good"
    : status === "failed"
      ? "bad"
      : status === "input_required"
        ? "warn"
        : ["working", "submitted"].includes(status)
          ? "active"
          : "";
  return <span className={`badge ${tone}`}>{status.replaceAll("_", " ")}</span>;
}
function Empty({
  title,
  children,
  action,
}: {
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <Network size={28} strokeWidth={1.4} />
      <h3>{title}</h3>
      <p>{children}</p>
      {action}
    </div>
  );
}
function ErrorNotice({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  return (
    <div role="alert" className="notice error">
      <AlertCircle size={16} />
      <span className="grow">{message}</span>
      {retry && (
        <button className="btn quiet" onClick={retry}>
          Retry
        </button>
      )}
    </div>
  );
}
function Loading() {
  return (
    <div className="empty" role="status">
      <Loader2 className="spin" size={20} />
      Loading…
    </div>
  );
}
function CopyButton({ value }: { value: string }) {
  const [note, setNote] = useState("");
  return (
    <button
      className="btn quiet"
      title={value}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setNote("Copied");
        } catch {
          setNote("Copy unavailable");
        }
      }}
    >
      {note === "Copied" ? <Check size={13} /> : <Copy size={13} />}
      {note || "Copy address"}
    </button>
  );
}
function Sheet({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>("button")?.focus();
    return () => previous?.focus();
  }, []);
  return (
    <div
      className="overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={ref}
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            closeRef.current();
          }
          if (e.key === "Tab") {
            const nodes = [
              ...(ref.current?.querySelectorAll<HTMLElement>(
                'button:not(:disabled),a[href],input,select,textarea,[tabindex="0"]',
              ) || []),
            ];
            const first = nodes[0],
              last = nodes.at(-1);
            if (e.shiftKey && document.activeElement === first) {
              e.preventDefault();
              last?.focus();
            } else if (!e.shiftKey && document.activeElement === last) {
              e.preventDefault();
              first?.focus();
            }
          }
        }}
      >
        <div className="row between">
          <h2>{title}</h2>
          <button
            className="icon-btn"
            aria-label="Close panel"
            onClick={onClose}
          >
            <X size={20} />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
function TaskRow({
  task,
  connections,
  selected,
  onClick,
}: {
  task: Task;
  connections: Connection[];
  selected?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`list-row ${selected ? "selected" : ""}`}
      onClick={onClick}
      aria-pressed={selected}
    >
      <div className="row between">
        <span className="row grow">
          <strong className="truncate">{participant(task, "from")}</strong>
          <ArrowRight size={12} />
          <strong className="truncate">{participant(task, "to")}</strong>
        </span>
        <Badge status={task.status} />
      </div>
      <p>
        {task.preview ||
          (task.kind === "ask"
            ? "Request awaiting its first recorded message"
            : "One-way message")}
      </p>
      <div className="row wrap">
        <small>{peerLabel(task, connections)}</small>
        <small>· #{task.id}</small>
        {!!task.pending_delivery && (
          <span className="badge warn">Delivery pending</span>
        )}
        {!!task.poll_failures &&
          ["working", "submitted", "input_required"].includes(task.status) && (
            <span className="badge warn">Sync retrying</span>
          )}
        <small style={{ marginLeft: "auto" }} title={time(task.updated_at)}>
          {age(task.updated_at)}
        </small>
      </div>
    </button>
  );
}
function EmbeddedFile({ bytes, name }: { bytes: string; name: string }) {
  const [error, setError] = useState("");
  const download = () => {
    try {
      const decoded = atob(bytes);
      const data = Uint8Array.from(decoded, (c) => c.charCodeAt(0));
      const href = URL.createObjectURL(
        new Blob([data], { type: "application/octet-stream" }),
      );
      const a = document.createElement("a");
      a.href = href;
      a.download = name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(href), 1000);
    } catch {
      setError(
        "This embedded file could not be decoded. Its original data is available below.",
      );
    }
  };
  return (
    <>
      <button className="btn quiet" onClick={download}>
        <FileText size={13} />
        Download {name}
      </button>
      {error && <p className="muted">{error}</p>}
    </>
  );
}
function ArtifactView({ value, index }: { value: unknown; index: number }) {
  const artifact =
    value && typeof value === "object"
      ? (value as Record<string, unknown>)
      : {};
  const parts = Array.isArray(artifact.parts) ? artifact.parts : [];
  return (
    <article className="panel pad stack gap-sm">
      <div className="row">
        <FileText size={16} />
        <strong>
          {typeof artifact.name === "string"
            ? artifact.name
            : `Artifact ${index + 1}`}
        </strong>
      </div>
      {typeof artifact.description === "string" && (
        <p className="muted">{artifact.description}</p>
      )}
      {parts.map((part: any, i: number) => {
        if (!part || typeof part !== "object") return null;
        const href = part.url || part.file?.uri;
        const safe = typeof href === "string" && /^https?:\/\//i.test(href);
        const text = typeof part.text === "string" ? part.text : null;
        return (
          <div key={i}>
            {text && <p className="message-body">{text}</p>}
            {part.data && <pre>{JSON.stringify(part.data, null, 2)}</pre>}
            {safe && (
              <a
                className="btn quiet"
                href={href}
                target="_blank"
                rel="noopener noreferrer"
              >
                <ExternalLink size={13} />
                {typeof part.file?.name === "string"
                  ? part.file.name
                  : "Open returned file"}
              </a>
            )}
            {typeof (part.raw || part.file?.bytes) === "string" && (
              <EmbeddedFile
                bytes={part.raw || part.file.bytes}
                name={
                  typeof part.file?.name === "string"
                    ? part.file.name
                    : "artifact.bin"
                }
              />
            )}
          </div>
        );
      })}
      <details>
        <summary className="muted">Artifact JSON</summary>
        <pre>{JSON.stringify(value, null, 2)}</pre>
      </details>
    </article>
  );
}
function ExchangeDetail({
  project,
  task,
  revision,
  connections,
}: {
  project: string;
  task: Task;
  revision: number;
  connections: Connection[];
}) {
  const result = useResource<{ task: Task; messages: Message[] }>(
    project,
    `/tasks/${task.id}/messages`,
    revision,
  );
  const detail = { ...task, ...result.data?.task };
  return (
    <section className="panel">
      <div className="section-head stack gap-sm">
        <div className="row between">
          <span className="eyebrow">Exchange #{task.id}</span>
          <Badge status={detail.status} />
        </div>
        <h2>
          {participant(task, "from")} <span className="muted">→</span>{" "}
          {participant(task, "to")}
        </h2>
        <p className="muted" style={{ fontSize: 12 }}>
          {detail.kind === "ask" ? "Request with reply" : "One-way message"} ·{" "}
          {peerLabel(task, connections)}
        </p>
      </div>
      <div className="detail-meta">
        <span className="row">
          <Clock size={13} />
          Started {time(task.created_at)}
        </span>
        {(["from", "to"] as const).map((side) => {
          const local =
            side === "from"
              ? task.direction !== "inbound"
              : task.direction !== "outbound";
          const id = task[`${side}_agent_id`];
          return (
            local &&
            id > 0 && (
              <a
                key={side}
                href={`/agents/${id}?thread=${encodeURIComponent(detail[`${side}_thread_id`] || "main")}`}
                className="row"
              >
                {participant(task, side)} thread <ArrowUpRight size={13} />
              </a>
            )
          );
        })}
      </div>
      {!!task.pending_delivery && (
        <div className="pad">
          <div className="notice">
            <AlertCircle size={15} />A recorded result is waiting to be
            delivered. Automatic retries are active.
          </div>
        </div>
      )}
      {task.direction === "outbound" && (
        <div className="detail-meta">
          Remote sync: {time(detail.last_synced_at)}
          {!!task.poll_failures && (
            <> · {task.poll_failures} failed attempts; retrying automatically</>
          )}
        </div>
      )}
      {result.error ? (
        <div className="pad">
          <ErrorNotice
            message={`Could not load this exchange: ${result.error}`}
            retry={result.reload}
          />
        </div>
      ) : result.loading && !result.data ? (
        <Loading />
      ) : (
        <div className="timeline">
          {!result.data?.messages.length ? (
            <Empty title="No messages recorded">
              This exchange has no recorded message content yet.
            </Empty>
          ) : (
            result.data.messages.map((m) => (
              <article className="timeline-item" key={m.id}>
                <div className="row wrap">
                  <strong>
                    {participant(
                      task,
                      m.from_agent_id === task.from_agent_id ? "from" : "to",
                    )}
                  </strong>
                  {m.status_after && <Badge status={m.status_after} />}
                  <small className="muted" title={time(m.created_at)}>
                    {time(m.created_at)}
                  </small>
                </div>
                <p className="message-body">{m.body}</p>
              </article>
            ))
          )}
        </div>
      )}
      {!!detail.artifacts?.length && (
        <div className="artifacts stack">
          <h3>
            Returned artifacts{" "}
            <span className="muted">({detail.artifacts.length})</span>
          </h3>
          {detail.artifacts.map((value, i) => (
            <ArtifactView key={i} value={value} index={i} />
          ))}
        </div>
      )}
    </section>
  );
}
function AgentDetail({
  project,
  agent,
  onClose,
  onExchanges,
}: {
  project: string;
  agent: Agent;
  onClose: () => void;
  onExchanges: (agent: Agent) => void;
}) {
  const remote = agent.kind !== "local";
  const result = useResource<{ card: Card }>(
    project,
    remote
      ? `/network/card?address=${encodeURIComponent(agent.address)}`
      : null,
  );
  const card = result.data?.card || agent.card;
  return (
    <Sheet title={agent.name} onClose={onClose}>
      <div className="row">
        <div className="avatar">
          <Bot size={21} />
        </div>
        <div>
          <strong>{agent.peer_name}</strong>
          <p className="muted">{remote ? "Remote agent" : "Local agent"}</p>
        </div>
        <Badge status={agent.status} />
      </div>
      <p>
        {agent.description ||
          card?.description ||
          "This agent has no published description."}
      </p>
      <div className="row wrap">
        <CopyButton value={agent.address} />
        <button className="btn" onClick={() => onExchanges(agent)}>
          <MessageSquare size={13} />
          View exchanges
        </button>
        {agent.id && (
          <a className="btn" href={`/agents/${agent.id}`}>
            Open agent <ArrowUpRight size={13} />
          </a>
        )}
      </div>
      {remote && (
        <p className="notice">
          Remote directory entries are cached. Check the connection to refresh
          discovery; a published card does not prove task execution is
          available.
        </p>
      )}
      {result.error && (
        <ErrorNotice message={result.error} retry={result.reload} />
      )}
      {remote && result.loading && (
        <p className="muted row">
          <Loader2 size={14} className="spin" />
          Retrieving current Agent Card…
        </p>
      )}
      <div className="stack">
        <h3>Capabilities</h3>
        {card?.skills?.length ? (
          card.skills.map((skill) => (
            <div className="panel pad" key={skill.id}>
              <strong>{skill.name || skill.id}</strong>
              <p className="subtitle">
                {skill.description || "No description provided."}
              </p>
              {skill.examples?.map((example, i) => (
                <p key={i} className="message-body muted">
                  “{example}”
                </p>
              ))}
            </div>
          ))
        ) : agent.skills?.length ? (
          <div className="chips">
            {agent.skills.map((s) => (
              <span className="badge" key={s}>
                {s}
              </span>
            ))}
          </div>
        ) : (
          <p className="muted">No capabilities published yet.</p>
        )}
      </div>
      {card && (
        <>
          <dl className="kv">
            <dt>Provider</dt>
            <dd>{card.provider?.organization || "Not specified"}</dd>
            <dt>Version</dt>
            <dd>{card.version || "Not specified"}</dd>
            <dt>Input</dt>
            <dd>{card.defaultInputModes?.join(", ") || "Not specified"}</dd>
            <dt>Output</dt>
            <dd>{card.defaultOutputModes?.join(", ") || "Not specified"}</dd>
            <dt>Streaming</dt>
            <dd>
              {card.capabilities?.streaming ? "Supported" : "Not advertised"}
            </dd>
          </dl>
          <details>
            <summary>Agent Card JSON</summary>
            <pre>{JSON.stringify(card, null, 2)}</pre>
          </details>
        </>
      )}
    </Sheet>
  );
}
function Grants({
  title,
  value,
  onChange,
  agents,
}: {
  title: string;
  value: string[];
  onChange: (v: string[]) => void;
  agents: Agent[];
}) {
  const [raw, setRaw] = useState(value.join(", "));
  const choose = (next: string[]) => {
    setRaw(next.join(", "));
    onChange(next);
  };
  return (
    <div className="stack gap-sm">
      <h3>{title}</h3>
      <div className="checks">
        {agents
          .filter((a) => a.kind === "local")
          .map((a) => {
            const id = String(a.id);
            return (
              <label key={a.address}>
                <input
                  type="checkbox"
                  checked={value.includes(id) || value.includes(a.name)}
                  disabled={value.includes("*")}
                  onChange={(e) =>
                    choose(
                      e.target.checked
                        ? [...value, id]
                        : value.filter((v) => v !== id && v !== a.name),
                    )
                  }
                />
                {a.name}
              </label>
            );
          })}
      </div>
      <label className="field">
        Agent names or IDs
        <input
          className="input"
          aria-label={title}
          placeholder="None — no inbound access"
          value={raw}
          onChange={(e) => {
            setRaw(e.target.value);
            onChange(
              e.target.value
                .split(",")
                .map((s) => s.trim())
                .filter(Boolean),
            );
          }}
        />
        <small>
          Empty grants deny access. * grants every exposed agent across this
          installation, including other projects.
        </small>
      </label>
    </div>
  );
}
function ConnectionWizard({
  project,
  agents,
  edit,
  onClose,
  onSaved,
}: {
  project: string;
  agents: Agent[];
  edit?: Connection;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [step, setStep] = useState(edit ? 1 : 0);
  const [kind, setKind] = useState<"node" | "agent_card">(
    edit?.kind || "agent_card",
  );
  const [fields, setFields] = useState({
    card_url: "",
    id: "",
    name: "",
    base_url: "",
    token: "",
  });
  const [discover, setDiscover] = useState<string[]>(
      edit?.discover_agents || [],
    ),
    [invoke, setInvoke] = useState<string[]>(edit?.invoke_agents || []);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const valid =
    !!edit ||
    (kind === "agent_card"
      ? !!fields.card_url.trim()
      : !!fields.id.trim() &&
        !!fields.base_url.trim() &&
        !!fields.token.trim());
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      await request(
        project,
        edit ? `/connections/${encodeURIComponent(edit.id)}` : "/connections",
        {
          method: edit ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(
            edit
              ? { discover_agents: discover, invoke_agents: invoke }
              : {
                  ...fields,
                  kind,
                  discover_agents: discover,
                  invoke_agents: invoke,
                },
          ),
        },
      );
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const field = (
    key: keyof typeof fields,
    label: string,
    placeholder: string,
    type = "text",
  ) => (
    <label className="field">
      {label}
      <input
        className="input"
        type={type}
        autoComplete="off"
        value={fields[key]}
        placeholder={placeholder}
        onChange={(e) => setFields((f) => ({ ...f, [key]: e.target.value }))}
      />
    </label>
  );
  return (
    <Sheet
      title={edit ? `Access · ${edit.name}` : "Add connection"}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <p className="muted">
        Connections are shared across this A2A installation. Local agents and
        exchanges remain scoped to the current project.
      </p>
      {!edit && (
        <div className="steps">
          {["01 · Choose type", "02 · Configure", "03 · Review"].map(
            (label, i) => (
              <span
                key={label}
                className={`step ${step === i ? "current" : ""}`}
              >
                {label}
              </span>
            ),
          )}
        </div>
      )}
      {step === 0 && (
        <div className="stack">
          {(
            [
              {
                kind: "agent_card",
                icon: Globe2,
                title: "Public agent",
                detail: "Connect an external agent using its Agent Card URL.",
              },
              {
                kind: "node",
                icon: Server,
                title: "Apteva installation",
                detail: "Connect a node and discover the agents it shares.",
              },
            ] as const
          ).map((option) => (
            <button
              key={option.kind}
              className="panel option"
              aria-pressed={kind === option.kind}
              onClick={() => setKind(option.kind)}
            >
              <option.icon size={24} />
              <div>
                <strong>{option.title}</strong>
                <p className="subtitle">{option.detail}</p>
              </div>
              {kind === option.kind && <CheckCircle2 size={18} />}
            </button>
          ))}
        </div>
      )}
      {step === 1 && (
        <div className="stack">
          {!edit &&
            (kind === "agent_card" ? (
              <>
                {field(
                  "card_url",
                  "Agent Card URL",
                  "https://agent.example/.well-known/agent-card.json",
                  "url",
                )}
                {field(
                  "token",
                  "Bearer token (optional)",
                  "Leave empty for anonymous discovery",
                  "password",
                )}
              </>
            ) : (
              <>
                {field("name", "Display name", "e.g. Research team")}
                {field("id", "Connection ID", "research-team")}
                {field(
                  "base_url",
                  "A2A base URL",
                  "https://node.example/api/apps/a2a",
                  "url",
                )}
                {field(
                  "token",
                  "Pairing token",
                  "Reciprocal pairing token",
                  "password",
                )}
              </>
            ))}
          {kind === "node" && (
            <>
              <div className="notice">
                <Shield size={17} />
                <span>
                  These grants control which of your agents this remote node may
                  discover and invoke. Access to its agents is controlled on the
                  remote node.
                </span>
              </div>
              <Grants
                title="Agents this node may discover"
                agents={agents}
                value={discover}
                onChange={setDiscover}
              />
              <Grants
                title="Agents this node may invoke"
                agents={agents}
                value={invoke}
                onChange={setInvoke}
              />
            </>
          )}
        </div>
      )}
      {step === 2 && (
        <div className="stack">
          <div className="panel pad">
            <h3>
              {kind === "node" ? fields.name || fields.id : "Public agent"}
            </h3>
            <p className="subtitle break">
              {kind === "node" ? fields.base_url : fields.card_url}
            </p>
          </div>
          <dl className="kv">
            <dt>Authentication</dt>
            <dd>{fields.token ? "Bearer token provided" : "Anonymous"}</dd>
            {kind === "node" && (
              <>
                <dt>Discovery grants</dt>
                <dd>{discover.join(", ") || "No inbound access"}</dd>
                <dt>Invocation grants</dt>
                <dd>{invoke.join(", ") || "No inbound access"}</dd>
              </>
            )}
          </dl>
          <p className="notice">
            {kind === "agent_card"
              ? "The Agent Card will be validated before saving. No message will be sent."
              : "Save the pairing, then run Check connection to verify remote discovery. No message will be sent."}
          </p>
        </div>
      )}
      {error && <ErrorNotice message={error} />}
      <div className="row between" style={{ marginTop: "auto" }}>
        {step > 0 && !edit ? (
          <button
            className="btn"
            disabled={busy}
            onClick={() => setStep((s) => s - 1)}
          >
            <ChevronLeft size={14} />
            Back
          </button>
        ) : (
          <span />
        )}
        {step < 2 && !edit ? (
          <button
            className="btn primary"
            disabled={step === 1 && !valid}
            onClick={() => setStep((s) => s + 1)}
          >
            Continue <ChevronRight size={14} />
          </button>
        ) : (
          <button
            className="btn primary"
            disabled={busy || !valid}
            onClick={save}
          >
            {busy ? (
              <Loader2 size={14} className="spin" />
            ) : (
              <Check size={14} />
            )}
            {edit ? "Save access" : "Save connection"}
          </button>
        )}
      </div>
    </Sheet>
  );
}

export default function A2APanel(props: Props) {
  return <Panel key={`${props.projectId}:${props.installId}`} {...props} />;
}
function Panel({ projectId: project }: Props) {
  const [view, setView] = useState<View>("Overview"),
    [revision, setRevision] = useState(0);
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null),
    [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [wizard, setWizard] = useState<false | "new" | Connection>(false),
    [remove, setRemove] = useState<Connection | null>(null);
  const [mutationError, setMutationError] = useState(""),
    [removing, setRemoving] = useState(false);
  const [checks, setChecks] = useState<Record<string, CheckResult>>({}),
    [checking, setChecking] = useState<Record<string, boolean>>({});
  const [filters, setFilters] = useState<Filters>(clearFilters),
    [offset, setOffset] = useState(0);
  const [agentQuery, setAgentQuery] = useState(""),
    [agentPeer, setAgentPeer] = useState(""),
    [agentView, setAgentView] = useState<"directory" | "map">("directory");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedQuery(filters.q), 250);
    return () => clearTimeout(timer);
  }, [filters.q]);
  const network = useResource<NetworkData>(project, "/network", revision);
  const connections = useResource<{ connections: Connection[] }>(
    project,
    "/connections",
    revision,
  );
  const overview = useResource<OverviewData>(project, "/overview", revision);
  const recent = useResource<TaskPage>(project, "/tasks?limit=5", revision);
  const attention = useResource<TaskPage>(
    project,
    "/tasks?status=attention&limit=4",
    revision,
  );
  const params = new URLSearchParams({
    ...filters,
    q: debouncedQuery,
    limit: "30",
    offset: String(offset),
  });
  const exchanges = useResource<TaskPage>(
    project,
    view === "Exchanges" ? `/tasks?${params}` : null,
    revision,
  );
  const cs = connections.data?.connections || [],
    agents = network.data?.agents || [];
  const refresh = useCallback(() => setRevision((n) => n + 1), []);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  useAppEvents("a2a", project, (ev) => {
    if (ev.topic === "task.created" || ev.topic === "task.updated") {
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(refresh, 200);
    }
  });
  const openExchanges = (next: Partial<Filters> = {}, task?: Task) => {
    setFilters({ ...clearFilters, ...next });
    setDebouncedQuery(next.q || "");
    setOffset(0);
    setSelectedTask(task || null);
    setView("Exchanges");
  };
  const filter = (key: keyof Filters, value: string) => {
    setFilters((f) => ({ ...f, [key]: value }));
    setOffset(0);
    setSelectedTask(null);
  };
  const activeTask =
    selectedTask &&
    (exchanges.data?.tasks.find((t) => t.id === selectedTask.id) ||
      selectedTask);
  const filteredAgents = useMemo(
    () =>
      agents.filter(
        (a) =>
          (!agentPeer || a.peer_id === agentPeer) &&
          `${a.name} ${a.description} ${a.peer_name} ${a.skills?.join(" ") || ""}`
            .toLowerCase()
            .includes(agentQuery.toLowerCase()),
      ),
    [agents, agentPeer, agentQuery],
  );
  const checkConnection = async (c: Connection) => {
    setChecking((s) => ({ ...s, [c.id]: true }));
    try {
      const result = await request<CheckResult>(
        project,
        `/connections/${encodeURIComponent(c.id)}/check`,
        { method: "POST" },
      );
      setChecks((s) => ({ ...s, [c.id]: result }));
      if (result.ok) refresh();
    } catch (err) {
      setChecks((s) => ({
        ...s,
        [c.id]: {
          ok: false,
          message: err instanceof Error ? err.message : String(err),
          checked_at: new Date().toISOString(),
          latency_ms: 0,
        },
      }));
    } finally {
      setChecking((s) => ({ ...s, [c.id]: false }));
    }
  };
  const deleteConnection = async () => {
    if (!remove) return;
    setRemoving(true);
    setMutationError("");
    try {
      await request(project, `/connections/${encodeURIComponent(remove.id)}`, {
        method: "DELETE",
      });
      setRemove(null);
      refresh();
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
    } finally {
      setRemoving(false);
    }
  };
  const metrics = overview.data;
  return (
    <div className="a2a">
      <style>{styles}</style>
      <header className="header">
        <div className="eyebrow">
          Agent to Agent <span style={{ opacity: 0.5 }}> / </span> Network
          workspace
        </div>
        <div className="row between title-line">
          <div>
            <h1>Your agents, working together.</h1>
            <p className="subtitle">
              Discover capabilities. Follow the work. Manage who connects.
            </p>
          </div>
          <div className="row">
            <button
              className="btn quiet"
              aria-label="Refresh workspace"
              onClick={refresh}
            >
              <RefreshCw size={14} />
            </button>
            <button className="btn primary" onClick={() => setWizard("new")}>
              <Plus size={14} />
              Connect
            </button>
          </div>
        </div>
        <nav
          className="tabs"
          role="tablist"
          aria-label="A2A views"
          onKeyDown={(e) => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key))
              return;
            e.preventDefault();
            const i = tabs.findIndex((t) => t.name === view);
            const n =
              e.key === "Home"
                ? 0
                : e.key === "End"
                  ? tabs.length - 1
                  : (i + (e.key === "ArrowRight" ? 1 : -1) + tabs.length) %
                    tabs.length;
            setView(tabs[n].name);
            document.getElementById(`a2a-tab-${tabs[n].name}`)?.focus();
          }}
        >
          {tabs.map(({ name, icon: Icon }) => (
            <button
              role="tab"
              id={`a2a-tab-${name}`}
              aria-controls={`a2a-view-${name}`}
              aria-selected={view === name}
              tabIndex={view === name ? 0 : -1}
              className="tab"
              key={name}
              onClick={() => setView(name)}
            >
              <Icon size={15} />
              {name}
              {name === "Agents" && network.data && (
                <span className="badge">{agents.length}</span>
              )}
              {name === "Connections" && connections.data && (
                <span className="badge">{cs.length}</span>
              )}
            </button>
          ))}
        </nav>
      </header>
      <main
        className="main stack"
        id={`a2a-view-${view}`}
        role="tabpanel"
        aria-labelledby={`a2a-tab-${view}`}
      >
        {network.error && (
          <ErrorNotice
            message={`Agent directory: ${network.error}`}
            retry={network.reload}
          />
        )}
        {connections.error && (
          <ErrorNotice
            message={`Connections: ${connections.error}`}
            retry={connections.reload}
          />
        )}
        {network.data?.warnings?.map((w) => (
          <div className="notice" key={w}>
            <AlertCircle size={15} />
            {w}
          </div>
        ))}
        {view === "Overview" && (
          <>
            <div className="row between">
              <div>
                <h2>At a glance</h2>
                <p className="subtitle">Project exchanges · all time</p>
              </div>
              <small className="muted">Updated {age(metrics?.as_of)}</small>
            </div>
            {overview.error ? (
              <ErrorNotice message={overview.error} retry={overview.reload} />
            ) : (
              <div className="metrics">
                {[
                  {
                    label: "Active exchanges",
                    value: metrics?.active,
                    note: "Submitted or working",
                    icon: Activity,
                    status: "active",
                  },
                  {
                    label: "Waiting for input",
                    value: metrics?.input_required,
                    note: "An agent has a question",
                    icon: MessageSquare,
                    status: "input_required",
                  },
                  {
                    label: "Needs attention",
                    value: metrics?.attention,
                    note: "Failures, input, or delivery retries",
                    icon: AlertCircle,
                    status: "attention",
                  },
                  {
                    label: "Completed",
                    value: metrics?.completed,
                    note: `${metrics?.total ?? "—"} exchanges in total`,
                    icon: CheckCircle2,
                    status: "completed",
                  },
                ].map((m) => (
                  <button
                    className="panel metric"
                    key={m.label}
                    onClick={() => openExchanges({ status: m.status })}
                  >
                    <div className="row">
                      <span>{m.label}</span>
                      <m.icon size={16} />
                    </div>
                    <div className="metric-value">{m.value ?? "—"}</div>
                    <small>{m.note}</small>
                  </button>
                ))}
              </div>
            )}
            <div className="overview-grid">
              <section className="panel">
                <div className="section-head row between">
                  <h2>Recent exchanges</h2>
                  <button className="btn quiet" onClick={() => openExchanges()}>
                    View all <ArrowRight size={13} />
                  </button>
                </div>
                {recent.error ? (
                  <div className="pad">
                    <ErrorNotice message={recent.error} retry={recent.reload} />
                  </div>
                ) : recent.loading && !recent.data ? (
                  <Loading />
                ) : recent.data?.tasks.length ? (
                  recent.data.tasks.map((t) => (
                    <TaskRow
                      key={t.id}
                      task={t}
                      connections={cs}
                      onClick={() => openExchanges({}, t)}
                    />
                  ))
                ) : (
                  <Empty title="The first exchange starts with an agent">
                    Attach A2A to two agents and ask one to collaborate with the
                    other. Their work will appear here.
                  </Empty>
                )}
              </section>
              <div className="stack">
                <section className="panel">
                  <div className="section-head row between">
                    <h2>Needs attention</h2>
                    <AlertCircle size={16} className="muted" />
                  </div>
                  {attention.error ? (
                    <div className="pad">
                      <ErrorNotice
                        message={attention.error}
                        retry={attention.reload}
                      />
                    </div>
                  ) : attention.loading && !attention.data ? (
                    <Loading />
                  ) : attention.data?.tasks.length ? (
                    attention.data.tasks.map((t) => (
                      <TaskRow
                        key={t.id}
                        task={t}
                        connections={cs}
                        onClick={() =>
                          openExchanges({ status: "attention" }, t)
                        }
                      />
                    ))
                  ) : (
                    <Empty title="Nothing needs attention">
                      No failed exchanges, unanswered input requests, or pending
                      delivery retries.
                    </Empty>
                  )}
                </section>
                <section className="panel pad stack">
                  <div className="row between">
                    <h3>Connected network</h3>
                    <Network size={17} className="muted" />
                  </div>
                  <div className="row wrap">
                    <span className="badge">
                      {network.data
                        ? agents.filter((a) => a.kind === "local").length
                        : "—"}{" "}
                      local agents
                    </span>
                    <span className="badge">
                      {network.data
                        ? agents.filter((a) => a.kind !== "local").length
                        : "—"}{" "}
                      cached remote agents
                    </span>
                  </div>
                  <p className="muted">
                    {connections.data ? cs.length : "—"} external connections ·{" "}
                    {network.data?.node?.display_name || "This installation"}
                  </p>
                  <button
                    className="btn"
                    onClick={() => {
                      setView("Agents");
                      setAgentView("map");
                    }}
                  >
                    Explore network <ArrowRight size={13} />
                  </button>
                </section>
              </div>
            </div>
          </>
        )}
        {view === "Agents" && (
          <>
            <div className="row between wrap">
              <div>
                <h2>Agent directory</h2>
                <p className="subtitle">
                  Local agents in this project and remote agents discovered by
                  this installation.
                </p>
              </div>
              <div className="segmented">
                <button
                  aria-pressed={agentView === "directory"}
                  onClick={() => setAgentView("directory")}
                >
                  <LayoutGrid size={14} />
                  Directory
                </button>
                <button
                  aria-pressed={agentView === "map"}
                  onClick={() => setAgentView("map")}
                >
                  <Network size={14} />
                  Map
                </button>
              </div>
            </div>
            {agentView === "map" && (
              <section className="panel">
                <div className="map">
                  <div className="map-hub">
                    <Server size={28} style={{ margin: "0 auto 12px" }} />
                    <h3>
                      {network.data?.node?.display_name || "This installation"}
                    </h3>
                    <p className="subtitle">
                      {agents.filter((a) => a.kind === "local").length} local
                      agents in this project
                    </p>
                    <button
                      className="btn quiet"
                      style={{ marginTop: 14 }}
                      onClick={() => {
                        setAgentPeer("local");
                        setAgentView("directory");
                      }}
                    >
                      View agents
                    </button>
                  </div>
                  <div className="map-lines" />
                  <div className="map-peers">
                    {cs.length ? (
                      cs.map((c) => (
                        <div className="map-peer" key={c.id}>
                          <button
                            className="panel"
                            onClick={() => {
                              setAgentPeer(c.id);
                              setAgentView("directory");
                              setAgentQuery("");
                            }}
                          >
                            {c.kind === "node" ? (
                              <Server size={18} />
                            ) : (
                              <Globe2 size={18} />
                            )}
                            <span className="grow">
                              <strong>{c.name}</strong>
                              <p className="subtitle">
                                {c.agents?.length || 0} cached agents ·{" "}
                                {c.kind === "node"
                                  ? "Installation"
                                  : "Public agent"}
                              </p>
                            </span>
                            <ChevronRight size={16} />
                          </button>
                        </div>
                      ))
                    ) : (
                      <Empty title="Extend your network">
                        Connect another installation or a public agent.
                        <button
                          className="btn"
                          style={{ marginTop: 12 }}
                          onClick={() => setWizard("new")}
                        >
                          Add connection
                        </button>
                      </Empty>
                    )}
                  </div>
                </div>
                <p
                  className="pad muted"
                  style={{
                    fontSize: 12,
                    borderTop: "1px solid var(--a-border)",
                  }}
                >
                  Lines show configured connections. Select a node to see its
                  agents and open their exchanges. Availability is verified from
                  Connections.
                </p>
              </section>
            )}
            {agentView === "directory" && (
              <>
                <div className="toolbar">
                  <label className="search">
                    <Search size={15} />
                    <input
                      aria-label="Search agents"
                      className="input"
                      placeholder="Search names, capabilities, or installations…"
                      value={agentQuery}
                      onChange={(e) => setAgentQuery(e.target.value)}
                    />
                  </label>
                  <select
                    className="input"
                    aria-label="Agent installation"
                    value={agentPeer}
                    onChange={(e) => setAgentPeer(e.target.value)}
                  >
                    <option value="">All installations</option>
                    <option value="local">This installation</option>
                    {cs.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                  <small className="muted">
                    {filteredAgents.length} agents
                  </small>
                </div>
                {network.loading && !network.data ? (
                  <Loading />
                ) : filteredAgents.length ? (
                  <div className="agent-grid">
                    {filteredAgents.map((agent) => (
                      <button
                        key={agent.address}
                        className="panel agent-card"
                        onClick={() => setSelectedAgent(agent)}
                      >
                        <div className="row">
                          <div className="avatar">
                            {agent.kind === "local" ? (
                              <Bot size={20} />
                            ) : (
                              <Globe2 size={20} />
                            )}
                          </div>
                          <div className="grow">
                            <h3 className="truncate">{agent.name}</h3>
                            <small className="muted">{agent.peer_name}</small>
                          </div>
                          <ChevronRight size={14} className="muted" />
                        </div>
                        <p>
                          {agent.description ||
                            "Open this agent to inspect its published capabilities."}
                        </p>
                        <div className="chips">
                          {agent.skills?.slice(0, 3).map((s) => (
                            <span className="badge" key={s}>
                              {s}
                            </span>
                          ))}
                          {(agent.skills?.length || 0) > 3 && (
                            <span className="badge">
                              +{agent.skills!.length - 3}
                            </span>
                          )}
                        </div>
                        <div
                          className="row between"
                          style={{ marginTop: "auto" }}
                        >
                          <Badge status={agent.status || "unknown"} />
                          <small className="muted">
                            {agent.kind === "local"
                              ? "This project"
                              : `Cached ${age(agent.fetched_at).toLowerCase()}`}
                          </small>
                        </div>
                      </button>
                    ))}
                  </div>
                ) : (
                  !network.error && (
                    <Empty
                      title={
                        agentQuery || agentPeer
                          ? "No matching agents"
                          : "No agents discovered yet"
                      }
                    >
                      Attach A2A to local agents, or check a connection to
                      discover remote agents.
                      <button
                        className="btn"
                        style={{ marginTop: 12 }}
                        onClick={() => setView("Connections")}
                      >
                        Manage connections
                      </button>
                    </Empty>
                  )
                )}
              </>
            )}
          </>
        )}
        {view === "Exchanges" && (
          <>
            <div>
              <h2>Follow the work</h2>
              <p className="subtitle">
                Search the full exchange history, inspect replies, and open the
                originating threads.
              </p>
            </div>
            <div className="toolbar">
              <label className="search">
                <Search size={15} />
                <input
                  aria-label="Search exchanges"
                  className="input"
                  placeholder="Search agents or message content…"
                  value={filters.q}
                  onChange={(e) => filter("q", e.target.value)}
                />
              </label>
              <select
                aria-label="Exchange status"
                className="input"
                value={filters.status}
                onChange={(e) => filter("status", e.target.value)}
              >
                {[
                  ["", "All statuses"],
                  ["open", "Open"],
                  ["active", "Active"],
                  ["attention", "Needs attention"],
                  ["working", "Working"],
                  ["submitted", "Submitted"],
                  ["input_required", "Waiting for input"],
                  ["completed", "Completed"],
                  ["failed", "Failed"],
                  ["canceled", "Canceled"],
                ].map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>
              <select
                aria-label="Exchange connection"
                className="input"
                value={filters.peer}
                onChange={(e) => filter("peer", e.target.value)}
              >
                <option value="">All connections</option>
                <option value="local">Local only</option>
                {cs.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="toolbar">
              <select
                aria-label="Exchange agent"
                className="input"
                value={filters.agent_address}
                onChange={(e) => filter("agent_address", e.target.value)}
              >
                <option value="">All agents</option>
                {agents.map((a) => (
                  <option key={a.address} value={a.address}>
                    {a.name}
                  </option>
                ))}
              </select>
              <label>
                From (UTC)
                <input
                  className="input"
                  type="date"
                  value={filters.from}
                  onChange={(e) => filter("from", e.target.value)}
                />
              </label>
              <label>
                To (UTC)
                <input
                  className="input"
                  type="date"
                  value={filters.to}
                  onChange={(e) => filter("to", e.target.value)}
                />
              </label>
              {Object.values(filters).some(Boolean) && (
                <button className="btn quiet" onClick={() => openExchanges()}>
                  Clear filters <X size={12} />
                </button>
              )}
            </div>
            {exchanges.error && (
              <ErrorNotice message={exchanges.error} retry={exchanges.reload} />
            )}
            <div className="exchange-grid">
              <section className="panel">
                <div className="section-head row between">
                  <h3>
                    Exchanges{" "}
                    <span className="muted">
                      {exchanges.data?.total ?? "—"}
                    </span>
                  </h3>
                  <span className="eyebrow">Latest first</span>
                </div>
                <div className="exchange-list">
                  {exchanges.loading && !exchanges.data ? (
                    <Loading />
                  ) : exchanges.data?.tasks.length ? (
                    exchanges.data.tasks.map((t) => (
                      <TaskRow
                        key={t.id}
                        task={t}
                        connections={cs}
                        selected={activeTask?.id === t.id}
                        onClick={() => setSelectedTask(t)}
                      />
                    ))
                  ) : (
                    !exchanges.error && (
                      <Empty title="No exchanges found">
                        Try changing the filters, or start a collaboration
                        between agents.
                      </Empty>
                    )
                  )}
                </div>
                <div className="pagination">
                  <span>
                    {exchanges.data?.total
                      ? `${offset + 1}–${offset + exchanges.data.tasks.length} of ${exchanges.data.total}`
                      : "0 exchanges"}
                  </span>
                  <div className="row">
                    <button
                      className="btn quiet"
                      aria-label="Previous page"
                      disabled={!offset || exchanges.loading}
                      onClick={() => {
                        setOffset((o) => Math.max(0, o - 30));
                        setSelectedTask(null);
                      }}
                    >
                      <ChevronLeft size={14} />
                    </button>
                    <button
                      className="btn quiet"
                      aria-label="Next page"
                      disabled={
                        !exchanges.data ||
                        offset + 30 >= exchanges.data.total ||
                        exchanges.loading
                      }
                      onClick={() => {
                        setOffset((o) => o + 30);
                        setSelectedTask(null);
                      }}
                    >
                      <ChevronRight size={14} />
                    </button>
                  </div>
                </div>
              </section>
              {activeTask ? (
                <ExchangeDetail
                  key={activeTask.id}
                  task={activeTask}
                  project={project}
                  revision={revision}
                  connections={cs}
                />
              ) : (
                <section className="panel">
                  <Empty title="Select an exchange">
                    Read the request and replies, inspect returned artifacts,
                    and trace the work back to each agent’s thread.
                  </Empty>
                </section>
              )}
            </div>
          </>
        )}
        {view === "Connections" && (
          <>
            <div className="row between wrap">
              <div>
                <h2>Connected installations & public agents</h2>
                <p className="subtitle">
                  Shared across this A2A installation. Checks verify discovery
                  access without sending tasks.
                </p>
              </div>
              <button className="btn" onClick={() => setWizard("new")}>
                <Plus size={14} />
                Add connection
              </button>
            </div>
            {connections.loading && !connections.data ? (
              <Loading />
            ) : cs.length ? (
              <div className="connection-grid">
                {cs.map((c) => (
                  <article className="panel connection" key={c.id}>
                    <div className="row">
                      <div className="avatar">
                        {c.kind === "node" ? (
                          <Server size={20} />
                        ) : (
                          <Globe2 size={20} />
                        )}
                      </div>
                      <div className="grow">
                        <h3>{c.name}</h3>
                        <small className="muted">
                          {c.kind === "node"
                            ? "Apteva installation"
                            : "Public agent"}
                        </small>
                      </div>
                      <span
                        className={`badge ${checks[c.id] ? (checks[c.id].ok ? "good" : "bad") : ""}`}
                      >
                        {checks[c.id]
                          ? checks[c.id].ok
                            ? "Discovery verified"
                            : "Check failed"
                          : "Not checked"}
                      </span>
                    </div>
                    <p className="muted break" style={{ fontSize: 12 }}>
                      {c.card_url || c.base_url}
                    </p>
                    <dl className="kv">
                      <dt>Authentication</dt>
                      <dd>
                        {c.authenticated
                          ? "Bearer token configured"
                          : "Anonymous"}
                      </dd>
                      <dt>Managed by</dt>
                      <dd>
                        {c.managed_by === "operator"
                          ? "You"
                          : c.managed_by === "app"
                            ? "Another app"
                            : c.managed_by === "config"
                              ? "Installation configuration"
                              : "Agent discovery"}
                      </dd>
                      {c.protocol_version && (
                        <>
                          <dt>Protocol</dt>
                          <dd>A2A {c.protocol_version}</dd>
                        </>
                      )}
                      {c.kind === "node" && (
                        <>
                          <dt>May discover</dt>
                          <dd>
                            {c.discover_agents?.join(", ") ||
                              "No inbound access"}
                          </dd>
                          <dt>May invoke</dt>
                          <dd>{grantNames(c.invoke_agents, agents)}</dd>
                        </>
                      )}
                    </dl>
                    <div className="stack gap-sm">
                      <div className="row between">
                        <small className="muted">Discovered agents</small>
                        <button
                          className="icon-btn"
                          aria-label={`View agents on ${c.name}`}
                          onClick={() => {
                            setAgentPeer(c.id);
                            setAgentQuery("");
                            setAgentView("directory");
                            setView("Agents");
                          }}
                        >
                          <ArrowUpRight size={14} />
                        </button>
                      </div>
                      <div className="chips">
                        {c.agents?.length ? (
                          c.agents.map((name) => (
                            <span className="badge" key={name}>
                              {name}
                            </span>
                          ))
                        ) : (
                          <small className="muted">
                            Run a check to refresh this directory.
                          </small>
                        )}
                      </div>
                    </div>
                    {checks[c.id] && (
                      <div className="notice" role="status">
                        <span>
                          {checks[c.id].message}
                          <br />
                          <small>
                            {time(checks[c.id].checked_at)} ·{" "}
                            {checks[c.id].latency_ms} ms
                          </small>
                        </span>
                      </div>
                    )}
                    <div className="connection-footer">
                      <button
                        className="btn"
                        disabled={checking[c.id]}
                        onClick={() => checkConnection(c)}
                      >
                        {checking[c.id] ? (
                          <Loader2 size={13} className="spin" />
                        ) : (
                          <RefreshCw size={13} />
                        )}
                        Check connection
                      </button>
                      <button
                        className="btn quiet"
                        onClick={() => openExchanges({ peer: c.id })}
                      >
                        Exchanges
                      </button>
                      {["operator", "agent"].includes(c.managed_by) && (
                        <>
                          {c.kind === "node" && (
                            <button
                              className="btn quiet"
                              onClick={() => setWizard(c)}
                            >
                              <Shield size={13} />
                              Edit access
                            </button>
                          )}
                          <button
                            className="icon-btn danger"
                            aria-label={`Remove ${c.name}`}
                            onClick={() => {
                              setMutationError("");
                              setRemove(c);
                            }}
                          >
                            <Unplug size={15} />
                          </button>
                        </>
                      )}
                    </div>
                  </article>
                ))}
              </div>
            ) : (
              !connections.error && (
                <section className="panel">
                  <Empty title="Bring another agent into the conversation">
                    Connect an Apteva installation or paste a public Agent Card
                    URL. Your agents can then discover its capabilities.
                    <button
                      className="btn primary"
                      style={{ marginTop: 16 }}
                      onClick={() => setWizard("new")}
                    >
                      <Plus size={14} />
                      Add your first connection
                    </button>
                  </Empty>
                </section>
              )
            )}
          </>
        )}
      </main>
      {selectedAgent && (
        <AgentDetail
          key={selectedAgent.address}
          project={project}
          agent={selectedAgent}
          onClose={() => setSelectedAgent(null)}
          onExchanges={(a) => {
            setSelectedAgent(null);
            openExchanges(
              a.id
                ? { agent_address: a.address }
                : { peer: a.peer_id, agent_address: a.address },
            );
          }}
        />
      )}
      {wizard && (
        <ConnectionWizard
          project={project}
          agents={agents}
          edit={wizard === "new" ? undefined : wizard}
          onClose={() => setWizard(false)}
          onSaved={() => {
            setWizard(false);
            setChecks({});
            setView("Connections");
            refresh();
          }}
        />
      )}
      {remove && (
        <Sheet
          title={`Remove ${remove.name}?`}
          onClose={() => {
            if (!removing) setRemove(null);
          }}
        >
          <p>
            This removes the connection from the whole A2A installation.
            Existing exchange history stays available; active remote exchanges
            may no longer synchronize.
          </p>
          {mutationError && <ErrorNotice message={mutationError} />}
          <div className="row">
            <button
              disabled={removing}
              className="btn"
              onClick={() => setRemove(null)}
            >
              Keep connection
            </button>
            <button
              disabled={removing}
              className="btn danger"
              onClick={deleteConnection}
            >
              Remove connection
            </button>
          </div>
        </Sheet>
      )}
    </div>
  );
}
