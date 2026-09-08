import type { AppHandle } from "@apteva/web-sdk";
import { TelephonyClient } from "./src/client";
export function createClient({ app }: { app: AppHandle }) { return new TelephonyClient(app); }
