export type FinanceAPI = <T>(path: string, init?: RequestInit) => Promise<T>;

// Each mounted panel owns its client, including asynchronous payment callbacks.
export function createFinanceAPI(projectId: string, installId: number): FinanceAPI {
  return async <T>(path: string, init?: RequestInit): Promise<T> => {
    const url = new URL(`/api/apps/finance${path}`, "http://finance.local");
    if (projectId) url.searchParams.set("project_id", projectId);
    if (Number.isInteger(installId) && installId > 0) url.searchParams.set("install_id", String(installId));
    if (!projectId && !(Number.isInteger(installId) && installId > 0)) {
      throw new Error("Finance panel requires a project or installation");
    }
    const headers = new Headers(init?.headers);
    if (!headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    const response = await fetch(url.pathname + url.search, { ...init, credentials: "include", headers });
    if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
    return response.json();
  };
}
