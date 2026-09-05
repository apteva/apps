export function normalizeArchitecture(value: string): string {
  if (["arm", "arm64", "aarch64"].includes(value.toLowerCase())) return "arm64";
  if (["x86", "x86_64", "amd64", "x64"].includes(value.toLowerCase())) return "amd64";
  return value.toLowerCase();
}
