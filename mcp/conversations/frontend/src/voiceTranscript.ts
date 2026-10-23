import type { Message } from "./types";

interface Segment {session:string;generation:number;start:number;end:number;status:string}
function segment(message: Message): Segment | undefined {
  const metadata=message.metadata;
  const transcript=metadata?.transcript as Record<string,unknown> | undefined;
  if(metadata?.source!=="voice" || typeof metadata.voice_session_id!=="string" || !transcript || typeof transcript.id!=="string" || typeof transcript.generation!=="number")return;
  return {session:metadata.voice_session_id,generation:transcript.generation,start:Number(transcript.start_ms??0),end:Number(transcript.end_ms??0),status:String(transcript.status)};
}

/** Presentation only: retain every durable segment and revision in transport.
 * Never merge across another speaker, typed message, session, card or long gap.
 * Legacy rows lack identities; guessing their duplicates could erase speech. */
export function voiceTranscriptMessages(messages: Message[]): Message[] {
  const result:Message[]=[];
  let previous:Segment|undefined;
  for(const message of [...messages].sort((a,b)=>(Date.parse(a.created_at)||0)-(Date.parse(b.created_at)||0)||a.id-b.id)) {
    const current=segment(message);
    if(current?.status==="blocked") {previous=undefined;continue;}
    const last=result.at(-1);
    const plain=!(message.components?.length || message.attachments?.length || message.component_kind);
    if(current && previous && last && plain && !(last.components?.length || last.attachments?.length || last.component_kind) &&
       last.role===message.role && last.agent_id===message.agent_id && last.conversation_id===message.conversation_id &&
       current.session===previous.session && current.generation===previous.generation &&
       current.start>=previous.end && previous.end>0 && current.start-previous.end<=1500) {
      const join=/^[,.;:!?，。！？]/.test(message.content)?"":" ";
      result[result.length-1]={...last,content:last.content.trimEnd()+join+message.content.trimStart(),revision:Math.max(last.revision??0,message.revision??0)};
    } else result.push(message);
    previous=current;
  }
  return result;
}
