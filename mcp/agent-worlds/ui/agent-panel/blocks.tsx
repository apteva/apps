import { clock, eventColor, short, statusColor, type Palette } from "../palette";
import { describeTool } from "../pixel/iso/stations";
import { contentsForAgents, type Agent, type Destination, type Event } from "../world-model";

// What the built-in blocks read. Every block filters the same live (or
// replayed) event list, so the panel and the view always agree.
export type BlockContext = {
  agents: Agent[]; destinations: Destination[]; events: Event[]; projectId?: string; projectName?: string;
  palette: Palette; onSelectAgent: (id: string) => void; onSelectPlace: (id: string) => void;
};

const nameOf = (context: BlockContext, id: string) => context.agents.find(agent => agent.id === id)?.name || id;
const placeOf = (context: BlockContext, id?: string) => context.destinations.find(item => item.id === id);
const Empty = ({ children }: { children: string }) => <div className="aw-block-note">{children}</div>;

function EventRow({ event, context, showAgent }: { event: Event; context: BlockContext; showAgent?: boolean }) {
  return <div className="aw-event"><span className="aw-event-mark" style={{ background: eventColor(event.kind, context.palette) }} /><div>
    <div className="aw-event-line">{showAgent && <b>{nameOf(context, event.agent_id)} </b>}{event.label}</div>
    <div className="aw-event-meta">{clock(event.time)}{event.thread_id ? ` · ${short(event.thread_id, 18)}` : ""}</div>
  </div></div>;
}

export function currentAction(agent: Agent, context: BlockContext) {
  const last = [...context.events].reverse().find(event => event.agent_id === agent.id && event.kind === "tool" && event.target);
  if (!last) return null;
  const place = placeOf(context, last.target_id);
  return { text: describeTool(last.target!, place?.name || ""), place, time: last.time };
}

export function AgentBlock({ component, agent, context }: { component: string; agent: Agent; context: BlockContext }) {
  const mine = context.events.filter(event => event.agent_id === agent.id);
  if (component === "worlds:now") {
    const action = currentAction(agent, context);
    return <dl className="aw-facts">
      <dt>Status</dt><dd><span className="aw-dot" style={{ color: statusColor(agent.status, context.palette) }} />{agent.status}</dd>
      <dt>Doing</dt><dd>{action ? <>{action.text}{action.place && <> in <button className="aw-link" onClick={() => context.onSelectPlace(action.place!.id)}>{action.place.name}</button></>} <small>{clock(action.time)}</small></> : "Nothing recent"}</dd>
      {agent.model && <><dt>Model</dt><dd>{agent.model}</dd></>}
      {context.projectName && <><dt>Project</dt><dd>{context.projectName}</dd></>}
    </dl>;
  }
  if (component === "worlds:activity") return mine.length ? <div className="aw-activity-list">{mine.slice(-12).reverse().map(event => <EventRow key={event.id} event={event} context={context} />)}</div> : <Empty>Activity appears as the agent works.</Empty>;
  if (component === "worlds:threads") return agent.threads?.length
    ? <div>{agent.threads.map(thread => <div className="aw-thread-row" key={thread.id}><strong>{thread.id}</strong><span>{thread.status || "Observed"}{thread.parent_id ? ` · parent ${thread.parent_id}` : ""}</span></div>)}</div>
    : <Empty>No thread inventory available.</Empty>;
  if (component === "worlds:apps") {
    const used = contentsForAgents([agent], context.destinations, context.events, context.projectId).destinations;
    return used.length ? <div className="aw-destination-list">{used.map(item => {
      const calls = mine.filter(event => event.kind === "tool" && event.target_id === item.id).length;
      return <button key={item.id} className="aw-destination-row" onClick={() => context.onSelectPlace(item.id)}><span className={`aw-destination-icon is-${item.kind}`}>{item.kind === "app" ? "▣" : item.kind === "integration" ? "⌁" : "◇"}</span><span><strong>{item.name}</strong><small>{item.kind} · {item.status}</small></span><em>{calls}</em></button>;
    })}</div> : <Empty>No apps or integrations yet.</Empty>;
  }
  if (component === "worlds:handoffs") {
    const handoffs = context.events.filter(event => event.target_agent_id && (event.agent_id === agent.id || event.target_agent_id === agent.id));
    return handoffs.length ? <div>{handoffs.slice(-10).reverse().map(event => {
      const outgoing = event.agent_id === agent.id, other = outgoing ? event.target_agent_id! : event.agent_id;
      return <div className="aw-thread-row" key={event.id}><strong>{outgoing ? "To" : "From"} <button className="aw-link" onClick={() => context.onSelectAgent(other)}>{nameOf(context, other)}</button></strong><span>{event.label} · {clock(event.time)}</span></div>;
    })}</div> : <Empty>No handoffs with other agents yet.</Empty>;
  }
  return <Empty>This block is not available in this version.</Empty>;
}

export function PlaceBlock({ component, place, context }: { component: string; place: Destination; context: BlockContext }) {
  const calls = context.events.filter(event => event.kind === "tool" && event.target_id === place.id);
  if (component === "worlds:visitors") {
    const ids = [...new Set([...calls.map(event => event.agent_id).reverse(), ...(place.agent_ids || [])])];
    return ids.length ? <div className="aw-agent-list">{ids.map(id => {
      const agent = context.agents.find(item => item.id === id), last = [...calls].reverse().find(event => event.agent_id === id);
      return <button key={id} className="aw-agent-row" onClick={() => context.onSelectAgent(id)}><span className="aw-agent-avatar">{(agent?.name || id).slice(0, 1).toUpperCase()}</span><span className="aw-agent-name"><strong>{agent?.name || id}</strong><small>{last ? `${describeTool(last.target || "", place.name)} · ${clock(last.time)}` : "Attached, no calls yet"}</small></span><span className="aw-agent-status" style={{ background: statusColor(agent?.status || "", context.palette) }} /></button>;
    })}</div> : <Empty>No agents have visited yet.</Empty>;
  }
  if (component === "worlds:calls") return calls.length ? <div className="aw-activity-list">{calls.slice(-12).reverse().map(event => <EventRow key={event.id} event={event} context={context} showAgent />)}</div> : <Empty>No calls yet.</Empty>;
  if (component === "worlds:tools") {
    const counts = new Map<string, number>();
    for (const event of calls) if (event.target) counts.set(event.target, (counts.get(event.target) || 0) + 1);
    const top = [...counts].sort((a, b) => b[1] - a[1]).slice(0, 8);
    return top.length ? <div>{top.map(([tool, count]) => <div className="aw-thread-row" key={tool}><strong>{describeTool(tool, place.name)}</strong><span>{tool} · {count} {count === 1 ? "call" : "calls"}</span></div>)}</div>
      : <Empty>{place.tools?.length ? `${place.tool_count} tools available, none called yet.` : "No tool calls yet."}</Empty>;
  }
  return <Empty>This block is not available in this version.</Empty>;
}
