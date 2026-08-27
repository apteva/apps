import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ComponentType,
} from "react";

interface HostProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
  eventRevision?: number;
}

interface Helper {
  id: number;
  name: string;
  status: string;
}

interface Goal {
  id: string;
  title: string;
  objective: string;
  status: string;
  current_phase: string;
  summary: string;
  next_action: string;
  success_criteria: string[];
  updated_at: string;
}

interface Step {
  id: string;
  position: number;
  title: string;
  detail: string;
  status: string;
  approval_state: string;
  blocking_reason?: string;
}

interface GoalCheck {
  id: string;
  name: string;
  description: string;
  status: string;
  result: string;
}

interface Resource {
  id: string;
  kind: string;
  name: string;
  status: string;
  note?: string;
}

interface BuilderEvent {
  id: string;
  kind: string;
  title: string;
  detail: string;
  created_at: string;
}

interface GoalBundle {
  goal: Goal;
  steps: Step[];
  checks: GoalCheck[];
  resources: Resource[];
  events: BuilderEvent[];
  completion: {
    ready: boolean;
    incomplete_steps: number;
    unsatisfied_checks: number;
    issues: string[];
  };
}

type ConversationsWidget = ComponentType<{
  appName: string;
  installId: number;
  projectId: string;
  instanceId: number;
  eventRevision?: number;
}>;

type TrackerTab = "plan" | "checks" | "resources" | "activity";

const statusTone: Record<string, string> = {
  active: "border-accent/30 bg-accent/10 text-accent",
  planning: "border-info/30 bg-info/10 text-info",
  waiting_approval: "border-warn/30 bg-warn/10 text-warn",
  blocked: "border-error/30 bg-error/10 text-error",
  failed: "border-error/30 bg-error/10 text-error",
  completed: "border-green/30 bg-green/10 text-green",
  passing: "border-green/30 bg-green/10 text-green",
  ready: "border-green/30 bg-green/10 text-green",
  drifted: "border-warn/30 bg-warn/10 text-warn",
  needs_attention: "border-warn/30 bg-warn/10 text-warn",
};

function apiPath(props: HostProps, path: string, helperID: number): string {
  const query = new URLSearchParams({
    install_id: String(props.installId),
    project_id: props.projectId,
    owner_agent_id: String(helperID),
  });
  return `/api/apps/${encodeURIComponent(props.appName || "builder")}${path}?${query}`;
}

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "same-origin" });
  if (!response.ok) {
    throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

function pretty(value: string): string {
  return value.replaceAll("_", " ");
}

function StatusPill({ value }: { value: string }) {
  return (
    <span className={`rounded border px-1.5 py-0.5 text-[9px] font-bold uppercase tracking-wide ${statusTone[value] || "border-border bg-bg-input text-text-muted"}`}>
      {pretty(value)}
    </span>
  );
}

function relativeTime(value: string): string {
  const elapsed = Date.now() - new Date(value).getTime();
  if (!Number.isFinite(elapsed) || elapsed < 60_000) return "now";
  const minutes = Math.floor(elapsed / 60_000);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`;
}

function Tracker({
  goals,
  selectedID,
  bundle,
  loading,
  error,
  onSelect,
  onRefresh,
}: {
  goals: Goal[];
  selectedID: string;
  bundle: GoalBundle | null;
  loading: boolean;
  error: string;
  onSelect: (id: string) => void;
  onRefresh: () => void;
}) {
  const [tab, setTab] = useState<TrackerTab>("plan");
  const goal = bundle?.goal;
  const completedSteps = bundle?.steps.filter((step) => ["completed", "skipped"].includes(step.status)).length ?? 0;
  const totalSteps = bundle?.steps.length ?? 0;
  const progress = totalSteps ? Math.round((completedSteps / totalSteps) * 100) : 0;

  return (
    <section className="flex h-full min-h-0 flex-col bg-bg-card">
      <header className="shrink-0 border-b border-border p-4">
        <div className="flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-bold text-text">Project tracker</h2>
            <p className="mt-0.5 text-[10px] text-text-dim">Durable state maintained by Helper</p>
          </div>
          <button type="button" onClick={onRefresh} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover hover:text-text">
            Refresh
          </button>
        </div>
        {goals.length > 1 && (
          <select
            value={selectedID}
            onChange={(event) => onSelect(event.target.value)}
            className="mt-3 w-full rounded border border-border bg-bg-input px-2.5 py-2 text-xs text-text"
          >
            {goals.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}
          </select>
        )}
      </header>

      {error ? (
        <div className="m-4 rounded border border-error/30 bg-error/10 p-3 text-xs text-error">{error}</div>
      ) : loading && !goal ? (
        <div className="grid min-h-64 flex-1 place-items-center p-6 text-xs text-text-dim">Loading Builder state…</div>
      ) : !goal ? (
        <div className="grid min-h-64 flex-1 place-items-center p-6 text-center">
          <div className="max-w-xs">
            <div className="mx-auto grid h-10 w-10 place-items-center rounded-full border border-accent/30 bg-accent/10 text-lg text-accent">✦</div>
            <h3 className="mt-3 text-sm font-bold text-text">Describe the outcome</h3>
            <p className="mt-1 text-xs leading-5 text-text-muted">
              Tell Helper what you want in the conversation. Its goal, plan, checks, resources, and decisions will appear here automatically.
            </p>
          </div>
        </div>
      ) : (
        <>
          <div className="shrink-0 border-b border-border p-4">
            <div className="flex items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="text-sm font-bold text-text">{goal.title}</h3>
                  <StatusPill value={goal.status} />
                </div>
                <p className="mt-1 line-clamp-3 text-xs leading-5 text-text-muted">{goal.objective}</p>
              </div>
              <span className="shrink-0 text-[9px] text-text-dim">{relativeTime(goal.updated_at)}</span>
            </div>
            <div className="mt-3">
              <div className="mb-1 flex items-center justify-between text-[9px] text-text-dim">
                <span>{goal.current_phase || "Planning"}</span>
                <span>{completedSteps}/{totalSteps || 0} steps</span>
              </div>
              <div className="h-1.5 overflow-hidden rounded-full bg-bg-input">
                <div className="h-full rounded-full bg-accent transition-all" style={{ width: `${progress}%` }} />
              </div>
            </div>
            {goal.next_action && (
              <div className="mt-3 rounded border border-border bg-bg-input/60 p-2.5">
                <p className="text-[9px] font-bold uppercase tracking-wide text-text-dim">Next action</p>
                <p className="mt-1 text-xs text-text">{goal.next_action}</p>
              </div>
            )}
          </div>

          <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-border p-2">
            {(["plan", "checks", "resources", "activity"] as TrackerTab[]).map((item) => (
              <button
                key={item}
                type="button"
                onClick={() => setTab(item)}
                className={`rounded px-2.5 py-1.5 text-[10px] font-bold capitalize ${tab === item ? "bg-accent/15 text-accent" : "text-text-dim hover:bg-bg-hover hover:text-text"}`}
              >
                {item}
                {item === "plan" && ` ${bundle.steps.length}`}
                {item === "checks" && ` ${bundle.checks.length}`}
                {item === "resources" && ` ${bundle.resources.length}`}
              </button>
            ))}
          </nav>

          <div className="min-h-0 flex-1 overflow-auto p-3">
            {tab === "plan" && (
              <div className="space-y-2">
                {bundle.steps.map((step) => (
                  <article key={step.id} className="rounded border border-border bg-bg p-3">
                    <div className="flex items-start gap-2">
                      <span className="grid h-5 w-5 shrink-0 place-items-center rounded-full border border-border text-[9px] font-bold text-text-dim">{step.position}</span>
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <p className="text-xs font-semibold text-text">{step.title}</p>
                          <StatusPill value={step.status} />
                        </div>
                        {step.detail && <p className="mt-1 text-[10px] leading-4 text-text-muted">{step.detail}</p>}
                        {step.blocking_reason && <p className="mt-1 text-[10px] text-error">{step.blocking_reason}</p>}
                        {step.approval_state !== "none" && <p className="mt-1 text-[9px] uppercase tracking-wide text-warn">Approval: {pretty(step.approval_state)}</p>}
                      </div>
                    </div>
                  </article>
                ))}
                {!bundle.steps.length && <EmptyLine text="Helper has not set the execution plan yet." />}
              </div>
            )}
            {tab === "checks" && (
              <div className="space-y-2">
                {bundle.checks.map((check) => (
                  <article key={check.id} className="rounded border border-border bg-bg p-3">
                    <div className="flex items-center gap-2">
                      <p className="min-w-0 flex-1 text-xs font-semibold text-text">{check.name}</p>
                      <StatusPill value={check.status} />
                    </div>
                    {(check.result || check.description) && <p className="mt-1 text-[10px] leading-4 text-text-muted">{check.result || check.description}</p>}
                  </article>
                ))}
                {!bundle.checks.length && <EmptyLine text="Success checks will appear when Helper sets the plan." />}
              </div>
            )}
            {tab === "resources" && (
              <div className="space-y-2">
                {bundle.resources.map((resource) => (
                  <article key={resource.id} className="rounded border border-border bg-bg p-3">
                    <div className="flex items-center gap-2">
                      <span className="rounded bg-bg-input px-1.5 py-0.5 text-[9px] uppercase text-text-dim">{pretty(resource.kind)}</span>
                      <p className="min-w-0 flex-1 truncate text-xs font-semibold text-text">{resource.name}</p>
                      <StatusPill value={resource.status} />
                    </div>
                    {resource.note && <p className="mt-1 text-[10px] leading-4 text-text-muted">{resource.note}</p>}
                  </article>
                ))}
                {!bundle.resources.length && <EmptyLine text="Agents, apps, connections, and credentials will be tracked here." />}
              </div>
            )}
            {tab === "activity" && (
              <div className="space-y-3">
                {bundle.events.map((event) => (
                  <article key={event.id} className="border-l border-border pl-3">
                    <div className="flex items-center gap-2">
                      <p className="min-w-0 flex-1 text-xs font-semibold text-text">{event.title}</p>
                      <span className="text-[9px] text-text-dim">{relativeTime(event.created_at)}</span>
                    </div>
                    {event.detail && <p className="mt-1 text-[10px] leading-4 text-text-muted">{event.detail}</p>}
                    <p className="mt-1 text-[9px] uppercase tracking-wide text-text-dim">{pretty(event.kind)}</p>
                  </article>
                ))}
                {!bundle.events.length && <EmptyLine text="Meaningful progress and decisions will appear here." />}
              </div>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function EmptyLine({ text }: { text: string }) {
  return <p className="rounded border border-dashed border-border p-5 text-center text-[10px] text-text-dim">{text}</p>;
}

export default function BuilderPanel(props: HostProps) {
  const [helper, setHelper] = useState<Helper | null>(null);
  const [conversation, setConversation] = useState<ConversationsWidget | null>(null);
  const [conversationError, setConversationError] = useState("");
  const [goals, setGoals] = useState<Goal[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [bundle, setBundle] = useState<GoalBundle | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [wide, setWide] = useState(() => typeof window !== "undefined" && window.matchMedia("(min-width: 1024px)").matches);

  useEffect(() => {
    const media = window.matchMedia("(min-width: 1024px)");
    const update = () => setWide(media.matches);
    media.addEventListener("change", update);
    update();
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    let cancelled = false;
    if (props.instanceId && props.instanceId > 0) {
      setHelper({ id: props.instanceId, name: "Apteva Helper", status: "running" });
      return () => { cancelled = true; };
    }
    getJSON<Helper>("/api/platform/helper")
      .then((row) => { if (!cancelled) setHelper(row); })
      .catch((reason) => { if (!cancelled) setError(reason instanceof Error ? reason.message : String(reason)); });
    return () => { cancelled = true; };
  }, [props.instanceId]);

  useEffect(() => {
    let cancelled = false;
    const url = "/api/apps/conversations/ui/AgentConversationsWidget.mjs";
    import(/* @vite-ignore */ url)
      .then((module) => {
        if (cancelled) return;
        const Component = (module.default || module.AgentConversationsWidget) as ConversationsWidget | undefined;
        if (!Component) throw new Error("Conversations did not export its agent workspace");
        setConversation(() => Component);
      })
      .catch((reason) => {
        if (!cancelled) setConversationError(reason instanceof Error ? reason.message : String(reason));
      });
    return () => { cancelled = true; };
  }, []);

  const load = useCallback(async () => {
    if (!props.projectId || !helper?.id) return;
    setLoading(true);
    try {
      const list = await getJSON<{ goals: Goal[] }>(apiPath(props, "/goals", helper.id));
      setGoals(list.goals || []);
      const goalID = selectedID && list.goals.some((goal) => goal.id === selectedID)
        ? selectedID
        : (list.goals[0]?.id || "");
      setSelectedID(goalID);
      setBundle(goalID ? await getJSON<GoalBundle>(apiPath(props, `/goals/${encodeURIComponent(goalID)}`, helper.id)) : null);
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setLoading(false);
    }
  }, [helper?.id, props.appName, props.installId, props.projectId, selectedID]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 8_000);
    return () => window.clearInterval(timer);
  }, [load, props.eventRevision]);

  const layout = useMemo(() => wide
    ? { gridTemplateColumns: "minmax(0, 1.45fr) minmax(320px, 0.8fr)", gridTemplateRows: "minmax(0, 1fr)" }
    : { gridTemplateColumns: "minmax(0, 1fr)", gridTemplateRows: "minmax(420px, 1fr) minmax(420px, 1fr)" }, [wide]);

  const Conversation = conversation;
  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-bg text-text">
      <header className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-3">
        <div className="grid h-8 w-8 place-items-center rounded border border-accent/30 bg-accent/10 text-accent">✦</div>
        <div className="min-w-0 flex-1">
          <h1 className="text-sm font-bold text-text">Builder</h1>
          <p className="truncate text-[10px] text-text-dim">Set the outcome. Helper plans, builds, verifies, and asks before consequential actions.</p>
        </div>
        {helper && (
          <span className="inline-flex items-center gap-1.5 rounded border border-border px-2 py-1 text-[9px] font-bold text-text-muted">
            <span className={`h-1.5 w-1.5 rounded-full ${helper.status === "running" ? "bg-green" : "bg-text-dim"}`} />
            {helper.name}
          </span>
        )}
      </header>
      <main className="grid min-h-0 flex-1 overflow-auto" style={layout}>
        <section className="min-h-0 overflow-hidden border-border" style={wide ? { borderRightWidth: 1 } : { borderBottomWidth: 1 }}>
          {Conversation && helper ? (
            <Conversation
              appName="conversations"
              installId={0}
              projectId={props.projectId}
              instanceId={helper.id}
              eventRevision={props.eventRevision}
            />
          ) : conversationError ? (
            <div className="grid h-full place-items-center p-6 text-center">
              <div>
                <p className="text-sm font-bold text-text">Conversations is unavailable</p>
                <p className="mt-1 max-w-sm text-xs text-text-muted">{conversationError}</p>
              </div>
            </div>
          ) : (
            <div className="grid h-full place-items-center p-6 text-xs text-text-dim">Opening Helper conversation…</div>
          )}
        </section>
        <Tracker
          goals={goals}
          selectedID={selectedID}
          bundle={bundle}
          loading={loading}
          error={error}
          onSelect={setSelectedID}
          onRefresh={() => void load()}
        />
      </main>
    </div>
  );
}
