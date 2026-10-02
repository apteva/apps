export type LineageAsset = { id: string; kind: string; sources?: Array<{ asset_id: string; relation: string }> };
export type AssetFamily<T> = { asset: T; children: AssetFamily<T>[]; matches: boolean; derivedCount: number; matchingCount: number; rank: number };

export function lineageBadge(asset: LineageAsset): string {
  if (!asset.sources?.length) return asset.kind === "original" ? "Original" : "No source linked";
  for (const type of ["reel", "clip"]) {
    if (asset.kind.toLowerCase() === type || asset.sources.some(source => source.relation.toLowerCase() === type)) return type === "reel" ? "Reel" : "Clip";
  }
  return "Derived";
}

// Display each asset once, under its first linked in-session source. All other
// sources remain available on its card/detail. External parents stay linked.
export function buildAssetFamilies<T extends LineageAsset>(assets: T[], matches: T[]): AssetFamily<T>[] {
  const byID = new Map(assets.map(asset => [asset.id, asset]));
  const parents = new Map<string, string>();
  for (const asset of assets) {
    const parent = asset.sources?.find(source => source.asset_id !== asset.id && byID.has(source.asset_id));
    if (parent) parents.set(asset.id, parent.asset_id);
  }
  // Bad historical graphs must never hide files or recurse forever.
  for (const asset of assets) {
    const seen = new Set<string>(); let id: string | undefined = asset.id;
    while (id) { if (seen.has(id)) { parents.delete(asset.id); break; } seen.add(id); id = parents.get(id); }
  }
  const ranks = new Map(matches.map((asset, index) => [asset.id, index]));
  const nodes = new Map<string, AssetFamily<T>>(assets.map(asset => [asset.id, { asset, children: [], matches: ranks.has(asset.id), derivedCount: 0, matchingCount: 0, rank: ranks.get(asset.id) ?? Infinity }]));
  const roots: AssetFamily<T>[] = [];
  for (const asset of assets) { const node = nodes.get(asset.id)!; const parent = parents.get(asset.id); if (parent) nodes.get(parent)!.children.push(node); else roots.push(node); }
  const prune = (node: AssetFamily<T>): AssetFamily<T> | null => {
    const children = node.children.map(prune);
    node.derivedCount = node.children.reduce((sum, child) => sum + child.derivedCount + 1, 0);
    node.children = children.filter((child): child is AssetFamily<T> => child !== null);
    node.matchingCount = Number(node.matches) + node.children.reduce((sum, child) => sum + child.matchingCount, 0);
    node.rank = Math.min(node.rank, ...node.children.map(child => child.rank));
    node.children.sort((a, b) => a.rank - b.rank);
    return node.matchingCount ? node : null;
  };
  return roots.map(prune).filter((node): node is AssetFamily<T> => node !== null).sort((a, b) => a.rank - b.rank);
}
