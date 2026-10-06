package main

import (
	"net/http"
	"time"
)

// A browser receipt is distinct from an issued offer, a successful answer
// claim, and connected media. An adviser can also explicitly decline without
// ending the caller's leg or declining another adviser's offer.
func (a *App) softphoneOfferResponse(w http.ResponseWriter, r *http.Request, project, callID string, decline bool) {
	if callID == "" {
		http.Error(w, "call_id required", 400)
		return
	}
	var request struct {
		OfferID string `json:"offer_id"`
	}
	if decodeJSONBody(r, &request) != nil || request.OfferID == "" {
		http.Error(w, "offer_id required", 400)
		return
	}
	p := phoneUserFrom(r)
	if p == nil {
		http.Error(w, "application user required", 403)
		return
	}
	unlock := a.softphones.lockClaim(callID)
	defer unlock()
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, "call unavailable", 500)
		return
	}
	if row == nil || row.ProjectID != project {
		http.NotFound(w, r)
		return
	}
	if row.Status != "pending" {
		http.Error(w, "call no longer pending", 409)
		return
	}
	offers, err := a.db().activeRingOffers(callID, project)
	if err != nil {
		http.Error(w, "offers unavailable", 500)
		return
	}
	var selected *ringOffer
	for i := range offers {
		if offers[i].ID == request.OfferID && offers[i].Kind == "browser" && p.Destinations[offers[i].DestinationID] && a.destinationAllowsIdentity(project, offers[i].DestinationID, p.Identity) {
			selected = &offers[i]
			break
		}
	}
	if selected == nil {
		http.NotFound(w, r)
		return
	}
	now := ringTime(time.Now())
	if !decline {
		res, updateErr := a.db().db.Exec(`UPDATE call_offers SET acknowledged_at=CASE WHEN acknowledged_at='' THEN ? ELSE acknowledged_at END WHERE id=? AND call_id=? AND status='offered' AND expires_at>? AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='pending')`, now, selected.ID, callID, now, callID)
		err = updateErr
		if err != nil {
			http.Error(w, "receipt unavailable", 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, "offer expired", 409)
			return
		}
		writeJSON(w, map[string]any{"offer_id": selected.ID, "acknowledged": true})
		return
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		http.Error(w, "decline unavailable", 500)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE call_offers SET status='declined',declined_at=? WHERE id=? AND call_id=? AND status='offered' AND expires_at>? AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='pending')`, now, selected.ID, callID, now, callID)
	if err != nil {
		http.Error(w, "decline unavailable", 500)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "offer expired", 409)
		return
	}
	if err = advanceRingRunTx(tx, selected.RunID, time.Now()); err != nil {
		http.Error(w, "decline unavailable", 500)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "decline unavailable", 500)
		return
	}
	a.routingCommitted(project)
	writeJSON(w, map[string]any{"offer_id": selected.ID, "declined": true})
}
