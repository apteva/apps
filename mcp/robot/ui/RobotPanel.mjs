import{useEffect as f,useRef as u}from"react";import{jsx as v}from"react/jsx-runtime";var F="/api/apps/robot",Q=24,j={floor:"#f4f4f5",wall:"#1f2937",goal:"#22c55e",agent:"#3b82f6",fog:"#27272a",oob:"#09090b",item:"#f59e0b",hazard:"#ef4444",grid:"#d4d4d8"};function P(K){K.innerHTML=`
    <style>
      .robot-panel { font: 13px/1.4 system-ui, sans-serif; color: #18181b; padding: 16px; }
      .robot-panel.dark { color: #e4e4e7; }
      .robot-row { display: flex; gap: 16px; align-items: flex-start; }
      .robot-col { flex: 1; min-width: 0; }
      .robot-h { font-size: 11px; text-transform: uppercase; letter-spacing: .04em; color: #71717a; margin-bottom: 4px; }
      .robot-title { font-size: 18px; font-weight: 600; margin: 0 0 4px 0; }
      .robot-desc { color: #52525b; margin: 0 0 12px 0; }
      .robot-canvas-wrap { background: #fafafa; border: 1px solid #e4e4e7; border-radius: 6px; padding: 12px; display: inline-block; }
      .robot-feed { max-height: 480px; overflow: auto; border: 1px solid #e4e4e7; border-radius: 6px; padding: 8px; background: #fafafa; }
      .robot-feed-row { display: grid; grid-template-columns: 36px 90px 1fr; gap: 8px; padding: 3px 4px; border-bottom: 1px dashed #e4e4e7; font-family: ui-monospace, monospace; font-size: 12px; }
      .robot-feed-row:last-child { border-bottom: none; }
      .robot-step { color: #71717a; text-align: right; }
      .robot-tool { color: #2563eb; }
      .robot-result { color: #18181b; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
      .robot-metrics { display: flex; gap: 16px; font-size: 13px; margin-top: 8px; }
      .robot-metric b { color: #18181b; }
      .robot-controls { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; }
      .robot-controls select, .robot-controls button { font: inherit; padding: 4px 8px; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; }
      .robot-controls button { cursor: pointer; background: #2563eb; color: #fff; border-color: #2563eb; }
      .robot-controls button:hover { background: #1d4ed8; }
      .robot-controls button:disabled { background: #a1a1aa; border-color: #a1a1aa; cursor: default; }
      .robot-status-pill { font-size: 11px; padding: 2px 8px; border-radius: 999px; background: #e4e4e7; color: #18181b; }
      .robot-status-pill.success { background: #dcfce7; color: #166534; }
      .robot-status-pill.timeout { background: #fef3c7; color: #92400e; }
      .robot-status-pill.active  { background: #dbeafe; color: #1e40af; }
      .robot-dpad { display: grid; grid-template-columns: repeat(3, 36px); grid-template-rows: repeat(3, 36px); gap: 4px; margin-top: 12px; }
      .robot-dpad button { font: inherit; font-weight: 600; cursor: pointer; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; color: #18181b; }
      .robot-dpad button:hover:not(:disabled) { background: #f4f4f5; }
      .robot-dpad button:active:not(:disabled) { background: #e4e4e7; }
      .robot-dpad button:disabled { color: #a1a1aa; cursor: default; }
      .robot-dpad-N { grid-column: 2; grid-row: 1; }
      .robot-dpad-W { grid-column: 1; grid-row: 2; }
      .robot-dpad-E { grid-column: 3; grid-row: 2; }
      .robot-dpad-S { grid-column: 2; grid-row: 3; }
      .robot-drive { display: flex; gap: 16px; align-items: flex-start; margin-top: 12px; }
      .robot-drive-extras { display: flex; flex-direction: column; gap: 4px; margin-top: 22px; }
      .robot-drive-extras button { font: inherit; padding: 4px 10px; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; color: #18181b; cursor: pointer; }
      .robot-drive-extras button:disabled { color: #a1a1aa; cursor: default; }
      .robot-drive-hint { font-size: 11px; color: #71717a; align-self: center; }
    </style>
    <div class="robot-panel">
      <h2 class="robot-title">Robot</h2>
      <p class="robot-desc">Agent navigation eval sandbox. Pick a scenario, start an episode, watch the agent's tool calls land in the feed.</p>

      <div class="robot-controls">
        <span class="robot-h">Scenario</span>
        <select data-role="scenario-picker"></select>
        <button data-role="start">Start episode</button>
        <span class="robot-h">Episode</span>
        <select data-role="episode-picker"></select>
        <span class="robot-status-pill" data-role="status">idle</span>
      </div>

      <div class="robot-row">
        <div class="robot-col" style="flex: 0 0 auto;">
          <div class="robot-h">World</div>
          <div class="robot-canvas-wrap"><canvas data-role="grid"></canvas></div>
          <div class="robot-metrics">
            <span class="robot-metric">steps <b data-role="m-steps">—</b></span>
            <span class="robot-metric">optimal <b data-role="m-optimal">—</b></span>
            <span class="robot-metric">ratio <b data-role="m-ratio">—</b></span>
          </div>
          <div class="robot-drive">
            <div class="robot-dpad">
              <button class="robot-dpad-N" data-dir="N" title="Move north (↑)">↑</button>
              <button class="robot-dpad-W" data-dir="W" title="Move west (←)">←</button>
              <button class="robot-dpad-E" data-dir="E" title="Move east (→)">→</button>
              <button class="robot-dpad-S" data-dir="S" title="Move south (↓)">↓</button>
            </div>
            <div class="robot-drive-extras">
              <button data-role="pick" title="Pick up an item (inert in v0.1)">pick</button>
              <button data-role="drop" title="Drop the held item (inert in v0.1)">drop</button>
            </div>
            <span class="robot-drive-hint">arrow keys also move</span>
          </div>
        </div>
        <div class="robot-col">
          <div class="robot-h">Activity</div>
          <div class="robot-feed" data-role="feed"></div>
        </div>
      </div>
    </div>
  `;let q={scenPicker:K.querySelector('[data-role="scenario-picker"]'),epPicker:K.querySelector('[data-role="episode-picker"]'),startBtn:K.querySelector('[data-role="start"]'),statusPill:K.querySelector('[data-role="status"]'),canvas:K.querySelector('[data-role="grid"]'),feed:K.querySelector('[data-role="feed"]'),mSteps:K.querySelector('[data-role="m-steps"]'),mOptimal:K.querySelector('[data-role="m-optimal"]'),mRatio:K.querySelector('[data-role="m-ratio"]'),dpadBtns:Array.from(K.querySelectorAll(".robot-dpad button")),pickBtn:K.querySelector('[data-role="pick"]'),dropBtn:K.querySelector('[data-role="drop"]')},B={scenarios:[],episodes:[],activeEpisode:null,activeScenario:null,pollHandle:null};q.scenPicker.addEventListener("change",()=>{B.activeScenario=B.scenarios.find((z)=>z.id===q.scenPicker.value),D(q.canvas,B.activeScenario,null,[])}),q.epPicker.addEventListener("change",()=>Z(q.epPicker.value)),q.startBtn.addEventListener("click",V);for(let z of q.dpadBtns)z.addEventListener("click",()=>b(z.dataset.dir));q.pickBtn.addEventListener("click",()=>m("pick")),q.dropBtn.addEventListener("click",()=>m("drop"));let W=(z)=>{let J=z.target;if(J&&/^(INPUT|SELECT|TEXTAREA)$/.test(J.tagName))return;let U={ArrowUp:"N",ArrowDown:"S",ArrowLeft:"W",ArrowRight:"E"}[z.key];if(!U)return;if(!M())return;z.preventDefault(),b(U)};document.addEventListener("keydown",W),_();let Y=window.setInterval(_,5000);return function(){window.clearInterval(Y),document.removeEventListener("keydown",W),$()};async function _(){await Promise.all([N(),k()])}async function N(){let z=await fetch(`${F}/scenarios`);if(!z.ok)return;let J=await z.json();B.scenarios=J.scenarios||[];let X=q.scenPicker.value;if(q.scenPicker.innerHTML=B.scenarios.map((U)=>`<option value="${U.id}">${H(U.name)}</option>`).join(""),X)q.scenPicker.value=X;if(B.activeScenario=B.scenarios.find((U)=>U.id===q.scenPicker.value)||B.scenarios[0],!B.activeEpisode&&B.activeScenario)D(q.canvas,B.activeScenario,null,[])}async function k(){let z=await fetch(`${F}/episodes?limit=20`);if(!z.ok)return;let J=await z.json();B.episodes=J.episodes||[];let X=q.epPicker.value;if(q.epPicker.innerHTML='<option value="">— recent episodes —</option>'+B.episodes.map((U)=>{let R=U.terminal_reason?` (${U.terminal_reason})`:" (active)";return`<option value="${U.episode_id}">${y(U.episode_id)} · ${H(U.scenario_id)}${R}</option>`}).join(""),X)q.epPicker.value=X;if(!B.activeEpisode&&B.episodes.length>0){let U=B.episodes[0];q.epPicker.value=U.episode_id,Z(U.episode_id)}}async function V(){let z=q.scenPicker.value;if(!z)return;q.startBtn.disabled=!0;try{let J=await fetch(`${F}/episodes`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({scenario_id:z,model:"manual"})});if(!J.ok){alert(`Start failed: ${await J.text()}`);return}let X=await J.json();await k(),q.epPicker.value=X.episode_id,Z(X.episode_id)}finally{q.startBtn.disabled=!1}}async function Z(z){if(!z){$(),B.activeEpisode=null,D(q.canvas,B.activeScenario,null,[]),q.feed.innerHTML="",q.statusPill.textContent="idle",q.statusPill.className="robot-status-pill",q.mSteps.textContent=q.mOptimal.textContent=q.mRatio.textContent="—",S();return}await C(z),G(z)}function G(z){$(),B.pollHandle=window.setInterval(()=>C(z),1500)}function $(){if(B.pollHandle)window.clearInterval(B.pollHandle);B.pollHandle=null}async function C(z){let J=await fetch(`${F}/episodes/${z}`);if(!J.ok)return;let X=await J.json(),U=X.episode,R=X.steps||[];B.activeEpisode=U;let w=B.scenarios.find((A)=>A.id===U.scenario_id);if(w)D(q.canvas,w,U.position,R);if(g(q.feed,R),O(q,U),h(q.statusPill,U),S(),U.terminal_reason)$()}function M(){let z=B.activeEpisode;return z&&!z.terminal_reason}function S(){let z=M();for(let J of q.dpadBtns)J.disabled=!z;q.pickBtn.disabled=!z,q.dropBtn.disabled=!z}async function b(z){let J=B.activeEpisode;if(!J||J.terminal_reason)return;try{await fetch(`${F}/episodes/${J.episode_id}/move`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({direction:z})})}catch(X){}C(J.episode_id)}async function m(z){let J=B.activeEpisode;if(!J||J.terminal_reason)return;try{await fetch(`${F}/episodes/${J.episode_id}/${z}`,{method:"POST"})}catch(X){}C(J.episode_id)}function O(z,J){z.mSteps.textContent=J.steps,z.mOptimal.textContent=J.optimal_steps||"—",z.mRatio.textContent=J.optimality_ratio?J.optimality_ratio.toFixed(2):"—"}function h(z,J){if(z.classList.remove("success","timeout","active"),J.terminal_reason==="success")z.textContent="success",z.classList.add("success");else if(J.terminal_reason==="timeout")z.textContent="timeout",z.classList.add("timeout");else z.textContent="active",z.classList.add("active")}}function D(K,q,B,W){if(!K||!q)return;let Y=q.grid.width,_=q.grid.height;K.width=Y*Q+1,K.height=_*Q+1;let N=K.getContext("2d");N.fillStyle=j.floor,N.fillRect(0,0,K.width,K.height);let k=q.walls||[];N.fillStyle=j.wall;for(let[V,Z]of k)N.fillRect(V*Q,Z*Q,Q,Q);if(q.goal){let[V,Z]=q.goal;N.fillStyle=j.goal,N.fillRect(V*Q+4,Z*Q+4,Q-8,Q-8)}if(W&&W.length>0){N.strokeStyle="rgba(59,130,246,0.35)",N.lineWidth=2,N.beginPath();let V=!1;for(let Z of W){let G=Z.pos_x*Q+Q/2,$=Z.pos_y*Q+Q/2;if(!V)N.moveTo(G,$),V=!0;else N.lineTo(G,$)}N.stroke()}if(B){let[V,Z]=B;N.fillStyle=j.agent,N.beginPath(),N.arc(V*Q+Q/2,Z*Q+Q/2,Q/3,0,Math.PI*2),N.fill()}N.strokeStyle=j.grid,N.lineWidth=0.5,N.beginPath();for(let V=0;V<=Y;V++)N.moveTo(V*Q+0.5,0),N.lineTo(V*Q+0.5,_*Q);for(let V=0;V<=_;V++)N.moveTo(0,V*Q+0.5),N.lineTo(Y*Q,V*Q+0.5);N.stroke()}function g(K,q){if(!q||q.length===0){K.innerHTML='<div style="color:#71717a; font-size:12px; padding:8px;">no steps yet</div>';return}let B=[...q].sort((W,Y)=>Y.step-W.step);K.innerHTML=B.map((W)=>{let Y="";try{let _=JSON.parse(W.result);if(W.tool==="move")Y=_.moved?"→ ok":`→ ${_.reason}`;else Y=_.reason||JSON.stringify(_).slice(0,60)}catch{Y=(W.result?.slice?W.result.slice(0,60):"")||""}return`<div class="robot-feed-row">
      <span class="robot-step">${W.step}</span>
      <span class="robot-tool">${H(W.tool)}</span>
      <span class="robot-result">${H(Y)}</span>
    </div>`}).join("")}function y(K){return K?K.slice(-6):""}function H(K){return String(K??"").replace(/[&<>"']/g,(q)=>{return{"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[q]})}function T(){let K=u(null);return f(()=>{if(!K.current)return;return P(K.current)},[]),v("div",{ref:K,style:{height:"100%",overflow:"auto"}})}export{T as default};

//# debugId=31586355B783904764756E2164756E21
