package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type resolvedComponentPayload struct {
	PartID         string          `json:"part_id"`
	SourcePartID   string          `json:"source_part_id,omitempty"`
	DesignID       int64           `json:"design_id"`
	DesignName     string          `json:"design_name"`
	RevisionID     int64           `json:"revision_id"`
	RevisionNumber int             `json:"revision_number"`
	SourceSHA256   string          `json:"source_sha256"`
	Definition     json.RawMessage `json:"definition"`
	Parameters     json.RawMessage `json:"parameters"`
}

// materializeComponentSources resolves immutable cross-design links into a
// runner-only payload. The persisted revision remains compact and contains
// only pinned source coordinates and hashes.
func (s *Service) materializeComponentSources(ownerRevisionID int64, canonical []byte) ([]byte, []ComponentDependency, error) {
	stack := map[int64]bool{}
	if ownerRevisionID > 0 {
		stack[ownerRevisionID] = true
	}
	count := 0
	return s.materializeDefinition(ownerRevisionID, canonical, stack, &count)
}

func (s *Service) materializeDefinition(ownerRevisionID int64, canonical []byte, stack map[int64]bool, count *int) ([]byte, []ComponentDependency, error) {
	if len(stack) > 16 {
		return nil, nil, errors.New("component dependency depth exceeds 16 revisions")
	}
	var definition DesignDefinition
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return nil, nil, fmt.Errorf("decode component definition: %w", err)
	}
	resolved := make([]resolvedComponentPayload, 0)
	dependencies := make([]ComponentDependency, 0)
	for _, part := range definition.Parts {
		if part.Source == nil {
			continue
		}
		source := part.Source
		*count = *count + 1
		if *count > 64 {
			return nil, nil, errors.New("component dependency graph exceeds 64 links")
		}
		revision, err := s.store.GetRevision(s.project, source.RevisionID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve part %q revision %d: %w", part.ID, source.RevisionID, err)
		}
		if revision.DesignID != source.DesignID {
			return nil, nil, fmt.Errorf("part %q source revision %d does not belong to design %d", part.ID, source.RevisionID, source.DesignID)
		}
		if revision.SourceSHA256 != source.SourceSHA256 {
			return nil, nil, fmt.Errorf("part %q source hash mismatch for revision %d", part.ID, source.RevisionID)
		}
		if stack[revision.ID] {
			return nil, nil, fmt.Errorf("component dependency cycle reaches revision %d through part %q", revision.ID, part.ID)
		}
		design, err := s.store.GetDesign(s.project, source.DesignID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve part %q design %d: %w", part.ID, source.DesignID, err)
		}
		sourceCanonical, sourceDefinition, err := normalizeDefinition(revision.Definition, s.maxOperations)
		if err != nil {
			return nil, nil, fmt.Errorf("part %q source revision is invalid: %w", part.ID, err)
		}
		if source.PartID != "" && !definitionHasPart(sourceDefinition, source.PartID) {
			return nil, nil, fmt.Errorf("part %q source revision has no part %q", part.ID, source.PartID)
		}
		sourceParameters, err := normalizeParameters(revision.Parameters, sourceDefinition)
		if err != nil {
			return nil, nil, fmt.Errorf("part %q source parameters are invalid: %w", part.ID, err)
		}
		nextStack := cloneRevisionStack(stack)
		nextStack[revision.ID] = true
		materialized, nested, err := s.materializeDefinition(revision.ID, sourceCanonical, nextStack, count)
		if err != nil {
			return nil, nil, err
		}
		resolved = append(resolved, resolvedComponentPayload{
			PartID: part.ID, SourcePartID: source.PartID, DesignID: design.ID, DesignName: design.Name,
			RevisionID: revision.ID, RevisionNumber: revision.RevisionNumber, SourceSHA256: revision.SourceSHA256,
			Definition: materialized, Parameters: sourceParameters,
		})
		dependencies = append(dependencies, ComponentDependency{
			OwnerRevisionID: ownerRevisionID, OwnerPartID: part.ID,
			SourceDesignID: design.ID, SourceDesignName: design.Name,
			SourceRevisionID: revision.ID, SourceRevisionNumber: revision.RevisionNumber,
			SourceSHA256: revision.SourceSHA256, SourcePartID: source.PartID,
		})
		dependencies = append(dependencies, nested...)
	}
	if len(resolved) == 0 {
		return canonical, dependencies, nil
	}
	var runtime map[string]any
	if err := json.Unmarshal(canonical, &runtime); err != nil {
		return nil, nil, err
	}
	runtime["_resolved_components"] = resolved
	materialized, err := json.Marshal(runtime)
	return materialized, dependencies, err
}

func definitionHasPart(definition *DesignDefinition, id string) bool {
	for _, part := range definition.Parts {
		if part.ID == id {
			return true
		}
	}
	return false
}

func cloneRevisionStack(input map[int64]bool) map[int64]bool {
	output := make(map[int64]bool, len(input)+1)
	for id, present := range input {
		output[id] = present
	}
	return output
}

type ComponentSourceUpdate struct {
	PartID         string `json:"part_id"`
	DesignID       int64  `json:"design_id"`
	FromRevisionID int64  `json:"from_revision_id"`
	ToRevisionID   int64  `json:"to_revision_id"`
	SourceSHA256   string `json:"source_sha256"`
}

func (s *Service) RefreshComponentSources(designID, parentID int64, note, author string) (*Revision, []ComponentSourceUpdate, error) {
	target, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, nil, err
	}
	if target.CurrentRevisionID != parentID {
		return nil, nil, fmt.Errorf("%w: expected %d, current is %d", errRevisionConflict, parentID, target.CurrentRevisionID)
	}
	parent, err := s.store.GetRevision(s.project, parentID)
	if err != nil {
		return nil, nil, err
	}
	if parent.DesignID != designID {
		return nil, nil, errors.New("expected parent does not belong to design")
	}
	_, definition, err := normalizeDefinition(parent.Definition, s.maxOperations)
	if err != nil {
		return nil, nil, err
	}
	updates := []ComponentSourceUpdate{}
	for index := range definition.Parts {
		source := definition.Parts[index].Source
		if source == nil {
			continue
		}
		design, err := s.store.GetDesign(s.project, source.DesignID)
		if err != nil {
			return nil, nil, fmt.Errorf("refresh part %q: %w", definition.Parts[index].ID, err)
		}
		if design.CurrentRevisionID == source.RevisionID {
			continue
		}
		current := design.CurrentRevision
		if current == nil {
			return nil, nil, fmt.Errorf("source design %d has no current revision", design.ID)
		}
		updates = append(updates, ComponentSourceUpdate{
			PartID: definition.Parts[index].ID, DesignID: design.ID,
			FromRevisionID: source.RevisionID, ToRevisionID: current.ID, SourceSHA256: current.SourceSHA256,
		})
		source.RevisionID = current.ID
		source.SourceSHA256 = current.SourceSHA256
	}
	if len(updates) == 0 {
		return parent, updates, nil
	}
	body, err := json.Marshal(definition)
	if err != nil {
		return nil, nil, err
	}
	canonical, _, err := normalizeDefinition(body, s.maxOperations)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := s.materializeComponentSources(0, canonical); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(note) == "" {
		note = fmt.Sprintf("Refresh %d linked component revision(s)", len(updates))
	}
	revision, err := s.store.CreateRevision(s.project, CreateRevisionInput{
		DesignID: designID, ExpectedParent: parentID, Definition: canonical, Parameters: parent.Parameters,
		Note: note, Author: author,
	})
	return revision, updates, err
}
