export function assetTitle(asset: { name: string; title?: string }): string {
  return asset.title?.trim() || asset.name;
}
