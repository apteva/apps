import type { Scene } from './world-model';

export type AppMetadata = { install_id: number; project_id?: string; display_name?: string };

// Keep display labels separate from app/tool identities. Resolve by installation,
// never by slug: two projects can install the same app with different names.
export function withAppDisplayNames(scene: Scene | null, apps: AppMetadata[]): Scene | null {
 if (!scene || scene.source.kind.startsWith('remote')) return scene;
 const names = new Map(apps.filter(app => app.display_name?.trim()).map(app => [String(app.install_id), app]));
 return {...scene, destinations: scene.destinations.map(destination => {
  if (destination.kind !== 'app') return destination;
  const marker = destination.id.lastIndexOf(':app:');
  const app = names.get(marker < 0 ? destination.id : destination.id.slice(marker + 5));
  if (!app || app.project_id && app.project_id !== destination.project_id) return destination;
  return {...destination, name: app.display_name!.trim()};
 })};
}
