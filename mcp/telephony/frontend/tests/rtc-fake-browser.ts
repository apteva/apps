/** Minimal browser media surface for driving WebRTCAudioConnection in tests:
 * every socket and peer connection is recorded so a test plays the server. */
export class FakeSocket {
 static instances:FakeSocket[]=[];
 static CONNECTING=0;static OPEN=1;static CLOSING=2;static CLOSED=3;
 readyState=0;binaryType="";bufferedAmount=0;sent:any[]=[];
 onopen:any;onmessage:any;onerror:any;onclose:any;
 constructor(public url:string){FakeSocket.instances.push(this);}
 send(text:string){this.sent.push(JSON.parse(text));}
 close(){this.readyState=3;}
 open(){this.readyState=1;this.onopen?.({});}
 receive(value:unknown){this.onmessage?.({data:JSON.stringify(value)});}
 drop(code=1006){this.readyState=3;this.onclose?.({code,wasClean:false});}
 sentOfType(type:string){return this.sent.filter(v=>v.type===type);}
}
export class FakePeer {
 static instances:FakePeer[]=[];
 connectionState="new";iceConnectionState="new";iceGatheringState="complete";signalingState="stable";
 localDescription:any;onconnectionstatechange:any;ontrack:any;closed=false;
 configurations:any[]=[];offers:any[]=[];age=10;listeners=new Map<string,Set<any>>();
 constructor(public config:any){FakePeer.instances.push(this);}
 addTrack(){return {getParameters:()=>({encodings:[{}]}),setParameters:async()=>{}};}
 async createOffer(options?:any){this.offers.push(options);return {type:"offer",sdp:"v=0 offer"};}
 async setLocalDescription(value:any){this.localDescription=value;if(value?.type==="offer")this.signalingState="have-local-offer";}
 async setRemoteDescription(){this.signalingState="stable";}
 getConfiguration(){return this.config;}
 setConfiguration(value:any){this.configurations.push(value);}
 addEventListener(name:string,fn:any){let set=this.listeners.get(name);if(!set)this.listeners.set(name,set=new Set());set.add(fn);}
 removeEventListener(name:string,fn:any){this.listeners.get(name)?.delete(fn);}
 async getStats(){return new Map<string,any>([["in",{type:"inbound-rtp",kind:"audio",timestamp:Date.now(),lastPacketReceivedTimestamp:Date.now()-this.age,packetsReceived:100}]]);}
 close(){this.closed=true;this.connectionState="closed";}
 state(value:string){this.connectionState=value;this.iceConnectionState=value;this.onconnectionstatechange?.();}
}
const node=()=>({connect:(n:any)=>n,disconnect(){},port:{postMessage(){},onmessage:null}});
const track=()=>({getSettings:()=>({}),stop(){},readyState:"live",muted:false,enabled:true,onended:null as any,onmute:null,onunmute:null});
const mediaStream=()=>{const t=track();return {getAudioTracks:()=>[t],getTracks:()=>[t]};};
class FakeContext{
 state="running";sampleRate=48000;onstatechange:any;destination={};audioWorklet={addModule:async()=>{}};currentTime=0;
 createMediaStreamDestination(){return {channelCount:2,stream:mediaStream()};}
 createMediaStreamSource(){return node();}
 createGain(){return {...node(),gain:{value:1}};}
 createAnalyser(){return {...node(),fftSize:256,getFloatTimeDomainData(){}};}
 async resume(){}
 async close(){}
}
class FakeWorklet{port={postMessage(){},onmessage:null as any};connect(n:any){return n;}disconnect(){}}
export function installFakeBrowser(){
 const names=["WebSocket","RTCPeerConnection","AudioContext","AudioWorkletNode","navigator"];
 const saved=new Map(names.map(name=>[name,Object.getOwnPropertyDescriptor(globalThis,name)]));
 FakeSocket.instances=[];FakePeer.instances=[];
 const define=(name:string,value:unknown)=>Object.defineProperty(globalThis,name,{configurable:true,writable:true,value});
 define("WebSocket",FakeSocket);define("RTCPeerConnection",FakePeer);define("AudioContext",FakeContext);define("AudioWorkletNode",FakeWorklet);
 define("navigator",{onLine:true,mediaDevices:{getUserMedia:async()=>mediaStream()}});
 return ()=>{for(const [name,descriptor] of saved){if(descriptor)Object.defineProperty(globalThis,name,descriptor);else Reflect.deleteProperty(globalThis,name);}};
}
/** Lets chained promises and zero-delay work run. */
export const settle=async(rounds=20)=>{for(let i=0;i<rounds;i++)await new Promise(resolve=>setTimeout(resolve,0));};
/** Walks a socket from open through a connected path and the server's ready. */
export async function completeSetup(socket:FakeSocket,peerIndex=FakePeer.instances.length,extra:Record<string,unknown>={}){
 socket.open();socket.receive({type:"webrtc.config",ice_servers:[],...extra});await settle();
 socket.receive({type:"webrtc.answer",sdp:"v=0 answer"});await settle();
 const peer=FakePeer.instances[peerIndex];peer.state("connected");
 socket.receive({type:"ready"});socket.receive({type:"peer.connected"});socket.receive({type:"call.status",status:"answered"});
 await settle();
 return peer;
}
