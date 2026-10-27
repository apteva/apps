// Optional intelligibility proxy using public recorded-speech fixtures. Never a
// listening/MOS test; no production recordings, credentials or deployment calls.
import {resolve,join} from "node:path";
const root=resolve(process.argv[2]??"");
const key=process.env.OPENAI_API_KEY;
if(!process.argv[2]||!key)throw Error("Usage: OPENAI_API_KEY=... bun quality.ts /replay/output");
async function transcribe(path:string){
 const body=new FormData();body.set("model","gpt-4o-transcribe");body.set("file",Bun.file(path),"speech.wav");body.set("language","en");
 const response=await fetch("https://api.openai.com/v1/audio/transcriptions",{method:"POST",headers:{Authorization:`Bearer ${key}`},body,signal:AbortSignal.timeout(60000)});
 if(!response.ok)throw Error(`Transcription failed: HTTP ${response.status}`);
 const data=await response.json() as {text:string};if(typeof data.text!=="string")throw Error("Invalid transcript response");return data.text;
}
function words(text:string){return text.toLowerCase().replace(/[^a-z0-9' ]/g," ").split(/\s+/).filter(Boolean)}
function distance(a:string[],b:string[]){let row=b.map((_,i)=>i+1);row.unshift(0);for(let i=0;i<a.length;i++){const next=[i+1];for(let j=0;j<b.length;j++)next[j+1]=Math.min(row[j+1]+1,next[j]+1,row[j]+Number(a[i]!==b[j]));row=next}return row[b.length]}
const results=[];
for(const codec of ["G722","PCMA"]){
 const baseline=await transcribe(join(root,codec,"normal","fixed","output-samples.wav"));
 for(const pattern of ["slow102","burst320"]){
  const candidate=await transcribe(join(root,codec,pattern,"adaptive_speed","output-samples.wav"));
  const difference=distance(words(baseline),words(candidate))/Math.max(1,words(baseline).length);
  results.push({codec,pattern,baseline,candidate,word_difference: difference,accepted:difference<=.05});
 }
}
await Bun.write(join(root,"intelligibility.json"),JSON.stringify({note:"ASR agreement on sample audio; not a perceptual quality score or measurement of pauses.",results},null,2));
console.log(JSON.stringify(results.map(({baseline,candidate,...result})=>result),null,2));
if(results.some(r=>!r.accepted))process.exitCode=1;
