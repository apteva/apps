import { describe, expect, test } from 'bun:test';
import { forward, spikeStep } from './network';

describe('live neuron display', () => {
  test('forward propagation includes biases and sigmoid output', () => {
    const a=forward({shape:[2,2,1],weights:[[[.5,-.2,.1],[-.1,.8,-.3]],[[.6,-.5,.2]]],step:0},.6,-.4);
    const h1=Math.tanh(.5*.6-.2*-.4+.1),h2=Math.tanh(-.1*.6+.8*-.4-.3);
    expect(a[1][0]).toBeCloseTo(h1,12);
    expect(a[1][1]).toBeCloseTo(h2,12);
    expect(a[2][0]).toBeCloseTo(1/(1+Math.exp(-(.6*h1-.5*h2+.2))),12);
  });
  test('silent input remains silent and stronger activation spikes more often',()=>{
    const spikes=(activation:number)=>{let v=0,count=0;for(let i=0;i<100;i++){const s=spikeStep(v,activation,.05);v=s.voltage;if(s.fired)count++;expect(v).toBeGreaterThanOrEqual(0);expect(v).toBeLessThan(1);}return count;};
    expect(spikes(0)).toBe(0);
    expect(spikes(.95)).toBeGreaterThan(spikes(.5));
    expect(spikes(-.95)).toBe(spikes(.95));
  });
});
