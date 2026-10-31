import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { statusColor, type Palette } from "../palette";
import type { Agent, Destination } from "../world-model";
import { AppWidgetMount, defaultSettings, type AppWidget, type SettingsSchema } from "./app-widgets";
import { AgentBlock, PlaceBlock, type BlockContext } from "./blocks";
import { BLOCK_HEIGHTS, BUILTIN_BLOCKS, newBlockId, useBlockLayout, type Block, type BlockHeight, type PanelKind } from "./layout";

const MIN_WIDTH = 280, MAX_WIDTH = 640, DEFAULT_WIDTH = 360;
const WIDTH_KEY = "agent-worlds:panel-width";
const readWidth = () => { try { const value = Number(window.localStorage.getItem(WIDTH_KEY)); return value >= MIN_WIDTH && value <= MAX_WIDTH ? value : DEFAULT_WIDTH; } catch { return DEFAULT_WIDTH; } };

const Icon = ({ path }: { path: string }) => <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d={path} fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" /></svg>;
const ICONS = { prev: "M10 3 5 8l5 5", next: "m6 3 5 5-5 5", close: "m4 4 8 8M12 4l-8 8", collapse: "m6 3 5 5-5 5M3 3v10", edit: "M3 13h3l7-7-3-3-7 7v3Z", up: "m4 10 4-4 4 4", down: "m4 6 4 4 4-4" };

// The floating shell: it sits over the side of the world view and never takes
// space from it. Collapsed, it becomes a slim tab that keeps the name.
export function FloatingPanel({ title, status, palette, label, actions, onClose, children }: { title: string; status?: string; palette: Palette; label: string; actions?: ReactNode; onClose: () => void; children: ReactNode }) {
  const [width, setWidth] = useState(readWidth);
  const [collapsed, setCollapsed] = useState(false);
  const drag = useRef<{ x: number; width: number } | null>(null);
  const body = useRef<HTMLDivElement | null>(null), shell = useRef<HTMLElement | null>(null);
  useEffect(() => { if (body.current) body.current.scrollTop = 0; }, [title]);
  // The view's camera buttons move left of the panel instead of under it.
  useEffect(() => {
    const host = shell.current?.parentElement;
    host?.style.setProperty("--aw-float-width", `${width}px`);
    return () => { host?.style.removeProperty("--aw-float-width"); };
  }, [width, collapsed]);
  if (collapsed) return <button type="button" className="aw-float-tab" onClick={() => setCollapsed(false)} aria-label={`Show ${title}`}>
    {status && <span className="aw-dot" style={{ color: statusColor(status, palette) }} />}<span>{title}</span>
  </button>;
  return <aside ref={shell} className="aw-float" style={{ width }} aria-label={label}>
    <div className="aw-float-resize" role="separator" aria-orientation="vertical" aria-label="Resize panel"
      onPointerDown={event => { drag.current = { x: event.clientX, width }; event.currentTarget.setPointerCapture?.(event.pointerId); }}
      onPointerMove={event => { if (drag.current) setWidth(Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, drag.current.width + drag.current.x - event.clientX))); }}
      onPointerUp={() => { drag.current = null; try { window.localStorage.setItem(WIDTH_KEY, String(width)); } catch {} }} />
    <header className="aw-float-head">
      {status && <span className="aw-dot" style={{ color: statusColor(status, palette) }} title={status} />}
      <h2>{title}</h2>
      {actions}
      <button type="button" className="aw-icon-button" aria-label="Collapse panel" onClick={() => setCollapsed(true)}><Icon path={ICONS.collapse} /></button>
      <button type="button" className="aw-icon-button" aria-label="Close panel" onClick={onClose}><Icon path={ICONS.close} /></button>
    </header>
    <div className="aw-float-body" ref={body}>{children}</div>
  </aside>;
}

function SettingsForm({ schema, settings, onChange }: { schema?: SettingsSchema; settings: Record<string, unknown>; onChange: (settings: Record<string, unknown>) => void }) {
  const properties = Object.entries(schema?.properties || {});
  if (!properties.length) return null;
  const values = { ...defaultSettings(schema), ...settings };
  return <div className="aw-block-settings">{properties.map(([key, property]) => {
    const set = (value: unknown) => onChange({ ...settings, [key]: value });
    const title = property.title || key;
    if (property.enum?.length) return <label key={key} className="aw-field"><span>{title}</span><select value={String(values[key] ?? "")} onChange={event => set(property.enum!.find(option => String(option) === event.target.value))}>{property.enum.map((option, index) => <option key={String(option)} value={String(option)}>{property.enum_names?.[index] || String(option)}</option>)}</select></label>;
    if (property.type === "boolean") return <label key={key} className="aw-portrait"><input type="checkbox" checked={!!values[key]} onChange={event => set(event.target.checked)} /> {title}</label>;
    if (property.type === "integer" || property.type === "number") return <label key={key} className="aw-field"><span>{title}</span><input type="number" min={property.minimum} max={property.maximum} value={Number(values[key] ?? 0)} onChange={event => set(property.type === "integer" ? Math.round(Number(event.target.value)) : Number(event.target.value))} /></label>;
    return <label key={key} className="aw-field"><span>{title}</span><input value={String(values[key] ?? "")} onChange={event => set(event.target.value)} /></label>;
  })}</div>;
}

type Props = {
  kind: PanelKind; agent?: Agent; place?: Destination; context: BlockContext; palette: Palette;
  projectId: string; widgets: AppWidget[]; appBlocks: boolean;
  onClose: () => void; onStep?: (direction: -1 | 1) => void;
};

// An agent or a building, shown as the blocks the user chose. App blocks only
// render for agents on this server, since other sources' agents are not
// addressable by local apps.
export default function AgentPanel({ kind, agent, place, context, palette, projectId, widgets, appBlocks, onClose, onStep }: Props) {
  const components = useMemo(() => widgets.map(widget => widget.component), [widgets]);
  const layout = useBlockLayout(kind, projectId, components);
  const [editing, setEditing] = useState(false);
  const labels = new Map([...BUILTIN_BLOCKS[kind].map(block => [block.component, block.label] as const), ...(kind === "agent" ? widgets.map(widget => [widget.component, widget.label] as const) : [])]);
  const addable = [...labels].filter(([component]) => !layout.blocks.some(block => block.component === component));
  const update = (next: Block[]) => void layout.save(next);
  const move = (index: number, by: number) => { const next = [...layout.blocks]; const [block] = next.splice(index, 1); next.splice(index + by, 0, block); update(next); };
  const patch = (index: number, change: Partial<Block>) => update(layout.blocks.map((block, i) => i === index ? { ...block, ...change } : block));
  const subject = agent || place;
  if (!subject) return null;

  const renderBlock = (block: Block) => {
    if (block.component.startsWith("worlds:")) return agent ? <AgentBlock component={block.component} agent={agent} context={context} /> : <PlaceBlock component={block.component} place={place!} context={context} />;
    const widget = widgets.find(item => item.component === block.component);
    if (!widget) return <div className="aw-block-note">This app is not installed in this project.</div>;
    if (!agent) return null;
    if (!appBlocks) return <div className="aw-block-note">{widget.appLabel} is only available for agents on this server.</div>;
    const attached = context.destinations.some(item => item.kind === "app" && item.id.endsWith(`:app:${widget.installId}`) && item.agent_ids?.includes(agent.id));
    if (widget.attachedOnly && !attached) return <div className="aw-block-note">{widget.appLabel} is not attached to {agent.name}.</div>;
    return <AppWidgetMount widget={widget} projectId={projectId} agentId={agent.id} settings={block.settings || {}} />;
  };

  const actions = <>
    {onStep && <><button type="button" className="aw-icon-button" aria-label={`Previous ${kind === "agent" ? "agent" : "building"}`} onClick={() => onStep(-1)}><Icon path={ICONS.prev} /></button>
      <button type="button" className="aw-icon-button" aria-label={`Next ${kind === "agent" ? "agent" : "building"}`} onClick={() => onStep(1)}><Icon path={ICONS.next} /></button></>}
    <button type="button" className="aw-icon-button" aria-label="Customise panel" aria-pressed={editing} onClick={() => setEditing(!editing)}><Icon path={ICONS.edit} /></button>
  </>;

  return <FloatingPanel title={subject.name} status={agent?.status} palette={palette} label={agent ? "Agent details" : "App details"} actions={actions} onClose={onClose}>
    <div data-agent-details={agent?.id} data-place-details={place?.id}>
      {!agent && place && <div className="aw-block-note">{place.kind} · {place.status}{place.tools?.length ? ` · ${place.tool_count} tools` : ""}</div>}
      {layout.error && <div className="aw-block-note" role="alert">{layout.error}</div>}
      {layout.blocks.map((block, index) => {
        const height = BLOCK_HEIGHTS[block.height || "auto"];
        const widget = widgets.find(item => item.component === block.component);
        return <section className="aw-block" key={block.id} data-block={block.component}>
          <div className="aw-block-head">
            <h3>{labels.get(block.component) || block.component}</h3>
            {editing && <div className="aw-block-tools">
              <button type="button" className="aw-icon-button" aria-label="Move up" disabled={index === 0} onClick={() => move(index, -1)}><Icon path={ICONS.up} /></button>
              <button type="button" className="aw-icon-button" aria-label="Move down" disabled={index === layout.blocks.length - 1} onClick={() => move(index, 1)}><Icon path={ICONS.down} /></button>
              <select aria-label="Block height" value={block.height || "auto"} onChange={event => patch(index, { height: event.target.value as BlockHeight })}><option value="auto">Fit</option><option value="short">Short</option><option value="tall">Tall</option></select>
              <button type="button" className="aw-icon-button" aria-label={`Remove ${labels.get(block.component) || "block"}`} onClick={() => update(layout.blocks.filter((_, i) => i !== index))}><Icon path={ICONS.close} /></button>
            </div>}
          </div>
          {editing && widget && <SettingsForm schema={widget.schema} settings={block.settings || {}} onChange={settings => patch(index, { settings })} />}
          <div className="aw-block-body" style={height ? { height, overflow: "auto" } : undefined}>{renderBlock(block)}</div>
        </section>;
      })}
      {!layout.blocks.length && <div className="aw-block-note">No blocks. Use Customise to add some.</div>}
      {editing && <div className="aw-block-add">
        <select aria-label="Add block" value="" onChange={event => { const component = event.target.value; if (component) update([...layout.blocks, { id: newBlockId(component), component, ...(component.startsWith("worlds:") ? {} : { height: "tall" as const }) }]); }}>
          <option value="">Add a block…</option>
          {addable.map(([component, label]) => <option key={component} value={component}>{label}{component.startsWith("worlds:") ? "" : ` · ${widgets.find(widget => widget.component === component)?.appLabel}`}</option>)}
        </select>
        <button type="button" className="aw-button" onClick={() => setEditing(false)}>Done</button>
      </div>}
    </div>
  </FloatingPanel>;
}
