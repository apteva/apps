export const HTML_LEAD_MAGNET_STARTER = `<article class="document">
  <section class="page cover">
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

  <section class="page">
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
    <footer class="page-footer"><span>{{.title}}</span><span>2</span></footer>
  </section>

  <section class="page">
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
    <footer class="page-footer"><span>{{.title}}</span><span>3</span></footer>
  </section>

  <section class="page">
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
  String json = "{\\\"value\\\":" + String(value) + "}";
  server.send(200, "application/json", json);
}</code></pre>
    <div class="two-up">
      <div class="panel"><span>ON STARTUP</span><h3>Print the address</h3><p>After connecting, output <code>WiFi.localIP()</code> so you always know which URL to open.</p></div>
      <div class="panel"><span>IN THE LOOP</span><h3>Handle requests</h3><p>Call <code>server.handleClient()</code> continuously. Long delays make the page feel unresponsive.</p></div>
    </div>
    <div class="callout next"><span>NEXT STEP</span><p>Open the printed IP address on a phone connected to the same WiFi network. If the status endpoint returns a number, the hard part is done.</p></div>
    <footer class="page-footer"><span>{{.title}}</span><span>4</span></footer>
  </section>

  <section class="page final-page">
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
    <footer class="page-footer"><span>{{.brand_name}} · {{.website}}</span><span>5</span></footer>
  </section>
</article>`;

export const HTML_LEAD_MAGNET_CSS = `
@page { size: A4; margin: 0; }
:root { --ink:#172033; --muted:#657086; --line:#dbe3ee; --paper:#fff; --soft:#f3f6fb; --brand:#5b4ce6; --brand2:#17a8c7; --accent:#ff8a3d; }
* { box-sizing:border-box; }
body { font-family:Arial,Helvetica,sans-serif; background:#fff; color:var(--ink); }
.document { margin:0; }
.page { position:relative; width:210mm; height:297mm; overflow:hidden; padding:17mm 18mm 15mm; background:var(--paper); break-after:page; }
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
`;

export const HTML_LEAD_MAGNET_SETTINGS = {
  page_size: "A4",
  margin_top_mm: 0,
  margin_right_mm: 0,
  margin_bottom_mm: 0,
  margin_left_mm: 0,
  print_background: true,
};

export const HTML_LEAD_MAGNET_SAMPLE_DATA = {
  brand_name: "Makecademy",
  edition: "2026",
  title: "ESP32 Starter Guide",
  promise: "From a blank board to a live browser dashboard - one clear step at a time.",
  duration: "90-minute build",
  outcome: "An ESP32 that reads a sensor, joins WiFi, and serves a dashboard you can open from your phone.",
  website: "https://makecademy.com",
  parts: [
    { item: "ESP32 development board", purpose: "Runs the sensor and web server", qty: "1" },
    { item: "Analog sensor or potentiometer", purpose: "Provides a changing input", qty: "1" },
    { item: "Breadboard and jumper wires", purpose: "Creates a reusable prototype", qty: "1 set" },
    { item: "USB data cable", purpose: "Powers and programs the board", qty: "1" },
  ],
  checklist: [
    "The sensor value changes reliably.",
    "WiFi reconnects after a restart.",
    "The board prints its local IP address.",
    "The dashboard opens from another device.",
    "No credentials are shown in screenshots.",
    "The finished wiring is photographed.",
  ],
  cta_title: "Turn this prototype into your next connected product.",
  cta_body: "Use the same staged method to add displays, alerts, storage, and automation without losing control of the build.",
};
