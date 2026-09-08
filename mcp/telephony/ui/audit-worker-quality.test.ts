// Regression for fallback sample-rate conversion.
import {expect,test} from "bun:test";
import {readFileSync} from "node:fs";
import vm from "node:vm";

test("audit: 48kHz fallback resampling must reject content above 12kHz",()=>{
  const context=vm.createContext({self:{},postMessage(){},performance:{now:()=>0},setTimeout,clearTimeout});
  vm.runInContext(readFileSync(new URL("./softphone-worker.js",import.meta.url),"utf8"),context);
  const result=vm.runInContext(`(()=>{
    const frame=new Float32Array(960);
    for(let i=0;i<frame.length;i++)frame[i]=Math.sin(2*Math.PI*18000*i/48000);
    const output=resample(frame,48000,24000);
    return Math.sqrt(output.reduce((sum,x)=>sum+x*x,0)/output.length);
  })()`,context);
  // 18kHz should be removed before downsampling; otherwise it aliases to 6kHz.
  expect(result).toBeLessThan(0.02);
});

test("muted worker startup cannot send capture audio before an explicit unmute", () => {
  const context = vm.createContext({ self: {}, postMessage() {}, performance: { now: () => 0 }, setTimeout, clearTimeout });
  vm.runInContext(readFileSync(new URL("./softphone-worker.js", import.meta.url), "utf8"), context);
  const sent = vm.runInContext(`(() => {
    let sent = 0;
    connect = () => {};
    self.onmessage({data:{type:"init",muted:true,mediaURL:"ws://unused",capturePort:{},playbackPort:{}}});
    socket = { readyState: 1, bufferedAmount: 0, send() { sent++; } };
    WebSocket = { OPEN: 1 };
    microphoneReady = true;
    const packet = {type:"capture",frame:new Float32Array(480).fill(0.5),sequence:1,timestampMS:0,sampleRate:24000};
    capture(packet);
    const mutedCount = sent;
    self.onmessage({data:{type:"muted",value:false}});
    capture(packet);
    return [mutedCount,sent];
  })()`, context);
  expect(sent[0]).toBe(0);
  expect(sent[1]).toBe(1);
});

function transportFixture() {
  const context = vm.createContext({ self: {}, postMessage() {}, performance: { now: () => 0 } });
  vm.runInContext(`
    let now = 1000, nextTimer = 0;
    const timers = new Map(), sockets = [], events = [];
    Date.now = () => now;
    postMessage = event => events.push(event);
    setTimeout = (fn, ms) => { const id = ++nextTimer; timers.set(id, {fn, at: now + ms, ms: 0}); return id; };
    setInterval = (fn, ms) => { const id = ++nextTimer; timers.set(id, {fn, at: now + ms, ms}); return id; };
    clearTimeout = clearInterval = id => timers.delete(id);
    close = () => {};
    WebSocket = class {
      static OPEN = 1;
      readyState = 0; bufferedAmount = 0; sent = []; responsive = true;
      constructor() { sockets.push(this); }
      open() { this.readyState = 1; this.onopen(); }
      send(data) { this.sent.push(data); if(this.responsive && JSON.parse(data).type === 'ping') this.onmessage({data:JSON.stringify({type:'pong',nonce:-1})}); }
      close() { this.readyState = 3; this.onclose?.(); }
    };
    function advance(ms) {
      const end = now + ms;
      while(true) {
        const entry = [...timers].filter(([,t]) => t.at <= end).sort((a,b) => a[1].at-b[1].at)[0];
        if(!entry) break;
        const [id,t] = entry; now = t.at;
        if(t.ms) t.at += t.ms; else timers.delete(id);
        t.fn();
      }
      now = end;
    }
  `, context);
  vm.runInContext(readFileSync(new URL("./softphone-worker.js", import.meta.url), "utf8"), context);
  vm.runInContext(`self.onmessage({data:{type:'init',mediaURL:'ws://fixture',capturePort:{},playbackPort:{postMessage(){}}}});`, context);
  return (script: string) => vm.runInContext(script, context);
}

test("ringing for 60 seconds keeps a responsive media socket open without a carrier peer", () => {
  const run = transportFixture();
  run("sockets[0].open(); advance(60000)");
  expect(run("sockets.length")).toBe(1);
  expect(run("sockets[0].readyState")).toBe(1);
  expect(run("microphoneReady")).toBe(false);
});

test("a half-open media socket is detached and retried without waiting for TCP to close", () => {
  const run = transportFixture();
  run("sockets[0].open(); advance(12000); sockets[0].responsive = false; advance(16000)");
  expect(run("sockets.length")).toBeGreaterThan(1);
  expect(run("events.some(e=>e.type==='socket.close')")).toBe(true);
  expect(run("microphoneReady")).toBe(false);
});

test("stalled handshakes exhaust a bounded retry budget", () => {
  const run = transportFixture();
  run("advance(60000)");
  expect(run("events.filter(e=>e.type==='socket.failed').length")).toBe(1);
  const attempts = run("sockets.length");
  run("advance(60000)");
  expect(run("sockets.length")).toBe(attempts);
});

test("malformed PCM and late frames from a detached socket cannot reach playback", () => {
  const run = transportFixture();
  expect(() => run("sockets[0].open(); sockets[0].onmessage({data:new ArrayBuffer(3)});")).not.toThrow();
  run("sockets[0].close(); advance(1000);");
  expect(() => run("sockets[0].onmessage({data:new ArrayBuffer(960)});")).not.toThrow();
  expect(run("playbackSequence")).toBe(0);
});
