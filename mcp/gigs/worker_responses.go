package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type workerDraft struct {
	AssignmentID      int64          `json:"assignment_id"`
	Payload           map[string]any `json:"payload"`
	AttachmentFileIDs []int64        `json:"attachment_file_ids"`
	Revision          int            `json:"revision"`
	UpdatedAt         string         `json:"updated_at"`
}

type gigResponseRequirement struct {
	SortOrder int
	Key       string
	Spec      responseSpec
}

type submittedInstructionResponse struct {
	Note  string
	Files []submittedFileRef
}

type submittedFileRef struct {
	StorageFileID int64
	Filename      string
	Mime          string
}

func loadWorkerDraft(db *sql.DB, assignmentID int64) (*workerDraft, error) {
	draft := &workerDraft{AssignmentID: assignmentID}
	var payloadJSON, attachmentJSON string
	err := db.QueryRow(`SELECT payload_json, attachment_file_ids_json, revision, COALESCE(updated_at,'')
		FROM gig_assignment_drafts WHERE assignment_id=?`, assignmentID).
		Scan(&payloadJSON, &attachmentJSON, &draft.Revision, &draft.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = parseJSON(payloadJSON, &draft.Payload)
	_ = parseJSON(attachmentJSON, &draft.AttachmentFileIDs)
	if draft.Payload == nil {
		draft.Payload = map[string]any{}
	}
	return draft, nil
}

func saveWorkerDraft(db *sql.DB, assignmentID int64, payload map[string]any, attachmentIDs []int64) (*workerDraft, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	_, err := db.Exec(`INSERT INTO gig_assignment_drafts
		(assignment_id,payload_json,attachment_file_ids_json,revision,updated_at)
		VALUES (?,?,?,1,CURRENT_TIMESTAMP)
		ON CONFLICT(assignment_id) DO UPDATE SET
		payload_json=excluded.payload_json,
		attachment_file_ids_json=excluded.attachment_file_ids_json,
		revision=gig_assignment_drafts.revision+1,
		updated_at=CURRENT_TIMESTAMP`, assignmentID, mustJSON(payload), mustJSON(attachmentIDs))
	if err != nil {
		return nil, err
	}
	return loadWorkerDraft(db, assignmentID)
}

func loadGigResponseRequirements(db *sql.DB, gigID int64) ([]gigResponseRequirement, error) {
	rows, err := db.Query(`SELECT sort_order, COALESCE(result_key,''), rendered_body_json
		FROM gig_instructions WHERE gig_id=? ORDER BY sort_order`, gigID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []gigResponseRequirement{}
	for rows.Next() {
		var sortOrder int
		var resultKey, bodyJSON string
		if err := rows.Scan(&sortOrder, &resultKey, &bodyJSON); err != nil {
			return nil, err
		}
		body := map[string]any{}
		_ = parseJSON(bodyJSON, &body)
		spec := responseSpecFromBody(body)
		if !spec.Note.Enabled && !spec.Files.Enabled && !spec.LegacyAnyRequired {
			continue
		}
		out = append(out, gigResponseRequirement{
			SortOrder: sortOrder,
			Key:       instructionResponseKey(resultKey, sortOrder),
			Spec:      spec,
		})
	}
	return out, rows.Err()
}

func loadGigResponseRequirement(db *sql.DB, gigID int64, key string) (*gigResponseRequirement, error) {
	requirements, err := loadGigResponseRequirements(db, gigID)
	if err != nil {
		return nil, err
	}
	for i := range requirements {
		if requirements[i].Key == key {
			return &requirements[i], nil
		}
	}
	return nil, nil
}

func loadGigFileRequirement(db *sql.DB, gigID int64, key string) (*gigResponseRequirement, error) {
	rows, err := db.Query(`SELECT sort_order, COALESCE(result_key,''), instruction_kind, rendered_body_json
		FROM gig_instructions WHERE gig_id=? ORDER BY sort_order`, gigID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sortOrder int
		var resultKey, kind, bodyJSON string
		if err := rows.Scan(&sortOrder, &resultKey, &kind, &bodyJSON); err != nil {
			return nil, err
		}
		resolvedKey := instructionResponseKey(resultKey, sortOrder)
		if resolvedKey != key {
			continue
		}
		body := map[string]any{}
		_ = parseJSON(bodyJSON, &body)
		spec := responseSpecFromBody(body)
		if spec.Files.Enabled {
			return &gigResponseRequirement{SortOrder: sortOrder, Key: key, Spec: spec}, nil
		}
		fileSpec, ok := structuredFileResponseSpec(kind, body)
		if !ok {
			return nil, nil
		}
		return &gigResponseRequirement{SortOrder: sortOrder, Key: key, Spec: responseSpec{Files: fileSpec}}, nil
	}
	return nil, rows.Err()
}

func structuredFileResponseSpec(kind string, body map[string]any) (responseFileSpec, bool) {
	accept := []string{}
	switch kind {
	case kindInputPhoto:
		accept = []string{"image/*"}
	case kindInputAudioRecording:
		accept = []string{"audio/*"}
	case kindInputVideoRecording:
		accept = []string{"video/*"}
	case kindInputFile, kindInputSignature:
		if raw := strings.TrimSpace(strOf(body["accept_mime"])); raw != "" {
			for _, value := range strings.Split(raw, ",") {
				if value = strings.TrimSpace(value); value != "" {
					accept = append(accept, value)
				}
			}
		}
	default:
		return responseFileSpec{}, false
	}
	return responseFileSpec{
		Enabled:  true,
		Required: isRequired(kind, body),
		Accept:   accept,
		MinItems: 1,
		MaxItems: 1,
	}, true
}

func parseSubmittedInstructionResponses(payload map[string]any) map[string]submittedInstructionResponse {
	out := map[string]submittedInstructionResponse{}
	raw, _ := payload["instruction_responses"].([]any)
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		key := strings.TrimSpace(strOf(entry["key"]))
		if key == "" {
			continue
		}
		parsed := submittedInstructionResponse{Note: strings.TrimSpace(strOf(entry["note"]))}
		if files, ok := entry["files"].([]any); ok {
			for _, file := range files {
				m, _ := file.(map[string]any)
				if id := int64Cast(m["storage_file_id"]); id > 0 {
					parsed.Files = append(parsed.Files, submittedFileRef{
						StorageFileID: id,
						Filename:      strings.TrimSpace(strOf(m["filename"])),
						Mime:          strings.TrimSpace(strOf(m["mime"])),
					})
				}
			}
		}
		out[key] = parsed
	}
	return out
}

func validateInstructionResponses(db *sql.DB, gigID, assignmentID int64, payload map[string]any, requireComplete bool) error {
	requirements, err := loadGigResponseRequirements(db, gigID)
	if err != nil {
		return err
	}
	responses := parseSubmittedInstructionResponses(payload)
	for _, requirement := range requirements {
		response := responses[requirement.Key]
		spec := requirement.Spec
		step := requirement.SortOrder + 1
		if requireComplete && spec.LegacyAnyRequired && response.Note == "" && len(response.Files) == 0 {
			return fmt.Errorf("missing required response for step %d", step)
		}
		if requireComplete && spec.Note.Required && response.Note == "" {
			return fmt.Errorf("step %d requires a note", step)
		}
		if !spec.Note.Enabled && response.Note != "" {
			return fmt.Errorf("step %d does not accept a note", step)
		}
		if !spec.Files.Enabled && len(response.Files) > 0 {
			return fmt.Errorf("step %d does not accept files", step)
		}
		if requireComplete && spec.Files.Required && len(response.Files) < spec.Files.MinItems {
			return fmt.Errorf("step %d requires at least %d file(s)", step, spec.Files.MinItems)
		}
		if len(response.Files) > 0 && len(response.Files) < spec.Files.MinItems {
			return fmt.Errorf("step %d requires at least %d file(s) when files are provided", step, spec.Files.MinItems)
		}
		if spec.Files.MaxItems > 0 && len(response.Files) > spec.Files.MaxItems {
			return fmt.Errorf("step %d accepts at most %d file(s)", step, spec.Files.MaxItems)
		}
		for _, file := range response.Files {
			if assignmentID == 0 {
				return fmt.Errorf("step %d file validation requires an assignment", step)
			}
			if err := validateInstructionUpload(db, assignmentID, requirement.Key, file, spec.Files); err != nil {
				return fmt.Errorf("step %d: %w", step, err)
			}
		}
	}
	return nil
}

func validateInstructionUpload(db *sql.DB, assignmentID int64, instructionKey string, file submittedFileRef, spec responseFileSpec) error {
	var storedKey, filename, contentType string
	var sizeBytes int64
	err := db.QueryRow(`SELECT COALESCE(instruction_key,''), COALESCE(filename,''),
		COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM gig_upload_sessions
		WHERE assignment_id=? AND storage_file_id=? AND status='completed' AND discarded_at IS NULL
		ORDER BY completed_at DESC LIMIT 1`, assignmentID, file.StorageFileID).
		Scan(&storedKey, &filename, &contentType, &sizeBytes)
	if errors.Is(err, sql.ErrNoRows) {
		// Files from an older submitted revision remain valid even if their
		// original v0.2 upload session did not carry an instruction key.
		previous, err := submissionReferencesFile(db, assignmentID, file.StorageFileID)
		if err != nil {
			return err
		}
		if !previous {
			return fmt.Errorf("file %d was not uploaded for this assignment", file.StorageFileID)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if storedKey != "" && storedKey != instructionKey {
		return fmt.Errorf("file %d belongs to response %q", file.StorageFileID, storedKey)
	}
	if filename == "" {
		filename = file.Filename
	}
	if contentType == "" {
		contentType = file.Mime
	}
	return responseAcceptsFile(spec, filename, contentType, sizeBytes)
}

func draftAttachmentIDs(payload map[string]any) []int64 {
	ids := map[int64]bool{}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if id := int64Cast(typed["storage_file_id"]); id > 0 {
				ids[id] = true
			}
			for _, child := range typed {
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(payload)
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}

func submissionReferencesFile(db *sql.DB, assignmentID, fileID int64) (bool, error) {
	rows, err := db.Query(`SELECT COALESCE(attachment_file_ids_json,'[]')
		FROM gig_submissions WHERE assignment_id=?`, assignmentID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var ids []int64
		_ = parseJSON(raw, &ids)
		for _, id := range ids {
			if id == fileID {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}
