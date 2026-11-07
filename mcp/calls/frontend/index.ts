import type { AppHandle } from "@apteva/web-sdk";
import { CallsSession, type CallsCapabilities, type CallsSessionOptions } from "./client";

export * from "./client";

export interface CallsRoom {
  id: number;
  title: string;
  slug: string;
  status: "open" | "ended";
}

export interface CallsInviteOptions {
  room_id: number;
  participant_kind?: "human" | "agent" | "service";
  role?: "host" | "guest" | "observer";
  display_name?: string;
  capabilities?: CallsCapabilities;
  expires_at?: string;
  max_uses?: number;
}

export type CallsClientOptions = Partial<Omit<CallsSessionOptions, "projectId">>;

export class CallsClient {
  constructor(private readonly app: AppHandle, private readonly options?: CallsClientOptions) {}

  createRoom(title: string) {
    return this.app.tool<{ room: CallsRoom; host_join_url: string }>("calls_create_room", { title });
  }

  createInvite(options: CallsInviteOptions) {
    return this.app.tool<{ token: string; join_url: string }>("calls_create_join_token", { ...options });
  }

  createSession(): CallsSession {
    const apiBaseURL = new URL(this.options?.apiBaseURL ?? this.app.mcpURL(), globalThis.location?.href);
    if (!this.options?.apiBaseURL) apiBaseURL.pathname = apiBaseURL.pathname.replace(/\/mcp$/, "");
    if (this.app.installId) apiBaseURL.searchParams.set("install_id", String(this.app.installId));
    return new CallsSession({ apiBaseURL, fetch: this.options?.fetch, projectId: this.app.projectId });
  }
}

export function createClient({ app }: { app: AppHandle }, options?: CallsClientOptions): CallsClient {
  return new CallsClient(app, options);
}
