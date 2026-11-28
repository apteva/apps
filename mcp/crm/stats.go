package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Activity stats for the CRM activity widget: new contacts and message
// traffic over a period of whole UTC days, compared with the period before,
// plus a daily series and the latest new contacts. One query set over one or
// more projects, so the project and All projects views share the same code.

var statsPeriods = map[int]bool{7: true, 30: true, 90: true}

const statsLatestContacts = 6

type statsTotals struct {
	NewContacts         int `json:"new_contacts"`
	MessagesReceived    int `json:"messages_received"`
	MessagesSent        int `json:"messages_sent"`
	SendFailures        int `json:"send_failures"`
	ConversationsOpened int `json:"conversations_opened"`
	ConversationsClosed int `json:"conversations_closed"`
}

type statsDay struct {
	Date             string `json:"date"`
	NewContacts      int    `json:"new_contacts"`
	MessagesReceived int    `json:"messages_received"`
	MessagesSent     int    `json:"messages_sent"`
}

type statsContact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Source      string `json:"source,omitempty"`
	CreatedAt   string `json:"created_at"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
}

type activityStats struct {
	PeriodDays     int            `json:"period_days"`
	Since          string         `json:"since"`
	Until          string         `json:"until"`
	HideAutomated  bool           `json:"hide_automated"`
	Totals         statsTotals    `json:"totals"`
	Previous       statsTotals    `json:"previous"`
	OpenNow        int            `json:"open_now"`
	Daily          []statsDay     `json:"daily"`
	LatestContacts []statsContact `json:"latest_contacts"`
}

const (
	statsReceivedKinds = "'email_received','sms_received','whatsapp_received'"
	statsSentKinds     = "'email_sent','sms_sent','whatsapp_sent'"
	statsFailedKinds   = "'email_send_failed','sms_send_failed','whatsapp_send_failed'"
)

func sqlPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// dbActivityStats computes the stats for pids over the period ending today
// (UTC). hideAutomated leaves out contacts tagged automated everywhere.
func dbActivityStats(db *sql.DB, pids []string, days int, hideAutomated bool, now time.Time) (*activityStats, error) {
	if !statsPeriods[days] {
		days = 7
	}
	today := now.UTC().Truncate(24 * time.Hour)
	since := today.AddDate(0, 0, -(days - 1))
	until := today.AddDate(0, 0, 1)
	prevSince := since.AddDate(0, 0, -days)
	out := &activityStats{
		PeriodDays: days, HideAutomated: hideAutomated,
		Since: since.Format(time.RFC3339), Until: until.Format(time.RFC3339),
		Daily: []statsDay{}, LatestContacts: []statsContact{},
	}
	if len(pids) == 0 {
		for d := since; d.Before(until); d = d.AddDate(0, 0, 1) {
			out.Daily = append(out.Daily, statsDay{Date: d.Format("2006-01-02")})
		}
		return out, nil
	}
	in := sqlPlaceholders(len(pids))
	pidArgs := make([]any, 0, len(pids))
	for _, pid := range pids {
		pidArgs = append(pidArgs, pid)
	}
	// alias.contact_id / alias.id must not be a contact tagged automated.
	notAutomated := func(col string) string {
		if !hideAutomated {
			return ""
		}
		return " AND NOT EXISTS (SELECT 1 FROM contact_tags t WHERE t.contact_id = " + col + " AND t.tag_name = '" + tagAutomated + "')"
	}
	withPids := func(rest ...any) []any { return append(append([]any{}, pidArgs...), rest...) }
	sinceS, untilS, prevS := since.Format(time.RFC3339), until.Format(time.RFC3339), prevSince.Format(time.RFC3339)

	// Totals for the current and previous window in one pass per table.
	// Two counts: rows in the current window, rows in the previous window.
	window := func(col string) string {
		count := func() string {
			return `COALESCE(SUM(CASE WHEN julianday(` + col + `) >= julianday(?) AND julianday(` + col + `) < julianday(?) THEN 1 ELSE 0 END),0)`
		}
		return count() + ", " + count()
	}
	windowArgs := []any{sinceS, untilS, prevS, sinceS}

	if err := db.QueryRow(`SELECT `+window("c.created_at")+`
		FROM contacts c WHERE c.project_id IN (`+in+`) AND c.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday(?)`+notAutomated("c.id"),
		append(append(windowArgs, pidArgs...), prevS)...,
	).Scan(&out.Totals.NewContacts, &out.Previous.NewContacts); err != nil {
		return nil, err
	}

	var cur, prev [3]int
	if err := db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsReceivedKinds+`) AND julianday(a.occurred_at) >= julianday(?) THEN 1 END),0),
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsSentKinds+`) AND julianday(a.occurred_at) >= julianday(?) THEN 1 END),0),
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsFailedKinds+`) AND julianday(a.occurred_at) >= julianday(?) THEN 1 END),0),
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsReceivedKinds+`) AND julianday(a.occurred_at) < julianday(?) THEN 1 END),0),
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsSentKinds+`) AND julianday(a.occurred_at) < julianday(?) THEN 1 END),0),
			COALESCE(SUM(CASE WHEN a.kind IN (`+statsFailedKinds+`) AND julianday(a.occurred_at) < julianday(?) THEN 1 END),0)
		FROM contact_activities a
		WHERE a.project_id IN (`+in+`)
		  AND a.kind IN (`+statsReceivedKinds+`,`+statsSentKinds+`,`+statsFailedKinds+`)
		  AND julianday(a.occurred_at) >= julianday(?) AND julianday(a.occurred_at) < julianday(?)`+notAutomated("a.contact_id"),
		append(append([]any{sinceS, sinceS, sinceS, sinceS, sinceS, sinceS}, pidArgs...), prevS, untilS)...,
	).Scan(&cur[0], &cur[1], &cur[2], &prev[0], &prev[1], &prev[2]); err != nil {
		return nil, err
	}
	out.Totals.MessagesReceived, out.Totals.MessagesSent, out.Totals.SendFailures = cur[0], cur[1], cur[2]
	out.Previous.MessagesReceived, out.Previous.MessagesSent, out.Previous.SendFailures = prev[0], prev[1], prev[2]

	if err := db.QueryRow(`SELECT `+window("cc.started_at")+`
		FROM contact_conversations cc JOIN contacts c ON c.id = cc.contact_id AND c.project_id = cc.project_id
		WHERE cc.project_id IN (`+in+`) AND c.deleted_at IS NULL
		  AND julianday(cc.started_at) >= julianday(?)`+notAutomated("cc.contact_id"),
		append(append(windowArgs, pidArgs...), prevS)...,
	).Scan(&out.Totals.ConversationsOpened, &out.Previous.ConversationsOpened); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT `+window("cc.status_changed_at")+`
		FROM contact_conversations cc JOIN contacts c ON c.id = cc.contact_id AND c.project_id = cc.project_id
		WHERE cc.project_id IN (`+in+`) AND c.deleted_at IS NULL AND cc.status = 'closed'
		  AND cc.status_changed_at IS NOT NULL AND julianday(cc.status_changed_at) >= julianday(?)`+notAutomated("cc.contact_id"),
		append(append(windowArgs, pidArgs...), prevS)...,
	).Scan(&out.Totals.ConversationsClosed, &out.Previous.ConversationsClosed); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*)
		FROM contact_conversations cc JOIN contacts c ON c.id = cc.contact_id AND c.project_id = cc.project_id
		WHERE cc.project_id IN (`+in+`) AND c.deleted_at IS NULL AND COALESCE(cc.status,'open') = 'open'`+notAutomated("cc.contact_id"),
		pidArgs...,
	).Scan(&out.OpenNow); err != nil {
		return nil, err
	}

	// Daily series over the current window.
	byDate := map[string]*statsDay{}
	for d := since; d.Before(until); d = d.AddDate(0, 0, 1) {
		out.Daily = append(out.Daily, statsDay{Date: d.Format("2006-01-02")})
	}
	for i := range out.Daily {
		byDate[out.Daily[i].Date] = &out.Daily[i]
	}
	rows, err := db.Query(`SELECT date(c.created_at), COUNT(*) FROM contacts c
		WHERE c.project_id IN (`+in+`) AND c.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday(?) AND julianday(c.created_at) < julianday(?)`+notAutomated("c.id")+`
		GROUP BY 1`, withPids(sinceS, untilS)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var date string
		var n int
		if err := rows.Scan(&date, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if day := byDate[date]; day != nil {
			day.NewContacts = n
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = db.Query(`SELECT date(a.occurred_at),
			SUM(CASE WHEN a.kind IN (`+statsReceivedKinds+`) THEN 1 ELSE 0 END),
			SUM(CASE WHEN a.kind IN (`+statsSentKinds+`) THEN 1 ELSE 0 END)
		FROM contact_activities a
		WHERE a.project_id IN (`+in+`) AND a.kind IN (`+statsReceivedKinds+`,`+statsSentKinds+`)
		  AND julianday(a.occurred_at) >= julianday(?) AND julianday(a.occurred_at) < julianday(?)`+notAutomated("a.contact_id")+`
		GROUP BY 1`, withPids(sinceS, untilS)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var date string
		var received, sent int
		if err := rows.Scan(&date, &received, &sent); err != nil {
			rows.Close()
			return nil, err
		}
		if day := byDate[date]; day != nil {
			day.MessagesReceived, day.MessagesSent = received, sent
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	rows, err = db.Query(`SELECT c.id, c.project_id, COALESCE(c.display_name,''), COALESCE(c.primary_email,''),
			COALESCE(c.primary_phone,''), COALESCE(c.source,''), c.created_at
		FROM contacts c
		WHERE c.project_id IN (`+in+`) AND c.deleted_at IS NULL`+notAutomated("c.id")+`
		ORDER BY julianday(c.created_at) DESC, c.id DESC LIMIT ?`, withPids(statsLatestContacts)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c statsContact
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Email, &c.Phone, &c.Source, &c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out.LatestContacts = append(out.LatestContacts, c)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// GET /stats?period=7|30|90&hide_automated=1[&scope=global][&project_id=]
func (a *App) handleHTTPStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()
	scope := strings.TrimSpace(q.Get("scope"))
	if scope != "" && scope != "global" {
		httpErr(w, http.StatusBadRequest, "scope must be global when provided")
		return
	}
	days := 7
	if raw := strings.TrimSpace(q.Get("period")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || !statsPeriods[n] {
			httpErr(w, http.StatusBadRequest, "period must be 7, 30 or 90")
			return
		}
		days = n
	}
	hide := q.Get("hide_automated") == "1" || q.Get("hide_automated") == "true"
	if scope == "global" {
		gp, ok := resolveGlobalProjects(w, r)
		if !ok {
			return
		}
		stats, err := dbActivityStats(gp.ctx.AppDB(), gp.ids, days, hide, time.Now())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for i := range stats.LatestContacts {
			stats.LatestContacts[i].ProjectName = gp.names[stats.LatestContacts[i].ProjectID]
		}
		httpJSON(w, map[string]any{"stats": stats, "projects": gp.list, "selected_project_id": gp.selected})
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := dbActivityStats(globalCtx.AppDB(), []string{pid}, days, hide, time.Now())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range stats.LatestContacts {
		stats.LatestContacts[i].ProjectID = ""
	}
	httpJSON(w, map[string]any{"stats": stats})
}
