// Test signal only. Timed marker IDs wrap every 128 seconds; natural speech
// fills the other 300ms of each 500ms block. The production capture DSP follows.
class OutboundSoakSource extends AudioWorkletProcessor {
 constructor(){super();this.speech=[];this.frame=0;this.port.onmessage=e=>{if(e.data.speech)this.speech=e.data.speech;};}
 process(_,outputs){const out=outputs[0][0];for(let i=0;i<out.length;i++,this.frame++){
  if(this.frame%(sampleRate/2)===0)this.port.postMessage({index:Math.floor(this.frame/(sampleRate/2)),context_time:(currentFrame+i)/sampleRate});
  const ms=this.frame*1000/sampleRate,id=Math.floor(ms/500)%256,within=ms%500,slot=Math.floor(within/50);
  let v=0;
  if(slot<4){if(within%50<30){const hi=(id>>4)&15,lo=id&15;const symbol=slot===0?16:[hi,lo,hi^lo^10][slot-1];const freq=symbol===16?3000:600+symbol*100;v=.25*Math.sin(2*Math.PI*freq*this.frame/sampleRate);}}
  else if(this.speech.length){v=this.speech[Math.floor(this.frame*24000/sampleRate)%this.speech.length]/32768;}
  out[i]=v;
 }return true;}
}
registerProcessor('outbound-soak-source',OutboundSoakSource);
