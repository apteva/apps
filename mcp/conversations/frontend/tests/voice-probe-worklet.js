// Test-only tap of actual rendered output; never used in the shipped player.
class ContinuityProbe extends AudioWorkletProcessor {
  constructor() {super();this.samples=[];}
  process(inputs,outputs) {
    const input=inputs[0]?.[0];
    if(input) {for(const x of input)this.samples.push(x);if(this.samples.length>=4096){this.port.postMessage(this.samples);this.samples=[];}}
    outputs[0]?.[0]?.fill(0);return true;
  }
}
registerProcessor("continuity-probe",ContinuityProbe);
