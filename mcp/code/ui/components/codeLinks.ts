import { containsPath } from "./editorState";
export interface CodePanelLink {
  view: "repositories" | "issues";
  repo?: string;
  path?: string;
  line?: number;
  issue?: number;
}

export interface CodeEventData {
  slug?: string;
  path?: string;
  from?: string;
  to?: string;
  number?: number;
  status?: string;
}

export function codePanelURL(link: CodePanelLink): string {
  const params = new URLSearchParams({ view: link.view });
  if (link.repo) params.set("repo", link.repo);
  if (link.path) params.set("path", link.path);
  if (link.line && link.line > 0) params.set("line", String(Math.floor(link.line)));
  if (link.issue && link.issue > 0) params.set("issue", String(Math.floor(link.issue)));
  return `/apps/code/page?${params.toString()}`;
}

export function parseCodePanelLink(search: string): CodePanelLink {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const view = params.get("view") === "issues" ? "issues" : "repositories";
  const positiveInt = (name: string) => {
    const value = Number.parseInt(params.get(name) || "", 10);
    return Number.isFinite(value) && value > 0 ? value : undefined;
  };
  return {
    view,
    repo: params.get("repo") || undefined,
    path: params.get("path") || undefined,
    line: positiveInt("line"),
    issue: positiveInt("issue"),
  };
}

export function eventTouchesRepository(topic: string, data: CodeEventData, repo: string): boolean {
  return data.slug === repo && /^(repo\.|file\.|dev\.|template\.)/.test(topic);
}

export function eventTouchesFile(topic: string, data: CodeEventData, repo: string, path: string): boolean {
  if (data.slug !== repo) return false;
  if (["repo.deleted","repo.git.switched","repo.git.pulled","repo.workspace.applied","repo.imported"].includes(topic)) return true;
  if (topic === "file.renamed") return !!((data.from && containsPath(data.from,path)) || (data.to && containsPath(data.to,path)));
  return (topic === "file.changed" || topic === "file.deleted") && !!data.path && containsPath(data.path,path);
}
