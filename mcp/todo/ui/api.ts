const API = "/api/apps/todo";

export function todoApiUrl(path: string, projectId: string): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${API}${path}${separator}project_id=${encodeURIComponent(projectId)}`;
}
