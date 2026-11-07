export type Source = { id: string; label: string; kind: string; status: string; agents: number; project_id?: string };
export type Agent = { id: string; name: string; status: string; model?: string; main_thread?: string; project_id?: string; threads?: {id: string; parent_id?: string; status?: string}[] };
export type AppNode = { id: string; name: string; status: string };
export type Destination = { id: string; project_id?: string; agent_ids?: string[]; kind: "app" | "integration" | "other"; name: string; status: string; tools?: string[]; tool_count: number; call_count: number; last_call?: string };
export type Event = { id: string; agent_id: string; target_agent_id?: string; thread_id?: string; type: string; kind: string; label: string; target?: string; target_id?: string; success?: boolean; time: string };
export type Scene = { source: Source; scope?: "project" | "instance"; projects?: {id: string; name: string}[]; agents: Agent[]; apps: AppNode[]; destinations: Destination[]; events: Event[]; at: string; warnings?: string[] };
export type Focus = {projectId?: string; teamId?: string; agentId?: string};
export type WorldNode = { id: string; kind: "project" | "team" | "agent"; name: string; agents: Agent[]; focus: Focus };
export type Team = { id: string; name: string; projectId: string; agentIds: string[] };
export const PAGE_SIZE = 6;
const projectOf = (scene: Scene, agent: Agent) => agent.project_id || scene.source.project_id || scene.projects?.[0]?.id || "source";

// Renderers consume this one hierarchy. A team is a connected component of
// observed cross-agent handoffs, never an invented platform team or a role
// guessed from an agent's name. Unlinked agents remain independent.
export function buildWorld(scene: Scene, events: Event[]) {
 const projects = new Map((scene.projects || []).map(project => [project.id, project]));
 const agents = new Map(scene.agents.map(agent => [agent.id, agent]));
 for (const agent of scene.agents) { const id = projectOf(scene, agent); if (!projects.has(id)) projects.set(id, {id, name: scene.scope === "instance" ? id : scene.source.label}); }
 const parent = new Map(scene.agents.map(agent => [agent.id, agent.id]));
 const find = (id: string): string => { let root = id; while (parent.get(root) !== root) root = parent.get(root)!; return root; };
 for (const event of events) {
  const from = agents.get(event.agent_id), to = agents.get(event.target_agent_id || "");
  if (from && to && from.id !== to.id && projectOf(scene, from) === projectOf(scene, to)) parent.set(find(to.id), find(from.id));
 }
 const components = new Map<string, Agent[]>();
 for (const agent of scene.agents) { const id = find(agent.id); components.set(id, [...(components.get(id) || []), agent]); }
 const teams: Team[] = [...components.values()].filter(items => items.length > 1).map(items => {
  const ordered = [...items].sort((a,b) => a.id.localeCompare(b.id));
  return {id: `observed:${ordered.map(a => a.id).join(",")}`, projectId: projectOf(scene, ordered[0]), name: `${ordered[0].name} + ${ordered.length - 1}`, agentIds: ordered.map(a => a.id)};
 });
 return {projects: [...projects.values()].sort((a,b) => a.name.localeCompare(b.name)), teams, projectOf: (agent: Agent) => projectOf(scene,agent)};
}
export function rootFocus(scene: Scene): Focus {
 return scene.scope === "instance" ? {} : {projectId: scene.projects?.[0]?.id || scene.source.project_id || "source"};
}
export function resolveFocus(scene: Scene, world: ReturnType<typeof buildWorld>, focus: Focus): Focus {
 const base = rootFocus(scene);
 if (!focus.projectId || !world.projects.some(p => p.id === focus.projectId)) return base;
 const team = world.teams.find(team => team.id === focus.teamId && team.projectId === focus.projectId);
 const agent = scene.agents.find(agent => agent.id === focus.agentId && world.projectOf(agent) === focus.projectId && (!team || team.agentIds.includes(agent.id)));
 return {projectId:focus.projectId, ...(team ? {teamId:team.id}:{}), ...(agent ? {agentId:agent.id}:{})};
}
export function projectWorld(scene: Scene, world: ReturnType<typeof buildWorld>, focus: Focus) {
 const team = world.teams.find(team => team.id === focus.teamId);
 const agents = scene.agents.filter(agent => (!focus.projectId || world.projectOf(agent) === focus.projectId) && (!team || team.agentIds.includes(agent.id)) && (!focus.agentId || agent.id === focus.agentId));
 let nodes: WorldNode[] = [];
 if (!focus.projectId) nodes = world.projects.map(project => ({id:project.id, kind:"project", name:project.name, agents:scene.agents.filter(agent => world.projectOf(agent) === project.id), focus:{projectId:project.id}}));
 else if (!team && !focus.agentId) {
  const teams = world.teams.filter(team => team.projectId === focus.projectId);
  if (teams.length) {
   nodes = teams.map(team => ({id:team.id, kind:"team", name:team.name, agents:agents.filter(agent => team.agentIds.includes(agent.id)), focus:{projectId:focus.projectId,teamId:team.id}}));
   const grouped = new Set(teams.flatMap(team => team.agentIds));
   nodes.push(...agents.filter(agent => !grouped.has(agent.id)).map(agent => ({id:agent.id,kind:"agent" as const,name:agent.name,agents:[agent],focus:{projectId:focus.projectId,agentId:agent.id}})));
  }
 }
 const destinations = destinationsForAgents(scene.destinations || [], agents, focus.projectId);
 return {agents,nodes,destinations, team};
}

export function featuredDestinations(items: Destination[], limit: number, selectedID: string, recent: Event[] = []) {
  const ranked = [...items].sort((a, b) => b.call_count - a.call_count || a.name.localeCompare(b.name));
  const featured: Destination[] = [];
  const add = (item?: Destination) => { if (item && featured.length < limit && !featured.some(pick => pick.id === item.id)) featured.push(item); };
  add(items.find(item => item.id === selectedID));
  for (const event of [...recent].reverse()) if (event.kind === "tool") add(items.find(item => item.id === event.target_id));
  for (const item of ranked.filter(item => item.call_count > 0)) if (featured.length < limit - 2) add(item);
  for (const kind of ["app", "integration"] as const) for (const item of ranked.filter(item => item.kind === kind).slice(0, 2)) add(item);
  for (const item of ranked) add(item);
  return recent.length ? featured.sort((a, b) => a.id.localeCompare(b.id)) : featured;
}

export function villageDestinations(items: Destination[], selectedID: string, events: Event[]) {
  items = items.filter(item => item.kind === "app" || item.kind === "integration");
  const latest = [...new Map(events.filter(event => event.kind === "tool" && items.some(item => item.id === event.target_id)).map(event => [event.agent_id, event])).values()];
  const needed = new Set([...latest.map(event => event.target_id), selectedID].filter(Boolean)).size;
  return featuredDestinations(items, Math.max(4, Math.min(8, needed)), selectedID, latest);
}


function destinationsForAgents(items: Destination[], agents: Agent[], projectId?: string) {
 const ids = new Set(agents.map(agent => agent.id));
 return items.filter(dest => (!dest.project_id || !projectId || dest.project_id === projectId) &&
   (!dest.agent_ids?.length || dest.agent_ids.some(id => ids.has(id))));
}
export function contentsForAgents(agents: Agent[], destinations: Destination[], events: Event[], projectId?: string) {
 const ids = new Set(agents.map(agent => agent.id));
 return {agents, destinations:destinationsForAgents(destinations,agents,projectId), events:events.filter(event => ids.has(event.agent_id))};
}
export function contentsForNode(node: WorldNode, destinations: Destination[], events: Event[]) {
 return contentsForAgents(node.agents,destinations,events,node.focus.projectId);
}
