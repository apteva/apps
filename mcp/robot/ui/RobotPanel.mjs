import{useEffect as x,useRef as E}from"react";import{jsx as c}from"react/jsx-runtime";var R="/api/apps/robot",V=24,u="text-xs px-2 py-0.5 rounded-full ml-1",g={idle:"bg-bg-hover text-text-muted",active:"bg-info/20 text-info",success:"bg-success/20 text-success",timeout:"bg-warn/20 text-warn"},_="bg-bg-input border border-border rounded text-text font-semibold cursor-pointer hover:bg-bg-hover active:bg-bg-card disabled:opacity-50 disabled:cursor-default",w="px-3 py-1 rounded border border-border bg-bg-input text-text cursor-pointer hover:bg-bg-hover disabled:opacity-50 disabled:cursor-default text-sm";function T(){let K=getComputedStyle(document.documentElement),q=(J,$)=>K.getPropertyValue(`--${J}`).trim()||$;return{floor:q("bg-input","#f4f4f5"),wall:q("text-dim","#888"),goal:q("success","#22c55e"),agent:q("info","#3b82f6"),grid:q("border-subtle","#333"),trail:q("info","#3b82f6"),fog:q("bg-card","#141414")}}function n(K){K.innerHTML=`
    <div class="p-6 text-sm text-text">
      <h2 class="text-lg font-semibold text-text mb-1">Robot</h2>
      <p class="text-text-muted mb-4">Agent navigation eval sandbox. Pick a scenario, start an episode, watch the agent's tool calls land in the feed.</p>

      <div class="flex items-center gap-2 mb-4 flex-wrap">
        <span class="text-xs uppercase tracking-wider text-text-dim">Scenario</span>
        <select data-role="scenario-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <button data-role="start" class="bg-accent text-bg hover:bg-accent-hover rounded px-3 py-1 text-sm font-medium cursor-pointer disabled:opacity-50 disabled:cursor-default">Start episode</button>
        <span class="text-xs uppercase tracking-wider text-text-dim ml-2">Episode</span>
        <select data-role="episode-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <span data-role="status" class="${u} ${g.idle}">idle</span>
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
              <button data-dir="N" class="${_}" style="grid-column: 2; grid-row: 1;" title="Move north (↑)">↑</button>
              <button data-dir="W" class="${_}" style="grid-column: 1; grid-row: 2;" title="Move west (←)">←</button>
              <button data-dir="E" class="${_}" style="grid-column: 3; grid-row: 2;" title="Move east (→)">→</button>
              <button data-dir="S" class="${_}" style="grid-column: 2; grid-row: 3;" title="Move south (↓)">↓</button>
            </div>
            <div class="flex flex-col gap-1 mt-[22px]">
              <button data-role="pick" class="${w}" title="Pick up an item (inert in v0.1)">pick</button>
              <button data-role="drop" class="${w}" title="Drop the held item (inert in v0.1)">drop</button>
              <button data-role="observe" class="${w}" title="Show what observe() returns to the agent (no step cost)">observe</button>
              <button data-role="fog" class="${w}" title="Hide cells outside the agent's view radius">fog: off</button>
            </div>
            <span class="text-xs text-text-dim self-center">arrow keys also move</span>
          </div>
        </div>

        <div class="flex-1 min-w-0">
          <div class="text-xs uppercase tracking-wider text-text-dim mb-1">Activity</div>
          <div data-role="feed" class="max-h-[480px] overflow-auto bg-bg-card border border-border rounded p-2"></div>
        </div>
      </div>

      <div data-role="observe-modal" class="fixed inset-0 bg-bg-overlay items-center justify-center z-50" style="display: none;">
        <div class="bg-bg-card border border-border rounded p-4 max-w-2xl w-[90vw] max-h-[80vh] overflow-auto m-4 flex flex-col">
          <div class="flex items-center justify-between mb-2">
            <h3 class="font-semibold text-text">observe()</h3>
            <button data-role="observe-close" class="text-text-muted hover:text-text text-lg leading-none px-1">×</button>
          </div>
          <pre data-role="observe-output" class="font-mono text-xs text-text whitespace-pre overflow-auto bg-bg-input border border-border rounded p-2"></pre>
        </div>
      </div>
    </div>
  `;let q={scenPicker:K.querySelector('[data-role="scenario-picker"]'),epPicker:K.querySelector('[data-role="episode-picker"]'),startBtn:K.querySelector('[data-role="start"]'),statusPill:K.querySelector('[data-role="status"]'),canvas:K.querySelector('[data-role="grid"]'),feed:K.querySelector('[data-role="feed"]'),mSteps:K.querySelector('[data-role="m-steps"]'),mOptimal:K.querySelector('[data-role="m-optimal"]'),mRatio:K.querySelector('[data-role="m-ratio"]'),dpadBtns:Array.from(K.querySelectorAll("[data-dir]")),pickBtn:K.querySelector('[data-role="pick"]'),dropBtn:K.querySelector('[data-role="drop"]'),observeBtn:K.querySelector('[data-role="observe"]'),fogBtn:K.querySelector('[data-role="fog"]'),obsModal:K.querySelector('[data-role="observe-modal"]'),obsOutput:K.querySelector('[data-role="observe-output"]'),obsClose:K.querySelector('[data-role="observe-close"]')},J={scenarios:[],episodes:[],activeEpisode:null,activeScenario:null,pollHandle:null,fog:!1};q.scenPicker.addEventListener("change",()=>{J.activeScenario=J.scenarios.find((z)=>z.id===q.scenPicker.value),B(q.canvas,J.activeScenario,null,[],J.fog)}),q.epPicker.addEventListener("change",()=>N(q.epPicker.value)),q.startBtn.addEventListener("click",k);for(let z of q.dpadBtns)z.addEventListener("click",()=>b(z.dataset.dir));q.pickBtn.addEventListener("click",()=>h("pick")),q.dropBtn.addEventListener("click",()=>h("drop")),q.observeBtn.addEventListener("click",A),q.fogBtn.addEventListener("click",P),q.obsClose.addEventListener("click",()=>D(q.obsModal)),q.obsModal.addEventListener("click",(z)=>{if(z.target===q.obsModal)D(q.obsModal)});let $=(z)=>{let Q=z.target;if(Q&&/^(INPUT|SELECT|TEXTAREA)$/.test(Q.tagName))return;let Y={ArrowUp:"N",ArrowDown:"S",ArrowLeft:"W",ArrowRight:"E"}[z.key];if(!Y)return;if(!X())return;z.preventDefault(),b(Y)};document.addEventListener("keydown",$),O();let j=window.setInterval(O,5000);return function(){window.clearInterval(j),document.removeEventListener("keydown",$),C()};async function O(){await Promise.all([H(),U()])}async function H(){let z=await fetch(`${R}/scenarios`);if(!z.ok)return;let Q=await z.json();J.scenarios=Q.scenarios||[];let Z=q.scenPicker.value;if(q.scenPicker.innerHTML=J.scenarios.map((Y)=>`<option value="${Y.id}">${M(Y.name)}</option>`).join(""),Z)q.scenPicker.value=Z;if(J.activeScenario=J.scenarios.find((Y)=>Y.id===q.scenPicker.value)||J.scenarios[0],!J.activeEpisode&&J.activeScenario)B(q.canvas,J.activeScenario,null,[],J.fog)}async function U(){let z=await fetch(`${R}/episodes?limit=20`);if(!z.ok)return;let Q=await z.json();J.episodes=Q.episodes||[];let Z=q.epPicker.value;if(q.epPicker.innerHTML='<option value="">— recent episodes —</option>'+J.episodes.map((Y)=>{let S=Y.terminal_reason?` (${Y.terminal_reason})`:" (active)";return`<option value="${Y.episode_id}">${i(Y.episode_id)} · ${M(Y.scenario_id)}${S}</option>`}).join(""),Z)q.epPicker.value=Z;if(!J.activeEpisode&&J.episodes.length>0){let Y=J.episodes[0];q.epPicker.value=Y.episode_id,N(Y.episode_id)}}async function k(){let z=q.scenPicker.value;if(!z)return;q.startBtn.disabled=!0;try{let Q=await fetch(`${R}/episodes`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({scenario_id:z,model:"manual"})});if(!Q.ok){alert(`Start failed: ${await Q.text()}`);return}let Z=await Q.json();await U(),q.epPicker.value=Z.episode_id,N(Z.episode_id)}finally{q.startBtn.disabled=!1}}async function N(z){if(!z){C(),J.activeEpisode=null,B(q.canvas,J.activeScenario,null,[],J.fog),q.feed.innerHTML="",m(q.statusPill,"idle","idle"),q.mSteps.textContent="—",q.mOptimal.textContent="—",q.mRatio.textContent="—",G();return}await F(z),W(z)}function W(z){C(),J.pollHandle=window.setInterval(()=>F(z),1500)}function C(){if(J.pollHandle)window.clearInterval(J.pollHandle);J.pollHandle=null}async function F(z){let Q=await fetch(`${R}/episodes/${z}`);if(!Q.ok)return;let Z=await Q.json(),Y=Z.episode,S=Z.steps||[];J.activeEpisode=Y;let f=J.scenarios.find((I)=>I.id===Y.scenario_id);if(f)B(q.canvas,f,Y.position,S,J.fog);if(d(q.feed,S),L(q,Y),v(q.statusPill,Y),G(),Y.terminal_reason)C()}function X(){let z=J.activeEpisode;return z&&!z.terminal_reason}function G(){let z=X();for(let Q of q.dpadBtns)Q.disabled=!z;q.pickBtn.disabled=!z,q.dropBtn.disabled=!z,q.observeBtn.disabled=!J.activeEpisode}function y(z){z.style.display="flex"}function D(z){z.style.display="none"}async function b(z){let Q=J.activeEpisode;if(!Q||Q.terminal_reason)return;try{await fetch(`${R}/episodes/${Q.episode_id}/move`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({direction:z})})}catch(Z){}F(Q.episode_id)}async function h(z){let Q=J.activeEpisode;if(!Q||Q.terminal_reason)return;try{await fetch(`${R}/episodes/${Q.episode_id}/${z}`,{method:"POST"})}catch(Z){}F(Q.episode_id)}async function A(){let z=J.activeEpisode;if(!z)return;try{let Q=await fetch(`${R}/episodes/${z.episode_id}/observe`,{method:"POST"});if(!Q.ok)q.obsOutput.textContent=`error: ${Q.status} ${await Q.text()}`;else{let Z=await Q.json();q.obsOutput.textContent=JSON.stringify(Z.view??Z,null,2)}}catch(Q){q.obsOutput.textContent=`error: ${Q.message}`}y(q.obsModal)}function P(){J.fog=!J.fog,q.fogBtn.textContent=J.fog?"fog: on":"fog: off";let z=J.activeEpisode,Q=z?J.scenarios.find((Z)=>Z.id===z.scenario_id):J.activeScenario;if(B(q.canvas,Q,z?z.position:null,[],J.fog),z)F(z.episode_id)}function L(z,Q){z.mSteps.textContent=Q.steps,z.mOptimal.textContent=Q.optimal_steps||"—",z.mRatio.textContent=Q.optimality_ratio?Q.optimality_ratio.toFixed(2):"—"}function v(z,Q){if(Q.terminal_reason==="success")m(z,"success","success");else if(Q.terminal_reason==="timeout")m(z,"timeout","timeout");else m(z,"active","active")}}function m(K,q,J){K.className=`${u} ${g[q]}`,K.textContent=J}function B(K,q,J,$,j=!1){if(!K||!q)return;let O=q.grid.width,H=q.grid.height;K.width=O*V+1,K.height=H*V+1;let U=K.getContext("2d"),k=T();U.fillStyle=k.floor,U.fillRect(0,0,K.width,K.height);let N=q.walls||[];U.fillStyle=k.wall;for(let[W,C]of N)U.fillRect(W*V,C*V,V,V);if(q.goal){let[W,C]=q.goal;U.fillStyle=k.goal,U.fillRect(W*V+4,C*V+4,V-8,V-8)}if(j&&J&&(q.observability?.kind??"partial")==="partial"){let W=q.observability?.radius??2,[C,F]=J;U.fillStyle=k.fog;for(let X=0;X<H;X++)for(let G=0;G<O;G++)if(Math.abs(G-C)>W||Math.abs(X-F)>W)U.fillRect(G*V,X*V,V,V)}if($&&$.length>0){U.strokeStyle=k.trail,U.globalAlpha=0.35,U.lineWidth=2,U.beginPath();let W=!1;for(let C of $){let F=C.pos_x*V+V/2,X=C.pos_y*V+V/2;if(!W)U.moveTo(F,X),W=!0;else U.lineTo(F,X)}U.stroke(),U.globalAlpha=1}if(J){let[W,C]=J;U.fillStyle=k.agent,U.beginPath(),U.arc(W*V+V/2,C*V+V/2,V/3,0,Math.PI*2),U.fill()}U.strokeStyle=k.grid,U.lineWidth=0.5,U.beginPath();for(let W=0;W<=O;W++)U.moveTo(W*V+0.5,0),U.lineTo(W*V+0.5,H*V);for(let W=0;W<=H;W++)U.moveTo(0,W*V+0.5),U.lineTo(O*V,W*V+0.5);U.stroke()}function d(K,q){if(!q||q.length===0){K.innerHTML='<div class="text-text-dim text-xs p-2">no steps yet</div>';return}let J=[...q].sort(($,j)=>j.step-$.step);K.innerHTML=J.map(($)=>{let j="";try{let O=JSON.parse($.result);if($.tool==="move")j=O.moved?"→ ok":`→ ${O.reason}`;else j=O.reason||JSON.stringify(O).slice(0,60)}catch{j=($.result?.slice?$.result.slice(0,60):"")||""}return`<div class="grid gap-2 px-1 py-0.5 border-b border-border-subtle last:border-b-0 font-mono text-xs" style="grid-template-columns: 36px 90px 1fr;">
        <span class="text-text-dim text-right">${$.step}</span>
        <span class="text-info">${M($.tool)}</span>
        <span class="text-text whitespace-nowrap overflow-hidden text-ellipsis" title="${M(j)}">${M(j)}</span>
      </div>`}).join("")}function i(K){return K?K.slice(-6):""}function M(K){return String(K??"").replace(/[&<>"']/g,(q)=>{return{"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[q]})}function p(){let K=E(null);return x(()=>{if(!K.current)return;return n(K.current)},[]),c("div",{ref:K,className:"h-full overflow-auto"})}export{p as default};

//# debugId=26071FCED1724AE664756E2164756E21
