import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { cameraBox, FIT_CAMERA, panCamera, zoomCamera, type Camera } from "./camera";

type Props = {
 stageRef: React.RefObject<SVGSVGElement|null>; hostRef: React.RefObject<HTMLDivElement|null>;
 width:number; height:number; sceneKey:string; children:React.ReactNode; status:string;
};
type Contact = {x:number;y:number};
export default function SceneViewport({stageRef,hostRef,width,height,sceneKey,children,status}:Props){
 const [state,setState]=useState({key:sceneKey,camera:FIT_CAMERA});
 const [dragging,setDragging]=useState(false);
 const camera=state.key===sceneKey?state.camera:FIT_CAMERA;
 const current=useRef({camera,width,height,sceneKey});current.current={camera,width,height,sceneKey};
 const contacts=useRef(new Map<number,Contact>()),start=useRef<Contact|null>(null),moved=useRef(false),suppressClick=useRef(0);
 const change=(fn:(previous:Camera)=>Camera)=>setState(previous=>({key:current.current.sceneKey,camera:fn(previous.key===current.current.sceneKey?previous.camera:FIT_CAMERA)}));
 const reset=()=>change(()=>FIT_CAMERA);
 // Changing viewBox moves the actual scene, including keyboard hit targets and
 // exports. Nested village SVGs retain their own geometry and motion clocks.
 useLayoutEffect(()=>{
  const svg=stageRef.current;if(!svg)return;
  const box=cameraBox(camera,width,height);if(svg.getAttribute("viewBox")!==box)svg.setAttribute("viewBox",box);
 });
 const metrics=()=>{
  const box=stageRef.current?.getBoundingClientRect()||hostRef.current!.getBoundingClientRect();
  const scale=Math.min(box.width/current.current.width,box.height/current.current.height)||1;
  const w=current.current.width*scale,h=current.current.height*scale;
  return {width:w,height:h,left:box.left+(box.width-w)/2,top:box.top+(box.height-h)/2};
 };
 const anchor=(x:number,y:number)=>{const box=metrics();return {x:(x-box.left)/box.width,y:(y-box.top)/box.height};};
 useEffect(()=>{
  const host=hostRef.current;if(!host)return;
  const wheel=(event:WheelEvent)=>{
   if((event.target as Element).closest(".aw-camera-controls"))return;
   event.preventDefault();
   const box=metrics(),unit=event.deltaMode===1?16:event.deltaMode===2?box.height:1;
   if(event.shiftKey)change(camera=>panCamera(camera,-(event.deltaX||event.deltaY)*unit/box.width,0));
   else change(camera=>zoomCamera(camera,Math.exp(-Math.max(-250,Math.min(250,event.deltaY*unit))*(event.ctrlKey ? .012 : .003)),anchor(event.clientX,event.clientY)));
  };
  host.addEventListener("wheel",wheel,{passive:false});return()=>host.removeEventListener("wheel",wheel);
 },[]);
 const pointerDown=(event:React.PointerEvent<HTMLDivElement>)=>{
  suppressClick.current=0;
  if(event.button!==0||(event.target as Element).closest(".aw-camera-controls"))return;
  if(!contacts.current.size){start.current={x:event.clientX,y:event.clientY};moved.current=false;}
  contacts.current.set(event.pointerId,{x:event.clientX,y:event.clientY});
  if(contacts.current.size>1){moved.current=true;setDragging(true);for(const id of contacts.current.keys())event.currentTarget.setPointerCapture?.(id);}
 };
 const pointerMove=(event:React.PointerEvent<HTMLDivElement>)=>{
  const old=contacts.current.get(event.pointerId);if(!old)return;
  const next={x:event.clientX,y:event.clientY};
  const other=[...contacts.current.entries()].find(([id])=>id!==event.pointerId)?.[1];
  contacts.current.set(event.pointerId,next);
  if(other){
   event.preventDefault();const before=Math.hypot(old.x-other.x,old.y-other.y),after=Math.hypot(next.x-other.x,next.y-other.y);
   if(before>2&&after>2){const box=metrics();change(camera=>panCamera(zoomCamera(camera,after/before,anchor((old.x+other.x)/2,(old.y+other.y)/2)),(next.x-old.x)/2/box.width,(next.y-old.y)/2/box.height));}
   return;
  }
  if(!moved.current&&start.current&&Math.hypot(next.x-start.current.x,next.y-start.current.y)>5){moved.current=true;setDragging(true);event.currentTarget.setPointerCapture?.(event.pointerId);}
  if(moved.current){event.preventDefault();const box=metrics();change(camera=>panCamera(camera,(next.x-old.x)/box.width,(next.y-old.y)/box.height));}
 };
 const pointerUp=(event:React.PointerEvent<HTMLDivElement>)=>{
  if(moved.current)suppressClick.current=Date.now()+250;
  contacts.current.delete(event.pointerId);
  if(event.currentTarget.hasPointerCapture?.(event.pointerId))event.currentTarget.releasePointerCapture(event.pointerId);
  if(!contacts.current.size){setDragging(false);start.current=null;}
 };
 const keyboard=(event:React.KeyboardEvent<HTMLDivElement>)=>{
  if((event.target as Element).closest(".aw-camera-controls"))return;
  if(["+","="].includes(event.key))change(camera=>zoomCamera(camera,1.25));
  else if(event.key==="-")change(camera=>zoomCamera(camera,.8));
  else if(["0","Home"].includes(event.key))reset();
  else if(event.key.startsWith("Arrow"))change(camera=>panCamera(camera,event.key==="ArrowLeft" ? .08 :event.key==="ArrowRight"?-.08:0,event.key==="ArrowUp" ? .08 :event.key==="ArrowDown"?-.08:0));
  else return;
  event.preventDefault();
 };
 return <div ref={hostRef} className={`aw-canvas${dragging?" is-dragging":""}`} role="region" aria-label="Interactive visualization" tabIndex={0}
  data-camera-zoom={camera.zoom} onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={pointerUp} onPointerCancel={pointerUp} onLostPointerCapture={pointerUp}
  onKeyDown={keyboard} onClickCapture={event=>{if(!(event.target as Element).closest(".aw-camera-controls")&&Date.now()<suppressClick.current){event.preventDefault();event.stopPropagation();suppressClick.current=0;}}}>
  {children}
  <div className="aw-canvas-status" aria-live="off">{status}</div>
  <div className="aw-camera-hint">Scroll to zoom · Drag to pan</div>
  <div className="aw-camera-controls" aria-label="Camera controls" onPointerDown={event=>event.stopPropagation()}>
   <button type="button" aria-label="Zoom out visualization" disabled={camera.zoom<=1} onClick={()=>change(camera=>zoomCamera(camera,.8))}>−</button>
   <button type="button" aria-label="Fit visualization" title="Reset camera (0)" onClick={reset}>{Math.round(camera.zoom*100)}%</button>
   <button type="button" aria-label="Zoom in visualization" disabled={camera.zoom>=8} onClick={()=>change(camera=>zoomCamera(camera,1.25))}>+</button>
  </div>
 </div>;
}
