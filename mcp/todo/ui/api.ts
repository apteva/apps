// Todos are stored in UTC, but "today" has to end at the operator's
// midnight rather than the server's — so every request carries the
// viewer's offset east of UTC in minutes (CEST = 120).
export function tzOffsetMinutes(now: Date = new Date()): number {
  return -now.getTimezoneOffset();
}

export function todoApiUrl(
  path: string,
  projectId: string,
  tzOffset: number = tzOffsetMinutes(),
  scope: { appName?: string; installId?: number } = {},
): string {
  const separator = path.includes("?") ? "&" : "?";
  const api = `/api/apps/${encodeURIComponent(scope.appName || "todo")}`;
  const install = scope.installId ? `&install_id=${scope.installId}` : "";
  return `${api}${path}${separator}project_id=${encodeURIComponent(projectId)}&tz_offset=${tzOffset}${install}`;
}
