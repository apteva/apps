import {expect,test} from "bun:test";
import {carrierBufferInput} from "../../ui/CarrierJitterBufferControls";

test("carrier jitter buffer remains optional and accepts Telnyx bounds",()=>{
 expect(carrierBufferInput({enabled:false,min_ms:60,max_ms:200}).enabled).toBe(false);
 expect(carrierBufferInput({enabled:true,min_ms:40,max_ms:400})).toEqual({enabled:true,min_ms:40,max_ms:400});
});
test("carrier jitter buffer rejects invalid values",()=>{
 for(const [min,max] of [[39,200],[60,401],[201,200],[60.5,200],[60,NaN]])
  expect(()=>carrierBufferInput({enabled:true,min_ms:min,max_ms:max})).toThrow();
});
