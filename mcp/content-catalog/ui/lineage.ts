export type LineageAsset = { id: string; kind: string; role?: string; output_type?: string; lifecycle?: string; session_lifecycle?: string; session_id?: string; sources?: Array<{ asset_id: string; relation: string }>; ancestors?: LineageAsset[] };
export type AssetFamily<T> = { asset: T; children: AssetFamily<T>[]; matches: boolean; derivedCount: number; matchingCount: number; rank: number; outputCounts: Record<string, number> };
export type AssetScope = { lifecycle?: string; includeIntermediates?: boolean; mainOnly?: boolean };
export function assetInScope(asset: LineageAsset, scope: AssetScope = {}): boolean {
  const active = asset.lifecycle !== "archived" && asset.session_lifecycle !== "archived";
  const lifecycle = scope.lifecycle || "active";
  return (lifecycle === "all" || (lifecycle === "archived" ? !active : active)) && (scope.includeIntermediates || asset.role !== "intermediate");
}
export function lineageBadge(asset: LineageAsset): string {
  if (asset.role && asset.role !== "unspecified") return ({ main: "Main", derivative: "Derivative", intermediate: "Intermediate" } as Record<string, string>)[asset.role] || asset.role;
  if (!asset.sources?.length) return asset.kind === "original" ? "Original" : "No source linked";
  for (const type of ["reel", "clip"]) {
    if (asset.kind.toLowerCase() === type || asset.sources.some(source => source.relation.toLowerCase() === type)) return type === "reel" ? "Reel" : "Clip";
  }
  return "Derived";
}

// Context nodes explain saved ancestry but never become cards. Resolve the
// nearest visible main through hidden ancestors without rewriting source links.
export function buildAssetFamilies<T extends LineageAsset>(assets: T[], matches: T[], scope: AssetScope = {}): AssetFamily<T>[] {
  const visible = assets.filter(a => assetInScope(a, scope));
  const byID = new Map<string, LineageAsset>();
  for (const asset of assets) for (const ancestor of asset.ancestors || []) byID.set(ancestor.id, ancestor);
  for (const asset of assets) byID.set(asset.id, asset);
  const visibleIDs = new Set(visible.map(a => a.id));
  const parents = new Map<string, string>();
  for (const asset of visible) {
    if (asset.role === "main") continue;
    const seen = new Set([asset.id]);
    const queue = (asset.sources || []).map(s => s.asset_id); let fallback: string | undefined;
    for (let index = 0; index < queue.length; index++) {
      const id = queue[index]; if (seen.has(id)) continue; seen.add(id);
      const parent = byID.get(id); if (!parent) continue;
      if (visibleIDs.has(id)) {
        fallback ??= id;
        if (parent.role === "main") { fallback = id; break; }
      }
      queue.push(...(parent.sources || []).map(s => s.asset_id));
    }
    if (fallback) parents.set(asset.id, fallback);
  }
  // Bad historical graphs must never hide files or recurse forever.
  for (const asset of visible) {
    const seen = new Set<string>(); let id: string | undefined = asset.id;
    while (id) { if (seen.has(id)) { parents.delete(asset.id); break; } seen.add(id); id = parents.get(id); }
  }
  const ranks = new Map(matches.filter(a => visibleIDs.has(a.id)).map((asset, index) => [asset.id, index]));
  const nodes = new Map<string, AssetFamily<T>>(visible.map(asset => [asset.id, { asset, children: [], matches: ranks.has(asset.id), derivedCount: 0, matchingCount: 0, rank: ranks.get(asset.id) ?? Infinity, outputCounts: {} }]));
  const roots: AssetFamily<T>[] = [];
  for (const asset of visible) { const node = nodes.get(asset.id)!; const parent = parents.get(asset.id); if (parent) nodes.get(parent)!.children.push(node); else roots.push(node); }
  if (scope.mainOnly) {
    const selectChildren = (node: AssetFamily<T>, rank: number) => { for(const child of node.children) {child.matches=true;child.rank=rank;selectChildren(child,rank);} };
    for(const root of roots) if(root.asset.role==="main" && root.matches) selectChildren(root,root.rank);
  }
  const prune = (node: AssetFamily<T>): AssetFamily<T> | null => {
    const children = node.children.map(prune);
    node.derivedCount = node.children.reduce((sum, child) => sum + child.derivedCount + Number(child.asset.role !== "intermediate"), 0);
    for (const child of node.children) {
      if (child.asset.role !== "intermediate") { const category = child.asset.output_type || child.asset.kind; node.outputCounts[category] = (node.outputCounts[category] || 0) + 1; }
      for (const [category, count] of Object.entries(child.outputCounts)) node.outputCounts[category] = (node.outputCounts[category] || 0) + count;
    }
    node.children = children.filter((child): child is AssetFamily<T> => child !== null);
    node.matchingCount = Number(node.matches) + node.children.reduce((sum, child) => sum + child.matchingCount, 0);
    node.rank = Math.min(node.rank, ...node.children.map(child => child.rank));
    node.children.sort((a, b) => a.rank - b.rank);
    return node.matchingCount ? node : null;
  };
  return roots.map(prune).filter((node): node is AssetFamily<T> => node !== null).sort((a, b) => a.rank - b.rank);
}
