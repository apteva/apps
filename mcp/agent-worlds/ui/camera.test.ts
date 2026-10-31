import {expect,test} from 'bun:test';
import {FIT_CAMERA,zoomCamera,panCamera,cameraBox,frameCamera} from './camera';
test('zoom keeps the world point under the pointer fixed',()=>{
 const anchor={x:.7,y:.3};const next=zoomCamera(FIT_CAMERA,2,anchor);
 expect(next.x+(anchor.x-.5)/next.zoom).toBeCloseTo(anchor.x);
 expect(next.y+(anchor.y-.5)/next.zoom).toBeCloseTo(anchor.y);
 const box=cameraBox(next,1280,760).split(' ').map(Number);
 expect(box[0]).toBeCloseTo(448);expect(box[1]).toBeCloseTo(114);expect(box.slice(2)).toEqual([640,380]);
});
test('pan and zoom stay within the world and fit resets all bounds',()=>{
 const zoomed=zoomCamera(FIT_CAMERA,100);expect(zoomed.zoom).toBe(8);
 const edge=panCamera(zoomed,100,-100);expect(edge.x).toBe(.0625);expect(edge.y).toBe(.9375);
 expect(zoomCamera(edge,.001)).toEqual(FIT_CAMERA);
 expect(panCamera(FIT_CAMERA,1,1)).toEqual(FIT_CAMERA);
 expect(cameraBox(FIT_CAMERA,540,960)).toBe('0 0 540 960');
});
test('framing a box centres it and zooms until it fills the view',()=>{
 const framed=frameCamera({x:.3,y:.25,w:.4,h:.5},1);
 expect(framed).toEqual({zoom:2,x:.5,y:.5});
 expect(frameCamera({x:0,y:0,w:1,h:1})).toEqual(FIT_CAMERA);
});
