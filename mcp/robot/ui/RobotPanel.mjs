import{useEffect as P,useRef as v}from"react";import{jsx as n}from"react/jsx-runtime";var k="/api/apps/robot",U=24,D="text-xs px-2 py-0.5 rounded-full ml-1",g={idle:"bg-bg-hover text-text-muted",active:"bg-info/20 text-info",success:"bg-success/20 text-success",timeout:"bg-warn/20 text-warn"},B="bg-bg-input border border-border rounded text-text font-semibold cursor-pointer hover:bg-bg-hover active:bg-bg-card disabled:opacity-50 disabled:cursor-default",f="px-3 py-1 rounded border border-border bg-bg-input text-text cursor-pointer hover:bg-bg-hover disabled:opacity-50 disabled:cursor-default text-sm";function x(){let J=getComputedStyle(document.documentElement),q=(K,Y)=>J.getPropertyValue(`--${K}`).trim()||Y;return{floor:q("bg-input","#f4f4f5"),wall:q("text-dim","#888"),goal:q("success","#22c55e"),agent:q("info","#3b82f6"),grid:q("border-subtle","#333"),trail:q("info","#3b82f6")}}function I(J){J.innerHTML=`
    <div class="p-6 text-sm text-text">
      <h2 class="text-lg font-semibold text-text mb-1">Robot</h2>
      <p class="text-text-muted mb-4">Agent navigation eval sandbox. Pick a scenario, start an episode, watch the agent's tool calls land in the feed.</p>

      <div class="flex items-center gap-2 mb-4 flex-wrap">
        <span class="text-xs uppercase tracking-wider text-text-dim">Scenario</span>
        <select data-role="scenario-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <button data-role="start" class="bg-accent text-bg hover:bg-accent-hover rounded px-3 py-1 text-sm font-medium cursor-pointer disabled:opacity-50 disabled:cursor-default">Start episode</button>
        <span class="text-xs uppercase tracking-wider text-text-dim ml-2">Episode</span>
        <select data-role="episode-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <span data-role="status" class="${D} ${g.idle}">idle</span>
      </div>

      <div class="flex gap-4 items-start flex-wrap">
        <div class="flex-shrink-0">
          <div class="text-xs uppercase tracking-wider text-text-dim mb-1">World</div>
          <div class="bg-bg-card border border-border rounded p-3 inline-block">
            <canvas data-role="grid"></canvas>
          </div>
          <div class="flex gap-4 text-sm text-text-muted mt-2">
            <span>steps <b class="text-text" data-role="m-steps">—</b></span>
            <span>optimal <b class="text-text" data-role="m-optimal">—</b></span>
            <span>ratio <b class="text-text" data-role="m-ratio">—</b></span>
          </div>

          <div class="flex gap-4 items-start mt-3">
            <div class="grid gap-1" style="grid-template-columns: repeat(3, 36px); grid-template-rows: repeat(3, 36px);">
              <button data-dir="N" class="${B}" style="grid-column: 2; grid-row: 1;" title="Move north (↑)">↑</button>
              <button data-dir="W" class="${B}" style="grid-column: 1; grid-row: 2;" title="Move west (←)">←</button>
              <button data-dir="E" class="${B}" style="grid-column: 3; grid-row: 2;" title="Move east (→)">→</button>
              <button data-dir="S" class="${B}" style="grid-column: 2; grid-row: 3;" title="Move south (↓)">↓</button>
            </div>
            <div class="flex flex-col gap-1 mt-[22px]">
              <button data-role="pick" class="${f}" title="Pick up an item (inert in v0.1)">pick</button>
              <button data-role="drop" class="${f}" title="Drop the held item (inert in v0.1)">drop</button>
            </div>
            <span class="text-xs text-text-dim self-center">arrow keys also move</span>
          </div>
        </div>

        <div class="flex-1 min-w-0">
          <div class="text-xs uppercase tracking-wider text-text-dim mb-1">Activity</div>
          <div data-role="feed" class="max-h-[480px] overflow-auto bg-bg-card border border-border rounded p-2"></div>
        </div>
      </div>
    </div>
  `;let q={scenPicker:J.querySelector('[data-role="scenario-picker"]'),epPicker:J.querySelector('[data-role="episode-picker"]'),startBtn:J.querySelector('[data-role="start"]'),statusPill:J.querySelector('[data-role="status"]'),canvas:J.querySelector('[data-role="grid"]'),feed:J.querySelector('[data-role="feed"]'),mSteps:J.querySelector('[data-role="m-steps"]'),mOptimal:J.querySelector('[data-role="m-optimal"]'),mRatio:J.querySelector('[data-role="m-ratio"]'),dpadBtns:Array.from(J.querySelectorAll("[data-dir]")),pickBtn:J.querySelector('[data-role="pick"]'),dropBtn:J.querySelector('[data-role="drop"]')},K={scenarios:[],episodes:[],activeEpisode:null,activeScenario:null,pollHandle:null};q.scenPicker.addEventListener("change",()=>{K.activeScenario=K.scenarios.find((z)=>z.id===q.scenPicker.value),M(q.canvas,K.activeScenario,null,[])}),q.epPicker.addEventListener("change",()=>W(q.epPicker.value)),q.startBtn.addEventListener("click",w);for(let z of q.dpadBtns)z.addEventListener("click",()=>S(z.dataset.dir));q.pickBtn.addEventListener("click",()=>b("pick")),q.dropBtn.addEventListener("click",()=>b("drop"));let Y=(z)=>{let O=z.target;if(O&&/^(INPUT|SELECT|TEXTAREA)$/.test(O.tagName))return;let V={ArrowUp:"N",ArrowDown:"S",ArrowLeft:"W",ArrowRight:"E"}[z.key];if(!V)return;if(!_())return;z.preventDefault(),S(V)};document.addEventListener("keydown",Y),C();let $=window.setInterval(C,5000);return function(){window.clearInterval($),document.removeEventListener("keydown",Y),X()};async function C(){await Promise.all([Q(),j()])}async function Q(){let z=await fetch(`${k}/scenarios`);if(!z.ok)return;let O=await z.json();K.scenarios=O.scenarios||[];let Z=q.scenPicker.value;if(q.scenPicker.innerHTML=K.scenarios.map((V)=>`<option value="${V.id}">${G(V.name)}</option>`).join(""),Z)q.scenPicker.value=Z;if(K.activeScenario=K.scenarios.find((V)=>V.id===q.scenPicker.value)||K.scenarios[0],!K.activeEpisode&&K.activeScenario)M(q.canvas,K.activeScenario,null,[])}async function j(){let z=await fetch(`${k}/episodes?limit=20`);if(!z.ok)return;let O=await z.json();K.episodes=O.episodes||[];let Z=q.epPicker.value;if(q.epPicker.innerHTML='<option value="">— recent episodes —</option>'+K.episodes.map((V)=>{let N=V.terminal_reason?` (${V.terminal_reason})`:" (active)";return`<option value="${V.episode_id}">${E(V.episode_id)} · ${G(V.scenario_id)}${N}</option>`}).join(""),Z)q.epPicker.value=Z;if(!K.activeEpisode&&K.episodes.length>0){let V=K.episodes[0];q.epPicker.value=V.episode_id,W(V.episode_id)}}async function w(){let z=q.scenPicker.value;if(!z)return;q.startBtn.disabled=!0;try{let O=await fetch(`${k}/episodes`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({scenario_id:z,model:"manual"})});if(!O.ok){alert(`Start failed: ${await O.text()}`);return}let Z=await O.json();await j(),q.epPicker.value=Z.episode_id,W(Z.episode_id)}finally{q.startBtn.disabled=!1}}async function W(z){if(!z){X(),K.activeEpisode=null,M(q.canvas,K.activeScenario,null,[]),q.feed.innerHTML="",H(q.statusPill,"idle","idle"),q.mSteps.textContent="—",q.mOptimal.textContent="—",q.mRatio.textContent="—",h();return}await R(z),F(z)}function F(z){X(),K.pollHandle=window.setInterval(()=>R(z),1500)}function X(){if(K.pollHandle)window.clearInterval(K.pollHandle);K.pollHandle=null}async function R(z){let O=await fetch(`${k}/episodes/${z}`);if(!O.ok)return;let Z=await O.json(),V=Z.episode,N=Z.steps||[];K.activeEpisode=V;let m=K.scenarios.find((A)=>A.id===V.scenario_id);if(m)M(q.canvas,m,V.position,N);if(L(q.feed,N),u(q,V),y(q.statusPill,V),h(),V.terminal_reason)X()}function _(){let z=K.activeEpisode;return z&&!z.terminal_reason}function h(){let z=_();for(let O of q.dpadBtns)O.disabled=!z;q.pickBtn.disabled=!z,q.dropBtn.disabled=!z}async function S(z){let O=K.activeEpisode;if(!O||O.terminal_reason)return;try{await fetch(`${k}/episodes/${O.episode_id}/move`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({direction:z})})}catch(Z){}R(O.episode_id)}async function b(z){let O=K.activeEpisode;if(!O||O.terminal_reason)return;try{await fetch(`${k}/episodes/${O.episode_id}/${z}`,{method:"POST"})}catch(Z){}R(O.episode_id)}function u(z,O){z.mSteps.textContent=O.steps,z.mOptimal.textContent=O.optimal_steps||"—",z.mRatio.textContent=O.optimality_ratio?O.optimality_ratio.toFixed(2):"—"}function y(z,O){if(O.terminal_reason==="success")H(z,"success","success");else if(O.terminal_reason==="timeout")H(z,"timeout","timeout");else H(z,"active","active")}}function H(J,q,K){J.className=`${D} ${g[q]}`,J.textContent=K}function M(J,q,K,Y){if(!J||!q)return;let $=q.grid.width,C=q.grid.height;J.width=$*U+1,J.height=C*U+1;let Q=J.getContext("2d"),j=x();Q.fillStyle=j.floor,Q.fillRect(0,0,J.width,J.height);let w=q.walls||[];Q.fillStyle=j.wall;for(let[W,F]of w)Q.fillRect(W*U,F*U,U,U);if(q.goal){let[W,F]=q.goal;Q.fillStyle=j.goal,Q.fillRect(W*U+4,F*U+4,U-8,U-8)}if(Y&&Y.length>0){Q.strokeStyle=j.trail,Q.globalAlpha=0.35,Q.lineWidth=2,Q.beginPath();let W=!1;for(let F of Y){let X=F.pos_x*U+U/2,R=F.pos_y*U+U/2;if(!W)Q.moveTo(X,R),W=!0;else Q.lineTo(X,R)}Q.stroke(),Q.globalAlpha=1}if(K){let[W,F]=K;Q.fillStyle=j.agent,Q.beginPath(),Q.arc(W*U+U/2,F*U+U/2,U/3,0,Math.PI*2),Q.fill()}Q.strokeStyle=j.grid,Q.lineWidth=0.5,Q.beginPath();for(let W=0;W<=$;W++)Q.moveTo(W*U+0.5,0),Q.lineTo(W*U+0.5,C*U);for(let W=0;W<=C;W++)Q.moveTo(0,W*U+0.5),Q.lineTo($*U,W*U+0.5);Q.stroke()}function L(J,q){if(!q||q.length===0){J.innerHTML='<div class="text-text-dim text-xs p-2">no steps yet</div>';return}let K=[...q].sort((Y,$)=>$.step-Y.step);J.innerHTML=K.map((Y)=>{let $="";try{let C=JSON.parse(Y.result);if(Y.tool==="move")$=C.moved?"→ ok":`→ ${C.reason}`;else $=C.reason||JSON.stringify(C).slice(0,60)}catch{$=(Y.result?.slice?Y.result.slice(0,60):"")||""}return`<div class="grid gap-2 px-1 py-0.5 border-b border-border-subtle last:border-b-0 font-mono text-xs" style="grid-template-columns: 36px 90px 1fr;">
        <span class="text-text-dim text-right">${Y.step}</span>
        <span class="text-info">${G(Y.tool)}</span>
        <span class="text-text whitespace-nowrap overflow-hidden text-ellipsis" title="${G($)}">${G($)}</span>
      </div>`}).join("")}function E(J){return J?J.slice(-6):""}function G(J){return String(J??"").replace(/[&<>"']/g,(q)=>{return{"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[q]})}function T(){let J=v(null);return P(()=>{if(!J.current)return;return I(J.current)},[]),n("div",{ref:J,className:"h-full overflow-auto"})}export{T as default};

//# debugId=60FC64BFD70F87C564756E2164756E21
