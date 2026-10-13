import {expect,test} from "bun:test";
import {SOFTPHONE_CLIENT_VERSION} from "../../ui/client-version";
import {rtcRelayEvidence} from "../src/webrtc-audio";
import {safeMediaSessionEvent} from "../src/media-lease";

test("browser build marker matches the released manifest and survives safe telemetry",async()=>{
 const manifest=await Bun.file(new URL("../../apteva.yaml",import.meta.url)).text();
 expect(SOFTPHONE_CLIENT_VERSION).toBe(manifest.match(/^version:\s*(\S+)/m)![1]);
 const event=safeMediaSessionEvent({timestamp:new Date().toISOString(),action:"client_build",outcome:"v"+SOFTPHONE_CLIENT_VERSION.replaceAll(".","_")});
 expect(event.action).toBe("client_build");expect(event.outcome).toBe("v0_12_19");
 for(const outcome of ["relay_configured","no_relay"])expect(safeMediaSessionEvent({...event,action:"ice_configuration",outcome}).outcome).toBe(outcome);
});

test("older servers do not fabricate missing relay configuration",()=>{
 expect(rtcRelayEvidence(true)).toBe("relay_configured");expect(rtcRelayEvidence(false)).toBe("no_relay");
 for(const value of [undefined,null,"false",{}])expect(rtcRelayEvidence(value)).toBe("unreported");
});
