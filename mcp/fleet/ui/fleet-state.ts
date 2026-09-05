export function decodeToolResult<T>(response: any): T {
  if (response.error) throw new Error(response.error.message || "Tool failed");
  if (response.result?.isError)
    throw new Error(response.result.content?.[0]?.text || "Tool failed");
  const text = response.result?.content?.[0]?.text;
  return text ? JSON.parse(text) : response.result;
}

export function createSelectionGate() {
  let generation = 0;
  let selected: string | null = null;
  return {
    select(id: string | null) {
      if (id !== selected) {
        selected = id;
        generation++;
      }
    },
    request(id: string) {
      const current = ++generation;
      return () => id === selected && current === generation;
    },
  };
}

export function requestedUpdateVersion(
  tenant: { target_version?: string; current_version?: string },
  latest?: string,
) {
  return tenant.target_version &&
    tenant.target_version !== tenant.current_version
    ? tenant.target_version
    : latest || "";
}
