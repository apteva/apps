// Benchmark-only source and output tap. Production worklets remain unchanged.
class MarkerSource extends AudioWorkletProcessor {
  constructor(){super();this.start=Infinity;this.port.onmessage=e=>{
    if(Number.isFinite(e.data.start))this.start=e.data.start;
    if(e.data.clock_port){const port=e.data.clock_port;port.onmessage=e=>{
      if(e.data.type==='clock.probe')port.postMessage({type:'clock.reply',nonce:e.data.nonce,audio_ms:currentTime*1000});
    };port.start();}
  };}
  process(_inputs,outputs){const out=outputs[0][0];for(let i=0;i<out.length;i++){
    const sample=Math.round((currentTime-this.start)*sampleRate)+i;
    if(sample<0||!Number.isFinite(sample)){out[i]=0;continue;}
    const ms=sample*1000/sampleRate,id=Math.floor(ms/500),within=ms%500,slot=Math.floor(within/50);
    if(id>127||slot>3||within%50>=30){out[i]=0;continue;}
    const hi=(id>>4)&15,lo=id&15,freq=slot===0?3000:600+[hi,lo,hi^lo^10][slot-1]*100;
    out[i]=.25*Math.sin(2*Math.PI*freq*sample/sampleRate);
  }return true;}
}
class OutputProbe extends AudioWorkletProcessor {
  constructor(){super();this.frame=new Float32Array(Math.round(sampleRate/100));this.offset=0;
    this.port.onmessage=e=>{if(e.data.clock_port){const port=e.data.clock_port;port.onmessage=e=>{
      if(e.data.type==='clock.probe')port.postMessage({type:'clock.reply',nonce:e.data.nonce,audio_ms:currentTime*1000});
    };port.start();}};
  }
  process(inputs,outputs){outputs[0][0]?.fill(0);const channel=inputs[0]?.[0];if(!channel)return true;
    for(let i=0;i<channel.length;i++){this.frame[this.offset++]=channel[i];if(this.offset===this.frame.length){
      const frame=this.frame;this.port.postMessage({frame,end:currentTime+(i+1)/sampleRate},[frame.buffer]);this.frame=new Float32Array(Math.round(sampleRate/100));this.offset=0;
    }}return true;}
}
registerProcessor('benchmark-source',MarkerSource);
registerProcessor('benchmark-output',OutputProbe);
