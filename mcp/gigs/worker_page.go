package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The worker page is served at /worker/<token>[/sub] with NoAuth — the
// magic_token in the path is the auth. Workers don't have Apteva
// accounts; they get a link via their CRM-bound channel and submit
// from there.

func (a *App) handleWorkerRoot(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/worker/")
	parts := strings.SplitN(rest, "/", 2)
	token := parts[0]
	if token == "" {
		httpErr(w, http.StatusBadRequest, "token required")
		return
	}
	if len(parts) == 1 || parts[1] == "" {
		// HTML page.
		a.handleWorkerPage(w, r, token)
		return
	}
	switch parts[1] {
	case "api/gig":
		a.handleWorkerGigJSON(w, r, token)
	case "submit":
		a.handleWorkerSubmit(w, r, token)
	case "upload":
		a.handleWorkerUpload(w, r, token)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

// ─── HTML ───────────────────────────────────────────────────────────

func (a *App) handleWorkerPage(w http.ResponseWriter, _ *http.Request, token string) {
	// We don't render server-side — the page is a single-file shell
	// that fetches /api/gig and renders client-side. Keeps the page
	// stateless and easy to iterate on.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(workerPageHTML(token)))
}

func workerPageHTML(token string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
<title>Gig</title>
<style>
  :root {
    --bg: #fafaf9;
    --fg: #1c1917;
    --muted: #78716c;
    --line: #e7e5e4;
    --accent: #2563eb;
    --accent-fg: #ffffff;
    --warn: #b45309;
    --crit: #b91c1c;
    --ok: #15803d;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0c0a09;
      --fg: #fafaf9;
      --muted: #a8a29e;
      --line: #292524;
      --accent: #3b82f6;
    }
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; background: var(--bg); color: var(--fg); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; }
  main { max-width: 720px; margin: 0 auto; padding: 24px 16px 96px; }
  h1 { font-size: 22px; margin: 0 0 4px; line-height: 1.2; }
  .meta { color: var(--muted); font-size: 13px; margin-bottom: 24px; }
  .instr { padding: 16px 0; border-top: 1px solid var(--line); }
  .instr:first-of-type { border-top: none; padding-top: 0; }
  .label { font-size: 13px; color: var(--muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.02em; }
  .text { white-space: pre-wrap; line-height: 1.5; }
  .warning { padding: 12px; border-radius: 8px; background: rgba(180, 83, 9, 0.1); color: var(--warn); }
  .warning.critical { background: rgba(185, 28, 28, 0.1); color: var(--crit); }
  .script { background: rgba(0,0,0,0.04); border-radius: 8px; padding: 12px; font-style: italic; }
  @media (prefers-color-scheme: dark) {
    .script { background: rgba(255,255,255,0.05); }
  }
  .script p { margin: 4px 0; }
  audio, video, img { max-width: 100%; border-radius: 8px; }
  a.link { color: var(--accent); }
  .check { display: flex; align-items: flex-start; gap: 10px; padding: 8px 0; cursor: pointer; }
  .check input { margin-top: 4px; transform: scale(1.2); }
  input[type=text], input[type=number], input[type=date], textarea, select {
    width: 100%; padding: 10px 12px; font-size: 16px;
    border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg);
  }
  textarea { min-height: 100px; resize: vertical; font-family: inherit; }
  .options label { display: flex; align-items: center; gap: 8px; padding: 6px 0; cursor: pointer; }
  .rating { display: flex; gap: 6px; }
  .rating button { font-size: 22px; padding: 8px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--muted); cursor: pointer; }
  .rating button.on { background: var(--accent); color: var(--accent-fg); border-color: var(--accent); }
  .yn { display: flex; gap: 8px; }
  .yn button { flex: 1; padding: 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg); cursor: pointer; font-size: 16px; }
  .yn button.on { background: var(--accent); color: var(--accent-fg); border-color: var(--accent); }
  .submit-bar {
    position: fixed; bottom: 0; left: 0; right: 0;
    background: var(--bg); border-top: 1px solid var(--line);
    padding: 12px 16px env(safe-area-inset-bottom);
  }
  .submit-bar .row { max-width: 720px; margin: 0 auto; display: flex; gap: 12px; align-items: center; }
  button.primary {
    background: var(--accent); color: var(--accent-fg); border: none; border-radius: 8px;
    padding: 12px 20px; font-size: 16px; font-weight: 600; cursor: pointer; flex: 1;
  }
  button.primary:disabled { opacity: 0.5; cursor: not-allowed; }
  .status { font-size: 13px; color: var(--muted); flex: 0 0 auto; }
  .done { padding: 40px 16px; text-align: center; color: var(--ok); font-size: 18px; }
</style>
</head>
<body>
  <main id="app">Loading...</main>
  <script>
    const TOKEN = ` + jsString(token) + `;
    const API = "/api/apps/gigs/worker/" + TOKEN;
    const result = {};
    let gig = null;

    fetch(API + "/api/gig").then(r => r.json()).then(data => {
      if (data.error) {
        document.getElementById("app").innerHTML = "<p style='color:var(--crit)'>" + escapeHTML(data.error) + "</p>";
        return;
      }
      gig = data.gig;
      if (gig.assignment_status === "submitted") {
        document.getElementById("app").innerHTML = "<div class='done'>Submission received. Thank you!</div>";
        return;
      }
      render();
    });

    function render() {
      const app = document.getElementById("app");
      app.innerHTML = "";
      const title = document.createElement("h1");
      title.textContent = gig.title;
      app.appendChild(title);
      const meta = document.createElement("div");
      meta.className = "meta";
      meta.textContent = "Deadline: " + (gig.deadline_at || "no deadline");
      app.appendChild(meta);
      for (const it of gig.composition) {
        app.appendChild(renderInstruction(it));
      }
      const bar = document.createElement("div");
      bar.className = "submit-bar";
      bar.innerHTML = '<div class="row"><span class="status" id="status"></span><button class="primary" id="submit">Submit</button></div>';
      document.body.appendChild(bar);
      document.getElementById("submit").addEventListener("click", submit);
    }

    function renderInstruction(it) {
      const wrap = document.createElement("div");
      wrap.className = "instr";
      const body = it.rendered_body || {};
      switch (it.instruction_kind) {
        case "text":
          wrap.innerHTML = '<div class="text">' + escapeHTML(body.markdown || "") + '</div>';
          break;
        case "audio":
          if (it.signed_url) wrap.innerHTML = '<audio controls src="' + it.signed_url + '"></audio>';
          else wrap.innerHTML = '<div class="meta">[audio unavailable]</div>';
          break;
        case "video":
          if (it.signed_url) wrap.innerHTML = '<video controls src="' + it.signed_url + '"></video>';
          else wrap.innerHTML = '<div class="meta">[video unavailable]</div>';
          break;
        case "image":
          if (it.signed_url) wrap.innerHTML = '<img alt="' + escapeAttr(body.caption||"") + '" src="' + it.signed_url + '" />';
          break;
        case "document":
          if (it.signed_url) wrap.innerHTML = '<a class="link" href="' + it.signed_url + '" target="_blank">Open document</a>';
          break;
        case "link":
          wrap.innerHTML = '<a class="link" href="' + escapeAttr(body.url||"") + '" target="_blank">' + escapeHTML(body.label || body.url || "Open") + '</a>';
          break;
        case "script":
          const lines = (body.lines || []).map(l => '<p>"' + escapeHTML(l) + '"</p>').join("");
          wrap.innerHTML = '<div class="label">Say:</div><div class="script">' + lines + '</div>';
          break;
        case "warning":
          const sev = body.severity === "critical" ? "warning critical" : "warning";
          wrap.innerHTML = '<div class="' + sev + '">' + escapeHTML(body.text||"") + '</div>';
          break;
        case "example":
          let ex = "";
          if (body.good_text) ex += '<div class="label">Good</div><div class="text">' + escapeHTML(body.good_text) + '</div>';
          if (body.bad_text) ex += '<div class="label" style="margin-top:8px">Avoid</div><div class="text">' + escapeHTML(body.bad_text) + '</div>';
          wrap.innerHTML = ex;
          break;
        case "checklist_item":
        case "confirmation": {
          const lbl = document.createElement("label");
          lbl.className = "check";
          const cb = document.createElement("input");
          cb.type = "checkbox";
          cb.addEventListener("change", () => { result[it.result_key] = cb.checked; });
          lbl.appendChild(cb);
          const sp = document.createElement("span");
          sp.textContent = body.text || "";
          lbl.appendChild(sp);
          wrap.appendChild(lbl);
          break;
        }
        case "timer_hint":
          wrap.innerHTML = '<div class="meta">Suggested time: ' + (body.seconds_suggested || 0) + 's</div>';
          break;
        default:
          if (it.instruction_kind.startsWith("input_")) {
            renderInput(wrap, it, body);
          } else {
            wrap.innerHTML = '<div class="meta">[unknown instruction kind: ' + escapeHTML(it.instruction_kind) + ']</div>';
          }
      }
      return wrap;
    }

    function renderInput(wrap, it, body) {
      const k = it.instruction_kind;
      const key = it.result_key;
      const lbl = document.createElement("div");
      lbl.className = "label";
      lbl.textContent = body.label || key;
      wrap.appendChild(lbl);
      let el;
      switch (k) {
        case "input_short_text":
          el = document.createElement("input"); el.type = "text"; el.placeholder = body.placeholder || "";
          el.addEventListener("input", () => result[key] = el.value);
          break;
        case "input_long_text":
          el = document.createElement("textarea"); el.placeholder = body.placeholder || "";
          el.addEventListener("input", () => result[key] = el.value);
          break;
        case "input_number":
          el = document.createElement("input"); el.type = "number";
          if (body.min !== undefined) el.min = body.min; if (body.max !== undefined) el.max = body.max;
          el.addEventListener("input", () => result[key] = parseFloat(el.value));
          break;
        case "input_date":
          el = document.createElement("input"); el.type = "date";
          el.addEventListener("input", () => result[key] = el.value);
          break;
        case "input_choice":
          el = document.createElement("select");
          el.innerHTML = '<option value="">—</option>' + (body.options || []).map(o => {
            const v = typeof o === "string" ? o : o.value;
            const lab = typeof o === "string" ? o : (o.label || o.value);
            return '<option value="' + escapeAttr(v) + '">' + escapeHTML(lab) + '</option>';
          }).join("");
          el.addEventListener("change", () => result[key] = el.value);
          break;
        case "input_multi_choice":
          el = document.createElement("div"); el.className = "options";
          (body.options || []).forEach(o => {
            const v = typeof o === "string" ? o : o.value;
            const lab = typeof o === "string" ? o : (o.label || o.value);
            const id = key + "-" + v;
            const wrap2 = document.createElement("label");
            wrap2.innerHTML = '<input type="checkbox" value="' + escapeAttr(v) + '" id="' + escapeAttr(id) + '" /> ' + escapeHTML(lab);
            wrap2.querySelector("input").addEventListener("change", () => {
              const ckd = Array.from(el.querySelectorAll("input:checked")).map(i => i.value);
              result[key] = ckd;
            });
            el.appendChild(wrap2);
          });
          break;
        case "input_rating":
          el = document.createElement("div"); el.className = "rating";
          const scale = body.scale || 5;
          for (let i = 1; i <= scale; i++) {
            const b = document.createElement("button"); b.type = "button"; b.textContent = "★";
            b.addEventListener("click", () => {
              result[key] = i;
              Array.from(el.children).forEach((c, idx) => c.classList.toggle("on", idx < i));
            });
            el.appendChild(b);
          }
          break;
        case "input_yes_no":
          el = document.createElement("div"); el.className = "yn";
          ["yes","no"].forEach(v => {
            const b = document.createElement("button"); b.type = "button"; b.textContent = v.toUpperCase();
            b.addEventListener("click", () => {
              result[key] = (v === "yes");
              Array.from(el.children).forEach(c => c.classList.toggle("on", c.textContent === v.toUpperCase()));
            });
            el.appendChild(b);
          });
          break;
        case "input_photo":
        case "input_audio_recording":
        case "input_video_recording":
        case "input_file":
        case "input_signature":
          el = document.createElement("input"); el.type = "file";
          if (k === "input_photo") el.accept = "image/*";
          if (k === "input_audio_recording") el.accept = "audio/*";
          if (k === "input_video_recording") el.accept = "video/*";
          el.addEventListener("change", async () => {
            const file = el.files && el.files[0];
            if (!file) return;
            setStatus("Uploading " + file.name + "...");
            const id = await uploadFile(file);
            if (id) { result[key] = { storage_file_id: id, filename: file.name, mime: file.type }; setStatus("Uploaded."); }
          });
          break;
        case "input_location":
          el = document.createElement("button"); el.type = "button"; el.className = "primary"; el.textContent = "Use my location";
          el.addEventListener("click", () => {
            navigator.geolocation.getCurrentPosition(p => {
              result[key] = { lat: p.coords.latitude, lng: p.coords.longitude, accuracy_m: p.coords.accuracy };
              el.textContent = "Location captured (±" + Math.round(p.coords.accuracy) + " m)";
            }, e => setStatus("Location error: " + e.message));
          });
          break;
        default:
          el = document.createElement("div"); el.className = "meta";
          el.textContent = "[unsupported input: " + k + "]";
      }
      wrap.appendChild(el);
    }

    async function uploadFile(file) {
      const buf = await file.arrayBuffer();
      const b64 = arrayBufferToBase64(buf);
      const res = await fetch(API + "/upload", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: file.name, content_type: file.type, content_base64: b64 }),
      });
      const j = await res.json();
      if (j.error) { setStatus("Upload failed: " + j.error); return null; }
      return j.storage_file_id;
    }

    async function submit() {
      const res = await fetch(API + "/submit", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ payload: result }),
      });
      const j = await res.json();
      if (j.error) { setStatus("Submit failed: " + j.error); return; }
      document.body.innerHTML = "<main><div class='done'>Submission received. Thank you!</div></main>";
    }

    function setStatus(s) { document.getElementById("status").textContent = s; }
    function escapeHTML(s) { return String(s||"").replace(/[&<>]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;"}[c])); }
    function escapeAttr(s) { return String(s||"").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c])); }
    function arrayBufferToBase64(buf) {
      const bytes = new Uint8Array(buf);
      let s = ""; for (let i = 0; i < bytes.byteLength; i++) s += String.fromCharCode(bytes[i]);
      return btoa(s);
    }
  </script>
</body>
</html>`
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ─── API: gig JSON, submit, upload ──────────────────────────────────

type workerGigPayload struct {
	GigID            int64                   `json:"gig_id"`
	Title            string                  `json:"title"`
	DeadlineAt       string                  `json:"deadline_at,omitempty"`
	AssignmentStatus string                  `json:"assignment_status"`
	ProjectID        string                  `json:"project_id"`
	Composition      []map[string]any        `json:"composition"`
}

func (a *App) handleWorkerGigJSON(w http.ResponseWriter, _ *http.Request, token string) {
	ctx := globalCtx
	assignID, gigID, pid, status, err := lookupAssignment(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid token")
		return
	}
	_ = assignID

	g, err := loadGig(ctx.AppDB(), pid, gigID)
	if err != nil || g == nil {
		httpErr(w, http.StatusNotFound, "gig not found")
		return
	}

	// Hydrate signed URLs for media kinds.
	ttl := atoi(ctx.Config().Get("signed_url_ttl_seconds"))
	if ttl <= 0 {
		ttl = 3600
	}
	rendered := make([]map[string]any, 0, len(g.Composition))
	for _, it := range g.Composition {
		m := map[string]any{
			"sort_order":       it.SortOrder,
			"instruction_kind": it.InstructionKind,
			"rendered_body":    it.RenderedBody,
			"result_key":       it.ResultKey,
		}
		if isMediaKind(it.InstructionKind) {
			if fid := int64Cast(it.RenderedBody["storage_file_id"]); fid > 0 {
				if url, err := storageSignedURL(ctx, pid, fid, ttl); err == nil {
					m["signed_url"] = url
				}
			}
		}
		rendered = append(rendered, m)
	}

	httpJSON(w, map[string]any{
		"gig": workerGigPayload{
			GigID:            g.ID,
			Title:            g.Title,
			DeadlineAt:       g.DeadlineAt,
			AssignmentStatus: status,
			ProjectID:        pid,
			Composition:      rendered,
		},
	})
}

func (a *App) handleWorkerSubmit(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignID, gigID, pid, status, err := lookupAssignment(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid token")
		return
	}
	if status == "submitted" || status == "withdrawn" {
		httpErr(w, http.StatusGone, "already "+status)
		return
	}
	var body struct {
		Payload     map[string]any `json:"payload"`
		Attachments []int64        `json:"attachment_file_ids,omitempty"`
	}
	if err := httpDecode(r, &body); err != nil || body.Payload == nil {
		httpErr(w, http.StatusBadRequest, "payload required")
		return
	}
	if err := validateSubmission(ctx.AppDB(), gigID, body.Payload); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO gig_submissions (assignment_id, payload_json, attachment_file_ids_json, channel)
		 VALUES (?, ?, ?, 'web')`,
		assignID, mustJSON(body.Payload), mustJSON(body.Attachments),
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(
		`UPDATE gig_assignments SET status='submitted', submitted_at=CURRENT_TIMESTAMP WHERE id=?`,
		assignID,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(
		`UPDATE gigs SET status='submitted', updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		gigID,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
		 VALUES (?, ?, 'submitted', 'worker', 'web')`,
		pid, gigID,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.EmitWithProject("gig.submitted", pid, map[string]any{
		"gig_id":        gigID,
		"assignment_id": assignID,
		"channel":       "web",
	})
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWorkerUpload(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	_, gigID, pid, _, err := lookupAssignment(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid token")
		return
	}
	var body struct {
		Name          string `json:"name"`
		ContentType   string `json:"content_type"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := httpDecode(r, &body); err != nil || body.Name == "" || body.ContentBase64 == "" {
		httpErr(w, http.StatusBadRequest, "name and content_base64 required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.ContentBase64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid base64")
		return
	}
	folder := fmt.Sprintf("submissions/%d", gigID)
	fileID, _, err := storageUpload(ctx, pid, body.Name, folder, body.ContentType, raw)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"storage_file_id": fileID})
}

// ─── Inbound reply handler ──────────────────────────────────────────
//
// Subscribed to crm.contact.message_received. When a worker replies on
// the conversation we opened for their assignment, parse the body and
// (when possible) create a submission. Otherwise reply via CRM with a
// "please open the link" nudge.

func (a *App) handleContactMessageReceived(ctx *sdk.AppCtx, evt sdk.Event) error {
	d := evt.Data
	if d == nil {
		return nil
	}
	pid := evt.ProjectID
	if pid == "" {
		pid = strOf(d["project_id"])
	}
	if pid == "" {
		return nil
	}
	contactID := int64Cast(d["contact_id"])
	convoID := int64Cast(d["conversation_id"])
	body := strOf(d["body"])
	if contactID == 0 || body == "" {
		return nil
	}

	// Find an open assignment for this contact, optionally narrowed
	// to the inbound conversation thread.
	var assignID, gigID int64
	q := `SELECT a.id, a.gig_id
	      FROM gig_assignments a
	      JOIN workers w ON w.id = a.worker_id
	      WHERE w.contact_id=? AND a.status IN ('offered','accepted')`
	args := []any{contactID}
	if convoID > 0 {
		q += ` AND (a.crm_conversation_id=? OR a.crm_conversation_id IS NULL)`
		args = append(args, convoID)
	}
	q += ` ORDER BY a.offered_at DESC LIMIT 1`
	if err := ctx.AppDB().QueryRow(q, args...).Scan(&assignID, &gigID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}

	// Pull the result schema and try to parse the message body.
	var schemaJSON string
	if err := ctx.AppDB().QueryRow(
		`SELECT derived_result_schema_json FROM gigs WHERE id=?`, gigID,
	).Scan(&schemaJSON); err != nil {
		return err
	}
	var schema map[string]any
	_ = parseJSON(schemaJSON, &schema)
	payload, ok := parseReplyToSubmission(schema, body, boolFromConfig(ctx, "lenient_inbound_parsing", true))
	if !ok {
		// Schema needs structured fields we couldn't extract — nudge
		// the worker to open the link.
		var token string
		_ = ctx.AppDB().QueryRow(
			`SELECT magic_token FROM gig_assignments WHERE id=?`, assignID,
		).Scan(&token)
		nudge := "Thanks — to submit this, please open: " + buildWorkerURL(token)
		_, _ = crmSendMessage(ctx, pid, contactID, nudge, "", "")
		return nil
	}
	// Write the submission.
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO gig_submissions (assignment_id, payload_json, channel) VALUES (?, ?, ?)`,
		assignID, mustJSON(payload), "channel_reply",
	); err != nil {
		return err
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE gig_assignments SET status='submitted', submitted_at=CURRENT_TIMESTAMP WHERE id=?`,
		assignID,
	); err != nil {
		return err
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE gigs SET status='submitted', updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		gigID,
	); err != nil {
		return err
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body) VALUES (?, ?, 'submitted', 'worker', 'channel_reply')`,
		pid, gigID,
	); err != nil {
		return err
	}
	ctx.EmitWithProject("gig.submitted", pid, map[string]any{
		"gig_id":        gigID,
		"assignment_id": assignID,
		"channel":       "channel_reply",
	})
	return nil
}

// parseReplyToSubmission converts a free-text inbound message into a
// structured submission when the gig's result schema is simple enough
// for that to make sense — primarily single yes/no or short text gigs.
// Returns ok=false when the schema demands fields we can't infer
// from a one-line reply.
func parseReplyToSubmission(schema map[string]any, body string, lenient bool) (map[string]any, bool) {
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil, false
	}
	// Single-field schemas are the easy case.
	if len(props) == 1 {
		for key, raw := range props {
			fdef, _ := raw.(map[string]any)
			t := strOf(fdef["type"])
			s := strings.TrimSpace(body)
			switch t {
			case "boolean":
				if v, ok := parseYesNo(s, lenient); ok {
					return map[string]any{key: v}, true
				}
			case "string":
				return map[string]any{key: s}, true
			case "number", "integer":
				var n float64
				if _, err := fmt.Sscanf(s, "%f", &n); err == nil {
					return map[string]any{key: n}, true
				}
			}
		}
	}
	return nil, false
}

func parseYesNo(s string, lenient bool) (bool, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	yes := []string{"yes", "y", "yep", "yup", "ok", "okay", "sure", "confirm", "confirmed", "👍", "true"}
	no := []string{"no", "n", "nope", "nah", "cancel", "decline", "false"}
	if !lenient {
		yes = []string{"yes", "y"}
		no = []string{"no", "n"}
	}
	for _, w := range yes {
		if s == w {
			return true, true
		}
	}
	for _, w := range no {
		if s == w {
			return false, true
		}
	}
	return false, false
}

func boolFromConfig(ctx *sdk.AppCtx, key string, def bool) bool {
	v := ctx.Config().Get(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

// ─── Validation ─────────────────────────────────────────────────────

// validateSubmission ensures every required key in the gig's derived
// schema is present in the payload. We do not do full JSON Schema
// validation in v0.1 — required-key coverage is the high-value check.
func validateSubmission(db *sql.DB, gigID int64, payload map[string]any) error {
	var schemaJSON string
	if err := db.QueryRow(
		`SELECT derived_result_schema_json FROM gigs WHERE id=?`, gigID,
	).Scan(&schemaJSON); err != nil {
		return err
	}
	var schema map[string]any
	if err := parseJSON(schemaJSON, &schema); err != nil {
		return err
	}
	requiredAny, _ := schema["required"].([]any)
	for _, r := range requiredAny {
		key := strOf(r)
		if key == "" {
			continue
		}
		v, present := payload[key]
		if !present {
			return fmt.Errorf("missing required field %q", key)
		}
		// Treat empty string and explicit false-y placeholders as
		// missing for required boolean checklist items.
		if s, ok := v.(string); ok && s == "" {
			return fmt.Errorf("field %q cannot be empty", key)
		}
	}
	return nil
}

// ─── Token lookup ───────────────────────────────────────────────────

func lookupAssignment(db *sql.DB, token string) (assignID, gigID int64, projectID, status string, err error) {
	err = db.QueryRow(
		`SELECT a.id, a.gig_id, g.project_id, a.status
		 FROM gig_assignments a
		 JOIN gigs g ON g.id = a.gig_id
		 WHERE a.magic_token=?`,
		token,
	).Scan(&assignID, &gigID, &projectID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.New("not found")
	}
	return
}

// ─── Future: deadline expirer ───────────────────────────────────────
// Reserved hook — once the SDK's Workers slice is wired in main.go we
// can run a periodic sweep to mark stale gigs as 'expired'.
var _ = time.Now
