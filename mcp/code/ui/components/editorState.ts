// Shared editor request identity and file URL policy.
export const encodeFilePath = (path: string) => path.split("/").map(encodeURIComponent).join("/");
export const containsPath = (parent: string, path: string) => path === parent || path.startsWith(parent + "/");
export class RequestGate {
  private generation = 0;
  next() { return ++this.generation; }
  current(token: number) { return token === this.generation; }
  invalidate() { this.generation++; }
}
export const boundedLog = (previous: string, next: string) => (previous + next).slice(-256 * 1024);
