import{useEffect as G7,useRef as EN,useState as v8}from"react";var sN="180",P9={LEFT:0,MIDDLE:1,RIGHT:2,ROTATE:0,DOLLY:1,PAN:2},A9={ROTATE:0,PAN:1,DOLLY_PAN:2,DOLLY_ROTATE:3},iN=0,p7=1,oN=2;var d7=1,aN=2,W9=3,RJ=0,m8=1,a8=2,T9=0,dJ=1,l7=2,m7=3,u7=4,rN=5,FJ=100,tN=101,eN=102,JQ=103,$Q=104,NQ=200,QQ=201,ZQ=202,WQ=203,KQ=204,YQ=205,HQ=206,XQ=207,UQ=208,GQ=209,EQ=210,DQ=211,qQ=212,kQ=213,OQ=214,P6=0,A6=1,T6=2,lJ=3,S6=4,b6=5,y6=6,j6=7,RQ=0,FQ=1,MQ=2,M9=0,_Q=1,LQ=2,VQ=3,wQ=4,zQ=5,BQ=6,CQ=7;var MJ=301,p9=302,f6=303,v6=304,mJ=306,IQ=1000,PQ=1001,AQ=1002,_J=1003,TQ=1004;var uJ=1005;var d9=1006,h6=1007;var LJ=1008;var S9=1009,SQ=1010,bQ=1011,cJ=1012,c7=1013,VJ=1014,b9=1015,nJ=1016,n7=1017,s7=1018,wJ=1020,yQ=35902,jQ=35899,fQ=1021,vQ=1022,K9=1023,x6=1026,sJ=1027,hQ=1028,i7=1029,xQ=1030,o7=1031;var a7=1033,g6=33776,p6=33777,d6=33778,l6=33779,r7=35840,t7=35841,e7=35842,J$=35843,$$=36196,N$=37492,Q$=37496,Z$=37808,W$=37809,K$=37810,Y$=37811,H$=37812,X$=37813,U$=37814,G$=37815,E$=37816,D$=37817,q$=37818,k$=37819,O$=37820,R$=37821,F$=36492,M$=36494,_$=36495,L$=36283,V$=36284,w$=36285,z$=36286;var gQ=3201;var pQ=0,dQ=1,l9="",lQ="srgb",iJ="srgb-linear",B$="linear",Y8="srgb";var mQ=512,uQ=513,cQ=514,C$=515,nQ=516,sQ=517,iQ=518,oQ=519;var I$="300 es",P$=2000;class _9{addEventListener(J,$){if(this._listeners===void 0)this._listeners={};let N=this._listeners;if(N[J]===void 0)N[J]=[];if(N[J].indexOf($)===-1)N[J].push($)}hasEventListener(J,$){let N=this._listeners;if(N===void 0)return!1;return N[J]!==void 0&&N[J].indexOf($)!==-1}removeEventListener(J,$){let N=this._listeners;if(N===void 0)return;let Q=N[J];if(Q!==void 0){let Z=Q.indexOf($);if(Z!==-1)Q.splice(Z,1)}}dispatchEvent(J){let $=this._listeners;if($===void 0)return;let N=$[J.type];if(N!==void 0){J.target=this;let Q=N.slice(0);for(let Z=0,W=Q.length;Z<W;Z++)Q[Z].call(this,J);J.target=null}}}var w8=["00","01","02","03","04","05","06","07","08","09","0a","0b","0c","0d","0e","0f","10","11","12","13","14","15","16","17","18","19","1a","1b","1c","1d","1e","1f","20","21","22","23","24","25","26","27","28","29","2a","2b","2c","2d","2e","2f","30","31","32","33","34","35","36","37","38","39","3a","3b","3c","3d","3e","3f","40","41","42","43","44","45","46","47","48","49","4a","4b","4c","4d","4e","4f","50","51","52","53","54","55","56","57","58","59","5a","5b","5c","5d","5e","5f","60","61","62","63","64","65","66","67","68","69","6a","6b","6c","6d","6e","6f","70","71","72","73","74","75","76","77","78","79","7a","7b","7c","7d","7e","7f","80","81","82","83","84","85","86","87","88","89","8a","8b","8c","8d","8e","8f","90","91","92","93","94","95","96","97","98","99","9a","9b","9c","9d","9e","9f","a0","a1","a2","a3","a4","a5","a6","a7","a8","a9","aa","ab","ac","ad","ae","af","b0","b1","b2","b3","b4","b5","b6","b7","b8","b9","ba","bb","bc","bd","be","bf","c0","c1","c2","c3","c4","c5","c6","c7","c8","c9","ca","cb","cc","cd","ce","cf","d0","d1","d2","d3","d4","d5","d6","d7","d8","d9","da","db","dc","dd","de","df","e0","e1","e2","e3","e4","e5","e6","e7","e8","e9","ea","eb","ec","ed","ee","ef","f0","f1","f2","f3","f4","f5","f6","f7","f8","f9","fa","fb","fc","fd","fe","ff"],wN=1234567,xJ=Math.PI/180,kJ=180/Math.PI;function zJ(){let J=Math.random()*4294967295|0,$=Math.random()*4294967295|0,N=Math.random()*4294967295|0,Q=Math.random()*4294967295|0;return(w8[J&255]+w8[J>>8&255]+w8[J>>16&255]+w8[J>>24&255]+"-"+w8[$&255]+w8[$>>8&255]+"-"+w8[$>>16&15|64]+w8[$>>24&255]+"-"+w8[N&63|128]+w8[N>>8&255]+"-"+w8[N>>16&255]+w8[N>>24&255]+w8[Q&255]+w8[Q>>8&255]+w8[Q>>16&255]+w8[Q>>24&255]).toLowerCase()}function n0(J,$,N){return Math.max($,Math.min(N,J))}function A$(J,$){return(J%$+$)%$}function tZ(J,$,N,Q,Z){return Q+(J-$)*(Z-Q)/(N-$)}function eZ(J,$,N){if(J!==$)return(N-J)/($-J);else return 0}function gJ(J,$,N){return(1-N)*J+N*$}function JW(J,$,N,Q){return gJ(J,$,1-Math.exp(-N*Q))}function $W(J,$=1){return $-Math.abs(A$(J,$*2)-$)}function NW(J,$,N){if(J<=$)return 0;if(J>=N)return 1;return J=(J-$)/(N-$),J*J*(3-2*J)}function QW(J,$,N){if(J<=$)return 0;if(J>=N)return 1;return J=(J-$)/(N-$),J*J*J*(J*(J*6-15)+10)}function ZW(J,$){return J+Math.floor(Math.random()*($-J+1))}function WW(J,$){return J+Math.random()*($-J)}function KW(J){return J*(0.5-Math.random())}function YW(J){if(J!==void 0)wN=J;let $=wN+=1831565813;return $=Math.imul($^$>>>15,$|1),$^=$+Math.imul($^$>>>7,$|61),(($^$>>>14)>>>0)/4294967296}function HW(J){return J*xJ}function XW(J){return J*kJ}function UW(J){return(J&J-1)===0&&J!==0}function GW(J){return Math.pow(2,Math.ceil(Math.log(J)/Math.LN2))}function EW(J){return Math.pow(2,Math.floor(Math.log(J)/Math.LN2))}function DW(J,$,N,Q,Z){let{cos:W,sin:Y}=Math,K=W(N/2),H=Y(N/2),X=W(($+Q)/2),G=Y(($+Q)/2),E=W(($-Q)/2),q=Y(($-Q)/2),k=W((Q-$)/2),M=Y((Q-$)/2);switch(Z){case"XYX":J.set(K*G,H*E,H*q,K*X);break;case"YZY":J.set(H*q,K*G,H*E,K*X);break;case"ZXZ":J.set(H*E,H*q,K*G,K*X);break;case"XZX":J.set(K*G,H*M,H*k,K*X);break;case"YXY":J.set(H*k,K*G,H*M,K*X);break;case"ZYZ":J.set(H*M,H*k,K*G,K*X);break;default:console.warn("THREE.MathUtils: .setQuaternionFromProperEuler() encountered an unknown order: "+Z)}}function DJ(J,$){switch($.constructor){case Float32Array:return J;case Uint32Array:return J/4294967295;case Uint16Array:return J/65535;case Uint8Array:return J/255;case Int32Array:return Math.max(J/2147483647,-1);case Int16Array:return Math.max(J/32767,-1);case Int8Array:return Math.max(J/127,-1);default:throw Error("Invalid component type.")}}function A8(J,$){switch($.constructor){case Float32Array:return J;case Uint32Array:return Math.round(J*4294967295);case Uint16Array:return Math.round(J*65535);case Uint8Array:return Math.round(J*255);case Int32Array:return Math.round(J*2147483647);case Int16Array:return Math.round(J*32767);case Int8Array:return Math.round(J*127);default:throw Error("Invalid component type.")}}var T$={DEG2RAD:xJ,RAD2DEG:kJ,generateUUID:zJ,clamp:n0,euclideanModulo:A$,mapLinear:tZ,inverseLerp:eZ,lerp:gJ,damp:JW,pingpong:$W,smoothstep:NW,smootherstep:QW,randInt:ZW,randFloat:WW,randFloatSpread:KW,seededRandom:YW,degToRad:HW,radToDeg:XW,isPowerOfTwo:UW,ceilPowerOfTwo:GW,floorPowerOfTwo:EW,setQuaternionFromProperEuler:DW,normalize:A8,denormalize:DJ};class p0{constructor(J=0,$=0){p0.prototype.isVector2=!0,this.x=J,this.y=$}get width(){return this.x}set width(J){this.x=J}get height(){return this.y}set height(J){this.y=J}set(J,$){return this.x=J,this.y=$,this}setScalar(J){return this.x=J,this.y=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y)}copy(J){return this.x=J.x,this.y=J.y,this}add(J){return this.x+=J.x,this.y+=J.y,this}addScalar(J){return this.x+=J,this.y+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this}subScalar(J){return this.x-=J,this.y-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this}multiply(J){return this.x*=J.x,this.y*=J.y,this}multiplyScalar(J){return this.x*=J,this.y*=J,this}divide(J){return this.x/=J.x,this.y/=J.y,this}divideScalar(J){return this.multiplyScalar(1/J)}applyMatrix3(J){let $=this.x,N=this.y,Q=J.elements;return this.x=Q[0]*$+Q[3]*N+Q[6],this.y=Q[1]*$+Q[4]*N+Q[7],this}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this}clamp(J,$){return this.x=n0(this.x,J.x,$.x),this.y=n0(this.y,J.y,$.y),this}clampScalar(J,$){return this.x=n0(this.x,J,$),this.y=n0(this.y,J,$),this}clampLength(J,$){let N=this.length();return this.divideScalar(N||1).multiplyScalar(n0(N,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this}negate(){return this.x=-this.x,this.y=-this.y,this}dot(J){return this.x*J.x+this.y*J.y}cross(J){return this.x*J.y-this.y*J.x}lengthSq(){return this.x*this.x+this.y*this.y}length(){return Math.sqrt(this.x*this.x+this.y*this.y)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)}normalize(){return this.divideScalar(this.length()||1)}angle(){return Math.atan2(-this.y,-this.x)+Math.PI}angleTo(J){let $=Math.sqrt(this.lengthSq()*J.lengthSq());if($===0)return Math.PI/2;let N=this.dot(J)/$;return Math.acos(n0(N,-1,1))}distanceTo(J){return Math.sqrt(this.distanceToSquared(J))}distanceToSquared(J){let $=this.x-J.x,N=this.y-J.y;return $*$+N*N}manhattanDistanceTo(J){return Math.abs(this.x-J.x)+Math.abs(this.y-J.y)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this}lerpVectors(J,$,N){return this.x=J.x+($.x-J.x)*N,this.y=J.y+($.y-J.y)*N,this}equals(J){return J.x===this.x&&J.y===this.y}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this}rotateAround(J,$){let N=Math.cos($),Q=Math.sin($),Z=this.x-J.x,W=this.y-J.y;return this.x=Z*N-W*Q+J.x,this.y=Z*Q+W*N+J.y,this}random(){return this.x=Math.random(),this.y=Math.random(),this}*[Symbol.iterator](){yield this.x,yield this.y}}class N9{constructor(J=0,$=0,N=0,Q=1){this.isQuaternion=!0,this._x=J,this._y=$,this._z=N,this._w=Q}static slerpFlat(J,$,N,Q,Z,W,Y){let K=N[Q+0],H=N[Q+1],X=N[Q+2],G=N[Q+3],E=Z[W+0],q=Z[W+1],k=Z[W+2],M=Z[W+3];if(Y===0){J[$+0]=K,J[$+1]=H,J[$+2]=X,J[$+3]=G;return}if(Y===1){J[$+0]=E,J[$+1]=q,J[$+2]=k,J[$+3]=M;return}if(G!==M||K!==E||H!==q||X!==k){let _=1-Y,D=K*E+H*q+X*k+G*M,U=D>=0?1:-1,C=1-D*D;if(C>Number.EPSILON){let B=Math.sqrt(C),y=Math.atan2(B,D*U);_=Math.sin(_*y)/B,Y=Math.sin(Y*y)/B}let L=Y*U;if(K=K*_+E*L,H=H*_+q*L,X=X*_+k*L,G=G*_+M*L,_===1-Y){let B=1/Math.sqrt(K*K+H*H+X*X+G*G);K*=B,H*=B,X*=B,G*=B}}J[$]=K,J[$+1]=H,J[$+2]=X,J[$+3]=G}static multiplyQuaternionsFlat(J,$,N,Q,Z,W){let Y=N[Q],K=N[Q+1],H=N[Q+2],X=N[Q+3],G=Z[W],E=Z[W+1],q=Z[W+2],k=Z[W+3];return J[$]=Y*k+X*G+K*q-H*E,J[$+1]=K*k+X*E+H*G-Y*q,J[$+2]=H*k+X*q+Y*E-K*G,J[$+3]=X*k-Y*G-K*E-H*q,J}get x(){return this._x}set x(J){this._x=J,this._onChangeCallback()}get y(){return this._y}set y(J){this._y=J,this._onChangeCallback()}get z(){return this._z}set z(J){this._z=J,this._onChangeCallback()}get w(){return this._w}set w(J){this._w=J,this._onChangeCallback()}set(J,$,N,Q){return this._x=J,this._y=$,this._z=N,this._w=Q,this._onChangeCallback(),this}clone(){return new this.constructor(this._x,this._y,this._z,this._w)}copy(J){return this._x=J.x,this._y=J.y,this._z=J.z,this._w=J.w,this._onChangeCallback(),this}setFromEuler(J,$=!0){let{_x:N,_y:Q,_z:Z,_order:W}=J,Y=Math.cos,K=Math.sin,H=Y(N/2),X=Y(Q/2),G=Y(Z/2),E=K(N/2),q=K(Q/2),k=K(Z/2);switch(W){case"XYZ":this._x=E*X*G+H*q*k,this._y=H*q*G-E*X*k,this._z=H*X*k+E*q*G,this._w=H*X*G-E*q*k;break;case"YXZ":this._x=E*X*G+H*q*k,this._y=H*q*G-E*X*k,this._z=H*X*k-E*q*G,this._w=H*X*G+E*q*k;break;case"ZXY":this._x=E*X*G-H*q*k,this._y=H*q*G+E*X*k,this._z=H*X*k+E*q*G,this._w=H*X*G-E*q*k;break;case"ZYX":this._x=E*X*G-H*q*k,this._y=H*q*G+E*X*k,this._z=H*X*k-E*q*G,this._w=H*X*G+E*q*k;break;case"YZX":this._x=E*X*G+H*q*k,this._y=H*q*G+E*X*k,this._z=H*X*k-E*q*G,this._w=H*X*G-E*q*k;break;case"XZY":this._x=E*X*G-H*q*k,this._y=H*q*G-E*X*k,this._z=H*X*k+E*q*G,this._w=H*X*G+E*q*k;break;default:console.warn("THREE.Quaternion: .setFromEuler() encountered an unknown order: "+W)}if($===!0)this._onChangeCallback();return this}setFromAxisAngle(J,$){let N=$/2,Q=Math.sin(N);return this._x=J.x*Q,this._y=J.y*Q,this._z=J.z*Q,this._w=Math.cos(N),this._onChangeCallback(),this}setFromRotationMatrix(J){let $=J.elements,N=$[0],Q=$[4],Z=$[8],W=$[1],Y=$[5],K=$[9],H=$[2],X=$[6],G=$[10],E=N+Y+G;if(E>0){let q=0.5/Math.sqrt(E+1);this._w=0.25/q,this._x=(X-K)*q,this._y=(Z-H)*q,this._z=(W-Q)*q}else if(N>Y&&N>G){let q=2*Math.sqrt(1+N-Y-G);this._w=(X-K)/q,this._x=0.25*q,this._y=(Q+W)/q,this._z=(Z+H)/q}else if(Y>G){let q=2*Math.sqrt(1+Y-N-G);this._w=(Z-H)/q,this._x=(Q+W)/q,this._y=0.25*q,this._z=(K+X)/q}else{let q=2*Math.sqrt(1+G-N-Y);this._w=(W-Q)/q,this._x=(Z+H)/q,this._y=(K+X)/q,this._z=0.25*q}return this._onChangeCallback(),this}setFromUnitVectors(J,$){let N=J.dot($)+1;if(N<0.00000001)if(N=0,Math.abs(J.x)>Math.abs(J.z))this._x=-J.y,this._y=J.x,this._z=0,this._w=N;else this._x=0,this._y=-J.z,this._z=J.y,this._w=N;else this._x=J.y*$.z-J.z*$.y,this._y=J.z*$.x-J.x*$.z,this._z=J.x*$.y-J.y*$.x,this._w=N;return this.normalize()}angleTo(J){return 2*Math.acos(Math.abs(n0(this.dot(J),-1,1)))}rotateTowards(J,$){let N=this.angleTo(J);if(N===0)return this;let Q=Math.min(1,$/N);return this.slerp(J,Q),this}identity(){return this.set(0,0,0,1)}invert(){return this.conjugate()}conjugate(){return this._x*=-1,this._y*=-1,this._z*=-1,this._onChangeCallback(),this}dot(J){return this._x*J._x+this._y*J._y+this._z*J._z+this._w*J._w}lengthSq(){return this._x*this._x+this._y*this._y+this._z*this._z+this._w*this._w}length(){return Math.sqrt(this._x*this._x+this._y*this._y+this._z*this._z+this._w*this._w)}normalize(){let J=this.length();if(J===0)this._x=0,this._y=0,this._z=0,this._w=1;else J=1/J,this._x=this._x*J,this._y=this._y*J,this._z=this._z*J,this._w=this._w*J;return this._onChangeCallback(),this}multiply(J){return this.multiplyQuaternions(this,J)}premultiply(J){return this.multiplyQuaternions(J,this)}multiplyQuaternions(J,$){let{_x:N,_y:Q,_z:Z,_w:W}=J,Y=$._x,K=$._y,H=$._z,X=$._w;return this._x=N*X+W*Y+Q*H-Z*K,this._y=Q*X+W*K+Z*Y-N*H,this._z=Z*X+W*H+N*K-Q*Y,this._w=W*X-N*Y-Q*K-Z*H,this._onChangeCallback(),this}slerp(J,$){if($===0)return this;if($===1)return this.copy(J);let N=this._x,Q=this._y,Z=this._z,W=this._w,Y=W*J._w+N*J._x+Q*J._y+Z*J._z;if(Y<0)this._w=-J._w,this._x=-J._x,this._y=-J._y,this._z=-J._z,Y=-Y;else this.copy(J);if(Y>=1)return this._w=W,this._x=N,this._y=Q,this._z=Z,this;let K=1-Y*Y;if(K<=Number.EPSILON){let q=1-$;return this._w=q*W+$*this._w,this._x=q*N+$*this._x,this._y=q*Q+$*this._y,this._z=q*Z+$*this._z,this.normalize(),this}let H=Math.sqrt(K),X=Math.atan2(H,Y),G=Math.sin((1-$)*X)/H,E=Math.sin($*X)/H;return this._w=W*G+this._w*E,this._x=N*G+this._x*E,this._y=Q*G+this._y*E,this._z=Z*G+this._z*E,this._onChangeCallback(),this}slerpQuaternions(J,$,N){return this.copy(J).slerp($,N)}random(){let J=2*Math.PI*Math.random(),$=2*Math.PI*Math.random(),N=Math.random(),Q=Math.sqrt(1-N),Z=Math.sqrt(N);return this.set(Q*Math.sin(J),Q*Math.cos(J),Z*Math.sin($),Z*Math.cos($))}equals(J){return J._x===this._x&&J._y===this._y&&J._z===this._z&&J._w===this._w}fromArray(J,$=0){return this._x=J[$],this._y=J[$+1],this._z=J[$+2],this._w=J[$+3],this._onChangeCallback(),this}toArray(J=[],$=0){return J[$]=this._x,J[$+1]=this._y,J[$+2]=this._z,J[$+3]=this._w,J}fromBufferAttribute(J,$){return this._x=J.getX($),this._y=J.getY($),this._z=J.getZ($),this._w=J.getW($),this._onChangeCallback(),this}toJSON(){return this.toArray()}_onChange(J){return this._onChangeCallback=J,this}_onChangeCallback(){}*[Symbol.iterator](){yield this._x,yield this._y,yield this._z,yield this._w}}class h{constructor(J=0,$=0,N=0){h.prototype.isVector3=!0,this.x=J,this.y=$,this.z=N}set(J,$,N){if(N===void 0)N=this.z;return this.x=J,this.y=$,this.z=N,this}setScalar(J){return this.x=J,this.y=J,this.z=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setZ(J){return this.z=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;case 2:this.z=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;case 2:return this.z;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y,this.z)}copy(J){return this.x=J.x,this.y=J.y,this.z=J.z,this}add(J){return this.x+=J.x,this.y+=J.y,this.z+=J.z,this}addScalar(J){return this.x+=J,this.y+=J,this.z+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this.z=J.z+$.z,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this.z+=J.z*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this.z-=J.z,this}subScalar(J){return this.x-=J,this.y-=J,this.z-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this.z=J.z-$.z,this}multiply(J){return this.x*=J.x,this.y*=J.y,this.z*=J.z,this}multiplyScalar(J){return this.x*=J,this.y*=J,this.z*=J,this}multiplyVectors(J,$){return this.x=J.x*$.x,this.y=J.y*$.y,this.z=J.z*$.z,this}applyEuler(J){return this.applyQuaternion(zN.setFromEuler(J))}applyAxisAngle(J,$){return this.applyQuaternion(zN.setFromAxisAngle(J,$))}applyMatrix3(J){let $=this.x,N=this.y,Q=this.z,Z=J.elements;return this.x=Z[0]*$+Z[3]*N+Z[6]*Q,this.y=Z[1]*$+Z[4]*N+Z[7]*Q,this.z=Z[2]*$+Z[5]*N+Z[8]*Q,this}applyNormalMatrix(J){return this.applyMatrix3(J).normalize()}applyMatrix4(J){let $=this.x,N=this.y,Q=this.z,Z=J.elements,W=1/(Z[3]*$+Z[7]*N+Z[11]*Q+Z[15]);return this.x=(Z[0]*$+Z[4]*N+Z[8]*Q+Z[12])*W,this.y=(Z[1]*$+Z[5]*N+Z[9]*Q+Z[13])*W,this.z=(Z[2]*$+Z[6]*N+Z[10]*Q+Z[14])*W,this}applyQuaternion(J){let $=this.x,N=this.y,Q=this.z,Z=J.x,W=J.y,Y=J.z,K=J.w,H=2*(W*Q-Y*N),X=2*(Y*$-Z*Q),G=2*(Z*N-W*$);return this.x=$+K*H+W*G-Y*X,this.y=N+K*X+Y*H-Z*G,this.z=Q+K*G+Z*X-W*H,this}project(J){return this.applyMatrix4(J.matrixWorldInverse).applyMatrix4(J.projectionMatrix)}unproject(J){return this.applyMatrix4(J.projectionMatrixInverse).applyMatrix4(J.matrixWorld)}transformDirection(J){let $=this.x,N=this.y,Q=this.z,Z=J.elements;return this.x=Z[0]*$+Z[4]*N+Z[8]*Q,this.y=Z[1]*$+Z[5]*N+Z[9]*Q,this.z=Z[2]*$+Z[6]*N+Z[10]*Q,this.normalize()}divide(J){return this.x/=J.x,this.y/=J.y,this.z/=J.z,this}divideScalar(J){return this.multiplyScalar(1/J)}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this.z=Math.min(this.z,J.z),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this.z=Math.max(this.z,J.z),this}clamp(J,$){return this.x=n0(this.x,J.x,$.x),this.y=n0(this.y,J.y,$.y),this.z=n0(this.z,J.z,$.z),this}clampScalar(J,$){return this.x=n0(this.x,J,$),this.y=n0(this.y,J,$),this.z=n0(this.z,J,$),this}clampLength(J,$){let N=this.length();return this.divideScalar(N||1).multiplyScalar(n0(N,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this.z=Math.floor(this.z),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this.z=Math.ceil(this.z),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this.z=Math.round(this.z),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this.z=Math.trunc(this.z),this}negate(){return this.x=-this.x,this.y=-this.y,this.z=-this.z,this}dot(J){return this.x*J.x+this.y*J.y+this.z*J.z}lengthSq(){return this.x*this.x+this.y*this.y+this.z*this.z}length(){return Math.sqrt(this.x*this.x+this.y*this.y+this.z*this.z)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)+Math.abs(this.z)}normalize(){return this.divideScalar(this.length()||1)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this.z+=(J.z-this.z)*$,this}lerpVectors(J,$,N){return this.x=J.x+($.x-J.x)*N,this.y=J.y+($.y-J.y)*N,this.z=J.z+($.z-J.z)*N,this}cross(J){return this.crossVectors(this,J)}crossVectors(J,$){let{x:N,y:Q,z:Z}=J,W=$.x,Y=$.y,K=$.z;return this.x=Q*K-Z*Y,this.y=Z*W-N*K,this.z=N*Y-Q*W,this}projectOnVector(J){let $=J.lengthSq();if($===0)return this.set(0,0,0);let N=J.dot(this)/$;return this.copy(J).multiplyScalar(N)}projectOnPlane(J){return O7.copy(this).projectOnVector(J),this.sub(O7)}reflect(J){return this.sub(O7.copy(J).multiplyScalar(2*this.dot(J)))}angleTo(J){let $=Math.sqrt(this.lengthSq()*J.lengthSq());if($===0)return Math.PI/2;let N=this.dot(J)/$;return Math.acos(n0(N,-1,1))}distanceTo(J){return Math.sqrt(this.distanceToSquared(J))}distanceToSquared(J){let $=this.x-J.x,N=this.y-J.y,Q=this.z-J.z;return $*$+N*N+Q*Q}manhattanDistanceTo(J){return Math.abs(this.x-J.x)+Math.abs(this.y-J.y)+Math.abs(this.z-J.z)}setFromSpherical(J){return this.setFromSphericalCoords(J.radius,J.phi,J.theta)}setFromSphericalCoords(J,$,N){let Q=Math.sin($)*J;return this.x=Q*Math.sin(N),this.y=Math.cos($)*J,this.z=Q*Math.cos(N),this}setFromCylindrical(J){return this.setFromCylindricalCoords(J.radius,J.theta,J.y)}setFromCylindricalCoords(J,$,N){return this.x=J*Math.sin($),this.y=N,this.z=J*Math.cos($),this}setFromMatrixPosition(J){let $=J.elements;return this.x=$[12],this.y=$[13],this.z=$[14],this}setFromMatrixScale(J){let $=this.setFromMatrixColumn(J,0).length(),N=this.setFromMatrixColumn(J,1).length(),Q=this.setFromMatrixColumn(J,2).length();return this.x=$,this.y=N,this.z=Q,this}setFromMatrixColumn(J,$){return this.fromArray(J.elements,$*4)}setFromMatrix3Column(J,$){return this.fromArray(J.elements,$*3)}setFromEuler(J){return this.x=J._x,this.y=J._y,this.z=J._z,this}setFromColor(J){return this.x=J.r,this.y=J.g,this.z=J.b,this}equals(J){return J.x===this.x&&J.y===this.y&&J.z===this.z}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this.z=J[$+2],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J[$+2]=this.z,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this.z=J.getZ($),this}random(){return this.x=Math.random(),this.y=Math.random(),this.z=Math.random(),this}randomDirection(){let J=Math.random()*Math.PI*2,$=Math.random()*2-1,N=Math.sqrt(1-$*$);return this.x=N*Math.cos(J),this.y=$,this.z=N*Math.sin(J),this}*[Symbol.iterator](){yield this.x,yield this.y,yield this.z}}var O7=new h,zN=new N9;class l0{constructor(J,$,N,Q,Z,W,Y,K,H){if(l0.prototype.isMatrix3=!0,this.elements=[1,0,0,0,1,0,0,0,1],J!==void 0)this.set(J,$,N,Q,Z,W,Y,K,H)}set(J,$,N,Q,Z,W,Y,K,H){let X=this.elements;return X[0]=J,X[1]=Q,X[2]=Y,X[3]=$,X[4]=Z,X[5]=K,X[6]=N,X[7]=W,X[8]=H,this}identity(){return this.set(1,0,0,0,1,0,0,0,1),this}copy(J){let $=this.elements,N=J.elements;return $[0]=N[0],$[1]=N[1],$[2]=N[2],$[3]=N[3],$[4]=N[4],$[5]=N[5],$[6]=N[6],$[7]=N[7],$[8]=N[8],this}extractBasis(J,$,N){return J.setFromMatrix3Column(this,0),$.setFromMatrix3Column(this,1),N.setFromMatrix3Column(this,2),this}setFromMatrix4(J){let $=J.elements;return this.set($[0],$[4],$[8],$[1],$[5],$[9],$[2],$[6],$[10]),this}multiply(J){return this.multiplyMatrices(this,J)}premultiply(J){return this.multiplyMatrices(J,this)}multiplyMatrices(J,$){let N=J.elements,Q=$.elements,Z=this.elements,W=N[0],Y=N[3],K=N[6],H=N[1],X=N[4],G=N[7],E=N[2],q=N[5],k=N[8],M=Q[0],_=Q[3],D=Q[6],U=Q[1],C=Q[4],L=Q[7],B=Q[2],y=Q[5],I=Q[8];return Z[0]=W*M+Y*U+K*B,Z[3]=W*_+Y*C+K*y,Z[6]=W*D+Y*L+K*I,Z[1]=H*M+X*U+G*B,Z[4]=H*_+X*C+G*y,Z[7]=H*D+X*L+G*I,Z[2]=E*M+q*U+k*B,Z[5]=E*_+q*C+k*y,Z[8]=E*D+q*L+k*I,this}multiplyScalar(J){let $=this.elements;return $[0]*=J,$[3]*=J,$[6]*=J,$[1]*=J,$[4]*=J,$[7]*=J,$[2]*=J,$[5]*=J,$[8]*=J,this}determinant(){let J=this.elements,$=J[0],N=J[1],Q=J[2],Z=J[3],W=J[4],Y=J[5],K=J[6],H=J[7],X=J[8];return $*W*X-$*Y*H-N*Z*X+N*Y*K+Q*Z*H-Q*W*K}invert(){let J=this.elements,$=J[0],N=J[1],Q=J[2],Z=J[3],W=J[4],Y=J[5],K=J[6],H=J[7],X=J[8],G=X*W-Y*H,E=Y*K-X*Z,q=H*Z-W*K,k=$*G+N*E+Q*q;if(k===0)return this.set(0,0,0,0,0,0,0,0,0);let M=1/k;return J[0]=G*M,J[1]=(Q*H-X*N)*M,J[2]=(Y*N-Q*W)*M,J[3]=E*M,J[4]=(X*$-Q*K)*M,J[5]=(Q*Z-Y*$)*M,J[6]=q*M,J[7]=(N*K-H*$)*M,J[8]=(W*$-N*Z)*M,this}transpose(){let J,$=this.elements;return J=$[1],$[1]=$[3],$[3]=J,J=$[2],$[2]=$[6],$[6]=J,J=$[5],$[5]=$[7],$[7]=J,this}getNormalMatrix(J){return this.setFromMatrix4(J).invert().transpose()}transposeIntoArray(J){let $=this.elements;return J[0]=$[0],J[1]=$[3],J[2]=$[6],J[3]=$[1],J[4]=$[4],J[5]=$[7],J[6]=$[2],J[7]=$[5],J[8]=$[8],this}setUvTransform(J,$,N,Q,Z,W,Y){let K=Math.cos(Z),H=Math.sin(Z);return this.set(N*K,N*H,-N*(K*W+H*Y)+W+J,-Q*H,Q*K,-Q*(-H*W+K*Y)+Y+$,0,0,1),this}scale(J,$){return this.premultiply(R7.makeScale(J,$)),this}rotate(J){return this.premultiply(R7.makeRotation(-J)),this}translate(J,$){return this.premultiply(R7.makeTranslation(J,$)),this}makeTranslation(J,$){if(J.isVector2)this.set(1,0,J.x,0,1,J.y,0,0,1);else this.set(1,0,J,0,1,$,0,0,1);return this}makeRotation(J){let $=Math.cos(J),N=Math.sin(J);return this.set($,-N,0,N,$,0,0,0,1),this}makeScale(J,$){return this.set(J,0,0,0,$,0,0,0,1),this}equals(J){let $=this.elements,N=J.elements;for(let Q=0;Q<9;Q++)if($[Q]!==N[Q])return!1;return!0}fromArray(J,$=0){for(let N=0;N<9;N++)this.elements[N]=J[N+$];return this}toArray(J=[],$=0){let N=this.elements;return J[$]=N[0],J[$+1]=N[1],J[$+2]=N[2],J[$+3]=N[3],J[$+4]=N[4],J[$+5]=N[5],J[$+6]=N[6],J[$+7]=N[7],J[$+8]=N[8],J}clone(){return new this.constructor().fromArray(this.elements)}}var R7=new l0;function S$(J){for(let $=J.length-1;$>=0;--$)if(J[$]>=65535)return!0;return!1}function pJ(J){return document.createElementNS("http://www.w3.org/1999/xhtml",J)}function aQ(){let J=pJ("canvas");return J.style.display="block",J}var BN={};function OJ(J){if(J in BN)return;BN[J]=!0,console.warn(J)}function rQ(J,$,N){return new Promise(function(Q,Z){function W(){switch(J.clientWaitSync($,J.SYNC_FLUSH_COMMANDS_BIT,0)){case J.WAIT_FAILED:Z();break;case J.TIMEOUT_EXPIRED:setTimeout(W,N);break;default:Q()}}setTimeout(W,N)})}var CN=new l0().set(0.4123908,0.3575843,0.1804808,0.212639,0.7151687,0.0721923,0.0193308,0.1191948,0.9505322),IN=new l0().set(3.2409699,-1.5373832,-0.4986108,-0.9692436,1.8759675,0.0415551,0.0556301,-0.203977,1.0569715);function qW(){let J={enabled:!0,workingColorSpace:"srgb-linear",spaces:{},convert:function(Z,W,Y){if(this.enabled===!1||W===Y||!W||!Y)return Z;if(this.spaces[W].transfer==="srgb")Z.r=F9(Z.r),Z.g=F9(Z.g),Z.b=F9(Z.b);if(this.spaces[W].primaries!==this.spaces[Y].primaries)Z.applyMatrix3(this.spaces[W].toXYZ),Z.applyMatrix3(this.spaces[Y].fromXYZ);if(this.spaces[Y].transfer==="srgb")Z.r=qJ(Z.r),Z.g=qJ(Z.g),Z.b=qJ(Z.b);return Z},workingToColorSpace:function(Z,W){return this.convert(Z,this.workingColorSpace,W)},colorSpaceToWorking:function(Z,W){return this.convert(Z,W,this.workingColorSpace)},getPrimaries:function(Z){return this.spaces[Z].primaries},getTransfer:function(Z){if(Z==="")return"linear";return this.spaces[Z].transfer},getToneMappingMode:function(Z){return this.spaces[Z].outputColorSpaceConfig.toneMappingMode||"standard"},getLuminanceCoefficients:function(Z,W=this.workingColorSpace){return Z.fromArray(this.spaces[W].luminanceCoefficients)},define:function(Z){Object.assign(this.spaces,Z)},_getMatrix:function(Z,W,Y){return Z.copy(this.spaces[W].toXYZ).multiply(this.spaces[Y].fromXYZ)},_getDrawingBufferColorSpace:function(Z){return this.spaces[Z].outputColorSpaceConfig.drawingBufferColorSpace},_getUnpackColorSpace:function(Z=this.workingColorSpace){return this.spaces[Z].workingColorSpaceConfig.unpackColorSpace},fromWorkingColorSpace:function(Z,W){return OJ("THREE.ColorManagement: .fromWorkingColorSpace() has been renamed to .workingToColorSpace()."),J.workingToColorSpace(Z,W)},toWorkingColorSpace:function(Z,W){return OJ("THREE.ColorManagement: .toWorkingColorSpace() has been renamed to .colorSpaceToWorking()."),J.colorSpaceToWorking(Z,W)}},$=[0.64,0.33,0.3,0.6,0.15,0.06],N=[0.2126,0.7152,0.0722],Q=[0.3127,0.329];return J.define({["srgb-linear"]:{primaries:$,whitePoint:Q,transfer:"linear",toXYZ:CN,fromXYZ:IN,luminanceCoefficients:N,workingColorSpaceConfig:{unpackColorSpace:"srgb"},outputColorSpaceConfig:{drawingBufferColorSpace:"srgb"}},["srgb"]:{primaries:$,whitePoint:Q,transfer:"srgb",toXYZ:CN,fromXYZ:IN,luminanceCoefficients:N,outputColorSpaceConfig:{drawingBufferColorSpace:"srgb"}}}),J}var o0=qW();function F9(J){return J<0.04045?J*0.0773993808:Math.pow(J*0.9478672986+0.0521327014,2.4)}function qJ(J){return J<0.0031308?J*12.92:1.055*Math.pow(J,0.41666)-0.055}var $J;class b${static getDataURL(J,$="image/png"){if(/^data:/i.test(J.src))return J.src;if(typeof HTMLCanvasElement>"u")return J.src;let N;if(J instanceof HTMLCanvasElement)N=J;else{if($J===void 0)$J=pJ("canvas");$J.width=J.width,$J.height=J.height;let Q=$J.getContext("2d");if(J instanceof ImageData)Q.putImageData(J,0,0);else Q.drawImage(J,0,0,J.width,J.height);N=$J}return N.toDataURL($)}static sRGBToLinear(J){if(typeof HTMLImageElement<"u"&&J instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&J instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&J instanceof ImageBitmap){let $=pJ("canvas");$.width=J.width,$.height=J.height;let N=$.getContext("2d");N.drawImage(J,0,0,J.width,J.height);let Q=N.getImageData(0,0,J.width,J.height),Z=Q.data;for(let W=0;W<Z.length;W++)Z[W]=F9(Z[W]/255)*255;return N.putImageData(Q,0,0),$}else if(J.data){let $=J.data.slice(0);for(let N=0;N<$.length;N++)if($ instanceof Uint8Array||$ instanceof Uint8ClampedArray)$[N]=Math.floor(F9($[N]/255)*255);else $[N]=F9($[N]);return{data:$,width:J.width,height:J.height}}else return console.warn("THREE.ImageUtils.sRGBToLinear(): Unsupported image type. No color space conversion applied."),J}}var kW=0;class oJ{constructor(J=null){this.isSource=!0,Object.defineProperty(this,"id",{value:kW++}),this.uuid=zJ(),this.data=J,this.dataReady=!0,this.version=0}getSize(J){let $=this.data;if(typeof HTMLVideoElement<"u"&&$ instanceof HTMLVideoElement)J.set($.videoWidth,$.videoHeight,0);else if($ instanceof VideoFrame)J.set($.displayHeight,$.displayWidth,0);else if($!==null)J.set($.width,$.height,$.depth||0);else J.set(0,0,0);return J}set needsUpdate(J){if(J===!0)this.version++}toJSON(J){let $=J===void 0||typeof J==="string";if(!$&&J.images[this.uuid]!==void 0)return J.images[this.uuid];let N={uuid:this.uuid,url:""},Q=this.data;if(Q!==null){let Z;if(Array.isArray(Q)){Z=[];for(let W=0,Y=Q.length;W<Y;W++)if(Q[W].isDataTexture)Z.push(F7(Q[W].image));else Z.push(F7(Q[W]))}else Z=F7(Q);N.url=Z}if(!$)J.images[this.uuid]=N;return N}}function F7(J){if(typeof HTMLImageElement<"u"&&J instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&J instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&J instanceof ImageBitmap)return b$.getDataURL(J);else if(J.data)return{data:Array.from(J.data),width:J.width,height:J.height,type:J.data.constructor.name};else return console.warn("THREE.Texture: Unable to serialize Texture."),{}}var OW=0,M7=new h;class S8 extends _9{constructor(J=S8.DEFAULT_IMAGE,$=S8.DEFAULT_MAPPING,N=1001,Q=1001,Z=1006,W=1008,Y=1023,K=1009,H=S8.DEFAULT_ANISOTROPY,X=""){super();this.isTexture=!0,Object.defineProperty(this,"id",{value:OW++}),this.uuid=zJ(),this.name="",this.source=new oJ(J),this.mipmaps=[],this.mapping=$,this.channel=0,this.wrapS=N,this.wrapT=Q,this.magFilter=Z,this.minFilter=W,this.anisotropy=H,this.format=Y,this.internalFormat=null,this.type=K,this.offset=new p0(0,0),this.repeat=new p0(1,1),this.center=new p0(0,0),this.rotation=0,this.matrixAutoUpdate=!0,this.matrix=new l0,this.generateMipmaps=!0,this.premultiplyAlpha=!1,this.flipY=!0,this.unpackAlignment=4,this.colorSpace=X,this.userData={},this.updateRanges=[],this.version=0,this.onUpdate=null,this.renderTarget=null,this.isRenderTargetTexture=!1,this.isArrayTexture=J&&J.depth&&J.depth>1?!0:!1,this.pmremVersion=0}get width(){return this.source.getSize(M7).x}get height(){return this.source.getSize(M7).y}get depth(){return this.source.getSize(M7).z}get image(){return this.source.data}set image(J=null){this.source.data=J}updateMatrix(){this.matrix.setUvTransform(this.offset.x,this.offset.y,this.repeat.x,this.repeat.y,this.rotation,this.center.x,this.center.y)}addUpdateRange(J,$){this.updateRanges.push({start:J,count:$})}clearUpdateRanges(){this.updateRanges.length=0}clone(){return new this.constructor().copy(this)}copy(J){return this.name=J.name,this.source=J.source,this.mipmaps=J.mipmaps.slice(0),this.mapping=J.mapping,this.channel=J.channel,this.wrapS=J.wrapS,this.wrapT=J.wrapT,this.magFilter=J.magFilter,this.minFilter=J.minFilter,this.anisotropy=J.anisotropy,this.format=J.format,this.internalFormat=J.internalFormat,this.type=J.type,this.offset.copy(J.offset),this.repeat.copy(J.repeat),this.center.copy(J.center),this.rotation=J.rotation,this.matrixAutoUpdate=J.matrixAutoUpdate,this.matrix.copy(J.matrix),this.generateMipmaps=J.generateMipmaps,this.premultiplyAlpha=J.premultiplyAlpha,this.flipY=J.flipY,this.unpackAlignment=J.unpackAlignment,this.colorSpace=J.colorSpace,this.renderTarget=J.renderTarget,this.isRenderTargetTexture=J.isRenderTargetTexture,this.isArrayTexture=J.isArrayTexture,this.userData=JSON.parse(JSON.stringify(J.userData)),this.needsUpdate=!0,this}setValues(J){for(let $ in J){let N=J[$];if(N===void 0){console.warn(`THREE.Texture.setValues(): parameter '${$}' has value of undefined.`);continue}let Q=this[$];if(Q===void 0){console.warn(`THREE.Texture.setValues(): property '${$}' does not exist.`);continue}if(Q&&N&&(Q.isVector2&&N.isVector2))Q.copy(N);else if(Q&&N&&(Q.isVector3&&N.isVector3))Q.copy(N);else if(Q&&N&&(Q.isMatrix3&&N.isMatrix3))Q.copy(N);else this[$]=N}}toJSON(J){let $=J===void 0||typeof J==="string";if(!$&&J.textures[this.uuid]!==void 0)return J.textures[this.uuid];let N={metadata:{version:4.7,type:"Texture",generator:"Texture.toJSON"},uuid:this.uuid,name:this.name,image:this.source.toJSON(J).uuid,mapping:this.mapping,channel:this.channel,repeat:[this.repeat.x,this.repeat.y],offset:[this.offset.x,this.offset.y],center:[this.center.x,this.center.y],rotation:this.rotation,wrap:[this.wrapS,this.wrapT],format:this.format,internalFormat:this.internalFormat,type:this.type,colorSpace:this.colorSpace,minFilter:this.minFilter,magFilter:this.magFilter,anisotropy:this.anisotropy,flipY:this.flipY,generateMipmaps:this.generateMipmaps,premultiplyAlpha:this.premultiplyAlpha,unpackAlignment:this.unpackAlignment};if(Object.keys(this.userData).length>0)N.userData=this.userData;if(!$)J.textures[this.uuid]=N;return N}dispose(){this.dispatchEvent({type:"dispose"})}transformUv(J){if(this.mapping!==300)return J;if(J.applyMatrix3(this.matrix),J.x<0||J.x>1)switch(this.wrapS){case 1000:J.x=J.x-Math.floor(J.x);break;case 1001:J.x=J.x<0?0:1;break;case 1002:if(Math.abs(Math.floor(J.x)%2)===1)J.x=Math.ceil(J.x)-J.x;else J.x=J.x-Math.floor(J.x);break}if(J.y<0||J.y>1)switch(this.wrapT){case 1000:J.y=J.y-Math.floor(J.y);break;case 1001:J.y=J.y<0?0:1;break;case 1002:if(Math.abs(Math.floor(J.y)%2)===1)J.y=Math.ceil(J.y)-J.y;else J.y=J.y-Math.floor(J.y);break}if(this.flipY)J.y=1-J.y;return J}set needsUpdate(J){if(J===!0)this.version++,this.source.needsUpdate=!0}set needsPMREMUpdate(J){if(J===!0)this.pmremVersion++}}S8.DEFAULT_IMAGE=null;S8.DEFAULT_MAPPING=300;S8.DEFAULT_ANISOTROPY=1;class D8{constructor(J=0,$=0,N=0,Q=1){D8.prototype.isVector4=!0,this.x=J,this.y=$,this.z=N,this.w=Q}get width(){return this.z}set width(J){this.z=J}get height(){return this.w}set height(J){this.w=J}set(J,$,N,Q){return this.x=J,this.y=$,this.z=N,this.w=Q,this}setScalar(J){return this.x=J,this.y=J,this.z=J,this.w=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setZ(J){return this.z=J,this}setW(J){return this.w=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;case 2:this.z=$;break;case 3:this.w=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;case 2:return this.z;case 3:return this.w;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y,this.z,this.w)}copy(J){return this.x=J.x,this.y=J.y,this.z=J.z,this.w=J.w!==void 0?J.w:1,this}add(J){return this.x+=J.x,this.y+=J.y,this.z+=J.z,this.w+=J.w,this}addScalar(J){return this.x+=J,this.y+=J,this.z+=J,this.w+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this.z=J.z+$.z,this.w=J.w+$.w,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this.z+=J.z*$,this.w+=J.w*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this.z-=J.z,this.w-=J.w,this}subScalar(J){return this.x-=J,this.y-=J,this.z-=J,this.w-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this.z=J.z-$.z,this.w=J.w-$.w,this}multiply(J){return this.x*=J.x,this.y*=J.y,this.z*=J.z,this.w*=J.w,this}multiplyScalar(J){return this.x*=J,this.y*=J,this.z*=J,this.w*=J,this}applyMatrix4(J){let $=this.x,N=this.y,Q=this.z,Z=this.w,W=J.elements;return this.x=W[0]*$+W[4]*N+W[8]*Q+W[12]*Z,this.y=W[1]*$+W[5]*N+W[9]*Q+W[13]*Z,this.z=W[2]*$+W[6]*N+W[10]*Q+W[14]*Z,this.w=W[3]*$+W[7]*N+W[11]*Q+W[15]*Z,this}divide(J){return this.x/=J.x,this.y/=J.y,this.z/=J.z,this.w/=J.w,this}divideScalar(J){return this.multiplyScalar(1/J)}setAxisAngleFromQuaternion(J){this.w=2*Math.acos(J.w);let $=Math.sqrt(1-J.w*J.w);if($<0.0001)this.x=1,this.y=0,this.z=0;else this.x=J.x/$,this.y=J.y/$,this.z=J.z/$;return this}setAxisAngleFromRotationMatrix(J){let $,N,Q,Z,W=0.01,Y=0.1,K=J.elements,H=K[0],X=K[4],G=K[8],E=K[1],q=K[5],k=K[9],M=K[2],_=K[6],D=K[10];if(Math.abs(X-E)<0.01&&Math.abs(G-M)<0.01&&Math.abs(k-_)<0.01){if(Math.abs(X+E)<0.1&&Math.abs(G+M)<0.1&&Math.abs(k+_)<0.1&&Math.abs(H+q+D-3)<0.1)return this.set(1,0,0,0),this;$=Math.PI;let C=(H+1)/2,L=(q+1)/2,B=(D+1)/2,y=(X+E)/4,I=(G+M)/4,b=(k+_)/4;if(C>L&&C>B)if(C<0.01)N=0,Q=0.707106781,Z=0.707106781;else N=Math.sqrt(C),Q=y/N,Z=I/N;else if(L>B)if(L<0.01)N=0.707106781,Q=0,Z=0.707106781;else Q=Math.sqrt(L),N=y/Q,Z=b/Q;else if(B<0.01)N=0.707106781,Q=0.707106781,Z=0;else Z=Math.sqrt(B),N=I/Z,Q=b/Z;return this.set(N,Q,Z,$),this}let U=Math.sqrt((_-k)*(_-k)+(G-M)*(G-M)+(E-X)*(E-X));if(Math.abs(U)<0.001)U=1;return this.x=(_-k)/U,this.y=(G-M)/U,this.z=(E-X)/U,this.w=Math.acos((H+q+D-1)/2),this}setFromMatrixPosition(J){let $=J.elements;return this.x=$[12],this.y=$[13],this.z=$[14],this.w=$[15],this}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this.z=Math.min(this.z,J.z),this.w=Math.min(this.w,J.w),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this.z=Math.max(this.z,J.z),this.w=Math.max(this.w,J.w),this}clamp(J,$){return this.x=n0(this.x,J.x,$.x),this.y=n0(this.y,J.y,$.y),this.z=n0(this.z,J.z,$.z),this.w=n0(this.w,J.w,$.w),this}clampScalar(J,$){return this.x=n0(this.x,J,$),this.y=n0(this.y,J,$),this.z=n0(this.z,J,$),this.w=n0(this.w,J,$),this}clampLength(J,$){let N=this.length();return this.divideScalar(N||1).multiplyScalar(n0(N,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this.z=Math.floor(this.z),this.w=Math.floor(this.w),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this.z=Math.ceil(this.z),this.w=Math.ceil(this.w),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this.z=Math.round(this.z),this.w=Math.round(this.w),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this.z=Math.trunc(this.z),this.w=Math.trunc(this.w),this}negate(){return this.x=-this.x,this.y=-this.y,this.z=-this.z,this.w=-this.w,this}dot(J){return this.x*J.x+this.y*J.y+this.z*J.z+this.w*J.w}lengthSq(){return this.x*this.x+this.y*this.y+this.z*this.z+this.w*this.w}length(){return Math.sqrt(this.x*this.x+this.y*this.y+this.z*this.z+this.w*this.w)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)+Math.abs(this.z)+Math.abs(this.w)}normalize(){return this.divideScalar(this.length()||1)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this.z+=(J.z-this.z)*$,this.w+=(J.w-this.w)*$,this}lerpVectors(J,$,N){return this.x=J.x+($.x-J.x)*N,this.y=J.y+($.y-J.y)*N,this.z=J.z+($.z-J.z)*N,this.w=J.w+($.w-J.w)*N,this}equals(J){return J.x===this.x&&J.y===this.y&&J.z===this.z&&J.w===this.w}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this.z=J[$+2],this.w=J[$+3],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J[$+2]=this.z,J[$+3]=this.w,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this.z=J.getZ($),this.w=J.getW($),this}random(){return this.x=Math.random(),this.y=Math.random(),this.z=Math.random(),this.w=Math.random(),this}*[Symbol.iterator](){yield this.x,yield this.y,yield this.z,yield this.w}}class y$ extends _9{constructor(J=1,$=1,N={}){super();N=Object.assign({generateMipmaps:!1,internalFormat:null,minFilter:1006,depthBuffer:!0,stencilBuffer:!1,resolveDepthBuffer:!0,resolveStencilBuffer:!0,depthTexture:null,samples:0,count:1,depth:1,multiview:!1},N),this.isRenderTarget=!0,this.width=J,this.height=$,this.depth=N.depth,this.scissor=new D8(0,0,J,$),this.scissorTest=!1,this.viewport=new D8(0,0,J,$);let Q={width:J,height:$,depth:N.depth},Z=new S8(Q);this.textures=[];let W=N.count;for(let Y=0;Y<W;Y++)this.textures[Y]=Z.clone(),this.textures[Y].isRenderTargetTexture=!0,this.textures[Y].renderTarget=this;this._setTextureOptions(N),this.depthBuffer=N.depthBuffer,this.stencilBuffer=N.stencilBuffer,this.resolveDepthBuffer=N.resolveDepthBuffer,this.resolveStencilBuffer=N.resolveStencilBuffer,this._depthTexture=null,this.depthTexture=N.depthTexture,this.samples=N.samples,this.multiview=N.multiview}_setTextureOptions(J={}){let $={minFilter:1006,generateMipmaps:!1,flipY:!1,internalFormat:null};if(J.mapping!==void 0)$.mapping=J.mapping;if(J.wrapS!==void 0)$.wrapS=J.wrapS;if(J.wrapT!==void 0)$.wrapT=J.wrapT;if(J.wrapR!==void 0)$.wrapR=J.wrapR;if(J.magFilter!==void 0)$.magFilter=J.magFilter;if(J.minFilter!==void 0)$.minFilter=J.minFilter;if(J.format!==void 0)$.format=J.format;if(J.type!==void 0)$.type=J.type;if(J.anisotropy!==void 0)$.anisotropy=J.anisotropy;if(J.colorSpace!==void 0)$.colorSpace=J.colorSpace;if(J.flipY!==void 0)$.flipY=J.flipY;if(J.generateMipmaps!==void 0)$.generateMipmaps=J.generateMipmaps;if(J.internalFormat!==void 0)$.internalFormat=J.internalFormat;for(let N=0;N<this.textures.length;N++)this.textures[N].setValues($)}get texture(){return this.textures[0]}set texture(J){this.textures[0]=J}set depthTexture(J){if(this._depthTexture!==null)this._depthTexture.renderTarget=null;if(J!==null)J.renderTarget=this;this._depthTexture=J}get depthTexture(){return this._depthTexture}setSize(J,$,N=1){if(this.width!==J||this.height!==$||this.depth!==N){this.width=J,this.height=$,this.depth=N;for(let Q=0,Z=this.textures.length;Q<Z;Q++)this.textures[Q].image.width=J,this.textures[Q].image.height=$,this.textures[Q].image.depth=N,this.textures[Q].isArrayTexture=this.textures[Q].image.depth>1;this.dispose()}this.viewport.set(0,0,J,$),this.scissor.set(0,0,J,$)}clone(){return new this.constructor().copy(this)}copy(J){this.width=J.width,this.height=J.height,this.depth=J.depth,this.scissor.copy(J.scissor),this.scissorTest=J.scissorTest,this.viewport.copy(J.viewport),this.textures.length=0;for(let $=0,N=J.textures.length;$<N;$++){this.textures[$]=J.textures[$].clone(),this.textures[$].isRenderTargetTexture=!0,this.textures[$].renderTarget=this;let Q=Object.assign({},J.textures[$].image);this.textures[$].source=new oJ(Q)}if(this.depthBuffer=J.depthBuffer,this.stencilBuffer=J.stencilBuffer,this.resolveDepthBuffer=J.resolveDepthBuffer,this.resolveStencilBuffer=J.resolveStencilBuffer,J.depthTexture!==null)this.depthTexture=J.depthTexture.clone();return this.samples=J.samples,this}dispose(){this.dispatchEvent({type:"dispose"})}}class L9 extends y${constructor(J=1,$=1,N={}){super(J,$,N);this.isWebGLRenderTarget=!0}}class m6 extends S8{constructor(J=null,$=1,N=1,Q=1){super(null);this.isDataArrayTexture=!0,this.image={data:J,width:$,height:N,depth:Q},this.magFilter=1003,this.minFilter=1003,this.wrapR=1001,this.generateMipmaps=!1,this.flipY=!1,this.unpackAlignment=1,this.layerUpdates=new Set}addLayerUpdate(J){this.layerUpdates.add(J)}clearLayerUpdates(){this.layerUpdates.clear()}}class j$ extends S8{constructor(J=null,$=1,N=1,Q=1){super(null);this.isData3DTexture=!0,this.image={data:J,width:$,height:N,depth:Q},this.magFilter=1003,this.minFilter=1003,this.wrapR=1001,this.generateMipmaps=!1,this.flipY=!1,this.unpackAlignment=1}}class Y9{constructor(J=new h(1/0,1/0,1/0),$=new h(-1/0,-1/0,-1/0)){this.isBox3=!0,this.min=J,this.max=$}set(J,$){return this.min.copy(J),this.max.copy($),this}setFromArray(J){this.makeEmpty();for(let $=0,N=J.length;$<N;$+=3)this.expandByPoint(t8.fromArray(J,$));return this}setFromBufferAttribute(J){this.makeEmpty();for(let $=0,N=J.count;$<N;$++)this.expandByPoint(t8.fromBufferAttribute(J,$));return this}setFromPoints(J){this.makeEmpty();for(let $=0,N=J.length;$<N;$++)this.expandByPoint(J[$]);return this}setFromCenterAndSize(J,$){let N=t8.copy($).multiplyScalar(0.5);return this.min.copy(J).sub(N),this.max.copy(J).add(N),this}setFromObject(J,$=!1){return this.makeEmpty(),this.expandByObject(J,$)}clone(){return new this.constructor().copy(this)}copy(J){return this.min.copy(J.min),this.max.copy(J.max),this}makeEmpty(){return this.min.x=this.min.y=this.min.z=1/0,this.max.x=this.max.y=this.max.z=-1/0,this}isEmpty(){return this.max.x<this.min.x||this.max.y<this.min.y||this.max.z<this.min.z}getCenter(J){return this.isEmpty()?J.set(0,0,0):J.addVectors(this.min,this.max).multiplyScalar(0.5)}getSize(J){return this.isEmpty()?J.set(0,0,0):J.subVectors(this.max,this.min)}expandByPoint(J){return this.min.min(J),this.max.max(J),this}expandByVector(J){return this.min.sub(J),this.max.add(J),this}expandByScalar(J){return this.min.addScalar(-J),this.max.addScalar(J),this}expandByObject(J,$=!1){J.updateWorldMatrix(!1,!1);let N=J.geometry;if(N!==void 0){let Z=N.getAttribute("position");if($===!0&&Z!==void 0&&J.isInstancedMesh!==!0)for(let W=0,Y=Z.count;W<Y;W++){if(J.isMesh===!0)J.getVertexPosition(W,t8);else t8.fromBufferAttribute(Z,W);t8.applyMatrix4(J.matrixWorld),this.expandByPoint(t8)}else{if(J.boundingBox!==void 0){if(J.boundingBox===null)J.computeBoundingBox();K6.copy(J.boundingBox)}else{if(N.boundingBox===null)N.computeBoundingBox();K6.copy(N.boundingBox)}K6.applyMatrix4(J.matrixWorld),this.union(K6)}}let Q=J.children;for(let Z=0,W=Q.length;Z<W;Z++)this.expandByObject(Q[Z],$);return this}containsPoint(J){return J.x>=this.min.x&&J.x<=this.max.x&&J.y>=this.min.y&&J.y<=this.max.y&&J.z>=this.min.z&&J.z<=this.max.z}containsBox(J){return this.min.x<=J.min.x&&J.max.x<=this.max.x&&this.min.y<=J.min.y&&J.max.y<=this.max.y&&this.min.z<=J.min.z&&J.max.z<=this.max.z}getParameter(J,$){return $.set((J.x-this.min.x)/(this.max.x-this.min.x),(J.y-this.min.y)/(this.max.y-this.min.y),(J.z-this.min.z)/(this.max.z-this.min.z))}intersectsBox(J){return J.max.x>=this.min.x&&J.min.x<=this.max.x&&J.max.y>=this.min.y&&J.min.y<=this.max.y&&J.max.z>=this.min.z&&J.min.z<=this.max.z}intersectsSphere(J){return this.clampPoint(J.center,t8),t8.distanceToSquared(J.center)<=J.radius*J.radius}intersectsPlane(J){let $,N;if(J.normal.x>0)$=J.normal.x*this.min.x,N=J.normal.x*this.max.x;else $=J.normal.x*this.max.x,N=J.normal.x*this.min.x;if(J.normal.y>0)$+=J.normal.y*this.min.y,N+=J.normal.y*this.max.y;else $+=J.normal.y*this.max.y,N+=J.normal.y*this.min.y;if(J.normal.z>0)$+=J.normal.z*this.min.z,N+=J.normal.z*this.max.z;else $+=J.normal.z*this.max.z,N+=J.normal.z*this.min.z;return $<=-J.constant&&N>=-J.constant}intersectsTriangle(J){if(this.isEmpty())return!1;this.getCenter(yJ),Y6.subVectors(this.max,yJ),NJ.subVectors(J.a,yJ),QJ.subVectors(J.b,yJ),ZJ.subVectors(J.c,yJ),V9.subVectors(QJ,NJ),w9.subVectors(ZJ,QJ),v9.subVectors(NJ,ZJ);let $=[0,-V9.z,V9.y,0,-w9.z,w9.y,0,-v9.z,v9.y,V9.z,0,-V9.x,w9.z,0,-w9.x,v9.z,0,-v9.x,-V9.y,V9.x,0,-w9.y,w9.x,0,-v9.y,v9.x,0];if(!_7($,NJ,QJ,ZJ,Y6))return!1;if($=[1,0,0,0,1,0,0,0,1],!_7($,NJ,QJ,ZJ,Y6))return!1;return H6.crossVectors(V9,w9),$=[H6.x,H6.y,H6.z],_7($,NJ,QJ,ZJ,Y6)}clampPoint(J,$){return $.copy(J).clamp(this.min,this.max)}distanceToPoint(J){return this.clampPoint(J,t8).distanceTo(J)}getBoundingSphere(J){if(this.isEmpty())J.makeEmpty();else this.getCenter(J.center),J.radius=this.getSize(t8).length()*0.5;return J}intersect(J){if(this.min.max(J.min),this.max.min(J.max),this.isEmpty())this.makeEmpty();return this}union(J){return this.min.min(J.min),this.max.max(J.max),this}applyMatrix4(J){if(this.isEmpty())return this;return E9[0].set(this.min.x,this.min.y,this.min.z).applyMatrix4(J),E9[1].set(this.min.x,this.min.y,this.max.z).applyMatrix4(J),E9[2].set(this.min.x,this.max.y,this.min.z).applyMatrix4(J),E9[3].set(this.min.x,this.max.y,this.max.z).applyMatrix4(J),E9[4].set(this.max.x,this.min.y,this.min.z).applyMatrix4(J),E9[5].set(this.max.x,this.min.y,this.max.z).applyMatrix4(J),E9[6].set(this.max.x,this.max.y,this.min.z).applyMatrix4(J),E9[7].set(this.max.x,this.max.y,this.max.z).applyMatrix4(J),this.setFromPoints(E9),this}translate(J){return this.min.add(J),this.max.add(J),this}equals(J){return J.min.equals(this.min)&&J.max.equals(this.max)}toJSON(){return{min:this.min.toArray(),max:this.max.toArray()}}fromJSON(J){return this.min.fromArray(J.min),this.max.fromArray(J.max),this}}var E9=[new h,new h,new h,new h,new h,new h,new h,new h],t8=new h,K6=new Y9,NJ=new h,QJ=new h,ZJ=new h,V9=new h,w9=new h,v9=new h,yJ=new h,Y6=new h,H6=new h,h9=new h;function _7(J,$,N,Q,Z){for(let W=0,Y=J.length-3;W<=Y;W+=3){h9.fromArray(J,W);let K=Z.x*Math.abs(h9.x)+Z.y*Math.abs(h9.y)+Z.z*Math.abs(h9.z),H=$.dot(h9),X=N.dot(h9),G=Q.dot(h9);if(Math.max(-Math.max(H,X,G),Math.min(H,X,G))>K)return!1}return!0}var RW=new Y9,jJ=new h,L7=new h;class BJ{constructor(J=new h,$=-1){this.isSphere=!0,this.center=J,this.radius=$}set(J,$){return this.center.copy(J),this.radius=$,this}setFromPoints(J,$){let N=this.center;if($!==void 0)N.copy($);else RW.setFromPoints(J).getCenter(N);let Q=0;for(let Z=0,W=J.length;Z<W;Z++)Q=Math.max(Q,N.distanceToSquared(J[Z]));return this.radius=Math.sqrt(Q),this}copy(J){return this.center.copy(J.center),this.radius=J.radius,this}isEmpty(){return this.radius<0}makeEmpty(){return this.center.set(0,0,0),this.radius=-1,this}containsPoint(J){return J.distanceToSquared(this.center)<=this.radius*this.radius}distanceToPoint(J){return J.distanceTo(this.center)-this.radius}intersectsSphere(J){let $=this.radius+J.radius;return J.center.distanceToSquared(this.center)<=$*$}intersectsBox(J){return J.intersectsSphere(this)}intersectsPlane(J){return Math.abs(J.distanceToPoint(this.center))<=this.radius}clampPoint(J,$){let N=this.center.distanceToSquared(J);if($.copy(J),N>this.radius*this.radius)$.sub(this.center).normalize(),$.multiplyScalar(this.radius).add(this.center);return $}getBoundingBox(J){if(this.isEmpty())return J.makeEmpty(),J;return J.set(this.center,this.center),J.expandByScalar(this.radius),J}applyMatrix4(J){return this.center.applyMatrix4(J),this.radius=this.radius*J.getMaxScaleOnAxis(),this}translate(J){return this.center.add(J),this}expandByPoint(J){if(this.isEmpty())return this.center.copy(J),this.radius=0,this;jJ.subVectors(J,this.center);let $=jJ.lengthSq();if($>this.radius*this.radius){let N=Math.sqrt($),Q=(N-this.radius)*0.5;this.center.addScaledVector(jJ,Q/N),this.radius+=Q}return this}union(J){if(J.isEmpty())return this;if(this.isEmpty())return this.copy(J),this;if(this.center.equals(J.center)===!0)this.radius=Math.max(this.radius,J.radius);else L7.subVectors(J.center,this.center).setLength(J.radius),this.expandByPoint(jJ.copy(J.center).add(L7)),this.expandByPoint(jJ.copy(J.center).sub(L7));return this}equals(J){return J.center.equals(this.center)&&J.radius===this.radius}clone(){return new this.constructor().copy(this)}toJSON(){return{radius:this.radius,center:this.center.toArray()}}fromJSON(J){return this.radius=J.radius,this.center.fromArray(J.center),this}}var D9=new h,V7=new h,X6=new h,z9=new h,w7=new h,U6=new h,z7=new h;class m9{constructor(J=new h,$=new h(0,0,-1)){this.origin=J,this.direction=$}set(J,$){return this.origin.copy(J),this.direction.copy($),this}copy(J){return this.origin.copy(J.origin),this.direction.copy(J.direction),this}at(J,$){return $.copy(this.origin).addScaledVector(this.direction,J)}lookAt(J){return this.direction.copy(J).sub(this.origin).normalize(),this}recast(J){return this.origin.copy(this.at(J,D9)),this}closestPointToPoint(J,$){$.subVectors(J,this.origin);let N=$.dot(this.direction);if(N<0)return $.copy(this.origin);return $.copy(this.origin).addScaledVector(this.direction,N)}distanceToPoint(J){return Math.sqrt(this.distanceSqToPoint(J))}distanceSqToPoint(J){let $=D9.subVectors(J,this.origin).dot(this.direction);if($<0)return this.origin.distanceToSquared(J);return D9.copy(this.origin).addScaledVector(this.direction,$),D9.distanceToSquared(J)}distanceSqToSegment(J,$,N,Q){V7.copy(J).add($).multiplyScalar(0.5),X6.copy($).sub(J).normalize(),z9.copy(this.origin).sub(V7);let Z=J.distanceTo($)*0.5,W=-this.direction.dot(X6),Y=z9.dot(this.direction),K=-z9.dot(X6),H=z9.lengthSq(),X=Math.abs(1-W*W),G,E,q,k;if(X>0)if(G=W*K-Y,E=W*Y-K,k=Z*X,G>=0)if(E>=-k)if(E<=k){let M=1/X;G*=M,E*=M,q=G*(G+W*E+2*Y)+E*(W*G+E+2*K)+H}else E=Z,G=Math.max(0,-(W*E+Y)),q=-G*G+E*(E+2*K)+H;else E=-Z,G=Math.max(0,-(W*E+Y)),q=-G*G+E*(E+2*K)+H;else if(E<=-k)G=Math.max(0,-(-W*Z+Y)),E=G>0?-Z:Math.min(Math.max(-Z,-K),Z),q=-G*G+E*(E+2*K)+H;else if(E<=k)G=0,E=Math.min(Math.max(-Z,-K),Z),q=E*(E+2*K)+H;else G=Math.max(0,-(W*Z+Y)),E=G>0?Z:Math.min(Math.max(-Z,-K),Z),q=-G*G+E*(E+2*K)+H;else E=W>0?-Z:Z,G=Math.max(0,-(W*E+Y)),q=-G*G+E*(E+2*K)+H;if(N)N.copy(this.origin).addScaledVector(this.direction,G);if(Q)Q.copy(V7).addScaledVector(X6,E);return q}intersectSphere(J,$){D9.subVectors(J.center,this.origin);let N=D9.dot(this.direction),Q=D9.dot(D9)-N*N,Z=J.radius*J.radius;if(Q>Z)return null;let W=Math.sqrt(Z-Q),Y=N-W,K=N+W;if(K<0)return null;if(Y<0)return this.at(K,$);return this.at(Y,$)}intersectsSphere(J){if(J.radius<0)return!1;return this.distanceSqToPoint(J.center)<=J.radius*J.radius}distanceToPlane(J){let $=J.normal.dot(this.direction);if($===0){if(J.distanceToPoint(this.origin)===0)return 0;return null}let N=-(this.origin.dot(J.normal)+J.constant)/$;return N>=0?N:null}intersectPlane(J,$){let N=this.distanceToPlane(J);if(N===null)return null;return this.at(N,$)}intersectsPlane(J){let $=J.distanceToPoint(this.origin);if($===0)return!0;if(J.normal.dot(this.direction)*$<0)return!0;return!1}intersectBox(J,$){let N,Q,Z,W,Y,K,H=1/this.direction.x,X=1/this.direction.y,G=1/this.direction.z,E=this.origin;if(H>=0)N=(J.min.x-E.x)*H,Q=(J.max.x-E.x)*H;else N=(J.max.x-E.x)*H,Q=(J.min.x-E.x)*H;if(X>=0)Z=(J.min.y-E.y)*X,W=(J.max.y-E.y)*X;else Z=(J.max.y-E.y)*X,W=(J.min.y-E.y)*X;if(N>W||Z>Q)return null;if(Z>N||isNaN(N))N=Z;if(W<Q||isNaN(Q))Q=W;if(G>=0)Y=(J.min.z-E.z)*G,K=(J.max.z-E.z)*G;else Y=(J.max.z-E.z)*G,K=(J.min.z-E.z)*G;if(N>K||Y>Q)return null;if(Y>N||N!==N)N=Y;if(K<Q||Q!==Q)Q=K;if(Q<0)return null;return this.at(N>=0?N:Q,$)}intersectsBox(J){return this.intersectBox(J,D9)!==null}intersectTriangle(J,$,N,Q,Z){w7.subVectors($,J),U6.subVectors(N,J),z7.crossVectors(w7,U6);let W=this.direction.dot(z7),Y;if(W>0){if(Q)return null;Y=1}else if(W<0)Y=-1,W=-W;else return null;z9.subVectors(this.origin,J);let K=Y*this.direction.dot(U6.crossVectors(z9,U6));if(K<0)return null;let H=Y*this.direction.dot(w7.cross(z9));if(H<0)return null;if(K+H>W)return null;let X=-Y*z9.dot(z7);if(X<0)return null;return this.at(X/W,Z)}applyMatrix4(J){return this.origin.applyMatrix4(J),this.direction.transformDirection(J),this}equals(J){return J.origin.equals(this.origin)&&J.direction.equals(this.direction)}clone(){return new this.constructor().copy(this)}}class U8{constructor(J,$,N,Q,Z,W,Y,K,H,X,G,E,q,k,M,_){if(U8.prototype.isMatrix4=!0,this.elements=[1,0,0,0,0,1,0,0,0,0,1,0,0,0,0,1],J!==void 0)this.set(J,$,N,Q,Z,W,Y,K,H,X,G,E,q,k,M,_)}set(J,$,N,Q,Z,W,Y,K,H,X,G,E,q,k,M,_){let D=this.elements;return D[0]=J,D[4]=$,D[8]=N,D[12]=Q,D[1]=Z,D[5]=W,D[9]=Y,D[13]=K,D[2]=H,D[6]=X,D[10]=G,D[14]=E,D[3]=q,D[7]=k,D[11]=M,D[15]=_,this}identity(){return this.set(1,0,0,0,0,1,0,0,0,0,1,0,0,0,0,1),this}clone(){return new U8().fromArray(this.elements)}copy(J){let $=this.elements,N=J.elements;return $[0]=N[0],$[1]=N[1],$[2]=N[2],$[3]=N[3],$[4]=N[4],$[5]=N[5],$[6]=N[6],$[7]=N[7],$[8]=N[8],$[9]=N[9],$[10]=N[10],$[11]=N[11],$[12]=N[12],$[13]=N[13],$[14]=N[14],$[15]=N[15],this}copyPosition(J){let $=this.elements,N=J.elements;return $[12]=N[12],$[13]=N[13],$[14]=N[14],this}setFromMatrix3(J){let $=J.elements;return this.set($[0],$[3],$[6],0,$[1],$[4],$[7],0,$[2],$[5],$[8],0,0,0,0,1),this}extractBasis(J,$,N){return J.setFromMatrixColumn(this,0),$.setFromMatrixColumn(this,1),N.setFromMatrixColumn(this,2),this}makeBasis(J,$,N){return this.set(J.x,$.x,N.x,0,J.y,$.y,N.y,0,J.z,$.z,N.z,0,0,0,0,1),this}extractRotation(J){let $=this.elements,N=J.elements,Q=1/WJ.setFromMatrixColumn(J,0).length(),Z=1/WJ.setFromMatrixColumn(J,1).length(),W=1/WJ.setFromMatrixColumn(J,2).length();return $[0]=N[0]*Q,$[1]=N[1]*Q,$[2]=N[2]*Q,$[3]=0,$[4]=N[4]*Z,$[5]=N[5]*Z,$[6]=N[6]*Z,$[7]=0,$[8]=N[8]*W,$[9]=N[9]*W,$[10]=N[10]*W,$[11]=0,$[12]=0,$[13]=0,$[14]=0,$[15]=1,this}makeRotationFromEuler(J){let $=this.elements,N=J.x,Q=J.y,Z=J.z,W=Math.cos(N),Y=Math.sin(N),K=Math.cos(Q),H=Math.sin(Q),X=Math.cos(Z),G=Math.sin(Z);if(J.order==="XYZ"){let E=W*X,q=W*G,k=Y*X,M=Y*G;$[0]=K*X,$[4]=-K*G,$[8]=H,$[1]=q+k*H,$[5]=E-M*H,$[9]=-Y*K,$[2]=M-E*H,$[6]=k+q*H,$[10]=W*K}else if(J.order==="YXZ"){let E=K*X,q=K*G,k=H*X,M=H*G;$[0]=E+M*Y,$[4]=k*Y-q,$[8]=W*H,$[1]=W*G,$[5]=W*X,$[9]=-Y,$[2]=q*Y-k,$[6]=M+E*Y,$[10]=W*K}else if(J.order==="ZXY"){let E=K*X,q=K*G,k=H*X,M=H*G;$[0]=E-M*Y,$[4]=-W*G,$[8]=k+q*Y,$[1]=q+k*Y,$[5]=W*X,$[9]=M-E*Y,$[2]=-W*H,$[6]=Y,$[10]=W*K}else if(J.order==="ZYX"){let E=W*X,q=W*G,k=Y*X,M=Y*G;$[0]=K*X,$[4]=k*H-q,$[8]=E*H+M,$[1]=K*G,$[5]=M*H+E,$[9]=q*H-k,$[2]=-H,$[6]=Y*K,$[10]=W*K}else if(J.order==="YZX"){let E=W*K,q=W*H,k=Y*K,M=Y*H;$[0]=K*X,$[4]=M-E*G,$[8]=k*G+q,$[1]=G,$[5]=W*X,$[9]=-Y*X,$[2]=-H*X,$[6]=q*G+k,$[10]=E-M*G}else if(J.order==="XZY"){let E=W*K,q=W*H,k=Y*K,M=Y*H;$[0]=K*X,$[4]=-G,$[8]=H*X,$[1]=E*G+M,$[5]=W*X,$[9]=q*G-k,$[2]=k*G-q,$[6]=Y*X,$[10]=M*G+E}return $[3]=0,$[7]=0,$[11]=0,$[12]=0,$[13]=0,$[14]=0,$[15]=1,this}makeRotationFromQuaternion(J){return this.compose(FW,J,MW)}lookAt(J,$,N){let Q=this.elements;if(p8.subVectors(J,$),p8.lengthSq()===0)p8.z=1;if(p8.normalize(),B9.crossVectors(N,p8),B9.lengthSq()===0){if(Math.abs(N.z)===1)p8.x+=0.0001;else p8.z+=0.0001;p8.normalize(),B9.crossVectors(N,p8)}return B9.normalize(),G6.crossVectors(p8,B9),Q[0]=B9.x,Q[4]=G6.x,Q[8]=p8.x,Q[1]=B9.y,Q[5]=G6.y,Q[9]=p8.y,Q[2]=B9.z,Q[6]=G6.z,Q[10]=p8.z,this}multiply(J){return this.multiplyMatrices(this,J)}premultiply(J){return this.multiplyMatrices(J,this)}multiplyMatrices(J,$){let N=J.elements,Q=$.elements,Z=this.elements,W=N[0],Y=N[4],K=N[8],H=N[12],X=N[1],G=N[5],E=N[9],q=N[13],k=N[2],M=N[6],_=N[10],D=N[14],U=N[3],C=N[7],L=N[11],B=N[15],y=Q[0],I=Q[4],b=Q[8],g=Q[12],V=Q[1],F=Q[5],S=Q[9],s=Q[13],i=Q[2],m=Q[6],T=Q[10],p=Q[14],$0=Q[3],c=Q[7],X0=Q[11],q0=Q[15];return Z[0]=W*y+Y*V+K*i+H*$0,Z[4]=W*I+Y*F+K*m+H*c,Z[8]=W*b+Y*S+K*T+H*X0,Z[12]=W*g+Y*s+K*p+H*q0,Z[1]=X*y+G*V+E*i+q*$0,Z[5]=X*I+G*F+E*m+q*c,Z[9]=X*b+G*S+E*T+q*X0,Z[13]=X*g+G*s+E*p+q*q0,Z[2]=k*y+M*V+_*i+D*$0,Z[6]=k*I+M*F+_*m+D*c,Z[10]=k*b+M*S+_*T+D*X0,Z[14]=k*g+M*s+_*p+D*q0,Z[3]=U*y+C*V+L*i+B*$0,Z[7]=U*I+C*F+L*m+B*c,Z[11]=U*b+C*S+L*T+B*X0,Z[15]=U*g+C*s+L*p+B*q0,this}multiplyScalar(J){let $=this.elements;return $[0]*=J,$[4]*=J,$[8]*=J,$[12]*=J,$[1]*=J,$[5]*=J,$[9]*=J,$[13]*=J,$[2]*=J,$[6]*=J,$[10]*=J,$[14]*=J,$[3]*=J,$[7]*=J,$[11]*=J,$[15]*=J,this}determinant(){let J=this.elements,$=J[0],N=J[4],Q=J[8],Z=J[12],W=J[1],Y=J[5],K=J[9],H=J[13],X=J[2],G=J[6],E=J[10],q=J[14],k=J[3],M=J[7],_=J[11],D=J[15];return k*(+Z*K*G-Q*H*G-Z*Y*E+N*H*E+Q*Y*q-N*K*q)+M*(+$*K*q-$*H*E+Z*W*E-Q*W*q+Q*H*X-Z*K*X)+_*(+$*H*G-$*Y*q-Z*W*G+N*W*q+Z*Y*X-N*H*X)+D*(-Q*Y*X-$*K*G+$*Y*E+Q*W*G-N*W*E+N*K*X)}transpose(){let J=this.elements,$;return $=J[1],J[1]=J[4],J[4]=$,$=J[2],J[2]=J[8],J[8]=$,$=J[6],J[6]=J[9],J[9]=$,$=J[3],J[3]=J[12],J[12]=$,$=J[7],J[7]=J[13],J[13]=$,$=J[11],J[11]=J[14],J[14]=$,this}setPosition(J,$,N){let Q=this.elements;if(J.isVector3)Q[12]=J.x,Q[13]=J.y,Q[14]=J.z;else Q[12]=J,Q[13]=$,Q[14]=N;return this}invert(){let J=this.elements,$=J[0],N=J[1],Q=J[2],Z=J[3],W=J[4],Y=J[5],K=J[6],H=J[7],X=J[8],G=J[9],E=J[10],q=J[11],k=J[12],M=J[13],_=J[14],D=J[15],U=G*_*H-M*E*H+M*K*q-Y*_*q-G*K*D+Y*E*D,C=k*E*H-X*_*H-k*K*q+W*_*q+X*K*D-W*E*D,L=X*M*H-k*G*H+k*Y*q-W*M*q-X*Y*D+W*G*D,B=k*G*K-X*M*K-k*Y*E+W*M*E+X*Y*_-W*G*_,y=$*U+N*C+Q*L+Z*B;if(y===0)return this.set(0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0);let I=1/y;return J[0]=U*I,J[1]=(M*E*Z-G*_*Z-M*Q*q+N*_*q+G*Q*D-N*E*D)*I,J[2]=(Y*_*Z-M*K*Z+M*Q*H-N*_*H-Y*Q*D+N*K*D)*I,J[3]=(G*K*Z-Y*E*Z-G*Q*H+N*E*H+Y*Q*q-N*K*q)*I,J[4]=C*I,J[5]=(X*_*Z-k*E*Z+k*Q*q-$*_*q-X*Q*D+$*E*D)*I,J[6]=(k*K*Z-W*_*Z-k*Q*H+$*_*H+W*Q*D-$*K*D)*I,J[7]=(W*E*Z-X*K*Z+X*Q*H-$*E*H-W*Q*q+$*K*q)*I,J[8]=L*I,J[9]=(k*G*Z-X*M*Z-k*N*q+$*M*q+X*N*D-$*G*D)*I,J[10]=(W*M*Z-k*Y*Z+k*N*H-$*M*H-W*N*D+$*Y*D)*I,J[11]=(X*Y*Z-W*G*Z-X*N*H+$*G*H+W*N*q-$*Y*q)*I,J[12]=B*I,J[13]=(X*M*Q-k*G*Q+k*N*E-$*M*E-X*N*_+$*G*_)*I,J[14]=(k*Y*Q-W*M*Q-k*N*K+$*M*K+W*N*_-$*Y*_)*I,J[15]=(W*G*Q-X*Y*Q+X*N*K-$*G*K-W*N*E+$*Y*E)*I,this}scale(J){let $=this.elements,N=J.x,Q=J.y,Z=J.z;return $[0]*=N,$[4]*=Q,$[8]*=Z,$[1]*=N,$[5]*=Q,$[9]*=Z,$[2]*=N,$[6]*=Q,$[10]*=Z,$[3]*=N,$[7]*=Q,$[11]*=Z,this}getMaxScaleOnAxis(){let J=this.elements,$=J[0]*J[0]+J[1]*J[1]+J[2]*J[2],N=J[4]*J[4]+J[5]*J[5]+J[6]*J[6],Q=J[8]*J[8]+J[9]*J[9]+J[10]*J[10];return Math.sqrt(Math.max($,N,Q))}makeTranslation(J,$,N){if(J.isVector3)this.set(1,0,0,J.x,0,1,0,J.y,0,0,1,J.z,0,0,0,1);else this.set(1,0,0,J,0,1,0,$,0,0,1,N,0,0,0,1);return this}makeRotationX(J){let $=Math.cos(J),N=Math.sin(J);return this.set(1,0,0,0,0,$,-N,0,0,N,$,0,0,0,0,1),this}makeRotationY(J){let $=Math.cos(J),N=Math.sin(J);return this.set($,0,N,0,0,1,0,0,-N,0,$,0,0,0,0,1),this}makeRotationZ(J){let $=Math.cos(J),N=Math.sin(J);return this.set($,-N,0,0,N,$,0,0,0,0,1,0,0,0,0,1),this}makeRotationAxis(J,$){let N=Math.cos($),Q=Math.sin($),Z=1-N,W=J.x,Y=J.y,K=J.z,H=Z*W,X=Z*Y;return this.set(H*W+N,H*Y-Q*K,H*K+Q*Y,0,H*Y+Q*K,X*Y+N,X*K-Q*W,0,H*K-Q*Y,X*K+Q*W,Z*K*K+N,0,0,0,0,1),this}makeScale(J,$,N){return this.set(J,0,0,0,0,$,0,0,0,0,N,0,0,0,0,1),this}makeShear(J,$,N,Q,Z,W){return this.set(1,N,Z,0,J,1,W,0,$,Q,1,0,0,0,0,1),this}compose(J,$,N){let Q=this.elements,Z=$._x,W=$._y,Y=$._z,K=$._w,H=Z+Z,X=W+W,G=Y+Y,E=Z*H,q=Z*X,k=Z*G,M=W*X,_=W*G,D=Y*G,U=K*H,C=K*X,L=K*G,B=N.x,y=N.y,I=N.z;return Q[0]=(1-(M+D))*B,Q[1]=(q+L)*B,Q[2]=(k-C)*B,Q[3]=0,Q[4]=(q-L)*y,Q[5]=(1-(E+D))*y,Q[6]=(_+U)*y,Q[7]=0,Q[8]=(k+C)*I,Q[9]=(_-U)*I,Q[10]=(1-(E+M))*I,Q[11]=0,Q[12]=J.x,Q[13]=J.y,Q[14]=J.z,Q[15]=1,this}decompose(J,$,N){let Q=this.elements,Z=WJ.set(Q[0],Q[1],Q[2]).length(),W=WJ.set(Q[4],Q[5],Q[6]).length(),Y=WJ.set(Q[8],Q[9],Q[10]).length();if(this.determinant()<0)Z=-Z;J.x=Q[12],J.y=Q[13],J.z=Q[14],e8.copy(this);let H=1/Z,X=1/W,G=1/Y;return e8.elements[0]*=H,e8.elements[1]*=H,e8.elements[2]*=H,e8.elements[4]*=X,e8.elements[5]*=X,e8.elements[6]*=X,e8.elements[8]*=G,e8.elements[9]*=G,e8.elements[10]*=G,$.setFromRotationMatrix(e8),N.x=Z,N.y=W,N.z=Y,this}makePerspective(J,$,N,Q,Z,W,Y=2000,K=!1){let H=this.elements,X=2*Z/($-J),G=2*Z/(N-Q),E=($+J)/($-J),q=(N+Q)/(N-Q),k,M;if(K)k=Z/(W-Z),M=W*Z/(W-Z);else if(Y===2000)k=-(W+Z)/(W-Z),M=-2*W*Z/(W-Z);else if(Y===2001)k=-W/(W-Z),M=-W*Z/(W-Z);else throw Error("THREE.Matrix4.makePerspective(): Invalid coordinate system: "+Y);return H[0]=X,H[4]=0,H[8]=E,H[12]=0,H[1]=0,H[5]=G,H[9]=q,H[13]=0,H[2]=0,H[6]=0,H[10]=k,H[14]=M,H[3]=0,H[7]=0,H[11]=-1,H[15]=0,this}makeOrthographic(J,$,N,Q,Z,W,Y=2000,K=!1){let H=this.elements,X=2/($-J),G=2/(N-Q),E=-($+J)/($-J),q=-(N+Q)/(N-Q),k,M;if(K)k=1/(W-Z),M=W/(W-Z);else if(Y===2000)k=-2/(W-Z),M=-(W+Z)/(W-Z);else if(Y===2001)k=-1/(W-Z),M=-Z/(W-Z);else throw Error("THREE.Matrix4.makeOrthographic(): Invalid coordinate system: "+Y);return H[0]=X,H[4]=0,H[8]=0,H[12]=E,H[1]=0,H[5]=G,H[9]=0,H[13]=q,H[2]=0,H[6]=0,H[10]=k,H[14]=M,H[3]=0,H[7]=0,H[11]=0,H[15]=1,this}equals(J){let $=this.elements,N=J.elements;for(let Q=0;Q<16;Q++)if($[Q]!==N[Q])return!1;return!0}fromArray(J,$=0){for(let N=0;N<16;N++)this.elements[N]=J[N+$];return this}toArray(J=[],$=0){let N=this.elements;return J[$]=N[0],J[$+1]=N[1],J[$+2]=N[2],J[$+3]=N[3],J[$+4]=N[4],J[$+5]=N[5],J[$+6]=N[6],J[$+7]=N[7],J[$+8]=N[8],J[$+9]=N[9],J[$+10]=N[10],J[$+11]=N[11],J[$+12]=N[12],J[$+13]=N[13],J[$+14]=N[14],J[$+15]=N[15],J}}var WJ=new h,e8=new U8,FW=new h(0,0,0),MW=new h(1,1,1),B9=new h,G6=new h,p8=new h,PN=new U8,AN=new N9;class $9{constructor(J=0,$=0,N=0,Q=$9.DEFAULT_ORDER){this.isEuler=!0,this._x=J,this._y=$,this._z=N,this._order=Q}get x(){return this._x}set x(J){this._x=J,this._onChangeCallback()}get y(){return this._y}set y(J){this._y=J,this._onChangeCallback()}get z(){return this._z}set z(J){this._z=J,this._onChangeCallback()}get order(){return this._order}set order(J){this._order=J,this._onChangeCallback()}set(J,$,N,Q=this._order){return this._x=J,this._y=$,this._z=N,this._order=Q,this._onChangeCallback(),this}clone(){return new this.constructor(this._x,this._y,this._z,this._order)}copy(J){return this._x=J._x,this._y=J._y,this._z=J._z,this._order=J._order,this._onChangeCallback(),this}setFromRotationMatrix(J,$=this._order,N=!0){let Q=J.elements,Z=Q[0],W=Q[4],Y=Q[8],K=Q[1],H=Q[5],X=Q[9],G=Q[2],E=Q[6],q=Q[10];switch($){case"XYZ":if(this._y=Math.asin(n0(Y,-1,1)),Math.abs(Y)<0.9999999)this._x=Math.atan2(-X,q),this._z=Math.atan2(-W,Z);else this._x=Math.atan2(E,H),this._z=0;break;case"YXZ":if(this._x=Math.asin(-n0(X,-1,1)),Math.abs(X)<0.9999999)this._y=Math.atan2(Y,q),this._z=Math.atan2(K,H);else this._y=Math.atan2(-G,Z),this._z=0;break;case"ZXY":if(this._x=Math.asin(n0(E,-1,1)),Math.abs(E)<0.9999999)this._y=Math.atan2(-G,q),this._z=Math.atan2(-W,H);else this._y=0,this._z=Math.atan2(K,Z);break;case"ZYX":if(this._y=Math.asin(-n0(G,-1,1)),Math.abs(G)<0.9999999)this._x=Math.atan2(E,q),this._z=Math.atan2(K,Z);else this._x=0,this._z=Math.atan2(-W,H);break;case"YZX":if(this._z=Math.asin(n0(K,-1,1)),Math.abs(K)<0.9999999)this._x=Math.atan2(-X,H),this._y=Math.atan2(-G,Z);else this._x=0,this._y=Math.atan2(Y,q);break;case"XZY":if(this._z=Math.asin(-n0(W,-1,1)),Math.abs(W)<0.9999999)this._x=Math.atan2(E,H),this._y=Math.atan2(Y,Z);else this._x=Math.atan2(-X,q),this._y=0;break;default:console.warn("THREE.Euler: .setFromRotationMatrix() encountered an unknown order: "+$)}if(this._order=$,N===!0)this._onChangeCallback();return this}setFromQuaternion(J,$,N){return PN.makeRotationFromQuaternion(J),this.setFromRotationMatrix(PN,$,N)}setFromVector3(J,$=this._order){return this.set(J.x,J.y,J.z,$)}reorder(J){return AN.setFromEuler(this),this.setFromQuaternion(AN,J)}equals(J){return J._x===this._x&&J._y===this._y&&J._z===this._z&&J._order===this._order}fromArray(J){if(this._x=J[0],this._y=J[1],this._z=J[2],J[3]!==void 0)this._order=J[3];return this._onChangeCallback(),this}toArray(J=[],$=0){return J[$]=this._x,J[$+1]=this._y,J[$+2]=this._z,J[$+3]=this._order,J}_onChange(J){return this._onChangeCallback=J,this}_onChangeCallback(){}*[Symbol.iterator](){yield this._x,yield this._y,yield this._z,yield this._order}}$9.DEFAULT_ORDER="XYZ";class aJ{constructor(){this.mask=1}set(J){this.mask=(1<<J|0)>>>0}enable(J){this.mask|=1<<J|0}enableAll(){this.mask=-1}toggle(J){this.mask^=1<<J|0}disable(J){this.mask&=~(1<<J|0)}disableAll(){this.mask=0}test(J){return(this.mask&J.mask)!==0}isEnabled(J){return(this.mask&(1<<J|0))!==0}}var _W=0,TN=new h,KJ=new N9,q9=new U8,E6=new h,fJ=new h,LW=new h,VW=new N9,SN=new h(1,0,0),bN=new h(0,1,0),yN=new h(0,0,1),jN={type:"added"},wW={type:"removed"},YJ={type:"childadded",child:null},B7={type:"childremoved",child:null};class F8 extends _9{constructor(){super();this.isObject3D=!0,Object.defineProperty(this,"id",{value:_W++}),this.uuid=zJ(),this.name="",this.type="Object3D",this.parent=null,this.children=[],this.up=F8.DEFAULT_UP.clone();let J=new h,$=new $9,N=new N9,Q=new h(1,1,1);function Z(){N.setFromEuler($,!1)}function W(){$.setFromQuaternion(N,void 0,!1)}$._onChange(Z),N._onChange(W),Object.defineProperties(this,{position:{configurable:!0,enumerable:!0,value:J},rotation:{configurable:!0,enumerable:!0,value:$},quaternion:{configurable:!0,enumerable:!0,value:N},scale:{configurable:!0,enumerable:!0,value:Q},modelViewMatrix:{value:new U8},normalMatrix:{value:new l0}}),this.matrix=new U8,this.matrixWorld=new U8,this.matrixAutoUpdate=F8.DEFAULT_MATRIX_AUTO_UPDATE,this.matrixWorldAutoUpdate=F8.DEFAULT_MATRIX_WORLD_AUTO_UPDATE,this.matrixWorldNeedsUpdate=!1,this.layers=new aJ,this.visible=!0,this.castShadow=!1,this.receiveShadow=!1,this.frustumCulled=!0,this.renderOrder=0,this.animations=[],this.customDepthMaterial=void 0,this.customDistanceMaterial=void 0,this.userData={}}onBeforeShadow(){}onAfterShadow(){}onBeforeRender(){}onAfterRender(){}applyMatrix4(J){if(this.matrixAutoUpdate)this.updateMatrix();this.matrix.premultiply(J),this.matrix.decompose(this.position,this.quaternion,this.scale)}applyQuaternion(J){return this.quaternion.premultiply(J),this}setRotationFromAxisAngle(J,$){this.quaternion.setFromAxisAngle(J,$)}setRotationFromEuler(J){this.quaternion.setFromEuler(J,!0)}setRotationFromMatrix(J){this.quaternion.setFromRotationMatrix(J)}setRotationFromQuaternion(J){this.quaternion.copy(J)}rotateOnAxis(J,$){return KJ.setFromAxisAngle(J,$),this.quaternion.multiply(KJ),this}rotateOnWorldAxis(J,$){return KJ.setFromAxisAngle(J,$),this.quaternion.premultiply(KJ),this}rotateX(J){return this.rotateOnAxis(SN,J)}rotateY(J){return this.rotateOnAxis(bN,J)}rotateZ(J){return this.rotateOnAxis(yN,J)}translateOnAxis(J,$){return TN.copy(J).applyQuaternion(this.quaternion),this.position.add(TN.multiplyScalar($)),this}translateX(J){return this.translateOnAxis(SN,J)}translateY(J){return this.translateOnAxis(bN,J)}translateZ(J){return this.translateOnAxis(yN,J)}localToWorld(J){return this.updateWorldMatrix(!0,!1),J.applyMatrix4(this.matrixWorld)}worldToLocal(J){return this.updateWorldMatrix(!0,!1),J.applyMatrix4(q9.copy(this.matrixWorld).invert())}lookAt(J,$,N){if(J.isVector3)E6.copy(J);else E6.set(J,$,N);let Q=this.parent;if(this.updateWorldMatrix(!0,!1),fJ.setFromMatrixPosition(this.matrixWorld),this.isCamera||this.isLight)q9.lookAt(fJ,E6,this.up);else q9.lookAt(E6,fJ,this.up);if(this.quaternion.setFromRotationMatrix(q9),Q)q9.extractRotation(Q.matrixWorld),KJ.setFromRotationMatrix(q9),this.quaternion.premultiply(KJ.invert())}add(J){if(arguments.length>1){for(let $=0;$<arguments.length;$++)this.add(arguments[$]);return this}if(J===this)return console.error("THREE.Object3D.add: object can't be added as a child of itself.",J),this;if(J&&J.isObject3D)J.removeFromParent(),J.parent=this,this.children.push(J),J.dispatchEvent(jN),YJ.child=J,this.dispatchEvent(YJ),YJ.child=null;else console.error("THREE.Object3D.add: object not an instance of THREE.Object3D.",J);return this}remove(J){if(arguments.length>1){for(let N=0;N<arguments.length;N++)this.remove(arguments[N]);return this}let $=this.children.indexOf(J);if($!==-1)J.parent=null,this.children.splice($,1),J.dispatchEvent(wW),B7.child=J,this.dispatchEvent(B7),B7.child=null;return this}removeFromParent(){let J=this.parent;if(J!==null)J.remove(this);return this}clear(){return this.remove(...this.children)}attach(J){if(this.updateWorldMatrix(!0,!1),q9.copy(this.matrixWorld).invert(),J.parent!==null)J.parent.updateWorldMatrix(!0,!1),q9.multiply(J.parent.matrixWorld);return J.applyMatrix4(q9),J.removeFromParent(),J.parent=this,this.children.push(J),J.updateWorldMatrix(!1,!0),J.dispatchEvent(jN),YJ.child=J,this.dispatchEvent(YJ),YJ.child=null,this}getObjectById(J){return this.getObjectByProperty("id",J)}getObjectByName(J){return this.getObjectByProperty("name",J)}getObjectByProperty(J,$){if(this[J]===$)return this;for(let N=0,Q=this.children.length;N<Q;N++){let W=this.children[N].getObjectByProperty(J,$);if(W!==void 0)return W}return}getObjectsByProperty(J,$,N=[]){if(this[J]===$)N.push(this);let Q=this.children;for(let Z=0,W=Q.length;Z<W;Z++)Q[Z].getObjectsByProperty(J,$,N);return N}getWorldPosition(J){return this.updateWorldMatrix(!0,!1),J.setFromMatrixPosition(this.matrixWorld)}getWorldQuaternion(J){return this.updateWorldMatrix(!0,!1),this.matrixWorld.decompose(fJ,J,LW),J}getWorldScale(J){return this.updateWorldMatrix(!0,!1),this.matrixWorld.decompose(fJ,VW,J),J}getWorldDirection(J){this.updateWorldMatrix(!0,!1);let $=this.matrixWorld.elements;return J.set($[8],$[9],$[10]).normalize()}raycast(){}traverse(J){J(this);let $=this.children;for(let N=0,Q=$.length;N<Q;N++)$[N].traverse(J)}traverseVisible(J){if(this.visible===!1)return;J(this);let $=this.children;for(let N=0,Q=$.length;N<Q;N++)$[N].traverseVisible(J)}traverseAncestors(J){let $=this.parent;if($!==null)J($),$.traverseAncestors(J)}updateMatrix(){this.matrix.compose(this.position,this.quaternion,this.scale),this.matrixWorldNeedsUpdate=!0}updateMatrixWorld(J){if(this.matrixAutoUpdate)this.updateMatrix();if(this.matrixWorldNeedsUpdate||J){if(this.matrixWorldAutoUpdate===!0)if(this.parent===null)this.matrixWorld.copy(this.matrix);else this.matrixWorld.multiplyMatrices(this.parent.matrixWorld,this.matrix);this.matrixWorldNeedsUpdate=!1,J=!0}let $=this.children;for(let N=0,Q=$.length;N<Q;N++)$[N].updateMatrixWorld(J)}updateWorldMatrix(J,$){let N=this.parent;if(J===!0&&N!==null)N.updateWorldMatrix(!0,!1);if(this.matrixAutoUpdate)this.updateMatrix();if(this.matrixWorldAutoUpdate===!0)if(this.parent===null)this.matrixWorld.copy(this.matrix);else this.matrixWorld.multiplyMatrices(this.parent.matrixWorld,this.matrix);if($===!0){let Q=this.children;for(let Z=0,W=Q.length;Z<W;Z++)Q[Z].updateWorldMatrix(!1,!0)}}toJSON(J){let $=J===void 0||typeof J==="string",N={};if($)J={geometries:{},materials:{},textures:{},images:{},shapes:{},skeletons:{},animations:{},nodes:{}},N.metadata={version:4.7,type:"Object",generator:"Object3D.toJSON"};let Q={};if(Q.uuid=this.uuid,Q.type=this.type,this.name!=="")Q.name=this.name;if(this.castShadow===!0)Q.castShadow=!0;if(this.receiveShadow===!0)Q.receiveShadow=!0;if(this.visible===!1)Q.visible=!1;if(this.frustumCulled===!1)Q.frustumCulled=!1;if(this.renderOrder!==0)Q.renderOrder=this.renderOrder;if(Object.keys(this.userData).length>0)Q.userData=this.userData;if(Q.layers=this.layers.mask,Q.matrix=this.matrix.toArray(),Q.up=this.up.toArray(),this.matrixAutoUpdate===!1)Q.matrixAutoUpdate=!1;if(this.isInstancedMesh){if(Q.type="InstancedMesh",Q.count=this.count,Q.instanceMatrix=this.instanceMatrix.toJSON(),this.instanceColor!==null)Q.instanceColor=this.instanceColor.toJSON()}if(this.isBatchedMesh){if(Q.type="BatchedMesh",Q.perObjectFrustumCulled=this.perObjectFrustumCulled,Q.sortObjects=this.sortObjects,Q.drawRanges=this._drawRanges,Q.reservedRanges=this._reservedRanges,Q.geometryInfo=this._geometryInfo.map((Y)=>({...Y,boundingBox:Y.boundingBox?Y.boundingBox.toJSON():void 0,boundingSphere:Y.boundingSphere?Y.boundingSphere.toJSON():void 0})),Q.instanceInfo=this._instanceInfo.map((Y)=>({...Y})),Q.availableInstanceIds=this._availableInstanceIds.slice(),Q.availableGeometryIds=this._availableGeometryIds.slice(),Q.nextIndexStart=this._nextIndexStart,Q.nextVertexStart=this._nextVertexStart,Q.geometryCount=this._geometryCount,Q.maxInstanceCount=this._maxInstanceCount,Q.maxVertexCount=this._maxVertexCount,Q.maxIndexCount=this._maxIndexCount,Q.geometryInitialized=this._geometryInitialized,Q.matricesTexture=this._matricesTexture.toJSON(J),Q.indirectTexture=this._indirectTexture.toJSON(J),this._colorsTexture!==null)Q.colorsTexture=this._colorsTexture.toJSON(J);if(this.boundingSphere!==null)Q.boundingSphere=this.boundingSphere.toJSON();if(this.boundingBox!==null)Q.boundingBox=this.boundingBox.toJSON()}function Z(Y,K){if(Y[K.uuid]===void 0)Y[K.uuid]=K.toJSON(J);return K.uuid}if(this.isScene){if(this.background){if(this.background.isColor)Q.background=this.background.toJSON();else if(this.background.isTexture)Q.background=this.background.toJSON(J).uuid}if(this.environment&&this.environment.isTexture&&this.environment.isRenderTargetTexture!==!0)Q.environment=this.environment.toJSON(J).uuid}else if(this.isMesh||this.isLine||this.isPoints){Q.geometry=Z(J.geometries,this.geometry);let Y=this.geometry.parameters;if(Y!==void 0&&Y.shapes!==void 0){let K=Y.shapes;if(Array.isArray(K))for(let H=0,X=K.length;H<X;H++){let G=K[H];Z(J.shapes,G)}else Z(J.shapes,K)}}if(this.isSkinnedMesh){if(Q.bindMode=this.bindMode,Q.bindMatrix=this.bindMatrix.toArray(),this.skeleton!==void 0)Z(J.skeletons,this.skeleton),Q.skeleton=this.skeleton.uuid}if(this.material!==void 0)if(Array.isArray(this.material)){let Y=[];for(let K=0,H=this.material.length;K<H;K++)Y.push(Z(J.materials,this.material[K]));Q.material=Y}else Q.material=Z(J.materials,this.material);if(this.children.length>0){Q.children=[];for(let Y=0;Y<this.children.length;Y++)Q.children.push(this.children[Y].toJSON(J).object)}if(this.animations.length>0){Q.animations=[];for(let Y=0;Y<this.animations.length;Y++){let K=this.animations[Y];Q.animations.push(Z(J.animations,K))}}if($){let Y=W(J.geometries),K=W(J.materials),H=W(J.textures),X=W(J.images),G=W(J.shapes),E=W(J.skeletons),q=W(J.animations),k=W(J.nodes);if(Y.length>0)N.geometries=Y;if(K.length>0)N.materials=K;if(H.length>0)N.textures=H;if(X.length>0)N.images=X;if(G.length>0)N.shapes=G;if(E.length>0)N.skeletons=E;if(q.length>0)N.animations=q;if(k.length>0)N.nodes=k}return N.object=Q,N;function W(Y){let K=[];for(let H in Y){let X=Y[H];delete X.metadata,K.push(X)}return K}}clone(J){return new this.constructor().copy(this,J)}copy(J,$=!0){if(this.name=J.name,this.up.copy(J.up),this.position.copy(J.position),this.rotation.order=J.rotation.order,this.quaternion.copy(J.quaternion),this.scale.copy(J.scale),this.matrix.copy(J.matrix),this.matrixWorld.copy(J.matrixWorld),this.matrixAutoUpdate=J.matrixAutoUpdate,this.matrixWorldAutoUpdate=J.matrixWorldAutoUpdate,this.matrixWorldNeedsUpdate=J.matrixWorldNeedsUpdate,this.layers.mask=J.layers.mask,this.visible=J.visible,this.castShadow=J.castShadow,this.receiveShadow=J.receiveShadow,this.frustumCulled=J.frustumCulled,this.renderOrder=J.renderOrder,this.animations=J.animations.slice(),this.userData=JSON.parse(JSON.stringify(J.userData)),$===!0)for(let N=0;N<J.children.length;N++){let Q=J.children[N];this.add(Q.clone())}return this}}F8.DEFAULT_UP=new h(0,1,0);F8.DEFAULT_MATRIX_AUTO_UPDATE=!0;F8.DEFAULT_MATRIX_WORLD_AUTO_UPDATE=!0;var J9=new h,k9=new h,C7=new h,O9=new h,HJ=new h,XJ=new h,fN=new h,I7=new h,P7=new h,A7=new h,T7=new D8,S7=new D8,b7=new D8;class i8{constructor(J=new h,$=new h,N=new h){this.a=J,this.b=$,this.c=N}static getNormal(J,$,N,Q){Q.subVectors(N,$),J9.subVectors(J,$),Q.cross(J9);let Z=Q.lengthSq();if(Z>0)return Q.multiplyScalar(1/Math.sqrt(Z));return Q.set(0,0,0)}static getBarycoord(J,$,N,Q,Z){J9.subVectors(Q,$),k9.subVectors(N,$),C7.subVectors(J,$);let W=J9.dot(J9),Y=J9.dot(k9),K=J9.dot(C7),H=k9.dot(k9),X=k9.dot(C7),G=W*H-Y*Y;if(G===0)return Z.set(0,0,0),null;let E=1/G,q=(H*K-Y*X)*E,k=(W*X-Y*K)*E;return Z.set(1-q-k,k,q)}static containsPoint(J,$,N,Q){if(this.getBarycoord(J,$,N,Q,O9)===null)return!1;return O9.x>=0&&O9.y>=0&&O9.x+O9.y<=1}static getInterpolation(J,$,N,Q,Z,W,Y,K){if(this.getBarycoord(J,$,N,Q,O9)===null){if(K.x=0,K.y=0,"z"in K)K.z=0;if("w"in K)K.w=0;return null}return K.setScalar(0),K.addScaledVector(Z,O9.x),K.addScaledVector(W,O9.y),K.addScaledVector(Y,O9.z),K}static getInterpolatedAttribute(J,$,N,Q,Z,W){return T7.setScalar(0),S7.setScalar(0),b7.setScalar(0),T7.fromBufferAttribute(J,$),S7.fromBufferAttribute(J,N),b7.fromBufferAttribute(J,Q),W.setScalar(0),W.addScaledVector(T7,Z.x),W.addScaledVector(S7,Z.y),W.addScaledVector(b7,Z.z),W}static isFrontFacing(J,$,N,Q){return J9.subVectors(N,$),k9.subVectors(J,$),J9.cross(k9).dot(Q)<0?!0:!1}set(J,$,N){return this.a.copy(J),this.b.copy($),this.c.copy(N),this}setFromPointsAndIndices(J,$,N,Q){return this.a.copy(J[$]),this.b.copy(J[N]),this.c.copy(J[Q]),this}setFromAttributeAndIndices(J,$,N,Q){return this.a.fromBufferAttribute(J,$),this.b.fromBufferAttribute(J,N),this.c.fromBufferAttribute(J,Q),this}clone(){return new this.constructor().copy(this)}copy(J){return this.a.copy(J.a),this.b.copy(J.b),this.c.copy(J.c),this}getArea(){return J9.subVectors(this.c,this.b),k9.subVectors(this.a,this.b),J9.cross(k9).length()*0.5}getMidpoint(J){return J.addVectors(this.a,this.b).add(this.c).multiplyScalar(0.3333333333333333)}getNormal(J){return i8.getNormal(this.a,this.b,this.c,J)}getPlane(J){return J.setFromCoplanarPoints(this.a,this.b,this.c)}getBarycoord(J,$){return i8.getBarycoord(J,this.a,this.b,this.c,$)}getInterpolation(J,$,N,Q,Z){return i8.getInterpolation(J,this.a,this.b,this.c,$,N,Q,Z)}containsPoint(J){return i8.containsPoint(J,this.a,this.b,this.c)}isFrontFacing(J){return i8.isFrontFacing(this.a,this.b,this.c,J)}intersectsBox(J){return J.intersectsTriangle(this)}closestPointToPoint(J,$){let N=this.a,Q=this.b,Z=this.c,W,Y;HJ.subVectors(Q,N),XJ.subVectors(Z,N),I7.subVectors(J,N);let K=HJ.dot(I7),H=XJ.dot(I7);if(K<=0&&H<=0)return $.copy(N);P7.subVectors(J,Q);let X=HJ.dot(P7),G=XJ.dot(P7);if(X>=0&&G<=X)return $.copy(Q);let E=K*G-X*H;if(E<=0&&K>=0&&X<=0)return W=K/(K-X),$.copy(N).addScaledVector(HJ,W);A7.subVectors(J,Z);let q=HJ.dot(A7),k=XJ.dot(A7);if(k>=0&&q<=k)return $.copy(Z);let M=q*H-K*k;if(M<=0&&H>=0&&k<=0)return Y=H/(H-k),$.copy(N).addScaledVector(XJ,Y);let _=X*k-q*G;if(_<=0&&G-X>=0&&q-k>=0)return fN.subVectors(Z,Q),Y=(G-X)/(G-X+(q-k)),$.copy(Q).addScaledVector(fN,Y);let D=1/(_+M+E);return W=M*D,Y=E*D,$.copy(N).addScaledVector(HJ,W).addScaledVector(XJ,Y)}equals(J){return J.a.equals(this.a)&&J.b.equals(this.b)&&J.c.equals(this.c)}}var tQ={aliceblue:15792383,antiquewhite:16444375,aqua:65535,aquamarine:8388564,azure:15794175,beige:16119260,bisque:16770244,black:0,blanchedalmond:16772045,blue:255,blueviolet:9055202,brown:10824234,burlywood:14596231,cadetblue:6266528,chartreuse:8388352,chocolate:13789470,coral:16744272,cornflowerblue:6591981,cornsilk:16775388,crimson:14423100,cyan:65535,darkblue:139,darkcyan:35723,darkgoldenrod:12092939,darkgray:11119017,darkgreen:25600,darkgrey:11119017,darkkhaki:12433259,darkmagenta:9109643,darkolivegreen:5597999,darkorange:16747520,darkorchid:10040012,darkred:9109504,darksalmon:15308410,darkseagreen:9419919,darkslateblue:4734347,darkslategray:3100495,darkslategrey:3100495,darkturquoise:52945,darkviolet:9699539,deeppink:16716947,deepskyblue:49151,dimgray:6908265,dimgrey:6908265,dodgerblue:2003199,firebrick:11674146,floralwhite:16775920,forestgreen:2263842,fuchsia:16711935,gainsboro:14474460,ghostwhite:16316671,gold:16766720,goldenrod:14329120,gray:8421504,green:32768,greenyellow:11403055,grey:8421504,honeydew:15794160,hotpink:16738740,indianred:13458524,indigo:4915330,ivory:16777200,khaki:15787660,lavender:15132410,lavenderblush:16773365,lawngreen:8190976,lemonchiffon:16775885,lightblue:11393254,lightcoral:15761536,lightcyan:14745599,lightgoldenrodyellow:16448210,lightgray:13882323,lightgreen:9498256,lightgrey:13882323,lightpink:16758465,lightsalmon:16752762,lightseagreen:2142890,lightskyblue:8900346,lightslategray:7833753,lightslategrey:7833753,lightsteelblue:11584734,lightyellow:16777184,lime:65280,limegreen:3329330,linen:16445670,magenta:16711935,maroon:8388608,mediumaquamarine:6737322,mediumblue:205,mediumorchid:12211667,mediumpurple:9662683,mediumseagreen:3978097,mediumslateblue:8087790,mediumspringgreen:64154,mediumturquoise:4772300,mediumvioletred:13047173,midnightblue:1644912,mintcream:16121850,mistyrose:16770273,moccasin:16770229,navajowhite:16768685,navy:128,oldlace:16643558,olive:8421376,olivedrab:7048739,orange:16753920,orangered:16729344,orchid:14315734,palegoldenrod:15657130,palegreen:10025880,paleturquoise:11529966,palevioletred:14381203,papayawhip:16773077,peachpuff:16767673,peru:13468991,pink:16761035,plum:14524637,powderblue:11591910,purple:8388736,rebeccapurple:6697881,red:16711680,rosybrown:12357519,royalblue:4286945,saddlebrown:9127187,salmon:16416882,sandybrown:16032864,seagreen:3050327,seashell:16774638,sienna:10506797,silver:12632256,skyblue:8900331,slateblue:6970061,slategray:7372944,slategrey:7372944,snow:16775930,springgreen:65407,steelblue:4620980,tan:13808780,teal:32896,thistle:14204888,tomato:16737095,turquoise:4251856,violet:15631086,wheat:16113331,white:16777215,whitesmoke:16119285,yellow:16776960,yellowgreen:10145074},C9={h:0,s:0,l:0},D6={h:0,s:0,l:0};function y7(J,$,N){if(N<0)N+=1;if(N>1)N-=1;if(N<0.16666666666666666)return J+($-J)*6*N;if(N<0.5)return $;if(N<0.6666666666666666)return J+($-J)*6*(0.6666666666666666-N);return J}class i0{constructor(J,$,N){return this.isColor=!0,this.r=1,this.g=1,this.b=1,this.set(J,$,N)}set(J,$,N){if($===void 0&&N===void 0){let Q=J;if(Q&&Q.isColor)this.copy(Q);else if(typeof Q==="number")this.setHex(Q);else if(typeof Q==="string")this.setStyle(Q)}else this.setRGB(J,$,N);return this}setScalar(J){return this.r=J,this.g=J,this.b=J,this}setHex(J,$="srgb"){return J=Math.floor(J),this.r=(J>>16&255)/255,this.g=(J>>8&255)/255,this.b=(J&255)/255,o0.colorSpaceToWorking(this,$),this}setRGB(J,$,N,Q=o0.workingColorSpace){return this.r=J,this.g=$,this.b=N,o0.colorSpaceToWorking(this,Q),this}setHSL(J,$,N,Q=o0.workingColorSpace){if(J=A$(J,1),$=n0($,0,1),N=n0(N,0,1),$===0)this.r=this.g=this.b=N;else{let Z=N<=0.5?N*(1+$):N+$-N*$,W=2*N-Z;this.r=y7(W,Z,J+0.3333333333333333),this.g=y7(W,Z,J),this.b=y7(W,Z,J-0.3333333333333333)}return o0.colorSpaceToWorking(this,Q),this}setStyle(J,$="srgb"){function N(Z){if(Z===void 0)return;if(parseFloat(Z)<1)console.warn("THREE.Color: Alpha component of "+J+" will be ignored.")}let Q;if(Q=/^(\w+)\(([^\)]*)\)/.exec(J)){let Z,W=Q[1],Y=Q[2];switch(W){case"rgb":case"rgba":if(Z=/^\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(Y))return N(Z[4]),this.setRGB(Math.min(255,parseInt(Z[1],10))/255,Math.min(255,parseInt(Z[2],10))/255,Math.min(255,parseInt(Z[3],10))/255,$);if(Z=/^\s*(\d+)\%\s*,\s*(\d+)\%\s*,\s*(\d+)\%\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(Y))return N(Z[4]),this.setRGB(Math.min(100,parseInt(Z[1],10))/100,Math.min(100,parseInt(Z[2],10))/100,Math.min(100,parseInt(Z[3],10))/100,$);break;case"hsl":case"hsla":if(Z=/^\s*(\d*\.?\d+)\s*,\s*(\d*\.?\d+)\%\s*,\s*(\d*\.?\d+)\%\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(Y))return N(Z[4]),this.setHSL(parseFloat(Z[1])/360,parseFloat(Z[2])/100,parseFloat(Z[3])/100,$);break;default:console.warn("THREE.Color: Unknown color model "+J)}}else if(Q=/^\#([A-Fa-f\d]+)$/.exec(J)){let Z=Q[1],W=Z.length;if(W===3)return this.setRGB(parseInt(Z.charAt(0),16)/15,parseInt(Z.charAt(1),16)/15,parseInt(Z.charAt(2),16)/15,$);else if(W===6)return this.setHex(parseInt(Z,16),$);else console.warn("THREE.Color: Invalid hex color "+J)}else if(J&&J.length>0)return this.setColorName(J,$);return this}setColorName(J,$="srgb"){let N=tQ[J.toLowerCase()];if(N!==void 0)this.setHex(N,$);else console.warn("THREE.Color: Unknown color "+J);return this}clone(){return new this.constructor(this.r,this.g,this.b)}copy(J){return this.r=J.r,this.g=J.g,this.b=J.b,this}copySRGBToLinear(J){return this.r=F9(J.r),this.g=F9(J.g),this.b=F9(J.b),this}copyLinearToSRGB(J){return this.r=qJ(J.r),this.g=qJ(J.g),this.b=qJ(J.b),this}convertSRGBToLinear(){return this.copySRGBToLinear(this),this}convertLinearToSRGB(){return this.copyLinearToSRGB(this),this}getHex(J="srgb"){return o0.workingToColorSpace(z8.copy(this),J),Math.round(n0(z8.r*255,0,255))*65536+Math.round(n0(z8.g*255,0,255))*256+Math.round(n0(z8.b*255,0,255))}getHexString(J="srgb"){return("000000"+this.getHex(J).toString(16)).slice(-6)}getHSL(J,$=o0.workingColorSpace){o0.workingToColorSpace(z8.copy(this),$);let{r:N,g:Q,b:Z}=z8,W=Math.max(N,Q,Z),Y=Math.min(N,Q,Z),K,H,X=(Y+W)/2;if(Y===W)K=0,H=0;else{let G=W-Y;switch(H=X<=0.5?G/(W+Y):G/(2-W-Y),W){case N:K=(Q-Z)/G+(Q<Z?6:0);break;case Q:K=(Z-N)/G+2;break;case Z:K=(N-Q)/G+4;break}K/=6}return J.h=K,J.s=H,J.l=X,J}getRGB(J,$=o0.workingColorSpace){return o0.workingToColorSpace(z8.copy(this),$),J.r=z8.r,J.g=z8.g,J.b=z8.b,J}getStyle(J="srgb"){o0.workingToColorSpace(z8.copy(this),J);let{r:$,g:N,b:Q}=z8;if(J!=="srgb")return`color(${J} ${$.toFixed(3)} ${N.toFixed(3)} ${Q.toFixed(3)})`;return`rgb(${Math.round($*255)},${Math.round(N*255)},${Math.round(Q*255)})`}offsetHSL(J,$,N){return this.getHSL(C9),this.setHSL(C9.h+J,C9.s+$,C9.l+N)}add(J){return this.r+=J.r,this.g+=J.g,this.b+=J.b,this}addColors(J,$){return this.r=J.r+$.r,this.g=J.g+$.g,this.b=J.b+$.b,this}addScalar(J){return this.r+=J,this.g+=J,this.b+=J,this}sub(J){return this.r=Math.max(0,this.r-J.r),this.g=Math.max(0,this.g-J.g),this.b=Math.max(0,this.b-J.b),this}multiply(J){return this.r*=J.r,this.g*=J.g,this.b*=J.b,this}multiplyScalar(J){return this.r*=J,this.g*=J,this.b*=J,this}lerp(J,$){return this.r+=(J.r-this.r)*$,this.g+=(J.g-this.g)*$,this.b+=(J.b-this.b)*$,this}lerpColors(J,$,N){return this.r=J.r+($.r-J.r)*N,this.g=J.g+($.g-J.g)*N,this.b=J.b+($.b-J.b)*N,this}lerpHSL(J,$){this.getHSL(C9),J.getHSL(D6);let N=gJ(C9.h,D6.h,$),Q=gJ(C9.s,D6.s,$),Z=gJ(C9.l,D6.l,$);return this.setHSL(N,Q,Z),this}setFromVector3(J){return this.r=J.x,this.g=J.y,this.b=J.z,this}applyMatrix3(J){let $=this.r,N=this.g,Q=this.b,Z=J.elements;return this.r=Z[0]*$+Z[3]*N+Z[6]*Q,this.g=Z[1]*$+Z[4]*N+Z[7]*Q,this.b=Z[2]*$+Z[5]*N+Z[8]*Q,this}equals(J){return J.r===this.r&&J.g===this.g&&J.b===this.b}fromArray(J,$=0){return this.r=J[$],this.g=J[$+1],this.b=J[$+2],this}toArray(J=[],$=0){return J[$]=this.r,J[$+1]=this.g,J[$+2]=this.b,J}fromBufferAttribute(J,$){return this.r=J.getX($),this.g=J.getY($),this.b=J.getZ($),this}toJSON(){return this.getHex()}*[Symbol.iterator](){yield this.r,yield this.g,yield this.b}}var z8=new i0;i0.NAMES=tQ;var zW=0;class y9 extends _9{constructor(){super();this.isMaterial=!0,Object.defineProperty(this,"id",{value:zW++}),this.uuid=zJ(),this.name="",this.type="Material",this.blending=1,this.side=0,this.vertexColors=!1,this.opacity=1,this.transparent=!1,this.alphaHash=!1,this.blendSrc=204,this.blendDst=205,this.blendEquation=100,this.blendSrcAlpha=null,this.blendDstAlpha=null,this.blendEquationAlpha=null,this.blendColor=new i0(0,0,0),this.blendAlpha=0,this.depthFunc=3,this.depthTest=!0,this.depthWrite=!0,this.stencilWriteMask=255,this.stencilFunc=519,this.stencilRef=0,this.stencilFuncMask=255,this.stencilFail=7680,this.stencilZFail=7680,this.stencilZPass=7680,this.stencilWrite=!1,this.clippingPlanes=null,this.clipIntersection=!1,this.clipShadows=!1,this.shadowSide=null,this.colorWrite=!0,this.precision=null,this.polygonOffset=!1,this.polygonOffsetFactor=0,this.polygonOffsetUnits=0,this.dithering=!1,this.alphaToCoverage=!1,this.premultipliedAlpha=!1,this.forceSinglePass=!1,this.allowOverride=!0,this.visible=!0,this.toneMapped=!0,this.userData={},this.version=0,this._alphaTest=0}get alphaTest(){return this._alphaTest}set alphaTest(J){if(this._alphaTest>0!==J>0)this.version++;this._alphaTest=J}onBeforeRender(){}onBeforeCompile(){}customProgramCacheKey(){return this.onBeforeCompile.toString()}setValues(J){if(J===void 0)return;for(let $ in J){let N=J[$];if(N===void 0){console.warn(`THREE.Material: parameter '${$}' has value of undefined.`);continue}let Q=this[$];if(Q===void 0){console.warn(`THREE.Material: '${$}' is not a property of THREE.${this.type}.`);continue}if(Q&&Q.isColor)Q.set(N);else if(Q&&Q.isVector3&&(N&&N.isVector3))Q.copy(N);else this[$]=N}}toJSON(J){let $=J===void 0||typeof J==="string";if($)J={textures:{},images:{}};let N={metadata:{version:4.7,type:"Material",generator:"Material.toJSON"}};if(N.uuid=this.uuid,N.type=this.type,this.name!=="")N.name=this.name;if(this.color&&this.color.isColor)N.color=this.color.getHex();if(this.roughness!==void 0)N.roughness=this.roughness;if(this.metalness!==void 0)N.metalness=this.metalness;if(this.sheen!==void 0)N.sheen=this.sheen;if(this.sheenColor&&this.sheenColor.isColor)N.sheenColor=this.sheenColor.getHex();if(this.sheenRoughness!==void 0)N.sheenRoughness=this.sheenRoughness;if(this.emissive&&this.emissive.isColor)N.emissive=this.emissive.getHex();if(this.emissiveIntensity!==void 0&&this.emissiveIntensity!==1)N.emissiveIntensity=this.emissiveIntensity;if(this.specular&&this.specular.isColor)N.specular=this.specular.getHex();if(this.specularIntensity!==void 0)N.specularIntensity=this.specularIntensity;if(this.specularColor&&this.specularColor.isColor)N.specularColor=this.specularColor.getHex();if(this.shininess!==void 0)N.shininess=this.shininess;if(this.clearcoat!==void 0)N.clearcoat=this.clearcoat;if(this.clearcoatRoughness!==void 0)N.clearcoatRoughness=this.clearcoatRoughness;if(this.clearcoatMap&&this.clearcoatMap.isTexture)N.clearcoatMap=this.clearcoatMap.toJSON(J).uuid;if(this.clearcoatRoughnessMap&&this.clearcoatRoughnessMap.isTexture)N.clearcoatRoughnessMap=this.clearcoatRoughnessMap.toJSON(J).uuid;if(this.clearcoatNormalMap&&this.clearcoatNormalMap.isTexture)N.clearcoatNormalMap=this.clearcoatNormalMap.toJSON(J).uuid,N.clearcoatNormalScale=this.clearcoatNormalScale.toArray();if(this.sheenColorMap&&this.sheenColorMap.isTexture)N.sheenColorMap=this.sheenColorMap.toJSON(J).uuid;if(this.sheenRoughnessMap&&this.sheenRoughnessMap.isTexture)N.sheenRoughnessMap=this.sheenRoughnessMap.toJSON(J).uuid;if(this.dispersion!==void 0)N.dispersion=this.dispersion;if(this.iridescence!==void 0)N.iridescence=this.iridescence;if(this.iridescenceIOR!==void 0)N.iridescenceIOR=this.iridescenceIOR;if(this.iridescenceThicknessRange!==void 0)N.iridescenceThicknessRange=this.iridescenceThicknessRange;if(this.iridescenceMap&&this.iridescenceMap.isTexture)N.iridescenceMap=this.iridescenceMap.toJSON(J).uuid;if(this.iridescenceThicknessMap&&this.iridescenceThicknessMap.isTexture)N.iridescenceThicknessMap=this.iridescenceThicknessMap.toJSON(J).uuid;if(this.anisotropy!==void 0)N.anisotropy=this.anisotropy;if(this.anisotropyRotation!==void 0)N.anisotropyRotation=this.anisotropyRotation;if(this.anisotropyMap&&this.anisotropyMap.isTexture)N.anisotropyMap=this.anisotropyMap.toJSON(J).uuid;if(this.map&&this.map.isTexture)N.map=this.map.toJSON(J).uuid;if(this.matcap&&this.matcap.isTexture)N.matcap=this.matcap.toJSON(J).uuid;if(this.alphaMap&&this.alphaMap.isTexture)N.alphaMap=this.alphaMap.toJSON(J).uuid;if(this.lightMap&&this.lightMap.isTexture)N.lightMap=this.lightMap.toJSON(J).uuid,N.lightMapIntensity=this.lightMapIntensity;if(this.aoMap&&this.aoMap.isTexture)N.aoMap=this.aoMap.toJSON(J).uuid,N.aoMapIntensity=this.aoMapIntensity;if(this.bumpMap&&this.bumpMap.isTexture)N.bumpMap=this.bumpMap.toJSON(J).uuid,N.bumpScale=this.bumpScale;if(this.normalMap&&this.normalMap.isTexture)N.normalMap=this.normalMap.toJSON(J).uuid,N.normalMapType=this.normalMapType,N.normalScale=this.normalScale.toArray();if(this.displacementMap&&this.displacementMap.isTexture)N.displacementMap=this.displacementMap.toJSON(J).uuid,N.displacementScale=this.displacementScale,N.displacementBias=this.displacementBias;if(this.roughnessMap&&this.roughnessMap.isTexture)N.roughnessMap=this.roughnessMap.toJSON(J).uuid;if(this.metalnessMap&&this.metalnessMap.isTexture)N.metalnessMap=this.metalnessMap.toJSON(J).uuid;if(this.emissiveMap&&this.emissiveMap.isTexture)N.emissiveMap=this.emissiveMap.toJSON(J).uuid;if(this.specularMap&&this.specularMap.isTexture)N.specularMap=this.specularMap.toJSON(J).uuid;if(this.specularIntensityMap&&this.specularIntensityMap.isTexture)N.specularIntensityMap=this.specularIntensityMap.toJSON(J).uuid;if(this.specularColorMap&&this.specularColorMap.isTexture)N.specularColorMap=this.specularColorMap.toJSON(J).uuid;if(this.envMap&&this.envMap.isTexture){if(N.envMap=this.envMap.toJSON(J).uuid,this.combine!==void 0)N.combine=this.combine}if(this.envMapRotation!==void 0)N.envMapRotation=this.envMapRotation.toArray();if(this.envMapIntensity!==void 0)N.envMapIntensity=this.envMapIntensity;if(this.reflectivity!==void 0)N.reflectivity=this.reflectivity;if(this.refractionRatio!==void 0)N.refractionRatio=this.refractionRatio;if(this.gradientMap&&this.gradientMap.isTexture)N.gradientMap=this.gradientMap.toJSON(J).uuid;if(this.transmission!==void 0)N.transmission=this.transmission;if(this.transmissionMap&&this.transmissionMap.isTexture)N.transmissionMap=this.transmissionMap.toJSON(J).uuid;if(this.thickness!==void 0)N.thickness=this.thickness;if(this.thicknessMap&&this.thicknessMap.isTexture)N.thicknessMap=this.thicknessMap.toJSON(J).uuid;if(this.attenuationDistance!==void 0&&this.attenuationDistance!==1/0)N.attenuationDistance=this.attenuationDistance;if(this.attenuationColor!==void 0)N.attenuationColor=this.attenuationColor.getHex();if(this.size!==void 0)N.size=this.size;if(this.shadowSide!==null)N.shadowSide=this.shadowSide;if(this.sizeAttenuation!==void 0)N.sizeAttenuation=this.sizeAttenuation;if(this.blending!==1)N.blending=this.blending;if(this.side!==0)N.side=this.side;if(this.vertexColors===!0)N.vertexColors=!0;if(this.opacity<1)N.opacity=this.opacity;if(this.transparent===!0)N.transparent=!0;if(this.blendSrc!==204)N.blendSrc=this.blendSrc;if(this.blendDst!==205)N.blendDst=this.blendDst;if(this.blendEquation!==100)N.blendEquation=this.blendEquation;if(this.blendSrcAlpha!==null)N.blendSrcAlpha=this.blendSrcAlpha;if(this.blendDstAlpha!==null)N.blendDstAlpha=this.blendDstAlpha;if(this.blendEquationAlpha!==null)N.blendEquationAlpha=this.blendEquationAlpha;if(this.blendColor&&this.blendColor.isColor)N.blendColor=this.blendColor.getHex();if(this.blendAlpha!==0)N.blendAlpha=this.blendAlpha;if(this.depthFunc!==3)N.depthFunc=this.depthFunc;if(this.depthTest===!1)N.depthTest=this.depthTest;if(this.depthWrite===!1)N.depthWrite=this.depthWrite;if(this.colorWrite===!1)N.colorWrite=this.colorWrite;if(this.stencilWriteMask!==255)N.stencilWriteMask=this.stencilWriteMask;if(this.stencilFunc!==519)N.stencilFunc=this.stencilFunc;if(this.stencilRef!==0)N.stencilRef=this.stencilRef;if(this.stencilFuncMask!==255)N.stencilFuncMask=this.stencilFuncMask;if(this.stencilFail!==7680)N.stencilFail=this.stencilFail;if(this.stencilZFail!==7680)N.stencilZFail=this.stencilZFail;if(this.stencilZPass!==7680)N.stencilZPass=this.stencilZPass;if(this.stencilWrite===!0)N.stencilWrite=this.stencilWrite;if(this.rotation!==void 0&&this.rotation!==0)N.rotation=this.rotation;if(this.polygonOffset===!0)N.polygonOffset=!0;if(this.polygonOffsetFactor!==0)N.polygonOffsetFactor=this.polygonOffsetFactor;if(this.polygonOffsetUnits!==0)N.polygonOffsetUnits=this.polygonOffsetUnits;if(this.linewidth!==void 0&&this.linewidth!==1)N.linewidth=this.linewidth;if(this.dashSize!==void 0)N.dashSize=this.dashSize;if(this.gapSize!==void 0)N.gapSize=this.gapSize;if(this.scale!==void 0)N.scale=this.scale;if(this.dithering===!0)N.dithering=!0;if(this.alphaTest>0)N.alphaTest=this.alphaTest;if(this.alphaHash===!0)N.alphaHash=!0;if(this.alphaToCoverage===!0)N.alphaToCoverage=!0;if(this.premultipliedAlpha===!0)N.premultipliedAlpha=!0;if(this.forceSinglePass===!0)N.forceSinglePass=!0;if(this.wireframe===!0)N.wireframe=!0;if(this.wireframeLinewidth>1)N.wireframeLinewidth=this.wireframeLinewidth;if(this.wireframeLinecap!=="round")N.wireframeLinecap=this.wireframeLinecap;if(this.wireframeLinejoin!=="round")N.wireframeLinejoin=this.wireframeLinejoin;if(this.flatShading===!0)N.flatShading=!0;if(this.visible===!1)N.visible=!1;if(this.toneMapped===!1)N.toneMapped=!1;if(this.fog===!1)N.fog=!1;if(Object.keys(this.userData).length>0)N.userData=this.userData;function Q(Z){let W=[];for(let Y in Z){let K=Z[Y];delete K.metadata,W.push(K)}return W}if($){let Z=Q(J.textures),W=Q(J.images);if(Z.length>0)N.textures=Z;if(W.length>0)N.images=W}return N}clone(){return new this.constructor().copy(this)}copy(J){this.name=J.name,this.blending=J.blending,this.side=J.side,this.vertexColors=J.vertexColors,this.opacity=J.opacity,this.transparent=J.transparent,this.blendSrc=J.blendSrc,this.blendDst=J.blendDst,this.blendEquation=J.blendEquation,this.blendSrcAlpha=J.blendSrcAlpha,this.blendDstAlpha=J.blendDstAlpha,this.blendEquationAlpha=J.blendEquationAlpha,this.blendColor.copy(J.blendColor),this.blendAlpha=J.blendAlpha,this.depthFunc=J.depthFunc,this.depthTest=J.depthTest,this.depthWrite=J.depthWrite,this.stencilWriteMask=J.stencilWriteMask,this.stencilFunc=J.stencilFunc,this.stencilRef=J.stencilRef,this.stencilFuncMask=J.stencilFuncMask,this.stencilFail=J.stencilFail,this.stencilZFail=J.stencilZFail,this.stencilZPass=J.stencilZPass,this.stencilWrite=J.stencilWrite;let $=J.clippingPlanes,N=null;if($!==null){let Q=$.length;N=Array(Q);for(let Z=0;Z!==Q;++Z)N[Z]=$[Z].clone()}return this.clippingPlanes=N,this.clipIntersection=J.clipIntersection,this.clipShadows=J.clipShadows,this.shadowSide=J.shadowSide,this.colorWrite=J.colorWrite,this.precision=J.precision,this.polygonOffset=J.polygonOffset,this.polygonOffsetFactor=J.polygonOffsetFactor,this.polygonOffsetUnits=J.polygonOffsetUnits,this.dithering=J.dithering,this.alphaTest=J.alphaTest,this.alphaHash=J.alphaHash,this.alphaToCoverage=J.alphaToCoverage,this.premultipliedAlpha=J.premultipliedAlpha,this.forceSinglePass=J.forceSinglePass,this.visible=J.visible,this.toneMapped=J.toneMapped,this.userData=JSON.parse(JSON.stringify(J.userData)),this}dispose(){this.dispatchEvent({type:"dispose"})}set needsUpdate(J){if(J===!0)this.version++}}class u9 extends y9{constructor(J){super();this.isMeshBasicMaterial=!0,this.type="MeshBasicMaterial",this.color=new i0(16777215),this.map=null,this.lightMap=null,this.lightMapIntensity=1,this.aoMap=null,this.aoMapIntensity=1,this.specularMap=null,this.alphaMap=null,this.envMap=null,this.envMapRotation=new $9,this.combine=0,this.reflectivity=1,this.refractionRatio=0.98,this.wireframe=!1,this.wireframeLinewidth=1,this.wireframeLinecap="round",this.wireframeLinejoin="round",this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.color.copy(J.color),this.map=J.map,this.lightMap=J.lightMap,this.lightMapIntensity=J.lightMapIntensity,this.aoMap=J.aoMap,this.aoMapIntensity=J.aoMapIntensity,this.specularMap=J.specularMap,this.alphaMap=J.alphaMap,this.envMap=J.envMap,this.envMapRotation.copy(J.envMapRotation),this.combine=J.combine,this.reflectivity=J.reflectivity,this.refractionRatio=J.refractionRatio,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.wireframeLinecap=J.wireframeLinecap,this.wireframeLinejoin=J.wireframeLinejoin,this.fog=J.fog,this}}var R8=new h,q6=new p0,BW=0;class o8{constructor(J,$,N=!1){if(Array.isArray(J))throw TypeError("THREE.BufferAttribute: array should be a Typed Array.");this.isBufferAttribute=!0,Object.defineProperty(this,"id",{value:BW++}),this.name="",this.array=J,this.itemSize=$,this.count=J!==void 0?J.length/$:0,this.normalized=N,this.usage=35044,this.updateRanges=[],this.gpuType=1015,this.version=0}onUploadCallback(){}set needsUpdate(J){if(J===!0)this.version++}setUsage(J){return this.usage=J,this}addUpdateRange(J,$){this.updateRanges.push({start:J,count:$})}clearUpdateRanges(){this.updateRanges.length=0}copy(J){return this.name=J.name,this.array=new J.array.constructor(J.array),this.itemSize=J.itemSize,this.count=J.count,this.normalized=J.normalized,this.usage=J.usage,this.gpuType=J.gpuType,this}copyAt(J,$,N){J*=this.itemSize,N*=$.itemSize;for(let Q=0,Z=this.itemSize;Q<Z;Q++)this.array[J+Q]=$.array[N+Q];return this}copyArray(J){return this.array.set(J),this}applyMatrix3(J){if(this.itemSize===2)for(let $=0,N=this.count;$<N;$++)q6.fromBufferAttribute(this,$),q6.applyMatrix3(J),this.setXY($,q6.x,q6.y);else if(this.itemSize===3)for(let $=0,N=this.count;$<N;$++)R8.fromBufferAttribute(this,$),R8.applyMatrix3(J),this.setXYZ($,R8.x,R8.y,R8.z);return this}applyMatrix4(J){for(let $=0,N=this.count;$<N;$++)R8.fromBufferAttribute(this,$),R8.applyMatrix4(J),this.setXYZ($,R8.x,R8.y,R8.z);return this}applyNormalMatrix(J){for(let $=0,N=this.count;$<N;$++)R8.fromBufferAttribute(this,$),R8.applyNormalMatrix(J),this.setXYZ($,R8.x,R8.y,R8.z);return this}transformDirection(J){for(let $=0,N=this.count;$<N;$++)R8.fromBufferAttribute(this,$),R8.transformDirection(J),this.setXYZ($,R8.x,R8.y,R8.z);return this}set(J,$=0){return this.array.set(J,$),this}getComponent(J,$){let N=this.array[J*this.itemSize+$];if(this.normalized)N=DJ(N,this.array);return N}setComponent(J,$,N){if(this.normalized)N=A8(N,this.array);return this.array[J*this.itemSize+$]=N,this}getX(J){let $=this.array[J*this.itemSize];if(this.normalized)$=DJ($,this.array);return $}setX(J,$){if(this.normalized)$=A8($,this.array);return this.array[J*this.itemSize]=$,this}getY(J){let $=this.array[J*this.itemSize+1];if(this.normalized)$=DJ($,this.array);return $}setY(J,$){if(this.normalized)$=A8($,this.array);return this.array[J*this.itemSize+1]=$,this}getZ(J){let $=this.array[J*this.itemSize+2];if(this.normalized)$=DJ($,this.array);return $}setZ(J,$){if(this.normalized)$=A8($,this.array);return this.array[J*this.itemSize+2]=$,this}getW(J){let $=this.array[J*this.itemSize+3];if(this.normalized)$=DJ($,this.array);return $}setW(J,$){if(this.normalized)$=A8($,this.array);return this.array[J*this.itemSize+3]=$,this}setXY(J,$,N){if(J*=this.itemSize,this.normalized)$=A8($,this.array),N=A8(N,this.array);return this.array[J+0]=$,this.array[J+1]=N,this}setXYZ(J,$,N,Q){if(J*=this.itemSize,this.normalized)$=A8($,this.array),N=A8(N,this.array),Q=A8(Q,this.array);return this.array[J+0]=$,this.array[J+1]=N,this.array[J+2]=Q,this}setXYZW(J,$,N,Q,Z){if(J*=this.itemSize,this.normalized)$=A8($,this.array),N=A8(N,this.array),Q=A8(Q,this.array),Z=A8(Z,this.array);return this.array[J+0]=$,this.array[J+1]=N,this.array[J+2]=Q,this.array[J+3]=Z,this}onUpload(J){return this.onUploadCallback=J,this}clone(){return new this.constructor(this.array,this.itemSize).copy(this)}toJSON(){let J={itemSize:this.itemSize,type:this.array.constructor.name,array:Array.from(this.array),normalized:this.normalized};if(this.name!=="")J.name=this.name;if(this.usage!==35044)J.usage=this.usage;return J}}class u6 extends o8{constructor(J,$,N){super(new Uint16Array(J),$,N)}}class c6 extends o8{constructor(J,$,N){super(new Uint32Array(J),$,N)}}class B8 extends o8{constructor(J,$,N){super(new Float32Array(J),$,N)}}var CW=0,s8=new U8,j7=new F8,UJ=new h,d8=new Y9,vJ=new Y9,V8=new h;class u8 extends _9{constructor(){super();this.isBufferGeometry=!0,Object.defineProperty(this,"id",{value:CW++}),this.uuid=zJ(),this.name="",this.type="BufferGeometry",this.index=null,this.indirect=null,this.attributes={},this.morphAttributes={},this.morphTargetsRelative=!1,this.groups=[],this.boundingBox=null,this.boundingSphere=null,this.drawRange={start:0,count:1/0},this.userData={}}getIndex(){return this.index}setIndex(J){if(Array.isArray(J))this.index=new((S$(J))?c6:u6)(J,1);else this.index=J;return this}setIndirect(J){return this.indirect=J,this}getIndirect(){return this.indirect}getAttribute(J){return this.attributes[J]}setAttribute(J,$){return this.attributes[J]=$,this}deleteAttribute(J){return delete this.attributes[J],this}hasAttribute(J){return this.attributes[J]!==void 0}addGroup(J,$,N=0){this.groups.push({start:J,count:$,materialIndex:N})}clearGroups(){this.groups=[]}setDrawRange(J,$){this.drawRange.start=J,this.drawRange.count=$}applyMatrix4(J){let $=this.attributes.position;if($!==void 0)$.applyMatrix4(J),$.needsUpdate=!0;let N=this.attributes.normal;if(N!==void 0){let Z=new l0().getNormalMatrix(J);N.applyNormalMatrix(Z),N.needsUpdate=!0}let Q=this.attributes.tangent;if(Q!==void 0)Q.transformDirection(J),Q.needsUpdate=!0;if(this.boundingBox!==null)this.computeBoundingBox();if(this.boundingSphere!==null)this.computeBoundingSphere();return this}applyQuaternion(J){return s8.makeRotationFromQuaternion(J),this.applyMatrix4(s8),this}rotateX(J){return s8.makeRotationX(J),this.applyMatrix4(s8),this}rotateY(J){return s8.makeRotationY(J),this.applyMatrix4(s8),this}rotateZ(J){return s8.makeRotationZ(J),this.applyMatrix4(s8),this}translate(J,$,N){return s8.makeTranslation(J,$,N),this.applyMatrix4(s8),this}scale(J,$,N){return s8.makeScale(J,$,N),this.applyMatrix4(s8),this}lookAt(J){return j7.lookAt(J),j7.updateMatrix(),this.applyMatrix4(j7.matrix),this}center(){return this.computeBoundingBox(),this.boundingBox.getCenter(UJ).negate(),this.translate(UJ.x,UJ.y,UJ.z),this}setFromPoints(J){let $=this.getAttribute("position");if($===void 0){let N=[];for(let Q=0,Z=J.length;Q<Z;Q++){let W=J[Q];N.push(W.x,W.y,W.z||0)}this.setAttribute("position",new B8(N,3))}else{let N=Math.min(J.length,$.count);for(let Q=0;Q<N;Q++){let Z=J[Q];$.setXYZ(Q,Z.x,Z.y,Z.z||0)}if(J.length>$.count)console.warn("THREE.BufferGeometry: Buffer size too small for points data. Use .dispose() and create a new geometry.");$.needsUpdate=!0}return this}computeBoundingBox(){if(this.boundingBox===null)this.boundingBox=new Y9;let J=this.attributes.position,$=this.morphAttributes.position;if(J&&J.isGLBufferAttribute){console.error("THREE.BufferGeometry.computeBoundingBox(): GLBufferAttribute requires a manual bounding box.",this),this.boundingBox.set(new h(-1/0,-1/0,-1/0),new h(1/0,1/0,1/0));return}if(J!==void 0){if(this.boundingBox.setFromBufferAttribute(J),$)for(let N=0,Q=$.length;N<Q;N++){let Z=$[N];if(d8.setFromBufferAttribute(Z),this.morphTargetsRelative)V8.addVectors(this.boundingBox.min,d8.min),this.boundingBox.expandByPoint(V8),V8.addVectors(this.boundingBox.max,d8.max),this.boundingBox.expandByPoint(V8);else this.boundingBox.expandByPoint(d8.min),this.boundingBox.expandByPoint(d8.max)}}else this.boundingBox.makeEmpty();if(isNaN(this.boundingBox.min.x)||isNaN(this.boundingBox.min.y)||isNaN(this.boundingBox.min.z))console.error('THREE.BufferGeometry.computeBoundingBox(): Computed min/max have NaN values. The "position" attribute is likely to have NaN values.',this)}computeBoundingSphere(){if(this.boundingSphere===null)this.boundingSphere=new BJ;let J=this.attributes.position,$=this.morphAttributes.position;if(J&&J.isGLBufferAttribute){console.error("THREE.BufferGeometry.computeBoundingSphere(): GLBufferAttribute requires a manual bounding sphere.",this),this.boundingSphere.set(new h,1/0);return}if(J){let N=this.boundingSphere.center;if(d8.setFromBufferAttribute(J),$)for(let Z=0,W=$.length;Z<W;Z++){let Y=$[Z];if(vJ.setFromBufferAttribute(Y),this.morphTargetsRelative)V8.addVectors(d8.min,vJ.min),d8.expandByPoint(V8),V8.addVectors(d8.max,vJ.max),d8.expandByPoint(V8);else d8.expandByPoint(vJ.min),d8.expandByPoint(vJ.max)}d8.getCenter(N);let Q=0;for(let Z=0,W=J.count;Z<W;Z++)V8.fromBufferAttribute(J,Z),Q=Math.max(Q,N.distanceToSquared(V8));if($)for(let Z=0,W=$.length;Z<W;Z++){let Y=$[Z],K=this.morphTargetsRelative;for(let H=0,X=Y.count;H<X;H++){if(V8.fromBufferAttribute(Y,H),K)UJ.fromBufferAttribute(J,H),V8.add(UJ);Q=Math.max(Q,N.distanceToSquared(V8))}}if(this.boundingSphere.radius=Math.sqrt(Q),isNaN(this.boundingSphere.radius))console.error('THREE.BufferGeometry.computeBoundingSphere(): Computed radius is NaN. The "position" attribute is likely to have NaN values.',this)}}computeTangents(){let J=this.index,$=this.attributes;if(J===null||$.position===void 0||$.normal===void 0||$.uv===void 0){console.error("THREE.BufferGeometry: .computeTangents() failed. Missing required attributes (index, position, normal or uv)");return}let{position:N,normal:Q,uv:Z}=$;if(this.hasAttribute("tangent")===!1)this.setAttribute("tangent",new o8(new Float32Array(4*N.count),4));let W=this.getAttribute("tangent"),Y=[],K=[];for(let b=0;b<N.count;b++)Y[b]=new h,K[b]=new h;let H=new h,X=new h,G=new h,E=new p0,q=new p0,k=new p0,M=new h,_=new h;function D(b,g,V){H.fromBufferAttribute(N,b),X.fromBufferAttribute(N,g),G.fromBufferAttribute(N,V),E.fromBufferAttribute(Z,b),q.fromBufferAttribute(Z,g),k.fromBufferAttribute(Z,V),X.sub(H),G.sub(H),q.sub(E),k.sub(E);let F=1/(q.x*k.y-k.x*q.y);if(!isFinite(F))return;M.copy(X).multiplyScalar(k.y).addScaledVector(G,-q.y).multiplyScalar(F),_.copy(G).multiplyScalar(q.x).addScaledVector(X,-k.x).multiplyScalar(F),Y[b].add(M),Y[g].add(M),Y[V].add(M),K[b].add(_),K[g].add(_),K[V].add(_)}let U=this.groups;if(U.length===0)U=[{start:0,count:J.count}];for(let b=0,g=U.length;b<g;++b){let V=U[b],F=V.start,S=V.count;for(let s=F,i=F+S;s<i;s+=3)D(J.getX(s+0),J.getX(s+1),J.getX(s+2))}let C=new h,L=new h,B=new h,y=new h;function I(b){B.fromBufferAttribute(Q,b),y.copy(B);let g=Y[b];C.copy(g),C.sub(B.multiplyScalar(B.dot(g))).normalize(),L.crossVectors(y,g);let F=L.dot(K[b])<0?-1:1;W.setXYZW(b,C.x,C.y,C.z,F)}for(let b=0,g=U.length;b<g;++b){let V=U[b],F=V.start,S=V.count;for(let s=F,i=F+S;s<i;s+=3)I(J.getX(s+0)),I(J.getX(s+1)),I(J.getX(s+2))}}computeVertexNormals(){let J=this.index,$=this.getAttribute("position");if($!==void 0){let N=this.getAttribute("normal");if(N===void 0)N=new o8(new Float32Array($.count*3),3),this.setAttribute("normal",N);else for(let E=0,q=N.count;E<q;E++)N.setXYZ(E,0,0,0);let Q=new h,Z=new h,W=new h,Y=new h,K=new h,H=new h,X=new h,G=new h;if(J)for(let E=0,q=J.count;E<q;E+=3){let k=J.getX(E+0),M=J.getX(E+1),_=J.getX(E+2);Q.fromBufferAttribute($,k),Z.fromBufferAttribute($,M),W.fromBufferAttribute($,_),X.subVectors(W,Z),G.subVectors(Q,Z),X.cross(G),Y.fromBufferAttribute(N,k),K.fromBufferAttribute(N,M),H.fromBufferAttribute(N,_),Y.add(X),K.add(X),H.add(X),N.setXYZ(k,Y.x,Y.y,Y.z),N.setXYZ(M,K.x,K.y,K.z),N.setXYZ(_,H.x,H.y,H.z)}else for(let E=0,q=$.count;E<q;E+=3)Q.fromBufferAttribute($,E+0),Z.fromBufferAttribute($,E+1),W.fromBufferAttribute($,E+2),X.subVectors(W,Z),G.subVectors(Q,Z),X.cross(G),N.setXYZ(E+0,X.x,X.y,X.z),N.setXYZ(E+1,X.x,X.y,X.z),N.setXYZ(E+2,X.x,X.y,X.z);this.normalizeNormals(),N.needsUpdate=!0}}normalizeNormals(){let J=this.attributes.normal;for(let $=0,N=J.count;$<N;$++)V8.fromBufferAttribute(J,$),V8.normalize(),J.setXYZ($,V8.x,V8.y,V8.z)}toNonIndexed(){function J(Y,K){let{array:H,itemSize:X,normalized:G}=Y,E=new H.constructor(K.length*X),q=0,k=0;for(let M=0,_=K.length;M<_;M++){if(Y.isInterleavedBufferAttribute)q=K[M]*Y.data.stride+Y.offset;else q=K[M]*X;for(let D=0;D<X;D++)E[k++]=H[q++]}return new o8(E,X,G)}if(this.index===null)return console.warn("THREE.BufferGeometry.toNonIndexed(): BufferGeometry is already non-indexed."),this;let $=new u8,N=this.index.array,Q=this.attributes;for(let Y in Q){let K=Q[Y],H=J(K,N);$.setAttribute(Y,H)}let Z=this.morphAttributes;for(let Y in Z){let K=[],H=Z[Y];for(let X=0,G=H.length;X<G;X++){let E=H[X],q=J(E,N);K.push(q)}$.morphAttributes[Y]=K}$.morphTargetsRelative=this.morphTargetsRelative;let W=this.groups;for(let Y=0,K=W.length;Y<K;Y++){let H=W[Y];$.addGroup(H.start,H.count,H.materialIndex)}return $}toJSON(){let J={metadata:{version:4.7,type:"BufferGeometry",generator:"BufferGeometry.toJSON"}};if(J.uuid=this.uuid,J.type=this.type,this.name!=="")J.name=this.name;if(Object.keys(this.userData).length>0)J.userData=this.userData;if(this.parameters!==void 0){let K=this.parameters;for(let H in K)if(K[H]!==void 0)J[H]=K[H];return J}J.data={attributes:{}};let $=this.index;if($!==null)J.data.index={type:$.array.constructor.name,array:Array.prototype.slice.call($.array)};let N=this.attributes;for(let K in N){let H=N[K];J.data.attributes[K]=H.toJSON(J.data)}let Q={},Z=!1;for(let K in this.morphAttributes){let H=this.morphAttributes[K],X=[];for(let G=0,E=H.length;G<E;G++){let q=H[G];X.push(q.toJSON(J.data))}if(X.length>0)Q[K]=X,Z=!0}if(Z)J.data.morphAttributes=Q,J.data.morphTargetsRelative=this.morphTargetsRelative;let W=this.groups;if(W.length>0)J.data.groups=JSON.parse(JSON.stringify(W));let Y=this.boundingSphere;if(Y!==null)J.data.boundingSphere=Y.toJSON();return J}clone(){return new this.constructor().copy(this)}copy(J){this.index=null,this.attributes={},this.morphAttributes={},this.groups=[],this.boundingBox=null,this.boundingSphere=null;let $={};this.name=J.name;let N=J.index;if(N!==null)this.setIndex(N.clone());let Q=J.attributes;for(let H in Q){let X=Q[H];this.setAttribute(H,X.clone($))}let Z=J.morphAttributes;for(let H in Z){let X=[],G=Z[H];for(let E=0,q=G.length;E<q;E++)X.push(G[E].clone($));this.morphAttributes[H]=X}this.morphTargetsRelative=J.morphTargetsRelative;let W=J.groups;for(let H=0,X=W.length;H<X;H++){let G=W[H];this.addGroup(G.start,G.count,G.materialIndex)}let Y=J.boundingBox;if(Y!==null)this.boundingBox=Y.clone();let K=J.boundingSphere;if(K!==null)this.boundingSphere=K.clone();return this.drawRange.start=J.drawRange.start,this.drawRange.count=J.drawRange.count,this.userData=J.userData,this}dispose(){this.dispatchEvent({type:"dispose"})}}var vN=new U8,x9=new m9,k6=new BJ,hN=new h,O6=new h,R6=new h,F6=new h,f7=new h,M6=new h,xN=new h,_6=new h;class b8 extends F8{constructor(J=new u8,$=new u9){super();this.isMesh=!0,this.type="Mesh",this.geometry=J,this.material=$,this.morphTargetDictionary=void 0,this.morphTargetInfluences=void 0,this.count=1,this.updateMorphTargets()}copy(J,$){if(super.copy(J,$),J.morphTargetInfluences!==void 0)this.morphTargetInfluences=J.morphTargetInfluences.slice();if(J.morphTargetDictionary!==void 0)this.morphTargetDictionary=Object.assign({},J.morphTargetDictionary);return this.material=Array.isArray(J.material)?J.material.slice():J.material,this.geometry=J.geometry,this}updateMorphTargets(){let $=this.geometry.morphAttributes,N=Object.keys($);if(N.length>0){let Q=$[N[0]];if(Q!==void 0){this.morphTargetInfluences=[],this.morphTargetDictionary={};for(let Z=0,W=Q.length;Z<W;Z++){let Y=Q[Z].name||String(Z);this.morphTargetInfluences.push(0),this.morphTargetDictionary[Y]=Z}}}}getVertexPosition(J,$){let N=this.geometry,Q=N.attributes.position,Z=N.morphAttributes.position,W=N.morphTargetsRelative;$.fromBufferAttribute(Q,J);let Y=this.morphTargetInfluences;if(Z&&Y){M6.set(0,0,0);for(let K=0,H=Z.length;K<H;K++){let X=Y[K],G=Z[K];if(X===0)continue;if(f7.fromBufferAttribute(G,J),W)M6.addScaledVector(f7,X);else M6.addScaledVector(f7.sub($),X)}$.add(M6)}return $}raycast(J,$){let N=this.geometry,Q=this.material,Z=this.matrixWorld;if(Q===void 0)return;if(N.boundingSphere===null)N.computeBoundingSphere();if(k6.copy(N.boundingSphere),k6.applyMatrix4(Z),x9.copy(J.ray).recast(J.near),k6.containsPoint(x9.origin)===!1){if(x9.intersectSphere(k6,hN)===null)return;if(x9.origin.distanceToSquared(hN)>(J.far-J.near)**2)return}if(vN.copy(Z).invert(),x9.copy(J.ray).applyMatrix4(vN),N.boundingBox!==null){if(x9.intersectsBox(N.boundingBox)===!1)return}this._computeIntersections(J,$,x9)}_computeIntersections(J,$,N){let Q,Z=this.geometry,W=this.material,Y=Z.index,K=Z.attributes.position,H=Z.attributes.uv,X=Z.attributes.uv1,G=Z.attributes.normal,E=Z.groups,q=Z.drawRange;if(Y!==null)if(Array.isArray(W))for(let k=0,M=E.length;k<M;k++){let _=E[k],D=W[_.materialIndex],U=Math.max(_.start,q.start),C=Math.min(Y.count,Math.min(_.start+_.count,q.start+q.count));for(let L=U,B=C;L<B;L+=3){let y=Y.getX(L),I=Y.getX(L+1),b=Y.getX(L+2);if(Q=L6(this,D,J,N,H,X,G,y,I,b),Q)Q.faceIndex=Math.floor(L/3),Q.face.materialIndex=_.materialIndex,$.push(Q)}}else{let k=Math.max(0,q.start),M=Math.min(Y.count,q.start+q.count);for(let _=k,D=M;_<D;_+=3){let U=Y.getX(_),C=Y.getX(_+1),L=Y.getX(_+2);if(Q=L6(this,W,J,N,H,X,G,U,C,L),Q)Q.faceIndex=Math.floor(_/3),$.push(Q)}}else if(K!==void 0)if(Array.isArray(W))for(let k=0,M=E.length;k<M;k++){let _=E[k],D=W[_.materialIndex],U=Math.max(_.start,q.start),C=Math.min(K.count,Math.min(_.start+_.count,q.start+q.count));for(let L=U,B=C;L<B;L+=3){let y=L,I=L+1,b=L+2;if(Q=L6(this,D,J,N,H,X,G,y,I,b),Q)Q.faceIndex=Math.floor(L/3),Q.face.materialIndex=_.materialIndex,$.push(Q)}}else{let k=Math.max(0,q.start),M=Math.min(K.count,q.start+q.count);for(let _=k,D=M;_<D;_+=3){let U=_,C=_+1,L=_+2;if(Q=L6(this,W,J,N,H,X,G,U,C,L),Q)Q.faceIndex=Math.floor(_/3),$.push(Q)}}}}function IW(J,$,N,Q,Z,W,Y,K){let H;if($.side===1)H=Q.intersectTriangle(Y,W,Z,!0,K);else H=Q.intersectTriangle(Z,W,Y,$.side===0,K);if(H===null)return null;_6.copy(K),_6.applyMatrix4(J.matrixWorld);let X=N.ray.origin.distanceTo(_6);if(X<N.near||X>N.far)return null;return{distance:X,point:_6.clone(),object:J}}function L6(J,$,N,Q,Z,W,Y,K,H,X){J.getVertexPosition(K,O6),J.getVertexPosition(H,R6),J.getVertexPosition(X,F6);let G=IW(J,$,N,Q,O6,R6,F6,xN);if(G){let E=new h;if(i8.getBarycoord(xN,O6,R6,F6,E),Z)G.uv=i8.getInterpolatedAttribute(Z,K,H,X,E,new p0);if(W)G.uv1=i8.getInterpolatedAttribute(W,K,H,X,E,new p0);if(Y){if(G.normal=i8.getInterpolatedAttribute(Y,K,H,X,E,new h),G.normal.dot(Q.direction)>0)G.normal.multiplyScalar(-1)}let q={a:K,b:H,c:X,normal:new h,materialIndex:0};i8.getNormal(O6,R6,F6,q.normal),G.face=q,G.barycoord=E}return G}class CJ extends u8{constructor(J=1,$=1,N=1,Q=1,Z=1,W=1){super();this.type="BoxGeometry",this.parameters={width:J,height:$,depth:N,widthSegments:Q,heightSegments:Z,depthSegments:W};let Y=this;Q=Math.floor(Q),Z=Math.floor(Z),W=Math.floor(W);let K=[],H=[],X=[],G=[],E=0,q=0;k("z","y","x",-1,-1,N,$,J,W,Z,0),k("z","y","x",1,-1,N,$,-J,W,Z,1),k("x","z","y",1,1,J,N,$,Q,W,2),k("x","z","y",1,-1,J,N,-$,Q,W,3),k("x","y","z",1,-1,J,$,N,Q,Z,4),k("x","y","z",-1,-1,J,$,-N,Q,Z,5),this.setIndex(K),this.setAttribute("position",new B8(H,3)),this.setAttribute("normal",new B8(X,3)),this.setAttribute("uv",new B8(G,2));function k(M,_,D,U,C,L,B,y,I,b,g){let V=L/I,F=B/b,S=L/2,s=B/2,i=y/2,m=I+1,T=b+1,p=0,$0=0,c=new h;for(let X0=0;X0<T;X0++){let q0=X0*F-s;for(let P0=0;P0<m;P0++){let d0=P0*V-S;c[M]=d0*U,c[_]=q0*C,c[D]=i,H.push(c.x,c.y,c.z),c[M]=0,c[_]=0,c[D]=y>0?1:-1,X.push(c.x,c.y,c.z),G.push(P0/I),G.push(1-X0/b),p+=1}}for(let X0=0;X0<b;X0++)for(let q0=0;q0<I;q0++){let P0=E+q0+m*X0,d0=E+q0+m*(X0+1),J8=E+(q0+1)+m*(X0+1),c0=E+(q0+1)+m*X0;K.push(P0,d0,c0),K.push(d0,J8,c0),$0+=6}Y.addGroup(q,$0,g),q+=$0,E+=p}}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new CJ(J.width,J.height,J.depth,J.widthSegments,J.heightSegments,J.depthSegments)}}function c9(J){let $={};for(let N in J){$[N]={};for(let Q in J[N]){let Z=J[N][Q];if(Z&&(Z.isColor||Z.isMatrix3||Z.isMatrix4||Z.isVector2||Z.isVector3||Z.isVector4||Z.isTexture||Z.isQuaternion))if(Z.isRenderTargetTexture)console.warn("UniformsUtils: Textures of render targets cannot be cloned via cloneUniforms() or mergeUniforms()."),$[N][Q]=null;else $[N][Q]=Z.clone();else if(Array.isArray(Z))$[N][Q]=Z.slice();else $[N][Q]=Z}}return $}function C8(J){let $={};for(let N=0;N<J.length;N++){let Q=c9(J[N]);for(let Z in Q)$[Z]=Q[Z]}return $}function PW(J){let $=[];for(let N=0;N<J.length;N++)$.push(J[N].clone());return $}function f$(J){let $=J.getRenderTarget();if($===null)return J.outputColorSpace;if($.isXRRenderTarget===!0)return $.texture.colorSpace;return o0.workingColorSpace}var eQ={clone:c9,merge:C8},AW=`void main() {
	gl_Position = projectionMatrix * modelViewMatrix * vec4( position, 1.0 );
}`,TW=`void main() {
	gl_FragColor = vec4( 1.0, 0.0, 0.0, 1.0 );
}`;class H9 extends y9{constructor(J){super();if(this.isShaderMaterial=!0,this.type="ShaderMaterial",this.defines={},this.uniforms={},this.uniformsGroups=[],this.vertexShader=AW,this.fragmentShader=TW,this.linewidth=1,this.wireframe=!1,this.wireframeLinewidth=1,this.fog=!1,this.lights=!1,this.clipping=!1,this.forceSinglePass=!0,this.extensions={clipCullDistance:!1,multiDraw:!1},this.defaultAttributeValues={color:[1,1,1],uv:[0,0],uv1:[0,0]},this.index0AttributeName=void 0,this.uniformsNeedUpdate=!1,this.glslVersion=null,J!==void 0)this.setValues(J)}copy(J){return super.copy(J),this.fragmentShader=J.fragmentShader,this.vertexShader=J.vertexShader,this.uniforms=c9(J.uniforms),this.uniformsGroups=PW(J.uniformsGroups),this.defines=Object.assign({},J.defines),this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.fog=J.fog,this.lights=J.lights,this.clipping=J.clipping,this.extensions=Object.assign({},J.extensions),this.glslVersion=J.glslVersion,this}toJSON(J){let $=super.toJSON(J);$.glslVersion=this.glslVersion,$.uniforms={};for(let Q in this.uniforms){let W=this.uniforms[Q].value;if(W&&W.isTexture)$.uniforms[Q]={type:"t",value:W.toJSON(J).uuid};else if(W&&W.isColor)$.uniforms[Q]={type:"c",value:W.getHex()};else if(W&&W.isVector2)$.uniforms[Q]={type:"v2",value:W.toArray()};else if(W&&W.isVector3)$.uniforms[Q]={type:"v3",value:W.toArray()};else if(W&&W.isVector4)$.uniforms[Q]={type:"v4",value:W.toArray()};else if(W&&W.isMatrix3)$.uniforms[Q]={type:"m3",value:W.toArray()};else if(W&&W.isMatrix4)$.uniforms[Q]={type:"m4",value:W.toArray()};else $.uniforms[Q]={value:W}}if(Object.keys(this.defines).length>0)$.defines=this.defines;$.vertexShader=this.vertexShader,$.fragmentShader=this.fragmentShader,$.lights=this.lights,$.clipping=this.clipping;let N={};for(let Q in this.extensions)if(this.extensions[Q]===!0)N[Q]=!0;if(Object.keys(N).length>0)$.extensions=N;return $}}class n6 extends F8{constructor(){super();this.isCamera=!0,this.type="Camera",this.matrixWorldInverse=new U8,this.projectionMatrix=new U8,this.projectionMatrixInverse=new U8,this.coordinateSystem=2000,this._reversedDepth=!1}get reversedDepth(){return this._reversedDepth}copy(J,$){return super.copy(J,$),this.matrixWorldInverse.copy(J.matrixWorldInverse),this.projectionMatrix.copy(J.projectionMatrix),this.projectionMatrixInverse.copy(J.projectionMatrixInverse),this.coordinateSystem=J.coordinateSystem,this}getWorldDirection(J){return super.getWorldDirection(J).negate()}updateMatrixWorld(J){super.updateMatrixWorld(J),this.matrixWorldInverse.copy(this.matrixWorld).invert()}updateWorldMatrix(J,$){super.updateWorldMatrix(J,$),this.matrixWorldInverse.copy(this.matrixWorld).invert()}clone(){return new this.constructor().copy(this)}}var I9=new h,gN=new p0,pN=new p0;class T8 extends n6{constructor(J=50,$=1,N=0.1,Q=2000){super();this.isPerspectiveCamera=!0,this.type="PerspectiveCamera",this.fov=J,this.zoom=1,this.near=N,this.far=Q,this.focus=10,this.aspect=$,this.view=null,this.filmGauge=35,this.filmOffset=0,this.updateProjectionMatrix()}copy(J,$){return super.copy(J,$),this.fov=J.fov,this.zoom=J.zoom,this.near=J.near,this.far=J.far,this.focus=J.focus,this.aspect=J.aspect,this.view=J.view===null?null:Object.assign({},J.view),this.filmGauge=J.filmGauge,this.filmOffset=J.filmOffset,this}setFocalLength(J){let $=0.5*this.getFilmHeight()/J;this.fov=kJ*2*Math.atan($),this.updateProjectionMatrix()}getFocalLength(){let J=Math.tan(xJ*0.5*this.fov);return 0.5*this.getFilmHeight()/J}getEffectiveFOV(){return kJ*2*Math.atan(Math.tan(xJ*0.5*this.fov)/this.zoom)}getFilmWidth(){return this.filmGauge*Math.min(this.aspect,1)}getFilmHeight(){return this.filmGauge/Math.max(this.aspect,1)}getViewBounds(J,$,N){I9.set(-1,-1,0.5).applyMatrix4(this.projectionMatrixInverse),$.set(I9.x,I9.y).multiplyScalar(-J/I9.z),I9.set(1,1,0.5).applyMatrix4(this.projectionMatrixInverse),N.set(I9.x,I9.y).multiplyScalar(-J/I9.z)}getViewSize(J,$){return this.getViewBounds(J,gN,pN),$.subVectors(pN,gN)}setViewOffset(J,$,N,Q,Z,W){if(this.aspect=J/$,this.view===null)this.view={enabled:!0,fullWidth:1,fullHeight:1,offsetX:0,offsetY:0,width:1,height:1};this.view.enabled=!0,this.view.fullWidth=J,this.view.fullHeight=$,this.view.offsetX=N,this.view.offsetY=Q,this.view.width=Z,this.view.height=W,this.updateProjectionMatrix()}clearViewOffset(){if(this.view!==null)this.view.enabled=!1;this.updateProjectionMatrix()}updateProjectionMatrix(){let J=this.near,$=J*Math.tan(xJ*0.5*this.fov)/this.zoom,N=2*$,Q=this.aspect*N,Z=-0.5*Q,W=this.view;if(this.view!==null&&this.view.enabled){let{fullWidth:K,fullHeight:H}=W;Z+=W.offsetX*Q/K,$-=W.offsetY*N/H,Q*=W.width/K,N*=W.height/H}let Y=this.filmOffset;if(Y!==0)Z+=J*Y/this.getFilmWidth();this.projectionMatrix.makePerspective(Z,Z+Q,$,$-N,J,this.far,this.coordinateSystem,this.reversedDepth),this.projectionMatrixInverse.copy(this.projectionMatrix).invert()}toJSON(J){let $=super.toJSON(J);if($.object.fov=this.fov,$.object.zoom=this.zoom,$.object.near=this.near,$.object.far=this.far,$.object.focus=this.focus,$.object.aspect=this.aspect,this.view!==null)$.object.view=Object.assign({},this.view);return $.object.filmGauge=this.filmGauge,$.object.filmOffset=this.filmOffset,$}}var GJ=-90,EJ=1;class v$ extends F8{constructor(J,$,N){super();this.type="CubeCamera",this.renderTarget=N,this.coordinateSystem=null,this.activeMipmapLevel=0;let Q=new T8(GJ,EJ,J,$);Q.layers=this.layers,this.add(Q);let Z=new T8(GJ,EJ,J,$);Z.layers=this.layers,this.add(Z);let W=new T8(GJ,EJ,J,$);W.layers=this.layers,this.add(W);let Y=new T8(GJ,EJ,J,$);Y.layers=this.layers,this.add(Y);let K=new T8(GJ,EJ,J,$);K.layers=this.layers,this.add(K);let H=new T8(GJ,EJ,J,$);H.layers=this.layers,this.add(H)}updateCoordinateSystem(){let J=this.coordinateSystem,$=this.children.concat(),[N,Q,Z,W,Y,K]=$;for(let H of $)this.remove(H);if(J===2000)N.up.set(0,1,0),N.lookAt(1,0,0),Q.up.set(0,1,0),Q.lookAt(-1,0,0),Z.up.set(0,0,-1),Z.lookAt(0,1,0),W.up.set(0,0,1),W.lookAt(0,-1,0),Y.up.set(0,1,0),Y.lookAt(0,0,1),K.up.set(0,1,0),K.lookAt(0,0,-1);else if(J===2001)N.up.set(0,-1,0),N.lookAt(-1,0,0),Q.up.set(0,-1,0),Q.lookAt(1,0,0),Z.up.set(0,0,1),Z.lookAt(0,1,0),W.up.set(0,0,-1),W.lookAt(0,-1,0),Y.up.set(0,-1,0),Y.lookAt(0,0,1),K.up.set(0,-1,0),K.lookAt(0,0,-1);else throw Error("THREE.CubeCamera.updateCoordinateSystem(): Invalid coordinate system: "+J);for(let H of $)this.add(H),H.updateMatrixWorld()}update(J,$){if(this.parent===null)this.updateMatrixWorld();let{renderTarget:N,activeMipmapLevel:Q}=this;if(this.coordinateSystem!==J.coordinateSystem)this.coordinateSystem=J.coordinateSystem,this.updateCoordinateSystem();let[Z,W,Y,K,H,X]=this.children,G=J.getRenderTarget(),E=J.getActiveCubeFace(),q=J.getActiveMipmapLevel(),k=J.xr.enabled;J.xr.enabled=!1;let M=N.texture.generateMipmaps;N.texture.generateMipmaps=!1,J.setRenderTarget(N,0,Q),J.render($,Z),J.setRenderTarget(N,1,Q),J.render($,W),J.setRenderTarget(N,2,Q),J.render($,Y),J.setRenderTarget(N,3,Q),J.render($,K),J.setRenderTarget(N,4,Q),J.render($,H),N.texture.generateMipmaps=M,J.setRenderTarget(N,5,Q),J.render($,X),J.setRenderTarget(G,E,q),J.xr.enabled=k,N.texture.needsPMREMUpdate=!0}}class s6 extends S8{constructor(J=[],$=301,N,Q,Z,W,Y,K,H,X){super(J,$,N,Q,Z,W,Y,K,H,X);this.isCubeTexture=!0,this.flipY=!1}get images(){return this.image}set images(J){this.image=J}}class h$ extends L9{constructor(J=1,$={}){super(J,J,$);this.isWebGLCubeRenderTarget=!0;let N={width:J,height:J,depth:1},Q=[N,N,N,N,N,N];this.texture=new s6(Q),this._setTextureOptions($),this.texture.isRenderTargetTexture=!0}fromEquirectangularTexture(J,$){this.texture.type=$.type,this.texture.colorSpace=$.colorSpace,this.texture.generateMipmaps=$.generateMipmaps,this.texture.minFilter=$.minFilter,this.texture.magFilter=$.magFilter;let N={uniforms:{tEquirect:{value:null}},vertexShader:`

				varying vec3 vWorldDirection;

				vec3 transformDirection( in vec3 dir, in mat4 matrix ) {

					return normalize( ( matrix * vec4( dir, 0.0 ) ).xyz );

				}

				void main() {

					vWorldDirection = transformDirection( position, modelMatrix );

					#include <begin_vertex>
					#include <project_vertex>

				}
			`,fragmentShader:`

				uniform sampler2D tEquirect;

				varying vec3 vWorldDirection;

				#include <common>

				void main() {

					vec3 direction = normalize( vWorldDirection );

					vec2 sampleUV = equirectUv( direction );

					gl_FragColor = texture2D( tEquirect, sampleUV );

				}
			`},Q=new CJ(5,5,5),Z=new H9({name:"CubemapFromEquirect",uniforms:c9(N.uniforms),vertexShader:N.vertexShader,fragmentShader:N.fragmentShader,side:1,blending:0});Z.uniforms.tEquirect.value=$;let W=new b8(Q,Z),Y=$.minFilter;if($.minFilter===1008)$.minFilter=1006;return new v$(1,10,this).update(J,W),$.minFilter=Y,W.geometry.dispose(),W.material.dispose(),this}clear(J,$=!0,N=!0,Q=!0){let Z=J.getRenderTarget();for(let W=0;W<6;W++)J.setRenderTarget(this,W),J.clear($,N,Q);J.setRenderTarget(Z)}}class R9 extends F8{constructor(){super();this.isGroup=!0,this.type="Group"}}var SW={type:"move"};class rJ{constructor(){this._targetRay=null,this._grip=null,this._hand=null}getHandSpace(){if(this._hand===null)this._hand=new R9,this._hand.matrixAutoUpdate=!1,this._hand.visible=!1,this._hand.joints={},this._hand.inputState={pinching:!1};return this._hand}getTargetRaySpace(){if(this._targetRay===null)this._targetRay=new R9,this._targetRay.matrixAutoUpdate=!1,this._targetRay.visible=!1,this._targetRay.hasLinearVelocity=!1,this._targetRay.linearVelocity=new h,this._targetRay.hasAngularVelocity=!1,this._targetRay.angularVelocity=new h;return this._targetRay}getGripSpace(){if(this._grip===null)this._grip=new R9,this._grip.matrixAutoUpdate=!1,this._grip.visible=!1,this._grip.hasLinearVelocity=!1,this._grip.linearVelocity=new h,this._grip.hasAngularVelocity=!1,this._grip.angularVelocity=new h;return this._grip}dispatchEvent(J){if(this._targetRay!==null)this._targetRay.dispatchEvent(J);if(this._grip!==null)this._grip.dispatchEvent(J);if(this._hand!==null)this._hand.dispatchEvent(J);return this}connect(J){if(J&&J.hand){let $=this._hand;if($)for(let N of J.hand.values())this._getHandJoint($,N)}return this.dispatchEvent({type:"connected",data:J}),this}disconnect(J){if(this.dispatchEvent({type:"disconnected",data:J}),this._targetRay!==null)this._targetRay.visible=!1;if(this._grip!==null)this._grip.visible=!1;if(this._hand!==null)this._hand.visible=!1;return this}update(J,$,N){let Q=null,Z=null,W=null,Y=this._targetRay,K=this._grip,H=this._hand;if(J&&$.session.visibilityState!=="visible-blurred"){if(H&&J.hand){W=!0;for(let M of J.hand.values()){let _=$.getJointPose(M,N),D=this._getHandJoint(H,M);if(_!==null)D.matrix.fromArray(_.transform.matrix),D.matrix.decompose(D.position,D.rotation,D.scale),D.matrixWorldNeedsUpdate=!0,D.jointRadius=_.radius;D.visible=_!==null}let X=H.joints["index-finger-tip"],G=H.joints["thumb-tip"],E=X.position.distanceTo(G.position),q=0.02,k=0.005;if(H.inputState.pinching&&E>q+k)H.inputState.pinching=!1,this.dispatchEvent({type:"pinchend",handedness:J.handedness,target:this});else if(!H.inputState.pinching&&E<=q-k)H.inputState.pinching=!0,this.dispatchEvent({type:"pinchstart",handedness:J.handedness,target:this})}else if(K!==null&&J.gripSpace){if(Z=$.getPose(J.gripSpace,N),Z!==null){if(K.matrix.fromArray(Z.transform.matrix),K.matrix.decompose(K.position,K.rotation,K.scale),K.matrixWorldNeedsUpdate=!0,Z.linearVelocity)K.hasLinearVelocity=!0,K.linearVelocity.copy(Z.linearVelocity);else K.hasLinearVelocity=!1;if(Z.angularVelocity)K.hasAngularVelocity=!0,K.angularVelocity.copy(Z.angularVelocity);else K.hasAngularVelocity=!1}}if(Y!==null){if(Q=$.getPose(J.targetRaySpace,N),Q===null&&Z!==null)Q=Z;if(Q!==null){if(Y.matrix.fromArray(Q.transform.matrix),Y.matrix.decompose(Y.position,Y.rotation,Y.scale),Y.matrixWorldNeedsUpdate=!0,Q.linearVelocity)Y.hasLinearVelocity=!0,Y.linearVelocity.copy(Q.linearVelocity);else Y.hasLinearVelocity=!1;if(Q.angularVelocity)Y.hasAngularVelocity=!0,Y.angularVelocity.copy(Q.angularVelocity);else Y.hasAngularVelocity=!1;this.dispatchEvent(SW)}}}if(Y!==null)Y.visible=Q!==null;if(K!==null)K.visible=Z!==null;if(H!==null)H.visible=W!==null;return this}_getHandJoint(J,$){if(J.joints[$.jointName]===void 0){let N=new R9;N.matrixAutoUpdate=!1,N.visible=!1,J.joints[$.jointName]=N,J.add(N)}return J.joints[$.jointName]}}class i6 extends F8{constructor(){super();if(this.isScene=!0,this.type="Scene",this.background=null,this.environment=null,this.fog=null,this.backgroundBlurriness=0,this.backgroundIntensity=1,this.backgroundRotation=new $9,this.environmentIntensity=1,this.environmentRotation=new $9,this.overrideMaterial=null,typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("observe",{detail:this}))}copy(J,$){if(super.copy(J,$),J.background!==null)this.background=J.background.clone();if(J.environment!==null)this.environment=J.environment.clone();if(J.fog!==null)this.fog=J.fog.clone();if(this.backgroundBlurriness=J.backgroundBlurriness,this.backgroundIntensity=J.backgroundIntensity,this.backgroundRotation.copy(J.backgroundRotation),this.environmentIntensity=J.environmentIntensity,this.environmentRotation.copy(J.environmentRotation),J.overrideMaterial!==null)this.overrideMaterial=J.overrideMaterial.clone();return this.matrixAutoUpdate=J.matrixAutoUpdate,this}toJSON(J){let $=super.toJSON(J);if(this.fog!==null)$.object.fog=this.fog.toJSON();if(this.backgroundBlurriness>0)$.object.backgroundBlurriness=this.backgroundBlurriness;if(this.backgroundIntensity!==1)$.object.backgroundIntensity=this.backgroundIntensity;if($.object.backgroundRotation=this.backgroundRotation.toArray(),this.environmentIntensity!==1)$.object.environmentIntensity=this.environmentIntensity;return $.object.environmentRotation=this.environmentRotation.toArray(),$}}var v7=new h,bW=new h,yW=new l0;class l8{constructor(J=new h(1,0,0),$=0){this.isPlane=!0,this.normal=J,this.constant=$}set(J,$){return this.normal.copy(J),this.constant=$,this}setComponents(J,$,N,Q){return this.normal.set(J,$,N),this.constant=Q,this}setFromNormalAndCoplanarPoint(J,$){return this.normal.copy(J),this.constant=-$.dot(this.normal),this}setFromCoplanarPoints(J,$,N){let Q=v7.subVectors(N,$).cross(bW.subVectors(J,$)).normalize();return this.setFromNormalAndCoplanarPoint(Q,J),this}copy(J){return this.normal.copy(J.normal),this.constant=J.constant,this}normalize(){let J=1/this.normal.length();return this.normal.multiplyScalar(J),this.constant*=J,this}negate(){return this.constant*=-1,this.normal.negate(),this}distanceToPoint(J){return this.normal.dot(J)+this.constant}distanceToSphere(J){return this.distanceToPoint(J.center)-J.radius}projectPoint(J,$){return $.copy(J).addScaledVector(this.normal,-this.distanceToPoint(J))}intersectLine(J,$){let N=J.delta(v7),Q=this.normal.dot(N);if(Q===0){if(this.distanceToPoint(J.start)===0)return $.copy(J.start);return null}let Z=-(J.start.dot(this.normal)+this.constant)/Q;if(Z<0||Z>1)return null;return $.copy(J.start).addScaledVector(N,Z)}intersectsLine(J){let $=this.distanceToPoint(J.start),N=this.distanceToPoint(J.end);return $<0&&N>0||N<0&&$>0}intersectsBox(J){return J.intersectsPlane(this)}intersectsSphere(J){return J.intersectsPlane(this)}coplanarPoint(J){return J.copy(this.normal).multiplyScalar(-this.constant)}applyMatrix4(J,$){let N=$||yW.getNormalMatrix(J),Q=this.coplanarPoint(v7).applyMatrix4(J),Z=this.normal.applyMatrix3(N).normalize();return this.constant=-Q.dot(Z),this}translate(J){return this.constant-=J.dot(this.normal),this}equals(J){return J.normal.equals(this.normal)&&J.constant===this.constant}clone(){return new this.constructor().copy(this)}}var g9=new BJ,jW=new p0(0.5,0.5),V6=new h;class tJ{constructor(J=new l8,$=new l8,N=new l8,Q=new l8,Z=new l8,W=new l8){this.planes=[J,$,N,Q,Z,W]}set(J,$,N,Q,Z,W){let Y=this.planes;return Y[0].copy(J),Y[1].copy($),Y[2].copy(N),Y[3].copy(Q),Y[4].copy(Z),Y[5].copy(W),this}copy(J){let $=this.planes;for(let N=0;N<6;N++)$[N].copy(J.planes[N]);return this}setFromProjectionMatrix(J,$=2000,N=!1){let Q=this.planes,Z=J.elements,W=Z[0],Y=Z[1],K=Z[2],H=Z[3],X=Z[4],G=Z[5],E=Z[6],q=Z[7],k=Z[8],M=Z[9],_=Z[10],D=Z[11],U=Z[12],C=Z[13],L=Z[14],B=Z[15];if(Q[0].setComponents(H-W,q-X,D-k,B-U).normalize(),Q[1].setComponents(H+W,q+X,D+k,B+U).normalize(),Q[2].setComponents(H+Y,q+G,D+M,B+C).normalize(),Q[3].setComponents(H-Y,q-G,D-M,B-C).normalize(),N)Q[4].setComponents(K,E,_,L).normalize(),Q[5].setComponents(H-K,q-E,D-_,B-L).normalize();else if(Q[4].setComponents(H-K,q-E,D-_,B-L).normalize(),$===2000)Q[5].setComponents(H+K,q+E,D+_,B+L).normalize();else if($===2001)Q[5].setComponents(K,E,_,L).normalize();else throw Error("THREE.Frustum.setFromProjectionMatrix(): Invalid coordinate system: "+$);return this}intersectsObject(J){if(J.boundingSphere!==void 0){if(J.boundingSphere===null)J.computeBoundingSphere();g9.copy(J.boundingSphere).applyMatrix4(J.matrixWorld)}else{let $=J.geometry;if($.boundingSphere===null)$.computeBoundingSphere();g9.copy($.boundingSphere).applyMatrix4(J.matrixWorld)}return this.intersectsSphere(g9)}intersectsSprite(J){g9.center.set(0,0,0);let $=jW.distanceTo(J.center);return g9.radius=0.7071067811865476+$,g9.applyMatrix4(J.matrixWorld),this.intersectsSphere(g9)}intersectsSphere(J){let $=this.planes,N=J.center,Q=-J.radius;for(let Z=0;Z<6;Z++)if($[Z].distanceToPoint(N)<Q)return!1;return!0}intersectsBox(J){let $=this.planes;for(let N=0;N<6;N++){let Q=$[N];if(V6.x=Q.normal.x>0?J.max.x:J.min.x,V6.y=Q.normal.y>0?J.max.y:J.min.y,V6.z=Q.normal.z>0?J.max.z:J.min.z,Q.distanceToPoint(V6)<0)return!1}return!0}containsPoint(J){let $=this.planes;for(let N=0;N<6;N++)if($[N].distanceToPoint(J)<0)return!1;return!0}clone(){return new this.constructor().copy(this)}}class eJ extends y9{constructor(J){super();this.isLineBasicMaterial=!0,this.type="LineBasicMaterial",this.color=new i0(16777215),this.map=null,this.linewidth=1,this.linecap="round",this.linejoin="round",this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.color.copy(J.color),this.map=J.map,this.linewidth=J.linewidth,this.linecap=J.linecap,this.linejoin=J.linejoin,this.fog=J.fog,this}}var C6=new h,I6=new h,dN=new U8,hJ=new m9,w6=new BJ,h7=new h,lN=new h;class o6 extends F8{constructor(J=new u8,$=new eJ){super();this.isLine=!0,this.type="Line",this.geometry=J,this.material=$,this.morphTargetDictionary=void 0,this.morphTargetInfluences=void 0,this.updateMorphTargets()}copy(J,$){return super.copy(J,$),this.material=Array.isArray(J.material)?J.material.slice():J.material,this.geometry=J.geometry,this}computeLineDistances(){let J=this.geometry;if(J.index===null){let $=J.attributes.position,N=[0];for(let Q=1,Z=$.count;Q<Z;Q++)C6.fromBufferAttribute($,Q-1),I6.fromBufferAttribute($,Q),N[Q]=N[Q-1],N[Q]+=C6.distanceTo(I6);J.setAttribute("lineDistance",new B8(N,1))}else console.warn("THREE.Line.computeLineDistances(): Computation only possible with non-indexed BufferGeometry.");return this}raycast(J,$){let N=this.geometry,Q=this.matrixWorld,Z=J.params.Line.threshold,W=N.drawRange;if(N.boundingSphere===null)N.computeBoundingSphere();if(w6.copy(N.boundingSphere),w6.applyMatrix4(Q),w6.radius+=Z,J.ray.intersectsSphere(w6)===!1)return;dN.copy(Q).invert(),hJ.copy(J.ray).applyMatrix4(dN);let Y=Z/((this.scale.x+this.scale.y+this.scale.z)/3),K=Y*Y,H=this.isLineSegments?2:1,X=N.index,E=N.attributes.position;if(X!==null){let q=Math.max(0,W.start),k=Math.min(X.count,W.start+W.count);for(let M=q,_=k-1;M<_;M+=H){let D=X.getX(M),U=X.getX(M+1),C=z6(this,J,hJ,K,D,U,M);if(C)$.push(C)}if(this.isLineLoop){let M=X.getX(k-1),_=X.getX(q),D=z6(this,J,hJ,K,M,_,k-1);if(D)$.push(D)}}else{let q=Math.max(0,W.start),k=Math.min(E.count,W.start+W.count);for(let M=q,_=k-1;M<_;M+=H){let D=z6(this,J,hJ,K,M,M+1,M);if(D)$.push(D)}if(this.isLineLoop){let M=z6(this,J,hJ,K,k-1,q,k-1);if(M)$.push(M)}}}updateMorphTargets(){let $=this.geometry.morphAttributes,N=Object.keys($);if(N.length>0){let Q=$[N[0]];if(Q!==void 0){this.morphTargetInfluences=[],this.morphTargetDictionary={};for(let Z=0,W=Q.length;Z<W;Z++){let Y=Q[Z].name||String(Z);this.morphTargetInfluences.push(0),this.morphTargetDictionary[Y]=Z}}}}}function z6(J,$,N,Q,Z,W,Y){let K=J.geometry.attributes.position;if(C6.fromBufferAttribute(K,Z),I6.fromBufferAttribute(K,W),N.distanceSqToSegment(C6,I6,h7,lN)>Q)return;h7.applyMatrix4(J.matrixWorld);let X=$.ray.origin.distanceTo(h7);if(X<$.near||X>$.far)return;return{distance:X,point:lN.clone().applyMatrix4(J.matrixWorld),index:Y,face:null,faceIndex:null,barycoord:null,object:J}}class a6 extends S8{constructor(J,$,N=1014,Q,Z,W,Y=1003,K=1003,H,X=1026,G=1){if(X!==1026&&X!==1027)throw Error("DepthTexture format must be either THREE.DepthFormat or THREE.DepthStencilFormat");let E={width:J,height:$,depth:G};super(E,Q,Z,W,Y,K,X,N,H);this.isDepthTexture=!0,this.flipY=!1,this.generateMipmaps=!1,this.compareFunction=null}copy(J){return super.copy(J),this.source=new oJ(Object.assign({},J.image)),this.compareFunction=J.compareFunction,this}toJSON(J){let $=super.toJSON(J);if(this.compareFunction!==null)$.compareFunction=this.compareFunction;return $}}class r6 extends S8{constructor(J=null){super();this.sourceTexture=J,this.isExternalTexture=!0}copy(J){return super.copy(J),this.sourceTexture=J.sourceTexture,this}}class J6 extends u8{constructor(J=1,$=1,N=1,Q=1){super();this.type="PlaneGeometry",this.parameters={width:J,height:$,widthSegments:N,heightSegments:Q};let Z=J/2,W=$/2,Y=Math.floor(N),K=Math.floor(Q),H=Y+1,X=K+1,G=J/Y,E=$/K,q=[],k=[],M=[],_=[];for(let D=0;D<X;D++){let U=D*E-W;for(let C=0;C<H;C++){let L=C*G-Z;k.push(L,-U,0),M.push(0,0,1),_.push(C/Y),_.push(1-D/K)}}for(let D=0;D<K;D++)for(let U=0;U<Y;U++){let C=U+H*D,L=U+H*(D+1),B=U+1+H*(D+1),y=U+1+H*D;q.push(C,L,y),q.push(L,B,y)}this.setIndex(q),this.setAttribute("position",new B8(k,3)),this.setAttribute("normal",new B8(M,3)),this.setAttribute("uv",new B8(_,2))}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new J6(J.width,J.height,J.widthSegments,J.heightSegments)}}class IJ extends u8{constructor(J=1,$=32,N=16,Q=0,Z=Math.PI*2,W=0,Y=Math.PI){super();this.type="SphereGeometry",this.parameters={radius:J,widthSegments:$,heightSegments:N,phiStart:Q,phiLength:Z,thetaStart:W,thetaLength:Y},$=Math.max(3,Math.floor($)),N=Math.max(2,Math.floor(N));let K=Math.min(W+Y,Math.PI),H=0,X=[],G=new h,E=new h,q=[],k=[],M=[],_=[];for(let D=0;D<=N;D++){let U=[],C=D/N,L=0;if(D===0&&W===0)L=0.5/$;else if(D===N&&K===Math.PI)L=-0.5/$;for(let B=0;B<=$;B++){let y=B/$;G.x=-J*Math.cos(Q+y*Z)*Math.sin(W+C*Y),G.y=J*Math.cos(W+C*Y),G.z=J*Math.sin(Q+y*Z)*Math.sin(W+C*Y),k.push(G.x,G.y,G.z),E.copy(G).normalize(),M.push(E.x,E.y,E.z),_.push(y+L,1-C),U.push(H++)}X.push(U)}for(let D=0;D<N;D++)for(let U=0;U<$;U++){let C=X[D][U+1],L=X[D][U],B=X[D+1][U],y=X[D+1][U+1];if(D!==0||W>0)q.push(C,L,y);if(D!==N-1||K<Math.PI)q.push(L,B,y)}this.setIndex(q),this.setAttribute("position",new B8(k,3)),this.setAttribute("normal",new B8(M,3)),this.setAttribute("uv",new B8(_,2))}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new IJ(J.radius,J.widthSegments,J.heightSegments,J.phiStart,J.phiLength,J.thetaStart,J.thetaLength)}}class t6 extends y9{constructor(J){super();this.isMeshStandardMaterial=!0,this.type="MeshStandardMaterial",this.defines={STANDARD:""},this.color=new i0(16777215),this.roughness=1,this.metalness=0,this.map=null,this.lightMap=null,this.lightMapIntensity=1,this.aoMap=null,this.aoMapIntensity=1,this.emissive=new i0(0),this.emissiveIntensity=1,this.emissiveMap=null,this.bumpMap=null,this.bumpScale=1,this.normalMap=null,this.normalMapType=0,this.normalScale=new p0(1,1),this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.roughnessMap=null,this.metalnessMap=null,this.alphaMap=null,this.envMap=null,this.envMapRotation=new $9,this.envMapIntensity=1,this.wireframe=!1,this.wireframeLinewidth=1,this.wireframeLinecap="round",this.wireframeLinejoin="round",this.flatShading=!1,this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.defines={STANDARD:""},this.color.copy(J.color),this.roughness=J.roughness,this.metalness=J.metalness,this.map=J.map,this.lightMap=J.lightMap,this.lightMapIntensity=J.lightMapIntensity,this.aoMap=J.aoMap,this.aoMapIntensity=J.aoMapIntensity,this.emissive.copy(J.emissive),this.emissiveMap=J.emissiveMap,this.emissiveIntensity=J.emissiveIntensity,this.bumpMap=J.bumpMap,this.bumpScale=J.bumpScale,this.normalMap=J.normalMap,this.normalMapType=J.normalMapType,this.normalScale.copy(J.normalScale),this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this.roughnessMap=J.roughnessMap,this.metalnessMap=J.metalnessMap,this.alphaMap=J.alphaMap,this.envMap=J.envMap,this.envMapRotation.copy(J.envMapRotation),this.envMapIntensity=J.envMapIntensity,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.wireframeLinecap=J.wireframeLinecap,this.wireframeLinejoin=J.wireframeLinejoin,this.flatShading=J.flatShading,this.fog=J.fog,this}}class x$ extends y9{constructor(J){super();this.isMeshDepthMaterial=!0,this.type="MeshDepthMaterial",this.depthPacking=3200,this.map=null,this.alphaMap=null,this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.wireframe=!1,this.wireframeLinewidth=1,this.setValues(J)}copy(J){return super.copy(J),this.depthPacking=J.depthPacking,this.map=J.map,this.alphaMap=J.alphaMap,this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this}}class g$ extends y9{constructor(J){super();this.isMeshDistanceMaterial=!0,this.type="MeshDistanceMaterial",this.map=null,this.alphaMap=null,this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.setValues(J)}copy(J){return super.copy(J),this.map=J.map,this.alphaMap=J.alphaMap,this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this}}function B6(J,$){if(!J||J.constructor===$)return J;if(typeof $.BYTES_PER_ELEMENT==="number")return new $(J);return Array.prototype.slice.call(J)}function fW(J){return ArrayBuffer.isView(J)&&!(J instanceof DataView)}class PJ{constructor(J,$,N,Q){this.parameterPositions=J,this._cachedIndex=0,this.resultBuffer=Q!==void 0?Q:new $.constructor(N),this.sampleValues=$,this.valueSize=N,this.settings=null,this.DefaultSettings_={}}evaluate(J){let $=this.parameterPositions,N=this._cachedIndex,Q=$[N],Z=$[N-1];N:{J:{let W;$:{Q:if(!(J<Q)){for(let Y=N+2;;){if(Q===void 0){if(J<Z)break Q;return N=$.length,this._cachedIndex=N,this.copySampleValue_(N-1)}if(N===Y)break;if(Z=Q,Q=$[++N],J<Q)break J}W=$.length;break $}if(!(J>=Z)){let Y=$[1];if(J<Y)N=2,Z=Y;for(let K=N-2;;){if(Z===void 0)return this._cachedIndex=0,this.copySampleValue_(0);if(N===K)break;if(Q=Z,Z=$[--N-1],J>=Z)break J}W=N,N=0;break $}break N}while(N<W){let Y=N+W>>>1;if(J<$[Y])W=Y;else N=Y+1}if(Q=$[N],Z=$[N-1],Z===void 0)return this._cachedIndex=0,this.copySampleValue_(0);if(Q===void 0)return N=$.length,this._cachedIndex=N,this.copySampleValue_(N-1)}this._cachedIndex=N,this.intervalChanged_(N,Z,Q)}return this.interpolate_(N,Z,J,Q)}getSettings_(){return this.settings||this.DefaultSettings_}copySampleValue_(J){let $=this.resultBuffer,N=this.sampleValues,Q=this.valueSize,Z=J*Q;for(let W=0;W!==Q;++W)$[W]=N[Z+W];return $}interpolate_(){throw Error("call to abstract method")}intervalChanged_(){}}class p$ extends PJ{constructor(J,$,N,Q){super(J,$,N,Q);this._weightPrev=-0,this._offsetPrev=-0,this._weightNext=-0,this._offsetNext=-0,this.DefaultSettings_={endingStart:2400,endingEnd:2400}}intervalChanged_(J,$,N){let Q=this.parameterPositions,Z=J-2,W=J+1,Y=Q[Z],K=Q[W];if(Y===void 0)switch(this.getSettings_().endingStart){case 2401:Z=J,Y=2*$-N;break;case 2402:Z=Q.length-2,Y=$+Q[Z]-Q[Z+1];break;default:Z=J,Y=N}if(K===void 0)switch(this.getSettings_().endingEnd){case 2401:W=J,K=2*N-$;break;case 2402:W=1,K=N+Q[1]-Q[0];break;default:W=J-1,K=$}let H=(N-$)*0.5,X=this.valueSize;this._weightPrev=H/($-Y),this._weightNext=H/(K-N),this._offsetPrev=Z*X,this._offsetNext=W*X}interpolate_(J,$,N,Q){let Z=this.resultBuffer,W=this.sampleValues,Y=this.valueSize,K=J*Y,H=K-Y,X=this._offsetPrev,G=this._offsetNext,E=this._weightPrev,q=this._weightNext,k=(N-$)/(Q-$),M=k*k,_=M*k,D=-E*_+2*E*M-E*k,U=(1+E)*_+(-1.5-2*E)*M+(-0.5+E)*k+1,C=(-1-q)*_+(1.5+q)*M+0.5*k,L=q*_-q*M;for(let B=0;B!==Y;++B)Z[B]=D*W[X+B]+U*W[H+B]+C*W[K+B]+L*W[G+B];return Z}}class d$ extends PJ{constructor(J,$,N,Q){super(J,$,N,Q)}interpolate_(J,$,N,Q){let Z=this.resultBuffer,W=this.sampleValues,Y=this.valueSize,K=J*Y,H=K-Y,X=(N-$)/(Q-$),G=1-X;for(let E=0;E!==Y;++E)Z[E]=W[H+E]*G+W[K+E]*X;return Z}}class l$ extends PJ{constructor(J,$,N,Q){super(J,$,N,Q)}interpolate_(J){return this.copySampleValue_(J-1)}}class r8{constructor(J,$,N,Q){if(J===void 0)throw Error("THREE.KeyframeTrack: track name is undefined");if($===void 0||$.length===0)throw Error("THREE.KeyframeTrack: no keyframes in track named "+J);this.name=J,this.times=B6($,this.TimeBufferType),this.values=B6(N,this.ValueBufferType),this.setInterpolation(Q||this.DefaultInterpolation)}static toJSON(J){let $=J.constructor,N;if($.toJSON!==this.toJSON)N=$.toJSON(J);else{N={name:J.name,times:B6(J.times,Array),values:B6(J.values,Array)};let Q=J.getInterpolation();if(Q!==J.DefaultInterpolation)N.interpolation=Q}return N.type=J.ValueTypeName,N}InterpolantFactoryMethodDiscrete(J){return new l$(this.times,this.values,this.getValueSize(),J)}InterpolantFactoryMethodLinear(J){return new d$(this.times,this.values,this.getValueSize(),J)}InterpolantFactoryMethodSmooth(J){return new p$(this.times,this.values,this.getValueSize(),J)}setInterpolation(J){let $;switch(J){case 2300:$=this.InterpolantFactoryMethodDiscrete;break;case 2301:$=this.InterpolantFactoryMethodLinear;break;case 2302:$=this.InterpolantFactoryMethodSmooth;break}if($===void 0){let N="unsupported interpolation for "+this.ValueTypeName+" keyframe track named "+this.name;if(this.createInterpolant===void 0)if(J!==this.DefaultInterpolation)this.setInterpolation(this.DefaultInterpolation);else throw Error(N);return console.warn("THREE.KeyframeTrack:",N),this}return this.createInterpolant=$,this}getInterpolation(){switch(this.createInterpolant){case this.InterpolantFactoryMethodDiscrete:return 2300;case this.InterpolantFactoryMethodLinear:return 2301;case this.InterpolantFactoryMethodSmooth:return 2302}}getValueSize(){return this.values.length/this.times.length}shift(J){if(J!==0){let $=this.times;for(let N=0,Q=$.length;N!==Q;++N)$[N]+=J}return this}scale(J){if(J!==1){let $=this.times;for(let N=0,Q=$.length;N!==Q;++N)$[N]*=J}return this}trim(J,$){let N=this.times,Q=N.length,Z=0,W=Q-1;while(Z!==Q&&N[Z]<J)++Z;while(W!==-1&&N[W]>$)--W;if(++W,Z!==0||W!==Q){if(Z>=W)W=Math.max(W,1),Z=W-1;let Y=this.getValueSize();this.times=N.slice(Z,W),this.values=this.values.slice(Z*Y,W*Y)}return this}validate(){let J=!0,$=this.getValueSize();if($-Math.floor($)!==0)console.error("THREE.KeyframeTrack: Invalid value size in track.",this),J=!1;let N=this.times,Q=this.values,Z=N.length;if(Z===0)console.error("THREE.KeyframeTrack: Track is empty.",this),J=!1;let W=null;for(let Y=0;Y!==Z;Y++){let K=N[Y];if(typeof K==="number"&&isNaN(K)){console.error("THREE.KeyframeTrack: Time is not a valid number.",this,Y,K),J=!1;break}if(W!==null&&W>K){console.error("THREE.KeyframeTrack: Out of order keys.",this,Y,K,W),J=!1;break}W=K}if(Q!==void 0){if(fW(Q))for(let Y=0,K=Q.length;Y!==K;++Y){let H=Q[Y];if(isNaN(H)){console.error("THREE.KeyframeTrack: Value is not a valid number.",this,Y,H),J=!1;break}}}return J}optimize(){let J=this.times.slice(),$=this.values.slice(),N=this.getValueSize(),Q=this.getInterpolation()===2302,Z=J.length-1,W=1;for(let Y=1;Y<Z;++Y){let K=!1,H=J[Y],X=J[Y+1];if(H!==X&&(Y!==1||H!==J[0]))if(!Q){let G=Y*N,E=G-N,q=G+N;for(let k=0;k!==N;++k){let M=$[G+k];if(M!==$[E+k]||M!==$[q+k]){K=!0;break}}}else K=!0;if(K){if(Y!==W){J[W]=J[Y];let G=Y*N,E=W*N;for(let q=0;q!==N;++q)$[E+q]=$[G+q]}++W}}if(Z>0){J[W]=J[Z];for(let Y=Z*N,K=W*N,H=0;H!==N;++H)$[K+H]=$[Y+H];++W}if(W!==J.length)this.times=J.slice(0,W),this.values=$.slice(0,W*N);else this.times=J,this.values=$;return this}clone(){let J=this.times.slice(),$=this.values.slice(),Q=new this.constructor(this.name,J,$);return Q.createInterpolant=this.createInterpolant,Q}}r8.prototype.ValueTypeName="";r8.prototype.TimeBufferType=Float32Array;r8.prototype.ValueBufferType=Float32Array;r8.prototype.DefaultInterpolation=2301;class n9 extends r8{constructor(J,$,N){super(J,$,N)}}n9.prototype.ValueTypeName="bool";n9.prototype.ValueBufferType=Array;n9.prototype.DefaultInterpolation=2300;n9.prototype.InterpolantFactoryMethodLinear=void 0;n9.prototype.InterpolantFactoryMethodSmooth=void 0;class m$ extends r8{constructor(J,$,N,Q){super(J,$,N,Q)}}m$.prototype.ValueTypeName="color";class u$ extends r8{constructor(J,$,N,Q){super(J,$,N,Q)}}u$.prototype.ValueTypeName="number";class c$ extends PJ{constructor(J,$,N,Q){super(J,$,N,Q)}interpolate_(J,$,N,Q){let Z=this.resultBuffer,W=this.sampleValues,Y=this.valueSize,K=(N-$)/(Q-$),H=J*Y;for(let X=H+Y;H!==X;H+=4)N9.slerpFlat(Z,0,W,H-Y,W,H,K);return Z}}class e6 extends r8{constructor(J,$,N,Q){super(J,$,N,Q)}InterpolantFactoryMethodLinear(J){return new c$(this.times,this.values,this.getValueSize(),J)}}e6.prototype.ValueTypeName="quaternion";e6.prototype.InterpolantFactoryMethodSmooth=void 0;class s9 extends r8{constructor(J,$,N){super(J,$,N)}}s9.prototype.ValueTypeName="string";s9.prototype.ValueBufferType=Array;s9.prototype.DefaultInterpolation=2300;s9.prototype.InterpolantFactoryMethodLinear=void 0;s9.prototype.InterpolantFactoryMethodSmooth=void 0;class n$ extends r8{constructor(J,$,N,Q){super(J,$,N,Q)}}n$.prototype.ValueTypeName="vector";class s${constructor(J,$,N){let Q=this,Z=!1,W=0,Y=0,K=void 0,H=[];this.onStart=void 0,this.onLoad=J,this.onProgress=$,this.onError=N,this.abortController=new AbortController,this.itemStart=function(X){if(Y++,Z===!1){if(Q.onStart!==void 0)Q.onStart(X,W,Y)}Z=!0},this.itemEnd=function(X){if(W++,Q.onProgress!==void 0)Q.onProgress(X,W,Y);if(W===Y){if(Z=!1,Q.onLoad!==void 0)Q.onLoad()}},this.itemError=function(X){if(Q.onError!==void 0)Q.onError(X)},this.resolveURL=function(X){if(K)return K(X);return X},this.setURLModifier=function(X){return K=X,this},this.addHandler=function(X,G){return H.push(X,G),this},this.removeHandler=function(X){let G=H.indexOf(X);if(G!==-1)H.splice(G,2);return this},this.getHandler=function(X){for(let G=0,E=H.length;G<E;G+=2){let q=H[G],k=H[G+1];if(q.global)q.lastIndex=0;if(q.test(X))return k}return null},this.abort=function(){return this.abortController.abort(),this.abortController=new AbortController,this}}}var JZ=new s$;class i${constructor(J){this.manager=J!==void 0?J:JZ,this.crossOrigin="anonymous",this.withCredentials=!1,this.path="",this.resourcePath="",this.requestHeader={}}load(){}loadAsync(J,$){let N=this;return new Promise(function(Q,Z){N.load(J,Q,$,Z)})}parse(){}setCrossOrigin(J){return this.crossOrigin=J,this}setWithCredentials(J){return this.withCredentials=J,this}setPath(J){return this.path=J,this}setResourcePath(J){return this.resourcePath=J,this}setRequestHeader(J){return this.requestHeader=J,this}abort(){return this}}i$.DEFAULT_MATERIAL_NAME="__DEFAULT";class J7 extends F8{constructor(J,$=1){super();this.isLight=!0,this.type="Light",this.color=new i0(J),this.intensity=$}dispose(){}copy(J,$){return super.copy(J,$),this.color.copy(J.color),this.intensity=J.intensity,this}toJSON(J){let $=super.toJSON(J);if($.object.color=this.color.getHex(),$.object.intensity=this.intensity,this.groundColor!==void 0)$.object.groundColor=this.groundColor.getHex();if(this.distance!==void 0)$.object.distance=this.distance;if(this.angle!==void 0)$.object.angle=this.angle;if(this.decay!==void 0)$.object.decay=this.decay;if(this.penumbra!==void 0)$.object.penumbra=this.penumbra;if(this.shadow!==void 0)$.object.shadow=this.shadow.toJSON();if(this.target!==void 0)$.object.target=this.target.uuid;return $}}class $7 extends J7{constructor(J,$,N){super(J,N);this.isHemisphereLight=!0,this.type="HemisphereLight",this.position.copy(F8.DEFAULT_UP),this.updateMatrix(),this.groundColor=new i0($)}copy(J,$){return super.copy(J,$),this.groundColor.copy(J.groundColor),this}}var x7=new U8,mN=new h,uN=new h;class $Z{constructor(J){this.camera=J,this.intensity=1,this.bias=0,this.normalBias=0,this.radius=1,this.blurSamples=8,this.mapSize=new p0(512,512),this.mapType=1009,this.map=null,this.mapPass=null,this.matrix=new U8,this.autoUpdate=!0,this.needsUpdate=!1,this._frustum=new tJ,this._frameExtents=new p0(1,1),this._viewportCount=1,this._viewports=[new D8(0,0,1,1)]}getViewportCount(){return this._viewportCount}getFrustum(){return this._frustum}updateMatrices(J){let $=this.camera,N=this.matrix;if(mN.setFromMatrixPosition(J.matrixWorld),$.position.copy(mN),uN.setFromMatrixPosition(J.target.matrixWorld),$.lookAt(uN),$.updateMatrixWorld(),x7.multiplyMatrices($.projectionMatrix,$.matrixWorldInverse),this._frustum.setFromProjectionMatrix(x7,$.coordinateSystem,$.reversedDepth),$.reversedDepth)N.set(0.5,0,0,0.5,0,0.5,0,0.5,0,0,1,0,0,0,0,1);else N.set(0.5,0,0,0.5,0,0.5,0,0.5,0,0,0.5,0.5,0,0,0,1);N.multiply(x7)}getViewport(J){return this._viewports[J]}getFrameExtents(){return this._frameExtents}dispose(){if(this.map)this.map.dispose();if(this.mapPass)this.mapPass.dispose()}copy(J){return this.camera=J.camera.clone(),this.intensity=J.intensity,this.bias=J.bias,this.radius=J.radius,this.autoUpdate=J.autoUpdate,this.needsUpdate=J.needsUpdate,this.normalBias=J.normalBias,this.blurSamples=J.blurSamples,this.mapSize.copy(J.mapSize),this}clone(){return new this.constructor().copy(this)}toJSON(){let J={};if(this.intensity!==1)J.intensity=this.intensity;if(this.bias!==0)J.bias=this.bias;if(this.normalBias!==0)J.normalBias=this.normalBias;if(this.radius!==1)J.radius=this.radius;if(this.mapSize.x!==512||this.mapSize.y!==512)J.mapSize=this.mapSize.toArray();return J.camera=this.camera.toJSON(!1).object,delete J.camera.matrix,J}}class N7 extends n6{constructor(J=-1,$=1,N=1,Q=-1,Z=0.1,W=2000){super();this.isOrthographicCamera=!0,this.type="OrthographicCamera",this.zoom=1,this.view=null,this.left=J,this.right=$,this.top=N,this.bottom=Q,this.near=Z,this.far=W,this.updateProjectionMatrix()}copy(J,$){return super.copy(J,$),this.left=J.left,this.right=J.right,this.top=J.top,this.bottom=J.bottom,this.near=J.near,this.far=J.far,this.zoom=J.zoom,this.view=J.view===null?null:Object.assign({},J.view),this}setViewOffset(J,$,N,Q,Z,W){if(this.view===null)this.view={enabled:!0,fullWidth:1,fullHeight:1,offsetX:0,offsetY:0,width:1,height:1};this.view.enabled=!0,this.view.fullWidth=J,this.view.fullHeight=$,this.view.offsetX=N,this.view.offsetY=Q,this.view.width=Z,this.view.height=W,this.updateProjectionMatrix()}clearViewOffset(){if(this.view!==null)this.view.enabled=!1;this.updateProjectionMatrix()}updateProjectionMatrix(){let J=(this.right-this.left)/(2*this.zoom),$=(this.top-this.bottom)/(2*this.zoom),N=(this.right+this.left)/2,Q=(this.top+this.bottom)/2,Z=N-J,W=N+J,Y=Q+$,K=Q-$;if(this.view!==null&&this.view.enabled){let H=(this.right-this.left)/this.view.fullWidth/this.zoom,X=(this.top-this.bottom)/this.view.fullHeight/this.zoom;Z+=H*this.view.offsetX,W=Z+H*this.view.width,Y-=X*this.view.offsetY,K=Y-X*this.view.height}this.projectionMatrix.makeOrthographic(Z,W,Y,K,this.near,this.far,this.coordinateSystem,this.reversedDepth),this.projectionMatrixInverse.copy(this.projectionMatrix).invert()}toJSON(J){let $=super.toJSON(J);if($.object.zoom=this.zoom,$.object.left=this.left,$.object.right=this.right,$.object.top=this.top,$.object.bottom=this.bottom,$.object.near=this.near,$.object.far=this.far,this.view!==null)$.object.view=Object.assign({},this.view);return $}}class NZ extends $Z{constructor(){super(new N7(-5,5,5,-5,0.5,500));this.isDirectionalLightShadow=!0}}class Q7 extends J7{constructor(J,$){super(J,$);this.isDirectionalLight=!0,this.type="DirectionalLight",this.position.copy(F8.DEFAULT_UP),this.updateMatrix(),this.target=new F8,this.shadow=new NZ}dispose(){this.shadow.dispose()}copy(J){return super.copy(J),this.target=J.target.clone(),this.shadow=J.shadow.clone(),this}}class o$ extends T8{constructor(J=[]){super();this.isArrayCamera=!0,this.isMultiViewCamera=!1,this.cameras=J}}var a$="\\[\\]\\.:\\/",vW=new RegExp("["+a$+"]","g"),r$="[^"+a$+"]",hW="[^"+a$.replace("\\.","")+"]",xW=/((?:WC+[\/:])*)/.source.replace("WC",r$),gW=/(WCOD+)?/.source.replace("WCOD",hW),pW=/(?:\.(WC+)(?:\[(.+)\])?)?/.source.replace("WC",r$),dW=/\.(WC+)(?:\[(.+)\])?/.source.replace("WC",r$),lW=new RegExp("^"+xW+gW+pW+dW+"$"),mW=["material","materials","bones","map"];class QZ{constructor(J,$,N){let Q=N||$8.parseTrackName($);this._targetGroup=J,this._bindings=J.subscribe_($,Q)}getValue(J,$){this.bind();let N=this._targetGroup.nCachedObjects_,Q=this._bindings[N];if(Q!==void 0)Q.getValue(J,$)}setValue(J,$){let N=this._bindings;for(let Q=this._targetGroup.nCachedObjects_,Z=N.length;Q!==Z;++Q)N[Q].setValue(J,$)}bind(){let J=this._bindings;for(let $=this._targetGroup.nCachedObjects_,N=J.length;$!==N;++$)J[$].bind()}unbind(){let J=this._bindings;for(let $=this._targetGroup.nCachedObjects_,N=J.length;$!==N;++$)J[$].unbind()}}class $8{constructor(J,$,N){this.path=$,this.parsedPath=N||$8.parseTrackName($),this.node=$8.findNode(J,this.parsedPath.nodeName),this.rootNode=J,this.getValue=this._getValue_unbound,this.setValue=this._setValue_unbound}static create(J,$,N){if(!(J&&J.isAnimationObjectGroup))return new $8(J,$,N);else return new $8.Composite(J,$,N)}static sanitizeNodeName(J){return J.replace(/\s/g,"_").replace(vW,"")}static parseTrackName(J){let $=lW.exec(J);if($===null)throw Error("PropertyBinding: Cannot parse trackName: "+J);let N={nodeName:$[2],objectName:$[3],objectIndex:$[4],propertyName:$[5],propertyIndex:$[6]},Q=N.nodeName&&N.nodeName.lastIndexOf(".");if(Q!==void 0&&Q!==-1){let Z=N.nodeName.substring(Q+1);if(mW.indexOf(Z)!==-1)N.nodeName=N.nodeName.substring(0,Q),N.objectName=Z}if(N.propertyName===null||N.propertyName.length===0)throw Error("PropertyBinding: can not parse propertyName from trackName: "+J);return N}static findNode(J,$){if($===void 0||$===""||$==="."||$===-1||$===J.name||$===J.uuid)return J;if(J.skeleton){let N=J.skeleton.getBoneByName($);if(N!==void 0)return N}if(J.children){let N=function(Z){for(let W=0;W<Z.length;W++){let Y=Z[W];if(Y.name===$||Y.uuid===$)return Y;let K=N(Y.children);if(K)return K}return null},Q=N(J.children);if(Q)return Q}return null}_getValue_unavailable(){}_setValue_unavailable(){}_getValue_direct(J,$){J[$]=this.targetObject[this.propertyName]}_getValue_array(J,$){let N=this.resolvedProperty;for(let Q=0,Z=N.length;Q!==Z;++Q)J[$++]=N[Q]}_getValue_arrayElement(J,$){J[$]=this.resolvedProperty[this.propertyIndex]}_getValue_toArray(J,$){this.resolvedProperty.toArray(J,$)}_setValue_direct(J,$){this.targetObject[this.propertyName]=J[$]}_setValue_direct_setNeedsUpdate(J,$){this.targetObject[this.propertyName]=J[$],this.targetObject.needsUpdate=!0}_setValue_direct_setMatrixWorldNeedsUpdate(J,$){this.targetObject[this.propertyName]=J[$],this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_array(J,$){let N=this.resolvedProperty;for(let Q=0,Z=N.length;Q!==Z;++Q)N[Q]=J[$++]}_setValue_array_setNeedsUpdate(J,$){let N=this.resolvedProperty;for(let Q=0,Z=N.length;Q!==Z;++Q)N[Q]=J[$++];this.targetObject.needsUpdate=!0}_setValue_array_setMatrixWorldNeedsUpdate(J,$){let N=this.resolvedProperty;for(let Q=0,Z=N.length;Q!==Z;++Q)N[Q]=J[$++];this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_arrayElement(J,$){this.resolvedProperty[this.propertyIndex]=J[$]}_setValue_arrayElement_setNeedsUpdate(J,$){this.resolvedProperty[this.propertyIndex]=J[$],this.targetObject.needsUpdate=!0}_setValue_arrayElement_setMatrixWorldNeedsUpdate(J,$){this.resolvedProperty[this.propertyIndex]=J[$],this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_fromArray(J,$){this.resolvedProperty.fromArray(J,$)}_setValue_fromArray_setNeedsUpdate(J,$){this.resolvedProperty.fromArray(J,$),this.targetObject.needsUpdate=!0}_setValue_fromArray_setMatrixWorldNeedsUpdate(J,$){this.resolvedProperty.fromArray(J,$),this.targetObject.matrixWorldNeedsUpdate=!0}_getValue_unbound(J,$){this.bind(),this.getValue(J,$)}_setValue_unbound(J,$){this.bind(),this.setValue(J,$)}bind(){let J=this.node,$=this.parsedPath,N=$.objectName,Q=$.propertyName,Z=$.propertyIndex;if(!J)J=$8.findNode(this.rootNode,$.nodeName),this.node=J;if(this.getValue=this._getValue_unavailable,this.setValue=this._setValue_unavailable,!J){console.warn("THREE.PropertyBinding: No target node found for track: "+this.path+".");return}if(N){let H=$.objectIndex;switch(N){case"materials":if(!J.material){console.error("THREE.PropertyBinding: Can not bind to material as node does not have a material.",this);return}if(!J.material.materials){console.error("THREE.PropertyBinding: Can not bind to material.materials as node.material does not have a materials array.",this);return}J=J.material.materials;break;case"bones":if(!J.skeleton){console.error("THREE.PropertyBinding: Can not bind to bones as node does not have a skeleton.",this);return}J=J.skeleton.bones;for(let X=0;X<J.length;X++)if(J[X].name===H){H=X;break}break;case"map":if("map"in J){J=J.map;break}if(!J.material){console.error("THREE.PropertyBinding: Can not bind to material as node does not have a material.",this);return}if(!J.material.map){console.error("THREE.PropertyBinding: Can not bind to material.map as node.material does not have a map.",this);return}J=J.material.map;break;default:if(J[N]===void 0){console.error("THREE.PropertyBinding: Can not bind to objectName of node undefined.",this);return}J=J[N]}if(H!==void 0){if(J[H]===void 0){console.error("THREE.PropertyBinding: Trying to bind to objectIndex of objectName, but is undefined.",this,J);return}J=J[H]}}let W=J[Q];if(W===void 0){let H=$.nodeName;console.error("THREE.PropertyBinding: Trying to update property for track: "+H+"."+Q+" but it wasn't found.",J);return}let Y=this.Versioning.None;if(this.targetObject=J,J.isMaterial===!0)Y=this.Versioning.NeedsUpdate;else if(J.isObject3D===!0)Y=this.Versioning.MatrixWorldNeedsUpdate;let K=this.BindingType.Direct;if(Z!==void 0){if(Q==="morphTargetInfluences"){if(!J.geometry){console.error("THREE.PropertyBinding: Can not bind to morphTargetInfluences because node does not have a geometry.",this);return}if(!J.geometry.morphAttributes){console.error("THREE.PropertyBinding: Can not bind to morphTargetInfluences because node does not have a geometry.morphAttributes.",this);return}if(J.morphTargetDictionary[Z]!==void 0)Z=J.morphTargetDictionary[Z]}K=this.BindingType.ArrayElement,this.resolvedProperty=W,this.propertyIndex=Z}else if(W.fromArray!==void 0&&W.toArray!==void 0)K=this.BindingType.HasFromToArray,this.resolvedProperty=W;else if(Array.isArray(W))K=this.BindingType.EntireArray,this.resolvedProperty=W;else this.propertyName=Q;this.getValue=this.GetterByBindingType[K],this.setValue=this.SetterByBindingTypeAndVersioning[K][Y]}unbind(){this.node=null,this.getValue=this._getValue_unbound,this.setValue=this._setValue_unbound}}$8.Composite=QZ;$8.prototype.BindingType={Direct:0,EntireArray:1,ArrayElement:2,HasFromToArray:3};$8.prototype.Versioning={None:0,NeedsUpdate:1,MatrixWorldNeedsUpdate:2};$8.prototype.GetterByBindingType=[$8.prototype._getValue_direct,$8.prototype._getValue_array,$8.prototype._getValue_arrayElement,$8.prototype._getValue_toArray];$8.prototype.SetterByBindingTypeAndVersioning=[[$8.prototype._setValue_direct,$8.prototype._setValue_direct_setNeedsUpdate,$8.prototype._setValue_direct_setMatrixWorldNeedsUpdate],[$8.prototype._setValue_array,$8.prototype._setValue_array_setNeedsUpdate,$8.prototype._setValue_array_setMatrixWorldNeedsUpdate],[$8.prototype._setValue_arrayElement,$8.prototype._setValue_arrayElement_setNeedsUpdate,$8.prototype._setValue_arrayElement_setMatrixWorldNeedsUpdate],[$8.prototype._setValue_fromArray,$8.prototype._setValue_fromArray_setNeedsUpdate,$8.prototype._setValue_fromArray_setMatrixWorldNeedsUpdate]];var lX=new Float32Array(1);var cN=new U8;class Z7{constructor(J,$,N=0,Q=1/0){this.ray=new m9(J,$),this.near=N,this.far=Q,this.camera=null,this.layers=new aJ,this.params={Mesh:{},Line:{threshold:1},LOD:{},Points:{threshold:1},Sprite:{}}}set(J,$){this.ray.set(J,$)}setFromCamera(J,$){if($.isPerspectiveCamera)this.ray.origin.setFromMatrixPosition($.matrixWorld),this.ray.direction.set(J.x,J.y,0.5).unproject($).sub(this.ray.origin).normalize(),this.camera=$;else if($.isOrthographicCamera)this.ray.origin.set(J.x,J.y,($.near+$.far)/($.near-$.far)).unproject($),this.ray.direction.set(0,0,-1).transformDirection($.matrixWorld),this.camera=$;else console.error("THREE.Raycaster: Unsupported camera type: "+$.type)}setFromXRController(J){return cN.identity().extractRotation(J.matrixWorld),this.ray.origin.setFromMatrixPosition(J.matrixWorld),this.ray.direction.set(0,0,-1).applyMatrix4(cN),this}intersectObject(J,$=!0,N=[]){return g7(J,this,N,$),N.sort(nN),N}intersectObjects(J,$=!0,N=[]){for(let Q=0,Z=J.length;Q<Z;Q++)g7(J[Q],this,N,$);return N.sort(nN),N}}function nN(J,$){return J.distance-$.distance}function g7(J,$,N,Q){let Z=!0;if(J.layers.test($.layers)){if(J.raycast($,N)===!1)Z=!1}if(Z===!0&&Q===!0){let W=J.children;for(let Y=0,K=W.length;Y<K;Y++)g7(W[Y],$,N,!0)}}class $6{constructor(J=1,$=0,N=0){this.radius=J,this.phi=$,this.theta=N}set(J,$,N){return this.radius=J,this.phi=$,this.theta=N,this}copy(J){return this.radius=J.radius,this.phi=J.phi,this.theta=J.theta,this}makeSafe(){return this.phi=n0(this.phi,0.000001,Math.PI-0.000001),this}setFromVector3(J){return this.setFromCartesianCoords(J.x,J.y,J.z)}setFromCartesianCoords(J,$,N){if(this.radius=Math.sqrt(J*J+$*$+N*N),this.radius===0)this.theta=0,this.phi=0;else this.theta=Math.atan2(J,N),this.phi=Math.acos(n0($/this.radius,-1,1));return this}clone(){return new this.constructor().copy(this)}}class W7 extends _9{constructor(J,$=null){super();this.object=J,this.domElement=$,this.enabled=!0,this.state=-1,this.keys={},this.mouseButtons={LEFT:null,MIDDLE:null,RIGHT:null},this.touches={ONE:null,TWO:null}}connect(J){if(J===void 0){console.warn("THREE.Controls: connect() now requires an element.");return}if(this.domElement!==null)this.disconnect();this.domElement=J}disconnect(){}dispose(){}update(){}}function t$(J,$,N,Q){let Z=uW(Q);switch(N){case 1021:return J*$;case 1028:return J*$/Z.components*Z.byteLength;case 1029:return J*$/Z.components*Z.byteLength;case 1030:return J*$*2/Z.components*Z.byteLength;case 1031:return J*$*2/Z.components*Z.byteLength;case 1022:return J*$*3/Z.components*Z.byteLength;case 1023:return J*$*4/Z.components*Z.byteLength;case 1033:return J*$*4/Z.components*Z.byteLength;case 33776:case 33777:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*8;case 33778:case 33779:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 35841:case 35843:return Math.max(J,16)*Math.max($,8)/4;case 35840:case 35842:return Math.max(J,8)*Math.max($,8)/2;case 36196:case 37492:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*8;case 37496:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 37808:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 37809:return Math.floor((J+4)/5)*Math.floor(($+3)/4)*16;case 37810:return Math.floor((J+4)/5)*Math.floor(($+4)/5)*16;case 37811:return Math.floor((J+5)/6)*Math.floor(($+4)/5)*16;case 37812:return Math.floor((J+5)/6)*Math.floor(($+5)/6)*16;case 37813:return Math.floor((J+7)/8)*Math.floor(($+4)/5)*16;case 37814:return Math.floor((J+7)/8)*Math.floor(($+5)/6)*16;case 37815:return Math.floor((J+7)/8)*Math.floor(($+7)/8)*16;case 37816:return Math.floor((J+9)/10)*Math.floor(($+4)/5)*16;case 37817:return Math.floor((J+9)/10)*Math.floor(($+5)/6)*16;case 37818:return Math.floor((J+9)/10)*Math.floor(($+7)/8)*16;case 37819:return Math.floor((J+9)/10)*Math.floor(($+9)/10)*16;case 37820:return Math.floor((J+11)/12)*Math.floor(($+9)/10)*16;case 37821:return Math.floor((J+11)/12)*Math.floor(($+11)/12)*16;case 36492:case 36494:case 36495:return Math.ceil(J/4)*Math.ceil($/4)*16;case 36283:case 36284:return Math.ceil(J/4)*Math.ceil($/4)*8;case 36285:case 36286:return Math.ceil(J/4)*Math.ceil($/4)*16}throw Error(`Unable to determine texture byte length for ${N} format.`)}function uW(J){switch(J){case 1009:case 1010:return{byteLength:1,components:1};case 1012:case 1011:case 1016:return{byteLength:2,components:1};case 1017:case 1018:return{byteLength:2,components:4};case 1014:case 1013:case 1015:return{byteLength:4,components:1};case 35902:case 35899:return{byteLength:4,components:3}}throw Error(`Unknown texture type ${J}.`)}if(typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("register",{detail:{revision:"180"}}));if(typeof window<"u")if(window.__THREE__)console.warn("WARNING: Multiple instances of Three.js being imported.");else window.__THREE__="180";function IZ(){let J=null,$=!1,N=null,Q=null;function Z(W,Y){N(W,Y),Q=J.requestAnimationFrame(Z)}return{start:function(){if($===!0)return;if(N===null)return;Q=J.requestAnimationFrame(Z),$=!0},stop:function(){J.cancelAnimationFrame(Q),$=!1},setAnimationLoop:function(W){N=W},setContext:function(W){J=W}}}function cW(J){let $=new WeakMap;function N(K,H){let{array:X,usage:G}=K,E=X.byteLength,q=J.createBuffer();J.bindBuffer(H,q),J.bufferData(H,X,G),K.onUploadCallback();let k;if(X instanceof Float32Array)k=J.FLOAT;else if(typeof Float16Array<"u"&&X instanceof Float16Array)k=J.HALF_FLOAT;else if(X instanceof Uint16Array)if(K.isFloat16BufferAttribute)k=J.HALF_FLOAT;else k=J.UNSIGNED_SHORT;else if(X instanceof Int16Array)k=J.SHORT;else if(X instanceof Uint32Array)k=J.UNSIGNED_INT;else if(X instanceof Int32Array)k=J.INT;else if(X instanceof Int8Array)k=J.BYTE;else if(X instanceof Uint8Array)k=J.UNSIGNED_BYTE;else if(X instanceof Uint8ClampedArray)k=J.UNSIGNED_BYTE;else throw Error("THREE.WebGLAttributes: Unsupported buffer data format: "+X);return{buffer:q,type:k,bytesPerElement:X.BYTES_PER_ELEMENT,version:K.version,size:E}}function Q(K,H,X){let{array:G,updateRanges:E}=H;if(J.bindBuffer(X,K),E.length===0)J.bufferSubData(X,0,G);else{E.sort((k,M)=>k.start-M.start);let q=0;for(let k=1;k<E.length;k++){let M=E[q],_=E[k];if(_.start<=M.start+M.count+1)M.count=Math.max(M.count,_.start+_.count-M.start);else++q,E[q]=_}E.length=q+1;for(let k=0,M=E.length;k<M;k++){let _=E[k];J.bufferSubData(X,_.start*G.BYTES_PER_ELEMENT,G,_.start,_.count)}H.clearUpdateRanges()}H.onUploadCallback()}function Z(K){if(K.isInterleavedBufferAttribute)K=K.data;return $.get(K)}function W(K){if(K.isInterleavedBufferAttribute)K=K.data;let H=$.get(K);if(H)J.deleteBuffer(H.buffer),$.delete(K)}function Y(K,H){if(K.isInterleavedBufferAttribute)K=K.data;if(K.isGLBufferAttribute){let G=$.get(K);if(!G||G.version<K.version)$.set(K,{buffer:K.buffer,type:K.type,bytesPerElement:K.elementSize,version:K.version});return}let X=$.get(K);if(X===void 0)$.set(K,N(K,H));else if(X.version<K.version){if(X.size!==K.array.byteLength)throw Error("THREE.WebGLAttributes: The size of the buffer attribute's array buffer does not match the original size. Resizing buffer attributes is not supported.");Q(X.buffer,K,H),X.version=K.version}}return{get:Z,remove:W,update:Y}}var nW=`#ifdef USE_ALPHAHASH
	if ( diffuseColor.a < getAlphaHashThreshold( vPosition ) ) discard;
#endif`,sW=`#ifdef USE_ALPHAHASH
	const float ALPHA_HASH_SCALE = 0.05;
	float hash2D( vec2 value ) {
		return fract( 1.0e4 * sin( 17.0 * value.x + 0.1 * value.y ) * ( 0.1 + abs( sin( 13.0 * value.y + value.x ) ) ) );
	}
	float hash3D( vec3 value ) {
		return hash2D( vec2( hash2D( value.xy ), value.z ) );
	}
	float getAlphaHashThreshold( vec3 position ) {
		float maxDeriv = max(
			length( dFdx( position.xyz ) ),
			length( dFdy( position.xyz ) )
		);
		float pixScale = 1.0 / ( ALPHA_HASH_SCALE * maxDeriv );
		vec2 pixScales = vec2(
			exp2( floor( log2( pixScale ) ) ),
			exp2( ceil( log2( pixScale ) ) )
		);
		vec2 alpha = vec2(
			hash3D( floor( pixScales.x * position.xyz ) ),
			hash3D( floor( pixScales.y * position.xyz ) )
		);
		float lerpFactor = fract( log2( pixScale ) );
		float x = ( 1.0 - lerpFactor ) * alpha.x + lerpFactor * alpha.y;
		float a = min( lerpFactor, 1.0 - lerpFactor );
		vec3 cases = vec3(
			x * x / ( 2.0 * a * ( 1.0 - a ) ),
			( x - 0.5 * a ) / ( 1.0 - a ),
			1.0 - ( ( 1.0 - x ) * ( 1.0 - x ) / ( 2.0 * a * ( 1.0 - a ) ) )
		);
		float threshold = ( x < ( 1.0 - a ) )
			? ( ( x < a ) ? cases.x : cases.y )
			: cases.z;
		return clamp( threshold , 1.0e-6, 1.0 );
	}
#endif`,iW=`#ifdef USE_ALPHAMAP
	diffuseColor.a *= texture2D( alphaMap, vAlphaMapUv ).g;
#endif`,oW=`#ifdef USE_ALPHAMAP
	uniform sampler2D alphaMap;
#endif`,aW=`#ifdef USE_ALPHATEST
	#ifdef ALPHA_TO_COVERAGE
	diffuseColor.a = smoothstep( alphaTest, alphaTest + fwidth( diffuseColor.a ), diffuseColor.a );
	if ( diffuseColor.a == 0.0 ) discard;
	#else
	if ( diffuseColor.a < alphaTest ) discard;
	#endif
#endif`,rW=`#ifdef USE_ALPHATEST
	uniform float alphaTest;
#endif`,tW=`#ifdef USE_AOMAP
	float ambientOcclusion = ( texture2D( aoMap, vAoMapUv ).r - 1.0 ) * aoMapIntensity + 1.0;
	reflectedLight.indirectDiffuse *= ambientOcclusion;
	#if defined( USE_CLEARCOAT ) 
		clearcoatSpecularIndirect *= ambientOcclusion;
	#endif
	#if defined( USE_SHEEN ) 
		sheenSpecularIndirect *= ambientOcclusion;
	#endif
	#if defined( USE_ENVMAP ) && defined( STANDARD )
		float dotNV = saturate( dot( geometryNormal, geometryViewDir ) );
		reflectedLight.indirectSpecular *= computeSpecularOcclusion( dotNV, ambientOcclusion, material.roughness );
	#endif
#endif`,eW=`#ifdef USE_AOMAP
	uniform sampler2D aoMap;
	uniform float aoMapIntensity;
#endif`,J1=`#ifdef USE_BATCHING
	#if ! defined( GL_ANGLE_multi_draw )
	#define gl_DrawID _gl_DrawID
	uniform int _gl_DrawID;
	#endif
	uniform highp sampler2D batchingTexture;
	uniform highp usampler2D batchingIdTexture;
	mat4 getBatchingMatrix( const in float i ) {
		int size = textureSize( batchingTexture, 0 ).x;
		int j = int( i ) * 4;
		int x = j % size;
		int y = j / size;
		vec4 v1 = texelFetch( batchingTexture, ivec2( x, y ), 0 );
		vec4 v2 = texelFetch( batchingTexture, ivec2( x + 1, y ), 0 );
		vec4 v3 = texelFetch( batchingTexture, ivec2( x + 2, y ), 0 );
		vec4 v4 = texelFetch( batchingTexture, ivec2( x + 3, y ), 0 );
		return mat4( v1, v2, v3, v4 );
	}
	float getIndirectIndex( const in int i ) {
		int size = textureSize( batchingIdTexture, 0 ).x;
		int x = i % size;
		int y = i / size;
		return float( texelFetch( batchingIdTexture, ivec2( x, y ), 0 ).r );
	}
#endif
#ifdef USE_BATCHING_COLOR
	uniform sampler2D batchingColorTexture;
	vec3 getBatchingColor( const in float i ) {
		int size = textureSize( batchingColorTexture, 0 ).x;
		int j = int( i );
		int x = j % size;
		int y = j / size;
		return texelFetch( batchingColorTexture, ivec2( x, y ), 0 ).rgb;
	}
#endif`,$1=`#ifdef USE_BATCHING
	mat4 batchingMatrix = getBatchingMatrix( getIndirectIndex( gl_DrawID ) );
#endif`,N1=`vec3 transformed = vec3( position );
#ifdef USE_ALPHAHASH
	vPosition = vec3( position );
#endif`,Q1=`vec3 objectNormal = vec3( normal );
#ifdef USE_TANGENT
	vec3 objectTangent = vec3( tangent.xyz );
#endif`,Z1=`float G_BlinnPhong_Implicit( ) {
	return 0.25;
}
float D_BlinnPhong( const in float shininess, const in float dotNH ) {
	return RECIPROCAL_PI * ( shininess * 0.5 + 1.0 ) * pow( dotNH, shininess );
}
vec3 BRDF_BlinnPhong( const in vec3 lightDir, const in vec3 viewDir, const in vec3 normal, const in vec3 specularColor, const in float shininess ) {
	vec3 halfDir = normalize( lightDir + viewDir );
	float dotNH = saturate( dot( normal, halfDir ) );
	float dotVH = saturate( dot( viewDir, halfDir ) );
	vec3 F = F_Schlick( specularColor, 1.0, dotVH );
	float G = G_BlinnPhong_Implicit( );
	float D = D_BlinnPhong( shininess, dotNH );
	return F * ( G * D );
} // validated`,W1=`#ifdef USE_IRIDESCENCE
	const mat3 XYZ_TO_REC709 = mat3(
		 3.2404542, -0.9692660,  0.0556434,
		-1.5371385,  1.8760108, -0.2040259,
		-0.4985314,  0.0415560,  1.0572252
	);
	vec3 Fresnel0ToIor( vec3 fresnel0 ) {
		vec3 sqrtF0 = sqrt( fresnel0 );
		return ( vec3( 1.0 ) + sqrtF0 ) / ( vec3( 1.0 ) - sqrtF0 );
	}
	vec3 IorToFresnel0( vec3 transmittedIor, float incidentIor ) {
		return pow2( ( transmittedIor - vec3( incidentIor ) ) / ( transmittedIor + vec3( incidentIor ) ) );
	}
	float IorToFresnel0( float transmittedIor, float incidentIor ) {
		return pow2( ( transmittedIor - incidentIor ) / ( transmittedIor + incidentIor ));
	}
	vec3 evalSensitivity( float OPD, vec3 shift ) {
		float phase = 2.0 * PI * OPD * 1.0e-9;
		vec3 val = vec3( 5.4856e-13, 4.4201e-13, 5.2481e-13 );
		vec3 pos = vec3( 1.6810e+06, 1.7953e+06, 2.2084e+06 );
		vec3 var = vec3( 4.3278e+09, 9.3046e+09, 6.6121e+09 );
		vec3 xyz = val * sqrt( 2.0 * PI * var ) * cos( pos * phase + shift ) * exp( - pow2( phase ) * var );
		xyz.x += 9.7470e-14 * sqrt( 2.0 * PI * 4.5282e+09 ) * cos( 2.2399e+06 * phase + shift[ 0 ] ) * exp( - 4.5282e+09 * pow2( phase ) );
		xyz /= 1.0685e-7;
		vec3 rgb = XYZ_TO_REC709 * xyz;
		return rgb;
	}
	vec3 evalIridescence( float outsideIOR, float eta2, float cosTheta1, float thinFilmThickness, vec3 baseF0 ) {
		vec3 I;
		float iridescenceIOR = mix( outsideIOR, eta2, smoothstep( 0.0, 0.03, thinFilmThickness ) );
		float sinTheta2Sq = pow2( outsideIOR / iridescenceIOR ) * ( 1.0 - pow2( cosTheta1 ) );
		float cosTheta2Sq = 1.0 - sinTheta2Sq;
		if ( cosTheta2Sq < 0.0 ) {
			return vec3( 1.0 );
		}
		float cosTheta2 = sqrt( cosTheta2Sq );
		float R0 = IorToFresnel0( iridescenceIOR, outsideIOR );
		float R12 = F_Schlick( R0, 1.0, cosTheta1 );
		float T121 = 1.0 - R12;
		float phi12 = 0.0;
		if ( iridescenceIOR < outsideIOR ) phi12 = PI;
		float phi21 = PI - phi12;
		vec3 baseIOR = Fresnel0ToIor( clamp( baseF0, 0.0, 0.9999 ) );		vec3 R1 = IorToFresnel0( baseIOR, iridescenceIOR );
		vec3 R23 = F_Schlick( R1, 1.0, cosTheta2 );
		vec3 phi23 = vec3( 0.0 );
		if ( baseIOR[ 0 ] < iridescenceIOR ) phi23[ 0 ] = PI;
		if ( baseIOR[ 1 ] < iridescenceIOR ) phi23[ 1 ] = PI;
		if ( baseIOR[ 2 ] < iridescenceIOR ) phi23[ 2 ] = PI;
		float OPD = 2.0 * iridescenceIOR * thinFilmThickness * cosTheta2;
		vec3 phi = vec3( phi21 ) + phi23;
		vec3 R123 = clamp( R12 * R23, 1e-5, 0.9999 );
		vec3 r123 = sqrt( R123 );
		vec3 Rs = pow2( T121 ) * R23 / ( vec3( 1.0 ) - R123 );
		vec3 C0 = R12 + Rs;
		I = C0;
		vec3 Cm = Rs - T121;
		for ( int m = 1; m <= 2; ++ m ) {
			Cm *= r123;
			vec3 Sm = 2.0 * evalSensitivity( float( m ) * OPD, float( m ) * phi );
			I += Cm * Sm;
		}
		return max( I, vec3( 0.0 ) );
	}
#endif`,K1=`#ifdef USE_BUMPMAP
	uniform sampler2D bumpMap;
	uniform float bumpScale;
	vec2 dHdxy_fwd() {
		vec2 dSTdx = dFdx( vBumpMapUv );
		vec2 dSTdy = dFdy( vBumpMapUv );
		float Hll = bumpScale * texture2D( bumpMap, vBumpMapUv ).x;
		float dBx = bumpScale * texture2D( bumpMap, vBumpMapUv + dSTdx ).x - Hll;
		float dBy = bumpScale * texture2D( bumpMap, vBumpMapUv + dSTdy ).x - Hll;
		return vec2( dBx, dBy );
	}
	vec3 perturbNormalArb( vec3 surf_pos, vec3 surf_norm, vec2 dHdxy, float faceDirection ) {
		vec3 vSigmaX = normalize( dFdx( surf_pos.xyz ) );
		vec3 vSigmaY = normalize( dFdy( surf_pos.xyz ) );
		vec3 vN = surf_norm;
		vec3 R1 = cross( vSigmaY, vN );
		vec3 R2 = cross( vN, vSigmaX );
		float fDet = dot( vSigmaX, R1 ) * faceDirection;
		vec3 vGrad = sign( fDet ) * ( dHdxy.x * R1 + dHdxy.y * R2 );
		return normalize( abs( fDet ) * surf_norm - vGrad );
	}
#endif`,Y1=`#if NUM_CLIPPING_PLANES > 0
	vec4 plane;
	#ifdef ALPHA_TO_COVERAGE
		float distanceToPlane, distanceGradient;
		float clipOpacity = 1.0;
		#pragma unroll_loop_start
		for ( int i = 0; i < UNION_CLIPPING_PLANES; i ++ ) {
			plane = clippingPlanes[ i ];
			distanceToPlane = - dot( vClipPosition, plane.xyz ) + plane.w;
			distanceGradient = fwidth( distanceToPlane ) / 2.0;
			clipOpacity *= smoothstep( - distanceGradient, distanceGradient, distanceToPlane );
			if ( clipOpacity == 0.0 ) discard;
		}
		#pragma unroll_loop_end
		#if UNION_CLIPPING_PLANES < NUM_CLIPPING_PLANES
			float unionClipOpacity = 1.0;
			#pragma unroll_loop_start
			for ( int i = UNION_CLIPPING_PLANES; i < NUM_CLIPPING_PLANES; i ++ ) {
				plane = clippingPlanes[ i ];
				distanceToPlane = - dot( vClipPosition, plane.xyz ) + plane.w;
				distanceGradient = fwidth( distanceToPlane ) / 2.0;
				unionClipOpacity *= 1.0 - smoothstep( - distanceGradient, distanceGradient, distanceToPlane );
			}
			#pragma unroll_loop_end
			clipOpacity *= 1.0 - unionClipOpacity;
		#endif
		diffuseColor.a *= clipOpacity;
		if ( diffuseColor.a == 0.0 ) discard;
	#else
		#pragma unroll_loop_start
		for ( int i = 0; i < UNION_CLIPPING_PLANES; i ++ ) {
			plane = clippingPlanes[ i ];
			if ( dot( vClipPosition, plane.xyz ) > plane.w ) discard;
		}
		#pragma unroll_loop_end
		#if UNION_CLIPPING_PLANES < NUM_CLIPPING_PLANES
			bool clipped = true;
			#pragma unroll_loop_start
			for ( int i = UNION_CLIPPING_PLANES; i < NUM_CLIPPING_PLANES; i ++ ) {
				plane = clippingPlanes[ i ];
				clipped = ( dot( vClipPosition, plane.xyz ) > plane.w ) && clipped;
			}
			#pragma unroll_loop_end
			if ( clipped ) discard;
		#endif
	#endif
#endif`,H1=`#if NUM_CLIPPING_PLANES > 0
	varying vec3 vClipPosition;
	uniform vec4 clippingPlanes[ NUM_CLIPPING_PLANES ];
#endif`,X1=`#if NUM_CLIPPING_PLANES > 0
	varying vec3 vClipPosition;
#endif`,U1=`#if NUM_CLIPPING_PLANES > 0
	vClipPosition = - mvPosition.xyz;
#endif`,G1=`#if defined( USE_COLOR_ALPHA )
	diffuseColor *= vColor;
#elif defined( USE_COLOR )
	diffuseColor.rgb *= vColor;
#endif`,E1=`#if defined( USE_COLOR_ALPHA )
	varying vec4 vColor;
#elif defined( USE_COLOR )
	varying vec3 vColor;
#endif`,D1=`#if defined( USE_COLOR_ALPHA )
	varying vec4 vColor;
#elif defined( USE_COLOR ) || defined( USE_INSTANCING_COLOR ) || defined( USE_BATCHING_COLOR )
	varying vec3 vColor;
#endif`,q1=`#if defined( USE_COLOR_ALPHA )
	vColor = vec4( 1.0 );
#elif defined( USE_COLOR ) || defined( USE_INSTANCING_COLOR ) || defined( USE_BATCHING_COLOR )
	vColor = vec3( 1.0 );
#endif
#ifdef USE_COLOR
	vColor *= color;
#endif
#ifdef USE_INSTANCING_COLOR
	vColor.xyz *= instanceColor.xyz;
#endif
#ifdef USE_BATCHING_COLOR
	vec3 batchingColor = getBatchingColor( getIndirectIndex( gl_DrawID ) );
	vColor.xyz *= batchingColor.xyz;
#endif`,k1=`#define PI 3.141592653589793
#define PI2 6.283185307179586
#define PI_HALF 1.5707963267948966
#define RECIPROCAL_PI 0.3183098861837907
#define RECIPROCAL_PI2 0.15915494309189535
#define EPSILON 1e-6
#ifndef saturate
#define saturate( a ) clamp( a, 0.0, 1.0 )
#endif
#define whiteComplement( a ) ( 1.0 - saturate( a ) )
float pow2( const in float x ) { return x*x; }
vec3 pow2( const in vec3 x ) { return x*x; }
float pow3( const in float x ) { return x*x*x; }
float pow4( const in float x ) { float x2 = x*x; return x2*x2; }
float max3( const in vec3 v ) { return max( max( v.x, v.y ), v.z ); }
float average( const in vec3 v ) { return dot( v, vec3( 0.3333333 ) ); }
highp float rand( const in vec2 uv ) {
	const highp float a = 12.9898, b = 78.233, c = 43758.5453;
	highp float dt = dot( uv.xy, vec2( a,b ) ), sn = mod( dt, PI );
	return fract( sin( sn ) * c );
}
#ifdef HIGH_PRECISION
	float precisionSafeLength( vec3 v ) { return length( v ); }
#else
	float precisionSafeLength( vec3 v ) {
		float maxComponent = max3( abs( v ) );
		return length( v / maxComponent ) * maxComponent;
	}
#endif
struct IncidentLight {
	vec3 color;
	vec3 direction;
	bool visible;
};
struct ReflectedLight {
	vec3 directDiffuse;
	vec3 directSpecular;
	vec3 indirectDiffuse;
	vec3 indirectSpecular;
};
#ifdef USE_ALPHAHASH
	varying vec3 vPosition;
#endif
vec3 transformDirection( in vec3 dir, in mat4 matrix ) {
	return normalize( ( matrix * vec4( dir, 0.0 ) ).xyz );
}
vec3 inverseTransformDirection( in vec3 dir, in mat4 matrix ) {
	return normalize( ( vec4( dir, 0.0 ) * matrix ).xyz );
}
mat3 transposeMat3( const in mat3 m ) {
	mat3 tmp;
	tmp[ 0 ] = vec3( m[ 0 ].x, m[ 1 ].x, m[ 2 ].x );
	tmp[ 1 ] = vec3( m[ 0 ].y, m[ 1 ].y, m[ 2 ].y );
	tmp[ 2 ] = vec3( m[ 0 ].z, m[ 1 ].z, m[ 2 ].z );
	return tmp;
}
bool isPerspectiveMatrix( mat4 m ) {
	return m[ 2 ][ 3 ] == - 1.0;
}
vec2 equirectUv( in vec3 dir ) {
	float u = atan( dir.z, dir.x ) * RECIPROCAL_PI2 + 0.5;
	float v = asin( clamp( dir.y, - 1.0, 1.0 ) ) * RECIPROCAL_PI + 0.5;
	return vec2( u, v );
}
vec3 BRDF_Lambert( const in vec3 diffuseColor ) {
	return RECIPROCAL_PI * diffuseColor;
}
vec3 F_Schlick( const in vec3 f0, const in float f90, const in float dotVH ) {
	float fresnel = exp2( ( - 5.55473 * dotVH - 6.98316 ) * dotVH );
	return f0 * ( 1.0 - fresnel ) + ( f90 * fresnel );
}
float F_Schlick( const in float f0, const in float f90, const in float dotVH ) {
	float fresnel = exp2( ( - 5.55473 * dotVH - 6.98316 ) * dotVH );
	return f0 * ( 1.0 - fresnel ) + ( f90 * fresnel );
} // validated`,O1=`#ifdef ENVMAP_TYPE_CUBE_UV
	#define cubeUV_minMipLevel 4.0
	#define cubeUV_minTileSize 16.0
	float getFace( vec3 direction ) {
		vec3 absDirection = abs( direction );
		float face = - 1.0;
		if ( absDirection.x > absDirection.z ) {
			if ( absDirection.x > absDirection.y )
				face = direction.x > 0.0 ? 0.0 : 3.0;
			else
				face = direction.y > 0.0 ? 1.0 : 4.0;
		} else {
			if ( absDirection.z > absDirection.y )
				face = direction.z > 0.0 ? 2.0 : 5.0;
			else
				face = direction.y > 0.0 ? 1.0 : 4.0;
		}
		return face;
	}
	vec2 getUV( vec3 direction, float face ) {
		vec2 uv;
		if ( face == 0.0 ) {
			uv = vec2( direction.z, direction.y ) / abs( direction.x );
		} else if ( face == 1.0 ) {
			uv = vec2( - direction.x, - direction.z ) / abs( direction.y );
		} else if ( face == 2.0 ) {
			uv = vec2( - direction.x, direction.y ) / abs( direction.z );
		} else if ( face == 3.0 ) {
			uv = vec2( - direction.z, direction.y ) / abs( direction.x );
		} else if ( face == 4.0 ) {
			uv = vec2( - direction.x, direction.z ) / abs( direction.y );
		} else {
			uv = vec2( direction.x, direction.y ) / abs( direction.z );
		}
		return 0.5 * ( uv + 1.0 );
	}
	vec3 bilinearCubeUV( sampler2D envMap, vec3 direction, float mipInt ) {
		float face = getFace( direction );
		float filterInt = max( cubeUV_minMipLevel - mipInt, 0.0 );
		mipInt = max( mipInt, cubeUV_minMipLevel );
		float faceSize = exp2( mipInt );
		highp vec2 uv = getUV( direction, face ) * ( faceSize - 2.0 ) + 1.0;
		if ( face > 2.0 ) {
			uv.y += faceSize;
			face -= 3.0;
		}
		uv.x += face * faceSize;
		uv.x += filterInt * 3.0 * cubeUV_minTileSize;
		uv.y += 4.0 * ( exp2( CUBEUV_MAX_MIP ) - faceSize );
		uv.x *= CUBEUV_TEXEL_WIDTH;
		uv.y *= CUBEUV_TEXEL_HEIGHT;
		#ifdef texture2DGradEXT
			return texture2DGradEXT( envMap, uv, vec2( 0.0 ), vec2( 0.0 ) ).rgb;
		#else
			return texture2D( envMap, uv ).rgb;
		#endif
	}
	#define cubeUV_r0 1.0
	#define cubeUV_m0 - 2.0
	#define cubeUV_r1 0.8
	#define cubeUV_m1 - 1.0
	#define cubeUV_r4 0.4
	#define cubeUV_m4 2.0
	#define cubeUV_r5 0.305
	#define cubeUV_m5 3.0
	#define cubeUV_r6 0.21
	#define cubeUV_m6 4.0
	float roughnessToMip( float roughness ) {
		float mip = 0.0;
		if ( roughness >= cubeUV_r1 ) {
			mip = ( cubeUV_r0 - roughness ) * ( cubeUV_m1 - cubeUV_m0 ) / ( cubeUV_r0 - cubeUV_r1 ) + cubeUV_m0;
		} else if ( roughness >= cubeUV_r4 ) {
			mip = ( cubeUV_r1 - roughness ) * ( cubeUV_m4 - cubeUV_m1 ) / ( cubeUV_r1 - cubeUV_r4 ) + cubeUV_m1;
		} else if ( roughness >= cubeUV_r5 ) {
			mip = ( cubeUV_r4 - roughness ) * ( cubeUV_m5 - cubeUV_m4 ) / ( cubeUV_r4 - cubeUV_r5 ) + cubeUV_m4;
		} else if ( roughness >= cubeUV_r6 ) {
			mip = ( cubeUV_r5 - roughness ) * ( cubeUV_m6 - cubeUV_m5 ) / ( cubeUV_r5 - cubeUV_r6 ) + cubeUV_m5;
		} else {
			mip = - 2.0 * log2( 1.16 * roughness );		}
		return mip;
	}
	vec4 textureCubeUV( sampler2D envMap, vec3 sampleDir, float roughness ) {
		float mip = clamp( roughnessToMip( roughness ), cubeUV_m0, CUBEUV_MAX_MIP );
		float mipF = fract( mip );
		float mipInt = floor( mip );
		vec3 color0 = bilinearCubeUV( envMap, sampleDir, mipInt );
		if ( mipF == 0.0 ) {
			return vec4( color0, 1.0 );
		} else {
			vec3 color1 = bilinearCubeUV( envMap, sampleDir, mipInt + 1.0 );
			return vec4( mix( color0, color1, mipF ), 1.0 );
		}
	}
#endif`,R1=`vec3 transformedNormal = objectNormal;
#ifdef USE_TANGENT
	vec3 transformedTangent = objectTangent;
#endif
#ifdef USE_BATCHING
	mat3 bm = mat3( batchingMatrix );
	transformedNormal /= vec3( dot( bm[ 0 ], bm[ 0 ] ), dot( bm[ 1 ], bm[ 1 ] ), dot( bm[ 2 ], bm[ 2 ] ) );
	transformedNormal = bm * transformedNormal;
	#ifdef USE_TANGENT
		transformedTangent = bm * transformedTangent;
	#endif
#endif
#ifdef USE_INSTANCING
	mat3 im = mat3( instanceMatrix );
	transformedNormal /= vec3( dot( im[ 0 ], im[ 0 ] ), dot( im[ 1 ], im[ 1 ] ), dot( im[ 2 ], im[ 2 ] ) );
	transformedNormal = im * transformedNormal;
	#ifdef USE_TANGENT
		transformedTangent = im * transformedTangent;
	#endif
#endif
transformedNormal = normalMatrix * transformedNormal;
#ifdef FLIP_SIDED
	transformedNormal = - transformedNormal;
#endif
#ifdef USE_TANGENT
	transformedTangent = ( modelViewMatrix * vec4( transformedTangent, 0.0 ) ).xyz;
	#ifdef FLIP_SIDED
		transformedTangent = - transformedTangent;
	#endif
#endif`,F1=`#ifdef USE_DISPLACEMENTMAP
	uniform sampler2D displacementMap;
	uniform float displacementScale;
	uniform float displacementBias;
#endif`,M1=`#ifdef USE_DISPLACEMENTMAP
	transformed += normalize( objectNormal ) * ( texture2D( displacementMap, vDisplacementMapUv ).x * displacementScale + displacementBias );
#endif`,_1=`#ifdef USE_EMISSIVEMAP
	vec4 emissiveColor = texture2D( emissiveMap, vEmissiveMapUv );
	#ifdef DECODE_VIDEO_TEXTURE_EMISSIVE
		emissiveColor = sRGBTransferEOTF( emissiveColor );
	#endif
	totalEmissiveRadiance *= emissiveColor.rgb;
#endif`,L1=`#ifdef USE_EMISSIVEMAP
	uniform sampler2D emissiveMap;
#endif`,V1="gl_FragColor = linearToOutputTexel( gl_FragColor );",w1=`vec4 LinearTransferOETF( in vec4 value ) {
	return value;
}
vec4 sRGBTransferEOTF( in vec4 value ) {
	return vec4( mix( pow( value.rgb * 0.9478672986 + vec3( 0.0521327014 ), vec3( 2.4 ) ), value.rgb * 0.0773993808, vec3( lessThanEqual( value.rgb, vec3( 0.04045 ) ) ) ), value.a );
}
vec4 sRGBTransferOETF( in vec4 value ) {
	return vec4( mix( pow( value.rgb, vec3( 0.41666 ) ) * 1.055 - vec3( 0.055 ), value.rgb * 12.92, vec3( lessThanEqual( value.rgb, vec3( 0.0031308 ) ) ) ), value.a );
}`,z1=`#ifdef USE_ENVMAP
	#ifdef ENV_WORLDPOS
		vec3 cameraToFrag;
		if ( isOrthographic ) {
			cameraToFrag = normalize( vec3( - viewMatrix[ 0 ][ 2 ], - viewMatrix[ 1 ][ 2 ], - viewMatrix[ 2 ][ 2 ] ) );
		} else {
			cameraToFrag = normalize( vWorldPosition - cameraPosition );
		}
		vec3 worldNormal = inverseTransformDirection( normal, viewMatrix );
		#ifdef ENVMAP_MODE_REFLECTION
			vec3 reflectVec = reflect( cameraToFrag, worldNormal );
		#else
			vec3 reflectVec = refract( cameraToFrag, worldNormal, refractionRatio );
		#endif
	#else
		vec3 reflectVec = vReflect;
	#endif
	#ifdef ENVMAP_TYPE_CUBE
		vec4 envColor = textureCube( envMap, envMapRotation * vec3( flipEnvMap * reflectVec.x, reflectVec.yz ) );
	#else
		vec4 envColor = vec4( 0.0 );
	#endif
	#ifdef ENVMAP_BLENDING_MULTIPLY
		outgoingLight = mix( outgoingLight, outgoingLight * envColor.xyz, specularStrength * reflectivity );
	#elif defined( ENVMAP_BLENDING_MIX )
		outgoingLight = mix( outgoingLight, envColor.xyz, specularStrength * reflectivity );
	#elif defined( ENVMAP_BLENDING_ADD )
		outgoingLight += envColor.xyz * specularStrength * reflectivity;
	#endif
#endif`,B1=`#ifdef USE_ENVMAP
	uniform float envMapIntensity;
	uniform float flipEnvMap;
	uniform mat3 envMapRotation;
	#ifdef ENVMAP_TYPE_CUBE
		uniform samplerCube envMap;
	#else
		uniform sampler2D envMap;
	#endif
	
#endif`,C1=`#ifdef USE_ENVMAP
	uniform float reflectivity;
	#if defined( USE_BUMPMAP ) || defined( USE_NORMALMAP ) || defined( PHONG ) || defined( LAMBERT )
		#define ENV_WORLDPOS
	#endif
	#ifdef ENV_WORLDPOS
		varying vec3 vWorldPosition;
		uniform float refractionRatio;
	#else
		varying vec3 vReflect;
	#endif
#endif`,I1=`#ifdef USE_ENVMAP
	#if defined( USE_BUMPMAP ) || defined( USE_NORMALMAP ) || defined( PHONG ) || defined( LAMBERT )
		#define ENV_WORLDPOS
	#endif
	#ifdef ENV_WORLDPOS
		
		varying vec3 vWorldPosition;
	#else
		varying vec3 vReflect;
		uniform float refractionRatio;
	#endif
#endif`,P1=`#ifdef USE_ENVMAP
	#ifdef ENV_WORLDPOS
		vWorldPosition = worldPosition.xyz;
	#else
		vec3 cameraToVertex;
		if ( isOrthographic ) {
			cameraToVertex = normalize( vec3( - viewMatrix[ 0 ][ 2 ], - viewMatrix[ 1 ][ 2 ], - viewMatrix[ 2 ][ 2 ] ) );
		} else {
			cameraToVertex = normalize( worldPosition.xyz - cameraPosition );
		}
		vec3 worldNormal = inverseTransformDirection( transformedNormal, viewMatrix );
		#ifdef ENVMAP_MODE_REFLECTION
			vReflect = reflect( cameraToVertex, worldNormal );
		#else
			vReflect = refract( cameraToVertex, worldNormal, refractionRatio );
		#endif
	#endif
#endif`,A1=`#ifdef USE_FOG
	vFogDepth = - mvPosition.z;
#endif`,T1=`#ifdef USE_FOG
	varying float vFogDepth;
#endif`,S1=`#ifdef USE_FOG
	#ifdef FOG_EXP2
		float fogFactor = 1.0 - exp( - fogDensity * fogDensity * vFogDepth * vFogDepth );
	#else
		float fogFactor = smoothstep( fogNear, fogFar, vFogDepth );
	#endif
	gl_FragColor.rgb = mix( gl_FragColor.rgb, fogColor, fogFactor );
#endif`,b1=`#ifdef USE_FOG
	uniform vec3 fogColor;
	varying float vFogDepth;
	#ifdef FOG_EXP2
		uniform float fogDensity;
	#else
		uniform float fogNear;
		uniform float fogFar;
	#endif
#endif`,y1=`#ifdef USE_GRADIENTMAP
	uniform sampler2D gradientMap;
#endif
vec3 getGradientIrradiance( vec3 normal, vec3 lightDirection ) {
	float dotNL = dot( normal, lightDirection );
	vec2 coord = vec2( dotNL * 0.5 + 0.5, 0.0 );
	#ifdef USE_GRADIENTMAP
		return vec3( texture2D( gradientMap, coord ).r );
	#else
		vec2 fw = fwidth( coord ) * 0.5;
		return mix( vec3( 0.7 ), vec3( 1.0 ), smoothstep( 0.7 - fw.x, 0.7 + fw.x, coord.x ) );
	#endif
}`,j1=`#ifdef USE_LIGHTMAP
	uniform sampler2D lightMap;
	uniform float lightMapIntensity;
#endif`,f1=`LambertMaterial material;
material.diffuseColor = diffuseColor.rgb;
material.specularStrength = specularStrength;`,v1=`varying vec3 vViewPosition;
struct LambertMaterial {
	vec3 diffuseColor;
	float specularStrength;
};
void RE_Direct_Lambert( const in IncidentLight directLight, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in LambertMaterial material, inout ReflectedLight reflectedLight ) {
	float dotNL = saturate( dot( geometryNormal, directLight.direction ) );
	vec3 irradiance = dotNL * directLight.color;
	reflectedLight.directDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
void RE_IndirectDiffuse_Lambert( const in vec3 irradiance, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in LambertMaterial material, inout ReflectedLight reflectedLight ) {
	reflectedLight.indirectDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
#define RE_Direct				RE_Direct_Lambert
#define RE_IndirectDiffuse		RE_IndirectDiffuse_Lambert`,h1=`uniform bool receiveShadow;
uniform vec3 ambientLightColor;
#if defined( USE_LIGHT_PROBES )
	uniform vec3 lightProbe[ 9 ];
#endif
vec3 shGetIrradianceAt( in vec3 normal, in vec3 shCoefficients[ 9 ] ) {
	float x = normal.x, y = normal.y, z = normal.z;
	vec3 result = shCoefficients[ 0 ] * 0.886227;
	result += shCoefficients[ 1 ] * 2.0 * 0.511664 * y;
	result += shCoefficients[ 2 ] * 2.0 * 0.511664 * z;
	result += shCoefficients[ 3 ] * 2.0 * 0.511664 * x;
	result += shCoefficients[ 4 ] * 2.0 * 0.429043 * x * y;
	result += shCoefficients[ 5 ] * 2.0 * 0.429043 * y * z;
	result += shCoefficients[ 6 ] * ( 0.743125 * z * z - 0.247708 );
	result += shCoefficients[ 7 ] * 2.0 * 0.429043 * x * z;
	result += shCoefficients[ 8 ] * 0.429043 * ( x * x - y * y );
	return result;
}
vec3 getLightProbeIrradiance( const in vec3 lightProbe[ 9 ], const in vec3 normal ) {
	vec3 worldNormal = inverseTransformDirection( normal, viewMatrix );
	vec3 irradiance = shGetIrradianceAt( worldNormal, lightProbe );
	return irradiance;
}
vec3 getAmbientLightIrradiance( const in vec3 ambientLightColor ) {
	vec3 irradiance = ambientLightColor;
	return irradiance;
}
float getDistanceAttenuation( const in float lightDistance, const in float cutoffDistance, const in float decayExponent ) {
	float distanceFalloff = 1.0 / max( pow( lightDistance, decayExponent ), 0.01 );
	if ( cutoffDistance > 0.0 ) {
		distanceFalloff *= pow2( saturate( 1.0 - pow4( lightDistance / cutoffDistance ) ) );
	}
	return distanceFalloff;
}
float getSpotAttenuation( const in float coneCosine, const in float penumbraCosine, const in float angleCosine ) {
	return smoothstep( coneCosine, penumbraCosine, angleCosine );
}
#if NUM_DIR_LIGHTS > 0
	struct DirectionalLight {
		vec3 direction;
		vec3 color;
	};
	uniform DirectionalLight directionalLights[ NUM_DIR_LIGHTS ];
	void getDirectionalLightInfo( const in DirectionalLight directionalLight, out IncidentLight light ) {
		light.color = directionalLight.color;
		light.direction = directionalLight.direction;
		light.visible = true;
	}
#endif
#if NUM_POINT_LIGHTS > 0
	struct PointLight {
		vec3 position;
		vec3 color;
		float distance;
		float decay;
	};
	uniform PointLight pointLights[ NUM_POINT_LIGHTS ];
	void getPointLightInfo( const in PointLight pointLight, const in vec3 geometryPosition, out IncidentLight light ) {
		vec3 lVector = pointLight.position - geometryPosition;
		light.direction = normalize( lVector );
		float lightDistance = length( lVector );
		light.color = pointLight.color;
		light.color *= getDistanceAttenuation( lightDistance, pointLight.distance, pointLight.decay );
		light.visible = ( light.color != vec3( 0.0 ) );
	}
#endif
#if NUM_SPOT_LIGHTS > 0
	struct SpotLight {
		vec3 position;
		vec3 direction;
		vec3 color;
		float distance;
		float decay;
		float coneCos;
		float penumbraCos;
	};
	uniform SpotLight spotLights[ NUM_SPOT_LIGHTS ];
	void getSpotLightInfo( const in SpotLight spotLight, const in vec3 geometryPosition, out IncidentLight light ) {
		vec3 lVector = spotLight.position - geometryPosition;
		light.direction = normalize( lVector );
		float angleCos = dot( light.direction, spotLight.direction );
		float spotAttenuation = getSpotAttenuation( spotLight.coneCos, spotLight.penumbraCos, angleCos );
		if ( spotAttenuation > 0.0 ) {
			float lightDistance = length( lVector );
			light.color = spotLight.color * spotAttenuation;
			light.color *= getDistanceAttenuation( lightDistance, spotLight.distance, spotLight.decay );
			light.visible = ( light.color != vec3( 0.0 ) );
		} else {
			light.color = vec3( 0.0 );
			light.visible = false;
		}
	}
#endif
#if NUM_RECT_AREA_LIGHTS > 0
	struct RectAreaLight {
		vec3 color;
		vec3 position;
		vec3 halfWidth;
		vec3 halfHeight;
	};
	uniform sampler2D ltc_1;	uniform sampler2D ltc_2;
	uniform RectAreaLight rectAreaLights[ NUM_RECT_AREA_LIGHTS ];
#endif
#if NUM_HEMI_LIGHTS > 0
	struct HemisphereLight {
		vec3 direction;
		vec3 skyColor;
		vec3 groundColor;
	};
	uniform HemisphereLight hemisphereLights[ NUM_HEMI_LIGHTS ];
	vec3 getHemisphereLightIrradiance( const in HemisphereLight hemiLight, const in vec3 normal ) {
		float dotNL = dot( normal, hemiLight.direction );
		float hemiDiffuseWeight = 0.5 * dotNL + 0.5;
		vec3 irradiance = mix( hemiLight.groundColor, hemiLight.skyColor, hemiDiffuseWeight );
		return irradiance;
	}
#endif`,x1=`#ifdef USE_ENVMAP
	vec3 getIBLIrradiance( const in vec3 normal ) {
		#ifdef ENVMAP_TYPE_CUBE_UV
			vec3 worldNormal = inverseTransformDirection( normal, viewMatrix );
			vec4 envMapColor = textureCubeUV( envMap, envMapRotation * worldNormal, 1.0 );
			return PI * envMapColor.rgb * envMapIntensity;
		#else
			return vec3( 0.0 );
		#endif
	}
	vec3 getIBLRadiance( const in vec3 viewDir, const in vec3 normal, const in float roughness ) {
		#ifdef ENVMAP_TYPE_CUBE_UV
			vec3 reflectVec = reflect( - viewDir, normal );
			reflectVec = normalize( mix( reflectVec, normal, roughness * roughness) );
			reflectVec = inverseTransformDirection( reflectVec, viewMatrix );
			vec4 envMapColor = textureCubeUV( envMap, envMapRotation * reflectVec, roughness );
			return envMapColor.rgb * envMapIntensity;
		#else
			return vec3( 0.0 );
		#endif
	}
	#ifdef USE_ANISOTROPY
		vec3 getIBLAnisotropyRadiance( const in vec3 viewDir, const in vec3 normal, const in float roughness, const in vec3 bitangent, const in float anisotropy ) {
			#ifdef ENVMAP_TYPE_CUBE_UV
				vec3 bentNormal = cross( bitangent, viewDir );
				bentNormal = normalize( cross( bentNormal, bitangent ) );
				bentNormal = normalize( mix( bentNormal, normal, pow2( pow2( 1.0 - anisotropy * ( 1.0 - roughness ) ) ) ) );
				return getIBLRadiance( viewDir, bentNormal, roughness );
			#else
				return vec3( 0.0 );
			#endif
		}
	#endif
#endif`,g1=`ToonMaterial material;
material.diffuseColor = diffuseColor.rgb;`,p1=`varying vec3 vViewPosition;
struct ToonMaterial {
	vec3 diffuseColor;
};
void RE_Direct_Toon( const in IncidentLight directLight, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in ToonMaterial material, inout ReflectedLight reflectedLight ) {
	vec3 irradiance = getGradientIrradiance( geometryNormal, directLight.direction ) * directLight.color;
	reflectedLight.directDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
void RE_IndirectDiffuse_Toon( const in vec3 irradiance, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in ToonMaterial material, inout ReflectedLight reflectedLight ) {
	reflectedLight.indirectDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
#define RE_Direct				RE_Direct_Toon
#define RE_IndirectDiffuse		RE_IndirectDiffuse_Toon`,d1=`BlinnPhongMaterial material;
material.diffuseColor = diffuseColor.rgb;
material.specularColor = specular;
material.specularShininess = shininess;
material.specularStrength = specularStrength;`,l1=`varying vec3 vViewPosition;
struct BlinnPhongMaterial {
	vec3 diffuseColor;
	vec3 specularColor;
	float specularShininess;
	float specularStrength;
};
void RE_Direct_BlinnPhong( const in IncidentLight directLight, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in BlinnPhongMaterial material, inout ReflectedLight reflectedLight ) {
	float dotNL = saturate( dot( geometryNormal, directLight.direction ) );
	vec3 irradiance = dotNL * directLight.color;
	reflectedLight.directDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
	reflectedLight.directSpecular += irradiance * BRDF_BlinnPhong( directLight.direction, geometryViewDir, geometryNormal, material.specularColor, material.specularShininess ) * material.specularStrength;
}
void RE_IndirectDiffuse_BlinnPhong( const in vec3 irradiance, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in BlinnPhongMaterial material, inout ReflectedLight reflectedLight ) {
	reflectedLight.indirectDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
#define RE_Direct				RE_Direct_BlinnPhong
#define RE_IndirectDiffuse		RE_IndirectDiffuse_BlinnPhong`,m1=`PhysicalMaterial material;
material.diffuseColor = diffuseColor.rgb * ( 1.0 - metalnessFactor );
vec3 dxy = max( abs( dFdx( nonPerturbedNormal ) ), abs( dFdy( nonPerturbedNormal ) ) );
float geometryRoughness = max( max( dxy.x, dxy.y ), dxy.z );
material.roughness = max( roughnessFactor, 0.0525 );material.roughness += geometryRoughness;
material.roughness = min( material.roughness, 1.0 );
#ifdef IOR
	material.ior = ior;
	#ifdef USE_SPECULAR
		float specularIntensityFactor = specularIntensity;
		vec3 specularColorFactor = specularColor;
		#ifdef USE_SPECULAR_COLORMAP
			specularColorFactor *= texture2D( specularColorMap, vSpecularColorMapUv ).rgb;
		#endif
		#ifdef USE_SPECULAR_INTENSITYMAP
			specularIntensityFactor *= texture2D( specularIntensityMap, vSpecularIntensityMapUv ).a;
		#endif
		material.specularF90 = mix( specularIntensityFactor, 1.0, metalnessFactor );
	#else
		float specularIntensityFactor = 1.0;
		vec3 specularColorFactor = vec3( 1.0 );
		material.specularF90 = 1.0;
	#endif
	material.specularColor = mix( min( pow2( ( material.ior - 1.0 ) / ( material.ior + 1.0 ) ) * specularColorFactor, vec3( 1.0 ) ) * specularIntensityFactor, diffuseColor.rgb, metalnessFactor );
#else
	material.specularColor = mix( vec3( 0.04 ), diffuseColor.rgb, metalnessFactor );
	material.specularF90 = 1.0;
#endif
#ifdef USE_CLEARCOAT
	material.clearcoat = clearcoat;
	material.clearcoatRoughness = clearcoatRoughness;
	material.clearcoatF0 = vec3( 0.04 );
	material.clearcoatF90 = 1.0;
	#ifdef USE_CLEARCOATMAP
		material.clearcoat *= texture2D( clearcoatMap, vClearcoatMapUv ).x;
	#endif
	#ifdef USE_CLEARCOAT_ROUGHNESSMAP
		material.clearcoatRoughness *= texture2D( clearcoatRoughnessMap, vClearcoatRoughnessMapUv ).y;
	#endif
	material.clearcoat = saturate( material.clearcoat );	material.clearcoatRoughness = max( material.clearcoatRoughness, 0.0525 );
	material.clearcoatRoughness += geometryRoughness;
	material.clearcoatRoughness = min( material.clearcoatRoughness, 1.0 );
#endif
#ifdef USE_DISPERSION
	material.dispersion = dispersion;
#endif
#ifdef USE_IRIDESCENCE
	material.iridescence = iridescence;
	material.iridescenceIOR = iridescenceIOR;
	#ifdef USE_IRIDESCENCEMAP
		material.iridescence *= texture2D( iridescenceMap, vIridescenceMapUv ).r;
	#endif
	#ifdef USE_IRIDESCENCE_THICKNESSMAP
		material.iridescenceThickness = (iridescenceThicknessMaximum - iridescenceThicknessMinimum) * texture2D( iridescenceThicknessMap, vIridescenceThicknessMapUv ).g + iridescenceThicknessMinimum;
	#else
		material.iridescenceThickness = iridescenceThicknessMaximum;
	#endif
#endif
#ifdef USE_SHEEN
	material.sheenColor = sheenColor;
	#ifdef USE_SHEEN_COLORMAP
		material.sheenColor *= texture2D( sheenColorMap, vSheenColorMapUv ).rgb;
	#endif
	material.sheenRoughness = clamp( sheenRoughness, 0.07, 1.0 );
	#ifdef USE_SHEEN_ROUGHNESSMAP
		material.sheenRoughness *= texture2D( sheenRoughnessMap, vSheenRoughnessMapUv ).a;
	#endif
#endif
#ifdef USE_ANISOTROPY
	#ifdef USE_ANISOTROPYMAP
		mat2 anisotropyMat = mat2( anisotropyVector.x, anisotropyVector.y, - anisotropyVector.y, anisotropyVector.x );
		vec3 anisotropyPolar = texture2D( anisotropyMap, vAnisotropyMapUv ).rgb;
		vec2 anisotropyV = anisotropyMat * normalize( 2.0 * anisotropyPolar.rg - vec2( 1.0 ) ) * anisotropyPolar.b;
	#else
		vec2 anisotropyV = anisotropyVector;
	#endif
	material.anisotropy = length( anisotropyV );
	if( material.anisotropy == 0.0 ) {
		anisotropyV = vec2( 1.0, 0.0 );
	} else {
		anisotropyV /= material.anisotropy;
		material.anisotropy = saturate( material.anisotropy );
	}
	material.alphaT = mix( pow2( material.roughness ), 1.0, pow2( material.anisotropy ) );
	material.anisotropyT = tbn[ 0 ] * anisotropyV.x + tbn[ 1 ] * anisotropyV.y;
	material.anisotropyB = tbn[ 1 ] * anisotropyV.x - tbn[ 0 ] * anisotropyV.y;
#endif`,u1=`struct PhysicalMaterial {
	vec3 diffuseColor;
	float roughness;
	vec3 specularColor;
	float specularF90;
	float dispersion;
	#ifdef USE_CLEARCOAT
		float clearcoat;
		float clearcoatRoughness;
		vec3 clearcoatF0;
		float clearcoatF90;
	#endif
	#ifdef USE_IRIDESCENCE
		float iridescence;
		float iridescenceIOR;
		float iridescenceThickness;
		vec3 iridescenceFresnel;
		vec3 iridescenceF0;
	#endif
	#ifdef USE_SHEEN
		vec3 sheenColor;
		float sheenRoughness;
	#endif
	#ifdef IOR
		float ior;
	#endif
	#ifdef USE_TRANSMISSION
		float transmission;
		float transmissionAlpha;
		float thickness;
		float attenuationDistance;
		vec3 attenuationColor;
	#endif
	#ifdef USE_ANISOTROPY
		float anisotropy;
		float alphaT;
		vec3 anisotropyT;
		vec3 anisotropyB;
	#endif
};
vec3 clearcoatSpecularDirect = vec3( 0.0 );
vec3 clearcoatSpecularIndirect = vec3( 0.0 );
vec3 sheenSpecularDirect = vec3( 0.0 );
vec3 sheenSpecularIndirect = vec3(0.0 );
vec3 Schlick_to_F0( const in vec3 f, const in float f90, const in float dotVH ) {
    float x = clamp( 1.0 - dotVH, 0.0, 1.0 );
    float x2 = x * x;
    float x5 = clamp( x * x2 * x2, 0.0, 0.9999 );
    return ( f - vec3( f90 ) * x5 ) / ( 1.0 - x5 );
}
float V_GGX_SmithCorrelated( const in float alpha, const in float dotNL, const in float dotNV ) {
	float a2 = pow2( alpha );
	float gv = dotNL * sqrt( a2 + ( 1.0 - a2 ) * pow2( dotNV ) );
	float gl = dotNV * sqrt( a2 + ( 1.0 - a2 ) * pow2( dotNL ) );
	return 0.5 / max( gv + gl, EPSILON );
}
float D_GGX( const in float alpha, const in float dotNH ) {
	float a2 = pow2( alpha );
	float denom = pow2( dotNH ) * ( a2 - 1.0 ) + 1.0;
	return RECIPROCAL_PI * a2 / pow2( denom );
}
#ifdef USE_ANISOTROPY
	float V_GGX_SmithCorrelated_Anisotropic( const in float alphaT, const in float alphaB, const in float dotTV, const in float dotBV, const in float dotTL, const in float dotBL, const in float dotNV, const in float dotNL ) {
		float gv = dotNL * length( vec3( alphaT * dotTV, alphaB * dotBV, dotNV ) );
		float gl = dotNV * length( vec3( alphaT * dotTL, alphaB * dotBL, dotNL ) );
		float v = 0.5 / ( gv + gl );
		return saturate(v);
	}
	float D_GGX_Anisotropic( const in float alphaT, const in float alphaB, const in float dotNH, const in float dotTH, const in float dotBH ) {
		float a2 = alphaT * alphaB;
		highp vec3 v = vec3( alphaB * dotTH, alphaT * dotBH, a2 * dotNH );
		highp float v2 = dot( v, v );
		float w2 = a2 / v2;
		return RECIPROCAL_PI * a2 * pow2 ( w2 );
	}
#endif
#ifdef USE_CLEARCOAT
	vec3 BRDF_GGX_Clearcoat( const in vec3 lightDir, const in vec3 viewDir, const in vec3 normal, const in PhysicalMaterial material) {
		vec3 f0 = material.clearcoatF0;
		float f90 = material.clearcoatF90;
		float roughness = material.clearcoatRoughness;
		float alpha = pow2( roughness );
		vec3 halfDir = normalize( lightDir + viewDir );
		float dotNL = saturate( dot( normal, lightDir ) );
		float dotNV = saturate( dot( normal, viewDir ) );
		float dotNH = saturate( dot( normal, halfDir ) );
		float dotVH = saturate( dot( viewDir, halfDir ) );
		vec3 F = F_Schlick( f0, f90, dotVH );
		float V = V_GGX_SmithCorrelated( alpha, dotNL, dotNV );
		float D = D_GGX( alpha, dotNH );
		return F * ( V * D );
	}
#endif
vec3 BRDF_GGX( const in vec3 lightDir, const in vec3 viewDir, const in vec3 normal, const in PhysicalMaterial material ) {
	vec3 f0 = material.specularColor;
	float f90 = material.specularF90;
	float roughness = material.roughness;
	float alpha = pow2( roughness );
	vec3 halfDir = normalize( lightDir + viewDir );
	float dotNL = saturate( dot( normal, lightDir ) );
	float dotNV = saturate( dot( normal, viewDir ) );
	float dotNH = saturate( dot( normal, halfDir ) );
	float dotVH = saturate( dot( viewDir, halfDir ) );
	vec3 F = F_Schlick( f0, f90, dotVH );
	#ifdef USE_IRIDESCENCE
		F = mix( F, material.iridescenceFresnel, material.iridescence );
	#endif
	#ifdef USE_ANISOTROPY
		float dotTL = dot( material.anisotropyT, lightDir );
		float dotTV = dot( material.anisotropyT, viewDir );
		float dotTH = dot( material.anisotropyT, halfDir );
		float dotBL = dot( material.anisotropyB, lightDir );
		float dotBV = dot( material.anisotropyB, viewDir );
		float dotBH = dot( material.anisotropyB, halfDir );
		float V = V_GGX_SmithCorrelated_Anisotropic( material.alphaT, alpha, dotTV, dotBV, dotTL, dotBL, dotNV, dotNL );
		float D = D_GGX_Anisotropic( material.alphaT, alpha, dotNH, dotTH, dotBH );
	#else
		float V = V_GGX_SmithCorrelated( alpha, dotNL, dotNV );
		float D = D_GGX( alpha, dotNH );
	#endif
	return F * ( V * D );
}
vec2 LTC_Uv( const in vec3 N, const in vec3 V, const in float roughness ) {
	const float LUT_SIZE = 64.0;
	const float LUT_SCALE = ( LUT_SIZE - 1.0 ) / LUT_SIZE;
	const float LUT_BIAS = 0.5 / LUT_SIZE;
	float dotNV = saturate( dot( N, V ) );
	vec2 uv = vec2( roughness, sqrt( 1.0 - dotNV ) );
	uv = uv * LUT_SCALE + LUT_BIAS;
	return uv;
}
float LTC_ClippedSphereFormFactor( const in vec3 f ) {
	float l = length( f );
	return max( ( l * l + f.z ) / ( l + 1.0 ), 0.0 );
}
vec3 LTC_EdgeVectorFormFactor( const in vec3 v1, const in vec3 v2 ) {
	float x = dot( v1, v2 );
	float y = abs( x );
	float a = 0.8543985 + ( 0.4965155 + 0.0145206 * y ) * y;
	float b = 3.4175940 + ( 4.1616724 + y ) * y;
	float v = a / b;
	float theta_sintheta = ( x > 0.0 ) ? v : 0.5 * inversesqrt( max( 1.0 - x * x, 1e-7 ) ) - v;
	return cross( v1, v2 ) * theta_sintheta;
}
vec3 LTC_Evaluate( const in vec3 N, const in vec3 V, const in vec3 P, const in mat3 mInv, const in vec3 rectCoords[ 4 ] ) {
	vec3 v1 = rectCoords[ 1 ] - rectCoords[ 0 ];
	vec3 v2 = rectCoords[ 3 ] - rectCoords[ 0 ];
	vec3 lightNormal = cross( v1, v2 );
	if( dot( lightNormal, P - rectCoords[ 0 ] ) < 0.0 ) return vec3( 0.0 );
	vec3 T1, T2;
	T1 = normalize( V - N * dot( V, N ) );
	T2 = - cross( N, T1 );
	mat3 mat = mInv * transposeMat3( mat3( T1, T2, N ) );
	vec3 coords[ 4 ];
	coords[ 0 ] = mat * ( rectCoords[ 0 ] - P );
	coords[ 1 ] = mat * ( rectCoords[ 1 ] - P );
	coords[ 2 ] = mat * ( rectCoords[ 2 ] - P );
	coords[ 3 ] = mat * ( rectCoords[ 3 ] - P );
	coords[ 0 ] = normalize( coords[ 0 ] );
	coords[ 1 ] = normalize( coords[ 1 ] );
	coords[ 2 ] = normalize( coords[ 2 ] );
	coords[ 3 ] = normalize( coords[ 3 ] );
	vec3 vectorFormFactor = vec3( 0.0 );
	vectorFormFactor += LTC_EdgeVectorFormFactor( coords[ 0 ], coords[ 1 ] );
	vectorFormFactor += LTC_EdgeVectorFormFactor( coords[ 1 ], coords[ 2 ] );
	vectorFormFactor += LTC_EdgeVectorFormFactor( coords[ 2 ], coords[ 3 ] );
	vectorFormFactor += LTC_EdgeVectorFormFactor( coords[ 3 ], coords[ 0 ] );
	float result = LTC_ClippedSphereFormFactor( vectorFormFactor );
	return vec3( result );
}
#if defined( USE_SHEEN )
float D_Charlie( float roughness, float dotNH ) {
	float alpha = pow2( roughness );
	float invAlpha = 1.0 / alpha;
	float cos2h = dotNH * dotNH;
	float sin2h = max( 1.0 - cos2h, 0.0078125 );
	return ( 2.0 + invAlpha ) * pow( sin2h, invAlpha * 0.5 ) / ( 2.0 * PI );
}
float V_Neubelt( float dotNV, float dotNL ) {
	return saturate( 1.0 / ( 4.0 * ( dotNL + dotNV - dotNL * dotNV ) ) );
}
vec3 BRDF_Sheen( const in vec3 lightDir, const in vec3 viewDir, const in vec3 normal, vec3 sheenColor, const in float sheenRoughness ) {
	vec3 halfDir = normalize( lightDir + viewDir );
	float dotNL = saturate( dot( normal, lightDir ) );
	float dotNV = saturate( dot( normal, viewDir ) );
	float dotNH = saturate( dot( normal, halfDir ) );
	float D = D_Charlie( sheenRoughness, dotNH );
	float V = V_Neubelt( dotNV, dotNL );
	return sheenColor * ( D * V );
}
#endif
float IBLSheenBRDF( const in vec3 normal, const in vec3 viewDir, const in float roughness ) {
	float dotNV = saturate( dot( normal, viewDir ) );
	float r2 = roughness * roughness;
	float a = roughness < 0.25 ? -339.2 * r2 + 161.4 * roughness - 25.9 : -8.48 * r2 + 14.3 * roughness - 9.95;
	float b = roughness < 0.25 ? 44.0 * r2 - 23.7 * roughness + 3.26 : 1.97 * r2 - 3.27 * roughness + 0.72;
	float DG = exp( a * dotNV + b ) + ( roughness < 0.25 ? 0.0 : 0.1 * ( roughness - 0.25 ) );
	return saturate( DG * RECIPROCAL_PI );
}
vec2 DFGApprox( const in vec3 normal, const in vec3 viewDir, const in float roughness ) {
	float dotNV = saturate( dot( normal, viewDir ) );
	const vec4 c0 = vec4( - 1, - 0.0275, - 0.572, 0.022 );
	const vec4 c1 = vec4( 1, 0.0425, 1.04, - 0.04 );
	vec4 r = roughness * c0 + c1;
	float a004 = min( r.x * r.x, exp2( - 9.28 * dotNV ) ) * r.x + r.y;
	vec2 fab = vec2( - 1.04, 1.04 ) * a004 + r.zw;
	return fab;
}
vec3 EnvironmentBRDF( const in vec3 normal, const in vec3 viewDir, const in vec3 specularColor, const in float specularF90, const in float roughness ) {
	vec2 fab = DFGApprox( normal, viewDir, roughness );
	return specularColor * fab.x + specularF90 * fab.y;
}
#ifdef USE_IRIDESCENCE
void computeMultiscatteringIridescence( const in vec3 normal, const in vec3 viewDir, const in vec3 specularColor, const in float specularF90, const in float iridescence, const in vec3 iridescenceF0, const in float roughness, inout vec3 singleScatter, inout vec3 multiScatter ) {
#else
void computeMultiscattering( const in vec3 normal, const in vec3 viewDir, const in vec3 specularColor, const in float specularF90, const in float roughness, inout vec3 singleScatter, inout vec3 multiScatter ) {
#endif
	vec2 fab = DFGApprox( normal, viewDir, roughness );
	#ifdef USE_IRIDESCENCE
		vec3 Fr = mix( specularColor, iridescenceF0, iridescence );
	#else
		vec3 Fr = specularColor;
	#endif
	vec3 FssEss = Fr * fab.x + specularF90 * fab.y;
	float Ess = fab.x + fab.y;
	float Ems = 1.0 - Ess;
	vec3 Favg = Fr + ( 1.0 - Fr ) * 0.047619;	vec3 Fms = FssEss * Favg / ( 1.0 - Ems * Favg );
	singleScatter += FssEss;
	multiScatter += Fms * Ems;
}
#if NUM_RECT_AREA_LIGHTS > 0
	void RE_Direct_RectArea_Physical( const in RectAreaLight rectAreaLight, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in PhysicalMaterial material, inout ReflectedLight reflectedLight ) {
		vec3 normal = geometryNormal;
		vec3 viewDir = geometryViewDir;
		vec3 position = geometryPosition;
		vec3 lightPos = rectAreaLight.position;
		vec3 halfWidth = rectAreaLight.halfWidth;
		vec3 halfHeight = rectAreaLight.halfHeight;
		vec3 lightColor = rectAreaLight.color;
		float roughness = material.roughness;
		vec3 rectCoords[ 4 ];
		rectCoords[ 0 ] = lightPos + halfWidth - halfHeight;		rectCoords[ 1 ] = lightPos - halfWidth - halfHeight;
		rectCoords[ 2 ] = lightPos - halfWidth + halfHeight;
		rectCoords[ 3 ] = lightPos + halfWidth + halfHeight;
		vec2 uv = LTC_Uv( normal, viewDir, roughness );
		vec4 t1 = texture2D( ltc_1, uv );
		vec4 t2 = texture2D( ltc_2, uv );
		mat3 mInv = mat3(
			vec3( t1.x, 0, t1.y ),
			vec3(    0, 1,    0 ),
			vec3( t1.z, 0, t1.w )
		);
		vec3 fresnel = ( material.specularColor * t2.x + ( vec3( 1.0 ) - material.specularColor ) * t2.y );
		reflectedLight.directSpecular += lightColor * fresnel * LTC_Evaluate( normal, viewDir, position, mInv, rectCoords );
		reflectedLight.directDiffuse += lightColor * material.diffuseColor * LTC_Evaluate( normal, viewDir, position, mat3( 1.0 ), rectCoords );
	}
#endif
void RE_Direct_Physical( const in IncidentLight directLight, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in PhysicalMaterial material, inout ReflectedLight reflectedLight ) {
	float dotNL = saturate( dot( geometryNormal, directLight.direction ) );
	vec3 irradiance = dotNL * directLight.color;
	#ifdef USE_CLEARCOAT
		float dotNLcc = saturate( dot( geometryClearcoatNormal, directLight.direction ) );
		vec3 ccIrradiance = dotNLcc * directLight.color;
		clearcoatSpecularDirect += ccIrradiance * BRDF_GGX_Clearcoat( directLight.direction, geometryViewDir, geometryClearcoatNormal, material );
	#endif
	#ifdef USE_SHEEN
		sheenSpecularDirect += irradiance * BRDF_Sheen( directLight.direction, geometryViewDir, geometryNormal, material.sheenColor, material.sheenRoughness );
	#endif
	reflectedLight.directSpecular += irradiance * BRDF_GGX( directLight.direction, geometryViewDir, geometryNormal, material );
	reflectedLight.directDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
void RE_IndirectDiffuse_Physical( const in vec3 irradiance, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in PhysicalMaterial material, inout ReflectedLight reflectedLight ) {
	reflectedLight.indirectDiffuse += irradiance * BRDF_Lambert( material.diffuseColor );
}
void RE_IndirectSpecular_Physical( const in vec3 radiance, const in vec3 irradiance, const in vec3 clearcoatRadiance, const in vec3 geometryPosition, const in vec3 geometryNormal, const in vec3 geometryViewDir, const in vec3 geometryClearcoatNormal, const in PhysicalMaterial material, inout ReflectedLight reflectedLight) {
	#ifdef USE_CLEARCOAT
		clearcoatSpecularIndirect += clearcoatRadiance * EnvironmentBRDF( geometryClearcoatNormal, geometryViewDir, material.clearcoatF0, material.clearcoatF90, material.clearcoatRoughness );
	#endif
	#ifdef USE_SHEEN
		sheenSpecularIndirect += irradiance * material.sheenColor * IBLSheenBRDF( geometryNormal, geometryViewDir, material.sheenRoughness );
	#endif
	vec3 singleScattering = vec3( 0.0 );
	vec3 multiScattering = vec3( 0.0 );
	vec3 cosineWeightedIrradiance = irradiance * RECIPROCAL_PI;
	#ifdef USE_IRIDESCENCE
		computeMultiscatteringIridescence( geometryNormal, geometryViewDir, material.specularColor, material.specularF90, material.iridescence, material.iridescenceFresnel, material.roughness, singleScattering, multiScattering );
	#else
		computeMultiscattering( geometryNormal, geometryViewDir, material.specularColor, material.specularF90, material.roughness, singleScattering, multiScattering );
	#endif
	vec3 totalScattering = singleScattering + multiScattering;
	vec3 diffuse = material.diffuseColor * ( 1.0 - max( max( totalScattering.r, totalScattering.g ), totalScattering.b ) );
	reflectedLight.indirectSpecular += radiance * singleScattering;
	reflectedLight.indirectSpecular += multiScattering * cosineWeightedIrradiance;
	reflectedLight.indirectDiffuse += diffuse * cosineWeightedIrradiance;
}
#define RE_Direct				RE_Direct_Physical
#define RE_Direct_RectArea		RE_Direct_RectArea_Physical
#define RE_IndirectDiffuse		RE_IndirectDiffuse_Physical
#define RE_IndirectSpecular		RE_IndirectSpecular_Physical
float computeSpecularOcclusion( const in float dotNV, const in float ambientOcclusion, const in float roughness ) {
	return saturate( pow( dotNV + ambientOcclusion, exp2( - 16.0 * roughness - 1.0 ) ) - 1.0 + ambientOcclusion );
}`,c1=`
vec3 geometryPosition = - vViewPosition;
vec3 geometryNormal = normal;
vec3 geometryViewDir = ( isOrthographic ) ? vec3( 0, 0, 1 ) : normalize( vViewPosition );
vec3 geometryClearcoatNormal = vec3( 0.0 );
#ifdef USE_CLEARCOAT
	geometryClearcoatNormal = clearcoatNormal;
#endif
#ifdef USE_IRIDESCENCE
	float dotNVi = saturate( dot( normal, geometryViewDir ) );
	if ( material.iridescenceThickness == 0.0 ) {
		material.iridescence = 0.0;
	} else {
		material.iridescence = saturate( material.iridescence );
	}
	if ( material.iridescence > 0.0 ) {
		material.iridescenceFresnel = evalIridescence( 1.0, material.iridescenceIOR, dotNVi, material.iridescenceThickness, material.specularColor );
		material.iridescenceF0 = Schlick_to_F0( material.iridescenceFresnel, 1.0, dotNVi );
	}
#endif
IncidentLight directLight;
#if ( NUM_POINT_LIGHTS > 0 ) && defined( RE_Direct )
	PointLight pointLight;
	#if defined( USE_SHADOWMAP ) && NUM_POINT_LIGHT_SHADOWS > 0
	PointLightShadow pointLightShadow;
	#endif
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_POINT_LIGHTS; i ++ ) {
		pointLight = pointLights[ i ];
		getPointLightInfo( pointLight, geometryPosition, directLight );
		#if defined( USE_SHADOWMAP ) && ( UNROLLED_LOOP_INDEX < NUM_POINT_LIGHT_SHADOWS )
		pointLightShadow = pointLightShadows[ i ];
		directLight.color *= ( directLight.visible && receiveShadow ) ? getPointShadow( pointShadowMap[ i ], pointLightShadow.shadowMapSize, pointLightShadow.shadowIntensity, pointLightShadow.shadowBias, pointLightShadow.shadowRadius, vPointShadowCoord[ i ], pointLightShadow.shadowCameraNear, pointLightShadow.shadowCameraFar ) : 1.0;
		#endif
		RE_Direct( directLight, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
	}
	#pragma unroll_loop_end
#endif
#if ( NUM_SPOT_LIGHTS > 0 ) && defined( RE_Direct )
	SpotLight spotLight;
	vec4 spotColor;
	vec3 spotLightCoord;
	bool inSpotLightMap;
	#if defined( USE_SHADOWMAP ) && NUM_SPOT_LIGHT_SHADOWS > 0
	SpotLightShadow spotLightShadow;
	#endif
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_SPOT_LIGHTS; i ++ ) {
		spotLight = spotLights[ i ];
		getSpotLightInfo( spotLight, geometryPosition, directLight );
		#if ( UNROLLED_LOOP_INDEX < NUM_SPOT_LIGHT_SHADOWS_WITH_MAPS )
		#define SPOT_LIGHT_MAP_INDEX UNROLLED_LOOP_INDEX
		#elif ( UNROLLED_LOOP_INDEX < NUM_SPOT_LIGHT_SHADOWS )
		#define SPOT_LIGHT_MAP_INDEX NUM_SPOT_LIGHT_MAPS
		#else
		#define SPOT_LIGHT_MAP_INDEX ( UNROLLED_LOOP_INDEX - NUM_SPOT_LIGHT_SHADOWS + NUM_SPOT_LIGHT_SHADOWS_WITH_MAPS )
		#endif
		#if ( SPOT_LIGHT_MAP_INDEX < NUM_SPOT_LIGHT_MAPS )
			spotLightCoord = vSpotLightCoord[ i ].xyz / vSpotLightCoord[ i ].w;
			inSpotLightMap = all( lessThan( abs( spotLightCoord * 2. - 1. ), vec3( 1.0 ) ) );
			spotColor = texture2D( spotLightMap[ SPOT_LIGHT_MAP_INDEX ], spotLightCoord.xy );
			directLight.color = inSpotLightMap ? directLight.color * spotColor.rgb : directLight.color;
		#endif
		#undef SPOT_LIGHT_MAP_INDEX
		#if defined( USE_SHADOWMAP ) && ( UNROLLED_LOOP_INDEX < NUM_SPOT_LIGHT_SHADOWS )
		spotLightShadow = spotLightShadows[ i ];
		directLight.color *= ( directLight.visible && receiveShadow ) ? getShadow( spotShadowMap[ i ], spotLightShadow.shadowMapSize, spotLightShadow.shadowIntensity, spotLightShadow.shadowBias, spotLightShadow.shadowRadius, vSpotLightCoord[ i ] ) : 1.0;
		#endif
		RE_Direct( directLight, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
	}
	#pragma unroll_loop_end
#endif
#if ( NUM_DIR_LIGHTS > 0 ) && defined( RE_Direct )
	DirectionalLight directionalLight;
	#if defined( USE_SHADOWMAP ) && NUM_DIR_LIGHT_SHADOWS > 0
	DirectionalLightShadow directionalLightShadow;
	#endif
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_DIR_LIGHTS; i ++ ) {
		directionalLight = directionalLights[ i ];
		getDirectionalLightInfo( directionalLight, directLight );
		#if defined( USE_SHADOWMAP ) && ( UNROLLED_LOOP_INDEX < NUM_DIR_LIGHT_SHADOWS )
		directionalLightShadow = directionalLightShadows[ i ];
		directLight.color *= ( directLight.visible && receiveShadow ) ? getShadow( directionalShadowMap[ i ], directionalLightShadow.shadowMapSize, directionalLightShadow.shadowIntensity, directionalLightShadow.shadowBias, directionalLightShadow.shadowRadius, vDirectionalShadowCoord[ i ] ) : 1.0;
		#endif
		RE_Direct( directLight, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
	}
	#pragma unroll_loop_end
#endif
#if ( NUM_RECT_AREA_LIGHTS > 0 ) && defined( RE_Direct_RectArea )
	RectAreaLight rectAreaLight;
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_RECT_AREA_LIGHTS; i ++ ) {
		rectAreaLight = rectAreaLights[ i ];
		RE_Direct_RectArea( rectAreaLight, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
	}
	#pragma unroll_loop_end
#endif
#if defined( RE_IndirectDiffuse )
	vec3 iblIrradiance = vec3( 0.0 );
	vec3 irradiance = getAmbientLightIrradiance( ambientLightColor );
	#if defined( USE_LIGHT_PROBES )
		irradiance += getLightProbeIrradiance( lightProbe, geometryNormal );
	#endif
	#if ( NUM_HEMI_LIGHTS > 0 )
		#pragma unroll_loop_start
		for ( int i = 0; i < NUM_HEMI_LIGHTS; i ++ ) {
			irradiance += getHemisphereLightIrradiance( hemisphereLights[ i ], geometryNormal );
		}
		#pragma unroll_loop_end
	#endif
#endif
#if defined( RE_IndirectSpecular )
	vec3 radiance = vec3( 0.0 );
	vec3 clearcoatRadiance = vec3( 0.0 );
#endif`,n1=`#if defined( RE_IndirectDiffuse )
	#ifdef USE_LIGHTMAP
		vec4 lightMapTexel = texture2D( lightMap, vLightMapUv );
		vec3 lightMapIrradiance = lightMapTexel.rgb * lightMapIntensity;
		irradiance += lightMapIrradiance;
	#endif
	#if defined( USE_ENVMAP ) && defined( STANDARD ) && defined( ENVMAP_TYPE_CUBE_UV )
		iblIrradiance += getIBLIrradiance( geometryNormal );
	#endif
#endif
#if defined( USE_ENVMAP ) && defined( RE_IndirectSpecular )
	#ifdef USE_ANISOTROPY
		radiance += getIBLAnisotropyRadiance( geometryViewDir, geometryNormal, material.roughness, material.anisotropyB, material.anisotropy );
	#else
		radiance += getIBLRadiance( geometryViewDir, geometryNormal, material.roughness );
	#endif
	#ifdef USE_CLEARCOAT
		clearcoatRadiance += getIBLRadiance( geometryViewDir, geometryClearcoatNormal, material.clearcoatRoughness );
	#endif
#endif`,s1=`#if defined( RE_IndirectDiffuse )
	RE_IndirectDiffuse( irradiance, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
#endif
#if defined( RE_IndirectSpecular )
	RE_IndirectSpecular( radiance, iblIrradiance, clearcoatRadiance, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
#endif`,i1=`#if defined( USE_LOGARITHMIC_DEPTH_BUFFER )
	gl_FragDepth = vIsPerspective == 0.0 ? gl_FragCoord.z : log2( vFragDepth ) * logDepthBufFC * 0.5;
#endif`,o1=`#if defined( USE_LOGARITHMIC_DEPTH_BUFFER )
	uniform float logDepthBufFC;
	varying float vFragDepth;
	varying float vIsPerspective;
#endif`,a1=`#ifdef USE_LOGARITHMIC_DEPTH_BUFFER
	varying float vFragDepth;
	varying float vIsPerspective;
#endif`,r1=`#ifdef USE_LOGARITHMIC_DEPTH_BUFFER
	vFragDepth = 1.0 + gl_Position.w;
	vIsPerspective = float( isPerspectiveMatrix( projectionMatrix ) );
#endif`,t1=`#ifdef USE_MAP
	vec4 sampledDiffuseColor = texture2D( map, vMapUv );
	#ifdef DECODE_VIDEO_TEXTURE
		sampledDiffuseColor = sRGBTransferEOTF( sampledDiffuseColor );
	#endif
	diffuseColor *= sampledDiffuseColor;
#endif`,e1=`#ifdef USE_MAP
	uniform sampler2D map;
#endif`,JK=`#if defined( USE_MAP ) || defined( USE_ALPHAMAP )
	#if defined( USE_POINTS_UV )
		vec2 uv = vUv;
	#else
		vec2 uv = ( uvTransform * vec3( gl_PointCoord.x, 1.0 - gl_PointCoord.y, 1 ) ).xy;
	#endif
#endif
#ifdef USE_MAP
	diffuseColor *= texture2D( map, uv );
#endif
#ifdef USE_ALPHAMAP
	diffuseColor.a *= texture2D( alphaMap, uv ).g;
#endif`,$K=`#if defined( USE_POINTS_UV )
	varying vec2 vUv;
#else
	#if defined( USE_MAP ) || defined( USE_ALPHAMAP )
		uniform mat3 uvTransform;
	#endif
#endif
#ifdef USE_MAP
	uniform sampler2D map;
#endif
#ifdef USE_ALPHAMAP
	uniform sampler2D alphaMap;
#endif`,NK=`float metalnessFactor = metalness;
#ifdef USE_METALNESSMAP
	vec4 texelMetalness = texture2D( metalnessMap, vMetalnessMapUv );
	metalnessFactor *= texelMetalness.b;
#endif`,QK=`#ifdef USE_METALNESSMAP
	uniform sampler2D metalnessMap;
#endif`,ZK=`#ifdef USE_INSTANCING_MORPH
	float morphTargetInfluences[ MORPHTARGETS_COUNT ];
	float morphTargetBaseInfluence = texelFetch( morphTexture, ivec2( 0, gl_InstanceID ), 0 ).r;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		morphTargetInfluences[i] =  texelFetch( morphTexture, ivec2( i + 1, gl_InstanceID ), 0 ).r;
	}
#endif`,WK=`#if defined( USE_MORPHCOLORS )
	vColor *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		#if defined( USE_COLOR_ALPHA )
			if ( morphTargetInfluences[ i ] != 0.0 ) vColor += getMorph( gl_VertexID, i, 2 ) * morphTargetInfluences[ i ];
		#elif defined( USE_COLOR )
			if ( morphTargetInfluences[ i ] != 0.0 ) vColor += getMorph( gl_VertexID, i, 2 ).rgb * morphTargetInfluences[ i ];
		#endif
	}
#endif`,KK=`#ifdef USE_MORPHNORMALS
	objectNormal *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		if ( morphTargetInfluences[ i ] != 0.0 ) objectNormal += getMorph( gl_VertexID, i, 1 ).xyz * morphTargetInfluences[ i ];
	}
#endif`,YK=`#ifdef USE_MORPHTARGETS
	#ifndef USE_INSTANCING_MORPH
		uniform float morphTargetBaseInfluence;
		uniform float morphTargetInfluences[ MORPHTARGETS_COUNT ];
	#endif
	uniform sampler2DArray morphTargetsTexture;
	uniform ivec2 morphTargetsTextureSize;
	vec4 getMorph( const in int vertexIndex, const in int morphTargetIndex, const in int offset ) {
		int texelIndex = vertexIndex * MORPHTARGETS_TEXTURE_STRIDE + offset;
		int y = texelIndex / morphTargetsTextureSize.x;
		int x = texelIndex - y * morphTargetsTextureSize.x;
		ivec3 morphUV = ivec3( x, y, morphTargetIndex );
		return texelFetch( morphTargetsTexture, morphUV, 0 );
	}
#endif`,HK=`#ifdef USE_MORPHTARGETS
	transformed *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		if ( morphTargetInfluences[ i ] != 0.0 ) transformed += getMorph( gl_VertexID, i, 0 ).xyz * morphTargetInfluences[ i ];
	}
#endif`,XK=`float faceDirection = gl_FrontFacing ? 1.0 : - 1.0;
#ifdef FLAT_SHADED
	vec3 fdx = dFdx( vViewPosition );
	vec3 fdy = dFdy( vViewPosition );
	vec3 normal = normalize( cross( fdx, fdy ) );
#else
	vec3 normal = normalize( vNormal );
	#ifdef DOUBLE_SIDED
		normal *= faceDirection;
	#endif
#endif
#if defined( USE_NORMALMAP_TANGENTSPACE ) || defined( USE_CLEARCOAT_NORMALMAP ) || defined( USE_ANISOTROPY )
	#ifdef USE_TANGENT
		mat3 tbn = mat3( normalize( vTangent ), normalize( vBitangent ), normal );
	#else
		mat3 tbn = getTangentFrame( - vViewPosition, normal,
		#if defined( USE_NORMALMAP )
			vNormalMapUv
		#elif defined( USE_CLEARCOAT_NORMALMAP )
			vClearcoatNormalMapUv
		#else
			vUv
		#endif
		);
	#endif
	#if defined( DOUBLE_SIDED ) && ! defined( FLAT_SHADED )
		tbn[0] *= faceDirection;
		tbn[1] *= faceDirection;
	#endif
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	#ifdef USE_TANGENT
		mat3 tbn2 = mat3( normalize( vTangent ), normalize( vBitangent ), normal );
	#else
		mat3 tbn2 = getTangentFrame( - vViewPosition, normal, vClearcoatNormalMapUv );
	#endif
	#if defined( DOUBLE_SIDED ) && ! defined( FLAT_SHADED )
		tbn2[0] *= faceDirection;
		tbn2[1] *= faceDirection;
	#endif
#endif
vec3 nonPerturbedNormal = normal;`,UK=`#ifdef USE_NORMALMAP_OBJECTSPACE
	normal = texture2D( normalMap, vNormalMapUv ).xyz * 2.0 - 1.0;
	#ifdef FLIP_SIDED
		normal = - normal;
	#endif
	#ifdef DOUBLE_SIDED
		normal = normal * faceDirection;
	#endif
	normal = normalize( normalMatrix * normal );
#elif defined( USE_NORMALMAP_TANGENTSPACE )
	vec3 mapN = texture2D( normalMap, vNormalMapUv ).xyz * 2.0 - 1.0;
	mapN.xy *= normalScale;
	normal = normalize( tbn * mapN );
#elif defined( USE_BUMPMAP )
	normal = perturbNormalArb( - vViewPosition, normal, dHdxy_fwd(), faceDirection );
#endif`,GK=`#ifndef FLAT_SHADED
	varying vec3 vNormal;
	#ifdef USE_TANGENT
		varying vec3 vTangent;
		varying vec3 vBitangent;
	#endif
#endif`,EK=`#ifndef FLAT_SHADED
	varying vec3 vNormal;
	#ifdef USE_TANGENT
		varying vec3 vTangent;
		varying vec3 vBitangent;
	#endif
#endif`,DK=`#ifndef FLAT_SHADED
	vNormal = normalize( transformedNormal );
	#ifdef USE_TANGENT
		vTangent = normalize( transformedTangent );
		vBitangent = normalize( cross( vNormal, vTangent ) * tangent.w );
	#endif
#endif`,qK=`#ifdef USE_NORMALMAP
	uniform sampler2D normalMap;
	uniform vec2 normalScale;
#endif
#ifdef USE_NORMALMAP_OBJECTSPACE
	uniform mat3 normalMatrix;
#endif
#if ! defined ( USE_TANGENT ) && ( defined ( USE_NORMALMAP_TANGENTSPACE ) || defined ( USE_CLEARCOAT_NORMALMAP ) || defined( USE_ANISOTROPY ) )
	mat3 getTangentFrame( vec3 eye_pos, vec3 surf_norm, vec2 uv ) {
		vec3 q0 = dFdx( eye_pos.xyz );
		vec3 q1 = dFdy( eye_pos.xyz );
		vec2 st0 = dFdx( uv.st );
		vec2 st1 = dFdy( uv.st );
		vec3 N = surf_norm;
		vec3 q1perp = cross( q1, N );
		vec3 q0perp = cross( N, q0 );
		vec3 T = q1perp * st0.x + q0perp * st1.x;
		vec3 B = q1perp * st0.y + q0perp * st1.y;
		float det = max( dot( T, T ), dot( B, B ) );
		float scale = ( det == 0.0 ) ? 0.0 : inversesqrt( det );
		return mat3( T * scale, B * scale, N );
	}
#endif`,kK=`#ifdef USE_CLEARCOAT
	vec3 clearcoatNormal = nonPerturbedNormal;
#endif`,OK=`#ifdef USE_CLEARCOAT_NORMALMAP
	vec3 clearcoatMapN = texture2D( clearcoatNormalMap, vClearcoatNormalMapUv ).xyz * 2.0 - 1.0;
	clearcoatMapN.xy *= clearcoatNormalScale;
	clearcoatNormal = normalize( tbn2 * clearcoatMapN );
#endif`,RK=`#ifdef USE_CLEARCOATMAP
	uniform sampler2D clearcoatMap;
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	uniform sampler2D clearcoatNormalMap;
	uniform vec2 clearcoatNormalScale;
#endif
#ifdef USE_CLEARCOAT_ROUGHNESSMAP
	uniform sampler2D clearcoatRoughnessMap;
#endif`,FK=`#ifdef USE_IRIDESCENCEMAP
	uniform sampler2D iridescenceMap;
#endif
#ifdef USE_IRIDESCENCE_THICKNESSMAP
	uniform sampler2D iridescenceThicknessMap;
#endif`,MK=`#ifdef OPAQUE
diffuseColor.a = 1.0;
#endif
#ifdef USE_TRANSMISSION
diffuseColor.a *= material.transmissionAlpha;
#endif
gl_FragColor = vec4( outgoingLight, diffuseColor.a );`,_K=`vec3 packNormalToRGB( const in vec3 normal ) {
	return normalize( normal ) * 0.5 + 0.5;
}
vec3 unpackRGBToNormal( const in vec3 rgb ) {
	return 2.0 * rgb.xyz - 1.0;
}
const float PackUpscale = 256. / 255.;const float UnpackDownscale = 255. / 256.;const float ShiftRight8 = 1. / 256.;
const float Inv255 = 1. / 255.;
const vec4 PackFactors = vec4( 1.0, 256.0, 256.0 * 256.0, 256.0 * 256.0 * 256.0 );
const vec2 UnpackFactors2 = vec2( UnpackDownscale, 1.0 / PackFactors.g );
const vec3 UnpackFactors3 = vec3( UnpackDownscale / PackFactors.rg, 1.0 / PackFactors.b );
const vec4 UnpackFactors4 = vec4( UnpackDownscale / PackFactors.rgb, 1.0 / PackFactors.a );
vec4 packDepthToRGBA( const in float v ) {
	if( v <= 0.0 )
		return vec4( 0., 0., 0., 0. );
	if( v >= 1.0 )
		return vec4( 1., 1., 1., 1. );
	float vuf;
	float af = modf( v * PackFactors.a, vuf );
	float bf = modf( vuf * ShiftRight8, vuf );
	float gf = modf( vuf * ShiftRight8, vuf );
	return vec4( vuf * Inv255, gf * PackUpscale, bf * PackUpscale, af );
}
vec3 packDepthToRGB( const in float v ) {
	if( v <= 0.0 )
		return vec3( 0., 0., 0. );
	if( v >= 1.0 )
		return vec3( 1., 1., 1. );
	float vuf;
	float bf = modf( v * PackFactors.b, vuf );
	float gf = modf( vuf * ShiftRight8, vuf );
	return vec3( vuf * Inv255, gf * PackUpscale, bf );
}
vec2 packDepthToRG( const in float v ) {
	if( v <= 0.0 )
		return vec2( 0., 0. );
	if( v >= 1.0 )
		return vec2( 1., 1. );
	float vuf;
	float gf = modf( v * 256., vuf );
	return vec2( vuf * Inv255, gf );
}
float unpackRGBAToDepth( const in vec4 v ) {
	return dot( v, UnpackFactors4 );
}
float unpackRGBToDepth( const in vec3 v ) {
	return dot( v, UnpackFactors3 );
}
float unpackRGToDepth( const in vec2 v ) {
	return v.r * UnpackFactors2.r + v.g * UnpackFactors2.g;
}
vec4 pack2HalfToRGBA( const in vec2 v ) {
	vec4 r = vec4( v.x, fract( v.x * 255.0 ), v.y, fract( v.y * 255.0 ) );
	return vec4( r.x - r.y / 255.0, r.y, r.z - r.w / 255.0, r.w );
}
vec2 unpackRGBATo2Half( const in vec4 v ) {
	return vec2( v.x + ( v.y / 255.0 ), v.z + ( v.w / 255.0 ) );
}
float viewZToOrthographicDepth( const in float viewZ, const in float near, const in float far ) {
	return ( viewZ + near ) / ( near - far );
}
float orthographicDepthToViewZ( const in float depth, const in float near, const in float far ) {
	return depth * ( near - far ) - near;
}
float viewZToPerspectiveDepth( const in float viewZ, const in float near, const in float far ) {
	return ( ( near + viewZ ) * far ) / ( ( far - near ) * viewZ );
}
float perspectiveDepthToViewZ( const in float depth, const in float near, const in float far ) {
	return ( near * far ) / ( ( far - near ) * depth - far );
}`,LK=`#ifdef PREMULTIPLIED_ALPHA
	gl_FragColor.rgb *= gl_FragColor.a;
#endif`,VK=`vec4 mvPosition = vec4( transformed, 1.0 );
#ifdef USE_BATCHING
	mvPosition = batchingMatrix * mvPosition;
#endif
#ifdef USE_INSTANCING
	mvPosition = instanceMatrix * mvPosition;
#endif
mvPosition = modelViewMatrix * mvPosition;
gl_Position = projectionMatrix * mvPosition;`,wK=`#ifdef DITHERING
	gl_FragColor.rgb = dithering( gl_FragColor.rgb );
#endif`,zK=`#ifdef DITHERING
	vec3 dithering( vec3 color ) {
		float grid_position = rand( gl_FragCoord.xy );
		vec3 dither_shift_RGB = vec3( 0.25 / 255.0, -0.25 / 255.0, 0.25 / 255.0 );
		dither_shift_RGB = mix( 2.0 * dither_shift_RGB, -2.0 * dither_shift_RGB, grid_position );
		return color + dither_shift_RGB;
	}
#endif`,BK=`float roughnessFactor = roughness;
#ifdef USE_ROUGHNESSMAP
	vec4 texelRoughness = texture2D( roughnessMap, vRoughnessMapUv );
	roughnessFactor *= texelRoughness.g;
#endif`,CK=`#ifdef USE_ROUGHNESSMAP
	uniform sampler2D roughnessMap;
#endif`,IK=`#if NUM_SPOT_LIGHT_COORDS > 0
	varying vec4 vSpotLightCoord[ NUM_SPOT_LIGHT_COORDS ];
#endif
#if NUM_SPOT_LIGHT_MAPS > 0
	uniform sampler2D spotLightMap[ NUM_SPOT_LIGHT_MAPS ];
#endif
#ifdef USE_SHADOWMAP
	#if NUM_DIR_LIGHT_SHADOWS > 0
		uniform sampler2D directionalShadowMap[ NUM_DIR_LIGHT_SHADOWS ];
		varying vec4 vDirectionalShadowCoord[ NUM_DIR_LIGHT_SHADOWS ];
		struct DirectionalLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
		};
		uniform DirectionalLightShadow directionalLightShadows[ NUM_DIR_LIGHT_SHADOWS ];
	#endif
	#if NUM_SPOT_LIGHT_SHADOWS > 0
		uniform sampler2D spotShadowMap[ NUM_SPOT_LIGHT_SHADOWS ];
		struct SpotLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
		};
		uniform SpotLightShadow spotLightShadows[ NUM_SPOT_LIGHT_SHADOWS ];
	#endif
	#if NUM_POINT_LIGHT_SHADOWS > 0
		uniform sampler2D pointShadowMap[ NUM_POINT_LIGHT_SHADOWS ];
		varying vec4 vPointShadowCoord[ NUM_POINT_LIGHT_SHADOWS ];
		struct PointLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
			float shadowCameraNear;
			float shadowCameraFar;
		};
		uniform PointLightShadow pointLightShadows[ NUM_POINT_LIGHT_SHADOWS ];
	#endif
	float texture2DCompare( sampler2D depths, vec2 uv, float compare ) {
		float depth = unpackRGBAToDepth( texture2D( depths, uv ) );
		#ifdef USE_REVERSED_DEPTH_BUFFER
			return step( depth, compare );
		#else
			return step( compare, depth );
		#endif
	}
	vec2 texture2DDistribution( sampler2D shadow, vec2 uv ) {
		return unpackRGBATo2Half( texture2D( shadow, uv ) );
	}
	float VSMShadow( sampler2D shadow, vec2 uv, float compare ) {
		float occlusion = 1.0;
		vec2 distribution = texture2DDistribution( shadow, uv );
		#ifdef USE_REVERSED_DEPTH_BUFFER
			float hard_shadow = step( distribution.x, compare );
		#else
			float hard_shadow = step( compare, distribution.x );
		#endif
		if ( hard_shadow != 1.0 ) {
			float distance = compare - distribution.x;
			float variance = max( 0.00000, distribution.y * distribution.y );
			float softness_probability = variance / (variance + distance * distance );			softness_probability = clamp( ( softness_probability - 0.3 ) / ( 0.95 - 0.3 ), 0.0, 1.0 );			occlusion = clamp( max( hard_shadow, softness_probability ), 0.0, 1.0 );
		}
		return occlusion;
	}
	float getShadow( sampler2D shadowMap, vec2 shadowMapSize, float shadowIntensity, float shadowBias, float shadowRadius, vec4 shadowCoord ) {
		float shadow = 1.0;
		shadowCoord.xyz /= shadowCoord.w;
		shadowCoord.z += shadowBias;
		bool inFrustum = shadowCoord.x >= 0.0 && shadowCoord.x <= 1.0 && shadowCoord.y >= 0.0 && shadowCoord.y <= 1.0;
		bool frustumTest = inFrustum && shadowCoord.z <= 1.0;
		if ( frustumTest ) {
		#if defined( SHADOWMAP_TYPE_PCF )
			vec2 texelSize = vec2( 1.0 ) / shadowMapSize;
			float dx0 = - texelSize.x * shadowRadius;
			float dy0 = - texelSize.y * shadowRadius;
			float dx1 = + texelSize.x * shadowRadius;
			float dy1 = + texelSize.y * shadowRadius;
			float dx2 = dx0 / 2.0;
			float dy2 = dy0 / 2.0;
			float dx3 = dx1 / 2.0;
			float dy3 = dy1 / 2.0;
			shadow = (
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx0, dy0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( 0.0, dy0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx1, dy0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx2, dy2 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( 0.0, dy2 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx3, dy2 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx0, 0.0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx2, 0.0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy, shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx3, 0.0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx1, 0.0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx2, dy3 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( 0.0, dy3 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx3, dy3 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx0, dy1 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( 0.0, dy1 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, shadowCoord.xy + vec2( dx1, dy1 ), shadowCoord.z )
			) * ( 1.0 / 17.0 );
		#elif defined( SHADOWMAP_TYPE_PCF_SOFT )
			vec2 texelSize = vec2( 1.0 ) / shadowMapSize;
			float dx = texelSize.x;
			float dy = texelSize.y;
			vec2 uv = shadowCoord.xy;
			vec2 f = fract( uv * shadowMapSize + 0.5 );
			uv -= f * texelSize;
			shadow = (
				texture2DCompare( shadowMap, uv, shadowCoord.z ) +
				texture2DCompare( shadowMap, uv + vec2( dx, 0.0 ), shadowCoord.z ) +
				texture2DCompare( shadowMap, uv + vec2( 0.0, dy ), shadowCoord.z ) +
				texture2DCompare( shadowMap, uv + texelSize, shadowCoord.z ) +
				mix( texture2DCompare( shadowMap, uv + vec2( -dx, 0.0 ), shadowCoord.z ),
					 texture2DCompare( shadowMap, uv + vec2( 2.0 * dx, 0.0 ), shadowCoord.z ),
					 f.x ) +
				mix( texture2DCompare( shadowMap, uv + vec2( -dx, dy ), shadowCoord.z ),
					 texture2DCompare( shadowMap, uv + vec2( 2.0 * dx, dy ), shadowCoord.z ),
					 f.x ) +
				mix( texture2DCompare( shadowMap, uv + vec2( 0.0, -dy ), shadowCoord.z ),
					 texture2DCompare( shadowMap, uv + vec2( 0.0, 2.0 * dy ), shadowCoord.z ),
					 f.y ) +
				mix( texture2DCompare( shadowMap, uv + vec2( dx, -dy ), shadowCoord.z ),
					 texture2DCompare( shadowMap, uv + vec2( dx, 2.0 * dy ), shadowCoord.z ),
					 f.y ) +
				mix( mix( texture2DCompare( shadowMap, uv + vec2( -dx, -dy ), shadowCoord.z ),
						  texture2DCompare( shadowMap, uv + vec2( 2.0 * dx, -dy ), shadowCoord.z ),
						  f.x ),
					 mix( texture2DCompare( shadowMap, uv + vec2( -dx, 2.0 * dy ), shadowCoord.z ),
						  texture2DCompare( shadowMap, uv + vec2( 2.0 * dx, 2.0 * dy ), shadowCoord.z ),
						  f.x ),
					 f.y )
			) * ( 1.0 / 9.0 );
		#elif defined( SHADOWMAP_TYPE_VSM )
			shadow = VSMShadow( shadowMap, shadowCoord.xy, shadowCoord.z );
		#else
			shadow = texture2DCompare( shadowMap, shadowCoord.xy, shadowCoord.z );
		#endif
		}
		return mix( 1.0, shadow, shadowIntensity );
	}
	vec2 cubeToUV( vec3 v, float texelSizeY ) {
		vec3 absV = abs( v );
		float scaleToCube = 1.0 / max( absV.x, max( absV.y, absV.z ) );
		absV *= scaleToCube;
		v *= scaleToCube * ( 1.0 - 2.0 * texelSizeY );
		vec2 planar = v.xy;
		float almostATexel = 1.5 * texelSizeY;
		float almostOne = 1.0 - almostATexel;
		if ( absV.z >= almostOne ) {
			if ( v.z > 0.0 )
				planar.x = 4.0 - v.x;
		} else if ( absV.x >= almostOne ) {
			float signX = sign( v.x );
			planar.x = v.z * signX + 2.0 * signX;
		} else if ( absV.y >= almostOne ) {
			float signY = sign( v.y );
			planar.x = v.x + 2.0 * signY + 2.0;
			planar.y = v.z * signY - 2.0;
		}
		return vec2( 0.125, 0.25 ) * planar + vec2( 0.375, 0.75 );
	}
	float getPointShadow( sampler2D shadowMap, vec2 shadowMapSize, float shadowIntensity, float shadowBias, float shadowRadius, vec4 shadowCoord, float shadowCameraNear, float shadowCameraFar ) {
		float shadow = 1.0;
		vec3 lightToPosition = shadowCoord.xyz;
		
		float lightToPositionLength = length( lightToPosition );
		if ( lightToPositionLength - shadowCameraFar <= 0.0 && lightToPositionLength - shadowCameraNear >= 0.0 ) {
			float dp = ( lightToPositionLength - shadowCameraNear ) / ( shadowCameraFar - shadowCameraNear );			dp += shadowBias;
			vec3 bd3D = normalize( lightToPosition );
			vec2 texelSize = vec2( 1.0 ) / ( shadowMapSize * vec2( 4.0, 2.0 ) );
			#if defined( SHADOWMAP_TYPE_PCF ) || defined( SHADOWMAP_TYPE_PCF_SOFT ) || defined( SHADOWMAP_TYPE_VSM )
				vec2 offset = vec2( - 1, 1 ) * shadowRadius * texelSize.y;
				shadow = (
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.xyy, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.yyy, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.xyx, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.yyx, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.xxy, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.yxy, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.xxx, texelSize.y ), dp ) +
					texture2DCompare( shadowMap, cubeToUV( bd3D + offset.yxx, texelSize.y ), dp )
				) * ( 1.0 / 9.0 );
			#else
				shadow = texture2DCompare( shadowMap, cubeToUV( bd3D, texelSize.y ), dp );
			#endif
		}
		return mix( 1.0, shadow, shadowIntensity );
	}
#endif`,PK=`#if NUM_SPOT_LIGHT_COORDS > 0
	uniform mat4 spotLightMatrix[ NUM_SPOT_LIGHT_COORDS ];
	varying vec4 vSpotLightCoord[ NUM_SPOT_LIGHT_COORDS ];
#endif
#ifdef USE_SHADOWMAP
	#if NUM_DIR_LIGHT_SHADOWS > 0
		uniform mat4 directionalShadowMatrix[ NUM_DIR_LIGHT_SHADOWS ];
		varying vec4 vDirectionalShadowCoord[ NUM_DIR_LIGHT_SHADOWS ];
		struct DirectionalLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
		};
		uniform DirectionalLightShadow directionalLightShadows[ NUM_DIR_LIGHT_SHADOWS ];
	#endif
	#if NUM_SPOT_LIGHT_SHADOWS > 0
		struct SpotLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
		};
		uniform SpotLightShadow spotLightShadows[ NUM_SPOT_LIGHT_SHADOWS ];
	#endif
	#if NUM_POINT_LIGHT_SHADOWS > 0
		uniform mat4 pointShadowMatrix[ NUM_POINT_LIGHT_SHADOWS ];
		varying vec4 vPointShadowCoord[ NUM_POINT_LIGHT_SHADOWS ];
		struct PointLightShadow {
			float shadowIntensity;
			float shadowBias;
			float shadowNormalBias;
			float shadowRadius;
			vec2 shadowMapSize;
			float shadowCameraNear;
			float shadowCameraFar;
		};
		uniform PointLightShadow pointLightShadows[ NUM_POINT_LIGHT_SHADOWS ];
	#endif
#endif`,AK=`#if ( defined( USE_SHADOWMAP ) && ( NUM_DIR_LIGHT_SHADOWS > 0 || NUM_POINT_LIGHT_SHADOWS > 0 ) ) || ( NUM_SPOT_LIGHT_COORDS > 0 )
	vec3 shadowWorldNormal = inverseTransformDirection( transformedNormal, viewMatrix );
	vec4 shadowWorldPosition;
#endif
#if defined( USE_SHADOWMAP )
	#if NUM_DIR_LIGHT_SHADOWS > 0
		#pragma unroll_loop_start
		for ( int i = 0; i < NUM_DIR_LIGHT_SHADOWS; i ++ ) {
			shadowWorldPosition = worldPosition + vec4( shadowWorldNormal * directionalLightShadows[ i ].shadowNormalBias, 0 );
			vDirectionalShadowCoord[ i ] = directionalShadowMatrix[ i ] * shadowWorldPosition;
		}
		#pragma unroll_loop_end
	#endif
	#if NUM_POINT_LIGHT_SHADOWS > 0
		#pragma unroll_loop_start
		for ( int i = 0; i < NUM_POINT_LIGHT_SHADOWS; i ++ ) {
			shadowWorldPosition = worldPosition + vec4( shadowWorldNormal * pointLightShadows[ i ].shadowNormalBias, 0 );
			vPointShadowCoord[ i ] = pointShadowMatrix[ i ] * shadowWorldPosition;
		}
		#pragma unroll_loop_end
	#endif
#endif
#if NUM_SPOT_LIGHT_COORDS > 0
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_SPOT_LIGHT_COORDS; i ++ ) {
		shadowWorldPosition = worldPosition;
		#if ( defined( USE_SHADOWMAP ) && UNROLLED_LOOP_INDEX < NUM_SPOT_LIGHT_SHADOWS )
			shadowWorldPosition.xyz += shadowWorldNormal * spotLightShadows[ i ].shadowNormalBias;
		#endif
		vSpotLightCoord[ i ] = spotLightMatrix[ i ] * shadowWorldPosition;
	}
	#pragma unroll_loop_end
#endif`,TK=`float getShadowMask() {
	float shadow = 1.0;
	#ifdef USE_SHADOWMAP
	#if NUM_DIR_LIGHT_SHADOWS > 0
	DirectionalLightShadow directionalLight;
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_DIR_LIGHT_SHADOWS; i ++ ) {
		directionalLight = directionalLightShadows[ i ];
		shadow *= receiveShadow ? getShadow( directionalShadowMap[ i ], directionalLight.shadowMapSize, directionalLight.shadowIntensity, directionalLight.shadowBias, directionalLight.shadowRadius, vDirectionalShadowCoord[ i ] ) : 1.0;
	}
	#pragma unroll_loop_end
	#endif
	#if NUM_SPOT_LIGHT_SHADOWS > 0
	SpotLightShadow spotLight;
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_SPOT_LIGHT_SHADOWS; i ++ ) {
		spotLight = spotLightShadows[ i ];
		shadow *= receiveShadow ? getShadow( spotShadowMap[ i ], spotLight.shadowMapSize, spotLight.shadowIntensity, spotLight.shadowBias, spotLight.shadowRadius, vSpotLightCoord[ i ] ) : 1.0;
	}
	#pragma unroll_loop_end
	#endif
	#if NUM_POINT_LIGHT_SHADOWS > 0
	PointLightShadow pointLight;
	#pragma unroll_loop_start
	for ( int i = 0; i < NUM_POINT_LIGHT_SHADOWS; i ++ ) {
		pointLight = pointLightShadows[ i ];
		shadow *= receiveShadow ? getPointShadow( pointShadowMap[ i ], pointLight.shadowMapSize, pointLight.shadowIntensity, pointLight.shadowBias, pointLight.shadowRadius, vPointShadowCoord[ i ], pointLight.shadowCameraNear, pointLight.shadowCameraFar ) : 1.0;
	}
	#pragma unroll_loop_end
	#endif
	#endif
	return shadow;
}`,SK=`#ifdef USE_SKINNING
	mat4 boneMatX = getBoneMatrix( skinIndex.x );
	mat4 boneMatY = getBoneMatrix( skinIndex.y );
	mat4 boneMatZ = getBoneMatrix( skinIndex.z );
	mat4 boneMatW = getBoneMatrix( skinIndex.w );
#endif`,bK=`#ifdef USE_SKINNING
	uniform mat4 bindMatrix;
	uniform mat4 bindMatrixInverse;
	uniform highp sampler2D boneTexture;
	mat4 getBoneMatrix( const in float i ) {
		int size = textureSize( boneTexture, 0 ).x;
		int j = int( i ) * 4;
		int x = j % size;
		int y = j / size;
		vec4 v1 = texelFetch( boneTexture, ivec2( x, y ), 0 );
		vec4 v2 = texelFetch( boneTexture, ivec2( x + 1, y ), 0 );
		vec4 v3 = texelFetch( boneTexture, ivec2( x + 2, y ), 0 );
		vec4 v4 = texelFetch( boneTexture, ivec2( x + 3, y ), 0 );
		return mat4( v1, v2, v3, v4 );
	}
#endif`,yK=`#ifdef USE_SKINNING
	vec4 skinVertex = bindMatrix * vec4( transformed, 1.0 );
	vec4 skinned = vec4( 0.0 );
	skinned += boneMatX * skinVertex * skinWeight.x;
	skinned += boneMatY * skinVertex * skinWeight.y;
	skinned += boneMatZ * skinVertex * skinWeight.z;
	skinned += boneMatW * skinVertex * skinWeight.w;
	transformed = ( bindMatrixInverse * skinned ).xyz;
#endif`,jK=`#ifdef USE_SKINNING
	mat4 skinMatrix = mat4( 0.0 );
	skinMatrix += skinWeight.x * boneMatX;
	skinMatrix += skinWeight.y * boneMatY;
	skinMatrix += skinWeight.z * boneMatZ;
	skinMatrix += skinWeight.w * boneMatW;
	skinMatrix = bindMatrixInverse * skinMatrix * bindMatrix;
	objectNormal = vec4( skinMatrix * vec4( objectNormal, 0.0 ) ).xyz;
	#ifdef USE_TANGENT
		objectTangent = vec4( skinMatrix * vec4( objectTangent, 0.0 ) ).xyz;
	#endif
#endif`,fK=`float specularStrength;
#ifdef USE_SPECULARMAP
	vec4 texelSpecular = texture2D( specularMap, vSpecularMapUv );
	specularStrength = texelSpecular.r;
#else
	specularStrength = 1.0;
#endif`,vK=`#ifdef USE_SPECULARMAP
	uniform sampler2D specularMap;
#endif`,hK=`#if defined( TONE_MAPPING )
	gl_FragColor.rgb = toneMapping( gl_FragColor.rgb );
#endif`,xK=`#ifndef saturate
#define saturate( a ) clamp( a, 0.0, 1.0 )
#endif
uniform float toneMappingExposure;
vec3 LinearToneMapping( vec3 color ) {
	return saturate( toneMappingExposure * color );
}
vec3 ReinhardToneMapping( vec3 color ) {
	color *= toneMappingExposure;
	return saturate( color / ( vec3( 1.0 ) + color ) );
}
vec3 CineonToneMapping( vec3 color ) {
	color *= toneMappingExposure;
	color = max( vec3( 0.0 ), color - 0.004 );
	return pow( ( color * ( 6.2 * color + 0.5 ) ) / ( color * ( 6.2 * color + 1.7 ) + 0.06 ), vec3( 2.2 ) );
}
vec3 RRTAndODTFit( vec3 v ) {
	vec3 a = v * ( v + 0.0245786 ) - 0.000090537;
	vec3 b = v * ( 0.983729 * v + 0.4329510 ) + 0.238081;
	return a / b;
}
vec3 ACESFilmicToneMapping( vec3 color ) {
	const mat3 ACESInputMat = mat3(
		vec3( 0.59719, 0.07600, 0.02840 ),		vec3( 0.35458, 0.90834, 0.13383 ),
		vec3( 0.04823, 0.01566, 0.83777 )
	);
	const mat3 ACESOutputMat = mat3(
		vec3(  1.60475, -0.10208, -0.00327 ),		vec3( -0.53108,  1.10813, -0.07276 ),
		vec3( -0.07367, -0.00605,  1.07602 )
	);
	color *= toneMappingExposure / 0.6;
	color = ACESInputMat * color;
	color = RRTAndODTFit( color );
	color = ACESOutputMat * color;
	return saturate( color );
}
const mat3 LINEAR_REC2020_TO_LINEAR_SRGB = mat3(
	vec3( 1.6605, - 0.1246, - 0.0182 ),
	vec3( - 0.5876, 1.1329, - 0.1006 ),
	vec3( - 0.0728, - 0.0083, 1.1187 )
);
const mat3 LINEAR_SRGB_TO_LINEAR_REC2020 = mat3(
	vec3( 0.6274, 0.0691, 0.0164 ),
	vec3( 0.3293, 0.9195, 0.0880 ),
	vec3( 0.0433, 0.0113, 0.8956 )
);
vec3 agxDefaultContrastApprox( vec3 x ) {
	vec3 x2 = x * x;
	vec3 x4 = x2 * x2;
	return + 15.5 * x4 * x2
		- 40.14 * x4 * x
		+ 31.96 * x4
		- 6.868 * x2 * x
		+ 0.4298 * x2
		+ 0.1191 * x
		- 0.00232;
}
vec3 AgXToneMapping( vec3 color ) {
	const mat3 AgXInsetMatrix = mat3(
		vec3( 0.856627153315983, 0.137318972929847, 0.11189821299995 ),
		vec3( 0.0951212405381588, 0.761241990602591, 0.0767994186031903 ),
		vec3( 0.0482516061458583, 0.101439036467562, 0.811302368396859 )
	);
	const mat3 AgXOutsetMatrix = mat3(
		vec3( 1.1271005818144368, - 0.1413297634984383, - 0.14132976349843826 ),
		vec3( - 0.11060664309660323, 1.157823702216272, - 0.11060664309660294 ),
		vec3( - 0.016493938717834573, - 0.016493938717834257, 1.2519364065950405 )
	);
	const float AgxMinEv = - 12.47393;	const float AgxMaxEv = 4.026069;
	color *= toneMappingExposure;
	color = LINEAR_SRGB_TO_LINEAR_REC2020 * color;
	color = AgXInsetMatrix * color;
	color = max( color, 1e-10 );	color = log2( color );
	color = ( color - AgxMinEv ) / ( AgxMaxEv - AgxMinEv );
	color = clamp( color, 0.0, 1.0 );
	color = agxDefaultContrastApprox( color );
	color = AgXOutsetMatrix * color;
	color = pow( max( vec3( 0.0 ), color ), vec3( 2.2 ) );
	color = LINEAR_REC2020_TO_LINEAR_SRGB * color;
	color = clamp( color, 0.0, 1.0 );
	return color;
}
vec3 NeutralToneMapping( vec3 color ) {
	const float StartCompression = 0.8 - 0.04;
	const float Desaturation = 0.15;
	color *= toneMappingExposure;
	float x = min( color.r, min( color.g, color.b ) );
	float offset = x < 0.08 ? x - 6.25 * x * x : 0.04;
	color -= offset;
	float peak = max( color.r, max( color.g, color.b ) );
	if ( peak < StartCompression ) return color;
	float d = 1. - StartCompression;
	float newPeak = 1. - d * d / ( peak + d - StartCompression );
	color *= newPeak / peak;
	float g = 1. - 1. / ( Desaturation * ( peak - newPeak ) + 1. );
	return mix( color, vec3( newPeak ), g );
}
vec3 CustomToneMapping( vec3 color ) { return color; }`,gK=`#ifdef USE_TRANSMISSION
	material.transmission = transmission;
	material.transmissionAlpha = 1.0;
	material.thickness = thickness;
	material.attenuationDistance = attenuationDistance;
	material.attenuationColor = attenuationColor;
	#ifdef USE_TRANSMISSIONMAP
		material.transmission *= texture2D( transmissionMap, vTransmissionMapUv ).r;
	#endif
	#ifdef USE_THICKNESSMAP
		material.thickness *= texture2D( thicknessMap, vThicknessMapUv ).g;
	#endif
	vec3 pos = vWorldPosition;
	vec3 v = normalize( cameraPosition - pos );
	vec3 n = inverseTransformDirection( normal, viewMatrix );
	vec4 transmitted = getIBLVolumeRefraction(
		n, v, material.roughness, material.diffuseColor, material.specularColor, material.specularF90,
		pos, modelMatrix, viewMatrix, projectionMatrix, material.dispersion, material.ior, material.thickness,
		material.attenuationColor, material.attenuationDistance );
	material.transmissionAlpha = mix( material.transmissionAlpha, transmitted.a, material.transmission );
	totalDiffuse = mix( totalDiffuse, transmitted.rgb, material.transmission );
#endif`,pK=`#ifdef USE_TRANSMISSION
	uniform float transmission;
	uniform float thickness;
	uniform float attenuationDistance;
	uniform vec3 attenuationColor;
	#ifdef USE_TRANSMISSIONMAP
		uniform sampler2D transmissionMap;
	#endif
	#ifdef USE_THICKNESSMAP
		uniform sampler2D thicknessMap;
	#endif
	uniform vec2 transmissionSamplerSize;
	uniform sampler2D transmissionSamplerMap;
	uniform mat4 modelMatrix;
	uniform mat4 projectionMatrix;
	varying vec3 vWorldPosition;
	float w0( float a ) {
		return ( 1.0 / 6.0 ) * ( a * ( a * ( - a + 3.0 ) - 3.0 ) + 1.0 );
	}
	float w1( float a ) {
		return ( 1.0 / 6.0 ) * ( a *  a * ( 3.0 * a - 6.0 ) + 4.0 );
	}
	float w2( float a ){
		return ( 1.0 / 6.0 ) * ( a * ( a * ( - 3.0 * a + 3.0 ) + 3.0 ) + 1.0 );
	}
	float w3( float a ) {
		return ( 1.0 / 6.0 ) * ( a * a * a );
	}
	float g0( float a ) {
		return w0( a ) + w1( a );
	}
	float g1( float a ) {
		return w2( a ) + w3( a );
	}
	float h0( float a ) {
		return - 1.0 + w1( a ) / ( w0( a ) + w1( a ) );
	}
	float h1( float a ) {
		return 1.0 + w3( a ) / ( w2( a ) + w3( a ) );
	}
	vec4 bicubic( sampler2D tex, vec2 uv, vec4 texelSize, float lod ) {
		uv = uv * texelSize.zw + 0.5;
		vec2 iuv = floor( uv );
		vec2 fuv = fract( uv );
		float g0x = g0( fuv.x );
		float g1x = g1( fuv.x );
		float h0x = h0( fuv.x );
		float h1x = h1( fuv.x );
		float h0y = h0( fuv.y );
		float h1y = h1( fuv.y );
		vec2 p0 = ( vec2( iuv.x + h0x, iuv.y + h0y ) - 0.5 ) * texelSize.xy;
		vec2 p1 = ( vec2( iuv.x + h1x, iuv.y + h0y ) - 0.5 ) * texelSize.xy;
		vec2 p2 = ( vec2( iuv.x + h0x, iuv.y + h1y ) - 0.5 ) * texelSize.xy;
		vec2 p3 = ( vec2( iuv.x + h1x, iuv.y + h1y ) - 0.5 ) * texelSize.xy;
		return g0( fuv.y ) * ( g0x * textureLod( tex, p0, lod ) + g1x * textureLod( tex, p1, lod ) ) +
			g1( fuv.y ) * ( g0x * textureLod( tex, p2, lod ) + g1x * textureLod( tex, p3, lod ) );
	}
	vec4 textureBicubic( sampler2D sampler, vec2 uv, float lod ) {
		vec2 fLodSize = vec2( textureSize( sampler, int( lod ) ) );
		vec2 cLodSize = vec2( textureSize( sampler, int( lod + 1.0 ) ) );
		vec2 fLodSizeInv = 1.0 / fLodSize;
		vec2 cLodSizeInv = 1.0 / cLodSize;
		vec4 fSample = bicubic( sampler, uv, vec4( fLodSizeInv, fLodSize ), floor( lod ) );
		vec4 cSample = bicubic( sampler, uv, vec4( cLodSizeInv, cLodSize ), ceil( lod ) );
		return mix( fSample, cSample, fract( lod ) );
	}
	vec3 getVolumeTransmissionRay( const in vec3 n, const in vec3 v, const in float thickness, const in float ior, const in mat4 modelMatrix ) {
		vec3 refractionVector = refract( - v, normalize( n ), 1.0 / ior );
		vec3 modelScale;
		modelScale.x = length( vec3( modelMatrix[ 0 ].xyz ) );
		modelScale.y = length( vec3( modelMatrix[ 1 ].xyz ) );
		modelScale.z = length( vec3( modelMatrix[ 2 ].xyz ) );
		return normalize( refractionVector ) * thickness * modelScale;
	}
	float applyIorToRoughness( const in float roughness, const in float ior ) {
		return roughness * clamp( ior * 2.0 - 2.0, 0.0, 1.0 );
	}
	vec4 getTransmissionSample( const in vec2 fragCoord, const in float roughness, const in float ior ) {
		float lod = log2( transmissionSamplerSize.x ) * applyIorToRoughness( roughness, ior );
		return textureBicubic( transmissionSamplerMap, fragCoord.xy, lod );
	}
	vec3 volumeAttenuation( const in float transmissionDistance, const in vec3 attenuationColor, const in float attenuationDistance ) {
		if ( isinf( attenuationDistance ) ) {
			return vec3( 1.0 );
		} else {
			vec3 attenuationCoefficient = -log( attenuationColor ) / attenuationDistance;
			vec3 transmittance = exp( - attenuationCoefficient * transmissionDistance );			return transmittance;
		}
	}
	vec4 getIBLVolumeRefraction( const in vec3 n, const in vec3 v, const in float roughness, const in vec3 diffuseColor,
		const in vec3 specularColor, const in float specularF90, const in vec3 position, const in mat4 modelMatrix,
		const in mat4 viewMatrix, const in mat4 projMatrix, const in float dispersion, const in float ior, const in float thickness,
		const in vec3 attenuationColor, const in float attenuationDistance ) {
		vec4 transmittedLight;
		vec3 transmittance;
		#ifdef USE_DISPERSION
			float halfSpread = ( ior - 1.0 ) * 0.025 * dispersion;
			vec3 iors = vec3( ior - halfSpread, ior, ior + halfSpread );
			for ( int i = 0; i < 3; i ++ ) {
				vec3 transmissionRay = getVolumeTransmissionRay( n, v, thickness, iors[ i ], modelMatrix );
				vec3 refractedRayExit = position + transmissionRay;
				vec4 ndcPos = projMatrix * viewMatrix * vec4( refractedRayExit, 1.0 );
				vec2 refractionCoords = ndcPos.xy / ndcPos.w;
				refractionCoords += 1.0;
				refractionCoords /= 2.0;
				vec4 transmissionSample = getTransmissionSample( refractionCoords, roughness, iors[ i ] );
				transmittedLight[ i ] = transmissionSample[ i ];
				transmittedLight.a += transmissionSample.a;
				transmittance[ i ] = diffuseColor[ i ] * volumeAttenuation( length( transmissionRay ), attenuationColor, attenuationDistance )[ i ];
			}
			transmittedLight.a /= 3.0;
		#else
			vec3 transmissionRay = getVolumeTransmissionRay( n, v, thickness, ior, modelMatrix );
			vec3 refractedRayExit = position + transmissionRay;
			vec4 ndcPos = projMatrix * viewMatrix * vec4( refractedRayExit, 1.0 );
			vec2 refractionCoords = ndcPos.xy / ndcPos.w;
			refractionCoords += 1.0;
			refractionCoords /= 2.0;
			transmittedLight = getTransmissionSample( refractionCoords, roughness, ior );
			transmittance = diffuseColor * volumeAttenuation( length( transmissionRay ), attenuationColor, attenuationDistance );
		#endif
		vec3 attenuatedColor = transmittance * transmittedLight.rgb;
		vec3 F = EnvironmentBRDF( n, v, specularColor, specularF90, roughness );
		float transmittanceFactor = ( transmittance.r + transmittance.g + transmittance.b ) / 3.0;
		return vec4( ( 1.0 - F ) * attenuatedColor, 1.0 - ( 1.0 - transmittedLight.a ) * transmittanceFactor );
	}
#endif`,dK=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
	varying vec2 vUv;
#endif
#ifdef USE_MAP
	varying vec2 vMapUv;
#endif
#ifdef USE_ALPHAMAP
	varying vec2 vAlphaMapUv;
#endif
#ifdef USE_LIGHTMAP
	varying vec2 vLightMapUv;
#endif
#ifdef USE_AOMAP
	varying vec2 vAoMapUv;
#endif
#ifdef USE_BUMPMAP
	varying vec2 vBumpMapUv;
#endif
#ifdef USE_NORMALMAP
	varying vec2 vNormalMapUv;
#endif
#ifdef USE_EMISSIVEMAP
	varying vec2 vEmissiveMapUv;
#endif
#ifdef USE_METALNESSMAP
	varying vec2 vMetalnessMapUv;
#endif
#ifdef USE_ROUGHNESSMAP
	varying vec2 vRoughnessMapUv;
#endif
#ifdef USE_ANISOTROPYMAP
	varying vec2 vAnisotropyMapUv;
#endif
#ifdef USE_CLEARCOATMAP
	varying vec2 vClearcoatMapUv;
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	varying vec2 vClearcoatNormalMapUv;
#endif
#ifdef USE_CLEARCOAT_ROUGHNESSMAP
	varying vec2 vClearcoatRoughnessMapUv;
#endif
#ifdef USE_IRIDESCENCEMAP
	varying vec2 vIridescenceMapUv;
#endif
#ifdef USE_IRIDESCENCE_THICKNESSMAP
	varying vec2 vIridescenceThicknessMapUv;
#endif
#ifdef USE_SHEEN_COLORMAP
	varying vec2 vSheenColorMapUv;
#endif
#ifdef USE_SHEEN_ROUGHNESSMAP
	varying vec2 vSheenRoughnessMapUv;
#endif
#ifdef USE_SPECULARMAP
	varying vec2 vSpecularMapUv;
#endif
#ifdef USE_SPECULAR_COLORMAP
	varying vec2 vSpecularColorMapUv;
#endif
#ifdef USE_SPECULAR_INTENSITYMAP
	varying vec2 vSpecularIntensityMapUv;
#endif
#ifdef USE_TRANSMISSIONMAP
	uniform mat3 transmissionMapTransform;
	varying vec2 vTransmissionMapUv;
#endif
#ifdef USE_THICKNESSMAP
	uniform mat3 thicknessMapTransform;
	varying vec2 vThicknessMapUv;
#endif`,lK=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
	varying vec2 vUv;
#endif
#ifdef USE_MAP
	uniform mat3 mapTransform;
	varying vec2 vMapUv;
#endif
#ifdef USE_ALPHAMAP
	uniform mat3 alphaMapTransform;
	varying vec2 vAlphaMapUv;
#endif
#ifdef USE_LIGHTMAP
	uniform mat3 lightMapTransform;
	varying vec2 vLightMapUv;
#endif
#ifdef USE_AOMAP
	uniform mat3 aoMapTransform;
	varying vec2 vAoMapUv;
#endif
#ifdef USE_BUMPMAP
	uniform mat3 bumpMapTransform;
	varying vec2 vBumpMapUv;
#endif
#ifdef USE_NORMALMAP
	uniform mat3 normalMapTransform;
	varying vec2 vNormalMapUv;
#endif
#ifdef USE_DISPLACEMENTMAP
	uniform mat3 displacementMapTransform;
	varying vec2 vDisplacementMapUv;
#endif
#ifdef USE_EMISSIVEMAP
	uniform mat3 emissiveMapTransform;
	varying vec2 vEmissiveMapUv;
#endif
#ifdef USE_METALNESSMAP
	uniform mat3 metalnessMapTransform;
	varying vec2 vMetalnessMapUv;
#endif
#ifdef USE_ROUGHNESSMAP
	uniform mat3 roughnessMapTransform;
	varying vec2 vRoughnessMapUv;
#endif
#ifdef USE_ANISOTROPYMAP
	uniform mat3 anisotropyMapTransform;
	varying vec2 vAnisotropyMapUv;
#endif
#ifdef USE_CLEARCOATMAP
	uniform mat3 clearcoatMapTransform;
	varying vec2 vClearcoatMapUv;
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	uniform mat3 clearcoatNormalMapTransform;
	varying vec2 vClearcoatNormalMapUv;
#endif
#ifdef USE_CLEARCOAT_ROUGHNESSMAP
	uniform mat3 clearcoatRoughnessMapTransform;
	varying vec2 vClearcoatRoughnessMapUv;
#endif
#ifdef USE_SHEEN_COLORMAP
	uniform mat3 sheenColorMapTransform;
	varying vec2 vSheenColorMapUv;
#endif
#ifdef USE_SHEEN_ROUGHNESSMAP
	uniform mat3 sheenRoughnessMapTransform;
	varying vec2 vSheenRoughnessMapUv;
#endif
#ifdef USE_IRIDESCENCEMAP
	uniform mat3 iridescenceMapTransform;
	varying vec2 vIridescenceMapUv;
#endif
#ifdef USE_IRIDESCENCE_THICKNESSMAP
	uniform mat3 iridescenceThicknessMapTransform;
	varying vec2 vIridescenceThicknessMapUv;
#endif
#ifdef USE_SPECULARMAP
	uniform mat3 specularMapTransform;
	varying vec2 vSpecularMapUv;
#endif
#ifdef USE_SPECULAR_COLORMAP
	uniform mat3 specularColorMapTransform;
	varying vec2 vSpecularColorMapUv;
#endif
#ifdef USE_SPECULAR_INTENSITYMAP
	uniform mat3 specularIntensityMapTransform;
	varying vec2 vSpecularIntensityMapUv;
#endif
#ifdef USE_TRANSMISSIONMAP
	uniform mat3 transmissionMapTransform;
	varying vec2 vTransmissionMapUv;
#endif
#ifdef USE_THICKNESSMAP
	uniform mat3 thicknessMapTransform;
	varying vec2 vThicknessMapUv;
#endif`,mK=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
	vUv = vec3( uv, 1 ).xy;
#endif
#ifdef USE_MAP
	vMapUv = ( mapTransform * vec3( MAP_UV, 1 ) ).xy;
#endif
#ifdef USE_ALPHAMAP
	vAlphaMapUv = ( alphaMapTransform * vec3( ALPHAMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_LIGHTMAP
	vLightMapUv = ( lightMapTransform * vec3( LIGHTMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_AOMAP
	vAoMapUv = ( aoMapTransform * vec3( AOMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_BUMPMAP
	vBumpMapUv = ( bumpMapTransform * vec3( BUMPMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_NORMALMAP
	vNormalMapUv = ( normalMapTransform * vec3( NORMALMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_DISPLACEMENTMAP
	vDisplacementMapUv = ( displacementMapTransform * vec3( DISPLACEMENTMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_EMISSIVEMAP
	vEmissiveMapUv = ( emissiveMapTransform * vec3( EMISSIVEMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_METALNESSMAP
	vMetalnessMapUv = ( metalnessMapTransform * vec3( METALNESSMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_ROUGHNESSMAP
	vRoughnessMapUv = ( roughnessMapTransform * vec3( ROUGHNESSMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_ANISOTROPYMAP
	vAnisotropyMapUv = ( anisotropyMapTransform * vec3( ANISOTROPYMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_CLEARCOATMAP
	vClearcoatMapUv = ( clearcoatMapTransform * vec3( CLEARCOATMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	vClearcoatNormalMapUv = ( clearcoatNormalMapTransform * vec3( CLEARCOAT_NORMALMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_CLEARCOAT_ROUGHNESSMAP
	vClearcoatRoughnessMapUv = ( clearcoatRoughnessMapTransform * vec3( CLEARCOAT_ROUGHNESSMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_IRIDESCENCEMAP
	vIridescenceMapUv = ( iridescenceMapTransform * vec3( IRIDESCENCEMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_IRIDESCENCE_THICKNESSMAP
	vIridescenceThicknessMapUv = ( iridescenceThicknessMapTransform * vec3( IRIDESCENCE_THICKNESSMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_SHEEN_COLORMAP
	vSheenColorMapUv = ( sheenColorMapTransform * vec3( SHEEN_COLORMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_SHEEN_ROUGHNESSMAP
	vSheenRoughnessMapUv = ( sheenRoughnessMapTransform * vec3( SHEEN_ROUGHNESSMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_SPECULARMAP
	vSpecularMapUv = ( specularMapTransform * vec3( SPECULARMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_SPECULAR_COLORMAP
	vSpecularColorMapUv = ( specularColorMapTransform * vec3( SPECULAR_COLORMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_SPECULAR_INTENSITYMAP
	vSpecularIntensityMapUv = ( specularIntensityMapTransform * vec3( SPECULAR_INTENSITYMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_TRANSMISSIONMAP
	vTransmissionMapUv = ( transmissionMapTransform * vec3( TRANSMISSIONMAP_UV, 1 ) ).xy;
#endif
#ifdef USE_THICKNESSMAP
	vThicknessMapUv = ( thicknessMapTransform * vec3( THICKNESSMAP_UV, 1 ) ).xy;
#endif`,uK=`#if defined( USE_ENVMAP ) || defined( DISTANCE ) || defined ( USE_SHADOWMAP ) || defined ( USE_TRANSMISSION ) || NUM_SPOT_LIGHT_COORDS > 0
	vec4 worldPosition = vec4( transformed, 1.0 );
	#ifdef USE_BATCHING
		worldPosition = batchingMatrix * worldPosition;
	#endif
	#ifdef USE_INSTANCING
		worldPosition = instanceMatrix * worldPosition;
	#endif
	worldPosition = modelMatrix * worldPosition;
#endif`,cK=`varying vec2 vUv;
uniform mat3 uvTransform;
void main() {
	vUv = ( uvTransform * vec3( uv, 1 ) ).xy;
	gl_Position = vec4( position.xy, 1.0, 1.0 );
}`,nK=`uniform sampler2D t2D;
uniform float backgroundIntensity;
varying vec2 vUv;
void main() {
	vec4 texColor = texture2D( t2D, vUv );
	#ifdef DECODE_VIDEO_TEXTURE
		texColor = vec4( mix( pow( texColor.rgb * 0.9478672986 + vec3( 0.0521327014 ), vec3( 2.4 ) ), texColor.rgb * 0.0773993808, vec3( lessThanEqual( texColor.rgb, vec3( 0.04045 ) ) ) ), texColor.w );
	#endif
	texColor.rgb *= backgroundIntensity;
	gl_FragColor = texColor;
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,sK=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
	gl_Position.z = gl_Position.w;
}`,iK=`#ifdef ENVMAP_TYPE_CUBE
	uniform samplerCube envMap;
#elif defined( ENVMAP_TYPE_CUBE_UV )
	uniform sampler2D envMap;
#endif
uniform float flipEnvMap;
uniform float backgroundBlurriness;
uniform float backgroundIntensity;
uniform mat3 backgroundRotation;
varying vec3 vWorldDirection;
#include <cube_uv_reflection_fragment>
void main() {
	#ifdef ENVMAP_TYPE_CUBE
		vec4 texColor = textureCube( envMap, backgroundRotation * vec3( flipEnvMap * vWorldDirection.x, vWorldDirection.yz ) );
	#elif defined( ENVMAP_TYPE_CUBE_UV )
		vec4 texColor = textureCubeUV( envMap, backgroundRotation * vWorldDirection, backgroundBlurriness );
	#else
		vec4 texColor = vec4( 0.0, 0.0, 0.0, 1.0 );
	#endif
	texColor.rgb *= backgroundIntensity;
	gl_FragColor = texColor;
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,oK=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
	gl_Position.z = gl_Position.w;
}`,aK=`uniform samplerCube tCube;
uniform float tFlip;
uniform float opacity;
varying vec3 vWorldDirection;
void main() {
	vec4 texColor = textureCube( tCube, vec3( tFlip * vWorldDirection.x, vWorldDirection.yz ) );
	gl_FragColor = texColor;
	gl_FragColor.a *= opacity;
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,rK=`#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
varying vec2 vHighPrecisionZW;
void main() {
	#include <uv_vertex>
	#include <batching_vertex>
	#include <skinbase_vertex>
	#include <morphinstance_vertex>
	#ifdef USE_DISPLACEMENTMAP
		#include <beginnormal_vertex>
		#include <morphnormal_vertex>
		#include <skinnormal_vertex>
	#endif
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	vHighPrecisionZW = gl_Position.zw;
}`,tK=`#if DEPTH_PACKING == 3200
	uniform float opacity;
#endif
#include <common>
#include <packing>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
varying vec2 vHighPrecisionZW;
void main() {
	vec4 diffuseColor = vec4( 1.0 );
	#include <clipping_planes_fragment>
	#if DEPTH_PACKING == 3200
		diffuseColor.a = opacity;
	#endif
	#include <map_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <logdepthbuf_fragment>
	#ifdef USE_REVERSED_DEPTH_BUFFER
		float fragCoordZ = vHighPrecisionZW[ 0 ] / vHighPrecisionZW[ 1 ];
	#else
		float fragCoordZ = 0.5 * vHighPrecisionZW[ 0 ] / vHighPrecisionZW[ 1 ] + 0.5;
	#endif
	#if DEPTH_PACKING == 3200
		gl_FragColor = vec4( vec3( 1.0 - fragCoordZ ), opacity );
	#elif DEPTH_PACKING == 3201
		gl_FragColor = packDepthToRGBA( fragCoordZ );
	#elif DEPTH_PACKING == 3202
		gl_FragColor = vec4( packDepthToRGB( fragCoordZ ), 1.0 );
	#elif DEPTH_PACKING == 3203
		gl_FragColor = vec4( packDepthToRG( fragCoordZ ), 0.0, 1.0 );
	#endif
}`,eK=`#define DISTANCE
varying vec3 vWorldPosition;
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <batching_vertex>
	#include <skinbase_vertex>
	#include <morphinstance_vertex>
	#ifdef USE_DISPLACEMENTMAP
		#include <beginnormal_vertex>
		#include <morphnormal_vertex>
		#include <skinnormal_vertex>
	#endif
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <worldpos_vertex>
	#include <clipping_planes_vertex>
	vWorldPosition = worldPosition.xyz;
}`,JY=`#define DISTANCE
uniform vec3 referencePosition;
uniform float nearDistance;
uniform float farDistance;
varying vec3 vWorldPosition;
#include <common>
#include <packing>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <clipping_planes_pars_fragment>
void main () {
	vec4 diffuseColor = vec4( 1.0 );
	#include <clipping_planes_fragment>
	#include <map_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	float dist = length( vWorldPosition - referencePosition );
	dist = ( dist - nearDistance ) / ( farDistance - nearDistance );
	dist = saturate( dist );
	gl_FragColor = packDepthToRGBA( dist );
}`,$Y=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
}`,NY=`uniform sampler2D tEquirect;
varying vec3 vWorldDirection;
#include <common>
void main() {
	vec3 direction = normalize( vWorldDirection );
	vec2 sampleUV = equirectUv( direction );
	gl_FragColor = texture2D( tEquirect, sampleUV );
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,QY=`uniform float scale;
attribute float lineDistance;
varying float vLineDistance;
#include <common>
#include <uv_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <morphtarget_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	vLineDistance = scale * lineDistance;
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	#include <fog_vertex>
}`,ZY=`uniform vec3 diffuse;
uniform float opacity;
uniform float dashSize;
uniform float totalSize;
varying float vLineDistance;
#include <common>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <fog_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	if ( mod( vLineDistance, totalSize ) > dashSize ) {
		discard;
	}
	vec3 outgoingLight = vec3( 0.0 );
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	outgoingLight = diffuseColor.rgb;
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
}`,WY=`#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <envmap_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#if defined ( USE_ENVMAP ) || defined ( USE_SKINNING )
		#include <beginnormal_vertex>
		#include <morphnormal_vertex>
		#include <skinbase_vertex>
		#include <skinnormal_vertex>
		#include <defaultnormal_vertex>
	#endif
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	#include <worldpos_vertex>
	#include <envmap_vertex>
	#include <fog_vertex>
}`,KY=`uniform vec3 diffuse;
uniform float opacity;
#ifndef FLAT_SHADED
	varying vec3 vNormal;
#endif
#include <common>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <aomap_pars_fragment>
#include <lightmap_pars_fragment>
#include <envmap_common_pars_fragment>
#include <envmap_pars_fragment>
#include <fog_pars_fragment>
#include <specularmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <specularmap_fragment>
	ReflectedLight reflectedLight = ReflectedLight( vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ) );
	#ifdef USE_LIGHTMAP
		vec4 lightMapTexel = texture2D( lightMap, vLightMapUv );
		reflectedLight.indirectDiffuse += lightMapTexel.rgb * lightMapIntensity * RECIPROCAL_PI;
	#else
		reflectedLight.indirectDiffuse += vec3( 1.0 );
	#endif
	#include <aomap_fragment>
	reflectedLight.indirectDiffuse *= diffuseColor.rgb;
	vec3 outgoingLight = reflectedLight.indirectDiffuse;
	#include <envmap_fragment>
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,YY=`#define LAMBERT
varying vec3 vViewPosition;
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <envmap_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <shadowmap_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	vViewPosition = - mvPosition.xyz;
	#include <worldpos_vertex>
	#include <envmap_vertex>
	#include <shadowmap_vertex>
	#include <fog_vertex>
}`,HY=`#define LAMBERT
uniform vec3 diffuse;
uniform vec3 emissive;
uniform float opacity;
#include <common>
#include <packing>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <aomap_pars_fragment>
#include <lightmap_pars_fragment>
#include <emissivemap_pars_fragment>
#include <envmap_common_pars_fragment>
#include <envmap_pars_fragment>
#include <fog_pars_fragment>
#include <bsdfs>
#include <lights_pars_begin>
#include <normal_pars_fragment>
#include <lights_lambert_pars_fragment>
#include <shadowmap_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <specularmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	ReflectedLight reflectedLight = ReflectedLight( vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ) );
	vec3 totalEmissiveRadiance = emissive;
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <specularmap_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	#include <emissivemap_fragment>
	#include <lights_lambert_fragment>
	#include <lights_fragment_begin>
	#include <lights_fragment_maps>
	#include <lights_fragment_end>
	#include <aomap_fragment>
	vec3 outgoingLight = reflectedLight.directDiffuse + reflectedLight.indirectDiffuse + totalEmissiveRadiance;
	#include <envmap_fragment>
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,XY=`#define MATCAP
varying vec3 vViewPosition;
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <color_pars_vertex>
#include <displacementmap_pars_vertex>
#include <fog_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	#include <fog_vertex>
	vViewPosition = - mvPosition.xyz;
}`,UY=`#define MATCAP
uniform vec3 diffuse;
uniform float opacity;
uniform sampler2D matcap;
varying vec3 vViewPosition;
#include <common>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <fog_pars_fragment>
#include <normal_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	vec3 viewDir = normalize( vViewPosition );
	vec3 x = normalize( vec3( viewDir.z, 0.0, - viewDir.x ) );
	vec3 y = cross( viewDir, x );
	vec2 uv = vec2( dot( x, normal ), dot( y, normal ) ) * 0.495 + 0.5;
	#ifdef USE_MATCAP
		vec4 matcapColor = texture2D( matcap, uv );
	#else
		vec4 matcapColor = vec4( vec3( mix( 0.2, 0.8, uv.y ) ), 1.0 );
	#endif
	vec3 outgoingLight = diffuseColor.rgb * matcapColor.rgb;
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,GY=`#define NORMAL
#if defined( FLAT_SHADED ) || defined( USE_BUMPMAP ) || defined( USE_NORMALMAP_TANGENTSPACE )
	varying vec3 vViewPosition;
#endif
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphinstance_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
#if defined( FLAT_SHADED ) || defined( USE_BUMPMAP ) || defined( USE_NORMALMAP_TANGENTSPACE )
	vViewPosition = - mvPosition.xyz;
#endif
}`,EY=`#define NORMAL
uniform float opacity;
#if defined( FLAT_SHADED ) || defined( USE_BUMPMAP ) || defined( USE_NORMALMAP_TANGENTSPACE )
	varying vec3 vViewPosition;
#endif
#include <packing>
#include <uv_pars_fragment>
#include <normal_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( 0.0, 0.0, 0.0, opacity );
	#include <clipping_planes_fragment>
	#include <logdepthbuf_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	gl_FragColor = vec4( packNormalToRGB( normal ), diffuseColor.a );
	#ifdef OPAQUE
		gl_FragColor.a = 1.0;
	#endif
}`,DY=`#define PHONG
varying vec3 vViewPosition;
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <envmap_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <shadowmap_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphinstance_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	vViewPosition = - mvPosition.xyz;
	#include <worldpos_vertex>
	#include <envmap_vertex>
	#include <shadowmap_vertex>
	#include <fog_vertex>
}`,qY=`#define PHONG
uniform vec3 diffuse;
uniform vec3 emissive;
uniform vec3 specular;
uniform float shininess;
uniform float opacity;
#include <common>
#include <packing>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <aomap_pars_fragment>
#include <lightmap_pars_fragment>
#include <emissivemap_pars_fragment>
#include <envmap_common_pars_fragment>
#include <envmap_pars_fragment>
#include <fog_pars_fragment>
#include <bsdfs>
#include <lights_pars_begin>
#include <normal_pars_fragment>
#include <lights_phong_pars_fragment>
#include <shadowmap_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <specularmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	ReflectedLight reflectedLight = ReflectedLight( vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ) );
	vec3 totalEmissiveRadiance = emissive;
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <specularmap_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	#include <emissivemap_fragment>
	#include <lights_phong_fragment>
	#include <lights_fragment_begin>
	#include <lights_fragment_maps>
	#include <lights_fragment_end>
	#include <aomap_fragment>
	vec3 outgoingLight = reflectedLight.directDiffuse + reflectedLight.indirectDiffuse + reflectedLight.directSpecular + reflectedLight.indirectSpecular + totalEmissiveRadiance;
	#include <envmap_fragment>
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,kY=`#define STANDARD
varying vec3 vViewPosition;
#ifdef USE_TRANSMISSION
	varying vec3 vWorldPosition;
#endif
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <shadowmap_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	vViewPosition = - mvPosition.xyz;
	#include <worldpos_vertex>
	#include <shadowmap_vertex>
	#include <fog_vertex>
#ifdef USE_TRANSMISSION
	vWorldPosition = worldPosition.xyz;
#endif
}`,OY=`#define STANDARD
#ifdef PHYSICAL
	#define IOR
	#define USE_SPECULAR
#endif
uniform vec3 diffuse;
uniform vec3 emissive;
uniform float roughness;
uniform float metalness;
uniform float opacity;
#ifdef IOR
	uniform float ior;
#endif
#ifdef USE_SPECULAR
	uniform float specularIntensity;
	uniform vec3 specularColor;
	#ifdef USE_SPECULAR_COLORMAP
		uniform sampler2D specularColorMap;
	#endif
	#ifdef USE_SPECULAR_INTENSITYMAP
		uniform sampler2D specularIntensityMap;
	#endif
#endif
#ifdef USE_CLEARCOAT
	uniform float clearcoat;
	uniform float clearcoatRoughness;
#endif
#ifdef USE_DISPERSION
	uniform float dispersion;
#endif
#ifdef USE_IRIDESCENCE
	uniform float iridescence;
	uniform float iridescenceIOR;
	uniform float iridescenceThicknessMinimum;
	uniform float iridescenceThicknessMaximum;
#endif
#ifdef USE_SHEEN
	uniform vec3 sheenColor;
	uniform float sheenRoughness;
	#ifdef USE_SHEEN_COLORMAP
		uniform sampler2D sheenColorMap;
	#endif
	#ifdef USE_SHEEN_ROUGHNESSMAP
		uniform sampler2D sheenRoughnessMap;
	#endif
#endif
#ifdef USE_ANISOTROPY
	uniform vec2 anisotropyVector;
	#ifdef USE_ANISOTROPYMAP
		uniform sampler2D anisotropyMap;
	#endif
#endif
varying vec3 vViewPosition;
#include <common>
#include <packing>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <aomap_pars_fragment>
#include <lightmap_pars_fragment>
#include <emissivemap_pars_fragment>
#include <iridescence_fragment>
#include <cube_uv_reflection_fragment>
#include <envmap_common_pars_fragment>
#include <envmap_physical_pars_fragment>
#include <fog_pars_fragment>
#include <lights_pars_begin>
#include <normal_pars_fragment>
#include <lights_physical_pars_fragment>
#include <transmission_pars_fragment>
#include <shadowmap_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <clearcoat_pars_fragment>
#include <iridescence_pars_fragment>
#include <roughnessmap_pars_fragment>
#include <metalnessmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	ReflectedLight reflectedLight = ReflectedLight( vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ) );
	vec3 totalEmissiveRadiance = emissive;
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <roughnessmap_fragment>
	#include <metalnessmap_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	#include <clearcoat_normal_fragment_begin>
	#include <clearcoat_normal_fragment_maps>
	#include <emissivemap_fragment>
	#include <lights_physical_fragment>
	#include <lights_fragment_begin>
	#include <lights_fragment_maps>
	#include <lights_fragment_end>
	#include <aomap_fragment>
	vec3 totalDiffuse = reflectedLight.directDiffuse + reflectedLight.indirectDiffuse;
	vec3 totalSpecular = reflectedLight.directSpecular + reflectedLight.indirectSpecular;
	#include <transmission_fragment>
	vec3 outgoingLight = totalDiffuse + totalSpecular + totalEmissiveRadiance;
	#ifdef USE_SHEEN
		float sheenEnergyComp = 1.0 - 0.157 * max3( material.sheenColor );
		outgoingLight = outgoingLight * sheenEnergyComp + sheenSpecularDirect + sheenSpecularIndirect;
	#endif
	#ifdef USE_CLEARCOAT
		float dotNVcc = saturate( dot( geometryClearcoatNormal, geometryViewDir ) );
		vec3 Fcc = F_Schlick( material.clearcoatF0, material.clearcoatF90, dotNVcc );
		outgoingLight = outgoingLight * ( 1.0 - material.clearcoat * Fcc ) + ( clearcoatSpecularDirect + clearcoatSpecularIndirect ) * material.clearcoat;
	#endif
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,RY=`#define TOON
varying vec3 vViewPosition;
#include <common>
#include <batching_pars_vertex>
#include <uv_pars_vertex>
#include <displacementmap_pars_vertex>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <normal_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <shadowmap_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <normal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <displacementmap_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	vViewPosition = - mvPosition.xyz;
	#include <worldpos_vertex>
	#include <shadowmap_vertex>
	#include <fog_vertex>
}`,FY=`#define TOON
uniform vec3 diffuse;
uniform vec3 emissive;
uniform float opacity;
#include <common>
#include <packing>
#include <dithering_pars_fragment>
#include <color_pars_fragment>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <aomap_pars_fragment>
#include <lightmap_pars_fragment>
#include <emissivemap_pars_fragment>
#include <gradientmap_pars_fragment>
#include <fog_pars_fragment>
#include <bsdfs>
#include <lights_pars_begin>
#include <normal_pars_fragment>
#include <lights_toon_pars_fragment>
#include <shadowmap_pars_fragment>
#include <bumpmap_pars_fragment>
#include <normalmap_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	ReflectedLight reflectedLight = ReflectedLight( vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ), vec3( 0.0 ) );
	vec3 totalEmissiveRadiance = emissive;
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <color_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	#include <normal_fragment_begin>
	#include <normal_fragment_maps>
	#include <emissivemap_fragment>
	#include <lights_toon_fragment>
	#include <lights_fragment_begin>
	#include <lights_fragment_maps>
	#include <lights_fragment_end>
	#include <aomap_fragment>
	vec3 outgoingLight = reflectedLight.directDiffuse + reflectedLight.indirectDiffuse + totalEmissiveRadiance;
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
	#include <dithering_fragment>
}`,MY=`uniform float size;
uniform float scale;
#include <common>
#include <color_pars_vertex>
#include <fog_pars_vertex>
#include <morphtarget_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
#ifdef USE_POINTS_UV
	varying vec2 vUv;
	uniform mat3 uvTransform;
#endif
void main() {
	#ifdef USE_POINTS_UV
		vUv = ( uvTransform * vec3( uv, 1 ) ).xy;
	#endif
	#include <color_vertex>
	#include <morphinstance_vertex>
	#include <morphcolor_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <project_vertex>
	gl_PointSize = size;
	#ifdef USE_SIZEATTENUATION
		bool isPerspective = isPerspectiveMatrix( projectionMatrix );
		if ( isPerspective ) gl_PointSize *= ( scale / - mvPosition.z );
	#endif
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	#include <worldpos_vertex>
	#include <fog_vertex>
}`,_Y=`uniform vec3 diffuse;
uniform float opacity;
#include <common>
#include <color_pars_fragment>
#include <map_particle_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <fog_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	vec3 outgoingLight = vec3( 0.0 );
	#include <logdepthbuf_fragment>
	#include <map_particle_fragment>
	#include <color_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	outgoingLight = diffuseColor.rgb;
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
	#include <premultiplied_alpha_fragment>
}`,LY=`#include <common>
#include <batching_pars_vertex>
#include <fog_pars_vertex>
#include <morphtarget_pars_vertex>
#include <skinning_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <shadowmap_pars_vertex>
void main() {
	#include <batching_vertex>
	#include <beginnormal_vertex>
	#include <morphinstance_vertex>
	#include <morphnormal_vertex>
	#include <skinbase_vertex>
	#include <skinnormal_vertex>
	#include <defaultnormal_vertex>
	#include <begin_vertex>
	#include <morphtarget_vertex>
	#include <skinning_vertex>
	#include <project_vertex>
	#include <logdepthbuf_vertex>
	#include <worldpos_vertex>
	#include <shadowmap_vertex>
	#include <fog_vertex>
}`,VY=`uniform vec3 color;
uniform float opacity;
#include <common>
#include <packing>
#include <fog_pars_fragment>
#include <bsdfs>
#include <lights_pars_begin>
#include <logdepthbuf_pars_fragment>
#include <shadowmap_pars_fragment>
#include <shadowmask_pars_fragment>
void main() {
	#include <logdepthbuf_fragment>
	gl_FragColor = vec4( color, opacity * ( 1.0 - getShadowMask() ) );
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
}`,wY=`uniform float rotation;
uniform vec2 center;
#include <common>
#include <uv_pars_vertex>
#include <fog_pars_vertex>
#include <logdepthbuf_pars_vertex>
#include <clipping_planes_pars_vertex>
void main() {
	#include <uv_vertex>
	vec4 mvPosition = modelViewMatrix[ 3 ];
	vec2 scale = vec2( length( modelMatrix[ 0 ].xyz ), length( modelMatrix[ 1 ].xyz ) );
	#ifndef USE_SIZEATTENUATION
		bool isPerspective = isPerspectiveMatrix( projectionMatrix );
		if ( isPerspective ) scale *= - mvPosition.z;
	#endif
	vec2 alignedPosition = ( position.xy - ( center - vec2( 0.5 ) ) ) * scale;
	vec2 rotatedPosition;
	rotatedPosition.x = cos( rotation ) * alignedPosition.x - sin( rotation ) * alignedPosition.y;
	rotatedPosition.y = sin( rotation ) * alignedPosition.x + cos( rotation ) * alignedPosition.y;
	mvPosition.xy += rotatedPosition;
	gl_Position = projectionMatrix * mvPosition;
	#include <logdepthbuf_vertex>
	#include <clipping_planes_vertex>
	#include <fog_vertex>
}`,zY=`uniform vec3 diffuse;
uniform float opacity;
#include <common>
#include <uv_pars_fragment>
#include <map_pars_fragment>
#include <alphamap_pars_fragment>
#include <alphatest_pars_fragment>
#include <alphahash_pars_fragment>
#include <fog_pars_fragment>
#include <logdepthbuf_pars_fragment>
#include <clipping_planes_pars_fragment>
void main() {
	vec4 diffuseColor = vec4( diffuse, opacity );
	#include <clipping_planes_fragment>
	vec3 outgoingLight = vec3( 0.0 );
	#include <logdepthbuf_fragment>
	#include <map_fragment>
	#include <alphamap_fragment>
	#include <alphatest_fragment>
	#include <alphahash_fragment>
	outgoingLight = diffuseColor.rgb;
	#include <opaque_fragment>
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
	#include <fog_fragment>
}`,u0={alphahash_fragment:nW,alphahash_pars_fragment:sW,alphamap_fragment:iW,alphamap_pars_fragment:oW,alphatest_fragment:aW,alphatest_pars_fragment:rW,aomap_fragment:tW,aomap_pars_fragment:eW,batching_pars_vertex:J1,batching_vertex:$1,begin_vertex:N1,beginnormal_vertex:Q1,bsdfs:Z1,iridescence_fragment:W1,bumpmap_pars_fragment:K1,clipping_planes_fragment:Y1,clipping_planes_pars_fragment:H1,clipping_planes_pars_vertex:X1,clipping_planes_vertex:U1,color_fragment:G1,color_pars_fragment:E1,color_pars_vertex:D1,color_vertex:q1,common:k1,cube_uv_reflection_fragment:O1,defaultnormal_vertex:R1,displacementmap_pars_vertex:F1,displacementmap_vertex:M1,emissivemap_fragment:_1,emissivemap_pars_fragment:L1,colorspace_fragment:V1,colorspace_pars_fragment:w1,envmap_fragment:z1,envmap_common_pars_fragment:B1,envmap_pars_fragment:C1,envmap_pars_vertex:I1,envmap_physical_pars_fragment:x1,envmap_vertex:P1,fog_vertex:A1,fog_pars_vertex:T1,fog_fragment:S1,fog_pars_fragment:b1,gradientmap_pars_fragment:y1,lightmap_pars_fragment:j1,lights_lambert_fragment:f1,lights_lambert_pars_fragment:v1,lights_pars_begin:h1,lights_toon_fragment:g1,lights_toon_pars_fragment:p1,lights_phong_fragment:d1,lights_phong_pars_fragment:l1,lights_physical_fragment:m1,lights_physical_pars_fragment:u1,lights_fragment_begin:c1,lights_fragment_maps:n1,lights_fragment_end:s1,logdepthbuf_fragment:i1,logdepthbuf_pars_fragment:o1,logdepthbuf_pars_vertex:a1,logdepthbuf_vertex:r1,map_fragment:t1,map_pars_fragment:e1,map_particle_fragment:JK,map_particle_pars_fragment:$K,metalnessmap_fragment:NK,metalnessmap_pars_fragment:QK,morphinstance_vertex:ZK,morphcolor_vertex:WK,morphnormal_vertex:KK,morphtarget_pars_vertex:YK,morphtarget_vertex:HK,normal_fragment_begin:XK,normal_fragment_maps:UK,normal_pars_fragment:GK,normal_pars_vertex:EK,normal_vertex:DK,normalmap_pars_fragment:qK,clearcoat_normal_fragment_begin:kK,clearcoat_normal_fragment_maps:OK,clearcoat_pars_fragment:RK,iridescence_pars_fragment:FK,opaque_fragment:MK,packing:_K,premultiplied_alpha_fragment:LK,project_vertex:VK,dithering_fragment:wK,dithering_pars_fragment:zK,roughnessmap_fragment:BK,roughnessmap_pars_fragment:CK,shadowmap_pars_fragment:IK,shadowmap_pars_vertex:PK,shadowmap_vertex:AK,shadowmask_pars_fragment:TK,skinbase_vertex:SK,skinning_pars_vertex:bK,skinning_vertex:yK,skinnormal_vertex:jK,specularmap_fragment:fK,specularmap_pars_fragment:vK,tonemapping_fragment:hK,tonemapping_pars_fragment:xK,transmission_fragment:gK,transmission_pars_fragment:pK,uv_pars_fragment:dK,uv_pars_vertex:lK,uv_vertex:mK,worldpos_vertex:uK,background_vert:cK,background_frag:nK,backgroundCube_vert:sK,backgroundCube_frag:iK,cube_vert:oK,cube_frag:aK,depth_vert:rK,depth_frag:tK,distanceRGBA_vert:eK,distanceRGBA_frag:JY,equirect_vert:$Y,equirect_frag:NY,linedashed_vert:QY,linedashed_frag:ZY,meshbasic_vert:WY,meshbasic_frag:KY,meshlambert_vert:YY,meshlambert_frag:HY,meshmatcap_vert:XY,meshmatcap_frag:UY,meshnormal_vert:GY,meshnormal_frag:EY,meshphong_vert:DY,meshphong_frag:qY,meshphysical_vert:kY,meshphysical_frag:OY,meshtoon_vert:RY,meshtoon_frag:FY,points_vert:MY,points_frag:_Y,shadow_vert:LY,shadow_frag:VY,sprite_vert:wY,sprite_frag:zY},D0={common:{diffuse:{value:new i0(16777215)},opacity:{value:1},map:{value:null},mapTransform:{value:new l0},alphaMap:{value:null},alphaMapTransform:{value:new l0},alphaTest:{value:0}},specularmap:{specularMap:{value:null},specularMapTransform:{value:new l0}},envmap:{envMap:{value:null},envMapRotation:{value:new l0},flipEnvMap:{value:-1},reflectivity:{value:1},ior:{value:1.5},refractionRatio:{value:0.98}},aomap:{aoMap:{value:null},aoMapIntensity:{value:1},aoMapTransform:{value:new l0}},lightmap:{lightMap:{value:null},lightMapIntensity:{value:1},lightMapTransform:{value:new l0}},bumpmap:{bumpMap:{value:null},bumpMapTransform:{value:new l0},bumpScale:{value:1}},normalmap:{normalMap:{value:null},normalMapTransform:{value:new l0},normalScale:{value:new p0(1,1)}},displacementmap:{displacementMap:{value:null},displacementMapTransform:{value:new l0},displacementScale:{value:1},displacementBias:{value:0}},emissivemap:{emissiveMap:{value:null},emissiveMapTransform:{value:new l0}},metalnessmap:{metalnessMap:{value:null},metalnessMapTransform:{value:new l0}},roughnessmap:{roughnessMap:{value:null},roughnessMapTransform:{value:new l0}},gradientmap:{gradientMap:{value:null}},fog:{fogDensity:{value:0.00025},fogNear:{value:1},fogFar:{value:2000},fogColor:{value:new i0(16777215)}},lights:{ambientLightColor:{value:[]},lightProbe:{value:[]},directionalLights:{value:[],properties:{direction:{},color:{}}},directionalLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{}}},directionalShadowMap:{value:[]},directionalShadowMatrix:{value:[]},spotLights:{value:[],properties:{color:{},position:{},direction:{},distance:{},coneCos:{},penumbraCos:{},decay:{}}},spotLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{}}},spotLightMap:{value:[]},spotShadowMap:{value:[]},spotLightMatrix:{value:[]},pointLights:{value:[],properties:{color:{},position:{},decay:{},distance:{}}},pointLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{},shadowCameraNear:{},shadowCameraFar:{}}},pointShadowMap:{value:[]},pointShadowMatrix:{value:[]},hemisphereLights:{value:[],properties:{direction:{},skyColor:{},groundColor:{}}},rectAreaLights:{value:[],properties:{color:{},position:{},width:{},height:{}}},ltc_1:{value:null},ltc_2:{value:null}},points:{diffuse:{value:new i0(16777215)},opacity:{value:1},size:{value:1},scale:{value:1},map:{value:null},alphaMap:{value:null},alphaMapTransform:{value:new l0},alphaTest:{value:0},uvTransform:{value:new l0}},sprite:{diffuse:{value:new i0(16777215)},opacity:{value:1},center:{value:new p0(0.5,0.5)},rotation:{value:0},map:{value:null},mapTransform:{value:new l0},alphaMap:{value:null},alphaMapTransform:{value:new l0},alphaTest:{value:0}}},X9={basic:{uniforms:C8([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.fog]),vertexShader:u0.meshbasic_vert,fragmentShader:u0.meshbasic_frag},lambert:{uniforms:C8([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,D0.lights,{emissive:{value:new i0(0)}}]),vertexShader:u0.meshlambert_vert,fragmentShader:u0.meshlambert_frag},phong:{uniforms:C8([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,D0.lights,{emissive:{value:new i0(0)},specular:{value:new i0(1118481)},shininess:{value:30}}]),vertexShader:u0.meshphong_vert,fragmentShader:u0.meshphong_frag},standard:{uniforms:C8([D0.common,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.roughnessmap,D0.metalnessmap,D0.fog,D0.lights,{emissive:{value:new i0(0)},roughness:{value:1},metalness:{value:0},envMapIntensity:{value:1}}]),vertexShader:u0.meshphysical_vert,fragmentShader:u0.meshphysical_frag},toon:{uniforms:C8([D0.common,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.gradientmap,D0.fog,D0.lights,{emissive:{value:new i0(0)}}]),vertexShader:u0.meshtoon_vert,fragmentShader:u0.meshtoon_frag},matcap:{uniforms:C8([D0.common,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,{matcap:{value:null}}]),vertexShader:u0.meshmatcap_vert,fragmentShader:u0.meshmatcap_frag},points:{uniforms:C8([D0.points,D0.fog]),vertexShader:u0.points_vert,fragmentShader:u0.points_frag},dashed:{uniforms:C8([D0.common,D0.fog,{scale:{value:1},dashSize:{value:1},totalSize:{value:2}}]),vertexShader:u0.linedashed_vert,fragmentShader:u0.linedashed_frag},depth:{uniforms:C8([D0.common,D0.displacementmap]),vertexShader:u0.depth_vert,fragmentShader:u0.depth_frag},normal:{uniforms:C8([D0.common,D0.bumpmap,D0.normalmap,D0.displacementmap,{opacity:{value:1}}]),vertexShader:u0.meshnormal_vert,fragmentShader:u0.meshnormal_frag},sprite:{uniforms:C8([D0.sprite,D0.fog]),vertexShader:u0.sprite_vert,fragmentShader:u0.sprite_frag},background:{uniforms:{uvTransform:{value:new l0},t2D:{value:null},backgroundIntensity:{value:1}},vertexShader:u0.background_vert,fragmentShader:u0.background_frag},backgroundCube:{uniforms:{envMap:{value:null},flipEnvMap:{value:-1},backgroundBlurriness:{value:0},backgroundIntensity:{value:1},backgroundRotation:{value:new l0}},vertexShader:u0.backgroundCube_vert,fragmentShader:u0.backgroundCube_frag},cube:{uniforms:{tCube:{value:null},tFlip:{value:-1},opacity:{value:1}},vertexShader:u0.cube_vert,fragmentShader:u0.cube_frag},equirect:{uniforms:{tEquirect:{value:null}},vertexShader:u0.equirect_vert,fragmentShader:u0.equirect_frag},distanceRGBA:{uniforms:C8([D0.common,D0.displacementmap,{referencePosition:{value:new h},nearDistance:{value:1},farDistance:{value:1000}}]),vertexShader:u0.distanceRGBA_vert,fragmentShader:u0.distanceRGBA_frag},shadow:{uniforms:C8([D0.lights,D0.fog,{color:{value:new i0(0)},opacity:{value:1}}]),vertexShader:u0.shadow_vert,fragmentShader:u0.shadow_frag}};X9.physical={uniforms:C8([X9.standard.uniforms,{clearcoat:{value:0},clearcoatMap:{value:null},clearcoatMapTransform:{value:new l0},clearcoatNormalMap:{value:null},clearcoatNormalMapTransform:{value:new l0},clearcoatNormalScale:{value:new p0(1,1)},clearcoatRoughness:{value:0},clearcoatRoughnessMap:{value:null},clearcoatRoughnessMapTransform:{value:new l0},dispersion:{value:0},iridescence:{value:0},iridescenceMap:{value:null},iridescenceMapTransform:{value:new l0},iridescenceIOR:{value:1.3},iridescenceThicknessMinimum:{value:100},iridescenceThicknessMaximum:{value:400},iridescenceThicknessMap:{value:null},iridescenceThicknessMapTransform:{value:new l0},sheen:{value:0},sheenColor:{value:new i0(0)},sheenColorMap:{value:null},sheenColorMapTransform:{value:new l0},sheenRoughness:{value:1},sheenRoughnessMap:{value:null},sheenRoughnessMapTransform:{value:new l0},transmission:{value:0},transmissionMap:{value:null},transmissionMapTransform:{value:new l0},transmissionSamplerSize:{value:new p0},transmissionSamplerMap:{value:null},thickness:{value:0},thicknessMap:{value:null},thicknessMapTransform:{value:new l0},attenuationDistance:{value:0},attenuationColor:{value:new i0(0)},specularColor:{value:new i0(1,1,1)},specularColorMap:{value:null},specularColorMapTransform:{value:new l0},specularIntensity:{value:1},specularIntensityMap:{value:null},specularIntensityMapTransform:{value:new l0},anisotropyVector:{value:new p0},anisotropyMap:{value:null},anisotropyMapTransform:{value:new l0}}]),vertexShader:u0.meshphysical_vert,fragmentShader:u0.meshphysical_frag};var K7={r:0,b:0,g:0},i9=new $9,BY=new U8;function CY(J,$,N,Q,Z,W,Y){let K=new i0(0),H=W===!0?0:1,X,G,E=null,q=0,k=null;function M(L){let B=L.isScene===!0?L.background:null;if(B&&B.isTexture)B=(L.backgroundBlurriness>0?N:$).get(B);return B}function _(L){let B=!1,y=M(L);if(y===null)U(K,H);else if(y&&y.isColor)U(y,1),B=!0;let I=J.xr.getEnvironmentBlendMode();if(I==="additive")Q.buffers.color.setClear(0,0,0,1,Y);else if(I==="alpha-blend")Q.buffers.color.setClear(0,0,0,0,Y);if(J.autoClear||B)Q.buffers.depth.setTest(!0),Q.buffers.depth.setMask(!0),Q.buffers.color.setMask(!0),J.clear(J.autoClearColor,J.autoClearDepth,J.autoClearStencil)}function D(L,B){let y=M(B);if(y&&(y.isCubeTexture||y.mapping===mJ)){if(G===void 0)G=new b8(new CJ(1,1,1),new H9({name:"BackgroundCubeMaterial",uniforms:c9(X9.backgroundCube.uniforms),vertexShader:X9.backgroundCube.vertexShader,fragmentShader:X9.backgroundCube.fragmentShader,side:m8,depthTest:!1,depthWrite:!1,fog:!1,allowOverride:!1})),G.geometry.deleteAttribute("normal"),G.geometry.deleteAttribute("uv"),G.onBeforeRender=function(I,b,g){this.matrixWorld.copyPosition(g.matrixWorld)},Object.defineProperty(G.material,"envMap",{get:function(){return this.uniforms.envMap.value}}),Z.update(G);if(i9.copy(B.backgroundRotation),i9.x*=-1,i9.y*=-1,i9.z*=-1,y.isCubeTexture&&y.isRenderTargetTexture===!1)i9.y*=-1,i9.z*=-1;if(G.material.uniforms.envMap.value=y,G.material.uniforms.flipEnvMap.value=y.isCubeTexture&&y.isRenderTargetTexture===!1?-1:1,G.material.uniforms.backgroundBlurriness.value=B.backgroundBlurriness,G.material.uniforms.backgroundIntensity.value=B.backgroundIntensity,G.material.uniforms.backgroundRotation.value.setFromMatrix4(BY.makeRotationFromEuler(i9)),G.material.toneMapped=o0.getTransfer(y.colorSpace)!==Y8,E!==y||q!==y.version||k!==J.toneMapping)G.material.needsUpdate=!0,E=y,q=y.version,k=J.toneMapping;G.layers.enableAll(),L.unshift(G,G.geometry,G.material,0,0,null)}else if(y&&y.isTexture){if(X===void 0)X=new b8(new J6(2,2),new H9({name:"BackgroundMaterial",uniforms:c9(X9.background.uniforms),vertexShader:X9.background.vertexShader,fragmentShader:X9.background.fragmentShader,side:RJ,depthTest:!1,depthWrite:!1,fog:!1,allowOverride:!1})),X.geometry.deleteAttribute("normal"),Object.defineProperty(X.material,"map",{get:function(){return this.uniforms.t2D.value}}),Z.update(X);if(X.material.uniforms.t2D.value=y,X.material.uniforms.backgroundIntensity.value=B.backgroundIntensity,X.material.toneMapped=o0.getTransfer(y.colorSpace)!==Y8,y.matrixAutoUpdate===!0)y.updateMatrix();if(X.material.uniforms.uvTransform.value.copy(y.matrix),E!==y||q!==y.version||k!==J.toneMapping)X.material.needsUpdate=!0,E=y,q=y.version,k=J.toneMapping;X.layers.enableAll(),L.unshift(X,X.geometry,X.material,0,0,null)}}function U(L,B){L.getRGB(K7,f$(J)),Q.buffers.color.setClear(K7.r,K7.g,K7.b,B,Y)}function C(){if(G!==void 0)G.geometry.dispose(),G.material.dispose(),G=void 0;if(X!==void 0)X.geometry.dispose(),X.material.dispose(),X=void 0}return{getClearColor:function(){return K},setClearColor:function(L,B=1){K.set(L),H=B,U(K,H)},getClearAlpha:function(){return H},setClearAlpha:function(L){H=L,U(K,H)},render:_,addToRenderList:D,dispose:C}}function IY(J,$){let N=J.getParameter(J.MAX_VERTEX_ATTRIBS),Q={},Z=q(null),W=Z,Y=!1;function K(F,S,s,i,m){let T=!1,p=E(i,s,S);if(W!==p)W=p,X(W.object);if(T=k(F,i,s,m),T)M(F,i,s,m);if(m!==null)$.update(m,J.ELEMENT_ARRAY_BUFFER);if(T||Y){if(Y=!1,B(F,S,s,i),m!==null)J.bindBuffer(J.ELEMENT_ARRAY_BUFFER,$.get(m).buffer)}}function H(){return J.createVertexArray()}function X(F){return J.bindVertexArray(F)}function G(F){return J.deleteVertexArray(F)}function E(F,S,s){let i=s.wireframe===!0,m=Q[F.id];if(m===void 0)m={},Q[F.id]=m;let T=m[S.id];if(T===void 0)T={},m[S.id]=T;let p=T[i];if(p===void 0)p=q(H()),T[i]=p;return p}function q(F){let S=[],s=[],i=[];for(let m=0;m<N;m++)S[m]=0,s[m]=0,i[m]=0;return{geometry:null,program:null,wireframe:!1,newAttributes:S,enabledAttributes:s,attributeDivisors:i,object:F,attributes:{},index:null}}function k(F,S,s,i){let m=W.attributes,T=S.attributes,p=0,$0=s.getAttributes();for(let c in $0)if($0[c].location>=0){let q0=m[c],P0=T[c];if(P0===void 0){if(c==="instanceMatrix"&&F.instanceMatrix)P0=F.instanceMatrix;if(c==="instanceColor"&&F.instanceColor)P0=F.instanceColor}if(q0===void 0)return!0;if(q0.attribute!==P0)return!0;if(P0&&q0.data!==P0.data)return!0;p++}if(W.attributesNum!==p)return!0;if(W.index!==i)return!0;return!1}function M(F,S,s,i){let m={},T=S.attributes,p=0,$0=s.getAttributes();for(let c in $0)if($0[c].location>=0){let q0=T[c];if(q0===void 0){if(c==="instanceMatrix"&&F.instanceMatrix)q0=F.instanceMatrix;if(c==="instanceColor"&&F.instanceColor)q0=F.instanceColor}let P0={};if(P0.attribute=q0,q0&&q0.data)P0.data=q0.data;m[c]=P0,p++}W.attributes=m,W.attributesNum=p,W.index=i}function _(){let F=W.newAttributes;for(let S=0,s=F.length;S<s;S++)F[S]=0}function D(F){U(F,0)}function U(F,S){let{newAttributes:s,enabledAttributes:i,attributeDivisors:m}=W;if(s[F]=1,i[F]===0)J.enableVertexAttribArray(F),i[F]=1;if(m[F]!==S)J.vertexAttribDivisor(F,S),m[F]=S}function C(){let{newAttributes:F,enabledAttributes:S}=W;for(let s=0,i=S.length;s<i;s++)if(S[s]!==F[s])J.disableVertexAttribArray(s),S[s]=0}function L(F,S,s,i,m,T,p){if(p===!0)J.vertexAttribIPointer(F,S,s,m,T);else J.vertexAttribPointer(F,S,s,i,m,T)}function B(F,S,s,i){_();let m=i.attributes,T=s.getAttributes(),p=S.defaultAttributeValues;for(let $0 in T){let c=T[$0];if(c.location>=0){let X0=m[$0];if(X0===void 0){if($0==="instanceMatrix"&&F.instanceMatrix)X0=F.instanceMatrix;if($0==="instanceColor"&&F.instanceColor)X0=F.instanceColor}if(X0!==void 0){let{normalized:q0,itemSize:P0}=X0,d0=$.get(X0);if(d0===void 0)continue;let{buffer:J8,type:c0,bytesPerElement:t}=d0,K0=c0===J.INT||c0===J.UNSIGNED_INT||X0.gpuType===c7;if(X0.isInterleavedBufferAttribute){let x=X0.data,J0=x.stride,Q0=X0.offset;if(x.isInstancedInterleavedBuffer){for(let E0=0;E0<c.locationSize;E0++)U(c.location+E0,x.meshPerAttribute);if(F.isInstancedMesh!==!0&&i._maxInstanceCount===void 0)i._maxInstanceCount=x.meshPerAttribute*x.count}else for(let E0=0;E0<c.locationSize;E0++)D(c.location+E0);J.bindBuffer(J.ARRAY_BUFFER,J8);for(let E0=0;E0<c.locationSize;E0++)L(c.location+E0,P0/c.locationSize,c0,q0,J0*t,(Q0+P0/c.locationSize*E0)*t,K0)}else{if(X0.isInstancedBufferAttribute){for(let x=0;x<c.locationSize;x++)U(c.location+x,X0.meshPerAttribute);if(F.isInstancedMesh!==!0&&i._maxInstanceCount===void 0)i._maxInstanceCount=X0.meshPerAttribute*X0.count}else for(let x=0;x<c.locationSize;x++)D(c.location+x);J.bindBuffer(J.ARRAY_BUFFER,J8);for(let x=0;x<c.locationSize;x++)L(c.location+x,P0/c.locationSize,c0,q0,P0*t,P0/c.locationSize*x*t,K0)}}else if(p!==void 0){let q0=p[$0];if(q0!==void 0)switch(q0.length){case 2:J.vertexAttrib2fv(c.location,q0);break;case 3:J.vertexAttrib3fv(c.location,q0);break;case 4:J.vertexAttrib4fv(c.location,q0);break;default:J.vertexAttrib1fv(c.location,q0)}}}}C()}function y(){g();for(let F in Q){let S=Q[F];for(let s in S){let i=S[s];for(let m in i)G(i[m].object),delete i[m];delete S[s]}delete Q[F]}}function I(F){if(Q[F.id]===void 0)return;let S=Q[F.id];for(let s in S){let i=S[s];for(let m in i)G(i[m].object),delete i[m];delete S[s]}delete Q[F.id]}function b(F){for(let S in Q){let s=Q[S];if(s[F.id]===void 0)continue;let i=s[F.id];for(let m in i)G(i[m].object),delete i[m];delete s[F.id]}}function g(){if(V(),Y=!0,W===Z)return;W=Z,X(W.object)}function V(){Z.geometry=null,Z.program=null,Z.wireframe=!1}return{setup:K,reset:g,resetDefaultState:V,dispose:y,releaseStatesOfGeometry:I,releaseStatesOfProgram:b,initAttributes:_,enableAttribute:D,disableUnusedAttributes:C}}function PY(J,$,N){let Q;function Z(X){Q=X}function W(X,G){J.drawArrays(Q,X,G),N.update(G,Q,1)}function Y(X,G,E){if(E===0)return;J.drawArraysInstanced(Q,X,G,E),N.update(G,Q,E)}function K(X,G,E){if(E===0)return;$.get("WEBGL_multi_draw").multiDrawArraysWEBGL(Q,X,0,G,0,E);let k=0;for(let M=0;M<E;M++)k+=G[M];N.update(k,Q,1)}function H(X,G,E,q){if(E===0)return;let k=$.get("WEBGL_multi_draw");if(k===null)for(let M=0;M<X.length;M++)Y(X[M],G[M],q[M]);else{k.multiDrawArraysInstancedWEBGL(Q,X,0,G,0,q,0,E);let M=0;for(let _=0;_<E;_++)M+=G[_]*q[_];N.update(M,Q,1)}}this.setMode=Z,this.render=W,this.renderInstances=Y,this.renderMultiDraw=K,this.renderMultiDrawInstances=H}function AY(J,$,N,Q){let Z;function W(){if(Z!==void 0)return Z;if($.has("EXT_texture_filter_anisotropic")===!0){let b=$.get("EXT_texture_filter_anisotropic");Z=J.getParameter(b.MAX_TEXTURE_MAX_ANISOTROPY_EXT)}else Z=0;return Z}function Y(b){if(b!==K9&&Q.convert(b)!==J.getParameter(J.IMPLEMENTATION_COLOR_READ_FORMAT))return!1;return!0}function K(b){let g=b===nJ&&($.has("EXT_color_buffer_half_float")||$.has("EXT_color_buffer_float"));if(b!==S9&&Q.convert(b)!==J.getParameter(J.IMPLEMENTATION_COLOR_READ_TYPE)&&b!==b9&&!g)return!1;return!0}function H(b){if(b==="highp"){if(J.getShaderPrecisionFormat(J.VERTEX_SHADER,J.HIGH_FLOAT).precision>0&&J.getShaderPrecisionFormat(J.FRAGMENT_SHADER,J.HIGH_FLOAT).precision>0)return"highp";b="mediump"}if(b==="mediump"){if(J.getShaderPrecisionFormat(J.VERTEX_SHADER,J.MEDIUM_FLOAT).precision>0&&J.getShaderPrecisionFormat(J.FRAGMENT_SHADER,J.MEDIUM_FLOAT).precision>0)return"mediump"}return"lowp"}let X=N.precision!==void 0?N.precision:"highp",G=H(X);if(G!==X)console.warn("THREE.WebGLRenderer:",X,"not supported, using",G,"instead."),X=G;let E=N.logarithmicDepthBuffer===!0,q=N.reversedDepthBuffer===!0&&$.has("EXT_clip_control"),k=J.getParameter(J.MAX_TEXTURE_IMAGE_UNITS),M=J.getParameter(J.MAX_VERTEX_TEXTURE_IMAGE_UNITS),_=J.getParameter(J.MAX_TEXTURE_SIZE),D=J.getParameter(J.MAX_CUBE_MAP_TEXTURE_SIZE),U=J.getParameter(J.MAX_VERTEX_ATTRIBS),C=J.getParameter(J.MAX_VERTEX_UNIFORM_VECTORS),L=J.getParameter(J.MAX_VARYING_VECTORS),B=J.getParameter(J.MAX_FRAGMENT_UNIFORM_VECTORS),y=M>0,I=J.getParameter(J.MAX_SAMPLES);return{isWebGL2:!0,getMaxAnisotropy:W,getMaxPrecision:H,textureFormatReadable:Y,textureTypeReadable:K,precision:X,logarithmicDepthBuffer:E,reversedDepthBuffer:q,maxTextures:k,maxVertexTextures:M,maxTextureSize:_,maxCubemapSize:D,maxAttributes:U,maxVertexUniforms:C,maxVaryings:L,maxFragmentUniforms:B,vertexTextures:y,maxSamples:I}}function TY(J){let $=this,N=null,Q=0,Z=!1,W=!1,Y=new l8,K=new l0,H={value:null,needsUpdate:!1};this.uniform=H,this.numPlanes=0,this.numIntersection=0,this.init=function(E,q){let k=E.length!==0||q||Q!==0||Z;return Z=q,Q=E.length,k},this.beginShadows=function(){W=!0,G(null)},this.endShadows=function(){W=!1},this.setGlobalState=function(E,q){N=G(E,q,0)},this.setState=function(E,q,k){let{clippingPlanes:M,clipIntersection:_,clipShadows:D}=E,U=J.get(E);if(!Z||M===null||M.length===0||W&&!D)if(W)G(null);else X();else{let C=W?0:Q,L=C*4,B=U.clippingState||null;H.value=B,B=G(M,q,L,k);for(let y=0;y!==L;++y)B[y]=N[y];U.clippingState=B,this.numIntersection=_?this.numPlanes:0,this.numPlanes+=C}};function X(){if(H.value!==N)H.value=N,H.needsUpdate=Q>0;$.numPlanes=Q,$.numIntersection=0}function G(E,q,k,M){let _=E!==null?E.length:0,D=null;if(_!==0){if(D=H.value,M!==!0||D===null){let U=k+_*4,C=q.matrixWorldInverse;if(K.getNormalMatrix(C),D===null||D.length<U)D=new Float32Array(U);for(let L=0,B=k;L!==_;++L,B+=4)Y.copy(E[L]).applyMatrix4(C,K),Y.normal.toArray(D,B),D[B+3]=Y.constant}H.value=D,H.needsUpdate=!0}return $.numPlanes=_,$.numIntersection=0,D}}function SY(J){let $=new WeakMap;function N(Y,K){if(K===f6)Y.mapping=MJ;else if(K===v6)Y.mapping=p9;return Y}function Q(Y){if(Y&&Y.isTexture){let K=Y.mapping;if(K===f6||K===v6)if($.has(Y)){let H=$.get(Y).texture;return N(H,Y.mapping)}else{let H=Y.image;if(H&&H.height>0){let X=new h$(H.height);return X.fromEquirectangularTexture(J,Y),$.set(Y,X),Y.addEventListener("dispose",Z),N(X.texture,Y.mapping)}else return null}}return Y}function Z(Y){let K=Y.target;K.removeEventListener("dispose",Z);let H=$.get(K);if(H!==void 0)$.delete(K),H.dispose()}function W(){$=new WeakMap}return{get:Q,dispose:W}}var TJ=4,ZZ=[0.125,0.215,0.35,0.446,0.526,0.582],r9=20,e$=new N7,WZ=new i0,JN=null,$N=0,NN=0,QN=!1,a9=(1+Math.sqrt(5))/2,AJ=1/a9,KZ=[new h(-a9,AJ,0),new h(a9,AJ,0),new h(-AJ,0,a9),new h(AJ,0,a9),new h(0,a9,-AJ),new h(0,a9,AJ),new h(-1,1,-1),new h(1,1,-1),new h(-1,1,1),new h(1,1,1)],bY=new h;class WN{constructor(J){this._renderer=J,this._pingPongRenderTarget=null,this._lodMax=0,this._cubeSize=0,this._lodPlanes=[],this._sizeLods=[],this._sigmas=[],this._blurMaterial=null,this._cubemapMaterial=null,this._equirectMaterial=null,this._compileMaterial(this._blurMaterial)}fromScene(J,$=0,N=0.1,Q=100,Z={}){let{size:W=256,position:Y=bY}=Z;JN=this._renderer.getRenderTarget(),$N=this._renderer.getActiveCubeFace(),NN=this._renderer.getActiveMipmapLevel(),QN=this._renderer.xr.enabled,this._renderer.xr.enabled=!1,this._setSize(W);let K=this._allocateTargets();if(K.depthBuffer=!0,this._sceneToCubeUV(J,N,Q,K,Y),$>0)this._blur(K,0,0,$);return this._applyPMREM(K),this._cleanup(K),K}fromEquirectangular(J,$=null){return this._fromTexture(J,$)}fromCubemap(J,$=null){return this._fromTexture(J,$)}compileCubemapShader(){if(this._cubemapMaterial===null)this._cubemapMaterial=XZ(),this._compileMaterial(this._cubemapMaterial)}compileEquirectangularShader(){if(this._equirectMaterial===null)this._equirectMaterial=HZ(),this._compileMaterial(this._equirectMaterial)}dispose(){if(this._dispose(),this._cubemapMaterial!==null)this._cubemapMaterial.dispose();if(this._equirectMaterial!==null)this._equirectMaterial.dispose()}_setSize(J){this._lodMax=Math.floor(Math.log2(J)),this._cubeSize=Math.pow(2,this._lodMax)}_dispose(){if(this._blurMaterial!==null)this._blurMaterial.dispose();if(this._pingPongRenderTarget!==null)this._pingPongRenderTarget.dispose();for(let J=0;J<this._lodPlanes.length;J++)this._lodPlanes[J].dispose()}_cleanup(J){this._renderer.setRenderTarget(JN,$N,NN),this._renderer.xr.enabled=QN,J.scissorTest=!1,Y7(J,0,0,J.width,J.height)}_fromTexture(J,$){if(J.mapping===MJ||J.mapping===p9)this._setSize(J.image.length===0?16:J.image[0].width||J.image[0].image.width);else this._setSize(J.image.width/4);JN=this._renderer.getRenderTarget(),$N=this._renderer.getActiveCubeFace(),NN=this._renderer.getActiveMipmapLevel(),QN=this._renderer.xr.enabled,this._renderer.xr.enabled=!1;let N=$||this._allocateTargets();return this._textureToCubeUV(J,N),this._applyPMREM(N),this._cleanup(N),N}_allocateTargets(){let J=3*Math.max(this._cubeSize,112),$=4*this._cubeSize,N={magFilter:d9,minFilter:d9,generateMipmaps:!1,type:nJ,format:K9,colorSpace:iJ,depthBuffer:!1},Q=YZ(J,$,N);if(this._pingPongRenderTarget===null||this._pingPongRenderTarget.width!==J||this._pingPongRenderTarget.height!==$){if(this._pingPongRenderTarget!==null)this._dispose();this._pingPongRenderTarget=YZ(J,$,N);let{_lodMax:Z}=this;({sizeLods:this._sizeLods,lodPlanes:this._lodPlanes,sigmas:this._sigmas}=yY(Z)),this._blurMaterial=jY(Z,J,$)}return Q}_compileMaterial(J){let $=new b8(this._lodPlanes[0],J);this._renderer.compile($,e$)}_sceneToCubeUV(J,$,N,Q,Z){let K=new T8(90,1,$,N),H=[1,-1,1,1,1,1],X=[1,1,1,-1,-1,-1],G=this._renderer,E=G.autoClear,q=G.toneMapping;if(G.getClearColor(WZ),G.toneMapping=M9,G.autoClear=!1,G.state.buffers.depth.getReversed())G.setRenderTarget(Q),G.clearDepth(),G.setRenderTarget(null);let M=new u9({name:"PMREM.Background",side:m8,depthWrite:!1,depthTest:!1}),_=new b8(new CJ,M),D=!1,U=J.background;if(U){if(U.isColor)M.color.copy(U),J.background=null,D=!0}else M.color.copy(WZ),D=!0;for(let C=0;C<6;C++){let L=C%3;if(L===0)K.up.set(0,H[C],0),K.position.set(Z.x,Z.y,Z.z),K.lookAt(Z.x+X[C],Z.y,Z.z);else if(L===1)K.up.set(0,0,H[C]),K.position.set(Z.x,Z.y,Z.z),K.lookAt(Z.x,Z.y+X[C],Z.z);else K.up.set(0,H[C],0),K.position.set(Z.x,Z.y,Z.z),K.lookAt(Z.x,Z.y,Z.z+X[C]);let B=this._cubeSize;if(Y7(Q,L*B,C>2?B:0,B,B),G.setRenderTarget(Q),D)G.render(_,K);G.render(J,K)}_.geometry.dispose(),_.material.dispose(),G.toneMapping=q,G.autoClear=E,J.background=U}_textureToCubeUV(J,$){let N=this._renderer,Q=J.mapping===MJ||J.mapping===p9;if(Q){if(this._cubemapMaterial===null)this._cubemapMaterial=XZ();this._cubemapMaterial.uniforms.flipEnvMap.value=J.isRenderTargetTexture===!1?-1:1}else if(this._equirectMaterial===null)this._equirectMaterial=HZ();let Z=Q?this._cubemapMaterial:this._equirectMaterial,W=new b8(this._lodPlanes[0],Z),Y=Z.uniforms;Y.envMap.value=J;let K=this._cubeSize;Y7($,0,0,3*K,2*K),N.setRenderTarget($),N.render(W,e$)}_applyPMREM(J){let $=this._renderer,N=$.autoClear;$.autoClear=!1;let Q=this._lodPlanes.length;for(let Z=1;Z<Q;Z++){let W=Math.sqrt(this._sigmas[Z]*this._sigmas[Z]-this._sigmas[Z-1]*this._sigmas[Z-1]),Y=KZ[(Q-Z-1)%KZ.length];this._blur(J,Z-1,Z,W,Y)}$.autoClear=N}_blur(J,$,N,Q,Z){let W=this._pingPongRenderTarget;this._halfBlur(J,W,$,N,Q,"latitudinal",Z),this._halfBlur(W,J,N,N,Q,"longitudinal",Z)}_halfBlur(J,$,N,Q,Z,W,Y){let K=this._renderer,H=this._blurMaterial;if(W!=="latitudinal"&&W!=="longitudinal")console.error("blur direction must be either latitudinal or longitudinal!");let X=3,G=new b8(this._lodPlanes[Q],H),E=H.uniforms,q=this._sizeLods[N]-1,k=isFinite(Z)?Math.PI/(2*q):2*Math.PI/(2*r9-1),M=Z/k,_=isFinite(Z)?1+Math.floor(X*M):r9;if(_>r9)console.warn(`sigmaRadians, ${Z}, is too large and will clip, as it requested ${_} samples when the maximum is set to ${r9}`);let D=[],U=0;for(let I=0;I<r9;++I){let b=I/M,g=Math.exp(-b*b/2);if(D.push(g),I===0)U+=g;else if(I<_)U+=2*g}for(let I=0;I<D.length;I++)D[I]=D[I]/U;if(E.envMap.value=J.texture,E.samples.value=_,E.weights.value=D,E.latitudinal.value=W==="latitudinal",Y)E.poleAxis.value=Y;let{_lodMax:C}=this;E.dTheta.value=k,E.mipInt.value=C-N;let L=this._sizeLods[Q],B=3*L*(Q>C-TJ?Q-C+TJ:0),y=4*(this._cubeSize-L);Y7($,B,y,3*L,2*L),K.setRenderTarget($),K.render(G,e$)}}function yY(J){let $=[],N=[],Q=[],Z=J,W=J-TJ+1+ZZ.length;for(let Y=0;Y<W;Y++){let K=Math.pow(2,Z);N.push(K);let H=1/K;if(Y>J-TJ)H=ZZ[Y-J+TJ-1];else if(Y===0)H=0;Q.push(H);let X=1/(K-2),G=-X,E=1+X,q=[G,G,E,G,E,E,G,G,E,E,G,E],k=6,M=6,_=3,D=2,U=1,C=new Float32Array(_*M*k),L=new Float32Array(D*M*k),B=new Float32Array(U*M*k);for(let I=0;I<k;I++){let b=I%3*2/3-1,g=I>2?0:-1,V=[b,g,0,b+0.6666666666666666,g,0,b+0.6666666666666666,g+1,0,b,g,0,b+0.6666666666666666,g+1,0,b,g+1,0];C.set(V,_*M*I),L.set(q,D*M*I);let F=[I,I,I,I,I,I];B.set(F,U*M*I)}let y=new u8;if(y.setAttribute("position",new o8(C,_)),y.setAttribute("uv",new o8(L,D)),y.setAttribute("faceIndex",new o8(B,U)),$.push(y),Z>TJ)Z--}return{lodPlanes:$,sizeLods:N,sigmas:Q}}function YZ(J,$,N){let Q=new L9(J,$,N);return Q.texture.mapping=mJ,Q.texture.name="PMREM.cubeUv",Q.scissorTest=!0,Q}function Y7(J,$,N,Q,Z){J.viewport.set($,N,Q,Z),J.scissor.set($,N,Q,Z)}function jY(J,$,N){let Q=new Float32Array(r9),Z=new h(0,1,0);return new H9({name:"SphericalGaussianBlur",defines:{n:r9,CUBEUV_TEXEL_WIDTH:1/$,CUBEUV_TEXEL_HEIGHT:1/N,CUBEUV_MAX_MIP:`${J}.0`},uniforms:{envMap:{value:null},samples:{value:1},weights:{value:Q},latitudinal:{value:!1},dTheta:{value:0},mipInt:{value:0},poleAxis:{value:Z}},vertexShader:YN(),fragmentShader:`

			precision mediump float;
			precision mediump int;

			varying vec3 vOutputDirection;

			uniform sampler2D envMap;
			uniform int samples;
			uniform float weights[ n ];
			uniform bool latitudinal;
			uniform float dTheta;
			uniform float mipInt;
			uniform vec3 poleAxis;

			#define ENVMAP_TYPE_CUBE_UV
			#include <cube_uv_reflection_fragment>

			vec3 getSample( float theta, vec3 axis ) {

				float cosTheta = cos( theta );
				// Rodrigues' axis-angle rotation
				vec3 sampleDirection = vOutputDirection * cosTheta
					+ cross( axis, vOutputDirection ) * sin( theta )
					+ axis * dot( axis, vOutputDirection ) * ( 1.0 - cosTheta );

				return bilinearCubeUV( envMap, sampleDirection, mipInt );

			}

			void main() {

				vec3 axis = latitudinal ? poleAxis : cross( poleAxis, vOutputDirection );

				if ( all( equal( axis, vec3( 0.0 ) ) ) ) {

					axis = vec3( vOutputDirection.z, 0.0, - vOutputDirection.x );

				}

				axis = normalize( axis );

				gl_FragColor = vec4( 0.0, 0.0, 0.0, 1.0 );
				gl_FragColor.rgb += weights[ 0 ] * getSample( 0.0, axis );

				for ( int i = 1; i < n; i++ ) {

					if ( i >= samples ) {

						break;

					}

					float theta = dTheta * float( i );
					gl_FragColor.rgb += weights[ i ] * getSample( -1.0 * theta, axis );
					gl_FragColor.rgb += weights[ i ] * getSample( theta, axis );

				}

			}
		`,blending:T9,depthTest:!1,depthWrite:!1})}function HZ(){return new H9({name:"EquirectangularToCubeUV",uniforms:{envMap:{value:null}},vertexShader:YN(),fragmentShader:`

			precision mediump float;
			precision mediump int;

			varying vec3 vOutputDirection;

			uniform sampler2D envMap;

			#include <common>

			void main() {

				vec3 outputDirection = normalize( vOutputDirection );
				vec2 uv = equirectUv( outputDirection );

				gl_FragColor = vec4( texture2D ( envMap, uv ).rgb, 1.0 );

			}
		`,blending:T9,depthTest:!1,depthWrite:!1})}function XZ(){return new H9({name:"CubemapToCubeUV",uniforms:{envMap:{value:null},flipEnvMap:{value:-1}},vertexShader:YN(),fragmentShader:`

			precision mediump float;
			precision mediump int;

			uniform float flipEnvMap;

			varying vec3 vOutputDirection;

			uniform samplerCube envMap;

			void main() {

				gl_FragColor = textureCube( envMap, vec3( flipEnvMap * vOutputDirection.x, vOutputDirection.yz ) );

			}
		`,blending:T9,depthTest:!1,depthWrite:!1})}function YN(){return`

		precision mediump float;
		precision mediump int;

		attribute float faceIndex;

		varying vec3 vOutputDirection;

		// RH coordinate system; PMREM face-indexing convention
		vec3 getDirection( vec2 uv, float face ) {

			uv = 2.0 * uv - 1.0;

			vec3 direction = vec3( uv, 1.0 );

			if ( face == 0.0 ) {

				direction = direction.zyx; // ( 1, v, u ) pos x

			} else if ( face == 1.0 ) {

				direction = direction.xzy;
				direction.xz *= -1.0; // ( -u, 1, -v ) pos y

			} else if ( face == 2.0 ) {

				direction.x *= -1.0; // ( -u, v, 1 ) pos z

			} else if ( face == 3.0 ) {

				direction = direction.zyx;
				direction.xz *= -1.0; // ( -1, v, -u ) neg x

			} else if ( face == 4.0 ) {

				direction = direction.xzy;
				direction.xy *= -1.0; // ( -u, -1, v ) neg y

			} else if ( face == 5.0 ) {

				direction.z *= -1.0; // ( u, v, -1 ) neg z

			}

			return direction;

		}

		void main() {

			vOutputDirection = getDirection( uv, faceIndex );
			gl_Position = vec4( position, 1.0 );

		}
	`}function fY(J){let $=new WeakMap,N=null;function Q(K){if(K&&K.isTexture){let H=K.mapping,X=H===f6||H===v6,G=H===MJ||H===p9;if(X||G){let E=$.get(K),q=E!==void 0?E.texture.pmremVersion:0;if(K.isRenderTargetTexture&&K.pmremVersion!==q){if(N===null)N=new WN(J);return E=X?N.fromEquirectangular(K,E):N.fromCubemap(K,E),E.texture.pmremVersion=K.pmremVersion,$.set(K,E),E.texture}else if(E!==void 0)return E.texture;else{let k=K.image;if(X&&k&&k.height>0||G&&k&&Z(k)){if(N===null)N=new WN(J);return E=X?N.fromEquirectangular(K):N.fromCubemap(K),E.texture.pmremVersion=K.pmremVersion,$.set(K,E),K.addEventListener("dispose",W),E.texture}else return null}}}return K}function Z(K){let H=0,X=6;for(let G=0;G<X;G++)if(K[G]!==void 0)H++;return H===X}function W(K){let H=K.target;H.removeEventListener("dispose",W);let X=$.get(H);if(X!==void 0)$.delete(H),X.dispose()}function Y(){if($=new WeakMap,N!==null)N.dispose(),N=null}return{get:Q,dispose:Y}}function vY(J){let $={};function N(Q){if($[Q]!==void 0)return $[Q];let Z;switch(Q){case"WEBGL_depth_texture":Z=J.getExtension("WEBGL_depth_texture")||J.getExtension("MOZ_WEBGL_depth_texture")||J.getExtension("WEBKIT_WEBGL_depth_texture");break;case"EXT_texture_filter_anisotropic":Z=J.getExtension("EXT_texture_filter_anisotropic")||J.getExtension("MOZ_EXT_texture_filter_anisotropic")||J.getExtension("WEBKIT_EXT_texture_filter_anisotropic");break;case"WEBGL_compressed_texture_s3tc":Z=J.getExtension("WEBGL_compressed_texture_s3tc")||J.getExtension("MOZ_WEBGL_compressed_texture_s3tc")||J.getExtension("WEBKIT_WEBGL_compressed_texture_s3tc");break;case"WEBGL_compressed_texture_pvrtc":Z=J.getExtension("WEBGL_compressed_texture_pvrtc")||J.getExtension("WEBKIT_WEBGL_compressed_texture_pvrtc");break;default:Z=J.getExtension(Q)}return $[Q]=Z,Z}return{has:function(Q){return N(Q)!==null},init:function(){N("EXT_color_buffer_float"),N("WEBGL_clip_cull_distance"),N("OES_texture_float_linear"),N("EXT_color_buffer_half_float"),N("WEBGL_multisampled_render_to_texture"),N("WEBGL_render_shared_exponent")},get:function(Q){let Z=N(Q);if(Z===null)OJ("THREE.WebGLRenderer: "+Q+" extension not supported.");return Z}}}function hY(J,$,N,Q){let Z={},W=new WeakMap;function Y(E){let q=E.target;if(q.index!==null)$.remove(q.index);for(let M in q.attributes)$.remove(q.attributes[M]);q.removeEventListener("dispose",Y),delete Z[q.id];let k=W.get(q);if(k)$.remove(k),W.delete(q);if(Q.releaseStatesOfGeometry(q),q.isInstancedBufferGeometry===!0)delete q._maxInstanceCount;N.memory.geometries--}function K(E,q){if(Z[q.id]===!0)return q;return q.addEventListener("dispose",Y),Z[q.id]=!0,N.memory.geometries++,q}function H(E){let q=E.attributes;for(let k in q)$.update(q[k],J.ARRAY_BUFFER)}function X(E){let q=[],k=E.index,M=E.attributes.position,_=0;if(k!==null){let C=k.array;_=k.version;for(let L=0,B=C.length;L<B;L+=3){let y=C[L+0],I=C[L+1],b=C[L+2];q.push(y,I,I,b,b,y)}}else if(M!==void 0){let C=M.array;_=M.version;for(let L=0,B=C.length/3-1;L<B;L+=3){let y=L+0,I=L+1,b=L+2;q.push(y,I,I,b,b,y)}}else return;let D=new((S$(q))?c6:u6)(q,1);D.version=_;let U=W.get(E);if(U)$.remove(U);W.set(E,D)}function G(E){let q=W.get(E);if(q){let k=E.index;if(k!==null){if(q.version<k.version)X(E)}}else X(E);return W.get(E)}return{get:K,update:H,getWireframeAttribute:G}}function xY(J,$,N){let Q;function Z(q){Q=q}let W,Y;function K(q){W=q.type,Y=q.bytesPerElement}function H(q,k){J.drawElements(Q,k,W,q*Y),N.update(k,Q,1)}function X(q,k,M){if(M===0)return;J.drawElementsInstanced(Q,k,W,q*Y,M),N.update(k,Q,M)}function G(q,k,M){if(M===0)return;$.get("WEBGL_multi_draw").multiDrawElementsWEBGL(Q,k,0,W,q,0,M);let D=0;for(let U=0;U<M;U++)D+=k[U];N.update(D,Q,1)}function E(q,k,M,_){if(M===0)return;let D=$.get("WEBGL_multi_draw");if(D===null)for(let U=0;U<q.length;U++)X(q[U]/Y,k[U],_[U]);else{D.multiDrawElementsInstancedWEBGL(Q,k,0,W,q,0,_,0,M);let U=0;for(let C=0;C<M;C++)U+=k[C]*_[C];N.update(U,Q,1)}}this.setMode=Z,this.setIndex=K,this.render=H,this.renderInstances=X,this.renderMultiDraw=G,this.renderMultiDrawInstances=E}function gY(J){let $={geometries:0,textures:0},N={frame:0,calls:0,triangles:0,points:0,lines:0};function Q(W,Y,K){switch(N.calls++,Y){case J.TRIANGLES:N.triangles+=K*(W/3);break;case J.LINES:N.lines+=K*(W/2);break;case J.LINE_STRIP:N.lines+=K*(W-1);break;case J.LINE_LOOP:N.lines+=K*W;break;case J.POINTS:N.points+=K*W;break;default:console.error("THREE.WebGLInfo: Unknown draw mode:",Y);break}}function Z(){N.calls=0,N.triangles=0,N.points=0,N.lines=0}return{memory:$,render:N,programs:null,autoReset:!0,reset:Z,update:Q}}function pY(J,$,N){let Q=new WeakMap,Z=new D8;function W(Y,K,H){let X=Y.morphTargetInfluences,G=K.morphAttributes.position||K.morphAttributes.normal||K.morphAttributes.color,E=G!==void 0?G.length:0,q=Q.get(K);if(q===void 0||q.count!==E){let V=function(){b.dispose(),Q.delete(K),K.removeEventListener("dispose",V)};if(q!==void 0)q.texture.dispose();let k=K.morphAttributes.position!==void 0,M=K.morphAttributes.normal!==void 0,_=K.morphAttributes.color!==void 0,D=K.morphAttributes.position||[],U=K.morphAttributes.normal||[],C=K.morphAttributes.color||[],L=0;if(k===!0)L=1;if(M===!0)L=2;if(_===!0)L=3;let B=K.attributes.position.count*L,y=1;if(B>$.maxTextureSize)y=Math.ceil(B/$.maxTextureSize),B=$.maxTextureSize;let I=new Float32Array(B*y*4*E),b=new m6(I,B,y,E);b.type=b9,b.needsUpdate=!0;let g=L*4;for(let F=0;F<E;F++){let S=D[F],s=U[F],i=C[F],m=B*y*4*F;for(let T=0;T<S.count;T++){let p=T*g;if(k===!0)Z.fromBufferAttribute(S,T),I[m+p+0]=Z.x,I[m+p+1]=Z.y,I[m+p+2]=Z.z,I[m+p+3]=0;if(M===!0)Z.fromBufferAttribute(s,T),I[m+p+4]=Z.x,I[m+p+5]=Z.y,I[m+p+6]=Z.z,I[m+p+7]=0;if(_===!0)Z.fromBufferAttribute(i,T),I[m+p+8]=Z.x,I[m+p+9]=Z.y,I[m+p+10]=Z.z,I[m+p+11]=i.itemSize===4?Z.w:1}}q={count:E,texture:b,size:new p0(B,y)},Q.set(K,q),K.addEventListener("dispose",V)}if(Y.isInstancedMesh===!0&&Y.morphTexture!==null)H.getUniforms().setValue(J,"morphTexture",Y.morphTexture,N);else{let k=0;for(let _=0;_<X.length;_++)k+=X[_];let M=K.morphTargetsRelative?1:1-k;H.getUniforms().setValue(J,"morphTargetBaseInfluence",M),H.getUniforms().setValue(J,"morphTargetInfluences",X)}H.getUniforms().setValue(J,"morphTargetsTexture",q.texture,N),H.getUniforms().setValue(J,"morphTargetsTextureSize",q.size)}return{update:W}}function dY(J,$,N,Q){let Z=new WeakMap;function W(H){let X=Q.render.frame,G=H.geometry,E=$.get(H,G);if(Z.get(E)!==X)$.update(E),Z.set(E,X);if(H.isInstancedMesh){if(H.hasEventListener("dispose",K)===!1)H.addEventListener("dispose",K);if(Z.get(H)!==X){if(N.update(H.instanceMatrix,J.ARRAY_BUFFER),H.instanceColor!==null)N.update(H.instanceColor,J.ARRAY_BUFFER);Z.set(H,X)}}if(H.isSkinnedMesh){let q=H.skeleton;if(Z.get(q)!==X)q.update(),Z.set(q,X)}return E}function Y(){Z=new WeakMap}function K(H){let X=H.target;if(X.removeEventListener("dispose",K),N.remove(X.instanceMatrix),X.instanceColor!==null)N.remove(X.instanceColor)}return{update:W,dispose:Y}}var PZ=new S8,UZ=new a6(1,1),AZ=new m6,TZ=new j$,SZ=new s6,GZ=[],EZ=[],DZ=new Float32Array(16),qZ=new Float32Array(9),kZ=new Float32Array(4);function SJ(J,$,N){let Q=J[0];if(Q<=0||Q>0)return J;let Z=$*N,W=GZ[Z];if(W===void 0)W=new Float32Array(Z),GZ[Z]=W;if($!==0){Q.toArray(W,0);for(let Y=1,K=0;Y!==$;++Y)K+=N,J[Y].toArray(W,K)}return W}function M8(J,$){if(J.length!==$.length)return!1;for(let N=0,Q=J.length;N<Q;N++)if(J[N]!==$[N])return!1;return!0}function _8(J,$){for(let N=0,Q=$.length;N<Q;N++)J[N]=$[N]}function X7(J,$){let N=EZ[$];if(N===void 0)N=new Int32Array($),EZ[$]=N;for(let Q=0;Q!==$;++Q)N[Q]=J.allocateTextureUnit();return N}function lY(J,$){let N=this.cache;if(N[0]===$)return;J.uniform1f(this.addr,$),N[0]=$}function mY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y)J.uniform2f(this.addr,$.x,$.y),N[0]=$.x,N[1]=$.y}else{if(M8(N,$))return;J.uniform2fv(this.addr,$),_8(N,$)}}function uY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z)J.uniform3f(this.addr,$.x,$.y,$.z),N[0]=$.x,N[1]=$.y,N[2]=$.z}else if($.r!==void 0){if(N[0]!==$.r||N[1]!==$.g||N[2]!==$.b)J.uniform3f(this.addr,$.r,$.g,$.b),N[0]=$.r,N[1]=$.g,N[2]=$.b}else{if(M8(N,$))return;J.uniform3fv(this.addr,$),_8(N,$)}}function cY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z||N[3]!==$.w)J.uniform4f(this.addr,$.x,$.y,$.z,$.w),N[0]=$.x,N[1]=$.y,N[2]=$.z,N[3]=$.w}else{if(M8(N,$))return;J.uniform4fv(this.addr,$),_8(N,$)}}function nY(J,$){let N=this.cache,Q=$.elements;if(Q===void 0){if(M8(N,$))return;J.uniformMatrix2fv(this.addr,!1,$),_8(N,$)}else{if(M8(N,Q))return;kZ.set(Q),J.uniformMatrix2fv(this.addr,!1,kZ),_8(N,Q)}}function sY(J,$){let N=this.cache,Q=$.elements;if(Q===void 0){if(M8(N,$))return;J.uniformMatrix3fv(this.addr,!1,$),_8(N,$)}else{if(M8(N,Q))return;qZ.set(Q),J.uniformMatrix3fv(this.addr,!1,qZ),_8(N,Q)}}function iY(J,$){let N=this.cache,Q=$.elements;if(Q===void 0){if(M8(N,$))return;J.uniformMatrix4fv(this.addr,!1,$),_8(N,$)}else{if(M8(N,Q))return;DZ.set(Q),J.uniformMatrix4fv(this.addr,!1,DZ),_8(N,Q)}}function oY(J,$){let N=this.cache;if(N[0]===$)return;J.uniform1i(this.addr,$),N[0]=$}function aY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y)J.uniform2i(this.addr,$.x,$.y),N[0]=$.x,N[1]=$.y}else{if(M8(N,$))return;J.uniform2iv(this.addr,$),_8(N,$)}}function rY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z)J.uniform3i(this.addr,$.x,$.y,$.z),N[0]=$.x,N[1]=$.y,N[2]=$.z}else{if(M8(N,$))return;J.uniform3iv(this.addr,$),_8(N,$)}}function tY(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z||N[3]!==$.w)J.uniform4i(this.addr,$.x,$.y,$.z,$.w),N[0]=$.x,N[1]=$.y,N[2]=$.z,N[3]=$.w}else{if(M8(N,$))return;J.uniform4iv(this.addr,$),_8(N,$)}}function eY(J,$){let N=this.cache;if(N[0]===$)return;J.uniform1ui(this.addr,$),N[0]=$}function JH(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y)J.uniform2ui(this.addr,$.x,$.y),N[0]=$.x,N[1]=$.y}else{if(M8(N,$))return;J.uniform2uiv(this.addr,$),_8(N,$)}}function $H(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z)J.uniform3ui(this.addr,$.x,$.y,$.z),N[0]=$.x,N[1]=$.y,N[2]=$.z}else{if(M8(N,$))return;J.uniform3uiv(this.addr,$),_8(N,$)}}function NH(J,$){let N=this.cache;if($.x!==void 0){if(N[0]!==$.x||N[1]!==$.y||N[2]!==$.z||N[3]!==$.w)J.uniform4ui(this.addr,$.x,$.y,$.z,$.w),N[0]=$.x,N[1]=$.y,N[2]=$.z,N[3]=$.w}else{if(M8(N,$))return;J.uniform4uiv(this.addr,$),_8(N,$)}}function QH(J,$,N){let Q=this.cache,Z=N.allocateTextureUnit();if(Q[0]!==Z)J.uniform1i(this.addr,Z),Q[0]=Z;let W;if(this.type===J.SAMPLER_2D_SHADOW)UZ.compareFunction=C$,W=UZ;else W=PZ;N.setTexture2D($||W,Z)}function ZH(J,$,N){let Q=this.cache,Z=N.allocateTextureUnit();if(Q[0]!==Z)J.uniform1i(this.addr,Z),Q[0]=Z;N.setTexture3D($||TZ,Z)}function WH(J,$,N){let Q=this.cache,Z=N.allocateTextureUnit();if(Q[0]!==Z)J.uniform1i(this.addr,Z),Q[0]=Z;N.setTextureCube($||SZ,Z)}function KH(J,$,N){let Q=this.cache,Z=N.allocateTextureUnit();if(Q[0]!==Z)J.uniform1i(this.addr,Z),Q[0]=Z;N.setTexture2DArray($||AZ,Z)}function YH(J){switch(J){case 5126:return lY;case 35664:return mY;case 35665:return uY;case 35666:return cY;case 35674:return nY;case 35675:return sY;case 35676:return iY;case 5124:case 35670:return oY;case 35667:case 35671:return aY;case 35668:case 35672:return rY;case 35669:case 35673:return tY;case 5125:return eY;case 36294:return JH;case 36295:return $H;case 36296:return NH;case 35678:case 36198:case 36298:case 36306:case 35682:return QH;case 35679:case 36299:case 36307:return ZH;case 35680:case 36300:case 36308:case 36293:return WH;case 36289:case 36303:case 36311:case 36292:return KH}}function HH(J,$){J.uniform1fv(this.addr,$)}function XH(J,$){let N=SJ($,this.size,2);J.uniform2fv(this.addr,N)}function UH(J,$){let N=SJ($,this.size,3);J.uniform3fv(this.addr,N)}function GH(J,$){let N=SJ($,this.size,4);J.uniform4fv(this.addr,N)}function EH(J,$){let N=SJ($,this.size,4);J.uniformMatrix2fv(this.addr,!1,N)}function DH(J,$){let N=SJ($,this.size,9);J.uniformMatrix3fv(this.addr,!1,N)}function qH(J,$){let N=SJ($,this.size,16);J.uniformMatrix4fv(this.addr,!1,N)}function kH(J,$){J.uniform1iv(this.addr,$)}function OH(J,$){J.uniform2iv(this.addr,$)}function RH(J,$){J.uniform3iv(this.addr,$)}function FH(J,$){J.uniform4iv(this.addr,$)}function MH(J,$){J.uniform1uiv(this.addr,$)}function _H(J,$){J.uniform2uiv(this.addr,$)}function LH(J,$){J.uniform3uiv(this.addr,$)}function VH(J,$){J.uniform4uiv(this.addr,$)}function wH(J,$,N){let Q=this.cache,Z=$.length,W=X7(N,Z);if(!M8(Q,W))J.uniform1iv(this.addr,W),_8(Q,W);for(let Y=0;Y!==Z;++Y)N.setTexture2D($[Y]||PZ,W[Y])}function zH(J,$,N){let Q=this.cache,Z=$.length,W=X7(N,Z);if(!M8(Q,W))J.uniform1iv(this.addr,W),_8(Q,W);for(let Y=0;Y!==Z;++Y)N.setTexture3D($[Y]||TZ,W[Y])}function BH(J,$,N){let Q=this.cache,Z=$.length,W=X7(N,Z);if(!M8(Q,W))J.uniform1iv(this.addr,W),_8(Q,W);for(let Y=0;Y!==Z;++Y)N.setTextureCube($[Y]||SZ,W[Y])}function CH(J,$,N){let Q=this.cache,Z=$.length,W=X7(N,Z);if(!M8(Q,W))J.uniform1iv(this.addr,W),_8(Q,W);for(let Y=0;Y!==Z;++Y)N.setTexture2DArray($[Y]||AZ,W[Y])}function IH(J){switch(J){case 5126:return HH;case 35664:return XH;case 35665:return UH;case 35666:return GH;case 35674:return EH;case 35675:return DH;case 35676:return qH;case 5124:case 35670:return kH;case 35667:case 35671:return OH;case 35668:case 35672:return RH;case 35669:case 35673:return FH;case 5125:return MH;case 36294:return _H;case 36295:return LH;case 36296:return VH;case 35678:case 36198:case 36298:case 36306:case 35682:return wH;case 35679:case 36299:case 36307:return zH;case 35680:case 36300:case 36308:case 36293:return BH;case 36289:case 36303:case 36311:case 36292:return CH}}class bZ{constructor(J,$,N){this.id=J,this.addr=N,this.cache=[],this.type=$.type,this.setValue=YH($.type)}}class yZ{constructor(J,$,N){this.id=J,this.addr=N,this.cache=[],this.type=$.type,this.size=$.size,this.setValue=IH($.type)}}class jZ{constructor(J){this.id=J,this.seq=[],this.map={}}setValue(J,$,N){let Q=this.seq;for(let Z=0,W=Q.length;Z!==W;++Z){let Y=Q[Z];Y.setValue(J,$[Y.id],N)}}}var ZN=/(\w+)(\])?(\[|\.)?/g;function OZ(J,$){J.seq.push($),J.map[$.id]=$}function PH(J,$,N){let Q=J.name,Z=Q.length;ZN.lastIndex=0;while(!0){let W=ZN.exec(Q),Y=ZN.lastIndex,K=W[1],H=W[2]==="]",X=W[3];if(H)K=K|0;if(X===void 0||X==="["&&Y+2===Z){OZ(N,X===void 0?new bZ(K,J,$):new yZ(K,J,$));break}else{let E=N.map[K];if(E===void 0)E=new jZ(K),OZ(N,E);N=E}}}class Q6{constructor(J,$){this.seq=[],this.map={};let N=J.getProgramParameter($,J.ACTIVE_UNIFORMS);for(let Q=0;Q<N;++Q){let Z=J.getActiveUniform($,Q),W=J.getUniformLocation($,Z.name);PH(Z,W,this)}}setValue(J,$,N,Q){let Z=this.map[$];if(Z!==void 0)Z.setValue(J,N,Q)}setOptional(J,$,N){let Q=$[N];if(Q!==void 0)this.setValue(J,N,Q)}static upload(J,$,N,Q){for(let Z=0,W=$.length;Z!==W;++Z){let Y=$[Z],K=N[Y.id];if(K.needsUpdate!==!1)Y.setValue(J,K.value,Q)}}static seqWithValue(J,$){let N=[];for(let Q=0,Z=J.length;Q!==Z;++Q){let W=J[Q];if(W.id in $)N.push(W)}return N}}function RZ(J,$,N){let Q=J.createShader($);return J.shaderSource(Q,N),J.compileShader(Q),Q}var AH=37297,TH=0;function SH(J,$){let N=J.split(`
`),Q=[],Z=Math.max($-6,0),W=Math.min($+6,N.length);for(let Y=Z;Y<W;Y++){let K=Y+1;Q.push(`${K===$?">":" "} ${K}: ${N[Y]}`)}return Q.join(`
`)}var FZ=new l0;function bH(J){o0._getMatrix(FZ,o0.workingColorSpace,J);let $=`mat3( ${FZ.elements.map((N)=>N.toFixed(4))} )`;switch(o0.getTransfer(J)){case B$:return[$,"LinearTransferOETF"];case Y8:return[$,"sRGBTransferOETF"];default:return console.warn("THREE.WebGLProgram: Unsupported color space: ",J),[$,"LinearTransferOETF"]}}function MZ(J,$,N){let Q=J.getShaderParameter($,J.COMPILE_STATUS),W=(J.getShaderInfoLog($)||"").trim();if(Q&&W==="")return"";let Y=/ERROR: 0:(\d+)/.exec(W);if(Y){let K=parseInt(Y[1]);return N.toUpperCase()+`

`+W+`

`+SH(J.getShaderSource($),K)}else return W}function yH(J,$){let N=bH($);return[`vec4 ${J}( vec4 value ) {`,`	return ${N[1]}( vec4( value.rgb * ${N[0]}, value.a ) );`,"}"].join(`
`)}function jH(J,$){let N;switch($){case _Q:N="Linear";break;case LQ:N="Reinhard";break;case VQ:N="Cineon";break;case wQ:N="ACESFilmic";break;case BQ:N="AgX";break;case CQ:N="Neutral";break;case zQ:N="Custom";break;default:console.warn("THREE.WebGLProgram: Unsupported toneMapping:",$),N="Linear"}return"vec3 "+J+"( vec3 color ) { return "+N+"ToneMapping( color ); }"}var H7=new h;function fH(){o0.getLuminanceCoefficients(H7);let J=H7.x.toFixed(4),$=H7.y.toFixed(4),N=H7.z.toFixed(4);return["float luminance( const in vec3 rgb ) {",`	const vec3 weights = vec3( ${J}, ${$}, ${N} );`,"\treturn dot( weights, rgb );","}"].join(`
`)}function vH(J){return[J.extensionClipCullDistance?"#extension GL_ANGLE_clip_cull_distance : require":"",J.extensionMultiDraw?"#extension GL_ANGLE_multi_draw : require":""].filter(N6).join(`
`)}function hH(J){let $=[];for(let N in J){let Q=J[N];if(Q===!1)continue;$.push("#define "+N+" "+Q)}return $.join(`
`)}function xH(J,$){let N={},Q=J.getProgramParameter($,J.ACTIVE_ATTRIBUTES);for(let Z=0;Z<Q;Z++){let W=J.getActiveAttrib($,Z),Y=W.name,K=1;if(W.type===J.FLOAT_MAT2)K=2;if(W.type===J.FLOAT_MAT3)K=3;if(W.type===J.FLOAT_MAT4)K=4;N[Y]={type:W.type,location:J.getAttribLocation($,Y),locationSize:K}}return N}function N6(J){return J!==""}function _Z(J,$){let N=$.numSpotLightShadows+$.numSpotLightMaps-$.numSpotLightShadowsWithMaps;return J.replace(/NUM_DIR_LIGHTS/g,$.numDirLights).replace(/NUM_SPOT_LIGHTS/g,$.numSpotLights).replace(/NUM_SPOT_LIGHT_MAPS/g,$.numSpotLightMaps).replace(/NUM_SPOT_LIGHT_COORDS/g,N).replace(/NUM_RECT_AREA_LIGHTS/g,$.numRectAreaLights).replace(/NUM_POINT_LIGHTS/g,$.numPointLights).replace(/NUM_HEMI_LIGHTS/g,$.numHemiLights).replace(/NUM_DIR_LIGHT_SHADOWS/g,$.numDirLightShadows).replace(/NUM_SPOT_LIGHT_SHADOWS_WITH_MAPS/g,$.numSpotLightShadowsWithMaps).replace(/NUM_SPOT_LIGHT_SHADOWS/g,$.numSpotLightShadows).replace(/NUM_POINT_LIGHT_SHADOWS/g,$.numPointLightShadows)}function LZ(J,$){return J.replace(/NUM_CLIPPING_PLANES/g,$.numClippingPlanes).replace(/UNION_CLIPPING_PLANES/g,$.numClippingPlanes-$.numClipIntersection)}var gH=/^[ \t]*#include +<([\w\d./]+)>/gm;function KN(J){return J.replace(gH,dH)}var pH=new Map;function dH(J,$){let N=u0[$];if(N===void 0){let Q=pH.get($);if(Q!==void 0)N=u0[Q],console.warn('THREE.WebGLRenderer: Shader chunk "%s" has been deprecated. Use "%s" instead.',$,Q);else throw Error("Can not resolve #include <"+$+">")}return KN(N)}var lH=/#pragma unroll_loop_start\s+for\s*\(\s*int\s+i\s*=\s*(\d+)\s*;\s*i\s*<\s*(\d+)\s*;\s*i\s*\+\+\s*\)\s*{([\s\S]+?)}\s+#pragma unroll_loop_end/g;function VZ(J){return J.replace(lH,mH)}function mH(J,$,N,Q){let Z="";for(let W=parseInt($);W<parseInt(N);W++)Z+=Q.replace(/\[\s*i\s*\]/g,"[ "+W+" ]").replace(/UNROLLED_LOOP_INDEX/g,W);return Z}function wZ(J){let $=`precision ${J.precision} float;
	precision ${J.precision} int;
	precision ${J.precision} sampler2D;
	precision ${J.precision} samplerCube;
	precision ${J.precision} sampler3D;
	precision ${J.precision} sampler2DArray;
	precision ${J.precision} sampler2DShadow;
	precision ${J.precision} samplerCubeShadow;
	precision ${J.precision} sampler2DArrayShadow;
	precision ${J.precision} isampler2D;
	precision ${J.precision} isampler3D;
	precision ${J.precision} isamplerCube;
	precision ${J.precision} isampler2DArray;
	precision ${J.precision} usampler2D;
	precision ${J.precision} usampler3D;
	precision ${J.precision} usamplerCube;
	precision ${J.precision} usampler2DArray;
	`;if(J.precision==="highp")$+=`
#define HIGH_PRECISION`;else if(J.precision==="mediump")$+=`
#define MEDIUM_PRECISION`;else if(J.precision==="lowp")$+=`
#define LOW_PRECISION`;return $}function uH(J){let $="SHADOWMAP_TYPE_BASIC";if(J.shadowMapType===d7)$="SHADOWMAP_TYPE_PCF";else if(J.shadowMapType===aN)$="SHADOWMAP_TYPE_PCF_SOFT";else if(J.shadowMapType===W9)$="SHADOWMAP_TYPE_VSM";return $}function cH(J){let $="ENVMAP_TYPE_CUBE";if(J.envMap)switch(J.envMapMode){case MJ:case p9:$="ENVMAP_TYPE_CUBE";break;case mJ:$="ENVMAP_TYPE_CUBE_UV";break}return $}function nH(J){let $="ENVMAP_MODE_REFLECTION";if(J.envMap)switch(J.envMapMode){case p9:$="ENVMAP_MODE_REFRACTION";break}return $}function sH(J){let $="ENVMAP_BLENDING_NONE";if(J.envMap)switch(J.combine){case RQ:$="ENVMAP_BLENDING_MULTIPLY";break;case FQ:$="ENVMAP_BLENDING_MIX";break;case MQ:$="ENVMAP_BLENDING_ADD";break}return $}function iH(J){let $=J.envMapCubeUVHeight;if($===null)return null;let N=Math.log2($)-2,Q=1/$;return{texelWidth:1/(3*Math.max(Math.pow(2,N),112)),texelHeight:Q,maxMip:N}}function oH(J,$,N,Q){let Z=J.getContext(),W=N.defines,Y=N.vertexShader,K=N.fragmentShader,H=uH(N),X=cH(N),G=nH(N),E=sH(N),q=iH(N),k=vH(N),M=hH(W),_=Z.createProgram(),D,U,C=N.glslVersion?"#version "+N.glslVersion+`
`:"";if(N.isRawShaderMaterial){if(D=["#define SHADER_TYPE "+N.shaderType,"#define SHADER_NAME "+N.shaderName,M].filter(N6).join(`
`),D.length>0)D+=`
`;if(U=["#define SHADER_TYPE "+N.shaderType,"#define SHADER_NAME "+N.shaderName,M].filter(N6).join(`
`),U.length>0)U+=`
`}else D=[wZ(N),"#define SHADER_TYPE "+N.shaderType,"#define SHADER_NAME "+N.shaderName,M,N.extensionClipCullDistance?"#define USE_CLIP_DISTANCE":"",N.batching?"#define USE_BATCHING":"",N.batchingColor?"#define USE_BATCHING_COLOR":"",N.instancing?"#define USE_INSTANCING":"",N.instancingColor?"#define USE_INSTANCING_COLOR":"",N.instancingMorph?"#define USE_INSTANCING_MORPH":"",N.useFog&&N.fog?"#define USE_FOG":"",N.useFog&&N.fogExp2?"#define FOG_EXP2":"",N.map?"#define USE_MAP":"",N.envMap?"#define USE_ENVMAP":"",N.envMap?"#define "+G:"",N.lightMap?"#define USE_LIGHTMAP":"",N.aoMap?"#define USE_AOMAP":"",N.bumpMap?"#define USE_BUMPMAP":"",N.normalMap?"#define USE_NORMALMAP":"",N.normalMapObjectSpace?"#define USE_NORMALMAP_OBJECTSPACE":"",N.normalMapTangentSpace?"#define USE_NORMALMAP_TANGENTSPACE":"",N.displacementMap?"#define USE_DISPLACEMENTMAP":"",N.emissiveMap?"#define USE_EMISSIVEMAP":"",N.anisotropy?"#define USE_ANISOTROPY":"",N.anisotropyMap?"#define USE_ANISOTROPYMAP":"",N.clearcoatMap?"#define USE_CLEARCOATMAP":"",N.clearcoatRoughnessMap?"#define USE_CLEARCOAT_ROUGHNESSMAP":"",N.clearcoatNormalMap?"#define USE_CLEARCOAT_NORMALMAP":"",N.iridescenceMap?"#define USE_IRIDESCENCEMAP":"",N.iridescenceThicknessMap?"#define USE_IRIDESCENCE_THICKNESSMAP":"",N.specularMap?"#define USE_SPECULARMAP":"",N.specularColorMap?"#define USE_SPECULAR_COLORMAP":"",N.specularIntensityMap?"#define USE_SPECULAR_INTENSITYMAP":"",N.roughnessMap?"#define USE_ROUGHNESSMAP":"",N.metalnessMap?"#define USE_METALNESSMAP":"",N.alphaMap?"#define USE_ALPHAMAP":"",N.alphaHash?"#define USE_ALPHAHASH":"",N.transmission?"#define USE_TRANSMISSION":"",N.transmissionMap?"#define USE_TRANSMISSIONMAP":"",N.thicknessMap?"#define USE_THICKNESSMAP":"",N.sheenColorMap?"#define USE_SHEEN_COLORMAP":"",N.sheenRoughnessMap?"#define USE_SHEEN_ROUGHNESSMAP":"",N.mapUv?"#define MAP_UV "+N.mapUv:"",N.alphaMapUv?"#define ALPHAMAP_UV "+N.alphaMapUv:"",N.lightMapUv?"#define LIGHTMAP_UV "+N.lightMapUv:"",N.aoMapUv?"#define AOMAP_UV "+N.aoMapUv:"",N.emissiveMapUv?"#define EMISSIVEMAP_UV "+N.emissiveMapUv:"",N.bumpMapUv?"#define BUMPMAP_UV "+N.bumpMapUv:"",N.normalMapUv?"#define NORMALMAP_UV "+N.normalMapUv:"",N.displacementMapUv?"#define DISPLACEMENTMAP_UV "+N.displacementMapUv:"",N.metalnessMapUv?"#define METALNESSMAP_UV "+N.metalnessMapUv:"",N.roughnessMapUv?"#define ROUGHNESSMAP_UV "+N.roughnessMapUv:"",N.anisotropyMapUv?"#define ANISOTROPYMAP_UV "+N.anisotropyMapUv:"",N.clearcoatMapUv?"#define CLEARCOATMAP_UV "+N.clearcoatMapUv:"",N.clearcoatNormalMapUv?"#define CLEARCOAT_NORMALMAP_UV "+N.clearcoatNormalMapUv:"",N.clearcoatRoughnessMapUv?"#define CLEARCOAT_ROUGHNESSMAP_UV "+N.clearcoatRoughnessMapUv:"",N.iridescenceMapUv?"#define IRIDESCENCEMAP_UV "+N.iridescenceMapUv:"",N.iridescenceThicknessMapUv?"#define IRIDESCENCE_THICKNESSMAP_UV "+N.iridescenceThicknessMapUv:"",N.sheenColorMapUv?"#define SHEEN_COLORMAP_UV "+N.sheenColorMapUv:"",N.sheenRoughnessMapUv?"#define SHEEN_ROUGHNESSMAP_UV "+N.sheenRoughnessMapUv:"",N.specularMapUv?"#define SPECULARMAP_UV "+N.specularMapUv:"",N.specularColorMapUv?"#define SPECULAR_COLORMAP_UV "+N.specularColorMapUv:"",N.specularIntensityMapUv?"#define SPECULAR_INTENSITYMAP_UV "+N.specularIntensityMapUv:"",N.transmissionMapUv?"#define TRANSMISSIONMAP_UV "+N.transmissionMapUv:"",N.thicknessMapUv?"#define THICKNESSMAP_UV "+N.thicknessMapUv:"",N.vertexTangents&&N.flatShading===!1?"#define USE_TANGENT":"",N.vertexColors?"#define USE_COLOR":"",N.vertexAlphas?"#define USE_COLOR_ALPHA":"",N.vertexUv1s?"#define USE_UV1":"",N.vertexUv2s?"#define USE_UV2":"",N.vertexUv3s?"#define USE_UV3":"",N.pointsUvs?"#define USE_POINTS_UV":"",N.flatShading?"#define FLAT_SHADED":"",N.skinning?"#define USE_SKINNING":"",N.morphTargets?"#define USE_MORPHTARGETS":"",N.morphNormals&&N.flatShading===!1?"#define USE_MORPHNORMALS":"",N.morphColors?"#define USE_MORPHCOLORS":"",N.morphTargetsCount>0?"#define MORPHTARGETS_TEXTURE_STRIDE "+N.morphTextureStride:"",N.morphTargetsCount>0?"#define MORPHTARGETS_COUNT "+N.morphTargetsCount:"",N.doubleSided?"#define DOUBLE_SIDED":"",N.flipSided?"#define FLIP_SIDED":"",N.shadowMapEnabled?"#define USE_SHADOWMAP":"",N.shadowMapEnabled?"#define "+H:"",N.sizeAttenuation?"#define USE_SIZEATTENUATION":"",N.numLightProbes>0?"#define USE_LIGHT_PROBES":"",N.logarithmicDepthBuffer?"#define USE_LOGARITHMIC_DEPTH_BUFFER":"",N.reversedDepthBuffer?"#define USE_REVERSED_DEPTH_BUFFER":"","uniform mat4 modelMatrix;","uniform mat4 modelViewMatrix;","uniform mat4 projectionMatrix;","uniform mat4 viewMatrix;","uniform mat3 normalMatrix;","uniform vec3 cameraPosition;","uniform bool isOrthographic;","#ifdef USE_INSTANCING","\tattribute mat4 instanceMatrix;","#endif","#ifdef USE_INSTANCING_COLOR","\tattribute vec3 instanceColor;","#endif","#ifdef USE_INSTANCING_MORPH","\tuniform sampler2D morphTexture;","#endif","attribute vec3 position;","attribute vec3 normal;","attribute vec2 uv;","#ifdef USE_UV1","\tattribute vec2 uv1;","#endif","#ifdef USE_UV2","\tattribute vec2 uv2;","#endif","#ifdef USE_UV3","\tattribute vec2 uv3;","#endif","#ifdef USE_TANGENT","\tattribute vec4 tangent;","#endif","#if defined( USE_COLOR_ALPHA )","\tattribute vec4 color;","#elif defined( USE_COLOR )","\tattribute vec3 color;","#endif","#ifdef USE_SKINNING","\tattribute vec4 skinIndex;","\tattribute vec4 skinWeight;","#endif",`
`].filter(N6).join(`
`),U=[wZ(N),"#define SHADER_TYPE "+N.shaderType,"#define SHADER_NAME "+N.shaderName,M,N.useFog&&N.fog?"#define USE_FOG":"",N.useFog&&N.fogExp2?"#define FOG_EXP2":"",N.alphaToCoverage?"#define ALPHA_TO_COVERAGE":"",N.map?"#define USE_MAP":"",N.matcap?"#define USE_MATCAP":"",N.envMap?"#define USE_ENVMAP":"",N.envMap?"#define "+X:"",N.envMap?"#define "+G:"",N.envMap?"#define "+E:"",q?"#define CUBEUV_TEXEL_WIDTH "+q.texelWidth:"",q?"#define CUBEUV_TEXEL_HEIGHT "+q.texelHeight:"",q?"#define CUBEUV_MAX_MIP "+q.maxMip+".0":"",N.lightMap?"#define USE_LIGHTMAP":"",N.aoMap?"#define USE_AOMAP":"",N.bumpMap?"#define USE_BUMPMAP":"",N.normalMap?"#define USE_NORMALMAP":"",N.normalMapObjectSpace?"#define USE_NORMALMAP_OBJECTSPACE":"",N.normalMapTangentSpace?"#define USE_NORMALMAP_TANGENTSPACE":"",N.emissiveMap?"#define USE_EMISSIVEMAP":"",N.anisotropy?"#define USE_ANISOTROPY":"",N.anisotropyMap?"#define USE_ANISOTROPYMAP":"",N.clearcoat?"#define USE_CLEARCOAT":"",N.clearcoatMap?"#define USE_CLEARCOATMAP":"",N.clearcoatRoughnessMap?"#define USE_CLEARCOAT_ROUGHNESSMAP":"",N.clearcoatNormalMap?"#define USE_CLEARCOAT_NORMALMAP":"",N.dispersion?"#define USE_DISPERSION":"",N.iridescence?"#define USE_IRIDESCENCE":"",N.iridescenceMap?"#define USE_IRIDESCENCEMAP":"",N.iridescenceThicknessMap?"#define USE_IRIDESCENCE_THICKNESSMAP":"",N.specularMap?"#define USE_SPECULARMAP":"",N.specularColorMap?"#define USE_SPECULAR_COLORMAP":"",N.specularIntensityMap?"#define USE_SPECULAR_INTENSITYMAP":"",N.roughnessMap?"#define USE_ROUGHNESSMAP":"",N.metalnessMap?"#define USE_METALNESSMAP":"",N.alphaMap?"#define USE_ALPHAMAP":"",N.alphaTest?"#define USE_ALPHATEST":"",N.alphaHash?"#define USE_ALPHAHASH":"",N.sheen?"#define USE_SHEEN":"",N.sheenColorMap?"#define USE_SHEEN_COLORMAP":"",N.sheenRoughnessMap?"#define USE_SHEEN_ROUGHNESSMAP":"",N.transmission?"#define USE_TRANSMISSION":"",N.transmissionMap?"#define USE_TRANSMISSIONMAP":"",N.thicknessMap?"#define USE_THICKNESSMAP":"",N.vertexTangents&&N.flatShading===!1?"#define USE_TANGENT":"",N.vertexColors||N.instancingColor||N.batchingColor?"#define USE_COLOR":"",N.vertexAlphas?"#define USE_COLOR_ALPHA":"",N.vertexUv1s?"#define USE_UV1":"",N.vertexUv2s?"#define USE_UV2":"",N.vertexUv3s?"#define USE_UV3":"",N.pointsUvs?"#define USE_POINTS_UV":"",N.gradientMap?"#define USE_GRADIENTMAP":"",N.flatShading?"#define FLAT_SHADED":"",N.doubleSided?"#define DOUBLE_SIDED":"",N.flipSided?"#define FLIP_SIDED":"",N.shadowMapEnabled?"#define USE_SHADOWMAP":"",N.shadowMapEnabled?"#define "+H:"",N.premultipliedAlpha?"#define PREMULTIPLIED_ALPHA":"",N.numLightProbes>0?"#define USE_LIGHT_PROBES":"",N.decodeVideoTexture?"#define DECODE_VIDEO_TEXTURE":"",N.decodeVideoTextureEmissive?"#define DECODE_VIDEO_TEXTURE_EMISSIVE":"",N.logarithmicDepthBuffer?"#define USE_LOGARITHMIC_DEPTH_BUFFER":"",N.reversedDepthBuffer?"#define USE_REVERSED_DEPTH_BUFFER":"","uniform mat4 viewMatrix;","uniform vec3 cameraPosition;","uniform bool isOrthographic;",N.toneMapping!==M9?"#define TONE_MAPPING":"",N.toneMapping!==M9?u0.tonemapping_pars_fragment:"",N.toneMapping!==M9?jH("toneMapping",N.toneMapping):"",N.dithering?"#define DITHERING":"",N.opaque?"#define OPAQUE":"",u0.colorspace_pars_fragment,yH("linearToOutputTexel",N.outputColorSpace),fH(),N.useDepthPacking?"#define DEPTH_PACKING "+N.depthPacking:"",`
`].filter(N6).join(`
`);if(Y=KN(Y),Y=_Z(Y,N),Y=LZ(Y,N),K=KN(K),K=_Z(K,N),K=LZ(K,N),Y=VZ(Y),K=VZ(K),N.isRawShaderMaterial!==!0)C=`#version 300 es
`,D=[k,"#define attribute in","#define varying out","#define texture2D texture"].join(`
`)+`
`+D,U=["#define varying in",N.glslVersion===I$?"":"layout(location = 0) out highp vec4 pc_fragColor;",N.glslVersion===I$?"":"#define gl_FragColor pc_fragColor","#define gl_FragDepthEXT gl_FragDepth","#define texture2D texture","#define textureCube texture","#define texture2DProj textureProj","#define texture2DLodEXT textureLod","#define texture2DProjLodEXT textureProjLod","#define textureCubeLodEXT textureLod","#define texture2DGradEXT textureGrad","#define texture2DProjGradEXT textureProjGrad","#define textureCubeGradEXT textureGrad"].join(`
`)+`
`+U;let L=C+D+Y,B=C+U+K,y=RZ(Z,Z.VERTEX_SHADER,L),I=RZ(Z,Z.FRAGMENT_SHADER,B);if(Z.attachShader(_,y),Z.attachShader(_,I),N.index0AttributeName!==void 0)Z.bindAttribLocation(_,0,N.index0AttributeName);else if(N.morphTargets===!0)Z.bindAttribLocation(_,0,"position");Z.linkProgram(_);function b(S){if(J.debug.checkShaderErrors){let s=Z.getProgramInfoLog(_)||"",i=Z.getShaderInfoLog(y)||"",m=Z.getShaderInfoLog(I)||"",T=s.trim(),p=i.trim(),$0=m.trim(),c=!0,X0=!0;if(Z.getProgramParameter(_,Z.LINK_STATUS)===!1)if(c=!1,typeof J.debug.onShaderError==="function")J.debug.onShaderError(Z,_,y,I);else{let q0=MZ(Z,y,"vertex"),P0=MZ(Z,I,"fragment");console.error("THREE.WebGLProgram: Shader Error "+Z.getError()+" - VALIDATE_STATUS "+Z.getProgramParameter(_,Z.VALIDATE_STATUS)+`

Material Name: `+S.name+`
Material Type: `+S.type+`

Program Info Log: `+T+`
`+q0+`
`+P0)}else if(T!=="")console.warn("THREE.WebGLProgram: Program Info Log:",T);else if(p===""||$0==="")X0=!1;if(X0)S.diagnostics={runnable:c,programLog:T,vertexShader:{log:p,prefix:D},fragmentShader:{log:$0,prefix:U}}}Z.deleteShader(y),Z.deleteShader(I),g=new Q6(Z,_),V=xH(Z,_)}let g;this.getUniforms=function(){if(g===void 0)b(this);return g};let V;this.getAttributes=function(){if(V===void 0)b(this);return V};let F=N.rendererExtensionParallelShaderCompile===!1;return this.isReady=function(){if(F===!1)F=Z.getProgramParameter(_,AH);return F},this.destroy=function(){Q.releaseStatesOfProgram(this),Z.deleteProgram(_),this.program=void 0},this.type=N.shaderType,this.name=N.shaderName,this.id=TH++,this.cacheKey=$,this.usedTimes=1,this.program=_,this.vertexShader=y,this.fragmentShader=I,this}var aH=0;class fZ{constructor(){this.shaderCache=new Map,this.materialCache=new Map}update(J){let{vertexShader:$,fragmentShader:N}=J,Q=this._getShaderStage($),Z=this._getShaderStage(N),W=this._getShaderCacheForMaterial(J);if(W.has(Q)===!1)W.add(Q),Q.usedTimes++;if(W.has(Z)===!1)W.add(Z),Z.usedTimes++;return this}remove(J){let $=this.materialCache.get(J);for(let N of $)if(N.usedTimes--,N.usedTimes===0)this.shaderCache.delete(N.code);return this.materialCache.delete(J),this}getVertexShaderID(J){return this._getShaderStage(J.vertexShader).id}getFragmentShaderID(J){return this._getShaderStage(J.fragmentShader).id}dispose(){this.shaderCache.clear(),this.materialCache.clear()}_getShaderCacheForMaterial(J){let $=this.materialCache,N=$.get(J);if(N===void 0)N=new Set,$.set(J,N);return N}_getShaderStage(J){let $=this.shaderCache,N=$.get(J);if(N===void 0)N=new vZ(J),$.set(J,N);return N}}class vZ{constructor(J){this.id=aH++,this.code=J,this.usedTimes=0}}function rH(J,$,N,Q,Z,W,Y){let K=new aJ,H=new fZ,X=new Set,G=[],E=Z.logarithmicDepthBuffer,q=Z.vertexTextures,k=Z.precision,M={MeshDepthMaterial:"depth",MeshDistanceMaterial:"distanceRGBA",MeshNormalMaterial:"normal",MeshBasicMaterial:"basic",MeshLambertMaterial:"lambert",MeshPhongMaterial:"phong",MeshToonMaterial:"toon",MeshStandardMaterial:"physical",MeshPhysicalMaterial:"physical",MeshMatcapMaterial:"matcap",LineBasicMaterial:"basic",LineDashedMaterial:"dashed",PointsMaterial:"points",ShadowMaterial:"shadow",SpriteMaterial:"sprite"};function _(V){if(X.add(V),V===0)return"uv";return`uv${V}`}function D(V,F,S,s,i){let m=s.fog,T=i.geometry,p=V.isMeshStandardMaterial?s.environment:null,$0=(V.isMeshStandardMaterial?N:$).get(V.envMap||p),c=!!$0&&$0.mapping===mJ?$0.image.height:null,X0=M[V.type];if(V.precision!==null){if(k=Z.getMaxPrecision(V.precision),k!==V.precision)console.warn("THREE.WebGLProgram.getParameters:",V.precision,"not supported, using",k,"instead.")}let q0=T.morphAttributes.position||T.morphAttributes.normal||T.morphAttributes.color,P0=q0!==void 0?q0.length:0,d0=0;if(T.morphAttributes.position!==void 0)d0=1;if(T.morphAttributes.normal!==void 0)d0=2;if(T.morphAttributes.color!==void 0)d0=3;let J8,c0,t,K0;if(X0){let e0=X9[X0];J8=e0.vertexShader,c0=e0.fragmentShader}else J8=V.vertexShader,c0=V.fragmentShader,H.update(V),t=H.getVertexShaderID(V),K0=H.getFragmentShaderID(V);let x=J.getRenderTarget(),J0=J.state.buffers.depth.getReversed(),Q0=i.isInstancedMesh===!0,E0=i.isBatchedMesh===!0,m0=!!V.map,z=!!V.matcap,x0=!!$0,y0=!!V.aoMap,S0=!!V.lightMap,L0=!!V.bumpMap,a0=!!V.normalMap,B0=!!V.displacementMap,C0=!!V.emissiveMap,X8=!!V.metalnessMap,k8=!!V.roughnessMap,N8=V.anisotropy>0,w=V.clearcoat>0,O=V.dispersion>0,j=V.iridescence>0,o=V.sheen>0,u=V.transmission>0,n=N8&&!!V.anisotropyMap,O0=w&&!!V.clearcoatMap,H0=w&&!!V.clearcoatNormalMap,w0=w&&!!V.clearcoatRoughnessMap,b0=j&&!!V.iridescenceMap,e=j&&!!V.iridescenceThicknessMap,Z0=o&&!!V.sheenColorMap,_0=o&&!!V.sheenRoughnessMap,F0=!!V.specularMap,G0=!!V.specularColorMap,I0=!!V.specularIntensityMap,A=u&&!!V.transmissionMap,Y0=u&&!!V.thicknessMap,U0=!!V.gradientMap,P=!!V.alphaMap,r=V.alphaTest>0,a=!!V.alphaHash,R0=!!V.extensions,f0=M9;if(V.toneMapped){if(x===null||x.isXRRenderTarget===!0)f0=J.toneMapping}let t0={shaderID:X0,shaderType:V.type,shaderName:V.name,vertexShader:J8,fragmentShader:c0,defines:V.defines,customVertexShaderID:t,customFragmentShaderID:K0,isRawShaderMaterial:V.isRawShaderMaterial===!0,glslVersion:V.glslVersion,precision:k,batching:E0,batchingColor:E0&&i._colorsTexture!==null,instancing:Q0,instancingColor:Q0&&i.instanceColor!==null,instancingMorph:Q0&&i.morphTexture!==null,supportsVertexTextures:q,outputColorSpace:x===null?J.outputColorSpace:x.isXRRenderTarget===!0?x.texture.colorSpace:iJ,alphaToCoverage:!!V.alphaToCoverage,map:m0,matcap:z,envMap:x0,envMapMode:x0&&$0.mapping,envMapCubeUVHeight:c,aoMap:y0,lightMap:S0,bumpMap:L0,normalMap:a0,displacementMap:q&&B0,emissiveMap:C0,normalMapObjectSpace:a0&&V.normalMapType===dQ,normalMapTangentSpace:a0&&V.normalMapType===pQ,metalnessMap:X8,roughnessMap:k8,anisotropy:N8,anisotropyMap:n,clearcoat:w,clearcoatMap:O0,clearcoatNormalMap:H0,clearcoatRoughnessMap:w0,dispersion:O,iridescence:j,iridescenceMap:b0,iridescenceThicknessMap:e,sheen:o,sheenColorMap:Z0,sheenRoughnessMap:_0,specularMap:F0,specularColorMap:G0,specularIntensityMap:I0,transmission:u,transmissionMap:A,thicknessMap:Y0,gradientMap:U0,opaque:V.transparent===!1&&V.blending===dJ&&V.alphaToCoverage===!1,alphaMap:P,alphaTest:r,alphaHash:a,combine:V.combine,mapUv:m0&&_(V.map.channel),aoMapUv:y0&&_(V.aoMap.channel),lightMapUv:S0&&_(V.lightMap.channel),bumpMapUv:L0&&_(V.bumpMap.channel),normalMapUv:a0&&_(V.normalMap.channel),displacementMapUv:B0&&_(V.displacementMap.channel),emissiveMapUv:C0&&_(V.emissiveMap.channel),metalnessMapUv:X8&&_(V.metalnessMap.channel),roughnessMapUv:k8&&_(V.roughnessMap.channel),anisotropyMapUv:n&&_(V.anisotropyMap.channel),clearcoatMapUv:O0&&_(V.clearcoatMap.channel),clearcoatNormalMapUv:H0&&_(V.clearcoatNormalMap.channel),clearcoatRoughnessMapUv:w0&&_(V.clearcoatRoughnessMap.channel),iridescenceMapUv:b0&&_(V.iridescenceMap.channel),iridescenceThicknessMapUv:e&&_(V.iridescenceThicknessMap.channel),sheenColorMapUv:Z0&&_(V.sheenColorMap.channel),sheenRoughnessMapUv:_0&&_(V.sheenRoughnessMap.channel),specularMapUv:F0&&_(V.specularMap.channel),specularColorMapUv:G0&&_(V.specularColorMap.channel),specularIntensityMapUv:I0&&_(V.specularIntensityMap.channel),transmissionMapUv:A&&_(V.transmissionMap.channel),thicknessMapUv:Y0&&_(V.thicknessMap.channel),alphaMapUv:P&&_(V.alphaMap.channel),vertexTangents:!!T.attributes.tangent&&(a0||N8),vertexColors:V.vertexColors,vertexAlphas:V.vertexColors===!0&&!!T.attributes.color&&T.attributes.color.itemSize===4,pointsUvs:i.isPoints===!0&&!!T.attributes.uv&&(m0||P),fog:!!m,useFog:V.fog===!0,fogExp2:!!m&&m.isFogExp2,flatShading:V.flatShading===!0&&V.wireframe===!1,sizeAttenuation:V.sizeAttenuation===!0,logarithmicDepthBuffer:E,reversedDepthBuffer:J0,skinning:i.isSkinnedMesh===!0,morphTargets:T.morphAttributes.position!==void 0,morphNormals:T.morphAttributes.normal!==void 0,morphColors:T.morphAttributes.color!==void 0,morphTargetsCount:P0,morphTextureStride:d0,numDirLights:F.directional.length,numPointLights:F.point.length,numSpotLights:F.spot.length,numSpotLightMaps:F.spotLightMap.length,numRectAreaLights:F.rectArea.length,numHemiLights:F.hemi.length,numDirLightShadows:F.directionalShadowMap.length,numPointLightShadows:F.pointShadowMap.length,numSpotLightShadows:F.spotShadowMap.length,numSpotLightShadowsWithMaps:F.numSpotLightShadowsWithMaps,numLightProbes:F.numLightProbes,numClippingPlanes:Y.numPlanes,numClipIntersection:Y.numIntersection,dithering:V.dithering,shadowMapEnabled:J.shadowMap.enabled&&S.length>0,shadowMapType:J.shadowMap.type,toneMapping:f0,decodeVideoTexture:m0&&V.map.isVideoTexture===!0&&o0.getTransfer(V.map.colorSpace)===Y8,decodeVideoTextureEmissive:C0&&V.emissiveMap.isVideoTexture===!0&&o0.getTransfer(V.emissiveMap.colorSpace)===Y8,premultipliedAlpha:V.premultipliedAlpha,doubleSided:V.side===a8,flipSided:V.side===m8,useDepthPacking:V.depthPacking>=0,depthPacking:V.depthPacking||0,index0AttributeName:V.index0AttributeName,extensionClipCullDistance:R0&&V.extensions.clipCullDistance===!0&&Q.has("WEBGL_clip_cull_distance"),extensionMultiDraw:(R0&&V.extensions.multiDraw===!0||E0)&&Q.has("WEBGL_multi_draw"),rendererExtensionParallelShaderCompile:Q.has("KHR_parallel_shader_compile"),customProgramCacheKey:V.customProgramCacheKey()};return t0.vertexUv1s=X.has(1),t0.vertexUv2s=X.has(2),t0.vertexUv3s=X.has(3),X.clear(),t0}function U(V){let F=[];if(V.shaderID)F.push(V.shaderID);else F.push(V.customVertexShaderID),F.push(V.customFragmentShaderID);if(V.defines!==void 0)for(let S in V.defines)F.push(S),F.push(V.defines[S]);if(V.isRawShaderMaterial===!1)C(F,V),L(F,V),F.push(J.outputColorSpace);return F.push(V.customProgramCacheKey),F.join()}function C(V,F){V.push(F.precision),V.push(F.outputColorSpace),V.push(F.envMapMode),V.push(F.envMapCubeUVHeight),V.push(F.mapUv),V.push(F.alphaMapUv),V.push(F.lightMapUv),V.push(F.aoMapUv),V.push(F.bumpMapUv),V.push(F.normalMapUv),V.push(F.displacementMapUv),V.push(F.emissiveMapUv),V.push(F.metalnessMapUv),V.push(F.roughnessMapUv),V.push(F.anisotropyMapUv),V.push(F.clearcoatMapUv),V.push(F.clearcoatNormalMapUv),V.push(F.clearcoatRoughnessMapUv),V.push(F.iridescenceMapUv),V.push(F.iridescenceThicknessMapUv),V.push(F.sheenColorMapUv),V.push(F.sheenRoughnessMapUv),V.push(F.specularMapUv),V.push(F.specularColorMapUv),V.push(F.specularIntensityMapUv),V.push(F.transmissionMapUv),V.push(F.thicknessMapUv),V.push(F.combine),V.push(F.fogExp2),V.push(F.sizeAttenuation),V.push(F.morphTargetsCount),V.push(F.morphAttributeCount),V.push(F.numDirLights),V.push(F.numPointLights),V.push(F.numSpotLights),V.push(F.numSpotLightMaps),V.push(F.numHemiLights),V.push(F.numRectAreaLights),V.push(F.numDirLightShadows),V.push(F.numPointLightShadows),V.push(F.numSpotLightShadows),V.push(F.numSpotLightShadowsWithMaps),V.push(F.numLightProbes),V.push(F.shadowMapType),V.push(F.toneMapping),V.push(F.numClippingPlanes),V.push(F.numClipIntersection),V.push(F.depthPacking)}function L(V,F){if(K.disableAll(),F.supportsVertexTextures)K.enable(0);if(F.instancing)K.enable(1);if(F.instancingColor)K.enable(2);if(F.instancingMorph)K.enable(3);if(F.matcap)K.enable(4);if(F.envMap)K.enable(5);if(F.normalMapObjectSpace)K.enable(6);if(F.normalMapTangentSpace)K.enable(7);if(F.clearcoat)K.enable(8);if(F.iridescence)K.enable(9);if(F.alphaTest)K.enable(10);if(F.vertexColors)K.enable(11);if(F.vertexAlphas)K.enable(12);if(F.vertexUv1s)K.enable(13);if(F.vertexUv2s)K.enable(14);if(F.vertexUv3s)K.enable(15);if(F.vertexTangents)K.enable(16);if(F.anisotropy)K.enable(17);if(F.alphaHash)K.enable(18);if(F.batching)K.enable(19);if(F.dispersion)K.enable(20);if(F.batchingColor)K.enable(21);if(F.gradientMap)K.enable(22);if(V.push(K.mask),K.disableAll(),F.fog)K.enable(0);if(F.useFog)K.enable(1);if(F.flatShading)K.enable(2);if(F.logarithmicDepthBuffer)K.enable(3);if(F.reversedDepthBuffer)K.enable(4);if(F.skinning)K.enable(5);if(F.morphTargets)K.enable(6);if(F.morphNormals)K.enable(7);if(F.morphColors)K.enable(8);if(F.premultipliedAlpha)K.enable(9);if(F.shadowMapEnabled)K.enable(10);if(F.doubleSided)K.enable(11);if(F.flipSided)K.enable(12);if(F.useDepthPacking)K.enable(13);if(F.dithering)K.enable(14);if(F.transmission)K.enable(15);if(F.sheen)K.enable(16);if(F.opaque)K.enable(17);if(F.pointsUvs)K.enable(18);if(F.decodeVideoTexture)K.enable(19);if(F.decodeVideoTextureEmissive)K.enable(20);if(F.alphaToCoverage)K.enable(21);V.push(K.mask)}function B(V){let F=M[V.type],S;if(F){let s=X9[F];S=eQ.clone(s.uniforms)}else S=V.uniforms;return S}function y(V,F){let S;for(let s=0,i=G.length;s<i;s++){let m=G[s];if(m.cacheKey===F){S=m,++S.usedTimes;break}}if(S===void 0)S=new oH(J,F,V,W),G.push(S);return S}function I(V){if(--V.usedTimes===0){let F=G.indexOf(V);G[F]=G[G.length-1],G.pop(),V.destroy()}}function b(V){H.remove(V)}function g(){H.dispose()}return{getParameters:D,getProgramCacheKey:U,getUniforms:B,acquireProgram:y,releaseProgram:I,releaseShaderCache:b,programs:G,dispose:g}}function tH(){let J=new WeakMap;function $(Y){return J.has(Y)}function N(Y){let K=J.get(Y);if(K===void 0)K={},J.set(Y,K);return K}function Q(Y){J.delete(Y)}function Z(Y,K,H){J.get(Y)[K]=H}function W(){J=new WeakMap}return{has:$,get:N,remove:Q,update:Z,dispose:W}}function eH(J,$){if(J.groupOrder!==$.groupOrder)return J.groupOrder-$.groupOrder;else if(J.renderOrder!==$.renderOrder)return J.renderOrder-$.renderOrder;else if(J.material.id!==$.material.id)return J.material.id-$.material.id;else if(J.z!==$.z)return J.z-$.z;else return J.id-$.id}function zZ(J,$){if(J.groupOrder!==$.groupOrder)return J.groupOrder-$.groupOrder;else if(J.renderOrder!==$.renderOrder)return J.renderOrder-$.renderOrder;else if(J.z!==$.z)return $.z-J.z;else return J.id-$.id}function BZ(){let J=[],$=0,N=[],Q=[],Z=[];function W(){$=0,N.length=0,Q.length=0,Z.length=0}function Y(E,q,k,M,_,D){let U=J[$];if(U===void 0)U={id:E.id,object:E,geometry:q,material:k,groupOrder:M,renderOrder:E.renderOrder,z:_,group:D},J[$]=U;else U.id=E.id,U.object=E,U.geometry=q,U.material=k,U.groupOrder=M,U.renderOrder=E.renderOrder,U.z=_,U.group=D;return $++,U}function K(E,q,k,M,_,D){let U=Y(E,q,k,M,_,D);if(k.transmission>0)Q.push(U);else if(k.transparent===!0)Z.push(U);else N.push(U)}function H(E,q,k,M,_,D){let U=Y(E,q,k,M,_,D);if(k.transmission>0)Q.unshift(U);else if(k.transparent===!0)Z.unshift(U);else N.unshift(U)}function X(E,q){if(N.length>1)N.sort(E||eH);if(Q.length>1)Q.sort(q||zZ);if(Z.length>1)Z.sort(q||zZ)}function G(){for(let E=$,q=J.length;E<q;E++){let k=J[E];if(k.id===null)break;k.id=null,k.object=null,k.geometry=null,k.material=null,k.group=null}}return{opaque:N,transmissive:Q,transparent:Z,init:W,push:K,unshift:H,finish:G,sort:X}}function JX(){let J=new WeakMap;function $(Q,Z){let W=J.get(Q),Y;if(W===void 0)Y=new BZ,J.set(Q,[Y]);else if(Z>=W.length)Y=new BZ,W.push(Y);else Y=W[Z];return Y}function N(){J=new WeakMap}return{get:$,dispose:N}}function $X(){let J={};return{get:function($){if(J[$.id]!==void 0)return J[$.id];let N;switch($.type){case"DirectionalLight":N={direction:new h,color:new i0};break;case"SpotLight":N={position:new h,direction:new h,color:new i0,distance:0,coneCos:0,penumbraCos:0,decay:0};break;case"PointLight":N={position:new h,color:new i0,distance:0,decay:0};break;case"HemisphereLight":N={direction:new h,skyColor:new i0,groundColor:new i0};break;case"RectAreaLight":N={color:new i0,position:new h,halfWidth:new h,halfHeight:new h};break}return J[$.id]=N,N}}}function NX(){let J={};return{get:function($){if(J[$.id]!==void 0)return J[$.id];let N;switch($.type){case"DirectionalLight":N={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new p0};break;case"SpotLight":N={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new p0};break;case"PointLight":N={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new p0,shadowCameraNear:1,shadowCameraFar:1000};break}return J[$.id]=N,N}}}var QX=0;function ZX(J,$){return($.castShadow?2:0)-(J.castShadow?2:0)+($.map?1:0)-(J.map?1:0)}function WX(J){let $=new $X,N=NX(),Q={version:0,hash:{directionalLength:-1,pointLength:-1,spotLength:-1,rectAreaLength:-1,hemiLength:-1,numDirectionalShadows:-1,numPointShadows:-1,numSpotShadows:-1,numSpotMaps:-1,numLightProbes:-1},ambient:[0,0,0],probe:[],directional:[],directionalShadow:[],directionalShadowMap:[],directionalShadowMatrix:[],spot:[],spotLightMap:[],spotShadow:[],spotShadowMap:[],spotLightMatrix:[],rectArea:[],rectAreaLTC1:null,rectAreaLTC2:null,point:[],pointShadow:[],pointShadowMap:[],pointShadowMatrix:[],hemi:[],numSpotLightShadowsWithMaps:0,numLightProbes:0};for(let X=0;X<9;X++)Q.probe.push(new h);let Z=new h,W=new U8,Y=new U8;function K(X){let G=0,E=0,q=0;for(let V=0;V<9;V++)Q.probe[V].set(0,0,0);let k=0,M=0,_=0,D=0,U=0,C=0,L=0,B=0,y=0,I=0,b=0;X.sort(ZX);for(let V=0,F=X.length;V<F;V++){let S=X[V],s=S.color,i=S.intensity,m=S.distance,T=S.shadow&&S.shadow.map?S.shadow.map.texture:null;if(S.isAmbientLight)G+=s.r*i,E+=s.g*i,q+=s.b*i;else if(S.isLightProbe){for(let p=0;p<9;p++)Q.probe[p].addScaledVector(S.sh.coefficients[p],i);b++}else if(S.isDirectionalLight){let p=$.get(S);if(p.color.copy(S.color).multiplyScalar(S.intensity),S.castShadow){let $0=S.shadow,c=N.get(S);c.shadowIntensity=$0.intensity,c.shadowBias=$0.bias,c.shadowNormalBias=$0.normalBias,c.shadowRadius=$0.radius,c.shadowMapSize=$0.mapSize,Q.directionalShadow[k]=c,Q.directionalShadowMap[k]=T,Q.directionalShadowMatrix[k]=S.shadow.matrix,C++}Q.directional[k]=p,k++}else if(S.isSpotLight){let p=$.get(S);p.position.setFromMatrixPosition(S.matrixWorld),p.color.copy(s).multiplyScalar(i),p.distance=m,p.coneCos=Math.cos(S.angle),p.penumbraCos=Math.cos(S.angle*(1-S.penumbra)),p.decay=S.decay,Q.spot[_]=p;let $0=S.shadow;if(S.map){if(Q.spotLightMap[y]=S.map,y++,$0.updateMatrices(S),S.castShadow)I++}if(Q.spotLightMatrix[_]=$0.matrix,S.castShadow){let c=N.get(S);c.shadowIntensity=$0.intensity,c.shadowBias=$0.bias,c.shadowNormalBias=$0.normalBias,c.shadowRadius=$0.radius,c.shadowMapSize=$0.mapSize,Q.spotShadow[_]=c,Q.spotShadowMap[_]=T,B++}_++}else if(S.isRectAreaLight){let p=$.get(S);p.color.copy(s).multiplyScalar(i),p.halfWidth.set(S.width*0.5,0,0),p.halfHeight.set(0,S.height*0.5,0),Q.rectArea[D]=p,D++}else if(S.isPointLight){let p=$.get(S);if(p.color.copy(S.color).multiplyScalar(S.intensity),p.distance=S.distance,p.decay=S.decay,S.castShadow){let $0=S.shadow,c=N.get(S);c.shadowIntensity=$0.intensity,c.shadowBias=$0.bias,c.shadowNormalBias=$0.normalBias,c.shadowRadius=$0.radius,c.shadowMapSize=$0.mapSize,c.shadowCameraNear=$0.camera.near,c.shadowCameraFar=$0.camera.far,Q.pointShadow[M]=c,Q.pointShadowMap[M]=T,Q.pointShadowMatrix[M]=S.shadow.matrix,L++}Q.point[M]=p,M++}else if(S.isHemisphereLight){let p=$.get(S);p.skyColor.copy(S.color).multiplyScalar(i),p.groundColor.copy(S.groundColor).multiplyScalar(i),Q.hemi[U]=p,U++}}if(D>0)if(J.has("OES_texture_float_linear")===!0)Q.rectAreaLTC1=D0.LTC_FLOAT_1,Q.rectAreaLTC2=D0.LTC_FLOAT_2;else Q.rectAreaLTC1=D0.LTC_HALF_1,Q.rectAreaLTC2=D0.LTC_HALF_2;Q.ambient[0]=G,Q.ambient[1]=E,Q.ambient[2]=q;let g=Q.hash;if(g.directionalLength!==k||g.pointLength!==M||g.spotLength!==_||g.rectAreaLength!==D||g.hemiLength!==U||g.numDirectionalShadows!==C||g.numPointShadows!==L||g.numSpotShadows!==B||g.numSpotMaps!==y||g.numLightProbes!==b)Q.directional.length=k,Q.spot.length=_,Q.rectArea.length=D,Q.point.length=M,Q.hemi.length=U,Q.directionalShadow.length=C,Q.directionalShadowMap.length=C,Q.pointShadow.length=L,Q.pointShadowMap.length=L,Q.spotShadow.length=B,Q.spotShadowMap.length=B,Q.directionalShadowMatrix.length=C,Q.pointShadowMatrix.length=L,Q.spotLightMatrix.length=B+y-I,Q.spotLightMap.length=y,Q.numSpotLightShadowsWithMaps=I,Q.numLightProbes=b,g.directionalLength=k,g.pointLength=M,g.spotLength=_,g.rectAreaLength=D,g.hemiLength=U,g.numDirectionalShadows=C,g.numPointShadows=L,g.numSpotShadows=B,g.numSpotMaps=y,g.numLightProbes=b,Q.version=QX++}function H(X,G){let E=0,q=0,k=0,M=0,_=0,D=G.matrixWorldInverse;for(let U=0,C=X.length;U<C;U++){let L=X[U];if(L.isDirectionalLight){let B=Q.directional[E];B.direction.setFromMatrixPosition(L.matrixWorld),Z.setFromMatrixPosition(L.target.matrixWorld),B.direction.sub(Z),B.direction.transformDirection(D),E++}else if(L.isSpotLight){let B=Q.spot[k];B.position.setFromMatrixPosition(L.matrixWorld),B.position.applyMatrix4(D),B.direction.setFromMatrixPosition(L.matrixWorld),Z.setFromMatrixPosition(L.target.matrixWorld),B.direction.sub(Z),B.direction.transformDirection(D),k++}else if(L.isRectAreaLight){let B=Q.rectArea[M];B.position.setFromMatrixPosition(L.matrixWorld),B.position.applyMatrix4(D),Y.identity(),W.copy(L.matrixWorld),W.premultiply(D),Y.extractRotation(W),B.halfWidth.set(L.width*0.5,0,0),B.halfHeight.set(0,L.height*0.5,0),B.halfWidth.applyMatrix4(Y),B.halfHeight.applyMatrix4(Y),M++}else if(L.isPointLight){let B=Q.point[q];B.position.setFromMatrixPosition(L.matrixWorld),B.position.applyMatrix4(D),q++}else if(L.isHemisphereLight){let B=Q.hemi[_];B.direction.setFromMatrixPosition(L.matrixWorld),B.direction.transformDirection(D),_++}}}return{setup:K,setupView:H,state:Q}}function CZ(J){let $=new WX(J),N=[],Q=[];function Z(G){X.camera=G,N.length=0,Q.length=0}function W(G){N.push(G)}function Y(G){Q.push(G)}function K(){$.setup(N)}function H(G){$.setupView(N,G)}let X={lightsArray:N,shadowsArray:Q,camera:null,lights:$,transmissionRenderTarget:{}};return{init:Z,state:X,setupLights:K,setupLightsView:H,pushLight:W,pushShadow:Y}}function KX(J){let $=new WeakMap;function N(Z,W=0){let Y=$.get(Z),K;if(Y===void 0)K=new CZ(J),$.set(Z,[K]);else if(W>=Y.length)K=new CZ(J),Y.push(K);else K=Y[W];return K}function Q(){$=new WeakMap}return{get:N,dispose:Q}}var YX=`void main() {
	gl_Position = vec4( position, 1.0 );
}`,HX=`uniform sampler2D shadow_pass;
uniform vec2 resolution;
uniform float radius;
#include <packing>
void main() {
	const float samples = float( VSM_SAMPLES );
	float mean = 0.0;
	float squared_mean = 0.0;
	float uvStride = samples <= 1.0 ? 0.0 : 2.0 / ( samples - 1.0 );
	float uvStart = samples <= 1.0 ? 0.0 : - 1.0;
	for ( float i = 0.0; i < samples; i ++ ) {
		float uvOffset = uvStart + i * uvStride;
		#ifdef HORIZONTAL_PASS
			vec2 distribution = unpackRGBATo2Half( texture2D( shadow_pass, ( gl_FragCoord.xy + vec2( uvOffset, 0.0 ) * radius ) / resolution ) );
			mean += distribution.x;
			squared_mean += distribution.y * distribution.y + distribution.x * distribution.x;
		#else
			float depth = unpackRGBAToDepth( texture2D( shadow_pass, ( gl_FragCoord.xy + vec2( 0.0, uvOffset ) * radius ) / resolution ) );
			mean += depth;
			squared_mean += depth * depth;
		#endif
	}
	mean = mean / samples;
	squared_mean = squared_mean / samples;
	float std_dev = sqrt( squared_mean - mean * mean );
	gl_FragColor = pack2HalfToRGBA( vec2( mean, std_dev ) );
}`;function XX(J,$,N){let Q=new tJ,Z=new p0,W=new p0,Y=new D8,K=new x$({depthPacking:gQ}),H=new g$,X={},G=N.maxTextureSize,E={[RJ]:m8,[m8]:RJ,[a8]:a8},q=new H9({defines:{VSM_SAMPLES:8},uniforms:{shadow_pass:{value:null},resolution:{value:new p0},radius:{value:4}},vertexShader:YX,fragmentShader:HX}),k=q.clone();k.defines.HORIZONTAL_PASS=1;let M=new u8;M.setAttribute("position",new o8(new Float32Array([-1,-1,0.5,3,-1,0.5,-1,3,0.5]),3));let _=new b8(M,q),D=this;this.enabled=!1,this.autoUpdate=!0,this.needsUpdate=!1,this.type=d7;let U=this.type;this.render=function(I,b,g){if(D.enabled===!1)return;if(D.autoUpdate===!1&&D.needsUpdate===!1)return;if(I.length===0)return;let V=J.getRenderTarget(),F=J.getActiveCubeFace(),S=J.getActiveMipmapLevel(),s=J.state;if(s.setBlending(T9),s.buffers.depth.getReversed()===!0)s.buffers.color.setClear(0,0,0,0);else s.buffers.color.setClear(1,1,1,1);s.buffers.depth.setTest(!0),s.setScissorTest(!1);let i=U!==W9&&this.type===W9,m=U===W9&&this.type!==W9;for(let T=0,p=I.length;T<p;T++){let $0=I[T],c=$0.shadow;if(c===void 0){console.warn("THREE.WebGLShadowMap:",$0,"has no shadow.");continue}if(c.autoUpdate===!1&&c.needsUpdate===!1)continue;Z.copy(c.mapSize);let X0=c.getFrameExtents();if(Z.multiply(X0),W.copy(c.mapSize),Z.x>G||Z.y>G){if(Z.x>G)W.x=Math.floor(G/X0.x),Z.x=W.x*X0.x,c.mapSize.x=W.x;if(Z.y>G)W.y=Math.floor(G/X0.y),Z.y=W.y*X0.y,c.mapSize.y=W.y}if(c.map===null||i===!0||m===!0){let P0=this.type!==W9?{minFilter:_J,magFilter:_J}:{};if(c.map!==null)c.map.dispose();c.map=new L9(Z.x,Z.y,P0),c.map.texture.name=$0.name+".shadowMap",c.camera.updateProjectionMatrix()}J.setRenderTarget(c.map),J.clear();let q0=c.getViewportCount();for(let P0=0;P0<q0;P0++){let d0=c.getViewport(P0);Y.set(W.x*d0.x,W.y*d0.y,W.x*d0.z,W.y*d0.w),s.viewport(Y),c.updateMatrices($0,P0),Q=c.getFrustum(),B(b,g,c.camera,$0,this.type)}if(c.isPointLightShadow!==!0&&this.type===W9)C(c,g);c.needsUpdate=!1}U=this.type,D.needsUpdate=!1,J.setRenderTarget(V,F,S)};function C(I,b){let g=$.update(_);if(q.defines.VSM_SAMPLES!==I.blurSamples)q.defines.VSM_SAMPLES=I.blurSamples,k.defines.VSM_SAMPLES=I.blurSamples,q.needsUpdate=!0,k.needsUpdate=!0;if(I.mapPass===null)I.mapPass=new L9(Z.x,Z.y);q.uniforms.shadow_pass.value=I.map.texture,q.uniforms.resolution.value=I.mapSize,q.uniforms.radius.value=I.radius,J.setRenderTarget(I.mapPass),J.clear(),J.renderBufferDirect(b,null,g,q,_,null),k.uniforms.shadow_pass.value=I.mapPass.texture,k.uniforms.resolution.value=I.mapSize,k.uniforms.radius.value=I.radius,J.setRenderTarget(I.map),J.clear(),J.renderBufferDirect(b,null,g,k,_,null)}function L(I,b,g,V){let F=null,S=g.isPointLight===!0?I.customDistanceMaterial:I.customDepthMaterial;if(S!==void 0)F=S;else if(F=g.isPointLight===!0?H:K,J.localClippingEnabled&&b.clipShadows===!0&&Array.isArray(b.clippingPlanes)&&b.clippingPlanes.length!==0||b.displacementMap&&b.displacementScale!==0||b.alphaMap&&b.alphaTest>0||b.map&&b.alphaTest>0||b.alphaToCoverage===!0){let s=F.uuid,i=b.uuid,m=X[s];if(m===void 0)m={},X[s]=m;let T=m[i];if(T===void 0)T=F.clone(),m[i]=T,b.addEventListener("dispose",y);F=T}if(F.visible=b.visible,F.wireframe=b.wireframe,V===W9)F.side=b.shadowSide!==null?b.shadowSide:b.side;else F.side=b.shadowSide!==null?b.shadowSide:E[b.side];if(F.alphaMap=b.alphaMap,F.alphaTest=b.alphaToCoverage===!0?0.5:b.alphaTest,F.map=b.map,F.clipShadows=b.clipShadows,F.clippingPlanes=b.clippingPlanes,F.clipIntersection=b.clipIntersection,F.displacementMap=b.displacementMap,F.displacementScale=b.displacementScale,F.displacementBias=b.displacementBias,F.wireframeLinewidth=b.wireframeLinewidth,F.linewidth=b.linewidth,g.isPointLight===!0&&F.isMeshDistanceMaterial===!0){let s=J.properties.get(F);s.light=g}return F}function B(I,b,g,V,F){if(I.visible===!1)return;if(I.layers.test(b.layers)&&(I.isMesh||I.isLine||I.isPoints)){if((I.castShadow||I.receiveShadow&&F===W9)&&(!I.frustumCulled||Q.intersectsObject(I))){I.modelViewMatrix.multiplyMatrices(g.matrixWorldInverse,I.matrixWorld);let i=$.update(I),m=I.material;if(Array.isArray(m)){let T=i.groups;for(let p=0,$0=T.length;p<$0;p++){let c=T[p],X0=m[c.materialIndex];if(X0&&X0.visible){let q0=L(I,X0,V,F);I.onBeforeShadow(J,I,b,g,i,q0,c),J.renderBufferDirect(g,null,i,q0,I,c),I.onAfterShadow(J,I,b,g,i,q0,c)}}}else if(m.visible){let T=L(I,m,V,F);I.onBeforeShadow(J,I,b,g,i,T,null),J.renderBufferDirect(g,null,i,T,I,null),I.onAfterShadow(J,I,b,g,i,T,null)}}}let s=I.children;for(let i=0,m=s.length;i<m;i++)B(s[i],b,g,V,F)}function y(I){I.target.removeEventListener("dispose",y);for(let g in X){let V=X[g],F=I.target.uuid;if(F in V)V[F].dispose(),delete V[F]}}}var UX={[P6]:A6,[T6]:y6,[S6]:j6,[lJ]:b6,[A6]:P6,[y6]:T6,[j6]:S6,[b6]:lJ};function GX(J,$){function N(){let A=!1,Y0=new D8,U0=null,P=new D8(0,0,0,0);return{setMask:function(r){if(U0!==r&&!A)J.colorMask(r,r,r,r),U0=r},setLocked:function(r){A=r},setClear:function(r,a,R0,f0,t0){if(t0===!0)r*=f0,a*=f0,R0*=f0;if(Y0.set(r,a,R0,f0),P.equals(Y0)===!1)J.clearColor(r,a,R0,f0),P.copy(Y0)},reset:function(){A=!1,U0=null,P.set(-1,0,0,0)}}}function Q(){let A=!1,Y0=!1,U0=null,P=null,r=null;return{setReversed:function(a){if(Y0!==a){let R0=$.get("EXT_clip_control");if(a)R0.clipControlEXT(R0.LOWER_LEFT_EXT,R0.ZERO_TO_ONE_EXT);else R0.clipControlEXT(R0.LOWER_LEFT_EXT,R0.NEGATIVE_ONE_TO_ONE_EXT);Y0=a;let f0=r;r=null,this.setClear(f0)}},getReversed:function(){return Y0},setTest:function(a){if(a)x(J.DEPTH_TEST);else J0(J.DEPTH_TEST)},setMask:function(a){if(U0!==a&&!A)J.depthMask(a),U0=a},setFunc:function(a){if(Y0)a=UX[a];if(P!==a){switch(a){case P6:J.depthFunc(J.NEVER);break;case A6:J.depthFunc(J.ALWAYS);break;case T6:J.depthFunc(J.LESS);break;case lJ:J.depthFunc(J.LEQUAL);break;case S6:J.depthFunc(J.EQUAL);break;case b6:J.depthFunc(J.GEQUAL);break;case y6:J.depthFunc(J.GREATER);break;case j6:J.depthFunc(J.NOTEQUAL);break;default:J.depthFunc(J.LEQUAL)}P=a}},setLocked:function(a){A=a},setClear:function(a){if(r!==a){if(Y0)a=1-a;J.clearDepth(a),r=a}},reset:function(){A=!1,U0=null,P=null,r=null,Y0=!1}}}function Z(){let A=!1,Y0=null,U0=null,P=null,r=null,a=null,R0=null,f0=null,t0=null;return{setTest:function(e0){if(!A)if(e0)x(J.STENCIL_TEST);else J0(J.STENCIL_TEST)},setMask:function(e0){if(Y0!==e0&&!A)J.stencilMask(e0),Y0=e0},setFunc:function(e0,Q9,Z9){if(U0!==e0||P!==Q9||r!==Z9)J.stencilFunc(e0,Q9,Z9),U0=e0,P=Q9,r=Z9},setOp:function(e0,Q9,Z9){if(a!==e0||R0!==Q9||f0!==Z9)J.stencilOp(e0,Q9,Z9),a=e0,R0=Q9,f0=Z9},setLocked:function(e0){A=e0},setClear:function(e0){if(t0!==e0)J.clearStencil(e0),t0=e0},reset:function(){A=!1,Y0=null,U0=null,P=null,r=null,a=null,R0=null,f0=null,t0=null}}}let W=new N,Y=new Q,K=new Z,H=new WeakMap,X=new WeakMap,G={},E={},q=new WeakMap,k=[],M=null,_=!1,D=null,U=null,C=null,L=null,B=null,y=null,I=null,b=new i0(0,0,0),g=0,V=!1,F=null,S=null,s=null,i=null,m=null,T=J.getParameter(J.MAX_COMBINED_TEXTURE_IMAGE_UNITS),p=!1,$0=0,c=J.getParameter(J.VERSION);if(c.indexOf("WebGL")!==-1)$0=parseFloat(/^WebGL (\d)/.exec(c)[1]),p=$0>=1;else if(c.indexOf("OpenGL ES")!==-1)$0=parseFloat(/^OpenGL ES (\d)/.exec(c)[1]),p=$0>=2;let X0=null,q0={},P0=J.getParameter(J.SCISSOR_BOX),d0=J.getParameter(J.VIEWPORT),J8=new D8().fromArray(P0),c0=new D8().fromArray(d0);function t(A,Y0,U0,P){let r=new Uint8Array(4),a=J.createTexture();J.bindTexture(A,a),J.texParameteri(A,J.TEXTURE_MIN_FILTER,J.NEAREST),J.texParameteri(A,J.TEXTURE_MAG_FILTER,J.NEAREST);for(let R0=0;R0<U0;R0++)if(A===J.TEXTURE_3D||A===J.TEXTURE_2D_ARRAY)J.texImage3D(Y0,0,J.RGBA,1,1,P,0,J.RGBA,J.UNSIGNED_BYTE,r);else J.texImage2D(Y0+R0,0,J.RGBA,1,1,0,J.RGBA,J.UNSIGNED_BYTE,r);return a}let K0={};K0[J.TEXTURE_2D]=t(J.TEXTURE_2D,J.TEXTURE_2D,1),K0[J.TEXTURE_CUBE_MAP]=t(J.TEXTURE_CUBE_MAP,J.TEXTURE_CUBE_MAP_POSITIVE_X,6),K0[J.TEXTURE_2D_ARRAY]=t(J.TEXTURE_2D_ARRAY,J.TEXTURE_2D_ARRAY,1,1),K0[J.TEXTURE_3D]=t(J.TEXTURE_3D,J.TEXTURE_3D,1,1),W.setClear(0,0,0,1),Y.setClear(1),K.setClear(0),x(J.DEPTH_TEST),Y.setFunc(lJ),L0(!1),a0(p7),x(J.CULL_FACE),y0(T9);function x(A){if(G[A]!==!0)J.enable(A),G[A]=!0}function J0(A){if(G[A]!==!1)J.disable(A),G[A]=!1}function Q0(A,Y0){if(E[A]!==Y0){if(J.bindFramebuffer(A,Y0),E[A]=Y0,A===J.DRAW_FRAMEBUFFER)E[J.FRAMEBUFFER]=Y0;if(A===J.FRAMEBUFFER)E[J.DRAW_FRAMEBUFFER]=Y0;return!0}return!1}function E0(A,Y0){let U0=k,P=!1;if(A){if(U0=q.get(Y0),U0===void 0)U0=[],q.set(Y0,U0);let r=A.textures;if(U0.length!==r.length||U0[0]!==J.COLOR_ATTACHMENT0){for(let a=0,R0=r.length;a<R0;a++)U0[a]=J.COLOR_ATTACHMENT0+a;U0.length=r.length,P=!0}}else if(U0[0]!==J.BACK)U0[0]=J.BACK,P=!0;if(P)J.drawBuffers(U0)}function m0(A){if(M!==A)return J.useProgram(A),M=A,!0;return!1}let z={[FJ]:J.FUNC_ADD,[tN]:J.FUNC_SUBTRACT,[eN]:J.FUNC_REVERSE_SUBTRACT};z[JQ]=J.MIN,z[$Q]=J.MAX;let x0={[NQ]:J.ZERO,[QQ]:J.ONE,[ZQ]:J.SRC_COLOR,[KQ]:J.SRC_ALPHA,[EQ]:J.SRC_ALPHA_SATURATE,[UQ]:J.DST_COLOR,[HQ]:J.DST_ALPHA,[WQ]:J.ONE_MINUS_SRC_COLOR,[YQ]:J.ONE_MINUS_SRC_ALPHA,[GQ]:J.ONE_MINUS_DST_COLOR,[XQ]:J.ONE_MINUS_DST_ALPHA,[DQ]:J.CONSTANT_COLOR,[qQ]:J.ONE_MINUS_CONSTANT_COLOR,[kQ]:J.CONSTANT_ALPHA,[OQ]:J.ONE_MINUS_CONSTANT_ALPHA};function y0(A,Y0,U0,P,r,a,R0,f0,t0,e0){if(A===T9){if(_===!0)J0(J.BLEND),_=!1;return}if(_===!1)x(J.BLEND),_=!0;if(A!==rN){if(A!==D||e0!==V){if(U!==FJ||B!==FJ)J.blendEquation(J.FUNC_ADD),U=FJ,B=FJ;if(e0)switch(A){case dJ:J.blendFuncSeparate(J.ONE,J.ONE_MINUS_SRC_ALPHA,J.ONE,J.ONE_MINUS_SRC_ALPHA);break;case l7:J.blendFunc(J.ONE,J.ONE);break;case m7:J.blendFuncSeparate(J.ZERO,J.ONE_MINUS_SRC_COLOR,J.ZERO,J.ONE);break;case u7:J.blendFuncSeparate(J.DST_COLOR,J.ONE_MINUS_SRC_ALPHA,J.ZERO,J.ONE);break;default:console.error("THREE.WebGLState: Invalid blending: ",A);break}else switch(A){case dJ:J.blendFuncSeparate(J.SRC_ALPHA,J.ONE_MINUS_SRC_ALPHA,J.ONE,J.ONE_MINUS_SRC_ALPHA);break;case l7:J.blendFuncSeparate(J.SRC_ALPHA,J.ONE,J.ONE,J.ONE);break;case m7:console.error("THREE.WebGLState: SubtractiveBlending requires material.premultipliedAlpha = true");break;case u7:console.error("THREE.WebGLState: MultiplyBlending requires material.premultipliedAlpha = true");break;default:console.error("THREE.WebGLState: Invalid blending: ",A);break}C=null,L=null,y=null,I=null,b.set(0,0,0),g=0,D=A,V=e0}return}if(r=r||Y0,a=a||U0,R0=R0||P,Y0!==U||r!==B)J.blendEquationSeparate(z[Y0],z[r]),U=Y0,B=r;if(U0!==C||P!==L||a!==y||R0!==I)J.blendFuncSeparate(x0[U0],x0[P],x0[a],x0[R0]),C=U0,L=P,y=a,I=R0;if(f0.equals(b)===!1||t0!==g)J.blendColor(f0.r,f0.g,f0.b,t0),b.copy(f0),g=t0;D=A,V=!1}function S0(A,Y0){A.side===a8?J0(J.CULL_FACE):x(J.CULL_FACE);let U0=A.side===m8;if(Y0)U0=!U0;L0(U0),A.blending===dJ&&A.transparent===!1?y0(T9):y0(A.blending,A.blendEquation,A.blendSrc,A.blendDst,A.blendEquationAlpha,A.blendSrcAlpha,A.blendDstAlpha,A.blendColor,A.blendAlpha,A.premultipliedAlpha),Y.setFunc(A.depthFunc),Y.setTest(A.depthTest),Y.setMask(A.depthWrite),W.setMask(A.colorWrite);let P=A.stencilWrite;if(K.setTest(P),P)K.setMask(A.stencilWriteMask),K.setFunc(A.stencilFunc,A.stencilRef,A.stencilFuncMask),K.setOp(A.stencilFail,A.stencilZFail,A.stencilZPass);C0(A.polygonOffset,A.polygonOffsetFactor,A.polygonOffsetUnits),A.alphaToCoverage===!0?x(J.SAMPLE_ALPHA_TO_COVERAGE):J0(J.SAMPLE_ALPHA_TO_COVERAGE)}function L0(A){if(F!==A){if(A)J.frontFace(J.CW);else J.frontFace(J.CCW);F=A}}function a0(A){if(A!==iN){if(x(J.CULL_FACE),A!==S)if(A===p7)J.cullFace(J.BACK);else if(A===oN)J.cullFace(J.FRONT);else J.cullFace(J.FRONT_AND_BACK)}else J0(J.CULL_FACE);S=A}function B0(A){if(A!==s){if(p)J.lineWidth(A);s=A}}function C0(A,Y0,U0){if(A){if(x(J.POLYGON_OFFSET_FILL),i!==Y0||m!==U0)J.polygonOffset(Y0,U0),i=Y0,m=U0}else J0(J.POLYGON_OFFSET_FILL)}function X8(A){if(A)x(J.SCISSOR_TEST);else J0(J.SCISSOR_TEST)}function k8(A){if(A===void 0)A=J.TEXTURE0+T-1;if(X0!==A)J.activeTexture(A),X0=A}function N8(A,Y0,U0){if(U0===void 0)if(X0===null)U0=J.TEXTURE0+T-1;else U0=X0;let P=q0[U0];if(P===void 0)P={type:void 0,texture:void 0},q0[U0]=P;if(P.type!==A||P.texture!==Y0){if(X0!==U0)J.activeTexture(U0),X0=U0;J.bindTexture(A,Y0||K0[A]),P.type=A,P.texture=Y0}}function w(){let A=q0[X0];if(A!==void 0&&A.type!==void 0)J.bindTexture(A.type,null),A.type=void 0,A.texture=void 0}function O(){try{J.compressedTexImage2D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function j(){try{J.compressedTexImage3D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function o(){try{J.texSubImage2D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function u(){try{J.texSubImage3D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function n(){try{J.compressedTexSubImage2D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function O0(){try{J.compressedTexSubImage3D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function H0(){try{J.texStorage2D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function w0(){try{J.texStorage3D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function b0(){try{J.texImage2D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function e(){try{J.texImage3D(...arguments)}catch(A){console.error("THREE.WebGLState:",A)}}function Z0(A){if(J8.equals(A)===!1)J.scissor(A.x,A.y,A.z,A.w),J8.copy(A)}function _0(A){if(c0.equals(A)===!1)J.viewport(A.x,A.y,A.z,A.w),c0.copy(A)}function F0(A,Y0){let U0=X.get(Y0);if(U0===void 0)U0=new WeakMap,X.set(Y0,U0);let P=U0.get(A);if(P===void 0)P=J.getUniformBlockIndex(Y0,A.name),U0.set(A,P)}function G0(A,Y0){let P=X.get(Y0).get(A);if(H.get(Y0)!==P)J.uniformBlockBinding(Y0,P,A.__bindingPointIndex),H.set(Y0,P)}function I0(){J.disable(J.BLEND),J.disable(J.CULL_FACE),J.disable(J.DEPTH_TEST),J.disable(J.POLYGON_OFFSET_FILL),J.disable(J.SCISSOR_TEST),J.disable(J.STENCIL_TEST),J.disable(J.SAMPLE_ALPHA_TO_COVERAGE),J.blendEquation(J.FUNC_ADD),J.blendFunc(J.ONE,J.ZERO),J.blendFuncSeparate(J.ONE,J.ZERO,J.ONE,J.ZERO),J.blendColor(0,0,0,0),J.colorMask(!0,!0,!0,!0),J.clearColor(0,0,0,0),J.depthMask(!0),J.depthFunc(J.LESS),Y.setReversed(!1),J.clearDepth(1),J.stencilMask(4294967295),J.stencilFunc(J.ALWAYS,0,4294967295),J.stencilOp(J.KEEP,J.KEEP,J.KEEP),J.clearStencil(0),J.cullFace(J.BACK),J.frontFace(J.CCW),J.polygonOffset(0,0),J.activeTexture(J.TEXTURE0),J.bindFramebuffer(J.FRAMEBUFFER,null),J.bindFramebuffer(J.DRAW_FRAMEBUFFER,null),J.bindFramebuffer(J.READ_FRAMEBUFFER,null),J.useProgram(null),J.lineWidth(1),J.scissor(0,0,J.canvas.width,J.canvas.height),J.viewport(0,0,J.canvas.width,J.canvas.height),G={},X0=null,q0={},E={},q=new WeakMap,k=[],M=null,_=!1,D=null,U=null,C=null,L=null,B=null,y=null,I=null,b=new i0(0,0,0),g=0,V=!1,F=null,S=null,s=null,i=null,m=null,J8.set(0,0,J.canvas.width,J.canvas.height),c0.set(0,0,J.canvas.width,J.canvas.height),W.reset(),Y.reset(),K.reset()}return{buffers:{color:W,depth:Y,stencil:K},enable:x,disable:J0,bindFramebuffer:Q0,drawBuffers:E0,useProgram:m0,setBlending:y0,setMaterial:S0,setFlipSided:L0,setCullFace:a0,setLineWidth:B0,setPolygonOffset:C0,setScissorTest:X8,activeTexture:k8,bindTexture:N8,unbindTexture:w,compressedTexImage2D:O,compressedTexImage3D:j,texImage2D:b0,texImage3D:e,updateUBOMapping:F0,uniformBlockBinding:G0,texStorage2D:H0,texStorage3D:w0,texSubImage2D:o,texSubImage3D:u,compressedTexSubImage2D:n,compressedTexSubImage3D:O0,scissor:Z0,viewport:_0,reset:I0}}function EX(J,$,N,Q,Z,W,Y){let K=$.has("WEBGL_multisampled_render_to_texture")?$.get("WEBGL_multisampled_render_to_texture"):null,H=typeof navigator>"u"?!1:/OculusBrowser/g.test(navigator.userAgent),X=new p0,G=new WeakMap,E,q=new WeakMap,k=!1;try{k=typeof OffscreenCanvas<"u"&&new OffscreenCanvas(1,1).getContext("2d")!==null}catch(w){}function M(w,O){return k?new OffscreenCanvas(w,O):pJ("canvas")}function _(w,O,j){let o=1,u=N8(w);if(u.width>j||u.height>j)o=j/Math.max(u.width,u.height);if(o<1)if(typeof HTMLImageElement<"u"&&w instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&w instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&w instanceof ImageBitmap||typeof VideoFrame<"u"&&w instanceof VideoFrame){let n=Math.floor(o*u.width),O0=Math.floor(o*u.height);if(E===void 0)E=M(n,O0);let H0=O?M(n,O0):E;return H0.width=n,H0.height=O0,H0.getContext("2d").drawImage(w,0,0,n,O0),console.warn("THREE.WebGLRenderer: Texture has been resized from ("+u.width+"x"+u.height+") to ("+n+"x"+O0+")."),H0}else{if("data"in w)console.warn("THREE.WebGLRenderer: Image in DataTexture is too big ("+u.width+"x"+u.height+").");return w}return w}function D(w){return w.generateMipmaps}function U(w){J.generateMipmap(w)}function C(w){if(w.isWebGLCubeRenderTarget)return J.TEXTURE_CUBE_MAP;if(w.isWebGL3DRenderTarget)return J.TEXTURE_3D;if(w.isWebGLArrayRenderTarget||w.isCompressedArrayTexture)return J.TEXTURE_2D_ARRAY;return J.TEXTURE_2D}function L(w,O,j,o,u=!1){if(w!==null){if(J[w]!==void 0)return J[w];console.warn("THREE.WebGLRenderer: Attempt to use non-existing WebGL internal format '"+w+"'")}let n=O;if(O===J.RED){if(j===J.FLOAT)n=J.R32F;if(j===J.HALF_FLOAT)n=J.R16F;if(j===J.UNSIGNED_BYTE)n=J.R8}if(O===J.RED_INTEGER){if(j===J.UNSIGNED_BYTE)n=J.R8UI;if(j===J.UNSIGNED_SHORT)n=J.R16UI;if(j===J.UNSIGNED_INT)n=J.R32UI;if(j===J.BYTE)n=J.R8I;if(j===J.SHORT)n=J.R16I;if(j===J.INT)n=J.R32I}if(O===J.RG){if(j===J.FLOAT)n=J.RG32F;if(j===J.HALF_FLOAT)n=J.RG16F;if(j===J.UNSIGNED_BYTE)n=J.RG8}if(O===J.RG_INTEGER){if(j===J.UNSIGNED_BYTE)n=J.RG8UI;if(j===J.UNSIGNED_SHORT)n=J.RG16UI;if(j===J.UNSIGNED_INT)n=J.RG32UI;if(j===J.BYTE)n=J.RG8I;if(j===J.SHORT)n=J.RG16I;if(j===J.INT)n=J.RG32I}if(O===J.RGB_INTEGER){if(j===J.UNSIGNED_BYTE)n=J.RGB8UI;if(j===J.UNSIGNED_SHORT)n=J.RGB16UI;if(j===J.UNSIGNED_INT)n=J.RGB32UI;if(j===J.BYTE)n=J.RGB8I;if(j===J.SHORT)n=J.RGB16I;if(j===J.INT)n=J.RGB32I}if(O===J.RGBA_INTEGER){if(j===J.UNSIGNED_BYTE)n=J.RGBA8UI;if(j===J.UNSIGNED_SHORT)n=J.RGBA16UI;if(j===J.UNSIGNED_INT)n=J.RGBA32UI;if(j===J.BYTE)n=J.RGBA8I;if(j===J.SHORT)n=J.RGBA16I;if(j===J.INT)n=J.RGBA32I}if(O===J.RGB){if(j===J.UNSIGNED_INT_5_9_9_9_REV)n=J.RGB9_E5;if(j===J.UNSIGNED_INT_10F_11F_11F_REV)n=J.R11F_G11F_B10F}if(O===J.RGBA){let O0=u?B$:o0.getTransfer(o);if(j===J.FLOAT)n=J.RGBA32F;if(j===J.HALF_FLOAT)n=J.RGBA16F;if(j===J.UNSIGNED_BYTE)n=O0===Y8?J.SRGB8_ALPHA8:J.RGBA8;if(j===J.UNSIGNED_SHORT_4_4_4_4)n=J.RGBA4;if(j===J.UNSIGNED_SHORT_5_5_5_1)n=J.RGB5_A1}if(n===J.R16F||n===J.R32F||n===J.RG16F||n===J.RG32F||n===J.RGBA16F||n===J.RGBA32F)$.get("EXT_color_buffer_float");return n}function B(w,O){let j;if(w){if(O===null||O===VJ||O===wJ)j=J.DEPTH24_STENCIL8;else if(O===b9)j=J.DEPTH32F_STENCIL8;else if(O===cJ)j=J.DEPTH24_STENCIL8,console.warn("DepthTexture: 16 bit depth attachment is not supported with stencil. Using 24-bit attachment.")}else if(O===null||O===VJ||O===wJ)j=J.DEPTH_COMPONENT24;else if(O===b9)j=J.DEPTH_COMPONENT32F;else if(O===cJ)j=J.DEPTH_COMPONENT16;return j}function y(w,O){if(D(w)===!0||w.isFramebufferTexture&&w.minFilter!==_J&&w.minFilter!==d9)return Math.log2(Math.max(O.width,O.height))+1;else if(w.mipmaps!==void 0&&w.mipmaps.length>0)return w.mipmaps.length;else if(w.isCompressedTexture&&Array.isArray(w.image))return O.mipmaps.length;else return 1}function I(w){let O=w.target;if(O.removeEventListener("dispose",I),g(O),O.isVideoTexture)G.delete(O)}function b(w){let O=w.target;O.removeEventListener("dispose",b),F(O)}function g(w){let O=Q.get(w);if(O.__webglInit===void 0)return;let j=w.source,o=q.get(j);if(o){let u=o[O.__cacheKey];if(u.usedTimes--,u.usedTimes===0)V(w);if(Object.keys(o).length===0)q.delete(j)}Q.remove(w)}function V(w){let O=Q.get(w);J.deleteTexture(O.__webglTexture);let j=w.source,o=q.get(j);delete o[O.__cacheKey],Y.memory.textures--}function F(w){let O=Q.get(w);if(w.depthTexture)w.depthTexture.dispose(),Q.remove(w.depthTexture);if(w.isWebGLCubeRenderTarget)for(let o=0;o<6;o++){if(Array.isArray(O.__webglFramebuffer[o]))for(let u=0;u<O.__webglFramebuffer[o].length;u++)J.deleteFramebuffer(O.__webglFramebuffer[o][u]);else J.deleteFramebuffer(O.__webglFramebuffer[o]);if(O.__webglDepthbuffer)J.deleteRenderbuffer(O.__webglDepthbuffer[o])}else{if(Array.isArray(O.__webglFramebuffer))for(let o=0;o<O.__webglFramebuffer.length;o++)J.deleteFramebuffer(O.__webglFramebuffer[o]);else J.deleteFramebuffer(O.__webglFramebuffer);if(O.__webglDepthbuffer)J.deleteRenderbuffer(O.__webglDepthbuffer);if(O.__webglMultisampledFramebuffer)J.deleteFramebuffer(O.__webglMultisampledFramebuffer);if(O.__webglColorRenderbuffer){for(let o=0;o<O.__webglColorRenderbuffer.length;o++)if(O.__webglColorRenderbuffer[o])J.deleteRenderbuffer(O.__webglColorRenderbuffer[o])}if(O.__webglDepthRenderbuffer)J.deleteRenderbuffer(O.__webglDepthRenderbuffer)}let j=w.textures;for(let o=0,u=j.length;o<u;o++){let n=Q.get(j[o]);if(n.__webglTexture)J.deleteTexture(n.__webglTexture),Y.memory.textures--;Q.remove(j[o])}Q.remove(w)}let S=0;function s(){S=0}function i(){let w=S;if(w>=Z.maxTextures)console.warn("THREE.WebGLTextures: Trying to use "+w+" texture units while this GPU supports only "+Z.maxTextures);return S+=1,w}function m(w){let O=[];return O.push(w.wrapS),O.push(w.wrapT),O.push(w.wrapR||0),O.push(w.magFilter),O.push(w.minFilter),O.push(w.anisotropy),O.push(w.internalFormat),O.push(w.format),O.push(w.type),O.push(w.generateMipmaps),O.push(w.premultiplyAlpha),O.push(w.flipY),O.push(w.unpackAlignment),O.push(w.colorSpace),O.join()}function T(w,O){let j=Q.get(w);if(w.isVideoTexture)X8(w);if(w.isRenderTargetTexture===!1&&w.isExternalTexture!==!0&&w.version>0&&j.__version!==w.version){let o=w.image;if(o===null)console.warn("THREE.WebGLRenderer: Texture marked for update but no image data found.");else if(o.complete===!1)console.warn("THREE.WebGLRenderer: Texture marked for update but image is incomplete");else{K0(j,w,O);return}}else if(w.isExternalTexture)j.__webglTexture=w.sourceTexture?w.sourceTexture:null;N.bindTexture(J.TEXTURE_2D,j.__webglTexture,J.TEXTURE0+O)}function p(w,O){let j=Q.get(w);if(w.isRenderTargetTexture===!1&&w.version>0&&j.__version!==w.version){K0(j,w,O);return}N.bindTexture(J.TEXTURE_2D_ARRAY,j.__webglTexture,J.TEXTURE0+O)}function $0(w,O){let j=Q.get(w);if(w.isRenderTargetTexture===!1&&w.version>0&&j.__version!==w.version){K0(j,w,O);return}N.bindTexture(J.TEXTURE_3D,j.__webglTexture,J.TEXTURE0+O)}function c(w,O){let j=Q.get(w);if(w.version>0&&j.__version!==w.version){x(j,w,O);return}N.bindTexture(J.TEXTURE_CUBE_MAP,j.__webglTexture,J.TEXTURE0+O)}let X0={[IQ]:J.REPEAT,[PQ]:J.CLAMP_TO_EDGE,[AQ]:J.MIRRORED_REPEAT},q0={[_J]:J.NEAREST,[TQ]:J.NEAREST_MIPMAP_NEAREST,[uJ]:J.NEAREST_MIPMAP_LINEAR,[d9]:J.LINEAR,[h6]:J.LINEAR_MIPMAP_NEAREST,[LJ]:J.LINEAR_MIPMAP_LINEAR},P0={[mQ]:J.NEVER,[oQ]:J.ALWAYS,[uQ]:J.LESS,[C$]:J.LEQUAL,[cQ]:J.EQUAL,[iQ]:J.GEQUAL,[nQ]:J.GREATER,[sQ]:J.NOTEQUAL};function d0(w,O){if(O.type===b9&&$.has("OES_texture_float_linear")===!1&&(O.magFilter===d9||O.magFilter===h6||O.magFilter===uJ||O.magFilter===LJ||O.minFilter===d9||O.minFilter===h6||O.minFilter===uJ||O.minFilter===LJ))console.warn("THREE.WebGLRenderer: Unable to use linear filtering with floating point textures. OES_texture_float_linear not supported on this device.");if(J.texParameteri(w,J.TEXTURE_WRAP_S,X0[O.wrapS]),J.texParameteri(w,J.TEXTURE_WRAP_T,X0[O.wrapT]),w===J.TEXTURE_3D||w===J.TEXTURE_2D_ARRAY)J.texParameteri(w,J.TEXTURE_WRAP_R,X0[O.wrapR]);if(J.texParameteri(w,J.TEXTURE_MAG_FILTER,q0[O.magFilter]),J.texParameteri(w,J.TEXTURE_MIN_FILTER,q0[O.minFilter]),O.compareFunction)J.texParameteri(w,J.TEXTURE_COMPARE_MODE,J.COMPARE_REF_TO_TEXTURE),J.texParameteri(w,J.TEXTURE_COMPARE_FUNC,P0[O.compareFunction]);if($.has("EXT_texture_filter_anisotropic")===!0){if(O.magFilter===_J)return;if(O.minFilter!==uJ&&O.minFilter!==LJ)return;if(O.type===b9&&$.has("OES_texture_float_linear")===!1)return;if(O.anisotropy>1||Q.get(O).__currentAnisotropy){let j=$.get("EXT_texture_filter_anisotropic");J.texParameterf(w,j.TEXTURE_MAX_ANISOTROPY_EXT,Math.min(O.anisotropy,Z.getMaxAnisotropy())),Q.get(O).__currentAnisotropy=O.anisotropy}}}function J8(w,O){let j=!1;if(w.__webglInit===void 0)w.__webglInit=!0,O.addEventListener("dispose",I);let o=O.source,u=q.get(o);if(u===void 0)u={},q.set(o,u);let n=m(O);if(n!==w.__cacheKey){if(u[n]===void 0)u[n]={texture:J.createTexture(),usedTimes:0},Y.memory.textures++,j=!0;u[n].usedTimes++;let O0=u[w.__cacheKey];if(O0!==void 0){if(u[w.__cacheKey].usedTimes--,O0.usedTimes===0)V(O)}w.__cacheKey=n,w.__webglTexture=u[n].texture}return j}function c0(w,O,j){return Math.floor(Math.floor(w/j)/O)}function t(w,O,j,o){let n=w.updateRanges;if(n.length===0)N.texSubImage2D(J.TEXTURE_2D,0,0,0,O.width,O.height,j,o,O.data);else{n.sort((e,Z0)=>e.start-Z0.start);let O0=0;for(let e=1;e<n.length;e++){let Z0=n[O0],_0=n[e],F0=Z0.start+Z0.count,G0=c0(_0.start,O.width,4),I0=c0(Z0.start,O.width,4);if(_0.start<=F0+1&&G0===I0&&c0(_0.start+_0.count-1,O.width,4)===G0)Z0.count=Math.max(Z0.count,_0.start+_0.count-Z0.start);else++O0,n[O0]=_0}n.length=O0+1;let H0=J.getParameter(J.UNPACK_ROW_LENGTH),w0=J.getParameter(J.UNPACK_SKIP_PIXELS),b0=J.getParameter(J.UNPACK_SKIP_ROWS);J.pixelStorei(J.UNPACK_ROW_LENGTH,O.width);for(let e=0,Z0=n.length;e<Z0;e++){let _0=n[e],F0=Math.floor(_0.start/4),G0=Math.ceil(_0.count/4),I0=F0%O.width,A=Math.floor(F0/O.width),Y0=G0,U0=1;J.pixelStorei(J.UNPACK_SKIP_PIXELS,I0),J.pixelStorei(J.UNPACK_SKIP_ROWS,A),N.texSubImage2D(J.TEXTURE_2D,0,I0,A,Y0,1,j,o,O.data)}w.clearUpdateRanges(),J.pixelStorei(J.UNPACK_ROW_LENGTH,H0),J.pixelStorei(J.UNPACK_SKIP_PIXELS,w0),J.pixelStorei(J.UNPACK_SKIP_ROWS,b0)}}function K0(w,O,j){let o=J.TEXTURE_2D;if(O.isDataArrayTexture||O.isCompressedArrayTexture)o=J.TEXTURE_2D_ARRAY;if(O.isData3DTexture)o=J.TEXTURE_3D;let u=J8(w,O),n=O.source;N.bindTexture(o,w.__webglTexture,J.TEXTURE0+j);let O0=Q.get(n);if(n.version!==O0.__version||u===!0){N.activeTexture(J.TEXTURE0+j);let H0=o0.getPrimaries(o0.workingColorSpace),w0=O.colorSpace===l9?null:o0.getPrimaries(O.colorSpace),b0=O.colorSpace===l9||H0===w0?J.NONE:J.BROWSER_DEFAULT_WEBGL;J.pixelStorei(J.UNPACK_FLIP_Y_WEBGL,O.flipY),J.pixelStorei(J.UNPACK_PREMULTIPLY_ALPHA_WEBGL,O.premultiplyAlpha),J.pixelStorei(J.UNPACK_ALIGNMENT,O.unpackAlignment),J.pixelStorei(J.UNPACK_COLORSPACE_CONVERSION_WEBGL,b0);let e=_(O.image,!1,Z.maxTextureSize);e=k8(O,e);let Z0=W.convert(O.format,O.colorSpace),_0=W.convert(O.type),F0=L(O.internalFormat,Z0,_0,O.colorSpace,O.isVideoTexture);d0(o,O);let G0,I0=O.mipmaps,A=O.isVideoTexture!==!0,Y0=O0.__version===void 0||u===!0,U0=n.dataReady,P=y(O,e);if(O.isDepthTexture){if(F0=B(O.format===sJ,O.type),Y0)if(A)N.texStorage2D(J.TEXTURE_2D,1,F0,e.width,e.height);else N.texImage2D(J.TEXTURE_2D,0,F0,e.width,e.height,0,Z0,_0,null)}else if(O.isDataTexture)if(I0.length>0){if(A&&Y0)N.texStorage2D(J.TEXTURE_2D,P,F0,I0[0].width,I0[0].height);for(let r=0,a=I0.length;r<a;r++)if(G0=I0[r],A){if(U0)N.texSubImage2D(J.TEXTURE_2D,r,0,0,G0.width,G0.height,Z0,_0,G0.data)}else N.texImage2D(J.TEXTURE_2D,r,F0,G0.width,G0.height,0,Z0,_0,G0.data);O.generateMipmaps=!1}else if(A){if(Y0)N.texStorage2D(J.TEXTURE_2D,P,F0,e.width,e.height);if(U0)t(O,e,Z0,_0)}else N.texImage2D(J.TEXTURE_2D,0,F0,e.width,e.height,0,Z0,_0,e.data);else if(O.isCompressedTexture)if(O.isCompressedArrayTexture){if(A&&Y0)N.texStorage3D(J.TEXTURE_2D_ARRAY,P,F0,I0[0].width,I0[0].height,e.depth);for(let r=0,a=I0.length;r<a;r++)if(G0=I0[r],O.format!==K9)if(Z0!==null)if(A){if(U0)if(O.layerUpdates.size>0){let R0=t$(G0.width,G0.height,O.format,O.type);for(let f0 of O.layerUpdates){let t0=G0.data.subarray(f0*R0/G0.data.BYTES_PER_ELEMENT,(f0+1)*R0/G0.data.BYTES_PER_ELEMENT);N.compressedTexSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,f0,G0.width,G0.height,1,Z0,t0)}O.clearLayerUpdates()}else N.compressedTexSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,0,G0.width,G0.height,e.depth,Z0,G0.data)}else N.compressedTexImage3D(J.TEXTURE_2D_ARRAY,r,F0,G0.width,G0.height,e.depth,0,G0.data,0,0);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .uploadTexture()");else if(A){if(U0)N.texSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,0,G0.width,G0.height,e.depth,Z0,_0,G0.data)}else N.texImage3D(J.TEXTURE_2D_ARRAY,r,F0,G0.width,G0.height,e.depth,0,Z0,_0,G0.data)}else{if(A&&Y0)N.texStorage2D(J.TEXTURE_2D,P,F0,I0[0].width,I0[0].height);for(let r=0,a=I0.length;r<a;r++)if(G0=I0[r],O.format!==K9)if(Z0!==null)if(A){if(U0)N.compressedTexSubImage2D(J.TEXTURE_2D,r,0,0,G0.width,G0.height,Z0,G0.data)}else N.compressedTexImage2D(J.TEXTURE_2D,r,F0,G0.width,G0.height,0,G0.data);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .uploadTexture()");else if(A){if(U0)N.texSubImage2D(J.TEXTURE_2D,r,0,0,G0.width,G0.height,Z0,_0,G0.data)}else N.texImage2D(J.TEXTURE_2D,r,F0,G0.width,G0.height,0,Z0,_0,G0.data)}else if(O.isDataArrayTexture)if(A){if(Y0)N.texStorage3D(J.TEXTURE_2D_ARRAY,P,F0,e.width,e.height,e.depth);if(U0)if(O.layerUpdates.size>0){let r=t$(e.width,e.height,O.format,O.type);for(let a of O.layerUpdates){let R0=e.data.subarray(a*r/e.data.BYTES_PER_ELEMENT,(a+1)*r/e.data.BYTES_PER_ELEMENT);N.texSubImage3D(J.TEXTURE_2D_ARRAY,0,0,0,a,e.width,e.height,1,Z0,_0,R0)}O.clearLayerUpdates()}else N.texSubImage3D(J.TEXTURE_2D_ARRAY,0,0,0,0,e.width,e.height,e.depth,Z0,_0,e.data)}else N.texImage3D(J.TEXTURE_2D_ARRAY,0,F0,e.width,e.height,e.depth,0,Z0,_0,e.data);else if(O.isData3DTexture)if(A){if(Y0)N.texStorage3D(J.TEXTURE_3D,P,F0,e.width,e.height,e.depth);if(U0)N.texSubImage3D(J.TEXTURE_3D,0,0,0,0,e.width,e.height,e.depth,Z0,_0,e.data)}else N.texImage3D(J.TEXTURE_3D,0,F0,e.width,e.height,e.depth,0,Z0,_0,e.data);else if(O.isFramebufferTexture){if(Y0)if(A)N.texStorage2D(J.TEXTURE_2D,P,F0,e.width,e.height);else{let{width:r,height:a}=e;for(let R0=0;R0<P;R0++)N.texImage2D(J.TEXTURE_2D,R0,F0,r,a,0,Z0,_0,null),r>>=1,a>>=1}}else if(I0.length>0){if(A&&Y0){let r=N8(I0[0]);N.texStorage2D(J.TEXTURE_2D,P,F0,r.width,r.height)}for(let r=0,a=I0.length;r<a;r++)if(G0=I0[r],A){if(U0)N.texSubImage2D(J.TEXTURE_2D,r,0,0,Z0,_0,G0)}else N.texImage2D(J.TEXTURE_2D,r,F0,Z0,_0,G0);O.generateMipmaps=!1}else if(A){if(Y0){let r=N8(e);N.texStorage2D(J.TEXTURE_2D,P,F0,r.width,r.height)}if(U0)N.texSubImage2D(J.TEXTURE_2D,0,0,0,Z0,_0,e)}else N.texImage2D(J.TEXTURE_2D,0,F0,Z0,_0,e);if(D(O))U(o);if(O0.__version=n.version,O.onUpdate)O.onUpdate(O)}w.__version=O.version}function x(w,O,j){if(O.image.length!==6)return;let o=J8(w,O),u=O.source;N.bindTexture(J.TEXTURE_CUBE_MAP,w.__webglTexture,J.TEXTURE0+j);let n=Q.get(u);if(u.version!==n.__version||o===!0){N.activeTexture(J.TEXTURE0+j);let O0=o0.getPrimaries(o0.workingColorSpace),H0=O.colorSpace===l9?null:o0.getPrimaries(O.colorSpace),w0=O.colorSpace===l9||O0===H0?J.NONE:J.BROWSER_DEFAULT_WEBGL;J.pixelStorei(J.UNPACK_FLIP_Y_WEBGL,O.flipY),J.pixelStorei(J.UNPACK_PREMULTIPLY_ALPHA_WEBGL,O.premultiplyAlpha),J.pixelStorei(J.UNPACK_ALIGNMENT,O.unpackAlignment),J.pixelStorei(J.UNPACK_COLORSPACE_CONVERSION_WEBGL,w0);let b0=O.isCompressedTexture||O.image[0].isCompressedTexture,e=O.image[0]&&O.image[0].isDataTexture,Z0=[];for(let a=0;a<6;a++){if(!b0&&!e)Z0[a]=_(O.image[a],!0,Z.maxCubemapSize);else Z0[a]=e?O.image[a].image:O.image[a];Z0[a]=k8(O,Z0[a])}let _0=Z0[0],F0=W.convert(O.format,O.colorSpace),G0=W.convert(O.type),I0=L(O.internalFormat,F0,G0,O.colorSpace),A=O.isVideoTexture!==!0,Y0=n.__version===void 0||o===!0,U0=u.dataReady,P=y(O,_0);d0(J.TEXTURE_CUBE_MAP,O);let r;if(b0){if(A&&Y0)N.texStorage2D(J.TEXTURE_CUBE_MAP,P,I0,_0.width,_0.height);for(let a=0;a<6;a++){r=Z0[a].mipmaps;for(let R0=0;R0<r.length;R0++){let f0=r[R0];if(O.format!==K9)if(F0!==null)if(A){if(U0)N.compressedTexSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0,0,0,f0.width,f0.height,F0,f0.data)}else N.compressedTexImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0,I0,f0.width,f0.height,0,f0.data);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .setTextureCube()");else if(A){if(U0)N.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0,0,0,f0.width,f0.height,F0,G0,f0.data)}else N.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0,I0,f0.width,f0.height,0,F0,G0,f0.data)}}}else{if(r=O.mipmaps,A&&Y0){if(r.length>0)P++;let a=N8(Z0[0]);N.texStorage2D(J.TEXTURE_CUBE_MAP,P,I0,a.width,a.height)}for(let a=0;a<6;a++)if(e){if(A){if(U0)N.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,0,0,0,Z0[a].width,Z0[a].height,F0,G0,Z0[a].data)}else N.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,0,I0,Z0[a].width,Z0[a].height,0,F0,G0,Z0[a].data);for(let R0=0;R0<r.length;R0++){let t0=r[R0].image[a].image;if(A){if(U0)N.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0+1,0,0,t0.width,t0.height,F0,G0,t0.data)}else N.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0+1,I0,t0.width,t0.height,0,F0,G0,t0.data)}}else{if(A){if(U0)N.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,0,0,0,F0,G0,Z0[a])}else N.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,0,I0,F0,G0,Z0[a]);for(let R0=0;R0<r.length;R0++){let f0=r[R0];if(A){if(U0)N.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0+1,0,0,F0,G0,f0.image[a])}else N.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+a,R0+1,I0,F0,G0,f0.image[a])}}}if(D(O))U(J.TEXTURE_CUBE_MAP);if(n.__version=u.version,O.onUpdate)O.onUpdate(O)}w.__version=O.version}function J0(w,O,j,o,u,n){let O0=W.convert(j.format,j.colorSpace),H0=W.convert(j.type),w0=L(j.internalFormat,O0,H0,j.colorSpace),b0=Q.get(O),e=Q.get(j);if(e.__renderTarget=O,!b0.__hasExternalTextures){let Z0=Math.max(1,O.width>>n),_0=Math.max(1,O.height>>n);if(u===J.TEXTURE_3D||u===J.TEXTURE_2D_ARRAY)N.texImage3D(u,n,w0,Z0,_0,O.depth,0,O0,H0,null);else N.texImage2D(u,n,w0,Z0,_0,0,O0,H0,null)}if(N.bindFramebuffer(J.FRAMEBUFFER,w),C0(O))K.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,o,u,e.__webglTexture,0,B0(O));else if(u===J.TEXTURE_2D||u>=J.TEXTURE_CUBE_MAP_POSITIVE_X&&u<=J.TEXTURE_CUBE_MAP_NEGATIVE_Z)J.framebufferTexture2D(J.FRAMEBUFFER,o,u,e.__webglTexture,n);N.bindFramebuffer(J.FRAMEBUFFER,null)}function Q0(w,O,j){if(J.bindRenderbuffer(J.RENDERBUFFER,w),O.depthBuffer){let o=O.depthTexture,u=o&&o.isDepthTexture?o.type:null,n=B(O.stencilBuffer,u),O0=O.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,H0=B0(O);if(C0(O))K.renderbufferStorageMultisampleEXT(J.RENDERBUFFER,H0,n,O.width,O.height);else if(j)J.renderbufferStorageMultisample(J.RENDERBUFFER,H0,n,O.width,O.height);else J.renderbufferStorage(J.RENDERBUFFER,n,O.width,O.height);J.framebufferRenderbuffer(J.FRAMEBUFFER,O0,J.RENDERBUFFER,w)}else{let o=O.textures;for(let u=0;u<o.length;u++){let n=o[u],O0=W.convert(n.format,n.colorSpace),H0=W.convert(n.type),w0=L(n.internalFormat,O0,H0,n.colorSpace),b0=B0(O);if(j&&C0(O)===!1)J.renderbufferStorageMultisample(J.RENDERBUFFER,b0,w0,O.width,O.height);else if(C0(O))K.renderbufferStorageMultisampleEXT(J.RENDERBUFFER,b0,w0,O.width,O.height);else J.renderbufferStorage(J.RENDERBUFFER,w0,O.width,O.height)}}J.bindRenderbuffer(J.RENDERBUFFER,null)}function E0(w,O){if(O&&O.isWebGLCubeRenderTarget)throw Error("Depth Texture with cube render targets is not supported");if(N.bindFramebuffer(J.FRAMEBUFFER,w),!(O.depthTexture&&O.depthTexture.isDepthTexture))throw Error("renderTarget.depthTexture must be an instance of THREE.DepthTexture");let o=Q.get(O.depthTexture);if(o.__renderTarget=O,!o.__webglTexture||O.depthTexture.image.width!==O.width||O.depthTexture.image.height!==O.height)O.depthTexture.image.width=O.width,O.depthTexture.image.height=O.height,O.depthTexture.needsUpdate=!0;T(O.depthTexture,0);let u=o.__webglTexture,n=B0(O);if(O.depthTexture.format===x6)if(C0(O))K.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,J.DEPTH_ATTACHMENT,J.TEXTURE_2D,u,0,n);else J.framebufferTexture2D(J.FRAMEBUFFER,J.DEPTH_ATTACHMENT,J.TEXTURE_2D,u,0);else if(O.depthTexture.format===sJ)if(C0(O))K.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,J.DEPTH_STENCIL_ATTACHMENT,J.TEXTURE_2D,u,0,n);else J.framebufferTexture2D(J.FRAMEBUFFER,J.DEPTH_STENCIL_ATTACHMENT,J.TEXTURE_2D,u,0);else throw Error("Unknown depthTexture format")}function m0(w){let O=Q.get(w),j=w.isWebGLCubeRenderTarget===!0;if(O.__boundDepthTexture!==w.depthTexture){let o=w.depthTexture;if(O.__depthDisposeCallback)O.__depthDisposeCallback();if(o){let u=()=>{delete O.__boundDepthTexture,delete O.__depthDisposeCallback,o.removeEventListener("dispose",u)};o.addEventListener("dispose",u),O.__depthDisposeCallback=u}O.__boundDepthTexture=o}if(w.depthTexture&&!O.__autoAllocateDepthBuffer){if(j)throw Error("target.depthTexture not supported in Cube render targets");let o=w.texture.mipmaps;if(o&&o.length>0)E0(O.__webglFramebuffer[0],w);else E0(O.__webglFramebuffer,w)}else if(j){O.__webglDepthbuffer=[];for(let o=0;o<6;o++)if(N.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer[o]),O.__webglDepthbuffer[o]===void 0)O.__webglDepthbuffer[o]=J.createRenderbuffer(),Q0(O.__webglDepthbuffer[o],w,!1);else{let u=w.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,n=O.__webglDepthbuffer[o];J.bindRenderbuffer(J.RENDERBUFFER,n),J.framebufferRenderbuffer(J.FRAMEBUFFER,u,J.RENDERBUFFER,n)}}else{let o=w.texture.mipmaps;if(o&&o.length>0)N.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer[0]);else N.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer);if(O.__webglDepthbuffer===void 0)O.__webglDepthbuffer=J.createRenderbuffer(),Q0(O.__webglDepthbuffer,w,!1);else{let u=w.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,n=O.__webglDepthbuffer;J.bindRenderbuffer(J.RENDERBUFFER,n),J.framebufferRenderbuffer(J.FRAMEBUFFER,u,J.RENDERBUFFER,n)}}N.bindFramebuffer(J.FRAMEBUFFER,null)}function z(w,O,j){let o=Q.get(w);if(O!==void 0)J0(o.__webglFramebuffer,w,w.texture,J.COLOR_ATTACHMENT0,J.TEXTURE_2D,0);if(j!==void 0)m0(w)}function x0(w){let O=w.texture,j=Q.get(w),o=Q.get(O);w.addEventListener("dispose",b);let u=w.textures,n=w.isWebGLCubeRenderTarget===!0,O0=u.length>1;if(!O0){if(o.__webglTexture===void 0)o.__webglTexture=J.createTexture();o.__version=O.version,Y.memory.textures++}if(n){j.__webglFramebuffer=[];for(let H0=0;H0<6;H0++)if(O.mipmaps&&O.mipmaps.length>0){j.__webglFramebuffer[H0]=[];for(let w0=0;w0<O.mipmaps.length;w0++)j.__webglFramebuffer[H0][w0]=J.createFramebuffer()}else j.__webglFramebuffer[H0]=J.createFramebuffer()}else{if(O.mipmaps&&O.mipmaps.length>0){j.__webglFramebuffer=[];for(let H0=0;H0<O.mipmaps.length;H0++)j.__webglFramebuffer[H0]=J.createFramebuffer()}else j.__webglFramebuffer=J.createFramebuffer();if(O0)for(let H0=0,w0=u.length;H0<w0;H0++){let b0=Q.get(u[H0]);if(b0.__webglTexture===void 0)b0.__webglTexture=J.createTexture(),Y.memory.textures++}if(w.samples>0&&C0(w)===!1){j.__webglMultisampledFramebuffer=J.createFramebuffer(),j.__webglColorRenderbuffer=[],N.bindFramebuffer(J.FRAMEBUFFER,j.__webglMultisampledFramebuffer);for(let H0=0;H0<u.length;H0++){let w0=u[H0];j.__webglColorRenderbuffer[H0]=J.createRenderbuffer(),J.bindRenderbuffer(J.RENDERBUFFER,j.__webglColorRenderbuffer[H0]);let b0=W.convert(w0.format,w0.colorSpace),e=W.convert(w0.type),Z0=L(w0.internalFormat,b0,e,w0.colorSpace,w.isXRRenderTarget===!0),_0=B0(w);J.renderbufferStorageMultisample(J.RENDERBUFFER,_0,Z0,w.width,w.height),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+H0,J.RENDERBUFFER,j.__webglColorRenderbuffer[H0])}if(J.bindRenderbuffer(J.RENDERBUFFER,null),w.depthBuffer)j.__webglDepthRenderbuffer=J.createRenderbuffer(),Q0(j.__webglDepthRenderbuffer,w,!0);N.bindFramebuffer(J.FRAMEBUFFER,null)}}if(n){N.bindTexture(J.TEXTURE_CUBE_MAP,o.__webglTexture),d0(J.TEXTURE_CUBE_MAP,O);for(let H0=0;H0<6;H0++)if(O.mipmaps&&O.mipmaps.length>0)for(let w0=0;w0<O.mipmaps.length;w0++)J0(j.__webglFramebuffer[H0][w0],w,O,J.COLOR_ATTACHMENT0,J.TEXTURE_CUBE_MAP_POSITIVE_X+H0,w0);else J0(j.__webglFramebuffer[H0],w,O,J.COLOR_ATTACHMENT0,J.TEXTURE_CUBE_MAP_POSITIVE_X+H0,0);if(D(O))U(J.TEXTURE_CUBE_MAP);N.unbindTexture()}else if(O0){for(let H0=0,w0=u.length;H0<w0;H0++){let b0=u[H0],e=Q.get(b0),Z0=J.TEXTURE_2D;if(w.isWebGL3DRenderTarget||w.isWebGLArrayRenderTarget)Z0=w.isWebGL3DRenderTarget?J.TEXTURE_3D:J.TEXTURE_2D_ARRAY;if(N.bindTexture(Z0,e.__webglTexture),d0(Z0,b0),J0(j.__webglFramebuffer,w,b0,J.COLOR_ATTACHMENT0+H0,Z0,0),D(b0))U(Z0)}N.unbindTexture()}else{let H0=J.TEXTURE_2D;if(w.isWebGL3DRenderTarget||w.isWebGLArrayRenderTarget)H0=w.isWebGL3DRenderTarget?J.TEXTURE_3D:J.TEXTURE_2D_ARRAY;if(N.bindTexture(H0,o.__webglTexture),d0(H0,O),O.mipmaps&&O.mipmaps.length>0)for(let w0=0;w0<O.mipmaps.length;w0++)J0(j.__webglFramebuffer[w0],w,O,J.COLOR_ATTACHMENT0,H0,w0);else J0(j.__webglFramebuffer,w,O,J.COLOR_ATTACHMENT0,H0,0);if(D(O))U(H0);N.unbindTexture()}if(w.depthBuffer)m0(w)}function y0(w){let O=w.textures;for(let j=0,o=O.length;j<o;j++){let u=O[j];if(D(u)){let n=C(w),O0=Q.get(u).__webglTexture;N.bindTexture(n,O0),U(n),N.unbindTexture()}}}let S0=[],L0=[];function a0(w){if(w.samples>0){if(C0(w)===!1){let{textures:O,width:j,height:o}=w,u=J.COLOR_BUFFER_BIT,n=w.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,O0=Q.get(w),H0=O.length>1;if(H0)for(let b0=0;b0<O.length;b0++)N.bindFramebuffer(J.FRAMEBUFFER,O0.__webglMultisampledFramebuffer),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+b0,J.RENDERBUFFER,null),N.bindFramebuffer(J.FRAMEBUFFER,O0.__webglFramebuffer),J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0+b0,J.TEXTURE_2D,null,0);N.bindFramebuffer(J.READ_FRAMEBUFFER,O0.__webglMultisampledFramebuffer);let w0=w.texture.mipmaps;if(w0&&w0.length>0)N.bindFramebuffer(J.DRAW_FRAMEBUFFER,O0.__webglFramebuffer[0]);else N.bindFramebuffer(J.DRAW_FRAMEBUFFER,O0.__webglFramebuffer);for(let b0=0;b0<O.length;b0++){if(w.resolveDepthBuffer){if(w.depthBuffer)u|=J.DEPTH_BUFFER_BIT;if(w.stencilBuffer&&w.resolveStencilBuffer)u|=J.STENCIL_BUFFER_BIT}if(H0){J.framebufferRenderbuffer(J.READ_FRAMEBUFFER,J.COLOR_ATTACHMENT0,J.RENDERBUFFER,O0.__webglColorRenderbuffer[b0]);let e=Q.get(O[b0]).__webglTexture;J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0,J.TEXTURE_2D,e,0)}if(J.blitFramebuffer(0,0,j,o,0,0,j,o,u,J.NEAREST),H===!0){if(S0.length=0,L0.length=0,S0.push(J.COLOR_ATTACHMENT0+b0),w.depthBuffer&&w.resolveDepthBuffer===!1)S0.push(n),L0.push(n),J.invalidateFramebuffer(J.DRAW_FRAMEBUFFER,L0);J.invalidateFramebuffer(J.READ_FRAMEBUFFER,S0)}}if(N.bindFramebuffer(J.READ_FRAMEBUFFER,null),N.bindFramebuffer(J.DRAW_FRAMEBUFFER,null),H0)for(let b0=0;b0<O.length;b0++){N.bindFramebuffer(J.FRAMEBUFFER,O0.__webglMultisampledFramebuffer),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+b0,J.RENDERBUFFER,O0.__webglColorRenderbuffer[b0]);let e=Q.get(O[b0]).__webglTexture;N.bindFramebuffer(J.FRAMEBUFFER,O0.__webglFramebuffer),J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0+b0,J.TEXTURE_2D,e,0)}N.bindFramebuffer(J.DRAW_FRAMEBUFFER,O0.__webglMultisampledFramebuffer)}else if(w.depthBuffer&&w.resolveDepthBuffer===!1&&H){let O=w.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT;J.invalidateFramebuffer(J.DRAW_FRAMEBUFFER,[O])}}}function B0(w){return Math.min(Z.maxSamples,w.samples)}function C0(w){let O=Q.get(w);return w.samples>0&&$.has("WEBGL_multisampled_render_to_texture")===!0&&O.__useRenderToTexture!==!1}function X8(w){let O=Y.render.frame;if(G.get(w)!==O)G.set(w,O),w.update()}function k8(w,O){let{colorSpace:j,format:o,type:u}=w;if(w.isCompressedTexture===!0||w.isVideoTexture===!0)return O;if(j!==iJ&&j!==l9)if(o0.getTransfer(j)===Y8){if(o!==K9||u!==S9)console.warn("THREE.WebGLTextures: sRGB encoded textures have to use RGBAFormat and UnsignedByteType.")}else console.error("THREE.WebGLTextures: Unsupported texture color space:",j);return O}function N8(w){if(typeof HTMLImageElement<"u"&&w instanceof HTMLImageElement)X.width=w.naturalWidth||w.width,X.height=w.naturalHeight||w.height;else if(typeof VideoFrame<"u"&&w instanceof VideoFrame)X.width=w.displayWidth,X.height=w.displayHeight;else X.width=w.width,X.height=w.height;return X}this.allocateTextureUnit=i,this.resetTextureUnits=s,this.setTexture2D=T,this.setTexture2DArray=p,this.setTexture3D=$0,this.setTextureCube=c,this.rebindTextures=z,this.setupRenderTarget=x0,this.updateRenderTargetMipmap=y0,this.updateMultisampleRenderTarget=a0,this.setupDepthRenderbuffer=m0,this.setupFrameBufferTexture=J0,this.useMultisampledRTT=C0}function DX(J,$){function N(Q,Z=l9){let W,Y=o0.getTransfer(Z);if(Q===S9)return J.UNSIGNED_BYTE;if(Q===n7)return J.UNSIGNED_SHORT_4_4_4_4;if(Q===s7)return J.UNSIGNED_SHORT_5_5_5_1;if(Q===yQ)return J.UNSIGNED_INT_5_9_9_9_REV;if(Q===jQ)return J.UNSIGNED_INT_10F_11F_11F_REV;if(Q===SQ)return J.BYTE;if(Q===bQ)return J.SHORT;if(Q===cJ)return J.UNSIGNED_SHORT;if(Q===c7)return J.INT;if(Q===VJ)return J.UNSIGNED_INT;if(Q===b9)return J.FLOAT;if(Q===nJ)return J.HALF_FLOAT;if(Q===fQ)return J.ALPHA;if(Q===vQ)return J.RGB;if(Q===K9)return J.RGBA;if(Q===x6)return J.DEPTH_COMPONENT;if(Q===sJ)return J.DEPTH_STENCIL;if(Q===hQ)return J.RED;if(Q===i7)return J.RED_INTEGER;if(Q===xQ)return J.RG;if(Q===o7)return J.RG_INTEGER;if(Q===a7)return J.RGBA_INTEGER;if(Q===g6||Q===p6||Q===d6||Q===l6)if(Y===Y8)if(W=$.get("WEBGL_compressed_texture_s3tc_srgb"),W!==null){if(Q===g6)return W.COMPRESSED_SRGB_S3TC_DXT1_EXT;if(Q===p6)return W.COMPRESSED_SRGB_ALPHA_S3TC_DXT1_EXT;if(Q===d6)return W.COMPRESSED_SRGB_ALPHA_S3TC_DXT3_EXT;if(Q===l6)return W.COMPRESSED_SRGB_ALPHA_S3TC_DXT5_EXT}else return null;else if(W=$.get("WEBGL_compressed_texture_s3tc"),W!==null){if(Q===g6)return W.COMPRESSED_RGB_S3TC_DXT1_EXT;if(Q===p6)return W.COMPRESSED_RGBA_S3TC_DXT1_EXT;if(Q===d6)return W.COMPRESSED_RGBA_S3TC_DXT3_EXT;if(Q===l6)return W.COMPRESSED_RGBA_S3TC_DXT5_EXT}else return null;if(Q===r7||Q===t7||Q===e7||Q===J$)if(W=$.get("WEBGL_compressed_texture_pvrtc"),W!==null){if(Q===r7)return W.COMPRESSED_RGB_PVRTC_4BPPV1_IMG;if(Q===t7)return W.COMPRESSED_RGB_PVRTC_2BPPV1_IMG;if(Q===e7)return W.COMPRESSED_RGBA_PVRTC_4BPPV1_IMG;if(Q===J$)return W.COMPRESSED_RGBA_PVRTC_2BPPV1_IMG}else return null;if(Q===$$||Q===N$||Q===Q$)if(W=$.get("WEBGL_compressed_texture_etc"),W!==null){if(Q===$$||Q===N$)return Y===Y8?W.COMPRESSED_SRGB8_ETC2:W.COMPRESSED_RGB8_ETC2;if(Q===Q$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ETC2_EAC:W.COMPRESSED_RGBA8_ETC2_EAC}else return null;if(Q===Z$||Q===W$||Q===K$||Q===Y$||Q===H$||Q===X$||Q===U$||Q===G$||Q===E$||Q===D$||Q===q$||Q===k$||Q===O$||Q===R$)if(W=$.get("WEBGL_compressed_texture_astc"),W!==null){if(Q===Z$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_4x4_KHR:W.COMPRESSED_RGBA_ASTC_4x4_KHR;if(Q===W$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_5x4_KHR:W.COMPRESSED_RGBA_ASTC_5x4_KHR;if(Q===K$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_5x5_KHR:W.COMPRESSED_RGBA_ASTC_5x5_KHR;if(Q===Y$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_6x5_KHR:W.COMPRESSED_RGBA_ASTC_6x5_KHR;if(Q===H$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_6x6_KHR:W.COMPRESSED_RGBA_ASTC_6x6_KHR;if(Q===X$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_8x5_KHR:W.COMPRESSED_RGBA_ASTC_8x5_KHR;if(Q===U$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_8x6_KHR:W.COMPRESSED_RGBA_ASTC_8x6_KHR;if(Q===G$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_8x8_KHR:W.COMPRESSED_RGBA_ASTC_8x8_KHR;if(Q===E$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_10x5_KHR:W.COMPRESSED_RGBA_ASTC_10x5_KHR;if(Q===D$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_10x6_KHR:W.COMPRESSED_RGBA_ASTC_10x6_KHR;if(Q===q$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_10x8_KHR:W.COMPRESSED_RGBA_ASTC_10x8_KHR;if(Q===k$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_10x10_KHR:W.COMPRESSED_RGBA_ASTC_10x10_KHR;if(Q===O$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_12x10_KHR:W.COMPRESSED_RGBA_ASTC_12x10_KHR;if(Q===R$)return Y===Y8?W.COMPRESSED_SRGB8_ALPHA8_ASTC_12x12_KHR:W.COMPRESSED_RGBA_ASTC_12x12_KHR}else return null;if(Q===F$||Q===M$||Q===_$)if(W=$.get("EXT_texture_compression_bptc"),W!==null){if(Q===F$)return Y===Y8?W.COMPRESSED_SRGB_ALPHA_BPTC_UNORM_EXT:W.COMPRESSED_RGBA_BPTC_UNORM_EXT;if(Q===M$)return W.COMPRESSED_RGB_BPTC_SIGNED_FLOAT_EXT;if(Q===_$)return W.COMPRESSED_RGB_BPTC_UNSIGNED_FLOAT_EXT}else return null;if(Q===L$||Q===V$||Q===w$||Q===z$)if(W=$.get("EXT_texture_compression_rgtc"),W!==null){if(Q===L$)return W.COMPRESSED_RED_RGTC1_EXT;if(Q===V$)return W.COMPRESSED_SIGNED_RED_RGTC1_EXT;if(Q===w$)return W.COMPRESSED_RED_GREEN_RGTC2_EXT;if(Q===z$)return W.COMPRESSED_SIGNED_RED_GREEN_RGTC2_EXT}else return null;if(Q===wJ)return J.UNSIGNED_INT_24_8;return J[Q]!==void 0?J[Q]:null}return{convert:N}}var qX=`
void main() {

	gl_Position = vec4( position, 1.0 );

}`,kX=`
uniform sampler2DArray depthColor;
uniform float depthWidth;
uniform float depthHeight;

void main() {

	vec2 coord = vec2( gl_FragCoord.x / depthWidth, gl_FragCoord.y / depthHeight );

	if ( coord.x >= 1.0 ) {

		gl_FragDepth = texture( depthColor, vec3( coord.x - 1.0, coord.y, 1 ) ).r;

	} else {

		gl_FragDepth = texture( depthColor, vec3( coord.x, coord.y, 0 ) ).r;

	}

}`;class hZ{constructor(){this.texture=null,this.mesh=null,this.depthNear=0,this.depthFar=0}init(J,$){if(this.texture===null){let N=new r6(J.texture);if(J.depthNear!==$.depthNear||J.depthFar!==$.depthFar)this.depthNear=J.depthNear,this.depthFar=J.depthFar;this.texture=N}}getMesh(J){if(this.texture!==null){if(this.mesh===null){let $=J.cameras[0].viewport,N=new H9({vertexShader:qX,fragmentShader:kX,uniforms:{depthColor:{value:this.texture},depthWidth:{value:$.z},depthHeight:{value:$.w}}});this.mesh=new b8(new J6(20,20),N)}}return this.mesh}reset(){this.texture=null,this.mesh=null}getDepthTexture(){return this.texture}}class xZ extends _9{constructor(J,$){super();let N=this,Q=null,Z=1,W=null,Y="local-floor",K=1,H=null,X=null,G=null,E=null,q=null,k=null,M=typeof XRWebGLBinding<"u",_=new hZ,D={},U=$.getContextAttributes(),C=null,L=null,B=[],y=[],I=new p0,b=null,g=new T8;g.viewport=new D8;let V=new T8;V.viewport=new D8;let F=[g,V],S=new o$,s=null,i=null;this.cameraAutoUpdate=!0,this.enabled=!1,this.isPresenting=!1,this.getController=function(t){let K0=B[t];if(K0===void 0)K0=new rJ,B[t]=K0;return K0.getTargetRaySpace()},this.getControllerGrip=function(t){let K0=B[t];if(K0===void 0)K0=new rJ,B[t]=K0;return K0.getGripSpace()},this.getHand=function(t){let K0=B[t];if(K0===void 0)K0=new rJ,B[t]=K0;return K0.getHandSpace()};function m(t){let K0=y.indexOf(t.inputSource);if(K0===-1)return;let x=B[K0];if(x!==void 0)x.update(t.inputSource,t.frame,H||W),x.dispatchEvent({type:t.type,data:t.inputSource})}function T(){Q.removeEventListener("select",m),Q.removeEventListener("selectstart",m),Q.removeEventListener("selectend",m),Q.removeEventListener("squeeze",m),Q.removeEventListener("squeezestart",m),Q.removeEventListener("squeezeend",m),Q.removeEventListener("end",T),Q.removeEventListener("inputsourceschange",p);for(let t=0;t<B.length;t++){let K0=y[t];if(K0===null)continue;y[t]=null,B[t].disconnect(K0)}s=null,i=null,_.reset();for(let t in D)delete D[t];J.setRenderTarget(C),q=null,E=null,G=null,Q=null,L=null,c0.stop(),N.isPresenting=!1,J.setPixelRatio(b),J.setSize(I.width,I.height,!1),N.dispatchEvent({type:"sessionend"})}this.setFramebufferScaleFactor=function(t){if(Z=t,N.isPresenting===!0)console.warn("THREE.WebXRManager: Cannot change framebuffer scale while presenting.")},this.setReferenceSpaceType=function(t){if(Y=t,N.isPresenting===!0)console.warn("THREE.WebXRManager: Cannot change reference space type while presenting.")},this.getReferenceSpace=function(){return H||W},this.setReferenceSpace=function(t){H=t},this.getBaseLayer=function(){return E!==null?E:q},this.getBinding=function(){if(G===null&&M)G=new XRWebGLBinding(Q,$);return G},this.getFrame=function(){return k},this.getSession=function(){return Q},this.setSession=async function(t){if(Q=t,Q!==null){if(C=J.getRenderTarget(),Q.addEventListener("select",m),Q.addEventListener("selectstart",m),Q.addEventListener("selectend",m),Q.addEventListener("squeeze",m),Q.addEventListener("squeezestart",m),Q.addEventListener("squeezeend",m),Q.addEventListener("end",T),Q.addEventListener("inputsourceschange",p),U.xrCompatible!==!0)await $.makeXRCompatible();if(b=J.getPixelRatio(),J.getSize(I),!(M&&("createProjectionLayer"in XRWebGLBinding.prototype))){let x={antialias:U.antialias,alpha:!0,depth:U.depth,stencil:U.stencil,framebufferScaleFactor:Z};q=new XRWebGLLayer(Q,$,x),Q.updateRenderState({baseLayer:q}),J.setPixelRatio(1),J.setSize(q.framebufferWidth,q.framebufferHeight,!1),L=new L9(q.framebufferWidth,q.framebufferHeight,{format:K9,type:S9,colorSpace:J.outputColorSpace,stencilBuffer:U.stencil,resolveDepthBuffer:q.ignoreDepthValues===!1,resolveStencilBuffer:q.ignoreDepthValues===!1})}else{let x=null,J0=null,Q0=null;if(U.depth)Q0=U.stencil?$.DEPTH24_STENCIL8:$.DEPTH_COMPONENT24,x=U.stencil?sJ:x6,J0=U.stencil?wJ:VJ;let E0={colorFormat:$.RGBA8,depthFormat:Q0,scaleFactor:Z};G=this.getBinding(),E=G.createProjectionLayer(E0),Q.updateRenderState({layers:[E]}),J.setPixelRatio(1),J.setSize(E.textureWidth,E.textureHeight,!1),L=new L9(E.textureWidth,E.textureHeight,{format:K9,type:S9,depthTexture:new a6(E.textureWidth,E.textureHeight,J0,void 0,void 0,void 0,void 0,void 0,void 0,x),stencilBuffer:U.stencil,colorSpace:J.outputColorSpace,samples:U.antialias?4:0,resolveDepthBuffer:E.ignoreDepthValues===!1,resolveStencilBuffer:E.ignoreDepthValues===!1})}L.isXRRenderTarget=!0,this.setFoveation(K),H=null,W=await Q.requestReferenceSpace(Y),c0.setContext(Q),c0.start(),N.isPresenting=!0,N.dispatchEvent({type:"sessionstart"})}},this.getEnvironmentBlendMode=function(){if(Q!==null)return Q.environmentBlendMode},this.getDepthTexture=function(){return _.getDepthTexture()};function p(t){for(let K0=0;K0<t.removed.length;K0++){let x=t.removed[K0],J0=y.indexOf(x);if(J0>=0)y[J0]=null,B[J0].disconnect(x)}for(let K0=0;K0<t.added.length;K0++){let x=t.added[K0],J0=y.indexOf(x);if(J0===-1){for(let E0=0;E0<B.length;E0++)if(E0>=y.length){y.push(x),J0=E0;break}else if(y[E0]===null){y[E0]=x,J0=E0;break}if(J0===-1)break}let Q0=B[J0];if(Q0)Q0.connect(x)}}let $0=new h,c=new h;function X0(t,K0,x){$0.setFromMatrixPosition(K0.matrixWorld),c.setFromMatrixPosition(x.matrixWorld);let J0=$0.distanceTo(c),Q0=K0.projectionMatrix.elements,E0=x.projectionMatrix.elements,m0=Q0[14]/(Q0[10]-1),z=Q0[14]/(Q0[10]+1),x0=(Q0[9]+1)/Q0[5],y0=(Q0[9]-1)/Q0[5],S0=(Q0[8]-1)/Q0[0],L0=(E0[8]+1)/E0[0],a0=m0*S0,B0=m0*L0,C0=J0/(-S0+L0),X8=C0*-S0;if(K0.matrixWorld.decompose(t.position,t.quaternion,t.scale),t.translateX(X8),t.translateZ(C0),t.matrixWorld.compose(t.position,t.quaternion,t.scale),t.matrixWorldInverse.copy(t.matrixWorld).invert(),Q0[10]===-1)t.projectionMatrix.copy(K0.projectionMatrix),t.projectionMatrixInverse.copy(K0.projectionMatrixInverse);else{let k8=m0+C0,N8=z+C0,w=a0-X8,O=B0+(J0-X8),j=x0*z/N8*k8,o=y0*z/N8*k8;t.projectionMatrix.makePerspective(w,O,j,o,k8,N8),t.projectionMatrixInverse.copy(t.projectionMatrix).invert()}}function q0(t,K0){if(K0===null)t.matrixWorld.copy(t.matrix);else t.matrixWorld.multiplyMatrices(K0.matrixWorld,t.matrix);t.matrixWorldInverse.copy(t.matrixWorld).invert()}this.updateCamera=function(t){if(Q===null)return;let{near:K0,far:x}=t;if(_.texture!==null){if(_.depthNear>0)K0=_.depthNear;if(_.depthFar>0)x=_.depthFar}if(S.near=V.near=g.near=K0,S.far=V.far=g.far=x,s!==S.near||i!==S.far)Q.updateRenderState({depthNear:S.near,depthFar:S.far}),s=S.near,i=S.far;S.layers.mask=t.layers.mask|6,g.layers.mask=S.layers.mask&3,V.layers.mask=S.layers.mask&5;let J0=t.parent,Q0=S.cameras;q0(S,J0);for(let E0=0;E0<Q0.length;E0++)q0(Q0[E0],J0);if(Q0.length===2)X0(S,g,V);else S.projectionMatrix.copy(g.projectionMatrix);P0(t,S,J0)};function P0(t,K0,x){if(x===null)t.matrix.copy(K0.matrixWorld);else t.matrix.copy(x.matrixWorld),t.matrix.invert(),t.matrix.multiply(K0.matrixWorld);if(t.matrix.decompose(t.position,t.quaternion,t.scale),t.updateMatrixWorld(!0),t.projectionMatrix.copy(K0.projectionMatrix),t.projectionMatrixInverse.copy(K0.projectionMatrixInverse),t.isPerspectiveCamera)t.fov=kJ*2*Math.atan(1/t.projectionMatrix.elements[5]),t.zoom=1}this.getCamera=function(){return S},this.getFoveation=function(){if(E===null&&q===null)return;return K},this.setFoveation=function(t){if(K=t,E!==null)E.fixedFoveation=t;if(q!==null&&q.fixedFoveation!==void 0)q.fixedFoveation=t},this.hasDepthSensing=function(){return _.texture!==null},this.getDepthSensingMesh=function(){return _.getMesh(S)},this.getCameraTexture=function(t){return D[t]};let d0=null;function J8(t,K0){if(X=K0.getViewerPose(H||W),k=K0,X!==null){let x=X.views;if(q!==null)J.setRenderTargetFramebuffer(L,q.framebuffer),J.setRenderTarget(L);let J0=!1;if(x.length!==S.cameras.length)S.cameras.length=0,J0=!0;for(let z=0;z<x.length;z++){let x0=x[z],y0=null;if(q!==null)y0=q.getViewport(x0);else{let L0=G.getViewSubImage(E,x0);if(y0=L0.viewport,z===0)J.setRenderTargetTextures(L,L0.colorTexture,L0.depthStencilTexture),J.setRenderTarget(L)}let S0=F[z];if(S0===void 0)S0=new T8,S0.layers.enable(z),S0.viewport=new D8,F[z]=S0;if(S0.matrix.fromArray(x0.transform.matrix),S0.matrix.decompose(S0.position,S0.quaternion,S0.scale),S0.projectionMatrix.fromArray(x0.projectionMatrix),S0.projectionMatrixInverse.copy(S0.projectionMatrix).invert(),S0.viewport.set(y0.x,y0.y,y0.width,y0.height),z===0)S.matrix.copy(S0.matrix),S.matrix.decompose(S.position,S.quaternion,S.scale);if(J0===!0)S.cameras.push(S0)}let Q0=Q.enabledFeatures;if(Q0&&Q0.includes("depth-sensing")&&Q.depthUsage=="gpu-optimized"&&M){G=N.getBinding();let z=G.getDepthInformation(x[0]);if(z&&z.isValid&&z.texture)_.init(z,Q.renderState)}if(Q0&&Q0.includes("camera-access")&&M){J.state.unbindTexture(),G=N.getBinding();for(let z=0;z<x.length;z++){let x0=x[z].camera;if(x0){let y0=D[x0];if(!y0)y0=new r6,D[x0]=y0;let S0=G.getCameraImage(x0);y0.sourceTexture=S0}}}}for(let x=0;x<B.length;x++){let J0=y[x],Q0=B[x];if(J0!==null&&Q0!==void 0)Q0.update(J0,K0,H||W)}if(d0)d0(t,K0);if(K0.detectedPlanes)N.dispatchEvent({type:"planesdetected",data:K0});k=null}let c0=new IZ;c0.setAnimationLoop(J8),this.setAnimationLoop=function(t){d0=t},this.dispose=function(){}}}var o9=new $9,OX=new U8;function RX(J,$){function N(D,U){if(D.matrixAutoUpdate===!0)D.updateMatrix();U.value.copy(D.matrix)}function Q(D,U){if(U.color.getRGB(D.fogColor.value,f$(J)),U.isFog)D.fogNear.value=U.near,D.fogFar.value=U.far;else if(U.isFogExp2)D.fogDensity.value=U.density}function Z(D,U,C,L,B){if(U.isMeshBasicMaterial)W(D,U);else if(U.isMeshLambertMaterial)W(D,U);else if(U.isMeshToonMaterial)W(D,U),E(D,U);else if(U.isMeshPhongMaterial)W(D,U),G(D,U);else if(U.isMeshStandardMaterial){if(W(D,U),q(D,U),U.isMeshPhysicalMaterial)k(D,U,B)}else if(U.isMeshMatcapMaterial)W(D,U),M(D,U);else if(U.isMeshDepthMaterial)W(D,U);else if(U.isMeshDistanceMaterial)W(D,U),_(D,U);else if(U.isMeshNormalMaterial)W(D,U);else if(U.isLineBasicMaterial){if(Y(D,U),U.isLineDashedMaterial)K(D,U)}else if(U.isPointsMaterial)H(D,U,C,L);else if(U.isSpriteMaterial)X(D,U);else if(U.isShadowMaterial)D.color.value.copy(U.color),D.opacity.value=U.opacity;else if(U.isShaderMaterial)U.uniformsNeedUpdate=!1}function W(D,U){if(D.opacity.value=U.opacity,U.color)D.diffuse.value.copy(U.color);if(U.emissive)D.emissive.value.copy(U.emissive).multiplyScalar(U.emissiveIntensity);if(U.map)D.map.value=U.map,N(U.map,D.mapTransform);if(U.alphaMap)D.alphaMap.value=U.alphaMap,N(U.alphaMap,D.alphaMapTransform);if(U.bumpMap){if(D.bumpMap.value=U.bumpMap,N(U.bumpMap,D.bumpMapTransform),D.bumpScale.value=U.bumpScale,U.side===m8)D.bumpScale.value*=-1}if(U.normalMap){if(D.normalMap.value=U.normalMap,N(U.normalMap,D.normalMapTransform),D.normalScale.value.copy(U.normalScale),U.side===m8)D.normalScale.value.negate()}if(U.displacementMap)D.displacementMap.value=U.displacementMap,N(U.displacementMap,D.displacementMapTransform),D.displacementScale.value=U.displacementScale,D.displacementBias.value=U.displacementBias;if(U.emissiveMap)D.emissiveMap.value=U.emissiveMap,N(U.emissiveMap,D.emissiveMapTransform);if(U.specularMap)D.specularMap.value=U.specularMap,N(U.specularMap,D.specularMapTransform);if(U.alphaTest>0)D.alphaTest.value=U.alphaTest;let C=$.get(U),L=C.envMap,B=C.envMapRotation;if(L){if(D.envMap.value=L,o9.copy(B),o9.x*=-1,o9.y*=-1,o9.z*=-1,L.isCubeTexture&&L.isRenderTargetTexture===!1)o9.y*=-1,o9.z*=-1;D.envMapRotation.value.setFromMatrix4(OX.makeRotationFromEuler(o9)),D.flipEnvMap.value=L.isCubeTexture&&L.isRenderTargetTexture===!1?-1:1,D.reflectivity.value=U.reflectivity,D.ior.value=U.ior,D.refractionRatio.value=U.refractionRatio}if(U.lightMap)D.lightMap.value=U.lightMap,D.lightMapIntensity.value=U.lightMapIntensity,N(U.lightMap,D.lightMapTransform);if(U.aoMap)D.aoMap.value=U.aoMap,D.aoMapIntensity.value=U.aoMapIntensity,N(U.aoMap,D.aoMapTransform)}function Y(D,U){if(D.diffuse.value.copy(U.color),D.opacity.value=U.opacity,U.map)D.map.value=U.map,N(U.map,D.mapTransform)}function K(D,U){D.dashSize.value=U.dashSize,D.totalSize.value=U.dashSize+U.gapSize,D.scale.value=U.scale}function H(D,U,C,L){if(D.diffuse.value.copy(U.color),D.opacity.value=U.opacity,D.size.value=U.size*C,D.scale.value=L*0.5,U.map)D.map.value=U.map,N(U.map,D.uvTransform);if(U.alphaMap)D.alphaMap.value=U.alphaMap,N(U.alphaMap,D.alphaMapTransform);if(U.alphaTest>0)D.alphaTest.value=U.alphaTest}function X(D,U){if(D.diffuse.value.copy(U.color),D.opacity.value=U.opacity,D.rotation.value=U.rotation,U.map)D.map.value=U.map,N(U.map,D.mapTransform);if(U.alphaMap)D.alphaMap.value=U.alphaMap,N(U.alphaMap,D.alphaMapTransform);if(U.alphaTest>0)D.alphaTest.value=U.alphaTest}function G(D,U){D.specular.value.copy(U.specular),D.shininess.value=Math.max(U.shininess,0.0001)}function E(D,U){if(U.gradientMap)D.gradientMap.value=U.gradientMap}function q(D,U){if(D.metalness.value=U.metalness,U.metalnessMap)D.metalnessMap.value=U.metalnessMap,N(U.metalnessMap,D.metalnessMapTransform);if(D.roughness.value=U.roughness,U.roughnessMap)D.roughnessMap.value=U.roughnessMap,N(U.roughnessMap,D.roughnessMapTransform);if(U.envMap)D.envMapIntensity.value=U.envMapIntensity}function k(D,U,C){if(D.ior.value=U.ior,U.sheen>0){if(D.sheenColor.value.copy(U.sheenColor).multiplyScalar(U.sheen),D.sheenRoughness.value=U.sheenRoughness,U.sheenColorMap)D.sheenColorMap.value=U.sheenColorMap,N(U.sheenColorMap,D.sheenColorMapTransform);if(U.sheenRoughnessMap)D.sheenRoughnessMap.value=U.sheenRoughnessMap,N(U.sheenRoughnessMap,D.sheenRoughnessMapTransform)}if(U.clearcoat>0){if(D.clearcoat.value=U.clearcoat,D.clearcoatRoughness.value=U.clearcoatRoughness,U.clearcoatMap)D.clearcoatMap.value=U.clearcoatMap,N(U.clearcoatMap,D.clearcoatMapTransform);if(U.clearcoatRoughnessMap)D.clearcoatRoughnessMap.value=U.clearcoatRoughnessMap,N(U.clearcoatRoughnessMap,D.clearcoatRoughnessMapTransform);if(U.clearcoatNormalMap){if(D.clearcoatNormalMap.value=U.clearcoatNormalMap,N(U.clearcoatNormalMap,D.clearcoatNormalMapTransform),D.clearcoatNormalScale.value.copy(U.clearcoatNormalScale),U.side===m8)D.clearcoatNormalScale.value.negate()}}if(U.dispersion>0)D.dispersion.value=U.dispersion;if(U.iridescence>0){if(D.iridescence.value=U.iridescence,D.iridescenceIOR.value=U.iridescenceIOR,D.iridescenceThicknessMinimum.value=U.iridescenceThicknessRange[0],D.iridescenceThicknessMaximum.value=U.iridescenceThicknessRange[1],U.iridescenceMap)D.iridescenceMap.value=U.iridescenceMap,N(U.iridescenceMap,D.iridescenceMapTransform);if(U.iridescenceThicknessMap)D.iridescenceThicknessMap.value=U.iridescenceThicknessMap,N(U.iridescenceThicknessMap,D.iridescenceThicknessMapTransform)}if(U.transmission>0){if(D.transmission.value=U.transmission,D.transmissionSamplerMap.value=C.texture,D.transmissionSamplerSize.value.set(C.width,C.height),U.transmissionMap)D.transmissionMap.value=U.transmissionMap,N(U.transmissionMap,D.transmissionMapTransform);if(D.thickness.value=U.thickness,U.thicknessMap)D.thicknessMap.value=U.thicknessMap,N(U.thicknessMap,D.thicknessMapTransform);D.attenuationDistance.value=U.attenuationDistance,D.attenuationColor.value.copy(U.attenuationColor)}if(U.anisotropy>0){if(D.anisotropyVector.value.set(U.anisotropy*Math.cos(U.anisotropyRotation),U.anisotropy*Math.sin(U.anisotropyRotation)),U.anisotropyMap)D.anisotropyMap.value=U.anisotropyMap,N(U.anisotropyMap,D.anisotropyMapTransform)}if(D.specularIntensity.value=U.specularIntensity,D.specularColor.value.copy(U.specularColor),U.specularColorMap)D.specularColorMap.value=U.specularColorMap,N(U.specularColorMap,D.specularColorMapTransform);if(U.specularIntensityMap)D.specularIntensityMap.value=U.specularIntensityMap,N(U.specularIntensityMap,D.specularIntensityMapTransform)}function M(D,U){if(U.matcap)D.matcap.value=U.matcap}function _(D,U){let C=$.get(U).light;D.referencePosition.value.setFromMatrixPosition(C.matrixWorld),D.nearDistance.value=C.shadow.camera.near,D.farDistance.value=C.shadow.camera.far}return{refreshFogUniforms:Q,refreshMaterialUniforms:Z}}function FX(J,$,N,Q){let Z={},W={},Y=[],K=J.getParameter(J.MAX_UNIFORM_BUFFER_BINDINGS);function H(C,L){let B=L.program;Q.uniformBlockBinding(C,B)}function X(C,L){let B=Z[C.id];if(B===void 0)M(C),B=G(C),Z[C.id]=B,C.addEventListener("dispose",D);let y=L.program;Q.updateUBOMapping(C,y);let I=$.render.frame;if(W[C.id]!==I)q(C),W[C.id]=I}function G(C){let L=E();C.__bindingPointIndex=L;let B=J.createBuffer(),y=C.__size,I=C.usage;return J.bindBuffer(J.UNIFORM_BUFFER,B),J.bufferData(J.UNIFORM_BUFFER,y,I),J.bindBuffer(J.UNIFORM_BUFFER,null),J.bindBufferBase(J.UNIFORM_BUFFER,L,B),B}function E(){for(let C=0;C<K;C++)if(Y.indexOf(C)===-1)return Y.push(C),C;return console.error("THREE.WebGLRenderer: Maximum number of simultaneously usable uniforms groups reached."),0}function q(C){let L=Z[C.id],B=C.uniforms,y=C.__cache;J.bindBuffer(J.UNIFORM_BUFFER,L);for(let I=0,b=B.length;I<b;I++){let g=Array.isArray(B[I])?B[I]:[B[I]];for(let V=0,F=g.length;V<F;V++){let S=g[V];if(k(S,I,V,y)===!0){let s=S.__offset,i=Array.isArray(S.value)?S.value:[S.value],m=0;for(let T=0;T<i.length;T++){let p=i[T],$0=_(p);if(typeof p==="number"||typeof p==="boolean")S.__data[0]=p,J.bufferSubData(J.UNIFORM_BUFFER,s+m,S.__data);else if(p.isMatrix3)S.__data[0]=p.elements[0],S.__data[1]=p.elements[1],S.__data[2]=p.elements[2],S.__data[3]=0,S.__data[4]=p.elements[3],S.__data[5]=p.elements[4],S.__data[6]=p.elements[5],S.__data[7]=0,S.__data[8]=p.elements[6],S.__data[9]=p.elements[7],S.__data[10]=p.elements[8],S.__data[11]=0;else p.toArray(S.__data,m),m+=$0.storage/Float32Array.BYTES_PER_ELEMENT}J.bufferSubData(J.UNIFORM_BUFFER,s,S.__data)}}}J.bindBuffer(J.UNIFORM_BUFFER,null)}function k(C,L,B,y){let I=C.value,b=L+"_"+B;if(y[b]===void 0){if(typeof I==="number"||typeof I==="boolean")y[b]=I;else y[b]=I.clone();return!0}else{let g=y[b];if(typeof I==="number"||typeof I==="boolean"){if(g!==I)return y[b]=I,!0}else if(g.equals(I)===!1)return g.copy(I),!0}return!1}function M(C){let L=C.uniforms,B=0,y=16;for(let b=0,g=L.length;b<g;b++){let V=Array.isArray(L[b])?L[b]:[L[b]];for(let F=0,S=V.length;F<S;F++){let s=V[F],i=Array.isArray(s.value)?s.value:[s.value];for(let m=0,T=i.length;m<T;m++){let p=i[m],$0=_(p),c=B%y,X0=c%$0.boundary,q0=c+X0;if(B+=X0,q0!==0&&y-q0<$0.storage)B+=y-q0;s.__data=new Float32Array($0.storage/Float32Array.BYTES_PER_ELEMENT),s.__offset=B,B+=$0.storage}}}let I=B%y;if(I>0)B+=y-I;return C.__size=B,C.__cache={},this}function _(C){let L={boundary:0,storage:0};if(typeof C==="number"||typeof C==="boolean")L.boundary=4,L.storage=4;else if(C.isVector2)L.boundary=8,L.storage=8;else if(C.isVector3||C.isColor)L.boundary=16,L.storage=12;else if(C.isVector4)L.boundary=16,L.storage=16;else if(C.isMatrix3)L.boundary=48,L.storage=48;else if(C.isMatrix4)L.boundary=64,L.storage=64;else if(C.isTexture)console.warn("THREE.WebGLRenderer: Texture samplers can not be part of an uniforms group.");else console.warn("THREE.WebGLRenderer: Unsupported uniform value type.",C);return L}function D(C){let L=C.target;L.removeEventListener("dispose",D);let B=Y.indexOf(L.__bindingPointIndex);Y.splice(B,1),J.deleteBuffer(Z[L.id]),delete Z[L.id],delete W[L.id]}function U(){for(let C in Z)J.deleteBuffer(Z[C]);Y=[],Z={},W={}}return{bind:H,update:X,dispose:U}}class HN{constructor(J={}){let{canvas:$=aQ(),context:N=null,depth:Q=!0,stencil:Z=!1,alpha:W=!1,antialias:Y=!1,premultipliedAlpha:K=!0,preserveDrawingBuffer:H=!1,powerPreference:X="default",failIfMajorPerformanceCaveat:G=!1,reversedDepthBuffer:E=!1}=J;this.isWebGLRenderer=!0;let q;if(N!==null){if(typeof WebGLRenderingContext<"u"&&N instanceof WebGLRenderingContext)throw Error("THREE.WebGLRenderer: WebGL 1 is not supported since r163.");q=N.getContextAttributes().alpha}else q=W;let k=new Uint32Array(4),M=new Int32Array(4),_=null,D=null,U=[],C=[];this.domElement=$,this.debug={checkShaderErrors:!0,onShaderError:null},this.autoClear=!0,this.autoClearColor=!0,this.autoClearDepth=!0,this.autoClearStencil=!0,this.sortObjects=!0,this.clippingPlanes=[],this.localClippingEnabled=!1,this.toneMapping=M9,this.toneMappingExposure=1,this.transmissionResolutionScale=1;let L=this,B=!1;this._outputColorSpace=lQ;let y=0,I=0,b=null,g=-1,V=null,F=new D8,S=new D8,s=null,i=new i0(0),m=0,T=$.width,p=$.height,$0=1,c=null,X0=null,q0=new D8(0,0,T,p),P0=new D8(0,0,T,p),d0=!1,J8=new tJ,c0=!1,t=!1,K0=new U8,x=new h,J0=new D8,Q0={background:null,fog:null,environment:null,overrideMaterial:null,isScene:!0},E0=!1;function m0(){return b===null?$0:1}let z=N;function x0(R,f){return $.getContext(R,f)}try{let R={alpha:!0,depth:Q,stencil:Z,antialias:Y,premultipliedAlpha:K,preserveDrawingBuffer:H,powerPreference:X,failIfMajorPerformanceCaveat:G};if("setAttribute"in $)$.setAttribute("data-engine",`three.js r${sN}`);if($.addEventListener("webglcontextlost",Y0,!1),$.addEventListener("webglcontextrestored",U0,!1),$.addEventListener("webglcontextcreationerror",P,!1),z===null){if(z=x0("webgl2",R),z===null)if(x0("webgl2"))throw Error("Error creating WebGL context with your selected attributes.");else throw Error("Error creating WebGL context.")}}catch(R){throw console.error("THREE.WebGLRenderer: "+R.message),R}let y0,S0,L0,a0,B0,C0,X8,k8,N8,w,O,j,o,u,n,O0,H0,w0,b0,e,Z0,_0,F0,G0;function I0(){if(y0=new vY(z),y0.init(),_0=new DX(z,y0),S0=new AY(z,y0,J,_0),L0=new GX(z,y0),S0.reversedDepthBuffer&&E)L0.buffers.depth.setReversed(!0);a0=new gY(z),B0=new tH,C0=new EX(z,y0,L0,B0,S0,_0,a0),X8=new SY(L),k8=new fY(L),N8=new cW(z),F0=new IY(z,N8),w=new hY(z,N8,a0,F0),O=new dY(z,w,N8,a0),b0=new pY(z,S0,C0),O0=new TY(B0),j=new rH(L,X8,k8,y0,S0,F0,O0),o=new RX(L,B0),u=new JX,n=new KX(y0),w0=new CY(L,X8,k8,L0,O,q,K),H0=new XX(L,O,S0),G0=new FX(z,a0,S0,L0),e=new PY(z,y0,a0),Z0=new xY(z,y0,a0),a0.programs=j.programs,L.capabilities=S0,L.extensions=y0,L.properties=B0,L.renderLists=u,L.shadowMap=H0,L.state=L0,L.info=a0}I0();let A=new xZ(L,z);this.xr=A,this.getContext=function(){return z},this.getContextAttributes=function(){return z.getContextAttributes()},this.forceContextLoss=function(){let R=y0.get("WEBGL_lose_context");if(R)R.loseContext()},this.forceContextRestore=function(){let R=y0.get("WEBGL_lose_context");if(R)R.restoreContext()},this.getPixelRatio=function(){return $0},this.setPixelRatio=function(R){if(R===void 0)return;$0=R,this.setSize(T,p,!1)},this.getSize=function(R){return R.set(T,p)},this.setSize=function(R,f,d=!0){if(A.isPresenting){console.warn("THREE.WebGLRenderer: Can't change size while VR device is presenting.");return}if(T=R,p=f,$.width=Math.floor(R*$0),$.height=Math.floor(f*$0),d===!0)$.style.width=R+"px",$.style.height=f+"px";this.setViewport(0,0,R,f)},this.getDrawingBufferSize=function(R){return R.set(T*$0,p*$0).floor()},this.setDrawingBufferSize=function(R,f,d){T=R,p=f,$0=d,$.width=Math.floor(R*d),$.height=Math.floor(f*d),this.setViewport(0,0,R,f)},this.getCurrentViewport=function(R){return R.copy(F)},this.getViewport=function(R){return R.copy(q0)},this.setViewport=function(R,f,d,l){if(R.isVector4)q0.set(R.x,R.y,R.z,R.w);else q0.set(R,f,d,l);L0.viewport(F.copy(q0).multiplyScalar($0).round())},this.getScissor=function(R){return R.copy(P0)},this.setScissor=function(R,f,d,l){if(R.isVector4)P0.set(R.x,R.y,R.z,R.w);else P0.set(R,f,d,l);L0.scissor(S.copy(P0).multiplyScalar($0).round())},this.getScissorTest=function(){return d0},this.setScissorTest=function(R){L0.setScissorTest(d0=R)},this.setOpaqueSort=function(R){c=R},this.setTransparentSort=function(R){X0=R},this.getClearColor=function(R){return R.copy(w0.getClearColor())},this.setClearColor=function(){w0.setClearColor(...arguments)},this.getClearAlpha=function(){return w0.getClearAlpha()},this.setClearAlpha=function(){w0.setClearAlpha(...arguments)},this.clear=function(R=!0,f=!0,d=!0){let l=0;if(R){let v=!1;if(b!==null){let W0=b.texture.format;v=W0===a7||W0===o7||W0===i7}if(v){let W0=b.texture.type,k0=W0===S9||W0===VJ||W0===cJ||W0===wJ||W0===n7||W0===s7,z0=w0.getClearColor(),V0=w0.getClearAlpha(),v0=z0.r,g0=z0.g,A0=z0.b;if(k0)k[0]=v0,k[1]=g0,k[2]=A0,k[3]=V0,z.clearBufferuiv(z.COLOR,0,k);else M[0]=v0,M[1]=g0,M[2]=A0,M[3]=V0,z.clearBufferiv(z.COLOR,0,M)}else l|=z.COLOR_BUFFER_BIT}if(f)l|=z.DEPTH_BUFFER_BIT;if(d)l|=z.STENCIL_BUFFER_BIT,this.state.buffers.stencil.setMask(4294967295);z.clear(l)},this.clearColor=function(){this.clear(!0,!1,!1)},this.clearDepth=function(){this.clear(!1,!0,!1)},this.clearStencil=function(){this.clear(!1,!1,!0)},this.dispose=function(){$.removeEventListener("webglcontextlost",Y0,!1),$.removeEventListener("webglcontextrestored",U0,!1),$.removeEventListener("webglcontextcreationerror",P,!1),w0.dispose(),u.dispose(),n.dispose(),B0.dispose(),X8.dispose(),k8.dispose(),O.dispose(),F0.dispose(),G0.dispose(),j.dispose(),A.dispose(),A.removeEventListener("sessionstart",Q9),A.removeEventListener("sessionend",Z9),j9.stop()};function Y0(R){R.preventDefault(),console.log("THREE.WebGLRenderer: Context Lost."),B=!0}function U0(){console.log("THREE.WebGLRenderer: Context Restored."),B=!1;let R=a0.autoReset,f=H0.enabled,d=H0.autoUpdate,l=H0.needsUpdate,v=H0.type;I0(),a0.autoReset=R,H0.enabled=f,H0.autoUpdate=d,H0.needsUpdate=l,H0.type=v}function P(R){console.error("THREE.WebGLRenderer: A WebGL context could not be created. Reason: ",R.statusMessage)}function r(R){let f=R.target;f.removeEventListener("dispose",r),a(f)}function a(R){R0(R),B0.remove(R)}function R0(R){let f=B0.get(R).programs;if(f!==void 0){if(f.forEach(function(d){j.releaseProgram(d)}),R.isShaderMaterial)j.releaseShaderCache(R)}}this.renderBufferDirect=function(R,f,d,l,v,W0){if(f===null)f=Q0;let k0=v.isMesh&&v.matrixWorld.determinant()<0,z0=nZ(R,f,d,l,v);L0.setMaterial(l,k0);let V0=d.index,v0=1;if(l.wireframe===!0){if(V0=w.getWireframeAttribute(d),V0===void 0)return;v0=2}let g0=d.drawRange,A0=d.attributes.position,s0=g0.start*v0,Q8=(g0.start+g0.count)*v0;if(W0!==null)s0=Math.max(s0,W0.start*v0),Q8=Math.min(Q8,(W0.start+W0.count)*v0);if(V0!==null)s0=Math.max(s0,0),Q8=Math.min(Q8,V0.count);else if(A0!==void 0&&A0!==null)s0=Math.max(s0,0),Q8=Math.min(Q8,A0.count);let O8=Q8-s0;if(O8<0||O8===1/0)return;F0.setup(v,l,z0,d,V0);let H8,K8=e;if(V0!==null)H8=N8.get(V0),K8=Z0,K8.setIndex(H8);if(v.isMesh)if(l.wireframe===!0)L0.setLineWidth(l.wireframeLinewidth*m0()),K8.setMode(z.LINES);else K8.setMode(z.TRIANGLES);else if(v.isLine){let j0=l.linewidth;if(j0===void 0)j0=1;if(L0.setLineWidth(j0*m0()),v.isLineSegments)K8.setMode(z.LINES);else if(v.isLineLoop)K8.setMode(z.LINE_LOOP);else K8.setMode(z.LINE_STRIP)}else if(v.isPoints)K8.setMode(z.POINTS);else if(v.isSprite)K8.setMode(z.TRIANGLES);if(v.isBatchedMesh)if(v._multiDrawInstances!==null)OJ("THREE.WebGLRenderer: renderMultiDrawInstances has been deprecated and will be removed in r184. Append to renderMultiDraw arguments and use indirection."),K8.renderMultiDrawInstances(v._multiDrawStarts,v._multiDrawCounts,v._multiDrawCount,v._multiDrawInstances);else if(!y0.get("WEBGL_multi_draw")){let{_multiDrawStarts:j0,_multiDrawCounts:G8,_multiDrawCount:r0}=v,x8=V0?N8.get(V0).bytesPerElement:1,JJ=B0.get(l).currentProgram.getUniforms();for(let g8=0;g8<r0;g8++)JJ.setValue(z,"_gl_DrawID",g8),K8.render(j0[g8]/x8,G8[g8])}else K8.renderMultiDraw(v._multiDrawStarts,v._multiDrawCounts,v._multiDrawCount);else if(v.isInstancedMesh)K8.renderInstances(s0,O8,v.count);else if(d.isInstancedBufferGeometry){let j0=d._maxInstanceCount!==void 0?d._maxInstanceCount:1/0,G8=Math.min(d.instanceCount,j0);K8.renderInstances(s0,O8,G8)}else K8.render(s0,O8)};function f0(R,f,d){if(R.transparent===!0&&R.side===a8&&R.forceSinglePass===!1)R.side=m8,R.needsUpdate=!0,W6(R,f,d),R.side=RJ,R.needsUpdate=!0,W6(R,f,d),R.side=a8;else W6(R,f,d)}this.compile=function(R,f,d=null){if(d===null)d=R;if(D=n.get(d),D.init(f),C.push(D),d.traverseVisible(function(v){if(v.isLight&&v.layers.test(f.layers)){if(D.pushLight(v),v.castShadow)D.pushShadow(v)}}),R!==d)R.traverseVisible(function(v){if(v.isLight&&v.layers.test(f.layers)){if(D.pushLight(v),v.castShadow)D.pushShadow(v)}});D.setupLights();let l=new Set;return R.traverse(function(v){if(!(v.isMesh||v.isPoints||v.isLine||v.isSprite))return;let W0=v.material;if(W0)if(Array.isArray(W0))for(let k0=0;k0<W0.length;k0++){let z0=W0[k0];f0(z0,d,v),l.add(z0)}else f0(W0,d,v),l.add(W0)}),D=C.pop(),l},this.compileAsync=function(R,f,d=null){let l=this.compile(R,f,d);return new Promise((v)=>{function W0(){if(l.forEach(function(k0){if(B0.get(k0).currentProgram.isReady())l.delete(k0)}),l.size===0){v(R);return}setTimeout(W0,10)}if(y0.get("KHR_parallel_shader_compile")!==null)W0();else setTimeout(W0,10)})};let t0=null;function e0(R){if(t0)t0(R)}function Q9(){j9.stop()}function Z9(){j9.start()}let j9=new IZ;if(j9.setAnimationLoop(e0),typeof self<"u")j9.setContext(self);this.setAnimationLoop=function(R){t0=R,A.setAnimationLoop(R),R===null?j9.stop():j9.start()},A.addEventListener("sessionstart",Q9),A.addEventListener("sessionend",Z9),this.render=function(R,f){if(f!==void 0&&f.isCamera!==!0){console.error("THREE.WebGLRenderer.render: camera is not an instance of THREE.Camera.");return}if(B===!0)return;if(R.matrixWorldAutoUpdate===!0)R.updateMatrixWorld();if(f.parent===null&&f.matrixWorldAutoUpdate===!0)f.updateMatrixWorld();if(A.enabled===!0&&A.isPresenting===!0){if(A.cameraAutoUpdate===!0)A.updateCamera(f);f=A.getCamera()}if(R.isScene===!0)R.onBeforeRender(L,R,f,b);if(D=n.get(R,C.length),D.init(f),C.push(D),K0.multiplyMatrices(f.projectionMatrix,f.matrixWorldInverse),J8.setFromProjectionMatrix(K0,P$,f.reversedDepth),t=this.localClippingEnabled,c0=O0.init(this.clippingPlanes,t),_=u.get(R,U.length),_.init(),U.push(_),A.enabled===!0&&A.isPresenting===!0){let W0=L.xr.getDepthSensingMesh();if(W0!==null)q7(W0,f,-1/0,L.sortObjects)}if(q7(R,f,0,L.sortObjects),_.finish(),L.sortObjects===!0)_.sort(c,X0);if(E0=A.enabled===!1||A.isPresenting===!1||A.hasDepthSensing()===!1,E0)w0.addToRenderList(_,R);if(this.info.render.frame++,c0===!0)O0.beginShadows();let d=D.state.shadowsArray;if(H0.render(d,R,f),c0===!0)O0.endShadows();if(this.info.autoReset===!0)this.info.reset();let{opaque:l,transmissive:v}=_;if(D.setupLights(),f.isArrayCamera){let W0=f.cameras;if(v.length>0)for(let k0=0,z0=W0.length;k0<z0;k0++){let V0=W0[k0];MN(l,v,R,V0)}if(E0)w0.render(R);for(let k0=0,z0=W0.length;k0<z0;k0++){let V0=W0[k0];FN(_,R,V0,V0.viewport)}}else{if(v.length>0)MN(l,v,R,f);if(E0)w0.render(R);FN(_,R,f)}if(b!==null&&I===0)C0.updateMultisampleRenderTarget(b),C0.updateRenderTargetMipmap(b);if(R.isScene===!0)R.onAfterRender(L,R,f);if(F0.resetDefaultState(),g=-1,V=null,C.pop(),C.length>0){if(D=C[C.length-1],c0===!0)O0.setGlobalState(L.clippingPlanes,D.state.camera)}else D=null;if(U.pop(),U.length>0)_=U[U.length-1];else _=null};function q7(R,f,d,l){if(R.visible===!1)return;if(R.layers.test(f.layers)){if(R.isGroup)d=R.renderOrder;else if(R.isLOD){if(R.autoUpdate===!0)R.update(f)}else if(R.isLight){if(D.pushLight(R),R.castShadow)D.pushShadow(R)}else if(R.isSprite){if(!R.frustumCulled||J8.intersectsSprite(R)){if(l)J0.setFromMatrixPosition(R.matrixWorld).applyMatrix4(K0);let k0=O.update(R),z0=R.material;if(z0.visible)_.push(R,k0,z0,d,J0.z,null)}}else if(R.isMesh||R.isLine||R.isPoints){if(!R.frustumCulled||J8.intersectsObject(R)){let k0=O.update(R),z0=R.material;if(l){if(R.boundingSphere!==void 0){if(R.boundingSphere===null)R.computeBoundingSphere();J0.copy(R.boundingSphere.center)}else{if(k0.boundingSphere===null)k0.computeBoundingSphere();J0.copy(k0.boundingSphere.center)}J0.applyMatrix4(R.matrixWorld).applyMatrix4(K0)}if(Array.isArray(z0)){let V0=k0.groups;for(let v0=0,g0=V0.length;v0<g0;v0++){let A0=V0[v0],s0=z0[A0.materialIndex];if(s0&&s0.visible)_.push(R,k0,s0,d,J0.z,A0)}}else if(z0.visible)_.push(R,k0,z0,d,J0.z,null)}}}let W0=R.children;for(let k0=0,z0=W0.length;k0<z0;k0++)q7(W0[k0],f,d,l)}function FN(R,f,d,l){let{opaque:v,transmissive:W0,transparent:k0}=R;if(D.setupLightsView(d),c0===!0)O0.setGlobalState(L.clippingPlanes,d);if(l)L0.viewport(F.copy(l));if(v.length>0)Z6(v,f,d);if(W0.length>0)Z6(W0,f,d);if(k0.length>0)Z6(k0,f,d);L0.buffers.depth.setTest(!0),L0.buffers.depth.setMask(!0),L0.buffers.color.setMask(!0),L0.setPolygonOffset(!1)}function MN(R,f,d,l){if((d.isScene===!0?d.overrideMaterial:null)!==null)return;if(D.state.transmissionRenderTarget[l.id]===void 0)D.state.transmissionRenderTarget[l.id]=new L9(1,1,{generateMipmaps:!0,type:y0.has("EXT_color_buffer_half_float")||y0.has("EXT_color_buffer_float")?nJ:S9,minFilter:LJ,samples:4,stencilBuffer:Z,resolveDepthBuffer:!1,resolveStencilBuffer:!1,colorSpace:o0.workingColorSpace});let W0=D.state.transmissionRenderTarget[l.id],k0=l.viewport||F;W0.setSize(k0.z*L.transmissionResolutionScale,k0.w*L.transmissionResolutionScale);let z0=L.getRenderTarget(),V0=L.getActiveCubeFace(),v0=L.getActiveMipmapLevel();if(L.setRenderTarget(W0),L.getClearColor(i),m=L.getClearAlpha(),m<1)L.setClearColor(16777215,0.5);if(L.clear(),E0)w0.render(d);let g0=L.toneMapping;L.toneMapping=M9;let A0=l.viewport;if(l.viewport!==void 0)l.viewport=void 0;if(D.setupLightsView(l),c0===!0)O0.setGlobalState(L.clippingPlanes,l);if(Z6(R,d,l),C0.updateMultisampleRenderTarget(W0),C0.updateRenderTargetMipmap(W0),y0.has("WEBGL_multisampled_render_to_texture")===!1){let s0=!1;for(let Q8=0,O8=f.length;Q8<O8;Q8++){let H8=f[Q8],K8=H8.object,j0=H8.geometry,G8=H8.material,r0=H8.group;if(G8.side===a8&&K8.layers.test(l.layers)){let x8=G8.side;G8.side=m8,G8.needsUpdate=!0,_N(K8,d,l,j0,G8,r0),G8.side=x8,G8.needsUpdate=!0,s0=!0}}if(s0===!0)C0.updateMultisampleRenderTarget(W0),C0.updateRenderTargetMipmap(W0)}if(L.setRenderTarget(z0,V0,v0),L.setClearColor(i,m),A0!==void 0)l.viewport=A0;L.toneMapping=g0}function Z6(R,f,d){let l=f.isScene===!0?f.overrideMaterial:null;for(let v=0,W0=R.length;v<W0;v++){let k0=R[v],z0=k0.object,V0=k0.geometry,v0=k0.group,g0=k0.material;if(g0.allowOverride===!0&&l!==null)g0=l;if(z0.layers.test(d.layers))_N(z0,f,d,V0,g0,v0)}}function _N(R,f,d,l,v,W0){if(R.onBeforeRender(L,f,d,l,v,W0),R.modelViewMatrix.multiplyMatrices(d.matrixWorldInverse,R.matrixWorld),R.normalMatrix.getNormalMatrix(R.modelViewMatrix),v.onBeforeRender(L,f,d,l,R,W0),v.transparent===!0&&v.side===a8&&v.forceSinglePass===!1)v.side=m8,v.needsUpdate=!0,L.renderBufferDirect(d,f,l,v,R,W0),v.side=RJ,v.needsUpdate=!0,L.renderBufferDirect(d,f,l,v,R,W0),v.side=a8;else L.renderBufferDirect(d,f,l,v,R,W0);R.onAfterRender(L,f,d,l,v,W0)}function W6(R,f,d){if(f.isScene!==!0)f=Q0;let l=B0.get(R),v=D.state.lights,W0=D.state.shadowsArray,k0=v.state.version,z0=j.getParameters(R,v.state,W0,f,d),V0=j.getProgramCacheKey(z0),v0=l.programs;if(l.environment=R.isMeshStandardMaterial?f.environment:null,l.fog=f.fog,l.envMap=(R.isMeshStandardMaterial?k8:X8).get(R.envMap||l.environment),l.envMapRotation=l.environment!==null&&R.envMap===null?f.environmentRotation:R.envMapRotation,v0===void 0)R.addEventListener("dispose",r),v0=new Map,l.programs=v0;let g0=v0.get(V0);if(g0!==void 0){if(l.currentProgram===g0&&l.lightsStateVersion===k0)return VN(R,z0),g0}else z0.uniforms=j.getUniforms(R),R.onBeforeCompile(z0,L),g0=j.acquireProgram(z0,V0),v0.set(V0,g0),l.uniforms=z0.uniforms;let A0=l.uniforms;if(!R.isShaderMaterial&&!R.isRawShaderMaterial||R.clipping===!0)A0.clippingPlanes=O0.uniform;if(VN(R,z0),l.needsLights=iZ(R),l.lightsStateVersion=k0,l.needsLights)A0.ambientLightColor.value=v.state.ambient,A0.lightProbe.value=v.state.probe,A0.directionalLights.value=v.state.directional,A0.directionalLightShadows.value=v.state.directionalShadow,A0.spotLights.value=v.state.spot,A0.spotLightShadows.value=v.state.spotShadow,A0.rectAreaLights.value=v.state.rectArea,A0.ltc_1.value=v.state.rectAreaLTC1,A0.ltc_2.value=v.state.rectAreaLTC2,A0.pointLights.value=v.state.point,A0.pointLightShadows.value=v.state.pointShadow,A0.hemisphereLights.value=v.state.hemi,A0.directionalShadowMap.value=v.state.directionalShadowMap,A0.directionalShadowMatrix.value=v.state.directionalShadowMatrix,A0.spotShadowMap.value=v.state.spotShadowMap,A0.spotLightMatrix.value=v.state.spotLightMatrix,A0.spotLightMap.value=v.state.spotLightMap,A0.pointShadowMap.value=v.state.pointShadowMap,A0.pointShadowMatrix.value=v.state.pointShadowMatrix;return l.currentProgram=g0,l.uniformsList=null,g0}function LN(R){if(R.uniformsList===null){let f=R.currentProgram.getUniforms();R.uniformsList=Q6.seqWithValue(f.seq,R.uniforms)}return R.uniformsList}function VN(R,f){let d=B0.get(R);d.outputColorSpace=f.outputColorSpace,d.batching=f.batching,d.batchingColor=f.batchingColor,d.instancing=f.instancing,d.instancingColor=f.instancingColor,d.instancingMorph=f.instancingMorph,d.skinning=f.skinning,d.morphTargets=f.morphTargets,d.morphNormals=f.morphNormals,d.morphColors=f.morphColors,d.morphTargetsCount=f.morphTargetsCount,d.numClippingPlanes=f.numClippingPlanes,d.numIntersection=f.numClipIntersection,d.vertexAlphas=f.vertexAlphas,d.vertexTangents=f.vertexTangents,d.toneMapping=f.toneMapping}function nZ(R,f,d,l,v){if(f.isScene!==!0)f=Q0;C0.resetTextureUnits();let W0=f.fog,k0=l.isMeshStandardMaterial?f.environment:null,z0=b===null?L.outputColorSpace:b.isXRRenderTarget===!0?b.texture.colorSpace:iJ,V0=(l.isMeshStandardMaterial?k8:X8).get(l.envMap||k0),v0=l.vertexColors===!0&&!!d.attributes.color&&d.attributes.color.itemSize===4,g0=!!d.attributes.tangent&&(!!l.normalMap||l.anisotropy>0),A0=!!d.morphAttributes.position,s0=!!d.morphAttributes.normal,Q8=!!d.morphAttributes.color,O8=M9;if(l.toneMapped){if(b===null||b.isXRRenderTarget===!0)O8=L.toneMapping}let H8=d.morphAttributes.position||d.morphAttributes.normal||d.morphAttributes.color,K8=H8!==void 0?H8.length:0,j0=B0.get(l),G8=D.state.lights;if(c0===!0){if(t===!0||R!==V){let P8=R===V&&l.id===g;O0.setState(l,R,P8)}}let r0=!1;if(l.version===j0.__version){if(j0.needsLights&&j0.lightsStateVersion!==G8.state.version)r0=!0;else if(j0.outputColorSpace!==z0)r0=!0;else if(v.isBatchedMesh&&j0.batching===!1)r0=!0;else if(!v.isBatchedMesh&&j0.batching===!0)r0=!0;else if(v.isBatchedMesh&&j0.batchingColor===!0&&v.colorTexture===null)r0=!0;else if(v.isBatchedMesh&&j0.batchingColor===!1&&v.colorTexture!==null)r0=!0;else if(v.isInstancedMesh&&j0.instancing===!1)r0=!0;else if(!v.isInstancedMesh&&j0.instancing===!0)r0=!0;else if(v.isSkinnedMesh&&j0.skinning===!1)r0=!0;else if(!v.isSkinnedMesh&&j0.skinning===!0)r0=!0;else if(v.isInstancedMesh&&j0.instancingColor===!0&&v.instanceColor===null)r0=!0;else if(v.isInstancedMesh&&j0.instancingColor===!1&&v.instanceColor!==null)r0=!0;else if(v.isInstancedMesh&&j0.instancingMorph===!0&&v.morphTexture===null)r0=!0;else if(v.isInstancedMesh&&j0.instancingMorph===!1&&v.morphTexture!==null)r0=!0;else if(j0.envMap!==V0)r0=!0;else if(l.fog===!0&&j0.fog!==W0)r0=!0;else if(j0.numClippingPlanes!==void 0&&(j0.numClippingPlanes!==O0.numPlanes||j0.numIntersection!==O0.numIntersection))r0=!0;else if(j0.vertexAlphas!==v0)r0=!0;else if(j0.vertexTangents!==g0)r0=!0;else if(j0.morphTargets!==A0)r0=!0;else if(j0.morphNormals!==s0)r0=!0;else if(j0.morphColors!==Q8)r0=!0;else if(j0.toneMapping!==O8)r0=!0;else if(j0.morphTargetsCount!==K8)r0=!0}else r0=!0,j0.__version=l.version;let x8=j0.currentProgram;if(r0===!0)x8=W6(l,f,v);let JJ=!1,g8=!1,bJ=!1,E8=x8.getUniforms(),c8=j0.uniforms;if(L0.useProgram(x8.program))JJ=!0,g8=!0,bJ=!0;if(l.id!==g)g=l.id,g8=!0;if(JJ||V!==R){if(L0.buffers.depth.getReversed()&&R.reversedDepth!==!0)R._reversedDepth=!0,R.updateProjectionMatrix();E8.setValue(z,"projectionMatrix",R.projectionMatrix),E8.setValue(z,"viewMatrix",R.matrixWorldInverse);let j8=E8.map.cameraPosition;if(j8!==void 0)j8.setValue(z,x.setFromMatrixPosition(R.matrixWorld));if(S0.logarithmicDepthBuffer)E8.setValue(z,"logDepthBufFC",2/(Math.log(R.far+1)/Math.LN2));if(l.isMeshPhongMaterial||l.isMeshToonMaterial||l.isMeshLambertMaterial||l.isMeshBasicMaterial||l.isMeshStandardMaterial||l.isShaderMaterial)E8.setValue(z,"isOrthographic",R.isOrthographicCamera===!0);if(V!==R)V=R,g8=!0,bJ=!0}if(v.isSkinnedMesh){E8.setOptional(z,v,"bindMatrix"),E8.setOptional(z,v,"bindMatrixInverse");let P8=v.skeleton;if(P8){if(P8.boneTexture===null)P8.computeBoneTexture();E8.setValue(z,"boneTexture",P8.boneTexture,C0)}}if(v.isBatchedMesh){if(E8.setOptional(z,v,"batchingTexture"),E8.setValue(z,"batchingTexture",v._matricesTexture,C0),E8.setOptional(z,v,"batchingIdTexture"),E8.setValue(z,"batchingIdTexture",v._indirectTexture,C0),E8.setOptional(z,v,"batchingColorTexture"),v._colorsTexture!==null)E8.setValue(z,"batchingColorTexture",v._colorsTexture,C0)}let n8=d.morphAttributes;if(n8.position!==void 0||n8.normal!==void 0||n8.color!==void 0)b0.update(v,d,x8);if(g8||j0.receiveShadow!==v.receiveShadow)j0.receiveShadow=v.receiveShadow,E8.setValue(z,"receiveShadow",v.receiveShadow);if(l.isMeshGouraudMaterial&&l.envMap!==null)c8.envMap.value=V0,c8.flipEnvMap.value=V0.isCubeTexture&&V0.isRenderTargetTexture===!1?-1:1;if(l.isMeshStandardMaterial&&l.envMap===null&&f.environment!==null)c8.envMapIntensity.value=f.environmentIntensity;if(g8){if(E8.setValue(z,"toneMappingExposure",L.toneMappingExposure),j0.needsLights)sZ(c8,bJ);if(W0&&l.fog===!0)o.refreshFogUniforms(c8,W0);o.refreshMaterialUniforms(c8,l,$0,p,D.state.transmissionRenderTarget[R.id]),Q6.upload(z,LN(j0),c8,C0)}if(l.isShaderMaterial&&l.uniformsNeedUpdate===!0)Q6.upload(z,LN(j0),c8,C0),l.uniformsNeedUpdate=!1;if(l.isSpriteMaterial)E8.setValue(z,"center",v.center);if(E8.setValue(z,"modelViewMatrix",v.modelViewMatrix),E8.setValue(z,"normalMatrix",v.normalMatrix),E8.setValue(z,"modelMatrix",v.matrixWorld),l.isShaderMaterial||l.isRawShaderMaterial){let P8=l.uniformsGroups;for(let j8=0,k7=P8.length;j8<k7;j8++){let f9=P8[j8];G0.update(f9,x8),G0.bind(f9,x8)}}return x8}function sZ(R,f){R.ambientLightColor.needsUpdate=f,R.lightProbe.needsUpdate=f,R.directionalLights.needsUpdate=f,R.directionalLightShadows.needsUpdate=f,R.pointLights.needsUpdate=f,R.pointLightShadows.needsUpdate=f,R.spotLights.needsUpdate=f,R.spotLightShadows.needsUpdate=f,R.rectAreaLights.needsUpdate=f,R.hemisphereLights.needsUpdate=f}function iZ(R){return R.isMeshLambertMaterial||R.isMeshToonMaterial||R.isMeshPhongMaterial||R.isMeshStandardMaterial||R.isShadowMaterial||R.isShaderMaterial&&R.lights===!0}this.getActiveCubeFace=function(){return y},this.getActiveMipmapLevel=function(){return I},this.getRenderTarget=function(){return b},this.setRenderTargetTextures=function(R,f,d){let l=B0.get(R);if(l.__autoAllocateDepthBuffer=R.resolveDepthBuffer===!1,l.__autoAllocateDepthBuffer===!1)l.__useRenderToTexture=!1;B0.get(R.texture).__webglTexture=f,B0.get(R.depthTexture).__webglTexture=l.__autoAllocateDepthBuffer?void 0:d,l.__hasExternalTextures=!0},this.setRenderTargetFramebuffer=function(R,f){let d=B0.get(R);d.__webglFramebuffer=f,d.__useDefaultFramebuffer=f===void 0};let oZ=z.createFramebuffer();this.setRenderTarget=function(R,f=0,d=0){b=R,y=f,I=d;let l=!0,v=null,W0=!1,k0=!1;if(R){let V0=B0.get(R);if(V0.__useDefaultFramebuffer!==void 0)L0.bindFramebuffer(z.FRAMEBUFFER,null),l=!1;else if(V0.__webglFramebuffer===void 0)C0.setupRenderTarget(R);else if(V0.__hasExternalTextures)C0.rebindTextures(R,B0.get(R.texture).__webglTexture,B0.get(R.depthTexture).__webglTexture);else if(R.depthBuffer){let A0=R.depthTexture;if(V0.__boundDepthTexture!==A0){if(A0!==null&&B0.has(A0)&&(R.width!==A0.image.width||R.height!==A0.image.height))throw Error("WebGLRenderTarget: Attached DepthTexture is initialized to the incorrect size.");C0.setupDepthRenderbuffer(R)}}let v0=R.texture;if(v0.isData3DTexture||v0.isDataArrayTexture||v0.isCompressedArrayTexture)k0=!0;let g0=B0.get(R).__webglFramebuffer;if(R.isWebGLCubeRenderTarget){if(Array.isArray(g0[f]))v=g0[f][d];else v=g0[f];W0=!0}else if(R.samples>0&&C0.useMultisampledRTT(R)===!1)v=B0.get(R).__webglMultisampledFramebuffer;else if(Array.isArray(g0))v=g0[d];else v=g0;F.copy(R.viewport),S.copy(R.scissor),s=R.scissorTest}else F.copy(q0).multiplyScalar($0).floor(),S.copy(P0).multiplyScalar($0).floor(),s=d0;if(d!==0)v=oZ;if(L0.bindFramebuffer(z.FRAMEBUFFER,v)&&l)L0.drawBuffers(R,v);if(L0.viewport(F),L0.scissor(S),L0.setScissorTest(s),W0){let V0=B0.get(R.texture);z.framebufferTexture2D(z.FRAMEBUFFER,z.COLOR_ATTACHMENT0,z.TEXTURE_CUBE_MAP_POSITIVE_X+f,V0.__webglTexture,d)}else if(k0){let V0=f;for(let v0=0;v0<R.textures.length;v0++){let g0=B0.get(R.textures[v0]);z.framebufferTextureLayer(z.FRAMEBUFFER,z.COLOR_ATTACHMENT0+v0,g0.__webglTexture,d,V0)}}else if(R!==null&&d!==0){let V0=B0.get(R.texture);z.framebufferTexture2D(z.FRAMEBUFFER,z.COLOR_ATTACHMENT0,z.TEXTURE_2D,V0.__webglTexture,d)}g=-1},this.readRenderTargetPixels=function(R,f,d,l,v,W0,k0,z0=0){if(!(R&&R.isWebGLRenderTarget)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not THREE.WebGLRenderTarget.");return}let V0=B0.get(R).__webglFramebuffer;if(R.isWebGLCubeRenderTarget&&k0!==void 0)V0=V0[k0];if(V0){L0.bindFramebuffer(z.FRAMEBUFFER,V0);try{let v0=R.textures[z0],g0=v0.format,A0=v0.type;if(!S0.textureFormatReadable(g0)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not in RGBA or implementation defined format.");return}if(!S0.textureTypeReadable(A0)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not in UnsignedByteType or implementation defined type.");return}if(f>=0&&f<=R.width-l&&(d>=0&&d<=R.height-v)){if(R.textures.length>1)z.readBuffer(z.COLOR_ATTACHMENT0+z0);z.readPixels(f,d,l,v,_0.convert(g0),_0.convert(A0),W0)}}finally{let v0=b!==null?B0.get(b).__webglFramebuffer:null;L0.bindFramebuffer(z.FRAMEBUFFER,v0)}}},this.readRenderTargetPixelsAsync=async function(R,f,d,l,v,W0,k0,z0=0){if(!(R&&R.isWebGLRenderTarget))throw Error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not THREE.WebGLRenderTarget.");let V0=B0.get(R).__webglFramebuffer;if(R.isWebGLCubeRenderTarget&&k0!==void 0)V0=V0[k0];if(V0)if(f>=0&&f<=R.width-l&&(d>=0&&d<=R.height-v)){L0.bindFramebuffer(z.FRAMEBUFFER,V0);let v0=R.textures[z0],g0=v0.format,A0=v0.type;if(!S0.textureFormatReadable(g0))throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: renderTarget is not in RGBA or implementation defined format.");if(!S0.textureTypeReadable(A0))throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: renderTarget is not in UnsignedByteType or implementation defined type.");let s0=z.createBuffer();if(z.bindBuffer(z.PIXEL_PACK_BUFFER,s0),z.bufferData(z.PIXEL_PACK_BUFFER,W0.byteLength,z.STREAM_READ),R.textures.length>1)z.readBuffer(z.COLOR_ATTACHMENT0+z0);z.readPixels(f,d,l,v,_0.convert(g0),_0.convert(A0),0);let Q8=b!==null?B0.get(b).__webglFramebuffer:null;L0.bindFramebuffer(z.FRAMEBUFFER,Q8);let O8=z.fenceSync(z.SYNC_GPU_COMMANDS_COMPLETE,0);return z.flush(),await rQ(z,O8,4),z.bindBuffer(z.PIXEL_PACK_BUFFER,s0),z.getBufferSubData(z.PIXEL_PACK_BUFFER,0,W0),z.deleteBuffer(s0),z.deleteSync(O8),W0}else throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: requested read bounds are out of range.")},this.copyFramebufferToTexture=function(R,f=null,d=0){let l=Math.pow(2,-d),v=Math.floor(R.image.width*l),W0=Math.floor(R.image.height*l),k0=f!==null?f.x:0,z0=f!==null?f.y:0;C0.setTexture2D(R,0),z.copyTexSubImage2D(z.TEXTURE_2D,d,0,0,k0,z0,v,W0),L0.unbindTexture()};let aZ=z.createFramebuffer(),rZ=z.createFramebuffer();if(this.copyTextureToTexture=function(R,f,d=null,l=null,v=0,W0=null){if(W0===null)if(v!==0)OJ("WebGLRenderer: copyTextureToTexture function signature has changed to support src and dst mipmap levels."),W0=v,v=0;else W0=0;let k0,z0,V0,v0,g0,A0,s0,Q8,O8,H8=R.isCompressedTexture?R.mipmaps[W0]:R.image;if(d!==null)k0=d.max.x-d.min.x,z0=d.max.y-d.min.y,V0=d.isBox3?d.max.z-d.min.z:1,v0=d.min.x,g0=d.min.y,A0=d.isBox3?d.min.z:0;else{let n8=Math.pow(2,-v);if(k0=Math.floor(H8.width*n8),z0=Math.floor(H8.height*n8),R.isDataArrayTexture)V0=H8.depth;else if(R.isData3DTexture)V0=Math.floor(H8.depth*n8);else V0=1;v0=0,g0=0,A0=0}if(l!==null)s0=l.x,Q8=l.y,O8=l.z;else s0=0,Q8=0,O8=0;let K8=_0.convert(f.format),j0=_0.convert(f.type),G8;if(f.isData3DTexture)C0.setTexture3D(f,0),G8=z.TEXTURE_3D;else if(f.isDataArrayTexture||f.isCompressedArrayTexture)C0.setTexture2DArray(f,0),G8=z.TEXTURE_2D_ARRAY;else C0.setTexture2D(f,0),G8=z.TEXTURE_2D;z.pixelStorei(z.UNPACK_FLIP_Y_WEBGL,f.flipY),z.pixelStorei(z.UNPACK_PREMULTIPLY_ALPHA_WEBGL,f.premultiplyAlpha),z.pixelStorei(z.UNPACK_ALIGNMENT,f.unpackAlignment);let r0=z.getParameter(z.UNPACK_ROW_LENGTH),x8=z.getParameter(z.UNPACK_IMAGE_HEIGHT),JJ=z.getParameter(z.UNPACK_SKIP_PIXELS),g8=z.getParameter(z.UNPACK_SKIP_ROWS),bJ=z.getParameter(z.UNPACK_SKIP_IMAGES);z.pixelStorei(z.UNPACK_ROW_LENGTH,H8.width),z.pixelStorei(z.UNPACK_IMAGE_HEIGHT,H8.height),z.pixelStorei(z.UNPACK_SKIP_PIXELS,v0),z.pixelStorei(z.UNPACK_SKIP_ROWS,g0),z.pixelStorei(z.UNPACK_SKIP_IMAGES,A0);let E8=R.isDataArrayTexture||R.isData3DTexture,c8=f.isDataArrayTexture||f.isData3DTexture;if(R.isDepthTexture){let n8=B0.get(R),P8=B0.get(f),j8=B0.get(n8.__renderTarget),k7=B0.get(P8.__renderTarget);L0.bindFramebuffer(z.READ_FRAMEBUFFER,j8.__webglFramebuffer),L0.bindFramebuffer(z.DRAW_FRAMEBUFFER,k7.__webglFramebuffer);for(let f9=0;f9<V0;f9++){if(E8)z.framebufferTextureLayer(z.READ_FRAMEBUFFER,z.COLOR_ATTACHMENT0,B0.get(R).__webglTexture,v,A0+f9),z.framebufferTextureLayer(z.DRAW_FRAMEBUFFER,z.COLOR_ATTACHMENT0,B0.get(f).__webglTexture,W0,O8+f9);z.blitFramebuffer(v0,g0,k0,z0,s0,Q8,k0,z0,z.DEPTH_BUFFER_BIT,z.NEAREST)}L0.bindFramebuffer(z.READ_FRAMEBUFFER,null),L0.bindFramebuffer(z.DRAW_FRAMEBUFFER,null)}else if(v!==0||R.isRenderTargetTexture||B0.has(R)){let n8=B0.get(R),P8=B0.get(f);L0.bindFramebuffer(z.READ_FRAMEBUFFER,aZ),L0.bindFramebuffer(z.DRAW_FRAMEBUFFER,rZ);for(let j8=0;j8<V0;j8++){if(E8)z.framebufferTextureLayer(z.READ_FRAMEBUFFER,z.COLOR_ATTACHMENT0,n8.__webglTexture,v,A0+j8);else z.framebufferTexture2D(z.READ_FRAMEBUFFER,z.COLOR_ATTACHMENT0,z.TEXTURE_2D,n8.__webglTexture,v);if(c8)z.framebufferTextureLayer(z.DRAW_FRAMEBUFFER,z.COLOR_ATTACHMENT0,P8.__webglTexture,W0,O8+j8);else z.framebufferTexture2D(z.DRAW_FRAMEBUFFER,z.COLOR_ATTACHMENT0,z.TEXTURE_2D,P8.__webglTexture,W0);if(v!==0)z.blitFramebuffer(v0,g0,k0,z0,s0,Q8,k0,z0,z.COLOR_BUFFER_BIT,z.NEAREST);else if(c8)z.copyTexSubImage3D(G8,W0,s0,Q8,O8+j8,v0,g0,k0,z0);else z.copyTexSubImage2D(G8,W0,s0,Q8,v0,g0,k0,z0)}L0.bindFramebuffer(z.READ_FRAMEBUFFER,null),L0.bindFramebuffer(z.DRAW_FRAMEBUFFER,null)}else if(c8)if(R.isDataTexture||R.isData3DTexture)z.texSubImage3D(G8,W0,s0,Q8,O8,k0,z0,V0,K8,j0,H8.data);else if(f.isCompressedArrayTexture)z.compressedTexSubImage3D(G8,W0,s0,Q8,O8,k0,z0,V0,K8,H8.data);else z.texSubImage3D(G8,W0,s0,Q8,O8,k0,z0,V0,K8,j0,H8);else if(R.isDataTexture)z.texSubImage2D(z.TEXTURE_2D,W0,s0,Q8,k0,z0,K8,j0,H8.data);else if(R.isCompressedTexture)z.compressedTexSubImage2D(z.TEXTURE_2D,W0,s0,Q8,H8.width,H8.height,K8,H8.data);else z.texSubImage2D(z.TEXTURE_2D,W0,s0,Q8,k0,z0,K8,j0,H8);if(z.pixelStorei(z.UNPACK_ROW_LENGTH,r0),z.pixelStorei(z.UNPACK_IMAGE_HEIGHT,x8),z.pixelStorei(z.UNPACK_SKIP_PIXELS,JJ),z.pixelStorei(z.UNPACK_SKIP_ROWS,g8),z.pixelStorei(z.UNPACK_SKIP_IMAGES,bJ),W0===0&&f.generateMipmaps)z.generateMipmap(G8);L0.unbindTexture()},this.initRenderTarget=function(R){if(B0.get(R).__webglFramebuffer===void 0)C0.setupRenderTarget(R)},this.initTexture=function(R){if(R.isCubeTexture)C0.setTextureCube(R,0);else if(R.isData3DTexture)C0.setTexture3D(R,0);else if(R.isDataArrayTexture||R.isCompressedArrayTexture)C0.setTexture2DArray(R,0);else C0.setTexture2D(R,0);L0.unbindTexture()},this.resetState=function(){y=0,I=0,b=null,L0.reset(),F0.reset()},typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("observe",{detail:this}))}get coordinateSystem(){return P$}get outputColorSpace(){return this._outputColorSpace}set outputColorSpace(J){this._outputColorSpace=J;let $=this.getContext();$.drawingBufferColorSpace=o0._getDrawingBufferColorSpace(J),$.unpackColorSpace=o0._getUnpackColorSpace()}}var gZ={type:"change"},UN={type:"start"},dZ={type:"end"},U7=new m9,pZ=new l8,_X=Math.cos(70*T$.DEG2RAD),L8=new h,f8=2*Math.PI,Z8={NONE:-1,ROTATE:0,DOLLY:1,PAN:2,TOUCH_ROTATE:3,TOUCH_PAN:4,TOUCH_DOLLY_PAN:5,TOUCH_DOLLY_ROTATE:6},XN=0.000001;class GN extends W7{constructor(J,$=null){super(J,$);if(this.state=Z8.NONE,this.target=new h,this.cursor=new h,this.minDistance=0,this.maxDistance=1/0,this.minZoom=0,this.maxZoom=1/0,this.minTargetRadius=0,this.maxTargetRadius=1/0,this.minPolarAngle=0,this.maxPolarAngle=Math.PI,this.minAzimuthAngle=-1/0,this.maxAzimuthAngle=1/0,this.enableDamping=!1,this.dampingFactor=0.05,this.enableZoom=!0,this.zoomSpeed=1,this.enableRotate=!0,this.rotateSpeed=1,this.keyRotateSpeed=1,this.enablePan=!0,this.panSpeed=1,this.screenSpacePanning=!0,this.keyPanSpeed=7,this.zoomToCursor=!1,this.autoRotate=!1,this.autoRotateSpeed=2,this.keys={LEFT:"ArrowLeft",UP:"ArrowUp",RIGHT:"ArrowRight",BOTTOM:"ArrowDown"},this.mouseButtons={LEFT:P9.ROTATE,MIDDLE:P9.DOLLY,RIGHT:P9.PAN},this.touches={ONE:A9.ROTATE,TWO:A9.DOLLY_PAN},this.target0=this.target.clone(),this.position0=this.object.position.clone(),this.zoom0=this.object.zoom,this._domElementKeyEvents=null,this._lastPosition=new h,this._lastQuaternion=new N9,this._lastTargetPosition=new h,this._quat=new N9().setFromUnitVectors(J.up,new h(0,1,0)),this._quatInverse=this._quat.clone().invert(),this._spherical=new $6,this._sphericalDelta=new $6,this._scale=1,this._panOffset=new h,this._rotateStart=new p0,this._rotateEnd=new p0,this._rotateDelta=new p0,this._panStart=new p0,this._panEnd=new p0,this._panDelta=new p0,this._dollyStart=new p0,this._dollyEnd=new p0,this._dollyDelta=new p0,this._dollyDirection=new h,this._mouse=new p0,this._performCursorZoom=!1,this._pointers=[],this._pointerPositions={},this._controlActive=!1,this._onPointerMove=VX.bind(this),this._onPointerDown=LX.bind(this),this._onPointerUp=wX.bind(this),this._onContextMenu=TX.bind(this),this._onMouseWheel=CX.bind(this),this._onKeyDown=IX.bind(this),this._onTouchStart=PX.bind(this),this._onTouchMove=AX.bind(this),this._onMouseDown=zX.bind(this),this._onMouseMove=BX.bind(this),this._interceptControlDown=SX.bind(this),this._interceptControlUp=bX.bind(this),this.domElement!==null)this.connect(this.domElement);this.update()}connect(J){super.connect(J),this.domElement.addEventListener("pointerdown",this._onPointerDown),this.domElement.addEventListener("pointercancel",this._onPointerUp),this.domElement.addEventListener("contextmenu",this._onContextMenu),this.domElement.addEventListener("wheel",this._onMouseWheel,{passive:!1}),this.domElement.getRootNode().addEventListener("keydown",this._interceptControlDown,{passive:!0,capture:!0}),this.domElement.style.touchAction="none"}disconnect(){this.domElement.removeEventListener("pointerdown",this._onPointerDown),this.domElement.removeEventListener("pointermove",this._onPointerMove),this.domElement.removeEventListener("pointerup",this._onPointerUp),this.domElement.removeEventListener("pointercancel",this._onPointerUp),this.domElement.removeEventListener("wheel",this._onMouseWheel),this.domElement.removeEventListener("contextmenu",this._onContextMenu),this.stopListenToKeyEvents(),this.domElement.getRootNode().removeEventListener("keydown",this._interceptControlDown,{capture:!0}),this.domElement.style.touchAction="auto"}dispose(){this.disconnect()}getPolarAngle(){return this._spherical.phi}getAzimuthalAngle(){return this._spherical.theta}getDistance(){return this.object.position.distanceTo(this.target)}listenToKeyEvents(J){J.addEventListener("keydown",this._onKeyDown),this._domElementKeyEvents=J}stopListenToKeyEvents(){if(this._domElementKeyEvents!==null)this._domElementKeyEvents.removeEventListener("keydown",this._onKeyDown),this._domElementKeyEvents=null}saveState(){this.target0.copy(this.target),this.position0.copy(this.object.position),this.zoom0=this.object.zoom}reset(){this.target.copy(this.target0),this.object.position.copy(this.position0),this.object.zoom=this.zoom0,this.object.updateProjectionMatrix(),this.dispatchEvent(gZ),this.update(),this.state=Z8.NONE}update(J=null){let $=this.object.position;if(L8.copy($).sub(this.target),L8.applyQuaternion(this._quat),this._spherical.setFromVector3(L8),this.autoRotate&&this.state===Z8.NONE)this._rotateLeft(this._getAutoRotationAngle(J));if(this.enableDamping)this._spherical.theta+=this._sphericalDelta.theta*this.dampingFactor,this._spherical.phi+=this._sphericalDelta.phi*this.dampingFactor;else this._spherical.theta+=this._sphericalDelta.theta,this._spherical.phi+=this._sphericalDelta.phi;let N=this.minAzimuthAngle,Q=this.maxAzimuthAngle;if(isFinite(N)&&isFinite(Q)){if(N<-Math.PI)N+=f8;else if(N>Math.PI)N-=f8;if(Q<-Math.PI)Q+=f8;else if(Q>Math.PI)Q-=f8;if(N<=Q)this._spherical.theta=Math.max(N,Math.min(Q,this._spherical.theta));else this._spherical.theta=this._spherical.theta>(N+Q)/2?Math.max(N,this._spherical.theta):Math.min(Q,this._spherical.theta)}if(this._spherical.phi=Math.max(this.minPolarAngle,Math.min(this.maxPolarAngle,this._spherical.phi)),this._spherical.makeSafe(),this.enableDamping===!0)this.target.addScaledVector(this._panOffset,this.dampingFactor);else this.target.add(this._panOffset);this.target.sub(this.cursor),this.target.clampLength(this.minTargetRadius,this.maxTargetRadius),this.target.add(this.cursor);let Z=!1;if(this.zoomToCursor&&this._performCursorZoom||this.object.isOrthographicCamera)this._spherical.radius=this._clampDistance(this._spherical.radius);else{let W=this._spherical.radius;this._spherical.radius=this._clampDistance(this._spherical.radius*this._scale),Z=W!=this._spherical.radius}if(L8.setFromSpherical(this._spherical),L8.applyQuaternion(this._quatInverse),$.copy(this.target).add(L8),this.object.lookAt(this.target),this.enableDamping===!0)this._sphericalDelta.theta*=1-this.dampingFactor,this._sphericalDelta.phi*=1-this.dampingFactor,this._panOffset.multiplyScalar(1-this.dampingFactor);else this._sphericalDelta.set(0,0,0),this._panOffset.set(0,0,0);if(this.zoomToCursor&&this._performCursorZoom){let W=null;if(this.object.isPerspectiveCamera){let Y=L8.length();W=this._clampDistance(Y*this._scale);let K=Y-W;this.object.position.addScaledVector(this._dollyDirection,K),this.object.updateMatrixWorld(),Z=!!K}else if(this.object.isOrthographicCamera){let Y=new h(this._mouse.x,this._mouse.y,0);Y.unproject(this.object);let K=this.object.zoom;this.object.zoom=Math.max(this.minZoom,Math.min(this.maxZoom,this.object.zoom/this._scale)),this.object.updateProjectionMatrix(),Z=K!==this.object.zoom;let H=new h(this._mouse.x,this._mouse.y,0);H.unproject(this.object),this.object.position.sub(H).add(Y),this.object.updateMatrixWorld(),W=L8.length()}else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - zoom to cursor disabled."),this.zoomToCursor=!1;if(W!==null)if(this.screenSpacePanning)this.target.set(0,0,-1).transformDirection(this.object.matrix).multiplyScalar(W).add(this.object.position);else if(U7.origin.copy(this.object.position),U7.direction.set(0,0,-1).transformDirection(this.object.matrix),Math.abs(this.object.up.dot(U7.direction))<_X)this.object.lookAt(this.target);else pZ.setFromNormalAndCoplanarPoint(this.object.up,this.target),U7.intersectPlane(pZ,this.target)}else if(this.object.isOrthographicCamera){let W=this.object.zoom;if(this.object.zoom=Math.max(this.minZoom,Math.min(this.maxZoom,this.object.zoom/this._scale)),W!==this.object.zoom)this.object.updateProjectionMatrix(),Z=!0}if(this._scale=1,this._performCursorZoom=!1,Z||this._lastPosition.distanceToSquared(this.object.position)>XN||8*(1-this._lastQuaternion.dot(this.object.quaternion))>XN||this._lastTargetPosition.distanceToSquared(this.target)>XN)return this.dispatchEvent(gZ),this._lastPosition.copy(this.object.position),this._lastQuaternion.copy(this.object.quaternion),this._lastTargetPosition.copy(this.target),!0;return!1}_getAutoRotationAngle(J){if(J!==null)return f8/60*this.autoRotateSpeed*J;else return f8/60/60*this.autoRotateSpeed}_getZoomScale(J){let $=Math.abs(J*0.01);return Math.pow(0.95,this.zoomSpeed*$)}_rotateLeft(J){this._sphericalDelta.theta-=J}_rotateUp(J){this._sphericalDelta.phi-=J}_panLeft(J,$){L8.setFromMatrixColumn($,0),L8.multiplyScalar(-J),this._panOffset.add(L8)}_panUp(J,$){if(this.screenSpacePanning===!0)L8.setFromMatrixColumn($,1);else L8.setFromMatrixColumn($,0),L8.crossVectors(this.object.up,L8);L8.multiplyScalar(J),this._panOffset.add(L8)}_pan(J,$){let N=this.domElement;if(this.object.isPerspectiveCamera){let Q=this.object.position;L8.copy(Q).sub(this.target);let Z=L8.length();Z*=Math.tan(this.object.fov/2*Math.PI/180),this._panLeft(2*J*Z/N.clientHeight,this.object.matrix),this._panUp(2*$*Z/N.clientHeight,this.object.matrix)}else if(this.object.isOrthographicCamera)this._panLeft(J*(this.object.right-this.object.left)/this.object.zoom/N.clientWidth,this.object.matrix),this._panUp($*(this.object.top-this.object.bottom)/this.object.zoom/N.clientHeight,this.object.matrix);else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - pan disabled."),this.enablePan=!1}_dollyOut(J){if(this.object.isPerspectiveCamera||this.object.isOrthographicCamera)this._scale/=J;else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - dolly/zoom disabled."),this.enableZoom=!1}_dollyIn(J){if(this.object.isPerspectiveCamera||this.object.isOrthographicCamera)this._scale*=J;else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - dolly/zoom disabled."),this.enableZoom=!1}_updateZoomParameters(J,$){if(!this.zoomToCursor)return;this._performCursorZoom=!0;let N=this.domElement.getBoundingClientRect(),Q=J-N.left,Z=$-N.top,W=N.width,Y=N.height;this._mouse.x=Q/W*2-1,this._mouse.y=-(Z/Y)*2+1,this._dollyDirection.set(this._mouse.x,this._mouse.y,1).unproject(this.object).sub(this.object.position).normalize()}_clampDistance(J){return Math.max(this.minDistance,Math.min(this.maxDistance,J))}_handleMouseDownRotate(J){this._rotateStart.set(J.clientX,J.clientY)}_handleMouseDownDolly(J){this._updateZoomParameters(J.clientX,J.clientX),this._dollyStart.set(J.clientX,J.clientY)}_handleMouseDownPan(J){this._panStart.set(J.clientX,J.clientY)}_handleMouseMoveRotate(J){this._rotateEnd.set(J.clientX,J.clientY),this._rotateDelta.subVectors(this._rotateEnd,this._rotateStart).multiplyScalar(this.rotateSpeed);let $=this.domElement;this._rotateLeft(f8*this._rotateDelta.x/$.clientHeight),this._rotateUp(f8*this._rotateDelta.y/$.clientHeight),this._rotateStart.copy(this._rotateEnd),this.update()}_handleMouseMoveDolly(J){if(this._dollyEnd.set(J.clientX,J.clientY),this._dollyDelta.subVectors(this._dollyEnd,this._dollyStart),this._dollyDelta.y>0)this._dollyOut(this._getZoomScale(this._dollyDelta.y));else if(this._dollyDelta.y<0)this._dollyIn(this._getZoomScale(this._dollyDelta.y));this._dollyStart.copy(this._dollyEnd),this.update()}_handleMouseMovePan(J){this._panEnd.set(J.clientX,J.clientY),this._panDelta.subVectors(this._panEnd,this._panStart).multiplyScalar(this.panSpeed),this._pan(this._panDelta.x,this._panDelta.y),this._panStart.copy(this._panEnd),this.update()}_handleMouseWheel(J){if(this._updateZoomParameters(J.clientX,J.clientY),J.deltaY<0)this._dollyIn(this._getZoomScale(J.deltaY));else if(J.deltaY>0)this._dollyOut(this._getZoomScale(J.deltaY));this.update()}_handleKeyDown(J){let $=!1;switch(J.code){case this.keys.UP:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateUp(f8*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(0,this.keyPanSpeed);$=!0;break;case this.keys.BOTTOM:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateUp(-f8*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(0,-this.keyPanSpeed);$=!0;break;case this.keys.LEFT:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateLeft(f8*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(this.keyPanSpeed,0);$=!0;break;case this.keys.RIGHT:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateLeft(-f8*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(-this.keyPanSpeed,0);$=!0;break}if($)J.preventDefault(),this.update()}_handleTouchStartRotate(J){if(this._pointers.length===1)this._rotateStart.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),N=0.5*(J.pageX+$.x),Q=0.5*(J.pageY+$.y);this._rotateStart.set(N,Q)}}_handleTouchStartPan(J){if(this._pointers.length===1)this._panStart.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),N=0.5*(J.pageX+$.x),Q=0.5*(J.pageY+$.y);this._panStart.set(N,Q)}}_handleTouchStartDolly(J){let $=this._getSecondPointerPosition(J),N=J.pageX-$.x,Q=J.pageY-$.y,Z=Math.sqrt(N*N+Q*Q);this._dollyStart.set(0,Z)}_handleTouchStartDollyPan(J){if(this.enableZoom)this._handleTouchStartDolly(J);if(this.enablePan)this._handleTouchStartPan(J)}_handleTouchStartDollyRotate(J){if(this.enableZoom)this._handleTouchStartDolly(J);if(this.enableRotate)this._handleTouchStartRotate(J)}_handleTouchMoveRotate(J){if(this._pointers.length==1)this._rotateEnd.set(J.pageX,J.pageY);else{let N=this._getSecondPointerPosition(J),Q=0.5*(J.pageX+N.x),Z=0.5*(J.pageY+N.y);this._rotateEnd.set(Q,Z)}this._rotateDelta.subVectors(this._rotateEnd,this._rotateStart).multiplyScalar(this.rotateSpeed);let $=this.domElement;this._rotateLeft(f8*this._rotateDelta.x/$.clientHeight),this._rotateUp(f8*this._rotateDelta.y/$.clientHeight),this._rotateStart.copy(this._rotateEnd)}_handleTouchMovePan(J){if(this._pointers.length===1)this._panEnd.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),N=0.5*(J.pageX+$.x),Q=0.5*(J.pageY+$.y);this._panEnd.set(N,Q)}this._panDelta.subVectors(this._panEnd,this._panStart).multiplyScalar(this.panSpeed),this._pan(this._panDelta.x,this._panDelta.y),this._panStart.copy(this._panEnd)}_handleTouchMoveDolly(J){let $=this._getSecondPointerPosition(J),N=J.pageX-$.x,Q=J.pageY-$.y,Z=Math.sqrt(N*N+Q*Q);this._dollyEnd.set(0,Z),this._dollyDelta.set(0,Math.pow(this._dollyEnd.y/this._dollyStart.y,this.zoomSpeed)),this._dollyOut(this._dollyDelta.y),this._dollyStart.copy(this._dollyEnd);let W=(J.pageX+$.x)*0.5,Y=(J.pageY+$.y)*0.5;this._updateZoomParameters(W,Y)}_handleTouchMoveDollyPan(J){if(this.enableZoom)this._handleTouchMoveDolly(J);if(this.enablePan)this._handleTouchMovePan(J)}_handleTouchMoveDollyRotate(J){if(this.enableZoom)this._handleTouchMoveDolly(J);if(this.enableRotate)this._handleTouchMoveRotate(J)}_addPointer(J){this._pointers.push(J.pointerId)}_removePointer(J){delete this._pointerPositions[J.pointerId];for(let $=0;$<this._pointers.length;$++)if(this._pointers[$]==J.pointerId){this._pointers.splice($,1);return}}_isTrackingPointer(J){for(let $=0;$<this._pointers.length;$++)if(this._pointers[$]==J.pointerId)return!0;return!1}_trackPointer(J){let $=this._pointerPositions[J.pointerId];if($===void 0)$=new p0,this._pointerPositions[J.pointerId]=$;$.set(J.pageX,J.pageY)}_getSecondPointerPosition(J){let $=J.pointerId===this._pointers[0]?this._pointers[1]:this._pointers[0];return this._pointerPositions[$]}_customWheelEvent(J){let $=J.deltaMode,N={clientX:J.clientX,clientY:J.clientY,deltaY:J.deltaY};switch($){case 1:N.deltaY*=16;break;case 2:N.deltaY*=100;break}if(J.ctrlKey&&!this._controlActive)N.deltaY*=10;return N}}function LX(J){if(this.enabled===!1)return;if(this._pointers.length===0)this.domElement.setPointerCapture(J.pointerId),this.domElement.addEventListener("pointermove",this._onPointerMove),this.domElement.addEventListener("pointerup",this._onPointerUp);if(this._isTrackingPointer(J))return;if(this._addPointer(J),J.pointerType==="touch")this._onTouchStart(J);else this._onMouseDown(J)}function VX(J){if(this.enabled===!1)return;if(J.pointerType==="touch")this._onTouchMove(J);else this._onMouseMove(J)}function wX(J){switch(this._removePointer(J),this._pointers.length){case 0:this.domElement.releasePointerCapture(J.pointerId),this.domElement.removeEventListener("pointermove",this._onPointerMove),this.domElement.removeEventListener("pointerup",this._onPointerUp),this.dispatchEvent(dZ),this.state=Z8.NONE;break;case 1:let $=this._pointers[0],N=this._pointerPositions[$];this._onTouchStart({pointerId:$,pageX:N.x,pageY:N.y});break}}function zX(J){let $;switch(J.button){case 0:$=this.mouseButtons.LEFT;break;case 1:$=this.mouseButtons.MIDDLE;break;case 2:$=this.mouseButtons.RIGHT;break;default:$=-1}switch($){case P9.DOLLY:if(this.enableZoom===!1)return;this._handleMouseDownDolly(J),this.state=Z8.DOLLY;break;case P9.ROTATE:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enablePan===!1)return;this._handleMouseDownPan(J),this.state=Z8.PAN}else{if(this.enableRotate===!1)return;this._handleMouseDownRotate(J),this.state=Z8.ROTATE}break;case P9.PAN:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate===!1)return;this._handleMouseDownRotate(J),this.state=Z8.ROTATE}else{if(this.enablePan===!1)return;this._handleMouseDownPan(J),this.state=Z8.PAN}break;default:this.state=Z8.NONE}if(this.state!==Z8.NONE)this.dispatchEvent(UN)}function BX(J){switch(this.state){case Z8.ROTATE:if(this.enableRotate===!1)return;this._handleMouseMoveRotate(J);break;case Z8.DOLLY:if(this.enableZoom===!1)return;this._handleMouseMoveDolly(J);break;case Z8.PAN:if(this.enablePan===!1)return;this._handleMouseMovePan(J);break}}function CX(J){if(this.enabled===!1||this.enableZoom===!1||this.state!==Z8.NONE)return;J.preventDefault(),this.dispatchEvent(UN),this._handleMouseWheel(this._customWheelEvent(J)),this.dispatchEvent(dZ)}function IX(J){if(this.enabled===!1)return;this._handleKeyDown(J)}function PX(J){switch(this._trackPointer(J),this._pointers.length){case 1:switch(this.touches.ONE){case A9.ROTATE:if(this.enableRotate===!1)return;this._handleTouchStartRotate(J),this.state=Z8.TOUCH_ROTATE;break;case A9.PAN:if(this.enablePan===!1)return;this._handleTouchStartPan(J),this.state=Z8.TOUCH_PAN;break;default:this.state=Z8.NONE}break;case 2:switch(this.touches.TWO){case A9.DOLLY_PAN:if(this.enableZoom===!1&&this.enablePan===!1)return;this._handleTouchStartDollyPan(J),this.state=Z8.TOUCH_DOLLY_PAN;break;case A9.DOLLY_ROTATE:if(this.enableZoom===!1&&this.enableRotate===!1)return;this._handleTouchStartDollyRotate(J),this.state=Z8.TOUCH_DOLLY_ROTATE;break;default:this.state=Z8.NONE}break;default:this.state=Z8.NONE}if(this.state!==Z8.NONE)this.dispatchEvent(UN)}function AX(J){switch(this._trackPointer(J),this.state){case Z8.TOUCH_ROTATE:if(this.enableRotate===!1)return;this._handleTouchMoveRotate(J),this.update();break;case Z8.TOUCH_PAN:if(this.enablePan===!1)return;this._handleTouchMovePan(J),this.update();break;case Z8.TOUCH_DOLLY_PAN:if(this.enableZoom===!1&&this.enablePan===!1)return;this._handleTouchMoveDollyPan(J),this.update();break;case Z8.TOUCH_DOLLY_ROTATE:if(this.enableZoom===!1&&this.enableRotate===!1)return;this._handleTouchMoveDollyRotate(J),this.update();break;default:this.state=Z8.NONE}}function TX(J){if(this.enabled===!1)return;J.preventDefault()}function SX(J){if(J.key==="Control")this._controlActive=!0,this.domElement.getRootNode().addEventListener("keyup",this._interceptControlUp,{passive:!0,capture:!0})}function bX(J){if(J.key==="Control")this._controlActive=!1,this.domElement.getRootNode().removeEventListener("keyup",this._interceptControlUp,{passive:!0,capture:!0})}import{jsx as q8,jsxs as U9,Fragment as yX}from"react/jsx-runtime";var lZ=(J,$)=>`/api/apps/design/api${J}${J.includes("?")?"&":"?"}project_id=${encodeURIComponent($)}`;function DN({projectId:J,artifactId:$,designId:N,revisionId:Q,focus:Z,issue:W,onSelect:Y,onCapture:K}){let H=EN(null),X=EN(null),[G,E]=v8([]),[q,k]=v8(""),[M,_]=v8(new Set),[D,U]=v8(0.35),[C,L]=v8(!1),[B,y]=v8(!0),[I,b]=v8(""),[g,V]=v8(0),[F,S]=v8([-100,100]),[s,i]=v8(!1),[m,T]=v8("point"),[p,$0]=v8([]),[c,X0]=v8(""),[q0,P0]=v8(""),d0=EN({measuring:s,measureMode:m});d0.current={measuring:s,measureMode:m};let J8=(x,J0)=>{k(J0||x),Y?.(x)};G7(()=>{if(!$){E([]);return}let x=!1,J0;return P0(""),$0([]),X0(""),_(new Set),fetch(lZ(`/artifacts/${$}/content`,J),{credentials:"same-origin"}).then((Q0)=>{if(!Q0.ok)throw Error("Unable to load assembly preview");return Q0.json()}).then((Q0)=>{if(x||!H.current)return;let E0=new HN({antialias:!0,alpha:!0,preserveDrawingBuffer:!0});E0.setPixelRatio(Math.min(devicePixelRatio,2)),E0.localClippingEnabled=!0;let m0=E0.domElement;m0.className="design-canvas",H.current.appendChild(m0);let z=new i6,x0=new T8(35,1,0.01,1e5),y0=new GN(x0,m0);y0.enableDamping=!0,z.add(new $7(16777215,6583435,2));let S0=new Q7(16777215,2);S0.position.set(1,-2,4),z.add(S0);let L0=new R9;z.add(L0);let a0=[],B0=Q0.parts?.length?Q0.parts:[{...Q0,id:"output",name:"Output"}];for(let[e,Z0]of[...B0,...Q0.keepouts||[]].entries()){let _0=new u8;if(_0.setAttribute("position",new B8(Z0.vertices,3)),_0.setIndex(Z0.triangles),Z0.normals?.length===Z0.vertices.length)_0.setAttribute("normal",new B8(Z0.normals,3));else _0.computeVertexNormals();let F0=e>=B0.length,G0=new t6({color:Z0.color||"#64748b",side:a8,roughness:0.6,transparent:F0,opacity:F0?0.18:1,depthWrite:!F0}),I0=new b8(_0,G0);I0.userData={part:Z0,id:Z0.id||`part_${e}`,instance:Z0.instance_id||Z0.id||"output",keepout:F0},I0.visible=Z0.visible!==!1,L0.add(I0),a0.push(I0)}let C0=new Y9().setFromObject(L0),X8=C0.getCenter(new h),k8=C0.getSize(new h),N8=Math.max(k8.length(),1);y0.target.copy(X8),x0.up.set(0,0,1),x0.position.copy(X8).add(new h(1,-1,1).multiplyScalar(N8*1.6)),x0.near=Math.max(N8/1e4,0.001),x0.far=N8*100,x0.updateProjectionMatrix(),y0.update();let w=new R9;z.add(w);let O=new l8;X.current={renderer:E0,scene:z,camera:x0,controls:y0,objects:a0,box:C0,center:X8,diagonal:N8,clip:O,helpers:w},_(new Set(B0.filter((e)=>e.visible===!1).map((e)=>e.id||""))),E(B0),S([C0.min.z,C0.max.z]),V(X8.z);function j(){if(!H.current)return;let{width:e,height:Z0}=H.current.getBoundingClientRect();E0.setSize(e,Math.max(Z0,1),!1),x0.aspect=e/Math.max(Z0,1),x0.updateProjectionMatrix()}j();let o=new ResizeObserver(j);o.observe(H.current);let u=new Z7,n=[0,0];function O0(e){n=[e.clientX,e.clientY]}function H0(e){if(Math.hypot(e.clientX-n[0],e.clientY-n[1])>4)return;let Z0=m0.getBoundingClientRect();u.setFromCamera(new p0((e.clientX-Z0.left)/Z0.width*2-1,-(e.clientY-Z0.top)/Z0.height*2+1),x0);let _0=u.intersectObjects(a0.filter((I0)=>I0.visible&&!I0.userData.keepout)).filter((I0)=>!E0.clippingPlanes.some((A)=>A.distanceToPoint(I0.point)<0));if(!_0.length)return;let F0=_0[0],G0=F0.object;if(J8(G0.userData.instance,G0.userData.id),d0.current.measuring){let A=G0.userData.part.faceGroups?.find((P)=>(F0.faceIndex??0)*3>=P.start&&(F0.faceIndex??0)*3<P.start+P.count),Y0=d0.current.measureMode==="face"?A?.faceId:void 0;if(d0.current.measureMode==="face"&&Y0===void 0){P0("Exact face selection is unavailable for this nested preview; use instance distance or point measurement.");return}let U0={visual:G0.userData.id,instance:G0.userData.instance,point:F0.point.toArray(),face:Y0};$0((P)=>P.length>=2?[U0]:[...P,U0])}}m0.addEventListener("pointerdown",O0),m0.addEventListener("pointerup",H0);let w0=0;function b0(){w0=requestAnimationFrame(b0),y0.update(),E0.render(z,x0)}b0(),J0=()=>{cancelAnimationFrame(w0),o.disconnect(),m0.removeEventListener("pointerdown",O0),m0.removeEventListener("pointerup",H0),y0.dispose();for(let e of a0)e.geometry.dispose(),e.material.dispose();if(E0.dispose(),m0.remove(),X.current?.renderer===E0)X.current=null}}).catch((Q0)=>P0(Q0.message)),()=>{x=!0,J0?.()}},[$,J]),G7(()=>{let x=X.current;if(!x)return;for(let J0 of x.objects){J0.visible=J0.userData.keepout?B:!M.has(J0.userData.id)&&!M.has(J0.userData.instance);let Q0=J0.material,E0=J0.userData.instance===q||J0.userData.id===q;Q0.emissive.set(E0?"#5f3200":"#000000"),Q0.transparent=J0.userData.keepout||C,Q0.opacity=J0.userData.keepout?0.18:C?D:1,Q0.depthWrite=!Q0.transparent}if(I){let J0=Number(I);x.clip.normal.set(J0===0?1:0,J0===1?1:0,J0===2?1:0),x.clip.constant=-g,x.renderer.clippingPlanes=[x.clip]}else x.renderer.clippingPlanes=[]},[G,M,q,C,D,B,I,g]);function c0(x){let J0=X.current;if(!J0)return;let Q0=new Y9;for(let x0 of J0.objects)if(!x0.userData.keepout&&(!x?.length||x.includes(x0.userData.instance)||x.includes(x0.userData.id)))Q0.expandByObject(x0);if(Q0.isEmpty())return;let E0=Q0.getCenter(new h),m0=Math.max(Q0.getSize(new h).length(),1),z=J0.camera.position.clone().sub(J0.controls.target).normalize();J0.controls.target.copy(E0),J0.camera.position.copy(E0).addScaledVector(z,m0*1.6),J0.controls.update()}G7(()=>{if(Z?.length)_(new Set),k(Z[0]),c0(Z);let x=X.current;if(!x)return;x.helpers.clear();let J0=W?.details?.pairs||[W?.details];for(let Q0 of J0){if(!Q0?.closest_points)continue;let E0=Q0.closest_points.map((z)=>new h(...z)),m0=new u8().setFromPoints(E0);x.helpers.add(new o6(m0,new eJ({color:"#ef4444",depthTest:!1})));for(let z of E0){let x0=new b8(new IJ(x.diagonal/200,10,8),new u9({color:"#ef4444",depthTest:!1}));x0.position.copy(z),x.helpers.add(x0)}}for(let Q0 of[...W?.details?.regions||[],...W?.details?.feature_regions||[],...W?.details?.overhang_regions||[]].slice(0,512)){let E0=new b8(new IJ(x.diagonal/150,8,6),new u9({color:"#ef4444",depthTest:!1}));E0.position.fromArray(Q0.point),x.helpers.add(E0)}},[Z,W,G]),G7(()=>{if(p.length!==2)return;if(m==="point"){let Q0=Math.hypot(...p[0].point.map((E0,m0)=>E0-p[1].point[m0]));X0(`Preview mesh points: ${Q0.toFixed(3)} mm (approximate)`);return}if(!N||!Q)return;X0("Measuring exact CAD…");let x=!1,J0={a:p[0].instance,b:p[1].instance,visual_a:p[0].visual,visual_b:p[1].visual};if(m==="face")J0.face_a=p[0].face,J0.face_b=p[1].face;return fetch(lZ("/measure",J),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({design_id:N,revision_id:Q,query:J0})}).then((Q0)=>Q0.json()).then((Q0)=>{if(!x)X0(Q0.error||Q0.measurement?.message||"No measurement result")}).catch((Q0)=>{if(!x)X0(Q0.message)}),()=>{x=!0}},[p,m,N,Q,J]);function t(x){let J0=X.current;if(!J0)return;let Q0={front:[0,-1,0],back:[0,1,0],top:[0,0,1],side:[1,0,0],iso:[1,-1,1]};if(J0.camera.up.set(0,0,1),x==="top")J0.camera.up.set(0,1,0);J0.camera.position.copy(J0.controls.target).add(new h(...Q0[x]).normalize().multiplyScalar(J0.diagonal*1.6)),J0.controls.update()}let K0={fontSize:11,padding:"4px 7px",border:"1px solid #475569",borderRadius:4,background:"#111827",color:"#e2e8f0",cursor:"pointer"};return U9("div",{style:{position:"absolute",inset:0,display:"flex",flexDirection:"column"},children:[U9("div",{style:{display:"flex",flexWrap:"wrap",gap:4,padding:8,zIndex:2,background:"#0b101acc"},children:[["front","top","side","iso"].map((x)=>q8("button",{style:K0,onClick:()=>t(x),children:x},x)),q8("button",{style:K0,onClick:()=>c0(),children:"Fit all"}),q8("button",{style:K0,disabled:!q,onClick:()=>c0([q]),children:"Fit selection"}),q8("button",{style:K0,onClick:()=>{_(new Set),L(!1)},children:"Show all"}),q8("button",{style:K0,onClick:()=>L(!C),children:"Transparency"}),C&&q8("input",{"aria-label":"Opacity",type:"range",min:".1",max:"1",step:".05",value:D,onChange:(x)=>U(Number(x.target.value))}),U9("select",{"aria-label":"Section plane",style:K0,value:I,onChange:(x)=>{let J0=x.target.value;if(b(J0),J0){let Q0=X.current,E0=Number(J0);if(Q0)S([Q0.box.min.getComponent(E0),Q0.box.max.getComponent(E0)]),V(Q0.center.getComponent(E0))}},children:[q8("option",{value:"",children:"No section"}),q8("option",{value:"0",children:"Section X"}),q8("option",{value:"1",children:"Section Y"}),q8("option",{value:"2",children:"Section Z"})]}),I&&U9(yX,{children:[q8("input",{"aria-label":"Section position",type:"range",min:F[0],max:F[1],step:".1",value:g,onChange:(x)=>V(Number(x.target.value))}),U9("span",{children:[g.toFixed(1)," mm"]})]}),U9("label",{style:{fontSize:11},children:[q8("input",{type:"checkbox",checked:B,onChange:(x)=>y(x.target.checked)}),"Keepouts"]}),q8("button",{style:K0,onClick:()=>{i(!s),$0([]),X0("")},children:s?"Stop measuring":"Measure"}),s&&U9("select",{"aria-label":"Measurement mode",style:K0,value:m,onChange:(x)=>{T(x.target.value),$0([])},children:[q8("option",{value:"point",children:"Mesh points (approximate)"}),q8("option",{value:"instance",children:"Exact instance distance"}),q8("option",{value:"face",children:"Exact CAD faces"})]}),q8("button",{style:K0,onClick:()=>{let x=X.current;if(!x)return;x.renderer.render(x.scene,x.camera);let J0=x.renderer.domElement.toDataURL("image/png");K?.(J0);let Q0=document.createElement("a");Q0.download=`design-${N}-r${Q}-${Date.now()}.png`,Q0.href=J0,Q0.click()},children:"Save inspection view"})]}),U9("div",{style:{position:"relative",flex:1,minHeight:180},children:[q8("div",{ref:H,style:{position:"absolute",inset:0}}),G.length>0&&U9("div",{style:{position:"absolute",top:8,left:8,maxHeight:"65%",width:200,overflow:"auto",background:"#0b101add",borderRadius:6,padding:8,fontSize:11},children:[q8("strong",{children:"Assembly instances"}),G.map((x,J0)=>U9("div",{style:{display:"flex",gap:5,marginTop:5,alignItems:"center"},children:[q8("input",{"aria-label":`Show ${x.name}`,type:"checkbox",checked:!M.has(x.id||"")&&!M.has(x.instance_id||x.id||""),onChange:(Q0)=>_((E0)=>{let m0=new Set(E0);return Q0.target.checked?m0.delete(x.id||""):m0.add(x.id||""),m0})}),q8("button",{style:{...K0,flex:1,textAlign:"left",borderColor:q===x.id||q===x.instance_id?"#f97316":"transparent",overflow:"hidden",textOverflow:"ellipsis"},title:x.id,onClick:()=>J8(x.instance_id||x.id||"",x.id),children:x.name||x.id}),q8("button",{title:"Isolate",style:K0,onClick:()=>{_(new Set(G.filter((Q0)=>Q0.id!==x.id).map((Q0)=>Q0.id||""))),J8(x.instance_id||x.id||"",x.id),c0([x.id||""])},children:"◎"})]},x.id||J0))]})]}),(q0||c||s)&&q8("div",{style:{padding:8,fontSize:12,background:"#0b101add",zIndex:2},children:q0||c||`Select two ${m==="face"?"faces":m==="instance"?"instances":"mesh points"} (${p.length}/2)`})]})}import{useCallback as jX,useEffect as uZ,useState as h8}from"react";import{jsx as M0,jsxs as T0,Fragment as E7}from"react/jsx-runtime";var t9=(J,$)=>`${(J||0).toFixed(2)} ${$}`,fX=["awaiting_approval","approved"],mZ={id:"",label:"",first_name:"",last_name:"",line1:"",city:"",zip:"",country:"FR",phone:"",email:""};function qN({request:J,designId:$,revisionId:N}){let[Q,Z]=h8([]),[W,Y]=h8([]),[K,H]=h8(null),[X,G]=h8([]),[E,q]=h8([]),[k,M]=h8("1"),[_,D]=h8(""),[U,C]=h8(""),[L,B]=h8(""),[y,I]=h8("quotes"),b=jX(async()=>{try{let[T,p,$0]=await Promise.all([J("/fabrication/providers"),J("/fabrication/settings"),J(`/fabrication/orders${$?`?design_id=${$}`:""}`)]);if(Z(T.providers||[]),Y(T.supported||[]),H(p),q($0.orders||[]),D((c)=>c||p.default_address_id||p.addresses?.[0]?.id||""),$)G((await J(`/fabrication/quotes?design_id=${$}&limit=5`)).quotes||[])}catch(T){B(T.message)}},[J,$]);uZ(()=>{b()},[b]);async function g(T,p){C(T),B("");try{await p(),await b()}catch($0){B($0.message)}finally{C("")}}let V=()=>g("Collecting quotes",()=>J("/fabrication/quotes",{method:"POST",body:JSON.stringify({design_id:$,revision_id:N,quantity:Number(k)||1})})),F=(T,p)=>g("Preparing order",async()=>{await J("/fabrication/orders",{method:"POST",body:JSON.stringify({quote_id:T.id,option_id:p.id,address_id:_})}),I("orders")}),S=(T,p,$0)=>g(p,()=>J(`/fabrication/orders/${T.id}/${p}`,{method:"POST",body:JSON.stringify($0||{})})),s=(T)=>g("Saving settings",()=>J("/fabrication/settings",{method:"PUT",body:JSON.stringify(T)})),i=X[0],m=E.filter((T)=>T.status==="awaiting_approval").length;return T0("div",{className:"fab",children:[T0("div",{className:"fab-bar",children:[M0("button",{className:y==="quotes"?"tab-active":"tab",onClick:()=>I("quotes"),children:"Quotes"}),T0("button",{className:y==="orders"?"tab-active":"tab",onClick:()=>I("orders"),children:["Orders",m?` (${m} to approve)`:""]}),M0("button",{className:y==="settings"?"tab-active":"tab",onClick:()=>I("settings"),children:"Policy and addresses"}),M0("span",{className:"flex-1"}),U&&T0("span",{className:"text-[11px] text-text-muted",children:[U,"..."]})]}),L&&M0("div",{className:"fab-error",children:L}),T0("div",{className:"fab-body",children:[y==="quotes"&&T0(E7,{children:[T0("section",{className:"fab-card",children:[M0("h3",{className:"fab-h",children:"Print services"}),Q.length?M0("div",{className:"fab-chips",children:Q.map((T)=>T0("span",{className:"fab-chip","data-ok":T.supported&&T.allowed_by_policy,children:[T.capabilities?.name||T.slug," · #",T.connection_id,!T.supported?" · no adapter":!T.allowed_by_policy?" · not allowed":""]},T.connection_id))}):M0("p",{className:"fab-muted",children:`No print service is bound. Bind Craftcloud, Slant 3D, Shapeways, Treatstock or Sculpteo connections to the "3D printing services" role in this app's settings.`}),T0("div",{className:"fab-row",children:[T0("label",{className:"fab-muted",children:["Quantity per part",M0("input",{className:"field fab-num",type:"number",min:1,value:k,onChange:(T)=>M(T.target.value)})]}),M0("button",{className:"design-button design-button-primary",disabled:!!U||!$||!Q.length,onClick:V,children:"Get quotes for this revision"})]}),M0("p",{className:"fab-muted",children:"Builds STL files for the printed parts and sends only those files to the services. Nothing is ordered."})]}),i&&T0("section",{className:"fab-card",children:[T0("h3",{className:"fab-h",children:["Quote #",i.id," · revision ",i.revision_id,i.expires_at?` · valid until ${new Date(i.expires_at).toLocaleString()}`:""]}),i.errors?.map((T,p)=>T0("p",{className:"fab-warn",children:[T.provider," #",T.connection_id,": ",T.error]},p)),i.options?.length?T0(E7,{children:[M0("div",{className:"fab-row",children:T0("label",{className:"fab-muted",children:["Ship to",T0("select",{className:"field",value:_,onChange:(T)=>D(T.target.value),children:[M0("option",{value:"",children:"Choose a saved address"}),K?.addresses?.map((T)=>T0("option",{value:T.id,children:[T.label||T.id," (",T.country,")"]},T.id))]})]})}),T0("table",{className:"fab-table",children:[M0("thead",{children:T0("tr",{children:[M0("th",{children:"Option"}),M0("th",{children:"Service"}),M0("th",{children:"Material"}),M0("th",{children:"Parts"}),M0("th",{children:"Shipping"}),M0("th",{children:"Total"}),M0("th",{children:"Lead"}),M0("th",{})]})}),M0("tbody",{children:i.options.map((T)=>T0("tr",{children:[M0("td",{className:"font-mono",children:T.id}),T0("td",{children:[T.provider,T.vendor?` / ${T.vendor}`:""]}),T0("td",{children:[T.material,T.process?` (${T.process})`:""]}),M0("td",{children:t9(T.subtotal,T.currency)}),T0("td",{children:[t9(T.shipping,T.currency),T.shipping_name?M0("div",{className:"fab-muted",children:T.shipping_name}):null]}),M0("td",{className:"font-mono",children:t9(T.total,T.currency)}),M0("td",{children:T.lead_time_days?`${T.lead_time_days} d`:"-"}),M0("td",{children:M0("button",{className:"design-button",disabled:!!U||!_,onClick:()=>F(i,T),children:"Prepare order"})})]},T.id))})]}),!K?.addresses?.length&&M0("p",{className:"fab-muted",children:"Add a shipping address under Policy and addresses before preparing an order."})]}):M0("p",{className:"fab-muted",children:"No options returned."})]})]}),y==="orders"&&T0("section",{className:"fab-card",children:[M0("h3",{className:"fab-h",children:"Orders"}),!E.length&&M0("p",{className:"fab-muted",children:"No orders yet."}),E.map((T)=>T0("div",{className:"fab-order","data-status":T.status,children:[T0("div",{className:"fab-row",children:[T0("strong",{children:["Order #",T.id]}),M0("span",{className:"fab-status",children:T.status.replace(/_/g," ")}),T0("span",{className:"fab-muted",children:[T.provider,T.option?.vendor?` / ${T.option.vendor}`:""," · ",T.option?.material," · design ",T.design_id," r",T.revision_id]}),M0("span",{className:"flex-1"}),M0("span",{className:"font-mono",children:t9(T.draft_total,T.currency)})]}),T.draft_total!==T.quoted_total&&T0("p",{className:"fab-muted",children:["Quoted ",t9(T.quoted_total,T.currency),"; provider confirmed ",t9(T.draft_total,T.currency),"."]}),T.status==="awaiting_approval"&&T0("p",{className:"fab-muted",children:["Needs approval: ",T.policy?.reasons?.join("; ")]}),T.provider_order_id&&T0("p",{className:"fab-muted",children:["Provider order ",T.provider_order_id,T.provider_status?` · ${T.provider_status}`:""]}),T.tracking?.payment_url&&T0("p",{className:"fab-muted",children:["Payment pending at the provider: ",M0("a",{className:"text-accent",href:T.tracking.payment_url,target:"_blank",rel:"noreferrer",children:"open payment page"})]}),T.tracking?.note&&M0("p",{className:"fab-muted",children:T.tracking.note}),(T.tracking?.tracking_number||T.tracking?.tracking_url)&&T0("p",{className:"fab-muted",children:["Tracking: ",T.tracking.carrier?`${T.tracking.carrier} `:"",T.tracking.tracking_url?M0("a",{className:"text-accent",href:T.tracking.tracking_url,target:"_blank",rel:"noreferrer",children:T.tracking.tracking_number||"track parcel"}):T.tracking.tracking_number]}),T.error&&M0("p",{className:"fab-warn",children:T.error}),T0("div",{className:"fab-row",children:[T.status==="awaiting_approval"&&T0(E7,{children:[T0("button",{className:"design-button design-button-primary",disabled:!!U,onClick:()=>S(T,"approve",{confirm_total:T.draft_total}),children:["Approve ",t9(T.draft_total,T.currency)]}),M0("button",{className:"design-button",disabled:!!U,onClick:()=>S(T,"decline"),children:"Decline"})]}),T.status==="approved"&&M0("button",{className:"design-button design-button-primary",disabled:!!U,onClick:()=>S(T,"submit"),children:"Place order now"}),!fX.includes(T.status)&&!["declined","cancelled","failed","delivered"].includes(T.status)&&M0("button",{className:"design-button",disabled:!!U,onClick:()=>S(T,"refresh"),children:"Refresh status"}),["awaiting_approval","approved","submitted","awaiting_payment"].includes(T.status)&&M0("button",{className:"design-button",disabled:!!U,onClick:()=>S(T,"cancel"),children:"Cancel"})]})]},T.id))]}),y==="settings"&&K&&M0(vX,{settings:K,supported:W,busy:!!U,onSave:s})]}),M0("style",{children:hX})]})}function vX({settings:J,supported:$,busy:N,onSave:Q}){let[Z,W]=h8(J),[Y,K]=h8(mZ),[H,X]=h8(JSON.stringify(J.provider_settings||{},null,2)),[G,E]=h8("");uZ(()=>{W(J),X(JSON.stringify(J.provider_settings||{},null,2))},[J]);let q=(U)=>W((C)=>({...C,...U}));function k(){try{let U=JSON.parse(H||"{}");E(""),Q({...Z,provider_settings:U})}catch{E("Provider settings must be valid JSON.")}}function M(){let U=Y.id||(Y.label||"address").toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,"")||`address-${Z.addresses.length+1}`;q({addresses:[...(Z.addresses||[]).filter((C)=>C.id!==U),{...Y,id:U}],default_address_id:Z.default_address_id||U}),K(mZ)}let _=(U)=>q({allowed_providers:Z.allowed_providers?.includes(U)?Z.allowed_providers.filter((C)=>C!==U):[...Z.allowed_providers||[],U]}),D=(U,C)=>T0("label",{className:"fab-muted",children:[C,M0("input",{className:"field",value:Y[U]||"",onChange:(L)=>K({...Y,[U]:L.target.value})})]});return T0(E7,{children:[T0("section",{className:"fab-card",children:[M0("h3",{className:"fab-h",children:"Order policy"}),T0("label",{className:"fab-check",children:[M0("input",{type:"checkbox",checked:Z.auto_order,onChange:(U)=>q({auto_order:U.target.checked})})," Let agents place orders without approval when within these limits"]}),T0("div",{className:"fab-grid",children:[T0("label",{className:"fab-muted",children:["Max per order",M0("input",{className:"field",type:"number",min:0,value:Z.max_order_total,onChange:(U)=>q({max_order_total:Number(U.target.value)})})]}),T0("label",{className:"fab-muted",children:["Monthly budget",M0("input",{className:"field",type:"number",min:0,value:Z.monthly_budget,onChange:(U)=>q({monthly_budget:Number(U.target.value)})})]}),T0("label",{className:"fab-muted",children:["Currency",M0("select",{className:"field",value:Z.currency,onChange:(U)=>q({currency:U.target.value}),children:["EUR","USD","GBP","CAD","AUD","CHF"].map((U)=>M0("option",{children:U},U))})]}),T0("label",{className:"fab-muted",children:["Quote country",M0("input",{className:"field",maxLength:2,value:Z.country,onChange:(U)=>q({country:U.target.value.toUpperCase()})})]})]}),M0("p",{className:"fab-muted",children:"Allowed services (none checked means all bound services are allowed):"}),M0("div",{className:"fab-chips",children:$.map((U)=>T0("label",{className:"fab-check",children:[M0("input",{type:"checkbox",checked:!!Z.allowed_providers?.includes(U.slug),onChange:()=>_(U.slug)})," ",U.name]},U.slug))}),M0("p",{className:"fab-muted",children:"Orders outside the limits, a price that rises more than 5% after quoting, or a different currency always wait for approval here."})]}),T0("section",{className:"fab-card",children:[M0("h3",{className:"fab-h",children:"Shipping addresses"}),Z.addresses?.map((U)=>T0("div",{className:"fab-row",children:[T0("label",{className:"fab-check",children:[M0("input",{type:"radio",name:"default-address",checked:Z.default_address_id===U.id,onChange:()=>q({default_address_id:U.id})})," ",U.label||U.id]}),T0("span",{className:"fab-muted",children:[U.first_name," ",U.last_name,", ",U.line1,", ",U.zip," ",U.city,", ",U.country]}),M0("span",{className:"flex-1"}),M0("button",{className:"design-button",onClick:()=>q({addresses:Z.addresses.filter((C)=>C.id!==U.id),default_address_id:Z.default_address_id===U.id?"":Z.default_address_id}),children:"Remove"})]},U.id)),T0("div",{className:"fab-grid",children:[D("label","Label"),D("first_name","First name"),D("last_name","Last name"),D("company","Company"),D("line1","Address line 1"),D("line2","Address line 2"),D("zip","Postal code"),D("city","City"),D("state","State"),D("country","Country (2 letters)"),D("phone","Phone"),D("email","Email")]}),M0("button",{className:"design-button",disabled:!Y.line1||!Y.city||!Y.country,onClick:M,children:"Add address"})]}),T0("section",{className:"fab-card",children:[M0("h3",{className:"fab-h",children:"Service settings"}),T0("p",{className:"fab-muted",children:["Per-service options keyed by slug. ",$.filter((U)=>U.settings?.length).map((U)=>`${U.slug}: ${U.settings.join(", ")}`).join(" · ")]}),M0("textarea",{className:"field fab-json",spellCheck:!1,value:H,onChange:(U)=>X(U.target.value)}),G&&M0("p",{className:"fab-warn",children:G})]}),M0("div",{className:"fab-row",children:M0("button",{className:"design-button design-button-primary",disabled:N,onClick:k,children:"Save policy and addresses"})})]})}var hX=`
.fab{display:flex;flex-direction:column;flex:1;min-height:0}
.fab-bar{display:flex;align-items:center;gap:6px;padding:8px 12px;border-bottom:1px solid var(--color-border,#252a31)}
.fab-body{flex:1;min-height:0;overflow-y:auto;padding:12px;display:flex;flex-direction:column;gap:12px}
.fab-card{border:1px solid var(--color-border,#252a31);border-radius:8px;padding:12px;background:var(--color-bg,#090b0e)}
.fab-h{margin:0 0 10px;font-size:12px;font-weight:600}
.fab-muted{font-size:11px;color:var(--color-text-muted,#8b949e);display:flex;flex-direction:column;gap:4px}
.fab-warn{font-size:11px;color:#f87171;margin:4px 0}
.fab-error{margin:8px 12px 0;padding:8px;border-radius:6px;font-size:12px;color:#fecaca;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.4)}
.fab-row{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:8px 0}
.fab-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:8px;margin:8px 0}
.fab-chips{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:8px}
.fab-chip{font-size:11px;padding:3px 8px;border-radius:999px;border:1px solid var(--color-border,#30363d);color:var(--color-text-muted,#8b949e)}.fab-chip[data-ok="true"]{color:var(--color-text,#e8edf2);border-color:color-mix(in srgb,var(--color-accent,#f97316) 50%,transparent)}
.fab-check{font-size:12px;display:flex;align-items:center;gap:6px}
.fab-num{width:80px}
.fab-json{min-height:90px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11px}
.fab-table{width:100%;border-collapse:collapse;font-size:11px}.fab-table th{text-align:left;font-weight:500;color:var(--color-text-muted,#8b949e);padding:6px 4px;border-bottom:1px solid var(--color-border,#252a31)}.fab-table td{padding:6px 4px;border-bottom:1px solid var(--color-border,#1b2027);vertical-align:top}
.fab-order{border-top:1px solid var(--color-border,#252a31);padding:8px 0;font-size:12px}.fab-order:first-of-type{border-top:0}
.fab-status{font-size:10px;text-transform:uppercase;letter-spacing:.08em;padding:2px 6px;border-radius:4px;background:var(--color-bg-input,#151a21)}
.fab-order[data-status="awaiting_approval"] .fab-status{color:var(--color-accent,#f97316)}.fab-order[data-status="submit_unknown"] .fab-status,.fab-order[data-status="failed"] .fab-status{color:#f87171}
`;import{useCallback as kN,useEffect as D7,useMemo as xX,useState as W8}from"react";import{jsx as N0,jsxs as h0}from"react/jsx-runtime";var gX="/api/apps/design/api",cZ={schema:"apteva-design/v1",units:"mm",parameters:{width:{type:"number",default:80,min:20,max:300},depth:{type:"number",default:50,min:20,max:300},thickness:{type:"number",default:4,min:1,max:20},hole:{type:"number",default:4.2,min:1,max:20},margin:{type:"number",default:7,min:2,max:40}},operations:[{id:"plate",type:"box",size:["width","depth","thickness"]},{id:"h1",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["margin","margin",-1]},{id:"h2",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["width-margin","margin",-1]},{id:"h3",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["width-margin","depth-margin",-1]},{id:"h4",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["margin","depth-margin",-1]},{id:"drilled",type:"cut",inputs:["plate","h1","h2","h3","h4"]},{id:"finished",type:"fillet",input:"drilled",radius:1.5}],output:"finished",checks:[{type:"bounding_box",max:[300,300,20]},{type:"body_count",equals:1}]};function RN(J,$){let N=J.includes("?")?"&":"?";return`${gX}${J}${N}project_id=${encodeURIComponent($)}`}async function I8(J,$,N){let Q=await fetch(RN(J,$),{credentials:"same-origin",...N,headers:{"Content-Type":"application/json",...N?.headers||{}}}),Z=await Q.json().catch(()=>({}));if(!Q.ok)throw Error(Z.error||Q.statusText);return Z}function e9(J,$=1){return Number.isFinite(J)?J.toLocaleString(void 0,{maximumFractionDigits:$}):"—"}function pX({projectId:J}){let[$,N]=W8([]),[Q,Z]=W8(null),[W,Y]=W8(null),[K,H]=W8(JSON.stringify(cZ,null,2)),[X,G]=W8({}),[E,q]=W8("Mounting plate"),[k,M]=W8(""),[_,D]=W8(""),[U,C]=W8(null),[L,B]=W8(""),[y,I]=W8("model"),[b,g]=W8(""),[V,F]=W8(""),[S,s]=W8(null),[i,m]=W8(null),[T,p]=W8([]),[$0,c]=W8(null),[X0,q0]=W8(""),[P0,d0]=W8(!1),[J8,c0]=W8(""),[t,K0]=W8(""),[x,J0]=W8(""),[Q0,E0]=W8(""),[m0,z]=W8(""),[x0,y0]=W8("manufacturer_cad"),[S0,L0]=W8("0"),[a0,B0]=W8(!1),[C0,X8]=W8(null);async function k8(P){g("Importing STEP"),F("");try{let r=new FormData;r.append("file",P),r.append("evidence",JSON.stringify({model:J8,manufacturer:t,hardware_revision:Q0,source:x,date:new Date().toISOString().slice(0,10),geometry_status:x0,complete:a0,uncertainty_mm:Number(S0),omissions:m0.split(`
`).filter(Boolean)}));let a=await fetch(RN("/sources",J),{method:"POST",credentials:"same-origin",body:r}),R0=await a.json();if(!a.ok)throw Error(R0.error||"Import failed");await j(),Z(R0.design.id),d0(!1),await o(R0.design.id),I("model")}catch(r){F(r.message)}finally{g("")}}async function N8(){if(!W)return;g("Generating enclosure"),F("");try{let P=await I8("/assembly-enclosures",J,{method:"POST",body:JSON.stringify({assembly_design_id:W.id,assembly_revision_id:W.current_revision_id,options:{}})});await j(),Z(P.design.id),await o(P.design.id)}catch(P){F(P.message)}finally{g("")}}function w(P,r,a=!1){if(!u)return;let R0=structuredClone(u),f0=R0.assembly?.instances.find((e0)=>e0.id===X0);if(!f0)return;let t0=Number(r);if(!Number.isFinite(t0))return;if(f0.transform||={},a)f0.transform.rotate=[{angle:t0,direction:[0,0,1]}];else f0.transform.translate||=[0,0,0],f0.transform.translate[P]=t0;H(JSON.stringify(R0,null,2))}let O=kN((P,r)=>I8(P,J,r),[J]),j=kN(async()=>{let P=await I8("/designs?status=all",J);if(N(P.designs||[]),!Q&&P.designs?.length)Z(P.designs[0].id)},[J,Q]),o=kN(async(P)=>{let a=(await I8(`/designs/${P}`,J)).design;Y(a),H(JSON.stringify(a.current_revision?.definition||cZ,null,2)),G(a.current_revision?.parameters||{}),s(a.latest_build?{report:a.latest_build.report,checks:a.latest_build.checks,artifacts:a.artifacts}:{artifacts:a.artifacts})},[J]);D7(()=>{j().catch((P)=>F(P.message))},[j]),D7(()=>{if(Q)o(Q).catch((P)=>F(P.message))},[Q,o]),D7(()=>{I8("/pcb-source",J).then((P)=>C(!!P.bound)).catch(()=>C(!1))},[J]),D7(()=>{I8("/examples",J).then((P)=>m(P.featured?.open_rover?.definition||null)).catch(()=>m(null))},[J]);let u=xX(()=>{try{return JSON.parse(K)}catch{return null}},[K]),n=u?.parameters||{};async function O0(){if(!u)return F("The design JSON is not valid.");g("Creating"),F("");try{let P=await I8("/designs",J,{method:"POST",body:JSON.stringify({name:E,kind:"parametric",definition:u,parameters:X,note:"Initial design"})});await j(),Z(P.design.id)}catch(P){F(P.message)}finally{g("")}}async function H0(){if(!W||!u)return F("Select a design and fix the source JSON first.");g("Saving revision"),F("");try{await I8(`/designs/${W.id}/revisions`,J,{method:"POST",body:JSON.stringify({expected_parent_id:W.current_revision_id,definition:u,parameters:X,note:L})}),B(""),await o(W.id),await j()}catch(P){F(P.message)}finally{g("")}}async function w0(){let P=Number(k),r=Number(_||0);if(!Number.isInteger(P)||P<=0)return F("Enter a valid PCB Studio design ID.");g("Creating PCB enclosure"),F("");try{let a=await I8("/pcb-enclosures",J,{method:"POST",body:JSON.stringify({pcb_design_id:P,pcb_revision_id:r,name:`PCB ${P} enclosure`})});await j(),Z(a.design.id),I("model")}catch(a){F(a.message)}finally{g("")}}async function b0(){if(!i)return F("The Open Rover reference design is unavailable.");g("Creating Open Rover"),F("");try{let P=await I8("/designs",J,{method:"POST",body:JSON.stringify({name:"Apteva Open Rover",description:"Fully printable modular open-hardware robot chassis.",kind:"parametric",tags:["robot","open-hardware","assembly","3d-printing"],definition:i,parameters:{},note:"Open Rover reference design"})});await I8(`/designs/${P.design.id}/build`,J,{method:"POST",body:JSON.stringify({formats:["mesh-json","glb"]})}),await j(),Z(P.design.id),await o(P.design.id),I("model")}catch(P){F(P.message)}finally{g("")}}async function e(){if(!W?.current_revision)return F("Select a component design first.");g("Creating linked assembly"),F("");try{let P=await I8("/assemblies",J,{method:"POST",body:JSON.stringify({name:`${W.name} Assembly`,description:`Assembly linked to ${W.name} r${W.current_revision.revision_number}.`,components:[{design_id:W.id,revision_id:W.current_revision.id}]})});await j(),Z(P.design.id),await o(P.design.id),I("source")}catch(P){F(P.message)}finally{g("")}}async function Z0(){if(!W||!u?.provenance||u.provenance.kind!=="pcb-mechanical-envelope")return;g("Refreshing PCB enclosure"),F("");try{await I8(`/designs/${W.id}/refresh-pcb`,J,{method:"POST",body:JSON.stringify({expected_parent_id:W.current_revision_id,pcb_revision_id:0})}),await o(W.id),await j()}catch(P){F(P.message)}finally{g("")}}async function _0(){if(!W)return;g("Checking linked components"),F("");try{await I8(`/designs/${W.id}/refresh-components`,J,{method:"POST",body:JSON.stringify({expected_parent_id:W.current_revision_id})}),await o(W.id),await j()}catch(P){F(P.message)}finally{g("")}}async function F0(P){if(!W)return F("Create or select a design first.");g(P.length>2?"Exporting":"Building"),F("");try{let r=await I8(`/designs/${W.id}/build`,J,{method:"POST",body:JSON.stringify({formats:P})});s(r),await o(W.id)}catch(r){F(r.message)}finally{g("")}}async function G0(){if(!W)return;g("Packaging"),F("");try{await I8(`/designs/${W.id}/package`,J,{method:"POST",body:"{}"}),await o(W.id)}catch(P){F(P.message)}finally{g("")}}let I0=S?.artifacts||W?.artifacts||[],A=I0.filter((P)=>P.format==="mesh-json"&&!P.metadata?.part_id).sort((P,r)=>r.id-P.id)[0],Y0=S?.report||W?.latest_build?.report,U0=S?.checks||W?.latest_build?.checks||[];return h0("div",{className:"design-studio h-full flex flex-col bg-bg text-text overflow-hidden",children:[h0("header",{className:"design-header h-14 px-4 border-b border-border flex items-center gap-3 shrink-0",children:[N0("div",{className:"w-8 h-8 rounded-lg bg-accent/15 text-accent grid place-items-center text-lg",children:"◇"}),h0("div",{children:[N0("h1",{className:"text-sm font-semibold leading-tight",children:"Design Studio"}),N0("p",{className:"text-[11px] text-text-muted",children:"Open hardware · assemblies · parametric CAD"})]}),N0("span",{className:"flex-1"}),N0("button",{className:"design-button",disabled:!!b,onClick:()=>d0(!P0),children:"Import STEP"}),N0("button",{className:"design-button",disabled:!!b||!W,onClick:N8,children:"Enclosure template"}),N0("button",{className:"design-button",disabled:!!b||!W,onClick:()=>F0(["mesh-json","glb"]),children:"Build preview"}),N0("button",{className:"design-button",disabled:!!b||!W,onClick:()=>F0(["step","stl","3mf","glb"]),children:"Export CAD"}),N0("button",{className:"design-button design-button-primary",disabled:!!b||!W,onClick:G0,children:"Manufacturing ZIP"})]}),P0&&h0("div",{className:"p-4 border-b border-border text-xs",style:{display:"grid",gridTemplateColumns:"repeat(4,minmax(0,1fr))",gap:8},children:[N0("input",{className:"field",placeholder:"Exact manufacturer model (required)",value:J8,onChange:(P)=>c0(P.target.value)}),N0("input",{className:"field",placeholder:"Manufacturer",value:t,onChange:(P)=>K0(P.target.value)}),N0("input",{className:"field",placeholder:"Hardware revision",value:Q0,onChange:(P)=>E0(P.target.value)}),N0("input",{className:"field",placeholder:"CAD source URL",value:x,onChange:(P)=>J0(P.target.value)}),h0("select",{className:"field",value:x0,onChange:(P)=>y0(P.target.value),children:[N0("option",{value:"manufacturer_cad",children:"Manufacturer CAD"}),N0("option",{value:"drawing_derived",children:"Drawing derived"}),N0("option",{value:"measured",children:"Physically measured"}),N0("option",{value:"estimated",children:"Estimated"})]}),N0("input",{className:"field",type:"number",min:"0",step:".01","aria-label":"Dimensional uncertainty in mm",value:S0,onChange:(P)=>L0(P.target.value)}),N0("textarea",{className:"field",placeholder:"Known omissions, one per line",value:m0,onChange:(P)=>z(P.target.value)}),h0("label",{children:[N0("input",{type:"checkbox",checked:a0,onChange:(P)=>B0(P.target.checked)}),"Complete geometry evidence"]}),h0("label",{children:["STEP file (up to 96 MiB)",N0("input",{type:"file",accept:".step,.stp",disabled:!!b||!J8.trim(),onChange:(P)=>{if(P.target.files?.[0])k8(P.target.files[0])}})]}),N0("span",{children:"Original file, source hash and assembly placements are retained. Missing features require measured proxy solids."})]}),V&&N0("div",{className:"px-4 py-2 text-xs border-b border-red/30 bg-red/10 text-red",children:V}),b&&N0("div",{className:"h-0.5 bg-accent animate-pulse"}),h0("div",{className:"design-workspace",children:[h0("aside",{className:"design-library",children:[N0("div",{className:"text-[10px] uppercase tracking-widest text-text-muted mb-2",children:"Designs"}),h0("div",{className:"space-y-1",children:[$.map((P)=>h0("button",{onClick:()=>Z(P.id),className:"design-list-item","data-selected":Q===P.id,children:[N0("div",{className:"design-list-name",children:P.name}),h0("div",{className:"design-list-meta",children:[P.kind,P.id===W?.id&&W.current_revision?` · r${W.current_revision.revision_number}`:""]})]},P.id)),!$.length&&N0("p",{className:"text-xs text-text-muted p-2",children:"No designs yet. Start with the template."})]}),h0("div",{className:"mt-5 pt-4 border-t border-border space-y-2",children:[N0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"New design"}),N0("input",{className:"field",value:E,onChange:(P)=>q(P.target.value),placeholder:"Design name"}),N0("button",{className:"design-button design-button-primary design-full",disabled:!!b||!E.trim()||!u,onClick:O0,children:"Create from editor"}),N0("button",{className:"design-button design-full",disabled:!!b||!W?.current_revision,onClick:e,children:"Start assembly with selected"})]}),h0("div",{className:"mt-5 pt-4 border-t border-border space-y-2",children:[N0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"Featured open hardware"}),h0("button",{className:"design-button design-button-rover design-full",disabled:!!b||!i,onClick:b0,children:[N0("span",{children:"◈"})," Create Open Rover"]}),N0("p",{className:"text-[10px] leading-4 text-text-muted",children:"Printable chassis, TPU tires, hubs, modular deck, interfaces, joints and release BOM."})]}),h0("div",{className:"pcb-import mt-5 pt-4 border-t border-border space-y-2","data-bound":U===!0,children:[h0("div",{className:"flex items-center justify-between gap-2",children:[N0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"PCB enclosure"}),N0("span",{className:"pcb-binding-dot",title:U?"PCB Studio bound":"PCB Studio not bound"})]}),N0("input",{className:"field",inputMode:"numeric",value:k,onChange:(P)=>M(P.target.value),placeholder:"PCB design ID"}),N0("input",{className:"field",inputMode:"numeric",value:_,onChange:(P)=>D(P.target.value),placeholder:"Revision ID · current if empty"}),N0("button",{className:"design-button design-full",disabled:!!b||!U||!k.trim(),onClick:w0,children:"Generate linked enclosure"}),U===!1&&N0("p",{className:"text-[10px] leading-4 text-text-muted",children:"Bind PCB Studio as the optional PCB source to enable this workflow."})]})]}),h0("main",{className:"design-main",children:[h0("div",{className:"h-11 px-3 border-b border-border flex items-center gap-2 bg-bg",children:[N0("button",{className:y==="model"?"tab-active":"tab",onClick:()=>I("model"),children:"3D model"}),N0("button",{className:y==="source"?"tab-active":"tab",onClick:()=>I("source"),children:"Geometry source"}),N0("button",{className:y==="fabrication"?"tab-active":"tab",onClick:()=>I("fabrication"),children:"Fabrication"}),N0("span",{className:"flex-1"}),W&&h0("span",{className:"text-[10px] text-text-muted font-mono",children:["r",W.current_revision?.revision_number," · ",W.current_revision?.source_sha256?.slice(0,10)]})]}),y==="fabrication"?N0(qN,{request:O,designId:W?.id,revisionId:W?.current_revision_id}):y==="model"?h0("div",{className:"design-viewport",children:[N0(DN,{projectId:J,artifactId:A?.id,designId:W?.id,revisionId:W?.current_revision_id,focus:T,issue:$0,onSelect:q0,onCapture:X8}),!A&&N0("div",{className:"absolute inset-0 grid place-items-center pointer-events-none",children:h0("div",{className:"text-center",children:[N0("div",{className:"text-4xl text-text-muted/30 mb-3",children:"◇"}),N0("p",{className:"text-sm text-text-muted",children:"Build a preview to inspect the model"}),N0("p",{className:"text-[11px] text-text-muted/60 mt-1",children:"Drag to orbit · wheel to zoom"})]})}),Y0&&h0("div",{className:"absolute left-3 bottom-3 flex gap-1.5",children:[N0(ON,{label:"X",value:`${e9(Y0.bounds.size[0])} mm`}),N0(ON,{label:"Y",value:`${e9(Y0.bounds.size[1])} mm`}),N0(ON,{label:"Z",value:`${e9(Y0.bounds.size[2])} mm`})]})]}):N0("textarea",{className:"flex-1 min-h-0 resize-none bg-[#0b1016] p-5 font-mono text-xs leading-5 outline-none text-[#cbd5e1]",spellCheck:!1,value:K,onChange:(P)=>H(P.target.value)})]}),h0("aside",{className:"design-inspector",children:[h0(G9,{title:"Parameters",children:[u?.print_profiles?.some((P)=>P.fits_mm)&&h0("label",{className:"text-xs",children:["Fit profile",h0("select",{className:"field",value:u.fit_profile_id||"",onChange:(P)=>{let r=structuredClone(u);r.fit_profile_id=P.target.value||void 0,H(JSON.stringify(r,null,2))},children:[N0("option",{value:"",children:"Parameter defaults"}),u.print_profiles.filter((P)=>P.fits_mm).map((P)=>N0("option",{value:P.id,children:P.name},P.id))]}),N0("p",{className:"text-[10px] mt-1",children:"Save a revision to apply. Explicit parameter values override profile allowances."})]}),Object.entries(n).map(([P,r])=>h0("label",{className:"design-param",children:[h0("span",{className:"design-param-head",children:[N0("span",{children:P}),h0("span",{className:"design-param-value",children:[X[P]??r.default," mm"]})]}),N0("input",{className:"design-range",type:"range",min:r.min??0,max:r.max??Math.max((r.default||1)*3,10),step:r.step??0.1,value:X[P]??r.default,onChange:(a)=>G((R0)=>({...R0,[P]:Number(a.target.value)}))})]},P)),!Object.keys(n).length&&N0("p",{className:"text-xs text-text-muted",children:"No exposed parameters."})]}),h0(G9,{title:"Revision",children:[N0("textarea",{className:"field min-h-16 resize-y",value:L,onChange:(P)=>B(P.target.value),placeholder:"What changed?"}),N0("button",{className:"design-button design-full design-revision-button",disabled:!!b||!W||!u,onClick:H0,children:"Save immutable revision"})]}),u?.provenance?.kind==="pcb-mechanical-envelope"&&h0(G9,{title:"PCB source",children:[N0(y8,{label:"PCB design",value:String(u.provenance.source_design_id)}),N0(y8,{label:"PCB revision",value:String(u.provenance.source_revision_id)}),N0(y8,{label:"Source hash",value:String(u.provenance.source_sha256||"").slice(0,12)}),N0("button",{className:"design-button design-full design-revision-button",disabled:!!b||!U||!W,onClick:Z0,children:"Refresh from current PCB revision"})]}),!!u?.parts?.some((P)=>P.source)&&h0(G9,{title:"Linked components",children:[N0("div",{className:"space-y-2",children:u.parts.filter((P)=>P.source).map((P)=>h0("div",{className:"rounded border border-border p-2 text-[11px]",children:[N0("div",{className:"font-medium",children:P.name}),h0("div",{className:"text-text-muted mt-1",children:["Design ",P.source.design_id," · revision ",P.source.revision_id]}),N0("div",{className:"font-mono text-text-muted",children:String(P.source.source_sha256).slice(0,12)})]},P.id))}),N0("button",{className:"design-button design-full design-revision-button",disabled:!!b||!W,onClick:_0,children:"Update to current source revisions"}),N0("p",{className:"text-[10px] leading-4 text-text-muted mt-2",children:"Updates create a new assembly revision. Existing revisions stay pinned and reproducible."})]}),X0&&u?.assembly&&h0(G9,{title:"Instance placement",children:[N0("p",{className:"text-xs mb-2",children:X0}),[0,1,2].map((P)=>h0("label",{className:"text-xs",children:[["X","Y","Z"][P]," mm",N0("input",{className:"field",type:"number",step:".1",value:u.assembly.instances.find((r)=>r.id===X0)?.transform?.translate?.[P]??0,onChange:(r)=>w(P,r.target.value)})]},P)),h0("label",{className:"text-xs",children:["Rotation about Z (degrees)",N0("input",{className:"field",type:"number",value:u.assembly.instances.find((P)=>P.id===X0)?.transform?.rotate?.[0]?.angle??0,onChange:(P)=>w(2,P.target.value,!0)})]}),N0("button",{className:"design-button",disabled:!!b,onClick:H0,children:"Save placement revision"}),N0("p",{className:"text-[10px] mt-2",children:"Save, then build to validate the new placement. Arbitrary rotations and datum alignment are available in Source."})]}),C0&&h0(G9,{title:"Inspection capture",children:[N0("img",{src:C0,alt:"Saved assembly inspection",style:{width:"100%"}}),N0("p",{className:"text-[10px]",children:"Downloaded PNG. Attach it to revision-bound test evidence for release."})]}),Y0&&h0(G9,{title:"Measurements",children:[N0(y8,{label:"Bodies",value:String(Y0.body_count||1)}),N0(y8,{label:"Mass",value:Y0.mass_g?`${e9(Y0.mass_g)} g`:"—"}),N0(y8,{label:"Volume",value:`${e9(Y0.volume_mm3)} mm³`}),N0(y8,{label:"Surface",value:`${e9(Y0.surface_area_mm2)} mm²`}),N0(y8,{label:"Faces",value:String(Y0.face_count)}),N0(y8,{label:"Triangles",value:String(Y0.triangle_count)})]}),u?.open_hardware&&h0(G9,{title:"Open hardware",children:[N0(y8,{label:"Project",value:u.open_hardware.project_name}),N0(y8,{label:"Version",value:u.open_hardware.version}),N0(y8,{label:"License",value:u.open_hardware.license}),N0(y8,{label:"Parts",value:String(u.parts?.length||0)}),N0(y8,{label:"Instances",value:String(u.assembly?.instances?.length||0)}),N0(y8,{label:"Joints",value:String(u.assembly?.joints?.length||0)})]}),!!U0.length&&N0(G9,{title:"Validation",children:N0("div",{className:"space-y-2",children:U0.map((P,r)=>h0("button",{onClick:()=>{p(P.instances||[]),c(P)},className:"design-validation-issue flex gap-2 text-xs text-left w-full",title:"Focus affected instances",children:[N0("span",{className:P.status==="pass"?"text-green":"text-red",children:P.status==="pass"?"●":"×"}),h0("span",{className:"text-text-muted",children:[P.status,": ",P.message]})]},r))})}),N0(G9,{title:"Artifacts",children:h0("div",{className:"space-y-1.5",children:[I0.filter((P)=>P.format!=="mesh-json").map((P)=>h0("a",{className:"flex items-center gap-2 rounded border border-border p-2 hover:border-accent/50 text-xs",href:RN(`/artifacts/${P.id}/content`,J),children:[N0("span",{className:"font-mono uppercase text-accent w-9",children:P.format}),N0("span",{className:"truncate flex-1",children:P.name}),h0("span",{className:"text-text-muted",children:[e9(P.size_bytes/1024),"K"]})]},P.id)),!I0.some((P)=>P.format!=="mesh-json")&&N0("p",{className:"text-xs text-text-muted",children:"Exports will appear here."})]})})]})]}),N0("style",{children:dX})]})}function G9({title:J,children:$}){return h0("section",{className:"design-section",children:[N0("h2",{className:"design-section-title",children:J}),$]})}function y8({label:J,value:$}){return h0("div",{className:"design-row",children:[N0("span",{className:"text-text-muted",children:J}),N0("span",{className:"font-mono",children:$})]})}function ON({label:J,value:$}){return h0("div",{className:"rounded bg-black/50 backdrop-blur px-2 py-1 text-[10px]",children:[N0("span",{className:"text-text-muted mr-1",children:J}),N0("span",{className:"font-mono",children:$})]})}var dX=`
.design-studio,.design-studio *{box-sizing:border-box}.design-studio{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--color-bg,#090b0e);color:var(--color-text,#e8edf2)}
.design-header{display:flex;align-items:center;gap:12px;height:56px;min-height:56px;padding:0 16px;border-bottom:1px solid var(--color-border,#252a31);overflow-x:auto}
.design-workspace{display:grid;grid-template-columns:220px minmax(360px,1fr) 320px;flex:1;min-width:0;min-height:0;overflow:hidden}
.design-library{min-width:0;min-height:0;padding:12px;border-right:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-main{display:flex;flex-direction:column;min-width:0;min-height:0;background:#0c1118}
.design-viewport{position:relative;flex:1;min-width:0;min-height:0;overflow:hidden;background:radial-gradient(circle at 50% 38%,#18202a 0,#0a0f15 48%,#05080c 100%)}
.design-viewport:after{content:"";position:absolute;inset:58% -20% -30%;pointer-events:none;opacity:.25;transform:perspective(500px) rotateX(62deg);transform-origin:top;background-image:linear-gradient(rgba(249,115,22,.18) 1px,transparent 1px),linear-gradient(90deg,rgba(249,115,22,.18) 1px,transparent 1px);background-size:32px 32px}
.design-inspector{min-width:0;min-height:0;border-left:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-list-item{display:block;width:100%;padding:10px;text-align:left;color:inherit;background:transparent;border:1px solid transparent;border-radius:8px;cursor:pointer;transition:background .15s,border-color .15s}
.design-list-item:hover{background:var(--color-bg-input,#151a21)}.design-list-item[data-selected="true"]{background:color-mix(in srgb,var(--color-accent,#f97316) 12%,transparent);border-color:color-mix(in srgb,var(--color-accent,#f97316) 48%,transparent)}
.design-list-name{font-size:12px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.design-list-meta{margin-top:4px;font-size:10px;color:var(--color-text-muted,#8b949e)}
.design-button{flex:none;font-size:11px;line-height:1.2;padding:7px 10px;color:var(--color-text,#e8edf2);background:transparent;border:1px solid var(--color-border,#30363d);border-radius:6px;cursor:pointer;transition:background .15s,border-color .15s}.design-button:hover{background:var(--color-bg-input,#151a21);border-color:var(--color-text-muted,#6e7681)}.design-button-primary{background:var(--color-accent,#f97316);border-color:var(--color-accent,#f97316);color:var(--color-bg,#090b0e);font-weight:700}.design-button:disabled{opacity:.4;cursor:not-allowed}.design-full{width:100%}.design-revision-button{margin-top:8px}
.design-button-rover{display:flex;align-items:center;justify-content:center;gap:8px;border-color:color-mix(in srgb,var(--color-accent,#f97316) 55%,var(--color-border,#30363d));background:color-mix(in srgb,var(--color-accent,#f97316) 9%,transparent)}
.design-studio .field{display:block;width:100%;border:1px solid var(--color-border,#30363d);background:var(--color-bg-input,#151a21);color:inherit;border-radius:6px;padding:7px 8px;font-size:12px;outline:none}.design-studio .field:focus{border-color:var(--color-accent,#f97316)}
.design-studio .tab,.design-studio .tab-active{font-size:11px;padding:6px 9px;border:0;border-radius:6px;background:transparent;color:var(--color-text-muted,#8b949e);cursor:pointer}.design-studio .tab-active{background:var(--color-bg-input,#151a21);color:var(--color-text,#e8edf2)}
.design-validation-issue{background:transparent;border:0;color:inherit;cursor:pointer;padding:4px 0;font-size:11px;text-align:left}.design-validation-issue:hover{color:#f97316}
.design-canvas{display:block;width:100%;height:100%;cursor:grab;touch-action:none}.design-canvas:active{cursor:grabbing}
.design-section{padding:16px;border-bottom:1px solid var(--color-border,#252a31)}.design-section-title{margin:0 0 12px;font-size:10px;font-weight:500;text-transform:uppercase;letter-spacing:.12em;color:var(--color-text-muted,#8b949e)}
.design-param{display:block;margin-bottom:14px}.design-param-head{display:flex;justify-content:space-between;gap:8px;margin-bottom:5px;font-size:12px}.design-param-value{color:var(--color-accent,#f97316);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.design-range{display:block;width:100%;accent-color:var(--color-accent,#f97316)}
.design-row{display:flex;justify-content:space-between;gap:12px;padding:4px 0;font-size:12px}
.pcb-binding-dot{width:7px;height:7px;border-radius:50%;background:#64748b;box-shadow:0 0 0 3px rgba(100,116,139,.12)}.pcb-import[data-bound="true"] .pcb-binding-dot{background:#22c55e;box-shadow:0 0 0 3px rgba(34,197,94,.12)}
@media(max-width:1100px){.design-workspace{grid-template-columns:190px minmax(320px,1fr) 280px}}
@media(max-width:820px){.design-workspace{grid-template-columns:170px minmax(280px,1fr)}.design-inspector{display:none}}
`;export{pX as default};

//# debugId=237B9296114464E264756E2164756E21
