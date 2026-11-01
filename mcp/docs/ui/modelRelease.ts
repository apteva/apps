// Model release starter: an HTML/CSS template whose signature page is
// laid out for Apteva Signatures. Each signer has a labelled box
// ("Model signature:", "Date:", …) that Signatures fields can be placed
// over; the guardian block only renders when guardian_name is set.

export const MODEL_RELEASE_STARTER = `<article class="release">
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
`;

export const MODEL_RELEASE_CSS = `:root { --ink:#111827; --muted:#4b5563; --line:#d1d5db; --soft:#f3f4f6; --accent:#1f2937; }
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
`;

export const MODEL_RELEASE_SETTINGS = {
  layout_mode: "fixed" as const,
  page_size: "A4",
  margin_top_mm: 0,
  margin_right_mm: 0,
  margin_bottom_mm: 0,
  margin_left_mm: 0,
  print_background: true,
};

export const MODEL_RELEASE_SAMPLE_DATA = {
  "company_name": "Northlight Studio",
  "company_address": "12 Rue des Arts, 75003 Paris, France",
  "photographer_name": "Camille Laurent",
  "reference": "MR-2026-0142",
  "shoot_date": "14 November 2026",
  "shoot_location": "Northlight Studio, Paris",
  "model_name": "Jordan Rivera",
  "model_address": "8 Avenue Victor Hugo, 75116 Paris",
  "model_email": "jordan.rivera@example.com",
  "guardian_name": "",
  "project_description": "Autumn/Winter 2026 lookbook and campaign imagery for Northlight Studio clients",
  "media": "All media now known or later developed, including print, web, social media and advertising",
  "territory": "Worldwide",
  "term": "In perpetuity",
  "compensation": "EUR 450 flat fee, paid within 30 days of the shoot",
  "allow_sensitive_uses": false,
  "additional_terms": "",
  "governing_law_clause": "This release is governed by the laws of France."
};
