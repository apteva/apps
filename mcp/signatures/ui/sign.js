// Signer-side enhancement for the Signatures signing page.
//
// Renders the PDF with pdf.js and overlays this recipient's field
// boxes at the exact positions the sender placed, so the signer sees
// where each signature/initial/date will land. Also upgrades signature
// inputs with a Type/Draw toggle; drawn signatures submit as a
// data:image/png payload in the same form field.
//
// The page works without this script (noscript iframe + plain form);
// everything here is progressive enhancement. All same-origin URLs
// must keep the page's query string (project_id) — the platform
// resolves the app install from it on anonymous requests.

const dataEl = document.getElementById("signing-data");
const doc = document.getElementById("doc");
if (dataEl && doc) {
  boot(JSON.parse(dataEl.textContent)).catch((err) => {
    console.error("signing page enhancement failed:", err);
    fallbackIframe();
  });
}

function pageQuery() {
  return new URL(import.meta.url).search || "";
}

function fallbackIframe() {
  const payload = JSON.parse(dataEl.textContent);
  doc.innerHTML = "";
  const frame = document.createElement("iframe");
  frame.src = payload.doc_url;
  frame.title = "Document";
  frame.style.cssText = "width:100%;height:75vh;border:0;background:#fff";
  doc.appendChild(frame);
}

async function boot(payload) {
  const q = pageQuery();
  const pdfjs = await import(new URL("./pdf.min.mjs" + q, import.meta.url).href);
  pdfjs.GlobalWorkerOptions.workerSrc = new URL("./pdf.worker.min.mjs" + q, import.meta.url).href;

  const bytes = await fetch(payload.doc_url, { credentials: "same-origin" }).then((r) => {
    if (!r.ok) throw new Error("document fetch failed: " + r.status);
    return r.arrayBuffer();
  });
  const pdf = await pdfjs.getDocument({ data: bytes }).promise;

  doc.innerHTML = "";
  const fieldsByPage = new Map();
  for (const f of payload.fields || []) {
    if (!fieldsByPage.has(f.page)) fieldsByPage.set(f.page, []);
    fieldsByPage.get(f.page).push(f);
  }

  const boxes = new Map(); // field id -> box element
  for (let n = 1; n <= pdf.numPages; n++) {
    const page = await pdf.getPage(n);
    const wrap = document.createElement("div");
    wrap.className = "pdfpage";
    doc.appendChild(wrap);

    const base = page.getViewport({ scale: 1 });
    const cssWidth = Math.min(doc.clientWidth - 28, 900);
    const scale = cssWidth / base.width;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const viewport = page.getViewport({ scale: scale * dpr });

    const canvas = document.createElement("canvas");
    canvas.width = Math.floor(viewport.width);
    canvas.height = Math.floor(viewport.height);
    wrap.style.width = cssWidth + "px";
    wrap.style.height = Math.floor(base.height * scale) + "px";
    wrap.appendChild(canvas);
    await page.render({ canvasContext: canvas.getContext("2d"), viewport }).promise;

    for (const f of fieldsByPage.get(n) || []) {
      const box = document.createElement("div");
      box.className = "fieldbox";
      box.style.left = (f.x * 100).toFixed(3) + "%";
      box.style.top = (f.y * 100).toFixed(3) + "%";
      box.style.width = (f.width * 100).toFixed(3) + "%";
      box.style.height = (f.height * 100).toFixed(3) + "%";
      box.title = f.label + (f.required ? " (required)" : "");
      box.textContent = f.label;
      box.addEventListener("click", () => focusField(f));
      wrap.appendChild(box);
      boxes.set(f.id, box);
    }
  }

  const inputs = new Map(); // field id -> {input, type}
  for (const label of document.querySelectorAll("label[data-field-id]")) {
    const id = Number(label.dataset.fieldId);
    const type = label.dataset.fieldType;
    const input = label.querySelector("input");
    if (!input) continue;
    inputs.set(id, { input, type, label: label });
    if (type === "signature") enhanceSignature(id, label, input);
    input.addEventListener("input", () => paint(id));
    input.addEventListener("focus", () => highlight(id, true));
    input.addEventListener("blur", () => highlight(id, false));
    paint(id);
  }

  function focusField(f) {
    const entry = inputs.get(f.id);
    if (!entry) return;
    entry.label.scrollIntoView({ behavior: "smooth", block: "center" });
    if (entry.type === "checkbox") {
      entry.input.checked = !entry.input.checked;
      paint(f.id);
    } else if (!entry.input.disabled && entry.input.type !== "hidden") {
      entry.input.focus({ preventScroll: true });
    }
  }

  function highlight(id, on) {
    const box = boxes.get(id);
    if (box) box.classList.toggle("focus", on);
    if (on) box && box.scrollIntoView({ behavior: "smooth", block: "center" });
  }

  function paint(id) {
    const box = boxes.get(id);
    const entry = inputs.get(id);
    if (!box || !entry) return;
    const f = (payload.fields || []).find((x) => x.id === id);
    box.innerHTML = "";
    let done = false;
    if (entry.type === "checkbox") {
      done = entry.input.checked;
      box.textContent = done ? "✓ " + f.label : f.label;
    } else if (entry.type === "date_signed") {
      box.textContent = new Date().toISOString().slice(0, 10) + " (auto)";
      done = true;
    } else if (entry.input.value.startsWith("data:image/")) {
      const img = document.createElement("img");
      img.src = entry.input.value;
      img.alt = f.label;
      box.appendChild(img);
      done = true;
    } else if (entry.input.value.trim()) {
      const span = document.createElement("span");
      span.className = entry.type === "signature" ? "sigtext" : "";
      span.textContent = entry.input.value;
      box.appendChild(span);
      done = true;
    } else {
      box.textContent = f.label;
    }
    box.classList.toggle("done", done);
  }

  function enhanceSignature(id, label, input) {
    const f = (payload.fields || []).find((x) => x.id === id);
    const aspect = f && f.height > 0 ? f.width / f.height : 4;

    const tabs = document.createElement("div");
    tabs.className = "sigtabs";
    const typeBtn = document.createElement("button");
    typeBtn.type = "button";
    typeBtn.textContent = "Type";
    typeBtn.className = "on";
    const drawBtn = document.createElement("button");
    drawBtn.type = "button";
    drawBtn.textContent = "Draw";
    tabs.append(typeBtn, drawBtn);

    const pad = document.createElement("canvas");
    pad.className = "sigpad";
    pad.style.display = "none";
    const padRow = document.createElement("div");
    padRow.className = "sigpadrow";
    padRow.style.display = "none";
    const hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = "Draw your signature above";
    const clearBtn = document.createElement("button");
    clearBtn.type = "button";
    clearBtn.textContent = "Clear";
    padRow.append(hint, clearBtn);

    input.insertAdjacentElement("afterend", pad);
    pad.insertAdjacentElement("afterend", padRow);
    input.insertAdjacentElement("beforebegin", tabs);

    const ctx = pad.getContext("2d");
    let drawing = false;
    let drew = false;
    let typedValue = "";

    function sizePad() {
      const w = Math.max(pad.parentElement.clientWidth - 2, 260);
      const h = Math.round(w / Math.max(aspect, 1.5));
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      pad.style.height = h + "px";
      pad.width = Math.floor(w * dpr);
      pad.height = Math.floor(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.lineWidth = 2.4;
      ctx.lineCap = "round";
      ctx.lineJoin = "round";
      ctx.strokeStyle = "#172033";
      drew = false;
    }

    function pos(e) {
      const r = pad.getBoundingClientRect();
      return [e.clientX - r.left, e.clientY - r.top];
    }
    pad.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      pad.setPointerCapture(e.pointerId);
      drawing = true;
      drew = true;
      const [x, y] = pos(e);
      ctx.beginPath();
      ctx.moveTo(x, y);
    });
    pad.addEventListener("pointermove", (e) => {
      if (!drawing) return;
      const [x, y] = pos(e);
      ctx.lineTo(x, y);
      ctx.stroke();
    });
    const stop = () => {
      if (!drawing) return;
      drawing = false;
      if (drew) {
        input.value = pad.toDataURL("image/png");
        paint(id);
      }
    };
    pad.addEventListener("pointerup", stop);
    pad.addEventListener("pointercancel", stop);
    clearBtn.addEventListener("click", () => {
      ctx.clearRect(0, 0, pad.width, pad.height);
      drew = false;
      input.value = "";
      paint(id);
    });

    typeBtn.addEventListener("click", () => {
      typeBtn.className = "on";
      drawBtn.className = "";
      pad.style.display = "none";
      padRow.style.display = "none";
      input.type = "text";
      input.value = typedValue;
      paint(id);
    });
    drawBtn.addEventListener("click", () => {
      drawBtn.className = "on";
      typeBtn.className = "";
      if (input.type !== "hidden") typedValue = input.value;
      input.type = "hidden";
      input.value = "";
      pad.style.display = "block";
      padRow.style.display = "flex";
      sizePad();
      paint(id);
    });
  }
}
