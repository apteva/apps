import type { Camera } from "./camera";
import { HierarchyStage, OperationsStage, SystemGraph } from "./clean-stages";
import type { usePalette } from "./palette";
import PixelStage from "./pixel/PixelStage";
import IsoStage from "./pixel/iso/IsoStage";
import type { ThemeEntry } from "./themes";
import { PAGE_SIZE, type Agent, type Destination, type Event, type Focus, type Scene, type WorldNode } from "./world-model";

// The one place a theme picks its renderer, shared by the page and the
// dashboard widget so every view looks the same wherever it is shown.
export type WorldStageProps = {
  theme: ThemeEntry; scene: Scene; camera: Camera; palette: ReturnType<typeof usePalette>;
  stageRef: React.RefObject<HTMLCanvasElement | SVGSVGElement | null>;
  agents: Agent[]; nodes: WorldNode[]; destinations: Destination[]; events: Event[]; stageEvents: Event[];
  teams: Map<string, string>; portrait: boolean; now: number; live: boolean; page: number;
  selectedAgent: string; selectedDestination: string;
  onSelect: (id: string) => void; onSelectDestination: (id: string) => void; onFocus: (focus: Focus) => void;
  onWorldSize?: (size: { width: number; height: number }) => void;
};

export default function WorldStage(props: WorldStageProps) {
  const { theme, scene, stageRef, nodes, palette, portrait } = props;
  const stage = { stageRef, camera: props.camera, agents: props.agents, nodes, destinations: props.destinations, events: props.events, teams: props.teams, portrait, now: props.now, live: props.live, selectedAgent: props.selectedAgent, selectedDestination: props.selectedDestination, onSelect: props.onSelect, onSelectDestination: props.onSelectDestination, onFocus: props.onFocus, onWorldSize: props.onWorldSize };
  if (theme.iso) return <IsoStage {...stage} />;
  if (theme.pixel) return <PixelStage theme={theme.pixel} {...stage} />;
  if (nodes.length) return <HierarchyStage refNode={stageRef} nodes={nodes.slice(props.page * PAGE_SIZE, (props.page + 1) * PAGE_SIZE)} preset={theme.layout === "graph" ? "graph" : "operations"} label={theme.name} portrait={portrait} destinations={props.destinations} events={props.events} selectedAgent={props.selectedAgent} onSelect={props.onSelect} onFocus={props.onFocus} palette={palette} />;
  if (theme.layout === "graph") return <SystemGraph refNode={stageRef} source={scene.source} agents={props.agents} destinations={props.destinations} selectedDestination={props.selectedDestination} events={props.stageEvents} portrait={portrait} selectedAgent={props.selectedAgent} onSelect={props.onSelect} onSelectDestination={props.onSelectDestination} palette={palette} />;
  return <OperationsStage refNode={stageRef} agents={props.agents} events={props.stageEvents} portrait={portrait} selectedAgent={props.selectedAgent} onSelect={props.onSelect} palette={palette} />;
}
