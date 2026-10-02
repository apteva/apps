import type { AppHandle } from "@apteva/web-sdk";
import source from "../../ui/listener-worklet.js" with { type: "text" };
import captureSource from "../../ui/softphone-worklet.js" with {type:"text"};
import { PreviewResampler } from "../../ui/softphone-audio";

export interface ListenerDiagnostics {
  dropped_ms: number[];
  played_ms: number[];
  max_queue_ms: number;
  max_late_ms: number;
  sequence_gaps: number[];
  network_excess_ms: number;
  network_dropped_ms: number[];
}
export interface ListenerAudioCallbacks {
  onReady(): void;
  onClose(reason: string): void;
  onTalking?(talking:boolean,detail?:string):void;
  onDiagnostics?(value: ListenerDiagnostics): void;
}
export interface ListenerAudioConnection {
  start(url: string, options?:{coaching?:boolean}): Promise<void>;
  startTalking?():Promise<void>;
  stopTalking?():void;
  stop(): void;
  setOutputVolume(value: number): void;
}
export interface ListenerAudioRuntime { create(callbacks: ListenerAudioCallbacks): ListenerAudioConnection }
export interface ListenerPlaybackOptions { stereo?: boolean; outputVolume?: number; outputDeviceId?: string; inputDeviceId?: string }

/** Validate the directional protocol before passing any samples to the renderer. */
export function decodeListenerFrame(buffer: ArrayBuffer) {
  if (buffer.byteLength < 26 || buffer.byteLength > 984 || buffer.byteLength % 2) throw new Error("Invalid listener audio frame");
  const view = new DataView(buffer);
  const direction = view.getUint32(4, true);
  if (view.getUint32(0, true) !== 0x314c5441 || direction > 1) throw new Error("Invalid listener audio protocol");
  const sequence = Number(view.getBigUint64(8, true));
  const timestampMS = Number(view.getBigUint64(16, true));
  if (!Number.isSafeInteger(sequence) || !Number.isSafeInteger(timestampMS)) throw new Error("Invalid listener audio timing");
  const frame = new Float32Array((buffer.byteLength - 24) / 2);
  for (let i = 0; i < frame.length; i++) frame[i] = view.getInt16(24 + i * 2, true) / 32768;
  return { direction, sequence, timestampMS, frame };
}

/** A coaching credential's only accepted capture protocol. */
export function encodeCoachingFrame(frame:Float32Array,generation:number,sequence:number,clock:number):ArrayBuffer {
  if(frame.length<1 || frame.length>480 || !Number.isFinite(clock) || clock<0 || !Number.isInteger(generation) || generation<1 || generation>0xffffffff) throw new Error("Invalid coaching capture");
  const buffer=new ArrayBuffer(24+frame.length*2),v=new DataView(buffer);
  v.setUint32(0,0x31435741,true);v.setUint32(4,generation,true);v.setUint32(8,sequence>>>0,true);v.setFloat64(16,clock,true);
  for(let i=0;i<frame.length;i++){ const n=Math.max(-1,Math.min(1,frame[i]));v.setInt16(24+i*2,n<0?n*32768:n*32767,true); }
  return buffer;
}

export function createListenerAudio(app: AppHandle, options: ListenerPlaybackOptions = {}): ListenerAudioRuntime {
  return { create(callbacks) {
    let context: AudioContext | undefined, node: AudioWorkletNode | undefined, gain: GainNode | undefined;
    let socket: WebSocket | undefined, closed = false;
    const blobURLs:string[]=[];
    let coaching=false, talkGeneration=0, talkWanted=false, talking=false;
    let mic:MediaStream|undefined, capture:AudioWorkletNode|undefined, micSource:MediaStreamAudioSourceNode|undefined, silent:GainNode|undefined, captureChannel:MessageChannel|undefined;
    let keepalive:ReturnType<typeof setInterval>|undefined;
    let talkAck:((generation:number,error?:Error)=>void)|undefined;
    let captureLoaded=false;
    let audioOffset: number | undefined, transitBase: number | undefined, networkExcess = 0;
    let ready = false;
    const networkDroppedMS = [0, 0];
    const sequenceGaps = [0, 0], expected: Array<number | undefined> = [undefined, undefined];
    const resamplers = [new PreviewResampler(), new PreviewResampler()];
    let volume = options.outputVolume ?? 1;
    let cancelStart: (() => void) | undefined;
    const stopTalking = () => {
      const generation=++talkGeneration;
      talkWanted=false;talking=false;talkAck?.(generation,new Error("Coaching cancelled"));
      if(keepalive)clearInterval(keepalive);keepalive=undefined;
      captureChannel?.port1.close();captureChannel?.port2.close();captureChannel=undefined;
      capture?.disconnect();capture=undefined;micSource?.disconnect();micSource=undefined;silent?.disconnect();silent=undefined;
      mic?.getTracks().forEach(t=>{t.onended=null;t.stop();});mic=undefined;
      if(coaching && ready && socket?.readyState===WebSocket.OPEN) socket.send(JSON.stringify({type:"coach.stop",generation}));
      callbacks.onTalking?.(false);
    };
    const onBlur=()=>stopTalking();
    const onVisibility=()=>{if(document.visibilityState!=="visible")stopTalking();};
    const stop = () => {
      if (closed) return;
      stopTalking();closed = true; cancelStart?.();
      window.removeEventListener("blur",onBlur);document.removeEventListener("visibilitychange",onVisibility);
      if (socket) { socket.onmessage = socket.onclose = socket.onerror = null; socket.close(); }
      node?.disconnect(); gain?.disconnect();
      if (context) { context.onstatechange = null; void context.close().catch(() => {}); }
      for(const url of blobURLs) URL.revokeObjectURL(url);
    };
    const fail = (reason: string) => { if (!closed) { stop(); callbacks.onClose(reason); } };
    const ensureOpen = () => { if (closed) throw new Error("Listening cancelled"); };
    return {
      async start(url,settings) {
        coaching=settings?.coaching===true;
        ensureOpen();
        try {
          context = new AudioContext({ latencyHint: "interactive" });
          if (context.state === "suspended") await context.resume();
          ensureOpen();
          if (options.outputDeviceId && "setSinkId" in context) await (context as AudioContext & { setSinkId(id: string): Promise<void> }).setSinkId(options.outputDeviceId);
          ensureOpen();
          let workletURL: string;
          const gateway = new URL(app.mcpURL(), location.href);
          if (gateway.origin === location.origin) {
            const hash = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(source))), b => b.toString(16).padStart(2, "0")).join("");
            const path = `/ui/frontend/listener-${hash}.js`;
            const served = await app.get<string>(path, { cache: "no-cache", redirect: "error", headers: { Accept: "text/plain" } });
            ensureOpen();
            if (served !== source) throw new Error("Listener audio asset integrity mismatch");
            const asset = new URL(`/api/apps/telephony/_install/${app.installId}${path}`, gateway);
            asset.searchParams.set("project_id", app.projectId!); asset.searchParams.set("install_id", String(app.installId));
            workletURL = asset.href;
          } else { const blobURL = URL.createObjectURL(new Blob([source], { type: "text/javascript" })); blobURLs.push(blobURL);workletURL = blobURL; }
          await context.audioWorklet.addModule(workletURL); ensureOpen();
          node = new AudioWorkletNode(context, "telephony-listener", { numberOfInputs: 0, outputChannelCount: [options.stereo ? 2 : 1], processorOptions: { stereo: options.stereo } });
          gain = context.createGain(); gain.gain.value = volume;
          node.connect(gain).connect(context.destination);
          node.onprocessorerror = () => fail("listener_audio_error");
          node.port.onmessage = ({ data }) => { if (data?.type === "listener.diagnostics" && !closed) { try { callbacks.onDiagnostics?.({ ...data, sequence_gaps: [...sequenceGaps], network_excess_ms: networkExcess, network_dropped_ms: [...networkDroppedMS] }); } catch { /* host isolation */ } } };
          context.onstatechange = () => { if (context?.state === "suspended" || (context?.state as string) === "interrupted") fail("listener_audio_paused"); };
          await new Promise<void>((resolve, reject) => {
            let settled = false;
            const timeout = setTimeout(() => { finish(new Error("Listener connection timed out")); fail("listener_network_error"); }, 10_000);
            const finish = (error?: Error) => { if (settled) return; settled = true; clearTimeout(timeout); cancelStart = undefined; error ? reject(error) : resolve(); };
            cancelStart = () => finish(new Error("Listening cancelled"));
            socket = new WebSocket(url); socket.binaryType = "arraybuffer";
            socket.onmessage = ({ data }) => {
              if (closed || !context || !node) return;
              try {
                if (typeof data === "string") { const event=JSON.parse(data); if(event.type==="listener.ready") { if(coaching && event.coaching!==true)throw new Error("Coaching not authorized");ready=true;finish();callbacks.onReady(); } else if(event.type==="coach.started") { talkAck?.(event.generation); } else if(event.type==="coach.rejected") { talkAck?.(talkGeneration,new Error(event.detail||"Coaching unavailable")); } else if(event.type==="coach.stopped" && (event.generation===undefined || event.generation===talkGeneration)) { stopTalking(); } return; }
                if (!ready || !(data instanceof ArrayBuffer)) return;
                const decoded = decodeListenerFrame(data);
                const d = decoded.direction;
                if (expected[d] !== undefined && decoded.sequence < expected[d]!) return;
                if (expected[d] !== undefined && decoded.sequence > expected[d]!) sequenceGaps[d] += decoded.sequence - expected[d]!;
                expected[d] = decoded.sequence + 1;
                const transit = performance.now() - decoded.timestampMS;
                transitBase = Math.min(transitBase ?? transit, transit);
                networkExcess = Math.max(0, transit - transitBase);
                if (networkExcess > 200) { networkDroppedMS[d] += decoded.frame.length * 1000 / 24_000; return; } // Never play accumulated network backlog.
                audioOffset ??= context.currentTime * 1000 + 60 - decoded.timestampMS;
                const frame = resamplers[d].process(decoded.frame, 24_000, context.sampleRate);
                node.port.postMessage({ direction: d, frame, playAtMS: decoded.timestampMS + audioOffset }, [frame.buffer]);
              } catch { finish(new Error("Invalid listener media")); fail("listener_protocol_error"); }
            };
            socket.onerror = () => { finish(new Error("Listener connection failed")); fail("listener_network_error"); };
            socket.onclose = event => { finish(new Error(event.reason || "Listener disconnected")); fail(event.reason || "listener_disconnected"); };
          });
          ensureOpen();
          if(coaching){window.addEventListener("blur",onBlur);document.addEventListener("visibilitychange",onVisibility);}
        } catch (error) { stop(); throw error; }
      },
      async startTalking() {
        ensureOpen();
        if(!coaching || !ready || !context || !socket || socket.readyState!==WebSocket.OPEN) throw new Error("Coaching session not ready");
        if(talkWanted)return;
        talkWanted=true;const generation=++talkGeneration;
        const current=()=>!closed && talkWanted && generation===talkGeneration;
        try {
          const stream=await navigator.mediaDevices.getUserMedia({audio:{echoCancellation:true,noiseSuppression:false,autoGainControl:false,...(options.inputDeviceId?{deviceId:{exact:options.inputDeviceId}}:{})}});
          if(!current()){stream.getTracks().forEach(t=>t.stop());return;}
          mic=stream;mic.getAudioTracks().forEach(t=>{t.onended=()=>stopTalking();});
          if(!captureLoaded) {
            const gateway=new URL(app.mcpURL(),location.href);let url:string;
            if(gateway.origin===location.origin){ const hash=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(captureSource))),b=>b.toString(16).padStart(2,"0")).join("");const path=`/ui/frontend/worklet-${hash}.js`;const served=await app.get<string>(path,{cache:"no-cache",redirect:"error",headers:{Accept:"text/plain"}});if(served!==captureSource)throw new Error("Coaching audio asset integrity mismatch");const asset=new URL(`/api/apps/telephony/_install/${app.installId}${path}`,gateway);asset.searchParams.set("project_id",app.projectId!);asset.searchParams.set("install_id",String(app.installId));url=asset.href;
            } else {url=URL.createObjectURL(new Blob([captureSource],{type:"text/javascript"}));blobURLs.push(url);}
            if(!current())return;await context.audioWorklet.addModule(url);captureLoaded=true;
          }
          if(!current())return;
          await new Promise<void>((resolve,reject)=>{
            const timeout=setTimeout(()=>{talkAck=undefined;reject(new Error("Coaching activation timed out"));},3000);
            talkAck=(ack,error)=>{if(error || ack===generation){clearTimeout(timeout);talkAck=undefined;error?reject(error):resolve();}};
            socket!.send(JSON.stringify({type:"coach.start",generation}));
          });
          if(!current())return;
          capture=new AudioWorkletNode(context,"softphone-capture",{numberOfInputs:1,outputChannelCount:[1],processorOptions:{inputGainDB:0,highpassFilter:true}});
          micSource=context.createMediaStreamSource(stream);silent=context.createGain();silent.gain.value=0;micSource.connect(capture).connect(silent).connect(context.destination);
          captureChannel=new MessageChannel();let sequence=0;const resampler=new PreviewResampler();
          captureChannel.port1.onmessage=({data})=>{
            if(!current() || !talking || data?.type!=="capture" || !(data.frame instanceof Float32Array) || !context || !socket)return;
            if(!Number.isFinite(data.timestamp_ms)||context.currentTime*1000-data.timestamp_ms>150 || socket.bufferedAmount>2880 || socket.readyState!==WebSocket.OPEN)return;
            const frame=resampler.process(data.frame,data.sample_rate,24000);
            if(frame.length>0 && frame.length<=480)socket.send(encodeCoachingFrame(frame,generation,sequence++,data.timestamp_ms));
          };
          captureChannel.port1.start();capture.port.postMessage({type:"transport",port:captureChannel.port2},[captureChannel.port2]);
          talking=true;callbacks.onTalking?.(true);
          keepalive=setInterval(()=>{ if(current() && socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify({type:"coach.keepalive",generation})); },1000);
        } catch(error) { if(current()){stopTalking();throw error;} }
      },
      stopTalking,
      stop,
      setOutputVolume(value) { if (!Number.isFinite(value) || value < 0 || value > 1) throw new RangeError("Listener volume must be 0–1"); volume = value; if (gain) gain.gain.value = value; },
    };
  } };
}
