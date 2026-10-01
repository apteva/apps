import { describe, expect, test } from "bun:test";
import { pcm16WAV } from "./softphone-audio";

describe("local microphone WAV", () => {
  test("writes mono PCM16 with an exact duration and payload", async () => {
    const samples = new Int16Array([0, 1234, -2345, 32767, -32768]);
    const wav = pcm16WAV([samples.subarray(0, 2), samples.subarray(2)], samples.length, 24000);
    const bytes = new Uint8Array(await wav.arrayBuffer());
    const view = new DataView(bytes.buffer);

    expect(wav.type).toBe("audio/wav");
    expect(new TextDecoder().decode(bytes.subarray(0, 4))).toBe("RIFF");
    expect(new TextDecoder().decode(bytes.subarray(8, 12))).toBe("WAVE");
    expect(view.getUint16(22, true)).toBe(1);
    expect(view.getUint32(24, true)).toBe(24000);
    expect(view.getUint16(34, true)).toBe(16);
    expect(view.getUint32(40, true)).toBe(samples.length * 2);
    expect(Array.from({ length: samples.length }, (_, index) => view.getInt16(44 + index * 2, true))).toEqual(Array.from(samples));
  });
});

test('carrier delivery notices preserve the healthy microphone and report recovery once', async()=>{
 const {SoftphoneSession}=await import('./softphone-audio');
 const notices:string[]=[];const states:unknown[]=[];
 const session:any=new SoftphoneSession({onNotice:n=>notices.push(n),onState:s=>states.push(s)});
 session.microphoneTransportReady=true;
 session.handleControl(JSON.stringify({type:'media.delivery',direction:'carrier_to_operator',state:'stalled'}));
 expect(session.microphoneTransportReady).toBe(true);
 session.handleControl(JSON.stringify({type:'media.delivery',state:'flowing'}));
 session.handleControl(JSON.stringify({type:'media.delivery',state:'flowing'}));
 expect(notices).toHaveLength(2);expect(states).toHaveLength(0);
});
