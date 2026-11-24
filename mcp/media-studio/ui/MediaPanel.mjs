import{useCallback as E6,useEffect as g,useId as L0,useRef as n,useState as O}from"react";import{forwardRef as Y7,createElement as W7}from"react";var k8=(...$)=>$.filter((Z,Y,Q)=>{return Boolean(Z)&&Z.trim()!==""&&Q.indexOf(Z)===Y}).join(" ").trim();var h1=($)=>$.replace(/([a-z0-9])([A-Z])/g,"$1-$2").toLowerCase();var R1=($)=>$.replace(/^([A-Z])|[\s-_]+(\w)/g,(Z,Y,Q)=>Q?Q.toUpperCase():Y.toLowerCase());var O0=($)=>{let Z=R1($);return Z.charAt(0).toUpperCase()+Z.slice(1)};import{forwardRef as Z7,createElement as C1}from"react";var L8={xmlns:"http://www.w3.org/2000/svg",width:24,height:24,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:2,strokeLinecap:"round",strokeLinejoin:"round"};var y1=($)=>{for(let Z in $)if(Z.startsWith("aria-")||Z==="role"||Z==="title")return!0;return!1};import{createContext as e5,useContext as $7,useMemo as K9,createElement as T9}from"react";var X7=e5({});var P1=()=>$7(X7);var M1=Z7(({color:$,size:Z,strokeWidth:Y,absoluteStrokeWidth:Q,className:H="",children:J,iconNode:z,..._},V)=>{let{size:k=24,strokeWidth:K=2,absoluteStrokeWidth:P=!1,color:C="currentColor",className:R=""}=P1()??{},D=Q??P?Number(Y??K)*24/Number(Z??k):Y??K;return C1("svg",{ref:V,...L8,width:Z??k??L8.width,height:Z??k??L8.height,stroke:$??C,strokeWidth:D,className:k8("lucide",R,H),...!J&&!y1(_)&&{"aria-hidden":"true"},..._},[...z.map(([F,L])=>C1(F,L)),...Array.isArray(J)?J:[J]])});var v=($,Z)=>{let Y=Y7(({className:Q,...H},J)=>W7(M1,{ref:J,iconNode:Z,className:k8(`lucide-${h1(O0($))}`,`lucide-${$}`,Q),...H}));return Y.displayName=O0($),Y};var Q7=[["path",{d:"M2 10v3",key:"1fnikh"}],["path",{d:"M6 6v11",key:"11sgs0"}],["path",{d:"M10 3v18",key:"yhl04a"}],["path",{d:"M14 8v7",key:"3a1oy3"}],["path",{d:"M18 5v13",key:"123xd1"}],["path",{d:"M22 10v3",key:"154ddg"}]],n6=v("audio-lines",Q7);var J7=[["path",{d:"M15 3h6v6",key:"1q9fwt"}],["path",{d:"m21 3-7 7",key:"1l2asr"}],["path",{d:"m3 21 7-7",key:"tjx5ai"}],["path",{d:"M9 21H3v-6",key:"wtvkvv"}]],a6=v("maximize-2",J7);var B7=[["rect",{x:"14",y:"3",width:"5",height:"18",rx:"1",key:"kaeet6"}],["rect",{x:"5",y:"3",width:"5",height:"18",rx:"1",key:"1wsw3u"}]],r6=v("pause",B7);var H7=[["path",{d:"M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z",key:"10ikf1"}]],I6=v("play",H7);var q7=[["path",{d:"M5 12h14",key:"1ays0h"}],["path",{d:"M12 5v14",key:"s699le"}]],i6=v("plus",q7);var U7=[["path",{d:"M12 3v12",key:"1x0j5s"}],["path",{d:"m17 8-5-5-5 5",key:"7q97r8"}],["path",{d:"M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4",key:"ih7n3h"}]],o6=v("upload",U7);var G7=[["path",{d:"M11 4.702a.705.705 0 0 0-1.203-.498L6.413 7.587A1.4 1.4 0 0 1 5.416 8H3a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2.416a1.4 1.4 0 0 1 .997.413l3.383 3.384A.705.705 0 0 0 11 19.298z",key:"uqj9uw"}],["path",{d:"M16 9a5 5 0 0 1 0 6",key:"1q6k2b"}],["path",{d:"M19.364 18.364a9 9 0 0 0 0-12.728",key:"ijwkga"}]],s6=v("volume-2",G7);var O7=[["path",{d:"M11 4.702a.705.705 0 0 0-1.203-.498L6.413 7.587A1.4 1.4 0 0 1 5.416 8H3a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2.416a1.4 1.4 0 0 1 .997.413l3.383 3.384A.705.705 0 0 0 11 19.298z",key:"uqj9uw"}],["line",{x1:"22",x2:"16",y1:"9",y2:"15",key:"1ewh16"}],["line",{x1:"16",x2:"22",y1:"9",y2:"15",key:"5ykzw1"}]],t6=v("volume-x",O7);var z7=[["path",{d:"M18 6 6 18",key:"1bl5f8"}],["path",{d:"m6 6 12 12",key:"d8bk6v"}]],e6=v("x",z7);var I1="jpeg";var _7={video:{min:1,max:60},audio_sfx:{min:1,max:30},music:{min:3,max:300}};function S1($,Z){let Y=_7[$];if(!Number.isFinite(Z))return Y.min;return Math.min(Y.max,Math.max(Y.min,Z))}function S6($){let Z=$.indexOf(":");return Z>0?$.slice(0,Z):""}function f1($,Z,Y){return $||S6(Z)||Y}function x1($,Z,Y,Q,H){let J={safe_mode:H};if(!$||$==="openai-api"||$==="openai-codex"){if(Z!=="dall-e-2")J.quality=Y;if(Z.startsWith("gpt-image"))J.output_format=Q}else if($==="venice-ai")J.output_format=Q;return J}function g1($,Z){return`/api/apps/storage/files/${encodeURIComponent($)}/content?project_id=${encodeURIComponent(Z)}`}function j1($,Z){let Y=String($||"").toLowerCase(),Q=Z.toLowerCase();return Y==="image-to-video"||Q.includes("image-to-video")||Q.includes("reference-to-video")}function $8($){return $.toLowerCase().includes("reference-to-video")}function z0($,Z="reference"){let Y=$.toLowerCase();if(Y.startsWith("kling-"))return Z==="identity"?4:7;if(Y.startsWith("grok-imagine-"))return 7;return 9}function v1($,Z,Y){if(!$8($)||Y<=0)return[];let Q=$.toLowerCase();if(Q.startsWith("kling-")&&Z==="identity")return[{token:"@Element1",label:"Subject"}];return Array.from({length:Y},(H,J)=>{let z=J+1;if(Q.startsWith("happyhorse-1-0-"))return{token:`character${z}`,label:`Reference ${z}`};if(Q.startsWith("happyhorse-"))return{token:`[Image ${z}]`,label:`Reference ${z}`};return{token:`@Image${z}`,label:Z==="identity"?`Identity photo ${z}`:`Reference ${z}`}})}function m1($,Z,Y,Q){let H=Math.max(0,Math.min(Y,$.length)),J=Math.max(H,Math.min(Q,$.length)),z=$.slice(0,H),_=$.slice(J),V=z.length>0&&!/\s$/.test(z)?" ":"",k=_.length>0&&!/^\s/.test(_)?" ":"",K=V+Z+k;return{value:z+K+_,cursor:z.length+V.length+Z.length}}function u1($,Z,Y){if(Y.length===0)return{};if($8($))return{reference_groups:[{role:Z,images:Y}]};return{source_image:Y[0]}}function _0($,Z){return!Z||!!$?.length}function E8($){return $!=="deepgram"}function A8($){if($==="deepgram")return["mp3","wav","opus","flac","aac"];if($==="fish-audio")return["mp3","wav","opus","pcm"];if($==="cartesia")return["mp3","wav","pcm"];if($==="minimax-audio")return["mp3","wav","flac","pcm"];return[]}function f6($){return $==="elevenlabs"||$==="minimax-audio"}function h8($){return $==="elevenlabs"||$==="fish-audio"||$==="cartesia"||$==="minimax-audio"}function p1($,Z){return Z.filter((Y)=>(Y.provider||S6(Y.id)||$)===$)}function l1($,Z,Y){let Q=[...Z,...Y];return!Q.includes($)&&(!!$||Q.length>0)}function N0($){if(!Number.isFinite($)||$<0)return"0:00";let Z=Math.floor($),Y=Math.floor(Z/3600),Q=Math.floor(Z%3600/60),H=Z%60;if(Y>0)return`${Y}:${String(Q).padStart(2,"0")}:${String(H).padStart(2,"0")}`;return`${Q}:${String(H).padStart(2,"0")}`}function t($,Z){return $===Z}function c1($,Z,Y){if(!Y)return Z;let Q=new Set($.map((H)=>H.id));return[...$,...Z.filter((H)=>!Q.has(H.id))]}function d1($){let Z=$.trim().toLowerCase();return Z.startsWith("storage:")||Z.startsWith("http://")||Z.startsWith("https://")}function n1($,Z,Y,Q){return $===Z&&Y===Q}function R8($,Z){let Y=`${Z}/`;if(!$.type.startsWith(Y))return`Choose a valid ${Z} file.`;let Q=Z==="image"?20971520:26214400;if($.size>Q)return`${Z==="image"?"Image":"Audio"} files must be ${Q/1048576} MB or smaller.`;return""}var y8={"heygen-avatar-6":{label:"Avatar 6",resolutions:["1080p","2k"],motionPrompt:!0,maxMotionPromptChars:1200,expressiveness:"engine",requiresAdvertised:!0},"heygen-avatar-6-turbo":{label:"Avatar 6 Turbo",resolutions:["1080p","2k"],motionPrompt:!0,maxMotionPromptChars:1200,expressiveness:"engine",requiresAdvertised:!0},avatar_v:{label:"Avatar V",motionPrompt:!1,expressiveness:"",requiresAdvertised:!0},avatar_iv:{label:"Avatar IV",motionPrompt:!0,expressiveness:"top",requiresAdvertised:!1},avatar_iii:{label:"Avatar III",motionPrompt:!1,expressiveness:"",requiresAdvertised:!1}},N7=["1080p","720p","2k","4k"],b1=["1080p","720p","4k"];function P8($){if($==="auto")return"Auto";return y8[$]?.label||$}function V0($){let Z=["auto"],Y=$?.supported_api_engines||[];if($&&Y.length>0){for(let Q of Y)if(Q&&!Z.includes(Q))Z.push(Q);return Z}for(let[Q,H]of Object.entries(y8)){if($&&H.requiresAdvertised)continue;Z.push(Q)}return Z}function a1($){if($==="auto")return b1;let Z=y8[$];if(!Z)return N7;return Z.resolutions||b1}function r1($){return $==="2k"||$==="4k"?$.toUpperCase():$}function C8($){if($==="auto")return{prompt:!1,expressiveness:""};let Z=y8[$];if(!Z)return{prompt:!0,expressiveness:"top"};return{prompt:Z.motionPrompt,maxChars:Z.maxMotionPromptChars,expressiveness:Z.expressiveness}}function F0($){if($==="engine")return["default","less"];if($==="top")return["high","medium","low"];return[]}function w0($,Z){if($==="auto"||!Z)return!1;return!V0(Z).includes($)}function i1($,Z,Y){let Q={};if($==="auto")return Q;Q.engine={type:$};let H=C8($),J=Z.trim();if(H.prompt&&J)Q.motion_prompt=J;if(Y&&F0(H.expressiveness).includes(Y))if(H.expressiveness==="engine")Q.motion_expressiveness=Y;else Q.expressiveness=Y;return Q}function o1($,Z,Y,Q){if($<=0)return 0;let H=Q.toLowerCase();if(Z==="heygen-avatar-6-turbo")return H==="1080p"?$*0.05:0;if(!["auto","avatar_iii","avatar_iv","avatar_v"].includes(Z))return 0;let J=Y||"photo_avatar",z=J==="studio_avatar"||J==="digital_twin"?0.0667:0.05;if(H==="4k")z=J==="photo_avatar"?0.0667:0.0833;return $*z}function D0($){if(!$)return 0;let Z=$.trim();if(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(Z))Z=Z.replace(" ","T")+"Z";let Y=Date.parse(Z);return Number.isFinite(Y)?Y:0}import{jsx as X,jsxs as W,Fragment as Z6}from"react/jsx-runtime";function V7($,Z,Y){let Q=n(Y);Q.current=Y,g(()=>{if(!$||!Z)return;let H=(P)=>Q.current(P),J=window.__aptevaAppEvents;if(J)return J.subscribe($,Z,H);let z=0,_=null,V=!1,k=null,K=()=>{if(V)return;let P=`/api/app-events/${encodeURIComponent($)}?project_id=${encodeURIComponent(Z)}`+(z>0?`&since=${z}`:"");_=new EventSource(P,{withCredentials:!0}),_.onmessage=(C)=>{try{let R=JSON.parse(C.data);if(R.seq<=z)return;z=R.seq,Q.current(R)}catch{}},_.onerror=()=>{if(_&&_.readyState===EventSource.CLOSED){if(k)window.clearTimeout(k);k=window.setTimeout(K,2000)}}};return K(),()=>{if(V=!0,k)window.clearTimeout(k);if(_)_.close()}},[$,Z])}function a($){if(!$||$<=0)return"";if($>=0.01)return"$"+$.toFixed(2);if($>=0.001)return"$"+$.toFixed(4);return"$"+$.toFixed(6)}var p="/api/apps/media-studio",F7=24,s1={image:"Images",video:"Videos",audio_tts:"Audio",music:"Music",avatar:"Avatar"},K0={image:"image",video:"video",audio_tts:"voiceover",audio_sfx:"sound effect",music:"music track",avatar:"avatar video"},w7={"gpt-image-2":"GPT Image 2 (current)","gpt-image-1.5":"GPT Image 1.5","gpt-image-1":"GPT Image 1","gpt-image-1-mini":"GPT Image 1 Mini","dall-e-3":"DALL·E 3 (legacy)","dall-e-2":"DALL·E 2 (legacy)"},D7=["gpt-image-2","gpt-image-1.5","gpt-image-1","gpt-image-1-mini","dall-e-3","dall-e-2"],X5={"gpt-image-2":["1024x1024","1024x1536","1536x1024","2048x2048","3840x2160"],"gpt-image-1.5":["1024x1024","1024x1536","1536x1024"],"gpt-image-1":["1024x1024","1024x1536","1536x1024"],"gpt-image-1-mini":["1024x1024","1024x1536","1536x1024"],"dall-e-3":["1024x1024","1792x1024","1024x1792"],"dall-e-2":["256x256","512x512","1024x1024"]},Z5=["auto","low","medium","high"],Y5=["standard","hd"];function W5($){return $.startsWith("gpt-image")}function K7($){let Z=[$.label||$.id];if($.model_type==="image-to-video")Z.push("img→vid");if($.size_modes&&$.size_modes.length>0)Z.push($.size_modes.join("+"));let Y=a($.price_usd||0);if(Y)Z.push(Y);return Z.join(" · ")}function X8($){if($==="fish-audio")return"Fish Audio";if($==="elevenlabs")return"ElevenLabs";if($==="deepgram")return"Deepgram";if($==="cartesia")return"Cartesia";if($==="minimax-audio")return"MiniMax";return $}var T7=["firered-image-edit","qwen-edit","grok-imagine-edit","flux-2-max-edit","gpt-image-2-edit"],k7={"firered-image-edit":3,"qwen-edit":3,"grok-imagine-edit":3,"flux-2-max-edit":3,"gpt-image-2-edit":3};function L7(){return W("svg",{"aria-hidden":"true",width:"14",height:"14",viewBox:"0 0 16 16",fill:"none",stroke:"currentColor",strokeWidth:"1.5",children:[X("rect",{x:"1.5",y:"2.5",width:"13",height:"11",rx:"1"}),X("circle",{cx:"5.5",cy:"6",r:"1"}),X("path",{d:"M2 12l3.5-3.5 3 3L11 7l3 3"})]})}function E7(){return W("svg",{"aria-hidden":"true",width:"14",height:"14",viewBox:"0 0 16 16",fill:"none",stroke:"currentColor",strokeWidth:"1.5",children:[X("rect",{x:"1.5",y:"3.5",width:"10",height:"9",rx:"1"}),X("path",{d:"M11.5 7l3-2v6l-3-2z"})]})}function A7(){return W("svg",{"aria-hidden":"true",width:"14",height:"14",viewBox:"0 0 16 16",fill:"none",stroke:"currentColor",strokeWidth:"1.5",children:[X("path",{d:"M3 6v4h2l3 2.5v-9L5 6H3z"}),X("path",{d:"M10 5.5a3 3 0 010 5"}),X("path",{d:"M12 3.5a6 6 0 010 9"})]})}function h7(){return W("svg",{"aria-hidden":"true",width:"14",height:"14",viewBox:"0 0 16 16",fill:"none",stroke:"currentColor",strokeWidth:"1.5",children:[X("path",{d:"M6 12V3l7-1.5v9"}),X("circle",{cx:"4.5",cy:"12",r:"1.5"}),X("circle",{cx:"11.5",cy:"10.5",r:"1.5"})]})}function E0($){if($.storage_urls&&$.storage_urls.length>0)return $.storage_urls[0];if($.local_cache_url)return $.local_cache_url;if($.thumbnail_b64)return`data:image/jpeg;base64,${$.thumbnail_b64}`;return""}function R7({projectId:$}){let[Z,Y]=O("image"),[Q,H]=O("audio_tts"),J=Z==="audio"?Q:Z,[z,_]=O([]),[V,k]=O(""),[K,P]=O(!1),[C,R]=O(!1),[D,F]=O(!1),[L,w]=O(null),[I,G]=O(""),[j,A]=O(!1),[x,T]=O(null),[Y6,W6]=O(!1),[Y8,g6]=O(null),[m,s]=O(null),[B6,A6]=O(null),[f8,F6]=O(""),[Q6,b]=O(null);g(()=>{b((B)=>B&&B.kind!==J?null:B)},[J]);let[$6,l]=O(null),[h6,H6]=O(!1),[c,x8]=O(""),[X6,h0]=O("gpt-image-2"),[W8,R0]=O("1024x1024"),[Q8,y0]=O("1K"),[j6,g8]=O("auto"),[J8,U5]=O(I1),[P0,G5]=O({video:5,audio_sfx:5,music:30}),[R6,j8]=O("16:9"),[v8,O5]=O(!1),[B8,C0]=O("720p"),[w6,z5]=O("identity"),[O6,H8]=O(""),[d,M0]=O(""),[z6,m8]=O(""),[D6,b0]=O(""),[K6,I0]=O(""),[q8,_5]=O(!1),[S0,N5]=O([]),[T6,f0]=O(""),[u8,V5]=O("avatar_v"),[p8,F5]=O("1080p"),[l8,w5]=O("16:9"),[c8,D5]=O(""),[d8,K5]=O(""),[n8,T5]=O(null),[k5,L5]=O([]),[E5,A5]=O(!1),[x0,g0]=O(""),[q6,j0]=O("photo"),[U8,v0]=O(""),[a8,m0]=O(""),[u0,p0]=O(!1),[r8,h5]=O([]),[i8,R5]=O([]),[y5,l0]=O(!1),[c0,d0]=O(""),[n0,P5]=O(""),[a0,C5]=O("prompt"),[r0,i0]=O(""),[o0,M5]=O("eleven_ttv_v3"),[y6,s0]=O(""),[P6,b5]=O(!0),[o8,t0]=O(""),[s8,e0]=O(""),[t8,$1]=O(""),[X1,Z1]=O("en"),[Y1,W1]=O(!1),[_6,Q1]=O("mp3"),[v6,I5]=O(1),[r,e8]=O([]),[N6,J1]=O("firered-image-edit"),J6=J==="image"&&r.length>0,[G8,O8]=O(null),[B1,m6]=O(null),[H1,$0]=O(""),[S5,X0]=O(!1),[f5,z8]=O(!1),[x5,_8]=O([]),[q1,N8]=O(null),[g5,V8]=O(!1),[j5,v5]=O({image:"",video:"",audio_tts:"",audio_sfx:"",music:"",avatar:""}),[m5,Z0]=O([]),i=n(J),U1=n(c),Y0=n(null),u6=n(0);i.current=J,U1.current=c;let p6=J==="video"||J==="audio_sfx"||J==="music"?J:null,C6=p6?P0[p6]:P0.video,G1=(B)=>{if(!p6)return;G5((U)=>({...U,[p6]:S1(p6,B)}))},k6=j5[J]||"",u5=(B)=>v5((U)=>({...U,[J]:B})),O1=E6(async()=>{try{let B=await fetch(`${p}/bindings?project_id=${encodeURIComponent($)}`,{credentials:"same-origin"});if(!B.ok)return;let U=await B.json();w(U)}catch{}},[$]),U6=E6(async(B="")=>{let U=J,q=++u6.current,N=B!=="";if(N)F(!0);else R(!0);try{let h=new URLSearchParams({project_id:$,kind:J,limit:String(F7)});if(B)h.set("cursor",B);let E=await fetch(`${p}/generations?${h.toString()}`,{credentials:"same-origin"});if(q!==u6.current||!t(U,i.current))return;if(!E.ok){G(`Error: ${E.status}`);return}let M=await E.json();if(q!==u6.current||!t(U,i.current))return;let S=Array.isArray(M.generations)?M.generations:[];_((f)=>c1(f,S,N)),k(String(M.next_cursor||"")),P(Boolean(M.has_more)),G("")}catch(h){if(q===u6.current&&t(U,i.current))G("Error: "+h.message)}finally{if(q===u6.current&&t(U,i.current))R(!1),F(!1)}},[$,J]),l6=E6(async()=>{try{let B=await fetch(`${p}/avatars?project_id=${encodeURIComponent($)}`,{credentials:"same-origin"});if(!B.ok)return;let U=await B.json(),q=Array.isArray(U.avatars)?U.avatars:[];if(N5(q),q.length>0&&!q.some((N)=>N.id===T6))f0(q[0].id)}catch{}},[$,T6]),W0=E6(async()=>{try{let B=await fetch(`${p}/avatar-create-jobs?project_id=${encodeURIComponent($)}`,{credentials:"same-origin"});if(!B.ok)return[];let U=await B.json(),q=Array.isArray(U.jobs)?U.jobs:[];return L5(q),q}catch{return[]}},[$]),z1=E6(async()=>{try{let B=await fetch(`${p}/avatar-capabilities?project_id=${encodeURIComponent($)}`,{credentials:"same-origin"});if(!B.ok)return;let U=await B.json();T5({bound:!!U.bound,provider:U.provider||"",source_types:Array.isArray(U.source_types)?U.source_types:[],notes:U.notes||""})}catch{}},[$]),Q0=E6(async()=>{try{let B=await fetch(`${p}/identities?project_id=${encodeURIComponent($)}&kind=voice`,{credentials:"same-origin"});if(!B.ok)return;let U=await B.json();R5(Array.isArray(U.identities)?U.identities:[])}catch{}},[$]),J0=E6(async()=>{let B=J;try{let U=await fetch(`${p}/voices?project_id=${encodeURIComponent($)}&kind=${encodeURIComponent(J)}`,{credentials:"same-origin"});if(!t(B,i.current))return;if(!U.ok)return;let q=await U.json();if(!t(B,i.current))return;let N=Array.isArray(q.voices)?q.voices:[];h5(N)}catch{}},[J,$]);g(()=>{O1()},[O1]),g(()=>{_([]),k(""),P(!1),U6()},[U6]),g(()=>{if(!L?.storage?.bound){Z0([]);return}let B=!1,U=window.setTimeout(()=>{let q=new URLSearchParams({project_id:$,folder:"/",recursive:"true",limit:"240"});if(k6.trim())q.set("q",k6.trim());fetch(`${p}/storage-files?${q.toString()}`,{credentials:"same-origin"}).then((N)=>N.ok?N.json():null).then((N)=>{if(B||!N)return;Z0(t7(N.files||[]))}).catch(()=>{if(!B)Z0([])})},250);return()=>{B=!0,window.clearTimeout(U)}},[L?.storage?.bound,$,k6]),g(()=>{let B=n8?.source_types||[];if(B.length>0&&!B.includes(q6)){let U=B.find((q)=>q==="photo"||q==="prompt"||q==="video");if(U)j0(U)}},[n8,q6]),g(()=>{if(J!=="video"&&J!=="avatar")return;let B=!1,U=new Set,q=()=>{fetch(`${p}/video-jobs?project_id=${encodeURIComponent($)}`,{credentials:"same-origin"}).then((h)=>h.ok?h.json():null).then((h)=>{if(B||!h)return;let E=Array.isArray(h.jobs)?h.jobs:[];_8(E);let M=new Set(E.filter((f)=>f.status==="queued"||f.status==="polling"||f.status==="finalizing").map((f)=>f.id)),S=!1;for(let f of U)if(!M.has(f))S=!0;if(S)U6();U=M}).catch(()=>{})};q();let N=window.setInterval(q,5000);return()=>{B=!0,window.clearInterval(N)}},[J,$,U6]),g(()=>{if(!I.startsWith("Error")&&!I.startsWith("Delete failed"))return;let B=I,U=window.setTimeout(()=>{G((q)=>q===B?"":q)},12000);return()=>window.clearTimeout(U)},[I]),g(()=>{if(J!=="avatar")return;let B=!1,U=new Set,q=async()=>{let h=await W0();if(B)return;let E=new Set(h.filter((S)=>S.status==="queued"||S.status==="training").map((S)=>S.id)),M=!1;for(let S of U)if(!E.has(S))M=!0;if(M)l6();U=E};z1(),q();let N=window.setInterval(q,5000);return()=>{B=!0,window.clearInterval(N)}},[J,z1,W0,l6]),g(()=>{if(J!=="avatar"&&J!=="audio_tts")return;if(J==="avatar")l6();if(J0(),J==="audio_tts")Q0()},[J,L,l6,Q0,J0,$]),g(()=>{if(!L?.[J]?.slug){O8(null),m6(null),$0(""),X0(!1),z8(!1);return}let U=!1;if(O8(null),$0(""),X0(!0),fetch(`${p}/models?project_id=${encodeURIComponent($)}&kind=${encodeURIComponent(J)}`,{credentials:"same-origin"}).then((q)=>q.ok?q.json():null).then((q)=>{if(U||!q)return;if(Array.isArray(q.models)){if(O8(q.models),$0(String(q.provider||"")),q.models.length>0){if(J==="image"){if(!q.models.some((h)=>h.id===X6))h0(q.models[0].id)}else if(J==="video"){if(!q.models.some((h)=>h.id===d))M0(q.models[0].id)}else if(J==="audio_tts"){if(!q.models.some((h)=>h.id===z6))m8(q.models[0].id)}else if(J==="audio_sfx"){if(!q.models.some((h)=>h.id===D6))b0(q.models[0].id)}else if(J==="music"){if(!q.models.some((h)=>h.id===K6))I0(q.models[0].id)}}}}).catch(()=>{if(!U)O8(null)}).finally(()=>{if(!U)X0(!1)}),J==="image")m6(null),z8(!0),fetch(`${p}/models?project_id=${encodeURIComponent($)}&kind=image&capability=image.edit`,{credentials:"same-origin"}).then((q)=>q.ok?q.json():null).then((q)=>{if(U||!q)return;let N=Array.isArray(q.models)?q.models:[];if(m6(N),N.length>0&&!N.some((h)=>h.id===N6))J1(N[0].id)}).catch(()=>{if(!U)m6(null)}).finally(()=>{if(!U)z8(!1)});else m6(null),z8(!1);return()=>{U=!0}},[J,L,$]),V7("media-studio",$,(B)=>{if(B.topic==="media.generated"){if(B.data?.kind===J)U6()}});let F8=L?L[J]:null,M6=!!F8?.bound,_1=J==="image"?J6?N6:X6:J==="video"?d:J==="audio_tts"?z6:J==="audio_sfx"?D6:J==="music"?K6:"",y=(J==="image"&&J6?B1:G8)?.find((B)=>B.id===_1),c6=f1(y?.provider,X6,H1),w8=L?.audio_tts.providers?.length?L.audio_tts.providers:L?.audio_tts.slug?[{connection_id:0,slug:L.audio_tts.slug,default:!0}]:[],N1=w8.filter((B)=>h8(B.slug)).length,o=y?.provider||S6(z6)||w8[0]?.slug||"";g(()=>{if(J!=="audio_tts"||!o)return;let B=r8.filter((q)=>!q.provider||q.provider===o),U=i8.filter((q)=>q.status==="ready"&&q.provider===o).map((q)=>N1>1?`${q.provider}:${q.provider_identity_id}`:q.provider_identity_id);if(l1(O6,B.map((q)=>q.id),U))H8(B[0]?.id||U[0]||"")},[J,o,i8,N1,r8,O6]),g(()=>{let B=A8(o);if(B.length>0&&!B.includes(_6))Q1(B[0])},[o,_6]);let B0=Array.from(c).length,D8=y?.prompt_char_limit||(J==="video"&&F8?.slug==="venice-ai"?2500:0),K8=D8>0&&B0>D8,V1=J==="image"?J6?N6:X6:J==="video"?d:J==="audio_tts"?z6:J==="audio_sfx"?D6:J==="music"?K6:T6,F1=J==="video"||J==="avatar"?x5.filter((B)=>(B.kind||"video")===J&&(B.status==="queued"||B.status==="polling"||B.status==="finalizing")):[],w1=J==="video"&&j1(y?.model_type,d),T8=J==="video"&&(!!y?.supports_image_to_video||w1),D1=J==="video"&&$8(d),K1=w1&&r.length===0,T1=r.some((B)=>!d1(B.value)),d6=J==="image"?y?.max_source_images||k7[N6]||1:1,H0=J==="video"&&T8?$8(d)?Math.min(y?.max_source_images||z0(d,w6),z0(d,w6)):y?.max_source_images||1:1,q0=J==="image"?d6:H0,p5=D1?v1(d,w6,r.length):[],U0=(B,U)=>{let q=B.trim();if(!q)return;e8((N)=>{return[...N.filter((E)=>E.value!==q),{value:q,label:U}].slice(0,q0)})},l5=(B)=>{e8((U)=>U.filter((q,N)=>N!==B))},c5=(B)=>{let U=Y0.current,q=U?.selectionStart??c.length,N=U?.selectionEnd??q,h=m1(c,B,q,N);x8(h.value),window.requestAnimationFrame(()=>{let E=Y0.current;if(!E)return;E.focus(),E.setSelectionRange(h.cursor,h.cursor)})};g(()=>{if(!y)return;if(J==="image"&&!J6){let B=y.pixel_sizes&&y.pixel_sizes.length>0?y.pixel_sizes:X5[X6]||[];if(B.length>0&&!B.includes(W8))R0(B[0]);if(y.resolutions&&y.resolutions.length>0&&!y.resolutions.includes(Q8))y0(y.default_resolution||y.resolutions[0]);if(y.aspect_ratios&&y.aspect_ratios.length>0&&!y.aspect_ratios.includes(R6))j8(y.default_aspect_ratio||y.aspect_ratios[0]);if(!c6||c6==="openai-api"||c6==="openai-codex"){if(W5(X6)&&!Z5.includes(j6))g8("auto");else if(X6==="dall-e-3"&&!Y5.includes(j6))g8("standard")}}if(y.aspect_ratios&&y.aspect_ratios.length>0&&!y.aspect_ratios.includes(R6))j8(y.default_aspect_ratio||y.aspect_ratios[0]);if(J==="video"&&y.resolutions&&y.resolutions.length>0&&!y.resolutions.includes(B8))C0(y.default_resolution||y.resolutions[0]);if(y.durations&&y.durations.length>0){let B=`${C6}s`;if(!y.durations.includes(B)){let U=y.durations[0],q=parseInt(U.replace(/[^\d]/g,""),10);if(!isNaN(q))G1(q)}}},[_1,J,J6]);let d5=async()=>{if(u0)return;let B="avatar",U=(N)=>{if(t(B,i.current))G(N)},q=x0.trim();if(!q){G("Avatar name required.");return}if(q6==="photo"&&!U8.trim()){G("Photo avatar source required.");return}if(q6==="prompt"&&!a8.trim()){G("Prompt avatar text required.");return}p0(!0),G("Creating avatar…");try{let N={project_id:$,name:q,source_type:q6};if(q6==="photo")N.source_image=U8.trim();if(q6==="prompt")N.prompt=a8.trim();if(q6==="video")N.source_video=U8.trim();let h=await fetch(`${p}/avatar-create?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(N)}),E=await h.text();if(!h.ok){U(`Error ${h.status}: ${E.slice(0,300)}`);return}let M={};try{M=JSON.parse(E)}catch{}if(M.isError){let S=M.content?.find((f)=>f.type==="text")?.text||"avatar creation failed";U(`Error: ${S}`);return}U(M._meta?.status==="completed"?"Avatar created.":"Avatar creation started."),g0(""),v0(""),m0(""),await W0(),await l6()}catch(N){U("Error: "+N.message)}finally{p0(!1)}},n5=async()=>{if(Y1)return;let B="audio_tts",U=(S)=>{if(t(B,i.current))G(S)},q=c0.trim(),N=r0.trim(),h=w8.filter((S)=>h8(S.slug)),E=n0||h.find((S)=>S.slug===o)?.slug||h[0]?.slug,M=f6(E)?a0:"audio";if(!q){G("Voice name required.");return}if(!E){G("Voice provider required.");return}if(M==="prompt"&&N.length<20){G("Voice description must be at least 20 characters.");return}if(M==="audio"&&!o8.trim()){G("Voice audio sample required.");return}W1(!0),G("Creating voice…");try{let S=M==="prompt"?E==="minimax-audio"?{preview_text:y6.trim()||"Hello, this is a preview of my new voice."}:{model_id:o0,auto_generate_text:!y6.trim(),should_enhance:P6,quality:0.9,loudness:0.5,output_format:"mp3_44100_128"}:E==="fish-audio"?{visibility:"private",enhance_audio_quality:P6,generate_sample:!0}:E==="minimax-audio"?{need_noise_reduction:P6,need_volume_normalization:P6,language_boost:"auto"}:E==="elevenlabs"?{remove_background_noise:P6}:{};if(M==="prompt"&&E==="elevenlabs"&&y6.trim())S.text=y6.trim();let f={project_id:$,kind:"voice",provider:E,name:q,source_type:M,voice_description:N,preview_text:y6.trim(),language:X1.trim()||"en",labels:{created_in:"media-studio"},options:S};if(M==="audio"){if(f.source_audio=o8.trim(),s8.trim())f.source_audio_filename=s8.trim();if(t8.trim())f.transcripts=[t8.trim()]}let u=await fetch(`${p}/identity-create?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(f)}),V6=await u.text();if(!u.ok){U(`Error ${u.status}: ${V6.slice(0,300)}`);return}let G6={};try{G6=JSON.parse(V6)}catch{}if(G6.isError){let G0=G6.content?.find((t5)=>t5.type==="text")?.text||"voice creation failed";U(`Error: ${G0}`);return}let L6=G6._meta?.provider_identity_id||G6._meta?.identity?.provider_identity_id||"",b6=L6&&h.length>1?`${E}:${L6}`:L6,A1=G8?.find((G0)=>G0.provider===E);if(A1)m8(A1.id);if(b6)H8(b6);if(d0(""),i0(""),s0(""),t0(""),e0(""),$1(""),Z1("en"),l0(!1),U("Voice created."),await Q0(),await J0(),b6)H8(b6)}catch(S){U("Error: "+S.message)}finally{W1(!1)}},k1=()=>{let B={kind:J,prompt:c,project_id:$};if(k6.trim())B.storage_folder=k6.trim();if(J==="image")if(J6){if(r.length>d6)return G(`Error: ${N6} supports at most ${d6} source image${d6===1?"":"s"}.`),null;if(B.model=N6,r.length===1)B.source_image=r[0].value;else B.source_images=r.map((U)=>U.value);B.options={output_format:J8,safe_mode:q8}}else{B.model=X6;let U=!!y?.aspect_ratios?.length,q=!!y?.resolutions?.length;if(!U||!!y?.pixel_sizes?.length)B.size=W8;if(U)B.aspect=R6;if(q)B.resolution=Q8;B.options=x1(c6,X6,j6,J8,q8)}else if(J==="video"){if(d)B.model=d;if(B.duration=C6,_0(y?.aspect_ratios,!!y))B.aspect=R6;let U={};if(y?.resolutions?.length)U.resolution=B8;if(v8)U.audio=!1;if(Object.keys(U).length>0)B.options=U;if(T8&&r.length>0){if(r.length>H0)return G(`Error: ${d} supports at most ${H0} ${w6==="identity"?"identity":"reference"} images.`),null;Object.assign(B,u1(d,w6,r.map((q)=>q.value)))}}else if(J==="audio_tts"){if(z6)B.model=z6;if(O6&&E8(o))B.voice=O6;if(o==="fish-audio")B.options={output_format:_6,prosody:{speed:v6,normalize_loudness:!0}};else if(o==="deepgram")B.options={output_format:_6};else if(o==="cartesia")B.options={output_format:_6,generation_config:{speed:v6}};else if(o==="minimax-audio")B.options={output_format:_6,voice_setting:{speed:v6}}}else if(J==="audio_sfx"){if(D6)B.model=D6;B.duration=C6}else if(J==="music"){if(K6)B.model=K6;B.duration=C6}else if(J==="avatar"){if(B.avatar=T6,O6)B.voice=O6;if(F8?.slug==="heygen"){let U=S0.find((N)=>N.id===T6),q={resolution:p8,aspect:l8,estimated_duration_seconds:B5(c)};if(Object.assign(q,i1(u8,c8,d8)),U?.avatar_type)q.avatar_type=U.avatar_type;B.options=q}}return B};g(()=>{if(!c.trim()||!M6||K8){N8(null),V8(!1);return}let B=k1();if(!B){N8(null),V8(!1);return}let U=!1;V8(!0);let q=window.setTimeout(()=>{fetch(`${p}/estimate?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(B)}).then((N)=>N.ok?N.json():null).then((N)=>{if(U)return;N8(N||null)}).catch(()=>{if(!U)N8(null)}).finally(()=>{if(!U)V8(!1)})},350);return()=>{U=!0,window.clearTimeout(q)}},[$,c,K8,M6,J,J6,X6,N6,W8,Q8,j6,J8,q8,d,C6,R6,v8,B8,w6,z6,_6,v6,o,D6,K6,O6,T6,u8,p8,l8,c8,d8,k6,r]);let L1=async(B)=>{if(!c.trim()||j||Y6)return!1;if(K8)return G(`Error: prompt is ${B0} characters; this model allows ${D8}.`),!1;if(K1)return G("Error: the selected video model requires at least one source image."),!1;if(B==="draft"&&T1)return G("Error: drafts require Storage references or HTTP(S) source URLs."),!1;let U=J,q=c,N=(E)=>{if(t(U,i.current))G(E)},h=()=>{if(n1(U,i.current,q,U1.current))x8("")};if(B==="generate")A(!0),T({kind:U,prompt:q,model:V1,costUSD:q1?.cost_usd||0}),G("Generating…");else W6(!0),G("Saving draft…");try{let E=k1();if(!E)return!1;if(B==="draft")E.mode="draft";let M=await fetch(`${p}/generate?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(E)}),S=await M.text();if(!M.ok)return N(`Error ${M.status}: ${S.slice(0,300)}`),!1;let f={};try{f=JSON.parse(S)}catch{}if(f.isError){let V6=f.content?.find((G6)=>G6.type==="text")?.text||"generation failed";return N(`Error: ${V6}`),!1}let u=f._meta;if(u?.status==="draft"){h();let V6=u.cost_usd?` · est. ${a(u.cost_usd)}`:"";return N(`Draft saved.${V6}`),U6(),!0}if(u?.status==="queued"){let V6=q;h();let G6=u.cost_usd?` · est. ${a(u.cost_usd)}`:"";if(N(`Generating…${G6}`),u.job_id&&(U==="video"||U==="avatar"))_8((L6)=>{if(L6.some((b6)=>b6.id===u.job_id))return L6;return[{id:u.job_id||0,queue_id:"",model:V1,prompt:V6,status:"queued",error:"",generation_id:u.generation_id||0,cost_usd:u.cost_usd||0},...L6]});return!0}return h(),N("Done."),U6(),!0}catch(E){return N("Error: "+E.message),!1}finally{A(!1),T(null),W6(!1)}},a5=async()=>{if(await L1("generate"))H6(!1)},r5=async()=>{if(await L1("draft"))H6(!1)},i5=async(B)=>{if(Y8||B.status!=="draft")return;let U=B.kind,q=(N)=>{if(t(U,i.current))G(N)};g6(B.id),G("Generating draft…");try{let N=await fetch(`${p}/generate?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({generation_id:B.id,project_id:$})}),h=await N.text();if(!N.ok){q(`Error ${N.status}: ${h.slice(0,300)}`);return}let E={};try{E=JSON.parse(h)}catch{}if(E.isError){let S=E.content?.find((f)=>f.type==="text")?.text||"generation failed";q(`Error: ${S}`);return}let M=E._meta;if(M?.status==="queued"){let S=M.cost_usd?` · est. ${a(M.cost_usd)}`:"";if(q(`Generating…${S}`),M.job_id&&(B.kind==="video"||B.kind==="avatar"))_8((f)=>{if(f.some((u)=>u.id===M.job_id))return f;return[{id:M.job_id||0,queue_id:"",model:B.model,prompt:B.prompt,status:"queued",error:"",generation_id:M.generation_id||B.id,cost_usd:M.cost_usd||B.cost_usd||0},...f]})}else q("Done.");U6()}catch(N){q("Error: "+N.message)}finally{g6(null)}},o5=(B)=>{if(m)return;F6(""),A6(B)},s5=async(B)=>{if(m)return;let U=B.kind,q=(N)=>{if(t(U,i.current))G(N)};s(B.id),F6("");try{let N=await fetch(`${p}/delete?project_id=${encodeURIComponent($)}`,{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({id:B.id,delete_storage:!0})}),h=await N.json().catch(()=>({}));if(!N.ok||h.error){F6(String(h.error||`The server returned ${N.status}.`));return}A6(null),_((E)=>E.filter((M)=>M.id!==B.id)),b((E)=>E?.id===B.id?null:E),l((E)=>E?.id===B.id?null:E),_8((E)=>E.filter((M)=>M.generation_id!==B.id)),q("Deleted.")}catch(N){F6(N.message||"Network error.")}finally{s(null)}},E1=`${z.length}${K?"+":""} generation${z.length===1&&!K?"":"s"}`;return W("div",{className:"h-full flex flex-col",children:[X("style",{children:`
        .ms-media-main { flex-direction: row; overflow: hidden; }
        .ms-media-content { padding: 12px; }
        .ms-media-detail { width: 384px; border-left-width: 1px; border-left-style: solid; }
        .ms-kind-tabs { scrollbar-width: none; }
        .ms-kind-tabs::-webkit-scrollbar { display: none; }
        .ms-detail-header { display: flex; flex-direction: column; gap: 10px; padding: 14px 16px 12px; }
        .ms-detail-title {
          flex: 1;
          min-width: 0;
          margin: 0;
          font-size: 14px;
          line-height: 1.4;
          display: -webkit-box;
          -webkit-line-clamp: 2;
          -webkit-box-orient: vertical;
          overflow: hidden;
          overflow-wrap: anywhere;
        }
        .ms-detail-close {
          flex-shrink: 0;
          display: inline-flex;
          align-items: center;
          justify-content: center;
          width: 28px;
          height: 28px;
          margin: -4px -6px 0 0;
          border-radius: 6px;
        }
        .ms-detail-close:hover { background: var(--bg-input); }
        .ms-detail-request summary { cursor: pointer; padding: 8px 10px; user-select: none; }
        .ms-detail-request[open] summary { border-bottom: 1px solid var(--border); }
        .ms-detail-request pre {
          margin: 0;
          padding: 10px;
          max-height: 260px;
          overflow: auto;
          font-size: 11px;
          line-height: 1.5;
          white-space: pre-wrap;
          overflow-wrap: anywhere;
        }
        .ms-btn-icon { display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
        .ms-btn-danger {
          color: var(--error);
          background: transparent;
          border: 1px solid color-mix(in srgb, var(--error) 45%, transparent);
          transition: background-color 120ms ease, border-color 120ms ease;
        }
        .ms-btn-danger:hover:not(:disabled) {
          background: color-mix(in srgb, var(--error) 12%, transparent);
          border-color: var(--error);
        }
        .ms-btn-danger:disabled { opacity: .5; }
        .ms-btn-danger-solid {
          color: #fff;
          background: var(--error);
          border: 1px solid var(--error);
          transition: filter 120ms ease;
        }
        .ms-btn-danger-solid:hover:not(:disabled) { filter: brightness(1.1); }
        .ms-btn-danger-solid:disabled { opacity: .7; cursor: progress; }
        .ms-btn-danger:focus-visible, .ms-btn-danger-solid:focus-visible {
          outline: 2px solid color-mix(in srgb, var(--error) 60%, transparent);
          outline-offset: 2px;
        }
        .ms-confirm-backdrop {
          position: fixed;
          inset: 0;
          z-index: 10000;
          display: flex;
          align-items: center;
          justify-content: center;
          padding: 24px;
          background: rgba(0, 0, 0, 0.72);
        }
        .ms-confirm-dialog {
          width: min(460px, 100%);
          border: 1px solid var(--border);
          background: var(--bg-card);
          box-shadow: 0 24px 64px rgba(0, 0, 0, 0.45);
          overflow: hidden;
        }
        .ms-confirm-body { display: flex; gap: 14px; padding: 20px 20px 18px; }
        .ms-confirm-body h2 { margin: 2px 0 12px; }
        .ms-confirm-icon {
          flex-shrink: 0;
          display: flex;
          align-items: center;
          justify-content: center;
          width: 36px;
          height: 36px;
          border-radius: 999px;
          color: var(--error);
          background: color-mix(in srgb, var(--error) 14%, transparent);
        }
        .ms-confirm-item { display: flex; align-items: center; gap: 10px; padding: 8px; margin-bottom: 12px; }
        .ms-confirm-thumb {
          flex-shrink: 0;
          width: 48px;
          height: 48px;
          border-radius: 4px;
          object-fit: cover;
          background: var(--bg);
        }
        .ms-confirm-thumb-icon { display: flex; align-items: center; justify-content: center; }
        .ms-confirm-prompt {
          margin: 0 0 2px;
          display: -webkit-box;
          -webkit-line-clamp: 2;
          -webkit-box-orient: vertical;
          overflow: hidden;
          overflow-wrap: anywhere;
          line-height: 1.35;
        }
        .ms-confirm-error {
          margin-top: 12px;
          padding: 8px 10px;
          color: var(--error);
          background: color-mix(in srgb, var(--error) 10%, transparent);
          border: 1px solid color-mix(in srgb, var(--error) 35%, transparent);
        }
        .ms-confirm-actions { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 20px; background: var(--bg); }
        .ms-confirm-spinner {
          width: 13px;
          height: 13px;
          border-radius: 999px;
          border: 2px solid rgba(255, 255, 255, 0.4);
          border-top-color: #fff;
          animation: ms-confirm-spin 0.8s linear infinite;
        }
        @keyframes ms-confirm-spin { to { transform: rotate(360deg); } }
        .ms-tile-media {
          display: flex;
          align-items: center;
          justify-content: center;
          width: 100%;
          aspect-ratio: 1 / 1;
          overflow: hidden;
        }
        .ms-tile-media img { width: 100%; height: 100%; object-fit: contain; display: block; }
        .ms-tile-empty { flex-direction: column; gap: 8px; font-size: 12px; }
        .ms-tile-failed { color: var(--error); }
        .ms-card-selected { box-shadow: 0 0 0 1px var(--accent); }
        .ms-media-card.ms-card-selected { border-color: var(--accent); }
        .ms-create-backdrop {
          position: fixed;
          inset: 0;
          z-index: 9998;
          display: flex;
          align-items: center;
          justify-content: center;
          padding: 24px;
          background: rgba(0, 0, 0, 0.72);
        }
        .ms-create-dialog {
          width: min(960px, calc(100vw - 48px));
          max-height: min(820px, calc(100vh - 48px));
          display: flex;
          flex-direction: column;
          overflow: hidden;
        }
        .ms-create-body { overflow: auto; padding: 20px; }
        .ms-create-body > * + * { margin-top: 16px; }
        .ms-composer { display: flex; flex-direction: column; gap: 20px; }
        .ms-prompt-input { min-height: 156px; line-height: 1.5; }
        .ms-composer-options {
          display: grid;
          grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
          gap: 14px;
          align-items: end;
        }
        .ms-composer-options select,
        .ms-composer-options input[type="text"],
        .ms-composer-options input[type="number"] { width: 100%; min-width: 0 !important; }
        .ms-option-wide { grid-column: 1 / -1; }
        .ms-gen-card {
          align-self: start;
          display: flex;
          flex-direction: column;
          border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--border));
          background: var(--bg-card);
          animation: ms-gen-enter 260ms ease-out both;
        }
        .ms-gen-stage {
          position: relative;
          width: 100%;
          overflow: hidden;
          background: var(--bg-input);
          display: flex;
          align-items: center;
          justify-content: center;
        }
        .ms-gen-stage-square { aspect-ratio: 1 / 1; }
        .ms-gen-stage-wide { aspect-ratio: 16 / 10; }
        .ms-gen-stage-short { min-height: 140px; }
        .ms-gen-glow {
          position: absolute;
          inset: -20%;
          background: radial-gradient(circle at 50% 50%, color-mix(in srgb, var(--accent) 26%, transparent) 0%, transparent 55%);
          animation: ms-gen-pulse 2.4s ease-in-out infinite;
        }
        .ms-gen-shimmer {
          position: absolute;
          inset: 0;
          background: linear-gradient(110deg, transparent 30%, color-mix(in srgb, var(--text, #fff) 7%, transparent) 50%, transparent 70%);
          background-size: 250% 100%;
          animation: ms-gen-sweep 1.8s linear infinite;
        }
        .ms-gen-center {
          position: relative;
          display: flex;
          flex-direction: column;
          align-items: center;
          gap: 12px;
        }
        .ms-gen-dots { display: flex; gap: 7px; }
        .ms-gen-dots span {
          width: 9px;
          height: 9px;
          border-radius: 999px;
          background: var(--accent);
          animation: ms-gen-bounce 1.2s ease-in-out infinite;
        }
        .ms-gen-dots span:nth-child(2) { animation-delay: 0.15s; }
        .ms-gen-dots span:nth-child(3) { animation-delay: 0.3s; }
        .ms-gen-label {
          display: flex;
          align-items: baseline;
          gap: 8px;
          font-size: 13px;
          color: var(--text);
        }
        .ms-gen-time { font-size: 11px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
        .ms-gen-progress {
          position: absolute;
          left: 0;
          right: 0;
          bottom: 0;
          height: 3px;
          overflow: hidden;
          background: color-mix(in srgb, var(--accent) 15%, transparent);
        }
        .ms-gen-progress-fill {
          height: 100%;
          background: var(--accent);
          transition: width 1s linear;
        }
        .ms-gen-progress-indeterminate {
          height: 100%;
          width: 35%;
          background: var(--accent);
          animation: ms-gen-slide 1.6s ease-in-out infinite;
        }
        .ms-gen-meta { padding: 8px; display: flex; flex-direction: column; gap: 4px; min-width: 0; }
        .ms-gen-prompt {
          font-size: 12px;
          color: var(--text);
          display: -webkit-box;
          -webkit-line-clamp: 2;
          -webkit-box-orient: vertical;
          overflow: hidden;
        }
        .ms-gen-facts {
          display: flex;
          justify-content: space-between;
          gap: 8px;
          font-size: 10px;
          color: var(--text-dim);
          min-width: 0;
        }
        .ms-gen-cost { color: var(--accent); flex-shrink: 0; }
        @keyframes ms-gen-enter { from { opacity: 0; transform: translateY(6px) scale(0.98); } to { opacity: 1; transform: none; } }
        @keyframes ms-gen-pulse { 0%, 100% { opacity: 0.45; transform: scale(0.92); } 50% { opacity: 1; transform: scale(1.05); } }
        @keyframes ms-gen-sweep { from { background-position: 150% 0; } to { background-position: -100% 0; } }
        @keyframes ms-gen-bounce { 0%, 80%, 100% { transform: translateY(0); opacity: 0.4; } 40% { transform: translateY(-7px); opacity: 1; } }
        @keyframes ms-gen-slide { from { transform: translateX(-110%); } to { transform: translateX(310%); } }
        @media (prefers-reduced-motion: reduce) {
          .ms-gen-card, .ms-gen-glow, .ms-gen-shimmer, .ms-gen-dots span, .ms-gen-progress-indeterminate { animation: none; }
          .ms-gen-dots span { opacity: 0.8; }
        }
        .ms-option-check { min-height: 34px; }
        .ms-tts-settings {
          grid-column: 1 / -1;
          display: flex;
          flex-direction: column;
          gap: 16px;
          min-width: 0;
        }
        .ms-tts-controls {
          display: grid;
          grid-template-columns: minmax(240px, 1.6fr) minmax(140px, 0.8fr) minmax(120px, 0.6fr);
          gap: 14px;
          align-items: end;
        }
        .ms-tts-controls > div { min-width: 0; }
        .ms-tts-controls select,
        .ms-tts-controls input { width: 100%; max-width: none !important; }
        .ms-voice-picker {
          min-width: 0;
          padding-top: 14px;
          border-top: 1px solid var(--border);
        }
        .ms-voice-picker-header {
          display: flex;
          align-items: center;
          justify-content: space-between;
          gap: 12px;
          margin-bottom: 7px;
        }
        .ms-voice-picker-row {
          display: grid;
          grid-template-columns: minmax(0, 1fr) auto;
          gap: 8px;
          align-items: stretch;
        }
        .ms-voice-picker-row select { width: 100%; min-width: 0; min-height: 40px; }
        .ms-provider-label {
          display: inline-flex;
          align-items: center;
          min-height: 20px;
          padding: 1px 6px;
          border: 1px solid var(--border);
          border-radius: 4px;
          color: var(--text-muted);
          font-size: 10px;
          font-weight: 600;
        }
        .ms-voice-backdrop {
          position: fixed;
          inset: 0;
          z-index: 10010;
          display: flex;
          align-items: center;
          justify-content: center;
          padding: 24px;
          background: rgba(0, 0, 0, 0.78);
        }
        .ms-voice-dialog {
          width: min(720px, calc(100vw - 48px));
          max-height: min(760px, calc(100vh - 48px));
          display: flex;
          flex-direction: column;
          overflow: hidden;
          border: 1px solid var(--border);
          border-radius: 6px;
          background: var(--bg-card);
          box-shadow: 0 24px 80px rgba(0, 0, 0, 0.45);
        }
        .ms-voice-dialog-body {
          display: flex;
          flex-direction: column;
          gap: 18px;
          overflow: auto;
          padding: 20px;
        }
        .ms-voice-form-grid {
          display: grid;
          grid-template-columns: repeat(2, minmax(0, 1fr));
          gap: 14px;
          align-items: end;
        }
        .ms-voice-form-grid > div { min-width: 0; }
        .ms-voice-form-grid select,
        .ms-voice-form-grid input,
        .ms-voice-form-grid textarea { width: 100%; min-width: 0; }
        .ms-voice-mode {
          display: inline-grid;
          grid-auto-flow: column;
          grid-auto-columns: 1fr;
          padding: 3px;
          border: 1px solid var(--border);
          border-radius: 6px;
          background: var(--bg-input);
        }
        .ms-voice-mode button {
          min-height: 34px;
          padding: 5px 12px;
          border-radius: 4px;
          color: var(--text-muted);
          font-size: 12px;
          font-weight: 600;
        }
        .ms-voice-mode button[data-active="true"] {
          background: var(--accent);
          color: var(--bg);
        }
        .ms-voice-dialog-footer {
          display: flex;
          align-items: center;
          justify-content: space-between;
          gap: 12px;
          padding: 14px 20px;
          border-top: 1px solid var(--border);
          background: var(--bg-card);
        }
        .ms-composer-footer {
          display: flex;
          align-items: center;
          justify-content: space-between;
          gap: 16px;
          padding-top: 16px;
          border-top-width: 1px;
          border-top-style: solid;
        }
        .ms-composer-actions { margin-left: auto; }
        .ms-gallery-grid {
          display: grid;
          gap: 10px;
          padding: 10px;
          align-items: start;
          align-content: start;
        }
        .ms-gallery-video {
          grid-template-columns: repeat(auto-fill, minmax(min(360px, 100%), 1fr));
        }
        .ms-gallery-audio {
          grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr));
        }
        .ms-media-card {
          min-width: 0;
          overflow: hidden;
          border: 1px solid var(--border);
          border-radius: 6px;
          background: var(--bg-card);
          transition: border-color 140ms ease, background-color 140ms ease;
        }
        .ms-media-card:hover { border-color: color-mix(in srgb, var(--accent) 55%, var(--border)); }
        .ms-video-player {
          position: relative;
          width: 100%;
          aspect-ratio: 16 / 10;
          overflow: hidden;
          background: #050505;
        }
        .ms-video-player video {
          display: block;
          width: 100%;
          height: 100%;
          object-fit: contain;
        }
        .ms-video-player.ms-player-lightbox {
          width: min(1100px, 92vw, calc(82vh * 1.6));
        }
        .ms-video-player:fullscreen,
        .ms-video-player:-webkit-full-screen {
          width: 100vw;
          height: 100vh;
          aspect-ratio: auto;
        }
        .ms-audio-player {
          display: grid;
          grid-template-columns: 42px minmax(0, 1fr);
          align-items: center;
          gap: 10px;
          min-height: 84px;
          padding: 12px;
          background: var(--bg-input);
        }
        .ms-audio-player.ms-player-lightbox {
          width: min(560px, calc(100vw - 32px));
          border: 1px solid var(--border);
          border-radius: 6px;
        }
        .ms-audio-mark {
          width: 42px;
          height: 42px;
          display: flex;
          align-items: center;
          justify-content: center;
          color: var(--accent);
          border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--border));
          border-radius: 6px;
          background: var(--bg-card);
        }
        .ms-audio-content { min-width: 0; }
        .ms-audio-kicker {
          margin-bottom: 6px;
          color: var(--text-dim);
          font-size: 10px;
          line-height: 1;
          text-transform: uppercase;
        }
        .ms-player-controls {
          display: grid;
          grid-template-columns: 32px minmax(56px, 1fr) auto 32px;
          align-items: center;
          gap: 8px;
          min-width: 0;
        }
        .ms-video-player .ms-player-controls {
          position: absolute;
          inset: auto 0 0;
          z-index: 2;
          padding: 8px 10px;
          color: #fff;
          background: rgba(0, 0, 0, 0.78);
          grid-template-columns: 32px minmax(56px, 1fr) auto 32px 32px;
        }
        .ms-player-button {
          width: 32px;
          height: 32px;
          display: inline-flex;
          align-items: center;
          justify-content: center;
          flex: 0 0 auto;
          border: 0;
          border-radius: 50%;
          color: inherit;
          background: transparent;
          cursor: pointer;
        }
        .ms-player-button:hover { color: var(--accent); background: rgba(255, 255, 255, 0.08); }
        .ms-player-button:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
        .ms-player-range {
          width: 100%;
          min-width: 0;
          margin: 0;
          accent-color: var(--accent);
          cursor: pointer;
        }
        .ms-player-time {
          min-width: 68px;
          color: var(--text-muted);
          font-size: 10px;
          font-variant-numeric: tabular-nums;
          text-align: right;
          white-space: nowrap;
        }
        .ms-video-player .ms-player-time { color: rgba(255, 255, 255, 0.82); }
        .ms-video-play-overlay {
          position: absolute;
          inset: 50% auto auto 50%;
          z-index: 2;
          width: 48px;
          height: 48px;
          transform: translate(-50%, -50%);
          display: flex;
          align-items: center;
          justify-content: center;
          border: 1px solid rgba(255, 255, 255, 0.28);
          border-radius: 50%;
          color: #fff;
          background: rgba(0, 0, 0, 0.72);
          cursor: pointer;
        }
        .ms-video-play-overlay:hover { color: var(--accent); border-color: var(--accent); }
        .ms-card-meta { min-height: 54px; }
        .ms-card-facts {
          display: grid;
          grid-template-columns: minmax(0, 1fr) auto;
          align-items: center;
          gap: 8px;
        }
        @media (max-width: 1023px) {
          .ms-media-main { flex-direction: column; overflow: auto; }
          .ms-media-content { padding: 12px; }
          .ms-media-detail {
            width: 100%;
            max-height: 45vh;
            border-left-width: 0;
            border-top-width: 1px;
            border-top-style: solid;
          }
        }
        @media (max-width: 640px) {
          .ms-media-detail {
            position: fixed;
            inset: 0;
            z-index: 9990;
            max-height: none;
            border-top-width: 0;
          }
          .ms-confirm-backdrop { align-items: flex-end; padding: 0; }
          .ms-confirm-dialog { width: 100%; border-radius: 12px 12px 0 0 !important; }
          .ms-confirm-actions { flex-direction: column-reverse; }
          .ms-confirm-actions button { width: 100%; min-height: 44px; }
          .ms-gallery-grid { gap: 8px; padding: 8px; }
          .ms-gallery-video, .ms-gallery-audio { grid-template-columns: minmax(0, 1fr); }
          .ms-create-backdrop { padding: 0; align-items: stretch; }
          .ms-create-dialog { width: 100%; max-height: none; height: 100%; border-radius: 0 !important; }
          .ms-create-body { padding: 16px; }
          .ms-composer-options { grid-template-columns: 1fr; }
          .ms-tts-controls,
          .ms-voice-form-grid { grid-template-columns: 1fr; }
          .ms-voice-picker-row { grid-template-columns: 1fr; }
          .ms-voice-picker-row button { width: 100%; justify-content: center; }
          .ms-voice-backdrop { padding: 0; align-items: stretch; }
          .ms-voice-dialog {
            width: 100%;
            max-height: none;
            height: 100%;
            border-radius: 0;
          }
          .ms-voice-dialog-body { padding: 16px; }
          .ms-voice-dialog-footer {
            position: sticky;
            bottom: 0;
            flex-direction: column;
            align-items: stretch;
            padding: 12px 16px;
          }
          .ms-voice-dialog-footer > * { width: 100%; }
          .ms-composer-footer {
            position: sticky;
            bottom: -16px;
            z-index: 2;
            align-items: stretch;
            flex-direction: column;
            margin-bottom: -16px;
            padding: 16px 0;
            background: var(--bg-card);
          }
          .ms-composer-actions { display: grid; grid-template-columns: 1fr 1fr; margin-left: 0; }
          .ms-composer-actions button { width: 100%; }
        }
      `}),X("nav",{className:"ms-kind-tabs flex items-center flex-shrink-0 overflow-x-auto border-b border-border px-2 sm:px-4",role:"tablist","aria-label":"Media type",children:Object.keys(s1).map((B)=>{let U=B==="audio_tts"?"audio":B,q=Z===U,h=L?L[B]?.bound:!1;return W("button",{onClick:()=>Y(U),role:"tab","aria-selected":q,"aria-controls":"media-studio-panel",className:"flex flex-shrink-0 items-center gap-1.5 px-3 py-2.5 text-sm border-b-2 transition-colors "+(q?"border-accent text-text":"border-transparent text-text-muted hover:text-text"),children:[X(Q5,{kind:B}),s1[B],X(J5,{bound:h})]},U)})}),Z==="audio"&&W("div",{className:"flex items-center gap-1 px-4 py-1.5 border-b border-border bg-bg-card",role:"tablist","aria-label":"Audio type",children:[X(t1,{label:"TTS",active:Q==="audio_tts",onClick:()=>H("audio_tts"),bound:!!L?.audio_tts.bound}),X(t1,{label:"SFX",active:Q==="audio_sfx",onClick:()=>H("audio_sfx"),bound:!!L?.audio_sfx.bound})]}),L&&!M6&&W("div",{className:"px-4 py-2 text-xs text-text-muted bg-bg-card border-b border-border",children:["No provider bound for ",X("strong",{className:"text-text",children:J}),". Open the app settings to pick one."]}),W("div",{className:"ms-media-main flex-1 flex min-h-0",children:[W("div",{id:"media-studio-panel",role:"tabpanel",className:"ms-media-content flex-1 flex flex-col gap-4 min-w-0 min-h-0",children:[W("div",{className:"flex items-center justify-between gap-3 flex-wrap",children:[W("div",{className:"min-w-0",children:[X("div",{className:"text-sm font-medium text-text",children:E1}),I&&I!==E1&&W("div",{role:I.startsWith("Error")||I.startsWith("Delete failed")?"alert":"status",className:"flex items-center gap-1 text-xs text-text-dim min-w-0",children:[X("span",{className:"truncate",title:I,children:I}),(I.startsWith("Error")||I.startsWith("Delete failed"))&&X("button",{type:"button",onClick:()=>G(""),className:"flex-shrink-0 px-1 text-text-muted hover:text-text","aria-label":"Dismiss error",title:"Dismiss",children:"×"})]})]}),W("button",{onClick:()=>{G(""),H6(!0)},disabled:!M6,className:"flex items-center gap-2 px-3 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50",title:M6?`Create a new ${K0[J]}`:"Bind a provider first",children:[X("span",{"aria-hidden":"true",className:"text-base leading-none",children:"+"}),"New ",K0[J]]})]}),h6&&W(y7,{title:J6?"Edit image":`Create ${K0[J]}`,onClose:()=>H6(!1),children:[(J==="image"||T8)&&X(d7,{projectId:$,sources:r,maxSources:q0,onAdd:U0,onRemove:l5,onClear:()=>e8([]),onError:G,referencePurpose:D1?w6:void 0,onReferencePurposeChange:z5,promptReferences:p5,onInsertPromptReference:c5,hint:T8?q0>1?"Reference images for this video model":"Source image for the image-to-video model (required)":void 0}),X(C7,{kind:J,prompt:c,setPrompt:x8,promptInputRef:Y0,promptLength:B0,promptLimit:D8,promptTooLong:K8,generate:a5,createDraft:r5,generating:j,creatingDraft:Y6,disabled:!M6||S5||J6&&f5,isEditMode:J6,liveModels:G8,liveProvider:H1,imageProvider:c6,imageModel:X6,setImageModel:h0,imageSize:W8,setImageSize:R0,imageResolution:Q8,setImageResolution:y0,imageQuality:j6,setImageQuality:g8,imageFormat:J8,setImageFormat:U5,editModel:N6,setEditModel:J1,editSourceLimit:d6,editModels:B1||G8?.filter((B)=>B.supports_image_edit)||[],videoModel:d,setVideoModel:M0,audioModel:z6,setAudioModel:m8,sfxModel:D6,setSfxModel:b0,musicModel:K6,setMusicModel:I0,currentModel:y,safeMode:q8,setSafeMode:_5,duration:C6,setDuration:G1,aspect:R6,setAspect:j8,videoNoSound:v8,setVideoNoSound:O5,videoResolution:B8,setVideoResolution:C0,voice:O6,setVoice:H8,avatars:S0,selectedAvatar:T6,setSelectedAvatar:f0,avatarCaps:n8,avatarCreateJobs:k5,avatarCreateOpen:E5,setAvatarCreateOpen:A5,avatarCreateName:x0,setAvatarCreateName:g0,avatarCreateSourceType:q6,setAvatarCreateSourceType:j0,avatarCreateSource:U8,setAvatarCreateSource:v0,avatarCreatePrompt:a8,setAvatarCreatePrompt:m0,avatarCreating:u0,createAvatar:d5,avatarProvider:F8?.slug||"",avatarEngine:u8,setAvatarEngine:V5,avatarResolution:p8,setAvatarResolution:F5,avatarAspect:l8,setAvatarAspect:w5,avatarMotionPrompt:c8,setAvatarMotionPrompt:D5,avatarExpressiveness:d8,setAvatarExpressiveness:K5,voices:r8,voiceIdentities:i8,voiceCreateOpen:y5,setVoiceCreateOpen:l0,voiceCreateName:c0,setVoiceCreateName:d0,voiceCreateProvider:n0,setVoiceCreateProvider:P5,voiceCreateSourceType:a0,setVoiceCreateSourceType:C5,voiceCreateDescription:r0,setVoiceCreateDescription:i0,voiceCreateModel:o0,setVoiceCreateModel:M5,voiceCreatePreviewText:y6,setVoiceCreatePreviewText:s0,voiceCreateEnhance:P6,setVoiceCreateEnhance:b5,voiceCreateAudio:o8,setVoiceCreateAudio:t0,voiceCreateAudioFilename:s8,setVoiceCreateAudioFilename:e0,voiceCreateTranscript:t8,setVoiceCreateTranscript:$1,voiceCreateLanguage:X1,setVoiceCreateLanguage:Z1,voiceCreating:Y1,createVoice:n5,onError:G,audioProviders:w8,audioFormat:_6,setAudioFormat:Q1,audioSpeed:v6,setAudioSpeed:I5,storageBound:!!L?.storage?.bound,storageFolder:k6,setStorageFolder:u5,storageFolderDefault:s7(J),folderSuggestions:m5,estimate:q1,estimateLoading:g5,sourceRequiredMissing:K1,draftBlocked:T1})]}),X("div",{className:"flex-1 overflow-auto border border-border rounded",children:z.length===0&&!(x&&x.kind===J)&&F1.length===0?X("div",{className:"py-12 px-6 text-center text-text-muted text-sm",children:C?"Loading…":I||"No generations yet for this kind."}):W(Z6,{children:[X(X9,{kind:J,items:z,selectedId:Q6?.id??null,onSelect:b,onOpenLightbox:l,onGenerateDraft:i5,generating:!!x&&x.kind===J,generatingPrompt:x?.prompt||"",generatingModel:x?.model||"",generatingCostUSD:x?.costUSD||0,pendingJobs:F1,generatingDraftId:Y8}),K&&V&&X("div",{className:"flex justify-center px-3 py-4 border-t border-border",children:X("button",{onClick:()=>U6(V),disabled:D,className:"px-4 py-2 text-sm border border-border rounded text-text hover:border-accent disabled:opacity-50",children:D?"Loading…":"Load more"})})]})})]}),Q6&&X(Y9,{selected:Q6,onClose:()=>b(null),deleting:m===Q6.id,onDelete:()=>o5(Q6),onUseAsReference:Q6.kind==="image"&&Q6.storage_ids.length>0?()=>{let B=Q6.storage_ids[0];U0(`storage:${B}`,`Storage #${B}`),b(null),Y("image"),H6(!0)}:void 0})]}),B6&&X(U9,{generation:B6,busy:m===B6.id,error:f8,onCancel:()=>{if(m)return;A6(null),F6("")},onConfirm:()=>s5(B6)}),$6&&X(Q9,{item:$6,onClose:()=>l(null),onUseAsReference:$6.kind==="image"&&$6.storage_ids.length>0?()=>{let B=$6.storage_ids[0];U0(`storage:${B}`,`Storage #${B}`),l(null),Y("image"),H6(!0)}:void 0})]})}function y7({title:$,onClose:Z,children:Y}){let Q=n(null),H=n(Z);return H.current=Z,g(()=>{let J=document.activeElement instanceof HTMLElement?document.activeElement:null,z=(_)=>{if(Q.current?.querySelector('[role="dialog"][aria-modal="true"]'))return;if(_.key==="Escape"){H.current();return}if(_.key!=="Tab"||!Q.current)return;let k=Array.from(Q.current.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]"));if(k.length===0)return;let K=k[0],P=k[k.length-1];if(_.shiftKey&&document.activeElement===K)_.preventDefault(),P.focus();else if(!_.shiftKey&&document.activeElement===P)_.preventDefault(),K.focus()};return window.addEventListener("keydown",z),Q.current?.querySelector("textarea")?.focus(),()=>{window.removeEventListener("keydown",z),J?.focus()}},[]),X("div",{className:"ms-create-backdrop",onMouseDown:Z,children:W("div",{ref:Q,role:"dialog","aria-modal":"true","aria-labelledby":"media-create-title",className:"ms-create-dialog border border-border rounded bg-bg-card",onMouseDown:(J)=>J.stopPropagation(),children:[W("header",{className:"flex items-center justify-between gap-4 px-5 py-4 border-b border-border flex-shrink-0",children:[X("h2",{id:"media-create-title",className:"text-base font-semibold text-text",children:$}),X("button",{onClick:Z,"aria-label":"Close creation dialog",className:"text-text-muted hover:text-text leading-none px-2 py-1",style:{fontSize:22},children:"×"})]}),X("div",{className:"ms-create-body",children:Y})]})})}function Q5({kind:$}){if($==="image")return X(L7,{});if($==="video")return X(E7,{});if($==="music")return X(h7,{});if($==="avatar")return X(P7,{});return X(A7,{})}function P7(){return W("svg",{"aria-hidden":"true",width:"14",height:"14",viewBox:"0 0 16 16",fill:"none",stroke:"currentColor",strokeWidth:"1.5",children:[X("circle",{cx:"8",cy:"5.5",r:"2.5"}),X("path",{d:"M3 13.5c0-2.8 2.2-4.5 5-4.5s5 1.7 5 4.5"})]})}function J5({bound:$}){return X("span",{"aria-hidden":"true",className:"rounded-full ml-1",style:{width:6,height:6,background:$?"var(--apteva-accent, #4ade80)":"var(--apteva-text-dim, #555)"}})}function t1({label:$,active:Z,bound:Y,onClick:Q}){return W("button",{onClick:Q,role:"tab","aria-selected":Z,className:"flex items-center gap-1.5 px-2.5 py-1 text-xs rounded transition-colors "+(Z?"bg-bg-input text-text":"text-text-muted hover:text-text"),children:[$,X(J5,{bound:Y})]})}function C7($){let Z=$.isEditMode?"Edit instruction — 'remove the tree', 'change sky to sunset'":$.kind==="avatar"?"Script the avatar will speak…":$.kind==="audio_tts"?"Text to speak":$.kind==="music"?"A jazzy lo-fi loop with piano":$.kind==="video"?"A cat walking through a sunlit garden":$.kind==="audio_sfx"?"A door creaking open":"a cat in a hat",Y=$.avatars.find((w)=>w.id===$.selectedAvatar),Q=$.currentModel?.provider||S6($.audioModel)||$.audioProviders[0]?.slug||"",H=$.audioProviders.filter((w)=>w.slug==="elevenlabs"||w.slug==="fish-audio"||w.slug==="cartesia"||w.slug==="minimax-audio"),J=H.some((w)=>w.slug===$.voiceCreateProvider)?$.voiceCreateProvider:H.some((w)=>w.slug===Q)?Q:H[0]?.slug||"",z=H.length>1,_=$.voiceIdentities.filter((w)=>w.status==="ready"&&w.provider_identity_id).map((w)=>({id:z?`${w.provider}:${w.provider_identity_id}`:w.provider_identity_id,name:w.name||w.provider_identity_id,provider:w.provider,preview:w.preview_url})),V=new Set($.voices.map((w)=>w.id)),k=[...$.voices,..._.filter((w)=>!V.has(w.id))],K=p1(Q,k),P=(w)=>{$.setVoice(w)},C=()=>{let w=h8(Q)?Q:J;if($.setVoiceCreateProvider(w),!f6(w))$.setVoiceCreateSourceType("audio");$.setVoiceCreateOpen(!0)},R=$.kind==="avatar"&&$.avatarProvider==="heygen"&&w0($.avatarEngine,Y),D=C8($.avatarEngine).maxChars,F=$.kind==="avatar"&&$.avatarProvider==="heygen"&&!!D&&$.avatarMotionPrompt.length>D,L=$.kind==="audio_tts"&&E8(Q)&&!$.voice;return W("div",{className:"ms-composer",children:[W("div",{children:[W("label",{className:"text-text-muted text-xs flex items-center justify-between gap-2",children:[X("span",{children:"Prompt"}),$.promptLimit>0&&W("span",{className:$.promptTooLong?"text-red":"text-text-dim",children:[$.promptLength,"/",$.promptLimit]})]}),X("textarea",{ref:$.promptInputRef,rows:7,value:$.prompt,onChange:(w)=>$.setPrompt(w.target.value),onKeyDown:(w)=>{if(w.key==="Enter"&&(w.metaKey||w.ctrlKey))$.generate()},placeholder:Z,autoFocus:!0,className:`ms-prompt-input w-full bg-bg-input border rounded px-3 py-2.5 text-sm resize-y ${$.promptTooLong?"border-red":"border-border"}`})]}),W("div",{className:"ms-composer-options",children:[$.kind==="image"&&$.isEditMode&&X(c7,{model:$.editModel,setModel:$.setEditModel,format:$.imageFormat,setFormat:$.setImageFormat,maxSources:$.editSourceLimit,liveModels:$.editModels,liveProvider:$.liveProvider}),$.kind==="image"&&!$.isEditMode&&X(n7,{model:$.imageModel,setModel:$.setImageModel,size:$.imageSize,setSize:$.setImageSize,imageResolution:$.imageResolution,setImageResolution:$.setImageResolution,quality:$.imageQuality,setQuality:$.setImageQuality,format:$.imageFormat,setFormat:$.setImageFormat,liveModels:$.liveModels,liveProvider:$.liveProvider,selectedProvider:$.imageProvider,currentModel:$.currentModel,aspect:$.aspect,setAspect:$.setAspect}),$.kind==="video"&&W(Z6,{children:[X(M8,{model:$.videoModel,setModel:$.setVideoModel,liveModels:$.liveModels,liveProvider:$.liveProvider}),X(i7,{durations:$.currentModel?.durations,value:$.duration,onChange:$.setDuration}),X(H5,{aspects:$.currentModel?.aspect_ratios,value:$.aspect,onChange:$.setAspect,disabledHint:$.currentModel&&!_0($.currentModel.aspect_ratios,!0)?$.currentModel.model_type==="image-to-video"?"Inherited from source image":"Not supported by this model":void 0}),$.currentModel?.resolutions&&$.currentModel.resolutions.length>0&&X(q5,{resolutions:$.currentModel.resolutions,value:$.videoResolution,onChange:$.setVideoResolution}),X(l7,{value:$.videoNoSound,onChange:$.setVideoNoSound,configurable:$.currentModel?.audio_configurable})]}),$.kind==="audio_tts"&&W("div",{className:"ms-tts-settings",children:[W("div",{className:"ms-tts-controls",children:[X(M8,{model:$.audioModel,setModel:$.setAudioModel,liveModels:$.liveModels,liveProvider:$.liveProvider,label:Q==="deepgram"?"Voice model":"Provider & model"}),A8(Q).length>0&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Format"}),X("select",{value:$.audioFormat,onChange:(w)=>$.setAudioFormat(w.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:A8(Q).map((w)=>X("option",{value:w,children:w.toUpperCase()},w))})]}),(Q==="fish-audio"||Q==="cartesia"||Q==="minimax-audio")&&X(I8,{label:"Speed",value:$.audioSpeed,onChange:$.setAudioSpeed,min:0.5,max:2,step:0.1})]}),E8(Q)&&X(a7,{voice:$.voice,provider:Q,voices:K,onChange:P,canCreate:H.length>0,onCreate:C}),H.length>0&&X(b7,{providers:H,provider:J,setProvider:$.setVoiceCreateProvider,sourceType:$.voiceCreateSourceType,setSourceType:$.setVoiceCreateSourceType,open:$.voiceCreateOpen,setOpen:$.setVoiceCreateOpen,name:$.voiceCreateName,setName:$.setVoiceCreateName,description:$.voiceCreateDescription,setDescription:$.setVoiceCreateDescription,model:$.voiceCreateModel,setModel:$.setVoiceCreateModel,previewText:$.voiceCreatePreviewText,setPreviewText:$.setVoiceCreatePreviewText,enhance:$.voiceCreateEnhance,setEnhance:$.setVoiceCreateEnhance,audio:$.voiceCreateAudio,setAudio:$.setVoiceCreateAudio,audioFilename:$.voiceCreateAudioFilename,setAudioFilename:$.setVoiceCreateAudioFilename,transcript:$.voiceCreateTranscript,setTranscript:$.setVoiceCreateTranscript,language:$.voiceCreateLanguage,setLanguage:$.setVoiceCreateLanguage,creating:$.voiceCreating,createVoice:$.createVoice,onError:$.onError})]}),$.kind==="audio_sfx"&&W(Z6,{children:[X(M8,{model:$.sfxModel,setModel:$.setSfxModel,liveModels:$.liveModels,liveProvider:$.liveProvider}),X(I8,{label:"Duration (s)",value:$.duration,onChange:$.setDuration,min:1,max:30})]}),$.kind==="music"&&W(Z6,{children:[X(M8,{model:$.musicModel,setModel:$.setMusicModel,liveModels:$.liveModels,liveProvider:$.liveProvider}),X(I8,{label:"Duration (s)",value:$.duration,onChange:$.setDuration,min:3,max:300})]}),$.kind==="avatar"&&X(I7,{provider:$.avatarProvider,caps:$.avatarCaps,jobs:$.avatarCreateJobs,open:$.avatarCreateOpen,setOpen:$.setAvatarCreateOpen,name:$.avatarCreateName,setName:$.setAvatarCreateName,sourceType:$.avatarCreateSourceType,setSourceType:$.setAvatarCreateSourceType,source:$.avatarCreateSource,setSource:$.setAvatarCreateSource,prompt:$.avatarCreatePrompt,setPrompt:$.setAvatarCreatePrompt,creating:$.avatarCreating,createAvatar:$.createAvatar,onError:$.onError}),$.kind==="avatar"&&X(f7,{avatars:$.avatars,selected:$.selectedAvatar,setSelected:$.setSelectedAvatar}),$.kind==="avatar"&&$.avatarProvider==="heygen"&&X(u7,{prompt:$.prompt,selectedAvatar:Y,engine:$.avatarEngine,setEngine:$.setAvatarEngine,resolution:$.avatarResolution,setResolution:$.setAvatarResolution,aspect:$.avatarAspect,setAspect:$.setAvatarAspect,motionPrompt:$.avatarMotionPrompt,setMotionPrompt:$.setAvatarMotionPrompt,expressiveness:$.avatarExpressiveness,setExpressiveness:$.setAvatarExpressiveness}),$.kind==="avatar"&&$.voices.length>0&&X(r7,{voice:$.voice,setVoice:$.setVoice,voices:$.voices}),$.kind==="image"&&X(p7,{value:$.safeMode,onChange:$.setSafeMode}),$.storageBound&&X(o7,{value:$.storageFolder,onChange:$.setStorageFolder,placeholder:$.storageFolderDefault,suggestions:$.folderSuggestions})]}),W("div",{className:"ms-composer-footer border-border",children:[X(M7,{estimate:$.estimate,loading:$.estimateLoading}),W("div",{className:"ms-composer-actions flex items-center gap-2",children:[X("button",{onClick:$.createDraft,disabled:!$.prompt.trim()||$.promptTooLong||$.generating||$.creatingDraft||$.disabled||$.sourceRequiredMissing||$.draftBlocked,title:$.sourceRequiredMissing?"The selected video model requires a source image":$.draftBlocked?"Drafts require Storage references or HTTP(S) source URLs":void 0,className:"px-3 py-2 text-sm border border-border rounded text-text-muted hover:text-text hover:border-accent disabled:opacity-50",children:$.creatingDraft?"Saving…":"Save draft"}),X("button",{onClick:$.generate,disabled:!$.prompt.trim()||$.promptTooLong||$.generating||$.creatingDraft||$.disabled||R||F||$.sourceRequiredMissing||L,className:"px-4 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50",title:R?`Selected avatar does not support ${P8($.avatarEngine)}`:F?`Motion prompt is limited to ${D} characters`:$.sourceRequiredMissing?"The selected video model requires a source image":L?"Select a voice":void 0,children:$.generating?"Generating…":$.isEditMode?"Generate edit":$.kind==="avatar"?"Generate video":"Generate"})]})]})]})}function M7({estimate:$,loading:Z}){let Y=a($?.cost_usd||0),Q=Z?"estimating":Y?`est. ${Y}`:$&&!$.available?"cost unavailable":"";if(!Q)return null;return X("div",{className:"px-2 py-1 rounded border border-border bg-bg-card text-xs text-text-muted",title:$?.source||"estimate",children:Q})}function b7({providers:$,provider:Z,setProvider:Y,sourceType:Q,setSourceType:H,open:J,setOpen:z,name:_,setName:V,description:k,setDescription:K,model:P,setModel:C,previewText:R,setPreviewText:D,enhance:F,setEnhance:L,audio:w,setAudio:I,audioFilename:G,setAudioFilename:j,transcript:A,setTranscript:x,language:T,setLanguage:Y6,creating:W6,createVoice:Y8,onError:g6}){let m=Z||$[0]?.slug||"",s=f6(m)?Q:"audio",B6=n(null),A6=L0();g(()=>{if(!J)return;let b=document.activeElement instanceof HTMLElement?document.activeElement:null,$6=(l)=>{if(l.key==="Escape"&&!W6){z(!1);return}if(l.key!=="Tab"||!B6.current)return;let h6=Array.from(B6.current.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])"));if(h6.length===0)return;let H6=h6[0],c=h6[h6.length-1];if(l.shiftKey&&document.activeElement===H6)l.preventDefault(),c.focus();else if(!l.shiftKey&&document.activeElement===c)l.preventDefault(),H6.focus()};return window.addEventListener("keydown",$6),window.setTimeout(()=>{B6.current?.querySelector('input[type="text"]')?.focus()},0),()=>{window.removeEventListener("keydown",$6),b?.focus()}},[J,W6,z]);let f8=!!w.trim(),F6=k.trim().length>=20,Q6=(b)=>{if(!b)return;let $6=R8(b,"audio");if($6){g6($6);return}let l=new FileReader;l.onload=()=>{I(String(l.result||"")),j(b.name)},l.onerror=()=>g6(`Could not read ${b.name}.`),l.readAsDataURL(b)};if(!J)return null;return X("div",{className:"ms-voice-backdrop",onMouseDown:()=>!W6&&z(!1),children:W("div",{ref:B6,role:"dialog","aria-modal":"true","aria-labelledby":A6,className:"ms-voice-dialog",onMouseDown:(b)=>b.stopPropagation(),children:[W("header",{className:"flex items-center justify-between gap-4 px-5 py-4 border-b border-border flex-shrink-0",children:[W("div",{className:"min-w-0",children:[X("h3",{id:A6,className:"text-base font-semibold text-text",children:"New custom voice"}),X("span",{className:"text-text-dim text-xs",children:X8(m)})]}),X("button",{type:"button",onClick:()=>z(!1),disabled:W6,"aria-label":"Close custom voice dialog",title:"Close",className:"p-2 text-text-muted hover:text-text disabled:opacity-50",children:X(e6,{size:18})})]}),W("div",{className:"ms-voice-dialog-body",children:[W("div",{className:"ms-voice-form-grid",children:[W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Provider"}),X("select",{value:m,onChange:(b)=>{if(Y(b.target.value),!f6(b.target.value))H("audio")},className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:$.map((b)=>X("option",{value:b.slug,children:X8(b.slug)},b.slug))})]}),X(x6,{label:"Voice name",value:_,onChange:V,placeholder:"Narrator"})]}),f6(m)&&W("div",{children:[X("label",{className:"text-text-muted text-xs block mb-1",children:"Method"}),W("div",{className:"ms-voice-mode",role:"group","aria-label":"Voice creation method",children:[X("button",{type:"button","data-active":s==="audio",onClick:()=>H("audio"),children:"Clone audio"}),X("button",{type:"button","data-active":s==="prompt",onClick:()=>H("prompt"),children:"Design voice"})]})]}),s==="prompt"&&m==="elevenlabs"&&X("div",{className:"ms-voice-form-grid",children:W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Design model"}),W("select",{value:P,onChange:(b)=>C(b.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:[X("option",{value:"eleven_ttv_v3",children:"Eleven v3"}),X("option",{value:"eleven_multilingual_ttv_v2",children:"Multilingual v2"})]})]})}),s==="audio"&&W("div",{className:"ms-voice-form-grid",children:[W("div",{style:{gridColumn:"1 / -1"},children:[X("label",{className:"text-text-muted text-xs block",children:"Audio sample"}),W("div",{className:"flex items-stretch gap-2",children:[W("label",{className:"inline-flex items-center justify-center gap-2 px-3 py-2 border border-border rounded text-accent text-xs cursor-pointer hover:border-accent flex-shrink-0",children:[X(o6,{size:14}),G||"Choose audio",X("input",{type:"file",accept:"audio/*",className:"hidden",onChange:(b)=>Q6(b.target.files?.[0])})]}),X("input",{type:"text",value:w.startsWith("data:")?"":w,onChange:(b)=>{I(b.target.value),j("")},placeholder:"storage:123 or https://...",className:"flex-1 bg-bg-input border border-border rounded px-3 py-2 text-sm min-w-0"})]})]}),m==="cartesia"&&X(x6,{label:"Language",value:T,onChange:Y6,placeholder:"en"}),W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Transcript"}),X("textarea",{value:A,onChange:(b)=>x(b.target.value),rows:2,className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]})]}),s==="prompt"&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Voice description"}),X("textarea",{value:k,onChange:(b)=>K(b.target.value),placeholder:"Warm, composed narrator with clear pacing and a polished studio tone.",rows:4,className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]}),s==="prompt"&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Preview text"}),X("textarea",{value:R,onChange:(b)=>D(b.target.value),placeholder:m==="minimax-audio"?"Text for the MiniMax voice preview.":"Optional. Leave empty to let ElevenLabs generate preview text.",rows:2,className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]})]}),W("footer",{className:"ms-voice-dialog-footer",children:[m!=="cartesia"?W("label",{className:"inline-flex items-center gap-2 text-xs text-text-muted cursor-pointer select-none",children:[X("input",{type:"checkbox",checked:F,onChange:(b)=>L(b.target.checked),style:{accentColor:"var(--accent)"}}),s==="audio"?m==="elevenlabs"?"Clean audio":m==="minimax-audio"?"Clean and normalize":"Enhance audio":"Enhance description"]}):X("span",{className:"text-text-dim text-xs",children:"Audio clone · Cartesia"}),W("div",{className:"flex items-center justify-end gap-2",children:[X("button",{type:"button",onClick:()=>z(!1),disabled:W6,className:"px-3 py-2 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50",children:"Cancel"}),X("button",{type:"button",onClick:Y8,disabled:W6||!_.trim()||!m||(s==="audio"?!f8:!F6),className:"px-4 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50",children:W6?"Creating...":s==="audio"?"Clone voice":"Create voice"})]})]})]})})}function I7({provider:$,caps:Z,jobs:Y,open:Q,setOpen:H,name:J,setName:z,sourceType:_,setSourceType:V,source:k,setSource:K,prompt:P,setPrompt:C,creating:R,createAvatar:D,onError:F}){let L=(Z?.source_types||["photo"]).filter((A)=>A==="photo"||A==="prompt"||A==="video"),w=L.length>0?L:["photo"],I=Y.filter((A)=>A.status==="queued"||A.status==="training"),G=Y.filter((A)=>A.status==="failed"),j=(A)=>{let x=R8(A,"image");if(x){F(x);return}let T=new FileReader;T.onload=()=>K(String(T.result||"")),T.onerror=()=>F(`Could not read ${A.name}.`),T.readAsDataURL(A)};return W("div",{style:{flexBasis:"100%"},className:"ms-option-wide border border-border rounded p-2 bg-bg-card",children:[W("div",{className:"flex items-center gap-2",children:[X("button",{onClick:()=>H(!Q),className:"text-xs px-2 py-1 border border-border rounded text-accent hover:border-accent",disabled:!$,children:Q?"Close avatar creator":"Create avatar"}),I.length>0&&W("span",{className:"text-text-dim",style:{fontSize:10},children:[I.length," creating"]}),G.length>0&&W("span",{style:{fontSize:10,color:"var(--apteva-danger, #ef4444)"},children:[G.length," failed"]})]}),Q&&W("div",{className:"flex items-end gap-2 flex-wrap mt-2",children:[X(x6,{label:"Name",value:J,onChange:z,placeholder:"Avatar name"}),W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Source"}),X("select",{value:_,onChange:(A)=>V(A.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:w.map((A)=>X("option",{value:A,children:A==="photo"?"Photo":A==="prompt"?"Prompt":"Video"},A))})]}),_==="photo"&&W(Z6,{children:[W("div",{className:"flex-1",style:{minWidth:220},children:[X("label",{className:"text-text-muted text-xs block",children:"Photo"}),X("input",{type:"text",value:k,onChange:(A)=>K(A.target.value),placeholder:"storage:123 or https://...",className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]}),W("label",{className:"text-xs px-2 py-1.5 border border-border rounded text-text hover:border-accent cursor-pointer",children:["Upload",X("input",{type:"file",accept:"image/*",onChange:(A)=>{let x=A.target.files?.[0];if(x)j(x);A.currentTarget.value=""},style:{display:"none"}})]})]}),_==="prompt"&&W("div",{className:"flex-1",style:{minWidth:260},children:[X("label",{className:"text-text-muted text-xs block",children:"Prompt"}),X("input",{type:"text",value:P,onChange:(A)=>C(A.target.value),className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]}),_==="video"&&W("div",{className:"flex-1",style:{minWidth:260},children:[X("label",{className:"text-text-muted text-xs block",children:"Video URL"}),X("input",{type:"text",value:k,onChange:(A)=>K(A.target.value),placeholder:"https://...",className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]}),X("button",{onClick:D,disabled:R||!J.trim()||(_==="photo"||_==="video")&&!k.trim()||_==="prompt"&&!P.trim(),className:"px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50",children:R?"…":"Create"})]}),Y.length>0&&X("div",{className:"flex gap-2 flex-wrap mt-2",children:Y.slice(0,6).map((A)=>X(S7,{job:A},A.id))})]})}function S7({job:$}){let Z=$.status==="failed",Y=$.status==="completed";return W("div",{className:"border rounded px-2 py-1 bg-bg-input",style:{borderColor:Z?"var(--apteva-danger, #ef4444)":Y?"var(--apteva-accent, #4ade80)":"var(--apteva-border, #333)",fontSize:10,maxWidth:180},title:$.error||$.provider_avatar_id||$.name,children:[X("span",{className:"text-text truncate block",children:$.name||`#${$.id}`}),W("span",{className:"text-text-dim truncate block",children:["#",$.id," · ",$.provider," · ",$.status]})]})}function f7({avatars:$,selected:Z,setSelected:Y}){if($.length===0)return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Avatar"}),X("div",{className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",style:{minWidth:200},children:"no replicas — train one in your provider"})]});let Q=j7($);return W("div",{className:"ms-option-wide",style:{flexBasis:"100%"},children:[W("label",{className:"text-text-muted text-xs block mb-1",children:["Avatar / replica (",$.length,")"]}),X("div",{className:"flex flex-col gap-2",style:{maxHeight:324,overflow:"auto",paddingRight:4},children:Q.map((H)=>W("div",{children:[W("div",{className:"text-text-dim mb-1",style:{fontSize:10},children:[H.label," (",H.items.length,")"]}),X("div",{className:"flex gap-2 flex-wrap",children:H.items.map((J)=>X(x7,{avatar:J,selected:J.id===Z,onSelect:()=>Y(J.id)},J.id))})]},H.key))})]})}function x7({avatar:$,selected:Z,onSelect:Y}){let Q=[$.name||$.id,$.status?`(${$.status})`:"",$.avatar_type?$.avatar_type.replace(/_/g," "):""].filter(Boolean);return W("button",{onClick:Y,title:Q.join(" "),className:"border rounded overflow-hidden text-left bg-bg-card "+(Z?"border-accent":"border-border hover:border-accent"),style:{width:96},children:[X(g7,{avatar:$}),X("div",{className:"text-text truncate px-1 pt-0.5",style:{fontSize:10},children:$.name||$.id}),X("div",{className:"text-text-dim truncate px-1 pb-0.5",style:{fontSize:9},children:v7($)})]})}function g7({avatar:$}){if(!$.thumbnail)return X("div",{className:"flex items-center justify-center text-text-dim",style:{width:96,height:96,background:"var(--apteva-bg-input, #222)",fontSize:10},children:$.name||$.id});if($.thumbnail_type==="image"||m7($.thumbnail))return X("img",{src:$.thumbnail,alt:"",loading:"lazy",style:{width:96,height:96,objectFit:"cover",display:"block"}});return X("video",{src:$.thumbnail,muted:!0,loop:!0,playsInline:!0,onMouseEnter:(Z)=>Z.currentTarget.play(),onMouseLeave:(Z)=>Z.currentTarget.pause(),style:{width:96,height:96,objectFit:"cover",display:"block"}})}function j7($){let Z=(H)=>H.ownership==="private",Y=(H)=>H.ownership!=="private";return[{key:"mine",label:"My avatars",items:$.filter(Z)},{key:"photo",label:"Photo avatars",items:$.filter((H)=>Y(H)&&H.avatar_type==="photo_avatar")},{key:"studio",label:"Studio avatars",items:$.filter((H)=>Y(H)&&(H.avatar_type==="studio_avatar"||H.avatar_type==="digital_twin"))},{key:"library",label:"Library",items:$.filter((H)=>Y(H)&&H.avatar_type!=="photo_avatar"&&H.avatar_type!=="studio_avatar"&&H.avatar_type!=="digital_twin")}].filter((H)=>H.items.length>0)}function v7($){if($.ownership==="private")return"mine";if(!$.avatar_type)return $.ownership==="public"?"library":"";return $.avatar_type.replace(/_/g," ")}function m7($){let Z=$.split("?",1)[0].toLowerCase();return Z.endsWith(".png")||Z.endsWith(".jpg")||Z.endsWith(".jpeg")||Z.endsWith(".webp")}function u7({prompt:$,selectedAvatar:Z,engine:Y,setEngine:Q,resolution:H,setResolution:J,aspect:z,setAspect:_,motionPrompt:V,setMotionPrompt:k,expressiveness:K,setExpressiveness:P}){let C=V0(Z),R=a1(Y),D=C8(Y),F=F0(D.expressiveness),L=w0(Y,Z);g(()=>{if(!C.includes(Y))Q("auto")},[C.join("|"),Y]),g(()=>{if(!R.includes(H))J(R[0])},[R.join("|"),H]),g(()=>{if(K&&!F.includes(K))P("")},[F.join("|"),K]);let w=o1(B5($),Y,Z?.avatar_type,H),I=!!D.maxChars&&V.length>D.maxChars;return W(Z6,{children:[W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Engine"}),X("select",{value:C.includes(Y)?Y:"auto",onChange:(G)=>Q(G.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:C.map((G)=>X("option",{value:G,children:P8(G)},G))})]}),W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Resolution"}),X("select",{value:R.includes(H)?H:R[0],onChange:(G)=>J(G.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:R.map((G)=>X("option",{value:G,children:r1(G)},G))})]}),W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Aspect"}),W("select",{value:z,onChange:(G)=>_(G.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:[X("option",{value:"16:9",children:"16:9"}),X("option",{value:"9:16",children:"9:16"})]})]}),F.length>0&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Expressiveness"}),W("select",{value:K,onChange:(G)=>P(G.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:[X("option",{value:"",children:"Engine default"}),F.map((G)=>X("option",{value:G,children:G.charAt(0).toUpperCase()+G.slice(1)},G))]})]}),X("div",{className:"text-text-dim text-xs pb-1",children:L?`${P8(Y)} unavailable for this avatar`:w?`est. ${a(w)} API`:$.trim()?"no published rate":"est. after script"}),D.prompt&&W("div",{className:"ms-option-wide",children:[W("label",{className:"text-text-muted text-xs flex items-center justify-between gap-2",children:[X("span",{children:"Motion prompt (optional)"}),D.maxChars?W("span",{className:I?"text-red":"text-text-dim",children:[V.length,"/",D.maxChars]}):null]}),X("textarea",{value:V,onChange:(G)=>k(G.target.value),rows:2,placeholder:"Gestures, expression and posture, e.g. calm, open hand gestures, nods while listening",className:"w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"})]})]})}function B5($){let Z=$.trim().split(/\s+/).filter(Boolean).length;if(Z===0)return 0;return Math.max(5,Z/2.5)}function p7({value:$,onChange:Z}){return W("label",{className:"ms-option-check flex items-center gap-1.5 text-xs text-text-muted cursor-pointer select-none",title:"When on, Venice blurs adult-classified output. Off = pass-through (default).",children:[X("input",{type:"checkbox",checked:$,onChange:(Y)=>Z(Y.target.checked),style:{accentColor:"var(--apteva-accent, #4ade80)"}}),"Safe mode"]})}function l7({value:$,onChange:Z,configurable:Y}){return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Audio"}),W("select",{value:$?"silent":"default",onChange:(Q)=>Z(Q.target.value==="silent"),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",title:Y===!1?"This model does not advertise configurable audio; silent output may already be its default.":"No sound sends audio=false to Venice video models that support generated audio.",children:[X("option",{value:"default",children:"Default"}),X("option",{value:"silent",children:"No sound"})]})]})}function c7({model:$,setModel:Z,format:Y,setFormat:Q,maxSources:H,liveModels:J,liveProvider:z}){let _=J.length>0?J:T7.map((V)=>({id:V,label:V}));return W(Z6,{children:[W("div",{children:[W("label",{className:"text-text-muted text-xs block",children:["Edit model",W("span",{className:"text-text-dim ml-1",style:{fontSize:10},children:["· max ",H," ref",H===1?"":"s"]})]}),X("select",{value:$,onChange:(V)=>Z(V.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",title:J.length>0?`${J.length} models from ${z}`:void 0,children:_.map((V)=>X("option",{value:V.id,children:V.label},V.id))})]}),W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Format"}),W("select",{value:Y,onChange:(V)=>Q(V.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:[X("option",{value:"png",children:"PNG"}),X("option",{value:"jpeg",children:"JPEG"}),X("option",{value:"webp",children:"WebP"})]})]})]})}function d7({projectId:$,sources:Z,maxSources:Y,onAdd:Q,onRemove:H,onClear:J,onError:z,hint:_,referencePurpose:V,onReferencePurposeChange:k,promptReferences:K,onInsertPromptReference:P}){let[C,R]=O(""),D=n(null),F=Z.length>=Y,L=(G)=>{let j=R8(G,"image");if(j){z(j);return}let A=new FileReader;A.onload=()=>{let x=String(A.result||"");Q(x,`Upload (${G.name})`)},A.onerror=()=>z(`Could not read ${G.name}.`),A.readAsDataURL(G)},w=(G)=>{G.preventDefault(),Array.from(G.dataTransfer.files||[]).slice(0,Math.max(0,Y-Z.length)).forEach(L)},I=()=>{let G=C.trim();if(!G||F)return;Q(G,G.length>40?G.slice(0,37)+"...":G),R("")};return W("div",{onDrop:w,onDragOver:(G)=>G.preventDefault(),className:"flex flex-col gap-2 p-2 rounded bg-bg-card "+(Z.length>0?"border border-accent":"border border-dashed border-border"),children:[V&&k&&W("div",{className:"flex items-center justify-between gap-3 flex-wrap",children:[X("span",{className:"text-text-muted text-xs font-medium",children:"Reference use"}),X("div",{role:"group","aria-label":"Reference use",className:"inline-flex border border-border rounded overflow-hidden",children:[["identity","Subject identity"],["reference","General reference"]].map(([G,j])=>X("button",{type:"button","aria-pressed":V===G,onClick:()=>k(G),className:"px-2.5 py-1.5 text-xs border-0 "+(V===G?"bg-accent text-bg font-bold":"bg-bg-input text-text-muted hover:text-text"),children:j},G))})]}),K&&K.length>0&&P&&W("div",{className:"flex items-start gap-2 flex-wrap",children:[X("span",{className:"text-text-muted text-xs font-medium py-1",children:"Prompt references"}),X("div",{className:"flex items-center gap-1.5 flex-wrap min-w-0",children:K.map((G)=>W("button",{type:"button",onClick:()=>P(G.token),className:"flex items-center gap-1.5 max-w-full px-2 py-1 text-xs border border-border rounded bg-bg-input text-text hover:border-accent",title:`Insert ${G.token} at the prompt cursor`,children:[X("span",{className:"text-text-dim truncate",children:G.label}),X("code",{className:"text-accent flex-shrink-0",children:G.token})]},G.token))})]}),Z.length>0&&X("div",{className:"flex gap-2 overflow-x-auto pb-1",children:Z.map((G,j)=>{let A=W9(G.value,$),x=V==="identity"?j===0?"Primary identity":`Identity angle ${j+1}`:`Reference ${j+1}`;return W("div",{className:"flex items-center gap-2 border border-border rounded p-1.5 bg-bg",style:{minWidth:180,maxWidth:240},children:[A?X("img",{src:A,alt:"",style:{width:44,height:44,objectFit:"cover",borderRadius:4,flexShrink:0}}):X("div",{style:{width:44,height:44,borderRadius:4,background:"var(--apteva-bg-input, #222)",flexShrink:0},className:"flex items-center justify-center text-text-dim text-xs",children:"ref"}),W("div",{className:"min-w-0 flex-1",children:[X("div",{className:"text-text-dim",style:{fontSize:10},children:x}),X("div",{className:"text-xs text-text truncate",title:G.label,children:G.label||"(set)"})]}),X("button",{onClick:()=>H(j),"aria-label":`Remove ${x.toLowerCase()}`,className:"text-text-muted hover:text-text text-xs px-1.5 py-0.5 border border-border rounded",title:"Remove reference",children:"x"})]},`${G.value}-${j}`)})}),W("div",{className:"flex items-center justify-between gap-3 flex-wrap",children:[W("span",{className:"text-text-muted text-xs font-medium",children:[_||"Reference images",W("span",{className:"text-text-dim",children:[" · ",Z.length,"/",Y]})]}),W("div",{className:"flex items-center gap-2",children:[Z.length>0&&X("button",{onClick:J,className:"text-text-muted hover:text-text text-xs px-2 py-1 border border-border rounded",children:"Clear"}),X("button",{disabled:F,onClick:()=>D.current?.click(),className:"text-xs px-2 py-1 border border-border rounded text-text hover:border-accent disabled:opacity-50",children:"Upload"})]}),X("input",{ref:D,type:"file",accept:"image/*",multiple:!0,onChange:(G)=>{Array.from(G.target.files||[]).slice(0,Math.max(0,Y-Z.length)).forEach(L),G.target.value=""},style:{display:"none"}})]}),W("div",{className:"flex items-end gap-2",children:[W("label",{className:"flex-1 min-w-0 text-text-muted text-xs",children:["Image URL",X("input",{type:"text",value:C,disabled:F,onChange:(G)=>R(G.target.value),onKeyDown:(G)=>{if(G.key==="Enter")I()},placeholder:F?"reference limit reached":"https://...",className:"block w-full mt-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm disabled:opacity-50"})]}),X("button",{onClick:I,disabled:F||!C.trim(),className:"px-3 py-1.5 text-sm border border-border rounded text-text hover:border-accent disabled:opacity-50",children:"Add"})]})]})}function n7({model:$,setModel:Z,size:Y,setSize:Q,imageResolution:H,setImageResolution:J,quality:z,setQuality:_,format:V,setFormat:k,liveModels:K,liveProvider:P,selectedProvider:C,currentModel:R,aspect:D,setAspect:F}){let L=K&&K.length>0,w=R?.pixel_sizes&&R.pixel_sizes.length>0?R.pixel_sizes:L?[]:X5[$]||["1024x1024"],I=!L||!R?.aspect_ratios?.length||!!R?.pixel_sizes?.length,G=!!R?.aspect_ratios?.length,j=!!R?.resolutions?.length,A=!L||C==="openai-api"||C==="openai-codex",x=!L||C==="openai-api"||C==="openai-codex"||C==="venice-ai";return W(Z6,{children:[W("div",{children:[W("label",{className:"text-text-muted text-xs block",children:["Model",L&&W("span",{className:"text-text-dim ml-1",style:{fontSize:10},children:["· ",P," (",K.length,")"]})]}),X("select",{value:$,onChange:(T)=>Z(T.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:L?K.map((T)=>X("option",{value:T.id,children:K7(T)},T.id)):D7.map((T)=>X("option",{value:T,children:w7[T]},T))})]}),I&&(w.length>0?W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Size"}),X("select",{value:Y,onChange:(T)=>Q(T.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:w.map((T)=>X("option",{value:T,children:T},T))})]}):X(x6,{label:"Size",value:Y,onChange:Q,placeholder:"720x1280"})),G&&X(H5,{aspects:R?.aspect_ratios,value:D,onChange:F}),j&&X(q5,{resolutions:R?.resolutions,value:H,onChange:J}),A&&$!=="dall-e-2"&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Quality"}),X("select",{value:z,onChange:(T)=>_(T.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:(W5($)?Z5:Y5).map((T)=>X("option",{value:T,children:T},T))})]}),x&&W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Format"}),W("select",{value:V,onChange:(T)=>k(T.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:[X("option",{value:"png",children:"PNG"}),X("option",{value:"jpeg",children:"JPEG"}),X("option",{value:"webp",children:"WebP"})]})]})]})}function M8({model:$,setModel:Z,liveModels:Y,liveProvider:Q,label:H="Model"}){let J=Y||[];if(J.length===0)return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:H}),X("div",{className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",style:{minWidth:200},children:Q?`loading ${Q}…`:"no provider bound"})]});return W("div",{children:[W("label",{className:"text-text-muted text-xs block",children:[H,W("span",{className:"text-text-dim ml-1",style:{fontSize:10},children:["· ",Q," (",J.length,")"]})]}),X("select",{value:$,onChange:(z)=>Z(z.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",style:{maxWidth:280},children:J.map((z)=>{let _=z.model_type==="image-to-video"?" · img→vid":"",V=a(z.price_usd||0),k=[_,V?` ${V}`:""].filter(Boolean).join("");return W("option",{value:z.id,children:[z.id,k]},z.id)})})]})}function a7({voice:$,provider:Z,voices:Y,onChange:Q,canCreate:H,onCreate:J}){let z=L0(),_=new Map;for(let V of Y){let k=V.provider||S6(V.id)||Z,K=_.get(k)||[];K.push(V),_.set(k,K)}return W("div",{className:"ms-voice-picker",children:[X("div",{className:"ms-voice-picker-header",children:W("div",{className:"flex items-center gap-2 min-w-0",children:[X("label",{className:"text-text-muted text-xs font-semibold",htmlFor:z,children:"Voice"}),Z&&X("span",{className:"ms-provider-label",children:X8(Z)})]})}),W("div",{className:"ms-voice-picker-row",children:[W("select",{id:z,value:Y.some((V)=>V.id===$)?$:"",onChange:(V)=>Q(V.target.value),className:"bg-bg-input border border-border rounded px-3 py-2 text-sm",children:[X("option",{value:"",children:Y.length>0?"Select a voice":`No ${X8(Z)} voices available`}),Array.from(_.entries()).map(([V,k])=>X("optgroup",{label:X8(V),children:k.map((K)=>{let P=[K.language,K.gender].filter(Boolean).join(" · ");return W("option",{value:K.id,children:[K.name||K.id,P?` · ${P}`:""]},K.id)})},V))]}),H&&W("button",{type:"button",onClick:J,className:"inline-flex items-center gap-2 px-3 py-2 border border-border rounded text-accent text-xs font-semibold hover:border-accent",children:[X(i6,{size:14}),"New custom voice"]})]})]})}function r7({voice:$,setVoice:Z,voices:Y}){return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Voice"}),X("select",{value:$,onChange:(Q)=>Z(Q.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",style:{maxWidth:260},children:Y.map((Q)=>W("option",{value:Q.id,children:[Q.name||Q.id,Q.language?` · ${Q.language}`:"",Q.gender?` · ${Q.gender}`:""]},Q.id))})]})}function i7({durations:$,value:Z,onChange:Y}){if(!$||$.length===0)return X(I8,{label:"Duration (s)",value:Z,onChange:Y,min:1,max:60});return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Duration"}),X("select",{value:`${Z}s`,onChange:(Q)=>{let H=parseInt(Q.target.value.replace(/[^\d]/g,""),10);if(!isNaN(H))Y(H)},className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:$.map((Q)=>X("option",{value:Q,children:Q},Q))})]})}function H5({aspects:$,value:Z,onChange:Y,disabledHint:Q}){if(Q)return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Aspect"}),X("div",{className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",style:{minWidth:160},title:Q,children:Q})]});if(!$||$.length===0)return X(x6,{label:"Aspect",value:Z,onChange:Y});return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Aspect"}),X("select",{value:Z,onChange:(H)=>Y(H.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:$.map((H)=>X("option",{value:H,children:H},H))})]})}function q5({resolutions:$,value:Z,onChange:Y}){if(!$||$.length===0)return X(x6,{label:"Resolution",value:Z,onChange:Y});return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Resolution"}),X("select",{value:Z,onChange:(Q)=>Y(Q.target.value),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",children:$.map((Q)=>X("option",{value:Q,children:Q},Q))})]})}function I8({label:$,value:Z,onChange:Y,min:Q,max:H,step:J}){return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:$}),X("input",{type:"number",value:Z,min:Q,max:H,step:J,onChange:(z)=>Y(Number(z.target.value)||0),className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",style:{width:96}})]})}function x6({label:$,value:Z,onChange:Y,placeholder:Q}){return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:$}),X("input",{type:"text",value:Z,onChange:(H)=>Y(H.target.value),placeholder:Q,className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",style:{width:140}})]})}function o7({value:$,onChange:Z,placeholder:Y,suggestions:Q}){return W("div",{children:[X("label",{className:"text-text-muted text-xs block",children:"Output folder"}),X("input",{type:"text",value:$,onChange:(H)=>Z(H.target.value),placeholder:Y,list:"media-storage-folder-suggestions",className:"bg-bg-input border border-border rounded px-2 py-1.5 text-sm",title:"Storage folder for generated output. Leave empty for the default."}),X("datalist",{id:"media-storage-folder-suggestions",children:Q.map((H)=>X("option",{value:H},H))})]})}function s7($){if($==="image")return"/.generated/images/";if($==="video")return"/.generated/videos/";if($==="music")return"/.generated/music/";if($==="avatar")return"/.generated/avatars/";return"/.generated/audio/"}function A0($){if($==="music")return"Music";if($==="audio_sfx")return"Sound effect";if($==="audio_tts")return"Voiceover";if($==="avatar")return"Avatar video";return"Video"}function S8($){return $.thumbnail_b64?`data:image/jpeg;base64,${$.thumbnail_b64}`:void 0}function t7($){let Z=new Set;for(let Y of $||[]){let Q=e7(Y.folder||"");if(Q&&!$9(Q))Z.add(Q)}return Array.from(Z).sort().slice(0,40)}function e7($){let Z=String($||"").trim();if(!Z)return"";let Y=Z.startsWith("/")?Z:`/${Z}`;return Y.endsWith("/")?Y:`${Y}/`}function $9($){return $.split("/").some((Z)=>Z.startsWith("."))}var T0="media-studio:player-play";function Z8({src:$,mediaType:Z,label:Y,durationSeconds:Q=0,poster:H,variant:J="card"}){let z=L0(),_=n(null),V=n(null),[k,K]=O(!1),[P,C]=O(!1),[R,D]=O(0),[F,L]=O(Q>0?Q:0);g(()=>{let T=(Y6)=>{if(Y6.detail!==z)_.current?.pause()};return window.addEventListener(T0,T),()=>window.removeEventListener(T0,T)},[z]),g(()=>{K(!1),D(0),L(Q>0?Q:0)},[$,Q]);let w=()=>{window.dispatchEvent(new CustomEvent(T0,{detail:z}))},I=()=>{let T=_.current;if(!T)return;if(T.paused)w(),T.play().catch(()=>K(!1));else T.pause()},G=()=>{let T=_.current;if(!T)return;T.muted=!T.muted,C(T.muted)},j=()=>{let T=V.current;if(T?.requestFullscreen){T.requestFullscreen();return}_.current?.webkitEnterFullscreen?.()},A=()=>{let T=_.current?.duration||0;if(Number.isFinite(T)&&T>0)L(T)},x=W("div",{className:"ms-player-controls",children:[X("button",{type:"button",className:"ms-player-button",onClick:I,"aria-label":k?`Pause ${Y}`:`Play ${Y}`,title:k?"Pause":"Play",children:k?X(r6,{size:16,strokeWidth:1.8}):X(I6,{size:16,strokeWidth:1.8,fill:"currentColor"})}),X("input",{type:"range",className:"ms-player-range",min:0,max:Math.max(F,0.01),step:0.01,value:Math.min(R,Math.max(F,0.01)),onChange:(T)=>{let Y6=Number(T.currentTarget.value);if(_.current)_.current.currentTime=Y6;D(Y6)},"aria-label":`Seek ${Y}`}),W("span",{className:"ms-player-time",children:[N0(R)," / ",N0(F)]}),X("button",{type:"button",className:"ms-player-button",onClick:G,"aria-label":P?`Unmute ${Y}`:`Mute ${Y}`,title:P?"Unmute":"Mute",children:P?X(t6,{size:16,strokeWidth:1.8}):X(s6,{size:16,strokeWidth:1.8})}),Z==="video"&&X("button",{type:"button",className:"ms-player-button",onClick:j,"aria-label":`View ${Y} fullscreen`,title:"Fullscreen",children:X(a6,{size:16,strokeWidth:1.8})})]});if(Z==="audio")return W("div",{className:`ms-audio-player ms-player-${J}`,children:[X("div",{className:"ms-audio-mark","aria-hidden":"true",children:X(n6,{size:22,strokeWidth:1.7})}),W("div",{className:"ms-audio-content",children:[X("div",{className:"ms-audio-kicker",children:Y}),X("audio",{ref:(T)=>{_.current=T},src:$,preload:"metadata",onLoadedMetadata:A,onDurationChange:A,onTimeUpdate:()=>D(_.current?.currentTime||0),onPlay:()=>{w(),K(!0)},onPause:()=>K(!1),onEnded:()=>K(!1),onVolumeChange:()=>C(Boolean(_.current?.muted))}),x]})]});return W("div",{ref:V,className:`ms-video-player ms-player-${J}`,children:[X("video",{ref:(T)=>{_.current=T},src:$,poster:H,preload:"metadata",playsInline:!0,onClick:I,onLoadedMetadata:A,onDurationChange:A,onTimeUpdate:()=>D(_.current?.currentTime||0),onPlay:()=>{w(),K(!0)},onPause:()=>K(!1),onEnded:()=>K(!1),onVolumeChange:()=>C(Boolean(_.current?.muted))}),!k&&X("button",{type:"button",className:"ms-video-play-overlay",onClick:I,"aria-label":`Play ${Y}`,title:"Play",children:X(I6,{size:20,strokeWidth:1.8,fill:"currentColor"})}),x]})}function X9({kind:$,items:Z,onSelect:Y,onOpenLightbox:Q,onGenerateDraft:H,generating:J,generatingPrompt:z,generatingModel:_,generatingCostUSD:V,pendingJobs:k,generatingDraftId:K,selectedId:P}){if($==="image")return W("div",{style:{display:"grid",gridTemplateColumns:"repeat(auto-fill, minmax(min(280px, 100%), 1fr))",gap:8,padding:8,alignItems:"start",alignContent:"start"},children:[J&&X(b8,{prompt:z,model:_,costUSD:V,kind:"image"}),Z.map((D)=>{let F=E0(D),L=D.status==="draft";return W("div",{className:"ms-image-card border rounded overflow-hidden hover:border-accent transition-colors "+(P===D.id?"border-accent ms-card-selected":"border-border"),children:[L?X(e1,{generation:D,busy:K===D.id,onGenerate:H}):F?X("button",{onClick:()=>Q(D),className:"ms-tile-media bg-bg-input",title:"Open full size","aria-label":`Open ${D.prompt||"image"} full size`,children:X("img",{src:F,alt:"",loading:"lazy"})}):W("div",{className:`ms-tile-media ms-tile-empty bg-bg-input ${D.status==="failed"?"ms-tile-failed":"text-text-muted"}`,children:[X(H9,{failed:D.status==="failed"}),X("span",{children:D.status==="failed"?"Generation failed":"No preview"})]}),X("button",{onClick:()=>Y(D),className:"block w-full text-left",title:"Show details","aria-pressed":P===D.id,children:X($5,{g:D})})]},D.id)})]});let C=$==="video"||$==="avatar",R=A0($);return W("div",{className:`ms-gallery-grid ${C?"ms-gallery-video":"ms-gallery-audio"}`,children:[J&&X(b8,{prompt:z,model:_,costUSD:V,kind:$}),($==="video"||$==="avatar")&&k.map((D)=>X(b8,{prompt:D.prompt,model:D.model?`#${D.id} · ${D.model}`:`#${D.id}`,costUSD:D.cost_usd||0,kind:$,status:D.status,estimatedSeconds:D.estimated_duration_seconds||0,startedAt:D0(D.created_at)},`job-${D.id}`)),(()=>{let D=new Set(k.map((F)=>F.generation_id||0).filter((F)=>F>0));return Z.map((F)=>{let L=F.storage_urls?.[0]||F.local_cache_url||F.upstream_urls?.[0]||"",w=($==="video"||$==="avatar")&&(F.status==="queued"||F.status==="generating")&&!L;if(w&&D.has(F.id))return null;if(w)return X(b8,{prompt:F.prompt,model:F.model?`#${F.id} · ${F.model}`:`#${F.id}`,costUSD:F.cost_usd||0,kind:$,status:F.status,startedAt:D0(F.created_at)},F.id);let I=F.status==="draft";return W("div",{className:P===F.id?"ms-media-card ms-card-selected":"ms-media-card",children:[I?X(e1,{generation:F,busy:K===F.id,onGenerate:H}):L?X(Z8,{src:L,mediaType:C?"video":"audio",label:R,durationSeconds:F.duration_ms>0?F.duration_ms/1000:0,poster:S8(F)}):X("div",{className:"bg-bg-input py-6 text-center text-text-muted text-xs",children:"no source"}),X("button",{onClick:()=>Y(F),className:"block w-full text-left",title:"Show details","aria-pressed":P===F.id,children:X($5,{g:F})})]},F.id)})})()]})}function e1({generation:$,busy:Z,onGenerate:Y}){let Q=a($.cost_usd);return W("div",{className:"bg-bg-input flex flex-col items-center justify-center text-center px-4 py-8",style:{minHeight:160},children:[X("div",{className:"text-xs uppercase text-text-dim",children:"Draft"}),X("div",{className:"mt-2 text-sm text-text",style:{maxWidth:280},children:$.prompt.length>90?$.prompt.slice(0,87)+"…":$.prompt}),Q&&W("div",{className:"mt-2 text-accent",style:{fontSize:10},children:["est. ",Q]}),X("button",{type:"button",disabled:Z,onClick:(H)=>{H.stopPropagation(),Y($)},className:"mt-4 px-3 py-1.5 text-xs bg-accent text-bg rounded font-bold disabled:opacity-50",children:Z?"Generating…":"Generate"})]})}function b8({prompt:$,model:Z,costUSD:Y=0,kind:Q="image",status:H="",estimatedSeconds:J=0,startedAt:z=0}){let _=a(Y),[V]=O(()=>Date.now()),k=z>0&&z<=V+5000&&V-z<86400000?Math.min(z,V):V,[K,P]=O(()=>Date.now());g(()=>{let w=setInterval(()=>P(Date.now()),1000);return()=>clearInterval(w)},[]);let C=Math.max(0,Math.floor((K-k)/1000)),R=H==="queued"||H==="pending",D=R?"Queued":Q==="image"?"Generating":"Rendering",F=J>0&&!R?Math.min(0.95,C/J):0,L=Q==="image"?"ms-gen-stage-square":Q==="video"||Q==="avatar"?"ms-gen-stage-wide":"ms-gen-stage-short";return W("div",{className:"ms-gen-card rounded overflow-hidden",role:"status","aria-live":"polite","aria-label":`${D}: ${$}`,children:[W("div",{className:`ms-gen-stage ${L}`,children:[X("div",{className:"ms-gen-glow","aria-hidden":"true"}),X("div",{className:"ms-gen-shimmer","aria-hidden":"true"}),W("div",{className:"ms-gen-center",children:[W("div",{className:"ms-gen-dots","aria-hidden":"true",children:[X("span",{}),X("span",{}),X("span",{})]}),W("div",{className:"ms-gen-label",children:[D,X("span",{className:"ms-gen-time",children:Z9(C)})]})]}),X("div",{className:"ms-gen-progress","aria-hidden":"true",children:F>0?X("div",{className:"ms-gen-progress-fill",style:{width:`${Math.round(F*100)}%`}}):X("div",{className:"ms-gen-progress-indeterminate"})})]}),W("div",{className:"ms-gen-meta",children:[$&&X("div",{className:"ms-gen-prompt",title:$,children:$}),W("div",{className:"ms-gen-facts",children:[Z&&X("span",{className:"truncate",children:Z}),_&&W("span",{className:"ms-gen-cost",children:["est. ",_]})]})]})]})}function Z9($){if($<60)return`${$}s`;let Z=Math.floor($/60),Y=$%60;return`${Z}:${String(Y).padStart(2,"0")}`}function $5({g:$}){let Z=a($.cost_usd),Y=$.status&&$.status!=="ready"?$.status:"";return W("div",{className:"ms-card-meta p-2",children:[W("div",{className:"text-text text-xs truncate flex items-center gap-1.5",children:[Y&&X("span",{className:"text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted uppercase",children:Y}),X("span",{className:"truncate",children:$.prompt})]}),W("div",{className:"ms-card-facts text-text-dim mt-1",style:{fontSize:10},children:[W("span",{className:"truncate",title:`${$.provider} · ${$.model||$.size||"—"}`,children:[$.provider," · ",$.model||$.size||"—"]}),W("span",{className:"flex items-center gap-1.5 whitespace-nowrap",children:[X("span",{children:new Date($.created_at).toLocaleString()}),Z&&W("span",{className:"text-accent",children:["· ",Z]})]})]})]})}function Y9({selected:$,onClose:Z,deleting:Y,onDelete:Q,onUseAsReference:H}){let J=$.storage_urls?.[0]||$.local_cache_url||$.upstream_urls?.[0]||"",z=A0($.kind);return W("aside",{className:"ms-media-detail flex-shrink-0 border-border bg-bg-card flex flex-col",children:[W("header",{className:"ms-detail-header border-b border-border",children:[W("div",{className:"flex items-start gap-3",children:[X("h3",{className:"ms-detail-title text-text font-medium",title:$.prompt,children:$.prompt||"Untitled generation"}),X("button",{onClick:Z,"aria-label":"Close details",title:"Close details",className:"ms-detail-close text-text-muted hover:text-text",children:X(B9,{})})]}),W("div",{className:"flex flex-wrap items-center gap-2",children:[H&&X("button",{onClick:H,className:"text-xs px-2.5 py-1.5 border border-border rounded text-accent hover:border-accent",title:"Use this image as the reference for an edit",children:"Use as reference"}),W("button",{onClick:Q,disabled:Y,className:"ms-btn-danger ms-btn-icon text-xs px-2.5 py-1.5 rounded",title:"Delete this generation and its linked Storage files",children:[X(k0,{size:13}),Y?"Deleting…":"Delete"]})]})]}),W("div",{className:"flex-1 overflow-auto",children:[J&&$.kind==="image"&&X("img",{src:J,alt:"",className:"w-full"}),J&&($.kind==="video"||$.kind==="avatar")&&X(Z8,{src:J,mediaType:"video",label:z,durationSeconds:$.duration_ms>0?$.duration_ms/1000:0,poster:S8($),variant:"detail"}),J&&($.kind==="audio_tts"||$.kind==="audio_sfx"||$.kind==="music")&&X(Z8,{src:J,mediaType:"audio",label:z,durationSeconds:$.duration_ms>0?$.duration_ms/1000:0,variant:"detail"}),W("dl",{className:"px-4 py-3 text-xs flex flex-col gap-2",children:[$.prompt&&X(e,{label:"Prompt",value:$.prompt,wrap:!0}),X(e,{label:"Kind",value:$.kind}),$.status&&X(e,{label:"Status",value:$.status}),X(e,{label:"Provider",value:$.provider}),X(e,{label:"Model",value:$.model||"—"}),$.size&&X(e,{label:"Size",value:$.size}),$.duration_ms>0&&X(e,{label:"Duration",value:`${($.duration_ms/1000).toFixed(1)}s`}),X(e,{label:"Count",value:String($.count)}),a($.cost_usd)&&X(e,{label:"Cost",value:a($.cost_usd)}),X(e,{label:"Created",value:new Date($.created_at).toLocaleString()}),$.revised_prompt&&X(e,{label:"Revised",value:$.revised_prompt,wrap:!0}),$.storage_ids.length>0&&X(e,{label:"Storage IDs",value:$.storage_ids.map((_)=>`#${_}`).join(", ")})]}),$.request_json&&W("details",{className:"ms-detail-request mx-4 mb-3 border border-border rounded",children:[X("summary",{className:"text-xs text-text-muted",children:"Request details"}),X("pre",{className:"text-text",children:J9($.request_json)})]}),$.storage_urls&&$.storage_urls.length>0&&X("div",{className:"px-4 pb-3 flex flex-col gap-1",children:$.storage_urls.map((_,V)=>W("a",{href:_,target:"_blank",rel:"noopener",className:"text-accent text-xs hover:underline",children:["Open #",$.storage_ids[V]," →"]},V))})]})]})}function W9($,Z){let Y=$.trim();if(!Y)return"";if(Y.startsWith("storage:")){let Q=Y.slice(8);return g1(Q,Z)}if(Y.startsWith("http://")||Y.startsWith("https://")||Y.startsWith("data:"))return Y;return`data:image/png;base64,${Y}`}function Q9({item:$,onClose:Z,onUseAsReference:Y}){let Q=$.storage_urls?.[0]||$.local_cache_url||$.upstream_urls?.[0]||E0($),H=A0($.kind),J=n(null);return g(()=>{let z=document.activeElement instanceof HTMLElement?document.activeElement:null,_=(V)=>{if(V.key==="Escape")Z()};return window.addEventListener("keydown",_),J.current?.focus(),()=>{window.removeEventListener("keydown",_),z?.focus()}},[Z]),X("div",{ref:J,role:"dialog","aria-modal":"true","aria-label":`${$.kind} preview`,tabIndex:-1,onClick:Z,style:{position:"fixed",inset:0,background:"rgba(0,0,0,0.85)",zIndex:9999,display:"flex",flexDirection:"column",alignItems:"center",justifyContent:"center",padding:"clamp(12px, 4vw, 24px)"},children:W("div",{onClick:(z)=>z.stopPropagation(),style:{display:"flex",flexDirection:"column",alignItems:"center",maxWidth:"100%",maxHeight:"100%",gap:12},children:[Q&&$.kind==="image"&&X("img",{src:Q,alt:"",style:{maxWidth:"92vw",maxHeight:"82vh",objectFit:"contain",borderRadius:4}}),Q&&($.kind==="video"||$.kind==="avatar")&&X(Z8,{src:Q,mediaType:"video",label:H,durationSeconds:$.duration_ms>0?$.duration_ms/1000:0,poster:S8($),variant:"lightbox"}),Q&&($.kind==="audio_tts"||$.kind==="audio_sfx"||$.kind==="music")&&X(Z8,{src:Q,mediaType:"audio",label:H,durationSeconds:$.duration_ms>0?$.duration_ms/1000:0,variant:"lightbox"}),X("div",{className:"text-text text-sm text-center",style:{maxWidth:700},children:$.prompt}),W("div",{className:"text-text-dim",style:{fontSize:11},children:[$.provider," · ",$.model||$.size||"—"," ·"," ",new Date($.created_at).toLocaleString(),a($.cost_usd)&&W(Z6,{children:[" · ",X("span",{className:"text-accent",children:a($.cost_usd)})]})]}),W("div",{className:"flex items-center gap-2",children:[Y&&X("button",{onClick:Y,className:"text-xs px-3 py-1.5 border border-border rounded text-accent hover:border-accent",children:"Use as reference"}),Q&&X("a",{href:Q,target:"_blank",rel:"noopener",className:"text-xs px-3 py-1.5 border border-border rounded text-text",children:"Open original"}),X("button",{onClick:Z,className:"text-xs px-3 py-1.5 border border-border rounded text-text-muted",children:"Close (Esc)"})]})]})})}function e({label:$,value:Z,wrap:Y}){return W("div",{className:"flex gap-2",children:[X("span",{className:"text-text-dim flex-shrink-0",style:{width:80},children:$}),X("span",{className:"flex-1 min-w-0 text-text "+(Y?"break-words":"break-all"),style:Y?{whiteSpace:"pre-wrap",lineHeight:1.5}:void 0,title:Y?void 0:Z,children:Z})]})}function J9($){try{return JSON.stringify(JSON.parse($),null,2)}catch{return $}}function k0({size:$=14}){return W("svg",{width:$,height:$,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:"2",strokeLinecap:"round",strokeLinejoin:"round","aria-hidden":"true",children:[X("path",{d:"M3 6h18"}),X("path",{d:"M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}),X("path",{d:"M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"}),X("path",{d:"M10 11v6M14 11v6"})]})}function B9(){return X("svg",{width:"16",height:"16",viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:"2",strokeLinecap:"round","aria-hidden":"true",children:X("path",{d:"M18 6 6 18M6 6l12 12"})})}function H9({failed:$}){return $?W("svg",{width:"22",height:"22",viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:"1.8",strokeLinecap:"round","aria-hidden":"true",children:[X("circle",{cx:"12",cy:"12",r:"9"}),X("path",{d:"M12 7.5v5.5M12 16.5h.01"})]}):W("svg",{width:"22",height:"22",viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:"1.8",strokeLinecap:"round",strokeLinejoin:"round","aria-hidden":"true",children:[X("rect",{x:"3",y:"3",width:"18",height:"18",rx:"2"}),X("circle",{cx:"9",cy:"9",r:"2"}),X("path",{d:"m21 15-3.1-3.1a2 2 0 0 0-2.8 0L6 21"})]})}var q9={image:"image",video:"video",audio_tts:"voiceover",audio_sfx:"sound effect",music:"track",avatar:"avatar video"};function U9({generation:$,busy:Z,error:Y,onCancel:Q,onConfirm:H}){let J=n(null),z=n(null),_=n(Q);_.current=Q,g(()=>{let C=document.activeElement instanceof HTMLElement?document.activeElement:null;z.current?.focus();let R=(D)=>{if(D.key==="Escape"){D.preventDefault(),D.stopImmediatePropagation(),_.current();return}if(D.key!=="Tab"||!J.current)return;let F=Array.from(J.current.querySelectorAll("button:not([disabled])"));if(F.length===0)return;let L=F[0],w=F[F.length-1];if(D.shiftKey&&document.activeElement===L)D.preventDefault(),w.focus();else if(!D.shiftKey&&document.activeElement===w)D.preventDefault(),L.focus()};return window.addEventListener("keydown",R,!0),()=>{window.removeEventListener("keydown",R,!0),C?.focus()}},[]);let V=q9[$.kind]||"generation",k=$.storage_ids||[],K=$.kind==="image"?E0($):S8($)||"",P=$.created_at?new Date($.created_at).toLocaleString():"";return X("div",{className:"ms-confirm-backdrop",onMouseDown:()=>_.current(),children:W("div",{ref:J,role:"alertdialog","aria-modal":"true","aria-labelledby":"ms-confirm-title","aria-describedby":"ms-confirm-desc",className:"ms-confirm-dialog rounded",onMouseDown:(C)=>C.stopPropagation(),children:[W("div",{className:"ms-confirm-body",children:[X("div",{className:"ms-confirm-icon","aria-hidden":"true",children:X(k0,{size:18})}),W("div",{className:"min-w-0 flex-1",children:[W("h2",{id:"ms-confirm-title",className:"text-base font-semibold text-text",children:["Delete this ",V,"?"]}),W("div",{className:"ms-confirm-item border border-border rounded bg-bg-input",children:[K?X("img",{src:K,alt:"",className:"ms-confirm-thumb"}):X("span",{className:"ms-confirm-thumb ms-confirm-thumb-icon text-text-muted",children:X(Q5,{kind:$.kind})}),W("div",{className:"min-w-0",children:[X("p",{className:"ms-confirm-prompt text-sm text-text",children:$.prompt||"Untitled generation"}),X("p",{className:"text-xs text-text-dim truncate",children:[$.model,P].filter(Boolean).join(" · ")})]})]}),W("p",{id:"ms-confirm-desc",className:"text-sm text-text-muted",style:{lineHeight:1.5},children:[k.length>0?W(Z6,{children:["The generation and its ",k.length===1?"Storage file":`${k.length} Storage files`," ",X("span",{className:"text-text",children:k.map((C)=>`#${C}`).join(", ")})," will be permanently deleted."]}):X(Z6,{children:"The generation record will be permanently deleted."})," ","This can't be undone."]}),Y&&W("p",{className:"ms-confirm-error text-sm rounded",role:"alert",children:["Couldn't delete: ",Y]})]})]}),W("div",{className:"ms-confirm-actions border-t border-border",children:[X("button",{ref:z,onClick:Q,disabled:Z,className:"px-4 py-2 text-sm border border-border rounded text-text hover:border-accent disabled:opacity-50",children:"Cancel"}),W("button",{onClick:H,disabled:Z,className:"ms-btn-danger-solid ms-btn-icon px-4 py-2 text-sm rounded font-semibold",children:[Z?X("span",{className:"ms-confirm-spinner","aria-hidden":"true"}):X(k0,{size:14}),Z?"Deleting…":Y?"Try again":"Delete"]})]})]})})}export{R7 as default};

//# debugId=0D68333A7296E20864756E2164756E21
