package main

import (
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) handleSigning(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/sign/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	session, err := sessionByToken(globalCtx.AppDB(), token)
	if err != nil {
		http.Error(w, "unable to load signing request", http.StatusInternalServerError)
		return
	}
	if session == nil {
		renderSigningMessage(w, http.StatusNotFound, "This signing link is invalid, expired, or no longer active.")
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		changed, err := markRecipientViewed(globalCtx.AppDB(), session)
		if err != nil {
			http.Error(w, "unable to open signing request", http.StatusInternalServerError)
			return
		}
		if changed {
			emit(globalCtx, "recipient.viewed", &session.Envelope, session.Recipient.ID)
		}
		renderSigningPage(w, token, session)
	case r.Method == http.MethodGet && action == "document":
		a.serveSigningDocument(w, session)
	case r.Method == http.MethodPost && action == "complete":
		if !allowSignerPost(r) {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		a.completeSigningHTTP(w, r, token)
	case r.Method == http.MethodPost && action == "decline":
		if !allowSignerPost(r) {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		a.declineSigningHTTP(w, r, token)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) serveSigningDocument(w http.ResponseWriter, session *SigningSession) {
	_, body, err := storageDownload(globalCtx.WithProject(session.Envelope.ProjectID), session.Envelope.SourceFileID)
	if err != nil {
		http.Error(w, "unable to load document", http.StatusBadGateway)
		return
	}
	if bytesHash(body) != session.Envelope.SourceSHA256 {
		http.Error(w, "document integrity check failed", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="document.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (a *App) completeSigningHTTP(w http.ResponseWriter, r *http.Request, token string) {
	// The signing page posts a plain urlencoded form; multipart is
	// accepted too for future drawn-signature uploads. ParseMultipartForm
	// alone rejected urlencoded bodies, so no browser submit ever worked.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var err error
	if mediaType == "multipart/form-data" {
		err = r.ParseMultipartForm(1 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		renderSigningMessage(w, http.StatusBadRequest, "The submitted form could not be read or is too large.")
		return
	}
	if r.FormValue("consent") != "on" {
		renderSigningMessage(w, http.StatusBadRequest, "You must consent before completing this document.")
		return
	}
	values := map[int64]string{}
	for key, list := range r.Form {
		if !strings.HasPrefix(key, "field_") || len(list) == 0 {
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(key, "field_"), 10, 64)
		if id != 0 {
			values[id] = list[0]
		}
	}
	env, recipient, next, finalize, err := completeRecipient(globalCtx.AppDB(), token, r.FormValue("legal_name"), values)
	if err != nil {
		renderSigningMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	topic := "recipient.signed"
	if recipient.Role == "approver" {
		topic = "recipient.approved"
	}
	emit(globalCtx, topic, env, recipient.ID)
	if next != nil && env.DeliveryMode == "messaging" {
		_, _, nextToken, tokenErr := createRecipientToken(globalCtx.AppDB(), env.ProjectID, env.ID, next.ID)
		if tokenErr == nil {
			_ = sendInvitation(globalCtx, env, next, nextToken, false)
		}
	}
	if finalize {
		env, err = finalizeEnvelope(globalCtx, env)
		if err != nil {
			renderSigningMessage(w, http.StatusInternalServerError, "Your signature was recorded, but final document generation failed. The sender can retry finalization.")
			return
		}
		emit(globalCtx, "envelope.completed", env, 0)
		sendCompletionNotices(globalCtx, env)
	}
	renderSigningMessage(w, http.StatusOK, "Thank you. Your response has been recorded.")
}

func (a *App) declineSigningHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if err := r.ParseForm(); err != nil {
		renderSigningMessage(w, http.StatusBadRequest, "Invalid decline request.")
		return
	}
	env, recipient, err := declineRecipient(globalCtx.AppDB(), token, r.FormValue("reason"))
	if err != nil {
		renderSigningMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	emit(globalCtx, "envelope.declined", env, recipient.ID)
	renderSigningMessage(w, http.StatusOK, "You declined this document. The sender has been notified through the envelope status.")
}

func allowSignerPost(r *http.Request) bool {
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	return site == "" || site == "same-origin" || site == "none"
}

// pageQuery keeps project-scoped install resolution working across the
// page's own relative links (iframe document, complete/decline posts):
// those drop the incoming query string, so the project_id the platform
// proxy needs must be re-attached to every URL the page emits.
func pageQuery(session *SigningSession) string {
	pid := strings.TrimSpace(session.Envelope.ProjectID)
	if pid == "" {
		return ""
	}
	return "?project_id=" + url.QueryEscape(pid)
}

func renderSigningPage(w http.ResponseWriter, token string, session *SigningSession) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
	var fields strings.Builder
	for _, field := range session.Fields {
		label := field.Label
		if label == "" {
			label = strings.ReplaceAll(field.FieldType, "_", " ")
		}
		required := ""
		if field.Required {
			required = " required"
		}
		name := fmt.Sprintf("field_%d", field.ID)
		switch field.FieldType {
		case "date_signed":
			fields.WriteString(`<label>` + html.EscapeString(label) + `<input type="text" value="Set automatically when submitted" disabled></label>`)
		case "checkbox":
			fields.WriteString(`<label class="check"><input type="checkbox" name="` + name + `"` + required + `> ` + html.EscapeString(label) + `</label>`)
		case "signature":
			fields.WriteString(`<label>` + html.EscapeString(label) + `<input type="text" name="` + name + `" placeholder="Type your signature" maxlength="300"` + required + `></label>`)
		default:
			fields.WriteString(`<label>` + html.EscapeString(label) + `<input type="text" name="` + name + `" maxlength="2000"` + required + `></label>`)
		}
	}
	legalName := ""
	if session.Recipient.Role == "signer" {
		legalName = `<label>Full legal name<input type="text" name="legal_name" maxlength="300" required></label>`
	}
	actionLabel := "Approve document"
	if session.Recipient.Role == "signer" {
		actionLabel = "Sign document"
	}
	q := pageQuery(session)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>
<style>body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f5f6f8;color:#172033}header{padding:18px 24px;background:#fff;border-bottom:1px solid #dde1e7}header h1{font-size:18px;margin:0 0 4px}header p{margin:0;color:#667085;font-size:13px}.layout{display:grid;grid-template-columns:minmax(0,1fr)360px;gap:16px;padding:16px;height:calc(100vh - 86px);box-sizing:border-box}iframe{width:100%%;height:100%%;border:1px solid #d0d5dd;border-radius:10px;background:#fff}.panel{background:#fff;border:1px solid #d0d5dd;border-radius:10px;padding:18px;overflow:auto}.panel h2{font-size:15px;margin:0 0 14px}label{display:block;font-size:12px;color:#475467;margin:0 0 12px;text-transform:capitalize}input[type=text]{box-sizing:border-box;width:100%%;margin-top:5px;padding:10px;border:1px solid #c8ced8;border-radius:7px;font-size:14px}.check{display:flex;align-items:flex-start;gap:8px;text-transform:none;line-height:1.4}.check input{margin-top:2px}.actions{display:grid;gap:8px;margin-top:16px}button{border:0;border-radius:7px;padding:11px;font-weight:600;cursor:pointer}.primary{background:#2457d6;color:#fff}.decline{background:#fff;color:#b42318;border:1px solid #f0b4ad}.message{padding:10px;background:#f8fafc;border-radius:7px;font-size:13px;color:#475467;margin-bottom:14px}@media(max-width:850px){.layout{grid-template-columns:1fr;height:auto}iframe{height:60vh}}</style></head><body>
<header><h1>%s</h1><p>From %s · For %s · Expires %s</p></header><main class="layout"><iframe src="./%s/document%s" title="Document"></iframe><section class="panel">%s<h2>Your fields</h2><form method="post" action="./%s/complete%s">%s%s<label class="check"><input type="checkbox" name="consent" required> I consent to use an electronic signature and confirm that the information I submit is accurate.</label><div class="actions"><button class="primary" type="submit">%s</button></div></form><form method="post" action="./%s/decline%s"><label>Reason (optional)<input type="text" name="reason" maxlength="1000"></label><button class="decline" type="submit">Decline</button></form></section></main></body></html>`,
		html.EscapeString(session.Envelope.Title), html.EscapeString(session.Envelope.Title), html.EscapeString(session.Envelope.SenderName), html.EscapeString(session.Recipient.Name), html.EscapeString(session.Envelope.ExpiresAt), token, q,
		optionalMessage(session.Envelope.Message), token, q, legalName, fields.String(), actionLabel, token, q)
}

func optionalMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	return `<div class="message">` + html.EscapeString(message) + `</div>`
}

func renderSigningMessage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Signatures</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f5f6f8;color:#172033;display:grid;place-items:center;min-height:100vh;margin:0}.card{max-width:520px;background:#fff;border:1px solid #d0d5dd;border-radius:12px;padding:28px;box-shadow:0 10px 30px #10182812}h1{font-size:20px;margin-top:0}p{line-height:1.55;color:#475467}</style></head><body><div class="card"><h1>Signatures</h1><p>%s</p></div></body></html>`, html.EscapeString(message))
}
