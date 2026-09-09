import type { AppHandle } from "@apteva/web-sdk";
import { TelephonyClient, type TelephonyClientOptions } from "./src/client";
export function createClient({ app }: { app: AppHandle }, options?: TelephonyClientOptions) { return new TelephonyClient(app, options); }
