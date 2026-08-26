package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/yuin/goldmark"
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
	case "accept":
		a.handleWorkerAccept(w, r, token)
	case "decline":
		a.handleWorkerDecline(w, r, token)
	case "submit":
		a.handleWorkerSubmit(w, r, token)
	case "draft":
		a.handleWorkerDraft(w, r, token)
	case "upload/init":
		a.handleWorkerUploadInit(w, r, token)
	case "upload/part":
		a.handleWorkerUploadPart(w, r, token)
	case "upload/complete":
		a.handleWorkerUploadComplete(w, r, token)
	case "upload/abort":
		a.handleWorkerUploadAbort(w, r, token)
	case "upload/remove":
		a.handleWorkerUploadRemove(w, r, token)
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
    --bg: #f6f4ef;
    --surface: #fffdfa;
    --surface-2: #f0ede7;
    --fg: #181512;
    --muted: #756e65;
    --line: #ded8cf;
    --accent: #2455d6;
    --accent-fg: #ffffff;
    --warn: #a15c00;
    --crit: #ad1f2d;
    --ok: #177a47;
    --shadow: 0 18px 50px rgba(44, 34, 22, 0.12);
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0b0b0c;
      --surface: #161618;
      --surface-2: #222226;
      --fg: #f7f3eb;
      --muted: #aaa29a;
      --line: #303036;
      --accent: #5d7ff0;
      --shadow: none;
    }
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; min-height: 100%; background: var(--bg); color: var(--fg); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; }
  body { line-height: 1.45; }
  main { max-width: 940px; margin: 0 auto; padding: 28px 18px 132px; }
  .shell { display: grid; gap: 18px; }
  .hero { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 22px; box-shadow: var(--shadow); }
  h1 { font-size: clamp(28px, 5vw, 44px); margin: 0 0 12px; line-height: 1.05; letter-spacing: 0; }
  .meta-row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .pill { display: inline-flex; align-items: center; min-height: 28px; padding: 5px 10px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: 13px; background: var(--surface-2); }
	  .summary { margin-top: 16px; color: var(--muted); font-size: 14px; }
	  .access-ended { max-width: 620px; margin: 12vh auto 0; padding: 28px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface); box-shadow: var(--shadow); }
	  .access-ended h1 { font-size: 30px; }
	  .access-ended p { color: var(--muted); font-size: 16px; }
  .offer { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 20px; display: grid; gap: 14px; }
  .offer h2 { margin: 0; font-size: 20px; }
  .offer-actions { display: flex; flex-wrap: wrap; gap: 10px; }
  .offer-actions button { min-width: 150px; }
  .section-title { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; margin: 8px 2px 0; }
  .section-title h2 { margin: 0; font-size: 18px; }
  .section-title span { color: var(--muted); font-size: 13px; }
  .instructions { display: grid; gap: 14px; }
  .instr { background: var(--surface); border: 1px solid var(--line); border-radius: 16px; overflow: hidden; box-shadow: 0 8px 28px rgba(32, 26, 20, 0.06); }
  .instr-head { display: flex; gap: 12px; align-items: center; padding: 16px 18px; border-bottom: 1px solid var(--line); background: color-mix(in srgb, var(--surface-2), var(--surface) 45%); }
  .num { width: 36px; height: 36px; border-radius: 11px; border: 1px solid color-mix(in srgb, var(--accent), transparent 68%); background: color-mix(in srgb, var(--accent), transparent 86%); color: var(--accent); display: inline-flex; align-items: center; justify-content: center; font-weight: 750; flex: 0 0 auto; }
  .kind { color: var(--muted); font-size: 12px; text-transform: uppercase; }
  .instr-title { font-size: 16px; font-weight: 650; }
  .instr-body { padding: 18px; display: grid; gap: 14px; }
  .instruction-copy { white-space: pre-wrap; font-size: 16px; line-height: 1.55; }
  .instruction-markdown { font-size: 16px; line-height: 1.6; overflow-wrap: anywhere; }
  .instruction-markdown > :first-child { margin-top: 0; }
  .instruction-markdown > :last-child { margin-bottom: 0; }
  .instruction-markdown h1, .instruction-markdown h2, .instruction-markdown h3, .instruction-markdown h4 { margin: 1.1em 0 0.45em; line-height: 1.2; }
  .instruction-markdown h1 { font-size: 28px; }
  .instruction-markdown h2 { font-size: 23px; }
  .instruction-markdown h3 { font-size: 19px; }
  .instruction-markdown h4 { font-size: 16px; }
  .instruction-markdown p { margin: 0.7em 0; }
  .instruction-markdown ul, .instruction-markdown ol { margin: 0.7em 0; padding-left: 1.6em; }
  .instruction-markdown li + li { margin-top: 0.3em; }
  .instruction-markdown blockquote { margin: 0.8em 0; padding: 0.1em 0 0.1em 1em; border-left: 3px solid var(--line); color: var(--muted); }
  .instruction-markdown code { padding: 0.12em 0.35em; border-radius: 4px; background: var(--surface-2); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.9em; }
  .instruction-markdown pre { overflow-x: auto; padding: 12px; border-radius: 8px; background: var(--surface-2); }
  .instruction-markdown pre code { padding: 0; background: transparent; }
  .instruction-markdown a { color: var(--accent); font-weight: 600; }
  .instruction-markdown hr { border: 0; border-top: 1px solid var(--line); margin: 1.2em 0; }
  .media-frame { border: 1px solid var(--line); background: #050505; border-radius: 8px; padding: 10px; }
  audio, video, img { width: 100%; max-width: 100%; border-radius: 6px; display: block; }
  audio { min-height: 44px; }
  .content-blocks { display: grid; gap: 16px; }
  .content-image { margin: 0; display: grid; gap: 8px; }
  .content-image img { background: #050505; border: 1px solid var(--line); max-height: 720px; object-fit: contain; }
  .content-image figcaption { color: var(--muted); font-size: 13px; line-height: 1.45; }
  .content-callout { border-left: 3px solid var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--surface)); border-radius: 6px; padding: 12px 14px; white-space: pre-wrap; }
  .content-callout.tip { border-left-color: var(--ok); }
  .content-callout.warning { border-left-color: var(--warn); }
  .content-divider { width: 100%; border: 0; border-top: 1px solid var(--line); margin: 2px 0; }
  a.link { color: var(--accent); font-weight: 600; }
  .label { font-size: 12px; color: var(--muted); margin-bottom: 7px; text-transform: uppercase; }
  .text { white-space: pre-wrap; line-height: 1.55; }
  .warning { padding: 12px; border-radius: 8px; background: rgba(161, 92, 0, 0.12); color: var(--warn); }
  .warning.critical { background: rgba(173, 31, 45, 0.12); color: var(--crit); }
  .script { background: var(--surface-2); border-radius: 8px; padding: 12px; font-style: italic; }
  .script p { margin: 4px 0; }
  .check { display: flex; align-items: flex-start; gap: 10px; padding: 8px 0; cursor: pointer; }
  .check input { margin-top: 4px; transform: scale(1.2); }
  .response { border-top: 1px solid var(--line); background: color-mix(in srgb, var(--surface-2), var(--surface) 62%); padding: 18px; display: grid; gap: 14px; }
  .response-title { font-size: 13px; color: var(--muted); font-weight: 650; text-transform: uppercase; }
  .response-grid { display: grid; gap: 10px; }
  input[type=text], input[type=number], input[type=date], textarea, select {
    width: 100%; padding: 10px 12px; font-size: 16px;
    border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--fg);
  }
  textarea { min-height: 92px; resize: vertical; font-family: inherit; }
  .file-row { display: grid; gap: 10px; }
  .file-rules { display: flex; flex-wrap: wrap; gap: 6px; }
  .file-rule { display: inline-flex; align-items: center; min-height: 26px; padding: 4px 9px; border: 1px solid var(--line); border-radius: 999px; background: var(--surface); color: var(--muted); font-size: 12px; }
  .upload-dropzone { min-height: 138px; border: 1.5px dashed color-mix(in srgb, var(--line), var(--muted) 35%); border-radius: 13px; background: color-mix(in srgb, var(--surface), var(--surface-2) 24%); display: grid; place-items: center; padding: 20px; text-align: center; cursor: pointer; transition: border-color 150ms ease, background 150ms ease, transform 150ms ease; }
  .upload-dropzone:hover, .upload-dropzone:focus-within, .upload-dropzone.is-dragging { border-color: var(--accent); background: color-mix(in srgb, var(--accent), var(--surface) 94%); }
  .upload-dropzone.is-dragging { transform: translateY(-1px); }
  .upload-dropzone.hidden { display: none; }
  .file-input { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
  .upload-inner { display: grid; justify-items: center; gap: 7px; }
  .upload-icon { width: 42px; height: 42px; display: grid; place-items: center; border-radius: 12px; background: color-mix(in srgb, var(--accent), transparent 86%); color: var(--accent); }
  .upload-icon svg { width: 22px; height: 22px; }
  .upload-title { color: var(--fg); font-size: 15px; font-weight: 700; }
  .upload-copy { color: var(--muted); font-size: 13px; }
  .upload-action { color: var(--accent); font-weight: 700; }
  .previews { display: grid; gap: 10px; }
  .preview-card { width: 100%; border: 1px solid var(--line); border-radius: 13px; background: var(--surface); overflow: hidden; }
  .preview-top { min-height: 72px; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: 12px; align-items: center; padding: 12px; }
  .preview-kind-icon { width: 44px; height: 44px; display: grid; place-items: center; border-radius: 10px; background: var(--surface-2); color: var(--muted); }
  .preview-kind-icon svg { width: 22px; height: 22px; }
  .preview-details { min-width: 0; display: grid; gap: 5px; }
  .preview-name { overflow: hidden; color: var(--fg); font-size: 14px; font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
  .preview-media { border-top: 1px solid var(--line); background: #050505; }
  .preview-media.collapsed { display: none; }
  .preview-card.video-preview .preview-media video { max-height: 420px; object-fit: contain; background: #050505; }
  .preview-media video, .preview-media audio, .preview-media img { width: 100%; border-radius: 0; }
  .preview-media audio { padding: 10px; background: var(--surface); }
  .preview-file { padding: 14px; color: var(--fg); background: var(--surface-2); word-break: break-word; }
  .preview-status { width: fit-content; color: var(--muted); font-size: 12px; }
  .preview-status.success { color: var(--ok); }
  .preview-progress { height: 4px; background: var(--surface-2); }
  .preview-progress span { display: block; width: 0; height: 100%; background: var(--accent); transition: width 160ms ease; }
  .preview-progress.complete { display: none; }
  .preview-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
  .preview-status.error { color: var(--crit); }
  .preview-action { border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--fg); padding: 7px 10px; font: inherit; font-size: 12px; font-weight: 650; cursor: pointer; }
  .preview-action:hover { background: var(--surface-2); }
  .preview-action.danger { color: var(--crit); }
  .single-input { display: grid; gap: 8px; }
  .uploaded { margin: 0; padding: 0; list-style: none; display: grid; gap: 5px; color: var(--muted); font-size: 13px; }
  .uploaded li { display: flex; justify-content: space-between; gap: 10px; border: 1px solid var(--line); border-radius: 8px; padding: 7px 9px; background: var(--surface); }
  .options label { display: flex; align-items: center; gap: 8px; padding: 6px 0; cursor: pointer; }
  .rating { display: flex; gap: 6px; }
  .rating button { font-size: 22px; padding: 8px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--muted); cursor: pointer; }
  .rating button.on { background: var(--accent); color: var(--accent-fg); border-color: var(--accent); }
  .yn { display: flex; gap: 8px; }
  .yn button { flex: 1; padding: 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--fg); cursor: pointer; font-size: 16px; }
  .yn button.on { background: var(--accent); color: var(--accent-fg); border-color: var(--accent); }
  .submit-bar {
    position: fixed; bottom: 0; left: 0; right: 0;
    background: color-mix(in srgb, var(--bg), transparent 6%); border-top: 1px solid var(--line);
    padding: 12px 16px calc(12px + env(safe-area-inset-bottom)); backdrop-filter: blur(14px);
  }
  .submit-bar .row { max-width: 940px; margin: 0 auto; display: flex; gap: 12px; align-items: center; }
  button.primary {
    background: var(--accent); color: var(--accent-fg); border: none; border-radius: 8px;
    padding: 12px 20px; font-size: 16px; font-weight: 600; cursor: pointer; flex: 1;
  }
  button.primary:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { background: var(--surface); color: var(--fg); border: 1px solid var(--line); border-radius: 8px; padding: 11px 18px; font-size: 15px; font-weight: 600; cursor: pointer; }
  button.danger { color: var(--crit); }
  button.text-button { border: 0; padding: 4px 0; background: transparent; color: var(--crit); cursor: pointer; font: inherit; font-size: 13px; }
  .status { font-size: 13px; color: var(--muted); flex: 0 1 320px; min-height: 18px; }
  .done { max-width: 560px; margin: 12vh auto 0; padding: 32px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface); text-align: center; color: var(--ok); font-size: 20px; box-shadow: var(--shadow); }
  .error { color: var(--crit); }
  @media (max-width: 720px) {
    main { padding: 18px 12px 138px; }
    .hero { padding: 18px; }
    .offer-actions { display: grid; grid-template-columns: 1fr; }
    .offer-actions button { width: 100%; }
    .submit-bar .row { display: grid; }
    .status { min-height: 18px; }
    .preview-top { grid-template-columns: auto minmax(0, 1fr); }
    .preview-actions { grid-column: 1 / -1; justify-content: stretch; }
    .preview-actions button { flex: 1; }
  }
</style>
</head>
<body>
  <main id="app">Loading...</main>
  <script>
	    const TOKEN = ` + jsString(token) + `;
	    const API = window.location.pathname.replace(/\/+$/, "");
	    const result = {};
	    const instructionResponses = {};
	    const allAttachmentIDs = new Set();
	    let gig = null;
	    let pendingUploads = 0;
	    let draftTimer = null;
	    let draftSavePromise = Promise.resolve();

    fetch(API + "/api/gig")
      .then(r => r.json())
	      .then(data => {
	        if (data.error) {
	          if (data.error === "access_ended") {
	            document.getElementById("app").innerHTML = "<section class='access-ended'><h1>This link has ended</h1><p>" + escapeHTML(data.message || "Contact the manager who sent you this link if you still need to submit work.") + "</p></section>";
	          } else {
	            document.getElementById("app").innerHTML = "<div class='done error'>" + escapeHTML(data.error) + "</div>";
	          }
	          return;
        }
	        gig = data.gig;
	        hydrateExistingSubmission(gig.draft || gig.submission);
	        render();
      })
      .catch(e => {
        document.getElementById("app").innerHTML = "<div class='done error'>Could not load this gig: " + escapeHTML(e.message) + "</div>";
      });

    function render() {
      const app = document.getElementById("app");
      document.querySelectorAll(".submit-bar").forEach(el => el.remove());
      app.innerHTML = "<div class='shell'></div>";
      const shell = app.firstElementChild;
      const hero = document.createElement("section");
      hero.className = "hero";
      hero.innerHTML =
	        "<h1>" + escapeHTML(gig.title) + "</h1>" +
	        "<div class='meta-row'>" +
	          (gig.scheduled_for ? "<span class='pill'>Scheduled: " + escapeHTML(formatDeadline(gig.scheduled_for)) + "</span>" : "") +
	          (gig.due_at ? "<span class='pill'>Due: " + escapeHTML(formatDeadline(gig.due_at)) + "</span>" : "") +
	          (gig.overdue ? "<span class='pill'>Overdue — submissions remain open</span>" : "") +
	          (gig.access_expires_at ? "<span class='pill'>Access until: " + escapeHTML(formatDeadline(gig.access_expires_at)) + "</span>" : "") +
          (gig.compensation ? "<span class='pill'>Agreed pay: " + escapeHTML(formatMoney(gig.compensation.worker_amount_minor, gig.compensation.currency)) + "</span>" : "") +
          (gig.assignment_status === "offered"
            ? "<span class='pill'>Full brief after acceptance</span>"
            : "<span class='pill'>" + (gig.composition || []).length + " instruction" + ((gig.composition || []).length === 1 ? "" : "s") + "</span>") +
        "</div>" +
	        "<div class='summary'>" + escapeHTML(summaryText()) + "</div>";
      shell.appendChild(hero);

	      if (gig.assignment_status === "offered") {
	        shell.appendChild(renderOffer());
	        return;
	      }
      if (gig.assignment_status !== "offered" && !canEditAssignment()) {
        const state = document.createElement("section");
        state.className = "done";
        state.textContent = closedStateText();
        shell.appendChild(state);
        return;
      }

      const title = document.createElement("div");
      title.className = "section-title";
      title.innerHTML = "<h2>Instructions</h2><span>Numbered in the order to complete them</span>";
      shell.appendChild(title);

      const list = document.createElement("section");
      list.className = "instructions";
      const items = gig.composition || [];
      for (let i = 0; i < items.length; i++) {
        list.appendChild(renderInstruction(items[i], i));
      }
      shell.appendChild(list);

      if (gig.assignment_status === "offered") return;

      const bar = document.createElement("div");
      bar.className = "submit-bar";
	      bar.innerHTML = '<div class="row"><span class="status" id="status">' + escapeHTML(initialStatusText()) + '</span><button class="primary" id="submit">' + escapeHTML(submitButtonLabel()) + '</button></div>';
      document.body.appendChild(bar);
      document.getElementById("submit").addEventListener("click", submit);
    }

    function renderOffer() {
      const section = document.createElement("section");
      section.className = "offer";
      section.innerHTML = "<h2>Ready to take this gig?</h2><div class='text'>Accept to review the full instructions and submit your work. You can also decline this offer.</div><div class='status' data-offer-status></div><div class='offer-actions'><button class='primary' data-accept>Accept gig</button><button class='secondary danger' data-decline>Decline</button></div>";
      section.querySelector("[data-accept]").addEventListener("click", () => respondToOffer("accept"));
      section.querySelector("[data-decline]").addEventListener("click", () => respondToOffer("decline"));
      return section;
    }

    async function respondToOffer(action) {
      const buttons = Array.from(document.querySelectorAll(".offer-actions button"));
      const status = document.querySelector("[data-offer-status]");
      buttons.forEach(button => button.disabled = true);
      if (status) status.textContent = action === "accept" ? "Accepting..." : "Declining...";
      try {
        const res = await fetch(publicWorkerURL("/" + action), {
          method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
        });
        await responseJSON(res);
        if (action === "accept") {
          const refreshed = await fetch(API + "/api/gig");
          const data = await responseJSON(refreshed);
          gig = data.gig;
          hydrateExistingSubmission(gig.draft || gig.submission);
        } else {
          gig.assignment_status = "declined";
        }
        render();
      } catch (e) {
        buttons.forEach(button => button.disabled = false);
        if (status) status.textContent = e.message;
      }
    }

    function canEditAssignment() {
      return ["accepted", "submitted"].includes(gig.assignment_status) && ["accepted", "submitted"].includes(gig.gig_status);
    }

    function closedStateText() {
      if (gig.assignment_status === "reviewed") return "This submission has been reviewed and accepted.";
      if (gig.assignment_status === "declined") return "You declined this gig.";
      if (gig.gig_status === "cancelled") return "This gig was cancelled.";
      if (gig.gig_status === "expired") return "The deadline for this gig has passed.";
      return "This assignment is no longer open.";
    }

    function renderInstruction(it, index) {
      const card = document.createElement("article");
      card.className = "instr";
      const body = it.rendered_body || {};
      const key = instructionKey(it, index);
      card.innerHTML =
        "<div class='instr-head'>" +
          "<div class='num'>" + (index + 1) + "</div>" +
          "<div><div class='kind'>" + escapeHTML(kindLabel(it.instruction_kind)) + "</div>" +
          "<div class='instr-title'>" + escapeHTML(instructionTitle(it, body, index)) + "</div></div>" +
        "</div>";
      const wrap = document.createElement("div");
      wrap.className = "instr-body";
      switch (it.instruction_kind) {
        case "text":
          wrap.appendChild(markdownBlock(body.markdown_html, body.markdown || ""));
          break;
        case "content":
          wrap.appendChild(renderContentBlocks(body.blocks || []));
          break;
        case "audio":
          appendInstructionCopy(wrap, body);
          if (it.signed_url) wrap.appendChild(mediaBlock('<audio controls src="' + escapeAttr(it.signed_url) + '"></audio>'));
          else wrap.appendChild(mut("Audio unavailable"));
          break;
        case "video":
          appendInstructionCopy(wrap, body);
          if (it.signed_url) wrap.appendChild(mediaBlock('<video controls src="' + escapeAttr(it.signed_url) + '"></video>'));
          else wrap.appendChild(mut("Video unavailable"));
          break;
        case "image":
          appendInstructionCopy(wrap, body);
          if (it.signed_url) wrap.appendChild(mediaBlock('<img alt="' + escapeAttr(body.caption || "") + '" src="' + escapeAttr(it.signed_url) + '" />'));
          break;
        case "document":
          appendInstructionCopy(wrap, body);
          if (it.signed_url) wrap.innerHTML += '<a class="link" href="' + escapeAttr(it.signed_url) + '" target="_blank">Open document</a>';
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
	          cb.checked = result[it.result_key] === true;
	          cb.addEventListener("change", () => { setResult(it.result_key, cb.checked); });
          lbl.appendChild(cb);
          const sp = document.createElement("span");
          sp.textContent = body.text || "";
          lbl.appendChild(sp);
          wrap.appendChild(lbl);
          break;
        }
        case "timer_hint":
          wrap.appendChild(mut("Suggested time: " + (body.seconds_suggested || 0) + "s"));
          break;
        default:
          if (it.instruction_kind.startsWith("input_")) {
            renderInput(wrap, it, body);
          } else {
            wrap.appendChild(mut("Unknown instruction kind: " + it.instruction_kind));
          }
      }
	      card.appendChild(wrap);
	      const spec = responseSpec(it);
	      if (spec.note.enabled || spec.files.enabled) card.appendChild(renderResponseBox(it, index, key, spec));
      return card;
    }

    function appendInstructionCopy(wrap, body) {
      const text = body.caption || body.transcript || body.markdown || body.text || "";
      if (text) wrap.appendChild(textBlock(text));
    }

    function renderContentBlocks(blocks) {
      const container = document.createElement("div");
      container.className = "content-blocks";
      (Array.isArray(blocks) ? blocks : []).forEach(block => {
        if (!block || typeof block !== "object") return;
        switch (String(block.type || "")) {
          case "markdown":
            container.appendChild(markdownBlock(block.markdown_html, block.markdown || ""));
            break;
          case "image": {
            const figure = document.createElement("figure");
            figure.className = "content-image";
            if (block.signed_url) {
              const img = document.createElement("img");
              img.src = block.signed_url;
              img.alt = block.alt || block.caption || "Instruction image";
              img.loading = "lazy";
              figure.appendChild(img);
            } else {
              figure.appendChild(mut("Image unavailable"));
            }
            if (block.caption) {
              const caption = document.createElement("figcaption");
              caption.textContent = block.caption;
              figure.appendChild(caption);
            }
            container.appendChild(figure);
            break;
          }
          case "callout": {
            const callout = document.createElement("div");
            const tone = ["info", "tip", "warning"].includes(block.tone) ? block.tone : "info";
            callout.className = "content-callout " + tone;
            callout.textContent = block.text || "";
            container.appendChild(callout);
            break;
          }
          case "divider": {
            const divider = document.createElement("hr");
            divider.className = "content-divider";
            container.appendChild(divider);
            break;
          }
        }
      });
      return container;
    }

    function textBlock(text) {
      const div = document.createElement("div");
      div.className = "instruction-copy";
      div.textContent = text || "";
      return div;
    }

    function markdownBlock(html, fallback) {
      const div = document.createElement("div");
      div.className = "instruction-markdown";
      if (html) div.innerHTML = html;
      else div.textContent = fallback || "";
      return div;
    }

    function mediaBlock(html) {
      const div = document.createElement("div");
      div.className = "media-frame";
      div.innerHTML = html;
      return div;
    }

    function mut(text) {
      const div = document.createElement("div");
      div.className = "pill";
      div.textContent = text;
      return div;
    }

	    function renderResponseBox(it, index, key, spec) {
	      const box = document.createElement("section");
	      box.className = "response";
	      const requirements = [];
	      if (spec.note.required) requirements.push("note required");
	      if (spec.files.required) requirements.push(spec.files.min_items === 1 ? "1 file required" : spec.files.min_items + " files required");
	      if (spec.legacy_any_required) requirements.push("note or file required");
	      box.innerHTML = "<div class='response-title'>Response for step " + (index + 1) + (requirements.length ? " · " + escapeHTML(requirements.join(" · ")) : " · optional") + "</div><div class='response-grid'></div>";
	      const grid = box.querySelector(".response-grid");
	      let note = null;
	      let file = null;
	      let previews = null;
	      let dropzone = null;
	      let filesInFlight = 0;
	      if (spec.note.enabled) {
	        const noteWrap = document.createElement("label");
	        noteWrap.className = "file-row";
	        noteWrap.innerHTML = "<span class='label'>" + escapeHTML(spec.note.label) + (spec.note.required ? " · required" : " · optional") + "</span><textarea data-note></textarea>";
	        note = noteWrap.querySelector("[data-note]");
	        note.placeholder = spec.note.placeholder;
	        grid.appendChild(noteWrap);
	      }
	      if (spec.files.enabled) {
	        const filesWrap = document.createElement("div");
	        filesWrap.className = "file-row";
	        const noun = uploadFileNoun(spec.files.accept);
	        const ruleParts = [spec.files.accept.length ? uploadAcceptLabel(spec.files.accept) : "Any file type"];
	        if (spec.files.max_items > 0) ruleParts.push(spec.files.max_items + " " + (spec.files.max_items === 1 ? "file" : "files") + " maximum");
	        if (spec.files.max_size_mb > 0) ruleParts.push("Up to " + spec.files.max_size_mb + " MB each");
	        filesWrap.innerHTML = "<div class='label'>Files" + (spec.files.required ? " · required" : " · optional") + "</div>" +
	          "<div class='file-rules'>" + ruleParts.map(rule => "<span class='file-rule'>" + escapeHTML(rule) + "</span>").join("") + "</div>" +
	          "<label class='upload-dropzone' data-dropzone><input class='file-input' data-files type='file' />" +
	          "<span class='upload-inner'><span class='upload-icon' aria-hidden='true'><svg viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'><path d='M12 16V4'/><path d='m7 9 5-5 5 5'/><path d='M5 20h14'/></svg></span>" +
	          "<span class='upload-title'>Drop " + escapeHTML(noun) + " here</span><span class='upload-copy'>or <span class='upload-action'>choose " + escapeHTML(noun) + "</span> from your device</span></span></label>" +
	          "<div class='previews' data-previews></div>";
	        file = filesWrap.querySelector("[data-files]");
	        dropzone = filesWrap.querySelector("[data-dropzone]");
	        previews = filesWrap.querySelector("[data-previews]");
	        if (spec.files.accept.length) file.accept = spec.files.accept.join(",");
	        if (spec.files.max_items !== 1) file.multiple = true;
	        grid.appendChild(filesWrap);
	      }
	      const existing = instructionResponses[key];
	      if (existing) {
	        if (note) note.value = existing.note || "";
	        for (const f of existing.files || []) {
	          if (!previews) continue;
	          const preview = renderSubmittedFilePreview(f, gig.draft ? "Ready to submit" : "Submitted");
	          previews.appendChild(preview.card);
	          preview.addRemove(() => {
	            existing.files = existing.files.filter(item => item !== f);
	            if (f.storage_file_id) allAttachmentIDs.delete(f.storage_file_id);
	            preview.card.remove();
	            pruneInstructionResponse(key);
	            updateStatus();
	            discardUploadedFile(key, f.storage_file_id);
	            updateFilePickerState();
	          });
	        }
	      }
	      if (note) note.addEventListener("input", () => {
	        const entry = ensureInstructionResponse(key, it, index);
	        entry.note = note.value;
	        pruneInstructionResponse(key);
	        updateStatus();
	      });
	      function updateFilePickerState() {
	        if (!dropzone) return;
	        const existingCount = (instructionResponses[key] && instructionResponses[key].files || []).length;
	        const atLimit = spec.files.max_items > 0 && existingCount + filesInFlight >= spec.files.max_items;
	        dropzone.classList.toggle("hidden", atLimit);
	      }
	      async function processSelectedFiles(files) {
	        if (!file || files.length === 0) return;
	        const existingCount = (instructionResponses[key] && instructionResponses[key].files || []).length;
	        if (spec.files.max_items > 0 && existingCount + files.length > spec.files.max_items) {
	          setStatus("Step " + (index + 1) + " accepts at most " + spec.files.max_items + " file(s).");
	          file.value = "";
	          return;
	        }
	        filesInFlight += files.length;
	        updateFilePickerState();
	        for (const f of files) {
	          if (spec.files.max_size_mb > 0 && f.size > spec.files.max_size_mb * 1024 * 1024) {
	            setStatus("Step " + (index + 1) + " accepts files up to " + spec.files.max_size_mb + " MB each.");
	            filesInFlight--;
	            updateFilePickerState();
	            continue;
	          }
	          const preview = renderFilePreview(f, "Uploading...");
	          previews.appendChild(preview.card);
	          setStatus("Uploading " + f.name + "...");
	          let id = null;
	          pendingUploads++;
	          updateSubmitDisabled();
	          try {
	            id = await uploadFile(f, key, percent => preview.setProgress(percent));
	          } catch (e) {
	            preview.setStatus("Upload failed", true);
	            setStatus("Upload failed: " + e.message);
	          } finally {
	            pendingUploads--;
	            filesInFlight--;
	            updateSubmitDisabled();
	            updateFilePickerState();
	          }
	          if (!id) continue;
	          const entry = ensureInstructionResponse(key, it, index);
	          entry.files.push({ storage_file_id: id, filename: f.name, mime: f.type });
	          allAttachmentIDs.add(id);
	          preview.setStatus("Ready to submit");
	          preview.addRemove(() => {
	            entry.files = entry.files.filter(item => item.storage_file_id !== id);
	            allAttachmentIDs.delete(id);
	            preview.card.remove();
	            pruneInstructionResponse(key);
	            updateStatus();
	            discardUploadedFile(key, id);
	            updateFilePickerState();
	          });
	          updateFilePickerState();
	        }
	        file.value = "";
	        updateStatus();
	      }
	      if (file) file.addEventListener("change", () => {
	        processSelectedFiles(Array.from(file.files || []));
	      });
	      if (dropzone) {
	        ["dragenter", "dragover"].forEach(name => dropzone.addEventListener(name, event => {
	          event.preventDefault();
	          dropzone.classList.add("is-dragging");
	        }));
	        ["dragleave", "drop"].forEach(name => dropzone.addEventListener(name, event => {
	          event.preventDefault();
	          dropzone.classList.remove("is-dragging");
	        }));
	        dropzone.addEventListener("drop", event => processSelectedFiles(Array.from(event.dataTransfer && event.dataTransfer.files || [])));
	      }
	      updateFilePickerState();
	      return box;
	    }

	    function uploadFileNoun(accept) {
	      if (accept.length && accept.every(value => value.startsWith("video/"))) return "a video";
	      if (accept.length && accept.every(value => value.startsWith("audio/"))) return "an audio file";
	      if (accept.length && accept.every(value => value.startsWith("image/"))) return "an image";
	      return "files";
	    }
	    function uploadAcceptLabel(accept) {
	      if (accept.length && accept.every(value => value.startsWith("video/"))) return "Video files";
	      if (accept.length && accept.every(value => value.startsWith("audio/"))) return "Audio files";
	      if (accept.length && accept.every(value => value.startsWith("image/"))) return "Images";
	      return accept.join(", ");
	    }

	    function ensureInstructionResponse(key, it, index) {
      if (!instructionResponses[key]) {
        instructionResponses[key] = {
          key: key,
          step: index + 1,
          sort_order: it.sort_order,
          instruction_kind: it.instruction_kind,
          note: "",
          files: [],
        };
      }
	      return instructionResponses[key];
	    }

    function pruneInstructionResponse(key) {
      const entry = instructionResponses[key];
      if (!entry) return;
      if (!entry.note && entry.files.length === 0) delete instructionResponses[key];
    }

	    function renderInput(wrap, it, body) {
	      const k = it.instruction_kind;
	      const key = it.result_key;
	      const existingValue = result[key];
	      const lbl = document.createElement("div");
      lbl.className = "label";
      lbl.textContent = body.label || key;
      wrap.appendChild(lbl);
      let el;
      switch (k) {
	        case "input_short_text":
	          el = document.createElement("input"); el.type = "text"; el.placeholder = body.placeholder || "";
	          if (existingValue != null) el.value = String(existingValue);
	          el.addEventListener("input", () => setResult(key, el.value));
	          break;
	        case "input_long_text":
	          el = document.createElement("textarea"); el.placeholder = body.placeholder || "";
	          if (existingValue != null) el.value = String(existingValue);
	          el.addEventListener("input", () => setResult(key, el.value));
	          break;
	        case "input_number":
	          el = document.createElement("input"); el.type = "number";
	          if (body.min !== undefined) el.min = body.min; if (body.max !== undefined) el.max = body.max;
	          if (existingValue != null) el.value = String(existingValue);
	          el.addEventListener("input", () => setResult(key, el.value === "" ? undefined : parseFloat(el.value)));
	          break;
	        case "input_date":
	          el = document.createElement("input"); el.type = "date";
	          if (existingValue != null) el.value = String(existingValue);
	          el.addEventListener("input", () => setResult(key, el.value));
	          break;
        case "input_choice":
          el = document.createElement("select");
	          el.innerHTML = '<option value="">—</option>' + (body.options || []).map(o => {
            const v = typeof o === "string" ? o : o.value;
            const lab = typeof o === "string" ? o : (o.label || o.value);
	            return '<option value="' + escapeAttr(v) + '">' + escapeHTML(lab) + '</option>';
	          }).join("");
	          if (existingValue != null) el.value = String(existingValue);
	          el.addEventListener("change", () => setResult(key, el.value));
	          break;
        case "input_multi_choice":
          el = document.createElement("div"); el.className = "options";
          (body.options || []).forEach(o => {
            const v = typeof o === "string" ? o : o.value;
            const lab = typeof o === "string" ? o : (o.label || o.value);
            const id = key + "-" + v;
            const wrap2 = document.createElement("label");
	            wrap2.innerHTML = '<input type="checkbox" value="' + escapeAttr(v) + '" id="' + escapeAttr(id) + '" /> ' + escapeHTML(lab);
	            const input = wrap2.querySelector("input");
	            if (Array.isArray(existingValue) && existingValue.includes(v)) input.checked = true;
	            input.addEventListener("change", () => {
	              const ckd = Array.from(el.querySelectorAll("input:checked")).map(i => i.value);
	              setResult(key, ckd);
            });
            el.appendChild(wrap2);
          });
          break;
        case "input_rating":
          el = document.createElement("div"); el.className = "rating";
          const scale = body.scale || 5;
	          for (let i = 1; i <= scale; i++) {
	            const b = document.createElement("button"); b.type = "button"; b.textContent = "★";
	            if (Number(existingValue) >= i) b.classList.add("on");
	            b.addEventListener("click", () => {
              setResult(key, i);
              Array.from(el.children).forEach((c, idx) => c.classList.toggle("on", idx < i));
            });
            el.appendChild(b);
          }
          break;
        case "input_yes_no":
          el = document.createElement("div"); el.className = "yn";
	          ["yes","no"].forEach(v => {
	            const b = document.createElement("button"); b.type = "button"; b.textContent = v.toUpperCase();
	            if (typeof existingValue === "boolean" && existingValue === (v === "yes")) b.classList.add("on");
            b.addEventListener("click", () => {
              setResult(key, v === "yes");
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
          el = document.createElement("div"); el.className = "single-input";
          const fileInput = document.createElement("input"); fileInput.type = "file";
          const previews = document.createElement("div"); previews.className = "previews";
	          if (k === "input_photo") fileInput.accept = "image/*";
	          if (k === "input_audio_recording") fileInput.accept = "audio/*";
	          if (k === "input_video_recording") fileInput.accept = "video/*";
	          if ((k === "input_file" || k === "input_signature") && body.accept_mime) fileInput.accept = String(body.accept_mime);
	          if (existingValue && typeof existingValue === "object") {
	            const preview = renderSubmittedFilePreview(existingValue, gig.draft ? "Ready — not submitted" : "Submitted");
	            previews.appendChild(preview.card);
	            preview.addRemove(() => {
	              delete result[key];
	              if (existingValue.storage_file_id) allAttachmentIDs.delete(existingValue.storage_file_id);
	              preview.card.remove();
	              updateStatus();
	              discardUploadedFile(key, existingValue.storage_file_id);
	            });
	          }
          fileInput.addEventListener("change", async () => {
            const file = fileInput.files && fileInput.files[0];
            if (!file) return;
	          const previousValue = result[key] && typeof result[key] === "object" ? result[key] : null;
            const preview = renderFilePreview(file, "Uploading...");
            previews.appendChild(preview.card);
            setStatus("Uploading " + file.name + "...");
            let id = null;
	          pendingUploads++;
	          updateSubmitDisabled();
            try {
	            id = await uploadFile(file, key, percent => preview.setProgress(percent));
            } catch (e) {
              preview.setStatus("Upload failed", true);
              setStatus("Upload failed: " + e.message);
	          } finally {
	            pendingUploads--;
	            updateSubmitDisabled();
            }
            if (id) {
	            Array.from(previews.children).forEach(child => { if (child !== preview.card) child.remove(); });
              result[key] = { storage_file_id: id, filename: file.name, mime: file.type };
              allAttachmentIDs.add(id);
	            preview.setStatus("Ready — not submitted");
              preview.addRemove(() => {
                if (result[key] && result[key].storage_file_id === id) delete result[key];
                allAttachmentIDs.delete(id);
                preview.card.remove();
                updateStatus();
	              discardUploadedFile(key, id);
              });
	            if (previousValue && previousValue.storage_file_id && previousValue.storage_file_id !== id) {
	              allAttachmentIDs.delete(previousValue.storage_file_id);
	              discardUploadedFile(key, previousValue.storage_file_id);
	            }
	            updateStatus();
            }
          });
          el.appendChild(fileInput);
          el.appendChild(previews);
          break;
        case "input_location":
          el = document.createElement("button"); el.type = "button"; el.className = "primary"; el.textContent = "Use my location";
          el.addEventListener("click", () => {
            navigator.geolocation.getCurrentPosition(p => {
	            setResult(key, { lat: p.coords.latitude, lng: p.coords.longitude, accuracy_m: p.coords.accuracy });
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

	    function setResult(key, value) {
	      if (value === undefined || value === "") delete result[key];
	      else result[key] = value;
	      updateStatus();
	    }

	    async function uploadFile(file, instructionKey, onProgress) {
	      const initRes = await fetch(publicWorkerURL("/upload/init"), {
	        method: "POST",
	        headers: { "Content-Type": "application/json" },
	        body: JSON.stringify({ instruction_key: instructionKey, name: file.name, content_type: file.type, size_bytes: file.size }),
	      });
	      const init = await responseJSON(initRes);
	      if (init.storage_file_id) {
	        if (onProgress) onProgress(100);
	        return init.storage_file_id;
	      }
	      const uploadID = init.upload_id;
	      const partSize = Number(init.part_size) || (1024 * 1024);
	      if (!uploadID) throw new Error("Storage did not start the upload");
	      try {
	        let part = 1;
	        for (let offset = 0; offset < file.size; offset += partSize, part++) {
	          const end = Math.min(offset + partSize, file.size);
	          const bytes = await file.slice(offset, end).arrayBuffer();
	          const partRes = await fetch(publicWorkerURL("/upload/part"), {
	            method: "POST",
	            headers: { "Content-Type": "application/json" },
	            body: JSON.stringify({ upload_id: uploadID, part_number: part, content_base64: arrayBufferToBase64(bytes) }),
	          });
	          await responseJSON(partRes);
	          if (onProgress) onProgress(Math.round((end / file.size) * 100));
	        }
	        const completeRes = await fetch(publicWorkerURL("/upload/complete"), {
	          method: "POST",
	          headers: { "Content-Type": "application/json" },
	          body: JSON.stringify({ upload_id: uploadID }),
	        });
	        const complete = await responseJSON(completeRes);
	        return complete.storage_file_id;
	      } catch (error) {
	        fetch(publicWorkerURL("/upload/abort"), {
	          method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ upload_id: uploadID }),
	        }).catch(() => {});
	        throw error;
	      }
	    }

	    function buildPayload() {
	      const payload = Object.assign({}, result);
	      const instructionPayload = Object.values(instructionResponses)
	        .filter(r => r.note || (r.files && r.files.length))
	        .map(r => ({
	          key: r.key,
	          step: r.step,
	          sort_order: r.sort_order,
	          instruction_kind: r.instruction_kind,
	          note: r.note,
	          files: r.files,
	        }));
	      if (instructionPayload.length) payload.instruction_responses = instructionPayload;
	      return payload;
	    }

	    function scheduleDraftSave() {
	      if (!gig || !canEditAssignment()) return;
	      if (draftTimer) clearTimeout(draftTimer);
	      draftTimer = setTimeout(() => saveDraft(false).catch(() => {}), 450);
	    }

	    async function saveDraft(showStatus) {
	      if (!gig || !canEditAssignment()) return;
	      if (draftTimer) {
	        clearTimeout(draftTimer);
	        draftTimer = null;
	      }
	      const payload = buildPayload();
	      draftSavePromise = draftSavePromise.then(async () => {
	        if (showStatus) setStatus("Saving draft...");
	        const res = await fetch(publicWorkerURL("/draft"), {
	          method: "POST",
	          headers: { "Content-Type": "application/json" },
	          body: JSON.stringify({ payload: payload, attachment_file_ids: currentAttachmentIDs(payload) }),
	        });
	        await responseJSON(res);
	        gig.draft = { payload: payload, attachment_file_ids: currentAttachmentIDs(payload) };
	      });
	      try {
	        await draftSavePromise;
	      } catch (e) {
	        draftSavePromise = Promise.resolve();
	        setStatus("Draft save failed: " + e.message);
	        throw e;
	      }
	    }

	    async function discardUploadedFile(instructionKey, storageFileID) {
	      if (!storageFileID) return;
	      try {
	        const res = await fetch(publicWorkerURL("/upload/remove"), {
	          method: "POST",
	          headers: { "Content-Type": "application/json" },
	          body: JSON.stringify({ instruction_key: instructionKey, storage_file_id: storageFileID }),
	        });
	        await responseJSON(res);
	      } catch (e) {
	        setStatus("File removed from this draft, but storage cleanup failed: " + e.message);
	      }
	    }

	    function updateSubmitDisabled() {
	      const button = document.getElementById("submit");
	      if (button) button.disabled = pendingUploads > 0;
	    }

	    async function submit() {
	      if (pendingUploads > 0) {
	        setStatus("Wait for every file to finish uploading before submitting.");
	        return;
	      }
	      const missing = firstMissingRequiredResponse();
	      if (missing) {
	        setStatus(missing.message + " for step " + missing.step + ".");
	        return;
	      }
	      const payload = buildPayload();
      const button = document.getElementById("submit");
      button.disabled = true;
      setStatus("Submitting...");
	      let j = null;
	      try {
	        await saveDraft(false);
	        const res = await fetch(publicWorkerURL("/submit"), {
	          method: "POST",
	          headers: { "Content-Type": "application/json" },
	          body: JSON.stringify({ payload: payload, attachment_file_ids: currentAttachmentIDs(payload) }),
	        });
	        j = await responseJSON(res);
	      } catch (e) {
	        button.disabled = false;
	        setStatus("Submit failed: " + e.message);
	        return;
	      }
	      if (j.error) {
	        button.disabled = false;
	        setStatus("Submit failed: " + j.error);
	        return;
	      }
		      gig.assignment_status = "submitted";
		      gig.draft = null;
		      button.disabled = false;
		      button.textContent = submitButtonLabel();
		      setStatus(hasWorkInputs() ? "Submission saved. You can keep editing and submit again." : "Completion saved.");
		    }

    function updateStatus() {
      const responses = Object.values(instructionResponses).length;
      const files = currentAttachmentIDs(Object.assign({}, result, { instruction_responses: Object.values(instructionResponses) })).length;
      const parts = [];
      if (responses) parts.push(responses + " step response" + (responses === 1 ? "" : "s"));
      if (files) parts.push(files + " file" + (files === 1 ? "" : "s"));
	      setStatus(parts.length ? parts.join(", ") + " ready — not submitted yet." : initialStatusText());
	      scheduleDraftSave();
	      updateSubmitDisabled();
    }
	    function setStatus(s) {
	      const el = document.getElementById("status");
	      if (el) el.textContent = s;
	    }
	    function hydrateExistingSubmission(submission) {
	      if (!submission || !submission.payload) return;
	      Object.keys(submission.payload).forEach(key => {
	        if (key !== "instruction_responses") result[key] = submission.payload[key];
	      });
	      (submission.attachment_file_ids || []).forEach(id => allAttachmentIDs.add(id));
	      const responses = Array.isArray(submission.payload.instruction_responses) ? submission.payload.instruction_responses : [];
	      responses.forEach((r, idx) => {
	        const key = String(r.key || ("step_" + (r.step || idx + 1)));
	        instructionResponses[key] = {
	          key: key,
	          step: r.step || idx + 1,
	          sort_order: r.sort_order,
	          instruction_kind: r.instruction_kind || "",
	          note: r.note || "",
	          files: Array.isArray(r.files) ? r.files : [],
	        };
	        instructionResponses[key].files.forEach(f => {
	          if (f.storage_file_id) allAttachmentIDs.add(f.storage_file_id);
	        });
	      });
	    }
	    function instructionKey(it, index) { return String(it.result_key || "step_" + (index + 1)); }
	    function kindLabel(kind) { return String(kind || "").replace(/^input_/, "input: ").replace(/_/g, " "); }
	    function instructionTitle(it, body, index) {
	      return it.instruction_name || body.label || body.caption || body.display || body.title || body.text || kindLabel(it.instruction_kind) || ("Instruction " + (index + 1));
	    }
	    function responseSpec(it) {
	      const body = it.rendered_body || {};
	      if (body.response && typeof body.response === "object") {
	        const note = body.response.note || {};
	        const files = body.response.files || {};
	        const noteRequired = note.required === true;
	        const filesRequired = files.required === true;
	        return {
	          note: {
	            enabled: note.enabled === true || noteRequired,
	            required: noteRequired,
	            label: note.label || "Notes for this instruction",
	            placeholder: note.placeholder || "Add a note",
	          },
	          files: {
	            enabled: files.enabled === true || filesRequired,
	            required: filesRequired,
	            accept: Array.isArray(files.accept) ? files.accept : [],
	            min_items: Number(files.min_items || (filesRequired ? 1 : 0)),
	            max_items: Number(files.max_items || 0),
	            max_size_mb: Number(files.max_size_mb || 0),
	          },
	          legacy_any_required: false,
	        };
	      }
	      const mode = String(body.response_mode || "").toLowerCase();
	      if (mode === "optional" || mode === "required") {
	        return {
	          note: { enabled: true, required: false, label: "Notes for this instruction", placeholder: "Add a note" },
	          files: { enabled: true, required: false, accept: [], min_items: 0, max_items: 0, max_size_mb: 0 },
	          legacy_any_required: mode === "required",
	        };
	      }
	      return {
	        note: { enabled: false, required: false },
	        files: { enabled: false, required: false, accept: [], min_items: 0, max_items: 0, max_size_mb: 0 },
	        legacy_any_required: false,
	      };
	    }
	    function hasResponseBoxes() {
	      return (gig.composition || []).some(it => {
	        const spec = responseSpec(it);
	        return spec.note.enabled || spec.files.enabled;
	      });
	    }
	    function hasStructuredFields() {
	      return (gig.composition || []).some(it => {
	        const kind = String(it.instruction_kind || "");
	        return kind.startsWith("input_") || kind === "checklist_item" || kind === "confirmation";
	      });
	    }
	    function hasWorkInputs() {
	      return hasStructuredFields() || hasResponseBoxes();
	    }
	    function submitButtonLabel() {
	      if (gig.assignment_status === "submitted") return hasWorkInputs() ? "Update submission" : "Update completion";
	      return hasWorkInputs() ? "Submit work" : "Mark complete";
	    }
	    function initialStatusText() {
	      if (gig.assignment_status === "submitted") {
	        return hasWorkInputs() ? "Submission saved. Edit anything and resubmit when ready." : "Completion saved.";
	      }
	      return hasWorkInputs() ? "Add the requested responses, then submit." : "Review the instructions, then mark complete.";
	    }
	    function summaryText() {
	      if (gig.assignment_status === "offered") {
	        return "Review the offer details, then accept to see the full instructions.";
	      }
	      if (gig.assignment_status === "submitted") {
	        return hasWorkInputs()
	          ? "Your submission is saved. You can still adjust notes or upload replacement files, then submit again."
	          : "This gig has been marked complete. You can update completion if needed.";
	      }
	      return hasWorkInputs()
	        ? "Review each numbered instruction. Add the requested responses, then submit once at the bottom."
	        : "Review each numbered instruction, then mark the gig complete at the bottom.";
	    }
	    function firstMissingRequiredResponse() {
	      const items = gig.composition || [];
	      for (let i = 0; i < items.length; i++) {
	        const it = items[i];
	        const spec = responseSpec(it);
	        const entry = instructionResponses[instructionKey(it, i)];
	        const note = String(entry && entry.note || "").trim();
	        const files = entry && Array.isArray(entry.files) ? entry.files : [];
	        if (spec.legacy_any_required && !note && files.length === 0) return { step: i + 1, message: "Add a note or file" };
	        if (spec.note.required && !note) return { step: i + 1, message: "Add the required note" };
	        if (spec.files.required && files.length < spec.files.min_items) return { step: i + 1, message: "Upload at least " + spec.files.min_items + " file(s)" };
	        if (files.length > 0 && files.length < spec.files.min_items) return { step: i + 1, message: "Upload at least " + spec.files.min_items + " file(s), or remove the partial upload" };
	      }
	      return null;
	    }
	    function currentAttachmentIDs(value) {
	      const ids = new Set();
	      const visit = item => {
	        if (!item || typeof item !== "object") return;
	        if (Number(item.storage_file_id) > 0) ids.add(Number(item.storage_file_id));
	        if (Array.isArray(item)) item.forEach(visit);
	        else Object.values(item).forEach(visit);
	      };
	      visit(value);
	      return Array.from(ids);
	    }
	    function formatDeadline(s) {
      if (!s) return "No deadline";
      const d = new Date(s);
      if (Number.isNaN(d.getTime())) return s;
	      return d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
	    }
	    function formatMoney(amountMinor, currency) {
	      const amount = Number(amountMinor || 0) / 100;
	      try { return new Intl.NumberFormat([], { style: "currency", currency: currency || "USD" }).format(amount); }
	      catch (_) { return (currency || "") + " " + amount.toFixed(2); }
	    }
	    function publicWorkerURL(path) {
	      const exp = Math.floor(Date.now() / 1000) + 86400;
	      return API + path + "?sig=" + encodeURIComponent(TOKEN) + "&exp=" + exp;
	    }
	    function renderFilePreview(file, status) {
	      const card = document.createElement("article");
	      card.className = "preview-card";
	      const media = document.createElement("div");
	      media.className = "preview-media";
	      const url = URL.createObjectURL(file);
	      let node = null;
	      if (file.type.startsWith("video/")) {
	        card.classList.add("video-preview");
	        media.classList.add("collapsed");
	        node = document.createElement("video");
	        node.controls = true;
	        node.preload = "metadata";
	        node.playsInline = true;
	        node.src = url;
	      } else if (file.type.startsWith("audio/")) {
	        node = document.createElement("audio");
	        node.controls = true;
	        node.preload = "metadata";
	        node.src = url;
	      } else if (file.type.startsWith("image/")) {
	        node = document.createElement("img");
	        node.alt = file.name;
	        node.src = url;
	      }
	      if (node) media.appendChild(node);
	      const header = appendPreviewHeader(card, file.name, file.type, status || "Selected");
	      const progress = document.createElement("div");
	      progress.className = "preview-progress";
	      progress.innerHTML = "<span></span>";
	      card.appendChild(progress);
	      if (node) card.appendChild(media);
	      if (file.type.startsWith("video/")) {
	        header.actions.appendChild(videoPreviewToggle(media));
	      }
	      return {
	        card,
	        setStatus(text, isError) {
	          header.setStatus(text, isError);
	          progress.classList.toggle("complete", !isError && (text === "Ready to submit" || text === "Submitted"));
	        },
	        setProgress(percent) {
	          progress.firstElementChild.style.width = Math.max(0, Math.min(100, percent)) + "%";
	        },
	        addRemove(handler) { addPreviewRemove(card, handler); },
	      };
	    }
	    function renderSubmittedFilePreview(file, status) {
	      const card = document.createElement("article");
	      card.className = "preview-card";
	      const media = document.createElement("div");
	      media.className = "preview-media";
	      const url = file.signed_url || "";
	      const mime = file.mime || file.content_type || "";
	      let node = null;
	      if (url && mime.startsWith("video/")) {
	        card.classList.add("video-preview");
	        media.classList.add("collapsed");
	        node = document.createElement("video");
	        node.controls = true;
	        node.preload = "metadata";
	        node.playsInline = true;
	        node.src = url;
	      } else if (url && mime.startsWith("audio/")) {
	        node = document.createElement("audio");
	        node.controls = true;
	        node.preload = "metadata";
	        node.src = url;
	      } else if (url && mime.startsWith("image/")) {
	        node = document.createElement("img");
	        node.alt = file.filename || "Submitted file";
	        node.src = url;
	      } else {
	        node = document.createElement(url ? "a" : "div");
	        node.className = "preview-file";
	        node.textContent = file.filename || ("Storage file #" + (file.storage_file_id || ""));
	        if (url) {
	          node.href = url;
	          node.target = "_blank";
	        node.rel = "noreferrer";
	        }
	      }
	      media.appendChild(node);
	      const header = appendPreviewHeader(card, file.filename || ("Storage file #" + (file.storage_file_id || "")), mime, status || "Submitted");
	      card.appendChild(media);
	      if (url && mime.startsWith("video/")) {
	        header.actions.appendChild(videoPreviewToggle(media));
	      }
	      return { card, setStatus(text) { header.setStatus(text, false); }, addRemove(handler) { addPreviewRemove(card, handler); } };
	    }
	    function appendPreviewHeader(card, filename, mime, status) {
	      const top = document.createElement("div");
	      top.className = "preview-top";
	      const icon = document.createElement("div");
	      icon.className = "preview-kind-icon";
	      icon.setAttribute("aria-hidden", "true");
	      icon.innerHTML = previewFileIcon(mime);
	      const details = document.createElement("div");
	      details.className = "preview-details";
	      const name = document.createElement("div");
	      name.className = "preview-name";
	      name.title = filename;
	      name.textContent = filename;
	      const state = document.createElement("div");
	      state.className = "preview-status";
	      details.appendChild(name);
	      details.appendChild(state);
	      const actions = document.createElement("div");
	      actions.className = "preview-actions";
	      top.appendChild(icon);
	      top.appendChild(details);
	      top.appendChild(actions);
	      card.appendChild(top);
	      const setStatus = (text, isError) => {
	        state.textContent = text;
	        state.classList.toggle("error", Boolean(isError));
	        state.classList.toggle("success", !isError && (text === "Ready to submit" || text === "Submitted"));
	      };
	      setStatus(status, false);
	      return { actions, setStatus };
	    }
	    function previewFileIcon(mime) {
	      if (String(mime || "").startsWith("video/")) {
	        return "<svg viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'><rect x='3' y='5' width='14' height='14' rx='3'/><path d='m17 10 4-2v8l-4-2'/></svg>";
	      }
	      if (String(mime || "").startsWith("image/")) {
	        return "<svg viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'><rect x='3' y='4' width='18' height='16' rx='3'/><circle cx='9' cy='10' r='2'/><path d='m4 17 5-5 4 4 2-2 5 5'/></svg>";
	      }
	      if (String(mime || "").startsWith("audio/")) {
	        return "<svg viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'><path d='M9 18V5l10-2v13'/><circle cx='6' cy='18' r='3'/><circle cx='16' cy='16' r='3'/></svg>";
	      }
	      return "<svg viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'><path d='M6 3h8l4 4v14H6z'/><path d='M14 3v5h5'/></svg>";
	    }
	    function addPreviewRemove(card, handler) {
	      let actions = card.querySelector(".preview-actions");
	      if (!actions) {
	        actions = document.createElement("div");
	        actions.className = "preview-actions";
	        card.appendChild(actions);
	      }
	      const button = document.createElement("button");
	      button.type = "button";
	      button.className = "preview-action danger";
	      button.textContent = "Remove";
	      button.addEventListener("click", handler);
	      actions.appendChild(button);
	    }
	    function videoPreviewToggle(media) {
	      const button = document.createElement("button");
	      button.type = "button";
	      button.className = "preview-action";
	      button.textContent = "Preview";
	      button.addEventListener("click", () => {
	        const hidden = media.classList.toggle("collapsed");
	        button.textContent = hidden ? "Preview" : "Hide preview";
	      });
	      return button;
	    }
	    async function responseJSON(res) {
	      const text = await res.text();
	      let json = {};
	      if (text) {
	        try {
	          json = JSON.parse(text);
	        } catch (_) {
	          json = { error: text };
	        }
	      }
	      if (!res.ok) {
	        throw new Error(json.error || ("HTTP " + res.status));
	      }
	      return json;
	    }
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
	GigID              int64            `json:"gig_id"`
	Title              string           `json:"title"`
	ScheduledFor       string           `json:"scheduled_for,omitempty"`
	DueAt              string           `json:"due_at,omitempty"`
	DeadlineAt         string           `json:"deadline_at,omitempty"`
	Overdue            bool             `json:"overdue"`
	AccessExpiresAt    string           `json:"access_expires_at,omitempty"`
	GigStatus          string           `json:"gig_status"`
	AssignmentStatus   string           `json:"assignment_status"`
	AssignmentMode     string           `json:"assignment_mode"`
	ProjectID          string           `json:"project_id"`
	Composition        []map[string]any `json:"composition"`
	RequiredResultKeys []string         `json:"required_result_keys,omitempty"`
	Submission         *submission      `json:"submission,omitempty"`
	Draft              *workerDraft     `json:"draft,omitempty"`
	Compensation       *gigCompensation `json:"compensation,omitempty"`
}

func (a *App) handleWorkerGigJSON(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	ctx := globalCtx
	assignID, gigID, pid, status, gigStatus, mode, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid token")
		return
	}
	if revoked || accessExpired {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":   "access_ended",
			"message": "Access to this gig has ended. Contact the manager who sent you this link if you still need to submit work.",
		})
		return
	}

	g, err := loadGig(ctx, pid, gigID)
	if err != nil || g == nil {
		httpErr(w, http.StatusNotFound, "gig not found")
		return
	}

	// The offer page deliberately withholds the work composition until the
	// worker accepts. Compensation and deadline remain visible so they can make
	// that decision with the agreed terms in front of them.
	ttl := atoi(ctx.Config().Get("signed_url_ttl_seconds"))
	if ttl <= 0 {
		ttl = 3600
	}
	rendered := make([]map[string]any, 0, len(g.Composition))
	composition := g.Composition
	if status == "offered" {
		composition = nil
	}
	for _, it := range composition {
		body := it.RenderedBody
		if it.InstructionKind == kindText {
			body = enrichMarkdownBody(body)
		} else if it.InstructionKind == kindContent {
			body = enrichContentBlockURLs(ctx, pid, body, ttl)
		}
		m := map[string]any{
			"sort_order":       it.SortOrder,
			"instruction_kind": it.InstructionKind,
			"instruction_name": it.InstructionName,
			"rendered_body":    body,
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
	submission, err := loadLatestSubmissionForAssignment(ctx.AppDB(), assignID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if submission != nil {
		enrichSubmissionFileURLs(ctx, pid, submission.Payload, ttl)
	}
	draft, err := loadWorkerDraft(ctx.AppDB(), assignID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if draft != nil {
		enrichSubmissionFileURLs(ctx, pid, draft.Payload, ttl)
	}

	httpJSON(w, map[string]any{
		"gig": workerGigPayload{
			GigID:            g.ID,
			Title:            g.Title,
			ScheduledFor:     g.ScheduledFor,
			DueAt:            g.DueAt,
			DeadlineAt:       g.DeadlineAt,
			Overdue:          g.Overdue,
			AccessExpiresAt:  g.assignmentAccessExpiry(assignID),
			GigStatus:        gigStatus,
			AssignmentStatus: status,
			AssignmentMode:   mode,
			ProjectID:        pid,
			Composition:      rendered,
			RequiredResultKeys: func() []string {
				if status == "offered" {
					return nil
				}
				return requiredResultKeys(g.DerivedResultSchema)
			}(),
			Submission:   submission,
			Draft:        draft,
			Compensation: g.Compensation,
		},
	})
}

func enrichContentBlockURLs(ctx *sdk.AppCtx, pid string, body map[string]any, ttl int) map[string]any {
	out := make(map[string]any, len(body))
	for key, value := range body {
		out[key] = value
	}
	blocks, ok := body["blocks"].([]any)
	if !ok {
		return out
	}
	enriched := make([]any, len(blocks))
	for i, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			enriched[i] = raw
			continue
		}
		copy := make(map[string]any, len(block)+1)
		for key, value := range block {
			copy[key] = value
		}
		switch strOf(copy["type"]) {
		case "markdown":
			if markdown := strOf(copy["markdown"]); markdown != "" {
				copy["markdown_html"] = renderWorkerMarkdown(markdown)
			}
		case "image":
			if fid := int64Cast(copy["storage_file_id"]); fid > 0 {
				if url, err := storageSignedURL(ctx, pid, fid, ttl); err == nil {
					copy["signed_url"] = url
				}
			}
		}
		enriched[i] = copy
	}
	out["blocks"] = enriched
	return out
}

var workerMarkdown = goldmark.New()

func renderWorkerMarkdown(source string) string {
	var out bytes.Buffer
	if err := workerMarkdown.Convert([]byte(source), &out); err != nil {
		return ""
	}
	return out.String()
}

func enrichMarkdownBody(body map[string]any) map[string]any {
	out := make(map[string]any, len(body)+1)
	for key, value := range body {
		out[key] = value
	}
	if markdown := strOf(out["markdown"]); markdown != "" {
		out["markdown_html"] = renderWorkerMarkdown(markdown)
	}
	return out
}

func loadLatestSubmissionForAssignment(db *sql.DB, assignmentID int64) (*submission, error) {
	var payloadJSON, attachmentIDsJSON string
	sub := &submission{}
	err := db.QueryRow(
		`SELECT id, assignment_id, payload_json,
		        COALESCE(attachment_file_ids_json, '[]'),
		        COALESCE(channel, ''), COALESCE(submitted_at, '')
		   FROM gig_submissions
		  WHERE assignment_id = ?
		  ORDER BY id DESC
		  LIMIT 1`,
		assignmentID,
	).Scan(&sub.ID, &sub.AssignmentID, &payloadJSON, &attachmentIDsJSON, &sub.Channel, &sub.SubmittedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = parseJSON(payloadJSON, &sub.Payload)
	_ = parseJSON(attachmentIDsJSON, &sub.AttachmentFileIDs)
	if sub.Payload == nil {
		sub.Payload = map[string]any{}
	}
	return sub, nil
}

func enrichSubmissionFileURLs(ctx *sdk.AppCtx, pid string, value any, ttl int) {
	switch v := value.(type) {
	case map[string]any:
		if fid := int64Cast(v["storage_file_id"]); fid > 0 {
			if url, err := storageSignedURL(ctx, pid, fid, ttl); err == nil {
				v["signed_url"] = url
			}
		}
		for _, child := range v {
			enrichSubmissionFileURLs(ctx, pid, child, ttl)
		}
	case []any:
		for _, child := range v {
			enrichSubmissionFileURLs(ctx, pid, child, ttl)
		}
	}
}

func (a *App) handleWorkerAccept(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, gigID, pid, assignmentStatus, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if revoked || accessExpired {
		httpErr(w, http.StatusGone, "access to this gig has ended; contact the manager who sent the link")
		return
	}
	if assignmentStatus != "offered" || (gigStatus != "offered" && gigStatus != "accepted") {
		httpErr(w, http.StatusConflict, "this offer can no longer be accepted")
		return
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE gig_assignments
		SET status='accepted', responded_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='offered' AND token_revoked_at IS NULL`, assignmentID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		httpErr(w, http.StatusConflict, "this offer changed; reload the page")
		return
	}
	if _, err = tx.Exec(`UPDATE gigs SET status='accepted', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND project_id=? AND status IN ('offered','accepted')`, gigID, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err = tx.Exec(`INSERT INTO gig_events (project_id,gig_id,kind,actor,body)
		VALUES (?,?,'accepted_by_worker','worker','web')`, pid, gigID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.EmitWithProject("gig.accepted", pid, map[string]any{"gig_id": gigID, "assignment_id": assignmentID})
	httpJSON(w, map[string]any{"ok": true, "assignment_status": "accepted"})
}

func (a *App) handleWorkerDecline(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, gigID, pid, assignmentStatus, _, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if revoked || accessExpired {
		httpErr(w, http.StatusGone, "access to this gig has ended; contact the manager who sent the link")
		return
	}
	if assignmentStatus != "offered" && assignmentStatus != "accepted" {
		httpErr(w, http.StatusConflict, "this offer can no longer be declined")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := httpDecode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE gig_assignments
		SET status='declined', responded_at=CURRENT_TIMESTAMP, token_revoked_at=CURRENT_TIMESTAMP
		WHERE id=? AND status IN ('offered','accepted') AND token_revoked_at IS NULL`, assignmentID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		httpErr(w, http.StatusConflict, "this offer changed; reload the page")
		return
	}
	if _, err = tx.Exec(`UPDATE gigs SET status=CASE
		WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='accepted') THEN 'accepted'
		WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='offered') THEN 'offered'
		ELSE 'open' END, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, gigID, gigID, gigID, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err = tx.Exec(`INSERT INTO gig_events (project_id,gig_id,kind,actor,body)
		VALUES (?,?,'declined','worker',?)`, pid, gigID, strings.TrimSpace(body.Reason)); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.EmitWithProject("gig.declined", pid, map[string]any{"gig_id": gigID, "assignment_id": assignmentID})
	httpJSON(w, map[string]any{"ok": true, "assignment_status": "declined"})
}

func (a *App) handleWorkerDraft(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignID, gigID, _, status, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if !assignmentAcceptsWork(status, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
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
	if err := validateSubmissionAttachments(ctx.AppDB(), assignID, body.Attachments); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateInstructionResponses(ctx.AppDB(), gigID, assignID, body.Payload, false); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	draft, err := saveWorkerDraft(ctx.AppDB(), assignID, body.Payload, body.Attachments)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true, "draft": draft})
}

func (a *App) handleWorkerSubmit(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignID, gigID, pid, status, gigStatus, mode, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if !assignmentAcceptsWork(status, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
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
	if err := validateSubmission(ctx.AppDB(), gigID, assignID, body.Payload); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateSubmissionAttachments(ctx.AppDB(), assignID, body.Attachments); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	markedOverdue, err := markLateSubmissionOverdueTx(tx, pid, gigID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mode == "first-come" {
		var other int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM gig_submissions s
			JOIN gig_assignments a ON a.id=s.assignment_id
			WHERE a.gig_id=? AND a.id<>?`, gigID, assignID).Scan(&other); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if other > 0 {
			httpErr(w, http.StatusConflict, "this first-come gig was already submitted by another worker")
			return
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_submissions (assignment_id, payload_json, attachment_file_ids_json, channel)
		 VALUES (?, ?, ?, 'web')`,
		assignID, mustJSON(body.Payload), mustJSON(body.Attachments),
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(`DELETE FROM gig_assignment_drafts WHERE assignment_id=?`, assignID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := tx.Exec(`UPDATE gig_assignments SET status='submitted', responded_at=COALESCE(responded_at,CURRENT_TIMESTAMP),
		submitted_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('accepted','submitted')
		AND token_revoked_at IS NULL`, assignID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		httpErr(w, http.StatusConflict, "this assignment changed; reload the page")
		return
	}
	if _, err := tx.Exec(
		`UPDATE gigs SET status='submitted',
		 overdue_at=CASE WHEN due_at IS NOT NULL AND datetime(due_at)<datetime('now') THEN COALESCE(overdue_at,CURRENT_TIMESTAMP) ELSE overdue_at END,
		 updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		gigID,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mode == "first-come" {
		if _, err := tx.Exec(`UPDATE gig_assignments SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
			WHERE gig_id=? AND id<>? AND status IN ('offered','accepted')`, gigID, assignID); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
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
	if err := syncContractFromGig(ctx.AppDB(), pid, gigID, "submitted"); err != nil {
		ctx.Logger().Warn("sync contract milestone after web submission failed", "gig_id", gigID, "err", err.Error())
	}
	ctx.EmitWithProject("gig.submitted", pid, map[string]any{
		"gig_id":        gigID,
		"assignment_id": assignID,
		"channel":       "web",
	})
	if markedOverdue {
		ctx.EmitWithProject("gig.overdue", pid, map[string]any{"gig_id": gigID, "reason": "late_submission"})
	}
	httpJSON(w, map[string]any{"ok": true})
}

func markLateSubmissionOverdueTx(tx *sql.Tx, pid string, gigID int64) (bool, error) {
	var shouldMark int
	if err := tx.QueryRow(`SELECT CASE WHEN due_at IS NOT NULL AND overdue_at IS NULL
		AND datetime(due_at)<datetime('now') THEN 1 ELSE 0 END FROM gigs WHERE id=?`, gigID).Scan(&shouldMark); err != nil {
		return false, err
	}
	if shouldMark == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE gigs SET overdue_at=CURRENT_TIMESTAMP WHERE id=? AND overdue_at IS NULL`, gigID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO gig_events(project_id,gig_id,kind,actor,body)
		VALUES (?,?,'overdue','system','submitted after soft due date')`, pid, gigID); err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) handleWorkerUploadInit(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, gigID, pid, assignmentStatus, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if !assignmentAcceptsWork(assignmentStatus, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
		return
	}
	var body struct {
		InstructionKey string `json:"instruction_key"`
		Name           string `json:"name"`
		ContentType    string `json:"content_type"`
		SizeBytes      int64  `json:"size_bytes"`
	}
	if err := httpDecode(r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	body.InstructionKey = strings.TrimSpace(body.InstructionKey)
	if strings.TrimSpace(body.Name) == "" || body.SizeBytes <= 0 || body.InstructionKey == "" {
		httpErr(w, http.StatusBadRequest, "instruction_key, name and positive size_bytes required")
		return
	}
	requirement, err := loadGigFileRequirement(ctx.AppDB(), gigID, body.InstructionKey)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if requirement == nil {
		httpErr(w, http.StatusBadRequest, "this instruction does not accept files")
		return
	}
	if err := responseAcceptsFile(requirement.Spec.Files, body.Name, body.ContentType, body.SizeBytes); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	maxBytes := int64(atoi(ctx.Config().Get("max_submission_file_mb"))) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024 * 1024
	}
	if body.SizeBytes > maxBytes {
		httpErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d MB limit", maxBytes/(1024*1024)))
		return
	}
	folder := fmt.Sprintf("submissions/%d", gigID)
	init, err := storageUploadInit(ctx, pid, body.Name, folder, body.ContentType, body.SizeBytes)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if init.WasExisting {
		fileID := int64Cast(init.File["id"])
		if fileID == 0 {
			httpErr(w, http.StatusBadGateway, "storage returned an invalid existing file")
			return
		}
		metadata, metadataErr := storageGetFile(ctx, pid, fileID)
		if metadataErr != nil {
			httpErr(w, http.StatusBadGateway, metadataErr.Error())
			return
		}
		if err := responseAcceptsFile(requirement.Spec.Files, metadata.Name, metadata.ContentType, metadata.SizeBytes); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := ctx.AppDB().Exec(`INSERT OR REPLACE INTO gig_upload_sessions
			(upload_id,assignment_id,project_id,status,storage_file_id,instruction_key,
			 filename,content_type,size_bytes,was_existing,completed_at)
			VALUES (?,?,?,?,?,?,?,?,?,1,CURRENT_TIMESTAMP)`, "existing:"+token+":"+body.InstructionKey+":"+fmt.Sprint(fileID), assignmentID, pid, "completed", fileID,
			body.InstructionKey, metadata.Name, metadata.ContentType, metadata.SizeBytes); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"was_existing": true, "storage_file_id": fileID})
		return
	}
	if init.UploadID == "" || init.PartSize <= 0 {
		httpErr(w, http.StatusBadGateway, "storage returned an invalid upload session")
		return
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions
		(upload_id,assignment_id,project_id,status,instruction_key,filename,content_type,size_bytes)
		VALUES (?,?,?,'uploading',?,?,?,?)`, init.UploadID, assignmentID, pid,
		body.InstructionKey, body.Name, body.ContentType, body.SizeBytes); err != nil {
		_ = storageUploadAbort(ctx, pid, init.UploadID, "gigs session registration failed")
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"upload_id": init.UploadID, "part_size": init.PartSize, "was_existing": false})
}

func (a *App) handleWorkerUploadPart(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, _, pid, assignmentStatus, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil || !assignmentAcceptsWork(assignmentStatus, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
		return
	}
	var body struct {
		UploadID      string `json:"upload_id"`
		PartNumber    int    `json:"part_number"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := httpDecodeLimit(r, &body, 2*1024*1024); err != nil || body.UploadID == "" || body.PartNumber < 1 || body.ContentBase64 == "" {
		httpErr(w, http.StatusBadRequest, "upload_id, part_number and content_base64 required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.ContentBase64)
	if err != nil || len(raw) == 0 || len(raw) > 1024*1024 {
		httpErr(w, http.StatusBadRequest, "invalid upload part")
		return
	}
	if err := requireWorkerUploadSession(ctx.AppDB(), body.UploadID, assignmentID, pid, "uploading"); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := storageUploadPart(ctx, pid, body.UploadID, body.PartNumber, body.ContentBase64); err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true, "part_number": body.PartNumber})
}

func (a *App) handleWorkerUploadComplete(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, gigID, pid, assignmentStatus, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil || !assignmentAcceptsWork(assignmentStatus, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
		return
	}
	var body struct {
		UploadID string `json:"upload_id"`
	}
	if err := httpDecode(r, &body); err != nil || body.UploadID == "" {
		httpErr(w, http.StatusBadRequest, "upload_id required")
		return
	}
	if err := requireWorkerUploadSession(ctx.AppDB(), body.UploadID, assignmentID, pid, "uploading"); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	fileID, err := storageUploadComplete(ctx, pid, body.UploadID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var instructionKey, filename, contentType string
	var sizeBytes int64
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(instruction_key,''),COALESCE(filename,''),COALESCE(content_type,''),COALESCE(size_bytes,0)
		FROM gig_upload_sessions WHERE upload_id=? AND assignment_id=?`, body.UploadID, assignmentID).
		Scan(&instructionKey, &filename, &contentType, &sizeBytes); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	metadata, metadataErr := storageGetFile(ctx, pid, fileID)
	if metadataErr != nil {
		_ = storageDeleteFile(ctx, pid, fileID)
		_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='discarded',storage_file_id=?,discarded_at=CURRENT_TIMESTAMP WHERE upload_id=?`, fileID, body.UploadID)
		httpErr(w, http.StatusBadGateway, metadataErr.Error())
		return
	}
	filename, contentType, sizeBytes = metadata.Name, metadata.ContentType, metadata.SizeBytes
	requirement, err := loadGigFileRequirement(ctx.AppDB(), gigID, instructionKey)
	if err != nil || requirement == nil {
		_ = storageDeleteFile(ctx, pid, fileID)
		_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='discarded',storage_file_id=?,discarded_at=CURRENT_TIMESTAMP WHERE upload_id=?`, fileID, body.UploadID)
		httpErr(w, http.StatusBadRequest, "this instruction no longer accepts files")
		return
	}
	if err := responseAcceptsFile(requirement.Spec.Files, filename, contentType, sizeBytes); err != nil {
		_ = storageDeleteFile(ctx, pid, fileID)
		_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='discarded',storage_file_id=?,discarded_at=CURRENT_TIMESTAMP WHERE upload_id=?`, fileID, body.UploadID)
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='completed', storage_file_id=?,filename=?,content_type=?,size_bytes=?,completed_at=CURRENT_TIMESTAMP
		WHERE upload_id=? AND assignment_id=? AND status='uploading'`, fileID, filename, contentType, sizeBytes, body.UploadID, assignmentID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		httpErr(w, http.StatusConflict, "upload session changed")
		return
	}
	httpJSON(w, map[string]any{"ok": true, "storage_file_id": fileID})
}

func (a *App) handleWorkerUploadAbort(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, _, pid, _, _, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if revoked || accessExpired {
		httpErr(w, http.StatusGone, "this assignment is closed")
		return
	}
	var body struct {
		UploadID string `json:"upload_id"`
	}
	if err := httpDecode(r, &body); err != nil || body.UploadID == "" {
		httpErr(w, http.StatusBadRequest, "upload_id required")
		return
	}
	if err := requireWorkerUploadSession(ctx.AppDB(), body.UploadID, assignmentID, pid, "uploading"); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := storageUploadAbort(ctx, pid, body.UploadID, "worker cancelled"); err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='aborted' WHERE upload_id=? AND assignment_id=?`, body.UploadID, assignmentID)
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWorkerUploadRemove(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := globalCtx
	assignmentID, _, pid, assignmentStatus, gigStatus, _, revoked, accessExpired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusNotFound, "invalid or expired link")
		return
	}
	if !assignmentAcceptsWork(assignmentStatus, gigStatus, revoked, accessExpired) {
		httpErr(w, http.StatusGone, "this assignment is closed")
		return
	}
	var body struct {
		InstructionKey string `json:"instruction_key"`
		StorageFileID  int64  `json:"storage_file_id"`
	}
	if err := httpDecode(r, &body); err != nil || body.StorageFileID <= 0 || strings.TrimSpace(body.InstructionKey) == "" {
		httpErr(w, http.StatusBadRequest, "instruction_key and storage_file_id required")
		return
	}
	var storedKey string
	var wasExisting bool
	err = ctx.AppDB().QueryRow(`SELECT COALESCE(instruction_key,''),COALESCE(was_existing,0)
		FROM gig_upload_sessions WHERE assignment_id=? AND storage_file_id=? AND status='completed'
		ORDER BY completed_at DESC LIMIT 1`, assignmentID, body.StorageFileID).Scan(&storedKey, &wasExisting)
	if errors.Is(err, sql.ErrNoRows) {
		// A previous release may not have retained an instruction-scoped
		// upload session. The worker can still remove it from the new draft;
		// historical submission data remains immutable.
		httpJSON(w, map[string]any{"ok": true, "deleted": false, "retained_for_history": true})
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if storedKey != "" && storedKey != strings.TrimSpace(body.InstructionKey) {
		httpErr(w, http.StatusBadRequest, "file belongs to a different instruction")
		return
	}
	referenced, err := submissionReferencesFile(ctx.AppDB(), assignmentID, body.StorageFileID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if referenced || wasExisting {
		httpJSON(w, map[string]any{"ok": true, "deleted": false, "retained_for_history": true})
		return
	}
	res, err := ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='discarded',discarded_at=CURRENT_TIMESTAMP
		WHERE assignment_id=? AND storage_file_id=? AND status='completed'`, assignmentID, body.StorageFileID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpErr(w, http.StatusConflict, "file changed; reload the page")
		return
	}
	if err := storageDeleteFile(ctx, pid, body.StorageFileID); err != nil {
		ctx.Logger().Warn("delete discarded worker upload failed", "file_id", body.StorageFileID, "err", err.Error())
		httpJSON(w, map[string]any{"ok": true, "deleted": false, "discarded": true})
		return
	}
	httpJSON(w, map[string]any{"ok": true, "deleted": true})
}

func requireWorkerUploadSession(db *sql.DB, uploadID string, assignmentID int64, pid, status string) error {
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM gig_upload_sessions
		WHERE upload_id=? AND assignment_id=? AND project_id=? AND status=?`, uploadID, assignmentID, pid, status).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return errors.New("upload session not found")
	}
	return nil
}

func validateSubmissionAttachments(db *sql.DB, assignmentID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	allowed := map[int64]bool{}
	rows, err := db.Query(`SELECT storage_file_id FROM gig_upload_sessions
		WHERE assignment_id=? AND status='completed' AND storage_file_id IS NOT NULL`, assignmentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		allowed[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	oldRows, err := db.Query(`SELECT COALESCE(attachment_file_ids_json,'[]') FROM gig_submissions WHERE assignment_id=?`, assignmentID)
	if err != nil {
		return err
	}
	for oldRows.Next() {
		var raw string
		if err := oldRows.Scan(&raw); err != nil {
			oldRows.Close()
			return err
		}
		var existing []int64
		_ = parseJSON(raw, &existing)
		for _, id := range existing {
			allowed[id] = true
		}
	}
	if err := oldRows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if id <= 0 || !allowed[id] {
			return fmt.Errorf("attachment %d was not uploaded for this assignment", id)
		}
	}
	return nil
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
	var mode string
	q := `SELECT a.id, a.gig_id, COALESCE(a.mode,'direct')
	      FROM gig_assignments a
	      JOIN workers w ON w.id = a.worker_id
	      JOIN gigs g ON g.id=a.gig_id
	      WHERE w.contact_id=? AND w.project_id=? AND g.project_id=?
	        AND a.status IN ('accepted','submitted')
	        AND g.status IN ('accepted','submitted')
	        AND a.token_revoked_at IS NULL
	        AND (a.token_expires_at IS NULL OR datetime(a.token_expires_at)>datetime('now'))`
	args := []any{contactID, pid, pid}
	if convoID > 0 {
		q += ` AND (a.crm_conversation_id=? OR a.crm_conversation_id IS NULL)`
		args = append(args, convoID)
	}
	q += ` ORDER BY a.offered_at DESC LIMIT 1`
	if err := ctx.AppDB().QueryRow(q, args...).Scan(&assignID, &gigID, &mode); errors.Is(err, sql.ErrNoRows) {
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
		var token, publicBaseURL string
		_ = ctx.AppDB().QueryRow(
			`SELECT magic_token, COALESCE(public_base_url,'') FROM gig_assignments WHERE id=?`, assignID,
		).Scan(&token, &publicBaseURL)
		workerURL, err := buildWorkerURL(ctx, token, publicBaseURL)
		if err != nil {
			return err
		}
		nudge := "Thanks — to submit this, please open: " + workerURL
		_, _ = crmSendMessage(ctx, pid, contactID, nudge, "", "")
		return nil
	}
	if err := validateSubmission(ctx.AppDB(), gigID, assignID, payload); err != nil {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	markedOverdue, err := markLateSubmissionOverdueTx(tx, pid, gigID)
	if err != nil {
		return err
	}
	if mode == "first-come" {
		var other int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM gig_submissions s
			JOIN gig_assignments a ON a.id=s.assignment_id
			WHERE a.gig_id=? AND a.id<>?`, gigID, assignID).Scan(&other); err != nil {
			return err
		}
		if other > 0 {
			return nil
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_submissions (assignment_id, payload_json, channel) VALUES (?, ?, ?)`,
		assignID, mustJSON(payload), "channel_reply",
	); err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE gig_assignments SET status='submitted', responded_at=COALESCE(responded_at,CURRENT_TIMESTAMP), submitted_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('accepted','submitted') AND token_revoked_at IS NULL`,
		assignID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("assignment changed during submission")
	}
	if _, err := tx.Exec(
		`UPDATE gigs SET status='submitted',
		 overdue_at=CASE WHEN due_at IS NOT NULL AND datetime(due_at)<datetime('now') THEN COALESCE(overdue_at,CURRENT_TIMESTAMP) ELSE overdue_at END,
		 updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		gigID,
	); err != nil {
		return err
	}
	if mode == "first-come" {
		if _, err := tx.Exec(`UPDATE gig_assignments SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
			WHERE gig_id=? AND id<>? AND status IN ('offered','accepted')`, gigID, assignID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body) VALUES (?, ?, 'submitted', 'worker', 'channel_reply')`,
		pid, gigID,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := syncContractFromGig(ctx.AppDB(), pid, gigID, "submitted"); err != nil {
		ctx.Logger().Warn("sync contract milestone after channel submission failed", "gig_id", gigID, "err", err.Error())
	}
	ctx.EmitWithProject("gig.submitted", pid, map[string]any{
		"gig_id":        gigID,
		"assignment_id": assignID,
		"channel":       "channel_reply",
	})
	if markedOverdue {
		ctx.EmitWithProject("gig.overdue", pid, map[string]any{"gig_id": gigID, "reason": "late_submission"})
	}
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

func validateSubmission(db *sql.DB, gigID, assignmentID int64, payload map[string]any) error {
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
	for _, key := range requiredResultKeys(schema) {
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
	if err := validateSchemaValue("submission", schema, payload); err != nil {
		return err
	}
	if err := validateInstructionResponses(db, gigID, assignmentID, payload, true); err != nil {
		return err
	}
	return nil
}

func validateSchemaValue(path string, schema map[string]any, value any) error {
	typeName := strOf(schema["type"])
	switch typeName {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, key := range requiredResultKeys(schema) {
			if _, present := obj[key]; !present {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for key, raw := range props {
				child, present := obj[key]
				if !present {
					continue
				}
				childSchema, _ := raw.(map[string]any)
				if err := validateSchemaValue(path+"."+key, childSchema, child); err != nil {
					return err
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range items {
				if err := validateSchemaValue(fmt.Sprintf("%s[%d]", path, i), itemSchema, item); err != nil {
					return err
				}
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be text", path)
		}
		if max := intFromAny(schema["maxLength"]); max > 0 && len([]rune(s)) > max {
			return fmt.Errorf("%s must be at most %d characters", path, max)
		}
		if strOf(schema["format"]) == "date" {
			if _, err := time.Parse("2006-01-02", s); err != nil {
				return fmt.Errorf("%s must be a date", path)
			}
		}
	case "number", "integer":
		n, ok := numberFromAny(value)
		if !ok || (typeName == "integer" && math.Trunc(n) != n) {
			return fmt.Errorf("%s must be %s", path, typeName)
		}
		if min, ok := numberFromAny(schema["minimum"]); ok && n < min {
			return fmt.Errorf("%s must be at least %v", path, min)
		}
		if max, ok := numberFromAny(schema["maximum"]); ok && n > max {
			return fmt.Errorf("%s must be at most %v", path, max)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be true or false", path)
		}
	}
	if allowed, ok := schema["enum"].([]any); ok && len(allowed) > 0 {
		matched := false
		for _, candidate := range allowed {
			if fmt.Sprint(candidate) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s contains an unsupported value", path)
		}
	}
	if expected, ok := schema["const"]; ok && fmt.Sprint(expected) != fmt.Sprint(value) {
		return fmt.Errorf("%s must be %v", path, expected)
	}
	return nil
}

func numberFromAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func requiredResultKeys(schema map[string]any) []string {
	requiredAny, _ := schema["required"].([]any)
	out := make([]string, 0, len(requiredAny))
	for _, r := range requiredAny {
		if key := strOf(r); key != "" {
			out = append(out, key)
		}
	}
	return out
}
