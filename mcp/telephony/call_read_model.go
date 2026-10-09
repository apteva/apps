package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Read models deliberately exclude diagnostics, media credentials, signaling
// payloads and preparation state. Detail and mutation paths keep full rows.
const callListColumns = `id,thread_id,COALESCE(direction,'outbound'),COALESCE(carrier_slug,'twilio'),
 COALESCE(carrier_sid,''),COALESCE(carrier_leg_id,''),COALESCE(carrier_session_id,''),COALESCE(ingress_path,''),
 to_number,from_number,directive,voice,status,placed_at,COALESCE(answered_at,''),COALESCE(ended_at,''),project_id,
 COALESCE(error_message,''),COALESCE(recording_mode,'off'),COALESCE(termination_cause,''),COALESCE(termination_code,''),
 COALESCE(termination_initiator,''),COALESCE(media_status,'idle'),COALESCE(media_error_message,''),
 COALESCE(media_connected_at,''),COALESCE(media_disconnected_at,''),COALESCE(media_close_code,0),COALESCE(media_close_reason,''),
 COALESCE(media_close_leg,''),COALESCE(peer_kind,'realtime'),COALESCE(routing_flow_id,''),COALESCE(routing_flow_version_id,''),
 COALESCE(routing_destination_id,''),COALESCE(answered_by,''),COALESCE(termination_reason,''),COALESCE(handling_reason,''),
 COALESCE(routing_resolution,''),COALESCE(callback_on_ai,0),COALESCE(machine_detection,'off'),COALESCE(hold_state,'active'),
 COALESCE(recording_control_state,'default'),COALESCE(control_error,''),COALESCE(recording_requested_at,''),
 max_duration_sec,duration_started_at,connected_deadline_at`

const callHintColumns = `id,project_id,COALESCE(direction,'outbound'),from_number,status,COALESCE(peer_kind,'realtime'),
 COALESCE(routing_destination_id,''),COALESCE(media_status,'idle'),COALESCE(hold_state,'active'),
 COALESCE(recording_mode,'off'),COALESCE(recording_control_state,'default'),COALESCE(recording_requested_at,''),COALESCE(handling_reason,'')`

func scanCallList(s rowScanner, hint bool) (callRow, error) {
	var r callRow
	if hint {
		err := s.Scan(&r.ID, &r.ProjectID, &r.Direction, &r.FromNumber, &r.Status, &r.PeerKind, &r.RoutingDestinationID, &r.MediaStatus, &r.HoldState, &r.RecordingMode, &r.RecordingControlState, &r.RecordingRequestedAt, &r.HandlingReason)
		return r, err
	}
	err := s.Scan(&r.ID, &r.ThreadID, &r.Direction, &r.CarrierSlug, &r.CarrierSID, &r.CarrierLegID, &r.CarrierSessionID, &r.IngressPath,
		&r.ToNumber, &r.FromNumber, &r.Directive, &r.Voice, &r.Status, &r.PlacedAt, &r.AnsweredAt, &r.EndedAt, &r.ProjectID, &r.ErrorMessage,
		&r.RecordingMode, &r.TerminationCause, &r.TerminationCode, &r.TerminationInitiator, &r.MediaStatus, &r.MediaErrorMessage,
		&r.MediaConnectedAt, &r.MediaDisconnectedAt, &r.MediaCloseCode, &r.MediaCloseReason, &r.MediaCloseLeg, &r.PeerKind, &r.RoutingFlowID,
		&r.RoutingFlowVersionID, &r.RoutingDestinationID, &r.AnsweredBy, &r.TerminationReason, &r.HandlingReason, &r.RoutingResolution,
		&r.CallbackOnAI, &r.MachineDetection, &r.HoldState, &r.RecordingControlState, &r.ControlError, &r.RecordingRequestedAt,
		&r.MaxDurationSec, &r.DurationStartedAt, &r.ConnectedDeadlineAt)
	return r, err
}

func callReadIDs(project string, calls []callRow) ([]string, []any) {
	ids := make([]string, 0, len(calls))
	args := []any{project}
	seen := map[string]bool{}
	for _, row := range calls {
		if !seen[row.ID] {
			seen[row.ID] = true
			ids = append(ids, row.ID)
			args = append(args, row.ID)
		}
	}
	return ids, args
}

type phoneReadOwner struct{ principal, destination string }
type phoneReadPermissions struct {
	covered      map[string]bool
	owners       map[string]phoneReadOwner
	offers       map[string][]ringOffer
	runs         map[string]bool
	destinations map[string]bool
}

func (a *App) phoneReadOwner(p *phonePrincipal, r *callRow) (string, string, error) {
	if m := p.readPermissions; m != nil && m.covered[r.ID] {
		o := m.owners[r.ID]
		return o.principal, o.destination, nil
	}
	return a.phoneOwner(r.ID)
}
func (a *App) phoneReadOffers(p *phonePrincipal, r *callRow) ([]ringOffer, int, error) {
	if m := p.readPermissions; m != nil && m.covered[r.ID] {
		n := 0
		if m.runs[r.ID] {
			n = 1
		}
		return m.offers[r.ID], n, nil
	}
	offers, err := a.db().activeRingOffers(r.ID, r.ProjectID)
	if err != nil {
		return nil, 0, err
	}
	if len(offers) > 0 {
		return offers, 0, nil
	}
	// Preserve the direct-destination fallback check used by Answer.
	var n int
	err = a.db().db.QueryRow(`SELECT COUNT(*) FROM call_ring_runs WHERE call_id=? AND project_id=?`, r.ID, r.ProjectID).Scan(&n)
	return offers, n, err
}
func (a *App) phoneReadDestination(p *phonePrincipal, project, dest string) bool {
	if m := p.readPermissions; m != nil {
		if allowed, ok := m.destinations[dest]; ok {
			return allowed
		}
	}
	return a.destinationAllowsIdentity(project, dest, p.Identity)
}

// Queries are closed before the next query: the SDK intentionally uses a
// single SQLite connection. All metadata is bounded to the candidate page.
func (a *App) loadPhoneReadPermissions(project string, calls []callRow, p *phonePrincipal) (*phoneReadPermissions, error) {
	m := &phoneReadPermissions{covered: map[string]bool{}, owners: map[string]phoneReadOwner{}, offers: map[string][]ringOffer{}, runs: map[string]bool{}, destinations: map[string]bool{}}
	ids, args := callReadIDs(project, calls)
	if len(ids) == 0 {
		return m, nil
	}
	in := phonePlaceholders(len(ids))
	destinations := map[string]bool{}
	for _, r := range calls {
		m.covered[r.ID] = true
		destinations[r.RoutingDestinationID] = true
	}
	read := func(query string, args []any, scan func(*sql.Rows) error) error {
		rows, err := a.db().db.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err = scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := read(`SELECT call_id,principal,destination_id FROM telephony_call_owners WHERE project_id=? AND call_id IN (`+in+`)`, args, func(s *sql.Rows) error {
		var id string
		var o phoneReadOwner
		err := s.Scan(&id, &o.principal, &o.destination)
		m.owners[id] = o
		destinations[o.destination] = true
		return err
	}); err != nil {
		return nil, err
	}
	if err := read(`SELECT DISTINCT call_id FROM call_ring_runs WHERE project_id=? AND call_id IN (`+in+`)`, args, func(s *sql.Rows) error {
		var id string
		err := s.Scan(&id)
		m.runs[id] = true
		return err
	}); err != nil {
		return nil, err
	}
	offerArgs := append(append([]any{}, args...), ringTime(time.Now()))
	if err := read(`SELECT o.call_id,o.id,o.destination_id,o.destination_name,o.kind,o.agent_id,o.expires_at FROM call_offers o JOIN call_ring_runs r ON r.id=o.run_id WHERE o.project_id=? AND o.call_id IN (`+in+`) AND o.status='offered' AND r.status='ringing' AND o.expires_at>? ORDER BY o.position`, offerArgs, func(s *sql.Rows) error {
		var id string
		var o ringOffer
		err := s.Scan(&id, &o.ID, &o.DestinationID, &o.Name, &o.Kind, &o.AgentID, &o.ExpiresAt)
		m.offers[id] = append(m.offers[id], o)
		destinations[o.DestinationID] = true
		return err
	}); err != nil {
		return nil, err
	}
	if p != nil {
		destArgs := []any{project}
		for d := range destinations {
			m.destinations[d] = false
			destArgs = append(destArgs, d)
		}
		if err := read(`SELECT id,config_json FROM routing_destinations WHERE project_id=? AND id IN (`+phonePlaceholders(len(destArgs)-1)+`)`, destArgs, func(s *sql.Rows) error {
			var id, raw string
			if err := s.Scan(&id, &raw); err != nil {
				return err
			}
			c, err := readDestinationCapacity(raw)
			m.destinations[id] = err == nil && (c.Limit == 0 || c.Identity == p.Identity)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Only selected calls survive in the cache. Ineligible candidate pages may be
// large, but cannot make each retained entry grow with project history.
func selectedReadPermissions(source *phoneReadPermissions, rows []callRow) *phoneReadPermissions {
	m := &phoneReadPermissions{covered: map[string]bool{}, owners: map[string]phoneReadOwner{}, offers: map[string][]ringOffer{}, runs: map[string]bool{}, destinations: map[string]bool{}}
	addDestination := func(id string) {
		if allowed, ok := source.destinations[id]; ok {
			m.destinations[id] = allowed
		}
	}
	for _, row := range rows {
		m.covered[row.ID] = true
		m.owners[row.ID] = source.owners[row.ID]
		m.offers[row.ID] = source.offers[row.ID]
		m.runs[row.ID] = source.runs[row.ID]
		addDestination(row.RoutingDestinationID)
		addDestination(source.owners[row.ID].destination)
		for _, offer := range source.offers[row.ID] {
			addDestination(offer.DestinationID)
		}
	}
	return m
}

type callReadModel struct {
	rows        []callRow
	permissions *phoneReadPermissions
	until       time.Time
}
type callReadEntry struct {
	revision int64
	model    callReadModel
	ready    chan struct{}
	err      error
}
type callReadCache struct {
	mu      sync.Mutex
	entries map[string]*callReadEntry
}

const callReadCacheLimit = 128

// Scope keys include every grant and namespaced identity, not just subject ID.
// Authentication still runs on every HTTP request and stream wake. The cache
// is strictly read-only and is never consulted by Answer, attach or controls.
func callReadScope(r *http.Request, project string, limit int, hint bool) string {
	p := phoneUserFrom(r)
	scope := struct {
		Project                                            string
		Limit                                              int
		Hint                                               bool
		Identity                                           phoneIdentity
		Revision                                           int64
		Supervisor, Listen, ListenScope, Coach, CoachScope bool
		Destinations, Numbers                              []string
	}{Project: project, Limit: limit, Hint: hint}
	if p != nil {
		scope.Identity = p.Identity
		scope.Revision = p.Revision
		scope.Supervisor = p.Supervisor
		scope.Listen = p.Listen
		scope.ListenScope = p.ListenScope
		scope.Coach = p.Coach
		scope.CoachScope = p.CoachScope
		scope.Destinations = phoneGrantKeys(p.Destinations)
		scope.Numbers = phoneGrantKeys(p.Numbers)
	}
	raw, _ := json.Marshal(scope)
	return string(raw)
}
func readRequest(r *http.Request, m *phoneReadPermissions) *http.Request {
	if p := phoneUserFrom(r); p != nil {
		copy := *p
		copy.readPermissions = m
		return withPhonePrincipal(r, &copy)
	}
	return r
}
func (a *App) phoneCallRead(r *http.Request, project string, limit int, hint bool) (callReadModel, *http.Request, error) {
	var revision int64
	db := a.db().db
	if err := db.QueryRow(`SELECT COALESCE((SELECT revision FROM telephony_read_versions WHERE project_id=?),0)`, project).Scan(&revision); err != nil {
		return callReadModel{}, r, err
	}
	key := callReadScope(r, project, limit, hint)
	cache := &a.callReads
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[string]*callReadEntry{}
	}
	if entry := cache.entries[key]; entry != nil && entry.revision == revision {
		if entry.ready != nil {
			ready := entry.ready
			cache.mu.Unlock()
			select {
			case <-r.Context().Done():
				return callReadModel{}, r, r.Context().Err()
			case <-ready:
			}
			// Entries are immutable after completion, synchronized by channel close.
			if entry.err != nil {
				return callReadModel{}, r, entry.err
			}
			if time.Now().Before(entry.model.until) {
				return entry.model, readRequest(r, entry.model.permissions), nil
			}
			return a.phoneCallRead(r, project, limit, hint)
		}
		if time.Now().Before(entry.model.until) {
			model := entry.model
			cache.mu.Unlock()
			return model, readRequest(r, model.permissions), nil
		}
	}
	// Bounded storage; clearing only read results is safe. In-flight callers keep
	// their own entry and receive its result, even if eviction occurs.
	if len(cache.entries) >= callReadCacheLimit {
		cache.entries = map[string]*callReadEntry{}
	}
	entry := &callReadEntry{revision: revision, ready: make(chan struct{})}
	cache.entries[key] = entry
	cache.mu.Unlock()
	model, err := a.loadPhoneCallRead(r, project, limit, hint)
	cache.mu.Lock()
	entry.model = model
	entry.err = err
	close(entry.ready)
	entry.ready = nil
	if err != nil && cache.entries[key] == entry {
		delete(cache.entries, key)
	}
	cache.mu.Unlock()
	return model, readRequest(r, model.permissions), err
}
func (a *App) loadPhoneCallRead(r *http.Request, project string, limit int, hint bool) (callReadModel, error) {
	model := callReadModel{rows: []callRow{}, permissions: &phoneReadPermissions{covered: map[string]bool{}, owners: map[string]phoneReadOwner{}, offers: map[string][]ringOffer{}, runs: map[string]bool{}, destinations: map[string]bool{}}}
	ttl := 250 * time.Millisecond
	if hint {
		ttl = callStreamLease
	}
	model.until = time.Now().Add(ttl)
	p := phoneUserFrom(r)
	columns := callListColumns
	if hint {
		columns = callHintColumns
	}
	for offset := 0; len(model.rows) < limit; offset += 200 {
		where := `project_id=? AND ingress_path<>'ring_group'`
		args := []any{project}
		if p != nil {
			candidates, candidateArgs := phoneCallCandidates(p, project)
			where = `id IN (` + candidates + `) AND ` + where
			args = append(candidateArgs, project)
		}
		rows, err := a.db().db.Query(`SELECT `+columns+` FROM calls WHERE `+where+` ORDER BY placed_at DESC,id DESC LIMIT 200 OFFSET ?`, append(args, offset)...)
		if err != nil {
			return model, err
		}
		batch := []callRow{}
		for rows.Next() {
			row, e := scanCallList(rows, hint)
			if e != nil {
				rows.Close()
				return model, e
			}
			batch = append(batch, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return model, err
		}
		metadata, err := a.loadPhoneReadPermissions(project, batch, p)
		if err != nil {
			return model, err
		}
		for id, o := range metadata.owners {
			model.permissions.owners[id] = o
		}
		for id, o := range metadata.offers {
			model.permissions.offers[id] = o
			for _, offer := range o {
				if expires, e := time.Parse(time.RFC3339Nano, offer.ExpiresAt); e == nil && expires.Before(model.until) {
					model.until = expires
				}
			}
		}
		for id, v := range metadata.runs {
			model.permissions.runs[id] = v
		}
		for id, v := range metadata.covered {
			model.permissions.covered[id] = v
		}
		for id, v := range metadata.destinations {
			model.permissions.destinations[id] = v
		}
		for i := range batch {
			if batch[i].Status == "pending" {
				batch[i].RingOffers = metadata.offers[batch[i].ID]
			}
			if requested, e := time.Parse(time.RFC3339Nano, batch[i].RecordingRequestedAt); e == nil {
				boundary := requested.Add(30 * time.Second)
				if time.Now().Before(boundary) && boundary.Before(model.until) {
					model.until = boundary
				}
			}
		}
		model.rows = append(model.rows, a.filterPhoneCalls(readRequest(r, metadata), batch)...)
		if len(batch) < 200 {
			break
		}
	}
	if len(model.rows) > limit {
		model.rows = model.rows[:limit]
	}
	model.permissions = selectedReadPermissions(model.permissions, model.rows)
	if !hint && len(model.rows) > 0 {
		// Project settings are fetched once, rather than twice for every call row.
		var music string
		var file int64
		err := a.db().db.QueryRow(`SELECT hold_music_url,hold_music_storage_file_id FROM call_control_settings WHERE project_id=?`, project).Scan(&music, &file)
		if err != nil && err != sql.ErrNoRows {
			return model, err
		}
		for i := range model.rows {
			model.rows[i].HoldMusicURL = music
			model.rows[i].HoldMusicStorageFileID = file
		}
		if err = a.db().attachRecordingSummaries(project, model.rows); err != nil {
			return model, err
		}
	}
	return model, nil
}
