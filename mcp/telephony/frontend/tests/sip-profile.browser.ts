import {test,expect} from '@playwright/test';
import {sipPreset} from '../../ui/SIPAudioProfileControls';
test('runtime profile sends the selected codec, bounded policy and server-relative TTL',async({page})=>{
 const requests:any[]=[];
 await page.route('**/profile',async(route)=>{const body=route.request().postDataJSON();requests.push(body);await route.fulfill({json:{effective:{rtp_playback:{...sipPreset('balanced'),profile_revision:'test-revision'}},...(body.config?{config:body.config,active_override:true}:{} )}})});
 await page.goto('/');await page.locator('summary').click();
 await expect(page.getByRole('button',{name:'Save audio profile'})).toBeEnabled();
 await page.getByLabel('Preset',{exact:true}).selectOption('resilient');
 await page.getByLabel('Codec restriction').selectOption('PCMA');
 await page.getByLabel(/Temporary test/).check();
 await page.getByRole('button',{name:'Save audio profile'}).click();
 await expect.poll(()=>requests.length).toBe(2);
 expect(requests[1]).toMatchObject({direction:'outbound',carrier_connection_id:10,phone_number:'+12025550100',ttl_seconds:3600,config:{preset:'resilient',codecs:['PCMA'],rtp_playback:{target_ms:80,max_target_ms:120,capacity_ms:200,max_age_ms:200}}});
 expect(requests[1]).not.toHaveProperty('project_id');
 await page.getByRole('button',{name:'Reset to inherited profile'}).click();
 await expect.poll(()=>requests.length).toBe(3);
 expect(requests[2].reset).toBe(true);expect(requests[2]).not.toHaveProperty('config');
});
test('validation errors remain visible without silently changing the profile',async({page})=>{
 await page.route('**/profile',async(route)=>{const body=route.request().postDataJSON();if(body.config){await route.fulfill({status:400,json:{error:'Local residence budget must cover capacity'}})}else await route.fulfill({json:{effective:{rtp_playback:sipPreset('balanced')}}})});
 await page.goto('/');await page.locator('summary').click();await expect(page.getByRole('button',{name:'Save audio profile'})).toBeEnabled();
 await page.getByLabel('Local residence budget (ms)',{exact:true}).fill('120');
 await page.getByRole('button',{name:'Save audio profile'}).click();
 await expect(page.getByRole('alert')).toHaveText('Local residence budget must cover capacity');
 await expect(page.getByLabel('Local residence budget (ms)',{exact:true})).toHaveValue('120');
});
