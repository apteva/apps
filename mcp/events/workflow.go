package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

type EventSettings struct {
	ApplicationsOpen   bool   `json:"applications_open"`
	OpensAt            string `json:"opens_at"`
	ClosesAt           string `json:"closes_at"`
	PerformerCapacity  int64  `json:"performer_capacity"`
	Instructions       string `json:"instructions"`
	SetMinutes         int64  `json:"set_minutes"`
	EmailRequired      bool   `json:"email_required"`
	LineupPublished    bool   `json:"lineup_published"`
	Cancelled          bool   `json:"cancelled"`
	CancellationReason string `json:"cancellation_reason"`
}

func settingsFrom(q rowQuerier, id int64) (EventSettings, error) {
	s := EventSettings{SetMinutes: 5}
	var raw string
	err := q.QueryRow(`SELECT settings_json FROM event_settings WHERE event_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
func saveSettings(id int64, in map[string]any) (EventSettings, error) {
	e, err := getEvent(id)
	if err != nil {
		return EventSettings{}, err
	}
	s, err := settingsFrom(db(), id)
	if err != nil {
		return s, err
	}
	raw, _ := json.Marshal(in)
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.PerformerCapacity < 0 || s.SetMinutes < 1 || s.SetMinutes > 120 {
		return s, errors.New("invalid performer capacity or set length")
	}
	for _, v := range []*string{&s.OpensAt, &s.ClosesAt} {
		if *v != "" {
			t, err := time.Parse(time.RFC3339, *v)
			if err != nil {
				return s, errors.New("application dates need a timezone")
			}
			*v = t.UTC().Format(time.RFC3339)
		}
	}
	if s.OpensAt != "" && s.ClosesAt != "" && s.OpensAt >= s.ClosesAt {
		return s, errors.New("application deadline must follow opening")
	}
	if s.ClosesAt != "" && e.StartsAt != "" && s.ClosesAt > e.StartsAt {
		return s, errors.New("application deadline must not follow the show start")
	}
	if len(s.Instructions) > 10000 || len(s.CancellationReason) > 2000 {
		return s, errors.New("text is too long")
	}
	if s.Cancelled {
		s.ApplicationsOpen = false
		s.LineupPublished = false
	}
	raw, _ = json.Marshal(s)
	_, err = db().Exec(`INSERT INTO event_settings(event_id,settings_json) VALUES(?,?) ON CONFLICT(event_id) DO UPDATE SET settings_json=excluded.settings_json`, id, string(raw))
	return s, err
}
func applicationClosed(e *Event, s EventSettings) string {
	if s.Cancelled {
		return "This show has been cancelled."
	}
	if e.Status != "published" || e.Visibility != "public" || !s.ApplicationsOpen {
		return "Artist applications are closed."
	}
	n := now()
	if e.StartsAt != "" && e.StartsAt <= n {
		return "This show has already started."
	}
	if s.OpensAt != "" && n < s.OpensAt {
		return "Artist applications are not open yet."
	}
	if s.ClosesAt != "" && n >= s.ClosesAt {
		return "The application deadline has passed."
	}
	return ""
}
func (a *App) handleWorkflow(w http.ResponseWriter, r *http.Request) bool {
	id, action := parseIDAction(r.URL.Path, "/shows/")
	switch action {
	case "settings":
		if _, err := getEvent(id); err != nil {
			writeResult[any](w, nil, err)
			return true
		}
		if r.Method == "GET" {
			s, err := settingsFrom(db(), id)
			writeResult(w, s, err)
			return true
		}
		if r.Method == "PATCH" {
			var in map[string]any
			if decodeJSON(w, r, &in) {
				s, err := saveSettings(id, in)
				writeResult(w, s, err)
			}
			return true
		}
	case "duplicate":
		if r.Method == "POST" {
			e, err := getEvent(id)
			if err != nil {
				writeResult[any](w, nil, err)
				return true
			}
			s, err := settingsFrom(db(), id)
			if err != nil {
				writeResult[any](w, nil, err)
				return true
			}
			suffix, _ := newCode()
			in := map[string]any{"title": e.Title + " (copy)", "slug": e.Slug + "-" + strings.ToLower(suffix), "description": e.Description, "timezone": e.Timezone, "capacity": e.Capacity, "external_checkout_url": e.ExternalCheckoutURL}
			if e.VenueID != nil {
				in["venue_id"] = *e.VenueID
			}
			copied, err := createEvent(in)
			if err == nil {
				s.ApplicationsOpen = false
				s.LineupPublished = false
				s.Cancelled = false
				s.CancellationReason = ""
				s.OpensAt = ""
				s.ClosesAt = ""
				raw, _ := json.Marshal(s)
				_, err = db().Exec(`INSERT INTO event_settings(event_id,settings_json) VALUES(?,?)`, copied.ID, string(raw))
			}
			writeResult(w, copied, err)
			return true
		}
	case "export":
		if r.Method == "GET" {
			rows, err := listApplications(id, "")
			if err != nil {
				writeResult[any](w, nil, err)
				return true
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="artist-applications.csv"`)
			out := csv.NewWriter(w)
			out.Write([]string{"Name", "Stage name", "Email", "WhatsApp", "Instagram", "Status", "Private notes"})
			for _, row := range rows {
				var social map[string]any
				json.Unmarshal([]byte(row.SocialLinksJSON), &social)
				values := []string{row.ApplicantName, row.StageName, row.Email, row.Phone, argString(social, "instagram"), row.Status, row.ReviewerNotes}
				for i, v := range values {
					if len(v) > 0 && strings.ContainsAny(v[:1], "=+-@\t\r") {
						values[i] = "'" + v
					}
				}
				out.Write(values)
			}
			out.Flush()
			return true
		}
	}
	if action != "" {
		http.NotFound(w, r)
		return true
	}
	return false
}

var nonDigits = regexp.MustCompile(`[^0-9]`)
var instagramPattern = regexp.MustCompile(`^[a-zA-Z0-9._]{1,30}$`)

func submitPublicApplication(e *Event, in map[string]any) error {
	name := argString(in, "applicant_name")
	email := strings.ToLower(argString(in, "email"))
	phone := nonDigits.ReplaceAllString(argString(in, "phone"), "")
	instagram := strings.ToLower(strings.TrimPrefix(argString(in, "instagram"), "@"))
	if name == "" || len(name) > 160 {
		return errors.New("your name is required (up to 160 characters)")
	}
	if email != "" {
		m, err := mail.ParseAddress(email)
		if err != nil || m.Address != email {
			return errors.New("enter a valid email")
		}
	}
	if phone != "" && (len(phone) < 7 || len(phone) > 15) {
		return errors.New("enter a WhatsApp number including country code")
	}
	if instagram != "" && !instagramPattern.MatchString(instagram) {
		return errors.New("enter an Instagram handle, without a URL")
	}
	if phone == "" && instagram == "" && email == "" {
		return errors.New("add WhatsApp, Instagram or email so the organiser can contact you")
	}
	for _, key := range []string{"stage_name", "bio", "tech_needs", "notes"} {
		if len(argString(in, key)) > 4000 {
			return errors.New("application text is too long")
		}
	}
	var photo []byte
	mime := ""
	consent, _ := in["photo_consent"].(bool)
	if encoded := argString(in, "photo_base64"); encoded != "" {
		var err error
		photo, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(photo) > 2<<20 {
			return errors.New("photo must be a JPEG or PNG under 2 MB")
		}
		mime = http.DetectContentType(photo)
		if mime != "image/jpeg" && mime != "image/png" {
			return errors.New("photo must be JPEG or PNG")
		}
		if !consent {
			return errors.New("photo permission is required when uploading a photo")
		}
	}
	tx, err := db().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE events SET id=id WHERE id=? AND project_id=?`, e.ID, projectID()); err != nil {
		return err
	}
	e, err = getEventFrom(tx, e.ID)
	if err != nil {
		return err
	}
	s, err := settingsFrom(tx, e.ID)
	if err != nil {
		return err
	}
	if reason := applicationClosed(e, s); reason != "" {
		return errors.New(reason)
	}
	if s.EmailRequired && email == "" {
		return errors.New("email is required for this event")
	}
	ids := []string{}
	for key, v := range map[string]string{"email": email, "phone": phone, "instagram": instagram} {
		if v != "" {
			sum := sha256.Sum256([]byte(key + ":" + v))
			ids = append(ids, hex.EncodeToString(sum[:]))
		}
	}
	for _, identity := range ids {
		var existing int64
		err := tx.QueryRow(`SELECT application_id FROM application_identities WHERE event_id=? AND identity=?`, e.ID, identity).Scan(&existing)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	social, _ := json.Marshal(map[string]any{"instagram": instagram, "photo_consent": consent, "has_photo": len(photo) > 0})
	res, err := tx.Exec(`INSERT INTO performer_applications(event_id,applicant_name,stage_name,email,phone,bio,set_length_minutes,social_links_json,tech_needs,notes,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, e.ID, name, argString(in, "stage_name"), email, phone, argString(in, "bio"), s.SetMinutes, string(social), argString(in, "tech_needs"), argString(in, "notes"), now())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	for _, identity := range ids {
		if _, err = tx.Exec(`INSERT INTO application_identities(event_id,identity,application_id) VALUES(?,?,?)`, e.ID, identity, id); err != nil {
			return err
		}
	}
	if len(photo) > 0 {
		if _, err = tx.Exec(`INSERT INTO application_photos(application_id,content_type,data,consent) VALUES(?,?,?,?)`, id, mime, photo, consent); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func serveApplicationPhoto(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := getApplication(id); err != nil {
		http.NotFound(w, r)
		return
	}
	var mime string
	var data []byte
	if err := db().QueryRow(`SELECT content_type,data FROM application_photos WHERE application_id=?`, id).Scan(&mime, &data); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		ext := "jpg"
		if mime == "image/png" {
			ext = "png"
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="artist-%d.%s"`, id, ext))
	}
	w.Write(data)
}
func (a *App) handleSlotItem(w http.ResponseWriter, r *http.Request) {
	id, action := parseIDAction(r.URL.Path, "/slots/")
	if action != "" {
		http.NotFound(w, r)
		return
	}
	slot, err := getSlot(id)
	if err != nil {
		writeResult[any](w, nil, err)
		return
	}
	if r.Method == "DELETE" {
		_, err = db().Exec(`DELETE FROM performance_slots WHERE id=?`, id)
		writeResult(w, map[string]bool{"removed": true}, err)
		return
	}
	if r.Method == "PATCH" {
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		for key, target := range map[string]*string{"starts_at": &slot.StartsAt, "ends_at": &slot.EndsAt, "title": &slot.Title, "performer_name": &slot.PerformerName, "notes": &slot.Notes} {
			if _, ok := in[key]; ok {
				*target = argString(in, key)
			}
		}
		if strings.TrimSpace(slot.PerformerName) == "" {
			http.Error(w, "performer name required", 400)
			return
		}
		tx, txErr := db().Begin()
		if txErr != nil {
			writeResult[any](w, nil, txErr)
			return
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE events SET id=id WHERE id=? AND project_id=?`, slot.EventID, projectID()); err != nil {
			writeResult[any](w, nil, err)
			return
		}
		e, err := getEventFrom(tx, slot.EventID)
		if err != nil {
			writeResult[any](w, nil, err)
			return
		}
		if err = validateSlotTimesFrom(tx, e, slot.StartsAt, slot.EndsAt, id); err == nil {
			_, err = tx.Exec(`UPDATE performance_slots SET performer_name=?,title=?,starts_at=?,ends_at=?,notes=?,updated_at=? WHERE id=?`, slot.PerformerName, slot.Title, slot.StartsAt, slot.EndsAt, slot.Notes, now(), id)
		}
		if err == nil {
			err = tx.Commit()
		}
		writeResult(w, slot, err)
		return
	}
	http.Error(w, "PATCH or DELETE", 405)
}
func validateSlotTimes(e *Event, start, end string, except int64) error {
	return validateSlotTimesFrom(db(), e, start, end, except)
}
func validateSlotTimesFrom(q rowQuerier, e *Event, start, end string, except int64) error {
	if start == "" && end == "" {
		return nil
	}
	a, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return errors.New("slot start requires a time with timezone")
	}
	b, err := time.Parse(time.RFC3339, end)
	if err != nil || !b.After(a) {
		return errors.New("slot end must follow start")
	}
	if e.StartsAt != "" {
		t, _ := time.Parse(time.RFC3339, e.StartsAt)
		if a.Before(t) {
			return errors.New("slot starts before event")
		}
	}
	if e.EndsAt != "" {
		t, _ := time.Parse(time.RFC3339, e.EndsAt)
		if b.After(t) {
			return errors.New("slot ends after event")
		}
	}
	var n int
	err = q.QueryRow(`SELECT COUNT(*) FROM performance_slots WHERE event_id=? AND id<>? AND status='scheduled' AND julianday(starts_at)<julianday(?) AND julianday(ends_at)>julianday(?)`, e.ID, except, end, start).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return errors.New("this slot overlaps another performer")
	}
	return nil
}
