import{useEffect as H7,useRef as HQ,useState as b9}from"react";var pQ="180",CJ={LEFT:0,MIDDLE:1,RIGHT:2,ROTATE:0,DOLLY:1,PAN:2},_J={ROTATE:0,PAN:1,DOLLY_PAN:2,DOLLY_ROTATE:3},lQ=0,f7=1,dQ=2;var v7=1,mQ=2,QJ=3,D8=0,p9=1,s9=2,IJ=0,h8=1,h7=2,x7=3,g7=4,uQ=5,q8=100,cQ=101,nQ=102,sQ=103,iQ=104,oQ=200,aQ=201,rQ=202,tQ=203,eQ=204,JZ=205,$Z=206,QZ=207,ZZ=208,WZ=209,KZ=210,YZ=211,HZ=212,XZ=213,NZ=214,w6=0,C6=1,_6=2,x8=3,I6=4,P6=5,A6=6,T6=7,UZ=0,GZ=1,EZ=2,RJ=0,DZ=1,qZ=2,OZ=3,RZ=4,kZ=5,FZ=6,MZ=7;var O8=301,hJ=302,S6=303,j6=304,g8=306,LZ=1000,VZ=1001,zZ=1002,R8=1003,BZ=1004;var p8=1005;var xJ=1006,y6=1007;var k8=1008;var PJ=1009,wZ=1010,CZ=1011,l8=1012,p7=1013,F8=1014,AJ=1015,d8=1016,l7=1017,d7=1018,M8=1020,_Z=35902,IZ=35899,PZ=1021,AZ=1022,ZJ=1023,b6=1026,m8=1027,TZ=1028,m7=1029,SZ=1030,u7=1031;var c7=1033,f6=33776,v6=33777,h6=33778,x6=33779,n7=35840,s7=35841,i7=35842,o7=35843,a7=36196,r7=37492,t7=37496,e7=37808,J$=37809,$$=37810,Q$=37811,Z$=37812,W$=37813,K$=37814,Y$=37815,H$=37816,X$=37817,N$=37818,U$=37819,G$=37820,E$=37821,D$=36492,q$=36494,O$=36495,R$=36283,k$=36284,F$=36285,M$=36286;var jZ=3201;var yZ=0,bZ=1,gJ="",fZ="srgb",u8="srgb-linear",L$="linear",K9="srgb";var vZ=512,hZ=513,xZ=514,V$=515,gZ=516,pZ=517,lZ=518,dZ=519;var z$="300 es",B$=2000;class kJ{addEventListener(J,$){if(this._listeners===void 0)this._listeners={};let Q=this._listeners;if(Q[J]===void 0)Q[J]=[];if(Q[J].indexOf($)===-1)Q[J].push($)}hasEventListener(J,$){let Q=this._listeners;if(Q===void 0)return!1;return Q[J]!==void 0&&Q[J].indexOf($)!==-1}removeEventListener(J,$){let Q=this._listeners;if(Q===void 0)return;let Z=Q[J];if(Z!==void 0){let W=Z.indexOf($);if(W!==-1)Z.splice(W,1)}}dispatchEvent(J){let $=this._listeners;if($===void 0)return;let Q=$[J.type];if(Q!==void 0){J.target=this;let Z=Q.slice(0);for(let W=0,K=Z.length;W<K;W++)Z[W].call(this,J);J.target=null}}}var V9=["00","01","02","03","04","05","06","07","08","09","0a","0b","0c","0d","0e","0f","10","11","12","13","14","15","16","17","18","19","1a","1b","1c","1d","1e","1f","20","21","22","23","24","25","26","27","28","29","2a","2b","2c","2d","2e","2f","30","31","32","33","34","35","36","37","38","39","3a","3b","3c","3d","3e","3f","40","41","42","43","44","45","46","47","48","49","4a","4b","4c","4d","4e","4f","50","51","52","53","54","55","56","57","58","59","5a","5b","5c","5d","5e","5f","60","61","62","63","64","65","66","67","68","69","6a","6b","6c","6d","6e","6f","70","71","72","73","74","75","76","77","78","79","7a","7b","7c","7d","7e","7f","80","81","82","83","84","85","86","87","88","89","8a","8b","8c","8d","8e","8f","90","91","92","93","94","95","96","97","98","99","9a","9b","9c","9d","9e","9f","a0","a1","a2","a3","a4","a5","a6","a7","a8","a9","aa","ab","ac","ad","ae","af","b0","b1","b2","b3","b4","b5","b6","b7","b8","b9","ba","bb","bc","bd","be","bf","c0","c1","c2","c3","c4","c5","c6","c7","c8","c9","ca","cb","cc","cd","ce","cf","d0","d1","d2","d3","d4","d5","d6","d7","d8","d9","da","db","dc","dd","de","df","e0","e1","e2","e3","e4","e5","e6","e7","e8","e9","ea","eb","ec","ed","ee","ef","f0","f1","f2","f3","f4","f5","f6","f7","f8","f9","fa","fb","fc","fd","fe","ff"],RQ=1234567,b8=Math.PI/180,G8=180/Math.PI;function L8(){let J=Math.random()*4294967295|0,$=Math.random()*4294967295|0,Q=Math.random()*4294967295|0,Z=Math.random()*4294967295|0;return(V9[J&255]+V9[J>>8&255]+V9[J>>16&255]+V9[J>>24&255]+"-"+V9[$&255]+V9[$>>8&255]+"-"+V9[$>>16&15|64]+V9[$>>24&255]+"-"+V9[Q&63|128]+V9[Q>>8&255]+"-"+V9[Q>>16&255]+V9[Q>>24&255]+V9[Z&255]+V9[Z>>8&255]+V9[Z>>16&255]+V9[Z>>24&255]).toLowerCase()}function u0(J,$,Q){return Math.max($,Math.min(Q,J))}function w$(J,$){return(J%$+$)%$}function uW(J,$,Q,Z,W){return Z+(J-$)*(W-Z)/(Q-$)}function cW(J,$,Q){if(J!==$)return(Q-J)/($-J);else return 0}function f8(J,$,Q){return(1-Q)*J+Q*$}function nW(J,$,Q,Z){return f8(J,$,1-Math.exp(-Q*Z))}function sW(J,$=1){return $-Math.abs(w$(J,$*2)-$)}function iW(J,$,Q){if(J<=$)return 0;if(J>=Q)return 1;return J=(J-$)/(Q-$),J*J*(3-2*J)}function oW(J,$,Q){if(J<=$)return 0;if(J>=Q)return 1;return J=(J-$)/(Q-$),J*J*J*(J*(J*6-15)+10)}function aW(J,$){return J+Math.floor(Math.random()*($-J+1))}function rW(J,$){return J+Math.random()*($-J)}function tW(J){return J*(0.5-Math.random())}function eW(J){if(J!==void 0)RQ=J;let $=RQ+=1831565813;return $=Math.imul($^$>>>15,$|1),$^=$+Math.imul($^$>>>7,$|61),(($^$>>>14)>>>0)/4294967296}function JK(J){return J*b8}function $K(J){return J*G8}function QK(J){return(J&J-1)===0&&J!==0}function ZK(J){return Math.pow(2,Math.ceil(Math.log(J)/Math.LN2))}function WK(J){return Math.pow(2,Math.floor(Math.log(J)/Math.LN2))}function KK(J,$,Q,Z,W){let{cos:K,sin:H}=Math,Y=K(Q/2),X=H(Q/2),N=K(($+Z)/2),U=H(($+Z)/2),G=K(($-Z)/2),D=H(($-Z)/2),R=K((Z-$)/2),M=H((Z-$)/2);switch(W){case"XYX":J.set(Y*U,X*G,X*D,Y*N);break;case"YZY":J.set(X*D,Y*U,X*G,Y*N);break;case"ZXZ":J.set(X*G,X*D,Y*U,Y*N);break;case"XZX":J.set(Y*U,X*M,X*R,Y*N);break;case"YXY":J.set(X*R,Y*U,X*M,Y*N);break;case"ZYZ":J.set(X*M,X*R,Y*U,Y*N);break;default:console.warn("THREE.MathUtils: .setQuaternionFromProperEuler() encountered an unknown order: "+W)}}function N8(J,$){switch($.constructor){case Float32Array:return J;case Uint32Array:return J/4294967295;case Uint16Array:return J/65535;case Uint8Array:return J/255;case Int32Array:return Math.max(J/2147483647,-1);case Int16Array:return Math.max(J/32767,-1);case Int8Array:return Math.max(J/127,-1);default:throw Error("Invalid component type.")}}function _9(J,$){switch($.constructor){case Float32Array:return J;case Uint32Array:return Math.round(J*4294967295);case Uint16Array:return Math.round(J*65535);case Uint8Array:return Math.round(J*255);case Int32Array:return Math.round(J*2147483647);case Int16Array:return Math.round(J*32767);case Int8Array:return Math.round(J*127);default:throw Error("Invalid component type.")}}var C$={DEG2RAD:b8,RAD2DEG:G8,generateUUID:L8,clamp:u0,euclideanModulo:w$,mapLinear:uW,inverseLerp:cW,lerp:f8,damp:nW,pingpong:sW,smoothstep:iW,smootherstep:oW,randInt:aW,randFloat:rW,randFloatSpread:tW,seededRandom:eW,degToRad:JK,radToDeg:$K,isPowerOfTwo:QK,ceilPowerOfTwo:ZK,floorPowerOfTwo:WK,setQuaternionFromProperEuler:KK,normalize:_9,denormalize:N8};class x0{constructor(J=0,$=0){x0.prototype.isVector2=!0,this.x=J,this.y=$}get width(){return this.x}set width(J){this.x=J}get height(){return this.y}set height(J){this.y=J}set(J,$){return this.x=J,this.y=$,this}setScalar(J){return this.x=J,this.y=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y)}copy(J){return this.x=J.x,this.y=J.y,this}add(J){return this.x+=J.x,this.y+=J.y,this}addScalar(J){return this.x+=J,this.y+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this}subScalar(J){return this.x-=J,this.y-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this}multiply(J){return this.x*=J.x,this.y*=J.y,this}multiplyScalar(J){return this.x*=J,this.y*=J,this}divide(J){return this.x/=J.x,this.y/=J.y,this}divideScalar(J){return this.multiplyScalar(1/J)}applyMatrix3(J){let $=this.x,Q=this.y,Z=J.elements;return this.x=Z[0]*$+Z[3]*Q+Z[6],this.y=Z[1]*$+Z[4]*Q+Z[7],this}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this}clamp(J,$){return this.x=u0(this.x,J.x,$.x),this.y=u0(this.y,J.y,$.y),this}clampScalar(J,$){return this.x=u0(this.x,J,$),this.y=u0(this.y,J,$),this}clampLength(J,$){let Q=this.length();return this.divideScalar(Q||1).multiplyScalar(u0(Q,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this}negate(){return this.x=-this.x,this.y=-this.y,this}dot(J){return this.x*J.x+this.y*J.y}cross(J){return this.x*J.y-this.y*J.x}lengthSq(){return this.x*this.x+this.y*this.y}length(){return Math.sqrt(this.x*this.x+this.y*this.y)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)}normalize(){return this.divideScalar(this.length()||1)}angle(){return Math.atan2(-this.y,-this.x)+Math.PI}angleTo(J){let $=Math.sqrt(this.lengthSq()*J.lengthSq());if($===0)return Math.PI/2;let Q=this.dot(J)/$;return Math.acos(u0(Q,-1,1))}distanceTo(J){return Math.sqrt(this.distanceToSquared(J))}distanceToSquared(J){let $=this.x-J.x,Q=this.y-J.y;return $*$+Q*Q}manhattanDistanceTo(J){return Math.abs(this.x-J.x)+Math.abs(this.y-J.y)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this}lerpVectors(J,$,Q){return this.x=J.x+($.x-J.x)*Q,this.y=J.y+($.y-J.y)*Q,this}equals(J){return J.x===this.x&&J.y===this.y}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this}rotateAround(J,$){let Q=Math.cos($),Z=Math.sin($),W=this.x-J.x,K=this.y-J.y;return this.x=W*Q-K*Z+J.x,this.y=W*Z+K*Q+J.y,this}random(){return this.x=Math.random(),this.y=Math.random(),this}*[Symbol.iterator](){yield this.x,yield this.y}}class e9{constructor(J=0,$=0,Q=0,Z=1){this.isQuaternion=!0,this._x=J,this._y=$,this._z=Q,this._w=Z}static slerpFlat(J,$,Q,Z,W,K,H){let Y=Q[Z+0],X=Q[Z+1],N=Q[Z+2],U=Q[Z+3],G=W[K+0],D=W[K+1],R=W[K+2],M=W[K+3];if(H===0){J[$+0]=Y,J[$+1]=X,J[$+2]=N,J[$+3]=U;return}if(H===1){J[$+0]=G,J[$+1]=D,J[$+2]=R,J[$+3]=M;return}if(U!==M||Y!==G||X!==D||N!==R){let L=1-H,E=Y*G+X*D+N*R+U*M,q=E>=0?1:-1,A=1-E*E;if(A>Number.EPSILON){let I=Math.sqrt(A),f=Math.atan2(I,E*q);L=Math.sin(L*f)/I,H=Math.sin(H*f)/I}let V=H*q;if(Y=Y*L+G*V,X=X*L+D*V,N=N*L+R*V,U=U*L+M*V,L===1-H){let I=1/Math.sqrt(Y*Y+X*X+N*N+U*U);Y*=I,X*=I,N*=I,U*=I}}J[$]=Y,J[$+1]=X,J[$+2]=N,J[$+3]=U}static multiplyQuaternionsFlat(J,$,Q,Z,W,K){let H=Q[Z],Y=Q[Z+1],X=Q[Z+2],N=Q[Z+3],U=W[K],G=W[K+1],D=W[K+2],R=W[K+3];return J[$]=H*R+N*U+Y*D-X*G,J[$+1]=Y*R+N*G+X*U-H*D,J[$+2]=X*R+N*D+H*G-Y*U,J[$+3]=N*R-H*U-Y*G-X*D,J}get x(){return this._x}set x(J){this._x=J,this._onChangeCallback()}get y(){return this._y}set y(J){this._y=J,this._onChangeCallback()}get z(){return this._z}set z(J){this._z=J,this._onChangeCallback()}get w(){return this._w}set w(J){this._w=J,this._onChangeCallback()}set(J,$,Q,Z){return this._x=J,this._y=$,this._z=Q,this._w=Z,this._onChangeCallback(),this}clone(){return new this.constructor(this._x,this._y,this._z,this._w)}copy(J){return this._x=J.x,this._y=J.y,this._z=J.z,this._w=J.w,this._onChangeCallback(),this}setFromEuler(J,$=!0){let{_x:Q,_y:Z,_z:W,_order:K}=J,H=Math.cos,Y=Math.sin,X=H(Q/2),N=H(Z/2),U=H(W/2),G=Y(Q/2),D=Y(Z/2),R=Y(W/2);switch(K){case"XYZ":this._x=G*N*U+X*D*R,this._y=X*D*U-G*N*R,this._z=X*N*R+G*D*U,this._w=X*N*U-G*D*R;break;case"YXZ":this._x=G*N*U+X*D*R,this._y=X*D*U-G*N*R,this._z=X*N*R-G*D*U,this._w=X*N*U+G*D*R;break;case"ZXY":this._x=G*N*U-X*D*R,this._y=X*D*U+G*N*R,this._z=X*N*R+G*D*U,this._w=X*N*U-G*D*R;break;case"ZYX":this._x=G*N*U-X*D*R,this._y=X*D*U+G*N*R,this._z=X*N*R-G*D*U,this._w=X*N*U+G*D*R;break;case"YZX":this._x=G*N*U+X*D*R,this._y=X*D*U+G*N*R,this._z=X*N*R-G*D*U,this._w=X*N*U-G*D*R;break;case"XZY":this._x=G*N*U-X*D*R,this._y=X*D*U-G*N*R,this._z=X*N*R+G*D*U,this._w=X*N*U+G*D*R;break;default:console.warn("THREE.Quaternion: .setFromEuler() encountered an unknown order: "+K)}if($===!0)this._onChangeCallback();return this}setFromAxisAngle(J,$){let Q=$/2,Z=Math.sin(Q);return this._x=J.x*Z,this._y=J.y*Z,this._z=J.z*Z,this._w=Math.cos(Q),this._onChangeCallback(),this}setFromRotationMatrix(J){let $=J.elements,Q=$[0],Z=$[4],W=$[8],K=$[1],H=$[5],Y=$[9],X=$[2],N=$[6],U=$[10],G=Q+H+U;if(G>0){let D=0.5/Math.sqrt(G+1);this._w=0.25/D,this._x=(N-Y)*D,this._y=(W-X)*D,this._z=(K-Z)*D}else if(Q>H&&Q>U){let D=2*Math.sqrt(1+Q-H-U);this._w=(N-Y)/D,this._x=0.25*D,this._y=(Z+K)/D,this._z=(W+X)/D}else if(H>U){let D=2*Math.sqrt(1+H-Q-U);this._w=(W-X)/D,this._x=(Z+K)/D,this._y=0.25*D,this._z=(Y+N)/D}else{let D=2*Math.sqrt(1+U-Q-H);this._w=(K-Z)/D,this._x=(W+X)/D,this._y=(Y+N)/D,this._z=0.25*D}return this._onChangeCallback(),this}setFromUnitVectors(J,$){let Q=J.dot($)+1;if(Q<0.00000001)if(Q=0,Math.abs(J.x)>Math.abs(J.z))this._x=-J.y,this._y=J.x,this._z=0,this._w=Q;else this._x=0,this._y=-J.z,this._z=J.y,this._w=Q;else this._x=J.y*$.z-J.z*$.y,this._y=J.z*$.x-J.x*$.z,this._z=J.x*$.y-J.y*$.x,this._w=Q;return this.normalize()}angleTo(J){return 2*Math.acos(Math.abs(u0(this.dot(J),-1,1)))}rotateTowards(J,$){let Q=this.angleTo(J);if(Q===0)return this;let Z=Math.min(1,$/Q);return this.slerp(J,Z),this}identity(){return this.set(0,0,0,1)}invert(){return this.conjugate()}conjugate(){return this._x*=-1,this._y*=-1,this._z*=-1,this._onChangeCallback(),this}dot(J){return this._x*J._x+this._y*J._y+this._z*J._z+this._w*J._w}lengthSq(){return this._x*this._x+this._y*this._y+this._z*this._z+this._w*this._w}length(){return Math.sqrt(this._x*this._x+this._y*this._y+this._z*this._z+this._w*this._w)}normalize(){let J=this.length();if(J===0)this._x=0,this._y=0,this._z=0,this._w=1;else J=1/J,this._x=this._x*J,this._y=this._y*J,this._z=this._z*J,this._w=this._w*J;return this._onChangeCallback(),this}multiply(J){return this.multiplyQuaternions(this,J)}premultiply(J){return this.multiplyQuaternions(J,this)}multiplyQuaternions(J,$){let{_x:Q,_y:Z,_z:W,_w:K}=J,H=$._x,Y=$._y,X=$._z,N=$._w;return this._x=Q*N+K*H+Z*X-W*Y,this._y=Z*N+K*Y+W*H-Q*X,this._z=W*N+K*X+Q*Y-Z*H,this._w=K*N-Q*H-Z*Y-W*X,this._onChangeCallback(),this}slerp(J,$){if($===0)return this;if($===1)return this.copy(J);let Q=this._x,Z=this._y,W=this._z,K=this._w,H=K*J._w+Q*J._x+Z*J._y+W*J._z;if(H<0)this._w=-J._w,this._x=-J._x,this._y=-J._y,this._z=-J._z,H=-H;else this.copy(J);if(H>=1)return this._w=K,this._x=Q,this._y=Z,this._z=W,this;let Y=1-H*H;if(Y<=Number.EPSILON){let D=1-$;return this._w=D*K+$*this._w,this._x=D*Q+$*this._x,this._y=D*Z+$*this._y,this._z=D*W+$*this._z,this.normalize(),this}let X=Math.sqrt(Y),N=Math.atan2(X,H),U=Math.sin((1-$)*N)/X,G=Math.sin($*N)/X;return this._w=K*U+this._w*G,this._x=Q*U+this._x*G,this._y=Z*U+this._y*G,this._z=W*U+this._z*G,this._onChangeCallback(),this}slerpQuaternions(J,$,Q){return this.copy(J).slerp($,Q)}random(){let J=2*Math.PI*Math.random(),$=2*Math.PI*Math.random(),Q=Math.random(),Z=Math.sqrt(1-Q),W=Math.sqrt(Q);return this.set(Z*Math.sin(J),Z*Math.cos(J),W*Math.sin($),W*Math.cos($))}equals(J){return J._x===this._x&&J._y===this._y&&J._z===this._z&&J._w===this._w}fromArray(J,$=0){return this._x=J[$],this._y=J[$+1],this._z=J[$+2],this._w=J[$+3],this._onChangeCallback(),this}toArray(J=[],$=0){return J[$]=this._x,J[$+1]=this._y,J[$+2]=this._z,J[$+3]=this._w,J}fromBufferAttribute(J,$){return this._x=J.getX($),this._y=J.getY($),this._z=J.getZ($),this._w=J.getW($),this._onChangeCallback(),this}toJSON(){return this.toArray()}_onChange(J){return this._onChangeCallback=J,this}_onChangeCallback(){}*[Symbol.iterator](){yield this._x,yield this._y,yield this._z,yield this._w}}class v{constructor(J=0,$=0,Q=0){v.prototype.isVector3=!0,this.x=J,this.y=$,this.z=Q}set(J,$,Q){if(Q===void 0)Q=this.z;return this.x=J,this.y=$,this.z=Q,this}setScalar(J){return this.x=J,this.y=J,this.z=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setZ(J){return this.z=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;case 2:this.z=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;case 2:return this.z;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y,this.z)}copy(J){return this.x=J.x,this.y=J.y,this.z=J.z,this}add(J){return this.x+=J.x,this.y+=J.y,this.z+=J.z,this}addScalar(J){return this.x+=J,this.y+=J,this.z+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this.z=J.z+$.z,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this.z+=J.z*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this.z-=J.z,this}subScalar(J){return this.x-=J,this.y-=J,this.z-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this.z=J.z-$.z,this}multiply(J){return this.x*=J.x,this.y*=J.y,this.z*=J.z,this}multiplyScalar(J){return this.x*=J,this.y*=J,this.z*=J,this}multiplyVectors(J,$){return this.x=J.x*$.x,this.y=J.y*$.y,this.z=J.z*$.z,this}applyEuler(J){return this.applyQuaternion(kQ.setFromEuler(J))}applyAxisAngle(J,$){return this.applyQuaternion(kQ.setFromAxisAngle(J,$))}applyMatrix3(J){let $=this.x,Q=this.y,Z=this.z,W=J.elements;return this.x=W[0]*$+W[3]*Q+W[6]*Z,this.y=W[1]*$+W[4]*Q+W[7]*Z,this.z=W[2]*$+W[5]*Q+W[8]*Z,this}applyNormalMatrix(J){return this.applyMatrix3(J).normalize()}applyMatrix4(J){let $=this.x,Q=this.y,Z=this.z,W=J.elements,K=1/(W[3]*$+W[7]*Q+W[11]*Z+W[15]);return this.x=(W[0]*$+W[4]*Q+W[8]*Z+W[12])*K,this.y=(W[1]*$+W[5]*Q+W[9]*Z+W[13])*K,this.z=(W[2]*$+W[6]*Q+W[10]*Z+W[14])*K,this}applyQuaternion(J){let $=this.x,Q=this.y,Z=this.z,W=J.x,K=J.y,H=J.z,Y=J.w,X=2*(K*Z-H*Q),N=2*(H*$-W*Z),U=2*(W*Q-K*$);return this.x=$+Y*X+K*U-H*N,this.y=Q+Y*N+H*X-W*U,this.z=Z+Y*U+W*N-K*X,this}project(J){return this.applyMatrix4(J.matrixWorldInverse).applyMatrix4(J.projectionMatrix)}unproject(J){return this.applyMatrix4(J.projectionMatrixInverse).applyMatrix4(J.matrixWorld)}transformDirection(J){let $=this.x,Q=this.y,Z=this.z,W=J.elements;return this.x=W[0]*$+W[4]*Q+W[8]*Z,this.y=W[1]*$+W[5]*Q+W[9]*Z,this.z=W[2]*$+W[6]*Q+W[10]*Z,this.normalize()}divide(J){return this.x/=J.x,this.y/=J.y,this.z/=J.z,this}divideScalar(J){return this.multiplyScalar(1/J)}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this.z=Math.min(this.z,J.z),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this.z=Math.max(this.z,J.z),this}clamp(J,$){return this.x=u0(this.x,J.x,$.x),this.y=u0(this.y,J.y,$.y),this.z=u0(this.z,J.z,$.z),this}clampScalar(J,$){return this.x=u0(this.x,J,$),this.y=u0(this.y,J,$),this.z=u0(this.z,J,$),this}clampLength(J,$){let Q=this.length();return this.divideScalar(Q||1).multiplyScalar(u0(Q,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this.z=Math.floor(this.z),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this.z=Math.ceil(this.z),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this.z=Math.round(this.z),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this.z=Math.trunc(this.z),this}negate(){return this.x=-this.x,this.y=-this.y,this.z=-this.z,this}dot(J){return this.x*J.x+this.y*J.y+this.z*J.z}lengthSq(){return this.x*this.x+this.y*this.y+this.z*this.z}length(){return Math.sqrt(this.x*this.x+this.y*this.y+this.z*this.z)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)+Math.abs(this.z)}normalize(){return this.divideScalar(this.length()||1)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this.z+=(J.z-this.z)*$,this}lerpVectors(J,$,Q){return this.x=J.x+($.x-J.x)*Q,this.y=J.y+($.y-J.y)*Q,this.z=J.z+($.z-J.z)*Q,this}cross(J){return this.crossVectors(this,J)}crossVectors(J,$){let{x:Q,y:Z,z:W}=J,K=$.x,H=$.y,Y=$.z;return this.x=Z*Y-W*H,this.y=W*K-Q*Y,this.z=Q*H-Z*K,this}projectOnVector(J){let $=J.lengthSq();if($===0)return this.set(0,0,0);let Q=J.dot(this)/$;return this.copy(J).multiplyScalar(Q)}projectOnPlane(J){return G7.copy(this).projectOnVector(J),this.sub(G7)}reflect(J){return this.sub(G7.copy(J).multiplyScalar(2*this.dot(J)))}angleTo(J){let $=Math.sqrt(this.lengthSq()*J.lengthSq());if($===0)return Math.PI/2;let Q=this.dot(J)/$;return Math.acos(u0(Q,-1,1))}distanceTo(J){return Math.sqrt(this.distanceToSquared(J))}distanceToSquared(J){let $=this.x-J.x,Q=this.y-J.y,Z=this.z-J.z;return $*$+Q*Q+Z*Z}manhattanDistanceTo(J){return Math.abs(this.x-J.x)+Math.abs(this.y-J.y)+Math.abs(this.z-J.z)}setFromSpherical(J){return this.setFromSphericalCoords(J.radius,J.phi,J.theta)}setFromSphericalCoords(J,$,Q){let Z=Math.sin($)*J;return this.x=Z*Math.sin(Q),this.y=Math.cos($)*J,this.z=Z*Math.cos(Q),this}setFromCylindrical(J){return this.setFromCylindricalCoords(J.radius,J.theta,J.y)}setFromCylindricalCoords(J,$,Q){return this.x=J*Math.sin($),this.y=Q,this.z=J*Math.cos($),this}setFromMatrixPosition(J){let $=J.elements;return this.x=$[12],this.y=$[13],this.z=$[14],this}setFromMatrixScale(J){let $=this.setFromMatrixColumn(J,0).length(),Q=this.setFromMatrixColumn(J,1).length(),Z=this.setFromMatrixColumn(J,2).length();return this.x=$,this.y=Q,this.z=Z,this}setFromMatrixColumn(J,$){return this.fromArray(J.elements,$*4)}setFromMatrix3Column(J,$){return this.fromArray(J.elements,$*3)}setFromEuler(J){return this.x=J._x,this.y=J._y,this.z=J._z,this}setFromColor(J){return this.x=J.r,this.y=J.g,this.z=J.b,this}equals(J){return J.x===this.x&&J.y===this.y&&J.z===this.z}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this.z=J[$+2],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J[$+2]=this.z,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this.z=J.getZ($),this}random(){return this.x=Math.random(),this.y=Math.random(),this.z=Math.random(),this}randomDirection(){let J=Math.random()*Math.PI*2,$=Math.random()*2-1,Q=Math.sqrt(1-$*$);return this.x=Q*Math.cos(J),this.y=$,this.z=Q*Math.sin(J),this}*[Symbol.iterator](){yield this.x,yield this.y,yield this.z}}var G7=new v,kQ=new e9;class p0{constructor(J,$,Q,Z,W,K,H,Y,X){if(p0.prototype.isMatrix3=!0,this.elements=[1,0,0,0,1,0,0,0,1],J!==void 0)this.set(J,$,Q,Z,W,K,H,Y,X)}set(J,$,Q,Z,W,K,H,Y,X){let N=this.elements;return N[0]=J,N[1]=Z,N[2]=H,N[3]=$,N[4]=W,N[5]=Y,N[6]=Q,N[7]=K,N[8]=X,this}identity(){return this.set(1,0,0,0,1,0,0,0,1),this}copy(J){let $=this.elements,Q=J.elements;return $[0]=Q[0],$[1]=Q[1],$[2]=Q[2],$[3]=Q[3],$[4]=Q[4],$[5]=Q[5],$[6]=Q[6],$[7]=Q[7],$[8]=Q[8],this}extractBasis(J,$,Q){return J.setFromMatrix3Column(this,0),$.setFromMatrix3Column(this,1),Q.setFromMatrix3Column(this,2),this}setFromMatrix4(J){let $=J.elements;return this.set($[0],$[4],$[8],$[1],$[5],$[9],$[2],$[6],$[10]),this}multiply(J){return this.multiplyMatrices(this,J)}premultiply(J){return this.multiplyMatrices(J,this)}multiplyMatrices(J,$){let Q=J.elements,Z=$.elements,W=this.elements,K=Q[0],H=Q[3],Y=Q[6],X=Q[1],N=Q[4],U=Q[7],G=Q[2],D=Q[5],R=Q[8],M=Z[0],L=Z[3],E=Z[6],q=Z[1],A=Z[4],V=Z[7],I=Z[2],f=Z[5],P=Z[8];return W[0]=K*M+H*q+Y*I,W[3]=K*L+H*A+Y*f,W[6]=K*E+H*V+Y*P,W[1]=X*M+N*q+U*I,W[4]=X*L+N*A+U*f,W[7]=X*E+N*V+U*P,W[2]=G*M+D*q+R*I,W[5]=G*L+D*A+R*f,W[8]=G*E+D*V+R*P,this}multiplyScalar(J){let $=this.elements;return $[0]*=J,$[3]*=J,$[6]*=J,$[1]*=J,$[4]*=J,$[7]*=J,$[2]*=J,$[5]*=J,$[8]*=J,this}determinant(){let J=this.elements,$=J[0],Q=J[1],Z=J[2],W=J[3],K=J[4],H=J[5],Y=J[6],X=J[7],N=J[8];return $*K*N-$*H*X-Q*W*N+Q*H*Y+Z*W*X-Z*K*Y}invert(){let J=this.elements,$=J[0],Q=J[1],Z=J[2],W=J[3],K=J[4],H=J[5],Y=J[6],X=J[7],N=J[8],U=N*K-H*X,G=H*Y-N*W,D=X*W-K*Y,R=$*U+Q*G+Z*D;if(R===0)return this.set(0,0,0,0,0,0,0,0,0);let M=1/R;return J[0]=U*M,J[1]=(Z*X-N*Q)*M,J[2]=(H*Q-Z*K)*M,J[3]=G*M,J[4]=(N*$-Z*Y)*M,J[5]=(Z*W-H*$)*M,J[6]=D*M,J[7]=(Q*Y-X*$)*M,J[8]=(K*$-Q*W)*M,this}transpose(){let J,$=this.elements;return J=$[1],$[1]=$[3],$[3]=J,J=$[2],$[2]=$[6],$[6]=J,J=$[5],$[5]=$[7],$[7]=J,this}getNormalMatrix(J){return this.setFromMatrix4(J).invert().transpose()}transposeIntoArray(J){let $=this.elements;return J[0]=$[0],J[1]=$[3],J[2]=$[6],J[3]=$[1],J[4]=$[4],J[5]=$[7],J[6]=$[2],J[7]=$[5],J[8]=$[8],this}setUvTransform(J,$,Q,Z,W,K,H){let Y=Math.cos(W),X=Math.sin(W);return this.set(Q*Y,Q*X,-Q*(Y*K+X*H)+K+J,-Z*X,Z*Y,-Z*(-X*K+Y*H)+H+$,0,0,1),this}scale(J,$){return this.premultiply(E7.makeScale(J,$)),this}rotate(J){return this.premultiply(E7.makeRotation(-J)),this}translate(J,$){return this.premultiply(E7.makeTranslation(J,$)),this}makeTranslation(J,$){if(J.isVector2)this.set(1,0,J.x,0,1,J.y,0,0,1);else this.set(1,0,J,0,1,$,0,0,1);return this}makeRotation(J){let $=Math.cos(J),Q=Math.sin(J);return this.set($,-Q,0,Q,$,0,0,0,1),this}makeScale(J,$){return this.set(J,0,0,0,$,0,0,0,1),this}equals(J){let $=this.elements,Q=J.elements;for(let Z=0;Z<9;Z++)if($[Z]!==Q[Z])return!1;return!0}fromArray(J,$=0){for(let Q=0;Q<9;Q++)this.elements[Q]=J[Q+$];return this}toArray(J=[],$=0){let Q=this.elements;return J[$]=Q[0],J[$+1]=Q[1],J[$+2]=Q[2],J[$+3]=Q[3],J[$+4]=Q[4],J[$+5]=Q[5],J[$+6]=Q[6],J[$+7]=Q[7],J[$+8]=Q[8],J}clone(){return new this.constructor().fromArray(this.elements)}}var E7=new p0;function _$(J){for(let $=J.length-1;$>=0;--$)if(J[$]>=65535)return!0;return!1}function v8(J){return document.createElementNS("http://www.w3.org/1999/xhtml",J)}function mZ(){let J=v8("canvas");return J.style.display="block",J}var FQ={};function E8(J){if(J in FQ)return;FQ[J]=!0,console.warn(J)}function uZ(J,$,Q){return new Promise(function(Z,W){function K(){switch(J.clientWaitSync($,J.SYNC_FLUSH_COMMANDS_BIT,0)){case J.WAIT_FAILED:W();break;case J.TIMEOUT_EXPIRED:setTimeout(K,Q);break;default:Z()}}setTimeout(K,Q)})}var MQ=new p0().set(0.4123908,0.3575843,0.1804808,0.212639,0.7151687,0.0721923,0.0193308,0.1191948,0.9505322),LQ=new p0().set(3.2409699,-1.5373832,-0.4986108,-0.9692436,1.8759675,0.0415551,0.0556301,-0.203977,1.0569715);function YK(){let J={enabled:!0,workingColorSpace:"srgb-linear",spaces:{},convert:function(W,K,H){if(this.enabled===!1||K===H||!K||!H)return W;if(this.spaces[K].transfer==="srgb")W.r=OJ(W.r),W.g=OJ(W.g),W.b=OJ(W.b);if(this.spaces[K].primaries!==this.spaces[H].primaries)W.applyMatrix3(this.spaces[K].toXYZ),W.applyMatrix3(this.spaces[H].fromXYZ);if(this.spaces[H].transfer==="srgb")W.r=U8(W.r),W.g=U8(W.g),W.b=U8(W.b);return W},workingToColorSpace:function(W,K){return this.convert(W,this.workingColorSpace,K)},colorSpaceToWorking:function(W,K){return this.convert(W,K,this.workingColorSpace)},getPrimaries:function(W){return this.spaces[W].primaries},getTransfer:function(W){if(W==="")return"linear";return this.spaces[W].transfer},getToneMappingMode:function(W){return this.spaces[W].outputColorSpaceConfig.toneMappingMode||"standard"},getLuminanceCoefficients:function(W,K=this.workingColorSpace){return W.fromArray(this.spaces[K].luminanceCoefficients)},define:function(W){Object.assign(this.spaces,W)},_getMatrix:function(W,K,H){return W.copy(this.spaces[K].toXYZ).multiply(this.spaces[H].fromXYZ)},_getDrawingBufferColorSpace:function(W){return this.spaces[W].outputColorSpaceConfig.drawingBufferColorSpace},_getUnpackColorSpace:function(W=this.workingColorSpace){return this.spaces[W].workingColorSpaceConfig.unpackColorSpace},fromWorkingColorSpace:function(W,K){return E8("THREE.ColorManagement: .fromWorkingColorSpace() has been renamed to .workingToColorSpace()."),J.workingToColorSpace(W,K)},toWorkingColorSpace:function(W,K){return E8("THREE.ColorManagement: .toWorkingColorSpace() has been renamed to .colorSpaceToWorking()."),J.colorSpaceToWorking(W,K)}},$=[0.64,0.33,0.3,0.6,0.15,0.06],Q=[0.2126,0.7152,0.0722],Z=[0.3127,0.329];return J.define({["srgb-linear"]:{primaries:$,whitePoint:Z,transfer:"linear",toXYZ:MQ,fromXYZ:LQ,luminanceCoefficients:Q,workingColorSpaceConfig:{unpackColorSpace:"srgb"},outputColorSpaceConfig:{drawingBufferColorSpace:"srgb"}},["srgb"]:{primaries:$,whitePoint:Z,transfer:"srgb",toXYZ:MQ,fromXYZ:LQ,luminanceCoefficients:Q,outputColorSpaceConfig:{drawingBufferColorSpace:"srgb"}}}),J}var s0=YK();function OJ(J){return J<0.04045?J*0.0773993808:Math.pow(J*0.9478672986+0.0521327014,2.4)}function U8(J){return J<0.0031308?J*12.92:1.055*Math.pow(J,0.41666)-0.055}var rJ;class I${static getDataURL(J,$="image/png"){if(/^data:/i.test(J.src))return J.src;if(typeof HTMLCanvasElement>"u")return J.src;let Q;if(J instanceof HTMLCanvasElement)Q=J;else{if(rJ===void 0)rJ=v8("canvas");rJ.width=J.width,rJ.height=J.height;let Z=rJ.getContext("2d");if(J instanceof ImageData)Z.putImageData(J,0,0);else Z.drawImage(J,0,0,J.width,J.height);Q=rJ}return Q.toDataURL($)}static sRGBToLinear(J){if(typeof HTMLImageElement<"u"&&J instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&J instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&J instanceof ImageBitmap){let $=v8("canvas");$.width=J.width,$.height=J.height;let Q=$.getContext("2d");Q.drawImage(J,0,0,J.width,J.height);let Z=Q.getImageData(0,0,J.width,J.height),W=Z.data;for(let K=0;K<W.length;K++)W[K]=OJ(W[K]/255)*255;return Q.putImageData(Z,0,0),$}else if(J.data){let $=J.data.slice(0);for(let Q=0;Q<$.length;Q++)if($ instanceof Uint8Array||$ instanceof Uint8ClampedArray)$[Q]=Math.floor(OJ($[Q]/255)*255);else $[Q]=OJ($[Q]);return{data:$,width:J.width,height:J.height}}else return console.warn("THREE.ImageUtils.sRGBToLinear(): Unsupported image type. No color space conversion applied."),J}}var HK=0;class c8{constructor(J=null){this.isSource=!0,Object.defineProperty(this,"id",{value:HK++}),this.uuid=L8(),this.data=J,this.dataReady=!0,this.version=0}getSize(J){let $=this.data;if(typeof HTMLVideoElement<"u"&&$ instanceof HTMLVideoElement)J.set($.videoWidth,$.videoHeight,0);else if($ instanceof VideoFrame)J.set($.displayHeight,$.displayWidth,0);else if($!==null)J.set($.width,$.height,$.depth||0);else J.set(0,0,0);return J}set needsUpdate(J){if(J===!0)this.version++}toJSON(J){let $=J===void 0||typeof J==="string";if(!$&&J.images[this.uuid]!==void 0)return J.images[this.uuid];let Q={uuid:this.uuid,url:""},Z=this.data;if(Z!==null){let W;if(Array.isArray(Z)){W=[];for(let K=0,H=Z.length;K<H;K++)if(Z[K].isDataTexture)W.push(D7(Z[K].image));else W.push(D7(Z[K]))}else W=D7(Z);Q.url=W}if(!$)J.images[this.uuid]=Q;return Q}}function D7(J){if(typeof HTMLImageElement<"u"&&J instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&J instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&J instanceof ImageBitmap)return I$.getDataURL(J);else if(J.data)return{data:Array.from(J.data),width:J.width,height:J.height,type:J.data.constructor.name};else return console.warn("THREE.Texture: Unable to serialize Texture."),{}}var XK=0,q7=new v;class P9 extends kJ{constructor(J=P9.DEFAULT_IMAGE,$=P9.DEFAULT_MAPPING,Q=1001,Z=1001,W=1006,K=1008,H=1023,Y=1009,X=P9.DEFAULT_ANISOTROPY,N=""){super();this.isTexture=!0,Object.defineProperty(this,"id",{value:XK++}),this.uuid=L8(),this.name="",this.source=new c8(J),this.mipmaps=[],this.mapping=$,this.channel=0,this.wrapS=Q,this.wrapT=Z,this.magFilter=W,this.minFilter=K,this.anisotropy=X,this.format=H,this.internalFormat=null,this.type=Y,this.offset=new x0(0,0),this.repeat=new x0(1,1),this.center=new x0(0,0),this.rotation=0,this.matrixAutoUpdate=!0,this.matrix=new p0,this.generateMipmaps=!0,this.premultiplyAlpha=!1,this.flipY=!0,this.unpackAlignment=4,this.colorSpace=N,this.userData={},this.updateRanges=[],this.version=0,this.onUpdate=null,this.renderTarget=null,this.isRenderTargetTexture=!1,this.isArrayTexture=J&&J.depth&&J.depth>1?!0:!1,this.pmremVersion=0}get width(){return this.source.getSize(q7).x}get height(){return this.source.getSize(q7).y}get depth(){return this.source.getSize(q7).z}get image(){return this.source.data}set image(J=null){this.source.data=J}updateMatrix(){this.matrix.setUvTransform(this.offset.x,this.offset.y,this.repeat.x,this.repeat.y,this.rotation,this.center.x,this.center.y)}addUpdateRange(J,$){this.updateRanges.push({start:J,count:$})}clearUpdateRanges(){this.updateRanges.length=0}clone(){return new this.constructor().copy(this)}copy(J){return this.name=J.name,this.source=J.source,this.mipmaps=J.mipmaps.slice(0),this.mapping=J.mapping,this.channel=J.channel,this.wrapS=J.wrapS,this.wrapT=J.wrapT,this.magFilter=J.magFilter,this.minFilter=J.minFilter,this.anisotropy=J.anisotropy,this.format=J.format,this.internalFormat=J.internalFormat,this.type=J.type,this.offset.copy(J.offset),this.repeat.copy(J.repeat),this.center.copy(J.center),this.rotation=J.rotation,this.matrixAutoUpdate=J.matrixAutoUpdate,this.matrix.copy(J.matrix),this.generateMipmaps=J.generateMipmaps,this.premultiplyAlpha=J.premultiplyAlpha,this.flipY=J.flipY,this.unpackAlignment=J.unpackAlignment,this.colorSpace=J.colorSpace,this.renderTarget=J.renderTarget,this.isRenderTargetTexture=J.isRenderTargetTexture,this.isArrayTexture=J.isArrayTexture,this.userData=JSON.parse(JSON.stringify(J.userData)),this.needsUpdate=!0,this}setValues(J){for(let $ in J){let Q=J[$];if(Q===void 0){console.warn(`THREE.Texture.setValues(): parameter '${$}' has value of undefined.`);continue}let Z=this[$];if(Z===void 0){console.warn(`THREE.Texture.setValues(): property '${$}' does not exist.`);continue}if(Z&&Q&&(Z.isVector2&&Q.isVector2))Z.copy(Q);else if(Z&&Q&&(Z.isVector3&&Q.isVector3))Z.copy(Q);else if(Z&&Q&&(Z.isMatrix3&&Q.isMatrix3))Z.copy(Q);else this[$]=Q}}toJSON(J){let $=J===void 0||typeof J==="string";if(!$&&J.textures[this.uuid]!==void 0)return J.textures[this.uuid];let Q={metadata:{version:4.7,type:"Texture",generator:"Texture.toJSON"},uuid:this.uuid,name:this.name,image:this.source.toJSON(J).uuid,mapping:this.mapping,channel:this.channel,repeat:[this.repeat.x,this.repeat.y],offset:[this.offset.x,this.offset.y],center:[this.center.x,this.center.y],rotation:this.rotation,wrap:[this.wrapS,this.wrapT],format:this.format,internalFormat:this.internalFormat,type:this.type,colorSpace:this.colorSpace,minFilter:this.minFilter,magFilter:this.magFilter,anisotropy:this.anisotropy,flipY:this.flipY,generateMipmaps:this.generateMipmaps,premultiplyAlpha:this.premultiplyAlpha,unpackAlignment:this.unpackAlignment};if(Object.keys(this.userData).length>0)Q.userData=this.userData;if(!$)J.textures[this.uuid]=Q;return Q}dispose(){this.dispatchEvent({type:"dispose"})}transformUv(J){if(this.mapping!==300)return J;if(J.applyMatrix3(this.matrix),J.x<0||J.x>1)switch(this.wrapS){case 1000:J.x=J.x-Math.floor(J.x);break;case 1001:J.x=J.x<0?0:1;break;case 1002:if(Math.abs(Math.floor(J.x)%2)===1)J.x=Math.ceil(J.x)-J.x;else J.x=J.x-Math.floor(J.x);break}if(J.y<0||J.y>1)switch(this.wrapT){case 1000:J.y=J.y-Math.floor(J.y);break;case 1001:J.y=J.y<0?0:1;break;case 1002:if(Math.abs(Math.floor(J.y)%2)===1)J.y=Math.ceil(J.y)-J.y;else J.y=J.y-Math.floor(J.y);break}if(this.flipY)J.y=1-J.y;return J}set needsUpdate(J){if(J===!0)this.version++,this.source.needsUpdate=!0}set needsPMREMUpdate(J){if(J===!0)this.pmremVersion++}}P9.DEFAULT_IMAGE=null;P9.DEFAULT_MAPPING=300;P9.DEFAULT_ANISOTROPY=1;class G9{constructor(J=0,$=0,Q=0,Z=1){G9.prototype.isVector4=!0,this.x=J,this.y=$,this.z=Q,this.w=Z}get width(){return this.z}set width(J){this.z=J}get height(){return this.w}set height(J){this.w=J}set(J,$,Q,Z){return this.x=J,this.y=$,this.z=Q,this.w=Z,this}setScalar(J){return this.x=J,this.y=J,this.z=J,this.w=J,this}setX(J){return this.x=J,this}setY(J){return this.y=J,this}setZ(J){return this.z=J,this}setW(J){return this.w=J,this}setComponent(J,$){switch(J){case 0:this.x=$;break;case 1:this.y=$;break;case 2:this.z=$;break;case 3:this.w=$;break;default:throw Error("index is out of range: "+J)}return this}getComponent(J){switch(J){case 0:return this.x;case 1:return this.y;case 2:return this.z;case 3:return this.w;default:throw Error("index is out of range: "+J)}}clone(){return new this.constructor(this.x,this.y,this.z,this.w)}copy(J){return this.x=J.x,this.y=J.y,this.z=J.z,this.w=J.w!==void 0?J.w:1,this}add(J){return this.x+=J.x,this.y+=J.y,this.z+=J.z,this.w+=J.w,this}addScalar(J){return this.x+=J,this.y+=J,this.z+=J,this.w+=J,this}addVectors(J,$){return this.x=J.x+$.x,this.y=J.y+$.y,this.z=J.z+$.z,this.w=J.w+$.w,this}addScaledVector(J,$){return this.x+=J.x*$,this.y+=J.y*$,this.z+=J.z*$,this.w+=J.w*$,this}sub(J){return this.x-=J.x,this.y-=J.y,this.z-=J.z,this.w-=J.w,this}subScalar(J){return this.x-=J,this.y-=J,this.z-=J,this.w-=J,this}subVectors(J,$){return this.x=J.x-$.x,this.y=J.y-$.y,this.z=J.z-$.z,this.w=J.w-$.w,this}multiply(J){return this.x*=J.x,this.y*=J.y,this.z*=J.z,this.w*=J.w,this}multiplyScalar(J){return this.x*=J,this.y*=J,this.z*=J,this.w*=J,this}applyMatrix4(J){let $=this.x,Q=this.y,Z=this.z,W=this.w,K=J.elements;return this.x=K[0]*$+K[4]*Q+K[8]*Z+K[12]*W,this.y=K[1]*$+K[5]*Q+K[9]*Z+K[13]*W,this.z=K[2]*$+K[6]*Q+K[10]*Z+K[14]*W,this.w=K[3]*$+K[7]*Q+K[11]*Z+K[15]*W,this}divide(J){return this.x/=J.x,this.y/=J.y,this.z/=J.z,this.w/=J.w,this}divideScalar(J){return this.multiplyScalar(1/J)}setAxisAngleFromQuaternion(J){this.w=2*Math.acos(J.w);let $=Math.sqrt(1-J.w*J.w);if($<0.0001)this.x=1,this.y=0,this.z=0;else this.x=J.x/$,this.y=J.y/$,this.z=J.z/$;return this}setAxisAngleFromRotationMatrix(J){let $,Q,Z,W,K=0.01,H=0.1,Y=J.elements,X=Y[0],N=Y[4],U=Y[8],G=Y[1],D=Y[5],R=Y[9],M=Y[2],L=Y[6],E=Y[10];if(Math.abs(N-G)<0.01&&Math.abs(U-M)<0.01&&Math.abs(R-L)<0.01){if(Math.abs(N+G)<0.1&&Math.abs(U+M)<0.1&&Math.abs(R+L)<0.1&&Math.abs(X+D+E-3)<0.1)return this.set(1,0,0,0),this;$=Math.PI;let A=(X+1)/2,V=(D+1)/2,I=(E+1)/2,f=(N+G)/4,P=(U+M)/4,S=(R+L)/4;if(A>V&&A>I)if(A<0.01)Q=0,Z=0.707106781,W=0.707106781;else Q=Math.sqrt(A),Z=f/Q,W=P/Q;else if(V>I)if(V<0.01)Q=0.707106781,Z=0,W=0.707106781;else Z=Math.sqrt(V),Q=f/Z,W=S/Z;else if(I<0.01)Q=0.707106781,Z=0.707106781,W=0;else W=Math.sqrt(I),Q=P/W,Z=S/W;return this.set(Q,Z,W,$),this}let q=Math.sqrt((L-R)*(L-R)+(U-M)*(U-M)+(G-N)*(G-N));if(Math.abs(q)<0.001)q=1;return this.x=(L-R)/q,this.y=(U-M)/q,this.z=(G-N)/q,this.w=Math.acos((X+D+E-1)/2),this}setFromMatrixPosition(J){let $=J.elements;return this.x=$[12],this.y=$[13],this.z=$[14],this.w=$[15],this}min(J){return this.x=Math.min(this.x,J.x),this.y=Math.min(this.y,J.y),this.z=Math.min(this.z,J.z),this.w=Math.min(this.w,J.w),this}max(J){return this.x=Math.max(this.x,J.x),this.y=Math.max(this.y,J.y),this.z=Math.max(this.z,J.z),this.w=Math.max(this.w,J.w),this}clamp(J,$){return this.x=u0(this.x,J.x,$.x),this.y=u0(this.y,J.y,$.y),this.z=u0(this.z,J.z,$.z),this.w=u0(this.w,J.w,$.w),this}clampScalar(J,$){return this.x=u0(this.x,J,$),this.y=u0(this.y,J,$),this.z=u0(this.z,J,$),this.w=u0(this.w,J,$),this}clampLength(J,$){let Q=this.length();return this.divideScalar(Q||1).multiplyScalar(u0(Q,J,$))}floor(){return this.x=Math.floor(this.x),this.y=Math.floor(this.y),this.z=Math.floor(this.z),this.w=Math.floor(this.w),this}ceil(){return this.x=Math.ceil(this.x),this.y=Math.ceil(this.y),this.z=Math.ceil(this.z),this.w=Math.ceil(this.w),this}round(){return this.x=Math.round(this.x),this.y=Math.round(this.y),this.z=Math.round(this.z),this.w=Math.round(this.w),this}roundToZero(){return this.x=Math.trunc(this.x),this.y=Math.trunc(this.y),this.z=Math.trunc(this.z),this.w=Math.trunc(this.w),this}negate(){return this.x=-this.x,this.y=-this.y,this.z=-this.z,this.w=-this.w,this}dot(J){return this.x*J.x+this.y*J.y+this.z*J.z+this.w*J.w}lengthSq(){return this.x*this.x+this.y*this.y+this.z*this.z+this.w*this.w}length(){return Math.sqrt(this.x*this.x+this.y*this.y+this.z*this.z+this.w*this.w)}manhattanLength(){return Math.abs(this.x)+Math.abs(this.y)+Math.abs(this.z)+Math.abs(this.w)}normalize(){return this.divideScalar(this.length()||1)}setLength(J){return this.normalize().multiplyScalar(J)}lerp(J,$){return this.x+=(J.x-this.x)*$,this.y+=(J.y-this.y)*$,this.z+=(J.z-this.z)*$,this.w+=(J.w-this.w)*$,this}lerpVectors(J,$,Q){return this.x=J.x+($.x-J.x)*Q,this.y=J.y+($.y-J.y)*Q,this.z=J.z+($.z-J.z)*Q,this.w=J.w+($.w-J.w)*Q,this}equals(J){return J.x===this.x&&J.y===this.y&&J.z===this.z&&J.w===this.w}fromArray(J,$=0){return this.x=J[$],this.y=J[$+1],this.z=J[$+2],this.w=J[$+3],this}toArray(J=[],$=0){return J[$]=this.x,J[$+1]=this.y,J[$+2]=this.z,J[$+3]=this.w,J}fromBufferAttribute(J,$){return this.x=J.getX($),this.y=J.getY($),this.z=J.getZ($),this.w=J.getW($),this}random(){return this.x=Math.random(),this.y=Math.random(),this.z=Math.random(),this.w=Math.random(),this}*[Symbol.iterator](){yield this.x,yield this.y,yield this.z,yield this.w}}class P$ extends kJ{constructor(J=1,$=1,Q={}){super();Q=Object.assign({generateMipmaps:!1,internalFormat:null,minFilter:1006,depthBuffer:!0,stencilBuffer:!1,resolveDepthBuffer:!0,resolveStencilBuffer:!0,depthTexture:null,samples:0,count:1,depth:1,multiview:!1},Q),this.isRenderTarget=!0,this.width=J,this.height=$,this.depth=Q.depth,this.scissor=new G9(0,0,J,$),this.scissorTest=!1,this.viewport=new G9(0,0,J,$);let Z={width:J,height:$,depth:Q.depth},W=new P9(Z);this.textures=[];let K=Q.count;for(let H=0;H<K;H++)this.textures[H]=W.clone(),this.textures[H].isRenderTargetTexture=!0,this.textures[H].renderTarget=this;this._setTextureOptions(Q),this.depthBuffer=Q.depthBuffer,this.stencilBuffer=Q.stencilBuffer,this.resolveDepthBuffer=Q.resolveDepthBuffer,this.resolveStencilBuffer=Q.resolveStencilBuffer,this._depthTexture=null,this.depthTexture=Q.depthTexture,this.samples=Q.samples,this.multiview=Q.multiview}_setTextureOptions(J={}){let $={minFilter:1006,generateMipmaps:!1,flipY:!1,internalFormat:null};if(J.mapping!==void 0)$.mapping=J.mapping;if(J.wrapS!==void 0)$.wrapS=J.wrapS;if(J.wrapT!==void 0)$.wrapT=J.wrapT;if(J.wrapR!==void 0)$.wrapR=J.wrapR;if(J.magFilter!==void 0)$.magFilter=J.magFilter;if(J.minFilter!==void 0)$.minFilter=J.minFilter;if(J.format!==void 0)$.format=J.format;if(J.type!==void 0)$.type=J.type;if(J.anisotropy!==void 0)$.anisotropy=J.anisotropy;if(J.colorSpace!==void 0)$.colorSpace=J.colorSpace;if(J.flipY!==void 0)$.flipY=J.flipY;if(J.generateMipmaps!==void 0)$.generateMipmaps=J.generateMipmaps;if(J.internalFormat!==void 0)$.internalFormat=J.internalFormat;for(let Q=0;Q<this.textures.length;Q++)this.textures[Q].setValues($)}get texture(){return this.textures[0]}set texture(J){this.textures[0]=J}set depthTexture(J){if(this._depthTexture!==null)this._depthTexture.renderTarget=null;if(J!==null)J.renderTarget=this;this._depthTexture=J}get depthTexture(){return this._depthTexture}setSize(J,$,Q=1){if(this.width!==J||this.height!==$||this.depth!==Q){this.width=J,this.height=$,this.depth=Q;for(let Z=0,W=this.textures.length;Z<W;Z++)this.textures[Z].image.width=J,this.textures[Z].image.height=$,this.textures[Z].image.depth=Q,this.textures[Z].isArrayTexture=this.textures[Z].image.depth>1;this.dispose()}this.viewport.set(0,0,J,$),this.scissor.set(0,0,J,$)}clone(){return new this.constructor().copy(this)}copy(J){this.width=J.width,this.height=J.height,this.depth=J.depth,this.scissor.copy(J.scissor),this.scissorTest=J.scissorTest,this.viewport.copy(J.viewport),this.textures.length=0;for(let $=0,Q=J.textures.length;$<Q;$++){this.textures[$]=J.textures[$].clone(),this.textures[$].isRenderTargetTexture=!0,this.textures[$].renderTarget=this;let Z=Object.assign({},J.textures[$].image);this.textures[$].source=new c8(Z)}if(this.depthBuffer=J.depthBuffer,this.stencilBuffer=J.stencilBuffer,this.resolveDepthBuffer=J.resolveDepthBuffer,this.resolveStencilBuffer=J.resolveStencilBuffer,J.depthTexture!==null)this.depthTexture=J.depthTexture.clone();return this.samples=J.samples,this}dispose(){this.dispatchEvent({type:"dispose"})}}class FJ extends P${constructor(J=1,$=1,Q={}){super(J,$,Q);this.isWebGLRenderTarget=!0}}class g6 extends P9{constructor(J=null,$=1,Q=1,Z=1){super(null);this.isDataArrayTexture=!0,this.image={data:J,width:$,height:Q,depth:Z},this.magFilter=1003,this.minFilter=1003,this.wrapR=1001,this.generateMipmaps=!1,this.flipY=!1,this.unpackAlignment=1,this.layerUpdates=new Set}addLayerUpdate(J){this.layerUpdates.add(J)}clearLayerUpdates(){this.layerUpdates.clear()}}class A$ extends P9{constructor(J=null,$=1,Q=1,Z=1){super(null);this.isData3DTexture=!0,this.image={data:J,width:$,height:Q,depth:Z},this.magFilter=1003,this.minFilter=1003,this.wrapR=1001,this.generateMipmaps=!1,this.flipY=!1,this.unpackAlignment=1}}class WJ{constructor(J=new v(1/0,1/0,1/0),$=new v(-1/0,-1/0,-1/0)){this.isBox3=!0,this.min=J,this.max=$}set(J,$){return this.min.copy(J),this.max.copy($),this}setFromArray(J){this.makeEmpty();for(let $=0,Q=J.length;$<Q;$+=3)this.expandByPoint(o9.fromArray(J,$));return this}setFromBufferAttribute(J){this.makeEmpty();for(let $=0,Q=J.count;$<Q;$++)this.expandByPoint(o9.fromBufferAttribute(J,$));return this}setFromPoints(J){this.makeEmpty();for(let $=0,Q=J.length;$<Q;$++)this.expandByPoint(J[$]);return this}setFromCenterAndSize(J,$){let Q=o9.copy($).multiplyScalar(0.5);return this.min.copy(J).sub(Q),this.max.copy(J).add(Q),this}setFromObject(J,$=!1){return this.makeEmpty(),this.expandByObject(J,$)}clone(){return new this.constructor().copy(this)}copy(J){return this.min.copy(J.min),this.max.copy(J.max),this}makeEmpty(){return this.min.x=this.min.y=this.min.z=1/0,this.max.x=this.max.y=this.max.z=-1/0,this}isEmpty(){return this.max.x<this.min.x||this.max.y<this.min.y||this.max.z<this.min.z}getCenter(J){return this.isEmpty()?J.set(0,0,0):J.addVectors(this.min,this.max).multiplyScalar(0.5)}getSize(J){return this.isEmpty()?J.set(0,0,0):J.subVectors(this.max,this.min)}expandByPoint(J){return this.min.min(J),this.max.max(J),this}expandByVector(J){return this.min.sub(J),this.max.add(J),this}expandByScalar(J){return this.min.addScalar(-J),this.max.addScalar(J),this}expandByObject(J,$=!1){J.updateWorldMatrix(!1,!1);let Q=J.geometry;if(Q!==void 0){let W=Q.getAttribute("position");if($===!0&&W!==void 0&&J.isInstancedMesh!==!0)for(let K=0,H=W.count;K<H;K++){if(J.isMesh===!0)J.getVertexPosition(K,o9);else o9.fromBufferAttribute(W,K);o9.applyMatrix4(J.matrixWorld),this.expandByPoint(o9)}else{if(J.boundingBox!==void 0){if(J.boundingBox===null)J.computeBoundingBox();Q6.copy(J.boundingBox)}else{if(Q.boundingBox===null)Q.computeBoundingBox();Q6.copy(Q.boundingBox)}Q6.applyMatrix4(J.matrixWorld),this.union(Q6)}}let Z=J.children;for(let W=0,K=Z.length;W<K;W++)this.expandByObject(Z[W],$);return this}containsPoint(J){return J.x>=this.min.x&&J.x<=this.max.x&&J.y>=this.min.y&&J.y<=this.max.y&&J.z>=this.min.z&&J.z<=this.max.z}containsBox(J){return this.min.x<=J.min.x&&J.max.x<=this.max.x&&this.min.y<=J.min.y&&J.max.y<=this.max.y&&this.min.z<=J.min.z&&J.max.z<=this.max.z}getParameter(J,$){return $.set((J.x-this.min.x)/(this.max.x-this.min.x),(J.y-this.min.y)/(this.max.y-this.min.y),(J.z-this.min.z)/(this.max.z-this.min.z))}intersectsBox(J){return J.max.x>=this.min.x&&J.min.x<=this.max.x&&J.max.y>=this.min.y&&J.min.y<=this.max.y&&J.max.z>=this.min.z&&J.min.z<=this.max.z}intersectsSphere(J){return this.clampPoint(J.center,o9),o9.distanceToSquared(J.center)<=J.radius*J.radius}intersectsPlane(J){let $,Q;if(J.normal.x>0)$=J.normal.x*this.min.x,Q=J.normal.x*this.max.x;else $=J.normal.x*this.max.x,Q=J.normal.x*this.min.x;if(J.normal.y>0)$+=J.normal.y*this.min.y,Q+=J.normal.y*this.max.y;else $+=J.normal.y*this.max.y,Q+=J.normal.y*this.min.y;if(J.normal.z>0)$+=J.normal.z*this.min.z,Q+=J.normal.z*this.max.z;else $+=J.normal.z*this.max.z,Q+=J.normal.z*this.min.z;return $<=-J.constant&&Q>=-J.constant}intersectsTriangle(J){if(this.isEmpty())return!1;this.getCenter(A8),Z6.subVectors(this.max,A8),tJ.subVectors(J.a,A8),eJ.subVectors(J.b,A8),J8.subVectors(J.c,A8),MJ.subVectors(eJ,tJ),LJ.subVectors(J8,eJ),yJ.subVectors(tJ,J8);let $=[0,-MJ.z,MJ.y,0,-LJ.z,LJ.y,0,-yJ.z,yJ.y,MJ.z,0,-MJ.x,LJ.z,0,-LJ.x,yJ.z,0,-yJ.x,-MJ.y,MJ.x,0,-LJ.y,LJ.x,0,-yJ.y,yJ.x,0];if(!O7($,tJ,eJ,J8,Z6))return!1;if($=[1,0,0,0,1,0,0,0,1],!O7($,tJ,eJ,J8,Z6))return!1;return W6.crossVectors(MJ,LJ),$=[W6.x,W6.y,W6.z],O7($,tJ,eJ,J8,Z6)}clampPoint(J,$){return $.copy(J).clamp(this.min,this.max)}distanceToPoint(J){return this.clampPoint(J,o9).distanceTo(J)}getBoundingSphere(J){if(this.isEmpty())J.makeEmpty();else this.getCenter(J.center),J.radius=this.getSize(o9).length()*0.5;return J}intersect(J){if(this.min.max(J.min),this.max.min(J.max),this.isEmpty())this.makeEmpty();return this}union(J){return this.min.min(J.min),this.max.max(J.max),this}applyMatrix4(J){if(this.isEmpty())return this;return NJ[0].set(this.min.x,this.min.y,this.min.z).applyMatrix4(J),NJ[1].set(this.min.x,this.min.y,this.max.z).applyMatrix4(J),NJ[2].set(this.min.x,this.max.y,this.min.z).applyMatrix4(J),NJ[3].set(this.min.x,this.max.y,this.max.z).applyMatrix4(J),NJ[4].set(this.max.x,this.min.y,this.min.z).applyMatrix4(J),NJ[5].set(this.max.x,this.min.y,this.max.z).applyMatrix4(J),NJ[6].set(this.max.x,this.max.y,this.min.z).applyMatrix4(J),NJ[7].set(this.max.x,this.max.y,this.max.z).applyMatrix4(J),this.setFromPoints(NJ),this}translate(J){return this.min.add(J),this.max.add(J),this}equals(J){return J.min.equals(this.min)&&J.max.equals(this.max)}toJSON(){return{min:this.min.toArray(),max:this.max.toArray()}}fromJSON(J){return this.min.fromArray(J.min),this.max.fromArray(J.max),this}}var NJ=[new v,new v,new v,new v,new v,new v,new v,new v],o9=new v,Q6=new WJ,tJ=new v,eJ=new v,J8=new v,MJ=new v,LJ=new v,yJ=new v,A8=new v,Z6=new v,W6=new v,bJ=new v;function O7(J,$,Q,Z,W){for(let K=0,H=J.length-3;K<=H;K+=3){bJ.fromArray(J,K);let Y=W.x*Math.abs(bJ.x)+W.y*Math.abs(bJ.y)+W.z*Math.abs(bJ.z),X=$.dot(bJ),N=Q.dot(bJ),U=Z.dot(bJ);if(Math.max(-Math.max(X,N,U),Math.min(X,N,U))>Y)return!1}return!0}var NK=new WJ,T8=new v,R7=new v;class V8{constructor(J=new v,$=-1){this.isSphere=!0,this.center=J,this.radius=$}set(J,$){return this.center.copy(J),this.radius=$,this}setFromPoints(J,$){let Q=this.center;if($!==void 0)Q.copy($);else NK.setFromPoints(J).getCenter(Q);let Z=0;for(let W=0,K=J.length;W<K;W++)Z=Math.max(Z,Q.distanceToSquared(J[W]));return this.radius=Math.sqrt(Z),this}copy(J){return this.center.copy(J.center),this.radius=J.radius,this}isEmpty(){return this.radius<0}makeEmpty(){return this.center.set(0,0,0),this.radius=-1,this}containsPoint(J){return J.distanceToSquared(this.center)<=this.radius*this.radius}distanceToPoint(J){return J.distanceTo(this.center)-this.radius}intersectsSphere(J){let $=this.radius+J.radius;return J.center.distanceToSquared(this.center)<=$*$}intersectsBox(J){return J.intersectsSphere(this)}intersectsPlane(J){return Math.abs(J.distanceToPoint(this.center))<=this.radius}clampPoint(J,$){let Q=this.center.distanceToSquared(J);if($.copy(J),Q>this.radius*this.radius)$.sub(this.center).normalize(),$.multiplyScalar(this.radius).add(this.center);return $}getBoundingBox(J){if(this.isEmpty())return J.makeEmpty(),J;return J.set(this.center,this.center),J.expandByScalar(this.radius),J}applyMatrix4(J){return this.center.applyMatrix4(J),this.radius=this.radius*J.getMaxScaleOnAxis(),this}translate(J){return this.center.add(J),this}expandByPoint(J){if(this.isEmpty())return this.center.copy(J),this.radius=0,this;T8.subVectors(J,this.center);let $=T8.lengthSq();if($>this.radius*this.radius){let Q=Math.sqrt($),Z=(Q-this.radius)*0.5;this.center.addScaledVector(T8,Z/Q),this.radius+=Z}return this}union(J){if(J.isEmpty())return this;if(this.isEmpty())return this.copy(J),this;if(this.center.equals(J.center)===!0)this.radius=Math.max(this.radius,J.radius);else R7.subVectors(J.center,this.center).setLength(J.radius),this.expandByPoint(T8.copy(J.center).add(R7)),this.expandByPoint(T8.copy(J.center).sub(R7));return this}equals(J){return J.center.equals(this.center)&&J.radius===this.radius}clone(){return new this.constructor().copy(this)}toJSON(){return{radius:this.radius,center:this.center.toArray()}}fromJSON(J){return this.radius=J.radius,this.center.fromArray(J.center),this}}var UJ=new v,k7=new v,K6=new v,VJ=new v,F7=new v,Y6=new v,M7=new v;class pJ{constructor(J=new v,$=new v(0,0,-1)){this.origin=J,this.direction=$}set(J,$){return this.origin.copy(J),this.direction.copy($),this}copy(J){return this.origin.copy(J.origin),this.direction.copy(J.direction),this}at(J,$){return $.copy(this.origin).addScaledVector(this.direction,J)}lookAt(J){return this.direction.copy(J).sub(this.origin).normalize(),this}recast(J){return this.origin.copy(this.at(J,UJ)),this}closestPointToPoint(J,$){$.subVectors(J,this.origin);let Q=$.dot(this.direction);if(Q<0)return $.copy(this.origin);return $.copy(this.origin).addScaledVector(this.direction,Q)}distanceToPoint(J){return Math.sqrt(this.distanceSqToPoint(J))}distanceSqToPoint(J){let $=UJ.subVectors(J,this.origin).dot(this.direction);if($<0)return this.origin.distanceToSquared(J);return UJ.copy(this.origin).addScaledVector(this.direction,$),UJ.distanceToSquared(J)}distanceSqToSegment(J,$,Q,Z){k7.copy(J).add($).multiplyScalar(0.5),K6.copy($).sub(J).normalize(),VJ.copy(this.origin).sub(k7);let W=J.distanceTo($)*0.5,K=-this.direction.dot(K6),H=VJ.dot(this.direction),Y=-VJ.dot(K6),X=VJ.lengthSq(),N=Math.abs(1-K*K),U,G,D,R;if(N>0)if(U=K*Y-H,G=K*H-Y,R=W*N,U>=0)if(G>=-R)if(G<=R){let M=1/N;U*=M,G*=M,D=U*(U+K*G+2*H)+G*(K*U+G+2*Y)+X}else G=W,U=Math.max(0,-(K*G+H)),D=-U*U+G*(G+2*Y)+X;else G=-W,U=Math.max(0,-(K*G+H)),D=-U*U+G*(G+2*Y)+X;else if(G<=-R)U=Math.max(0,-(-K*W+H)),G=U>0?-W:Math.min(Math.max(-W,-Y),W),D=-U*U+G*(G+2*Y)+X;else if(G<=R)U=0,G=Math.min(Math.max(-W,-Y),W),D=G*(G+2*Y)+X;else U=Math.max(0,-(K*W+H)),G=U>0?W:Math.min(Math.max(-W,-Y),W),D=-U*U+G*(G+2*Y)+X;else G=K>0?-W:W,U=Math.max(0,-(K*G+H)),D=-U*U+G*(G+2*Y)+X;if(Q)Q.copy(this.origin).addScaledVector(this.direction,U);if(Z)Z.copy(k7).addScaledVector(K6,G);return D}intersectSphere(J,$){UJ.subVectors(J.center,this.origin);let Q=UJ.dot(this.direction),Z=UJ.dot(UJ)-Q*Q,W=J.radius*J.radius;if(Z>W)return null;let K=Math.sqrt(W-Z),H=Q-K,Y=Q+K;if(Y<0)return null;if(H<0)return this.at(Y,$);return this.at(H,$)}intersectsSphere(J){if(J.radius<0)return!1;return this.distanceSqToPoint(J.center)<=J.radius*J.radius}distanceToPlane(J){let $=J.normal.dot(this.direction);if($===0){if(J.distanceToPoint(this.origin)===0)return 0;return null}let Q=-(this.origin.dot(J.normal)+J.constant)/$;return Q>=0?Q:null}intersectPlane(J,$){let Q=this.distanceToPlane(J);if(Q===null)return null;return this.at(Q,$)}intersectsPlane(J){let $=J.distanceToPoint(this.origin);if($===0)return!0;if(J.normal.dot(this.direction)*$<0)return!0;return!1}intersectBox(J,$){let Q,Z,W,K,H,Y,X=1/this.direction.x,N=1/this.direction.y,U=1/this.direction.z,G=this.origin;if(X>=0)Q=(J.min.x-G.x)*X,Z=(J.max.x-G.x)*X;else Q=(J.max.x-G.x)*X,Z=(J.min.x-G.x)*X;if(N>=0)W=(J.min.y-G.y)*N,K=(J.max.y-G.y)*N;else W=(J.max.y-G.y)*N,K=(J.min.y-G.y)*N;if(Q>K||W>Z)return null;if(W>Q||isNaN(Q))Q=W;if(K<Z||isNaN(Z))Z=K;if(U>=0)H=(J.min.z-G.z)*U,Y=(J.max.z-G.z)*U;else H=(J.max.z-G.z)*U,Y=(J.min.z-G.z)*U;if(Q>Y||H>Z)return null;if(H>Q||Q!==Q)Q=H;if(Y<Z||Z!==Z)Z=Y;if(Z<0)return null;return this.at(Q>=0?Q:Z,$)}intersectsBox(J){return this.intersectBox(J,UJ)!==null}intersectTriangle(J,$,Q,Z,W){F7.subVectors($,J),Y6.subVectors(Q,J),M7.crossVectors(F7,Y6);let K=this.direction.dot(M7),H;if(K>0){if(Z)return null;H=1}else if(K<0)H=-1,K=-K;else return null;VJ.subVectors(this.origin,J);let Y=H*this.direction.dot(Y6.crossVectors(VJ,Y6));if(Y<0)return null;let X=H*this.direction.dot(F7.cross(VJ));if(X<0)return null;if(Y+X>K)return null;let N=-H*VJ.dot(M7);if(N<0)return null;return this.at(N/K,W)}applyMatrix4(J){return this.origin.applyMatrix4(J),this.direction.transformDirection(J),this}equals(J){return J.origin.equals(this.origin)&&J.direction.equals(this.direction)}clone(){return new this.constructor().copy(this)}}class X9{constructor(J,$,Q,Z,W,K,H,Y,X,N,U,G,D,R,M,L){if(X9.prototype.isMatrix4=!0,this.elements=[1,0,0,0,0,1,0,0,0,0,1,0,0,0,0,1],J!==void 0)this.set(J,$,Q,Z,W,K,H,Y,X,N,U,G,D,R,M,L)}set(J,$,Q,Z,W,K,H,Y,X,N,U,G,D,R,M,L){let E=this.elements;return E[0]=J,E[4]=$,E[8]=Q,E[12]=Z,E[1]=W,E[5]=K,E[9]=H,E[13]=Y,E[2]=X,E[6]=N,E[10]=U,E[14]=G,E[3]=D,E[7]=R,E[11]=M,E[15]=L,this}identity(){return this.set(1,0,0,0,0,1,0,0,0,0,1,0,0,0,0,1),this}clone(){return new X9().fromArray(this.elements)}copy(J){let $=this.elements,Q=J.elements;return $[0]=Q[0],$[1]=Q[1],$[2]=Q[2],$[3]=Q[3],$[4]=Q[4],$[5]=Q[5],$[6]=Q[6],$[7]=Q[7],$[8]=Q[8],$[9]=Q[9],$[10]=Q[10],$[11]=Q[11],$[12]=Q[12],$[13]=Q[13],$[14]=Q[14],$[15]=Q[15],this}copyPosition(J){let $=this.elements,Q=J.elements;return $[12]=Q[12],$[13]=Q[13],$[14]=Q[14],this}setFromMatrix3(J){let $=J.elements;return this.set($[0],$[3],$[6],0,$[1],$[4],$[7],0,$[2],$[5],$[8],0,0,0,0,1),this}extractBasis(J,$,Q){return J.setFromMatrixColumn(this,0),$.setFromMatrixColumn(this,1),Q.setFromMatrixColumn(this,2),this}makeBasis(J,$,Q){return this.set(J.x,$.x,Q.x,0,J.y,$.y,Q.y,0,J.z,$.z,Q.z,0,0,0,0,1),this}extractRotation(J){let $=this.elements,Q=J.elements,Z=1/$8.setFromMatrixColumn(J,0).length(),W=1/$8.setFromMatrixColumn(J,1).length(),K=1/$8.setFromMatrixColumn(J,2).length();return $[0]=Q[0]*Z,$[1]=Q[1]*Z,$[2]=Q[2]*Z,$[3]=0,$[4]=Q[4]*W,$[5]=Q[5]*W,$[6]=Q[6]*W,$[7]=0,$[8]=Q[8]*K,$[9]=Q[9]*K,$[10]=Q[10]*K,$[11]=0,$[12]=0,$[13]=0,$[14]=0,$[15]=1,this}makeRotationFromEuler(J){let $=this.elements,Q=J.x,Z=J.y,W=J.z,K=Math.cos(Q),H=Math.sin(Q),Y=Math.cos(Z),X=Math.sin(Z),N=Math.cos(W),U=Math.sin(W);if(J.order==="XYZ"){let G=K*N,D=K*U,R=H*N,M=H*U;$[0]=Y*N,$[4]=-Y*U,$[8]=X,$[1]=D+R*X,$[5]=G-M*X,$[9]=-H*Y,$[2]=M-G*X,$[6]=R+D*X,$[10]=K*Y}else if(J.order==="YXZ"){let G=Y*N,D=Y*U,R=X*N,M=X*U;$[0]=G+M*H,$[4]=R*H-D,$[8]=K*X,$[1]=K*U,$[5]=K*N,$[9]=-H,$[2]=D*H-R,$[6]=M+G*H,$[10]=K*Y}else if(J.order==="ZXY"){let G=Y*N,D=Y*U,R=X*N,M=X*U;$[0]=G-M*H,$[4]=-K*U,$[8]=R+D*H,$[1]=D+R*H,$[5]=K*N,$[9]=M-G*H,$[2]=-K*X,$[6]=H,$[10]=K*Y}else if(J.order==="ZYX"){let G=K*N,D=K*U,R=H*N,M=H*U;$[0]=Y*N,$[4]=R*X-D,$[8]=G*X+M,$[1]=Y*U,$[5]=M*X+G,$[9]=D*X-R,$[2]=-X,$[6]=H*Y,$[10]=K*Y}else if(J.order==="YZX"){let G=K*Y,D=K*X,R=H*Y,M=H*X;$[0]=Y*N,$[4]=M-G*U,$[8]=R*U+D,$[1]=U,$[5]=K*N,$[9]=-H*N,$[2]=-X*N,$[6]=D*U+R,$[10]=G-M*U}else if(J.order==="XZY"){let G=K*Y,D=K*X,R=H*Y,M=H*X;$[0]=Y*N,$[4]=-U,$[8]=X*N,$[1]=G*U+M,$[5]=K*N,$[9]=D*U-R,$[2]=R*U-D,$[6]=H*N,$[10]=M*U+G}return $[3]=0,$[7]=0,$[11]=0,$[12]=0,$[13]=0,$[14]=0,$[15]=1,this}makeRotationFromQuaternion(J){return this.compose(UK,J,GK)}lookAt(J,$,Q){let Z=this.elements;if(h9.subVectors(J,$),h9.lengthSq()===0)h9.z=1;if(h9.normalize(),zJ.crossVectors(Q,h9),zJ.lengthSq()===0){if(Math.abs(Q.z)===1)h9.x+=0.0001;else h9.z+=0.0001;h9.normalize(),zJ.crossVectors(Q,h9)}return zJ.normalize(),H6.crossVectors(h9,zJ),Z[0]=zJ.x,Z[4]=H6.x,Z[8]=h9.x,Z[1]=zJ.y,Z[5]=H6.y,Z[9]=h9.y,Z[2]=zJ.z,Z[6]=H6.z,Z[10]=h9.z,this}multiply(J){return this.multiplyMatrices(this,J)}premultiply(J){return this.multiplyMatrices(J,this)}multiplyMatrices(J,$){let Q=J.elements,Z=$.elements,W=this.elements,K=Q[0],H=Q[4],Y=Q[8],X=Q[12],N=Q[1],U=Q[5],G=Q[9],D=Q[13],R=Q[2],M=Q[6],L=Q[10],E=Q[14],q=Q[3],A=Q[7],V=Q[11],I=Q[15],f=Z[0],P=Z[4],S=Z[8],g=Z[12],z=Z[1],F=Z[5],T=Z[9],n=Z[13],s=Z[2],d=Z[6],Z0=Z[10],m=Z[14],W0=Z[3],u=Z[7],N0=Z[11],q0=Z[15];return W[0]=K*f+H*z+Y*s+X*W0,W[4]=K*P+H*F+Y*d+X*u,W[8]=K*S+H*T+Y*Z0+X*N0,W[12]=K*g+H*n+Y*m+X*q0,W[1]=N*f+U*z+G*s+D*W0,W[5]=N*P+U*F+G*d+D*u,W[9]=N*S+U*T+G*Z0+D*N0,W[13]=N*g+U*n+G*m+D*q0,W[2]=R*f+M*z+L*s+E*W0,W[6]=R*P+M*F+L*d+E*u,W[10]=R*S+M*T+L*Z0+E*N0,W[14]=R*g+M*n+L*m+E*q0,W[3]=q*f+A*z+V*s+I*W0,W[7]=q*P+A*F+V*d+I*u,W[11]=q*S+A*T+V*Z0+I*N0,W[15]=q*g+A*n+V*m+I*q0,this}multiplyScalar(J){let $=this.elements;return $[0]*=J,$[4]*=J,$[8]*=J,$[12]*=J,$[1]*=J,$[5]*=J,$[9]*=J,$[13]*=J,$[2]*=J,$[6]*=J,$[10]*=J,$[14]*=J,$[3]*=J,$[7]*=J,$[11]*=J,$[15]*=J,this}determinant(){let J=this.elements,$=J[0],Q=J[4],Z=J[8],W=J[12],K=J[1],H=J[5],Y=J[9],X=J[13],N=J[2],U=J[6],G=J[10],D=J[14],R=J[3],M=J[7],L=J[11],E=J[15];return R*(+W*Y*U-Z*X*U-W*H*G+Q*X*G+Z*H*D-Q*Y*D)+M*(+$*Y*D-$*X*G+W*K*G-Z*K*D+Z*X*N-W*Y*N)+L*(+$*X*U-$*H*D-W*K*U+Q*K*D+W*H*N-Q*X*N)+E*(-Z*H*N-$*Y*U+$*H*G+Z*K*U-Q*K*G+Q*Y*N)}transpose(){let J=this.elements,$;return $=J[1],J[1]=J[4],J[4]=$,$=J[2],J[2]=J[8],J[8]=$,$=J[6],J[6]=J[9],J[9]=$,$=J[3],J[3]=J[12],J[12]=$,$=J[7],J[7]=J[13],J[13]=$,$=J[11],J[11]=J[14],J[14]=$,this}setPosition(J,$,Q){let Z=this.elements;if(J.isVector3)Z[12]=J.x,Z[13]=J.y,Z[14]=J.z;else Z[12]=J,Z[13]=$,Z[14]=Q;return this}invert(){let J=this.elements,$=J[0],Q=J[1],Z=J[2],W=J[3],K=J[4],H=J[5],Y=J[6],X=J[7],N=J[8],U=J[9],G=J[10],D=J[11],R=J[12],M=J[13],L=J[14],E=J[15],q=U*L*X-M*G*X+M*Y*D-H*L*D-U*Y*E+H*G*E,A=R*G*X-N*L*X-R*Y*D+K*L*D+N*Y*E-K*G*E,V=N*M*X-R*U*X+R*H*D-K*M*D-N*H*E+K*U*E,I=R*U*Y-N*M*Y-R*H*G+K*M*G+N*H*L-K*U*L,f=$*q+Q*A+Z*V+W*I;if(f===0)return this.set(0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0);let P=1/f;return J[0]=q*P,J[1]=(M*G*W-U*L*W-M*Z*D+Q*L*D+U*Z*E-Q*G*E)*P,J[2]=(H*L*W-M*Y*W+M*Z*X-Q*L*X-H*Z*E+Q*Y*E)*P,J[3]=(U*Y*W-H*G*W-U*Z*X+Q*G*X+H*Z*D-Q*Y*D)*P,J[4]=A*P,J[5]=(N*L*W-R*G*W+R*Z*D-$*L*D-N*Z*E+$*G*E)*P,J[6]=(R*Y*W-K*L*W-R*Z*X+$*L*X+K*Z*E-$*Y*E)*P,J[7]=(K*G*W-N*Y*W+N*Z*X-$*G*X-K*Z*D+$*Y*D)*P,J[8]=V*P,J[9]=(R*U*W-N*M*W-R*Q*D+$*M*D+N*Q*E-$*U*E)*P,J[10]=(K*M*W-R*H*W+R*Q*X-$*M*X-K*Q*E+$*H*E)*P,J[11]=(N*H*W-K*U*W-N*Q*X+$*U*X+K*Q*D-$*H*D)*P,J[12]=I*P,J[13]=(N*M*Z-R*U*Z+R*Q*G-$*M*G-N*Q*L+$*U*L)*P,J[14]=(R*H*Z-K*M*Z-R*Q*Y+$*M*Y+K*Q*L-$*H*L)*P,J[15]=(K*U*Z-N*H*Z+N*Q*Y-$*U*Y-K*Q*G+$*H*G)*P,this}scale(J){let $=this.elements,Q=J.x,Z=J.y,W=J.z;return $[0]*=Q,$[4]*=Z,$[8]*=W,$[1]*=Q,$[5]*=Z,$[9]*=W,$[2]*=Q,$[6]*=Z,$[10]*=W,$[3]*=Q,$[7]*=Z,$[11]*=W,this}getMaxScaleOnAxis(){let J=this.elements,$=J[0]*J[0]+J[1]*J[1]+J[2]*J[2],Q=J[4]*J[4]+J[5]*J[5]+J[6]*J[6],Z=J[8]*J[8]+J[9]*J[9]+J[10]*J[10];return Math.sqrt(Math.max($,Q,Z))}makeTranslation(J,$,Q){if(J.isVector3)this.set(1,0,0,J.x,0,1,0,J.y,0,0,1,J.z,0,0,0,1);else this.set(1,0,0,J,0,1,0,$,0,0,1,Q,0,0,0,1);return this}makeRotationX(J){let $=Math.cos(J),Q=Math.sin(J);return this.set(1,0,0,0,0,$,-Q,0,0,Q,$,0,0,0,0,1),this}makeRotationY(J){let $=Math.cos(J),Q=Math.sin(J);return this.set($,0,Q,0,0,1,0,0,-Q,0,$,0,0,0,0,1),this}makeRotationZ(J){let $=Math.cos(J),Q=Math.sin(J);return this.set($,-Q,0,0,Q,$,0,0,0,0,1,0,0,0,0,1),this}makeRotationAxis(J,$){let Q=Math.cos($),Z=Math.sin($),W=1-Q,K=J.x,H=J.y,Y=J.z,X=W*K,N=W*H;return this.set(X*K+Q,X*H-Z*Y,X*Y+Z*H,0,X*H+Z*Y,N*H+Q,N*Y-Z*K,0,X*Y-Z*H,N*Y+Z*K,W*Y*Y+Q,0,0,0,0,1),this}makeScale(J,$,Q){return this.set(J,0,0,0,0,$,0,0,0,0,Q,0,0,0,0,1),this}makeShear(J,$,Q,Z,W,K){return this.set(1,Q,W,0,J,1,K,0,$,Z,1,0,0,0,0,1),this}compose(J,$,Q){let Z=this.elements,W=$._x,K=$._y,H=$._z,Y=$._w,X=W+W,N=K+K,U=H+H,G=W*X,D=W*N,R=W*U,M=K*N,L=K*U,E=H*U,q=Y*X,A=Y*N,V=Y*U,I=Q.x,f=Q.y,P=Q.z;return Z[0]=(1-(M+E))*I,Z[1]=(D+V)*I,Z[2]=(R-A)*I,Z[3]=0,Z[4]=(D-V)*f,Z[5]=(1-(G+E))*f,Z[6]=(L+q)*f,Z[7]=0,Z[8]=(R+A)*P,Z[9]=(L-q)*P,Z[10]=(1-(G+M))*P,Z[11]=0,Z[12]=J.x,Z[13]=J.y,Z[14]=J.z,Z[15]=1,this}decompose(J,$,Q){let Z=this.elements,W=$8.set(Z[0],Z[1],Z[2]).length(),K=$8.set(Z[4],Z[5],Z[6]).length(),H=$8.set(Z[8],Z[9],Z[10]).length();if(this.determinant()<0)W=-W;J.x=Z[12],J.y=Z[13],J.z=Z[14],a9.copy(this);let X=1/W,N=1/K,U=1/H;return a9.elements[0]*=X,a9.elements[1]*=X,a9.elements[2]*=X,a9.elements[4]*=N,a9.elements[5]*=N,a9.elements[6]*=N,a9.elements[8]*=U,a9.elements[9]*=U,a9.elements[10]*=U,$.setFromRotationMatrix(a9),Q.x=W,Q.y=K,Q.z=H,this}makePerspective(J,$,Q,Z,W,K,H=2000,Y=!1){let X=this.elements,N=2*W/($-J),U=2*W/(Q-Z),G=($+J)/($-J),D=(Q+Z)/(Q-Z),R,M;if(Y)R=W/(K-W),M=K*W/(K-W);else if(H===2000)R=-(K+W)/(K-W),M=-2*K*W/(K-W);else if(H===2001)R=-K/(K-W),M=-K*W/(K-W);else throw Error("THREE.Matrix4.makePerspective(): Invalid coordinate system: "+H);return X[0]=N,X[4]=0,X[8]=G,X[12]=0,X[1]=0,X[5]=U,X[9]=D,X[13]=0,X[2]=0,X[6]=0,X[10]=R,X[14]=M,X[3]=0,X[7]=0,X[11]=-1,X[15]=0,this}makeOrthographic(J,$,Q,Z,W,K,H=2000,Y=!1){let X=this.elements,N=2/($-J),U=2/(Q-Z),G=-($+J)/($-J),D=-(Q+Z)/(Q-Z),R,M;if(Y)R=1/(K-W),M=K/(K-W);else if(H===2000)R=-2/(K-W),M=-(K+W)/(K-W);else if(H===2001)R=-1/(K-W),M=-W/(K-W);else throw Error("THREE.Matrix4.makeOrthographic(): Invalid coordinate system: "+H);return X[0]=N,X[4]=0,X[8]=0,X[12]=G,X[1]=0,X[5]=U,X[9]=0,X[13]=D,X[2]=0,X[6]=0,X[10]=R,X[14]=M,X[3]=0,X[7]=0,X[11]=0,X[15]=1,this}equals(J){let $=this.elements,Q=J.elements;for(let Z=0;Z<16;Z++)if($[Z]!==Q[Z])return!1;return!0}fromArray(J,$=0){for(let Q=0;Q<16;Q++)this.elements[Q]=J[Q+$];return this}toArray(J=[],$=0){let Q=this.elements;return J[$]=Q[0],J[$+1]=Q[1],J[$+2]=Q[2],J[$+3]=Q[3],J[$+4]=Q[4],J[$+5]=Q[5],J[$+6]=Q[6],J[$+7]=Q[7],J[$+8]=Q[8],J[$+9]=Q[9],J[$+10]=Q[10],J[$+11]=Q[11],J[$+12]=Q[12],J[$+13]=Q[13],J[$+14]=Q[14],J[$+15]=Q[15],J}}var $8=new v,a9=new X9,UK=new v(0,0,0),GK=new v(1,1,1),zJ=new v,H6=new v,h9=new v,VQ=new X9,zQ=new e9;class t9{constructor(J=0,$=0,Q=0,Z=t9.DEFAULT_ORDER){this.isEuler=!0,this._x=J,this._y=$,this._z=Q,this._order=Z}get x(){return this._x}set x(J){this._x=J,this._onChangeCallback()}get y(){return this._y}set y(J){this._y=J,this._onChangeCallback()}get z(){return this._z}set z(J){this._z=J,this._onChangeCallback()}get order(){return this._order}set order(J){this._order=J,this._onChangeCallback()}set(J,$,Q,Z=this._order){return this._x=J,this._y=$,this._z=Q,this._order=Z,this._onChangeCallback(),this}clone(){return new this.constructor(this._x,this._y,this._z,this._order)}copy(J){return this._x=J._x,this._y=J._y,this._z=J._z,this._order=J._order,this._onChangeCallback(),this}setFromRotationMatrix(J,$=this._order,Q=!0){let Z=J.elements,W=Z[0],K=Z[4],H=Z[8],Y=Z[1],X=Z[5],N=Z[9],U=Z[2],G=Z[6],D=Z[10];switch($){case"XYZ":if(this._y=Math.asin(u0(H,-1,1)),Math.abs(H)<0.9999999)this._x=Math.atan2(-N,D),this._z=Math.atan2(-K,W);else this._x=Math.atan2(G,X),this._z=0;break;case"YXZ":if(this._x=Math.asin(-u0(N,-1,1)),Math.abs(N)<0.9999999)this._y=Math.atan2(H,D),this._z=Math.atan2(Y,X);else this._y=Math.atan2(-U,W),this._z=0;break;case"ZXY":if(this._x=Math.asin(u0(G,-1,1)),Math.abs(G)<0.9999999)this._y=Math.atan2(-U,D),this._z=Math.atan2(-K,X);else this._y=0,this._z=Math.atan2(Y,W);break;case"ZYX":if(this._y=Math.asin(-u0(U,-1,1)),Math.abs(U)<0.9999999)this._x=Math.atan2(G,D),this._z=Math.atan2(Y,W);else this._x=0,this._z=Math.atan2(-K,X);break;case"YZX":if(this._z=Math.asin(u0(Y,-1,1)),Math.abs(Y)<0.9999999)this._x=Math.atan2(-N,X),this._y=Math.atan2(-U,W);else this._x=0,this._y=Math.atan2(H,D);break;case"XZY":if(this._z=Math.asin(-u0(K,-1,1)),Math.abs(K)<0.9999999)this._x=Math.atan2(G,X),this._y=Math.atan2(H,W);else this._x=Math.atan2(-N,D),this._y=0;break;default:console.warn("THREE.Euler: .setFromRotationMatrix() encountered an unknown order: "+$)}if(this._order=$,Q===!0)this._onChangeCallback();return this}setFromQuaternion(J,$,Q){return VQ.makeRotationFromQuaternion(J),this.setFromRotationMatrix(VQ,$,Q)}setFromVector3(J,$=this._order){return this.set(J.x,J.y,J.z,$)}reorder(J){return zQ.setFromEuler(this),this.setFromQuaternion(zQ,J)}equals(J){return J._x===this._x&&J._y===this._y&&J._z===this._z&&J._order===this._order}fromArray(J){if(this._x=J[0],this._y=J[1],this._z=J[2],J[3]!==void 0)this._order=J[3];return this._onChangeCallback(),this}toArray(J=[],$=0){return J[$]=this._x,J[$+1]=this._y,J[$+2]=this._z,J[$+3]=this._order,J}_onChange(J){return this._onChangeCallback=J,this}_onChangeCallback(){}*[Symbol.iterator](){yield this._x,yield this._y,yield this._z,yield this._order}}t9.DEFAULT_ORDER="XYZ";class n8{constructor(){this.mask=1}set(J){this.mask=(1<<J|0)>>>0}enable(J){this.mask|=1<<J|0}enableAll(){this.mask=-1}toggle(J){this.mask^=1<<J|0}disable(J){this.mask&=~(1<<J|0)}disableAll(){this.mask=0}test(J){return(this.mask&J.mask)!==0}isEnabled(J){return(this.mask&(1<<J|0))!==0}}var EK=0,BQ=new v,Q8=new e9,GJ=new X9,X6=new v,S8=new v,DK=new v,qK=new e9,wQ=new v(1,0,0),CQ=new v(0,1,0),_Q=new v(0,0,1),IQ={type:"added"},OK={type:"removed"},Z8={type:"childadded",child:null},L7={type:"childremoved",child:null};class R9 extends kJ{constructor(){super();this.isObject3D=!0,Object.defineProperty(this,"id",{value:EK++}),this.uuid=L8(),this.name="",this.type="Object3D",this.parent=null,this.children=[],this.up=R9.DEFAULT_UP.clone();let J=new v,$=new t9,Q=new e9,Z=new v(1,1,1);function W(){Q.setFromEuler($,!1)}function K(){$.setFromQuaternion(Q,void 0,!1)}$._onChange(W),Q._onChange(K),Object.defineProperties(this,{position:{configurable:!0,enumerable:!0,value:J},rotation:{configurable:!0,enumerable:!0,value:$},quaternion:{configurable:!0,enumerable:!0,value:Q},scale:{configurable:!0,enumerable:!0,value:Z},modelViewMatrix:{value:new X9},normalMatrix:{value:new p0}}),this.matrix=new X9,this.matrixWorld=new X9,this.matrixAutoUpdate=R9.DEFAULT_MATRIX_AUTO_UPDATE,this.matrixWorldAutoUpdate=R9.DEFAULT_MATRIX_WORLD_AUTO_UPDATE,this.matrixWorldNeedsUpdate=!1,this.layers=new n8,this.visible=!0,this.castShadow=!1,this.receiveShadow=!1,this.frustumCulled=!0,this.renderOrder=0,this.animations=[],this.customDepthMaterial=void 0,this.customDistanceMaterial=void 0,this.userData={}}onBeforeShadow(){}onAfterShadow(){}onBeforeRender(){}onAfterRender(){}applyMatrix4(J){if(this.matrixAutoUpdate)this.updateMatrix();this.matrix.premultiply(J),this.matrix.decompose(this.position,this.quaternion,this.scale)}applyQuaternion(J){return this.quaternion.premultiply(J),this}setRotationFromAxisAngle(J,$){this.quaternion.setFromAxisAngle(J,$)}setRotationFromEuler(J){this.quaternion.setFromEuler(J,!0)}setRotationFromMatrix(J){this.quaternion.setFromRotationMatrix(J)}setRotationFromQuaternion(J){this.quaternion.copy(J)}rotateOnAxis(J,$){return Q8.setFromAxisAngle(J,$),this.quaternion.multiply(Q8),this}rotateOnWorldAxis(J,$){return Q8.setFromAxisAngle(J,$),this.quaternion.premultiply(Q8),this}rotateX(J){return this.rotateOnAxis(wQ,J)}rotateY(J){return this.rotateOnAxis(CQ,J)}rotateZ(J){return this.rotateOnAxis(_Q,J)}translateOnAxis(J,$){return BQ.copy(J).applyQuaternion(this.quaternion),this.position.add(BQ.multiplyScalar($)),this}translateX(J){return this.translateOnAxis(wQ,J)}translateY(J){return this.translateOnAxis(CQ,J)}translateZ(J){return this.translateOnAxis(_Q,J)}localToWorld(J){return this.updateWorldMatrix(!0,!1),J.applyMatrix4(this.matrixWorld)}worldToLocal(J){return this.updateWorldMatrix(!0,!1),J.applyMatrix4(GJ.copy(this.matrixWorld).invert())}lookAt(J,$,Q){if(J.isVector3)X6.copy(J);else X6.set(J,$,Q);let Z=this.parent;if(this.updateWorldMatrix(!0,!1),S8.setFromMatrixPosition(this.matrixWorld),this.isCamera||this.isLight)GJ.lookAt(S8,X6,this.up);else GJ.lookAt(X6,S8,this.up);if(this.quaternion.setFromRotationMatrix(GJ),Z)GJ.extractRotation(Z.matrixWorld),Q8.setFromRotationMatrix(GJ),this.quaternion.premultiply(Q8.invert())}add(J){if(arguments.length>1){for(let $=0;$<arguments.length;$++)this.add(arguments[$]);return this}if(J===this)return console.error("THREE.Object3D.add: object can't be added as a child of itself.",J),this;if(J&&J.isObject3D)J.removeFromParent(),J.parent=this,this.children.push(J),J.dispatchEvent(IQ),Z8.child=J,this.dispatchEvent(Z8),Z8.child=null;else console.error("THREE.Object3D.add: object not an instance of THREE.Object3D.",J);return this}remove(J){if(arguments.length>1){for(let Q=0;Q<arguments.length;Q++)this.remove(arguments[Q]);return this}let $=this.children.indexOf(J);if($!==-1)J.parent=null,this.children.splice($,1),J.dispatchEvent(OK),L7.child=J,this.dispatchEvent(L7),L7.child=null;return this}removeFromParent(){let J=this.parent;if(J!==null)J.remove(this);return this}clear(){return this.remove(...this.children)}attach(J){if(this.updateWorldMatrix(!0,!1),GJ.copy(this.matrixWorld).invert(),J.parent!==null)J.parent.updateWorldMatrix(!0,!1),GJ.multiply(J.parent.matrixWorld);return J.applyMatrix4(GJ),J.removeFromParent(),J.parent=this,this.children.push(J),J.updateWorldMatrix(!1,!0),J.dispatchEvent(IQ),Z8.child=J,this.dispatchEvent(Z8),Z8.child=null,this}getObjectById(J){return this.getObjectByProperty("id",J)}getObjectByName(J){return this.getObjectByProperty("name",J)}getObjectByProperty(J,$){if(this[J]===$)return this;for(let Q=0,Z=this.children.length;Q<Z;Q++){let K=this.children[Q].getObjectByProperty(J,$);if(K!==void 0)return K}return}getObjectsByProperty(J,$,Q=[]){if(this[J]===$)Q.push(this);let Z=this.children;for(let W=0,K=Z.length;W<K;W++)Z[W].getObjectsByProperty(J,$,Q);return Q}getWorldPosition(J){return this.updateWorldMatrix(!0,!1),J.setFromMatrixPosition(this.matrixWorld)}getWorldQuaternion(J){return this.updateWorldMatrix(!0,!1),this.matrixWorld.decompose(S8,J,DK),J}getWorldScale(J){return this.updateWorldMatrix(!0,!1),this.matrixWorld.decompose(S8,qK,J),J}getWorldDirection(J){this.updateWorldMatrix(!0,!1);let $=this.matrixWorld.elements;return J.set($[8],$[9],$[10]).normalize()}raycast(){}traverse(J){J(this);let $=this.children;for(let Q=0,Z=$.length;Q<Z;Q++)$[Q].traverse(J)}traverseVisible(J){if(this.visible===!1)return;J(this);let $=this.children;for(let Q=0,Z=$.length;Q<Z;Q++)$[Q].traverseVisible(J)}traverseAncestors(J){let $=this.parent;if($!==null)J($),$.traverseAncestors(J)}updateMatrix(){this.matrix.compose(this.position,this.quaternion,this.scale),this.matrixWorldNeedsUpdate=!0}updateMatrixWorld(J){if(this.matrixAutoUpdate)this.updateMatrix();if(this.matrixWorldNeedsUpdate||J){if(this.matrixWorldAutoUpdate===!0)if(this.parent===null)this.matrixWorld.copy(this.matrix);else this.matrixWorld.multiplyMatrices(this.parent.matrixWorld,this.matrix);this.matrixWorldNeedsUpdate=!1,J=!0}let $=this.children;for(let Q=0,Z=$.length;Q<Z;Q++)$[Q].updateMatrixWorld(J)}updateWorldMatrix(J,$){let Q=this.parent;if(J===!0&&Q!==null)Q.updateWorldMatrix(!0,!1);if(this.matrixAutoUpdate)this.updateMatrix();if(this.matrixWorldAutoUpdate===!0)if(this.parent===null)this.matrixWorld.copy(this.matrix);else this.matrixWorld.multiplyMatrices(this.parent.matrixWorld,this.matrix);if($===!0){let Z=this.children;for(let W=0,K=Z.length;W<K;W++)Z[W].updateWorldMatrix(!1,!0)}}toJSON(J){let $=J===void 0||typeof J==="string",Q={};if($)J={geometries:{},materials:{},textures:{},images:{},shapes:{},skeletons:{},animations:{},nodes:{}},Q.metadata={version:4.7,type:"Object",generator:"Object3D.toJSON"};let Z={};if(Z.uuid=this.uuid,Z.type=this.type,this.name!=="")Z.name=this.name;if(this.castShadow===!0)Z.castShadow=!0;if(this.receiveShadow===!0)Z.receiveShadow=!0;if(this.visible===!1)Z.visible=!1;if(this.frustumCulled===!1)Z.frustumCulled=!1;if(this.renderOrder!==0)Z.renderOrder=this.renderOrder;if(Object.keys(this.userData).length>0)Z.userData=this.userData;if(Z.layers=this.layers.mask,Z.matrix=this.matrix.toArray(),Z.up=this.up.toArray(),this.matrixAutoUpdate===!1)Z.matrixAutoUpdate=!1;if(this.isInstancedMesh){if(Z.type="InstancedMesh",Z.count=this.count,Z.instanceMatrix=this.instanceMatrix.toJSON(),this.instanceColor!==null)Z.instanceColor=this.instanceColor.toJSON()}if(this.isBatchedMesh){if(Z.type="BatchedMesh",Z.perObjectFrustumCulled=this.perObjectFrustumCulled,Z.sortObjects=this.sortObjects,Z.drawRanges=this._drawRanges,Z.reservedRanges=this._reservedRanges,Z.geometryInfo=this._geometryInfo.map((H)=>({...H,boundingBox:H.boundingBox?H.boundingBox.toJSON():void 0,boundingSphere:H.boundingSphere?H.boundingSphere.toJSON():void 0})),Z.instanceInfo=this._instanceInfo.map((H)=>({...H})),Z.availableInstanceIds=this._availableInstanceIds.slice(),Z.availableGeometryIds=this._availableGeometryIds.slice(),Z.nextIndexStart=this._nextIndexStart,Z.nextVertexStart=this._nextVertexStart,Z.geometryCount=this._geometryCount,Z.maxInstanceCount=this._maxInstanceCount,Z.maxVertexCount=this._maxVertexCount,Z.maxIndexCount=this._maxIndexCount,Z.geometryInitialized=this._geometryInitialized,Z.matricesTexture=this._matricesTexture.toJSON(J),Z.indirectTexture=this._indirectTexture.toJSON(J),this._colorsTexture!==null)Z.colorsTexture=this._colorsTexture.toJSON(J);if(this.boundingSphere!==null)Z.boundingSphere=this.boundingSphere.toJSON();if(this.boundingBox!==null)Z.boundingBox=this.boundingBox.toJSON()}function W(H,Y){if(H[Y.uuid]===void 0)H[Y.uuid]=Y.toJSON(J);return Y.uuid}if(this.isScene){if(this.background){if(this.background.isColor)Z.background=this.background.toJSON();else if(this.background.isTexture)Z.background=this.background.toJSON(J).uuid}if(this.environment&&this.environment.isTexture&&this.environment.isRenderTargetTexture!==!0)Z.environment=this.environment.toJSON(J).uuid}else if(this.isMesh||this.isLine||this.isPoints){Z.geometry=W(J.geometries,this.geometry);let H=this.geometry.parameters;if(H!==void 0&&H.shapes!==void 0){let Y=H.shapes;if(Array.isArray(Y))for(let X=0,N=Y.length;X<N;X++){let U=Y[X];W(J.shapes,U)}else W(J.shapes,Y)}}if(this.isSkinnedMesh){if(Z.bindMode=this.bindMode,Z.bindMatrix=this.bindMatrix.toArray(),this.skeleton!==void 0)W(J.skeletons,this.skeleton),Z.skeleton=this.skeleton.uuid}if(this.material!==void 0)if(Array.isArray(this.material)){let H=[];for(let Y=0,X=this.material.length;Y<X;Y++)H.push(W(J.materials,this.material[Y]));Z.material=H}else Z.material=W(J.materials,this.material);if(this.children.length>0){Z.children=[];for(let H=0;H<this.children.length;H++)Z.children.push(this.children[H].toJSON(J).object)}if(this.animations.length>0){Z.animations=[];for(let H=0;H<this.animations.length;H++){let Y=this.animations[H];Z.animations.push(W(J.animations,Y))}}if($){let H=K(J.geometries),Y=K(J.materials),X=K(J.textures),N=K(J.images),U=K(J.shapes),G=K(J.skeletons),D=K(J.animations),R=K(J.nodes);if(H.length>0)Q.geometries=H;if(Y.length>0)Q.materials=Y;if(X.length>0)Q.textures=X;if(N.length>0)Q.images=N;if(U.length>0)Q.shapes=U;if(G.length>0)Q.skeletons=G;if(D.length>0)Q.animations=D;if(R.length>0)Q.nodes=R}return Q.object=Z,Q;function K(H){let Y=[];for(let X in H){let N=H[X];delete N.metadata,Y.push(N)}return Y}}clone(J){return new this.constructor().copy(this,J)}copy(J,$=!0){if(this.name=J.name,this.up.copy(J.up),this.position.copy(J.position),this.rotation.order=J.rotation.order,this.quaternion.copy(J.quaternion),this.scale.copy(J.scale),this.matrix.copy(J.matrix),this.matrixWorld.copy(J.matrixWorld),this.matrixAutoUpdate=J.matrixAutoUpdate,this.matrixWorldAutoUpdate=J.matrixWorldAutoUpdate,this.matrixWorldNeedsUpdate=J.matrixWorldNeedsUpdate,this.layers.mask=J.layers.mask,this.visible=J.visible,this.castShadow=J.castShadow,this.receiveShadow=J.receiveShadow,this.frustumCulled=J.frustumCulled,this.renderOrder=J.renderOrder,this.animations=J.animations.slice(),this.userData=JSON.parse(JSON.stringify(J.userData)),$===!0)for(let Q=0;Q<J.children.length;Q++){let Z=J.children[Q];this.add(Z.clone())}return this}}R9.DEFAULT_UP=new v(0,1,0);R9.DEFAULT_MATRIX_AUTO_UPDATE=!0;R9.DEFAULT_MATRIX_WORLD_AUTO_UPDATE=!0;var r9=new v,EJ=new v,V7=new v,DJ=new v,W8=new v,K8=new v,PQ=new v,z7=new v,B7=new v,w7=new v,C7=new G9,_7=new G9,I7=new G9;class c9{constructor(J=new v,$=new v,Q=new v){this.a=J,this.b=$,this.c=Q}static getNormal(J,$,Q,Z){Z.subVectors(Q,$),r9.subVectors(J,$),Z.cross(r9);let W=Z.lengthSq();if(W>0)return Z.multiplyScalar(1/Math.sqrt(W));return Z.set(0,0,0)}static getBarycoord(J,$,Q,Z,W){r9.subVectors(Z,$),EJ.subVectors(Q,$),V7.subVectors(J,$);let K=r9.dot(r9),H=r9.dot(EJ),Y=r9.dot(V7),X=EJ.dot(EJ),N=EJ.dot(V7),U=K*X-H*H;if(U===0)return W.set(0,0,0),null;let G=1/U,D=(X*Y-H*N)*G,R=(K*N-H*Y)*G;return W.set(1-D-R,R,D)}static containsPoint(J,$,Q,Z){if(this.getBarycoord(J,$,Q,Z,DJ)===null)return!1;return DJ.x>=0&&DJ.y>=0&&DJ.x+DJ.y<=1}static getInterpolation(J,$,Q,Z,W,K,H,Y){if(this.getBarycoord(J,$,Q,Z,DJ)===null){if(Y.x=0,Y.y=0,"z"in Y)Y.z=0;if("w"in Y)Y.w=0;return null}return Y.setScalar(0),Y.addScaledVector(W,DJ.x),Y.addScaledVector(K,DJ.y),Y.addScaledVector(H,DJ.z),Y}static getInterpolatedAttribute(J,$,Q,Z,W,K){return C7.setScalar(0),_7.setScalar(0),I7.setScalar(0),C7.fromBufferAttribute(J,$),_7.fromBufferAttribute(J,Q),I7.fromBufferAttribute(J,Z),K.setScalar(0),K.addScaledVector(C7,W.x),K.addScaledVector(_7,W.y),K.addScaledVector(I7,W.z),K}static isFrontFacing(J,$,Q,Z){return r9.subVectors(Q,$),EJ.subVectors(J,$),r9.cross(EJ).dot(Z)<0?!0:!1}set(J,$,Q){return this.a.copy(J),this.b.copy($),this.c.copy(Q),this}setFromPointsAndIndices(J,$,Q,Z){return this.a.copy(J[$]),this.b.copy(J[Q]),this.c.copy(J[Z]),this}setFromAttributeAndIndices(J,$,Q,Z){return this.a.fromBufferAttribute(J,$),this.b.fromBufferAttribute(J,Q),this.c.fromBufferAttribute(J,Z),this}clone(){return new this.constructor().copy(this)}copy(J){return this.a.copy(J.a),this.b.copy(J.b),this.c.copy(J.c),this}getArea(){return r9.subVectors(this.c,this.b),EJ.subVectors(this.a,this.b),r9.cross(EJ).length()*0.5}getMidpoint(J){return J.addVectors(this.a,this.b).add(this.c).multiplyScalar(0.3333333333333333)}getNormal(J){return c9.getNormal(this.a,this.b,this.c,J)}getPlane(J){return J.setFromCoplanarPoints(this.a,this.b,this.c)}getBarycoord(J,$){return c9.getBarycoord(J,this.a,this.b,this.c,$)}getInterpolation(J,$,Q,Z,W){return c9.getInterpolation(J,this.a,this.b,this.c,$,Q,Z,W)}containsPoint(J){return c9.containsPoint(J,this.a,this.b,this.c)}isFrontFacing(J){return c9.isFrontFacing(this.a,this.b,this.c,J)}intersectsBox(J){return J.intersectsTriangle(this)}closestPointToPoint(J,$){let Q=this.a,Z=this.b,W=this.c,K,H;W8.subVectors(Z,Q),K8.subVectors(W,Q),z7.subVectors(J,Q);let Y=W8.dot(z7),X=K8.dot(z7);if(Y<=0&&X<=0)return $.copy(Q);B7.subVectors(J,Z);let N=W8.dot(B7),U=K8.dot(B7);if(N>=0&&U<=N)return $.copy(Z);let G=Y*U-N*X;if(G<=0&&Y>=0&&N<=0)return K=Y/(Y-N),$.copy(Q).addScaledVector(W8,K);w7.subVectors(J,W);let D=W8.dot(w7),R=K8.dot(w7);if(R>=0&&D<=R)return $.copy(W);let M=D*X-Y*R;if(M<=0&&X>=0&&R<=0)return H=X/(X-R),$.copy(Q).addScaledVector(K8,H);let L=N*R-D*U;if(L<=0&&U-N>=0&&D-R>=0)return PQ.subVectors(W,Z),H=(U-N)/(U-N+(D-R)),$.copy(Z).addScaledVector(PQ,H);let E=1/(L+M+G);return K=M*E,H=G*E,$.copy(Q).addScaledVector(W8,K).addScaledVector(K8,H)}equals(J){return J.a.equals(this.a)&&J.b.equals(this.b)&&J.c.equals(this.c)}}var cZ={aliceblue:15792383,antiquewhite:16444375,aqua:65535,aquamarine:8388564,azure:15794175,beige:16119260,bisque:16770244,black:0,blanchedalmond:16772045,blue:255,blueviolet:9055202,brown:10824234,burlywood:14596231,cadetblue:6266528,chartreuse:8388352,chocolate:13789470,coral:16744272,cornflowerblue:6591981,cornsilk:16775388,crimson:14423100,cyan:65535,darkblue:139,darkcyan:35723,darkgoldenrod:12092939,darkgray:11119017,darkgreen:25600,darkgrey:11119017,darkkhaki:12433259,darkmagenta:9109643,darkolivegreen:5597999,darkorange:16747520,darkorchid:10040012,darkred:9109504,darksalmon:15308410,darkseagreen:9419919,darkslateblue:4734347,darkslategray:3100495,darkslategrey:3100495,darkturquoise:52945,darkviolet:9699539,deeppink:16716947,deepskyblue:49151,dimgray:6908265,dimgrey:6908265,dodgerblue:2003199,firebrick:11674146,floralwhite:16775920,forestgreen:2263842,fuchsia:16711935,gainsboro:14474460,ghostwhite:16316671,gold:16766720,goldenrod:14329120,gray:8421504,green:32768,greenyellow:11403055,grey:8421504,honeydew:15794160,hotpink:16738740,indianred:13458524,indigo:4915330,ivory:16777200,khaki:15787660,lavender:15132410,lavenderblush:16773365,lawngreen:8190976,lemonchiffon:16775885,lightblue:11393254,lightcoral:15761536,lightcyan:14745599,lightgoldenrodyellow:16448210,lightgray:13882323,lightgreen:9498256,lightgrey:13882323,lightpink:16758465,lightsalmon:16752762,lightseagreen:2142890,lightskyblue:8900346,lightslategray:7833753,lightslategrey:7833753,lightsteelblue:11584734,lightyellow:16777184,lime:65280,limegreen:3329330,linen:16445670,magenta:16711935,maroon:8388608,mediumaquamarine:6737322,mediumblue:205,mediumorchid:12211667,mediumpurple:9662683,mediumseagreen:3978097,mediumslateblue:8087790,mediumspringgreen:64154,mediumturquoise:4772300,mediumvioletred:13047173,midnightblue:1644912,mintcream:16121850,mistyrose:16770273,moccasin:16770229,navajowhite:16768685,navy:128,oldlace:16643558,olive:8421376,olivedrab:7048739,orange:16753920,orangered:16729344,orchid:14315734,palegoldenrod:15657130,palegreen:10025880,paleturquoise:11529966,palevioletred:14381203,papayawhip:16773077,peachpuff:16767673,peru:13468991,pink:16761035,plum:14524637,powderblue:11591910,purple:8388736,rebeccapurple:6697881,red:16711680,rosybrown:12357519,royalblue:4286945,saddlebrown:9127187,salmon:16416882,sandybrown:16032864,seagreen:3050327,seashell:16774638,sienna:10506797,silver:12632256,skyblue:8900331,slateblue:6970061,slategray:7372944,slategrey:7372944,snow:16775930,springgreen:65407,steelblue:4620980,tan:13808780,teal:32896,thistle:14204888,tomato:16737095,turquoise:4251856,violet:15631086,wheat:16113331,white:16777215,whitesmoke:16119285,yellow:16776960,yellowgreen:10145074},BJ={h:0,s:0,l:0},N6={h:0,s:0,l:0};function P7(J,$,Q){if(Q<0)Q+=1;if(Q>1)Q-=1;if(Q<0.16666666666666666)return J+($-J)*6*Q;if(Q<0.5)return $;if(Q<0.6666666666666666)return J+($-J)*6*(0.6666666666666666-Q);return J}class n0{constructor(J,$,Q){return this.isColor=!0,this.r=1,this.g=1,this.b=1,this.set(J,$,Q)}set(J,$,Q){if($===void 0&&Q===void 0){let Z=J;if(Z&&Z.isColor)this.copy(Z);else if(typeof Z==="number")this.setHex(Z);else if(typeof Z==="string")this.setStyle(Z)}else this.setRGB(J,$,Q);return this}setScalar(J){return this.r=J,this.g=J,this.b=J,this}setHex(J,$="srgb"){return J=Math.floor(J),this.r=(J>>16&255)/255,this.g=(J>>8&255)/255,this.b=(J&255)/255,s0.colorSpaceToWorking(this,$),this}setRGB(J,$,Q,Z=s0.workingColorSpace){return this.r=J,this.g=$,this.b=Q,s0.colorSpaceToWorking(this,Z),this}setHSL(J,$,Q,Z=s0.workingColorSpace){if(J=w$(J,1),$=u0($,0,1),Q=u0(Q,0,1),$===0)this.r=this.g=this.b=Q;else{let W=Q<=0.5?Q*(1+$):Q+$-Q*$,K=2*Q-W;this.r=P7(K,W,J+0.3333333333333333),this.g=P7(K,W,J),this.b=P7(K,W,J-0.3333333333333333)}return s0.colorSpaceToWorking(this,Z),this}setStyle(J,$="srgb"){function Q(W){if(W===void 0)return;if(parseFloat(W)<1)console.warn("THREE.Color: Alpha component of "+J+" will be ignored.")}let Z;if(Z=/^(\w+)\(([^\)]*)\)/.exec(J)){let W,K=Z[1],H=Z[2];switch(K){case"rgb":case"rgba":if(W=/^\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(H))return Q(W[4]),this.setRGB(Math.min(255,parseInt(W[1],10))/255,Math.min(255,parseInt(W[2],10))/255,Math.min(255,parseInt(W[3],10))/255,$);if(W=/^\s*(\d+)\%\s*,\s*(\d+)\%\s*,\s*(\d+)\%\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(H))return Q(W[4]),this.setRGB(Math.min(100,parseInt(W[1],10))/100,Math.min(100,parseInt(W[2],10))/100,Math.min(100,parseInt(W[3],10))/100,$);break;case"hsl":case"hsla":if(W=/^\s*(\d*\.?\d+)\s*,\s*(\d*\.?\d+)\%\s*,\s*(\d*\.?\d+)\%\s*(?:,\s*(\d*\.?\d+)\s*)?$/.exec(H))return Q(W[4]),this.setHSL(parseFloat(W[1])/360,parseFloat(W[2])/100,parseFloat(W[3])/100,$);break;default:console.warn("THREE.Color: Unknown color model "+J)}}else if(Z=/^\#([A-Fa-f\d]+)$/.exec(J)){let W=Z[1],K=W.length;if(K===3)return this.setRGB(parseInt(W.charAt(0),16)/15,parseInt(W.charAt(1),16)/15,parseInt(W.charAt(2),16)/15,$);else if(K===6)return this.setHex(parseInt(W,16),$);else console.warn("THREE.Color: Invalid hex color "+J)}else if(J&&J.length>0)return this.setColorName(J,$);return this}setColorName(J,$="srgb"){let Q=cZ[J.toLowerCase()];if(Q!==void 0)this.setHex(Q,$);else console.warn("THREE.Color: Unknown color "+J);return this}clone(){return new this.constructor(this.r,this.g,this.b)}copy(J){return this.r=J.r,this.g=J.g,this.b=J.b,this}copySRGBToLinear(J){return this.r=OJ(J.r),this.g=OJ(J.g),this.b=OJ(J.b),this}copyLinearToSRGB(J){return this.r=U8(J.r),this.g=U8(J.g),this.b=U8(J.b),this}convertSRGBToLinear(){return this.copySRGBToLinear(this),this}convertLinearToSRGB(){return this.copyLinearToSRGB(this),this}getHex(J="srgb"){return s0.workingToColorSpace(z9.copy(this),J),Math.round(u0(z9.r*255,0,255))*65536+Math.round(u0(z9.g*255,0,255))*256+Math.round(u0(z9.b*255,0,255))}getHexString(J="srgb"){return("000000"+this.getHex(J).toString(16)).slice(-6)}getHSL(J,$=s0.workingColorSpace){s0.workingToColorSpace(z9.copy(this),$);let{r:Q,g:Z,b:W}=z9,K=Math.max(Q,Z,W),H=Math.min(Q,Z,W),Y,X,N=(H+K)/2;if(H===K)Y=0,X=0;else{let U=K-H;switch(X=N<=0.5?U/(K+H):U/(2-K-H),K){case Q:Y=(Z-W)/U+(Z<W?6:0);break;case Z:Y=(W-Q)/U+2;break;case W:Y=(Q-Z)/U+4;break}Y/=6}return J.h=Y,J.s=X,J.l=N,J}getRGB(J,$=s0.workingColorSpace){return s0.workingToColorSpace(z9.copy(this),$),J.r=z9.r,J.g=z9.g,J.b=z9.b,J}getStyle(J="srgb"){s0.workingToColorSpace(z9.copy(this),J);let{r:$,g:Q,b:Z}=z9;if(J!=="srgb")return`color(${J} ${$.toFixed(3)} ${Q.toFixed(3)} ${Z.toFixed(3)})`;return`rgb(${Math.round($*255)},${Math.round(Q*255)},${Math.round(Z*255)})`}offsetHSL(J,$,Q){return this.getHSL(BJ),this.setHSL(BJ.h+J,BJ.s+$,BJ.l+Q)}add(J){return this.r+=J.r,this.g+=J.g,this.b+=J.b,this}addColors(J,$){return this.r=J.r+$.r,this.g=J.g+$.g,this.b=J.b+$.b,this}addScalar(J){return this.r+=J,this.g+=J,this.b+=J,this}sub(J){return this.r=Math.max(0,this.r-J.r),this.g=Math.max(0,this.g-J.g),this.b=Math.max(0,this.b-J.b),this}multiply(J){return this.r*=J.r,this.g*=J.g,this.b*=J.b,this}multiplyScalar(J){return this.r*=J,this.g*=J,this.b*=J,this}lerp(J,$){return this.r+=(J.r-this.r)*$,this.g+=(J.g-this.g)*$,this.b+=(J.b-this.b)*$,this}lerpColors(J,$,Q){return this.r=J.r+($.r-J.r)*Q,this.g=J.g+($.g-J.g)*Q,this.b=J.b+($.b-J.b)*Q,this}lerpHSL(J,$){this.getHSL(BJ),J.getHSL(N6);let Q=f8(BJ.h,N6.h,$),Z=f8(BJ.s,N6.s,$),W=f8(BJ.l,N6.l,$);return this.setHSL(Q,Z,W),this}setFromVector3(J){return this.r=J.x,this.g=J.y,this.b=J.z,this}applyMatrix3(J){let $=this.r,Q=this.g,Z=this.b,W=J.elements;return this.r=W[0]*$+W[3]*Q+W[6]*Z,this.g=W[1]*$+W[4]*Q+W[7]*Z,this.b=W[2]*$+W[5]*Q+W[8]*Z,this}equals(J){return J.r===this.r&&J.g===this.g&&J.b===this.b}fromArray(J,$=0){return this.r=J[$],this.g=J[$+1],this.b=J[$+2],this}toArray(J=[],$=0){return J[$]=this.r,J[$+1]=this.g,J[$+2]=this.b,J}fromBufferAttribute(J,$){return this.r=J.getX($),this.g=J.getY($),this.b=J.getZ($),this}toJSON(){return this.getHex()}*[Symbol.iterator](){yield this.r,yield this.g,yield this.b}}var z9=new n0;n0.NAMES=cZ;var RK=0;class TJ extends kJ{constructor(){super();this.isMaterial=!0,Object.defineProperty(this,"id",{value:RK++}),this.uuid=L8(),this.name="",this.type="Material",this.blending=1,this.side=0,this.vertexColors=!1,this.opacity=1,this.transparent=!1,this.alphaHash=!1,this.blendSrc=204,this.blendDst=205,this.blendEquation=100,this.blendSrcAlpha=null,this.blendDstAlpha=null,this.blendEquationAlpha=null,this.blendColor=new n0(0,0,0),this.blendAlpha=0,this.depthFunc=3,this.depthTest=!0,this.depthWrite=!0,this.stencilWriteMask=255,this.stencilFunc=519,this.stencilRef=0,this.stencilFuncMask=255,this.stencilFail=7680,this.stencilZFail=7680,this.stencilZPass=7680,this.stencilWrite=!1,this.clippingPlanes=null,this.clipIntersection=!1,this.clipShadows=!1,this.shadowSide=null,this.colorWrite=!0,this.precision=null,this.polygonOffset=!1,this.polygonOffsetFactor=0,this.polygonOffsetUnits=0,this.dithering=!1,this.alphaToCoverage=!1,this.premultipliedAlpha=!1,this.forceSinglePass=!1,this.allowOverride=!0,this.visible=!0,this.toneMapped=!0,this.userData={},this.version=0,this._alphaTest=0}get alphaTest(){return this._alphaTest}set alphaTest(J){if(this._alphaTest>0!==J>0)this.version++;this._alphaTest=J}onBeforeRender(){}onBeforeCompile(){}customProgramCacheKey(){return this.onBeforeCompile.toString()}setValues(J){if(J===void 0)return;for(let $ in J){let Q=J[$];if(Q===void 0){console.warn(`THREE.Material: parameter '${$}' has value of undefined.`);continue}let Z=this[$];if(Z===void 0){console.warn(`THREE.Material: '${$}' is not a property of THREE.${this.type}.`);continue}if(Z&&Z.isColor)Z.set(Q);else if(Z&&Z.isVector3&&(Q&&Q.isVector3))Z.copy(Q);else this[$]=Q}}toJSON(J){let $=J===void 0||typeof J==="string";if($)J={textures:{},images:{}};let Q={metadata:{version:4.7,type:"Material",generator:"Material.toJSON"}};if(Q.uuid=this.uuid,Q.type=this.type,this.name!=="")Q.name=this.name;if(this.color&&this.color.isColor)Q.color=this.color.getHex();if(this.roughness!==void 0)Q.roughness=this.roughness;if(this.metalness!==void 0)Q.metalness=this.metalness;if(this.sheen!==void 0)Q.sheen=this.sheen;if(this.sheenColor&&this.sheenColor.isColor)Q.sheenColor=this.sheenColor.getHex();if(this.sheenRoughness!==void 0)Q.sheenRoughness=this.sheenRoughness;if(this.emissive&&this.emissive.isColor)Q.emissive=this.emissive.getHex();if(this.emissiveIntensity!==void 0&&this.emissiveIntensity!==1)Q.emissiveIntensity=this.emissiveIntensity;if(this.specular&&this.specular.isColor)Q.specular=this.specular.getHex();if(this.specularIntensity!==void 0)Q.specularIntensity=this.specularIntensity;if(this.specularColor&&this.specularColor.isColor)Q.specularColor=this.specularColor.getHex();if(this.shininess!==void 0)Q.shininess=this.shininess;if(this.clearcoat!==void 0)Q.clearcoat=this.clearcoat;if(this.clearcoatRoughness!==void 0)Q.clearcoatRoughness=this.clearcoatRoughness;if(this.clearcoatMap&&this.clearcoatMap.isTexture)Q.clearcoatMap=this.clearcoatMap.toJSON(J).uuid;if(this.clearcoatRoughnessMap&&this.clearcoatRoughnessMap.isTexture)Q.clearcoatRoughnessMap=this.clearcoatRoughnessMap.toJSON(J).uuid;if(this.clearcoatNormalMap&&this.clearcoatNormalMap.isTexture)Q.clearcoatNormalMap=this.clearcoatNormalMap.toJSON(J).uuid,Q.clearcoatNormalScale=this.clearcoatNormalScale.toArray();if(this.sheenColorMap&&this.sheenColorMap.isTexture)Q.sheenColorMap=this.sheenColorMap.toJSON(J).uuid;if(this.sheenRoughnessMap&&this.sheenRoughnessMap.isTexture)Q.sheenRoughnessMap=this.sheenRoughnessMap.toJSON(J).uuid;if(this.dispersion!==void 0)Q.dispersion=this.dispersion;if(this.iridescence!==void 0)Q.iridescence=this.iridescence;if(this.iridescenceIOR!==void 0)Q.iridescenceIOR=this.iridescenceIOR;if(this.iridescenceThicknessRange!==void 0)Q.iridescenceThicknessRange=this.iridescenceThicknessRange;if(this.iridescenceMap&&this.iridescenceMap.isTexture)Q.iridescenceMap=this.iridescenceMap.toJSON(J).uuid;if(this.iridescenceThicknessMap&&this.iridescenceThicknessMap.isTexture)Q.iridescenceThicknessMap=this.iridescenceThicknessMap.toJSON(J).uuid;if(this.anisotropy!==void 0)Q.anisotropy=this.anisotropy;if(this.anisotropyRotation!==void 0)Q.anisotropyRotation=this.anisotropyRotation;if(this.anisotropyMap&&this.anisotropyMap.isTexture)Q.anisotropyMap=this.anisotropyMap.toJSON(J).uuid;if(this.map&&this.map.isTexture)Q.map=this.map.toJSON(J).uuid;if(this.matcap&&this.matcap.isTexture)Q.matcap=this.matcap.toJSON(J).uuid;if(this.alphaMap&&this.alphaMap.isTexture)Q.alphaMap=this.alphaMap.toJSON(J).uuid;if(this.lightMap&&this.lightMap.isTexture)Q.lightMap=this.lightMap.toJSON(J).uuid,Q.lightMapIntensity=this.lightMapIntensity;if(this.aoMap&&this.aoMap.isTexture)Q.aoMap=this.aoMap.toJSON(J).uuid,Q.aoMapIntensity=this.aoMapIntensity;if(this.bumpMap&&this.bumpMap.isTexture)Q.bumpMap=this.bumpMap.toJSON(J).uuid,Q.bumpScale=this.bumpScale;if(this.normalMap&&this.normalMap.isTexture)Q.normalMap=this.normalMap.toJSON(J).uuid,Q.normalMapType=this.normalMapType,Q.normalScale=this.normalScale.toArray();if(this.displacementMap&&this.displacementMap.isTexture)Q.displacementMap=this.displacementMap.toJSON(J).uuid,Q.displacementScale=this.displacementScale,Q.displacementBias=this.displacementBias;if(this.roughnessMap&&this.roughnessMap.isTexture)Q.roughnessMap=this.roughnessMap.toJSON(J).uuid;if(this.metalnessMap&&this.metalnessMap.isTexture)Q.metalnessMap=this.metalnessMap.toJSON(J).uuid;if(this.emissiveMap&&this.emissiveMap.isTexture)Q.emissiveMap=this.emissiveMap.toJSON(J).uuid;if(this.specularMap&&this.specularMap.isTexture)Q.specularMap=this.specularMap.toJSON(J).uuid;if(this.specularIntensityMap&&this.specularIntensityMap.isTexture)Q.specularIntensityMap=this.specularIntensityMap.toJSON(J).uuid;if(this.specularColorMap&&this.specularColorMap.isTexture)Q.specularColorMap=this.specularColorMap.toJSON(J).uuid;if(this.envMap&&this.envMap.isTexture){if(Q.envMap=this.envMap.toJSON(J).uuid,this.combine!==void 0)Q.combine=this.combine}if(this.envMapRotation!==void 0)Q.envMapRotation=this.envMapRotation.toArray();if(this.envMapIntensity!==void 0)Q.envMapIntensity=this.envMapIntensity;if(this.reflectivity!==void 0)Q.reflectivity=this.reflectivity;if(this.refractionRatio!==void 0)Q.refractionRatio=this.refractionRatio;if(this.gradientMap&&this.gradientMap.isTexture)Q.gradientMap=this.gradientMap.toJSON(J).uuid;if(this.transmission!==void 0)Q.transmission=this.transmission;if(this.transmissionMap&&this.transmissionMap.isTexture)Q.transmissionMap=this.transmissionMap.toJSON(J).uuid;if(this.thickness!==void 0)Q.thickness=this.thickness;if(this.thicknessMap&&this.thicknessMap.isTexture)Q.thicknessMap=this.thicknessMap.toJSON(J).uuid;if(this.attenuationDistance!==void 0&&this.attenuationDistance!==1/0)Q.attenuationDistance=this.attenuationDistance;if(this.attenuationColor!==void 0)Q.attenuationColor=this.attenuationColor.getHex();if(this.size!==void 0)Q.size=this.size;if(this.shadowSide!==null)Q.shadowSide=this.shadowSide;if(this.sizeAttenuation!==void 0)Q.sizeAttenuation=this.sizeAttenuation;if(this.blending!==1)Q.blending=this.blending;if(this.side!==0)Q.side=this.side;if(this.vertexColors===!0)Q.vertexColors=!0;if(this.opacity<1)Q.opacity=this.opacity;if(this.transparent===!0)Q.transparent=!0;if(this.blendSrc!==204)Q.blendSrc=this.blendSrc;if(this.blendDst!==205)Q.blendDst=this.blendDst;if(this.blendEquation!==100)Q.blendEquation=this.blendEquation;if(this.blendSrcAlpha!==null)Q.blendSrcAlpha=this.blendSrcAlpha;if(this.blendDstAlpha!==null)Q.blendDstAlpha=this.blendDstAlpha;if(this.blendEquationAlpha!==null)Q.blendEquationAlpha=this.blendEquationAlpha;if(this.blendColor&&this.blendColor.isColor)Q.blendColor=this.blendColor.getHex();if(this.blendAlpha!==0)Q.blendAlpha=this.blendAlpha;if(this.depthFunc!==3)Q.depthFunc=this.depthFunc;if(this.depthTest===!1)Q.depthTest=this.depthTest;if(this.depthWrite===!1)Q.depthWrite=this.depthWrite;if(this.colorWrite===!1)Q.colorWrite=this.colorWrite;if(this.stencilWriteMask!==255)Q.stencilWriteMask=this.stencilWriteMask;if(this.stencilFunc!==519)Q.stencilFunc=this.stencilFunc;if(this.stencilRef!==0)Q.stencilRef=this.stencilRef;if(this.stencilFuncMask!==255)Q.stencilFuncMask=this.stencilFuncMask;if(this.stencilFail!==7680)Q.stencilFail=this.stencilFail;if(this.stencilZFail!==7680)Q.stencilZFail=this.stencilZFail;if(this.stencilZPass!==7680)Q.stencilZPass=this.stencilZPass;if(this.stencilWrite===!0)Q.stencilWrite=this.stencilWrite;if(this.rotation!==void 0&&this.rotation!==0)Q.rotation=this.rotation;if(this.polygonOffset===!0)Q.polygonOffset=!0;if(this.polygonOffsetFactor!==0)Q.polygonOffsetFactor=this.polygonOffsetFactor;if(this.polygonOffsetUnits!==0)Q.polygonOffsetUnits=this.polygonOffsetUnits;if(this.linewidth!==void 0&&this.linewidth!==1)Q.linewidth=this.linewidth;if(this.dashSize!==void 0)Q.dashSize=this.dashSize;if(this.gapSize!==void 0)Q.gapSize=this.gapSize;if(this.scale!==void 0)Q.scale=this.scale;if(this.dithering===!0)Q.dithering=!0;if(this.alphaTest>0)Q.alphaTest=this.alphaTest;if(this.alphaHash===!0)Q.alphaHash=!0;if(this.alphaToCoverage===!0)Q.alphaToCoverage=!0;if(this.premultipliedAlpha===!0)Q.premultipliedAlpha=!0;if(this.forceSinglePass===!0)Q.forceSinglePass=!0;if(this.wireframe===!0)Q.wireframe=!0;if(this.wireframeLinewidth>1)Q.wireframeLinewidth=this.wireframeLinewidth;if(this.wireframeLinecap!=="round")Q.wireframeLinecap=this.wireframeLinecap;if(this.wireframeLinejoin!=="round")Q.wireframeLinejoin=this.wireframeLinejoin;if(this.flatShading===!0)Q.flatShading=!0;if(this.visible===!1)Q.visible=!1;if(this.toneMapped===!1)Q.toneMapped=!1;if(this.fog===!1)Q.fog=!1;if(Object.keys(this.userData).length>0)Q.userData=this.userData;function Z(W){let K=[];for(let H in W){let Y=W[H];delete Y.metadata,K.push(Y)}return K}if($){let W=Z(J.textures),K=Z(J.images);if(W.length>0)Q.textures=W;if(K.length>0)Q.images=K}return Q}clone(){return new this.constructor().copy(this)}copy(J){this.name=J.name,this.blending=J.blending,this.side=J.side,this.vertexColors=J.vertexColors,this.opacity=J.opacity,this.transparent=J.transparent,this.blendSrc=J.blendSrc,this.blendDst=J.blendDst,this.blendEquation=J.blendEquation,this.blendSrcAlpha=J.blendSrcAlpha,this.blendDstAlpha=J.blendDstAlpha,this.blendEquationAlpha=J.blendEquationAlpha,this.blendColor.copy(J.blendColor),this.blendAlpha=J.blendAlpha,this.depthFunc=J.depthFunc,this.depthTest=J.depthTest,this.depthWrite=J.depthWrite,this.stencilWriteMask=J.stencilWriteMask,this.stencilFunc=J.stencilFunc,this.stencilRef=J.stencilRef,this.stencilFuncMask=J.stencilFuncMask,this.stencilFail=J.stencilFail,this.stencilZFail=J.stencilZFail,this.stencilZPass=J.stencilZPass,this.stencilWrite=J.stencilWrite;let $=J.clippingPlanes,Q=null;if($!==null){let Z=$.length;Q=Array(Z);for(let W=0;W!==Z;++W)Q[W]=$[W].clone()}return this.clippingPlanes=Q,this.clipIntersection=J.clipIntersection,this.clipShadows=J.clipShadows,this.shadowSide=J.shadowSide,this.colorWrite=J.colorWrite,this.precision=J.precision,this.polygonOffset=J.polygonOffset,this.polygonOffsetFactor=J.polygonOffsetFactor,this.polygonOffsetUnits=J.polygonOffsetUnits,this.dithering=J.dithering,this.alphaTest=J.alphaTest,this.alphaHash=J.alphaHash,this.alphaToCoverage=J.alphaToCoverage,this.premultipliedAlpha=J.premultipliedAlpha,this.forceSinglePass=J.forceSinglePass,this.visible=J.visible,this.toneMapped=J.toneMapped,this.userData=JSON.parse(JSON.stringify(J.userData)),this}dispose(){this.dispatchEvent({type:"dispose"})}set needsUpdate(J){if(J===!0)this.version++}}class lJ extends TJ{constructor(J){super();this.isMeshBasicMaterial=!0,this.type="MeshBasicMaterial",this.color=new n0(16777215),this.map=null,this.lightMap=null,this.lightMapIntensity=1,this.aoMap=null,this.aoMapIntensity=1,this.specularMap=null,this.alphaMap=null,this.envMap=null,this.envMapRotation=new t9,this.combine=0,this.reflectivity=1,this.refractionRatio=0.98,this.wireframe=!1,this.wireframeLinewidth=1,this.wireframeLinecap="round",this.wireframeLinejoin="round",this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.color.copy(J.color),this.map=J.map,this.lightMap=J.lightMap,this.lightMapIntensity=J.lightMapIntensity,this.aoMap=J.aoMap,this.aoMapIntensity=J.aoMapIntensity,this.specularMap=J.specularMap,this.alphaMap=J.alphaMap,this.envMap=J.envMap,this.envMapRotation.copy(J.envMapRotation),this.combine=J.combine,this.reflectivity=J.reflectivity,this.refractionRatio=J.refractionRatio,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.wireframeLinecap=J.wireframeLinecap,this.wireframeLinejoin=J.wireframeLinejoin,this.fog=J.fog,this}}var O9=new v,U6=new x0,kK=0;class n9{constructor(J,$,Q=!1){if(Array.isArray(J))throw TypeError("THREE.BufferAttribute: array should be a Typed Array.");this.isBufferAttribute=!0,Object.defineProperty(this,"id",{value:kK++}),this.name="",this.array=J,this.itemSize=$,this.count=J!==void 0?J.length/$:0,this.normalized=Q,this.usage=35044,this.updateRanges=[],this.gpuType=1015,this.version=0}onUploadCallback(){}set needsUpdate(J){if(J===!0)this.version++}setUsage(J){return this.usage=J,this}addUpdateRange(J,$){this.updateRanges.push({start:J,count:$})}clearUpdateRanges(){this.updateRanges.length=0}copy(J){return this.name=J.name,this.array=new J.array.constructor(J.array),this.itemSize=J.itemSize,this.count=J.count,this.normalized=J.normalized,this.usage=J.usage,this.gpuType=J.gpuType,this}copyAt(J,$,Q){J*=this.itemSize,Q*=$.itemSize;for(let Z=0,W=this.itemSize;Z<W;Z++)this.array[J+Z]=$.array[Q+Z];return this}copyArray(J){return this.array.set(J),this}applyMatrix3(J){if(this.itemSize===2)for(let $=0,Q=this.count;$<Q;$++)U6.fromBufferAttribute(this,$),U6.applyMatrix3(J),this.setXY($,U6.x,U6.y);else if(this.itemSize===3)for(let $=0,Q=this.count;$<Q;$++)O9.fromBufferAttribute(this,$),O9.applyMatrix3(J),this.setXYZ($,O9.x,O9.y,O9.z);return this}applyMatrix4(J){for(let $=0,Q=this.count;$<Q;$++)O9.fromBufferAttribute(this,$),O9.applyMatrix4(J),this.setXYZ($,O9.x,O9.y,O9.z);return this}applyNormalMatrix(J){for(let $=0,Q=this.count;$<Q;$++)O9.fromBufferAttribute(this,$),O9.applyNormalMatrix(J),this.setXYZ($,O9.x,O9.y,O9.z);return this}transformDirection(J){for(let $=0,Q=this.count;$<Q;$++)O9.fromBufferAttribute(this,$),O9.transformDirection(J),this.setXYZ($,O9.x,O9.y,O9.z);return this}set(J,$=0){return this.array.set(J,$),this}getComponent(J,$){let Q=this.array[J*this.itemSize+$];if(this.normalized)Q=N8(Q,this.array);return Q}setComponent(J,$,Q){if(this.normalized)Q=_9(Q,this.array);return this.array[J*this.itemSize+$]=Q,this}getX(J){let $=this.array[J*this.itemSize];if(this.normalized)$=N8($,this.array);return $}setX(J,$){if(this.normalized)$=_9($,this.array);return this.array[J*this.itemSize]=$,this}getY(J){let $=this.array[J*this.itemSize+1];if(this.normalized)$=N8($,this.array);return $}setY(J,$){if(this.normalized)$=_9($,this.array);return this.array[J*this.itemSize+1]=$,this}getZ(J){let $=this.array[J*this.itemSize+2];if(this.normalized)$=N8($,this.array);return $}setZ(J,$){if(this.normalized)$=_9($,this.array);return this.array[J*this.itemSize+2]=$,this}getW(J){let $=this.array[J*this.itemSize+3];if(this.normalized)$=N8($,this.array);return $}setW(J,$){if(this.normalized)$=_9($,this.array);return this.array[J*this.itemSize+3]=$,this}setXY(J,$,Q){if(J*=this.itemSize,this.normalized)$=_9($,this.array),Q=_9(Q,this.array);return this.array[J+0]=$,this.array[J+1]=Q,this}setXYZ(J,$,Q,Z){if(J*=this.itemSize,this.normalized)$=_9($,this.array),Q=_9(Q,this.array),Z=_9(Z,this.array);return this.array[J+0]=$,this.array[J+1]=Q,this.array[J+2]=Z,this}setXYZW(J,$,Q,Z,W){if(J*=this.itemSize,this.normalized)$=_9($,this.array),Q=_9(Q,this.array),Z=_9(Z,this.array),W=_9(W,this.array);return this.array[J+0]=$,this.array[J+1]=Q,this.array[J+2]=Z,this.array[J+3]=W,this}onUpload(J){return this.onUploadCallback=J,this}clone(){return new this.constructor(this.array,this.itemSize).copy(this)}toJSON(){let J={itemSize:this.itemSize,type:this.array.constructor.name,array:Array.from(this.array),normalized:this.normalized};if(this.name!=="")J.name=this.name;if(this.usage!==35044)J.usage=this.usage;return J}}class p6 extends n9{constructor(J,$,Q){super(new Uint16Array(J),$,Q)}}class l6 extends n9{constructor(J,$,Q){super(new Uint32Array(J),$,Q)}}class B9 extends n9{constructor(J,$,Q){super(new Float32Array(J),$,Q)}}var FK=0,u9=new X9,A7=new R9,Y8=new v,x9=new WJ,j8=new WJ,L9=new v;class l9 extends kJ{constructor(){super();this.isBufferGeometry=!0,Object.defineProperty(this,"id",{value:FK++}),this.uuid=L8(),this.name="",this.type="BufferGeometry",this.index=null,this.indirect=null,this.attributes={},this.morphAttributes={},this.morphTargetsRelative=!1,this.groups=[],this.boundingBox=null,this.boundingSphere=null,this.drawRange={start:0,count:1/0},this.userData={}}getIndex(){return this.index}setIndex(J){if(Array.isArray(J))this.index=new((_$(J))?l6:p6)(J,1);else this.index=J;return this}setIndirect(J){return this.indirect=J,this}getIndirect(){return this.indirect}getAttribute(J){return this.attributes[J]}setAttribute(J,$){return this.attributes[J]=$,this}deleteAttribute(J){return delete this.attributes[J],this}hasAttribute(J){return this.attributes[J]!==void 0}addGroup(J,$,Q=0){this.groups.push({start:J,count:$,materialIndex:Q})}clearGroups(){this.groups=[]}setDrawRange(J,$){this.drawRange.start=J,this.drawRange.count=$}applyMatrix4(J){let $=this.attributes.position;if($!==void 0)$.applyMatrix4(J),$.needsUpdate=!0;let Q=this.attributes.normal;if(Q!==void 0){let W=new p0().getNormalMatrix(J);Q.applyNormalMatrix(W),Q.needsUpdate=!0}let Z=this.attributes.tangent;if(Z!==void 0)Z.transformDirection(J),Z.needsUpdate=!0;if(this.boundingBox!==null)this.computeBoundingBox();if(this.boundingSphere!==null)this.computeBoundingSphere();return this}applyQuaternion(J){return u9.makeRotationFromQuaternion(J),this.applyMatrix4(u9),this}rotateX(J){return u9.makeRotationX(J),this.applyMatrix4(u9),this}rotateY(J){return u9.makeRotationY(J),this.applyMatrix4(u9),this}rotateZ(J){return u9.makeRotationZ(J),this.applyMatrix4(u9),this}translate(J,$,Q){return u9.makeTranslation(J,$,Q),this.applyMatrix4(u9),this}scale(J,$,Q){return u9.makeScale(J,$,Q),this.applyMatrix4(u9),this}lookAt(J){return A7.lookAt(J),A7.updateMatrix(),this.applyMatrix4(A7.matrix),this}center(){return this.computeBoundingBox(),this.boundingBox.getCenter(Y8).negate(),this.translate(Y8.x,Y8.y,Y8.z),this}setFromPoints(J){let $=this.getAttribute("position");if($===void 0){let Q=[];for(let Z=0,W=J.length;Z<W;Z++){let K=J[Z];Q.push(K.x,K.y,K.z||0)}this.setAttribute("position",new B9(Q,3))}else{let Q=Math.min(J.length,$.count);for(let Z=0;Z<Q;Z++){let W=J[Z];$.setXYZ(Z,W.x,W.y,W.z||0)}if(J.length>$.count)console.warn("THREE.BufferGeometry: Buffer size too small for points data. Use .dispose() and create a new geometry.");$.needsUpdate=!0}return this}computeBoundingBox(){if(this.boundingBox===null)this.boundingBox=new WJ;let J=this.attributes.position,$=this.morphAttributes.position;if(J&&J.isGLBufferAttribute){console.error("THREE.BufferGeometry.computeBoundingBox(): GLBufferAttribute requires a manual bounding box.",this),this.boundingBox.set(new v(-1/0,-1/0,-1/0),new v(1/0,1/0,1/0));return}if(J!==void 0){if(this.boundingBox.setFromBufferAttribute(J),$)for(let Q=0,Z=$.length;Q<Z;Q++){let W=$[Q];if(x9.setFromBufferAttribute(W),this.morphTargetsRelative)L9.addVectors(this.boundingBox.min,x9.min),this.boundingBox.expandByPoint(L9),L9.addVectors(this.boundingBox.max,x9.max),this.boundingBox.expandByPoint(L9);else this.boundingBox.expandByPoint(x9.min),this.boundingBox.expandByPoint(x9.max)}}else this.boundingBox.makeEmpty();if(isNaN(this.boundingBox.min.x)||isNaN(this.boundingBox.min.y)||isNaN(this.boundingBox.min.z))console.error('THREE.BufferGeometry.computeBoundingBox(): Computed min/max have NaN values. The "position" attribute is likely to have NaN values.',this)}computeBoundingSphere(){if(this.boundingSphere===null)this.boundingSphere=new V8;let J=this.attributes.position,$=this.morphAttributes.position;if(J&&J.isGLBufferAttribute){console.error("THREE.BufferGeometry.computeBoundingSphere(): GLBufferAttribute requires a manual bounding sphere.",this),this.boundingSphere.set(new v,1/0);return}if(J){let Q=this.boundingSphere.center;if(x9.setFromBufferAttribute(J),$)for(let W=0,K=$.length;W<K;W++){let H=$[W];if(j8.setFromBufferAttribute(H),this.morphTargetsRelative)L9.addVectors(x9.min,j8.min),x9.expandByPoint(L9),L9.addVectors(x9.max,j8.max),x9.expandByPoint(L9);else x9.expandByPoint(j8.min),x9.expandByPoint(j8.max)}x9.getCenter(Q);let Z=0;for(let W=0,K=J.count;W<K;W++)L9.fromBufferAttribute(J,W),Z=Math.max(Z,Q.distanceToSquared(L9));if($)for(let W=0,K=$.length;W<K;W++){let H=$[W],Y=this.morphTargetsRelative;for(let X=0,N=H.count;X<N;X++){if(L9.fromBufferAttribute(H,X),Y)Y8.fromBufferAttribute(J,X),L9.add(Y8);Z=Math.max(Z,Q.distanceToSquared(L9))}}if(this.boundingSphere.radius=Math.sqrt(Z),isNaN(this.boundingSphere.radius))console.error('THREE.BufferGeometry.computeBoundingSphere(): Computed radius is NaN. The "position" attribute is likely to have NaN values.',this)}}computeTangents(){let J=this.index,$=this.attributes;if(J===null||$.position===void 0||$.normal===void 0||$.uv===void 0){console.error("THREE.BufferGeometry: .computeTangents() failed. Missing required attributes (index, position, normal or uv)");return}let{position:Q,normal:Z,uv:W}=$;if(this.hasAttribute("tangent")===!1)this.setAttribute("tangent",new n9(new Float32Array(4*Q.count),4));let K=this.getAttribute("tangent"),H=[],Y=[];for(let S=0;S<Q.count;S++)H[S]=new v,Y[S]=new v;let X=new v,N=new v,U=new v,G=new x0,D=new x0,R=new x0,M=new v,L=new v;function E(S,g,z){X.fromBufferAttribute(Q,S),N.fromBufferAttribute(Q,g),U.fromBufferAttribute(Q,z),G.fromBufferAttribute(W,S),D.fromBufferAttribute(W,g),R.fromBufferAttribute(W,z),N.sub(X),U.sub(X),D.sub(G),R.sub(G);let F=1/(D.x*R.y-R.x*D.y);if(!isFinite(F))return;M.copy(N).multiplyScalar(R.y).addScaledVector(U,-D.y).multiplyScalar(F),L.copy(U).multiplyScalar(D.x).addScaledVector(N,-R.x).multiplyScalar(F),H[S].add(M),H[g].add(M),H[z].add(M),Y[S].add(L),Y[g].add(L),Y[z].add(L)}let q=this.groups;if(q.length===0)q=[{start:0,count:J.count}];for(let S=0,g=q.length;S<g;++S){let z=q[S],F=z.start,T=z.count;for(let n=F,s=F+T;n<s;n+=3)E(J.getX(n+0),J.getX(n+1),J.getX(n+2))}let A=new v,V=new v,I=new v,f=new v;function P(S){I.fromBufferAttribute(Z,S),f.copy(I);let g=H[S];A.copy(g),A.sub(I.multiplyScalar(I.dot(g))).normalize(),V.crossVectors(f,g);let F=V.dot(Y[S])<0?-1:1;K.setXYZW(S,A.x,A.y,A.z,F)}for(let S=0,g=q.length;S<g;++S){let z=q[S],F=z.start,T=z.count;for(let n=F,s=F+T;n<s;n+=3)P(J.getX(n+0)),P(J.getX(n+1)),P(J.getX(n+2))}}computeVertexNormals(){let J=this.index,$=this.getAttribute("position");if($!==void 0){let Q=this.getAttribute("normal");if(Q===void 0)Q=new n9(new Float32Array($.count*3),3),this.setAttribute("normal",Q);else for(let G=0,D=Q.count;G<D;G++)Q.setXYZ(G,0,0,0);let Z=new v,W=new v,K=new v,H=new v,Y=new v,X=new v,N=new v,U=new v;if(J)for(let G=0,D=J.count;G<D;G+=3){let R=J.getX(G+0),M=J.getX(G+1),L=J.getX(G+2);Z.fromBufferAttribute($,R),W.fromBufferAttribute($,M),K.fromBufferAttribute($,L),N.subVectors(K,W),U.subVectors(Z,W),N.cross(U),H.fromBufferAttribute(Q,R),Y.fromBufferAttribute(Q,M),X.fromBufferAttribute(Q,L),H.add(N),Y.add(N),X.add(N),Q.setXYZ(R,H.x,H.y,H.z),Q.setXYZ(M,Y.x,Y.y,Y.z),Q.setXYZ(L,X.x,X.y,X.z)}else for(let G=0,D=$.count;G<D;G+=3)Z.fromBufferAttribute($,G+0),W.fromBufferAttribute($,G+1),K.fromBufferAttribute($,G+2),N.subVectors(K,W),U.subVectors(Z,W),N.cross(U),Q.setXYZ(G+0,N.x,N.y,N.z),Q.setXYZ(G+1,N.x,N.y,N.z),Q.setXYZ(G+2,N.x,N.y,N.z);this.normalizeNormals(),Q.needsUpdate=!0}}normalizeNormals(){let J=this.attributes.normal;for(let $=0,Q=J.count;$<Q;$++)L9.fromBufferAttribute(J,$),L9.normalize(),J.setXYZ($,L9.x,L9.y,L9.z)}toNonIndexed(){function J(H,Y){let{array:X,itemSize:N,normalized:U}=H,G=new X.constructor(Y.length*N),D=0,R=0;for(let M=0,L=Y.length;M<L;M++){if(H.isInterleavedBufferAttribute)D=Y[M]*H.data.stride+H.offset;else D=Y[M]*N;for(let E=0;E<N;E++)G[R++]=X[D++]}return new n9(G,N,U)}if(this.index===null)return console.warn("THREE.BufferGeometry.toNonIndexed(): BufferGeometry is already non-indexed."),this;let $=new l9,Q=this.index.array,Z=this.attributes;for(let H in Z){let Y=Z[H],X=J(Y,Q);$.setAttribute(H,X)}let W=this.morphAttributes;for(let H in W){let Y=[],X=W[H];for(let N=0,U=X.length;N<U;N++){let G=X[N],D=J(G,Q);Y.push(D)}$.morphAttributes[H]=Y}$.morphTargetsRelative=this.morphTargetsRelative;let K=this.groups;for(let H=0,Y=K.length;H<Y;H++){let X=K[H];$.addGroup(X.start,X.count,X.materialIndex)}return $}toJSON(){let J={metadata:{version:4.7,type:"BufferGeometry",generator:"BufferGeometry.toJSON"}};if(J.uuid=this.uuid,J.type=this.type,this.name!=="")J.name=this.name;if(Object.keys(this.userData).length>0)J.userData=this.userData;if(this.parameters!==void 0){let Y=this.parameters;for(let X in Y)if(Y[X]!==void 0)J[X]=Y[X];return J}J.data={attributes:{}};let $=this.index;if($!==null)J.data.index={type:$.array.constructor.name,array:Array.prototype.slice.call($.array)};let Q=this.attributes;for(let Y in Q){let X=Q[Y];J.data.attributes[Y]=X.toJSON(J.data)}let Z={},W=!1;for(let Y in this.morphAttributes){let X=this.morphAttributes[Y],N=[];for(let U=0,G=X.length;U<G;U++){let D=X[U];N.push(D.toJSON(J.data))}if(N.length>0)Z[Y]=N,W=!0}if(W)J.data.morphAttributes=Z,J.data.morphTargetsRelative=this.morphTargetsRelative;let K=this.groups;if(K.length>0)J.data.groups=JSON.parse(JSON.stringify(K));let H=this.boundingSphere;if(H!==null)J.data.boundingSphere=H.toJSON();return J}clone(){return new this.constructor().copy(this)}copy(J){this.index=null,this.attributes={},this.morphAttributes={},this.groups=[],this.boundingBox=null,this.boundingSphere=null;let $={};this.name=J.name;let Q=J.index;if(Q!==null)this.setIndex(Q.clone());let Z=J.attributes;for(let X in Z){let N=Z[X];this.setAttribute(X,N.clone($))}let W=J.morphAttributes;for(let X in W){let N=[],U=W[X];for(let G=0,D=U.length;G<D;G++)N.push(U[G].clone($));this.morphAttributes[X]=N}this.morphTargetsRelative=J.morphTargetsRelative;let K=J.groups;for(let X=0,N=K.length;X<N;X++){let U=K[X];this.addGroup(U.start,U.count,U.materialIndex)}let H=J.boundingBox;if(H!==null)this.boundingBox=H.clone();let Y=J.boundingSphere;if(Y!==null)this.boundingSphere=Y.clone();return this.drawRange.start=J.drawRange.start,this.drawRange.count=J.drawRange.count,this.userData=J.userData,this}dispose(){this.dispatchEvent({type:"dispose"})}}var AQ=new X9,fJ=new pJ,G6=new V8,TQ=new v,E6=new v,D6=new v,q6=new v,T7=new v,O6=new v,SQ=new v,R6=new v;class A9 extends R9{constructor(J=new l9,$=new lJ){super();this.isMesh=!0,this.type="Mesh",this.geometry=J,this.material=$,this.morphTargetDictionary=void 0,this.morphTargetInfluences=void 0,this.count=1,this.updateMorphTargets()}copy(J,$){if(super.copy(J,$),J.morphTargetInfluences!==void 0)this.morphTargetInfluences=J.morphTargetInfluences.slice();if(J.morphTargetDictionary!==void 0)this.morphTargetDictionary=Object.assign({},J.morphTargetDictionary);return this.material=Array.isArray(J.material)?J.material.slice():J.material,this.geometry=J.geometry,this}updateMorphTargets(){let $=this.geometry.morphAttributes,Q=Object.keys($);if(Q.length>0){let Z=$[Q[0]];if(Z!==void 0){this.morphTargetInfluences=[],this.morphTargetDictionary={};for(let W=0,K=Z.length;W<K;W++){let H=Z[W].name||String(W);this.morphTargetInfluences.push(0),this.morphTargetDictionary[H]=W}}}}getVertexPosition(J,$){let Q=this.geometry,Z=Q.attributes.position,W=Q.morphAttributes.position,K=Q.morphTargetsRelative;$.fromBufferAttribute(Z,J);let H=this.morphTargetInfluences;if(W&&H){O6.set(0,0,0);for(let Y=0,X=W.length;Y<X;Y++){let N=H[Y],U=W[Y];if(N===0)continue;if(T7.fromBufferAttribute(U,J),K)O6.addScaledVector(T7,N);else O6.addScaledVector(T7.sub($),N)}$.add(O6)}return $}raycast(J,$){let Q=this.geometry,Z=this.material,W=this.matrixWorld;if(Z===void 0)return;if(Q.boundingSphere===null)Q.computeBoundingSphere();if(G6.copy(Q.boundingSphere),G6.applyMatrix4(W),fJ.copy(J.ray).recast(J.near),G6.containsPoint(fJ.origin)===!1){if(fJ.intersectSphere(G6,TQ)===null)return;if(fJ.origin.distanceToSquared(TQ)>(J.far-J.near)**2)return}if(AQ.copy(W).invert(),fJ.copy(J.ray).applyMatrix4(AQ),Q.boundingBox!==null){if(fJ.intersectsBox(Q.boundingBox)===!1)return}this._computeIntersections(J,$,fJ)}_computeIntersections(J,$,Q){let Z,W=this.geometry,K=this.material,H=W.index,Y=W.attributes.position,X=W.attributes.uv,N=W.attributes.uv1,U=W.attributes.normal,G=W.groups,D=W.drawRange;if(H!==null)if(Array.isArray(K))for(let R=0,M=G.length;R<M;R++){let L=G[R],E=K[L.materialIndex],q=Math.max(L.start,D.start),A=Math.min(H.count,Math.min(L.start+L.count,D.start+D.count));for(let V=q,I=A;V<I;V+=3){let f=H.getX(V),P=H.getX(V+1),S=H.getX(V+2);if(Z=k6(this,E,J,Q,X,N,U,f,P,S),Z)Z.faceIndex=Math.floor(V/3),Z.face.materialIndex=L.materialIndex,$.push(Z)}}else{let R=Math.max(0,D.start),M=Math.min(H.count,D.start+D.count);for(let L=R,E=M;L<E;L+=3){let q=H.getX(L),A=H.getX(L+1),V=H.getX(L+2);if(Z=k6(this,K,J,Q,X,N,U,q,A,V),Z)Z.faceIndex=Math.floor(L/3),$.push(Z)}}else if(Y!==void 0)if(Array.isArray(K))for(let R=0,M=G.length;R<M;R++){let L=G[R],E=K[L.materialIndex],q=Math.max(L.start,D.start),A=Math.min(Y.count,Math.min(L.start+L.count,D.start+D.count));for(let V=q,I=A;V<I;V+=3){let f=V,P=V+1,S=V+2;if(Z=k6(this,E,J,Q,X,N,U,f,P,S),Z)Z.faceIndex=Math.floor(V/3),Z.face.materialIndex=L.materialIndex,$.push(Z)}}else{let R=Math.max(0,D.start),M=Math.min(Y.count,D.start+D.count);for(let L=R,E=M;L<E;L+=3){let q=L,A=L+1,V=L+2;if(Z=k6(this,K,J,Q,X,N,U,q,A,V),Z)Z.faceIndex=Math.floor(L/3),$.push(Z)}}}}function MK(J,$,Q,Z,W,K,H,Y){let X;if($.side===1)X=Z.intersectTriangle(H,K,W,!0,Y);else X=Z.intersectTriangle(W,K,H,$.side===0,Y);if(X===null)return null;R6.copy(Y),R6.applyMatrix4(J.matrixWorld);let N=Q.ray.origin.distanceTo(R6);if(N<Q.near||N>Q.far)return null;return{distance:N,point:R6.clone(),object:J}}function k6(J,$,Q,Z,W,K,H,Y,X,N){J.getVertexPosition(Y,E6),J.getVertexPosition(X,D6),J.getVertexPosition(N,q6);let U=MK(J,$,Q,Z,E6,D6,q6,SQ);if(U){let G=new v;if(c9.getBarycoord(SQ,E6,D6,q6,G),W)U.uv=c9.getInterpolatedAttribute(W,Y,X,N,G,new x0);if(K)U.uv1=c9.getInterpolatedAttribute(K,Y,X,N,G,new x0);if(H){if(U.normal=c9.getInterpolatedAttribute(H,Y,X,N,G,new v),U.normal.dot(Z.direction)>0)U.normal.multiplyScalar(-1)}let D={a:Y,b:X,c:N,normal:new v,materialIndex:0};c9.getNormal(E6,D6,q6,D.normal),U.face=D,U.barycoord=G}return U}class z8 extends l9{constructor(J=1,$=1,Q=1,Z=1,W=1,K=1){super();this.type="BoxGeometry",this.parameters={width:J,height:$,depth:Q,widthSegments:Z,heightSegments:W,depthSegments:K};let H=this;Z=Math.floor(Z),W=Math.floor(W),K=Math.floor(K);let Y=[],X=[],N=[],U=[],G=0,D=0;R("z","y","x",-1,-1,Q,$,J,K,W,0),R("z","y","x",1,-1,Q,$,-J,K,W,1),R("x","z","y",1,1,J,Q,$,Z,K,2),R("x","z","y",1,-1,J,Q,-$,Z,K,3),R("x","y","z",1,-1,J,$,Q,Z,W,4),R("x","y","z",-1,-1,J,$,-Q,Z,W,5),this.setIndex(Y),this.setAttribute("position",new B9(X,3)),this.setAttribute("normal",new B9(N,3)),this.setAttribute("uv",new B9(U,2));function R(M,L,E,q,A,V,I,f,P,S,g){let z=V/P,F=I/S,T=V/2,n=I/2,s=f/2,d=P+1,Z0=S+1,m=0,W0=0,u=new v;for(let N0=0;N0<Z0;N0++){let q0=N0*F-n;for(let _0=0;_0<d;_0++){let g0=_0*z-T;u[M]=g0*q,u[L]=q0*A,u[E]=s,X.push(u.x,u.y,u.z),u[M]=0,u[L]=0,u[E]=f>0?1:-1,N.push(u.x,u.y,u.z),U.push(_0/P),U.push(1-N0/S),m+=1}}for(let N0=0;N0<S;N0++)for(let q0=0;q0<P;q0++){let _0=G+q0+d*N0,g0=G+q0+d*(N0+1),a0=G+(q0+1)+d*(N0+1),m0=G+(q0+1)+d*N0;Y.push(_0,g0,m0),Y.push(g0,a0,m0),W0+=6}H.addGroup(D,W0,g),D+=W0,G+=m}}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new z8(J.width,J.height,J.depth,J.widthSegments,J.heightSegments,J.depthSegments)}}function dJ(J){let $={};for(let Q in J){$[Q]={};for(let Z in J[Q]){let W=J[Q][Z];if(W&&(W.isColor||W.isMatrix3||W.isMatrix4||W.isVector2||W.isVector3||W.isVector4||W.isTexture||W.isQuaternion))if(W.isRenderTargetTexture)console.warn("UniformsUtils: Textures of render targets cannot be cloned via cloneUniforms() or mergeUniforms()."),$[Q][Z]=null;else $[Q][Z]=W.clone();else if(Array.isArray(W))$[Q][Z]=W.slice();else $[Q][Z]=W}}return $}function w9(J){let $={};for(let Q=0;Q<J.length;Q++){let Z=dJ(J[Q]);for(let W in Z)$[W]=Z[W]}return $}function LK(J){let $=[];for(let Q=0;Q<J.length;Q++)$.push(J[Q].clone());return $}function T$(J){let $=J.getRenderTarget();if($===null)return J.outputColorSpace;if($.isXRRenderTarget===!0)return $.texture.colorSpace;return s0.workingColorSpace}var nZ={clone:dJ,merge:w9},VK=`void main() {
	gl_Position = projectionMatrix * modelViewMatrix * vec4( position, 1.0 );
}`,zK=`void main() {
	gl_FragColor = vec4( 1.0, 0.0, 0.0, 1.0 );
}`;class KJ extends TJ{constructor(J){super();if(this.isShaderMaterial=!0,this.type="ShaderMaterial",this.defines={},this.uniforms={},this.uniformsGroups=[],this.vertexShader=VK,this.fragmentShader=zK,this.linewidth=1,this.wireframe=!1,this.wireframeLinewidth=1,this.fog=!1,this.lights=!1,this.clipping=!1,this.forceSinglePass=!0,this.extensions={clipCullDistance:!1,multiDraw:!1},this.defaultAttributeValues={color:[1,1,1],uv:[0,0],uv1:[0,0]},this.index0AttributeName=void 0,this.uniformsNeedUpdate=!1,this.glslVersion=null,J!==void 0)this.setValues(J)}copy(J){return super.copy(J),this.fragmentShader=J.fragmentShader,this.vertexShader=J.vertexShader,this.uniforms=dJ(J.uniforms),this.uniformsGroups=LK(J.uniformsGroups),this.defines=Object.assign({},J.defines),this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.fog=J.fog,this.lights=J.lights,this.clipping=J.clipping,this.extensions=Object.assign({},J.extensions),this.glslVersion=J.glslVersion,this}toJSON(J){let $=super.toJSON(J);$.glslVersion=this.glslVersion,$.uniforms={};for(let Z in this.uniforms){let K=this.uniforms[Z].value;if(K&&K.isTexture)$.uniforms[Z]={type:"t",value:K.toJSON(J).uuid};else if(K&&K.isColor)$.uniforms[Z]={type:"c",value:K.getHex()};else if(K&&K.isVector2)$.uniforms[Z]={type:"v2",value:K.toArray()};else if(K&&K.isVector3)$.uniforms[Z]={type:"v3",value:K.toArray()};else if(K&&K.isVector4)$.uniforms[Z]={type:"v4",value:K.toArray()};else if(K&&K.isMatrix3)$.uniforms[Z]={type:"m3",value:K.toArray()};else if(K&&K.isMatrix4)$.uniforms[Z]={type:"m4",value:K.toArray()};else $.uniforms[Z]={value:K}}if(Object.keys(this.defines).length>0)$.defines=this.defines;$.vertexShader=this.vertexShader,$.fragmentShader=this.fragmentShader,$.lights=this.lights,$.clipping=this.clipping;let Q={};for(let Z in this.extensions)if(this.extensions[Z]===!0)Q[Z]=!0;if(Object.keys(Q).length>0)$.extensions=Q;return $}}class d6 extends R9{constructor(){super();this.isCamera=!0,this.type="Camera",this.matrixWorldInverse=new X9,this.projectionMatrix=new X9,this.projectionMatrixInverse=new X9,this.coordinateSystem=2000,this._reversedDepth=!1}get reversedDepth(){return this._reversedDepth}copy(J,$){return super.copy(J,$),this.matrixWorldInverse.copy(J.matrixWorldInverse),this.projectionMatrix.copy(J.projectionMatrix),this.projectionMatrixInverse.copy(J.projectionMatrixInverse),this.coordinateSystem=J.coordinateSystem,this}getWorldDirection(J){return super.getWorldDirection(J).negate()}updateMatrixWorld(J){super.updateMatrixWorld(J),this.matrixWorldInverse.copy(this.matrixWorld).invert()}updateWorldMatrix(J,$){super.updateWorldMatrix(J,$),this.matrixWorldInverse.copy(this.matrixWorld).invert()}clone(){return new this.constructor().copy(this)}}var wJ=new v,jQ=new x0,yQ=new x0;class I9 extends d6{constructor(J=50,$=1,Q=0.1,Z=2000){super();this.isPerspectiveCamera=!0,this.type="PerspectiveCamera",this.fov=J,this.zoom=1,this.near=Q,this.far=Z,this.focus=10,this.aspect=$,this.view=null,this.filmGauge=35,this.filmOffset=0,this.updateProjectionMatrix()}copy(J,$){return super.copy(J,$),this.fov=J.fov,this.zoom=J.zoom,this.near=J.near,this.far=J.far,this.focus=J.focus,this.aspect=J.aspect,this.view=J.view===null?null:Object.assign({},J.view),this.filmGauge=J.filmGauge,this.filmOffset=J.filmOffset,this}setFocalLength(J){let $=0.5*this.getFilmHeight()/J;this.fov=G8*2*Math.atan($),this.updateProjectionMatrix()}getFocalLength(){let J=Math.tan(b8*0.5*this.fov);return 0.5*this.getFilmHeight()/J}getEffectiveFOV(){return G8*2*Math.atan(Math.tan(b8*0.5*this.fov)/this.zoom)}getFilmWidth(){return this.filmGauge*Math.min(this.aspect,1)}getFilmHeight(){return this.filmGauge/Math.max(this.aspect,1)}getViewBounds(J,$,Q){wJ.set(-1,-1,0.5).applyMatrix4(this.projectionMatrixInverse),$.set(wJ.x,wJ.y).multiplyScalar(-J/wJ.z),wJ.set(1,1,0.5).applyMatrix4(this.projectionMatrixInverse),Q.set(wJ.x,wJ.y).multiplyScalar(-J/wJ.z)}getViewSize(J,$){return this.getViewBounds(J,jQ,yQ),$.subVectors(yQ,jQ)}setViewOffset(J,$,Q,Z,W,K){if(this.aspect=J/$,this.view===null)this.view={enabled:!0,fullWidth:1,fullHeight:1,offsetX:0,offsetY:0,width:1,height:1};this.view.enabled=!0,this.view.fullWidth=J,this.view.fullHeight=$,this.view.offsetX=Q,this.view.offsetY=Z,this.view.width=W,this.view.height=K,this.updateProjectionMatrix()}clearViewOffset(){if(this.view!==null)this.view.enabled=!1;this.updateProjectionMatrix()}updateProjectionMatrix(){let J=this.near,$=J*Math.tan(b8*0.5*this.fov)/this.zoom,Q=2*$,Z=this.aspect*Q,W=-0.5*Z,K=this.view;if(this.view!==null&&this.view.enabled){let{fullWidth:Y,fullHeight:X}=K;W+=K.offsetX*Z/Y,$-=K.offsetY*Q/X,Z*=K.width/Y,Q*=K.height/X}let H=this.filmOffset;if(H!==0)W+=J*H/this.getFilmWidth();this.projectionMatrix.makePerspective(W,W+Z,$,$-Q,J,this.far,this.coordinateSystem,this.reversedDepth),this.projectionMatrixInverse.copy(this.projectionMatrix).invert()}toJSON(J){let $=super.toJSON(J);if($.object.fov=this.fov,$.object.zoom=this.zoom,$.object.near=this.near,$.object.far=this.far,$.object.focus=this.focus,$.object.aspect=this.aspect,this.view!==null)$.object.view=Object.assign({},this.view);return $.object.filmGauge=this.filmGauge,$.object.filmOffset=this.filmOffset,$}}var H8=-90,X8=1;class S$ extends R9{constructor(J,$,Q){super();this.type="CubeCamera",this.renderTarget=Q,this.coordinateSystem=null,this.activeMipmapLevel=0;let Z=new I9(H8,X8,J,$);Z.layers=this.layers,this.add(Z);let W=new I9(H8,X8,J,$);W.layers=this.layers,this.add(W);let K=new I9(H8,X8,J,$);K.layers=this.layers,this.add(K);let H=new I9(H8,X8,J,$);H.layers=this.layers,this.add(H);let Y=new I9(H8,X8,J,$);Y.layers=this.layers,this.add(Y);let X=new I9(H8,X8,J,$);X.layers=this.layers,this.add(X)}updateCoordinateSystem(){let J=this.coordinateSystem,$=this.children.concat(),[Q,Z,W,K,H,Y]=$;for(let X of $)this.remove(X);if(J===2000)Q.up.set(0,1,0),Q.lookAt(1,0,0),Z.up.set(0,1,0),Z.lookAt(-1,0,0),W.up.set(0,0,-1),W.lookAt(0,1,0),K.up.set(0,0,1),K.lookAt(0,-1,0),H.up.set(0,1,0),H.lookAt(0,0,1),Y.up.set(0,1,0),Y.lookAt(0,0,-1);else if(J===2001)Q.up.set(0,-1,0),Q.lookAt(-1,0,0),Z.up.set(0,-1,0),Z.lookAt(1,0,0),W.up.set(0,0,1),W.lookAt(0,1,0),K.up.set(0,0,-1),K.lookAt(0,-1,0),H.up.set(0,-1,0),H.lookAt(0,0,1),Y.up.set(0,-1,0),Y.lookAt(0,0,-1);else throw Error("THREE.CubeCamera.updateCoordinateSystem(): Invalid coordinate system: "+J);for(let X of $)this.add(X),X.updateMatrixWorld()}update(J,$){if(this.parent===null)this.updateMatrixWorld();let{renderTarget:Q,activeMipmapLevel:Z}=this;if(this.coordinateSystem!==J.coordinateSystem)this.coordinateSystem=J.coordinateSystem,this.updateCoordinateSystem();let[W,K,H,Y,X,N]=this.children,U=J.getRenderTarget(),G=J.getActiveCubeFace(),D=J.getActiveMipmapLevel(),R=J.xr.enabled;J.xr.enabled=!1;let M=Q.texture.generateMipmaps;Q.texture.generateMipmaps=!1,J.setRenderTarget(Q,0,Z),J.render($,W),J.setRenderTarget(Q,1,Z),J.render($,K),J.setRenderTarget(Q,2,Z),J.render($,H),J.setRenderTarget(Q,3,Z),J.render($,Y),J.setRenderTarget(Q,4,Z),J.render($,X),Q.texture.generateMipmaps=M,J.setRenderTarget(Q,5,Z),J.render($,N),J.setRenderTarget(U,G,D),J.xr.enabled=R,Q.texture.needsPMREMUpdate=!0}}class m6 extends P9{constructor(J=[],$=301,Q,Z,W,K,H,Y,X,N){super(J,$,Q,Z,W,K,H,Y,X,N);this.isCubeTexture=!0,this.flipY=!1}get images(){return this.image}set images(J){this.image=J}}class j$ extends FJ{constructor(J=1,$={}){super(J,J,$);this.isWebGLCubeRenderTarget=!0;let Q={width:J,height:J,depth:1},Z=[Q,Q,Q,Q,Q,Q];this.texture=new m6(Z),this._setTextureOptions($),this.texture.isRenderTargetTexture=!0}fromEquirectangularTexture(J,$){this.texture.type=$.type,this.texture.colorSpace=$.colorSpace,this.texture.generateMipmaps=$.generateMipmaps,this.texture.minFilter=$.minFilter,this.texture.magFilter=$.magFilter;let Q={uniforms:{tEquirect:{value:null}},vertexShader:`

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
			`},Z=new z8(5,5,5),W=new KJ({name:"CubemapFromEquirect",uniforms:dJ(Q.uniforms),vertexShader:Q.vertexShader,fragmentShader:Q.fragmentShader,side:1,blending:0});W.uniforms.tEquirect.value=$;let K=new A9(Z,W),H=$.minFilter;if($.minFilter===1008)$.minFilter=1006;return new S$(1,10,this).update(J,K),$.minFilter=H,K.geometry.dispose(),K.material.dispose(),this}clear(J,$=!0,Q=!0,Z=!0){let W=J.getRenderTarget();for(let K=0;K<6;K++)J.setRenderTarget(this,K),J.clear($,Q,Z);J.setRenderTarget(W)}}class qJ extends R9{constructor(){super();this.isGroup=!0,this.type="Group"}}var BK={type:"move"};class s8{constructor(){this._targetRay=null,this._grip=null,this._hand=null}getHandSpace(){if(this._hand===null)this._hand=new qJ,this._hand.matrixAutoUpdate=!1,this._hand.visible=!1,this._hand.joints={},this._hand.inputState={pinching:!1};return this._hand}getTargetRaySpace(){if(this._targetRay===null)this._targetRay=new qJ,this._targetRay.matrixAutoUpdate=!1,this._targetRay.visible=!1,this._targetRay.hasLinearVelocity=!1,this._targetRay.linearVelocity=new v,this._targetRay.hasAngularVelocity=!1,this._targetRay.angularVelocity=new v;return this._targetRay}getGripSpace(){if(this._grip===null)this._grip=new qJ,this._grip.matrixAutoUpdate=!1,this._grip.visible=!1,this._grip.hasLinearVelocity=!1,this._grip.linearVelocity=new v,this._grip.hasAngularVelocity=!1,this._grip.angularVelocity=new v;return this._grip}dispatchEvent(J){if(this._targetRay!==null)this._targetRay.dispatchEvent(J);if(this._grip!==null)this._grip.dispatchEvent(J);if(this._hand!==null)this._hand.dispatchEvent(J);return this}connect(J){if(J&&J.hand){let $=this._hand;if($)for(let Q of J.hand.values())this._getHandJoint($,Q)}return this.dispatchEvent({type:"connected",data:J}),this}disconnect(J){if(this.dispatchEvent({type:"disconnected",data:J}),this._targetRay!==null)this._targetRay.visible=!1;if(this._grip!==null)this._grip.visible=!1;if(this._hand!==null)this._hand.visible=!1;return this}update(J,$,Q){let Z=null,W=null,K=null,H=this._targetRay,Y=this._grip,X=this._hand;if(J&&$.session.visibilityState!=="visible-blurred"){if(X&&J.hand){K=!0;for(let M of J.hand.values()){let L=$.getJointPose(M,Q),E=this._getHandJoint(X,M);if(L!==null)E.matrix.fromArray(L.transform.matrix),E.matrix.decompose(E.position,E.rotation,E.scale),E.matrixWorldNeedsUpdate=!0,E.jointRadius=L.radius;E.visible=L!==null}let N=X.joints["index-finger-tip"],U=X.joints["thumb-tip"],G=N.position.distanceTo(U.position),D=0.02,R=0.005;if(X.inputState.pinching&&G>D+R)X.inputState.pinching=!1,this.dispatchEvent({type:"pinchend",handedness:J.handedness,target:this});else if(!X.inputState.pinching&&G<=D-R)X.inputState.pinching=!0,this.dispatchEvent({type:"pinchstart",handedness:J.handedness,target:this})}else if(Y!==null&&J.gripSpace){if(W=$.getPose(J.gripSpace,Q),W!==null){if(Y.matrix.fromArray(W.transform.matrix),Y.matrix.decompose(Y.position,Y.rotation,Y.scale),Y.matrixWorldNeedsUpdate=!0,W.linearVelocity)Y.hasLinearVelocity=!0,Y.linearVelocity.copy(W.linearVelocity);else Y.hasLinearVelocity=!1;if(W.angularVelocity)Y.hasAngularVelocity=!0,Y.angularVelocity.copy(W.angularVelocity);else Y.hasAngularVelocity=!1}}if(H!==null){if(Z=$.getPose(J.targetRaySpace,Q),Z===null&&W!==null)Z=W;if(Z!==null){if(H.matrix.fromArray(Z.transform.matrix),H.matrix.decompose(H.position,H.rotation,H.scale),H.matrixWorldNeedsUpdate=!0,Z.linearVelocity)H.hasLinearVelocity=!0,H.linearVelocity.copy(Z.linearVelocity);else H.hasLinearVelocity=!1;if(Z.angularVelocity)H.hasAngularVelocity=!0,H.angularVelocity.copy(Z.angularVelocity);else H.hasAngularVelocity=!1;this.dispatchEvent(BK)}}}if(H!==null)H.visible=Z!==null;if(Y!==null)Y.visible=W!==null;if(X!==null)X.visible=K!==null;return this}_getHandJoint(J,$){if(J.joints[$.jointName]===void 0){let Q=new qJ;Q.matrixAutoUpdate=!1,Q.visible=!1,J.joints[$.jointName]=Q,J.add(Q)}return J.joints[$.jointName]}}class u6 extends R9{constructor(){super();if(this.isScene=!0,this.type="Scene",this.background=null,this.environment=null,this.fog=null,this.backgroundBlurriness=0,this.backgroundIntensity=1,this.backgroundRotation=new t9,this.environmentIntensity=1,this.environmentRotation=new t9,this.overrideMaterial=null,typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("observe",{detail:this}))}copy(J,$){if(super.copy(J,$),J.background!==null)this.background=J.background.clone();if(J.environment!==null)this.environment=J.environment.clone();if(J.fog!==null)this.fog=J.fog.clone();if(this.backgroundBlurriness=J.backgroundBlurriness,this.backgroundIntensity=J.backgroundIntensity,this.backgroundRotation.copy(J.backgroundRotation),this.environmentIntensity=J.environmentIntensity,this.environmentRotation.copy(J.environmentRotation),J.overrideMaterial!==null)this.overrideMaterial=J.overrideMaterial.clone();return this.matrixAutoUpdate=J.matrixAutoUpdate,this}toJSON(J){let $=super.toJSON(J);if(this.fog!==null)$.object.fog=this.fog.toJSON();if(this.backgroundBlurriness>0)$.object.backgroundBlurriness=this.backgroundBlurriness;if(this.backgroundIntensity!==1)$.object.backgroundIntensity=this.backgroundIntensity;if($.object.backgroundRotation=this.backgroundRotation.toArray(),this.environmentIntensity!==1)$.object.environmentIntensity=this.environmentIntensity;return $.object.environmentRotation=this.environmentRotation.toArray(),$}}var S7=new v,wK=new v,CK=new p0;class g9{constructor(J=new v(1,0,0),$=0){this.isPlane=!0,this.normal=J,this.constant=$}set(J,$){return this.normal.copy(J),this.constant=$,this}setComponents(J,$,Q,Z){return this.normal.set(J,$,Q),this.constant=Z,this}setFromNormalAndCoplanarPoint(J,$){return this.normal.copy(J),this.constant=-$.dot(this.normal),this}setFromCoplanarPoints(J,$,Q){let Z=S7.subVectors(Q,$).cross(wK.subVectors(J,$)).normalize();return this.setFromNormalAndCoplanarPoint(Z,J),this}copy(J){return this.normal.copy(J.normal),this.constant=J.constant,this}normalize(){let J=1/this.normal.length();return this.normal.multiplyScalar(J),this.constant*=J,this}negate(){return this.constant*=-1,this.normal.negate(),this}distanceToPoint(J){return this.normal.dot(J)+this.constant}distanceToSphere(J){return this.distanceToPoint(J.center)-J.radius}projectPoint(J,$){return $.copy(J).addScaledVector(this.normal,-this.distanceToPoint(J))}intersectLine(J,$){let Q=J.delta(S7),Z=this.normal.dot(Q);if(Z===0){if(this.distanceToPoint(J.start)===0)return $.copy(J.start);return null}let W=-(J.start.dot(this.normal)+this.constant)/Z;if(W<0||W>1)return null;return $.copy(J.start).addScaledVector(Q,W)}intersectsLine(J){let $=this.distanceToPoint(J.start),Q=this.distanceToPoint(J.end);return $<0&&Q>0||Q<0&&$>0}intersectsBox(J){return J.intersectsPlane(this)}intersectsSphere(J){return J.intersectsPlane(this)}coplanarPoint(J){return J.copy(this.normal).multiplyScalar(-this.constant)}applyMatrix4(J,$){let Q=$||CK.getNormalMatrix(J),Z=this.coplanarPoint(S7).applyMatrix4(J),W=this.normal.applyMatrix3(Q).normalize();return this.constant=-Z.dot(W),this}translate(J){return this.constant-=J.dot(this.normal),this}equals(J){return J.normal.equals(this.normal)&&J.constant===this.constant}clone(){return new this.constructor().copy(this)}}var vJ=new V8,_K=new x0(0.5,0.5),F6=new v;class i8{constructor(J=new g9,$=new g9,Q=new g9,Z=new g9,W=new g9,K=new g9){this.planes=[J,$,Q,Z,W,K]}set(J,$,Q,Z,W,K){let H=this.planes;return H[0].copy(J),H[1].copy($),H[2].copy(Q),H[3].copy(Z),H[4].copy(W),H[5].copy(K),this}copy(J){let $=this.planes;for(let Q=0;Q<6;Q++)$[Q].copy(J.planes[Q]);return this}setFromProjectionMatrix(J,$=2000,Q=!1){let Z=this.planes,W=J.elements,K=W[0],H=W[1],Y=W[2],X=W[3],N=W[4],U=W[5],G=W[6],D=W[7],R=W[8],M=W[9],L=W[10],E=W[11],q=W[12],A=W[13],V=W[14],I=W[15];if(Z[0].setComponents(X-K,D-N,E-R,I-q).normalize(),Z[1].setComponents(X+K,D+N,E+R,I+q).normalize(),Z[2].setComponents(X+H,D+U,E+M,I+A).normalize(),Z[3].setComponents(X-H,D-U,E-M,I-A).normalize(),Q)Z[4].setComponents(Y,G,L,V).normalize(),Z[5].setComponents(X-Y,D-G,E-L,I-V).normalize();else if(Z[4].setComponents(X-Y,D-G,E-L,I-V).normalize(),$===2000)Z[5].setComponents(X+Y,D+G,E+L,I+V).normalize();else if($===2001)Z[5].setComponents(Y,G,L,V).normalize();else throw Error("THREE.Frustum.setFromProjectionMatrix(): Invalid coordinate system: "+$);return this}intersectsObject(J){if(J.boundingSphere!==void 0){if(J.boundingSphere===null)J.computeBoundingSphere();vJ.copy(J.boundingSphere).applyMatrix4(J.matrixWorld)}else{let $=J.geometry;if($.boundingSphere===null)$.computeBoundingSphere();vJ.copy($.boundingSphere).applyMatrix4(J.matrixWorld)}return this.intersectsSphere(vJ)}intersectsSprite(J){vJ.center.set(0,0,0);let $=_K.distanceTo(J.center);return vJ.radius=0.7071067811865476+$,vJ.applyMatrix4(J.matrixWorld),this.intersectsSphere(vJ)}intersectsSphere(J){let $=this.planes,Q=J.center,Z=-J.radius;for(let W=0;W<6;W++)if($[W].distanceToPoint(Q)<Z)return!1;return!0}intersectsBox(J){let $=this.planes;for(let Q=0;Q<6;Q++){let Z=$[Q];if(F6.x=Z.normal.x>0?J.max.x:J.min.x,F6.y=Z.normal.y>0?J.max.y:J.min.y,F6.z=Z.normal.z>0?J.max.z:J.min.z,Z.distanceToPoint(F6)<0)return!1}return!0}containsPoint(J){let $=this.planes;for(let Q=0;Q<6;Q++)if($[Q].distanceToPoint(J)<0)return!1;return!0}clone(){return new this.constructor().copy(this)}}class o8 extends TJ{constructor(J){super();this.isLineBasicMaterial=!0,this.type="LineBasicMaterial",this.color=new n0(16777215),this.map=null,this.linewidth=1,this.linecap="round",this.linejoin="round",this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.color.copy(J.color),this.map=J.map,this.linewidth=J.linewidth,this.linecap=J.linecap,this.linejoin=J.linejoin,this.fog=J.fog,this}}var z6=new v,B6=new v,bQ=new X9,y8=new pJ,M6=new V8,j7=new v,fQ=new v;class c6 extends R9{constructor(J=new l9,$=new o8){super();this.isLine=!0,this.type="Line",this.geometry=J,this.material=$,this.morphTargetDictionary=void 0,this.morphTargetInfluences=void 0,this.updateMorphTargets()}copy(J,$){return super.copy(J,$),this.material=Array.isArray(J.material)?J.material.slice():J.material,this.geometry=J.geometry,this}computeLineDistances(){let J=this.geometry;if(J.index===null){let $=J.attributes.position,Q=[0];for(let Z=1,W=$.count;Z<W;Z++)z6.fromBufferAttribute($,Z-1),B6.fromBufferAttribute($,Z),Q[Z]=Q[Z-1],Q[Z]+=z6.distanceTo(B6);J.setAttribute("lineDistance",new B9(Q,1))}else console.warn("THREE.Line.computeLineDistances(): Computation only possible with non-indexed BufferGeometry.");return this}raycast(J,$){let Q=this.geometry,Z=this.matrixWorld,W=J.params.Line.threshold,K=Q.drawRange;if(Q.boundingSphere===null)Q.computeBoundingSphere();if(M6.copy(Q.boundingSphere),M6.applyMatrix4(Z),M6.radius+=W,J.ray.intersectsSphere(M6)===!1)return;bQ.copy(Z).invert(),y8.copy(J.ray).applyMatrix4(bQ);let H=W/((this.scale.x+this.scale.y+this.scale.z)/3),Y=H*H,X=this.isLineSegments?2:1,N=Q.index,G=Q.attributes.position;if(N!==null){let D=Math.max(0,K.start),R=Math.min(N.count,K.start+K.count);for(let M=D,L=R-1;M<L;M+=X){let E=N.getX(M),q=N.getX(M+1),A=L6(this,J,y8,Y,E,q,M);if(A)$.push(A)}if(this.isLineLoop){let M=N.getX(R-1),L=N.getX(D),E=L6(this,J,y8,Y,M,L,R-1);if(E)$.push(E)}}else{let D=Math.max(0,K.start),R=Math.min(G.count,K.start+K.count);for(let M=D,L=R-1;M<L;M+=X){let E=L6(this,J,y8,Y,M,M+1,M);if(E)$.push(E)}if(this.isLineLoop){let M=L6(this,J,y8,Y,R-1,D,R-1);if(M)$.push(M)}}}updateMorphTargets(){let $=this.geometry.morphAttributes,Q=Object.keys($);if(Q.length>0){let Z=$[Q[0]];if(Z!==void 0){this.morphTargetInfluences=[],this.morphTargetDictionary={};for(let W=0,K=Z.length;W<K;W++){let H=Z[W].name||String(W);this.morphTargetInfluences.push(0),this.morphTargetDictionary[H]=W}}}}}function L6(J,$,Q,Z,W,K,H){let Y=J.geometry.attributes.position;if(z6.fromBufferAttribute(Y,W),B6.fromBufferAttribute(Y,K),Q.distanceSqToSegment(z6,B6,j7,fQ)>Z)return;j7.applyMatrix4(J.matrixWorld);let N=$.ray.origin.distanceTo(j7);if(N<$.near||N>$.far)return;return{distance:N,point:fQ.clone().applyMatrix4(J.matrixWorld),index:H,face:null,faceIndex:null,barycoord:null,object:J}}class n6 extends P9{constructor(J,$,Q=1014,Z,W,K,H=1003,Y=1003,X,N=1026,U=1){if(N!==1026&&N!==1027)throw Error("DepthTexture format must be either THREE.DepthFormat or THREE.DepthStencilFormat");let G={width:J,height:$,depth:U};super(G,Z,W,K,H,Y,N,Q,X);this.isDepthTexture=!0,this.flipY=!1,this.generateMipmaps=!1,this.compareFunction=null}copy(J){return super.copy(J),this.source=new c8(Object.assign({},J.image)),this.compareFunction=J.compareFunction,this}toJSON(J){let $=super.toJSON(J);if(this.compareFunction!==null)$.compareFunction=this.compareFunction;return $}}class s6 extends P9{constructor(J=null){super();this.sourceTexture=J,this.isExternalTexture=!0}copy(J){return super.copy(J),this.sourceTexture=J.sourceTexture,this}}class a8 extends l9{constructor(J=1,$=1,Q=1,Z=1){super();this.type="PlaneGeometry",this.parameters={width:J,height:$,widthSegments:Q,heightSegments:Z};let W=J/2,K=$/2,H=Math.floor(Q),Y=Math.floor(Z),X=H+1,N=Y+1,U=J/H,G=$/Y,D=[],R=[],M=[],L=[];for(let E=0;E<N;E++){let q=E*G-K;for(let A=0;A<X;A++){let V=A*U-W;R.push(V,-q,0),M.push(0,0,1),L.push(A/H),L.push(1-E/Y)}}for(let E=0;E<Y;E++)for(let q=0;q<H;q++){let A=q+X*E,V=q+X*(E+1),I=q+1+X*(E+1),f=q+1+X*E;D.push(A,V,f),D.push(V,I,f)}this.setIndex(D),this.setAttribute("position",new B9(R,3)),this.setAttribute("normal",new B9(M,3)),this.setAttribute("uv",new B9(L,2))}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new a8(J.width,J.height,J.widthSegments,J.heightSegments)}}class B8 extends l9{constructor(J=1,$=32,Q=16,Z=0,W=Math.PI*2,K=0,H=Math.PI){super();this.type="SphereGeometry",this.parameters={radius:J,widthSegments:$,heightSegments:Q,phiStart:Z,phiLength:W,thetaStart:K,thetaLength:H},$=Math.max(3,Math.floor($)),Q=Math.max(2,Math.floor(Q));let Y=Math.min(K+H,Math.PI),X=0,N=[],U=new v,G=new v,D=[],R=[],M=[],L=[];for(let E=0;E<=Q;E++){let q=[],A=E/Q,V=0;if(E===0&&K===0)V=0.5/$;else if(E===Q&&Y===Math.PI)V=-0.5/$;for(let I=0;I<=$;I++){let f=I/$;U.x=-J*Math.cos(Z+f*W)*Math.sin(K+A*H),U.y=J*Math.cos(K+A*H),U.z=J*Math.sin(Z+f*W)*Math.sin(K+A*H),R.push(U.x,U.y,U.z),G.copy(U).normalize(),M.push(G.x,G.y,G.z),L.push(f+V,1-A),q.push(X++)}N.push(q)}for(let E=0;E<Q;E++)for(let q=0;q<$;q++){let A=N[E][q+1],V=N[E][q],I=N[E+1][q],f=N[E+1][q+1];if(E!==0||K>0)D.push(A,V,f);if(E!==Q-1||Y<Math.PI)D.push(V,I,f)}this.setIndex(D),this.setAttribute("position",new B9(R,3)),this.setAttribute("normal",new B9(M,3)),this.setAttribute("uv",new B9(L,2))}copy(J){return super.copy(J),this.parameters=Object.assign({},J.parameters),this}static fromJSON(J){return new B8(J.radius,J.widthSegments,J.heightSegments,J.phiStart,J.phiLength,J.thetaStart,J.thetaLength)}}class i6 extends TJ{constructor(J){super();this.isMeshStandardMaterial=!0,this.type="MeshStandardMaterial",this.defines={STANDARD:""},this.color=new n0(16777215),this.roughness=1,this.metalness=0,this.map=null,this.lightMap=null,this.lightMapIntensity=1,this.aoMap=null,this.aoMapIntensity=1,this.emissive=new n0(0),this.emissiveIntensity=1,this.emissiveMap=null,this.bumpMap=null,this.bumpScale=1,this.normalMap=null,this.normalMapType=0,this.normalScale=new x0(1,1),this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.roughnessMap=null,this.metalnessMap=null,this.alphaMap=null,this.envMap=null,this.envMapRotation=new t9,this.envMapIntensity=1,this.wireframe=!1,this.wireframeLinewidth=1,this.wireframeLinecap="round",this.wireframeLinejoin="round",this.flatShading=!1,this.fog=!0,this.setValues(J)}copy(J){return super.copy(J),this.defines={STANDARD:""},this.color.copy(J.color),this.roughness=J.roughness,this.metalness=J.metalness,this.map=J.map,this.lightMap=J.lightMap,this.lightMapIntensity=J.lightMapIntensity,this.aoMap=J.aoMap,this.aoMapIntensity=J.aoMapIntensity,this.emissive.copy(J.emissive),this.emissiveMap=J.emissiveMap,this.emissiveIntensity=J.emissiveIntensity,this.bumpMap=J.bumpMap,this.bumpScale=J.bumpScale,this.normalMap=J.normalMap,this.normalMapType=J.normalMapType,this.normalScale.copy(J.normalScale),this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this.roughnessMap=J.roughnessMap,this.metalnessMap=J.metalnessMap,this.alphaMap=J.alphaMap,this.envMap=J.envMap,this.envMapRotation.copy(J.envMapRotation),this.envMapIntensity=J.envMapIntensity,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this.wireframeLinecap=J.wireframeLinecap,this.wireframeLinejoin=J.wireframeLinejoin,this.flatShading=J.flatShading,this.fog=J.fog,this}}class y$ extends TJ{constructor(J){super();this.isMeshDepthMaterial=!0,this.type="MeshDepthMaterial",this.depthPacking=3200,this.map=null,this.alphaMap=null,this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.wireframe=!1,this.wireframeLinewidth=1,this.setValues(J)}copy(J){return super.copy(J),this.depthPacking=J.depthPacking,this.map=J.map,this.alphaMap=J.alphaMap,this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this.wireframe=J.wireframe,this.wireframeLinewidth=J.wireframeLinewidth,this}}class b$ extends TJ{constructor(J){super();this.isMeshDistanceMaterial=!0,this.type="MeshDistanceMaterial",this.map=null,this.alphaMap=null,this.displacementMap=null,this.displacementScale=1,this.displacementBias=0,this.setValues(J)}copy(J){return super.copy(J),this.map=J.map,this.alphaMap=J.alphaMap,this.displacementMap=J.displacementMap,this.displacementScale=J.displacementScale,this.displacementBias=J.displacementBias,this}}function V6(J,$){if(!J||J.constructor===$)return J;if(typeof $.BYTES_PER_ELEMENT==="number")return new $(J);return Array.prototype.slice.call(J)}function IK(J){return ArrayBuffer.isView(J)&&!(J instanceof DataView)}class w8{constructor(J,$,Q,Z){this.parameterPositions=J,this._cachedIndex=0,this.resultBuffer=Z!==void 0?Z:new $.constructor(Q),this.sampleValues=$,this.valueSize=Q,this.settings=null,this.DefaultSettings_={}}evaluate(J){let $=this.parameterPositions,Q=this._cachedIndex,Z=$[Q],W=$[Q-1];Q:{J:{let K;$:{Z:if(!(J<Z)){for(let H=Q+2;;){if(Z===void 0){if(J<W)break Z;return Q=$.length,this._cachedIndex=Q,this.copySampleValue_(Q-1)}if(Q===H)break;if(W=Z,Z=$[++Q],J<Z)break J}K=$.length;break $}if(!(J>=W)){let H=$[1];if(J<H)Q=2,W=H;for(let Y=Q-2;;){if(W===void 0)return this._cachedIndex=0,this.copySampleValue_(0);if(Q===Y)break;if(Z=W,W=$[--Q-1],J>=W)break J}K=Q,Q=0;break $}break Q}while(Q<K){let H=Q+K>>>1;if(J<$[H])K=H;else Q=H+1}if(Z=$[Q],W=$[Q-1],W===void 0)return this._cachedIndex=0,this.copySampleValue_(0);if(Z===void 0)return Q=$.length,this._cachedIndex=Q,this.copySampleValue_(Q-1)}this._cachedIndex=Q,this.intervalChanged_(Q,W,Z)}return this.interpolate_(Q,W,J,Z)}getSettings_(){return this.settings||this.DefaultSettings_}copySampleValue_(J){let $=this.resultBuffer,Q=this.sampleValues,Z=this.valueSize,W=J*Z;for(let K=0;K!==Z;++K)$[K]=Q[W+K];return $}interpolate_(){throw Error("call to abstract method")}intervalChanged_(){}}class f$ extends w8{constructor(J,$,Q,Z){super(J,$,Q,Z);this._weightPrev=-0,this._offsetPrev=-0,this._weightNext=-0,this._offsetNext=-0,this.DefaultSettings_={endingStart:2400,endingEnd:2400}}intervalChanged_(J,$,Q){let Z=this.parameterPositions,W=J-2,K=J+1,H=Z[W],Y=Z[K];if(H===void 0)switch(this.getSettings_().endingStart){case 2401:W=J,H=2*$-Q;break;case 2402:W=Z.length-2,H=$+Z[W]-Z[W+1];break;default:W=J,H=Q}if(Y===void 0)switch(this.getSettings_().endingEnd){case 2401:K=J,Y=2*Q-$;break;case 2402:K=1,Y=Q+Z[1]-Z[0];break;default:K=J-1,Y=$}let X=(Q-$)*0.5,N=this.valueSize;this._weightPrev=X/($-H),this._weightNext=X/(Y-Q),this._offsetPrev=W*N,this._offsetNext=K*N}interpolate_(J,$,Q,Z){let W=this.resultBuffer,K=this.sampleValues,H=this.valueSize,Y=J*H,X=Y-H,N=this._offsetPrev,U=this._offsetNext,G=this._weightPrev,D=this._weightNext,R=(Q-$)/(Z-$),M=R*R,L=M*R,E=-G*L+2*G*M-G*R,q=(1+G)*L+(-1.5-2*G)*M+(-0.5+G)*R+1,A=(-1-D)*L+(1.5+D)*M+0.5*R,V=D*L-D*M;for(let I=0;I!==H;++I)W[I]=E*K[N+I]+q*K[X+I]+A*K[Y+I]+V*K[U+I];return W}}class v$ extends w8{constructor(J,$,Q,Z){super(J,$,Q,Z)}interpolate_(J,$,Q,Z){let W=this.resultBuffer,K=this.sampleValues,H=this.valueSize,Y=J*H,X=Y-H,N=(Q-$)/(Z-$),U=1-N;for(let G=0;G!==H;++G)W[G]=K[X+G]*U+K[Y+G]*N;return W}}class h$ extends w8{constructor(J,$,Q,Z){super(J,$,Q,Z)}interpolate_(J){return this.copySampleValue_(J-1)}}class i9{constructor(J,$,Q,Z){if(J===void 0)throw Error("THREE.KeyframeTrack: track name is undefined");if($===void 0||$.length===0)throw Error("THREE.KeyframeTrack: no keyframes in track named "+J);this.name=J,this.times=V6($,this.TimeBufferType),this.values=V6(Q,this.ValueBufferType),this.setInterpolation(Z||this.DefaultInterpolation)}static toJSON(J){let $=J.constructor,Q;if($.toJSON!==this.toJSON)Q=$.toJSON(J);else{Q={name:J.name,times:V6(J.times,Array),values:V6(J.values,Array)};let Z=J.getInterpolation();if(Z!==J.DefaultInterpolation)Q.interpolation=Z}return Q.type=J.ValueTypeName,Q}InterpolantFactoryMethodDiscrete(J){return new h$(this.times,this.values,this.getValueSize(),J)}InterpolantFactoryMethodLinear(J){return new v$(this.times,this.values,this.getValueSize(),J)}InterpolantFactoryMethodSmooth(J){return new f$(this.times,this.values,this.getValueSize(),J)}setInterpolation(J){let $;switch(J){case 2300:$=this.InterpolantFactoryMethodDiscrete;break;case 2301:$=this.InterpolantFactoryMethodLinear;break;case 2302:$=this.InterpolantFactoryMethodSmooth;break}if($===void 0){let Q="unsupported interpolation for "+this.ValueTypeName+" keyframe track named "+this.name;if(this.createInterpolant===void 0)if(J!==this.DefaultInterpolation)this.setInterpolation(this.DefaultInterpolation);else throw Error(Q);return console.warn("THREE.KeyframeTrack:",Q),this}return this.createInterpolant=$,this}getInterpolation(){switch(this.createInterpolant){case this.InterpolantFactoryMethodDiscrete:return 2300;case this.InterpolantFactoryMethodLinear:return 2301;case this.InterpolantFactoryMethodSmooth:return 2302}}getValueSize(){return this.values.length/this.times.length}shift(J){if(J!==0){let $=this.times;for(let Q=0,Z=$.length;Q!==Z;++Q)$[Q]+=J}return this}scale(J){if(J!==1){let $=this.times;for(let Q=0,Z=$.length;Q!==Z;++Q)$[Q]*=J}return this}trim(J,$){let Q=this.times,Z=Q.length,W=0,K=Z-1;while(W!==Z&&Q[W]<J)++W;while(K!==-1&&Q[K]>$)--K;if(++K,W!==0||K!==Z){if(W>=K)K=Math.max(K,1),W=K-1;let H=this.getValueSize();this.times=Q.slice(W,K),this.values=this.values.slice(W*H,K*H)}return this}validate(){let J=!0,$=this.getValueSize();if($-Math.floor($)!==0)console.error("THREE.KeyframeTrack: Invalid value size in track.",this),J=!1;let Q=this.times,Z=this.values,W=Q.length;if(W===0)console.error("THREE.KeyframeTrack: Track is empty.",this),J=!1;let K=null;for(let H=0;H!==W;H++){let Y=Q[H];if(typeof Y==="number"&&isNaN(Y)){console.error("THREE.KeyframeTrack: Time is not a valid number.",this,H,Y),J=!1;break}if(K!==null&&K>Y){console.error("THREE.KeyframeTrack: Out of order keys.",this,H,Y,K),J=!1;break}K=Y}if(Z!==void 0){if(IK(Z))for(let H=0,Y=Z.length;H!==Y;++H){let X=Z[H];if(isNaN(X)){console.error("THREE.KeyframeTrack: Value is not a valid number.",this,H,X),J=!1;break}}}return J}optimize(){let J=this.times.slice(),$=this.values.slice(),Q=this.getValueSize(),Z=this.getInterpolation()===2302,W=J.length-1,K=1;for(let H=1;H<W;++H){let Y=!1,X=J[H],N=J[H+1];if(X!==N&&(H!==1||X!==J[0]))if(!Z){let U=H*Q,G=U-Q,D=U+Q;for(let R=0;R!==Q;++R){let M=$[U+R];if(M!==$[G+R]||M!==$[D+R]){Y=!0;break}}}else Y=!0;if(Y){if(H!==K){J[K]=J[H];let U=H*Q,G=K*Q;for(let D=0;D!==Q;++D)$[G+D]=$[U+D]}++K}}if(W>0){J[K]=J[W];for(let H=W*Q,Y=K*Q,X=0;X!==Q;++X)$[Y+X]=$[H+X];++K}if(K!==J.length)this.times=J.slice(0,K),this.values=$.slice(0,K*Q);else this.times=J,this.values=$;return this}clone(){let J=this.times.slice(),$=this.values.slice(),Z=new this.constructor(this.name,J,$);return Z.createInterpolant=this.createInterpolant,Z}}i9.prototype.ValueTypeName="";i9.prototype.TimeBufferType=Float32Array;i9.prototype.ValueBufferType=Float32Array;i9.prototype.DefaultInterpolation=2301;class mJ extends i9{constructor(J,$,Q){super(J,$,Q)}}mJ.prototype.ValueTypeName="bool";mJ.prototype.ValueBufferType=Array;mJ.prototype.DefaultInterpolation=2300;mJ.prototype.InterpolantFactoryMethodLinear=void 0;mJ.prototype.InterpolantFactoryMethodSmooth=void 0;class x$ extends i9{constructor(J,$,Q,Z){super(J,$,Q,Z)}}x$.prototype.ValueTypeName="color";class g$ extends i9{constructor(J,$,Q,Z){super(J,$,Q,Z)}}g$.prototype.ValueTypeName="number";class p$ extends w8{constructor(J,$,Q,Z){super(J,$,Q,Z)}interpolate_(J,$,Q,Z){let W=this.resultBuffer,K=this.sampleValues,H=this.valueSize,Y=(Q-$)/(Z-$),X=J*H;for(let N=X+H;X!==N;X+=4)e9.slerpFlat(W,0,K,X-H,K,X,Y);return W}}class o6 extends i9{constructor(J,$,Q,Z){super(J,$,Q,Z)}InterpolantFactoryMethodLinear(J){return new p$(this.times,this.values,this.getValueSize(),J)}}o6.prototype.ValueTypeName="quaternion";o6.prototype.InterpolantFactoryMethodSmooth=void 0;class uJ extends i9{constructor(J,$,Q){super(J,$,Q)}}uJ.prototype.ValueTypeName="string";uJ.prototype.ValueBufferType=Array;uJ.prototype.DefaultInterpolation=2300;uJ.prototype.InterpolantFactoryMethodLinear=void 0;uJ.prototype.InterpolantFactoryMethodSmooth=void 0;class l$ extends i9{constructor(J,$,Q,Z){super(J,$,Q,Z)}}l$.prototype.ValueTypeName="vector";class d${constructor(J,$,Q){let Z=this,W=!1,K=0,H=0,Y=void 0,X=[];this.onStart=void 0,this.onLoad=J,this.onProgress=$,this.onError=Q,this.abortController=new AbortController,this.itemStart=function(N){if(H++,W===!1){if(Z.onStart!==void 0)Z.onStart(N,K,H)}W=!0},this.itemEnd=function(N){if(K++,Z.onProgress!==void 0)Z.onProgress(N,K,H);if(K===H){if(W=!1,Z.onLoad!==void 0)Z.onLoad()}},this.itemError=function(N){if(Z.onError!==void 0)Z.onError(N)},this.resolveURL=function(N){if(Y)return Y(N);return N},this.setURLModifier=function(N){return Y=N,this},this.addHandler=function(N,U){return X.push(N,U),this},this.removeHandler=function(N){let U=X.indexOf(N);if(U!==-1)X.splice(U,2);return this},this.getHandler=function(N){for(let U=0,G=X.length;U<G;U+=2){let D=X[U],R=X[U+1];if(D.global)D.lastIndex=0;if(D.test(N))return R}return null},this.abort=function(){return this.abortController.abort(),this.abortController=new AbortController,this}}}var sZ=new d$;class m${constructor(J){this.manager=J!==void 0?J:sZ,this.crossOrigin="anonymous",this.withCredentials=!1,this.path="",this.resourcePath="",this.requestHeader={}}load(){}loadAsync(J,$){let Q=this;return new Promise(function(Z,W){Q.load(J,Z,$,W)})}parse(){}setCrossOrigin(J){return this.crossOrigin=J,this}setWithCredentials(J){return this.withCredentials=J,this}setPath(J){return this.path=J,this}setResourcePath(J){return this.resourcePath=J,this}setRequestHeader(J){return this.requestHeader=J,this}abort(){return this}}m$.DEFAULT_MATERIAL_NAME="__DEFAULT";class a6 extends R9{constructor(J,$=1){super();this.isLight=!0,this.type="Light",this.color=new n0(J),this.intensity=$}dispose(){}copy(J,$){return super.copy(J,$),this.color.copy(J.color),this.intensity=J.intensity,this}toJSON(J){let $=super.toJSON(J);if($.object.color=this.color.getHex(),$.object.intensity=this.intensity,this.groundColor!==void 0)$.object.groundColor=this.groundColor.getHex();if(this.distance!==void 0)$.object.distance=this.distance;if(this.angle!==void 0)$.object.angle=this.angle;if(this.decay!==void 0)$.object.decay=this.decay;if(this.penumbra!==void 0)$.object.penumbra=this.penumbra;if(this.shadow!==void 0)$.object.shadow=this.shadow.toJSON();if(this.target!==void 0)$.object.target=this.target.uuid;return $}}class r6 extends a6{constructor(J,$,Q){super(J,Q);this.isHemisphereLight=!0,this.type="HemisphereLight",this.position.copy(R9.DEFAULT_UP),this.updateMatrix(),this.groundColor=new n0($)}copy(J,$){return super.copy(J,$),this.groundColor.copy(J.groundColor),this}}var y7=new X9,vQ=new v,hQ=new v;class iZ{constructor(J){this.camera=J,this.intensity=1,this.bias=0,this.normalBias=0,this.radius=1,this.blurSamples=8,this.mapSize=new x0(512,512),this.mapType=1009,this.map=null,this.mapPass=null,this.matrix=new X9,this.autoUpdate=!0,this.needsUpdate=!1,this._frustum=new i8,this._frameExtents=new x0(1,1),this._viewportCount=1,this._viewports=[new G9(0,0,1,1)]}getViewportCount(){return this._viewportCount}getFrustum(){return this._frustum}updateMatrices(J){let $=this.camera,Q=this.matrix;if(vQ.setFromMatrixPosition(J.matrixWorld),$.position.copy(vQ),hQ.setFromMatrixPosition(J.target.matrixWorld),$.lookAt(hQ),$.updateMatrixWorld(),y7.multiplyMatrices($.projectionMatrix,$.matrixWorldInverse),this._frustum.setFromProjectionMatrix(y7,$.coordinateSystem,$.reversedDepth),$.reversedDepth)Q.set(0.5,0,0,0.5,0,0.5,0,0.5,0,0,1,0,0,0,0,1);else Q.set(0.5,0,0,0.5,0,0.5,0,0.5,0,0,0.5,0.5,0,0,0,1);Q.multiply(y7)}getViewport(J){return this._viewports[J]}getFrameExtents(){return this._frameExtents}dispose(){if(this.map)this.map.dispose();if(this.mapPass)this.mapPass.dispose()}copy(J){return this.camera=J.camera.clone(),this.intensity=J.intensity,this.bias=J.bias,this.radius=J.radius,this.autoUpdate=J.autoUpdate,this.needsUpdate=J.needsUpdate,this.normalBias=J.normalBias,this.blurSamples=J.blurSamples,this.mapSize.copy(J.mapSize),this}clone(){return new this.constructor().copy(this)}toJSON(){let J={};if(this.intensity!==1)J.intensity=this.intensity;if(this.bias!==0)J.bias=this.bias;if(this.normalBias!==0)J.normalBias=this.normalBias;if(this.radius!==1)J.radius=this.radius;if(this.mapSize.x!==512||this.mapSize.y!==512)J.mapSize=this.mapSize.toArray();return J.camera=this.camera.toJSON(!1).object,delete J.camera.matrix,J}}class t6 extends d6{constructor(J=-1,$=1,Q=1,Z=-1,W=0.1,K=2000){super();this.isOrthographicCamera=!0,this.type="OrthographicCamera",this.zoom=1,this.view=null,this.left=J,this.right=$,this.top=Q,this.bottom=Z,this.near=W,this.far=K,this.updateProjectionMatrix()}copy(J,$){return super.copy(J,$),this.left=J.left,this.right=J.right,this.top=J.top,this.bottom=J.bottom,this.near=J.near,this.far=J.far,this.zoom=J.zoom,this.view=J.view===null?null:Object.assign({},J.view),this}setViewOffset(J,$,Q,Z,W,K){if(this.view===null)this.view={enabled:!0,fullWidth:1,fullHeight:1,offsetX:0,offsetY:0,width:1,height:1};this.view.enabled=!0,this.view.fullWidth=J,this.view.fullHeight=$,this.view.offsetX=Q,this.view.offsetY=Z,this.view.width=W,this.view.height=K,this.updateProjectionMatrix()}clearViewOffset(){if(this.view!==null)this.view.enabled=!1;this.updateProjectionMatrix()}updateProjectionMatrix(){let J=(this.right-this.left)/(2*this.zoom),$=(this.top-this.bottom)/(2*this.zoom),Q=(this.right+this.left)/2,Z=(this.top+this.bottom)/2,W=Q-J,K=Q+J,H=Z+$,Y=Z-$;if(this.view!==null&&this.view.enabled){let X=(this.right-this.left)/this.view.fullWidth/this.zoom,N=(this.top-this.bottom)/this.view.fullHeight/this.zoom;W+=X*this.view.offsetX,K=W+X*this.view.width,H-=N*this.view.offsetY,Y=H-N*this.view.height}this.projectionMatrix.makeOrthographic(W,K,H,Y,this.near,this.far,this.coordinateSystem,this.reversedDepth),this.projectionMatrixInverse.copy(this.projectionMatrix).invert()}toJSON(J){let $=super.toJSON(J);if($.object.zoom=this.zoom,$.object.left=this.left,$.object.right=this.right,$.object.top=this.top,$.object.bottom=this.bottom,$.object.near=this.near,$.object.far=this.far,this.view!==null)$.object.view=Object.assign({},this.view);return $}}class oZ extends iZ{constructor(){super(new t6(-5,5,5,-5,0.5,500));this.isDirectionalLightShadow=!0}}class e6 extends a6{constructor(J,$){super(J,$);this.isDirectionalLight=!0,this.type="DirectionalLight",this.position.copy(R9.DEFAULT_UP),this.updateMatrix(),this.target=new R9,this.shadow=new oZ}dispose(){this.shadow.dispose()}copy(J){return super.copy(J),this.target=J.target.clone(),this.shadow=J.shadow.clone(),this}}class u$ extends I9{constructor(J=[]){super();this.isArrayCamera=!0,this.isMultiViewCamera=!1,this.cameras=J}}var c$="\\[\\]\\.:\\/",PK=new RegExp("["+c$+"]","g"),n$="[^"+c$+"]",AK="[^"+c$.replace("\\.","")+"]",TK=/((?:WC+[\/:])*)/.source.replace("WC",n$),SK=/(WCOD+)?/.source.replace("WCOD",AK),jK=/(?:\.(WC+)(?:\[(.+)\])?)?/.source.replace("WC",n$),yK=/\.(WC+)(?:\[(.+)\])?/.source.replace("WC",n$),bK=new RegExp("^"+TK+SK+jK+yK+"$"),fK=["material","materials","bones","map"];class aZ{constructor(J,$,Q){let Z=Q||t0.parseTrackName($);this._targetGroup=J,this._bindings=J.subscribe_($,Z)}getValue(J,$){this.bind();let Q=this._targetGroup.nCachedObjects_,Z=this._bindings[Q];if(Z!==void 0)Z.getValue(J,$)}setValue(J,$){let Q=this._bindings;for(let Z=this._targetGroup.nCachedObjects_,W=Q.length;Z!==W;++Z)Q[Z].setValue(J,$)}bind(){let J=this._bindings;for(let $=this._targetGroup.nCachedObjects_,Q=J.length;$!==Q;++$)J[$].bind()}unbind(){let J=this._bindings;for(let $=this._targetGroup.nCachedObjects_,Q=J.length;$!==Q;++$)J[$].unbind()}}class t0{constructor(J,$,Q){this.path=$,this.parsedPath=Q||t0.parseTrackName($),this.node=t0.findNode(J,this.parsedPath.nodeName),this.rootNode=J,this.getValue=this._getValue_unbound,this.setValue=this._setValue_unbound}static create(J,$,Q){if(!(J&&J.isAnimationObjectGroup))return new t0(J,$,Q);else return new t0.Composite(J,$,Q)}static sanitizeNodeName(J){return J.replace(/\s/g,"_").replace(PK,"")}static parseTrackName(J){let $=bK.exec(J);if($===null)throw Error("PropertyBinding: Cannot parse trackName: "+J);let Q={nodeName:$[2],objectName:$[3],objectIndex:$[4],propertyName:$[5],propertyIndex:$[6]},Z=Q.nodeName&&Q.nodeName.lastIndexOf(".");if(Z!==void 0&&Z!==-1){let W=Q.nodeName.substring(Z+1);if(fK.indexOf(W)!==-1)Q.nodeName=Q.nodeName.substring(0,Z),Q.objectName=W}if(Q.propertyName===null||Q.propertyName.length===0)throw Error("PropertyBinding: can not parse propertyName from trackName: "+J);return Q}static findNode(J,$){if($===void 0||$===""||$==="."||$===-1||$===J.name||$===J.uuid)return J;if(J.skeleton){let Q=J.skeleton.getBoneByName($);if(Q!==void 0)return Q}if(J.children){let Q=function(W){for(let K=0;K<W.length;K++){let H=W[K];if(H.name===$||H.uuid===$)return H;let Y=Q(H.children);if(Y)return Y}return null},Z=Q(J.children);if(Z)return Z}return null}_getValue_unavailable(){}_setValue_unavailable(){}_getValue_direct(J,$){J[$]=this.targetObject[this.propertyName]}_getValue_array(J,$){let Q=this.resolvedProperty;for(let Z=0,W=Q.length;Z!==W;++Z)J[$++]=Q[Z]}_getValue_arrayElement(J,$){J[$]=this.resolvedProperty[this.propertyIndex]}_getValue_toArray(J,$){this.resolvedProperty.toArray(J,$)}_setValue_direct(J,$){this.targetObject[this.propertyName]=J[$]}_setValue_direct_setNeedsUpdate(J,$){this.targetObject[this.propertyName]=J[$],this.targetObject.needsUpdate=!0}_setValue_direct_setMatrixWorldNeedsUpdate(J,$){this.targetObject[this.propertyName]=J[$],this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_array(J,$){let Q=this.resolvedProperty;for(let Z=0,W=Q.length;Z!==W;++Z)Q[Z]=J[$++]}_setValue_array_setNeedsUpdate(J,$){let Q=this.resolvedProperty;for(let Z=0,W=Q.length;Z!==W;++Z)Q[Z]=J[$++];this.targetObject.needsUpdate=!0}_setValue_array_setMatrixWorldNeedsUpdate(J,$){let Q=this.resolvedProperty;for(let Z=0,W=Q.length;Z!==W;++Z)Q[Z]=J[$++];this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_arrayElement(J,$){this.resolvedProperty[this.propertyIndex]=J[$]}_setValue_arrayElement_setNeedsUpdate(J,$){this.resolvedProperty[this.propertyIndex]=J[$],this.targetObject.needsUpdate=!0}_setValue_arrayElement_setMatrixWorldNeedsUpdate(J,$){this.resolvedProperty[this.propertyIndex]=J[$],this.targetObject.matrixWorldNeedsUpdate=!0}_setValue_fromArray(J,$){this.resolvedProperty.fromArray(J,$)}_setValue_fromArray_setNeedsUpdate(J,$){this.resolvedProperty.fromArray(J,$),this.targetObject.needsUpdate=!0}_setValue_fromArray_setMatrixWorldNeedsUpdate(J,$){this.resolvedProperty.fromArray(J,$),this.targetObject.matrixWorldNeedsUpdate=!0}_getValue_unbound(J,$){this.bind(),this.getValue(J,$)}_setValue_unbound(J,$){this.bind(),this.setValue(J,$)}bind(){let J=this.node,$=this.parsedPath,Q=$.objectName,Z=$.propertyName,W=$.propertyIndex;if(!J)J=t0.findNode(this.rootNode,$.nodeName),this.node=J;if(this.getValue=this._getValue_unavailable,this.setValue=this._setValue_unavailable,!J){console.warn("THREE.PropertyBinding: No target node found for track: "+this.path+".");return}if(Q){let X=$.objectIndex;switch(Q){case"materials":if(!J.material){console.error("THREE.PropertyBinding: Can not bind to material as node does not have a material.",this);return}if(!J.material.materials){console.error("THREE.PropertyBinding: Can not bind to material.materials as node.material does not have a materials array.",this);return}J=J.material.materials;break;case"bones":if(!J.skeleton){console.error("THREE.PropertyBinding: Can not bind to bones as node does not have a skeleton.",this);return}J=J.skeleton.bones;for(let N=0;N<J.length;N++)if(J[N].name===X){X=N;break}break;case"map":if("map"in J){J=J.map;break}if(!J.material){console.error("THREE.PropertyBinding: Can not bind to material as node does not have a material.",this);return}if(!J.material.map){console.error("THREE.PropertyBinding: Can not bind to material.map as node.material does not have a map.",this);return}J=J.material.map;break;default:if(J[Q]===void 0){console.error("THREE.PropertyBinding: Can not bind to objectName of node undefined.",this);return}J=J[Q]}if(X!==void 0){if(J[X]===void 0){console.error("THREE.PropertyBinding: Trying to bind to objectIndex of objectName, but is undefined.",this,J);return}J=J[X]}}let K=J[Z];if(K===void 0){let X=$.nodeName;console.error("THREE.PropertyBinding: Trying to update property for track: "+X+"."+Z+" but it wasn't found.",J);return}let H=this.Versioning.None;if(this.targetObject=J,J.isMaterial===!0)H=this.Versioning.NeedsUpdate;else if(J.isObject3D===!0)H=this.Versioning.MatrixWorldNeedsUpdate;let Y=this.BindingType.Direct;if(W!==void 0){if(Z==="morphTargetInfluences"){if(!J.geometry){console.error("THREE.PropertyBinding: Can not bind to morphTargetInfluences because node does not have a geometry.",this);return}if(!J.geometry.morphAttributes){console.error("THREE.PropertyBinding: Can not bind to morphTargetInfluences because node does not have a geometry.morphAttributes.",this);return}if(J.morphTargetDictionary[W]!==void 0)W=J.morphTargetDictionary[W]}Y=this.BindingType.ArrayElement,this.resolvedProperty=K,this.propertyIndex=W}else if(K.fromArray!==void 0&&K.toArray!==void 0)Y=this.BindingType.HasFromToArray,this.resolvedProperty=K;else if(Array.isArray(K))Y=this.BindingType.EntireArray,this.resolvedProperty=K;else this.propertyName=Z;this.getValue=this.GetterByBindingType[Y],this.setValue=this.SetterByBindingTypeAndVersioning[Y][H]}unbind(){this.node=null,this.getValue=this._getValue_unbound,this.setValue=this._setValue_unbound}}t0.Composite=aZ;t0.prototype.BindingType={Direct:0,EntireArray:1,ArrayElement:2,HasFromToArray:3};t0.prototype.Versioning={None:0,NeedsUpdate:1,MatrixWorldNeedsUpdate:2};t0.prototype.GetterByBindingType=[t0.prototype._getValue_direct,t0.prototype._getValue_array,t0.prototype._getValue_arrayElement,t0.prototype._getValue_toArray];t0.prototype.SetterByBindingTypeAndVersioning=[[t0.prototype._setValue_direct,t0.prototype._setValue_direct_setNeedsUpdate,t0.prototype._setValue_direct_setMatrixWorldNeedsUpdate],[t0.prototype._setValue_array,t0.prototype._setValue_array_setNeedsUpdate,t0.prototype._setValue_array_setMatrixWorldNeedsUpdate],[t0.prototype._setValue_arrayElement,t0.prototype._setValue_arrayElement_setNeedsUpdate,t0.prototype._setValue_arrayElement_setMatrixWorldNeedsUpdate],[t0.prototype._setValue_fromArray,t0.prototype._setValue_fromArray_setNeedsUpdate,t0.prototype._setValue_fromArray_setMatrixWorldNeedsUpdate]];var T1=new Float32Array(1);var xQ=new X9;class J7{constructor(J,$,Q=0,Z=1/0){this.ray=new pJ(J,$),this.near=Q,this.far=Z,this.camera=null,this.layers=new n8,this.params={Mesh:{},Line:{threshold:1},LOD:{},Points:{threshold:1},Sprite:{}}}set(J,$){this.ray.set(J,$)}setFromCamera(J,$){if($.isPerspectiveCamera)this.ray.origin.setFromMatrixPosition($.matrixWorld),this.ray.direction.set(J.x,J.y,0.5).unproject($).sub(this.ray.origin).normalize(),this.camera=$;else if($.isOrthographicCamera)this.ray.origin.set(J.x,J.y,($.near+$.far)/($.near-$.far)).unproject($),this.ray.direction.set(0,0,-1).transformDirection($.matrixWorld),this.camera=$;else console.error("THREE.Raycaster: Unsupported camera type: "+$.type)}setFromXRController(J){return xQ.identity().extractRotation(J.matrixWorld),this.ray.origin.setFromMatrixPosition(J.matrixWorld),this.ray.direction.set(0,0,-1).applyMatrix4(xQ),this}intersectObject(J,$=!0,Q=[]){return b7(J,this,Q,$),Q.sort(gQ),Q}intersectObjects(J,$=!0,Q=[]){for(let Z=0,W=J.length;Z<W;Z++)b7(J[Z],this,Q,$);return Q.sort(gQ),Q}}function gQ(J,$){return J.distance-$.distance}function b7(J,$,Q,Z){let W=!0;if(J.layers.test($.layers)){if(J.raycast($,Q)===!1)W=!1}if(W===!0&&Z===!0){let K=J.children;for(let H=0,Y=K.length;H<Y;H++)b7(K[H],$,Q,!0)}}class r8{constructor(J=1,$=0,Q=0){this.radius=J,this.phi=$,this.theta=Q}set(J,$,Q){return this.radius=J,this.phi=$,this.theta=Q,this}copy(J){return this.radius=J.radius,this.phi=J.phi,this.theta=J.theta,this}makeSafe(){return this.phi=u0(this.phi,0.000001,Math.PI-0.000001),this}setFromVector3(J){return this.setFromCartesianCoords(J.x,J.y,J.z)}setFromCartesianCoords(J,$,Q){if(this.radius=Math.sqrt(J*J+$*$+Q*Q),this.radius===0)this.theta=0,this.phi=0;else this.theta=Math.atan2(J,Q),this.phi=Math.acos(u0($/this.radius,-1,1));return this}clone(){return new this.constructor().copy(this)}}class $7 extends kJ{constructor(J,$=null){super();this.object=J,this.domElement=$,this.enabled=!0,this.state=-1,this.keys={},this.mouseButtons={LEFT:null,MIDDLE:null,RIGHT:null},this.touches={ONE:null,TWO:null}}connect(J){if(J===void 0){console.warn("THREE.Controls: connect() now requires an element.");return}if(this.domElement!==null)this.disconnect();this.domElement=J}disconnect(){}dispose(){}update(){}}function s$(J,$,Q,Z){let W=vK(Z);switch(Q){case 1021:return J*$;case 1028:return J*$/W.components*W.byteLength;case 1029:return J*$/W.components*W.byteLength;case 1030:return J*$*2/W.components*W.byteLength;case 1031:return J*$*2/W.components*W.byteLength;case 1022:return J*$*3/W.components*W.byteLength;case 1023:return J*$*4/W.components*W.byteLength;case 1033:return J*$*4/W.components*W.byteLength;case 33776:case 33777:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*8;case 33778:case 33779:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 35841:case 35843:return Math.max(J,16)*Math.max($,8)/4;case 35840:case 35842:return Math.max(J,8)*Math.max($,8)/2;case 36196:case 37492:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*8;case 37496:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 37808:return Math.floor((J+3)/4)*Math.floor(($+3)/4)*16;case 37809:return Math.floor((J+4)/5)*Math.floor(($+3)/4)*16;case 37810:return Math.floor((J+4)/5)*Math.floor(($+4)/5)*16;case 37811:return Math.floor((J+5)/6)*Math.floor(($+4)/5)*16;case 37812:return Math.floor((J+5)/6)*Math.floor(($+5)/6)*16;case 37813:return Math.floor((J+7)/8)*Math.floor(($+4)/5)*16;case 37814:return Math.floor((J+7)/8)*Math.floor(($+5)/6)*16;case 37815:return Math.floor((J+7)/8)*Math.floor(($+7)/8)*16;case 37816:return Math.floor((J+9)/10)*Math.floor(($+4)/5)*16;case 37817:return Math.floor((J+9)/10)*Math.floor(($+5)/6)*16;case 37818:return Math.floor((J+9)/10)*Math.floor(($+7)/8)*16;case 37819:return Math.floor((J+9)/10)*Math.floor(($+9)/10)*16;case 37820:return Math.floor((J+11)/12)*Math.floor(($+9)/10)*16;case 37821:return Math.floor((J+11)/12)*Math.floor(($+11)/12)*16;case 36492:case 36494:case 36495:return Math.ceil(J/4)*Math.ceil($/4)*16;case 36283:case 36284:return Math.ceil(J/4)*Math.ceil($/4)*8;case 36285:case 36286:return Math.ceil(J/4)*Math.ceil($/4)*16}throw Error(`Unable to determine texture byte length for ${Q} format.`)}function vK(J){switch(J){case 1009:case 1010:return{byteLength:1,components:1};case 1012:case 1011:case 1016:return{byteLength:2,components:1};case 1017:case 1018:return{byteLength:2,components:4};case 1014:case 1013:case 1015:return{byteLength:4,components:1};case 35902:case 35899:return{byteLength:4,components:3}}throw Error(`Unknown texture type ${J}.`)}if(typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("register",{detail:{revision:"180"}}));if(typeof window<"u")if(window.__THREE__)console.warn("WARNING: Multiple instances of Three.js being imported.");else window.__THREE__="180";function LW(){let J=null,$=!1,Q=null,Z=null;function W(K,H){Q(K,H),Z=J.requestAnimationFrame(W)}return{start:function(){if($===!0)return;if(Q===null)return;Z=J.requestAnimationFrame(W),$=!0},stop:function(){J.cancelAnimationFrame(Z),$=!1},setAnimationLoop:function(K){Q=K},setContext:function(K){J=K}}}function hK(J){let $=new WeakMap;function Q(Y,X){let{array:N,usage:U}=Y,G=N.byteLength,D=J.createBuffer();J.bindBuffer(X,D),J.bufferData(X,N,U),Y.onUploadCallback();let R;if(N instanceof Float32Array)R=J.FLOAT;else if(typeof Float16Array<"u"&&N instanceof Float16Array)R=J.HALF_FLOAT;else if(N instanceof Uint16Array)if(Y.isFloat16BufferAttribute)R=J.HALF_FLOAT;else R=J.UNSIGNED_SHORT;else if(N instanceof Int16Array)R=J.SHORT;else if(N instanceof Uint32Array)R=J.UNSIGNED_INT;else if(N instanceof Int32Array)R=J.INT;else if(N instanceof Int8Array)R=J.BYTE;else if(N instanceof Uint8Array)R=J.UNSIGNED_BYTE;else if(N instanceof Uint8ClampedArray)R=J.UNSIGNED_BYTE;else throw Error("THREE.WebGLAttributes: Unsupported buffer data format: "+N);return{buffer:D,type:R,bytesPerElement:N.BYTES_PER_ELEMENT,version:Y.version,size:G}}function Z(Y,X,N){let{array:U,updateRanges:G}=X;if(J.bindBuffer(N,Y),G.length===0)J.bufferSubData(N,0,U);else{G.sort((R,M)=>R.start-M.start);let D=0;for(let R=1;R<G.length;R++){let M=G[D],L=G[R];if(L.start<=M.start+M.count+1)M.count=Math.max(M.count,L.start+L.count-M.start);else++D,G[D]=L}G.length=D+1;for(let R=0,M=G.length;R<M;R++){let L=G[R];J.bufferSubData(N,L.start*U.BYTES_PER_ELEMENT,U,L.start,L.count)}X.clearUpdateRanges()}X.onUploadCallback()}function W(Y){if(Y.isInterleavedBufferAttribute)Y=Y.data;return $.get(Y)}function K(Y){if(Y.isInterleavedBufferAttribute)Y=Y.data;let X=$.get(Y);if(X)J.deleteBuffer(X.buffer),$.delete(Y)}function H(Y,X){if(Y.isInterleavedBufferAttribute)Y=Y.data;if(Y.isGLBufferAttribute){let U=$.get(Y);if(!U||U.version<Y.version)$.set(Y,{buffer:Y.buffer,type:Y.type,bytesPerElement:Y.elementSize,version:Y.version});return}let N=$.get(Y);if(N===void 0)$.set(Y,Q(Y,X));else if(N.version<Y.version){if(N.size!==Y.array.byteLength)throw Error("THREE.WebGLAttributes: The size of the buffer attribute's array buffer does not match the original size. Resizing buffer attributes is not supported.");Z(N.buffer,Y,X),N.version=Y.version}}return{get:W,remove:K,update:H}}var xK=`#ifdef USE_ALPHAHASH
	if ( diffuseColor.a < getAlphaHashThreshold( vPosition ) ) discard;
#endif`,gK=`#ifdef USE_ALPHAHASH
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
#endif`,pK=`#ifdef USE_ALPHAMAP
	diffuseColor.a *= texture2D( alphaMap, vAlphaMapUv ).g;
#endif`,lK=`#ifdef USE_ALPHAMAP
	uniform sampler2D alphaMap;
#endif`,dK=`#ifdef USE_ALPHATEST
	#ifdef ALPHA_TO_COVERAGE
	diffuseColor.a = smoothstep( alphaTest, alphaTest + fwidth( diffuseColor.a ), diffuseColor.a );
	if ( diffuseColor.a == 0.0 ) discard;
	#else
	if ( diffuseColor.a < alphaTest ) discard;
	#endif
#endif`,mK=`#ifdef USE_ALPHATEST
	uniform float alphaTest;
#endif`,uK=`#ifdef USE_AOMAP
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
#endif`,cK=`#ifdef USE_AOMAP
	uniform sampler2D aoMap;
	uniform float aoMapIntensity;
#endif`,nK=`#ifdef USE_BATCHING
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
#endif`,sK=`#ifdef USE_BATCHING
	mat4 batchingMatrix = getBatchingMatrix( getIndirectIndex( gl_DrawID ) );
#endif`,iK=`vec3 transformed = vec3( position );
#ifdef USE_ALPHAHASH
	vPosition = vec3( position );
#endif`,oK=`vec3 objectNormal = vec3( normal );
#ifdef USE_TANGENT
	vec3 objectTangent = vec3( tangent.xyz );
#endif`,aK=`float G_BlinnPhong_Implicit( ) {
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
} // validated`,rK=`#ifdef USE_IRIDESCENCE
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
#endif`,tK=`#ifdef USE_BUMPMAP
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
#endif`,eK=`#if NUM_CLIPPING_PLANES > 0
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
#endif`,JY=`#if NUM_CLIPPING_PLANES > 0
	varying vec3 vClipPosition;
	uniform vec4 clippingPlanes[ NUM_CLIPPING_PLANES ];
#endif`,$Y=`#if NUM_CLIPPING_PLANES > 0
	varying vec3 vClipPosition;
#endif`,QY=`#if NUM_CLIPPING_PLANES > 0
	vClipPosition = - mvPosition.xyz;
#endif`,ZY=`#if defined( USE_COLOR_ALPHA )
	diffuseColor *= vColor;
#elif defined( USE_COLOR )
	diffuseColor.rgb *= vColor;
#endif`,WY=`#if defined( USE_COLOR_ALPHA )
	varying vec4 vColor;
#elif defined( USE_COLOR )
	varying vec3 vColor;
#endif`,KY=`#if defined( USE_COLOR_ALPHA )
	varying vec4 vColor;
#elif defined( USE_COLOR ) || defined( USE_INSTANCING_COLOR ) || defined( USE_BATCHING_COLOR )
	varying vec3 vColor;
#endif`,YY=`#if defined( USE_COLOR_ALPHA )
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
#endif`,HY=`#define PI 3.141592653589793
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
} // validated`,XY=`#ifdef ENVMAP_TYPE_CUBE_UV
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
#endif`,NY=`vec3 transformedNormal = objectNormal;
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
#endif`,UY=`#ifdef USE_DISPLACEMENTMAP
	uniform sampler2D displacementMap;
	uniform float displacementScale;
	uniform float displacementBias;
#endif`,GY=`#ifdef USE_DISPLACEMENTMAP
	transformed += normalize( objectNormal ) * ( texture2D( displacementMap, vDisplacementMapUv ).x * displacementScale + displacementBias );
#endif`,EY=`#ifdef USE_EMISSIVEMAP
	vec4 emissiveColor = texture2D( emissiveMap, vEmissiveMapUv );
	#ifdef DECODE_VIDEO_TEXTURE_EMISSIVE
		emissiveColor = sRGBTransferEOTF( emissiveColor );
	#endif
	totalEmissiveRadiance *= emissiveColor.rgb;
#endif`,DY=`#ifdef USE_EMISSIVEMAP
	uniform sampler2D emissiveMap;
#endif`,qY="gl_FragColor = linearToOutputTexel( gl_FragColor );",OY=`vec4 LinearTransferOETF( in vec4 value ) {
	return value;
}
vec4 sRGBTransferEOTF( in vec4 value ) {
	return vec4( mix( pow( value.rgb * 0.9478672986 + vec3( 0.0521327014 ), vec3( 2.4 ) ), value.rgb * 0.0773993808, vec3( lessThanEqual( value.rgb, vec3( 0.04045 ) ) ) ), value.a );
}
vec4 sRGBTransferOETF( in vec4 value ) {
	return vec4( mix( pow( value.rgb, vec3( 0.41666 ) ) * 1.055 - vec3( 0.055 ), value.rgb * 12.92, vec3( lessThanEqual( value.rgb, vec3( 0.0031308 ) ) ) ), value.a );
}`,RY=`#ifdef USE_ENVMAP
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
#endif`,kY=`#ifdef USE_ENVMAP
	uniform float envMapIntensity;
	uniform float flipEnvMap;
	uniform mat3 envMapRotation;
	#ifdef ENVMAP_TYPE_CUBE
		uniform samplerCube envMap;
	#else
		uniform sampler2D envMap;
	#endif
	
#endif`,FY=`#ifdef USE_ENVMAP
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
#endif`,MY=`#ifdef USE_ENVMAP
	#if defined( USE_BUMPMAP ) || defined( USE_NORMALMAP ) || defined( PHONG ) || defined( LAMBERT )
		#define ENV_WORLDPOS
	#endif
	#ifdef ENV_WORLDPOS
		
		varying vec3 vWorldPosition;
	#else
		varying vec3 vReflect;
		uniform float refractionRatio;
	#endif
#endif`,LY=`#ifdef USE_ENVMAP
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
#endif`,VY=`#ifdef USE_FOG
	vFogDepth = - mvPosition.z;
#endif`,zY=`#ifdef USE_FOG
	varying float vFogDepth;
#endif`,BY=`#ifdef USE_FOG
	#ifdef FOG_EXP2
		float fogFactor = 1.0 - exp( - fogDensity * fogDensity * vFogDepth * vFogDepth );
	#else
		float fogFactor = smoothstep( fogNear, fogFar, vFogDepth );
	#endif
	gl_FragColor.rgb = mix( gl_FragColor.rgb, fogColor, fogFactor );
#endif`,wY=`#ifdef USE_FOG
	uniform vec3 fogColor;
	varying float vFogDepth;
	#ifdef FOG_EXP2
		uniform float fogDensity;
	#else
		uniform float fogNear;
		uniform float fogFar;
	#endif
#endif`,CY=`#ifdef USE_GRADIENTMAP
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
}`,_Y=`#ifdef USE_LIGHTMAP
	uniform sampler2D lightMap;
	uniform float lightMapIntensity;
#endif`,IY=`LambertMaterial material;
material.diffuseColor = diffuseColor.rgb;
material.specularStrength = specularStrength;`,PY=`varying vec3 vViewPosition;
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
#define RE_IndirectDiffuse		RE_IndirectDiffuse_Lambert`,AY=`uniform bool receiveShadow;
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
#endif`,TY=`#ifdef USE_ENVMAP
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
#endif`,SY=`ToonMaterial material;
material.diffuseColor = diffuseColor.rgb;`,jY=`varying vec3 vViewPosition;
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
#define RE_IndirectDiffuse		RE_IndirectDiffuse_Toon`,yY=`BlinnPhongMaterial material;
material.diffuseColor = diffuseColor.rgb;
material.specularColor = specular;
material.specularShininess = shininess;
material.specularStrength = specularStrength;`,bY=`varying vec3 vViewPosition;
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
#define RE_IndirectDiffuse		RE_IndirectDiffuse_BlinnPhong`,fY=`PhysicalMaterial material;
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
#endif`,vY=`struct PhysicalMaterial {
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
}`,hY=`
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
#endif`,xY=`#if defined( RE_IndirectDiffuse )
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
#endif`,gY=`#if defined( RE_IndirectDiffuse )
	RE_IndirectDiffuse( irradiance, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
#endif
#if defined( RE_IndirectSpecular )
	RE_IndirectSpecular( radiance, iblIrradiance, clearcoatRadiance, geometryPosition, geometryNormal, geometryViewDir, geometryClearcoatNormal, material, reflectedLight );
#endif`,pY=`#if defined( USE_LOGARITHMIC_DEPTH_BUFFER )
	gl_FragDepth = vIsPerspective == 0.0 ? gl_FragCoord.z : log2( vFragDepth ) * logDepthBufFC * 0.5;
#endif`,lY=`#if defined( USE_LOGARITHMIC_DEPTH_BUFFER )
	uniform float logDepthBufFC;
	varying float vFragDepth;
	varying float vIsPerspective;
#endif`,dY=`#ifdef USE_LOGARITHMIC_DEPTH_BUFFER
	varying float vFragDepth;
	varying float vIsPerspective;
#endif`,mY=`#ifdef USE_LOGARITHMIC_DEPTH_BUFFER
	vFragDepth = 1.0 + gl_Position.w;
	vIsPerspective = float( isPerspectiveMatrix( projectionMatrix ) );
#endif`,uY=`#ifdef USE_MAP
	vec4 sampledDiffuseColor = texture2D( map, vMapUv );
	#ifdef DECODE_VIDEO_TEXTURE
		sampledDiffuseColor = sRGBTransferEOTF( sampledDiffuseColor );
	#endif
	diffuseColor *= sampledDiffuseColor;
#endif`,cY=`#ifdef USE_MAP
	uniform sampler2D map;
#endif`,nY=`#if defined( USE_MAP ) || defined( USE_ALPHAMAP )
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
#endif`,sY=`#if defined( USE_POINTS_UV )
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
#endif`,iY=`float metalnessFactor = metalness;
#ifdef USE_METALNESSMAP
	vec4 texelMetalness = texture2D( metalnessMap, vMetalnessMapUv );
	metalnessFactor *= texelMetalness.b;
#endif`,oY=`#ifdef USE_METALNESSMAP
	uniform sampler2D metalnessMap;
#endif`,aY=`#ifdef USE_INSTANCING_MORPH
	float morphTargetInfluences[ MORPHTARGETS_COUNT ];
	float morphTargetBaseInfluence = texelFetch( morphTexture, ivec2( 0, gl_InstanceID ), 0 ).r;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		morphTargetInfluences[i] =  texelFetch( morphTexture, ivec2( i + 1, gl_InstanceID ), 0 ).r;
	}
#endif`,rY=`#if defined( USE_MORPHCOLORS )
	vColor *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		#if defined( USE_COLOR_ALPHA )
			if ( morphTargetInfluences[ i ] != 0.0 ) vColor += getMorph( gl_VertexID, i, 2 ) * morphTargetInfluences[ i ];
		#elif defined( USE_COLOR )
			if ( morphTargetInfluences[ i ] != 0.0 ) vColor += getMorph( gl_VertexID, i, 2 ).rgb * morphTargetInfluences[ i ];
		#endif
	}
#endif`,tY=`#ifdef USE_MORPHNORMALS
	objectNormal *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		if ( morphTargetInfluences[ i ] != 0.0 ) objectNormal += getMorph( gl_VertexID, i, 1 ).xyz * morphTargetInfluences[ i ];
	}
#endif`,eY=`#ifdef USE_MORPHTARGETS
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
#endif`,JH=`#ifdef USE_MORPHTARGETS
	transformed *= morphTargetBaseInfluence;
	for ( int i = 0; i < MORPHTARGETS_COUNT; i ++ ) {
		if ( morphTargetInfluences[ i ] != 0.0 ) transformed += getMorph( gl_VertexID, i, 0 ).xyz * morphTargetInfluences[ i ];
	}
#endif`,$H=`float faceDirection = gl_FrontFacing ? 1.0 : - 1.0;
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
vec3 nonPerturbedNormal = normal;`,QH=`#ifdef USE_NORMALMAP_OBJECTSPACE
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
#endif`,ZH=`#ifndef FLAT_SHADED
	varying vec3 vNormal;
	#ifdef USE_TANGENT
		varying vec3 vTangent;
		varying vec3 vBitangent;
	#endif
#endif`,WH=`#ifndef FLAT_SHADED
	varying vec3 vNormal;
	#ifdef USE_TANGENT
		varying vec3 vTangent;
		varying vec3 vBitangent;
	#endif
#endif`,KH=`#ifndef FLAT_SHADED
	vNormal = normalize( transformedNormal );
	#ifdef USE_TANGENT
		vTangent = normalize( transformedTangent );
		vBitangent = normalize( cross( vNormal, vTangent ) * tangent.w );
	#endif
#endif`,YH=`#ifdef USE_NORMALMAP
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
#endif`,HH=`#ifdef USE_CLEARCOAT
	vec3 clearcoatNormal = nonPerturbedNormal;
#endif`,XH=`#ifdef USE_CLEARCOAT_NORMALMAP
	vec3 clearcoatMapN = texture2D( clearcoatNormalMap, vClearcoatNormalMapUv ).xyz * 2.0 - 1.0;
	clearcoatMapN.xy *= clearcoatNormalScale;
	clearcoatNormal = normalize( tbn2 * clearcoatMapN );
#endif`,NH=`#ifdef USE_CLEARCOATMAP
	uniform sampler2D clearcoatMap;
#endif
#ifdef USE_CLEARCOAT_NORMALMAP
	uniform sampler2D clearcoatNormalMap;
	uniform vec2 clearcoatNormalScale;
#endif
#ifdef USE_CLEARCOAT_ROUGHNESSMAP
	uniform sampler2D clearcoatRoughnessMap;
#endif`,UH=`#ifdef USE_IRIDESCENCEMAP
	uniform sampler2D iridescenceMap;
#endif
#ifdef USE_IRIDESCENCE_THICKNESSMAP
	uniform sampler2D iridescenceThicknessMap;
#endif`,GH=`#ifdef OPAQUE
diffuseColor.a = 1.0;
#endif
#ifdef USE_TRANSMISSION
diffuseColor.a *= material.transmissionAlpha;
#endif
gl_FragColor = vec4( outgoingLight, diffuseColor.a );`,EH=`vec3 packNormalToRGB( const in vec3 normal ) {
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
}`,DH=`#ifdef PREMULTIPLIED_ALPHA
	gl_FragColor.rgb *= gl_FragColor.a;
#endif`,qH=`vec4 mvPosition = vec4( transformed, 1.0 );
#ifdef USE_BATCHING
	mvPosition = batchingMatrix * mvPosition;
#endif
#ifdef USE_INSTANCING
	mvPosition = instanceMatrix * mvPosition;
#endif
mvPosition = modelViewMatrix * mvPosition;
gl_Position = projectionMatrix * mvPosition;`,OH=`#ifdef DITHERING
	gl_FragColor.rgb = dithering( gl_FragColor.rgb );
#endif`,RH=`#ifdef DITHERING
	vec3 dithering( vec3 color ) {
		float grid_position = rand( gl_FragCoord.xy );
		vec3 dither_shift_RGB = vec3( 0.25 / 255.0, -0.25 / 255.0, 0.25 / 255.0 );
		dither_shift_RGB = mix( 2.0 * dither_shift_RGB, -2.0 * dither_shift_RGB, grid_position );
		return color + dither_shift_RGB;
	}
#endif`,kH=`float roughnessFactor = roughness;
#ifdef USE_ROUGHNESSMAP
	vec4 texelRoughness = texture2D( roughnessMap, vRoughnessMapUv );
	roughnessFactor *= texelRoughness.g;
#endif`,FH=`#ifdef USE_ROUGHNESSMAP
	uniform sampler2D roughnessMap;
#endif`,MH=`#if NUM_SPOT_LIGHT_COORDS > 0
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
#endif`,LH=`#if NUM_SPOT_LIGHT_COORDS > 0
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
#endif`,VH=`#if ( defined( USE_SHADOWMAP ) && ( NUM_DIR_LIGHT_SHADOWS > 0 || NUM_POINT_LIGHT_SHADOWS > 0 ) ) || ( NUM_SPOT_LIGHT_COORDS > 0 )
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
#endif`,zH=`float getShadowMask() {
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
}`,BH=`#ifdef USE_SKINNING
	mat4 boneMatX = getBoneMatrix( skinIndex.x );
	mat4 boneMatY = getBoneMatrix( skinIndex.y );
	mat4 boneMatZ = getBoneMatrix( skinIndex.z );
	mat4 boneMatW = getBoneMatrix( skinIndex.w );
#endif`,wH=`#ifdef USE_SKINNING
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
#endif`,CH=`#ifdef USE_SKINNING
	vec4 skinVertex = bindMatrix * vec4( transformed, 1.0 );
	vec4 skinned = vec4( 0.0 );
	skinned += boneMatX * skinVertex * skinWeight.x;
	skinned += boneMatY * skinVertex * skinWeight.y;
	skinned += boneMatZ * skinVertex * skinWeight.z;
	skinned += boneMatW * skinVertex * skinWeight.w;
	transformed = ( bindMatrixInverse * skinned ).xyz;
#endif`,_H=`#ifdef USE_SKINNING
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
#endif`,IH=`float specularStrength;
#ifdef USE_SPECULARMAP
	vec4 texelSpecular = texture2D( specularMap, vSpecularMapUv );
	specularStrength = texelSpecular.r;
#else
	specularStrength = 1.0;
#endif`,PH=`#ifdef USE_SPECULARMAP
	uniform sampler2D specularMap;
#endif`,AH=`#if defined( TONE_MAPPING )
	gl_FragColor.rgb = toneMapping( gl_FragColor.rgb );
#endif`,TH=`#ifndef saturate
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
vec3 CustomToneMapping( vec3 color ) { return color; }`,SH=`#ifdef USE_TRANSMISSION
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
#endif`,jH=`#ifdef USE_TRANSMISSION
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
#endif`,yH=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
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
#endif`,bH=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
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
#endif`,fH=`#if defined( USE_UV ) || defined( USE_ANISOTROPY )
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
#endif`,vH=`#if defined( USE_ENVMAP ) || defined( DISTANCE ) || defined ( USE_SHADOWMAP ) || defined ( USE_TRANSMISSION ) || NUM_SPOT_LIGHT_COORDS > 0
	vec4 worldPosition = vec4( transformed, 1.0 );
	#ifdef USE_BATCHING
		worldPosition = batchingMatrix * worldPosition;
	#endif
	#ifdef USE_INSTANCING
		worldPosition = instanceMatrix * worldPosition;
	#endif
	worldPosition = modelMatrix * worldPosition;
#endif`,hH=`varying vec2 vUv;
uniform mat3 uvTransform;
void main() {
	vUv = ( uvTransform * vec3( uv, 1 ) ).xy;
	gl_Position = vec4( position.xy, 1.0, 1.0 );
}`,xH=`uniform sampler2D t2D;
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
}`,gH=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
	gl_Position.z = gl_Position.w;
}`,pH=`#ifdef ENVMAP_TYPE_CUBE
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
}`,lH=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
	gl_Position.z = gl_Position.w;
}`,dH=`uniform samplerCube tCube;
uniform float tFlip;
uniform float opacity;
varying vec3 vWorldDirection;
void main() {
	vec4 texColor = textureCube( tCube, vec3( tFlip * vWorldDirection.x, vWorldDirection.yz ) );
	gl_FragColor = texColor;
	gl_FragColor.a *= opacity;
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,mH=`#include <common>
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
}`,uH=`#if DEPTH_PACKING == 3200
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
}`,cH=`#define DISTANCE
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
}`,nH=`#define DISTANCE
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
}`,sH=`varying vec3 vWorldDirection;
#include <common>
void main() {
	vWorldDirection = transformDirection( position, modelMatrix );
	#include <begin_vertex>
	#include <project_vertex>
}`,iH=`uniform sampler2D tEquirect;
varying vec3 vWorldDirection;
#include <common>
void main() {
	vec3 direction = normalize( vWorldDirection );
	vec2 sampleUV = equirectUv( direction );
	gl_FragColor = texture2D( tEquirect, sampleUV );
	#include <tonemapping_fragment>
	#include <colorspace_fragment>
}`,oH=`uniform float scale;
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
}`,aH=`uniform vec3 diffuse;
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
}`,rH=`#include <common>
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
}`,tH=`uniform vec3 diffuse;
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
}`,eH=`#define LAMBERT
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
}`,JX=`#define LAMBERT
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
}`,$X=`#define MATCAP
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
}`,QX=`#define MATCAP
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
}`,ZX=`#define NORMAL
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
}`,WX=`#define NORMAL
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
}`,KX=`#define PHONG
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
}`,YX=`#define PHONG
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
}`,HX=`#define STANDARD
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
}`,XX=`#define STANDARD
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
}`,NX=`#define TOON
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
}`,UX=`#define TOON
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
}`,GX=`uniform float size;
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
}`,EX=`uniform vec3 diffuse;
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
}`,DX=`#include <common>
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
}`,qX=`uniform vec3 color;
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
}`,OX=`uniform float rotation;
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
}`,RX=`uniform vec3 diffuse;
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
}`,d0={alphahash_fragment:xK,alphahash_pars_fragment:gK,alphamap_fragment:pK,alphamap_pars_fragment:lK,alphatest_fragment:dK,alphatest_pars_fragment:mK,aomap_fragment:uK,aomap_pars_fragment:cK,batching_pars_vertex:nK,batching_vertex:sK,begin_vertex:iK,beginnormal_vertex:oK,bsdfs:aK,iridescence_fragment:rK,bumpmap_pars_fragment:tK,clipping_planes_fragment:eK,clipping_planes_pars_fragment:JY,clipping_planes_pars_vertex:$Y,clipping_planes_vertex:QY,color_fragment:ZY,color_pars_fragment:WY,color_pars_vertex:KY,color_vertex:YY,common:HY,cube_uv_reflection_fragment:XY,defaultnormal_vertex:NY,displacementmap_pars_vertex:UY,displacementmap_vertex:GY,emissivemap_fragment:EY,emissivemap_pars_fragment:DY,colorspace_fragment:qY,colorspace_pars_fragment:OY,envmap_fragment:RY,envmap_common_pars_fragment:kY,envmap_pars_fragment:FY,envmap_pars_vertex:MY,envmap_physical_pars_fragment:TY,envmap_vertex:LY,fog_vertex:VY,fog_pars_vertex:zY,fog_fragment:BY,fog_pars_fragment:wY,gradientmap_pars_fragment:CY,lightmap_pars_fragment:_Y,lights_lambert_fragment:IY,lights_lambert_pars_fragment:PY,lights_pars_begin:AY,lights_toon_fragment:SY,lights_toon_pars_fragment:jY,lights_phong_fragment:yY,lights_phong_pars_fragment:bY,lights_physical_fragment:fY,lights_physical_pars_fragment:vY,lights_fragment_begin:hY,lights_fragment_maps:xY,lights_fragment_end:gY,logdepthbuf_fragment:pY,logdepthbuf_pars_fragment:lY,logdepthbuf_pars_vertex:dY,logdepthbuf_vertex:mY,map_fragment:uY,map_pars_fragment:cY,map_particle_fragment:nY,map_particle_pars_fragment:sY,metalnessmap_fragment:iY,metalnessmap_pars_fragment:oY,morphinstance_vertex:aY,morphcolor_vertex:rY,morphnormal_vertex:tY,morphtarget_pars_vertex:eY,morphtarget_vertex:JH,normal_fragment_begin:$H,normal_fragment_maps:QH,normal_pars_fragment:ZH,normal_pars_vertex:WH,normal_vertex:KH,normalmap_pars_fragment:YH,clearcoat_normal_fragment_begin:HH,clearcoat_normal_fragment_maps:XH,clearcoat_pars_fragment:NH,iridescence_pars_fragment:UH,opaque_fragment:GH,packing:EH,premultiplied_alpha_fragment:DH,project_vertex:qH,dithering_fragment:OH,dithering_pars_fragment:RH,roughnessmap_fragment:kH,roughnessmap_pars_fragment:FH,shadowmap_pars_fragment:MH,shadowmap_pars_vertex:LH,shadowmap_vertex:VH,shadowmask_pars_fragment:zH,skinbase_vertex:BH,skinning_pars_vertex:wH,skinning_vertex:CH,skinnormal_vertex:_H,specularmap_fragment:IH,specularmap_pars_fragment:PH,tonemapping_fragment:AH,tonemapping_pars_fragment:TH,transmission_fragment:SH,transmission_pars_fragment:jH,uv_pars_fragment:yH,uv_pars_vertex:bH,uv_vertex:fH,worldpos_vertex:vH,background_vert:hH,background_frag:xH,backgroundCube_vert:gH,backgroundCube_frag:pH,cube_vert:lH,cube_frag:dH,depth_vert:mH,depth_frag:uH,distanceRGBA_vert:cH,distanceRGBA_frag:nH,equirect_vert:sH,equirect_frag:iH,linedashed_vert:oH,linedashed_frag:aH,meshbasic_vert:rH,meshbasic_frag:tH,meshlambert_vert:eH,meshlambert_frag:JX,meshmatcap_vert:$X,meshmatcap_frag:QX,meshnormal_vert:ZX,meshnormal_frag:WX,meshphong_vert:KX,meshphong_frag:YX,meshphysical_vert:HX,meshphysical_frag:XX,meshtoon_vert:NX,meshtoon_frag:UX,points_vert:GX,points_frag:EX,shadow_vert:DX,shadow_frag:qX,sprite_vert:OX,sprite_frag:RX},D0={common:{diffuse:{value:new n0(16777215)},opacity:{value:1},map:{value:null},mapTransform:{value:new p0},alphaMap:{value:null},alphaMapTransform:{value:new p0},alphaTest:{value:0}},specularmap:{specularMap:{value:null},specularMapTransform:{value:new p0}},envmap:{envMap:{value:null},envMapRotation:{value:new p0},flipEnvMap:{value:-1},reflectivity:{value:1},ior:{value:1.5},refractionRatio:{value:0.98}},aomap:{aoMap:{value:null},aoMapIntensity:{value:1},aoMapTransform:{value:new p0}},lightmap:{lightMap:{value:null},lightMapIntensity:{value:1},lightMapTransform:{value:new p0}},bumpmap:{bumpMap:{value:null},bumpMapTransform:{value:new p0},bumpScale:{value:1}},normalmap:{normalMap:{value:null},normalMapTransform:{value:new p0},normalScale:{value:new x0(1,1)}},displacementmap:{displacementMap:{value:null},displacementMapTransform:{value:new p0},displacementScale:{value:1},displacementBias:{value:0}},emissivemap:{emissiveMap:{value:null},emissiveMapTransform:{value:new p0}},metalnessmap:{metalnessMap:{value:null},metalnessMapTransform:{value:new p0}},roughnessmap:{roughnessMap:{value:null},roughnessMapTransform:{value:new p0}},gradientmap:{gradientMap:{value:null}},fog:{fogDensity:{value:0.00025},fogNear:{value:1},fogFar:{value:2000},fogColor:{value:new n0(16777215)}},lights:{ambientLightColor:{value:[]},lightProbe:{value:[]},directionalLights:{value:[],properties:{direction:{},color:{}}},directionalLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{}}},directionalShadowMap:{value:[]},directionalShadowMatrix:{value:[]},spotLights:{value:[],properties:{color:{},position:{},direction:{},distance:{},coneCos:{},penumbraCos:{},decay:{}}},spotLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{}}},spotLightMap:{value:[]},spotShadowMap:{value:[]},spotLightMatrix:{value:[]},pointLights:{value:[],properties:{color:{},position:{},decay:{},distance:{}}},pointLightShadows:{value:[],properties:{shadowIntensity:1,shadowBias:{},shadowNormalBias:{},shadowRadius:{},shadowMapSize:{},shadowCameraNear:{},shadowCameraFar:{}}},pointShadowMap:{value:[]},pointShadowMatrix:{value:[]},hemisphereLights:{value:[],properties:{direction:{},skyColor:{},groundColor:{}}},rectAreaLights:{value:[],properties:{color:{},position:{},width:{},height:{}}},ltc_1:{value:null},ltc_2:{value:null}},points:{diffuse:{value:new n0(16777215)},opacity:{value:1},size:{value:1},scale:{value:1},map:{value:null},alphaMap:{value:null},alphaMapTransform:{value:new p0},alphaTest:{value:0},uvTransform:{value:new p0}},sprite:{diffuse:{value:new n0(16777215)},opacity:{value:1},center:{value:new x0(0.5,0.5)},rotation:{value:0},map:{value:null},mapTransform:{value:new p0},alphaMap:{value:null},alphaMapTransform:{value:new p0},alphaTest:{value:0}}},YJ={basic:{uniforms:w9([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.fog]),vertexShader:d0.meshbasic_vert,fragmentShader:d0.meshbasic_frag},lambert:{uniforms:w9([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,D0.lights,{emissive:{value:new n0(0)}}]),vertexShader:d0.meshlambert_vert,fragmentShader:d0.meshlambert_frag},phong:{uniforms:w9([D0.common,D0.specularmap,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,D0.lights,{emissive:{value:new n0(0)},specular:{value:new n0(1118481)},shininess:{value:30}}]),vertexShader:d0.meshphong_vert,fragmentShader:d0.meshphong_frag},standard:{uniforms:w9([D0.common,D0.envmap,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.roughnessmap,D0.metalnessmap,D0.fog,D0.lights,{emissive:{value:new n0(0)},roughness:{value:1},metalness:{value:0},envMapIntensity:{value:1}}]),vertexShader:d0.meshphysical_vert,fragmentShader:d0.meshphysical_frag},toon:{uniforms:w9([D0.common,D0.aomap,D0.lightmap,D0.emissivemap,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.gradientmap,D0.fog,D0.lights,{emissive:{value:new n0(0)}}]),vertexShader:d0.meshtoon_vert,fragmentShader:d0.meshtoon_frag},matcap:{uniforms:w9([D0.common,D0.bumpmap,D0.normalmap,D0.displacementmap,D0.fog,{matcap:{value:null}}]),vertexShader:d0.meshmatcap_vert,fragmentShader:d0.meshmatcap_frag},points:{uniforms:w9([D0.points,D0.fog]),vertexShader:d0.points_vert,fragmentShader:d0.points_frag},dashed:{uniforms:w9([D0.common,D0.fog,{scale:{value:1},dashSize:{value:1},totalSize:{value:2}}]),vertexShader:d0.linedashed_vert,fragmentShader:d0.linedashed_frag},depth:{uniforms:w9([D0.common,D0.displacementmap]),vertexShader:d0.depth_vert,fragmentShader:d0.depth_frag},normal:{uniforms:w9([D0.common,D0.bumpmap,D0.normalmap,D0.displacementmap,{opacity:{value:1}}]),vertexShader:d0.meshnormal_vert,fragmentShader:d0.meshnormal_frag},sprite:{uniforms:w9([D0.sprite,D0.fog]),vertexShader:d0.sprite_vert,fragmentShader:d0.sprite_frag},background:{uniforms:{uvTransform:{value:new p0},t2D:{value:null},backgroundIntensity:{value:1}},vertexShader:d0.background_vert,fragmentShader:d0.background_frag},backgroundCube:{uniforms:{envMap:{value:null},flipEnvMap:{value:-1},backgroundBlurriness:{value:0},backgroundIntensity:{value:1},backgroundRotation:{value:new p0}},vertexShader:d0.backgroundCube_vert,fragmentShader:d0.backgroundCube_frag},cube:{uniforms:{tCube:{value:null},tFlip:{value:-1},opacity:{value:1}},vertexShader:d0.cube_vert,fragmentShader:d0.cube_frag},equirect:{uniforms:{tEquirect:{value:null}},vertexShader:d0.equirect_vert,fragmentShader:d0.equirect_frag},distanceRGBA:{uniforms:w9([D0.common,D0.displacementmap,{referencePosition:{value:new v},nearDistance:{value:1},farDistance:{value:1000}}]),vertexShader:d0.distanceRGBA_vert,fragmentShader:d0.distanceRGBA_frag},shadow:{uniforms:w9([D0.lights,D0.fog,{color:{value:new n0(0)},opacity:{value:1}}]),vertexShader:d0.shadow_vert,fragmentShader:d0.shadow_frag}};YJ.physical={uniforms:w9([YJ.standard.uniforms,{clearcoat:{value:0},clearcoatMap:{value:null},clearcoatMapTransform:{value:new p0},clearcoatNormalMap:{value:null},clearcoatNormalMapTransform:{value:new p0},clearcoatNormalScale:{value:new x0(1,1)},clearcoatRoughness:{value:0},clearcoatRoughnessMap:{value:null},clearcoatRoughnessMapTransform:{value:new p0},dispersion:{value:0},iridescence:{value:0},iridescenceMap:{value:null},iridescenceMapTransform:{value:new p0},iridescenceIOR:{value:1.3},iridescenceThicknessMinimum:{value:100},iridescenceThicknessMaximum:{value:400},iridescenceThicknessMap:{value:null},iridescenceThicknessMapTransform:{value:new p0},sheen:{value:0},sheenColor:{value:new n0(0)},sheenColorMap:{value:null},sheenColorMapTransform:{value:new p0},sheenRoughness:{value:1},sheenRoughnessMap:{value:null},sheenRoughnessMapTransform:{value:new p0},transmission:{value:0},transmissionMap:{value:null},transmissionMapTransform:{value:new p0},transmissionSamplerSize:{value:new x0},transmissionSamplerMap:{value:null},thickness:{value:0},thicknessMap:{value:null},thicknessMapTransform:{value:new p0},attenuationDistance:{value:0},attenuationColor:{value:new n0(0)},specularColor:{value:new n0(1,1,1)},specularColorMap:{value:null},specularColorMapTransform:{value:new p0},specularIntensity:{value:1},specularIntensityMap:{value:null},specularIntensityMapTransform:{value:new p0},anisotropyVector:{value:new x0},anisotropyMap:{value:null},anisotropyMapTransform:{value:new p0}}]),vertexShader:d0.meshphysical_vert,fragmentShader:d0.meshphysical_frag};var Q7={r:0,b:0,g:0},cJ=new t9,kX=new X9;function FX(J,$,Q,Z,W,K,H){let Y=new n0(0),X=K===!0?0:1,N,U,G=null,D=0,R=null;function M(V){let I=V.isScene===!0?V.background:null;if(I&&I.isTexture)I=(V.backgroundBlurriness>0?Q:$).get(I);return I}function L(V){let I=!1,f=M(V);if(f===null)q(Y,X);else if(f&&f.isColor)q(f,1),I=!0;let P=J.xr.getEnvironmentBlendMode();if(P==="additive")Z.buffers.color.setClear(0,0,0,1,H);else if(P==="alpha-blend")Z.buffers.color.setClear(0,0,0,0,H);if(J.autoClear||I)Z.buffers.depth.setTest(!0),Z.buffers.depth.setMask(!0),Z.buffers.color.setMask(!0),J.clear(J.autoClearColor,J.autoClearDepth,J.autoClearStencil)}function E(V,I){let f=M(I);if(f&&(f.isCubeTexture||f.mapping===g8)){if(U===void 0)U=new A9(new z8(1,1,1),new KJ({name:"BackgroundCubeMaterial",uniforms:dJ(YJ.backgroundCube.uniforms),vertexShader:YJ.backgroundCube.vertexShader,fragmentShader:YJ.backgroundCube.fragmentShader,side:p9,depthTest:!1,depthWrite:!1,fog:!1,allowOverride:!1})),U.geometry.deleteAttribute("normal"),U.geometry.deleteAttribute("uv"),U.onBeforeRender=function(P,S,g){this.matrixWorld.copyPosition(g.matrixWorld)},Object.defineProperty(U.material,"envMap",{get:function(){return this.uniforms.envMap.value}}),W.update(U);if(cJ.copy(I.backgroundRotation),cJ.x*=-1,cJ.y*=-1,cJ.z*=-1,f.isCubeTexture&&f.isRenderTargetTexture===!1)cJ.y*=-1,cJ.z*=-1;if(U.material.uniforms.envMap.value=f,U.material.uniforms.flipEnvMap.value=f.isCubeTexture&&f.isRenderTargetTexture===!1?-1:1,U.material.uniforms.backgroundBlurriness.value=I.backgroundBlurriness,U.material.uniforms.backgroundIntensity.value=I.backgroundIntensity,U.material.uniforms.backgroundRotation.value.setFromMatrix4(kX.makeRotationFromEuler(cJ)),U.material.toneMapped=s0.getTransfer(f.colorSpace)!==K9,G!==f||D!==f.version||R!==J.toneMapping)U.material.needsUpdate=!0,G=f,D=f.version,R=J.toneMapping;U.layers.enableAll(),V.unshift(U,U.geometry,U.material,0,0,null)}else if(f&&f.isTexture){if(N===void 0)N=new A9(new a8(2,2),new KJ({name:"BackgroundMaterial",uniforms:dJ(YJ.background.uniforms),vertexShader:YJ.background.vertexShader,fragmentShader:YJ.background.fragmentShader,side:D8,depthTest:!1,depthWrite:!1,fog:!1,allowOverride:!1})),N.geometry.deleteAttribute("normal"),Object.defineProperty(N.material,"map",{get:function(){return this.uniforms.t2D.value}}),W.update(N);if(N.material.uniforms.t2D.value=f,N.material.uniforms.backgroundIntensity.value=I.backgroundIntensity,N.material.toneMapped=s0.getTransfer(f.colorSpace)!==K9,f.matrixAutoUpdate===!0)f.updateMatrix();if(N.material.uniforms.uvTransform.value.copy(f.matrix),G!==f||D!==f.version||R!==J.toneMapping)N.material.needsUpdate=!0,G=f,D=f.version,R=J.toneMapping;N.layers.enableAll(),V.unshift(N,N.geometry,N.material,0,0,null)}}function q(V,I){V.getRGB(Q7,T$(J)),Z.buffers.color.setClear(Q7.r,Q7.g,Q7.b,I,H)}function A(){if(U!==void 0)U.geometry.dispose(),U.material.dispose(),U=void 0;if(N!==void 0)N.geometry.dispose(),N.material.dispose(),N=void 0}return{getClearColor:function(){return Y},setClearColor:function(V,I=1){Y.set(V),X=I,q(Y,X)},getClearAlpha:function(){return X},setClearAlpha:function(V){X=V,q(Y,X)},render:L,addToRenderList:E,dispose:A}}function MX(J,$){let Q=J.getParameter(J.MAX_VERTEX_ATTRIBS),Z={},W=D(null),K=W,H=!1;function Y(F,T,n,s,d){let Z0=!1,m=G(s,n,T);if(K!==m)K=m,N(K.object);if(Z0=R(F,s,n,d),Z0)M(F,s,n,d);if(d!==null)$.update(d,J.ELEMENT_ARRAY_BUFFER);if(Z0||H){if(H=!1,I(F,T,n,s),d!==null)J.bindBuffer(J.ELEMENT_ARRAY_BUFFER,$.get(d).buffer)}}function X(){return J.createVertexArray()}function N(F){return J.bindVertexArray(F)}function U(F){return J.deleteVertexArray(F)}function G(F,T,n){let s=n.wireframe===!0,d=Z[F.id];if(d===void 0)d={},Z[F.id]=d;let Z0=d[T.id];if(Z0===void 0)Z0={},d[T.id]=Z0;let m=Z0[s];if(m===void 0)m=D(X()),Z0[s]=m;return m}function D(F){let T=[],n=[],s=[];for(let d=0;d<Q;d++)T[d]=0,n[d]=0,s[d]=0;return{geometry:null,program:null,wireframe:!1,newAttributes:T,enabledAttributes:n,attributeDivisors:s,object:F,attributes:{},index:null}}function R(F,T,n,s){let d=K.attributes,Z0=T.attributes,m=0,W0=n.getAttributes();for(let u in W0)if(W0[u].location>=0){let q0=d[u],_0=Z0[u];if(_0===void 0){if(u==="instanceMatrix"&&F.instanceMatrix)_0=F.instanceMatrix;if(u==="instanceColor"&&F.instanceColor)_0=F.instanceColor}if(q0===void 0)return!0;if(q0.attribute!==_0)return!0;if(_0&&q0.data!==_0.data)return!0;m++}if(K.attributesNum!==m)return!0;if(K.index!==s)return!0;return!1}function M(F,T,n,s){let d={},Z0=T.attributes,m=0,W0=n.getAttributes();for(let u in W0)if(W0[u].location>=0){let q0=Z0[u];if(q0===void 0){if(u==="instanceMatrix"&&F.instanceMatrix)q0=F.instanceMatrix;if(u==="instanceColor"&&F.instanceColor)q0=F.instanceColor}let _0={};if(_0.attribute=q0,q0&&q0.data)_0.data=q0.data;d[u]=_0,m++}K.attributes=d,K.attributesNum=m,K.index=s}function L(){let F=K.newAttributes;for(let T=0,n=F.length;T<n;T++)F[T]=0}function E(F){q(F,0)}function q(F,T){let{newAttributes:n,enabledAttributes:s,attributeDivisors:d}=K;if(n[F]=1,s[F]===0)J.enableVertexAttribArray(F),s[F]=1;if(d[F]!==T)J.vertexAttribDivisor(F,T),d[F]=T}function A(){let{newAttributes:F,enabledAttributes:T}=K;for(let n=0,s=T.length;n<s;n++)if(T[n]!==F[n])J.disableVertexAttribArray(n),T[n]=0}function V(F,T,n,s,d,Z0,m){if(m===!0)J.vertexAttribIPointer(F,T,n,d,Z0);else J.vertexAttribPointer(F,T,n,s,d,Z0)}function I(F,T,n,s){L();let d=s.attributes,Z0=n.getAttributes(),m=T.defaultAttributeValues;for(let W0 in Z0){let u=Z0[W0];if(u.location>=0){let N0=d[W0];if(N0===void 0){if(W0==="instanceMatrix"&&F.instanceMatrix)N0=F.instanceMatrix;if(W0==="instanceColor"&&F.instanceColor)N0=F.instanceColor}if(N0!==void 0){let{normalized:q0,itemSize:_0}=N0,g0=$.get(N0);if(g0===void 0)continue;let{buffer:a0,type:m0,bytesPerElement:o}=g0,H0=m0===J.INT||m0===J.UNSIGNED_INT||N0.gpuType===p7;if(N0.isInterleavedBufferAttribute){let h=N0.data,t=h.stride,Q0=N0.offset;if(h.isInstancedInterleavedBuffer){for(let E0=0;E0<u.locationSize;E0++)q(u.location+E0,h.meshPerAttribute);if(F.isInstancedMesh!==!0&&s._maxInstanceCount===void 0)s._maxInstanceCount=h.meshPerAttribute*h.count}else for(let E0=0;E0<u.locationSize;E0++)E(u.location+E0);J.bindBuffer(J.ARRAY_BUFFER,a0);for(let E0=0;E0<u.locationSize;E0++)V(u.location+E0,_0/u.locationSize,m0,q0,t*o,(Q0+_0/u.locationSize*E0)*o,H0)}else{if(N0.isInstancedBufferAttribute){for(let h=0;h<u.locationSize;h++)q(u.location+h,N0.meshPerAttribute);if(F.isInstancedMesh!==!0&&s._maxInstanceCount===void 0)s._maxInstanceCount=N0.meshPerAttribute*N0.count}else for(let h=0;h<u.locationSize;h++)E(u.location+h);J.bindBuffer(J.ARRAY_BUFFER,a0);for(let h=0;h<u.locationSize;h++)V(u.location+h,_0/u.locationSize,m0,q0,_0*o,_0/u.locationSize*h*o,H0)}}else if(m!==void 0){let q0=m[W0];if(q0!==void 0)switch(q0.length){case 2:J.vertexAttrib2fv(u.location,q0);break;case 3:J.vertexAttrib3fv(u.location,q0);break;case 4:J.vertexAttrib4fv(u.location,q0);break;default:J.vertexAttrib1fv(u.location,q0)}}}}A()}function f(){g();for(let F in Z){let T=Z[F];for(let n in T){let s=T[n];for(let d in s)U(s[d].object),delete s[d];delete T[n]}delete Z[F]}}function P(F){if(Z[F.id]===void 0)return;let T=Z[F.id];for(let n in T){let s=T[n];for(let d in s)U(s[d].object),delete s[d];delete T[n]}delete Z[F.id]}function S(F){for(let T in Z){let n=Z[T];if(n[F.id]===void 0)continue;let s=n[F.id];for(let d in s)U(s[d].object),delete s[d];delete n[F.id]}}function g(){if(z(),H=!0,K===W)return;K=W,N(K.object)}function z(){W.geometry=null,W.program=null,W.wireframe=!1}return{setup:Y,reset:g,resetDefaultState:z,dispose:f,releaseStatesOfGeometry:P,releaseStatesOfProgram:S,initAttributes:L,enableAttribute:E,disableUnusedAttributes:A}}function LX(J,$,Q){let Z;function W(N){Z=N}function K(N,U){J.drawArrays(Z,N,U),Q.update(U,Z,1)}function H(N,U,G){if(G===0)return;J.drawArraysInstanced(Z,N,U,G),Q.update(U,Z,G)}function Y(N,U,G){if(G===0)return;$.get("WEBGL_multi_draw").multiDrawArraysWEBGL(Z,N,0,U,0,G);let R=0;for(let M=0;M<G;M++)R+=U[M];Q.update(R,Z,1)}function X(N,U,G,D){if(G===0)return;let R=$.get("WEBGL_multi_draw");if(R===null)for(let M=0;M<N.length;M++)H(N[M],U[M],D[M]);else{R.multiDrawArraysInstancedWEBGL(Z,N,0,U,0,D,0,G);let M=0;for(let L=0;L<G;L++)M+=U[L]*D[L];Q.update(M,Z,1)}}this.setMode=W,this.render=K,this.renderInstances=H,this.renderMultiDraw=Y,this.renderMultiDrawInstances=X}function VX(J,$,Q,Z){let W;function K(){if(W!==void 0)return W;if($.has("EXT_texture_filter_anisotropic")===!0){let S=$.get("EXT_texture_filter_anisotropic");W=J.getParameter(S.MAX_TEXTURE_MAX_ANISOTROPY_EXT)}else W=0;return W}function H(S){if(S!==ZJ&&Z.convert(S)!==J.getParameter(J.IMPLEMENTATION_COLOR_READ_FORMAT))return!1;return!0}function Y(S){let g=S===d8&&($.has("EXT_color_buffer_half_float")||$.has("EXT_color_buffer_float"));if(S!==PJ&&Z.convert(S)!==J.getParameter(J.IMPLEMENTATION_COLOR_READ_TYPE)&&S!==AJ&&!g)return!1;return!0}function X(S){if(S==="highp"){if(J.getShaderPrecisionFormat(J.VERTEX_SHADER,J.HIGH_FLOAT).precision>0&&J.getShaderPrecisionFormat(J.FRAGMENT_SHADER,J.HIGH_FLOAT).precision>0)return"highp";S="mediump"}if(S==="mediump"){if(J.getShaderPrecisionFormat(J.VERTEX_SHADER,J.MEDIUM_FLOAT).precision>0&&J.getShaderPrecisionFormat(J.FRAGMENT_SHADER,J.MEDIUM_FLOAT).precision>0)return"mediump"}return"lowp"}let N=Q.precision!==void 0?Q.precision:"highp",U=X(N);if(U!==N)console.warn("THREE.WebGLRenderer:",N,"not supported, using",U,"instead."),N=U;let G=Q.logarithmicDepthBuffer===!0,D=Q.reversedDepthBuffer===!0&&$.has("EXT_clip_control"),R=J.getParameter(J.MAX_TEXTURE_IMAGE_UNITS),M=J.getParameter(J.MAX_VERTEX_TEXTURE_IMAGE_UNITS),L=J.getParameter(J.MAX_TEXTURE_SIZE),E=J.getParameter(J.MAX_CUBE_MAP_TEXTURE_SIZE),q=J.getParameter(J.MAX_VERTEX_ATTRIBS),A=J.getParameter(J.MAX_VERTEX_UNIFORM_VECTORS),V=J.getParameter(J.MAX_VARYING_VECTORS),I=J.getParameter(J.MAX_FRAGMENT_UNIFORM_VECTORS),f=M>0,P=J.getParameter(J.MAX_SAMPLES);return{isWebGL2:!0,getMaxAnisotropy:K,getMaxPrecision:X,textureFormatReadable:H,textureTypeReadable:Y,precision:N,logarithmicDepthBuffer:G,reversedDepthBuffer:D,maxTextures:R,maxVertexTextures:M,maxTextureSize:L,maxCubemapSize:E,maxAttributes:q,maxVertexUniforms:A,maxVaryings:V,maxFragmentUniforms:I,vertexTextures:f,maxSamples:P}}function zX(J){let $=this,Q=null,Z=0,W=!1,K=!1,H=new g9,Y=new p0,X={value:null,needsUpdate:!1};this.uniform=X,this.numPlanes=0,this.numIntersection=0,this.init=function(G,D){let R=G.length!==0||D||Z!==0||W;return W=D,Z=G.length,R},this.beginShadows=function(){K=!0,U(null)},this.endShadows=function(){K=!1},this.setGlobalState=function(G,D){Q=U(G,D,0)},this.setState=function(G,D,R){let{clippingPlanes:M,clipIntersection:L,clipShadows:E}=G,q=J.get(G);if(!W||M===null||M.length===0||K&&!E)if(K)U(null);else N();else{let A=K?0:Z,V=A*4,I=q.clippingState||null;X.value=I,I=U(M,D,V,R);for(let f=0;f!==V;++f)I[f]=Q[f];q.clippingState=I,this.numIntersection=L?this.numPlanes:0,this.numPlanes+=A}};function N(){if(X.value!==Q)X.value=Q,X.needsUpdate=Z>0;$.numPlanes=Z,$.numIntersection=0}function U(G,D,R,M){let L=G!==null?G.length:0,E=null;if(L!==0){if(E=X.value,M!==!0||E===null){let q=R+L*4,A=D.matrixWorldInverse;if(Y.getNormalMatrix(A),E===null||E.length<q)E=new Float32Array(q);for(let V=0,I=R;V!==L;++V,I+=4)H.copy(G[V]).applyMatrix4(A,Y),H.normal.toArray(E,I),E[I+3]=H.constant}X.value=E,X.needsUpdate=!0}return $.numPlanes=L,$.numIntersection=0,E}}function BX(J){let $=new WeakMap;function Q(H,Y){if(Y===S6)H.mapping=O8;else if(Y===j6)H.mapping=hJ;return H}function Z(H){if(H&&H.isTexture){let Y=H.mapping;if(Y===S6||Y===j6)if($.has(H)){let X=$.get(H).texture;return Q(X,H.mapping)}else{let X=H.image;if(X&&X.height>0){let N=new j$(X.height);return N.fromEquirectangularTexture(J,H),$.set(H,N),H.addEventListener("dispose",W),Q(N.texture,H.mapping)}else return null}}return H}function W(H){let Y=H.target;Y.removeEventListener("dispose",W);let X=$.get(Y);if(X!==void 0)$.delete(Y),X.dispose()}function K(){$=new WeakMap}return{get:Z,dispose:K}}var _8=4,rZ=[0.125,0.215,0.35,0.446,0.526,0.582],iJ=20,i$=new t6,tZ=new n0,o$=null,a$=0,r$=0,t$=!1,sJ=(1+Math.sqrt(5))/2,C8=1/sJ,eZ=[new v(-sJ,C8,0),new v(sJ,C8,0),new v(-C8,0,sJ),new v(C8,0,sJ),new v(0,sJ,-C8),new v(0,sJ,C8),new v(-1,1,-1),new v(1,1,-1),new v(-1,1,1),new v(1,1,1)],wX=new v;class JQ{constructor(J){this._renderer=J,this._pingPongRenderTarget=null,this._lodMax=0,this._cubeSize=0,this._lodPlanes=[],this._sizeLods=[],this._sigmas=[],this._blurMaterial=null,this._cubemapMaterial=null,this._equirectMaterial=null,this._compileMaterial(this._blurMaterial)}fromScene(J,$=0,Q=0.1,Z=100,W={}){let{size:K=256,position:H=wX}=W;o$=this._renderer.getRenderTarget(),a$=this._renderer.getActiveCubeFace(),r$=this._renderer.getActiveMipmapLevel(),t$=this._renderer.xr.enabled,this._renderer.xr.enabled=!1,this._setSize(K);let Y=this._allocateTargets();if(Y.depthBuffer=!0,this._sceneToCubeUV(J,Q,Z,Y,H),$>0)this._blur(Y,0,0,$);return this._applyPMREM(Y),this._cleanup(Y),Y}fromEquirectangular(J,$=null){return this._fromTexture(J,$)}fromCubemap(J,$=null){return this._fromTexture(J,$)}compileCubemapShader(){if(this._cubemapMaterial===null)this._cubemapMaterial=QW(),this._compileMaterial(this._cubemapMaterial)}compileEquirectangularShader(){if(this._equirectMaterial===null)this._equirectMaterial=$W(),this._compileMaterial(this._equirectMaterial)}dispose(){if(this._dispose(),this._cubemapMaterial!==null)this._cubemapMaterial.dispose();if(this._equirectMaterial!==null)this._equirectMaterial.dispose()}_setSize(J){this._lodMax=Math.floor(Math.log2(J)),this._cubeSize=Math.pow(2,this._lodMax)}_dispose(){if(this._blurMaterial!==null)this._blurMaterial.dispose();if(this._pingPongRenderTarget!==null)this._pingPongRenderTarget.dispose();for(let J=0;J<this._lodPlanes.length;J++)this._lodPlanes[J].dispose()}_cleanup(J){this._renderer.setRenderTarget(o$,a$,r$),this._renderer.xr.enabled=t$,J.scissorTest=!1,Z7(J,0,0,J.width,J.height)}_fromTexture(J,$){if(J.mapping===O8||J.mapping===hJ)this._setSize(J.image.length===0?16:J.image[0].width||J.image[0].image.width);else this._setSize(J.image.width/4);o$=this._renderer.getRenderTarget(),a$=this._renderer.getActiveCubeFace(),r$=this._renderer.getActiveMipmapLevel(),t$=this._renderer.xr.enabled,this._renderer.xr.enabled=!1;let Q=$||this._allocateTargets();return this._textureToCubeUV(J,Q),this._applyPMREM(Q),this._cleanup(Q),Q}_allocateTargets(){let J=3*Math.max(this._cubeSize,112),$=4*this._cubeSize,Q={magFilter:xJ,minFilter:xJ,generateMipmaps:!1,type:d8,format:ZJ,colorSpace:u8,depthBuffer:!1},Z=JW(J,$,Q);if(this._pingPongRenderTarget===null||this._pingPongRenderTarget.width!==J||this._pingPongRenderTarget.height!==$){if(this._pingPongRenderTarget!==null)this._dispose();this._pingPongRenderTarget=JW(J,$,Q);let{_lodMax:W}=this;({sizeLods:this._sizeLods,lodPlanes:this._lodPlanes,sigmas:this._sigmas}=CX(W)),this._blurMaterial=_X(W,J,$)}return Z}_compileMaterial(J){let $=new A9(this._lodPlanes[0],J);this._renderer.compile($,i$)}_sceneToCubeUV(J,$,Q,Z,W){let Y=new I9(90,1,$,Q),X=[1,-1,1,1,1,1],N=[1,1,1,-1,-1,-1],U=this._renderer,G=U.autoClear,D=U.toneMapping;if(U.getClearColor(tZ),U.toneMapping=RJ,U.autoClear=!1,U.state.buffers.depth.getReversed())U.setRenderTarget(Z),U.clearDepth(),U.setRenderTarget(null);let M=new lJ({name:"PMREM.Background",side:p9,depthWrite:!1,depthTest:!1}),L=new A9(new z8,M),E=!1,q=J.background;if(q){if(q.isColor)M.color.copy(q),J.background=null,E=!0}else M.color.copy(tZ),E=!0;for(let A=0;A<6;A++){let V=A%3;if(V===0)Y.up.set(0,X[A],0),Y.position.set(W.x,W.y,W.z),Y.lookAt(W.x+N[A],W.y,W.z);else if(V===1)Y.up.set(0,0,X[A]),Y.position.set(W.x,W.y,W.z),Y.lookAt(W.x,W.y+N[A],W.z);else Y.up.set(0,X[A],0),Y.position.set(W.x,W.y,W.z),Y.lookAt(W.x,W.y,W.z+N[A]);let I=this._cubeSize;if(Z7(Z,V*I,A>2?I:0,I,I),U.setRenderTarget(Z),E)U.render(L,Y);U.render(J,Y)}L.geometry.dispose(),L.material.dispose(),U.toneMapping=D,U.autoClear=G,J.background=q}_textureToCubeUV(J,$){let Q=this._renderer,Z=J.mapping===O8||J.mapping===hJ;if(Z){if(this._cubemapMaterial===null)this._cubemapMaterial=QW();this._cubemapMaterial.uniforms.flipEnvMap.value=J.isRenderTargetTexture===!1?-1:1}else if(this._equirectMaterial===null)this._equirectMaterial=$W();let W=Z?this._cubemapMaterial:this._equirectMaterial,K=new A9(this._lodPlanes[0],W),H=W.uniforms;H.envMap.value=J;let Y=this._cubeSize;Z7($,0,0,3*Y,2*Y),Q.setRenderTarget($),Q.render(K,i$)}_applyPMREM(J){let $=this._renderer,Q=$.autoClear;$.autoClear=!1;let Z=this._lodPlanes.length;for(let W=1;W<Z;W++){let K=Math.sqrt(this._sigmas[W]*this._sigmas[W]-this._sigmas[W-1]*this._sigmas[W-1]),H=eZ[(Z-W-1)%eZ.length];this._blur(J,W-1,W,K,H)}$.autoClear=Q}_blur(J,$,Q,Z,W){let K=this._pingPongRenderTarget;this._halfBlur(J,K,$,Q,Z,"latitudinal",W),this._halfBlur(K,J,Q,Q,Z,"longitudinal",W)}_halfBlur(J,$,Q,Z,W,K,H){let Y=this._renderer,X=this._blurMaterial;if(K!=="latitudinal"&&K!=="longitudinal")console.error("blur direction must be either latitudinal or longitudinal!");let N=3,U=new A9(this._lodPlanes[Z],X),G=X.uniforms,D=this._sizeLods[Q]-1,R=isFinite(W)?Math.PI/(2*D):2*Math.PI/(2*iJ-1),M=W/R,L=isFinite(W)?1+Math.floor(N*M):iJ;if(L>iJ)console.warn(`sigmaRadians, ${W}, is too large and will clip, as it requested ${L} samples when the maximum is set to ${iJ}`);let E=[],q=0;for(let P=0;P<iJ;++P){let S=P/M,g=Math.exp(-S*S/2);if(E.push(g),P===0)q+=g;else if(P<L)q+=2*g}for(let P=0;P<E.length;P++)E[P]=E[P]/q;if(G.envMap.value=J.texture,G.samples.value=L,G.weights.value=E,G.latitudinal.value=K==="latitudinal",H)G.poleAxis.value=H;let{_lodMax:A}=this;G.dTheta.value=R,G.mipInt.value=A-Q;let V=this._sizeLods[Z],I=3*V*(Z>A-_8?Z-A+_8:0),f=4*(this._cubeSize-V);Z7($,I,f,3*V,2*V),Y.setRenderTarget($),Y.render(U,i$)}}function CX(J){let $=[],Q=[],Z=[],W=J,K=J-_8+1+rZ.length;for(let H=0;H<K;H++){let Y=Math.pow(2,W);Q.push(Y);let X=1/Y;if(H>J-_8)X=rZ[H-J+_8-1];else if(H===0)X=0;Z.push(X);let N=1/(Y-2),U=-N,G=1+N,D=[U,U,G,U,G,G,U,U,G,G,U,G],R=6,M=6,L=3,E=2,q=1,A=new Float32Array(L*M*R),V=new Float32Array(E*M*R),I=new Float32Array(q*M*R);for(let P=0;P<R;P++){let S=P%3*2/3-1,g=P>2?0:-1,z=[S,g,0,S+0.6666666666666666,g,0,S+0.6666666666666666,g+1,0,S,g,0,S+0.6666666666666666,g+1,0,S,g+1,0];A.set(z,L*M*P),V.set(D,E*M*P);let F=[P,P,P,P,P,P];I.set(F,q*M*P)}let f=new l9;if(f.setAttribute("position",new n9(A,L)),f.setAttribute("uv",new n9(V,E)),f.setAttribute("faceIndex",new n9(I,q)),$.push(f),W>_8)W--}return{lodPlanes:$,sizeLods:Q,sigmas:Z}}function JW(J,$,Q){let Z=new FJ(J,$,Q);return Z.texture.mapping=g8,Z.texture.name="PMREM.cubeUv",Z.scissorTest=!0,Z}function Z7(J,$,Q,Z,W){J.viewport.set($,Q,Z,W),J.scissor.set($,Q,Z,W)}function _X(J,$,Q){let Z=new Float32Array(iJ),W=new v(0,1,0);return new KJ({name:"SphericalGaussianBlur",defines:{n:iJ,CUBEUV_TEXEL_WIDTH:1/$,CUBEUV_TEXEL_HEIGHT:1/Q,CUBEUV_MAX_MIP:`${J}.0`},uniforms:{envMap:{value:null},samples:{value:1},weights:{value:Z},latitudinal:{value:!1},dTheta:{value:0},mipInt:{value:0},poleAxis:{value:W}},vertexShader:QQ(),fragmentShader:`

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
		`,blending:IJ,depthTest:!1,depthWrite:!1})}function $W(){return new KJ({name:"EquirectangularToCubeUV",uniforms:{envMap:{value:null}},vertexShader:QQ(),fragmentShader:`

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
		`,blending:IJ,depthTest:!1,depthWrite:!1})}function QW(){return new KJ({name:"CubemapToCubeUV",uniforms:{envMap:{value:null},flipEnvMap:{value:-1}},vertexShader:QQ(),fragmentShader:`

			precision mediump float;
			precision mediump int;

			uniform float flipEnvMap;

			varying vec3 vOutputDirection;

			uniform samplerCube envMap;

			void main() {

				gl_FragColor = textureCube( envMap, vec3( flipEnvMap * vOutputDirection.x, vOutputDirection.yz ) );

			}
		`,blending:IJ,depthTest:!1,depthWrite:!1})}function QQ(){return`

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
	`}function IX(J){let $=new WeakMap,Q=null;function Z(Y){if(Y&&Y.isTexture){let X=Y.mapping,N=X===S6||X===j6,U=X===O8||X===hJ;if(N||U){let G=$.get(Y),D=G!==void 0?G.texture.pmremVersion:0;if(Y.isRenderTargetTexture&&Y.pmremVersion!==D){if(Q===null)Q=new JQ(J);return G=N?Q.fromEquirectangular(Y,G):Q.fromCubemap(Y,G),G.texture.pmremVersion=Y.pmremVersion,$.set(Y,G),G.texture}else if(G!==void 0)return G.texture;else{let R=Y.image;if(N&&R&&R.height>0||U&&R&&W(R)){if(Q===null)Q=new JQ(J);return G=N?Q.fromEquirectangular(Y):Q.fromCubemap(Y),G.texture.pmremVersion=Y.pmremVersion,$.set(Y,G),Y.addEventListener("dispose",K),G.texture}else return null}}}return Y}function W(Y){let X=0,N=6;for(let U=0;U<N;U++)if(Y[U]!==void 0)X++;return X===N}function K(Y){let X=Y.target;X.removeEventListener("dispose",K);let N=$.get(X);if(N!==void 0)$.delete(X),N.dispose()}function H(){if($=new WeakMap,Q!==null)Q.dispose(),Q=null}return{get:Z,dispose:H}}function PX(J){let $={};function Q(Z){if($[Z]!==void 0)return $[Z];let W;switch(Z){case"WEBGL_depth_texture":W=J.getExtension("WEBGL_depth_texture")||J.getExtension("MOZ_WEBGL_depth_texture")||J.getExtension("WEBKIT_WEBGL_depth_texture");break;case"EXT_texture_filter_anisotropic":W=J.getExtension("EXT_texture_filter_anisotropic")||J.getExtension("MOZ_EXT_texture_filter_anisotropic")||J.getExtension("WEBKIT_EXT_texture_filter_anisotropic");break;case"WEBGL_compressed_texture_s3tc":W=J.getExtension("WEBGL_compressed_texture_s3tc")||J.getExtension("MOZ_WEBGL_compressed_texture_s3tc")||J.getExtension("WEBKIT_WEBGL_compressed_texture_s3tc");break;case"WEBGL_compressed_texture_pvrtc":W=J.getExtension("WEBGL_compressed_texture_pvrtc")||J.getExtension("WEBKIT_WEBGL_compressed_texture_pvrtc");break;default:W=J.getExtension(Z)}return $[Z]=W,W}return{has:function(Z){return Q(Z)!==null},init:function(){Q("EXT_color_buffer_float"),Q("WEBGL_clip_cull_distance"),Q("OES_texture_float_linear"),Q("EXT_color_buffer_half_float"),Q("WEBGL_multisampled_render_to_texture"),Q("WEBGL_render_shared_exponent")},get:function(Z){let W=Q(Z);if(W===null)E8("THREE.WebGLRenderer: "+Z+" extension not supported.");return W}}}function AX(J,$,Q,Z){let W={},K=new WeakMap;function H(G){let D=G.target;if(D.index!==null)$.remove(D.index);for(let M in D.attributes)$.remove(D.attributes[M]);D.removeEventListener("dispose",H),delete W[D.id];let R=K.get(D);if(R)$.remove(R),K.delete(D);if(Z.releaseStatesOfGeometry(D),D.isInstancedBufferGeometry===!0)delete D._maxInstanceCount;Q.memory.geometries--}function Y(G,D){if(W[D.id]===!0)return D;return D.addEventListener("dispose",H),W[D.id]=!0,Q.memory.geometries++,D}function X(G){let D=G.attributes;for(let R in D)$.update(D[R],J.ARRAY_BUFFER)}function N(G){let D=[],R=G.index,M=G.attributes.position,L=0;if(R!==null){let A=R.array;L=R.version;for(let V=0,I=A.length;V<I;V+=3){let f=A[V+0],P=A[V+1],S=A[V+2];D.push(f,P,P,S,S,f)}}else if(M!==void 0){let A=M.array;L=M.version;for(let V=0,I=A.length/3-1;V<I;V+=3){let f=V+0,P=V+1,S=V+2;D.push(f,P,P,S,S,f)}}else return;let E=new((_$(D))?l6:p6)(D,1);E.version=L;let q=K.get(G);if(q)$.remove(q);K.set(G,E)}function U(G){let D=K.get(G);if(D){let R=G.index;if(R!==null){if(D.version<R.version)N(G)}}else N(G);return K.get(G)}return{get:Y,update:X,getWireframeAttribute:U}}function TX(J,$,Q){let Z;function W(D){Z=D}let K,H;function Y(D){K=D.type,H=D.bytesPerElement}function X(D,R){J.drawElements(Z,R,K,D*H),Q.update(R,Z,1)}function N(D,R,M){if(M===0)return;J.drawElementsInstanced(Z,R,K,D*H,M),Q.update(R,Z,M)}function U(D,R,M){if(M===0)return;$.get("WEBGL_multi_draw").multiDrawElementsWEBGL(Z,R,0,K,D,0,M);let E=0;for(let q=0;q<M;q++)E+=R[q];Q.update(E,Z,1)}function G(D,R,M,L){if(M===0)return;let E=$.get("WEBGL_multi_draw");if(E===null)for(let q=0;q<D.length;q++)N(D[q]/H,R[q],L[q]);else{E.multiDrawElementsInstancedWEBGL(Z,R,0,K,D,0,L,0,M);let q=0;for(let A=0;A<M;A++)q+=R[A]*L[A];Q.update(q,Z,1)}}this.setMode=W,this.setIndex=Y,this.render=X,this.renderInstances=N,this.renderMultiDraw=U,this.renderMultiDrawInstances=G}function SX(J){let $={geometries:0,textures:0},Q={frame:0,calls:0,triangles:0,points:0,lines:0};function Z(K,H,Y){switch(Q.calls++,H){case J.TRIANGLES:Q.triangles+=Y*(K/3);break;case J.LINES:Q.lines+=Y*(K/2);break;case J.LINE_STRIP:Q.lines+=Y*(K-1);break;case J.LINE_LOOP:Q.lines+=Y*K;break;case J.POINTS:Q.points+=Y*K;break;default:console.error("THREE.WebGLInfo: Unknown draw mode:",H);break}}function W(){Q.calls=0,Q.triangles=0,Q.points=0,Q.lines=0}return{memory:$,render:Q,programs:null,autoReset:!0,reset:W,update:Z}}function jX(J,$,Q){let Z=new WeakMap,W=new G9;function K(H,Y,X){let N=H.morphTargetInfluences,U=Y.morphAttributes.position||Y.morphAttributes.normal||Y.morphAttributes.color,G=U!==void 0?U.length:0,D=Z.get(Y);if(D===void 0||D.count!==G){let z=function(){S.dispose(),Z.delete(Y),Y.removeEventListener("dispose",z)};if(D!==void 0)D.texture.dispose();let R=Y.morphAttributes.position!==void 0,M=Y.morphAttributes.normal!==void 0,L=Y.morphAttributes.color!==void 0,E=Y.morphAttributes.position||[],q=Y.morphAttributes.normal||[],A=Y.morphAttributes.color||[],V=0;if(R===!0)V=1;if(M===!0)V=2;if(L===!0)V=3;let I=Y.attributes.position.count*V,f=1;if(I>$.maxTextureSize)f=Math.ceil(I/$.maxTextureSize),I=$.maxTextureSize;let P=new Float32Array(I*f*4*G),S=new g6(P,I,f,G);S.type=AJ,S.needsUpdate=!0;let g=V*4;for(let F=0;F<G;F++){let T=E[F],n=q[F],s=A[F],d=I*f*4*F;for(let Z0=0;Z0<T.count;Z0++){let m=Z0*g;if(R===!0)W.fromBufferAttribute(T,Z0),P[d+m+0]=W.x,P[d+m+1]=W.y,P[d+m+2]=W.z,P[d+m+3]=0;if(M===!0)W.fromBufferAttribute(n,Z0),P[d+m+4]=W.x,P[d+m+5]=W.y,P[d+m+6]=W.z,P[d+m+7]=0;if(L===!0)W.fromBufferAttribute(s,Z0),P[d+m+8]=W.x,P[d+m+9]=W.y,P[d+m+10]=W.z,P[d+m+11]=s.itemSize===4?W.w:1}}D={count:G,texture:S,size:new x0(I,f)},Z.set(Y,D),Y.addEventListener("dispose",z)}if(H.isInstancedMesh===!0&&H.morphTexture!==null)X.getUniforms().setValue(J,"morphTexture",H.morphTexture,Q);else{let R=0;for(let L=0;L<N.length;L++)R+=N[L];let M=Y.morphTargetsRelative?1:1-R;X.getUniforms().setValue(J,"morphTargetBaseInfluence",M),X.getUniforms().setValue(J,"morphTargetInfluences",N)}X.getUniforms().setValue(J,"morphTargetsTexture",D.texture,Q),X.getUniforms().setValue(J,"morphTargetsTextureSize",D.size)}return{update:K}}function yX(J,$,Q,Z){let W=new WeakMap;function K(X){let N=Z.render.frame,U=X.geometry,G=$.get(X,U);if(W.get(G)!==N)$.update(G),W.set(G,N);if(X.isInstancedMesh){if(X.hasEventListener("dispose",Y)===!1)X.addEventListener("dispose",Y);if(W.get(X)!==N){if(Q.update(X.instanceMatrix,J.ARRAY_BUFFER),X.instanceColor!==null)Q.update(X.instanceColor,J.ARRAY_BUFFER);W.set(X,N)}}if(X.isSkinnedMesh){let D=X.skeleton;if(W.get(D)!==N)D.update(),W.set(D,N)}return G}function H(){W=new WeakMap}function Y(X){let N=X.target;if(N.removeEventListener("dispose",Y),Q.remove(N.instanceMatrix),N.instanceColor!==null)Q.remove(N.instanceColor)}return{update:K,dispose:H}}var VW=new P9,ZW=new n6(1,1),zW=new g6,BW=new A$,wW=new m6,WW=[],KW=[],YW=new Float32Array(16),HW=new Float32Array(9),XW=new Float32Array(4);function I8(J,$,Q){let Z=J[0];if(Z<=0||Z>0)return J;let W=$*Q,K=WW[W];if(K===void 0)K=new Float32Array(W),WW[W]=K;if($!==0){Z.toArray(K,0);for(let H=1,Y=0;H!==$;++H)Y+=Q,J[H].toArray(K,Y)}return K}function k9(J,$){if(J.length!==$.length)return!1;for(let Q=0,Z=J.length;Q<Z;Q++)if(J[Q]!==$[Q])return!1;return!0}function F9(J,$){for(let Q=0,Z=$.length;Q<Z;Q++)J[Q]=$[Q]}function K7(J,$){let Q=KW[$];if(Q===void 0)Q=new Int32Array($),KW[$]=Q;for(let Z=0;Z!==$;++Z)Q[Z]=J.allocateTextureUnit();return Q}function bX(J,$){let Q=this.cache;if(Q[0]===$)return;J.uniform1f(this.addr,$),Q[0]=$}function fX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y)J.uniform2f(this.addr,$.x,$.y),Q[0]=$.x,Q[1]=$.y}else{if(k9(Q,$))return;J.uniform2fv(this.addr,$),F9(Q,$)}}function vX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z)J.uniform3f(this.addr,$.x,$.y,$.z),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z}else if($.r!==void 0){if(Q[0]!==$.r||Q[1]!==$.g||Q[2]!==$.b)J.uniform3f(this.addr,$.r,$.g,$.b),Q[0]=$.r,Q[1]=$.g,Q[2]=$.b}else{if(k9(Q,$))return;J.uniform3fv(this.addr,$),F9(Q,$)}}function hX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z||Q[3]!==$.w)J.uniform4f(this.addr,$.x,$.y,$.z,$.w),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z,Q[3]=$.w}else{if(k9(Q,$))return;J.uniform4fv(this.addr,$),F9(Q,$)}}function xX(J,$){let Q=this.cache,Z=$.elements;if(Z===void 0){if(k9(Q,$))return;J.uniformMatrix2fv(this.addr,!1,$),F9(Q,$)}else{if(k9(Q,Z))return;XW.set(Z),J.uniformMatrix2fv(this.addr,!1,XW),F9(Q,Z)}}function gX(J,$){let Q=this.cache,Z=$.elements;if(Z===void 0){if(k9(Q,$))return;J.uniformMatrix3fv(this.addr,!1,$),F9(Q,$)}else{if(k9(Q,Z))return;HW.set(Z),J.uniformMatrix3fv(this.addr,!1,HW),F9(Q,Z)}}function pX(J,$){let Q=this.cache,Z=$.elements;if(Z===void 0){if(k9(Q,$))return;J.uniformMatrix4fv(this.addr,!1,$),F9(Q,$)}else{if(k9(Q,Z))return;YW.set(Z),J.uniformMatrix4fv(this.addr,!1,YW),F9(Q,Z)}}function lX(J,$){let Q=this.cache;if(Q[0]===$)return;J.uniform1i(this.addr,$),Q[0]=$}function dX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y)J.uniform2i(this.addr,$.x,$.y),Q[0]=$.x,Q[1]=$.y}else{if(k9(Q,$))return;J.uniform2iv(this.addr,$),F9(Q,$)}}function mX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z)J.uniform3i(this.addr,$.x,$.y,$.z),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z}else{if(k9(Q,$))return;J.uniform3iv(this.addr,$),F9(Q,$)}}function uX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z||Q[3]!==$.w)J.uniform4i(this.addr,$.x,$.y,$.z,$.w),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z,Q[3]=$.w}else{if(k9(Q,$))return;J.uniform4iv(this.addr,$),F9(Q,$)}}function cX(J,$){let Q=this.cache;if(Q[0]===$)return;J.uniform1ui(this.addr,$),Q[0]=$}function nX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y)J.uniform2ui(this.addr,$.x,$.y),Q[0]=$.x,Q[1]=$.y}else{if(k9(Q,$))return;J.uniform2uiv(this.addr,$),F9(Q,$)}}function sX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z)J.uniform3ui(this.addr,$.x,$.y,$.z),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z}else{if(k9(Q,$))return;J.uniform3uiv(this.addr,$),F9(Q,$)}}function iX(J,$){let Q=this.cache;if($.x!==void 0){if(Q[0]!==$.x||Q[1]!==$.y||Q[2]!==$.z||Q[3]!==$.w)J.uniform4ui(this.addr,$.x,$.y,$.z,$.w),Q[0]=$.x,Q[1]=$.y,Q[2]=$.z,Q[3]=$.w}else{if(k9(Q,$))return;J.uniform4uiv(this.addr,$),F9(Q,$)}}function oX(J,$,Q){let Z=this.cache,W=Q.allocateTextureUnit();if(Z[0]!==W)J.uniform1i(this.addr,W),Z[0]=W;let K;if(this.type===J.SAMPLER_2D_SHADOW)ZW.compareFunction=V$,K=ZW;else K=VW;Q.setTexture2D($||K,W)}function aX(J,$,Q){let Z=this.cache,W=Q.allocateTextureUnit();if(Z[0]!==W)J.uniform1i(this.addr,W),Z[0]=W;Q.setTexture3D($||BW,W)}function rX(J,$,Q){let Z=this.cache,W=Q.allocateTextureUnit();if(Z[0]!==W)J.uniform1i(this.addr,W),Z[0]=W;Q.setTextureCube($||wW,W)}function tX(J,$,Q){let Z=this.cache,W=Q.allocateTextureUnit();if(Z[0]!==W)J.uniform1i(this.addr,W),Z[0]=W;Q.setTexture2DArray($||zW,W)}function eX(J){switch(J){case 5126:return bX;case 35664:return fX;case 35665:return vX;case 35666:return hX;case 35674:return xX;case 35675:return gX;case 35676:return pX;case 5124:case 35670:return lX;case 35667:case 35671:return dX;case 35668:case 35672:return mX;case 35669:case 35673:return uX;case 5125:return cX;case 36294:return nX;case 36295:return sX;case 36296:return iX;case 35678:case 36198:case 36298:case 36306:case 35682:return oX;case 35679:case 36299:case 36307:return aX;case 35680:case 36300:case 36308:case 36293:return rX;case 36289:case 36303:case 36311:case 36292:return tX}}function JN(J,$){J.uniform1fv(this.addr,$)}function $N(J,$){let Q=I8($,this.size,2);J.uniform2fv(this.addr,Q)}function QN(J,$){let Q=I8($,this.size,3);J.uniform3fv(this.addr,Q)}function ZN(J,$){let Q=I8($,this.size,4);J.uniform4fv(this.addr,Q)}function WN(J,$){let Q=I8($,this.size,4);J.uniformMatrix2fv(this.addr,!1,Q)}function KN(J,$){let Q=I8($,this.size,9);J.uniformMatrix3fv(this.addr,!1,Q)}function YN(J,$){let Q=I8($,this.size,16);J.uniformMatrix4fv(this.addr,!1,Q)}function HN(J,$){J.uniform1iv(this.addr,$)}function XN(J,$){J.uniform2iv(this.addr,$)}function NN(J,$){J.uniform3iv(this.addr,$)}function UN(J,$){J.uniform4iv(this.addr,$)}function GN(J,$){J.uniform1uiv(this.addr,$)}function EN(J,$){J.uniform2uiv(this.addr,$)}function DN(J,$){J.uniform3uiv(this.addr,$)}function qN(J,$){J.uniform4uiv(this.addr,$)}function ON(J,$,Q){let Z=this.cache,W=$.length,K=K7(Q,W);if(!k9(Z,K))J.uniform1iv(this.addr,K),F9(Z,K);for(let H=0;H!==W;++H)Q.setTexture2D($[H]||VW,K[H])}function RN(J,$,Q){let Z=this.cache,W=$.length,K=K7(Q,W);if(!k9(Z,K))J.uniform1iv(this.addr,K),F9(Z,K);for(let H=0;H!==W;++H)Q.setTexture3D($[H]||BW,K[H])}function kN(J,$,Q){let Z=this.cache,W=$.length,K=K7(Q,W);if(!k9(Z,K))J.uniform1iv(this.addr,K),F9(Z,K);for(let H=0;H!==W;++H)Q.setTextureCube($[H]||wW,K[H])}function FN(J,$,Q){let Z=this.cache,W=$.length,K=K7(Q,W);if(!k9(Z,K))J.uniform1iv(this.addr,K),F9(Z,K);for(let H=0;H!==W;++H)Q.setTexture2DArray($[H]||zW,K[H])}function MN(J){switch(J){case 5126:return JN;case 35664:return $N;case 35665:return QN;case 35666:return ZN;case 35674:return WN;case 35675:return KN;case 35676:return YN;case 5124:case 35670:return HN;case 35667:case 35671:return XN;case 35668:case 35672:return NN;case 35669:case 35673:return UN;case 5125:return GN;case 36294:return EN;case 36295:return DN;case 36296:return qN;case 35678:case 36198:case 36298:case 36306:case 35682:return ON;case 35679:case 36299:case 36307:return RN;case 35680:case 36300:case 36308:case 36293:return kN;case 36289:case 36303:case 36311:case 36292:return FN}}class CW{constructor(J,$,Q){this.id=J,this.addr=Q,this.cache=[],this.type=$.type,this.setValue=eX($.type)}}class _W{constructor(J,$,Q){this.id=J,this.addr=Q,this.cache=[],this.type=$.type,this.size=$.size,this.setValue=MN($.type)}}class IW{constructor(J){this.id=J,this.seq=[],this.map={}}setValue(J,$,Q){let Z=this.seq;for(let W=0,K=Z.length;W!==K;++W){let H=Z[W];H.setValue(J,$[H.id],Q)}}}var e$=/(\w+)(\])?(\[|\.)?/g;function NW(J,$){J.seq.push($),J.map[$.id]=$}function LN(J,$,Q){let Z=J.name,W=Z.length;e$.lastIndex=0;while(!0){let K=e$.exec(Z),H=e$.lastIndex,Y=K[1],X=K[2]==="]",N=K[3];if(X)Y=Y|0;if(N===void 0||N==="["&&H+2===W){NW(Q,N===void 0?new CW(Y,J,$):new _W(Y,J,$));break}else{let G=Q.map[Y];if(G===void 0)G=new IW(Y),NW(Q,G);Q=G}}}class e8{constructor(J,$){this.seq=[],this.map={};let Q=J.getProgramParameter($,J.ACTIVE_UNIFORMS);for(let Z=0;Z<Q;++Z){let W=J.getActiveUniform($,Z),K=J.getUniformLocation($,W.name);LN(W,K,this)}}setValue(J,$,Q,Z){let W=this.map[$];if(W!==void 0)W.setValue(J,Q,Z)}setOptional(J,$,Q){let Z=$[Q];if(Z!==void 0)this.setValue(J,Q,Z)}static upload(J,$,Q,Z){for(let W=0,K=$.length;W!==K;++W){let H=$[W],Y=Q[H.id];if(Y.needsUpdate!==!1)H.setValue(J,Y.value,Z)}}static seqWithValue(J,$){let Q=[];for(let Z=0,W=J.length;Z!==W;++Z){let K=J[Z];if(K.id in $)Q.push(K)}return Q}}function UW(J,$,Q){let Z=J.createShader($);return J.shaderSource(Z,Q),J.compileShader(Z),Z}var VN=37297,zN=0;function BN(J,$){let Q=J.split(`
`),Z=[],W=Math.max($-6,0),K=Math.min($+6,Q.length);for(let H=W;H<K;H++){let Y=H+1;Z.push(`${Y===$?">":" "} ${Y}: ${Q[H]}`)}return Z.join(`
`)}var GW=new p0;function wN(J){s0._getMatrix(GW,s0.workingColorSpace,J);let $=`mat3( ${GW.elements.map((Q)=>Q.toFixed(4))} )`;switch(s0.getTransfer(J)){case L$:return[$,"LinearTransferOETF"];case K9:return[$,"sRGBTransferOETF"];default:return console.warn("THREE.WebGLProgram: Unsupported color space: ",J),[$,"LinearTransferOETF"]}}function EW(J,$,Q){let Z=J.getShaderParameter($,J.COMPILE_STATUS),K=(J.getShaderInfoLog($)||"").trim();if(Z&&K==="")return"";let H=/ERROR: 0:(\d+)/.exec(K);if(H){let Y=parseInt(H[1]);return Q.toUpperCase()+`

`+K+`

`+BN(J.getShaderSource($),Y)}else return K}function CN(J,$){let Q=wN($);return[`vec4 ${J}( vec4 value ) {`,`	return ${Q[1]}( vec4( value.rgb * ${Q[0]}, value.a ) );`,"}"].join(`
`)}function _N(J,$){let Q;switch($){case DZ:Q="Linear";break;case qZ:Q="Reinhard";break;case OZ:Q="Cineon";break;case RZ:Q="ACESFilmic";break;case FZ:Q="AgX";break;case MZ:Q="Neutral";break;case kZ:Q="Custom";break;default:console.warn("THREE.WebGLProgram: Unsupported toneMapping:",$),Q="Linear"}return"vec3 "+J+"( vec3 color ) { return "+Q+"ToneMapping( color ); }"}var W7=new v;function IN(){s0.getLuminanceCoefficients(W7);let J=W7.x.toFixed(4),$=W7.y.toFixed(4),Q=W7.z.toFixed(4);return["float luminance( const in vec3 rgb ) {",`	const vec3 weights = vec3( ${J}, ${$}, ${Q} );`,"\treturn dot( weights, rgb );","}"].join(`
`)}function PN(J){return[J.extensionClipCullDistance?"#extension GL_ANGLE_clip_cull_distance : require":"",J.extensionMultiDraw?"#extension GL_ANGLE_multi_draw : require":""].filter(t8).join(`
`)}function AN(J){let $=[];for(let Q in J){let Z=J[Q];if(Z===!1)continue;$.push("#define "+Q+" "+Z)}return $.join(`
`)}function TN(J,$){let Q={},Z=J.getProgramParameter($,J.ACTIVE_ATTRIBUTES);for(let W=0;W<Z;W++){let K=J.getActiveAttrib($,W),H=K.name,Y=1;if(K.type===J.FLOAT_MAT2)Y=2;if(K.type===J.FLOAT_MAT3)Y=3;if(K.type===J.FLOAT_MAT4)Y=4;Q[H]={type:K.type,location:J.getAttribLocation($,H),locationSize:Y}}return Q}function t8(J){return J!==""}function DW(J,$){let Q=$.numSpotLightShadows+$.numSpotLightMaps-$.numSpotLightShadowsWithMaps;return J.replace(/NUM_DIR_LIGHTS/g,$.numDirLights).replace(/NUM_SPOT_LIGHTS/g,$.numSpotLights).replace(/NUM_SPOT_LIGHT_MAPS/g,$.numSpotLightMaps).replace(/NUM_SPOT_LIGHT_COORDS/g,Q).replace(/NUM_RECT_AREA_LIGHTS/g,$.numRectAreaLights).replace(/NUM_POINT_LIGHTS/g,$.numPointLights).replace(/NUM_HEMI_LIGHTS/g,$.numHemiLights).replace(/NUM_DIR_LIGHT_SHADOWS/g,$.numDirLightShadows).replace(/NUM_SPOT_LIGHT_SHADOWS_WITH_MAPS/g,$.numSpotLightShadowsWithMaps).replace(/NUM_SPOT_LIGHT_SHADOWS/g,$.numSpotLightShadows).replace(/NUM_POINT_LIGHT_SHADOWS/g,$.numPointLightShadows)}function qW(J,$){return J.replace(/NUM_CLIPPING_PLANES/g,$.numClippingPlanes).replace(/UNION_CLIPPING_PLANES/g,$.numClippingPlanes-$.numClipIntersection)}var SN=/^[ \t]*#include +<([\w\d./]+)>/gm;function $Q(J){return J.replace(SN,yN)}var jN=new Map;function yN(J,$){let Q=d0[$];if(Q===void 0){let Z=jN.get($);if(Z!==void 0)Q=d0[Z],console.warn('THREE.WebGLRenderer: Shader chunk "%s" has been deprecated. Use "%s" instead.',$,Z);else throw Error("Can not resolve #include <"+$+">")}return $Q(Q)}var bN=/#pragma unroll_loop_start\s+for\s*\(\s*int\s+i\s*=\s*(\d+)\s*;\s*i\s*<\s*(\d+)\s*;\s*i\s*\+\+\s*\)\s*{([\s\S]+?)}\s+#pragma unroll_loop_end/g;function OW(J){return J.replace(bN,fN)}function fN(J,$,Q,Z){let W="";for(let K=parseInt($);K<parseInt(Q);K++)W+=Z.replace(/\[\s*i\s*\]/g,"[ "+K+" ]").replace(/UNROLLED_LOOP_INDEX/g,K);return W}function RW(J){let $=`precision ${J.precision} float;
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
#define LOW_PRECISION`;return $}function vN(J){let $="SHADOWMAP_TYPE_BASIC";if(J.shadowMapType===v7)$="SHADOWMAP_TYPE_PCF";else if(J.shadowMapType===mQ)$="SHADOWMAP_TYPE_PCF_SOFT";else if(J.shadowMapType===QJ)$="SHADOWMAP_TYPE_VSM";return $}function hN(J){let $="ENVMAP_TYPE_CUBE";if(J.envMap)switch(J.envMapMode){case O8:case hJ:$="ENVMAP_TYPE_CUBE";break;case g8:$="ENVMAP_TYPE_CUBE_UV";break}return $}function xN(J){let $="ENVMAP_MODE_REFLECTION";if(J.envMap)switch(J.envMapMode){case hJ:$="ENVMAP_MODE_REFRACTION";break}return $}function gN(J){let $="ENVMAP_BLENDING_NONE";if(J.envMap)switch(J.combine){case UZ:$="ENVMAP_BLENDING_MULTIPLY";break;case GZ:$="ENVMAP_BLENDING_MIX";break;case EZ:$="ENVMAP_BLENDING_ADD";break}return $}function pN(J){let $=J.envMapCubeUVHeight;if($===null)return null;let Q=Math.log2($)-2,Z=1/$;return{texelWidth:1/(3*Math.max(Math.pow(2,Q),112)),texelHeight:Z,maxMip:Q}}function lN(J,$,Q,Z){let W=J.getContext(),K=Q.defines,H=Q.vertexShader,Y=Q.fragmentShader,X=vN(Q),N=hN(Q),U=xN(Q),G=gN(Q),D=pN(Q),R=PN(Q),M=AN(K),L=W.createProgram(),E,q,A=Q.glslVersion?"#version "+Q.glslVersion+`
`:"";if(Q.isRawShaderMaterial){if(E=["#define SHADER_TYPE "+Q.shaderType,"#define SHADER_NAME "+Q.shaderName,M].filter(t8).join(`
`),E.length>0)E+=`
`;if(q=["#define SHADER_TYPE "+Q.shaderType,"#define SHADER_NAME "+Q.shaderName,M].filter(t8).join(`
`),q.length>0)q+=`
`}else E=[RW(Q),"#define SHADER_TYPE "+Q.shaderType,"#define SHADER_NAME "+Q.shaderName,M,Q.extensionClipCullDistance?"#define USE_CLIP_DISTANCE":"",Q.batching?"#define USE_BATCHING":"",Q.batchingColor?"#define USE_BATCHING_COLOR":"",Q.instancing?"#define USE_INSTANCING":"",Q.instancingColor?"#define USE_INSTANCING_COLOR":"",Q.instancingMorph?"#define USE_INSTANCING_MORPH":"",Q.useFog&&Q.fog?"#define USE_FOG":"",Q.useFog&&Q.fogExp2?"#define FOG_EXP2":"",Q.map?"#define USE_MAP":"",Q.envMap?"#define USE_ENVMAP":"",Q.envMap?"#define "+U:"",Q.lightMap?"#define USE_LIGHTMAP":"",Q.aoMap?"#define USE_AOMAP":"",Q.bumpMap?"#define USE_BUMPMAP":"",Q.normalMap?"#define USE_NORMALMAP":"",Q.normalMapObjectSpace?"#define USE_NORMALMAP_OBJECTSPACE":"",Q.normalMapTangentSpace?"#define USE_NORMALMAP_TANGENTSPACE":"",Q.displacementMap?"#define USE_DISPLACEMENTMAP":"",Q.emissiveMap?"#define USE_EMISSIVEMAP":"",Q.anisotropy?"#define USE_ANISOTROPY":"",Q.anisotropyMap?"#define USE_ANISOTROPYMAP":"",Q.clearcoatMap?"#define USE_CLEARCOATMAP":"",Q.clearcoatRoughnessMap?"#define USE_CLEARCOAT_ROUGHNESSMAP":"",Q.clearcoatNormalMap?"#define USE_CLEARCOAT_NORMALMAP":"",Q.iridescenceMap?"#define USE_IRIDESCENCEMAP":"",Q.iridescenceThicknessMap?"#define USE_IRIDESCENCE_THICKNESSMAP":"",Q.specularMap?"#define USE_SPECULARMAP":"",Q.specularColorMap?"#define USE_SPECULAR_COLORMAP":"",Q.specularIntensityMap?"#define USE_SPECULAR_INTENSITYMAP":"",Q.roughnessMap?"#define USE_ROUGHNESSMAP":"",Q.metalnessMap?"#define USE_METALNESSMAP":"",Q.alphaMap?"#define USE_ALPHAMAP":"",Q.alphaHash?"#define USE_ALPHAHASH":"",Q.transmission?"#define USE_TRANSMISSION":"",Q.transmissionMap?"#define USE_TRANSMISSIONMAP":"",Q.thicknessMap?"#define USE_THICKNESSMAP":"",Q.sheenColorMap?"#define USE_SHEEN_COLORMAP":"",Q.sheenRoughnessMap?"#define USE_SHEEN_ROUGHNESSMAP":"",Q.mapUv?"#define MAP_UV "+Q.mapUv:"",Q.alphaMapUv?"#define ALPHAMAP_UV "+Q.alphaMapUv:"",Q.lightMapUv?"#define LIGHTMAP_UV "+Q.lightMapUv:"",Q.aoMapUv?"#define AOMAP_UV "+Q.aoMapUv:"",Q.emissiveMapUv?"#define EMISSIVEMAP_UV "+Q.emissiveMapUv:"",Q.bumpMapUv?"#define BUMPMAP_UV "+Q.bumpMapUv:"",Q.normalMapUv?"#define NORMALMAP_UV "+Q.normalMapUv:"",Q.displacementMapUv?"#define DISPLACEMENTMAP_UV "+Q.displacementMapUv:"",Q.metalnessMapUv?"#define METALNESSMAP_UV "+Q.metalnessMapUv:"",Q.roughnessMapUv?"#define ROUGHNESSMAP_UV "+Q.roughnessMapUv:"",Q.anisotropyMapUv?"#define ANISOTROPYMAP_UV "+Q.anisotropyMapUv:"",Q.clearcoatMapUv?"#define CLEARCOATMAP_UV "+Q.clearcoatMapUv:"",Q.clearcoatNormalMapUv?"#define CLEARCOAT_NORMALMAP_UV "+Q.clearcoatNormalMapUv:"",Q.clearcoatRoughnessMapUv?"#define CLEARCOAT_ROUGHNESSMAP_UV "+Q.clearcoatRoughnessMapUv:"",Q.iridescenceMapUv?"#define IRIDESCENCEMAP_UV "+Q.iridescenceMapUv:"",Q.iridescenceThicknessMapUv?"#define IRIDESCENCE_THICKNESSMAP_UV "+Q.iridescenceThicknessMapUv:"",Q.sheenColorMapUv?"#define SHEEN_COLORMAP_UV "+Q.sheenColorMapUv:"",Q.sheenRoughnessMapUv?"#define SHEEN_ROUGHNESSMAP_UV "+Q.sheenRoughnessMapUv:"",Q.specularMapUv?"#define SPECULARMAP_UV "+Q.specularMapUv:"",Q.specularColorMapUv?"#define SPECULAR_COLORMAP_UV "+Q.specularColorMapUv:"",Q.specularIntensityMapUv?"#define SPECULAR_INTENSITYMAP_UV "+Q.specularIntensityMapUv:"",Q.transmissionMapUv?"#define TRANSMISSIONMAP_UV "+Q.transmissionMapUv:"",Q.thicknessMapUv?"#define THICKNESSMAP_UV "+Q.thicknessMapUv:"",Q.vertexTangents&&Q.flatShading===!1?"#define USE_TANGENT":"",Q.vertexColors?"#define USE_COLOR":"",Q.vertexAlphas?"#define USE_COLOR_ALPHA":"",Q.vertexUv1s?"#define USE_UV1":"",Q.vertexUv2s?"#define USE_UV2":"",Q.vertexUv3s?"#define USE_UV3":"",Q.pointsUvs?"#define USE_POINTS_UV":"",Q.flatShading?"#define FLAT_SHADED":"",Q.skinning?"#define USE_SKINNING":"",Q.morphTargets?"#define USE_MORPHTARGETS":"",Q.morphNormals&&Q.flatShading===!1?"#define USE_MORPHNORMALS":"",Q.morphColors?"#define USE_MORPHCOLORS":"",Q.morphTargetsCount>0?"#define MORPHTARGETS_TEXTURE_STRIDE "+Q.morphTextureStride:"",Q.morphTargetsCount>0?"#define MORPHTARGETS_COUNT "+Q.morphTargetsCount:"",Q.doubleSided?"#define DOUBLE_SIDED":"",Q.flipSided?"#define FLIP_SIDED":"",Q.shadowMapEnabled?"#define USE_SHADOWMAP":"",Q.shadowMapEnabled?"#define "+X:"",Q.sizeAttenuation?"#define USE_SIZEATTENUATION":"",Q.numLightProbes>0?"#define USE_LIGHT_PROBES":"",Q.logarithmicDepthBuffer?"#define USE_LOGARITHMIC_DEPTH_BUFFER":"",Q.reversedDepthBuffer?"#define USE_REVERSED_DEPTH_BUFFER":"","uniform mat4 modelMatrix;","uniform mat4 modelViewMatrix;","uniform mat4 projectionMatrix;","uniform mat4 viewMatrix;","uniform mat3 normalMatrix;","uniform vec3 cameraPosition;","uniform bool isOrthographic;","#ifdef USE_INSTANCING","\tattribute mat4 instanceMatrix;","#endif","#ifdef USE_INSTANCING_COLOR","\tattribute vec3 instanceColor;","#endif","#ifdef USE_INSTANCING_MORPH","\tuniform sampler2D morphTexture;","#endif","attribute vec3 position;","attribute vec3 normal;","attribute vec2 uv;","#ifdef USE_UV1","\tattribute vec2 uv1;","#endif","#ifdef USE_UV2","\tattribute vec2 uv2;","#endif","#ifdef USE_UV3","\tattribute vec2 uv3;","#endif","#ifdef USE_TANGENT","\tattribute vec4 tangent;","#endif","#if defined( USE_COLOR_ALPHA )","\tattribute vec4 color;","#elif defined( USE_COLOR )","\tattribute vec3 color;","#endif","#ifdef USE_SKINNING","\tattribute vec4 skinIndex;","\tattribute vec4 skinWeight;","#endif",`
`].filter(t8).join(`
`),q=[RW(Q),"#define SHADER_TYPE "+Q.shaderType,"#define SHADER_NAME "+Q.shaderName,M,Q.useFog&&Q.fog?"#define USE_FOG":"",Q.useFog&&Q.fogExp2?"#define FOG_EXP2":"",Q.alphaToCoverage?"#define ALPHA_TO_COVERAGE":"",Q.map?"#define USE_MAP":"",Q.matcap?"#define USE_MATCAP":"",Q.envMap?"#define USE_ENVMAP":"",Q.envMap?"#define "+N:"",Q.envMap?"#define "+U:"",Q.envMap?"#define "+G:"",D?"#define CUBEUV_TEXEL_WIDTH "+D.texelWidth:"",D?"#define CUBEUV_TEXEL_HEIGHT "+D.texelHeight:"",D?"#define CUBEUV_MAX_MIP "+D.maxMip+".0":"",Q.lightMap?"#define USE_LIGHTMAP":"",Q.aoMap?"#define USE_AOMAP":"",Q.bumpMap?"#define USE_BUMPMAP":"",Q.normalMap?"#define USE_NORMALMAP":"",Q.normalMapObjectSpace?"#define USE_NORMALMAP_OBJECTSPACE":"",Q.normalMapTangentSpace?"#define USE_NORMALMAP_TANGENTSPACE":"",Q.emissiveMap?"#define USE_EMISSIVEMAP":"",Q.anisotropy?"#define USE_ANISOTROPY":"",Q.anisotropyMap?"#define USE_ANISOTROPYMAP":"",Q.clearcoat?"#define USE_CLEARCOAT":"",Q.clearcoatMap?"#define USE_CLEARCOATMAP":"",Q.clearcoatRoughnessMap?"#define USE_CLEARCOAT_ROUGHNESSMAP":"",Q.clearcoatNormalMap?"#define USE_CLEARCOAT_NORMALMAP":"",Q.dispersion?"#define USE_DISPERSION":"",Q.iridescence?"#define USE_IRIDESCENCE":"",Q.iridescenceMap?"#define USE_IRIDESCENCEMAP":"",Q.iridescenceThicknessMap?"#define USE_IRIDESCENCE_THICKNESSMAP":"",Q.specularMap?"#define USE_SPECULARMAP":"",Q.specularColorMap?"#define USE_SPECULAR_COLORMAP":"",Q.specularIntensityMap?"#define USE_SPECULAR_INTENSITYMAP":"",Q.roughnessMap?"#define USE_ROUGHNESSMAP":"",Q.metalnessMap?"#define USE_METALNESSMAP":"",Q.alphaMap?"#define USE_ALPHAMAP":"",Q.alphaTest?"#define USE_ALPHATEST":"",Q.alphaHash?"#define USE_ALPHAHASH":"",Q.sheen?"#define USE_SHEEN":"",Q.sheenColorMap?"#define USE_SHEEN_COLORMAP":"",Q.sheenRoughnessMap?"#define USE_SHEEN_ROUGHNESSMAP":"",Q.transmission?"#define USE_TRANSMISSION":"",Q.transmissionMap?"#define USE_TRANSMISSIONMAP":"",Q.thicknessMap?"#define USE_THICKNESSMAP":"",Q.vertexTangents&&Q.flatShading===!1?"#define USE_TANGENT":"",Q.vertexColors||Q.instancingColor||Q.batchingColor?"#define USE_COLOR":"",Q.vertexAlphas?"#define USE_COLOR_ALPHA":"",Q.vertexUv1s?"#define USE_UV1":"",Q.vertexUv2s?"#define USE_UV2":"",Q.vertexUv3s?"#define USE_UV3":"",Q.pointsUvs?"#define USE_POINTS_UV":"",Q.gradientMap?"#define USE_GRADIENTMAP":"",Q.flatShading?"#define FLAT_SHADED":"",Q.doubleSided?"#define DOUBLE_SIDED":"",Q.flipSided?"#define FLIP_SIDED":"",Q.shadowMapEnabled?"#define USE_SHADOWMAP":"",Q.shadowMapEnabled?"#define "+X:"",Q.premultipliedAlpha?"#define PREMULTIPLIED_ALPHA":"",Q.numLightProbes>0?"#define USE_LIGHT_PROBES":"",Q.decodeVideoTexture?"#define DECODE_VIDEO_TEXTURE":"",Q.decodeVideoTextureEmissive?"#define DECODE_VIDEO_TEXTURE_EMISSIVE":"",Q.logarithmicDepthBuffer?"#define USE_LOGARITHMIC_DEPTH_BUFFER":"",Q.reversedDepthBuffer?"#define USE_REVERSED_DEPTH_BUFFER":"","uniform mat4 viewMatrix;","uniform vec3 cameraPosition;","uniform bool isOrthographic;",Q.toneMapping!==RJ?"#define TONE_MAPPING":"",Q.toneMapping!==RJ?d0.tonemapping_pars_fragment:"",Q.toneMapping!==RJ?_N("toneMapping",Q.toneMapping):"",Q.dithering?"#define DITHERING":"",Q.opaque?"#define OPAQUE":"",d0.colorspace_pars_fragment,CN("linearToOutputTexel",Q.outputColorSpace),IN(),Q.useDepthPacking?"#define DEPTH_PACKING "+Q.depthPacking:"",`
`].filter(t8).join(`
`);if(H=$Q(H),H=DW(H,Q),H=qW(H,Q),Y=$Q(Y),Y=DW(Y,Q),Y=qW(Y,Q),H=OW(H),Y=OW(Y),Q.isRawShaderMaterial!==!0)A=`#version 300 es
`,E=[R,"#define attribute in","#define varying out","#define texture2D texture"].join(`
`)+`
`+E,q=["#define varying in",Q.glslVersion===z$?"":"layout(location = 0) out highp vec4 pc_fragColor;",Q.glslVersion===z$?"":"#define gl_FragColor pc_fragColor","#define gl_FragDepthEXT gl_FragDepth","#define texture2D texture","#define textureCube texture","#define texture2DProj textureProj","#define texture2DLodEXT textureLod","#define texture2DProjLodEXT textureProjLod","#define textureCubeLodEXT textureLod","#define texture2DGradEXT textureGrad","#define texture2DProjGradEXT textureProjGrad","#define textureCubeGradEXT textureGrad"].join(`
`)+`
`+q;let V=A+E+H,I=A+q+Y,f=UW(W,W.VERTEX_SHADER,V),P=UW(W,W.FRAGMENT_SHADER,I);if(W.attachShader(L,f),W.attachShader(L,P),Q.index0AttributeName!==void 0)W.bindAttribLocation(L,0,Q.index0AttributeName);else if(Q.morphTargets===!0)W.bindAttribLocation(L,0,"position");W.linkProgram(L);function S(T){if(J.debug.checkShaderErrors){let n=W.getProgramInfoLog(L)||"",s=W.getShaderInfoLog(f)||"",d=W.getShaderInfoLog(P)||"",Z0=n.trim(),m=s.trim(),W0=d.trim(),u=!0,N0=!0;if(W.getProgramParameter(L,W.LINK_STATUS)===!1)if(u=!1,typeof J.debug.onShaderError==="function")J.debug.onShaderError(W,L,f,P);else{let q0=EW(W,f,"vertex"),_0=EW(W,P,"fragment");console.error("THREE.WebGLProgram: Shader Error "+W.getError()+" - VALIDATE_STATUS "+W.getProgramParameter(L,W.VALIDATE_STATUS)+`

Material Name: `+T.name+`
Material Type: `+T.type+`

Program Info Log: `+Z0+`
`+q0+`
`+_0)}else if(Z0!=="")console.warn("THREE.WebGLProgram: Program Info Log:",Z0);else if(m===""||W0==="")N0=!1;if(N0)T.diagnostics={runnable:u,programLog:Z0,vertexShader:{log:m,prefix:E},fragmentShader:{log:W0,prefix:q}}}W.deleteShader(f),W.deleteShader(P),g=new e8(W,L),z=TN(W,L)}let g;this.getUniforms=function(){if(g===void 0)S(this);return g};let z;this.getAttributes=function(){if(z===void 0)S(this);return z};let F=Q.rendererExtensionParallelShaderCompile===!1;return this.isReady=function(){if(F===!1)F=W.getProgramParameter(L,VN);return F},this.destroy=function(){Z.releaseStatesOfProgram(this),W.deleteProgram(L),this.program=void 0},this.type=Q.shaderType,this.name=Q.shaderName,this.id=zN++,this.cacheKey=$,this.usedTimes=1,this.program=L,this.vertexShader=f,this.fragmentShader=P,this}var dN=0;class PW{constructor(){this.shaderCache=new Map,this.materialCache=new Map}update(J){let{vertexShader:$,fragmentShader:Q}=J,Z=this._getShaderStage($),W=this._getShaderStage(Q),K=this._getShaderCacheForMaterial(J);if(K.has(Z)===!1)K.add(Z),Z.usedTimes++;if(K.has(W)===!1)K.add(W),W.usedTimes++;return this}remove(J){let $=this.materialCache.get(J);for(let Q of $)if(Q.usedTimes--,Q.usedTimes===0)this.shaderCache.delete(Q.code);return this.materialCache.delete(J),this}getVertexShaderID(J){return this._getShaderStage(J.vertexShader).id}getFragmentShaderID(J){return this._getShaderStage(J.fragmentShader).id}dispose(){this.shaderCache.clear(),this.materialCache.clear()}_getShaderCacheForMaterial(J){let $=this.materialCache,Q=$.get(J);if(Q===void 0)Q=new Set,$.set(J,Q);return Q}_getShaderStage(J){let $=this.shaderCache,Q=$.get(J);if(Q===void 0)Q=new AW(J),$.set(J,Q);return Q}}class AW{constructor(J){this.id=dN++,this.code=J,this.usedTimes=0}}function mN(J,$,Q,Z,W,K,H){let Y=new n8,X=new PW,N=new Set,U=[],G=W.logarithmicDepthBuffer,D=W.vertexTextures,R=W.precision,M={MeshDepthMaterial:"depth",MeshDistanceMaterial:"distanceRGBA",MeshNormalMaterial:"normal",MeshBasicMaterial:"basic",MeshLambertMaterial:"lambert",MeshPhongMaterial:"phong",MeshToonMaterial:"toon",MeshStandardMaterial:"physical",MeshPhysicalMaterial:"physical",MeshMatcapMaterial:"matcap",LineBasicMaterial:"basic",LineDashedMaterial:"dashed",PointsMaterial:"points",ShadowMaterial:"shadow",SpriteMaterial:"sprite"};function L(z){if(N.add(z),z===0)return"uv";return`uv${z}`}function E(z,F,T,n,s){let d=n.fog,Z0=s.geometry,m=z.isMeshStandardMaterial?n.environment:null,W0=(z.isMeshStandardMaterial?Q:$).get(z.envMap||m),u=!!W0&&W0.mapping===g8?W0.image.height:null,N0=M[z.type];if(z.precision!==null){if(R=W.getMaxPrecision(z.precision),R!==z.precision)console.warn("THREE.WebGLProgram.getParameters:",z.precision,"not supported, using",R,"instead.")}let q0=Z0.morphAttributes.position||Z0.morphAttributes.normal||Z0.morphAttributes.color,_0=q0!==void 0?q0.length:0,g0=0;if(Z0.morphAttributes.position!==void 0)g0=1;if(Z0.morphAttributes.normal!==void 0)g0=2;if(Z0.morphAttributes.color!==void 0)g0=3;let a0,m0,o,H0;if(N0){let J9=YJ[N0];a0=J9.vertexShader,m0=J9.fragmentShader}else a0=z.vertexShader,m0=z.fragmentShader,X.update(z),o=X.getVertexShaderID(z),H0=X.getFragmentShaderID(z);let h=J.getRenderTarget(),t=J.state.buffers.depth.getReversed(),Q0=s.isInstancedMesh===!0,E0=s.isBatchedMesh===!0,l0=!!z.map,w=!!z.matcap,v0=!!W0,S0=!!z.aoMap,A0=!!z.lightMap,L0=!!z.bumpMap,i0=!!z.normalMap,w0=!!z.displacementMap,C0=!!z.emissiveMap,H9=!!z.metalnessMap,D9=!!z.roughnessMap,e0=z.anisotropy>0,B=z.clearcoat>0,O=z.dispersion>0,j=z.iridescence>0,x=z.sheen>0,e=z.transmission>0,c=e0&&!!z.anisotropyMap,R0=B&&!!z.clearcoatMap,X0=B&&!!z.clearcoatNormalMap,z0=B&&!!z.clearcoatRoughnessMap,T0=j&&!!z.iridescenceMap,a=j&&!!z.iridescenceThicknessMap,K0=x&&!!z.sheenColorMap,k0=x&&!!z.sheenRoughnessMap,F0=!!z.specularMap,U0=!!z.specularColorMap,I0=!!z.specularIntensityMap,_=e&&!!z.transmissionMap,G0=e&&!!z.thicknessMap,C=!!z.gradientMap,J0=!!z.alphaMap,r=z.alphaTest>0,i=!!z.alphaHash,M0=!!z.extensions,f0=RJ;if(z.toneMapped){if(h===null||h.isXRRenderTarget===!0)f0=J.toneMapping}let r0={shaderID:N0,shaderType:z.type,shaderName:z.name,vertexShader:a0,fragmentShader:m0,defines:z.defines,customVertexShaderID:o,customFragmentShaderID:H0,isRawShaderMaterial:z.isRawShaderMaterial===!0,glslVersion:z.glslVersion,precision:R,batching:E0,batchingColor:E0&&s._colorsTexture!==null,instancing:Q0,instancingColor:Q0&&s.instanceColor!==null,instancingMorph:Q0&&s.morphTexture!==null,supportsVertexTextures:D,outputColorSpace:h===null?J.outputColorSpace:h.isXRRenderTarget===!0?h.texture.colorSpace:u8,alphaToCoverage:!!z.alphaToCoverage,map:l0,matcap:w,envMap:v0,envMapMode:v0&&W0.mapping,envMapCubeUVHeight:u,aoMap:S0,lightMap:A0,bumpMap:L0,normalMap:i0,displacementMap:D&&w0,emissiveMap:C0,normalMapObjectSpace:i0&&z.normalMapType===bZ,normalMapTangentSpace:i0&&z.normalMapType===yZ,metalnessMap:H9,roughnessMap:D9,anisotropy:e0,anisotropyMap:c,clearcoat:B,clearcoatMap:R0,clearcoatNormalMap:X0,clearcoatRoughnessMap:z0,dispersion:O,iridescence:j,iridescenceMap:T0,iridescenceThicknessMap:a,sheen:x,sheenColorMap:K0,sheenRoughnessMap:k0,specularMap:F0,specularColorMap:U0,specularIntensityMap:I0,transmission:e,transmissionMap:_,thicknessMap:G0,gradientMap:C,opaque:z.transparent===!1&&z.blending===h8&&z.alphaToCoverage===!1,alphaMap:J0,alphaTest:r,alphaHash:i,combine:z.combine,mapUv:l0&&L(z.map.channel),aoMapUv:S0&&L(z.aoMap.channel),lightMapUv:A0&&L(z.lightMap.channel),bumpMapUv:L0&&L(z.bumpMap.channel),normalMapUv:i0&&L(z.normalMap.channel),displacementMapUv:w0&&L(z.displacementMap.channel),emissiveMapUv:C0&&L(z.emissiveMap.channel),metalnessMapUv:H9&&L(z.metalnessMap.channel),roughnessMapUv:D9&&L(z.roughnessMap.channel),anisotropyMapUv:c&&L(z.anisotropyMap.channel),clearcoatMapUv:R0&&L(z.clearcoatMap.channel),clearcoatNormalMapUv:X0&&L(z.clearcoatNormalMap.channel),clearcoatRoughnessMapUv:z0&&L(z.clearcoatRoughnessMap.channel),iridescenceMapUv:T0&&L(z.iridescenceMap.channel),iridescenceThicknessMapUv:a&&L(z.iridescenceThicknessMap.channel),sheenColorMapUv:K0&&L(z.sheenColorMap.channel),sheenRoughnessMapUv:k0&&L(z.sheenRoughnessMap.channel),specularMapUv:F0&&L(z.specularMap.channel),specularColorMapUv:U0&&L(z.specularColorMap.channel),specularIntensityMapUv:I0&&L(z.specularIntensityMap.channel),transmissionMapUv:_&&L(z.transmissionMap.channel),thicknessMapUv:G0&&L(z.thicknessMap.channel),alphaMapUv:J0&&L(z.alphaMap.channel),vertexTangents:!!Z0.attributes.tangent&&(i0||e0),vertexColors:z.vertexColors,vertexAlphas:z.vertexColors===!0&&!!Z0.attributes.color&&Z0.attributes.color.itemSize===4,pointsUvs:s.isPoints===!0&&!!Z0.attributes.uv&&(l0||J0),fog:!!d,useFog:z.fog===!0,fogExp2:!!d&&d.isFogExp2,flatShading:z.flatShading===!0&&z.wireframe===!1,sizeAttenuation:z.sizeAttenuation===!0,logarithmicDepthBuffer:G,reversedDepthBuffer:t,skinning:s.isSkinnedMesh===!0,morphTargets:Z0.morphAttributes.position!==void 0,morphNormals:Z0.morphAttributes.normal!==void 0,morphColors:Z0.morphAttributes.color!==void 0,morphTargetsCount:_0,morphTextureStride:g0,numDirLights:F.directional.length,numPointLights:F.point.length,numSpotLights:F.spot.length,numSpotLightMaps:F.spotLightMap.length,numRectAreaLights:F.rectArea.length,numHemiLights:F.hemi.length,numDirLightShadows:F.directionalShadowMap.length,numPointLightShadows:F.pointShadowMap.length,numSpotLightShadows:F.spotShadowMap.length,numSpotLightShadowsWithMaps:F.numSpotLightShadowsWithMaps,numLightProbes:F.numLightProbes,numClippingPlanes:H.numPlanes,numClipIntersection:H.numIntersection,dithering:z.dithering,shadowMapEnabled:J.shadowMap.enabled&&T.length>0,shadowMapType:J.shadowMap.type,toneMapping:f0,decodeVideoTexture:l0&&z.map.isVideoTexture===!0&&s0.getTransfer(z.map.colorSpace)===K9,decodeVideoTextureEmissive:C0&&z.emissiveMap.isVideoTexture===!0&&s0.getTransfer(z.emissiveMap.colorSpace)===K9,premultipliedAlpha:z.premultipliedAlpha,doubleSided:z.side===s9,flipSided:z.side===p9,useDepthPacking:z.depthPacking>=0,depthPacking:z.depthPacking||0,index0AttributeName:z.index0AttributeName,extensionClipCullDistance:M0&&z.extensions.clipCullDistance===!0&&Z.has("WEBGL_clip_cull_distance"),extensionMultiDraw:(M0&&z.extensions.multiDraw===!0||E0)&&Z.has("WEBGL_multi_draw"),rendererExtensionParallelShaderCompile:Z.has("KHR_parallel_shader_compile"),customProgramCacheKey:z.customProgramCacheKey()};return r0.vertexUv1s=N.has(1),r0.vertexUv2s=N.has(2),r0.vertexUv3s=N.has(3),N.clear(),r0}function q(z){let F=[];if(z.shaderID)F.push(z.shaderID);else F.push(z.customVertexShaderID),F.push(z.customFragmentShaderID);if(z.defines!==void 0)for(let T in z.defines)F.push(T),F.push(z.defines[T]);if(z.isRawShaderMaterial===!1)A(F,z),V(F,z),F.push(J.outputColorSpace);return F.push(z.customProgramCacheKey),F.join()}function A(z,F){z.push(F.precision),z.push(F.outputColorSpace),z.push(F.envMapMode),z.push(F.envMapCubeUVHeight),z.push(F.mapUv),z.push(F.alphaMapUv),z.push(F.lightMapUv),z.push(F.aoMapUv),z.push(F.bumpMapUv),z.push(F.normalMapUv),z.push(F.displacementMapUv),z.push(F.emissiveMapUv),z.push(F.metalnessMapUv),z.push(F.roughnessMapUv),z.push(F.anisotropyMapUv),z.push(F.clearcoatMapUv),z.push(F.clearcoatNormalMapUv),z.push(F.clearcoatRoughnessMapUv),z.push(F.iridescenceMapUv),z.push(F.iridescenceThicknessMapUv),z.push(F.sheenColorMapUv),z.push(F.sheenRoughnessMapUv),z.push(F.specularMapUv),z.push(F.specularColorMapUv),z.push(F.specularIntensityMapUv),z.push(F.transmissionMapUv),z.push(F.thicknessMapUv),z.push(F.combine),z.push(F.fogExp2),z.push(F.sizeAttenuation),z.push(F.morphTargetsCount),z.push(F.morphAttributeCount),z.push(F.numDirLights),z.push(F.numPointLights),z.push(F.numSpotLights),z.push(F.numSpotLightMaps),z.push(F.numHemiLights),z.push(F.numRectAreaLights),z.push(F.numDirLightShadows),z.push(F.numPointLightShadows),z.push(F.numSpotLightShadows),z.push(F.numSpotLightShadowsWithMaps),z.push(F.numLightProbes),z.push(F.shadowMapType),z.push(F.toneMapping),z.push(F.numClippingPlanes),z.push(F.numClipIntersection),z.push(F.depthPacking)}function V(z,F){if(Y.disableAll(),F.supportsVertexTextures)Y.enable(0);if(F.instancing)Y.enable(1);if(F.instancingColor)Y.enable(2);if(F.instancingMorph)Y.enable(3);if(F.matcap)Y.enable(4);if(F.envMap)Y.enable(5);if(F.normalMapObjectSpace)Y.enable(6);if(F.normalMapTangentSpace)Y.enable(7);if(F.clearcoat)Y.enable(8);if(F.iridescence)Y.enable(9);if(F.alphaTest)Y.enable(10);if(F.vertexColors)Y.enable(11);if(F.vertexAlphas)Y.enable(12);if(F.vertexUv1s)Y.enable(13);if(F.vertexUv2s)Y.enable(14);if(F.vertexUv3s)Y.enable(15);if(F.vertexTangents)Y.enable(16);if(F.anisotropy)Y.enable(17);if(F.alphaHash)Y.enable(18);if(F.batching)Y.enable(19);if(F.dispersion)Y.enable(20);if(F.batchingColor)Y.enable(21);if(F.gradientMap)Y.enable(22);if(z.push(Y.mask),Y.disableAll(),F.fog)Y.enable(0);if(F.useFog)Y.enable(1);if(F.flatShading)Y.enable(2);if(F.logarithmicDepthBuffer)Y.enable(3);if(F.reversedDepthBuffer)Y.enable(4);if(F.skinning)Y.enable(5);if(F.morphTargets)Y.enable(6);if(F.morphNormals)Y.enable(7);if(F.morphColors)Y.enable(8);if(F.premultipliedAlpha)Y.enable(9);if(F.shadowMapEnabled)Y.enable(10);if(F.doubleSided)Y.enable(11);if(F.flipSided)Y.enable(12);if(F.useDepthPacking)Y.enable(13);if(F.dithering)Y.enable(14);if(F.transmission)Y.enable(15);if(F.sheen)Y.enable(16);if(F.opaque)Y.enable(17);if(F.pointsUvs)Y.enable(18);if(F.decodeVideoTexture)Y.enable(19);if(F.decodeVideoTextureEmissive)Y.enable(20);if(F.alphaToCoverage)Y.enable(21);z.push(Y.mask)}function I(z){let F=M[z.type],T;if(F){let n=YJ[F];T=nZ.clone(n.uniforms)}else T=z.uniforms;return T}function f(z,F){let T;for(let n=0,s=U.length;n<s;n++){let d=U[n];if(d.cacheKey===F){T=d,++T.usedTimes;break}}if(T===void 0)T=new lN(J,F,z,K),U.push(T);return T}function P(z){if(--z.usedTimes===0){let F=U.indexOf(z);U[F]=U[U.length-1],U.pop(),z.destroy()}}function S(z){X.remove(z)}function g(){X.dispose()}return{getParameters:E,getProgramCacheKey:q,getUniforms:I,acquireProgram:f,releaseProgram:P,releaseShaderCache:S,programs:U,dispose:g}}function uN(){let J=new WeakMap;function $(H){return J.has(H)}function Q(H){let Y=J.get(H);if(Y===void 0)Y={},J.set(H,Y);return Y}function Z(H){J.delete(H)}function W(H,Y,X){J.get(H)[Y]=X}function K(){J=new WeakMap}return{has:$,get:Q,remove:Z,update:W,dispose:K}}function cN(J,$){if(J.groupOrder!==$.groupOrder)return J.groupOrder-$.groupOrder;else if(J.renderOrder!==$.renderOrder)return J.renderOrder-$.renderOrder;else if(J.material.id!==$.material.id)return J.material.id-$.material.id;else if(J.z!==$.z)return J.z-$.z;else return J.id-$.id}function kW(J,$){if(J.groupOrder!==$.groupOrder)return J.groupOrder-$.groupOrder;else if(J.renderOrder!==$.renderOrder)return J.renderOrder-$.renderOrder;else if(J.z!==$.z)return $.z-J.z;else return J.id-$.id}function FW(){let J=[],$=0,Q=[],Z=[],W=[];function K(){$=0,Q.length=0,Z.length=0,W.length=0}function H(G,D,R,M,L,E){let q=J[$];if(q===void 0)q={id:G.id,object:G,geometry:D,material:R,groupOrder:M,renderOrder:G.renderOrder,z:L,group:E},J[$]=q;else q.id=G.id,q.object=G,q.geometry=D,q.material=R,q.groupOrder=M,q.renderOrder=G.renderOrder,q.z=L,q.group=E;return $++,q}function Y(G,D,R,M,L,E){let q=H(G,D,R,M,L,E);if(R.transmission>0)Z.push(q);else if(R.transparent===!0)W.push(q);else Q.push(q)}function X(G,D,R,M,L,E){let q=H(G,D,R,M,L,E);if(R.transmission>0)Z.unshift(q);else if(R.transparent===!0)W.unshift(q);else Q.unshift(q)}function N(G,D){if(Q.length>1)Q.sort(G||cN);if(Z.length>1)Z.sort(D||kW);if(W.length>1)W.sort(D||kW)}function U(){for(let G=$,D=J.length;G<D;G++){let R=J[G];if(R.id===null)break;R.id=null,R.object=null,R.geometry=null,R.material=null,R.group=null}}return{opaque:Q,transmissive:Z,transparent:W,init:K,push:Y,unshift:X,finish:U,sort:N}}function nN(){let J=new WeakMap;function $(Z,W){let K=J.get(Z),H;if(K===void 0)H=new FW,J.set(Z,[H]);else if(W>=K.length)H=new FW,K.push(H);else H=K[W];return H}function Q(){J=new WeakMap}return{get:$,dispose:Q}}function sN(){let J={};return{get:function($){if(J[$.id]!==void 0)return J[$.id];let Q;switch($.type){case"DirectionalLight":Q={direction:new v,color:new n0};break;case"SpotLight":Q={position:new v,direction:new v,color:new n0,distance:0,coneCos:0,penumbraCos:0,decay:0};break;case"PointLight":Q={position:new v,color:new n0,distance:0,decay:0};break;case"HemisphereLight":Q={direction:new v,skyColor:new n0,groundColor:new n0};break;case"RectAreaLight":Q={color:new n0,position:new v,halfWidth:new v,halfHeight:new v};break}return J[$.id]=Q,Q}}}function iN(){let J={};return{get:function($){if(J[$.id]!==void 0)return J[$.id];let Q;switch($.type){case"DirectionalLight":Q={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new x0};break;case"SpotLight":Q={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new x0};break;case"PointLight":Q={shadowIntensity:1,shadowBias:0,shadowNormalBias:0,shadowRadius:1,shadowMapSize:new x0,shadowCameraNear:1,shadowCameraFar:1000};break}return J[$.id]=Q,Q}}}var oN=0;function aN(J,$){return($.castShadow?2:0)-(J.castShadow?2:0)+($.map?1:0)-(J.map?1:0)}function rN(J){let $=new sN,Q=iN(),Z={version:0,hash:{directionalLength:-1,pointLength:-1,spotLength:-1,rectAreaLength:-1,hemiLength:-1,numDirectionalShadows:-1,numPointShadows:-1,numSpotShadows:-1,numSpotMaps:-1,numLightProbes:-1},ambient:[0,0,0],probe:[],directional:[],directionalShadow:[],directionalShadowMap:[],directionalShadowMatrix:[],spot:[],spotLightMap:[],spotShadow:[],spotShadowMap:[],spotLightMatrix:[],rectArea:[],rectAreaLTC1:null,rectAreaLTC2:null,point:[],pointShadow:[],pointShadowMap:[],pointShadowMatrix:[],hemi:[],numSpotLightShadowsWithMaps:0,numLightProbes:0};for(let N=0;N<9;N++)Z.probe.push(new v);let W=new v,K=new X9,H=new X9;function Y(N){let U=0,G=0,D=0;for(let z=0;z<9;z++)Z.probe[z].set(0,0,0);let R=0,M=0,L=0,E=0,q=0,A=0,V=0,I=0,f=0,P=0,S=0;N.sort(aN);for(let z=0,F=N.length;z<F;z++){let T=N[z],n=T.color,s=T.intensity,d=T.distance,Z0=T.shadow&&T.shadow.map?T.shadow.map.texture:null;if(T.isAmbientLight)U+=n.r*s,G+=n.g*s,D+=n.b*s;else if(T.isLightProbe){for(let m=0;m<9;m++)Z.probe[m].addScaledVector(T.sh.coefficients[m],s);S++}else if(T.isDirectionalLight){let m=$.get(T);if(m.color.copy(T.color).multiplyScalar(T.intensity),T.castShadow){let W0=T.shadow,u=Q.get(T);u.shadowIntensity=W0.intensity,u.shadowBias=W0.bias,u.shadowNormalBias=W0.normalBias,u.shadowRadius=W0.radius,u.shadowMapSize=W0.mapSize,Z.directionalShadow[R]=u,Z.directionalShadowMap[R]=Z0,Z.directionalShadowMatrix[R]=T.shadow.matrix,A++}Z.directional[R]=m,R++}else if(T.isSpotLight){let m=$.get(T);m.position.setFromMatrixPosition(T.matrixWorld),m.color.copy(n).multiplyScalar(s),m.distance=d,m.coneCos=Math.cos(T.angle),m.penumbraCos=Math.cos(T.angle*(1-T.penumbra)),m.decay=T.decay,Z.spot[L]=m;let W0=T.shadow;if(T.map){if(Z.spotLightMap[f]=T.map,f++,W0.updateMatrices(T),T.castShadow)P++}if(Z.spotLightMatrix[L]=W0.matrix,T.castShadow){let u=Q.get(T);u.shadowIntensity=W0.intensity,u.shadowBias=W0.bias,u.shadowNormalBias=W0.normalBias,u.shadowRadius=W0.radius,u.shadowMapSize=W0.mapSize,Z.spotShadow[L]=u,Z.spotShadowMap[L]=Z0,I++}L++}else if(T.isRectAreaLight){let m=$.get(T);m.color.copy(n).multiplyScalar(s),m.halfWidth.set(T.width*0.5,0,0),m.halfHeight.set(0,T.height*0.5,0),Z.rectArea[E]=m,E++}else if(T.isPointLight){let m=$.get(T);if(m.color.copy(T.color).multiplyScalar(T.intensity),m.distance=T.distance,m.decay=T.decay,T.castShadow){let W0=T.shadow,u=Q.get(T);u.shadowIntensity=W0.intensity,u.shadowBias=W0.bias,u.shadowNormalBias=W0.normalBias,u.shadowRadius=W0.radius,u.shadowMapSize=W0.mapSize,u.shadowCameraNear=W0.camera.near,u.shadowCameraFar=W0.camera.far,Z.pointShadow[M]=u,Z.pointShadowMap[M]=Z0,Z.pointShadowMatrix[M]=T.shadow.matrix,V++}Z.point[M]=m,M++}else if(T.isHemisphereLight){let m=$.get(T);m.skyColor.copy(T.color).multiplyScalar(s),m.groundColor.copy(T.groundColor).multiplyScalar(s),Z.hemi[q]=m,q++}}if(E>0)if(J.has("OES_texture_float_linear")===!0)Z.rectAreaLTC1=D0.LTC_FLOAT_1,Z.rectAreaLTC2=D0.LTC_FLOAT_2;else Z.rectAreaLTC1=D0.LTC_HALF_1,Z.rectAreaLTC2=D0.LTC_HALF_2;Z.ambient[0]=U,Z.ambient[1]=G,Z.ambient[2]=D;let g=Z.hash;if(g.directionalLength!==R||g.pointLength!==M||g.spotLength!==L||g.rectAreaLength!==E||g.hemiLength!==q||g.numDirectionalShadows!==A||g.numPointShadows!==V||g.numSpotShadows!==I||g.numSpotMaps!==f||g.numLightProbes!==S)Z.directional.length=R,Z.spot.length=L,Z.rectArea.length=E,Z.point.length=M,Z.hemi.length=q,Z.directionalShadow.length=A,Z.directionalShadowMap.length=A,Z.pointShadow.length=V,Z.pointShadowMap.length=V,Z.spotShadow.length=I,Z.spotShadowMap.length=I,Z.directionalShadowMatrix.length=A,Z.pointShadowMatrix.length=V,Z.spotLightMatrix.length=I+f-P,Z.spotLightMap.length=f,Z.numSpotLightShadowsWithMaps=P,Z.numLightProbes=S,g.directionalLength=R,g.pointLength=M,g.spotLength=L,g.rectAreaLength=E,g.hemiLength=q,g.numDirectionalShadows=A,g.numPointShadows=V,g.numSpotShadows=I,g.numSpotMaps=f,g.numLightProbes=S,Z.version=oN++}function X(N,U){let G=0,D=0,R=0,M=0,L=0,E=U.matrixWorldInverse;for(let q=0,A=N.length;q<A;q++){let V=N[q];if(V.isDirectionalLight){let I=Z.directional[G];I.direction.setFromMatrixPosition(V.matrixWorld),W.setFromMatrixPosition(V.target.matrixWorld),I.direction.sub(W),I.direction.transformDirection(E),G++}else if(V.isSpotLight){let I=Z.spot[R];I.position.setFromMatrixPosition(V.matrixWorld),I.position.applyMatrix4(E),I.direction.setFromMatrixPosition(V.matrixWorld),W.setFromMatrixPosition(V.target.matrixWorld),I.direction.sub(W),I.direction.transformDirection(E),R++}else if(V.isRectAreaLight){let I=Z.rectArea[M];I.position.setFromMatrixPosition(V.matrixWorld),I.position.applyMatrix4(E),H.identity(),K.copy(V.matrixWorld),K.premultiply(E),H.extractRotation(K),I.halfWidth.set(V.width*0.5,0,0),I.halfHeight.set(0,V.height*0.5,0),I.halfWidth.applyMatrix4(H),I.halfHeight.applyMatrix4(H),M++}else if(V.isPointLight){let I=Z.point[D];I.position.setFromMatrixPosition(V.matrixWorld),I.position.applyMatrix4(E),D++}else if(V.isHemisphereLight){let I=Z.hemi[L];I.direction.setFromMatrixPosition(V.matrixWorld),I.direction.transformDirection(E),L++}}}return{setup:Y,setupView:X,state:Z}}function MW(J){let $=new rN(J),Q=[],Z=[];function W(U){N.camera=U,Q.length=0,Z.length=0}function K(U){Q.push(U)}function H(U){Z.push(U)}function Y(){$.setup(Q)}function X(U){$.setupView(Q,U)}let N={lightsArray:Q,shadowsArray:Z,camera:null,lights:$,transmissionRenderTarget:{}};return{init:W,state:N,setupLights:Y,setupLightsView:X,pushLight:K,pushShadow:H}}function tN(J){let $=new WeakMap;function Q(W,K=0){let H=$.get(W),Y;if(H===void 0)Y=new MW(J),$.set(W,[Y]);else if(K>=H.length)Y=new MW(J),H.push(Y);else Y=H[K];return Y}function Z(){$=new WeakMap}return{get:Q,dispose:Z}}var eN=`void main() {
	gl_Position = vec4( position, 1.0 );
}`,J1=`uniform sampler2D shadow_pass;
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
}`;function $1(J,$,Q){let Z=new i8,W=new x0,K=new x0,H=new G9,Y=new y$({depthPacking:jZ}),X=new b$,N={},U=Q.maxTextureSize,G={[D8]:p9,[p9]:D8,[s9]:s9},D=new KJ({defines:{VSM_SAMPLES:8},uniforms:{shadow_pass:{value:null},resolution:{value:new x0},radius:{value:4}},vertexShader:eN,fragmentShader:J1}),R=D.clone();R.defines.HORIZONTAL_PASS=1;let M=new l9;M.setAttribute("position",new n9(new Float32Array([-1,-1,0.5,3,-1,0.5,-1,3,0.5]),3));let L=new A9(M,D),E=this;this.enabled=!1,this.autoUpdate=!0,this.needsUpdate=!1,this.type=v7;let q=this.type;this.render=function(P,S,g){if(E.enabled===!1)return;if(E.autoUpdate===!1&&E.needsUpdate===!1)return;if(P.length===0)return;let z=J.getRenderTarget(),F=J.getActiveCubeFace(),T=J.getActiveMipmapLevel(),n=J.state;if(n.setBlending(IJ),n.buffers.depth.getReversed()===!0)n.buffers.color.setClear(0,0,0,0);else n.buffers.color.setClear(1,1,1,1);n.buffers.depth.setTest(!0),n.setScissorTest(!1);let s=q!==QJ&&this.type===QJ,d=q===QJ&&this.type!==QJ;for(let Z0=0,m=P.length;Z0<m;Z0++){let W0=P[Z0],u=W0.shadow;if(u===void 0){console.warn("THREE.WebGLShadowMap:",W0,"has no shadow.");continue}if(u.autoUpdate===!1&&u.needsUpdate===!1)continue;W.copy(u.mapSize);let N0=u.getFrameExtents();if(W.multiply(N0),K.copy(u.mapSize),W.x>U||W.y>U){if(W.x>U)K.x=Math.floor(U/N0.x),W.x=K.x*N0.x,u.mapSize.x=K.x;if(W.y>U)K.y=Math.floor(U/N0.y),W.y=K.y*N0.y,u.mapSize.y=K.y}if(u.map===null||s===!0||d===!0){let _0=this.type!==QJ?{minFilter:R8,magFilter:R8}:{};if(u.map!==null)u.map.dispose();u.map=new FJ(W.x,W.y,_0),u.map.texture.name=W0.name+".shadowMap",u.camera.updateProjectionMatrix()}J.setRenderTarget(u.map),J.clear();let q0=u.getViewportCount();for(let _0=0;_0<q0;_0++){let g0=u.getViewport(_0);H.set(K.x*g0.x,K.y*g0.y,K.x*g0.z,K.y*g0.w),n.viewport(H),u.updateMatrices(W0,_0),Z=u.getFrustum(),I(S,g,u.camera,W0,this.type)}if(u.isPointLightShadow!==!0&&this.type===QJ)A(u,g);u.needsUpdate=!1}q=this.type,E.needsUpdate=!1,J.setRenderTarget(z,F,T)};function A(P,S){let g=$.update(L);if(D.defines.VSM_SAMPLES!==P.blurSamples)D.defines.VSM_SAMPLES=P.blurSamples,R.defines.VSM_SAMPLES=P.blurSamples,D.needsUpdate=!0,R.needsUpdate=!0;if(P.mapPass===null)P.mapPass=new FJ(W.x,W.y);D.uniforms.shadow_pass.value=P.map.texture,D.uniforms.resolution.value=P.mapSize,D.uniforms.radius.value=P.radius,J.setRenderTarget(P.mapPass),J.clear(),J.renderBufferDirect(S,null,g,D,L,null),R.uniforms.shadow_pass.value=P.mapPass.texture,R.uniforms.resolution.value=P.mapSize,R.uniforms.radius.value=P.radius,J.setRenderTarget(P.map),J.clear(),J.renderBufferDirect(S,null,g,R,L,null)}function V(P,S,g,z){let F=null,T=g.isPointLight===!0?P.customDistanceMaterial:P.customDepthMaterial;if(T!==void 0)F=T;else if(F=g.isPointLight===!0?X:Y,J.localClippingEnabled&&S.clipShadows===!0&&Array.isArray(S.clippingPlanes)&&S.clippingPlanes.length!==0||S.displacementMap&&S.displacementScale!==0||S.alphaMap&&S.alphaTest>0||S.map&&S.alphaTest>0||S.alphaToCoverage===!0){let n=F.uuid,s=S.uuid,d=N[n];if(d===void 0)d={},N[n]=d;let Z0=d[s];if(Z0===void 0)Z0=F.clone(),d[s]=Z0,S.addEventListener("dispose",f);F=Z0}if(F.visible=S.visible,F.wireframe=S.wireframe,z===QJ)F.side=S.shadowSide!==null?S.shadowSide:S.side;else F.side=S.shadowSide!==null?S.shadowSide:G[S.side];if(F.alphaMap=S.alphaMap,F.alphaTest=S.alphaToCoverage===!0?0.5:S.alphaTest,F.map=S.map,F.clipShadows=S.clipShadows,F.clippingPlanes=S.clippingPlanes,F.clipIntersection=S.clipIntersection,F.displacementMap=S.displacementMap,F.displacementScale=S.displacementScale,F.displacementBias=S.displacementBias,F.wireframeLinewidth=S.wireframeLinewidth,F.linewidth=S.linewidth,g.isPointLight===!0&&F.isMeshDistanceMaterial===!0){let n=J.properties.get(F);n.light=g}return F}function I(P,S,g,z,F){if(P.visible===!1)return;if(P.layers.test(S.layers)&&(P.isMesh||P.isLine||P.isPoints)){if((P.castShadow||P.receiveShadow&&F===QJ)&&(!P.frustumCulled||Z.intersectsObject(P))){P.modelViewMatrix.multiplyMatrices(g.matrixWorldInverse,P.matrixWorld);let s=$.update(P),d=P.material;if(Array.isArray(d)){let Z0=s.groups;for(let m=0,W0=Z0.length;m<W0;m++){let u=Z0[m],N0=d[u.materialIndex];if(N0&&N0.visible){let q0=V(P,N0,z,F);P.onBeforeShadow(J,P,S,g,s,q0,u),J.renderBufferDirect(g,null,s,q0,P,u),P.onAfterShadow(J,P,S,g,s,q0,u)}}}else if(d.visible){let Z0=V(P,d,z,F);P.onBeforeShadow(J,P,S,g,s,Z0,null),J.renderBufferDirect(g,null,s,Z0,P,null),P.onAfterShadow(J,P,S,g,s,Z0,null)}}}let n=P.children;for(let s=0,d=n.length;s<d;s++)I(n[s],S,g,z,F)}function f(P){P.target.removeEventListener("dispose",f);for(let g in N){let z=N[g],F=P.target.uuid;if(F in z)z[F].dispose(),delete z[F]}}}var Q1={[w6]:C6,[_6]:A6,[I6]:T6,[x8]:P6,[C6]:w6,[A6]:_6,[T6]:I6,[P6]:x8};function Z1(J,$){function Q(){let _=!1,G0=new G9,C=null,J0=new G9(0,0,0,0);return{setMask:function(r){if(C!==r&&!_)J.colorMask(r,r,r,r),C=r},setLocked:function(r){_=r},setClear:function(r,i,M0,f0,r0){if(r0===!0)r*=f0,i*=f0,M0*=f0;if(G0.set(r,i,M0,f0),J0.equals(G0)===!1)J.clearColor(r,i,M0,f0),J0.copy(G0)},reset:function(){_=!1,C=null,J0.set(-1,0,0,0)}}}function Z(){let _=!1,G0=!1,C=null,J0=null,r=null;return{setReversed:function(i){if(G0!==i){let M0=$.get("EXT_clip_control");if(i)M0.clipControlEXT(M0.LOWER_LEFT_EXT,M0.ZERO_TO_ONE_EXT);else M0.clipControlEXT(M0.LOWER_LEFT_EXT,M0.NEGATIVE_ONE_TO_ONE_EXT);G0=i;let f0=r;r=null,this.setClear(f0)}},getReversed:function(){return G0},setTest:function(i){if(i)h(J.DEPTH_TEST);else t(J.DEPTH_TEST)},setMask:function(i){if(C!==i&&!_)J.depthMask(i),C=i},setFunc:function(i){if(G0)i=Q1[i];if(J0!==i){switch(i){case w6:J.depthFunc(J.NEVER);break;case C6:J.depthFunc(J.ALWAYS);break;case _6:J.depthFunc(J.LESS);break;case x8:J.depthFunc(J.LEQUAL);break;case I6:J.depthFunc(J.EQUAL);break;case P6:J.depthFunc(J.GEQUAL);break;case A6:J.depthFunc(J.GREATER);break;case T6:J.depthFunc(J.NOTEQUAL);break;default:J.depthFunc(J.LEQUAL)}J0=i}},setLocked:function(i){_=i},setClear:function(i){if(r!==i){if(G0)i=1-i;J.clearDepth(i),r=i}},reset:function(){_=!1,C=null,J0=null,r=null,G0=!1}}}function W(){let _=!1,G0=null,C=null,J0=null,r=null,i=null,M0=null,f0=null,r0=null;return{setTest:function(J9){if(!_)if(J9)h(J.STENCIL_TEST);else t(J.STENCIL_TEST)},setMask:function(J9){if(G0!==J9&&!_)J.stencilMask(J9),G0=J9},setFunc:function(J9,JJ,$J){if(C!==J9||J0!==JJ||r!==$J)J.stencilFunc(J9,JJ,$J),C=J9,J0=JJ,r=$J},setOp:function(J9,JJ,$J){if(i!==J9||M0!==JJ||f0!==$J)J.stencilOp(J9,JJ,$J),i=J9,M0=JJ,f0=$J},setLocked:function(J9){_=J9},setClear:function(J9){if(r0!==J9)J.clearStencil(J9),r0=J9},reset:function(){_=!1,G0=null,C=null,J0=null,r=null,i=null,M0=null,f0=null,r0=null}}}let K=new Q,H=new Z,Y=new W,X=new WeakMap,N=new WeakMap,U={},G={},D=new WeakMap,R=[],M=null,L=!1,E=null,q=null,A=null,V=null,I=null,f=null,P=null,S=new n0(0,0,0),g=0,z=!1,F=null,T=null,n=null,s=null,d=null,Z0=J.getParameter(J.MAX_COMBINED_TEXTURE_IMAGE_UNITS),m=!1,W0=0,u=J.getParameter(J.VERSION);if(u.indexOf("WebGL")!==-1)W0=parseFloat(/^WebGL (\d)/.exec(u)[1]),m=W0>=1;else if(u.indexOf("OpenGL ES")!==-1)W0=parseFloat(/^OpenGL ES (\d)/.exec(u)[1]),m=W0>=2;let N0=null,q0={},_0=J.getParameter(J.SCISSOR_BOX),g0=J.getParameter(J.VIEWPORT),a0=new G9().fromArray(_0),m0=new G9().fromArray(g0);function o(_,G0,C,J0){let r=new Uint8Array(4),i=J.createTexture();J.bindTexture(_,i),J.texParameteri(_,J.TEXTURE_MIN_FILTER,J.NEAREST),J.texParameteri(_,J.TEXTURE_MAG_FILTER,J.NEAREST);for(let M0=0;M0<C;M0++)if(_===J.TEXTURE_3D||_===J.TEXTURE_2D_ARRAY)J.texImage3D(G0,0,J.RGBA,1,1,J0,0,J.RGBA,J.UNSIGNED_BYTE,r);else J.texImage2D(G0+M0,0,J.RGBA,1,1,0,J.RGBA,J.UNSIGNED_BYTE,r);return i}let H0={};H0[J.TEXTURE_2D]=o(J.TEXTURE_2D,J.TEXTURE_2D,1),H0[J.TEXTURE_CUBE_MAP]=o(J.TEXTURE_CUBE_MAP,J.TEXTURE_CUBE_MAP_POSITIVE_X,6),H0[J.TEXTURE_2D_ARRAY]=o(J.TEXTURE_2D_ARRAY,J.TEXTURE_2D_ARRAY,1,1),H0[J.TEXTURE_3D]=o(J.TEXTURE_3D,J.TEXTURE_3D,1,1),K.setClear(0,0,0,1),H.setClear(1),Y.setClear(0),h(J.DEPTH_TEST),H.setFunc(x8),L0(!1),i0(f7),h(J.CULL_FACE),S0(IJ);function h(_){if(U[_]!==!0)J.enable(_),U[_]=!0}function t(_){if(U[_]!==!1)J.disable(_),U[_]=!1}function Q0(_,G0){if(G[_]!==G0){if(J.bindFramebuffer(_,G0),G[_]=G0,_===J.DRAW_FRAMEBUFFER)G[J.FRAMEBUFFER]=G0;if(_===J.FRAMEBUFFER)G[J.DRAW_FRAMEBUFFER]=G0;return!0}return!1}function E0(_,G0){let C=R,J0=!1;if(_){if(C=D.get(G0),C===void 0)C=[],D.set(G0,C);let r=_.textures;if(C.length!==r.length||C[0]!==J.COLOR_ATTACHMENT0){for(let i=0,M0=r.length;i<M0;i++)C[i]=J.COLOR_ATTACHMENT0+i;C.length=r.length,J0=!0}}else if(C[0]!==J.BACK)C[0]=J.BACK,J0=!0;if(J0)J.drawBuffers(C)}function l0(_){if(M!==_)return J.useProgram(_),M=_,!0;return!1}let w={[q8]:J.FUNC_ADD,[cQ]:J.FUNC_SUBTRACT,[nQ]:J.FUNC_REVERSE_SUBTRACT};w[sQ]=J.MIN,w[iQ]=J.MAX;let v0={[oQ]:J.ZERO,[aQ]:J.ONE,[rQ]:J.SRC_COLOR,[eQ]:J.SRC_ALPHA,[KZ]:J.SRC_ALPHA_SATURATE,[ZZ]:J.DST_COLOR,[$Z]:J.DST_ALPHA,[tQ]:J.ONE_MINUS_SRC_COLOR,[JZ]:J.ONE_MINUS_SRC_ALPHA,[WZ]:J.ONE_MINUS_DST_COLOR,[QZ]:J.ONE_MINUS_DST_ALPHA,[YZ]:J.CONSTANT_COLOR,[HZ]:J.ONE_MINUS_CONSTANT_COLOR,[XZ]:J.CONSTANT_ALPHA,[NZ]:J.ONE_MINUS_CONSTANT_ALPHA};function S0(_,G0,C,J0,r,i,M0,f0,r0,J9){if(_===IJ){if(L===!0)t(J.BLEND),L=!1;return}if(L===!1)h(J.BLEND),L=!0;if(_!==uQ){if(_!==E||J9!==z){if(q!==q8||I!==q8)J.blendEquation(J.FUNC_ADD),q=q8,I=q8;if(J9)switch(_){case h8:J.blendFuncSeparate(J.ONE,J.ONE_MINUS_SRC_ALPHA,J.ONE,J.ONE_MINUS_SRC_ALPHA);break;case h7:J.blendFunc(J.ONE,J.ONE);break;case x7:J.blendFuncSeparate(J.ZERO,J.ONE_MINUS_SRC_COLOR,J.ZERO,J.ONE);break;case g7:J.blendFuncSeparate(J.DST_COLOR,J.ONE_MINUS_SRC_ALPHA,J.ZERO,J.ONE);break;default:console.error("THREE.WebGLState: Invalid blending: ",_);break}else switch(_){case h8:J.blendFuncSeparate(J.SRC_ALPHA,J.ONE_MINUS_SRC_ALPHA,J.ONE,J.ONE_MINUS_SRC_ALPHA);break;case h7:J.blendFuncSeparate(J.SRC_ALPHA,J.ONE,J.ONE,J.ONE);break;case x7:console.error("THREE.WebGLState: SubtractiveBlending requires material.premultipliedAlpha = true");break;case g7:console.error("THREE.WebGLState: MultiplyBlending requires material.premultipliedAlpha = true");break;default:console.error("THREE.WebGLState: Invalid blending: ",_);break}A=null,V=null,f=null,P=null,S.set(0,0,0),g=0,E=_,z=J9}return}if(r=r||G0,i=i||C,M0=M0||J0,G0!==q||r!==I)J.blendEquationSeparate(w[G0],w[r]),q=G0,I=r;if(C!==A||J0!==V||i!==f||M0!==P)J.blendFuncSeparate(v0[C],v0[J0],v0[i],v0[M0]),A=C,V=J0,f=i,P=M0;if(f0.equals(S)===!1||r0!==g)J.blendColor(f0.r,f0.g,f0.b,r0),S.copy(f0),g=r0;E=_,z=!1}function A0(_,G0){_.side===s9?t(J.CULL_FACE):h(J.CULL_FACE);let C=_.side===p9;if(G0)C=!C;L0(C),_.blending===h8&&_.transparent===!1?S0(IJ):S0(_.blending,_.blendEquation,_.blendSrc,_.blendDst,_.blendEquationAlpha,_.blendSrcAlpha,_.blendDstAlpha,_.blendColor,_.blendAlpha,_.premultipliedAlpha),H.setFunc(_.depthFunc),H.setTest(_.depthTest),H.setMask(_.depthWrite),K.setMask(_.colorWrite);let J0=_.stencilWrite;if(Y.setTest(J0),J0)Y.setMask(_.stencilWriteMask),Y.setFunc(_.stencilFunc,_.stencilRef,_.stencilFuncMask),Y.setOp(_.stencilFail,_.stencilZFail,_.stencilZPass);C0(_.polygonOffset,_.polygonOffsetFactor,_.polygonOffsetUnits),_.alphaToCoverage===!0?h(J.SAMPLE_ALPHA_TO_COVERAGE):t(J.SAMPLE_ALPHA_TO_COVERAGE)}function L0(_){if(F!==_){if(_)J.frontFace(J.CW);else J.frontFace(J.CCW);F=_}}function i0(_){if(_!==lQ){if(h(J.CULL_FACE),_!==T)if(_===f7)J.cullFace(J.BACK);else if(_===dQ)J.cullFace(J.FRONT);else J.cullFace(J.FRONT_AND_BACK)}else t(J.CULL_FACE);T=_}function w0(_){if(_!==n){if(m)J.lineWidth(_);n=_}}function C0(_,G0,C){if(_){if(h(J.POLYGON_OFFSET_FILL),s!==G0||d!==C)J.polygonOffset(G0,C),s=G0,d=C}else t(J.POLYGON_OFFSET_FILL)}function H9(_){if(_)h(J.SCISSOR_TEST);else t(J.SCISSOR_TEST)}function D9(_){if(_===void 0)_=J.TEXTURE0+Z0-1;if(N0!==_)J.activeTexture(_),N0=_}function e0(_,G0,C){if(C===void 0)if(N0===null)C=J.TEXTURE0+Z0-1;else C=N0;let J0=q0[C];if(J0===void 0)J0={type:void 0,texture:void 0},q0[C]=J0;if(J0.type!==_||J0.texture!==G0){if(N0!==C)J.activeTexture(C),N0=C;J.bindTexture(_,G0||H0[_]),J0.type=_,J0.texture=G0}}function B(){let _=q0[N0];if(_!==void 0&&_.type!==void 0)J.bindTexture(_.type,null),_.type=void 0,_.texture=void 0}function O(){try{J.compressedTexImage2D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function j(){try{J.compressedTexImage3D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function x(){try{J.texSubImage2D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function e(){try{J.texSubImage3D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function c(){try{J.compressedTexSubImage2D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function R0(){try{J.compressedTexSubImage3D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function X0(){try{J.texStorage2D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function z0(){try{J.texStorage3D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function T0(){try{J.texImage2D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function a(){try{J.texImage3D(...arguments)}catch(_){console.error("THREE.WebGLState:",_)}}function K0(_){if(a0.equals(_)===!1)J.scissor(_.x,_.y,_.z,_.w),a0.copy(_)}function k0(_){if(m0.equals(_)===!1)J.viewport(_.x,_.y,_.z,_.w),m0.copy(_)}function F0(_,G0){let C=N.get(G0);if(C===void 0)C=new WeakMap,N.set(G0,C);let J0=C.get(_);if(J0===void 0)J0=J.getUniformBlockIndex(G0,_.name),C.set(_,J0)}function U0(_,G0){let J0=N.get(G0).get(_);if(X.get(G0)!==J0)J.uniformBlockBinding(G0,J0,_.__bindingPointIndex),X.set(G0,J0)}function I0(){J.disable(J.BLEND),J.disable(J.CULL_FACE),J.disable(J.DEPTH_TEST),J.disable(J.POLYGON_OFFSET_FILL),J.disable(J.SCISSOR_TEST),J.disable(J.STENCIL_TEST),J.disable(J.SAMPLE_ALPHA_TO_COVERAGE),J.blendEquation(J.FUNC_ADD),J.blendFunc(J.ONE,J.ZERO),J.blendFuncSeparate(J.ONE,J.ZERO,J.ONE,J.ZERO),J.blendColor(0,0,0,0),J.colorMask(!0,!0,!0,!0),J.clearColor(0,0,0,0),J.depthMask(!0),J.depthFunc(J.LESS),H.setReversed(!1),J.clearDepth(1),J.stencilMask(4294967295),J.stencilFunc(J.ALWAYS,0,4294967295),J.stencilOp(J.KEEP,J.KEEP,J.KEEP),J.clearStencil(0),J.cullFace(J.BACK),J.frontFace(J.CCW),J.polygonOffset(0,0),J.activeTexture(J.TEXTURE0),J.bindFramebuffer(J.FRAMEBUFFER,null),J.bindFramebuffer(J.DRAW_FRAMEBUFFER,null),J.bindFramebuffer(J.READ_FRAMEBUFFER,null),J.useProgram(null),J.lineWidth(1),J.scissor(0,0,J.canvas.width,J.canvas.height),J.viewport(0,0,J.canvas.width,J.canvas.height),U={},N0=null,q0={},G={},D=new WeakMap,R=[],M=null,L=!1,E=null,q=null,A=null,V=null,I=null,f=null,P=null,S=new n0(0,0,0),g=0,z=!1,F=null,T=null,n=null,s=null,d=null,a0.set(0,0,J.canvas.width,J.canvas.height),m0.set(0,0,J.canvas.width,J.canvas.height),K.reset(),H.reset(),Y.reset()}return{buffers:{color:K,depth:H,stencil:Y},enable:h,disable:t,bindFramebuffer:Q0,drawBuffers:E0,useProgram:l0,setBlending:S0,setMaterial:A0,setFlipSided:L0,setCullFace:i0,setLineWidth:w0,setPolygonOffset:C0,setScissorTest:H9,activeTexture:D9,bindTexture:e0,unbindTexture:B,compressedTexImage2D:O,compressedTexImage3D:j,texImage2D:T0,texImage3D:a,updateUBOMapping:F0,uniformBlockBinding:U0,texStorage2D:X0,texStorage3D:z0,texSubImage2D:x,texSubImage3D:e,compressedTexSubImage2D:c,compressedTexSubImage3D:R0,scissor:K0,viewport:k0,reset:I0}}function W1(J,$,Q,Z,W,K,H){let Y=$.has("WEBGL_multisampled_render_to_texture")?$.get("WEBGL_multisampled_render_to_texture"):null,X=typeof navigator>"u"?!1:/OculusBrowser/g.test(navigator.userAgent),N=new x0,U=new WeakMap,G,D=new WeakMap,R=!1;try{R=typeof OffscreenCanvas<"u"&&new OffscreenCanvas(1,1).getContext("2d")!==null}catch(B){}function M(B,O){return R?new OffscreenCanvas(B,O):v8("canvas")}function L(B,O,j){let x=1,e=e0(B);if(e.width>j||e.height>j)x=j/Math.max(e.width,e.height);if(x<1)if(typeof HTMLImageElement<"u"&&B instanceof HTMLImageElement||typeof HTMLCanvasElement<"u"&&B instanceof HTMLCanvasElement||typeof ImageBitmap<"u"&&B instanceof ImageBitmap||typeof VideoFrame<"u"&&B instanceof VideoFrame){let c=Math.floor(x*e.width),R0=Math.floor(x*e.height);if(G===void 0)G=M(c,R0);let X0=O?M(c,R0):G;return X0.width=c,X0.height=R0,X0.getContext("2d").drawImage(B,0,0,c,R0),console.warn("THREE.WebGLRenderer: Texture has been resized from ("+e.width+"x"+e.height+") to ("+c+"x"+R0+")."),X0}else{if("data"in B)console.warn("THREE.WebGLRenderer: Image in DataTexture is too big ("+e.width+"x"+e.height+").");return B}return B}function E(B){return B.generateMipmaps}function q(B){J.generateMipmap(B)}function A(B){if(B.isWebGLCubeRenderTarget)return J.TEXTURE_CUBE_MAP;if(B.isWebGL3DRenderTarget)return J.TEXTURE_3D;if(B.isWebGLArrayRenderTarget||B.isCompressedArrayTexture)return J.TEXTURE_2D_ARRAY;return J.TEXTURE_2D}function V(B,O,j,x,e=!1){if(B!==null){if(J[B]!==void 0)return J[B];console.warn("THREE.WebGLRenderer: Attempt to use non-existing WebGL internal format '"+B+"'")}let c=O;if(O===J.RED){if(j===J.FLOAT)c=J.R32F;if(j===J.HALF_FLOAT)c=J.R16F;if(j===J.UNSIGNED_BYTE)c=J.R8}if(O===J.RED_INTEGER){if(j===J.UNSIGNED_BYTE)c=J.R8UI;if(j===J.UNSIGNED_SHORT)c=J.R16UI;if(j===J.UNSIGNED_INT)c=J.R32UI;if(j===J.BYTE)c=J.R8I;if(j===J.SHORT)c=J.R16I;if(j===J.INT)c=J.R32I}if(O===J.RG){if(j===J.FLOAT)c=J.RG32F;if(j===J.HALF_FLOAT)c=J.RG16F;if(j===J.UNSIGNED_BYTE)c=J.RG8}if(O===J.RG_INTEGER){if(j===J.UNSIGNED_BYTE)c=J.RG8UI;if(j===J.UNSIGNED_SHORT)c=J.RG16UI;if(j===J.UNSIGNED_INT)c=J.RG32UI;if(j===J.BYTE)c=J.RG8I;if(j===J.SHORT)c=J.RG16I;if(j===J.INT)c=J.RG32I}if(O===J.RGB_INTEGER){if(j===J.UNSIGNED_BYTE)c=J.RGB8UI;if(j===J.UNSIGNED_SHORT)c=J.RGB16UI;if(j===J.UNSIGNED_INT)c=J.RGB32UI;if(j===J.BYTE)c=J.RGB8I;if(j===J.SHORT)c=J.RGB16I;if(j===J.INT)c=J.RGB32I}if(O===J.RGBA_INTEGER){if(j===J.UNSIGNED_BYTE)c=J.RGBA8UI;if(j===J.UNSIGNED_SHORT)c=J.RGBA16UI;if(j===J.UNSIGNED_INT)c=J.RGBA32UI;if(j===J.BYTE)c=J.RGBA8I;if(j===J.SHORT)c=J.RGBA16I;if(j===J.INT)c=J.RGBA32I}if(O===J.RGB){if(j===J.UNSIGNED_INT_5_9_9_9_REV)c=J.RGB9_E5;if(j===J.UNSIGNED_INT_10F_11F_11F_REV)c=J.R11F_G11F_B10F}if(O===J.RGBA){let R0=e?L$:s0.getTransfer(x);if(j===J.FLOAT)c=J.RGBA32F;if(j===J.HALF_FLOAT)c=J.RGBA16F;if(j===J.UNSIGNED_BYTE)c=R0===K9?J.SRGB8_ALPHA8:J.RGBA8;if(j===J.UNSIGNED_SHORT_4_4_4_4)c=J.RGBA4;if(j===J.UNSIGNED_SHORT_5_5_5_1)c=J.RGB5_A1}if(c===J.R16F||c===J.R32F||c===J.RG16F||c===J.RG32F||c===J.RGBA16F||c===J.RGBA32F)$.get("EXT_color_buffer_float");return c}function I(B,O){let j;if(B){if(O===null||O===F8||O===M8)j=J.DEPTH24_STENCIL8;else if(O===AJ)j=J.DEPTH32F_STENCIL8;else if(O===l8)j=J.DEPTH24_STENCIL8,console.warn("DepthTexture: 16 bit depth attachment is not supported with stencil. Using 24-bit attachment.")}else if(O===null||O===F8||O===M8)j=J.DEPTH_COMPONENT24;else if(O===AJ)j=J.DEPTH_COMPONENT32F;else if(O===l8)j=J.DEPTH_COMPONENT16;return j}function f(B,O){if(E(B)===!0||B.isFramebufferTexture&&B.minFilter!==R8&&B.minFilter!==xJ)return Math.log2(Math.max(O.width,O.height))+1;else if(B.mipmaps!==void 0&&B.mipmaps.length>0)return B.mipmaps.length;else if(B.isCompressedTexture&&Array.isArray(B.image))return O.mipmaps.length;else return 1}function P(B){let O=B.target;if(O.removeEventListener("dispose",P),g(O),O.isVideoTexture)U.delete(O)}function S(B){let O=B.target;O.removeEventListener("dispose",S),F(O)}function g(B){let O=Z.get(B);if(O.__webglInit===void 0)return;let j=B.source,x=D.get(j);if(x){let e=x[O.__cacheKey];if(e.usedTimes--,e.usedTimes===0)z(B);if(Object.keys(x).length===0)D.delete(j)}Z.remove(B)}function z(B){let O=Z.get(B);J.deleteTexture(O.__webglTexture);let j=B.source,x=D.get(j);delete x[O.__cacheKey],H.memory.textures--}function F(B){let O=Z.get(B);if(B.depthTexture)B.depthTexture.dispose(),Z.remove(B.depthTexture);if(B.isWebGLCubeRenderTarget)for(let x=0;x<6;x++){if(Array.isArray(O.__webglFramebuffer[x]))for(let e=0;e<O.__webglFramebuffer[x].length;e++)J.deleteFramebuffer(O.__webglFramebuffer[x][e]);else J.deleteFramebuffer(O.__webglFramebuffer[x]);if(O.__webglDepthbuffer)J.deleteRenderbuffer(O.__webglDepthbuffer[x])}else{if(Array.isArray(O.__webglFramebuffer))for(let x=0;x<O.__webglFramebuffer.length;x++)J.deleteFramebuffer(O.__webglFramebuffer[x]);else J.deleteFramebuffer(O.__webglFramebuffer);if(O.__webglDepthbuffer)J.deleteRenderbuffer(O.__webglDepthbuffer);if(O.__webglMultisampledFramebuffer)J.deleteFramebuffer(O.__webglMultisampledFramebuffer);if(O.__webglColorRenderbuffer){for(let x=0;x<O.__webglColorRenderbuffer.length;x++)if(O.__webglColorRenderbuffer[x])J.deleteRenderbuffer(O.__webglColorRenderbuffer[x])}if(O.__webglDepthRenderbuffer)J.deleteRenderbuffer(O.__webglDepthRenderbuffer)}let j=B.textures;for(let x=0,e=j.length;x<e;x++){let c=Z.get(j[x]);if(c.__webglTexture)J.deleteTexture(c.__webglTexture),H.memory.textures--;Z.remove(j[x])}Z.remove(B)}let T=0;function n(){T=0}function s(){let B=T;if(B>=W.maxTextures)console.warn("THREE.WebGLTextures: Trying to use "+B+" texture units while this GPU supports only "+W.maxTextures);return T+=1,B}function d(B){let O=[];return O.push(B.wrapS),O.push(B.wrapT),O.push(B.wrapR||0),O.push(B.magFilter),O.push(B.minFilter),O.push(B.anisotropy),O.push(B.internalFormat),O.push(B.format),O.push(B.type),O.push(B.generateMipmaps),O.push(B.premultiplyAlpha),O.push(B.flipY),O.push(B.unpackAlignment),O.push(B.colorSpace),O.join()}function Z0(B,O){let j=Z.get(B);if(B.isVideoTexture)H9(B);if(B.isRenderTargetTexture===!1&&B.isExternalTexture!==!0&&B.version>0&&j.__version!==B.version){let x=B.image;if(x===null)console.warn("THREE.WebGLRenderer: Texture marked for update but no image data found.");else if(x.complete===!1)console.warn("THREE.WebGLRenderer: Texture marked for update but image is incomplete");else{H0(j,B,O);return}}else if(B.isExternalTexture)j.__webglTexture=B.sourceTexture?B.sourceTexture:null;Q.bindTexture(J.TEXTURE_2D,j.__webglTexture,J.TEXTURE0+O)}function m(B,O){let j=Z.get(B);if(B.isRenderTargetTexture===!1&&B.version>0&&j.__version!==B.version){H0(j,B,O);return}Q.bindTexture(J.TEXTURE_2D_ARRAY,j.__webglTexture,J.TEXTURE0+O)}function W0(B,O){let j=Z.get(B);if(B.isRenderTargetTexture===!1&&B.version>0&&j.__version!==B.version){H0(j,B,O);return}Q.bindTexture(J.TEXTURE_3D,j.__webglTexture,J.TEXTURE0+O)}function u(B,O){let j=Z.get(B);if(B.version>0&&j.__version!==B.version){h(j,B,O);return}Q.bindTexture(J.TEXTURE_CUBE_MAP,j.__webglTexture,J.TEXTURE0+O)}let N0={[LZ]:J.REPEAT,[VZ]:J.CLAMP_TO_EDGE,[zZ]:J.MIRRORED_REPEAT},q0={[R8]:J.NEAREST,[BZ]:J.NEAREST_MIPMAP_NEAREST,[p8]:J.NEAREST_MIPMAP_LINEAR,[xJ]:J.LINEAR,[y6]:J.LINEAR_MIPMAP_NEAREST,[k8]:J.LINEAR_MIPMAP_LINEAR},_0={[vZ]:J.NEVER,[dZ]:J.ALWAYS,[hZ]:J.LESS,[V$]:J.LEQUAL,[xZ]:J.EQUAL,[lZ]:J.GEQUAL,[gZ]:J.GREATER,[pZ]:J.NOTEQUAL};function g0(B,O){if(O.type===AJ&&$.has("OES_texture_float_linear")===!1&&(O.magFilter===xJ||O.magFilter===y6||O.magFilter===p8||O.magFilter===k8||O.minFilter===xJ||O.minFilter===y6||O.minFilter===p8||O.minFilter===k8))console.warn("THREE.WebGLRenderer: Unable to use linear filtering with floating point textures. OES_texture_float_linear not supported on this device.");if(J.texParameteri(B,J.TEXTURE_WRAP_S,N0[O.wrapS]),J.texParameteri(B,J.TEXTURE_WRAP_T,N0[O.wrapT]),B===J.TEXTURE_3D||B===J.TEXTURE_2D_ARRAY)J.texParameteri(B,J.TEXTURE_WRAP_R,N0[O.wrapR]);if(J.texParameteri(B,J.TEXTURE_MAG_FILTER,q0[O.magFilter]),J.texParameteri(B,J.TEXTURE_MIN_FILTER,q0[O.minFilter]),O.compareFunction)J.texParameteri(B,J.TEXTURE_COMPARE_MODE,J.COMPARE_REF_TO_TEXTURE),J.texParameteri(B,J.TEXTURE_COMPARE_FUNC,_0[O.compareFunction]);if($.has("EXT_texture_filter_anisotropic")===!0){if(O.magFilter===R8)return;if(O.minFilter!==p8&&O.minFilter!==k8)return;if(O.type===AJ&&$.has("OES_texture_float_linear")===!1)return;if(O.anisotropy>1||Z.get(O).__currentAnisotropy){let j=$.get("EXT_texture_filter_anisotropic");J.texParameterf(B,j.TEXTURE_MAX_ANISOTROPY_EXT,Math.min(O.anisotropy,W.getMaxAnisotropy())),Z.get(O).__currentAnisotropy=O.anisotropy}}}function a0(B,O){let j=!1;if(B.__webglInit===void 0)B.__webglInit=!0,O.addEventListener("dispose",P);let x=O.source,e=D.get(x);if(e===void 0)e={},D.set(x,e);let c=d(O);if(c!==B.__cacheKey){if(e[c]===void 0)e[c]={texture:J.createTexture(),usedTimes:0},H.memory.textures++,j=!0;e[c].usedTimes++;let R0=e[B.__cacheKey];if(R0!==void 0){if(e[B.__cacheKey].usedTimes--,R0.usedTimes===0)z(O)}B.__cacheKey=c,B.__webglTexture=e[c].texture}return j}function m0(B,O,j){return Math.floor(Math.floor(B/j)/O)}function o(B,O,j,x){let c=B.updateRanges;if(c.length===0)Q.texSubImage2D(J.TEXTURE_2D,0,0,0,O.width,O.height,j,x,O.data);else{c.sort((a,K0)=>a.start-K0.start);let R0=0;for(let a=1;a<c.length;a++){let K0=c[R0],k0=c[a],F0=K0.start+K0.count,U0=m0(k0.start,O.width,4),I0=m0(K0.start,O.width,4);if(k0.start<=F0+1&&U0===I0&&m0(k0.start+k0.count-1,O.width,4)===U0)K0.count=Math.max(K0.count,k0.start+k0.count-K0.start);else++R0,c[R0]=k0}c.length=R0+1;let X0=J.getParameter(J.UNPACK_ROW_LENGTH),z0=J.getParameter(J.UNPACK_SKIP_PIXELS),T0=J.getParameter(J.UNPACK_SKIP_ROWS);J.pixelStorei(J.UNPACK_ROW_LENGTH,O.width);for(let a=0,K0=c.length;a<K0;a++){let k0=c[a],F0=Math.floor(k0.start/4),U0=Math.ceil(k0.count/4),I0=F0%O.width,_=Math.floor(F0/O.width),G0=U0,C=1;J.pixelStorei(J.UNPACK_SKIP_PIXELS,I0),J.pixelStorei(J.UNPACK_SKIP_ROWS,_),Q.texSubImage2D(J.TEXTURE_2D,0,I0,_,G0,1,j,x,O.data)}B.clearUpdateRanges(),J.pixelStorei(J.UNPACK_ROW_LENGTH,X0),J.pixelStorei(J.UNPACK_SKIP_PIXELS,z0),J.pixelStorei(J.UNPACK_SKIP_ROWS,T0)}}function H0(B,O,j){let x=J.TEXTURE_2D;if(O.isDataArrayTexture||O.isCompressedArrayTexture)x=J.TEXTURE_2D_ARRAY;if(O.isData3DTexture)x=J.TEXTURE_3D;let e=a0(B,O),c=O.source;Q.bindTexture(x,B.__webglTexture,J.TEXTURE0+j);let R0=Z.get(c);if(c.version!==R0.__version||e===!0){Q.activeTexture(J.TEXTURE0+j);let X0=s0.getPrimaries(s0.workingColorSpace),z0=O.colorSpace===gJ?null:s0.getPrimaries(O.colorSpace),T0=O.colorSpace===gJ||X0===z0?J.NONE:J.BROWSER_DEFAULT_WEBGL;J.pixelStorei(J.UNPACK_FLIP_Y_WEBGL,O.flipY),J.pixelStorei(J.UNPACK_PREMULTIPLY_ALPHA_WEBGL,O.premultiplyAlpha),J.pixelStorei(J.UNPACK_ALIGNMENT,O.unpackAlignment),J.pixelStorei(J.UNPACK_COLORSPACE_CONVERSION_WEBGL,T0);let a=L(O.image,!1,W.maxTextureSize);a=D9(O,a);let K0=K.convert(O.format,O.colorSpace),k0=K.convert(O.type),F0=V(O.internalFormat,K0,k0,O.colorSpace,O.isVideoTexture);g0(x,O);let U0,I0=O.mipmaps,_=O.isVideoTexture!==!0,G0=R0.__version===void 0||e===!0,C=c.dataReady,J0=f(O,a);if(O.isDepthTexture){if(F0=I(O.format===m8,O.type),G0)if(_)Q.texStorage2D(J.TEXTURE_2D,1,F0,a.width,a.height);else Q.texImage2D(J.TEXTURE_2D,0,F0,a.width,a.height,0,K0,k0,null)}else if(O.isDataTexture)if(I0.length>0){if(_&&G0)Q.texStorage2D(J.TEXTURE_2D,J0,F0,I0[0].width,I0[0].height);for(let r=0,i=I0.length;r<i;r++)if(U0=I0[r],_){if(C)Q.texSubImage2D(J.TEXTURE_2D,r,0,0,U0.width,U0.height,K0,k0,U0.data)}else Q.texImage2D(J.TEXTURE_2D,r,F0,U0.width,U0.height,0,K0,k0,U0.data);O.generateMipmaps=!1}else if(_){if(G0)Q.texStorage2D(J.TEXTURE_2D,J0,F0,a.width,a.height);if(C)o(O,a,K0,k0)}else Q.texImage2D(J.TEXTURE_2D,0,F0,a.width,a.height,0,K0,k0,a.data);else if(O.isCompressedTexture)if(O.isCompressedArrayTexture){if(_&&G0)Q.texStorage3D(J.TEXTURE_2D_ARRAY,J0,F0,I0[0].width,I0[0].height,a.depth);for(let r=0,i=I0.length;r<i;r++)if(U0=I0[r],O.format!==ZJ)if(K0!==null)if(_){if(C)if(O.layerUpdates.size>0){let M0=s$(U0.width,U0.height,O.format,O.type);for(let f0 of O.layerUpdates){let r0=U0.data.subarray(f0*M0/U0.data.BYTES_PER_ELEMENT,(f0+1)*M0/U0.data.BYTES_PER_ELEMENT);Q.compressedTexSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,f0,U0.width,U0.height,1,K0,r0)}O.clearLayerUpdates()}else Q.compressedTexSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,0,U0.width,U0.height,a.depth,K0,U0.data)}else Q.compressedTexImage3D(J.TEXTURE_2D_ARRAY,r,F0,U0.width,U0.height,a.depth,0,U0.data,0,0);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .uploadTexture()");else if(_){if(C)Q.texSubImage3D(J.TEXTURE_2D_ARRAY,r,0,0,0,U0.width,U0.height,a.depth,K0,k0,U0.data)}else Q.texImage3D(J.TEXTURE_2D_ARRAY,r,F0,U0.width,U0.height,a.depth,0,K0,k0,U0.data)}else{if(_&&G0)Q.texStorage2D(J.TEXTURE_2D,J0,F0,I0[0].width,I0[0].height);for(let r=0,i=I0.length;r<i;r++)if(U0=I0[r],O.format!==ZJ)if(K0!==null)if(_){if(C)Q.compressedTexSubImage2D(J.TEXTURE_2D,r,0,0,U0.width,U0.height,K0,U0.data)}else Q.compressedTexImage2D(J.TEXTURE_2D,r,F0,U0.width,U0.height,0,U0.data);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .uploadTexture()");else if(_){if(C)Q.texSubImage2D(J.TEXTURE_2D,r,0,0,U0.width,U0.height,K0,k0,U0.data)}else Q.texImage2D(J.TEXTURE_2D,r,F0,U0.width,U0.height,0,K0,k0,U0.data)}else if(O.isDataArrayTexture)if(_){if(G0)Q.texStorage3D(J.TEXTURE_2D_ARRAY,J0,F0,a.width,a.height,a.depth);if(C)if(O.layerUpdates.size>0){let r=s$(a.width,a.height,O.format,O.type);for(let i of O.layerUpdates){let M0=a.data.subarray(i*r/a.data.BYTES_PER_ELEMENT,(i+1)*r/a.data.BYTES_PER_ELEMENT);Q.texSubImage3D(J.TEXTURE_2D_ARRAY,0,0,0,i,a.width,a.height,1,K0,k0,M0)}O.clearLayerUpdates()}else Q.texSubImage3D(J.TEXTURE_2D_ARRAY,0,0,0,0,a.width,a.height,a.depth,K0,k0,a.data)}else Q.texImage3D(J.TEXTURE_2D_ARRAY,0,F0,a.width,a.height,a.depth,0,K0,k0,a.data);else if(O.isData3DTexture)if(_){if(G0)Q.texStorage3D(J.TEXTURE_3D,J0,F0,a.width,a.height,a.depth);if(C)Q.texSubImage3D(J.TEXTURE_3D,0,0,0,0,a.width,a.height,a.depth,K0,k0,a.data)}else Q.texImage3D(J.TEXTURE_3D,0,F0,a.width,a.height,a.depth,0,K0,k0,a.data);else if(O.isFramebufferTexture){if(G0)if(_)Q.texStorage2D(J.TEXTURE_2D,J0,F0,a.width,a.height);else{let{width:r,height:i}=a;for(let M0=0;M0<J0;M0++)Q.texImage2D(J.TEXTURE_2D,M0,F0,r,i,0,K0,k0,null),r>>=1,i>>=1}}else if(I0.length>0){if(_&&G0){let r=e0(I0[0]);Q.texStorage2D(J.TEXTURE_2D,J0,F0,r.width,r.height)}for(let r=0,i=I0.length;r<i;r++)if(U0=I0[r],_){if(C)Q.texSubImage2D(J.TEXTURE_2D,r,0,0,K0,k0,U0)}else Q.texImage2D(J.TEXTURE_2D,r,F0,K0,k0,U0);O.generateMipmaps=!1}else if(_){if(G0){let r=e0(a);Q.texStorage2D(J.TEXTURE_2D,J0,F0,r.width,r.height)}if(C)Q.texSubImage2D(J.TEXTURE_2D,0,0,0,K0,k0,a)}else Q.texImage2D(J.TEXTURE_2D,0,F0,K0,k0,a);if(E(O))q(x);if(R0.__version=c.version,O.onUpdate)O.onUpdate(O)}B.__version=O.version}function h(B,O,j){if(O.image.length!==6)return;let x=a0(B,O),e=O.source;Q.bindTexture(J.TEXTURE_CUBE_MAP,B.__webglTexture,J.TEXTURE0+j);let c=Z.get(e);if(e.version!==c.__version||x===!0){Q.activeTexture(J.TEXTURE0+j);let R0=s0.getPrimaries(s0.workingColorSpace),X0=O.colorSpace===gJ?null:s0.getPrimaries(O.colorSpace),z0=O.colorSpace===gJ||R0===X0?J.NONE:J.BROWSER_DEFAULT_WEBGL;J.pixelStorei(J.UNPACK_FLIP_Y_WEBGL,O.flipY),J.pixelStorei(J.UNPACK_PREMULTIPLY_ALPHA_WEBGL,O.premultiplyAlpha),J.pixelStorei(J.UNPACK_ALIGNMENT,O.unpackAlignment),J.pixelStorei(J.UNPACK_COLORSPACE_CONVERSION_WEBGL,z0);let T0=O.isCompressedTexture||O.image[0].isCompressedTexture,a=O.image[0]&&O.image[0].isDataTexture,K0=[];for(let i=0;i<6;i++){if(!T0&&!a)K0[i]=L(O.image[i],!0,W.maxCubemapSize);else K0[i]=a?O.image[i].image:O.image[i];K0[i]=D9(O,K0[i])}let k0=K0[0],F0=K.convert(O.format,O.colorSpace),U0=K.convert(O.type),I0=V(O.internalFormat,F0,U0,O.colorSpace),_=O.isVideoTexture!==!0,G0=c.__version===void 0||x===!0,C=e.dataReady,J0=f(O,k0);g0(J.TEXTURE_CUBE_MAP,O);let r;if(T0){if(_&&G0)Q.texStorage2D(J.TEXTURE_CUBE_MAP,J0,I0,k0.width,k0.height);for(let i=0;i<6;i++){r=K0[i].mipmaps;for(let M0=0;M0<r.length;M0++){let f0=r[M0];if(O.format!==ZJ)if(F0!==null)if(_){if(C)Q.compressedTexSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0,0,0,f0.width,f0.height,F0,f0.data)}else Q.compressedTexImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0,I0,f0.width,f0.height,0,f0.data);else console.warn("THREE.WebGLRenderer: Attempt to load unsupported compressed texture format in .setTextureCube()");else if(_){if(C)Q.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0,0,0,f0.width,f0.height,F0,U0,f0.data)}else Q.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0,I0,f0.width,f0.height,0,F0,U0,f0.data)}}}else{if(r=O.mipmaps,_&&G0){if(r.length>0)J0++;let i=e0(K0[0]);Q.texStorage2D(J.TEXTURE_CUBE_MAP,J0,I0,i.width,i.height)}for(let i=0;i<6;i++)if(a){if(_){if(C)Q.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,0,0,0,K0[i].width,K0[i].height,F0,U0,K0[i].data)}else Q.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,0,I0,K0[i].width,K0[i].height,0,F0,U0,K0[i].data);for(let M0=0;M0<r.length;M0++){let r0=r[M0].image[i].image;if(_){if(C)Q.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0+1,0,0,r0.width,r0.height,F0,U0,r0.data)}else Q.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0+1,I0,r0.width,r0.height,0,F0,U0,r0.data)}}else{if(_){if(C)Q.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,0,0,0,F0,U0,K0[i])}else Q.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,0,I0,F0,U0,K0[i]);for(let M0=0;M0<r.length;M0++){let f0=r[M0];if(_){if(C)Q.texSubImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0+1,0,0,F0,U0,f0.image[i])}else Q.texImage2D(J.TEXTURE_CUBE_MAP_POSITIVE_X+i,M0+1,I0,F0,U0,f0.image[i])}}}if(E(O))q(J.TEXTURE_CUBE_MAP);if(c.__version=e.version,O.onUpdate)O.onUpdate(O)}B.__version=O.version}function t(B,O,j,x,e,c){let R0=K.convert(j.format,j.colorSpace),X0=K.convert(j.type),z0=V(j.internalFormat,R0,X0,j.colorSpace),T0=Z.get(O),a=Z.get(j);if(a.__renderTarget=O,!T0.__hasExternalTextures){let K0=Math.max(1,O.width>>c),k0=Math.max(1,O.height>>c);if(e===J.TEXTURE_3D||e===J.TEXTURE_2D_ARRAY)Q.texImage3D(e,c,z0,K0,k0,O.depth,0,R0,X0,null);else Q.texImage2D(e,c,z0,K0,k0,0,R0,X0,null)}if(Q.bindFramebuffer(J.FRAMEBUFFER,B),C0(O))Y.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,x,e,a.__webglTexture,0,w0(O));else if(e===J.TEXTURE_2D||e>=J.TEXTURE_CUBE_MAP_POSITIVE_X&&e<=J.TEXTURE_CUBE_MAP_NEGATIVE_Z)J.framebufferTexture2D(J.FRAMEBUFFER,x,e,a.__webglTexture,c);Q.bindFramebuffer(J.FRAMEBUFFER,null)}function Q0(B,O,j){if(J.bindRenderbuffer(J.RENDERBUFFER,B),O.depthBuffer){let x=O.depthTexture,e=x&&x.isDepthTexture?x.type:null,c=I(O.stencilBuffer,e),R0=O.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,X0=w0(O);if(C0(O))Y.renderbufferStorageMultisampleEXT(J.RENDERBUFFER,X0,c,O.width,O.height);else if(j)J.renderbufferStorageMultisample(J.RENDERBUFFER,X0,c,O.width,O.height);else J.renderbufferStorage(J.RENDERBUFFER,c,O.width,O.height);J.framebufferRenderbuffer(J.FRAMEBUFFER,R0,J.RENDERBUFFER,B)}else{let x=O.textures;for(let e=0;e<x.length;e++){let c=x[e],R0=K.convert(c.format,c.colorSpace),X0=K.convert(c.type),z0=V(c.internalFormat,R0,X0,c.colorSpace),T0=w0(O);if(j&&C0(O)===!1)J.renderbufferStorageMultisample(J.RENDERBUFFER,T0,z0,O.width,O.height);else if(C0(O))Y.renderbufferStorageMultisampleEXT(J.RENDERBUFFER,T0,z0,O.width,O.height);else J.renderbufferStorage(J.RENDERBUFFER,z0,O.width,O.height)}}J.bindRenderbuffer(J.RENDERBUFFER,null)}function E0(B,O){if(O&&O.isWebGLCubeRenderTarget)throw Error("Depth Texture with cube render targets is not supported");if(Q.bindFramebuffer(J.FRAMEBUFFER,B),!(O.depthTexture&&O.depthTexture.isDepthTexture))throw Error("renderTarget.depthTexture must be an instance of THREE.DepthTexture");let x=Z.get(O.depthTexture);if(x.__renderTarget=O,!x.__webglTexture||O.depthTexture.image.width!==O.width||O.depthTexture.image.height!==O.height)O.depthTexture.image.width=O.width,O.depthTexture.image.height=O.height,O.depthTexture.needsUpdate=!0;Z0(O.depthTexture,0);let e=x.__webglTexture,c=w0(O);if(O.depthTexture.format===b6)if(C0(O))Y.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,J.DEPTH_ATTACHMENT,J.TEXTURE_2D,e,0,c);else J.framebufferTexture2D(J.FRAMEBUFFER,J.DEPTH_ATTACHMENT,J.TEXTURE_2D,e,0);else if(O.depthTexture.format===m8)if(C0(O))Y.framebufferTexture2DMultisampleEXT(J.FRAMEBUFFER,J.DEPTH_STENCIL_ATTACHMENT,J.TEXTURE_2D,e,0,c);else J.framebufferTexture2D(J.FRAMEBUFFER,J.DEPTH_STENCIL_ATTACHMENT,J.TEXTURE_2D,e,0);else throw Error("Unknown depthTexture format")}function l0(B){let O=Z.get(B),j=B.isWebGLCubeRenderTarget===!0;if(O.__boundDepthTexture!==B.depthTexture){let x=B.depthTexture;if(O.__depthDisposeCallback)O.__depthDisposeCallback();if(x){let e=()=>{delete O.__boundDepthTexture,delete O.__depthDisposeCallback,x.removeEventListener("dispose",e)};x.addEventListener("dispose",e),O.__depthDisposeCallback=e}O.__boundDepthTexture=x}if(B.depthTexture&&!O.__autoAllocateDepthBuffer){if(j)throw Error("target.depthTexture not supported in Cube render targets");let x=B.texture.mipmaps;if(x&&x.length>0)E0(O.__webglFramebuffer[0],B);else E0(O.__webglFramebuffer,B)}else if(j){O.__webglDepthbuffer=[];for(let x=0;x<6;x++)if(Q.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer[x]),O.__webglDepthbuffer[x]===void 0)O.__webglDepthbuffer[x]=J.createRenderbuffer(),Q0(O.__webglDepthbuffer[x],B,!1);else{let e=B.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,c=O.__webglDepthbuffer[x];J.bindRenderbuffer(J.RENDERBUFFER,c),J.framebufferRenderbuffer(J.FRAMEBUFFER,e,J.RENDERBUFFER,c)}}else{let x=B.texture.mipmaps;if(x&&x.length>0)Q.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer[0]);else Q.bindFramebuffer(J.FRAMEBUFFER,O.__webglFramebuffer);if(O.__webglDepthbuffer===void 0)O.__webglDepthbuffer=J.createRenderbuffer(),Q0(O.__webglDepthbuffer,B,!1);else{let e=B.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,c=O.__webglDepthbuffer;J.bindRenderbuffer(J.RENDERBUFFER,c),J.framebufferRenderbuffer(J.FRAMEBUFFER,e,J.RENDERBUFFER,c)}}Q.bindFramebuffer(J.FRAMEBUFFER,null)}function w(B,O,j){let x=Z.get(B);if(O!==void 0)t(x.__webglFramebuffer,B,B.texture,J.COLOR_ATTACHMENT0,J.TEXTURE_2D,0);if(j!==void 0)l0(B)}function v0(B){let O=B.texture,j=Z.get(B),x=Z.get(O);B.addEventListener("dispose",S);let e=B.textures,c=B.isWebGLCubeRenderTarget===!0,R0=e.length>1;if(!R0){if(x.__webglTexture===void 0)x.__webglTexture=J.createTexture();x.__version=O.version,H.memory.textures++}if(c){j.__webglFramebuffer=[];for(let X0=0;X0<6;X0++)if(O.mipmaps&&O.mipmaps.length>0){j.__webglFramebuffer[X0]=[];for(let z0=0;z0<O.mipmaps.length;z0++)j.__webglFramebuffer[X0][z0]=J.createFramebuffer()}else j.__webglFramebuffer[X0]=J.createFramebuffer()}else{if(O.mipmaps&&O.mipmaps.length>0){j.__webglFramebuffer=[];for(let X0=0;X0<O.mipmaps.length;X0++)j.__webglFramebuffer[X0]=J.createFramebuffer()}else j.__webglFramebuffer=J.createFramebuffer();if(R0)for(let X0=0,z0=e.length;X0<z0;X0++){let T0=Z.get(e[X0]);if(T0.__webglTexture===void 0)T0.__webglTexture=J.createTexture(),H.memory.textures++}if(B.samples>0&&C0(B)===!1){j.__webglMultisampledFramebuffer=J.createFramebuffer(),j.__webglColorRenderbuffer=[],Q.bindFramebuffer(J.FRAMEBUFFER,j.__webglMultisampledFramebuffer);for(let X0=0;X0<e.length;X0++){let z0=e[X0];j.__webglColorRenderbuffer[X0]=J.createRenderbuffer(),J.bindRenderbuffer(J.RENDERBUFFER,j.__webglColorRenderbuffer[X0]);let T0=K.convert(z0.format,z0.colorSpace),a=K.convert(z0.type),K0=V(z0.internalFormat,T0,a,z0.colorSpace,B.isXRRenderTarget===!0),k0=w0(B);J.renderbufferStorageMultisample(J.RENDERBUFFER,k0,K0,B.width,B.height),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+X0,J.RENDERBUFFER,j.__webglColorRenderbuffer[X0])}if(J.bindRenderbuffer(J.RENDERBUFFER,null),B.depthBuffer)j.__webglDepthRenderbuffer=J.createRenderbuffer(),Q0(j.__webglDepthRenderbuffer,B,!0);Q.bindFramebuffer(J.FRAMEBUFFER,null)}}if(c){Q.bindTexture(J.TEXTURE_CUBE_MAP,x.__webglTexture),g0(J.TEXTURE_CUBE_MAP,O);for(let X0=0;X0<6;X0++)if(O.mipmaps&&O.mipmaps.length>0)for(let z0=0;z0<O.mipmaps.length;z0++)t(j.__webglFramebuffer[X0][z0],B,O,J.COLOR_ATTACHMENT0,J.TEXTURE_CUBE_MAP_POSITIVE_X+X0,z0);else t(j.__webglFramebuffer[X0],B,O,J.COLOR_ATTACHMENT0,J.TEXTURE_CUBE_MAP_POSITIVE_X+X0,0);if(E(O))q(J.TEXTURE_CUBE_MAP);Q.unbindTexture()}else if(R0){for(let X0=0,z0=e.length;X0<z0;X0++){let T0=e[X0],a=Z.get(T0),K0=J.TEXTURE_2D;if(B.isWebGL3DRenderTarget||B.isWebGLArrayRenderTarget)K0=B.isWebGL3DRenderTarget?J.TEXTURE_3D:J.TEXTURE_2D_ARRAY;if(Q.bindTexture(K0,a.__webglTexture),g0(K0,T0),t(j.__webglFramebuffer,B,T0,J.COLOR_ATTACHMENT0+X0,K0,0),E(T0))q(K0)}Q.unbindTexture()}else{let X0=J.TEXTURE_2D;if(B.isWebGL3DRenderTarget||B.isWebGLArrayRenderTarget)X0=B.isWebGL3DRenderTarget?J.TEXTURE_3D:J.TEXTURE_2D_ARRAY;if(Q.bindTexture(X0,x.__webglTexture),g0(X0,O),O.mipmaps&&O.mipmaps.length>0)for(let z0=0;z0<O.mipmaps.length;z0++)t(j.__webglFramebuffer[z0],B,O,J.COLOR_ATTACHMENT0,X0,z0);else t(j.__webglFramebuffer,B,O,J.COLOR_ATTACHMENT0,X0,0);if(E(O))q(X0);Q.unbindTexture()}if(B.depthBuffer)l0(B)}function S0(B){let O=B.textures;for(let j=0,x=O.length;j<x;j++){let e=O[j];if(E(e)){let c=A(B),R0=Z.get(e).__webglTexture;Q.bindTexture(c,R0),q(c),Q.unbindTexture()}}}let A0=[],L0=[];function i0(B){if(B.samples>0){if(C0(B)===!1){let{textures:O,width:j,height:x}=B,e=J.COLOR_BUFFER_BIT,c=B.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT,R0=Z.get(B),X0=O.length>1;if(X0)for(let T0=0;T0<O.length;T0++)Q.bindFramebuffer(J.FRAMEBUFFER,R0.__webglMultisampledFramebuffer),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+T0,J.RENDERBUFFER,null),Q.bindFramebuffer(J.FRAMEBUFFER,R0.__webglFramebuffer),J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0+T0,J.TEXTURE_2D,null,0);Q.bindFramebuffer(J.READ_FRAMEBUFFER,R0.__webglMultisampledFramebuffer);let z0=B.texture.mipmaps;if(z0&&z0.length>0)Q.bindFramebuffer(J.DRAW_FRAMEBUFFER,R0.__webglFramebuffer[0]);else Q.bindFramebuffer(J.DRAW_FRAMEBUFFER,R0.__webglFramebuffer);for(let T0=0;T0<O.length;T0++){if(B.resolveDepthBuffer){if(B.depthBuffer)e|=J.DEPTH_BUFFER_BIT;if(B.stencilBuffer&&B.resolveStencilBuffer)e|=J.STENCIL_BUFFER_BIT}if(X0){J.framebufferRenderbuffer(J.READ_FRAMEBUFFER,J.COLOR_ATTACHMENT0,J.RENDERBUFFER,R0.__webglColorRenderbuffer[T0]);let a=Z.get(O[T0]).__webglTexture;J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0,J.TEXTURE_2D,a,0)}if(J.blitFramebuffer(0,0,j,x,0,0,j,x,e,J.NEAREST),X===!0){if(A0.length=0,L0.length=0,A0.push(J.COLOR_ATTACHMENT0+T0),B.depthBuffer&&B.resolveDepthBuffer===!1)A0.push(c),L0.push(c),J.invalidateFramebuffer(J.DRAW_FRAMEBUFFER,L0);J.invalidateFramebuffer(J.READ_FRAMEBUFFER,A0)}}if(Q.bindFramebuffer(J.READ_FRAMEBUFFER,null),Q.bindFramebuffer(J.DRAW_FRAMEBUFFER,null),X0)for(let T0=0;T0<O.length;T0++){Q.bindFramebuffer(J.FRAMEBUFFER,R0.__webglMultisampledFramebuffer),J.framebufferRenderbuffer(J.FRAMEBUFFER,J.COLOR_ATTACHMENT0+T0,J.RENDERBUFFER,R0.__webglColorRenderbuffer[T0]);let a=Z.get(O[T0]).__webglTexture;Q.bindFramebuffer(J.FRAMEBUFFER,R0.__webglFramebuffer),J.framebufferTexture2D(J.DRAW_FRAMEBUFFER,J.COLOR_ATTACHMENT0+T0,J.TEXTURE_2D,a,0)}Q.bindFramebuffer(J.DRAW_FRAMEBUFFER,R0.__webglMultisampledFramebuffer)}else if(B.depthBuffer&&B.resolveDepthBuffer===!1&&X){let O=B.stencilBuffer?J.DEPTH_STENCIL_ATTACHMENT:J.DEPTH_ATTACHMENT;J.invalidateFramebuffer(J.DRAW_FRAMEBUFFER,[O])}}}function w0(B){return Math.min(W.maxSamples,B.samples)}function C0(B){let O=Z.get(B);return B.samples>0&&$.has("WEBGL_multisampled_render_to_texture")===!0&&O.__useRenderToTexture!==!1}function H9(B){let O=H.render.frame;if(U.get(B)!==O)U.set(B,O),B.update()}function D9(B,O){let{colorSpace:j,format:x,type:e}=B;if(B.isCompressedTexture===!0||B.isVideoTexture===!0)return O;if(j!==u8&&j!==gJ)if(s0.getTransfer(j)===K9){if(x!==ZJ||e!==PJ)console.warn("THREE.WebGLTextures: sRGB encoded textures have to use RGBAFormat and UnsignedByteType.")}else console.error("THREE.WebGLTextures: Unsupported texture color space:",j);return O}function e0(B){if(typeof HTMLImageElement<"u"&&B instanceof HTMLImageElement)N.width=B.naturalWidth||B.width,N.height=B.naturalHeight||B.height;else if(typeof VideoFrame<"u"&&B instanceof VideoFrame)N.width=B.displayWidth,N.height=B.displayHeight;else N.width=B.width,N.height=B.height;return N}this.allocateTextureUnit=s,this.resetTextureUnits=n,this.setTexture2D=Z0,this.setTexture2DArray=m,this.setTexture3D=W0,this.setTextureCube=u,this.rebindTextures=w,this.setupRenderTarget=v0,this.updateRenderTargetMipmap=S0,this.updateMultisampleRenderTarget=i0,this.setupDepthRenderbuffer=l0,this.setupFrameBufferTexture=t,this.useMultisampledRTT=C0}function K1(J,$){function Q(Z,W=gJ){let K,H=s0.getTransfer(W);if(Z===PJ)return J.UNSIGNED_BYTE;if(Z===l7)return J.UNSIGNED_SHORT_4_4_4_4;if(Z===d7)return J.UNSIGNED_SHORT_5_5_5_1;if(Z===_Z)return J.UNSIGNED_INT_5_9_9_9_REV;if(Z===IZ)return J.UNSIGNED_INT_10F_11F_11F_REV;if(Z===wZ)return J.BYTE;if(Z===CZ)return J.SHORT;if(Z===l8)return J.UNSIGNED_SHORT;if(Z===p7)return J.INT;if(Z===F8)return J.UNSIGNED_INT;if(Z===AJ)return J.FLOAT;if(Z===d8)return J.HALF_FLOAT;if(Z===PZ)return J.ALPHA;if(Z===AZ)return J.RGB;if(Z===ZJ)return J.RGBA;if(Z===b6)return J.DEPTH_COMPONENT;if(Z===m8)return J.DEPTH_STENCIL;if(Z===TZ)return J.RED;if(Z===m7)return J.RED_INTEGER;if(Z===SZ)return J.RG;if(Z===u7)return J.RG_INTEGER;if(Z===c7)return J.RGBA_INTEGER;if(Z===f6||Z===v6||Z===h6||Z===x6)if(H===K9)if(K=$.get("WEBGL_compressed_texture_s3tc_srgb"),K!==null){if(Z===f6)return K.COMPRESSED_SRGB_S3TC_DXT1_EXT;if(Z===v6)return K.COMPRESSED_SRGB_ALPHA_S3TC_DXT1_EXT;if(Z===h6)return K.COMPRESSED_SRGB_ALPHA_S3TC_DXT3_EXT;if(Z===x6)return K.COMPRESSED_SRGB_ALPHA_S3TC_DXT5_EXT}else return null;else if(K=$.get("WEBGL_compressed_texture_s3tc"),K!==null){if(Z===f6)return K.COMPRESSED_RGB_S3TC_DXT1_EXT;if(Z===v6)return K.COMPRESSED_RGBA_S3TC_DXT1_EXT;if(Z===h6)return K.COMPRESSED_RGBA_S3TC_DXT3_EXT;if(Z===x6)return K.COMPRESSED_RGBA_S3TC_DXT5_EXT}else return null;if(Z===n7||Z===s7||Z===i7||Z===o7)if(K=$.get("WEBGL_compressed_texture_pvrtc"),K!==null){if(Z===n7)return K.COMPRESSED_RGB_PVRTC_4BPPV1_IMG;if(Z===s7)return K.COMPRESSED_RGB_PVRTC_2BPPV1_IMG;if(Z===i7)return K.COMPRESSED_RGBA_PVRTC_4BPPV1_IMG;if(Z===o7)return K.COMPRESSED_RGBA_PVRTC_2BPPV1_IMG}else return null;if(Z===a7||Z===r7||Z===t7)if(K=$.get("WEBGL_compressed_texture_etc"),K!==null){if(Z===a7||Z===r7)return H===K9?K.COMPRESSED_SRGB8_ETC2:K.COMPRESSED_RGB8_ETC2;if(Z===t7)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ETC2_EAC:K.COMPRESSED_RGBA8_ETC2_EAC}else return null;if(Z===e7||Z===J$||Z===$$||Z===Q$||Z===Z$||Z===W$||Z===K$||Z===Y$||Z===H$||Z===X$||Z===N$||Z===U$||Z===G$||Z===E$)if(K=$.get("WEBGL_compressed_texture_astc"),K!==null){if(Z===e7)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_4x4_KHR:K.COMPRESSED_RGBA_ASTC_4x4_KHR;if(Z===J$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_5x4_KHR:K.COMPRESSED_RGBA_ASTC_5x4_KHR;if(Z===$$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_5x5_KHR:K.COMPRESSED_RGBA_ASTC_5x5_KHR;if(Z===Q$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_6x5_KHR:K.COMPRESSED_RGBA_ASTC_6x5_KHR;if(Z===Z$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_6x6_KHR:K.COMPRESSED_RGBA_ASTC_6x6_KHR;if(Z===W$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_8x5_KHR:K.COMPRESSED_RGBA_ASTC_8x5_KHR;if(Z===K$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_8x6_KHR:K.COMPRESSED_RGBA_ASTC_8x6_KHR;if(Z===Y$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_8x8_KHR:K.COMPRESSED_RGBA_ASTC_8x8_KHR;if(Z===H$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_10x5_KHR:K.COMPRESSED_RGBA_ASTC_10x5_KHR;if(Z===X$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_10x6_KHR:K.COMPRESSED_RGBA_ASTC_10x6_KHR;if(Z===N$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_10x8_KHR:K.COMPRESSED_RGBA_ASTC_10x8_KHR;if(Z===U$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_10x10_KHR:K.COMPRESSED_RGBA_ASTC_10x10_KHR;if(Z===G$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_12x10_KHR:K.COMPRESSED_RGBA_ASTC_12x10_KHR;if(Z===E$)return H===K9?K.COMPRESSED_SRGB8_ALPHA8_ASTC_12x12_KHR:K.COMPRESSED_RGBA_ASTC_12x12_KHR}else return null;if(Z===D$||Z===q$||Z===O$)if(K=$.get("EXT_texture_compression_bptc"),K!==null){if(Z===D$)return H===K9?K.COMPRESSED_SRGB_ALPHA_BPTC_UNORM_EXT:K.COMPRESSED_RGBA_BPTC_UNORM_EXT;if(Z===q$)return K.COMPRESSED_RGB_BPTC_SIGNED_FLOAT_EXT;if(Z===O$)return K.COMPRESSED_RGB_BPTC_UNSIGNED_FLOAT_EXT}else return null;if(Z===R$||Z===k$||Z===F$||Z===M$)if(K=$.get("EXT_texture_compression_rgtc"),K!==null){if(Z===R$)return K.COMPRESSED_RED_RGTC1_EXT;if(Z===k$)return K.COMPRESSED_SIGNED_RED_RGTC1_EXT;if(Z===F$)return K.COMPRESSED_RED_GREEN_RGTC2_EXT;if(Z===M$)return K.COMPRESSED_SIGNED_RED_GREEN_RGTC2_EXT}else return null;if(Z===M8)return J.UNSIGNED_INT_24_8;return J[Z]!==void 0?J[Z]:null}return{convert:Q}}var Y1=`
void main() {

	gl_Position = vec4( position, 1.0 );

}`,H1=`
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

}`;class TW{constructor(){this.texture=null,this.mesh=null,this.depthNear=0,this.depthFar=0}init(J,$){if(this.texture===null){let Q=new s6(J.texture);if(J.depthNear!==$.depthNear||J.depthFar!==$.depthFar)this.depthNear=J.depthNear,this.depthFar=J.depthFar;this.texture=Q}}getMesh(J){if(this.texture!==null){if(this.mesh===null){let $=J.cameras[0].viewport,Q=new KJ({vertexShader:Y1,fragmentShader:H1,uniforms:{depthColor:{value:this.texture},depthWidth:{value:$.z},depthHeight:{value:$.w}}});this.mesh=new A9(new a8(20,20),Q)}}return this.mesh}reset(){this.texture=null,this.mesh=null}getDepthTexture(){return this.texture}}class SW extends kJ{constructor(J,$){super();let Q=this,Z=null,W=1,K=null,H="local-floor",Y=1,X=null,N=null,U=null,G=null,D=null,R=null,M=typeof XRWebGLBinding<"u",L=new TW,E={},q=$.getContextAttributes(),A=null,V=null,I=[],f=[],P=new x0,S=null,g=new I9;g.viewport=new G9;let z=new I9;z.viewport=new G9;let F=[g,z],T=new u$,n=null,s=null;this.cameraAutoUpdate=!0,this.enabled=!1,this.isPresenting=!1,this.getController=function(o){let H0=I[o];if(H0===void 0)H0=new s8,I[o]=H0;return H0.getTargetRaySpace()},this.getControllerGrip=function(o){let H0=I[o];if(H0===void 0)H0=new s8,I[o]=H0;return H0.getGripSpace()},this.getHand=function(o){let H0=I[o];if(H0===void 0)H0=new s8,I[o]=H0;return H0.getHandSpace()};function d(o){let H0=f.indexOf(o.inputSource);if(H0===-1)return;let h=I[H0];if(h!==void 0)h.update(o.inputSource,o.frame,X||K),h.dispatchEvent({type:o.type,data:o.inputSource})}function Z0(){Z.removeEventListener("select",d),Z.removeEventListener("selectstart",d),Z.removeEventListener("selectend",d),Z.removeEventListener("squeeze",d),Z.removeEventListener("squeezestart",d),Z.removeEventListener("squeezeend",d),Z.removeEventListener("end",Z0),Z.removeEventListener("inputsourceschange",m);for(let o=0;o<I.length;o++){let H0=f[o];if(H0===null)continue;f[o]=null,I[o].disconnect(H0)}n=null,s=null,L.reset();for(let o in E)delete E[o];J.setRenderTarget(A),D=null,G=null,U=null,Z=null,V=null,m0.stop(),Q.isPresenting=!1,J.setPixelRatio(S),J.setSize(P.width,P.height,!1),Q.dispatchEvent({type:"sessionend"})}this.setFramebufferScaleFactor=function(o){if(W=o,Q.isPresenting===!0)console.warn("THREE.WebXRManager: Cannot change framebuffer scale while presenting.")},this.setReferenceSpaceType=function(o){if(H=o,Q.isPresenting===!0)console.warn("THREE.WebXRManager: Cannot change reference space type while presenting.")},this.getReferenceSpace=function(){return X||K},this.setReferenceSpace=function(o){X=o},this.getBaseLayer=function(){return G!==null?G:D},this.getBinding=function(){if(U===null&&M)U=new XRWebGLBinding(Z,$);return U},this.getFrame=function(){return R},this.getSession=function(){return Z},this.setSession=async function(o){if(Z=o,Z!==null){if(A=J.getRenderTarget(),Z.addEventListener("select",d),Z.addEventListener("selectstart",d),Z.addEventListener("selectend",d),Z.addEventListener("squeeze",d),Z.addEventListener("squeezestart",d),Z.addEventListener("squeezeend",d),Z.addEventListener("end",Z0),Z.addEventListener("inputsourceschange",m),q.xrCompatible!==!0)await $.makeXRCompatible();if(S=J.getPixelRatio(),J.getSize(P),!(M&&("createProjectionLayer"in XRWebGLBinding.prototype))){let h={antialias:q.antialias,alpha:!0,depth:q.depth,stencil:q.stencil,framebufferScaleFactor:W};D=new XRWebGLLayer(Z,$,h),Z.updateRenderState({baseLayer:D}),J.setPixelRatio(1),J.setSize(D.framebufferWidth,D.framebufferHeight,!1),V=new FJ(D.framebufferWidth,D.framebufferHeight,{format:ZJ,type:PJ,colorSpace:J.outputColorSpace,stencilBuffer:q.stencil,resolveDepthBuffer:D.ignoreDepthValues===!1,resolveStencilBuffer:D.ignoreDepthValues===!1})}else{let h=null,t=null,Q0=null;if(q.depth)Q0=q.stencil?$.DEPTH24_STENCIL8:$.DEPTH_COMPONENT24,h=q.stencil?m8:b6,t=q.stencil?M8:F8;let E0={colorFormat:$.RGBA8,depthFormat:Q0,scaleFactor:W};U=this.getBinding(),G=U.createProjectionLayer(E0),Z.updateRenderState({layers:[G]}),J.setPixelRatio(1),J.setSize(G.textureWidth,G.textureHeight,!1),V=new FJ(G.textureWidth,G.textureHeight,{format:ZJ,type:PJ,depthTexture:new n6(G.textureWidth,G.textureHeight,t,void 0,void 0,void 0,void 0,void 0,void 0,h),stencilBuffer:q.stencil,colorSpace:J.outputColorSpace,samples:q.antialias?4:0,resolveDepthBuffer:G.ignoreDepthValues===!1,resolveStencilBuffer:G.ignoreDepthValues===!1})}V.isXRRenderTarget=!0,this.setFoveation(Y),X=null,K=await Z.requestReferenceSpace(H),m0.setContext(Z),m0.start(),Q.isPresenting=!0,Q.dispatchEvent({type:"sessionstart"})}},this.getEnvironmentBlendMode=function(){if(Z!==null)return Z.environmentBlendMode},this.getDepthTexture=function(){return L.getDepthTexture()};function m(o){for(let H0=0;H0<o.removed.length;H0++){let h=o.removed[H0],t=f.indexOf(h);if(t>=0)f[t]=null,I[t].disconnect(h)}for(let H0=0;H0<o.added.length;H0++){let h=o.added[H0],t=f.indexOf(h);if(t===-1){for(let E0=0;E0<I.length;E0++)if(E0>=f.length){f.push(h),t=E0;break}else if(f[E0]===null){f[E0]=h,t=E0;break}if(t===-1)break}let Q0=I[t];if(Q0)Q0.connect(h)}}let W0=new v,u=new v;function N0(o,H0,h){W0.setFromMatrixPosition(H0.matrixWorld),u.setFromMatrixPosition(h.matrixWorld);let t=W0.distanceTo(u),Q0=H0.projectionMatrix.elements,E0=h.projectionMatrix.elements,l0=Q0[14]/(Q0[10]-1),w=Q0[14]/(Q0[10]+1),v0=(Q0[9]+1)/Q0[5],S0=(Q0[9]-1)/Q0[5],A0=(Q0[8]-1)/Q0[0],L0=(E0[8]+1)/E0[0],i0=l0*A0,w0=l0*L0,C0=t/(-A0+L0),H9=C0*-A0;if(H0.matrixWorld.decompose(o.position,o.quaternion,o.scale),o.translateX(H9),o.translateZ(C0),o.matrixWorld.compose(o.position,o.quaternion,o.scale),o.matrixWorldInverse.copy(o.matrixWorld).invert(),Q0[10]===-1)o.projectionMatrix.copy(H0.projectionMatrix),o.projectionMatrixInverse.copy(H0.projectionMatrixInverse);else{let D9=l0+C0,e0=w+C0,B=i0-H9,O=w0+(t-H9),j=v0*w/e0*D9,x=S0*w/e0*D9;o.projectionMatrix.makePerspective(B,O,j,x,D9,e0),o.projectionMatrixInverse.copy(o.projectionMatrix).invert()}}function q0(o,H0){if(H0===null)o.matrixWorld.copy(o.matrix);else o.matrixWorld.multiplyMatrices(H0.matrixWorld,o.matrix);o.matrixWorldInverse.copy(o.matrixWorld).invert()}this.updateCamera=function(o){if(Z===null)return;let{near:H0,far:h}=o;if(L.texture!==null){if(L.depthNear>0)H0=L.depthNear;if(L.depthFar>0)h=L.depthFar}if(T.near=z.near=g.near=H0,T.far=z.far=g.far=h,n!==T.near||s!==T.far)Z.updateRenderState({depthNear:T.near,depthFar:T.far}),n=T.near,s=T.far;T.layers.mask=o.layers.mask|6,g.layers.mask=T.layers.mask&3,z.layers.mask=T.layers.mask&5;let t=o.parent,Q0=T.cameras;q0(T,t);for(let E0=0;E0<Q0.length;E0++)q0(Q0[E0],t);if(Q0.length===2)N0(T,g,z);else T.projectionMatrix.copy(g.projectionMatrix);_0(o,T,t)};function _0(o,H0,h){if(h===null)o.matrix.copy(H0.matrixWorld);else o.matrix.copy(h.matrixWorld),o.matrix.invert(),o.matrix.multiply(H0.matrixWorld);if(o.matrix.decompose(o.position,o.quaternion,o.scale),o.updateMatrixWorld(!0),o.projectionMatrix.copy(H0.projectionMatrix),o.projectionMatrixInverse.copy(H0.projectionMatrixInverse),o.isPerspectiveCamera)o.fov=G8*2*Math.atan(1/o.projectionMatrix.elements[5]),o.zoom=1}this.getCamera=function(){return T},this.getFoveation=function(){if(G===null&&D===null)return;return Y},this.setFoveation=function(o){if(Y=o,G!==null)G.fixedFoveation=o;if(D!==null&&D.fixedFoveation!==void 0)D.fixedFoveation=o},this.hasDepthSensing=function(){return L.texture!==null},this.getDepthSensingMesh=function(){return L.getMesh(T)},this.getCameraTexture=function(o){return E[o]};let g0=null;function a0(o,H0){if(N=H0.getViewerPose(X||K),R=H0,N!==null){let h=N.views;if(D!==null)J.setRenderTargetFramebuffer(V,D.framebuffer),J.setRenderTarget(V);let t=!1;if(h.length!==T.cameras.length)T.cameras.length=0,t=!0;for(let w=0;w<h.length;w++){let v0=h[w],S0=null;if(D!==null)S0=D.getViewport(v0);else{let L0=U.getViewSubImage(G,v0);if(S0=L0.viewport,w===0)J.setRenderTargetTextures(V,L0.colorTexture,L0.depthStencilTexture),J.setRenderTarget(V)}let A0=F[w];if(A0===void 0)A0=new I9,A0.layers.enable(w),A0.viewport=new G9,F[w]=A0;if(A0.matrix.fromArray(v0.transform.matrix),A0.matrix.decompose(A0.position,A0.quaternion,A0.scale),A0.projectionMatrix.fromArray(v0.projectionMatrix),A0.projectionMatrixInverse.copy(A0.projectionMatrix).invert(),A0.viewport.set(S0.x,S0.y,S0.width,S0.height),w===0)T.matrix.copy(A0.matrix),T.matrix.decompose(T.position,T.quaternion,T.scale);if(t===!0)T.cameras.push(A0)}let Q0=Z.enabledFeatures;if(Q0&&Q0.includes("depth-sensing")&&Z.depthUsage=="gpu-optimized"&&M){U=Q.getBinding();let w=U.getDepthInformation(h[0]);if(w&&w.isValid&&w.texture)L.init(w,Z.renderState)}if(Q0&&Q0.includes("camera-access")&&M){J.state.unbindTexture(),U=Q.getBinding();for(let w=0;w<h.length;w++){let v0=h[w].camera;if(v0){let S0=E[v0];if(!S0)S0=new s6,E[v0]=S0;let A0=U.getCameraImage(v0);S0.sourceTexture=A0}}}}for(let h=0;h<I.length;h++){let t=f[h],Q0=I[h];if(t!==null&&Q0!==void 0)Q0.update(t,H0,X||K)}if(g0)g0(o,H0);if(H0.detectedPlanes)Q.dispatchEvent({type:"planesdetected",data:H0});R=null}let m0=new LW;m0.setAnimationLoop(a0),this.setAnimationLoop=function(o){g0=o},this.dispose=function(){}}}var nJ=new t9,X1=new X9;function N1(J,$){function Q(E,q){if(E.matrixAutoUpdate===!0)E.updateMatrix();q.value.copy(E.matrix)}function Z(E,q){if(q.color.getRGB(E.fogColor.value,T$(J)),q.isFog)E.fogNear.value=q.near,E.fogFar.value=q.far;else if(q.isFogExp2)E.fogDensity.value=q.density}function W(E,q,A,V,I){if(q.isMeshBasicMaterial)K(E,q);else if(q.isMeshLambertMaterial)K(E,q);else if(q.isMeshToonMaterial)K(E,q),G(E,q);else if(q.isMeshPhongMaterial)K(E,q),U(E,q);else if(q.isMeshStandardMaterial){if(K(E,q),D(E,q),q.isMeshPhysicalMaterial)R(E,q,I)}else if(q.isMeshMatcapMaterial)K(E,q),M(E,q);else if(q.isMeshDepthMaterial)K(E,q);else if(q.isMeshDistanceMaterial)K(E,q),L(E,q);else if(q.isMeshNormalMaterial)K(E,q);else if(q.isLineBasicMaterial){if(H(E,q),q.isLineDashedMaterial)Y(E,q)}else if(q.isPointsMaterial)X(E,q,A,V);else if(q.isSpriteMaterial)N(E,q);else if(q.isShadowMaterial)E.color.value.copy(q.color),E.opacity.value=q.opacity;else if(q.isShaderMaterial)q.uniformsNeedUpdate=!1}function K(E,q){if(E.opacity.value=q.opacity,q.color)E.diffuse.value.copy(q.color);if(q.emissive)E.emissive.value.copy(q.emissive).multiplyScalar(q.emissiveIntensity);if(q.map)E.map.value=q.map,Q(q.map,E.mapTransform);if(q.alphaMap)E.alphaMap.value=q.alphaMap,Q(q.alphaMap,E.alphaMapTransform);if(q.bumpMap){if(E.bumpMap.value=q.bumpMap,Q(q.bumpMap,E.bumpMapTransform),E.bumpScale.value=q.bumpScale,q.side===p9)E.bumpScale.value*=-1}if(q.normalMap){if(E.normalMap.value=q.normalMap,Q(q.normalMap,E.normalMapTransform),E.normalScale.value.copy(q.normalScale),q.side===p9)E.normalScale.value.negate()}if(q.displacementMap)E.displacementMap.value=q.displacementMap,Q(q.displacementMap,E.displacementMapTransform),E.displacementScale.value=q.displacementScale,E.displacementBias.value=q.displacementBias;if(q.emissiveMap)E.emissiveMap.value=q.emissiveMap,Q(q.emissiveMap,E.emissiveMapTransform);if(q.specularMap)E.specularMap.value=q.specularMap,Q(q.specularMap,E.specularMapTransform);if(q.alphaTest>0)E.alphaTest.value=q.alphaTest;let A=$.get(q),V=A.envMap,I=A.envMapRotation;if(V){if(E.envMap.value=V,nJ.copy(I),nJ.x*=-1,nJ.y*=-1,nJ.z*=-1,V.isCubeTexture&&V.isRenderTargetTexture===!1)nJ.y*=-1,nJ.z*=-1;E.envMapRotation.value.setFromMatrix4(X1.makeRotationFromEuler(nJ)),E.flipEnvMap.value=V.isCubeTexture&&V.isRenderTargetTexture===!1?-1:1,E.reflectivity.value=q.reflectivity,E.ior.value=q.ior,E.refractionRatio.value=q.refractionRatio}if(q.lightMap)E.lightMap.value=q.lightMap,E.lightMapIntensity.value=q.lightMapIntensity,Q(q.lightMap,E.lightMapTransform);if(q.aoMap)E.aoMap.value=q.aoMap,E.aoMapIntensity.value=q.aoMapIntensity,Q(q.aoMap,E.aoMapTransform)}function H(E,q){if(E.diffuse.value.copy(q.color),E.opacity.value=q.opacity,q.map)E.map.value=q.map,Q(q.map,E.mapTransform)}function Y(E,q){E.dashSize.value=q.dashSize,E.totalSize.value=q.dashSize+q.gapSize,E.scale.value=q.scale}function X(E,q,A,V){if(E.diffuse.value.copy(q.color),E.opacity.value=q.opacity,E.size.value=q.size*A,E.scale.value=V*0.5,q.map)E.map.value=q.map,Q(q.map,E.uvTransform);if(q.alphaMap)E.alphaMap.value=q.alphaMap,Q(q.alphaMap,E.alphaMapTransform);if(q.alphaTest>0)E.alphaTest.value=q.alphaTest}function N(E,q){if(E.diffuse.value.copy(q.color),E.opacity.value=q.opacity,E.rotation.value=q.rotation,q.map)E.map.value=q.map,Q(q.map,E.mapTransform);if(q.alphaMap)E.alphaMap.value=q.alphaMap,Q(q.alphaMap,E.alphaMapTransform);if(q.alphaTest>0)E.alphaTest.value=q.alphaTest}function U(E,q){E.specular.value.copy(q.specular),E.shininess.value=Math.max(q.shininess,0.0001)}function G(E,q){if(q.gradientMap)E.gradientMap.value=q.gradientMap}function D(E,q){if(E.metalness.value=q.metalness,q.metalnessMap)E.metalnessMap.value=q.metalnessMap,Q(q.metalnessMap,E.metalnessMapTransform);if(E.roughness.value=q.roughness,q.roughnessMap)E.roughnessMap.value=q.roughnessMap,Q(q.roughnessMap,E.roughnessMapTransform);if(q.envMap)E.envMapIntensity.value=q.envMapIntensity}function R(E,q,A){if(E.ior.value=q.ior,q.sheen>0){if(E.sheenColor.value.copy(q.sheenColor).multiplyScalar(q.sheen),E.sheenRoughness.value=q.sheenRoughness,q.sheenColorMap)E.sheenColorMap.value=q.sheenColorMap,Q(q.sheenColorMap,E.sheenColorMapTransform);if(q.sheenRoughnessMap)E.sheenRoughnessMap.value=q.sheenRoughnessMap,Q(q.sheenRoughnessMap,E.sheenRoughnessMapTransform)}if(q.clearcoat>0){if(E.clearcoat.value=q.clearcoat,E.clearcoatRoughness.value=q.clearcoatRoughness,q.clearcoatMap)E.clearcoatMap.value=q.clearcoatMap,Q(q.clearcoatMap,E.clearcoatMapTransform);if(q.clearcoatRoughnessMap)E.clearcoatRoughnessMap.value=q.clearcoatRoughnessMap,Q(q.clearcoatRoughnessMap,E.clearcoatRoughnessMapTransform);if(q.clearcoatNormalMap){if(E.clearcoatNormalMap.value=q.clearcoatNormalMap,Q(q.clearcoatNormalMap,E.clearcoatNormalMapTransform),E.clearcoatNormalScale.value.copy(q.clearcoatNormalScale),q.side===p9)E.clearcoatNormalScale.value.negate()}}if(q.dispersion>0)E.dispersion.value=q.dispersion;if(q.iridescence>0){if(E.iridescence.value=q.iridescence,E.iridescenceIOR.value=q.iridescenceIOR,E.iridescenceThicknessMinimum.value=q.iridescenceThicknessRange[0],E.iridescenceThicknessMaximum.value=q.iridescenceThicknessRange[1],q.iridescenceMap)E.iridescenceMap.value=q.iridescenceMap,Q(q.iridescenceMap,E.iridescenceMapTransform);if(q.iridescenceThicknessMap)E.iridescenceThicknessMap.value=q.iridescenceThicknessMap,Q(q.iridescenceThicknessMap,E.iridescenceThicknessMapTransform)}if(q.transmission>0){if(E.transmission.value=q.transmission,E.transmissionSamplerMap.value=A.texture,E.transmissionSamplerSize.value.set(A.width,A.height),q.transmissionMap)E.transmissionMap.value=q.transmissionMap,Q(q.transmissionMap,E.transmissionMapTransform);if(E.thickness.value=q.thickness,q.thicknessMap)E.thicknessMap.value=q.thicknessMap,Q(q.thicknessMap,E.thicknessMapTransform);E.attenuationDistance.value=q.attenuationDistance,E.attenuationColor.value.copy(q.attenuationColor)}if(q.anisotropy>0){if(E.anisotropyVector.value.set(q.anisotropy*Math.cos(q.anisotropyRotation),q.anisotropy*Math.sin(q.anisotropyRotation)),q.anisotropyMap)E.anisotropyMap.value=q.anisotropyMap,Q(q.anisotropyMap,E.anisotropyMapTransform)}if(E.specularIntensity.value=q.specularIntensity,E.specularColor.value.copy(q.specularColor),q.specularColorMap)E.specularColorMap.value=q.specularColorMap,Q(q.specularColorMap,E.specularColorMapTransform);if(q.specularIntensityMap)E.specularIntensityMap.value=q.specularIntensityMap,Q(q.specularIntensityMap,E.specularIntensityMapTransform)}function M(E,q){if(q.matcap)E.matcap.value=q.matcap}function L(E,q){let A=$.get(q).light;E.referencePosition.value.setFromMatrixPosition(A.matrixWorld),E.nearDistance.value=A.shadow.camera.near,E.farDistance.value=A.shadow.camera.far}return{refreshFogUniforms:Z,refreshMaterialUniforms:W}}function U1(J,$,Q,Z){let W={},K={},H=[],Y=J.getParameter(J.MAX_UNIFORM_BUFFER_BINDINGS);function X(A,V){let I=V.program;Z.uniformBlockBinding(A,I)}function N(A,V){let I=W[A.id];if(I===void 0)M(A),I=U(A),W[A.id]=I,A.addEventListener("dispose",E);let f=V.program;Z.updateUBOMapping(A,f);let P=$.render.frame;if(K[A.id]!==P)D(A),K[A.id]=P}function U(A){let V=G();A.__bindingPointIndex=V;let I=J.createBuffer(),f=A.__size,P=A.usage;return J.bindBuffer(J.UNIFORM_BUFFER,I),J.bufferData(J.UNIFORM_BUFFER,f,P),J.bindBuffer(J.UNIFORM_BUFFER,null),J.bindBufferBase(J.UNIFORM_BUFFER,V,I),I}function G(){for(let A=0;A<Y;A++)if(H.indexOf(A)===-1)return H.push(A),A;return console.error("THREE.WebGLRenderer: Maximum number of simultaneously usable uniforms groups reached."),0}function D(A){let V=W[A.id],I=A.uniforms,f=A.__cache;J.bindBuffer(J.UNIFORM_BUFFER,V);for(let P=0,S=I.length;P<S;P++){let g=Array.isArray(I[P])?I[P]:[I[P]];for(let z=0,F=g.length;z<F;z++){let T=g[z];if(R(T,P,z,f)===!0){let n=T.__offset,s=Array.isArray(T.value)?T.value:[T.value],d=0;for(let Z0=0;Z0<s.length;Z0++){let m=s[Z0],W0=L(m);if(typeof m==="number"||typeof m==="boolean")T.__data[0]=m,J.bufferSubData(J.UNIFORM_BUFFER,n+d,T.__data);else if(m.isMatrix3)T.__data[0]=m.elements[0],T.__data[1]=m.elements[1],T.__data[2]=m.elements[2],T.__data[3]=0,T.__data[4]=m.elements[3],T.__data[5]=m.elements[4],T.__data[6]=m.elements[5],T.__data[7]=0,T.__data[8]=m.elements[6],T.__data[9]=m.elements[7],T.__data[10]=m.elements[8],T.__data[11]=0;else m.toArray(T.__data,d),d+=W0.storage/Float32Array.BYTES_PER_ELEMENT}J.bufferSubData(J.UNIFORM_BUFFER,n,T.__data)}}}J.bindBuffer(J.UNIFORM_BUFFER,null)}function R(A,V,I,f){let P=A.value,S=V+"_"+I;if(f[S]===void 0){if(typeof P==="number"||typeof P==="boolean")f[S]=P;else f[S]=P.clone();return!0}else{let g=f[S];if(typeof P==="number"||typeof P==="boolean"){if(g!==P)return f[S]=P,!0}else if(g.equals(P)===!1)return g.copy(P),!0}return!1}function M(A){let V=A.uniforms,I=0,f=16;for(let S=0,g=V.length;S<g;S++){let z=Array.isArray(V[S])?V[S]:[V[S]];for(let F=0,T=z.length;F<T;F++){let n=z[F],s=Array.isArray(n.value)?n.value:[n.value];for(let d=0,Z0=s.length;d<Z0;d++){let m=s[d],W0=L(m),u=I%f,N0=u%W0.boundary,q0=u+N0;if(I+=N0,q0!==0&&f-q0<W0.storage)I+=f-q0;n.__data=new Float32Array(W0.storage/Float32Array.BYTES_PER_ELEMENT),n.__offset=I,I+=W0.storage}}}let P=I%f;if(P>0)I+=f-P;return A.__size=I,A.__cache={},this}function L(A){let V={boundary:0,storage:0};if(typeof A==="number"||typeof A==="boolean")V.boundary=4,V.storage=4;else if(A.isVector2)V.boundary=8,V.storage=8;else if(A.isVector3||A.isColor)V.boundary=16,V.storage=12;else if(A.isVector4)V.boundary=16,V.storage=16;else if(A.isMatrix3)V.boundary=48,V.storage=48;else if(A.isMatrix4)V.boundary=64,V.storage=64;else if(A.isTexture)console.warn("THREE.WebGLRenderer: Texture samplers can not be part of an uniforms group.");else console.warn("THREE.WebGLRenderer: Unsupported uniform value type.",A);return V}function E(A){let V=A.target;V.removeEventListener("dispose",E);let I=H.indexOf(V.__bindingPointIndex);H.splice(I,1),J.deleteBuffer(W[V.id]),delete W[V.id],delete K[V.id]}function q(){for(let A in W)J.deleteBuffer(W[A]);H=[],W={},K={}}return{bind:X,update:N,dispose:q}}class ZQ{constructor(J={}){let{canvas:$=mZ(),context:Q=null,depth:Z=!0,stencil:W=!1,alpha:K=!1,antialias:H=!1,premultipliedAlpha:Y=!0,preserveDrawingBuffer:X=!1,powerPreference:N="default",failIfMajorPerformanceCaveat:U=!1,reversedDepthBuffer:G=!1}=J;this.isWebGLRenderer=!0;let D;if(Q!==null){if(typeof WebGLRenderingContext<"u"&&Q instanceof WebGLRenderingContext)throw Error("THREE.WebGLRenderer: WebGL 1 is not supported since r163.");D=Q.getContextAttributes().alpha}else D=K;let R=new Uint32Array(4),M=new Int32Array(4),L=null,E=null,q=[],A=[];this.domElement=$,this.debug={checkShaderErrors:!0,onShaderError:null},this.autoClear=!0,this.autoClearColor=!0,this.autoClearDepth=!0,this.autoClearStencil=!0,this.sortObjects=!0,this.clippingPlanes=[],this.localClippingEnabled=!1,this.toneMapping=RJ,this.toneMappingExposure=1,this.transmissionResolutionScale=1;let V=this,I=!1;this._outputColorSpace=fZ;let f=0,P=0,S=null,g=-1,z=null,F=new G9,T=new G9,n=null,s=new n0(0),d=0,Z0=$.width,m=$.height,W0=1,u=null,N0=null,q0=new G9(0,0,Z0,m),_0=new G9(0,0,Z0,m),g0=!1,a0=new i8,m0=!1,o=!1,H0=new X9,h=new v,t=new G9,Q0={background:null,fog:null,environment:null,overrideMaterial:null,isScene:!0},E0=!1;function l0(){return S===null?W0:1}let w=Q;function v0(k,y){return $.getContext(k,y)}try{let k={alpha:!0,depth:Z,stencil:W,antialias:H,premultipliedAlpha:Y,preserveDrawingBuffer:X,powerPreference:N,failIfMajorPerformanceCaveat:U};if("setAttribute"in $)$.setAttribute("data-engine",`three.js r${pQ}`);if($.addEventListener("webglcontextlost",G0,!1),$.addEventListener("webglcontextrestored",C,!1),$.addEventListener("webglcontextcreationerror",J0,!1),w===null){if(w=v0("webgl2",k),w===null)if(v0("webgl2"))throw Error("Error creating WebGL context with your selected attributes.");else throw Error("Error creating WebGL context.")}}catch(k){throw console.error("THREE.WebGLRenderer: "+k.message),k}let S0,A0,L0,i0,w0,C0,H9,D9,e0,B,O,j,x,e,c,R0,X0,z0,T0,a,K0,k0,F0,U0;function I0(){if(S0=new PX(w),S0.init(),k0=new K1(w,S0),A0=new VX(w,S0,J,k0),L0=new Z1(w,S0),A0.reversedDepthBuffer&&G)L0.buffers.depth.setReversed(!0);i0=new SX(w),w0=new uN,C0=new W1(w,S0,L0,w0,A0,k0,i0),H9=new BX(V),D9=new IX(V),e0=new hK(w),F0=new MX(w,e0),B=new AX(w,e0,i0,F0),O=new yX(w,B,e0,i0),T0=new jX(w,A0,C0),R0=new zX(w0),j=new mN(V,H9,D9,S0,A0,F0,R0),x=new N1(V,w0),e=new nN,c=new tN(S0),z0=new FX(V,H9,D9,L0,O,D,Y),X0=new $1(V,O,A0),U0=new U1(w,i0,A0,L0),a=new LX(w,S0,i0),K0=new TX(w,S0,i0),i0.programs=j.programs,V.capabilities=A0,V.extensions=S0,V.properties=w0,V.renderLists=e,V.shadowMap=X0,V.state=L0,V.info=i0}I0();let _=new SW(V,w);this.xr=_,this.getContext=function(){return w},this.getContextAttributes=function(){return w.getContextAttributes()},this.forceContextLoss=function(){let k=S0.get("WEBGL_lose_context");if(k)k.loseContext()},this.forceContextRestore=function(){let k=S0.get("WEBGL_lose_context");if(k)k.restoreContext()},this.getPixelRatio=function(){return W0},this.setPixelRatio=function(k){if(k===void 0)return;W0=k,this.setSize(Z0,m,!1)},this.getSize=function(k){return k.set(Z0,m)},this.setSize=function(k,y,p=!0){if(_.isPresenting){console.warn("THREE.WebGLRenderer: Can't change size while VR device is presenting.");return}if(Z0=k,m=y,$.width=Math.floor(k*W0),$.height=Math.floor(y*W0),p===!0)$.style.width=k+"px",$.style.height=y+"px";this.setViewport(0,0,k,y)},this.getDrawingBufferSize=function(k){return k.set(Z0*W0,m*W0).floor()},this.setDrawingBufferSize=function(k,y,p){Z0=k,m=y,W0=p,$.width=Math.floor(k*p),$.height=Math.floor(y*p),this.setViewport(0,0,k,y)},this.getCurrentViewport=function(k){return k.copy(F)},this.getViewport=function(k){return k.copy(q0)},this.setViewport=function(k,y,p,l){if(k.isVector4)q0.set(k.x,k.y,k.z,k.w);else q0.set(k,y,p,l);L0.viewport(F.copy(q0).multiplyScalar(W0).round())},this.getScissor=function(k){return k.copy(_0)},this.setScissor=function(k,y,p,l){if(k.isVector4)_0.set(k.x,k.y,k.z,k.w);else _0.set(k,y,p,l);L0.scissor(T.copy(_0).multiplyScalar(W0).round())},this.getScissorTest=function(){return g0},this.setScissorTest=function(k){L0.setScissorTest(g0=k)},this.setOpaqueSort=function(k){u=k},this.setTransparentSort=function(k){N0=k},this.getClearColor=function(k){return k.copy(z0.getClearColor())},this.setClearColor=function(){z0.setClearColor(...arguments)},this.getClearAlpha=function(){return z0.getClearAlpha()},this.setClearAlpha=function(){z0.setClearAlpha(...arguments)},this.clear=function(k=!0,y=!0,p=!0){let l=0;if(k){let b=!1;if(S!==null){let Y0=S.texture.format;b=Y0===c7||Y0===u7||Y0===m7}if(b){let Y0=S.texture.type,O0=Y0===PJ||Y0===F8||Y0===l8||Y0===M8||Y0===l7||Y0===d7,B0=z0.getClearColor(),V0=z0.getClearAlpha(),y0=B0.r,h0=B0.g,P0=B0.b;if(O0)R[0]=y0,R[1]=h0,R[2]=P0,R[3]=V0,w.clearBufferuiv(w.COLOR,0,R);else M[0]=y0,M[1]=h0,M[2]=P0,M[3]=V0,w.clearBufferiv(w.COLOR,0,M)}else l|=w.COLOR_BUFFER_BIT}if(y)l|=w.DEPTH_BUFFER_BIT;if(p)l|=w.STENCIL_BUFFER_BIT,this.state.buffers.stencil.setMask(4294967295);w.clear(l)},this.clearColor=function(){this.clear(!0,!1,!1)},this.clearDepth=function(){this.clear(!1,!0,!1)},this.clearStencil=function(){this.clear(!1,!1,!0)},this.dispose=function(){$.removeEventListener("webglcontextlost",G0,!1),$.removeEventListener("webglcontextrestored",C,!1),$.removeEventListener("webglcontextcreationerror",J0,!1),z0.dispose(),e.dispose(),c.dispose(),w0.dispose(),H9.dispose(),D9.dispose(),O.dispose(),F0.dispose(),U0.dispose(),j.dispose(),_.dispose(),_.removeEventListener("sessionstart",JJ),_.removeEventListener("sessionend",$J),SJ.stop()};function G0(k){k.preventDefault(),console.log("THREE.WebGLRenderer: Context Lost."),I=!0}function C(){console.log("THREE.WebGLRenderer: Context Restored."),I=!1;let k=i0.autoReset,y=X0.enabled,p=X0.autoUpdate,l=X0.needsUpdate,b=X0.type;I0(),i0.autoReset=k,X0.enabled=y,X0.autoUpdate=p,X0.needsUpdate=l,X0.type=b}function J0(k){console.error("THREE.WebGLRenderer: A WebGL context could not be created. Reason: ",k.statusMessage)}function r(k){let y=k.target;y.removeEventListener("dispose",r),i(y)}function i(k){M0(k),w0.remove(k)}function M0(k){let y=w0.get(k).programs;if(y!==void 0){if(y.forEach(function(p){j.releaseProgram(p)}),k.isShaderMaterial)j.releaseShaderCache(k)}}this.renderBufferDirect=function(k,y,p,l,b,Y0){if(y===null)y=Q0;let O0=b.isMesh&&b.matrixWorld.determinant()<0,B0=xW(k,y,p,l,b);L0.setMaterial(l,O0);let V0=p.index,y0=1;if(l.wireframe===!0){if(V0=B.getWireframeAttribute(p),V0===void 0)return;y0=2}let h0=p.drawRange,P0=p.attributes.position,c0=h0.start*y0,$9=(h0.start+h0.count)*y0;if(Y0!==null)c0=Math.max(c0,Y0.start*y0),$9=Math.min($9,(Y0.start+Y0.count)*y0);if(V0!==null)c0=Math.max(c0,0),$9=Math.min($9,V0.count);else if(P0!==void 0&&P0!==null)c0=Math.max(c0,0),$9=Math.min($9,P0.count);let q9=$9-c0;if(q9<0||q9===1/0)return;F0.setup(b,l,B0,p,V0);let Y9,W9=a;if(V0!==null)Y9=e0.get(V0),W9=K0,W9.setIndex(Y9);if(b.isMesh)if(l.wireframe===!0)L0.setLineWidth(l.wireframeLinewidth*l0()),W9.setMode(w.LINES);else W9.setMode(w.TRIANGLES);else if(b.isLine){let j0=l.linewidth;if(j0===void 0)j0=1;if(L0.setLineWidth(j0*l0()),b.isLineSegments)W9.setMode(w.LINES);else if(b.isLineLoop)W9.setMode(w.LINE_LOOP);else W9.setMode(w.LINE_STRIP)}else if(b.isPoints)W9.setMode(w.POINTS);else if(b.isSprite)W9.setMode(w.TRIANGLES);if(b.isBatchedMesh)if(b._multiDrawInstances!==null)E8("THREE.WebGLRenderer: renderMultiDrawInstances has been deprecated and will be removed in r184. Append to renderMultiDraw arguments and use indirection."),W9.renderMultiDrawInstances(b._multiDrawStarts,b._multiDrawCounts,b._multiDrawCount,b._multiDrawInstances);else if(!S0.get("WEBGL_multi_draw")){let{_multiDrawStarts:j0,_multiDrawCounts:N9,_multiDrawCount:o0}=b,f9=V0?e0.get(V0).bytesPerElement:1,aJ=w0.get(l).currentProgram.getUniforms();for(let v9=0;v9<o0;v9++)aJ.setValue(w,"_gl_DrawID",v9),W9.render(j0[v9]/f9,N9[v9])}else W9.renderMultiDraw(b._multiDrawStarts,b._multiDrawCounts,b._multiDrawCount);else if(b.isInstancedMesh)W9.renderInstances(c0,q9,b.count);else if(p.isInstancedBufferGeometry){let j0=p._maxInstanceCount!==void 0?p._maxInstanceCount:1/0,N9=Math.min(p.instanceCount,j0);W9.renderInstances(c0,q9,N9)}else W9.render(c0,q9)};function f0(k,y,p){if(k.transparent===!0&&k.side===s9&&k.forceSinglePass===!1)k.side=p9,k.needsUpdate=!0,$6(k,y,p),k.side=D8,k.needsUpdate=!0,$6(k,y,p),k.side=s9;else $6(k,y,p)}this.compile=function(k,y,p=null){if(p===null)p=k;if(E=c.get(p),E.init(y),A.push(E),p.traverseVisible(function(b){if(b.isLight&&b.layers.test(y.layers)){if(E.pushLight(b),b.castShadow)E.pushShadow(b)}}),k!==p)k.traverseVisible(function(b){if(b.isLight&&b.layers.test(y.layers)){if(E.pushLight(b),b.castShadow)E.pushShadow(b)}});E.setupLights();let l=new Set;return k.traverse(function(b){if(!(b.isMesh||b.isPoints||b.isLine||b.isSprite))return;let Y0=b.material;if(Y0)if(Array.isArray(Y0))for(let O0=0;O0<Y0.length;O0++){let B0=Y0[O0];f0(B0,p,b),l.add(B0)}else f0(Y0,p,b),l.add(Y0)}),E=A.pop(),l},this.compileAsync=function(k,y,p=null){let l=this.compile(k,y,p);return new Promise((b)=>{function Y0(){if(l.forEach(function(O0){if(w0.get(O0).currentProgram.isReady())l.delete(O0)}),l.size===0){b(k);return}setTimeout(Y0,10)}if(S0.get("KHR_parallel_shader_compile")!==null)Y0();else setTimeout(Y0,10)})};let r0=null;function J9(k){if(r0)r0(k)}function JJ(){SJ.stop()}function $J(){SJ.start()}let SJ=new LW;if(SJ.setAnimationLoop(J9),typeof self<"u")SJ.setContext(self);this.setAnimationLoop=function(k){r0=k,_.setAnimationLoop(k),k===null?SJ.stop():SJ.start()},_.addEventListener("sessionstart",JJ),_.addEventListener("sessionend",$J),this.render=function(k,y){if(y!==void 0&&y.isCamera!==!0){console.error("THREE.WebGLRenderer.render: camera is not an instance of THREE.Camera.");return}if(I===!0)return;if(k.matrixWorldAutoUpdate===!0)k.updateMatrixWorld();if(y.parent===null&&y.matrixWorldAutoUpdate===!0)y.updateMatrixWorld();if(_.enabled===!0&&_.isPresenting===!0){if(_.cameraAutoUpdate===!0)_.updateCamera(y);y=_.getCamera()}if(k.isScene===!0)k.onBeforeRender(V,k,y,S);if(E=c.get(k,A.length),E.init(y),A.push(E),H0.multiplyMatrices(y.projectionMatrix,y.matrixWorldInverse),a0.setFromProjectionMatrix(H0,B$,y.reversedDepth),o=this.localClippingEnabled,m0=R0.init(this.clippingPlanes,o),L=e.get(k,q.length),L.init(),q.push(L),_.enabled===!0&&_.isPresenting===!0){let Y0=V.xr.getDepthSensingMesh();if(Y0!==null)N7(Y0,y,-1/0,V.sortObjects)}if(N7(k,y,0,V.sortObjects),L.finish(),V.sortObjects===!0)L.sort(u,N0);if(E0=_.enabled===!1||_.isPresenting===!1||_.hasDepthSensing()===!1,E0)z0.addToRenderList(L,k);if(this.info.render.frame++,m0===!0)R0.beginShadows();let p=E.state.shadowsArray;if(X0.render(p,k,y),m0===!0)R0.endShadows();if(this.info.autoReset===!0)this.info.reset();let{opaque:l,transmissive:b}=L;if(E.setupLights(),y.isArrayCamera){let Y0=y.cameras;if(b.length>0)for(let O0=0,B0=Y0.length;O0<B0;O0++){let V0=Y0[O0];EQ(l,b,k,V0)}if(E0)z0.render(k);for(let O0=0,B0=Y0.length;O0<B0;O0++){let V0=Y0[O0];GQ(L,k,V0,V0.viewport)}}else{if(b.length>0)EQ(l,b,k,y);if(E0)z0.render(k);GQ(L,k,y)}if(S!==null&&P===0)C0.updateMultisampleRenderTarget(S),C0.updateRenderTargetMipmap(S);if(k.isScene===!0)k.onAfterRender(V,k,y);if(F0.resetDefaultState(),g=-1,z=null,A.pop(),A.length>0){if(E=A[A.length-1],m0===!0)R0.setGlobalState(V.clippingPlanes,E.state.camera)}else E=null;if(q.pop(),q.length>0)L=q[q.length-1];else L=null};function N7(k,y,p,l){if(k.visible===!1)return;if(k.layers.test(y.layers)){if(k.isGroup)p=k.renderOrder;else if(k.isLOD){if(k.autoUpdate===!0)k.update(y)}else if(k.isLight){if(E.pushLight(k),k.castShadow)E.pushShadow(k)}else if(k.isSprite){if(!k.frustumCulled||a0.intersectsSprite(k)){if(l)t.setFromMatrixPosition(k.matrixWorld).applyMatrix4(H0);let O0=O.update(k),B0=k.material;if(B0.visible)L.push(k,O0,B0,p,t.z,null)}}else if(k.isMesh||k.isLine||k.isPoints){if(!k.frustumCulled||a0.intersectsObject(k)){let O0=O.update(k),B0=k.material;if(l){if(k.boundingSphere!==void 0){if(k.boundingSphere===null)k.computeBoundingSphere();t.copy(k.boundingSphere.center)}else{if(O0.boundingSphere===null)O0.computeBoundingSphere();t.copy(O0.boundingSphere.center)}t.applyMatrix4(k.matrixWorld).applyMatrix4(H0)}if(Array.isArray(B0)){let V0=O0.groups;for(let y0=0,h0=V0.length;y0<h0;y0++){let P0=V0[y0],c0=B0[P0.materialIndex];if(c0&&c0.visible)L.push(k,O0,c0,p,t.z,P0)}}else if(B0.visible)L.push(k,O0,B0,p,t.z,null)}}}let Y0=k.children;for(let O0=0,B0=Y0.length;O0<B0;O0++)N7(Y0[O0],y,p,l)}function GQ(k,y,p,l){let{opaque:b,transmissive:Y0,transparent:O0}=k;if(E.setupLightsView(p),m0===!0)R0.setGlobalState(V.clippingPlanes,p);if(l)L0.viewport(F.copy(l));if(b.length>0)J6(b,y,p);if(Y0.length>0)J6(Y0,y,p);if(O0.length>0)J6(O0,y,p);L0.buffers.depth.setTest(!0),L0.buffers.depth.setMask(!0),L0.buffers.color.setMask(!0),L0.setPolygonOffset(!1)}function EQ(k,y,p,l){if((p.isScene===!0?p.overrideMaterial:null)!==null)return;if(E.state.transmissionRenderTarget[l.id]===void 0)E.state.transmissionRenderTarget[l.id]=new FJ(1,1,{generateMipmaps:!0,type:S0.has("EXT_color_buffer_half_float")||S0.has("EXT_color_buffer_float")?d8:PJ,minFilter:k8,samples:4,stencilBuffer:W,resolveDepthBuffer:!1,resolveStencilBuffer:!1,colorSpace:s0.workingColorSpace});let Y0=E.state.transmissionRenderTarget[l.id],O0=l.viewport||F;Y0.setSize(O0.z*V.transmissionResolutionScale,O0.w*V.transmissionResolutionScale);let B0=V.getRenderTarget(),V0=V.getActiveCubeFace(),y0=V.getActiveMipmapLevel();if(V.setRenderTarget(Y0),V.getClearColor(s),d=V.getClearAlpha(),d<1)V.setClearColor(16777215,0.5);if(V.clear(),E0)z0.render(p);let h0=V.toneMapping;V.toneMapping=RJ;let P0=l.viewport;if(l.viewport!==void 0)l.viewport=void 0;if(E.setupLightsView(l),m0===!0)R0.setGlobalState(V.clippingPlanes,l);if(J6(k,p,l),C0.updateMultisampleRenderTarget(Y0),C0.updateRenderTargetMipmap(Y0),S0.has("WEBGL_multisampled_render_to_texture")===!1){let c0=!1;for(let $9=0,q9=y.length;$9<q9;$9++){let Y9=y[$9],W9=Y9.object,j0=Y9.geometry,N9=Y9.material,o0=Y9.group;if(N9.side===s9&&W9.layers.test(l.layers)){let f9=N9.side;N9.side=p9,N9.needsUpdate=!0,DQ(W9,p,l,j0,N9,o0),N9.side=f9,N9.needsUpdate=!0,c0=!0}}if(c0===!0)C0.updateMultisampleRenderTarget(Y0),C0.updateRenderTargetMipmap(Y0)}if(V.setRenderTarget(B0,V0,y0),V.setClearColor(s,d),P0!==void 0)l.viewport=P0;V.toneMapping=h0}function J6(k,y,p){let l=y.isScene===!0?y.overrideMaterial:null;for(let b=0,Y0=k.length;b<Y0;b++){let O0=k[b],B0=O0.object,V0=O0.geometry,y0=O0.group,h0=O0.material;if(h0.allowOverride===!0&&l!==null)h0=l;if(B0.layers.test(p.layers))DQ(B0,y,p,V0,h0,y0)}}function DQ(k,y,p,l,b,Y0){if(k.onBeforeRender(V,y,p,l,b,Y0),k.modelViewMatrix.multiplyMatrices(p.matrixWorldInverse,k.matrixWorld),k.normalMatrix.getNormalMatrix(k.modelViewMatrix),b.onBeforeRender(V,y,p,l,k,Y0),b.transparent===!0&&b.side===s9&&b.forceSinglePass===!1)b.side=p9,b.needsUpdate=!0,V.renderBufferDirect(p,y,l,b,k,Y0),b.side=D8,b.needsUpdate=!0,V.renderBufferDirect(p,y,l,b,k,Y0),b.side=s9;else V.renderBufferDirect(p,y,l,b,k,Y0);k.onAfterRender(V,y,p,l,b,Y0)}function $6(k,y,p){if(y.isScene!==!0)y=Q0;let l=w0.get(k),b=E.state.lights,Y0=E.state.shadowsArray,O0=b.state.version,B0=j.getParameters(k,b.state,Y0,y,p),V0=j.getProgramCacheKey(B0),y0=l.programs;if(l.environment=k.isMeshStandardMaterial?y.environment:null,l.fog=y.fog,l.envMap=(k.isMeshStandardMaterial?D9:H9).get(k.envMap||l.environment),l.envMapRotation=l.environment!==null&&k.envMap===null?y.environmentRotation:k.envMapRotation,y0===void 0)k.addEventListener("dispose",r),y0=new Map,l.programs=y0;let h0=y0.get(V0);if(h0!==void 0){if(l.currentProgram===h0&&l.lightsStateVersion===O0)return OQ(k,B0),h0}else B0.uniforms=j.getUniforms(k),k.onBeforeCompile(B0,V),h0=j.acquireProgram(B0,V0),y0.set(V0,h0),l.uniforms=B0.uniforms;let P0=l.uniforms;if(!k.isShaderMaterial&&!k.isRawShaderMaterial||k.clipping===!0)P0.clippingPlanes=R0.uniform;if(OQ(k,B0),l.needsLights=pW(k),l.lightsStateVersion=O0,l.needsLights)P0.ambientLightColor.value=b.state.ambient,P0.lightProbe.value=b.state.probe,P0.directionalLights.value=b.state.directional,P0.directionalLightShadows.value=b.state.directionalShadow,P0.spotLights.value=b.state.spot,P0.spotLightShadows.value=b.state.spotShadow,P0.rectAreaLights.value=b.state.rectArea,P0.ltc_1.value=b.state.rectAreaLTC1,P0.ltc_2.value=b.state.rectAreaLTC2,P0.pointLights.value=b.state.point,P0.pointLightShadows.value=b.state.pointShadow,P0.hemisphereLights.value=b.state.hemi,P0.directionalShadowMap.value=b.state.directionalShadowMap,P0.directionalShadowMatrix.value=b.state.directionalShadowMatrix,P0.spotShadowMap.value=b.state.spotShadowMap,P0.spotLightMatrix.value=b.state.spotLightMatrix,P0.spotLightMap.value=b.state.spotLightMap,P0.pointShadowMap.value=b.state.pointShadowMap,P0.pointShadowMatrix.value=b.state.pointShadowMatrix;return l.currentProgram=h0,l.uniformsList=null,h0}function qQ(k){if(k.uniformsList===null){let y=k.currentProgram.getUniforms();k.uniformsList=e8.seqWithValue(y.seq,k.uniforms)}return k.uniformsList}function OQ(k,y){let p=w0.get(k);p.outputColorSpace=y.outputColorSpace,p.batching=y.batching,p.batchingColor=y.batchingColor,p.instancing=y.instancing,p.instancingColor=y.instancingColor,p.instancingMorph=y.instancingMorph,p.skinning=y.skinning,p.morphTargets=y.morphTargets,p.morphNormals=y.morphNormals,p.morphColors=y.morphColors,p.morphTargetsCount=y.morphTargetsCount,p.numClippingPlanes=y.numClippingPlanes,p.numIntersection=y.numClipIntersection,p.vertexAlphas=y.vertexAlphas,p.vertexTangents=y.vertexTangents,p.toneMapping=y.toneMapping}function xW(k,y,p,l,b){if(y.isScene!==!0)y=Q0;C0.resetTextureUnits();let Y0=y.fog,O0=l.isMeshStandardMaterial?y.environment:null,B0=S===null?V.outputColorSpace:S.isXRRenderTarget===!0?S.texture.colorSpace:u8,V0=(l.isMeshStandardMaterial?D9:H9).get(l.envMap||O0),y0=l.vertexColors===!0&&!!p.attributes.color&&p.attributes.color.itemSize===4,h0=!!p.attributes.tangent&&(!!l.normalMap||l.anisotropy>0),P0=!!p.morphAttributes.position,c0=!!p.morphAttributes.normal,$9=!!p.morphAttributes.color,q9=RJ;if(l.toneMapped){if(S===null||S.isXRRenderTarget===!0)q9=V.toneMapping}let Y9=p.morphAttributes.position||p.morphAttributes.normal||p.morphAttributes.color,W9=Y9!==void 0?Y9.length:0,j0=w0.get(l),N9=E.state.lights;if(m0===!0){if(o===!0||k!==z){let C9=k===z&&l.id===g;R0.setState(l,k,C9)}}let o0=!1;if(l.version===j0.__version){if(j0.needsLights&&j0.lightsStateVersion!==N9.state.version)o0=!0;else if(j0.outputColorSpace!==B0)o0=!0;else if(b.isBatchedMesh&&j0.batching===!1)o0=!0;else if(!b.isBatchedMesh&&j0.batching===!0)o0=!0;else if(b.isBatchedMesh&&j0.batchingColor===!0&&b.colorTexture===null)o0=!0;else if(b.isBatchedMesh&&j0.batchingColor===!1&&b.colorTexture!==null)o0=!0;else if(b.isInstancedMesh&&j0.instancing===!1)o0=!0;else if(!b.isInstancedMesh&&j0.instancing===!0)o0=!0;else if(b.isSkinnedMesh&&j0.skinning===!1)o0=!0;else if(!b.isSkinnedMesh&&j0.skinning===!0)o0=!0;else if(b.isInstancedMesh&&j0.instancingColor===!0&&b.instanceColor===null)o0=!0;else if(b.isInstancedMesh&&j0.instancingColor===!1&&b.instanceColor!==null)o0=!0;else if(b.isInstancedMesh&&j0.instancingMorph===!0&&b.morphTexture===null)o0=!0;else if(b.isInstancedMesh&&j0.instancingMorph===!1&&b.morphTexture!==null)o0=!0;else if(j0.envMap!==V0)o0=!0;else if(l.fog===!0&&j0.fog!==Y0)o0=!0;else if(j0.numClippingPlanes!==void 0&&(j0.numClippingPlanes!==R0.numPlanes||j0.numIntersection!==R0.numIntersection))o0=!0;else if(j0.vertexAlphas!==y0)o0=!0;else if(j0.vertexTangents!==h0)o0=!0;else if(j0.morphTargets!==P0)o0=!0;else if(j0.morphNormals!==c0)o0=!0;else if(j0.morphColors!==$9)o0=!0;else if(j0.toneMapping!==q9)o0=!0;else if(j0.morphTargetsCount!==W9)o0=!0}else o0=!0,j0.__version=l.version;let f9=j0.currentProgram;if(o0===!0)f9=$6(l,y,b);let aJ=!1,v9=!1,P8=!1,U9=f9.getUniforms(),d9=j0.uniforms;if(L0.useProgram(f9.program))aJ=!0,v9=!0,P8=!0;if(l.id!==g)g=l.id,v9=!0;if(aJ||z!==k){if(L0.buffers.depth.getReversed()&&k.reversedDepth!==!0)k._reversedDepth=!0,k.updateProjectionMatrix();U9.setValue(w,"projectionMatrix",k.projectionMatrix),U9.setValue(w,"viewMatrix",k.matrixWorldInverse);let j9=U9.map.cameraPosition;if(j9!==void 0)j9.setValue(w,h.setFromMatrixPosition(k.matrixWorld));if(A0.logarithmicDepthBuffer)U9.setValue(w,"logDepthBufFC",2/(Math.log(k.far+1)/Math.LN2));if(l.isMeshPhongMaterial||l.isMeshToonMaterial||l.isMeshLambertMaterial||l.isMeshBasicMaterial||l.isMeshStandardMaterial||l.isShaderMaterial)U9.setValue(w,"isOrthographic",k.isOrthographicCamera===!0);if(z!==k)z=k,v9=!0,P8=!0}if(b.isSkinnedMesh){U9.setOptional(w,b,"bindMatrix"),U9.setOptional(w,b,"bindMatrixInverse");let C9=b.skeleton;if(C9){if(C9.boneTexture===null)C9.computeBoneTexture();U9.setValue(w,"boneTexture",C9.boneTexture,C0)}}if(b.isBatchedMesh){if(U9.setOptional(w,b,"batchingTexture"),U9.setValue(w,"batchingTexture",b._matricesTexture,C0),U9.setOptional(w,b,"batchingIdTexture"),U9.setValue(w,"batchingIdTexture",b._indirectTexture,C0),U9.setOptional(w,b,"batchingColorTexture"),b._colorsTexture!==null)U9.setValue(w,"batchingColorTexture",b._colorsTexture,C0)}let m9=p.morphAttributes;if(m9.position!==void 0||m9.normal!==void 0||m9.color!==void 0)T0.update(b,p,f9);if(v9||j0.receiveShadow!==b.receiveShadow)j0.receiveShadow=b.receiveShadow,U9.setValue(w,"receiveShadow",b.receiveShadow);if(l.isMeshGouraudMaterial&&l.envMap!==null)d9.envMap.value=V0,d9.flipEnvMap.value=V0.isCubeTexture&&V0.isRenderTargetTexture===!1?-1:1;if(l.isMeshStandardMaterial&&l.envMap===null&&y.environment!==null)d9.envMapIntensity.value=y.environmentIntensity;if(v9){if(U9.setValue(w,"toneMappingExposure",V.toneMappingExposure),j0.needsLights)gW(d9,P8);if(Y0&&l.fog===!0)x.refreshFogUniforms(d9,Y0);x.refreshMaterialUniforms(d9,l,W0,m,E.state.transmissionRenderTarget[k.id]),e8.upload(w,qQ(j0),d9,C0)}if(l.isShaderMaterial&&l.uniformsNeedUpdate===!0)e8.upload(w,qQ(j0),d9,C0),l.uniformsNeedUpdate=!1;if(l.isSpriteMaterial)U9.setValue(w,"center",b.center);if(U9.setValue(w,"modelViewMatrix",b.modelViewMatrix),U9.setValue(w,"normalMatrix",b.normalMatrix),U9.setValue(w,"modelMatrix",b.matrixWorld),l.isShaderMaterial||l.isRawShaderMaterial){let C9=l.uniformsGroups;for(let j9=0,U7=C9.length;j9<U7;j9++){let jJ=C9[j9];U0.update(jJ,f9),U0.bind(jJ,f9)}}return f9}function gW(k,y){k.ambientLightColor.needsUpdate=y,k.lightProbe.needsUpdate=y,k.directionalLights.needsUpdate=y,k.directionalLightShadows.needsUpdate=y,k.pointLights.needsUpdate=y,k.pointLightShadows.needsUpdate=y,k.spotLights.needsUpdate=y,k.spotLightShadows.needsUpdate=y,k.rectAreaLights.needsUpdate=y,k.hemisphereLights.needsUpdate=y}function pW(k){return k.isMeshLambertMaterial||k.isMeshToonMaterial||k.isMeshPhongMaterial||k.isMeshStandardMaterial||k.isShadowMaterial||k.isShaderMaterial&&k.lights===!0}this.getActiveCubeFace=function(){return f},this.getActiveMipmapLevel=function(){return P},this.getRenderTarget=function(){return S},this.setRenderTargetTextures=function(k,y,p){let l=w0.get(k);if(l.__autoAllocateDepthBuffer=k.resolveDepthBuffer===!1,l.__autoAllocateDepthBuffer===!1)l.__useRenderToTexture=!1;w0.get(k.texture).__webglTexture=y,w0.get(k.depthTexture).__webglTexture=l.__autoAllocateDepthBuffer?void 0:p,l.__hasExternalTextures=!0},this.setRenderTargetFramebuffer=function(k,y){let p=w0.get(k);p.__webglFramebuffer=y,p.__useDefaultFramebuffer=y===void 0};let lW=w.createFramebuffer();this.setRenderTarget=function(k,y=0,p=0){S=k,f=y,P=p;let l=!0,b=null,Y0=!1,O0=!1;if(k){let V0=w0.get(k);if(V0.__useDefaultFramebuffer!==void 0)L0.bindFramebuffer(w.FRAMEBUFFER,null),l=!1;else if(V0.__webglFramebuffer===void 0)C0.setupRenderTarget(k);else if(V0.__hasExternalTextures)C0.rebindTextures(k,w0.get(k.texture).__webglTexture,w0.get(k.depthTexture).__webglTexture);else if(k.depthBuffer){let P0=k.depthTexture;if(V0.__boundDepthTexture!==P0){if(P0!==null&&w0.has(P0)&&(k.width!==P0.image.width||k.height!==P0.image.height))throw Error("WebGLRenderTarget: Attached DepthTexture is initialized to the incorrect size.");C0.setupDepthRenderbuffer(k)}}let y0=k.texture;if(y0.isData3DTexture||y0.isDataArrayTexture||y0.isCompressedArrayTexture)O0=!0;let h0=w0.get(k).__webglFramebuffer;if(k.isWebGLCubeRenderTarget){if(Array.isArray(h0[y]))b=h0[y][p];else b=h0[y];Y0=!0}else if(k.samples>0&&C0.useMultisampledRTT(k)===!1)b=w0.get(k).__webglMultisampledFramebuffer;else if(Array.isArray(h0))b=h0[p];else b=h0;F.copy(k.viewport),T.copy(k.scissor),n=k.scissorTest}else F.copy(q0).multiplyScalar(W0).floor(),T.copy(_0).multiplyScalar(W0).floor(),n=g0;if(p!==0)b=lW;if(L0.bindFramebuffer(w.FRAMEBUFFER,b)&&l)L0.drawBuffers(k,b);if(L0.viewport(F),L0.scissor(T),L0.setScissorTest(n),Y0){let V0=w0.get(k.texture);w.framebufferTexture2D(w.FRAMEBUFFER,w.COLOR_ATTACHMENT0,w.TEXTURE_CUBE_MAP_POSITIVE_X+y,V0.__webglTexture,p)}else if(O0){let V0=y;for(let y0=0;y0<k.textures.length;y0++){let h0=w0.get(k.textures[y0]);w.framebufferTextureLayer(w.FRAMEBUFFER,w.COLOR_ATTACHMENT0+y0,h0.__webglTexture,p,V0)}}else if(k!==null&&p!==0){let V0=w0.get(k.texture);w.framebufferTexture2D(w.FRAMEBUFFER,w.COLOR_ATTACHMENT0,w.TEXTURE_2D,V0.__webglTexture,p)}g=-1},this.readRenderTargetPixels=function(k,y,p,l,b,Y0,O0,B0=0){if(!(k&&k.isWebGLRenderTarget)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not THREE.WebGLRenderTarget.");return}let V0=w0.get(k).__webglFramebuffer;if(k.isWebGLCubeRenderTarget&&O0!==void 0)V0=V0[O0];if(V0){L0.bindFramebuffer(w.FRAMEBUFFER,V0);try{let y0=k.textures[B0],h0=y0.format,P0=y0.type;if(!A0.textureFormatReadable(h0)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not in RGBA or implementation defined format.");return}if(!A0.textureTypeReadable(P0)){console.error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not in UnsignedByteType or implementation defined type.");return}if(y>=0&&y<=k.width-l&&(p>=0&&p<=k.height-b)){if(k.textures.length>1)w.readBuffer(w.COLOR_ATTACHMENT0+B0);w.readPixels(y,p,l,b,k0.convert(h0),k0.convert(P0),Y0)}}finally{let y0=S!==null?w0.get(S).__webglFramebuffer:null;L0.bindFramebuffer(w.FRAMEBUFFER,y0)}}},this.readRenderTargetPixelsAsync=async function(k,y,p,l,b,Y0,O0,B0=0){if(!(k&&k.isWebGLRenderTarget))throw Error("THREE.WebGLRenderer.readRenderTargetPixels: renderTarget is not THREE.WebGLRenderTarget.");let V0=w0.get(k).__webglFramebuffer;if(k.isWebGLCubeRenderTarget&&O0!==void 0)V0=V0[O0];if(V0)if(y>=0&&y<=k.width-l&&(p>=0&&p<=k.height-b)){L0.bindFramebuffer(w.FRAMEBUFFER,V0);let y0=k.textures[B0],h0=y0.format,P0=y0.type;if(!A0.textureFormatReadable(h0))throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: renderTarget is not in RGBA or implementation defined format.");if(!A0.textureTypeReadable(P0))throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: renderTarget is not in UnsignedByteType or implementation defined type.");let c0=w.createBuffer();if(w.bindBuffer(w.PIXEL_PACK_BUFFER,c0),w.bufferData(w.PIXEL_PACK_BUFFER,Y0.byteLength,w.STREAM_READ),k.textures.length>1)w.readBuffer(w.COLOR_ATTACHMENT0+B0);w.readPixels(y,p,l,b,k0.convert(h0),k0.convert(P0),0);let $9=S!==null?w0.get(S).__webglFramebuffer:null;L0.bindFramebuffer(w.FRAMEBUFFER,$9);let q9=w.fenceSync(w.SYNC_GPU_COMMANDS_COMPLETE,0);return w.flush(),await uZ(w,q9,4),w.bindBuffer(w.PIXEL_PACK_BUFFER,c0),w.getBufferSubData(w.PIXEL_PACK_BUFFER,0,Y0),w.deleteBuffer(c0),w.deleteSync(q9),Y0}else throw Error("THREE.WebGLRenderer.readRenderTargetPixelsAsync: requested read bounds are out of range.")},this.copyFramebufferToTexture=function(k,y=null,p=0){let l=Math.pow(2,-p),b=Math.floor(k.image.width*l),Y0=Math.floor(k.image.height*l),O0=y!==null?y.x:0,B0=y!==null?y.y:0;C0.setTexture2D(k,0),w.copyTexSubImage2D(w.TEXTURE_2D,p,0,0,O0,B0,b,Y0),L0.unbindTexture()};let dW=w.createFramebuffer(),mW=w.createFramebuffer();if(this.copyTextureToTexture=function(k,y,p=null,l=null,b=0,Y0=null){if(Y0===null)if(b!==0)E8("WebGLRenderer: copyTextureToTexture function signature has changed to support src and dst mipmap levels."),Y0=b,b=0;else Y0=0;let O0,B0,V0,y0,h0,P0,c0,$9,q9,Y9=k.isCompressedTexture?k.mipmaps[Y0]:k.image;if(p!==null)O0=p.max.x-p.min.x,B0=p.max.y-p.min.y,V0=p.isBox3?p.max.z-p.min.z:1,y0=p.min.x,h0=p.min.y,P0=p.isBox3?p.min.z:0;else{let m9=Math.pow(2,-b);if(O0=Math.floor(Y9.width*m9),B0=Math.floor(Y9.height*m9),k.isDataArrayTexture)V0=Y9.depth;else if(k.isData3DTexture)V0=Math.floor(Y9.depth*m9);else V0=1;y0=0,h0=0,P0=0}if(l!==null)c0=l.x,$9=l.y,q9=l.z;else c0=0,$9=0,q9=0;let W9=k0.convert(y.format),j0=k0.convert(y.type),N9;if(y.isData3DTexture)C0.setTexture3D(y,0),N9=w.TEXTURE_3D;else if(y.isDataArrayTexture||y.isCompressedArrayTexture)C0.setTexture2DArray(y,0),N9=w.TEXTURE_2D_ARRAY;else C0.setTexture2D(y,0),N9=w.TEXTURE_2D;w.pixelStorei(w.UNPACK_FLIP_Y_WEBGL,y.flipY),w.pixelStorei(w.UNPACK_PREMULTIPLY_ALPHA_WEBGL,y.premultiplyAlpha),w.pixelStorei(w.UNPACK_ALIGNMENT,y.unpackAlignment);let o0=w.getParameter(w.UNPACK_ROW_LENGTH),f9=w.getParameter(w.UNPACK_IMAGE_HEIGHT),aJ=w.getParameter(w.UNPACK_SKIP_PIXELS),v9=w.getParameter(w.UNPACK_SKIP_ROWS),P8=w.getParameter(w.UNPACK_SKIP_IMAGES);w.pixelStorei(w.UNPACK_ROW_LENGTH,Y9.width),w.pixelStorei(w.UNPACK_IMAGE_HEIGHT,Y9.height),w.pixelStorei(w.UNPACK_SKIP_PIXELS,y0),w.pixelStorei(w.UNPACK_SKIP_ROWS,h0),w.pixelStorei(w.UNPACK_SKIP_IMAGES,P0);let U9=k.isDataArrayTexture||k.isData3DTexture,d9=y.isDataArrayTexture||y.isData3DTexture;if(k.isDepthTexture){let m9=w0.get(k),C9=w0.get(y),j9=w0.get(m9.__renderTarget),U7=w0.get(C9.__renderTarget);L0.bindFramebuffer(w.READ_FRAMEBUFFER,j9.__webglFramebuffer),L0.bindFramebuffer(w.DRAW_FRAMEBUFFER,U7.__webglFramebuffer);for(let jJ=0;jJ<V0;jJ++){if(U9)w.framebufferTextureLayer(w.READ_FRAMEBUFFER,w.COLOR_ATTACHMENT0,w0.get(k).__webglTexture,b,P0+jJ),w.framebufferTextureLayer(w.DRAW_FRAMEBUFFER,w.COLOR_ATTACHMENT0,w0.get(y).__webglTexture,Y0,q9+jJ);w.blitFramebuffer(y0,h0,O0,B0,c0,$9,O0,B0,w.DEPTH_BUFFER_BIT,w.NEAREST)}L0.bindFramebuffer(w.READ_FRAMEBUFFER,null),L0.bindFramebuffer(w.DRAW_FRAMEBUFFER,null)}else if(b!==0||k.isRenderTargetTexture||w0.has(k)){let m9=w0.get(k),C9=w0.get(y);L0.bindFramebuffer(w.READ_FRAMEBUFFER,dW),L0.bindFramebuffer(w.DRAW_FRAMEBUFFER,mW);for(let j9=0;j9<V0;j9++){if(U9)w.framebufferTextureLayer(w.READ_FRAMEBUFFER,w.COLOR_ATTACHMENT0,m9.__webglTexture,b,P0+j9);else w.framebufferTexture2D(w.READ_FRAMEBUFFER,w.COLOR_ATTACHMENT0,w.TEXTURE_2D,m9.__webglTexture,b);if(d9)w.framebufferTextureLayer(w.DRAW_FRAMEBUFFER,w.COLOR_ATTACHMENT0,C9.__webglTexture,Y0,q9+j9);else w.framebufferTexture2D(w.DRAW_FRAMEBUFFER,w.COLOR_ATTACHMENT0,w.TEXTURE_2D,C9.__webglTexture,Y0);if(b!==0)w.blitFramebuffer(y0,h0,O0,B0,c0,$9,O0,B0,w.COLOR_BUFFER_BIT,w.NEAREST);else if(d9)w.copyTexSubImage3D(N9,Y0,c0,$9,q9+j9,y0,h0,O0,B0);else w.copyTexSubImage2D(N9,Y0,c0,$9,y0,h0,O0,B0)}L0.bindFramebuffer(w.READ_FRAMEBUFFER,null),L0.bindFramebuffer(w.DRAW_FRAMEBUFFER,null)}else if(d9)if(k.isDataTexture||k.isData3DTexture)w.texSubImage3D(N9,Y0,c0,$9,q9,O0,B0,V0,W9,j0,Y9.data);else if(y.isCompressedArrayTexture)w.compressedTexSubImage3D(N9,Y0,c0,$9,q9,O0,B0,V0,W9,Y9.data);else w.texSubImage3D(N9,Y0,c0,$9,q9,O0,B0,V0,W9,j0,Y9);else if(k.isDataTexture)w.texSubImage2D(w.TEXTURE_2D,Y0,c0,$9,O0,B0,W9,j0,Y9.data);else if(k.isCompressedTexture)w.compressedTexSubImage2D(w.TEXTURE_2D,Y0,c0,$9,Y9.width,Y9.height,W9,Y9.data);else w.texSubImage2D(w.TEXTURE_2D,Y0,c0,$9,O0,B0,W9,j0,Y9);if(w.pixelStorei(w.UNPACK_ROW_LENGTH,o0),w.pixelStorei(w.UNPACK_IMAGE_HEIGHT,f9),w.pixelStorei(w.UNPACK_SKIP_PIXELS,aJ),w.pixelStorei(w.UNPACK_SKIP_ROWS,v9),w.pixelStorei(w.UNPACK_SKIP_IMAGES,P8),Y0===0&&y.generateMipmaps)w.generateMipmap(N9);L0.unbindTexture()},this.initRenderTarget=function(k){if(w0.get(k).__webglFramebuffer===void 0)C0.setupRenderTarget(k)},this.initTexture=function(k){if(k.isCubeTexture)C0.setTextureCube(k,0);else if(k.isData3DTexture)C0.setTexture3D(k,0);else if(k.isDataArrayTexture||k.isCompressedArrayTexture)C0.setTexture2DArray(k,0);else C0.setTexture2D(k,0);L0.unbindTexture()},this.resetState=function(){f=0,P=0,S=null,L0.reset(),F0.reset()},typeof __THREE_DEVTOOLS__<"u")__THREE_DEVTOOLS__.dispatchEvent(new CustomEvent("observe",{detail:this}))}get coordinateSystem(){return B$}get outputColorSpace(){return this._outputColorSpace}set outputColorSpace(J){this._outputColorSpace=J;let $=this.getContext();$.drawingBufferColorSpace=s0._getDrawingBufferColorSpace(J),$.unpackColorSpace=s0._getUnpackColorSpace()}}var jW={type:"change"},KQ={type:"start"},bW={type:"end"},Y7=new pJ,yW=new g9,E1=Math.cos(70*C$.DEG2RAD),M9=new v,y9=2*Math.PI,Q9={NONE:-1,ROTATE:0,DOLLY:1,PAN:2,TOUCH_ROTATE:3,TOUCH_PAN:4,TOUCH_DOLLY_PAN:5,TOUCH_DOLLY_ROTATE:6},WQ=0.000001;class YQ extends $7{constructor(J,$=null){super(J,$);if(this.state=Q9.NONE,this.target=new v,this.cursor=new v,this.minDistance=0,this.maxDistance=1/0,this.minZoom=0,this.maxZoom=1/0,this.minTargetRadius=0,this.maxTargetRadius=1/0,this.minPolarAngle=0,this.maxPolarAngle=Math.PI,this.minAzimuthAngle=-1/0,this.maxAzimuthAngle=1/0,this.enableDamping=!1,this.dampingFactor=0.05,this.enableZoom=!0,this.zoomSpeed=1,this.enableRotate=!0,this.rotateSpeed=1,this.keyRotateSpeed=1,this.enablePan=!0,this.panSpeed=1,this.screenSpacePanning=!0,this.keyPanSpeed=7,this.zoomToCursor=!1,this.autoRotate=!1,this.autoRotateSpeed=2,this.keys={LEFT:"ArrowLeft",UP:"ArrowUp",RIGHT:"ArrowRight",BOTTOM:"ArrowDown"},this.mouseButtons={LEFT:CJ.ROTATE,MIDDLE:CJ.DOLLY,RIGHT:CJ.PAN},this.touches={ONE:_J.ROTATE,TWO:_J.DOLLY_PAN},this.target0=this.target.clone(),this.position0=this.object.position.clone(),this.zoom0=this.object.zoom,this._domElementKeyEvents=null,this._lastPosition=new v,this._lastQuaternion=new e9,this._lastTargetPosition=new v,this._quat=new e9().setFromUnitVectors(J.up,new v(0,1,0)),this._quatInverse=this._quat.clone().invert(),this._spherical=new r8,this._sphericalDelta=new r8,this._scale=1,this._panOffset=new v,this._rotateStart=new x0,this._rotateEnd=new x0,this._rotateDelta=new x0,this._panStart=new x0,this._panEnd=new x0,this._panDelta=new x0,this._dollyStart=new x0,this._dollyEnd=new x0,this._dollyDelta=new x0,this._dollyDirection=new v,this._mouse=new x0,this._performCursorZoom=!1,this._pointers=[],this._pointerPositions={},this._controlActive=!1,this._onPointerMove=q1.bind(this),this._onPointerDown=D1.bind(this),this._onPointerUp=O1.bind(this),this._onContextMenu=z1.bind(this),this._onMouseWheel=F1.bind(this),this._onKeyDown=M1.bind(this),this._onTouchStart=L1.bind(this),this._onTouchMove=V1.bind(this),this._onMouseDown=R1.bind(this),this._onMouseMove=k1.bind(this),this._interceptControlDown=B1.bind(this),this._interceptControlUp=w1.bind(this),this.domElement!==null)this.connect(this.domElement);this.update()}connect(J){super.connect(J),this.domElement.addEventListener("pointerdown",this._onPointerDown),this.domElement.addEventListener("pointercancel",this._onPointerUp),this.domElement.addEventListener("contextmenu",this._onContextMenu),this.domElement.addEventListener("wheel",this._onMouseWheel,{passive:!1}),this.domElement.getRootNode().addEventListener("keydown",this._interceptControlDown,{passive:!0,capture:!0}),this.domElement.style.touchAction="none"}disconnect(){this.domElement.removeEventListener("pointerdown",this._onPointerDown),this.domElement.removeEventListener("pointermove",this._onPointerMove),this.domElement.removeEventListener("pointerup",this._onPointerUp),this.domElement.removeEventListener("pointercancel",this._onPointerUp),this.domElement.removeEventListener("wheel",this._onMouseWheel),this.domElement.removeEventListener("contextmenu",this._onContextMenu),this.stopListenToKeyEvents(),this.domElement.getRootNode().removeEventListener("keydown",this._interceptControlDown,{capture:!0}),this.domElement.style.touchAction="auto"}dispose(){this.disconnect()}getPolarAngle(){return this._spherical.phi}getAzimuthalAngle(){return this._spherical.theta}getDistance(){return this.object.position.distanceTo(this.target)}listenToKeyEvents(J){J.addEventListener("keydown",this._onKeyDown),this._domElementKeyEvents=J}stopListenToKeyEvents(){if(this._domElementKeyEvents!==null)this._domElementKeyEvents.removeEventListener("keydown",this._onKeyDown),this._domElementKeyEvents=null}saveState(){this.target0.copy(this.target),this.position0.copy(this.object.position),this.zoom0=this.object.zoom}reset(){this.target.copy(this.target0),this.object.position.copy(this.position0),this.object.zoom=this.zoom0,this.object.updateProjectionMatrix(),this.dispatchEvent(jW),this.update(),this.state=Q9.NONE}update(J=null){let $=this.object.position;if(M9.copy($).sub(this.target),M9.applyQuaternion(this._quat),this._spherical.setFromVector3(M9),this.autoRotate&&this.state===Q9.NONE)this._rotateLeft(this._getAutoRotationAngle(J));if(this.enableDamping)this._spherical.theta+=this._sphericalDelta.theta*this.dampingFactor,this._spherical.phi+=this._sphericalDelta.phi*this.dampingFactor;else this._spherical.theta+=this._sphericalDelta.theta,this._spherical.phi+=this._sphericalDelta.phi;let Q=this.minAzimuthAngle,Z=this.maxAzimuthAngle;if(isFinite(Q)&&isFinite(Z)){if(Q<-Math.PI)Q+=y9;else if(Q>Math.PI)Q-=y9;if(Z<-Math.PI)Z+=y9;else if(Z>Math.PI)Z-=y9;if(Q<=Z)this._spherical.theta=Math.max(Q,Math.min(Z,this._spherical.theta));else this._spherical.theta=this._spherical.theta>(Q+Z)/2?Math.max(Q,this._spherical.theta):Math.min(Z,this._spherical.theta)}if(this._spherical.phi=Math.max(this.minPolarAngle,Math.min(this.maxPolarAngle,this._spherical.phi)),this._spherical.makeSafe(),this.enableDamping===!0)this.target.addScaledVector(this._panOffset,this.dampingFactor);else this.target.add(this._panOffset);this.target.sub(this.cursor),this.target.clampLength(this.minTargetRadius,this.maxTargetRadius),this.target.add(this.cursor);let W=!1;if(this.zoomToCursor&&this._performCursorZoom||this.object.isOrthographicCamera)this._spherical.radius=this._clampDistance(this._spherical.radius);else{let K=this._spherical.radius;this._spherical.radius=this._clampDistance(this._spherical.radius*this._scale),W=K!=this._spherical.radius}if(M9.setFromSpherical(this._spherical),M9.applyQuaternion(this._quatInverse),$.copy(this.target).add(M9),this.object.lookAt(this.target),this.enableDamping===!0)this._sphericalDelta.theta*=1-this.dampingFactor,this._sphericalDelta.phi*=1-this.dampingFactor,this._panOffset.multiplyScalar(1-this.dampingFactor);else this._sphericalDelta.set(0,0,0),this._panOffset.set(0,0,0);if(this.zoomToCursor&&this._performCursorZoom){let K=null;if(this.object.isPerspectiveCamera){let H=M9.length();K=this._clampDistance(H*this._scale);let Y=H-K;this.object.position.addScaledVector(this._dollyDirection,Y),this.object.updateMatrixWorld(),W=!!Y}else if(this.object.isOrthographicCamera){let H=new v(this._mouse.x,this._mouse.y,0);H.unproject(this.object);let Y=this.object.zoom;this.object.zoom=Math.max(this.minZoom,Math.min(this.maxZoom,this.object.zoom/this._scale)),this.object.updateProjectionMatrix(),W=Y!==this.object.zoom;let X=new v(this._mouse.x,this._mouse.y,0);X.unproject(this.object),this.object.position.sub(X).add(H),this.object.updateMatrixWorld(),K=M9.length()}else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - zoom to cursor disabled."),this.zoomToCursor=!1;if(K!==null)if(this.screenSpacePanning)this.target.set(0,0,-1).transformDirection(this.object.matrix).multiplyScalar(K).add(this.object.position);else if(Y7.origin.copy(this.object.position),Y7.direction.set(0,0,-1).transformDirection(this.object.matrix),Math.abs(this.object.up.dot(Y7.direction))<E1)this.object.lookAt(this.target);else yW.setFromNormalAndCoplanarPoint(this.object.up,this.target),Y7.intersectPlane(yW,this.target)}else if(this.object.isOrthographicCamera){let K=this.object.zoom;if(this.object.zoom=Math.max(this.minZoom,Math.min(this.maxZoom,this.object.zoom/this._scale)),K!==this.object.zoom)this.object.updateProjectionMatrix(),W=!0}if(this._scale=1,this._performCursorZoom=!1,W||this._lastPosition.distanceToSquared(this.object.position)>WQ||8*(1-this._lastQuaternion.dot(this.object.quaternion))>WQ||this._lastTargetPosition.distanceToSquared(this.target)>WQ)return this.dispatchEvent(jW),this._lastPosition.copy(this.object.position),this._lastQuaternion.copy(this.object.quaternion),this._lastTargetPosition.copy(this.target),!0;return!1}_getAutoRotationAngle(J){if(J!==null)return y9/60*this.autoRotateSpeed*J;else return y9/60/60*this.autoRotateSpeed}_getZoomScale(J){let $=Math.abs(J*0.01);return Math.pow(0.95,this.zoomSpeed*$)}_rotateLeft(J){this._sphericalDelta.theta-=J}_rotateUp(J){this._sphericalDelta.phi-=J}_panLeft(J,$){M9.setFromMatrixColumn($,0),M9.multiplyScalar(-J),this._panOffset.add(M9)}_panUp(J,$){if(this.screenSpacePanning===!0)M9.setFromMatrixColumn($,1);else M9.setFromMatrixColumn($,0),M9.crossVectors(this.object.up,M9);M9.multiplyScalar(J),this._panOffset.add(M9)}_pan(J,$){let Q=this.domElement;if(this.object.isPerspectiveCamera){let Z=this.object.position;M9.copy(Z).sub(this.target);let W=M9.length();W*=Math.tan(this.object.fov/2*Math.PI/180),this._panLeft(2*J*W/Q.clientHeight,this.object.matrix),this._panUp(2*$*W/Q.clientHeight,this.object.matrix)}else if(this.object.isOrthographicCamera)this._panLeft(J*(this.object.right-this.object.left)/this.object.zoom/Q.clientWidth,this.object.matrix),this._panUp($*(this.object.top-this.object.bottom)/this.object.zoom/Q.clientHeight,this.object.matrix);else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - pan disabled."),this.enablePan=!1}_dollyOut(J){if(this.object.isPerspectiveCamera||this.object.isOrthographicCamera)this._scale/=J;else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - dolly/zoom disabled."),this.enableZoom=!1}_dollyIn(J){if(this.object.isPerspectiveCamera||this.object.isOrthographicCamera)this._scale*=J;else console.warn("WARNING: OrbitControls.js encountered an unknown camera type - dolly/zoom disabled."),this.enableZoom=!1}_updateZoomParameters(J,$){if(!this.zoomToCursor)return;this._performCursorZoom=!0;let Q=this.domElement.getBoundingClientRect(),Z=J-Q.left,W=$-Q.top,K=Q.width,H=Q.height;this._mouse.x=Z/K*2-1,this._mouse.y=-(W/H)*2+1,this._dollyDirection.set(this._mouse.x,this._mouse.y,1).unproject(this.object).sub(this.object.position).normalize()}_clampDistance(J){return Math.max(this.minDistance,Math.min(this.maxDistance,J))}_handleMouseDownRotate(J){this._rotateStart.set(J.clientX,J.clientY)}_handleMouseDownDolly(J){this._updateZoomParameters(J.clientX,J.clientX),this._dollyStart.set(J.clientX,J.clientY)}_handleMouseDownPan(J){this._panStart.set(J.clientX,J.clientY)}_handleMouseMoveRotate(J){this._rotateEnd.set(J.clientX,J.clientY),this._rotateDelta.subVectors(this._rotateEnd,this._rotateStart).multiplyScalar(this.rotateSpeed);let $=this.domElement;this._rotateLeft(y9*this._rotateDelta.x/$.clientHeight),this._rotateUp(y9*this._rotateDelta.y/$.clientHeight),this._rotateStart.copy(this._rotateEnd),this.update()}_handleMouseMoveDolly(J){if(this._dollyEnd.set(J.clientX,J.clientY),this._dollyDelta.subVectors(this._dollyEnd,this._dollyStart),this._dollyDelta.y>0)this._dollyOut(this._getZoomScale(this._dollyDelta.y));else if(this._dollyDelta.y<0)this._dollyIn(this._getZoomScale(this._dollyDelta.y));this._dollyStart.copy(this._dollyEnd),this.update()}_handleMouseMovePan(J){this._panEnd.set(J.clientX,J.clientY),this._panDelta.subVectors(this._panEnd,this._panStart).multiplyScalar(this.panSpeed),this._pan(this._panDelta.x,this._panDelta.y),this._panStart.copy(this._panEnd),this.update()}_handleMouseWheel(J){if(this._updateZoomParameters(J.clientX,J.clientY),J.deltaY<0)this._dollyIn(this._getZoomScale(J.deltaY));else if(J.deltaY>0)this._dollyOut(this._getZoomScale(J.deltaY));this.update()}_handleKeyDown(J){let $=!1;switch(J.code){case this.keys.UP:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateUp(y9*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(0,this.keyPanSpeed);$=!0;break;case this.keys.BOTTOM:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateUp(-y9*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(0,-this.keyPanSpeed);$=!0;break;case this.keys.LEFT:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateLeft(y9*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(this.keyPanSpeed,0);$=!0;break;case this.keys.RIGHT:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate)this._rotateLeft(-y9*this.keyRotateSpeed/this.domElement.clientHeight)}else if(this.enablePan)this._pan(-this.keyPanSpeed,0);$=!0;break}if($)J.preventDefault(),this.update()}_handleTouchStartRotate(J){if(this._pointers.length===1)this._rotateStart.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),Q=0.5*(J.pageX+$.x),Z=0.5*(J.pageY+$.y);this._rotateStart.set(Q,Z)}}_handleTouchStartPan(J){if(this._pointers.length===1)this._panStart.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),Q=0.5*(J.pageX+$.x),Z=0.5*(J.pageY+$.y);this._panStart.set(Q,Z)}}_handleTouchStartDolly(J){let $=this._getSecondPointerPosition(J),Q=J.pageX-$.x,Z=J.pageY-$.y,W=Math.sqrt(Q*Q+Z*Z);this._dollyStart.set(0,W)}_handleTouchStartDollyPan(J){if(this.enableZoom)this._handleTouchStartDolly(J);if(this.enablePan)this._handleTouchStartPan(J)}_handleTouchStartDollyRotate(J){if(this.enableZoom)this._handleTouchStartDolly(J);if(this.enableRotate)this._handleTouchStartRotate(J)}_handleTouchMoveRotate(J){if(this._pointers.length==1)this._rotateEnd.set(J.pageX,J.pageY);else{let Q=this._getSecondPointerPosition(J),Z=0.5*(J.pageX+Q.x),W=0.5*(J.pageY+Q.y);this._rotateEnd.set(Z,W)}this._rotateDelta.subVectors(this._rotateEnd,this._rotateStart).multiplyScalar(this.rotateSpeed);let $=this.domElement;this._rotateLeft(y9*this._rotateDelta.x/$.clientHeight),this._rotateUp(y9*this._rotateDelta.y/$.clientHeight),this._rotateStart.copy(this._rotateEnd)}_handleTouchMovePan(J){if(this._pointers.length===1)this._panEnd.set(J.pageX,J.pageY);else{let $=this._getSecondPointerPosition(J),Q=0.5*(J.pageX+$.x),Z=0.5*(J.pageY+$.y);this._panEnd.set(Q,Z)}this._panDelta.subVectors(this._panEnd,this._panStart).multiplyScalar(this.panSpeed),this._pan(this._panDelta.x,this._panDelta.y),this._panStart.copy(this._panEnd)}_handleTouchMoveDolly(J){let $=this._getSecondPointerPosition(J),Q=J.pageX-$.x,Z=J.pageY-$.y,W=Math.sqrt(Q*Q+Z*Z);this._dollyEnd.set(0,W),this._dollyDelta.set(0,Math.pow(this._dollyEnd.y/this._dollyStart.y,this.zoomSpeed)),this._dollyOut(this._dollyDelta.y),this._dollyStart.copy(this._dollyEnd);let K=(J.pageX+$.x)*0.5,H=(J.pageY+$.y)*0.5;this._updateZoomParameters(K,H)}_handleTouchMoveDollyPan(J){if(this.enableZoom)this._handleTouchMoveDolly(J);if(this.enablePan)this._handleTouchMovePan(J)}_handleTouchMoveDollyRotate(J){if(this.enableZoom)this._handleTouchMoveDolly(J);if(this.enableRotate)this._handleTouchMoveRotate(J)}_addPointer(J){this._pointers.push(J.pointerId)}_removePointer(J){delete this._pointerPositions[J.pointerId];for(let $=0;$<this._pointers.length;$++)if(this._pointers[$]==J.pointerId){this._pointers.splice($,1);return}}_isTrackingPointer(J){for(let $=0;$<this._pointers.length;$++)if(this._pointers[$]==J.pointerId)return!0;return!1}_trackPointer(J){let $=this._pointerPositions[J.pointerId];if($===void 0)$=new x0,this._pointerPositions[J.pointerId]=$;$.set(J.pageX,J.pageY)}_getSecondPointerPosition(J){let $=J.pointerId===this._pointers[0]?this._pointers[1]:this._pointers[0];return this._pointerPositions[$]}_customWheelEvent(J){let $=J.deltaMode,Q={clientX:J.clientX,clientY:J.clientY,deltaY:J.deltaY};switch($){case 1:Q.deltaY*=16;break;case 2:Q.deltaY*=100;break}if(J.ctrlKey&&!this._controlActive)Q.deltaY*=10;return Q}}function D1(J){if(this.enabled===!1)return;if(this._pointers.length===0)this.domElement.setPointerCapture(J.pointerId),this.domElement.addEventListener("pointermove",this._onPointerMove),this.domElement.addEventListener("pointerup",this._onPointerUp);if(this._isTrackingPointer(J))return;if(this._addPointer(J),J.pointerType==="touch")this._onTouchStart(J);else this._onMouseDown(J)}function q1(J){if(this.enabled===!1)return;if(J.pointerType==="touch")this._onTouchMove(J);else this._onMouseMove(J)}function O1(J){switch(this._removePointer(J),this._pointers.length){case 0:this.domElement.releasePointerCapture(J.pointerId),this.domElement.removeEventListener("pointermove",this._onPointerMove),this.domElement.removeEventListener("pointerup",this._onPointerUp),this.dispatchEvent(bW),this.state=Q9.NONE;break;case 1:let $=this._pointers[0],Q=this._pointerPositions[$];this._onTouchStart({pointerId:$,pageX:Q.x,pageY:Q.y});break}}function R1(J){let $;switch(J.button){case 0:$=this.mouseButtons.LEFT;break;case 1:$=this.mouseButtons.MIDDLE;break;case 2:$=this.mouseButtons.RIGHT;break;default:$=-1}switch($){case CJ.DOLLY:if(this.enableZoom===!1)return;this._handleMouseDownDolly(J),this.state=Q9.DOLLY;break;case CJ.ROTATE:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enablePan===!1)return;this._handleMouseDownPan(J),this.state=Q9.PAN}else{if(this.enableRotate===!1)return;this._handleMouseDownRotate(J),this.state=Q9.ROTATE}break;case CJ.PAN:if(J.ctrlKey||J.metaKey||J.shiftKey){if(this.enableRotate===!1)return;this._handleMouseDownRotate(J),this.state=Q9.ROTATE}else{if(this.enablePan===!1)return;this._handleMouseDownPan(J),this.state=Q9.PAN}break;default:this.state=Q9.NONE}if(this.state!==Q9.NONE)this.dispatchEvent(KQ)}function k1(J){switch(this.state){case Q9.ROTATE:if(this.enableRotate===!1)return;this._handleMouseMoveRotate(J);break;case Q9.DOLLY:if(this.enableZoom===!1)return;this._handleMouseMoveDolly(J);break;case Q9.PAN:if(this.enablePan===!1)return;this._handleMouseMovePan(J);break}}function F1(J){if(this.enabled===!1||this.enableZoom===!1||this.state!==Q9.NONE)return;J.preventDefault(),this.dispatchEvent(KQ),this._handleMouseWheel(this._customWheelEvent(J)),this.dispatchEvent(bW)}function M1(J){if(this.enabled===!1)return;this._handleKeyDown(J)}function L1(J){switch(this._trackPointer(J),this._pointers.length){case 1:switch(this.touches.ONE){case _J.ROTATE:if(this.enableRotate===!1)return;this._handleTouchStartRotate(J),this.state=Q9.TOUCH_ROTATE;break;case _J.PAN:if(this.enablePan===!1)return;this._handleTouchStartPan(J),this.state=Q9.TOUCH_PAN;break;default:this.state=Q9.NONE}break;case 2:switch(this.touches.TWO){case _J.DOLLY_PAN:if(this.enableZoom===!1&&this.enablePan===!1)return;this._handleTouchStartDollyPan(J),this.state=Q9.TOUCH_DOLLY_PAN;break;case _J.DOLLY_ROTATE:if(this.enableZoom===!1&&this.enableRotate===!1)return;this._handleTouchStartDollyRotate(J),this.state=Q9.TOUCH_DOLLY_ROTATE;break;default:this.state=Q9.NONE}break;default:this.state=Q9.NONE}if(this.state!==Q9.NONE)this.dispatchEvent(KQ)}function V1(J){switch(this._trackPointer(J),this.state){case Q9.TOUCH_ROTATE:if(this.enableRotate===!1)return;this._handleTouchMoveRotate(J),this.update();break;case Q9.TOUCH_PAN:if(this.enablePan===!1)return;this._handleTouchMovePan(J),this.update();break;case Q9.TOUCH_DOLLY_PAN:if(this.enableZoom===!1&&this.enablePan===!1)return;this._handleTouchMoveDollyPan(J),this.update();break;case Q9.TOUCH_DOLLY_ROTATE:if(this.enableZoom===!1&&this.enableRotate===!1)return;this._handleTouchMoveDollyRotate(J),this.update();break;default:this.state=Q9.NONE}}function z1(J){if(this.enabled===!1)return;J.preventDefault()}function B1(J){if(J.key==="Control")this._controlActive=!0,this.domElement.getRootNode().addEventListener("keyup",this._interceptControlUp,{passive:!0,capture:!0})}function w1(J){if(J.key==="Control")this._controlActive=!1,this.domElement.getRootNode().removeEventListener("keyup",this._interceptControlUp,{passive:!0,capture:!0})}import{jsx as E9,jsxs as HJ,Fragment as C1}from"react/jsx-runtime";var fW=(J,$)=>`/api/apps/design/api${J}${J.includes("?")?"&":"?"}project_id=${encodeURIComponent($)}`;function XQ({projectId:J,artifactId:$,designId:Q,revisionId:Z,focus:W,issue:K,onSelect:H,onCapture:Y}){let X=HQ(null),N=HQ(null),[U,G]=b9([]),[D,R]=b9(""),[M,L]=b9(new Set),[E,q]=b9(0.35),[A,V]=b9(!1),[I,f]=b9(!0),[P,S]=b9(""),[g,z]=b9(0),[F,T]=b9([-100,100]),[n,s]=b9(!1),[d,Z0]=b9("point"),[m,W0]=b9([]),[u,N0]=b9(""),[q0,_0]=b9(""),g0=HQ({measuring:n,measureMode:d});g0.current={measuring:n,measureMode:d};let a0=(h)=>{R(h),H?.(h)};H7(()=>{if(!$){G([]);return}let h=!1,t;return _0(""),W0([]),N0(""),L(new Set),fetch(fW(`/artifacts/${$}/content`,J),{credentials:"same-origin"}).then((Q0)=>{if(!Q0.ok)throw Error("Unable to load assembly preview");return Q0.json()}).then((Q0)=>{if(h||!X.current)return;let E0=new ZQ({antialias:!0,alpha:!0,preserveDrawingBuffer:!0});E0.setPixelRatio(Math.min(devicePixelRatio,2)),E0.localClippingEnabled=!0;let l0=E0.domElement;l0.className="design-canvas",X.current.appendChild(l0);let w=new u6,v0=new I9(35,1,0.01,1e5),S0=new YQ(v0,l0);S0.enableDamping=!0,w.add(new r6(16777215,6583435,2));let A0=new e6(16777215,2);A0.position.set(1,-2,4),w.add(A0);let L0=new qJ;w.add(L0);let i0=[],w0=Q0.parts?.length?Q0.parts:[{...Q0,id:"output",name:"Output"}];for(let[a,K0]of[...w0,...Q0.keepouts||[]].entries()){let k0=new l9;if(k0.setAttribute("position",new B9(K0.vertices,3)),k0.setIndex(K0.triangles),K0.normals?.length===K0.vertices.length)k0.setAttribute("normal",new B9(K0.normals,3));else k0.computeVertexNormals();let F0=a>=w0.length,U0=new i6({color:K0.color||"#64748b",side:s9,roughness:0.6,transparent:F0,opacity:F0?0.18:1,depthWrite:!F0}),I0=new A9(k0,U0);I0.userData={part:K0,id:K0.id||`part_${a}`,instance:K0.instance_id||K0.id||"output",keepout:F0},I0.visible=K0.visible!==!1,L0.add(I0),i0.push(I0)}let C0=new WJ().setFromObject(L0),H9=C0.getCenter(new v),D9=C0.getSize(new v),e0=Math.max(D9.length(),1);S0.target.copy(H9),v0.up.set(0,0,1),v0.position.copy(H9).add(new v(1,-1,1).multiplyScalar(e0*1.6)),v0.near=Math.max(e0/1e4,0.001),v0.far=e0*100,v0.updateProjectionMatrix(),S0.update();let B=new qJ;w.add(B);let O=new g9;N.current={renderer:E0,scene:w,camera:v0,controls:S0,objects:i0,box:C0,center:H9,diagonal:e0,clip:O,helpers:B},L(new Set(w0.filter((a)=>a.visible===!1).map((a)=>a.id||""))),G(w0),T([C0.min.z,C0.max.z]),z(H9.z);function j(){if(!X.current)return;let{width:a,height:K0}=X.current.getBoundingClientRect();E0.setSize(a,Math.max(K0,1),!1),v0.aspect=a/Math.max(K0,1),v0.updateProjectionMatrix()}j();let x=new ResizeObserver(j);x.observe(X.current);let e=new J7,c=[0,0];function R0(a){c=[a.clientX,a.clientY]}function X0(a){if(Math.hypot(a.clientX-c[0],a.clientY-c[1])>4)return;let K0=l0.getBoundingClientRect();e.setFromCamera(new x0((a.clientX-K0.left)/K0.width*2-1,-(a.clientY-K0.top)/K0.height*2+1),v0);let k0=e.intersectObjects(i0.filter((I0)=>I0.visible&&!I0.userData.keepout)).filter((I0)=>!E0.clippingPlanes.some((_)=>_.distanceToPoint(I0.point)<0));if(!k0.length)return;let F0=k0[0],U0=F0.object;if(a0(U0.userData.instance),g0.current.measuring){let _=U0.userData.part.faceGroups?.find((J0)=>(F0.faceIndex??0)*3>=J0.start&&(F0.faceIndex??0)*3<J0.start+J0.count),G0=g0.current.measureMode==="face"?_?.faceId:void 0;if(g0.current.measureMode==="face"&&G0===void 0){_0("Exact face selection is unavailable for this nested preview; use instance distance or point measurement.");return}let C={visual:U0.userData.id,instance:U0.userData.instance,point:F0.point.toArray(),face:G0};W0((J0)=>J0.length>=2?[C]:[...J0,C])}}l0.addEventListener("pointerdown",R0),l0.addEventListener("pointerup",X0);let z0=0;function T0(){z0=requestAnimationFrame(T0),S0.update(),E0.render(w,v0)}T0(),t=()=>{cancelAnimationFrame(z0),x.disconnect(),l0.removeEventListener("pointerdown",R0),l0.removeEventListener("pointerup",X0),S0.dispose();for(let a of i0)a.geometry.dispose(),a.material.dispose();if(E0.dispose(),l0.remove(),N.current?.renderer===E0)N.current=null}}).catch((Q0)=>_0(Q0.message)),()=>{h=!0,t?.()}},[$,J]),H7(()=>{let h=N.current;if(!h)return;for(let t of h.objects){t.visible=t.userData.keepout?I:!M.has(t.userData.id)&&!M.has(t.userData.instance);let Q0=t.material,E0=t.userData.instance===D||t.userData.id===D;Q0.emissive.set(E0?"#5f3200":"#000000"),Q0.transparent=t.userData.keepout||A,Q0.opacity=t.userData.keepout?0.18:A?E:1,Q0.depthWrite=!Q0.transparent}if(P){let t=Number(P);h.clip.normal.set(t===0?1:0,t===1?1:0,t===2?1:0),h.clip.constant=-g,h.renderer.clippingPlanes=[h.clip]}else h.renderer.clippingPlanes=[]},[U,M,D,A,E,I,P,g]);function m0(h){let t=N.current;if(!t)return;let Q0=new WJ;for(let v0 of t.objects)if(!v0.userData.keepout&&(!h?.length||h.includes(v0.userData.instance)||h.includes(v0.userData.id)))Q0.expandByObject(v0);if(Q0.isEmpty())return;let E0=Q0.getCenter(new v),l0=Math.max(Q0.getSize(new v).length(),1),w=t.camera.position.clone().sub(t.controls.target).normalize();t.controls.target.copy(E0),t.camera.position.copy(E0).addScaledVector(w,l0*1.6),t.controls.update()}H7(()=>{if(W?.length)L(new Set),R(W[0]),m0(W);let h=N.current;if(!h)return;h.helpers.clear();let t=K?.details?.pairs||[K?.details];for(let Q0 of t){if(!Q0?.closest_points)continue;let E0=Q0.closest_points.map((w)=>new v(...w)),l0=new l9().setFromPoints(E0);h.helpers.add(new c6(l0,new o8({color:"#ef4444",depthTest:!1})));for(let w of E0){let v0=new A9(new B8(h.diagonal/200,10,8),new lJ({color:"#ef4444",depthTest:!1}));v0.position.copy(w),h.helpers.add(v0)}}for(let Q0 of[...K?.details?.regions||[],...K?.details?.feature_regions||[],...K?.details?.overhang_regions||[]].slice(0,512)){let E0=new A9(new B8(h.diagonal/150,8,6),new lJ({color:"#ef4444",depthTest:!1}));E0.position.fromArray(Q0.point),h.helpers.add(E0)}},[W,K,U]),H7(()=>{if(m.length!==2)return;if(d==="point"){let Q0=Math.hypot(...m[0].point.map((E0,l0)=>E0-m[1].point[l0]));N0(`Preview mesh points: ${Q0.toFixed(3)} mm (approximate)`);return}if(!Q||!Z)return;N0("Measuring exact CAD…");let h=!1,t={a:m[0].instance,b:m[1].instance,visual_a:m[0].visual,visual_b:m[1].visual};if(d==="face")t.face_a=m[0].face,t.face_b=m[1].face;return fetch(fW("/measure",J),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({design_id:Q,revision_id:Z,query:t})}).then((Q0)=>Q0.json()).then((Q0)=>{if(!h)N0(Q0.error||Q0.measurement?.message||"No measurement result")}).catch((Q0)=>{if(!h)N0(Q0.message)}),()=>{h=!0}},[m,d,Q,Z,J]);function o(h){let t=N.current;if(!t)return;let Q0={front:[0,-1,0],back:[0,1,0],top:[0,0,1],side:[1,0,0],iso:[1,-1,1]};if(t.camera.up.set(0,0,1),h==="top")t.camera.up.set(0,1,0);t.camera.position.copy(t.controls.target).add(new v(...Q0[h]).normalize().multiplyScalar(t.diagonal*1.6)),t.controls.update()}let H0={fontSize:11,padding:"4px 7px",border:"1px solid #475569",borderRadius:4,background:"#111827",color:"#e2e8f0",cursor:"pointer"};return HJ("div",{style:{position:"absolute",inset:0,display:"flex",flexDirection:"column"},children:[HJ("div",{style:{display:"flex",flexWrap:"wrap",gap:4,padding:8,zIndex:2,background:"#0b101acc"},children:[["front","top","side","iso"].map((h)=>E9("button",{style:H0,onClick:()=>o(h),children:h},h)),E9("button",{style:H0,onClick:()=>m0(),children:"Fit all"}),E9("button",{style:H0,disabled:!D,onClick:()=>m0([D]),children:"Fit selection"}),E9("button",{style:H0,onClick:()=>{L(new Set),V(!1)},children:"Show all"}),E9("button",{style:H0,onClick:()=>V(!A),children:"Transparency"}),A&&E9("input",{"aria-label":"Opacity",type:"range",min:".1",max:"1",step:".05",value:E,onChange:(h)=>q(Number(h.target.value))}),HJ("select",{"aria-label":"Section plane",style:H0,value:P,onChange:(h)=>{let t=h.target.value;if(S(t),t){let Q0=N.current,E0=Number(t);if(Q0)T([Q0.box.min.getComponent(E0),Q0.box.max.getComponent(E0)]),z(Q0.center.getComponent(E0))}},children:[E9("option",{value:"",children:"No section"}),E9("option",{value:"0",children:"Section X"}),E9("option",{value:"1",children:"Section Y"}),E9("option",{value:"2",children:"Section Z"})]}),P&&HJ(C1,{children:[E9("input",{"aria-label":"Section position",type:"range",min:F[0],max:F[1],step:".1",value:g,onChange:(h)=>z(Number(h.target.value))}),HJ("span",{children:[g.toFixed(1)," mm"]})]}),HJ("label",{style:{fontSize:11},children:[E9("input",{type:"checkbox",checked:I,onChange:(h)=>f(h.target.checked)}),"Keepouts"]}),E9("button",{style:H0,onClick:()=>{s(!n),W0([]),N0("")},children:n?"Stop measuring":"Measure"}),n&&HJ("select",{"aria-label":"Measurement mode",style:H0,value:d,onChange:(h)=>{Z0(h.target.value),W0([])},children:[E9("option",{value:"point",children:"Mesh points (approximate)"}),E9("option",{value:"instance",children:"Exact instance distance"}),E9("option",{value:"face",children:"Exact CAD faces"})]}),E9("button",{style:H0,onClick:()=>{let h=N.current;if(!h)return;h.renderer.render(h.scene,h.camera);let t=h.renderer.domElement.toDataURL("image/png");Y?.(t);let Q0=document.createElement("a");Q0.download=`design-${Q}-r${Z}-${Date.now()}.png`,Q0.href=t,Q0.click()},children:"Save inspection view"})]}),HJ("div",{style:{position:"relative",flex:1,minHeight:180},children:[E9("div",{ref:X,style:{position:"absolute",inset:0}}),U.length>0&&HJ("div",{style:{position:"absolute",top:8,left:8,maxHeight:"65%",width:200,overflow:"auto",background:"#0b101add",borderRadius:6,padding:8,fontSize:11},children:[E9("strong",{children:"Assembly instances"}),U.map((h,t)=>HJ("div",{style:{display:"flex",gap:5,marginTop:5,alignItems:"center"},children:[E9("input",{"aria-label":`Show ${h.name}`,type:"checkbox",checked:!M.has(h.id||"")&&!M.has(h.instance_id||h.id||""),onChange:(Q0)=>L((E0)=>{let l0=new Set(E0);return Q0.target.checked?l0.delete(h.id||""):l0.add(h.id||""),l0})}),E9("button",{style:{...H0,flex:1,textAlign:"left",borderColor:D===(h.instance_id||h.id)?"#f97316":"transparent",overflow:"hidden",textOverflow:"ellipsis"},title:h.id,onClick:()=>a0(h.instance_id||h.id||""),children:h.name||h.id}),E9("button",{title:"Isolate",style:H0,onClick:()=>{L(new Set(U.filter((Q0)=>Q0.id!==h.id).map((Q0)=>Q0.id||""))),a0(h.instance_id||h.id||""),m0([h.instance_id||h.id||""])},children:"◎"})]},h.id||t))]})]}),(q0||u||n)&&E9("div",{style:{padding:8,fontSize:12,background:"#0b101add",zIndex:2},children:q0||u||`Select two ${d==="face"?"faces":d==="instance"?"instances":"mesh points"} (${m.length}/2)`})]})}import{useCallback as vW,useEffect as X7,useMemo as _1,useState as Z9}from"react";import{jsx as $0,jsxs as b0}from"react/jsx-runtime";var I1="/api/apps/design/api",hW={schema:"apteva-design/v1",units:"mm",parameters:{width:{type:"number",default:80,min:20,max:300},depth:{type:"number",default:50,min:20,max:300},thickness:{type:"number",default:4,min:1,max:20},hole:{type:"number",default:4.2,min:1,max:20},margin:{type:"number",default:7,min:2,max:40}},operations:[{id:"plate",type:"box",size:["width","depth","thickness"]},{id:"h1",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["margin","margin",-1]},{id:"h2",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["width-margin","margin",-1]},{id:"h3",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["width-margin","depth-margin",-1]},{id:"h4",type:"cylinder",radius:"hole/2",height:"thickness+2",origin:["margin","depth-margin",-1]},{id:"drilled",type:"cut",inputs:["plate","h1","h2","h3","h4"]},{id:"finished",type:"fillet",input:"drilled",radius:1.5}],output:"finished",checks:[{type:"bounding_box",max:[300,300,20]},{type:"body_count",equals:1}]};function UQ(J,$){let Q=J.includes("?")?"&":"?";return`${I1}${J}${Q}project_id=${encodeURIComponent($)}`}async function T9(J,$,Q){let Z=await fetch(UQ(J,$),{credentials:"same-origin",...Q,headers:{"Content-Type":"application/json",...Q?.headers||{}}}),W=await Z.json().catch(()=>({}));if(!Z.ok)throw Error(W.error||Z.statusText);return W}function oJ(J,$=1){return Number.isFinite(J)?J.toLocaleString(void 0,{maximumFractionDigits:$}):"—"}function P1({projectId:J}){let[$,Q]=Z9([]),[Z,W]=Z9(null),[K,H]=Z9(null),[Y,X]=Z9(JSON.stringify(hW,null,2)),[N,U]=Z9({}),[G,D]=Z9("Mounting plate"),[R,M]=Z9(""),[L,E]=Z9(""),[q,A]=Z9(null),[V,I]=Z9(""),[f,P]=Z9("model"),[S,g]=Z9(""),[z,F]=Z9(""),[T,n]=Z9(null),[s,d]=Z9(null),[Z0,m]=Z9([]),[W0,u]=Z9(null),[N0,q0]=Z9(""),[_0,g0]=Z9(!1),[a0,m0]=Z9(""),[o,H0]=Z9(""),[h,t]=Z9(""),[Q0,E0]=Z9(""),[l0,w]=Z9(""),[v0,S0]=Z9("manufacturer_cad"),[A0,L0]=Z9("0"),[i0,w0]=Z9(!1),[C0,H9]=Z9(null);async function D9(C){g("Importing STEP"),F("");try{let J0=new FormData;J0.append("file",C),J0.append("evidence",JSON.stringify({model:a0,manufacturer:o,hardware_revision:Q0,source:h,date:new Date().toISOString().slice(0,10),geometry_status:v0,complete:i0,uncertainty_mm:Number(A0),omissions:l0.split(`
`).filter(Boolean)}));let r=await fetch(UQ("/sources",J),{method:"POST",credentials:"same-origin",body:J0}),i=await r.json();if(!r.ok)throw Error(i.error||"Import failed");await O(),W(i.design.id),g0(!1),await j(i.design.id),P("model")}catch(J0){F(J0.message)}finally{g("")}}async function e0(){if(!K)return;g("Generating enclosure"),F("");try{let C=await T9("/assembly-enclosures",J,{method:"POST",body:JSON.stringify({assembly_design_id:K.id,assembly_revision_id:K.current_revision_id,options:{}})});await O(),W(C.design.id),await j(C.design.id)}catch(C){F(C.message)}finally{g("")}}function B(C,J0,r=!1){if(!x)return;let i=structuredClone(x),M0=i.assembly?.instances.find((r0)=>r0.id===N0);if(!M0)return;let f0=Number(J0);if(!Number.isFinite(f0))return;if(M0.transform||={},r)M0.transform.rotate=[{angle:f0,direction:[0,0,1]}];else M0.transform.translate||=[0,0,0],M0.transform.translate[C]=f0;X(JSON.stringify(i,null,2))}let O=vW(async()=>{let C=await T9("/designs?status=all",J);if(Q(C.designs||[]),!Z&&C.designs?.length)W(C.designs[0].id)},[J,Z]),j=vW(async(C)=>{let r=(await T9(`/designs/${C}`,J)).design;H(r),X(JSON.stringify(r.current_revision?.definition||hW,null,2)),U(r.current_revision?.parameters||{}),n(r.latest_build?{report:r.latest_build.report,checks:r.latest_build.checks,artifacts:r.artifacts}:{artifacts:r.artifacts})},[J]);X7(()=>{O().catch((C)=>F(C.message))},[O]),X7(()=>{if(Z)j(Z).catch((C)=>F(C.message))},[Z,j]),X7(()=>{T9("/pcb-source",J).then((C)=>A(!!C.bound)).catch(()=>A(!1))},[J]),X7(()=>{T9("/examples",J).then((C)=>d(C.featured?.open_rover?.definition||null)).catch(()=>d(null))},[J]);let x=_1(()=>{try{return JSON.parse(Y)}catch{return null}},[Y]),e=x?.parameters||{};async function c(){if(!x)return F("The design JSON is not valid.");g("Creating"),F("");try{let C=await T9("/designs",J,{method:"POST",body:JSON.stringify({name:G,kind:"parametric",definition:x,parameters:N,note:"Initial design"})});await O(),W(C.design.id)}catch(C){F(C.message)}finally{g("")}}async function R0(){if(!K||!x)return F("Select a design and fix the source JSON first.");g("Saving revision"),F("");try{await T9(`/designs/${K.id}/revisions`,J,{method:"POST",body:JSON.stringify({expected_parent_id:K.current_revision_id,definition:x,parameters:N,note:V})}),I(""),await j(K.id),await O()}catch(C){F(C.message)}finally{g("")}}async function X0(){let C=Number(R),J0=Number(L||0);if(!Number.isInteger(C)||C<=0)return F("Enter a valid PCB Studio design ID.");g("Creating PCB enclosure"),F("");try{let r=await T9("/pcb-enclosures",J,{method:"POST",body:JSON.stringify({pcb_design_id:C,pcb_revision_id:J0,name:`PCB ${C} enclosure`})});await O(),W(r.design.id),P("model")}catch(r){F(r.message)}finally{g("")}}async function z0(){if(!s)return F("The Open Rover reference design is unavailable.");g("Creating Open Rover"),F("");try{let C=await T9("/designs",J,{method:"POST",body:JSON.stringify({name:"Apteva Open Rover",description:"Fully printable modular open-hardware robot chassis.",kind:"parametric",tags:["robot","open-hardware","assembly","3d-printing"],definition:s,parameters:{},note:"Open Rover reference design"})});await T9(`/designs/${C.design.id}/build`,J,{method:"POST",body:JSON.stringify({formats:["mesh-json","glb"]})}),await O(),W(C.design.id),await j(C.design.id),P("model")}catch(C){F(C.message)}finally{g("")}}async function T0(){if(!K?.current_revision)return F("Select a component design first.");g("Creating linked assembly"),F("");try{let C=await T9("/assemblies",J,{method:"POST",body:JSON.stringify({name:`${K.name} Assembly`,description:`Assembly linked to ${K.name} r${K.current_revision.revision_number}.`,components:[{design_id:K.id,revision_id:K.current_revision.id}]})});await O(),W(C.design.id),await j(C.design.id),P("source")}catch(C){F(C.message)}finally{g("")}}async function a(){if(!K||!x?.provenance||x.provenance.kind!=="pcb-mechanical-envelope")return;g("Refreshing PCB enclosure"),F("");try{await T9(`/designs/${K.id}/refresh-pcb`,J,{method:"POST",body:JSON.stringify({expected_parent_id:K.current_revision_id,pcb_revision_id:0})}),await j(K.id),await O()}catch(C){F(C.message)}finally{g("")}}async function K0(){if(!K)return;g("Checking linked components"),F("");try{await T9(`/designs/${K.id}/refresh-components`,J,{method:"POST",body:JSON.stringify({expected_parent_id:K.current_revision_id})}),await j(K.id),await O()}catch(C){F(C.message)}finally{g("")}}async function k0(C){if(!K)return F("Create or select a design first.");g(C.length>2?"Exporting":"Building"),F("");try{let J0=await T9(`/designs/${K.id}/build`,J,{method:"POST",body:JSON.stringify({formats:C})});n(J0),await j(K.id)}catch(J0){F(J0.message)}finally{g("")}}async function F0(){if(!K)return;g("Packaging"),F("");try{await T9(`/designs/${K.id}/package`,J,{method:"POST",body:"{}"}),await j(K.id)}catch(C){F(C.message)}finally{g("")}}let U0=T?.artifacts||K?.artifacts||[],I0=U0.filter((C)=>C.format==="mesh-json"&&!C.metadata?.part_id).sort((C,J0)=>J0.id-C.id)[0],_=T?.report||K?.latest_build?.report,G0=T?.checks||K?.latest_build?.checks||[];return b0("div",{className:"design-studio h-full flex flex-col bg-bg text-text overflow-hidden",children:[b0("header",{className:"design-header h-14 px-4 border-b border-border flex items-center gap-3 shrink-0",children:[$0("div",{className:"w-8 h-8 rounded-lg bg-accent/15 text-accent grid place-items-center text-lg",children:"◇"}),b0("div",{children:[$0("h1",{className:"text-sm font-semibold leading-tight",children:"Design Studio"}),$0("p",{className:"text-[11px] text-text-muted",children:"Open hardware · assemblies · parametric CAD"})]}),$0("span",{className:"flex-1"}),$0("button",{className:"design-button",disabled:!!S,onClick:()=>g0(!_0),children:"Import STEP"}),$0("button",{className:"design-button",disabled:!!S||!K,onClick:e0,children:"Enclosure template"}),$0("button",{className:"design-button",disabled:!!S||!K,onClick:()=>k0(["mesh-json","glb"]),children:"Build preview"}),$0("button",{className:"design-button",disabled:!!S||!K,onClick:()=>k0(["step","stl","3mf","glb"]),children:"Export CAD"}),$0("button",{className:"design-button design-button-primary",disabled:!!S||!K,onClick:F0,children:"Manufacturing ZIP"})]}),_0&&b0("div",{className:"p-4 border-b border-border text-xs",style:{display:"grid",gridTemplateColumns:"repeat(4,minmax(0,1fr))",gap:8},children:[$0("input",{className:"field",placeholder:"Exact manufacturer model (required)",value:a0,onChange:(C)=>m0(C.target.value)}),$0("input",{className:"field",placeholder:"Manufacturer",value:o,onChange:(C)=>H0(C.target.value)}),$0("input",{className:"field",placeholder:"Hardware revision",value:Q0,onChange:(C)=>E0(C.target.value)}),$0("input",{className:"field",placeholder:"CAD source URL",value:h,onChange:(C)=>t(C.target.value)}),b0("select",{className:"field",value:v0,onChange:(C)=>S0(C.target.value),children:[$0("option",{value:"manufacturer_cad",children:"Manufacturer CAD"}),$0("option",{value:"drawing_derived",children:"Drawing derived"}),$0("option",{value:"measured",children:"Physically measured"}),$0("option",{value:"estimated",children:"Estimated"})]}),$0("input",{className:"field",type:"number",min:"0",step:".01","aria-label":"Dimensional uncertainty in mm",value:A0,onChange:(C)=>L0(C.target.value)}),$0("textarea",{className:"field",placeholder:"Known omissions, one per line",value:l0,onChange:(C)=>w(C.target.value)}),b0("label",{children:[$0("input",{type:"checkbox",checked:i0,onChange:(C)=>w0(C.target.checked)}),"Complete geometry evidence"]}),b0("label",{children:["STEP file (up to 96 MiB)",$0("input",{type:"file",accept:".step,.stp",disabled:!!S||!a0.trim(),onChange:(C)=>{if(C.target.files?.[0])D9(C.target.files[0])}})]}),$0("span",{children:"Original file, source hash and assembly placements are retained. Missing features require measured proxy solids."})]}),z&&$0("div",{className:"px-4 py-2 text-xs border-b border-red/30 bg-red/10 text-red",children:z}),S&&$0("div",{className:"h-0.5 bg-accent animate-pulse"}),b0("div",{className:"design-workspace",children:[b0("aside",{className:"design-library",children:[$0("div",{className:"text-[10px] uppercase tracking-widest text-text-muted mb-2",children:"Designs"}),b0("div",{className:"space-y-1",children:[$.map((C)=>b0("button",{onClick:()=>W(C.id),className:"design-list-item","data-selected":Z===C.id,children:[$0("div",{className:"design-list-name",children:C.name}),b0("div",{className:"design-list-meta",children:[C.kind,C.id===K?.id&&K.current_revision?` · r${K.current_revision.revision_number}`:""]})]},C.id)),!$.length&&$0("p",{className:"text-xs text-text-muted p-2",children:"No designs yet. Start with the template."})]}),b0("div",{className:"mt-5 pt-4 border-t border-border space-y-2",children:[$0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"New design"}),$0("input",{className:"field",value:G,onChange:(C)=>D(C.target.value),placeholder:"Design name"}),$0("button",{className:"design-button design-button-primary design-full",disabled:!!S||!G.trim()||!x,onClick:c,children:"Create from editor"}),$0("button",{className:"design-button design-full",disabled:!!S||!K?.current_revision,onClick:T0,children:"Start assembly with selected"})]}),b0("div",{className:"mt-5 pt-4 border-t border-border space-y-2",children:[$0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"Featured open hardware"}),b0("button",{className:"design-button design-button-rover design-full",disabled:!!S||!s,onClick:z0,children:[$0("span",{children:"◈"})," Create Open Rover"]}),$0("p",{className:"text-[10px] leading-4 text-text-muted",children:"Printable chassis, TPU tires, hubs, modular deck, interfaces, joints and release BOM."})]}),b0("div",{className:"pcb-import mt-5 pt-4 border-t border-border space-y-2","data-bound":q===!0,children:[b0("div",{className:"flex items-center justify-between gap-2",children:[$0("label",{className:"text-[10px] uppercase tracking-widest text-text-muted",children:"PCB enclosure"}),$0("span",{className:"pcb-binding-dot",title:q?"PCB Studio bound":"PCB Studio not bound"})]}),$0("input",{className:"field",inputMode:"numeric",value:R,onChange:(C)=>M(C.target.value),placeholder:"PCB design ID"}),$0("input",{className:"field",inputMode:"numeric",value:L,onChange:(C)=>E(C.target.value),placeholder:"Revision ID · current if empty"}),$0("button",{className:"design-button design-full",disabled:!!S||!q||!R.trim(),onClick:X0,children:"Generate linked enclosure"}),q===!1&&$0("p",{className:"text-[10px] leading-4 text-text-muted",children:"Bind PCB Studio as the optional PCB source to enable this workflow."})]})]}),b0("main",{className:"design-main",children:[b0("div",{className:"h-11 px-3 border-b border-border flex items-center gap-2 bg-bg",children:[$0("button",{className:f==="model"?"tab-active":"tab",onClick:()=>P("model"),children:"3D model"}),$0("button",{className:f==="source"?"tab-active":"tab",onClick:()=>P("source"),children:"Geometry source"}),$0("span",{className:"flex-1"}),K&&b0("span",{className:"text-[10px] text-text-muted font-mono",children:["r",K.current_revision?.revision_number," · ",K.current_revision?.source_sha256?.slice(0,10)]})]}),f==="model"?b0("div",{className:"design-viewport",children:[$0(XQ,{projectId:J,artifactId:I0?.id,designId:K?.id,revisionId:K?.current_revision_id,focus:Z0,issue:W0,onSelect:q0,onCapture:H9}),!I0&&$0("div",{className:"absolute inset-0 grid place-items-center pointer-events-none",children:b0("div",{className:"text-center",children:[$0("div",{className:"text-4xl text-text-muted/30 mb-3",children:"◇"}),$0("p",{className:"text-sm text-text-muted",children:"Build a preview to inspect the model"}),$0("p",{className:"text-[11px] text-text-muted/60 mt-1",children:"Drag to orbit · wheel to zoom"})]})}),_&&b0("div",{className:"absolute left-3 bottom-3 flex gap-1.5",children:[$0(NQ,{label:"X",value:`${oJ(_.bounds.size[0])} mm`}),$0(NQ,{label:"Y",value:`${oJ(_.bounds.size[1])} mm`}),$0(NQ,{label:"Z",value:`${oJ(_.bounds.size[2])} mm`})]})]}):$0("textarea",{className:"flex-1 min-h-0 resize-none bg-[#0b1016] p-5 font-mono text-xs leading-5 outline-none text-[#cbd5e1]",spellCheck:!1,value:Y,onChange:(C)=>X(C.target.value)})]}),b0("aside",{className:"design-inspector",children:[b0(XJ,{title:"Parameters",children:[x?.print_profiles?.some((C)=>C.fits_mm)&&b0("label",{className:"text-xs",children:["Fit profile",b0("select",{className:"field",value:x.fit_profile_id||"",onChange:(C)=>{let J0=structuredClone(x);J0.fit_profile_id=C.target.value||void 0,X(JSON.stringify(J0,null,2))},children:[$0("option",{value:"",children:"Parameter defaults"}),x.print_profiles.filter((C)=>C.fits_mm).map((C)=>$0("option",{value:C.id,children:C.name},C.id))]}),$0("p",{className:"text-[10px] mt-1",children:"Save a revision to apply. Explicit parameter values override profile allowances."})]}),Object.entries(e).map(([C,J0])=>b0("label",{className:"design-param",children:[b0("span",{className:"design-param-head",children:[$0("span",{children:C}),b0("span",{className:"design-param-value",children:[N[C]??J0.default," mm"]})]}),$0("input",{className:"design-range",type:"range",min:J0.min??0,max:J0.max??Math.max((J0.default||1)*3,10),step:J0.step??0.1,value:N[C]??J0.default,onChange:(r)=>U((i)=>({...i,[C]:Number(r.target.value)}))})]},C)),!Object.keys(e).length&&$0("p",{className:"text-xs text-text-muted",children:"No exposed parameters."})]}),b0(XJ,{title:"Revision",children:[$0("textarea",{className:"field min-h-16 resize-y",value:V,onChange:(C)=>I(C.target.value),placeholder:"What changed?"}),$0("button",{className:"design-button design-full design-revision-button",disabled:!!S||!K||!x,onClick:R0,children:"Save immutable revision"})]}),x?.provenance?.kind==="pcb-mechanical-envelope"&&b0(XJ,{title:"PCB source",children:[$0(S9,{label:"PCB design",value:String(x.provenance.source_design_id)}),$0(S9,{label:"PCB revision",value:String(x.provenance.source_revision_id)}),$0(S9,{label:"Source hash",value:String(x.provenance.source_sha256||"").slice(0,12)}),$0("button",{className:"design-button design-full design-revision-button",disabled:!!S||!q||!K,onClick:a,children:"Refresh from current PCB revision"})]}),!!x?.parts?.some((C)=>C.source)&&b0(XJ,{title:"Linked components",children:[$0("div",{className:"space-y-2",children:x.parts.filter((C)=>C.source).map((C)=>b0("div",{className:"rounded border border-border p-2 text-[11px]",children:[$0("div",{className:"font-medium",children:C.name}),b0("div",{className:"text-text-muted mt-1",children:["Design ",C.source.design_id," · revision ",C.source.revision_id]}),$0("div",{className:"font-mono text-text-muted",children:String(C.source.source_sha256).slice(0,12)})]},C.id))}),$0("button",{className:"design-button design-full design-revision-button",disabled:!!S||!K,onClick:K0,children:"Update to current source revisions"}),$0("p",{className:"text-[10px] leading-4 text-text-muted mt-2",children:"Updates create a new assembly revision. Existing revisions stay pinned and reproducible."})]}),N0&&x?.assembly&&b0(XJ,{title:"Instance placement",children:[$0("p",{className:"text-xs mb-2",children:N0}),[0,1,2].map((C)=>b0("label",{className:"text-xs",children:[["X","Y","Z"][C]," mm",$0("input",{className:"field",type:"number",step:".1",value:x.assembly.instances.find((J0)=>J0.id===N0)?.transform?.translate?.[C]??0,onChange:(J0)=>B(C,J0.target.value)})]},C)),b0("label",{className:"text-xs",children:["Rotation about Z (degrees)",$0("input",{className:"field",type:"number",value:x.assembly.instances.find((C)=>C.id===N0)?.transform?.rotate?.[0]?.angle??0,onChange:(C)=>B(2,C.target.value,!0)})]}),$0("button",{className:"design-button",disabled:!!S,onClick:R0,children:"Save placement revision"}),$0("p",{className:"text-[10px] mt-2",children:"Save, then build to validate the new placement. Arbitrary rotations and datum alignment are available in Source."})]}),C0&&b0(XJ,{title:"Inspection capture",children:[$0("img",{src:C0,alt:"Saved assembly inspection",style:{width:"100%"}}),$0("p",{className:"text-[10px]",children:"Downloaded PNG. Attach it to revision-bound test evidence for release."})]}),_&&b0(XJ,{title:"Measurements",children:[$0(S9,{label:"Bodies",value:String(_.body_count||1)}),$0(S9,{label:"Mass",value:_.mass_g?`${oJ(_.mass_g)} g`:"—"}),$0(S9,{label:"Volume",value:`${oJ(_.volume_mm3)} mm³`}),$0(S9,{label:"Surface",value:`${oJ(_.surface_area_mm2)} mm²`}),$0(S9,{label:"Faces",value:String(_.face_count)}),$0(S9,{label:"Triangles",value:String(_.triangle_count)})]}),x?.open_hardware&&b0(XJ,{title:"Open hardware",children:[$0(S9,{label:"Project",value:x.open_hardware.project_name}),$0(S9,{label:"Version",value:x.open_hardware.version}),$0(S9,{label:"License",value:x.open_hardware.license}),$0(S9,{label:"Parts",value:String(x.parts?.length||0)}),$0(S9,{label:"Instances",value:String(x.assembly?.instances?.length||0)}),$0(S9,{label:"Joints",value:String(x.assembly?.joints?.length||0)})]}),!!G0.length&&$0(XJ,{title:"Validation",children:$0("div",{className:"space-y-2",children:G0.map((C,J0)=>b0("button",{onClick:()=>{m(C.instances||[]),u(C)},className:"design-validation-issue flex gap-2 text-xs text-left w-full",title:"Focus affected instances",children:[$0("span",{className:C.status==="pass"?"text-green":"text-red",children:C.status==="pass"?"●":"×"}),b0("span",{className:"text-text-muted",children:[C.status,": ",C.message]})]},J0))})}),$0(XJ,{title:"Artifacts",children:b0("div",{className:"space-y-1.5",children:[U0.filter((C)=>C.format!=="mesh-json").map((C)=>b0("a",{className:"flex items-center gap-2 rounded border border-border p-2 hover:border-accent/50 text-xs",href:UQ(`/artifacts/${C.id}/content`,J),children:[$0("span",{className:"font-mono uppercase text-accent w-9",children:C.format}),$0("span",{className:"truncate flex-1",children:C.name}),b0("span",{className:"text-text-muted",children:[oJ(C.size_bytes/1024),"K"]})]},C.id)),!U0.some((C)=>C.format!=="mesh-json")&&$0("p",{className:"text-xs text-text-muted",children:"Exports will appear here."})]})})]})]}),$0("style",{children:A1})]})}function XJ({title:J,children:$}){return b0("section",{className:"design-section",children:[$0("h2",{className:"design-section-title",children:J}),$]})}function S9({label:J,value:$}){return b0("div",{className:"design-row",children:[$0("span",{className:"text-text-muted",children:J}),$0("span",{className:"font-mono",children:$})]})}function NQ({label:J,value:$}){return b0("div",{className:"rounded bg-black/50 backdrop-blur px-2 py-1 text-[10px]",children:[$0("span",{className:"text-text-muted mr-1",children:J}),$0("span",{className:"font-mono",children:$})]})}var A1=`
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
`;export{P1 as default};

//# debugId=2414A373A69936EA64756E2164756E21
