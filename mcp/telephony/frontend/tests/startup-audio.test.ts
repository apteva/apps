import {expect,test} from 'bun:test';
import {WebRTCAudioConnection} from '../src/webrtc-audio';
import {SoftphoneSession} from '../../ui/softphone-audio';

test('RTC warms microphone independently of peer notification, but respects intentional mute',()=>{
 const a:any=new WebRTCAudioConnection({}),messages:any[]=[],track={enabled:false};
 a.capture={port:{postMessage:(m:any)=>messages.push(m)}};
 a.destination={stream:{getAudioTracks:()=>[track]}};
 a.gate();expect(track.enabled).toBe(true);expect(messages.at(-1)).toEqual({type:'muted',value:false});
 a.setMuted(true);expect(track.enabled).toBe(false);
 a.ready=a.peer=true;a.gate();expect(track.enabled).toBe(false);
 a.stopped=true;a.muted=false;a.gate();expect(track.enabled).toBe(false);
});
test('RTC does not report live until answer, media handshake and peer attachment in any order',()=>{
 for(const order of [['ready','peer','answered'],['answered','ready','peer'],['peer','answered','ready']]){
  const states:string[]=[],a:any=new WebRTCAudioConnection({onState:s=>states.push(s)});
  a.pc={connectionState:'connected'};a.context={state:'running'};
  for(let i=0;i<order.length;i++){a[order[i]]=true;a.updateLiveState();expect(states).toEqual(i===2?['live']:[]);}
  a.peer=false;a.updateLiveState();expect(states.at(-1)).toBe('reconnecting');
  a.peer=true;a.updateLiveState();expect(states.at(-1)).toBe('live');
 }
});
test('PCM capture starts at authorized ready, live waits for answer and carrier',()=>{
 for(const order of [['ready','peer.connected','call.status'],['call.status','ready','peer.connected'],['peer.connected','call.status','ready']]){
  const states:string[]=[],wire:any[]=[],s:any=new SoftphoneSession({onState:s=>states.push(s)});
  s.ctx={state:'running'};s.worker={postMessage:(m:any)=>wire.push(m)};
  for(let i=0;i<order.length;i++){s.handleControl(JSON.stringify({type:order[i],call_id:'call',status:'answered'}));expect(states.includes('live')).toBe(i===2);}
  expect(wire).toContainEqual({type:'microphone.ready',value:true});
  s.handleControl('{"type":"peer.disconnected"}');expect(states.at(-1)).toBe('reconnecting');
  expect(wire).not.toContainEqual({type:'microphone.ready',value:false});
 }
});

test('explicit media readiness is authoritative and old revisions cannot revive an interrupted path',()=>{
 const states:string[]=[],events:any[]=[],s:any=new SoftphoneSession({onState:s=>states.push(s),onMediaReadiness:e=>events.push(e)});
 s.ctx={state:'running'};s.worker={postMessage(){}};
 const control=(type:string,revision=0)=>s.handleControl(JSON.stringify({type,revision,call_id:'call',connection_id:'browser',status:'answered'}));
 control('media.not_ready',1);control('ready');control('peer.connected');control('call.status');expect(states).not.toContain('live');
 control('media.ready',2);expect(states.at(-1)).toBe('live');
 control('media.not_ready',3);expect(states.at(-1)).toBe('reconnecting');
 control('media.ready',2);expect(states.at(-1)).toBe('reconnecting');expect(events).toHaveLength(3);
});
