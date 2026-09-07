import { ConversationsClient } from "./src/client";
import type { AppHandle } from "@apteva/web-sdk";
export function createClient({ app }: { app: AppHandle }, options: { storageKey?: string; audience?: "public" | "operator" } = {}) {
  return new ConversationsClient(app, options.storageKey, options.audience ?? "public");
}
