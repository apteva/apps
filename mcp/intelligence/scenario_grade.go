package main

// Causal grading: what do a link's sources actually say? The grader reads
// the headlines behind each (from, type, to) link and records whether they
// describe a mechanism, assert the relation, merely mention the entities
// together, or do not support it. This grades text, not causality: a
// mechanism a headline describes can still be wrong.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const gradeSystemPrompt = `You grade what news headlines say about a stated relationship between two entities. You do not judge whether it is true or causal in reality, only what the cited headlines support.
For each numbered relationship, pick one basis:
- mechanism: a headline says how one entity affects the other (e.g. "strike at port halts copper shipments to smelter")
- asserted: a headline states the relationship but not how it works
- co_occurrence: the entities appear together but no relationship of this type is stated
- unsupported: the headlines do not support this relationship (or contradict it)
Be strict: if unsure between two, choose the weaker. Reply with JSON only:
{"grades":[{"n":1,"basis":"mechanism|asserted|co_occurrence|unsupported","note":"one short reason quoting at most a few words"}]}`

type gradeTarget struct {
	key              string
	from, typ, to    string
	ids, evidenceIDs []int64
}

// ungradedLinks groups ungraded, live, non-synthetic relationship rows by
// (from, type, to), oldest first.
func ungradedLinks(db *sql.DB, pid string) ([]*gradeTarget, error) {
	rows, err := db.Query(`SELECT r.id, r.from_entity_id, f.canonical, r.rel_type, r.to_entity_id, t.canonical, r.evidence_ids
		FROM intel_relationships r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE r.project_id = ? AND r.causal_basis = '' AND r.status <> 'assumed' AND r.basis <> ? AND r.retracted_at IS NULL ORDER BY r.id`, pid, basisSynthetic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]*gradeTarget{}
	order := []*gradeTarget{}
	for rows.Next() {
		var id, from, to int64
		var fname, typ, tname, ev string
		if err := rows.Scan(&id, &from, &fname, &typ, &to, &tname, &ev); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d|%s|%d", from, typ, to)
		g := byKey[key]
		if g == nil {
			g = &gradeTarget{key: key, from: fname, typ: typ, to: tname}
			byKey[key] = g
			order = append(order, g)
		}
		g.ids = append(g.ids, id)
		var ids []int64
		_ = json.Unmarshal([]byte(ev), &ids)
		for _, e := range ids {
			if !containsID(g.evidenceIDs, e) {
				g.evidenceIDs = append(g.evidenceIDs, e)
			}
		}
	}
	return order, rows.Err()
}

// gradeRelationships grades up to limit ungraded links with the LLM, in
// batches of 15. Every row of a graded link gets the same grade.
func gradeRelationships(db *sql.DB, pid string, llm llmClient, limit int) (map[string]any, error) {
	if llm == nil {
		return nil, fmt.Errorf("no LLM configured: set llm_model and install the llm app, or grade links with relationship_assert causal_basis")
	}
	if limit <= 0 {
		limit = 60
	}
	if limit > 300 {
		limit = 300
	}
	targets, err := ungradedLinks(db, pid)
	if err != nil {
		return nil, err
	}
	remainingBefore := len(targets)
	if len(targets) > limit {
		targets = targets[:limit]
	}
	counts := map[string]int{}
	graded, calls := 0, 0
	var firstErr error
	gradedBy := "llm:" + llm.Model()
	for start := 0; start < len(targets); start += 15 {
		batch := targets[start:min(start+15, len(targets))]
		var prompt strings.Builder
		for i, g := range batch {
			fmt.Fprintf(&prompt, "%d. %s —%s→ %s\n", i+1, g.from, g.typ, g.to)
			briefs, err := loadEvidenceBriefs(db, pid, g.evidenceIDs)
			if err != nil {
				return nil, err
			}
			sort.Slice(briefs, func(a, b int) bool { return briefs[a].ID < briefs[b].ID })
			for j, b := range briefs {
				if j == 4 {
					fmt.Fprintf(&prompt, "   (+%d more)\n", len(briefs)-4)
					break
				}
				fmt.Fprintf(&prompt, "   - %s (%s)\n", b.Title, b.Source)
			}
		}
		calls++
		reply, err := llm.Complete(gradeSystemPrompt, prompt.String(), 1500)
		if err != nil {
			firstErr = err
			break
		}
		var parsed struct {
			Grades []struct {
				N     int    `json:"n"`
				Basis string `json:"basis"`
				Note  string `json:"note"`
			} `json:"grades"`
		}
		if err := decodeLLMJSON(reply, &parsed); err != nil {
			firstErr = fmt.Errorf("grading reply: %w", err)
			continue
		}
		now := ts(nowUTC())
		for _, gr := range parsed.Grades {
			b := strings.ToLower(strings.TrimSpace(gr.Basis))
			if gr.N < 1 || gr.N > len(batch) || !causalBases[b] || b == cbAssumed {
				continue
			}
			g := batch[gr.N-1]
			for _, id := range g.ids {
				if _, err := db.Exec(`UPDATE intel_relationships SET causal_basis = ?, causal_note = ?, graded_by = ?, graded_at = ? WHERE project_id = ? AND id = ? AND causal_basis = ''`,
					b, truncate(gr.Note, 240), gradedBy, now, pid, id); err != nil {
					return nil, err
				}
			}
			counts[b]++
			graded++
		}
	}
	out := map[string]any{"links_graded": graded, "by_causal_basis": counts, "llm_calls": calls, "graded_by": gradedBy,
		"ungraded_links_remaining": remainingBefore - graded,
		"note": "Grades describe what the cited headlines say, read by a language model; they are not causal validation."}
	if firstErr != nil {
		out["error"] = firstErr.Error()
	}
	return out, nil
}

// causalProfile counts live links by causal basis.
func causalProfile(db *sql.DB, pid string) (map[string]int, error) {
	rows, err := db.Query(`SELECT causal_basis, COUNT(*) FROM intel_relationships WHERE project_id = ? AND retracted_at IS NULL AND basis <> ? GROUP BY causal_basis`, pid, basisSynthetic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		out[orStr(b, "ungraded")] = n
	}
	return out, rows.Err()
}
