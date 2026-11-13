import { AppComponentRegistry, type AppComponentSpec } from "@apteva/web-sdk";
import { Component, createContext, useContext, useEffect, useMemo, useState, type ComponentType, type ReactNode } from "react";
import { ConversationComponentProvider, type ConversationComponentProps } from "./messageComponents";
import { useConversationLocalization } from "./i18n";

/** Existing provides.ui_components declarations from an authorized host catalog. */
export interface InstalledComponentApp {
  name: string;
  install_id: number;
  project_id?: string;
  version: string;
  display_name?: string;
  source?: string;
  status?: string;
  serving?: boolean;
  ui_components?: AppComponentSpec[];
}
export type AppCardModule = { default?: ComponentType<any>; Component?: ComponentType<any> };
export interface AppComponentLoader {
  /** Must return only installations visible to this authenticated project. */
  catalog(projectId: string): Promise<InstalledComponentApp[]>;
  /** Host-controlled code loading; message props never supply a module URL. */
  loadModule(url: string): Promise<AppCardModule>;
}

const slot = "chat.message_attachment";
const slug = /^[a-z][a-z0-9_-]*$/;

export function resolveAppCard(apps: InstalledComponentApp[], appName: string, name: string, projectId: string) {
  if (!projectId || !slug.test(appName)) throw new Error("Invalid component scope");
  const matches = apps.filter(app => app.name === appName && (!app.project_id || app.project_id === projectId));
  const local = matches.filter(app => app.project_id === projectId);
  const candidates = local.length ? local : matches;
  if (candidates.length !== 1) throw new Error("Component installation unavailable or ambiguous");
  const app = candidates[0];
  const integration = app.source === "integration";
  if ((app.status !== "running" && !app.serving) || !app.version || (!integration && (!Number.isSafeInteger(app.install_id) || app.install_id <= 0))) throw new Error("Component app unavailable");
  const specs = (app.ui_components ?? []).filter(spec => spec.name === name);
  const spec = specs.length === 1 ? specs[0] : undefined;
  if (!spec?.slots?.includes(slot)) throw new Error("Component not declared for messages");
  // Only app-relative, declared UI modules. Reject encoded traversal, query
  // injection, absolute URLs and paths outside the app's UI directory.
  if (!/^\/ui\/[a-zA-Z0-9_./-]+\.mjs$/.test(spec.entry) || spec.entry.split("/").some(part => part === "." || part === "..")) throw new Error("Invalid component entry");
  const params = new URLSearchParams({v: app.version});
  if (!integration) { params.set("install_id", String(app.install_id)); params.set("project_id", projectId); }
  const url = `/api/${integration ? "integrations" : "apps"}/${app.name}${spec.entry}?${params}`;
  return {app, spec, url};
}

interface Runtime {
  revision: number;
  retry(): void;
  resolve(project: string, app: string, name: string): Promise<{Renderer: ComponentType<any>; app: InstalledComponentApp; spec: AppComponentSpec}>;
}
const RuntimeContext = createContext<Runtime | null>(null);

/** Optional host adapter. Dashboard enables it using its existing authenticated
 * catalog. Other hosts may supply local SDK exports or their own trusted loader. */
export function InstalledAppComponentProvider({loader, children}: {loader: AppComponentLoader; children: ReactNode}) {
  const [revision, setRevision] = useState(0);
  const runtime = useMemo<Runtime>(() => {
    const catalogs = new Map<string, Promise<InstalledComponentApp[]>>();
    const modules = new Map<string, Promise<{registry: AppComponentRegistry<ComponentType<any>>; Renderer: ComponentType<any>}>>();
    return {
      revision,
      retry: () => setRevision(value => value + 1),
      async resolve(project, appName, name) {
        let catalog = catalogs.get(project);
        if (!catalog) {
          catalog = loader.catalog(project);
          catalogs.set(project, catalog);
          catalog.catch(() => catalogs.delete(project));
        }
        const {app, spec, url} = resolveAppCard(await catalog, appName, name, project);
        let pending = modules.get(url);
        if (!pending) {
          pending = loader.loadModule(revision ? `${url}&retry=${revision}` : url).then(module => {
            const Renderer = module.default ?? module.Component;
            if (!Renderer || (typeof Renderer !== "function" && typeof Renderer !== "object")) throw new Error("Component export unavailable");
            const registry = new AppComponentRegistry<ComponentType<any>>();
            registry.register(app.name, {[spec.name]: Renderer});
            return {registry, Renderer};
          });
          modules.set(url, pending);
          pending.catch(() => modules.delete(url));
        }
        // One module can be declared under compatibility aliases.
        const loaded = await pending;
        if (!loaded.registry.resolve({app: app.name, name: spec.name})) loaded.registry.register(app.name, {[spec.name]: loaded.Renderer});
        return {Renderer: loaded.registry.resolve({app: app.name, name: spec.name})!, app, spec};
      },
    };
  }, [loader, revision]);
  useEffect(() => {
    const refresh = () => setRevision(value => value + 1);
    window.addEventListener("apteva:apps-changed", refresh);
    return () => window.removeEventListener("apteva:apps-changed", refresh);
  }, []);
  return <RuntimeContext.Provider value={runtime}><ConversationComponentProvider appRenderer={InstalledAppCard}>{children}</ConversationComponentProvider></RuntimeContext.Provider>;
}

function CardUnavailable({retry}: {retry?: () => void}) {
  const {t} = useConversationLocalization();
  return <div role="status" className="rounded-md border border-border p-3 text-sm text-text-muted">
    <p>{t("component.previewUnavailable")}</p>
    {retry && <button type="button" className="mt-2 rounded border border-border px-3 py-1.5 text-text" onClick={retry}>{t("component.retryPreview")}</button>}
  </div>;
}

class CardBoundary extends Component<{children: ReactNode; retry: () => void}, {failed: boolean}> {
  state = {failed: false};
  static getDerivedStateFromError() { return {failed: true}; }
  render() { return this.state.failed ? <CardUnavailable retry={this.props.retry}/> : this.props.children; }
}

function InstalledAppCard({component, projectId}: ConversationComponentProps) {
  const runtime = useContext(RuntimeContext)!;
  const {t} = useConversationLocalization();
  const [loaded, setLoaded] = useState<Awaited<ReturnType<Runtime["resolve"]>> | null>(null);
  const [failed, setFailed] = useState(false);
  const identity = `${runtime.revision}:${projectId}:${component.app}:${component.name}`;
  const [loadedIdentity, setLoadedIdentity] = useState("");
  useEffect(() => {
    let active = true;
    setLoaded(null); setFailed(false);
    if (!projectId) { setFailed(true); return; }
    runtime.resolve(projectId, component.app, component.name).then(value => {
      if (active) { setLoaded(value); setLoadedIdentity(identity); }
    }, () => { if (active) setFailed(true); });
    return () => { active = false; };
  }, [runtime, projectId, component.app, component.name, identity]);
  if (failed) return <CardUnavailable retry={runtime.retry}/>;
  if (!loaded || loadedIdentity !== identity) return <p role="status" className="p-3 text-sm text-text-muted">{t("component.loadingPreview")}</p>;
  const {Renderer, app} = loaded;
  return <CardBoundary key={identity} retry={runtime.retry}><div className="min-w-0 max-w-full" data-app-card={`${app.name}/${component.name}`}>
    <Renderer {...component.props} appName={app.name} projectId={projectId} installId={app.install_id||undefined} slot={slot}/>
  </div></CardBoundary>;
}
