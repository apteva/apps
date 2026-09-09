import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
const source=readFileSync(new URL('./ComposerPanel.tsx',import.meta.url),'utf8');
const transpiler=new Bun.Transpiler({loader:'tsx',target:'browser'});
function between(start:string,end:string) {
 const a=source.indexOf(start);const b=source.indexOf(end,a);
 if(a<0||b<0)throw Error('source boundary missing');
 return source.slice(a,b);
}
function run(code:string,scope:Record<string,unknown>){
 return new Function(...Object.keys(scope),transpiler.transformSync(code))(...Object.values(scope));
}
test('splitting at 2x advances the right source_start',()=>{
 let draft:any={clips:[{id:'clip',start:0,length:10,source_start:3,playback_rate:2,asset:{type:'video',src:'storage:1'}}],audioClips:[],textClips:[]};
 const code=between('  const splitSelectedClip = () => {','  const updateAudioClip =');
 run(code+'\nsplitSelectedClip();',{playhead:4,selectedClipId:'clip',setSelectedClipId:()=>{},updateDraft:(f:any)=>{draft=f(draft)}});
 expect(draft.clips[1].source_start).toBe(11);
});
test('preview audio respects zero volume',()=>{
 const fn=between('function SyncedAudio(', 'function previewTextStyle(');
 const effect=fn.slice(fn.indexOf('    const audio ='),fn.indexOf('  }, [playing'));
 const audio={currentTime:0,duration:10,volume:1,play:async()=>{},pause:()=>{}};
 run(effect,{ref:{current:audio},sourceStart:0,playhead:0,start:0,playbackRate:1,loop:false,sourceEnd:undefined,volume:0,playing:false});
 expect(audio.volume).toBe(0);
});
test('looped video preview wraps to the next loop',()=>{
 const fn=between('function PreviewVisualLayer(', 'function SyncedAudio(');
 const effect=fn.slice(fn.indexOf('    const media ='),fn.indexOf('  }, [playing'));
 const media={currentTime:0,duration:2,playbackRate:1,play:async()=>{},pause:()=>{}};
 run(effect,{mediaRef:{current:media},clip:{start:0,length:6,timing:{mode:'loop'}},localTime:3,playing:false});
 expect(media.currentTime).toBe(1);
});
test('Render saves an existing edited composition before queueing',async()=>{
 const calls:string[]=[];
 const code=between('  const render = async () => {','  const deleteSelected =');
 await run(code+'\nreturn render();',{
 draftRevision:{current:1},renderBusy:{current:false},selectedId:7,tab:'timeline',draft:{clips:[{}],audioClips:[],textClips:[]},
 setStatus:()=>{},setRendering:()=>{},save:async()=>{calls.push('save'); return 7},executor:'auto',
 API:'/composer',projectId:'owner',withProject:(url:string)=>url,
 fetch:async()=>{calls.push('render');return {ok:true,text:async()=>JSON.stringify({render_id:8})}},
 load:async()=>{},loadCompositionDetail:async()=>{}
 });
 expect(calls).toEqual(['save','render']);
});
test('a failed initial save does not claim a successful save',async()=>{
 const statuses:string[]=[];
 const code=between('  const render = async () => {','  const deleteSelected =');
 await run(code+'\nreturn render();',{
 renderBusy:{current:false},selectedId:null,tab:'timeline',draft:{clips:[{}],audioClips:[],textClips:[]},
 setStatus:(s:string)=>statuses.push(s),save:async()=>{statuses.push('Save failed: validation')}
 });
 expect(statuses.at(-1)).toContain('failed');
});

test('clips without explicit UIDs remain independent across visual tracks',()=>{
 const helpers=between('interface NativePanelProps {','export default function ComposerPanel(');
 const composition={id:1,name:'Two tracks',edit_json:JSON.stringify({timeline:{tracks:[
 {type:'visual',clips:[{asset:{type:'image',src:'storage:1'},start:0,length:10}]},
 {type:'visual',clips:[{asset:{type:'image',src:'storage:2'},start:0,length:10}]}
 ]}}),output_json:'{}'};
 const draft=run(helpers+'\nreturn parseComposition(composition);',{composition});
 expect(new Set(draft.clips.map((clip:any)=>clip.id)).size).toBe(2);
});
test('async soundtrack generation is retained as queued, not failed',async()=>{
 const code=between('  const generateSoundtrackAI = async () => {','  const generateAudioClipAI =');
 let draft:any={soundtrack:{src:'',volume:1,ai:{media_kind:'music',prompt:'Test music'}}};
 await run(code+'\nreturn generateSoundtrackAI();',{
 draft,setAIBusy:()=>{},setStatus:()=>{},callComposerGenerate:async()=>({ai:{media_kind:'music',prompt:'Test music',status:'generating',job_id:123}}),
 updateDraft:(fn:any)=>{draft=fn(draft)}
 });
 expect(draft.soundtrack.ai.status).toBe('generating');
});
