import type { Message } from "./types";

interface Segment {session:string;generation:number;start:number;end:number;status:string}
function segment(message: Message): Segment | undefined {
  const metadata=message.metadata;
  const transcript=metadata?.transcript as Record<string,unknown> | undefined;
  if(metadata?.source!=="voice" || typeof metadata.voice_session_id!=="string" || !transcript || typeof transcript.id!=="string" || typeof transcript.generation!=="number")return;
  return {session:metadata.voice_session_id,generation:transcript.generation,start:Number(transcript.start_ms??0),end:Number(transcript.end_ms??0),status:String(transcript.status)};
}

// Expand only verified timed fragments. Old rows without this evidence stay intact.
function timedMessages(messages: Message[]): Message[] {
  const result: Message[] = [];
  let run: Message[] = [];
  const flush = () => {
    if (!run.length) return;
    const times = run.map(m => Date.parse(m.created_at) || 0);
    const expanded = run.flatMap(message => {
      const transcript = message.metadata!.transcript as Record<string, unknown>;
      const fragments = transcript.fragments as {text:string;start_ms:number;end_ms:number}[] | undefined;
      if (message.role!=="agent" || !Array.isArray(fragments) || !fragments.length ||
          !fragments.every(f => f && typeof f.text === "string" && Number.isFinite(f.start_ms) && Number.isFinite(f.end_ms) && f.start_ms >= 0 && f.end_ms >= f.start_ms) ||
          fragments.map(f => f.text).join("").trim() !== message.content.trim()) return [message];
      return fragments.map((f, index) => ({...message, content:f.text,
        presentation_key:`voice:${message.id}:${index}`, metadata:{...message.metadata,
          transcript:{...transcript,start_ms:f.start_ms,end_ms:f.end_ms}}}));
    });
    expanded.sort((a,b) => segment(a)!.start-segment(b)!.start || a.id-b.id);
    // Keep the run inside its durable wall-clock interval so typed messages and
    // cards remain boundaries. Caption timestamps decide order, arrival does not.
    const first=Math.min(...times), step=Math.max(0.001,(Math.max(...times)-first)/Math.max(1,expanded.length-1));
    expanded.forEach((m,i)=>result.push({...m,presentation_at:first+i*step}));
    run=[];
  };
  for (const m of [...messages].sort((a,b)=>(Date.parse(a.created_at)||0)-(Date.parse(b.created_at)||0)||a.id-b.id)) {
    const s=segment(m), last=run.at(-1), previous=last && segment(last);
    if (!s || s.status==="blocked" || m.components?.length || m.attachments?.length || m.component_kind) {flush();result.push(m);continue;}
    if (previous && (s.session!==previous.session || s.generation!==previous.generation || m.conversation_id!==last!.conversation_id)) flush();
    run.push(m);
  }
  flush();return result;
}

/** Presentation only: retain every durable segment and revision in transport.
 * Never merge across another speaker, typed message, session, card or long gap.
 * Legacy rows lack identities; guessing their duplicates could erase speech. */
export function voiceTranscriptMessages(messages: Message[]): Message[] {
  const result:Message[]=[];
  let previous:Segment|undefined;
  let previousMessage:Message|undefined;
  for(const message of timedMessages(messages)) {
    const current=segment(message);
    if(current?.status==="blocked") {previous=undefined;continue;}
    const last=result.at(-1);
    const plain=!(message.components?.length || message.attachments?.length || message.component_kind);
    if(current && previous && last && plain && !(last.components?.length || last.attachments?.length || last.component_kind) &&
       last.role===message.role && last.agent_id===message.agent_id && last.conversation_id===message.conversation_id &&
       current.session===previous.session && current.generation===previous.generation &&
       (current.start>=previous.end || (previousMessage?.id===message.id && Boolean(message.presentation_key))) && previous.end>0 && current.start-previous.end<=1500) {
      const sameSegment=previousMessage?.id===message.id && Boolean(message.presentation_key);
      const join=/^[,.;:!?，。！？]/.test(message.content)?"":" ";
      result[result.length-1]={...last,content:sameSegment?last.content+message.content:last.content.trimEnd()+join+message.content.trimStart(),revision:Math.max(last.revision??0,message.revision??0)};
    } else result.push(message);
    previous=current;previousMessage=message;
  }
  return result;
}
