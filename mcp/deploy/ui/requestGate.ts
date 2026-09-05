// One owner for every asynchronous detail snapshot. Obsolete requests may
// finish at the transport layer, but cannot update the selected deployment.
export class RequestGate {
  private generation = 0;
  private controller: AbortController | null = null;
  begin() {
    this.invalidate();
    const generation = this.generation;
    const controller = new AbortController();
    this.controller = controller;
    return { signal: controller.signal, current: () => this.generation === generation && !controller.signal.aborted };
  }
  invalidate() { this.generation++; this.controller?.abort(); this.controller = null; }
}
