import{useCallback as Y,useEffect as M,useMemo as K,useState as g}from"react";var S=`<article class="document">
  <section class="page cover" data-pdf-page data-allow-horizontal-overflow>
    <div class="cover-grid"></div>
    <header class="brand-row">
      <div class="brand-mark">M</div>
      <div>
        <strong>{{.brand_name}}</strong>
        <span>FIELD GUIDE · {{.edition}}</span>
      </div>
    </header>
    <div class="cover-copy">
      <p class="eyebrow">BUILD IT THIS WEEKEND</p>
      <h1>{{.title}}</h1>
      <p class="promise">{{.promise}}</p>
      <div class="meta-row">
        <span>Beginner friendly</span><span>{{.duration}}</span><span>No cloud required</span>
      </div>
    </div>
    <div class="device-visual" aria-label="Stylized ESP32 dashboard illustration">
      <div class="signal signal-one"></div><div class="signal signal-two"></div>
      <div class="board"><span>ESP32</span><i></i><i></i><i></i><i></i></div>
      <div class="dashboard-card"><small>LIVE SENSOR</small><strong>684</strong><span>Updated now</span></div>
    </div>
    <aside class="outcome"><span>YOUR OUTCOME</span><strong>{{.outcome}}</strong></aside>
    <footer class="page-footer"><span>{{.brand_name}}</span><span>{{.website}}</span></footer>
  </section>

  <section class="page" data-pdf-page>
    <header class="section-head"><span>01</span><div><p>ORIENT</p><h2>Know what you are building</h2></div></header>
    <p class="lede">This project turns a blank ESP32 into a tiny connected product: it reads a sensor, joins your WiFi network, and serves a clean dashboard directly to your browser.</p>
    <div class="three-up">
      <article class="feature"><b>1</b><h3>Sense</h3><p>Read a changing analog value and smooth the result.</p></article>
      <article class="feature"><b>2</b><h3>Connect</h3><p>Join a 2.4 GHz WiFi network and expose a local address.</p></article>
      <article class="feature"><b>3</b><h3>Serve</h3><p>Return a responsive status page from the board itself.</p></article>
    </div>
    <h3 class="rule-title">Parts and tools</h3>
    <table>
      <thead><tr><th>Item</th><th>Why you need it</th><th>Qty</th></tr></thead>
      <tbody>{{range .parts}}<tr><td><strong>{{.item}}</strong></td><td>{{.purpose}}</td><td>{{.qty}}</td></tr>{{end}}</tbody>
    </table>
    <div class="callout check"><span>QUICK CHECK</span><p>Use a data-capable USB cable. A charge-only cable powers the board but prevents uploads.</p></div>
    <div class="two-up compact">
      <div><h3>Before you begin</h3><ul><li>Install Arduino IDE 2.x</li><li>Add the ESP32 board package</li><li>Select the correct board and port</li></ul></div>
      <div><h3>Definition of done</h3><ul><li>Sensor value changes in Serial Monitor</li><li>Board prints a local IP address</li><li>Dashboard opens from your phone</li></ul></div>
    </div>
    <footer class="page-footer"><span>{{.title}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>

  <section class="page" data-pdf-page>
    <header class="section-head"><span>02</span><div><p>BUILD</p><h2>Read the sensor reliably</h2></div></header>
    <div class="step"><b>1</b><div><h3>Wire the input</h3><p>Connect the sensor output to GPIO 34, power to 3.3V, and ground to GND. GPIO 34 is input-only, which makes it a safe choice for this exercise.</p></div></div>
    <div class="wiring">
      <div class="pin source">SENSOR</div><span>OUT</span><i></i><span>GPIO 34</span><div class="pin target">ESP32</div>
    </div>
    <div class="step"><b>2</b><div><h3>Upload a minimal reading sketch</h3><p>Prove the hardware path before adding WiFi or a web server.</p></div></div>
    <pre><code>const int sensorPin = 34;

void setup() {
  Serial.begin(115200);
}

void loop() {
  int raw = analogRead(sensorPin);
  Serial.println(raw);
  delay(250);
}</code></pre>
    <div class="callout warning"><span>COMMON MISTAKE</span><p>Do not feed a 5V sensor output directly into an ESP32 input. Keep the signal within the board's 3.3V range.</p></div>
    <h3 class="rule-title">Test before moving on</h3>
    <ol class="numbered"><li>Open Serial Monitor at 115200 baud.</li><li>Change the sensor input.</li><li>Confirm the value moves consistently instead of remaining fixed.</li></ol>
    <footer class="page-footer"><span>{{.title}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>

  <section class="page" data-pdf-page>
    <header class="section-head"><span>03</span><div><p>CONNECT</p><h2>Put the dashboard on your network</h2></div></header>
    <p class="lede">Add WiFi only after the sensor works. This keeps failures easy to locate and gives every stage one clear success signal.</p>
    <div class="flow">
      <div><strong>Sensor</strong><span>analog value</span></div><i>→</i>
      <div><strong>ESP32</strong><span>read + format</span></div><i>→</i>
      <div><strong>Browser</strong><span>local dashboard</span></div>
    </div>
    <pre><code>#include &lt;WiFi.h&gt;
#include &lt;WebServer.h&gt;

WebServer server(80);

void handleStatus() {
  int value = analogRead(34);
  String json = "{\\"value\\":" + String(value) + "}";
  server.send(200, "application/json", json);
}</code></pre>
    <div class="two-up">
      <div class="panel"><span>ON STARTUP</span><h3>Print the address</h3><p>After connecting, output <code>WiFi.localIP()</code> so you always know which URL to open.</p></div>
      <div class="panel"><span>IN THE LOOP</span><h3>Handle requests</h3><p>Call <code>server.handleClient()</code> continuously. Long delays make the page feel unresponsive.</p></div>
    </div>
    <div class="callout next"><span>NEXT STEP</span><p>Open the printed IP address on a phone connected to the same WiFi network. If the status endpoint returns a number, the hard part is done.</p></div>
    <footer class="page-footer"><span>{{.title}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>

  <section class="page final-page" data-pdf-page>
    <header class="section-head"><span>04</span><div><p>SHIP</p><h2>Launch with confidence</h2></div></header>
    <h3 class="rule-title">Final checklist</h3>
    <div class="checklist">{{range .checklist}}<div><span>□</span><p>{{.}}</p></div>{{end}}</div>
    <h3 class="rule-title">Fast troubleshooting</h3>
    <table>
      <thead><tr><th>Symptom</th><th>Likely cause</th><th>Try this</th></tr></thead>
      <tbody>
        <tr><td>Port is missing</td><td>Charge-only cable or driver</td><td>Change cable, reconnect, reopen IDE</td></tr>
        <tr><td>Upload times out</td><td>Board not in boot mode</td><td>Hold BOOT during the connection step</td></tr>
        <tr><td>No IP address</td><td>Wrong credentials or 5 GHz WiFi</td><td>Verify SSID/password and use 2.4 GHz</td></tr>
        <tr><td>Page will not open</td><td>Devices on different networks</td><td>Put phone and board on the same LAN</td></tr>
      </tbody>
    </table>
    <aside class="cta">
      <div><p>KEEP BUILDING</p><h2>{{.cta_title}}</h2><span>{{.cta_body}}</span></div>
      <a href="{{.website}}">Explore the next project →</a>
    </aside>
    <div class="commitment"><span>MY NEXT BUILD</span><div></div><small>Target date</small><div class="short"></div></div>
    <footer class="page-footer"><span>{{.brand_name}} · {{.website}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>
</article>`,V=`
@page { margin: 0; }
:root { --ink:#172033; --muted:#657086; --line:#dbe3ee; --paper:#fff; --soft:#f3f6fb; --brand:#5b4ce6; --brand2:#17a8c7; --accent:#ff8a3d; }
* { box-sizing:border-box; }
body { font-family:Arial,Helvetica,sans-serif; background:#fff; color:var(--ink); }
.document { margin:0; }
.page { position:relative; width:var(--document-page-width); height:var(--document-page-height); overflow:hidden; padding:17mm 18mm 15mm; background:var(--paper); break-after:page; }
.page:last-child { break-after:auto; }
.cover { padding:16mm 18mm; color:#fff; background:linear-gradient(145deg,#121936 0%,#282264 54%,#3e2c8f 100%); }
.cover-grid { position:absolute; inset:0; opacity:.17; background-image:linear-gradient(rgba(255,255,255,.18) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.18) 1px,transparent 1px); background-size:14mm 14mm; mask-image:linear-gradient(to bottom,#000,transparent 72%); }
.cover:after { content:""; position:absolute; width:140mm; height:140mm; border-radius:50%; right:-52mm; top:-38mm; background:linear-gradient(135deg,rgba(23,168,199,.7),rgba(91,76,230,.1)); }
.brand-row { position:relative; z-index:2; display:flex; gap:4mm; align-items:center; }
.brand-mark { display:grid; place-items:center; width:11mm; height:11mm; border-radius:3mm; font-weight:800; background:linear-gradient(135deg,var(--brand2),#73d7e8); color:#10203e; }
.brand-row strong,.brand-row span { display:block; }.brand-row strong { font-size:11pt; }.brand-row span { margin-top:1mm; font-size:7pt; letter-spacing:.14em; opacity:.68; }
.cover-copy { position:relative; z-index:2; width:145mm; margin-top:28mm; }
.eyebrow { margin:0 0 5mm; color:#87e8f4; font-size:8pt; font-weight:800; letter-spacing:.2em; }
.cover h1 { margin:0; font-size:36pt; line-height:1.02; letter-spacing:-.035em; }
.promise { margin:7mm 0 0; width:130mm; color:#dce2ff; font-size:15pt; line-height:1.38; }
.meta-row { display:flex; gap:3mm; margin-top:8mm; }.meta-row span { padding:2.2mm 3.5mm; border:1px solid rgba(255,255,255,.28); border-radius:99px; font-size:7.5pt; color:#eef1ff; }
.device-visual { position:relative; z-index:2; height:70mm; margin-top:12mm; border:1px solid rgba(255,255,255,.18); border-radius:8mm; background:linear-gradient(135deg,rgba(255,255,255,.07),rgba(255,255,255,.02)); overflow:hidden; }
.board { position:absolute; left:20mm; top:17mm; width:54mm; height:38mm; border:2mm solid #1fa9c2; border-radius:5mm; background:#12233e; box-shadow:0 8mm 20mm rgba(0,0,0,.25); }.board span { position:absolute; inset:0; display:grid; place-items:center; color:#90f4ff; font-size:14pt; font-weight:800; letter-spacing:.1em; }.board i { position:relative; display:block; width:7mm; height:1.2mm; margin:5mm 0 0 -5mm; background:#f8bd65; }
.dashboard-card { position:absolute; right:18mm; top:12mm; width:61mm; padding:8mm; border-radius:5mm; background:#fff; color:var(--ink); box-shadow:0 8mm 18mm rgba(3,8,30,.3); }.dashboard-card small,.dashboard-card strong,.dashboard-card span { display:block; }.dashboard-card small { color:var(--brand); font-size:7pt; font-weight:800; letter-spacing:.15em; }.dashboard-card strong { margin:4mm 0 2mm; font-size:31pt; }.dashboard-card span { color:var(--muted); font-size:8pt; }
.signal { position:absolute; border:1.2mm solid rgba(135,232,244,.55); border-left-color:transparent; border-bottom-color:transparent; border-radius:50%; transform:rotate(45deg); }.signal-one { left:77mm; top:24mm; width:18mm; height:18mm; }.signal-two { left:72mm; top:19mm; width:28mm; height:28mm; opacity:.5; }
.outcome { position:relative; z-index:2; display:grid; grid-template-columns:35mm 1fr; gap:5mm; align-items:center; margin-top:10mm; padding:5mm 6mm; border-left:2mm solid var(--accent); background:rgba(255,255,255,.09); }.outcome span { color:#ffbd91; font-size:7pt; font-weight:800; letter-spacing:.15em; }.outcome strong { font-size:10pt; line-height:1.4; }
.section-head { display:flex; gap:5mm; align-items:center; padding-bottom:6mm; border-bottom:1px solid var(--line); }.section-head>span { display:grid; place-items:center; flex:0 0 14mm; height:14mm; border-radius:4mm; color:#fff; background:linear-gradient(135deg,var(--brand),#7669eb); font-size:12pt; font-weight:800; }.section-head p { margin:0 0 1mm; color:var(--brand); font-size:7pt; font-weight:800; letter-spacing:.18em; }.section-head h2 { margin:0; font-size:22pt; letter-spacing:-.025em; }
.lede { margin:7mm 0; color:#3f4a60; font-size:12pt; line-height:1.55; }
.three-up,.two-up { display:grid; grid-template-columns:repeat(3,1fr); gap:4mm; }.two-up { grid-template-columns:repeat(2,1fr); }.feature,.panel { padding:5mm; border:1px solid var(--line); border-radius:4mm; background:linear-gradient(180deg,#fff,var(--soft)); }.feature b { display:grid; place-items:center; width:7mm; height:7mm; border-radius:50%; background:#e9e7ff; color:var(--brand); font-size:8pt; }.feature h3,.panel h3 { margin:3mm 0 1.5mm; font-size:12pt; }.feature p,.panel p { margin:0; color:var(--muted); font-size:8.5pt; line-height:1.45; }
.rule-title { margin:7mm 0 3mm; font-size:13pt; }
table { width:100%; border-collapse:collapse; font-size:8.2pt; } th { padding:2.5mm 3mm; text-align:left; color:#fff; background:#252052; } th:first-child { border-radius:2mm 0 0 0; } th:last-child { border-radius:0 2mm 0 0; } td { padding:2.8mm 3mm; border-bottom:1px solid var(--line); vertical-align:top; } tbody tr:nth-child(even) { background:#f8f9fc; }
.callout { display:grid; grid-template-columns:31mm 1fr; gap:4mm; align-items:center; margin:6mm 0; padding:4mm 5mm; border-left:1.8mm solid; border-radius:0 3mm 3mm 0; }.callout span { font-size:7pt; font-weight:800; letter-spacing:.12em; }.callout p { margin:0; font-size:8.7pt; line-height:1.45; }.callout.check { color:#174ea6; border-color:#3b82f6; background:#eff6ff; }.callout.warning { color:#9f2443; border-color:#e43f68; background:#fff1f3; }.callout.next { color:#17653d; border-color:#28a86b; background:#effcf5; }
.compact { margin-top:4mm; }.compact>div { padding:3mm 5mm; background:var(--soft); border-radius:3mm; }.compact h3 { margin:0 0 2mm; font-size:10pt; } ul,ol { margin:0; padding-left:5mm; } li { margin:1.2mm 0; font-size:8.5pt; line-height:1.4; }
.step { display:grid; grid-template-columns:10mm 1fr; gap:4mm; margin:7mm 0 4mm; }.step>b { display:grid; place-items:center; width:9mm; height:9mm; border-radius:3mm; color:#fff; background:var(--brand); }.step h3 { margin:0 0 1mm; font-size:13pt; }.step p { margin:0; color:var(--muted); font-size:9pt; line-height:1.45; }
.wiring { display:flex; align-items:center; justify-content:center; gap:4mm; height:34mm; margin:4mm 0 7mm; border-radius:4mm; background:#f2f5fa; }.pin { display:grid; place-items:center; width:27mm; height:18mm; border-radius:4mm; color:#fff; font-size:8pt; font-weight:800; }.pin.source { background:#17a8c7; }.pin.target { background:#252052; }.wiring span { font-size:7pt; color:var(--muted); }.wiring i { width:22mm; border-top:1.2mm solid var(--accent); }
pre { margin:4mm 0 6mm; padding:5mm; overflow:hidden; border-radius:4mm; color:#e9f1ff; background:#151b2d; font:8.2pt/1.45 "Courier New",monospace; white-space:pre-wrap; }.numbered { columns:3; column-gap:8mm; padding-left:5mm; }.numbered li { break-inside:avoid; padding-right:3mm; }
.flow { display:grid; grid-template-columns:1fr 9mm 1fr 9mm 1fr; align-items:center; gap:2mm; margin:10mm 0 7mm; }.flow div { padding:5mm 3mm; text-align:center; border:1px solid var(--line); border-radius:4mm; background:var(--soft); }.flow strong,.flow span { display:block; }.flow strong { font-size:11pt; }.flow span { margin-top:1mm; color:var(--muted); font-size:7.5pt; }.flow i { text-align:center; color:var(--brand); font-size:18pt; font-style:normal; }.panel span { color:var(--brand); font-size:7pt; font-weight:800; letter-spacing:.12em; }
.checklist { display:grid; grid-template-columns:1fr 1fr; gap:3mm; }.checklist div { display:grid; grid-template-columns:8mm 1fr; align-items:center; min-height:14mm; padding:3mm 4mm; border:1px solid var(--line); border-radius:3mm; }.checklist span { color:var(--brand); font-size:16pt; }.checklist p { margin:0; font-size:8.7pt; line-height:1.35; }
.cta { display:grid; grid-template-columns:1fr 50mm; gap:8mm; align-items:center; margin-top:9mm; padding:8mm; border-radius:6mm; color:#fff; background:linear-gradient(135deg,#252052,#5b4ce6); }.cta p { margin:0 0 2mm; color:#8ee7f3; font-size:7pt; font-weight:800; letter-spacing:.15em; }.cta h2 { margin:0 0 2mm; font-size:19pt; }.cta span { color:#dfe2ff; font-size:8.5pt; line-height:1.4; }.cta a { display:block; padding:4mm; border-radius:3mm; color:#1c2140; background:#fff; text-align:center; text-decoration:none; font-size:8pt; font-weight:800; }
.commitment { display:grid; grid-template-columns:31mm 1fr 17mm 30mm; align-items:end; gap:3mm; margin-top:10mm; color:var(--muted); font-size:7pt; letter-spacing:.1em; }.commitment div { height:5mm; border-bottom:1px solid #7f899b; }.commitment small { font-size:7pt; }
.page-footer { position:absolute; left:18mm; right:18mm; bottom:8mm; display:flex; justify-content:space-between; padding-top:2.5mm; border-top:1px solid rgba(219,227,238,.75); color:var(--muted); font-size:7pt; }.cover .page-footer { color:rgba(255,255,255,.65); border-color:rgba(255,255,255,.2); }
`,L={layout_mode:"fixed",page_size:"A4",margin_top_mm:0,margin_right_mm:0,margin_bottom_mm:0,margin_left_mm:0,print_background:!0},R={brand_name:"Makecademy",edition:"2026",title:"ESP32 Starter Guide",promise:"From a blank board to a live browser dashboard - one clear step at a time.",duration:"90-minute build",outcome:"An ESP32 that reads a sensor, joins WiFi, and serves a dashboard you can open from your phone.",website:"https://makecademy.com",parts:[{item:"ESP32 development board",purpose:"Runs the sensor and web server",qty:"1"},{item:"Analog sensor or potentiometer",purpose:"Provides a changing input",qty:"1"},{item:"Breadboard and jumper wires",purpose:"Creates a reusable prototype",qty:"1 set"},{item:"USB data cable",purpose:"Powers and programs the board",qty:"1"}],checklist:["The sensor value changes reliably.","WiFi reconnects after a restart.","The board prints its local IP address.","The dashboard opens from another device.","No credentials are shown in screenshots.","The finished wiring is photographed."],cta_title:"Turn this prototype into your next connected product.",cta_body:"Use the same staged method to add displays, alerts, storage, and automation without losing control of the build."};var D=`<article class="release">
  <section class="page" data-pdf-page>
    <header class="head">
      <div>
        <p class="eyebrow">{{.company_name}}</p>
        <h1>Model Release</h1>
        <p class="sub">Consent to photograph and use likeness</p>
      </div>
      <dl class="ref">
        <div><dt>Reference</dt><dd>{{.reference}}</dd></div>
        <div><dt>Shoot date</dt><dd>{{.shoot_date}}</dd></div>
      </dl>
    </header>

    <div class="parties">
      <div class="party">
        <span class="label">Model</span>
        <strong>{{.model_name}}</strong>
        <span>{{.model_address}}</span>
        <span>{{.model_email}}</span>
      </div>
      <div class="party">
        <span class="label">Photographer / Producer</span>
        <strong>{{.photographer_name}}</strong>
        <span>for {{.company_name}}</span>
        <span>{{.company_address}}</span>
      </div>
    </div>

    <table class="facts">
      <tr><th>Project</th><td>{{.project_description}}</td></tr>
      <tr><th>Location</th><td>{{.shoot_location}}</td></tr>
      <tr><th>Media</th><td>{{.media}}</td></tr>
      <tr><th>Territory &amp; term</th><td>{{.territory}} · {{.term}}</td></tr>
      <tr><th>Consideration</th><td>{{.compensation}}</td></tr>
    </table>

    <ol class="terms">
      <li><b>Grant.</b> For the consideration stated above, the receipt of which I acknowledge, I grant {{.company_name}}, its licensees, successors and assigns (the "Producer") the right to photograph and record me and to use, reproduce, edit, publish and distribute the resulting photographs, video and audio, together with my name and likeness (the "Images"), in the media, territory and term stated above.</li>
      <li><b>Editing.</b> The Images may be cropped, retouched, combined with other material and used with text or captions, provided they are not used in a way that is defamatory or that a reasonable person would find offensive.</li>
      <li><b>Sensitive uses.</b> {{if .allow_sensitive_uses}}The Images may be used to illustrate sensitive subjects.{{else}}The Images will not be used to suggest that I endorse a political cause, or in connection with sensitive subjects (including health, sexuality, crime or substance use), without my separate written consent.{{end}}</li>
      <li><b>Ownership and approval.</b> The Producer owns the Images and all rights in them. I waive any right to inspect or approve the finished Images or the material in which they appear.</li>
      <li><b>Release of claims.</b> I release the Producer from claims for invasion of privacy, right of publicity, defamation or any similar claim arising from use of the Images as permitted by this release.</li>
      <li><b>Personal data.</b> My contact details are kept only to administer this release and are not published. Rights under applicable data-protection law are unaffected.</li>
      <li><b>Capacity.</b> I am of legal age and have the right to sign this release in my own name, or my parent or legal guardian signs below on my behalf.</li>
      <li><b>Electronic signature.</b> I agree that signing this release electronically has the same effect as a handwritten signature. {{.governing_law_clause}}</li>
    </ol>
    {{if .additional_terms}}<div class="extra"><span class="label">Additional terms</span><p>{{.additional_terms}}</p></div>{{end}}
    <footer class="foot"><span>{{.company_name}} · Model release {{.reference}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>

  <section class="page" data-pdf-page>
    <header class="head small">
      <div>
        <p class="eyebrow">{{.company_name}}</p>
        <h2>Signatures</h2>
      </div>
      <dl class="ref"><div><dt>Reference</dt><dd>{{.reference}}</dd></div></dl>
    </header>
    <p class="confirm">I have read this release, I understand it, and I sign it voluntarily.</p>

    <div class="sign-block">
      <h3>Model</h3>
      <div class="sign-row">
        <div class="box sig"><span class="cap">Model signature:</span></div>
        <div class="box date"><span class="cap">Date:</span></div>
      </div>
      <div class="name-line"><span class="cap">Printed name</span><strong>{{.model_name}}</strong></div>
    </div>

    {{if .guardian_name}}    <div class="sign-block">
      <h3>Parent or guardian <em>(only if the model is under 18)</em></h3>
      <p class="note">I am the parent or legal guardian of the model named above and consent to this release on the model's behalf.</p>
      <div class="sign-row">
        <div class="box sig"><span class="cap">Guardian signature:</span></div>
        <div class="box date"><span class="cap">Date:</span></div>
      </div>
      <div class="name-line"><span class="cap">Printed name</span><strong>{{.guardian_name}}</strong></div>
    </div>{{end}}

    <div class="sign-block">
      <h3>Photographer / Producer</h3>
      <div class="sign-row">
        <div class="box sig"><span class="cap">Producer signature:</span></div>
        <div class="box date"><span class="cap">Date:</span></div>
      </div>
      <div class="name-line"><span class="cap">Printed name</span><strong>{{.photographer_name}}</strong></div>
    </div>

    <p class="seal-note">Signed through Apteva Signatures. The completed PDF carries a digital seal; any change to the file after signing invalidates it.</p>
    <footer class="foot"><span>{{.company_name}} · Model release {{.reference}}</span><span><span data-page-number></span> / <span data-page-total></span></span></footer>
  </section>
</article>
`,x=`:root { --ink:#111827; --muted:#4b5563; --line:#d1d5db; --soft:#f3f4f6; --accent:#1f2937; }
* { box-sizing:border-box; }
body { margin:0; font-family:"Helvetica Neue", Helvetica, Arial, sans-serif; color:var(--ink); font-size:9.2pt; line-height:1.42; }
.page { position:relative; width:210mm; height:297mm; padding:16mm 18mm 18mm; overflow:hidden; page-break-after:always; }
.head { display:flex; justify-content:space-between; align-items:flex-end; border-bottom:2px solid var(--accent); padding-bottom:4mm; margin-bottom:6mm; }
.eyebrow { margin:0 0 1.5mm; font-size:7.5pt; letter-spacing:.14em; text-transform:uppercase; color:var(--muted); }
h1 { margin:0; font-size:24pt; line-height:1.05; letter-spacing:-.01em; }
h2 { margin:0; font-size:18pt; }
.sub { margin:1.5mm 0 0; color:var(--muted); }
.ref { margin:0; display:grid; gap:1.5mm; text-align:right; }
.ref dt { font-size:7pt; text-transform:uppercase; letter-spacing:.1em; color:var(--muted); }
.ref dd { margin:0; font-weight:600; }
.parties { display:grid; grid-template-columns:1fr 1fr; gap:5mm; margin-bottom:5mm; }
.party { background:var(--soft); border-radius:2mm; padding:3.5mm 4mm; display:flex; flex-direction:column; gap:.6mm; }
.label, .cap { font-size:7pt; text-transform:uppercase; letter-spacing:.1em; color:var(--muted); }
.party strong { font-size:11pt; }
.facts { width:100%; border-collapse:collapse; margin-bottom:5mm; }
.facts th { width:34mm; text-align:left; font-weight:600; color:var(--muted); vertical-align:top; padding:1.6mm 0; border-bottom:1px solid var(--line); }
.facts td { padding:1.6mm 0; border-bottom:1px solid var(--line); }
.terms { margin:0; padding-left:5mm; display:grid; gap:2mm; }
.terms li { padding-left:1mm; }
.extra { margin-top:4mm; border-left:2px solid var(--accent); padding-left:3mm; }
.extra p { margin:1mm 0 0; }
.confirm { font-size:10.5pt; margin:0 0 7mm; }
.sign-block { border:1px solid var(--line); border-radius:2mm; padding:4mm 5mm; margin-bottom:6mm; }
.sign-block h3 { margin:0 0 3mm; font-size:11pt; }
.sign-block h3 em { font-weight:400; font-style:normal; color:var(--muted); font-size:9pt; }
.note { margin:-1.5mm 0 3mm; color:var(--muted); }
.sign-row { display:grid; grid-template-columns:1fr 46mm; gap:5mm; }
.box { height:20mm; border-bottom:1.2px solid var(--ink); position:relative; }
.box .cap { position:absolute; left:0; top:0; font-size:7.5pt; letter-spacing:0; text-transform:none; }
.name-line { margin-top:3mm; display:flex; gap:4mm; align-items:baseline; }
.name-line strong { font-weight:600; }
.seal-note { margin-top:8mm; font-size:8pt; color:var(--muted); }
.foot { position:absolute; left:18mm; right:18mm; bottom:9mm; display:flex; justify-content:space-between; border-top:1px solid var(--line); padding-top:2mm; font-size:7pt; color:var(--muted); }
`,j={layout_mode:"fixed",page_size:"A4",margin_top_mm:0,margin_right_mm:0,margin_bottom_mm:0,margin_left_mm:0,print_background:!0},oo={company_name:"Northlight Studio",company_address:"12 Rue des Arts, 75003 Paris, France",photographer_name:"Camille Laurent",reference:"MR-2026-0142",shoot_date:"14 November 2026",shoot_location:"Northlight Studio, Paris",model_name:"Jordan Rivera",model_address:"8 Avenue Victor Hugo, 75116 Paris",model_email:"jordan.rivera@example.com",guardian_name:"",project_description:"Autumn/Winter 2026 lookbook and campaign imagery for Northlight Studio clients",media:"All media now known or later developed, including print, web, social media and advertising",territory:"Worldwide",term:"In perpetuity",compensation:"EUR 450 flat fee, paid within 30 days of the shoot",allow_sensitive_uses:!1,additional_terms:"",governing_law_clause:"This release is governed by the laws of France."};import{jsx as o,jsxs as d,Fragment as uo}from"react/jsx-runtime";var to="/api/apps/docs",lo=`# New template

Replace this with your markdown. Use {{.variable}} placeholders.`,po=`# {{.title}}

## A practical guide for {{.audience}}

{{.introduction}}

---

## What you'll learn

{{range .takeaways}}- {{.}}
{{end}}

## The framework

{{range .steps}}### {{.title}}

{{.description}}

{{end}}

---

## Your next step

{{.call_to_action}}

**{{.brand_name}}** · {{.website}}`;function so({projectId:m,installId:e}){let[t,s]=g("templates"),[h,n]=g([]),[l,a]=g(null),[c,w]=g([]),[I,A]=g(""),[P,W]=g(!1),B=Y((i={})=>{return new URLSearchParams({project_id:m,install_id:String(e),...i}).toString()},[m,e]),y=Y(async(i,v,G)=>{let _={method:i,credentials:"same-origin",headers:G?{"Content-Type":"application/json"}:void 0};if(G!==void 0)_.body=JSON.stringify(G);let z=await fetch(`${to}${v}?${B()}`,_);if(!z.ok)throw Error(`${z.status}: ${await z.text().catch(()=>"")}`);return await z.json()},[B]),u=Y(async()=>{W(!0),A("");try{let i=await y("GET","/templates");n(i.templates||[])}catch(i){A(i.message)}finally{W(!1)}},[y]),f=Y(async()=>{W(!0),A("");try{let i=await y("GET","/renders");w(i.renders||[])}catch(i){A(i.message)}finally{W(!1)}},[y]);return M(()=>{if(t==="templates"||t==="render")u();if(t==="renders")f()},[t,u,f]),d("div",{className:"h-full flex flex-col p-6 gap-4",children:[d("div",{className:"flex items-center gap-2 border-b border-border",children:[[{key:"templates",label:"Templates"},{key:"render",label:"Render"},{key:"renders",label:"Audit log"}].map((i)=>o("button",{type:"button",onClick:()=>s(i.key),className:`px-3 py-2 text-sm border-b-2 transition-colors ${t===i.key?"border-accent text-accent":"border-transparent text-text-muted hover:text-text"}`,children:i.label},i.key)),o("div",{className:"flex-1"}),o("button",{type:"button",onClick:()=>t==="renders"?f():u(),className:"px-2 py-1 text-xs border border-border rounded hover:bg-bg-input",children:"Refresh"})]}),I&&o("div",{className:"px-3 py-2 text-sm text-red bg-red/10 border border-red/40 rounded",children:I}),t==="templates"&&o(co,{templates:h,selected:l,onSelect:a,onSave:async(i)=>{if(i.id){await y("PATCH",`/templates/${i.id}`,{name:i.name,description:i.description||"",body:i.body||"",stylesheet:i.stylesheet||"",source_format:i.source_format||"markdown",settings:i.settings||{},default_folder:i.default_folder||""});let v=await y("GET",`/templates/${i.id}`);return await u(),a(v.template),v.template}else{let v=await y("POST","/templates",{slug:i.slug,name:i.name,description:i.description||"",body:i.body||"",stylesheet:i.stylesheet||"",source_format:i.source_format||"markdown",settings:i.settings||{},default_folder:i.default_folder||""});return await u(),a(v.template),v.template}},onDelete:async(i)=>{await y("DELETE",`/templates/${i}`),a(null),await u()},api:y,loading:P}),t==="render"&&o(ho,{templates:h,api:y,onRendered:f}),t==="renders"&&o(fo,{renders:c,loading:P,onRefresh:f,projectId:m})]})}function co({templates:m,selected:e,onSelect:t,onSave:s,onDelete:h,api:n,loading:l}){let[a,c]=g(null),[w,I]=g(null),[A,P]=g(!1),[W,B]=g(!1),[y,u]=g(""),[f,F]=g("A4"),[i,v]=g("source"),[G,_]=g("{}"),[z,O]=g(!1),[C,U]=g(50),[N,p]=g("page-width"),k=K(()=>$(a?.body||""),[a?.body]),q=K(()=>Z(k),[k]),J=a?.source_format==="html"&&a.settings?.layout_mode!=="flow";M(()=>{if(!e||e.id===a?.id)return;let r=!0;return u(""),I(null),n("GET",`/templates/${e.id}`).then((b)=>{if(!r)return;c(b.template);let H=Z($(b.template.body||""));_(JSON.stringify(H,null,2)),O(!1),v("source"),F(b.template.settings?.page_size||"A4")}).catch((b)=>{if(r)u(`Could not load template: ${b.message}`)}),()=>{r=!1}},[e,n,a?.id]),M(()=>{return()=>{if(w)URL.revokeObjectURL(w)}},[w]),M(()=>{if(!a||z)return;_(JSON.stringify(q,null,2))},[a?.body,q,z]);let Q=(r="blank")=>{let b=r==="html-lead-magnet",H=r==="model-release"?{id:0,slug:"model-release",name:"Model release",body:D,stylesheet:x,source_format:"html",settings:j,default_folder:"/model-releases/"}:{id:0,slug:"",name:b?"ESP32 Starter Guide":"",body:b?S:r==="lead-magnet"?po:lo,stylesheet:b?V:"",source_format:b?"html":"markdown",settings:b?L:{page_size:"A4"},default_folder:b?"/lead-magnets/":""};c(H);let X=r==="model-release"?oo:b?R:Z($(H.body||""));_(JSON.stringify(X,null,2)),O(!1),v("source"),F(H.settings?.page_size||"A4"),I(null),u(""),t(null)},io=async()=>{if(!a)return;P(!0),u("");try{let r;try{r=JSON.parse(G)}catch(E){throw Error("Preview data isn't valid JSON: "+E.message)}if(!r||typeof r!=="object"||Array.isArray(r))throw Error("Preview data must be a JSON object.");let b=a.id||0,H=b?`/templates/${b}/preview`:"/templates/0/preview",X=await n("POST",H,{data:r,body:a.body,stylesheet:a.stylesheet||"",source_format:a.source_format||"markdown",settings:a.settings||{},page_size:f}),eo=Uint8Array.from(atob(X.base64),(E)=>E.charCodeAt(0)),mo=URL.createObjectURL(new Blob([eo],{type:X.content_type}));I(mo)}catch(r){u(r.message)}finally{P(!1)}};return d("div",{className:"flex-1 flex gap-4 min-h-0",children:[d("div",{className:"w-64 flex flex-col gap-2",children:[d("div",{className:"grid grid-cols-2 gap-2",children:[o("button",{type:"button",onClick:()=>Q("blank"),className:"px-2 py-2 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg",children:"+ Blank"}),o("button",{type:"button",onClick:()=>Q("lead-magnet"),className:"px-2 py-2 text-xs border border-border rounded hover:bg-bg-input",children:"Markdown lead"}),o("button",{type:"button",onClick:()=>Q("html-lead-magnet"),className:"col-span-2 px-2 py-2 text-xs border border-accent/50 text-text rounded hover:bg-bg-input",children:"+ Professional HTML lead magnet"}),o("button",{type:"button",onClick:()=>Q("model-release"),className:"col-span-2 px-2 py-2 text-xs border border-border rounded hover:bg-bg-input",children:"+ Model release (for Signatures)"})]}),o("div",{className:"flex-1 overflow-auto border border-border rounded",children:l&&m.length===0?o("div",{className:"p-3 text-text-muted text-xs text-center",children:"Loading…"}):m.length===0?o("div",{className:"p-3 text-text-muted text-xs text-center",children:"No templates yet. Create one to get started."}):m.map((r)=>d("button",{type:"button",onClick:()=>t(r),className:`w-full text-left px-3 py-2 text-sm border-b border-border last:border-b-0 ${e?.id===r.id?"bg-accent/10 text-accent":"hover:bg-bg-input/50"}`,children:[o("div",{className:"font-medium truncate",children:r.name}),d("div",{className:"flex items-center justify-between gap-2 text-xs text-text-dim",children:[o("span",{className:"truncate",children:r.slug}),o("span",{className:"uppercase",children:r.source_format||"markdown"})]})]},r.id))})]}),o("div",{className:"flex-1 flex flex-col gap-3 min-w-0",children:!a?o("div",{className:"flex-1 flex items-center justify-center text-text-muted text-sm",children:"Select a template on the left, or create a new one."}):d(uo,{children:[d("div",{className:"grid grid-cols-[1fr_1fr_9rem] gap-2",children:[o("input",{type:"text",value:a.slug,onChange:(r)=>c({...a,slug:r.target.value}),placeholder:"slug (e.g. invoice)",disabled:!!a.id,className:"bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono disabled:opacity-50"}),o("input",{type:"text",value:a.name,onChange:(r)=>c({...a,name:r.target.value}),placeholder:"display name",className:"bg-bg-input border border-border rounded px-2 py-1 text-sm"}),d("select",{"aria-label":"Template source format",value:a.source_format||"markdown",onChange:(r)=>{let b=r.target.value;c({...a,source_format:b}),v("source")},className:"bg-bg-input border border-border rounded px-2 py-1 text-sm",children:[o("option",{value:"markdown",children:"Markdown"}),o("option",{value:"html",children:"HTML/CSS"})]})]}),o("input",{type:"text",value:a.description||"",onChange:(r)=>c({...a,description:r.target.value}),placeholder:"description (optional)",className:"bg-bg-input border border-border rounded px-2 py-1 text-sm"}),o("input",{type:"text",value:a.default_folder||"",onChange:(r)=>c({...a,default_folder:r.target.value}),placeholder:"default output folder (e.g. /invoices/)",className:"bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"}),o(ro,{variables:k,sampleData:q,compact:!0}),o("div",{className:"flex items-center gap-1 border-b border-border",children:[["source",a.source_format==="html"?"HTML":"Markdown"],...a.source_format==="html"?[["css","CSS"]]:[],["data","Preview data"],...a.source_format==="html"?[["settings","Settings"]]:[]].map(([r,b])=>o("button",{type:"button",onClick:()=>v(r),className:`px-3 py-1.5 text-xs border-b-2 ${i===r?"border-accent text-accent":"border-transparent text-text-muted"}`,children:b},r))}),d("div",{className:"flex items-center gap-2",children:[d("select",{"aria-label":"Preview page size",value:f,onChange:(r)=>F(r.target.value),disabled:J,title:J?"Fixed-layout templates use the page size saved in Settings.":void 0,className:"bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-60",children:[o("option",{value:"A4",children:"A4"}),o("option",{value:"letter",children:"Letter"}),o("option",{value:"legal",children:"Legal"})]}),J&&o("span",{className:"text-xs text-text-dim",children:"Locked by template"}),o("button",{type:"button",onClick:io,disabled:A,className:"px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50",children:A?"Rendering…":"Preview"}),o("button",{type:"button",onClick:async()=>{if(!a.slug||!a.name||!a.body){u("slug, name, body required");return}B(!0),u("");try{let r=await s(a);c(r)}catch(r){u(r.message)}finally{B(!1)}},disabled:W,className:"px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg",children:W?"Saving…":"Save"}),a.id?o("button",{type:"button",onClick:async()=>{if(window.confirm("Delete this template? Past renders are kept."))try{await h(a.id),c(null),I(null)}catch(r){u(r.message)}},className:"px-3 py-1 text-sm border border-red/40 text-red rounded hover:bg-red/10",children:"Delete"}):null,o("div",{className:"flex-1"}),y&&o("span",{className:"text-xs text-red truncate",children:y})]}),d("div",{className:`flex-1 min-h-[36rem] gap-3 ${w?"grid":"flex"}`,style:w?{gridTemplateColumns:`${100-C}fr ${C}fr`}:void 0,children:[d("div",{className:"min-w-0 flex flex-col",children:[i==="source"&&o("textarea",{value:a.body||"",onChange:(r)=>c({...a,body:r.target.value}),placeholder:a.source_format==="html"?"<main>HTML with {{.placeholders}}</main>":"# Markdown with {{.placeholders}}",className:"flex-1 bg-bg-input border border-border rounded p-3 text-xs font-mono min-h-[28rem] resize-none",spellCheck:!1}),i==="css"&&a.source_format==="html"&&o("textarea",{value:a.stylesheet||"",onChange:(r)=>c({...a,stylesheet:r.target.value}),placeholder:"@page { margin: 0; }",className:"flex-1 bg-bg-input border border-border rounded p-3 text-xs font-mono min-h-[28rem] resize-none",spellCheck:!1}),i==="data"&&o("textarea",{value:G,onChange:(r)=>{_(r.target.value),O(!0)},placeholder:"Preview data as JSON",className:"flex-1 bg-bg-input border border-border rounded p-3 text-xs font-mono min-h-[28rem] resize-none",spellCheck:!1}),i==="settings"&&a.source_format==="html"&&o(go,{settings:a.settings||{page_size:"A4"},onChange:(r)=>{c({...a,settings:r}),F(r.page_size||"A4")}})]}),w&&d("div",{className:"min-w-0 flex flex-col border border-border rounded overflow-hidden bg-bg-input/20",children:[d("div",{className:"flex items-center gap-2 px-2 py-1.5 border-b border-border text-xs text-text-muted",children:[o("span",{children:"Preview"}),d("label",{className:"flex items-center gap-1 ml-auto",title:"Resize the preview pane",children:["Width",o("input",{"aria-label":"Preview pane width",type:"range",min:35,max:70,value:C,onChange:(r)=>U(Number(r.target.value)),className:"w-20"})]}),d("select",{"aria-label":"Preview zoom",value:N,onChange:(r)=>p(r.target.value),className:"bg-bg-input border border-border rounded px-1.5 py-1",children:[o("option",{value:"page-width",children:"Fit width"}),o("option",{value:"75",children:"75%"}),o("option",{value:"100",children:"100%"}),o("option",{value:"125",children:"125%"}),o("option",{value:"150",children:"150%"})]})]}),o("iframe",{title:"preview",src:`${w}#zoom=${N}`,className:"w-full flex-1 bg-white"})]})]})]})})]})}function go({settings:m,onChange:e}){let t=m.layout_mode==="flow"?12:0,s=(n,l)=>typeof m[n]==="number"?m[n]:l,h=(n,l)=>{let a=Math.max(0,Math.min(50,Number(l)||0));e({...m,[n]:a})};return d("div",{className:"flex-1 min-h-[18rem] border border-border rounded bg-bg-input/30 p-4 space-y-4",children:[d("div",{children:[o("div",{className:"text-sm font-medium text-text",children:"Print settings"}),o("p",{className:"text-xs text-text-muted mt-1",children:"Fixed layouts lock render-time page-size overrides. Flow layouts can be rendered on different paper sizes."})]}),d("div",{className:"grid grid-cols-3 gap-3",children:[d("label",{className:"text-xs text-text-muted",children:["Layout",d("select",{value:m.layout_mode||"fixed",onChange:(n)=>e({...m,layout_mode:n.target.value}),className:"mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm text-text",children:[o("option",{value:"flow",children:"Flow"}),o("option",{value:"fixed",children:"Fixed pages"})]})]}),d("label",{className:"text-xs text-text-muted",children:["Page size",d("select",{value:m.page_size||"A4",onChange:(n)=>e({...m,page_size:n.target.value}),className:"mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm text-text",children:[o("option",{value:"A4",children:"A4"}),o("option",{value:"letter",children:"Letter"}),o("option",{value:"legal",children:"Legal"})]})]}),d("div",{className:"grid grid-cols-2 gap-2 items-end",children:[d("label",{className:"flex items-center gap-2 text-xs text-text-muted pb-2",children:[o("input",{type:"checkbox",checked:!!m.landscape,onChange:(n)=>e({...m,landscape:n.target.checked})}),"Landscape"]}),d("label",{className:"flex items-center gap-2 text-xs text-text-muted pb-2",children:[o("input",{type:"checkbox",checked:m.print_background!==!1,onChange:(n)=>e({...m,print_background:n.target.checked})}),"Print backgrounds"]})]})]}),d("div",{children:[o("div",{className:"text-xs uppercase tracking-wide text-text-dim mb-2",children:"Margins (millimetres)"}),o("div",{className:"grid grid-cols-4 gap-2",children:[["margin_top_mm","Top"],["margin_right_mm","Right"],["margin_bottom_mm","Bottom"],["margin_left_mm","Left"]].map(([n,l])=>d("label",{className:"text-xs text-text-muted",children:[l,o("input",{type:"number",min:0,max:50,step:1,value:s(n,t),onChange:(a)=>h(n,a.target.value),className:"mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm text-text"})]},n))})]}),d("div",{className:"rounded border border-border bg-bg p-3 text-xs text-text-muted",children:["CSS can use ",o("code",{children:"--document-page-width"})," and ",o("code",{children:"--document-page-height"}),". For full-bleed fixed pages, set all margins to 0 and add page padding in CSS."]})]})}function ho({templates:m,api:e,onRendered:t}){let[s,h]=g(0),[n,l]=g(`{
  "customer": { "name": "Acme" }
}`),[a,c]=g(""),[w,I]=g(""),[A,P]=g("A4"),[W,B]=g(!1),[y,u]=g(!1),[f,F]=g(null),[i,v]=g(null),[G,_]=g(""),z=K(()=>m.find((p)=>p.id===s)||null,[m,s]),O=K(()=>$(f?.body||""),[f?.body]),C=K(()=>Z(O),[O]),U=f?.source_format==="html"&&f.settings?.layout_mode!=="flow";M(()=>{if(!z){F(null);return}let p=!0;return u(!0),v(null),_(""),e("GET",`/templates/${z.id}`).then((k)=>{if(p)F(k.template)}).catch((k)=>{if(p)_(`Could not load template: ${k.message}`)}).finally(()=>{if(p)u(!1)}),()=>{p=!1}},[z,e]),M(()=>{if(!f)return;l(JSON.stringify(C,null,2)),I(f.default_folder||""),P(f.settings?.page_size||"A4")},[f,C]);let N=async()=>{B(!0),_(""),v(null);try{let p;try{p=JSON.parse(n)}catch(q){throw Error("Data isn't valid JSON: "+q.message)}if(!p||typeof p!=="object"||Array.isArray(p))throw Error("Data must be a JSON object.");let k=await e("POST",`/templates/${s}/render`,{data:p,output_name:a||void 0,output_folder:w||void 0,page_size:A});v(k),t()}catch(p){_(p.message)}finally{B(!1)}};return d("div",{className:"flex-1 flex flex-col gap-3 max-w-3xl",children:[o("label",{className:"text-xs text-text-dim",children:"Template"}),d("select",{value:s,onChange:(p)=>h(Number(p.target.value)),className:"bg-bg-input border border-border rounded px-2 py-1 text-sm",children:[o("option",{value:0,children:"— pick one —"}),m.map((p)=>d("option",{value:p.id,children:[p.name," (",p.slug,")"]},p.id))]}),o("label",{className:"text-xs text-text-dim mt-2",children:"Data (JSON)"}),f&&d("div",{className:"text-xs text-text-muted",children:["Renderer: ",o("span",{className:"font-medium text-text",children:f.source_format==="html"?"HTML/CSS · Chromium":"Markdown · Maroto"})]}),o("textarea",{value:n,onChange:(p)=>l(p.target.value),className:"bg-bg-input border border-border rounded p-2 text-xs font-mono min-h-[12rem]",spellCheck:!1}),o(ro,{variables:O,sampleData:C,compact:!1}),d("div",{className:"grid grid-cols-2 gap-2",children:[o("input",{type:"text",value:a,onChange:(p)=>c(p.target.value),placeholder:"output filename (optional)",className:"bg-bg-input border border-border rounded px-2 py-1 text-sm"}),o("input",{type:"text",value:w,onChange:(p)=>I(p.target.value),placeholder:"output folder (optional)",className:"bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"})]}),o("label",{className:"text-xs text-text-dim",children:"Page size"}),d("select",{value:A,onChange:(p)=>P(p.target.value),disabled:U,title:U?"Fixed-layout templates use their saved page size.":void 0,className:"w-48 bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-60",children:[o("option",{value:"A4",children:"A4"}),o("option",{value:"letter",children:"Letter"}),o("option",{value:"legal",children:"Legal"})]}),U&&o("span",{className:"text-xs text-text-dim",children:"Locked by template settings"}),d("div",{className:"flex items-center gap-2",children:[o("button",{type:"button",onClick:N,disabled:W||y||!s||!f,className:"px-4 py-2 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50",children:W?"Rendering…":y?"Loading template…":"Render PDF"}),G&&o("span",{className:"text-xs text-red",children:G})]}),i?.url&&d("div",{className:"border border-green/40 bg-green/5 rounded p-3 flex flex-col gap-2",children:[o("span",{className:"text-sm text-green",children:"Rendered ✓"}),(i.renderer_version||i.template_revision_id)&&d("span",{className:"text-xs text-text-muted",children:[i.renderer_version||"renderer",i.template_revision_id?` · template revision ${i.template_revision_id}`:""]}),o("a",{href:i.url,target:"_blank",rel:"noopener",className:"text-xs text-accent hover:underline break-all",children:i.url}),i.warnings&&i.warnings.length>0&&d("div",{className:"text-xs text-amber-600",children:["Rendered with ",i.warnings.length," warning",i.warnings.length===1?"":"s",":",o("ul",{className:"list-disc ml-5 mt-1",children:i.warnings.map((p,k)=>o("li",{children:p},`${k}-${p}`))})]})]})]})}function $(m){let e=new Map,t=[],s=m.matchAll(/{{\s*([^{}]+?)\s*}}/g),h=(n,l,a="")=>{if(!n)return;let c=e.get(n);if(!c){e.set(n,{path:n,kind:l,children:a?[a]:[]});return}if(c.kind==="value"&&l!=="value")c.kind=l;if(c.kind==="list"&&l==="object_list")c.kind=l;if(a&&!c.children.includes(a))c.children.push(a)};for(let n of s){let l=n[1].trim();if(l.startsWith("/*")||l==="end"||l.startsWith("else")){if(l==="end")t.pop();continue}if(l.startsWith("range ")){let w=ao(l.replace(/^range\s+/,""));h(w,"list"),t.push(w);continue}if(l.startsWith("if ")||l.startsWith("with "))l=l.replace(/^(if|with)\s+/,"");let a=t[t.length-1]||"";if(a&&l==="."){h(a,"list");continue}if(a&&/^\.[A-Za-z0-9_]+$/.test(l)){h(a,"object_list",l.slice(1));continue}let c=ao(l);if(c)h(c,"value")}return Array.from(e.values()).sort((n,l)=>n.path.localeCompare(l.path))}function ao(m){let e=m.trim().split(/\s+/)[0]||"";if(!e.startsWith("."))return"";return e.slice(1).replace(/[|,)]*$/g,"").split(".").filter(Boolean).join(".")}function Z(m){let e={};for(let t of m){if(t.kind==="object_list"){T(e,t.path,[Object.fromEntries(t.children.map((s)=>[s,no(s)]))]);continue}if(t.kind==="list"){T(e,t.path,["Sample item"]);continue}T(e,t.path,no(t.path.split(".").at(-1)||t.path))}return e}function T(m,e,t){let s=e.split(".").filter(Boolean),h=m;s.forEach((n,l)=>{if(l===s.length-1){h[n]=t;return}if(!h[n]||typeof h[n]!=="object"||Array.isArray(h[n]))h[n]={};h=h[n]})}function no(m){let e=m.toLowerCase();if(e.includes("email"))return"hello@example.com";if(e.includes("website"))return"https://example.com";if(e.includes("date"))return new Date().toISOString().slice(0,10);if(e.includes("duration"))return"6 weeks";if(e.includes("amount")||e.includes("total"))return"$10,000";if(e==="qty"||e.includes("quantity"))return"1";if(e.includes("name"))return"Sample name";if(e.includes("contact"))return"Sample contact";if(e.includes("title")||e.includes("phase"))return"Sample title";if(e.includes("folder"))return"/docs/";return"Sample "+m.replace(/_/g," ")}function ro({variables:m,sampleData:e,compact:t}){if(m.length===0)return o("div",{className:"border border-border rounded px-3 py-2 text-xs text-text-dim",children:"No template variables detected."});return d("div",{className:"border border-border rounded bg-bg-input/30 p-3",children:[d("div",{className:"flex items-center justify-between gap-2 mb-2",children:[o("div",{className:"text-xs uppercase tracking-wide text-text-dim",children:"Variables"}),o("div",{className:"text-xs text-text-dim",children:m.length})]}),o("div",{className:"flex flex-wrap gap-1.5 mb-2",children:m.map((s)=>o("span",{className:"px-2 py-0.5 rounded border border-border text-xs font-mono text-text-muted",title:s.kind==="object_list"?s.children.join(", "):s.kind,children:s.kind==="object_list"?`${s.path}[]`:s.kind==="list"?`${s.path}[]`:s.path},s.path))}),!t&&o("pre",{className:"max-h-48 overflow-auto text-xs font-mono text-text-muted whitespace-pre-wrap",children:JSON.stringify(e,null,2)})]})}function fo({renders:m,loading:e,onRefresh:t,projectId:s}){let h=(n)=>{if(!n)return"—";if(n<1024)return`${n} B`;if(n<1048576)return`${(n/1024).toFixed(1)} kB`;return`${(n/1024/1024).toFixed(2)} MB`};if(e&&m.length===0)return o("div",{className:"text-text-muted text-sm text-center mt-12",children:"Loading…"});if(m.length===0)return d("div",{className:"flex-1 flex flex-col items-center justify-center gap-3 text-text-muted text-sm",children:[o("div",{children:"No renders yet. Use the Render tab to produce one."}),o("button",{type:"button",onClick:t,className:"px-3 py-1 text-xs border border-border rounded hover:bg-bg-input",children:"Refresh audit log"})]});return d("div",{className:"flex-1 flex flex-col min-h-0 gap-2",children:[d("div",{className:"flex items-center justify-between text-xs text-text-dim",children:[d("span",{children:[m.length," render",m.length===1?"":"s"]}),o("button",{type:"button",onClick:t,className:"px-2 py-1 border border-border rounded hover:bg-bg-input",children:"Refresh"})]}),o("div",{className:"flex-1 overflow-auto border border-border rounded",children:d("table",{className:"w-full text-sm",children:[o("thead",{className:"text-text-dim text-xs uppercase tracking-wide bg-bg-input/50",children:d("tr",{children:[o("th",{className:"text-left px-4 py-2 font-normal",children:"Template"}),o("th",{className:"text-left px-4 py-2 font-normal",children:"File"}),o("th",{className:"text-left px-4 py-2 font-normal w-24",children:"Size"}),o("th",{className:"text-left px-4 py-2 font-normal w-48",children:"Rendered at"}),o("th",{className:"text-right px-4 py-2 font-normal w-24",children:"Open"})]})}),o("tbody",{children:m.map((n)=>d("tr",{className:"border-t border-border hover:bg-bg-input/30",children:[d("td",{className:"px-4 py-2",children:[o("div",{className:"text-text font-medium",children:n.template_slug}),d("div",{className:"text-xs text-text-dim",children:["render #",n.id,n.renderer_version?` · ${n.renderer_version}`:"",n.template_revision_id?` · rev ${n.template_revision_id}`:""]})]}),d("td",{className:"px-4 py-2",children:[o("div",{className:"text-text truncate max-w-md",title:n.output_name,children:n.output_name||"—"}),d("div",{className:"text-xs text-text-dim font-mono",children:["storage:",n.output_file_id]})]}),o("td",{className:"px-4 py-2 text-text-muted",children:h(n.bytes)}),o("td",{className:"px-4 py-2 text-text-muted text-xs",children:new Date(n.rendered_at).toLocaleString()}),o("td",{className:"px-4 py-2 text-right",children:n.output_file_id?o("a",{href:bo(n,s),target:"_blank",rel:"noopener",className:"text-xs text-accent hover:underline",children:"View PDF"}):o("span",{className:"text-xs text-text-dim",children:"—"})})]},n.id))})]})})]})}function bo(m,e){let t=m.output_name||`render-${m.id}.pdf`,s=e?`?project_id=${encodeURIComponent(e)}`:"";return`/api/apps/storage/files/${encodeURIComponent(m.output_file_id)}/content/${encodeURIComponent(t)}${s}`}export{so as default};

//# debugId=4EC1153AB489B7DA64756E2164756E21
