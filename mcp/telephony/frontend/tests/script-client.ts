// Run by the compiled-sidecar integration before starting Chromium.
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension } from "../src/index";
const sdk = new AptevaClient({ baseURL: process.env.TELEPHONY_TEST_GATEWAY!, apiKey: "headless-browser-fixture" });
const client = sdk.use(telephonyExtension, { projectId: "telephony-tier2", installId: 42 });
const calls = await client.listCalls();
if (client.incomingCalls(calls).length !== 1) throw new Error("Script client did not find the pending browser call");
const call = await client.getCall(calls[0].id);
if (call?.status !== "pending") throw new Error("Script client received an inconsistent call");
console.log("Script-only Telephony client passed against compiled sidecar");
