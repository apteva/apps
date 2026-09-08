package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// File ids in the payload are authoritative input too. A caller cannot hide a
// reference by omitting it from the auxiliary attachment_file_ids array.
func validateWorkerPayload(db *sql.DB, gigID, assignmentID int64, payload map[string]any, supplied []int64, complete bool) ([]int64, error) {
	var raw string
	if err := db.QueryRow(`SELECT derived_result_schema_json FROM gigs WHERE id=?`, gigID).Scan(&raw); err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := parseJSON(raw, &schema); err != nil {
		return nil, err
	}
	props, _ := schema["properties"].(map[string]any)
	for key, value := range payload {
		if key == "instruction_responses" {
			continue
		}
		if _, ok := props[key]; !ok {
			return nil, fmt.Errorf("unknown response field %q", key)
		}
		for _, id := range draftAttachmentIDs(map[string]any{"value": value}) {
			requirement, err := loadGigFileRequirement(db, gigID, key)
			if err != nil {
				return nil, err
			}
			if requirement == nil {
				return nil, fmt.Errorf("field %q does not accept files", key)
			}
			if err := validateInstructionUpload(db, assignmentID, key, submittedFileRef{StorageFileID: id}, requirement.Spec.Files); err != nil {
				return nil, err
			}
		}
	}
	ids := draftAttachmentIDs(payload)
	all := append(append([]int64{}, ids...), supplied...)
	if err := validateSubmissionAttachments(db, assignmentID, all); err != nil {
		return nil, err
	}
	if complete {
		if err := validateSubmission(db, gigID, assignmentID, payload); err != nil {
			return nil, err
		}
	} else if err := validateInstructionResponses(db, gigID, assignmentID, payload, false); err != nil {
		return nil, err
	}
	// Preserve valid legacy explicitly-listed attachments while deriving all new
	// payload references ourselves.
	unique := map[int64]bool{}
	for _, id := range all {
		if id <= 0 {
			return nil, errors.New("invalid attachment id")
		}
		unique[id] = true
	}
	ids = ids[:0]
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	stripWorkerSignedURLs(payload)
	return ids, nil
}

func stripWorkerSignedURLs(value any) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "signed_url")
		for _, child := range v {
			stripWorkerSignedURLs(child)
		}
	case []any:
		for _, child := range v {
			stripWorkerSignedURLs(child)
		}
	}
}

// One bounded batch per page; a slow/repeated attachment cannot multiply the
// number of serial cross-app requests. Never trust stored draft URL fields.
func workerSignedURLs(ctx *sdk.AppCtx, pid string, ttl int, composition []gigInstructionRow, assignmentID int64, payloads ...map[string]any) map[int64]string {
	ids := map[int64]bool{}
	for _, it := range composition {
		for _, id := range draftAttachmentIDs(it.RenderedBody) {
			ids[id] = true
		}
	}
	allowed, _ := allowedSubmissionFiles(ctx.AppDB(), assignmentID)
	for _, payload := range payloads {
		stripWorkerSignedURLs(payload)
		for _, id := range draftAttachmentIDs(payload) {
			if allowed[id] {
				ids[id] = true
			}
		}
	}
	urls := map[int64]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u, err := storageSignedURL(ctx, pid, id, ttl)
			if err == nil {
				mu.Lock()
				urls[id] = u
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return urls
}

func applyWorkerSignedURLs(value any, urls map[int64]string) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "signed_url")
		if u := urls[int64Cast(v["storage_file_id"])]; u != "" {
			v["signed_url"] = u
		}
		for _, child := range v {
			applyWorkerSignedURLs(child, urls)
		}
	case []any:
		for _, child := range v {
			applyWorkerSignedURLs(child, urls)
		}
	}
}

type workerCompensation struct {
	WorkerAmountMinor int64   `json:"worker_amount_minor"`
	Currency          string  `json:"currency"`
	PricingModel      string  `json:"pricing_model"`
	Quantity          float64 `json:"quantity"`
	Unit              string  `json:"unit,omitempty"`
}

func publicWorkerCompensation(c *gigCompensation) *workerCompensation {
	if c == nil {
		return nil
	}
	return &workerCompensation{c.WorkerAmountMinor, c.Currency, c.PricingModel, c.Quantity, c.Unit}
}
