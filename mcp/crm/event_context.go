package main

import (
	"database/sql"
	"sort"
	"strings"
)

// sqlRowsQuerier is implemented by both *sql.DB and *sql.Tx. Event payloads
// built inside the inbound transaction must observe the same membership state
// that will commit with the activity.
type sqlRowsQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// dbActiveListIDsForContact returns a stable point-in-time membership snapshot.
// Archived lists are deliberately excluded: list_ids describes audiences that
// can act on the contact now, not historical membership records.
func dbActiveListIDsForContact(db sqlRowsQuerier, pid string, contactID int64) ([]int64, error) {
	ids := []int64{}
	if db == nil || strings.TrimSpace(pid) == "" || contactID == 0 {
		return ids, nil
	}
	rows, err := db.Query(
		`SELECT m.list_id
		   FROM contact_list_members m
		   JOIN contact_lists l
		     ON l.id = m.list_id AND l.project_id = m.project_id
		  WHERE m.project_id = ? AND m.contact_id = ? AND l.archived_at IS NULL
		  ORDER BY m.list_id`,
		pid, contactID,
	)
	if err != nil {
		return ids, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return []int64{}, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// normalizeEventListIDs gives every enriched event a deterministic, unique,
// non-nil array. Stable arrays simplify workflow filters and event signatures.
func normalizeEventListIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) < 2 {
		return out
	}
	n := 1
	for i := 1; i < len(out); i++ {
		if out[i] == out[n-1] {
			continue
		}
		out[n] = out[i]
		n++
	}
	return out[:n]
}

func eventListIDsFromAny(raw any) []int64 {
	ids := []int64{}
	switch values := raw.(type) {
	case []int64:
		ids = append(ids, values...)
	case []int:
		for _, value := range values {
			ids = append(ids, int64(value))
		}
	case []any:
		for _, value := range values {
			if id := int64FromAny(value); id != 0 {
				ids = append(ids, id)
			}
		}
	}
	return normalizeEventListIDs(ids)
}

func crmEventContactID(topic string, payload map[string]any) int64 {
	switch topic {
	case "contact.added", "contact.updated":
		return int64FromAny(payload["id"])
	case "contact.channel.deliverability.changed",
		"contact.activity.added",
		"conversation.status.changed",
		"conversation.message.received":
		return int64FromAny(payload["contact_id"])
	default:
		if strings.HasPrefix(topic, "opportunity.") {
			return int64FromAny(payload["contact_id"])
		}
	}
	return 0
}

func eventNeedsAttribution(topic string) bool {
	return topic == "contact.activity.added" || topic == "conversation.message.received"
}

// enrichCRMEventListContext applies the shared contract for contact-scoped
// events. Explicit snapshots supplied by a transactional caller win; all other
// paths load the current active memberships immediately before enqueueing.
func enrichCRMEventListContext(db sqlRowsQuerier, pid, topic string, payload map[string]any) error {
	if payload == nil {
		return nil
	}
	contactID := crmEventContactID(topic, payload)
	if contactID != 0 {
		if raw, supplied := payload["list_ids"]; supplied {
			payload["list_ids"] = eventListIDsFromAny(raw)
		} else {
			ids, err := dbActiveListIDsForContact(db, pid, contactID)
			if err != nil {
				return err
			}
			payload["list_ids"] = ids
		}
	}
	if eventNeedsAttribution(topic) {
		payload["attributed_list_ids"] = eventListIDsFromAny(payload["attributed_list_ids"])
	}
	return nil
}

// preserveCRMEventListShape is the best-effort fallback for non-transactional
// callers. A lookup failure must not discard the base domain event.
func preserveCRMEventListShape(topic string, payload map[string]any) {
	if payload == nil {
		return
	}
	if crmEventContactID(topic, payload) != 0 {
		if _, ok := payload["list_ids"]; !ok {
			payload["list_ids"] = []int64{}
		}
	}
	if eventNeedsAttribution(topic) {
		if _, ok := payload["attributed_list_ids"]; !ok {
			payload["attributed_list_ids"] = []int64{}
		}
	}
}
